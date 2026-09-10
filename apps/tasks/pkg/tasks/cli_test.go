package tasks

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type cliFixture struct {
	dav *fakeDAV
	ts  *httptest.Server
	t   *testing.T
}

func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	// Run() reads POWEUR_TASKS_* first; clear them so a developer's shell
	// cannot change what the tests assert.
	t.Setenv("POWEUR_TASKS_RELAY", "")
	t.Setenv("POWEUR_TASKS_OWNER", "")
	t.Setenv("POWEUR_TASKS_TOKEN", "")
	f, ts := newFakeDAV(t, "alice.example.org")
	f.token = "tok"
	f.scope = NamespaceRoot + "/"
	return &cliFixture{dav: f, ts: ts, t: t}
}

func (f *cliFixture) run(args ...string) (int, string, string) {
	f.t.Helper()
	var stdout, stderr bytes.Buffer
	global := []string{"--relay", f.ts.URL, "--owner", "alice.example.org", "--token", "tok"}
	code := Run(append(global, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (f *cliFixture) mustRun(args ...string) string {
	f.t.Helper()
	code, out, errOut := f.run(args...)
	if code != 0 {
		f.t.Fatalf("poweur-tasks %v exited %d: %s%s", args, code, out, errOut)
	}
	return out
}

func TestCLIFullJourney(t *testing.T) {
	f := newCLIFixture(t)

	if out := f.mustRun("init"); !strings.Contains(out, NamespaceRoot) {
		t.Fatalf("init output %q", out)
	}
	if out := f.mustRun("projects"); !strings.Contains(out, "no projects") {
		t.Fatalf("empty projects output %q", out)
	}

	var project Project
	if err := json.Unmarshal([]byte(f.mustRun("--json", "project", "new", "Kitchen renovation")), &project); err != nil {
		t.Fatal(err)
	}
	if project.Title != "Kitchen renovation" || project.ID == "" {
		t.Fatalf("project = %+v", project)
	}

	var task Task
	if err := json.Unmarshal([]byte(f.mustRun("--json", "add", project.ID, "Measure the alcove")), &task); err != nil {
		t.Fatal(err)
	}

	out := f.mustRun("list", project.ID)
	if !strings.Contains(out, "[ ]") || !strings.Contains(out, "Measure the alcove") {
		t.Fatalf("list output %q", out)
	}

	f.mustRun("set", project.ID, task.ID, "priority=1", "due=2026-09-12", "tags=measuring,home", "assignee=bob.example.org")
	out = f.mustRun("list", project.ID)
	for _, want := range []string{"p1", "due 2026-09-12", "#measuring", "@bob.example.org"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output %q missing %q", out, want)
		}
	}

	f.mustRun("done", project.ID, task.ID)
	var after Task
	if err := json.Unmarshal([]byte(f.mustRun("--json", "list", project.ID)), &[]Task{}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(f.dav.get(mustTaskPath(t, project.ID, task.ID)), &after); err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusDone || after.Completed == "" {
		t.Fatalf("done did not set status+completed: %+v", after)
	}

	if out := f.mustRun("show", project.ID, task.ID); !strings.Contains(out, `"status": "done"`) {
		t.Fatalf("show output %q", out)
	}
	f.mustRun("rm", project.ID, task.ID)
	if out := f.mustRun("list", project.ID); !strings.Contains(out, "no tasks") {
		t.Fatalf("list after rm %q", out)
	}
}

func mustTaskPath(t *testing.T, projectID, taskID string) string {
	t.Helper()
	p, err := taskDocPath(projectID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The CLI's read-modify-write is the operation the unknown-field rule exists
// for. Another implementation's fields must survive `set`.
func TestCLISetPreservesForeignFields(t *testing.T) {
	f := newCLIFixture(t)
	f.mustRun("init")
	var project Project
	if err := json.Unmarshal([]byte(f.mustRun("--json", "project", "new", "P")), &project); err != nil {
		t.Fatal(err)
	}
	path := mustTaskPath(t, project.ID, "tsk_foreign")
	f.dav.put(path, []byte(`{
	  "schema_version":"1","id":"tsk_foreign","title":"Written elsewhere",
	  "status":"blocked","created":"2026-09-01T09:00:00Z","updated":"2026-09-01T09:00:00Z",
	  "x_other_app":{"colour":"red"}}`))

	if out := f.mustRun("list", project.ID); !strings.Contains(out, "unknown, shown as todo") {
		t.Fatalf("an unknown status should be surfaced, not hidden: %q", out)
	}

	f.mustRun("set", project.ID, "tsk_foreign", "priority=2")
	var got map[string]any
	if err := json.Unmarshal(f.dav.get(path), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["x_other_app"]; !ok {
		t.Fatalf("set dropped the foreign field: %v", got)
	}
	if got["status"] != "blocked" {
		t.Fatalf("set normalised an unknown status to %v", got["status"])
	}
	if got["priority"] != float64(2) {
		t.Fatalf("priority not applied: %v", got["priority"])
	}
	if got["updated"] == "2026-09-01T09:00:00Z" {
		t.Fatal("writers MUST bump updated on every write")
	}
}

func TestCLIFailureModes(t *testing.T) {
	f := newCLIFixture(t)
	f.mustRun("init")
	var project Project
	if err := json.Unmarshal([]byte(f.mustRun("--json", "project", "new", "P")), &project); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		args     []string
		wantErr  string
		wantCode int
	}{
		{"unknown command", []string{"frobnicate"}, "unknown command", 1},
		{"add without a title", []string{"add", project.ID}, "usage", 1},
		{"list without a project", []string{"list"}, "usage", 1},
		{"set without pairs", []string{"set", project.ID, "tsk_x"}, "usage", 1},
		{"set malformed pair", []string{"set", project.ID, "tsk_x", "nope"}, "key=value", 1},
		{"set unknown field", []string{"set", project.ID, "tsk_x", "colour=red"}, "unknown field", 1},
		{"set invalid status", []string{"set", project.ID, "tsk_x", "status=whatever"}, "status must be one of", 1},
		{"set non-numeric priority", []string{"set", project.ID, "tsk_x", "priority=high"}, "integer", 1},
		{"missing task", []string{"show", project.ID, "tsk_nope"}, "init", 1},
		{"traversal project id", []string{"list", "../../poweur-sys"}, "project id", 1},
		{"traversal task id", []string{"show", project.ID, "../../../id"}, "task id", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t2 *testing.T) {
			code, _, errOut := f.run(tc.args...)
			if code != tc.wantCode {
				t2.Fatalf("exit %d, want %d (stderr: %s)", code, tc.wantCode, errOut)
			}
			if !strings.Contains(errOut, tc.wantErr) {
				t2.Fatalf("stderr %q, want it to contain %q", errOut, tc.wantErr)
			}
		})
	}
}

// A bad `set` argument must be rejected before the read-modify-write starts,
// so a typo cannot leave the caller wondering whether a partial write landed.
func TestCLISetValidatesBeforeFetching(t *testing.T) {
	f := newCLIFixture(t)
	f.mustRun("init")
	var project Project
	if err := json.Unmarshal([]byte(f.mustRun("--json", "project", "new", "P")), &project); err != nil {
		t.Fatal(err)
	}
	task, err := NewTask("real task")
	if err != nil {
		t.Fatal(err)
	}
	f.dav.put(mustTaskPath(t, project.ID, task.ID), []byte(`{"schema_version":"1","id":"`+task.ID+
		`","title":"real task","status":"todo","created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}`))

	before := len(f.dav.methodCalls("GET")) + len(f.dav.methodCalls("PUT"))
	if code, _, errOut := f.run("set", project.ID, task.ID, "priority=1", "colour=red"); code != 1 {
		t.Fatalf("exit %d (%s)", code, errOut)
	}
	after := len(f.dav.methodCalls("GET")) + len(f.dav.methodCalls("PUT"))
	if after != before {
		t.Fatalf("a rejected `set` made %d requests; validate arguments first", after-before)
	}
	// The valid pair in the same command must not have been applied either.
	var stored Task
	if err := json.Unmarshal(f.dav.get(mustTaskPath(t, project.ID, task.ID)), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Priority != 0 {
		t.Fatal("a partially-applied set wrote priority despite the later argument failing")
	}
}

// Missing configuration is the first thing a new user hits; it must name the
// command that fixes it rather than fail with a URL parse error.
func TestCLIMissingConfigurationIsExplained(t *testing.T) {
	t.Setenv("POWEUR_TASKS_RELAY", "")
	t.Setenv("POWEUR_TASKS_OWNER", "")
	t.Setenv("POWEUR_TASKS_TOKEN", "")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no relay", []string{"init"}, "relay is required"},
		{"no owner", []string{"--relay", "https://r", "init"}, "owner identity is required"},
		{"no token", []string{"--relay", "https://r", "--owner", "a.example.org", "init"}, "poweur dav token --scope " + TokenScope},
	} {
		t.Run(tc.name, func(t2 *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tc.args, &stdout, &stderr); code != 1 {
				t2.Fatalf("exit %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t2.Fatalf("stderr %q, want %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestCLIEnvConfiguration(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	f.token = "tok"
	t.Setenv("POWEUR_TASKS_RELAY", ts.URL)
	t.Setenv("POWEUR_TASKS_OWNER", "alice.example.org")
	t.Setenv("POWEUR_TASKS_TOKEN", "tok")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if f.get(NamespaceRoot+"/manifest.json") == nil {
		t.Fatal("env-configured run did not write the manifest")
	}
}

func TestCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{{}, {"help"}} {
		stdout.Reset()
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("help exit %d", code)
		}
		if !strings.Contains(stdout.String(), TokenScope) {
			t.Fatalf("help must tell the user how to get a token: %q", stdout.String())
		}
	}
}
