package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Sign in with Poweur ID (EPIC-008). The protocol is documented in
// apps/docs/docs/auth/sign-in.md; this file is the canonical Go
// implementation of the wire objects, the canonical signing string and the
// validation rules. The TypeScript twin lives in
// packages/client-ts/src/signin.ts and is pinned by the conformance vectors
// generated from packages/identity/vectors_test.go.
//
// Design invariants (do not change without changing the spec):
//
//   - The verifier is stateless: no tokens are issued by Poweur, no
//     registration with any authority, no federation metadata beyond the
//     RP's own /.well-known/poweur.json.
//   - The *audience* (the RP's verified origin) is inside the signed bytes,
//     exactly as WebAuthn puts the origin inside clientDataJSON. A response
//     collected by a phishing origin is bound to that origin and is useless
//     at the real one.
//   - Requests are short lived (<= SignInMaxTTL) and nonces are single use.

const (
	// SignInVersion is the protocol version carried in `poweur_auth`.
	SignInVersion = "1"

	// SignInMaxTTL caps expires_at - issued_at on a request and on the
	// resulting response. Five minutes is the replay window a verifier has
	// to remember a nonce for.
	SignInMaxTTL = 5 * time.Minute

	// SignInMaxSkew is the clock skew a verifier tolerates on issued_at.
	SignInMaxSkew = 2 * time.Minute

	// SignInKeyIDIdentity is the key_id value for a response signed by the
	// long-lived identity key.
	SignInKeyIDIdentity = "identity"

	// SignInKeyIDSessionPrefix marks a response signed by a registered
	// session key: "session:<session_id>".
	SignInKeyIDSessionPrefix = "session:"

	// SignInMaxStatementLen bounds what a signer has to render (and a user
	// has to read) before approving.
	SignInMaxStatementLen = 300

	// SignInMaxScopes bounds the consent screen.
	SignInMaxScopes = 16
)

// Sign-in actions. Anything else is rejected: a signer must be able to say
// in one line what it is approving.
const (
	SignInActionSignin = "signin"
	SignInActionSignup = "signup"
	SignInActionLink   = "link"
)

// Scope vocabulary (EPIC-008 E08-T4). Storage v1's `dav:` path scopes are
// gone with WebDAV; app storage access returns as scoped drive handles
// (EPIC-020, EPIC-029).
const (
	ScopeProfileRead  = "profile:read"
	ScopeMessagesSend = "messages:send"
)

var (
	ErrSignInMalformed  = errors.New("sign-in: malformed object")
	ErrSignInVersion    = errors.New("sign-in: unsupported protocol version")
	ErrSignInExpired    = errors.New("sign-in: expired")
	ErrSignInNotYet     = errors.New("sign-in: issued_at is in the future")
	ErrSignInTTL        = errors.New("sign-in: validity window exceeds the 5 minute maximum")
	ErrSignInAudience   = errors.New("sign-in: audience mismatch")
	ErrSignInScope      = errors.New("sign-in: invalid scope")
	ErrSignInAction     = errors.New("sign-in: invalid action")
	ErrSignInSignature  = errors.New("sign-in: signature verification failed")
	ErrSignInSessionKey = errors.New("sign-in: session proof invalid")
)

// SignInRequest is what a relying party builds and hands to a signer. It is
// deliberately *not* signed by the RP: an RP signature would prove nothing a
// TLS-served /.well-known/poweur.json does not already prove, and would push
// every RP into key management. Authenticity of the request comes from the
// signer fetching the RP metadata at `audience` over TLS.
type SignInRequest struct {
	PoweurAuth string `json:"poweur_auth"`
	RequestID  string `json:"request_id"`
	// Domain is the human-facing name of the RP host, rendered in the
	// consent screen ("guestbook.poweur.net").
	Domain string `json:"domain"`
	// Audience is the RP origin the response is bound to
	// ("https://guestbook.poweur.net"). Scheme + host + non-default port.
	Audience  string `json:"audience"`
	Nonce     string `json:"nonce"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	Action    string `json:"action"`
	Statement string `json:"statement,omitempty"`
	// ResponseURI is where the signer delivers the response. MUST be
	// same-origin with Audience — this is the confused-deputy guard.
	ResponseURI string `json:"response_uri,omitempty"`
	// Scopes requested for the second (resource) step. Empty = login only.
	Scopes []string `json:"scopes,omitempty"`
}

// SignInSessionProof is the session-delegation proof carried by a response
// signed with a short-lived session key. It is byte-identical to the relay's
// `SessionProof` (apps/api/internal/relay/types.go) and validated by the
// same code path (VerifySessionProof).
type SignInSessionProof struct {
	SessionPublicKey  string `json:"session_public_key"`
	IssuedAt          string `json:"issued_at"`
	ExpiresAt         string `json:"expires_at"`
	Nonce             string `json:"nonce"`
	IdentitySignature string `json:"identity_signature"`
}

// SignInResponse is the user-signed approval. Every field that a verifier
// makes a decision on is inside the canonical signing string.
type SignInResponse struct {
	PoweurAuth string   `json:"poweur_auth"`
	RequestID  string   `json:"request_id"`
	Identity   string   `json:"identity"`
	Audience   string   `json:"audience"`
	Nonce      string   `json:"nonce"`
	IssuedAt   string   `json:"issued_at"`
	ExpiresAt  string   `json:"expires_at"`
	Action     string   `json:"action"`
	Statement  string   `json:"statement,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
	// KeyID is "identity" or "session:<session_id>".
	KeyID        string              `json:"key_id"`
	SessionProof *SignInSessionProof `json:"session_proof,omitempty"`
	Signature    string              `json:"signature"`
}

