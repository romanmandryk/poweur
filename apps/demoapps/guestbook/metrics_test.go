package guestbook

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/poweur/demoapps/appmetrics"
)

func metricsText(reg *appmetrics.Registry) string {
	var b strings.Builder
	reg.Write(&b)
	return b.String()
}

func wantMetrics(t *testing.T, reg *appmetrics.Registry, lines ...string) {
	t.Helper()
	text := metricsText(reg)
	for _, want := range lines {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestMetricsCountPostsByOutcome(t *testing.T) {
	reg := appmetrics.New("poweur_guestbook")
	alice := newUser(t, who)
	b := newBook(t, nil, func(c *Config) { c.Metrics = reg }, alice)

	if got := b.post(t, who, "first"); got.code != http.StatusCreated {
		t.Fatalf("first = %d", got.code)
	}
	b.clock.advance(time.Second)
	if got := b.post(t, who, "too soon"); got.code != http.StatusTooManyRequests {
		t.Fatalf("second = %d", got.code)
	}
	b.post(t, who, "")
	b.post(t, who, strings.Repeat("x", 1001))
	if rec := post(t, b.srv, "/api/entries", `{"message":"hi"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}

	wantMetrics(t, reg,
		`poweur_guestbook_posts_total{result="created"} 1`,
		`poweur_guestbook_posts_total{result="rate_limited"} 1`,
		`poweur_guestbook_posts_total{result="invalid"} 2`,
		`poweur_guestbook_posts_total{result="unauthorized"} 1`,
		`poweur_guestbook_posts_total{result="busy"} 0`,
		`poweur_guestbook_posts_total{result="store_error"} 0`,
	)
	text := metricsText(reg)
	for _, leak := range []string{"alice", "first", "too soon", "poweur.net"} {
		if strings.Contains(text, leak) {
			t.Errorf("%q leaked into the metrics:\n%s", leak, text)
		}
	}
}

func TestMetricsCountSignInSteps(t *testing.T) {
	reg := appmetrics.New("poweur_guestbook")
	alice, mallory := newUser(t, who), newUser(t, "mallory.poweur.net")
	// mallory publishes nothing, so her approval cannot be verified.
	srv, err := New(Config{Origin: rpOrigin, Resolver: zone{alice.name: alice.doc}, Now: func() time.Time { return testNow }, Metrics: reg})
	if err != nil {
		t.Fatal(err)
	}

	// Same device: started, approved, then the browser resumes.
	signIn(t, srv, alice)
	wantMetrics(t, reg,
		`poweur_guestbook_signins_total{result="started"} 1`,
		`poweur_guestbook_signins_total{result="approved"} 1`,
		`poweur_guestbook_signins_total{result="completed"} 1`,
	)

	// Cross device: the waiting tab is handed its cookie by the poll, once.
	started := start(t, srv)
	if rec, _ := deliver(t, srv, approve(t, alice, started.Request), started.MatchCode); rec.Code != http.StatusOK {
		t.Fatalf("callback = %d", rec.Code)
	}
	if got := pollStatus(t, srv, started); got != "complete" {
		t.Fatalf("poll = %q", got)
	}
	pollStatus(t, srv, started)
	wantMetrics(t, reg,
		`poweur_guestbook_signins_total{result="started"} 2`,
		`poweur_guestbook_signins_total{result="approved"} 2`,
		`poweur_guestbook_signins_total{result="completed"} 2`,
	)

	// An approval that does not verify.
	started = start(t, srv)
	if rec, _ := deliver(t, srv, approve(t, mallory, started.Request), started.MatchCode); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unverifiable approval = %d", rec.Code)
	}
	// A wrong match code.
	started = start(t, srv)
	wrong := "00"
	if started.MatchCode == wrong {
		wrong = "01"
	}
	if rec, _ := deliver(t, srv, approve(t, alice, started.Request), wrong); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong match code = %d", rec.Code)
	}
	// Approved for another browser than the one that started it: approved, never completed.
	started, _ = startIn(t, srv)
	_, receipt := deliver(t, srv, approve(t, alice, started.Request), "")
	if res := resume(t, srv, receipt.ResumeURI); res.Code != http.StatusForbidden {
		t.Fatalf("resume in another browser = %d", res.Code)
	}

	wantMetrics(t, reg,
		`poweur_guestbook_signins_total{result="started"} 5`,
		`poweur_guestbook_signins_total{result="approved"} 3`,
		`poweur_guestbook_signins_total{result="failed"} 1`,
		`poweur_guestbook_signins_total{result="match_failed"} 1`,
		`poweur_guestbook_signins_total{result="completed"} 2`,
		`poweur_guestbook_signins_total{result="busy"} 0`,
	)
}

func TestMetricsCountSignInsRefusedForLackOfRoom(t *testing.T) {
	reg := appmetrics.New("poweur_guestbook")
	srv, err := New(Config{Origin: rpOrigin, Metrics: reg, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	for i := 0; i < maxPending; i++ {
		srv.pending[string(rune(i))+"x"] = &pendingLogin{requestID: "x", expiresAt: testNow.Add(time.Hour)}
	}
	srv.mu.Unlock()
	if rec := post(t, srv, "/auth/start", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("start with no room = %d", rec.Code)
	}
	wantMetrics(t, reg,
		`poweur_guestbook_signins_total{result="busy"} 1`,
		`poweur_guestbook_signins_total{result="started"} 0`,
	)
}

func TestMetricsCountStoreAndRefreshErrors(t *testing.T) {
	reg := appmetrics.New("poweur_guestbook")
	alice := newUser(t, who)
	store := &failingStore{Store: NewMemStore()}
	b := newBook(t, store, func(c *Config) { c.Metrics = reg; c.MinInterval = -1 }, alice)

	if got := b.post(t, who, "stored"); got.code != http.StatusCreated {
		t.Fatalf("post = %d", got.code)
	}
	store.mu.Lock()
	store.failWrites = true
	store.mu.Unlock()
	if got := b.post(t, who, "lost"); got.code != http.StatusBadGateway {
		t.Fatalf("post with the drive down = %d", got.code)
	}
	wantMetrics(t, reg,
		`poweur_guestbook_posts_total{result="created"} 1`,
		`poweur_guestbook_posts_total{result="store_error"} 1`,
		`poweur_guestbook_errors_total{kind="store_write"} 1`,
		`poweur_guestbook_errors_total{kind="log_refresh"} 0`,
	)

	store.mu.Lock()
	store.failReads = true
	store.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.srv.Refresh(ctx, 5*time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(5 * time.Second); strings.Contains(metricsText(reg), `poweur_guestbook_errors_total{kind="log_refresh"} 0`); {
		if time.Now().After(deadline) {
			t.Fatalf("a failing refresh was never counted:\n%s", metricsText(reg))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMetricsCountRequestsByRouteNotByPath(t *testing.T) {
	reg := appmetrics.New("poweur_guestbook")
	b := newBook(t, nil, func(c *Config) { c.Metrics = reg })

	get(t, b.srv, "/api/entries")
	get(t, b.srv, "/api/entries?secret=abc")
	get(t, b.srv, "/healthz")
	get(t, b.srv, "/auth/r/not-a-real-code")
	post(t, b.srv, "/api/entries", `{"message":"hi"}`)

	wantMetrics(t, reg,
		`poweur_guestbook_http_requests_total{route="GET /api/entries",code="2xx"} 2`,
		`poweur_guestbook_http_requests_total{route="GET /healthz",code="2xx"} 1`,
		`poweur_guestbook_http_requests_total{route="GET /auth/r/{code}",code="4xx"} 1`,
		`poweur_guestbook_http_requests_total{route="POST /api/entries",code="4xx"} 1`,
	)
	text := metricsText(reg)
	for _, leak := range []string{"secret", "abc", "not-a-real-code"} {
		if strings.Contains(text, leak) {
			t.Errorf("%q leaked into the metrics:\n%s", leak, text)
		}
	}
}

func TestMetricsAreOptional(t *testing.T) {
	alice := newUser(t, who)
	b := newBook(t, nil, nil, alice)
	if got := b.post(t, who, "no registry at all"); got.code != http.StatusCreated {
		t.Fatalf("post = %d", got.code)
	}
}
