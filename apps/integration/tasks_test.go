// Integration tests for the tasks-domain dogfood (E06-T5 / PCP-0007): the
// `poweur-tasks` reference app driven against a real relay, with two real
// identities collaborating on a shared project through nothing but the
// published convention, an EPIC-005 share and a path-scoped DAV token.
//
// This is the ecosystem's hello-world: no server code, no app-specific relay
// endpoint, and the app imports no Poweur internals — only the standard
// library. If these tests need a special case in the relay, the convention
// has failed.
package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	clipkg "github.com/poweur/cli/pkg/cli"
	taskspkg "github.com/poweur/tasks/pkg/tasks"
)

// runTasksApp drives the reference app's single entry point, the same one its
// binary calls.
func runTasksApp(t *testing.T, relay, owner, token string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	global := []string{"--relay", relay, "--owner", owner, "--token", token}
	code := taskspkg.Run(append(global, args...), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func mustRunTasksApp(t *testing.T, relay, owner, token string, args ...string) string {
	t.Helper()
	stdout, stderr, code := runTasksApp(t, relay, owner, token, args...)
	if code != 0 {
		t.Fatalf("poweur-tasks %v exited %d:\n%s%s", args, code, stdout, stderr)
	}
	return stdout
}

// TestINT_TASKS_01: the full E06-T5 acceptance — alice runs the reference app
// against her home, shares one project directory with bob, and bob's instance
// of the same app reads and writes the same task documents through the grant.
func TestINT_TASKS_01_SharedProjectCollaboration(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("taskalice.poweur.net", addr)
	zone.SetHost("taskbob.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome := t.TempDir()
	bobHome := t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "taskalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "taskbob.poweur.net", "--hosted", "--relay", relayURL, "--json")

	// The app never mints its own credential: the user does, scoped to
	// exactly the app's namespace, with the first-party CLI.
	aliceToken := mintTokenViaCLI(t, aliceHome,
		"--use-identity", "taskalice.poweur.net", "--scope", taskspkg.TokenScope)

	// --- alice sets up the namespace and a project -----------------------
	mustRunTasksApp(t, relayURL, "taskalice.poweur.net", aliceToken, "init")

	// E06-T3: the manifest is mandatory and the relay validated it on write.
	base := relayURL + "/dav/taskalice.poweur.net"
	resp := davDo(t, http.MethodGet, base+"/apps/net.poweur.tasks/manifest.json", aliceToken, nil, nil)
	rawManifest, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest.json after init: %d", resp.StatusCode)
	}
	var manifest struct {
		AppID string `json:"app_id"`
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil || manifest.AppID != taskspkg.AppID {
		t.Fatalf("manifest %s (%v)", rawManifest, err)
	}

	var project taskspkg.Project
	out := mustRunTasksApp(t, relayURL, "taskalice.poweur.net", aliceToken,
		"--json", "project", "new", "Kitchen renovation")
	if err := json.Unmarshal([]byte(out), &project); err != nil {
		t.Fatalf("project new output %s: %v", out, err)
	}
	var alicesTask taskspkg.Task
	out = mustRunTasksApp(t, relayURL, "taskalice.poweur.net", aliceToken,
		"--json", "add", project.ID, "Measure the alcove")
	if err := json.Unmarshal([]byte(out), &alicesTask); err != nil {
		t.Fatalf("add output %s: %v", out, err)
	}

	// PCP-0007 layout, on the real tree: one file per task under the
	// project directory.
	projectPath := "/apps/net.poweur.tasks/projects/" + project.ID
	resp = davDo(t, "PROPFIND", base+projectPath+"/tasks/", aliceToken, nil, map[string]string{"Depth": "1"})
	listing, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus || !strings.Contains(string(listing), alicesTask.ID+".json") {
		t.Fatalf("task file not on the tree: %d\n%s", resp.StatusCode, listing)
	}

	// --- the token really is confined to the app's namespace -------------
	resp = davDo(t, http.MethodGet, base+"/private/", aliceToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("app-scoped token reached /private: %d, want 403", resp.StatusCode)
	}

	// --- sharing a project is an EPIC-005 share of its directory ---------
	shareOut, _ := runCLI(t, aliceHome, "share", "add", projectPath,
		"--with", "taskbob.poweur.net", "--perm", "rw", "--json")
	if !strings.Contains(shareOut, project.ID) {
		t.Fatalf("share add output: %s", shareOut)
	}

	bobToken := mintTokenViaCLI(t, bobHome,
		"--use-identity", "taskbob.poweur.net",
		"--audience", "taskalice.poweur.net",
		"--scope", taskspkg.TokenScope,
		"--relay", relayURL)

	// Bob points the *same app* at alice's home. A grant is read through the
	// owner's tree; nothing was copied into bob's.
	out = mustRunTasksApp(t, relayURL, "taskalice.poweur.net", bobToken, "list", project.ID)
	if !strings.Contains(out, "Measure the alcove") {
		t.Fatalf("bob cannot see alice's task through the share: %q", out)
	}

	var bobsTask taskspkg.Task
	out = mustRunTasksApp(t, relayURL, "taskalice.poweur.net", bobToken,
		"--json", "add", project.ID, "Order the tiles")
	if err := json.Unmarshal([]byte(out), &bobsTask); err != nil {
		t.Fatalf("bob add output %s: %v", out, err)
	}
	mustRunTasksApp(t, relayURL, "taskalice.poweur.net", bobToken,
		"set", project.ID, bobsTask.ID, "assignee=taskbob.poweur.net", "priority=2")

	// Alice sees bob's work in her own home, through her own token.
	out = mustRunTasksApp(t, relayURL, "taskalice.poweur.net", aliceToken, "list", project.ID)
	if !strings.Contains(out, "Order the tiles") || !strings.Contains(out, "@taskbob.poweur.net") {
		t.Fatalf("alice does not see bob's task: %q", out)
	}

	// Bob closes alice's task; alice sees the state change.
	mustRunTasksApp(t, relayURL, "taskalice.poweur.net", bobToken, "done", project.ID, alicesTask.ID)
	out = mustRunTasksApp(t, relayURL, "taskalice.poweur.net", aliceToken, "--json", "list", project.ID)
	var listed []taskspkg.Task
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list --json: %v (%s)", err, out)
	}
	var closed *taskspkg.Task
	for i := range listed {
		if listed[i].ID == alicesTask.ID {
			closed = &listed[i]
		}
	}
	if closed == nil || closed.Status != taskspkg.StatusDone || closed.Completed == "" {
		t.Fatalf("bob's completion did not land: %+v", listed)
	}

	// --- the grant is the authority; revoking it ends the collaboration --
	lsOut, _ := runCLI(t, aliceHome, "share", "ls")
	shareID := strings.Fields(lsOut)[0]
	if !strings.HasPrefix(shareID, "shr_") {
		t.Fatalf("share id not found: %s", lsOut)
	}
	runCLI(t, aliceHome, "share", "revoke", shareID)
	if _, _, code := runTasksApp(t, relayURL, "taskalice.poweur.net", bobToken, "list", project.ID); code == 0 {
		t.Fatal("bob still reads the project after revoke")
	}
}

// TestINT_TASKS_02: PCP-0007's most important compatibility rule against the
// real relay — a field this implementation has never heard of, written by a
// notional second implementation, survives a read-modify-write.
func TestINT_TASKS_02_UnknownFieldsSurviveARealRoundTrip(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("taskcompat.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home, "identity", "create", "taskcompat.poweur.net", "--hosted", "--relay", relayURL, "--json")
	token := mintTokenViaCLI(t, home, "--use-identity", "taskcompat.poweur.net", "--scope", taskspkg.TokenScope)

	mustRunTasksApp(t, relayURL, "taskcompat.poweur.net", token, "init")
	var project taskspkg.Project
	out := mustRunTasksApp(t, relayURL, "taskcompat.poweur.net", token, "--json", "project", "new", "Compat")
	if err := json.Unmarshal([]byte(out), &project); err != nil {
		t.Fatal(err)
	}

	base := relayURL + "/dav/taskcompat.poweur.net"
	dir := "/apps/net.poweur.tasks/projects/" + project.ID
	foreign := `{
	  "schema_version": "1",
	  "id": "tsk_fromelsewhere",
	  "title": "Written by another implementation",
	  "status": "blocked",
	  "created": "2026-09-01T09:00:00Z",
	  "updated": "2026-09-01T09:00:00Z",
	  "x_other_app": {"colour": "red", "board_column": 3},
	  "recurrence": {"freq": "weekly"}
	}`
	resp := davDo(t, http.MethodPut, base+dir+"/tasks/tsk_fromelsewhere.json", token, []byte(foreign), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("seed foreign task: %d", resp.StatusCode)
	}

	// An unknown status displays as todo and is flagged, never rewritten.
	out = mustRunTasksApp(t, relayURL, "taskcompat.poweur.net", token, "list", project.ID)
	if !strings.Contains(out, "unknown, shown as todo") {
		t.Fatalf("unknown status not surfaced: %q", out)
	}

	mustRunTasksApp(t, relayURL, "taskcompat.poweur.net", token,
		"set", project.ID, "tsk_fromelsewhere", "priority=1")

	resp = davDo(t, http.MethodGet, base+dir+"/tasks/tsk_fromelsewhere.json", token, nil, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("re-read: %v (%s)", err, raw)
	}
	for _, key := range []string{"x_other_app", "recurrence"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("read-modify-write through the relay dropped %q:\n%s", key, raw)
		}
	}
	if got["status"] != "blocked" {
		t.Fatalf("unknown status normalised to %v", got["status"])
	}
	if got["priority"] != float64(1) {
		t.Fatalf("the update itself did not land: %v", got["priority"])
	}
	if nested, ok := got["x_other_app"].(map[string]any); !ok || nested["board_column"] != float64(3) {
		t.Fatalf("nested foreign value mangled: %v", got["x_other_app"])
	}
}

// TestINT_TASKS_03: E06-T3's namespace claim is enforced by the relay, not by
// convention alone — a manifest whose app_id does not match its directory is
// rejected on write, so an app cannot squat another app's namespace.
func TestINT_TASKS_03_ManifestAppIDIsEnforced(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("taskmanifest.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	home := t.TempDir()
	runCLI(t, home, "identity", "create", "taskmanifest.poweur.net", "--hosted", "--relay", relayURL, "--json")
	token := mintTokenViaCLI(t, home, "--use-identity", "taskmanifest.poweur.net")
	base := relayURL + "/dav/taskmanifest.poweur.net"

	resp := davDo(t, "MKCOL", base+"/apps/net.poweur.tasks", token, nil, nil)
	resp.Body.Close()

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"matching app_id", `{"app_id":"net.poweur.tasks","name":"Poweur Tasks"}`, 0},
		{"squatting another namespace", `{"app_id":"com.example.other","name":"Other"}`, http.StatusUnprocessableEntity},
		{"no app_id", `{"name":"Nameless"}`, http.StatusUnprocessableEntity},
		{"not json", `nope`, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t2 *testing.T) {
			resp := davDo(t2, http.MethodPut, base+"/apps/net.poweur.tasks/manifest.json", token, []byte(tc.body), nil)
			resp.Body.Close()
			if tc.want == 0 {
				if resp.StatusCode >= 300 {
					t2.Fatalf("valid manifest rejected: %d", resp.StatusCode)
				}
				return
			}
			if resp.StatusCode != tc.want {
				t2.Fatalf("manifest PUT: %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestINT_TASKS_04: what a recipient's view of a shared namespace actually
// looks like. The dogfood expected to find a discovery gap here — that bob
// would need a project id handed to him out of band — and found instead that
// EPIC-005 makes the ancestor directories of a grant readable, so bob's
// unmodified `projects` command enumerates exactly the projects shared with
// him and nothing else.
//
// The genuine friction is one level up: `manifest.json` is a *sibling* of
// `projects/`, not an ancestor of the grant, so it is 403 for bob. app-data.md
// says a namespace without a readable manifest is "considered abandoned by
// tooling" — which is exactly what every shared namespace looks like from the
// recipient's side. See PCP-0007's retrospective.
func TestINT_TASKS_04_RecipientsViewOfASharedNamespace(t *testing.T) {
	zone := newZone(t)
	ts, addr := newHostedRelay(t, zone, t.TempDir())
	defer ts.Close()
	relayURL := "http://" + addr
	zone.SetHost("discalice.poweur.net", addr)
	zone.SetHost("discbob.poweur.net", addr)
	clipkg.ConfigureIdentityResolver("http", true, addr)
	t.Cleanup(func() { clipkg.ConfigureIdentityResolver("https", false, "") })

	aliceHome, bobHome := t.TempDir(), t.TempDir()
	runCLI(t, aliceHome, "identity", "create", "discalice.poweur.net", "--hosted", "--relay", relayURL, "--json")
	runCLI(t, bobHome, "identity", "create", "discbob.poweur.net", "--hosted", "--relay", relayURL, "--json")
	aliceToken := mintTokenViaCLI(t, aliceHome, "--use-identity", "discalice.poweur.net", "--scope", taskspkg.TokenScope)

	mustRunTasksApp(t, relayURL, "discalice.poweur.net", aliceToken, "init")

	newProject := func(title string) taskspkg.Project {
		t.Helper()
		var p taskspkg.Project
		out := mustRunTasksApp(t, relayURL, "discalice.poweur.net", aliceToken, "--json", "project", "new", title)
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	shared := newProject("Shared")
	private := newProject("Alice's private project")
	mustRunTasksApp(t, relayURL, "discalice.poweur.net", aliceToken, "add", shared.ID, "a task")
	mustRunTasksApp(t, relayURL, "discalice.poweur.net", aliceToken, "add", private.ID, "a secret")

	runCLI(t, aliceHome, "share", "add", "/apps/net.poweur.tasks/projects/"+shared.ID,
		"--with", "discbob.poweur.net", "--perm", "rw", "--json")

	bobToken := mintTokenViaCLI(t, bobHome,
		"--use-identity", "discbob.poweur.net",
		"--audience", "discalice.poweur.net",
		"--scope", taskspkg.TokenScope,
		"--relay", relayURL)

	// Told the id, bob works fine.
	if out := mustRunTasksApp(t, relayURL, "discalice.poweur.net", bobToken, "list", shared.ID); !strings.Contains(out, "a task") {
		t.Fatalf("bob cannot read the granted project: %q", out)
	}

	// He does not need to be told: EPIC-005 makes the ancestors of a grant
	// readable, so the unmodified `projects` command discovers it — and
	// shows only what he was granted.
	out := mustRunTasksApp(t, relayURL, "discalice.poweur.net", bobToken, "projects")
	if !strings.Contains(out, shared.ID) {
		t.Fatalf("bob cannot discover the project shared with him: %q", out)
	}
	if strings.Contains(out, private.ID) || strings.Contains(out, "private project") {
		t.Fatalf("the projects listing leaked an ungranted project:\n%s", out)
	}
	if _, _, code := runTasksApp(t, relayURL, "discalice.poweur.net", bobToken, "list", private.ID); code == 0 {
		t.Fatal("bob read an ungranted project's tasks")
	}

	// The friction: manifest.json is a sibling of projects/, not an ancestor
	// of the grant, so the recipient's view of the namespace has no readable
	// manifest — the shape app-data.md calls "abandoned".
	base := relayURL + "/dav/discalice.poweur.net"
	resp := davDo(t, http.MethodGet, base+"/apps/net.poweur.tasks/manifest.json", bobToken, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("manifest read by a project-share recipient: %d, want 403 — "+
			"if this changed, PCP-0007's retrospective needs updating", resp.StatusCode)
	}
}
