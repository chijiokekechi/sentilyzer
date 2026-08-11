// Package server hosts the four protocol surfaces — REST/JSON, REST/XML,
// gRPC, and GraphQL — over a single Service.
package server

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"sigs.k8s.io/yaml"
)

// requestTimeout caps a whole HTTP request.
//
// It sits above the per-connector budget (service.DefaultConnectorTimeout,
// 3s) and the inference client's own deadline, so it is a backstop rather
// than the thing that normally fires. The previous 60s was ~6x the measured
// p99 of a cold uncached request, which meant a stuck request held a
// connection long past the point anyone was still waiting for it.
const requestTimeout = 15 * time.Second

// REST holds dependencies for the JSON/XML/YAML handlers. It's a single
// struct because they share routes, response types, and content negotiation —
// the only difference is the encoder used to write the response body.
type REST struct {
	Service   *service.Service
	Version   string
	RateLimit RateLimitConfig
}

func (r *REST) Router() http.Handler {
	rt := chi.NewRouter()
	rt.Use(middleware.RequestID)
	rt.Use(middleware.RealIP)
	rt.Use(middleware.Recoverer)
	rt.Use(WithCredentials)

	// Liveness and readiness sit OUTSIDE content negotiation, the request
	// timeout, and rate limiting: a container healthcheck sends no Accept
	// header and a probe steers by status code, so both must return a plain
	// 200/503 regardless of the API's negotiation and limiting rules.
	// /livez is deliberately independent of the ML worker — it answers "is
	// the gateway process up?", which is what a container restart should key
	// on (restarting the gateway does not fix an ML outage) — while /readyz
	// reflects ML reachability, which is what a load balancer should steer on.
	rt.Get("/livez", handleLivez)
	rt.Get("/readyz", r.handleReadyz)

	// Built once so both route groups below share the same buckets; calling
	// rateLimiters per group would hand each group a budget of its own.
	limiters := rateLimiters(r.RateLimit)
	api := func(offers []string, register func(chi.Router)) {
		rt.Group(func(gr chi.Router) {
			gr.Use(middleware.Timeout(requestTimeout))
			gr.Use(noStore)
			gr.Use(negotiateMedia(offers))
			for _, mw := range limiters {
				gr.Use(mw)
			}
			register(gr)
		})
	}
	api(offers, func(gr chi.Router) {
		gr.Get("/health", r.handleHealth)
		gr.Get("/v1/platforms", r.handleListPlatforms)
	})
	// The analyze endpoints additionally offer the row-per-document export
	// formats (CSV, NDJSON); see export.go.
	api(analyzeOffers, func(gr chi.Router) {
		gr.Post("/v1/analyze/text", r.handleAnalyzeText)
		gr.Post("/v1/analyze/topic", r.handleAnalyzeTopic)
		// GET form for quick smoke-testing, e.g.
		//   curl /v1/analyze/topic?topic=Robinhood&platforms=hackernews,rss&limit=5
		gr.Get("/v1/analyze/topic", r.handleAnalyzeTopicGET)
	})
	return rt
}

