package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeWeb serves canned well-known answers per host. A host with no entry
// fails at the transport, like a dead DNS name.
type fakeWeb map[string]fakeAnswer

type fakeAnswer struct {
	status int
	body   []byte
}

func (f fakeWeb) client() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		answer, ok := f[r.URL.Host]
		if !ok {
			return nil, errors.New("dial tcp: no such host")
		}
		return &http.Response{
			StatusCode: answer.status,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader(answer.body)),
			Request:    r,
		}, nil
	})}
}

func signedDoc(t *testing.T, priv ed25519.PrivateKey, identity, updatedAt string, mutate func(*IdentityDocument)) (IdentityDocument, []byte) {
	t.Helper()
	pub := priv.Public().(ed25519.PublicKey)
	doc := NewDocument(identity, FormatEd25519PublicKey(pub), FormatX25519PublicKey(VectorSeed), "relay.example.org", nil)
	doc.UpdatedAt = updatedAt
	if mutate != nil {
		mutate(&doc)
	}
	if err := doc.Sign(priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return doc, raw
}

// Shared vectors for the last-known-good store (EPIC-029 E29-T1). The TS
// client runs the same cases in packages/client-ts/test/resolve-store.test.ts.

type storeVectorStored struct {
	Document  IdentityDocument `json:"document"`
	FetchedAt string           `json:"fetched_at"`
}

type storeVectorWeb struct {
	// NetworkError: the request never gets an answer.
	NetworkError bool `json:"network_error,omitempty"`
	Status       int  `json:"status,omitempty"`
	// Document is served as the body of a 200.
	Document *IdentityDocument `json:"document,omitempty"`
}

type storeVectorExpect struct {
	// Outcome is "fresh", "stale" or "error".
	Outcome string `json:"outcome"`
	Source  string `json:"source,omitempty"`
	// Error is "rollback", "key_mismatch" or "failed".
	Error string `json:"error,omitempty"`
	// UpdatedAt is the returned document's updated_at (signed results only).
	UpdatedAt string `json:"updated_at,omitempty"`
	// StoredUpdatedAt is what the store holds afterwards; empty for nothing.
	StoredUpdatedAt string `json:"stored_updated_at,omitempty"`
}

type storeVectorCase struct {
	Name            string             `json:"name"`
	Stored          *storeVectorStored `json:"stored,omitempty"`
	Web             storeVectorWeb     `json:"web"`
	DNSPublicKey    string             `json:"dns_public_key,omitempty"`
	MaxStaleSeconds int                `json:"max_stale_seconds"`
	Expect          storeVectorExpect  `json:"expect"`
}

type storeVectors struct {
	Identity string            `json:"identity"`
	Now      string            `json:"now"`
	Cases    []storeVectorCase `json:"cases"`
}

func buildStoreVectors(t *testing.T) storeVectors {
	t.Helper()
	const identity = "alice.example.org"
	const older = "2026-01-10T09:30:00Z"
	const newer = VectorTime
	const now = "2026-02-01T00:00:00Z"
	const dayAgo = "2026-01-31T00:00:00Z"
	const tooLongAgo = "2025-12-31T00:00:00Z" // 32 days before now
	const thirtyDays = 30 * 24 * 3600

	priv := vectorKey()
	otherPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	otherKey := FormatEd25519PublicKey(otherPriv.Public().(ed25519.PublicKey))
	ownKey := FormatEd25519PublicKey(priv.Public().(ed25519.PublicKey))

	oldDoc, _ := signedDoc(t, priv, identity, older, nil)
	newDoc, _ := signedDoc(t, priv, identity, newer, nil)
	tampered := newDoc
	tampered.Relay = "evil.example.org"

	stored := func(doc IdentityDocument, at string) *storeVectorStored {
		return &storeVectorStored{Document: doc, FetchedAt: at}
	}
	down := storeVectorWeb{NetworkError: true}

	return storeVectors{Identity: identity, Now: now, Cases: []storeVectorCase{
		{Name: "fresh answer replaces the stored document", Stored: stored(oldDoc, dayAgo),
			Web: storeVectorWeb{Status: 200, Document: &newDoc}, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "fresh", Source: "web", UpdatedAt: newer, StoredUpdatedAt: newer}},
		{Name: "first fetch is stored", Web: storeVectorWeb{Status: 200, Document: &newDoc}, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "fresh", Source: "web", UpdatedAt: newer, StoredUpdatedAt: newer}},
		{Name: "network error falls back to the stored document", Stored: stored(newDoc, dayAgo),
			Web: down, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "stale", Source: "stale", UpdatedAt: newer, StoredUpdatedAt: newer}},
		{Name: "5xx falls back to the stored document", Stored: stored(newDoc, dayAgo),
			Web: storeVectorWeb{Status: 503}, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "stale", Source: "stale", UpdatedAt: newer, StoredUpdatedAt: newer}},
		{Name: "stored document past max_stale is not used", Stored: stored(newDoc, tooLongAgo),
			Web: down, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "error", Error: "failed", StoredUpdatedAt: newer}},
		{Name: "max_stale zero never returns a stored document", Stored: stored(newDoc, dayAgo),
			Web: down, MaxStaleSeconds: 0,
			Expect: storeVectorExpect{Outcome: "error", Error: "failed", StoredUpdatedAt: newer}},
		{Name: "nothing stored and nothing reachable", Web: down, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "error", Error: "failed"}},
		{Name: "404 is an answer, not an outage", Stored: stored(newDoc, dayAgo),
			Web: storeVectorWeb{Status: 404}, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "error", Error: "failed", StoredUpdatedAt: newer}},
		{Name: "bad signature is not hidden by the store", Stored: stored(oldDoc, dayAgo),
			Web: storeVectorWeb{Status: 200, Document: &tampered}, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "error", Error: "failed", StoredUpdatedAt: older}},
		{Name: "web and DNS key mismatch is not hidden by the store", Stored: stored(oldDoc, dayAgo),
			Web: storeVectorWeb{Status: 200, Document: &newDoc}, DNSPublicKey: otherKey, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "error", Error: "key_mismatch", StoredUpdatedAt: older}},
		{Name: "older document than the stored one is a rollback", Stored: stored(newDoc, dayAgo),
			Web: storeVectorWeb{Status: 200, Document: &oldDoc}, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "error", Error: "rollback", StoredUpdatedAt: newer}},
		{Name: "DNS confirming the stored key returns the stored document", Stored: stored(newDoc, dayAgo),
			Web: down, DNSPublicKey: ownKey, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "stale", Source: "stale", UpdatedAt: newer, StoredUpdatedAt: newer}},
		{Name: "DNS answering with another key wins over the stored document", Stored: stored(newDoc, dayAgo),
			Web: down, DNSPublicKey: otherKey, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "fresh", Source: "dns", StoredUpdatedAt: newer}},
		{Name: "DNS-only result is never stored", Web: down, DNSPublicKey: ownKey, MaxStaleSeconds: thirtyDays,
			Expect: storeVectorExpect{Outcome: "fresh", Source: "dns"}},
	}}
}

