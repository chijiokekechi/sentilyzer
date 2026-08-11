package harvest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultGDELTBaseURL is the DOC 2.0 API host: keyless, no registration.
const DefaultGDELTBaseURL = "https://api.gdeltproject.org"

// gdeltMinInterval matches GDELT's published limit of roughly 1 request per
// 5 seconds per IP: the same discipline the serving connector enforces,
// because GDELT 429s aggressively past it. Known limitation: this throttle
// and the serving connector's are independent, so running a harvest while
// the API is answering GDELT queries from the same IP can briefly reach
// 2 req/5s. Harvesting is an occasional batch task; pause it if GDELT
// starts returning 429s.
const gdeltMinInterval = 5 * time.Second

// gdeltMaxRecords is GDELT's per-request cap.
const gdeltMaxRecords = 250

// DefaultGDELTQueries are deliberately broad: harvesting wants headline
// volume across domains, while precise topics stay a serving-time concern.
var DefaultGDELTQueries = []string{
	"economy",
	"inflation",
	"housing market",
	"stock market",
	"consumer spending",
	"artificial intelligence",
	"energy prices",
	"healthcare",
}

// GDELTHarvester runs each query once (batch mode) against the DOC 2.0
// ArtList endpoint and writes headline rows. Corpus policy
// (docs/corpus-policy.md): only GDELT's own fields enter the corpus (title,
// source domain, seendate); the linked article is NEVER fetched or stored,
// because that would reintroduce each publisher's copyright.
type GDELTHarvester struct {
	// HTTP issues API calls; nil gets a 10s-timeout default.
	HTTP *http.Client
	// BaseURL is overridable for tests; DefaultGDELTBaseURL if empty.
	BaseURL string
	Writer  *CorpusWriter
	// Queries to run; empty means DefaultGDELTQueries.
	Queries []string
	// Timespan is the per-query lookback window; 24h if zero.
	Timespan time.Duration
	Logger   *slog.Logger
	// Tick gates upstream calls: exactly one request per received tick.
	// Nil gets a real gdeltMinInterval ticker; tests inject their own.
	Tick <-chan time.Time

	// counters, read via Stats after Run returns (single goroutine writes).
	docs, skipped, failed int64
}

// Stats reports what a Run did: rows written, articles skipped (blank
// title), and queries that failed or were rejected.
func (h *GDELTHarvester) Stats() (docs, skipped, failedQueries int64) {
	return h.docs, h.skipped, h.failed
}

// Run issues one throttled request per query. Every request, including the
// first, waits for a tick: the simplest discipline that can never burst. A
// failing query logs and continues; writer errors abort.
func (h *GDELTHarvester) Run(ctx context.Context) error {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := h.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	queries := h.Queries
	if len(queries) == 0 {
		queries = DefaultGDELTQueries
	}
	tick := h.Tick
	if tick == nil {
		ticker := time.NewTicker(gdeltMinInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for _, q := range queries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
		}
		if err := h.harvestQuery(ctx, client, logger, q); err != nil {
			return err
		}
	}
	return nil
}

// gdeltArtList is the ArtList JSON shape; only GDELT's own fields are read.
type gdeltArtList struct {
	Articles []struct {
		URL      string `json:"url"`
		Title    string `json:"title"`
		SeenDate string `json:"seendate"` // 20060102T150405Z
		Domain   string `json:"domain"`
	} `json:"articles"`
}

func (h *GDELTHarvester) harvestQuery(ctx context.Context, client *http.Client, logger *slog.Logger, topic string) error {
	base := h.BaseURL
	if base == "" {
		base = DefaultGDELTBaseURL
	}
	timespan := h.Timespan
	if timespan <= 0 {
		timespan = 24 * time.Hour
	}
	u, err := url.Parse(base + "/api/v2/doc/doc")
	if err != nil {
		h.failed++
		logger.Warn("gdelt query failed; continuing", "query", topic, "err", err)
		return nil
	}
	v := u.Query()
	// Multi-word queries must be quoted or GDELT rejects them with
	// "please connect terms with OR/AND".
	v.Set("query", `"`+strings.ReplaceAll(topic, `"`, ``)+`"`)
	v.Set("mode", "ArtList")
	v.Set("format", "json")
	v.Set("maxrecords", strconv.Itoa(gdeltMaxRecords))
	v.Set("timespan", gdeltTimespan(timespan))
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := client.Do(req)
	if err != nil {
		h.failed++
		logger.Warn("gdelt query failed; continuing", "query", topic, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.failed++
		logger.Warn("gdelt query failed; continuing", "query", topic, "status", resp.StatusCode)
		return nil
	}
	// GDELT reports some query errors as HTTP-200 plain text, so a decode
	// failure means "the API declined the query", not corrupt JSON.
	var body gdeltArtList
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		h.failed++
		logger.Warn("gdelt query rejected; continuing", "query", topic)
		return nil
	}
	for _, a := range body.Articles {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			h.skipped++
			continue
		}
		var created string
		if t, err := time.Parse("20060102T150405Z", a.SeenDate); err == nil {
			created = t.UTC().Format(time.RFC3339)
		}
		h.docs++
		if err := h.Writer.WriteDocument(Document{
			Platform: h.Writer.Platform,
			DocID:    docHash(a.URL),
			Author:   a.Domain,
			// GDELT's own title field ONLY: never fetched article text.
			Text:      title,
			CreatedAt: created,
		}); err != nil {
			return err
		}
	}
	return nil
}

// gdeltTimespan renders a lookback window in GDELT's timespan syntax,
// clamped to GDELT's 15-minute minimum resolution. Mirrors the serving
// connector's rendering in internal/connectors/gdelt.go.
func gdeltTimespan(d time.Duration) string {
	secs := int64(d / time.Second)
	switch {
	case secs < 2*60*60:
		m := (secs + 59) / 60
		if m < 15 {
			m = 15
		}
		return fmt.Sprintf("%dmin", m)
	case secs < 72*60*60:
		return fmt.Sprintf("%dh", (secs+3599)/3600)
	default:
		return fmt.Sprintf("%dd", (secs+86399)/86400)
	}
}
