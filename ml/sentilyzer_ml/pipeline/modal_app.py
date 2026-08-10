"""Modal app: ON-DEMAND distillation runs. No mandatory cron anywhere.

    modal deploy ml/sentilyzer_ml/pipeline/modal_app.py     # once
    modal run ml/sentilyzer_ml/pipeline/modal_app.py        # trigger a run
    curl -X POST <trigger-url>                               # or over HTTP

A run flows prep → label → train → evaluate → (promote when the gate passes
and auto_promote is on; otherwise it stops and prints what promote would do).
Serving never touches Modal: the Oracle box polls R2's current.json and
hot-swaps (see model_store.py), so Modal bills only these bursts.

COST GUARDS (why each exists — see docs/oracle-deployment.md §4):
  - MAX_TRAIN_SECONDS caps the trainer's wall clock; the bill stays ~$1/run
    forever while epochs-delivered float with corpus growth.
  - MAX_TRAIN_SEQUENCES is asserted on CPU BEFORE the GPU container spawns.
  - retries=0 on every GPU function: timeout × (retries+1) multiplies spend.
  - max_containers=1 + the run lock: on-demand means humans can double-fire;
    concurrent spawns must not each burn a GPU.
  - No schedule attached to anything. A month with no triggers costs $0.

ONE-TIME SETUP:
  modal secret create sentilyzer-r2 \
      AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... \
      R2_ENDPOINT_URL=https://<account>.r2.cloudflarestorage.com \
      R2_BUCKET=sentilyzer
  The harvest box syncs its corpus to R2 (e.g. `rclone sync corpus/ r2:sentilyzer/corpus/`).
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

app = modal.App(APP_NAME)
volume = modal.Volume.from_name("sentilyzer-train", create_if_missing=True)
run_lock = modal.Dict.from_name("sentilyzer-train-lock", create_if_missing=True)
r2_secret = modal.Secret.from_name("sentilyzer-r2")

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


def _r2():
    import boto3

    return boto3.client("s3", endpoint_url=os.environ["R2_ENDPOINT_URL"]), os.environ["R2_BUCKET"]


def _sync_corpus_from_r2(dest: Path) -> int:
    """Pull corpus partitions down for prep. Corpus is small relative to the
    work (~15 MB/day gz); optimize to incremental sync when it matters."""
    s3, bucket = _r2()
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


@app.function(image=cpu_image, volumes={DATA: volume}, secrets=[r2_secret], timeout=1800)
def prep(run_id: str) -> dict:
    from sentilyzer_ml.pipeline.prep import PrepConfig, build_training_set

    corpus = Path(DATA) / "corpus"
    files = _sync_corpus_from_r2(corpus)
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
        "corpus_files": files,
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
    backend = TransformerBackend(TEACHER_MODEL, TEACHER_MODEL, device="cuda" if torch.cuda.is_available() else "cpu")
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


@app.function(image=cpu_image, volumes={DATA: volume}, secrets=[r2_secret], timeout=900)
def promote(run_id: str, metrics: dict) -> dict:
    """Upload the run's artifact and flip current.json — the pointer the
    Oracle box's ModelManager polls. Rollback = re-running this for a prior
    run_id."""
    s3, bucket = _r2()
    run_dir = Path(DATA) / "runs" / run_id / "model"

    from sentilyzer_ml.pipeline.export import sha256_file

    int8 = run_dir / "model.int8.onnx"
    sha = sha256_file(int8)
    for name in ("model.int8.onnx", "tokenizer.json"):
        s3.upload_file(str(run_dir / name), bucket, f"students/{run_id}/{name}")
    pointer = {"run_id": run_id, "sha256": sha, "metrics": metrics}
    s3.put_object(
        Bucket=bucket,
        Key="students/current.json",
        Body=json.dumps(pointer).encode(),
        ContentType="application/json",
    )
    return pointer


@app.function(image=cpu_image, timeout=4 * 3600)
def run_pipeline(auto_promote: bool = False) -> dict:
    """The on-demand orchestrator: lock → prep → label → train → evaluate →
    maybe promote. Everything it decides is returned for the caller to see."""
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
        if summary["eval"]["passed"] and auto_promote:
            summary["promoted"] = promote.remote(run_id, summary["eval"])
        else:
            summary["promoted"] = False
            summary["promote_hint"] = (
                f"gate {'PASSED' if summary['eval']['passed'] else 'FAILED'}; "
                f"to promote manually: modal run …::promote_run --run-id {run_id}"
            )
        return summary
    finally:
        run_lock.pop("run", None)


@app.function(image=cpu_image, timeout=60)
@modal.fastapi_endpoint(method="POST", requires_proxy_auth=True)
def trigger(auto_promote: bool = False) -> dict:
    """HTTP trigger: spawns a pipeline run and returns immediately with the
    call id (pollable via Modal). Proxy-auth keeps this from being a public
    free-GPU button."""
    call = run_pipeline.spawn(auto_promote)
    return {"spawned": call.object_id}


@app.local_entrypoint()
def main(auto_promote: bool = False):
    """`modal run …/modal_app.py` — trigger a run from your terminal and wait."""
    print(json.dumps(run_pipeline.remote(auto_promote), indent=2))


@app.local_entrypoint()
def promote_run(run_id: str):
    """Manually promote (or roll back to) a specific run's artifact."""
    print(json.dumps(promote.remote(run_id, {"manual": True}), indent=2))
