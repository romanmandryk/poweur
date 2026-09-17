package bridge

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

func jwsSigningInput(header, claims any) (string, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c), nil
}

type jwsHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid,omitempty"`
	Typ string `json:"typ,omitempty"`
}

// parseJWS splits a compact JWS and decodes its header and payload. It does
// not verify anything.
func parseJWS(token string) (jwsHeader, []byte, string, []byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwsHeader{}, nil, "", nil, errors.New("not a compact JWS")
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwsHeader{}, nil, "", nil, errors.New("JWS header is not base64url")
	}
	var h jwsHeader
	if err := json.Unmarshal(rawHeader, &h); err != nil {
		return jwsHeader{}, nil, "", nil, errors.New("JWS header is not JSON")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwsHeader{}, nil, "", nil, errors.New("JWS payload is not base64url")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwsHeader{}, nil, "", nil, errors.New("JWS signature is not base64url")
	}
	return h, payload, parts[0] + "." + parts[1], sig, nil
}

// verifyJWS checks a compact JWS against a key set and returns its payload.
// Accepted algorithms: RS256, ES256, EdDSA. "none" and HMAC are never
// accepted — a client's JWKS is public, so an HMAC "key" from it is not one.
func verifyJWS(token string, set JWKS) ([]byte, error) {
	h, payload, input, sig, err := parseJWS(token)
	if err != nil {
		return nil, err
	}
	var candidates []JWK
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if h.Kid != "" && k.Kid != "" && k.Kid != h.Kid {
			continue
		}
		candidates = append(candidates, k)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no key matches kid %q", h.Kid)
	}
	for _, k := range candidates {
		if verifyWithJWK(h.Alg, k, input, sig) == nil {
			return payload, nil
		}
	}
	return nil, errors.New("JWS signature does not verify")
}

func verifyWithJWK(alg string, k JWK, input string, sig []byte) error {
	if k.Alg != "" && k.Alg != alg {
		return errors.New("algorithm does not match key")
	}
	switch alg {
	case "RS256":
		pub, err := rsaPublicKey(k)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(input))
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
	case "ES256":
		pub, err := ecPublicKey(k)
		if err != nil {
			return err
		}
		if len(sig) != 64 {
			return errors.New("ES256 signature must be 64 bytes")
		}
		sum := sha256.Sum256([]byte(input))
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, sum[:], r, s) {
			return errors.New("bad signature")
		}
		return nil
	case "EdDSA":
		if k.Kty != "OKP" || k.Crv != "Ed25519" {
			return errors.New("EdDSA needs an Ed25519 OKP key")
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return errors.New("bad Ed25519 key")
		}
		if !ed25519.Verify(ed25519.PublicKey(x), []byte(input), sig) {
			return errors.New("bad signature")
		}
		return nil
	default:
		return fmt.Errorf("algorithm %q is not accepted", alg)
	}
}

func rsaPublicKey(k JWK) (*rsa.PublicKey, error) {
	if k.Kty != "RSA" {
		return nil, errors.New("not an RSA key")
	}
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, errors.New("bad RSA modulus")
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("bad RSA exponent")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	if pub.N.BitLen() < 2048 {
		return nil, errors.New("RSA keys must be at least 2048 bits")
	}
	return pub, nil
}

func ecPublicKey(k JWK) (*ecdsa.PublicKey, error) {
	if k.Kty != "EC" || k.Crv != "P-256" {
		return nil, errors.New("ES256 needs a P-256 EC key")
	}
	x, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil || len(x) != 32 {
		return nil, errors.New("bad EC x")
	}
	y, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil || len(y) != 32 {
		return nil, errors.New("bad EC y")
	}
	// Uncompressed point; ecdh validates it is on the curve.
	point := append([]byte{4}, append(x, y...)...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, errors.New("EC point is not on P-256")
	}
	return pub, nil
}
