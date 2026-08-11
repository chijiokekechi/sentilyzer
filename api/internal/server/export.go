package server

// CSV and NDJSON encoders for the analyze endpoints. Unlike the tree formats
// in rest.go these are row-per-document and per-endpoint (the column sets
// differ), so the analyze handlers dispatch here instead of writePayload.
// Both are documented-lossy like XML: CSV omits aggregates, aspects, and
// metadata; NDJSON omits the topic breakdowns and warnings. The column sets
// are contract, documented in README "Response formats".

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

var (
	textCSVHeader = []string{
		"index", "label", "confidence", "polarity",
		"p_negative", "p_neutral", "p_positive",
	}
	topicCSVHeader = []string{
		"platform", "doc_id", "created_at", "label", "confidence", "polarity",
		"p_negative", "p_neutral", "p_positive", "text",
	}
)

// csvFloat renders a float32 with the shortest representation that
// round-trips at 32-bit precision.
func csvFloat(f float32) string {
	return strconv.FormatFloat(float64(f), 'g', -1, 32)
}

// scoreColumns renders the label/confidence/polarity/p_* columns both CSV
// shapes share. Probabilities project onto fixed columns in canonical label
// order; a label the model never emitted renders as 0.
func scoreColumns(s domain.Score) []string {
	cols := []string{string(s.Label), csvFloat(s.Confidence), csvFloat(s.Polarity)}
	for _, label := range domain.AllSentiments {
		cols = append(cols, csvFloat(s.Probabilities[string(label)]))
	}
	return cols
}

// writeCSV emits the header row (always, so an empty result set is still a
// parseable table) and one row per document. encoding/csv quotes and escapes
// fields, so free text with commas, quotes, and newlines survives intact.
func writeCSV(w http.ResponseWriter, header []string, rows [][]string) {
	w.Header().Set("Content-Type", contentType(mimeCSV))
	w.WriteHeader(http.StatusOK)
	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	for _, row := range rows {
		_ = cw.Write(row)
	}
	cw.Flush()
}

// writeTextCSV renders analyze/text results. index is the document's position
// in the request, so rows stay attributable even when the caller sent no ids.
func writeTextCSV(w http.ResponseWriter, results []domain.DocumentResult) {
	rows := make([][]string, 0, len(results))
	for i, r := range results {
		rows = append(rows, append([]string{strconv.Itoa(i)}, scoreColumns(r.Overall)...))
	}
	writeCSV(w, textCSVHeader, rows)
}

// writeTopicCSV renders analyze/topic results. created_at is the platform's
// post timestamp in RFC 3339 UTC, blank when the connector reported none.
func writeTopicCSV(w http.ResponseWriter, a *domain.SourcedAnalysis) {
	rows := make([][]string, 0, len(a.Results))
	for _, r := range a.Results {
		doc := r.Document
		created := ""
		if !doc.PostedAt.IsZero() {
			created = doc.PostedAt.UTC().Format(time.RFC3339)
		}
		row := append([]string{doc.Platform, doc.Document.ID, created}, scoreColumns(r.Result.Overall)...)
		rows = append(rows, append(row, doc.Document.Text))
	}
	writeCSV(w, topicCSVHeader, rows)
}

// writeNDJSON streams one compact JSON object per line. The objects are the
// same ones the JSON response carries, so a line-by-line consumer needs no
// second schema. Encode terminates each object with the newline itself.
func writeNDJSON(w http.ResponseWriter, lines []any) {
	w.Header().Set("Content-Type", contentType(mimeNDJSON))
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for _, line := range lines {
		_ = enc.Encode(line)
	}
}

func writeTextNDJSON(w http.ResponseWriter, results []domain.DocumentResult) {
	lines := make([]any, len(results))
	for i, r := range results {
		lines[i] = r
	}
	writeNDJSON(w, lines)
}

// writeTopicNDJSON emits each sourced result, then one final aggregate line.
// The trailing position and the "aggregate" key are how a streaming consumer
// tells the summary from a document.
func writeTopicNDJSON(w http.ResponseWriter, a *domain.SourcedAnalysis) {
	lines := make([]any, 0, len(a.Results)+1)
	for _, r := range a.Results {
		lines = append(lines, r)
	}
	lines = append(lines, struct {
		Aggregate domain.Aggregate `json:"aggregate"`
	}{a.Aggregate})
	writeNDJSON(w, lines)
}
