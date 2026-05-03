package connectors

import (
	"context"
	"encoding/json"
	"fmt"
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
		return false, "missing MASTODON_ACCESS_TOKEN"
	}
	return true, ""
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
	if ok, _ := m.Enabled(); !ok {
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
	base := strings.TrimRight(m.Creds.Instance, "/") + "/api/v2/search"
	u, _ := url.Parse(base)
	v := u.Query()
	v.Set("q", q.Topic)
	v.Set("type", "statuses")
	v.Set("limit", strconv.Itoa(limit))
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+m.Creds.AccessToken)
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
