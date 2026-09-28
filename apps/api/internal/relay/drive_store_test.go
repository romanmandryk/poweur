package relay

import (
	"testing"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
)

func TestDriveStoreFollowsDataDir(t *testing.T) {
	cfg := config.Config{ListenAddr: ":0", RelayAddress: "relay.test", DataDir: t.TempDir(), StorageProvider: config.StorageFS}
	server := NewServer(cfg, &fakeResolver{}, dns.NewProviderFactory(cfg))
	if server.DriveError() != nil || server.Drive() == nil {
		t.Fatal(server.DriveError())
	}
	memory := cfg
	memory.DataDir = ""
	server = NewServer(memory, &fakeResolver{}, dns.NewProviderFactory(memory))
	if server.DriveError() != nil || server.Drive() != nil {
		t.Fatalf("memory relay store: %v", server.DriveError())
	}
}
