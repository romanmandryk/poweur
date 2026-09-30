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
}
