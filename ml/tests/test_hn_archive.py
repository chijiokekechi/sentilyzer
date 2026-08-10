"""Unit tests for the HN-archive ingestion core (offline parts)."""

from __future__ import annotations

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
