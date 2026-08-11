# Training-corpus eligibility policy

**Signed off 2026-08-10.** This is the single source of truth for which
sources may enter Sentilyzer's durable training corpus. It amends the
original HN+RSS-only policy in `continuous-training-plan.md`, following the
source survey in `free-sources-and-byok.md`. The `Policy()` method on
`api/internal/connectors.Connector` must mirror this table; a connector's
`Durable` flag flips only when this document changes first.

Serving (the interactive API, nothing persisted beyond the 10-minute
in-memory cache) is a separate, laxer question: a source can be
serving-enabled and corpus-blocked. This document governs **durable storage
and training** only. Engineering research, not legal advice; terms drift, so
re-verify a source's basis before relying on it in a dispute.

## Eligible

| Source | Basis | Conditions |
|---|---|---|
| **HackerNews** | Sanctioned public APIs (Algolia + Firebase), no training prohibition. Audited 2026-07-15. | None |
| **HN 20-year backfill** (HF `open-index/hacker-news`) | Same source, same audit. Eligibility rests on **our HN audit**, not the dataset's uploader-declared ODC-By label. | Treat as HN rows (platform `hackernews`). Verified updating at 5-minute cadence 2026-08-09. |
| **RSS** | Deliberately-syndicated feeds; lawful acquisition; labels don't substitute for the source. Audited 2026-07-15. | Summary-level content; composition cap per the training plan (AI-contamination risk in long-form). |
| **GDELT** | The only surveyed source whose license **affirmatively grants** commercial use and redistribution. | **GDELT's own fields only** (headlines, metadata, tone). Never fetch or store linked article text. That reintroduces each publisher's copyright. |
| **Bluesky** | Absence-of-prohibition under the same standard that admitted HN, plus: robots.txt on both API hosts affirmatively permits crawling, and Bluesky PBC states it does not forbid third-party training. Verified 2026-08-09. | **Binding, all four:** (1) bulk harvest via the sanctioned firehose/Jetstream, not endpoint scraping; (2) persist the **author DID** with every corpus row; (3) honor **delete events** from the firehose; (4) adopt Bluesky's user-intent/opt-out framework (proposal 0008) **the day it ships**. |

## Blocked (unchanged from the 2026-07-15 audit unless noted)

| Source | Reason |
|---|---|
| Reddit | Data API Terms 2.4: training rights withheld by rightsholders; uncurable. Kaggle Reddit mirrors inherit the same defect regardless of their license labels. |
| Twitter/X | Developer Agreement III.A(d)/(k). **Tweet datasets on Kaggle/HF are equally blocked**: an uploader's CC0 tag grants nothing X withholds (established 2026-08-09). |
| StockTwits | ToS bars extraction outside an approved API; no approved path exists. |
| YouTube | 30-day retention cap + derived-data ban. |
| Mastodon | Operators opted out via robots.txt (judgment call, not law). |
| Lemmy | **Serving-only for now** (2026-08-10 decision): defensible on some instances but per-instance terms need automated checks first. Revisit. |
| Guardian / NYT / news aggregators | Guardian prices sentiment analysis as a paid use; NYT bans use in ML systems outright (serving included); free aggregator tiers are delayed demos. |
| mock | Synthetic (would poison the corpus). |

## Standing revocation policy

**Purge and retrain immediately.** If an admitted source revokes (opt-out
framework ships, robots.txt or ToS turns prohibitive, a takedown arrives):

1. Delete that source's rows from the corpus at once.
2. Trigger an off-cycle retrain (~$1 per run on Modal) rather than waiting
   for the weekly cadence.
3. Promote the new student; the prior one is rotated out.

Because every student re-initializes from the frozen teacher, a purged
source exits the **live model** within one training cycle: days, not
model-generations. This is the property that makes admitting revocable
sources tolerable, and it is a reason the re-init-from-teacher rule must
never be "optimized away".

## Decision log

- **2026-07-15**: Initial policy: HN + RSS only, of seven connectors.
- **2026-08-10**: Added HN backfill, GDELT (own fields), Bluesky (four
  conditions). Lemmy held at serving-only. Revocation policy fixed at
  purge+retrain-now. Corpus breadth grows from ~10k docs/day to roughly two
  orders of magnitude more eligible volume; Bluesky is the only
  opinion-rich addition.
