// Package signin verifies "Sign in with Poweur ID" responses (EPIC-008
// E08-T2). It is the reference verifier: any Go backend can accept Poweur
// identities with a Verifier value and one Verify call.
//
//	v := signin.NewVerifier("https://guestbook.poweur.net")
//	req, _ := v.NewRequest(signin.RequestOptions{Statement: "Sign in to the Guestbook"})
//	// … hand req to the user's signer, receive `encoded` back …
//	res, err := v.Verify(ctx, encoded)
//	if err != nil { http.Error(w, "sign-in failed", 401); return }
//	log.Println("signed in:", res.Identity)
//
// The verifier holds no account state: it resolves the identity through the
// published resolver chain (HTTPS well-known then DNS TXT) and checks a
// signature. The only thing it remembers is used nonces.
package signin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/poweur/identity"
)

// Resolver fetches and verifies an identity document. The default is the
// package resolver chain; tests and offline deployments inject their own.
type Resolver interface {
	Resolve(ctx context.Context, name string) (identity.Result, error)
}

type resolverFunc func(ctx context.Context, name string) (identity.Result, error)

func (f resolverFunc) Resolve(ctx context.Context, name string) (identity.Result, error) {
	return f(ctx, name)
}

// Verifier validates sign-in responses addressed to one origin.
//
// The zero value is not usable — Origin is required, because the whole
// anti-phishing property of the protocol is that a response is bound to the
// origin it was collected at.
type Verifier struct {
	// Origin is this relying party's own origin, normalized. A response
	// whose audience is anything else is rejected even when its signature
	// is perfectly valid.
	Origin string

	// Nonces is the replay guard. Defaults to an in-process cache; a
	// multi-process RP must supply a shared one.
	Nonces NonceCache

	// Resolver overrides identity resolution (tests, private deployments).
	Resolver Resolver

	// ResolveOptions is passed to the default resolver.
	ResolveOptions identity.ResolveOptions

	// Now is injectable for tests.
	Now func() time.Time

	// RequestTTL is how long requests built by NewRequest stay valid.
	// Defaults to 2 minutes; capped at identity.SignInMaxTTL.
	RequestTTL time.Duration
}

// NewVerifier returns a verifier for origin with the default in-process
// nonce cache and the standard resolver chain.
func NewVerifier(origin string) (*Verifier, error) {
	norm, err := identity.NormalizeOrigin(origin)
	if err != nil {
		return nil, err
	}
	v := &Verifier{Origin: norm}
	v.Nonces = v.newNonceCache()
	return v, nil
}

// newNonceCache returns an in-process cache that reads the clock through the
// verifier, so an injected Now (tests, a deliberately skewed deployment) does
// not leave the replay guard measuring a different timeline than the checks
// it is guarding.
func (v *Verifier) newNonceCache() *MemoryNonceCache {
	c := NewMemoryNonceCache()
	c.Now = v.now
	return c
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}

func (v *Verifier) resolver() Resolver {
	if v.Resolver != nil {
		return v.Resolver
	}
	opts := v.ResolveOptions
	return resolverFunc(func(ctx context.Context, name string) (identity.Result, error) {
		return identity.Resolve(ctx, name, opts)
	})
}

// RequestOptions configure NewRequest.
type RequestOptions struct {
	// Action defaults to "signin".
	Action string
	// Statement is the single line the user is shown before approving.
	Statement string
	// ResponseURI is where the signer delivers the approval. Must be
	// same-origin with the verifier's Origin; defaults to empty (the RP
	// collects the response out of band, e.g. a pasted code).
	ResponseURI string
	// Scopes requested for the resource step. `dav:` scopes must live under
	// apps/<app id derived from this origin>.
	Scopes []string
	// TTL overrides Verifier.RequestTTL for this request.
	TTL time.Duration
}

// NewRequest builds a fresh request object with a random request id and
// nonce. The RP stores nothing: everything the verifier needs later travels
// inside the signed response.
func (v *Verifier) NewRequest(opts RequestOptions) (identity.SignInRequest, error) {
	origin, err := identity.NormalizeOrigin(v.Origin)
	if err != nil {
		return identity.SignInRequest{}, err
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = v.RequestTTL
	}
	if ttl == 0 {
		ttl = 2 * time.Minute
	}
	if ttl > identity.SignInMaxTTL {
		ttl = identity.SignInMaxTTL
	}
	action := opts.Action
	if action == "" {
		action = identity.SignInActionSignin
	}
	requestID, err := randomToken(16)
	if err != nil {
		return identity.SignInRequest{}, err
	}
	nonce, err := randomToken(16)
	if err != nil {
		return identity.SignInRequest{}, err
	}
	now := v.now()
	u, _ := url.Parse(origin)
	req := identity.SignInRequest{
		PoweurAuth:  identity.SignInVersion,
		RequestID:   "req_" + requestID,
		Domain:      u.Hostname(),
		Audience:    origin,
		Nonce:       nonce,
		IssuedAt:    now.Format(time.RFC3339),
		ExpiresAt:   now.Add(ttl).Format(time.RFC3339),
		Action:      action,
		Statement:   opts.Statement,
		ResponseURI: opts.ResponseURI,
		Scopes:      opts.Scopes,
	}
	normalized, err := req.Normalize()
	if err != nil {
		return identity.SignInRequest{}, err
	}
	if err := normalized.Validate(now); err != nil {
		return identity.SignInRequest{}, err
	}
	return normalized, nil
}

