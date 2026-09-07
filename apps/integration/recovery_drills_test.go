package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clipkg "github.com/poweur/cli/pkg/cli"
	idpkg "github.com/poweur/identity"
)

// Recovery drills (EPIC-011 E11-T7).
//
// These are the product's most load-bearing promises, so they are permanent
// fixtures rather than one-off checks: every scenario runs the real CLI
// against a real relay with real storage.
//
//	lost-one-device            → TestDrill_LostOneDevice
//	lost-all-devices-have-kit  → TestDrill_LostAllDevicesHaveKit
//	stolen-device-kill         → TestDrill_StolenDeviceKill
//	compromised-seed-rotation  → TestDrill_CompromisedSeedRotation
//	protected-keys-at-rest     → TestDrill_ProtectedKeysAtRest
//
// The fifth scenario in the epic, lost-everything-social, is gated on E11-T5
// (social recovery) and has no fixture yet — deliberately absent rather than
// silently passing.

// ── helpers ──────────────────────────────────────────────────────────────────

type drillRelay struct {
	url  string
	addr string
}

func newDrillRelay(t *testing.T, hosts ...string) *drillRelay {
	t.Helper()
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	t.Cleanup(ts.Close)
	for _, h := range hosts {
		zone.SetHost(h, addr)
	}
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })
	return &drillRelay{url: "http://" + addr, addr: addr}
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func postJSON(t *testing.T, url string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func deleteJSON(t *testing.T, url string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodDelete, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// drillAuthenticator is a stand-in security key: Ed25519 (COSE -8), which the
// web client already requests and whose signatures are raw, not DER.
type drillAuthenticator struct {
	credID string
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	spki   string
	rpID   string
}

func newDrillAuthenticator(t *testing.T, name, rpID string) *drillAuthenticator {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := marshalEd25519SPKI(pub)
	if err != nil {
		t.Fatal(err)
	}
	return &drillAuthenticator{
		credID: b64u([]byte("cred-" + name)), pub: pub, priv: priv,
		spki: b64u(spki), rpID: rpID,
	}
}

func (a *drillAuthenticator) assert(t *testing.T, challenge string) map[string]any {
	t.Helper()
	clientData, err := json.Marshal(map[string]any{
		"type":      "webauthn.get",
		"challenge": b64u([]byte(challenge)),
		"origin":    "https://" + a.rpID,
	})
	if err != nil {
		t.Fatal(err)
	}
	rpHash := sha256.Sum256([]byte(a.rpID))
	authData := append(append([]byte{}, rpHash[:]...), 0x05, 0, 0, 0, 1) // UP|UV
	clientHash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authData...), clientHash[:]...)
	return map[string]any{
		"credential_id":      a.credID,
		"client_data_json":   b64u(clientData),
		"authenticator_data": b64u(authData),
		"signature":          b64u(ed25519.Sign(a.priv, signed)),
	}
}

// enroll registers a wrapped copy for an identity whose seed we hold.
func enrollDevice(t *testing.T, relay *drillRelay, identity string, seed []byte,
	enrollmentID, role string, auth *drillAuthenticator) {
	t.Helper()
	_, priv, err := idpkg.DeriveSigningKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := json.RawMessage(`{"iv":"aXY","ciphertext":"Y3Q"}`)
	digest := sha256.Sum256(wrapped)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "n-" + enrollmentID
	canonical := strings.Join([]string{
		"keystore-enroll", strings.ToLower(identity), enrollmentID, "hardware-key",
		auth.credID, b64u(digest[:]), issuedAt, nonce,
	}, "\n")
	body := map[string]any{
		"enrollment_id": enrollmentID, "kind": "hardware-key", "wrap": "prf",
		"payload": "seed", "credential_id": auth.credID,
		"credential_public_key": auth.spki, "credential_alg": -8,
		"wrapped": wrapped, "role": role, "label": enrollmentID,
		"issued_at": issuedAt, "nonce": nonce,
		"identity_signature": b64u(ed25519.Sign(priv, []byte(canonical))),
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPut,
		relay.url+"/identities/"+identity+"/keystore", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("enroll %s: %d %s", enrollmentID, resp.StatusCode, out)
	}
}

func drillChallenge(t *testing.T, relay *drillRelay, identity string) string {
	t.Helper()
	resp, err := http.Get(relay.url + "/auth/challenge?identity=" + identity)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Challenge string `json:"challenge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Challenge
}

func listEnrollments(t *testing.T, relay *drillRelay, identity string, seed []byte) []map[string]any {
	t.Helper()
	_, priv, err := idpkg.DeriveSigningKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	canonical := strings.Join([]string{
		"keystore-list", strings.ToLower(identity), issuedAt, "n-ls"}, "\n")
	code, body := postJSON(t, relay.url+"/identities/"+identity+"/keystore/list", map[string]any{
		"issued_at": issuedAt, "nonce": "n-ls",
		"identity_signature": b64u(ed25519.Sign(priv, []byte(canonical))),
	})
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	var out struct {
		Enrollments []map[string]any `json:"enrollments"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Enrollments
}

