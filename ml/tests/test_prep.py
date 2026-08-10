"""Prep-step tests: the revocation machinery must actually revoke.

The fixture corpus mirrors the harvester's real layout and exercises every
drop path: a doc-scoped tombstone, an account-scoped tombstone, a cross-
platform content duplicate, a too-short row, and the deterministic cap.
"""

from __future__ import annotations

import gzip
import hashlib
import json
from pathlib import Path

import pytest

pytest.importorskip("duckdb")

from sentilyzer_ml.pipeline.prep import PrepConfig, build_training_set  # noqa: E402


def sha(text: str) -> str:
    return hashlib.sha256(" ".join(text.split()).lower().encode()).hexdigest()


def write_part(root: Path, kind: str, date: str, platform: str, rows: list[dict]):
    d = root / kind / f"dt={date}" / f"platform={platform}"
    d.mkdir(parents=True, exist_ok=True)
    with gzip.open(d / f"part-{len(rows)}.jsonl.gz", "wt") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")


def doc(platform: str, doc_id: str, text: str, author_did: str = "", created: str = "2026-08-01T00:00:00Z") -> dict:
    return {
        "platform": platform,
        "doc_id": doc_id,
        "author_did": author_did or None,
        "author": "someone",
        "text": text,
        "created_at": created,
        "harvested_at": "2026-08-10T00:00:00Z",
        "content_sha256": sha(text),
        "hash_version": 1,
    }


@pytest.fixture()
def corpus(tmp_path: Path) -> Path:
    long = "this text is comfortably long enough to pass the minimum length filter"
    write_part(tmp_path, "documents", "2026-08-01", "bluesky", [
        doc("bluesky", "did:plc:a/app.bsky.feed.post/r1", long + " one", "did:plc:a"),
        doc("bluesky", "did:plc:b/app.bsky.feed.post/r2", long + " two", "did:plc:b"),  # doc-tombstoned
        doc("bluesky", "did:plc:evil/app.bsky.feed.post/r3", long + " three", "did:plc:evil"),  # account-tombstoned
        doc("bluesky", "did:plc:evil/app.bsky.feed.post/r4", long + " four", "did:plc:evil"),   # account-tombstoned
        doc("bluesky", "did:plc:c/app.bsky.feed.post/r5", "short", "did:plc:c"),  # length filter
    ])
    write_part(tmp_path, "documents", "2026-08-02", "hackernews", [
        doc("hackernews", "hn-1", long + " one"),          # duplicate content of bluesky r1, later date
        doc("hackernews", "hn-2", long + " unique hn"),
    ])
    write_part(tmp_path, "tombstones", "2026-08-01", "bluesky", [
        {"platform": "bluesky", "scope": "doc",
         "doc_id": "did:plc:b/app.bsky.feed.post/r2", "author_did": "did:plc:b",
         "deleted_at": "2026-08-01T01:00:00Z"},
        {"platform": "bluesky", "scope": "account", "author_did": "did:plc:evil",
         "deleted_at": "2026-08-01T02:00:00Z"},
    ])
    return tmp_path


def test_prep_applies_revocations_and_dedupes(corpus: Path, tmp_path: Path):
    out = tmp_path / "train" / "train.parquet"
    stats = build_training_set(PrepConfig(corpus_root=str(corpus), out_path=str(out)))

    assert stats.raw_documents == 7
    assert stats.doc_tombstones == 1
    assert stats.account_tombstones == 1
    # r2 (doc tombstone) + r3, r4 (account tombstone) = 3 revoked rows.
    assert stats.dropped_by_tombstone == 3
    assert stats.dropped_by_length == 1
    # hn-1 duplicates bluesky r1's content; the earlier copy survives.
    assert stats.dropped_as_duplicate == 1
    assert stats.kept == 2

    import duckdb

    rows = duckdb.sql(f"SELECT platform, doc_id FROM read_parquet('{out}') ORDER BY doc_id").fetchall()
    ids = {r[1] for r in rows}
    assert ids == {"did:plc:a/app.bsky.feed.post/r1", "hn-2"}
    # Revoked and duplicate rows must be absent — the load-bearing assertions.
    assert "did:plc:b/app.bsky.feed.post/r2" not in ids
    assert "did:plc:evil/app.bsky.feed.post/r3" not in ids
    assert "hn-1" not in ids

    manifest = json.loads(Path(str(out) + ".manifest.json").read_text())
    assert manifest["stats"]["kept"] == 2
    assert manifest["stats"]["per_platform"] == {"bluesky": 1, "hackernews": 1}


def test_prep_cap_is_deterministic(corpus: Path, tmp_path: Path):
    outs = []
    for name in ("a", "b"):
        out = tmp_path / name / "train.parquet"
        build_training_set(PrepConfig(corpus_root=str(corpus), out_path=str(out), max_sequences=1))
        import duckdb

        outs.append(duckdb.sql(f"SELECT doc_id FROM read_parquet('{out}')").fetchall())
    # Same corpus + same cap => byte-identical selection, run after run.
    assert outs[0] == outs[1]
    assert len(outs[0]) == 1


def test_prep_without_tombstones(tmp_path: Path):
    long = "this text is comfortably long enough to pass the minimum length filter"
    write_part(tmp_path, "documents", "2026-08-01", "bluesky", [
        doc("bluesky", "did:plc:a/app.bsky.feed.post/r1", long, "did:plc:a"),
    ])
    out = tmp_path / "train.parquet"
    stats = build_training_set(PrepConfig(corpus_root=str(tmp_path), out_path=str(out)))
    assert stats.kept == 1
    assert stats.doc_tombstones == 0


def test_prep_refuses_empty_corpus(tmp_path: Path):
    with pytest.raises(FileNotFoundError):
        build_training_set(PrepConfig(corpus_root=str(tmp_path), out_path=str(tmp_path / "t.parquet")))
