# Sentilyzer

A sentiment-analysis API that serves the same business logic over **REST/JSON,
REST/XML, REST/YAML, gRPC, and GraphQL**, and pulls source material from public
platforms (Hacker News, Reddit, RSS, StockTwits, Mastodon, Twitter/X, YouTube;
all drop-in pluggable).

```
                  ┌──────────────────────────────────────────────┐
                  │            sentilyzerd  (Go)                 │
                  │   ┌────────┐  ┌────────┐  ┌────────────┐     │
   HTTP :8080 ───▶│   │ REST   │  │ REST/  │  │  GraphQL   │     │
                  │   │ /JSON  │  │ XML    │  │  /graphql  │     │
                  │   └────┬───┘  └────┬───┘  └─────┬──────┘     │
   gRPC :9090 ───▶│   ┌────┴───────────┴────────────┴──────┐     │
                  │   │           service.Service          │     │
                  │   └─────┬───────────────────┬──────────┘     │
                  │      ┌──┴──────┐         ┌──┴──────┐          │
                  │      │connectors│        │inference│          │
                  │      │ (HN/Reddit│       │  client │          │
                  │      │ /RSS/…)   │       └────┬────┘          │
                  └──────┴───────────┴────────────┼───────────────┘
                                                  │ gRPC
                                          ┌───────┴───────────┐
                                          │ sentilyzer-ml     │
                                          │ (Python, gRPC)    │
                                          │  HuggingFace      │
                                          │  Transformers     │
                                          └───────────────────┘
```

## Why these choices

- **Go for the gateway**: one process serves four protocol surfaces, fans out
  cleanly to many connectors via goroutines, and gives a static binary for
  trivial deployment.
- **Python for ML**: first-class HuggingFace + PyTorch ecosystem with the
  best research-to-production paved path. Talks to Go over gRPC so each side
  scales (and crashes) independently.
- **`cardiffnlp/twitter-roberta-base-sentiment-latest`** for the
  document-level model: RoBERTa-base fine-tuned on ~124M tweets, 3-class
  (negative/neutral/positive). Best published F1 we've found on consumer/
  social text, ~50-100ms CPU inference per item, batchable.
- **`yangheng/deberta-v3-base-absa-v1.1`** for aspect-based sentiment:
  DeBERTa-v3 fine-tuned on SemEval ABSA datasets so callers can get sentiment
  *about* a specific aspect (e.g. "the airline's food", "Robinhood's UI"),
  not just the whole post.
- **Heuristic stub backend** for offline / CI / quick demos: toggle with
  `SENTILYZER_ML_USE_STUB=1` and the worker uses a deterministic lexicon
  instead of pulling weights.

## Quickstart

```bash
# 1. Generate proto stubs
make tools         # one-time: installs protoc-gen-go + protoc-gen-go-grpc
make proto

# 2. Run ML worker (stub mode is instant; real models take ~1 min on first run)
make ml-install
SENTILYZER_ML_USE_STUB=1 make ml-run                  # in one terminal

# 3. Run the API
SENTILYZER_USE_MOCK=true make api-run                 # in another terminal

# 4. Try every protocol
curl -s -X POST http://localhost:8080/v1/analyze/text \
     -H 'Content-Type: application/json' \
     -d '{"documents":[{"text":"I love this app, it is amazing"}]}' | jq

curl -s -X POST http://localhost:8080/v1/analyze/text \
     -H 'Content-Type: application/xml' -H 'Accept: application/xml' \
     -d '<analyze_text><documents><document><text>terrible service</text></document></documents></analyze_text>'

# YAML out (?format=yaml works too). Requests stay JSON or XML; see below.
curl -s -X POST http://localhost:8080/v1/analyze/text \
     -H 'Content-Type: application/json' -H 'Accept: application/yaml' \
     -d '{"documents":[{"text":"loved it"}]}'

# GraphQL (the response is JSON)
curl -s http://localhost:8080/graphql \
     -H 'Content-Type: application/json' \
     -d '{"query":"{ analyzeTopic(topic:\"Robinhood\", platforms:[\"mock\"], limitPerPlatform:5) { topic aggregate { modalLabel meanPolarity sampleSize } } }"}' | jq

# gRPC
grpcurl -plaintext -d '{"documents":[{"text":"loved every minute"}]}' \
        localhost:9090 sentilyzer.v1.SentilyzerService/AnalyzeText
```

