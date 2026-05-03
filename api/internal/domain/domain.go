// Package domain defines the in-memory types shared by every protocol surface.
//
// Keeping the types separate from the generated proto types means the REST,
// XML, GraphQL, and connector layers don't get coupled to the on-the-wire
// proto layout — the proto encoder lives in one place (server/grpc.go).
package domain

import (
	"math"
	"sort"
	"strings"
	"time"
)

type Sentiment string

const (
	SentimentUnspecified Sentiment = ""
	SentimentNegative    Sentiment = "negative"
	SentimentNeutral     Sentiment = "neutral"
	SentimentPositive    Sentiment = "positive"
)

// AllSentiments is in canonical (negative…positive) order.
var AllSentiments = []Sentiment{SentimentNegative, SentimentNeutral, SentimentPositive}

// Score is the normalized model output for one text or aspect.
type Score struct {
	Label         Sentiment          `json:"label" xml:"label"`
	Confidence    float32            `json:"confidence" xml:"confidence"`
	Polarity      float32            `json:"polarity" xml:"polarity"`
	Probabilities map[string]float32 `json:"probabilities" xml:"-"`
}

// AspectScore captures sentiment toward one aspect within a Document.
type AspectScore struct {
	Aspect string `json:"aspect" xml:"aspect"`
	Score  Score  `json:"score" xml:"score"`
}

// Document is one unit submitted for analysis.
type Document struct {
	ID       string            `json:"id,omitempty" xml:"id,attr,omitempty"`
	Text     string            `json:"text" xml:"text"`
	Language string            `json:"language,omitempty" xml:"language,attr,omitempty"`
	Aspects  []string          `json:"aspects,omitempty" xml:"aspects>aspect,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty" xml:"-"`
}

// SourcedDocument is a Document enriched with provenance.
type SourcedDocument struct {
	Document Document  `json:"document" xml:"document"`
	Platform string    `json:"platform" xml:"platform,attr"`
	URL      string    `json:"source_url,omitempty" xml:"source_url,omitempty"`
	Author   string    `json:"author,omitempty" xml:"author,omitempty"`
	PostedAt time.Time `json:"posted_at,omitempty" xml:"posted_at,omitempty"`
}

// DocumentResult is what the model produced for one Document.
type DocumentResult struct {
	ID         string        `json:"id,omitempty" xml:"id,attr,omitempty"`
	Overall    Score         `json:"overall" xml:"overall"`
	Aspects    []AspectScore `json:"aspects,omitempty" xml:"aspects>aspect,omitempty"`
	AnalyzedAt time.Time     `json:"analyzed_at" xml:"analyzed_at"`
}

// SourcedDocumentResult pairs a SourcedDocument with its DocumentResult.
type SourcedDocumentResult struct {
	Document SourcedDocument `json:"document" xml:"document"`
	Result   DocumentResult  `json:"result" xml:"result"`
}

// Aggregate summarizes a collection of Scores.
type Aggregate struct {
	MeanPolarity float32          `json:"mean_polarity" xml:"mean_polarity"`
	LabelCounts  map[string]int32 `json:"label_counts" xml:"-"`
	ModalLabel   Sentiment        `json:"modal_label" xml:"modal_label"`
	SampleSize   int32            `json:"sample_size" xml:"sample_size"`
}

// Aggregator builds an Aggregate from individual Scores.
type Aggregator struct {
	count   int32
	sumPol  float64
	buckets map[Sentiment]int32
}

func NewAggregator() *Aggregator {
	return &Aggregator{buckets: make(map[Sentiment]int32, 3)}
}

func (a *Aggregator) Add(s Score) {
	a.count++
	a.sumPol += float64(s.Polarity)
	a.buckets[s.Label]++
}

func (a *Aggregator) Aggregate() Aggregate {
	if a.count == 0 {
		return Aggregate{LabelCounts: map[string]int32{}}
	}
	mean := float32(a.sumPol / float64(a.count))
	if math.IsNaN(float64(mean)) {
		mean = 0
	}
	counts := make(map[string]int32, len(a.buckets))
	for k, v := range a.buckets {
		counts[string(k)] = v
	}
	modal := SentimentNeutral
	var bestCount int32 = -1
	// Stable order: prefer neutral on ties.
	for _, label := range []Sentiment{SentimentNeutral, SentimentPositive, SentimentNegative} {
		if c, ok := a.buckets[label]; ok && c > bestCount {
			bestCount = c
			modal = label
		}
	}
	return Aggregate{
		MeanPolarity: mean,
		LabelCounts:  counts,
		ModalLabel:   modal,
		SampleSize:   a.count,
	}
}

// SourcedAnalysis is the result of an AnalyzeTopic request: the per-document
// scores, the overall aggregate, and aggregates broken down by platform
// and by requested aspect.
type SourcedAnalysis struct {
	Topic      string                  `json:"topic" xml:"topic"`
	Results    []SourcedDocumentResult `json:"results" xml:"results>result"`
	Aggregate  Aggregate               `json:"aggregate" xml:"aggregate"`
	ByPlatform map[string]Aggregate    `json:"by_platform" xml:"-"`
	ByAspect   map[string]Aggregate    `json:"by_aspect" xml:"-"`
}

// HealthInfo is what GET /health returns.
type HealthInfo struct {
	Healthy     bool   `json:"healthy" xml:"healthy"`
	MLReachable bool   `json:"ml_reachable" xml:"ml_reachable"`
	Version     string `json:"version,omitempty" xml:"version,omitempty"`
}

// PlatformInfo describes one configured connector.
type PlatformInfo struct {
	ID             string `json:"id" xml:"id,attr"`
	DisplayName    string `json:"display_name" xml:"display_name"`
	Enabled        bool   `json:"enabled" xml:"enabled"`
	DisabledReason string `json:"disabled_reason,omitempty" xml:"disabled_reason,omitempty"`
}

// SortPlatforms returns infos sorted by id (stable presentation).
func SortPlatforms(in []PlatformInfo) []PlatformInfo {
	out := make([]PlatformInfo, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(out[i].ID, out[j].ID) < 0
	})
	return out
}
