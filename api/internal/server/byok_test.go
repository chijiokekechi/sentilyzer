package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
	"github.com/chijiokekechi/sentilyzer/api/internal/server"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"google.golang.org/grpc/metadata"
)

// headerKeyedConnector unlocks on a caller YouTube key and echoes documents
// only when a key was supplied — so the response proves whether the header
// made it all the way through transport → context → service → connector.
type headerKeyedConnector struct{ lastKey string }

func (c *headerKeyedConnector) ID() string              { return "keyed" }
func (c *headerKeyedConnector) DisplayName() string     { return "Keyed" }
func (c *headerKeyedConnector) Enabled() (bool, string) { return false, "missing key" }
func (c *headerKeyedConnector) EnabledWith(cr connectors.Credentials) (bool, string) {
	if cr.YouTubeAPIKey != "" {
		return true, ""
	}
	return c.Enabled()
}

func (c *headerKeyedConnector) Search(_ context.Context, q connectors.Query) ([]domain.SourcedDocument, error) {
	c.lastKey = q.Creds.YouTubeAPIKey
	return []domain.SourcedDocument{{
		Platform: "keyed",
		Document: domain.Document{ID: "k1", Text: "wonderful and amazing"},
	}}, nil
}

func newByokREST(t *testing.T) (http.Handler, *headerKeyedConnector) {
	t.Helper()
	keyed := &headerKeyedConnector{}
	reg := connectors.NewRegistry()
	reg.Register(connectors.Mock{})
	reg.Register(keyed)
	svc, err := service.New(inference.NewFakeClient(), reg, time.Minute)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	rest := &server.REST{Service: svc, Version: "test"}
	return rest.Router(), keyed
}

func TestBYOK_HeaderReachesConnector(t *testing.T) {
	router, keyed := newByokREST(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	// Without the header the keyed platform must not appear.
	resp, err := http.Get(srv.URL + "/v1/analyze/topic?topic=x&platforms=mock,keyed")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		ByPlatform map[string]any `json:"by_platform"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, ok := body.ByPlatform["keyed"]; ok {
		t.Fatal("keyed platform served without the credential header")
	}

	// With the header it joins, and the connector received the exact key.
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/v1/analyze/topic?topic=x&platforms=mock,keyed", nil)
	req.Header.Set(server.HeaderYouTubeAPIKey, "yt-key-123")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body2 struct {
		ByPlatform map[string]any `json:"by_platform"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&body2); err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if _, ok := body2.ByPlatform["keyed"]; !ok {
		t.Fatalf("keyed platform missing despite header; by_platform=%v", body2.ByPlatform)
	}
	if keyed.lastKey != "yt-key-123" {
		t.Fatalf("connector saw key %q, want yt-key-123", keyed.lastKey)
	}
}

func TestBYOK_CredsNeverEchoedInResponse(t *testing.T) {
	router, _ := newByokREST(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/v1/analyze/topic?topic=x&platforms=mock,keyed", nil)
	const secret = "yt-super-secret-key-456"
	req.Header.Set(server.HeaderYouTubeAPIKey, secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := readAll(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, buf)
	}
	// The credential must never appear anywhere in a response body.
	if contains := (len(buf) > 0 && (stringContains(buf, secret))); contains {
		t.Fatal("response body contains the caller's API key")
	}
}

func stringContains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestBYOK_GRPCMetadataExtraction(t *testing.T) {
	md := metadata.New(map[string]string{
		// gRPC clients send lowercase keys; metadata.New also lowercases.
		"x-connector-youtube-api-key":      "yt-1",
		"x-connector-reddit-client-id":     "rid",
		"x-connector-reddit-client-secret": "rsec",
	})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	creds := server.CredsFromGRPCForTest(ctx)
	if creds.YouTubeAPIKey != "yt-1" || creds.RedditClientID != "rid" || creds.RedditClientSecret != "rsec" {
		t.Fatalf("extracted %+v", creds)
	}
	if creds.TwitterBearerToken != "" {
		t.Fatal("absent metadata must extract as empty")
	}

	// No metadata at all: zero value.
	if got := server.CredsFromGRPCForTest(context.Background()); !got.IsZero() {
		t.Fatalf("no metadata should yield zero creds, got %+v", got)
	}
}
