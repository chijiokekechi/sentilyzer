package connectors

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Credentials carries caller-supplied API keys for a single request ("bring
// your own key"). The zero value means the caller supplied nothing.
//
// Lifecycle contract: these values live for the duration of one request and
// are never persisted, never logged, and never placed in an error message.
// The only derived value that may outlive the request is Fingerprint(), a
// one-way hash used to isolate cache entries between callers.
type Credentials struct {
	RedditClientID     string
	RedditClientSecret string
	TwitterBearerToken string
	YouTubeAPIKey      string
	MastodonToken      string
	MastodonInstance   string
}

// IsZero reports whether the caller supplied no credentials at all.
func (c Credentials) IsZero() bool {
	return c == Credentials{}
}

// Fingerprint returns a stable one-way hash of the credential set, or "" for
// the zero value. It exists solely for cache keying: results fetched with one
// caller's keys must never be served to a caller with different (or no) keys,
// so the cache key has to vary with the credentials — but must not contain
// them. Two callers presenting identical keys hash identically and may share
// cache entries, which is correct: they see the same upstream data.
func (c Credentials) Fingerprint() string {
	if c.IsZero() {
		return ""
	}
	// Field-separated so adjacent values can't collide across boundaries
	// (e.g. {"ab",""} vs {"a","b"}).
	sum := sha256.Sum256([]byte(strings.Join([]string{
		c.RedditClientID,
		c.RedditClientSecret,
		c.TwitterBearerToken,
		c.YouTubeAPIKey,
		c.MastodonToken,
		c.MastodonInstance,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// CredentialedConnector is implemented by connectors that can run on
// caller-supplied credentials instead of (or as well as) server-side
// configuration. Keyless connectors don't implement it; the service layer
// type-asserts when deciding whether a request's credentials make an
// otherwise-disabled platform usable.
type CredentialedConnector interface {
	Connector
	// EnabledWith reports whether the connector can serve a request carrying
	// creds. It must return true when either the server-side configuration or
	// the supplied credentials are sufficient.
	EnabledWith(creds Credentials) (bool, string)
}
