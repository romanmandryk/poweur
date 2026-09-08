package identity

import (
	"fmt"
	"net"
	"strings"
)

const (
	MinLabelLen = 3
	MaxLabelLen = 63
	MaxNameLen  = 253
)

// ReservedLabels cannot be claimed as the leftmost label of a hosted identity.
// Grouped by why each is held back; an operator's own list (NamePolicy.Reserved)
// extends this rather than replacing it, so "www" cannot be un-reserved by
// configuration.
var ReservedLabels = map[string]struct{}{
	// Infra / protocol.
	"www": {}, "admin": {}, "relay": {}, "mail": {}, "ftp": {},
	"api": {}, "app": {}, "static": {}, "cdn": {}, "ns": {}, "ns1": {}, "ns2": {},
	"mx": {}, "smtp": {}, "imap": {}, "pop": {}, "root": {}, "localhost": {},
	"poweur": {}, "well-known": {}, "dav": {}, "sync": {},
	// Product surfaces the operator may want to run. `id` and `launcher` are
	// here because E18-T3 serves the claim flow on one of them.
	"id": {}, "ids": {}, "launcher": {}, "get": {}, "join": {}, "signup": {},
	"signin": {}, "login": {}, "auth": {}, "account": {}, "accounts": {},
	"console": {}, "dashboard": {}, "portal": {}, "home": {},
	// Data / ops.
	"data": {}, "files": {}, "file": {}, "storage": {}, "analytics": {},
	"metrics": {}, "status": {}, "health": {}, "logs": {}, "backup": {}, "db": {},
	"search": {}, "index": {}, "assets": {}, "media": {}, "img": {}, "images": {},
	// Comms / org.
	"support": {}, "help": {}, "docs": {}, "blog": {}, "news": {}, "about": {},
	"contact": {}, "legal": {}, "privacy": {}, "terms": {}, "security": {},
	"abuse": {}, "postmaster": {}, "hostmaster": {}, "webmaster": {},
	"noreply": {}, "no-reply": {},
	// Money / trust — names that would let a handle imply endorsement.
	"pay": {}, "payments": {}, "billing": {}, "wallet": {}, "invoice": {},
	"verify": {}, "verified": {}, "official": {}, "team": {}, "staff": {},
	"system": {}, "bot": {}, "test": {}, "demo": {}, "example": {},
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

// validateLabel enforces the DNS label rules every identity must satisfy,
// hosted or not: ASCII letter-digit-hyphen, no leading or trailing hyphen.
//
// It is deliberately ASCII-only. `unicode.IsLetter` used to be accepted here,
// which let a Cyrillic "а" stand in for "a" — so "аdmin" passed while "admin"
// was reserved. A raw UTF-8 label is also not a valid DNS label and is not
// covered by a wildcard certificate, so this is correctness before it is
// anti-impersonation. Punycode (`xn--…`) stays legal at this level for
// self-hosted identities on real IDN domains; hosted *handles* refuse it in
// NamePolicy, where v1 has no IDN story.
func validateLabel(label string) error {
	if len(label) < 1 || len(label) > MaxLabelLen {
		return fmt.Errorf("invalid length")
	}
	for i, r := range label {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(label)-1:
		default:
			return fmt.Errorf("invalid character")
		}
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("cannot start or end with hyphen")
	}
	return nil
}

// ValidateHostedHandle checks a hosted handle against the default policy.
// Callers with an operator-configured policy use
// ValidateHostedHandleWithPolicy; this stays as the zero-configuration door.
func ValidateHostedHandle(identity string) error {
	return ValidateHostedHandleWithPolicy(identity, DefaultHostedPolicy())
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
