package dns

import (
	"context"
	"testing"
	"time"

	"github.com/poweur/api/internal/config"
)

func TestNewProviderFactoryAndRegister(t *testing.T) {
	cfg := config.Config{}
	f := NewProviderFactory(cfg)
	if _, err := f.Provider("nope"); err == nil {
		t.Fatal("expected unsupported")
	}
	mem := &regTestProvider{}
	f.Register("mock", mem)
	p, err := f.Provider("mOcK")
	if err != nil {
		t.Fatal(err)
	}
	if p != mem {
		t.Fatal("override")
	}
	if _, err := f.Provider("cloudflare"); err != nil {
		t.Fatal("cloudflare", err)
	}
}

type regTestProvider struct{}

func (m *regTestProvider) WriteIdentityRecords(ctx context.Context, token, identity, publicKey, encryptionPublicKey, relayAddress string) error {
	return nil
}

func (m *regTestProvider) WriteEncryptionKey(ctx context.Context, token, identity, encryptionPublicKey string) error {
	return nil
}

func TestRelayRecord(t *testing.T) {
	typ, v := relayRecord("192.0.2.5")
	if typ != "A" || v != "192.0.2.5" {
		t.Fatalf("ip: %s %s", typ, v)
	}
	typ, v = relayRecord("relay.poweur.net")
	if typ != "CNAME" || v != "relay.poweur.net" {
		t.Fatalf("host: %s %s", typ, v)
	}
	typ, v = relayRecord("https://host.example:443")
	if v != "host.example" {
		t.Fatalf("https host %s %s", typ, v)
	}
	typ, v = relayRecord("h.example:8080")
	if v != "h.example" {
		t.Fatalf("host:port: %s %s", typ, v)
	}
}

func TestDefaultTTL(t *testing.T) {
	if d := defaultTTL(config.Config{DNSTTL: 0}); d != config.DefaultDNSTTL {
		t.Fatalf("%v", d)
	}
	if d := defaultTTL(config.Config{DNSTTL: 5 * 60 * 1e9}); d != 5*time.Minute {
		t.Fatalf("%v", d)
	}
}
