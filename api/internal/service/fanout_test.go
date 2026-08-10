package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
)

// scriptedConnector plays a fixed part in a fanout: return docs, return an
// error, or hang until its context expires.
type scriptedConnector struct {
	id    string
	docs  int
	err   error
	hang  bool
	calls int
}

func (c *scriptedConnector) ID() string                { return c.id }
func (c *scriptedConnector) DisplayName() string       { return c.id }
func (c *scriptedConnector) Enabled() (bool, string)   { return true, "" }
func (c *scriptedConnector) Policy() connectors.Policy { return connectors.Policy{} }

func (c *scriptedConnector) Search(ctx context.Context, _ connectors.Query) ([]domain.SourcedDocument, error) {
	c.calls++
	if c.hang {
		// Outlive any sane deadline; the fanout is expected to give up first.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
			return nil, errors.New("hang connector was never cancelled")
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	out := make([]domain.SourcedDocument, c.docs)
	for i := range out {
		out[i] = domain.SourcedDocument{
			Platform: c.id,
			Document: domain.Document{
				ID:   string(rune('a' + i)),
				Text: "this is great and I love it",
			},
		}
	}
	return out, nil
}

func newServiceWith(t *testing.T, timeout time.Duration, cs ...connectors.Connector) *service.Service {
	t.Helper()
	reg := connectors.NewRegistry()
	for _, c := range cs {
		reg.Register(c)
	}
	svc, err := service.New(inference.NewFakeClient(), reg, time.Minute)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	svc.ConnectorTimeout = timeout
	return svc
}

// TestFanout_SlowConnectorDoesNotStallRequest is the point of the whole
// change: one hung platform must not set the latency of the request. Measured
// against live feeds, a single unresponsive RSS host owned 7.7s of a 10.4s
// p99 while everything else answered in under 600ms.
func TestFanout_SlowConnectorDoesNotStallRequest(t *testing.T) {
	fast := &scriptedConnector{id: "fast", docs: 3}
	slow := &scriptedConnector{id: "slow", hang: true}
	svc := newServiceWith(t, 100*time.Millisecond, fast, slow)

	start := time.Now()
	out, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{Topic: "x"})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AnalyzeTopic: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("request took %s — the hung connector was waited on", elapsed)
	}
	if len(out.Results) != 3 {
		t.Fatalf("got %d results, want the 3 from the fast connector", len(out.Results))
	}
	if !out.Partial {
		t.Fatal("result is missing a platform but Partial is false")
	}
	if len(out.Warnings) != 1 || out.Warnings[0].Platform != "slow" {
		t.Fatalf("warnings = %+v, want one naming 'slow'", out.Warnings)
	}
	if !strings.Contains(out.Warnings[0].Message, "timed out") {
		t.Errorf("warning should say it timed out, got %q", out.Warnings[0].Message)
	}
}

// TestFanout_PartialResultIsNotCached: a blip lasts seconds, but caching its
// result would pin the degraded answer for the full TTL and serve it to every
// later caller without another attempt.
func TestFanout_PartialResultIsNotCached(t *testing.T) {
	fast := &scriptedConnector{id: "fast", docs: 2}
	broken := &scriptedConnector{id: "broken", err: errors.New("upstream 503")}
	svc := newServiceWith(t, time.Second, fast, broken)

	req := service.AnalyzeTopicRequest{Topic: "x"}
	first, err := svc.AnalyzeTopic(context.Background(), req)
	if err != nil {
		t.Fatalf("first AnalyzeTopic: %v", err)
	}
	if !first.Partial {
		t.Fatal("first result should be partial")
	}

	// The blip passes.
	broken.err = nil
	broken.docs = 2

	second, err := svc.AnalyzeTopic(context.Background(), req)
	if err != nil {
		t.Fatalf("second AnalyzeTopic: %v", err)
	}
	if second.Partial {
		t.Fatal("second result is still partial — the degraded result was cached")
	}
	if len(second.Results) != 4 {
		t.Fatalf("got %d results, want 4 — recovery was masked by the cache", len(second.Results))
	}
	if broken.calls != 2 {
		t.Fatalf("recovered connector was called %d times, want 2 — a cached "+
			"partial result would have skipped the retry", broken.calls)
	}
}

