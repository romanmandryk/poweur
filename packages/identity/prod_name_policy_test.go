package identity

import (
	"path/filepath"
	"testing"
)

// The production blocked-terms file is read by the relay with no error when it
// is missing or empty (blocking silently turns off), so pin what it must do.
func TestProdBlockedTermsFile(t *testing.T) {
	terms, err := LoadBlockedTerms(filepath.Join("..", "..", "deploy", "relay", "blocked-terms.txt"))
	if err != nil || len(terms) == 0 {
		t.Fatalf("terms = %v, %v", terms, err)
	}
	policy := NamePolicy{MinLen: 6, MaxLen: 24, Blocked: terms, BlockMode: BlockModeSubstring}
	for _, blocked := range []string{"paypal-help", "mypaypal", "poweur-support", "official-openai"} {
		if err := policy.ValidateHandleLabel(blocked); ReasonOf(err) != ReasonBlocked {
			t.Errorf("%q = %v, want blocked", blocked, err)
		}
	}
	for _, ok := range []string{"pineapple", "alice-smith", "roman-m", "stripesfan"} {
		if err := policy.ValidateHandleLabel(ok); err != nil {
			t.Errorf("%q = %v, want available", ok, err)
		}
	}
}
