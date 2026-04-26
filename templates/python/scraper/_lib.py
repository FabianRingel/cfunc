"""Shared utilities for the cfunc scraper functions.

Embedding model: BAAI/bge-small-en-v1.5 (384-dim, ONNX via fastembed).
The model is lazy-loaded at first call and lives in the function's
process for the rest of its warm lifetime — that's the cfunc payoff:
one cold start per function, then every invoke reuses it.
"""
from __future__ import annotations

import ipaddress
import os
import re
import socket
import threading
from contextlib import contextmanager
from typing import Iterable
from urllib.parse import urldefrag, urljoin, urlparse

import httpx
import psycopg
from bs4 import BeautifulSoup
from fastembed import TextEmbedding

DSN = os.environ.get(
    "CFUNC_PG_DSN",
    "postgresql://postgres:cfunc@127.0.0.1:5433/cfunc",
)
EMBED_MODEL = os.environ.get("CFUNC_EMBED_MODEL", "BAAI/bge-small-en-v1.5")
EMBED_DIM = 384

_embed_lock = threading.Lock()
_embed_model: TextEmbedding | None = None


def embedder() -> TextEmbedding:
    global _embed_model
    if _embed_model is None:
        with _embed_lock:
            if _embed_model is None:
                _embed_model = TextEmbedding(model_name=EMBED_MODEL)
    return _embed_model


def embed(texts: list[str]) -> list[list[float]]:
    return [list(v) for v in embedder().embed(texts)]


@contextmanager
def db():
    conn = psycopg.connect(DSN)
    try:
        yield conn
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


class UnsafeURLError(ValueError):
    """Raised when a URL resolves to a non-public address (loopback,
    link-local, RFC1918, etc.) — guards against SSRF into internal
    services and cloud metadata endpoints."""


def _check_public_host(host: str) -> None:
    """Resolve `host` and refuse if any A/AAAA record is private,
    loopback, link-local, multicast, or otherwise non-routable.

    NB: this is a TOCTOU window — between resolution here and the
    socket connect inside httpx, DNS could in theory return a different
    address. For determined attackers (DNS rebinding) the proper fix is
    a custom transport that pins the resolved IP. For ordinary misuse
    this check is enough.
    """
    try:
        infos = socket.getaddrinfo(host, None, type=socket.SOCK_STREAM)
    except socket.gaierror as e:
        raise UnsafeURLError(f"cannot resolve {host}: {e}") from e
    for info in infos:
        addr = info[4][0]
        try:
            ip = ipaddress.ip_address(addr)
        except ValueError:
            continue
        if (ip.is_private or ip.is_loopback or ip.is_link_local
                or ip.is_multicast or ip.is_reserved or ip.is_unspecified):
            raise UnsafeURLError(
                f"refusing to fetch non-public address {ip} for host {host}"
            )


def fetch(url: str, timeout: float = 15.0) -> str:
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https"):
        raise UnsafeURLError(f"unsupported scheme: {parsed.scheme!r}")
    if not parsed.hostname:
        raise UnsafeURLError(f"missing hostname in {url!r}")
    _check_public_host(parsed.hostname)
    with httpx.Client(
        timeout=timeout,
        headers={"User-Agent": "cfunc-scraper/0.1 (+https://github.com/fabianringel/cfunc)"},
        follow_redirects=True,
    ) as cl:
        r = cl.get(url)
        # Re-validate after redirects: the server may have redirected to
        # a private address. r.url is the final resolved URL.
        final = urlparse(str(r.url))
        if final.hostname and final.hostname != parsed.hostname:
            _check_public_host(final.hostname)
        r.raise_for_status()
        return r.text


def extract_text(html: str) -> str:
    soup = BeautifulSoup(html, "lxml")
    for tag in soup(["script", "style", "noscript", "header", "footer", "nav"]):
        tag.decompose()
    text = soup.get_text(separator="\n")
    text = re.sub(r"[ \t]+", " ", text)
    text = re.sub(r"\n\s*\n", "\n\n", text)
    return text.strip()


