# SPDX-License-Identifier: Apache-2.0

"""cfunc Python SDK — write a handler, call cfunc.start(handler).

Mirrors the Go SDK contract:

    def handle(event: Event, ctx: Context) -> Response: ...

The runtime invokes start(), which connects to the Unix socket given via
CFUNC_SOCKET, reads invoke frames, dispatches to handle(), and writes
result/error frames back.

Wire format: 4-byte big-endian length + JSON payload. Identical to the
Go implementation in internal/wire.
"""
from __future__ import annotations

import json
import os
import socket
import struct
import sys
import traceback
from dataclasses import dataclass, field
from typing import Any, Callable, Mapping, Optional


@dataclass
class Event:
    method: str = ""
    path: str = ""
    headers: Mapping[str, str] = field(default_factory=dict)
    body: Any = None  # already-decoded JSON value or string


@dataclass
class Context:
    deadline_ms: int = 0
    trace_id: str = ""


@dataclass
class Response:
    status: int = 200
    headers: Mapping[str, str] = field(default_factory=dict)
    body: Any = None  # JSON-serializable; written into the wire as raw JSON


Handler = Callable[[Event, Context], Response]


_HEADER = struct.Struct(">I")
_MAX_FRAME = 16 * 1024 * 1024


def _read_frame(sock: socket.socket) -> Optional[dict]:
    hdr = _read_exact(sock, 4)
    if hdr is None:
        return None
    (n,) = _HEADER.unpack(hdr)
    if n == 0 or n > _MAX_FRAME:
        raise ValueError(f"cfunc: invalid frame length {n}")
    payload = _read_exact(sock, n)
    if payload is None:
        raise ConnectionError("cfunc: short read on payload")
    return json.loads(payload.decode("utf-8"))


def _read_exact(sock: socket.socket, n: int) -> Optional[bytes]:
    buf = bytearray()
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            return None if not buf else None
        buf.extend(chunk)
    return bytes(buf)


def _write_frame(sock: socket.socket, frame: dict) -> None:
    payload = json.dumps(frame, separators=(",", ":")).encode("utf-8")
    if len(payload) > _MAX_FRAME:
        raise ValueError(f"cfunc: frame too large: {len(payload)}")
    sock.sendall(_HEADER.pack(len(payload)) + payload)


def start(handler: Handler) -> None:
    """Connect to CFUNC_SOCKET and serve frames until EOF."""
    sock_path = os.environ.get("CFUNC_SOCKET")
    if not sock_path:
        raise RuntimeError("cfunc: CFUNC_SOCKET not set")
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.connect(sock_path)
    try:
        _serve(sock, handler)
    finally:
        sock.close()


def _serve(sock: socket.socket, handler: Handler) -> None:
    while True:
        frame = _read_frame(sock)
        if frame is None:
            return
        ftype = frame.get("type")
        fid = frame.get("id", "")
        if ftype == "invoke":
            _handle_invoke(sock, handler, frame, fid)
        elif ftype == "init":
            _write_frame(sock, {"type": "init_ok", "id": fid})
        elif ftype == "shutdown":
            _write_frame(sock, {"type": "shutdown_ok", "id": fid})
            return
        else:
            _write_frame(sock, {
                "type": "error", "id": fid,
                "error": {"type": "ProtocolError", "message": f"unknown frame type: {ftype}"},
            })


def _handle_invoke(sock: socket.socket, handler: Handler, frame: dict, fid: str) -> None:
    try:
        ev_raw = frame.get("event") or {}
        event = Event(
            method=ev_raw.get("method", ""),
            path=ev_raw.get("path", ""),
            headers=ev_raw.get("headers") or {},
            body=ev_raw.get("body"),
        )
        cx_raw = frame.get("ctx") or {}
        ctx = Context(
            deadline_ms=int(cx_raw.get("deadline_ms", 0) or 0),
            trace_id=cx_raw.get("trace_id", ""),
        )
        resp = handler(event, ctx)
        if not isinstance(resp, Response):
            raise TypeError(f"handler must return cfunc.Response, got {type(resp).__name__}")
        result = {"status": resp.status, "headers": dict(resp.headers or {}), "body": resp.body}
        _write_frame(sock, {"type": "result", "id": fid, "result": result})
    except Exception as e:  # noqa: BLE001
        _write_frame(sock, {
            "type": "error", "id": fid,
            "error": {
                "type": type(e).__name__,
                "message": str(e),
                "stack": traceback.format_exc(),
            },
        })


__all__ = ["Event", "Context", "Response", "Handler", "start"]


if __name__ == "__main__":
    sys.stderr.write("cfunc.py is a library; import it from your handler\n")
    sys.exit(2)
