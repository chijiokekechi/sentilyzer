"""Export, eval-gate, and labeling tests.

The export test settles (at small scale) an open question from the design
docs: whether a custom two-head module exports to ONNX as ONE graph with two
outputs, survives dynamic axes, and still runs after INT8 dynamic
quantization.
"""

from __future__ import annotations

import numpy as np
import pytest

torch = pytest.importorskip("torch")
transformers = pytest.importorskip("transformers")
pytest.importorskip("onnxruntime")
duckdb = pytest.importorskip("duckdb")

from transformers import RobertaConfig, RobertaForSequenceClassification  # noqa: E402

from sentilyzer_ml.inference import Prediction  # noqa: E402
from sentilyzer_ml.model_store import Pointer  # noqa: E402
from sentilyzer_ml.pipeline.evalgate import GateConfig, evaluate_gate  # noqa: E402
from sentilyzer_ml.pipeline.export import export_student, sha256_file  # noqa: E402
from sentilyzer_ml.pipeline.kd import build_student_from_teacher  # noqa: E402
from sentilyzer_ml.pipeline.label import label_training_set  # noqa: E402

TINY = RobertaConfig(
    vocab_size=100,
    hidden_size=32,
    num_hidden_layers=4,
    num_attention_heads=2,
    intermediate_size=64,
    max_position_embeddings=64,
    num_labels=3,
    pad_token_id=1,
)


def sample_batch(batch=4, seq=12):
    torch.manual_seed(3)
    return {
        "input_ids": torch.randint(3, 99, (batch, seq)),
        "attention_mask": torch.ones(batch, seq, dtype=torch.long),
    }


# --- export ------------------------------------------------------------------


def test_two_head_export_and_int8(tmp_path):
    import onnxruntime as ort

    teacher = RobertaForSequenceClassification(TINY)
    student = build_student_from_teacher(teacher, student_layers=2)
    result = export_student(student, tmp_path, sample_batch=sample_batch())

    # Parity was asserted inside export; check the reported number is sane.
    assert result.max_parity_diff < 1e-3
    assert result.int8_bytes < result.fp32_bytes  # quantization shrank it
    assert len(result.int8_sha256) == 64
    assert result.int8_sha256 == sha256_file(tmp_path / "model.int8.onnx")

    # Dynamic axes: a DIFFERENT batch and sequence length must run, and the
    # graph must expose exactly the two named outputs.
    sess = ort.InferenceSession(result.int8_path, providers=["CPUExecutionProvider"])
    assert [o.name for o in sess.get_outputs()] == ["logits_doc", "logits_aspect"]
    other = sample_batch(batch=2, seq=31)
    doc, aspect = sess.run(
        None,
        {"input_ids": other["input_ids"].numpy(), "attention_mask": other["attention_mask"].numpy()},
    )
    assert doc.shape == (2, 3) and aspect.shape == (2, 3)


def test_export_refuses_parity_failure(tmp_path):
    class Liar(torch.nn.Module):
        """Model whose torch output disagrees with any exported graph:
        nondeterministic forward."""

        def forward(self, input_ids, attention_mask):
            noise = torch.randn(input_ids.shape[0], 3) * 10
            return noise, noise

    with pytest.raises((RuntimeError, Exception)):
        export_student(Liar(), tmp_path, sample_batch=sample_batch())


# --- eval gate ---------------------------------------------------------------


def probs_for(labels: list[int], confidence=0.9) -> np.ndarray:
    out = np.full((len(labels), 3), (1 - confidence) / 2, dtype=np.float32)
    for i, cls in enumerate(labels):
        out[i, cls] = confidence
    return out


def test_gate_passes_on_agreement():
    teacher = [i % 3 for i in range(600)]
    gate = evaluate_gate(probs_for(teacher), probs_for(teacher), GateConfig())
    assert gate.passed
    assert gate.metrics["agreement"] == 1.0


def test_gate_fails_on_low_agreement():
    teacher = [i % 3 for i in range(600)]
    student = [(i + 1) % 3 for i in range(600)]  # always wrong
    gate = evaluate_gate(probs_for(student), probs_for(teacher))
    assert not gate.passed
    assert any("agreement" in r for r in gate.reasons)


def test_gate_catches_neutral_collapse():
    """The failure mode the per-class floors exist for: high agreement,
    but the student never predicts neutral."""
    teacher = [0] * 270 + [1] * 60 + [2] * 270
    student = [0] * 270 + [0] * 60 + [2] * 270  # neutral -> negative
    gate = evaluate_gate(probs_for(student), probs_for(teacher))
    assert gate.metrics["agreement"] == pytest.approx(0.9)
    assert not gate.passed
    assert any("neutral recall" in r for r in gate.reasons)


def test_gate_requires_minimum_rows():
    teacher = [i % 3 for i in range(100)]
    gate = evaluate_gate(probs_for(teacher), probs_for(teacher))
    assert not gate.passed
    assert any("too small" in r for r in gate.reasons)


# --- labeling ----------------------------------------------------------------


class FakeTeacher:
    """Deterministic backend: probabilities derived from the text length."""

    def __init__(self):
        self.calls = 0

    def classify(self, texts):
        self.calls += 1
        out = []
        for t in texts:
            cls = len(t) % 3
            p = [0.05, 0.05, 0.05]
            p[cls] = 0.9
            out.append(Prediction(probabilities=p))
        return out


def write_train_parquet(path, n):
    duckdb.sql(f"""
        COPY (
            SELECT 'bluesky' AS platform,
                   'doc-' || lpad(i::VARCHAR, 4, '0') AS doc_id,
                   repeat('x', 40 + (i % 7)) AS text
            FROM range({n}) t(i)
        ) TO '{path}' (FORMAT parquet)
    """)


def test_labeling_is_incremental(tmp_path):
    train = tmp_path / "train.parquet"
    labels = tmp_path / "labels.parquet"
    write_train_parquet(train, 10)

    teacher = FakeTeacher()
    s1 = label_training_set(train, labels, teacher, batch_size=4)
    assert (s1.total_rows, s1.already_labeled, s1.newly_labeled) == (10, 0, 10)

    # Re-run with no new rows: nothing is re-labeled, teacher never called.
    teacher2 = FakeTeacher()
    s2 = label_training_set(train, labels, teacher2, batch_size=4)
    assert (s2.already_labeled, s2.newly_labeled) == (10, 0)
    assert teacher2.calls == 0

    # Grow the training set: only the delta is labeled.
    write_train_parquet(train, 15)
    s3 = label_training_set(train, labels, FakeTeacher(), batch_size=4)
    assert (s3.already_labeled, s3.newly_labeled) == (10, 5)

    rows = duckdb.sql(f"""
        SELECT count(*), min(p_negative + p_neutral + p_positive),
               max(p_negative + p_neutral + p_positive)
        FROM read_parquet('{labels}')
    """).fetchone()
    assert rows[0] == 15
    assert rows[1] == pytest.approx(1.0, abs=1e-5)
    assert rows[2] == pytest.approx(1.0, abs=1e-5)


# --- promotion pointer compatibility ----------------------------------------


def test_promote_pointer_matches_model_store():
    """What modal_app.promote writes must parse as model_store.Pointer —
    the contract between the trainer and the serving box."""
    import json

    pointer_json = json.dumps(
        {"run_id": "train:2026-08-10-120000", "sha256": "a" * 64, "metrics": {"agreement": 0.93}}
    )
    p = Pointer.from_json(pointer_json)
    assert p.run_id == "train:2026-08-10-120000"
    assert p.metrics == {"agreement": 0.93}
