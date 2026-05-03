"""Environment-driven configuration for the ML worker."""

from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Config:
    listen_addr: str
    general_model: str
    aspect_model: str
    device: str
    max_batch_size: int
    max_text_chars: int
    use_stub: bool

    @classmethod
    def from_env(cls) -> "Config":
        return cls(
            listen_addr=os.getenv("SENTILYZER_ML_LISTEN", "[::]:50051"),
            general_model=os.getenv(
                "SENTILYZER_ML_GENERAL_MODEL",
                "cardiffnlp/twitter-roberta-base-sentiment-latest",
            ),
            aspect_model=os.getenv(
                "SENTILYZER_ML_ASPECT_MODEL",
                "yangheng/deberta-v3-base-absa-v1.1",
            ),
            device=os.getenv("SENTILYZER_ML_DEVICE", "cpu"),
            max_batch_size=int(os.getenv("SENTILYZER_ML_MAX_BATCH", "32")),
            max_text_chars=int(os.getenv("SENTILYZER_ML_MAX_CHARS", "2000")),
            use_stub=os.getenv("SENTILYZER_ML_USE_STUB", "0") in {"1", "true", "TRUE"},
        )
