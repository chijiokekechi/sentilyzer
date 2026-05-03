package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// Mock returns deterministic fake posts. Useful for offline demos, CI, and
// for first-run UX before real API keys are configured.
type Mock struct{}

func (Mock) ID() string                   { return "mock" }
func (Mock) DisplayName() string          { return "Mock (synthetic)" }
func (Mock) Enabled() (bool, string)      { return true, "" }

var mockTemplates = []string{
	"I really love %s — best decision I made this year, can't recommend it enough.",
	"Honestly %s has been disappointing lately. Slow, buggy, and the support is bad.",
	"My take on %s: it's fine. Nothing groundbreaking but it gets the job done.",
	"%s is amazing! The new features are fantastic and the team keeps shipping.",
	"Tried %s for a week, it broke twice and I lost work. Would not recommend.",
	"%s works as advertised. No complaints, no surprises.",
	"%s changed my workflow completely — productivity through the roof, fantastic.",
	"Awful experience with %s, the latest update is a disaster.",
}

func (Mock) Search(_ context.Context, q Query) ([]domain.SourcedDocument, error) {
	if strings.TrimSpace(q.Topic) == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 || limit > len(mockTemplates) {
		limit = len(mockTemplates)
	}
	// Use a deterministic seed from the topic so demos look stable.
	sum := sha256.Sum256([]byte(q.Topic))
	seed := binary.LittleEndian.Uint32(sum[:4])
	now := time.Now().UTC()
	out := make([]domain.SourcedDocument, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (int(seed) + i) % len(mockTemplates)
		text := fmt.Sprintf(mockTemplates[idx], q.Topic)
		out = append(out, domain.SourcedDocument{
			Document: domain.Document{
				ID:   fmt.Sprintf("mock-%d", i),
				Text: text,
			},
			Platform: "mock",
			Author:   fmt.Sprintf("user_%d", (int(seed)+i)%100),
			URL:      fmt.Sprintf("https://example.com/mock/%d", i),
			PostedAt: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	return out, nil
}
