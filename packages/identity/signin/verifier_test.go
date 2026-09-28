package signin

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity"
)

const (
	testOrigin   = "https://guestbook.poweur.net"
	testIdentity = "alice.poweur.net"
	testAppScope = "messages:send"
)

var testNow = mustTime("2026-01-15T09:30:00Z")

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// testKey is the same fixed seed the conformance vectors use, so a vector
// file and these tests describe one protocol rather than two.
func testKey() ed25519.PrivateKey {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func sessionKey() ed25519.PrivateKey {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(200 - i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

// staticResolver stands in for the resolver chain. Resolution is the only
// I/O a verifier does, so injecting it is what makes the whole package
// testable offline.
type staticResolver struct {
	doc  identity.IdentityDocument
	err  error
	hits int
}

func (r *staticResolver) Resolve(_ context.Context, name string) (identity.Result, error) {
	r.hits++
	if r.err != nil {
		return identity.Result{}, r.err
	}
	if !strings.EqualFold(name, r.doc.Identity) {
		return identity.Result{}, errors.New("no such identity")
	}
	return identity.Result{Document: r.doc}, nil
}

func testDocument(t *testing.T, priv ed25519.PrivateKey) identity.IdentityDocument {
	t.Helper()
	pub := identity.FormatEd25519PublicKey(priv.Public().(ed25519.PublicKey))
	doc := identity.NewDocument(testIdentity, pub, "", "poweur.net", nil)
	doc.UpdatedAt = "2026-01-15T09:00:00Z"
	if err := doc.Sign(priv); err != nil {
		t.Fatalf("sign document: %v", err)
	}
	return doc
}

func newTestVerifier(t *testing.T, res Resolver) *Verifier {
	t.Helper()
	v, err := NewVerifier(testOrigin)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.Resolver = res
	v.Now = func() time.Time { return testNow }
	return v
}

func baseResponse() identity.SignInResponse {
	return identity.SignInResponse{
		PoweurAuth: identity.SignInVersion,
		RequestID:  "req_001",
		Identity:   testIdentity,
		Audience:   testOrigin,
		Nonce:      "bm9uY2UtMDAx",
		IssuedAt:   "2026-01-15T09:29:00Z",
		ExpiresAt:  "2026-01-15T09:31:00Z",
		Action:     identity.SignInActionSignin,
		Statement:  "Sign in to the Poweur Guestbook",
		Scopes:     []string{testAppScope},
		KeyID:      identity.SignInKeyIDIdentity,
	}
}

func signWith(resp identity.SignInResponse, key ed25519.PrivateKey) identity.SignInResponse {
	resp.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(resp.Canonical())))
	return resp
}

func makeSessionProof(t *testing.T, identityKey, sessKey ed25519.PrivateKey, issued, expires string) identity.SignInSessionProof {
	t.Helper()
	pub := base64.RawURLEncoding.EncodeToString(sessKey.Public().(ed25519.PublicKey))
	canonical := identity.CanonicalSessionRegistration(testIdentity, pub, issued, expires, "c2Vzcy1ub25jZQ")
	return identity.SignInSessionProof{
		SessionPublicKey:  pub,
		IssuedAt:          issued,
		ExpiresAt:         expires,
		Nonce:             "c2Vzcy1ub25jZQ",
		IdentitySignature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(identityKey, []byte(canonical))),
	}
}

