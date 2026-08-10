package harvest

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestNormalizeAndHash(t *testing.T) {
	if Normalize("  Hello\n\tWorld  ") != "hello world" {
		t.Fatalf("normalize = %q", Normalize("  Hello\n\tWorld  "))
	}
	// Reposts of the same text must dedupe regardless of spacing/case.
	if ContentSHA256("Hello World") != ContentSHA256("hello   world") {
		t.Fatal("equivalent texts must hash identically")
	}
	if ContentSHA256("hello world") == ContentSHA256("hello worlds") {
		t.Fatal("different texts must not collide")
	}
}

func TestSampleHashDeterministicAndProportional(t *testing.T) {
	// Deterministic: same key, same verdict, every time (unlike rand-based
	// sampling, a re-run over the same events selects the same documents).
	first := SampleHash("key-a", 0.5)
	for range 3 {
		if SampleHash("key-a", 0.5) != first {
			t.Fatal("sampling must be deterministic per key")
		}
	}
	if !SampleHash("anything", 1.0) {
		t.Fatal("rate 1.0 must keep everything")
	}
	if SampleHash("anything", 0) {
		t.Fatal("rate 0 must keep nothing")
	}
	// Proportional within tolerance over many keys.
	kept := 0
	const n = 20000
	for i := range n {
		if SampleHash(string(rune(i))+"-key", 0.2) {
			kept++
		}
	}
	got := float64(kept) / n
	if got < 0.17 || got > 0.23 {
		t.Fatalf("rate 0.2 kept %.3f", got)
	}
}

func TestBlueskyFilter(t *testing.T) {
	f := BlueskyFilter{Langs: []string{"en"}, MinChars: 10, MaxChars: 100, SampleRate: 1}
	cases := []struct {
		name  string
		text  string
		langs []string
		want  bool
	}{
		{"admit english", "long enough text", []string{"en"}, true},
		{"region subtag matches", "long enough text", []string{"en-US"}, true},
		{"wrong language", "long enough text", []string{"ja"}, false},
		{"no declared language", "long enough text", nil, false},
		{"too short", "tiny", []string{"en"}, false},
		{"too long", strings.Repeat("x", 101), []string{"en"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.admit("doc", tc.text, tc.langs); got != tc.want {
				t.Fatalf("admit = %v, want %v", got, tc.want)
			}
		})
	}
	// No language filter: undeclared languages are admitted.
	open := BlueskyFilter{SampleRate: 1}
	if !open.admit("doc", "any text at all", nil) {
		t.Fatal("empty filter should admit undeclared languages")
	}
}

func readJSONL[T any](t *testing.T, root, kind string) []T {
	t.Helper()
	var out []T
	err := filepath.WalkDir(filepath.Join(root, kind), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl.gz") {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(gz)
		for dec.More() {
			var row T
			if err := dec.Decode(&row); err != nil {
				return err
			}
			out = append(out, row)
		}
		return gz.Close()
	})
	if err != nil {
		t.Fatalf("read %s: %v", kind, err)
	}
	return out
}

