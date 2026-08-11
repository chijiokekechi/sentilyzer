"""Modal-app tests: what's checkable without a Modal account."""

from __future__ import annotations

import inspect

import pytest

pytest.importorskip("modal")

from sentilyzer_ml.pipeline import modal_app as m  # noqa: E402


def test_holdout_scales_with_run_size():
    assert m.holdout_rows(2_500) == 500  # exactly the gate's minimum
    assert m.holdout_rows(10_000) == 2_000
    assert m.holdout_rows(1_000_000) == m.HOLDOUT_ROWS  # capped
    with pytest.raises(RuntimeError, match="raise --limit or ingest more months"):
        m.holdout_rows(2_499)


def test_app_is_self_contained():
    """The default app must reference nothing outside Modal: no secrets, no
    R2, no operator mode."""
    src = inspect.getsource(m)
    assert "Secret" not in src
    assert "R2" not in src
    assert not hasattr(m, "promote")


def test_cli_surface():
    fn = m.main.info.raw_f
    assert list(inspect.signature(fn).parameters) == [
        "from_month", "to_month", "limit", "corpus", "output", "skip_ingest",
    ]


def test_lock_decision():
    """Liveness arbitrates the lock: a beating holder is respected, a silent
    one is taken over (field lesson: Modal's preemption retry carries a NEW
    input id, so identity alone stranded a healthy run)."""
    beating = {
        "run_id": "train:x", "started_at": 1000.0,
        "beat_at": 1990.0, "input_id": "in-A",
    }

    assert m.lock_decision(None, 2000.0, "in-A") == ("fresh", None)
    stale = 1000.0 + m.LOCK_STALE_SECONDS
    assert m.lock_decision(beating, stale, "in-B") == ("fresh", None)

    # Same input id: our own retry, immediate resume regardless of beat.
    assert m.lock_decision(beating, 2000.0, "in-A") == ("resume", "train:x")

    # Different input, holder still beating: genuinely concurrent, refuse.
    kind, reason = m.lock_decision(beating, 2000.0, "in-B")
    assert kind == "skip" and "train:x" in reason and "1000s" in reason

    # Holder went silent past BEAT_STALE_SECONDS: anyone may take over.
    dead = dict(beating, beat_at=2000.0 - m.BEAT_STALE_SECONDS)
    assert m.lock_decision(dead, 2000.0, "in-B") == ("resume", "train:x")
    assert m.lock_decision(dead, 2000.0, None) == ("resume", "train:x")

    # Pre-heartbeat legacy lock: beat falls back to started_at, so an old
    # one is taken over and a fresh one is still respected.
    legacy = {"run_id": "train:y", "started_at": 1000.0}
    assert m.lock_decision(legacy, 2000.0, "in-B") == ("resume", "train:y")
    assert m.lock_decision(legacy, 1100.0, "in-B")[0] == "skip"


def test_recovery_entrypoints_exist():
    """The detached-run workflow: a dropped client must leave the user a way
    to list runs, fetch artifacts, and clear a stranded lock."""
    for name in ("unlock", "runs", "fetch"):
        assert hasattr(m, name), name
    fetch_fn = m.fetch.info.raw_f
    assert list(inspect.signature(fetch_fn).parameters) == ["run_id", "output"]


def test_aspect_constants():
    """The aspect stage is pinned to the frozen ABSA teacher and a bounded
    pair budget; drift here changes the bill or the label distribution."""
    assert m.ASPECT_TEACHER_MODEL == "yangheng/deberta-v3-base-absa-v1.1"
    assert m.ASPECT_PAIR_CAP == 400_000
    assert m.HOLDOUT_PAIRS == 2_000


def test_holdout_pairs_floor():
    """Same 20%-up-to-cap rule as holdout_rows, but it never raises: aspects
    are optional, so a tiny pair count fails the aspect gate on its row
    minimum instead of killing the run."""
    assert m.holdout_pairs(10_000) == 2_000
    assert m.holdout_pairs(1_000_000) == m.HOLDOUT_PAIRS  # capped
    assert m.holdout_pairs(1_000) == 200
    assert m.holdout_pairs(4) == 0
    assert m.holdout_pairs(0) == 0


def test_label_aspects_stage_surface():
    """The stage exists, takes only run_id, and the orchestrator runs it
    after doc labeling with the same single timeout retry."""
    fn = m.label_aspects_stage.info.raw_f
    assert list(inspect.signature(fn).parameters) == ["run_id"]
    src = inspect.getsource(m.run_pipeline.info.raw_f)
    assert 'summary["label_aspects"]' in src
    assert src.count("label_aspects_stage.remote(run_id)") == 2  # call + one retry
    assert src.index("label.remote") < src.index("label_aspects_stage.remote")
    assert src.index("label_aspects_stage.remote") < src.index("train.remote")


def test_train_and_eval_share_the_aspect_join():
    """The gate's held-out tail only means something if train and evaluate
    read pairs identically: both must go through aspect_join_sql, and the
    join's order must be deterministic."""
    assert "aspect_join_sql" in inspect.getsource(m.train.info.raw_f)
    assert "aspect_join_sql" in inspect.getsource(m.evaluate.info.raw_f)
    sql = m.aspect_join_sql("t.parquet", "a.parquet")
    assert "ORDER BY md5" in sql
    assert "USING (platform, doc_id)" in sql
