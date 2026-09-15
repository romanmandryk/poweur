package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/poweur/api/internal/files"
	"github.com/poweur/api/internal/telemetry"
	idpkg "github.com/poweur/identity"
)

const analyticsPath = "poweur-sys/relay/analytics.json"

type analyticsPreference struct {
	Version   int    `json:"version"`
	Granted   bool   `json:"granted"`
	UpdatedAt string `json:"updated_at"`
}

func parseAnalytics(b []byte) (analyticsPreference, error) {
	var p analyticsPreference
	var required map[string]json.RawMessage
	if len(b) > 4096 || json.Unmarshal(b, &required) != nil || required["granted"] == nil || string(required["granted"]) == "null" {
		return p, fmt.Errorf("invalid analytics preference")
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("invalid analytics preference")
	}
	if p.Version != 1 {
		return p, fmt.Errorf("unsupported analytics preference version")
	}
	if _, err := time.Parse(time.RFC3339, p.UpdatedAt); err != nil {
		return p, fmt.Errorf("invalid analytics preference timestamp")
	}
	return p, nil
}
func (s *Server) consentGranted(actor string) bool {
	if s.filesProvider == nil || !s.identities.Exists(actor) {
		return false
	}
	f, err := s.filesProvider.OpenFile(context.Background(), actor, analyticsPath, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return false
	}
	p, err := parseAnalytics(b)
	return err == nil && p.Granted
}

// StartTelemetry is explicit so an in-process relay test doesn't acquire global
// loggers/exporter workers. Production calls it before serving requests.
func (s *Server) StartTelemetry(ctx context.Context, output io.Writer) error {
	version, _, _ := s.releaseInfo()
	t, err := telemetry.New(ctx, s.cfg.Telemetry, version, s.consentGranted, output)
	if err != nil {
		return err
	}
	s.telemetry = t
	for _, code := range s.startupFailures {
		t.Record(ctx, telemetry.Event{Kind: "diagnostic", Action: "storage.open", Outcome: "failure", ErrorCode: code, Level: slog.LevelWarn}, "", "")
	}
	s.event(context.Background(), "startup", "success")
	s.sampleTelemetry()
	return nil
}
func (s *Server) Close(ctx context.Context) error {
	s.closeOnce.Do(func() { close(s.stop) })
	s.event(context.Background(), "shutdown", "success")
	return s.telemetry.Shutdown(ctx)
}

type requestTelemetry struct {
	ip      string
	actor   string
	direct  bool
	action  string
	detail  string
	changes []string
}
type telemetryKey struct{}

func verifiedActor(r *http.Request, actor string) {
	if st, ok := r.Context().Value(telemetryKey{}).(*requestTelemetry); ok {
		st.actor = strings.ToLower(actor)
		st.direct = true
	}
}
func requestAction(r *http.Request, action string) {
	if st, ok := r.Context().Value(telemetryKey{}).(*requestTelemetry); ok {
		st.action = action
	}
}

// requestDetail sets the bounded detail label of the request's business action.
func requestDetail(r *http.Request, detail string) {
	if st, ok := r.Context().Value(telemetryKey{}).(*requestTelemetry); ok {
		st.detail = detail
	}
}

// settingsChanged queues one settings.change action per field, emitted only
// when the write itself succeeds.
func settingsChanged(r *http.Request, fields []string) {
	if st, ok := r.Context().Value(telemetryKey{}).(*requestTelemetry); ok {
		st.changes = fields
	}
}

func (s *Server) event(ctx context.Context, action, outcome string) {
	s.eventDetail(ctx, action, "", outcome)
}
func (s *Server) eventDetail(ctx context.Context, action, detail, outcome string) {
	actor, ip := "", ""
	if st, ok := ctx.Value(telemetryKey{}).(*requestTelemetry); ok {
		actor = st.actor
		if st.direct {
			ip = st.ip
		}
	}
	s.telemetry.Record(ctx, telemetry.Event{Kind: "action", Action: action, Detail: detail, Outcome: outcome}, actor, ip)
	if outcome == "failure" {
		s.telemetry.Record(ctx, telemetry.Event{Kind: "diagnostic", Action: action, Outcome: outcome, ErrorCode: "operation_failed", Level: slog.LevelWarn}, actor, ip)
	}
}

