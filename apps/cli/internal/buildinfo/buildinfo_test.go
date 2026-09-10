package buildinfo

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteIncludesVersion(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf)
	out := buf.String()
	if !strings.Contains(out, Version) {
		t.Fatalf("version output %q does not contain %q", out, Version)
	}
	if !strings.HasPrefix(out, "poweur ") {
		t.Fatalf("version output %q, want poweur prefix", out)
	}
}

func TestFormatTimeUTC(t *testing.T) {
	got := FormatTime("2026-09-10T13:05:00+01:00")
	if got != "2026-09-10 12:05" {
		t.Fatalf("got %q", got)
	}
}
