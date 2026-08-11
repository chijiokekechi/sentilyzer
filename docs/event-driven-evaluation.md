<!--
Generated 2026-07-15 by a multi-agent research + design pass (28 agents, 504 tool calls),
evaluating whether Sentilyzer should become event-driven / dormant-until-requested.
Companion to continuous-training-plan.md, which it corrects in several places (see §9).

Latency and cold-start figures here were MEASURED, not estimated, but measured from a
laptop against live third-party services on one day. Re-measure before trusting the tail.
Claims tagged [EST] are not measured. The load-bearing ones are called out in §9.

Independently re-verified before publication:
  - the batch-32 blocker (server.py:52 / config.py:32 / client.go:75 / service.go:146)
  - the dead Reuters feed (rss.go:35 -> HTTP 401)
  - HN Algolia repeat-query overlap (50/50 identical objectIDs on 'tesla' and 'openai')
Note: the per-topic FRESHNESS figures did not fully reproduce -- 'openai' returned 35/50
docs under 30 days old on re-measurement, vs 2/50 as reported here. Freshness is
topic-dependent. The r~0 repeat-contribution finding, which is what the argument rests on,
reproduced exactly.
-->

# Event-Driven Sentilyzer: Feasibility, Cost, and What It Breaks

**Date:** 2026-07-15 · **Repo:** `/Users/chijiokeekechi/workspace/github.com/chijiokekechi/sentilyzer` · **Baseline:** `docs/continuous-training-plan.md` ($19.30/mo)

---

## 1. Verdict

**Measured p50 for an uncached `AnalyzeTopic` is ~2.9s warm and ~7-15s dormant; measured p99 is ~10.4s warm, and 7.7s of that p99 is one dead third-party RSS feed, not architecture.** Synchronous HTTP is the right shape and stays the right shape, but *nothing you're proposing runs today*: `ml/sentilyzer_ml/server.py:52` aborts `RESOURCE_EXHAUSTED` above 32 documents and `api/internal/inference/client.go:75` sends every text in one call, so the **default** request (3 credential-free connectors × `LimitPerPlatform=20` = 60 docs, `service.go:146`) already fails on a clean checkout. Serving is **already** event-driven: `service.AnalyzeTopic` (`service.go:141`) fans out on demand, dedupes, batches one inference call, and aggregates, with nothing precomputed, so "go event-driven" is not a serving change; it is a proposal to delete the flywheel and the time series, which is **directly contrary to your stated priority that frequent training is what makes this worth using**. The dormancy prize is **$9.52/mo at 100 req/day** and it buys a **measured 3.9-12.4s Modal cold start on 70.7% of requests** (that is your *median*, not your tail), plus a corpus that grows **355 docs/day instead of 10,000** and RSS that is gone permanently.

**Ship: YAML (responses only), three latency fixes, one RSS cron fix. Keep everything warm, keep every cron, keep daily training. $19.40/mo, essentially unchanged.**

---

## 2. The Reframe: most of your ask is already built

You wrote: *"the system remains dormant, until someone sends a request. Then the whole process of gathering data and analyzing for sentiments kicks off then."*

That is a description of `api/internal/service/service.go:141` as it exists today:

```
AnalyzeTopic(ctx, req)
  ├─ cache lookup            service.go:149  (MEASURED hit: 4-13µs)
  ├─ selectPlatforms         service.go:256  (filters on Enabled())
  ├─ fanout → goroutines     service.go:290  (parallel; wall-clock = slowest, not sum)
  ├─ dedupe (platform|docID) service.go:353
  ├─ ONE batched Classify    service.go:172-180 → inference/client.go:75
  ├─ aggregate by platform/aspect
  └─ return
```

Nothing is precomputed. No cron feeds a request. The machine does zero work until a request arrives. **You already have what you asked for.**

**What the cron actually buys** (and it is not serving):

| The cron produces | Consumed by | Would dormancy break it? |
|---|---|---|
| Training corpus (HN firehose, ~12,628 raw docs/24h **measured live 2026-07-15**) | Teacher labeling → student distillation | **YES, catastrophically** (§4) |
| RSS corpus (2nd source, ToS-cleared) | Same | **YES, irreversibly** (§4) |
| `topic_daily` time series | Phase 7 history API | Partially (§5) |

So the real question is not "sync or async" and not "warm or cold." It is: **do the flywheel and the history survive?** That deserves its own decision, decided on its own merits, not imported as a side effect of a latency intuition.

---

## 3. The Latency Budget

### 3.1 The blocker: fix this before arguing about anything else

`ml/sentilyzer_ml/config.py:32` → `max_batch_size=int(os.getenv("SENTILYZER_ML_MAX_BATCH", "32"))`
`ml/sentilyzer_ml/server.py:52-55` → **aborts** `RESOURCE_EXHAUSTED`. It does not chunk.
`api/internal/inference/client.go:75` → `c.stub.Classify(ctx, &pb.ClassifyRequest{Texts: texts})`: **all texts, one call.** Nothing chunks anywhere.

The reachable number is worse than "limit=50 × 7 platforms." Only three connectors return `Enabled() → true` unconditionally (`hackernews.go:33`, `rss.go:50`, `stocktwits.go:32`); Reddit/Twitter/Mastodon/YouTube are credential-gated. So on a **clean checkout with no credentials**, at the **default** `LimitPerPlatform = 20` (`service.go:146`):

> **3 × 20 = 60 docs > 32 → `RESOURCE_EXHAUSTED` → `service.go:180` returns a hard error with no partial result.**

`main` is broken at defaults. The README example uses `limit=5` (15 docs), which is why this was never hit. `ClassifyAspects` has the same wall at `max_batch_size * 4 = 128` on **cumulative** aspects (`server.py:69`). **Every latency number below is hypothetical until `Classify` chunks.**

### 3.2 The measured budget

