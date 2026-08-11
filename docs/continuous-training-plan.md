<!--
Generated 2026-07-15 by a multi-agent research + design pass (38 agents, 260 verified
findings, 12 surviving adversarial refutations). Prices and API syntax were verified
against live docs on that date and WILL drift. Re-check anything load-bearing.

Claims are tagged [EST] (estimated) or [UNCERTAIN] (unverified) where they are not
verified. Those tags are not decoration; the cost model's headline depends on several.

The Terms-of-Service audit in section 11 is ENGINEERING RESEARCH, NOT LEGAL ADVICE.
It was produced by reading published ToS documents. Get a lawyer before relying on it.
-->

# Sentilyzer: Architecture & Implementation Plan

*Final synthesis. Every price traces to the verified briefing; every file path was read in this repo at `/Users/chijiokeekechi/workspace/github.com/chijiokekechi/sentilyzer`. Where a skeptic refuted a proposal at fatal/major severity, the correction is adopted and named.*

---

## 1. The answer in 5 sentences

**Sentilyzer becomes a distillation flywheel with exactly two always-on components and one paid line item.** A static, CGO-free Go binary (`api/cmd/sentilyzer-harvest`) stays the *single* owner of all connector logic and of a **compile-time ToS gate**. Modal ships it into a `debian_slim` image and shells out to it nightly, labels the eligible harvest (**HackerNews + RSS only: every other source is legally blocked from training, and Reddit is uncurable at any price**) with the frozen RoBERTa/DeBERTa teachers on a T4, distills a 6-layer `distilroberta-base` student on an A10 under a **hard 45-minute wall-clock budget**, and promotes it by rewriting a `current.json` pointer on Cloudflare R2. **The student serves production from Modal on CPU as INT8 ONNX with `min_containers=1`, not in-process next to Go**, because Fly's shared vCPUs deliver 6.25% of a physical core each and quotas are *shared across the machine*, making `shared-cpu-4x` **0.25 sustained cores, not the 2 the in-process proposal assumed** (an 8× error two independent skeptics found). The always-warm Go gateway on Fly keeps all four transports (`rest.go`, `grpc.go`, `graphql.go`, untouched) and gains 8 new RPCs for tracked topics, history, trend and alerts, backed by a tiny Neon Postgres time series the gateway mirrors *entirely* into RAM so Neon can autosuspend. **Total: $19.30/mo expected (band $6.18-$47.36) against a $50-100 budget. And daily retraining is $30.18/mo of gross Modal spend, of which the $30 Starter credit covers the first 17 runs, so the entire cash price of your stated priority is $13.12/mo.**

---

## 2. Architecture

```
┌── UPSTREAM ────────────────────────────────────────────────────────────────────┐
│ Policy.Durable = TRUE   (may enter durable storage: R2 corpus + Neon aggregates)│
│   hackernews   HN Algolia · no auth · 10,000 req/hr/IP · 11,436 docs/day        │
│   rss          feedparser · no auth · no contract, ordinary copyright           │
├────────────────────────────────────────────────────────────────────────────────┤
│ Policy.Durable = FALSE  (interactive AnalyzeTopic only: NOTHING persists)       │
│   reddit    ✗ Data API Terms §2.4: rights withheld by RIGHTSHOLDERS. UNCURABLE  │
│   twitter   ✗ §III.A(d) derivative works + $0.005/post ≈ $1,500/mo at 10k/day   │
│   stocktwits ✗ ToS §5 (rev. 2026-07-10); dev registration frozen since ~2021    │
│   youtube   ✗ §III.E.4.d 30-day retention + §III.E.4.h derived-data ban         │
│   mastodon  ✗ permitted-by-omission, but every instance blocks GPTBot           │
│   mock      ✗ synthetic templates (would poison the corpus)                     │
└───────────────┬────────────────────────────────────────────────┬───────────────┘
                │ batch, nightly                                  │ interactive
                ▼                                                 ▼
┌── MODAL  app="sentilyzer-pipeline" ───────┐  ┌── FLY.IO  shared-cpu-1x / 512MB ──────┐
│  scale-to-zero · 2 crons of Starter's 5   │  │  min_machines_running=1               │
│                                            │  │  auto_stop_machines=false             │
│ 04:15 daily_pipeline   0.25c  orchestrator │  │                                       │
│   ├ harvest        1c   ~9m ─┐             │  │  sentilyzerd  (Go, CGO-FREE)          │
│   │   exec /opt/sentilyzer/  │  the SAME   │  │   :8080 REST/JSON · REST/XML · GraphQL│
│   │   harvest  ◀─────────────┼──Go binary──┼──┤   :8443 gRPC (tls, alpn=["h2"],       │
│   │   (add_local_file,       │  CGO_       │  │          h2_backend=true)             │
│   │    CGO_ENABLED=0)        │  ENABLED=0  │  │                                       │
│   ├ prep_corpus    2c   ~10m │             │  │  service.Service (protocol-agnostic)  │
│   │   DuckDB dedup+reservoir │             │  │   ├ connectors.Registry (all 7)       │
│   ├ teacher        T4   ~3m  │             │  │   ├ cache.TTLCache ×2 (10m)           │
│   │   cardiffnlp RoBERTa +   │             │  │   ├ inference.Client ─────────────────┼─┐
│   │   yangheng DeBERTa FROZEN│             │  │   └ timeseries.Store (pgx) +          │ │
│   ├ train          A10  ≤45m │  ⏱ HARD CAP │  │      FULL 90-day RAM snapshot         │ │
│   │   distilroberta 2-head   │             │  └────────────┬──────────────────────────┘ │
│   ├ evaluate       2c   ~5m  │             │      pgx, MinConns=0, hourly refresh       │
│   │   4-tier dual-anchored   │             │               ▼                            │
│   └ promote        0.25c ~1m │        ┌────┴───┐ ┌── NEON POSTGRES (Launch) ─────┐      │
│       write current.json ────┼────┐   │        │ │  autosuspends after 5m idle    │      │
│ 05:30 Sun prune   0.5c       │    │   │        │ │  tracked_topics · topic_daily  │      │
│                              │    │   │        │ │  topic_aspect_daily · alerts   │      │
│ modal.Volume "sentilyzer-    │    │   └────────┼─│  alert_events · runs           │      │
│   weights"                   │    │            │ │  run_sources                   │      │
│   /weights/teachers/<sha>/   │    │            │ │  ~150 MB/yr · $1.66/mo         │      │
│   /weights/ckpt/<run_id>/    │    │            │ └────────────────────────────────┘      │
│   NEVER read outside Modal   │    │                                                      │
└──────────────┬───────────────┘    │                                                      │
     DuckDB    │                    │ write                                                │
     httpfs    ▼                    ▼                                                      │
┌── CLOUDFLARE R2  ($0.015/GB-mo · 10 GB free · ZERO EGRESS · S3-compatible) ────┐         │
│  corpus/documents/dt=<date>/platform=<id>/part-*.parquet   (text, no labels)   │         │
│  corpus/labels/dt=<date>/platform=<id>/part-*.parquet      (teacher probs)     │         │
│  corpus/reservoir/v1/*.parquet          (materialized weekly, NOT nightly)     │         │
│  corpus/manifests/run_id=<id>/docs.parquet   (reproducibility pin)             │         │
│  students/<run_id>/{model.int8.onnx, tokenizer.json}   ~80 MB                  │         │
│  students/current.json   ◀══ THE POINTER. Promotion AND rollback are this file.│         │
└──────┬──────────────────────────────────────────────┬──────────────────────────┘         │
       │ poll 5m + sha256                             │ GET once, cached, sha256           │
       ▼                                              ▼                                     │
┌── MODAL  app="sentilyzer-serving" ────┐  ┌── pip install sentilyzer[local] ──┐            │
│ @app.cls  cpu=0.125 · memory=1024     │  │  onnxruntime+tokenizers+numpy     │            │
│   min_containers=1 · scaledown=300    │  │  ~40 MB · NO torch (527 MB CUDA)  │            │
│   NO memory snapshots (see CRUX 5)    │  │  LocalEngine → same pydantic types│            │
│ INT8 ONNX student · NO torch (~65 MB) │  └───────────────────────────────────┘            │
│ so.intra_op_num_threads = 2  ◀ MUST   │  ┌── sdk/go  module .../sdk/go ──────┐            │
│ @modal.fastapi_endpoint(              │  │  tag: sdk/go/vX.Y.Z (dir-prefixed)│            │
│   label="sentilyzer-infer",           │◀─┤  deps: grpc + protobuf ONLY       │            │
│   requires_proxy_auth=True)           │  └───────────────────────────────────┘            │
└──────────────▲────────────────────────┘  ┌── openapi/sentilyzer.v1.yaml ─────┐            │
               │                            │  hand-authored 3.1 + 3.0.3 dgrade │            │
               │ HTTPS + Modal-Key/Secret   │  Scalar OSS on Cloudflare Pages   │            │
               └────────────────────────────┼──BSR public module (free)         │────────────┘
                 NOT gRPC. Modal's ASGI     └───────────────────────────────────┘
                 layer cannot express HTTP/2 stream IDs. The PUBLIC gRPC
                 surface at :8443 is untouched: it terminates at the gateway.

MONITORING (all $0): healthchecks.io (dead-man's switch, 3 of 20 checks) ·
Axiom Personal (500 GB/mo, 30-day: Modal Starter retains logs for 1 DAY) ·
Better Stack (uptime + status page, 2 of 10 monitors)
```

---

## 3. The distillation flywheel

```
   04:15 UTC  HARVEST                                            [Go binary, CPU, ~9 min]
   ─────────────────────────────────────────────────────────────────────────────────────
   /opt/sentilyzer/harvest --date=2026-07-15 --durable-only --out=docs.jsonl
     · HN Algolia, HOURLY time-slices (numericFilters=created_at_i>LO,<HI)
       (the 1,000-hit cap is PER QUERY, not per page. hitsPerPage=1000 returns
         nbPages=1 and page=1 returns zero with "you can only fetch the 1000 hits
         for this query". Naive pagination silently truncates at 1k and LOOKS FINE.)
         48 requests/day against 240,000/day = 0.02% utilisation.
     · RSS current feed (Backfillable=false: a missed RSS day is GONE FOREVER)
     · content_sha256 = sha256(normalize(text))  ← the training dedupe key
   → r2://corpus/documents/dt=2026-07-15/platform={hackernews,rss}/
   IDEMPOTENT BY WHOLE-PARTITION OVERWRITE. A re-run is a no-op by construction:
   no unique index, no dedupe migration, no ON CONFLICT.
   ~10,000 usable docs/day
                                    │
   04:30 UTC  PREP                  ▼                                  [CPU, ~10 min]
   ─────────────────────────────────────────────────────────────────────────────────────
   DuckDB: content-hash dedupe · 90-day window · 40≤text_len≤2000 ·
           UNION materialized reservoir → /weights/train.parquet
   ◀ THIS RUNS ON CPU, NOT ON THE A10. $0.047/core-hr vs $1.1016/hr = ~23× cheaper
     for identical work. (storage skeptic, major) The reservoir is refreshed WEEKLY,
     not re-sorted nightly: a nightly ROW_NUMBER() over all history grows forever.
                                    │
   05:00 UTC  TEACHER LABEL         ▼                             [T4 GPU, ~3 min]
   ─────────────────────────────────────────────────────────────────────────────────────
   FROZEN cardiffnlp/twitter-roberta-base-sentiment-latest  ← never retrained
   FROZEN yangheng/deberta-v3-base-absa-v1.1                ← never retrained
   fp32, explicit dtype=torch.float32. Reuses ml/sentilyzer_ml/inference.py's
   TransformerBackend so labels inherit _order_for()'s canonical (neg,neu,pos).
   Anti-joins labels/ → a re-run costs ~0 GPU-seconds.
   → r2://corpus/labels/dt=/platform=/  (p_negative, p_neutral, p_positive: float32)
                                    │
   05:30 UTC  TRAIN                 ▼                  [A10 GPU, ≤45 min HARD CAP]
   ─────────────────────────────────────────────────────────────────────────────────────
   ╔═══════════════════════════════════════════════════════════════════════════╗
   ║  RE-INIT FROM THE TEACHER'S LAYERS. EVERY RUN. NEVER FROM YESTERDAY'S      ║
   ║  STUDENT.  ← this line is the entire collapse firewall                     ║
   ╚═══════════════════════════════════════════════════════════════════════════╝
   distilroberta-base (6L/H768/82M), one shared encoder + two 3-class heads.
   L = T² · KL(softmax(z_t/T) ‖ softmax(z_s/T)),  T = 3,  alpha = 0
   Teacher logits recovered from stored probs via log(p): EXACT, by softmax
   shift-invariance: softmax(log(p)/T) ≡ softmax(z/T) for any T.
   Checkpoint every 500 steps to the Volume: GPU work is preemptible BY DEFAULT
   and nonpreemptible=True is UNSUPPORTED for GPU Functions AT ANY PRICE.
                                    │
   06:15 UTC  EVALUATE              ▼                                  [CPU, ~5 min]
   ─────────────────────────────────────────────────────────────────────────────────────
   4 tiers, DUAL-ANCHORED: ge_champion_minus AND ge_baseline_minus.
   Champion RE-SCORED on the SAME frozen slice, never read from stored eval.
                       ┌────────────┴────────────┐
                     PASS                      FAIL
                       │                         │
   06:20 UTC  PROMOTE  ▼                         ▼  keep champion · ping /fail ·
   ─────────────────────────────         alert · discard candidate · cost $0.99
   write r2://students/current.json
   {"run_id":"train:2026-07-15","sha256":"9f2c…","metrics":{…}}
                       │
   ≤5 min later:  serving container polls current.json, verifies sha256, builds a
   ─────────────  NEW ORT session, rebinds. NO REDEPLOY. Rollback = the same write,
                  in reverse, in <5 min.
                       │
                       ▼
              PRODUCTION TRAFFIC
                       │
   ╔═══════════════════╪═══════════════════════════════════════════════════════════╗
   ║  ✗✗✗ THE CYCLE IS DELIBERATELY BROKEN HERE ✗✗✗                                ║
   ║  Student output NEVER re-enters the corpus.                                    ║
   ║  · The gateway persists NO per-document rows (api/internal/store DELETED).     ║
   ║  · The corpus writer accepts only teacher_version ∈ frozen allowlist,          ║
   ║    ENFORCED IN CODE, not by convention.                                        ║
   ║  · There is no code path that loads a student checkpoint as a training seed.   ║
   ╚═══════════════════╪═══════════════════════════════════════════════════════════╝
                       X
```

### Why there is no model collapse here

`day-N student = f(FROZEN teacher, accumulated real human text)`, **not** `f(day-(N-1) student)`.

Shumailov and Mobahi both require a **model→data feedback loop**. Ours has none: the teacher is frozen, sees only fresh human text, and never consumes student output. The cheap defenses are the only ones taken:

1. **Teacher frozen forever**, pinned by commit SHA in `teacher_version`.
2. **Accumulate the corpus** (R2 keeps all partitions; the trainer windows a *view*, it never deletes history).
3. **Re-init from the teacher every run.** `build_student_from_teacher()` takes a *teacher* path; there is no signature that accepts a student.
4. **Never bootstrap the student as tomorrow's teacher.** This is the one tempting optimization (the 82M student is cheap to run) and it is *exactly* Mobahi's setup: "further rounds may lead to under-fitting and thus worse performance."
5. **Content-hash dedupe.** `service.go:353` keys on `d.Platform + "|" + d.Document.ID` in an in-memory, per-request map. Reposts and crossposts silently upweight popular docs.

### The 90-day window does **not** violate "accumulate"

Gerstgrasser's accumulate-vs-replace result concerns **synthetic data replacing real data**. Our corpus is real human text end-to-end; only the *labels* are model-generated, from a permanently frozen model. Windowing is a recency policy over real data. The theorem has no bite.

> **Skeptic correction adopted.** The cost-optimization design justified the window as *"Positive perf impact: recency tracks sentiment drift."* A skeptic refuted this **with the design's own analysis**: the teacher's data ends **December 2021**, so it cannot judge 2026 slang at *any* cadence; and Turc puts the distillation knee near 1M, so 90 days × 10k/day ≈ 900k sits *at* that target: discarding history has a plausible quality **cost**, not a benefit.
>
> **Corrected framing: the window is a COST control with an unmeasured quality effect, defensible on input-distribution coverage.** Validate against the frozen golden set before calling $30.18/mo the steady state. Fallback: 180 days ≈ $60/mo daily, still inside budget.

---

## 4. The crux decisions

### CRUX 1: Go owns the connectors. Modal shells out to a static Go binary.

**Three designs proposed three different answers.** Resolved as follows.

| Option | Verdict |
|---|---|
| **(a) Go CLI, `add_local_file`d into the Modal image, `subprocess.run` from Python** | **ADOPTED** |
| (b) Reimplement HN + RSS in Python inside Modal | **REJECTED** |
| (c) Modal calls the gateway's `POST /internal/harvest` | **REJECTED** |

**Why (a).** The repo already paid for it. `api/go.mod:13` uses `modernc.org/sqlite v1.50.0`, the *pure-Go* SQLite, chosen deliberately, and there is no other CGO dependency in the module. So `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/sentilyzer-harvest` emits a single static binary that runs inside `modal.Image.debian_slim()` with **zero runtime, zero shared libraries, zero bindings**. "Shell out to Go from Python" is normally ugly; here it is a 3-line `subprocess.run` *because a past decision already removed every reason it would be ugly*. `Image.add_local_file(local, remote, *, copy=False)` puts the binary in a **trailing** mount layer, so rebuilding it never invalidates the `pip_install` layer above it.

**Why not (b).** It duplicates the **ToS-compliance surface**, not just seven HTTP clients. Two implementations means two places to encode the legal audit, and the Python one is the one nobody audits. The compile-time `Policy()` method makes the alternative a build error instead.

**Why not (c).** It **inverts the failure domains**: the training pipeline would depend on the always-warm serving gateway being up, and the gateway depends on Modal for inference. A Fly deploy or a gateway restart kills the night's harvest, and the harvest is the one thing that cannot be recovered (`rss.Policy().Backfillable == false`; a missed RSS day is gone permanently). It also puts a multi-hundred-topic fanout on the 512 MB machine whose entire job is p99 latency for paying users, and pays Fly egress to move every document twice across clouds. And it isn't even "reuse what exists": `service.AnalyzeTopic` (`service.go:177`) *runs inference*, so Modal calling it would loop back into Modal, and it returns `domain.Score`s not raw text. A harvest-only RPC is new code either way.

**The objection to (a), answered.** `api/internal/connectors/hackernews.go:48` opens with `if q.Topic == "" { return nil, nil }`: the connectors are topic-scoped *search*, and the corpus needs the *firehose*. **That is an argument for extending the Go connector, not for a Python fork.** `connectors.Query` gains a `Window`, and `HackerNews.Search` learns to run a windowed firehose when `Topic == "" && !Window.IsZero()`. ~40 lines.

