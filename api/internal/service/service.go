// Package service contains the protocol-agnostic business logic. The REST,
// XML, gRPC, and GraphQL handlers are all thin adapters over this surface.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/cache"
	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
)

// DefaultConnectorTimeout bounds how long any single connector may take
// before fanout gives up on it and reports it as a warning.
const DefaultConnectorTimeout = 3 * time.Second

// Sentinel errors, so transports can map a failure onto a protocol-correct
// status without matching on message text. Wrap them with %w rather than
// rebuilding the string.
var (
	// ErrInvalidRequest marks anything malformed the caller sent. Wrap it to
	// add specifics: fmt.Errorf("%w: ...", service.ErrInvalidRequest).
	ErrInvalidRequest = errors.New("invalid request")
	// ErrTopicRequired means the caller sent no topic. Their mistake.
	ErrTopicRequired = errors.New("topic is required")
	// ErrNoPlatforms means the server has no usable connector. Our problem,
	// not the caller's — no retry or reworded request will help.
	ErrNoPlatforms = errors.New("no platforms enabled — set credentials or SENTILYZER_USE_MOCK=true")
	// ErrAllConnectorsFailed means every upstream we asked returned an error.
	ErrAllConnectorsFailed = errors.New("all connectors failed")
)

// Service is the central business logic component.
type Service struct {
	Inference  inference.Client
	Connectors *connectors.Registry
	Cache      *cache.TTLCache[string, *domain.SourcedAnalysis]
	Now        func() time.Time
	// ConnectorTimeout overrides DefaultConnectorTimeout. Zero means default.
	ConnectorTimeout time.Duration
}

// New builds a Service. The cache is created with the provided TTL.
func New(
	inf inference.Client,
	reg *connectors.Registry,
	cacheTTL time.Duration,
) (*Service, error) {
	if inf == nil {
		return nil, errors.New("service: inference client required")
	}
	if reg == nil {
		return nil, errors.New("service: connector registry required")
	}
	c, err := cache.New[string, *domain.SourcedAnalysis](512, cacheTTL)
	if err != nil {
		return nil, err
	}
	return &Service{
		Inference:  inf,
		Connectors: reg,
		Cache:      c,
		Now:        time.Now,
	}, nil
}

// AnalyzeTextRequest captures inline-document analysis parameters.
type AnalyzeTextRequest struct {
	Documents      []domain.Document
	IncludeAspects bool
}

// AnalyzeTextResponse is the result of analyzing a batch of inline docs.
type AnalyzeTextResponse struct {
	Results   []domain.DocumentResult
	Aggregate domain.Aggregate
}

func (s *Service) AnalyzeText(ctx context.Context, req AnalyzeTextRequest) (*AnalyzeTextResponse, error) {
	if len(req.Documents) == 0 {
		return &AnalyzeTextResponse{Aggregate: domain.NewAggregator().Aggregate()}, nil
	}
	texts := make([]string, len(req.Documents))
	for i, d := range req.Documents {
		texts[i] = d.Text
	}
	scores, err := s.Inference.Classify(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}
	if len(scores) != len(req.Documents) {
		return nil, fmt.Errorf("inference returned %d scores for %d documents", len(scores), len(req.Documents))
	}

	var aspectsByDoc [][]domain.AspectScore
	if req.IncludeAspects {
		aspectInputs := make([]inference.AspectInput, 0, len(req.Documents))
		positions := make([]int, 0, len(req.Documents))
		for i, d := range req.Documents {
			if len(d.Aspects) == 0 {
				continue
			}
			aspectInputs = append(aspectInputs, inference.AspectInput{Text: d.Text, Aspects: d.Aspects})
			positions = append(positions, i)
		}
		if len(aspectInputs) > 0 {
			batch, err := s.Inference.ClassifyAspects(ctx, aspectInputs)
			if err != nil {
				return nil, fmt.Errorf("classify aspects: %w", err)
			}
			aspectsByDoc = make([][]domain.AspectScore, len(req.Documents))
			for offset, pos := range positions {
				if offset < len(batch) {
					aspectsByDoc[pos] = batch[offset]
				}
			}
		}
	}

	now := s.Now().UTC()
	results := make([]domain.DocumentResult, len(req.Documents))
	agg := domain.NewAggregator()
	for i, d := range req.Documents {
		var aspects []domain.AspectScore
		if i < len(aspectsByDoc) {
			aspects = aspectsByDoc[i]
		}
		results[i] = domain.DocumentResult{
			ID:         d.ID,
			Overall:    scores[i],
			Aspects:    aspects,
			AnalyzedAt: now,
		}
		agg.Add(scores[i])
	}
	return &AnalyzeTextResponse{Results: results, Aggregate: agg.Aggregate()}, nil
}