// responseTelemetry supports streaming through ResponseController/Unwrap and
// Flush. It never captures response bodies or URL-derived error details.
type responseTelemetry struct {
	http.ResponseWriter
	status int
	code   string
	state  *requestTelemetry
}

func (w *responseTelemetry) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseTelemetry) WriteHeader(n int) {
	if n >= 100 && n < 200 {
		w.ResponseWriter.WriteHeader(n)
		return
	}
	if w.status == 0 {
		w.status = n
		w.ResponseWriter.WriteHeader(n)
	}
}
func (w *responseTelemetry) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *responseTelemetry) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *responseTelemetry) telemetryError(code string) { w.code = code }

func (s *Server) instrument(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		_, route := mux.Handler(r)
		if route == "" || (route == "GET /" && r.URL.Path != "/") {
			route = "unmatched"
		}
		method := r.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "PROPFIND", "PROPPATCH", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK":
		default:
			method = "OTHER"
		}
		st := &requestTelemetry{action: routeActions[route], ip: s.telemetry.ClientIP(r)}
		r = r.WithContext(context.WithValue(r.Context(), telemetryKey{}, st))
		rw := &responseTelemetry{ResponseWriter: w, state: st}
		defer func() {
			stack := ""
			if recovered := recover(); recovered != nil {
				var pcs [20]uintptr
				n := runtime.Callers(3, pcs[:])
				frames := runtime.CallersFrames(pcs[:n])
				for {
					f, more := frames.Next()
					if strings.Contains(f.Function, "github.com/poweur/") {
						stack += f.Function + "\n"
					}
					if !more {
						break
					}
				}
				rw.code = "panic"
				if rw.status == 0 {
					writeError(rw, 500, "internal_error", "internal server error")
				}
			}
			if rw.status == 0 {
				rw.status = 200
			}
			outcome := "success"
			if rw.status >= 500 {
				outcome = "failure"
			} else if rw.status >= 400 {
				outcome = "rejected"
			}
			ip := ""
			if st.direct {
				ip = s.telemetry.ClientIP(r)
			}
			s.telemetry.Record(context.Background(), telemetry.Event{Kind: "request", Action: "http.request", Outcome: outcome, Route: route, Method: method, Status: rw.status, ErrorCode: rw.code, DurationMS: float64(time.Since(start).Microseconds()) / 1000}, st.actor, ip)
			if st.action != "" {
				s.eventDetail(r.Context(), st.action, st.detail, outcome)
			}
			if outcome == "success" {
				for _, field := range st.changes {
					s.eventDetail(r.Context(), "settings.change", field, outcome)
				}
			}
			if rw.status >= 500 || stack != "" {
				code := rw.code
				if code == "" {
					code = "http_5xx"
				}
				s.telemetry.Record(context.Background(), telemetry.Event{Kind: "diagnostic", Action: "server.error", Outcome: "failure", Route: route, ErrorCode: code, Stack: stack, Level: slog.LevelError}, st.actor, ip)
			}
		}()
		next.ServeHTTP(rw, r)
	})
}

func (s *Server) sampleTelemetry() {
	if s.telemetry == nil {
		return
	}
	ids := s.identities.Names()
	s.telemetry.Gauge(context.Background(), "hosted_identities", int64(len(ids)))
	s.telemetry.Gauge(context.Background(), "inbox_depth", s.inbox.Depth())
	if s.filesProvider != nil {
		for state, n := range adoptionCounts(ids, s.readSysDoc) {
			s.telemetry.Gauge(context.Background(), state, n)
		}
		var used int64
		for _, id := range ids {
			n, err := s.filesProvider.UsedBytes(context.Background(), id)
			if err != nil {
				s.event(context.Background(), "storage.sample", "failure")
				return
			}
			used += n
		}
		s.telemetry.Gauge(context.Background(), "storage_bytes", used)
	}
}

