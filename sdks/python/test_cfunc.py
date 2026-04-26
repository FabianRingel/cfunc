# SPDX-License-Identifier: Apache-2.0

"""Unit tests for the Python SDK. Run with: python3 -m unittest test_cfunc"""
import json
import socket
import struct
import threading
import unittest
from pathlib import Path

import cfunc


def _make_socketpair():
    a, b = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    return a, b


def _write_frame(sock, frame):
    payload = json.dumps(frame).encode()
    sock.sendall(struct.pack(">I", len(payload)) + payload)


def _read_frame(sock):
    hdr = sock.recv(4)
    if len(hdr) < 4:
        return None
    (n,) = struct.unpack(">I", hdr)
    return json.loads(sock.recv(n))


class WireTests(unittest.TestCase):
    def test_invoke_round_trip(self):
        client, server = _make_socketpair()

        def handler(event, ctx):
            return cfunc.Response(status=200, body={"echo": event.path})

        t = threading.Thread(target=cfunc._serve, args=(server, handler))
        t.start()

        _write_frame(client, {
            "type": "invoke", "id": "r1",
            "event": {"method": "GET", "path": "/x"},
        })
        out = _read_frame(client)
        self.assertEqual(out["type"], "result")
        self.assertEqual(out["id"], "r1")
        self.assertEqual(out["result"]["status"], 200)
        self.assertEqual(out["result"]["body"]["echo"], "/x")

        client.close()
        t.join(timeout=2)

    def test_handler_exception_becomes_error_frame(self):
        client, server = _make_socketpair()

        def handler(event, ctx):
            raise RuntimeError("kapow")

        t = threading.Thread(target=cfunc._serve, args=(server, handler))
        t.start()

        _write_frame(client, {"type": "invoke", "id": "r2"})
        out = _read_frame(client)
        self.assertEqual(out["type"], "error")
        self.assertEqual(out["error"]["type"], "RuntimeError")
        self.assertEqual(out["error"]["message"], "kapow")
        self.assertIn("stack", out["error"])

        client.close()
        t.join(timeout=2)

    def test_shutdown_acknowledged_and_returns(self):
        client, server = _make_socketpair()

        t = threading.Thread(target=cfunc._serve, args=(server, lambda e, c: cfunc.Response()))
        t.start()

        _write_frame(client, {"type": "shutdown", "id": "sd"})
        out = _read_frame(client)
        self.assertEqual(out["type"], "shutdown_ok")
        t.join(timeout=2)
        self.assertFalse(t.is_alive())


if __name__ == "__main__":
    unittest.main()
