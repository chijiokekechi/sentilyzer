package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/chijiokekechi/sentilyzer/api/gen/go/sentilyzer/v1"
	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
	"github.com/chijiokekechi/sentilyzer/api/internal/server"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// newTestServer returns a service backed by FakeClient + Mock connector.
func newTestService(t *testing.T) *service.Service {
	t.Helper()
	reg := connectors.NewRegistry()
	reg.Register(connectors.Mock{})
	svc, err := service.New(inference.NewFakeClient(), reg, time.Minute)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc
}

// --- REST/JSON ---------------------------------------------------------------

func TestREST_AnalyzeText_JSON(t *testing.T) {
	rest := &server.REST{Service: newTestService(t), Version: "test"}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()

	body := `{"documents":[{"id":"1","text":"I love this app, it is amazing"}]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/analyze/text", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var payload struct {
		Results []struct {
			Overall struct {
				Label    string  `json:"label"`
				Polarity float32 `json:"polarity"`
			} `json:"overall"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Results) != 1 || payload.Results[0].Overall.Label != "positive" {
		t.Errorf("unexpected payload: %+v", payload)
	}
}

// --- REST/XML ----------------------------------------------------------------

func TestREST_AnalyzeText_XML(t *testing.T) {
	rest := &server.REST{Service: newTestService(t), Version: "test"}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()

	body := bytes.NewBufferString(
		`<analyze_text><documents><document><text>This is terrible, awful experience</text></document></documents></analyze_text>`,
	)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/analyze/text", body)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Accept", "application/xml")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("Content-Type = %q", ct)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "<label>negative</label>") {
		t.Errorf("expected <label>negative</label> in: %s", raw)
	}
	// Round-trip parse.
	var doc struct {
		XMLName xml.Name `xml:"analyze_text_response"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("xml unmarshal: %v", err)
	}
}

// --- REST/JSON: GET /v1/analyze/topic ---------------------------------------

func TestREST_AnalyzeTopic_GET(t *testing.T) {
	rest := &server.REST{Service: newTestService(t), Version: "test"}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/v1/analyze/topic?topic=Robinhood&platforms=mock&limit=3")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var payload struct {
		Topic   string `json:"topic"`
		Results []any  `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Topic != "Robinhood" || len(payload.Results) == 0 {
		t.Errorf("unexpected topic payload: %+v", payload)
	}
}

// --- gRPC --------------------------------------------------------------------

func TestGRPC_AnalyzeText(t *testing.T) {
	svc := newTestService(t)
	gSrv := grpc.NewServer()
	(&server.GRPC{Service: svc, Version: "test"}).Register(gSrv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go gSrv.Serve(lis)
	defer gSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := pb.NewSentilyzerServiceClient(conn)

	resp, err := client.AnalyzeText(context.Background(), &pb.AnalyzeTextRequest{
		Documents: []*pb.Document{{Id: "1", Text: "amazing experience, loved it"}},
	})
	if err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if got := resp.Results[0].Overall.Label; got != pb.Sentiment_SENTIMENT_POSITIVE {
		t.Errorf("label = %v, want POSITIVE", got)
	}
}

// --- GraphQL -----------------------------------------------------------------

func TestGraphQL_PlatformsAndAnalyze(t *testing.T) {
	gql := &server.GraphQL{Service: newTestService(t)}
	srv := httptest.NewServer(gql.Handler())
	defer srv.Close()

	for _, tc := range []struct {
		name  string
		query string
		// wants is a list of substrings; match is whitespace-insensitive
		// so the test does not break on pretty-printed responses.
		wants []string
	}{
		{
			name:  "platforms",
			query: `{ platforms { id displayName enabled } }`,
			wants: []string{`"id":"mock"`, `"enabled":true`},
		},
		{
			name: "analyzeText",
			query: `{ analyzeText(documents: [{ text: "amazing love love love" }]) {
				aggregate { modalLabel sampleSize }
			} }`,
			wants: []string{`"modalLabel":"positive"`, `"sampleSize":1`},
		},
		{
			name: "analyzeTopic",
			query: `{ analyzeTopic(topic: "Robinhood", platforms: ["mock"], limitPerPlatform: 3) {
				topic aggregate { sampleSize }
			} }`,
			wants: []string{`"topic":"Robinhood"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"query": tc.query})
			resp, err := srv.Client().Post(srv.URL, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, raw)
			}
			compact := stripWS(string(raw))
			for _, want := range tc.wants {
				if !strings.Contains(compact, want) {
					t.Errorf("response missing %q (compacted=%s)", want, compact)
				}
			}
		})
	}
}

// stripWS removes whitespace outside of JSON string literals so substring
// matches survive pretty-printed bodies.
func stripWS(s string) string {
	var b strings.Builder
	inStr := false
	prev := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' && prev != '\\' {
			inStr = !inStr
		}
		if !inStr && (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
			prev = c
			continue
		}
		b.WriteByte(c)
		prev = c
	}
	return b.String()
}
