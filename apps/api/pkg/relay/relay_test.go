package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubResolver struct{}

func (stubResolver) LookupTXT(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (stubResolver) LookupHost(_ context.Context, _ string) ([]string, error) { return nil, nil }

// Smoke test the public re-export: NewProviderFactory, Register, NewServer, Router.
func TestRegisterAndHealth(t *testing.T) {
	cfg := Config{
		ListenAddr:   ":0",
		RelayAddress: "r.test",
		RelayScheme:  "http",
		DNSTTL:       time.Minute,
		ChallengeTTL: time.Minute,
		Version:      "t",
		RateLimits: RateLimits{
			PerMinute: 10, PerHour: 100, PerDay: 1000,
		},
	}
	f := NewProviderFactory(cfg)
	RegisterProvider(f, "mock", mockP{})
	s := NewServer(cfg, stubResolver{}, f)
	ts := httptest.NewServer(Router(s))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

type mockP struct{}

func (mockP) WriteIdentityRecords(ctx context.Context, token, identity, publicKey, encryptionPublicKey, relayAddress string) error {
	return nil
}

func (mockP) WriteEncryptionKey(ctx context.Context, token, identity, encryptionPublicKey string) error {
	return nil
}
