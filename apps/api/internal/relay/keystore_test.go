package relay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	"github.com/poweur/api/internal/storage"
)

// Keystore endpoints (EPIC-011 E11-T1). The bootstrap read is the one place
// the relay authenticates a caller who holds no identity key, so these tests
// build real WebAuthn assertions rather than stubbing verification out.

const testIdentity = "alice.poweur.net"

type keystoreFixture struct {
	server   *Server
	ts       *httptest.Server
	identity string
	priv     ed25519.PrivateKey
}

func newKeystoreFixture(t *testing.T) *keystoreFixture {
	t.Helper()
	server, _ := newTestServer(t)
	server.cfg.HostedDomains = []string{"poweur.net"}
	pub, priv, _ := ed25519.GenerateKey(nil)
	server.identities.Add(storage.Identity{
		Identity:       testIdentity,
		PublicKey:      base64.RawURLEncoding.EncodeToString(pub),
		PublicKeyBytes: pub,
		CreatedAt:      time.Now().UTC(),
	})
	ts := httptest.NewServer(server.Router())
	t.Cleanup(ts.Close)
	return &keystoreFixture{server: server, ts: ts, identity: testIdentity, priv: priv}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (f *keystoreFixture) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var buf io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.ts.URL+path, buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// testCredential is a stand-in authenticator: an ES256 key plus the ability to
// produce assertions over relay challenges.
type testCredential struct {
	id     string
	key    *ecdsa.PrivateKey
	spki   string
	rpID   string
	alg    int
}

func newTestCredential(t *testing.T, rpID string) *testCredential {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return &testCredential{
		id:   b64([]byte("credential-" + rpID)),
		key:  key,
		spki: b64(der),
		rpID: rpID,
		alg:  coseAlgES256,
	}
}

// assert builds a WebAuthn assertion over challenge, with the given flags.
func (c *testCredential) assert(t *testing.T, challenge string, flags byte) WebAuthnAssertion {
	t.Helper()
	clientData, err := json.Marshal(collectedClientData{
		Type:      "webauthn.get",
		Challenge: b64([]byte(challenge)),
		Origin:    "https://" + c.rpID,
	})
	if err != nil {
		t.Fatal(err)
	}
	rpHash := sha256.Sum256([]byte(c.rpID))
	authData := append(append([]byte{}, rpHash[:]...), flags, 0, 0, 0, 1)
	clientHash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authData...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, c.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return WebAuthnAssertion{
		CredentialID:      c.id,
		ClientDataJSON:    b64(clientData),
		AuthenticatorData: b64(authData),
		Signature:         b64(sig),
	}
}

const flagsUPUV = authFlagUserPresent | authFlagUserVerified

// enroll stores a wrapped copy, signed by the identity key.
func (f *keystoreFixture) enroll(t *testing.T, enrollmentID string, cred *testCredential) (int, []byte) {
	t.Helper()
	wrapped := json.RawMessage(`{"iv":"aXY","ciphertext":"Y3Q"}`)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "nonce-" + enrollmentID
	req := KeystoreEnrollRequest{
		EnrollmentID: enrollmentID,
		Kind:         "passkey",
		Wrap:         "prf",
		Payload:      "seed",
		Wrapped:      wrapped,
		Label:        "Test device",
		IssuedAt:     issuedAt,
		Nonce:        nonce,
	}
	if cred != nil {
		req.CredentialID = cred.id
		req.CredentialPublicKey = cred.spki
		req.CredentialAlg = cred.alg
	}
	canonical := crypto.CanonicalKeystoreEnroll(
		f.identity, enrollmentID, req.Kind, req.CredentialID,
		wrappedDigest(wrapped), issuedAt, nonce,
	)
	req.IdentitySignature = b64(ed25519.Sign(f.priv, []byte(canonical)))
	return f.do(t, http.MethodPut, "/identities/"+f.identity+"/keystore", req)
}

// challenge asks the relay for a fresh single-use challenge.
func (f *keystoreFixture) challenge(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(f.ts.URL + "/auth/challenge?identity=" + f.identity)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out ChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Challenge
}

func TestKeystore_EnrollRequiresValidSignature(t *testing.T) {
	f := newKeystoreFixture(t)
	cred := newTestCredential(t, "poweur.net")

	if code, body := f.enroll(t, "e1", cred); code != http.StatusOK {
		t.Fatalf("enroll failed: %d %s", code, body)
	}

	// Same request, signature from a key that is not the identity's.
	_, wrongPriv, _ := ed25519.GenerateKey(nil)
	wrapped := json.RawMessage(`{"iv":"aXY","ciphertext":"Y3Q"}`)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	req := KeystoreEnrollRequest{
		EnrollmentID: "e2", Kind: "passkey", Wrap: "prf", Payload: "seed",
		Wrapped: wrapped, IssuedAt: issuedAt, Nonce: "n2",
		IdentitySignature: b64(ed25519.Sign(wrongPriv, []byte("whatever"))),
	}
	if code, _ := f.do(t, http.MethodPut, "/identities/"+f.identity+"/keystore", req); code != http.StatusUnauthorized {
		t.Fatalf("want 401 for bad signature, got %d", code)
	}
}

// The signature covers a digest of the ciphertext, so a swapped blob under an
// otherwise valid authorization must be rejected.
func TestKeystore_EnrollBindsSignatureToCiphertext(t *testing.T) {
	f := newKeystoreFixture(t)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	original := json.RawMessage(`{"iv":"aXY","ciphertext":"Y3Q"}`)
	canonical := crypto.CanonicalKeystoreEnroll(
		f.identity, "e1", "passkey", "", wrappedDigest(original), issuedAt, "n1")

	req := KeystoreEnrollRequest{
		EnrollmentID: "e1", Kind: "passkey", Wrap: "prf", Payload: "seed",
		Wrapped:           json.RawMessage(`{"iv":"aXY","ciphertext":"c3dhcHBlZA"}`),
		IssuedAt:          issuedAt,
		Nonce:             "n1",
		IdentitySignature: b64(ed25519.Sign(f.priv, []byte(canonical))),
	}
	if code, _ := f.do(t, http.MethodPut, "/identities/"+f.identity+"/keystore", req); code != http.StatusUnauthorized {
		t.Fatalf("swapped ciphertext must fail, got %d", code)
	}
}

func TestKeystore_PasskeyEnrollmentNeedsUsablePublicKey(t *testing.T) {
	f := newKeystoreFixture(t)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	wrapped := json.RawMessage(`{"iv":"aXY","ciphertext":"Y3Q"}`)
	canonical := crypto.CanonicalKeystoreEnroll(
		f.identity, "e1", "passkey", "cred-1", wrappedDigest(wrapped), issuedAt, "n1")
	req := KeystoreEnrollRequest{
		EnrollmentID: "e1", Kind: "passkey", Wrap: "prf", Payload: "seed",
		CredentialID: "cred-1", // no public key
		Wrapped:      wrapped, IssuedAt: issuedAt, Nonce: "n1",
		IdentitySignature: b64(ed25519.Sign(f.priv, []byte(canonical))),
	}
	code, body := f.do(t, http.MethodPut, "/identities/"+f.identity+"/keystore", req)
	if code != http.StatusBadRequest {
		t.Fatalf("want 400 for credential without public key, got %d %s", code, body)
	}
}

func TestKeystore_RejectsPinWrap(t *testing.T) {
	f := newKeystoreFixture(t)
	wrapped := json.RawMessage(`{"iv":"aXY","ciphertext":"Y3Q"}`)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	req := KeystoreEnrollRequest{
		EnrollmentID: "e-pin", Kind: "passkey", Wrap: "pin", Payload: "seed",
		Wrapped: wrapped, IssuedAt: issuedAt, Nonce: "n-pin",
		IdentitySignature: "dGVzdA",
	}
	code, body := f.do(t, http.MethodPut, "/identities/"+f.identity+"/keystore", req)
	if code != http.StatusBadRequest {
		t.Fatalf("want 400 for wrap=pin, got %d %s", code, body)
	}
	if !strings.Contains(string(body), "unknown wrap method") {
		t.Fatalf("want unknown wrap method, got %s", body)
	}
}

// The bootstrap read: no identity key anywhere in this request.
func TestKeystore_FetchWithAssertionReturnsCiphertext(t *testing.T) {
	f := newKeystoreFixture(t)
	cred := newTestCredential(t, "poweur.net")
	if code, body := f.enroll(t, "e1", cred); code != http.StatusOK {
		t.Fatalf("enroll: %d %s", code, body)
	}

	challenge := f.challenge(t)
	code, body := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch",
		KeystoreFetchRequest{Assertion: cred.assert(t, challenge, flagsUPUV), RelyingPartyID: "poweur.net"})
	if code != http.StatusOK {
		t.Fatalf("fetch: %d %s", code, body)
	}
	var out KeystoreFetchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 1 || out.Entries[0].EnrollmentID != "e1" {
		t.Fatalf("unexpected entries: %s", body)
	}
	if !strings.Contains(string(out.Entries[0].Wrapped), "ciphertext") {
		t.Fatal("wrapped ciphertext missing from response")
	}
	// The read is recorded for the inventory UI.
	entry, _ := f.server.keystore.Get(f.identity, "e1")
	if entry.LastUsedAt == "" {
		t.Fatal("expected last_used_at to be set")
	}
}

