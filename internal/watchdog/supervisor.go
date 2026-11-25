package watchdog

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/xity4uu-a11y/tradebox-local/internal/config"
)

// State represents the current lifecycle state of the supervised terminal process.
type State string

const (
	StateStopped     State = "stopped"
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateBackoff     State = "backoff"
	StateCircuitOpen State = "circuit_open"
)

// EventType classifies supervisor lifecycle transitions for alerting and telemetry.
type EventType string

const (
	EventStarted     EventType = "started"
	EventStopped     EventType = "stopped"
	EventCrashed     EventType = "crashed"
	EventRestarting  EventType = "restarting"
	EventCircuitOpen EventType = "circuit_open"
)

// Event describes a state transition or operational incident in the supervisor.
type Event struct {
	Type         EventType `json:"type"`
	Message      string    `json:"message"`
	PID          int       `json:"pid"`
	ExitCode     int       `json:"exit_code"`
	RestartCount int       `json:"restart_count"`
	BackoffSec   float64   `json:"backoff_sec,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
}

// Status provides a point-in-time snapshot of the supervised process.
type Status struct {
	State               State      `json:"state"`
	Command             string     `json:"command"`
	Args                []string   `json:"args"`
	WorkingDir          string     `json:"working_dir"`
	PID                 int        `json:"pid"`
	UptimeSec           int64      `json:"uptime_sec"`
	TotalUptimeSec      int64      `json:"total_uptime_sec"`
	RestartCount        int        `json:"restart_count"`
	RestartsLastHour    int        `json:"restarts_last_hour"`
	MaxRestartsPerHour  int        `json:"max_restarts_per_hour"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastExitCode        int        `json:"last_exit_code"`
	LastError           string     `json:"last_error,omitempty"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	LastRestartAt       *time.Time `json:"last_restart_at,omitempty"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
	RecentOutput        []string   `json:"recent_output"`
}

// Options configures a Supervisor instance.
type Options struct {
	Command             string
	Args                []string
	WorkingDir          string
	Env                 []string
	Policy              config.RestartPolicy
	OnEvent             func(Event)
	BackoffBaseOverride time.Duration
	BackoffMaxOverride  time.Duration
	StableResetOverride time.Duration
	WindowOverride      time.Duration
}

// Supervisor manages a 24/7 child process with exponential backoff and a crash-loop circuit breaker.
type Supervisor struct {
	opts Options

	mu                  sync.Mutex
	state               State
	pid                 int
	cmd                 *exec.Cmd
	cancel              context.CancelFunc
	loopDone            chan struct{}
	startedAt           time.Time
	accumulatedUptime   time.Duration
	restartCount        int
	restartWindow       []time.Time
	consecutiveFailures int
	lastExitCode        int
	lastError           string
	lastRestartAt       time.Time
	nextRetryAt         time.Time
	recentOutput        []string
	maxOutputLines      int
}

// NewSupervisor creates a new process supervisor in the stopped state.
func NewSupervisor(opts Options) *Supervisor {
	if opts.Policy.MaxRestartsPerHour <= 0 {
		opts.Policy.MaxRestartsPerHour = 12
	}
	if opts.Policy.BackoffBaseSec <= 0 {
		opts.Policy.BackoffBaseSec = 1
	}
	if opts.Policy.BackoffMaxSec <= 0 {
		opts.Policy.BackoffMaxSec = 60
	}
	if opts.Policy.StableResetSec <= 0 {
		opts.Policy.StableResetSec = 60
	}

	argsCopy := make([]string, len(opts.Args))
	copy(argsCopy, opts.Args)
	opts.Args = argsCopy

	return &Supervisor{
		opts:           opts,
		state:          StateStopped,
		maxOutputLines: 40,
		recentOutput:   make([]string, 0, 40),
	}
}

func (s *Supervisor) backoffBase() time.Duration {
	if s.opts.BackoffBaseOverride > 0 {
		return s.opts.BackoffBaseOverride
	}
	return time.Duration(s.opts.Policy.BackoffBaseSec) * time.Second
}

func (s *Supervisor) backoffMax() time.Duration {
	if s.opts.BackoffMaxOverride > 0 {
		return s.opts.BackoffMaxOverride
	}
	return time.Duration(s.opts.Policy.BackoffMaxSec) * time.Second
}

func (s *Supervisor) stableReset() time.Duration {
	if s.opts.StableResetOverride > 0 {
		return s.opts.StableResetOverride
	}
	return time.Duration(s.opts.Policy.StableResetSec) * time.Second
}

func (s *Supervisor) rateWindow() time.Duration {
	if s.opts.WindowOverride > 0 {
		return s.opts.WindowOverride
	}
	return time.Hour
}

