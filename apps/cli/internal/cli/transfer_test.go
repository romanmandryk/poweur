package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransferCreateValidation(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing subcommand", nil, "usage:"},
		{"unknown subcommand", []string{"send"}, "usage:"},
		{"missing file", []string{"create"}, "usage:"},
		{"negative downloads", []string{"create", "missing", "--max-downloads=-1"}, "cannot be negative"},
		{"bad expiry", []string{"create", "missing", "--expires=tomorrow"}, "future RFC3339"},
		{"empty file", []string{"create", empty}, "must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runTransfer(tc.args, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("code/output = %d, %q; want %q", code, stderr.String(), tc.want)
			}
		})
	}
}
