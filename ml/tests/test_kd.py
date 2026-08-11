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


def fake_tokenize_pair(batch: list[tuple[str, str]]):
    """Deterministic pair encoding: text and aspect joined by a separator
    char, through the same char-level scheme as fake_tokenize."""
    return fake_tokenize([f"{text} | {aspect}" for text, aspect in batch])


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


def _toy_aspect_task(n=48):
    """(text, aspect) pairs whose teacher label is recoverable from the aspect."""
    words = ["awful", "chair", "amazing"]
    pairs, probs = [], []
    for i in range(n):
        cls = i % 3
        pairs.append((f"the {words[cls]} thing #{i}", words[cls]))
        p = [0.05, 0.05, 0.05]
        p[cls] = 0.9
        probs.append(p)
    return pairs, torch.tensor(probs, dtype=torch.float32)


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


def test_distill_rejects_out_of_range_aspect_fraction(teacher, tmp_path):
    """Above 0.5 the interleave period rounds to 1 or 0 (crash), and 0 with
    aspect data present means the data silently never trains: both are
    caller errors and must fail loudly."""
    texts, probs = _toy_task()
    pairs, a_probs = _toy_aspect_task()
    student = build_student_from_teacher(teacher, student_layers=2)
    for bad in (0.0, 0.6, 1.0, 2.0):
        cfg = DistillConfig(
            batch_size=16, max_seconds=60, max_sequences=64, seed=7,
            aspect_fraction=bad,
        )
        with pytest.raises(ValueError, match="aspect_fraction"):
            distill(
                student, texts, probs, fake_tokenize, cfg, tmp_path / "ckpt",
                aspect_pairs=pairs, aspect_probs=a_probs,
                tokenize_pair=fake_tokenize_pair,
            )


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


def test_distill_without_aspect_data_is_unchanged(teacher, tmp_path):
    """The aspect extension must be invisible when no aspect data is passed:
    from the same seed, an old-style call and an explicit aspect_pairs=None
    call produce the exact same losses and weights, and the checkpoint keeps
    the exact pre-aspect key set."""
    texts, probs = _toy_task()
    cfg = DistillConfig(temperature=3.0, lr=5e-3, batch_size=16, max_seconds=60, max_sequences=10_000, seed=7)

    torch.manual_seed(0)
    old_style = build_student_from_teacher(teacher, student_layers=2)
    r_old = distill(old_style, texts, probs, fake_tokenize, cfg, tmp_path / "old")

    torch.manual_seed(0)
    new_style = build_student_from_teacher(teacher, student_layers=2)
    r_new = distill(
        new_style, texts, probs, fake_tokenize, cfg, tmp_path / "new",
        aspect_pairs=None, aspect_probs=None, tokenize_pair=None,
    )
    assert r_new.losses == r_old.losses  # exact float equality, not approx
    assert r_new.steps == r_old.steps
    assert r_new.sequences_seen == r_old.sequences_seen
    assert r_new.aspect_sequences_seen == 0
    for p_old, p_new in zip(old_style.parameters(), new_style.parameters(), strict=True):
        assert torch.equal(p_old, p_new)

    # A budget-stopped no-aspect run must write a checkpoint with only the
    # pre-aspect keys, so old and new payloads stay interchangeable.
    torch.manual_seed(0)
    stopped = build_student_from_teacher(teacher, student_layers=2)
    cfg_stop = DistillConfig(batch_size=16, max_seconds=60, max_sequences=32, seed=7)
    distill(stopped, texts, probs, fake_tokenize, cfg_stop, tmp_path / "stop")
    state = torch.load(tmp_path / "stop" / "checkpoint.pt", weights_only=True, map_location="cpu")
    assert set(state) == {"model", "optimizer", "rng", "next_batch", "steps", "sequences_seen"}


def test_distill_interleaves_aspect_and_doc_steps(teacher, tmp_path):
    """aspect_fraction=0.5 with 4 doc and 2 aspect batches: aspect slots are
    k=0 and k=2, and the exhausted aspect stream falls back to doc steps."""
    texts, probs = _toy_task(n=64)
    pairs, a_probs = _toy_aspect_task(n=32)
    student = build_student_from_teacher(teacher, student_layers=2)
    kinds: list[str] = []

    def counting_tokenize(batch):
        kinds.append("doc")
        return fake_tokenize(batch)

    def counting_tokenize_pair(batch):
        kinds.append("aspect")
        return fake_tokenize_pair(batch)

    cfg = DistillConfig(lr=5e-3, batch_size=16, max_seconds=60, max_sequences=10_000, seed=7, aspect_fraction=0.5)
    result = distill(
        student, texts, probs, counting_tokenize, cfg, tmp_path / "ckpt",
        aspect_pairs=pairs, aspect_probs=a_probs, tokenize_pair=counting_tokenize_pair,
    )
    assert kinds == ["aspect", "doc", "aspect", "doc", "doc", "doc"]
    assert result.steps == 6
    assert not result.partial


