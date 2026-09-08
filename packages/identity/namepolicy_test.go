package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bug this whole file exists for: `validateLabel` accepted any
// `unicode.IsLetter`, so a Cyrillic "а" (U+0430) stood in for ASCII "a" and
// "аdmin.poweur.net" registered while "admin.poweur.net" was reserved.
func TestHomoglyphHandlesAreRejected(t *testing.T) {
	policy := DefaultHostedPolicy()
	for _, identity := range []string{
		"аdmin.poweur.net",  // Cyrillic а + dmin
		"аlice.poweur.net",  // Cyrillic а + lice
		"pоweur.poweur.net", // Cyrillic о
	} {
		err := ValidateHostedHandleWithPolicy(identity, policy)
		if err == nil {
			t.Fatalf("%q was accepted; homoglyph bypass is back", identity)
		}
		if got := ReasonOf(err); got != ReasonCharset {
			t.Fatalf("%q: reason = %q, want %q", identity, got, ReasonCharset)
		}
	}

	// …and the plain-ASCII cases still behave.
	if err := ValidateHostedHandleWithPolicy("alice.poweur.net", policy); err != nil {
		t.Fatalf("alice rejected: %v", err)
	}
	if got := ReasonOf(ValidateHostedHandleWithPolicy("www.poweur.net", policy)); got != ReasonReserved {
		t.Fatalf("www: reason = %q, want %q", got, ReasonReserved)
	}
	if got := ReasonOf(ValidateHostedHandleWithPolicy("admin.poweur.net", policy)); got != ReasonReserved {
		t.Fatalf("admin: reason = %q, want %q", got, ReasonReserved)
	}
}

func TestValidateHandleLabelReasons(t *testing.T) {
	policy := NamePolicy{
		MinLen:    6,
		MaxLen:    12,
		Reserved:  []string{"acme"},
		Blocked:   []string{"spam"},
		BlockMode: BlockModeSubstring,
	}

	cases := []struct {
		label string
		want  NameReason
	}{
		{"robert", ReasonAvailable},
		{"robert-7", ReasonAvailable},
		{"bob", ReasonTooShort},
		{"", ReasonTooShort},
		{"averyveryverylonghandle", ReasonTooLong},
		{"admin", ReasonReserved},   // built-in list
		{"acme", ReasonReserved},    // operator addition (short-circuits length)
		{"spammer", ReasonBlocked},  // substring match
		{"bad_name", ReasonCharset}, // underscore is not LDH
		{"héllo", ReasonCharset},
		{"-robert", ReasonHyphen},
		{"robert-", ReasonHyphen},
		{"rob--ert", ReasonHyphen},
		{"xn--robert", ReasonPunycode},
	}
	for _, tc := range cases {
		got := ReasonOf(policy.ValidateHandleLabel(tc.label))
		if got != tc.want {
			t.Errorf("%q: reason = %q, want %q", tc.label, got, tc.want)
		}
	}
}

// "acme" is 4 characters under a MinLen of 6, so this pins the order: a
// reserved name reports *reserved*, not too_short, or an operator's held-back
// short names would be indistinguishable from ordinary rejections.
func TestReservedBeatsLength(t *testing.T) {
	policy := NamePolicy{MinLen: 6, Reserved: []string{"acme"}}
	if got := ReasonOf(policy.ValidateHandleLabel("acme")); got != ReasonReserved {
		t.Fatalf("reason = %q, want %q", got, ReasonReserved)
	}
}

func TestPolicyFlagsDisableCharacterClasses(t *testing.T) {
	noDigits := NamePolicy{MinLen: 3, DisallowDigits: true}
	if got := ReasonOf(noDigits.ValidateHandleLabel("bob7")); got != ReasonCharset {
		t.Fatalf("digits: reason = %q, want %q", got, ReasonCharset)
	}
	noHyphen := NamePolicy{MinLen: 3, DisallowHyphen: true}
	if got := ReasonOf(noHyphen.ValidateHandleLabel("bo-b")); got != ReasonCharset {
		t.Fatalf("hyphen: reason = %q, want %q", got, ReasonCharset)
	}
}

