package tasks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeDAV is a small in-memory stand-in for the relay's WebDAV endpoint. It
// exists so the unit tests can exercise Store and the CLI without a relay;
// the *real* endpoint is covered end to end by apps/integration/tasks_test.go.
//
// It deliberately reproduces the two relay behaviours this app depends on:
//   - a token whose scope does not cover the path gets 403;
//   - /apps/<app-id>/manifest.json is validated on write and its app_id must
//     match the directory (E06-T3).
type fakeDAV struct {
	mu    sync.Mutex
	files map[string][]byte  // tree path -> body
	dirs  map[string]bool    // tree path -> exists
	owner string             // identity whose home this is
	token string             // the only accepted bearer token ("" = no auth)
	scope string             // path prefix the token covers ("" = whole tree)
	calls []string           // "METHOD /path", for assertions
	fail  map[string]int     // tree path -> status to force
	raw   map[string]string  // tree path -> body to serve verbatim (corrupt docs)
}

func newFakeDAV(t *testing.T, owner string) (*fakeDAV, *httptest.Server) {
	t.Helper()
	f := &fakeDAV{
		files: map[string][]byte{},
		dirs:  map[string]bool{"/": true, "/apps": true},
		owner: owner,
		fail:  map[string]int{},
		raw:   map[string]string{},
	}
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return f, ts
}

func (f *fakeDAV) client(base string) *DAVClient {
	return &DAVClient{BaseURL: base, Owner: f.owner, Token: f.token}
}

func (f *fakeDAV) store(base string) *Store {
	return &Store{DAV: f.client(base)}
}

// put seeds a document as some *other* implementation would have written it.
func (f *fakeDAV) put(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = body
	for d := parentDir(path); d != "/" && d != "."; d = parentDir(d) {
		f.dirs[d] = true
	}
}

func (f *fakeDAV) get(path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[path]
}

func (f *fakeDAV) methodCalls(method string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, method+" ") {
			out = append(out, strings.TrimPrefix(c, method+" "))
		}
	}
	return out
}

func parentDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	prefix := "/dav/" + f.owner
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "wrong home", http.StatusNotFound)
		return
	}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if path == "" {
		path = "/"
	}

	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+path)
	forced := f.fail[path]
	f.mu.Unlock()
	if forced != 0 {
		http.Error(w, "forced failure", forced)
		return
	}

	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	if f.scope != "" && path != "/" && !strings.HasPrefix(path+"/", f.scope) && path+"/" != f.scope {
		http.Error(w, "token scope does not cover "+path, http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		f.serveGet(w, path)
	case http.MethodPut:
		f.servePut(w, r, path)
	case http.MethodDelete:
		f.serveDelete(w, path)
	case "MKCOL":
		f.serveMkcol(w, path)
	case "PROPFIND":
		f.servePropfind(w, path)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func (f *fakeDAV) serveGet(w http.ResponseWriter, path string) {
	f.mu.Lock()
	body, ok := f.files[path]
	verbatim, isRaw := f.raw[path]
	f.mu.Unlock()
	if isRaw {
		w.Write([]byte(verbatim))
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (f *fakeDAV) servePut(w http.ResponseWriter, r *http.Request, path string) {
	body := make([]byte, 0, 1024)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	// E06-T3: the relay validates /apps/<app-id>/manifest.json on write.
	if strings.HasSuffix(path, "/manifest.json") && strings.Count(path, "/") == 3 {
		var m Manifest
		if err := json.Unmarshal(body, &m); err != nil || strings.TrimSpace(m.Name) == "" {
			http.Error(w, "invalid manifest.json", http.StatusUnprocessableEntity)
			return
		}
		appDir := strings.Split(strings.TrimPrefix(path, "/"), "/")[1]
		if !strings.EqualFold(m.AppID, appDir) {
			http.Error(w, fmt.Sprintf("manifest app_id %q must match its directory %q", m.AppID, appDir),
				http.StatusUnprocessableEntity)
			return
		}
	}
	f.mu.Lock()
	if !f.dirs[parentDir(path)] {
		f.mu.Unlock()
		http.Error(w, "parent collection missing", http.StatusConflict)
		return
	}
	_, existed := f.files[path]
	f.files[path] = body
	f.mu.Unlock()
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeDAV) serveDelete(w http.ResponseWriter, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[path]; !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	delete(f.files, path)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeDAV) serveMkcol(w http.ResponseWriter, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dirs[path] {
		http.Error(w, "exists", http.StatusMethodNotAllowed) // RFC 4918
		return
	}
	if !f.dirs[parentDir(path)] {
		http.Error(w, "parent missing", http.StatusConflict)
		return
	}
	f.dirs[path] = true
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeDAV) servePropfind(w http.ResponseWriter, path string) {
	f.mu.Lock()
	if !f.dirs[path] {
		f.mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	children := map[string]bool{} // name -> isCollection
	for p := range f.files {
		if parentDir(p) == path {
			children[p[len(path)+1:]] = false
		}
	}
	for d := range f.dirs {
		if d != path && parentDir(d) == path {
			children[d[len(path)+1:]] = true
		}
	}
	f.mu.Unlock()

	names := make([]string, 0, len(children))
	for n := range children {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`)
	writeResp := func(href string, isColl bool) {
		rt := ""
		if isColl {
			rt = "<D:collection/>"
		}
		fmt.Fprintf(&b, `<D:response><D:href>/dav/%s%s</D:href><D:propstat><D:prop>`+
			`<D:resourcetype>%s</D:resourcetype></D:prop>`+
			`<D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, f.owner, href, rt)
	}
	writeResp(path, true)
	for _, n := range names {
		writeResp(path+"/"+n, children[n])
	}
	b.WriteString(`</D:multistatus>`)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusMultiStatus)
	w.Write([]byte(b.String()))
}