// TestFanout_CompleteResultIsStillCached guards against over-correcting: the
// cache must keep working for the normal path.
func TestFanout_CompleteResultIsStillCached(t *testing.T) {
	c := &scriptedConnector{id: "ok", docs: 2}
	svc := newServiceWith(t, time.Second, c)

	req := service.AnalyzeTopicRequest{Topic: "x"}
	for i := range 3 {
		out, err := svc.AnalyzeTopic(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if out.Partial {
			t.Fatalf("call %d: unexpectedly partial", i)
		}
	}
	if c.calls != 1 {
		t.Fatalf("connector called %d times, want 1 — complete results should cache", c.calls)
	}
}

// TestFanout_AllConnectorsFailing still errors, and the error names each one.
func TestFanout_AllConnectorsFailing(t *testing.T) {
	a := &scriptedConnector{id: "a", err: errors.New("boom")}
	b := &scriptedConnector{id: "b", err: errors.New("bang")}
	svc := newServiceWith(t, time.Second, a, b)

	_, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{Topic: "x"})
	if err == nil {
		t.Fatal("expected an error when every connector fails")
	}
	for _, want := range []string{"a: boom", "b: bang"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}
}

// TestFanout_ZeroDocumentsWithWarnings: a platform that fails while the rest
// legitimately return nothing still has to report that it dropped out.
func TestFanout_ZeroDocumentsWithWarnings(t *testing.T) {
	empty := &scriptedConnector{id: "empty", docs: 0}
	broken := &scriptedConnector{id: "broken", err: errors.New("nope")}
	svc := newServiceWith(t, time.Second, empty, broken)

	out, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{Topic: "x"})
	if err != nil {
		t.Fatalf("AnalyzeTopic: %v", err)
	}
	if len(out.Results) != 0 {
		t.Fatalf("got %d results, want 0", len(out.Results))
	}
	if !out.Partial || len(out.Warnings) != 1 {
		t.Fatalf("empty result lost its warning: partial=%v warnings=%+v", out.Partial, out.Warnings)
	}
}

// TestFanout_HealthyResultCarriesNoWarnings: Partial must mean something, so
// it has to be false on the happy path.
func TestFanout_HealthyResultCarriesNoWarnings(t *testing.T) {
	svc := newServiceWith(t, time.Second,
		&scriptedConnector{id: "a", docs: 2},
		&scriptedConnector{id: "b", docs: 2},
	)
	out, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{Topic: "x"})
	if err != nil {
		t.Fatalf("AnalyzeTopic: %v", err)
	}
	if out.Partial || len(out.Warnings) != 0 {
		t.Fatalf("healthy result marked partial: %+v", out.Warnings)
	}
	if len(out.Results) != 4 {
		t.Fatalf("got %d results, want 4", len(out.Results))
	}
}

// TestFanout_ResultOrderIsDeterministic: results follow registry order rather
// than whichever goroutine finished first, so identical inputs produce
// identical bodies.
func TestFanout_ResultOrderIsDeterministic(t *testing.T) {
	var last []string
	for i := range 8 {
		svc := newServiceWith(t, time.Second,
			&scriptedConnector{id: "aaa", docs: 2},
			&scriptedConnector{id: "bbb", docs: 2},
			&scriptedConnector{id: "ccc", docs: 2},
		)
		out, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{Topic: "x"})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		got := make([]string, len(out.Results))
		for j, r := range out.Results {
			got[j] = r.Document.Platform
		}
		if last != nil && strings.Join(got, ",") != strings.Join(last, ",") {
			t.Fatalf("run %d produced order %v, previous run gave %v", i, got, last)
		}
		last = got
	}
}