| Stage | p50 | p99 | Provenance |
|---|---|---|---|
| Cache hit (whole request) | **4-13µs** | - | MEASURED, repo's own service path |
| Connector fanout (parallel) | **0.82s** | **7.67s** | MEASURED, 6 trials: 0.336/0.388/0.505/0.818/2.279/**7.665**s |
| - HN Algolia alone | 0.374s | 1.068s | MEASURED, n=40 |
| - working RSS feeds | 0.10-0.32s | - | MEASURED |
| - **hnrss.org (the tail)** | 0.25s warm | **19.39s cold TTFB** | MEASURED: owns the entire p99 |
| Modal student, **warm** | ~0.50s | ~0.50s | MEASURED (499-524ms; session 0.35s, infer 17ms) |
| Modal student, **cold** @ `cpu=0.125` | **3.9-12.4s** | 12.4s | MEASURED, real ONNX. Breakdown of the 12.4s: import ORT 1.548s, `InferenceSession` init **6.214s**, actual inference **0.017s** |
| Inference, 200 docs, warm | ~1.9-3.0s | ~3.0s | [EST]: 214 docs/s on Apple M4 Pro arm64 @ `intra_op=2`, derated **1.5-2.5× for Modal x86 (a guess, not a measurement)** |
| Fly wake from stopped | ~2s | 2-30s | Fly docs ("~2+ seconds"); community reports ~30s outliers. **Not measured here (flyctl not installed)** |

**Totals:**

| Configuration | p50 | p99 |
|---|---|---|
| **Warm (baseline as written)** | **~2.9s** | **~10.4s** |
| Warm, after the 3 fixes | ~2.2-2.9s | **~6s** |
| **Dormant (Modal `min_containers=0` + Fly stopped)** | **~7-15s** | ~14.5-20s |

### 3.3 The conclusion, and the sharp line

**Interactive `AnalyzeTopic` → SYNCHRONOUS.** AIP-151 puts the long-running-operation bar at *~10 seconds* and says "Avoid for trivial operations." p50 is ~25× below it. A 202+poll default would turn a **measured 4µs cache hit** into a 202 + `Location` + a second round trip.

**History backfill → ASYNC, and inference is why, not the connectors.** A 90-day HN backfill *harvests* in 2-5s (8-way parallel slicing, MEASURED: duckdb 623 docs/1 req/1.16s; openai 10,031/11 req/13.5s sequential → 2.06s parallel; rust 26,669/27 req/29.6s → 5.18s parallel). But **scoring** it takes **0.4-34.6 minutes** [EST, from the 12.8-25 seq/s band]. A 10s sync budget buys 250 scored docs = 2.8/day across 90 days. Worthless. That path needs `202` + a job id, and it is the only path that does.

### 3.4 Three things about the p99 you should internalize

1. **Your p99 is a *failure*, not a slow response.** `client.go:73` sets a 30s deadline on `Classify`. `inference.py:194` uses `tok(texts, padding=True, truncation=True)`: **pads to the longest doc in the batch**, so one 2000-char doc makes a 32-doc chunk **36× slower (96ms → 3499ms, MEASURED)**. Projected 350 docs ≈ 38s → `DEADLINE_EXCEEDED` → `service.go:180` → a 30s hang then a 500. That is strictly worse than a slow 200 and it is the only honest argument for async, and it's a bug, not physics.

2. **Architecture does not own your p99. `hnrss.org` does.** 7.7s of a 10.4s p99, in 2 of 6 real trials, while HN Algolia stayed at 0.34-0.57s across 11 calls. `service.go:290-311` launches goroutines and `wg.Wait()`s on **all** of them with **no per-connector deadline**; the only bound is `factory.go:15`'s **one shared** `&http.Client{Timeout: 12 * time.Second}` (the per-connector `10*time.Second` defaults in each file are dead code: `BuildRegistry` always injects the shared client). The uncontrolled-feed tail *exceeds the entire cold-start budget*. **The sync shape is already wrong today, before any dormancy decision.**

3. **Dormancy does not force async. It forces *slow*.** A 12.4s cold start inside a 15s deadline is a slow 200, not a 202. Do not build a job API to route around a config choice.

---

## 4. What Breaks: Do Not Soften This

### 4.1 The flywheel: a hard ceiling, not slow growth

The mechanism is in one line. `api/internal/connectors/hackernews.go:47-49`:

```go
func (h *HackerNews) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if q.Topic == "" {
		return nil, nil
	}
```

The connector is **relevance-ranked search**, not a firehose. MEASURED, same query twice, 1s apart:

```
tesla    nbHits=143964   overlap=50/50 IDENTICAL objectIDs   ≤30d: 0   median age 2427.9d
openai   nbHits=241279   overlap=50/50 IDENTICAL objectIDs   ≤30d: 2   median age  658.3d
rust     nbHits=625497   overlap=50/50 IDENTICAL objectIDs   ≤30d: 3   median age 1766.4d
```

**Zero of tesla's top-50 are newer than 30 days. Median age 6.6 years.** Repeat queries return the *same documents forever*. So the corpus is not `rate × time`. It is `50 × (distinct topics ever queried)`, an asymptote. 100 users asking about Tesla forever contribute 50 documents, once, then nothing.

| req/day | gross docs/day | **NET new/day** | dedupe loss | days → 200k | days → 1M |
|---|---|---|---|---|---|
| 100 | 5,000 | **355** | **92.9%** | **563** | 32,041 (**88 yrs**) |
| 1,000 | 50,000 | 1,413 | 97.2% | 142 | 3,225 |
| 10,000 | 500,000 | 5,627 | 98.9% | 36 | 317 |
| **CRON** | **10,000** | **10,000** | **0.0%** | **20** | **100** |

*(Heaps' law `D = 2.524·N^0.6` is a model, marked [EST]. The `r ≈ 0` repeat contribution is MEASURED and it is what actually kills this: the ceiling exists regardless of the Heaps constants.)*

**The loss gets *worse* with traffic** (92.9% → 99.4% at 50k/day), because more traffic means more repeat queries against identical ranked sets.

Three more things dormancy costs that slowness doesn't capture:

- **The corpus becomes a sample of user curiosity.** Non-stationary and traffic-coupled: a quiet week starves it, a viral week floods it with one topic. The plan's `CORPUS_RESERVOIR = 20_000` stratified reservoir (plan:322) exists specifically to prevent rare-class forgetting: **a reservoir can only preserve what once entered.**
- **RSS goes to ~zero, permanently.** `hnrss.org/frontpage` holds **9h48m** of history (MEASURED). Wayback recovers ~13% of days at ~4% of content. Source diversity 2 → 1.
- **Chicken-and-egg.** You need ~10k req/day to feed the flywheel and a good model to earn 10k req/day.

**And this is *your* stated priority.** You said frequent training is what makes the system worth using. Event-driven doesn't slow training. It starves and biases the thing training consumes. Retraining daily on a corpus growing at 355 docs/day is retraining on noise.

### 4.2 The `rss.go` finding that kills the "keep one small RSS cron" escape

The obvious hedge is "keep one poll-only RSS cron, everything else event-driven." **That cron returns zero documents as usually specified**, because `api/internal/connectors/rss.go:53-55` carries the *identical* gate:

```go
func (r *RSS) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if q.Topic == "" {
		return nil, nil
	}
```

and `rss.go:85` does `strings.Contains(strings.ToLower(combined), needle)`: RSS is a topic **filter** over feed items, not a dump, and `rss.go:56-58` defaults `limit = 30` with truncation at `:127-129`. **The starvation diagnosis was never HN-specific.** Both connectors need a firehose branch (`Query.Window`, ~40 lines for HN + ~15 for RSS). Note `SinceSeconds` is `int64` (`connector.go:24`), not `int`. Any `Query` rewrite must preserve that or `reddit.go:174` breaks.

### 4.3 Your default feed set is 25% dead right now

`rss.go:35` hardcodes `https://www.reuters.com/markets/rss` → **reproducible HTTP 401** (DataDome CAPTCHA). `rss.go` swallows per-feed errors into `firstErr` and returns other feeds' results, so **25% of the default corpus is silently missing in `main` today** and has been. Dormancy makes this strictly worse: no traffic → no polling → no error → no signal a feed died.

### 4.4 The daily harvest already loses RSS: a bug in the baseline

The plan's harvest fires once daily. `hnrss.org` holds **9h48m**. A 24h gap silently drops RSS. **This is a pre-existing bug in `docs/continuous-training-plan.md`, independent of your question**, and it costs $0.10/mo to fix (§7).

---

## 5. Lazy Pull-Through History: What It Can Be

**It works, for a reason nobody expects, and only for HackerNews.**

### 5.1 Backfill is cheap and fast (MEASURED)

`GET /api/v1/search_by_date?query=X&tags=(story,comment)&numericFilters=created_at_i>LO,created_at_i<HI`: arbitrary windows, HTTP 200, **~39s index lag**. Real cursor-paginated 90-day backfills:

| topic | docs | requests | sequential | parallel-8 |
|---|---|---|---|---|
| duckdb | 623 | 1 | 0.79s | - |
| kubernetes | 1,123 | 2 | 1.37s | - |
| tesla | 2,700 | 3 | 2.35s | - |
| openai | 10,032 | 11 | 9.35s | **2.06s** |
| rust | 26,547 | 27 | 28.97s | **5.18s** |

**Median topic = 3 requests, 1-3 seconds.** Not 2,160 hourly slices. Cost is noise: **$0.0004-$0.032/topic**; 1,000 new topics/mo ≈ $0.92-$1.80. **Money is not the constraint. Latency is**: scoring, not harvesting.

### 5.2 The three traps that will silently corrupt it

- **`nbPages` LIES.** `hitsPerPage=1000` on a 10,032-hit query returns **`nbPages=1`**. A `while page < nbPages` loop terminates after one page, **reports success, and drops 90%.** Drive the loop off `len(hits) == hitsPerPage` and a time cursor. `nbHits` is also non-exhaustive (`exhaustiveNbHits: false` even at 132 hits).
- **The strict `< oldest` cursor drops documents silently** (4 of 26,732 on rust). Use `oldest + 1` + objectID dedupe: **zero extra requests**.
- **Raw `>` in `numericFilters` returns a Google Frontend 400 HTML page** that impersonates an outage. `url.Values.Encode()` handles it (`hackernews.go:75` already does this correctly).

### 5.3 The correction that matters most: `topic_daily` is not a pure cache

The seductive claim is "closed HN days are immutable, so `topic_daily` is a cache: evict freely, nothing is lost." **That is false.** HN re-serves the *documents* forever. It does not re-serve the *scores*. An aggregate is `f(documents, MODEL)`, and the student is promoted continuously (plan:21, plan:79 "THE POINTER", plan:164-167 `current.json` polling). Evict a day scored by student v1, refill it under v5, and **March's chart changes retroactively**: a model-version level-shift indistinguishable from a real sentiment change.

**Fix: pin history to the FROZEN teacher.** Plan:191: *"Teacher frozen forever."* Keep `teacher_version TEXT NOT NULL` (the plan's own choice at line 1619); drop `student_version` from the primary series. Then:
- A closed day's aggregate *genuinely is* a pure function of `(topic, day)`.
- Eviction *is* lossless.
- It joins the corpus, which stores **teacher** probs under a frozen allowlist (plan:75/140/178).

The seam to disclose honestly: `AnalyzeTopic` returns *student* scores; history returns *teacher* scores. They are distillation-aligned by construction, and this is far cheaper than the alternative. Say it in the docs; don't hide it.

### 5.4 RSS *does* belong in history, from t0 forward

"History is HN-only by construction because RSS is unbackfillable" is true **only pre-t0**. Because we are keeping the cron (§6), RSS is captured into the durable corpus from t0 onward at **~$0 marginal**: the documents are already harvested and already teacher-labeled. **The cron baseline strictly dominates on history: HN (backfilled to any depth) + RSS (complete from t0), versus HN-only-forever under dormancy.**

Schema: `PRIMARY KEY (topic_id, platform, day)` with `CHECK (platform IN ('hackernews','rss'))`. **Never write `_all`**: that is the only place the t0 composition shift materializes, and a note on a chart doesn't stop anyone plotting it.

### 5.5 Density, not coverage, is the honesty problem

MEASURED, bucketing real 90-day backfills by UTC day:

| topic | docs | zero-days | days with 1-9 docs | median/day |
|---|---|---|---|---|
| **openai** | 10,032 | **0/90** | 0/90 | **102** (a genuine series) |
| **duckdb** | 623 | **10/90** | **59/90** | **4** (a random number generator) |

Same endpoint, same code path. The plan's own `min_sample_size=20` and its own note ("*a z_score of 1.5 on a 20-document sample is noise wearing a statistic's clothes*") mean **the median topic sits below the plan's own noise floor on two-thirds of days.** Non-negotiable: emit per-bucket `sample_size` + CI on every point, and auto-widen day→week when `n < min_sample_size` (duckdb weekly ≈ 48/week, which clears 20). The plan's `sum_sq_polarity` (line 1633) is exactly the right primitive, but `domain.Aggregator` (`domain.go:82-96`) tracks only count/sumPol/buckets today, so it needs a real diff.

### 5.6 The API shape

```
POST /v1/topics {"query":"duckdb"}          → 202 {"job_id":"fc-..."} + Location + Retry-After
                                              201 if already ready (idempotent re-POST)
                                              429 if the backfill token bucket is exhausted
GET  /v1/topics/{slug}/history?from&to&interval=day|week
                                              404 + RFC 9457 problem+json → "POST /v1/topics"
                                              202 if registered, zero days ready
                                              200 with coverage{}, notes[], per-bucket n + CI
```

**GET must stay safe.** A GET that registers a topic and spawns a billed Modal job (up to 27 HN requests + minutes of inference) is an unauthenticated spend primitive for any crawler or link-prefetcher. `POST /v1/topics` is where auth, per-caller quota, and the **global backfill token bucket** live, because the abusable verb is "track a new topic," not "search." Budget client-side: HN Algolia exposes **no** rate-limit headers (`server: Google Frontend` only), the 10,000 req/hr/IP figure is **UNVERIFIED** and appears in no live doc, and the gateway is 1-2 Fly egress IPs sharing one budget. **The first signal is the 429.**

Stampede lock, no new dependency: `INSERT INTO topic (slug, query) VALUES ($1,$2) ON CONFLICT (slug) DO UPDATE SET last_read_at = now() RETURNING id, backfill_job_id, (xmax = 0) AS won`. Fifty concurrent callers, one winner, all fifty get the same `fc-…`. *(`xmax = 0` is a reliable MVCC idiom, not a documented contract. Flagged.)*

Run backfill **on Modal, not the Fly gateway**: a 1000-hit page is 1.89 MB; rust's backfill moves ~51 MB of JSON, ×8 parallel slices, on a 512MB machine whose job is p99 latency. That's an OOM.

---

## 6. The Recommended Topology

### 6.1 Cost table

**Constants (verified rates):** `$/core-mo = 0.0000131 × 3600 × 730 = $34.4268` · `$/GiB-mo = 0.00000222 × 3600 × 730 = $5.8342` · **serving floor** `= 0.125 × 34.4268 + 5.8342 = $10.14/mo` (plan says $10.26 [EST], 1.2% high, immaterial).

**Corrected inference excess.** `intra_op_num_threads=2` = 2 vCPU = **1 physical core** (Modal bills *physical* cores; briefing: "physical core = 2 vCPU"). Excess over the 0.125 reservation is **0.875 cores, not 1.875**, a 2× error that propagated through the earlier passes:

```
excess = req/mo × (200 docs ÷ 75 seq/s) × 0.875 cores × $0.0000131 = $3.06 per 100k req
```

| Architecture | @0 | @100/day | @1k/day | @10k/day | flywheel | history | cold starts |
|---|---|---|---|---|---|---|---|
| **A: BASELINE** (plan as written; daily crons, all warm) | **$19.30** | $19.41 | $20.25 | $28.61 | ✅ | ✅ | none |
| B: full event-driven (no crons, all dormant) | $1.03 | ~$2.00 | ~$4.40 | ~$11 | **❌ dead** | **❌ dead** | yes |
| **C: your proposal** (daily crons, dormant serving + dormant Fly) | $5.87 | $9.89 | ~$20 | $28.61 | ✅ | ✅ | **p50, not p99** |
| D: weekly train + all dormant | $2.87 | $3.83 | $6.15 | $6.18 | ✅ | ✅ | yes |
| E: weekly train + all warm | $6.18 | $6.18 | $6.18 | $6.18 | ✅ | ✅ | none |
| **★ SHIP: A + YAML + 3 fixes + RSS 4h cron** | **$19.40** | $19.51 | $20.35 | $28.71 | ✅ | ✅ | none |

**Two corrections to the baseline, independent of this decision:**
1. **The plan's "optimistic band" item `−$4.30 idle CPU not billed at floor` does not exist.** Modal is verbatim: *"you'll be charged based on whichever is higher: your request or actual usage."* Optimistic is **$10.48, not $6.18**. *(This also answers the plan's own open question #4 at line 1741: yes, idle CPU is billed at the floor.)*
2. **$19.30 is a zero-traffic number wearing a production label.** The $10.26 serving line is traffic-independent and shouldn't be. **At 10k req/day the real baseline is ~$28.61.** Architecture-neutral, so it moves no crossover, but it's a bug in the plan.

### 6.2 The crossovers, and why they never overlap

**Coverage, not per-wake counting.** For Poisson arrivals at rate λ with `scaledown_window` W, the container is alive whenever a request arrived in the last W seconds: `fraction_alive = 1 − e^(−λW)`. At W=300s, `λW = R/288` for R req/day:

| req/day | alive | dormant serving $/mo | **% of requests COLD** |
|---|---|---|---|
| 10 | 3.4% | $0.35 | **96.6%** |
| 100 | 29.3% | $2.97 | **70.7%** |
| 288 | 63.2% | $6.41 | 36.8% |
| 863 | 95.0% | $9.63 | 5.0% |
| 1,326 | 99.0% | $10.04 | 1.0% |
| 10,000 | 100.0% | $10.14 | 0.0% |

**Crossover 1 (dormancy becomes a no-op): ~1,326 req/day.** Above this, `min_containers=0` saves under 1% ($0.10/mo) and *still* risks a cold start on every gap >300s. It never costs *more* than warm. It asymptotically costs the **same**, at which point it is strictly worse: identical bill plus cold starts.

**Crossover 2: the $30 credit binds.** This is the one that decides everything, and **it depends entirely on training cadence:**

- **Under DAILY training:** Modal gross is **$43.12** (plan's own table). The credit is already blown. So dormancy saves **real cash: $10.14 @0, $7.17 @100/day.**
- **Under WEEKLY training:** Modal gross is **$17.24** (plan's own table). The credit binds only at **~13,730 req/day**. But dormancy is already **100% alive at ~1,326 req/day**: `e^(−13730/288) = e^(−47.7) ≈ 2×10⁻²¹`. **The two regimes never overlap. Under weekly, `min_containers=0` saves exactly $0.00 at every traffic level from 0 to 20,000 req/day.**

**Crossover 3 (C vs E): ~10 req/day.** Below it C is marginally cheaper; above it E wins and the gap widens without bound.

**The Fly term is the only unconditional cash:** `$3.32 − $0.0075 = $3.31/mo` @0, `$2.35/mo` @100/day, `$0.03/mo` @1,326/day. **$28-40/yr = 16-32 minutes of engineering per year** at a $75-150/hr loaded rate.

**Counter-intuitive and it favors you:** bursty traffic is *cheaper* than uniform at the same volume, because clustered requests share one container's scaledown window. 100 req/day uniform (864s apart, every one an isolated wake) ≈ $3.57/mo; the same 100 in one hourly burst ≈ $0.46/mo.

### 6.3 The two levers, honestly

Earlier analysis claimed "weekly training saves $25.88 (1.9× what dormancy saves), so you're optimizing the wrong lever." **That's wrong, and it's a units error.** Plan lines 464-470 read: `Weekly | $4.30 | Δ vs weekly - | gross $17.24 | after credit $0.00 | TOTAL $6.18` and `DAILY | $30.18 | +$25.88 | gross $43.12 | $13.12 | TOTAL $19.30`. **$25.88 is the Δ on the *trainer* line, in *gross* dollars.** The actual system saving of weekly is **$19.30 − $6.18 = $13.12 NET**. The credit absorbs the other $12.76.

Like-for-like in **cash**, each lever applied alone to the baseline:

| lever | @0 | @100/day | @1k/day | @10k/day |
|---|---|---|---|---|
| dormancy only (A→C) | **−$13.43** | −$9.52 | −$0.60 | **−$0.00** |
| weekly training only (A→E) | **−$13.12** | −$13.23 | −$14.07 | **−$22.43** |

**They're within 3% of each other at zero traffic.** Your instinct is not aimed at a small lever. But:

- **They are SUBSTITUTES, not independent**: both compete for the same $30 credit headroom.
- **Dormancy's saving decays as `e^(−R/288)`. Weekly's grows.**
- **Adopt weekly and dormancy's Modal half becomes worth exactly $0.00.** That is the single strongest argument for `min_containers=1`, and it is not the one usually made.

### 6.4 The decision

**Keep daily training.** You said it's what makes the system worth using; plan:460 says *"Yes. Daily training costs $30.18/mo gross, $13.12/mo out of pocket, and you should pay it"*; plan:503 says a daily loop is its own reliability mechanism. **I am not overturning your stated priority as a side effect of a latency question**: that is exactly the mistake I'm asking you not to make.

**Given daily, keep both tiers warm anyway.** Your own plan already priced and rejected this at line 1803: *"`min_containers=0` (−$9.29: buys an unmeasured cold start on the latency path)"*. **The cold start is no longer unmeasured.** It's **3.9-12.4s** at your exact config with a real ONNX transformer. Internal breakdown: import ORT 1.548s, `InferenceSession` init **6.214s**, actual inference **0.017s**. The 0.125-core reservation starves session init; inference itself is free. And at 100 req/day, **70.7% of requests are cold: that's your median, not your tail.** $9.52/mo does not buy a 2.4-5× p50 regression on the majority of your traffic.

Two footnotes that close the escape hatches:
- **`enable_memory_snapshot` makes it WORSE, not better.** MEASURED at `cpu=0.125`: 10,706 / 14,446 / 11,834 / 9,687 / 9,551 ms across 5/5 cold runs, consistently worse than 3.9-12.4s without it. Restoring a snapshot pages the whole ORT session back in, which at 0.125 cores is slower than re-initializing. It's built for large GPU models.
- **Externalizing the cache to Upstash does not rescue dormancy.** Fly's autostop fires at ~5min idle; `SENTILYZER_CACHE_TTL` is 10m (`config.go:72`) and `cache.go` expires on **wall clock**. Any dormancy long enough to trigger a stop leaves entries ≥5min old; any dormancy >10min leaves them **all expired**: 0% hit rate at wake, whether they sit in the heap, a suspended heap, or Redis. **The cache and scale-to-zero are anti-correlated by construction:** the cache only pays off when traffic is dense enough to keep the machine alive, which is exactly when scale-to-zero saves nothing.

---

## 7. The Design Choices

| # | Choice | **Recommendation** | Why |
|---|---|---|---|
| 1 | Sync vs async for `AnalyzeTopic` | **SYNC. No 202. No streaming. No `google.longrunning`. No webhooks.** | p50 2.9s vs AIP-151's ~10s bar; a 4µs cache hit must never become a 202 |
| 2 | Sync vs async for **history backfill** | **ASYNC: `202` + `fc-…`.** Inference (0.4-34.6 min), not connectors (2-5s), forces it | The only path that crosses the bar |
| 3 | Batch chunking | **BLOCKER. Chunk at 32, length-bucketed, in `inference/client.go` below the `Client` interface** | Nothing runs today; `service.go` never learns chunks exist |
| 4 | `SENTILYZER_ML_MAX_CHARS` | **Leave at 2000. Ship bucketing first, then measure.** | Bucketing does most of the work. Mean HN comment is **554 chars, p95 1700**: both exceed 512, and sentiment's polarity clause often lands at the end. "Near-zero truncation loss" was a *token-count* argument, not an *accuracy* measurement. Measure label agreement at 512/1024/2000 first |
| 5 | Per-connector timeout | **`context.WithTimeout(ctx, 3s)` + partial results** | 10 lines. Collapses p99 12s → 3s. Worth more than the entire async question |
| 6 | Partial-failure visibility | **Add `partial` + `warnings[]`** (Textract's `PARTIAL_SUCCESS` shape) | `service.go:322-325` discards per-connector errors unless *all* fail; store failure is a bare `fmt.Printf` (`service.go:236`). 3-of-7-failed is byte-identical to clean **today** |
| 7 | Cache poisoning | **`if !out.Partial { s.Cache.Set(...) }`**: `service.go:232` is unconditional today | One connector blip poisons a topic for 10m. *(Chronic timeouts → permanently uncacheable topic; a shorter partial-TTL is the real fix, unsized)* |
| 8 | Modal `min_containers` | **1** | Under daily: $7.17/mo buys away 3.9-12.4s on 70.7% of requests. Under weekly: saves **$0.00** |
| 9 | Fly `auto_stop_machines` | **`"off"`** | $28-40/yr = 16-32 min of engineering/yr. gRPC channels get **no GOAWAY** on stop (Fly's Apr-2026 graceful-close is WebSocket-only); the proxy warns the wake-triggering request "might fail." **Also set `[services.concurrency] type = "requests"`**: the default is `"connections"` and that metric drives autostop |
| 10 | `scaledown_window` | **300s. Unchanged.** | Costs nothing under the credit; protects p99. (Under dormancy you'd *shorten* to 60s, the plan's reasoning inverts, but that's moot) |
| 11 | Job state, **if** async is ever built | **Modal's `FunctionCall` id. Nothing else.** | PROVEN: process A spawned, printed `fc-…`, **exited**; a fresh process B polled it to completion. The string IS a durable 7-day job token. No Redis, no jobs table, no `modal.Dict` (**doesn't exist in the Go SDK**, verified against the `Client` struct) |
| 12 | Backfill membership | **Algolia for *which* docs, corpus for *their* teacher scores** | Local FTS silently redefines "documents about X" (Algolia stems + typo-tolerates) |
| 13 | History scorer | **Frozen teacher.** `teacher_version NOT NULL`, drop `student_version` from the series | Plan:191 "Teacher frozen forever." Otherwise eviction rewrites published history |
| 14 | History sources | **Per-platform: `hackernews` (backfilled) + `rss` (forward-only from t0). `_all` unrepresentable.** | The cron gives RSS from t0 at ~$0 marginal. `_all` renders composition change as sentiment change |
| 15 | Never-tracked topic | **404 + RFC 9457 pointing at `POST /v1/topics`** | GET must stay safe. 200-with-empty-points is indistinguishable from a genuinely silent topic (duckdb has 10 real zero-days) |
| 16 | Eviction | **Drop from the RAM snapshot only (LRU by `last_read_at`); never delete Neon rows** | Plan:1648 mirrors `topic_daily` entirely into RAM on a 512MB box, sized for ~200 curated topics; lazy pull-through makes topic count user-driven and unbounded |
| 17 | RSS cadence | **New cron: `modal.Cron("0 */4 * * *")`, +$0.10/mo** | **Bug fix.** hnrss holds 9h48m; the daily harvest has a 24h gap and already loses RSS. Uses 3 of Starter's 5 slots |
| 18 | RSS/HN firehose branch | **Required. `Query.Window` + a `Topic==""` branch in BOTH `hackernews.go` and `rss.go`** | The plan needs this regardless; `rss.go:53-55` has the same gate as `hackernews.go:48` |
| 19 | Dead Reuters feed | **Delete `rss.go:35`** | Reproducible 401, silently contributing zero docs in `main` today |
| 20 | `middleware.Timeout` | **60s → 15s** (`rest.go:38`) | 60s is absurd against a 6s p99. **Also bound the gRPC path**: `grpc.go` has no equivalent, so 13 chunks × 15s is reachable if a client sets no deadline |
| 21 | gRPC error codes | **Fix `grpc.go:67`**: everything maps to `InvalidArgument` today | Should be: `InvalidArgument` (empty topic) / `Unavailable` (all connectors failed) / `DeadlineExceeded` / `Internal` |
| 22 | REST error codes | **Fix `rest.go:159-161`**: everything maps to 502 today, including classify `DEADLINE_EXCEEDED` and "no platforms enabled" | 400 / 502 / 504 / 500 |
| 23 | Volume reload | **Mandatory with `min_containers=1`** | Containers mount latest state at creation; later external commits are invisible until `.reload()`. A long-lived serving container makes **promotion AND rollback silently no-op while reporting success** |
| 24 | `api/internal/store` | **Delete. Orthogonal. Ship regardless.** | `service.go:234` still writes per-document rows for YouTube (30-day retention + derived-data ban) and Reddit (uncurable). An idle machine's SQLite file still holds them |
| 25 | Streaming (`architecture.md:104-106`) | **Deprioritize** | Buys the ~0.2-1.5s connector spread; the dominant 2-3s batched `Classify` can't be streamed per-platform. And `graphql-go/handler v0.2.4`'s `go.mod` requires **only** `graphql-go/graphql` (no websocket dep); `subscriptionEndpoint` merely points the playground at a `ws://` server you'd write. **Unless the real motivation was *perceived* responsiveness: that's a UX goal my latency analysis doesn't price, and it's the one argument for streaming I can't refute with a number. Tell me if that's what you meant.** |

