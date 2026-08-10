"""Serving tests for the distilled student: OnnxBackend + ManagedBackend.

The artifact fixture is built with the REAL export pipeline (tiny teacher →
build_student → INT8 ONNX) and a from-scratch word-level tokenizer.json, so
these tests exercise the exact file formats the trainer promotes — fully
offline, no downloads.
"""

from __future__ import annotations

import hashlib
import shutil
from pathlib import Path

import pytest

torch = pytest.importorskip("torch")
pytest.importorskip("onnxruntime")
pytest.importorskip("transformers")

from transformers import RobertaConfig, RobertaForSequenceClassification  # noqa: E402

from sentilyzer_ml import config as cfg  # noqa: E402
from sentilyzer_ml import server as srv  # noqa: E402
from sentilyzer_ml.inference import LABELS, HeuristicBackend  # noqa: E402
from sentilyzer_ml.model_store import MODEL_FILENAME, ModelManager, Pointer  # noqa: E402
from sentilyzer_ml.onnx_backend import ManagedBackend, OnnxBackend  # noqa: E402
from sentilyzer_ml.pipeline.export import export_student  # noqa: E402
from sentilyzer_ml.pipeline.kd import build_student_from_teacher  # noqa: E402

TINY = RobertaConfig(
    vocab_size=100,
    hidden_size=32,
    num_hidden_layers=4,
    num_attention_heads=2,
    intermediate_size=64,
    max_position_embeddings=200,
    num_labels=3,
    pad_token_id=1,
)


def build_word_tokenizer(path: Path) -> None:
    """A minimal real tokenizer.json: word-level, ids within TINY's vocab."""
    from tokenizers import Tokenizer, models, pre_tokenizers

    words = [
        "good", "bad", "great", "terrible", "love", "hate", "chair", "table",
        "the", "is", "a", "this", "battery", "screen", "service", "price",
    ]
    vocab = {"<unk>": 0, "<pad>": 1}
    for i, w in enumerate(words):
        vocab[w] = i + 2
    tok = Tokenizer(models.WordLevel(vocab, unk_token="<unk>"))
    tok.pre_tokenizer = pre_tokenizers.Whitespace()
    tok.save(str(path))


@pytest.fixture(scope="module")
def artifact_dir(tmp_path_factory) -> Path:
    """A promoted-artifact directory exactly as the trainer produces it."""
    out = tmp_path_factory.mktemp("artifact")
    torch.manual_seed(5)
    teacher = RobertaForSequenceClassification(TINY)
    student = build_student_from_teacher(teacher, student_layers=2)
    sample = {
        "input_ids": torch.randint(2, 17, (4, 10)),
        "attention_mask": torch.ones(4, 10, dtype=torch.long),
    }
    export_student(student, out, sample_batch=sample)
    build_word_tokenizer(out / "tokenizer.json")
    return out


def test_onnx_backend_classify(artifact_dir):
    backend = OnnxBackend(artifact_dir, intra_op_threads=1)
    preds = backend.classify(["good chair", "bad table", "the battery is great"])
    assert len(preds) == 3
    for p in preds:
        assert len(p.probabilities) == 3
        assert sum(p.probabilities) == pytest.approx(1.0, abs=1e-4)
        assert p.label in LABELS
        assert 0 < p.confidence <= 1

    assert backend.classify([]) == []
    # None/empty text must not crash (the servicer truncates but can pass "").
    assert len(backend.classify(["", "good"])) == 2


def test_onnx_backend_padding_is_neutral(artifact_dir):
    """A short text's prediction must not change because a longer text shared
    its batch — i.e. the attention mask actually masks the padding."""
    backend = OnnxBackend(artifact_dir, intra_op_threads=1)
    alone = backend.classify(["good chair"])[0]
    padded = backend.classify(["good chair", "the battery is great and the screen is terrible"])[0]
    for a, b in zip(alone.probabilities, padded.probabilities, strict=True):
        # INT8 kernels may batch-vary slightly; direction and scale must hold.
        assert a == pytest.approx(b, abs=0.02)


def test_onnx_backend_aspects(artifact_dir):
    backend = OnnxBackend(artifact_dir, intra_op_threads=1)
    out = backend.classify_aspects(
        [
            ("the battery is great", ["battery", "screen"]),
            ("bad service", []),
            ("good price", ["price"]),
        ]
    )
    assert [len(row) for row in out] == [2, 0, 1]
    assert [a for a, _ in out[0]] == ["battery", "screen"]
    for _, p in out[0]:
        assert sum(p.probabilities) == pytest.approx(1.0, abs=1e-4)
    assert backend.classify_aspects([]) == []


