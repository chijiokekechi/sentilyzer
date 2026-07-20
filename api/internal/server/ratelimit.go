package server

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/httprate"
)

// RateLimitConfig bounds request volume. A non-positive value in either field
// disables that limiter.
//
// Per-instance is sufficient here: the single-box deployment has one instance,
// and the primary+fallback deployment fails over rather than load-balances, so
// only one instance serves at a time and a per-instance limit is effectively a
// global one. No shared datastore (Redis) is needed for this to be meaningful.
type RateLimitConfig struct {
	PerIPPerMinute  int
	GlobalPerMinute int
}

// rateLimiters returns the middlewares implied by cfg — global first, then
// per-IP — for the caller to apply to the API routes.
func rateLimiters(cfg RateLimitConfig) []func(http.Handler) http.Handler {
	var mws []func(http.Handler) http.Handler
	if cfg.GlobalPerMinute > 0 {
		// One shared key = a single global bucket for the whole instance.
		mws = append(mws, httprate.LimitBy(
			cfg.GlobalPerMinute, time.Minute,
			func(*http.Request) (string, error) { return "*", nil },
			httprate.WithLimitHandler(rateLimited),
		))
	}
	if cfg.PerIPPerMinute > 0 {
		mws = append(mws, httprate.LimitBy(
			cfg.PerIPPerMinute, time.Minute,
			clientIPKey,
			httprate.WithLimitHandler(rateLimited),
		))
	}
	return mws
}

// clientIPKey keys the per-IP limiter on the client address. chi's RealIP
// middleware runs first and rewrites RemoteAddr from X-Forwarded-For /
// X-Real-IP, so behind Caddy/Cloudflare this is the real client IP, not the
// proxy's. The gateway binds only to loopback in the deployment, so nothing
// but the local proxy can reach it to spoof that header.
func clientIPKey(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr, nil
	}
	return host, nil
}

// rateLimited renders a 429 in the negotiated format. It runs after
// negotiateMedia, so formatFrom resolves; Retry-After is set before the body
// because writePayload commits the status.
func rateLimited(w http.ResponseWriter, req *http.Request) {
	if w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", strconv.Itoa(int(time.Minute.Seconds())))
	}
	writePayload(w, req, http.StatusTooManyRequests, errResp{
		Error:  "rate limit exceeded",
		Status: http.StatusTooManyRequests,
	})
}
