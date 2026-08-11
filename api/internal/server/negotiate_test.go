package server

import (
	"testing"
)

func TestNegotiate(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		want   string
	}{
		{"empty defaults to json", "", mimeJSON},
		{"exact json", "application/json", mimeJSON},
		{"exact xml", "application/xml", mimeXML},
		{"exact yaml", "application/yaml", mimeYAML},
		{"legacy yaml alias", "application/x-yaml", mimeYAML},
		{"text yaml alias", "text/yaml", mimeYAML},
		{"wildcard prefers our first offer", "*/*", mimeJSON},
		{"type wildcard", "application/*", mimeJSON},

		// The bug this rewrite exists to fix: the old substring check saw
		// application/json anywhere in the header and returned JSON,
		// ignoring q entirely.
		{"q ranks xml over json", "application/xml;q=0.9, application/json;q=0.1", mimeXML},
		{"q ranks yaml over json", "application/yaml;q=1.0, application/json;q=0.2", mimeYAML},
		{"q ranks json over yaml", "application/yaml;q=0.2, application/json;q=0.9", mimeJSON},

		// Equal q falls back to our declared preference order.
		{"tie goes to json", "application/xml, application/json", mimeJSON},
		{"tie among all three", "application/yaml, application/xml, application/json", mimeJSON},

		// A browser's stock header names application/xml above its */*
		// fallback. Serving XML to a browser is never what anyone wanted.
		{
			"firefox",
			"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
			mimeJSON,
		},
		{
			"chrome",
			"text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8",
			mimeJSON,
		},
		// ...but a client that mentions html only as a low-q afterthought is
		// not a browser, and must still get what it actually asked for.
		{"deliberate yaml client that tolerates html", "text/html;q=0.1, application/yaml", mimeYAML},
		{"deliberate xml client that tolerates html", "text/html;q=0.1, application/xml", mimeXML},

		// q=0 means "not acceptable".
		{"json refused, xml offered", "application/json;q=0, application/xml", mimeXML},

		// Nothing we can serve.
		{"unsatisfiable", "text/plain", ""},
		{"image only", "image/png", ""},
		{"everything refused", "*/*;q=0", ""},

		// Malformed ranges are skipped, not fatal.
		{"garbage plus valid", "!!!, application/yaml", mimeYAML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := negotiate(tc.accept, offers); got != tc.want {
				t.Errorf("negotiate(%q) = %q, want %q", tc.accept, got, tc.want)
			}
		})
	}
}

func TestNegotiateAnalyzeOffers(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		want   string
	}{
		{"exact csv", "text/csv", mimeCSV},
		{"exact ndjson", "application/x-ndjson", mimeNDJSON},
		{"csv alias", "application/csv", mimeCSV},
		{"ndjson alias", "application/ndjson", mimeNDJSON},
		{"q ranks csv over json", "text/csv;q=0.9, application/json;q=0.1", mimeCSV},
		{"q ranks ndjson over json", "application/x-ndjson, application/json;q=0.5", mimeNDJSON},

		// JSON stays the tie-winner: it precedes the export formats in offers.
		{"csv tie goes to json", "text/csv, application/json", mimeJSON},
		{"ndjson tie goes to json", "application/x-ndjson, application/json", mimeJSON},
		{"wildcard still prefers json", "*/*", mimeJSON},

		// A browser's stock header must not start yielding CSV either.
		{
			"firefox still gets json",
			"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
			mimeJSON,
		},

		// text/* is a data client asking for the text offer, NOT a browser:
		// only a literal text/html trips the browser sniff. Returning JSON
		// here would hand the client a type it never accepted.
		{"text wildcard reaches csv", "text/*", mimeCSV},
		{"text wildcard beats lower-q json", "text/*, application/json;q=0.5", mimeCSV},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := negotiate(tc.accept, analyzeOffers); got != tc.want {
				t.Errorf("negotiate(%q) = %q, want %q", tc.accept, got, tc.want)
			}
		})
	}

	// The base offers must not have grown export formats by accident: on every
	// non-analyze endpoint these stay 406.
	for _, accept := range []string{"text/csv", "application/x-ndjson"} {
		if got := negotiate(accept, offers); got != "" {
			t.Errorf("negotiate(%q, offers) = %q, want unsatisfiable", accept, got)
		}
	}
}

func TestFormatParam(t *testing.T) {
	cases := []struct {
		v      string
		offers []string
		want   string
	}{
		{"json", offers, mimeJSON},
		{"YAML", offers, mimeYAML},
		{"yml", offers, mimeYAML},
		{"csv", analyzeOffers, mimeCSV},
		{"ndjson", analyzeOffers, mimeNDJSON},
		// Known name, but not offered on this route.
		{"csv", offers, ""},
		{"ndjson", offers, ""},
		{"toml", analyzeOffers, ""},
	}
	for _, tc := range cases {
		if got := formatParam(tc.v, tc.offers); got != tc.want {
			t.Errorf("formatParam(%q, %v) = %q, want %q", tc.v, tc.offers, got, tc.want)
		}
	}

	// The 400 message lists each route's formats once, canonical names only.
	if got, want := formatParamList(offers), "json, xml, or yaml"; got != want {
		t.Errorf("formatParamList(offers) = %q, want %q", got, want)
	}
	if got, want := formatParamList(analyzeOffers), "json, xml, yaml, csv, or ndjson"; got != want {
		t.Errorf("formatParamList(analyzeOffers) = %q, want %q", got, want)
	}
}

func TestQualitySpecificityWins(t *testing.T) {
	// RFC 9110: the most specific matching range applies, regardless of the
	// order ranges appear in.
	ranges := parseAccept("*/*;q=0.1, application/json;q=0.9")
	q, spec := quality(mimeJSON, ranges)
	if q != 0.9 || spec != 2 {
		t.Fatalf("exact match gave q=%v spec=%d, want 0.9 and 2", q, spec)
	}
	q, spec = quality(mimeXML, ranges)
	if q != 0.1 || spec != 0 {
		t.Fatalf("wildcard match gave q=%v spec=%d, want 0.1 and 0", q, spec)
	}
}

func TestCanonicalMedia(t *testing.T) {
	for in, want := range map[string]string{
		"application/json":     mimeJSON,
		"APPLICATION/JSON":     mimeJSON,
		"  text/json  ":        mimeJSON,
		"application/xml":      mimeXML,
		"text/xml":             mimeXML,
		"application/yaml":     mimeYAML,
		"application/x-yaml":   mimeYAML,
		"text/x-yaml":          mimeYAML,
		"text/csv":             mimeCSV,
		"application/csv":      mimeCSV,
		"application/x-ndjson": mimeNDJSON,
		"application/ndjson":   mimeNDJSON,
		"text/plain":           "",
		"":                     "",
	} {
		if got := canonicalMedia(in); got != want {
			t.Errorf("canonicalMedia(%q) = %q, want %q", in, got, want)
		}
	}
}
