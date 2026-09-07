package relay

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// WebAuthn assertion verification for the keystore bootstrap read
// (EPIC-011 E11-T1).
//
// This is the one place the relay authenticates a caller who does *not* hold
// the identity key — by design, since the keystore exists to recover it. The
// gate is possession of an enrolled authenticator.
//
// Public keys are stored as SPKI DER, exactly what WebAuthn's
// `getPublicKey()` returns, so verification uses crypto/x509 rather than a
// CBOR/COSE parser. Less code in the trusted path, and none of it ours.

// COSE algorithm identifiers, matching the web client's pubKeyCredParams.
const (
	coseAlgES256 = -7
	coseAlgEdDSA = -8
	coseAlgRS256 = -257
)

// Authenticator data flag bits (WebAuthn §6.1).
const (
	authFlagUserPresent  = 0x01
	authFlagUserVerified = 0x04
)

// authenticatorDataMinLen is rpIdHash(32) + flags(1) + signCount(4).
const authenticatorDataMinLen = 37

// WebAuthnAssertion is the client's response to an authentication challenge.
type WebAuthnAssertion struct {
	CredentialID      string `json:"credential_id"`
	ClientDataJSON    string `json:"client_data_json"`
	AuthenticatorData string `json:"authenticator_data"`
	Signature         string `json:"signature"`
}

// collectedClientData is the subset of clientDataJSON we verify.
type collectedClientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

// verifyWebAuthnAssertion checks an assertion against an enrolled credential.
//
// rpIDs lists the relying-party ids acceptable for this identity: a hosted
// identity's credential may be scoped to the registrable domain while the
// request arrives at the identity host (EPIC-018 E18-T4), so more than one can
// be legitimate. The caller supplies the set; this function does not guess.
func verifyWebAuthnAssertion(
	assertion WebAuthnAssertion,
	publicKeySPKI string,
	alg int,
	expectedChallenge string,
	rpIDs []string,
) error {
	if assertion.ClientDataJSON == "" || assertion.AuthenticatorData == "" || assertion.Signature == "" {
		return errors.New("assertion is incomplete")
	}

	clientDataRaw, err := decodeB64(assertion.ClientDataJSON)
	if err != nil {
		return fmt.Errorf("client_data_json: %w", err)
	}
	authData, err := decodeB64(assertion.AuthenticatorData)
	if err != nil {
		return fmt.Errorf("authenticator_data: %w", err)
	}
	sig, err := decodeB64(assertion.Signature)
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}

	var clientData collectedClientData
	if err := json.Unmarshal(clientDataRaw, &clientData); err != nil {
		return fmt.Errorf("client_data_json is not valid JSON: %w", err)
	}
	if clientData.Type != "webauthn.get" {
		return fmt.Errorf("unexpected client data type %q", clientData.Type)
	}
	// The challenge is relay-issued and single-use; comparing it is what makes
	// the assertion non-replayable.
	challengeBytes, err := decodeB64(clientData.Challenge)
	if err != nil {
		return fmt.Errorf("client data challenge: %w", err)
	}
	if string(challengeBytes) != expectedChallenge {
		return errors.New("challenge mismatch")
	}

	if len(authData) < authenticatorDataMinLen {
		return errors.New("authenticator_data too short")
	}
	if !rpIDHashMatches(authData[:32], rpIDs) {
		return errors.New("rp id hash does not match this identity")
	}
	flags := authData[32]
	if flags&authFlagUserPresent == 0 {
		return errors.New("user presence flag not set")
	}
	// The keystore releases key material, so a mere touch is not enough —
	// require the same user verification the PRF wrapping already demands.
	if flags&authFlagUserVerified == 0 {
		return errors.New("user verification required")
	}

	pub, err := parseSPKIPublicKey(publicKeySPKI)
	if err != nil {
		return err
	}
	clientDataHash := sha256.Sum256(clientDataRaw)
	signed := append(append([]byte{}, authData...), clientDataHash[:]...)
	return verifyAssertionSignature(pub, alg, signed, sig)
}

// rpIDHashMatches reports whether authData's rpIdHash equals SHA-256 of any
// acceptable relying-party id.
func rpIDHashMatches(got []byte, rpIDs []string) bool {
	for _, rp := range rpIDs {
		rp = strings.ToLower(strings.TrimSpace(rp))
		if rp == "" {
			continue
		}
		want := sha256.Sum256([]byte(rp))
		if subtle.ConstantTimeCompare(got, want[:]) == 1 {
			return true
		}
	}
	return false
}

func parseSPKIPublicKey(encoded string) (crypto.PublicKey, error) {
	der, err := decodeB64(encoded)
	if err != nil {
		return nil, fmt.Errorf("credential_public_key: %w", err)
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("credential_public_key is not valid SPKI: %w", err)
	}
	return pub, nil
}

func verifyAssertionSignature(pub crypto.PublicKey, alg int, signed, sig []byte) error {
	switch alg {
	case coseAlgES256:
		key, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("alg ES256 requires an ECDSA key")
		}
		digest := sha256.Sum256(signed)
		if !ecdsa.VerifyASN1(key, digest[:], sig) {
			return errors.New("assertion signature invalid")
		}
		return nil
	case coseAlgEdDSA:
		key, ok := pub.(ed25519.PublicKey)
		if !ok {
			return errors.New("alg EdDSA requires an Ed25519 key")
		}
		if !ed25519.Verify(key, signed, sig) {
			return errors.New("assertion signature invalid")
		}
		return nil
	case coseAlgRS256:
		key, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("alg RS256 requires an RSA key")
		}
		digest := sha256.Sum256(signed)
		if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
			return errors.New("assertion signature invalid")
		}
		return nil
	default:
		return fmt.Errorf("unsupported credential algorithm %d", alg)
	}
}

// decodeB64 accepts base64url with or without padding, matching what browsers
// and the existing clients emit.
func decodeB64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return nil, errors.New("value is not base64url")
}