func TestVectors_ResolveStore(t *testing.T) {
	vectors := buildStoreVectors(t)
	WriteVectors(t, vectorsDir, "resolve-store", vectors)

	now, err := time.Parse(time.RFC3339, vectors.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range vectors.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			store := NewMemoryStore(0)
			if tc.Stored != nil {
				raw, _ := json.Marshal(tc.Stored.Document)
				at, _ := time.Parse(time.RFC3339, tc.Stored.FetchedAt)
				if err := store.Put(vectors.Identity, raw, at); err != nil {
					t.Fatal(err)
				}
			}
			web := fakeWeb{}
			if !tc.Web.NetworkError {
				var body []byte
				if tc.Web.Document != nil {
					body, _ = json.Marshal(tc.Web.Document)
				}
				web[vectors.Identity] = fakeAnswer{status: tc.Web.Status, body: body}
			}
			opts := ResolveOptions{
				HTTPClient: web.client(),
				Store:      store,
				MaxStale:   time.Duration(tc.MaxStaleSeconds) * time.Second,
				Now:        func() time.Time { return now },
			}
			if tc.DNSPublicKey != "" {
				opts.TXT = fakeTXT{"_poweur." + vectors.Identity: {"poweur-pubkey=" + NormalizePublicKeyKey(tc.DNSPublicKey)}}
			}

			res, err := Resolve(context.Background(), vectors.Identity, opts)
			switch tc.Expect.Outcome {
			case "error":
				if err == nil {
					t.Fatalf("expected an error, got %+v", res)
				}
				got := "failed"
				switch {
				case errors.Is(err, ErrDocumentRollback):
					got = "rollback"
				case strings.Contains(err.Error(), "key mismatch"):
					got = "key_mismatch"
				}
				if got != tc.Expect.Error {
					t.Fatalf("error class = %s (%v), want %s", got, err, tc.Expect.Error)
				}
			default:
				if err != nil {
					t.Fatalf("resolve: %v", err)
				}
				if res.Stale != (tc.Expect.Outcome == "stale") {
					t.Fatalf("stale = %v, want outcome %s", res.Stale, tc.Expect.Outcome)
				}
				if string(res.Source) != tc.Expect.Source {
					t.Fatalf("source = %s, want %s", res.Source, tc.Expect.Source)
				}
				if tc.Expect.UpdatedAt != "" && res.Document.UpdatedAt != tc.Expect.UpdatedAt {
					t.Fatalf("updated_at = %s, want %s", res.Document.UpdatedAt, tc.Expect.UpdatedAt)
				}
				if res.Stale && !res.FetchedAt.Equal(mustTime(tc.Stored.FetchedAt)) {
					t.Fatalf("fetched_at = %s, want %s", res.FetchedAt, tc.Stored.FetchedAt)
				}
			}

			after, ok := store.Get(vectors.Identity)
			gotStored := ""
			if ok {
				doc, err := ParseDocument(after.Raw, true)
				if err != nil {
					t.Fatalf("stored document no longer verifies: %v", err)
				}
				gotStored = doc.UpdatedAt
			}
			if gotStored != tc.Expect.StoredUpdatedAt {
				t.Fatalf("stored updated_at = %q, want %q", gotStored, tc.Expect.StoredUpdatedAt)
			}
		})
	}
}

