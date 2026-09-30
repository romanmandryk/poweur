package dns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type noResolver struct{}

func (noResolver) LookupTXT(context.Context, string) ([]string, error) { return nil, errors.New("no") }
func (noResolver) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("no")
}

func TestHostsFileResolverReadsEachLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	r := &HostsFileResolver{Path: path, Next: noResolver{}}
	if _, err := r.LookupHost(context.Background(), "alice.poweur.net"); err == nil {
		t.Fatal("missing file must fall through to Next")
	}
	if err := os.WriteFile(path, []byte(`{"alice.poweur.net":"127.0.0.1:5001"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts, err := r.LookupHost(context.Background(), "Alice.poweur.net.")
	if err != nil || len(hosts) != 1 || hosts[0] != "127.0.0.1:5001" {
		t.Fatalf("hosts = %v %v", hosts, err)
	}
	if _, err := r.LookupHost(context.Background(), "bob.poweur.net"); err == nil {
		t.Fatal("unknown names fall through to Next")
	}
}
