package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
)

// Device enrollment from the terminal (EPIC-011 E11-T3).
//
// `key enroll` runs on the machine that wants the identity: it generates an
// ephemeral X25519 keypair, opens a rendezvous and prints a six-digit code.
// `key approve` runs on a machine that already holds the seed: the user types
// the rendezvous id, compares the code, and the seed is sealed to the waiting
// device's key.
//
// The code authenticates a public key rather than protecting a secret, so
// nothing here is brute-forceable offline — see apps/api/internal/relay/enroll.go
// for why that removes the need for a PAKE.

const enrollSASDigits = 6

// computeEnrollSAS mirrors relay.ComputeSAS. Both ends derive it from the
// ephemeral key so neither trusts the relay's copy.
func computeEnrollSAS(ephemeralPublicKey string) string {
	sum := sha256.Sum256([]byte("poweur/v1/enroll-sas\n" + ephemeralPublicKey))
	return fmt.Sprintf("%0*d", enrollSASDigits, binary.BigEndian.Uint32(sum[:4])%1000000)
}

func enrollHTTP(ctx context.Context, method, url string, body any, out any) (int, error) {
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
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, parseErrorResponse("enrollment request failed", resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// runKeyEnroll asks an existing device for this identity's seed.
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
	wait := fs.Bool("wait", false, "poll until the other device approves")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--wait": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: poweur key enroll <identity> [--relay ...] [--wait]")
		return 1
	}
	identityValue := fs.Arg(0)
	if *relayURL == "" {
		fmt.Fprintln(stderr, "--relay is required (this machine has no configuration yet)")
		return 1
	}
	base := strings.TrimRight(*relayURL, "/") + "/identities/" + identityValue + "/enroll"

	ephPub, ephPriv, err := cryptoe2e.GenerateX25519Keypair()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ephPubB64 := base64.RawURLEncoding.EncodeToString(ephPub)

	var offer struct {
		RendezvousID string `json:"rendezvous_id"`
		SAS          string `json:"sas"`
		ExpiresAt    string `json:"expires_at"`
	}
	if _, err := enrollHTTP(context.Background(), http.MethodPost, base+"/offer",
		map[string]any{"ephemeral_public_key": ephPubB64, "label": *label}, &offer); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Trust our own derivation, not the relay's.
	sas := computeEnrollSAS(ephPubB64)

	if !*jsonOut {
		fmt.Fprintf(stdout, "On a device that already has %s, run:\n\n"+
			"  poweur key approve %s\n\nand confirm this code matches: %s\n\n",
			identityValue, offer.RendezvousID, sas)
	}
	if !*wait {
		return writeOutput(stdout, *jsonOut, map[string]any{
			"rendezvous_id":         offer.RendezvousID,
			"sas":                   sas,
			"expires_at":            offer.ExpiresAt,
			"ephemeral_private_key": base64.RawURLEncoding.EncodeToString(ephPriv),
		}, "")
	}

	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var claim struct {
			Ready  bool   `json:"ready"`
			Sealed string `json:"sealed"`
		}
		if _, err := enrollHTTP(context.Background(), http.MethodGet,
			base+"/"+offer.RendezvousID, nil, &claim); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if claim.Ready {
			seed, err := openSealedSeed(claim.Sealed, ephPriv)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			keyPath, encKeyPath, err := identity.SaveKeysFromSeed(identityValue, seed)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			cfg.Identity = identityValue
			cfg.RelayURL = *relayURL
			cfg.KeysDir = filepath.Dir(keyPath)
			if err := config.Save(cfg); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return writeOutput(stdout, *jsonOut, map[string]any{
				"identity":            identityValue,
				"key_path":            keyPath,
				"encryption_key_path": encKeyPath,
				"enrolled":            true,
			}, fmt.Sprintf("enrolled %s on this device\n", identityValue))
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Fprintln(stderr, "timed out waiting for approval")
	return 1
}

