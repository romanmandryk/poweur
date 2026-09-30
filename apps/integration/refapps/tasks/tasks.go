// Package tasks is the headless task app of EPIC-026, the successor to the
// v1 PCP-0007 `poweur-tasks` dogfood app, rebuilt on storage v2.
//
// v1 kept one JSON document per task and merged by last writer. On v2 a
// project is an event log the relay orders — everyone who may write appends,
// everyone who may read folds the same log to the same board, and offline
// edits converge on reconnect without a merge step:
//
//	Tasks/<project>/project.json   replace file: title, description (owner)
//	Tasks/<project>/events.jsonl   append file: one Event per record
//
// PCP-0007's vocabulary carries over: status todo|doing|done|cancelled, an
// unknown status displays as todo and is preserved, and fields this version
// does not know survive in Task.Extra. Like every EPIC-026 app it never talks
// to a relay: storage, sharing and notification are `poweur` commands.
package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

const (
	Root        = "Tasks"
	ProjectFile = "project.json"
	EventsFile  = "events.jsonl"
	// NotifyType is the message sent to an assignee.
	NotifyType = "net.poweur.tasks.assigned"
)

// Paths returns a project's folder and files.
func Paths(project string) (folder, projectFile, events string) {
	folder = "/" + Root + "/" + project
	return folder, folder + "/" + ProjectFile, folder + "/" + EventsFile
}

// Status vocabulary (PCP-0007).
const (
	StatusTodo      = "todo"
	StatusDoing     = "doing"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

func KnownStatus(s string) bool {
	switch s {
	case StatusTodo, StatusDoing, StatusDone, StatusCancelled:
		return true
	}
	return false
}

// DisplayStatus renders an unknown status as todo (it is still preserved).
func DisplayStatus(s string) string {
	if KnownStatus(s) {
		return s
	}
	return StatusTodo
}

// Project is project.json.
type Project struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// Event is one line of events.jsonl. The author is the signed author of the
// append record that carries it, never a field here.
type Event struct {
	Type string `json:"type"` // task.create | task.update | task.comment
	Task string `json:"task"`
	// Fields for create and update: title, status, assignee, due, priority,
	// notes, or anything a newer version defines.
	Fields map[string]json.RawMessage `json:"fields,omitempty"`
	Text   string                     `json:"text,omitempty"` // task.comment
}

const (
	TypeCreate  = "task.create"
	TypeUpdate  = "task.update"
	TypeComment = "task.comment"
)

func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tsk_" + hex.EncodeToString(b), nil
}

func fields(values map[string]any) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for k, v := range values {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[k] = raw
	}
	return out, nil
}

// Create returns the event that adds a task (status todo unless given).
func Create(title string, extra map[string]any) (Event, error) {
	if strings.TrimSpace(title) == "" {
		return Event{}, errors.New("a task needs a title")
	}
	id, err := newID()
	if err != nil {
		return Event{}, err
	}
	values := map[string]any{"title": title, "status": StatusTodo}
	for k, v := range extra {
		values[k] = v
	}
	f, err := fields(values)
	return Event{Type: TypeCreate, Task: id, Fields: f}, err
}

// Update returns the event that changes fields of a task.
func Update(task string, values map[string]any) (Event, error) {
	if len(values) == 0 {
		return Event{}, errors.New("nothing to update")
	}
	f, err := fields(values)
	return Event{Type: TypeUpdate, Task: task, Fields: f}, err
}

// Comment returns the event that comments on a task.
func Comment(task, text string) Event { return Event{Type: TypeComment, Task: task, Text: text} }

// Entry is an append record as the drive returns it.
type Entry struct {
	Position uint64
	Author   string
	Text     string
}

type Task struct {
	ID        string
	Title     string
	Status    string
	Assignee  string
	Due       string
	Priority  int
	Notes     string
	CreatedBy string
	// UpdatedAt is the log position of the last change.
	UpdatedAt uint64
	Comments  []TaskComment
	// Extra keeps fields this version does not know (PCP-0007 rule).
	Extra map[string]json.RawMessage
}

type TaskComment struct {
	Author string
	Text   string
}

// Board is a folded project log.
type Board struct {
	Tasks map[string]*Task
	// Ignored counts events the reducer refused (malformed, unknown task,
	// duplicate create).
	Ignored int
}

// Ordered returns tasks by status column, then priority, then creation order.
func (b Board) Ordered() []*Task {
	out := make([]*Task, 0, len(b.Tasks))
	for _, t := range b.Tasks {
		out = append(out, t)
	}
	rank := map[string]int{StatusDoing: 0, StatusTodo: 1, StatusDone: 2, StatusCancelled: 3}
	sort.SliceStable(out, func(i, j int) bool {
		a, c := rank[DisplayStatus(out[i].Status)], rank[DisplayStatus(out[j].Status)]
		if a != c {
			return a < c
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Reduce folds the log in relay order. Every field is last-writer-wins by
// position, so every reader that has the same records has the same board.
func Reduce(entries []Entry) Board {
	board := Board{Tasks: map[string]*Task{}}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Position < entries[j].Position })
	for _, entry := range entries {
		var ev Event
		if json.Unmarshal([]byte(entry.Text), &ev) != nil || ev.Task == "" {
			board.Ignored++
			continue
		}
		task := board.Tasks[ev.Task]
		switch ev.Type {
		case TypeCreate:
			if task != nil {
				board.Ignored++
				continue
			}
			task = &Task{ID: ev.Task, Status: StatusTodo, CreatedBy: entry.Author, Extra: map[string]json.RawMessage{}}
			board.Tasks[ev.Task] = task
			apply(task, ev.Fields)
		case TypeUpdate:
			if task == nil {
				board.Ignored++
				continue
			}
			apply(task, ev.Fields)
		case TypeComment:
			if task == nil || strings.TrimSpace(ev.Text) == "" {
				board.Ignored++
				continue
			}
			task.Comments = append(task.Comments, TaskComment{Author: entry.Author, Text: ev.Text})
		default:
			board.Ignored++
			continue
		}
		task.UpdatedAt = entry.Position
	}
	return board
}

func apply(task *Task, values map[string]json.RawMessage) {
	for k, raw := range values {
		switch k {
		case "title":
			_ = json.Unmarshal(raw, &task.Title)
		case "status":
			_ = json.Unmarshal(raw, &task.Status)
		case "assignee":
			_ = json.Unmarshal(raw, &task.Assignee)
		case "due":
			_ = json.Unmarshal(raw, &task.Due)
		case "priority":
			_ = json.Unmarshal(raw, &task.Priority)
		case "notes":
			_ = json.Unmarshal(raw, &task.Notes)
		default:
			task.Extra[k] = raw
		}
	}
}
