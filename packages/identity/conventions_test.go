package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CI validation for the conventions registry + schemas (EPIC-006 E06-T4):
// registry.json parses, claimed names are unique and well-formed, and every
// schema file is valid JSON with the expected $schema envelope.

func conventionsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "conventions")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("conventions dir not found from package identity: %v", err)
	}
	return dir
}

func TestConventionsRegistryValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(conventionsDir(t), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Version int `json:"version"`
		AppIDs  []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"app_ids"`
		MessageTypes []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"message_types"`
		SchemaNamespaces []struct {
			Namespace string `json:"namespace"`
		} `json:"schema_namespaces"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("registry.json invalid: %v", err)
	}
	if reg.Version != 1 {
		t.Fatalf("registry version: %d", reg.Version)
	}
	validStatus := map[string]bool{"reserved": true, "draft": true, "experimental": true, "stable": true}
	seen := map[string]bool{}
	for _, a := range reg.AppIDs {
		if !strings.Contains(a.ID, ".") {
			t.Fatalf("app id %q must be reverse-DNS", a.ID)
		}
		if seen[a.ID] {
			t.Fatalf("duplicate app id %q", a.ID)
		}
		seen[a.ID] = true
		if !validStatus[a.Status] {
			t.Fatalf("app id %q: bad status %q", a.ID, a.Status)
		}
	}
	for _, m := range reg.MessageTypes {
		if !strings.HasPrefix(m.Type, "sys.") {
			t.Fatalf("message type %q must be sys.*", m.Type)
		}
		if seen[m.Type] {
			t.Fatalf("duplicate message type %q", m.Type)
		}
		seen[m.Type] = true
		if !validStatus[m.Status] {
			t.Fatalf("message type %q: bad status %q", m.Type, m.Status)
		}
	}
	// Message types the code emits must be registered.
	for _, mustHave := range []string{MsgTypeContactRequest, MsgTypeContactAccept, MsgTypeContactBlock, MsgTypeAbuseReport} {
		if !seen[mustHave] {
			t.Fatalf("message type %q used in code but missing from registry.json", mustHave)
		}
	}
	if len(reg.SchemaNamespaces) == 0 {
		t.Fatal("schema_namespaces must include poweur-sys")
	}
}

func TestConventionsSchemasParse(t *testing.T) {
	dir := filepath.Join(conventionsDir(t), "schemas", "poweur-sys")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 8 {
		t.Fatalf("expected the full poweur-sys schema set, found %d files", len(entries))
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".schema.json") {
			t.Fatalf("unexpected file in schema dir: %s", e.Name())
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Schema string `json:"$schema"`
			ID     string `json:"$id"`
			Title  string `json:"title"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: invalid JSON: %v", e.Name(), err)
		}
		if !strings.Contains(schema.Schema, "json-schema.org") || schema.ID == "" || schema.Title == "" {
			t.Fatalf("%s: missing $schema/$id/title", e.Name())
		}
	}
}
