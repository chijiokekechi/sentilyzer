package server_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// exportTextBody has one clearly positive and one clearly negative document,
// so tests can assert row content, not just shape.
const exportTextBody = `{"documents":[{"id":"1","text":"I love this, it is amazing"},{"id":"2","text":"terrible and broken"}]}`

func postAnalyzeText(t *testing.T, srv *httptest.Server, accept, format string) *http.Response {
	t.Helper()
	url := srv.URL + "/v1/analyze/text"
	if format != "" {
		url += "?format=" + format
	}
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(exportTextBody))
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// ndjsonLines parses a body as NDJSON, one object per line, failing the test
// on any line that is not a standalone JSON object.
func ndjsonLines(t *testing.T, body string) []map[string]any {
	t.Helper()
	raw := strings.Split(strings.TrimRight(body, "\n"), "\n")
	out := make([]map[string]any, 0, len(raw))
	for i, line := range raw {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\n%s", i, err, line)
		}
		out = append(out, obj)
	}
	return out
}

func TestExport_NegotiationAndContentType(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	cases := []struct {
		name   string
		accept string
		format string
		wantCT string
	}{
		{"accept csv", "text/csv", "", "text/csv; charset=utf-8"},
		{"format csv", "", "csv", "text/csv; charset=utf-8"},
		{"accept ndjson", "application/x-ndjson", "", "application/x-ndjson"},
		{"format ndjson", "", "ndjson", "application/x-ndjson"},
		// Equal q keeps JSON: the default wins ties.
		{"csv tie keeps json", "text/csv, application/json", "", "application/json; charset=utf-8"},
		{"ndjson tie keeps json", "application/x-ndjson, application/json", "", "application/json; charset=utf-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postAnalyzeText(t, srv, tc.accept, tc.format)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, readAll(t, resp.Body))
			}
			if ct := resp.Header.Get("Content-Type"); ct != tc.wantCT {
				t.Errorf("Content-Type = %q, want %q", ct, tc.wantCT)
			}
		})
	}

	// The topic GET form negotiates the same way.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/analyze/topic?topic=Robinhood&platforms=mock&limit=2", nil)
	req.Header.Set("Accept", "text/csv")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET topic csv status %d: %s", resp.StatusCode, readAll(t, resp.Body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("GET topic Content-Type = %q, want text/csv", ct)
	}
}

func TestExport_TextCSV(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	resp := postAnalyzeText(t, srv, "text/csv", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readAll(t, resp.Body))
	}
	records, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	wantHeader := []string{"index", "label", "confidence", "polarity", "p_negative", "p_neutral", "p_positive"}
	if len(records) != 3 {
		t.Fatalf("got %d records, want header + 2 rows", len(records))
	}
	if got := strings.Join(records[0], ","); got != strings.Join(wantHeader, ",") {
		t.Errorf("header = %v, want %v", records[0], wantHeader)
	}
	// One row per document, in request order, index from position.
	if records[1][0] != "0" || records[1][1] != "positive" {
		t.Errorf("row 1 = %v, want index 0 / positive", records[1])
	}
	if records[2][0] != "1" || records[2][1] != "negative" {
		t.Errorf("row 2 = %v, want index 1 / negative", records[2])
	}
	// The probability columns must be numeric and sum to ~1.
	var sum float64
	for _, col := range records[1][4:] {
		f, err := strconv.ParseFloat(col, 64)
		if err != nil {
			t.Fatalf("probability %q is not numeric: %v", col, err)
		}
		sum += f
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("probabilities sum to %v, want ~1", sum)
	}
}