// ── drills ───────────────────────────────────────────────────────────────────

// Drill: lost one device. Removing it must not disturb the others, and the
// identity keeps working throughout.
func TestDrill_LostOneDevice(t *testing.T) {
	relay := newDrillRelay(t, "lost1.poweur.net", "peer1.poweur.net")
	home, peerHome := t.TempDir(), t.TempDir()

	seedStr := createSeedIdentity(t, home, "lost1.poweur.net", relay.url)
	seed, err := idpkg.ParseSeed(seedStr)
	if err != nil {
		t.Fatal(err)
	}
	laptop := newDrillAuthenticator(t, "laptop", "poweur.net")
	phone := newDrillAuthenticator(t, "phone", "poweur.net")
	enrollDevice(t, relay, "lost1.poweur.net", seed, "laptop", "device", laptop)
	enrollDevice(t, relay, "lost1.poweur.net", seed, "phone", "device", phone)

	if got := len(listEnrollments(t, relay, "lost1.poweur.net", seed)); got != 2 {
		t.Fatalf("want 2 enrollments, got %d", got)
	}

	// Remove the lost phone. No recovery-master exists, so the identity
	// signature alone authorizes it.
	_, priv, _ := idpkg.DeriveSigningKey(seed)
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	canonical := strings.Join([]string{
		"keystore-remove", "lost1.poweur.net", "phone", issuedAt, "n-rm"}, "\n")
	code, body := deleteJSON(t, relay.url+"/identities/lost1.poweur.net/keystore/phone",
		map[string]any{
			"issued_at": issuedAt, "nonce": "n-rm",
			"identity_signature": b64u(ed25519.Sign(priv, []byte(canonical))),
		})
	if code != http.StatusNoContent {
		t.Fatalf("remove phone: %d %s", code, body)
	}

	remaining := listEnrollments(t, relay, "lost1.poweur.net", seed)
	if len(remaining) != 1 || remaining[0]["enrollment_id"] != "laptop" {
		t.Fatalf("laptop enrollment should survive: %+v", remaining)
	}

	// The identity still functions.
	runCLI(t, peerHome, "identity", "create", "peer1.poweur.net",
		"--hosted", "--relay", relay.url, "--json")
	runCLI(t, peerHome, "send", "lost1.poweur.net", "still reachable")
	assertDecryptedInbox(t, mustInbox(t, home), "peer1.poweur.net", "still reachable")
}

