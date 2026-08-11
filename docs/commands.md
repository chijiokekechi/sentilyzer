# Command registry

Every command for training, harvesting, serving, querying, testing, and
Modal operations, in one place. Paths at the start of each block tell you
where to run from. Nothing here runs on a schedule: idle time costs $0.

## One-time setup

```bash
# repo root
make tools                 # installs protoc-gen-go + protoc-gen-go-grpc
make proto                 # generates Go and Python gRPC stubs
make ml-install            # Python worker dependencies

# ml/ (training only)
pip install modal && modal setup    # authenticates YOUR Modal account
```

## Training (run from ml/)

The single command. `--detach` keeps the run alive if your connection
drops; `::main` is required because the file has several entrypoints.

```bash
# Full run: ingest -> label -> aspect-label -> train -> gates -> download
modal run --detach sentilyzer_ml/pipeline/modal_app.py::main \
    --from-month 2025-05 --output ./student
```

| Variant | Command suffix | Time | Cost |
|---|---|---|---|
| Smoke run (mechanics check) | `--from-month 2025-07 --to-month 2025-07 --limit 8000` | ~15 min | ~$0.15 |
| Reuse Volume corpus + labels | `--skip-ingest` | ~1 h | ~$1 |
| Wipe and rebuild the corpus | `--fresh-corpus` | ~2 h | ~$1.50 |
| Train on your harvested corpus | `--corpus ../corpus` | varies | ~$1+ |
| Longer training (ceiling 90) | `--train-minutes 60` | +15 min | +$0.30 |
| More aspect pairs (ceiling 800k) | `--aspect-pairs 600000` | +20 min | +$0.30 |

Recovery entrypoints (after a disconnect, or to inspect the Volume):

```bash
modal run sentilyzer_ml/pipeline/modal_app.py::runs      # list runs + gate status
modal run sentilyzer_ml/pipeline/modal_app.py::fetch \
    --run-id "train:2026-08-11-054942" --output ./student
modal run sentilyzer_ml/pipeline/modal_app.py::unlock    # clear a stranded lock
modal run sentilyzer_ml/pipeline/modal_app.py::evaluate \
    --run-id "train:..."                                 # re-gate an orphaned run
```

The lock normally needs no attention: a live run heartbeats, a dead run
is taken over automatically within five minutes, and `::unlock` exists
only for the impatient path.

## Harvesting your own corpus (run from api/)

All three write the same corpus layout, so rows dedupe across sources.
Feed the result to training with `--corpus`.

```bash
go run ./cmd/sentilyzer-harvest -source bluesky -out ../corpus \
    -duration 30m -sample 0.25 -langs en     # streams the public firehose
go run ./cmd/sentilyzer-harvest -source rss -out ../corpus
go run ./cmd/sentilyzer-harvest -source gdelt -out ../corpus -timespan 24h
```

Policy notes: Bluesky honors delete events as tombstones; GDELT stores
its own fields only (headline, domain, date) at one request per five
seconds; RSS stores feed-native content. See docs/corpus-policy.md.

## Serving (run from repo root)

On demand: start when you need it, Ctrl-C when you do not.

```bash
# Terminal 1: the ML worker serving YOUR trained student
SENTILYZER_ML_MODEL_DIR=ml/student make ml-run

# Terminal 1 alternative: no model yet, deterministic stub
SENTILYZER_ML_USE_STUB=1 make ml-run

# Terminal 2: the API gateway (defaults :8080 HTTP, :9090 gRPC)
make api-run

# If 8080/9090 are taken on your machine:
SENTILYZER_HTTP_ADDR=":8098" SENTILYZER_GRPC_ADDR=":9091" make api-run

# Or everything in Docker:
SENTILYZER_USE_MOCK=true SENTILYZER_ML_USE_STUB=1 make up
```

Health checks (adjust the port if you overrode it):

```bash
curl -s localhost:8080/health      # service + ML readiness, which model serves
curl -s localhost:8080/livez       # liveness
curl -s localhost:8080/readyz      # readiness
```

`/health` reports the served student run id, and whether aspects come
from the student (gate passed) or the teacher (gate failed or absent).

## Querying

```bash
# Inline text, JSON
curl -s -X POST localhost:8080/v1/analyze/text \
     -H 'Content-Type: application/json' \
     -d '{"documents":[{"text":"I love this"}]}' | jq

# Topic search across platforms, with optional location narrowing
curl -s "localhost:8080/v1/analyze/topic?topic=rust&limit_per_platform=5" | jq
curl -s "localhost:8080/v1/analyze/topic?topic=elections&platforms=gdelt&location=Germany" | jq

# Other response formats: Accept header or ?format=
curl -s "localhost:8080/v1/analyze/topic?topic=rust&format=yaml"
curl -s "localhost:8080/v1/analyze/topic?topic=rust&format=csv"
curl -s "localhost:8080/v1/analyze/topic?topic=rust&format=ndjson"

# Save results (persistence is deliberately the caller's job)
curl -s "localhost:8080/v1/analyze/topic?topic=edtech" > edtech-$(date +%F).json

# Bring your own key (YouTube / Mastodon only)
curl -s "localhost:8080/v1/analyze/topic?topic=Robinhood&platforms=youtube" \
     -H "X-Sentilyzer-Youtube-Api-Key: $MY_KEY" | jq .by_platform

# GraphQL
curl -s localhost:8080/graphql -H 'Content-Type: application/json' \
     -d '{"query":"{ analyzeTopic(topic:\"rust\", limitPerPlatform:5) { aggregate { modalLabel meanPolarity } } }"}' | jq

# gRPC
grpcurl -plaintext -d '{"documents":[{"text":"loved it"}]}' \
        localhost:9090 sentilyzer.v1.SentilyzerService/AnalyzeText
```

## Testing (run from repo root)

| Command | What | Time | Cost |
|---|---|---|---|
| `make test` | Go + Python unit suites | ~15 s | free |
| `make api-test` / `make ml-test` | one side only | ~10 s | free |
| `make lint` | vet + ruff | ~5 s | free |
| `make e2e` | boots worker + gateway, asserts 14 checks across every protocol and format | ~1 min | free |

## Modal operations (rarely needed)

```bash
modal app list                          # is anything running? (should be nothing when idle)
modal app stop -y <app-id>              # kill a runaway app
modal volume ls sentilyzer-train runs   # what runs exist on the Volume
modal volume ls sentilyzer-train labels # label cache sizes
```
