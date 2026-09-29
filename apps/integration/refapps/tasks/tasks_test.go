package tasks

import (
	"encoding/json"
	"testing"
)

func line(t *testing.T, ev Event) string {
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestReduceIsLastWriterWinsByPosition(t *testing.T) {
	create, _ := Create("Measure the alcove", map[string]any{"priority": 2, "x-colour": "blue"})
	id := create.Task
	toBob, _ := Update(id, map[string]any{"assignee": "bob", "status": StatusDoing})
	done, _ := Update(id, map[string]any{"status": StatusDone})
	unknown, _ := Update(id, map[string]any{"status": "blocked"})
	entries := []Entry{
		{Position: 4, Author: "bob", Text: line(t, done)},
		{Position: 1, Author: "alice", Text: line(t, create)},
		{Position: 2, Author: "alice", Text: line(t, toBob)},
		{Position: 3, Author: "carol", Text: line(t, Comment(id, "measured twice"))},
		{Position: 5, Author: "alice", Text: "not json"},
		{Position: 6, Author: "alice", Text: line(t, create)}, // duplicate create
	}
	board := Reduce(entries)
	task := board.Tasks[id]
	if task == nil || task.Status != StatusDone || task.Assignee != "bob" || task.Priority != 2 || task.CreatedBy != "alice" || task.UpdatedAt != 4 {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Comments) != 1 || task.Comments[0].Author != "carol" {
		t.Fatalf("comments = %+v", task.Comments)
	}
	if string(task.Extra["x-colour"]) != `"blue"` || board.Ignored != 2 {
		t.Fatalf("extra=%v ignored=%d", task.Extra, board.Ignored)
	}
	// An unknown status is kept and displayed as todo.
	board = Reduce(append(entries, Entry{Position: 7, Author: "bob", Text: line(t, unknown)}))
	if task = board.Tasks[id]; task.Status != "blocked" || DisplayStatus(task.Status) != StatusTodo {
		t.Fatalf("unknown status: %+v", task)
	}
}

func TestOrderedColumns(t *testing.T) {
	a, _ := Create("a", nil)
	b, _ := Create("b", map[string]any{"status": StatusDoing})
	c, _ := Create("c", map[string]any{"priority": 5})
	board := Reduce([]Entry{{1, "x", line(t, a)}, {2, "x", line(t, b)}, {3, "x", line(t, c)}})
	got := board.Ordered()
	if got[0].Title != "b" || got[1].Title != "c" || got[2].Title != "a" {
		t.Fatalf("order: %s %s %s", got[0].Title, got[1].Title, got[2].Title)
	}
	if _, err := Create(" ", nil); err == nil {
		t.Fatal("empty title accepted")
	}
	if _, err := Update("x", nil); err == nil {
		t.Fatal("empty update accepted")
	}
}
