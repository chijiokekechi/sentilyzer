"""ONNX export + INT8 quantization of the two-head student.

One graph, inputs (input_ids, attention_mask) with dynamic batch/sequence
axes, outputs (logits_doc, logits_aspect) — the shape the serving worker's
future OnnxBackend and the model_store hot-swap expect. Export parity is
asserted against the torch model before quantization, so a silently mangled
graph fails here rather than in production.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import torch


@dataclass
class ExportResult:
    fp32_path: str
    int8_path: str
    int8_sha256: str
    fp32_bytes: int
    int8_bytes: int
    max_parity_diff: float  # torch vs onnx fp32 logits, sample batch


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def export_student(
    student: torch.nn.Module,
    out_dir: str | Path,
    *,
    sample_batch: dict[str, torch.Tensor],
    opset: int = 17,
    parity_atol: float = 1e-3,
) -> ExportResult:
    """Export to ONNX, verify parity, quantize to INT8 dynamic.

    sample_batch supplies representative (input_ids, attention_mask) tensors;
    they also anchor the parity check.
    """
    import onnxruntime as ort
    from onnxruntime.quantization import QuantType, quantize_dynamic

    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    fp32_path = out / "model.onnx"
    int8_path = out / "model.int8.onnx"

    student.eval()
    ids = sample_batch["input_ids"]
    mask = sample_batch["attention_mask"]

    # The classic exporter: deterministic, no TorchDynamo warmup, and the
    # dynamic-axes contract is explicit. (torch>=2.9 defaults to the dynamo
    # exporter; be version-tolerant.)
    export_kwargs = dict(
        input_names=["input_ids", "attention_mask"],
        output_names=["logits_doc", "logits_aspect"],
        dynamic_axes={
            "input_ids": {0: "batch", 1: "seq"},
            "attention_mask": {0: "batch", 1: "seq"},
            "logits_doc": {0: "batch"},
            "logits_aspect": {0: "batch"},
        },
        opset_version=opset,
    )
    with torch.no_grad():
        try:
            torch.onnx.export(student, (ids, mask), str(fp32_path), dynamo=False, **export_kwargs)
        except TypeError:  # older torch without the dynamo kwarg
            torch.onnx.export(student, (ids, mask), str(fp32_path), **export_kwargs)

    # Parity: the exported graph must agree with torch before we quantize.
    with torch.no_grad():
        torch_doc, torch_aspect = student(ids, mask)
    sess = ort.InferenceSession(str(fp32_path), providers=["CPUExecutionProvider"])
    onnx_doc, onnx_aspect = sess.run(
        None,
        {"input_ids": ids.numpy(), "attention_mask": mask.numpy()},
    )
    diff = max(
        float(np.abs(torch_doc.numpy() - onnx_doc).max()),
        float(np.abs(torch_aspect.numpy() - onnx_aspect).max()),
    )
    if diff > parity_atol:
        raise RuntimeError(
            f"onnx export parity failed: max |torch - onnx| = {diff:.2e} > {parity_atol}"
        )

    quantize_dynamic(str(fp32_path), str(int8_path), weight_type=QuantType.QInt8)
    # The quantized graph must still load and run.
    sess8 = ort.InferenceSession(str(int8_path), providers=["CPUExecutionProvider"])
    sess8.run(None, {"input_ids": ids.numpy(), "attention_mask": mask.numpy()})

    return ExportResult(
        fp32_path=str(fp32_path),
        int8_path=str(int8_path),
        int8_sha256=sha256_file(int8_path),
        fp32_bytes=fp32_path.stat().st_size,
        int8_bytes=int8_path.stat().st_size,
        max_parity_diff=diff,
    )
