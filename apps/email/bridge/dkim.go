package bridge

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"github.com/emersion/go-msgauth/dkim"
)

// DKIMSigner signs outbound mail as the sender's domain, for a relay that
// does not (a mail server in front, or Brevo with an authenticated domain,
// signs on its own and needs none of this).
type DKIMSigner struct {
	selector string
	key      crypto.Signer
	domains  map[string]bool
}

// LoadDKIMSigner reads a PEM private key (PKCS#8 RSA or Ed25519, or PKCS#1 RSA).
func LoadDKIMSigner(selector, keyFile string, domains []DomainMap) (*DKIMSigner, error) {
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("DKIM key: not PEM")
	}
	var key any
	if key, err = x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		if key, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return nil, fmt.Errorf("DKIM key: %w", err)
		}
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("DKIM key: unsupported key type")
	}
	served := map[string]bool{}
	for _, d := range domains {
		served[d.Mail] = true
	}
	return &DKIMSigner{selector: selector, key: signer, domains: served}, nil
}

// Sign returns msg with a DKIM-Signature for the From domain, when that is
// one this bridge serves.
func (s *DKIMSigner) Sign(msg []byte, fromDomain string) ([]byte, error) {
	if s == nil || !s.domains[fromDomain] {
		return msg, nil
	}
	var out bytes.Buffer
	err := dkim.Sign(&out, bytes.NewReader(msg), &dkim.SignOptions{
		Domain:   fromDomain,
		Selector: s.selector,
		Signer:   s.key,
		HeaderKeys: []string{"From", "To", "Cc", "Subject", "Date", "Message-Id", "In-Reply-To", "References",
			"Mime-Version", "Content-Type", "Content-Transfer-Encoding"},
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
