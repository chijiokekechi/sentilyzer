# Sentilyzer

A sentiment-analysis API that serves the same business logic over **REST/JSON,
REST/XML, REST/YAML, gRPC, and GraphQL**, and pulls source material from public platforms
(Hacker News, Reddit, RSS, StockTwits, Mastodon, Twitter/X, YouTube — drop-in
pluggable).

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

- **Go for the gateway** — one process serves four protocol surfaces, fans out
  cleanly to many connectors via goroutines, and gives a static binary for
  trivial deployment.
- **Python for ML** — first-class HuggingFace + PyTorch ecosystem with the
  best research-to-production paved path. Talks to Go over gRPC so each side
  scales (and crashes) independently.
- **`cardiffnlp/twitter-roberta-base-sentiment-latest`** for the
  document-level model — RoBERTa-base fine-tuned on ~124M tweets, 3-class
  (negative/neutral/positive). Best published F1 we've found on consumer/
  social text, ~50–100ms CPU inference per item, batchable.
- **`yangheng/deberta-v3-base-absa-v1.1`** for aspect-based sentiment —
  DeBERTa-v3 fine-tuned on SemEval ABSA datasets so callers can get sentiment
  *about* a specific aspect (e.g. "the airline's food", "Robinhood's UI"),
  not just the whole post.
- **Heuristic stub backend** for offline / CI / quick demos — toggle with
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

# YAML out (?format=yaml works too). Requests stay JSON or XML — see below.
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
request — no server configuration needed. Send the key as a header (REST /
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
platform is a single file — see `internal/connectors/hackernews.go` for
the smallest reference.

## Response formats

Every REST endpoint content-negotiates JSON, XML, and YAML via `Accept`
(q-values honored) or `?format=json|xml|yaml`. JSON is the default and wins
ties. An `Accept` we can't satisfy gets a `406`.

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
| `SENTILYZER_ML_MAX_BATCH`    | `32`                                 | Batch ceiling — **set it on both processes or neither** |
| `SENTILYZER_CACHE_TTL`       | `10m`                                | LRU TTL for topic queries              |

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

MIT — see [LICENSE](LICENSE).
