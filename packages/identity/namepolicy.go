package identity

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Hosted name policy (EPIC-018 E18-T1). Which handles an operator will hand
// out is a deployment decision — anti-squatting minimums, reserved product
// names, a profanity list — so it is configuration, not a constant.
//
// What is *not* configurable is the character set. A handle becomes a DNS
// label and a TLS SAN under the operator's wildcard certificate, so it must be
// ASCII letter-digit-hyphen. Before this, `validateLabel` accepted any
// `unicode.IsLetter`, which let "аdmin" (Cyrillic U+0430) through while
// "admin" was reserved — an impersonation vector and an invalid DNS label at
// the same time.

// NameReason is the machine-readable verdict for a rejected (or accepted)
// handle. The availability endpoint (E18-T2) returns these verbatim, so a
// client can render its own message without parsing prose.
type NameReason string

const (
	ReasonAvailable       NameReason = "available"
	ReasonTaken           NameReason = "taken"
	ReasonReserved        NameReason = "reserved"
	ReasonBlocked         NameReason = "blocked"
	ReasonTooShort        NameReason = "too_short"
	ReasonTooLong         NameReason = "too_long"
	ReasonCharset         NameReason = "charset"
	ReasonHyphen          NameReason = "hyphen"
	ReasonPunycode        NameReason = "punycode"
	ReasonDomainNotHosted NameReason = "domain_not_hosted"
	ReasonInvalid         NameReason = "invalid"
)

// NameError carries the reason alongside the message, so callers branch on a
// code rather than matching error strings.
type NameError struct {
	Reason  NameReason
	Message string
}

func (e *NameError) Error() string { return e.Message }

