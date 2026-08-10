"""Train a Sentilyzer student on YOUR Modal account — clone and run.

    git clone <repo> && cd sentilyzer/ml
    pip install modal && modal setup          # your Modal account; we never see the key
    modal run --detach sentilyzer_ml/pipeline/modal_app.py \
        --from-month 2025-06 --output ./student

--detach matters: without it the app's lifetime is tied to your laptop's
heartbeats, so a Wi-Fi blip mid-training kills the run. Detached, the
pipeline finishes on Modal even if your connection drops — then
    modal run sentilyzer_ml/pipeline/modal_app.py::runs       # list runs
    modal run sentilyzer_ml/pipeline/modal_app.py::fetch --run-id … --output ./student
picks the artifact up afterwards. If a dropped client stranded the run lock,
    modal run sentilyzer_ml/pipeline/modal_app.py::unlock
clears it.

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
so a second run mostly pays for training (~$1). The run ends with sample
predictions from the fresh student, and serving it locally on demand is one
env var: SENTILYZER_ML_MODEL_DIR=./student (see README).

COST GUARDS (all enforced in code): the trainer's wall clock is capped
(MAX_TRAIN_SECONDS), the sequence cap is asserted on CPU before any GPU
spawns, GPU functions run with retries=0, a run lock stops double-fires,
and nothing has a schedule — an untriggered month costs $0.
"""

from __future__ import annotations

import json
import time
from pathlib import Path

import modal

APP_NAME = "sentilyzer-train"

# ── THE CONSTANTS THAT BOUND THE BILL ──────────────────────────────────────
MAX_TRAIN_SECONDS = 2_700  # 45 min on A10 ≈ $0.99/run, fixed forever
MAX_TRAIN_SEQUENCES = 1_840_000  # asserted on CPU before any GPU spawns
LOCK_STALE_SECONDS = 3 * 3600  # a crashed run frees the lock after this
HOLDOUT_ROWS = 5_000  # holdout ceiling; small runs scale it down (see holdout_rows)
MIN_LABELED_ROWS = 2_500  # floor so the gate's 500-row minimum is meetable
STUDENT_LAYERS = 6
TEACHER_MODEL = "cardiffnlp/twitter-roberta-base-sentiment-latest"
# ───────────────────────────────────────────────────────────────────────────

