package identity

import (
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

	"github.com/poweur/cli/internal/config"
	idpkg "github.com/poweur/identity"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type noTXT struct{}

func (noTXT) LookupTXT(context.Context, string) ([]string, error) {
	return nil, errors.New("no such host")
}
func (noTXT) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("no such host")
}
func (noTXT) LookupCNAME(context.Context, string) (string, error) {
	return "", errors.New("no such host")
}

// cacheFixture gives the CLI resolver a fake web host for bob and a HOME of
// its own. up=false makes every fetch fail at the connection.
type cacheFixture struct {
	up     bool
	status int
}

const cachedContact = "bob.example.org"

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	pub, priv, _ := ed25519.GenerateKey(nil)
	doc := idpkg.NewDocument(cachedContact, idpkg.FormatEd25519PublicKey(pub),
		idpkg.FormatX25519PublicKey(make([]byte, 32)), "relay.example.org", nil)
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)

	f := &cacheFixture{up: true, status: http.StatusOK}
	ConfigureResolver("https", false, "", &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if !f.up {
			return nil, errors.New("dial tcp: connection refused")
		}
		body := ""
		if f.status == http.StatusOK {
			body = string(raw)
		}
		return &http.Response{StatusCode: f.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	SetResolver(noTXT{})
	SetMaxStale(DefaultMaxStale)
	t.Cleanup(func() {
		ConfigureResolver("https", false, "", nil)
		ResetResolver()
		SetMaxStale(DefaultMaxStale)
	})
	return f
}

func TestResolveIdentity_WritesTheCacheButNeverReadsIt(t *testing.T) {
	f := newCacheFixture(t)
	ctx := context.Background()
	if _, err := ResolveIdentity(ctx, cachedContact); err != nil {
		t.Fatal(err)
	}
	dir, _ := config.IdentityCacheDir()
	info, err := os.Stat(filepath.Join(dir, cachedContact+".json"))
	if err != nil {
		t.Fatalf("identity not cached: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode %v, want 0600", info.Mode().Perm())
	}

	f.up = false
	if res, err := ResolveIdentity(ctx, cachedContact); err == nil {
		t.Fatalf("ResolveIdentity returned a cached document: %+v", res)
	}
}

func TestResolveIdentityCached_FallsBackOnlyDuringAnOutage(t *testing.T) {
	f := newCacheFixture(t)
	ctx := context.Background()
	if res, err := ResolveIdentityCached(ctx, cachedContact); err != nil || res.Stale {
		t.Fatalf("fresh resolve: stale=%v err=%v", res.Stale, err)
	}

	f.up = false
	res, err := ResolveIdentityCached(ctx, cachedContact)
	if err != nil || !res.Stale || res.Source != idpkg.SourceStale {
		t.Fatalf("outage: stale=%v source=%s err=%v", res.Stale, res.Source, err)
	}
	key, info, err := LookupEncryptionKeyCached(ctx, cachedContact)
	if err != nil || len(key) != 32 || !info.Stale {
		t.Fatalf("encryption key from cache: len=%d stale=%v err=%v", len(key), info.Stale, err)
	}

	// The host is up and says the identity is gone: that is an answer.
	f.up, f.status = true, http.StatusNotFound
	if _, err := ResolveIdentityCached(ctx, cachedContact); err == nil {
		t.Fatal("cached document used although the host answered 404")
	}
}

func TestResolveIdentityCached_RespectsMaxStale(t *testing.T) {
	f := newCacheFixture(t)
	ctx := context.Background()
	if _, err := ResolveIdentity(ctx, cachedContact); err != nil {
		t.Fatal(err)
	}
	f.up = false

	SetMaxStale(0)
	if _, err := ResolveIdentityCached(ctx, cachedContact); err == nil {
		t.Fatal("cached document used with max stale 0")
	}

	// A document fetched a week ago, with a limit of a day.
	store := identityStore()
	stored, ok := store.Get(cachedContact)
	if !ok {
		t.Fatal("nothing cached")
	}
	if err := store.Put(cachedContact, stored.Raw, time.Now().Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	SetMaxStale(24 * time.Hour)
	if _, err := ResolveIdentityCached(ctx, cachedContact); err == nil {
		t.Fatal("cached document older than max stale was used")
	}
	SetMaxStale(DefaultMaxStale)
	if res, err := ResolveIdentityCached(ctx, cachedContact); err != nil || !res.Stale {
		t.Fatalf("within 30 days: stale=%v err=%v", res.Stale, err)
	}
}

func TestLookupEncryptionKeyCached_SaysWhenNothingIsCached(t *testing.T) {
	f := newCacheFixture(t)
	f.up = false
	_, _, err := LookupEncryptionKeyCached(context.Background(), cachedContact)
	if err == nil || !strings.Contains(err.Error(), "no cached identity for "+cachedContact) {
		t.Fatalf("err = %v", err)
	}
}