// NormalizeOrigin returns the canonical origin form used in `audience`:
// lowercase scheme and host, default port dropped, no path, no trailing
// slash. Anything with a path, query, fragment or userinfo is rejected —
// an origin is not a URL.
func NormalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: empty origin", ErrSignInMalformed)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: origin unparseable", ErrSignInMalformed)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("%w: origin scheme must be https (http only for local development)", ErrSignInMalformed)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: origin must not carry userinfo, query or fragment", ErrSignInMalformed)
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("%w: origin must not carry a path", ErrSignInMalformed)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("%w: origin has no host", ErrSignInMalformed)
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + host + ":" + port, nil
	}
	return scheme + "://" + host, nil
}

// SameOrigin reports whether rawURL lives at origin. Used for the
// response_uri check: a signer never posts an approval anywhere but the
// origin it just showed the user.
func SameOrigin(origin, rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	got, err := NormalizeOrigin(u.Scheme + "://" + u.Host)
	if err != nil {
		return false
	}
	want, err := NormalizeOrigin(origin)
	if err != nil {
		return false
	}
	return got == want
}

// SignInAppID derives the app namespace from a verified origin by reversing
// the host labels: https://guestbook.poweur.net -> net.poweur.guestbook.
//
// This is what makes scope escalation structurally impossible rather than
// policy-enforced: the relay derives the namespace from the *signed*
// audience, so an RP cannot ask for another app's directory by naming it.
func SignInAppID(origin string) (string, error) {
	norm, err := NormalizeOrigin(origin)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(norm)
	host := u.Hostname()
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("%w: origin host needs at least two labels to derive an app id", ErrSignInMalformed)
	}
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	for _, l := range labels {
		if l == "" {
			return "", fmt.Errorf("%w: origin host has an empty label", ErrSignInMalformed)
		}
	}
	return strings.Join(labels, "."), nil
}

// NormalizeSignInScope canonicalizes one scope string.
func NormalizeSignInScope(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	switch s {
	case "":
		return "", fmt.Errorf("%w: empty scope", ErrSignInScope)
	case ScopeProfileRead, ScopeMessagesSend:
		return s, nil
	}
	return "", fmt.Errorf("%w: unknown scope %q", ErrSignInScope, raw)
}

