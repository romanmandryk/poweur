package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
	"rsc.io/qr"
)

// Device pairing from the terminal (EPIC-011 E11-T8, v2).
//
// `key enroll` runs on the machine that wants the identity; `key approve` on
// one that has it. The relay is not trusted: the new device commits to its
// ephemeral key before the approver contributes a nonce, and reveals it only
// after, so a relay cannot substitute a key it chose (see
// packages/identity/pairing.go). Two ways to carry the code:
//
//   - the pairing link (what the QR holds): `key approve '<link>'` checks the
//     key against the commitment inside it — nothing to compare;
//   - the typed code: both sides then show six digits, and `key approve`
//     delivers only once they have been confirmed (`--sas` or typed at the
//     prompt). There is no way to approve without one or the other.
//
// Each side keeps its in-flight pairing in ~/.poweur/pairing, so the steps
// also work as separate commands (scripts, CI).

const enrollPollEvery = 2 * time.Second

// newDevicePairing is the new device's state between commands. The
// ephemeral private key and claim token never leave this machine.
type newDevicePairing struct {
	Identity    string `json:"identity"`
	Relay       string `json:"relay"`
	Code        string `json:"code"`
	Link        string `json:"link"`
	Token       string `json:"claim_token"`
	PrivateKey  string `json:"ephemeral_private_key"`
	PublicKey   string `json:"ephemeral_public_key"`
	CommitNonce string `json:"commit_nonce"`
	Commitment  string `json:"commitment"`
	ExpiresAt   string `json:"expires_at"`
	Revealed    bool   `json:"revealed,omitempty"`
}

// approverPairing is the approving side's state: its nonce must stay the same
// across calls, or the digits would change under the user.
type approverPairing struct {
	Identity      string `json:"identity"`
	Code          string `json:"code"`
	ApproverNonce string `json:"approver_nonce"`
	Commitment    string `json:"commitment,omitempty"` // from a scanned link
}

func pairingDir() (string, error) {
	path, err := config.ConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "pairing"), nil
}

func pairingFile(kind, identityValue, code string) (string, error) {
	dir, err := pairingDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, kind+"-"+strings.ToLower(identityValue)+"-"+code+".json"), nil
}