func TestExport_TopicCSVEscapesText(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	// The mock connector embeds the topic verbatim in each document's text, so
	// a topic carrying CSV's three special characters lands in the text column.
	topic := "a,\"b\"\nc"
	body, _ := json.Marshal(map[string]any{
		"topic": topic, "platforms": []string{"mock"}, "limit_per_platform": 1,
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/analyze/topic", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/csv")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readAll(t, resp.Body))
	}
	raw := readAll(t, resp.Body)

	// Escaping visibly happened on the wire: an embedded quote is doubled.
	if !strings.Contains(raw, `""b""`) {
		t.Errorf("raw CSV does not double the embedded quote:\n%s", raw)
	}
	// And the field round-trips intact through a conforming reader.
	records, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want header + 1 row", len(records))
	}
	wantHeader := "platform,doc_id,created_at,label,confidence,polarity,p_negative,p_neutral,p_positive,text"
	if got := strings.Join(records[0], ","); got != wantHeader {
		t.Errorf("header = %v, want %s", records[0], wantHeader)
	}
	row := records[1]
	if row[0] != "mock" || row[1] != "mock-0" {
		t.Errorf("platform/doc_id = %q/%q, want mock/mock-0", row[0], row[1])
	}
	if _, err := time.Parse(time.RFC3339, row[2]); err != nil {
		t.Errorf("created_at %q is not RFC 3339: %v", row[2], err)
	}
	if !strings.Contains(row[9], topic) {
		t.Errorf("text column lost the topic: %q", row[9])
	}
}

func TestExport_TextNDJSON(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	resp := postAnalyzeText(t, srv, "application/x-ndjson", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readAll(t, resp.Body))
	}
	lines := ndjsonLines(t, readAll(t, resp.Body))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one per document and no aggregate", len(lines))
	}
	wantLabels := []string{"positive", "negative"}
	for i, obj := range lines {
		if _, ok := obj["aggregate"]; ok {
			t.Errorf("line %d carries an aggregate; analyze/text emits documents only", i)
		}
		overall, ok := obj["overall"].(map[string]any)
		if !ok {
			t.Fatalf("line %d is not a document result: %v", i, obj)
		}
		if overall["label"] != wantLabels[i] {
			t.Errorf("line %d label = %v, want %s", i, overall["label"], wantLabels[i])
		}
	}
}

func TestExport_TopicNDJSONEndsWithAggregate(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/analyze/topic?topic=Robinhood&platforms=mock&limit=3&format=ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readAll(t, resp.Body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}
	lines := ndjsonLines(t, readAll(t, resp.Body))
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 3 documents + 1 aggregate", len(lines))
	}
	for i, obj := range lines[:3] {
		if _, ok := obj["document"]; !ok {
			t.Errorf("line %d has no document: %v", i, obj)
		}
		if _, ok := obj["result"]; !ok {
			t.Errorf("line %d has no result: %v", i, obj)
		}
	}
	last := lines[3]
	agg, ok := last["aggregate"].(map[string]any)
	if !ok || len(last) != 1 {
		t.Fatalf("final line is not the bare aggregate: %v", last)
	}
	if agg["sample_size"] != float64(3) {
		t.Errorf("aggregate sample_size = %v, want 3", agg["sample_size"])
	}
}

// TestExport_UnsupportedCombosUnchanged: the export formats exist only on the
// analyze endpoints, and only for responses; everything else keeps its 406/400.
func TestExport_UnsupportedCombosUnchanged(t *testing.T) {
	srv := httptest.NewServer(newTestREST(t))
	defer srv.Close()

	t.Run("csv accept on health is still 406", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
		req.Header.Set("Accept", "text/csv")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotAcceptable {
			t.Fatalf("status = %d, want 406", resp.StatusCode)
		}
	})

	t.Run("csv format on health is still 400", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/health?format=csv")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		if body := readAll(t, resp.Body); !strings.Contains(body, "unsupported ?format=csv") {
			t.Errorf("body %q lacks the unsupported-format message", body)
		}
	})

	t.Run("unsatisfiable accept on analyze is still 406", func(t *testing.T) {
		resp := postAnalyzeText(t, srv, "text/plain", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotAcceptable {
			t.Fatalf("status = %d, want 406", resp.StatusCode)
		}
	})

	t.Run("csv errors are json", func(t *testing.T) {
		// CSV has no error shape, so a failing CSV request reports as JSON.
		resp, err := http.Get(srv.URL + "/v1/analyze/topic?format=csv")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for missing topic", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
	})

	t.Run("ndjson errors are one json line", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/v1/analyze/topic?format=ndjson")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for missing topic", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
			t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
		}
		lines := ndjsonLines(t, readAll(t, resp.Body))
		if len(lines) != 1 || lines[0]["status"] != float64(400) {
			t.Errorf("error body = %v, want a single line with status 400", lines)
		}
	})
}
