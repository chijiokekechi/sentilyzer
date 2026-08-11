package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// Twitter uses the X v2 recent search endpoint, which requires a Bearer
// token (Basic tier or higher). Without TWITTER_BEARER_TOKEN the connector
// reports disabled.
type Twitter struct {
	HTTP  *http.Client
	Creds config.TwitterCreds
}

func NewTwitter(client *http.Client, creds config.TwitterCreds) *Twitter {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Twitter{HTTP: client, Creds: creds}
}

func (Twitter) ID() string          { return "twitter" }
func (Twitter) DisplayName() string { return "Twitter / X" }
func (t *Twitter) Enabled() (bool, string) {
	if !t.Creds.Enabled() {
		return false, "missing TWITTER_BEARER_TOKEN"
	}
	return true, ""
}

func (Twitter) Policy() Policy {
	return Policy{Reason: "Developer Agreement III.A(d)/(k): derivative-works and training bans (docs/corpus-policy.md)"}
}

// Twitter deliberately does NOT accept caller-supplied keys: X's Developer
// Agreement (III.G) bans making any token, key, or password available to a
// third party, with no agent exception. Server-side key only.

type twitterRecentResp struct {
	Data []struct {
		ID        string    `json:"id"`
		Text      string    `json:"text"`
		AuthorID  string    `json:"author_id"`
		CreatedAt time.Time `json:"created_at"`
		Lang      string    `json:"lang"`
	} `json:"data"`
	Includes struct {
		Users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"users"`
	} `json:"includes"`
}

func (t *Twitter) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if ok, _ := t.Enabled(); !ok {
		return nil, nil
	}
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

	u, _ := url.Parse("https://api.twitter.com/2/tweets/search/recent")
	v := u.Query()
	query := andLocation(q.Topic, q.Location) + " -is:retweet"
	if q.Language != "" {
		query += " lang:" + q.Language
	}
	v.Set("query", query)
	v.Set("max_results", strconv.Itoa(limit))
	v.Set("tweet.fields", "created_at,lang,author_id")
	v.Set("expansions", "author_id")
	v.Set("user.fields", "username")
	if q.SinceSeconds > 0 {
		start := time.Now().UTC().Add(-time.Duration(q.SinceSeconds) * time.Second)
		v.Set("start_time", start.Format(time.RFC3339))
	}
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+t.Creds.BearerToken)
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("twitter: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("twitter: status %d", resp.StatusCode)
	}
	var body twitterRecentResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("twitter decode: %w", err)
	}
	users := map[string]string{}
	for _, u := range body.Includes.Users {
		users[u.ID] = u.Username
	}
	out := make([]domain.SourcedDocument, 0, len(body.Data))
	for _, tw := range body.Data {
		username := users[tw.AuthorID]
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:       "twitter-" + tw.ID,
				Text:     tw.Text,
				Language: tw.Lang,
			},
			Platform: t.ID(),
			Author:   username,
			URL:      fmt.Sprintf("https://twitter.com/%s/status/%s", username, tw.ID),
			PostedAt: tw.CreatedAt,
		})
	}
	return out, nil
}
