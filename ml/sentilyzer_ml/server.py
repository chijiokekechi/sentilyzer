"""gRPC server hosting the InferenceService.

Run with::

    python -m sentilyzer_ml.server
"""

from __future__ import annotations

import logging
import signal
import sys
from concurrent import futures

import grpc

from . import config as cfg
from . import inference as inf

# Generated proto modules use absolute imports rooted at `sentilyzer.v1.*`;
# our package's __init__.py prepends gen/ to sys.path so that resolves.
from sentilyzer.v1 import inference_pb2 as ipb  # noqa: E402
from sentilyzer.v1 import inference_pb2_grpc as igrpc  # noqa: E402
from sentilyzer.v1 import sentilyzer_pb2 as spb  # noqa: E402

logger = logging.getLogger("sentilyzer_ml.server")

# Keep proto label ordering aligned with our LABELS tuple.
_LABEL_TO_PROTO = {
    "negative": spb.SENTIMENT_NEGATIVE,
    "neutral": spb.SENTIMENT_NEUTRAL,
    "positive": spb.SENTIMENT_POSITIVE,
}


def _to_score(pred: inf.Prediction) -> spb.Score:
    return spb.Score(
        label=_LABEL_TO_PROTO[pred.label],
        confidence=pred.confidence,
        polarity=pred.polarity,
        probabilities=pred.as_dict(),
    )


class InferenceServicer(igrpc.InferenceServiceServicer):
    def __init__(self, backend, *, config: cfg.Config):
        self.backend = backend
        self.config = config

    def Classify(self, request: ipb.ClassifyRequest, context):  # noqa: N802
        texts = [self._truncate(t) for t in request.texts]
        if len(texts) > self.config.max_batch_size:
            context.abort(
                grpc.StatusCode.RESOURCE_EXHAUSTED,
                f"batch size {len(texts)} > max {self.config.max_batch_size}",
            )
        try:
            preds = self.backend.classify(texts)
        except Exception as exc:
            logger.exception("classify failed")
            context.abort(grpc.StatusCode.INTERNAL, f"classify failed: {exc}")
        return ipb.ClassifyResponse(scores=[_to_score(p) for p in preds])

    def ClassifyAspects(self, request: ipb.ClassifyAspectsRequest, context):  # noqa: N802
        items = [
            (self._truncate(inp.text), list(inp.aspects)) for inp in request.inputs
        ]
        total_aspects = sum(len(asps) for _, asps in items)
        if total_aspects > self.config.max_batch_size * 4:
            context.abort(
                grpc.StatusCode.RESOURCE_EXHAUSTED,
                f"aspect batch size {total_aspects} too large",
            )
        try:
            results = self.backend.classify_aspects(items)
        except Exception as exc:
            logger.exception("classify_aspects failed")
            context.abort(
                grpc.StatusCode.INTERNAL, f"classify_aspects failed: {exc}"
            )
        out = ipb.ClassifyAspectsResponse()
        for row in results:
            agg = ipb.AspectResult()
            for aspect, pred in row:
                agg.scores.append(spb.AspectScore(aspect=aspect, score=_to_score(pred)))
            out.results.append(agg)
        return out

    def Ready(self, request: ipb.ReadyRequest, context):  # noqa: N802
        return ipb.ReadyResponse(
            ready=True,
            general_model=self.config.general_model
            if not self.config.use_stub
            else "stub-heuristic",
            aspect_model=self.config.aspect_model
            if not self.config.use_stub
            else "stub-heuristic",
            device=self.config.device,
        )

    def _truncate(self, text: str) -> str:
        return (text or "")[: self.config.max_text_chars]


def serve(config: cfg.Config | None = None) -> grpc.Server:
    config = config or cfg.Config.from_env()
    backend = inf.make_backend(
        use_stub=config.use_stub,
        general_model=config.general_model,
        aspect_model=config.aspect_model,
        device=config.device,
    )
    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=4),
        options=[
            ("grpc.max_send_message_length", 16 * 1024 * 1024),
            ("grpc.max_receive_message_length", 16 * 1024 * 1024),
        ],
    )
    igrpc.add_InferenceServiceServicer_to_server(
        InferenceServicer(backend, config=config), server
    )
    server.add_insecure_port(config.listen_addr)
    return server


def main() -> int:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    config = cfg.Config.from_env()
    server = serve(config)
    server.start()
    logger.info(
        "sentilyzer-ml listening on %s (stub=%s, device=%s)",
        config.listen_addr,
        config.use_stub,
        config.device,
    )

    def _shutdown(signum, _frame):  # pragma: no cover - signal handler
        logger.info("received signal %s, shutting down", signum)
        server.stop(grace=5).wait()

    signal.signal(signal.SIGINT, _shutdown)
    signal.signal(signal.SIGTERM, _shutdown)
    server.wait_for_termination()
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
