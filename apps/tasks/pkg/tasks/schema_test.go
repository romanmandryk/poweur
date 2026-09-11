package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The dogfood's sharpest finding was that PCP-0007's prose and PCP-0007's own
// JSON Schema disagreed — the prose required an unknown `status` to be
// preserved, the schema had it as a closed `enum`, and the reference
// implementation produced documents the convention's own schema rejected.
// Nothing in the PCP-0001 process checks a document against its schema, and
// nothing checked either against an implementation.
//
// These tests are that check, from the implementation's side: the published
// schema and this package have to describe the same format.

type jsonSchema struct {
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
}

func loadSchema(t *testing.T, name string) jsonSchema {
	t.Helper()
	// apps/tasks/pkg/tasks -> repo root
	path := filepath.Join("..", "..", "..", "..", "conventions", "schemas", AppID, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		// The app is a standalone module by design; if it is ever consumed
		// outside this repo the schema simply is not next door.
		t.Skipf("published schema not available at %s: %v", path, err)
	}
	var s jsonSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return s
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func schemaProps(s jsonSchema) map[string]bool {
	out := map[string]bool{}
	for k := range s.Properties {
		out[k] = true
	}
	return out
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// A field in the schema that this package does not implement would be
// silently dropped on every read-modify-write... except that it would land in
// `extra` instead, i.e. it would work by accident and drift forever. A field
// this package writes that the schema does not document is worse: a second
// implementation validating strictly would reject our documents.
func TestSchemaAndImplementationAgreeOnFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   string
		fields map[string]bool
	}{
		{"task", "task.schema.json", taskFieldNames},
		{"project", "project.schema.json", projectFieldNames},
	} {
		t.Run(tc.name, func(t2 *testing.T) {
			s := loadSchema(t2, tc.file)
			props := schemaProps(s)
			if missing := diff(tc.fields, props); len(missing) > 0 {
				t2.Fatalf("this package writes fields the published schema does not document: %v\n"+
					"schema has: %v", missing, keys(props))
			}
			if unimplemented := diff(props, tc.fields); len(unimplemented) > 0 {
				t2.Fatalf("the published schema documents fields this package does not implement: %v",
					unimplemented)
			}
		})
	}
}

// The schema's `required` list and Validate() must not disagree about what a
// minimal document is.
func TestSchemaRequiredMatchesValidate(t *testing.T) {
	taskSchema := loadSchema(t, "task.schema.json")
	wantTask := []string{"created", "id", "schema_version", "status", "title", "updated"}
	got := append([]string(nil), taskSchema.Required...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(wantTask, ",") {
		t.Fatalf("task schema required = %v, want %v", got, wantTask)
	}
	// Every one of them is genuinely enforced by this implementation.
	for _, field := range wantTask {
		if err := taskMissing(field).Validate(); err == nil {
			t.Fatalf("Validate() accepts a task with no %s, but the schema requires it", field)
		}
	}

	projectSchema := loadSchema(t, "project.schema.json")
	wantProject := []string{"created", "id", "schema_version", "title"}
	got = append([]string(nil), projectSchema.Required...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(wantProject, ",") {
		t.Fatalf("project schema required = %v, want %v", got, wantProject)
	}
}

func taskMissing(field string) Task {
	t := Task{
		SchemaVersion: SchemaVersion,
		ID:            "tsk_1",
		Title:         "t",
		Status:        StatusTodo,
		Created:       "2026-09-01T09:00:00Z",
		Updated:       "2026-09-01T09:00:00Z",
	}
	switch field {
	case "schema_version":
		t.SchemaVersion = ""
	case "id":
		t.ID = ""
	case "title":
		t.Title = ""
	case "status":
		t.Status = ""
	case "created":
		t.Created = ""
	case "updated":
		t.Updated = ""
	}
	return t
}

// Regression guard for the contradiction itself: `status` must stay open, or
// the schema starts rejecting the documents the prose requires readers to
// accept and preserve.
func TestSchemaStatusIsNotAClosedEnum(t *testing.T) {
	s := loadSchema(t, "task.schema.json")
	raw, ok := s.Properties["status"]
	if !ok {
		t.Fatal("task schema has no status property")
	}
	var status struct {
		Enum []string `json:"enum"`
		Type any      `json:"type"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Enum) > 0 {
		t.Fatalf("task.schema.json constrains status to %v, but PCP-0007 requires an unknown "+
			"status to be valid, displayed as todo and preserved verbatim on write. "+
			"A validator built from this schema would reject documents the prose mandates.", status.Enum)
	}
	if status.Type != "string" {
		t.Fatalf("status type = %v, want string", status.Type)
	}
}
