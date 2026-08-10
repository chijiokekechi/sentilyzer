#!/usr/bin/env python3
"""Backfill the training corpus from the HackerNews archive — CLI wrapper.

The importable core lives in ``sentilyzer_ml.pipeline.hn_archive`` (shared
with the Modal app's ingest step). This script runs it against a local
corpus directory:

    python backfill_hn.py --out corpus --from-month 2024-01 --to-month 2024-03
    python backfill_hn.py --out corpus --from-month 2006-10   # through last month

Requires: pip install duckdb. Each month is ~45 MB downloaded; the full
20-year archive is ~5-6 GB and a few hours. Resumable per month.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

# Runnable both as `python scripts/backfill_hn.py` (repo checkout) and with
# the package installed.
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from sentilyzer_ml.pipeline.hn_archive import ingest_months  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", required=True, help="corpus root directory")
    ap.add_argument("--from-month", default="2006-10", help="first month (YYYY-MM)")
    ap.add_argument("--to-month", default=None, help="last month inclusive; default = last full month")
    ap.add_argument("--min-chars", type=int, default=40)
    ap.add_argument("--max-chars", type=int, default=2000)
    ap.add_argument("--limit", type=int, default=None, help="per-month row cap (smoke tests)")
    args = ap.parse_args()

    total = ingest_months(
        Path(args.out),
        args.from_month,
        args.to_month,
        min_chars=args.min_chars,
        max_chars=args.max_chars,
        limit=args.limit,
        log=lambda msg: print(msg, flush=True),
    )
    print(f"done: {total} documents total")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
