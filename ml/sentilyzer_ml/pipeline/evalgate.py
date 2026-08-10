"""The promotion gate: no student serves until it clears every check.

All metrics are computed against the FROZEN teacher's labels on a held-out
slice the trainer never saw. Agreement is the primary fidelity signal;
per-class recall floors guard the failure most likely to slip through a
single aggregate number — neutral-class collapse, where a student quietly
stops predicting one class while overall agreement still looks fine.

Pure numpy: no torch, no onnxruntime, unit-testable anywhere.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

LABELS = ("negative", "neutral", "positive")


@dataclass(frozen=True)
class GateConfig:
    # Minimum argmax agreement with the teacher on the held-out slice.
    min_agreement: float = 0.85
    # Per-class recall floors (teacher argmax as reference). Guards collapse.
    min_class_recall: float = 0.45
    # Minimum held-out size for the numbers to mean anything.
    min_eval_rows: int = 500


@dataclass
class GateResult:
    passed: bool
    reasons: list[str] = field(default_factory=list)
    metrics: dict = field(default_factory=dict)


def evaluate_gate(
    student_probs: np.ndarray,  # (N, 3), canonical order
    teacher_probs: np.ndarray,  # (N, 3)
    cfg: GateConfig | None = None,
) -> GateResult:
    cfg = cfg or GateConfig()
    if student_probs.shape != teacher_probs.shape or student_probs.ndim != 2:
        raise ValueError("student and teacher prob arrays must be (N, 3) and aligned")

    n = student_probs.shape[0]
    reasons: list[str] = []
    if n < cfg.min_eval_rows:
        reasons.append(f"held-out slice too small: {n} < {cfg.min_eval_rows}")

    s = student_probs.argmax(axis=1)
    t = teacher_probs.argmax(axis=1)
    agreement = float((s == t).mean()) if n else 0.0
    if agreement < cfg.min_agreement:
        reasons.append(f"agreement {agreement:.4f} < {cfg.min_agreement}")

    recalls: dict[str, float] = {}
    for cls, name in enumerate(LABELS):
        ref = t == cls
        if not ref.any():
            # The teacher never predicted this class on the slice — recall is
            # undefined, and silently passing would let collapse hide behind a
            # skewed slice.
            reasons.append(f"held-out slice has no teacher-{name} rows")
            recalls[name] = float("nan")
            continue
        recalls[name] = float((s[ref] == cls).mean())
        if recalls[name] < cfg.min_class_recall:
            reasons.append(
                f"{name} recall {recalls[name]:.4f} < {cfg.min_class_recall}"
            )

    return GateResult(
        passed=not reasons,
        reasons=reasons,
        metrics={
            "eval_rows": n,
            "agreement": agreement,
            "class_recall": recalls,
        },
    )
