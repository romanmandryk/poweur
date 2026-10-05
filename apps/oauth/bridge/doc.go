// Package bridge is the Poweur OAuth 2.0 / OpenID Connect bridge (EPIC-022).
//
// It is a conventional OpenID Provider whose only authentication method is a
// native Poweur Sign-In proof: the bridge is itself a Poweur relying party,
// verifies the user's signature through the public resolver chain, and then
// speaks ordinary OIDC to applications that cannot verify Poweur IDs
// themselves. It accepts any publicly resolvable Poweur ID and shares no
// secret with any relay.
//
// The normative design is apps/docs/docs/auth/oauth-oidc-bridge.md.
package bridge

// Version is the bridge's semver. Bump the patch when shipping behaviour
// changes (AGENTS.md → Version bumps).
const Version = "0.2.2"

// Build stamps, set by the binary from VERSION_HASH and BUILD_TIME (the image
// bakes them in, as the relay's does). /health reports them so a deploy can
// tell the new build is the one answering.
var (
	VersionHash string
	BuildTime   string
)
