package guestbook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a test clock the server reads.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// book is a guestbook server, its clock and a signed-in visitor.
type book struct {
	srv   *Server
	clock *clock
	store Store
	users map[string]*http.Cookie
}

func newBook(t *testing.T, store Store, tweak func(*Config), users ...user) *book {
	t.Helper()
	z := zone{}
	for _, u := range users {
		z[u.name] = u.doc
	}
	c := &clock{t: testNow}
	cfg := Config{Origin: rpOrigin, Resolver: z, Now: c.now, Store: store}
	if tweak != nil {
		tweak(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := &book{srv: srv, clock: c, store: cfg.Store, users: map[string]*http.Cookie{}}
	// Signing in uses the signer's own clock, so everyone signs in first.
	for _, u := range users {
		b.users[u.name] = signIn(t, srv, u)
	}
	return b
}

type posted struct {
	code  int
	retry string
	body  map[string]any
}

func (b *book) post(t *testing.T, who, message string) posted {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"message": message})
	rec := post(t, b.srv, "/api/entries", string(raw), b.users[who])
	out := posted{code: rec.Code, retry: rec.Header().Get("Retry-After"), body: map[string]any{}}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func (b *book) list(t *testing.T) []map[string]any {
	t.Helper()
	rec := get(t, b.srv, "/api/entries")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var out struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Entries
}

func messages(entries []map[string]any) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e["message"].(string))
	}
	return out
}

func TestPostIsRateLimitedPerIdentity(t *testing.T) {
	alice, bob := newUser(t, who), newUser(t, "bob.poweur.net")
	b := newBook(t, nil, nil, alice, bob)

	if got := b.post(t, who, "first"); got.code != http.StatusCreated {
		t.Fatalf("first post = %d %v", got.code, got.body)
	}
	b.clock.advance(2 * time.Second)
	got := b.post(t, who, "too soon")
	if got.code != http.StatusTooManyRequests {
		t.Fatalf("second post after 2s = %d, want 429", got.code)
	}
	if got.retry != "3" || got.body["retry_after"] != float64(3) {
		t.Fatalf("retry hints = %q, %v (3s left)", got.retry, got.body)
	}
	if !strings.Contains(got.body["error"].(string), "wait") {
		t.Fatalf("error = %v", got.body["error"])
	}

	// Another identity is not held up by alice's turn.
	if got := b.post(t, "bob.poweur.net", "bob here"); got.code != http.StatusCreated {
		t.Fatalf("bob = %d", got.code)
	}
	// Exactly five seconds after her post, alice may post again.
	b.clock.advance(3 * time.Second)
	if got := b.post(t, who, "second"); got.code != http.StatusCreated {
		t.Fatalf("post at 5s = %d %v", got.code, got.body)
	}
	if got := messages(b.list(t)); strings.Join(got, "|") != "second|bob here|first" {
		t.Fatalf("entries newest first = %q", got)
	}
}

func TestRefusedMessagesDoNotCostATurn(t *testing.T) {
	alice := newUser(t, who)
	b := newBook(t, nil, nil, alice)
	for _, bad := range []string{"", "   ", strings.Repeat("a", 1001)} {
		if got := b.post(t, who, bad); got.code != http.StatusBadRequest {
			t.Fatalf("%.20q = %d", bad, got.code)
		}
	}
	if got := b.post(t, who, "now a good one"); got.code != http.StatusCreated {
		t.Fatalf("good post after refusals = %d %v", got.code, got.body)
	}
}

func TestIntervalAndLimitAreConfigurable(t *testing.T) {
	alice := newUser(t, who)
	b := newBook(t, nil, func(c *Config) { c.MaxMessage = 10; c.MinInterval = -1 }, alice)
	if got := b.post(t, who, "12345678901"); got.code != http.StatusBadRequest {
		t.Fatalf("11 characters under a limit of 10 = %d", got.code)
	}
	for i := 0; i < 3; i++ {
		if got := b.post(t, who, "ok "+strconv.Itoa(i)); got.code != http.StatusCreated {
			t.Fatalf("post %d with the limit off = %d", i, got.code)
		}
	}
}

