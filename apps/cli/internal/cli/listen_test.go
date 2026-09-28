package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/poweur/cli/internal/session"
	idpkg "github.com/poweur/identity"
)

// `poweur listen` (EPIC-009 E09-T2). What is worth pinning is the contract:
// events carry no payload, frames decode the way SSE says, and the reconnect
// policy backs off but resets on every successful connection.

func TestParseSSEFrame(t *testing.T) {
	cases := []struct {
		name    string
		frame   string
		wantOK  bool
		wantTyp string
		wantMsg string
	}{
		{"message", "event: message\ndata: {\"type\":\"message\",\"message_id\":\"m1\"}", true, "message", "m1"},
		{"ready", "event: ready\ndata: {\"type\":\"ready\",\"identity\":\"a\"}", true, "ready", ""},
		{"data only", "data: {\"type\":\"ack\",\"message_id\":\"m2\"}", true, "ack", "m2"},
		{"crlf line endings", "event: message\r\ndata: {\"type\":\"message\"}\r", true, "message", ""},
		{"multiline data", "data: {\"type\":\ndata: \"message\"}", true, "message", ""},
		{"keepalive comment", ": keepalive", false, "", ""},
		{"empty", "", false, "", ""},
		{"no data lines", "event: message\nid: 7", false, "", ""},
		{"not json", "data: hello", false, "", ""},
		{"json without a type", "data: {\"message_id\":\"m1\"}", false, "", ""},
		{"json array", "data: [1,2,3]", false, "", ""},
	}
	for _, tc := range cases {
		got, ok := parseSSEFrame(tc.frame)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok = %v, want %v (event %+v)", tc.name, ok, tc.wantOK, got)
		}
		if !ok {
			continue
		}
		if got.Type != tc.wantTyp || got.MessageID != tc.wantMsg {
			t.Fatalf("%s: got %+v, want type=%q message_id=%q", tc.name, got, tc.wantTyp, tc.wantMsg)
		}
	}
}

// The epic's rule: the socket is notification, the cursor is truth. A frame
// that smuggled a payload would still decode into an event that carries none.
func TestSSEFrameCarriesNoPayload(t *testing.T) {
	got, ok := parseSSEFrame(`data: {"type":"message","message_id":"m1","payload":"secret","plaintext":"secret"}`)
	if !ok {
		t.Fatal("frame did not decode")
	}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", StreamEvent{Type: "message", MessageID: "m1"}) {
		t.Fatalf("event carries more than {type, message_id}: %+v", got)
	}
}