def chunk(text: str, size: int = 500, overlap: int = 80) -> list[str]:
    """Greedy chunking on paragraph boundaries with character-window
    fallback. Aims for ~size-char chunks, never splits mid-paragraph
    if it can avoid it.
    """
    paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]
    chunks: list[str] = []
    buf = ""
    for p in paragraphs:
        if not buf:
            buf = p
            continue
        if len(buf) + 2 + len(p) <= size:
            buf = buf + "\n\n" + p
        else:
            chunks.append(buf)
            buf = p
    if buf:
        chunks.append(buf)

    # Long paragraphs: hard-split with overlap.
    out: list[str] = []
    for c in chunks:
        if len(c) <= size * 1.5:
            out.append(c)
            continue
        i = 0
        while i < len(c):
            out.append(c[i : i + size])
            i += size - overlap
    return out


def vec_literal(vec: Iterable[float]) -> str:
    """pgvector accepts vectors as the string '[v1,v2,...]'."""
    return "[" + ",".join(f"{x:.6f}" for x in vec) + "]"


# --- URL utilities -------------------------------------------------------

_SKIP_SCHEMES = ("javascript:", "mailto:", "tel:", "#")
_SKIP_EXTENSIONS = (
    ".pdf", ".zip", ".tar", ".tgz", ".gz", ".bz2", ".7z",
    ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico",
    ".mp3", ".mp4", ".mov", ".avi", ".mkv",
    ".dmg", ".exe", ".msi", ".deb", ".rpm",
)


def normalize_url(url: str) -> str:
    """Drop fragment, trim trailing slash. Stable key for dedup."""
    url, _ = urldefrag(url)
    return url.rstrip("/")


def extract_links(html: str, base_url: str) -> list[str]:
    """Return absolutized, deduplicated, http(s)-only links from html."""
    soup = BeautifulSoup(html, "lxml")
    out: list[str] = []
    seen: set[str] = set()
    for a in soup.find_all("a", href=True):
        href = a["href"].strip()
        if not href or href.startswith(_SKIP_SCHEMES):
            continue
        absu = urljoin(base_url, href)
        u = urlparse(absu)
        if u.scheme not in ("http", "https") or not u.netloc:
            continue
        path_lower = u.path.lower()
        if path_lower.endswith(_SKIP_EXTENSIONS):
            continue
        n = normalize_url(absu)
        if n not in seen:
            seen.add(n)
            out.append(n)
    return out


def same_host(a: str, b: str, include_subdomains: bool = False) -> bool:
    ha = urlparse(a).netloc.lower()
    hb = urlparse(b).netloc.lower()
    if ha == hb:
        return True
    if include_subdomains:
        return ha.endswith("." + hb) or hb.endswith("." + ha)
    return False


def url_in_db(conn, url: str) -> bool:
    with conn.cursor() as cur:
        cur.execute("SELECT 1 FROM chunks WHERE url = %s LIMIT 1", (url,))
        return cur.fetchone() is not None


def call_function(gateway_url: str, name: str, body: dict, timeout: float = 60.0) -> dict:
    """Invoke another cfunc function via the gateway. Returns its parsed
    JSON body (or {"error": ...} if the call failed)."""
    url = gateway_url.rstrip("/") + "/fn/" + name
    try:
        with httpx.Client(timeout=timeout) as cl:
            r = cl.post(url, json=body)
        try:
            return r.json()
        except Exception:
            return {"error": f"non-json response (status={r.status_code})"}
    except Exception as e:
        return {"error": f"call failed: {e}"}


def store_chunks(conn, url: str, chunks: list[str], embeddings: list[list[float]]) -> None:
    """Replace any existing chunks for url with the new set."""
    with conn.cursor() as cur:
        cur.execute("DELETE FROM chunks WHERE url = %s", (url,))
        cur.executemany(
            "INSERT INTO chunks (url, chunk_idx, content, embedding) "
            "VALUES (%s, %s, %s, %s::vector)",
            [
                (url, i, content, vec_literal(vec))
                for i, (content, vec) in enumerate(zip(chunks, embeddings))
            ],
        )
