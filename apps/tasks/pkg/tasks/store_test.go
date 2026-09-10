package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStoreInitWritesManifestFirst(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	s := f.store(ts.URL)
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw := f.get(NamespaceRoot + "/manifest.json")
	if raw == nil {
		t.Fatal("init did not write manifest.json")
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.AppID != AppID {
		t.Fatalf("manifest app_id %q", m.AppID)
	}
	got, err := s.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Name == "" {
		t.Fatal("manifest name is required by E06-T3")
	}
	// Re-running init is safe: MKCOL on an existing collection is a no-op.
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("second init: %v", err)
	}
}

// The relay rejects a manifest whose app_id does not match the directory
// (E06-T3). The app must surface that rather than swallow it.
func TestStoreInitManifestAppIDMismatchIsRejected(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	c := f.client(ts.URL)
	ctx := context.Background()
	if err := c.Mkcol(ctx, NamespaceRoot); err != nil {
		t.Fatal(err)
	}
	err := c.Put(ctx, NamespaceRoot+"/manifest.json", []byte(`{"app_id":"com.example.other","name":"Other"}`))
	if err == nil {
		t.Fatal("mismatched app_id accepted")
	}
	if !strings.Contains(err.Error(), "app_id") {
		t.Fatalf("error should explain the mismatch: %v", err)
	}
}

func TestStoreProjectAndTaskLifecycle(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	s := f.store(ts.URL)
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}

	p, err := NewProject("Kitchen renovation")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Kitchen renovation" {
		t.Fatalf("title %q", got.Title)
	}

	task, err := NewTask("Measure the alcove")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(ctx, p.ID, task); err != nil {
		t.Fatal(err)
	}
	tasks, skipped, err := s.ListTasks(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || len(skipped) != 0 {
		t.Fatalf("ListTasks = %d tasks, %v skipped", len(tasks), skipped)
	}

	projects, skippedP, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || len(skippedP) != 0 {
		t.Fatalf("ListProjects = %+v, %v", projects, skippedP)
	}

	if err := s.DeleteTask(ctx, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	tasks, _, err = s.ListTasks(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("task survived delete: %+v", tasks)
	}
}

// A namespace that has never been initialised lists as empty, not as an error:
// "no projects yet" and "the relay is broken" are different conditions.
func TestListsAreEmptyBeforeInit(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	s := f.store(ts.URL)
	projects, _, err := s.ListProjects(context.Background())
	if err != nil || len(projects) != 0 {
		t.Fatalf("ListProjects = %v, %v", projects, err)
	}
	tasks, _, err := s.ListTasks(context.Background(), "prj_missing")
	if err != nil || len(tasks) != 0 {
		t.Fatalf("ListTasks = %v, %v", tasks, err)
	}
}

// PCP-0007 compatibility rules, all in one listing: an unreadable project is
// skipped not fatal, a project directory with tasks but no project.json is a
// partially-synced state, and unknown files are left alone.
func TestListProjectsToleratesPartialAndForeignContent(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	s := f.store(ts.URL)
	ctx := context.Background()

	good, err := NewProject("Good")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx, good); err != nil {
		t.Fatal(err)
	}

	// A half-synced project: tasks present, project.json not here yet.
	f.put(NamespaceRoot+"/projects/prj_halfsynced/tasks/tsk_x.json", []byte(`{}`))
	// A corrupt project document.
	f.put(NamespaceRoot+"/projects/prj_corrupt/project.json", []byte(`{not json`))
	// A project.json whose id disagrees with its directory.
	f.put(NamespaceRoot+"/projects/prj_mislabelled/project.json",
		[]byte(`{"schema_version":"1","id":"prj_somethingelse","title":"X","created":"2026-01-01T00:00:00Z"}`))
	// An unknown file the app must not touch.
	f.put(NamespaceRoot+"/projects/index.cache", []byte(`some other app's cache`))

	projects, skipped, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("a broken sibling must not fail the whole listing: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != good.ID {
		t.Fatalf("projects = %+v, want only the good one", projects)
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped = %v, want the three unreadable directories", skipped)
	}
	if f.get(NamespaceRoot+"/projects/index.cache") == nil {
		t.Fatal("the app deleted a file it does not own")
	}
	for _, call := range f.methodCalls("DELETE") {
		t.Fatalf("listing issued a DELETE: %s", call)
	}
}

func TestListTasksSkipsUnreadableDocuments(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	s := f.store(ts.URL)
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := NewProject("P")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	dir, err := projectDir(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	good, err := NewTask("good")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(ctx, p.ID, good); err != nil {
		t.Fatal(err)
	}
	f.put(dir+"/tasks/tsk_broken.json", []byte(`{"schema_version":"1"}`))
	f.put(dir+"/tasks/notes.md", []byte(`not a task at all`))

	tasks, skipped, err := s.ListTasks(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != good.ID {
		t.Fatalf("tasks = %+v", tasks)
	}
	if len(skipped) != 1 || skipped[0] != "tsk_broken.json" {
		t.Fatalf("skipped = %v, want only the broken .json", skipped)
	}
}

// The id inside the document is authoritative for identity; a mismatch with
// the path is a corruption signal, not something to silently paper over.
func TestGetTaskRejectsIDPathMismatch(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	s := f.store(ts.URL)
	f.put(NamespaceRoot+"/projects/prj_a/tasks/tsk_a.json",
		[]byte(`{"schema_version":"1","id":"tsk_different","title":"T","status":"todo","created":"c","updated":"u"}`))
	_, err := s.GetTask(context.Background(), "prj_a", "tsk_a")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("GetTask = %v, want a mismatch error", err)
	}
}

// Every entry point that takes an id refuses to build a path out of a hostile
// one, before any request is made.
func TestStoreRefusesUnsafeIDsWithoutTouchingTheWire(t *testing.T) {
	f, ts := newFakeDAV(t, "alice.example.org")
	s := f.store(ts.URL)
	ctx := context.Background()
	bad := "../../poweur-sys/public"

	if _, err := s.GetProject(ctx, bad); err == nil {
		t.Fatal("GetProject accepted a traversal id")
	}
	if _, err := s.GetTask(ctx, bad, "tsk_a"); err == nil {
		t.Fatal("GetTask accepted a traversal project id")
	}
	if _, err := s.GetTask(ctx, "prj_a", bad); err == nil {
		t.Fatal("GetTask accepted a traversal task id")
	}
	if err := s.DeleteTask(ctx, "prj_a", bad); err == nil {
		t.Fatal("DeleteTask accepted a traversal task id")
	}
	if _, _, err := s.ListTasks(ctx, bad); err == nil {
		t.Fatal("ListTasks accepted a traversal project id")
	}
	if err := s.PutTask(ctx, bad, Task{}); err == nil {
		t.Fatal("PutTask accepted a traversal project id")
	}
	if err := s.PutProject(ctx, Project{ID: bad}); err == nil {
		t.Fatal("PutProject accepted a traversal id")
	}
	if len(f.calls) != 0 {
		t.Fatalf("unsafe ids reached the network: %v", f.calls)
	}
}
