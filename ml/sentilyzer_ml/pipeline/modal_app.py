"""Train a Sentilyzer student on YOUR Modal account — clone and run.

    git clone <repo> && cd sentilyzer/ml
    pip install modal && modal setup          # your Modal account; we never see the key
    modal run sentilyzer_ml/pipeline/modal_app.py \
        --from-month 2025-06 --output ./student

That single command, entirely on your Modal account (the recurring $30/mo
credit covers it): ingests the free HackerNews archive into a Modal Volume,
labels it with the frozen teacher (T4), distills the student (A10, 45-min
hard cap ≈ $1), runs the eval gate, and downloads the INT8 ONNX model +
tokenizer + eval report to --output. No other accounts, no servers, no
object storage — everything lives in the Volume on your Modal account.

Options:
    --from-month / --to-month   archive window to ingest (YYYY-MM)
    --limit N                   per-month row cap (quick smoke runs)
    --corpus DIR                use YOUR local corpus instead of the archive
                                (uploaded to the Volume; harvester layout)
    --output DIR                where the trained model lands locally
    --skip-ingest               reuse whatever the Volume already holds

Repeat runs are incremental: ingested months and teacher labels are reused,
so a second run mostly pays for training (~$1).

OPERATOR MODE (optional, for running the always-on API): set
SENTILYZER_USE_R2=1 when deploying and the app additionally reads harvested
corpus from R2 and promotes gated models to R2's students/current.json — the
pointer a serving box hot-swaps from (see model_store.py). Requires the
`sentilyzer-r2` Modal secret. The default mode references no secret at all.

COST GUARDS (all enforced in code): the trainer's wall clock is capped
(MAX_TRAIN_SECONDS), the sequence cap is asserted on CPU before any GPU
spawns, GPU functions run with retries=0, a run lock stops double-fires,
and nothing has a schedule — an untriggered month costs $0.
"""

from __future__ import annotations

import json
import os
import time
from pathlib import Path

import modal

APP_NAME = "sentilyzer-train"

# ── THE CONSTANTS THAT BOUND THE BILL ──────────────────────────────────────
MAX_TRAIN_SECONDS = 2_700  # 45 min on A10 ≈ $0.99/run, fixed forever
MAX_TRAIN_SEQUENCES = 1_840_000  # asserted on CPU before any GPU spawns
LOCK_STALE_SECONDS = 3 * 3600  # a crashed run frees the lock after this
HOLDOUT_ROWS = 5_000  # tail of the deterministic prep order; never trained
STUDENT_LAYERS = 6
TEACHER_MODEL = "cardiffnlp/twitter-roberta-base-sentiment-latest"
# ───────────────────────────────────────────────────────────────────────────

# Operator mode is decided where `modal run`/`modal deploy` is invoked — the
# default app references no secret, so a fresh clone needs zero setup beyond
# `modal setup`.
USE_R2 = os.environ.get("SENTILYZER_USE_R2") == "1"

app = modal.App(APP_NAME)
volume = modal.Volume.from_name("sentilyzer-train", create_if_missing=True)
run_lock = modal.Dict.from_name("sentilyzer-train-lock", create_if_missing=True)
_secrets = [modal.Secret.from_name("sentilyzer-r2")] if USE_R2 else []

DATA = "/data"

cpu_image = (
    modal.Image.debian_slim(python_version="3.12")
    .pip_install("duckdb>=1.0", "boto3>=1.34", "numpy>=1.26", "onnxruntime>=1.18", "fastapi")
    .add_local_python_source("sentilyzer_ml")
)
gpu_image = (
    modal.Image.debian_slim(python_version="3.12")
    # transformers <5: dtype="auto" in 5.x can silently load fp16 teachers,
    # corrupting every soft label a student learns from.
    .pip_install(
        "torch>=2.4",
        "transformers>=4.45,<5",
        "tokenizers>=0.20",
        "duckdb>=1.0",
        "numpy>=1.26",
        # onnx is quantize_dynamic's own dependency — without it the INT8
        # step dies with ModuleNotFoundError (verified locally).
        "onnxruntime>=1.18",
        "onnx>=1.16",
        "scipy>=1.11",
    )
    .add_local_python_source("sentilyzer_ml")
)