def holdout_rows(n_labeled: int) -> int:
    """Held-out slice size: 20% of the data up to HOLDOUT_ROWS.

    Train and evaluate MUST derive the same number from the same row count —
    they read the same deterministic join, so agreeing on this function is
    what keeps the gate scoring rows the trainer never saw.
    """
    if n_labeled < MIN_LABELED_ROWS:
        raise RuntimeError(
            f"only {n_labeled} labeled rows; need at least {MIN_LABELED_ROWS} "
            "— raise --limit or ingest more months"
        )
    return min(HOLDOUT_ROWS, n_labeled // 5)


app = modal.App(APP_NAME)
volume = modal.Volume.from_name("sentilyzer-train", create_if_missing=True)
run_lock = modal.Dict.from_name("sentilyzer-train-lock", create_if_missing=True)

DATA = "/data"

cpu_image = (
    modal.Image.debian_slim(python_version="3.12")
    .pip_install("duckdb>=1.0", "numpy>=1.26", "onnxruntime>=1.18", "tokenizers>=0.20", "fastapi")
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


@app.function(image=cpu_image, volumes={DATA: volume}, timeout=1800)
def prep(run_id: str) -> dict:
    from sentilyzer_ml.pipeline.prep import PrepConfig, build_training_set

    corpus = Path(DATA) / "corpus"
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
    from transformers import AutoTokenizer

    from sentilyzer_ml.inference import _load_fp32
    from sentilyzer_ml.pipeline.export import export_student
    from sentilyzer_ml.pipeline.kd import DistillConfig, build_student_from_teacher, distill

    run_dir = Path(DATA) / "runs" / run_id
    rows = duckdb.sql(f"""
        SELECT t.text, l.p_negative, l.p_neutral, l.p_positive
        FROM read_parquet('{run_dir / "train.parquet"}') t
        JOIN read_parquet('{Path(DATA) / "labels" / "labels.parquet"}') l
        USING (platform, doc_id)
    """).fetchall()
    # Prep's output order is deterministic; the tail is the held-out slice.
    train_rows = rows[: -holdout_rows(len(rows))]

    texts = [r[0] for r in train_rows]
    probs = torch.tensor([[r[1], r[2], r[3]] for r in train_rows], dtype=torch.float32)

    # THE COLLAPSE FIREWALL: the student is built from the FROZEN teacher,
    # fresh, every run. Never from a previous student. _load_fp32 pins float32
    # across the transformers dtype-kwarg rename.
    teacher = _load_fp32(TEACHER_MODEL)
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


SAMPLE_TEXTS = [
    "I absolutely love this — best decision I've made all year.",
    "This is broken again. Total waste of money, avoid it.",
    "The meeting was moved from Tuesday to Wednesday.",
    "Great hardware, but the software keeps crashing.",
]


@app.function(image=cpu_image, volumes={DATA: volume}, timeout=1800)
def evaluate(run_id: str) -> dict:
    import duckdb
    import numpy as np

    from sentilyzer_ml.onnx_backend import OnnxBackend
    from sentilyzer_ml.pipeline.evalgate import GateConfig, evaluate_gate

    run_dir = Path(DATA) / "runs" / run_id
    rows = duckdb.sql(f"""
        SELECT t.text, l.p_negative, l.p_neutral, l.p_positive
        FROM read_parquet('{run_dir / "train.parquet"}') t
        JOIN read_parquet('{Path(DATA) / "labels" / "labels.parquet"}') l
        USING (platform, doc_id)
    """).fetchall()
    heldout = rows[-holdout_rows(len(rows)):]

    # The same backend the worker serves with — evaluating exactly what ships.
    backend = OnnxBackend(run_dir / "model", intra_op_threads=2)
    student_rows = []
    for start in range(0, len(heldout), 64):
        preds = backend.classify([r[0] for r in heldout[start : start + 64]])
        student_rows.extend(p.probabilities for p in preds)
    student = np.array(student_rows, dtype=np.float32)
    teacher = np.array([[r[1], r[2], r[3]] for r in heldout], dtype=np.float32)

    gate = evaluate_gate(student, teacher, GateConfig())
    samples = [
        {"text": text, "label": p.label, "confidence": round(p.confidence, 3)}
        for text, p in zip(SAMPLE_TEXTS, backend.classify(SAMPLE_TEXTS), strict=True)
    ]
    out = {"passed": gate.passed, "reasons": gate.reasons, "samples": samples, **gate.metrics}
    (run_dir / "eval.json").write_text(json.dumps(out, indent=2))
    volume.commit()
    return out


@app.function(image=cpu_image, timeout=4 * 3600)
def run_pipeline() -> dict:
    """Orchestrates one run: lock → prep → label → train → evaluate.
    Returns everything it decided."""
    held = run_lock.get("run", None)
    now = time.time()
    if held and now - held.get("started_at", 0) < LOCK_STALE_SECONDS:
        age = int(now - held.get("started_at", 0))
        return {
            "skipped": f"run {held['run_id']} already in flight ({age}s ago)",
            "hint": (
                "if that run is dead (e.g. a disconnected client killed it), "
                "clear the lock with: modal run "
                "sentilyzer_ml/pipeline/modal_app.py::unlock"
            ),
        }

    run_id = time.strftime("train:%Y-%m-%d-%H%M%S", time.gmtime())
    run_lock["run"] = {"run_id": run_id, "started_at": now}
    try:
        summary: dict = {"run_id": run_id}
        summary["prep"] = prep.remote(run_id)
        summary["label"] = label.remote(run_id)
        summary["train"] = train.remote(run_id)
        summary["eval"] = evaluate.remote(run_id)
        return summary
    finally:
        run_lock.pop("run", None)


@app.function(image=cpu_image, volumes={DATA: volume}, timeout=300)
def list_runs() -> list[dict]:
    """All runs on the Volume, newest first — for picking up a detached run."""
    runs_dir = Path(DATA) / "runs"
    out = []
    if runs_dir.is_dir():
        for run in sorted(runs_dir.iterdir(), reverse=True):
            eval_path = run / "eval.json"
            entry = {
                "run_id": run.name,
                "has_model": (run / "model" / "model.int8.onnx").is_file(),
                "gate": None,
            }
            if eval_path.is_file():
                entry["gate"] = json.loads(eval_path.read_text()).get("passed")
            out.append(entry)
    return out


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
def trigger() -> dict:
    """HTTP trigger for deployed apps: spawns a run and returns the call id.
    Proxy-auth keeps this from being a public free-GPU button."""
    call = run_pipeline.spawn()
    return {"spawned": call.object_id}


def _download(run_id: str, output: str) -> None:
    out_dir = Path(output)
    out_dir.mkdir(parents=True, exist_ok=True)
    blobs = read_artifact.remote(run_id)
    if not blobs:
        raise SystemExit(f"run {run_id!r} has no artifacts on the Volume")
    for name, blob in blobs.items():
        (out_dir / name).write_bytes(blob)
        print(f"wrote {out_dir / name} ({len(blob):,} bytes)")


@app.local_entrypoint()
def unlock():
    """Clear a stranded run lock (a killed client can leave one behind)."""
    held = run_lock.pop("run", None)
    print(f"cleared: {held}" if held else "no lock was held")


@app.local_entrypoint()
def runs():
    """List runs on the Volume — for fetching after a detached run."""
    for entry in list_runs.remote():
        gate = {True: "gate PASSED", False: "gate FAILED", None: "no eval"}[entry["gate"]]
        model = "model ready" if entry["has_model"] else "no model"
        print(f"  {entry['run_id']}  {model:12s}  {gate}")


@app.local_entrypoint()
def fetch(run_id: str, output: str = "./student"):
    """Download a run's artifact — the second half of a detached run."""
    _download(run_id, output)


@app.local_entrypoint()
def main(
    from_month: str = "2025-01",
    to_month: str = "",
    limit: int = 0,
    corpus: str = "",
    output: str = "./student",
    skip_ingest: bool = False,
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

    summary = run_pipeline.remote()
    print(json.dumps(summary, indent=2))
    if "skipped" in summary:
        return

    _download(summary["run_id"], output)
    out_dir = Path(output)
    verdict = summary.get("eval", {})
    print(f"\ngate: {'PASSED' if verdict.get('passed') else 'FAILED'} "
          f"(agreement={verdict.get('agreement')})")
    for sample in verdict.get("samples", []):
        print(f"  {sample['label']:<8} ({sample['confidence']:.2f})  {sample['text']}")
    print(f"\nmodel: {out_dir / 'model.int8.onnx'}")
    print("serve it on demand: SENTILYZER_ML_MODEL_DIR=" + str(out_dir)
          + " python -m sentilyzer_ml.server")
