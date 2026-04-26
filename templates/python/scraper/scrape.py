#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""cfunc scrape handler — single page per invocation, recurses via the
gateway by calling /fn/scrape for each discovered link.

POST /fn/scrape  body: {
  "url":               "https://...",       (required)
  "depth":             1,                    (default 0; remaining depth budget)
  "max_links_per_page": 5,                   (fan-out cap per invocation)
  "same_host":         true,
  "include_subdomains": false,
  "skip_existing":     true,
  "gateway_url":       "http://127.0.0.1:18080"   (optional; falls back to
                                                   $CFUNC_GATEWAY_URL)
}

Each invocation:
  1. fetches and embeds its assigned URL (or returns skipped=true),
  2. if depth > 0, picks up to max_links_per_page links and POSTs each
     back to /fn/scrape with depth-1 — that's where the recursion lives.

Children run concurrently (thread pool). Each runs as its own gateway
invocation — visible in the dashboard as separate spawn / invoke events.
"""
import atexit
import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import cfunc
import _lib as L

DEFAULT_GATEWAY = os.environ.get("CFUNC_GATEWAY_URL", "http://127.0.0.1:18080")
DISPATCH_WORKERS = int(os.environ.get("CFUNC_DISPATCH_WORKERS", "16"))

# Module-global executor: dispatching child invocations is fire-and-forget,
# but bounded so a wide crawl can't spawn unbounded threads. Submitted
# work that exceeds DISPATCH_WORKERS queues internally — backpressure is
# inherent. Daemon threads exit with the SDK process.
_dispatcher = ThreadPoolExecutor(
    max_workers=DISPATCH_WORKERS,
    thread_name_prefix="cfunc-dispatch",
)
atexit.register(lambda: _dispatcher.shutdown(wait=False, cancel_futures=True))


def parse_body(event):
    body = event.body or {}
    if isinstance(body, str):
        try:
            body = json.loads(body) if body else {}
        except json.JSONDecodeError:
            body = {}
    return body if isinstance(body, dict) else {}


def handle(event: cfunc.Event, ctx: cfunc.Context) -> cfunc.Response:
    body = parse_body(event)
    url = body.get("url")
    if not url:
        return cfunc.Response(status=400, body={"error": "missing 'url'"})

    depth = int(body.get("depth", 0))
    max_links_per_page = int(body.get("max_links_per_page", 5))
    same_host_only = bool(body.get("same_host", True))
    include_subdomains = bool(body.get("include_subdomains", False))
    skip_existing = bool(body.get("skip_existing", True))
    force = bool(body.get("force", False))
    gateway_url = body.get("gateway_url") or DEFAULT_GATEWAY

    if depth < 0 or depth > 5:
        return cfunc.Response(status=400, body={"error": "depth must be 0..5"})
    if max_links_per_page < 0 or max_links_per_page > 50:
        return cfunc.Response(status=400, body={"error": "max_links_per_page must be 0..50"})

    url = L.normalize_url(url)
    t0 = time.time()
    skipped = False
    chunks_count = 0
    chars_count = 0
    html: str | None = None

    # 1. scrape (or skip) this single page
    if skip_existing and not force:
        try:
            with L.db() as conn:
                if L.url_in_db(conn, url):
                    skipped = True
        except Exception as e:
            return _err(500, f"db check: {e}")

    if not skipped:
        try:
            html = L.fetch(url)
        except Exception as e:
            return _err(502, f"fetch: {e}")

        text = L.extract_text(html)
        if not text:
            return _err(204, "no text extracted")

        chunks = L.chunk(text)
        if not chunks:
            return _err(204, "empty after chunking")

        try:
            embeddings = L.embed(chunks)
            with L.db() as conn:
                L.store_chunks(conn, url, chunks, embeddings)
        except Exception as e:
            return _err(500, f"embed/store: {e}")

        chunks_count = len(chunks)
        chars_count = sum(len(c) for c in chunks)

    # 2. recurse via gateway if there's depth left
    children_dispatched: list[str] = []
    if depth > 0:
        # We need the HTML to extract links. If we skipped (no fetch),
        # fetch now just for link discovery — no embeds, no DB writes.
        if html is None:
            try:
                html = L.fetch(url)
            except Exception as e:
                # We at least already have the page in DB; just give up
                # on link discovery for this invocation.
                return _ok(url, depth, chunks_count, chars_count, skipped, [],
                           t0, note=f"link discovery skipped: {e}")

        links = L.extract_links(html, url)
        if same_host_only:
            links = [l for l in links if L.same_host(l, url, include_subdomains)]
        # Drop self-links and (if dedup is on) URLs already in the DB.
        links = [l for l in links if l != url]
        if skip_existing and not force:
            try:
                with L.db() as conn:
                    links = [l for l in links if not L.url_in_db(conn, l)]
            except Exception:
                pass
        links = links[:max_links_per_page]
        children_dispatched = links

        if links:
            # Fire-and-forget: dispatch each child as a fresh /fn/scrape
            # invocation, but DON'T wait. Otherwise the parent would hold
            # its pool slot while children try to acquire one — deadlock
            # when fanout * depth > pool size.
            #
            # Each child becomes its own visible event in the dashboard.
            # Daemon threads ensure they don't keep the SDK process alive
            # past natural shutdown.
            child_body_template = {
                "depth": depth - 1,
                "max_links_per_page": max_links_per_page,
                "same_host": same_host_only,
                "include_subdomains": include_subdomains,
                "skip_existing": skip_existing,
                "force": force,
                "gateway_url": gateway_url,
            }
            for l in links:
                _dispatcher.submit(
                    L.call_function,
                    gateway_url, "scrape",
                    {**child_body_template, "url": l},
                    120.0,
                )

    return _ok(url, depth, chunks_count, chars_count, skipped,
               t0, dispatched=children_dispatched)


def _ok(url, depth, chunks_count, chars_count, skipped, t0,
        dispatched=None, note=None):
    payload = {
        "url": url,
        "depth_remaining": depth,
        "skipped": skipped,
        "chunks": chunks_count,
        "chars": chars_count,
        "elapsed_ms": int((time.time() - t0) * 1000),
        "dispatched": dispatched or [],
    }
    if note:
        payload["note"] = note
    return cfunc.Response(
        status=200,
        headers={"Content-Type": "application/json"},
        body=payload,
    )


def _err(status: int, message: str) -> cfunc.Response:
    return cfunc.Response(status=status, body={"error": message})


if __name__ == "__main__":
    cfunc.start(handle)