func savePairing(kind, identityValue, code string, v any) error {
	path, err := pairingFile(kind, identityValue, code)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func loadPairing(kind, identityValue, code string, v any) error {
	path, err := pairingFile(kind, identityValue, code)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func dropPairing(kind, identityValue, code string) {
	if path, err := pairingFile(kind, identityValue, code); err == nil {
		_ = os.Remove(path)
	}
}

func randB64(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func enrollHTTP(ctx context.Context, method, url, token string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, parseErrorResponse("pairing request failed", resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func enrollBase(relayURL, identityValue string) string {
	return strings.TrimRight(relayURL, "/") + "/identities/" + url.PathEscape(identityValue) + "/enroll"
}

// pairingAppURL is where the approving person's web app lives: the identity's
// own host for a relay on https, the relay itself in local development.
func pairingAppURL(relayURL, identityValue string) string {
	if strings.HasPrefix(relayURL, "https://") {
		return "https://" + strings.ToLower(identityValue) + "/app/"
	}
	return strings.TrimRight(relayURL, "/") + "/app/"
}

// terminalQR draws the link with half-block characters: two rows of modules
// per line, dark on the terminal's light.
func terminalQR(w io.Writer, text string) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return
	}
	const quiet = 2
	size := code.Size + 2*quiet
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	for y := 0; y < size; y += 2 {
		var line strings.Builder
		for x := 0; x < size; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				line.WriteString(" ")
			case top:
				line.WriteString("\u2584")
			case bottom:
				line.WriteString("\u2580")
			default:
				line.WriteString("\u2588")
			}
		}
		fmt.Fprintln(w, line.String())
	}
}

// runKeyEnroll opens a pairing on the machine that wants the identity.
func runKeyEnroll(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	relayURL := fs.String("relay", cfg.RelayURL, "relay base url")
	label := fs.String("label", "", "device description shown to the approver")
	wait := fs.Bool("wait", false, "keep going until the other device approves")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--wait": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: poweur key enroll <identity> [--relay ...] [--wait]")
		return 1
	}
	identityValue := strings.ToLower(fs.Arg(0))
	if *relayURL == "" {
		fmt.Fprintln(stderr, "--relay is required (this machine has no configuration yet)")
		return 1
	}
	ephPub, ephPriv, err := cryptoe2e.GenerateX25519Keypair()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	p := newDevicePairing{
		Identity:   identityValue,
		Relay:      strings.TrimRight(*relayURL, "/"),
		PublicKey:  base64.RawURLEncoding.EncodeToString(ephPub),
		PrivateKey: base64.RawURLEncoding.EncodeToString(ephPriv),
	}
	if p.CommitNonce, err = randB64(32); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	p.Commitment = idpkg.PairingCommitment(p.PublicKey, p.CommitNonce)

	var offer struct {
		RendezvousID string `json:"rendezvous_id"`
		ClaimToken   string `json:"claim_token"`
		ExpiresAt    string `json:"expires_at"`
	}
	if _, err := enrollHTTP(context.Background(), http.MethodPost, enrollBase(p.Relay, identityValue)+"/offer", "",
		map[string]any{"commitment": p.Commitment, "label": *label}, &offer); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	p.Code, p.Token, p.ExpiresAt = offer.RendezvousID, offer.ClaimToken, offer.ExpiresAt
	p.Link = idpkg.PairingLink(pairingAppURL(p.Relay, identityValue), identityValue, p.Code, p.Commitment)
	if err := savePairing("new", identityValue, p.Code, p); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	appLink := idpkg.PairingAppLink(identityValue, p.Code, p.Commitment)
	if !*jsonOut {
		fmt.Fprintf(stdout, "Approve on a device that already has %s.\n\nPoweur app — scan with the phone's camera:\n\n", identityValue)
		terminalQR(stdout, appLink)
		fmt.Fprintf(stdout, "\nBrowser — open:    %s\n", p.Link)
		fmt.Fprintf(stdout, "Terminal — run:    poweur key approve '%s' --seed …\n", p.Link)
		fmt.Fprintf(stdout, "Or enter the code %s there (Settings → Keys & devices → Add a device).\n\n",
			idpkg.FormatShortCode(p.Code))
	}
	if !*wait {
		if !*jsonOut {
			fmt.Fprintf(stdout, "Then finish here:  poweur key claim %s %s\n", identityValue, p.Code)
		}
		return writeOutput(stdout, *jsonOut, map[string]any{
			"identity": identityValue, "code": p.Code, "link": p.Link, "app_link": appLink, "expires_at": p.ExpiresAt,
		}, "")
	}
	deadline := time.Now().Add(10 * time.Minute)
	shown := ""
	for time.Now().Before(deadline) {
		status, _, seed, err := stepNewDevice(&p)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if seed != nil {
			return adoptSeed(cfg, p, seed, stdout, stderr, *jsonOut)
		}
		if status != shown && !*jsonOut {
			fmt.Fprintln(stdout, status)
			shown = status
		}
		time.Sleep(enrollPollEvery)
	}
	fmt.Fprintln(stderr, "timed out waiting for approval")
	return 1
}

