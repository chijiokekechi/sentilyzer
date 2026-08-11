package connectors

import "strings"

// This file is where Query.Location's semantics live.
//
// Location filtering is BEST-EFFORT KEYWORD MATCHING, not geo-tagging. Every
// keyword-search connector (hackernews, bluesky, mastodon, reddit, twitter,
// youtube) ANDs a non-empty location into its platform query as a quoted
// phrase via andLocation; the platform's own search decides what the phrase
// means. Two exceptions:
//
//   - GDELT: a location that names a country (English name, alias, or ISO
//     3166-1 alpha-2 code, case-insensitive; see fips.go) becomes GDELT's
//     sourcecountry: operator carrying the FIPS 10-4 code GDELT expects,
//     instead of a quoted phrase. Anything else falls back to the quoted
//     phrase.
//   - RSS: there is no upstream search, so the quoted-phrase AND degrades to
//     a local substring match over the same title+description haystack the
//     topic is matched against.
//
// Connectors without a keyword query ignore the location: stocktwits streams
// a symbol and mock fabricates. Responses carry no location fields.

// andLocation ANDs the location into a keyword query as a quoted phrase. An
// empty location returns the query unchanged, byte for byte. Embedded quotes
// are stripped so the phrase stays a single search token on every platform.
func andLocation(query, location string) string {
	location = strings.TrimSpace(strings.ReplaceAll(location, `"`, ``))
	if location == "" {
		return query
	}
	return query + ` "` + location + `"`
}
