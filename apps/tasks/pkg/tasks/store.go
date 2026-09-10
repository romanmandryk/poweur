package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Store maps PCP-0007's layout onto a home's file tree.
//
//	/apps/net.poweur.tasks/manifest.json
//	/apps/net.poweur.tasks/projects/<project-id>/project.json
//	/apps/net.poweur.tasks/projects/<project-id>/tasks/<task-id>.json
type Store struct {
	DAV *DAVClient
}

// NamespaceRoot is the app's slice of the home. Everything this app touches
// is under here, and the token it asks for is scoped to exactly this prefix.
const NamespaceRoot = "/apps/" + AppID

// TokenScope is the DAV scope a user must mint for this app.
const TokenScope = "dav:rw:" + NamespaceRoot + "/"

func projectDir(projectID string) (string, error) {
	if err := ValidateID(projectID); err != nil {
		return "", fmt.Errorf("project id: %w", err)
	}
	return NamespaceRoot + "/projects/" + projectID, nil
}

func projectDocPath(projectID string) (string, error) {
	dir, err := projectDir(projectID)
	if err != nil {
		return "", err
	}
	return dir + "/project.json", nil
}

func taskDocPath(projectID, taskID string) (string, error) {
	dir, err := projectDir(projectID)
	if err != nil {
		return "", err
	}
	if err := ValidateID(taskID); err != nil {
		return "", fmt.Errorf("task id: %w", err)
	}
	return dir + "/tasks/" + taskID + ".json", nil
}

// Init writes the mandatory manifest and the projects/ collection. Safe to
// re-run; the relay validates the manifest on write (E06-T3) and rejects an
// app_id that does not match the directory.
func (s *Store) Init(ctx context.Context) error {
	if err := s.DAV.Mkcol(ctx, "/apps"); err != nil {
		return err
	}
	if err := s.DAV.Mkcol(ctx, NamespaceRoot); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(DefaultManifest(), "", "  ")
	if err != nil {
		return err
	}
	if err := s.DAV.Put(ctx, NamespaceRoot+"/manifest.json", raw); err != nil {
		return err
	}
	return s.DAV.Mkcol(ctx, NamespaceRoot+"/projects")
}

// Manifest reads the namespace's manifest.
func (s *Store) Manifest(ctx context.Context) (Manifest, error) {
	raw, err := s.DAV.Get(ctx, NamespaceRoot+"/manifest.json")
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("invalid manifest.json: %w", err)
	}
	return m, nil
}

// CreateProject writes a new project document and its tasks/ collection.
func (s *Store) CreateProject(ctx context.Context, p Project) error {
	dir, err := projectDir(p.ID)
	if err != nil {
		return err
	}
	if err := s.DAV.Mkcol(ctx, dir); err != nil {
		return err
	}
	if err := s.PutProject(ctx, p); err != nil {
		return err
	}
	return s.DAV.Mkcol(ctx, dir+"/tasks")
}

// PutProject writes a project document.
func (s *Store) PutProject(ctx context.Context, p Project) error {
	path, err := projectDocPath(p.ID)
	if err != nil {
		return err
	}
	raw, err := p.MarshalDocument()
	if err != nil {
		return err
	}
	return s.DAV.Put(ctx, path, raw)
}

// GetProject reads one project document.
func (s *Store) GetProject(ctx context.Context, projectID string) (Project, error) {
	path, err := projectDocPath(projectID)
	if err != nil {
		return Project{}, err
	}
	raw, err := s.DAV.Get(ctx, path)
	if err != nil {
		return Project{}, err
	}
	p, err := ParseProject(raw)
	if err != nil {
		return Project{}, err
	}
	if !strings.EqualFold(p.ID, projectID) {
		return Project{}, fmt.Errorf("project.json id %q does not match its directory %q", p.ID, projectID)
	}
	return p, nil
}

// ListProjects enumerates the namespace. PCP-0007 compatibility: a directory
// whose project.json is missing or unparseable is *skipped*, not deleted and
// not fatal — that shape is a partially-synced project, not a corrupt one.
func (s *Store) ListProjects(ctx context.Context) ([]Project, []string, error) {
	entries, err := s.DAV.List(ctx, NamespaceRoot+"/projects")
	if err != nil {
		if NotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var out []Project
	var skipped []string
	for _, e := range entries {
		if !e.IsCollection {
			continue // an unknown file next to the project dirs: leave it alone
		}
		p, err := s.GetProject(ctx, e.Name)
		if err != nil {
			skipped = append(skipped, e.Name)
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, skipped, nil
}

// PutTask writes a task document.
func (s *Store) PutTask(ctx context.Context, projectID string, t Task) error {
	path, err := taskDocPath(projectID, t.ID)
	if err != nil {
		return err
	}
	raw, err := t.MarshalDocument()
	if err != nil {
		return err
	}
	return s.DAV.Put(ctx, path, raw)
}

// GetTask reads one task document.
func (s *Store) GetTask(ctx context.Context, projectID, taskID string) (Task, error) {
	path, err := taskDocPath(projectID, taskID)
	if err != nil {
		return Task{}, err
	}
	raw, err := s.DAV.Get(ctx, path)
	if err != nil {
		return Task{}, err
	}
	t, err := ParseTask(raw)
	if err != nil {
		return Task{}, err
	}
	if !strings.EqualFold(t.ID, taskID) {
		return Task{}, fmt.Errorf("task id %q does not match its filename %q", t.ID, taskID)
	}
	return t, nil
}

// DeleteTask removes a task document.
func (s *Store) DeleteTask(ctx context.Context, projectID, taskID string) error {
	path, err := taskDocPath(projectID, taskID)
	if err != nil {
		return err
	}
	return s.DAV.Delete(ctx, path)
}

// ListTasks reads every task in a project. Unparseable documents are reported
// by name rather than swallowed or fatal.
func (s *Store) ListTasks(ctx context.Context, projectID string) ([]Task, []string, error) {
	dir, err := projectDir(projectID)
	if err != nil {
		return nil, nil, err
	}
	entries, err := s.DAV.List(ctx, dir+"/tasks")
	if err != nil {
		if NotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var out []Task
	var skipped []string
	for _, e := range entries {
		if e.IsCollection || !strings.HasSuffix(e.Name, ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name, ".json")
		t, err := s.GetTask(ctx, projectID, id)
		if err != nil {
			skipped = append(skipped, e.Name)
			continue
		}
		out = append(out, t)
	}
	SortTasks(out)
	return out, skipped, nil
}
