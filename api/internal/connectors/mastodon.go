package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// Mastodon hits the search v2 endpoint of any Mastodon instance.
// Requires MASTODON_INSTANCE + MASTODON_ACCESS_TOKEN.
type Mastodon struct {
	HTTP  *http.Client
	Creds config.MastodonCreds
}

func NewMastodon(client *http.Client, creds config.MastodonCreds) *Mastodon {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Mastodon{HTTP: client, Creds: creds}
}

func (Mastodon) ID() string          { return "mastodon" }
func (Mastodon) DisplayName() string { return "Mastodon" }
func (m *Mastodon) Enabled() (bool, string) {
	if !m.Creds.Enabled() {
		return false, "missing MASTODON_ACCESS_TOKEN (or send an X-Sentilyzer-Mastodon-Token header)"
	}
	return true, ""
}

func (Mastodon) Policy() Policy {
	return Policy{Reason: "instance operators opted out of AI use via robots.txt; excluded on judgment (docs/corpus-policy.md)"}
}

// EnabledWith also accepts a caller-supplied access token (and optionally the
// caller's own instance; without one the server's default instance is used).
func (m *Mastodon) EnabledWith(creds Credentials) (bool, string) {
	if creds.MastodonToken != "" {
		return true, ""
	}
	return m.Enabled()
}

// instanceFor prefers the caller's instance for this one request. A
// caller-supplied instance is an OUTBOUND FETCH TARGET THE CALLER CONTROLS,
// so it is validated against SSRF before use: https only, and the host must
// not resolve to a private, loopback, link-local (cloud metadata lives at
// 169.254.169.254), or unspecified address. The server-configured instance is
// the operator's own choice and is not re-validated.
func (m *Mastodon) instanceFor(ctx context.Context, q Query) (string, error) {
	if q.Creds.MastodonToken != "" && q.Creds.MastodonInstance != "" {
		if err := validateCallerInstance(ctx, q.Creds.MastodonInstance); err != nil {
			return "", fmt.Errorf("mastodon instance rejected: %w", err)
		}
		return q.Creds.MastodonInstance, nil
	}
	return m.Creds.Instance, nil
}

// validateCallerInstance rejects instance URLs that would turn the gateway
// into a proxy for the caller into networks only the gateway can reach.
// Residual risk: DNS rebinding between this resolution and the actual dial is
// not closed here; doing so needs a pinning dialer, which is deliberately
// deferred until BYOK sees real use.
func validateCallerInstance(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("must be https")
	}
	if u.Hostname() == "" {
		return errors.New("missing host")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil {
		return fmt.Errorf("host does not resolve: %w", err)
	}
	for _, ip := range ips {
		if ip.IP.IsLoopback() || ip.IP.IsPrivate() || ip.IP.IsLinkLocalUnicast() ||
			ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() {
			return errors.New("host resolves to a non-public address")
		}
	}
	return nil
}

func (m *Mastodon) tokenFor(q Query) string {
	if q.Creds.MastodonToken != "" {
		return q.Creds.MastodonToken
	}
	return m.Creds.AccessToken
}

type mastodonStatus struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	Account   struct {
		Username string `json:"username"`
	} `json:"account"`
}

type mastodonSearchResp struct {
	Statuses []mastodonStatus `json:"statuses"`
}

func (m *Mastodon) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if ok, _ := m.EnabledWith(q.Creds); !ok {
		return nil, nil
	}
	if q.Topic == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 40 {
		limit = 40
	}
	instance, err := m.instanceFor(ctx, q)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(instance, "/") + "/api/v2/search"
	u, _ := url.Parse(base)
	v := u.Query()
	v.Set("q", andLocation(q.Topic, q.Location))
	v.Set("type", "statuses")
	v.Set("limit", strconv.Itoa(limit))
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+m.tokenFor(q))
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mastodon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mastodon: status %d", resp.StatusCode)
	}
	var body mastodonSearchResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("mastodon decode: %w", err)
	}
	out := make([]domain.SourcedDocument, 0, len(body.Statuses))
	cutoff := time.Time{}
	if q.SinceSeconds > 0 {
		cutoff = time.Now().Add(-time.Duration(q.SinceSeconds) * time.Second)
	}
	for _, s := range body.Statuses {
		if !cutoff.IsZero() && s.CreatedAt.Before(cutoff) {
			continue
		}
		text := stripHTML(s.Content)
		if text == "" {
			continue
		}
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:   "mastodon-" + s.ID,
				Text: text,
			},
			Platform: m.ID(),
			Author:   s.Account.Username,
			URL:      s.URL,
			PostedAt: s.CreatedAt,
		})
	}
	return out, nil
}
