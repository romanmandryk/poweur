package dns

import (
	"context"
	"testing"

	"github.com/poweur/api/internal/config"
)

func TestMemoryProviderWriteAndRead(t *testing.T) {
	cfg := config.Config{}
	p := NewMemoryProvider(cfg)
	ctx := context.Background()
	if err := p.WriteIdentityRecords(ctx, "t", "id1.test", "pk1", "enc1", "relay:8080"); err != nil {
		t.Fatal(err)
	}
	if v, ok := p.Record("id1.test"); !ok || v == "" {
		t.Fatalf("record %q %v", v, ok)
	}
	if err := p.WriteEncryptionKey(ctx, "t", "id1.test", "enc2"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryProviderWriteEncEmptyFails(t *testing.T) {
	p := NewMemoryProvider(config.Config{})
	err := p.WriteEncryptionKey(context.Background(), "t", "x.test", "")
	if err == nil {
		t.Fatal("expected error")
	}
}