---

## 8. YAML + Formats

### 8.1 Library: `sigs.k8s.io/yaml v1.6.0`, and it's nearly free

`api/internal/domain/domain.go` carries **zero** `yaml:` tags (verified: `grep -c "yaml:"` → 0). `sigs.k8s.io/yaml` marshals **through `encoding/json`**, so it honors the existing `json:` tags. **Parity is guaranteed by construction, with zero new struct tags.**

MEASURED against the repo's real types:

| | `gopkg.in/yaml.v3` | `sigs.k8s.io/yaml` |
|---|---|---|
| `ByPlatform` | `byplatform` ❌ | `by_platform` ✅ |
| `MeanPolarity` | `meanpolarity` ❌ | `mean_polarity` ✅ |
| `URL` (json tag: `source_url`) | `url` ❌ | `source_url` ✅ |
| `omitempty` | leaks `aspects: []`, `author: ""` ❌ | honored ✅ |
| Status | **ARCHIVED 2025-04-01** | depends on the maintained `go.yaml.in` fork |

`goccy/go-yaml` is healthier by activity **and** honors json tags. Rejected anyway on a measured conformance bug: it emits **unquoted timestamps**, so a Python client doing `json.dumps(yaml.safe_load(resp))` raises `TypeError: Object of type datetime is not JSON serializable` while the JSON client gets a `str`. For an API whose premise is "same data, many formats," breaking *type* parity is disqualifying. `go.yaml.in/yaml/v4` is rc-only.