func TestVerifyHappyPath(t *testing.T) {
	priv := testKey()
	res := &staticResolver{doc: testDocument(t, priv)}
	v := newTestVerifier(t, res)

	encoded, err := identity.EncodeSignInResponse(signWith(baseResponse(), priv))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := v.Verify(context.Background(), encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Identity != testIdentity {
		t.Fatalf("identity = %q", got.Identity)
	}
	if got.Audience != testOrigin {
		t.Fatalf("audience = %q", got.Audience)
	}
	if got.AppID != "net.poweur.guestbook" {
		t.Fatalf("app id = %q", got.AppID)
	}
	if got.SessionDelegated {
		t.Fatal("identity-key signature must not report session delegation")
	}
	if got.Relay != "poweur.net" {
		t.Fatalf("relay = %q", got.Relay)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != testAppScope {
		t.Fatalf("scopes = %v", got.Scopes)
	}
	if !got.ExpiresAt.Equal(mustTime("2026-01-15T09:31:00Z")) {
		t.Fatalf("expires_at = %v", got.ExpiresAt)
	}
}

func TestVerifyAcceptsRawJSON(t *testing.T) {
	priv := testKey()
	v := newTestVerifier(t, &staticResolver{doc: testDocument(t, priv)})
	raw, err := json.Marshal(signWith(baseResponse(), priv))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := v.Verify(context.Background(), string(raw)); err != nil {
		t.Fatalf("Verify(raw JSON): %v", err)
	}
}

func TestVerifySessionDelegated(t *testing.T) {
	priv, sess := testKey(), sessionKey()
	v := newTestVerifier(t, &staticResolver{doc: testDocument(t, priv)})

	resp := baseResponse()
	resp.KeyID = identity.SignInKeyIDSessionPrefix + "sess_1"
	resp = signWith(resp, sess)
	proof := makeSessionProof(t, priv, sess, "2026-01-15T08:00:00Z", "2026-01-15T20:00:00Z")
	resp.SessionProof = &proof

	got, err := v.VerifyResponse(context.Background(), resp, testOrigin)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !got.SessionDelegated {
		t.Fatal("expected SessionDelegated")
	}
	if got.KeyID != "session:sess_1" {
		t.Fatalf("key id = %q", got.KeyID)
	}
}

// TestVerifyFailureModes is the table the spec's verification rules map onto:
// one case per rule, each asserted against the sentinel error it must raise.
func TestVerifyFailureModes(t *testing.T) {
	priv, sess := testKey(), sessionKey()
	doc := testDocument(t, priv)

	goodProof := makeSessionProof(t, priv, sess, "2026-01-15T08:00:00Z", "2026-01-15T20:00:00Z")

	tests := []struct {
		name string
		// mutate runs before signing when signAfter is true, after otherwise.
		build func() identity.SignInResponse
		want  error
	}{
		{
			name: "unsupported version",
			build: func() identity.SignInResponse {
				r := signWith(baseResponse(), priv)
				r.PoweurAuth = "2"
				return r
			},
			want: identity.ErrSignInVersion,
		},
		{
			name: "missing signature",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Signature = ""
				return r
			},
			want: identity.ErrSignInMalformed,
		},
		{
			name: "missing nonce",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Nonce = ""
				return signWith(r, priv)
			},
			want: identity.ErrSignInMalformed,
		},
		{
			name: "identity is not a poweur name",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Identity = "not a name"
				return signWith(r, priv)
			},
			want: identity.ErrSignInMalformed,
		},
		{
			name: "audience is another origin",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Audience = "https://evil.example"
				r.Scopes = nil
				return signWith(r, priv)
			},
			want: identity.ErrSignInAudience,
		},
		{
			name: "audience is a sibling host",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Audience = "https://guestbook.poweur.net.evil.example"
				r.Scopes = nil
				return signWith(r, priv)
			},
			want: identity.ErrSignInAudience,
		},
		{
			name: "audience differs only by port",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Audience = "https://guestbook.poweur.net:8443"
				r.Scopes = nil
				return signWith(r, priv)
			},
			want: identity.ErrSignInAudience,
		},
		{
			name: "unknown action",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Action = "authorize"
				return signWith(r, priv)
			},
			want: identity.ErrSignInAction,
		},
		{
			name: "statement carries a newline",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Statement = "line one\nline two"
				return signWith(r, priv)
			},
			want: identity.ErrSignInMalformed,
		},
		{
			name: "storage v1 dav scope",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Scopes = []string{"dav:rw:apps/net.poweur.mail"}
				return signWith(r, priv)
			},
			want: identity.ErrSignInScope,
		},
		{
			name: "scope reaching the private root",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Scopes = []string{"dav:read:private"}
				return signWith(r, priv)
			},
			want: identity.ErrSignInScope,
		},
		{
			name: "scopes not in canonical order",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Scopes = []string{identity.ScopeProfileRead, testAppScope}
				return signWith(r, priv)
			},
			want: identity.ErrSignInScope,
		},
		{
			name: "unknown scope",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.Scopes = []string{"admin:everything"}
				return signWith(r, priv)
			},
			want: identity.ErrSignInScope,
		},
		{
			name: "expired",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.IssuedAt = "2026-01-15T09:20:00Z"
				r.ExpiresAt = "2026-01-15T09:22:00Z"
				return signWith(r, priv)
			},
			want: identity.ErrSignInExpired,
		},
		{
			name: "issued in the future beyond skew",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.IssuedAt = "2026-01-15T09:40:00Z"
				r.ExpiresAt = "2026-01-15T09:44:00Z"
				return signWith(r, priv)
			},
			want: identity.ErrSignInNotYet,
		},
		{
			name: "window longer than five minutes",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.ExpiresAt = "2026-01-15T09:40:00Z"
				return signWith(r, priv)
			},
			want: identity.ErrSignInTTL,
		},
		{
			name: "expires_at before issued_at",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.ExpiresAt = "2026-01-15T09:28:00Z"
				return signWith(r, priv)
			},
			want: identity.ErrSignInMalformed,
		},
		{
			name: "issued_at is not RFC3339",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.IssuedAt = "yesterday"
				return signWith(r, priv)
			},
			want: identity.ErrSignInMalformed,
		},
		{
			name: "statement edited after signing",
			build: func() identity.SignInResponse {
				r := signWith(baseResponse(), priv)
				r.Statement = "Sign in and transfer everything"
				return r
			},
			want: identity.ErrSignInSignature,
		},
		{
			name: "signed by the wrong key",
			build: func() identity.SignInResponse {
				return signWith(baseResponse(), sess)
			},
			want: identity.ErrSignInSignature,
		},
		{
			name: "signature is not 64 bytes",
			build: func() identity.SignInResponse {
				r := signWith(baseResponse(), priv)
				r.Signature = base64.RawURLEncoding.EncodeToString([]byte("short"))
				return r
			},
			want: identity.ErrSignInSignature,
		},
		{
			name: "key_id names a session with no proof",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.KeyID = identity.SignInKeyIDSessionPrefix + "sess_1"
				return signWith(r, sess)
			},
			want: identity.ErrSignInSessionKey,
		},
		{
			name: "proof attached but key_id says identity",
			build: func() identity.SignInResponse {
				r := signWith(baseResponse(), priv)
				p := goodProof
				r.SessionProof = &p
				return r
			},
			want: identity.ErrSignInSessionKey,
		},
		{
			name: "key_id is neither identity nor a session",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.KeyID = "device"
				return signWith(r, priv)
			},
			want: identity.ErrSignInMalformed,
		},
		{
			name: "session proof signed by an impostor",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.KeyID = identity.SignInKeyIDSessionPrefix + "sess_1"
				r = signWith(r, sess)
				p := makeSessionProof(t, sess, sess, "2026-01-15T08:00:00Z", "2026-01-15T20:00:00Z")
				r.SessionProof = &p
				return r
			},
			want: identity.ErrSignInSessionKey,
		},
		{
			name: "session proof expired",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.KeyID = identity.SignInKeyIDSessionPrefix + "sess_1"
				r = signWith(r, sess)
				p := makeSessionProof(t, priv, sess, "2026-01-14T08:00:00Z", "2026-01-14T20:00:00Z")
				r.SessionProof = &p
				return r
			},
			want: identity.ErrSignInSessionKey,
		},
		{
			name: "session proof exceeds the 24h TTL cap",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.KeyID = identity.SignInKeyIDSessionPrefix + "sess_1"
				r = signWith(r, sess)
				p := makeSessionProof(t, priv, sess, "2026-01-15T08:00:00Z", "2026-01-20T08:00:00Z")
				r.SessionProof = &p
				return r
			},
			want: identity.ErrSignInSessionKey,
		},
		{
			name: "response signed by a key the proof does not cover",
			build: func() identity.SignInResponse {
				r := baseResponse()
				r.KeyID = identity.SignInKeyIDSessionPrefix + "sess_1"
				r = signWith(r, priv) // identity key, but key_id claims the session
				p := goodProof
				r.SessionProof = &p
				return r
			},
			want: identity.ErrSignInSignature,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestVerifier(t, &staticResolver{doc: doc})
			_, err := v.VerifyResponse(context.Background(), tc.build(), testOrigin)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyRejectsReplay(t *testing.T) {
	priv := testKey()
	v := newTestVerifier(t, &staticResolver{doc: testDocument(t, priv)})
	resp := signWith(baseResponse(), priv)

	if _, err := v.VerifyResponse(context.Background(), resp, testOrigin); err != nil {
		t.Fatalf("first use: %v", err)
	}
	_, err := v.VerifyResponse(context.Background(), resp, testOrigin)
	if err == nil {
		t.Fatal("expected the replay to be rejected")
	}
	if !errors.Is(err, identity.ErrSignInSignature) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("replay error should name the nonce, got %v", err)
	}
}