class RecordingBase:
    """Base backend that records what reached it."""

    def __init__(self):
        self.classify_calls = 0
        self.aspect_calls = 0
        self.inner = HeuristicBackend()

    def classify(self, texts):
        self.classify_calls += 1
        return self.inner.classify(texts)

    def classify_aspects(self, items):
        self.aspect_calls += 1
        return self.inner.classify_aspects(items)


class DirStore:
    """ArtifactStore over a local directory of runs + a pointer."""

    def __init__(self, runs_root: Path):
        self.runs_root = runs_root
        self.pointer: Pointer | None = None

    def publish(self, run_id: str):
        model = self.runs_root / run_id / MODEL_FILENAME
        sha = hashlib.sha256(model.read_bytes()).hexdigest()
        self.pointer = Pointer(run_id=run_id, sha256=sha)

    def read_pointer(self):
        return self.pointer

    def download(self, run_id: str, dest: Path):
        shutil.copytree(self.runs_root / run_id, dest, dirs_exist_ok=True)


@pytest.fixture()
def managed(artifact_dir, tmp_path):
    runs = tmp_path / "runs"
    (runs / "run-1").mkdir(parents=True)
    for name in (MODEL_FILENAME, "tokenizer.json"):
        shutil.copy(artifact_dir / name, runs / "run-1" / name)

    store = DirStore(runs)
    manager = ModelManager(
        store,
        lambda d: OnnxBackend(d, intra_op_threads=1),
        cache_dir=tmp_path / "cache",
    )
    base = RecordingBase()
    return ManagedBackend(base, manager), base, store, manager


def test_managed_backend_falls_back_then_swaps(managed):
    backend, base, store, manager = managed

    # No promoted student: the base (teacher/stub) answers, and Ready-style
    # reporting shows no student.
    preds = backend.classify(["good chair"])
    assert base.classify_calls == 1
    assert preds[0].label in LABELS
    assert backend.served_run_id() is None

    # Promote run-1 and poll once: doc-level requests swap to the student.
    store.publish("run-1")
    assert manager.refresh() is True
    backend.classify(["good chair"])
    assert base.classify_calls == 1  # unchanged — the student answered
    assert backend.served_run_id() == "run-1"

    # Aspect requests STILL go to the base: the student's aspect head is
    # untrained, and serving it would be noise with a confident shape.
    backend.classify_aspects([("good chair", ["chair"])])
    assert base.aspect_calls == 1


def test_servicer_ready_reports_served_student(managed):
    backend, _base, store, manager = managed
    store.publish("run-1")
    manager.refresh()

    config = cfg.Config.from_env()
    servicer = srv.InferenceServicer(backend, config=config)
    resp = servicer.Ready(None, None)
    assert resp.ready
    assert "student:run-1" in resp.general_model
    assert "fallback" in resp.general_model
    # Aspect model reporting is unchanged — aspects still serve from the base.
    assert "student" not in resp.aspect_model


def test_servicer_ready_without_student(managed):
    backend, _base, _store, _manager = managed
    config = cfg.Config.from_env()
    servicer = srv.InferenceServicer(backend, config=config)
    resp = servicer.Ready(None, None)
    assert resp.ready
    assert "student" not in resp.general_model


def test_build_backend_serves_local_model_dir(artifact_dir):
    """SENTILYZER_ML_MODEL_DIR: the on-demand serving path — worker points at
    a training run's --output directory, no store, no polling."""
    config = cfg.Config(
        listen_addr="[::]:0",
        general_model="stub",
        aspect_model="stub",
        device="cpu",
        max_batch_size=32,
        max_text_chars=2000,
        use_stub=True,
        model_dir=str(artifact_dir),
    )
    backend = srv.build_backend(config)
    assert backend.served_run_id() == artifact_dir.name
    preds = backend.classify(["good chair"])
    assert preds[0].label in LABELS

    # Without model_dir: the plain base backend, no student wrapper.
    plain = srv.build_backend(
        cfg.Config(
            listen_addr="[::]:0", general_model="stub", aspect_model="stub",
            device="cpu", max_batch_size=32, max_text_chars=2000, use_stub=True,
        )
    )
    assert not hasattr(plain, "served_run_id")
