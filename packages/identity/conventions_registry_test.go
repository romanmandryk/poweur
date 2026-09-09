package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Extra CI validation for the conventions directory, added by the E06-T5
// dogfood (PCP-0007). The pre-existing checks in conventions_test.go validate
// registry shape + the poweur-sys schema directory. These close three holes
// the first non-system convention walked straight into:
//
//  1. schemas for any namespace other than poweur-sys were not validated at
//     all — they could have been unparseable and CI stayed green;
//  2. a registry entry could cite a PCP file that does not exist (the tasks
//     app-id was reserved with "pcp": "pcp-0007-tasks" long before the
//     document was written), which makes name-squatting look documented;
//  3. conventions/README.md's PCP table drifted from the directory (PCP-0006
//     shipped with EPIC-014 and never got a row).

type conventionsRegistry struct {
	AppIDs []struct {
		ID  string  `json:"id"`
		PCP *string `json:"pcp"`
	} `json:"app_ids"`
	MessageTypes []struct {
		Type string  `json:"type"`
		PCP  *string `json:"pcp"`
	} `json:"message_types"`
	SchemaNamespaces []struct {
		Namespace string  `json:"namespace"`
		Path      string  `json:"path"`
		PCP       *string `json:"pcp"`
	} `json:"schema_namespaces"`
}

func loadConventionsRegistry(t *testing.T) (conventionsRegistry, string) {
	t.Helper()
	dir := conventionsDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reg conventionsRegistry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("registry.json invalid: %v", err)
	}
	return reg, dir
}

// A registry entry that names a PCP must name one that exists. Otherwise a
// reservation can cite a document nobody ever wrote.
func TestConventionsRegistryPCPReferencesResolve(t *testing.T) {
	reg, dir := loadConventionsRegistry(t)
	check := func(kind, name string, pcp *string) {
		t.Helper()
		if pcp == nil || strings.TrimSpace(*pcp) == "" {
			return // explicitly unattributed is allowed
		}
		path := filepath.Join(dir, *pcp+".md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s %q cites %q but %s does not exist", kind, name, *pcp, path)
		}
	}
	for _, a := range reg.AppIDs {
		check("app id", a.ID, a.PCP)
	}
	for _, m := range reg.MessageTypes {
		check("message type", m.Type, m.PCP)
	}
	for _, n := range reg.SchemaNamespaces {
		check("schema namespace", n.Namespace, n.PCP)
	}
}

// Every claimed schema namespace must exist on disk and hold only valid,
// well-enveloped JSON Schemas — not just poweur-sys.
func TestConventionsEveryClaimedSchemaNamespaceValidates(t *testing.T) {
	reg, dir := loadConventionsRegistry(t)
	if len(reg.SchemaNamespaces) == 0 {
		t.Fatal("no schema namespaces claimed")
	}
	repoRoot := filepath.Join(dir, "..")
	for _, ns := range reg.SchemaNamespaces {
		if ns.Path == "" {
			t.Fatalf("schema namespace %q has no path", ns.Namespace)
		}
		if strings.Contains(ns.Path, "..") || strings.HasPrefix(ns.Path, "/") {
			t.Fatalf("schema namespace %q path %q must be repo-relative", ns.Namespace, ns.Path)
		}
		nsDir := filepath.Join(repoRoot, filepath.FromSlash(strings.TrimSuffix(ns.Path, "/")))
		entries, err := os.ReadDir(nsDir)
		if err != nil {
			t.Fatalf("schema namespace %q: %v", ns.Namespace, err)
		}
		if len(entries) == 0 {
			t.Fatalf("schema namespace %q is empty", ns.Namespace)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".schema.json") {
				t.Fatalf("%s: unexpected file %s", ns.Namespace, e.Name())
			}
			raw, err := os.ReadFile(filepath.Join(nsDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Schema string `json:"$schema"`
				ID     string `json:"$id"`
				Title  string `json:"title"`
				Type   string `json:"type"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("%s/%s: invalid JSON: %v", ns.Namespace, e.Name(), err)
			}
			if !strings.Contains(schema.Schema, "json-schema.org") || schema.ID == "" || schema.Title == "" {
				t.Fatalf("%s/%s: missing $schema/$id/title", ns.Namespace, e.Name())
			}
			if schema.Type == "" {
				t.Fatalf("%s/%s: missing top-level type", ns.Namespace, e.Name())
			}
		}
	}
}

// The README's PCP table is the human index of the directory; keep them in
// sync mechanically rather than by remembering.
func TestConventionsREADMEListsEveryPCP(t *testing.T) {
	dir := conventionsDir(t)
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "pcp-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		if name == "pcp-0001-process.md" {
			continue // linked from the Process section, not the seed table
		}
		if !strings.Contains(string(readme), name) {
			t.Fatalf("conventions/README.md does not mention %s", name)
		}
	}
}
