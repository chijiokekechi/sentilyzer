package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// Reddit uses the official OAuth API: https://www.reddit.com/dev/api/
// Requires REDDIT_CLIENT_ID + REDDIT_CLIENT_SECRET (script app — no user
// password needed since we use the application-only flow).
type Reddit struct {
	HTTP  *http.Client
	Creds config.RedditCreds

	tokenMu  sync.Mutex
	token    string
	tokenExp time.Time
}

func NewReddit(client *http.Client, creds config.RedditCreds) *Reddit {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Reddit{HTTP: client, Creds: creds}
}

func (*Reddit) ID() string          { return "reddit" }
func (*Reddit) DisplayName() string { return "Reddit" }
func (r *Reddit) Enabled() (bool, string) {
	if !r.Creds.Enabled() {
		return false, "missing REDDIT_CLIENT_ID / REDDIT_CLIENT_SECRET " +
			"(or send X-Connector-Reddit-Client-Id / -Secret headers)"
	}
	return true, ""
}

// EnabledWith also accepts caller-supplied Reddit app credentials.
func (r *Reddit) EnabledWith(creds Credentials) (bool, string) {
	if creds.RedditClientID != "" && creds.RedditClientSecret != "" {
		return true, ""
	}
	return r.Enabled()
}

type redditTokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type redditSearchResp struct {
	Data struct {
		Children []struct {
			Kind string `json:"kind"`
			Data struct {
				ID         string  `json:"id"`
				Subreddit  string  `json:"subreddit"`
				Author     string  `json:"author"`
				Title      string  `json:"title"`
				Selftext   string  `json:"selftext"`
				Permalink  string  `json:"permalink"`
				CreatedUTC float64 `json:"created_utc"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

// fetchToken runs the application-only OAuth flow for the given app creds.
func (r *Reddit) fetchToken(ctx context.Context, clientID, clientSecret string) (string, time.Time, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	req, _ := http.NewRequestWithContext(
		ctx, http.MethodPost,
		"https://www.reddit.com/api/v1/access_token",
		strings.NewReader(form.Encode()),
	)
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", r.Creds.UserAgent)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("reddit token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("reddit token: status %d", resp.StatusCode)
	}
	var body redditTokenResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", time.Time{}, fmt.Errorf("reddit token decode: %w", err)
	}
	return body.AccessToken, time.Now().Add(time.Duration(body.ExpiresIn) * time.Second), nil
}

// tokenFor returns a bearer token for the request. Caller-supplied creds get a
// fresh token every time — deliberately uncached, so nothing derived from a
// caller's secret outlives their request. Only the server's own credentials
// use the shared token cache.
func (r *Reddit) tokenFor(ctx context.Context, q Query) (string, error) {
	if q.Creds.RedditClientID != "" && q.Creds.RedditClientSecret != "" {
		token, _, err := r.fetchToken(ctx, q.Creds.RedditClientID, q.Creds.RedditClientSecret)
		return token, err
	}
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	if r.token != "" && time.Now().Before(r.tokenExp.Add(-30*time.Second)) {
		return r.token, nil
	}
	token, exp, err := r.fetchToken(ctx, r.Creds.ClientID, r.Creds.ClientSecret)
	if err != nil {
		return "", err
	}
	r.token = token
	r.tokenExp = exp
	return r.token, nil
}

func (r *Reddit) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if ok, _ := r.EnabledWith(q.Creds); !ok {
		return nil, nil
	}
	if q.Topic == "" {
		return nil, nil
	}
	token, err := r.tokenFor(ctx, q)
	if err != nil {
		return nil, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	u, _ := url.Parse("https://oauth.reddit.com/search")
	v := u.Query()
	v.Set("q", q.Topic)
	v.Set("limit", strconv.Itoa(limit))
	v.Set("sort", "relevance")
	v.Set("type", "link")
	if q.SinceSeconds > 0 {
		v.Set("t", redditTimeWindow(q.SinceSeconds))
	}
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", r.Creds.UserAgent)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reddit search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reddit search: status %d", resp.StatusCode)
	}
	var body redditSearchResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("reddit search decode: %w", err)
	}
	out := make([]domain.SourcedDocument, 0, len(body.Data.Children))
	for _, child := range body.Data.Children {
		d := child.Data
		text := strings.TrimSpace(d.Title)
		if d.Selftext != "" {
			text = text + "\n" + d.Selftext
		}
		if text == "" {
			continue
		}
		posted := time.Unix(int64(d.CreatedUTC), 0).UTC()
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:   "reddit-" + d.ID,
				Text: text,
			},
			Platform: r.ID(),
			Author:   d.Author,
			URL:      "https://www.reddit.com" + d.Permalink,
			PostedAt: posted,
		})
	}
	return out, nil
}

// redditTimeWindow translates seconds-back into one of Reddit's named windows.
func redditTimeWindow(secs int64) string {
	switch {
	case secs <= 60*60:
		return "hour"
	case secs <= 24*60*60:
		return "day"
	case secs <= 7*24*60*60:
		return "week"
	case secs <= 30*24*60*60:
		return "month"
	case secs <= 365*24*60*60:
		return "year"
	default:
		return "all"
	}
}