// A nonce spent at one origin must not lock the same user out at another —
// the cache key includes the audience, so a shared cache stays correct.
func TestNonceCacheIsScopedByAudienceAndIdentity(t *testing.T) {
	cache := NewMemoryNonceCache()
	cache.Now = func() time.Time { return testNow }
	exp := testNow.Add(time.Minute)
	for _, key := range []string{
		"https://a.example|alice.poweur.net|req_1|n",
		"https://b.example|alice.poweur.net|req_1|n",
		"https://a.example|bob.poweur.net|req_1|n",
	} {
		fresh, err := cache.Use(context.Background(), key, exp)
		if err != nil || !fresh {
			t.Fatalf("%s: fresh=%v err=%v", key, fresh, err)
		}
	}
	fresh, _ := cache.Use(context.Background(), "https://a.example|alice.poweur.net|req_1|n", exp)
	if fresh {
		t.Fatal("expected the repeat to be refused")
	}
}

// The cache only has to remember a nonce for as long as the response could
// still be presented; after that it prunes itself without a goroutine.
func TestMemoryNonceCachePrunesExpiredEntries(t *testing.T) {
	cache := NewMemoryNonceCache()
	now := testNow
	cache.Now = func() time.Time { return now }
	if fresh, _ := cache.Use(context.Background(), "k", now.Add(time.Minute)); !fresh {
		t.Fatal("first use should be fresh")
	}
	now = now.Add(2 * time.Minute)
	if cache.Len() != 1 {
		t.Fatalf("entry should still be present before the next call, got %d", cache.Len())
	}
	// A later claim of a *different* key prunes the stale one, and the old
	// key is claimable again because the response it guarded is long dead.
	if fresh, _ := cache.Use(context.Background(), "other", now.Add(time.Minute)); !fresh {
		t.Fatal("unrelated key should be fresh")
	}
	if cache.Len() != 1 {
		t.Fatalf("stale entry should have been pruned, got %d", cache.Len())
	}
}

