package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Without the digits — flag or prompt — the typed path delivers nothing.
func TestConfirmSASNeedsTheDigits(t *testing.T) {
	prev := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = prev })
	var out, errOut bytes.Buffer
	if err := confirmSAS("482193", "", "", &out, &errOut); err == nil || !strings.Contains(err.Error(), "--sas") {
		t.Fatalf("no digits = %v", err)
	}
	if err := confirmSAS("482193", "000000", "", &out, &errOut); !errors.Is(err, errPairingMismatch) {
		t.Fatalf("wrong digits = %v", err)
	}
	for _, typed := range []string{"482193", "482 193", " 482193 "} {
		if err := confirmSAS("482193", typed, "", &out, &errOut); err != nil {
			t.Fatalf("%q = %v", typed, err)
		}
	}
}

func TestPairingAppURLAndTerminalQR(t *testing.T) {
	if got := pairingAppURL("https://poweur.net", "Alice.Poweur.net"); got != "https://alice.poweur.net/app/" {
		t.Fatal(got)
	}
	if got := pairingAppURL("http://127.0.0.1:8080/", "alice.poweur.net"); got != "http://127.0.0.1:8080/app/" {
		t.Fatal(got)
	}
	var buf bytes.Buffer
	terminalQR(&buf, "https://alice.poweur.net/app/#pair=K7QM4XP2.AOEnF9JjCmt3HikT4gFtQDkhhSXV3KzkJ4Vy3xLAuOA")
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 10 || len([]rune(lines[0])) != len([]rune(lines[len(lines)-1])) {
		t.Fatalf("QR = %d lines", len(lines))
	}
}
