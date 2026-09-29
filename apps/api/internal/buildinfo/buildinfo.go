// Package buildinfo is the relay's release identity.
//
// Version is the semver agents bump on apps/api changes (Go modules do not
// store this module's version in go.mod). Time and Hash are filled from VCS
// metadata at `go build`, or from BUILD_TIME / VERSION_HASH at runtime.
package buildinfo

import (
	"runtime/debug"
	"strings"
	"time"
)

// Version is the relay semver. Bump the patch when shipping apps/api changes.
var Version = "0.2.1"

// Time is a build timestamp (RFC3339 or "2006-01-02 15:04"). Hash is a git
// revision. Both may be empty in a stripped test binary.
var (
	Time string
	Hash string
)

func init() {
	fillFromVCS()
}

func fillFromVCS() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if Hash == "" {
				Hash = s.Value
			}
		case "vcs.time":
			if Time == "" {
				Time = s.Value
			}
		}
	}
}

// FormatTime turns a raw stamp into UTC "2006-01-02 15:04". Unknown layouts
// are returned trimmed, so a hand-written value still shows up.
func FormatTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format("2006-01-02 15:04")
		}
	}
	return raw
}