// Result is what a successful verification tells the relying party.
type Result struct {
	// Identity is the verified Poweur ID. This is the login subject.
	Identity string
	// Audience is the origin the approval was bound to (== Verifier.Origin).
	Audience string
	// RequestID echoes the request this answers, so an RP that kept
	// per-request state (a pending cross-device login) can match it up.
	RequestID string
	Action    string
	Statement string
	// Scopes the user approved, normalized and sorted. Possibly empty.
	Scopes []string
	// KeyID is "identity" or "session:<id>".
	KeyID string
	// SessionDelegated is true when a short-lived session key signed the
	// approval rather than the long-lived identity key.
	SessionDelegated bool
	// AppID is the app namespace derived from the audience.
	AppID string
	// ExpiresAt is when the approval stops being usable — including as an
	// authorization grant at the user's relay.
	ExpiresAt time.Time
	// Relay is the identity's home relay from its resolved document; the
	// address to present the approval at for scoped resource access.
	Relay string
	// Document is the resolved identity document.
	Document identity.IdentityDocument
	// Response is the decoded approval, for an RP that wants the raw object
	// (e.g. to forward it to the relay's POST /auth/grant).
	Response identity.SignInResponse
}

// Verify parses, validates and authenticates a sign-in response.
//
// encoded may be the base64url form, raw JSON, or a `response=` parameter
// value. Checks run in this order, cheapest and most local first:
//
//  1. shape and protocol version
//  2. audience == this verifier's origin  (phishing / misdelivery)
//  3. action, statement, scope vocabulary and namespace
//  4. validity window and the 5-minute TTL cap  (staleness)
//  5. nonce single-use                          (replay)
//  6. identity resolution through the resolver chain
//  7. signature — identity key, or session key via the delegation proof
//
// Resolution comes late on purpose: a replayed or misaddressed response
// should never cost a DNS or HTTPS lookup.
func (v *Verifier) Verify(ctx context.Context, encoded string) (*Result, error) {
	origin, err := identity.NormalizeOrigin(v.Origin)
	if err != nil {
		return nil, fmt.Errorf("verifier origin invalid: %w", err)
	}
	resp, err := identity.DecodeSignInResponse(encoded)
	if err != nil {
		return nil, err
	}
	return v.VerifyResponse(ctx, resp, origin)
}

