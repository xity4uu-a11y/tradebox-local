package web

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xity4uu-a11y/tradebox-local/internal/alerts"
	"github.com/xity4uu-a11y/tradebox-local/internal/config"
	"github.com/xity4uu-a11y/tradebox-local/internal/journal"
	"github.com/xity4uu-a11y/tradebox-local/internal/latency"
	"github.com/xity4uu-a11y/tradebox-local/internal/watchdog"
)

func TestWebServerRoutesAndEmbeddedUI(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen failed: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	tcpAddr := ln.Addr().(*net.TCPAddr)

	logsDir := t.TempDir()
	sampleLog := filepath.Join(logsDir, "MQL5", "Logs", "20261125.log")
	if err := journal.GenerateSampleUTF16LELog(sampleLog); err != nil {
		t.Fatalf("generate sample log failed: %v", err)
	}
	tailer := journal.NewTailer(logsDir, 100, nil)
	if _, err := tailer.Poll(false); err != nil {
		t.Fatalf("tailer poll failed: %v", err)
	}

	mon := latency.NewMonitor([]config.BrokerTarget{
		{Name: "TestBroker", Host: "127.0.0.1", Port: tcpAddr.Port, MaxLatencyMs: 250, TimeoutMs: 1000},
	}, 20, nil)

	sup := watchdog.NewSupervisor(watchdog.Options{
		Command: "terminal64.exe",
		Args:    []string{"/portable"},
		Policy:  config.DefaultConfig().RestartPolicy,
	})

	disp := alerts.NewDispatcher(config.AlertsConfig{CooldownSec: 60}, nil)

	srv := NewServer(ServerDependencies{
		Version:    "1.0.0",
		NoVNCURL:   "http://127.0.0.1:6080/vnc.html",
		PluginsDir: filepath.Join("..", "..", "plugins"),
		Supervisor: sup,
		Latency:    mon,
		Journal:    tailer,
		Alerts:     disp,
	})

	handler := srv.Handler()

	// 1. GET / serves embedded HTML dashboard
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "TradeBox Local") {
		t.Fatalf("GET / unexpected response: status=%d", rec.Code)
	}

	// 2. GET /assets/styles.css serves embedded CSS
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/styles.css", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "--surface") {
		t.Fatalf("GET /assets/styles.css unexpected response: status=%d", rec.Code)
	}

	// 3. GET /healthz
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz expected 200, got %d", rec.Code)
	}

	// 4. GET /api/status
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/status expected 200, got %d", rec.Code)
	}
	var statusBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &statusBody); err != nil {
		t.Fatalf("invalid /api/status JSON: %v", err)
	}
	if statusBody["novnc_url"] != "http://127.0.0.1:6080/vnc.html" {
		t.Errorf("unexpected novnc_url: %v", statusBody["novnc_url"])
	}

	// 5. POST /api/latency/probe & GET /api/latency
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/latency/probe", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"healthy"`) {
		t.Fatalf("POST /api/latency/probe unexpected response: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 6. GET /api/journal?filter=errors
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/journal?filter=errors", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "mt5_error_4756") {
		t.Fatalf("GET /api/journal?filter=errors missing expected Error 4756: code=%d", rec.Code)
	}

	// 7. POST /api/alerts/test & GET /api/alerts
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/alerts/test", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"no_channels"`) {
		t.Fatalf("POST /api/alerts/test unexpected response: code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/alerts", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "TradeBox Local Test Notification") {
		t.Fatalf("GET /api/alerts missing test notification history")
	}

	// 8. POST /api/terminal/stop (idempotent when stopped)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/terminal/stop", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/terminal/stop expected 200, got %d", rec.Code)
	}
}
