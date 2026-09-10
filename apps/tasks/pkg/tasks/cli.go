package tasks

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Config is everything the app needs to reach a home. There is no login and
// no OAuth-style consent flow in the platform yet (see the PCP-0007
// retrospective): the user mints a scoped token with the first-party CLI and
// hands it over.
//
//	poweur dav token --scope dav:rw:/apps/net.poweur.tasks/ --json
type Config struct {
	Relay string // https://relay.poweur.net
	Owner string // whose home; the other person's identity for a shared project
	Token string // bearer token from `poweur dav token`
}

// FromEnv reads the configuration from POWEUR_TASKS_*.
func FromEnv() Config {
	return Config{
		Relay: strings.TrimSpace(os.Getenv("POWEUR_TASKS_RELAY")),
		Owner: strings.TrimSpace(os.Getenv("POWEUR_TASKS_OWNER")),
		Token: strings.TrimSpace(os.Getenv("POWEUR_TASKS_TOKEN")),
	}
}

// Store builds a store from the configuration.
func (c Config) Store() (*Store, error) {
	if c.Relay == "" {
		return nil, fmt.Errorf("relay is required (--relay or POWEUR_TASKS_RELAY)")
	}
	if c.Owner == "" {
		return nil, fmt.Errorf("owner identity is required (--owner or POWEUR_TASKS_OWNER)")
	}
	if c.Token == "" {
		return nil, fmt.Errorf("token is required (--token or POWEUR_TASKS_TOKEN); mint one with:\n  poweur dav token --scope %s --json", TokenScope)
	}
	return &Store{DAV: &DAVClient{BaseURL: c.Relay, Owner: c.Owner, Token: c.Token}}, nil
}

const usage = `poweur-tasks — reference implementation of PCP-0007 (net.poweur.tasks)

usage: poweur-tasks [global flags] <command> [args]

commands:
  init                                   write manifest.json + projects/ into the home
  projects                               list projects
  project new <title>                    create a project
  add <project-id> <title>               add a task
  list <project-id>                      list a project's tasks
  show <project-id> <task-id>            print one task document
  set <project-id> <task-id> <k=v>...    update fields (status, title, due, priority,
                                         assignee, notes, tags, parent)
  done <project-id> <task-id>            shorthand for set status=done
  rm <project-id> <task-id>              delete a task

global flags:
  --relay URL     relay origin        (env POWEUR_TASKS_RELAY)
  --owner ID      home to address     (env POWEUR_TASKS_OWNER)
  --token TOKEN   scoped DAV token    (env POWEUR_TASKS_TOKEN)
  --json          machine-readable output

Get a token from the first-party CLI:
  poweur dav token --scope ` + TokenScope + ` --json

To work on someone else's shared project, point --owner at them and use a
token whose audience is their home (poweur dav token --audience <them>).
`

// Run executes one command. It returns a process exit code so the binary and
// the integration tests drive the exact same entry point.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("poweur-tasks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	cfg := FromEnv()
	fs.StringVar(&cfg.Relay, "relay", cfg.Relay, "relay origin")
	fs.StringVar(&cfg.Owner, "owner", cfg.Owner, "identity whose home to address")
	fs.StringVar(&cfg.Token, "token", cfg.Token, "scoped DAV bearer token")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	store, err := cfg.Store()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()

	if err := dispatch(ctx, store, rest, *asJSON, stdout); err != nil {
		fmt.Fprintln(stderr, explain(err))
		return 1
	}
	return 0
}

// explain turns the two failures every third-party app author will actually
// hit into sentences instead of status codes.
func explain(err error) string {
	switch {
	case Forbidden(err):
		return fmt.Sprintf("%v\n\nthe token does not cover this path. Mint one scoped to the app:\n  poweur dav token --scope %s --json", err, TokenScope)
	case NotFound(err):
		return fmt.Sprintf("%v\n\nnothing there yet — run `poweur-tasks init` first", err)
	}
	return err.Error()
}

