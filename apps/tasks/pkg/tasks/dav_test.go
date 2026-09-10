package tasks

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDAVURL(t *testing.T) {
	cases := []struct {
		name    string
		client  DAVClient
		path    string
		want    string
		wantErr bool
	}{
		{
			name:   "simple",
			client: DAVClient{BaseURL: "https://relay.example", Owner: "alice.example.org"},
			path:   "/apps/net.poweur.tasks/manifest.json",
			want:   "https://relay.example/dav/alice.example.org/apps/net.poweur.tasks/manifest.json",
		},
		{
			name:   "trailing slash on base is trimmed",
			client: DAVClient{BaseURL: "https://relay.example/", Owner: "Alice.Example.ORG"},
			path:   "apps/x",
			want:   "https://relay.example/dav/alice.example.org/apps/x",
		},
		{
			name:    "no base url",
			client:  DAVClient{Owner: "alice.example.org"},
			path:    "/apps/x",
			wantErr: true,
		},
		{
			name:    "no owner",
			client:  DAVClient{BaseURL: "https://relay.example"},
			path:    "/apps/x",
			wantErr: true,
		},
		{
			name:    "owner with a slash cannot repoint the request",
			client:  DAVClient{BaseURL: "https://relay.example", Owner: "alice/../bob"},
			path:    "/apps/x",
			wantErr: true,
		},
		{
			name:    "owner with dot-dot",
			client:  DAVClient{BaseURL: "https://relay.example", Owner: "..bob"},
			path:    "/apps/x",
			wantErr: true,
		},
		{
			name:    "empty path",
			client:  DAVClient{BaseURL: "https://relay.example", Owner: "alice.example.org"},
			path:    "/",
			wantErr: true,
		},
		{
			name:   "traversal is cleaned away, never sent",
			client: DAVClient{BaseURL: "https://relay.example", Owner: "alice.example.org"},
			path:   "/apps/net.poweur.tasks/../../private/diary",
			want:   "https://relay.example/dav/alice.example.org/private/diary",
		},
		{
			name:   "spaces in a segment are escaped",
			client: DAVClient{BaseURL: "https://relay.example", Owner: "alice.example.org"},
			path:   "/apps/a b/c",
			want:   "https://relay.example/dav/alice.example.org/apps/a%20b/c",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t2 *testing.T) {
			got, err := tc.client.url(tc.path)
			if tc.wantErr {
				if err == nil {
					t2.Fatalf("url(%q) = %q, want error", tc.path, got)
				}
				return
			}
			if err != nil {
				t2.Fatalf("url(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t2.Fatalf("url(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestDAVRoundTrip(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	c := f.client(ts.URL)
	ctx := context.Background()

	if err := c.Mkcol(ctx, "/apps/net.poweur.tasks"); err != nil {
		t.Fatal(err)
	}
	// Idempotent: the relay answers 405 for an existing collection.
	if err := c.Mkcol(ctx, "/apps/net.poweur.tasks"); err != nil {
		t.Fatalf("second MKCOL should be a no-op: %v", err)
	}
	if err := c.Put(ctx, "/apps/net.poweur.tasks/a.json", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	body, err := c.Get(ctx, "/apps/net.poweur.tasks/a.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"a":1}` {
		t.Fatalf("GET body %q", body)
	}
	entries, err := c.List(ctx, "/apps/net.poweur.tasks")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "a.json" || entries[0].IsCollection {
		t.Fatalf("List = %+v", entries)
	}
	if err := c.Delete(ctx, "/apps/net.poweur.tasks/a.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "/apps/net.poweur.tasks/a.json"); !NotFound(err) {
		t.Fatalf("GET after DELETE: %v, want not-found", err)
	}
}

func TestDAVListSeparatesCollections(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	f.put("/apps/net.poweur.tasks/projects/prj_a/project.json", []byte(`{}`))
	f.put("/apps/net.poweur.tasks/projects/prj_b/project.json", []byte(`{}`))
	f.put("/apps/net.poweur.tasks/projects/stray.txt", []byte(`x`))

	entries, err := f.client(ts.URL).List(context.Background(), "/apps/net.poweur.tasks/projects")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name] = e.IsCollection
	}
	if len(got) != 3 || !got["prj_a"] || !got["prj_b"] || got["stray.txt"] {
		t.Fatalf("List = %+v", got)
	}
}

func TestDAVErrorClassification(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	f.fail["/apps/net.poweur.tasks/forbidden.json"] = http.StatusForbidden
	f.fail["/apps/net.poweur.tasks/gone.json"] = http.StatusGone
	f.fail["/apps/net.poweur.tasks/boom.json"] = http.StatusInternalServerError
	c := f.client(ts.URL)
	ctx := context.Background()

	_, err := c.Get(ctx, "/apps/net.poweur.tasks/forbidden.json")
	if !Forbidden(err) || NotFound(err) {
		t.Fatalf("403 classified as %v", err)
	}
	_, err = c.Get(ctx, "/apps/net.poweur.tasks/gone.json")
	if !NotFound(err) {
		t.Fatalf("410 classified as %v", err)
	}
	_, err = c.Get(ctx, "/apps/net.poweur.tasks/boom.json")
	if NotFound(err) || Forbidden(err) {
		t.Fatalf("500 misclassified: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should name the status: %v", err)
	}
	if NotFound(nil) || Forbidden(nil) {
		t.Fatal("nil must not classify as a status error")
	}
}

// A token scoped to the app namespace must not be able to reach the rest of
// the home, and the app must report that as a scope problem.
func TestDAVScopedTokenIsEnforcedAndExplained(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	f.token = "tok"
	f.scope = NamespaceRoot + "/"
	c := f.client(ts.URL)
	ctx := context.Background()

	_, err := c.Get(ctx, "/private/diary.md")
	if !Forbidden(err) {
		t.Fatalf("out-of-scope read: %v, want 403", err)
	}
	if !strings.Contains(explain(err), TokenScope) {
		t.Fatalf("explain() should name the scope to mint: %s", explain(err))
	}

	bad := &DAVClient{BaseURL: ts.URL, Owner: "alice.example.org", Token: "wrong"}
	if _, err := bad.Get(ctx, NamespaceRoot+"/manifest.json"); !Forbidden(err) {
		t.Fatalf("wrong token: %v, want 401", err)
	}
}

func TestDAVGetCapsResponseSize(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	f.raw["/apps/net.poweur.tasks/huge.json"] = strings.Repeat("x", MaxDocBytes*2)
	body, err := f.client(ts.URL).Get(context.Background(), "/apps/net.poweur.tasks/huge.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > MaxDocBytes+1 {
		t.Fatalf("read %d bytes; a hostile remote must not be able to stream unbounded", len(body))
	}
}