@app.function(image=cpu_image, volumes={DATA: volume}, timeout=3600)
def ingest_hn(from_month: str, to_month: str = "", limit: int = 0) -> dict:
    """Stream the free HN archive into the Volume's corpus — the default,
    zero-infrastructure data source. Incremental: done months are skipped."""
    from sentilyzer_ml.pipeline.hn_archive import ingest_months

    total = ingest_months(
        Path(DATA) / "corpus",
        from_month,
        to_month or None,
        limit=limit or None,
    )
    volume.commit()
    return {"ingested": total}


def _sync_corpus_from_r2(dest: Path) -> int:
    """Operator mode only: pull harvested partitions down from R2."""
    import boto3

    s3 = boto3.client("s3", endpoint_url=os.environ["R2_ENDPOINT_URL"])
    bucket = os.environ["R2_BUCKET"]
    dest.mkdir(parents=True, exist_ok=True)
    n = 0
    paginator = s3.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=bucket, Prefix="corpus/"):
        for item in page.get("Contents", []):
            rel = item["Key"][len("corpus/"):]
            if not rel:
                continue
            target = dest / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            s3.download_file(bucket, item["Key"], str(target))
            n += 1
    return n


@app.function(image=cpu_image, volumes={DATA: volume}, secrets=_secrets, timeout=1800)
def prep(run_id: str) -> dict:
    from sentilyzer_ml.pipeline.prep import PrepConfig, build_training_set

    corpus = Path(DATA) / "corpus"
    synced = _sync_corpus_from_r2(corpus) if USE_R2 else 0
    out = Path(DATA) / "runs" / run_id / "train.parquet"
    stats = build_training_set(
        PrepConfig(
            corpus_root=str(corpus),
            out_path=str(out),
            max_sequences=MAX_TRAIN_SEQUENCES,
        )
    )
    # The CPU-side assertion that keeps the GPU bill bounded.
    assert stats.kept <= MAX_TRAIN_SEQUENCES, "prep exceeded the sequence cap"
    volume.commit()
    return {
        "r2_files_synced": synced,
        "kept": stats.kept,
        "dropped_by_tombstone": stats.dropped_by_tombstone,
        "per_platform": stats.per_platform,
    }


@app.function(
    image=gpu_image, gpu="T4", volumes={DATA: volume},
    timeout=7200, retries=0,
)
def label(run_id: str) -> dict:
    import torch

    from sentilyzer_ml.inference import TransformerBackend
    from sentilyzer_ml.pipeline.label import label_training_set

    # The frozen teacher, explicitly fp32 (see the transformers<5 pin above).
    backend = TransformerBackend(
        TEACHER_MODEL, TEACHER_MODEL,
        device="cuda" if torch.cuda.is_available() else "cpu",
    )
    run_dir = Path(DATA) / "runs" / run_id
    stats = label_training_set(
        run_dir / "train.parquet",
        Path(DATA) / "labels" / "labels.parquet",  # shared across runs: incremental
        backend,
    )
    volume.commit()
    return {"total": stats.total_rows, "already": stats.already_labeled, "new": stats.newly_labeled}


@app.function(
    image=gpu_image, gpu="A10", volumes={DATA: volume},
    timeout=MAX_TRAIN_SECONDS + 300,  # headroom so OUR budget stops us, never Modal's
    retries=0, max_containers=1,
)
def train(run_id: str) -> dict:
    import duckdb
    import torch
    from transformers import AutoModelForSequenceClassification, AutoTokenizer

    from sentilyzer_ml.pipeline.export import export_student
    from sentilyzer_ml.pipeline.kd import DistillConfig, build_student_from_teacher, distill

    run_dir = Path(DATA) / "runs" / run_id
    rows = duckdb.sql(f"""
        SELECT t.text, l.p_negative, l.p_neutral, l.p_positive
        FROM read_parquet('{run_dir / "train.parquet"}') t
        JOIN read_parquet('{Path(DATA) / "labels" / "labels.parquet"}') l
        USING (platform, doc_id)
    """).fetchall()
    if len(rows) <= HOLDOUT_ROWS:
        raise RuntimeError(f"only {len(rows)} labeled rows; not enough to hold out {HOLDOUT_ROWS}")
    # Prep's output order is deterministic; the tail is the held-out slice.
    train_rows = rows[:-HOLDOUT_ROWS]

    texts = [r[0] for r in train_rows]
    probs = torch.tensor([[r[1], r[2], r[3]] for r in train_rows], dtype=torch.float32)

    # THE COLLAPSE FIREWALL: the student is built from the FROZEN teacher,
    # fresh, every run. Never from a previous student.
    teacher = AutoModelForSequenceClassification.from_pretrained(
        TEACHER_MODEL, torch_dtype=torch.float32
    )
    student = build_student_from_teacher(teacher, STUDENT_LAYERS)
    tokenizer = AutoTokenizer.from_pretrained(TEACHER_MODEL)

    def tokenize(batch: list[str]):
        return tokenizer(batch, padding=True, truncation=True, max_length=128, return_tensors="pt")

    result = distill(
        student, texts, probs, tokenize,
        DistillConfig(
            student_layers=STUDENT_LAYERS,
            max_seconds=MAX_TRAIN_SECONDS - 600,  # leave room for export inside the cap
            max_sequences=MAX_TRAIN_SEQUENCES,
        ),
        checkpoint_dir=run_dir / "ckpt",
    )

    sample = tokenize(texts[:8])
    export = export_student(student.cpu(), run_dir / "model", sample_batch=sample)
    tokenizer.save_pretrained(str(run_dir / "model"))
    volume.commit()
    return {
        "steps": result.steps,
        "sequences_seen": result.sequences_seen,
        "partial": result.partial,
        "resumed": result.resumed,
        "final_loss": result.final_loss,
        "int8_sha256": export.int8_sha256,
        "int8_bytes": export.int8_bytes,
    }


