package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/xity4uu-a11y/tradebox-local/internal/alerts"
	"github.com/xity4uu-a11y/tradebox-local/internal/config"
	"github.com/xity4uu-a11y/tradebox-local/internal/journal"
	"github.com/xity4uu-a11y/tradebox-local/internal/latency"
	"github.com/xity4uu-a11y/tradebox-local/internal/plugins"
	"github.com/xity4uu-a11y/tradebox-local/internal/power"
	"github.com/xity4uu-a11y/tradebox-local/internal/watchdog"
	"github.com/xity4uu-a11y/tradebox-local/internal/web"
)

// Version is overridden at release build time via -ldflags "-X main.Version=...".
var Version = "1.0.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[tradebox] ")

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = runInit(args)
	case "serve":
		err = runServe(args)
	case "probe":
		err = runProbe(args)
	case "parse-log":
		err = runParseLog(args)
	case "host-check":
		err = runHostCheck(args)
	case "validate-plugins":
		err = runValidatePlugins(args)
	case "version", "--version", "-v":
		fmt.Printf("tradebox-local v%s (%s/%s, %s)\n", Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return
	case "help", "--help", "-h":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`tradebox-local - Self-hosted 24/7 MetaTrader 5 (MT5) watchdog & execution monitor

Usage:
  tradebox <command> [flags]

Commands:
  init               Generate tradebox.json and a sample UTF-16LE MT5 log fixture
  serve              Start the 24/7 watchdog daemon, probes, log tailer, and web UI
  probe              Measure broker TCP handshake latency (RTT, p50, p95, jitter, loss)
  parse-log <path>   Decode and classify a UTF-16LE or UTF-8 MetaTrader 5 .log file
  host-check         Inspect 24/7 host readiness and print safe keep-awake commands
  validate-plugins   Validate all community plugin manifests in the plugins directory
  version            Print build version and runtime platform details

Examples:
  tradebox init --config tradebox.json
  tradebox serve --config tradebox.json --addr 127.0.0.1:8092
  tradebox probe --host access.metatrader5.com --port 443 --count 5
  tradebox parse-log ./mt5-logs/MQL5/Logs/20261125.log --filter errors
  tradebox host-check
  tradebox validate-plugins --dir ./plugins`)
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	cfgPath := fs.String("config", "tradebox.json", "Path to write tradebox.json")
	logsDir := fs.String("logs-dir", "./mt5-logs", "Directory for MT5 logs and sample fixture")
	force := fs.Bool("force", false, "Overwrite existing configuration file if present")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*force {
		if _, err := os.Stat(*cfgPath); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", *cfgPath)
		}
	}

	cfg := config.DefaultConfig()
	cfg.MT5LogsDir = *logsDir

	if err := config.Save(*cfgPath, cfg); err != nil {
		return err
	}

	sampleName := time.Now().Format("20060102") + ".log"
	samplePath := filepath.Join(*logsDir, "MQL5", "Logs", sampleName)
	if err := journal.GenerateSampleUTF16LELog(samplePath); err != nil {
		return fmt.Errorf("write sample UTF-16LE MT5 log: %w", err)
	}

	fmt.Printf("Created configuration: %s\n", *cfgPath)
	fmt.Printf("Created UTF-16LE MT5 sample log: %s\n", samplePath)
	fmt.Printf("Next step: tradebox serve --config %s --addr %s\n", *cfgPath, cfg.HTTPAddr)
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "tradebox.json", "Path to tradebox.json configuration")
	addrOverride := fs.String("addr", "", "Override HTTP bind address (e.g. 127.0.0.1:8092)")
	pluginsOverride := fs.String("plugins-dir", "", "Override plugins directory")
	autoStartOverride := fs.Bool("auto-start", false, "Immediately start the configured terminal process on launch")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*addrOverride) != "" {
		cfg.HTTPAddr = strings.TrimSpace(*addrOverride)
	}
	if strings.TrimSpace(*pluginsOverride) != "" {
		cfg.PluginsDir = strings.TrimSpace(*pluginsOverride)
	}
	if *autoStartOverride {
		cfg.AutoStart = true
	}

	rootCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	dispatcher := alerts.NewDispatcher(cfg.Alerts, nil)

	supervisor := watchdog.NewSupervisor(watchdog.Options{
		Command:    cfg.TerminalCommand,
		Args:       cfg.TerminalArgs,
		WorkingDir: cfg.WorkingDir,
		Policy:     cfg.RestartPolicy,
		OnEvent: func(ev watchdog.Event) {
			log.Printf("watchdog [%s]: %s", ev.Type, ev.Message)
			if ev.Type == watchdog.EventCrashed || ev.Type == watchdog.EventCircuitOpen {
				sev := "warn"
				title := "MT5 Terminal Process Crashed"
				if ev.Type == watchdog.EventCircuitOpen {
					sev = "critical"
					title = "MT5 Watchdog Crash-Loop Circuit Breaker Opened"
				}
				dispatcher.Dispatch(rootCtx, alerts.Alert{
					RuleKey:  "watchdog:" + string(ev.Type),
					Severity: sev,
					Category: "watchdog",
					Title:    title,
					Message:  ev.Message,
					Source:   cfg.TerminalCommand,
					Metadata: map[string]string{
						"exit_code":     fmt.Sprintf("%d", ev.ExitCode),
						"restart_count": fmt.Sprintf("%d", ev.RestartCount),
					},
				})
			}
		},
	})

	monitor := latency.NewMonitor(cfg.Brokers, 60, func(ev latency.AlertEvent) {
		log.Printf("latency [%s]: %s", ev.Severity, ev.Reason)
		dispatcher.Dispatch(rootCtx, alerts.Alert{
			RuleKey:  fmt.Sprintf("latency:%s:%s", ev.Broker.Name, ev.Broker.Status),
			Severity: ev.Severity,
			Category: "latency",
			Title:    fmt.Sprintf("Broker Latency Alert: %s", ev.Broker.Name),
			Message:  ev.Reason,
			Source:   fmt.Sprintf("%s:%d", ev.Broker.Host, ev.Broker.Port),
			Metadata: map[string]string{
				"last_ms": fmt.Sprintf("%.2f", ev.Broker.LastMs),
				"p95_ms":  fmt.Sprintf("%.2f", ev.Broker.P95Ms),
				"loss":    fmt.Sprintf("%.1f%%", ev.Broker.LossPct),
			},
		})
	})

	tailer := journal.NewTailer(cfg.MT5LogsDir, 500, func(entry journal.Entry) {
		sev := "warn"
		if entry.Severity == "error" {
			sev = "critical"
		}
		rule := entry.RuleID
		if rule == "" {
			rule = entry.Category
		}
		log.Printf("journal [%s/%s]: %s", entry.Severity, rule, entry.Message)
		dispatcher.Dispatch(rootCtx, alerts.Alert{
			RuleKey:  "journal:" + rule,
			Severity: sev,
			Category: "journal",
			Title:    fmt.Sprintf("MT5 Journal Alert (%s)", rule),
			Message:  entry.Message,
			Source:   entry.Source,
			Metadata: map[string]string{
				"file":     entry.File,
				"encoding": string(entry.Encoding),
				"category": entry.Category,
			},
		})
	})

	if cfg.AutoStart {
		if err := supervisor.Start(); err != nil {
			log.Printf("auto-start failed: %v", err)
		}
	}

	go monitor.Run(rootCtx, time.Duration(cfg.LatencyCheckSec)*time.Second)
	go tailer.Run(rootCtx, time.Duration(cfg.JournalPollSec)*time.Second)

	webSrv := web.NewServer(web.ServerDependencies{
		Version:    Version,
		NoVNCURL:   cfg.NoVNCURL,
		PluginsDir: cfg.PluginsDir,
		Supervisor: supervisor,
		Latency:    monitor,
		Journal:    tailer,
		Alerts:     dispatcher,
	})

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           webSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("dashboard listening on http://%s (config=%s)", cfg.HTTPAddr, *cfgPath)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-rootCtx.Done():
		log.Printf("shutdown signal received; stopping server and terminal supervisor")
	case err := <-errCh:
		if err != nil {
			_ = supervisor.Stop()
			return fmt.Errorf("http server error: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	_ = supervisor.Stop()
	return nil
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	host := fs.String("host", "", "Broker TCP hostname or IP address")
	port := fs.Int("port", 443, "Broker TCP port (commonly 443)")
	count := fs.Int("count", 5, "Number of TCP handshake probes to run")
	timeoutMs := fs.Int("timeout-ms", 3000, "Per-probe TCP connect timeout in ms")
	maxLatencyMs := fs.Int("max-latency-ms", 120, "Latency warning threshold in ms")
	asJSON := fs.Bool("json", false, "Emit structured JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*host) == "" {
		return errors.New("--host is required (e.g. --host access.metatrader5.com --port 443)")
	}
	if *port < 1 || *port > 65535 {
		return errors.New("--port must be between 1 and 65535")
	}

	target := config.BrokerTarget{
		Name:         *host,
		Host:         strings.TrimSpace(*host),
		Port:         *port,
		MaxLatencyMs: *maxLatencyMs,
		TimeoutMs:    *timeoutMs,
	}

	stats := latency.ProbeSeries(context.Background(), target, *count, 150*time.Millisecond)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	}

	fmt.Printf("Broker TCP Latency Probe: %s:%d (%d samples)\n", stats.Host, stats.Port, stats.SampleCount)
	for i, s := range stats.History {
		if s.Success {
			fmt.Printf("  #%d  %.2f ms  OK\n", i+1, s.RTTMs)
		} else {
			fmt.Printf("  #%d  FAIL     %s\n", i+1, s.Error)
		}
	}
	fmt.Printf("\nSummary [%s]: min=%.2fms p50=%.2fms p95=%.2fms max=%.2fms jitter=%.2fms loss=%.1f%% (threshold=%dms)\n",
		strings.ToUpper(string(stats.Status)), stats.MinMs, stats.P50Ms, stats.P95Ms, stats.MaxMs, stats.JitterMs, stats.LossPct, stats.MaxLatencyMs)
	return nil
}

