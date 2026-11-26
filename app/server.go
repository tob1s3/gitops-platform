package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

type config struct {
	Addr string
	// Сколько ждать после SIGTERM, прежде чем перестать принимать соединения.
	// За это время kube-proxy и Ingress успевают убрать под из балансировки,
	// иначе часть запросов во время rolling update получит ошибку соединения.
	ShutdownDelay   time.Duration
	ShutdownTimeout time.Duration
	Environment     string
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		Addr:            ":8080",
		ShutdownDelay:   5 * time.Second,
		ShutdownTimeout: 20 * time.Second,
		Environment:     "local",
	}
	if v := getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return cfg, fmt.Errorf("PORT: invalid value %q", v)
		}
		cfg.Addr = ":" + v
	}
	for name, dst := range map[string]*time.Duration{
		"SHUTDOWN_DELAY":   &cfg.ShutdownDelay,
		"SHUTDOWN_TIMEOUT": &cfg.ShutdownTimeout,
	} {
		if v := getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 {
				return cfg, fmt.Errorf("%s: invalid duration %q", name, v)
			}
			*dst = d
		}
	}
	if v := getenv("APP_ENV"); v != "" {
		cfg.Environment = v
	}
	return cfg, nil
}

type server struct {
	cfg     config
	version string
	log     *slog.Logger
	metrics *metrics
	ready   atomic.Bool
	started time.Time
	sleep   func(time.Duration) // подменяется в тестах
}

func newServer(cfg config, version string, log *slog.Logger) *server {
	s := &server{cfg: cfg, version: version, log: log, metrics: newMetrics(), started: time.Now(), sleep: time.Sleep}
	s.ready.Store(true)
	return s
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	return s.metrics.instrument(mux)
}

func (s *server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]any{
		"service":     "pulse",
		"version":     s.version,
		"environment": s.cfg.Environment,
		"pod":         host,
		"uptime_s":    int(time.Since(s.started).Seconds()),
	})
}

// Liveness: процесс жив и отвечает. Не проверяем здесь зависимости — иначе сбой БД
// привёл бы к перезапуску всех подов, что только усугубило бы аварию.
func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readiness: готов ли под принимать трафик. Во время остановки отвечает 503.
func (s *server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "shutting down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.metrics.write(w, s.version, s.ready.Load())
}

// shutdown: перестать быть ready → подождать, пока под уберут из Endpoints → дождаться
// завершения активных запросов.
func (s *server) shutdown(srv *http.Server) error {
	s.ready.Store(false)
	s.log.Info("shutdown started", "delay", s.cfg.ShutdownDelay.String())
	s.sleep(s.cfg.ShutdownDelay)
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	s.log.Info("shutdown complete")
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
