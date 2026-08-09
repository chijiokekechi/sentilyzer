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

// keyedConnector fakes a platform that needs a caller-supplied key: disabled
// server-side, unlocked by Credentials.YouTubeAPIKey, and it records the key
// each Search actually ran with.
type keyedConnector struct {
	id       string
	sawKeys  []string
	docCount int
}

func (c *keyedConnector) ID() string              { return c.id }
func (c *keyedConnector) DisplayName() string     { return c.id }
func (c *keyedConnector) Enabled() (bool, string) { return false, "missing key" }

func (c *keyedConnector) EnabledWith(creds connectors.Credentials) (bool, string) {
	if creds.YouTubeAPIKey != "" {
		return true, ""
	}
	return c.Enabled()
}

func (c *keyedConnector) Search(_ context.Context, q connectors.Query) ([]domain.SourcedDocument, error) {
	c.sawKeys = append(c.sawKeys, q.Creds.YouTubeAPIKey)
	out := make([]domain.SourcedDocument, c.docCount)
	for i := range out {
		out[i] = domain.SourcedDocument{
			Platform: c.id,
			Document: domain.Document{ID: string(rune('a' + i)), Text: "great stuff, love it"},
		}
	}
	return out, nil
}

func newByokService(t *testing.T, cs ...connectors.Connector) *service.Service {
	t.Helper()
	reg := connectors.NewRegistry()
	for _, c := range cs {
		reg.Register(c)
	}
	svc, err := service.New(inference.NewFakeClient(), reg, time.Minute)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc
}

// TestBYOK_CredsUnlockKeyedPlatform: with caller creds the keyed platform
// joins the fanout and receives the key; without them it stays out entirely.
func TestBYOK_CredsUnlockKeyedPlatform(t *testing.T) {
	open := &scriptedConnector{id: "open", docs: 2}
	keyed := &keyedConnector{id: "keyed", docCount: 2}
	svc := newByokService(t, open, keyed)

	// Without creds: only the open platform serves; the keyed one is not called.
	out, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{Topic: "x"})
	if err != nil {
		t.Fatalf("AnalyzeTopic (no creds): %v", err)
	}
	if _, ok := out.ByPlatform["keyed"]; ok {
		t.Fatal("keyed platform served without credentials")
	}
	if len(keyed.sawKeys) != 0 {
		t.Fatalf("keyed connector was called %d times without creds", len(keyed.sawKeys))
	}

	// With creds: the keyed platform joins and Search runs with the key.
	creds := connectors.Credentials{YouTubeAPIKey: "caller-key-1"}
	out2, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{Topic: "x", Creds: creds})
	if err != nil {
		t.Fatalf("AnalyzeTopic (creds): %v", err)
	}
	if _, ok := out2.ByPlatform["keyed"]; !ok {
		t.Fatalf("keyed platform missing despite creds; by_platform=%v", out2.ByPlatform)
	}
	if len(keyed.sawKeys) != 1 || keyed.sawKeys[0] != "caller-key-1" {
		t.Fatalf("connector saw keys %v, want [caller-key-1]", keyed.sawKeys)
	}
}

// TestBYOK_CacheIsolation is the security-relevant property: a result fetched
// with caller A's keys covers platforms caller B cannot see, so the two must
// never share a cache entry — in either direction.
func TestBYOK_CacheIsolation(t *testing.T) {
	open := &scriptedConnector{id: "open", docs: 1}
	keyed := &keyedConnector{id: "keyed", docCount: 3}
	svc := newByokService(t, open, keyed)
	req := service.AnalyzeTopicRequest{Topic: "x"}
	creds := connectors.Credentials{YouTubeAPIKey: "caller-a"}

	// Caller A (with keys) populates the cache first.
	withKeys, err := svc.AnalyzeTopic(context.Background(),
		service.AnalyzeTopicRequest{Topic: "x", Creds: creds})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := withKeys.ByPlatform["keyed"]; !ok {
		t.Fatal("setup: keyed platform should have served caller A")
	}

	// Caller B (no keys) must NOT be served A's keyed results from cache.
	without, err := svc.AnalyzeTopic(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := without.ByPlatform["keyed"]; ok {
		t.Fatal("cache leak: keyless caller received results fetched with another caller's key")
	}

	// And caller A polling again hits their own cache entry: no second Search.
	if _, err := svc.AnalyzeTopic(context.Background(),
		service.AnalyzeTopicRequest{Topic: "x", Creds: creds}); err != nil {
		t.Fatal(err)
	}
	if len(keyed.sawKeys) != 1 {
		t.Fatalf("keyed connector called %d times for caller A, want 1 (second call cached)", len(keyed.sawKeys))
	}

	// Different keys are a different cache identity too.
	credsB := connectors.Credentials{YouTubeAPIKey: "caller-b"}
	if _, err := svc.AnalyzeTopic(context.Background(),
		service.AnalyzeTopicRequest{Topic: "x", Creds: credsB}); err != nil {
		t.Fatal(err)
	}
	if len(keyed.sawKeys) != 2 || keyed.sawKeys[1] != "caller-b" {
		t.Fatalf("caller B should trigger a fresh search with their key, saw %v", keyed.sawKeys)
	}
}

// TestBYOK_ExplicitPlatformSelection: naming a keyed platform explicitly works
// with creds and yields no platform without them.
func TestBYOK_ExplicitPlatformSelection(t *testing.T) {
	keyed := &keyedConnector{id: "keyed", docCount: 1}
	svc := newByokService(t, keyed)

	// Explicitly requested but no creds: no usable platform.
	_, err := svc.AnalyzeTopic(context.Background(),
		service.AnalyzeTopicRequest{Topic: "x", Platforms: []string{"keyed"}})
	if err == nil {
		t.Fatal("expected the no-platforms error without creds")
	}

	out, err := svc.AnalyzeTopic(context.Background(), service.AnalyzeTopicRequest{
		Topic:     "x",
		Platforms: []string{"keyed"},
		Creds:     connectors.Credentials{YouTubeAPIKey: "k"},
	})
	if err != nil {
		t.Fatalf("AnalyzeTopic: %v", err)
	}
	if len(out.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(out.Results))
	}
}
