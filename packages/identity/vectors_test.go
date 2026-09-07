package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Conformance vectors (EPIC-017 E17-T5). Go is the canonical implementation
// of the Poweur protocol; the TypeScript client in packages/client-ts
// conforms to it. These tests emit deterministic fixtures that the TS suite
// re-derives and compares against, so a canonical string that changes here
// and not there turns CI red instead of silently splitting the two clients.
//
// Regenerate with `go test ./packages/identity/...`; the output is a pure
// function of the fixed seeds below, so a clean tree stays clean.
//
// Sibling generators live next to what they pin:
//   apps/api/internal/crypto/vectors_test.go  — canonical signing strings
//   apps/cli/internal/crypto/vectors_test.go  — message encryption

const vectorsDir = "testdata/vectors"

// VectorSeed is the fixed Ed25519 seed every vector signs with. Sibling
// generators use the same one so a TS test needs a single key to verify all
// of them.
var VectorSeed = []byte{
	1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32,
}

// VectorTime is the fixed instant stamped into every vector.
const VectorTime = "2026-01-15T09:30:00Z"

func vectorKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(VectorSeed)
}

// WriteVectors marshals v to <dir>/<name>.json with a trailing newline.
func WriteVectors(t *testing.T, dir, name string, v any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type documentVector struct {
	Name      string           `json:"name"`
	Document  IdentityDocument `json:"document"`
	Canonical string           `json:"canonical"`
	Signature string           `json:"signature"`
	// KeyValid pins KeyValidAt for rotation-grace cases.
	KeyValid map[string]bool `json:"key_valid,omitempty"`
}

func TestVectors_Documents(t *testing.T) {
	priv := vectorKey()
	pub := priv.Public().(ed25519.PublicKey)
	pubStr := FormatEd25519PublicKey(pub)
	encStr := FormatX25519PublicKey(VectorSeed) // any 32 bytes; only encoding matters here

	build := func(mutate func(*IdentityDocument)) IdentityDocument {
		doc := NewDocument("alice.poweur.net", pubStr, encStr, "poweur.net", nil)
		doc.UpdatedAt = VectorTime
		if mutate != nil {
			mutate(&doc)
		}
		if err := doc.Sign(priv); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return doc
	}

	minimal := NewDocument("bob.example.org", pubStr, "", "relay.example.org", []string{})
	minimal.UpdatedAt = VectorTime
	if err := minimal.Sign(priv); err != nil {
		t.Fatalf("sign minimal: %v", err)
	}

	rotated := build(func(d *IdentityDocument) {
		d.PreviousKeys = []PreviousKey{
			{PublicKey: "ed25519:" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ValidUntil: "2026-02-01T00:00:00Z"},
		}
	})
	moved := build(func(d *IdentityDocument) { d.MovedTo = "alice.example.net" })

	vectors := []documentVector{}
	for _, entry := range []struct {
		name string
		doc  IdentityDocument
	}{
		{"full", build(nil)},
		{"minimal", minimal},
		{"rotated", rotated},
		{"moved", moved},
	} {
		canon, err := entry.doc.CanonicalBytes()
		if err != nil {
			t.Fatalf("canonical %s: %v", entry.name, err)
		}
		if err := entry.doc.Verify(); err != nil {
			t.Fatalf("verify %s: %v", entry.name, err)
		}
		vectors = append(vectors, documentVector{
			Name:      entry.name,
			Document:  entry.doc,
			Canonical: string(canon),
			Signature: entry.doc.Signature,
		})
	}

	// Rotation grace: the retired key is valid up to valid_until, not after.
	at := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	retired := rotated.PreviousKeys[0].PublicKey
	vectors[2].KeyValid = map[string]bool{
		rotated.PublicKey: rotated.KeyValidAt(rotated.PublicKey, at),
		retired:           rotated.KeyValidAt(retired, at),
	}
	if !vectors[2].KeyValid[retired] {
		t.Fatal("expected the retired key to still be valid inside its grace window")
	}

	WriteVectors(t, vectorsDir, "documents", vectors)
}

type grantVector struct {
	Name      string     `json:"name"`
	Grant     ShareGrant `json:"grant"`
	Canonical string     `json:"canonical"`
}

type groupVector struct {
	Name      string     `json:"name"`
	Group     ShareGroup `json:"group"`
	Canonical string     `json:"canonical"`
}

type pathVector struct {
	Input string `json:"input"`
	Path  string `json:"path,omitempty"`
	Error bool   `json:"error"`
}

func TestVectors_Grants(t *testing.T) {
	priv := vectorKey()
	pub := priv.Public().(ed25519.PublicKey)

	grants := []grantVector{}
	for _, entry := range []struct {
		name  string
		grant ShareGrant
	}{
		{"single-id-read", ShareGrant{
			ShareID: "shr_0011223344556677", Owner: "Alice.Poweur.NET",
			Path: "/shared/project-x/", Audience: []ShareAudience{{ID: "bob.example.org"}},
			Permissions: []string{PermRead}, CreatedAt: VectorTime,
		}},
		{"multi-audience-rw", ShareGrant{
			ShareID: "shr_8899aabbccddeeff", Owner: "alice.poweur.net",
			Path: "apps/notes/shared",
			// Deliberately unsorted and mixed-case: the canonical form sorts
			// and lowercases, so a client that skips that step fails here.
			Audience:    []ShareAudience{{Group: "Team"}, {ID: "Zoe.example.org"}, {ID: "bob.example.org"}},
			Permissions: []string{PermWrite, PermRead},
			CreatedAt:   VectorTime, ExpiresAt: "2026-06-01T00:00:00Z",
		}},
	} {
		grant := entry.grant
		if err := grant.Sign(priv); err != nil {
			t.Fatalf("sign %s: %v", entry.name, err)
		}
		if err := grant.VerifySignature(pub); err != nil {
			t.Fatalf("verify %s: %v", entry.name, err)
		}
		grants = append(grants, grantVector{entry.name, grant, grant.Canonical()})
	}
	WriteVectors(t, vectorsDir, "grants", grants)

	groups := []groupVector{}
	for _, entry := range []struct {
		name  string
		group ShareGroup
	}{
		{"team", ShareGroup{
			Group: "Team", Owner: "Alice.poweur.net",
			Members:   []string{"Zoe.example.org", "bob.example.org", " carol.poweur.net "},
			UpdatedAt: VectorTime,
		}},
		{"empty", ShareGroup{Group: "nobody", Owner: "alice.poweur.net", UpdatedAt: VectorTime}},
	} {
		group := entry.group
		if err := group.Sign(priv); err != nil {
			t.Fatalf("sign group %s: %v", entry.name, err)
		}
		if err := group.VerifySignature(pub); err != nil {
			t.Fatalf("verify group %s: %v", entry.name, err)
		}
		groups = append(groups, groupVector{entry.name, group, group.Canonical()})
	}
	WriteVectors(t, vectorsDir, "groups", groups)

	paths := []pathVector{}
	for _, input := range []string{
		"shared/a", "/shared/a/b/", "apps/notes/x",
		"shared", "apps", "private/secret", "poweur-sys/relay", "", "shared/../etc",
	} {
		normalized, err := NormalizeGrantPath(input)
		paths = append(paths, pathVector{Input: input, Path: normalized, Error: err != nil})
	}
	WriteVectors(t, vectorsDir, "grant-paths", paths)
}

type powVector struct {
	Token    string `json:"token"`
	Bits     int    `json:"bits"`
	Solution string `json:"solution"`
	Valid    bool   `json:"valid"`
}

type clampVector struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

func TestVectors_Pow(t *testing.T) {
	// Fixed tokens with pre-solved nonces: the TS solver may produce a
	// different nonce (the alphabet is not part of the contract), but it must
	// agree with Go on whether a given nonce satisfies a difficulty.
	vectors := []powVector{}
	for _, entry := range []struct {
		token string
		bits  int
	}{
		{"poweur-vector-token-1", 8},
		{"poweur-vector-token-2", 12},
		{"poweur-vector-token-3", 4},
	} {
		solution, err := SolvePow(t.Context(), entry.token, entry.bits)
		if err != nil {
			t.Fatalf("solve %s: %v", entry.token, err)
		}
		vectors = append(vectors, powVector{
			Token:    entry.token,
			Bits:     entry.bits,
			Solution: solution,
			Valid:    CheckPowSolution(entry.token, solution, entry.bits),
		})
	}
	// A deliberate non-solution: both sides must reject it.
	vectors = append(vectors, powVector{
		Token: "poweur-vector-token-1", Bits: 24, Solution: "not-a-solution",
		Valid: CheckPowSolution("poweur-vector-token-1", "not-a-solution", 24),
	})
	WriteVectors(t, vectorsDir, "pow", vectors)

	clamps := []clampVector{}
	for _, input := range []int{0, -5, 1, 8, 16, 30, 31, 100} {
		clamps = append(clamps, clampVector{input, ClampPowBits(input)})
	}
	WriteVectors(t, vectorsDir, "pow-clamp", clamps)
}

type nameVector struct {
	Identity string `json:"identity"`
	Valid    bool   `json:"valid"`
	Hosted   bool   `json:"hosted_valid"`
	DirName  string `json:"dir_name,omitempty"`
}

func TestVectors_Names(t *testing.T) {
	vectors := []nameVector{}
	for _, name := range []string{
		"alice.poweur.net", "a-b.example.org", "bob.co.uk", "ab.poweur.net",
		"www.poweur.net", "dav.example.org", "nodots", "192.168.0.1",
		"-bad.example.org", "bad-.example.org", "UPPER.Example.ORG", "",
	} {
		vector := nameVector{
			Identity: name,
			Valid:    ValidateIdentityName(name) == nil,
			Hosted:   ValidateHostedHandle(name) == nil,
		}
		if dir, err := SanitizeIdentityDirName(name); err == nil {
			vector.DirName = dir
		}
		vectors = append(vectors, vector)
	}
	WriteVectors(t, vectorsDir, "names", vectors)
}

type sysDocVector struct {
	Name string          `json:"name"`
	Raw  json.RawMessage `json:"raw"`
	// Valid records whether Go accepts the document, so the TS validator
	// agrees on rejections as well as acceptances.
	Valid bool `json:"valid"`
}

func TestVectors_SysDocs(t *testing.T) {
	contacts := []sysDocVector{}
	for _, entry := range []struct {
		name string
		raw  string
	}{
		{"accepted", `{"version":1,"contacts":[{"identity":"bob.example.org","state":"accepted","pinned_key":"ed25519:AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA","petname":"Bob","added_at":"2026-01-15T09:30:00Z"}]}`},
		{"empty", `{"version":1,"contacts":[]}`},
		{"bad-state", `{"version":1,"contacts":[{"identity":"bob.example.org","state":"pending"}]}`},
		{"duplicate", `{"version":1,"contacts":[{"identity":"bob.example.org","state":"accepted"},{"identity":"BOB.example.org","state":"blocked"}]}`},
	} {
		_, err := ParseContactsFile([]byte(entry.raw))
		contacts = append(contacts, sysDocVector{entry.name, json.RawMessage(entry.raw), err == nil})
	}
	WriteVectors(t, vectorsDir, "contacts", contacts)

	policies := []sysDocVector{}
	for _, entry := range []struct {
		name string
		raw  string
	}{
		{"open", `{"version":1,"mode":"open"}`},
		{"contacts-only", `{"version":1,"mode":"contacts_only"}`},
		{"anon-pow", `{"version":1,"mode":"contacts_and_requests","anonymous":{"allow":true,"challenge":"pow","pow_bits":18}}`},
		{"bad-mode", `{"version":1,"mode":"everyone"}`},
		{"bad-challenge", `{"version":1,"mode":"open","anonymous":{"allow":true,"challenge":"captcha"}}`},
	} {
		_, err := ParseInboxPolicy([]byte(entry.raw))
		policies = append(policies, sysDocVector{entry.name, json.RawMessage(entry.raw), err == nil})
	}
	WriteVectors(t, vectorsDir, "inbox-policies", policies)

	// profile.json is owner-written and relay-validated, and the web app
	// (EPIC-015 E15-T5) is the first thing to write one from a browser — so
	// the TS validator has to refuse exactly what Go refuses, especially the
	// avatar rule, which is what keeps a profile from pointing at an
	// off-tree URL.
	profiles := []sysDocVector{}
	for _, entry := range []struct {
		name string
		raw  string
	}{
		{"full", `{"version":1,"display_name":"Alice","avatar":"public/avatar.png","bio":"builder","links":[{"label":"site","url":"https://example.org"}],"locale":"en"}`},
		{"empty", `{"version":1}`},
		{"avatar-off-tree", `{"version":1,"avatar":"https://cdn.example.org/a.png"}`},
		{"avatar-outside-public", `{"version":1,"avatar":"private/avatar.png"}`},
		{"link-without-url", `{"version":1,"links":[{"label":"site"}]}`},
		{"bad-version", `{"version":2}`},
	} {
		_, err := ParseProfile([]byte(entry.raw))
		profiles = append(profiles, sysDocVector{entry.name, json.RawMessage(entry.raw), err == nil})
	}
	WriteVectors(t, vectorsDir, "profiles", profiles)
}