func TestReadSSE(t *testing.T) {
	body := strings.Join([]string{
		"event: ready\ndata: {\"type\":\"ready\"}\n\n",
		": keepalive\n\n",
		"event: message\ndata: {\"type\":\"message\",\"message_id\":\"m1\"}\n\n",
		"event: ack\ndata: {\"type\":\"ack\",\"message_id\":\"m2\"}\n\n",
		// A half-written frame at EOF is not an event.
		"event: message\ndata: {\"type\":\"mes",
	}, "")

	var seen []string
	if err := readSSE(context.Background(), strings.NewReader(body), func(e StreamEvent) {
		seen = append(seen, e.Type+":"+e.MessageID)
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"ready:", "message:m1", "ack:m2"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", seen, want)
	}
}

func TestReadSSEStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := strings.Repeat("data: {\"type\":\"message\"}\n\n", 10)
	count := 0
	err := readSSE(ctx, strings.NewReader(body), func(StreamEvent) {
		count++
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if count != 1 {
		t.Fatalf("kept reading after cancel: %d events", count)
	}
}

func TestNextBackoff(t *testing.T) {
	max := 30 * time.Second
	cases := []struct{ in, want time.Duration }{
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{16 * time.Second, max},
		{max, max},
		{time.Hour, max},
	}
	for _, tc := range cases {
		if got := nextBackoff(tc.in, max); got != tc.want {
			t.Fatalf("nextBackoff(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	// Overflow must saturate rather than wrap into a negative delay.
	if got := nextBackoff(time.Duration(1<<62), max); got != max {
		t.Fatalf("overflow: %v, want %v", got, max)
	}
}

// Backoff grows while the relay is unreachable and resets the moment a
// connection succeeds — reconnecting after an hour of uptime must not start
// at the cap.
func TestStreamLoopBackoff(t *testing.T) {
	base := time.Second
	max := 8 * time.Second

	var slept []time.Duration
	attempt := 0
	ctx, cancel := context.WithCancel(context.Background())

	streamLoop(ctx, base, max,
		func(ctx context.Context, onOpen func()) error {
			attempt++
			switch {
			case attempt <= 4:
				return errors.New("refused") // never opened
			case attempt == 5:
				onOpen() // a successful connection, then the relay closes it
				return nil
			case attempt == 6:
				return errors.New("refused again")
			}
			cancel()
			return nil
		},
		nil,
		func(_ context.Context, d time.Duration) { slept = append(slept, d) },
	)

	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, // doubling, capped
		1 * time.Second, // reset by the successful connection
		2 * time.Second, // and growing again from base, not from the cap
	}
	if len(slept) != len(want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("sleep %d = %v, want %v (all: %v)", i, slept[i], want[i], slept)
		}
	}
}

func TestStreamLoopStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	streamLoop(ctx, time.Second, time.Second,
		func(context.Context, func()) error { calls++; return nil },
		nil,
		func(context.Context, time.Duration) { t.Fatal("slept after cancel") },
	)
	if calls != 0 {
		t.Fatalf("connected %d times on an already-cancelled context", calls)
	}
}

// A stream that fails to open reports the error to the caller; one that
// opens and ends cleanly does not.
func TestStreamLoopReportsOpenFailures(t *testing.T) {
	var mu sync.Mutex
	var errs []string
	ctx, cancel := context.WithCancel(context.Background())
	attempt := 0
	streamLoop(ctx, time.Millisecond, time.Millisecond,
		func(_ context.Context, onOpen func()) error {
			attempt++
			if attempt == 1 {
				return errors.New("boom")
			}
			onOpen()
			cancel()
			return nil
		},
		func(err error) { mu.Lock(); errs = append(errs, err.Error()); mu.Unlock() },
		func(context.Context, time.Duration) {},
	)
	if len(errs) != 1 || errs[0] != "boom" {
		t.Fatalf("errors = %v, want exactly [boom]", errs)
	}
}

// End to end against a fake relay: the challenge is signed, the device is
// named, and every frame reaches the caller.
func TestStreamEventsAgainstFakeRelay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp, identityValue, priv := newListenIdentity(t)
	_ = tmp

	var gotHeaders http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/challenge":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"challenge":"chal-123","expires_at":"2099-01-01T00:00:00Z"}`)
		case strings.HasPrefix(r.URL.Path, "/events/"):
			gotHeaders = r.Header.Clone()
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("no flusher")
				return
			}
			fmt.Fprint(w, "event: ready\ndata: {\"type\":\"ready\"}\n\n")
			flusher.Flush()
			fmt.Fprint(w, "event: message\ndata: {\"type\":\"message\",\"message_id\":\"m1\"}\n\n")
			flusher.Flush()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	opened := false
	var seen []string
	err := streamEvents(context.Background(), ts.URL, identityValue, sessionForListen(), priv,
		func() { opened = true },
		func(e StreamEvent) { seen = append(seen, e.Type) })
	if err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("onOpen never fired")
	}
	if strings.Join(seen, ",") != "ready,message" {
		t.Fatalf("events = %v", seen)
	}
	if gotHeaders.Get("X-Poweur-Identity") != identityValue {
		t.Fatalf("identity header %q", gotHeaders.Get("X-Poweur-Identity"))
	}
	if gotHeaders.Get("X-Poweur-Challenge") != "chal-123" {
		t.Fatalf("challenge header %q", gotHeaders.Get("X-Poweur-Challenge"))
	}
	if gotHeaders.Get("X-Poweur-Signature") == "" {
		t.Fatal("no signature header — the stream would be unauthenticated")
	}
	if gotHeaders.Get("Accept") != "text/event-stream" {
		t.Fatalf("Accept %q", gotHeaders.Get("Accept"))
	}
	// The device names itself so the owner gets a last_seen (E04-T6).
	if gotHeaders.Get("X-Poweur-Device") == "" {
		t.Fatal("no device header on the stream")
	}
}

