package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
)

func TestBlueskySearch(t *testing.T) {
	var gotQuery url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xrpc/app.bsky.feed.searchPosts" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"posts": [
				{
					"uri": "at://did:plc:abc123/app.bsky.feed.post/3xyz",
					"author": {"did": "did:plc:abc123", "handle": "alice.bsky.social"},
					"record": {"text": "robinhood is great", "createdAt": "2026-08-09T12:00:00.000Z"}
				},
				{
					"uri": "at://did:plc:def456/app.bsky.feed.post/3aaa",
					"author": {"did": "did:plc:def456", "handle": "bob.bsky.social"},
					"record": {"text": "   ", "createdAt": "2026-08-09T13:00:00.000Z"}
				}
			],
			"cursor": "next"
		}`))
	}))
	defer ts.Close()

	b := NewBluesky(ts.Client())
	b.BaseURL = ts.URL
	docs, err := b.Search(context.Background(), Query{
		Topic: "robinhood", Limit: 5, Language: "en", SinceSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Blank-text post dropped.
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	d := docs[0]
	if d.Platform != "bluesky" {
		t.Errorf("platform = %q", d.Platform)
	}
	// ID must carry the full URI path: rkeys are only unique per author.
	if want := "bluesky-did:plc:abc123/app.bsky.feed.post/3xyz"; d.Document.ID != want {
		t.Errorf("id = %q, want %q", d.Document.ID, want)
	}
	if d.Author != "alice.bsky.social" {
		t.Errorf("author = %q", d.Author)
	}
	if want := "https://bsky.app/profile/alice.bsky.social/post/3xyz"; d.URL != want {
		t.Errorf("url = %q, want %q", d.URL, want)
	}
	if d.PostedAt.IsZero() {
		t.Error("posted_at not parsed")
	}

	// Request shape: latest sort, language passthrough, since window present.
	if gotQuery.Get("sort") != "latest" || gotQuery.Get("lang") != "en" {
		t.Errorf("query = %v", gotQuery)
	}
	if gotQuery.Get("since") == "" {
		t.Error("since window missing despite SinceSeconds")
	}
	if gotQuery.Get("limit") != "5" {
		t.Errorf("limit = %q", gotQuery.Get("limit"))
	}
}

func TestGDELTSearch(t *testing.T) {
	var gotQuery url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"articles": [
				{
					"url": "https://example.com/a",
					"title": "Robinhood launches something",
					"seendate": "20260807T130000Z",
					"domain": "example.com"
				},
				{"url": "https://example.com/b", "title": "  ", "seendate": "20260807T130000Z", "domain": "example.com"}
			]
		}`))
	}))
	defer ts.Close()

	g := NewGDELT(ts.Client())
	g.BaseURL = ts.URL
	g.MinInterval = 0 // no throttling in tests
	docs, err := g.Search(context.Background(), Query{
		Topic: "robinhood app", Limit: 7, Language: "en", SinceSeconds: 3 * 24 * 3600,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(docs) != 1 { // blank title dropped
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	d := docs[0]
	if d.Platform != "gdelt" || d.Author != "example.com" {
		t.Errorf("doc = %+v", d)
	}
	if d.Document.Text != "Robinhood launches something" {
		t.Errorf("text = %q", d.Document.Text)
	}
	if d.PostedAt.Format("2006-01-02") != "2026-08-07" {
		t.Errorf("posted_at = %v", d.PostedAt)
	}

	// Multi-word topics must be quoted, language mapped to GDELT's name.
	if want := `"robinhood app" sourcelang:english`; gotQuery.Get("query") != want {
		t.Errorf("query = %q, want %q", gotQuery.Get("query"), want)
	}
	if gotQuery.Get("timespan") != "3d" {
		t.Errorf("timespan = %q, want 3d", gotQuery.Get("timespan"))
	}
	if gotQuery.Get("mode") != "ArtList" || gotQuery.Get("maxrecords") != "7" {
		t.Errorf("query = %v", gotQuery)
	}
}

func TestGDELTRejectsNonJSON(t *testing.T) {
	// GDELT reports some query errors as HTTP-200 plain text.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Your query was too short or too long."))
	}))
	defer ts.Close()
	g := NewGDELT(ts.Client())
	g.BaseURL = ts.URL
	g.MinInterval = 0
	if _, err := g.Search(context.Background(), Query{Topic: "x"}); err == nil {
		t.Fatal("plain-text error body should surface as an error")
	}
}

func TestGDELTThrottleHonorsContext(t *testing.T) {
	g := NewGDELT(&http.Client{})
	g.MinInterval = time.Hour // force the second call to wait
	if err := g.throttle(context.Background()); err != nil {
		t.Fatalf("first throttle: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := g.throttle(ctx); err == nil {
		t.Fatal("second call should fail when the wait exceeds the deadline")
	}
	if time.Since(start) > time.Second {
		t.Fatal("throttle did not respect context cancellation")
	}
}

func TestGDELTTimespan(t *testing.T) {
	for secs, want := range map[int64]string{
		60:          "15min", // clamped to GDELT's minimum
		1800:        "30min",
		3600:        "60min",
		24 * 3600:   "24h",
		3 * 86400:   "3d",
		30 * 86400:  "30d",
		365 * 86400: "365d",
	} {
		if got := gdeltTimespan(secs); got != want {
			t.Errorf("gdeltTimespan(%d) = %q, want %q", secs, got, want)
		}
	}
}

// TestCorpusPolicyPinned locks the signed decision in docs/corpus-policy.md
// into code: exactly these platforms are corpus-eligible, and every blocked
// platform carries a reason. Changing this test means changing the policy
// document first.
func TestCorpusPolicyPinned(t *testing.T) {
	reg := BuildRegistry(&config.Config{UseMock: true})
	wantDurable := map[string]bool{
		"hackernews": true,
		"rss":        true,
		"bluesky":    true,
		"gdelt":      true,
	}
	seen := map[string]bool{}
	for _, c := range reg.List() {
		p := c.Policy()
		seen[c.ID()] = true
		if p.Durable != wantDurable[c.ID()] {
			t.Errorf("%s: Durable = %v, policy doc says %v", c.ID(), p.Durable, wantDurable[c.ID()])
		}
		if !p.Durable && p.Reason == "" {
			t.Errorf("%s: blocked platforms must state a reason", c.ID())
		}
		if p.Durable && p.Reason != "" {
			t.Errorf("%s: eligible platforms must not carry a block reason", c.ID())
		}
	}
	for id := range wantDurable {
		if !seen[id] {
			t.Errorf("expected platform %s to be registered", id)
		}
	}
}
