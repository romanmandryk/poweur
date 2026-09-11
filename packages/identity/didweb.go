package identity

import (
	"fmt"
	"strings"
)

// DIDWebDocument is the deterministic did:web projection of an identity
// document. Poweur IDs remain DNS names; this is only an interoperability view.
type DIDWebDocument struct {
	Context              []string                `json:"@context,omitempty"`
	ID                   string                  `json:"id"`
	VerificationMethod   []DIDVerificationMethod `json:"verificationMethod"`
	Authentication       []string                `json:"authentication"`
	AssertionMethod      []string                `json:"assertionMethod"`
	CapabilityInvocation []string                `json:"capabilityInvocation"`
	KeyAgreement         []string                `json:"keyAgreement,omitempty"`
	Service              []DIDService            `json:"service,omitempty"`
}

type DIDVerificationMethod struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Controller   string    `json:"controller"`
	PublicKeyJWK DIDKeyJWK `json:"publicKeyJwk"`
}

type DIDKeyJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	X   string `json:"x"`
	Use string `json:"use,omitempty"`
}

type DIDService struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}

// DIDWeb projects the current Poweur signing and encryption keys into the
// JSON representation consumed by did:web resolvers.
func DIDWeb(doc IdentityDocument) (DIDWebDocument, error) {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(doc.Identity), "."))
	if err := ValidateIdentityName(name); err != nil {
		return DIDWebDocument{}, fmt.Errorf("invalid did:web identity: %w", err)
	}
	signing, err := ParseEd25519PublicKey(doc.PublicKey)
	if err != nil {
		return DIDWebDocument{}, fmt.Errorf("invalid did:web signing key: %w", err)
	}
	did := "did:web:" + name
	signingID := did + "#signing-key"
	out := DIDWebDocument{
		Context: []string{"https://www.w3.org/ns/did/v1"},
		ID:      did,
		VerificationMethod: []DIDVerificationMethod{{
			ID:         signingID,
			Type:       "JsonWebKey2020",
			Controller: did,
			PublicKeyJWK: DIDKeyJWK{
				KTY: "OKP", CRV: "Ed25519", X: NormalizePublicKeyKey(FormatEd25519PublicKey(signing)), Use: "sig",
			},
		}},
		Authentication:       []string{signingID},
		AssertionMethod:      []string{signingID},
		CapabilityInvocation: []string{signingID},
	}
	if doc.EncryptionPublicKey != "" {
		enc, err := ParseX25519PublicKey(doc.EncryptionPublicKey)
		if err != nil {
			return DIDWebDocument{}, fmt.Errorf("invalid did:web encryption key: %w", err)
		}
		encID := did + "#encryption-key"
		out.VerificationMethod = append(out.VerificationMethod, DIDVerificationMethod{
			ID:         encID,
			Type:       "JsonWebKey2020",
			Controller: did,
			PublicKeyJWK: DIDKeyJWK{
				KTY: "OKP", CRV: "X25519", X: NormalizePublicKeyKey(FormatX25519PublicKey(enc)), Use: "enc",
			},
		})
		out.KeyAgreement = []string{encID}
	}
	if relay := strings.TrimSpace(doc.Relay); relay != "" {
		if !strings.Contains(relay, "://") {
			relay = "https://" + relay
		}
		out.Service = []DIDService{{ID: did + "#poweur-relay", Type: "PoweurRelay", ServiceEndpoint: strings.TrimRight(relay, "/")}}
	}
	return out, nil
}