@app.function(image=cpu_image, volumes={DATA: volume}, timeout=1800)
def evaluate(run_id: str) -> dict:
    import duckdb
    import numpy as np
    import onnxruntime as ort
    from tokenizers import Tokenizer

    from sentilyzer_ml.pipeline.evalgate import GateConfig, evaluate_gate

    run_dir = Path(DATA) / "runs" / run_id
    rows = duckdb.sql(f"""
        SELECT t.text, l.p_negative, l.p_neutral, l.p_positive
        FROM read_parquet('{run_dir / "train.parquet"}') t
        JOIN read_parquet('{Path(DATA) / "labels" / "labels.parquet"}') l
        USING (platform, doc_id)
    """).fetchall()
    heldout = rows[-HOLDOUT_ROWS:]

    tok = Tokenizer.from_file(str(run_dir / "model" / "tokenizer.json"))
    tok.enable_truncation(max_length=128)
    sess = ort.InferenceSession(
        str(run_dir / "model" / "model.int8.onnx"), providers=["CPUExecutionProvider"]
    )
    student_probs = []
    for start in range(0, len(heldout), 64):
        chunk = [r[0] for r in heldout[start : start + 64]]
        encs = tok.encode_batch(chunk)
        width = max(len(e.ids) for e in encs)
        ids = np.array([e.ids + [1] * (width - len(e.ids)) for e in encs], dtype=np.int64)
        mask = np.array(
            [e.attention_mask + [0] * (width - len(e.attention_mask)) for e in encs],
            dtype=np.int64,
        )
        logits_doc, _ = sess.run(None, {"input_ids": ids, "attention_mask": mask})
        z = logits_doc - logits_doc.max(axis=1, keepdims=True)
        e = np.exp(z)
        student_probs.append(e / e.sum(axis=1, keepdims=True))
    student = np.concatenate(student_probs)
    teacher = np.array([[r[1], r[2], r[3]] for r in heldout], dtype=np.float32)

    gate = evaluate_gate(student, teacher, GateConfig())
    out = {"passed": gate.passed, "reasons": gate.reasons, **gate.metrics}
    (run_dir / "eval.json").write_text(json.dumps(out, indent=2))
    volume.commit()
    return out


if USE_R2:

    @app.function(image=cpu_image, volumes={DATA: volume}, secrets=_secrets, timeout=900)
    def promote(run_id: str, metrics: dict) -> dict:
        """Operator mode: upload the artifact and flip R2's current.json —
        the pointer a serving box's ModelManager polls. Rollback = re-running
        this for a prior run_id."""
        import boto3

        from sentilyzer_ml.pipeline.export import sha256_file

        s3 = boto3.client("s3", endpoint_url=os.environ["R2_ENDPOINT_URL"])
        bucket = os.environ["R2_BUCKET"]
        run_dir = Path(DATA) / "runs" / run_id / "model"
        sha = sha256_file(run_dir / "model.int8.onnx")
        for name in ("model.int8.onnx", "tokenizer.json"):
            s3.upload_file(str(run_dir / name), bucket, f"students/{run_id}/{name}")
        pointer = {"run_id": run_id, "sha256": sha, "metrics": metrics}
        s3.put_object(
            Bucket=bucket, Key="students/current.json",
            Body=json.dumps(pointer).encode(), ContentType="application/json",
        )
        return pointer


