package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDriveConflictReport(t *testing.T) {
	c := &driveConflict{Path: "/Docs/a/doc.md", Base: "v1", Head: "v2"}
	raw, err := json.Marshal(c)
	if err != nil || string(raw) != `{"error":"conflict","path":"/Docs/a/doc.md","base":"v1","head":"v2"}` {
		t.Fatalf("json = %s %v", raw, err)
	}
	if !strings.Contains(c.Error(), "changed since v1 (now v2)") {
		t.Fatal(c.Error())
	}
}

func TestLinkGetRejectsNonLinks(t *testing.T) {
	for _, raw := range []string{
		"https://alice.poweur.net/pub/x",
		"https://alice.poweur.net/s/0123456789abcdef0123456789abcdef",       // no secret
		"https://alice.poweur.net/s/0123456789abcdef0123456789abcdef#short", // wrong length
		"https://alice.poweur.net/s/not-hex#AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if _, err := runLinkGet(context.Background(), raw, "", "", t.TempDir()+"/out", nil); err == nil || !strings.Contains(err.Error(), "not a drive link") {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}
