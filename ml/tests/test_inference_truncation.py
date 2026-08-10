"""Regression test for the first real-GPU failure.

The cardiffnlp tokenizer ships without model_max_length, so transformers
substitutes a ~1e30 sentinel and `truncation=True` truncates NOTHING — the
first batch containing a >512-token document overflowed RoBERTa's position
table ("expanded size (593) must match existing size (514)"). This test
reproduces that exact condition with a tiny model: a fast tokenizer with the
sentinel max, a model with a small position table, and a document far longer
than it — and asserts classify() now survives it.
"""

from __future__ import annotations

import pytest

torch = pytest.importorskip("torch")
pytest.importorskip("transformers")

from transformers import (  # noqa: E402
    PreTrainedTokenizerFast,
    RobertaConfig,
    RobertaForSequenceClassification,
)

from sentilyzer_ml.inference import TransformerBackend, resolve_max_length  # noqa: E402


def test_resolve_max_length():
    # The failing production case: sentinel tokenizer max, RoBERTa 514-slot
    # position table -> must land on 512 (514 - 2 pad offset).
    assert resolve_max_length(int(1e30), 514) == 512
    # A sane tokenizer max wins when it is the tightest bound.
    assert resolve_max_length(128, 514) == 128
    # The model's position table wins when smaller than the ceiling.
    assert resolve_max_length(None, 66) == 64
    # Nothing known: the ceiling holds.
    assert resolve_max_length(None, None) == 512
    assert resolve_max_length(0, 0) == 512


def tiny_pair():
    """A tokenizer with NO model_max_length (the sentinel condition) and a
    model whose position table is much smaller than long inputs."""
    from tokenizers import Tokenizer, models, pre_tokenizers

    vocab = {"<unk>": 0, "<pad>": 1, "word": 2, "good": 3, "bad": 4}
    tok_obj = Tokenizer(models.WordLevel(vocab, unk_token="<unk>"))
    tok_obj.pre_tokenizer = pre_tokenizers.Whitespace()
    tok = PreTrainedTokenizerFast(
        tokenizer_object=tok_obj, pad_token="<pad>", unk_token="<unk>"
    )
    # Precondition of the bug: transformers substitutes a huge sentinel.
    assert tok.model_max_length > 1_000_000

    config = RobertaConfig(
        vocab_size=10,
        hidden_size=16,
        num_hidden_layers=1,
        num_attention_heads=2,
        intermediate_size=32,
        max_position_embeddings=34,  # tiny position table, like 514 vs 593
        num_labels=3,
        pad_token_id=1,
        id2label={0: "negative", 1: "neutral", 2: "positive"},
        label2id={"negative": 0, "neutral": 1, "positive": 2},
    )
    torch.manual_seed(0)
    return tok, RobertaForSequenceClassification(config)


def test_long_document_no_longer_overflows_position_table():
    tok, mdl = tiny_pair()
    backend = TransformerBackend("unused", "unused")
    backend._general = backend._prepare(tok, mdl)

    # Without the fix this is the production crash: 200 tokens vs a 34-slot
    # position table ("expanded size ... must match existing size").
    long_doc = "word " * 200
    preds = backend.classify([long_doc, "good", long_doc + " bad"])
    assert len(preds) == 3
    for p in preds:
        assert sum(p.probabilities) == pytest.approx(1.0, abs=1e-5)

    # And the resolved bound is what _prepare computed: pos_table - 2.
    assert backend._general[3] == 32