@app.function(image=cpu_image, timeout=4 * 3600)
def run_pipeline(auto_promote: bool = False) -> dict:
    """Orchestrates one run: lock → prep → label → train → evaluate →
    (promote in operator mode). Returns everything it decided."""
    held = run_lock.get("run", None)
    now = time.time()
    if held and now - held.get("started_at", 0) < LOCK_STALE_SECONDS:
        return {"skipped": f"run {held['run_id']} already in flight"}

    run_id = time.strftime("train:%Y-%m-%d-%H%M%S", time.gmtime())
    run_lock["run"] = {"run_id": run_id, "started_at": now}
    try:
        summary: dict = {"run_id": run_id}
        summary["prep"] = prep.remote(run_id)
        summary["label"] = label.remote(run_id)
        summary["train"] = train.remote(run_id)
        summary["eval"] = evaluate.remote(run_id)
        if USE_R2 and auto_promote and summary["eval"]["passed"]:
            summary["promoted"] = promote.remote(run_id, summary["eval"])
        else:
            summary["promoted"] = False
        return summary
    finally:
        run_lock.pop("run", None)


@app.function(image=cpu_image, volumes={DATA: volume}, timeout=600)
def read_artifact(run_id: str) -> dict[str, bytes]:
    """Hand the trained artifact back so the CLI can write it locally —
    the self-service output path needs no storage account at all."""
    run_dir = Path(DATA) / "runs" / run_id
    out: dict[str, bytes] = {}
    for name in ("model/model.int8.onnx", "model/tokenizer.json", "eval.json",
                 "train.parquet.manifest.json"):
        path = run_dir / name
        if path.exists():
            out[Path(name).name] = path.read_bytes()
    return out


@app.function(image=cpu_image, timeout=60)
@modal.fastapi_endpoint(method="POST", requires_proxy_auth=True)
def trigger(auto_promote: bool = False) -> dict:
    """HTTP trigger for deployed apps: spawns a run and returns the call id.
    Proxy-auth keeps this from being a public free-GPU button."""
    call = run_pipeline.spawn(auto_promote)
    return {"spawned": call.object_id}


@app.local_entrypoint()
def main(
    from_month: str = "2025-01",
    to_month: str = "",
    limit: int = 0,
    corpus: str = "",
    output: str = "./student",
    skip_ingest: bool = False,
    auto_promote: bool = False,
):
    """The clone-and-run flow. See the module docstring for examples."""
    if corpus:
        src = Path(corpus)
        if not src.is_dir():
            raise SystemExit(f"--corpus {corpus}: not a directory")
        print(f"uploading local corpus {src} -> Volume /corpus …")
        with volume.batch_upload(force=True) as batch:
            batch.put_directory(str(src), "/corpus")
    elif not skip_ingest:
        print(f"ingesting HN archive {from_month}..{to_month or 'latest full month'} …")
        print(json.dumps(ingest_hn.remote(from_month, to_month, limit)))

    summary = run_pipeline.remote(auto_promote)
    print(json.dumps(summary, indent=2))
    if "skipped" in summary:
        return

    out_dir = Path(output)
    out_dir.mkdir(parents=True, exist_ok=True)
    for name, blob in read_artifact.remote(summary["run_id"]).items():
        (out_dir / name).write_bytes(blob)
        print(f"wrote {out_dir / name} ({len(blob):,} bytes)")
    verdict = summary.get("eval", {})
    print(f"\ngate: {'PASSED' if verdict.get('passed') else 'FAILED'} "
          f"(agreement={verdict.get('agreement')})")
    print(f"model: {out_dir / 'model.int8.onnx'}")
    print("serve it locally: SENTILYZER_ML_USE_STUB=0 + point OnnxBackend at that directory,")
    print("or with the API: make up, then set the model-store env (see README).")


@app.local_entrypoint()
def promote_run(run_id: str):
    """Operator mode: manually promote (or roll back to) a run's artifact."""
    if not USE_R2:
        raise SystemExit("promotion needs operator mode: SENTILYZER_USE_R2=1 and the sentilyzer-r2 secret")
    print(json.dumps(promote.remote(run_id, {"manual": True}), indent=2))
