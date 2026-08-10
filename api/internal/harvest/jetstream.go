// Package harvest collects training-corpus documents from sources the corpus
// policy (docs/corpus-policy.md) admits. It is deliberately separate from the
// serving connectors: serving answers one request and forgets; harvesting
// writes durable rows, so it runs only for Policy.Durable sources and carries
// the provenance the policy demands.
package harvest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

// DefaultJetstreamURL is Bluesky's sanctioned bulk interface. The corpus
// policy's condition (1) requires bulk harvesting to use the firehose, not
// the search endpoints.
const DefaultJetstreamURL = "wss://jetstream2.us-east.bsky.network/subscribe"

const postCollection = "app.bsky.feed.post"

// Event is one Jetstream message. Shapes verified against the live stream
// 2026-08-10.
type Event struct {
	DID    string `json:"did"`
	TimeUS int64  `json:"time_us"`
	Kind   string `json:"kind"` // "commit" | "identity" | "account"
	Commit *struct {
		Operation  string `json:"operation"` // "create" | "update" | "delete"
		Collection string `json:"collection"`
		RKey       string `json:"rkey"`
		Record     *struct {
			Text      string   `json:"text"`
			Langs     []string `json:"langs"`
			CreatedAt string   `json:"createdAt"`
		} `json:"record"`
	} `json:"commit"`
	Account *struct {
		Active bool   `json:"active"`
		DID    string `json:"did"`
	} `json:"account"`
}

// JetstreamClient reads post events from the firehose with cursor resume.
type JetstreamClient struct {
	// URL is the subscribe endpoint; DefaultJetstreamURL if empty.
	URL string
}

// subscribeURL renders the endpoint with the post-collection filter and, when
// resuming, a cursor rewound slightly so a crash never skips events (the
// prep step dedupes, so overlap is safe where a gap is not).
func (c *JetstreamClient) subscribeURL(cursorUS int64) (string, error) {
	base := c.URL
	if base == "" {
		base = DefaultJetstreamURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("jetstream url: %w", err)
	}
	v := u.Query()
	v.Set("wantedCollections", postCollection)
	if cursorUS > 0 {
		rewound := max(cursorUS-(5*time.Second).Microseconds(), 0)
		v.Set("cursor", strconv.FormatInt(rewound, 10))
	}
	u.RawQuery = v.Encode()
	return u.String(), nil
}

// Stream connects (resuming from cursorUS when > 0) and delivers each parsed
// event to handle until ctx ends or the connection drops. It returns the last
// event's time_us for cursor persistence, alongside the terminating error.
func (c *JetstreamClient) Stream(
	ctx context.Context,
	cursorUS int64,
	handle func(Event) error,
) (int64, error) {
	target, err := c.subscribeURL(cursorUS)
	if err != nil {
		return cursorUS, err
	}
	conn, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		return cursorUS, fmt.Errorf("jetstream dial: %w", err)
	}
	defer conn.CloseNow()
	// Posts are small but embeds/facets can pad a message well past the
	// 32 KiB default read limit.
	conn.SetReadLimit(1 << 20)

	last := cursorUS
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return last, fmt.Errorf("jetstream read: %w", err)
		}
		var ev Event
		if err := json.Unmarshal(data, &ev); err != nil {
			// One malformed frame is not a reason to drop the stream.
			continue
		}
		if ev.TimeUS > 0 {
			last = ev.TimeUS
		}
		if err := handle(ev); err != nil {
			return last, err
		}
	}
}
