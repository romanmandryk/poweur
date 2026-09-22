package telemetry

import (
	"log/slog"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

func initSentry(c Config, version string) (*sentry.Client, error) {
	if c.SentryDSN == "" {
		return nil, nil
	}
	env := c.Environment
	if env == "" {
		env = "production"
	}
	opts := sentry.ClientOptions{
		Dsn:              c.SentryDSN,
		Environment:      env,
		Release:          "poweur-relay@" + version,
		AttachStacktrace: true,
		TracesSampleRate: 0,
		MaxBreadcrumbs:   0,
		BeforeSend:       sanitizeSentryEvent,
	}
	if c.sentryTransport != nil {
		opts.Transport = c.sentryTransport
	}
	return sentry.NewClient(opts)
}

func sanitizeSentryEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}
	event.Request = nil
	event.User = sentry.User{}
	event.Modules = nil
	return event
}

func (t *Runtime) reportError(e Event) {
	if t == nil || t.sentry == nil {
		return
	}
	if e.Kind != "diagnostic" || e.Level < slog.LevelError {
		return
	}
	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Message = strings.TrimSpace(e.Action + " " + e.ErrorCode)
	event.Fingerprint = []string{e.Action, e.ErrorCode, e.Route}
	event.Tags = map[string]string{}
	if e.Action != "" {
		event.Tags["action"] = e.Action
	}
	if e.ErrorCode != "" {
		event.Tags["error_code"] = e.ErrorCode
	}
	if e.Route != "" {
		event.Tags["route"] = e.Route
	}
	if e.Stack != "" {
		event.Extra = map[string]interface{}{"stack": e.Stack}
	}
	t.sentry.CaptureEvent(event, nil, nil)
}

func (t *Runtime) flushSentry() {
	if t != nil && t.sentry != nil {
		t.sentry.Flush(2 * time.Second)
	}
}
