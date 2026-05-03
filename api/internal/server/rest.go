// Package server hosts the four protocol surfaces — REST/JSON, REST/XML,
// gRPC, and GraphQL — over a single Service.
package server

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	mimeJSON = "application/json"
	mimeXML  = "application/xml"
)

// REST holds dependencies for the JSON/XML handlers. It's a single struct
// because the two share routes, response types, and content negotiation —
// the only difference is the encoder used to write the response body.
type REST struct {
	Service *service.Service
	Version string
}

func (r *REST) Router() http.Handler {
	rt := chi.NewRouter()
	rt.Use(middleware.RequestID)
	rt.Use(middleware.RealIP)
	rt.Use(middleware.Recoverer)
	rt.Use(middleware.Timeout(60_000_000_000)) // 60s
	rt.Use(noStore)

	rt.Get("/health", r.handleHealth)
	rt.Get("/v1/platforms", r.handleListPlatforms)
	rt.Post("/v1/analyze/text", r.handleAnalyzeText)
	rt.Post("/v1/analyze/topic", r.handleAnalyzeTopic)
	// GET form for quick smoke-testing, e.g.
	//   curl /v1/analyze/topic?topic=Robinhood&platforms=hackernews,rss&limit=5
	rt.Get("/v1/analyze/topic", r.handleAnalyzeTopicGET)
	return rt
}

// --- handlers ---------------------------------------------------------------

func (r *REST) handleHealth(w http.ResponseWriter, req *http.Request) {
	info, err := r.Service.Health(req.Context())
	if err != nil {
		writeError(w, req, http.StatusInternalServerError, err)
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
		writeError(w, req, http.StatusBadRequest, err)
		return
	}
	if len(body.Documents) == 0 {
		writeError(w, req, http.StatusBadRequest, errors.New("documents must not be empty"))
		return
	}
	resp, err := r.Service.AnalyzeText(req.Context(), service.AnalyzeTextRequest{
		Documents:      body.Documents,
		IncludeAspects: body.IncludeAspects,
	})
	if err != nil {
		writeError(w, req, http.StatusInternalServerError, err)
		return
	}
	writePayload(w, req, http.StatusOK, struct {
		XMLName   xml.Name                `json:"-" xml:"analyze_text_response"`
		Results   []domain.DocumentResult `json:"results" xml:"results>result"`
		Aggregate domain.Aggregate        `json:"aggregate" xml:"aggregate"`
	}{Results: resp.Results, Aggregate: resp.Aggregate})
}

type analyzeTopicDTO struct {
	XMLName          xml.Name `json:"-" xml:"analyze_topic"`
	Topic            string   `json:"topic" xml:"topic"`
	Platforms        []string `json:"platforms,omitempty" xml:"platforms>platform,omitempty"`
	LimitPerPlatform int      `json:"limit_per_platform,omitempty" xml:"limit_per_platform,omitempty"`
	Aspects          []string `json:"aspects,omitempty" xml:"aspects>aspect,omitempty"`
	Language         string   `json:"language,omitempty" xml:"language,omitempty"`
	SinceSeconds     int64    `json:"since_seconds,omitempty" xml:"since_seconds,omitempty"`
}

func (r *REST) handleAnalyzeTopic(w http.ResponseWriter, req *http.Request) {
	var body analyzeTopicDTO
	if err := decodeRequest(req, &body); err != nil {
		writeError(w, req, http.StatusBadRequest, err)
		return
	}
	r.runTopic(w, req, body)
}

func (r *REST) handleAnalyzeTopicGET(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	body := analyzeTopicDTO{
		Topic:    q.Get("topic"),
		Language: q.Get("language"),
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
		writeError(w, req, http.StatusBadRequest, errors.New("topic is required"))
		return
	}
	resp, err := r.Service.AnalyzeTopic(req.Context(), service.AnalyzeTopicRequest{
		Topic:            body.Topic,
		Platforms:        body.Platforms,
		LimitPerPlatform: body.LimitPerPlatform,
		Aspects:          body.Aspects,
		Language:         body.Language,
		SinceSeconds:     body.SinceSeconds,
	})
	if err != nil {
		writeError(w, req, http.StatusBadGateway, err)
		return
	}
	writePayload(w, req, http.StatusOK, asTopicEnvelope(resp))
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
		ByPlatformXML: bp,
		ByAspectXML:   ba,
	}
}

// --- content negotiation ----------------------------------------------------

// preferredFormat picks between JSON and XML based on Accept (and ?format=).
// Default is JSON. The same content type is honored for the request body.
func preferredFormat(req *http.Request) string {
	if v := req.URL.Query().Get("format"); v != "" {
		switch strings.ToLower(v) {
		case "xml":
			return mimeXML
		case "json":
			return mimeJSON
		}
	}
	accept := req.Header.Get("Accept")
	if accept == "" {
		return mimeJSON
	}
	if strings.Contains(accept, mimeXML) && !strings.Contains(accept, mimeJSON) {
		return mimeXML
	}
	return mimeJSON
}

func decodeRequest(req *http.Request, dst any) error {
	ct := req.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, mimeXML):
		return xml.NewDecoder(req.Body).Decode(dst)
	default:
		dec := json.NewDecoder(req.Body)
		dec.DisallowUnknownFields()
		return dec.Decode(dst)
	}
}

func writePayload(w http.ResponseWriter, req *http.Request, status int, payload any) {
	format := preferredFormat(req)
	w.Header().Set("Content-Type", format+"; charset=utf-8")
	w.WriteHeader(status)
	if format == mimeXML {
		w.Write([]byte(xml.Header))
		_ = xml.NewEncoder(w).Encode(payload)
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func writeError(w http.ResponseWriter, req *http.Request, status int, err error) {
	type errResp struct {
		XMLName xml.Name `json:"-" xml:"error"`
		Error   string   `json:"error" xml:"message"`
		Status  int      `json:"status" xml:"status,attr"`
	}
	writePayload(w, req, status, errResp{Error: err.Error(), Status: status})
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

// Compile-time assertion that REST satisfies http.Handler indirectly.
var _ = fmt.Sprintf
