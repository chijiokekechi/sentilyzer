"""Aspect-mining tests: extraction must be pure, bounded, and deterministic.

The ranking test asserts exact output against hand-computed RAKE scores, so
a scoring change cannot slip through as a mere reordering.
"""

from __future__ import annotations

from sentilyzer_ml.pipeline.aspects import STOPWORDS, mine_aspects

REVIEW = (
    "battery life is great, battery life is everything, "
    "but the screen scratches easily"
)


def test_stopwords_embedded():
    assert isinstance(STOPWORDS, frozenset)
    assert 100 <= len(STOPWORDS) <= 150
    assert "the" in STOPWORDS
    assert "battery" not in STOPWORDS


def test_deterministic_across_calls():
    assert mine_aspects(REVIEW) == mine_aspects(REVIEW)
    assert mine_aspects(REVIEW, 10) == mine_aspects(REVIEW, 10)


def test_empty_none_and_stopword_only_text():
    assert mine_aspects("") == []
    assert mine_aspects(None) == []
    assert mine_aspects("the and but you very of to") == []


def test_phrase_length_bounds():
    # Five content words with no boundary: no 1-3 word candidate exists.
    assert mine_aspects("quick brown foxes jump gracefully") == []
    # A word under three letters disqualifies its whole phrase.
    assert mine_aspects("big ox") == []
    # Digits bound phrases: two single-word candidates, never "iphone battery".
    assert mine_aspects("iphone 15 battery", 10) == ["iphone", "battery"]


def test_scoring_prefers_multiword_salient_phrases():
    # Candidates of REVIEW: [battery life] x2, [great], [everything],
    # [screen scratches easily].
    #   freq:   battery 2, life 2, great 1, everything 1,
    #           screen 1, scratches 1, easily 1
    #   degree: battery 4, life 4, great 1, everything 1,
    #           screen 3, scratches 3, easily 3
    #   scores: screen scratches easily 9.0, battery life 4.0,
    #           great 1.0, everything 1.0 (first occurrence breaks the tie)
    assert mine_aspects(REVIEW, 4) == [
        "screen scratches easily",
        "battery life",
        "great",
        "everything",
    ]


def test_max_aspects_honored():
    assert mine_aspects(REVIEW, 1) == ["screen scratches easily"]
    assert mine_aspects(REVIEW) == ["screen scratches easily", "battery life"]
    # Requests beyond the candidate count return every deduped candidate.
    assert len(mine_aspects(REVIEW, 50)) == 4
    assert mine_aspects(REVIEW, 0) == []


def test_unicode_and_emoji_do_not_crash():
    text = "café ☕ tastes amazing 🎉🎉 très bien"
    # Emoji bound phrases like punctuation; accented letters stay word chars.
    # tastes amazing 4.0 ties très bien 4.0, first occurrence wins; café 1.0.
    assert mine_aspects(text, 10) == ["tastes amazing", "très bien", "café"]
    assert mine_aspects(text, 10) == mine_aspects(text, 10)