type failingCache struct{}

func (failingCache) Use(context.Context, string, time.Time) (bool, error) {
	return false, errors.New("redis is down")
}

// A verifier that cannot reach its replay guard must fail closed.
func TestVerifyFailsClosedWhenNonceCacheErrors(t *testing.T) {
	priv := testKey()
	v := newTestVerifier(t, &staticResolver{doc: testDocument(t, priv)})
	v.Nonces = failingCache{}
	if _, err := v.VerifyResponse(context.Background(), signWith(baseResponse(), priv), testOrigin); err == nil {
		t.Fatal("expected the verification to fail when the cache is unavailable")
	}
}

// Resolution is the only network call a verifier makes; a response that is
// stale or addressed elsewhere must never reach it.
func TestVerifyDoesNotResolveBeforeLocalChecksPass(t *testing.T) {
	priv := testKey()
	res := &staticResolver{doc: testDocument(t, priv)}
	v := newTestVerifier(t, res)

	bad := baseResponse()
	bad.Audience = "https://evil.example"
	bad.Scopes = nil
	if _, err := v.VerifyResponse(context.Background(), signWith(bad, priv), testOrigin); err == nil {
		t.Fatal("expected audience rejection")
	}
	if res.hits != 0 {
		t.Fatalf("resolver was called %d times for a misaddressed response", res.hits)
	}
}