// handleLivez reports that the gateway process is up and serving HTTP. It does
// not consult the ML worker: liveness answers "should this container be
// restarted?", and a restart cannot fix an ML outage — failing it on ML
// reachability would crash-loop a healthy gateway.
func handleLivez(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleReadyz reports whether the service can actually serve analyses — i.e.
// the ML worker is reachable. A probe steers on this: 200 = send traffic,
// 503 = don't.
func (r *REST) handleReadyz(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	info, err := r.Service.Health(req.Context())
	if err != nil || !info.MLReachable {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready: ml worker unreachable\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

// --- handlers ---------------------------------------------------------------

func (r *REST) handleHealth(w http.ResponseWriter, req *http.Request) {
	info, err := r.Service.Health(req.Context())
	if err != nil {
		writeError(w, req, err)
		return
	}
	info.Version = r.Version
	writePayload(w, req, http.StatusOK, info)
}

func (r *REST) handleListPlatforms(w http.ResponseWriter, req *http.Request) {
	infos := r.Service.ListPlatforms()
	writePayload(w, req, http.StatusOK, struct {
		XMLName   xml.Name              `json:"-" xml:"platforms"`
		Platforms []domain.PlatformInfo `json:"platforms" xml:"platform"`
	}{Platforms: infos})
}

type analyzeTextDTO struct {
	XMLName        xml.Name          `json:"-" xml:"analyze_text"`
	Documents      []domain.Document `json:"documents" xml:"documents>document"`
	IncludeAspects bool              `json:"include_aspects" xml:"include_aspects"`
}

func (r *REST) handleAnalyzeText(w http.ResponseWriter, req *http.Request) {
	var body analyzeTextDTO
	if err := decodeRequest(req, &body); err != nil {
		writeError(w, req, err)
		return
	}
	if len(body.Documents) == 0 {
		writeError(w, req, fmt.Errorf("%w: documents must not be empty", service.ErrInvalidRequest))
		return
	}
	resp, err := r.Service.AnalyzeText(req.Context(), service.AnalyzeTextRequest{
		Documents:      body.Documents,
		IncludeAspects: body.IncludeAspects,
	})
	if err != nil {
		writeError(w, req, err)
		return
	}
	switch formatFrom(req.Context()) {
	case mimeCSV:
		writeTextCSV(w, resp.Results)
	case mimeNDJSON:
		writeTextNDJSON(w, resp.Results)
	default:
		writePayload(w, req, http.StatusOK, struct {
			XMLName   xml.Name                `json:"-" xml:"analyze_text_response"`
			Results   []domain.DocumentResult `json:"results" xml:"results>result"`
			Aggregate domain.Aggregate        `json:"aggregate" xml:"aggregate"`
		}{Results: resp.Results, Aggregate: resp.Aggregate})
	}
}

type analyzeTopicDTO struct {
	XMLName          xml.Name `json:"-" xml:"analyze_topic"`
	Topic            string   `json:"topic" xml:"topic"`
	Platforms        []string `json:"platforms,omitempty" xml:"platforms>platform,omitempty"`
	LimitPerPlatform int      `json:"limit_per_platform,omitempty" xml:"limit_per_platform,omitempty"`
	Aspects          []string `json:"aspects,omitempty" xml:"aspects>aspect,omitempty"`
	Language         string   `json:"language,omitempty" xml:"language,omitempty"`
	SinceSeconds     int64    `json:"since_seconds,omitempty" xml:"since_seconds,omitempty"`
	// Best-effort keyword location filter; see connectors/location.go.
	Location string `json:"location,omitempty" xml:"location,omitempty"`
}

func (r *REST) handleAnalyzeTopic(w http.ResponseWriter, req *http.Request) {
	var body analyzeTopicDTO
	if err := decodeRequest(req, &body); err != nil {
		writeError(w, req, err)
		return
	}
	r.runTopic(w, req, body)
}

func (r *REST) handleAnalyzeTopicGET(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	body := analyzeTopicDTO{
		Topic:    q.Get("topic"),
		Language: q.Get("language"),
		Location: q.Get("location"),
	}
	if v := q.Get("platforms"); v != "" {
		body.Platforms = splitAndTrim(v)
	}
	if v := q.Get("aspects"); v != "" {
		body.Aspects = splitAndTrim(v)
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			body.LimitPerPlatform = n
		}
	}
	if v := q.Get("since_seconds"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			body.SinceSeconds = n
		}
	}
	r.runTopic(w, req, body)
}

func (r *REST) runTopic(w http.ResponseWriter, req *http.Request, body analyzeTopicDTO) {
	if body.Topic == "" {
		writeError(w, req, service.ErrTopicRequired)
		return
	}
	resp, err := r.Service.AnalyzeTopic(req.Context(), service.AnalyzeTopicRequest{
		Topic:            body.Topic,
		Platforms:        body.Platforms,
		LimitPerPlatform: body.LimitPerPlatform,
		Aspects:          body.Aspects,
		Language:         body.Language,
		SinceSeconds:     body.SinceSeconds,
		Location:         body.Location,
		Creds:            CredsFromContext(req.Context()),
	})
	if err != nil {
		writeError(w, req, err)
		return
	}
	switch formatFrom(req.Context()) {
	case mimeCSV:
		writeTopicCSV(w, resp)
	case mimeNDJSON:
		writeTopicNDJSON(w, resp)
	default:
		writePayload(w, req, http.StatusOK, asTopicEnvelope(resp))
	}
}

// asTopicEnvelope wraps the analysis in a struct that XML-encodes cleanly
// (Go's encoding/xml can't serialize map[string]X, so we project the by_*
// fields into slices of explicit pairs).
func asTopicEnvelope(a *domain.SourcedAnalysis) any {
	type kv struct {
		Key       string           `json:"-" xml:"key,attr"`
		Aggregate domain.Aggregate `json:"-" xml:",chardata"`
	}
	type breakdown struct {
		Key       string           `json:"-" xml:"key,attr"`
		Aggregate domain.Aggregate `json:",inline" xml:",inline"`
	}
	_ = kv{}
	type wrap struct {
		XMLName    xml.Name                       `json:"-" xml:"analyze_topic_response"`
		Topic      string                         `json:"topic" xml:"topic"`
		Results    []domain.SourcedDocumentResult `json:"results" xml:"results>result"`
		Aggregate  domain.Aggregate               `json:"aggregate" xml:"aggregate"`
		ByPlatform map[string]domain.Aggregate    `json:"by_platform" xml:"-"`
		ByAspect   map[string]domain.Aggregate    `json:"by_aspect" xml:"-"`
		Partial    bool                           `json:"partial" xml:"partial,attr"`
		Warnings   []domain.Warning               `json:"warnings,omitempty" xml:"warnings>warning,omitempty"`
		// XML-friendly projections:
		ByPlatformXML []breakdown `json:"-" xml:"by_platform>platform,omitempty"`
		ByAspectXML   []breakdown `json:"-" xml:"by_aspect>aspect,omitempty"`
	}
	bp := make([]breakdown, 0, len(a.ByPlatform))
	for k, v := range a.ByPlatform {
		bp = append(bp, breakdown{Key: k, Aggregate: v})
	}
	ba := make([]breakdown, 0, len(a.ByAspect))
	for k, v := range a.ByAspect {
		ba = append(ba, breakdown{Key: k, Aggregate: v})
	}
	return wrap{
		Topic:         a.Topic,
		Results:       a.Results,
		Aggregate:     a.Aggregate,
		ByPlatform:    a.ByPlatform,
		ByAspect:      a.ByAspect,
		Partial:       a.Partial,
		Warnings:      a.Warnings,
		ByPlatformXML: bp,
		ByAspectXML:   ba,
	}
}

// --- encoding ---------------------------------------------------------------

// decodeRequest parses a request body as JSON (the default) or XML.
//
// YAML is deliberately NOT accepted, even though it is offered for responses.
// sigs.k8s.io/yaml routes through YAML 1.1, whose implicit typing silently
// coerces unquoted scalars — measured against these very DTOs, every one of
// these parses with a nil error and no signal:
//
//	topic: no    -> Topic = "false"   (then fans out searching for "false")
//	text: y      -> Text  = "true"
//	text: 013    -> Text  = "11"      (octal)
//	language: no -> Language = "false" ("no" is ISO 639-1 Norwegian)
//
// The trap covers every string field, and it is unguardable on `text`
// specifically: this API scores terse social snippets, where "no", "y", and
// "on" are not adversarial inputs — they are the corpus. JSON has no
// equivalent silent-coercion class ({"text": no} is a syntax error, not a
// boolean). Accepting YAML input is a contract that cannot be withdrawn
// later; offering YAML output can always be relaxed into it.
func decodeRequest(req *http.Request, dst any) error {
	media, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	switch canonicalMedia(media) {
	case mimeXML:
		if err := xml.NewDecoder(req.Body).Decode(dst); err != nil {
			return fmt.Errorf("%w: %w", service.ErrInvalidRequest, err)
		}
		return nil
	case mimeYAML:
		return errYAMLRequestBody
	default:
		dec := json.NewDecoder(req.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			return fmt.Errorf("%w: %w", service.ErrInvalidRequest, err)
		}
		return nil
	}
}

func writePayload(w http.ResponseWriter, req *http.Request, status int, payload any) {
	writePayloadAs(w, formatFrom(req.Context()), status, payload)
}

func writePayloadAs(w http.ResponseWriter, format string, status int, payload any) {
	// Successful CSV responses are written by export.go, never through here, so
	// a CSV-negotiated payload on this path is an error body. CSV has no shape
	// for one (a bare header row would read as an empty result set), so CSV
	// clients get their errors as JSON. NDJSON needs no such downgrade: an
	// error object on a single line is valid NDJSON.
	if format == mimeCSV {
		format = mimeJSON
	}
	w.Header().Set("Content-Type", contentType(format))
	w.WriteHeader(status)
	switch format {
	case mimeXML:
		_, _ = w.Write([]byte(xml.Header))
		_ = xml.NewEncoder(w).Encode(payload)
	case mimeYAML:
		// sigs.k8s.io/yaml marshals via encoding/json, so it honors the same
		// `json:` tags and omitempty rules the JSON encoder does. YAML output
		// is therefore identical in shape to JSON by construction, with no
		// `yaml:` tags anywhere in the domain types to drift out of sync.
		if b, err := yaml.Marshal(payload); err == nil {
			_, _ = w.Write(b)
		}
	case mimeNDJSON:
		// Error bodies only; one compact object on one line.
		_ = json.NewEncoder(w).Encode(payload)
	default:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload)
	}
}

type errResp struct {
	XMLName xml.Name `json:"-" xml:"error"`
	Error   string   `json:"error" xml:"message"`
	Status  int      `json:"status" xml:"status,attr"`
}

// writeError renders err in the negotiated format, with the status its
// category earns. Handlers pass the error and stay out of the mapping.
func writeError(w http.ResponseWriter, req *http.Request, err error) {
	status := httpStatusFor(err)
	writePayload(w, req, status, errResp{Error: err.Error(), Status: status})
}

// writeErrorAs is for the paths that run before a format is negotiated — it
// cannot consult the request context, so the caller names the encoding.
func writeErrorAs(w http.ResponseWriter, format string, status int, msg string) {
	writePayloadAs(w, format, status, errResp{Error: msg, Status: status})
}

// noStore prevents caches from holding API responses (analysis results carry
// timestamps and are not cacheable HTTP-cache-wise; our own LRU lives inside).
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
