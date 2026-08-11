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

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// Bluesky searches public posts through the AppView's searchPosts endpoint.
// Keyless: api.bsky.app answers unauthenticated search (verified 2026-08-09;
// the documented "public" host public.api.bsky.app 403s search, so this uses
// the host the bsky.app frontend itself queries). If Bluesky ever gates it,
// the failure surfaces as a per-connector warning and an authenticated
// session (app password) can be added then.
type Bluesky struct {
	HTTP    *http.Client
	BaseURL string // overridable for tests; defaults to https://api.bsky.app
}

func NewBluesky(client *http.Client) *Bluesky {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Bluesky{HTTP: client, BaseURL: "https://api.bsky.app"}
}

func (*Bluesky) ID() string              { return "bluesky" }
func (*Bluesky) DisplayName() string     { return "Bluesky" }
func (*Bluesky) Enabled() (bool, string) { return true, "" }

// Policy: eligible for the training corpus per docs/corpus-policy.md
// (signed 2026-08-10), under four binding conditions that apply to the BULK
// harvester, not this search connector: harvest via the firehose/Jetstream,
// persist author DIDs, honor delete events, and adopt Bluesky's opt-out
// framework the day it ships. searchPosts supports since/until windows.
func (*Bluesky) Policy() Policy { return Policy{Durable: true, Backfillable: true} }

type blueskySearchResp struct {
	Posts []struct {
		URI    string `json:"uri"`
		Author struct {
			DID    string `json:"did"`
			Handle string `json:"handle"`
		} `json:"author"`
		Record struct {
			Text      string    `json:"text"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"record"`
	} `json:"posts"`
}

func (b *Bluesky) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if q.Topic == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	base := b.BaseURL
	if base == "" {
		base = "https://api.bsky.app"
	}
	u, _ := url.Parse(base + "/xrpc/app.bsky.feed.searchPosts")
	v := u.Query()
	v.Set("q", andLocation(q.Topic, q.Location))
	v.Set("sort", "latest")
	v.Set("limit", strconv.Itoa(limit))
	if q.Language != "" {
		v.Set("lang", q.Language) // searchPosts takes ISO 639-1 directly
	}
	if q.SinceSeconds > 0 {
		since := time.Now().UTC().Add(-time.Duration(q.SinceSeconds) * time.Second)
		v.Set("since", since.Format(time.RFC3339))
	}
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bluesky: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bluesky: status %d", resp.StatusCode)
	}
	var body blueskySearchResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("bluesky decode: %w", err)
	}

	out := make([]domain.SourcedDocument, 0, len(body.Posts))
	for _, p := range body.Posts {
		text := strings.TrimSpace(p.Record.Text)
		if text == "" {
			continue
		}
		// at://did:plc:xxx/app.bsky.feed.post/<rkey> — the rkey is only
		// unique per author, so the document ID keeps the full URI path.
		rkey := p.URI[strings.LastIndex(p.URI, "/")+1:]
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:   "bluesky-" + strings.TrimPrefix(p.URI, "at://"),
				Text: text,
			},
			Platform: b.ID(),
			Author:   p.Author.Handle,
			URL:      "https://bsky.app/profile/" + p.Author.Handle + "/post/" + rkey,
			PostedAt: p.Record.CreatedAt.UTC(),
		})
	}
	return out, nil
}
