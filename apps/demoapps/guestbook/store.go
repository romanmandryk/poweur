package guestbook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/poweur/cli/pkg/cli"
)

// Store is where the log lives: a set of named Markdown files.
type Store interface {
	// List returns the names of the files that exist.
	List(ctx context.Context) ([]string, error)
	// Read returns a file, or an error wrapping fs.ErrNotExist.
	Read(ctx context.Context, name string) ([]byte, error)
	// Write replaces a file, creating it if needed.
	Write(ctx context.Context, name string, data []byte) error
}

var pageNameRE = regexp.MustCompile(`^entries-(\d{6})\.md$`)

// pageName is the file holding page n of the log. Zero-padded so a listing
// sorts in the order the pages were written.
func pageName(n int) string { return fmt.Sprintf("entries-%06d.md", n) }

func parsePageName(name string) (int, bool) {
	m := pageNameRE.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n > 0
}

// MemStore keeps the log in memory. It is what tests and a local run use.
type MemStore struct {
	mu    sync.Mutex
	files map[string][]byte
}

func NewMemStore() *MemStore { return &MemStore{files: map[string][]byte{}} }

func (m *MemStore) List(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.files))
	for name := range m.files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (m *MemStore) Read(_ context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[name]
	if !ok {
		return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
	}
	return append([]byte(nil), data...), nil
}

func (m *MemStore) Write(_ context.Context, name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[name] = append([]byte(nil), data...)
	return nil
}

// DriveStore keeps the log in a public folder of the guestbook's own Poweur
// drive. It does what an operator would do by hand, with `poweur drive` run
// as the guestbook's ID, so the log is an ordinary drive folder anyone with
// the ID's keys can also edit: `poweur drive get`, delete an entry, `drive put`.
// Public means https://<identity>/pub/<folder>/<file> serves it to anyone.
type DriveStore struct {
	// Identity is the guestbook's own ID, e.g. guestbook.poweur.net. Its keys
	// and relay come from the CLI's configuration (KEYS_DIR, RELAY_URL).
	Identity string
	// Folder is the public folder at the top of the drive (default "guestbook").
	Folder string
	// Run executes one CLI invocation. Default: the real CLI.
	Run func(args []string, stdout, stderr io.Writer) int

	mu sync.Mutex
}

func (d *DriveStore) folder() string {
	if d.Folder == "" {
		return "guestbook"
	}
	return d.Folder
}

func (d *DriveStore) run(args ...string) (string, error) {
	run := d.Run
	if run == nil {
		run = cli.Run
	}
	args = append(args, "--use-identity", d.Identity)
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		return "", &cliError{args: args, code: code, stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.String(), nil
}

type cliError struct {
	args   []string
	code   int
	stderr string
}

func (e *cliError) Error() string {
	return fmt.Sprintf("poweur %s exited %d: %s", strings.Join(e.args[:2], " "), e.code, e.stderr)
}

type driveRow struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (d *DriveStore) list(path string) ([]driveRow, error) {
	out, err := d.run("drive", "list", path, "--json")
	if err != nil {
		return nil, err
	}
	var rows []driveRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("guestbook: unreadable drive listing: %w", err)
	}
	return rows, nil
}

// Init creates the public folder if the drive has none yet.
func (d *DriveStore) Init(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.list("/")
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Name == d.folder() {
			if row.Kind != "folder" {
				return fmt.Errorf("guestbook: /%s in %s's drive is not a folder", d.folder(), d.Identity)
			}
			return nil
		}
	}
	_, err = d.run("drive", "mkdir", "/"+d.folder(), "--public", "--json")
	return err
}

func (d *DriveStore) List(context.Context) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.list("/" + d.folder())
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Kind == "file" {
			names = append(names, row.Name)
		}
	}
	return names, nil
}

func (d *DriveStore) Read(_ context.Context, name string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tmp, err := os.CreateTemp("", "guestbook-read-*")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if _, err := d.run("drive", "get", "/"+d.folder()+"/"+name, tmp.Name(), "--json"); err != nil {
		if ce, ok := err.(*cliError); ok && strings.Contains(ce.stderr, "not found") {
			return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
		}
		return nil, err
	}
	return os.ReadFile(tmp.Name())
}

func (d *DriveStore) Write(_ context.Context, name string, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tmp, err := os.CreateTemp("", "guestbook-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_, err = d.run("drive", "put", tmp.Name(), "/"+d.folder()+"/"+name, "--json")
	return err
}
