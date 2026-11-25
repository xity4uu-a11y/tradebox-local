package latency

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xity4uu-a11y/tradebox-local/internal/config"
)

func startLocalTCPServer(t *testing.T) (string, int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start local tcp listener: %v", err)
	}

	stop := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
					return
				}
			}
			_ = conn.Close()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	cleanup := func() {
		close(stop)
		_ = ln.Close()
	}
	return addr.IP.String(), addr.Port, cleanup
}

func TestComputeStatsPercentilesAndJitter(t *testing.T) {
	now := time.Now()
	samples := []Sample{
		{Timestamp: now, RTTMs: 10, Success: true},
		{Timestamp: now.Add(time.Second), RTTMs: 20, Success: true},
		{Timestamp: now.Add(2 * time.Second), RTTMs: 30, Success: true},
		{Timestamp: now.Add(3 * time.Second), RTTMs: 40, Success: true},
		{Timestamp: now.Add(4 * time.Second), RTTMs: 100, Success: true},
	}

	st := ComputeStats(config.BrokerTarget{
		Name:         "Synthetic",
		Host:         "127.0.0.1",
		Port:         443,
		MaxLatencyMs: 150,
	}, samples)

	if st.Status != StatusHealthy {
		t.Errorf("expected healthy status, got %s", st.Status)
	}
	if st.MinMs != 10 || st.MaxMs != 100 || st.P50Ms != 30 {
		t.Errorf("unexpected min/p50/max: min=%.2f p50=%.2f max=%.2f", st.MinMs, st.P50Ms, st.MaxMs)
	}
	if st.P95Ms < 85 || st.P95Ms > 100 {
		t.Errorf("unexpected p95=%.2f", st.P95Ms)
	}
	// Differences: |20-10|=10, |30-20|=10, |40-30|=10, |100-40|=60 -> avg jitter = 90/4 = 22.5
	if st.JitterMs != 22.5 {
		t.Errorf("expected jitter 22.50, got %.2f", st.JitterMs)
	}
	if st.LossPct != 0 {
		t.Errorf("expected 0%% loss, got %.2f", st.LossPct)
	}
}

func TestMonitorProbesRealLoopbackTCPServerAndTriggersAlerts(t *testing.T) {
	host, port, cleanup := startLocalTCPServer(t)
	defer cleanup()

	// Find an unreachable closed ephemeral port by listening and closing immediately.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate closed port: %v", err)
	}
	deadPort := deadLn.Addr().(*net.TCPAddr).Port
	_ = deadLn.Close()

	var mu sync.Mutex
	var alerts []AlertEvent

	targets := []config.BrokerTarget{
		{
			Name:         "Local-Healthy",
			Host:         host,
			Port:         port,
			MaxLatencyMs: 500,
			TimeoutMs:    1500,
		},
		{
			Name:         "Local-Dead",
			Host:         "127.0.0.1",
			Port:         deadPort,
			MaxLatencyMs: 100,
			TimeoutMs:    400,
		},
	}

	mon := NewMonitor(targets, 10, func(ev AlertEvent) {
		mu.Lock()
		alerts = append(alerts, ev)
		mu.Unlock()
	})

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		mon.ProbeAll(ctx)
	}

	snap := mon.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 broker snapshots, got %d", len(snap))
	}

	healthy := snap[0]
	if healthy.Status != StatusHealthy {
		t.Errorf("expected Local-Healthy to be healthy, got %s (err=%q)", healthy.Status, healthy.LastError)
	}
	if healthy.SampleCount != 4 || healthy.LossPct != 0 {
		t.Errorf("expected 4 samples with 0%% loss, got count=%d loss=%.2f", healthy.SampleCount, healthy.LossPct)
	}
	if len(healthy.History) != 4 {
		t.Errorf("expected 4 history entries for sparkline, got %d", len(healthy.History))
	}

	dead := snap[1]
	if dead.Status != StatusDown {
		t.Errorf("expected Local-Dead to be down, got %s", dead.Status)
	}
	if dead.LossPct != 100 {
		t.Errorf("expected 100%% loss for closed port, got %.2f", dead.LossPct)
	}

	mu.Lock()
	alertCount := len(alerts)
	mu.Unlock()
	if alertCount < 1 {
		t.Error("expected at least one threshold/unreachable alert to fire")
	}
}

func TestProbeSeriesAgainstLocalListener(t *testing.T) {
	host, port, cleanup := startLocalTCPServer(t)
	defer cleanup()

	stats := ProbeSeries(context.Background(), config.BrokerTarget{
		Name:         "CLI-Probe",
		Host:         host,
		Port:         port,
		MaxLatencyMs: 250,
		TimeoutMs:    1000,
	}, 5, 5*time.Millisecond)

	if stats.SampleCount != 5 {
		t.Fatalf("expected 5 samples, got %d", stats.SampleCount)
	}
	if stats.Status != StatusHealthy {
		t.Errorf("expected healthy status, got %s", stats.Status)
	}
	if stats.P50Ms > stats.P95Ms || stats.MinMs > stats.MaxMs {
		t.Errorf("invalid ordering min=%.2f p50=%.2f p95=%.2f max=%.2f", stats.MinMs, stats.P50Ms, stats.P95Ms, stats.MaxMs)
	}
}
