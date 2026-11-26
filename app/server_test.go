package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	cfg, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(cfg, "1.2.3", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.sleep = func(time.Duration) {}
	return s, s.routes()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestIndex(t *testing.T) {
	_, h := testServer(t)
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "1.2.3" || body["service"] != "pulse" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	_, h := testServer(t)
	if rec := get(t, h, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestReadinessFlipsOnShutdown(t *testing.T) {
	s, h := testServer(t)
	if rec := get(t, h, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("ready before shutdown: %d", rec.Code)
	}
	var slept time.Duration
	s.sleep = func(d time.Duration) {
		slept = d
		// Во время задержки под уже не ready, но liveness ещё отвечает.
		if rec := get(t, h, "/readyz"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("readyz during shutdown = %d", rec.Code)
		}
		if rec := get(t, h, "/healthz"); rec.Code != http.StatusOK {
			t.Errorf("healthz during shutdown = %d", rec.Code)
		}
	}
	if err := s.shutdown(&http.Server{}); err != nil {
		t.Fatal(err)
	}
	if slept != 5*time.Second {
		t.Fatalf("shutdown delay = %v", slept)
	}
}

func TestShutdownWaitsForInflightRequests(t *testing.T) {
	s, _ := testServer(t)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = io.WriteString(w, "done")
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()

	result := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			result <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		result <- string(b)
	}()
	time.Sleep(50 * time.Millisecond) // запрос успел дойти до обработчика

	done := make(chan error, 1)
	go func() { done <- s.shutdown(srv) }()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("shutdown returned while a request was in flight")
	default:
	}
	close(release)
	if got := <-result; got != "done" {
		t.Fatalf("in-flight request: %q", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMetrics(t *testing.T) {
	_, h := testServer(t)
	get(t, h, "/")
	get(t, h, "/healthz")
	get(t, h, "/healthz")
	get(t, h, "/wp-login.php") // сканер: не должен создавать отдельный ряд
	rec := get(t, h, "/metrics")
	body := rec.Body.String()
	for _, want := range []string{
		`pulse_build_info{version="1.2.3"} 1`,
		`pulse_ready 1`,
		`pulse_http_requests_total{method="GET",path="/healthz",code="200"} 2`,
		`pulse_http_requests_total{method="GET",path="/",code="200"} 1`,
		`pulse_http_requests_total{method="GET",path="other",code="404"} 1`,
		`pulse_http_request_duration_seconds_bucket{path="/healthz",le="+Inf"} 2`,
		`pulse_http_request_duration_seconds_count{path="/healthz"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "wp-login") {
		t.Error("raw URL leaked into metric labels")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type %q", ct)
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	m := newMetrics()
	m.observe("GET", "/", 200, 0.003)
	m.observe("GET", "/", 200, 0.2)
	m.observe("GET", "/", 200, 10)
	m.observe("BREW", "/", 418, 0.001)
	var sb strings.Builder
	m.write(&sb, "v", false)
	out := sb.String()
	for _, want := range []string{
		`pulse_http_request_duration_seconds_bucket{path="/",le="0.005"} 2`,
		`pulse_http_request_duration_seconds_bucket{path="/",le="0.25"} 3`,
		`pulse_http_request_duration_seconds_bucket{path="/",le="2.5"} 3`,
		`pulse_http_request_duration_seconds_bucket{path="/",le="+Inf"} 4`,
		`pulse_http_requests_total{method="OTHER",path="/",code="418"} 1`,
		`pulse_ready 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	env := map[string]string{"PORT": "9090", "SHUTDOWN_DELAY": "0s", "SHUTDOWN_TIMEOUT": "3s", "APP_ENV": "staging"}
	cfg, err := loadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9090" || cfg.ShutdownDelay != 0 || cfg.ShutdownTimeout != 3*time.Second || cfg.Environment != "staging" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	for _, bad := range []map[string]string{{"PORT": "abc"}, {"PORT": "70000"}, {"SHUTDOWN_DELAY": "soon"}, {"SHUTDOWN_TIMEOUT": "-1s"}} {
		if _, err := loadConfig(func(k string) string { return bad[k] }); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
}

func TestRouteLabel(t *testing.T) {
	for in, want := range map[string]string{"": "other", "GET /{$}": "/", "GET /healthz": "/healthz", "/x": "/x"} {
		if got := routeLabel(in); got != want {
			t.Errorf("routeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
