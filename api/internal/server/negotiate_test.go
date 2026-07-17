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
			if got := negotiate(tc.accept); got != tc.want {
				t.Errorf("negotiate(%q) = %q, want %q", tc.accept, got, tc.want)
			}
		})
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
		"application/json":   mimeJSON,
		"APPLICATION/JSON":   mimeJSON,
		"  text/json  ":      mimeJSON,
		"application/xml":    mimeXML,
		"text/xml":           mimeXML,
		"application/yaml":   mimeYAML,
		"application/x-yaml": mimeYAML,
		"text/x-yaml":        mimeYAML,
		"text/plain":         "",
		"":                   "",
	} {
		if got := canonicalMedia(in); got != want {
			t.Errorf("canonicalMedia(%q) = %q, want %q", in, got, want)
		}
	}
}
