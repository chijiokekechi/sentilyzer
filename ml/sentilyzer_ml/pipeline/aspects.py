"""Candidate aspect mining: deterministic RAKE-style keyphrase extraction.

Aspect-based distillation needs candidate aspects per document, and the
teacher must see identical candidates on every pass: nondeterminism here
would defeat incremental label reuse, because a re-run could no longer tell
"already labeled" from "candidates changed". So this stage is pure text
statistics computed within one document. No model, no network, no
dependencies beyond the standard library.

Scoring follows RAKE: candidate phrases are maximal runs of content words
between stopwords, punctuation, and digits; a phrase scores the sum over its
words of degree(word) / freq(word), both counts taken over this document's
candidates only. Co-occurring multiword phrases outscore frequent single
words, which is the bias aspect mining wants.
"""

from __future__ import annotations

import re

# Embedded so pipeline images need no corpus download and the split behavior
# can never drift under a dependency upgrade. 120 words: enough to break
# phrases at function words without eating domain vocabulary.
STOPWORDS: frozenset[str] = frozenset("""
    a about above after again against all also am an and any are as at be
    because been before being below between both but by can could did do does
    doing down during each few for from further had has have having he her
    here hers him his how i if in into is it its itself just me more most my
    no nor not now of off on once only or other our ours out over own same
    she should so some such than that the their theirs them then there these
    they this those through to too under until up very was we were what when
    where which while who whom why will with would you your yours
""".split())

# A run of letter-only words separated by whitespace. Digits, punctuation,
# and emoji all fall outside the run, so they bound phrases; underscore is
# excluded from \w's letters explicitly.
_WORD_RUN = re.compile(r"[^\W\d_]+(?:\s+[^\W\d_]+)*")

_MAX_PHRASE_WORDS = 3
_MIN_WORD_CHARS = 3


def _candidate_phrases(text: str) -> list[list[str]]:
    """Split lowercased text into candidate phrases, one list of words each.

    Stopwords break phrases; a phrase longer than three words, or containing
    any word under three letters, is rejected whole rather than re-split.
    """
    phrases: list[list[str]] = []
    for run in _WORD_RUN.finditer(text):
        current: list[str] = []
        for word in run.group().split():
            if word in STOPWORDS:
                if current:
                    phrases.append(current)
                    current = []
            else:
                current.append(word)
        if current:
            phrases.append(current)
    return [
        p for p in phrases
        if len(p) <= _MAX_PHRASE_WORDS and all(len(w) >= _MIN_WORD_CHARS for w in p)
    ]


def mine_aspects(text: str, max_aspects: int = 2) -> list[str]:
    """Return the top-scoring candidate aspects mined from ``text``.

    Pure and deterministic: the same text always yields the same list.
    Ranking is score descending, first occurrence ascending on ties; dedupe
    keeps the first occurrence. Empty or None text returns [].
    """
    if not text or max_aspects <= 0:
        return []
    phrases = _candidate_phrases(text.lower())
    if not phrases:
        return []

    # RAKE word stats over every candidate occurrence (pre-dedupe): freq is
    # occurrences of the word, degree is the summed length of its phrases.
    freq: dict[str, int] = {}
    degree: dict[str, int] = {}
    for phrase in phrases:
        for word in phrase:
            freq[word] = freq.get(word, 0) + 1
            degree[word] = degree.get(word, 0) + len(phrase)

    scored: dict[str, tuple[float, int]] = {}
    for order, phrase in enumerate(phrases):
        key = " ".join(phrase)
        if key not in scored:
            scored[key] = (sum(degree[w] / freq[w] for w in phrase), order)

    ranked = sorted(scored.items(), key=lambda item: (-item[1][0], item[1][1]))
    return [key for key, _ in ranked[:max_aspects]]
