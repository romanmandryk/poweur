package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
)

// Every header the protocol sends has to survive a preflight, or the browser
// blocks the request before it is ever made.
//
// This went unnoticed because the web app is served *by* the relay: same
// origin, no preflight, no problem. A Capacitor shell on capacitor://localhost
// — and any third-party site using the SDK — is cross-origin for every call,
// and a missing X-Poweur-Challenge meant every authenticated read failed
// silently in the browser, with nothing reaching the relay to log.
func TestPreflightAllowsEveryProtocolHeader(t *testing.T) {
	cfg := config.Config{
		ListenAddr: ":0", RelayAddress: "relay.test", RelayScheme: "http",
		DNSTTL: time.Minute, ChallengeTTL: time.Minute, Version: "test",
		RateLimits: config.RateLimits{PerMinute: 100, PerHour: 1000, PerDay: 10000},
	}
	server := NewServer(cfg, &fakeResolver{}, dns.NewProviderFactory(cfg))
	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	request, err := http.NewRequest(http.MethodOptions, ts.URL+"/messages/bob.poweur.net", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "capacitor://localhost")
	request.Header.Set("Access-Control-Request-Method", "GET")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	allowed := strings.ToLower(response.Header.Get("Access-Control-Allow-Headers"))
	// The set a client actually sends: challenge-signed reads carry all four.
	for _, header := range []string{
		"x-poweur-identity", "x-poweur-challenge", "x-poweur-signature", "x-poweur-session-id",
	} {
		if !strings.Contains(allowed, header) {
			t.Errorf("preflight does not allow %s (allowed: %s)", header, allowed)
		}
	}
	if origin := response.Header.Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", origin)
	}
}