func nameErr(reason NameReason, format string, args ...any) *NameError {
	return &NameError{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// ReasonOf extracts the verdict from an error returned by this package,
// falling back to `invalid` for anything unrecognized.
func ReasonOf(err error) NameReason {
	if err == nil {
		return ReasonAvailable
	}
	var ne *NameError
	if ok := asNameError(err, &ne); ok {
		return ne.Reason
	}
	return ReasonInvalid
}

func asNameError(err error, target **NameError) bool {
	for err != nil {
		if ne, ok := err.(*NameError); ok {
			*target = ne
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// Block matching modes for NAME_BLOCKED_FILE terms.
const (
	BlockModeSubstring = "substring"
	BlockModeExact     = "exact"
)

// NamePolicy is the operator-tunable half of hosted handle validation.
//
// The two switches are negative on purpose: a zero-valued NamePolicy has to
// mean "the usual rules", because relays and tests construct Config structs
// directly and an `AllowDigits bool` left unset would silently reject every
// handle with a number in it.
type NamePolicy struct {
	MinLen         int
	MaxLen         int
	DisallowHyphen bool
	DisallowDigits bool
	// Reserved *extends* ReservedLabels rather than replacing it, so an
	// operator cannot accidentally un-reserve "www" by setting their own list.
	Reserved []string
	// Blocked terms (profanity / abuse), matched per BlockMode.
	Blocked   []string
	BlockMode string
}

// DefaultHostedPolicy is what the package assumes when nobody configures
// anything. MinLen stays 3 so the fixtures and integration suite (alice, bob)
// keep working; the production default of 6 lives in the deployment config,
// deliberately, and the two must not be unified by "fixing" the fixtures.
func DefaultHostedPolicy() NamePolicy {
	return NamePolicy{
		MinLen:    MinLabelLen,
		MaxLen:    MaxLabelLen,
		BlockMode: BlockModeSubstring,
	}
}

// normalized returns the policy with zero values filled in, so a partially
// configured struct behaves like the default rather than rejecting everything.
func (p NamePolicy) normalized() NamePolicy {
	if p.MinLen <= 0 {
		p.MinLen = MinLabelLen
	}
	if p.MaxLen <= 0 || p.MaxLen > MaxLabelLen {
		p.MaxLen = MaxLabelLen
	}
	if p.BlockMode == "" {
		p.BlockMode = BlockModeSubstring
	}
	return p
}

// Describe reports the policy in the shape the availability endpoint echoes,
// so a client can validate inline without hardcoding the rules.
func (p NamePolicy) Describe() map[string]any {
	p = p.normalized()
	charset := "a-z"
	if !p.DisallowDigits {
		charset += " 0-9"
	}
	if !p.DisallowHyphen {
		charset += " -"
	}
	return map[string]any{
		"min_len": p.MinLen,
		"max_len": p.MaxLen,
		"charset": charset,
	}
}

// IsReserved reports whether a (already normalized) label is held back, by the
// built-in list or the operator's additions.
func (p NamePolicy) IsReserved(label string) bool {
	if _, ok := ReservedLabels[label]; ok {
		return true
	}
	for _, reserved := range p.Reserved {
		if strings.EqualFold(strings.TrimSpace(reserved), label) {
			return true
		}
	}
	return false
}

// ValidateHandleLabel checks one leftmost label against the policy. The order
// matters: character-set problems are reported before length, because "аdmin"
// being 5 characters is not the interesting thing about it.
func (p NamePolicy) ValidateHandleLabel(label string) error {
	p = p.normalized()
	normalized := strings.ToLower(strings.TrimSpace(label))
	if normalized == "" {
		return nameErr(ReasonTooShort, "handle is empty")
	}

	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if p.DisallowDigits {
				return nameErr(ReasonCharset, "digits are not allowed in a handle")
			}
		case r == '-':
			if p.DisallowHyphen {
				return nameErr(ReasonCharset, "hyphens are not allowed in a handle")
			}
		default:
			// Non-ASCII lands here: this is the homoglyph gate.
			return nameErr(ReasonCharset, "handles may use only a-z, 0-9 and hyphen")
		}
	}

	if strings.HasPrefix(normalized, "xn--") {
		return nameErr(ReasonPunycode, "handles may not start with xn-- (no IDN in v1)")
	}
	if strings.HasPrefix(normalized, "-") || strings.HasSuffix(normalized, "-") {
		return nameErr(ReasonHyphen, "handles cannot start or end with a hyphen")
	}
	if strings.Contains(normalized, "--") {
		return nameErr(ReasonHyphen, "handles cannot contain a double hyphen")
	}
	// Reserved and blocked are checked *before* length, so a held-back name
	// says why it is held back. "admin" is 5 characters: under a MinLen of 6 a
	// length-first order would report too_short and quietly invite the user to
	// try "admins".
	if p.IsReserved(normalized) {
		return nameErr(ReasonReserved, "handle %q is reserved", normalized)
	}
	if len(normalized) < p.MinLen {
		return nameErr(ReasonTooShort, "handle must be at least %d characters", p.MinLen)
	}
	if len(normalized) > p.MaxLen {
		return nameErr(ReasonTooLong, "handle must be at most %d characters", p.MaxLen)
	}
	if p.isBlocked(normalized) {
		// The matched term is not echoed: a blocklist that answers "which word
		// did I trip?" is a blocklist you can read out.
		return nameErr(ReasonBlocked, "handle is not available")
	}
	return nil
}

func (p NamePolicy) isBlocked(label string) bool {
	for _, term := range p.Blocked {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		if p.BlockMode == BlockModeExact {
			if label == term {
				return true
			}
			continue
		}
		if strings.Contains(label, term) {
			return true
		}
	}
	return false
}

// ValidateHostedHandleWithPolicy validates a full hosted identity.
//
// The *handle* is checked first, before the generic FQDN shape, because only
// the policy produces typed reasons: run the shape check first and "аdmin"
// comes back as a nameless "invalid character" instead of `charset`, which is
// the one thing the user needs to be told.
func ValidateHostedHandleWithPolicy(identity string, p NamePolicy) error {
	trimmed := strings.ToLower(strings.TrimSpace(identity))
	if trimmed == "" {
		return nameErr(ReasonInvalid, "identity is empty")
	}
	if !strings.Contains(trimmed, ".") {
		return nameErr(ReasonInvalid, "identity must be a FQDN with at least two labels")
	}
	if err := p.ValidateHandleLabel(strings.Split(trimmed, ".")[0]); err != nil {
		return err
	}
	if err := ValidateIdentityName(identity); err != nil {
		if ne, ok := err.(*NameError); ok {
			return ne
		}
		return nameErr(ReasonInvalid, "%s", err.Error())
	}
	return nil
}

// LoadBlockedTerms reads one term per line, ignoring blanks and `#` comments.
// A missing file is not an error: an operator who never configured a list gets
// no blocking, and a relay must not refuse to boot over it.
func LoadBlockedTerms(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var terms []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		terms = append(terms, strings.ToLower(line))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return terms, nil
}
