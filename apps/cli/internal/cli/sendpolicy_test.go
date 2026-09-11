package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsPolicyRejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"policy rejection", 403, `{"error":"policy_rejected","detail":"nope"}`, true},
		{"other 403", 403, `{"error":"not_authorized"}`, false},
		{"anonymous rejection is still policy", 403, `{"error":"policy_rejected","detail":"recipient does not accept anonymous messages"}`, true},
		{"rate limit", 429, `{"error":"rate_limit_exceeded"}`, false},
		{"conflict on duplicate request", 409, `{"error":"request_pending"}`, false},
		{"not json", 403, `policy_rejected`, false},
		{"empty body", 403, ``, false},
		{"success", 202, `{"id":"m1"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPolicyRejection(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("isPolicyRejection(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestContactRequestIntro(t *testing.T) {
	cases := []struct {
		name      string
		plaintext string
		wantIntro string
		wantWhole bool
	}{
		{"short message travels", "hi, this is bob", "hi, this is bob", true},
		{"whitespace trimmed", "  hello  ", "hello", true},
		{"empty falls back", "   ", defaultIntro, false},
		{"oversize falls back", strings.Repeat("x", maxIntroPlaintext+1), defaultIntro, false},
		{"exactly at the cap travels", strings.Repeat("x", maxIntroPlaintext), strings.Repeat("x", maxIntroPlaintext), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intro, whole := contactRequestIntro(tc.plaintext)
			if intro != tc.wantIntro || whole != tc.wantWhole {
				t.Fatalf("got (%q, %v), want (%q, %v)", truncate(intro), whole, truncate(tc.wantIntro), tc.wantWhole)
			}
		})
	}
}

func truncate(s string) string {
	if len(s) > 32 {
		return s[:32] + "…"
	}
	return s
}

func TestOffersRequest(t *testing.T) {
	rejected := []byte(`{"error":"policy_rejected"}`)
	cases := []struct {
		name  string
		offer contactRequestOffer
		want  bool
	}{
		{"plain rejected send", contactRequestOffer{status: 403, body: rejected}, true},
		{"json output never asks", contactRequestOffer{status: 403, body: rejected, jsonOut: true}, false},
		{"contact request must not loop", contactRequestOffer{status: 403, body: rejected, msgType: "sys.contact.request"}, false},
		{"contact accept must not loop", contactRequestOffer{status: 403, body: rejected, msgType: "sys.contact.accept"}, false},
		{"other sys types are system traffic too", contactRequestOffer{status: 403, body: rejected, msgType: "sys.share.granted"}, false},
		{"app types still offer", contactRequestOffer{status: 403, body: rejected, msgType: "net.poweur.tasks.assigned"}, true},
		{"non-policy failure", contactRequestOffer{status: 502, body: []byte(`{"error":"forward_failed"}`)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := offersRequest(tc.offer); got != tc.want {
				t.Fatalf("offersRequest = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfirmAnswers(t *testing.T) {
	cases := []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{" YES \n", true},
		{"n\n", false},
		{"\n", false},
		{"", false}, // EOF: a closed stdin is a "no", never a hang
		{"maybe\n", false},
	}
	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc.answer)+"/", func(t *testing.T) {
			restore := promptInput
			promptInput = strings.NewReader(tc.answer)
			defer func() { promptInput = restore }()
			var stderr bytes.Buffer
			if got := confirm(&stderr, "proceed?"); got != tc.want {
				t.Fatalf("confirm(%q) = %v, want %v", tc.answer, got, tc.want)
			}
			if !strings.Contains(stderr.String(), "proceed?") {
				t.Fatalf("question not shown: %q", stderr.String())
			}
		})
	}
}

// A declined offer must not run the contact-request path at all — the user
// said no, and `contacts request` would pin a key and write contacts.json.
func TestOfferContactRequestDeclined(t *testing.T) {
	restore := promptInput
	promptInput = strings.NewReader("n\n")
	defer func() { promptInput = restore }()

	var stdout, stderr bytes.Buffer
	sent := offerContactRequest(contactRequestOffer{
		recipient: "alice.poweur.net",
		plaintext: "hello",
		status:    403,
		body:      []byte(`{"error":"policy_rejected"}`),
	}, &stdout, &stderr)
	if sent {
		t.Fatal("declined offer must report nothing sent")
	}
	if stdout.Len() != 0 {
		t.Fatalf("declining wrote to stdout: %q", stdout.String())
	}
}

// A non-policy failure never asks anything, even with a reader waiting.
func TestOfferContactRequestIgnoresOtherFailures(t *testing.T) {
	restore := promptInput
	promptInput = strings.NewReader("y\n")
	defer func() { promptInput = restore }()

	var stdout, stderr bytes.Buffer
	if offerContactRequest(contactRequestOffer{
		recipient: "alice.poweur.net",
		plaintext: "hello",
		status:    429,
		body:      []byte(`{"error":"rate_limit_exceeded"}`),
	}, &stdout, &stderr) {
		t.Fatal("rate limiting must not become a contact request")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected prompt: %q", stderr.String())
	}
}