// AnalyzeTopicRequest mirrors the proto message but stays in domain types.
type AnalyzeTopicRequest struct {
	Topic            string
	Platforms        []string
	LimitPerPlatform int
	Aspects          []string
	Language         string
	SinceSeconds     int64
	// Location is a best-effort keyword filter; how each connector maps it
	// onto its platform query lives in connectors/location.go.
	Location string
	// Creds are caller-supplied API keys ("bring your own key"). They enable
	// keyed connectors the server has no credentials for, for this request
	// only. Never logged, never persisted; the cache key carries only their
	// one-way fingerprint.
	Creds connectors.Credentials
}

// AnalyzeTopic gathers posts and runs analysis.
func (s *Service) AnalyzeTopic(ctx context.Context, req AnalyzeTopicRequest) (*domain.SourcedAnalysis, error) {
	if strings.TrimSpace(req.Topic) == "" {
		return nil, ErrTopicRequired
	}
	if req.LimitPerPlatform <= 0 {
		req.LimitPerPlatform = 20
	}

	cacheKey := buildCacheKey(req)
	if hit, ok := s.Cache.Get(cacheKey); ok {
		return hit, nil
	}

	platforms := s.selectPlatforms(req.Platforms, req.Creds)
	if len(platforms) == 0 {
		return nil, ErrNoPlatforms
	}

	docs, warnings, err := s.fanout(ctx, platforms, req)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return &domain.SourcedAnalysis{
			Topic:      req.Topic,
			ByPlatform: map[string]domain.Aggregate{},
			ByAspect:   map[string]domain.Aggregate{},
			Partial:    len(warnings) > 0,
			Warnings:   warnings,
		}, nil
	}

	docs = dedupeByID(docs)
	texts := make([]string, len(docs))
	for i, d := range docs {
		texts[i] = d.Document.Text
	}

	scores, err := s.Inference.Classify(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}

	aspectsByDoc := make([][]domain.AspectScore, len(docs))
	if len(req.Aspects) > 0 {
		inputs := make([]inference.AspectInput, len(docs))
		for i, d := range docs {
			inputs[i] = inference.AspectInput{Text: d.Document.Text, Aspects: req.Aspects}
		}
		batch, err := s.Inference.ClassifyAspects(ctx, inputs)
		if err != nil {
			return nil, fmt.Errorf("classify aspects: %w", err)
		}
		for i := range batch {
			aspectsByDoc[i] = batch[i]
		}
	}

	now := s.Now().UTC()
	results := make([]domain.SourcedDocumentResult, len(docs))
	overall := domain.NewAggregator()
	byPlatform := map[string]*domain.Aggregator{}
	byAspect := map[string]*domain.Aggregator{}

	for i, d := range docs {
		dr := domain.DocumentResult{
			ID:         d.Document.ID,
			Overall:    scores[i],
			Aspects:    aspectsByDoc[i],
			AnalyzedAt: now,
		}
		results[i] = domain.SourcedDocumentResult{Document: d, Result: dr}
		overall.Add(scores[i])
		if _, ok := byPlatform[d.Platform]; !ok {
			byPlatform[d.Platform] = domain.NewAggregator()
		}
		byPlatform[d.Platform].Add(scores[i])
		for _, asp := range aspectsByDoc[i] {
			if _, ok := byAspect[asp.Aspect]; !ok {
				byAspect[asp.Aspect] = domain.NewAggregator()
			}
			byAspect[asp.Aspect].Add(asp.Score)
		}
	}

	out := &domain.SourcedAnalysis{
		Topic:      req.Topic,
		Results:    results,
		Aggregate:  overall.Aggregate(),
		ByPlatform: aggregateMap(byPlatform),
		ByAspect:   aggregateMap(byAspect),
		Partial:    len(warnings) > 0,
		Warnings:   warnings,
	}

	// Only cache complete results. A single connector blip would otherwise
	// pin a degraded answer to this topic for the whole TTL (10m by default),
	// long outliving the blip that caused it — and every subsequent caller
	// would be served the smaller sample without another request being made.
	if !out.Partial {
		s.Cache.Set(cacheKey, out)
	}
	return out, nil
}

func (s *Service) ListPlatforms() []domain.PlatformInfo {
	return domain.SortPlatforms(s.Connectors.Info())
}

func (s *Service) Health(ctx context.Context) (*domain.HealthInfo, error) {
	info := &domain.HealthInfo{Healthy: true}
	if r, err := s.Inference.Ready(ctx); err == nil && r.Ready {
		info.MLReachable = true
	}
	return info, nil
}

// --- helpers -----------------------------------------------------------------

// usable reports whether a connector can serve this request: enabled by
// server-side configuration, or unlocked by the caller's own credentials.
func usable(c connectors.Connector, creds connectors.Credentials) bool {
	if cc, ok := c.(connectors.CredentialedConnector); ok {
		enabled, _ := cc.EnabledWith(creds)
		return enabled
	}
	enabled, _ := c.Enabled()
	return enabled
}

