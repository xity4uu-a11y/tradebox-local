package watchdog

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/xity4uu-a11y/tradebox-local/internal/config"
)

// TestHelperProcess is invoked as a cross-platform child process during supervisor tests.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("TRADEBOX_TEST_HELPER_MODE")
	if mode == "" {
		return
	}

	switch mode {
	case "sleep":
		fmt.Println("helper: running steady")
		time.Sleep(30 * time.Second)
		os.Exit(0)

	case "crash_always":
		fmt.Println("helper: simulated crash")
		os.Exit(42)

	case "crash_twice_then_stay":
		counterFile := os.Getenv("TRADEBOX_TEST_COUNTER_FILE")
		count := 0
		if raw, err := os.ReadFile(counterFile); err == nil {
			count, _ = strconv.Atoi(string(raw))
		}
		count++
		_ = os.WriteFile(counterFile, []byte(strconv.Itoa(count)), 0o644)
		if count <= 2 {
			fmt.Printf("helper: crash attempt %d\n", count)
			os.Exit(7)
		}
		fmt.Println("helper: recovered and stable")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}

func helperEnv(extra ...string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, extra...)
	return env
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func TestComputeBackoffDoublesAndCaps(t *testing.T) {
	base := time.Second
	max := 10 * time.Second
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 10 * time.Second},
		{10, 10 * time.Second},
	}
	for _, tc := range cases {
		if got := ComputeBackoff(base, max, tc.attempt); got != tc.want {
			t.Errorf("ComputeBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestSupervisorStartRestartAndStop(t *testing.T) {
	sup := NewSupervisor(Options{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess"},
		Env:     helperEnv("TRADEBOX_TEST_HELPER_MODE=sleep"),
		Policy: config.RestartPolicy{
			MaxRestartsPerHour: 5,
			BackoffBaseSec:     1,
			BackoffMaxSec:      4,
			CheckIntervalSec:   1,
		},
		BackoffBaseOverride: 50 * time.Millisecond,
		BackoffMaxOverride:  200 * time.Millisecond,
	})

	if err := sup.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = sup.Stop() }()

	waitForCondition(t, 5*time.Second, func() bool {
		st := sup.Status()
		return st.State == StateRunning && st.PID > 0
	}, "process to reach running state")

	firstPID := sup.Status().PID

	if err := sup.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		st := sup.Status()
		return st.State == StateRunning && st.PID > 0 && st.PID != firstPID
	}, "process to restart with a new PID")

	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if st := sup.Status(); st.State != StateStopped || st.PID != 0 {
		t.Fatalf("expected stopped state with pid 0, got %+v", st)
	}
}

func TestSupervisorAutoRecoveryWithBackoff(t *testing.T) {
	counterFile := filepath.Join(t.TempDir(), "count.txt")

	var mu sync.Mutex
	var events []Event

	sup := NewSupervisor(Options{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess"},
		Env: helperEnv(
			"TRADEBOX_TEST_HELPER_MODE=crash_twice_then_stay",
			"TRADEBOX_TEST_COUNTER_FILE="+counterFile,
		),
		Policy: config.RestartPolicy{
			MaxRestartsPerHour: 10,
			BackoffBaseSec:     1,
			BackoffMaxSec:      4,
			CheckIntervalSec:   1,
		},
		BackoffBaseOverride: 40 * time.Millisecond,
		BackoffMaxOverride:  200 * time.Millisecond,
		OnEvent: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})

	if err := sup.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = sup.Stop() }()

	waitForCondition(t, 8*time.Second, func() bool {
		st := sup.Status()
		return st.State == StateRunning && st.RestartCount == 2 && st.PID > 0
	}, "process to crash twice and recover into running state")

	st := sup.Status()
	if st.LastExitCode != 7 {
		t.Errorf("expected last exit code 7, got %d", st.LastExitCode)
	}

	mu.Lock()
	var backoffs []float64
	for _, ev := range events {
		if ev.Type == EventRestarting {
			backoffs = append(backoffs, ev.BackoffSec)
		}
	}
	mu.Unlock()

	if len(backoffs) != 2 {
		t.Fatalf("expected 2 restarting events, got %d", len(backoffs))
	}
	if backoffs[1] <= backoffs[0] {
		t.Errorf("expected exponential backoff growth, got %v -> %v", backoffs[0], backoffs[1])
	}
}

func TestSupervisorCrashLoopCircuitBreaker(t *testing.T) {
	var mu sync.Mutex
	circuitOpened := false

	sup := NewSupervisor(Options{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess"},
		Env:     helperEnv("TRADEBOX_TEST_HELPER_MODE=crash_always"),
		Policy: config.RestartPolicy{
			MaxRestartsPerHour: 3,
			BackoffBaseSec:     1,
			BackoffMaxSec:      2,
			CheckIntervalSec:   1,
		},
		BackoffBaseOverride: 25 * time.Millisecond,
		BackoffMaxOverride:  60 * time.Millisecond,
		OnEvent: func(ev Event) {
			if ev.Type == EventCircuitOpen {
				mu.Lock()
				circuitOpened = true
				mu.Unlock()
			}
		},
	})

	if err := sup.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = sup.Stop() }()

	waitForCondition(t, 8*time.Second, func() bool {
		return sup.Status().State == StateCircuitOpen
	}, "crash-loop circuit breaker to open")

	st := sup.Status()
	if st.RestartCount != 3 {
		t.Errorf("expected exactly 3 restarts before circuit open, got %d", st.RestartCount)
	}
	if st.LastExitCode != 42 {
		t.Errorf("expected last exit code 42, got %d", st.LastExitCode)
	}

	mu.Lock()
	opened := circuitOpened
	mu.Unlock()
	if !opened {
		t.Error("expected circuit_open alert event to be emitted")
	}
}