## Or with Docker

```bash
SENTILYZER_USE_MOCK=true SENTILYZER_ML_USE_STUB=1 make up
```

## Connectors

| ID            | Source                       | Auth                              | Free? |
|---------------|------------------------------|-----------------------------------|-------|
| `hackernews`  | HN Algolia search API        | none                              | yes   |
| `rss`         | Curated RSS/Atom feeds       | none                              | yes   |
| `bluesky`     | Bluesky post search          | none                              | yes   |
| `gdelt`       | GDELT world news headlines   | none (1 req/5s, self-throttled)   | yes   |
| `stocktwits`  | StockTwits cashtag stream    | none (public read)                | yes   |
| `reddit`      | Reddit OAuth search          | `REDDIT_CLIENT_ID/SECRET`         | yes   |
| `mastodon`    | Mastodon v2 search           | `MASTODON_ACCESS_TOKEN`           | yes   |
| `twitter`     | Twitter v2 recent search     | `TWITTER_BEARER_TOKEN`            | paid  |
| `youtube`     | YouTube Data API v3 search   | `YOUTUBE_API_KEY`                 | yes   |
| `mock`        | Synthetic deterministic posts | none (dev only)                  | yes   |

When a connector lacks credentials, `GET /v1/platforms` reports it as
disabled with a reason; calls that ask for it are silently skipped.

### Bring your own key

YouTube and Mastodon can also run on a **caller-supplied** key for a single
request, with no server configuration needed. Send the key as a header (REST /
GraphQL) or as gRPC metadata (same names, lowercased):

| Header                              | Unlocks    |
|-------------------------------------|------------|
| `X-Sentilyzer-Youtube-Api-Key`      | `youtube`  |
| `X-Sentilyzer-Mastodon-Token` (+ optional `X-Sentilyzer-Mastodon-Instance`) | `mastodon` |

```bash
curl -s "http://localhost:8080/v1/analyze/topic?topic=Robinhood&platforms=hackernews,youtube" \
     -H "X-Sentilyzer-Youtube-Api-Key: $MY_YOUTUBE_KEY" | jq .by_platform
```

Caller keys are used in memory for that one request, solely as your agent and
under a duty of confidentiality: never logged, never persisted, never echoed
into errors, never pooled across callers. Cached results are isolated per
credential set (the cache key carries a one-way hash of the keys), so results
fetched with your key are never served to anyone else. Requests you make with
your own key run on **your** quota and are subject to the platform's own
terms. Server-side keys, when configured, remain the fallback. A
caller-supplied Mastodon instance must be a public https host (the gateway
refuses private and link-local targets).

**Why not Reddit or Twitter/X?** Their developer terms forbid it: X's
Developer Agreement (III.G) bans making any token or key available to a third
party, and Reddit's Developer Terms (1.4) ban sharing Access Info without
Reddit's permission. Accepting those keys would put every caller in breach.
Both platforms remain available via server-side configuration.

### Why no Facebook / Quora / Blind?

Their APIs either don't exist for third parties (Quora, Blind) or have
ToS-incompatible scraping requirements (Facebook public posts are gated).
The framework leaves the slot open: implementing `Connector` for a new
platform is a single file; see `internal/connectors/hackernews.go` for
the smallest reference.

## Response formats

Every REST endpoint content-negotiates JSON, XML, and YAML via `Accept`
(q-values honored) or `?format=json|xml|yaml`. JSON is the default and wins
ties. An `Accept` we can't satisfy gets a `406`.

