# Architecture

## Process model

Two long-lived processes communicate over gRPC:

1. **`sentilyzerd`** (Go) is the public gateway. It speaks REST/JSON,
   REST/XML, gRPC, and GraphQL on top of a single `service.Service` and
   fans out to platform connectors with goroutines.
2. **`sentilyzer-ml`** (Python) hosts the inference models. It speaks one
   internal gRPC API: `InferenceService` (`Classify`, `ClassifyAspects`,
   `Ready`).

Splitting them avoids the usual polyglot pain (Python's GIL and slow
HTTP serving) without giving up its ML ecosystem. Each side scales,
restarts, and is monitored independently.

## Why the four protocols share one `Service`

The `service.Service` type is the protocol-agnostic core (in
`api/internal/service/service.go`). Each transport — `server/rest.go`,
`server/grpc.go`, `server/graphql.go` — is a thin adapter that converts
the inbound request shape to a `service.Analyze*` call and converts the
domain result back. Adding a fifth transport (e.g. WebSocket streaming,
SOAP) means a new adapter file; nothing else changes.

XML and JSON share the **same** REST router; we use HTTP content
negotiation (Accept / Content-Type / `?format=`) to flip the encoder.

## Why `cardiffnlp/twitter-roberta-base-sentiment-latest`

- **Trained on the right domain.** ~124M tweets, so consumer/social
  vocabulary, slang, hashtags, and brand mentions are first-class
  rather than out-of-distribution.
- **Three classes** — negative / neutral / positive — with calibrated
  softmax outputs we can convert to a continuous polarity (`pos − neg`).
- **Speed.** ~50–100ms per item on CPU at batch=16, ~10ms on GPU.
  Batching across platforms in `service.AnalyzeTopic` keeps tail latency
  down even when fanning out across N connectors.
- **MIT-licensed** model card; no commercial-use ambiguity.

Alternatives we considered:

| Model                                          | Why we didn't pick it (yet)                                  |
|------------------------------------------------|--------------------------------------------------------------|
| DistilBERT-SST2                                | Faster, but trained on movie reviews — shifts on social text.|
| `cardiffnlp/twitter-xlm-roberta-base-sentiment`| 8 languages, but ~25 % bigger; planned as a `--multilingual` knob. |
| Llama-3-8B with sentiment prompt               | Far higher cost/latency; quality gain marginal for 3-class.  |
| Pure VADER lexicon                             | Fast, but brittle on sarcasm and modern slang.               |

## Why `yangheng/deberta-v3-base-absa-v1.1` for aspects

ABSA (aspect-based sentiment analysis) needs the model to know what the
sentiment is *about*. yangheng's checkpoint is the most-downloaded
DeBERTa-v3 ABSA model on the HuggingFace hub, takes the standard
`(text, aspect)` pair format, and produces the same 3-class output we
already pipe through the rest of the system. Inputs are batched as text
pairs in a single forward pass.

## Connector framework

Each platform implements:

```go
type Connector interface {
    ID() string
    DisplayName() string
    Enabled() (bool, string)
    Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error)
}
```

`service.Service.fanout` runs every selected connector in parallel,
collects their `SourcedDocument`s, dedupes by `(platform, document_id)`,
classifies them in a single batched call to the ML worker, then
aggregates by platform and by aspect.

Disabled connectors stay registered so `GET /v1/platforms` can show the
operator *why* (missing API key, instance unset, etc.).

## Caching and persistence

- **In-process LRU TTL cache** on `service.Service` keys topic-analysis
  results by `(topic, platforms, limit, language, since, aspects)`. TTL
  defaults to 10 min — long enough to absorb the burst of duplicate
  queries that follows a retry-storm, short enough that a freshly
  trending topic still produces fresh results.
- **SQLite by default** (`modernc.org/sqlite`, pure Go, no CGO) for
  durable analysis history. The DSN is portable to libsql/Turso. For
  Postgres, swap the driver and DSN; the schema is plain SQL.

## Failure modes

| Failure                       | Behavior                                                    |
|-------------------------------|-------------------------------------------------------------|
| ML worker unreachable         | `/health` reports `ml_reachable=false`; analyses fail closed |
| Single connector errors       | Logged; other connectors' results are still returned        |
| All connectors error          | `AnalyzeTopic` returns `502 Bad Gateway` with the error list |
| Persistence fails             | Logged warning; request still succeeds                      |
| HuggingFace download fails    | Operators run with `SENTILYZER_ML_USE_STUB=1` until fixed   |

## Roadmap

- Streaming `AnalyzeTopic` results via gRPC server-stream / GraphQL
  subscriptions — stream documents as platforms finish, instead of
  waiting on the slowest one.
- ONNX-quantized export of the general model to halve CPU latency.
- Per-platform rate limiting (token bucket) and circuit breakers.
- Multilingual mode using `twitter-xlm-roberta-base-sentiment`.
