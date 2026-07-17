package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/server"
	"sigs.k8s.io/yaml"
)

// newTestREST wires the real router over the fake inference client and mock
// connector, so these tests exercise middleware + negotiation for real.
//
// The clock is pinned: comparing two encodings means issuing two requests, and
// a live clock makes analyzed_at differ between them for reasons that have
// nothing to do with the encoding under test.
func newTestREST(t *testing.T) http.Handler {
	t.Helper()
	svc := newTestService(t)
	fixed := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return fixed }
	rest := &server.REST{Service: svc, Version: "test"}
	return rest.Router()
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFormats_YAMLResponseMatchesJSONExactly(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	body := `{"documents":[{"id":"1","text":"I love this, it is amazing"},{"id":"2","text":"terrible and broken"}]}`

	fetch := func(t *testing.T, accept, format string) []byte {
		t.Helper()
		url := srv.URL + "/v1/analyze/text"
		if format != "" {
			url += "?format=" + format
		}
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if got := resp.Header.Get("Vary"); !strings.Contains(got, "Accept") {
			t.Errorf("Vary = %q, want it to contain Accept", got)
		}
		return []byte(readAll(t, resp.Body))
	}

	jsonBody := fetch(t, "application/json", "")
	yamlBody := fetch(t, "application/yaml", "")

	// The whole promise of "same data, many formats" is that a client can
	// switch encodings and get the same object back. Assert it structurally
	// rather than trusting that the tags happen to line up.
	var fromJSON, fromYAML any
	if err := json.Unmarshal(jsonBody, &fromJSON); err != nil {
		t.Fatalf("json: %v\n%s", err, jsonBody)
	}
	if err := yaml.Unmarshal(yamlBody, &fromYAML); err != nil {
		t.Fatalf("yaml: %v\n%s", err, yamlBody)
	}
	a, _ := json.Marshal(fromJSON)
	b, _ := json.Marshal(fromYAML)
	if string(a) != string(b) {
		t.Fatalf("YAML and JSON describe different objects:\n json: %s\n yaml: %s", a, b)
	}

	// ?format=yaml must agree with Accept: application/yaml.
	if q := fetch(t, "", "yaml"); string(q) != string(yamlBody) {
		t.Errorf("?format=yaml and Accept disagree:\n%s\n---\n%s", q, yamlBody)
	}
}

func TestFormats_ContentTypeAndStatus(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	cases := []struct {
		name        string
		url         string
		accept      string
		wantStatus  int
		wantCTPart  string
		wantBodyHas string
	}{
		{"json default", "/health", "", 200, "application/json", `"healthy"`},
		{"explicit yaml", "/health", "application/yaml", 200, "application/yaml", "healthy:"},
		{"explicit xml", "/health", "application/xml", 200, "application/xml", "<healthy>"},
		{"format overrides accept", "/health?format=yaml", "application/json", 200, "application/yaml", "healthy:"},
		{"browser gets json not xml", "/health", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", 200, "application/json", `"healthy"`},
		{"unacceptable is 406", "/health", "text/plain", 406, "application/json", "Accept"},
		{"bad format is 400", "/health?format=toml", "", 400, "application/json", "unsupported ?format=toml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+tc.url, nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, tc.wantCTPart) {
				t.Errorf("Content-Type = %q, want it to contain %q", ct, tc.wantCTPart)
			}
			if body := readAll(t, resp.Body); !strings.Contains(body, tc.wantBodyHas) {
				t.Errorf("body %q lacks %q", body, tc.wantBodyHas)
			}
		})
	}
}

// TestFormats_YAMLRequestBodyRejected: YAML output does not imply YAML input.
// YAML 1.1 implicit typing silently turns `topic: no` into the string
// "false", with no error, so the API refuses to parse it at all.
func TestFormats_YAMLRequestBodyRejected(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/analyze/topic",
		strings.NewReader("topic: no\n"))
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", resp.StatusCode)
	}
	body := readAll(t, resp.Body)
	// The error has to point at the way that does work, or it is just a wall.
	for _, want := range []string{"application/json", "format=yaml"} {
		if !strings.Contains(body, want) {
			t.Errorf("415 body should mention %q, got: %s", want, body)
		}
	}
}

// TestFormats_ErrorStatusesAreDistinct: every AnalyzeTopic failure used to be
// a 502. A caller could not tell their own mistake from a dead upstream.
func TestFormats_ErrorStatusesAreDistinct(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"missing topic is the caller's fault", "/v1/analyze/topic", http.StatusBadRequest},
		{"valid topic succeeds", "/v1/analyze/topic?topic=hello", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.url)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d (%s)", resp.StatusCode, tc.want, readAll(t, resp.Body))
			}
		})
	}
}

// TestFormats_ErrorsHonorNegotiation: an error body in the format the client
// asked for, not always JSON.
func TestFormats_ErrorsHonorNegotiation(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/analyze/topic", nil)
	req.Header.Set("Accept", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/yaml") {
		t.Fatalf("error Content-Type = %q, want yaml", ct)
	}
	body := readAll(t, resp.Body)
	var out map[string]any
	if err := yaml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("error body is not valid yaml: %v\n%s", err, body)
	}
	if out["status"] != float64(400) && out["status"] != 400 {
		t.Errorf("yaml error body status = %v, want 400", out["status"])
	}
}
