# Architecture

## Process model

Two long-lived processes communicate over gRPC:

1. **`sentilyzerd`** (Go) is the public gateway. It speaks REST/JSON,
   REST/XML, REST/YAML, gRPC, and GraphQL on top of a single
   `service.Service` and fans out to platform connectors with goroutines.
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

JSON, XML, and YAML share the **same** REST router; HTTP content
negotiation (Accept with q-values / `?format=`) flips the encoder.
YAML marshals through `sigs.k8s.io/yaml`, which routes via `encoding/json`
and so honors the same `json:` struct tags — YAML and JSON responses are
identical in shape by construction, with no `yaml:` tags to drift.
YAML is **output only**; see `server/rest.go:decodeRequest` for why.

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

Errors are classified once in `server/errors.go` and mapped to both HTTP
status codes and gRPC codes from that single table, so the two transports
cannot drift apart on what a given failure means.

`service.Service.fanout` runs every selected connector in parallel under a
per-connector deadline (`DefaultConnectorTimeout`, 3s), collects their
`SourcedDocument`s in registry order, dedupes by `(platform, document_id)`,
classifies them via the ML worker, then aggregates by platform and by aspect.

The deadline is per-connector rather than per-request because fanout waits on
every goroutine: without it, one slow third-party feed sets the latency of the
whole call. A connector that misses it is dropped and reported in
`Warnings`, and the response is flagged `Partial`.

The inference client splits work into batches the worker accepts and
reassembles them in the caller's order (`inference/batch.go`); the service
layer never sees the batching. Texts are grouped by length first, because the
worker pads every text in a batch out to the longest one in it.

Disabled connectors stay registered so `GET /v1/platforms` can show the
operator *why* (missing API key, instance unset, etc.).

## Caching and persistence

- **In-process LRU TTL cache** on `service.Service` keys topic-analysis
  results by `(topic, platforms, limit, language, since, aspects)`. TTL
  defaults to 10 min — long enough to absorb the burst of duplicate
  queries that follows a retry-storm, short enough that a freshly
  trending topic still produces fresh results.
- **No durable per-document storage.** An earlier SQLite store wrote one row
  per analyzed document; it was removed. Nothing ever read the table, and
  retaining third-party post text indefinitely conflicts with several source
  platforms' terms (YouTube caps Non-Authorized Data at 30 days and bars
  derived data; Reddit withholds rights for derived models). The 10-minute
  in-memory cache is now the only place third-party text lives, which keeps
  the gateway inside every source's retention rules by construction.
  See `docs/continuous-training-plan.md` for the audit and the corpus design
  that replaces it.

## Failure modes

| Failure                       | Behavior                                                    |
|-------------------------------|-------------------------------------------------------------|
| ML worker unreachable         | `/health` reports `ml_reachable=false`; analyses return `502` |
| Single connector errors/times out | Dropped after 3s; others still returned. Response is `partial: true` with a `warnings[]` entry naming it. Partial results are **not** cached |
| All connectors error          | `AnalyzeTopic` returns `502 Bad Gateway` with the error list |
| No connector enabled at all   | `503 Service Unavailable` — a misconfiguration, not a bad request |
| Request exceeds 15s           | `504 Gateway Timeout` (`middleware.Timeout`)                |
| Malformed request             | `400 Bad Request`                                            |
| YAML request body            | `415 Unsupported Media Type`                                 |
| Unsatisfiable `Accept`        | `406 Not Acceptable`                                         |
| HuggingFace download fails    | Operators run with `SENTILYZER_ML_USE_STUB=1` until fixed   |

## Roadmap

- Streaming `AnalyzeTopic` results via gRPC server-stream / GraphQL
  subscriptions — stream documents as platforms finish, instead of
  waiting on the slowest one.
- ONNX-quantized export of the general model to halve CPU latency.
- Per-platform rate limiting (token bucket) and circuit breakers.
- Multilingual mode using `twitter-xlm-roberta-base-sentiment`.
