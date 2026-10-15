package bridge

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Metrics are plain counters in the Prometheus text format (EPIC-013).
// Labels are outcomes, never addresses or identities.
type Metrics struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]func() float64
}

// NewMetrics builds an empty registry.
func NewMetrics() *Metrics {
	return &Metrics{counters: map[string]float64{}, gauges: map[string]func() float64{}}
}

// Inc adds one to name{label="value"}.
func (m *Metrics) Inc(name, label, value string) {
	if m == nil {
		return
	}
	key := name
	if label != "" {
		key = fmt.Sprintf("%s{%s=%q}", name, label, value)
	}
	m.mu.Lock()
	m.counters[key]++
	m.mu.Unlock()
}

// Gauge registers a value read at scrape time.
func (m *Metrics) Gauge(name string, f func() float64) {
	m.mu.Lock()
	m.gauges[name] = f
	m.mu.Unlock()
}

// ServeHTTP writes the exposition.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	keys := make([]string, 0, len(m.counters))
	for k := range m.counters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s %g\n", k, m.counters[k])
	}
	gauges := make([]string, 0, len(m.gauges))
	for k := range m.gauges {
		gauges = append(gauges, k)
	}
	sort.Strings(gauges)
	fns := m.gauges
	m.mu.Unlock()
	for _, k := range gauges {
		fmt.Fprintf(&b, "%s %g\n", k, fns[k]())
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(b.String()))
}
