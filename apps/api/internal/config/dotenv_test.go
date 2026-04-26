package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvMissingFile(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDotEnvReadsKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.env")
	content := "RELAY_ADDRESS=fromfile.test\n" +
		"\n" +
		"# comment\n" +
		"LISTEN_ADDR=:4000\n" +
		"QUOTED=\"hello\"\n" +
		"INVALID\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("RELAY_ADDRESS")
		_ = os.Unsetenv("LISTEN_ADDR")
		_ = os.Unsetenv("QUOTED")
	})
	_ = os.Setenv("LISTEN_ADDR", "already") // not overwritten
	if err := LoadDotEnv(p); err != nil {
		t.Fatal(err)
	}
	if v := os.Getenv("RELAY_ADDRESS"); v != "fromfile.test" {
		t.Fatalf("RELAY_ADDRESS %q", v)
	}
	if os.Getenv("LISTEN_ADDR") != "already" {
		t.Fatal("should not override existing")
	}
	if os.Getenv("QUOTED") != "hello" {
		t.Fatal("quoted")
	}
}
