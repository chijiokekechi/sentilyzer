package harvest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

// DefaultRSSFeeds duplicates the serving connector's curated list on
// purpose: harvest must not import the serving stack. Source of truth:
// connectors.DefaultFeeds in internal/connectors/rss.go. A test pins the
// two lists equal so they cannot drift silently.
var DefaultRSSFeeds = []string{
	"https://feeds.bbci.co.uk/news/business/rss.xml",
	"https://feeds.npr.org/1006/rss.xml",
	"https://hnrss.org/frontpage",
}

// docHash derives a stable doc id from a source identifier (article URL,
// feed item link). 16 hex chars: ids only need uniqueness within one
// platform partition.
func docHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

var (
	rssTagRE = regexp.MustCompile(`<[^>]+>`)
	rssWSRE  = regexp.MustCompile(`\s+`)
)

// rssStripHTML removes tags and decodes entities, matching the serving
// connector's stripHTML so the same feed item normalizes (and therefore
// content-hashes) identically on both paths.
func rssStripHTML(s string) string {
	if s == "" {
		return ""
	}
	without := rssTagRE.ReplaceAllString(s, " ")
	decoded := html.UnescapeString(without)
	return strings.TrimSpace(rssWSRE.ReplaceAllString(decoded, " "))
}

// RSSHarvester fetches each configured feed once (batch mode) and writes
// summary-level corpus rows. Corpus policy (docs/corpus-policy.md): RSS is
// eligible at summary level only, so a row carries the feed's own
// title+summary content, never the linked article body.
type RSSHarvester struct {
	// HTTP fetches feeds; nil gets a 10s-timeout default so one hung feed
	// cannot stall the batch.
	HTTP   *http.Client
	Writer *CorpusWriter
	// Feeds to fetch; empty means DefaultRSSFeeds.
	Feeds  []string
	Logger *slog.Logger

	// counters, read via Stats after Run returns (single goroutine writes).
	docs, skipped, failed int64
}

// Stats reports what a Run did: rows written, items skipped (empty after
// normalization), and feeds that failed to fetch or parse.
func (h *RSSHarvester) Stats() (docs, skipped, failedFeeds int64) {
	return h.docs, h.skipped, h.failed
}

// Run performs one pass over the feeds. A failing feed logs and continues:
// feed outages are routine and must not cost the rest of the batch. Writer
// errors abort, because silently dropping rows is worse than a loud failure.
func (h *RSSHarvester) Run(ctx context.Context) error {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := h.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	feeds := h.Feeds
	if len(feeds) == 0 {
		feeds = DefaultRSSFeeds
	}
	parser := gofeed.NewParser()
	parser.Client = client

	for _, feedURL := range feeds {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		feed, err := parser.ParseURLWithContext(feedURL, ctx)
		if err != nil {
			h.failed++
			logger.Warn("rss feed failed; continuing", "feed", feedURL, "err", err)
			continue
		}
		for _, item := range feed.Items {
			if item == nil {
				continue
			}
			doc, ok := rssDocument(h.Writer.Platform, feed.Title, item)
			if !ok {
				h.skipped++
				continue
			}
			h.docs++
			if err := h.Writer.WriteDocument(doc); err != nil {
				return err
			}
		}
	}
	return nil
}

// rssDocument normalizes one feed item: text is title + summary joined by a
// newline, doc id is the item GUID (or the link hash when the feed declares
// none), created_at comes from pubDate when present.
func rssDocument(platform, feedTitle string, item *gofeed.Item) (Document, bool) {
	title := strings.TrimSpace(item.Title)
	summary := rssStripHTML(item.Description)
	text := title
	if summary != "" {
		if text == "" {
			text = summary
		} else {
			text += "\n" + summary
		}
	}
	if text == "" {
		return Document{}, false
	}
	docID := item.GUID
	if docID == "" {
		docID = docHash(item.Link)
	}
	var created string
	if item.PublishedParsed != nil {
		created = item.PublishedParsed.UTC().Format(time.RFC3339)
	}
	return Document{
		Platform:  platform,
		DocID:     docID,
		Author:    feedTitle,
		Text:      text,
		CreatedAt: created,
	}, true
}
