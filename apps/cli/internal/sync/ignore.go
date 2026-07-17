package sync

import (
	"os"
	gopath "path"
	"path/filepath"
	"strings"
)

// Ignore matches paths against .poweurignore patterns — a gitignore-
// flavored subset: one glob per line, `#` comments, trailing `/` anchors a
// directory (its whole subtree is skipped), patterns without `/` match the
// basename anywhere, patterns with `/` match the full tree path. Negation
// (`!`) is not supported in v1.
type Ignore struct {
	patterns []string
}

// LoadIgnore reads root/.poweurignore (no file = empty matcher). The state
// and ignore files themselves are always ignored.
func LoadIgnore(root string) *Ignore {
	ig := &Ignore{}
	raw, err := os.ReadFile(filepath.Join(root, IgnoreFileName))
	if err != nil {
		return ig
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ig.patterns = append(ig.patterns, line)
	}
	return ig
}

// Match reports whether the clean tree path should be skipped.
func (ig *Ignore) Match(path string) bool {
	base := gopath.Base(path)
	if base == StateFileName || base == IgnoreFileName {
		return true
	}
	for _, pat := range ig.patterns {
		dirPat := strings.TrimSuffix(pat, "/")
		anchored := strings.Contains(dirPat, "/")
		// Directory patterns (or any match on a parent) skip the subtree.
		for probe := path; probe != "." && probe != ""; probe = gopath.Dir(probe) {
			target := probe
			if !anchored {
				target = gopath.Base(probe)
			}
			if ok, _ := gopath.Match(dirPat, target); ok || target == dirPat {
				return true
			}
			if probe == gopath.Dir(probe) {
				break
			}
		}
	}
	return false
}
