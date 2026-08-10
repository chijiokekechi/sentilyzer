package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// StockTwits queries the public symbol stream:
// https://api.stocktwits.com/api/2/streams/symbol/{SYMBOL}.json
//
// No auth required for the public read endpoint. Useful when the topic is
// a ticker like "$AAPL" or "$TSLA"; for non-ticker topics, returns nothing.
type StockTwits struct {
	HTTP *http.Client
}

func NewStockTwits(client *http.Client) *StockTwits {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &StockTwits{HTTP: client}
}

func (StockTwits) ID() string              { return "stocktwits" }
func (StockTwits) DisplayName() string     { return "StockTwits (cashtags)" }
func (StockTwits) Enabled() (bool, string) { return true, "" }

func (StockTwits) Policy() Policy {
	return Policy{Reason: "ToS bars extraction outside an approved API, and no approved path exists (docs/corpus-policy.md)"}
}

type stocktwitsResp struct {
	Messages []struct {
		ID        int64     `json:"id"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Username string `json:"username"`
		} `json:"user"`
	} `json:"messages"`
}

func (s *StockTwits) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	symbol := extractSymbol(q.Topic)
	if symbol == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 30 {
		limit = 30
	}
	endpoint := fmt.Sprintf("https://api.stocktwits.com/api/2/streams/symbol/%s.json?limit=%d", symbol, limit)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stocktwits: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Not a recognized ticker on StockTwits — no results, not an error.
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stocktwits: status %d", resp.StatusCode)
	}
	var body stocktwitsResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("stocktwits decode: %w", err)
	}
	out := make([]domain.SourcedDocument, 0, len(body.Messages))
	for _, m := range body.Messages {
		if m.Body == "" {
			continue
		}
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:   fmt.Sprintf("stocktwits-%d", m.ID),
				Text: m.Body,
			},
			Platform: "stocktwits",
			Author:   m.User.Username,
			URL:      fmt.Sprintf("https://stocktwits.com/%s/message/%d", m.User.Username, m.ID),
			PostedAt: m.CreatedAt,
		})
	}
	return out, nil
}

// extractSymbol pulls the first $TICKER cashtag out of a topic, or returns
// the topic itself if it looks ticker-like (1–5 uppercase letters).
func extractSymbol(topic string) string {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return ""
	}
	for _, word := range strings.Fields(topic) {
		w := strings.TrimPrefix(word, "$")
		if w == word {
			continue
		}
		w = strings.ToUpper(strings.Trim(w, ".,;:!?"))
		if isTickerShape(w) {
			return w
		}
	}
	if isTickerShape(strings.ToUpper(topic)) {
		return strings.ToUpper(topic)
	}
	return ""
}

func isTickerShape(s string) bool {
	if l := len(s); l < 1 || l > 5 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
