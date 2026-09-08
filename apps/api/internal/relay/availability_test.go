package relay

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/storage"
	idpkg "github.com/poweur/identity"
)

// availabilityServer builds a relay with the production-shaped name policy
// (MinLen 6), which is the configuration the reason codes are interesting
// under: "bob" is too short and "admin" is reserved *and* too short.
func availabilityServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	cfg := config.Config{
		ListenAddr:    ":0",
		RelayAddress:  "relay.test",
		RelayScheme:   "http",
		DNSTTL:        time.Minute,
		ChallengeTTL:  time.Minute,
		Version:       "test",
		HostedDomains: []string{"poweur.net"},
		RateLimits:    config.RateLimits{PerMinute: 1000, PerHour: 10000, PerDay: 100000},
		NamePolicy: idpkg.NamePolicy{
			MinLen: 6, MaxLen: 24,
			Reserved: []string{"acme"}, Blocked: []string{"spam"},
			BlockMode: idpkg.BlockModeSubstring,
		},
	}
	server := NewServer(cfg, &fakeResolver{}, dns.NewProviderFactory(cfg))
	return httptest.NewServer(server.Router()), server
}

func availability(t *testing.T, ts *httptest.Server, query string) availabilityResponse {
	t.Helper()
	resp, err := http.Get(ts.URL + "/hosted/availability?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: status %d, want 200", query, resp.StatusCode)
	}
	var body availabilityResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAvailabilityReasons(t *testing.T) {
	ts, server := availabilityServer(t)
	defer ts.Close()

	pub, _, _ := ed25519.GenerateKey(nil)
	server.identities.Add(storage.Identity{
		Identity:       "robert.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(pub),
		PublicKeyBytes: pub,
		CreatedAt:      time.Now().UTC(),
	})

	cases := []struct {
		query string
		want  idpkg.NameReason
	}{
		{"handle=melissa", idpkg.ReasonAvailable},
		{"handle=robert", idpkg.ReasonTaken},
		{"handle=admin", idpkg.ReasonReserved},
		{"handle=acme", idpkg.ReasonReserved}, // operator's own list
		{"handle=bob", idpkg.ReasonTooShort},
		{"handle=averyveryverylongwordindeedhere", idpkg.ReasonTooLong},
		{"handle=spammer", idpkg.ReasonBlocked},
		{"handle=bad_name", idpkg.ReasonCharset},
		{"handle=%D0%B0dmin", idpkg.ReasonCharset}, // Cyrillic а + dmin
		{"handle=-robert", idpkg.ReasonHyphen},
		{"handle=xn--robert", idpkg.ReasonPunycode},
		{"handle=melissa&domain=elsewhere.example", idpkg.ReasonDomainNotHosted},
	}
	for _, tc := range cases {
		body := availability(t, ts, tc.query)
		if idpkg.NameReason(body.Reason) != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.query, body.Reason, tc.want)
		}
		if body.Available != (tc.want == idpkg.ReasonAvailable) {
			t.Errorf("%s: available = %v for reason %q", tc.query, body.Available, body.Reason)
		}
		if body.Message == "" {
			t.Errorf("%s: empty message", tc.query)
		}
	}
}

// The verdict must carry the rules, so a client can validate as the user types
// without hardcoding a policy it cannot see.
func TestAvailabilityEchoesPolicy(t *testing.T) {
	ts, _ := availabilityServer(t)
	defer ts.Close()

	body := availability(t, ts, "handle=melissa")
	if body.Identity != "melissa.poweur.net" {
		t.Fatalf("identity = %q", body.Identity)
	}
	if got := body.Policy["min_len"]; got != float64(6) {
		t.Fatalf("policy.min_len = %v, want 6", got)
	}
	if got := body.Policy["charset"]; got != "a-z 0-9 -" {
		t.Fatalf("policy.charset = %v", got)
	}
}

// A reserved name that is *also* registered must still answer "reserved":
// answering "taken" would confirm that someone holds it.
func TestAvailabilityDoesNotLeakRegistrationOfPolicyRejections(t *testing.T) {
	ts, server := availabilityServer(t)
	defer ts.Close()

	pub, _, _ := ed25519.GenerateKey(nil)
	server.identities.Add(storage.Identity{
		Identity:       "admin.poweur.net",
		PublicKey:      base64.RawURLEncoding.EncodeToString(pub),
		PublicKeyBytes: pub,
		CreatedAt:      time.Now().UTC(),
	})

	if got := availability(t, ts, "handle=admin").Reason; got != string(idpkg.ReasonReserved) {
		t.Fatalf("reason = %q, want reserved", got)
	}
}

func TestAvailabilityRequiresAHandle(t *testing.T) {
	ts, _ := availabilityServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/hosted/availability")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// Cheap to probe means it has to be capped tighter than messaging: the
// endpoint charges several units per call, so a bucket sized for messages is
// exhausted proportionally sooner.
func TestAvailabilityIsRateLimitedTighterThanMessages(t *testing.T) {
	cfg := config.Config{
		ListenAddr:    ":0",
		RelayAddress:  "relay.test",
		RelayScheme:   "http",
		DNSTTL:        time.Minute,
		ChallengeTTL:  time.Minute,
		Version:       "test",
		HostedDomains: []string{"poweur.net"},
		RateLimits:    config.RateLimits{PerMinute: 8, PerHour: 1000, PerDay: 10000},
		NamePolicy:    idpkg.DefaultHostedPolicy(),
	}
	server := NewServer(cfg, &fakeResolver{}, dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	limited := false
	for i := 0; i < 8; i++ {
		resp, err := http.Get(fmt.Sprintf("%s/hosted/availability?handle=name%d", ts.URL, i))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("8 lookups against a per-minute cap of 8 were all allowed; the cost weighting is gone")
	}
}