func TestKeystore_FetchRejectsBadAssertions(t *testing.T) {
	cred := newTestCredential(t, "poweur.net")

	t.Run("no challenge issued", func(t *testing.T) {
		f := newKeystoreFixture(t)
		f.enroll(t, "e1", cred)
		code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch",
			KeystoreFetchRequest{Assertion: cred.assert(t, "never-issued", flagsUPUV)})
		if code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})

	t.Run("signature from another authenticator", func(t *testing.T) {
		f := newKeystoreFixture(t)
		f.enroll(t, "e1", cred)
		other := newTestCredential(t, "poweur.net")
		other.id = cred.id // claims the enrolled credential id, holds the wrong key
		challenge := f.challenge(t)
		code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch",
			KeystoreFetchRequest{Assertion: other.assert(t, challenge, flagsUPUV), RelyingPartyID: "poweur.net"})
		if code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})

	t.Run("wrong relying party", func(t *testing.T) {
		f := newKeystoreFixture(t)
		f.enroll(t, "e1", cred)
		evil := newTestCredential(t, "evil.example")
		evil.id, evil.key, evil.spki = cred.id, cred.key, cred.spki
		challenge := f.challenge(t)
		code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch",
			KeystoreFetchRequest{Assertion: evil.assert(t, challenge, flagsUPUV)})
		if code != http.StatusUnauthorized {
			t.Fatalf("want 401 for foreign rp id, got %d", code)
		}
	})

	t.Run("user verification not performed", func(t *testing.T) {
		f := newKeystoreFixture(t)
		f.enroll(t, "e1", cred)
		challenge := f.challenge(t)
		code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch",
			KeystoreFetchRequest{Assertion: cred.assert(t, challenge, authFlagUserPresent), RelyingPartyID: "poweur.net"})
		if code != http.StatusUnauthorized {
			t.Fatalf("want 401 without UV, got %d", code)
		}
	})
}

