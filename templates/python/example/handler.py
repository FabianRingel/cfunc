#!/usr/bin/env python3
"""Example cfunc handler in Python.

The SDK module (cfunc.py) is expected on PYTHONPATH. The gateway/runtime
arranges this by mounting the SDK layer at /opt/cfunc-sdk; for local
runs, set PYTHONPATH=$REPO/sdks/python.
"""
import cfunc


def handle(event: cfunc.Event, ctx: cfunc.Context) -> cfunc.Response:
    return cfunc.Response(
        status=200,
        headers={"Content-Type": "application/json"},
        body={
            "hello": "world",
            "method": event.method,
            "path": event.path,
            "lang": "python",
        },
    )


if __name__ == "__main__":
    cfunc.start(handle)