def test_distill_aspect_resume_reproduces_uninterrupted_run(teacher, tmp_path):
    """Budget-stop on the interleave boundary, then resume: the combined
    trajectory must land where a single uninterrupted run lands: same slot
    pattern, same aspect batches, same weights."""
    texts, probs = _toy_task()
    pairs, a_probs = _toy_aspect_task()
    aspect_kw = {"aspect_pairs": pairs, "aspect_probs": a_probs, "tokenize_pair": fake_tokenize_pair}
    cfg_full = DistillConfig(temperature=3.0, lr=5e-3, batch_size=16, max_seconds=1e9, max_sequences=10_000, seed=7, aspect_fraction=0.5)

    torch.manual_seed(0)
    uninterrupted = build_student_from_teacher(teacher, student_layers=2)
    r_full = distill(uninterrupted, texts, probs, fake_tokenize, cfg_full, tmp_path / "full", **aspect_kw)

    torch.manual_seed(0)
    interrupted = build_student_from_teacher(teacher, student_layers=2)
    cfg_half = DistillConfig(temperature=3.0, lr=5e-3, batch_size=16, max_seconds=1e9, max_sequences=48, seed=7, aspect_fraction=0.5)
    r1 = distill(interrupted, texts, probs, fake_tokenize, cfg_half, tmp_path / "resume", **aspect_kw)
    assert r1.partial and r1.sequences_seen == 48
    # Stopped after steps A,D,A: the checkpoint must carry the slot counter
    # and the aspect cursor so the resume lands on the k=3 doc slot.
    state = torch.load(tmp_path / "resume" / "checkpoint.pt", weights_only=True, map_location="cpu")
    assert state["executed_steps"] == 3
    assert state["next_aspect_batch"] == 2
    assert state["aspect_sequences_seen"] == 32

    r2 = distill(interrupted, texts, probs, fake_tokenize, cfg_full, tmp_path / "resume", **aspect_kw)
    assert r2.resumed
    assert r2.sequences_seen == r_full.sequences_seen
    assert r2.aspect_sequences_seen == r_full.aspect_sequences_seen

    for p_full, p_res in zip(uninterrupted.parameters(), interrupted.parameters(), strict=True):
        torch.testing.assert_close(p_full, p_res, rtol=1e-4, atol=1e-5)


def test_distill_aspect_sequences_seen_accounting(teacher, tmp_path):
    """Both step kinds count toward sequences_seen and the budgets; aspect
    steps also increment aspect_sequences_seen."""
    texts, probs = _toy_task(n=96)
    pairs, a_probs = _toy_aspect_task(n=32)
    student = build_student_from_teacher(teacher, student_layers=2)
    cfg = DistillConfig(lr=5e-3, batch_size=16, max_seconds=60, max_sequences=10_000, seed=7, aspect_fraction=0.25)
    result = distill(
        student, texts, probs, fake_tokenize, cfg, tmp_path / "ckpt",
        aspect_pairs=pairs, aspect_probs=a_probs, tokenize_pair=fake_tokenize_pair,
    )
    # Slots k=0 and k=4 are aspect (period 4); all 6 doc batches still run.
    assert result.aspect_sequences_seen == 32
    assert result.sequences_seen == 96 + 32
    assert result.steps == 8
    assert not result.partial

    # The sequence budget counts aspect sequences too: A(16) + D(16) hits 32.
    student2 = build_student_from_teacher(teacher, student_layers=2)
    cfg_stop = DistillConfig(lr=5e-3, batch_size=16, max_seconds=60, max_sequences=32, seed=7, aspect_fraction=0.5)
    result2 = distill(
        student2, texts, probs, fake_tokenize, cfg_stop, tmp_path / "stop",
        aspect_pairs=pairs, aspect_probs=a_probs, tokenize_pair=fake_tokenize_pair,
    )
    assert result2.partial
    assert result2.sequences_seen == 32
    assert result2.aspect_sequences_seen == 16