// ComputeBackoff returns the capped exponential backoff duration for a zero-based failure attempt.
func ComputeBackoff(base, max time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if max < base {
		max = base
	}
	if attempt <= 0 {
		return base
	}
	delay := base
	for i := 0; i < attempt; i++ {
		if delay >= max/2 {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}

// Start launches the supervision loop if not already active. Calling Start on a
// circuit-open supervisor resets the breaker and resumes supervision.
func (s *Supervisor) Start() error {
	if strings.TrimSpace(s.opts.Command) == "" {
		return errors.New("terminal command is empty")
	}

	s.mu.Lock()
	if s.state == StateRunning || s.state == StateStarting || s.state == StateBackoff {
		s.mu.Unlock()
		return nil
	}

	// Reset circuit breaker and transient error state on explicit operator Start.
	if s.state == StateCircuitOpen {
		s.restartWindow = nil
		s.consecutiveFailures = 0
	}
	s.lastError = ""
	s.nextRetryAt = time.Time{}
	s.state = StateStarting

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel = cancel
	s.loopDone = done
	s.mu.Unlock()

	go s.superviseLoop(ctx, done)
	return nil
}

// Stop gracefully terminates the supervised process and waits for the supervision loop to exit.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	cancel := s.cancel
	done := s.loopDone
	s.cancel = nil
	s.loopDone = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	s.mu.Lock()
	if s.state != StateStopped {
		s.state = StateStopped
		s.pid = 0
		s.nextRetryAt = time.Time{}
	}
	s.mu.Unlock()
	return nil
}

// Restart stops any running instance (or clears a tripped circuit breaker) and starts fresh.
func (s *Supervisor) Restart() error {
	if err := s.Stop(); err != nil {
		return err
	}
	s.mu.Lock()
	s.restartWindow = nil
	s.consecutiveFailures = 0
	s.mu.Unlock()
	return s.Start()
}

// Status returns a thread-safe snapshot of the supervisor state.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.pruneWindowLocked(now)

	var currentUptime time.Duration
	if s.state == StateRunning && !s.startedAt.IsZero() {
		currentUptime = now.Sub(s.startedAt)
	}
	totalUptime := s.accumulatedUptime + currentUptime

	argsCopy := make([]string, len(s.opts.Args))
	copy(argsCopy, s.opts.Args)

	outCopy := make([]string, len(s.recentOutput))
	copy(outCopy, s.recentOutput)

	st := Status{
		State:               s.state,
		Command:             s.opts.Command,
		Args:                argsCopy,
		WorkingDir:          s.opts.WorkingDir,
		PID:                 s.pid,
		UptimeSec:           int64(currentUptime.Seconds()),
		TotalUptimeSec:      int64(totalUptime.Seconds()),
		RestartCount:        s.restartCount,
		RestartsLastHour:    len(s.restartWindow),
		MaxRestartsPerHour:  s.opts.Policy.MaxRestartsPerHour,
		ConsecutiveFailures: s.consecutiveFailures,
		LastExitCode:        s.lastExitCode,
		LastError:           s.lastError,
		RecentOutput:        outCopy,
	}
	if !s.startedAt.IsZero() {
		t := s.startedAt
		st.StartedAt = &t
	}
	if !s.lastRestartAt.IsZero() {
		t := s.lastRestartAt
		st.LastRestartAt = &t
	}
	if !s.nextRetryAt.IsZero() && s.state == StateBackoff {
		t := s.nextRetryAt
		st.NextRetryAt = &t
	}
	return st
}

func (s *Supervisor) superviseLoop(ctx context.Context, done chan struct{}) {
	defer close(done)

	for {
		if ctx.Err() != nil {
			s.markStopped()
			return
		}

		cmd := exec.Command(s.opts.Command, s.opts.Args...)
		if s.opts.WorkingDir != "" {
			cmd.Dir = s.opts.WorkingDir
		}
		if len(s.opts.Env) > 0 {
			cmd.Env = s.opts.Env
		}

		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw

		readerDone := make(chan struct{})
		go s.consumeOutput(pr, readerDone)

		startErr := cmd.Start()
		if startErr != nil {
			_ = pw.Close()
			<-readerDone

			if !s.handleFailure(ctx, -1, fmt.Sprintf("failed to start %q: %v", s.opts.Command, startErr), 0) {
				return
			}
			continue
		}

		now := time.Now()
		pid := cmd.Process.Pid

		s.mu.Lock()
		s.cmd = cmd
		s.pid = pid
		s.startedAt = now
		s.state = StateRunning
		s.lastError = ""
		s.nextRetryAt = time.Time{}
		restarts := s.restartCount
		s.mu.Unlock()

		s.emit(Event{
			Type:         EventStarted,
			Message:      fmt.Sprintf("Process started (%s, pid %d)", s.opts.Command, pid),
			PID:          pid,
			RestartCount: restarts,
			Timestamp:    now,
		})

		waitDone := make(chan error, 1)
		go func() {
			err := cmd.Wait()
			_ = pw.Close()
			waitDone <- err
		}()

		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-waitDone
			<-readerDone
			s.finalizeRunUptime(now)
			s.markStopped()
			s.emit(Event{
				Type:         EventStopped,
				Message:      fmt.Sprintf("Process stopped cleanly (pid %d)", pid),
				PID:          pid,
				RestartCount: restarts,
				Timestamp:    time.Now(),
			})
			return

		case waitErr := <-waitDone:
			<-readerDone
			runDuration := time.Since(now)
			s.finalizeRunUptime(now)

			exitCode := 0
			errMsg := "process exited unexpectedly with code 0"
			if waitErr != nil {
				exitCode = 1
				var exitErr *exec.ExitError
				if errors.As(waitErr, &exitErr) {
					exitCode = exitErr.ExitCode()
				}
				errMsg = fmt.Sprintf("process exited with code %d: %v", exitCode, waitErr)
			}

			if ctx.Err() != nil {
				s.markStopped()
				return
			}

			if !s.handleFailure(ctx, exitCode, errMsg, runDuration) {
				return
			}
		}
	}
}

