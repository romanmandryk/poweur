package tasks

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateID(t *testing.T) {
	// ValidateID is this app's path sanitizer: every id becomes a path
	// segment on the wire, so traversal/absolute/escape shapes must be
	// rejected here rather than at the remote (AGENTS.md: storage path
	// sanitization must have unit tests).
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"simple", "tsk_abc123", true},
		{"dashes and underscores", "a-b_C-9", true},
		{"64 chars", strings.Repeat("a", 64), true},
		{"empty", "", false},
		{"65 chars", strings.Repeat("a", 65), false},
		{"dot", ".", false},
		{"parent traversal", "..", false},
		{"embedded traversal", "../../etc/passwd", false},
		{"slash", "a/b", false},
		{"backslash", `a\b`, false},
		{"absolute", "/etc/passwd", false},
		{"percent escape", "%2e%2e", false},
		{"encoded slash", "a%2Fb", false},
		{"space", "a b", false},
		{"newline", "a\nb", false},
		{"nul", "a\x00b", false},
		{"dotted json suffix", "task.json", false},
		{"unicode lookalike", "tsk_⁄etc", false},
		{"tilde", "~", false},
		{"colon", "c:", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateID(tc.id)
			if tc.ok && err != nil {
				t.Fatalf("ValidateID(%q) = %v, want nil", tc.id, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("ValidateID(%q) = nil, want an error", tc.id)
			}
		})
	}
}

// A rejected id must never reach the wire: the path builders fail closed.
func TestPathBuildersRejectUnsafeIDs(t *testing.T) {
	bad := []string{"..", "../..", "a/b", "/abs", "", strings.Repeat("x", 65)}
	for _, id := range bad {
		if _, err := projectDir(id); err == nil {
			t.Fatalf("projectDir(%q) built a path", id)
		}
		if _, err := projectDocPath(id); err == nil {
			t.Fatalf("projectDocPath(%q) built a path", id)
		}
		if _, err := taskDocPath("prj_ok", id); err == nil {
			t.Fatalf("taskDocPath(_, %q) built a path", id)
		}
		if _, err := taskDocPath(id, "tsk_ok"); err == nil {
			t.Fatalf("taskDocPath(%q, _) built a path", id)
		}
	}
	got, err := taskDocPath("prj_a", "tsk_b")
	if err != nil {
		t.Fatal(err)
	}
	want := "/apps/net.poweur.tasks/projects/prj_a/tasks/tsk_b.json"
	if got != want {
		t.Fatalf("taskDocPath = %q, want %q", got, want)
	}
}

