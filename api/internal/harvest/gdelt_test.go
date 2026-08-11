package harvest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// closedTick never blocks: the throttle discipline itself is exercised by
// TestGDELTOneRequestPerTick.
func closedTick() <-chan time.Time {
	ch := make(chan time.Time)
	close(ch)
	return ch
}

func TestGDELTHarvesterEndToEnd(t *testing.T) {
	var got url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/doc/doc" {
			t.Errorf("path = %s", r.URL.Path)
		}
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"articles":[
			{"url":"https://news.example.com/a1","title":"Inflation eases to 2.4 percent","seendate":"20260810T120000Z","domain":"news.example.com"},
			{"url":"https://blank.example.net/b2","title":"   ","seendate":"20260810T121500Z","domain":"blank.example.net"},
			{"url":"https://bad-date.example.org/c3","title":"Housing market cools","seendate":"bogus","domain":"bad-date.example.org"}
		]}`))
	}))
	defer ts.Close()

	dir := t.TempDir()
	h := &GDELTHarvester{
		HTTP:    ts.Client(),
		BaseURL: ts.URL,
		Writer:  &CorpusWriter{Root: dir, Platform: "gdelt"},
		Queries: []string{"housing market"},
		Tick:    closedTick(),
	}
	if err := h.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := h.Writer.Close(); err != nil {
		t.Fatal(err)
	}

	// Request shape: quoted query, ArtList JSON, the 24h default timespan.
	if got.Get("query") != `"housing market"` || got.Get("mode") != "ArtList" || got.Get("format") != "json" {
		t.Fatalf("query = %v", got)
	}
	if got.Get("timespan") != "24h" {
		t.Fatalf("timespan = %q, want 24h", got.Get("timespan"))
	}
	if got.Get("maxrecords") != "250" {
		t.Fatalf("maxrecords = %q, want 250", got.Get("maxrecords"))
	}

	docs := readJSONL[Document](t, dir, "documents")
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2 (blank title skipped): %+v", len(docs), docs)
	}
	byID := map[string]Document{}
	for _, d := range docs {
		byID[d.DocID] = d
	}

	d1, ok := byID[docHash("https://news.example.com/a1")]
	if !ok {
		t.Fatalf("URL-hash doc id missing: %+v", docs)
	}
	if d1.Platform != "gdelt" || d1.Author != "news.example.com" {
		t.Fatalf("row = %+v", d1)
	}
	if d1.Text != "Inflation eases to 2.4 percent" {
		t.Fatalf("text = %q", d1.Text)
	}
	if d1.CreatedAt != "2026-08-10T12:00:00Z" {
		t.Fatalf("created_at = %q", d1.CreatedAt)
	}
	// Shared Normalize semantics: GDELT rows dedupe against every platform.
	if d1.ContentSHA != ContentSHA256(d1.Text) || d1.HashVersion != HashVersion {
		t.Fatalf("hash mismatch: %+v", d1)
	}

	d2, ok := byID[docHash("https://bad-date.example.org/c3")]
	if !ok {
		t.Fatalf("second doc missing: %+v", docs)
	}
	if d2.CreatedAt != "" {
		t.Fatalf("unparseable seendate must leave created_at empty: %+v", d2)
	}

	docsN, skipped, failed := h.Stats()
	if docsN != 2 || skipped != 1 || failed != 0 {
		t.Fatalf("stats = %d/%d/%d, want 2/1/0", docsN, skipped, failed)
	}
}

// TestGDELTPolicyPinTitleOnly pins the corpus-policy condition for GDELT
// (docs/corpus-policy.md): the row text is GDELT's own title field, exactly,
// and the linked article URL is never fetched. Fetching bodies would
// reintroduce each publisher's copyright.
func TestGDELTPolicyPinTitleOnly(t *testing.T) {
	var articleFetches int32
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/article" {
			atomic.AddInt32(&articleFetches, 1)
			_, _ = w.Write([]byte("<html>the full article body</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"articles":[{"url":%q,"title":"Central bank holds rates steady","seendate":"20260810T090000Z","domain":"news.example.com"}]}`, ts.URL+"/article")
	}))
	defer ts.Close()

	dir := t.TempDir()
	h := &GDELTHarvester{
		HTTP:    ts.Client(),
		BaseURL: ts.URL,
		Writer:  &CorpusWriter{Root: dir, Platform: "gdelt"},
		Queries: []string{"rates"},
		Tick:    closedTick(),
	}
	if err := h.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := h.Writer.Close(); err != nil {
		t.Fatal(err)
	}

	docs := readJSONL[Document](t, dir, "documents")
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	if docs[0].Text != "Central bank holds rates steady" {
		t.Fatalf("text = %q, want the title field exactly", docs[0].Text)
	}
	if n := atomic.LoadInt32(&articleFetches); n != 0 {
		t.Fatalf("linked article fetched %d times; the policy forbids any", n)
	}
}

