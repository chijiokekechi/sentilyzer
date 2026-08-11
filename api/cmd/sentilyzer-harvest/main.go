// sentilyzer-harvest writes corpus-eligible sources into the training
// corpus, per docs/corpus-policy.md: the Bluesky firehose (streaming, the
// default source), RSS feeds (batch), and GDELT headlines (batch).
//
// Serving and harvesting stay separate processes on purpose: the gateway
// answers requests and persists nothing; this binary persists rows and runs
// only for sources the corpus policy admits.
//
//	sentilyzer-harvest -out /var/lib/sentilyzer/corpus \
//	    -langs en -min-chars 40 -sample 0.05 -duration 0
//	sentilyzer-harvest -source rss -out /var/lib/sentilyzer/corpus
//	sentilyzer-harvest -source gdelt -timespan 24h -out /var/lib/sentilyzer/corpus
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/harvest"
)

func main() {
	var (
		source   = flag.String("source", "bluesky", "corpus source: bluesky, rss, or gdelt")
		out      = flag.String("out", "corpus", "corpus root directory")
		duration = flag.Duration("duration", 0, "how long to run; 0 = until signalled (bluesky) or batch completion (rss, gdelt)")

		// bluesky (streaming)
		urlFlag  = flag.String("jetstream-url", harvest.DefaultJetstreamURL, "Jetstream subscribe endpoint")
		langs    = flag.String("langs", "en", "comma-separated ISO 639-1 language allowlist; empty = all")
		minChars = flag.Int("min-chars", 40, "minimum post length; 0 = no minimum")
		maxChars = flag.Int("max-chars", 2000, "maximum post length; 0 = no maximum")
		sample   = flag.Float64("sample", 0.05, "deterministic fraction of qualifying posts to keep (0..1]")

		// rss (batch)
		feeds = flag.String("feeds", "", "comma-separated RSS feed URLs; empty = the curated default list")

		// gdelt (batch)
		queries  = flag.String("queries", "", "comma-separated GDELT query terms; empty = the default broad topics")
		timespan = flag.Duration("timespan", 24*time.Hour, "GDELT lookback window per query")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	switch *source {
	case "bluesky", "rss", "gdelt":
	default:
		logger.Error("unknown -source; want bluesky, rss, or gdelt", "source", *source)
		os.Exit(2)
	}

	writer := &harvest.CorpusWriter{Root: *out, Platform: *source}
	defer func() {
		if err := writer.Close(); err != nil {
			logger.Error("corpus close failed", "err", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if *duration > 0 {
		var tcancel context.CancelFunc
		ctx, tcancel = context.WithTimeout(ctx, *duration)
		defer tcancel()
	}

	var err error
	switch *source {
	case "bluesky":
		err = runBluesky(ctx, logger, writer, *out, *urlFlag, splitCSV(*langs), *minChars, *maxChars, *sample)
	case "rss":
		err = runRSS(ctx, logger, writer, *out, splitCSV(*feeds))
	case "gdelt":
		err = runGDELT(ctx, logger, writer, *out, splitCSV(*queries), *timespan)
	}
	if err != nil {
		logger.Error("harvest failed", "err", err)
		os.Exit(1)
	}
}

// runBluesky streams the firehose until the context ends.
func runBluesky(ctx context.Context, logger *slog.Logger, writer *harvest.CorpusWriter, out, jetstreamURL string, langs []string, minChars, maxChars int, sample float64) error {
	h := &harvest.BlueskyHarvester{
		Client: &harvest.JetstreamClient{URL: jetstreamURL},
		Writer: writer,
		Filter: harvest.BlueskyFilter{
			Langs:      langs,
			MinChars:   minChars,
			MaxChars:   maxChars,
			SampleRate: sample,
		},
		Logger:     logger,
		CursorPath: filepath.Join(out, "cursors", "bluesky.json"),
	}
	logger.Info("harvest starting", "source", "bluesky", "out", out,
		"langs", langs, "sample", sample,
		"min_chars", minChars, "max_chars", maxChars)
	start := time.Now()
	if err := h.Run(ctx); err != nil {
		return err
	}
	docs, tombs, skipped := h.Stats()
	logger.Info("harvest stopped", "source", "bluesky",
		"ran", time.Since(start).Round(time.Second).String(),
		"documents", docs, "tombstones", tombs, "skipped", skipped)
	return nil
}

// runRSS performs one batch pass over the feeds and exits.
func runRSS(ctx context.Context, logger *slog.Logger, writer *harvest.CorpusWriter, out string, feeds []string) error {
	h := &harvest.RSSHarvester{Writer: writer, Feeds: feeds, Logger: logger}
	if len(feeds) == 0 {
		feeds = harvest.DefaultRSSFeeds
	}
	logger.Info("harvest starting", "source", "rss", "out", out, "feeds", len(feeds))
	start := time.Now()
	if err := h.Run(ctx); err != nil {
		return err
	}
	docs, skipped, failed := h.Stats()
	logger.Info("harvest stopped", "source", "rss",
		"ran", time.Since(start).Round(time.Second).String(),
		"documents", docs, "skipped", skipped, "failed_feeds", failed)
	return nil
}

// runGDELT performs one throttled batch pass over the queries and exits.
func runGDELT(ctx context.Context, logger *slog.Logger, writer *harvest.CorpusWriter, out string, queries []string, timespan time.Duration) error {
	h := &harvest.GDELTHarvester{Writer: writer, Queries: queries, Timespan: timespan, Logger: logger}
	if len(queries) == 0 {
		queries = harvest.DefaultGDELTQueries
	}
	logger.Info("harvest starting", "source", "gdelt", "out", out,
		"queries", len(queries), "timespan", timespan.String())
	start := time.Now()
	if err := h.Run(ctx); err != nil {
		return err
	}
	docs, skipped, failed := h.Stats()
	logger.Info("harvest stopped", "source", "gdelt",
		"ran", time.Since(start).Round(time.Second).String(),
		"documents", docs, "skipped", skipped, "failed_queries", failed)
	return nil
}

// splitCSV splits a comma-separated flag value, dropping blanks.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
