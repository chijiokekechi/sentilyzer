"""Serving backend for the distilled INT8 ONNX student.

Loads the artifact the training pipeline promotes (model.int8.onnx +
tokenizer.json), runs it under onnxruntime, and exposes the same
classify/classify_aspects surface the torch backends do — so the gRPC
servicer never learns which backend answered.

ManagedBackend is the piece that closes the flywheel: it routes
document-level requests to whatever student ModelManager currently holds and
falls back to the base backend (teachers or stub) when no student has been
promoted yet — and ALWAYS for aspect requests, because the student's aspect
head ships untrained until aspect labels exist in the corpus. Serving an
untrained head would be returning noise with a confident shape.
"""

from __future__ import annotations

import logging
import os
from pathlib import Path

import numpy as np

from .inference import Prediction

logger = logging.getLogger(__name__)

# Must match the training-side tokenize(max_length=...) in modal_app.train —
# the student never saw longer sequences, so serving longer ones is silently
# out-of-distribution.
MAX_SEQ_LEN = 128

MODEL_FILENAME = "model.int8.onnx"
TOKENIZER_FILENAME = "tokenizer.json"


def _pad_id(tok) -> int:
    """The tokenizer's pad id; RoBERTa's <pad>=1 as the conventional fallback."""
    for name in ("<pad>", "[PAD]", "<PAD>"):
        pid = tok.token_to_id(name)
        if pid is not None:
            return pid
    return 1


class OnnxBackend:
    """Runs the two-head student graph. CPU-only by design.

    intra_op_threads is set EXPLICITLY: onnxruntime sizes its thread pool
    from the host's physical cores and ignores cgroup quotas, so on a small
    container the default silently oversubscribes — burning cores you pay
    for while making latency worse. 2 is the measured knee for this model
    size; override via SENTILYZER_ML_ORT_THREADS.
    """

    def __init__(self, model_dir: str | Path, *, intra_op_threads: int | None = None):
        import onnxruntime as ort
        from tokenizers import Tokenizer

        model_dir = Path(model_dir)
        if intra_op_threads is None:
            intra_op_threads = int(os.getenv("SENTILYZER_ML_ORT_THREADS", "2"))

        opts = ort.SessionOptions()
        opts.intra_op_num_threads = max(1, intra_op_threads)
        self.session = ort.InferenceSession(
            str(model_dir / MODEL_FILENAME),
            sess_options=opts,
            providers=["CPUExecutionProvider"],
        )
        self.tokenizer = Tokenizer.from_file(str(model_dir / TOKENIZER_FILENAME))
        self.tokenizer.enable_truncation(max_length=MAX_SEQ_LEN)
        self.pad_id = _pad_id(self.tokenizer)

    def _run(self, encodings) -> tuple[np.ndarray, np.ndarray]:
        width = max(len(e.ids) for e in encodings)
        ids = np.array(
            [e.ids + [self.pad_id] * (width - len(e.ids)) for e in encodings],
            dtype=np.int64,
        )
        mask = np.array(
            [e.attention_mask + [0] * (width - len(e.attention_mask)) for e in encodings],
            dtype=np.int64,
        )
        logits_doc, logits_aspect = self.session.run(
            None, {"input_ids": ids, "attention_mask": mask}
        )
        return logits_doc, logits_aspect

    @staticmethod
    def _softmax(logits: np.ndarray) -> np.ndarray:
        z = logits - logits.max(axis=1, keepdims=True)
        e = np.exp(z)
        return e / e.sum(axis=1, keepdims=True)

    def classify(self, texts) -> list[Prediction]:
        texts = list(texts)
        if not texts:
            return []
        encodings = self.tokenizer.encode_batch([t or "" for t in texts])
        logits_doc, _ = self._run(encodings)
        probs = self._softmax(logits_doc)
        # head_doc was trained on canonical (negative, neutral, positive)
        # targets, so no reordering is needed — unlike the HF teachers.
        return [Prediction(probabilities=row.tolist()) for row in probs]

    def classify_aspects(self, items) -> list[list[tuple[str, Prediction]]]:
        items = list(items)
        flat: list = []
        spans: list[tuple[int, int]] = []
        for text, aspects in items:
            start = len(flat)
            for aspect in aspects:
                flat.append((text or "", aspect))
            spans.append((start, len(flat)))
        if not flat:
            return [[] for _ in items]

        encodings = [self.tokenizer.encode(text, pair=aspect) for text, aspect in flat]
        _, logits_aspect = self._run(encodings)
        probs = self._softmax(logits_aspect)

        out: list[list[tuple[str, Prediction]]] = []
        for (start, _end), (_text, aspects) in zip(spans, items, strict=True):
            row = []
            for offset, aspect in enumerate(aspects):
                row.append((aspect, Prediction(probabilities=probs[start + offset].tolist())))
            out.append(row)
        return out


class ManagedBackend:
    """Routes between the hot-swappable student and the base backend.

    - classify: the promoted student when ModelManager holds one, else base.
    - classify_aspects: ALWAYS base — the student's aspect head is untrained
      (kd.py trains head_doc only) until aspect labels exist. Flip
      serve_student_aspects once an aspect-trained student is promoted.

    current() is one attribute read on the manager, so a promotion swaps
    between requests, never mid-request.
    """

    def __init__(self, base, manager, *, serve_student_aspects: bool = False):
        self.base = base
        self.manager = manager
        self.serve_student_aspects = serve_student_aspects

    def classify(self, texts):
        student = self.manager.current()
        if student is not None:
            return student.classify(texts)
        return self.base.classify(texts)

    def classify_aspects(self, items):
        if self.serve_student_aspects:
            student = self.manager.current()
            if student is not None:
                return student.classify_aspects(items)
        return self.base.classify_aspects(items)

    def served_run_id(self) -> str | None:
        """The promoted run currently answering doc-level requests, if any."""
        if self.manager.current() is None:
            return None
        return self.manager.run_id
