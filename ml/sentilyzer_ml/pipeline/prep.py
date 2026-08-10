"""Corpus prep: turn raw harvest partitions into one training set.

This stage is where the corpus policy's revocation machinery actually bites:
documents whose doc-scoped tombstone exists are dropped, and every document
by an account-scoped-tombstoned author is dropped. **No training run may read
the corpus except through this step** — training on raw partitions would
silently ignore revocations the harvester faithfully recorded.

Also applied here:
  - content_sha256 dedupe (reposts/crossposts collapse to the earliest copy),
    per hash_version — different normalization versions never dedupe against
    each other;
  - length bounds;
  - a deterministic cap to ``max_sequences`` (ordered by a hash of doc_id, so
    the same corpus always yields the same training set — reproducible runs,
    and the cap is what keeps the trainer's wall-clock bill flat as the
    corpus grows).

Output: one parquet plus a JSON manifest recording counts and the tombstones
applied — the reproducibility pin for the run.
"""

from __future__ import annotations

import datetime as dt
import json
from dataclasses import dataclass, field
from pathlib import Path

DOC_GLOB = "documents/dt=*/platform=*/*.jsonl.gz"
TOMB_GLOB = "tombstones/dt=*/platform=*/*.jsonl.gz"


@dataclass(frozen=True)
class PrepConfig:
    corpus_root: str
    out_path: str  # parquet; manifest lands beside it as <out>.manifest.json
    max_sequences: int = 1_840_000
    min_chars: int = 40
    max_chars: int = 2000


@dataclass
class PrepStats:
    raw_documents: int = 0
    doc_tombstones: int = 0
    account_tombstones: int = 0
    dropped_by_tombstone: int = 0
    dropped_by_length: int = 0
    dropped_as_duplicate: int = 0
    kept: int = 0
    per_platform: dict[str, int] = field(default_factory=dict)


def build_training_set(cfg: PrepConfig) -> PrepStats:
    import duckdb  # lazy: serving installs don't need it

    root = Path(cfg.corpus_root)
    doc_files = sorted(str(p) for p in root.glob(DOC_GLOB))
    if not doc_files:
        raise FileNotFoundError(f"no corpus documents under {root}/{DOC_GLOB}")
    tomb_files = sorted(str(p) for p in root.glob(TOMB_GLOB))

    con = duckdb.connect()
    con.sql(f"""
        CREATE VIEW docs_raw AS
        SELECT * FROM read_json_auto({doc_files!r}, format='newline_delimited',
                                     union_by_name=true)
    """)
    # Optional columns (author_did, author, langs, created_at) simply don't
    # exist when no input row carries them — an HN-only corpus has no langs —
    # so project a fixed schema, filling NULL for whatever is absent.
    present = {row[0] for row in con.sql("DESCRIBE docs_raw").fetchall()}
    required = {"platform", "doc_id", "text", "content_sha256", "hash_version"}
    if missing := required - present:
        raise ValueError(f"corpus rows are missing required columns: {sorted(missing)}")
    optional = {"author_did": "VARCHAR", "author": "VARCHAR",
                "langs": "VARCHAR[]", "created_at": "VARCHAR"}
    projected = ", ".join(
        [*sorted(required)],
    ) + ", " + ", ".join(
        (col if col in present else f"NULL::{typ} AS {col}")
        for col, typ in optional.items()
    )
    con.sql(f"CREATE VIEW docs AS SELECT {projected} FROM docs_raw")
    if tomb_files:
        con.sql(f"""
            CREATE VIEW tombs AS
            SELECT * FROM read_json_auto({tomb_files!r}, format='newline_delimited',
                                         union_by_name=true)
        """)
    else:
        con.sql("""
            CREATE VIEW tombs AS
            SELECT NULL::VARCHAR AS platform, NULL::VARCHAR AS scope,
                   NULL::VARCHAR AS doc_id, NULL::VARCHAR AS author_did
            WHERE false
        """)

    stats = PrepStats()
    stats.raw_documents = con.sql("SELECT count(*) FROM docs").fetchone()[0]
    stats.doc_tombstones = con.sql(
        "SELECT count(*) FROM tombs WHERE scope = 'doc'").fetchone()[0]
    stats.account_tombstones = con.sql(
        "SELECT count(*) FROM tombs WHERE scope = 'account'").fetchone()[0]

    # Revocations first, then length, then dedupe, then the deterministic cap.
    # doc_id is only unique within a platform, so tombstones match on both.
    con.sql("""
        CREATE VIEW alive AS
        SELECT d.* FROM docs d
        WHERE NOT EXISTS (
            SELECT 1 FROM tombs t
            WHERE t.scope = 'doc' AND t.platform = d.platform AND t.doc_id = d.doc_id
        )
        AND NOT EXISTS (
            SELECT 1 FROM tombs t
            WHERE t.scope = 'account' AND t.platform = d.platform
              AND d.author_did IS NOT NULL AND t.author_did = d.author_did
        )
    """)
    con.sql(f"""
        CREATE VIEW sized AS
        SELECT * FROM alive
        WHERE length(text) >= {int(cfg.min_chars)}
          AND length(text) <= {int(cfg.max_chars)}
    """)
    con.sql("""
        CREATE VIEW deduped AS
        SELECT * FROM sized
        QUALIFY row_number() OVER (
            PARTITION BY hash_version, content_sha256
            ORDER BY created_at NULLS LAST, doc_id
        ) = 1
    """)

    alive = con.sql("SELECT count(*) FROM alive").fetchone()[0]
    sized = con.sql("SELECT count(*) FROM sized").fetchone()[0]
    deduped = con.sql("SELECT count(*) FROM deduped").fetchone()[0]
    stats.dropped_by_tombstone = stats.raw_documents - alive
    stats.dropped_by_length = alive - sized
    stats.dropped_as_duplicate = sized - deduped

    out = Path(cfg.out_path)
    out.parent.mkdir(parents=True, exist_ok=True)
    con.sql(f"""
        COPY (
            SELECT platform, doc_id, author_did, author, text, langs,
                   created_at, content_sha256, hash_version
            FROM deduped
            ORDER BY md5(platform || '|' || doc_id)
            LIMIT {int(cfg.max_sequences)}
        ) TO '{out}' (FORMAT parquet)
    """)

    stats.kept = con.sql(f"SELECT count(*) FROM read_parquet('{out}')").fetchone()[0]
    stats.per_platform = dict(con.sql(
        f"SELECT platform, count(*) FROM read_parquet('{out}') GROUP BY 1"
    ).fetchall())

    manifest = {
        "built_at": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "config": {
            "corpus_root": cfg.corpus_root,
            "max_sequences": cfg.max_sequences,
            "min_chars": cfg.min_chars,
            "max_chars": cfg.max_chars,
        },
        "stats": {
            "raw_documents": stats.raw_documents,
            "doc_tombstones": stats.doc_tombstones,
            "account_tombstones": stats.account_tombstones,
            "dropped_by_tombstone": stats.dropped_by_tombstone,
            "dropped_by_length": stats.dropped_by_length,
            "dropped_as_duplicate": stats.dropped_as_duplicate,
            "kept": stats.kept,
            "per_platform": stats.per_platform,
        },
        "inputs": {"document_files": len(doc_files), "tombstone_files": len(tomb_files)},
    }
    Path(str(out) + ".manifest.json").write_text(json.dumps(manifest, indent=2))
    return stats
