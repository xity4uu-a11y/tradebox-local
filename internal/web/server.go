package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/xity4uu-a11y/tradebox-local/internal/alerts"
	"github.com/xity4uu-a11y/tradebox-local/internal/journal"
	"github.com/xity4uu-a11y/tradebox-local/internal/latency"
	"github.com/xity4uu-a11y/tradebox-local/internal/plugins"
	"github.com/xity4uu-a11y/tradebox-local/internal/power"
	"github.com/xity4uu-a11y/tradebox-local/internal/watchdog"
)

//go:embed assets/*
var embeddedAssets embed.FS

// ServerDependencies wires all runtime subsystems into the HTTP API and embedded UI server.
type ServerDependencies struct {
	Version    string
	NoVNCURL   string
	PluginsDir string
	Supervisor *watchdog.Supervisor
	Latency    *latency.Monitor
	Journal    *journal.Tailer
	Alerts     *alerts.Dispatcher
}

// Server serves the embedded Apple-minimalist dashboard and REST API endpoints.
type Server struct {
	deps      ServerDependencies
	startedAt time.Time
	host      power.HostReport
	mux       *http.ServeMux
}

// NewServer creates a configured HTTP handler for tradebox-local.
func NewServer(deps ServerDependencies) *Server {
	if deps.Version == "" {
		deps.Version = "1.0.0"
	}
	s := &Server{
		deps:      deps,
		startedAt: time.Now(),
		host:      power.InspectHost(),
		mux:       http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// Handler returns the root http.Handler with standard defensive security headers.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "no-referrer")
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) registerRoutes() {
	sub, err := fs.Sub(embeddedAssets, "assets")
	if err == nil {
		fileServer := http.FileServer(http.FS(sub))
		s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", fileServer))
	}

	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("POST /api/terminal/start", s.handleTerminalStart)
	s.mux.HandleFunc("POST /api/terminal/stop", s.handleTerminalStop)
	s.mux.HandleFunc("POST /api/terminal/restart", s.handleTerminalRestart)
	s.mux.HandleFunc("GET /api/latency", s.handleLatencyGet)
	s.mux.HandleFunc("POST /api/latency/probe", s.handleLatencyProbe)
	s.mux.HandleFunc("GET /api/journal", s.handleJournal)
	s.mux.HandleFunc("GET /api/alerts", s.handleAlertsGet)
	s.mux.HandleFunc("POST /api/alerts/test", s.handleAlertsTest)
	s.mux.HandleFunc("GET /api/plugins", s.handlePlugins)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	raw, err := embeddedAssets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "dashboard asset missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"version":    s.deps.Version,
		"uptime_sec": int64(time.Since(s.startedAt).Seconds()),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	var termStatus watchdog.Status
	if s.deps.Supervisor != nil {
		termStatus = s.deps.Supervisor.Status()
	}

	var jSummary journal.Summary
	if s.deps.Journal != nil {
		jSummary = s.deps.Journal.Summary()
	}

	var brokers []latency.BrokerStats
	if s.deps.Latency != nil {
		brokers = s.deps.Latency.Snapshot()
	}

	var alertChannels alerts.ChannelsStatus
	var recentAlerts int
	if s.deps.Alerts != nil {
		alertChannels = s.deps.Alerts.Channels()
		recentAlerts = len(s.deps.Alerts.History(50))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":         s.deps.Version,
		"daemon_uptime":   int64(time.Since(s.startedAt).Seconds()),
		"novnc_url":       s.deps.NoVNCURL,
		"terminal":        termStatus,
		"journal_summary": jSummary,
		"brokers_count":   len(brokers),
		"alerts_channels": alertChannels,
		"alerts_recent":   recentAlerts,
		"host":            s.host,
	})
}

func (s *Server) handleTerminalStart(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Supervisor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "supervisor not configured"})
		return
	}
	if err := s.deps.Supervisor.Start(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"terminal": s.deps.Supervisor.Status(),
	})
}

func (s *Server) handleTerminalStop(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Supervisor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "supervisor not configured"})
		return
	}
	if err := s.deps.Supervisor.Stop(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"terminal": s.deps.Supervisor.Status(),
	})
}

func (s *Server) handleTerminalRestart(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Supervisor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "supervisor not configured"})
		return
	}
	if err := s.deps.Supervisor.Restart(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"terminal": s.deps.Supervisor.Status(),
	})
}

func (s *Server) handleLatencyGet(w http.ResponseWriter, _ *http.Request) {
	brokers := []latency.BrokerStats{}
	if s.deps.Latency != nil {
		brokers = s.deps.Latency.Snapshot()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brokers": brokers,
	})
}

func (s *Server) handleLatencyProbe(w http.ResponseWriter, r *http.Request) {
	brokers := []latency.BrokerStats{}
	if s.deps.Latency != nil {
		brokers = s.deps.Latency.ProbeAll(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brokers": brokers,
	})
}

func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	limit := 100
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	entries := []journal.Entry{}
	files := []journal.FileState{}
	summary := journal.Summary{}

	if s.deps.Journal != nil {
		entries = s.deps.Journal.Entries(filter, limit)
		files = s.deps.Journal.Files()
		summary = s.deps.Journal.Summary()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filter":  filter,
		"summary": summary,
		"files":   files,
		"entries": entries,
	})
}

func (s *Server) handleAlertsGet(w http.ResponseWriter, _ *http.Request) {
	channels := alerts.ChannelsStatus{}
	history := []alerts.DispatchRecord{}
	if s.deps.Alerts != nil {
		channels = s.deps.Alerts.Channels()
		history = s.deps.Alerts.History(50)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": channels,
		"history":  history,
	})
}

func (s *Server) handleAlertsTest(w http.ResponseWriter, r *http.Request) {
	if s.deps.Alerts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "alert dispatcher not configured"})
		return
	}
	rec := s.deps.Alerts.Dispatch(r.Context(), alerts.Alert{
		RuleKey:        "system:manual_test",
		Severity:       "info",
		Category:       "system",
		Title:          "TradeBox Local Test Notification",
		Message:        "Manual verification test triggered from TradeBox Local Web Dashboard.",
		Source:         s.host.Hostname,
		BypassCooldown: true,
		Metadata: map[string]string{
			"os":      s.host.OS + "/" + s.host.Arch,
			"version": s.deps.Version,
		},
	})
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handlePlugins(w http.ResponseWriter, _ *http.Request) {
	if s.deps.PluginsDir == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"total":   0,
			"passed":  0,
			"failed":  0,
			"plugins": []plugins.Plugin{},
			"results": []plugins.FileResult{},
		})
		return
	}
	report, err := plugins.LoadDir(s.deps.PluginsDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"dir":     s.deps.PluginsDir,
			"total":   0,
			"passed":  0,
			"failed":  0,
			"plugins": []plugins.Plugin{},
			"results": []plugins.FileResult{},
			"warning": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