func TestTaskValidate(t *testing.T) {
	base := func() Task {
		return Task{
			SchemaVersion: SchemaVersion,
			ID:            "tsk_1",
			Title:         "Measure the alcove",
			Status:        StatusTodo,
			Created:       "2026-09-01T09:00:00Z",
			Updated:       "2026-09-01T09:00:00Z",
		}
	}
	cases := []struct {
		name    string
		mutate  func(*Task)
		wantErr string
	}{
		{"valid", func(*Task) {}, ""},
		{"no schema_version", func(t *Task) { t.SchemaVersion = "" }, "schema_version"},
		{"no id", func(t *Task) { t.ID = "" }, "id"},
		{"unsafe id", func(t *Task) { t.ID = "../x" }, "id"},
		{"no title", func(t *Task) { t.Title = "   " }, "title"},
		{"multiline title", func(t *Task) { t.Title = "a\nb" }, "single line"},
		{"long title", func(t *Task) { t.Title = strings.Repeat("a", 1001) }, "single line"},
		{"no status", func(t *Task) { t.Status = "" }, "status"},
		{"unknown status is allowed", func(t *Task) { t.Status = "blocked" }, ""},
		{"no created", func(t *Task) { t.Created = "" }, "created"},
		{"no updated", func(t *Task) { t.Updated = "" }, "created and updated"},
		{"priority too high", func(t *Task) { t.Priority = 10 }, "priority"},
		{"priority negative", func(t *Task) { t.Priority = -1 }, "priority"},
		{"priority 9 ok", func(t *Task) { t.Priority = 9 }, ""},
		{"self parent", func(t *Task) { t.Parent = "tsk_1" }, "own parent"},
		{"unsafe parent", func(t *Task) { t.Parent = "../x" }, "parent"},
		{"too many tags", func(t *Task) {
			for i := 0; i < 33; i++ {
				t.Tags = append(t.Tags, "x")
			}
		}, "32 tags"},
		{"empty tag", func(t *Task) { t.Tags = []string{""} }, "1-64"},
		{"long tag", func(t *Task) { t.Tags = []string{strings.Repeat("t", 65)} }, "1-64"},
		{"done without completed", func(t *Task) { t.Status = StatusDone }, "completed is required"},
		{"done with completed", func(t *Task) {
			t.Status = StatusDone
			t.Completed = "2026-09-02T09:00:00Z"
		}, ""},
		{"completed while open", func(t *Task) { t.Completed = "2026-09-02T09:00:00Z" }, "must be cleared"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t2 *testing.T) {
			task := base()
			tc.mutate(&task)
			err := task.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t2.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && err == nil:
				t2.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t2.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestProjectValidate(t *testing.T) {
	base := Project{SchemaVersion: SchemaVersion, ID: "prj_1", Title: "Kitchen", Created: "2026-09-01T09:00:00Z"}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid project rejected: %v", err)
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*Project)
		wantErr string
	}{
		{"no schema_version", func(p *Project) { p.SchemaVersion = "" }, "schema_version"},
		{"unsafe id", func(p *Project) { p.ID = "../escape" }, "id"},
		{"no title", func(p *Project) { p.Title = "" }, "title"},
		{"no created", func(p *Project) { p.Created = "" }, "created"},
		{"too many tags", func(p *Project) {
			for i := 0; i < 33; i++ {
				p.Tags = append(p.Tags, "x")
			}
		}, "32 tags"},
	} {
		t.Run(tc.name, func(t2 *testing.T) {
			p := base
			tc.mutate(&p)
			err := p.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t2.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// The single most important rule in PCP-0007: a read-modify-write by this
// implementation must not delete a field written by another one.
func TestUnknownFieldsSurviveRoundTrip(t *testing.T) {
	foreign := []byte(`{
	  "schema_version": "1",
	  "id": "tsk_1",
	  "title": "Measure the alcove",
	  "status": "blocked",
	  "created": "2026-09-01T09:00:00Z",
	  "updated": "2026-09-01T09:00:00Z",
	  "x_taskwarrior_urgency": 12.5,
	  "recurrence": {"freq": "weekly", "count": 4},
	  "vendor_notes": ["one", "two"]
	}`)
	task, err := ParseTask(foreign)
	if err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
	if task.Status != "blocked" {
		t.Fatalf("status = %q, want the unknown value preserved verbatim", task.Status)
	}
	if DisplayStatus(task.Status) != StatusTodo {
		t.Fatalf("DisplayStatus(%q) = %q, want todo", task.Status, DisplayStatus(task.Status))
	}
	if len(task.Extra()) != 3 {
		t.Fatalf("Extra() = %v, want 3 preserved fields", task.Extra())
	}

	task.Title = "Measure the alcove twice"
	task.Updated = "2026-09-03T09:00:00Z"
	raw, err := task.MarshalDocument()
	if err != nil {
		t.Fatalf("MarshalDocument: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"x_taskwarrior_urgency", "recurrence", "vendor_notes"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("read-modify-write dropped %q: %s", key, raw)
		}
	}
	if got["status"] != "blocked" {
		t.Fatalf("status rewritten to %v; PCP-0007 forbids normalising an unknown status", got["status"])
	}
	if got["title"] != "Measure the alcove twice" {
		t.Fatalf("known field not updated: %v", got["title"])
	}
	if rec, ok := got["recurrence"].(map[string]any); !ok || rec["freq"] != "weekly" {
		t.Fatalf("nested unknown value mangled: %v", got["recurrence"])
	}
}

// Unknown project fields get the same treatment.
func TestProjectUnknownFieldsSurvive(t *testing.T) {
	raw := []byte(`{"schema_version":"1","id":"prj_1","title":"Kitchen",
	  "created":"2026-09-01T09:00:00Z","color":"#ff0000"}`)
	p, err := ParseProject(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.Title = "Kitchen renovation"
	out, err := p.MarshalDocument()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"color"`) {
		t.Fatalf("project read-modify-write dropped color: %s", out)
	}
}

// A known field always wins over a stale unknown copy, so the merge can never
// resurrect an old value.
func TestKnownFieldWinsOverExtra(t *testing.T) {
	task, err := NewTask("hello")
	if err != nil {
		t.Fatal(err)
	}
	task.SetExtra("title", json.RawMessage(`"stale"`))
	raw, err := task.MarshalDocument()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "hello" {
		t.Fatalf("title = %v, want the known field to win", got["title"])
	}
}

func TestParseTaskFailures(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"not json", `not json at all`},
		{"json array", `[]`},
		{"missing title", `{"schema_version":"1","id":"tsk_1","status":"todo","created":"t","updated":"t"}`},
		{"wrong type", `{"schema_version":"1","id":"tsk_1","title":5,"status":"todo","created":"t","updated":"t"}`},
		{"traversal id", `{"schema_version":"1","id":"../x","title":"t","status":"todo","created":"t","updated":"t"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t2 *testing.T) {
			if _, err := ParseTask([]byte(tc.raw)); err == nil {
				t2.Fatalf("ParseTask(%s) = nil error", tc.raw)
			}
		})
	}
}

func TestDocumentSizeCap(t *testing.T) {
	if _, err := ParseTask(make([]byte, MaxDocBytes+1)); err == nil {
		t.Fatal("oversized document accepted")
	}
	task, err := NewTask("big")
	if err != nil {
		t.Fatal(err)
	}
	task.Notes = strings.Repeat("n", MaxDocBytes)
	if _, err := task.MarshalDocument(); err == nil {
		t.Fatal("oversized marshal accepted")
	}
}

func TestSetStatusCouplesCompleted(t *testing.T) {
	task, err := NewTask("thing")
	if err != nil {
		t.Fatal(err)
	}
	task.SetStatus(StatusDone)
	if task.Completed == "" {
		t.Fatal("status=done must set completed")
	}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	done := task.Completed
	task.SetStatus(StatusDone) // idempotent: does not restamp
	if task.Completed != done {
		t.Fatal("re-setting done restamped completed")
	}
	task.SetStatus(StatusTodo)
	if task.Completed != "" {
		t.Fatal("leaving done must clear completed")
	}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNewIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := NewID("tsk_")
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateID(id); err != nil {
			t.Fatalf("minted id %q fails ValidateID: %v", id, err)
		}
		if !strings.HasPrefix(id, "tsk_") {
			t.Fatalf("id %q missing prefix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestSortTasks(t *testing.T) {
	mk := func(id, title, status string, prio int) Task {
		return Task{ID: id, Title: title, Status: status, Priority: prio}
	}
	tasks := []Task{
		mk("a", "zeta", StatusDone, 1),
		mk("b", "alpha", StatusTodo, 0),
		mk("c", "beta", StatusTodo, 3),
		mk("d", "gamma", StatusDoing, 5),
		mk("e", "delta", StatusCancelled, 1),
		mk("f", "epsilon", "blocked", 2), // unknown status sorts as todo
	}
	SortTasks(tasks)
	var order []string
	for _, task := range tasks {
		order = append(order, task.ID)
	}
	want := []string{"d", "f", "c", "b", "a", "e"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestDefaultManifestMatchesNamespace(t *testing.T) {
	m := DefaultManifest()
	if m.AppID != AppID {
		t.Fatalf("manifest app_id %q != %q", m.AppID, AppID)
	}
	if !strings.HasSuffix(NamespaceRoot, m.AppID) {
		t.Fatalf("namespace %q does not end in the app id %q", NamespaceRoot, m.AppID)
	}
	if TokenScope != "dav:rw:"+NamespaceRoot+"/" {
		t.Fatalf("token scope %q", TokenScope)
	}
}