---

### CRUX 2: Modal serves inference. The Go process does not.

**A design proposed in-process ONNX inside `sentilyzerd` and was refuted at major severity by two independent skeptics on the same arithmetic.**

The design asserted `shared-cpu-4x ≈ 2 physical cores`, applying the briefing's *Modal* unit (1 physical core = 2 vCPU). **That conversion does not apply to Fly.** Fly's docs: every shared vCPU gets a **5ms baseline quota per 80ms period (6.25% of a physical core)**, and *"Quotas are SHARED between a Machine's vCPUs."* Corroboration from Fly's own pricing page: `performance-1x` is *"roughly 16× more expensive than shared-cpu-1x despite similar baseline specifications"*: **that 16× is the 1/16 ratio.**

| | Design claimed | Reality |
|---|---|---|
| `shared-cpu-4x` sustained | 2 physical cores | **0.25 physical cores** (8× error) |
| Sustained throughput | ~100 seq/s | **~3.2 seq/s** |
| 200-doc `AnalyzeTopic` | "~1-2s" | **~62s once the 500s burst balance drains** |
| Escalation to ~2 sustained cores | `shared-cpu-8x` (+$8) | **`performance-2x` @ ~$64.39/mo `[EST]`** |

`performance-2x` **inverts the entire cost argument**: the in-process path becomes ~7× *more* expensive than Modal serving. And the cost-optimization design independently rejected in-process for a different reason: it imports **ONNX Runtime's cgroup-blindness into the always-warm gateway**. ORT sizes its intra-op pool from the *host's* physical core count, ignores CFS quotas, and `OMP_NUM_THREADS` does **not** control it (official builds ship without OpenMP). Dozens of threads contending inside the process serving REST/XML/GraphQL/gRPC. It also regresses the deliberate CGO-free build.

**Both roads lead to Modal serving.** Configuration:

```
cpu=0.125 · memory=1024 · min_containers=1 · scaledown_window=300 · max_containers=8
INT8 ONNX · onnxruntime + tokenizers + numpy + fastapi (~65 MB image, NO torch)
so.intra_op_num_threads = 2   ← MANDATORY. This fails SILENTLY as bad latency.
```

**The counterfactual this avoids:** a T4-backed student with a 60s `scaledown_window` on bursty traffic bills ~24h/day of mostly-idle GPU ≈ **$425/mo**. CPU serving is **41× cheaper** *and* lets you afford a **longer** scaledown window (better p99), not a shorter one.

**Consequence: a blocker.** Modal **cannot serve gRPC**: its ASGI request layer cannot express HTTP/2 stream IDs, and Tunnels won't translate HTTP/2 either (and pin a live container, killing scale-to-zero). So `api/internal/inference/client.go`'s `GRPCClient` (`:41-60`, `grpc.NewClient` → `pb.InferenceServiceClient`) must be joined by an `HTTPClient` implementing the same `Client` interface (`client.go:19-24`). **`service.go`, `rest.go`, `grpc.go`, `graphql.go` change by zero lines**: `Client` is already an interface and `fake.go` already implements it in-process. The **public** gRPC surface (`api/internal/server/grpc.go`, `main.go:95-96`, `:9090`) is completely unaffected.

---

### CRUX 3: `api/internal/store` is **deleted, not migrated**, and that one deletion is both the compliance fix and the collapse firewall.

`store.go:1-3` claims: *"Postgres can be slotted in by swapping the driver and DSN; the schema is intentionally portable."* **That is false on three counts**, all verified by reading the file: `:27` `sql.Open("sqlite", dsn)`; `:47` `id INTEGER PRIMARY KEY AUTOINCREMENT`; `:81` `VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`. But that undersells it: **nothing survives**. `SaveTopicResults` (`:66`) is the package's *only* method, the table is **write-only** (verified: no `Get`/`Query`/`Select` anywhere), and only two files import it (`main.go:27`, `service.go:20`).

So the deletion is trivially safe, and it does three things at once:

1. **It closes a live retention exposure that is arguably accruing today.** `service.go:233-238` writes a row per document for *every* platform, including YouTube. YouTube §III.E.4.d caps Non-Authorized Data at **30 calendar days**; §III.E.4.h bars **creating derived data**, and a sentiment label *is* derived data. Rows older than 30 days exist in `analyses` right now.
2. **It removes the only durable path by which a training-ineligible platform could reach storage.** After this, the *only* place third-party text lives in the gateway is `cache.TTLCache`: in-memory, ephemeral, bounded by `SENTILYZER_CACHE_TTL` (default `10m` per `config.go:72`), well inside every retention rule including Reddit's 48-hour recommendation.
3. **It is the collapse firewall.** Once the student serves production, `SaveTopicResults` fills `analyses` with **student** labels. If the corpus ever read `analyses`, day-N would train on day-(N-1)'s output, Mobahi's exact setup. Deleting the write path makes that recursion *unconstructible*.

