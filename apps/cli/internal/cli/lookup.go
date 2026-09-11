package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Rendering an identity's public self-description for `poweur identity
// lookup` (EPIC-006 E06-T2).
//
// `poweur-sys/public/` is world-served at `/.well-known/poweur/`, so the two
// documents that say who somebody is — profile.json and capabilities.json —
// are reachable over exactly the route that already serves id.json. The
// lookup used to print keys and stop, which meant the CLI could verify a
// stranger and still had nothing to show the human who asked.

// lookupPublicFiles fetches profile.json and capabilities.json best-effort.
// A missing document is the normal case and returns nil; an unreachable host
// returns a note for stderr, because "this identity publishes no profile" and
// "we could not ask" are different facts and only one of them is about them.
func lookupPublicFiles(ctx context.Context, res idpkg.Result) (*idpkg.Profile, *idpkg.Capabilities, string) {
	var (
		profile *idpkg.Profile
		caps    *idpkg.Capabilities
		note    string
	)
	if p, err := identity.FetchProfile(ctx, res.Document.Identity); err == nil {
		profile = &p
	} else if !errors.Is(err, idpkg.ErrPublicFileAbsent) {
		note = fmt.Sprintf("note: could not read %s's public profile: %v", res.Document.Identity, err)
	}
	if c, err := identity.FetchCapabilities(ctx, res.Document.Identity); err == nil {
		caps = &c
	} else if fallback := idpkg.CapabilitiesFromDocument(res.Document); len(fallback.Features) > 0 {
		// The document's own capability list is the floor, and until an
		// identity is on its own host it is usually the only answer there is.
		caps = &fallback
	}
	return profile, caps, note
}

func printProfile(stdout io.Writer, identityValue string, profile *idpkg.Profile) {
	if profile == nil {
		return
	}
	if profile.DisplayName != "" {
		fmt.Fprintf(stdout, "display name: %s\n", profile.DisplayName)
	}
	if profile.Bio != "" {
		fmt.Fprintf(stdout, "bio: %s\n", profile.Bio)
	}
	if profile.Locale != "" {
		fmt.Fprintf(stdout, "locale: %s\n", profile.Locale)
	}
	if url := idpkg.AvatarURL(identityValue, profile.Avatar, identity.ResolveScheme()); url != "" {
		fmt.Fprintf(stdout, "avatar: %s\n", url)
	}
	for _, link := range profile.Links {
		if link.Label != "" {
			fmt.Fprintf(stdout, "link: %s — %s\n", link.Label, link.URL)
			continue
		}
		fmt.Fprintf(stdout, "link: %s\n", link.URL)
	}
}

func printCapabilities(stdout io.Writer, caps *idpkg.Capabilities) {
	if caps == nil {
		return
	}
	// Sorted: a map's iteration order would reshuffle the output between
	// runs of the same lookup, which makes diffing two identities painful.
	for _, key := range sortedKeys(caps.Features) {
		if value := caps.Features[key]; value != "" {
			fmt.Fprintf(stdout, "capability: %s=%s\n", key, value)
		} else {
			fmt.Fprintf(stdout, "capability: %s\n", key)
		}
	}
	for _, key := range sortedKeys(caps.Endpoints) {
		fmt.Fprintf(stdout, "endpoint: %s=%s\n", key, caps.Endpoints[key])
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