// readSysDoc reads a relay-readable system document, or nil when it is absent,
// unreadable or oversized. Never used for poweur-sys/private.
func (s *Server) readSysDoc(identity, path string) []byte {
	if s.filesProvider == nil {
		return nil
	}
	f, err := s.filesProvider.OpenFile(context.Background(), identity, path, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSysDocBytes+1))
	if err != nil || len(b) > maxSysDocBytes {
		return nil
	}
	return b
}

// messageKind buckets an envelope type into a bounded metric value: plain
// chat, a registered sys.* type verbatim, or "app" for application types.
// The type is plaintext routing metadata; payloads are never inspected.
func messageKind(t string) string {
	t = idpkg.NormalizeMessageType(t)
	switch {
	case t == idpkg.MsgTypeChatText:
		return "chat"
	case idpkg.IsKnownSystemType(t):
		return t
	case idpkg.IsSystemType(t):
		return "sys.other"
	default:
		return "app"
	}
}

// settingsDocs lists the top-level fields counted per validated settings
// document. Unknown fields are preserved on disk but never become labels.
var settingsDocs = map[string]struct {
	prefix string
	fields []string
}{
	files.SysPublic + "/profile.json":     {"profile", []string{"display_name", "avatar", "bio", "links", "locale"}},
	files.SysRelay + "/inbox-policy.json": {"inbox", []string{"mode", "anonymous", "read_receipts"}},
	analyticsPath:                         {"analytics", []string{"granted"}},
}

// settingsChanges names the known fields that differ between two versions of
// a settings document. A missing old document counts every field now set;
// absent, null and empty values are all "unset".
func settingsChanges(clean string, old, next []byte) []string {
	doc, ok := settingsDocs[clean]
	if !ok {
		return nil
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(old, &before)
	if json.Unmarshal(next, &after) != nil {
		return nil
	}
	var out []string
	for _, f := range doc.fields {
		if !reflect.DeepEqual(jsonValue(before[f]), jsonValue(after[f])) {
			out = append(out, doc.prefix+"."+f)
		}
	}
	return out
}

func jsonValue(raw json.RawMessage) any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
	case []any:
		if len(x) == 0 {
			return nil
		}
	case map[string]any:
		if len(x) == 0 {
			return nil
		}
	}
	return v
}

// adoptionStates are always reported, so each series exists even at zero.
var adoptionStates = []string{
	"adopt_profile_display_name", "adopt_profile_bio", "adopt_profile_avatar", "adopt_profile_links", "adopt_profile_locale",
	"adopt_inbox_default", "adopt_inbox_open", "adopt_inbox_contacts_only", "adopt_inbox_contacts_and_requests",
	"adopt_inbox_anonymous", "adopt_read_receipts_off", "adopt_analytics_granted", "adopt_contacts",
}

var inboxModeStates = map[string]string{
	idpkg.InboxOpen:                "adopt_inbox_open",
	idpkg.InboxContactsOnly:        "adopt_inbox_contacts_only",
	idpkg.InboxContactsAndRequests: "adopt_inbox_contacts_and_requests",
}