// A challenge is single-use, so a captured assertion cannot be replayed.
func TestKeystore_FetchChallengeIsSingleUse(t *testing.T) {
	f := newKeystoreFixture(t)
	cred := newTestCredential(t, "poweur.net")
	f.enroll(t, "e1", cred)

	challenge := f.challenge(t)
	assertion := cred.assert(t, challenge, flagsUPUV)
	body := KeystoreFetchRequest{Assertion: assertion, RelyingPartyID: "poweur.net"}

	if code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch", body); code != http.StatusOK {
		t.Fatalf("first fetch should succeed, got %d", code)
	}
	if code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch", body); code != http.StatusUnauthorized {
		t.Fatalf("replayed assertion must fail, got %d", code)
	}
}

// An unenrolled credential id must be indistinguishable from a bad signature:
// the response must not reveal which credentials exist.
func TestKeystore_FetchDoesNotRevealEnrolledCredentials(t *testing.T) {
	f := newKeystoreFixture(t)
	cred := newTestCredential(t, "poweur.net")
	f.enroll(t, "e1", cred)

	unknown := newTestCredential(t, "poweur.net")
	unknown.id = b64([]byte("not-enrolled"))

	challenge := f.challenge(t)
	unknownCode, unknownBody := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch",
		KeystoreFetchRequest{Assertion: unknown.assert(t, challenge, flagsUPUV), RelyingPartyID: "poweur.net"})

	challenge = f.challenge(t)
	badSigCred := newTestCredential(t, "poweur.net")
	badSigCred.id = cred.id
	badCode, badBody := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch",
		KeystoreFetchRequest{Assertion: badSigCred.assert(t, challenge, flagsUPUV), RelyingPartyID: "poweur.net"})

	if unknownCode != badCode {
		t.Fatalf("status leaks enrollment: unknown=%d badsig=%d", unknownCode, badCode)
	}
	if string(unknownBody) != string(badBody) {
		t.Fatalf("body leaks enrollment:\n unknown: %s\n badsig:  %s", unknownBody, badBody)
	}
	if strings.Contains(string(unknownBody), cred.id) {
		t.Fatal("response echoed an enrolled credential id")
	}
}

