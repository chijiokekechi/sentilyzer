"""Sentiment + aspect classifiers.

Two models are loaded on first use:

  * ``general`` — ``cardiffnlp/twitter-roberta-base-sentiment-latest``,
    a RoBERTa-base fine-tuned on ~124M tweets. Output order is
    ``[negative, neutral, positive]``.
  * ``aspect``  — ``yangheng/deberta-v3-base-absa-v1.1``, fine-tuned on
    SemEval ABSA datasets. Inputs are ``(text, aspect)`` text pairs;
    output order is ``[Negative, Neutral, Positive]``.

A heuristic stub backend is provided for tests and offline boots so the
gateway can still be exercised end-to-end without downloading weights.
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass
from typing import Iterable

import numpy as np

LABELS = ("negative", "neutral", "positive")
LABEL_INDEX = {name: idx for idx, name in enumerate(LABELS)}

logger = logging.getLogger(__name__)


@dataclass
class Prediction:
    """Probability distribution over the three sentiment classes."""

    probabilities: list[float]  # ordered as LABELS

    @property
    def label(self) -> str:
        # Derive label from argmax. Any tie collapses to neutral, since a
        # tied positive/negative split is the textbook "mixed sentiment".
        max_p = max(self.probabilities)
        winners = [i for i, p in enumerate(self.probabilities) if p == max_p]
        if len(winners) != 1:
            return "neutral"
        return LABELS[winners[0]]

    @property
    def confidence(self) -> float:
        return float(max(self.probabilities))

    @property
    def polarity(self) -> float:
        # Continuous score in [-1, 1].
        return float(self.probabilities[2] - self.probabilities[0])

    def as_dict(self) -> dict[str, float]:
        return {LABELS[i]: float(p) for i, p in enumerate(self.probabilities)}


# --- backends ----------------------------------------------------------------


class HeuristicBackend:
    """Tiny deterministic sentiment scorer.

    Used when ``SENTILYZER_ML_USE_STUB=1`` (CI, offline development).
    Not a real model — just a lexicon-based softmax that lets the rest of
    the system be exercised end-to-end without GPU/network dependencies.
    """

    POSITIVE = {
        "good", "great", "love", "loved", "loving", "amazing", "awesome",
        "excellent", "fantastic", "wonderful", "best", "perfect", "happy",
        "win", "winning", "bullish", "up", "gains", "buy", "rally", "soaring",
    }
    NEGATIVE = {
        "bad", "worst", "hate", "hated", "terrible", "awful", "horrible",
        "disappointed", "disappointing", "broken", "issue", "issues", "bug",
        "buggy", "scam", "fraud", "loss", "losses", "bearish", "down", "sell",
        "crash", "crashing", "dump",
    }

    _TOKEN_RE = re.compile(r"[A-Za-z][A-Za-z']+")

    def classify(self, texts: Iterable[str]) -> list[Prediction]:
        return [self._score(t) for t in texts]

    def classify_aspects(
        self, items: Iterable[tuple[str, list[str]]]
    ) -> list[list[tuple[str, Prediction]]]:
        out = []
        for text, aspects in items:
            scores = []
            for aspect in aspects:
                # focus on sentences mentioning the aspect; fall back to whole text
                window = self._extract_window(text, aspect) or text
                scores.append((aspect, self._score(window)))
            out.append(scores)
        return out

    def _score(self, text: str) -> Prediction:
        tokens = [t.lower() for t in self._TOKEN_RE.findall(text or "")]
        pos = sum(1 for t in tokens if t in self.POSITIVE)
        neg = sum(1 for t in tokens if t in self.NEGATIVE)
        # Lower neutral baseline so a single matched word flips the label.
        logits = np.array([neg, 0.5, pos], dtype=np.float64)
        exp = np.exp(logits - logits.max())
        probs = exp / exp.sum()
        return Prediction(probabilities=probs.tolist())

    @staticmethod
    def _extract_window(text: str, aspect: str, radius: int = 40) -> str:
        if not text or not aspect:
            return ""
        lower = text.lower()
        idx = lower.find(aspect.lower())
        if idx == -1:
            return ""
        start = max(0, idx - radius)
        end = min(len(text), idx + len(aspect) + radius)
        return text[start:end]


class TransformerBackend:
    """HuggingFace-backed implementation. Models load on first call."""

    def __init__(self, general_model: str, aspect_model: str, device: str = "cpu"):
        self.general_model = general_model
        self.aspect_model = aspect_model
        self.device = device
        self._general = None
        self._aspect = None

    # ----- lazy loaders -----

    def _load_general(self):
        if self._general is not None:
            return self._general
        import torch
        from transformers import AutoModelForSequenceClassification, AutoTokenizer

        logger.info("loading general model %s on %s", self.general_model, self.device)
        tok = AutoTokenizer.from_pretrained(self.general_model)
        # Pin float32 explicitly. transformers 5's from_pretrained defaults to
        # dtype="auto", which can silently load a model's fp16/bf16 weights —
        # slower and less accurate on CPU, and it would corrupt the soft labels
        # a distilled student learns from.
        mdl = AutoModelForSequenceClassification.from_pretrained(
            self.general_model, torch_dtype=torch.float32
        )
        mdl.eval().to(self.device)
        # Map the model's id2label onto our canonical (negative, neutral, positive)
        # ordering. cardiffnlp uses LABEL_0/1/2 = negative/neutral/positive.
        id2label = {int(k): v.lower() for k, v in mdl.config.id2label.items()}
        order = self._order_for(id2label)
        self._general = (tok, mdl, order, torch)
        return self._general

    def _load_aspect(self):
        if self._aspect is not None:
            return self._aspect
        import torch
        from transformers import AutoModelForSequenceClassification, AutoTokenizer

        logger.info("loading aspect model %s on %s", self.aspect_model, self.device)
        tok = AutoTokenizer.from_pretrained(self.aspect_model)
        # Pin float32 — see _load_general for why dtype="auto" is unsafe here.
        mdl = AutoModelForSequenceClassification.from_pretrained(
            self.aspect_model, torch_dtype=torch.float32
        )
        mdl.eval().to(self.device)
        id2label = {int(k): v.lower() for k, v in mdl.config.id2label.items()}
        order = self._order_for(id2label)
        self._aspect = (tok, mdl, order, torch)
        return self._aspect

    @staticmethod
    def _order_for(id2label: dict[int, str]) -> list[int]:
        """Return indices that reorder model logits to our (neg, neu, pos) layout."""
        synonyms = {
            "negative": "negative", "neg": "negative", "label_0": "negative",
            "neutral": "neutral", "neu": "neutral", "label_1": "neutral",
            "positive": "positive", "pos": "positive", "label_2": "positive",
        }
        canonical_to_idx = {}
        for idx, raw in id2label.items():
            key = synonyms.get(raw, raw)
            canonical_to_idx[key] = idx
        if set(canonical_to_idx) != set(LABELS):
            raise ValueError(
                f"unexpected label set from model: {id2label!r}"
            )
        return [canonical_to_idx[name] for name in LABELS]

    # ----- inference -----

    def classify(self, texts: Iterable[str]) -> list[Prediction]:
        texts = list(texts)
        if not texts:
            return []
        tok, mdl, order, torch = self._load_general()
        with torch.no_grad():
            inputs = tok(texts, padding=True, truncation=True, return_tensors="pt").to(
                self.device
            )
            logits = mdl(**inputs).logits
            probs = torch.softmax(logits, dim=-1).cpu().numpy()
        return [Prediction(probabilities=row[order].tolist()) for row in probs]

    def classify_aspects(
        self, items: Iterable[tuple[str, list[str]]]
    ) -> list[list[tuple[str, Prediction]]]:
        items = list(items)
        flat_text: list[str] = []
        flat_aspect: list[str] = []
        spans: list[tuple[int, int]] = []  # (start, end) into flat lists per item
        for text, aspects in items:
            start = len(flat_text)
            for asp in aspects:
                flat_text.append(text or "")
                flat_aspect.append(asp)
            spans.append((start, len(flat_text)))

        if not flat_text:
            return [[] for _ in items]

        tok, mdl, order, torch = self._load_aspect()
        with torch.no_grad():
            inputs = tok(
                flat_text,
                flat_aspect,
                padding=True,
                truncation=True,
                return_tensors="pt",
            ).to(self.device)
            logits = mdl(**inputs).logits
            probs = torch.softmax(logits, dim=-1).cpu().numpy()

        out: list[list[tuple[str, Prediction]]] = []
        for (start, end), (_text, aspects) in zip(spans, items, strict=True):
            row = []
            for offset, asp in enumerate(aspects):
                pred = Prediction(probabilities=probs[start + offset, order].tolist())
                row.append((asp, pred))
            out.append(row)
        return out


def make_backend(*, use_stub: bool, general_model: str, aspect_model: str, device: str):
    if use_stub:
        return HeuristicBackend()
    return TransformerBackend(general_model, aspect_model, device)
