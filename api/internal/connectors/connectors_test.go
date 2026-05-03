package connectors_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
)

func TestMock_Deterministic(t *testing.T) {
	q := connectors.Query{Topic: "Hairstyle App", Limit: 5}
	a, _ := connectors.Mock{}.Search(context.Background(), q)
	b, _ := connectors.Mock{}.Search(context.Background(), q)
	if len(a) != 5 || len(b) != 5 {
		t.Fatalf("len(a)=%d len(b)=%d, want 5", len(a), len(b))
	}
	for i := range a {
		if a[i].Document.Text != b[i].Document.Text {
			t.Errorf("non-deterministic at %d: %q vs %q", i, a[i].Document.Text, b[i].Document.Text)
		}
	}
}

func TestStockTwits_ExtractSymbol(t *testing.T) {
	cases := map[string]string{
		"AAPL":             "AAPL",
		"$TSLA":            "TSLA",
		"the stock $NVDA":  "NVDA",
		"Robinhood UI":     "",
		"":                 "",
	}
	for in, want := range cases {
		got := connectors.ExportExtractSymbol(in)
		if got != want {
			t.Errorf("extractSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHackerNews_HappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "Robinhood" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": []map[string]any{
				{
					"objectID":     "1",
					"title":        "Robinhood new feature",
					"comment_text": "<p>I love it</p>",
					"author":       "alice",
					"created_at":   "2025-01-01T00:00:00.000Z",
				},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	hn := connectors.NewHackerNews(ts.Client())
	hn.BaseURL = ts.URL
	docs, err := hn.Search(context.Background(), connectors.Query{Topic: "Robinhood", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	if !strings.Contains(docs[0].Document.Text, "I love it") || strings.Contains(docs[0].Document.Text, "<p>") {
		t.Errorf("HTML not stripped: %q", docs[0].Document.Text)
	}
}

func TestRedditDisabled(t *testing.T) {
	r := connectors.NewReddit(nil, config.RedditCreds{})
	enabled, reason := r.Enabled()
	if enabled || reason == "" {
		t.Errorf("expected disabled with reason, got enabled=%v reason=%q", enabled, reason)
	}
}
