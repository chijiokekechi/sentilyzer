package connectors

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/mmcdole/gofeed"
)

// RSS searches a configurable list of feeds. By default it includes a small
// curated set of consumer/business news feeds; operators can extend it.
//
// This is a "free, no-API-key" path that lets sentiment monitoring work for
// long-tail topics (housing, niche products, regional news) where social
// platforms wouldn't surface enough volume.
type RSS struct {
	HTTP   *http.Client
	Feeds  []string
	Parser *gofeed.Parser
}

// DefaultFeeds is a small curated list — operators can override via NewRSS.
var DefaultFeeds = []string{
	"https://feeds.bbci.co.uk/news/business/rss.xml",
	"https://feeds.npr.org/1006/rss.xml",
	"https://hnrss.org/frontpage",
	// reuters.com/markets/rss was here and served 401 (DataDome) on every
	// request. Because Search only surfaces firstErr when no feed matched,
	// it stayed invisible on topics the other feeds covered and reported a
	// bare "401 Unauthorized" on topics they didn't — blaming a dead feed
	// for what was really an empty search.
}

func NewRSS(client *http.Client, feeds []string) *RSS {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if len(feeds) == 0 {
		feeds = DefaultFeeds
	}
	parser := gofeed.NewParser()
	parser.Client = client
	return &RSS{HTTP: client, Feeds: feeds, Parser: parser}
}

func (*RSS) ID() string              { return "rss" }
func (*RSS) DisplayName() string     { return "RSS / Atom feeds" }
func (*RSS) Enabled() (bool, string) { return true, "" }

// Policy: feeds are deliberately-syndicated content, lawfully acquired
// (docs/corpus-policy.md). A feed exposes only its current items, so a
// missed harvest window is gone permanently.
func (*RSS) Policy() Policy { return Policy{Durable: true, Backfillable: false} }

func (r *RSS) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if q.Topic == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}
	needle := strings.ToLower(q.Topic)
	var cutoff time.Time
	if q.SinceSeconds > 0 {
		cutoff = time.Now().Add(-time.Duration(q.SinceSeconds) * time.Second)
	}

	type result struct {
		docs []domain.SourcedDocument
		err  error
	}
	resCh := make(chan result, len(r.Feeds))
	for _, feedURL := range r.Feeds {
		feedURL := feedURL
		go func() {
			feed, err := r.Parser.ParseURLWithContext(feedURL, ctx)
			if err != nil {
				resCh <- result{nil, err}
				return
			}
			docs := make([]domain.SourcedDocument, 0)
			for _, item := range feed.Items {
				if item == nil {
					continue
				}
				combined := item.Title + " " + item.Description
				if !strings.Contains(strings.ToLower(combined), needle) {
					continue
				}
				if !cutoff.IsZero() && item.PublishedParsed != nil && item.PublishedParsed.Before(cutoff) {
					continue
				}
				body := strings.TrimSpace(stripHTML(item.Description))
				if body == "" {
					body = item.Title
				}
				posted := time.Now()
				if item.PublishedParsed != nil {
					posted = *item.PublishedParsed
				}
				docs = append(docs, domain.SourcedDocument{
					Document: domain.Document{
						ID:   "rss-" + hashID(feedURL+"|"+item.GUID+"|"+item.Link),
						Text: body,
					},
					Platform: "rss",
					Author:   feed.Title,
					URL:      item.Link,
					PostedAt: posted,
				})
			}
			resCh <- result{docs, nil}
		}()
	}

	var out []domain.SourcedDocument
	var firstErr error
	for range r.Feeds {
		res := <-resCh
		if res.err != nil && firstErr == nil {
			firstErr = res.err
			continue
		}
		out = append(out, res.docs...)
		if len(out) >= limit {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	if len(out) == 0 && firstErr != nil {
		return nil, fmt.Errorf("rss: %w", firstErr)
	}
	return out, nil
}

func hashID(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:8])
}