> **Skeptic correction adopted.** The training-loop design's §0 called `store.go:46`'s missing `text` column a *"SHIP-STOPPER… every day of delay is irrecoverable training data"* and proposed adding `text`, `probs_json`, `teacher_version`. A skeptic refuted the columns as **vestigial** (the design's own §7 establishes the trainer never reads `analyses`) and noted `text` there *creates* the retention liability. **The urgency is real but transfers to a different target: ship the harvester.** Nothing is accumulating until it exists.

`api/go.mod:13` loses `modernc.org/sqlite v1.50.0`, cascading out `modernc.org/{libc v1.72.0, mathutil v1.7.1, memory v1.11.0}` plus `ncruces/go-strftime`, `dustin/go-humanize`, `remyoudompheng/bigfft`. The CGO-free property is preserved via **pgx** (pure Go) when the time series lands in Phase 7.

**The one rule, enforced at compile time:**

```go
// api/internal/connectors/connector.go
type Policy struct {
	// Durable reports whether content from this platform (or anything derived
	// from it) may be written to durable storage (the R2 training corpus, or a
	// persisted daily aggregate). False does NOT disable the connector: the
	// interactive AnalyzeTopic path serves from every ENABLED connector and
	// persists nothing. It means: nothing from this source outlives the 10m cache.
	//
	// This is a METHOD ON THE INTERFACE, not a config flag and not a
	// `WHERE platform NOT IN (...)`. A flag rots the first time someone adds a
	// connector at 03:00 six months from now. A method is a compile error.
	Durable bool
	// Backfillable reports whether Search() can target a historical window.
	// RSS exposes only "now": a missed RSS day is gone permanently, and the
	// catchup loop must not pretend otherwise.
	Backfillable bool
	// Reason explains why Durable is false. Empty when Durable is true.
	// It exists so the decision gets re-argued on its merits rather than
	// quietly flipped.
	Reason string
}

type Connector interface {
	ID() string
	DisplayName() string
	Enabled() (bool, string)
	Policy() Policy           // ← NEW: breaks all 8 connectors, on purpose
	Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error)
}
```

The clearest proof this must be compile-time: `api/internal/connectors/mock.go` returns deterministic synthetic templates. If `mock` ever leaked into the corpus, the student would distill on generated text. `factory.go:26-28` registers it whenever `SENTILYZER_USE_MOCK=true`.

---

### CRUX 4: The trainer is bounded by **wall clock**, not epochs.

**Refuted at FATAL severity by a scheduling skeptic**, and independently at major by two more.

The training design specified a 90-day window with 2-3 epochs and `timeout=7200`. Under that spec, with the design's own commitments (re-init from the frozen teacher every run + accumulate the corpus), the training set grows **linearly with time and the run grows with it**. At ~200 seq/s on a T4, `timeout=7200` is exceeded at ~960k examples, *inside month 2*, and thereafter the trainer **times out every single night, forever**, burning a full billed GPU-hour and producing nothing. Worse, `resume` was keyed per-*date* (`version = f"student-{d}"`), so a run that dies at 2h leaves a checkpoint keyed to yesterday; tomorrow's run gets a fresh version and restarts from zero. There is no self-heal.

**Adopted:**

```python
MAX_TRAIN_SECONDS   = 2_700      # 45 min. $0.99284/run. $30.18/mo daily. FOREVER.
MAX_TRAIN_SEQUENCES = 1_840_000  # asserted on CPU, BEFORE the GPU container spawns
CORPUS_WINDOW_DAYS  = 90         # a PROXY. The SEQUENCE cap is the invariant.
CORPUS_RESERVOIR    = 20_000     # stratified; prevents rare-class forgetting

# Train to the CLOCK, not to an epoch count. Sample the window to
# MAX_TRAIN_SEQUENCES rather than sweeping epochs. If the corpus grows,
# epochs fall. The bill does not.
# Checkpoint-and-exit-PARTIAL at the budget. NEVER hit FunctionTimeoutError.
# Key the checkpoint on the CANDIDATE, not the date, so a preempted run is
# RESUMED by the next attempt instead of restarted from zero.
```

**The window must be expressed in DOCUMENTS, not days.** A day-cap silently tracks the harvest rate: add 200 RSS feeds and the corpus triples with no config change.

**What this is worth:**

| Corpus | Timeline | Daily trainer $/mo |
|---|---|---|
| 300k | month 1 | $15.68 |
| **920k** | **90-day steady state** | **$30.18** ← pinned forever |
| 3.65M (uncapped) | **year 1** | **$115.73** |
| 11M (uncapped) | **year 3** | **$335.01** |

**$85.55/mo in year one, arriving silently in month ~6.** This single line is the difference between $19.30/mo forever and $105/mo by year one.

---

### CRUX 5: R2 is the **single** model-delivery mechanism, and there are no memory snapshots.

Three proposals conflicted: a Modal Volume + `current.json`; an image-baked `add_local_file`; and R2 polled by Go.

- The **Volume pointer was refuted at major severity**: verified Modal semantics are *"Containers mount latest state at creation; later external commits are invisible until `.reload()`."* With `min_containers=1` the serving container mounts once and lives indefinitely, so the trainer's write is invisible and the mtime poll **never fires**. Promotion silently no-ops; **rollback silently no-ops**: it reports success while production serves the bad model, and `min_containers=1` *maximises* the blast radius. Fixable with `weights.reload()`, but…
- The **image-baked variant was refuted at major severity** for contradicting its own promote-by-pointer lever: baked weights make promotion *definitionally* a redeploy.

**Resolution: R2, because the `sentilyzer[local]` pip extra can only reach R2 anyway.** Modal Volumes have **no external HTTP access**: Python SDK, Modal CLI, or inside-Modal execution only. One pointer, two consumers, one mechanism. The Volume keeps only what it is genuinely good for: teacher weights and trainer checkpoints (preemption resilience, write-once/read-many, never externally read).

**And no memory snapshots.** Every argument points the same way, from the briefing's own text:
- *"Snapshots do not speed up model loading from storage"* and *"may even worsen"* it, and our load **is** storage-bound (80 MB from R2).
- *"Snapshots are only created for DEPLOYED apps and are invalidated by EVERY redeploy"*, and CPU functions need **~6 snapshots** for coverage: fatal at any iteration cadence.
- The ONNX image has **no torch**, so there is almost no import cost to amortise.
- Deleting a Volume file used during restore causes **restore failures**, a class of trap a pruning trainer eventually springs.
- Snapshot restore replays captured RNG state; and `torch.cuda.is_available()` inside a CPU snapshot triggers CUDA-init-with-zero-devices and breaks it.
- **The briefing's own verdict:** *"min_containers=1 is what actually papers over this, not snapshots."*

Skipping them costs ~nothing and makes promotion a clean pointer write.

> **Note on a bug we sidestep.** A skeptic found a concrete **use-after-free across CGO** in the Go in-process swap path (`eng` captured, `RUnlock()`, then `eng.tok.EncodeWithOptions(...)` while `Swap()` calls `prev.tok.Close()`). It would segfault the *entire gateway* (REST, gRPC, GraphQL) on the daily promotion path. In Python there is no manual free and attribute rebinding is atomic under the GIL; the correct pattern (read `sess = self._sess` once at the top of the handler) is trivially safe. This is another reason the swap lives in Modal, not in `sentilyzerd`.

---

## 5. Cost model

### Unit rates (all verified in the briefing)

| Resource | Verified rate | Derived $/hr | Derived $/month (730h) |
|---|---|---|---|
| CPU (**physical core = 2 vCPU**) | $0.0000131/core/s | $0.04716 | **$34.43/core-month** |
| Memory | $0.00000222/GiB/s | $0.007992 | **$5.83/GiB-month** |
| T4 | $0.000164/s | $0.5904 | - |
| L4 | $0.000222/s | $0.7992 | - |
| A10 | $0.000306/s | $1.1016 | - |
| A100-80GB | $0.000694/s | $2.4984 | - |
| Modal Volume | $0.09/GiB/mo | - | first **1 TiB/mo free** |
| Modal Starter | $0 base | - | **+$30/mo credits** |

`0.0000131 × 3600 = 0.04716`; `× 730 = 34.43`. **GPU rates are the GPU alone**: every GPU container additionally bills CPU (min 0.125 cores) and memory.

| Composite shape | Arithmetic | $/hr |
|---|---|---|
| T4 + 2 cores + 8 GiB | `0.5904 + 0.09432 + 0.063936` | **$0.74866** |
| A10 + 2 cores + 16 GiB | `1.1016 + 0.09432 + 0.127872` | **$1.32379** |
| 2 cores + 8 GiB (no GPU) | `0.09432 + 0.063936` | **$0.15826** |
| 1 core + 2 GiB | `0.04716 + 0.015984` | **$0.06314** |
| 0.25 core + 0.5 GiB | `0.01179 + 0.003996` | **$0.01579** |

### The bill

| Component | Unit rate | Usage | **$/mo** |
|---|---|---|---|
| Modal teacher labeling | $0.74866/hr | 176 s/day × 30.4 | **$1.11** `[EST]` |
| Modal student training | $1.32379/hr | **2,700 s/day** × 30.4 | **$30.18** |
| Modal `prep_corpus` | $0.15826/hr | 10 min/day × 30.4 | **$0.80** |
| Modal `harvest` | $0.06314/hr | 9 min/day × 30.4 | **$0.29** |
| Modal orchestrator | $0.01579/hr | 60 min/day × 30.4 | **$0.48** |
| Modal student serving | $34.43/core-mo + $5.83/GiB-mo | 0.125 core + **1.0 GiB**, warm 24/7 | **$10.26** `[EST]` |
| Modal Volume | 1 TiB free | ~3 GB | **$0.00** |
| **Modal gross** | | | **$43.12** |
| **less Starter credit** | | | **−$30.00** |
| **Modal out of pocket** | | | **$13.12** |
| Fly.io `shared-cpu-1x`/512MB | $3.32 | 1 machine, always warm | **$3.32** |
| Fly.io egress | $0.02/GB (NA/EU) | ~10 GB/mo | **$0.20** |
| Fly.io TLS + shared anycast IPv4 | first 10 certs/org free | 3 certs | **$0.00** |
| Neon Postgres (Launch) | $0.106/CU-hr + $0.35/GB-mo | 15.2 CU-hr + 0.15 GB | **$1.66** |
| Cloudflare R2 | $0.015/GB-mo, 10 GB free; **egress $0.00** | 2.3 GB, ~150 Class A, ~1M Class B | **$0.00** |
| Monitoring (healthchecks + Axiom + Better Stack) | free tiers | 3 checks, ~2 GB logs, 2 monitors | **$0.00** |
| Docs + SDK + CI (Pages, PyPI, BSR, Actions) | free tiers | ~30 builds/mo | **$0.00** |
| `.dev` domain (Cloudflare Registrar, at-cost) | ~$13/yr | 1 domain, 3 subdomains | **$1.00** `[EST]` |
| | | | |
| **TOTAL OUT OF POCKET** | | | **$19.30/mo** |

**Against $50-100: 2.6-5× headroom.**

> **This is not the "$4.94-9.42/mo, ~10× headroom" the source designs claimed.** The cost-realism skeptic (*"'~10× headroom' should read '~2-4× headroom'"*) was confirmed by independent recompute. **Every architectural decision survives; only the point estimate moves.** Where the correction bit:
> - Teacher `32s → 176s`: 32s implies ~62% MFU on T4 **fp32**, an fp16-shaped number in an fp32 design.
> - Trainer **20% MFU, not 30%**, plus ~600s of boot/import/tokenize/ONNX-export/eval that *no* design costed.
> - Serving **1.0 GiB, not 0.3-0.5 GiB**, and `serve_img` was missing `fastapi`, which `@modal.fastapi_endpoint` requires.
> - `prep_corpus` **moved off the A10** (saves $5.91/mo). `harvest` had **no line item at all**.
> - Neon row size **~300 B, not 120 B** (`teacher_version` stored inline as TEXT is ~95 bytes/row) → free tier is **~3 years, not a decade**.
> - Model artifact **~80 MB, not 23 MB**: 23 MB is MiniLM's, and we chose distilroberta.
> - `.dev` domain: the SDK design claimed "$0.00 net new" while hardcoding `api.sentilyzer.dev`.

### Sensitivity band

| Scenario | Deltas | Total |
|---|---|---|
| **Optimistic** | idle CPU not billed at floor (−$4.30); boot seconds not billed (−$0.57); 30% MFU so 30 min suffices (−$10.06) → Modal gross $28.19, **fully inside the credit** | **$6.18/mo** |
| **Expected** | as modelled | **$19.30/mo** |
| **Hostile** | serving RSS 2 GiB (+$5.83); Neon un-snapshotted (+$17.74); 2 Fly machines (+$3.32); unpruned R2 at yr 3 (+$1.16) | **$47.35/mo** |

**Even the hostile stack fits.** That is the real headline: the architecture is robust to every assumption being wrong *simultaneously*.

### Where the free tiers run out

| Traffic | What breaks | Fix | Cost |
|---|---|---|---|
| ~0.1 req/s | nothing | - | $19.30 |
| **~1 req/s** | **Neon Free 100 CU-hr**: any un-snapshotted read resets the 5-min autosuspend → 0.25 CU × 730h = **182.5 CU-hr** → **project SUSPENDED until next billing month** | snapshot **all** read paths; Launch | +$0-19.35 |
| **~2.8 uncached fanouts/s** | **HackerNews 10,000 req/hr/IP: HARD WALL, NOT A COST.** `2.8 × 3600 = 10,080`. `AnalyzeTopic` cannot scale past this at any price. | distributed cache; pre-computed tracked-topic path | $0 (engineering) |
| ~5 req/s | **Fly burst balance**: 500s drains in ~533s of full burst, then a **hard drop to 6.25% of one core**. Degrades HTTP handlers, connector I/O, **and the health check together** → Fly kills the machine → restart → cold. **Silent.** | `performance-1x` | +~$28 `[EST]` |
| ~10 req/s | Modal $30 credit | pay | +$60/mo |
| ~200 req/s | Axiom 500 GB/mo | Axiom Cloud | +$25/mo |
| ~600 req/s | Modal Starter 100-container cap | Team ($150/mo net), the **only** thing that ever justifies it | +$150/mo |
| never | **R2 egress** | there is none | $0 |

**The most important line is HackerNews at 2.8 fanouts/s.** It is not a cost limit and no amount of money moves it. It is also the point where `api/internal/cache`'s per-process LRU stops being a latency optimization and becomes a correctness requirement.

---

## 6. Training frequency: the direct answer

### **Yes. Daily training costs $30.18/mo gross, $13.12/mo out of pocket, and you should pay it.**

Per-run cost is **constant** under the wall-clock budget: `2,700 s = 0.75 hr × $1.32379/hr = $0.99284/run`. Frequency multiplies it linearly.

| Frequency | Runs/mo | Trainer $/mo | Δ vs weekly | Modal gross | After credit | **TOTAL SYSTEM** | vs $50-100 |
|---|---|---|---|---|---|---|---|
| Weekly | 4.33 | $4.30 | - | $17.24 | $0.00 | **$6.18/mo** | 8-16× headroom |
| **DAILY** ← ship this | 30.4 | **$30.18** | **+$25.88** | $43.12 | **$13.12** | **$19.30/mo** | **2.6-5× headroom** |
| 2× daily | 60.8 | $60.36 | +$56.06 | $73.30 | $43.30 | **$49.48/mo** | 1.0-2×, at the edge |
| Hourly | 730 | $724.77 | +$720.47 | $737.71 | $707.71 | **$713.89/mo** | **7-14× OVER** |

### The sharpest statement of the answer

```
Modal Starter credit                                        $30.00
less non-trainer Modal:
  teacher $1.11 + prep $0.80 + harvest $0.29
  + orchestrator $0.48 + serving $10.26                    −$12.94
                                                           ───────
Remaining for the trainer                                   $17.06
÷ $0.99284/run                                          = 17.2 runs

Runs 18-30.4    =  13.2 runs × $0.99284                   = $13.11   ✓
```

> **The ONLY thing you pay Modal for is training frequency itself.** Teacher labeling, corpus prep, harvesting, orchestration, warm CPU serving, and *all storage* are collectively **$12.94/mo (43% of the credit)**. The other 57% buys **17 free training runs**. Run 18 onward costs **$0.99 each**.
>
> **$1 of Modal spend = ~1 daily training run.**

### Three things are simultaneously true, and the designs oversold one of them

1. **Daily training is the single largest line item in the entire system.** At $30.18/mo it is **70% of gross Modal spend** and **1.6× everything else combined**. It is not a rounding error. The cost-optimization design claimed weekly→daily costs **$8.17/mo** and concluded *"the tension you assumed does not exist… ~10× headroom."* With 20% MFU and the ~600s of unbudgeted GPU-side overhead, the true marginal is **$25.88/mo, 3.2× higher**.
2. **It fits comfortably anyway.** $19.30/mo against $50-100.
3. **The corrected numbers change the *ceiling*, not the *decision*.** 2×-daily now costs **$49.48/mo** and consumes the entire low end of the budget (the design claimed $19.08). Both were "affordable" in the optimistic model; only daily still is.

### And frequency was never the risk anyway

**Cost = frequency × per-run cost, and per-run cost = corpus × epochs *unless you cap it*.** See CRUX 4: uncapped, the trainer reaches **$115.73/mo by year 1** and **$335/mo by year 3**, with no config change and no warning. The wall-clock budget is what pins it at $30.18/mo forever.

### Skip 2×-daily; never go hourly, and not for cost reasons

The corpus grows ~10k/day = **3.3%/day**, so an hourly retrain sees a corpus **0.139% different**, below run-to-run gradient noise. And the ceiling is fixed regardless: **the teacher's training data ends December 2021**. Retraining refreshes which **inputs** the student has seen (topics, tickers, entities), never the teacher's judgment on 2026 slang. **The honest pitch for daily retraining is input-distribution coverage, not accuracy drift.** If 2026 language quality is the goal, the lever is a **newer teacher**, not a faster cron.

There is one argument the designs didn't make that I'll make: **a daily loop is its own reliability mechanism.** A pipeline green for 60 consecutive days is trustworthy; a quarterly one is a coin flip. It also buys optionality: swapping the teacher becomes a next-morning change instead of a project.

---

## 7. The daily scheduled system

**Two crons of Starter's five.** Not six chained by wall clock. That isn't a quota workaround, it's a correctness fix: "label at 05:00" runs on yesterday's partition and **reports success** when harvest fails. Calling `label.remote(logical_date)` from an orchestrator makes it a **data dependency the runtime enforces**.

### What shipped: `ml/sentilyzer_ml/pipeline/modal_app.py` (verified 2026-08-10)

The trainer that actually shipped is a self-service app, **`sentilyzer-train`**, not the scheduled `sentilyzer-pipeline` sketched below: nothing has a schedule (an untriggered month costs $0), the corpus is the free HackerNews archive mirror streamed straight into a Modal Volume, and neither R2 nor Neon is in the loop. Where the planned tables in this section disagree with this list, the shipped values govern.

- **Invocation.** `modal run --detach sentilyzer_ml/pipeline/modal_app.py::main` (`::main` is required: the file has several entrypoints, and Modal won't guess which one you mean). `--detach` matters: without it the run's lifetime is tied to your laptop's heartbeats. Recovery entrypoints: `::runs` lists runs on the Volume, `::fetch --run-id ... --output ...` downloads a run's artifact, `::unlock` clears a stranded run lock.
- **Ingest.** Per-month Volume commits, so a timeout keeps the finished months. A `--limit` smoke ingest writes `.partial` part files that `month_done()` deliberately ignores: an unlimited rerun re-ingests the month in full and removes the truncated files.
- **Labeling.** T4, fp32 teacher, batch 256, work ordered by text length so each batch pads to similar lengths. Labels are incremental, keyed `(platform, doc_id, teacher_version)`, and flush to the Volume every 50k rows with a commit per flush. `label()` gets `timeout=14_400` (4 hours) and keeps `retries=0`, but `run_pipeline` catches `FunctionTimeoutError` and retries it once: the retry is monotonic precisely because progress flushes.
- **Training.** The device rides `DistillConfig` end to end (`"cuda"` on Modal), TF32 is enabled, batch 64. BOTH GPU functions raise if torch sees no CUDA device (the first full run burned its 45 minutes doing 240 steps on CPU). Checkpoints save CPU and CUDA RNG state, so a resumed run reproduces an uninterrupted one. Memory reservations are explicit: prep 8 GiB, label 4 GiB, train 8 GiB. `run_pipeline` runs under `timeout=8 * 3600`.
- **Volume semantics.** Every stage that reads another stage's output calls `volume.reload()` first: Modal containers otherwise see the Volume as of container start, and a preemption reshuffle once handed `train` a container whose view predated `prep`'s commit.
- **The run lock.** The lock stores the orchestrator's `modal.current_input_id()`. A preemption-restarted `run_pipeline` recognizes the lock as its own previous life and resumes the same `run_id` (prep short-circuits by returning its committed manifest) instead of refusing on its own lock.
- **First gate-passing run, for the record: `train:2026-08-10-221206`.** Corpus 4.15M HN docs (2025-05..2026-07) capped to 1,840,000 sequences; teacher agreement 0.8864; class recalls 0.89/0.90/0.85. A clean full run costs roughly $1.5-2 of Modal credit; repeat runs with cached labels come in well under $1.

### `ml/pipeline/modal_app.py`

```python
"""Sentilyzer distillation pipeline on Modal. All verified-current 1.0 syntax."""
import modal

app = modal.App("sentilyzer-pipeline")          # modal.Stub raises AttributeError at >=1.0
weights = modal.Volume.from_name("sentilyzer-weights", create_if_missing=True)
WEIGHTS_DIR = "/weights"
HARVEST_BIN = "/opt/sentilyzer/harvest"

# ── THE FIVE CONSTANTS THAT BOUND THE BILL ──────────────────────────────────────
MAX_CATCHUP_DAYS    = 7          # a NULL watermark read as 1970-01-01 = 20,000 days
                                 # of GPU labeling ≈ $732 IN ONE RUN. One line.
MAX_DOCS_PER_RUN    = 60_000     # asserted on CPU, BEFORE any GPU container spawns
MAX_TRAIN_SECONDS   = 2_700      # 45 min. $0.99284/run. $30.18/mo daily. FOREVER.
MAX_TRAIN_SEQUENCES = 1_840_000  # the REAL invariant; days are only a proxy
CORPUS_WINDOW_DAYS  = 90
# ────────────────────────────────────────────────────────────────────────────────

TEACHERS = frozenset({                        # the collapse-firewall allowlist
    "cardiffnlp/twitter-roberta-base-sentiment-latest@<sha>",
    "yangheng/deberta-v3-base-absa-v1.1@<sha>",
})
secrets = [modal.Secret.from_name("sentilyzer-pipeline")]  # NEON_DSN, R2_*, HC_PING_KEY,
                                                           # GIT_SHA, PIPELINE_DISABLED,
                                                           # AUTO_PROMOTE

# Layer order STABLE -> VOLATILE: changing any layer rebuilds every layer after it,
# so a code edit must never re-download torch.
_base = (modal.Image.debian_slim(python_version="3.11")     # onnxruntime 1.27 needs >=3.11
         .pip_install("duckdb==1.4.2", "pyarrow==22.0.0", "boto3==1.40.11",
                      "psycopg[binary]==3.2.10", "httpx==0.28.1"))

cpu_image = _base.add_local_python_source("pipeline")   # automounting ENFORCED-REMOVED at 1.0

harvest_image = (_base
    # copy=False (the default) => trailing mount layer: rebuilding the Go binary
    # never invalidates the pip layer above it.
    .add_local_file("../bin/sentilyzer-harvest-linux-amd64", HARVEST_BIN)
    .add_local_python_source("pipeline"))

gpu_image = (_base
    .pip_install("torch==2.9.1",
                 "transformers>=4.57,<5",   # NOT v5. Its from_pretrained dtype default
                                            # became "auto": the teachers would silently
                                            # load in fp16/bf16 and perturb the soft labels
                                            # the WHOLE student distills from. Silent.
                 "sentencepiece==0.2.1",    # DeBERTa-v3 tokenizer
                 "optimum-onnx[onnxruntime]==0.1.0", "onnxruntime==1.27.0")
    .add_local_python_source("pipeline")
    .add_local_python_source("sentilyzer_ml"))   # reuse the repo's TransformerBackend
```

### The orchestrator

```python
@app.function(
    image=cpu_image,
    # Cron, NOT Period. Period "will run X hours after this most recent
    # deployment", and you WILL be deploying often, so Period(days=1) could
    # starve forever. Modal's docs recommend Cron as "not disturbed by deploys".
    schedule=modal.Cron("15 4 * * *", timezone="UTC"),
    # .remote() BLOCKS. This MUST exceed the WORST case of what it awaits:
    #   3 catchup × (1800 harvest + 900 prep + 3600 label) + 3600 train + 1800 eval
    #   = 24,300s. 50,400 is 2× headroom. (scheduling skeptic: the source design
    #   set 21,600 against its own stated bound of 46,800. It would die on
    #   exactly the backfill path the catchup loop exists for.)
    # Cost: 0.25 core for the duration => ~$0.24 even at 14h. Timeout is a
    # ceiling, not a bill.
    timeout=50_400,
    max_containers=1,   # DEFAULT IS None = UNBOUNDED. Also serializes runs.
    retries=0,          # NEVER retry the entrypoint: modal.Retries re-fires the
                        # whole function, re-pinging /start and emitting /fail on
                        # attempt 1 even when attempt 2 succeeds.
    cpu=(0.25, 1.0), memory=512, secrets=secrets,
)
def daily_pipeline(logical_date: str | None = None, force: bool = False) -> dict:
    # Modal schedules CANNOT be paused ("the schedule should be removed and the App
    # redeployed"), so the kill switch is an env var on the Secret.
    if os.environ.get("PIPELINE_DISABLED") == "1":
        return {"skipped": "kill_switch"}

    # EXPLICIT BACKLOG, drained oldest-first, NOT a sliding `today - 7d` window.
    # A sliding window silently drops dates that age off the edge, and HN IS
    # backfillable, so that loss was avoidable. (scheduling skeptic, major)
    dates = ([date.fromisoformat(logical_date)] if logical_date
             else control.pending_dates("harvest", limit=3, max_age_days=MAX_CATCHUP_DAYS))

    m = {"pipeline_run": str(uuid.uuid4()),
         "code_version": os.environ.get("GIT_SHA", "unknown"),
         "dates": [d.isoformat() for d in dates], "stages": []}

    with heartbeat.check("sentilyzer-daily") as hb:
        hb.body = m
        for d in dates:
            # Sequential, not clock-chained. label CANNOT run on a date whose
            # harvest didn't finish: the runtime enforces the ordering.
            m["stages"] += [harvest.remote(d.isoformat(), force),
                            prep_corpus.remote(d.isoformat(), force),
                            label.remote(d.isoformat(), force)]

        gate = control.training_gate(date.today())
        m["train_gate"] = gate
        if not gate["ok"]:
            # Harvest SUCCEEDED: the data is safe. We decline to distill on a
            # skewed/quiet day. The asymmetry is deliberate: NEVER skip harvest
            # (data is perishable); freely skip train.
            return m

        m["train"] = train.remote(f"train:{date.today().isoformat()}", force)
        m["eval"]  = evaluate.remote(m["train"]["run_id"], date.today().isoformat())
        if m["eval"]["promote"] and os.environ.get("AUTO_PROMOTE") == "1":
            m["promote"] = promote.remote(m["train"]["run_id"], m["eval"])
        # AUTO_PROMOTE defaults OFF. See Phase 6.
    return m


@app.function(image=cpu_image, schedule=modal.Cron("30 5 * * 0", timezone="UTC"),
              timeout=1800, max_containers=1, cpu=(0.5, 2.0),
              volumes={WEIGHTS_DIR: weights}, secrets=secrets)
def prune() -> dict:
    """Weekly. Refresh the reservoir; prune R2 revisions; expire text."""
    with heartbeat.check("sentilyzer-prune") as hb:
        hb.body = {
            # The reservoir is MATERIALIZED weekly, not re-sorted nightly. A
            # nightly ROW_NUMBER() OVER (PARTITION BY label ORDER BY hash) over
            # ALL history is the one unbounded thing in the design.
            "reservoir_rows": corpus.rebuild_reservoir(n=20_000),
            # Keep last 10 revisions + 12 monthly archives, min_age_days=30.
            # Unpruned: 365 × 80 MB = 29.2 GB/yr → $0.29/mo yr1, $2.04/mo yr5.
            "student_revisions_dropped": corpus.prune_students(keep=10, min_age_days=30),
            # Drop document TEXT at 180d; KEEP the label tuple forever (bytes,
            # and it enables audit/rebuild). This is a POLICY decision, not a
            # cost one: storage is ~500 MB/yr.
            "text_partitions_dropped": corpus.drop_documents(older_than_days=180),
        }
    return hb.body
```

### Every job, with its guards

| Job | Shape | `timeout` | `retries` | `max_containers` | Idempotency key |
|---|---|---|---|---|---|
| `daily_pipeline` | 0.25c/0.5 GiB | 50,400 | **0** | **1** | `UNIQUE(kind, logical_date)` |
| `harvest` | 1c/2 GiB | 1,800 | **2** ← the only retry | 2 | **whole-partition overwrite** |
| `prep_corpus` | 2c/8 GiB | 900 | 0 | 1 | deterministic output path |
| `label` | **T4** + 2c/8 GiB | 3,600 | **0** | 2 | anti-join on `labels/` |
| `train` | **A10** + 2c/16 GiB | 3,600 | **0** | **1** | `runs.run_id` claim + resume |
| `evaluate` | 2c/4 GiB | 1,800 | 0 | 1 | pure function of `run_id` |
| `promote` | 0.25c | 600 | 0 | 1 | pointer write (last-write-wins, 1 writer) |
| `prune` | 0.5c/2 GiB | 1,800 | 0 | 1 | reference-counted |

*(Planned shapes. The shipped app's guards differ where the "What shipped" list above says so: `label` at 4 h / 4 GiB with one orchestrator-level retry on `FunctionTimeoutError`, `train` at 8 GiB, `run_pipeline` at 8 h.)*

**Why retries are asymmetric.** `timeout` is **PER EXECUTION ATTEMPT**: *"Functions configured with modal.Retries will start new execution timeouts on each retry."* So `retries=3, timeout=3600, gpu="A10"` is up to **4 × $1.32379 = $5.30 from one cron tick**. Rule: **retry cheap CPU stages (harvest: flaky networks, `3 × 0.5h × $0.06314 = $0.09`); never retry GPU stages, make them resumable and let tomorrow's catchup retry them.** Note container *crashes* (OOM, preemption) are auto-rescheduled independently of `retries=`; `retries=` is for **your code raising**.

**Why `retries=0` on the trainer specifically.** GPU work is preemptible **by default** and `nonpreemptible=True` is *"not supported for GPU Functions"*: the headline GPU rate **is** the preemptible rate. There is no cheaper spot tier *and no premium tier to buy up to*. The answer is checkpoint-to-Volume + resume, keyed on the **candidate**, not the date.

### Idempotency: one deterministic key, three enforcement layers

```sql
CREATE TABLE runs (
  run_id       TEXT PRIMARY KEY,   -- DETERMINISTIC: 'harvest:2026-07-15'
  kind         TEXT NOT NULL,      -- harvest|prep|label|train|evaluate|promote|prune
  logical_date DATE NOT NULL,
  attempt      INT  NOT NULL DEFAULT 1,
  status       TEXT NOT NULL,      -- pending|running|success|partial|failed|skipped
  started_at   TIMESTAMPTZ, finished_at TIMESTAMPTZ,
  docs_fetched INT DEFAULT 0, docs_labeled INT DEFAULT 0,
  code_version TEXT, error TEXT, manifest JSONB,
  UNIQUE (kind, logical_date)      -- a double-fired cron hits a CONSTRAINT
                                   -- VIOLATION instead of duplicating a day
);
```
1. **R2**: whole-partition overwrite. A re-run *replaces* `dt=<date>/platform=<id>/`.
2. **Neon**: `UNIQUE(kind, logical_date)` + a claim statement whose `WHERE runs.status <> 'success' OR %(force)s` is simultaneously the double-fire guard and the `--force` re-run switch.
3. **Labels**: `ON CONFLICT DO NOTHING` per `(platform, document_id, teacher_version)` → a re-run costs **~0 GPU-seconds**. This is what makes the stage *cheaply* idempotent, not just *correctly* idempotent.

> **The R2 partition rewrite is not atomic.** Write to `dt=<date>/platform=<id>/_tmp-<run_id>/`, then copy/rename, or include `run_id` in the filename and have the trainer `SELECT max(run_id)` per partition. Otherwise the trainer can scan a half-written partition.

### `run_sources`: the fix for `service.go`'s discarded errors

**Verified bug** (`api/internal/service/service.go:314-326`): `fanout` collects connector errors into `errs` and returns success if *any* connector worked (`:323`), then **throws `errs` away**. A run where 6 of 7 connectors died is **byte-identical to a healthy run**. Tolerable for an interactive API; **dangerous for a training pipeline**, because a day where RSS's feed list broke yields a corpus that is 100% HackerNews and trains on that skew with no signal.

```go
// api/internal/connectors/fanout.go  (NEW)
type Outcome struct {
	Platform  string `json:"platform"`
	Status    string `json:"status"`  // ok|failed|skipped|not_durable
	Docs      int    `json:"docs"`
	Err       string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

func Fanout(ctx context.Context, cs []Connector, q Query) ([]domain.SourcedDocument, []Outcome)
```
`service.fanout` becomes a thin adapter that keeps today's semantics but `slog.Warn`s each failure. The harvest binary writes them to `run_sources`; Modal reads them.

### The gates (all must pass)

```python
GATES = {
  # ── T1 DATA SANITY: evaluated on CPU, BEFORE any GPU container spawns ──────
  "n_new_docs":          ("ge_absolute", 2_000),
  "corpus_vs_7d_median": ("ge_absolute", 0.5),
  "max_platform_share":  ("le_absolute", 0.95),
  #  ^ calibrated for a TWO-source corpus where HN ≈ 80% is NORMAL. This is a
  #    "did a source vanish entirely" tripwire, not the 0.60 you'd use with 7
  #    sources. Source-level failures are surfaced by run_sources + an alert
  #    (3 consecutive failed days), NOT by blocking the run.

  # ── T2 FIDELITY: a FROZEN held-out slice ──────────────────────────────────
  #  A fixed doc-ID manifest, sampled ONCE, never re-sampled, excluded from every
  #  training window forever. The CHAMPION IS RE-SCORED ON THE SAME SLICE EACH RUN.
  #  NEVER compare against current.json's stored eval: it was measured on a
  #  different day's slice, and the 0.01 band is far smaller than slice-to-slice
  #  noise. (training-loop skeptic, major)
  "agree_doc":    [("ge_champion_minus", 0.01), ("ge_baseline_minus", 0.02)],
  "agree_aspect": [("ge_champion_minus", 0.015), ("ge_baseline_minus", 0.03)],
  #  ^^ THE ANTI-RATCHET. A rolling champion alone is a DOWNWARD RANDOM WALK with
  #     no floor: 180 daily promotions each ≤0.01 worse is unbounded cumulative
  #     decay while every gate passes every day. Worse, "re-init from teacher every
  #     run" makes consecutive students INDEPENDENT STOCHASTIC DRAWS with ±0.005-0.01
  #     seed variance, at or above the gate epsilon, so ~half of draws are downward
  #     steps promoted by noise. baseline_* are FROZEN CONSTANTS IN GIT, bumped only
  #     by explicit human commit.

  # ── T3 GROUND TRUTH: the only thing that catches TEACHER drift ─────────────
  "golden_hn_macro_f1":  ("ge_baseline_minus", 0.02),   # ← THE REAL BAR
  "tweeteval_macro_f1":  ("ge_baseline_minus", 0.03),   # relative TRIPWIRE only
  #  ^ SUBTLE AND IMPORTANT: our transfer set is HN/RSS; TweetEval's test set is
  #    tweets. Turc measured that at transfer/task domain correlation S=0.52,
  #    "distillation on D_T is 1.8% worse than basic training." A HEALTHY student
  #    distilled on HN WILL score below the teacher's ~72.6, from the DOMAIN GAP,
  #    not from being broken. So TweetEval is a drift tripwire; its floor is
  #    calibrated on student #1, NEVER on the teacher's number.
  #    And the tempting "fix" (padding the corpus with public tweet corpora) is
  #    EXACTLY what Turc forbids: off-domain transfer data makes distillation
  #    WORSE THAN NOT DISTILLING.

  # ── T4 PER-CLASS RECALL FLOOR ──────────────────────────────────────────────
  "recall_negative": ("ge_absolute", 0.45),
  "recall_neutral":  ("ge_absolute", 0.45),   # ← neutral collapse is THE classic
  "recall_positive": ("ge_absolute", 0.45),   #   3-class distillation failure and
                                              #   aggregate macro-F1 HIDES IT

  # ── T5 SERVING BUDGET: measured on CPU, in a SEPARATE function ────────────
  "p99_cpu_latency_ms": ("le_absolute", 150.0),   # do NOT benchmark this on the
  "onnx_bytes":         ("le_absolute", 100_000_000),  # A10: wrong silicon at 26×
                                                       # the CPU rate
}
```

**Why teacher-agreement alone is not a safe gate.** Stanton: *"more closely matching the teacher paradoxically does not always lead to better student generalization."* And agreement is computed on the harvest, so it stays green **precisely while** the student faithfully inherits and amplifies the teacher's blind spots. Without an external frozen anchor, **daily retraining is an unmonitored write path to production.**

**Frame the result honestly.** The teacher itself is only ~72-74 macro-F1 on TweetEval, so ~95% agreement ≈ **~70 true macro-F1**. Never advertise "retains 97% of teacher" without that denominator.

### The dead-man's switch: **Modal alerts on jobs that FAIL, never on jobs that never RUN**

Modal's documented alerts (Slack only; email is not documented) cover *"failed scheduled function runs"*: runs that **ran and failed**. They are **structurally blind** to: a stopped app, a deleted app, a deploy that silently never happened, an out-of-date client. All produce **perfect silence**, and silence is indistinguishable from success.

**healthchecks.io Hobbyist: 20 checks, $0**. Beats Cronitor (5 monitors) and Better Stack (10 **shared** with uptime monitors). Their **Business plan is free for open-source projects**.

```python
# ml/pipeline/heartbeat.py
_BASE = "https://hc-ping.com"

def _ping(slug, suffix, rid, body=b"", create=False):
    key = os.environ.get("HC_PING_KEY")
    if not key:
        return
    url = f"{_BASE}/{key}/{slug}{suffix}?rid={rid}" + ("&create=1" if create else "")
    try:
        urllib.request.urlopen(urllib.request.Request(url, data=body or None), timeout=10)
    except Exception as exc:
        print(f"heartbeat ping failed (non-fatal): {exc}")   # MONITORING MUST NEVER
                                                             # BREAK THE JOB

@contextlib.contextmanager
def check(slug: str):
    rid = str(uuid.uuid4())        # ?rid pairs /start with completion => real durations
    _ping(slug, "/start", rid, create=True)   # ?create=1 auto-provisions; no dashboard
    run = types.SimpleNamespace(body={})
    try:
        yield run
    except BaseException:
        _ping(slug, "/fail", rid, traceback.format_exc().encode()[:10_000])
        raise
    else:
        # Starter log retention is 1 DAY. The ping body is FREE DURABLE FORENSICS
        # for a job that failed at 03:00 Saturday.
        _ping(slug, "", rid, json.dumps(run.body, default=str).encode()[:10_000])
```

**Four checks (of 20 free):**

| slug | period / grace | catches |
|---|---|---|
| `sentilyzer-daily` | 1d / 2× p95 runtime | **the cron stopped firing**: Modal is silent for all of these |
| `sentilyzer-promote` | **3d / 1d** | **the model stopped being refreshed while the pipeline reports success nightly.** This is the one people forget: a stuck eval gate is *invisible* to an execution heartbeat |
| `sentilyzer-backlog` | 1d / 12h | any `runs` row `status='pending'` older than 2 days |
| `sentilyzer-prune` | 7d / 1d | the weekly job stopped |

Rate limit: ~5 pings/min per check, irrelevant for daily jobs, but never ping per-document.

### The staleness contract: **the appears-healthy-serves-garbage path**

> **Refuted at major severity.** Modal is the sole writer to `topic_daily`. When the cron silently stops, the gateway's 90-day RAM snapshot **keeps serving**. `GetTopicTrend` then computes its "current" window over days with no rows: `SUM(sum_polarity)/NULLIF(SUM(sample_size),0)` returns NULL, Go scans it to `0.0`, `current_sample_size=0`, `delta = 0 − baseline`, and direction resolves to **DECLINING**. The API returns **200 OK** with a confident *"sentiment declining sharply"* that is a pure artifact of a dead cron. The design's z-score guard only covered baseline <3 days: it said nothing about an empty **current** window. And `TrackedTopic.last_run_at` was declared in the proto with **no backing column**.

**Adopted:**
- `tracked_topics` gains `last_run_at TIMESTAMPTZ` and `last_run_id TEXT REFERENCES runs(run_id)`, updated in the same transaction as the `topic_daily` upsert.
- **Every** history/trend response carries `as_of` (max `day` actually present) and `stale_days`.
- `GetTopicTrend`: if `current_sample_size == 0` **or** the current window has zero days **or** `stale_days > window_days` → `direction = DIRECTION_UNSPECIFIED` (**not** STABLE, **not** DECLINING), `delta = 0`, `z_score = 0`, `label_flipped = false`.
- Scan into `pgtype.Float8`/`sql.NullFloat64` and branch explicitly: never let `current − baseline` compute when `current` is NULL.
- `stale: true` above `MAX_STALE_DAYS = 3`. **503 on `/trend` beyond 7 days**: a trend over week-dead data is not a degraded answer, it's a wrong one.
- `GET /model_version` on the Modal student returns the live `run_id`; alert when `served_run_id != current.json.run_id` for >10 min.

### Manual triggers: three, and they are **not** interchangeable

| How | Runs what | Use for |
|---|---|---|
| Dashboard **"run now"** on the App page | the **deployed** code, default args | "did last night's failure fix itself?" |
| `modal run ml/pipeline/modal_app.py::daily_pipeline --logical-date 2026-07-12 --force` | **local** code, ephemerally, **not** the deployed app | iterating on pipeline code |
| `modal.Function.from_name("sentilyzer-pipeline","daily_pipeline").remote(logical_date=..., force=True)` | the **deployed** code, arbitrary args | **backfills**: exercises the real artifact |

`make modal-backfill DATE=...` wraps the third.

### Redeploy semantics

- **`Cron` is not re-phased by deploys.** `Period` is (*"will run X hours after this most recent deployment"*), and with daily iteration `Period(days=1)` could starve forever. **This is the single reason to use `Cron`.**
- **A failed build changes nothing:** *"Errors during the build will abort the deployment with no change to the status of the App."* Yesterday's code keeps running on schedule. **So a green CI badge does not prove new code is live**: hence `GIT_SHA` baked into the image env and recorded in `runs.code_version`. **Assert it, don't assume it.**
- **Schedules cannot be paused.** Hence `PIPELINE_DISABLED=1` on the Secret.
- **Stopping an App is destructive**: *"Apps cannot be restarted from this state."* Never stop; use the kill switch.
- **Rollback:** Starter's feature table lists **3 version deployment rollbacks** (the briefing's reliability section says paid-only: **the two contradict; verify in the dashboard before an incident**). Fallback: `git checkout <sha> && make modal-deploy`.
- Redeploying `sentilyzer-pipeline` does **not** touch `sentilyzer-serving`. That's the whole point of two apps.

---

## 8. New API surface

The daily system's *output*: 8 new RPCs, mirrored across all four transports.

### `proto/sentilyzer/v1/sentilyzer.proto`

```protobuf
service SentilyzerService {
  // existing (untouched)
  rpc AnalyzeText(AnalyzeTextRequest) returns (AnalyzeTextResponse);
  rpc AnalyzeTopic(AnalyzeTopicRequest) returns (AnalyzeTopicResponse);
  rpc ListPlatforms(ListPlatformsRequest) returns (ListPlatformsResponse);
  rpc Health(HealthRequest) returns (HealthResponse);

  // NEW: the daily system's output surface
  rpc CreateTrackedTopic(CreateTrackedTopicRequest) returns (TrackedTopic);
  rpc GetTrackedTopic(GetTrackedTopicRequest)       returns (TrackedTopic);
  rpc ListTrackedTopics(ListTrackedTopicsRequest)   returns (ListTrackedTopicsResponse);
  rpc UpdateTrackedTopic(UpdateTrackedTopicRequest) returns (TrackedTopic);
  rpc DeleteTrackedTopic(DeleteTrackedTopicRequest) returns (DeleteTrackedTopicResponse);
  rpc GetTopicHistory(GetTopicHistoryRequest)       returns (GetTopicHistoryResponse);
  rpc GetTopicTrend(GetTopicTrendRequest)           returns (GetTopicTrendResponse);
  rpc ListAlertEvents(ListAlertEventsRequest)       returns (ListAlertEventsResponse);
}

// LabelCounts is a TYPED replacement for Aggregate.label_counts's
// map<string,int32>. This is not style: it is forced by the repo.
// VERIFIED: domain.go:76 is `LabelCounts map[string]int32 `json:"label_counts" xml:"-"``
// because Go's encoding/xml CANNOT serialize maps. That is also why
// rest.go:169-189 carries the asTopicEnvelope `breakdown` projection hack
// (and its dead `_ = kv{}` at :178). XML clients get NO label counts today.
// The sentiment label set is CLOSED (domain.AllSentiments, domain.go:25), so a
// struct is strictly better: XML-native, SQL-indexable, identical across all
// four transports. NO asTopicEnvelope-style hack is needed anywhere in the new
// surface. `Aggregate` is left UNTOUCHED for backward compat on the 4 old RPCs.
message LabelCounts { int32 negative = 1; int32 neutral = 2; int32 positive = 3; }

enum Interval  { INTERVAL_UNSPECIFIED = 0; INTERVAL_DAY = 1; INTERVAL_WEEK = 2;
                 INTERVAL_MONTH = 3; }
enum Direction { DIRECTION_UNSPECIFIED = 0; DIRECTION_IMPROVING = 1;
                 DIRECTION_STABLE = 2; DIRECTION_DECLINING = 3; }
enum AlertKind { ALERT_KIND_UNSPECIFIED = 0; ALERT_KIND_POLARITY_SWING = 1;
                 ALERT_KIND_VOLUME_SPIKE = 2; ALERT_KIND_LABEL_FLIP = 3; }

message AlertRule {
  AlertKind kind = 1;
  double threshold = 2;
  int32 window_days = 3;      // default 7
  int32 min_sample_size = 4;  // default 20 (see the honesty note)
  bool  active = 5;
}

message TrackedTopic {
  string slug = 1;                   // URL key; server-derived from query
  string query = 2;                  // fed to connectors.Query.Topic
  string display_name = 3;
  repeated string platforms = 4;     // empty = all DURABLE connectors
  repeated string aspects = 5;
  string language = 6;
  int32  limit_per_platform = 7;
  bool   active = 8;
  repeated AlertRule alerts = 9;     // folded in: "track X, alert on swings"
  google.protobuf.Timestamp created_at  = 10;
  google.protobuf.Timestamp updated_at  = 11;
  google.protobuf.Timestamp last_run_at = 12;   // ← now HAS a backing column
}

message TopicPoint {
  google.protobuf.Timestamp bucket_start = 1;  // UTC midnight at interval=day
  string platform = 2;                         // "_all" = cross-platform rollup
  double mean_polarity = 3;
  double stddev_polarity = 4;   // EXACT at any interval, via sum_sq_polarity
  double mean_confidence = 5;
  Sentiment modal_label = 6;
  int32 sample_size = 7;
  LabelCounts label_counts = 8;
  string teacher_version = 9;
  string student_version = 10;
}

message GetTopicHistoryResponse {
  string slug = 1; string platform = 2; Interval interval = 3;
  repeated TopicPoint points = 4;
  repeated AspectSeries aspect_series = 5;
  int32 total_sample_size = 6;
  google.protobuf.Timestamp as_of = 7;   // ← STALENESS IS A FIRST-CLASS FIELD
  int32 stale_days = 8;                  // ← not an operator concern
}

message GetTopicTrendResponse {
  string slug = 1; string platform = 2;
  double current_mean_polarity = 3;
  double baseline_mean_polarity = 4;
  double delta = 5;
  // z = delta / stddev(the baseline period's DAILY mean_polarity values).
  // UNDEFINED (returned as 0.0 with direction=UNSPECIFIED) when the baseline
  // has <3 days, its stddev ≈ 0, OR the CURRENT window is empty.
  // NEVER fabricate a z from 1 day, and never report DECLINING for a dead cron.
  double z_score = 6;
  Sentiment current_modal_label = 7;
  Sentiment baseline_modal_label = 8;
  bool  label_flipped = 9;
  int32 current_sample_size = 10;   // ALWAYS returned (see the honesty note)
  int32 baseline_sample_size = 11;
  Direction direction = 12;
  google.protobuf.Timestamp as_of = 13;
  int32 stale_days = 14;
}
```

> **HONESTY NOTE: belongs in the proto comments AND the docs.** The teacher's training data ends **December 2021** and it caps at **~72-74 macro-F1** on TweetEval. **A z_score of 1.5 on a 20-document sample is noise wearing a statistic's clothes.** That is why `min_sample_size` defaults to 20, why the baseline requires ≥3 days, why `z_score` is explicitly `0.0`/`UNSPECIFIED` when undefined, and why `sample_size` is **non-optional** in every trend and history response.

### REST (`api/internal/server/rest.go`)

Today's router is 5 routes (`rest.go:41-47`). Adding:

```go
func (r *REST) Router() http.Handler {
	rt := chi.NewRouter()
	// ...existing middleware (rest.go:35-39) and 5 routes (rest.go:41-47)...
	rt.Route("/v1/topics", func(t chi.Router) {
		t.Post("/", r.handleCreateTopic)               // 201 + Location: /v1/topics/{slug}
		t.Get("/",  r.handleListTopics)                // ?active=true&page_size=&page_token=
		t.Route("/{slug}", func(s chi.Router) {
			s.Get("/",    r.handleGetTopic)            // 404 on unknown slug
			s.Patch("/",  r.handleUpdateTopic)         // FieldMask / nullable-field PATCH
			s.Delete("/", r.handleDeleteTopic)
			s.Get("/history", r.handleTopicHistory)
			// ?from=2026-04-01&to=2026-07-15&interval=day|week|month
			// &platform=hackernews|rss|_all&include_aspects=true&aspects=ui,pricing
			s.Get("/trend",  r.handleTopicTrend)       // ?window_days=7&baseline_days=28
			s.Get("/alerts", r.handleTopicAlerts)      // ?from=&to=
		})
	})
	rt.Get("/v1/alerts", r.handleListAlerts)           // cross-topic ?from=&to=&limit=
	return rt
}
```

**Content negotiation is FREE.** `writePayload`/`decodeRequest` (`rest.go:222-256`) already switch on `Accept` / `Content-Type`. The new DTOs need only the same shape as `analyzeTopicDTO` (`rest.go:102-110`): an `XMLName xml.Name` field plus dual `json:`/`xml:` tags. **And because `TopicPoint` carries a typed `LabelCounts` instead of a map, no `asTopicEnvelope` projection hack is needed anywhere in the new surface**: these routes are XML-native by construction.

**Caching:** `rt.Use(noStore)` (`rest.go:39`) blankets everything today. Override on closed-day history to `public, max-age=3600, stale-while-revalidate=86400`; **drop `noStore` on `GET /v1/topics` and `GET /v1/topics/{slug}`** as defense-in-depth for the Neon CU cliff. Keep `no-store` on the analyze routes.

**Status codes:** 201 Created · 404 unknown slug · 409 duplicate slug · 400 `from>to`/bad interval · **501 Not Implemented when `SENTILYZER_TS_DSN` is unset** · **503 on `/trend` when `stale_days > 7`**.

### GraphQL (`api/internal/server/graphql.go`)

**Structural change.** `graphql.go:201` today is `graphql.NewSchema(graphql.SchemaConfig{Query: root})`: **there is no Mutation root.** Tracked-topic CRUD requires adding one:

```go
schema, _ := graphql.NewSchema(graphql.SchemaConfig{Query: root, Mutation: mutation})
```

```graphql
type Mutation {
  createTrackedTopic(input: TrackedTopicInput!): TrackedTopic!
  updateTrackedTopic(slug: String!, input: TrackedTopicPatchInput!): TrackedTopic!
  deleteTrackedTopic(slug: String!): Boolean!
}

extend type Query {
  trackedTopics(activeOnly: Boolean): [TrackedTopic!]!
  trackedTopic(slug: String!): TrackedTopic
  topicHistory(slug: String!, from: String, to: String, interval: Interval = DAY,
               platform: String = "_all", includeAspects: Boolean = false,
               aspects: [String!]): TopicHistory!
  topicTrend(slug: String!, platform: String = "_all",
             windowDays: Int = 7, baselineDays: Int = 28): TopicTrend!
  alertEvents(slug: String, from: String, to: String, limit: Int = 50): [AlertEvent!]!
}

type TopicHistory {
  slug: String!  platform: String!  interval: Interval!
  points: [TopicPoint!]!  aspectSeries: [AspectSeries!]!  totalSampleSize: Int!
  asOf: String!  staleDays: Int!
}
```

graphql-go resolves by matching field keys to Go struct fields, so a `domain.TopicPoint` with `MeanPolarity`/`SampleSize`/`LabelCounts` resolves **with no explicit `Resolve` funcs**, except timestamps, which need the same `.Format("2006-01-02T15:04:05Z07:00")` shim already used at `graphql.go:64`.

### `POST /internal/harvest`: deliberately **not** public

Guarded by `SENTILYZER_INTERNAL_KEY`, not in the proto, not in the OpenAPI spec. It **deliberately breaks four-transport symmetry because it is not public API.** *(Kept as a fallback path only. The primary harvest is the CLI binary. See CRUX 1.)*

---

## 9. SDKs

### Naming: resolved

| Artifact | Source | Registry name | Tag | Published? |
|---|---|---|---|---|
| Python client | `sdk/python/src/sentilyzer/` | `sentilyzer` | `python/vX.Y.Z` | **yes (flagship)** |
| Go SDK | `sdk/go/` | `github.com/chijiokekechi/sentilyzer/sdk/go` | **`sdk/go/vX.Y.Z`** | yes (tag = publish) |
| Server | `api/` | `.../sentilyzer/api` | `api/vX.Y.Z` | tag only, not a library |
| ML worker | `ml/sentilyzer_ml/` | `sentilyzer-ml` | - | **never** |
| Proto | `proto/` | `buf.build/chijiokekechi/sentilyzer` | - | yes, **$0** (public repos never bill) |

Verified 2026-07-15: `curl -o /dev/null -w "%{http_code}" https://pypi.org/simple/sentilyzer/` → **404**; control `httpx` → **200**. All names free. **A pending publisher does not reserve the name**: publish `0.0.1` immediately through the real Trusted Publishing workflow to claim it and prove the pipeline. No collision exists in practice: import names `sentilyzer` vs `sentilyzer_ml` are distinct and only one is ever uploaded. Add `classifiers = ["Private :: Do Not Upload"]` to `ml/pyproject.toml`: PyPI rejects unknown classifiers and `Private ::` is deliberately never valid, so upload hard-fails. `[EST: my own knowledge, not the briefing. Confirm with one deliberate TestPyPI attempt.]`

### `sdk/python/pyproject.toml`

```toml
[build-system]
# hatchling pinned EXACTLY, mirroring openai-python and anthropic-sdk-python (both
# ship `requires = ["hatchling==1.26.3", "hatch-fancy-pypi-readme"]`). NOT uv_build:
# packaging.python.org declines to recommend ANY backend and does not list uv_build
# among pure-Python backends at all, and uv_build would couple this build to uv's
# `<0.12` release cadence for zero benefit.
requires = ["hatchling==1.31.0", "hatch-fancy-pypi-readme"]
build-backend = "hatchling.build"

[project]
name = "sentilyzer"
version = "0.1.0"          # kept in sync with src/sentilyzer/_version.py
description = "Typed client for the Sentilyzer multi-protocol sentiment analysis API, with an optional offline ONNX engine."
authors = [{ name = "Chijioke Ekechi" }]
license = "MIT"
license-files = ["LICENSE"]
dynamic = ["readme"]
# 3.11 floor because onnxruntime 1.27.0 requires >=3.11. Deliberately applied to
# the WHOLE package rather than marker-gating [local]: a split floor lets
# `pip install 'sentilyzer[local]'` SUCCEED on 3.10 WITHOUT onnxruntime, and
# LocalEngine would then tell a user who just installed the extra to install the
# extra. A loud resolver error beats a lying ImportError.
requires-python = ">=3.11"
classifiers = [
  "Typing :: Typed",
  "Development Status :: 3 - Alpha",
  "License :: OSI Approved :: MIT License",
  "Programming Language :: Python :: 3.11",
  "Programming Language :: Python :: 3.12",
  "Programming Language :: Python :: 3.13",
  "Programming Language :: Python :: 3.14",
  "Topic :: Scientific/Engineering :: Artificial Intelligence",
]
dependencies = [
  "httpx>=0.25.0,<1",
  "pydantic>=2.7,<3",
  "typing-extensions>=4.14,<5",
]

[project.optional-dependencies]
# ~40 MB, CPU-only. NO torch, NO transformers, NO huggingface_hub.
# httpx (base) does the model download; pydantic (base) supplies return types.
local = [
  "onnxruntime>=1.27,<2",     # >=1.27 so the 3.11 floor above is actually implied
  "tokenizers>=0.21,<1",
  "numpy>=1.26,<3",
]

# PEP 735 (Final since 2024-10-10). Dev tooling belongs HERE, not in
# optional-dependencies: groups are excluded from built distributions, whereas an
# extra is published as advertised, RESOLVABLE INSTALL METADATA.
[dependency-groups]
test = ["pytest>=8", "pytest-asyncio>=0.24", "respx>=0.21"]
lint = ["ruff>=0.6", "mypy>=1.11"]
dev  = [{ include-group = "test" }, { include-group = "lint" }]

[tool.hatch.build.targets.wheel]
packages = ["src/sentilyzer"]      # also how py.typed gets into the wheel

[tool.hatch.metadata.hooks.fancy-pypi-readme]
content-type = "text/markdown"
[[tool.hatch.metadata.hooks.fancy-pypi-readme.fragments]]
path = "README.md"
# Relative markdown links render BROKEN on the PyPI project page. Rewrite to
# absolute GitHub URLs (the same trick openai/anthropic use).
[[tool.hatch.metadata.hooks.fancy-pypi-readme.substitutions]]
pattern = '\]\(((?!https?://)[^)]+)\)'
replacement = '](https://github.com/chijiokekechi/sentilyzer/blob/main/sdk/python/\1)'
```

**No `grpc` extra in v1.** The gateway already serves REST/JSON; httpx covers Python, gRPC covers any-language, and they needn't overlap. When someone asks, use google-api-core's verified stacking pattern (`grpcio>=1.49.1,<2` + `grpcio>=1.75.1,<2; python_version >= '3.14'` + explicit `protobuf`, since grpcio no longer depends on it).

### `[local]` is **ONNX, not torch**: the only place I revise a stated decision's *implementation*

Decision #3 was *"runs the distilled student offline, same return types"*: that stands. Only the mechanism changes, and **the briefing explicitly invites it**: *"ONNX satisfies that better than torch does on every axis."*

- `torch 2.13.0`'s default PyPI Linux wheel is **526.6 MB with CUDA bundled**, on a CPU-only path.
- `[tool.uv.sources]` **provably cannot fix this downstream**: *"Sources are only respected by uv"*, and PEP 508 has no index concept.
- `onnxruntime` (18.6 MB) + `tokenizers` (3.3 MB) + `numpy` (~18 MB) ≈ **40 MB**, needs no transformers, and INT8 is **~3.08× FASTER than PyTorch fp32** on short text.

**Same promise, 13× smaller, 3× faster.**

### The hardened import gate: the naive version is *actively harmful*

```python
# src/sentilyzer/__init__.py
_LOCAL_EXTRA_MODULES = frozenset({"onnxruntime", "tokenizers", "numpy"})

if TYPE_CHECKING:
    from ._local import LocalEngine as LocalEngine   # keeps mypy/pyright resolving it

def __getattr__(name: str):
    if name == "LocalEngine":
        try:
            from ._local import LocalEngine
        except ModuleNotFoundError as exc:
            # exc.name is "torch.nn"-style for submodules; compare the ROOT.
            root = (exc.name or "").split(".")[0]
            if root not in _LOCAL_EXTRA_MODULES:
                raise      # ← genuine bug inside _local.py. DO NOT MASK IT.
            raise ImportError(
                "sentilyzer.LocalEngine requires the optional 'local' extra.\n"
                "Install it with:\n\n    pip install 'sentilyzer[local]'\n\n"
                f"(missing module: {exc.name})"
            ) from exc
        return LocalEngine
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")

def __dir__() -> list[str]:
    return sorted(__all__)
```

The naive `try/except ModuleNotFoundError` was **empirically demonstrated** to report *"pip install 'sentilyzer[local]'"* when the actual fault was a typo inside `_local.py`, sending users to chase a phantom install bug.

### `LocalEngine`: four skeptic corrections adopted (refuted at major)

The SDK design's R2 resolver had four defects that all bite over six months of unattended use:

| Defect | Fix |
|---|---|
| **Network-mandatory offline engine**: `httpx.get(manifest).raise_for_status()` runs *before* consulting the cache, so a fully-cached model + no network is a hard failure | **Cache-first resolution:** concrete cached revision → zero network calls; manifest fetch failure → fall back to the newest cached revision with a warning; raise only if nothing is cached |
| **Silent daily model change under a pinned SDK**: `revision="stable"` means `pip install sentilyzer[local]==0.1.0` with a hash-pinned wheel still swaps weights daily | **Pin the model to the SDK release.** `__model_revision__ = "2026-07-15"` in `_version.py`, bumped by the same release-please marker, is the **default**. `revision="stable"` becomes explicit opt-in |
| **The cache poisons itself, permanently**: fixed `.part` path + hash computed over the *network stream*, not the written file. Two concurrent constructions interleave, each passes its own checksum, both rename. `_fetch` early-returns on `dest.exists()` → **no self-heal, ever** | `tempfile.mkstemp(dir=base)` for a per-process unique path; **verify by RE-READING the written file**; `os.replace`; verify a pre-existing `dest` against the manifest hash on load and re-download on mismatch |
| **Unbounded cache growth**: daily revisions × 6 months × 80 MB = **~14 GB** | Keep N most recent + the pinned one. `SENTILYZER_MODEL_CACHE_KEEP` (default 3) |

Plus the ORT threading trap, which every containerised `[local]` user hits:

```python
def _default_threads() -> int:
    """ONNX Runtime IGNORES cgroup CPU quotas.

    Left unset, ORT sizes its intra-op pool from the HOST's physical core count
    and applies affinity: inside a container that means dozens of threads
    fighting over a fraction of a core. OMP_NUM_THREADS does NOT fix this:
    official ORT builds ship WITHOUT OpenMP (the --use_openmp flag was removed).
    This is the single highest-impact knob here and it FAILS SILENTLY: you just
    get mysteriously bad latency.
    """
    if env := os.environ.get("SENTILYZER_LOCAL_THREADS"):
        return max(1, int(env))
    try:
        n = len(os.sched_getaffinity(0))   # closer to truth than cpu_count()
    except AttributeError:                 # macOS / Windows
        n = os.cpu_count() or 1
    # Cap at 4: hyper-threading gives NO benefit for compute-bound BERT, and at
    # seq~128 only PARTIAL core allocation helps. Scale with replicas, not threads.
    return max(1, min(4, n))
```

### The conformance test: the missing guard for decision #3

**"Same return types either way" is the entire product promise, and nothing tests it.** `domain.Aggregator` (`domain.go:98-125`) has a non-obvious contract that Python must independently reimplement: the modal tie-break prefers **Neutral > Positive > Negative** via `c > bestCount` from `-1` (`domain.go:110-118`); `mean` accumulates in `float64` then casts to `float32` (`:102`); `NaN → 0` (`:103-105`).

**Adopt:** emit golden vectors from the Go server (`go test -run TestGoldenVectors -update` → `testdata/golden.json`) and assert `LocalEngine` reproduces them within a stated float tolerance. Must cover the tie→neutral rule, the `Aggregator` tie order, the float64→float32 cast, and the NaN path. Run in CI for **both** the ONNX student and the fp32 student so INT8 drift shows as a diff, not a user report.

**Also fix `posted_at`.** Verified: `domain.go:56` is `PostedAt time.Time \`json:"posted_at,omitempty"\``, and Go's `omitempty` **does not omit a zero `time.Time`** (it's a struct). So `SourcedDocument` **always** emits `"posted_at":"0001-01-01T00:00:00Z"`, and the natural `posted_at: datetime | None = None` yields `datetime(1,1,1)`, never `None`. Add a pydantic validator normalising year-1 → `None`, and audit every `,omitempty` on a struct/time field in `domain.go`.

### `sdk/go`: a new module in **this** repo

```
sentilyzer/
├── go.work                  # NEW: dev-time wiring for api/ + sdk/go/
├── api/                     # module .../api      tag api/vX.Y.Z
│   ├── gen/go/…/v1/         # inference.proto ONLY now (internal)
│   └── internal/domain/     # → thin type ALIASES into sdk/go
└── sdk/go/                  # module .../sdk/go   tag sdk/go/vX.Y.Z
    ├── go.mod               # EXACTLY 2 direct deps: grpc + protobuf
    ├── types.go             # PROMOTED from api/internal/domain
    ├── client.go            # ergonomic wrapper over the generated stubs
    ├── convert.go           # mirror of grpc.go's converters
    └── gen/sentilyzer/v1/   # sentilyzer.proto ONLY (public)
```

**Not shipped from `api/go.mod`**: verified, it directly requires `go-chi/chi/v5 v5.2.5`, `graphql-go/graphql v0.8.1`, `mmcdole/gofeed v1.3.0`, `modernc.org/sqlite v1.50.0` (dragging `libc`/`memory`/`mathutil`). Go 1.17+ pruning spares consumers the *build* (`api/go.mod:3` is `go 1.25.0`), but those **direct** requirements still enter the module graph for MVS, and `go get .../sentilyzer/api` semantically hands a client a **server**.

**The type promotion is free.** `domain.go:1-5` says the package exists so *"the REST, XML, GraphQL, and connector layers don't get coupled to the on-the-wire proto layout."* **That is an SDK types package: it's just trapped behind `internal/`.** Move the wire types to `sdk/go/types.go` verbatim (`json:`/`xml:` tags intact), keep the server-only `Aggregator` (`domain.go:82-125`) and `SortPlatforms` (`:154-161`) in `domain`, and alias:

```go
// api/internal/domain/domain.go
import sentilyzer "github.com/chijiokekechi/sentilyzer/sdk/go"

// Type ALIASES, not new types, so every existing reference in service.go,
// rest.go, grpc.go, graphql.go, and connectors/* compiles UNCHANGED.
type (
	Score = sentilyzer.Score;  AspectScore = sentilyzer.AspectScore
	Document = sentilyzer.Document;  DocumentResult = sentilyzer.DocumentResult
	SourcedDocument = sentilyzer.SourcedDocument
	SourcedDocumentResult = sentilyzer.SourcedDocumentResult
	Aggregate = sentilyzer.Aggregate;  SourcedAnalysis = sentilyzer.SourcedAnalysis
	PlatformInfo = sentilyzer.PlatformInfo;  HealthInfo = sentilyzer.HealthInfo
	Sentiment = sentilyzer.Sentiment
)
```

**Proto split is 3 lines + 2 imports.** Change `sentilyzer.proto`'s `go_package` to `.../sdk/go/gen/sentilyzer/v1;sentilyzerv1`; leave `inference.proto` at `.../api/gen/go/...`. **Only 3 files import `api/gen/go`**: `server/grpc.go` and `server/server_test.go` (public proto → repoint) and `inference/client.go:12` (internal `inference.proto` → **unchanged**).

**Tag format is `sdk/go/v0.1.0`, NOT `v0.1.0`.** The major-version suffix is excluded from the prefix (a future `.../sdk/go/v2` still tags `sdk/go/v2.1.6`). Getting this wrong means `go get` silently can't resolve the version.

### "Any language": a **hand-authored** OpenAPI 3.1 spec

**The briefing's two sections conflict here. The repo breaks the tie decisively.** `protoc-gen-connect-openapi --features google.api.http` (MIT, v0.25.7, emits 3.1) is the right tool for a proto-first REST API. **This is not one.** Verified by reading `rest.go`:

| | proto / protojson | what `rest.go` **actually emits** |
|---|---|---|
| enum | `SENTIMENT_NEGATIVE` | **`"negative"`**: `domain.go:15-22`, `Sentiment` is a Go `string` |
| GET topic | `limit_per_platform` | **`?limit=`**: `rest.go:133` `q.Get("limit")` vs `analyzeTopicDTO:106` |
| errors | no such message | **`{"error":"…","status":400}`**: `rest.go:258-265`, in no proto message |
| XML | not expressible | **`?format=xml`** + `Accept` negotiation, with `by_platform` reprojected into `ByPlatformXML` (`rest.go:169-189`) |

A generated spec would document **an API that does not exist**. `grpc.go` exists precisely to convert `domain` → `pb` *because the two shapes differ*: **that converter is the proof**.

**So: `openapi/sentilyzer.v1.yaml`, hand-authored, is the source of truth for REST.** Maps translate cleanly and identically in 3.0 and 3.1:
```yaml
probabilities: { type: object, additionalProperties: { type: number, format: float } }
label_counts:  { type: object, additionalProperties: { type: integer, format: int32 } }
```

**Drift guard: zero new dependencies.** Hand-authored specs drift, and the thing that drifts *first* is the route set. `chi/v5 v5.2.5` is already a direct dep, `go-cmp` and `yaml.v3` are already in `api/go.sum`:

```go
// api/internal/server/openapi_test.go
func TestOpenAPICoversAllRoutes(t *testing.T) {
	rt := (&REST{Service: newTestService(t), Version: "test"}).Router()
	got := map[string]bool{}
	_ = chi.Walk(rt.(chi.Routes), func(method, route string, _ http.Handler,
		_ ...func(http.Handler) http.Handler) error {
		got[method+" "+route] = true
		return nil
	})
	want := loadSpecOperations(t, "../../../openapi/sentilyzer.v1.yaml")
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("spec/router mismatch (-spec +router):\n%s", diff)
	}
}
```

**And close the schema gap with the golden vectors**, since `posted_at` proves route parity is insufficient: validate each golden response against the spec in CI. One artifact, both guards, no new deps.

**Ship a 3.0.3 downgrade too.** OpenAPI Generator's 3.1 support is still maturing (#14943), and a 3.1 spec fed to a 3.0 parser **silently ignores** new keywords rather than erroring: the failure lands on the user as a subtly wrong client. The schema is flat objects/floats/strings/maps, so authoring in the 3.0 ∩ 3.1 intersection is free, reducing the downgrade to `sed 's/^openapi: 3\.1\.0$/openapi: 3.0.3/'` **which CI lints**, so a 3.1-only keyword fails *there* rather than silently vanishing in a user's generator.

**Hosting, all $0:** `/openapi.json` served by the gateway (`go:embed`) · **Scalar OSS renderer (MIT) on Cloudflare Pages** (unlimited bandwidth, 500 builds/mo, **not** the $72/mo hosted tier) · **BSR public module** (`buf.build/chijiokekechi/sentilyzer`: $0, public repos never bill; free stubs across Go/TS/Java/Kotlin/Swift/Python/Rust/C#/C++ you never build or host) · **OpenAPI Generator** for the long tail (generation is the *user's* job).

**Note:** Scalar lists gRPC/GraphQL as "Coming Soon," so it covers REST only. GraphQL keeps GraphiQL; gRPC docs come from the BSR module. **No single tool covers all four surfaces.**

### Release automation

**Trusted Publishing cannot be invoked from a reusable workflow**, and the pending publisher binds to a workflow **filename**, so one non-reusable `release-python.yml` per package.

```yaml
name: Release Python SDK
on: { push: { tags: ["python/v*"] } }
permissions: {}
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with: { python-version: "3.12" }
      - working-directory: sdk/python
        run: python -m pip install --upgrade pip build && python -m build
      - name: Assert py.typed made it into the wheel
        working-directory: sdk/python
        # py.typed is SILENTLY DROPPED from wheels unless the backend is told to
        # include it. A py.typed in the source tree proves NOTHING.
        run: python -m zipfile -l dist/*.whl | grep -q 'sentilyzer/py.typed'
      - name: Assert the base install stays thin
        working-directory: sdk/python
        # The whole product promise is "no torch by default". Guard it.
        run: |
          python - <<'PY'
          import tomllib, sys
          d = tomllib.load(open('pyproject.toml','rb'))['project']['dependencies']
          bad = [x for x in d if x.split('[')[0].split('>')[0].split('=')[0].strip()
                 in {'torch','transformers','onnxruntime','numpy'}]
          sys.exit(1 if bad else 0)
          PY
      - uses: actions/upload-artifact@v4
        with: { name: dist, path: sdk/python/dist/ }
  pypi-publish:
    needs: [build]
    runs-on: ubuntu-latest
    environment: { name: pypi, url: https://pypi.org/p/sentilyzer }
    permissions: { id-token: write }   # MANDATORY for trusted publishing
    steps:
      - uses: actions/download-artifact@v4
        with: { name: dist, path: dist/ }
      # NO username/password == the Trusted Publishing flow. PEP 740 attestations
      # are generated and uploaded automatically; PyPI REJECTS non-TP attestations.
      - uses: pypa/gh-action-pypi-publish@release/v1
```

**Go SDK CI must assert no server dep leaked** (`GOWORK=off go list -deps ./... | grep -qE 'go-chi|graphql-go|gofeed|modernc.org/sqlite'` → fail). **That check is the entire reason the module exists separately.**

### Versioning: three independent axes

| Axis | Where it lives | Cadence | Triggers an SDK release? |
|---|---|---|---|
| **API version** | `/v1/` in `rest.go`; proto pkg `sentilyzer.v1` | ~never | breaking change → `/v2/` |
| **SDK version** | `sentilyzer` 0.1.0 (PyPI); `sdk/go/v0.1.0` | on SDK code change | yes |
| **Model version** | R2 `current.json` → dated `run_id` | **daily** | **never** |

**The third row is load-bearing.** *"Coupling an SDK version to a daily-retrained backend would force a release per retrain, which conflicts with decision #1."* The pointer severs it. `X-Sentilyzer-API-Version` is **rejected**: that idiom suits date-versioned APIs (Stripe/Anthropic); this API is **path-versioned** and the URL already carries it. `User-Agent: sentilyzer-python/0.1.0` carries telemetry instead.

Version method: **static, in two places, bot-synced**. `version = "0.1.0"` in `pyproject.toml` plus `_version.py` with `# x-release-please-version`, exactly as openai/anthropic do. **No `hatch-vcs`, no `dynamic = ["version"]`.** Plus the PyPA-recommended test: `assert sentilyzer.__version__ == importlib.metadata.version("sentilyzer")`.

### Rejected vendors

**Stainless**: unavailable, acquired by Anthropic, *"new signups, projects, and SDKs will not be available"* as of **2026-05-18** (its pricing page still advertises a free tier, stale marketing). **Speakeasy**: free tier is **ONE language / 250 operations**, and Python is the one language that must be hand-written anyway (no generator emits a `[local]` extra that swaps an ONNX engine behind identical return types); additional languages are **$720/mo**. **Fern**: acquired by Postman **2026-01-08**; cloud SDK plans from **~$250/mo**; `fern generate --local` still demands an interactive login. **Underneath all of it: this API has 4 RPCs (12 after Phase 7).** Generators are amortization plays for 100-600-endpoint APIs (Stripe ships ~2,269 spec releases across 7 SDKs). **And the market demonstrated the vendor risk twice in five months.**

**Avoided spend: $1,762+/mo.**

---

## 10. Implementation plan

Legend: **[E]** exists, edited · **[N]** new · **[D]** deleted

### Phase 0: Stop the bleeding *(1 day, no new infra, no dependencies)*

| File | Change |
|---|---|
| `ml/pyproject.toml:18` **[E]** | `transformers>=4.45` → **`transformers>=4.45,<5`**. Verified unbounded → resolves to 5.14.0, whose `from_pretrained` dtype default became `"auto"` → **teachers silently load fp16/bf16 → perturbed soft labels → the student's ground truth is subtly wrong, with no error, on every fresh Docker build.** *One line. Do this first.* |
| `ml/pyproject.toml:15` **[E]** | Move `grpcio-tools>=1.66` out of runtime `dependencies` → `[dependency-groups] codegen`. It drags protoc + a compiler toolchain into every production install. |
| `ml/pyproject.toml:26-31` **[E]** | `dev` extra → `[dependency-groups]`. An extra is published as advertised, **resolvable install metadata**: `pip install sentilyzer-ml[dev]` is a public path you never intended. |
| `ml/pyproject.toml:11` **[E]** | `requires-python = ">=3.10"` → `">=3.11"` (onnxruntime 1.27.0 hard floor). |
| `ml/pyproject.toml` **[E]** | Add `classifiers = ["Typing :: Typed", "Private :: Do Not Upload"]`. |
| `ml/sentilyzer_ml/py.typed` **[N]** | Empty file. Verified absent today. |
| `ml/sentilyzer_ml/inference.py:~140` **[E]** | `TransformerBackend.__init__` gains `max_length: int = 128`, threaded into `tok(...)` at **`:194`** and **`:220-226`**. **Verified: neither call passes `max_length` today**, so both default to roberta's 512. If the teacher labels from 512 tokens and the student reads 128, **the student is asked to predict from strictly less information than produced the label: irreducible error, no error message.** ~4 lines. |
| `api/internal/connectors/factory.go:20` **[E]** | Gate `reg.Register(NewStockTwits(httpClient))` behind an explicit opt-in that defaults **off**. ToS §5 was **revised 2026-07-10** (five days before the audit), and the endpoints **still return HTTP 200 without auth**, so it keeps passing CI while breaching. **Working ≠ permitted.** |
| PyPI | Pending publisher (account sidebar → **Publishing**, since no project exists yet) + `git tag python/v0.0.1 && git push --tags`. **The name is unreserved until a publish succeeds.** |

**Ships:** correctness of the (not-yet-existing) soft labels; a claimed PyPI name; one ToS breach closed.

---

### Phase 1: The compile-time gate, and delete the store *(2-3 days)*

| File | Change |
|---|---|
| `api/internal/connectors/connector.go:20-25` **[E]** | `Query` gains `Window struct{ Start, End time.Time }`. `SinceSeconds` stays for the public API; `service.AnalyzeTopic` translates it. |
| `api/internal/connectors/connector.go:28-36` **[E]** | `Connector` gains `Policy() Policy`. **This breaks all 8 connectors at compile time, on purpose.** |
| `api/internal/connectors/{hackernews,rss,stocktwits,reddit,twitter,mastodon,youtube,mock}.go` **[E]** | 8 `Policy()` methods, each with a `Reason` string quoting the exact ToS clause. |
| `api/internal/connectors/fanout.go` **[N]** | `Fanout(ctx, []Connector, Query) ([]domain.SourcedDocument, []Outcome)`. |
| `api/internal/service/service.go:285-327` **[E]** | `fanout` delegates to `connectors.Fanout`, keeps today's semantics (`:323`: succeed if ≥1 worked), **`slog.Warn`s each failure instead of discarding `errs`**. |
| `api/internal/service/service.go:233-238` **[D]** | **DELETE the persistence call.** |
| `api/internal/service/service.go:20,28,36,53` **[D]** | DELETE the `store` import, the `Store` field, the `st` param, the assignment. |
| `api/cmd/sentilyzerd/main.go:27,58-63,75` **[D]** | DELETE `store.Open`. Note `:58-62` currently **swallows a failed `Open` with `logger.Warn` and continues**: a DSN typo is invisible today. |
| `api/internal/store/` **[D]** | **DELETE the package.** Write-only, one method, two importers. Verified. |
| `api/internal/config/config.go:19,58` **[D]** | DELETE `DBDSN` and its `file:sentilyzer.db?_pragma=journal_mode(WAL)` default. |
| `api/go.mod:13` **[E]** | DELETE `modernc.org/sqlite v1.50.0` → cascades out `modernc.org/{libc,mathutil,memory}` + `ncruces/go-strftime`, `dustin/go-humanize`, `remyoudompheng/bigfft`. |
| `README.md` **[E]** | DELETE *"For Postgres, swap the driver and DSN; the schema is plain SQL"* (false on 3 counts) and *"portable to libsql/Turso"* (true at the SQL level, but the pure-Go client `libsql-client-go` is **deprecated** and maintained `go-libsql` mandates `CGO_ENABLED=1`). |
| `api/internal/service/service_test.go` **[E]** | `service.New(..., nil, ...)` → drop the arg. |

**Ships:** the YouTube §III.E.4.d retention exposure is closed; the collapse recursion vector is unconstructible; a lighter dependency tree; partial-failure visibility. **Independently deployable: no new infra.**

---

### Phase 2: The harvester and the corpus *(1 week)* ← **THE SHIP-STOPPER**

| File | Change |
|---|---|
| `api/internal/connectors/hackernews.go:47-50` **[E]** | Firehose mode: when `Topic == "" && !Window.IsZero()`, hit `search_by_date` with `numericFilters=created_at_i>LO,created_at_i<HI` and no `query`. **The 1,000-hit cap is PER QUERY** (`hitsPerPage=1000` → `nbPages=1`; `page=1` → zero hits with *"you can only fetch the 1000 hits for this query"*). Hourly slices measured 564 hits, safely under. |
| `api/cmd/sentilyzer-harvest/main.go` **[N]** | `--date --durable-only --out=docs.jsonl --manifest=manifest.json`. Iterates `BuildRegistry(cfg)`, filters on `Policy().Durable`, cross-topic dedupes, emits `content_sha256`, exits 3 on zero docs. |
| `ml/pipeline/corpus.py` **[N]** | pyarrow Parquet writer → R2 via boto3; whole-partition overwrite via temp prefix + rename. |
| `Makefile` **[E]** | `harvest-build: cd api && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.Version=$$(git rev-parse --short HEAD)" -o ../bin/sentilyzer-harvest-linux-amd64 ./cmd/sentilyzer-harvest` |
| `.github/workflows/ci.yml` **[E]** | Build the binary **in the same job as the deploy**, or you will eventually run last week's connectors and not notice for days. |
| R2 bucket + Cloudflare API token | `sentilyzer-corpus` |

**Corpus schema, three deliberate choices:**

```
documents/dt=<date>/platform=<id>/part-*.parquet   (zstd)
  content_sha256 BLOB(32)  platform VARCHAR   document_id VARCHAR
  source_url     VARCHAR   author   VARCHAR   posted_at   TIMESTAMP[us,UTC]
  harvested_at   TIMESTAMP text     VARCHAR   text_len    INT32
  lang           VARCHAR   is_long_form BOOL  topic       VARCHAR (nullable)

labels/dt=<date>/platform=<id>/part-*.parquet      (zstd)
  content_sha256 BLOB(32)  platform VARCHAR   document_id VARCHAR
  task VARCHAR ('doc'|'aspect')  aspect VARCHAR (null when doc)
  teacher_version VARCHAR   labeler_role VARCHAR ('teacher' ONLY: a student
                            write is a SCHEMA VIOLATION, not a silent string)
  p_negative FLOAT32   p_neutral FLOAT32   p_positive FLOAT32
  max_len INT16        labeled_at TIMESTAMP   run_id VARCHAR
```

1. **Probabilities, not logits.** Softmax is shift-invariant: `log(p_i) = z_i − logsumexp(z)`, so `softmax(log(p)/T) ≡ softmax(z/T)` **exactly, for any T**. Probs are a **lossless** target. **No logits column. Ever.** And don't re-run the teacher to capture them.
2. **float32, not float16.** fp16 saves **6 bytes on a ~300-byte row (~2% of corpus)** while going subnormal below 6e-5, where `log(p)` **amplifies relative error into the tempered logit**, degrading precisely the small probabilities that *are* the dark knowledge, and degrading them **more** at higher T. R2 storage is $0. **There is nothing to buy.**
3. **Three NAMED columns, not `list<float32>`.** `inference.py:168-184`'s `_order_for` exists **because models disagree on label order** (`LABEL_0/1/2` vs `Negative/Neutral/Positive`). A list re-encodes that order as an implicit index convention: **the exact bug `_order_for` was written to prevent.** Column names make it un-losable.

**Day-1 backfill:** HN Algolia time-slicing backfills **~90 days (≈1.03M docs)** in ~5 chunked invocations of ~200k. **~$0.95, once.** *(Chunked, not one 76-minute run: `label`'s `timeout=3600` would kill it, and chunking makes it resumable.)* This reaches the ~920k steady-state corpus on **day 1 instead of day 90**, which matters because Turc puts 100k far left of the knee. **The single highest-return dollar in the plan.**

**Ships: the corpus starts accumulating. Nothing downstream can begin until this exists.**

---

### Phase 3: Teacher labeling on Modal, with the dead-man's switch *(1 week)*

| File | Change |
|---|---|
| `ml/pipeline/{__init__,modal_app,control,heartbeat,teacher}.py` **[N]** | The `daily_pipeline` cron; `harvest` (shells out); `prep_corpus`; `label` (reuses `TransformerBackend`). |
| `ml/pipeline/migrations/001_init.sql` **[N]** | **`runs` and `run_sources` FIRST**, then everything else. Verified skeptic finding: the source design declared `topic_daily … REFERENCES runs(run_id)` **before** `CREATE TABLE runs`: Postgres resolves `REFERENCES` at `CREATE TABLE` time and fails with `relation "runs" does not exist`. Add a CI smoke test that applies the migration to a throwaway Postgres. |
| Modal secrets **[N]** | `sentilyzer-pipeline` (NEON_DSN, R2_*, HC_PING_KEY, GIT_SHA, PIPELINE_DISABLED, AUTO_PROMOTE) |
| healthchecks.io **[N]** | 4 checks, `?create=1` auto-provisioned. **Verify the first ping actually provisioned the check: a switch that was never created is worse than none.** `[UNCERTAIN: `?rid=` and `?create=1` are documented independently but never together. One curl settles it; the code only passes `create=1` on `/start`, which limits the blast radius.]` |
| Axiom **[N]** | Ship pipeline logs. **Modal Starter retains logs for 1 DAY**: a job that fails at 03:00 Saturday may be unreadable by Monday. |

**Ships:** the corpus gets teacher soft labels; a pipeline that alarms when it stops.

---

### Phase 4: Trainer + eval gate + **manual** promotion *(2 weeks)*

| File | Change |
|---|---|
| `ml/pipeline/student.py` **[N]** | `DistilStudent`: one shared encoder + `head_doc` + `head_aspect`. `build_student_from_teacher()`: asserts `cfg.vocab_size == teacher.config.vocab_size == 50265`, copies embeddings + layers `2i+1`, seeds `head_doc` from the teacher's classifier **permuted via `TransformerBackend._order_for`** (don't assume cardiffnlp's `LABEL_0/1/2` mapping: *derive* it the way `inference.py` does). `head_aspect` stays random: its teacher is DeBERTa-v3, a **different tokenizer and hidden space**. |
| `ml/pipeline/evaluate.py` **[N]** | The 4-tier gate. **Re-scores the champion on the same frozen slice.** Runs the p99 latency check in a **separate CPU function**, never on the A10 (wrong silicon at 26× the CPU rate). |
| `ml/eval/golden_hn_v1.jsonl` **[N]** | **~500-1000 hand-labeled HN/RSS docs from the real harvest, versioned in git, never regenerated, never in training.** ~4 hours. **It is the only artifact that measures what users actually receive**: TweetEval measures a distribution we never serve. |
| `Makefile` **[E]** | `modal-deploy`, `modal-backfill DATE=` |

**Sequencing that matters:**
1. Ship student #1 gated on **T1/T2/T4 only** (T3 floors can't be set a priori).
2. Read its `golden_hn_macro_f1` and `tweeteval_macro_f1`.
3. **Freeze both as `baseline_*` constants in git.**
4. **Train 3× with different seeds on the identical corpus and measure the spread of `agree_doc`.** If that spread ≥ 0.01, the proposed epsilon is **below the noise floor** and the gate is decorative: widen it or gate on a 3-seed median.
5. **`AUTO_PROMOTE` stays OFF.** Promote by hand for 2-4 weeks.

**Alarm on promotion rate:** >20 consecutive days with no gate rejection is **evidence the gate is inert**, not evidence of health.

**The KD loss, exactly:**

```python
T = 3.0
# Hinton MEASURED this band: "when this was radically reduced to 30 units per
# layer, temperatures in the range 2.5 to 4 worked significantly better". T>8
# works only for LARGE students. A 6L/H768 student distilling a 12L teacher is
# capacity-constrained. T=3 is the centre of the measured band.

t_logp = torch.log(batch["teacher_probs"].clamp_min(1e-8))   # (B,3) fp32
with torch.autocast("cuda", dtype=torch.bfloat16):
    s_logits = model(batch["input_ids"], batch["attention_mask"], batch["is_aspect"])

# F.kl_div(input, target) computes KL(target ‖ input); input must be log-probs.
# log_target=True => target is log-probs too. So this is KL(teacher ‖ student).
loss = F.kl_div(F.log_softmax(s_logits.float() / T, dim=-1),
                F.log_softmax(t_logp / T, dim=-1),
                reduction="batchmean", log_target=True) * (T * T)
# T**2 because "the magnitudes of the gradients produced by the soft targets
# scale as 1/T^2". Omit it and T SILENTLY RESCALES YOUR LR: any LR tuned at one
# T becomes invalid at another. Keeping it decouples the two knobs.
#
# alpha = 0. NO hard-label CE term, and this is not a simplification: with no
# ground truth the only available "hard label" is argmax(teacher_probs), a
# deterministic, STRICTLY LOSSY function of the target we already have. Zero new
# information; it only sharpens toward the teacher's argmax, destroying exactly
# the dark knowledge KD runs on. Hinton: "considerably lower weight on the second
# objective function." Noisy Student, on out-of-domain unlabeled data (= our
# heterogeneous HN/RSS mix): "hard pseudo labels can hurt the performance while
# soft pseudo labels lead to robust performance."
# DO NOT PORT AN HF KD TUTORIAL THAT HARDCODES alpha=0.5.
```

| Knob | Value | Why not the obvious value |
|---|---|---|
| LR | `5e-5` | Above RoBERTa's `{1e-5,2e-5,3e-5}`: pure KD is a smoother, denser objective than supervised fine-tuning on small labeled GLUE sets |
| Batch | `128` planned; **shipped: `64`** | Above RoBERTa's `{16,32}`: soft targets are lower-variance so large batches are stable (**DistilBERT used up to 4K**). Also the GPU-utilisation lever if wall-clock overruns. The shipped `DistillConfig` runs `batch_size=64`; 128+ remains the tuning headroom |
| Sequences | `≤1,840,000` | **Not epochs.** See CRUX 4 |
| `max_len` | `128` | **Must equal the labeling `max_len`**, asserted from the Parquet column |
| Precision | `bf16` planned; **shipped: fp32 + TF32** | **bf16 requires Ampere (sm_80+). T4 is Turing (sm_75) and has NO bf16**: the briefing pairs "bf16" with "T4 or A10" and those are **incompatible**. A10 (sm_86) it is. The shipped trainer keeps fp32 KD semantics and enables TF32 tensor-core matmuls (`torch.backends.cuda.matmul.allow_tf32`) instead of bf16 autocast |
| Padding | dynamic **+ length-bucketed** | The ~2× lever. **Without bucketing, batch-128 random shuffle pads to ~128 and the benefit vanishes: the trainer doubles to ~$60/mo** |

---

### Phase 5: Modal serving + the Go HTTP hop *(1 week)*

| File | Change |
|---|---|
| `api/internal/inference/http.go` **[N]** | `HTTPClient` implementing `inference.Client` (`client.go:19-24`). `Modal-Key`/`Modal-Secret` headers. **`service.go`, `rest.go`, `grpc.go`, `graphql.go` change by ZERO lines.** |
| `api/internal/config/config.go` **[E]** | `SENTILYZER_INFERENCE_BACKEND` (`grpc`\|`http`\|`fake`, default `grpc` to preserve today's docker-compose behaviour), `SENTILYZER_MODAL_URL`, `SENTILYZER_MODAL_KEY`, `SENTILYZER_MODAL_SECRET` |
| `api/cmd/sentilyzerd/main.go:51-56` **[E]** | Backend switch. |
| `ml/serving/modal_app.py` **[N]** | See CRUX 2 / CRUX 5. |
| `fly.toml` **[N]** | Two `[[services]]` blocks. **`[[services]]` and `[http_service]` are MUTUALLY EXCLUSIVE: `[http_service]` is shorthand for a `[[services]]` block and Fly rejects a config carrying both.** |

```toml
app = "sentilyzer"
primary_region = "iad"

[build]
  dockerfile = "deploy/docker/api.Dockerfile"

[env]
  SENTILYZER_HTTP_ADDR = ":8080"
  SENTILYZER_GRPC_ADDR = ":9090"
  SENTILYZER_INFERENCE_BACKEND = "http"

[[vm]]
  size = "shared-cpu-1x"
  memory = "512mb"        # 512, not 256: connector-fanout goroutine headroom.
                          # Δ = (0.5-0.25) GiB × $5.20/GB-mo = $1.30/mo.

# REST/JSON + REST/XML + GraphQL
[[services]]
  internal_port = 8080
  protocol = "tcp"
  auto_stop_machines = false
  auto_start_machines = true
  min_machines_running = 1
  [[services.ports]]
    port = 80
    handlers = ["http"]
    force_https = true
  [[services.ports]]
    port = 443
    handlers = ["tls", "http"]

# gRPC. The "tls" handler (SNI routing) is what keeps this on the FREE shared
# anycast IPv4: a plaintext gRPC port would force a $2/mo dedicated IPv4.
# h2_backend forwards h2c directly: most HTTP/2 LBs terminate H2 and re-issue
# HTTP/1.1 upstream, which breaks gRPC (it needs H2 + trailers).
[[services]]
  internal_port = 9090
  protocol = "tcp"
  auto_stop_machines = false
  auto_start_machines = true
  min_machines_running = 1
  [services.tls_options]
    alpn = ["h2"]
  [services.http_options]
    h2_backend = true
  [[services.ports]]
    port = 8443
    handlers = ["tls"]
```
`[EST: verify the exact placement of `min_machines_running`/`auto_stop_machines` inside `[[services]]` with `fly config validate` before deploying. The briefing verifies the keys and the two-block structure but not their nesting in the non-`[http_service]` form.]`

**Ships:** production inference on the distilled student, scale-appropriate and cheap.

---

### Phase 6: Automated promotion *(1 week, after 2-4 weeks of manual)*

`AUTO_PROMOTE=1`. Add `sentilyzer-promote` (3d/1d) and `sentilyzer-backlog` (1d/12h) heartbeats. Add the `served_run_id != current.json.run_id` alarm. **Rollback drill: write `current.json` back to a prior `run_id` and assert the live endpoint reports it within 5 minutes.** *A rollback that reports success without acting is worse than no rollback.*

---

### Phase 7: Neon time series + the new API surface *(2 weeks)*

`ml/pipeline/migrations/002_timeseries.sql` **[N]** · `api/internal/timeseries/{timeseries,snapshot}.go` **[N]** · `proto/sentilyzer/v1/sentilyzer.proto` **[E]** · `api/internal/server/{rest,graphql,grpc}.go` **[E]** · `openapi/sentilyzer.v1.yaml` **[N]** · `api/internal/server/openapi_test.go` **[N]**

**Store additive moments, not derived statistics:**
```sql
CREATE TABLE topic_daily (
  topic_id BIGINT NOT NULL REFERENCES tracked_topics(id) ON DELETE CASCADE,
  platform TEXT NOT NULL,   -- 'hackernews' | 'rss' | '_all'
  day      DATE NOT NULL,
  -- ADDITIVE MOMENTS: the source of truth. Every rollup (week/month/_all) is
  -- EXACT because these sum. Rolling daily→weekly by AVERAGING mean_polarity is
  -- the classic silent bug.
  sample_size     INT              NOT NULL,
  sum_polarity    DOUBLE PRECISION NOT NULL,
  sum_sq_polarity DOUBLE PRECISION NOT NULL,   -- => EXACT stddev at any interval
  sum_confidence  DOUBLE PRECISION NOT NULL,
  count_negative INT NOT NULL DEFAULT 0,
  count_neutral  INT NOT NULL DEFAULT 0,
  count_positive INT NOT NULL DEFAULT 0,
  -- DENORMALIZED for the hot day-grain read path
  mean_polarity DOUBLE PRECISION NOT NULL,     -- = sum_polarity/sample_size
  modal_label   TEXT             NOT NULL,
  teacher_version TEXT NOT NULL, student_version TEXT,
  run_id TEXT NOT NULL REFERENCES runs(run_id),
  computed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- (topic_id, platform, day) = eq, eq, range. The served query filters
  -- topic_id= and platform= then ranges over day. (topic_id, day, platform)
  -- would force a filter step.
  PRIMARY KEY (topic_id, platform, day),
  CONSTRAINT counts_ck CHECK (count_negative+count_neutral+count_positive = sample_size),
  CONSTRAINT label_ck  CHECK (modal_label IN ('negative','neutral','positive')),
  -- Makes the '_all' sentinel collision IMPOSSIBLE rather than merely unlikely.
  CONSTRAINT platform_ck CHECK (platform = '_all' OR platform NOT LIKE '\_%')
);
```

**DELIBERATELY ABSENT: p10/p50/p90.** Percentiles are **not additive**: they cannot roll up daily→weekly or per-platform→`_all`, so any interval other than `day` would return a **silently wrong number**. `sum_sq_polarity` gives exact stddev at every interval for 8 bytes.

**`_all` must be STORED, not derived.** Summing per-platform rows on read would **double-count crossposts**: the same story on HN and an RSS feed is one document by `content_sha256` but two rows by `(platform, document_id)`. Modal computes `_all` from the batch **after** cross-platform dedupe. Storing it is the only **correct** option; the read-perf win is incidental.

**Rollups compute `modal_label` in Go via `domain.Aggregator`** so the neutral-preferring tie rule (`domain.go:110-118`) stays the single definition. **Do not reimplement it in Python**, and if Modal writes `topic_daily` directly, ship a shared test vector so Python's argmax and Go's tie-break cannot silently diverge.

**Snapshot EVERY read path**, not just `topic_daily`:
```go
// api/internal/timeseries/timeseries.go
cfg.MinConns, cfg.MaxConns = 0, 4
cfg.MaxConnIdleTime = 30 * time.Second
// ⚠ THIS IS NOT THE FIX. The CU cliff is caused by REQUEST ARRIVAL RATE, not by
// pool configuration: Neon suspends on 5 minutes of QUERY inactivity regardless
// of pool state. MinConns=0 does NOT prevent it. The fix is the snapshot below.
```
Mirror `topic_daily` (54k rows ≈ 6.5 MB) **AND** `tracked_topics` (200 rows) **AND** `alert_events` (few hundred). All trivially small. Refresh **hourly**, not every 10 min: a 10-min refresh beats the 5-min autosuspend and pins compute ~100% (≈91 CU-hr/mo, **$19.35/mo**). Hourly wakes 24×/day for ~5 min ≈ **15.2 CU-hr/mo, $1.61/mo**. **That is a 12× compute reduction and the single highest-leverage config decision in the data layer.**

`main.go` behaviour change: **DSN empty → analyze-only, topic endpoints 501/UNIMPLEMENTED. DSN set + `Open` fails → FATAL.** Rename `SENTILYZER_DB_DSN` → **`SENTILYZER_TS_DSN`** so the old SQLite default cannot be silently inherited into a Postgres world.

---

### Phase 8: SDKs *(2 weeks)*

`go.work` **[N]** · `sdk/go/` **[N]** · `sdk/python/` **[N]** · `.github/workflows/{release-python,release-go,release-spec}.yml` **[N]** · `buf.gen.yaml` **[N]** (replaces the raw `protoc` at `Makefile:30-42`; buf CLI v1.71.0 is Apache-2.0 and works offline with no BSR account)

⚠ **`go.work` + `deploy/docker/api.Dockerfile`.** The image builds via `cd api && go build`. With `go.work` at the root, the Docker context must include **both** `go.work` and `sdk/go/`, or set `GOWORK=off` and rely on the tagged `require`. **This fails at image build, not in CI: after the change looks green.**

⚠ **`make clean` between proto states.** The `go_package` split silently invalidates any local `api/gen/go`; stale stubs resolve and produce confusing duplicate-registration errors.

`LocalEngine` ships **last**: it depends on the trainer actually publishing `current.json` and an ONNX artifact.

---

## 11. Risks + open questions

### ⛔ THE HARD CONSTRAINT: READ THIS FIRST

**Four of seven connectors are legally prohibited from the training corpus. One is prohibited by judgment. Two are cleared. This is not a cost tradeoff and it is not negotiable.**

| Source | Status | The clause |
|---|---|---|
| **Reddit** | 🔴 **UNCURABLE AT ANY PRICE** | Data API Terms **§2.4**: *"no other rights or licenses are granted or implied, including any right to use User Content for other purposes, such as for training a machine learning or AI model, **without the express permission of rightsholders in the applicable User Content**."* **The permission must come from the individual redditors, not Reddit**, so a Reddit data-licensing deal (the $60M/yr Google-type deal) **would not grant what §2.4 withholds**. The Responsible Builder Policy bans training **even for non-commercial use** and now requires approval before *any* API access. **§6 requires deleting, on termination, "any data or models that were derived from User Content"**, at Reddit's unilateral, no-notice discretion. **A single Reddit-contaminated run makes the student checkpoint itself deletable on Reddit's say-so.** And anonymisation does not cure it: the same terms treat retention of deleted content or data as a violation even when it has been disassociated, de-identified, or anonymized. **There is no version of Sentilyzer at any budget that legally trains on Reddit.** |
| **X / Twitter** | 🔴 **BLOCKED** | Dev Agreement **§III.A(k)** (eff. 2026-04-27) bans training *"a foundation or frontier model"*: a 66M student arguably isn't one, **but do not test the loophole**: **§III.A(d)** independently bars creating *"derivative works of… the Licensed Material"*, and the pip package ships the model to third parties. **It dies on cost first anyway: $0.005/post read × 10k/day = ~$1,500/mo, 15-30× the entire budget.** |
| **StockTwits** | 🔴 **BLOCKED, AND THE MOST DANGEROUS** | ToS **§5**, revised **2026-07-10 (five days before the audit)**: no automated extraction *"except… through an approved API"*, and developer registration has been **frozen since ~2021** (`/developers/docs` 404s; footer still © 2021). **There is no approved-API path to obtain, so there is no compliant configuration.** It is dangerous precisely because it **still returns HTTP 200 with live data, no auth, no key, no rate-limit headers**, so the connector keeps passing CI while breaching a five-day-old ToS. **Working ≠ permitted. Disabled in Phase 0.** |
| **YouTube** | 🔴 **BLOCKED, but on retention, not training** | **The ToS contains NO ML/AI training clause** (verified: 0 hits for "machine learning" or "model" in the full Americas ToS). **Do not report YouTube as "prohibits training."** It prohibits **storing** Non-Authorized Data past **30 calendar days** (§III.E.4.d) and **creating derived data** (§III.E.4.h), and a sentiment label *is* derived data. Theoretically curable via a compliance audit + §III.L permission (new 2026-06-01). Not worth it at this tier. **⚠ This is the one that is arguably accruing today**: `analyses` holds rows past 30 days. **Phase 1 closes it.** |
| **Mastodon** | 🟡 **EXCLUDED ON JUDGMENT** | No instance ToS checked bans training (mastodon.social exposes **no ToS document at all**). But **all four instances checked block GPTBot in robots.txt**: the operators have **expressly opted out of AI training** and simply haven't anticipated API-side harvesting. **Legally defensible; reputationally indefensible.** Sentilyzer ships a public pip package with attributable provenance. If you want it: self-host an instance or get explicit operator consent. Don't rely on the silence of the ToS. |
| **HackerNews** | 🟢 **CLEAR** | 10,000 req/hr/IP, no auth, no key, **no training prohibition anywhere in the docs**. YC's ToU bars *"data mining, robots, scraping"* but (a) never mentions AI training and (b) `hn.algolia.com` is Algolia's domain, powers HN's own on-site search, and is a **sanctioned interface**: consuming a published API is not scraping. The official Firebase API is YC's own and states no restrictions. Measured: **11,436 docs/day for 48 requests = 0.02% of the limit.** |
| **RSS** | 🟢 **CLEAR-ish** | No API, no key, no quota, **no contract of adhesion**: you consume a file the publisher deliberately published for syndication. Exposure is ordinary copyright, per-publisher. Bartz v. Anthropic: training is transformative fair use **when the copies were lawfully acquired**: RSS is lawfully acquired by construction, and a 3-class label **cannot reproduce the source text and does not substitute for any publisher's market** (the strongest possible fair-use posture). Most feeds are summary-only. `[UNCERTAIN: favourable, not settled. Courts run a market-harm test; where a rightsholder offers a training licence and you decline to pay it, fair use gets materially harder.]` |

**Net: ~10,000 usable docs/day (HN ~8,000 + RSS ~2,000) ≈ 300k/month.** Distillation saturates around **200k-500k** soft-labelled examples, so the compliant corpus clears the bar within ~30 days, **or on day one with the $0.95 backfill.**

> **Data volume is NOT the binding constraint on this project. Legal eligibility is. Your decision to prioritise training frequency survives this audit fully intact; you just run it on two sources instead of seven.**

**The gate is `Policy()` on `connectors.Connector`: a compile-time method, not a config flag and not a `WHERE platform NOT IN (...)`.** A flag rots the first time someone adds a connector.

---

### Risks

| # | Risk | Mitigation |
|---|---|---|
| 1 | **`transformers>=4.45` is unbounded RIGHT NOW** (`ml/pyproject.toml:18`) and resolves to 5.14.0, whose `dtype="auto"` default would silently load teachers in fp16/bf16, **corrupting the soft labels the entire student distills from, with no error, on every fresh Docker build.** | Phase 0, one line: `<5`. **And set `dtype=torch.float32` explicitly** rather than relying on any default. |
| 2 | **The trainer times out every night from month ~2** if bounded by epochs. | CRUX 4: wall clock + `MAX_TRAIN_SEQUENCES` asserted on CPU. **Checkpoint-and-exit-partial; never hit `FunctionTimeoutError`.** |
| 3 | **GPU preemption cannot be paid away.** *"All Modal Functions are subject to preemption by default"*; `nonpreemptible=True` is *"not supported for GPU Functions."* Over 365 runs the trainer **WILL** be preempted mid-run. | Checkpoint every 500 steps, keyed on the **candidate** not the date. **Temp path → atomic rename → commit**: background commits fire every few seconds and will happily commit a half-written file. |
| 4 | **Trainer MFU is the least certain number here and it drives the budget.** 20% is my planning figure `[EST: the briefing has no figure]`; 10% is plausible for small models (~$60/mo). | Instrument run #1. The lever is batch size (128→256), sanctioned for distillation (DistilBERT used up to 4K). **The wall-clock cap means the BILL is fixed either way; only the epochs delivered float.** |
| 5 | **Neutral-class collapse**: the student abandons neutral while macro-F1 still looks fine. | Per-class recall floors ≥0.45. **This is the failure most likely to reach production if someone later "simplifies" the gate to a single F1 number.** |
| 6 | **The teacher is frozen in Dec-2021 language and daily retraining CANNOT fix it.** ~124M tweets, Jan 2018-Dec 2021, nearly 5 years stale. | Be clear-eyed: retraining refreshes **input coverage**, not judgment. The lever for 2026 slang is a **newer teacher**. Worth naming since retraining frequency is a stated priority. |
| 7 | **A bad promotion reaches prod in ~5 minutes with no human in the loop.** The pointer-poll's blast radius is symmetric with its rollback speed. | `AUTO_PROMOTE=0` for 2-4 weeks. Frozen golden set + per-class floors + in-process smoke test. **Promotion-rate alarm.** |
| 8 | **`serve_img` must include `fastapi`** or `@modal.fastapi_endpoint` fails at import. | In the spec. **Restate the footprint honestly: ~90-120 MB of runtime deps + model** (the "65 MB" was deps-only, excluded the base image, and predates fastapi). |
| 9 | **ORT thread misconfiguration fails silently.** ORT ignores cgroup quotas and sizes from the **host's** core count; `OMP_NUM_THREADS` is a **no-op** (no OpenMP in official builds). **Modal bills ACTUAL usage** and the default soft limit is request+16 cores, so a misconfigured session **burns cores you pay for**, not just cores you're throttled on. | `so.intra_op_num_threads = 2` in code. Prominent in the `[local]` README with `SENTILYZER_LOCAL_THREADS`. |
| 10 | **INT8 can be SLOWER on pre-VNNI silicon.** ORT docs: *"not rare to get worse performance on old devices."* And the **3.08× is a SHORT-TEXT number** (~39 chars): the briefing warns ONNX *"may perform slightly worse than PyTorch"* on longer inputs. **RSS full-article bodies are the concern.** | Check `/proc/cpuinfo` for AVX-512 VNNI on Modal's silicon. Benchmark on the longest RSS bodies. |
| 11 | **`models.sentilyzer.dev` is a permanent operational commitment.** A published pip package pointing at a domain you own: if it lapses, every `[local]` install breaks. | Register before v0.1.0. `model_dir=` escape hatch for air-gapped users. `r2.dev` managed URLs are rate-limited and unsuitable. |
| 12 | **`content_sha256` normalisation is a ONE-WAY DOOR.** Change `normalize()` and every historical hash becomes incomparable: dedupe silently stops matching old rows against new, re-admitting duplicates. | Version it (`hash_version` column) or freeze it. It lives in **exactly one place** so this is a single decision. |
| 13 | **Modal's Slack alerting is the only documented channel** (email is not mentioned). Without a Slack workspace, **healthchecks.io is not defense-in-depth: it is the ONLY alerting path**, and its availability becomes a SPOF for knowing anything is wrong. | Accept, or add Slack. |
| 14 | **AI contamination enters via long-form.** ~1 in 4 social posts >250 words are AI-generated in 2026 (LinkedIn 41%, X long-form ~50%, Medium 31%); Reddit replies are ~98% human and **short-form is largely clean**. **HN comments are short-form. RSS is the inlet.** | `is_long_form` column; cap RSS at ≤15% of any training batch. **A data-supply problem, unrelated to recursion.** |
| 15 | **Only 2 eligible connectors, and RSS is `Backfillable=false`.** A multi-day Modal outage costs RSS days **permanently**, and the HN-only fallback pushes the corpus toward one register. | HN backfill covers HN. Accept RSS loss; alert on 3 consecutive `run_sources` failures. |
| 16 | **`timeout` is PER ATTEMPT.** `retries=3, timeout=3600, gpu="A10"` = up to **4 GPU-hours ($5.30)** from one cron tick. | `retries=0` on GPU. **Always compute `timeout × (retries+1) × $/hr` before deploying.** |
| 17 | **Modal's spend-budget enforcement action is UNDOCUMENTED.** Docs say *"hard outer cap"* but never state whether it kills, blocks, or notifies. | Set ~$50 as a **backstop, not a guard**. The real guards are `MAX_CATCHUP_DAYS`, `MAX_DOCS_PER_RUN`, `max_containers`, `MAX_TRAIN_SECONDS`. |
| 18 | **A green CI badge does not prove new code is live.** *"Errors during the build will abort the deployment with no change to the status of the App."* | `GIT_SHA` in the image env → `runs.code_version`. **Assert it.** |
| 19 | **The `go.work`/Dockerfile interaction fails at image build, not in CI.** | Copy `go.work` + `sdk/go/`, or `GOWORK=off`. |
| 20 | **12 public RPCs weakens (but does not break) hand-written SDKs.** Still far under the 100-600 amortisation threshold, but every proto edit is now ~2 Python methods + ~2 Go wrappers + a GraphQL resolver + a REST DTO + an XML tag. **And the Mutation root is a genuinely new GraphQL surface with no existing test coverage.** | Budget for it per change. |

### The configuration traps (all default-correct: the lever is *not breaking them*)

| Rule | Cost of breaking it |
|---|---|
| **No `region=` anywhere** | **1.5× (broad) / 1.75× (narrow) on the ENTIRE Modal bill (GPU+CPU+RAM), and it applies on Starter.** +$21.56-32.34/mo for zero benefit on a cron. *(A widely-circulated third-party blog cites 1.25×-2.5×: that figure is wrong; 1.5×/1.75× is first-party.)* |
| **Never `nonpreemptible=True`** | 3× on CPU+RAM; **unsupported on GPU at any price**. The headline GPU rate **is** the preemptible rate. |
| **Never a large `disk=`** | **20:1 memory inflation**: 500 GiB disk → 25 GiB memory → **~$146/mo** on an always-on container. Bake weights into the image or use a Volume. |
| **Never leave the trainer in a Notebook** | **3.01×** on CPU/RAM ($0.141912 vs $0.04716/core-hr). Also: don't benchmark in a Sandbox and extrapolate. |
| **Never over-request `cpu=`/`memory=`** | Billing is `max(request, actual)`, **changed in 2024 from usage-only**. `cpu=1.0` on the warm student = **$40.26/mo, 4.0× the cost for zero gain on a batch-1 CPU path**. |
| **Snapshot EVERY Neon read path** | +$17.74/mo. **`MinConns=0` does NOT prevent it.** |
| **Prune R2 model revisions** | +$0.29/mo yr1 → +$2.04/mo yr5 (unbounded 80 MB/day = 29.2 GB/yr). |
| **Stay on Starter** | Team = **$250 − $100 = $150/mo NET before any compute**, 1.5-3× the entire budget. Starter's caps fit with 5-20× headroom. |
| **Never `H100`/`A100-80GB`** | A100-80GB is **4.2× a T4** for base-size encoders that don't need it. *(Free upside: H100 requests may be silently auto-upgraded to H200, and A100-40GB to A100-80GB, at NO extra cost. Use `"H100!"` to decline.)* |

---

### Open questions: ranked by what they change

1. **Trainer MFU / wall clock.** Swings the budget $15→$36/mo. My 23-32 min assumes 25-35% MFU on an A10 for an 82M model at batch 128/seq64; small models are often overhead-bound and could land at 15% (~53 min). **Instrument run #1

1. **Trainer MFU / wall clock.** Swings epochs-per-run from 2.6 to 0.85. My 20% figure is `[EST: the briefing has no figure]`. **Instrument run #1**, then tune batch size (128→256) as the utilisation lever before touching anything else. **The wall-clock cap means the BILL is fixed either way**: only the epochs delivered float.

2. **The GPU choice for the trainer is a $7-16/mo decision made on an assumption.** `[EST: the briefing does not verify relative GPU speed.]` At a fixed 26.25 PFLOP window and 20% MFU: T4 **$16.57/mo** (fp16 only: Turing has no bf16), **L4 $14.53/mo**, A10 **$30.18/mo**, L40S **$17.67/mo**. Two effects the table cannot resolve without measurement: L4 has ~2× A10's dense tensor throughput at 0.73× the price but **half** the memory bandwidth (300 vs 600 GB/s), and small-model training is often bandwidth-bound; and MFU *falls* as the GPU gets bigger for a fixed small model. **Crossover: L4 beats A10 unless L4 is more than 1.30× slower** (`$1.32379 / $1.02139`). Note also that the "A10 gives 2× wall clock vs T4" claim is **unsupported**: T4 fp16 dense (65 TFLOPS) slightly *exceeds* A10 bf16 dense (62.5); A10's advantage is bandwidth, not FLOPs. **Measure L4 and T4 against A10 on run #1.**

3. **Are Modal cold-start/boot seconds billed?** `[UNCERTAIN: briefing]`. The four pricing-page FAQ accordions that would settle it (*"What counts as billable time?"*, *"How are CPU and memory usage metered?"*) are **client-rendered and unfetchable**. Inference: they almost certainly ARE, since billing is per running container on `max(request, usage)` and the idle rule explicitly bills "GPU reservation." My model assumes billable. **Impact: teacher ±$0.57/mo.** Resolve by opening `modal.com/pricing` in a browser and expanding them.

4. **Is idle CPU billed at the 0.125-core floor?** `[UNCERTAIN: briefing]`. Pricing says "Minimum: 0.125 cores per container"; the cold-start doc's idle-billing list names **only memory and GPU**, conspicuously omitting CPU. **Swings the warm student between $10.26 and $5.96/mo.** Doesn't change the decision: reconcile against the first invoice.

5. **The agreement curve on YOUR harvest.** The briefing's 10k→85-88% / 50k→90-92% / 200k→93-95% / 1M→95-97% figures are **explicitly flagged as EXTRAPOLATED** from Turc's Amazon-reviews curve: nobody publishes this for RoBERTa-base 3-class social sentiment → 6-layer student. It is a **one-day experiment** after the backfill (label 200k, train at 10k/50k/200k, plot) and the answer is specific to your HN/RSS mix. **Do it before committing to any corpus-size target**, and note that if a 22M MiniLM hits the same agreement, the student decision flips and saves ~$6/mo.

6. **The T3 floors cannot be set a priori.** Because the transfer set is HN/RSS and TweetEval is tweets, the student's TweetEval score sits below the teacher's ~72.6 by an **unknown domain-gap margin**. Ship student #1 gated on T1/T2/T4 only, read both numbers, freeze the floors from those. **Guessing either blocks every promotion or gates nothing.**

7. **Seed variance vs the gate epsilon.** Train student #1 **three times with different seeds on the identical corpus** and measure the spread of `agree_doc`. If that spread ≥ 0.01, the proposed epsilon is **below the noise floor** and the gate is decorative: widen it, or gate on a 3-seed median, or accept that ~half your promotions are coin flips.

8. **How many aspect pairs does real traffic actually produce?** Aspect supervision comes from topic-scoped harvest `(text, topic)` plus user-requested `Aspects`; the firehose contributes doc-level only. If topic-scoped volume is thin, the `n_aspect_pairs >= 5,000` gate fails and `head_aspect` is starved. **Only real traffic answers this.** Fallback (entity/ticker regex mining) adds noise and should **not** be pre-built. If ABSA defers to v2, the teacher drops to ~$0.60/mo and the CPU-vs-GPU question becomes entirely moot.

9. **What is the real cold start for `onnxruntime` + an ~80 MB INT8 model?** **No authoritative Modal number exists for this shape**: the widely-cited "2-4s" traces to an SEO content farm still using the removed `container_idle_timeout` param. My ~1-3s estimate is a hypothesis, and it is what justifies skipping snapshots. `min_containers=1` makes it mostly moot, but measure before quoting.

10. **What is the INT8 accuracy delta on *our* student?** Expect ~0.4pt (Sentence-Transformers' threshold; Intel's DistilBERT SST-2 measured 0.9037 vs ~0.9130 fp32). **If a class collapses under INT8 (neutral being the obvious candidate), try `per_channel=True` or `nodes_to_exclude` before abandoning INT8.** If it happens, `LocalEngine` and the hosted API disagree on exactly the class users notice most.

11. **Does the two-head single-ONNX export actually work?** The spec assumes one graph, inputs `(input_ids, attention_mask)`, outputs `(logits_doc, logits_aspect)`, dynamic axes. Verify `optimum-onnx` exports a custom two-head module cleanly, and that `onnxruntime.transformers.optimizer` (which matters **specifically because** of dynamic axes) doesn't mangle it. **Fallback:** two ONNX files with a duplicated encoder (~2× disk/RAM, still small).

12. **DuckDB R2 write path.** The **read** path is verified with exact syntax (`CREATE SECRET (TYPE r2, KEY_ID, SECRET, ACCOUNT_ID)` + `read_parquet('r2://…')`). The **write** claim appears only in prose. **Use pyarrow + boto3 for writes and DuckDB only for reads.** Worth 10 minutes to check. Also note `duckdb/duckdb#14178`: R2 secrets have required `REGION 'auto'` in some versions.

13. **Should Modal write `topic_daily` directly (psycopg3), or POST aggregates to the gateway?** Direct-write is simpler and is what I specced, but it puts the aggregate math (mean, tie-break) in **Python AND Go**. `domain.go:110-118` prefers neutral on ties; if Python's argmax disagrees, rows silently differ. **Direct-write + a shared test vector is the recommendation.**

14. **Does the OpenAPI spec document the XML surface at all?** `rest.go` supports `?format=xml` with a **genuinely different shape** (`ByPlatformXML`, because `encoding/xml` cannot serialize maps). Options: document JSON only and note XML as undocumented-but-supported, or model both. **Leaning JSON-only for v1, but that makes the spec an incomplete description of the server, which the route-parity test will not catch.** The golden-vector schema validation partially covers it.

15. **GitHub Actions minutes**: `[EST: not covered by the briefing]`. Assumed free for a public repo. If so, move ONNX export/quantize off the trainer and shave its tail. **Verify before assuming.**

16. **Inter-region traffic IS billed at an UNPUBLISHED rate** `[LIKELY: briefing]`. Keep Fly / Neon / Modal in a matching footprint: Neon and Fly both run on AWS, so pick matching regions. **But do NOT `region=` pin Modal to achieve it** (1.5-1.75× the whole bill). Modal general egress appears free `[LIKELY: zero hits for "egress"/"bandwidth" on the pricing page; absence of a line item is strong but not affirmative]`.

17. **Fly `performance-2x` = ~$64.39/mo** `[EST: skeptics conflicted ($31/$62 vs $32.19/$64.39); the pessimistic figure is used]`. Only matters at ~5 req/s. Verify on Fly's pricing page before the escalation is needed.

18. **`.dev` domain ~$1.00/mo** `[EST: not in the briefing]`, Cloudflare Registrar at-cost. **Register before v0.1.0: a published pip package's default URL is effectively permanent.**

19. **Is `Private :: Do Not Upload` actually sufficient** to block `sentilyzer-ml` uploads? `[EST: my own knowledge]`. PyPI rejects unknown classifiers and this one is deliberately never valid, so upload should hard-fail. **Confirm with one deliberate TestPyPI attempt** before relying on it as the only guard.

20. **Does `Image.add_local_file` preserve the executable bit?** Not stated in the briefing. The spec `os.chmod(HARVEST_BIN, 0o755)` defensively at runtime: cheap and correct either way, but it's a guess about the failure rather than knowledge of it.

21. **Should `api/internal/domain` be deleted rather than aliased?** The alias shim keeps `service.go`/`rest.go`/`graphql.go` compiling with zero edits, which is the right *migration step*. But it leaves two names for one type indefinitely. Deleting `domain` and importing `sentilyzer` directly across `api/` is cleaner but touches ~10 files. **A separate follow-up PR, not mixed into the SDK split.**

---

## Appendix: the shortest possible summary

**If you do only three things:**

1. **Ship the harvester.** Nothing accumulates until `api/cmd/sentilyzer-harvest` exists, and `store.go:46` (verified) has no `text` column, so today's traffic trains nothing. Every day of delay is **irrecoverable** training data. Then run the **$0.95 90-day HN backfill** and reach steady state on day one instead of day ninety.

2. **Bound the trainer by wall clock (`MAX_TRAIN_SECONDS = 2_700`), not by epochs.** This one line converts the largest and fastest-growing item in the system into a **fixed $30.18/mo forever**, and it is the difference between **$19.30/mo forever and $105/mo by year one**. It is invisible for six months. Cap on **documents**, not days: a day-cap silently tracks the harvest rate.

3. **Serve the student from Modal on CPU, `cpu=0.125`, `min_containers=1`, INT8 ONNX, and set `intra_op_num_threads` explicitly.** $10.26/mo instead of a ~$425/mo GPU idle trap, and **not** in-process next to Go: Fly's `shared-cpu-4x` is **0.25 sustained physical cores, not 2**. The threading line is **not optional**: ORT ignores cgroup quotas, Modal's `OMP_NUM_THREADS` is a no-op because ORT ships without OpenMP, and Modal bills **actual** usage, so you'd pay for ~16 cores while wondering why latency is bad.

**Deliberately skipped, ~$45/mo of unclaimed savings left on the table:** weekly training (−$25.88: costs your stated priority), MiniLM-22M (−~$6: costs teacher-layer init, DistilBERT's **largest** ablation at −3.69, plus ~4 points), in-process Go inference (−$6: the compute premise is false), `min_containers=0` (−$9.29: buys an unmeasured cold start on the latency path), fp16 on the teacher (−$0.50: risks silent soft-label corruption), torch.compile (**−$0.01, actively NEGATIVE**: warmup exceeds the entire 86s compute budget, HF models are TorchInductor's *worst* category at 1.15-1.20×, and it can make snapshot creation fail outright), flash attention (**$0.00**: bandwidth-bound at seq~64, not attention-bound).

**Three of those seven are actively negative. The budget does not need any of them.**