func TestKeystore_RemoveEnrollment(t *testing.T) {
	f := newKeystoreFixture(t)
	cred := newTestCredential(t, "poweur.net")
	f.enroll(t, "e1", cred)

	issuedAt := time.Now().UTC().Format(time.RFC3339)
	canonical := crypto.CanonicalKeystoreRemove(f.identity, "e1", issuedAt, "n-rm")
	req := KeystoreRemoveRequest{
		IssuedAt: issuedAt, Nonce: "n-rm",
		IdentitySignature: b64(ed25519.Sign(f.priv, []byte(canonical))),
		AllowLast:         true,
	}
	if code, body := f.do(t, http.MethodDelete, "/identities/"+f.identity+"/keystore/e1", req); code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", code, body)
	}
	if _, ok := f.server.keystore.Get(f.identity, "e1"); ok {
		t.Fatal("enrollment still present after removal")
	}

	// Removed credential can no longer bootstrap.
	challenge := f.challenge(t)
	code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/fetch",
		KeystoreFetchRequest{Assertion: cred.assert(t, challenge, flagsUPUV), RelyingPartyID: "poweur.net"})
	if code != http.StatusUnauthorized {
		t.Fatalf("removed enrollment must not fetch, got %d", code)
	}
}

func TestKeystore_RemoveRequiresValidSignature(t *testing.T) {
	f := newKeystoreFixture(t)
	f.enroll(t, "e1", newTestCredential(t, "poweur.net"))
	_, wrongPriv, _ := ed25519.GenerateKey(nil)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	req := KeystoreRemoveRequest{
		IssuedAt: issuedAt, Nonce: "n",
		IdentitySignature: b64(ed25519.Sign(wrongPriv, []byte("nope"))),
	}
	if code, _ := f.do(t, http.MethodDelete, "/identities/"+f.identity+"/keystore/e1", req); code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
}

// remove builds a signed removal request.
func (f *keystoreFixture) removeReq(t *testing.T, enrollmentID string) KeystoreRemoveRequest {
	t.Helper()
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "n-" + enrollmentID
	canonical := crypto.CanonicalKeystoreRemove(f.identity, enrollmentID, issuedAt, nonce)
	return KeystoreRemoveRequest{
		IssuedAt: issuedAt, Nonce: nonce,
		IdentitySignature: b64(ed25519.Sign(f.priv, []byte(canonical))),
	}
}

// enrollWithRole stores an enrollment carrying an explicit role.
func (f *keystoreFixture) enrollWithRole(t *testing.T, enrollmentID, role string, cred *testCredential) {
	t.Helper()
	wrapped := json.RawMessage(`{"iv":"aXY","ciphertext":"Y3Q"}`)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "nonce-" + enrollmentID
	canonical := crypto.CanonicalKeystoreEnroll(
		f.identity, enrollmentID, "hardware-key", cred.id, wrappedDigest(wrapped), issuedAt, nonce)
	req := KeystoreEnrollRequest{
		EnrollmentID: enrollmentID, Kind: "hardware-key", Wrap: "prf", Payload: "seed",
		CredentialID: cred.id, CredentialPublicKey: cred.spki, CredentialAlg: cred.alg,
		Wrapped: wrapped, Role: role, IssuedAt: issuedAt, Nonce: nonce,
		IdentitySignature: b64(ed25519.Sign(f.priv, []byte(canonical))),
	}
	if code, body := f.do(t, http.MethodPut, "/identities/"+f.identity+"/keystore", req); code != http.StatusOK {
		t.Fatalf("enroll %s: %d %s", enrollmentID, code, body)
	}
}

// Removing the only enrollment would strand recovery, so it needs an explicit
// acknowledgement rather than happening quietly.
func TestKeystore_RefusesToRemoveLastEnrollment(t *testing.T) {
	f := newKeystoreFixture(t)
	f.enroll(t, "e1", newTestCredential(t, "poweur.net"))

	if code, _ := f.do(t, http.MethodDelete, "/identities/"+f.identity+"/keystore/e1",
		f.removeReq(t, "e1")); code != http.StatusConflict {
		t.Fatalf("want 409 for last enrollment, got %d", code)
	}
	req := f.removeReq(t, "e1")
	req.AllowLast = true
	if code, body := f.do(t, http.MethodDelete, "/identities/"+f.identity+"/keystore/e1", req); code != http.StatusNoContent {
		t.Fatalf("allow_last should permit removal: %d %s", code, body)
	}
}

