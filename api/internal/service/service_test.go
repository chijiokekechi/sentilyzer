package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
)

func newServiceWithMock(t *testing.T) *service.Service {
	t.Helper()
	reg := connectors.NewRegistry()
	reg.Register(connectors.Mock{})
	svc, err := service.New(inference.NewFakeClient(), reg, time.Minute)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc
}

func TestAnalyzeText_BasicLabels(t *testing.T) {
	svc := newServiceWithMock(t)
	resp, err := svc.AnalyzeText(context.Background(), service.AnalyzeTextRequest{
		Documents: []domain.Document{
			{ID: "1", Text: "I love this product, it is amazing"},
			{ID: "2", Text: "This is the worst, terrible experience"},
			{ID: "3", Text: "It is a chair"},
		},
	})
	if err != nil {
		t.Fatalf("AnalyzeText: %v", err)
	}
	if got, want := len(resp.Results), 3; got != want {
		t.Fatalf("results len = %d, want %d", got, want)
	}
	if resp.Results[0].Overall.Label != domain.SentimentPositive {
		t.Errorf("doc 1 label = %q, want positive", resp.Results[0].Overall.Label)
	}
	if resp.Results[1].Overall.Label != domain.SentimentNegative {
		t.Errorf("doc 2 label = %q, want negative", resp.Results[1].Overall.Label)
	}
	if resp.Results[2].Overall.Label != domain.SentimentNeutral {
		t.Errorf("doc 3 label = %q, want neutral", resp.Results[2].Overall.Label)
	}
	if resp.Aggregate.SampleSize != 3 {
		t.Errorf("aggregate sample = %d, want 3", resp.Aggregate.SampleSize)
	}
}

func TestAnalyzeText_WithAspects(t *testing.T) {
	svc := newServiceWithMock(t)
	resp, err := svc.AnalyzeText(context.Background(), service.AnalyzeTextRequest{
		IncludeAspects: true,
		Documents: []domain.Document{
			{ID: "1", Text: "Battery is great", Aspects: []string{"battery", "screen"}},
		},
	})
	if err != nil {
		t.Fatalf("AnalyzeText: %v", err)
	}
	if len(resp.Results[0].Aspects) != 2 {
		t.Fatalf("aspects = %d, want 2", len(resp.Results[0].Aspects))
	}
}

func TestAnalyzeTopic_FanoutAndAggregate(t *testing.T) {
	svc := newServiceWithMock(t)
	resp, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{
		Topic:            "Robinhood",
		Platforms:        []string{"mock"},
		LimitPerPlatform: 4,
	})
	if err != nil {
		t.Fatalf("AnalyzeTopic: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result from mock connector")
	}
	if resp.Topic != "Robinhood" {
		t.Errorf("topic = %q", resp.Topic)
	}
	if _, ok := resp.ByPlatform["mock"]; !ok {
		t.Errorf("by_platform missing 'mock': %v", resp.ByPlatform)
	}
}

func TestAnalyzeTopic_CacheHit(t *testing.T) {
	svc := newServiceWithMock(t)
	req := service.AnalyzeTopicRequest{Topic: "Tesla", Platforms: []string{"mock"}, LimitPerPlatform: 2}
	first, err := svc.AnalyzeTopic(context.Background(), req)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := svc.AnalyzeTopic(context.Background(), req)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	// Same pointer means it came from the LRU cache (Set stores *SourcedAnalysis).
	if first != second {
		t.Errorf("expected cache hit: first %p, second %p", first, second)
	}
}

func TestAnalyzeTopic_NoEnabledPlatforms(t *testing.T) {
	reg := connectors.NewRegistry()
	// Register a connector that's permanently disabled.
	reg.Register(disabledConnector{})
	svc, err := service.New(inference.NewFakeClient(), reg, time.Minute)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	_, err = svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{Topic: "X"})
	if err == nil {
		t.Fatal("expected error for empty platform set")
	}
}

type disabledConnector struct{}

func (disabledConnector) ID() string              { return "disabled" }
func (disabledConnector) DisplayName() string     { return "Disabled" }
func (disabledConnector) Enabled() (bool, string) { return false, "missing creds" }
func (disabledConnector) Search(context.Context, connectors.Query) ([]domain.SourcedDocument, error) {
	return nil, nil
}
