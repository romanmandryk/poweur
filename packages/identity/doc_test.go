package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocumentSignVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := NewDocument("alice.poweur.net", FormatEd25519PublicKey(pub), "x25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "relay.poweur.net", nil)
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	if err := doc.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentTamperFails(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	doc := NewDocument("alice.poweur.net", FormatEd25519PublicKey(pub), "", "relay.poweur.net", nil)
	_ = doc.Sign(priv)
	doc.Relay = "evil.example"
	if err := doc.Verify(); err == nil {
		t.Fatal("expected verify failure after tamper")
	}
}

func TestCanonicalStable(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	doc := NewDocument("bob.poweur.net", FormatEd25519PublicKey(pub), "", "r.example", []string{"messaging", "files"})
	doc.UpdatedAt = "2026-01-01T00:00:00Z"
	a, err := doc.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	b, err := doc.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical not stable:\n%s\n%s", a, b)
	}
	// Signature must not appear in canonical bytes
	if strings.Contains(string(a), "signature") {
		t.Fatal("signature must be omitted from canonical bytes")
	}
}

func TestValidateIdentityName(t *testing.T) {
	if err := ValidateIdentityName("alice.poweur.net"); err != nil {
		t.Fatal(err)
	}
	// Reserved names are held back from claiming, not from use: an operator
	// may create one, and then everyone must be able to reach it.
	if err := ValidateIdentityName("support.poweur.net"); err != nil {
		t.Fatalf("reserved but existing name refused: %v", err)
	}
	if err := ValidateClaimableName("support.poweur.net"); err == nil {
		t.Fatal("reserved name claimable")
	}
	if err := ValidateClaimableName("alice.poweur.net"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIdentityName("127.0.0.1"); err == nil {
		t.Fatal("ip literal")
	}
	if err := ValidateIdentityName("../evil"); err == nil {
		t.Fatal("path")
	}
}

func TestSanitizeIdentityDirName(t *testing.T) {
	s, err := SanitizeIdentityDirName("alice.poweur.net")
	if err != nil {
		t.Fatal(err)
	}
	if s != "alice__poweur__net" {
		t.Fatal(s)
	}
	// An operator-created reserved name still needs a home directory.
	if s, err := SanitizeIdentityDirName("support.poweur.net"); err != nil || s != "support__poweur__net" {
		t.Fatalf("reserved name dir: %q %v", s, err)
	}
	if _, err := SanitizeIdentityDirName("../evil"); err == nil {
		t.Fatal("path should fail validate")
	}
}

func TestIsUnderDomain(t *testing.T) {
	if !IsUnderDomain("alice.poweur.net", "poweur.net") {
		t.Fatal()
	}
	if IsUnderDomain("alice.evil.net", "poweur.net") {
		t.Fatal()
	}
}

func TestResolveWeb(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	doc := NewDocument("alice.test", FormatEd25519PublicKey(pub), "", "relay.test", nil)
	doc.UpdatedAt = "2026-01-01T00:00:00Z"
	_ = doc.Sign(priv)
	raw, _ := json.Marshal(doc)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/poweur/id.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// httptest host is 127.0.0.1:port — use AllowPrivate and custom client with Host rewrite
	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// Resolve against the test server by using identity that matches doc but
	// pointing HTTPClient at ts. We need Host header = alice.test while
	// dialing ts.URL. Use a transport that rewrites.
	transport := &hostRewriteTransport{base: ts.Client().Transport, host: strings.TrimPrefix(ts.URL, "http://")}
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return errNoRedirect
	}}

	// Override identity in URL: scheme http, but identity alice.test won't resolve DNS.
	// Skip host safety via AllowPrivate; Dial goes to rewrite host.
	res, err := Resolve(context.Background(), "alice.test", ResolveOptions{
		Scheme:       "http",
		AllowPrivate: true,
		SkipDNS:      true,
		HTTPClient:   httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceWeb {
		t.Fatal(res.Source)
	}
	if NormalizePublicKeyKey(res.Document.PublicKey) != NormalizePublicKeyKey(FormatEd25519PublicKey(pub)) {
		t.Fatal(res.Document.PublicKey)
	}
}

type hostRewriteTransport struct {
	base http.RoundTripper
	host string
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u := *req.URL
	u.Host = t.host
	u.Scheme = "http"
	req2 := req.Clone(req.Context())
	req2.URL = &u
	req2.Host = "alice.test"
	if t.base == nil {
		t.base = http.DefaultTransport
	}
	return t.base.RoundTrip(req2)
}

var errNoRedirect = errorsNew("no redirects")

func errorsNew(s string) error { return &strError{s} }

type strError struct{ s string }

func (e *strError) Error() string { return e.s }

func TestResolveRedirectRefused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/poweur/id.json", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	transport := &hostRewriteTransport{base: ts.Client().Transport, host: strings.TrimPrefix(ts.URL, "http://")}
	httpClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errNoRedirect
		},
	}
	_, err := Resolve(context.Background(), "alice.test", ResolveOptions{
		Scheme: "http", AllowPrivate: true, SkipDNS: true, HTTPClient: httpClient,
	})
	if err == nil {
		t.Fatal("expected error on redirect")
	}
}

func TestResolveTooLarge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/poweur/id.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaxDocumentBytes+10)))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	transport := &hostRewriteTransport{base: ts.Client().Transport, host: strings.TrimPrefix(ts.URL, "http://")}
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return errNoRedirect
	}}
	_, err := Resolve(context.Background(), "alice.test", ResolveOptions{
		Scheme: "http", AllowPrivate: true, SkipDNS: true, HTTPClient: httpClient,
	})
	if err == nil || !strings.Contains(err.Error(), "16KB") {
		t.Fatalf("expected 16KB error, got %v", err)
	}
}

type fakeTXT map[string][]string

func (f fakeTXT) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if v, ok := f[name]; ok {
		return v, nil
	}
	return nil, errNoTXT
}

var errNoTXT = &strError{"no such TXT"}

func TestResolveDNSOnly(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	b64 := FormatEd25519PublicKey(pub)
	b64 = strings.TrimPrefix(b64, "ed25519:")
	txt := fakeTXT{
		"_poweur.bob.poweur.net": {"poweur-pubkey=ed25519:" + b64},
	}
	res, err := Resolve(context.Background(), "bob.poweur.net", ResolveOptions{
		SkipWeb: true,
		TXT:     txt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceDNS {
		t.Fatal(res.Source)
	}
}

func TestResolveKeyMismatch(t *testing.T) {
	pub1, priv1, _ := ed25519.GenerateKey(nil)
	pub2, _, _ := ed25519.GenerateKey(nil)
	doc := NewDocument("alice.test", FormatEd25519PublicKey(pub1), "", "r", nil)
	doc.UpdatedAt = "2026-01-01T00:00:00Z"
	_ = doc.Sign(priv1)
	raw, _ := json.Marshal(doc)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/poweur/id.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(raw)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	transport := &hostRewriteTransport{base: ts.Client().Transport, host: strings.TrimPrefix(ts.URL, "http://")}
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return errNoRedirect
	}}

	b64 := strings.TrimPrefix(FormatEd25519PublicKey(pub2), "ed25519:")
	txt := fakeTXT{
		"_poweur.alice.test": {"poweur-pubkey=ed25519:" + b64},
	}
	_, err := Resolve(context.Background(), "alice.test", ResolveOptions{
		Scheme: "http", AllowPrivate: true, HTTPClient: httpClient, TXT: txt,
	})
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch, got %v", err)
	}
}
