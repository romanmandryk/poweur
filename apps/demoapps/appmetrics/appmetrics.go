// Package appmetrics is the demo apps' Prometheus metrics, in the same spirit
// as the OAuth bridge's (apps/oauth/bridge/metrics.go): hand-rolled text
// format, no dependencies, and nothing that identifies a person.
//
// Every counter has exactly one label whose values are listed up front. A
// value outside that list is counted as "other" instead of becoming a new
// series, so what a visitor types can never grow the registry: the label is
// chosen by the code, never copied from input. Allowed values start at zero,
// so the first event is already an increase for Prometheus.
//
// The apps serve the registry on a listener of its own (METRICS_ADDR), never
// on their public one; infra-prometheus scrapes it over the private network.
package appmetrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Other is where a value that is not on a counter's list is counted.
const Other = "other"

// Registry holds one app's metrics. The zero value is not usable; call New.
// A nil *Registry and the counters it returns are safe to use and record
// nothing, so instrumented code needs no "is metrics on" checks.
type Registry struct {
	prefix  string
	started time.Time

	mu       sync.Mutex
	counters []*Counter
	requests map[[2]string]uint64 // route, status class
	durs     map[string]*histogram
	http     bool
}

// New returns a registry whose metric names start with prefix, for example
// "poweur_hello".
func New(prefix string) *Registry {
	return &Registry{
		prefix:   prefix,
		started:  time.Now(),
		requests: map[[2]string]uint64{},
		durs:     map[string]*histogram{},
	}
}

// Counter is a monotonically increasing count split by one bounded label.
type Counter struct {
	r      *Registry
	name   string
	help   string
	label  string
	counts map[string]uint64
}

// Counter registers <prefix>_<name> with the given label and its allowed
// values. Register every counter before serving.
func (r *Registry) Counter(name, help, label string, allowed ...string) *Counter {
	if r == nil {
		return nil
	}
	c := &Counter{r: r, name: r.prefix + "_" + name, help: help, label: label, counts: make(map[string]uint64, len(allowed))}
	for _, v := range allowed {
		c.counts[v] = 0
	}
	r.mu.Lock()
	r.counters = append(r.counters, c)
	r.mu.Unlock()
	return c
}

// Inc counts one event under value, or under "other" when value is not one
// of the allowed ones.
func (c *Counter) Inc(value string) {
	if c == nil {
		return
	}
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	if _, ok := c.counts[value]; !ok {
		value = Other
	}
	c.counts[value]++
}

// Value returns the count under value (tests).
func (c *Counter) Value(value string) uint64 {
	if c == nil {
		return 0
	}
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	return c.counts[value]
}

var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

type histogram struct {
	counts []uint64
	sum    float64
	total  uint64
}

func (r *Registry) observe(route string, status int, d time.Duration) {
	class := fmt.Sprintf("%dxx", status/100)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.http = true
	r.requests[[2]string{route, class}]++
	h := r.durs[route]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(latencyBuckets))}
		r.durs[route] = h
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

// Instrument counts every request by route pattern and status class, and times
// it. The route is the mux pattern that matched ("GET /api/entries"), read
// after the handler ran, never the URL, so ids and queries cannot become
// labels. Wrap the *http.ServeMux itself: only it fills in the pattern.
func (r *Registry) Instrument(next http.Handler) http.Handler {
	if r == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, req)
		route := req.Pattern
		if route == "" {
			route = "unmatched"
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		r.observe(route, status, time.Since(start))
	})
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

// Write renders the registry in the Prometheus text format.
func (r *Registry) Write(w io.Writer) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(w, "# HELP %s_start_time_seconds Start time of the process, in unix seconds.\n# TYPE %[1]s_start_time_seconds gauge\n", r.prefix)
	fmt.Fprintf(w, "%s_start_time_seconds %d\n", r.prefix, r.started.Unix())

	counters := slices.Clone(r.counters)
	slices.SortFunc(counters, func(a, b *Counter) int { return strings.Compare(a.name, b.name) })
	for _, c := range counters {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %[1]s counter\n", c.name, c.help)
		for _, v := range sortedKeys(c.counts) {
			fmt.Fprintf(w, "%s{%s=%q} %d\n", c.name, c.label, v, c.counts[v])
		}
	}
	if !r.http {
		return
	}

	reqs := r.prefix + "_http_requests_total"
	fmt.Fprintf(w, "# HELP %s Requests by route pattern and status class.\n# TYPE %[1]s counter\n", reqs)
	keys := make([][2]string, 0, len(r.requests))
	for k := range r.requests {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int { return strings.Compare(a[0]+" "+a[1], b[0]+" "+b[1]) })
	for _, k := range keys {
		fmt.Fprintf(w, "%s{route=%q,code=%q} %d\n", reqs, k[0], k[1], r.requests[k])
	}

	dur := r.prefix + "_http_request_duration_seconds"
	fmt.Fprintf(w, "# HELP %s Request latency by route pattern.\n# TYPE %[1]s histogram\n", dur)
	for _, route := range sortedKeys(r.durs) {
		h := r.durs[route]
		for i, le := range latencyBuckets {
			fmt.Fprintf(w, "%s_bucket{route=%q,le=\"%g\"} %d\n", dur, route, le, h.counts[i])
		}
		fmt.Fprintf(w, "%s_bucket{route=%q,le=\"+Inf\"} %d\n", dur, route, h.total)
		fmt.Fprintf(w, "%s_sum{route=%q} %g\n", dur, route, h.sum)
		fmt.Fprintf(w, "%s_count{route=%q} %d\n", dur, route, h.total)
	}
}

// Handler serves the registry as GET /metrics would.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.Write(w)
	})
}

// Serve answers GET /metrics on addr until ctx ends. Bind it to a private
// address (":9464" inside a container that only the infra network reaches);
// the metrics are not authenticated. A nil registry serves nothing.
func (r *Registry) Serve(ctx context.Context, addr string) error {
	if r == nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", r.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
