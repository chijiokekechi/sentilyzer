"""HackerNews archive ingestion — importable core.

Reads the community mirror ``open-index/hacker-news`` (monthly parquets,
``data/YYYY/YYYY-MM.parquet``) and writes corpus rows in the harvester's
layout. Used two ways: by ``ml/scripts/backfill_hn.py`` on any machine with
a disk, and by the Modal app's ingest step — which is what makes the
clone-and-run training flow possible with no infrastructure at all: the
archive streams straight into a Modal Volume.

Eligibility rests on the repo's own HN audit (docs/corpus-policy.md); rows
the archive marks deleted/dead are excluded (HN's own revocations).
"""

from __future__ import annotations

import datetime as dt
import gzip
import hashlib
import html
import json
import re
from pathlib import Path

HF_URL = "https://huggingface.co/datasets/open-index/hacker-news/resolve/main/data/{year}/{month}.parquet"

# Mirrors harvest.HashVersion / harvest.Normalize in the Go harvester —
# lowercase + collapsed whitespace. (Unicode case-folding edge cases can
# differ between Go and Python; acceptable for dedupe, versioned regardless.)
HASH_VERSION = 1

_TAG_RE = re.compile(r"<[^>]+>")


def clean_html(text: str) -> str:
    """HN comment text -> plain text: <p> to newline, tags stripped, entities unescaped."""
    text = text.replace("<p>", "\n")
    text = _TAG_RE.sub("", text)
    return html.unescape(text).strip()


def normalize(text: str) -> str:
    return " ".join(text.split()).lower()


def content_sha256(text: str) -> str:
    return hashlib.sha256(normalize(text).encode()).hexdigest()


def month_range(start: str, end: str):
    y, m = map(int, start.split("-"))
    ey, em = map(int, end.split("-"))
    while (y, m) <= (ey, em):
        yield f"{y:04d}-{m:02d}"
        y, m = (y + 1, 1) if m == 12 else (y, m + 1)


def last_full_month() -> str:
    first_of_month = dt.date.today().replace(day=1)
    return (first_of_month - dt.timedelta(days=1)).strftime("%Y-%m")


def month_done(out_root: Path, month: str) -> bool:
    """A month is done when any dt= partition holds its backfill part file."""
    return any(out_root.glob(f"documents/dt=*/platform=hackernews/part-backfill-{month}.jsonl.gz"))


def backfill_month(con, month: str, out_root: Path, min_chars: int, max_chars: int,
                   limit: int | None) -> int:
    """Ingest one archive month into the corpus layout. Returns rows kept.

    Dataset quirks handled (verified against the live files 2026-08-10):
    ``type`` is a NUMERIC enum (1=story, 2=comment); comment text is
    HTML-encoded; ``by`` is reserved in SQL; deleted/dead rows are excluded.
    """
    year = month.split("-")[0]
    url = HF_URL.format(year=year, month=month)
    q = f"""
        SELECT id, type, "by" AS author, epoch(time)::BIGINT AS ts,
               CASE WHEN type = 2 THEN text ELSE title END AS raw
        FROM read_parquet('{url}')
        WHERE coalesce(deleted, 0) = 0 AND coalesce(dead, 0) = 0
          AND type IN (1, 2)
          AND CASE WHEN type = 2 THEN text ELSE title END IS NOT NULL
    """
    if limit:
        q += f" LIMIT {limit}"

    # Group output rows by the item's own UTC date to match the dt= layout.
    by_date: dict[str, list[dict]] = {}
    kept = 0
    for item_id, typ, author, ts, raw in con.sql(q).fetchall():
        text = clean_html(raw) if typ == 2 else raw.strip()
        if len(text) < min_chars or (max_chars and len(text) > max_chars):
            continue
        created = dt.datetime.fromtimestamp(ts, tz=dt.timezone.utc)
        row = {
            "platform": "hackernews",
            "doc_id": f"hn-{item_id}",
            "author": author or "",
            "text": text,
            "created_at": created.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "harvested_at": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "content_sha256": content_sha256(text),
            "hash_version": HASH_VERSION,
        }
        by_date.setdefault(created.strftime("%Y-%m-%d"), []).append(row)
        kept += 1

    for date, rows in sorted(by_date.items()):
        part_dir = out_root / "documents" / f"dt={date}" / "platform=hackernews"
        part_dir.mkdir(parents=True, exist_ok=True)
        path = part_dir / f"part-backfill-{month}.jsonl.gz"
        with gzip.open(path, "wt", encoding="utf-8") as f:
            for row in rows:
                f.write(json.dumps(row, ensure_ascii=False) + "\n")
    return kept


def ingest_months(
    out_root: Path,
    from_month: str,
    to_month: str | None = None,
    *,
    min_chars: int = 40,
    max_chars: int = 2000,
    limit: int | None = None,
    log=print,
) -> int:
    """Ingest a month range, resumably (skips months already present)."""
    import duckdb

    to_month = to_month or last_full_month()
    con = duckdb.connect()
    con.sql("INSTALL httpfs; LOAD httpfs;")
    total = 0
    for month in month_range(from_month, to_month):
        if month_done(out_root, month):
            log(f"{month}: already ingested, skipping")
            continue
        try:
            kept = backfill_month(con, month, out_root, min_chars, max_chars, limit)
        except Exception as exc:  # a missing early month must not kill the run
            log(f"{month}: FAILED: {exc}")
            continue
        total += kept
        log(f"{month}: {kept} documents")
    return total
