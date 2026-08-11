"""Teacher labeling: soft labels for the training set, incrementally.

The FROZEN teacher scores every training-set row that doesn't already carry a
label for the current teacher_version. Labels are probabilities in the
canonical (negative, neutral, positive) order — the exact distribution the KD
loss reconstructs logits from — stored as float32 parquet keyed by
(platform, doc_id, teacher_version).

Incrementality is the cost lever: a re-run labels only new rows, so labeling
spend tracks fresh documents, not corpus size.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

# Pinned identity of the frozen teacher configuration. Bump ONLY alongside a
# corpus-policy discussion — soft labels from different teachers must never
# mix in one training run.
TEACHER_VERSION = "cardiffnlp/twitter-roberta-base-sentiment-latest@fp32-v1"


@dataclass
class LabelStats:
    total_rows: int = 0
    already_labeled: int = 0
    newly_labeled: int = 0


def label_training_set(
    train_parquet: str | Path,
    labels_parquet: str | Path,
    backend,  # anything with .classify(texts) -> [Prediction]; the frozen teacher in prod
    *,
    batch_size: int = 32,
    teacher_version: str = TEACHER_VERSION,
    flush_every: int = 50_000,
    checkpoint=None,  # () -> None, fired after each flush lands on disk
) -> LabelStats:
    """Label unlabeled rows of train_parquet into labels_parquet (append).

    Labels land on disk every ``flush_every`` rows, and ``checkpoint`` fires
    after each flush (the Modal caller commits the Volume there) — so a
    timeout or preemption loses at most one flush window, and the retry
    relabels only what never flushed. Work is ordered by text length: the
    teacher pads each batch to its longest member, so batching
    similar-length texts together stops short texts burning compute on
    padding.
    """
    import duckdb

    train_parquet = str(train_parquet)
    labels_path = Path(labels_parquet)

    con = duckdb.connect()
    con.sql(f"""
        CREATE VIEW train AS
        SELECT platform, doc_id, text FROM read_parquet('{train_parquet}')
    """)
    if labels_path.exists():
        con.sql(f"""
            CREATE VIEW existing AS
            SELECT platform, doc_id FROM read_parquet('{labels_path}')
            WHERE teacher_version = '{teacher_version}'
        """)
    else:
        con.sql("""
            CREATE VIEW existing AS
            SELECT NULL::VARCHAR AS platform, NULL::VARCHAR AS doc_id WHERE false
        """)

    stats = LabelStats()
    stats.total_rows = con.sql("SELECT count(*) FROM train").fetchone()[0]
    # Materialized, not streamed: each flush rewrites labels_parquet, and the
    # existing-labels view reads from it — nothing may lazily scan that file
    # once labeling starts.
    con.sql("""
        CREATE TABLE todo AS
        SELECT row_number() OVER (ORDER BY length(t.text), t.platform, t.doc_id) AS rn,
               t.platform, t.doc_id, t.text
        FROM train t
        ANTI JOIN existing e USING (platform, doc_id)
    """)
    n_todo = con.sql("SELECT count(*) FROM todo").fetchone()[0]
    stats.already_labeled = stats.total_rows - n_todo
    if not n_todo:
        return stats

    labels_path.parent.mkdir(parents=True, exist_ok=True)
    con.sql("""
        CREATE TABLE new_labels (
            platform VARCHAR, doc_id VARCHAR,
            p_negative FLOAT, p_neutral FLOAT, p_positive FLOAT,
            teacher_version VARCHAR
        )
    """)

    def flush(rows: list[tuple[str, str, float, float, float, str]]) -> None:
        con.executemany("INSERT INTO new_labels VALUES (?, ?, ?, ?, ?, ?)", rows)
        if labels_path.exists():
            con.sql(f"""
                COPY (
                    SELECT * FROM read_parquet('{labels_path}')
                    UNION ALL SELECT * FROM new_labels
                ) TO '{labels_path}.new' (FORMAT parquet)
            """)
            Path(f"{labels_path}.new").replace(labels_path)
        else:
            con.sql(f"COPY new_labels TO '{labels_path}' (FORMAT parquet)")
        con.sql("DELETE FROM new_labels")
        stats.newly_labeled += len(rows)
        if checkpoint is not None:
            checkpoint()

    for window_start in range(1, n_todo + 1, flush_every):
        window = con.sql(f"""
            SELECT platform, doc_id, text FROM todo
            WHERE rn BETWEEN {window_start} AND {window_start + flush_every - 1}
            ORDER BY rn
        """).fetchall()
        rows: list[tuple[str, str, float, float, float, str]] = []
        for start in range(0, len(window), batch_size):
            chunk = window[start : start + batch_size]
            # Length-sorted order means the final windows are ALL long texts:
            # a full batch of near-max-length sequences peaks past a T4's
            # memory (observed live as a recovered 1.6GB OOM). Halve those.
            if max(len(r[2] or "") for r in chunk) > 1200 and len(chunk) > 1:
                mid = len(chunk) // 2
                parts = [chunk[:mid], chunk[mid:]]
            else:
                parts = [chunk]
            for part in parts:
                preds = backend.classify([r[2] for r in part])
                if len(preds) != len(part):
                    raise RuntimeError(
                        f"teacher returned {len(preds)} predictions for {len(part)} texts"
                    )
                for (platform, doc_id, _), pred in zip(part, preds, strict=True):
                    p = pred.probabilities  # canonical (neg, neu, pos) per inference.py
                    rows.append((platform, doc_id, float(p[0]), float(p[1]), float(p[2]), teacher_version))
        flush(rows)

    return stats
