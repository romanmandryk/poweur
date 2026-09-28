package signin

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/poweur/identity"
)

func signerRequest() identity.SignInRequest {
	return identity.SignInRequest{
		PoweurAuth:  identity.SignInVersion,
		RequestID:   "req_001",
		Domain:      "guestbook.poweur.net",
		Audience:    testOrigin,
		Nonce:       "bm9uY2UtMDAx",
		IssuedAt:    "2026-01-15T09:29:00Z",
		ExpiresAt:   "2026-01-15T09:31:00Z",
		Action:      identity.SignInActionSignin,
		Statement:   "Sign in to the Poweur Guestbook",
		ResponseURI: testOrigin + "/auth/callback",
		Scopes:      []string{"messages:send"},
	}
}

func signerOpts(priv ed25519.PrivateKey) SignOptions {
	return SignOptions{
		Identity:   testIdentity,
		PrivateKey: priv,
		Now:        func() time.Time { return testNow },
	}
}

func TestSignProducesAVerifiableApproval(t *testing.T) {
	priv := testKey()
	resp, err := Sign(signerRequest(), signerOpts(priv))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if resp.KeyID != identity.SignInKeyIDIdentity {
		t.Fatalf("key id = %q", resp.KeyID)
	}
	// The approval copies the challenge's window verbatim: it is valid
	// exactly as long as the request was, never longer.
	if resp.IssuedAt != "2026-01-15T09:29:00Z" || resp.ExpiresAt != "2026-01-15T09:31:00Z" {
		t.Fatalf("window = %s..%s", resp.IssuedAt, resp.ExpiresAt)
	}
	if len(resp.Scopes) != 1 || resp.Scopes[0] != testAppScope {
		t.Fatalf("scopes = %v (should be normalized before signing)", resp.Scopes)
	}
	sig, err := identity.DecodeAnyBase64(resp.Signature)
	if err != nil {
		t.Fatalf("signature not base64: %v", err)
	}
	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), []byte(resp.Canonical()), sig) {
		t.Fatal("signature does not verify over its own canonical string")
	}
}

func TestSignWithSessionKey(t *testing.T) {
	priv, sess := testKey(), sessionKey()
	proof := makeSessionProof(t, priv, sess, "2026-01-15T08:00:00Z", "2026-01-15T20:00:00Z")
	opts := signerOpts(sess)
	opts.SessionID = "sess_1"
	opts.SessionProof = &proof

	resp, err := Sign(signerRequest(), opts)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if resp.KeyID != "session:sess_1" {
		t.Fatalf("key id = %q", resp.KeyID)
	}
	if resp.SessionProof == nil {
		t.Fatal("session proof must travel with the approval")
	}

	v := newTestVerifier(t, &staticResolver{doc: testDocument(t, priv)})
	got, err := v.VerifyResponse(t.Context(), resp, testOrigin)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !got.SessionDelegated {
		t.Fatal("expected SessionDelegated")
	}
}

// A signer must never sign an object it would not itself accept.
func TestSignRefusesRequestsAVerifierWouldReject(t *testing.T) {
	priv := testKey()
	tests := []struct {
		name   string
		mutate func(*identity.SignInRequest)
		opts   func(*SignOptions)
	}{
		{"expired", func(r *identity.SignInRequest) {
			r.IssuedAt, r.ExpiresAt = "2026-01-15T09:00:00Z", "2026-01-15T09:02:00Z"
		}, nil},
		{"window longer than five minutes", func(r *identity.SignInRequest) {
			r.ExpiresAt = "2026-01-15T09:40:00Z"
		}, nil},
		{"response_uri off origin", func(r *identity.SignInRequest) {
			r.ResponseURI = "https://evil.example/collect"
		}, nil},
		{"scope in another app's namespace", func(r *identity.SignInRequest) {
			r.Scopes = []string{"dav:rw:apps/net.poweur.mail"}
		}, nil},
		{"statement with a newline", func(r *identity.SignInRequest) {
			r.Statement = "Sign in\nand hand over everything"
		}, nil},
		{"unknown action", func(r *identity.SignInRequest) { r.Action = "authorize" }, nil},
		{"audience with a path", func(r *identity.SignInRequest) {
			r.Audience = testOrigin + "/app"
		}, nil},
		{"not an ed25519 key", nil, func(o *SignOptions) { o.PrivateKey = []byte("too short") }},
		{"session id without a proof", nil, func(o *SignOptions) { o.SessionID = "sess_1" }},
		{"proof without a session id", nil, func(o *SignOptions) {
			p := makeSessionProof(t, testKey(), sessionKey(), "2026-01-15T08:00:00Z", "2026-01-15T20:00:00Z")
			o.SessionProof = &p
		}},
		{"identity is not a poweur name", nil, func(o *SignOptions) { o.Identity = "not a name" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := signerRequest()
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			opts := signerOpts(priv)
			if tc.opts != nil {
				tc.opts(&opts)
			}
			if _, err := Sign(req, opts); err == nil {
				t.Fatal("expected the signer to refuse")
			}
		})
	}
}

// Every scope a user approves has to be readable as a sentence — a raw
// token tells a user nothing about what is at risk.
func TestDescribeScope(t *testing.T) {
	tests := []struct {
		scope    string
		contains []string
	}{
		{identity.ScopeProfileRead, []string{"public profile"}},
		{identity.ScopeMessagesSend, []string{"send messages"}},
		{"dav:read:apps/net.poweur.guestbook", []string{"unrecognized", "do not approve"}},
		{"root:everything", []string{"unrecognized", "do not approve"}},
	}
	for _, tc := range tests {
		got := DescribeScope(tc.scope, "Guestbook")
		for _, want := range tc.contains {
			if !strings.Contains(got, want) {
				t.Fatalf("DescribeScope(%q) = %q, missing %q", tc.scope, got, want)
			}
		}
		if !strings.HasPrefix(got, "Guestbook") {
			t.Fatalf("DescribeScope(%q) should name the app: %q", tc.scope, got)
		}
	}
	if !strings.HasPrefix(DescribeScope(identity.ScopeProfileRead, ""), "This app") {
		t.Fatal("an unnamed app still needs a subject in the sentence")
	}
	if len(DescribeScopes([]string{identity.ScopeProfileRead, identity.ScopeMessagesSend}, "X")) != 2 {
		t.Fatal("DescribeScopes must render every scope")
	}
}

func TestSummarizeRequest(t *testing.T) {
	req := signerRequest()
	tests := []struct {
		action   string
		appName  string
		contains string
	}{
		{identity.SignInActionSignin, "Guestbook", "Sign in to Guestbook (guestbook.poweur.net)"},
		{identity.SignInActionSignup, "Guestbook", "Create an account at Guestbook"},
		{identity.SignInActionLink, "", "Link your Poweur ID"},
	}
	for _, tc := range tests {
		req.Action = tc.action
		got := SummarizeRequest(req, tc.appName)
		if !strings.Contains(got, tc.contains) {
			t.Fatalf("SummarizeRequest(%s) = %q, want it to contain %q", tc.action, got, tc.contains)
		}
		// The host is always shown: the app name is RP-supplied and the
		// host is the part a user can actually check.
		if !strings.Contains(got, "guestbook.poweur.net") {
			t.Fatalf("SummarizeRequest(%s) = %q, must always show the host", tc.action, got)
		}
	}
}
