package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// HackerNews uses HN's free Algolia search API:
// https://hn.algolia.com/api
//
// No auth required, generous public limits, no scraping required.
type HackerNews struct {
	HTTP    *http.Client
	BaseURL string // overridable for tests; defaults to https://hn.algolia.com
}

func NewHackerNews(client *http.Client) *HackerNews {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &HackerNews{HTTP: client, BaseURL: "https://hn.algolia.com"}
}

func (HackerNews) ID() string              { return "hackernews" }
func (HackerNews) DisplayName() string     { return "Hacker News" }
func (HackerNews) Enabled() (bool, string) { return true, "" }

// Policy: HN's public APIs are sanctioned interfaces with no training
// prohibition (docs/corpus-policy.md, audited 2026-07-15). Algolia's
// created_at_i filters make historical windows reachable.
func (HackerNews) Policy() Policy { return Policy{Durable: true, Backfillable: true} }

type hnResponse struct {
	Hits []struct {
		ObjectID    string `json:"objectID"`
		Title       string `json:"title"`
		StoryText   string `json:"story_text"`
		CommentText string `json:"comment_text"`
		Author      string `json:"author"`
		URL         string `json:"url"`
		CreatedAt   string `json:"created_at"`
	} `json:"hits"`
}

func (h *HackerNews) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if q.Topic == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	base := h.BaseURL
	if base == "" {
		base = "https://hn.algolia.com"
	}
	endpoint := base + "/api/v1/search"
	if q.SinceSeconds > 0 {
		endpoint = base + "/api/v1/search_by_date"
	}
	u, _ := url.Parse(endpoint)
	v := u.Query()
	v.Set("query", q.Topic)
	v.Set("hitsPerPage", strconv.Itoa(limit))
	v.Set("tags", "(story,comment)")
	if q.SinceSeconds > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(q.SinceSeconds) * time.Second).Unix()
		v.Set("numericFilters", fmt.Sprintf("created_at_i>%d", cutoff))
	}
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hackernews search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hackernews: status %d", resp.StatusCode)
	}
	var body hnResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("hackernews decode: %w", err)
	}
	out := make([]domain.SourcedDocument, 0, len(body.Hits))
	for _, hit := range body.Hits {
		text := firstNonEmpty(hit.CommentText, hit.StoryText, hit.Title)
		if text == "" {
			continue
		}
		posted, _ := time.Parse(time.RFC3339, hit.CreatedAt)
		linkURL := hit.URL
		if linkURL == "" {
			linkURL = "https://news.ycombinator.com/item?id=" + hit.ObjectID
		}
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:   "hn-" + hit.ObjectID,
				Text: stripHTML(text),
			},
			Platform: h.ID(),
			Author:   hit.Author,
			URL:      linkURL,
			PostedAt: posted,
		})
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