func TestVerifySurfacesResolutionFailure(t *testing.T) {
	priv := testKey()
	v := newTestVerifier(t, &staticResolver{err: errors.New("nxdomain")})
	_, err := v.VerifyResponse(context.Background(), signWith(baseResponse(), priv), testOrigin)
	if err == nil || !strings.Contains(err.Error(), "nxdomain") {
		t.Fatalf("got %v", err)
	}
}

// A rotated key inside its grace window still verifies a sign-in signed just
// before the rotation; outside the window it does not.
func TestVerifyAcceptsPreviousKeyInGraceWindow(t *testing.T) {
	oldKey := testKey()
	newSeed := make([]byte, 32)
	for i := range newSeed {
		newSeed[i] = byte(100 + i)
	}
	newKey := ed25519.NewKeyFromSeed(newSeed)

	doc := identity.NewDocument(testIdentity,
		identity.FormatEd25519PublicKey(newKey.Public().(ed25519.PublicKey)), "", "poweur.net", nil)
	doc.UpdatedAt = "2026-01-15T09:00:00Z"
	doc.PreviousKeys = []identity.PreviousKey{{
		PublicKey:  identity.FormatEd25519PublicKey(oldKey.Public().(ed25519.PublicKey)),
		ValidUntil: "2026-01-20T00:00:00Z",
	}}
	if err := doc.Sign(newKey); err != nil {
		t.Fatalf("sign: %v", err)
	}

	v := newTestVerifier(t, &staticResolver{doc: doc})
	if _, err := v.VerifyResponse(context.Background(), signWith(baseResponse(), oldKey), testOrigin); err != nil {
		t.Fatalf("retired key inside its grace window should still verify: %v", err)
	}

	expiredDoc := doc
	expiredDoc.PreviousKeys = []identity.PreviousKey{{
		PublicKey:  identity.FormatEd25519PublicKey(oldKey.Public().(ed25519.PublicKey)),
		ValidUntil: "2026-01-01T00:00:00Z",
	}}
	v2 := newTestVerifier(t, &staticResolver{doc: expiredDoc})
	r := baseResponse()
	r.Nonce = "bm9uY2UtMDAy"
	if _, err := v2.VerifyResponse(context.Background(), signWith(r, oldKey), testOrigin); err == nil {
		t.Fatal("a key past its grace window must not verify a sign-in")
	}
}

func TestNewRequestProducesAValidatableRequest(t *testing.T) {
	v := newTestVerifier(t, &staticResolver{})
	req, err := v.NewRequest(RequestOptions{
		Statement:   "Sign in to the Poweur Guestbook",
		ResponseURI: testOrigin + "/auth/callback",
		Scopes:      []string{"messages:send"},
	})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if req.Audience != testOrigin {
		t.Fatalf("audience = %q", req.Audience)
	}
	if req.Action != identity.SignInActionSignin {
		t.Fatalf("action = %q", req.Action)
	}
	if len(req.Scopes) != 1 || req.Scopes[0] != testAppScope {
		t.Fatalf("scopes = %v (should be normalized)", req.Scopes)
	}
	if err := req.Validate(testNow); err != nil {
		t.Fatalf("the verifier built a request it would not accept: %v", err)
	}
	// Two requests never share a nonce.
	other, err := v.NewRequest(RequestOptions{})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if other.Nonce == req.Nonce || other.RequestID == req.RequestID {
		t.Fatal("request id and nonce must be unpredictable per request")
	}
}

