"""Aspect teacher labeling: soft labels for (doc, aspect) pairs, incrementally.

The FROZEN aspect teacher scores (text, aspect) pairs mined from the training
set that don't already carry a label for the current teacher_version. Labels
are probabilities in the canonical (negative, neutral, positive) order, stored
as float32 parquet keyed by (platform, doc_id, aspect, teacher_version).

Pair volume, not corpus size, is the cost driver: each document yields up to
MAX_ASPECTS_PER_DOC mined aspects, and the global pair list is capped
deterministically (ordered by md5 of the pair key) so re-runs converge on the
same subset instead of drifting onto fresh pairs. Incrementality then works
exactly as in label.py: a re-run labels only pairs that never flushed.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

# Pinned identity of the frozen aspect-teacher configuration. Bump ONLY
# alongside a corpus-policy discussion: soft labels from different teachers
# must never mix in one training run.
ASPECT_TEACHER_VERSION = "yangheng/deberta-v3-base-absa-v1.1@fp32-v1"

# Per-document pair budget; the cap on total pairs is max_pairs below.
MAX_ASPECTS_PER_DOC = 2


@dataclass
class AspectLabelStats:
    total_pairs: int = 0
    already_labeled: int = 0
    newly_labeled: int = 0


def label_aspect_pairs(
    train_parquet: str | Path,
    aspect_labels_parquet: str | Path,
    backend,  # anything with .classify_aspects(items) -> [[(aspect, Prediction)]]; the frozen teacher in prod
    *,
    batch_size: int = 64,
    teacher_version: str = ASPECT_TEACHER_VERSION,
    flush_every: int = 25_000,
    checkpoint=None,  # () -> None, fired after each flush lands on disk
    max_pairs: int = 400_000,
    mine=None,  # (text) -> [aspect]; defaults to aspects.mine_aspects
) -> AspectLabelStats:
    """Label unlabeled (doc, aspect) pairs into aspect_labels_parquet (append).

    The capped pair set is a deterministic function of the pairs themselves
    (ordered by md5 of the pair key), so labeling it is idempotent under the
    cap. Labels land on disk every ``flush_every`` pairs, and ``checkpoint``
    fires after each flush (the Modal caller commits the Volume there), so a
    timeout or preemption loses at most one flush window and the retry
    relabels only what never flushed. Work is ordered by text length for the
    same padding reason as label.py.
    """
    import duckdb

    if mine is None:
        # Imported inside the function: a module-top import of aspects would
        # cycle through the pipeline package.
        from sentilyzer_ml.pipeline.aspects import mine_aspects

        mine = mine_aspects

    train_parquet = str(train_parquet)
    labels_path = Path(aspect_labels_parquet)

    con = duckdb.connect()
    docs = con.sql(f"""
        SELECT platform, doc_id, text FROM read_parquet('{train_parquet}')
    """).fetchall()

    # Keys are (platform, doc_id, aspect): dedupe what the miner returns so a
    # repeated aspect can't double-label within one run.
    pair_rows: list[tuple[str, str, str, str]] = []
    for platform, doc_id, text in docs:
        for aspect in list(dict.fromkeys(mine(text)))[:MAX_ASPECTS_PER_DOC]:
            pair_rows.append((platform, doc_id, aspect, text))

    stats = AspectLabelStats()
    stats.total_pairs = min(len(pair_rows), max_pairs)
    if not pair_rows:
        return stats

    con.sql("CREATE TABLE pairs (platform VARCHAR, doc_id VARCHAR, aspect VARCHAR, text VARCHAR)")
    con.executemany("INSERT INTO pairs VALUES (?, ?, ?, ?)", pair_rows)

    if labels_path.exists():
        con.sql(f"""
            CREATE VIEW existing AS
            SELECT platform, doc_id, aspect FROM read_parquet('{labels_path}')
            WHERE teacher_version = '{teacher_version}'
        """)
    else:
        con.sql("""
            CREATE VIEW existing AS
            SELECT NULL::VARCHAR AS platform, NULL::VARCHAR AS doc_id,
                   NULL::VARCHAR AS aspect WHERE false
        """)

    # Materialized, not streamed: each flush rewrites aspect_labels_parquet,
    # and the existing-labels view reads from it, so nothing may lazily scan
    # that file once labeling starts. chr(31) is a unit separator, so the
    # concatenated cap key cannot collide across column boundaries.
    con.sql(f"""
        CREATE TABLE todo AS
        WITH capped AS (
            SELECT platform, doc_id, aspect, text
            FROM pairs
            ORDER BY md5(platform || chr(31) || doc_id || chr(31) || aspect)
            LIMIT {max_pairs}
        )
        SELECT row_number() OVER (ORDER BY length(c.text), c.platform, c.doc_id, c.aspect) AS rn,
               c.platform, c.doc_id, c.aspect, c.text
        FROM capped c
        ANTI JOIN existing e USING (platform, doc_id, aspect)
    """)
    n_todo = con.sql("SELECT count(*) FROM todo").fetchone()[0]
    stats.already_labeled = stats.total_pairs - n_todo
    if not n_todo:
        return stats

    labels_path.parent.mkdir(parents=True, exist_ok=True)
    con.sql("""
        CREATE TABLE new_labels (
            platform VARCHAR, doc_id VARCHAR, aspect VARCHAR,
            p_negative FLOAT, p_neutral FLOAT, p_positive FLOAT,
            teacher_version VARCHAR
        )
    """)

    def flush(rows: list[tuple[str, str, str, float, float, float, str]]) -> None:
        con.executemany("INSERT INTO new_labels VALUES (?, ?, ?, ?, ?, ?, ?)", rows)
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
            SELECT platform, doc_id, aspect, text FROM todo
            WHERE rn BETWEEN {window_start} AND {window_start + flush_every - 1}
            ORDER BY rn
        """).fetchall()
        rows: list[tuple[str, str, str, float, float, float, str]] = []
        for start in range(0, len(window), batch_size):
            chunk = window[start : start + batch_size]
            results = backend.classify_aspects([(r[3], [r[2]]) for r in chunk])
            if len(results) != len(chunk):
                raise RuntimeError(
                    f"teacher returned {len(results)} results for {len(chunk)} pairs"
                )
            for (platform, doc_id, aspect, _), scored in zip(chunk, results, strict=True):
                if len(scored) != 1:
                    raise RuntimeError(
                        f"teacher returned {len(scored)} predictions for 1 aspect"
                    )
                p = scored[0][1].probabilities  # canonical (neg, neu, pos) per inference.py
                rows.append(
                    (platform, doc_id, aspect, float(p[0]), float(p[1]), float(p[2]), teacher_version)
                )
        flush(rows)

    return stats
