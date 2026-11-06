package health

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Check statuses.
const (
	StatusUp   = "up"
	StatusDown = "down"
	StatusOK   = "ok"
)

// LiveStatus is the /healthz payload: the process is running.
type LiveStatus struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// CheckResult is the outcome of one dependency check. Reason is deliberately generic
// ("timeout" or "unreachable"); the detailed error is only written to the server log.
type CheckResult struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Reason    string `json:"reason,omitempty"`
}

// Report is the /readyz payload. Status is "up" only if every check is up.
type Report struct {
	Status    string                 `json:"status"`
	Checks    map[string]CheckResult `json:"checks"`
	CheckedAt time.Time              `json:"checked_at"`
}

// Ready reports whether every dependency is up.
func (r Report) Ready() bool { return r.Status == StatusUp }

// Service runs the liveness and readiness checks.
type Service struct {
	version string
	started time.Time
	probes  []Probe
	timeout time.Duration
	logger  *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	lastSeen map[string]string // last status per probe, to log only transitions
}

// NewService creates the service. timeout bounds each probe individually.
func NewService(version string, timeout time.Duration, logger *slog.Logger, probes ...Probe) *Service {
	return &Service{
		version:  version,
		started:  time.Now(),
		probes:   probes,
		timeout:  timeout,
		logger:   logger,
		now:      time.Now,
		lastSeen: make(map[string]string, len(probes)),
	}
}

// Live reports that the process is alive. It never touches a dependency.
func (s *Service) Live() LiveStatus {
	return LiveStatus{Status: StatusOK, Version: s.version, UptimeSeconds: int64(s.now().Sub(s.started).Seconds())}
}

// Ready checks all probes concurrently, each bounded by the configured timeout.
func (s *Service) Ready(ctx context.Context) Report {
	results := make([]CheckResult, len(s.probes))
	errs := make([]error, len(s.probes))

	var wg sync.WaitGroup
	for i, p := range s.probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.runProbe(ctx, p)
		}()
	}
	wg.Wait()

	report := Report{Status: StatusUp, Checks: make(map[string]CheckResult, len(s.probes)), CheckedAt: s.now().UTC()}
	for i, p := range s.probes {
		report.Checks[p.Name()] = results[i]
		if results[i].Status != StatusUp {
			report.Status = StatusDown
		}
		s.logTransition(ctx, p.Name(), results[i], errs[i])
	}
	return report
}

func (s *Service) runProbe(ctx context.Context, p Probe) (CheckResult, error) {
	cctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	start := time.Now()
	err := p.Ping(cctx)
	latency := time.Since(start).Milliseconds()
	if err == nil {
		return CheckResult{Status: StatusUp, LatencyMS: latency}, nil
	}
	reason := "unreachable"
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(cctx.Err(), context.DeadlineExceeded) {
		reason = "timeout"
	}
	return CheckResult{Status: StatusDown, LatencyMS: latency, Reason: reason}, err
}

// logTransition logs when a dependency changes state, with the real error for operators.
func (s *Service) logTransition(ctx context.Context, name string, res CheckResult, err error) {
	s.mu.Lock()
	prev, seen := s.lastSeen[name]
	s.lastSeen[name] = res.Status
	s.mu.Unlock()

	switch {
	case res.Status == StatusDown && prev != StatusDown:
		s.logger.WarnContext(ctx, "dependency is down", slog.String("dependency", name),
			slog.String("reason", res.Reason), slog.String("error", err.Error()))
	case res.Status == StatusUp && seen && prev == StatusDown:
		s.logger.InfoContext(ctx, "dependency recovered", slog.String("dependency", name))
	}
}
