// Package connectors provides a pluggable interface for harvesting public
// posts about a topic from external platforms.
//
// Concrete connectors implement Connector and self-register at init() via
// Register. The service layer iterates Registry to fan out a Search across
// every enabled platform.
//
// New platforms drop in by creating a new file with init() { Register(...) }.
package connectors

import (
	"context"
	"sort"
	"sync"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// Query is what the service layer asks of every connector.
type Query struct {
	Topic        string
	Limit        int
	Language     string
	SinceSeconds int64
	// Creds are the caller's own API keys, if any. A CredentialedConnector
	// prefers these over its server-side configuration for the one request;
	// keyless connectors ignore them.
	Creds Credentials
}

// Policy declares what may be done with a platform's content beyond serving
// one request. The authoritative table lives in docs/corpus-policy.md; the
// values here must mirror it, and a Durable flag flips only when that
// document changes first.
//
// This is a METHOD ON THE INTERFACE rather than a config flag or a
// WHERE-clause so that adding a connector forces the eligibility decision at
// compile time — a flag rots the first time someone wires a platform in at
// 3am without re-reading the audit.
type Policy struct {
	// Durable reports whether content from this platform — or anything
	// derived from it — may be written to durable storage (the training
	// corpus, persisted aggregates). False does NOT disable the connector:
	// the interactive serving path persists nothing beyond the 10-minute
	// in-memory cache, which every source tolerates.
	Durable bool
	// Backfillable reports whether Search (or the platform's bulk interface)
	// can target a historical window. A non-backfillable source that misses a
	// harvest window has lost that data permanently.
	Backfillable bool
	// Reason explains why Durable is false. Empty when Durable is true. It
	// exists so the decision gets re-argued on its merits rather than
	// quietly flipped.
	Reason string
}

// Connector is the contract every platform implements.
type Connector interface {
	ID() string
	DisplayName() string
	// Enabled reports whether the connector has the credentials/config it
	// needs to run a real search. A disabled connector still appears in
	// ListPlatforms with a reason; it simply returns no results.
	Enabled() (bool, string)
	// Policy declares the platform's durable-storage eligibility per
	// docs/corpus-policy.md.
	Policy() Policy
	Search(ctx context.Context, q Query) ([]domain.SourcedDocument, error)
}

// Registry is the global set of connectors.
type Registry struct {
	mu         sync.RWMutex
	connectors map[string]Connector
}

func NewRegistry() *Registry {
	return &Registry{connectors: map[string]Connector{}}
}

func (r *Registry) Register(c Connector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectors[c.ID()] = c
}

// Get returns a connector by ID, or nil if none registered.
func (r *Registry) Get(id string) Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connectors[id]
}

// List returns connectors sorted by ID for stable presentation.
func (r *Registry) List() []Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Connector, 0, len(r.connectors))
	for _, c := range r.connectors {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Info returns PlatformInfo for every registered connector.
func (r *Registry) Info() []domain.PlatformInfo {
	infos := make([]domain.PlatformInfo, 0)
	for _, c := range r.List() {
		ok, reason := c.Enabled()
		infos = append(infos, domain.PlatformInfo{
			ID:             c.ID(),
			DisplayName:    c.DisplayName(),
			Enabled:        ok,
			DisabledReason: reason,
		})
	}
	return infos
}
