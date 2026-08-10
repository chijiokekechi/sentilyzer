#!/usr/bin/env python3
"""Backfill the training corpus from the HackerNews archive.

Reads the community mirror ``open-index/hacker-news`` (monthly parquet files,
verified 2026-08-10: ``data/YYYY/YYYY-MM.parquet``, live-updated) and writes
corpus rows in the same layout the Go harvester uses::

    <out>/documents/dt=<utc-date>/platform=hackernews/part-backfill-<month>.jsonl.gz

Eligibility rests on the repo's own HN audit (docs/corpus-policy.md) — the
dataset is treated as HN rows, not as a third-party grant. Rows the archive
marks ``deleted`` or ``dead`` are excluded: those are HN's own revocations.

Dataset quirks handled here (all verified against the live files):
  - ``type`` is a NUMERIC enum, not the API's string: 1=story, 2=comment.
  - comment text is HTML: entities escaped, paragraphs as ``<p>``.
  - ``by`` is a reserved word in SQL and must be quoted.

Usage::

    python backfill_hn.py --out corpus --from-month 2024-01 --to-month 2024-03
    python backfill_hn.py --out corpus --from-month 2006-10   # through last month

Requires: pip install duckdb (~40 MB). Each month is ~45 MB downloaded; the
full 20-year archive is ~5-6 GB and a few hours — run it on the box that
keeps the corpus, month by month; the script is resumable (skips months whose
output file already exists).
"""

from __future__ import annotations

import argparse
import datetime as dt
import gzip
import hashlib
import html
import json
import re
import sys
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


def backfill_month(con, month: str, out_root: Path, min_chars: int, max_chars: int,
                   limit: int | None) -> int:
    year = month.split("-")[0]
    url = HF_URL.format(year=year, month=month)
    # type=2 comments carry text; type=1 stories contribute their titles.
    # deleted/dead rows are HN's own revocations and never enter the corpus.
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


def month_done(out_root: Path, month: str) -> bool:
    """A month is done when any dt= partition holds its backfill part file."""
    return any(out_root.glob(f"documents/dt=*/platform=hackernews/part-backfill-{month}.jsonl.gz"))


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", required=True, help="corpus root directory")
    ap.add_argument("--from-month", default="2006-10", help="first month (YYYY-MM)")
    ap.add_argument("--to-month", default=None, help="last month inclusive; default = last full month")
    ap.add_argument("--min-chars", type=int, default=40)
    ap.add_argument("--max-chars", type=int, default=2000)
    ap.add_argument("--limit", type=int, default=None, help="per-month row cap (smoke tests)")
    args = ap.parse_args()

    import duckdb  # deferred so --help works without the dep

    to_month = args.to_month
    if not to_month:
        first_of_month = dt.date.today().replace(day=1)
        last_full = first_of_month - dt.timedelta(days=1)
        to_month = last_full.strftime("%Y-%m")

    out_root = Path(args.out)
    con = duckdb.connect()
    con.sql("INSTALL httpfs; LOAD httpfs;")

    total = 0
    for month in month_range(args.from_month, to_month):
        if month_done(out_root, month):
            print(f"{month}: already backfilled, skipping", flush=True)
            continue
        try:
            kept = backfill_month(con, month, out_root, args.min_chars, args.max_chars, args.limit)
        except Exception as exc:  # a missing early month must not kill the run
            print(f"{month}: FAILED: {exc}", file=sys.stderr, flush=True)
            continue
        total += kept
        print(f"{month}: {kept} documents", flush=True)
    print(f"done: {total} documents total")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