// stepNewDevice advances the new device's side once: reveal when the
// approver has answered, collect the seed when it has been delivered.
func stepNewDevice(p *newDevicePairing) (status, sas string, seed []byte, err error) {
	base := enrollBase(p.Relay, p.Identity) + "/" + p.Code
	var poll struct {
		State         string `json:"state"`
		ApproverNonce string `json:"approver_nonce"`
		Mode          string `json:"mode"`
		Sealed        string `json:"sealed"`
	}
	if _, err := enrollHTTP(context.Background(), http.MethodGet, base, p.Token, nil, &poll); err != nil {
		return "", "", nil, err
	}
	switch poll.State {
	case "offered":
		return "Waiting for the other device…", "", nil, nil
	case "delivered":
		priv, err := base64.RawURLEncoding.DecodeString(p.PrivateKey)
		if err != nil {
			return "", "", nil, err
		}
		seed, err := openSealedSeed(poll.Sealed, priv)
		return "", "", seed, err
	}
	// The approver has contributed its nonce: only now reveal the key.
	if !p.Revealed {
		if _, err := enrollHTTP(context.Background(), http.MethodPost, base+"/reveal", p.Token,
			map[string]string{"ephemeral_public_key": p.PublicKey, "commit_nonce": p.CommitNonce}, nil); err != nil {
			return "", "", nil, err
		}
		p.Revealed = true
		_ = savePairing("new", p.Identity, p.Code, p)
	}
	if poll.Mode == "scan" {
		return "Approve on the other device.", "", nil, nil
	}
	sas = idpkg.PairingSAS(p.Commitment, p.PublicKey, p.CommitNonce, poll.ApproverNonce)
	return fmt.Sprintf("Check the other device shows %s %s, then approve there.", sas[:3], sas[3:]), sas, nil, nil
}