// runKeyClaim completes an enrollment started earlier by `key enroll` without
// --wait. Headless boxes and scripts need the two halves separable; the
// ephemeral private key is the state that has to survive between them.
func runKeyClaim(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("key claim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	relayURL := fs.String("relay", cfg.RelayURL, "relay base url")
	ephemeral := fs.String("ephemeral-key", "", "ephemeral private key from `key enroll`")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: poweur key claim <identity> <rendezvous-id> --ephemeral-key <b64url>")
		return 1
	}
	identityValue, rendezvousID := fs.Arg(0), fs.Arg(1)
	if *ephemeral == "" {
		fmt.Fprintln(stderr, "--ephemeral-key is required (printed by `poweur key enroll`)")
		return 1
	}
	ephPriv, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(*ephemeral))
	if err != nil || len(ephPriv) != 32 {
		fmt.Fprintln(stderr, "--ephemeral-key must be a 32-byte base64url value")
		return 1
	}
	if *relayURL == "" {
		fmt.Fprintln(stderr, "--relay is required")
		return 1
	}
	var claim struct {
		Ready  bool   `json:"ready"`
		Sealed string `json:"sealed"`
	}
	url := strings.TrimRight(*relayURL, "/") + "/identities/" + identityValue + "/enroll/" + rendezvousID
	if _, err := enrollHTTP(context.Background(), http.MethodGet, url, nil, &claim); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !claim.Ready {
		fmt.Fprintln(stderr, "not approved yet — run `poweur key approve` on a device that has this identity")
		return 1
	}
	seed, err := openSealedSeed(claim.Sealed, ephPriv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	keyPath, encKeyPath, err := identity.SaveKeysFromSeed(identityValue, seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg.Identity = identityValue
	cfg.RelayURL = *relayURL
	cfg.KeysDir = filepath.Dir(keyPath)
	if err := config.Save(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, *jsonOut, map[string]any{
		"identity": identityValue, "key_path": keyPath,
		"encryption_key_path": encKeyPath, "enrolled": true,
	}, fmt.Sprintf("enrolled %s on this device\n", identityValue))
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
	expectSAS := fs.String("sas", "", "code shown on the new device; refuses to proceed if it differs")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: poweur key approve <rendezvous-id> [--sas 123456]")
		return 1
	}
	rendezvousID := fs.Arg(0)
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
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

	base := strings.TrimRight(*relayURL, "/") + "/identities/" + identityValue + "/enroll/" + rendezvousID
	sign := func(action string) map[string]any {
		issuedAt := time.Now().UTC().Format(time.RFC3339)
		nonce := newAdminNonce()
		canonical := strings.Join([]string{
			action, strings.ToLower(identityValue), rendezvousID, issuedAt, nonce}, "\n")
		return map[string]any{
			"issued_at": issuedAt, "nonce": nonce,
			"identity_signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canonical))),
		}
	}

	var pending struct {
		EphemeralPublicKey string `json:"ephemeral_public_key"`
		Label              string `json:"label"`
	}
	if _, err := enrollHTTP(context.Background(), http.MethodPost, base+"/fetch", sign("enroll-fetch"), &pending); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sas := computeEnrollSAS(pending.EphemeralPublicKey)

	// Comparing the code IS the authentication step. Without --sas we can only
	// print it and rely on the operator; with it, refuse a mismatch outright.
	if *expectSAS != "" && *expectSAS != sas {
		fmt.Fprintf(stderr, "code mismatch: this rendezvous shows %s, you expected %s\n"+
			"Do not approve — another device may be trying to enrol.\n", sas, *expectSAS)
		return 1
	}
	if *expectSAS == "" && !*jsonOut {
		fmt.Fprintf(stderr, "confirm this matches the new device's screen: %s\n", sas)
	}

	sealed, err := cryptoe2e.Encrypt(mustDecodeKey(pending.EphemeralPublicKey), seed)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sealedJSON, err := json.Marshal(map[string]string{
		"ciphertext":         sealed.Ciphertext,
		"ephemeralPublicKey": sealed.EphemeralPublicKey,
		"nonce":              sealed.Nonce,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	body := sign("enroll-deliver")
	body["sealed"] = string(sealedJSON)
	if _, err := enrollHTTP(context.Background(), http.MethodPost, base+"/deliver", body, nil); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, *jsonOut, map[string]any{
		"identity": identityValue, "rendezvous_id": rendezvousID, "sas": sas, "approved": true,
	}, fmt.Sprintf("approved device %s for %s\n", rendezvousID, identityValue))
}

func mustDecodeKey(value string) []byte {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return decoded
}
