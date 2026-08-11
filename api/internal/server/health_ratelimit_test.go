package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
	"github.com/chijiokekechi/sentilyzer/api/internal/server"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
)

// mlDownService returns a Service whose inference client is unreachable, so
// Health reports ml_reachable=false — the state /readyz must reflect and
// /livez must ignore.
func mlDownService(t *testing.T) *service.Service {
	t.Helper()
	reg := connectors.NewRegistry()
	reg.Register(connectors.Mock{})
	// Dial an address nothing listens on; Ready() will fail, so Health reports
	// ml_reachable=false without blocking (grpc.NewClient is lazy).
	inf, err := inference.Dial("127.0.0.1:59991")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	svc, err := service.New(inf, reg, time.Minute)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc
}

func TestLivez_IsUpEvenWhenMLIsDown(t *testing.T) {
	rest := &server.REST{Service: mlDownService(t), Version: "test"}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()

	// Liveness must be 200 regardless of ML: restarting the gateway can't fix
	// an ML outage, so /livez failing would crash-loop a healthy gateway.
	resp, err := http.Get(srv.URL + "/livez")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/livez = %d, want 200 even with ML down", resp.StatusCode)
	}
}

func TestReadyz_ReflectsMLReachability(t *testing.T) {
	// ML down -> 503.
	down := &server.REST{Service: mlDownService(t), Version: "test"}
	dsrv := httptest.NewServer(down.Router())
	defer dsrv.Close()
	resp, err := http.Get(dsrv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with ML down = %d, want 503", resp.StatusCode)
	}

	// ML reachable (FakeClient) -> 200.
	up := &server.REST{Service: newTestService(t), Version: "test"}
	usrv := httptest.NewServer(up.Router())
	defer usrv.Close()
	resp2, err := http.Get(usrv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/readyz with ML reachable = %d, want 200", resp2.StatusCode)
	}
}

func TestLivez_BypassesContentNegotiation(t *testing.T) {
	// A container healthcheck sends no Accept header; /livez must still be 200,
	// not a 406 from negotiation. And an Accept the API can't satisfy on a
	// normal route (406) must NOT affect /livez.
	rest := &server.REST{Service: newTestService(t), Version: "test"}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/livez", nil)
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/livez with unsatisfiable Accept = %d, want 200", resp.StatusCode)
	}
}

func TestRateLimit_Returns429AfterBudget(t *testing.T) {
	// A tiny per-IP budget so the test is fast and deterministic.
	rest := &server.REST{
		Service:   newTestService(t),
		Version:   "test",
		RateLimit: server.RateLimitConfig{PerIPPerMinute: 3},
	}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()

	var got200, got429 int
	for range 10 {
		resp, err := http.Get(srv.URL + "/v1/platforms")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			got200++
		case http.StatusTooManyRequests:
			got429++
			if ra := resp.Header.Get("Retry-After"); ra == "" {
				t.Error("429 response is missing Retry-After")
			}
		default:
			t.Fatalf("unexpected status %d", resp.StatusCode)
		}
	}
	if got200 != 3 {
		t.Errorf("allowed %d requests, want exactly the budget of 3", got200)
	}
	if got429 != 7 {
		t.Errorf("rejected %d requests, want 7", got429)
	}
}

func TestRateLimit_DoesNotApplyToLivez(t *testing.T) {
	// Liveness must never be rate-limited, or a burst of user traffic could
	// starve the container healthcheck and trigger a spurious restart.
	rest := &server.REST{
		Service:   newTestService(t),
		Version:   "test",
		RateLimit: server.RateLimitConfig{PerIPPerMinute: 2},
	}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()

	for i := range 10 {
		resp, err := http.Get(srv.URL + "/livez")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/livez request %d = %d, want 200 (never rate-limited)", i, resp.StatusCode)
		}
	}
}

func TestRateLimit_SharedAcrossRouteGroups(t *testing.T) {
	// The router registers the base and analyze endpoints in separate groups
	// (they offer different response formats), but the budget is one per
	// instance, not one per group.
	rest := &server.REST{
		Service:   newTestService(t),
		Version:   "test",
		RateLimit: server.RateLimitConfig{PerIPPerMinute: 2},
	}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()

	for _, path := range []string{"/health", "/v1/platforms"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d, want 200 within budget", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/v1/analyze/topic?topic=hello")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("analyze after exhausting the budget elsewhere = %d, want 429", resp.StatusCode)
	}
}

func TestRateLimit_DisabledWhenZero(t *testing.T) {
	rest := &server.REST{
		Service:   newTestService(t),
		Version:   "test",
		RateLimit: server.RateLimitConfig{}, // both zero => disabled
	}
	srv := httptest.NewServer(rest.Router())
	defer srv.Close()

	for i := range 20 {
		resp, err := http.Get(srv.URL + "/v1/platforms")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 (limiting disabled)", i, resp.StatusCode)
		}
	}
}