// A relay that refuses the stream is an error the loop can back off on, not
// a silent success.
func TestStreamEventsRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, identityValue, priv := newListenIdentity(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/challenge" {
			fmt.Fprint(w, `{"challenge":"c","expires_at":"2099-01-01T00:00:00Z"}`)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"too_many_streams","detail":"limit reached"}`)
	}))
	defer ts.Close()

	err := streamEvents(context.Background(), ts.URL, identityValue, sessionForListen(), priv, nil,
		func(StreamEvent) { t.Error("an event arrived from a refused stream") })
	if err == nil || !strings.Contains(err.Error(), "too_many_streams") {
		t.Fatalf("refused stream: %v", err)
	}
}

// newListenIdentity generates a local identity for the stream tests. It does
// not register anywhere: streamEvents only needs a key to sign the challenge
// with, and the fake relay does not verify it.
func newListenIdentity(t *testing.T) (dir, identityValue string, priv ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return t.TempDir(), "listener.poweur.net", priv
}

// sessionForListen returns an empty session, which is the headless path:
// with no session key, streamEvents falls back to the identity key.
func sessionForListen() session.Session { return session.Session{} }

// A device that has never introduced itself gets one on first use, and the
// record is stable across calls — the relay derives the same row id either
// way, so a rewritten fingerprint would fork the registry.
func TestLoadDeviceIsStable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := loadDevice()
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == "" {
		t.Fatal("no fingerprint generated")
	}
	if !idpkg.ValidDeviceID(first.DeviceID()) {
		t.Fatalf("derived device id %q is not valid", first.DeviceID())
	}
	second, err := loadDevice()
	if err != nil {
		t.Fatal(err)
	}
	if second.Fingerprint != first.Fingerprint {
		t.Fatalf("fingerprint changed between calls: %q then %q", first.Fingerprint, second.Fingerprint)
	}

	// The file is the owner's alone.
	path, err := devicePath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("device.json mode %o, want 600", perm)
	}

	// A corrupt file is replaced rather than fatal — the device record is a
	// convenience, not a credential.
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := loadDevice()
	if err != nil {
		t.Fatal(err)
	}
	if third.Fingerprint == "" {
		t.Fatal("corrupt device.json was not replaced")
	}
}

func TestDeviceHeaders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("POWEUR_DEVICE_NAME", "Roman's laptop")
	t.Setenv("POWEUR_DEVICE_KIND", "laptop")

	h := deviceHeaders()
	if h["X-Poweur-Device"] == "" {
		t.Fatal("no fingerprint header")
	}
	if h["X-Poweur-Device-Name"] != "Roman's laptop" {
		t.Fatalf("name header %q", h["X-Poweur-Device-Name"])
	}
	if h["X-Poweur-Device-Kind"] != "laptop" {
		t.Fatalf("kind header %q", h["X-Poweur-Device-Kind"])
	}
	// The fingerprint on the wire is not the id: the relay stores only a
	// hash, so nothing reversible about this machine travels.
	if strings.Contains(h["X-Poweur-Device"], "dev_") {
		t.Fatalf("fingerprint leaks the device id: %q", h["X-Poweur-Device"])
	}

	header := http.Header{}
	applyDeviceHeaders(header)
	if header.Get("X-Poweur-Device") != h["X-Poweur-Device"] {
		t.Fatal("applyDeviceHeaders did not set the fingerprint")
	}
}

// An unrecognized kind from the environment degrades rather than being
// sent verbatim, so the registry never has to reject the row.
func TestDefaultDeviceKindNormalizes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("POWEUR_DEVICE_KIND", "TOASTER")
	if got := defaultDeviceKind(); got != idpkg.DeviceKindUnknown {
		t.Fatalf("defaultDeviceKind = %q, want unknown", got)
	}
	t.Setenv("POWEUR_DEVICE_KIND", "  Phone ")
	if got := defaultDeviceKind(); got != idpkg.DeviceKindPhone {
		t.Fatalf("defaultDeviceKind = %q, want phone", got)
	}
}

func TestDefaultDeviceNameSanitizes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("POWEUR_DEVICE_NAME", "lap\x1btop")
	if got := defaultDeviceName(); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("defaultDeviceName = %q, control character survived", got)
	}
	t.Setenv("POWEUR_DEVICE_NAME", strings.Repeat("a", idpkg.MaxDeviceNameLen+10))
	if got := defaultDeviceName(); len(got) != idpkg.MaxDeviceNameLen {
		t.Fatalf("defaultDeviceName length %d, want %d", len(got), idpkg.MaxDeviceNameLen)
	}
}

func TestDescribeSync(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	cases := []struct{ name, syncedAt, want string }{
		{"never", "", "never synced"},
		{"unparseable", "soon", "never synced"},
		{"just now", at(10 * time.Second), "synced just now"},
		{"minutes", at(20 * time.Minute), "synced 20 min ago"},
		{"hours", at(5 * time.Hour), "synced 5 h ago"},
		// The line the epic asked for.
		{"three days", at(72 * time.Hour), "synced 3 day(s) ago"},
	}
	for _, tc := range cases {
		got := describeSync(idpkg.Device{SyncedAt: tc.syncedAt}, now)
		if got != tc.want {
			t.Fatalf("%s: describeSync = %q, want %q", tc.name, got, tc.want)
		}
	}
}
