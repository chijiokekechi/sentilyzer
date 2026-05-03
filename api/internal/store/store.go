// Package store persists analysis results to SQLite (default) or any DSN
// understood by modernc.org/sqlite — Postgres can be slotted in by swapping
// the driver and DSN; the schema is intentionally portable.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

type Store struct {
	db *sql.DB
}

func Open(dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("store: empty DSN")
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS analyses (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			topic        TEXT,
			platform     TEXT,
			document_id  TEXT,
			source_url   TEXT,
			label        TEXT NOT NULL,
			polarity     REAL NOT NULL,
			confidence   REAL NOT NULL,
			aspects_json TEXT,
			created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_analyses_topic_created
			ON analyses(topic, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_analyses_doc_id ON analyses(document_id);
	`)
	return err
}

// SaveTopicResults inserts a batch of (sourced) analyses for a topic.
func (s *Store) SaveTopicResults(
	ctx context.Context,
	topic string,
	results []domain.SourcedDocumentResult,
) error {
	if len(results) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO analyses
		  (topic, platform, document_id, source_url, label, polarity, confidence, aspects_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, r := range results {
		aspectsJSON := []byte("[]")
		if len(r.Result.Aspects) > 0 {
			b, err := json.Marshal(r.Result.Aspects)
			if err == nil {
				aspectsJSON = b
			}
		}
		if _, err := stmt.ExecContext(
			ctx,
			topic,
			r.Document.Platform,
			r.Document.Document.ID,
			r.Document.URL,
			string(r.Result.Overall.Label),
			r.Result.Overall.Polarity,
			r.Result.Overall.Confidence,
			string(aspectsJSON),
			now,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