// NormalizeSignInScopes normalizes, de-duplicates and sorts a scope list.
// Sorting is what makes the canonical string order-independent, so a signer
// may reorder the list for display without breaking the signature.
func NormalizeSignInScopes(scopes []string) ([]string, error) {
	if len(scopes) > SignInMaxScopes {
		return nil, fmt.Errorf("%w: at most %d scopes", ErrSignInScope, SignInMaxScopes)
	}
	seen := make(map[string]bool, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		n, err := NormalizeSignInScope(s)
		if err != nil {
			return nil, err
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// CanonicalSignInScopes renders a scope list for the signing string.
func CanonicalSignInScopes(scopes []string) string {
	return strings.Join(scopes, ",")
}

// CanonicalSignInResponse is the exact byte string a signer signs.
//
//	poweur-signin
//	<version>
//	<request_id>
//	<identity>
//	<audience>
//	<nonce>
//	<issued_at>
//	<expires_at>
//	<action>
//	<statement>
//	<scopes>      normalized, sorted, comma-joined ("" when none)
//	<key_id>
//
// The format is line oriented, so `statement` may not contain CR or LF —
// ValidateSignInStatement enforces that, and a signer that skipped the check
// would produce a string a verifier cannot re-derive.
func CanonicalSignInResponse(version, requestID, identity, audience, nonce, issuedAt, expiresAt, action, statement, scopes, keyID string) string {
	return strings.Join([]string{
		"poweur-signin",
		version,
		requestID,
		identity,
		audience,
		nonce,
		issuedAt,
		expiresAt,
		action,
		statement,
		scopes,
		keyID,
	}, "\n")
}

// Canonical returns the signing string for this response.
func (r SignInResponse) Canonical() string {
	return CanonicalSignInResponse(
		r.PoweurAuth, r.RequestID, strings.ToLower(strings.TrimSpace(r.Identity)),
		r.Audience, r.Nonce, r.IssuedAt, r.ExpiresAt, r.Action, r.Statement,
		CanonicalSignInScopes(r.Scopes), r.KeyID,
	)
}

// ValidateSignInStatement rejects statements that would break the
// line-oriented canonical string or overflow a consent screen.
func ValidateSignInStatement(s string) error {
	if len(s) > SignInMaxStatementLen {
		return fmt.Errorf("%w: statement longer than %d bytes", ErrSignInMalformed, SignInMaxStatementLen)
	}
	for _, r := range s {
		if r == '\n' || r == '\r' {
			return fmt.Errorf("%w: statement must be a single line", ErrSignInMalformed)
		}
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: statement contains a control character", ErrSignInMalformed)
		}
	}
	return nil
}

// ValidateSignInAction accepts only the three actions a consent screen knows
// how to describe.
func ValidateSignInAction(a string) error {
	switch a {
	case SignInActionSignin, SignInActionSignup, SignInActionLink:
		return nil
	}
	return fmt.Errorf("%w: %q", ErrSignInAction, a)
}

// validateSignInWindow checks issued_at/expires_at against now and the
// 5-minute cap.
func validateSignInWindow(issuedAt, expiresAt string, now time.Time) error {
	iat, err := time.Parse(time.RFC3339, issuedAt)
	if err != nil {
		return fmt.Errorf("%w: issued_at must be RFC3339", ErrSignInMalformed)
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return fmt.Errorf("%w: expires_at must be RFC3339", ErrSignInMalformed)
	}
	if !exp.After(iat) {
		return fmt.Errorf("%w: expires_at must be after issued_at", ErrSignInMalformed)
	}
	if exp.Sub(iat) > SignInMaxTTL {
		return ErrSignInTTL
	}
	if iat.After(now.Add(SignInMaxSkew)) {
		return ErrSignInNotYet
	}
	if now.After(exp) {
		return ErrSignInExpired
	}
	return nil
}

// Normalize returns the canonicalized copy of a request so a signer displays
// and signs the same values a verifier re-derives.
func (r SignInRequest) Normalize() (SignInRequest, error) {
	out := r
	out.PoweurAuth = strings.TrimSpace(r.PoweurAuth)
	if out.PoweurAuth == "" {
		out.PoweurAuth = SignInVersion
	}
	out.RequestID = strings.TrimSpace(r.RequestID)
	out.Nonce = strings.TrimSpace(r.Nonce)
	out.Action = strings.ToLower(strings.TrimSpace(r.Action))
	out.Statement = strings.TrimSpace(r.Statement)
	audience, err := NormalizeOrigin(r.Audience)
	if err != nil {
		return SignInRequest{}, err
	}
	out.Audience = audience
	if out.Domain == "" {
		u, _ := url.Parse(audience)
		out.Domain = u.Hostname()
	}
	out.Domain = strings.ToLower(strings.TrimSpace(out.Domain))
	scopes, err := NormalizeSignInScopes(r.Scopes)
	if err != nil {
		return SignInRequest{}, err
	}
	out.Scopes = scopes
	out.ResponseURI = strings.TrimSpace(r.ResponseURI)
	return out, nil
}

// Validate checks a request against the protocol rules at now. Callers
// should Normalize first; Validate normalizes defensively.
func (r SignInRequest) Validate(now time.Time) error {
	n, err := r.Normalize()
	if err != nil {
		return err
	}
	if n.PoweurAuth != SignInVersion {
		return fmt.Errorf("%w: %q", ErrSignInVersion, n.PoweurAuth)
	}
	if n.RequestID == "" || n.Nonce == "" {
		return fmt.Errorf("%w: request_id and nonce are required", ErrSignInMalformed)
	}
	if err := ValidateSignInAction(n.Action); err != nil {
		return err
	}
	if err := ValidateSignInStatement(n.Statement); err != nil {
		return err
	}
	if err := validateSignInWindow(n.IssuedAt, n.ExpiresAt, now); err != nil {
		return err
	}
	// The confused-deputy guard: an approval only ever travels back to the
	// origin whose name the user was shown.
	if n.ResponseURI != "" && !SameOrigin(n.Audience, n.ResponseURI) {
		return fmt.Errorf("%w: response_uri must be same-origin with audience", ErrSignInAudience)
	}
	// The audience must name an app the signer can show.
	if _, err := SignInAppID(n.Audience); err != nil {
		return err
	}
	return nil
}

// NewSignInResponse builds the response object for a request the signer has
// already validated. keyID is SignInKeyIDIdentity or "session:<id>".
// It copies the *request's* window verbatim: the approval is valid exactly
// as long as the challenge was, never longer.
func NewSignInResponse(req SignInRequest, identity, keyID string) (SignInResponse, error) {
	n, err := req.Normalize()
	if err != nil {
		return SignInResponse{}, err
	}
	identity = strings.ToLower(strings.TrimSpace(identity))
	if err := ValidateIdentityName(identity); err != nil {
		return SignInResponse{}, err
	}
	if keyID == "" {
		keyID = SignInKeyIDIdentity
	}
	if keyID != SignInKeyIDIdentity && !strings.HasPrefix(keyID, SignInKeyIDSessionPrefix) {
		return SignInResponse{}, fmt.Errorf("%w: key_id must be %q or %q<session id>", ErrSignInMalformed, SignInKeyIDIdentity, SignInKeyIDSessionPrefix)
	}
	return SignInResponse{
		PoweurAuth: SignInVersion,
		RequestID:  n.RequestID,
		Identity:   identity,
		Audience:   n.Audience,
		Nonce:      n.Nonce,
		IssuedAt:   n.IssuedAt,
		ExpiresAt:  n.ExpiresAt,
		Action:     n.Action,
		Statement:  n.Statement,
		Scopes:     n.Scopes,
		KeyID:      keyID,
	}, nil
}

// SessionIDFromKeyID returns the session id encoded in key_id, or "" when
// the response was signed by the long-lived identity key.
func SessionIDFromKeyID(keyID string) string {
	if strings.HasPrefix(keyID, SignInKeyIDSessionPrefix) {
		return strings.TrimPrefix(keyID, SignInKeyIDSessionPrefix)
	}
	return ""
}

// CanonicalSessionRegistration is the string the long-lived identity key
// signs to authorize a short-lived session key.
//
// This is the single definition of that string in the repo: the relay's
// crypto package delegates here, and VerifySessionProof below is the one
// implementation of the proof chain that the relay and the sign-in verifier
// both run.
func CanonicalSessionRegistration(identity, sessionPublicKey, issuedAt, expiresAt, nonce string) string {
	return strings.Join([]string{
		"session-registration",
		identity,
		sessionPublicKey,
		issuedAt,
		expiresAt,
		nonce,
	}, "\n")
}

// MaxSessionTTL caps how long a delegated session key may live. Mirrors the
// relay's own cap; a proof claiming more is rejected everywhere.
const MaxSessionTTL = 24 * time.Hour

// VerifiedSessionProof is what VerifySessionProof returns on success.
type VerifiedSessionProof struct {
	// PublicKey is the normalized (base64url, unpadded) session key.
	PublicKey string
	// PublicKeyBytes is the parsed key to verify the payload with.
	PublicKeyBytes ed25519.PublicKey
	IssuedAt       time.Time
	ExpiresAt      time.Time
}

// VerifySessionProof validates a session-delegation proof against the
// identity's long-lived public key and returns the session public key the
// caller should verify the actual payload with.
//
// Every field is checked: completeness, RFC3339 timestamps, expiry at `now`,
// the 24h TTL cap, key encoding, and the identity signature over
// CanonicalSessionRegistration. The relay's acceptSessionProof and the
// sign-in verifier both call this, so there is exactly one chain to audit.
func VerifySessionProof(identityPub ed25519.PublicKey, identityName string, proof SignInSessionProof, now time.Time) (VerifiedSessionProof, error) {
	if proof.SessionPublicKey == "" || proof.IssuedAt == "" || proof.ExpiresAt == "" ||
		proof.Nonce == "" || proof.IdentitySignature == "" {
		return VerifiedSessionProof{}, errors.New("session proof incomplete")
	}
	iat, err := time.Parse(time.RFC3339, proof.IssuedAt)
	if err != nil {
		return VerifiedSessionProof{}, errors.New("session proof issued_at invalid")
	}
	exp, err := time.Parse(time.RFC3339, proof.ExpiresAt)
	if err != nil {
		return VerifiedSessionProof{}, errors.New("session proof expires_at invalid")
	}
	if now.After(exp) {
		return VerifiedSessionProof{}, errors.New("session proof expired")
	}
	if exp.Sub(iat) > MaxSessionTTL {
		return VerifiedSessionProof{}, errors.New("session proof exceeds max TTL")
	}
	pubBytes, err := ParseEd25519PublicKey(proof.SessionPublicKey)
	if err != nil {
		return VerifiedSessionProof{}, errors.New("session proof public key invalid: " + err.Error())
	}
	normalized := base64.RawURLEncoding.EncodeToString(pubBytes)
	canonical := CanonicalSessionRegistration(identityName, normalized, proof.IssuedAt, proof.ExpiresAt, proof.Nonce)
	sig, err := DecodeAnyBase64(proof.IdentitySignature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return VerifiedSessionProof{}, errors.New("session proof signature invalid")
	}
	if !ed25519.Verify(identityPub, []byte(canonical), sig) {
		return VerifiedSessionProof{}, errors.New("session proof signature invalid")
	}
	return VerifiedSessionProof{
		PublicKey:      normalized,
		PublicKeyBytes: ed25519.PublicKey(pubBytes),
		IssuedAt:       iat.UTC(),
		ExpiresAt:      exp.UTC(),
	}, nil
}

// DecodeAnyBase64 accepts every base64 variant the protocol has emitted
// (raw/padded, std/url). Signatures and keys travel in more than one of
// them across the Go and TypeScript clients.
func DecodeAnyBase64(value string) ([]byte, error) {
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

// EncodeSignInRequest renders a request for a URL parameter: compact JSON,
// base64url without padding. Deep links, QR codes and redirects all carry
// this single form.
func EncodeSignInRequest(req SignInRequest) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeSignInRequest parses the encoded form produced by
// EncodeSignInRequest. Padded base64url and raw JSON are also accepted: a
// user pasting a request should not have to care which one they copied.
func DecodeSignInRequest(encoded string) (SignInRequest, error) {
	raw, err := decodeSignInBlob(encoded, "request")
	if err != nil {
		return SignInRequest{}, err
	}
	var req SignInRequest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return SignInRequest{}, fmt.Errorf("%w: %v", ErrSignInMalformed, err)
	}
	return req, nil
}

// EncodeSignInResponse renders a signed response as base64url compact JSON —
// the cross-device "copy this code" form and the `response` parameter of a
// redirect.
func EncodeSignInResponse(resp SignInResponse) (string, error) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeSignInResponse is the inverse of EncodeSignInResponse and also
// accepts raw JSON (an RP receiving a POSTed body).
func DecodeSignInResponse(encoded string) (SignInResponse, error) {
	raw, err := decodeSignInBlob(encoded, "response")
	if err != nil {
		return SignInResponse{}, err
	}
	var resp SignInResponse
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		return SignInResponse{}, fmt.Errorf("%w: %v", ErrSignInMalformed, err)
	}
	return resp, nil
}

func decodeSignInBlob(encoded, what string) ([]byte, error) {
	s := strings.TrimSpace(encoded)
	if s == "" {
		return nil, fmt.Errorf("%w: empty %s", ErrSignInMalformed, what)
	}
	if len(s) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: %s too large", ErrSignInMalformed, what)
	}
	if strings.HasPrefix(s, "{") {
		return []byte(s), nil
	}
	decoded, err := DecodeAnyBase64(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is neither JSON nor base64url", ErrSignInMalformed, what)
	}
	return decoded, nil
}

// SignInDeepLink renders the deep link / QR payload for a request.
func SignInDeepLink(req SignInRequest) (string, error) {
	encoded, err := EncodeSignInRequest(req)
	if err != nil {
		return "", err
	}
	return "poweur://auth?request=" + encoded, nil
}

// SignInWebLink renders the same request as a handoff URL into a web signer
// (signerBase is e.g. "https://poweur.net/app/").
func SignInWebLink(signerBase string, req SignInRequest) (string, error) {
	encoded, err := EncodeSignInRequest(req)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(strings.TrimSpace(signerBase), "?&")
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "auth=" + url.QueryEscape(encoded), nil
}
