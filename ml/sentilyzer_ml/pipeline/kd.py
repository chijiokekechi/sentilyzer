"""Knowledge distillation: build a small student from the frozen teacher and
train it on the teacher's soft labels.

Three load-bearing rules from docs/continuous-training-plan.md live here:

1. **The collapse firewall.** ``build_student_from_teacher`` takes a TEACHER
   model — there is deliberately no signature that accepts a previous
   student. Day-N's student is f(frozen teacher, accumulated human text),
   never f(day-(N-1) student), which is what keeps self-distillation
   degeneration structurally unconstructible.

2. **Pure soft-label KD, alpha = 0.** With no ground truth, the only "hard
   label" available is argmax(teacher) — a strictly lossy function of the
   soft target we already have. The loss is ``T² · KL(teacher_T ‖ student_T)``
   with the T² factor kept so temperature and learning rate stay decoupled
   (Hinton: soft-target gradients scale as 1/T²).

3. **Budgets, not epochs.** Training stops at ``max_seconds`` /
   ``max_sequences``, whichever first, checkpointing so a preempted or
   budget-stopped run RESUMES rather than restarts. The bill stays flat as
   the corpus grows; only epochs-delivered floats.

Everything here is framework-level (no Modal imports) so the math is
unit-testable on CPU with tiny models.
"""

from __future__ import annotations

import json
import math
import time
from dataclasses import dataclass, field
from pathlib import Path

import torch
import torch.nn.functional as F
from torch import nn

EPS = 1e-9


@dataclass(frozen=True)
class DistillConfig:
    student_layers: int = 6
    temperature: float = 3.0
    lr: float = 5e-5
    batch_size: int = 32
    aspect_fraction: float = 0.25  # fraction of steps on aspect batches, when aspect data is present
    max_seconds: float = 2700.0
    max_sequences: int = 1_840_000
    checkpoint_every_steps: int = 500
    seed: int = 13
    device: str = "cpu"


@dataclass
class DistillResult:
    steps: int = 0
    sequences_seen: int = 0
    aspect_sequences_seen: int = 0
    partial: bool = False  # stopped by budget rather than data exhaustion
    resumed: bool = False
    final_loss: float = math.nan
    losses: list[float] = field(default_factory=list)
    device: str = "cpu"


class TwoHeadStudent(nn.Module):
    """One shared encoder with document-level and aspect-level heads.

    Both heads emit 3-class logits in the canonical (negative, neutral,
    positive) order. forward() takes only (input_ids, attention_mask) and
    returns both logit sets, which keeps the ONNX export a single graph with
    two outputs; aspect inputs are encoded as tokenizer text pairs upstream.
    """

    def __init__(self, encoder: nn.Module, hidden_size: int, num_labels: int = 3):
        super().__init__()
        self.encoder = encoder
        self.head_doc = nn.Linear(hidden_size, num_labels)
        self.head_aspect = nn.Linear(hidden_size, num_labels)

    def forward(self, input_ids: torch.Tensor, attention_mask: torch.Tensor):
        out = self.encoder(input_ids=input_ids, attention_mask=attention_mask)
        # CLS pooling: first token of the last hidden state.
        cls = out.last_hidden_state[:, 0]
        return self.head_doc(cls), self.head_aspect(cls)


def student_layer_indices(teacher_layers: int, student_layers: int) -> list[int]:
    """Which teacher layers seed the student: evenly strided from the bottom
    (the DistilBERT initialization strategy — 12→6 takes 0,2,4,6,8,10)."""
    if student_layers <= 0 or student_layers > teacher_layers:
        raise ValueError(
            f"student_layers must be in 1..{teacher_layers}, got {student_layers}"
        )
    stride = teacher_layers // student_layers
    return [i * stride for i in range(student_layers)]


def build_student_from_teacher(teacher, student_layers: int) -> TwoHeadStudent:
    """Create a fresh student seeded from the FROZEN teacher's weights.

    This is the collapse firewall: the input is a
    ``*ForSequenceClassification`` teacher (or its base model), never a prior
    student, and it is re-run from scratch for every training run. The
    student copies the teacher's embeddings and an even stride of its encoder
    layers; both heads start fresh (the soft labels teach them).
    """
    import copy

    base = getattr(teacher, "base_model", teacher)
    t_config = base.config
    s_config = copy.deepcopy(t_config)
    s_config.num_hidden_layers = student_layers

    encoder = type(base)(s_config)
    keep = student_layer_indices(t_config.num_hidden_layers, student_layers)

    encoder.embeddings.load_state_dict(base.embeddings.state_dict())
    for s_idx, t_idx in enumerate(keep):
        encoder.encoder.layer[s_idx].load_state_dict(
            base.encoder.layer[t_idx].state_dict()
        )
    return TwoHeadStudent(encoder, hidden_size=t_config.hidden_size)


