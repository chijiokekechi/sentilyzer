package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAndLocation(t *testing.T) {
	for name, tc := range map[string]struct {
		query, location, want string
	}{
		// An empty location must leave the query byte-identical.
		"empty":      {"robinhood", "", "robinhood"},
		"whitespace": {"robinhood", "   ", "robinhood"},
		"phrase":     {"robinhood", "Austin", `robinhood "Austin"`},
		"multiword":  {"robinhood", "New York", `robinhood "New York"`},
		// Embedded quotes are stripped so the phrase stays one token.
		"quotes": {"robinhood", `Aus"tin`, `robinhood "Austin"`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := andLocation(tc.query, tc.location); got != tc.want {
				t.Errorf("andLocation(%q, %q) = %q, want %q", tc.query, tc.location, got, tc.want)
			}
		})
	}
}

func TestCountryFIPS(t *testing.T) {
	for in, want := range map[string]string{
		// Countries where FIPS 10-4 diverges from ISO 3166-1 alpha-2. These
		// pins are the point of the table: GDELT wants the FIPS code.
		"germany":        "GM",
		"japan":          "JA",
		"sweden":         "SW",
		"spain":          "SP",
		"united kingdom": "UK",
		// ISO alpha-2 inputs must resolve to the FIPS code, not echo back.
		"de": "GM",
		"jp": "JA",
		"gb": "UK",
		"us": "US",
		// Codes that swap meaning between the two systems read as ISO: gm is
		// ISO Gambia (FIPS GA), ch is ISO Switzerland (FIPS SZ).
		"gm": "GA",
		"ch": "SZ",
		// Countries where the two systems agree.
		"france":                   "FR",
		"United States":            "US",
		"united states of america": "US",
		"US":                       "US",
		"GERMANY":                  "GM",
		"Côte d'Ivoire":            "IV",
		"ivory coast":              "IV",
		"south korea":              "KS",
	} {
		got, ok := countryFIPS(in)
		if !ok || got != want {
			t.Errorf("countryFIPS(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "Austin", "zz", "Atlantis", "usx"} {
		if got, ok := countryFIPS(in); ok {
			t.Errorf("countryFIPS(%q) = %q, true; want a miss", in, got)
		}
	}
}

// newGDELTQueryRecorder serves an empty article list and hands back the raw
// query string GDELT was sent.
func newGDELTQueryRecorder(t *testing.T) (*GDELT, *string) {
	t.Helper()
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"articles": []}`))
	}))
	t.Cleanup(ts.Close)
	g := NewGDELT(ts.Client())
	g.BaseURL = ts.URL
	g.MinInterval = 0
	return g, &got
}

func TestGDELTLocation(t *testing.T) {
	for name, tc := range map[string]struct {
		location, want string
	}{
		// Country names and ISO alpha-2 codes become sourcecountry: with the
		// FIPS 10-4 code GDELT expects.
		"country name":   {"United States", `"robinhood" sourcecountry:US`},
		"alpha-2 code":   {"us", `"robinhood" sourcecountry:US`},
		"divergent name": {"Japan", `"robinhood" sourcecountry:JA`},
		"divergent code": {"gb", `"robinhood" sourcecountry:UK`},
		// Anything else falls back to the quoted-phrase AND.
		"city": {"Austin", `"robinhood" "Austin"`},
		// Empty must produce today's query, byte for byte.
		"empty": {"", `"robinhood"`},
	} {
		t.Run(name, func(t *testing.T) {
			g, got := newGDELTQueryRecorder(t)
			_, err := g.Search(context.Background(), Query{Topic: "robinhood", Location: tc.location})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if *got != tc.want {
				t.Errorf("query = %q, want %q", *got, tc.want)
			}
		})
	}
}

// TestGDELTLocationKeepsLanguage: sourcecountry: must compose with the
// existing sourcelang: operator, not replace it.
func TestGDELTLocationKeepsLanguage(t *testing.T) {
	g, got := newGDELTQueryRecorder(t)
	_, err := g.Search(context.Background(), Query{Topic: "robinhood", Location: "Germany", Language: "en"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := `"robinhood" sourcecountry:GM sourcelang:english`; *got != want {
		t.Errorf("query = %q, want %q", *got, want)
	}
}

func TestHackerNewsLocation(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits": []}`))
	}))
	defer ts.Close()
	hn := NewHackerNews(ts.Client())
	hn.BaseURL = ts.URL

	if _, err := hn.Search(context.Background(), Query{Topic: "robinhood", Location: "Austin"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := `robinhood "Austin"`; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}
	// Empty location must produce today's query, byte for byte.
	if _, err := hn.Search(context.Background(), Query{Topic: "robinhood"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got != "robinhood" {
		t.Errorf("query = %q, want %q", got, "robinhood")
	}
}

func TestBlueskyLocation(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"posts": []}`))
	}))
	defer ts.Close()
	b := NewBluesky(ts.Client())
	b.BaseURL = ts.URL

	if _, err := b.Search(context.Background(), Query{Topic: "robinhood", Location: "Austin"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := `robinhood "Austin"`; got != want {
		t.Errorf("q = %q, want %q", got, want)
	}
}

// TestRSSLocation: RSS has no upstream search, so the location becomes a
// second local substring filter over title+description.
func TestRSSLocation(t *testing.T) {
	const feed = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Test Feed</title>
<item><title>Robinhood expands in Austin</title><description>Texas launch</description><link>https://example.com/1</link><guid>1</guid></item>
<item><title>Robinhood quarterly results</title><description>Earnings</description><link>https://example.com/2</link><guid>2</guid></item>
</channel></rss>`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(feed))
	}))
	defer ts.Close()
	r := NewRSS(ts.Client(), []string{ts.URL})

	docs, err := r.Search(context.Background(), Query{Topic: "robinhood", Location: "austin"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(docs) != 1 || docs[0].URL != "https://example.com/1" {
		t.Fatalf("located search returned %+v, want only the Austin item", docs)
	}

	docs, err = r.Search(context.Background(), Query{Topic: "robinhood"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("unlocated search returned %d docs, want 2", len(docs))
	}
}

// TestFIPSTableShape pins the table at 244 distinct FIPS 10-4 codes (the ISO
// register minus the entries with no single certain FIPS code that GDELT
// accepts, plus Kosovo and Svalbard) and checks every value looks like a
// FIPS code.
func TestFIPSTableShape(t *testing.T) {
	codes := make(map[string]struct{}, len(fipsCountry))
	for key, code := range fipsCountry {
		codes[code] = struct{}{}
		if len(code) != 2 || code != strings.ToUpper(code) {
			t.Errorf("fipsCountry[%q] = %q, want a two-letter uppercase code", key, code)
		}
	}
	if got, want := len(codes), 244; got != want {
		t.Errorf("table maps to %d distinct codes, want %d", got, want)
	}
}
