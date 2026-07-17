package server

import (
	"context"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const (
	mimeJSON = "application/json"
	mimeXML  = "application/xml"
	mimeYAML = "application/yaml" // registered by RFC 9512 (Feb 2024)
	mimeHTML = "text/html"
)

// offers lists the response media types this server can produce, in
// preference order. The first entry wins ties and is therefore the default.
var offers = []string{mimeJSON, mimeXML, mimeYAML}

// canonicalMedia folds the media types clients actually send onto the one we
// speak, or "" for anything we don't. The YAML aliases predate RFC 9512 and
// remain common in the wild; the text/* JSON and XML spellings are legacy but
// harmless to accept.
func canonicalMedia(media string) string {
	switch strings.ToLower(strings.TrimSpace(media)) {
	case "application/json", "text/json":
		return mimeJSON
	case "application/xml", "text/xml":
		return mimeXML
	case "application/yaml", "text/yaml", "application/x-yaml", "text/x-yaml":
		return mimeYAML
	}
	return ""
}

// acceptRange is one comma-separated entry of an Accept header.
type acceptRange struct {
	typ string
	sub string
	q   float64
}

func parseAccept(header string) []acceptRange {
	var out []acceptRange
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		media, params, err := mime.ParseMediaType(part)
		if err != nil {
			continue // a malformed range is ignored, not fatal
		}
		// Fold known aliases onto the spelling in offers, so that
		// "application/x-yaml" matches. Wildcards and media types we don't
		// speak canonicalize to "" and pass through unchanged — the former
		// because they must still match by type, the latter so they can be
		// scored (and rejected) on their own terms.
		if c := canonicalMedia(media); c != "" {
			media = c
		}
		typ, sub, ok := strings.Cut(media, "/")
		if !ok {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		out = append(out, acceptRange{typ: typ, sub: sub, q: q})
	}
	return out
}

// quality returns the q-value the Accept header assigns to media, plus how
// specifically it matched: 2 for an exact type/subtype, 1 for type/*, 0 for
// */*, and -1 for no match at all. Per RFC 9110 the most specific range wins,
// regardless of the order the ranges appear in.
func quality(media string, ranges []acceptRange) (float64, int) {
	typ, sub, _ := strings.Cut(media, "/")
	q, spec := 0.0, -1
	for _, r := range ranges {
		var s int
		switch {
		case r.typ == typ && r.sub == sub:
			s = 2
		case r.typ == typ && r.sub == "*":
			s = 1
		case r.typ == "*" && r.sub == "*":
			s = 0
		default:
			continue
		}
		if s > spec {
			q, spec = r.q, s
		}
	}
	return q, spec
}

// negotiate picks the best offer for an Accept header, or "" if the client
// will accept nothing we can produce.
//
// This replaces a substring check that read:
//
//	if strings.Contains(accept, mimeXML) && !strings.Contains(accept, mimeJSON)
//
// which mis-ranked "application/xml;q=0.9, application/json;q=0.1" as JSON,
// and — because a third format can never win a Contains check against the
// application/json every SDK sends — could not be extended to YAML at all.
func negotiate(accept string) string {
	ranges := parseAccept(accept)
	if len(ranges) == 0 {
		return offers[0]
	}
	best, bestQ := "", 0.0
	for _, offer := range offers {
		q, spec := quality(offer, ranges)
		if spec < 0 || q <= 0 {
			continue
		}
		if q > bestQ {
			best, bestQ = offer, q
		}
	}
	if best == "" {
		return ""
	}
	// Browsers send text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8
	// — which contains application/xml at a higher q than the */* fallback, so
	// a strict reading hands every browser XML. The distinguishing signal is
	// that a browser ranks text/html at or above the data types it lists as
	// fallbacks, while a deliberate data client never leads with text/html at
	// full weight. So "text/html;q=0.1, application/yaml" still yields YAML.
	if hq, hspec := quality(mimeHTML, ranges); hspec >= 0 && hq >= bestQ {
		return mimeJSON
	}
	return best
}

// formatKey types the request-context slot holding the negotiated format.
type formatKey struct{}

// negotiateMedia resolves the response format once per request and rejects
// what it cannot satisfy. Doing it in middleware rather than per-write means
// handlers and error paths cannot disagree about the encoding — including the
// error bodies themselves.
func negotiateMedia(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The response body depends on Accept, so any shared cache must key
		// on it. This has to be set even on the failure paths below.
		w.Header().Add("Vary", "Accept")

		format := ""
		if v := req.URL.Query().Get("format"); v != "" {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "json":
				format = mimeJSON
			case "xml":
				format = mimeXML
			case "yaml", "yml":
				format = mimeYAML
			default:
				// A bad ?format= is a malformed request, not an unacceptable
				// one — 400 rather than 406.
				writeErrorAs(w, mimeJSON, http.StatusBadRequest,
					"unsupported ?format="+v+" (want json, xml, or yaml)")
				return
			}
		} else if format = negotiate(req.Header.Get("Accept")); format == "" {
			writeErrorAs(w, mimeJSON, http.StatusNotAcceptable,
				"none of the media types in Accept can be served; this API offers "+
					strings.Join(offers, ", "))
			return
		}
		next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), formatKey{}, format)))
	})
}

// formatFrom returns the format negotiateMedia resolved, defaulting to JSON
// for requests that never passed through it (e.g. direct handler tests).
func formatFrom(ctx context.Context) string {
	if f, ok := ctx.Value(formatKey{}).(string); ok && f != "" {
		return f
	}
	return mimeJSON
}

// contentType renders the Content-Type header for a format. YAML is defined
// as UTF-8 by RFC 9512 and registers no charset parameter, so it gets none.
func contentType(format string) string {
	if format == mimeYAML {
		return mimeYAML
	}
	return format + "; charset=utf-8"
}
