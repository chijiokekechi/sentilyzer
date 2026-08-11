package harvest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
)

// Real-shaped RSS 2.0: one item with a GUID and HTML in the summary, one
// without a GUID, one with no usable text at all.
const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
<title>Example Business News</title>
<link>https://news.example.com/</link>
<item>
<title>Markets rally on rate cut hopes</title>
<description>Stocks rose as &lt;b&gt;investors&lt;/b&gt; bet on cuts.</description>
<guid>tag:news.example.com,2026:item-1</guid>
<link>https://news.example.com/markets-rally</link>
<pubDate>Mon, 10 Aug 2026 14:00:00 GMT</pubDate>
</item>
<item>
<title>Feed item without a GUID</title>
<description>Summary two.</description>
<link>https://news.example.com/no-guid</link>
<pubDate>Mon, 10 Aug 2026 15:30:00 GMT</pubDate>
</item>
<item>
<title></title>
<description></description>
<link>https://news.example.com/empty</link>
</item>
</channel>
</rss>`

func TestRSSHarvesterEndToEnd(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/good.xml" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer ts.Close()

	dir := t.TempDir()
	h := &RSSHarvester{
		HTTP:   ts.Client(),
		Writer: &CorpusWriter{Root: dir, Platform: "rss"},
		// The failing feed comes first: it must log and continue, not
		// abort the batch.
		Feeds: []string{ts.URL + "/down.xml", ts.URL + "/good.xml"},
	}
	if err := h.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := h.Writer.Close(); err != nil {
		t.Fatal(err)
	}

	docs := readJSONL[Document](t, dir, "documents")
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2 (empty item skipped): %+v", len(docs), docs)
	}
	byID := map[string]Document{}
	for _, d := range docs {
		byID[d.DocID] = d
	}

	// GUID present: it is the doc id verbatim.
	d1, ok := byID["tag:news.example.com,2026:item-1"]
	if !ok {
		t.Fatalf("GUID doc id missing: %+v", docs)
	}
	// Text is title + summary joined by a newline, HTML stripped.
	wantText := "Markets rally on rate cut hopes\nStocks rose as investors bet on cuts."
	if d1.Text != wantText {
		t.Fatalf("text = %q, want %q", d1.Text, wantText)
	}
	if d1.Platform != "rss" || d1.Author != "Example Business News" {
		t.Fatalf("row = %+v", d1)
	}
	if d1.CreatedAt != "2026-08-10T14:00:00Z" {
		t.Fatalf("created_at = %q", d1.CreatedAt)
	}

	// GUID absent: the doc id is the link hash.
	d2, ok := byID[docHash("https://news.example.com/no-guid")]
	if !ok {
		t.Fatalf("link-hash doc id missing: %+v", docs)
	}
	if d2.Text != "Feed item without a GUID\nSummary two." {
		t.Fatalf("text = %q", d2.Text)
	}

	// Content hashing must go through the shared Normalize semantics so RSS
	// rows dedupe against every other platform's rows.
	for _, d := range docs {
		if d.ContentSHA != ContentSHA256(d.Text) || d.HashVersion != HashVersion {
			t.Fatalf("hash mismatch: %+v", d)
		}
		if d.HarvestedAt == "" {
			t.Fatalf("row missing provenance: %+v", d)
		}
	}

	docsN, skipped, failed := h.Stats()
	if docsN != 2 || skipped != 1 || failed != 1 {
		t.Fatalf("stats = %d/%d/%d, want 2/1/1", docsN, skipped, failed)
	}
}

// TestRSSDefaultFeedsPinnedToServing pins the deliberate duplication: the
// serving list (connectors.DefaultFeeds in internal/connectors/rss.go) is
// the source of truth, and the harvest copy must track it exactly.
func TestRSSDefaultFeedsPinnedToServing(t *testing.T) {
	if !reflect.DeepEqual(DefaultRSSFeeds, connectors.DefaultFeeds) {
		t.Fatalf("DefaultRSSFeeds = %v, serving list = %v", DefaultRSSFeeds, connectors.DefaultFeeds)
	}
}

func TestRSSStripHTML(t *testing.T) {
	got := rssStripHTML("<p>A &amp; B\n  <em>rise</em></p>")
	if got != "A & B rise" {
		t.Fatalf("stripped = %q", got)
	}
	if rssStripHTML("") != "" {
		t.Fatal("empty input must stay empty")
	}
}