**Costs:** binary +304 KB (+1.51%); go.sum 124→132 lines; pure Go (the plan's CGO-free property at line 270 is preserved). Encode is 3.2× JSON (366,275 vs 112,880 ns/op through the real chi router) but the absolute delta is **+253µs**, and it lands on **Fly, which bills machine-seconds, not CPU-seconds**. At 366µs against Fly's 5ms/80ms baseline quota, YAML alone would saturate only at ~171 req/s ≈ **14.8M req/day**, ~1,480× above your top scenario. **$0.00/mo.** The cache is also format-agnostic: `service.go:45` caches `*domain.SourcedAnalysis` (the domain object, not the payload) and `buildCacheKey` (`service.go:329`) has no format term: a third format cannot fragment it.

### 8.2 The q-value rewrite is non-optional: it fixes a live bug

`rest.go:226` is verbatim:
```go
if strings.Contains(accept, mimeXML) && !strings.Contains(accept, mimeJSON) {
	return mimeXML
}
```
A browser sends `text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8`: contains `application/xml`, does not contain `application/json`. **Every browser hitting `GET /v1/analyze/topic?topic=X` gets XML today.** It also mis-ranks `application/xml;q=0.9, application/json;q=0.1` as JSON.

You **cannot** bolt on a third `Contains` branch: YAML could never win when `application/json` is present, which is what every SDK sends. Hand-roll ~45 lines on stdlib `mime.ParseMediaType`; skip `elnormous/contenttype` (54 stars, last release 2023-02-28).

**The browser rule:** compare q-values, don't guess. A browser ranks `text/html` at q=1 **at or above** the data types it accepts as fallback; a deliberate data client never leads with `text/html` at full weight. So `if htmlQ >= offers[0].q → JSON`, which fixes browsers while `text/html;q=0.1, application/yaml` still correctly yields YAML.

### 8.3 **Responses only. Do NOT accept YAML request bodies.**

This is the one place I'll push back hard on the symmetry instinct. `sigs.k8s.io/yaml` routes through `go.yaml.in/yaml/v2` = **YAML 1.1 semantics**. MEASURED against DTOs mirroring the repo's real `analyzeTextDTO`/`analyzeTopicDTO`: **every one returns `nil` error, HTTP 200, no signal:**

```
topic: no                -> Topic = "false"    ← fans out searching HN/RSS for the literal string "false"
text: y / on             -> Text  = "true"
text: no / NO            -> Text  = "false"
text: null               -> Text  = ""         ← empty document, silently
text: 013                -> Text  = "11"       (octal)
text: 1.20               -> Text  = "1.2"
id: 013                  -> ID    = "11"
language: no             -> Language = "false" ← "no" is ISO 639-1 Norwegian
metadata: {k: no}        -> {k: "false"}
limit_per_platform: 013  -> 11
```

Three reasons this settles it:

1. **The trap is universal, not narrow.** It's every string field. `topic`, the field the entire request is about, is the worst case, and a `language`-only guard leaves it wide open.
2. **`text` is unguardable, and it's the worst case for *this* API specifically.** Sentilyzer analyzes terse social snippets. `"no"`, `"y"`, `"yes"`, `"on"`, `"off"` aren't adversarial inputs: **they are the corpus.** Any guard on `text` has false positives on exactly the documents the API exists to score.
3. **"`UnmarshalStrict` is strictly better validation" is false as a net claim.** It wins on unknown fields and duplicate keys (real wins: `encoding/json` silently takes the last). But **JSON has no silent-coercion class at all**: `{"text": "no"}` is unambiguous and `{"text": no}` is a *syntax error*. YAML input trades a loud error class for a silent one. If you want duplicate-key rejection on the JSON path, add it *there*.

Ship `Content-Type: application/yaml` → **415** with a body naming `application/json`/`application/xml` and pointing at `?format=yaml`. Response-only can be relaxed later; **accepting YAML input is a contract you cannot withdraw.**

### 8.4 XML: document as lossy. Do not fix, do not deprecate.

`domain.go` tags three maps `xml:"-"` because `encoding/xml` cannot serialize `map[string]X`: `Score.Probabilities` (:32), `Document.Metadata` (:47), `Aggregate.LabelCounts` (:76). Meanwhile `proto/sentilyzer/v1/sentilyzer.proto:44` has `map<string, float> probabilities` and `:128` has `map<string, int32> label_counts` **natively**. Fidelity: **gRPC = JSON = YAML > XML.**

MEASURED @limit=25: **JSON 6,332 B · YAML 4,508 B (−28.8%) · XML 4,624 B.** Note XML is **larger** than YAML *despite* silently omitting data: it is neither smaller nor complete.

**YAML doesn't create this problem; it makes it visible.** XML has been lossy since day one. YAML just moves it from 1-of-2 (looks like a tie) to 1-of-3 (obviously the odd one out). **Expect "why is XML missing fields?" the day YAML ships. That answer is owed today regardless.** Fix it only when a real XML client asks: there are none (no `openapi/`, no SDK dir, verified).

### 8.5 The concrete `rest.go` change

**Blast radius CONFIRMED: `rest.go` + `go.mod`/`go.sum` only.** `grep` for `preferredFormat|mimeJSON|mimeXML|writePayload|decodeRequest` hits `rest.go` and nowhere else; `graphql.go` and `grpc.go` import neither `encoding/json` nor `encoding/xml`.

```go
const (
	mimeJSON = "application/json"
	mimeXML  = "application/xml"
	// mimeYAML is the canonical type registered by RFC 9512 (Feb 2024).
	// RFC 9512 defines NO parameters, so unlike JSON/XML we must NOT append
	// "; charset=utf-8". rest.go:246 does that unconditionally today.
	mimeYAML = "application/yaml"
	maxBodyBytes = 1 << 20
)

// Accepted on INPUT only (RFC 9512 §5 predecessors). Always EMIT the canonical.
var yamlAliases = []string{"text/yaml", "application/x-yaml", "text/x-yaml"}
var supportedMedia = []string{mimeJSON, mimeYAML, mimeXML}

// negotiate replaces preferredFormat (rest.go:211-229). Returns ("", false) -> 406.
// Parse each element with mime.ParseMediaType; q defaults to 1.0; q=0 is an
// explicit REFUSAL; malformed q -> 1.0; handle */* and application/*; map
// yamlAliases -> mimeYAML. Rank by q, then specificity, then header order.
//
// BROWSER RULE (deliberate deviation from strict RFC 9110, which would serve
// XML because application/xml;q=0.9 outranks */*;q=0.8, the live bug):
//   if htmlQ >= offers[0].q { return mimeJSON, true }
func negotiate(accept string) (string, bool) { /* ~45 lines, stdlib only */ }

func writeIn(w http.ResponseWriter, format string, status int, payload any) {
	w.Header().Set("Vary", "Accept") // one URL now yields three bodies
	switch format {
	case mimeYAML:
		w.Header().Set("Content-Type", mimeYAML) // bare: no charset, per RFC 9512
		b, err := yaml.Marshal(payload)          // sigs.k8s.io/yaml: reads json tags
		if err != nil { /* 500 as JSON */ ; return }
		w.WriteHeader(status)
		_, _ = w.Write(b)
	case mimeXML:
		w.Header().Set("Content-Type", mimeXML+"; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(xml.Header))
		_ = xml.NewEncoder(w).Encode(payload)
	default:
		w.Header().Set("Content-Type", mimeJSON+"; charset=utf-8")
		w.WriteHeader(status)
		enc := json.NewEncoder(w); enc.SetIndent("", "  "); _ = enc.Encode(payload)
	}
}

// decodeRequest (rest.go:232): UNCHANGED. JSON/XML only.
// Content-Type: application/yaml -> 415. See §8.3.
```

**⚠️ The one thing that will silently ship broken.** `rest.go:163` is:
```go
writePayload(w, req, http.StatusOK, asTopicEnvelope(resp))
```
**REST never marshals `domain.SourcedAnalysis`.** `asTopicEnvelope` (`rest.go:169-208`) builds a separate `wrap` struct copying fields explicitly. So the `partial`/`warnings[]` work from §7 item 6 must be added to **`wrap` too**, or it will be silently absent from JSON, XML **and** the new YAML: the primary transport, and the exact surface the feature exists to serve. It fails with an omitted field, not a compile error. `gRPC` and `GraphQL` need their own projection edits. **"One core, thin adapters" means one domain field and *three* adapter edits, not zero.**

**Bugs found in this file while you're in it** (fix in separate commits, with golden-file tests):
- `rest.go:176`: `` `json:",inline" xml:",inline"` `` on `breakdown.Aggregate`. **Neither is a real option.** `encoding/xml` emits spurious `<Aggregate>` wrappers inside each `<platform key="...">`; `,inline` is a go-yaml/BSON idiom.
- `rest.go:170-178`: dead `type kv struct{...}` + `_ = kv{}`.
- `rest.go:288-289`: `var _ = fmt.Sprintf`, commented as a "compile-time assertion that REST satisfies http.Handler." It is not.
- `graphql.go:111-118`: `TopicAnalysis` is missing `byPlatform`/`byAspect`, which JSON, XML **and** gRPC all carry. GraphQL is a second-class surface today.

**⚠️ 406 is a behavior change.** Today `Accept: image/png` silently returns JSON. Consider logging would-be-406s for one release before enforcing.

---

## 9. What I'd Actually Do

### The bottom line

**Your instinct is aimed at a lever worth roughly the same as the one you already chose, and it costs you the thing you said you cared about most.** Serving is already event-driven. The cron is not serving; it's the flywheel and the history. Deleting it takes your corpus from 10,000 docs/day to 355 (**92.9% dedupe loss, MEASURED, because `hackernews.go:48` makes the connector relevance-ranked search and repeat queries return identical top-N forever**), makes it a traffic-coupled sample of user curiosity, and loses RSS permanently. Then you'd be retraining daily on noise.

### Ship order

1. **Chunk + length-bucket `Classify`/`ClassifyAspects`** in `inference/client.go`, below the `Client` interface. **BLOCKER: `main` is broken at defaults.** Unpermute correctly and **write a property test**: if `out[idx[start+j]]` is wrong, every score attaches to the wrong document, the response still validates, the aggregate still looks plausible, and nothing errors. `service.go:203-222` zips positionally with no id check: there is no downstream tripwire.
2. **Per-connector `context.WithTimeout(3s)` + `partial` + `warnings[]`** in `service.go` fanout, **and in `asTopicEnvelope`'s `wrap`, `grpc.go`, and `graphql.go`.** 12s → 3s tail. Fixes a real bug.
3. **`if !out.Partial { s.Cache.Set(...) }`**: one line, prevents 10m poisoning.
4. **YAML + q-value `negotiate` + `Vary: Accept` + 406**, responses only. ~130 lines, `rest.go` only.
5. **`middleware.Timeout` 60s → 15s; bound the gRPC path; fix `grpc.go:67` and `rest.go:159-161` code mapping.**
6. **Delete `rss.go:35` (dead Reuters). Delete `api/internal/store`** (ToS: orthogonal, ship regardless).
7. **Add `poll_rss` at `modal.Cron("0 */4 * * *")` (+$0.10/mo) and the `Query.Window` firehose branch in BOTH `hackernews.go` and `rss.go`.** Bug fixes to the baseline.
8. **Add `weights.reload()` to the serving path.** Mandatory with `min_containers=1`, or rollback silently no-ops while reporting success.
9. **MEASURE the real INT8 ONNX student on a Modal `cpu=0.125` container at batch=200.** This is the only input that can flip anything. **STOP HERE if p99 < 10s. You're done.**

### Diffs vs `docs/continuous-training-plan.md`

| Plan says | Change to |
|---|---|
| $19.30/mo | **~$28.61/mo at 10k req/day.** $19.30 is a zero-traffic number; inference bills actual CPU above the floor |
| Optimistic band: `−$4.30 idle CPU not billed at floor` | **Delete: the money doesn't exist.** Modal bills `max(request, actual)`. Optimistic is $10.48. *(Answers the plan's own open question #4, line 1741: yes, it's billed)* |
| Serving floor $10.26 [EST] | **$10.14** (1.2%, immaterial) |
| `intra_op_num_threads=2` justified as a latency ceiling | **It's a BILLING control.** `cpu=0.125` is a *reservation*, not a cap ("Containers can exceed this minimum"; soft throttle only at request+16 cores). Don't model inference as "0.125 cores": it bursts. Thread scaling is sublinear (1183/727/593ms at 1/2/4 threads) so 2 is the right knee |
| Daily harvest covers RSS | **It doesn't.** hnrss holds 9h48m; a 24h gap loses data. Add the 4h poll |
| `topic_daily` with `student_version` | **Teacher-pinned.** `teacher_version NOT NULL`; drop `student_version` from the series. Otherwise eviction rewrites published history |
| `min_containers=0` "−$9.29: unmeasured cold start" (line 1803) | **No longer unmeasured: 3.9-12.4s.** Keep the rejection; upgrade the reason |
| Gateway→ML must be `@modal.fastapi_endpoint` over HTTPS | **Obsolete.** `github.com/modal-labs/modal-client/go v0.9.0` talks to Modal's control plane natively (spawn 143ms, poll ~110ms, verified end-to-end). Import path is the **module root**, not `.../go/modal`. Pin it: pre-1.0, explicitly "not stable." **Permanent constraint: Go can only read outputs from calls Go itself spawned** ("PICKLE output format is not supported"). Deployment stays Python |
| - | **Add `openapi/sentilyzer.v1.yaml`**: JSON + YAML share **one** `$ref` (provably identical); XML gets its own, honestly marked lossy. That asymmetry tells generators the truth |

### If you still want dormancy

**One config flip, fully reversible, and I'd tell you no:** `auto_stop_machines = "suspend"` (not `"stop"`, the 512MB gateway is eligible: ≤2GB, no swap/schedule/GPU) turns ~2s into a few hundred ms at identical cost. It saves **$28-40/yr**. If you do it: set `[services.concurrency] type = "requests"` (default is `"connections"` and that metric drives the stop decision), set a health-check `grace_period`, and add a gRPC `retryPolicy` on `UNAVAILABLE`. Fly's graceful-close is WebSocket-only, so an idle channel gets severed with **no GOAWAY**. Do it to learn Fly's autostop, not because $40/yr matters.

**The cheapest real lever is not dormancy. It's weekly training: $13.12/mo NET, 68% of your bill, one cron string, measurable, reversible.** Your plan already concedes daily buys *"input-distribution coverage, not accuracy drift"* (line 501) because the teacher's data ends December 2021. **Test weekly against the frozen golden set.** If it holds, take it, and then dormancy is worth $2.35/mo and the question closes itself. If it doesn't, daily costs $13.12/mo and the answer is: pay it. **Decide that on training merits, not from a latency conversation.**

---

### Uncertain inputs, ranked by how much they move things

1. **The $30 Modal Starter credit**: a plan term Modal can change unilaterally. It's the single largest structural dependency here: without it, warm serving costs a real $10.14/mo and the dormancy verdict flips.
2. **75 seq/s inference throughput [EST]**: drives every credit-headroom figure. At 50 seq/s the free ceiling drops to ~9,000 req/day; at 120 seq/s it rises to ~22,000. **The crossover (depends only on W) is unaffected. Absolute bills are not.**
3. **The 1.5-2.5× x86 derate [EST]**: measured on Apple M4 Pro (arm64), not Modal. At 50 seq/s p99 lands at 9-10s, right on AIP-151's bar, and the sync call becomes marginal.
4. **The 3.9-12.4s Modal cold start**: MEASURED, but n=3 with a 3× spread dominated by worker image-cache state, taken from a laptop (includes ~100ms RTT). **A Modal deploy briefly reintroduces the worst case even with `min_containers=1`.**
5. **Fly's ~5min idle timeout and ~2s wake**: community-reported / Fly's own docs, **not measured here** (flyctl not installed). Community reports 3-4s baseline and ~30s outliers.
6. **HN Algolia's 10,000 req/hr/IP**: **UNVERIFIED.** In no live doc, in no response header; 150 requests at 8.2 req/s drew zero 429s. **You cannot detect approaching it. Budget client-side.**
7. **hnrss's 9h48m window**: one measurement, one day. A busy news day shortens it. Measure the p05 across a week before trusting 4h.
8. **Heaps' law calibration (k=2.524, β=0.6)**: a model. The `r ≈ 0` repeat contribution is MEASURED and is what actually kills the event-driven corpus; the ceiling exists regardless of the constants.