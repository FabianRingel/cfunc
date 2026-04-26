#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""cfunc vector-search handler.

POST /fn/search  body: {"query": "...", "k": 5}
Returns: {"query": ..., "matches": [{url, content, distance}, ...]}
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import cfunc
import _lib as L


def handle(event: cfunc.Event, ctx: cfunc.Context) -> cfunc.Response:
    body = event.body or {}
    if isinstance(body, str):
        try:
            body = json.loads(body) if body else {}
        except json.JSONDecodeError:
            body = {}
    if not isinstance(body, dict):
        return cfunc.Response(status=400, body={"error": "body must be JSON object"})

    query = body.get("query")
    k = int(body.get("k", 5))
    if not query:
        return cfunc.Response(status=400, body={"error": "missing 'query'"})
    if k < 1 or k > 50:
        return cfunc.Response(status=400, body={"error": "k must be in 1..50"})

    [vec] = L.embed([query])

    with L.db() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT url, chunk_idx, content,
                       embedding <=> %s::vector AS distance
                  FROM chunks
              ORDER BY embedding <=> %s::vector
                 LIMIT %s
                """,
                (L.vec_literal(vec), L.vec_literal(vec), k),
            )
            rows = cur.fetchall()

    matches = [
        {
            "url": url,
            "chunk_idx": idx,
            "content": content[:300] + ("…" if len(content) > 300 else ""),
            "distance": float(distance),
        }
        for url, idx, content, distance in rows
    ]
    return cfunc.Response(
        status=200,
        headers={"Content-Type": "application/json"},
        body={"query": query, "k": k, "matches": matches},
    )


if __name__ == "__main__":
    cfunc.start(handle)