func TestOperatorReservedExtendsRatherThanReplaces(t *testing.T) {
	policy := NamePolicy{MinLen: 3, Reserved: []string{"acme"}}
	if !policy.IsReserved("www") {
		t.Fatal("built-in reserved label was lost when the operator set their own list")
	}
	if !policy.IsReserved("acme") {
		t.Fatal("operator reserved label not honoured")
	}
}

func TestBlockModeExactOnlyMatchesWholeLabel(t *testing.T) {
	policy := NamePolicy{MinLen: 3, Blocked: []string{"spam"}, BlockMode: BlockModeExact}
	if got := ReasonOf(policy.ValidateHandleLabel("spammer")); got != ReasonAvailable {
		t.Fatalf("spammer under exact mode: reason = %q, want available", got)
	}
	if got := ReasonOf(policy.ValidateHandleLabel("spam")); got != ReasonBlocked {
		t.Fatalf("spam under exact mode: reason = %q, want %q", got, ReasonBlocked)
	}
}

func TestBlockedMessageDoesNotEchoTheTerm(t *testing.T) {
	// A blocklist that answers "which word did I trip?" is one you can read out.
	policy := NamePolicy{MinLen: 3, Blocked: []string{"secretword"}}
	err := policy.ValidateHandleLabel("secretword")
	if err == nil {
		t.Fatal("blocked term was accepted")
	}
	if got := err.Error(); got == "" || strings.Contains(got, "secretword") {
		t.Fatalf("message leaks the blocked term: %q", got)
	}
}

func TestLoadBlockedTerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocked.txt")
	body := "# comment\n\nSpam\n  fraud  \n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	terms, err := LoadBlockedTerms(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != 2 || terms[0] != "spam" || terms[1] != "fraud" {
		t.Fatalf("terms = %v, want [spam fraud]", terms)
	}

	// A missing file is a configuration that says "no blocking", not a failure:
	// a relay must boot without one.
	terms, err = LoadBlockedTerms(filepath.Join(dir, "absent.txt"))
	if err != nil || terms != nil {
		t.Fatalf("missing file: terms = %v, err = %v", terms, err)
	}
	if terms, err := LoadBlockedTerms(""); err != nil || terms != nil {
		t.Fatalf("empty path: terms = %v, err = %v", terms, err)
	}
}

// The default policy is the dev/test one: the fixtures and the integration
// suite are full of "bob", and the production minimum lives in deploy config.
func TestDefaultPolicyKeepsShortFixtureNames(t *testing.T) {
	if err := ValidateHostedHandle("bob.poweur.net"); err != nil {
		t.Fatalf("bob rejected by the default policy: %v", err)
	}
	if err := ValidateHostedHandleWithPolicy("bob.poweur.net", NamePolicy{MinLen: 6}); err == nil {
		t.Fatal("bob accepted under a MinLen of 6")
	}
	if err := ValidateHostedHandleWithPolicy("robert.poweur.net", NamePolicy{MinLen: 6}); err != nil {
		t.Fatalf("robert rejected under a MinLen of 6: %v", err)
	}
}

// A Config built in code (every test relay does this) must behave like the
// default, not like "no digits, no hyphens".
func TestZeroValuePolicyIsPermissive(t *testing.T) {
	var zero NamePolicy
	for _, label := range []string{"robert", "robert7", "rob-ert"} {
		if err := zero.ValidateHandleLabel(label); err != nil {
			t.Fatalf("%q rejected by a zero-valued policy: %v", label, err)
		}
	}
}

func TestDescribeEchoesTheRulesAClientNeeds(t *testing.T) {
	described := DefaultHostedPolicy().Describe()
	if described["min_len"] != MinLabelLen {
		t.Fatalf("min_len = %v", described["min_len"])
	}
	if described["charset"] != "a-z 0-9 -" {
		t.Fatalf("charset = %v", described["charset"])
	}
}
