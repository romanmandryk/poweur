package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	idpkg "github.com/poweur/identity"
)

// hostedID is an identity registered on a test relay.
type hostedID struct {
	name string
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

// newTestRelay starts a relay with persistence in a temp dir; system files
// live under it and tests set them with setSysFile.
func newTestRelay(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := config.Config{
		ListenAddr:           ":0",
		RelayAddress:         "relay.test",
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "test",
		DataDir:              t.TempDir(),
		HostedDomains:        []string{"poweur.net"},
		ResolverAllowPrivate: true,
		RateLimits:           config.RateLimits{PerMinute: 100000, PerHour: 100000, PerDay: 100000},
	}
	server := NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	t.Cleanup(ts.Close)
	server.cfg.RelayAddress = strings.TrimPrefix(ts.URL, "http://")
	return server, ts
}

// registerTestIdentity registers name on the relay with a fresh key.
func registerTestIdentity(t *testing.T, server *Server, ts *httptest.Server, name string) hostedID {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "n-" + name
	canon := crypto.CanonicalIdentityRegistration(name, pubB64, "", server.cfg.RelayAddress, issued, nonce)
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canon)))
	doc := idpkg.NewDocument(name, idpkg.FormatEd25519PublicKey(pub), "", server.cfg.RelayAddress, nil)
	doc.UpdatedAt = issued
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	docRaw, _ := json.Marshal(doc)
	body, _ := json.Marshal(IdentityRequest{
		Identity: name, PublicKey: pubB64, IssuedAt: issued, Nonce: nonce,
		IdentitySignature: sig, IdentityDocument: docRaw,
	})
	resp, err := http.Post(ts.URL+"/identities", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("register %s: %d %s", name, resp.StatusCode, raw)
	}
	return hostedID{name: name, pub: pub, priv: priv}
}

// setSysFile stores a system document for identity, as the owner's drive
// commit would. A relay built without system files gets an in-memory store.
func setSysFile(t *testing.T, server *Server, identity, path, body string) {
	t.Helper()
	if _, ok := server.sysFiles.(noSystemFiles); ok {
	}
	if err := server.sysFiles.Write(t.Context(), identity, path, []byte(body)); err != nil {
		t.Fatalf("set %s %s: %v", identity, path, err)
	}
}

// httpReq sends a request with an optional bearer token and headers.
func httpReq(t *testing.T, ts *httptest.Server, method, path, token string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