// VerifyResponse is Verify for an already-decoded response.
func (v *Verifier) VerifyResponse(ctx context.Context, resp identity.SignInResponse, origin string) (*Result, error) {
	now := v.now()

	if resp.PoweurAuth != identity.SignInVersion {
		return nil, fmt.Errorf("%w: %q", identity.ErrSignInVersion, resp.PoweurAuth)
	}
	if resp.RequestID == "" || resp.Nonce == "" || resp.Signature == "" {
		return nil, fmt.Errorf("%w: request_id, nonce and signature are required", identity.ErrSignInMalformed)
	}
	name := strings.ToLower(strings.TrimSpace(resp.Identity))
	if err := identity.ValidateIdentityName(name); err != nil {
		return nil, fmt.Errorf("%w: identity %v", identity.ErrSignInMalformed, err)
	}
	resp.Identity = name

	audience, err := identity.NormalizeOrigin(resp.Audience)
	if err != nil {
		return nil, err
	}
	if audience != origin {
		// The WebAuthn property: a response harvested at evil.example is
		// signed over evil.example and cannot be spent here.
		return nil, fmt.Errorf("%w: response is bound to %s, this verifier is %s", identity.ErrSignInAudience, audience, origin)
	}
	resp.Audience = audience

	if err := identity.ValidateSignInAction(resp.Action); err != nil {
		return nil, err
	}
	if err := identity.ValidateSignInStatement(resp.Statement); err != nil {
		return nil, err
	}
	scopes, err := identity.NormalizeSignInScopes(resp.Scopes)
	if err != nil {
		return nil, err
	}
	if !sameStrings(scopes, resp.Scopes) {
		// The canonical string is built from the normalized, sorted list, so
		// a response carrying a differently-ordered or non-normalized list
		// would verify against bytes the user never saw. Reject rather than
		// silently rewrite.
		return nil, fmt.Errorf("%w: scopes are not in canonical form", identity.ErrSignInScope)
	}
	appID, err := identity.SignInAppID(audience)
	if err != nil {
		return nil, err
	}

	expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("%w: expires_at must be RFC3339", identity.ErrSignInMalformed)
	}
	if err := validateResponseWindow(resp, now); err != nil {
		return nil, err
	}

	// Replay guard. Keyed by audience + identity + request + nonce so one
	// user's nonce cannot lock another user out, and a cache shared between
	// several RPs stays correct.
	nonces := v.Nonces
	if nonces == nil {
		nonces = v.newNonceCache()
		v.Nonces = nonces
	}
	key := strings.Join([]string{audience, resp.Identity, resp.RequestID, resp.Nonce}, "|")
	fresh, err := nonces.Use(ctx, key, expiresAt.UTC())
	if err != nil {
		return nil, fmt.Errorf("nonce cache unavailable: %w", err)
	}
	if !fresh {
		return nil, fmt.Errorf("%w: nonce already used", identity.ErrSignInSignature)
	}

	res, err := v.resolver().Resolve(ctx, resp.Identity)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", resp.Identity, err)
	}
	doc := res.Document

	canonical := resp.Canonical()
	sig, err := identity.DecodeAnyBase64(resp.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: signature is not a 64-byte Ed25519 signature", identity.ErrSignInSignature)
	}

	sessionDelegated := false
	if sessionID := identity.SessionIDFromKeyID(resp.KeyID); sessionID != "" || resp.SessionProof != nil {
		if resp.SessionProof == nil {
			return nil, fmt.Errorf("%w: key_id names a session but no session_proof is attached", identity.ErrSignInSessionKey)
		}
		if sessionID == "" {
			return nil, fmt.Errorf("%w: session_proof attached but key_id is %q", identity.ErrSignInSessionKey, resp.KeyID)
		}
		identityKeys, err := identityKeysAt(doc, resp.SessionProof.IssuedAt)
		if err != nil {
			return nil, err
		}
		// Exactly the chain the relay runs on a forwarded session-signed
		// message (identity.VerifySessionProof is the single implementation).
		var verified identity.VerifiedSessionProof
		var proofErr error
		for _, key := range identityKeys {
			verified, proofErr = identity.VerifySessionProof(key, resp.Identity, *resp.SessionProof, now)
			if proofErr == nil {
				break
			}
		}
		if proofErr != nil {
			return nil, fmt.Errorf("%w: %v", identity.ErrSignInSessionKey, proofErr)
		}
		if !ed25519.Verify(verified.PublicKeyBytes, []byte(canonical), sig) {
			return nil, identity.ErrSignInSignature
		}
		sessionDelegated = true
	} else if resp.KeyID != identity.SignInKeyIDIdentity {
		return nil, fmt.Errorf("%w: key_id must be %q or %q<session id>", identity.ErrSignInMalformed,
			identity.SignInKeyIDIdentity, identity.SignInKeyIDSessionPrefix)
	} else {
		keys, err := identityKeysAt(doc, resp.IssuedAt)
		if err != nil {
			return nil, err
		}
		ok := false
		for _, key := range keys {
			if ed25519.Verify(key, []byte(canonical), sig) {
				ok = true
				break
			}
		}
		if !ok {
			return nil, identity.ErrSignInSignature
		}
	}

	return &Result{
		Identity:         resp.Identity,
		Audience:         audience,
		RequestID:        resp.RequestID,
		Action:           resp.Action,
		Statement:        resp.Statement,
		Scopes:           scopes,
		KeyID:            resp.KeyID,
		SessionDelegated: sessionDelegated,
		AppID:            appID,
		ExpiresAt:        expiresAt.UTC(),
		Relay:            doc.Relay,
		Document:         doc,
		Response:         resp,
	}, nil
}

// validateResponseWindow applies the same staleness rules to a response that
// the signer applied to the request it answers.
func validateResponseWindow(resp identity.SignInResponse, now time.Time) error {
	iat, err := time.Parse(time.RFC3339, resp.IssuedAt)
	if err != nil {
		return fmt.Errorf("%w: issued_at must be RFC3339", identity.ErrSignInMalformed)
	}
	exp, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		return fmt.Errorf("%w: expires_at must be RFC3339", identity.ErrSignInMalformed)
	}
	if !exp.After(iat) {
		return fmt.Errorf("%w: expires_at must be after issued_at", identity.ErrSignInMalformed)
	}
	if exp.Sub(iat) > identity.SignInMaxTTL {
		return identity.ErrSignInTTL
	}
	if iat.After(now.Add(identity.SignInMaxSkew)) {
		return identity.ErrSignInNotYet
	}
	if now.After(exp) {
		return identity.ErrSignInExpired
	}
	return nil
}

// identityKeysAt returns every signing key the document says was valid at
// `at`: the current one first, then any retired key still inside the
// rotation grace window the document itself declares. A sign-in signed
// moments before a rotation therefore still verifies, and one signed with a
// key whose grace window has closed does not.
//
// It returns a list rather than one key because a response carries no key
// hint — the verifier learns which key signed it by trying them.
func identityKeysAt(doc identity.IdentityDocument, at string) ([]ed25519.PublicKey, error) {
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		when = time.Now().UTC()
	}
	var keys []ed25519.PublicKey
	if key, err := identity.ParseEd25519PublicKey(doc.PublicKey); err == nil {
		keys = append(keys, key)
	}
	for _, prev := range doc.PreviousKeys {
		if !doc.KeyValidAt(prev.PublicKey, when) {
			continue
		}
		if key, err := identity.ParseEd25519PublicKey(prev.PublicKey); err == nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("identity document has no usable key valid at " + at)
	}
	return keys, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
