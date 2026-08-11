"""Aspect-pair labeling tests.

Mirrors the label.py tests (deterministic fake teacher, duckdb-built train
parquet fixtures, the flush/checkpoint crash-safety contract) plus the
aspect-specific pieces: the injected miner, the per-doc aspect budget, and
the deterministic md5 cap.
"""

from __future__ import annotations

import pytest

duckdb = pytest.importorskip("duckdb")

from sentilyzer_ml.inference import Prediction  # noqa: E402
from sentilyzer_ml.pipeline.label_aspects import (  # noqa: E402
    ASPECT_TEACHER_VERSION,
    label_aspect_pairs,
)


class FakeAspectTeacher:
    """Deterministic backend: probabilities derived from text+aspect length."""

    def __init__(self):
        self.calls = 0

    def classify_aspects(self, items):
        self.calls += 1
        out = []
        for text, aspects in items:
            row = []
            for aspect in aspects:
                cls = (len(text) + len(aspect)) % 3
                p = [0.05, 0.05, 0.05]
                p[cls] = 0.9
                row.append((aspect, Prediction(probabilities=p)))
            out.append(row)
        return out


def mine_two(text):
    """Two fixed aspects per document; pair keys stay unique via doc_id."""
    return ["price", "service"]


def write_train_parquet(path, n):
    duckdb.sql(f"""
        COPY (
            SELECT 'bluesky' AS platform,
                   'doc-' || lpad(i::VARCHAR, 4, '0') AS doc_id,
                   repeat('x', 40 + (i % 7)) AS text
            FROM range({n}) t(i)
        ) TO '{path}' (FORMAT parquet)
    """)


def test_aspect_labeling_is_incremental(tmp_path):
    train = tmp_path / "train.parquet"
    labels = tmp_path / "aspect_labels.parquet"
    write_train_parquet(train, 10)

    teacher = FakeAspectTeacher()
    s1 = label_aspect_pairs(train, labels, teacher, batch_size=4, mine=mine_two)
    assert (s1.total_pairs, s1.already_labeled, s1.newly_labeled) == (20, 0, 20)

    # Re-run with no new docs: nothing is re-labeled, teacher never called.
    teacher2 = FakeAspectTeacher()
    s2 = label_aspect_pairs(train, labels, teacher2, batch_size=4, mine=mine_two)
    assert (s2.already_labeled, s2.newly_labeled) == (20, 0)
    assert teacher2.calls == 0

    # Grow the training set: only the new docs' pairs are labeled.
    write_train_parquet(train, 15)
    s3 = label_aspect_pairs(train, labels, FakeAspectTeacher(), batch_size=4, mine=mine_two)
    assert (s3.already_labeled, s3.newly_labeled) == (20, 10)

    rows = duckdb.sql(f"""
        SELECT count(*), min(p_negative + p_neutral + p_positive),
               max(p_negative + p_neutral + p_positive),
               count(DISTINCT teacher_version), min(teacher_version)
        FROM read_parquet('{labels}')
    """).fetchone()
    assert rows[0] == 30
    assert rows[1] == pytest.approx(1.0, abs=1e-5)
    assert rows[2] == pytest.approx(1.0, abs=1e-5)
    assert (rows[3], rows[4]) == (1, ASPECT_TEACHER_VERSION)


def test_cap_is_deterministic(tmp_path):
    """The md5 cap must pick the SAME pair subset on every run: otherwise a
    capped re-run would drift onto fresh pairs and relabel forever."""
    train = tmp_path / "train.parquet"
    write_train_parquet(train, 10)  # 20 mined pairs, cap at 12

    labels_a = tmp_path / "a.parquet"
    s1 = label_aspect_pairs(train, labels_a, FakeAspectTeacher(), max_pairs=12, mine=mine_two)
    assert (s1.total_pairs, s1.newly_labeled) == (12, 12)

    # Re-run over the same file: the capped subset is fully labeled already.
    teacher = FakeAspectTeacher()
    s2 = label_aspect_pairs(train, labels_a, teacher, max_pairs=12, mine=mine_two)
    assert (s2.total_pairs, s2.already_labeled, s2.newly_labeled) == (12, 12, 0)
    assert teacher.calls == 0

    # A fresh run into a different file selects the identical pair set.
    labels_b = tmp_path / "b.parquet"
    label_aspect_pairs(train, labels_b, FakeAspectTeacher(), max_pairs=12, mine=mine_two)
    set_a = set(duckdb.sql(
        f"SELECT platform, doc_id, aspect FROM read_parquet('{labels_a}')"
    ).fetchall())
    set_b = set(duckdb.sql(
        f"SELECT platform, doc_id, aspect FROM read_parquet('{labels_b}')"
    ).fetchall())
    assert set_a == set_b
    assert len(set_a) == 12


