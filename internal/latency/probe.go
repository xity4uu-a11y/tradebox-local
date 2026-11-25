package latency

import (
	"context"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/xity4uu-a11y/tradebox-local/internal/config"
)

// Status describes the current network reachability and latency state of a broker endpoint.
type Status string

const (
	StatusUnknown  Status = "unknown"
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// Sample records a single TCP handshake latency measurement.
type Sample struct {
	Timestamp time.Time `json:"timestamp"`
	RTTMs     float64   `json:"rtt_ms"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
}

// BrokerStats holds rolling-window execution latency statistics for one broker server.
type BrokerStats struct {
	Name         string     `json:"name"`
	Host         string     `json:"host"`
	Port         int        `json:"port"`
	MaxLatencyMs int        `json:"max_latency_ms"`
	Status       Status     `json:"status"`
	LastMs       float64    `json:"last_ms"`
	MinMs        float64    `json:"min_ms"`
	AvgMs        float64    `json:"avg_ms"`
	P50Ms        float64    `json:"p50_ms"`
	P95Ms        float64    `json:"p95_ms"`
	MaxMs        float64    `json:"max_ms"`
	JitterMs     float64    `json:"jitter_ms"`
	LossPct      float64    `json:"loss_pct"`
	SampleCount  int        `json:"sample_count"`
	LastChecked  *time.Time `json:"last_checked,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	History      []Sample   `json:"history"`
}

// AlertEvent describes a threshold breach or connectivity failure on a broker endpoint.
type AlertEvent struct {
	Broker   BrokerStats
	Severity string
	Reason   string
}

// ProbeOnce measures TCP connection handshake latency to host:port using net.DialTimeout semantics.
func ProbeOnce(ctx context.Context, host string, port int, timeout time.Duration) Sample {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	now := time.Now()

	dialer := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	elapsed := time.Since(start)
	if err != nil {
		return Sample{
			Timestamp: now,
			RTTMs:     0,
			Success:   false,
			Error:     err.Error(),
		}
	}
	_ = conn.Close()

	rttMs := round2(float64(elapsed.Microseconds()) / 1000.0)
	return Sample{
		Timestamp: now,
		RTTMs:     rttMs,
		Success:   true,
	}
}

// ComputeStats calculates rolling statistics (min, avg, p50, p95, max, jitter, packet loss) over samples.
func ComputeStats(target config.BrokerTarget, history []Sample) BrokerStats {
	histCopy := make([]Sample, len(history))
	copy(histCopy, history)

	st := BrokerStats{
		Name:         target.Name,
		Host:         target.Host,
		Port:         target.Port,
		MaxLatencyMs: target.MaxLatencyMs,
		Status:       StatusUnknown,
		SampleCount:  len(histCopy),
		History:      histCopy,
	}
	if len(histCopy) == 0 {
		return st
	}

	last := histCopy[len(histCopy)-1]
	ts := last.Timestamp
	st.LastChecked = &ts
	st.LastError = last.Error

	var rtts []float64
	var sum float64
	var jitterSum float64
	var jitterPairs int
	var prevSuccess bool
	var prevRTT float64
	failures := 0

	for _, s := range histCopy {
		if !s.Success {
			failures++
			prevSuccess = false
			continue
		}
		rtts = append(rtts, s.RTTMs)
		sum += s.RTTMs
		if prevSuccess {
			jitterSum += math.Abs(s.RTTMs - prevRTT)
			jitterPairs++
		}
		prevSuccess = true
		prevRTT = s.RTTMs
	}

	st.LossPct = round2(float64(failures) * 100.0 / float64(len(histCopy)))

	if len(rtts) > 0 {
		sorted := make([]float64, len(rtts))
		copy(sorted, rtts)
		sort.Float64s(sorted)

		st.MinMs = round2(sorted[0])
		st.MaxMs = round2(sorted[len(sorted)-1])
		st.AvgMs = round2(sum / float64(len(rtts)))
		st.P50Ms = round2(percentile(sorted, 0.50))
		st.P95Ms = round2(percentile(sorted, 0.95))
		if jitterPairs > 0 {
			st.JitterMs = round2(jitterSum / float64(jitterPairs))
		}
	}

	if !last.Success {
		st.LastMs = 0
		st.Status = StatusDown
		return st
	}

	st.LastMs = round2(last.RTTMs)
	if (target.MaxLatencyMs > 0 && last.RTTMs > float64(target.MaxLatencyMs)) || st.LossPct > 0 {
		st.Status = StatusDegraded
	} else {
		st.Status = StatusHealthy
	}
	return st
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	pos := p * float64(len(sorted)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return sorted[lower]
	}
	weight := pos - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// Monitor periodically measures broker TCP execution latency and maintains bounded rolling history.
type Monitor struct {
	targets    []config.BrokerTarget
	windowSize int
	onAlert    func(AlertEvent)

	mu      sync.RWMutex
	history map[string][]Sample
}

// NewMonitor initializes a latency monitor for the configured brokers.
func NewMonitor(targets []config.BrokerTarget, windowSize int, onAlert func(AlertEvent)) *Monitor {
	if windowSize <= 0 {
		windowSize = 60
	}
	tCopy := make([]config.BrokerTarget, len(targets))
	copy(tCopy, targets)

	hist := make(map[string][]Sample, len(tCopy))
	for _, t := range tCopy {
		hist[brokerKey(t)] = make([]Sample, 0, windowSize)
	}

	return &Monitor{
		targets:    tCopy,
		windowSize: windowSize,
		onAlert:    onAlert,
		history:    hist,
	}
}

func brokerKey(t config.BrokerTarget) string {
	return fmt.Sprintf("%s|%s:%d", t.Name, t.Host, t.Port)
}

// ProbeAll concurrently probes every configured broker, updates rolling stats, and emits threshold alerts.
func (m *Monitor) ProbeAll(ctx context.Context) []BrokerStats {
	results := make([]BrokerStats, len(m.targets))
	var wg sync.WaitGroup

	for i, target := range m.targets {
		wg.Add(1)
		go func(idx int, t config.BrokerTarget) {
			defer wg.Done()
			timeout := time.Duration(t.TimeoutMs) * time.Millisecond
			if timeout <= 0 {
				timeout = 3 * time.Second
			}
			sample := ProbeOnce(ctx, t.Host, t.Port, timeout)
			stats := m.recordSample(t, sample)
			results[idx] = stats
			m.evaluateAlert(t, sample, stats)
		}(i, target)
	}

	wg.Wait()
	return results
}

func (m *Monitor) recordSample(t config.BrokerTarget, sample Sample) BrokerStats {
	key := brokerKey(t)

	m.mu.Lock()
	h := m.history[key]
	if len(h) >= m.windowSize {
		copy(h, h[1:])
		h[len(h)-1] = sample
	} else {
		h = append(h, sample)
	}
	m.history[key] = h
	stats := ComputeStats(t, h)
	m.mu.Unlock()

	return stats
}

func (m *Monitor) evaluateAlert(t config.BrokerTarget, sample Sample, stats BrokerStats) {
	if m.onAlert == nil {
		return
	}
	if !sample.Success {
		m.onAlert(AlertEvent{
			Broker:   stats,
			Severity: "critical",
			Reason:   fmt.Sprintf("Broker %s (%s:%d) TCP port unreachable: %s", t.Name, t.Host, t.Port, sample.Error),
		})
		return
	}
	if t.MaxLatencyMs > 0 && sample.RTTMs > float64(t.MaxLatencyMs) {
		m.onAlert(AlertEvent{
			Broker:   stats,
			Severity: "warn",
			Reason:   fmt.Sprintf("Broker %s (%s:%d) TCP latency %.2f ms exceeded threshold %d ms (p95 %.2f ms)", t.Name, t.Host, t.Port, sample.RTTMs, t.MaxLatencyMs, stats.P95Ms),
		})
	}
}

// Snapshot returns current rolling statistics for all brokers without issuing new probes.
func (m *Monitor) Snapshot() []BrokerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]BrokerStats, len(m.targets))
	for i, t := range m.targets {
		out[i] = ComputeStats(t, m.history[brokerKey(t)])
	}
	return out
}

// Run probes all configured brokers immediately and then on every tick until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	_ = m.ProbeAll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = m.ProbeAll(ctx)
		}
	}
}

// ProbeSeries runs count sequential TCP probes against a single host:port for CLI diagnostics.
func ProbeSeries(ctx context.Context, target config.BrokerTarget, count int, spacing time.Duration) BrokerStats {
	if count <= 0 {
		count = 5
	}
	timeout := time.Duration(target.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	samples := make([]Sample, 0, count)
	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		samples = append(samples, ProbeOnce(ctx, target.Host, target.Port, timeout))
		if i < count-1 && spacing > 0 {
			timer := time.NewTimer(spacing)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	return ComputeStats(target, samples)
}
