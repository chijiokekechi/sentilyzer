"""Tests for the model store / hot-swap manager.

These cover the property that matters most for a live promotion: a request must
always see a fully-loaded model — the previous one until the new one is proven,
never a half-loaded or hash-mismatched one — and a bad promotion must degrade to
"keep serving the last good student" rather than take the worker down.
"""

from __future__ import annotations

import hashlib
from pathlib import Path

import pytest

from sentilyzer_ml.model_store import MODEL_FILENAME, ModelManager, Pointer


class FakeStore:
    """In-memory ArtifactStore: holds run_id -> model bytes and one pointer."""

    def __init__(self):
        self.models: dict[str, bytes] = {}
        self.pointer: Pointer | None = None
        self.forced_sha: str | None = None  # to simulate a corrupt artifact
        self.download_calls: list[str] = []

    def publish(self, run_id: str, content: bytes) -> None:
        """Write a run and point current.json at it (with the honest hash)."""
        self.models[run_id] = content
        sha = self.forced_sha or hashlib.sha256(content).hexdigest()
        self.pointer = Pointer(run_id=run_id, sha256=sha)

    def read_pointer(self) -> Pointer | None:
        return self.pointer

    def download(self, run_id: str, dest: Path) -> None:
        self.download_calls.append(run_id)
        dest.mkdir(parents=True, exist_ok=True)
        (dest / MODEL_FILENAME).write_bytes(self.models[run_id])


def counting_loader():
    """A loader that returns the run_id it loaded and records every call."""
    calls: list[str] = []

    def load(path: Path):
        calls.append(path.name)  # cache_dir/<run_id> -> run_id
        return path.name

    return load, calls


def make_manager(tmp_path: Path):
    store = FakeStore()
    load, calls = counting_loader()
    mgr = ModelManager(store, load, cache_dir=tmp_path / "cache")
    return mgr, store, calls


def test_no_pointer_serves_nothing(tmp_path):
    mgr, _store, calls = make_manager(tmp_path)
    assert mgr.refresh() is False
    assert mgr.current() is None
    assert mgr.run_id is None
    assert calls == []


def test_first_pointer_loads_and_serves(tmp_path):
    mgr, store, calls = make_manager(tmp_path)
    store.publish("train:2026-08-01", b"onnx-bytes-v1")

    assert mgr.refresh() is True
    assert mgr.current() == "train:2026-08-01"
    assert mgr.run_id == "train:2026-08-01"
    assert calls == ["train:2026-08-01"]


def test_unchanged_pointer_is_a_noop(tmp_path):
    mgr, store, calls = make_manager(tmp_path)
    store.publish("run-a", b"v1")
    assert mgr.refresh() is True

    # Polling again with the same pointer must not re-download or re-load.
    assert mgr.refresh() is False
    assert mgr.refresh() is False
    assert calls == ["run-a"]
    assert store.download_calls == ["run-a"]


def test_new_pointer_swaps(tmp_path):
    mgr, store, calls = make_manager(tmp_path)
    store.publish("run-a", b"v1")
    mgr.refresh()

    store.publish("run-b", b"v2-different")
    assert mgr.refresh() is True
    assert mgr.current() == "run-b"
    assert mgr.run_id == "run-b"
    assert calls == ["run-a", "run-b"]


def test_hash_mismatch_is_rejected_and_keeps_current(tmp_path):
    mgr, store, calls = make_manager(tmp_path)
    store.publish("good", b"v1")
    mgr.refresh()

    # A corrupt/truncated candidate: the pointer's sha256 doesn't match the bytes.
    store.forced_sha = "0" * 64
    store.publish("corrupt", b"v2")

    assert mgr.refresh() is False
    # The good model still serves; the corrupt run never became current.
    assert mgr.current() == "good"
    assert mgr.run_id == "good"
    assert "corrupt" not in calls  # loader is never reached past the hash check


def test_loader_failure_keeps_current(tmp_path):
    store = FakeStore()
    load_ok = {"fail": False}

    def flaky_loader(path: Path):
        if load_ok["fail"]:
            raise RuntimeError("cannot build session")
        return path.name

    mgr = ModelManager(store, flaky_loader, cache_dir=tmp_path / "cache")
    store.publish("run-a", b"v1")
    assert mgr.refresh() is True
    assert mgr.current() == "run-a"

    # A new run whose model loads badly must not unseat the working one.
    load_ok["fail"] = True
    store.publish("run-b", b"v2")
    assert mgr.refresh() is False
    assert mgr.current() == "run-a"
    assert mgr.run_id == "run-a"


def test_rollback_reloads_the_previous_run(tmp_path):
    mgr, store, calls = make_manager(tmp_path)
    store.publish("run-a", b"v1")
    mgr.refresh()
    store.publish("run-b", b"v2")
    mgr.refresh()
    assert mgr.current() == "run-b"

    # Rollback = writing the pointer back to a prior run_id. It differs from the
    # current run, so refresh treats it as a change and reloads it.
    store.publish("run-a", b"v1")
    assert mgr.refresh() is True
    assert mgr.current() == "run-a"


def test_pointer_read_failure_keeps_current(tmp_path):
    mgr, store, _calls = make_manager(tmp_path)
    store.publish("run-a", b"v1")
    mgr.refresh()

    def boom() -> Pointer:
        raise ConnectionError("store unreachable")

    store.read_pointer = boom  # type: ignore[method-assign]
    # A transient store outage must not drop the model we're already serving.
    assert mgr.refresh() is False
    assert mgr.current() == "run-a"


def test_pointer_from_json_parses_metrics():
    p = Pointer.from_json('{"run_id": "r1", "sha256": "abc", "metrics": {"agree": 0.94}}')
    assert p.run_id == "r1"
    assert p.sha256 == "abc"
    assert p.metrics == {"agree": 0.94}


def test_module_imports_without_boto3():
    # The module (and everything above) must import with no boto3 installed;
    # boto3 is only needed to construct the concrete S3 store.
    import importlib

    import sentilyzer_ml.model_store as ms

    importlib.reload(ms)
    assert hasattr(ms, "ModelManager")
    assert hasattr(ms, "S3ArtifactStore")


if __name__ == "__main__":
    raise SystemExit(pytest.main([__file__, "-q"]))
