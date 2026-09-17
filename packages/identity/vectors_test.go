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

// Fixed link-share inputs for the grant vectors (E05-T4).
const (
	vectorLinkToken        = "k7m4qz2rt6vwx3ab5cdefghijn"
	vectorLinkPasswordHash = "$argon2id$v=19$m=65536,t=1,p=4$eMqI4VYMYTc/H1SPsG5UbQ$Lg5Zrc+Mil5yDeAaWMyivDMdKiTmndk543TXv4rurPE"
)

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
		// Link shares (E05-T4). The token is fixed (not generated) so the
		// fixture is stable, and the password hash is a literal PHC string
		// for the same reason — argon2id salts are random, and a vector
		// that changed on every run would pin nothing.
		{"link-plain", ShareGrant{
			ShareID: "shr_link0011223344", Owner: "alice.poweur.net",
			Path: "/shared/project-x/", Audience: []ShareAudience{{Link: vectorLinkToken}},
			Permissions: []string{PermRead}, CreatedAt: VectorTime,
		}},
		{"link-password-capped", ShareGrant{
			ShareID: "shr_link5566778899", Owner: "alice.poweur.net",
			Path: "shared/project-x/handout.pdf", Audience: []ShareAudience{{Link: vectorLinkToken}},
			Permissions: []string{PermRead}, CreatedAt: VectorTime,
			ExpiresAt: "2026-06-01T00:00:00Z",
			Link:      &ShareLink{Password: vectorLinkPasswordHash, MaxDownloads: 25},
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
		// Group identities (E05-T5): the same document, signed by the
		// group's own key, with an admin list and a membership epoch. The
		// two vectors above have no admins and so must keep signing the
		// exact five lines they always did.
		{"group-identity", ShareGroup{
			Group: "Team.acme.poweur.net", Owner: "team.ACME.poweur.net",
			Members:   []string{"Zoe.example.org", "bob.example.org", " carol.poweur.net "},
			Admins:    []string{"Zoe.example.org", "bob.example.org"},
			Epoch:     3,
			UpdatedAt: VectorTime,
		}},
		{"group-identity-founding", ShareGroup{
			Group: "solo.acme.poweur.net", Owner: "solo.acme.poweur.net",
			Members:   []string{"alice.poweur.net"},
			Admins:    []string{"alice.poweur.net"},
			Epoch:     1,
			UpdatedAt: VectorTime,
		}},
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

// fingerprintVector pins the short auth string (EPIC-007 E07-T4). This is
// the one derivation a *human* transcribes between two implementations, so
// a Go/TS split here is not a failed request — it is two people on a phone
// call concluding they are being attacked when they are not.
type fingerprintVector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	// Kind is "signing" (ed25519) or "encryption" (x25519).
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
	Valid       bool   `json:"valid"`
}

func TestVectors_Fingerprints(t *testing.T) {
	pub := vectorKey().Public().(ed25519.PublicKey)
	raw := []byte(pub)
	flipped := append([]byte(nil), raw...)
	flipped[31] ^= 0x01

	vectors := []fingerprintVector{}
	add := func(name, kind, key string) {
		var fp string
		var err error
		if kind == "encryption" {
			fp, err = EncryptionKeyFingerprint(key)
		} else {
			fp, err = KeyFingerprint(key)
		}
		vectors = append(vectors, fingerprintVector{
			Name: name, Key: key, Kind: kind, Fingerprint: fp, Valid: err == nil,
		})
	}

	add("vector-key", "signing", FormatEd25519PublicKey(pub))
	// The same key in every encoding a document might carry it in: all four
	// must land on one string or the invariant is not an invariant.
	add("vector-key-bare", "signing", base64.RawURLEncoding.EncodeToString(raw))
	add("vector-key-padded", "signing", base64.URLEncoding.EncodeToString(raw))
	add("vector-key-std", "signing", base64.StdEncoding.EncodeToString(raw))
	add("one-bit-flip", "signing", FormatEd25519PublicKey(ed25519.PublicKey(flipped)))
	add("all-zero-key", "signing", FormatEd25519PublicKey(make([]byte, 32)))
	// Same 32 bytes, other algorithm: the prefix inside the hash keeps these apart.
	add("same-bytes-as-x25519", "encryption", FormatX25519PublicKey(raw))
	add("malformed", "signing", "ed25519:not-base64!!")
	add("wrong-length", "signing", "ed25519:"+base64.RawURLEncoding.EncodeToString([]byte("short")))

	WriteVectors(t, vectorsDir, "fingerprints", vectors)
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
		// Homoglyphs (EPIC-018 E18-T1). Without these in the vectors the two
		// implementations could disagree about "аdmin" — as they did, silently,
		// while both used a Unicode letter class.
		"аdmin.poweur.net", "аlice.poweur.net", "xn--80ak6aa92e.example.org",
		// Newly reserved labels, so a TS list that drifts from Go's is caught.
		"support.poweur.net", "verify.poweur.net", "id.poweur.net",
		"oauth.poweur.net", "indieauth.poweur.net",
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

// historyVector pins the client-side message archive (EPIC-009 E09-T1). The
// path is the interesting half: two clients that disagree about where a
// message is filed write it twice, and the sender-chosen id inside that path
// is untrusted input, so the sanitizing rule has to be identical on both
// sides or one of them has a traversal the other does not.
type historyVector struct {
	Name      string `json:"name"`
	Timestamp string `json:"timestamp"`
	ID        string `json:"id"`
	Path      string `json:"path"`
}

type readStateVector struct {
	Name   string              `json:"name"`
	Raw    json.RawMessage     `json:"raw"`
	Valid  bool                `json:"valid"`
	Unread map[string]int      `json:"unread,omitempty"`
	Marks  map[string]ReadMark `json:"marks,omitempty"`
}

func TestVectors_History(t *testing.T) {
	paths := []historyVector{}
	for _, entry := range []struct{ name, ts, id string }{
		{"ordinary", "2026-01-15T09:30:00Z", "msg_1757404500000_AbC-_9"},
		{"month-boundary", "2026-01-31T23:59:59Z", "msg_end"},
		{"next-month", "2026-02-01T00:00:00Z", "msg_start"},
		{"offset-timestamp", "2026-01-15T10:30:00+01:00", "msg_offset"},
		{"traversal-id", "2026-01-15T09:30:00Z", "../../poweur-sys/relay/contacts"},
		{"slash-id", "2026-01-15T09:30:00Z", "a/b"},
		{"empty-id", "2026-01-15T09:30:00Z", ""},
		{"dotfile-id", "2026-01-15T09:30:00Z", ".poweur-web-public"},
		{"unparseable-timestamp", "yesterday", "msg_undated"},
	} {
		paths = append(paths, historyVector{
			Name: entry.name, Timestamp: entry.ts, ID: entry.id,
			Path: HistoryPath(entry.ts, entry.id),
		})
	}
	WriteVectors(t, vectorsDir, "history-paths", paths)

	const owner = "alice.example.org"
	records := []HistoryRecord{
		{Version: 1, ID: "m1", Sender: "bob.example.org", Recipient: owner, Timestamp: "2026-01-15T09:00:00Z", Queue: HistoryQueueInbox, Body: "one"},
		{Version: 1, ID: "m2", Sender: "bob.example.org", Recipient: owner, Timestamp: "2026-01-15T09:30:00Z", Queue: HistoryQueueInbox, Body: "two"},
		{Version: 1, ID: "m3", Sender: owner, Recipient: "bob.example.org", Timestamp: "2026-01-15T09:45:00Z", Queue: HistoryQueueSent, Body: "reply"},
		{Version: 1, ID: "m4", Recipient: owner, Timestamp: "2026-01-15T10:00:00Z", Queue: HistoryQueueAnonymous, Body: "tip"},
	}

	states := []readStateVector{}
	for _, entry := range []struct {
		name string
		raw  string
	}{
		{"empty", `{"version":1,"conversations":{}}`},
		{"partly-read", `{"version":1,"conversations":{"bob.example.org":{"timestamp":"2026-01-15T09:00:00Z","id":"m1"}}}`},
		{"fully-read", `{"version":1,"conversations":{"bob.example.org":{"timestamp":"2026-01-15T09:30:00Z","id":"m2"},"anonymous":{"timestamp":"2026-01-15T10:00:00Z","id":"m4"}}}`},
		{"same-second", `{"version":1,"conversations":{"bob.example.org":{"timestamp":"2026-01-15T09:00:00Z"}}}`},
		{"bad-timestamp", `{"version":1,"conversations":{"bob.example.org":{"timestamp":"yesterday"}}}`},
		{"bad-version", `{"version":7,"conversations":{}}`},
	} {
		state, err := ParseReadState([]byte(entry.raw))
		v := readStateVector{Name: entry.name, Raw: json.RawMessage(entry.raw), Valid: err == nil}
		if err == nil {
			v.Unread = state.Unread(owner, records)
			// Reading everything must reach zero from any starting point.
			marked := state.MarkRead("BOB.example.org", "2026-01-15T09:30:00Z", "m2")
			v.Marks = marked.Conversations
		}
		states = append(states, v)
	}
	WriteVectors(t, vectorsDir, "history-read-state", states)
}

// abuseReportVector and blocklistVector pin the two documents EPIC-007 E07-T5
// added (`packages/identity/abuse.go`, `blocks.go`). Neither has a TypeScript
// implementation yet — these exist so that when one is written it has a target
// to conform to, and so that a change to either canonical string here is a
// visible diff rather than a silent fork.
//
// Both canonical forms sort and normalise: message IDs and block entries are
// sorted, identities lowercased and trimmed. The fixtures below are
// deliberately supplied unsorted and mixed-case, so an implementation that
// skips that step fails on them.
type abuseReportVector struct {
	Name      string      `json:"name"`
	Report    AbuseReport `json:"report"`
	Canonical string      `json:"canonical"`
}

type blocklistVector struct {
	Name      string    `json:"name"`
	Blocklist Blocklist `json:"blocklist"`
	Canonical string    `json:"canonical"`
}

func TestVectors_AbuseAndBlocks(t *testing.T) {
	priv := vectorKey()
	pub := priv.Public().(ed25519.PublicKey)

	reports := []abuseReportVector{}
	for _, entry := range []struct {
		name   string
		report AbuseReport
	}{
		{"spam-with-evidence", AbuseReport{
			Version: 1, Type: MsgTypeAbuseReport,
			Reporter: "alice.poweur.net", Subject: "loud.cheapco.test",
			Reason: AbuseReasonSpam, MessageIDs: []string{"m-9", "m-1", " m-4 "},
			Note: "twelve identical messages overnight", CreatedAt: VectorTime,
		}},
		{"bare", AbuseReport{
			Version: 1, Type: MsgTypeAbuseReport,
			Reporter: "Alice.Poweur.NET", Subject: "Loud.CheapCo.Test",
			Reason: AbuseReasonHarassment, CreatedAt: VectorTime,
		}},
	} {
		report := entry.report
		if err := report.Sign(priv); err != nil {
			t.Fatalf("sign %s: %v", entry.name, err)
		}
		if err := report.VerifySignature(pub); err != nil {
			t.Fatalf("verify %s: %v", entry.name, err)
		}
		reports = append(reports, abuseReportVector{entry.name, report, report.Canonical()})
	}
	WriteVectors(t, vectorsDir, "abuse-reports", reports)

	lists := []blocklistVector{}
	for _, entry := range []struct {
		name string
		list Blocklist
	}{
		{"two-entries", Blocklist{
			Version: 1, Publisher: "Alice.Poweur.NET", Name: "alice's list",
			Entries: []BlockEntry{
				{Identity: "Zoe.example.org", Reason: AbuseReasonPhishing, AddedAt: VectorTime},
				{Identity: " spamco.example.test ", Reason: AbuseReasonSpam},
			},
			UpdatedAt: VectorTime,
		}},
		{"empty", Blocklist{
			Version: 1, Publisher: "alice.poweur.net", Entries: []BlockEntry{}, UpdatedAt: VectorTime,
		}},
	} {
		list := entry.list
		if err := list.Sign(priv); err != nil {
			t.Fatalf("sign %s: %v", entry.name, err)
		}
		if err := list.VerifySignature(pub); err != nil {
			t.Fatalf("verify %s: %v", entry.name, err)
		}
		lists = append(lists, blocklistVector{entry.name, list, list.Canonical()})
	}
	WriteVectors(t, vectorsDir, "blocklists", lists)
}

// --- Sign in with Poweur ID (EPIC-008 E08-T1) --------------------------------

// signInVector pins one case of the sign-in protocol. `valid` is what a
// verifier standing at `origin` at VectorTime must decide; `reason` names the
// rule a rejected case exercises so a conforming implementation can assert it
// failed for the right reason rather than by accident.
type signInVector struct {
	Name      string         `json:"name"`
	Origin    string         `json:"origin"`
	Response  SignInResponse `json:"response"`
	Canonical string         `json:"canonical"`
	Valid     bool           `json:"valid"`
	Reason    string         `json:"reason,omitempty"`
	// ReplayOf names an earlier vector whose nonce this one re-presents.
	ReplayOf string `json:"replay_of,omitempty"`
}

func TestVectors_SignIn(t *testing.T) {
	priv := vectorKey()
	identityKey := FormatEd25519PublicKey(priv.Public().(ed25519.PublicKey))
	// A distinct session key, so the delegated vector cannot pass by
	// accidentally being the identity key.
	sessionSeed := make([]byte, 32)
	for i := range sessionSeed {
		sessionSeed[i] = byte(200 - i)
	}
	sessionPriv := ed25519.NewKeyFromSeed(sessionSeed)
	sessionPubRaw := base64.RawURLEncoding.EncodeToString(sessionPriv.Public().(ed25519.PublicKey))

	const (
		origin   = "https://guestbook.poweur.net"
		who      = "alice.poweur.net"
		appScope = "dav:rw:apps/net.poweur.guestbook"
	)

	sign := func(resp SignInResponse, key ed25519.PrivateKey) SignInResponse {
		resp.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(resp.Canonical())))
		return resp
	}

	base := func() SignInResponse {
		return SignInResponse{
			PoweurAuth: SignInVersion,
			RequestID:  "req_vector_001",
			Identity:   who,
			Audience:   origin,
			Nonce:      "bm9uY2UtdmVjdG9yLTAwMQ",
			IssuedAt:   "2026-01-15T09:29:00Z",
			ExpiresAt:  "2026-01-15T09:31:00Z",
			Action:     SignInActionSignin,
			Statement:  "Sign in to the Poweur Guestbook",
			Scopes:     []string{appScope},
			KeyID:      SignInKeyIDIdentity,
		}
	}

	// The delegation proof the relay would also accept on a forwarded message.
	proofIssued, proofExpires := "2026-01-15T08:00:00Z", "2026-01-15T20:00:00Z"
	proofNonce := "c2Vzc2lvbi1ub25jZQ"
	proof := SignInSessionProof{
		SessionPublicKey: sessionPubRaw,
		IssuedAt:         proofIssued,
		ExpiresAt:        proofExpires,
		Nonce:            proofNonce,
		IdentitySignature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv,
			[]byte(CanonicalSessionRegistration(who, sessionPubRaw, proofIssued, proofExpires, proofNonce)))),
	}

	delegated := base()
	delegated.RequestID = "req_vector_session"
	delegated.Nonce = "bm9uY2UtdmVjdG9yLXNlc3M"
	delegated.KeyID = SignInKeyIDSessionPrefix + "sess_vector"
	delegated = sign(delegated, sessionPriv)
	delegated.SessionProof = &proof

	noScopes := base()
	noScopes.RequestID = "req_vector_login_only"
	noScopes.Nonce = "bm9uY2UtdmVjdG9yLWxvZ2lu"
	noScopes.Scopes = nil
	noScopes.Statement = ""

	expired := base()
	expired.RequestID = "req_vector_expired"
	expired.Nonce = "bm9uY2UtdmVjdG9yLWV4cA"
	expired.IssuedAt = "2026-01-15T09:20:00Z"
	expired.ExpiresAt = "2026-01-15T09:22:00Z"

	longWindow := base()
	longWindow.RequestID = "req_vector_ttl"
	longWindow.Nonce = "bm9uY2UtdmVjdG9yLXR0bA"
	longWindow.ExpiresAt = "2026-01-15T09:40:00Z" // 11 minutes

	elsewhere := base()
	elsewhere.RequestID = "req_vector_audience"
	elsewhere.Nonce = "bm9uY2UtdmVjdG9yLWF1ZA"
	elsewhere.Audience = "https://evil.example"

	outOfNamespace := base()
	outOfNamespace.RequestID = "req_vector_scope"
	outOfNamespace.Nonce = "bm9uY2UtdmVjdG9yLXNjb3Bl"
	outOfNamespace.Scopes = []string{"dav:rw:apps/net.poweur.mail"}

	unsorted := base()
	unsorted.RequestID = "req_vector_unsorted"
	unsorted.Nonce = "bm9uY2UtdmVjdG9yLXVucw"
	unsorted.Scopes = []string{"profile:read", appScope} // sorted order puts dav: first

	// Signed over one statement, delivered with another: the signature must
	// fail, which is the whole point of putting the statement in the string.
	tampered := sign(base(), priv)
	tampered.RequestID = "req_vector_tampered"
	tampered.Nonce = "bm9uY2UtdmVjdG9yLXRhbXA"
	tampered.Statement = "Sign in and transfer everything"

	replay := sign(base(), priv) // byte-identical to valid-identity

	vectors := []signInVector{
		{Name: "valid-identity", Origin: origin, Response: sign(base(), priv), Valid: true},
		{Name: "valid-session-delegated", Origin: origin, Response: delegated, Valid: true},
		{Name: "valid-no-scopes", Origin: origin, Response: sign(noScopes, priv), Valid: true},
		{Name: "expired", Origin: origin, Response: sign(expired, priv), Valid: false, Reason: "expired"},
		{Name: "ttl-exceeded", Origin: origin, Response: sign(longWindow, priv), Valid: false, Reason: "ttl"},
		{Name: "wrong-audience", Origin: origin, Response: sign(elsewhere, priv), Valid: false, Reason: "audience"},
		{Name: "scope-out-of-namespace", Origin: origin, Response: sign(outOfNamespace, priv), Valid: false, Reason: "scope"},
		{Name: "scopes-unsorted", Origin: origin, Response: sign(unsorted, priv), Valid: false, Reason: "scope"},
		{Name: "tampered-statement", Origin: origin, Response: tampered, Valid: false, Reason: "signature"},
		{Name: "replayed-nonce", Origin: origin, Response: replay, Valid: false, Reason: "replay", ReplayOf: "valid-identity"},
	}
	for i := range vectors {
		vectors[i].Canonical = vectors[i].Response.Canonical()
	}

	// Self-check: the signatures we emit must actually verify (or not) with
	// the key the vector names, so a broken generator cannot ship a fixture
	// that pins the wrong bytes.
	pub, err := ParseEd25519PublicKey(identityKey)
	if err != nil {
		t.Fatalf("parse identity key: %v", err)
	}
	for _, v := range vectors {
		key := pub
		if v.Name == "valid-session-delegated" {
			key = sessionPriv.Public().(ed25519.PublicKey)
		}
		sig, err := DecodeAnyBase64(v.Response.Signature)
		if err != nil {
			t.Fatalf("%s: signature not base64: %v", v.Name, err)
		}
		ok := ed25519.Verify(key, []byte(v.Canonical), sig)
		if v.Name == "tampered-statement" {
			if ok {
				t.Fatalf("%s: expected the signature to fail over the edited statement", v.Name)
			}
			continue
		}
		if !ok {
			t.Fatalf("%s: signature does not verify over its own canonical string", v.Name)
		}
	}

	WriteVectors(t, vectorsDir, "signin", struct {
		IdentityKey  string         `json:"identity_key"`
		SessionKey   string         `json:"session_key"`
		SessionProof string         `json:"session_registration_canonical"`
		Now          string         `json:"now"`
		Vectors      []signInVector `json:"vectors"`
	}{
		IdentityKey:  identityKey,
		SessionKey:   "ed25519:" + sessionPubRaw,
		SessionProof: CanonicalSessionRegistration(who, sessionPubRaw, proofIssued, proofExpires, proofNonce),
		Now:          VectorTime,
		Vectors:      vectors,
	})
}
