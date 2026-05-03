"""End-to-end test: spin up the gRPC server with the stub backend and call it."""

import threading
import time

import grpc
import pytest

from sentilyzer_ml import config as cfg
from sentilyzer_ml import server as srv
from sentilyzer.v1 import inference_pb2 as ipb
from sentilyzer.v1 import inference_pb2_grpc as igrpc
from sentilyzer.v1 import sentilyzer_pb2 as spb


@pytest.fixture()
def grpc_server():
    # Build config with the special "0" port; serve() will bind it.
    config = cfg.Config(
        listen_addr="127.0.0.1:0",
        general_model="stub",
        aspect_model="stub",
        device="cpu",
        max_batch_size=32,
        max_text_chars=2000,
        use_stub=True,
    )
    # Build the server manually (without serve()) so we can capture the
    # ephemeral port.
    from concurrent import futures
    import grpc as _grpc

    backend = srv.inf.make_backend(
        use_stub=True, general_model="stub", aspect_model="stub", device="cpu"
    )
    server = _grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    igrpc.add_InferenceServiceServicer_to_server(
        srv.InferenceServicer(backend, config=config), server
    )
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    # Tiny readiness loop.
    deadline = time.time() + 5
    while time.time() < deadline:
        try:
            with grpc.insecure_channel(f"127.0.0.1:{port}") as ch:
                igrpc.InferenceServiceStub(ch).Ready(ipb.ReadyRequest(), timeout=1.0)
                break
        except Exception:
            time.sleep(0.05)
    yield port
    server.stop(grace=1).wait()


def test_classify(grpc_server):
    with grpc.insecure_channel(f"127.0.0.1:{grpc_server}") as ch:
        stub = igrpc.InferenceServiceStub(ch)
        resp = stub.Classify(ipb.ClassifyRequest(texts=["I love this", "Terrible bug"]))
    assert len(resp.scores) == 2
    assert resp.scores[0].label == spb.SENTIMENT_POSITIVE
    assert resp.scores[1].label == spb.SENTIMENT_NEGATIVE


def test_classify_aspects(grpc_server):
    with grpc.insecure_channel(f"127.0.0.1:{grpc_server}") as ch:
        stub = igrpc.InferenceServiceStub(ch)
        resp = stub.ClassifyAspects(
            ipb.ClassifyAspectsRequest(
                inputs=[ipb.AspectInput(text="Battery is amazing.", aspects=["battery", "screen"])]
            )
        )
    assert len(resp.results) == 1
    assert [s.aspect for s in resp.results[0].scores] == ["battery", "screen"]


def test_ready(grpc_server):
    with grpc.insecure_channel(f"127.0.0.1:{grpc_server}") as ch:
        stub = igrpc.InferenceServiceStub(ch)
        resp = stub.Ready(ipb.ReadyRequest())
    assert resp.ready is True


def test_classify_batch_too_large(grpc_server):
    with grpc.insecure_channel(f"127.0.0.1:{grpc_server}") as ch:
        stub = igrpc.InferenceServiceStub(ch)
        with pytest.raises(grpc.RpcError) as exc:
            stub.Classify(ipb.ClassifyRequest(texts=["x"] * 100))
        assert exc.value.code() == grpc.StatusCode.RESOURCE_EXHAUSTED


# --- run with: python -m pytest tests -q ---
def test_thread_safety():
    """The stub itself is stateless; this just makes sure parallel calls don't crash."""
    from sentilyzer_ml import inference

    backend = inference.HeuristicBackend()
    errors: list[Exception] = []

    def worker():
        try:
            for _ in range(50):
                backend.classify(["I love it", "I hate it"])
        except Exception as e:  # pragma: no cover
            errors.append(e)

    threads = [threading.Thread(target=worker) for _ in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert not errors
