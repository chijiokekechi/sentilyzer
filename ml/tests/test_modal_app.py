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
        "fresh_corpus", "train_minutes", "aspect_pairs",
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
    """The stage exists, takes run_id plus the optional pair override, and
    the orchestrator runs it after doc labeling with the same single timeout
    retry."""
    fn = m.label_aspects_stage.info.raw_f
    assert list(inspect.signature(fn).parameters) == ["run_id", "max_pairs"]
    src = inspect.getsource(m.run_pipeline.info.raw_f)
    assert 'summary["label_aspects"]' in src
    # call + one retry, both carrying the same override
    assert src.count("label_aspects_stage.remote(run_id, aspect_pairs)") == 2
    assert src.index("label.remote") < src.index("label_aspects_stage.remote")
    assert src.index("label_aspects_stage.remote") < src.index("train.remote")


def test_budget_ceiling_constants():
    """The informed-consent overrides are bounded by hard ceilings, and the
    defaults sit inside them so 0 (meaning "use the default") is always
    legal."""
    assert m.TRAIN_MINUTES_CEILING == 90
    assert m.ASPECT_PAIRS_CEILING == 800_000
    assert m.MAX_TRAIN_SECONDS <= m.TRAIN_MINUTES_CEILING * 60
    assert m.ASPECT_PAIR_CAP <= m.ASPECT_PAIRS_CEILING


def test_main_refuses_out_of_bounds_overrides():
    """main() is the consent gate: one minute or one pair past the ceiling,
    or any negative value, dies before a single remote call."""
    fn = m.main.info.raw_f
    with pytest.raises(SystemExit, match="--train-minutes 91"):
        fn(train_minutes=m.TRAIN_MINUTES_CEILING + 1)
    with pytest.raises(SystemExit, match="--aspect-pairs 800001"):
        fn(aspect_pairs=m.ASPECT_PAIRS_CEILING + 1)
    with pytest.raises(SystemExit, match="--train-minutes -1"):
        fn(train_minutes=-1)
    with pytest.raises(SystemExit, match="--aspect-pairs -1"):
        fn(aspect_pairs=-1)
    # The trainer reserves 600s of its budget for export, so a cap at or
    # under 10 minutes would train for zero seconds.
    with pytest.raises(SystemExit, match="export headroom"):
        fn(train_minutes=10)


def test_reset_corpus_surface():
    """The fresh-corpus path: reset_corpus is an app function taking no
    args, follows the reload/commit Volume protocol, deletes recursively,
    and main wires it in before ingest (never unconditionally)."""
    fn = m.reset_corpus.info.raw_f
    assert list(inspect.signature(fn).parameters) == []
    src = inspect.getsource(fn)
    assert "volume.reload()" in src
    assert "shutil.rmtree" in src
    assert src.index("shutil.rmtree") < src.index("volume.commit()")
    main_src = inspect.getsource(m.main.info.raw_f)
    assert "if fresh_corpus:" in main_src
    assert main_src.index("reset_corpus.remote") < main_src.index("ingest_hn.remote")


def test_run_pipeline_accepts_opts():
    """run_pipeline takes an optional opts dict (trigger's bare spawn still
    works) and re-asserts the EFFECTIVE budgets against the ceilings on CPU,
    before the lock and thus before any GPU spawn."""
    fn = m.run_pipeline.info.raw_f
    params = inspect.signature(fn).parameters
    assert list(params) == ["opts"]
    assert params["opts"].default is None
    src = inspect.getsource(fn)
    assert "TRAIN_MINUTES_CEILING" in src
    assert "ASPECT_PAIRS_CEILING" in src
    assert src.index("TRAIN_MINUTES_CEILING") < src.index("lock_decision")
    assert src.index("ASPECT_PAIRS_CEILING") < src.index("lock_decision")
    assert "train.remote(run_id, train_seconds)" in src
    # main hands its knobs over as the opts dict
    assert "run_pipeline.remote(opts)" in inspect.getsource(m.main.info.raw_f)


def test_stage_overrides_fall_back_to_constants():
    """Stages take their override with a None default and fall back to the
    module constants, so direct invocations keep today's bounded budgets."""
    train_fn = m.train.info.raw_f
    train_params = inspect.signature(train_fn).parameters
    assert list(train_params) == ["run_id", "max_seconds"]
    assert train_params["max_seconds"].default is None
    assert "max_seconds = MAX_TRAIN_SECONDS" in inspect.getsource(train_fn)

    aspects_fn = m.label_aspects_stage.info.raw_f
    assert inspect.signature(aspects_fn).parameters["max_pairs"].default is None
    assert "max_pairs = ASPECT_PAIR_CAP" in inspect.getsource(aspects_fn)


def test_train_and_eval_share_the_aspect_join():
    """The gate's held-out tail only means something if train and evaluate
    read pairs identically: both must go through aspect_join_sql, and the
    join's order must be deterministic."""
    assert "aspect_join_sql" in inspect.getsource(m.train.info.raw_f)
    assert "aspect_join_sql" in inspect.getsource(m.evaluate.info.raw_f)
    sql = m.aspect_join_sql("t.parquet", "a.parquet")
    assert "ORDER BY md5" in sql
    assert "USING (platform, doc_id)" in sql
