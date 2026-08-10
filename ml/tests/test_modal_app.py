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
