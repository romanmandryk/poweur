package identity

import (
	"fmt"
	"net/url"
	"strings"
)

// NormalizeIDInput turns what a person types into a login box — or what an
// IndieAuth client sends as `me` — into a canonical Poweur ID.
//
// Accepted: a bare name (`Alice.Example.com`, `@alice.example.com`,
// `alice.example.com.`) or a profile URL with an empty or root path
// (`https://alice.example.com/`). Refused: any other path, a port, query,
// fragment, userinfo, a non-https scheme (unless allowHTTP, for local
// development), a `www.` name, and anything ValidateIdentityName refuses —
// which includes raw Unicode, so an IDN must already be in its xn-- form.
//
// No alias is ever inferred: "www." is neither stripped nor added, because a
// Poweur ID is exactly the name whose document resolves.
//
// Specified in apps/docs/docs/auth/oauth-oidc-bridge.md ("Identifiers") and
// pinned by testdata/vectors/id-input.json.
func NormalizeIDInput(input string, allowHTTP bool) (string, error) {
	value := strings.TrimSpace(input)
	value = strings.TrimPrefix(value, "@")
	if value == "" {
		return "", fmt.Errorf("enter a Poweur ID")
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil {
			return "", fmt.Errorf("not a valid profile URL")
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
		case "http":
			if !allowHTTP {
				return "", fmt.Errorf("profile URLs must use https")
			}
		default:
			return "", fmt.Errorf("profile URLs must use https")
		}
		if u.User != nil {
			return "", fmt.Errorf("a profile URL must not contain credentials")
		}
		if u.Port() != "" {
			return "", fmt.Errorf("a profile URL must not contain a port")
		}
		if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
			return "", fmt.Errorf("a profile URL must not contain a query or fragment")
		}
		if u.Path != "" && u.Path != "/" {
			return "", fmt.Errorf("a profile URL must be the root of the identity's site")
		}
		value = u.Hostname()
	} else if strings.ContainsAny(value, "/?#:@ ") {
		return "", fmt.Errorf("that does not look like a Poweur ID")
	}
	value = strings.TrimSuffix(strings.ToLower(value), ".")
	if err := ValidateIdentityName(value); err != nil {
		return "", err
	}
	// A typed "www.example.com" is someone's website, not their ID. Refused
	// explicitly (it once fell out of the reserved-label check, which now
	// guards claiming names rather than using them).
	if strings.HasPrefix(value, "www.") {
		return "", fmt.Errorf("that is a website address (%s), not a Poweur ID", value)
	}
	return value, nil
}

// IndieAuthProfileURL is the IndieAuth `me` for a canonical Poweur ID.
func IndieAuthProfileURL(id string) string {
	return "https://" + id + "/"
}

// DIDWebID is the did:web identifier for a canonical Poweur ID. The full
// document projection is DIDWeb.
func DIDWebID(id string) string {
	return "did:web:" + id
}
