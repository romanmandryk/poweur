package dns

import (
	"context"
	"fmt"
	"sync"

	"github.com/poweur/api/internal/config"
)

type MemoryProvider struct {
	cfg     config.Config
	mu      sync.Mutex
	records map[string]string
}

func NewMemoryProvider(cfg config.Config) *MemoryProvider {
	return &MemoryProvider{
		cfg:     cfg,
		records: make(map[string]string),
	}
}

func (p *MemoryProvider) WriteIdentityRecords(ctx context.Context, token, identity, publicKey, encryptionPublicKey, relayAddress string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records[fmt.Sprintf("_poweur.%s", identity)] = fmt.Sprintf("poweur-pubkey=ed25519:%s", publicKey)
	if encryptionPublicKey != "" {
		p.records[fmt.Sprintf("_poweur-enc.%s", identity)] = fmt.Sprintf("poweur-enckey=x25519:%s", encryptionPublicKey)
	}
	p.records[identity] = relayAddress
	return nil
}

func (p *MemoryProvider) Record(name string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value, ok := p.records[name]
	return value, ok
}