// The recovery-master role is only meaningful if the relay can tell *which*
// device is asking — the identity key is shared, so removal must additionally
// prove possession of the recovery-master authenticator.
func TestKeystore_RecoveryMasterGatesRemoval(t *testing.T) {
	f := newKeystoreFixture(t)
	phone := newTestCredential(t, "poweur.net")
	yubi := newTestCredential(t, "poweur.net")
	yubi.id = b64([]byte("credential-yubikey"))
	f.enroll(t, "phone", phone)
	f.enrollWithRole(t, "yubikey", "recovery-master", yubi)

	// A stolen phone holding the identity key must not evict the YubiKey.
	if code, body := f.do(t, http.MethodDelete, "/identities/"+f.identity+"/keystore/yubikey",
		f.removeReq(t, "yubikey")); code != http.StatusForbidden {
		t.Fatalf("want 403 without an actor assertion, got %d %s", code, body)
	}

	// The phone cannot pass itself off as the recovery-master either.
	req := f.removeReq(t, "yubikey")
	assertion := phone.assert(t, f.challenge(t), flagsUPUV)
	req.ActorAssertion, req.RelyingPartyID = &assertion, "poweur.net"
	if code, _ := f.do(t, http.MethodDelete, "/identities/"+f.identity+"/keystore/yubikey", req); code != http.StatusForbidden {
		t.Fatalf("non-master actor must be refused, got %d", code)
	}

	// The YubiKey may remove the phone — the kill-my-lost-device path.
	req = f.removeReq(t, "phone")
	masterAssertion := yubi.assert(t, f.challenge(t), flagsUPUV)
	req.ActorAssertion, req.RelyingPartyID = &masterAssertion, "poweur.net"
	if code, body := f.do(t, http.MethodDelete, "/identities/"+f.identity+"/keystore/phone", req); code != http.StatusNoContent {
		t.Fatalf("recovery-master removal failed: %d %s", code, body)
	}
	if _, ok := f.server.keystore.Get(f.identity, "phone"); ok {
		t.Fatal("phone enrollment survived removal")
	}
}

// Removal denies future bootstrap reads; revoking sessions ends live access.
func TestKeystore_RemoveCanRevokeSessions(t *testing.T) {
	f := newKeystoreFixture(t)
	f.enroll(t, "e1", newTestCredential(t, "poweur.net"))
	f.enroll(t, "e2", newTestCredential(t, "poweur.net"))
	f.server.sessions.Put(storage.Session{
		ID: "sess-1", Identity: f.identity, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})

	req := f.removeReq(t, "e1")
	req.RevokeSessions = true
	code, body := f.do(t, http.MethodDelete, "/identities/"+f.identity+"/keystore/e1", req)
	if code != http.StatusOK {
		t.Fatalf("want 200 with revocation summary, got %d %s", code, body)
	}
	if !strings.Contains(string(body), `"sessions_revoked":1`) {
		t.Fatalf("expected session revocation count: %s", body)
	}
	if _, ok := f.server.sessions.Get("sess-1"); ok {
		t.Fatal("session survived revocation")
	}
}

func TestKeystore_ListReturnsMetadataOnly(t *testing.T) {
	f := newKeystoreFixture(t)
	f.enroll(t, "e1", newTestCredential(t, "poweur.net"))

	issuedAt := time.Now().UTC().Format(time.RFC3339)
	canonical := crypto.CanonicalKeystoreList(f.identity, issuedAt, "n-ls")
	req := KeystoreListRequest{
		IssuedAt: issuedAt, Nonce: "n-ls",
		IdentitySignature: b64(ed25519.Sign(f.priv, []byte(canonical))),
	}
	code, body := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/list", req)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	// An owner enumerating devices has no need for the ciphertext, so it must
	// not be in the response at all.
	if strings.Contains(string(body), "ciphertext") || strings.Contains(string(body), "wrapped") {
		t.Fatalf("listing leaked wrapped ciphertext: %s", body)
	}
	if !strings.Contains(string(body), `"has_passkey":true`) {
		t.Fatalf("expected passkey summary flag: %s", body)
	}
}

func TestKeystore_ListRequiresValidSignature(t *testing.T) {
	f := newKeystoreFixture(t)
	f.enroll(t, "e1", newTestCredential(t, "poweur.net"))
	_, wrongPriv, _ := ed25519.GenerateKey(nil)
	req := KeystoreListRequest{
		IssuedAt: time.Now().UTC().Format(time.RFC3339), Nonce: "n",
		IdentitySignature: b64(ed25519.Sign(wrongPriv, []byte("nope"))),
	}
	if code, _ := f.do(t, http.MethodPost, "/identities/"+f.identity+"/keystore/list", req); code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
}
