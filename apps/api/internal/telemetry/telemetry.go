package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	logapi "go.opentelemetry.io/otel/log"
	metricapi "go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Event contains only deliberately selected fields; no arbitrary attributes.
type Event struct {
	Timestamp    time.Time  `json:"timestamp"`
	Kind         string     `json:"kind"`
	Action       string     `json:"action"`
	Outcome      string     `json:"outcome"`
	ErrorCode    string     `json:"error_code,omitempty"`
	Route        string     `json:"route,omitempty"`
	Method       string     `json:"method,omitempty"`
	Status       int        `json:"status,omitempty"`
	DurationMS   float64    `json:"duration_ms,omitempty"`
	ActorID      string     `json:"actor_id,omitempty"`
	IdentityMode string     `json:"identity_mode,omitempty"`
	ClientIP     string     `json:"client_ip,omitempty"`
	Stack        string     `json:"stack,omitempty"`
	Level        slog.Level `json:"-"`
}

type Runtime struct {
	exportCtx    context.Context
	exportCancel context.CancelFunc
	batch        *bufferedExporter
	cfg          Config
	consent      func(string) bool
	local        *slog.Logger
	queue        chan Event
	done         chan struct{}
	stop         chan struct{}
	once         sync.Once
	mu           sync.RWMutex
	closed       bool
	dropped      atomic.Int64
	lp           *sdklog.LoggerProvider
	mp           *sdkmetric.MeterProvider
	logger       logapi.Logger
	actions      metricapi.Int64Counter
	requests     metricapi.Int64Counter
	duration     metricapi.Float64Histogram
	gauges       metricapi.Int64Gauge
}

// New creates private providers, never global auto-instrumentation. No endpoint
// means no exporter, network lookup, queue worker, or periodic metric reader.
func New(ctx context.Context, c Config, version string, consent func(string) bool, output io.Writer) (*Runtime, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	level := slog.LevelInfo
	if c.Level != "" {
		_ = level.UnmarshalText([]byte(c.Level))
	}
	t := &Runtime{cfg: c, consent: consent, local: slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))}
	if t.cfg.HashKey == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		t.cfg.HashKey = hex.EncodeToString(b)
	}
	if c.Endpoint == "" {
		return t, nil
	}
	headers, _ := parseHeaders(c.Headers)
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	logs, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(strings.TrimRight(c.Endpoint, "/")+"/v1/logs"), otlploghttp.WithHeaders(headers), otlploghttp.WithHTTPClient(client), otlploghttp.WithRetry(otlploghttp.RetryConfig{Enabled: false}))
	if err != nil {
		return nil, errors.New("cannot initialize OTLP logs")
	}
	metrics, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(strings.TrimRight(c.Endpoint, "/")+"/v1/metrics"), otlpmetrichttp.WithHeaders(headers), otlpmetrichttp.WithHTTPClient(client), otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}))
	if err != nil {
		_ = logs.Shutdown(ctx)
		return nil, errors.New("cannot initialize OTLP metrics")
	}
	env := c.Environment
	if env == "" {
		env = "production"
	}
	res := resource.NewSchemaless(attribute.String("service.name", "poweur-relay"), attribute.String("service.version", version), attribute.String("deployment.environment.name", env))
	// One bounded queue and one batch buffer: no second SDK queue can silently
	// discard records. The worker owns the processor and performs network I/O.
	t.batch = &bufferedExporter{Exporter: &countingExporter{Exporter: logs, t: t}}
	t.lp = sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewSimpleProcessor(t.batch)))
	t.logger = t.lp.Logger("poweur")
	t.mp = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithCardinalityLimit(2000), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics, sdkmetric.WithInterval(15*time.Second), sdkmetric.WithTimeout(3*time.Second))))
	m := t.mp.Meter("poweur")
	t.actions, _ = m.Int64Counter("poweur_actions", metricapi.WithDescription("Business transitions, independent of HTTP requests"))
	t.requests, _ = m.Int64Counter("poweur_http_requests")
	t.duration, _ = m.Float64Histogram("poweur_http_duration_seconds", metricapi.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10))
	t.gauges, _ = m.Int64Gauge("poweur_state")
	_, _ = m.Int64ObservableGauge("poweur_telemetry_dropped", metricapi.WithInt64Callback(func(ctx context.Context, o metricapi.Int64Observer) error { o.Observe(t.dropped.Load()); return nil }))
	_, _ = m.Int64ObservableGauge("poweur_heartbeat_timestamp_seconds", metricapi.WithInt64Callback(func(ctx context.Context, o metricapi.Int64Observer) error { o.Observe(time.Now().Unix()); return nil }))
	t.queue = make(chan Event, 512)
	t.done = make(chan struct{})
	t.stop = make(chan struct{})
	t.exportCtx, t.exportCancel = context.WithCancel(context.Background())
	go t.run()
	return t, nil
}

// countingExporter also rechecks consent at the last boundary, after SDK batching.
type countingExporter struct {
	sdklog.Exporter
	t *Runtime
}

func (e *countingExporter) Export(ctx context.Context, rs []sdklog.Record) error {
	for i := range rs {
		var event Event
		if json.Unmarshal([]byte(rs[i].Body().AsString()), &event) == nil {
			event = e.t.sanitize(event)
			b, _ := json.Marshal(event)
			rs[i].SetBody(logapi.StringValue(string(b)))
		}
	}
	err := e.Exporter.Export(ctx, rs)
	if err != nil {
		e.t.dropped.Add(int64(len(rs)))
		e.t.local.Warn("telemetry export failed", "error_code", "otlp_export_failed")
	}
	return err
}

