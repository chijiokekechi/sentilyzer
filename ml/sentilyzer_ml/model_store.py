"""Delivery and hot-swap for the distilled student model.

The trainer (on Modal) writes a versioned artifact to an object store and flips
a ``students/current.json`` pointer; the serving worker polls that pointer and,
when it changes, downloads the new student, verifies its hash, loads it, and
swaps it in atomically — no redeploy, no dropped requests. Promotion *and*
rollback are both just a pointer write.

This module is deliberately model-agnostic: it orchestrates poll → verify →
swap and delegates "turn a directory of files into something that classifies"
to a ``loader`` callback, so it imports (and tests) without onnxruntime, torch,
or boto3.
"""

from __future__ import annotations

import hashlib
import json
import logging
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Protocol

logger = logging.getLogger(__name__)

# Layout in the object store: students/current.json (the pointer) and
# students/<run_id>/model.int8.onnx + tokenizer.json (the artifact).
DEFAULT_PREFIX = "students"
DEFAULT_POINTER_KEY = "students/current.json"
MODEL_FILENAME = "model.int8.onnx"


@dataclass(frozen=True)
class Pointer:
    """The `current.json` contents: which run is live, and its model hash."""

    run_id: str
    sha256: str
    metrics: dict | None = None

    @classmethod
    def from_json(cls, data: bytes | str) -> Pointer:
        obj = json.loads(data)
        return cls(run_id=obj["run_id"], sha256=obj["sha256"], metrics=obj.get("metrics"))


class ArtifactStore(Protocol):
    """Reads the pointer and downloads a run's files. See S3ArtifactStore."""

    def read_pointer(self) -> Pointer | None:
        ...

    def download(self, run_id: str, dest: Path) -> None:
        """Download run ``run_id``'s files into ``dest`` (created if needed)."""
        ...


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


class ModelManager:
    """Holds the live student and swaps it when the pointer moves.

    ``current()`` is a single attribute read and ``refresh()`` commits the new
    model with one atomic assignment, so an in-flight request always sees a
    fully-loaded model — the old one until the instant the new one is proven,
    never a half-loaded one.
    """

    def __init__(
        self,
        store: ArtifactStore,
        loader: Callable[[Path], Any],
        cache_dir: Path | str,
        *,
        model_filename: str = MODEL_FILENAME,
    ):
        self._store = store
        self._loader = loader
        self._cache_dir = Path(cache_dir)
        self._cache_dir.mkdir(parents=True, exist_ok=True)
        self._model_filename = model_filename
        self._lock = threading.Lock()  # serializes refreshes, not reads
        self._current: Any = None
        self._run_id: str | None = None

    @property
    def run_id(self) -> str | None:
        """The run currently served, or None before the first successful load."""
        return self._run_id

    def current(self) -> Any:
        # One attribute read. The assignment in refresh() is atomic under the
        # GIL, so this returns a consistent, fully-loaded backend — never mid-swap.
        return self._current

    def refresh(self) -> bool:
        """Poll the pointer once; download, verify, load, and swap if it moved.

        Returns True on a swap. Never raises on a bad candidate: a failed read,
        hash mismatch, or load error leaves the current model in place and logs,
        so a broken promotion degrades to "keep serving the last good student".
        """
        with self._lock:
            try:
                pointer = self._store.read_pointer()
            except Exception:
                logger.exception("model refresh: reading pointer failed")
                return False
            if pointer is None or pointer.run_id == self._run_id:
                return False
            try:
                backend = self._load(pointer)
            except Exception:
                logger.exception(
                    "model refresh: loading run %s failed; keeping %s",
                    pointer.run_id,
                    self._run_id,
                )
                return False
            self._current = backend  # atomic swap
            self._run_id = pointer.run_id
            logger.info("model refresh: now serving run %s", pointer.run_id)
            return True

    def _load(self, pointer: Pointer) -> Any:
        dest = self._cache_dir / pointer.run_id
        if not (dest / self._model_filename).exists():
            self._store.download(pointer.run_id, dest)
        model_path = dest / self._model_filename
        actual = sha256_file(model_path)
        if actual != pointer.sha256:
            # Refuse a model whose bytes don't match what was promoted — a
            # truncated download or a swapped file must not reach production.
            raise ValueError(
                f"sha256 mismatch for run {pointer.run_id}: "
                f"pointer={pointer.sha256} downloaded={actual}"
            )
        return self._loader(dest)

    def start_polling(self, interval_seconds: float) -> threading.Thread:
        """Refresh on a background daemon thread every interval_seconds."""

        def loop() -> None:
            while True:
                time.sleep(interval_seconds)
                try:
                    self.refresh()
                except Exception:
                    logger.exception("model poll loop: refresh error")

        thread = threading.Thread(target=loop, daemon=True, name="model-poller")
        thread.start()
        return thread


class S3ArtifactStore:
    """ArtifactStore over any S3-compatible object store (Cloudflare R2,
    Backblaze B2, AWS S3).

    boto3 is imported lazily so this module — and the ModelManager tests — need
    neither boto3 nor credentials. Credentials come from the standard
    AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY environment (R2 issues these).
    """

    def __init__(
        self,
        bucket: str,
        *,
        endpoint_url: str,
        prefix: str = DEFAULT_PREFIX,
        pointer_key: str = DEFAULT_POINTER_KEY,
    ):
        import boto3  # lazy: keep the module importable without boto3

        self._bucket = bucket
        self._prefix = prefix.rstrip("/")
        self._pointer_key = pointer_key
        self._s3 = boto3.client("s3", endpoint_url=endpoint_url)

    def read_pointer(self) -> Pointer | None:
        try:
            obj = self._s3.get_object(Bucket=self._bucket, Key=self._pointer_key)
        except Exception:
            # A missing pointer (nothing promoted yet) is not an error.
            return None
        return Pointer.from_json(obj["Body"].read())

    def download(self, run_id: str, dest: Path) -> None:
        dest.mkdir(parents=True, exist_ok=True)
        prefix = f"{self._prefix}/{run_id}/"
        paginator = self._s3.get_paginator("list_objects_v2")
        for page in paginator.paginate(Bucket=self._bucket, Prefix=prefix):
            for item in page.get("Contents", []):
                key = item["Key"]
                rel = key[len(prefix):]
                if not rel:
                    continue
                target = dest / rel
                target.parent.mkdir(parents=True, exist_ok=True)
                self._s3.download_file(self._bucket, key, str(target))
