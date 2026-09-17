package signin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/poweur/identity"
)

func TestParseDeliveryShapes(t *testing.T) {
	const enc = "eyJwb3dldXJfYXV0aCI6IjEifQ"
	tests := []struct {
		name, contentType, body string
		want                    Delivery
	}{
		{"envelope", "application/json", `{"response":"` + enc + `"}`, Delivery{Response: enc}},
		{"envelope with match", "text/plain;charset=UTF-8", `{"response":"` + enc + `","match":" 4 2 "}`, Delivery{Response: enc, Match: "42"}},
		{"raw encoded", "text/plain", enc, Delivery{Response: enc}},
		{"bare response object", "application/json", `{"poweur_auth":"1","request_id":"r"}`, Delivery{Response: `{"poweur_auth":"1","request_id":"r"}`}},
		{"form", "application/x-www-form-urlencoded", "response=" + url.QueryEscape(enc) + "&match=07", Delivery{Response: enc, Match: "07"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			got, err := ParseDelivery(r)
			if err != nil {
				t.Fatalf("ParseDelivery: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseDeliveryRejections(t *testing.T) {
	tests := []struct {
		name, contentType, body string
	}{
		{"empty", "text/plain", "   "},
		{"form without response", "application/x-www-form-urlencoded", "match=42"},
		{"non-string match", "application/json", `{"response":"x","match":42}`},
		{"empty response string", "application/json", `{"response":"  "}`},
		{"too large", "text/plain", strings.Repeat("a", MaxDeliveryBytes+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/cb", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			if _, err := ParseDelivery(r); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestMatchCodes(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 300; i++ {
		code, err := NewMatchCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != MatchCodeDigits || NormalizeMatchCode(code) != code {
			t.Fatalf("malformed code %q", code)
		}
		seen[code] = true
	}
	if len(seen) < 20 {
		t.Fatalf("codes are not varying: %d distinct in 300", len(seen))
	}
	if !MatchCodesEqual("07", " 0-7 ") {
		t.Fatal("typed separators should be ignored")
	}
	for _, tc := range [][2]string{{"07", "70"}, {"07", ""}, {"", ""}, {"07", "7"}} {
		if MatchCodesEqual(tc[0], tc[1]) {
			t.Fatalf("MatchCodesEqual(%q, %q) = true", tc[0], tc[1])
		}
	}
}

func TestSecrets(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if a == b || len(a) < 43 {
		t.Fatalf("weak secrets %q %q", a, b)
	}
	h := HashSecret(a)
	if h == a {
		t.Fatal("hash equals the secret")
	}
	if !SecretMatches(h, a) {
		t.Fatal("secret does not match its own hash")
	}
	if SecretMatches(h, b) || SecretMatches("", a) || SecretMatches(h, "") {
		t.Fatal("mismatch accepted")
	}
}

func TestCheckResumeURI(t *testing.T) {
	const aud = "https://guestbook.poweur.net"
	ok := []string{"", "https://guestbook.poweur.net/auth/resume?code=x", "https://GUESTBOOK.poweur.net:443/r"}
	for _, u := range ok {
		if err := CheckResumeURI(aud, u); err != nil {
			t.Fatalf("CheckResumeURI(%q) = %v", u, err)
		}
	}
	bad := []string{
		"https://evil.example/auth/resume",
		"http://guestbook.poweur.net/auth/resume",
		"https://guestbook.poweur.net.evil.example/r",
		"https://user@guestbook.poweur.net/r",
		"https://guestbook.poweur.net/r#frag",
		"javascript:alert(1)",
		"/relative/only",
	}
	for _, u := range bad {
		err := CheckResumeURI(aud, u)
		if err == nil {
			t.Fatalf("CheckResumeURI(%q) accepted", u)
		}
		if !errors.Is(err, identity.ErrSignInAudience) {
			t.Fatalf("CheckResumeURI(%q) = %v, want ErrSignInAudience", u, err)
		}
	}
}
