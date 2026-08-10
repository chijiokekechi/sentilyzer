"""Distillation tests, on tiny models so CPU is enough.

The tiny teacher (4 layers, hidden 32) is constructed from config — no
downloads — which exercises the same layer-mapping code path the real
12-layer RoBERTa teacher takes.
"""

from __future__ import annotations

import inspect

import pytest

torch = pytest.importorskip("torch")
transformers = pytest.importorskip("transformers")

import torch.nn.functional as F  # noqa: E402
from transformers import RobertaConfig, RobertaForSequenceClassification  # noqa: E402

from sentilyzer_ml.pipeline.kd import (  # noqa: E402
    DistillConfig,
    TwoHeadStudent,
    build_student_from_teacher,
    distill,
    kd_loss,
    student_layer_indices,
)

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


@pytest.fixture(scope="module")
def teacher():
    torch.manual_seed(0)
    return RobertaForSequenceClassification(TINY)


def fake_tokenize(batch: list[str]):
    """Deterministic char-level ids — no tokenizer download needed."""
    width = max(8, max(len(t) for t in batch))
    width = min(width, 48)
    ids, mask = [], []
    for t in batch:
        row = [(ord(c) % 90) + 3 for c in t[:width]]
        pad = width - len(row)
        mask.append([1] * len(row) + [0] * pad)
        ids.append(row + [1] * pad)
    return {
        "input_ids": torch.tensor(ids, dtype=torch.long),
        "attention_mask": torch.tensor(mask, dtype=torch.long),
    }


def test_layer_indices_match_distilbert_strategy():
    assert student_layer_indices(12, 6) == [0, 2, 4, 6, 8, 10]
    assert student_layer_indices(4, 2) == [0, 2]
    assert student_layer_indices(4, 4) == [0, 1, 2, 3]
    with pytest.raises(ValueError):
        student_layer_indices(4, 5)


def test_student_seeded_from_teacher_layers(teacher):
    student = build_student_from_teacher(teacher, student_layers=2)
    assert isinstance(student, TwoHeadStudent)

    base = teacher.base_model
    # Embeddings copied exactly.
    torch.testing.assert_close(
        student.encoder.embeddings.word_embeddings.weight,
        base.embeddings.word_embeddings.weight,
    )
    # Student layer 0 <- teacher layer 0, student layer 1 <- teacher layer 2.
    for s_idx, t_idx in [(0, 0), (1, 2)]:
        torch.testing.assert_close(
            student.encoder.encoder.layer[s_idx].attention.self.query.weight,
            base.encoder.layer[t_idx].attention.self.query.weight,
        )
    # And NOT from teacher layer 1 (would mean the stride is wrong).
    assert not torch.equal(
        student.encoder.encoder.layer[1].attention.self.query.weight,
        base.encoder.layer[1].attention.self.query.weight,
    )


def test_collapse_firewall_shape():
    """The API must not offer a student→student path: the only builder takes
    a teacher model, and distill() takes the STUDENT it just built. Guard the
    signature so a refactor can't quietly add an init_from_student."""
    params = set(inspect.signature(build_student_from_teacher).parameters)
    assert params == {"teacher", "student_layers"}
    import sentilyzer_ml.pipeline.kd as kd_module

    assert not any("from_student" in name for name in dir(kd_module))


def test_kd_loss_zero_when_student_matches_teacher():
    probs = torch.tensor([[0.7, 0.2, 0.1], [0.1, 0.1, 0.8]], dtype=torch.float32)
    logits = torch.log(probs)  # softmax(log p) == p
    for t in (1.0, 3.0):
        loss = kd_loss(logits, probs, temperature=t)
        assert float(loss) == pytest.approx(0.0, abs=1e-6)


def test_kd_loss_matches_manual_formula():
    torch.manual_seed(1)
    logits = torch.randn(5, 3)
    probs = F.softmax(torch.randn(5, 3), dim=-1)
    t = 3.0
    teacher_t = F.softmax(torch.log(probs) / t, dim=-1)
    student_t = F.log_softmax(logits / t, dim=-1)
    manual = t * t * (teacher_t * (teacher_t.clamp_min(1e-9).log() - student_t)).sum(-1).mean()
    got = kd_loss(logits, probs, temperature=t)
    assert float(got) == pytest.approx(float(manual), rel=1e-5)
    assert float(got) > 0