func TestEntriesAreStoredAsAMarkdownLog(t *testing.T) {
	alice := newUser(t, who)
	store := NewMemStore()
	b := newBook(t, store, nil, alice)

	msg := "**Hello** <b>there</b>\n- one\n- two"
	got := b.post(t, who, msg)
	if got.code != http.StatusCreated {
		t.Fatalf("post = %d %v", got.code, got.body)
	}
	if html, _ := got.body["html"].(string); !strings.Contains(html, "<strong>Hello</strong>") || strings.Contains(html, "<b>") {
		t.Fatalf("rendered %q", html)
	}

	raw, err := store.Read(context.Background(), pageName(1))
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	for _, want := range []string{
		`<!-- gb:entry {"id":"` + got.body["id"].(string) + `","identity":"alice.poweur.net","at":"2026-01-15T09:30:00Z"} -->`,
		"### alice.poweur.net · 2026-01-15 09:30 UTC",
		"**Hello** \\<b>there\\</b>\n- one\n- two",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if !strings.HasSuffix(log, "\n\n") {
		t.Errorf("log should end with a blank line: %q", log[max(0, len(log)-10):])
	}
}

func TestLogPagesFillAndTheBookSpansThem(t *testing.T) {
	alice := newUser(t, who)
	store := NewMemStore()
	b := newBook(t, store, func(c *Config) { c.PageEntries = 2; c.MinInterval = -1 }, alice)
	for i := 1; i <= 5; i++ {
		if got := b.post(t, who, "entry "+strconv.Itoa(i)); got.code != http.StatusCreated {
			t.Fatalf("post %d = %d", i, got.code)
		}
	}
	names, _ := store.List(context.Background())
	if strings.Join(names, " ") != "entries-000001.md entries-000002.md entries-000003.md" {
		t.Fatalf("pages = %v", names)
	}
	for name, want := range map[string]int{"entries-000001.md": 2, "entries-000002.md": 2, "entries-000003.md": 1} {
		raw, _ := store.Read(context.Background(), name)
		if n := len(parseLog(raw)); n != want {
			t.Errorf("%s holds %d entries, want %d", name, n, want)
		}
	}
	if got := messages(b.list(t)); strings.Join(got, "|") != "entry 5|entry 4|entry 3|entry 2|entry 1" {
		t.Fatalf("book = %q", got)
	}
}

func TestOnlyTheNewestPagesAreKept(t *testing.T) {
	alice := newUser(t, who)
	b := newBook(t, nil, func(c *Config) { c.PageEntries = 1; c.MinInterval = -1 }, alice)
	for i := 1; i <= cachedPages+3; i++ {
		b.post(t, who, "entry "+strconv.Itoa(i))
	}
	if n := len(b.srv.pages); n != cachedPages {
		t.Fatalf("%d pages cached, want %d", n, cachedPages)
	}
	if got := messages(b.list(t)); len(got) != cachedPages || got[0] != "entry "+strconv.Itoa(cachedPages+3) {
		t.Fatalf("book = %q", got)
	}
}

func TestARestartedGuestbookReadsItsBookBack(t *testing.T) {
	alice := newUser(t, who)
	store := NewMemStore()
	first := newBook(t, store, func(c *Config) { c.PageEntries = 2 }, alice)
	first.post(t, who, "before the restart")
	first.clock.advance(10 * time.Second)
	first.post(t, who, "also before")
	first.clock.advance(10 * time.Second)
	first.post(t, who, "third, on page two")

	second := newBook(t, store, func(c *Config) { c.PageEntries = 2 }, alice)
	if got := messages(second.list(t)); strings.Join(got, "|") != "third, on page two|also before|before the restart" {
		t.Fatalf("after restart = %q", got)
	}
	// Posting goes on where the log left off.
	if got := second.post(t, who, "after the restart"); got.code != http.StatusCreated {
		t.Fatalf("post = %d", got.code)
	}
	raw, _ := store.Read(context.Background(), pageName(2))
	if n := len(parseLog(raw)); n != 2 {
		t.Fatalf("page two holds %d entries, want 2", n)
	}
}

// The log is the moderation tool: an entry deleted from the file leaves the
// page at the next refresh, and a later post is added to what the operator
// left, not to the guestbook's old copy.
func TestAnOperatorEditOfTheLogSticks(t *testing.T) {
	alice, bob := newUser(t, who), newUser(t, "bob.poweur.net")
	store := NewMemStore()
	b := newBook(t, store, func(c *Config) { c.MinInterval = -1 }, alice, bob)
	b.post(t, who, "keep me")
	b.post(t, "bob.poweur.net", "remove me")

	raw, _ := store.Read(context.Background(), pageName(1))
	var kept []string
	for _, e := range parseLog(raw) {
		if e.Message != "remove me" {
			kept = append(kept, formatEntry(e))
		}
	}
	if err := store.Write(context.Background(), pageName(1), []byte(strings.Join(kept, ""))); err != nil {
		t.Fatal(err)
	}

	if got := messages(b.list(t)); len(got) != 2 {
		t.Fatalf("before the refresh the page shows %q", got)
	}
	if err := b.srv.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := messages(b.list(t)); strings.Join(got, "|") != "keep me" {
		t.Fatalf("after the refresh = %q", got)
	}

	b.post(t, who, "after the edit")
	if got := messages(b.list(t)); strings.Join(got, "|") != "after the edit|keep me" {
		t.Fatalf("after a new post = %q (the removed entry came back)", got)
	}
}

func TestRefreshPicksUpEditsInTheBackground(t *testing.T) {
	store := NewMemStore()
	b := newBook(t, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.srv.Refresh(ctx, 5*time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()

	e := Entry{ID: "gb_x", Identity: "zed.poweur.net", At: "2026-01-15T09:00:00Z", Message: "written by hand"}
	if err := store.Write(ctx, pageName(1), []byte(formatEntry(e))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(b.srv.recent(10)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the refresh never showed the edited log")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// failingStore wraps a store and fails the calls a test asks for.
type failingStore struct {
	Store
	mu                    sync.Mutex
	failWrites, failReads bool
	writes                int
	block                 chan struct{}
}

func (f *failingStore) Write(ctx context.Context, name string, data []byte) error {
	f.mu.Lock()
	fail, block := f.failWrites, f.block
	f.writes++
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if fail {
		return errors.New("drive unavailable")
	}
	return f.Store.Write(ctx, name, data)
}

func (f *failingStore) Read(ctx context.Context, name string) ([]byte, error) {
	f.mu.Lock()
	fail := f.failReads
	f.mu.Unlock()
	if fail {
		return nil, errors.New("drive unavailable")
	}
	return f.Store.Read(ctx, name)
}

func TestAFailedWriteShowsNothingAndKeepsTheVisitorsTurn(t *testing.T) {
	alice := newUser(t, who)
	store := &failingStore{Store: NewMemStore(), failWrites: true}
	b := newBook(t, store, nil, alice)

	got := b.post(t, who, "lost?")
	if got.code != http.StatusBadGateway {
		t.Fatalf("post with the drive down = %d, want 502", got.code)
	}
	if strings.Contains(got.body["error"].(string), "unavailable") {
		t.Fatalf("the visitor was shown an internal error: %v", got.body["error"])
	}
	if n := len(b.list(t)); n != 0 {
		t.Fatalf("an entry that was not stored is listed (%d)", n)
	}

	store.mu.Lock()
	store.failWrites = false
	store.mu.Unlock()
	if got := b.post(t, who, "again"); got.code != http.StatusCreated {
		t.Fatalf("retry straight away = %d %v (the failed attempt used up the turn)", got.code, got.body)
	}
}

func TestLoadFailsRatherThanStartWithAnEmptyBook(t *testing.T) {
	store := &failingStore{Store: NewMemStore()}
	if err := store.Store.Write(context.Background(), pageName(1), []byte("x")); err != nil {
		t.Fatal(err)
	}
	store.failReads = true
	srv, err := New(Config{Origin: rpOrigin, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Load(context.Background()); err == nil {
		t.Fatal("Load succeeded with an unreadable store")
	}
}

func TestARefreshThatFailsKeepsTheBook(t *testing.T) {
	alice := newUser(t, who)
	store := &failingStore{Store: NewMemStore()}
	b := newBook(t, store, nil, alice)
	b.post(t, who, "still here")
	store.mu.Lock()
	store.failReads = true
	store.mu.Unlock()
	if err := b.srv.Load(context.Background()); err == nil {
		t.Fatal("expected the refresh to fail")
	}
	if got := messages(b.list(t)); len(got) != 1 {
		t.Fatalf("book after a failed refresh = %q", got)
	}
}

func TestTooManyWaitingWritesGetBusy(t *testing.T) {
	store := &failingStore{Store: NewMemStore(), block: make(chan struct{})}
	srv, err := New(Config{Origin: rpOrigin, Store: store, MinInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < maxWaiting; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = srv.add(context.Background(), "alice.poweur.net", "hi")
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(srv.waiting) < maxWaiting {
		if time.Now().After(deadline) {
			t.Fatal("writers never queued")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := srv.add(context.Background(), "alice.poweur.net", "one too many"); !errors.Is(err, errBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
	close(store.block)
	wg.Wait()
	if _, err := srv.add(context.Background(), "alice.poweur.net", "after"); err != nil {
		t.Fatalf("after the queue drained: %v", err)
	}
}

func TestBusyIsA503WithRetryAfter(t *testing.T) {
	alice := newUser(t, who)
	store := &failingStore{Store: NewMemStore(), block: make(chan struct{})}
	b := newBook(t, store, func(c *Config) { c.MinInterval = -1 }, alice)
	var wg sync.WaitGroup
	for i := 0; i < maxWaiting; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.srv.add(context.Background(), "x.poweur.net", "hi")
		}()
	}
	for len(b.srv.waiting) < maxWaiting {
		time.Sleep(time.Millisecond)
	}
	got := b.post(t, who, "no room")
	if got.code != http.StatusServiceUnavailable || got.retry == "" {
		t.Fatalf("post = %d retry %q", got.code, got.retry)
	}
	close(store.block)
	wg.Wait()
}

func TestListingCarriesLimitsAndArchive(t *testing.T) {
	b := newBook(t, nil, func(c *Config) { c.ArchiveURL = "https://guestbook.poweur.net/pub/guestbook/" })
	rec := get(t, b.srv, "/api/entries")
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["max_message"] != float64(1000) || out["min_interval"] != float64(5) || out["archive"] != "https://guestbook.poweur.net/pub/guestbook/" {
		t.Fatalf("listing = %v", out)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	b := newBook(t, nil, nil)
	for _, path := range []string{"/", "/api/entries", "/assets/page.js", "/.well-known/poweur.json", "/nope"} {
		h := get(t, b.srv, path).Header()
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP = %q", path, csp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers = %v", path, h)
		}
	}
}

func TestPageServesItsAssets(t *testing.T) {
	b := newBook(t, nil, nil)
	for path, want := range map[string]string{
		"/assets/page.js":       "text/javascript",
		"/assets/editor.js":     "text/javascript",
		"/assets/guestbook.css": "text/css",
	} {
		rec := get(t, b.srv, path)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), want) || rec.Body.Len() == 0 {
			t.Errorf("%s = %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	for _, path := range []string{"/assets/missing.js", "/assets/page.txt", "/assets/..%2Fserver.go", "/assets/"} {
		if rec := get(t, b.srv, path); rec.Code == http.StatusOK {
			t.Errorf("%s served", path)
		}
	}
	// Nothing the page runs may be inline: the CSP forbids it.
	index := get(t, b.srv, "/").Body.String()
	for _, bad := range []string{"<script>", "onclick=", "style="} {
		if strings.Contains(index, bad) {
			t.Errorf("index has inline %q", bad)
		}
	}
	if !strings.Contains(index, `id="editor"`) || !strings.Contains(index, `contenteditable="true"`) {
		t.Fatal("index has no editor")
	}
}

func TestPendingSignInsAreCapped(t *testing.T) {
	b := newBook(t, nil, nil)
	for i := 0; i < maxPending; i++ {
		b.srv.pending["fill-"+strconv.Itoa(i)] = &pendingLogin{expiresAt: b.clock.now().Add(time.Hour)}
	}
	rec := post(t, b.srv, "/auth/start", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("start with a full table = %d", rec.Code)
	}
}

// --- DriveStore --------------------------------------------------------------

// fakeCLI plays `poweur drive` over an in-memory drive.
type fakeCLI struct {
	mu      sync.Mutex
	root    map[string]string // top-level folder -> "folder"
	files   map[string][]byte // "folder/name" -> content
	public  map[string]bool
	calls   [][]string
	failing string
}

func newFakeCLI() *fakeCLI {
	return &fakeCLI{root: map[string]string{}, files: map[string][]byte{}, public: map[string]bool{}}
}

func (f *fakeCLI) run(args []string, stdout, stderr io.Writer) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	if f.failing != "" && args[1] == f.failing {
		io.WriteString(stderr, "relay unreachable")
		return 1
	}
	if args[0] != "drive" || args[len(args)-2] != "--use-identity" || args[len(args)-1] != "guestbook.poweur.net" {
		io.WriteString(stderr, "bad invocation")
		return 2
	}
	rows := func(v any) { raw, _ := json.Marshal(v); stdout.Write(raw) }
	switch args[1] {
	case "list":
		out := []map[string]string{}
		if args[2] == "/" {
			for name := range f.root {
				out = append(out, map[string]string{"name": name, "kind": "folder"})
			}
			rows(out)
			return 0
		}
		folder := strings.TrimPrefix(args[2], "/")
		if _, ok := f.root[folder]; !ok {
			io.WriteString(stderr, "drive path not found: "+folder)
			return 1
		}
		for key := range f.files {
			if dir, name, _ := strings.Cut(key, "/"); dir == folder {
				out = append(out, map[string]string{"name": name, "kind": "file"})
			}
		}
		rows(out)
	case "mkdir":
		folder := strings.TrimPrefix(args[2], "/")
		f.root[folder] = "folder"
		f.public[folder] = containsArg(args, "--public")
		rows(map[string]string{"node": "n1", "name": folder})
	case "put":
		raw, err := os.ReadFile(args[2])
		if err != nil {
			io.WriteString(stderr, err.Error())
			return 1
		}
		f.files[strings.TrimPrefix(args[3], "/")] = raw
		rows(map[string]string{"node": "n2"})
	case "get":
		raw, ok := f.files[strings.TrimPrefix(args[2], "/")]
		if !ok {
			io.WriteString(stderr, "drive path not found: "+args[2])
			return 1
		}
		if err := os.WriteFile(args[3], raw, 0o600); err != nil {
			return 1
		}
		rows(map[string]string{"node": "n2"})
	default:
		io.WriteString(stderr, "unknown command")
		return 2
	}
	return 0
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestDriveStoreMakesAPublicFolderOnce(t *testing.T) {
	cli := newFakeCLI()
	store := &DriveStore{Identity: "guestbook.poweur.net", Run: cli.run}
	for i := 0; i < 2; i++ {
		if err := store.Init(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !cli.public["guestbook"] || len(cli.root) != 1 {
		t.Fatalf("folders = %v public = %v", cli.root, cli.public)
	}
	mkdirs := 0
	for _, c := range cli.calls {
		if c[1] == "mkdir" {
			mkdirs++
		}
	}
	if mkdirs != 1 {
		t.Fatalf("mkdir ran %d times", mkdirs)
	}
}

func TestDriveStoreRefusesAFileWhereTheFolderShouldBe(t *testing.T) {
	store := &DriveStore{Identity: "guestbook.poweur.net", Run: func(args []string, stdout, stderr io.Writer) int {
		io.WriteString(stdout, `[{"name":"guestbook","kind":"file"}]`)
		return 0
	}}
	if err := store.Init(context.Background()); err == nil {
		t.Fatal("Init accepted a file named guestbook")
	}
}

func TestDriveStoreReadWriteList(t *testing.T) {
	ctx := context.Background()
	cli := newFakeCLI()
	store := &DriveStore{Identity: "guestbook.poweur.net", Run: cli.run}
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, pageName(1)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if names, err := store.List(ctx); err != nil || len(names) != 0 {
		t.Fatalf("empty folder: %v %v", names, err)
	}
	if err := store.Write(ctx, pageName(1), []byte("# one\n")); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, pageName(1), []byte("# two\n")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(ctx, pageName(1))
	if err != nil || string(got) != "# two\n" {
		t.Fatalf("read back %q %v", got, err)
	}
	if names, _ := store.List(ctx); strings.Join(names, " ") != pageName(1) {
		t.Fatalf("names = %v", names)
	}
	// The files the store makes for the CLI are not left behind.
	entries, _ := os.ReadDir(os.TempDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "guestbook-read-") || strings.HasPrefix(e.Name(), "guestbook-write-") {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}
}

func TestDriveStoreReportsCLIFailures(t *testing.T) {
	ctx := context.Background()
	cli := newFakeCLI()
	store := &DriveStore{Identity: "guestbook.poweur.net", Run: cli.run}
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	cli.failing = "put"
	if err := store.Write(ctx, pageName(1), []byte("x")); err == nil || !strings.Contains(err.Error(), "relay unreachable") {
		t.Fatalf("write error = %v", err)
	}
	cli.failing = "get"
	if _, err := store.Read(ctx, pageName(1)); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a failed read must not look like a missing file: %v", err)
	}
	cli.failing = "list"
	if _, err := store.List(ctx); err == nil {
		t.Fatal("list should fail")
	}
	if err := store.Init(ctx); err == nil {
		t.Fatal("init should fail")
	}
}

func TestAGuestbookOnADriveStore(t *testing.T) {
	alice := newUser(t, who)
	cli := newFakeCLI()
	store := &DriveStore{Identity: "guestbook.poweur.net", Run: cli.run}
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := newBook(t, store, nil, alice)
	if got := b.post(t, who, "in the drive"); got.code != http.StatusCreated {
		t.Fatalf("post = %d %v", got.code, got.body)
	}
	log := string(cli.files["guestbook/"+pageName(1)])
	if !strings.Contains(log, "in the drive") || !strings.Contains(log, "alice.poweur.net") {
		t.Fatalf("drive file = %q", log)
	}
}
