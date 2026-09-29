package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/poweur/integration/refapps/tasks"
)

// TestE31_Tasks is the tasks app on v2 (successor of v1's PCP-0007
// poweur-tasks): a project is an event log every member folds to the same
// board. Alice owns it (relay A); Bob edits and Carol observes from relay B;
// Dan joins later from relay C.
func TestE31_Tasks(t *testing.T) {
	forEachScenario(t, func(t *testing.T, s *scenario) {
		alice := s.newActor("taskalice", "A")
		bob := s.newActor("taskbob", "B")
		carol := s.newActor("taskcarol", "B")
		dan := s.newActor("taskdan", "C")

		project := "Kitchen-Renovation-3a7e"
		folder, projectFile, events := tasks.Paths(project)
		s.secret(project, "Measure the alcove 51c2", "Order the worktop 8d0e", "measured twice 77f1")

		alice.Run("drive", "mkdir", "/"+tasks.Root, "--json")
		var node map[string]string
		alice.JSON(&node, "drive", "mkdir", folder)
		projectJSON, _ := json.Marshal(tasks.Project{Title: project})
		alice.Run("drive", "put", alice.File("project.json", string(projectJSON)), projectFile, "--json")
		measure, _ := tasks.Create("Measure the alcove 51c2", map[string]any{"priority": 2})
		worktop, _ := tasks.Create("Order the worktop 8d0e", nil)
		appendEvent(t, alice, events, measure)
		appendEvent(t, alice, events, worktop)

		alice.Run("drive", "share", "add", folder, bob.Identity, "--role", "write", "--json")
		alice.Run("drive", "share", "add", folder, carol.Identity, "--role", "read", "--json")
		bob.acceptOffers(alice.Identity)
		carol.acceptOffers(alice.Identity)
		memberEvents := "/" + node["node"] + "/" + tasks.EventsFile

		// Offline on both sides: Bob starts the measuring, Alice assigns it
		// to him and finishes the worktop. The relay orders the appends.
		doing, _ := tasks.Update(measure.Task, map[string]any{"status": tasks.StatusDoing})
		appendEvent(t, bob, memberEvents, doing, "--drive", alice.Identity)
		assign, _ := tasks.Update(measure.Task, map[string]any{"assignee": bob.Identity})
		appendEvent(t, alice, events, assign)
		ordered, _ := tasks.Update(worktop.Task, map[string]any{"status": tasks.StatusDone})
		appendEvent(t, alice, events, ordered)
		appendEvent(t, bob, memberEvents, tasks.Comment(measure.Task, "measured twice 77f1"), "--drive", alice.Identity)

		// Alice tells the assignee.
		alice.Run("send", bob.Identity, "--type", tasks.NotifyType, `{"project":"`+node["node"]+`","task":"`+measure.Task+`"}`)
		if !strings.Contains(bob.Run("inbox"), tasks.NotifyType) {
			t.Fatal("bob was not notified of the assignment")
		}

		want := boardOf(t, alice, events)
		if m := want.Tasks[measure.Task]; m == nil || m.Status != tasks.StatusDoing || m.Assignee != bob.Identity || len(m.Comments) != 1 {
			t.Fatalf("measure task: %+v", m)
		}
		if w := want.Tasks[worktop.Task]; w == nil || w.Status != tasks.StatusDone {
			t.Fatalf("worktop task: %+v", w)
		}
		for _, reader := range []*actor{bob, carol} {
			if got := boardOf(t, reader, memberEvents, "--drive", alice.Identity); !sameBoard(got, want) {
				t.Fatalf("%s folds a different board", reader.Name)
			}
		}

		// Carol observes: the relay refuses her writes.
		observe, _ := tasks.Update(worktop.Task, map[string]any{"status": tasks.StatusTodo})
		raw, _ := json.Marshal(observe)
		if code, _, stderr := carol.Try("drive", "append", memberEvents, carol.File("e.json", string(raw)), "--drive", alice.Identity); code == 0 || !strings.Contains(stderr, "403") {
			t.Fatalf("carol appended: %d %s", code, stderr)
		}

		// The same board after a restart of the host relay with its caches gone.
		s.restart(alice.Relay.name)
		if got := boardOf(t, alice, events); !sameBoard(got, want) {
			t.Fatal("the board changed across a relay restart")
		}

		// Dan joins later and reads the whole history.
		alice.Run("drive", "share", "add", folder, dan.Identity, "--role", "read", "--json")
		dan.acceptOffers(alice.Identity)
		if got := boardOf(t, dan, memberEvents, "--drive", alice.Identity); !sameBoard(got, want) {
			t.Fatal("dan folds a different board")
		}

		// Revoking Bob stops his writes.
		var listed struct {
			Shares []map[string]any `json:"shares"`
		}
		alice.JSON(&listed, "drive", "share", "ls")
		for _, share := range listed.Shares {
			if share["member"] == bob.Identity && share["revoked"] == nil {
				alice.Run("drive", "share", "rm", share["id"].(string), "--json")
			}
		}
		late, _ := tasks.Update(measure.Task, map[string]any{"status": tasks.StatusDone})
		raw, _ = json.Marshal(late)
		if code, _, _ := bob.Try("drive", "append", memberEvents, bob.File("late.json", string(raw)), "--drive", alice.Identity); code == 0 {
			t.Fatal("bob appended after revocation")
		}

		s.privacyScan()
	})
}

func appendEvent(t *testing.T, a *actor, remote string, ev tasks.Event, extra ...string) {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	a.Run(append([]string{"drive", "append", remote, a.File("event.json", string(raw)), "--json"}, extra...)...)
}

func boardOf(t *testing.T, a *actor, remote string, extra ...string) tasks.Board {
	t.Helper()
	var records []struct {
		Position uint64 `json:"position"`
		Author   string `json:"author"`
		Text     string `json:"text"`
	}
	a.JSON(&records, append([]string{"drive", "tail", remote}, extra...)...)
	entries := make([]tasks.Entry, 0, len(records))
	for _, r := range records {
		entries = append(entries, tasks.Entry{Position: r.Position, Author: r.Author, Text: r.Text})
	}
	return tasks.Reduce(entries)
}

func sameBoard(a, b tasks.Board) bool {
	x, _ := json.Marshal(a.Ordered())
	y, _ := json.Marshal(b.Ordered())
	return string(x) == string(y)
}