func (s *Supervisor) handleFailure(ctx context.Context, exitCode int, errMsg string, runDuration time.Duration) bool {
	now := time.Now()

	s.mu.Lock()
	s.cmd = nil
	s.pid = 0
	s.lastExitCode = exitCode
	s.lastError = errMsg

	if runDuration >= s.stableReset() {
		s.consecutiveFailures = 0
	}

	s.pruneWindowLocked(now)
	restartsInWindow := len(s.restartWindow)
	maxPerHour := s.opts.Policy.MaxRestartsPerHour

	if restartsInWindow >= maxPerHour {
		s.state = StateCircuitOpen
		s.nextRetryAt = time.Time{}
		totalRestarts := s.restartCount
		s.mu.Unlock()

		s.emit(Event{
			Type:         EventCircuitOpen,
			Message:      fmt.Sprintf("Crash-loop circuit breaker opened after %d restarts in window (last exit %d: %s)", restartsInWindow, exitCode, errMsg),
			ExitCode:     exitCode,
			RestartCount: totalRestarts,
			Timestamp:    now,
		})
		return false
	}

	attempt := s.consecutiveFailures
	delay := ComputeBackoff(s.backoffBase(), s.backoffMax(), attempt)

	s.consecutiveFailures++
	s.restartCount++
	s.restartWindow = append(s.restartWindow, now)
	s.lastRestartAt = now
	s.nextRetryAt = now.Add(delay)
	s.state = StateBackoff
	totalRestarts := s.restartCount
	s.mu.Unlock()

	s.emit(Event{
		Type:         EventCrashed,
		Message:      errMsg,
		ExitCode:     exitCode,
		RestartCount: totalRestarts,
		Timestamp:    now,
	})

	s.emit(Event{
		Type:         EventRestarting,
		Message:      fmt.Sprintf("Scheduling restart #%d in %.2fs", totalRestarts, delay.Seconds()),
		ExitCode:     exitCode,
		RestartCount: totalRestarts,
		BackoffSec:   delay.Seconds(),
		Timestamp:    now,
	})

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		s.markStopped()
		return false
	case <-timer.C:
		return true
	}
}

func (s *Supervisor) finalizeRunUptime(started time.Time) {
	s.mu.Lock()
	if !started.IsZero() {
		s.accumulatedUptime += time.Since(started)
	}
	s.cmd = nil
	s.pid = 0
	s.mu.Unlock()
}

func (s *Supervisor) markStopped() {
	s.mu.Lock()
	s.state = StateStopped
	s.cmd = nil
	s.pid = 0
	s.nextRetryAt = time.Time{}
	s.mu.Unlock()
}

func (s *Supervisor) pruneWindowLocked(now time.Time) {
	cutoff := now.Add(-s.rateWindow())
	idx := 0
	for _, ts := range s.restartWindow {
		if ts.After(cutoff) {
			s.restartWindow[idx] = ts
			idx++
		}
	}
	s.restartWindow = s.restartWindow[:idx]
}

func (s *Supervisor) consumeOutput(r io.Reader, done chan struct{}) {
	defer close(done)
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 16*1024)
	scanner.Buffer(buf, 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		s.mu.Lock()
		if len(s.recentOutput) >= s.maxOutputLines {
			copy(s.recentOutput, s.recentOutput[1:])
			s.recentOutput[len(s.recentOutput)-1] = line
		} else {
			s.recentOutput = append(s.recentOutput, line)
		}
		s.mu.Unlock()
	}
}

func (s *Supervisor) emit(ev Event) {
	if s.opts.OnEvent != nil {
		s.opts.OnEvent(ev)
	}
}