// Drill: every device is gone, only the paper kit survives.
func TestDrill_LostAllDevicesHaveKit(t *testing.T) {
	relay := newDrillRelay(t, "lost2.poweur.net", "peer2.poweur.net")
	home, peerHome := t.TempDir(), t.TempDir()

	stdout, _ := runCLI(t, home, "identity", "create", "lost2.poweur.net",
		"--hosted", "--from-seed", "--relay", relay.url, "--json")
	var created struct {
		Mnemonic string `json:"mnemonic"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatal(err)
	}

	runCLI(t, peerHome, "identity", "create", "peer2.poweur.net",
		"--hosted", "--relay", relay.url, "--json")
	runCLI(t, peerHome, "send", "lost2.poweur.net", "waiting for you")

	// Every device is gone: a machine that has never seen this identity, armed
	// only with words off a card.
	freshHome := t.TempDir()
	runCLI(t, freshHome, "key", "recover", "lost2.poweur.net",
		"--seed", created.Mnemonic, "--relay", relay.url, "--json")
	assertDecryptedInbox(t, mustInbox(t, freshHome), "peer2.poweur.net", "waiting for you")
}

// Drill: stolen device. The recovery-master evicts it; the thief's device
// cannot evict the recovery-master, even holding the identity key.
func TestDrill_StolenDeviceKill(t *testing.T) {
	relay := newDrillRelay(t, "stolen.poweur.net")
	home := t.TempDir()
	seedStr := createSeedIdentity(t, home, "stolen.poweur.net", relay.url)
	seed, err := idpkg.ParseSeed(seedStr)
	if err != nil {
		t.Fatal(err)
	}
	phone := newDrillAuthenticator(t, "phone", "poweur.net")
	yubikey := newDrillAuthenticator(t, "yubikey", "poweur.net")
	enrollDevice(t, relay, "stolen.poweur.net", seed, "phone", "device", phone)
	enrollDevice(t, relay, "stolen.poweur.net", seed, "yubikey", "recovery-master", yubikey)

	_, priv, _ := idpkg.DeriveSigningKey(seed)
	remove := func(target string, actor *drillAuthenticator) (int, []byte) {
		issuedAt := time.Now().UTC().Format(time.RFC3339)
		nonce := "n-" + target
		canonical := strings.Join([]string{
			"keystore-remove", "stolen.poweur.net", target, issuedAt, nonce}, "\n")
		body := map[string]any{
			"issued_at": issuedAt, "nonce": nonce,
			"identity_signature": b64u(ed25519.Sign(priv, []byte(canonical))),
			"revoke_sessions":    true,
		}
		if actor != nil {
			body["actor_assertion"] = actor.assert(t, drillChallenge(t, relay, "stolen.poweur.net"))
			body["rp_id"] = "poweur.net"
		}
		return deleteJSON(t, relay.url+"/identities/stolen.poweur.net/keystore/"+target, body)
	}

	// The thief has the identity key — that must not be enough.
	if code, body := remove("yubikey", nil); code != http.StatusForbidden {
		t.Fatalf("thief evicted the recovery-master: %d %s", code, body)
	}
	if code, body := remove("yubikey", phone); code != http.StatusForbidden {
		t.Fatalf("a plain device passed itself off as recovery-master: %d %s", code, body)
	}

	// The owner's security key kills the stolen phone.
	if code, body := remove("phone", yubikey); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("recovery-master could not kill the phone: %d %s", code, body)
	}
	remaining := listEnrollments(t, relay, "stolen.poweur.net", seed)
	if len(remaining) != 1 || remaining[0]["enrollment_id"] != "yubikey" {
		t.Fatalf("expected only the yubikey to remain: %+v", remaining)
	}
}

// Drill: the seed is believed compromised. Rotation must publish a new key,
// keep the old one in previous_keys for continuity, and leave messaging working.
func TestDrill_CompromisedSeedRotation(t *testing.T) {
	relay := newDrillRelay(t, "rotated.poweur.net", "peer3.poweur.net")
	home, peerHome := t.TempDir(), t.TempDir()

	createSeedIdentity(t, home, "rotated.poweur.net", relay.url)
	before := fetchDoc(t, relay.url+"/identities/rotated.poweur.net")
	oldKey := idpkg.NormalizePublicKeyKey(before.PublicKey)

	runCLI(t, home, "key", "rotate", "--use-identity", "rotated.poweur.net", "--grace", "1h", "--json")

	after := fetchDoc(t, relay.url+"/identities/rotated.poweur.net")
	if idpkg.NormalizePublicKeyKey(after.PublicKey) == oldKey {
		t.Fatal("rotation did not change the signing key")
	}
	found := false
	for _, pk := range after.PreviousKeys {
		if idpkg.NormalizePublicKeyKey(pk.PublicKey) == oldKey {
			found = true
		}
	}
	if !found {
		t.Fatal("old key missing from previous_keys — contacts would see a key-change warning")
	}

	runCLI(t, peerHome, "identity", "create", "peer3.poweur.net",
		"--hosted", "--relay", relay.url, "--json")
	runCLI(t, peerHome, "send", "rotated.poweur.net", "after rotation")
	assertDecryptedInbox(t, mustInbox(t, home), "peer3.poweur.net", "after rotation")
}

// Drill: keys encrypted at rest. The whole CLI must keep working with only a
// passphrase in the environment — the unattended-agent story.
func TestDrill_ProtectedKeysAtRest(t *testing.T) {
	relay := newDrillRelay(t, "protected.poweur.net", "peer4.poweur.net")
	home, peerHome := t.TempDir(), t.TempDir()

	createSeedIdentity(t, home, "protected.poweur.net", relay.url)
	t.Setenv("POWEUR_KEY_PASSPHRASE", "drill-passphrase")
	runCLI(t, home, "key", "protect", "--use-identity", "protected.poweur.net", "--json")

	// No plaintext key material may remain on disk.
	raw, err := readKeyFileBytes(t, home, "protected.poweur.net.key")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		t.Fatalf("key file is still plaintext: %s", raw)
	}

	runCLI(t, peerHome, "identity", "create", "peer4.poweur.net",
		"--hosted", "--relay", relay.url, "--json")
	runCLI(t, peerHome, "send", "protected.poweur.net", "encrypted at rest")
	assertDecryptedInbox(t, mustInbox(t, home), "peer4.poweur.net", "encrypted at rest")

	// And the passphrase is genuinely required.
	t.Setenv("POWEUR_KEY_PASSPHRASE", "")
	t.Setenv("HOME", home)
	var out, errBuf strings.Builder
	if code := clipkg.Run([]string{"inbox"}, &out, &errBuf); code == 0 {
		t.Fatal("inbox succeeded without the passphrase")
	}
}

// marshalEd25519SPKI encodes an Ed25519 public key as SPKI DER, the form the
// relay expects (WebAuthn's getPublicKey() output).
func marshalEd25519SPKI(pub ed25519.PublicKey) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(pub)
}

// readKeyFileBytes reads a key file straight off disk, bypassing the loader.
func readKeyFileBytes(t *testing.T, home, name string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(filepath.Join(home, ".poweur", "keys", name))
}

// Drill: enrol a new device from the terminal. This is the camera-free path —
// a six-digit code typed by the user, no QR anywhere — and it must move the
// seed with the relay seeing only ciphertext.
func TestDrill_EnrollNewDeviceViaCode(t *testing.T) {
	relay := newDrillRelay(t, "enrolled.poweur.net", "peer5.poweur.net")
	oldDevice, newDevice, peerHome := t.TempDir(), t.TempDir(), t.TempDir()

	seed := createSeedIdentity(t, oldDevice, "enrolled.poweur.net", relay.url)

	// New device: no keys, no config. It opens a rendezvous and shows a code.
	stdout, _ := runCLI(t, newDevice, "key", "enroll", "enrolled.poweur.net",
		"--relay", relay.url, "--label", "laptop", "--json")
	var offer struct {
		RendezvousID        string `json:"rendezvous_id"`
		SAS                 string `json:"sas"`
		EphemeralPrivateKey string `json:"ephemeral_private_key"`
	}
	if err := json.Unmarshal([]byte(stdout), &offer); err != nil {
		t.Fatalf("parse enroll offer: %v\n%s", err, stdout)
	}
	if len(offer.SAS) != 6 || offer.RendezvousID == "" {
		t.Fatalf("expected a 6-digit code and a rendezvous id: %+v", offer)
	}

	// Old device: the user types the id and the code. A wrong code must abort
	// rather than hand over the seed.
	t.Setenv("HOME", oldDevice)
	var out, errBuf strings.Builder
	if code := clipkg.Run([]string{
		"key", "approve", offer.RendezvousID,
		"--use-identity", "enrolled.poweur.net", "--relay", relay.url,
		"--seed", seed, "--sas", "000000", "--json",
	}, &out, &errBuf); code == 0 {
		t.Fatal("approval proceeded despite a mismatched code")
	}
	if !strings.Contains(errBuf.String(), "mismatch") {
		t.Fatalf("expected a clear mismatch warning, got: %s", errBuf.String())
	}

	// With the right code it goes through.
	runCLI(t, oldDevice, "key", "approve", offer.RendezvousID,
		"--use-identity", "enrolled.poweur.net", "--relay", relay.url,
		"--seed", seed, "--sas", offer.SAS, "--json")

	// New device collects the sealed seed — this is the step that proves the
	// ceremony actually moved something. It never sees the seed in any other
	// form: only the ephemeral private key it generated itself.
	runCLI(t, newDevice, "key", "claim", "enrolled.poweur.net", offer.RendezvousID,
		"--ephemeral-key", offer.EphemeralPrivateKey, "--relay", relay.url, "--json")

	// The keys it derived must be the identity's, not merely well-formed.
	derived, _ := runCLI(t, newDevice, "key", "derive", "--seed", seed, "--json")
	var want struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(derived), &want); err != nil {
		t.Fatal(err)
	}
	doc := fetchDoc(t, relay.url+"/identities/enrolled.poweur.net")
	if idpkg.NormalizePublicKeyKey(want.PublicKey) != idpkg.NormalizePublicKeyKey(doc.PublicKey) {
		t.Fatal("enrolled device does not hold the published identity key")
	}

	// A claimed rendezvous is spent: replaying it must fail.
	t.Setenv("HOME", newDevice)
	var replayOut, replayErr strings.Builder
	if code := clipkg.Run([]string{
		"key", "claim", "enrolled.poweur.net", offer.RendezvousID,
		"--ephemeral-key", offer.EphemeralPrivateKey, "--relay", relay.url, "--json",
	}, &replayOut, &replayErr); code == 0 {
		t.Fatal("a spent rendezvous was claimed twice")
	}

	runCLI(t, peerHome, "identity", "create", "peer5.poweur.net",
		"--hosted", "--relay", relay.url, "--json")
	runCLI(t, peerHome, "send", "enrolled.poweur.net", "hello new device")
	assertDecryptedInbox(t, mustInbox(t, newDevice), "peer5.poweur.net", "hello new device")
}
