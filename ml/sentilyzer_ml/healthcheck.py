"""Container healthcheck for the ML worker.

Calls ``InferenceService.Ready()`` with a short deadline and exits 0 when the
worker answers, 1 otherwise. This replaces a probe that merely *constructed* a
lazy gRPC channel — which always "succeeds", because the connection isn't
attempted until the first RPC, so a dead or wedged worker looked healthy.

Run as::

    python -m sentilyzer_ml.healthcheck
"""

from __future__ import annotations

import os
import sys

import grpc

# Importing this module via ``python -m sentilyzer_ml.healthcheck`` first
# imports the package, whose __init__ puts the generated gen/ root on sys.path,
# so these absolute imports (mirroring server.py) resolve.
from sentilyzer.v1 import inference_pb2 as ipb
from sentilyzer.v1 import inference_pb2_grpc as igrpc


def main() -> int:
    addr = os.getenv("SENTILYZER_ML_HEALTHCHECK_ADDR", "localhost:50051")
    try:
        with grpc.insecure_channel(addr) as channel:
            stub = igrpc.InferenceServiceStub(channel)
            resp = stub.Ready(ipb.ReadyRequest(), timeout=3)
    except grpc.RpcError as exc:
        print(f"ml healthcheck: {exc.code().name}: {exc.details()}", file=sys.stderr)
        return 1
    except Exception as exc:  # a probe must never raise
        print(f"ml healthcheck: {exc}", file=sys.stderr)
        return 1
    if not resp.ready:
        print("ml healthcheck: worker reports not ready", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
