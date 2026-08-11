package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// GDELT queries the DOC 2.0 API for worldwide news coverage of a topic —
// keyless, no registration, updated every 15 minutes. Sentilyzer classifies
// the HEADLINES with its own models; per docs/corpus-policy.md only GDELT's
// own fields (title, metadata) are ever used — linked article text is never
// fetched, because that would reintroduce each publisher's copyright.
//
// GDELT enforces ~1 request per 5 seconds per IP and can take >3s to answer,
// so under load this connector rate-limits itself and will sometimes miss the
// fanout's per-connector budget — surfacing as a partial-result warning
// rather than an error. The topic cache absorbs most of that in practice.
type GDELT struct {
	HTTP    *http.Client
	BaseURL string // overridable for tests; defaults to https://api.gdeltproject.org
	// MinInterval spaces successive upstream calls (default 5s per GDELT's
	// limit). Tests set it to 0.
	MinInterval time.Duration

	mu   sync.Mutex
	next time.Time
}

func NewGDELT(client *http.Client) *GDELT {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &GDELT{
		HTTP:        client,
		BaseURL:     "https://api.gdeltproject.org",
		MinInterval: 5 * time.Second,
	}
}

func (*GDELT) ID() string              { return "gdelt" }
func (*GDELT) DisplayName() string     { return "GDELT (world news headlines)" }
func (*GDELT) Enabled() (bool, string) { return true, "" }

// Policy: the only surveyed source whose license affirmatively grants
// commercial use and redistribution (docs/corpus-policy.md, signed
// 2026-08-10). Durable applies to GDELT's OWN fields only — never fetched
// article bodies. Timespan queries reach back months, so it backfills.
func (*GDELT) Policy() Policy { return Policy{Durable: true, Backfillable: true} }

// throttle reserves the next upstream slot, waiting under ctx if the last
// call was too recent. GDELT 429s aggressively past 1 req/5s.
func (g *GDELT) throttle(ctx context.Context) error {
	g.mu.Lock()
	now := time.Now()
	wait := g.next.Sub(now)
	if wait < 0 {
		wait = 0
	}
	g.next = now.Add(wait + g.MinInterval)
	g.mu.Unlock()
	if wait == 0 {
		return nil
	}
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// gdeltLangs maps ISO 639-1 hints onto GDELT's sourcelang names.
var gdeltLangs = map[string]string{
	"en": "english", "es": "spanish", "fr": "french", "de": "german",
	"pt": "portuguese", "it": "italian", "nl": "dutch", "ru": "russian",
	"ja": "japanese", "zh": "chinese", "ar": "arabic", "ko": "korean",
}

type gdeltArtListResp struct {
	Articles []struct {
		URL      string `json:"url"`
		Title    string `json:"title"`
		SeenDate string `json:"seendate"` // 20060102T150405Z
		Domain   string `json:"domain"`
	} `json:"articles"`
}

func (g *GDELT) Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error) {
	if q.Topic == "" {
		return nil, nil
	}
	if err := g.throttle(ctx); err != nil {
		return nil, fmt.Errorf("gdelt: %w", err)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 250 {
		limit = 250
	}

	// Multi-word topics must be quoted or GDELT rejects the query with
	// "please connect terms with OR/AND".
	query := `"` + strings.ReplaceAll(q.Topic, `"`, ``) + `"`
	// A location naming a country (name or ISO alpha-2 code) becomes GDELT's
	// sourcecountry: operator with the FIPS 10-4 code GDELT expects; anything
	// else falls back to the quoted-phrase AND every other keyword connector
	// uses (location.go).
	if code, ok := countryFIPS(q.Location); ok {
		query += " sourcecountry:" + code
	} else {
		query = andLocation(query, q.Location)
	}
	if name, ok := gdeltLangs[strings.ToLower(q.Language)]; ok {
		query += " sourcelang:" + name
	}

	base := g.BaseURL
	if base == "" {
		base = "https://api.gdeltproject.org"
	}
	u, _ := url.Parse(base + "/api/v2/doc/doc")
	v := u.Query()
	v.Set("query", query)
	v.Set("mode", "ArtList")
	v.Set("format", "json")
	v.Set("maxrecords", strconv.Itoa(limit))
	if q.SinceSeconds > 0 {
		v.Set("timespan", gdeltTimespan(q.SinceSeconds))
	}
	u.RawQuery = v.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gdelt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gdelt: status %d", resp.StatusCode)
	}
	// GDELT reports some query errors as HTTP-200 plain text, so a decode
	// failure means "the API declined the query", not corrupt JSON.
	var body gdeltArtListResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("gdelt: unexpected response (query rejected?)")
	}

	out := make([]domain.SourcedDocument, 0, len(body.Articles))
	for _, a := range body.Articles {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			continue
		}
		posted, _ := time.Parse("20060102T150405Z", a.SeenDate)
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:   "gdelt-" + hashID(a.URL),
				Text: title,
			},
			Platform: g.ID(),
			Author:   a.Domain,
			URL:      a.URL,
			PostedAt: posted.UTC(),
		})
	}
	return out, nil
}

// gdeltTimespan renders a seconds-back window in GDELT's timespan syntax.
func gdeltTimespan(secs int64) string {
	switch {
	case secs < 2*60*60:
		m := (secs + 59) / 60
		if m < 15 {
			m = 15 // GDELT's minimum resolution
		}
		return fmt.Sprintf("%dmin", m)
	case secs < 72*60*60:
		return fmt.Sprintf("%dh", (secs+3599)/3600)
	default:
		return fmt.Sprintf("%dd", (secs+86399)/86400)
	}
}
