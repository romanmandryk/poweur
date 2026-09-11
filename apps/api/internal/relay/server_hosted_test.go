package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/dns"
	idpkg "github.com/poweur/identity"
)

func TestHostedRegistrationAndWellKnown(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		ListenAddr:           ":0",
		RelayAddress:         "relay.test",
		RelayScheme:          "http",
		DNSTTL:               time.Minute,
		ChallengeTTL:         time.Minute,
		Version:              "test",
		DataDir:              dir,
		HostedDomains:        []string{"poweur.net"},
		ResolverAllowPrivate: true,
		RateLimits:           config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	server := NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	defer ts.Close()
	// Point virtual host dialer at this test server
	server.cfg.RelayAddress = strings.TrimPrefix(ts.URL, "http://")

	pub, priv, _ := ed25519.GenerateKey(nil)
	encPub := make([]byte, 32)
	copy(encPub, pub[:32]) // not a real x25519 but Normalize needs 32 bytes
	encB64 := base64.RawURLEncoding.EncodeToString(encPub)

	identity := "alicehost.poweur.net"
	pubB64 := base64.RawURLEncoding.EncodeToString(pub)
	doc := idpkg.NewDocument(identity, idpkg.FormatEd25519PublicKey(pub), idpkg.FormatX25519PublicKey(encPub), server.cfg.RelayAddress, nil)
	doc.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := doc.Sign(priv); err != nil {
		t.Fatal(err)
	}
	docRaw, _ := json.Marshal(doc)

	issued := time.Now().UTC().Format(time.RFC3339)
	nonce := "n1"
	canon := crypto.CanonicalIdentityRegistration(identity, pubB64, encB64, server.cfg.RelayAddress, issued, nonce)
	sig := ed25519.Sign(priv, []byte(canon))

	reqBody := IdentityRequest{
		Identity:            identity,
		PublicKey:           pubB64,
		EncryptionPublicKey: encB64,
		IssuedAt:            issued,
		Nonce:               nonce,
		IdentitySignature:   base64.RawURLEncoding.EncodeToString(sig),
		IdentityDocument:    docRaw,
	}
	body, _ := json.Marshal(reqBody)
	resp, err := http.Post(ts.URL+"/identities", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		var er ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&er)
		t.Fatalf("status %d: %+v", resp.StatusCode, er)
	}

	// well-known via Host header
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/.well-known/poweur/id.json", nil)
	req.Host = identity
	wk, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer wk.Body.Close()
	if wk.StatusCode != http.StatusOK {
		t.Fatalf("well-known %d", wk.StatusCode)
	}
	var got idpkg.IdentityDocument
	if err := json.NewDecoder(wk.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(); err != nil {
		t.Fatal(err)
	}

	// A conforming did:web resolver turns did:web:<host> into this exact
	// well-known request, then requires the returned document id to match.
	didReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/.well-known/did.json", nil)
	didReq.Host = identity
	didResp, err := http.DefaultClient.Do(didReq)
	if err != nil {
		t.Fatal(err)
	}
	defer didResp.Body.Close()
	if didResp.StatusCode != http.StatusOK {
		t.Fatalf("did:web well-known %d", didResp.StatusCode)
	}
	if ct := didResp.Header.Get("Content-Type"); ct != "application/did+json" {
		t.Fatalf("did:web content type = %q", ct)
	}
	var did idpkg.DIDWebDocument
	if err := json.NewDecoder(didResp.Body).Decode(&did); err != nil {
		t.Fatal(err)
	}
	if did.ID != "did:web:"+identity {
		t.Fatalf("resolved DID id = %q", did.ID)
	}
	if gotKey := did.VerificationMethod[0].PublicKeyJWK.X; gotKey != pubB64 {
		t.Fatalf("DID signing key = %q, identity document key = %q", gotKey, pubB64)
	}

	// durable reload
	server2 := NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
	server2.cfg.RelayAddress = server.cfg.RelayAddress
	if !server2.identities.Exists(identity) {
		t.Fatal("identity lost after reopen")
	}
	_ = filepath.Join(dir, "identities")
}

func TestHostedRejectsReservedAndForeignDomain(t *testing.T) {
	cfg := config.Config{
		RelayAddress:  "relay.test",
		RelayScheme:   "http",
		HostedDomains: []string{"poweur.net"},
		RateLimits:    config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
	}
	server := NewServer(cfg, dns.NewNetResolver(), dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	try := func(name string) int {
		pub, priv, _ := ed25519.GenerateKey(nil)
		pubB64 := base64.RawURLEncoding.EncodeToString(pub)
		issued := time.Now().UTC().Format(time.RFC3339)
		nonce := "n"
		canon := crypto.CanonicalIdentityRegistration(name, pubB64, "", cfg.RelayAddress, issued, nonce)
		sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canon)))
		doc := idpkg.NewDocument(name, idpkg.FormatEd25519PublicKey(pub), "", cfg.RelayAddress, nil)
		doc.UpdatedAt = issued
		_ = doc.Sign(priv)
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
		return resp.StatusCode
	}
	if try("www.poweur.net") == http.StatusCreated {
		t.Fatal("reserved should fail")
	}
	if try("alice.evil.net") == http.StatusCreated {
		t.Fatal("foreign domain should fail")
	}
}
