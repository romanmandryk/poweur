package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func lifecycleGrant(t *testing.T) ShareGrant {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	grant := ShareGrant{
		ShareID:     "shr_lifecycle",
		Owner:       "alice.example.org",
		Path:        "shared/project-x",
		Audience:    []ShareAudience{{ID: "bob.example.org"}},
		Permissions: []string{PermRead, PermWrite},
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := grant.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	return grant
}

func TestShareOfferValidateFor(t *testing.T) {
	offer := ShareOffer{
		Version:   ShareLifecycleVersion,
		Grant:     lifecycleGrant(t),
		OfferedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := offer.ValidateFor("bob.example.org"); err != nil {
		t.Fatal(err)
	}
	if err := offer.ValidateFor("mallory.example.org"); err == nil {
		t.Fatal("offer must reject a recipient outside the direct audience")
	}
	raw, err := json.Marshal(offer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseShareOffer(raw, "bob.example.org"); err != nil {
		t.Fatal(err)
	}
}

func TestShareLifecycleRejectsUnsafeShapes(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	tests := []struct {
		name string
		err  error
	}{
		{"accept traversal", ShareAccept{Version: 1, ShareID: "shr_1", Owner: "alice.example.org", Recipient: "bob.example.org", MountPath: "shared/alice.example.org/../x", AcceptedAt: now}.Validate()},
		{"accept wrong owner", ShareAccept{Version: 1, ShareID: "shr_1", Owner: "alice.example.org", Recipient: "bob.example.org", MountPath: "shared/mallory.example.org/x", AcceptedAt: now}.Validate()},
		{"revoked slash id", ShareRevoked{Version: 1, ShareID: "../shr", Owner: "alice.example.org", RevokedAt: now}.Validate()},
		{"mount private source", ShareMount{Version: 1, ShareID: "shr_1", Owner: "alice.example.org", SourcePath: "private/nope", Permissions: []string{PermRead}, AcceptedAt: now}.Validate()},
		{"mount unknown permission", ShareMount{Version: 1, ShareID: "shr_1", Owner: "alice.example.org", SourcePath: "shared/x", Permissions: []string{"admin"}, AcceptedAt: now}.Validate()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestShareMountRoundTrip(t *testing.T) {
	mount := ShareMount{
		Version:     1,
		ShareID:     "shr_1",
		Owner:       "alice.example.org",
		SourcePath:  "shared/project-x",
		Permissions: []string{PermRead},
		AcceptedAt:  "2026-09-23T20:00:00Z",
		ExpiresAt:   "2026-10-23T20:00:00Z",
	}
	raw, err := json.Marshal(mount)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseShareMount(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ShareID != mount.ShareID || parsed.SourcePath != mount.SourcePath {
		t.Fatalf("round trip mismatch: %+v", parsed)
	}
	if got, err := NormalizeShareMountPath("/shared/alice.example.org/project-x/", mount.Owner); err != nil || got != "shared/alice.example.org/project-x" {
		t.Fatalf("normalize mount path: got %q err=%v", got, err)
	}
}

func TestShareClaimValidation(t *testing.T) {
	token, err := GenerateLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	claim := ShareClaim{
		Version: 1, ShareID: "shr_request", Owner: "alice.poweur.net", Token: token,
		Claimant: "bob.poweur.net", Action: "uploaded", ClaimedAt: "2026-09-24T08:00:00Z",
	}
	raw, _ := json.Marshal(claim)
	if _, err := ParseShareClaim(raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, mutate := range []func(*ShareClaim){
		func(v *ShareClaim) { v.Token = "bad" },
		func(v *ShareClaim) { v.Action = "edited" },
		func(v *ShareClaim) { v.Owner = "not an id" },
		func(v *ShareClaim) { v.ClaimedAt = "today" },
	} {
		bad := claim
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("expected validation error")
		}
	}
}
