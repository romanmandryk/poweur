package bridge

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Metrics are counters in the Prometheus text format, hand-rolled to keep the
// bridge's dependencies few. They carry no identities, client ids or
// addresses: route patterns, status classes and event names only.
type metrics struct {
	mu          sync.Mutex
	started     time.Time
	requests    map[[2]string]uint64 // route, status class
	durations   map[string]*histogram
	rateLimited map[string]uint64 // route
	events      map[string]uint64 // audit event
}

var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}

type histogram struct {
	counts []uint64 // per bucket, cumulative on write
	sum    float64
	total  uint64
}

func newMetrics(now time.Time) *metrics {
	return &metrics{
		started:     now,
		requests:    map[[2]string]uint64{},
		durations:   map[string]*histogram{},
		rateLimited: map[string]uint64{},
		events:      map[string]uint64{},
	}
}

func (m *metrics) observe(route string, status int, d time.Duration) {
	class := fmt.Sprintf("%dxx", status/100)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[[2]string{route, class}]++
	h := m.durations[route]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(latencyBuckets))}
		m.durations[route] = h
	}
	secs := d.Seconds()
	for i, le := range latencyBuckets {
		if secs <= le {
			h.counts[i]++
		}
	}
	h.sum += secs
	h.total++
}

func (m *metrics) limited(route string) {
	m.mu.Lock()
	m.rateLimited[route]++
	m.mu.Unlock()
}

func (m *metrics) event(name string) {
	m.mu.Lock()
	m.events[name]++
	m.mu.Unlock()
}

func (m *metrics) write(w io.Writer, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fmt.Fprintf(w, "# HELP poweur_oauth_info Build information.\n# TYPE poweur_oauth_info gauge\n")
	fmt.Fprintf(w, "poweur_oauth_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP poweur_oauth_uptime_seconds Seconds since start.\n# TYPE poweur_oauth_uptime_seconds gauge\n")
	fmt.Fprintf(w, "poweur_oauth_uptime_seconds %.0f\n", now.Sub(m.started).Seconds())

	fmt.Fprintf(w, "# HELP poweur_oauth_http_requests_total Requests by route and status class.\n# TYPE poweur_oauth_http_requests_total counter\n")
	keys := make([][2]string, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int { return strings.Compare(a[0]+a[1], b[0]+b[1]) })
	for _, k := range keys {
		fmt.Fprintf(w, "poweur_oauth_http_requests_total{route=%q,code=%q} %d\n", k[0], k[1], m.requests[k])
	}

	fmt.Fprintf(w, "# HELP poweur_oauth_http_request_duration_seconds Request latency by route.\n# TYPE poweur_oauth_http_request_duration_seconds histogram\n")
	for _, route := range sortedKeys(m.durations) {
		h := m.durations[route]
		for i, le := range latencyBuckets {
			fmt.Fprintf(w, "poweur_oauth_http_request_duration_seconds_bucket{route=%q,le=\"%g\"} %d\n", route, le, h.counts[i])
		}
		fmt.Fprintf(w, "poweur_oauth_http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", route, h.total)
		fmt.Fprintf(w, "poweur_oauth_http_request_duration_seconds_sum{route=%q} %g\n", route, h.sum)
		fmt.Fprintf(w, "poweur_oauth_http_request_duration_seconds_count{route=%q} %d\n", route, h.total)
	}

	fmt.Fprintf(w, "# HELP poweur_oauth_rate_limited_total Requests refused by a rate limit, by route.\n# TYPE poweur_oauth_rate_limited_total counter\n")
	for _, route := range sortedKeys(m.rateLimited) {
		fmt.Fprintf(w, "poweur_oauth_rate_limited_total{route=%q} %d\n", route, m.rateLimited[route])
	}

	fmt.Fprintf(w, "# HELP poweur_oauth_events_total Security-relevant events (the audit log), by name.\n# TYPE poweur_oauth_events_total counter\n")
	for _, name := range sortedKeys(m.events) {
		fmt.Fprintf(w, "poweur_oauth_events_total{event=%q} %d\n", name, m.events[name])
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// routeOf is the request's mux pattern (no ids), or "unmatched".
func routeOf(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

// rateRoute labels a request refused before routing.
func rateRoute(r *http.Request) string {
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/t/"):
		return "/t/{id}/..."
	case strings.HasPrefix(p, "/developers"), strings.HasPrefix(p, "/account"):
		return "console"
	default:
		return p
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// MetricsHandler serves the metrics. Mount it on a listener that is not
// public (OAUTH_METRICS_ADDR).
func (s *Server) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		s.metrics.write(w, s.now())
	})
}
