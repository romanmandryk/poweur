// Package tasks is the reference implementation of PCP-0007 (tasks &
// projects, app-id net.poweur.tasks).
//
// It reads and writes the convention through a Poweur home's WebDAV endpoint
// using nothing but the standard library, and is the thing E06-T5 uses to find
// out whether the convention as written is actually implementable.
package tasks

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AppID is the reverse-DNS namespace claimed in conventions/registry.json.
const AppID = "net.poweur.tasks"

// SchemaVersion is PCP-0007's document version. It is a string on purpose:
// readers that meet a major version they do not know present the document
// read-only rather than guess.
const SchemaVersion = "1"

// MaxDocBytes caps a single task or project document (PCP-0007), matching the
// poweur-sys system-document cap.
const MaxDocBytes = 64 * 1024

// Task status vocabulary (closed set; maps to VTODO STATUS).
const (
	StatusTodo      = "todo"
	StatusDoing     = "doing"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

// KnownStatus reports whether s is in PCP-0007's vocabulary. An unknown status
// is *not* an error: readers display it as todo and preserve it on write.
func KnownStatus(s string) bool {
	switch s {
	case StatusTodo, StatusDoing, StatusDone, StatusCancelled:
		return true
	}
	return false
}

// DisplayStatus maps any status onto something renderable. PCP-0007
// compatibility rule: unknown values render as todo.
func DisplayStatus(s string) string {
	if KnownStatus(s) {
		return s
	}
	return StatusTodo
}

// Project is /apps/net.poweur.tasks/projects/<id>/project.json.
type Project struct {
	SchemaVersion string   `json:"schema_version"`
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	Created       string   `json:"created"`
	Updated       string   `json:"updated,omitempty"`
	Archived      bool     `json:"archived,omitempty"`
	Tags          []string `json:"tags,omitempty"`

	// extra holds every top-level field this implementation does not know.
	// PCP-0007 requires them to survive a read-modify-write; dropping them is
	// how the second implementation of a convention eats the first one's data.
	extra map[string]json.RawMessage
}

// Task is /apps/net.poweur.tasks/projects/<pid>/tasks/<id>.json.
type Task struct {
	SchemaVersion string   `json:"schema_version"`
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Status        string   `json:"status"`
	Created       string   `json:"created"`
	Updated       string   `json:"updated"`
	Notes         string   `json:"notes,omitempty"`
	Due           string   `json:"due,omitempty"`
	Start         string   `json:"start,omitempty"`
	Priority      int      `json:"priority,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Parent        string   `json:"parent,omitempty"`
	Assignee      string   `json:"assignee,omitempty"`
	Completed     string   `json:"completed,omitempty"`

	extra map[string]json.RawMessage
}

// Extra exposes the preserved unknown fields (test/introspection helper).
func (t Task) Extra() map[string]json.RawMessage { return t.extra }

// Extra exposes the preserved unknown fields (test/introspection helper).
func (p Project) Extra() map[string]json.RawMessage { return p.extra }

// SetExtra records an unknown field, as a foreign implementation's writer
// would. Exported so tests can build documents from the "other" side.
func (t *Task) SetExtra(key string, raw json.RawMessage) {
	if t.extra == nil {
		t.extra = map[string]json.RawMessage{}
	}
	t.extra[key] = raw
}

// knownFields returns the JSON names this implementation understands, derived
// from the struct tags so the two can never drift.
func knownFields(v any) map[string]bool {
	out := map[string]bool{}
	raw, err := json.Marshal(v)
	if err != nil {
		return out
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	for k := range m {
		out[k] = true
	}
	return out
}

// taskFieldNames / projectFieldNames are the full tag sets (marshalling a
// zero value would omit every omitempty field, so use a filled probe).
var taskFieldNames = knownFields(Task{
	SchemaVersion: "x", ID: "x", Title: "x", Status: "x", Created: "x", Updated: "x",
	Notes: "x", Due: "x", Start: "x", Priority: 1, Tags: []string{"x"},
	Parent: "x", Assignee: "x", Completed: "x",
})

var projectFieldNames = knownFields(Project{
	SchemaVersion: "x", ID: "x", Title: "x", Description: "x", Created: "x",
	Updated: "x", Archived: true, Tags: []string{"x"},
})

func splitUnknown(raw []byte, known map[string]bool) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	extra := map[string]json.RawMessage{}
	for k, v := range all {
		if !known[k] {
			extra[k] = v
		}
	}
	if len(extra) == 0 {
		return nil, nil
	}
	return extra, nil
}

func mergeExtra(raw []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k, v := range extra {
		if _, clash := m[k]; clash {
			continue // a known field always wins over a stale unknown copy
		}
		m[k] = v
	}
	return json.MarshalIndent(m, "", "  ")
}

// ParseTask decodes a task document, validates it, and keeps unknown fields.
func ParseTask(raw []byte) (Task, error) {
	if len(raw) > MaxDocBytes {
		return Task{}, fmt.Errorf("task document exceeds %d bytes", MaxDocBytes)
	}
	var t Task
	if err := json.Unmarshal(raw, &t); err != nil {
		return Task{}, fmt.Errorf("invalid task document: %w", err)
	}
	extra, err := splitUnknown(raw, taskFieldNames)
	if err != nil {
		return Task{}, fmt.Errorf("invalid task document: %w", err)
	}
	t.extra = extra
	if err := t.Validate(); err != nil {
		return Task{}, err
	}
	return t, nil
}

// MarshalDocument renders the task, re-attaching preserved unknown fields.
func (t Task) MarshalDocument() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, err
	}
	out, err := mergeExtra(raw, t.extra)
	if err != nil {
		return nil, err
	}
	if len(out) > MaxDocBytes {
		return nil, fmt.Errorf("task document exceeds %d bytes", MaxDocBytes)
	}
	return out, nil
}

// Validate enforces the required fields and the closed-set rules that are
// this implementation's business. It deliberately does NOT reject an unknown
// status: PCP-0007 says preserve and display as todo.
func (t Task) Validate() error {
	if t.SchemaVersion == "" {
		return fmt.Errorf("schema_version is required")
	}
	if err := ValidateID(t.ID); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if len(t.Title) > 1000 || strings.ContainsAny(t.Title, "\n\r") {
		return fmt.Errorf("title must be a single line of at most 1000 characters")
	}
	if t.Status == "" {
		return fmt.Errorf("status is required")
	}
	if t.Created == "" || t.Updated == "" {
		return fmt.Errorf("created and updated are required")
	}
	if t.Priority < 0 || t.Priority > 9 {
		return fmt.Errorf("priority must be 0-9")
	}
	if t.Parent != "" {
		if err := ValidateID(t.Parent); err != nil {
			return fmt.Errorf("parent: %w", err)
		}
		if t.Parent == t.ID {
			return fmt.Errorf("parent: a task cannot be its own parent")
		}
	}
	if len(t.Tags) > 32 {
		return fmt.Errorf("at most 32 tags")
	}
	for _, tag := range t.Tags {
		if tag == "" || len(tag) > 64 {
			return fmt.Errorf("tag %q: must be 1-64 characters", tag)
		}
	}
	if t.Status == StatusDone && t.Completed == "" {
		return fmt.Errorf("completed is required when status is done")
	}
	if t.Status != StatusDone && t.Completed != "" {
		return fmt.Errorf("completed must be cleared when status is not done")
	}
	return nil
}

// ParseProject decodes a project document, preserving unknown fields.
func ParseProject(raw []byte) (Project, error) {
	if len(raw) > MaxDocBytes {
		return Project{}, fmt.Errorf("project document exceeds %d bytes", MaxDocBytes)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return Project{}, fmt.Errorf("invalid project document: %w", err)
	}
	extra, err := splitUnknown(raw, projectFieldNames)
	if err != nil {
		return Project{}, fmt.Errorf("invalid project document: %w", err)
	}
	p.extra = extra
	if err := p.Validate(); err != nil {
		return Project{}, err
	}
	return p, nil
}

// MarshalDocument renders the project, re-attaching unknown fields.
func (p Project) MarshalDocument() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return mergeExtra(raw, p.extra)
}

// Validate enforces project.json's required fields.
func (p Project) Validate() error {
	if p.SchemaVersion == "" {
		return fmt.Errorf("schema_version is required")
	}
	if err := ValidateID(p.ID); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if len(p.Title) > 1000 {
		return fmt.Errorf("title must be at most 1000 characters")
	}
	if p.Created == "" {
		return fmt.Errorf("created is required")
	}
	if len(p.Tags) > 32 {
		return fmt.Errorf("at most 32 tags")
	}
	return nil
}

// ValidateID enforces PCP-0007's opaque-id rule. It is also this app's path
// sanitizer: every id becomes a path segment on the remote, so an id that
// could escape its directory ("..", "a/b", an absolute path, a percent
// escape) must never reach the wire.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(id) > 64 {
		return fmt.Errorf("must be at most 64 characters")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
		default:
			return fmt.Errorf("must match [A-Za-z0-9_-] (got %q)", id)
		}
	}
	return nil
}

var idAlphabet = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID mints an opaque id with the given prefix and 128 bits of randomness.
func NewID(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + idAlphabet.EncodeToString(buf), nil
}

// Now is the timestamp format every document in this convention uses.
func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// NewTask builds a valid, minimal task.
func NewTask(title string) (Task, error) {
	id, err := NewID("tsk_")
	if err != nil {
		return Task{}, err
	}
	now := Now()
	t := Task{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Title:         strings.TrimSpace(title),
		Status:        StatusTodo,
		Created:       now,
		Updated:       now,
	}
	return t, t.Validate()
}

// NewProject builds a valid, minimal project.
func NewProject(title string) (Project, error) {
	id, err := NewID("prj_")
	if err != nil {
		return Project{}, err
	}
	now := Now()
	p := Project{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Title:         strings.TrimSpace(title),
		Created:       now,
		Updated:       now,
	}
	return p, p.Validate()
}

// SetStatus applies PCP-0007's completed/status coupling and bumps updated.
func (t *Task) SetStatus(status string) {
	t.Status = status
	if status == StatusDone {
		if t.Completed == "" {
			t.Completed = Now()
		}
	} else {
		t.Completed = ""
	}
	t.Updated = Now()
}

// SortTasks orders a listing the way a human reads one: open work first,
// then by priority (1 highest, 0 = undefined sorts last), then by title.
func SortTasks(ts []Task) {
	rank := map[string]int{StatusDoing: 0, StatusTodo: 1, StatusDone: 2, StatusCancelled: 3}
	prio := func(p int) int {
		if p == 0 {
			return 10
		}
		return p
	}
	sort.SliceStable(ts, func(i, j int) bool {
		ri, ok := rank[DisplayStatus(ts[i].Status)]
		if !ok {
			ri = 1
		}
		rj, ok := rank[DisplayStatus(ts[j].Status)]
		if !ok {
			rj = 1
		}
		if ri != rj {
			return ri < rj
		}
		if pi, pj := prio(ts[i].Priority), prio(ts[j].Priority); pi != pj {
			return pi < pj
		}
		return ts[i].Title < ts[j].Title
	})
}

// Manifest is /apps/net.poweur.tasks/manifest.json (E06-T3). The relay
// validates this document on write and rejects an app_id that does not match
// the directory, so the app writes it before anything else.
type Manifest struct {
	AppID         string `json:"app_id"`
	Name          string `json:"name"`
	Vendor        string `json:"vendor,omitempty"`
	SchemaVersion string `json:"schema_version,omitempty"`
	DocsURL       string `json:"docs_url,omitempty"`
}

// DefaultManifest is what `poweur-tasks init` writes.
func DefaultManifest() Manifest {
	return Manifest{
		AppID:         AppID,
		Name:          "Poweur Tasks",
		Vendor:        "poweur core",
		SchemaVersion: SchemaVersion,
		DocsURL:       "https://github.com/romanmandryk/poweur/blob/master/conventions/pcp-0007-tasks.md",
	}
}