func TestNewRequestRejectsBadOptions(t *testing.T) {
	v := newTestVerifier(t, &staticResolver{})
	tests := []struct {
		name string
		opts RequestOptions
	}{
		{"response_uri off origin", RequestOptions{ResponseURI: "https://evil.example/cb"}},
		{"a storage v1 dav scope", RequestOptions{Scopes: []string{"dav:rw:apps/net.poweur.mail"}}},
		{"unknown scope", RequestOptions{Scopes: []string{"root:everything"}}},
		{"unknown action", RequestOptions{Action: "authorize"}},
		{"statement too long", RequestOptions{Statement: strings.Repeat("x", identity.SignInMaxStatementLen+1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.NewRequest(tc.opts); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// NewRequest never emits a window a verifier would reject, even when asked.
func TestNewRequestClampsTTL(t *testing.T) {
	v := newTestVerifier(t, &staticResolver{})
	req, err := v.NewRequest(RequestOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	iat := mustTime(req.IssuedAt)
	exp := mustTime(req.ExpiresAt)
	if exp.Sub(iat) != identity.SignInMaxTTL {
		t.Fatalf("ttl = %v, want it clamped to %v", exp.Sub(iat), identity.SignInMaxTTL)
	}
}

func TestNewVerifierRejectsBadOrigin(t *testing.T) {
	for _, origin := range []string{"", "example.com", "https://example.com/app", "ftp://example.com"} {
		if _, err := NewVerifier(origin); err == nil {
			t.Fatalf("expected %q to be rejected", origin)
		}
	}
}

// The full round trip a relying party actually runs: build, sign, verify.
func TestSignThenVerifyRoundTrip(t *testing.T) {
	priv := testKey()
	v := newTestVerifier(t, &staticResolver{doc: testDocument(t, priv)})
	req, err := v.NewRequest(RequestOptions{Statement: "Sign in", Scopes: []string{identity.ScopeProfileRead}})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := Sign(req, SignOptions{
		Identity:   testIdentity,
		PrivateKey: priv,
		Now:        func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	encoded, err := identity.EncodeSignInResponse(resp)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := v.Verify(context.Background(), encoded)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.RequestID != req.RequestID {
		t.Fatalf("request id = %q want %q", got.RequestID, req.RequestID)
	}
}

// TestConformanceVectors runs the same fixtures the TypeScript client is
// pinned to, so the reference verifier and the vector file cannot drift.
func TestConformanceVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "vectors", "signin.json"))
	if err != nil {
		t.Skipf("vectors not generated: %v", err)
	}
	var file struct {
		IdentityKey string `json:"identity_key"`
		Now         string `json:"now"`
		Vectors     []struct {
			Name      string                  `json:"name"`
			Origin    string                  `json:"origin"`
			Response  identity.SignInResponse `json:"response"`
			Canonical string                  `json:"canonical"`
			Valid     bool                    `json:"valid"`
			Reason    string                  `json:"reason"`
			ReplayOf  string                  `json:"replay_of"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	now := mustTime(file.Now)

	doc := identity.NewDocument(testIdentity, file.IdentityKey, "", "poweur.net", nil)
	doc.UpdatedAt = "2026-01-15T09:00:00Z"
	if err := doc.Sign(testKey()); err != nil {
		t.Fatalf("sign document: %v", err)
	}

	// One verifier for the whole run, so the replay vector actually replays
	// the nonce the happy-path vector spent.
	v, err := NewVerifier(testOrigin)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	v.Resolver = &staticResolver{doc: doc}
	v.Now = func() time.Time { return now }

	seen := 0
	for _, vec := range file.Vectors {
		if vec.Canonical != vec.Response.Canonical() {
			t.Fatalf("%s: canonical string in the fixture does not match the implementation", vec.Name)
		}
		_, err := v.VerifyResponse(context.Background(), vec.Response, testOrigin)
		if vec.Valid && err != nil {
			t.Fatalf("%s: expected acceptance, got %v", vec.Name, err)
		}
		if !vec.Valid && err == nil {
			t.Fatalf("%s: expected rejection (%s)", vec.Name, vec.Reason)
		}
		seen++
	}
	if seen < 10 {
		t.Fatalf("expected the full vector set, saw %d", seen)
	}
}