// adoptionCounts counts identities per profile/settings choice. Only relay-
// readable documents (public profile, relay settings) are consulted, and only
// aggregate counts leave this function.
func adoptionCounts(ids []string, read func(identity, path string) []byte) map[string]int64 {
	out := make(map[string]int64, len(adoptionStates))
	for _, state := range adoptionStates {
		out[state] = 0
	}
	inc := func(state string, ok bool) {
		if ok {
			out[state]++
		}
	}
	for _, id := range ids {
		if p, err := idpkg.ParseProfile(read(id, files.SysPublic+"/profile.json")); err == nil {
			inc("adopt_profile_display_name", strings.TrimSpace(p.DisplayName) != "")
			inc("adopt_profile_bio", strings.TrimSpace(p.Bio) != "")
			inc("adopt_profile_avatar", p.Avatar != "")
			inc("adopt_profile_links", len(p.Links) > 0)
			inc("adopt_profile_locale", p.Locale != "")
		}
		if p, err := idpkg.ParseInboxPolicy(read(id, files.SysRelay+"/inbox-policy.json")); err == nil {
			out[inboxModeStates[p.Mode]]++
			inc("adopt_inbox_anonymous", p.Anonymous != nil)
			inc("adopt_read_receipts_off", p.ReadReceipts != nil && !p.ReadReceipts.Enabled)
		} else {
			out["adopt_inbox_default"]++
		}
		if p, err := parseAnalytics(read(id, analyticsPath)); err == nil {
			inc("adopt_analytics_granted", p.Granted)
		}
		if c, err := idpkg.ParseContactsFile(read(id, files.SysRelay+"/contacts.json")); err == nil {
			accepted := false
			for _, contact := range c.Contacts {
				accepted = accepted || contact.State == idpkg.ContactAccepted
			}
			inc("adopt_contacts", accepted)
		}
	}
	return out
}

// systemAction recognizes only validated system documents, never filenames.
func systemAction(clean, method string) string {
	switch clean {
	case analyticsPath:
		return "analytics.preference"
	case files.SysRelay + "/contacts.json":
		return "contacts.update"
	case files.SysRelay + "/inbox-policy.json":
		return "policy.update"
	}
	if strings.HasPrefix(clean, files.SysRelay+"/shares/") {
		if method == "DELETE" {
			return "share.revoke"
		}
		return "share.update"
	}
	if strings.HasPrefix(clean, files.SysRelay+"/groups/") {
		return "group.update"
	}
	return "dav.write"
}

// Fixed route inventory. Domain hooks refine writes/forwarding without counting
// them as a second submission. HTTP request outcomes cover every other route.
var routeActions = map[string]string{
	"POST /identities": "registration.create",
	"POST /sessions":   "session.create", "DELETE /sessions/{id}": "session.revoke",
	"POST /auth/dav-token": "dav.token.create", "DELETE /auth/dav-token/{token}": "dav.token.revoke",
	"GET /messages/{identity}": "message.pickup", "POST /messages/{identity}/consume": "message.consume",
	"POST /acks": "ack.submit", "GET /requests/{identity}": "contact.pickup", "GET /anon/{identity}": "anonymous.pickup",
	"POST /identities/{identity}/export": "identity.export", "POST /identities/{identity}/rotate": "identity.rotate",
	"POST /identities/{identity}/encryption-key": "identity.encryption_key",
	"PUT /identities/{identity}/keystore":        "keystore.put", "POST /identities/{identity}/keystore/list": "keystore.list",
	"POST /identities/{identity}/keystore/fetch": "keystore.fetch", "DELETE /identities/{identity}/keystore/{enrollment}": "keystore.delete",
	"POST /identities/{identity}/enroll/offer": "enroll.offer", "POST /identities/{identity}/enroll/{rendezvous}/fetch": "enroll.fetch",
	"POST /identities/{identity}/enroll/{rendezvous}/deliver": "enroll.deliver", "GET /identities/{identity}/enroll/{rendezvous}": "enroll.claim",
	"DELETE /identities/{identity}/enroll/{rendezvous}": "enroll.cancel",
	"POST /sync/{identity}/upload":                      "upload.create", "PATCH /sync/{identity}/upload/{id}": "upload.chunk",
	"DELETE /sync/{identity}/upload/{id}": "upload.cancel",
}
