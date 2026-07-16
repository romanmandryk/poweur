package identity

import (
	"fmt"
	"net"
	"strings"
	"unicode"
)

const (
	MinLabelLen = 3
	MaxLabelLen = 63
	MaxNameLen  = 253
)

// ReservedLabels cannot be claimed as the leftmost label of a hosted identity.
var ReservedLabels = map[string]struct{}{
	"www": {}, "admin": {}, "relay": {}, "mail": {}, "ftp": {},
	"api": {}, "app": {}, "static": {}, "cdn": {}, "ns": {}, "ns1": {}, "ns2": {},
	"mx": {}, "smtp": {}, "imap": {}, "pop": {}, "root": {}, "localhost": {},
	"poweur": {}, "well-known": {}, "dav": {}, "sync": {},
}

// ValidateIdentityName checks FQDN shape and reserved leftmost labels.
func ValidateIdentityName(identity string) error {
	identity = strings.TrimSpace(strings.ToLower(identity))
	if identity == "" {
		return fmt.Errorf("identity is empty")
	}
	if len(identity) > MaxNameLen {
		return fmt.Errorf("identity too long")
	}
	if net.ParseIP(identity) != nil || looksLikeIPLiteral(identity) {
		return fmt.Errorf("IP-literal identities are not allowed")
	}
	labels := strings.Split(identity, ".")
	if len(labels) < 2 {
		return fmt.Errorf("identity must be a FQDN with at least two labels")
	}
	for i, label := range labels {
		if err := validateLabel(label); err != nil {
			return fmt.Errorf("label %q: %w", label, err)
		}
		if i == 0 {
			if _, reserved := ReservedLabels[label]; reserved {
				return fmt.Errorf("label %q is reserved", label)
			}
		}
	}
	return nil
}

func validateLabel(label string) error {
	if len(label) < 1 || len(label) > MaxLabelLen {
		return fmt.Errorf("invalid length")
	}
	// Hosted leftmost labels should be reasonably long; allow short parent labels
	// (e.g. "co.uk") — only enforce MinLabelLen on the identity handle (caller).
	for i, r := range label {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		if r == '-' && i > 0 && i < len(label)-1 {
			continue
		}
		return fmt.Errorf("invalid character")
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("cannot start or end with hyphen")
	}
	return nil
}

// ValidateHostedHandle checks the leftmost label meets hosted min length.
func ValidateHostedHandle(identity string) error {
	if err := ValidateIdentityName(identity); err != nil {
		return err
	}
	label := strings.Split(strings.ToLower(identity), ".")[0]
	if len(label) < MinLabelLen {
		return fmt.Errorf("hosted handle must be at least %d characters", MinLabelLen)
	}
	return nil
}

// IsUnderDomain reports whether identity is a subdomain of parent (e.g. alice.poweur.net under poweur.net).
func IsUnderDomain(identity, parent string) bool {
	identity = strings.ToLower(strings.TrimSuffix(identity, "."))
	parent = strings.ToLower(strings.TrimSuffix(parent, "."))
	if parent == "" {
		return false
	}
	return identity == parent || strings.HasSuffix(identity, "."+parent)
}

// SanitizeIdentityDirName maps an identity FQDN to a single directory name.
// Dots are replaced with "__" so path segments cannot escape.
func SanitizeIdentityDirName(identity string) (string, error) {
	if err := ValidateIdentityName(identity); err != nil {
		return "", err
	}
	id := strings.ToLower(strings.TrimSuffix(identity, "."))
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("identity contains path separators")
	}
	return strings.ReplaceAll(id, ".", "__"), nil
}

func looksLikeIPLiteral(s string) bool {
	if strings.HasPrefix(s, "[") {
		return true
	}
	// IPv4-ish
	parts := strings.Split(s, ".")
	if len(parts) == 4 {
		allNum := true
		for _, p := range parts {
			if p == "" {
				allNum = false
				break
			}
			for _, c := range p {
				if c < '0' || c > '9' {
					allNum = false
					break
				}
			}
		}
		return allNum
	}
	return false
}