func TestGDELTPlainTextErrorContinues(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case strings.Contains(q, "bad"):
			// GDELT reports some query errors as HTTP-200 plain text.
			_, _ = w.Write([]byte("Your query was too short or too long."))
		case strings.Contains(q, "down"):
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"articles":[{"url":"https://ok.example.com/a","title":"Economy grows","seendate":"20260810T120000Z","domain":"ok.example.com"}]}`))
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	h := &GDELTHarvester{
		HTTP:    ts.Client(),
		BaseURL: ts.URL,
		Writer:  &CorpusWriter{Root: dir, Platform: "gdelt"},
		// Failing queries come first: they must log and continue, not
		// abort the batch.
		Queries: []string{"bad", "down", "economy"},
		Tick:    closedTick(),
	}
	if err := h.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := h.Writer.Close(); err != nil {
		t.Fatal(err)
	}

	docs := readJSONL[Document](t, dir, "documents")
	if len(docs) != 1 || docs[0].Text != "Economy grows" {
		t.Fatalf("docs = %+v", docs)
	}
	docsN, skipped, failed := h.Stats()
	if docsN != 1 || skipped != 0 || failed != 2 {
		t.Fatalf("stats = %d/%d/%d, want 1/0/2", docsN, skipped, failed)
	}
}

// TestGDELTOneRequestPerTick proves the self-throttle: no request is issued
// until a tick arrives, and each tick admits at most one request.
func TestGDELTOneRequestPerTick(t *testing.T) {
	var requests int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"articles":[]}`))
	}))
	defer ts.Close()

	dir := t.TempDir()
	tick := make(chan time.Time)
	h := &GDELTHarvester{
		HTTP:    ts.Client(),
		BaseURL: ts.URL,
		Writer:  &CorpusWriter{Root: dir, Platform: "gdelt"},
		Queries: []string{"a", "b", "c"},
		Tick:    tick,
	}
	done := make(chan error, 1)
	go func() { done <- h.Run(context.Background()) }()

	waitFor := func(n int32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for atomic.LoadInt32(&requests) < n {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %d requests (have %d)", n, atomic.LoadInt32(&requests))
			}
			time.Sleep(time.Millisecond)
		}
	}

	for i := int32(1); i <= 3; i++ {
		// A settle window: a harvester that ignored the ticker would have
		// issued the next request already.
		time.Sleep(20 * time.Millisecond)
		if got := atomic.LoadInt32(&requests); got != i-1 {
			t.Fatalf("before tick %d: %d requests, want %d", i, got, i-1)
		}
		select {
		case tick <- time.Time{}:
		case <-time.After(5 * time.Second):
			t.Fatal("harvester never consumed the tick")
		}
		waitFor(i)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
	if err := h.Writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGDELTHarvestTimespan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute:         "15min", // clamped to GDELT's minimum
		30 * time.Minute:    "30min",
		time.Hour:           "60min",
		24 * time.Hour:      "24h",
		72 * time.Hour:      "3d",
		30 * 24 * time.Hour: "30d",
	} {
		if got := gdeltTimespan(d); got != want {
			t.Errorf("gdeltTimespan(%v) = %q, want %q", d, got, want)
		}
	}
}
