package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"google.golang.org/grpc/metadata"
)

// Caller-supplied API key headers ("bring your own key"). A caller sets these
// to run keyed connectors on their own account/quota for a single request;
// server-side configuration, if present, remains the fallback.
//
// The values are used in memory for the one request and are never logged,
// never persisted, and never echoed into errors. gRPC callers send the same
// names as metadata keys (gRPC lowercases them; matching below is
// case-insensitive either way).
const (
	HeaderRedditClientID     = "X-Connector-Reddit-Client-Id"
	HeaderRedditClientSecret = "X-Connector-Reddit-Client-Secret"
	HeaderTwitterBearerToken = "X-Connector-Twitter-Bearer-Token"
	HeaderYouTubeAPIKey      = "X-Connector-Youtube-Api-Key"
	HeaderMastodonToken      = "X-Connector-Mastodon-Token"
	HeaderMastodonInstance   = "X-Connector-Mastodon-Instance"
)

type credsKey struct{}

// CredsFromContext returns the caller credentials extracted by the transport,
// or the zero value when none were supplied.
func CredsFromContext(ctx context.Context) connectors.Credentials {
	if c, ok := ctx.Value(credsKey{}).(connectors.Credentials); ok {
		return c
	}
	return connectors.Credentials{}
}

func credsFromHeader(h http.Header) connectors.Credentials {
	return connectors.Credentials{
		RedditClientID:     strings.TrimSpace(h.Get(HeaderRedditClientID)),
		RedditClientSecret: strings.TrimSpace(h.Get(HeaderRedditClientSecret)),
		TwitterBearerToken: strings.TrimSpace(h.Get(HeaderTwitterBearerToken)),
		YouTubeAPIKey:      strings.TrimSpace(h.Get(HeaderYouTubeAPIKey)),
		MastodonToken:      strings.TrimSpace(h.Get(HeaderMastodonToken)),
		MastodonInstance:   strings.TrimSpace(h.Get(HeaderMastodonInstance)),
	}
}

// WithCredentials extracts caller API keys from request headers into the
// context. It wraps both the REST router and the GraphQL handler so every
// HTTP transport shares one extraction path.
func WithCredentials(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		creds := credsFromHeader(req.Header)
		if !creds.IsZero() {
			req = req.WithContext(context.WithValue(req.Context(), credsKey{}, creds))
		}
		next.ServeHTTP(w, req)
	})
}

// credsFromGRPC extracts the same keys from incoming gRPC metadata. Metadata
// keys are the header names lowercased, which is also what a client sending
// the canonical names gets after gRPC's own normalization.
func credsFromGRPC(ctx context.Context) connectors.Credentials {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return connectors.Credentials{}
	}
	get := func(name string) string {
		vals := md.Get(strings.ToLower(name)) // md.Get lowercases, but be explicit
		if len(vals) == 0 {
			return ""
		}
		return strings.TrimSpace(vals[0])
	}
	return connectors.Credentials{
		RedditClientID:     get(HeaderRedditClientID),
		RedditClientSecret: get(HeaderRedditClientSecret),
		TwitterBearerToken: get(HeaderTwitterBearerToken),
		YouTubeAPIKey:      get(HeaderYouTubeAPIKey),
		MastodonToken:      get(HeaderMastodonToken),
		MastodonInstance:   get(HeaderMastodonInstance),
	}
}
