package main

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Метрики в текстовом формате Prometheus без внешних библиотек.
// Метка path берётся из шаблона маршрута, а не из URL: иначе любой сканер
// создавал бы новые временные ряды и раздувал TSDB.

var buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}

type seriesKey struct {
	method, path string
	code         int
}

type histogram struct {
	counts []uint64 // по бакетам, не накопительно
	sum    float64
	count  uint64
}

type metrics struct {
	mu       sync.Mutex
	requests map[seriesKey]uint64
	duration map[string]*histogram // по path
}

func newMetrics() *metrics {
	return &metrics{requests: map[seriesKey]uint64{}, duration: map[string]*histogram{}}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (m *metrics) instrument(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		mux.ServeHTTP(rec, r)
		// После ServeHTTP r.Pattern заполнен шаблоном маршрута ("GET /healthz"); для 404 он пуст.
		_, pattern := mux.Handler(r)
		path := routeLabel(pattern)
		m.observe(r.Method, path, rec.code, time.Since(start).Seconds())
	})
}

func routeLabel(pattern string) string {
	if pattern == "" {
		return "other"
	}
	// "GET /{$}" → "/"
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '/' {
			p := pattern[i:]
			if p == "/{$}" {
				return "/"
			}
			return p
		}
	}
	return pattern
}

func (m *metrics) observe(method, path string, code int, seconds float64) {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
	default:
		method = "OTHER" // метод тоже приходит от клиента
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[seriesKey{method, path, code}]++
	h := m.duration[path]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(buckets)+1)}
		m.duration[path] = h
	}
	i := sort.SearchFloat64s(buckets, seconds) // первый бакет с границей >= seconds
	h.counts[i]++
	h.sum += seconds
	h.count++
}

func (m *metrics) write(w io.Writer, version string, ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintf(w, "# HELP pulse_build_info Build information.\n# TYPE pulse_build_info gauge\n")
	fmt.Fprintf(w, "pulse_build_info{version=%q} 1\n", version)
	fmt.Fprintf(w, "# HELP pulse_ready 1 if the instance accepts traffic.\n# TYPE pulse_ready gauge\n")
	fmt.Fprintf(w, "pulse_ready %d\n", boolToInt(ready))

	keys := make([]seriesKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.path != b.path {
			return a.path < b.path
		}
		if a.method != b.method {
			return a.method < b.method
		}
		return a.code < b.code
	})
	fmt.Fprintf(w, "# HELP pulse_http_requests_total HTTP requests.\n# TYPE pulse_http_requests_total counter\n")
	for _, k := range keys {
		fmt.Fprintf(w, "pulse_http_requests_total{method=%q,path=%q,code=\"%d\"} %d\n", k.method, k.path, k.code, m.requests[k])
	}

	paths := make([]string, 0, len(m.duration))
	for p := range m.duration {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	fmt.Fprintf(w, "# HELP pulse_http_request_duration_seconds Request latency.\n# TYPE pulse_http_request_duration_seconds histogram\n")
	for _, p := range paths {
		h := m.duration[p]
		var cumulative uint64
		for i, le := range buckets {
			cumulative += h.counts[i]
			fmt.Fprintf(w, "pulse_http_request_duration_seconds_bucket{path=%q,le=%q} %d\n", p, strconv.FormatFloat(le, 'g', -1, 64), cumulative)
		}
		fmt.Fprintf(w, "pulse_http_request_duration_seconds_bucket{path=%q,le=\"+Inf\"} %d\n", p, h.count)
		fmt.Fprintf(w, "pulse_http_request_duration_seconds_sum{path=%q} %g\n", p, h.sum)
		fmt.Fprintf(w, "pulse_http_request_duration_seconds_count{path=%q} %d\n", p, h.count)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