func TestFileStore_RoundTripKeepsUnknownFields(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identities")
	store := NewFileStore(dir, 0)
	raw := []byte(`{"version":1,"identity":"alice.example.org","relay_set":{"seq":3},"relay":"r.example.org"}`)
	at := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	if err := store.Put("Alice.Example.org", raw, at); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("alice.example.org")
	if !ok {
		t.Fatal("stored document not found")
	}
	if !got.FetchedAt.Equal(at) || got.Identity != "alice.example.org" {
		t.Fatalf("got %+v", got)
	}
	var parsed map[string]any
	if err := json.Unmarshal(got.Raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["relay_set"]; !ok {
		t.Fatalf("unknown field lost: %s", got.Raw)
	}
	info, err := os.Stat(filepath.Join(dir, "alice.example.org.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestFileStore_RejectsUnsafeNames(t *testing.T) {
	store := NewFileStore(t.TempDir(), 0)
	for _, name := range []string{"", "../etc/passwd", "a/b.example.org", "localhost", "127.0.0.1"} {
		if err := store.Put(name, []byte(`{}`), time.Now()); err == nil {
			t.Errorf("Put(%q) succeeded", name)
		}
		if _, ok := store.Get(name); ok {
			t.Errorf("Get(%q) found something", name)
		}
	}
}

func TestFileStore_MissingAndCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 0)
	if _, ok := store.Get("alice.example.org"); ok {
		t.Fatal("found a document in an empty store")
	}
	if err := os.WriteFile(filepath.Join(dir, "alice.example.org.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("alice.example.org"); ok {
		t.Fatal("corrupt file returned as a document")
	}
}

func TestStores_EvictOldestPastTheCap(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	names := []string{"a.example.org", "b.example.org", "c.example.org", "d.example.org"}
	dir := t.TempDir()
	// A second FileStore over the same directory stands in for a restart:
	// it has to rebuild its index from the files.
	first := NewFileStore(dir, 3)
	for i, name := range names[:3] {
		if err := first.Put(name, []byte(`{}`), base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	stores := map[string]Store{"memory": NewMemoryStore(3), "file": NewFileStore(dir, 3)}
	for i, name := range names[:3] {
		_ = stores["memory"].Put(name, []byte(`{}`), base.Add(time.Duration(i)*time.Hour))
	}
	for kind, store := range stores {
		if err := store.Put(names[3], []byte(`{}`), base.Add(3*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.Get(names[0]); ok {
			t.Errorf("%s: oldest entry survived the cap", kind)
		}
		for _, name := range names[1:] {
			if _, ok := store.Get(name); !ok {
				t.Errorf("%s: %s was evicted", kind, name)
			}
		}
	}
}

func TestResolve_StaleDocumentIsVerifiedAgain(t *testing.T) {
	const identity = "alice.example.org"
	doc, _ := signedDoc(t, vectorKey(), identity, VectorTime, nil)
	doc.Relay = "evil.example.org" // changed on disk after it was stored
	raw, _ := json.Marshal(doc)
	store := NewMemoryStore(0)
	_ = store.Put(identity, raw, time.Now())
	_, err := Resolve(context.Background(), identity, ResolveOptions{
		HTTPClient: fakeWeb{}.client(), Store: store, MaxStale: time.Hour,
	})
	if err == nil {
		t.Fatal("a stored document with a broken signature was returned")
	}
}

func TestResolve_StaleResultIsCachedBriefly(t *testing.T) {
	const identity = "alice.example.org"
	_, raw := signedDoc(t, vectorKey(), identity, VectorTime, nil)
	store := NewMemoryStore(0)
	_ = store.Put(identity, raw, time.Now())
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection refused")
	})}
	opts := ResolveOptions{HTTPClient: client, Store: store, MaxStale: time.Hour, Cache: NewCache()}
	for i := 0; i < 3; i++ {
		res, err := Resolve(context.Background(), identity, opts)
		if err != nil || !res.Stale {
			t.Fatalf("resolve %d: stale=%v err=%v", i, res.Stale, err)
		}
	}
	if calls != 1 {
		t.Fatalf("dead host fetched %d times, want 1", calls)
	}
}

func TestResolve_ReadOnlyStoreFallsBackButNeverWrites(t *testing.T) {
	const identity = "alice.example.org"
	_, raw := signedDoc(t, vectorKey(), identity, VectorTime, nil)
	store := NewMemoryStore(0)
	web := fakeWeb{identity: {status: 200, body: raw}}
	opts := ResolveOptions{HTTPClient: web.client(), Store: ReadOnlyStore(store), MaxStale: time.Hour}
	res, err := Resolve(context.Background(), identity, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(identity); ok {
		t.Fatal("read-only store was written")
	}
	// The caller stores it once its own check passed.
	if err := store.Put(identity, res.Raw, res.FetchedAt); err != nil {
		t.Fatal(err)
	}
	delete(web, identity)
	res, err = Resolve(context.Background(), identity, opts)
	if err != nil || !res.Stale {
		t.Fatalf("stale=%v err=%v", res.Stale, err)
	}
}

func TestResolve_MovedToStoresTargetUnderItsOwnName(t *testing.T) {
	const source, target = "alice.old.example", "alice.new.example"
	priv := vectorKey()
	_, tombstone := signedDoc(t, priv, source, VectorTime, func(d *IdentityDocument) { d.MovedTo = target })
	_, targetRaw := signedDoc(t, priv, target, VectorTime, nil)
	store := NewMemoryStore(0)
	web := fakeWeb{source: {status: 200, body: tombstone}, target: {status: 200, body: targetRaw}}
	opts := ResolveOptions{HTTPClient: web.client(), Store: store, MaxStale: time.Hour}

	res, err := Resolve(context.Background(), source, opts)
	if err != nil || res.Document.Identity != target || res.Stale {
		t.Fatalf("fresh: %+v %v", res, err)
	}
	kept, ok := store.Get(source)
	if !ok || !bytes.Contains(kept.Raw, []byte(`"moved_to"`)) {
		t.Fatalf("source should keep its tombstone, got %s", kept.Raw)
	}
	if _, ok := store.Get(target); !ok {
		t.Fatal("target not stored under its own name")
	}

	// Both hosts down: the stored tombstone still leads to the stored target.
	opts.HTTPClient = fakeWeb{}.client()
	res, err = Resolve(context.Background(), source, opts)
	if err != nil || res.Document.Identity != target || !res.Stale {
		t.Fatalf("stale: %+v %v", res, err)
	}
}

func TestResolveWeb_OnlyOutagesAreTransient(t *testing.T) {
	const identity = "alice.example.org"
	_, raw := signedDoc(t, vectorKey(), identity, VectorTime, nil)
	store := NewMemoryStore(0)
	_ = store.Put(identity, raw, time.Now())

	redirecting := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": {"https://elsewhere.example.org/"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		}),
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("no redirects") },
	}
	refused := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errPrivateAddress
	})}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name   string
		ctx    context.Context
		client *http.Client
	}{
		{"redirect", context.Background(), redirecting},
		{"refused address", context.Background(), refused},
		{"caller cancelled", cancelled, &http.Client{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Resolve(tc.ctx, identity, ResolveOptions{HTTPClient: tc.client, Store: store, MaxStale: time.Hour})
			if err == nil {
				t.Fatalf("returned %+v; the store must not stand in here", res)
			}
		})
	}
}
