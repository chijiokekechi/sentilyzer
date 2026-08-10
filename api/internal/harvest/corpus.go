package harvest

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HashVersion identifies the normalize() used for ContentSHA. Changing the
// normalization is a one-way door — every historical hash stops matching new
// ones and dedupe silently re-admits duplicates — so any change MUST bump
// this constant, and the prep step must treat versions as incomparable.
const HashVersion = 1

// Document is one corpus row. AuthorDID is mandatory for Bluesky rows: the
// corpus policy's condition (2) requires author provenance so a future
// opt-out or takedown can be honored per account.
type Document struct {
	Platform    string   `json:"platform"`
	DocID       string   `json:"doc_id"`
	AuthorDID   string   `json:"author_did,omitempty"`
	Author      string   `json:"author,omitempty"`
	Text        string   `json:"text"`
	Langs       []string `json:"langs,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	HarvestedAt string   `json:"harvested_at"`
	ContentSHA  string   `json:"content_sha256"`
	HashVersion int      `json:"hash_version"`
}

// Tombstone revokes previously harvested content. The prep step must
// anti-join tombstones before any training run — this is how conditions (3)
// (delete events) and, later, the opt-out framework are honored.
type Tombstone struct {
	Platform string `json:"platform"`
	// Scope is "doc" (one post) or "account" (everything by AuthorDID —
	// emitted when an account deactivates or is taken down).
	Scope     string `json:"scope"`
	DocID     string `json:"doc_id,omitempty"`
	AuthorDID string `json:"author_did"`
	DeletedAt string `json:"deleted_at"`
}

// Normalize collapses whitespace and lowercases — hash_version 1. See
// HashVersion before touching this.
func Normalize(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// ContentSHA256 is the corpus dedupe key: reposts and crossposts of the same
// text hash identically regardless of source document identity.
func ContentSHA256(text string) string {
	sum := sha256.Sum256([]byte(Normalize(text)))
	return hex.EncodeToString(sum[:])
}

// SampleHash reports whether key falls inside a deterministic sample of the
// given rate (0..1]. Hash-based rather than random so a re-run over the same
// events (cursor rewind, backfill overlap) selects the same documents —
// keeping harvesting idempotent instead of resampling on every retry.
func SampleHash(key string, rate float64) bool {
	if rate >= 1 {
		return true
	}
	if rate <= 0 {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	v := binary.BigEndian.Uint64(sum[:8])
	return float64(v)/float64(^uint64(0)) < rate
}

// CorpusWriter appends documents and tombstones as gzipped JSONL under
//
//	<root>/documents/dt=<utc-date>/platform=<id>/part-<start>.jsonl.gz
//	<root>/tombstones/dt=<utc-date>/platform=<id>/part-<start>.jsonl.gz
//
// Files rotate when the UTC date changes. Append-only by design: revocation
// is expressed as tombstones, never by rewriting history, so a partially
// written file after a crash costs at most a re-harvestable window.
type CorpusWriter struct {
	Root     string
	Platform string
	// Now is the clock; time.Now if nil (injectable for tests).
	Now func() time.Time

	mu   sync.Mutex
	docs *partFile
	tomb *partFile
}

type partFile struct {
	date string
	f    *os.File
	gz   *gzip.Writer
	enc  *json.Encoder
}

func (w *CorpusWriter) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *CorpusWriter) part(kind string, current *partFile) (*partFile, error) {
	date := w.now().UTC().Format("2006-01-02")
	if current != nil && current.date == date {
		return current, nil
	}
	if current != nil {
		if err := closePart(current); err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(w.Root, kind, "dt="+date, "platform="+w.Platform)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("corpus mkdir: %w", err)
	}
	name := fmt.Sprintf("part-%d.jsonl.gz", w.now().UnixNano())
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("corpus open: %w", err)
	}
	gz := gzip.NewWriter(f)
	return &partFile{date: date, f: f, gz: gz, enc: json.NewEncoder(gz)}, nil
}

func closePart(p *partFile) error {
	if p == nil {
		return nil
	}
	if err := p.gz.Close(); err != nil {
		_ = p.f.Close()
		return err
	}
	return p.f.Close()
}

// WriteDocument appends one corpus row, filling HarvestedAt/ContentSHA.
func (w *CorpusWriter) WriteDocument(d Document) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, err := w.part("documents", w.docs)
	if err != nil {
		return err
	}
	w.docs = p
	d.HarvestedAt = w.now().UTC().Format(time.RFC3339)
	d.ContentSHA = ContentSHA256(d.Text)
	d.HashVersion = HashVersion
	return p.enc.Encode(d)
}

// WriteTombstone appends one revocation row.
func (w *CorpusWriter) WriteTombstone(t Tombstone) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, err := w.part("tombstones", w.tomb)
	if err != nil {
		return err
	}
	w.tomb = p
	t.DeletedAt = w.now().UTC().Format(time.RFC3339)
	return p.enc.Encode(t)
}

// Close flushes and closes any open part files.
func (w *CorpusWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var first error
	for _, p := range []*partFile{w.docs, w.tomb} {
		if err := closePart(p); err != nil && first == nil {
			first = err
		}
	}
	w.docs, w.tomb = nil, nil
	return first
}

// Cursor persistence: a tiny JSON file so a restarted harvester resumes the
// stream instead of gapping or replaying from now.

type cursorState struct {
	TimeUS  int64  `json:"time_us"`
	SavedAt string `json:"saved_at"`
}

// SaveCursor atomically writes the cursor file (temp + rename, so a crash
// mid-write never truncates the previous good cursor).
func SaveCursor(path string, timeUS int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(cursorState{
		TimeUS:  timeUS,
		SavedAt: time.Now().UTC().Format(time.RFC3339),
	})
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadCursor returns the saved cursor, or 0 when none exists yet.
func LoadCursor(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var st cursorState
	if json.Unmarshal(b, &st) != nil {
		return 0
	}
	return st.TimeUS
}
