package harvest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// BlueskyFilter selects which firehose posts enter the corpus. Filters apply
// to CREATES only — deletes and account deactivations always produce
// tombstones, because a revocation must be honored even for documents the
// sample happened to skip.
type BlueskyFilter struct {
	// Langs admits posts declaring at least one of these ISO 639-1 codes
	// (region subtags are stripped, so "en-US" matches "en"). Empty = any.
	// Posts declaring no language are excluded when a filter is set.
	Langs []string
	// MinChars/MaxChars bound the post length; 0 disables the bound.
	MinChars int
	MaxChars int
	// SampleRate keeps this deterministic fraction of qualifying posts
	// (see SampleHash). 1.0 keeps everything.
	SampleRate float64
}

func (f BlueskyFilter) admit(docID, text string, langs []string) bool {
	n := len(text)
	if f.MinChars > 0 && n < f.MinChars {
		return false
	}
	if f.MaxChars > 0 && n > f.MaxChars {
		return false
	}
	if len(f.Langs) > 0 {
		ok := false
		for _, l := range langs {
			base, _, _ := strings.Cut(strings.ToLower(l), "-")
			for _, want := range f.Langs {
				if base == strings.ToLower(want) {
					ok = true
					break
				}
			}
		}
		if !ok {
			return false
		}
	}
	return SampleHash(docID, f.SampleRate)
}

// BlueskyHarvester streams the firehose into a corpus directory, honoring
// the corpus policy's conditions: firehose as the bulk interface, author DID
// on every row, delete events as tombstones, account deactivations as
// account-scoped tombstones.
type BlueskyHarvester struct {
	Client *JetstreamClient
	Writer *CorpusWriter
	Filter BlueskyFilter
	Logger *slog.Logger
	// CursorPath persists resume state; empty disables persistence.
	CursorPath string
	// CursorEvery bounds how often the cursor file is rewritten (default 5s).
	CursorEvery time.Duration

	// counters, read via Stats after Run returns (single goroutine writes).
	docs, tombs, skipped int64
}

// Stats reports what a Run did.
func (h *BlueskyHarvester) Stats() (docs, tombstones, skipped int64) {
	return h.docs, h.tombs, h.skipped
}

func (h *BlueskyHarvester) handle(ev Event) error {
	switch ev.Kind {
	case "account":
		// An account deactivating or being taken down revokes its content.
		if ev.Account != nil && !ev.Account.Active {
			h.tombs++
			return h.Writer.WriteTombstone(Tombstone{
				Platform:  h.Writer.Platform,
				Scope:     "account",
				AuthorDID: ev.DID,
			})
		}
		return nil
	case "commit":
	default:
		return nil // identity churn et al.
	}
	c := ev.Commit
	if c == nil || c.Collection != postCollection {
		return nil
	}
	docID := ev.DID + "/" + postCollection + "/" + c.RKey

	switch c.Operation {
	case "delete":
		h.tombs++
		return h.Writer.WriteTombstone(Tombstone{
			Platform:  h.Writer.Platform,
			Scope:     "doc",
			DocID:     docID,
			AuthorDID: ev.DID,
		})
	case "create", "update":
		if c.Record == nil {
			return nil
		}
		text := strings.TrimSpace(c.Record.Text)
		if text == "" || !h.Filter.admit(docID, text, c.Record.Langs) {
			h.skipped++
			return nil
		}
		h.docs++
		return h.Writer.WriteDocument(Document{
			Platform:  h.Writer.Platform,
			DocID:     docID,
			AuthorDID: ev.DID,
			Text:      text,
			Langs:     c.Record.Langs,
			CreatedAt: c.Record.CreatedAt,
		})
	}
	return nil
}

// Run streams until ctx ends, reconnecting with capped exponential backoff
// and persisting the cursor as it goes. It returns nil on a clean
// ctx-cancelled shutdown.
func (h *BlueskyHarvester) Run(ctx context.Context) error {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cursorEvery := h.CursorEvery
	if cursorEvery <= 0 {
		cursorEvery = 5 * time.Second
	}

	cursor := int64(0)
	if h.CursorPath != "" {
		if cursor = LoadCursor(h.CursorPath); cursor > 0 {
			logger.Info("harvest resuming", "cursor_us", cursor)
		}
	}

	backoff := time.Second
	for {
		lastSave := time.Now()
		last, err := h.Client.Stream(ctx, cursor, func(ev Event) error {
			if err := h.handle(ev); err != nil {
				return err
			}
			if h.CursorPath != "" && ev.TimeUS > 0 && time.Since(lastSave) >= cursorEvery {
				lastSave = time.Now()
				if err := SaveCursor(h.CursorPath, ev.TimeUS); err != nil {
					logger.Warn("cursor save failed", "err", err)
				}
			}
			return nil
		})
		if last > cursor {
			cursor = last
			if h.CursorPath != "" {
				if err := SaveCursor(h.CursorPath, cursor); err != nil {
					logger.Warn("cursor save failed", "err", err)
				}
			}
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil // clean shutdown
		}
		logger.Warn("harvest stream dropped; reconnecting",
			"err", err, "backoff", backoff.String(),
			"docs", h.docs, "tombstones", h.tombs)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}
