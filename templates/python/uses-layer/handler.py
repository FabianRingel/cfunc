#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Python handler that imports a module from a mounted layer.

The layer is expected at /opt/layers/pylib (configurable via env LAYER_PATH).
PYTHONPATH must include both the cfunc SDK location and the layer.
"""
import os
import sys
import cfunc

LAYER = os.environ.get("LAYER_PATH", "/opt/layers/pylib")
if LAYER not in sys.path:
    sys.path.insert(0, LAYER)


def handle(event: cfunc.Event, ctx: cfunc.Context) -> cfunc.Response:
    import six  # provided by the layer
    return cfunc.Response(
        status=200,
        headers={"Content-Type": "application/json"},
        body={
            "six_version": six.__version__,
            "python": sys.version.split()[0],
            "layer_path": LAYER,
        },
    )


if __name__ == "__main__":
    cfunc.start(handle)
