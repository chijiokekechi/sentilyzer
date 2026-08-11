#!/usr/bin/env bash
# Full-stack smoke test, localhost only: boots the ML worker in stub mode and
# the gateway with the mock connector, exercises every protocol surface, and
# always tears both processes down. Needs no Modal, no GPU, and no network
# beyond loopback. Dedicated high ports so a dev stack on the default
# 8080/9090/50051 keeps running.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

HTTP_PORT=18098
GRPC_PORT=19091
ML_PORT=15051
BASE="http://127.0.0.1:${HTTP_PORT}"
HEALTH_TIMEOUT_S=60

# Same toolchain knobs the Makefile uses.
PYTHON="${PYTHON:-python3}"
GO="${GO:-go}"

CURL=(curl -sS --max-time 10)
JQ="$(command -v jq || true)"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/sentilyzer-e2e.XXXXXX")"
ML_PID=""
API_PID=""

PASSED=0
FAILED=0
SKIPPED=0

# Teardown must run on every exit path so no server outlives the script.
cleanup() {
  local pid
  for pid in "$API_PID" "$ML_PID"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

die() {
  echo "e2e: $*" >&2
  exit 1
}

dump_logs() {
  local f
  for f in "$WORK/ml.log" "$WORK/api.log"; do
    if [[ -s "$f" ]]; then
      echo "---- ${f##*/} (last 20 lines) ----" >&2
      tail -n 20 "$f" >&2
    fi
  done
}

pass() { PASSED=$((PASSED + 1)); echo "PASS $1"; }
fail() { FAILED=$((FAILED + 1)); echo "FAIL $1"; }
skip() { SKIPPED=$((SKIPPED + 1)); echo "SKIP $1 ($2)"; }

# Runs one named assertion. On failure the assertion's output is shown
# indented and the run continues, so the summary covers every check.
check() {
  local name="$1"
  shift
  if "$@" >"$WORK/check.out" 2>&1; then
    pass "$name"
  else
    fail "$name"
    sed 's/^/    /' "$WORK/check.out"
  fi
}

# --- request bodies ----------------------------------------------------------

# Stub-lexicon texts with known labels, so assertions can pin the sentiment.
TEXT_2DOCS='{"documents":[{"text":"I love this app, it is amazing"},{"text":"terrible service, awful support"}]}'
TEXT_3DOCS='{"documents":[{"text":"I love this app, it is amazing"},{"text":"terrible service, awful support"},{"text":"it exists"}]}'
XML_1DOC='<analyze_text><documents><document><text>terrible service</text></document></documents></analyze_text>'
TOPIC_BODY='{"topic":"Robinhood","platforms":["mock"],"limit_per_platform":5}'
GRAPHQL_BODY='{"query":"{ analyzeTopic(topic:\"Robinhood\", platforms:[\"mock\"], limitPerPlatform:5) { topic aggregate { modalLabel meanPolarity sampleSize } } }"}'

post_json() {
  local body="$1"
  shift
  "${CURL[@]}" -f -X POST -H 'Content-Type: application/json' -d "$body" "$@"
}

# --- assertions --------------------------------------------------------------

check_health() {
  local body
  body="$("${CURL[@]}" -f "$BASE/health")" || return 1
  if [[ -n "$JQ" ]]; then
    "$JQ" -e '.healthy == true and .ml_reachable == true' <<<"$body" >/dev/null && return 0
  else
    grep -q '"healthy": *true' <<<"$body" \
      && grep -q '"ml_reachable": *true' <<<"$body" && return 0
  fi
  echo "$body"
  return 1
}

check_platforms() {
  local body
  body="$("${CURL[@]}" -f "$BASE/v1/platforms")" || return 1
  if [[ -n "$JQ" ]]; then
    "$JQ" -e '[.platforms[] | select(.id == "mock" and .enabled)] | length == 1' \
      <<<"$body" >/dev/null && return 0
  else
    grep -q '"id": *"mock"' <<<"$body" && return 0
  fi
  echo "$body"
  return 1
}

check_text_json() {
  local body
  body="$(post_json "$TEXT_2DOCS" "$BASE/v1/analyze/text")" || return 1
  if [[ -n "$JQ" ]]; then
    "$JQ" -e '(.results | length == 2)
      and .results[0].overall.label == "positive"
      and .results[1].overall.label == "negative"
      and .aggregate.sample_size == 2' <<<"$body" >/dev/null && return 0
  else
    grep -q '"label": *"positive"' <<<"$body" \
      && grep -q '"label": *"negative"' <<<"$body" \
      && grep -q '"sample_size": *2' <<<"$body" && return 0
  fi
  echo "$body"
  return 1
}

check_text_xml_accept() {
  local body
  body="$("${CURL[@]}" -f -X POST "$BASE/v1/analyze/text" \
    -H 'Content-Type: application/xml' -H 'Accept: application/xml' \
    -d "$XML_1DOC")" || return 1
  grep -q '<analyze_text_response>' <<<"$body" \
    && grep -q '<label>negative</label>' <<<"$body" && return 0
  echo "$body"
  return 1
}

check_text_xml_param() {
  local body
  body="$(post_json "$TEXT_2DOCS" "$BASE/v1/analyze/text?format=xml")" || return 1
  grep -q '<analyze_text_response>' <<<"$body" \
    && grep -q '<label>positive</label>' <<<"$body" && return 0
  echo "$body"
  return 1
}

check_text_yaml_accept() {
  local body
  body="$(post_json "$TEXT_2DOCS" -H 'Accept: application/yaml' \
    "$BASE/v1/analyze/text")" || return 1
  grep -q '^results:' <<<"$body" \
    && grep -q 'label: positive' <<<"$body" \
    && grep -q 'label: negative' <<<"$body" && return 0
  echo "$body"
  return 1
}

check_text_yaml_param() {
  local body
  body="$(post_json "$TEXT_2DOCS" "$BASE/v1/analyze/text?format=yaml")" || return 1
  grep -q '^results:' <<<"$body" && grep -q 'label: positive' <<<"$body" && return 0
  echo "$body"
  return 1
}

check_text_csv() {
  local body header lines
  body="$(post_json "$TEXT_3DOCS" -H 'Accept: text/csv' \
    "$BASE/v1/analyze/text")" || return 1
  header="$(head -n 1 <<<"$body" | tr -d '\r')"
  lines="$(wc -l <<<"$body" | tr -d ' ')"
  [[ "$header" == "index,label,confidence,polarity,p_negative,p_neutral,p_positive" ]] \
    && [[ "$lines" == "4" ]] && return 0
  echo "want the documented header plus 3 rows, got ${lines} lines:"
  echo "$body"
  return 1
}

check_text_ndjson() {
  local body lines
  body="$(post_json "$TEXT_3DOCS" "$BASE/v1/analyze/text?format=ndjson")" || return 1
  lines="$(wc -l <<<"$body" | tr -d ' ')"
  if [[ "$lines" != "3" ]]; then
    echo "want 3 lines, got ${lines}:"
    echo "$body"
    return 1
  fi
  if [[ -n "$JQ" ]]; then
    "$JQ" -es 'length == 3 and all(.[]; has("overall"))' <<<"$body" >/dev/null \
      || { echo "$body"; return 1; }
  fi
}

check_topic_json() {
  local body
  body="$(post_json "$TOPIC_BODY" "$BASE/v1/analyze/topic")" || return 1
  if [[ -n "$JQ" ]]; then
    "$JQ" -e '.topic == "Robinhood"
      and (.results | length == 5)
      and (.results | all(.document.platform == "mock"))
      and .aggregate.sample_size == 5
      and (.aggregate.modal_label | IN("negative", "neutral", "positive"))
      and (.by_platform | has("mock"))' <<<"$body" >/dev/null && return 0
  else
    grep -q '"topic": *"Robinhood"' <<<"$body" \
      && grep -q '"platform": *"mock"' <<<"$body" \
      && grep -q '"sample_size": *5' <<<"$body" \
      && grep -q '"modal_label"' <<<"$body" && return 0
  fi
  echo "$body"
  return 1
}

check_graphql() {
  local body
  body="$("${CURL[@]}" -f -X POST -H 'Content-Type: application/json' \
    -d "$GRAPHQL_BODY" "$BASE/graphql")" || return 1
  if [[ -n "$JQ" ]]; then
    "$JQ" -e '(.errors // []) == []
      and .data.analyzeTopic.topic == "Robinhood"
      and .data.analyzeTopic.aggregate.sampleSize == 5' <<<"$body" >/dev/null && return 0
  else
    grep -q '"analyzeTopic"' <<<"$body" \
      && grep -q '"sampleSize": *5' <<<"$body" \
      && ! grep -q '"errors"' <<<"$body" && return 0
  fi
  echo "$body"
  return 1
}

check_unsupported_accept() {
  local code
  code="$("${CURL[@]}" -o /dev/null -w '%{http_code}' \
    -H 'Accept: text/csv' "$BASE/v1/platforms")" || return 1
  [[ "$code" == "406" ]] || { echo "want HTTP 406, got $code"; return 1; }
}

check_bad_format() {
  local code
  code="$("${CURL[@]}" -o /dev/null -w '%{http_code}' \
    "$BASE/v1/platforms?format=bogus")" || return 1
  [[ "$code" == "400" ]] || { echo "want HTTP 400, got $code"; return 1; }
}

check_grpc() {
  # No server reflection is registered, so grpcurl needs the proto source.
  local body
  body="$(grpcurl -plaintext \
    -import-path "$ROOT/proto" -proto sentilyzer/v1/sentilyzer.proto \
    -d '{"documents":[{"text":"loved every minute"}]}' \
    "127.0.0.1:${GRPC_PORT}" sentilyzer.v1.SentilyzerService/AnalyzeText)" || return 1
  grep -q '"results"' <<<"$body" \
    && grep -q 'SENTIMENT_POSITIVE' <<<"$body" && return 0
  echo "$body"
  return 1
}

# --- boot --------------------------------------------------------------------

echo "e2e: estimated runtime: about one minute"

command -v curl >/dev/null 2>&1 || die "curl not found on PATH"
command -v "$GO" >/dev/null 2>&1 || die "go not found on PATH"
# Import from ml/ the way make ml-run does; server pulls in the gen stubs,
# so a missing `make proto` or `make ml-install` fails here, not mid-boot.
(cd "$ROOT/ml" && "$PYTHON" -c 'import sentilyzer_ml.server') 2>/dev/null \
  || die "sentilyzer_ml not importable by ${PYTHON}; run: make proto && make ml-install"
[[ -n "$JQ" ]] || echo "e2e: jq not found, falling back to grep assertions"

echo "e2e: building gateway"
(cd "$ROOT/api" && "$GO" build -o "$WORK/sentilyzerd" ./cmd/sentilyzerd)

echo "e2e: starting ml worker (stub) on 127.0.0.1:${ML_PORT}"
(cd "$ROOT/ml" && exec env \
  SENTILYZER_ML_USE_STUB=1 \
  SENTILYZER_ML_LISTEN="127.0.0.1:${ML_PORT}" \
  "$PYTHON" -m sentilyzer_ml.server) >"$WORK/ml.log" 2>&1 &
ML_PID=$!

echo "e2e: starting gateway on 127.0.0.1:${HTTP_PORT} (grpc ${GRPC_PORT})"
env SENTILYZER_HTTP_ADDR="127.0.0.1:${HTTP_PORT}" \
  SENTILYZER_GRPC_ADDR="127.0.0.1:${GRPC_PORT}" \
  SENTILYZER_ML_ADDR="127.0.0.1:${ML_PORT}" \
  SENTILYZER_USE_MOCK=true \
  "$WORK/sentilyzerd" >"$WORK/api.log" 2>&1 &
API_PID=$!

# /health is behind the per-IP rate limit (default 120/min), so poll at 1s to
# leave the checks below headroom inside the same minute.
echo "e2e: waiting for /health to report ml_reachable (timeout ${HEALTH_TIMEOUT_S}s)"
deadline=$((SECONDS + HEALTH_TIMEOUT_S))
ready=""
while ((SECONDS < deadline)); do
  kill -0 "$ML_PID" 2>/dev/null \
    || { dump_logs; die "ml worker exited during startup (log above)"; }
  kill -0 "$API_PID" 2>/dev/null \
    || { dump_logs; die "gateway exited during startup (log above)"; }
  body="$("${CURL[@]}" --max-time 2 "$BASE/health" 2>/dev/null || true)"
  if grep -q '"ml_reachable": *true' <<<"$body"; then
    ready=1
    break
  fi
  sleep 1
done
if [[ -z "$ready" ]]; then
  dump_logs
  die "stack not healthy after ${HEALTH_TIMEOUT_S}s: ${BASE}/health never reported ml_reachable=true"
fi

# --- checks ------------------------------------------------------------------

check health check_health
check platforms-lists-mock check_platforms
check analyze-text-json check_text_json
check analyze-text-xml-accept check_text_xml_accept
check analyze-text-xml-format-param check_text_xml_param
check analyze-text-yaml-accept check_text_yaml_accept
check analyze-text-yaml-format-param check_text_yaml_param
check analyze-text-csv check_text_csv
check analyze-text-ndjson check_text_ndjson
check analyze-topic-json check_topic_json
check graphql-analyze-topic check_graphql
check unsupported-accept-406 check_unsupported_accept
check bad-format-param-400 check_bad_format
if command -v grpcurl >/dev/null 2>&1; then
  check grpc-analyze-text check_grpc
else
  skip grpc-analyze-text "grpcurl not on PATH"
fi

echo
echo "e2e: ${PASSED} passed, ${FAILED} failed, ${SKIPPED} skipped in ${SECONDS}s"
if ((FAILED > 0)); then
  exit 1
fi
echo "e2e: OK"