def test_kd_loss_temperature_scaling_keeps_gradients_comparable():
    """The T² factor: gradient magnitude must stay the same order across T,
    so LR tuned at one temperature remains valid at another."""
    torch.manual_seed(2)
    probs = F.softmax(torch.randn(16, 3), dim=-1)
    norms = {}
    for t in (1.0, 3.0, 6.0):
        logits = torch.randn(16, 3, requires_grad=True)
        kd_loss(logits, probs, temperature=t).backward()
        norms[t] = float(logits.grad.norm())
    # Without T² these would differ by ~T²x (36x from 1→6); with it, same order.
    assert norms[1.0] / norms[6.0] < 10
    assert norms[6.0] / norms[1.0] < 10


def _toy_task(n=96):
    """Texts whose teacher label is recoverable from the text itself."""
    texts, probs = [], []
    for i in range(n):
        cls = i % 3
        texts.append(["bad awful terrible", "plain neutral chair", "great love amazing"][cls] + f" #{i}")
        p = [0.05, 0.05, 0.05]
        p[cls] = 0.9
        probs.append(p)
    return texts, torch.tensor(probs, dtype=torch.float32)


def test_distill_learns_and_respects_budget(teacher, tmp_path):
    texts, probs = _toy_task()
    student = build_student_from_teacher(teacher, student_layers=2)
    cfg = DistillConfig(temperature=3.0, lr=5e-3, batch_size=16, max_seconds=60, max_sequences=10_000, seed=7)
    result = distill(student, texts, probs, fake_tokenize, cfg, tmp_path / "ckpt")
    assert not result.partial
    assert result.sequences_seen == len(texts)
    # The loss must actually go down — the training loop learns.
    assert result.losses[-1] < result.losses[0] * 0.8


def test_distill_device_contract(teacher, tmp_path):
    """The device rides the config end to end (the first full Modal run
    trained on CPU while the A10 idled — device must be explicit, reported,
    and absent from CPU checkpoints so resume stays portable)."""
    texts, probs = _toy_task()
    student = build_student_from_teacher(teacher, student_layers=2)
    cfg = DistillConfig(batch_size=16, max_seconds=60, max_sequences=32, seed=7, device="cpu")
    result = distill(student, texts, probs, fake_tokenize, cfg, tmp_path / "ckpt")
    assert result.device == "cpu"
    state = torch.load(tmp_path / "ckpt" / "checkpoint.pt", weights_only=True, map_location="cpu")
    assert "rng" in state and "rng_cuda" not in state


def test_distill_sequence_budget_stops_early(teacher, tmp_path):
    texts, probs = _toy_task()
    student = build_student_from_teacher(teacher, student_layers=2)
    cfg = DistillConfig(batch_size=16, max_seconds=60, max_sequences=32, seed=7)
    result = distill(student, texts, probs, fake_tokenize, cfg, tmp_path / "ckpt")
    assert result.partial
    assert result.sequences_seen == 32  # exactly two batches, then stop


def test_distill_wall_clock_budget_stops(teacher, tmp_path):
    texts, probs = _toy_task()
    student = build_student_from_teacher(teacher, student_layers=2)
    ticks = iter(range(1000))

    def clock():  # each call advances 10 "seconds"
        return next(ticks) * 10.0

    cfg = DistillConfig(batch_size=16, max_seconds=25, max_sequences=10_000, seed=7)
    result = distill(student, texts, probs, fake_tokenize, cfg, tmp_path / "ckpt", clock=clock)
    assert result.partial
    assert result.sequences_seen < len(texts)


def test_distill_resume_reproduces_uninterrupted_run(teacher, tmp_path):
    """Budget-stop, then resume: the combined trajectory must land where a
    single uninterrupted run lands — same batches, same order, same weights."""
    texts, probs = _toy_task()
    cfg_full = DistillConfig(temperature=3.0, lr=5e-3, batch_size=16, max_seconds=1e9, max_sequences=10_000, seed=7)

    torch.manual_seed(0)
    uninterrupted = build_student_from_teacher(teacher, student_layers=2)
    r_full = distill(uninterrupted, texts, probs, fake_tokenize, cfg_full, tmp_path / "full")

    torch.manual_seed(0)
    interrupted = build_student_from_teacher(teacher, student_layers=2)
    cfg_half = DistillConfig(temperature=3.0, lr=5e-3, batch_size=16, max_seconds=1e9, max_sequences=48, seed=7)
    r1 = distill(interrupted, texts, probs, fake_tokenize, cfg_half, tmp_path / "resume")
    assert r1.partial and r1.sequences_seen == 48
    r2 = distill(interrupted, texts, probs, fake_tokenize, cfg_full, tmp_path / "resume")
    assert r2.resumed
    assert r2.sequences_seen == r_full.sequences_seen

    for p_full, p_res in zip(uninterrupted.parameters(), interrupted.parameters(), strict=True):
        torch.testing.assert_close(p_full, p_res, rtol=1e-4, atol=1e-5)