The analyze endpoints (`/v1/analyze/text` and `/v1/analyze/topic`, POST and
GET) additionally offer two row-per-document exports, responses only: CSV
(`Accept: text/csv` or `?format=csv`) and NDJSON (`Accept:
application/x-ndjson` or `?format=ndjson`). On every other endpoint these
types still negotiate to `406`.

**CSV is lossy** the way XML is (see below): aggregates, aspects, and
`metadata` are omitted. The header row is always present, quoting/escaping is
RFC 4180, and the columns are:

- `/v1/analyze/text`: `index,label,confidence,polarity,p_negative,p_neutral,p_positive`
  (`index` is the document's position in the request)
- `/v1/analyze/topic`: `platform,doc_id,created_at,label,confidence,polarity,p_negative,p_neutral,p_positive,text`
  (`created_at` is the platform's post timestamp, RFC 3339 UTC, blank when unknown)

Errors on a CSV request come back as JSON: CSV has no error shape, and a bare
header row would read as an empty result set.

**NDJSON** carries the same document objects the JSON response does, one per
line. For `/v1/analyze/topic` a final `{"aggregate": ...}` line closes the
stream; the breakdowns (`by_platform`, `by_aspect`) and warnings are omitted.

**Requests** are JSON or XML only. YAML request bodies are refused with `415`,
deliberately: YAML 1.1 implicit typing silently coerces unquoted scalars, so
`topic: no` parses as the *string* `"false"` with no error. That is unguardable
on a field like `text`, where `"no"` and `"y"` are exactly the kind of terse
snippet this API exists to score.

**XML is lossy.** `encoding/xml` cannot serialize Go maps, so `probabilities`,
`label_counts`, and document `metadata` are omitted from XML responses. JSON,
YAML, and gRPC carry them. Prefer JSON or YAML if you need the distributions.

## Endpoints

```
GET  /health                      → service + ML readiness
GET  /v1/platforms                → connector catalog
POST /v1/analyze/text             → inline-document analysis
POST /v1/analyze/topic            → search + analyze a topic across platforms
GET  /v1/analyze/topic            → same, query-string form
POST /graphql                     → GraphQL
                                    (GraphiQL UI when the Accept header allows HTML)
gRPC sentilyzer.v1.SentilyzerService → mirror of the above
```

### Filtering by topic, and saving what you get

Topic filtering is the core of `/v1/analyze/topic`: the topic string is
searched on each requested platform, and every matching document is scored.
Multi-word topics work ("virtual educational tools", "iPhone battery
complaints"). Precision is bounded by each platform's own search: a query
like "barbershops in Austin" matches posts containing those words, not
businesses located there; the API has no entity or geo layer.

The API deliberately keeps no server-side state, so persisting results is
the caller's job, and it is a one-liner:

```bash
curl -s "localhost:8080/v1/analyze/topic?topic=virtual+educational+tools" \
     > edtech-$(date +%F).json
```

Pipe into your own database, or request `Accept: application/yaml` if that
suits your tooling better. Retention of platform content then happens under
your control and your responsibility, which is where the platforms' terms
put it anyway.

## Configuration

Every variable is optional; see `.env.example` for the full list.
Key knobs:

| Var                          | Default                              | Purpose                                |
|------------------------------|--------------------------------------|----------------------------------------|
| `SENTILYZER_HTTP_ADDR`       | `:8080`                              | REST + GraphQL listener                |
| `SENTILYZER_GRPC_ADDR`       | `:9090`                              | gRPC listener                          |
| `SENTILYZER_ML_ADDR`         | `localhost:50051`                    | ML worker address                      |
| `SENTILYZER_USE_MOCK`        | `false`                              | Register the synthetic connector       |
| `SENTILYZER_ML_USE_STUB`     | `0`                                  | Skip HuggingFace, use heuristic        |
| `SENTILYZER_ML_GENERAL_MODEL`| `cardiffnlp/twitter-roberta-base-…`  | Override the document-level model      |
| `SENTILYZER_ML_ASPECT_MODEL` | `yangheng/deberta-v3-base-absa-v1.1` | Override the aspect model              |
| `SENTILYZER_ML_MAX_BATCH`    | `32`                                 | Batch ceiling. **Set it on both processes or neither** |
| `SENTILYZER_CACHE_TTL`       | `10m`                                | LRU TTL for topic queries              |

## Train your own student (on your Modal account)

The distillation pipeline runs entirely on **your** [Modal](https://modal.com)
account: no servers, no storage accounts, no keys handed to anyone. Modal's
recurring free credit covers it (a full run costs ~$1-2 of credit; an idle
month costs $0):

```bash
cd ml
pip install modal && modal setup       # authenticates YOUR Modal account
modal run --detach sentilyzer_ml/pipeline/modal_app.py::main \
    --from-month 2025-06 --output ./student
```

`--detach` keeps the run alive on Modal even if your connection drops
mid-training (without it, the app's lifetime is tied to your laptop's
heartbeats). If you do get disconnected, the pipeline finishes anyway;
pick the artifact up afterwards:

```bash
modal run sentilyzer_ml/pipeline/modal_app.py::runs      # list finished runs
modal run sentilyzer_ml/pipeline/modal_app.py::fetch \
    --run-id "train:2026-08-10-221206" --output ./student
modal run sentilyzer_ml/pipeline/modal_app.py::unlock    # if a dead run stranded the lock
```

That one command ingests the freely licensed HackerNews archive into a Modal
Volume, labels it with the frozen teacher (T4), distills a 6-layer INT8 ONNX
student (A10, hard 45-minute cap), runs the eval gate (agreement +
per-class-collapse floors), and downloads `model.int8.onnx` + tokenizer +
`eval.json` to `--output`. Useful options:

| Flag | Meaning |
|------|---------|
| `--from-month` / `--to-month` | archive window to ingest (YYYY-MM) |
| `--limit N`      | per-month row cap for cheap smoke runs |
| `--corpus DIR`   | train on your own harvested corpus instead of the archive |
| `--skip-ingest`  | reuse whatever the Volume already holds |

Repeat runs are incremental (ingested months and teacher labels are reused),
and training is bounded by wall clock, so the bill stays ~$1/run no matter
how big the corpus grows. The run ends with sample predictions from your
fresh student. The pipeline survives disconnects, function timeouts, and
Modal preemptions: labels flush to the Volume every 50k rows, training
checkpoints with RNG state, and a preempted orchestrator resumes its own
run instead of restarting it.

**Serve it on demand**: start the API when you need it, stop it when you
don't; nothing runs in between:

```bash
# model path is relative to the repo root (make absolutizes it for you)
SENTILYZER_ML_MODEL_DIR=ml/student make ml-run     # worker serves YOUR student
make api-run                                       # gateway, other terminal
curl "localhost:8080/v1/analyze/topic?topic=rust&limit_per_platform=5" | jq
```

If 8080/9090 are taken on your machine (Prometheus famously squats 9090),
pick free ports: `SENTILYZER_HTTP_ADDR=":8098" SENTILYZER_GRPC_ADDR=":9091"
make api-run`, then curl port 8098.

Document-level sentiment comes from your student; aspect analysis keeps the
teacher (the student's aspect head is untrained until aspect labels exist).

## Development

```bash
make api-test          # Go tests
make ml-test           # Python tests
make test              # both
make lint              # vet + ruff
```

## Project layout

```
proto/sentilyzer/v1/   # public + internal protobuf
api/                   # Go API gateway (REST/XML/GraphQL/gRPC)
ml/                    # Python ML worker (gRPC + HuggingFace)
deploy/                # Dockerfiles + docker-compose
docs/architecture.md   # design notes
```

## License

MIT. See [LICENSE](LICENSE).