func (s *Service) selectPlatforms(want []string, creds connectors.Credentials) []connectors.Connector {
	all := s.Connectors.List()
	if len(want) == 0 {
		// Use every connector this request can run.
		out := make([]connectors.Connector, 0, len(all))
		for _, c := range all {
			if usable(c, creds) {
				out = append(out, c)
			}
		}
		return out
	}
	wanted := map[string]struct{}{}
	for _, w := range want {
		wanted[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
	out := make([]connectors.Connector, 0, len(want))
	for _, c := range all {
		if _, ok := wanted[strings.ToLower(c.ID())]; !ok {
			continue
		}
		if !usable(c, creds) {
			continue
		}
		out = append(out, c)
	}
	return out
}

type fanoutResult struct {
	platform string
	docs     []domain.SourcedDocument
	err      error
}

// connectorTimeout returns the per-connector deadline.
//
// The bound has to be per-connector rather than per-request: fanout waits on
// every goroutine, so one slow third-party feed sets the latency of the whole
// call. Measured, a single unresponsive RSS host contributed 7.7s of a 10.4s
// p99 while every other connector answered in under 600ms.
//
// This is a *floor* on nothing and a *ceiling* on everything: a connector that
// exceeds it is dropped and reported, not waited for.
func (s *Service) connectorTimeout() time.Duration {
	if s.ConnectorTimeout > 0 {
		return s.ConnectorTimeout
	}
	return DefaultConnectorTimeout
}

// fanout searches every platform in parallel, returning whatever came back
// plus a warning for each platform that didn't. It fails only when nothing
// succeeded.
func (s *Service) fanout(
	ctx context.Context,
	platforms []connectors.Connector,
	req AnalyzeTopicRequest,
) ([]domain.SourcedDocument, []domain.Warning, error) {
	q := connectors.Query{
		Topic:        req.Topic,
		Limit:        req.LimitPerPlatform,
		Language:     req.Language,
		SinceSeconds: req.SinceSeconds,
		Location:     req.Location,
		Creds:        req.Creds,
	}
	timeout := s.connectorTimeout()

	// Indexed writes rather than a channel: each goroutine owns one slot, so
	// results keep the caller's platform order instead of arrival order. That
	// makes documents, warnings, and therefore the response body deterministic
	// across runs.
	results := make([]fanoutResult, len(platforms))
	var wg sync.WaitGroup
	for i, c := range platforms {
		wg.Add(1)
		go func(i int, c connectors.Connector) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			docs, err := c.Search(cctx, q)
			// Distinguish "this connector ran out of time" from "this
			// connector is broken" — the operator response differs.
			if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
				err = fmt.Errorf("timed out after %s", timeout)
			}
			results[i] = fanoutResult{platform: c.ID(), docs: docs, err: err}
		}(i, c)
	}
	wg.Wait()

	var all []domain.SourcedDocument
	var warnings []domain.Warning
	var errs []string
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.platform+": "+r.err.Error())
			warnings = append(warnings, domain.Warning{Platform: r.platform, Message: r.err.Error()})
			continue
		}
		all = append(all, r.docs...)
	}
	if len(errs) == len(platforms) {
		return nil, warnings, fmt.Errorf("%w: %s", ErrAllConnectorsFailed, strings.Join(errs, "; "))
	}
	return all, warnings, nil
}

func buildCacheKey(req AnalyzeTopicRequest) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(req.Topic)),
		fmt.Sprintf("limit=%d", req.LimitPerPlatform),
		fmt.Sprintf("lang=%s", req.Language),
		fmt.Sprintf("since=%d", req.SinceSeconds),
		// Location changes what the connectors are asked for, so a located
		// request must never be answered from an unlocated entry (or vice
		// versa).
		"location=" + strings.ToLower(strings.TrimSpace(req.Location)),
		// Cache isolation between credential sets: a result fetched with one
		// caller's keys covers platforms another caller cannot see, so the key
		// must vary with the credentials — as a one-way fingerprint, never the
		// values themselves. No creds hashes to "" and shares the public key
		// space, exactly as before.
		"creds=" + req.Creds.Fingerprint(),
	}
	platforms := append([]string{}, req.Platforms...)
	sort.Strings(platforms)
	parts = append(parts, "platforms="+strings.Join(platforms, ","))
	aspects := append([]string{}, req.Aspects...)
	sort.Strings(aspects)
	parts = append(parts, "aspects="+strings.Join(aspects, ","))
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func dedupeByID(docs []domain.SourcedDocument) []domain.SourcedDocument {
	if len(docs) <= 1 {
		return docs
	}
	seen := make(map[string]struct{}, len(docs))
	out := make([]domain.SourcedDocument, 0, len(docs))
	for _, d := range docs {
		key := d.Platform + "|" + d.Document.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, d)
	}
	return out
}

func aggregateMap(in map[string]*domain.Aggregator) map[string]domain.Aggregate {
	out := make(map[string]domain.Aggregate, len(in))
	for k, agg := range in {
		out[k] = agg.Aggregate()
	}
	return out
}
