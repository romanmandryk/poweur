package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
	"github.com/poweur/demoapps/guestbook"
)

// INT_GUESTBOOK_01: the guestbook keeps its book in its own Poweur drive. A
// visitor signs in with the CLI, posts rich text, and the entry is a Markdown
// log in a public folder that the relay serves to anyone. The book survives a
// restart of the guestbook, an operator removes an entry by editing the file,
// and one identity cannot post twice within five seconds.
func TestINT_GUESTBOOK_01_BookLivesInItsOwnPublicDrive(t *testing.T) {
	const (
		book  = "guestbook.poweur.net"
		alice = "gbalice.poweur.net"
	)
	relay := newDrillRelay(t, book, alice)
	bookHome, aliceHome := t.TempDir(), t.TempDir()
	createSeedIdentity(t, bookHome, book, relay.url)
	createSeedIdentity(t, aliceHome, alice, relay.url)
	aliceDoc := hostedIdentityDocument(t, relay.url, alice)

	// The guestbook's CLI state is its own HOME, whichever test step runs next.
	store := &guestbook.DriveStore{Identity: book, Run: func(args []string, stdout, stderr io.Writer) int {
		t.Setenv("HOME", bookHome)
		return clipkg.Run(args, stdout, stderr)
	}}
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}

	serve := func() (*guestbook.Server, string, func()) {
		stub := httptest.NewUnstartedServer(nil)
		origin := "http://" + stub.Listener.Addr().String()
		srv, err := guestbook.New(guestbook.Config{Origin: origin, Store: store, Resolver: fixedSignInResolver{doc: aliceDoc}})
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.Load(context.Background()); err != nil {
			t.Fatalf("load: %v", err)
		}
		stub.Config.Handler = srv
		stub.Start()
		return srv, origin, stub.Close
	}
	srv, origin, stop := serve()
	defer stop()

	cookie := signInWithCLI(t, origin, aliceHome)
	post := func(message string) (int, map[string]any) {
		raw, _ := json.Marshal(map[string]string{"message": message})
		req, _ := http.NewRequest(http.MethodPost, origin+"/api/entries", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	message := "Hello **world**, from a <script>alert(1)</script> visitor\n- one\n- two"
	if status, body := post(message); status != http.StatusCreated {
		t.Fatalf("post = %d %v", status, body)
	}
	// Five seconds between posts, per identity.
	if status, body := post("again at once"); status != http.StatusTooManyRequests || body["retry_after"] == nil {
		t.Fatalf("second post = %d %v, want 429", status, body)
	}

	// The book is a public file on the relay: anyone can read it.
	public := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, relay.url+path, nil)
		req.Host = book
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	status, log := public("/pub/guestbook/entries-000001.md")
	if status != http.StatusOK {
		t.Fatalf("public log = %d %s", status, log)
	}
	for _, want := range []string{
		`"identity":"` + alice + `"`,
		"### " + alice,
		"Hello **world**, from a \\<script>alert(1)\\</script> visitor\n- one\n- two",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("public log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(strings.ReplaceAll(log, `\<`, ""), "<script") {
		t.Errorf("public log carries live markup:\n%s", log)
	}
	if status, listing := public("/pub/guestbook/"); status != http.StatusOK || !strings.Contains(listing, "entries-000001.md") {
		t.Fatalf("public folder = %d %s", status, listing)
	}
	if status, top := public("/pub/"); status != http.StatusOK || !strings.Contains(top, `"guestbook"`) {
		t.Fatalf("public index = %d %s", status, top)
	}

	// A restart loses nothing: the new process reads the book from the drive.
	stop()
	srv, origin, stop = serve()
	defer stop()
	entries := srv.Entries()
	if len(entries) != 1 || entries[0].Identity != alice || !strings.Contains(entries[0].Message, "Hello **world**") {
		t.Fatalf("after a restart: %+v", entries)
	}
	listed, err := http.Get(origin + "/api/entries")
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Entries []struct{ HTML string } `json:"entries"`
	}
	_ = json.NewDecoder(listed.Body).Decode(&page)
	listed.Body.Close()
	if len(page.Entries) != 1 || !strings.Contains(page.Entries[0].HTML, "<strong>world</strong>") || strings.Contains(page.Entries[0].HTML, "<script") {
		t.Fatalf("page entries: %+v", page.Entries)
	}

	// The operator's tool is the file: download it as the guestbook's ID,
	// delete the entry, upload it again. The page forgets it at the next refresh.
	edited := filepath.Join(t.TempDir(), "entries.md")
	t.Setenv("HOME", bookHome)
	runCLI(t, bookHome, "drive", "get", "/guestbook/entries-000001.md", edited, "--json", "--use-identity", book)
	if err := os.WriteFile(edited, []byte("(entry removed by the operator)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, bookHome, "drive", "put", edited, "/guestbook/entries-000001.md", "--json", "--use-identity", book)
	if err := srv.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := srv.Entries(); len(got) != 0 {
		t.Fatalf("a removed entry is still listed: %+v", got)
	}
	if _, log := public("/pub/guestbook/entries-000001.md"); strings.Contains(log, "world") {
		t.Fatalf("a removed entry is still public:\n%s", log)
	}
}

// signInWithCLI signs a browser in at the guestbook by approving from the
// CLI, which is approving from another device: the browser polls.
func signInWithCLI(t *testing.T, origin, home string) *http.Cookie {
	t.Helper()
	resp, err := http.Post(origin+"/auth/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var started guestbook.StartResponse
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	runCLI(t, home, "auth", "approve", started.DeepLink, "--sign-with", "identity", "--code", started.MatchCode)
	poll, err := http.Get(origin + "/auth/poll?request_id=" + url.QueryEscape(started.RequestID) +
		"&poll_secret=" + url.QueryEscape(started.PollSecret))
	if err != nil {
		t.Fatal(err)
	}
	defer poll.Body.Close()
	for _, c := range poll.Cookies() {
		if c.Name == "guestbook_session" {
			return c
		}
	}
	t.Fatal("the poll did not return a session")
	return nil
}
