package connectors

import (
	"html"
	"regexp"
	"strings"
)

var (
	tagRE   = regexp.MustCompile(`<[^>]+>`)
	wsRE    = regexp.MustCompile(`\s+`)
)

// stripHTML returns the text with HTML tags removed and entities decoded.
// Used for Hacker News comments (HTML) and RSS bodies.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	without := tagRE.ReplaceAllString(s, " ")
	decoded := html.UnescapeString(without)
	return strings.TrimSpace(wsRE.ReplaceAllString(decoded, " "))
}
