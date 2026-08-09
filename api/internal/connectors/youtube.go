package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// YouTube uses the public Data API v3 search endpoint.
// Requires YOUTUBE_API_KEY (free, generous quota).
type YouTube struct {
	HTTP  *http.Client
	Creds config.YouTubeCreds
}

func NewYouTube(client *http.Client, creds config.YouTubeCreds) *YouTube {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &YouTube{HTTP: client, Creds: creds}
}

func (YouTube) ID() string          { return "youtube" }
func (YouTube) DisplayName() string { return "YouTube (video titles + descriptions)" }
func (y *YouTube) Enabled() (bool, string) {
	if !y.Creds.Enabled() {
		return false, "missing YOUTUBE_API_KEY (or send an X-Sentilyzer-Youtube-Api-Key header)"
	}
	return true, ""
}

// EnabledWith also accepts a caller-supplied API key.
func (y *YouTube) EnabledWith(creds Credentials) (bool, string) {
	if creds.YouTubeAPIKey != "" {
		return true, ""
	}
	return y.Enabled()
}

// keyFor prefers the caller's key for this one request.
func (y *YouTube) keyFor(q Query) string {
	if q.Creds.YouTubeAPIKey != "" {
		return q.Creds.YouTubeAPIKey
	}
	return y.Creds.APIKey
}

type youtubeSearchResp struct {
	Items []struct {
		ID struct {
			VideoID string `json:"videoId"`
		} `json:"id"`
		Snippet struct {
			Title        string    `json:"title"`
			Description  string    `json:"description"`
			ChannelTitle string    `json:"channelTitle"`
			PublishedAt  time.Time `json:"publishedAt"`
		} `json:"snippet"`
	} `json:"items"`
}

func (y *YouTube) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if ok, _ := y.EnabledWith(q.Creds); !ok {
		return nil, nil
	}
	if q.Topic == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 50 {
		limit = 50
	}
	u, _ := url.Parse("https://www.googleapis.com/youtube/v3/search")
	v := u.Query()
	v.Set("part", "snippet")
	v.Set("q", q.Topic)
	v.Set("maxResults", strconv.Itoa(limit))
	v.Set("type", "video")
	v.Set("key", y.keyFor(q))
	if q.SinceSeconds > 0 {
		published := time.Now().UTC().Add(-time.Duration(q.SinceSeconds) * time.Second)
		v.Set("publishedAfter", published.Format(time.RFC3339))
	}
	if q.Language != "" {
		v.Set("relevanceLanguage", q.Language)
	}
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := y.HTTP.Do(req)
	if err != nil {
		// The API key travels in the URL query string, and a transport-level
		// *url.Error prints the full URL — which would put the key into an
		// error that now reaches clients via the response's warnings[]. Strip
		// the URL and keep only the underlying cause.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return nil, fmt.Errorf("youtube: %s: %w", uerr.Op, uerr.Err)
		}
		return nil, fmt.Errorf("youtube: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube: status %d", resp.StatusCode)
	}
	var body youtubeSearchResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("youtube decode: %w", err)
	}
	out := make([]domain.SourcedDocument, 0, len(body.Items))
	for _, item := range body.Items {
		text := item.Snippet.Title
		if item.Snippet.Description != "" {
			text = text + "\n" + item.Snippet.Description
		}
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:   "youtube-" + item.ID.VideoID,
				Text: text,
			},
			Platform: y.ID(),
			Author:   item.Snippet.ChannelTitle,
			URL:      "https://www.youtube.com/watch?v=" + item.ID.VideoID,
			PostedAt: item.Snippet.PublishedAt,
		})
	}
	return out, nil
}