func (t *Runtime) Hash(actor string) string {
	mac := hmac.New(sha256.New, []byte(t.cfg.HashKey))
	_, _ = mac.Write([]byte(strings.ToLower(strings.TrimSuffix(actor, "."))))
	return hex.EncodeToString(mac.Sum(nil))
}
func (t *Runtime) sanitize(e Event) Event {
	if e.IdentityMode == "raw" && (t.consent == nil || !t.consent(e.ActorID)) {
		e.ActorID = t.Hash(e.ActorID)
		e.IdentityMode = "hashed"
		e.ClientIP = ""
	}
	if e.IdentityMode != "raw" {
		e.ClientIP = ""
	}
	return e
}

func (t *Runtime) Record(ctx context.Context, e Event, actor, ip string) {
	if t == nil {
		return
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if actor != "" {
		e.ActorID = t.Hash(actor)
		e.IdentityMode = "hashed"
		if t.consent != nil && t.consent(actor) {
			e.ActorID = actor
			e.IdentityMode = "raw"
			e.ClientIP = ip
		}
	}
	e = t.sanitize(e)
	// The JSON event is the same privacy-filtered body in stdout and OTLP.
	b, _ := json.Marshal(e)
	if e.Kind == "action" || e.Kind == "request" {
		t.local.Log(context.Background(), slog.LevelInfo, "relay event", "event", json.RawMessage(b))
	} else {
		t.local.Log(context.Background(), e.Level, "relay diagnostic", "event", json.RawMessage(b))
	}
	if t.actions != nil && e.Kind == "action" {
		t.actions.Add(ctx, 1, metricapi.WithAttributes(attribute.String("action", e.Action), attribute.String("outcome", e.Outcome)))
	}
	if t.requests != nil && e.Kind == "request" {
		attrs := metricapi.WithAttributes(attribute.String("route", e.Route), attribute.String("method", e.Method), attribute.Int("status", e.Status))
		t.requests.Add(ctx, 1, attrs)
		t.duration.Record(ctx, e.DurationMS/1000, attrs)
	}
	if t.queue == nil {
		return
	}
	if e.Kind == "diagnostic" && !t.local.Enabled(ctx, e.Level) {
		return
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return
	}
	select {
	case t.queue <- e:
	default:
		t.dropped.Add(1)
	}
}
func (t *Runtime) run() {
	defer close(t.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	flush := func() {
		ctx, cancel := context.WithTimeout(t.exportCtx, 3*time.Second)
		defer cancel()
		_ = t.batch.flush(ctx)
	}
	for {
		select {
		case e := <-t.queue:
			t.emit(e)
			if len(t.batch.records) >= 128 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-t.stop:
			for {
				select {
				case e := <-t.queue:
					t.emit(e)
					if len(t.batch.records) >= 128 {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// bufferedExporter is used only by the runtime worker. SDK record clones must
// be retained because logger.Emit reuses the original record after Export.
type bufferedExporter struct {
	sdklog.Exporter
	records []sdklog.Record
}

func (e *bufferedExporter) Export(_ context.Context, rs []sdklog.Record) error {
	for i := range rs {
		e.records = append(e.records, rs[i].Clone())
	}
	return nil
}
func (e *bufferedExporter) flush(ctx context.Context) error {
	if len(e.records) == 0 {
		return nil
	}
	rs := e.records
	e.records = nil
	return e.Exporter.Export(ctx, rs)
}

func (t *Runtime) emit(e Event) {
	e = t.sanitize(e)
	b, _ := json.Marshal(e)
	var r logapi.Record
	r.SetTimestamp(e.Timestamp)
	r.SetObservedTimestamp(time.Now())
	r.SetBody(logapi.StringValue(string(b)))
	severity := logapi.SeverityInfo
	if e.Level >= slog.LevelError {
		severity = logapi.SeverityError
	} else if e.Level >= slog.LevelWarn {
		severity = logapi.SeverityWarn
	}
	r.SetSeverity(severity)
	r.SetSeverityText(e.Level.String())
	t.logger.Emit(context.Background(), r)
}
func (t *Runtime) Gauge(ctx context.Context, name string, n int64) {
	if t != nil && t.gauges != nil {
		t.gauges.Record(ctx, n, metricapi.WithAttributes(attribute.String("state", name)))
	}
}
func (t *Runtime) Shutdown(ctx context.Context) error {
	if t == nil || t.queue == nil {
		return nil
	}
	t.once.Do(func() { t.mu.Lock(); t.closed = true; close(t.stop); t.mu.Unlock() })
	select {
	case <-t.done:
	case <-ctx.Done():
		t.exportCancel()
		<-t.done
		return errors.Join(ctx.Err(), t.lp.Shutdown(ctx), t.mp.Shutdown(ctx))
	}
	t.exportCancel()
	return errors.Join(t.lp.Shutdown(ctx), t.mp.Shutdown(ctx))
}

// ClientIP walks X-Forwarded-For from the known peer towards the first untrusted
// hop. A user-controlled leftmost entry cannot override that boundary.
func (t *Runtime) ClientIP(r *http.Request) string {
	if t == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	peer = peer.Unmap()
	trusted := func(a netip.Addr) bool {
		for _, p := range t.cfg.TrustedProxies {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if trusted(peer) {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				return peer.String()
			}
			peer = a.Unmap()
			if !trusted(peer) {
				break
			}
		}
	}
	return peer.String()
}