def kd_loss(
    student_logits: torch.Tensor,
    teacher_probs: torch.Tensor,
    temperature: float,
) -> torch.Tensor:
    """T² · KL(teacher_T ‖ student_T), from STORED teacher probabilities.

    The corpus stores probabilities, not logits; softmax shift-invariance
    makes log(p) an exact stand-in for the teacher's logits:
    softmax(log(p)/T) ≡ softmax(z/T) for every T. The T² factor keeps
    gradient scale independent of T so LR tuned at one temperature stays
    valid at another.
    """
    t = float(temperature)
    teacher_t = F.softmax(torch.log(teacher_probs.clamp_min(EPS)) / t, dim=-1)
    student_log_t = F.log_softmax(student_logits / t, dim=-1)
    return t * t * F.kl_div(student_log_t, teacher_t, reduction="batchmean")


def _batches(n: int, batch_size: int, seed: int):
    """Deterministic shuffled batch order — the same seed yields the same
    trajectory, which is what makes checkpoint-resume reproduce an
    uninterrupted run instead of resampling."""
    g = torch.Generator().manual_seed(seed)
    order = torch.randperm(n, generator=g).tolist()
    for start in range(0, n, batch_size):
        yield order[start : start + batch_size]


def distill(
    student: TwoHeadStudent,
    texts: list[str],
    teacher_probs: torch.Tensor,  # (N, 3) float32, canonical label order
    tokenize,  # (list[str]) -> dict of tensors: input_ids, attention_mask
    cfg: DistillConfig,
    checkpoint_dir: str | Path,
    clock=time.monotonic,
    *,
    aspect_pairs: list[tuple[str, str]] | None = None,  # (text, aspect) pairs
    aspect_probs: torch.Tensor | None = None,  # (N, 3) float32, aligned with aspect_pairs
    tokenize_pair=None,  # (list[tuple[str, str]]) -> dict of tensors, text-pair encoding
) -> DistillResult:
    """Train the doc head, and optionally the aspect head, on soft labels
    under wall-clock/sequence budgets.

    The checkpoint is keyed to checkpoint_dir — the CALLER names it after the
    candidate run, so a preempted attempt resumes instead of restarting, and
    a different run never resumes a stranger's checkpoint.

    With aspect data, every round(1/aspect_fraction)-th executed step trains
    the aspect head on a (text, aspect) batch instead of a doc batch; once
    the aspect stream exhausts, the remaining steps are doc steps. Without
    aspect data the behavior is exactly the doc-only path.
    """
    if len(texts) != teacher_probs.shape[0]:
        raise ValueError("texts and teacher_probs must align")
    if aspect_pairs is not None:
        if aspect_probs is None or tokenize_pair is None:
            raise ValueError("aspect_pairs requires aspect_probs and tokenize_pair")
        if len(aspect_pairs) != aspect_probs.shape[0]:
            raise ValueError("aspect_pairs and aspect_probs must align")
        if not 0 < cfg.aspect_fraction <= 0.5:
            # Above 0.5, round(1/f) collapses to 1 (every step) or 0 (division
            # by zero at the modulo), neither of which means "fraction".
            raise ValueError(
                f"aspect_fraction must be in (0, 0.5] when aspect data is "
                f"present, got {cfg.aspect_fraction}"
            )
    ckpt_dir = Path(checkpoint_dir)
    ckpt_dir.mkdir(parents=True, exist_ok=True)
    ckpt_path = ckpt_dir / "checkpoint.pt"

    device = torch.device(cfg.device)
    if device.type == "cuda":
        # TF32 keeps fp32 KD semantics while actually using the tensor cores.
        torch.backends.cuda.matmul.allow_tf32 = True
        torch.backends.cudnn.allow_tf32 = True
    student.to(device)  # before the optimizer, so its state lives with the params

    optimizer = torch.optim.AdamW(student.parameters(), lr=cfg.lr)
    result = DistillResult(device=cfg.device)
    start_batch = 0
    executed = 0  # executed-step counter: picks the interleave slot for step k
    aspect_cursor = 0  # aspect batches consumed from the seed+1 stream

    if ckpt_path.exists():
        # Always load to CPU: load_state_dict copies into the (possibly CUDA)
        # params, and the RNG blobs must stay CPU byte tensors.
        state = torch.load(ckpt_path, weights_only=True, map_location="cpu")
        student.load_state_dict(state["model"])
        optimizer.load_state_dict(state["optimizer"])
        # Restore the RNG so dropout masks continue exactly where they left
        # off — without this, a resumed run silently diverges from an
        # uninterrupted one (the masks differ from the resume point on).
        torch.set_rng_state(state["rng"])
        if device.type == "cuda" and "rng_cuda" in state:
            torch.cuda.set_rng_state(state["rng_cuda"], device)
        start_batch = state["next_batch"]
        result.steps = state["steps"]
        result.sequences_seen = state["sequences_seen"]
        # Pre-aspect checkpoints lack these keys; missing means zero.
        executed = state.get("executed_steps", 0)
        aspect_cursor = state.get("next_aspect_batch", 0)
        result.aspect_sequences_seen = state.get("aspect_sequences_seen", 0)
        result.resumed = True
    else:
        torch.manual_seed(cfg.seed)

    started = clock()
    student.train()

    def save(next_batch: int) -> None:
        tmp = ckpt_path.with_suffix(".tmp")
        payload = {
            "model": student.state_dict(),
            "optimizer": optimizer.state_dict(),
            "rng": torch.get_rng_state(),
            "next_batch": next_batch,
            "steps": result.steps,
            "sequences_seen": result.sequences_seen,
        }
        if aspect_pairs is not None:
            # Resume must land on the same interleave slot and aspect batch;
            # a no-aspect run keeps the exact pre-aspect payload.
            payload["executed_steps"] = executed
            payload["next_aspect_batch"] = aspect_cursor
            payload["aspect_sequences_seen"] = result.aspect_sequences_seen
        if device.type == "cuda":
            # CUDA dropout draws from the device RNG — resume needs it too.
            payload["rng_cuda"] = torch.cuda.get_rng_state(device)
        torch.save(payload, tmp)
        tmp.replace(ckpt_path)  # atomic: a crash never truncates a good checkpoint

    doc_iter = _batches(len(texts), cfg.batch_size, cfg.seed)
    for _ in range(start_batch):
        next(doc_iter, None)  # replay the deterministic order up to the resume point
    doc_cursor = start_batch

    aspect_iter = None
    period = 1
    if aspect_pairs is not None and cfg.aspect_fraction > 0:
        period = round(1 / cfg.aspect_fraction)
        aspect_iter = _batches(len(aspect_pairs), cfg.batch_size, cfg.seed + 1)
        for _ in range(aspect_cursor):
            next(aspect_iter, None)

    while True:
        # Executed step k is an aspect step iff k % round(1/aspect_fraction)
        # == 0; an exhausted aspect stream falls back to doc steps.
        aspect_step = aspect_iter is not None and executed % period == 0
        idx = next(aspect_iter, None) if aspect_step else None
        if idx is None:
            aspect_step = False
            idx = next(doc_iter, None)
            if idx is None:
                break  # doc data exhausted: the run is complete
        if clock() - started >= cfg.max_seconds or result.sequences_seen >= cfg.max_sequences:
            result.partial = True
            save(doc_cursor)
            break

        if aspect_step:
            enc = tokenize_pair([aspect_pairs[i] for i in idx])
            probs = aspect_probs[idx].to(device)
            _, logits_aspect = student(enc["input_ids"].to(device), enc["attention_mask"].to(device))
            loss = kd_loss(logits_aspect, probs, cfg.temperature)
        else:
            enc = tokenize([texts[i] for i in idx])
            probs = teacher_probs[idx].to(device)
            logits_doc, _ = student(enc["input_ids"].to(device), enc["attention_mask"].to(device))
            loss = kd_loss(logits_doc, probs, cfg.temperature)

        optimizer.zero_grad()
        loss.backward()
        optimizer.step()

        executed += 1
        result.steps += 1
        result.sequences_seen += len(idx)
        if aspect_step:
            aspect_cursor += 1
            result.aspect_sequences_seen += len(idx)
        else:
            doc_cursor += 1
        result.losses.append(loss.detach().item())
        if result.steps % cfg.checkpoint_every_steps == 0:
            save(doc_cursor)

    if result.losses:
        result.final_loss = result.losses[-1]
    if not result.partial:
        save_marker = ckpt_dir / "complete.json"
        save_marker.write_text(json.dumps({"steps": result.steps}))
    return result