func TestCorpusWriterPartitionsAndRotates(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 8, 10, 23, 59, 0, 0, time.UTC)
	w := &CorpusWriter{Root: dir, Platform: "bluesky", Now: func() time.Time { return day }}

	if err := w.WriteDocument(Document{Platform: "bluesky", DocID: "d1", AuthorDID: "did:1", Text: "First Post"}); err != nil {
		t.Fatal(err)
	}
	day = day.Add(2 * time.Minute) // crosses midnight — must rotate to a new dt=
	if err := w.WriteDocument(Document{Platform: "bluesky", DocID: "d2", AuthorDID: "did:2", Text: "second post"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTombstone(Tombstone{Platform: "bluesky", Scope: "doc", DocID: "d1", AuthorDID: "did:1"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	docs := readJSONL[Document](t, dir, "documents")
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	for _, d := range docs {
		if d.ContentSHA == "" || d.HashVersion != HashVersion || d.HarvestedAt == "" {
			t.Fatalf("row missing provenance: %+v", d)
		}
	}
	// Date partitioning: one file under each of the two dt= dirs.
	for _, dt := range []string{"2026-08-10", "2026-08-11"} {
		glob, _ := filepath.Glob(filepath.Join(dir, "documents", "dt="+dt, "platform=bluesky", "*.jsonl.gz"))
		if len(glob) != 1 {
			t.Fatalf("dt=%s has %d part files, want 1", dt, len(glob))
		}
	}
	tombs := readJSONL[Tombstone](t, dir, "tombstones")
	if len(tombs) != 1 || tombs[0].DocID != "d1" || tombs[0].DeletedAt == "" {
		t.Fatalf("tombstones = %+v", tombs)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursors", "bluesky.json")
	if LoadCursor(path) != 0 {
		t.Fatal("missing cursor should load as 0")
	}
	if err := SaveCursor(path, 1786334288512923); err != nil {
		t.Fatal(err)
	}
	if got := LoadCursor(path); got != 1786334288512923 {
		t.Fatalf("cursor = %d", got)
	}
}

// fakeJetstream serves a scripted sequence of events over a real websocket,
// then closes — exercising dial, read, parse, and the harvester's routing
// exactly as the live firehose would.
func fakeJetstream(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("wantedCollections"); got != postCollection {
			t.Errorf("wantedCollections = %q", got)
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer c.CloseNow()
		for _, ev := range events {
			if err := c.Write(r.Context(), websocket.MessageText, []byte(ev)); err != nil {
				return
			}
		}
		_ = c.Close(websocket.StatusNormalClosure, "done")
	}))
}

func TestBlueskyHarvesterEndToEnd(t *testing.T) {
	// Real-shaped events, verified against the live stream 2026-08-10.
	events := []string{
		// English post long enough to pass the filter.
		`{"did":"did:plc:alice","time_us":100,"kind":"commit","commit":{"operation":"create","collection":"app.bsky.feed.post","rkey":"r1","record":{"$type":"app.bsky.feed.post","createdAt":"2026-08-10T03:11:02.791Z","langs":["en"],"text":"this is a sufficiently long english post about markets"}}}`,
		// Japanese post — filtered out.
		`{"did":"did:plc:bob","time_us":200,"kind":"commit","commit":{"operation":"create","collection":"app.bsky.feed.post","rkey":"r2","record":{"$type":"app.bsky.feed.post","createdAt":"2026-08-10T03:11:03.000Z","langs":["ja"],"text":"これは日本語の投稿ですこれは日本語の投稿ですこれは日本語の投稿です"}}}`,
		// Too short — filtered out.
		`{"did":"did:plc:carol","time_us":300,"kind":"commit","commit":{"operation":"create","collection":"app.bsky.feed.post","rkey":"r3","record":{"$type":"app.bsky.feed.post","createdAt":"2026-08-10T03:11:04.000Z","langs":["en"],"text":"short"}}}`,
		// Delete — must always tombstone, filters be damned.
		`{"did":"did:plc:dave","time_us":400,"kind":"commit","commit":{"operation":"delete","collection":"app.bsky.feed.post","rkey":"r4"}}`,
		// Account deactivation — account-scoped tombstone.
		`{"did":"did:plc:eve","time_us":500,"kind":"account","account":{"active":false,"did":"did:plc:eve"}}`,
		// Identity event — ignored.
		`{"did":"did:plc:frank","time_us":600,"kind":"identity"}`,
	}
	ts := fakeJetstream(t, events)
	defer ts.Close()

	dir := t.TempDir()
	h := &BlueskyHarvester{
		Client: &JetstreamClient{URL: "ws" + strings.TrimPrefix(ts.URL, "http")},
		Writer: &CorpusWriter{Root: dir, Platform: "bluesky"},
		Filter: BlueskyFilter{Langs: []string{"en"}, MinChars: 20, SampleRate: 1},
	}

	// The server closes after the script; Stream returns an error, and Run
	// would reconnect forever — so drive Stream directly for the scripted case.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	last, err := h.Client.Stream(ctx, 0, h.handle)
	if err == nil {
		t.Fatal("expected the stream to end when the server closes")
	}
	if last != 600 {
		t.Fatalf("last cursor = %d, want 600", last)
	}
	if err := h.Writer.Close(); err != nil {
		t.Fatal(err)
	}

	docs := readJSONL[Document](t, dir, "documents")
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1 (filters should drop ja + short): %+v", len(docs), docs)
	}
	d := docs[0]
	if d.AuthorDID != "did:plc:alice" {
		t.Fatalf("author DID missing or wrong: %+v", d) // policy condition (2)
	}
	if d.DocID != "did:plc:alice/app.bsky.feed.post/r1" {
		t.Fatalf("doc id = %q", d.DocID)
	}

	tombs := readJSONL[Tombstone](t, dir, "tombstones")
	if len(tombs) != 2 {
		t.Fatalf("got %d tombstones, want 2 (delete + account): %+v", len(tombs), tombs)
	}
	byScope := map[string]Tombstone{}
	for _, tb := range tombs {
		byScope[tb.Scope] = tb
	}
	if byScope["doc"].DocID != "did:plc:dave/app.bsky.feed.post/r4" {
		t.Fatalf("doc tombstone = %+v", byScope["doc"]) // policy condition (3)
	}
	if byScope["account"].AuthorDID != "did:plc:eve" {
		t.Fatalf("account tombstone = %+v", byScope["account"])
	}

	docCount, tombCount, skipped := h.Stats()
	if docCount != 1 || tombCount != 2 || skipped != 2 {
		t.Fatalf("stats = %d/%d/%d, want 1/2/2", docCount, tombCount, skipped)
	}
}

func TestSubscribeURLCursorRewind(t *testing.T) {
	c := &JetstreamClient{URL: "wss://example.com/subscribe"}
	u, err := c.subscribeURL(10_000_000) // 10s in µs
	if err != nil {
		t.Fatal(err)
	}
	// 5s rewind: overlap is safe (prep dedupes), a gap is not.
	if !strings.Contains(u, "cursor=5000000") {
		t.Fatalf("url = %q, want 5s rewind", u)
	}
	u2, _ := c.subscribeURL(0)
	if strings.Contains(u2, "cursor=") {
		t.Fatal("fresh start must not send a cursor")
	}
}
