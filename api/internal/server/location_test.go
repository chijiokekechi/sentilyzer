package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/chijiokekechi/sentilyzer/api/gen/go/sentilyzer/v1"
	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
	"github.com/chijiokekechi/sentilyzer/api/internal/server"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// queryRecordingConnector records the final connectors.Query each Search
// receives, so transport tests can assert what actually reached the platform
// layer.
type queryRecordingConnector struct{ lastQuery connectors.Query }

func (*queryRecordingConnector) ID() string                { return "recorder" }
func (*queryRecordingConnector) DisplayName() string       { return "Recorder" }
func (*queryRecordingConnector) Enabled() (bool, string)   { return true, "" }
func (*queryRecordingConnector) Policy() connectors.Policy { return connectors.Policy{} }
func (c *queryRecordingConnector) Search(_ context.Context, q connectors.Query) ([]domain.SourcedDocument, error) {
	c.lastQuery = q
	return []domain.SourcedDocument{{
		Platform: "recorder",
		Document: domain.Document{ID: "1", Text: "this is great"},
	}}, nil
}

func newRecordingService(t *testing.T) (*service.Service, *queryRecordingConnector) {
	t.Helper()
	rec := &queryRecordingConnector{}
	reg := connectors.NewRegistry()
	reg.Register(rec)
	svc, err := service.New(inference.NewFakeClient(), reg, time.Minute)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc, rec
}

func TestREST_AnalyzeTopic_LocationParam(t *testing.T) {
	svc, rec := newRecordingService(t)
	srv := httptest.NewServer((&server.REST{Service: svc, Version: "test"}).Router())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/v1/analyze/topic?topic=robinhood&location=Austin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if rec.lastQuery.Location != "Austin" {
		t.Errorf("connector saw Location %q, want %q", rec.lastQuery.Location, "Austin")
	}
	if rec.lastQuery.Topic != "robinhood" {
		t.Errorf("connector saw Topic %q, want %q", rec.lastQuery.Topic, "robinhood")
	}
}

func TestREST_AnalyzeTopic_LocationBody(t *testing.T) {
	svc, rec := newRecordingService(t)
	srv := httptest.NewServer((&server.REST{Service: svc, Version: "test"}).Router())
	defer srv.Close()

	body := `{"topic":"robinhood","location":"Austin"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/analyze/topic", strings.NewReader(body))
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
	if rec.lastQuery.Location != "Austin" {
		t.Errorf("connector saw Location %q, want %q", rec.lastQuery.Location, "Austin")
	}
}

func TestGraphQL_AnalyzeTopic_LocationArg(t *testing.T) {
	svc, rec := newRecordingService(t)
	srv := httptest.NewServer((&server.GraphQL{Service: svc}).Handler())
	defer srv.Close()

	query := `{ analyzeTopic(topic: "robinhood", location: "Austin") { topic aggregate { sampleSize } } }`
	body, _ := json.Marshal(map[string]string{"query": query})
	resp, err := srv.Client().Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), `"errors"`) {
		t.Fatalf("graphql errors: %s", raw)
	}
	if rec.lastQuery.Location != "Austin" {
		t.Errorf("connector saw Location %q, want %q", rec.lastQuery.Location, "Austin")
	}
}

func TestGRPC_AnalyzeTopic_LocationField(t *testing.T) {
	svc, rec := newRecordingService(t)
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

	if _, err := client.AnalyzeTopic(context.Background(), &pb.AnalyzeTopicRequest{
		Topic:    "robinhood",
		Location: "Austin",
	}); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if rec.lastQuery.Location != "Austin" {
		t.Errorf("connector saw Location %q, want %q", rec.lastQuery.Location, "Austin")
	}
}
