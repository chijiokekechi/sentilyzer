// sentilyzer-harvest streams corpus-eligible sources into the training
// corpus. Today that is one source: the Bluesky firehose (Jetstream), per
// docs/corpus-policy.md and its four binding conditions.
//
// Serving and harvesting stay separate processes on purpose: the gateway
// answers requests and persists nothing; this binary persists rows and runs
// only for sources the corpus policy admits.
//
//	sentilyzer-harvest -out /var/lib/sentilyzer/corpus \
//	    -langs en -min-chars 40 -sample 0.05 -duration 0
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
		out      = flag.String("out", "corpus", "corpus root directory")
		urlFlag  = flag.String("jetstream-url", harvest.DefaultJetstreamURL, "Jetstream subscribe endpoint")
		langs    = flag.String("langs", "en", "comma-separated ISO 639-1 language allowlist; empty = all")
		minChars = flag.Int("min-chars", 40, "minimum post length; 0 = no minimum")
		maxChars = flag.Int("max-chars", 2000, "maximum post length; 0 = no maximum")
		sample   = flag.Float64("sample", 0.05, "deterministic fraction of qualifying posts to keep (0..1]")
		duration = flag.Duration("duration", 0, "how long to run; 0 = until signalled")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	var langList []string
	if s := strings.TrimSpace(*langs); s != "" {
		langList = strings.Split(s, ",")
	}

	writer := &harvest.CorpusWriter{Root: *out, Platform: "bluesky"}
	defer func() {
		if err := writer.Close(); err != nil {
			logger.Error("corpus close failed", "err", err)
		}
	}()

	h := &harvest.BlueskyHarvester{
		Client: &harvest.JetstreamClient{URL: *urlFlag},
		Writer: writer,
		Filter: harvest.BlueskyFilter{
			Langs:      langList,
			MinChars:   *minChars,
			MaxChars:   *maxChars,
			SampleRate: *sample,
		},
		Logger:     logger,
		CursorPath: filepath.Join(*out, "cursors", "bluesky.json"),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if *duration > 0 {
		var tcancel context.CancelFunc
		ctx, tcancel = context.WithTimeout(ctx, *duration)
		defer tcancel()
	}

	logger.Info("harvest starting",
		"out", *out, "langs", langList, "sample", *sample,
		"min_chars", *minChars, "max_chars", *maxChars)
	start := time.Now()
	if err := h.Run(ctx); err != nil {
		logger.Error("harvest failed", "err", err)
		os.Exit(1)
	}
	docs, tombs, skipped := h.Stats()
	logger.Info("harvest stopped",
		"ran", time.Since(start).Round(time.Second).String(),
		"documents", docs, "tombstones", tombs, "skipped", skipped)
}