def test_mine_docs_cap_bounds_mining(tmp_path):
    """Mining runs only over the deterministic doc slice: the cap bounds the
    CPU cost (a full-corpus mine is an hour inside a billed GPU container),
    and the same cap always selects the same documents."""
    train = tmp_path / "train.parquet"
    labels = tmp_path / "labels.parquet"
    write_train_parquet(train, 12)

    mined: list[str] = []

    def counting_mine(text):
        mined.append(text)
        return ["price"]

    stats = label_aspect_pairs(
        train, labels, FakeAspectTeacher(),
        mine=counting_mine, mine_docs_cap=5,
    )
    assert len(mined) == 5
    assert (stats.total_pairs, stats.newly_labeled) == (5, 5)

    first_docs = duckdb.sql(
        f"SELECT doc_id FROM read_parquet('{labels}') ORDER BY doc_id"
    ).fetchall()
    # Same cap on a fresh file selects the identical slice.
    labels2 = tmp_path / "labels2.parquet"
    label_aspect_pairs(
        train, labels2, FakeAspectTeacher(),
        mine=lambda t: ["price"], mine_docs_cap=5,
    )
    second_docs = duckdb.sql(
        f"SELECT doc_id FROM read_parquet('{labels2}') ORDER BY doc_id"
    ).fetchall()
    assert first_docs == second_docs


class DyingAspectTeacher(FakeAspectTeacher):
    """Labels normally, then dies: a timeout/preemption stand-in."""

    def __init__(self, batches_before_death: int):
        super().__init__()
        self.batches_before_death = batches_before_death

    def classify_aspects(self, items):
        if self.calls >= self.batches_before_death:
            raise RuntimeError("killed")
        return super().classify_aspects(items)


def test_aspect_flushes_survive_a_kill(tmp_path):
    """A kill mid-labeling loses at most one flush window: whatever flushed
    stays on disk (checkpoint fired), and the retry labels only the rest."""
    train = tmp_path / "train.parquet"
    labels = tmp_path / "aspect_labels.parquet"
    write_train_parquet(train, 10)  # 20 pairs

    commits: list[int] = []
    # batch 4, flush window 8: window 1 = batches 1-2, then die in window 2.
    with pytest.raises(RuntimeError, match="killed"):
        label_aspect_pairs(
            train, labels, DyingAspectTeacher(batches_before_death=3),
            batch_size=4, flush_every=8, mine=mine_two,
            checkpoint=lambda: commits.append(1),
        )
    assert commits == [1]  # exactly the surviving window was committed
    on_disk = duckdb.sql(f"SELECT count(*) FROM read_parquet('{labels}')").fetchone()[0]
    assert on_disk == 8

    stats = label_aspect_pairs(
        train, labels, FakeAspectTeacher(),
        batch_size=4, flush_every=8, mine=mine_two,
        checkpoint=lambda: commits.append(1),
    )
    assert (stats.already_labeled, stats.newly_labeled) == (8, 12)
    final = duckdb.sql(f"""
        SELECT count(*), count(DISTINCT platform || doc_id || aspect)
        FROM read_parquet('{labels}')
    """).fetchone()
    assert final == (20, 20)  # complete, and nothing double-labeled


def test_injected_miner_is_used_and_budgeted(tmp_path):
    """The injected callable supplies the aspects, called once per document,
    and only the first two DISTINCT aspects per doc become pairs."""
    train = tmp_path / "train.parquet"
    labels = tmp_path / "aspect_labels.parquet"
    write_train_parquet(train, 3)

    mined_texts = []

    def mine(text):
        mined_texts.append(text)
        # A duplicate plus a third aspect: only alpha and beta may survive.
        return ["alpha", "alpha", "beta", "gamma"]

    stats = label_aspect_pairs(train, labels, FakeAspectTeacher(), mine=mine)
    assert len(mined_texts) == 3
    assert (stats.total_pairs, stats.newly_labeled) == (6, 6)
    aspects = {
        r[0] for r in duckdb.sql(
            f"SELECT DISTINCT aspect FROM read_parquet('{labels}')"
        ).fetchall()
    }
    assert aspects == {"alpha", "beta"}


class MiscountingTeacher:
    """Drops the last result: must be rejected, never silently zipped."""

    def classify_aspects(self, items):
        items = list(items)
        return [
            [(aspect, Prediction(probabilities=[0.1, 0.8, 0.1])) for aspect in aspects]
            for _text, aspects in items[:-1]
        ]


class EmptyRowTeacher:
    """Right item count, but no prediction inside any item."""

    def classify_aspects(self, items):
        return [[] for _ in list(items)]


def test_mismatched_prediction_count_is_rejected(tmp_path):
    train = tmp_path / "train.parquet"
    write_train_parquet(train, 4)

    with pytest.raises(RuntimeError, match="results for"):
        label_aspect_pairs(train, tmp_path / "a.parquet", MiscountingTeacher(), mine=mine_two)
    with pytest.raises(RuntimeError, match="predictions for"):
        label_aspect_pairs(train, tmp_path / "b.parquet", EmptyRowTeacher(), mine=mine_two)
