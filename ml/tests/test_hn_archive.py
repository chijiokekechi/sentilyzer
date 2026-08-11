"""Unit tests for the HN-archive ingestion core (offline parts)."""

from __future__ import annotations

import pytest

from sentilyzer_ml.pipeline.hn_archive import (
    clean_html,
    content_sha256,
    last_full_month,
    month_range,
    normalize,
)


def test_clean_html():
    raw = "Interesting (mis)use<p>Not how we normally understand it &#x27;catfish&#x27; <i>at all</i>"
    out = clean_html(raw)
    assert "<p>" not in out and "<i>" not in out
    assert "&#x27;" not in out
    assert "'catfish'" in out
    assert "\n" in out  # <p> became a paragraph break


def test_normalize_matches_go_harvester_semantics():
    # Same rule as harvest.Normalize: collapse whitespace + lowercase, so
    # backfilled HN rows dedupe against live-harvested ones.
    assert normalize("  Hello\n\tWorld  ") == "hello world"
    assert content_sha256("Hello World") == content_sha256("hello   world")
    assert content_sha256("hello world") != content_sha256("hello worlds")


def test_month_range():
    assert list(month_range("2024-11", "2025-02")) == [
        "2024-11", "2024-12", "2025-01", "2025-02",
    ]
    assert list(month_range("2025-03", "2025-03")) == ["2025-03"]
    assert list(month_range("2025-04", "2025-03")) == []


def test_last_full_month_shape():
    m = last_full_month()
    assert len(m) == 7 and m[4] == "-"
    year, month = m.split("-")
    assert 2006 <= int(year) <= 2100 and 1 <= int(month) <= 12


def test_limited_ingest_is_partial_and_redone(tmp_path, monkeypatch):
    """A --limit smoke ingest must not poison the month: month_done() stays
    False, and the next unlimited ingest replaces the truncated part files."""
    duckdb = pytest.importorskip("duckdb")  # pipeline dep, absent in gateway CI

    from sentilyzer_ml.pipeline import hn_archive as hn

    parquet = tmp_path / "2025-07.parquet"
    duckdb.sql(f"""
        COPY (
            SELECT i AS id, 2 AS type, 'user' AS "by",
                   TIMESTAMP '2025-07-01 12:00:00' + i * INTERVAL 1 DAY AS time,
                   repeat('hello world ', 8) || i::VARCHAR AS text,
                   NULL::VARCHAR AS title, 0 AS deleted, 0 AS dead
            FROM range(10) t(i)
        ) TO '{parquet}' (FORMAT parquet)
    """)
    monkeypatch.setattr(hn, "HF_URL", str(tmp_path) + "/{month}.parquet")

    con = duckdb.connect()
    root = tmp_path / "corpus"
    kept = hn.backfill_month(con, "2025-07", root, 10, 2000, limit=5)
    assert kept == 5
    assert list(root.glob("documents/dt=*/platform=hackernews/*.partial.jsonl.gz"))
    assert not hn.month_done(root, "2025-07")

    kept = hn.backfill_month(con, "2025-07", root, 10, 2000, limit=None)
    assert kept == 10
    assert hn.month_done(root, "2025-07")
    # the truncated files are gone — no truncated/full mix for one month
    assert not list(root.glob("documents/dt=*/platform=hackernews/*.partial.jsonl.gz"))


def test_ingest_months_checkpoints_each_month(tmp_path, monkeypatch):
    """The Volume commit fires per finished month, so an ingest timeout keeps
    completed months instead of losing the whole range."""
    duckdb = pytest.importorskip("duckdb")  # pipeline dep, absent in gateway CI

    from sentilyzer_ml.pipeline import hn_archive as hn

    class StubCon:
        def sql(self, *_):
            return None

    monkeypatch.setattr(duckdb, "connect", lambda: StubCon())
    monkeypatch.setattr(hn, "backfill_month", lambda con, month, *a: 3)

    commits: list[int] = []
    total = hn.ingest_months(
        tmp_path, "2025-01", "2025-03",
        checkpoint=lambda: commits.append(1), log=lambda *_: None,
    )
    assert total == 9
    assert len(commits) == 3