// adoptSeed saves the keys a delivered seed derives — only if they are the
// identity's published keys, so a seed that is not this identity's is never
// adopted.
func adoptSeed(cfg config.Config, p newDevicePairing, seed []byte, stdout, stderr io.Writer, jsonOut bool) int {
	pub, _, err := identity.KeypairFromSeed(seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var published struct {
		PublicKey string `json:"public_key"`
	}
	if _, err := enrollHTTP(context.Background(), http.MethodGet,
		strings.TrimRight(p.Relay, "/")+"/identities/"+url.PathEscape(p.Identity), "", nil, &published); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if idpkg.NormalizePublicKeyKey(published.PublicKey) != idpkg.NormalizePublicKeyKey(identity.PublicKeyString(pub)) {
		fmt.Fprintf(stderr, "the keys received are not %s's published keys — not saved\n", p.Identity)
		return 1
	}
	keyPath, encKeyPath, err := identity.SaveKeysFromSeed(p.Identity, seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg.Identity = p.Identity
	cfg.RelayURL = p.Relay
	cfg.KeysDir = filepath.Dir(keyPath)
	if err := config.Save(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dropPairing("new", p.Identity, p.Code)
	return writeOutput(stdout, jsonOut, map[string]any{
		"identity": p.Identity, "key_path": keyPath, "encryption_key_path": encKeyPath, "enrolled": true,
	}, fmt.Sprintf("enrolled %s on this device\n", p.Identity))
}

// runKeyClaim moves a pairing started by `key enroll` (without --wait) one
// step: reveal once the other device has answered, then collect the keys.
func runKeyClaim(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key claim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: poweur key claim <identity> <code>")
		return 1
	}
	identityValue := strings.ToLower(fs.Arg(0))
	code, err := idpkg.NormalizeShortCode(fs.Arg(1))
	if err != nil {
		fmt.Fprintln(stderr, "that is not a pairing code")
		return 1
	}
	var p newDevicePairing
	if err := loadPairing("new", identityValue, code, &p); err != nil {
		fmt.Fprintf(stderr, "no pairing %s for %s on this machine — start one with `poweur key enroll`\n", code, identityValue)
		return 1
	}
	status, sas, seed, err := stepNewDevice(&p)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if seed != nil {
		return adoptSeed(cfg, p, seed, stdout, stderr, *jsonOut)
	}
	out := map[string]any{"identity": identityValue, "code": code, "enrolled": false, "status": status}
	if sas != "" {
		out["sas"] = sas
	}
	return writeOutput(stdout, *jsonOut, out, status+"\n")
}

func openSealedSeed(sealed string, ephPriv []byte) ([]byte, error) {
	var payload struct {
		Ciphertext         string `json:"ciphertext"`
		EphemeralPublicKey string `json:"ephemeralPublicKey"`
		Nonce              string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(sealed), &payload); err != nil {
		return nil, fmt.Errorf("sealed payload is not valid JSON: %w", err)
	}
	seed, err := cryptoe2e.Decrypt(ephPriv, cryptoe2e.EncryptedPayload{
		Ciphertext:         payload.Ciphertext,
		EphemeralPublicKey: payload.EphemeralPublicKey,
		Nonce:              payload.Nonce,
	})
	if err != nil {
		return nil, fmt.Errorf("could not open the sealed seed: %w", err)
	}
	if len(seed) != identity.SeedLen {
		return nil, fmt.Errorf("expected a %d-byte seed, got %d", identity.SeedLen, len(seed))
	}
	return seed, nil
}

var errPairingMismatch = errors.New("pairing mismatch")

// runKeyApprove approves a waiting device and seals the seed to it.
func runKeyApprove(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key approve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to enroll the device into")
	relayURL := fs.String("relay", cfg.RelayURL, "relay base url")
	seedFlag := fs.String("seed", "", "master seed (base64url or mnemonic)")
	expectSAS := fs.String("sas", "", "the six digits the new device shows (typed code only)")
	noWait := fs.Bool("no-wait", false, "return at once if the new device has not answered yet")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--no-wait": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: poweur key approve <pairing-link | code> [--sas 123456]")
		return 1
	}
	identityValue := strings.ToLower(resolveIdentity(*useIdentity, cfg.Identity))
	if identityValue == "" || *relayURL == "" {
		fmt.Fprintln(stderr, "identity and relay url required")
		return 1
	}
	if *seedFlag == "" {
		fmt.Fprintln(stderr, "--seed is required: the seed lives only on your devices, never on the relay")
		return 1
	}
	seed, err := identity.ParseSeed(*seedFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, priv, err := identity.KeypairFromSeed(seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// A link (scanned or pasted) carries the commitment; a code does not.
	st := approverPairing{Identity: identityValue}
	mode := "compare"
	if link, perr := idpkg.ParsePairingLink(fs.Arg(0)); perr == nil {
		if link.Identity != "" && link.Identity != identityValue {
			fmt.Fprintf(stderr, "this pairing link is for %s, not %s — not approved\n", link.Identity, identityValue)
			return 1
		}
		st.Code, st.Commitment, mode = link.Code, link.Commitment, "scan"
	} else if st.Code, err = idpkg.NormalizeShortCode(fs.Arg(0)); err != nil {
		fmt.Fprintln(stderr, "that is neither a pairing link nor a code (8 characters, like K7QM-4XP2)")
		return 1
	}
	var saved approverPairing
	if loadPairing("approve", identityValue, st.Code, &saved) == nil && saved.ApproverNonce != "" {
		st.ApproverNonce = saved.ApproverNonce
		if st.Commitment == "" {
			st.Commitment = saved.Commitment
		}
	} else if st.ApproverNonce, err = randB64(32); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := savePairing("approve", identityValue, st.Code, st); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	base := enrollBase(*relayURL, identityValue) + "/" + st.Code
	sign := func(action string) map[string]any {
		issuedAt := time.Now().UTC().Format(time.RFC3339)
		nonce := newAdminNonce()
		canonical := strings.Join([]string{action, identityValue, st.Code, issuedAt, nonce}, "\n")
		return map[string]any{
			"issued_at": issuedAt, "nonce": nonce,
			"identity_signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canonical))),
		}
	}
	type pendingPairing struct {
		State              string `json:"state"`
		Commitment         string `json:"commitment"`
		Label              string `json:"label"`
		EphemeralPublicKey string `json:"ephemeral_public_key"`
		CommitNonce        string `json:"commit_nonce"`
	}
	fetch := func() (pendingPairing, error) {
		body := sign("enroll-fetch")
		body["approver_nonce"], body["mode"] = st.ApproverNonce, mode
		var out pendingPairing
		_, err := enrollHTTP(context.Background(), http.MethodPost, base+"/fetch", "", body, &out)
		return out, err
	}
	pending, err := fetch()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// A scanned commitment is the one the new device showed; the relay's copy
	// must be the same, or this is not that device.
	if st.Commitment != "" && pending.Commitment != st.Commitment {
		fmt.Fprintln(stderr, "this code belongs to a different device than the one you scanned — not approved")
		return 1
	}
	st.Commitment = pending.Commitment
	_ = savePairing("approve", identityValue, st.Code, st)

	deadline := time.Now().Add(10 * time.Minute)
	for pending.State == "nonce" {
		if *noWait || time.Now().After(deadline) {
			return writeOutput(stdout, *jsonOut, map[string]any{
				"identity": identityValue, "code": st.Code, "approved": false, "status": "waiting for the new device",
			}, "Waiting for the new device — run this again once it shows its digits.\n")
		}
		time.Sleep(enrollPollEvery)
		if pending, err = fetch(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if pending.State != "revealed" {
		fmt.Fprintln(stderr, "this request is no longer waiting for approval")
		return 1
	}
	// The relay's word is not taken for any of this.
	if err := idpkg.VerifyPairingReveal(st.Commitment, pending.EphemeralPublicKey, pending.CommitNonce); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sas := idpkg.PairingSAS(st.Commitment, pending.EphemeralPublicKey, pending.CommitNonce, st.ApproverNonce)
	if mode == "compare" {
		if err := confirmSAS(sas, *expectSAS, pending.Label, stdout, stderr); err != nil {
			if errors.Is(err, errPairingMismatch) {
				dropPairing("approve", identityValue, st.Code)
			}
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	ephemeral, err := base64.RawURLEncoding.DecodeString(pending.EphemeralPublicKey)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sealed, err := cryptoe2e.Encrypt(ephemeral, seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sealedJSON, err := json.Marshal(map[string]string{
		"ciphertext": sealed.Ciphertext, "ephemeralPublicKey": sealed.EphemeralPublicKey, "nonce": sealed.Nonce,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	body := sign("enroll-deliver")
	body["sealed"] = string(sealedJSON)
	if _, err := enrollHTTP(context.Background(), http.MethodPost, base+"/deliver", "", body, nil); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dropPairing("approve", identityValue, st.Code)
	return writeOutput(stdout, *jsonOut, map[string]any{
		"identity": identityValue, "code": st.Code, "mode": mode, "approved": true,
	}, fmt.Sprintf("approved the new device for %s\n", identityValue))
}

// confirmSAS is the typed path's authentication step: the digits given with
// --sas, or typed at the prompt, must be the ones this side computed. With
// neither, nothing is delivered.
func confirmSAS(sas, expect, label string, stdout, stderr io.Writer) error {
	given := strings.Join(strings.Fields(expect), "")
	if given == "" {
		if !stdinIsTerminal() {
			return fmt.Errorf("the new device shows six digits; run again with --sas <digits> to approve " +
				"(this side computed them independently, and delivers only if they match)")
		}
		who := "the new device"
		if label != "" {
			who = label
		}
		fmt.Fprintf(stdout, "Type the six digits %s shows: ", who)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		given = strings.Join(strings.Fields(line), "")
	}
	if given != sas {
		return fmt.Errorf("%w: the new device's digits do not match this one's (%s %s) — not approved. "+
			"Another device may be trying to pair; start again", errPairingMismatch, sas[:3], sas[3:])
	}
	return nil
}

// stdinIsTerminal reports whether someone can answer a prompt. /dev/null is
// a character device too, so it is ruled out by identity.
var stdinIsTerminal = func() bool {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(info, null)
}
