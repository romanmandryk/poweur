package signin

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/poweur/identity"
)

// SignOptions describe how an approval is signed.
type SignOptions struct {
	// Identity is the Poweur ID approving the request.
	Identity string
	// PrivateKey is the key that actually signs: the long-lived identity
	// key, or a session key when SessionProof is set.
	PrivateKey ed25519.PrivateKey
	// SessionID and SessionProof, when set, delegate the signature to a
	// registered session key so a daily sign-in never touches the
	// long-lived key. The proof is the same object the relay accepts on a
	// forwarded message.
	SessionID    string
	SessionProof *identity.SignInSessionProof
	// Now is injectable for tests and vector generation.
	Now func() time.Time
}

// Sign produces the user-signed approval for a request.
//
// The request is validated first: a signer must never sign an object it
// would not itself accept (expired, wrong scope namespace, response_uri
// pointing off-origin). Callers that display a consent screen should also
// have verified the origin via FetchMetadata — Sign cannot do that for them
// because it does no I/O.
func Sign(req identity.SignInRequest, opts SignOptions) (identity.SignInResponse, error) {
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	normalized, err := req.Normalize()
	if err != nil {
		return identity.SignInResponse{}, err
	}
	if err := normalized.Validate(now); err != nil {
		return identity.SignInResponse{}, err
	}
	keyID := identity.SignInKeyIDIdentity
	if opts.SessionProof != nil || opts.SessionID != "" {
		if opts.SessionProof == nil || opts.SessionID == "" {
			return identity.SignInResponse{}, fmt.Errorf("%w: session signing needs both a session id and a proof", identity.ErrSignInSessionKey)
		}
		keyID = identity.SignInKeyIDSessionPrefix + opts.SessionID
	}
	resp, err := identity.NewSignInResponse(normalized, opts.Identity, keyID)
	if err != nil {
		return identity.SignInResponse{}, err
	}
	resp.SessionProof = opts.SessionProof
	if len(opts.PrivateKey) != ed25519.PrivateKeySize {
		return identity.SignInResponse{}, fmt.Errorf("%w: signing key is not an Ed25519 private key", identity.ErrSignInMalformed)
	}
	sig := ed25519.Sign(opts.PrivateKey, []byte(resp.Canonical()))
	resp.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return resp, nil
}

// DescribeScope renders a scope for a consent screen. The signer UX spec
// (E08-T3) requires that every scope a user approves is shown as a sentence,
// never as a raw token — "app.example wants messages:send" tells a
// user nothing about what is at risk.
func DescribeScope(scope, appName string) string {
	if appName == "" {
		appName = "This app"
	}
	switch scope {
	case identity.ScopeProfileRead:
		return appName + " can read your public profile (name, avatar)."
	case identity.ScopeMessagesSend:
		return appName + " can send messages from your identity."
	}
	return appName + " requests an unrecognized permission (" + scope + ") — do not approve."
}

// DescribeScopes renders every scope in order.
func DescribeScopes(scopes []string, appName string) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, DescribeScope(s, appName))
	}
	return out
}

// SummarizeRequest is the one-line headline of a consent screen.
func SummarizeRequest(req identity.SignInRequest, appName string) string {
	host := req.Domain
	if host == "" {
		host = strings.TrimPrefix(strings.TrimPrefix(req.Audience, "https://"), "http://")
	}
	label := host
	if appName != "" && !strings.EqualFold(appName, host) {
		label = appName + " (" + host + ")"
	}
	switch req.Action {
	case identity.SignInActionSignup:
		return "Create an account at " + label + " with your Poweur ID"
	case identity.SignInActionLink:
		return "Link your Poweur ID to your existing account at " + label
	default:
		return "Sign in to " + label + " with your Poweur ID"
	}
}