func dispatch(ctx context.Context, store *Store, args []string, asJSON bool, out io.Writer) error {
	switch args[0] {
	case "init":
		if err := store.Init(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "initialised %s in %s's home\n", NamespaceRoot, store.DAV.Owner)
		return nil

	case "projects":
		projects, skipped, err := store.ListProjects(ctx)
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(out, projects)
		}
		if len(projects) == 0 {
			fmt.Fprintln(out, "no projects")
		}
		for _, p := range projects {
			archived := ""
			if p.Archived {
				archived = "  (archived)"
			}
			fmt.Fprintf(out, "%s  %s%s\n", p.ID, p.Title, archived)
		}
		for _, s := range skipped {
			fmt.Fprintf(out, "(skipped unreadable project %s)\n", s)
		}
		return nil

	case "project":
		if len(args) < 3 || args[1] != "new" {
			return fmt.Errorf("usage: poweur-tasks project new <title>")
		}
		p, err := NewProject(strings.Join(args[2:], " "))
		if err != nil {
			return err
		}
		if err := store.CreateProject(ctx, p); err != nil {
			return err
		}
		if asJSON {
			return writeJSON(out, p)
		}
		fmt.Fprintf(out, "%s  %s\n", p.ID, p.Title)
		return nil

	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: poweur-tasks add <project-id> <title>")
		}
		t, err := NewTask(strings.Join(args[2:], " "))
		if err != nil {
			return err
		}
		if err := store.PutTask(ctx, args[1], t); err != nil {
			return err
		}
		if asJSON {
			return writeJSON(out, t)
		}
		fmt.Fprintf(out, "%s  %s\n", t.ID, t.Title)
		return nil

	case "list":
		if len(args) != 2 {
			return fmt.Errorf("usage: poweur-tasks list <project-id>")
		}
		tasks, skipped, err := store.ListTasks(ctx, args[1])
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(out, tasks)
		}
		if len(tasks) == 0 {
			fmt.Fprintln(out, "no tasks")
		}
		for _, t := range tasks {
			fmt.Fprintln(out, formatTask(t))
		}
		for _, s := range skipped {
			fmt.Fprintf(out, "(skipped unreadable task %s)\n", s)
		}
		return nil

	case "show":
		if len(args) != 3 {
			return fmt.Errorf("usage: poweur-tasks show <project-id> <task-id>")
		}
		t, err := store.GetTask(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		raw, err := t.MarshalDocument()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(raw))
		return nil

	case "done":
		if len(args) != 3 {
			return fmt.Errorf("usage: poweur-tasks done <project-id> <task-id>")
		}
		return applyUpdates(ctx, store, args[1], args[2], []string{"status=done"}, asJSON, out)

	case "set":
		if len(args) < 4 {
			return fmt.Errorf("usage: poweur-tasks set <project-id> <task-id> <key=value>...")
		}
		return applyUpdates(ctx, store, args[1], args[2], args[3:], asJSON, out)

	case "rm":
		if len(args) != 3 {
			return fmt.Errorf("usage: poweur-tasks rm <project-id> <task-id>")
		}
		if err := store.DeleteTask(ctx, args[1], args[2]); err != nil {
			return err
		}
		fmt.Fprintf(out, "deleted %s\n", args[2])
		return nil
	}
	return fmt.Errorf("unknown command %q (try `poweur-tasks help`)", args[0])
}

// applyUpdates is a read-modify-write. It is the operation PCP-0007's
// unknown-field rule exists for: whatever another implementation wrote into
// this document has to still be there afterwards.
func applyUpdates(ctx context.Context, store *Store, projectID, taskID string, pairs []string, asJSON bool, out io.Writer) error {
	t, err := store.GetTask(ctx, projectID, taskID)
	if err != nil {
		return err
	}
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("expected key=value, got %q", kv)
		}
		if err := setField(&t, strings.TrimSpace(k), v); err != nil {
			return err
		}
	}
	t.Updated = Now()
	if err := store.PutTask(ctx, projectID, t); err != nil {
		return err
	}
	if asJSON {
		return writeJSON(out, t)
	}
	fmt.Fprintln(out, formatTask(t))
	return nil
}

func setField(t *Task, key, value string) error {
	switch key {
	case "status":
		if !KnownStatus(value) {
			return fmt.Errorf("status must be one of todo, doing, done, cancelled")
		}
		t.SetStatus(value)
	case "title":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("title must not be empty")
		}
		t.Title = value
	case "notes":
		t.Notes = value
	case "due":
		t.Due = value
	case "start":
		t.Start = value
	case "assignee":
		t.Assignee = value
	case "parent":
		t.Parent = value
	case "priority":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("priority must be an integer 0-9")
		}
		t.Priority = n
	case "tags":
		if strings.TrimSpace(value) == "" {
			t.Tags = nil
			break
		}
		var tags []string
		for _, tag := range strings.Split(value, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				tags = append(tags, tag)
			}
		}
		t.Tags = tags
	default:
		return fmt.Errorf("unknown field %q (settable: status, title, notes, due, start, priority, tags, parent, assignee)", key)
	}
	return t.Validate()
}

func formatTask(t Task) string {
	mark := map[string]string{
		StatusTodo: "[ ]", StatusDoing: "[~]", StatusDone: "[x]", StatusCancelled: "[-]",
	}[DisplayStatus(t.Status)]
	line := fmt.Sprintf("%s %s  %s", mark, t.ID, t.Title)
	var meta []string
	if t.Priority > 0 {
		meta = append(meta, "p"+strconv.Itoa(t.Priority))
	}
	if t.Due != "" {
		meta = append(meta, "due "+t.Due)
	}
	if t.Assignee != "" {
		meta = append(meta, "@"+t.Assignee)
	}
	if len(t.Tags) > 0 {
		meta = append(meta, "#"+strings.Join(t.Tags, " #"))
	}
	if !KnownStatus(t.Status) {
		meta = append(meta, "status:"+t.Status+" (unknown, shown as todo)")
	}
	if len(meta) > 0 {
		line += "  (" + strings.Join(meta, ", ") + ")"
	}
	return line
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