func runParseLog(args []string) error {
	fs := flag.NewFlagSet("parse-log", flag.ContinueOnError)
	filter := fs.String("filter", "all", "Filter events: all|errors|trades|connections")
	asJSON := fs.Bool("json", false, "Emit structured JSON output")

	// Support both `parse-log <path> --filter errors` and `parse-log --filter errors <path>`.
	var path string
	var flagArgs []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && path == "" {
			path = a
		} else {
			flagArgs = append(flagArgs, a)
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if path == "" && fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("log file path is required (e.g. tradebox parse-log ./mt5-logs/MQL5/Logs/20261125.log)")
	}

	entries, enc, err := journal.ParseFile(path)
	if err != nil {
		return err
	}

	filtered := make([]journal.Entry, 0, len(entries))
	counts := map[string]int{"error": 0, "warn": 0, "trade": 0, "info": 0}
	for _, e := range entries {
		counts[e.Severity]++
		if journal.MatchesFilter(e, *filter) {
			filtered = append(filtered, e)
		}
	}

	if *asJSON {
		out := map[string]any{
			"file":     path,
			"encoding": enc,
			"total":    len(entries),
			"matched":  len(filtered),
			"counts":   counts,
			"entries":  filtered,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(out)
	}

	fmt.Printf("MT5 Log Parser: %s\n", path)
	fmt.Printf("Detected Encoding: %s | Total Lines: %d | Errors: %d | Warnings: %d | Trades: %d | Showing (%s): %d\n\n",
		enc, len(entries), counts["error"], counts["warn"], counts["trade"], *filter, len(filtered))

	for _, e := range filtered {
		ruleSuffix := ""
		if e.RuleID != "" {
			ruleSuffix = " [" + e.RuleID + "]"
		}
		fmt.Printf("%-12s %-5s %-11s %-26s %s%s\n",
			e.Timestamp, strings.ToUpper(e.Severity), e.Category, truncate(e.Source, 26), e.Message, ruleSuffix)
	}
	return nil
}

func runHostCheck(args []string) error {
	fs := flag.NewFlagSet("host-check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "Emit structured JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	report := power.InspectHost()
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Print(power.FormatReport(report))
	return nil
}

func runValidatePlugins(args []string) error {
	fs := flag.NewFlagSet("validate-plugins", flag.ContinueOnError)
	dir := fs.String("dir", "./plugins", "Directory containing plugin JSON manifests")
	asJSON := fs.Bool("json", false, "Emit structured JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	report, err := plugins.LoadDir(*dir)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		if report.Failed > 0 || report.Total == 0 {
			return fmt.Errorf("plugin validation failed (%d/%d passed)", report.Passed, report.Total)
		}
		return nil
	}

	for _, res := range report.Results {
		if res.Valid {
			fmt.Printf("PASS  %s (%s)\n", res.File, res.ID)
		} else {
			fmt.Printf("FAIL  %s: %s\n", res.File, strings.Join(res.Errors, "; "))
		}
	}
	fmt.Printf("\nPlugin Validation Summary: %d/%d PASS\n", report.Passed, report.Total)
	if report.Total == 0 {
		return fmt.Errorf("no plugin manifests found in %q", *dir)
	}
	if report.Failed > 0 {
		return fmt.Errorf("%d plugin manifest(s) failed validation", report.Failed)
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
