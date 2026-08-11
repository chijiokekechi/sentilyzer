package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/service"
)

// TestAnalyzeTopic_LocationReachesConnectors: the request's Location must
// arrive on the connectors.Query the fanout hands each platform.
func TestAnalyzeTopic_LocationReachesConnectors(t *testing.T) {
	c := &scriptedConnector{id: "ok", docs: 2}
	svc := newServiceWith(t, time.Second, c)

	_, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{
		Topic:    "robinhood",
		Location: "Austin",
	})
	if err != nil {
		t.Fatalf("AnalyzeTopic: %v", err)
	}
	if c.lastQuery.Location != "Austin" {
		t.Errorf("connector saw Location %q, want %q", c.lastQuery.Location, "Austin")
	}
	if c.lastQuery.Topic != "robinhood" {
		t.Errorf("connector saw Topic %q, want %q", c.lastQuery.Topic, "robinhood")
	}
}

// TestAnalyzeTopic_CacheKeyIncludesLocation: a located request answered from
// an unlocated cache entry (or vice versa) would silently drop the filter, so
// the cache key must vary with the location.
func TestAnalyzeTopic_CacheKeyIncludesLocation(t *testing.T) {
	c := &scriptedConnector{id: "ok", docs: 2}
	svc := newServiceWith(t, time.Second, c)
	ctx := context.Background()

	plain := service.AnalyzeTopicRequest{Topic: "robinhood"}
	located := service.AnalyzeTopicRequest{Topic: "robinhood", Location: "Austin"}

	first, err := svc.AnalyzeTopic(ctx, plain)
	if err != nil {
		t.Fatalf("plain call: %v", err)
	}
	second, err := svc.AnalyzeTopic(ctx, located)
	if err != nil {
		t.Fatalf("located call: %v", err)
	}
	// Same pointer would mean the located request hit the unlocated entry.
	if first == second {
		t.Fatal("located request was served from the unlocated cache entry")
	}
	if c.calls != 2 {
		t.Fatalf("connector called %d times, want 2 (the located request must fan out again)", c.calls)
	}
	if c.lastQuery.Location != "Austin" {
		t.Errorf("second fanout saw Location %q, want %q", c.lastQuery.Location, "Austin")
	}

	// Identical located requests still share an entry.
	third, err := svc.AnalyzeTopic(ctx, located)
	if err != nil {
		t.Fatalf("repeat located call: %v", err)
	}
	if third != second {
		t.Error("repeat located request missed the cache")
	}
	// And an unlocated repeat still hits the original entry: empty location
	// behaves exactly as before the field existed.
	fourth, err := svc.AnalyzeTopic(ctx, plain)
	if err != nil {
		t.Fatalf("repeat plain call: %v", err)
	}
	if fourth != first {
		t.Error("repeat unlocated request missed the cache")
	}
	if c.calls != 2 {
		t.Fatalf("connector called %d times, want 2 (cache hits must not fan out)", c.calls)
	}
}
