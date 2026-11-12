package metrics

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// MetricType represents Prometheus metric types.
type MetricType string

const (
	TypeCounter MetricType = "counter"
	TypeGauge   MetricType = "gauge"
)

// Registry holds metric collectors in a thread-safe registry.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*CounterVec
	gauges   map[string]*GaugeVec
	role     string
}

// Global default registry.
var DefaultRegistry = NewRegistry("default")

// NewRegistry creates a new Prometheus metric registry.
func NewRegistry(role string) *Registry {
	return &Registry{
		counters: make(map[string]*CounterVec),
		gauges:   make(map[string]*GaugeVec),
		role:     role,
	}
}

// SetRole sets the runtime role tag for this registry.
func (r *Registry) SetRole(role string) {
	r.mu.Lock()
	r.role = role
	r.mu.Unlock()
}

// CounterVec manages multiple labeled counters.
type CounterVec struct {
	name   string
	help   string
	mu     sync.RWMutex
	values map[string]*uint64
}

// GaugeVec manages multiple labeled gauges.
type GaugeVec struct {
	name   string
	help   string
	mu     sync.RWMutex
	values map[string]*int64
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		// Escape values
		v := strings.ReplaceAll(labels[k], `\`, `\\`)
		v = strings.ReplaceAll(v, `"`, `\"`)
		v = strings.ReplaceAll(v, "\n", `\n`)
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`, k, v))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

// Counter registers or retrieves a CounterVec.
func (r *Registry) Counter(name, help string) *CounterVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c := &CounterVec{
		name:   name,
		help:   help,
		values: make(map[string]*uint64),
	}
	r.counters[name] = c
	return c
}

// Gauge registers or retrieves a GaugeVec.
func (r *Registry) Gauge(name, help string) *GaugeVec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &GaugeVec{
		name:   name,
		help:   help,
		values: make(map[string]*int64),
	}
	r.gauges[name] = g
	return g
}

// Inc increments a counter value by 1.
func (c *CounterVec) Inc(labels map[string]string) {
	c.Add(1, labels)
}

// Add adds delta to counter value.
func (c *CounterVec) Add(delta uint64, labels map[string]string) {
	key := formatLabels(labels)
	c.mu.RLock()
	val, ok := c.values[key]
	c.mu.RUnlock()
	if !ok {
		c.mu.Lock()
		val, ok = c.values[key]
		if !ok {
			var n uint64
			val = &n
			c.values[key] = val
		}
		c.mu.Unlock()
	}
	atomic.AddUint64(val, delta)
}

// Set sets a gauge value.
func (g *GaugeVec) Set(val int64, labels map[string]string) {
	key := formatLabels(labels)
	g.mu.RLock()
	v, ok := g.values[key]
	g.mu.RUnlock()
	if !ok {
		g.mu.Lock()
		v, ok = g.values[key]
		if !ok {
			var n int64
			v = &n
			g.values[key] = v
		}
		g.mu.Unlock()
	}
	atomic.StoreInt64(v, val)
}

// WritePrometheus writes all metrics in Prometheus text format (0.0.4).
func (r *Registry) WritePrometheus(w io.Writer) error {
	r.mu.RLock()
	role := r.role
	r.mu.RUnlock()

	// 1. Process & runtime gauges
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(w, "# HELP znero_process_up Process running state\n")
	fmt.Fprintf(w, "# TYPE znero_process_up gauge\n")
	fmt.Fprintf(w, "znero_process_up{role=\"%s\"} 1\n", role)

	fmt.Fprintf(w, "# HELP go_goroutines Number of goroutines\n")
	fmt.Fprintf(w, "# TYPE go_goroutines gauge\n")
	fmt.Fprintf(w, "go_goroutines{role=\"%s\"} %d\n", role, runtime.NumGoroutine())

	fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes Number of bytes allocated and still in use\n")
	fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes gauge\n")
	fmt.Fprintf(w, "go_memstats_alloc_bytes{role=\"%s\"} %d\n", role, mem.Alloc)

	r.mu.RLock()
	counterNames := make([]string, 0, len(r.counters))
	for n := range r.counters {
		counterNames = append(counterNames, n)
	}
	sort.Strings(counterNames)

	gaugeNames := make([]string, 0, len(r.gauges))
	for n := range r.gauges {
		gaugeNames = append(gaugeNames, n)
	}
	sort.Strings(gaugeNames)
	r.mu.RUnlock()

	// Write counters
	for _, n := range counterNames {
		r.mu.RLock()
		c := r.counters[n]
		r.mu.RUnlock()
		if c == nil {
			continue
		}
		fmt.Fprintf(w, "# HELP %s %s\n", c.name, c.help)
		fmt.Fprintf(w, "# TYPE %s counter\n", c.name)
		c.mu.RLock()
		keys := make([]string, 0, len(c.values))
		for k := range c.values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := atomic.LoadUint64(c.values[k])
			fmt.Fprintf(w, "%s%s %d\n", c.name, k, v)
		}
		c.mu.RUnlock()
	}

	// Write gauges
	for _, n := range gaugeNames {
		r.mu.RLock()
		g := r.gauges[n]
		r.mu.RUnlock()
		if g == nil {
			continue
		}
		fmt.Fprintf(w, "# HELP %s %s\n", g.name, g.help)
		fmt.Fprintf(w, "# TYPE %s gauge\n", g.name)
		g.mu.RLock()
		keys := make([]string, 0, len(g.values))
		for k := range g.values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := atomic.LoadInt64(g.values[k])
			fmt.Fprintf(w, "%s%s %d\n", g.name, k, v)
		}
		g.mu.RUnlock()
	}

	return nil
}

// HTTPHandler returns an http.Handler that serves Prometheus metrics.
// Enforces loopback-only or METRICS_TOKEN bearer auth protection.
func (r *Registry) HTTPHandler() http.Handler {
	expectedToken := strings.TrimSpace(os.Getenv("METRICS_TOKEN"))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			host = req.RemoteAddr
		}
		ip := net.ParseIP(host)
		isLoopback := ip != nil && ip.IsLoopback()

		if !isLoopback {
			if expectedToken == "" {
				http.Error(w, "Forbidden: remote metrics access disabled without METRICS_TOKEN", http.StatusForbidden)
				return
			}
			auth := req.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != expectedToken {
				http.Error(w, "Unauthorized: invalid metrics token", http.StatusUnauthorized)
				return
			}
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = r.WritePrometheus(w)
	})
}
