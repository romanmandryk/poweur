// Package migrate converts a storage-v1 data directory into the v2 layout
// (EPIC-020 E20-T12). It is a one-shot operator tool: run it with the relay
// stopped, against the relay's POWEUR_DATA.
//
// Only plaintext system data moves: each identity's signed id.json, profile
// (with its avatar), capabilities, contacts, inbox policy, analytics
// consent, device registry, connected apps and group roster; the key-backup
// keystore; and undelivered mail and acks. v1 files, shares, links and
// message history are not migrated (they were never end-to-end encrypted,
// and v2 cannot adopt them without the owner's keys). Documents that do not
// validate are reported and skipped, never half-written.
//
// It is idempotent: v2 writes of identical content are no-ops, so an
// interrupted run is simply run again. A dry run reads everything and
// writes nothing. After a complete run the v1 trees are renamed to
// <name>.v1-backup (not deleted) so an operator can inspect or roll back.
package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/poweur/api/internal/drive/engine"
	"github.com/poweur/api/internal/drive/provider/fs"
	idpkg "github.com/poweur/identity"
)

// Options configure a run.
type Options struct {
	DataDir string
	DryRun  bool
	// KeepSource leaves the v1 trees in place instead of renaming them to
	// *.v1-backup after a complete run.
	KeepSource bool
	Log        io.Writer
}

// Report is what a run did (or, dry, would do).
type Report struct {
	Identities []IdentityReport `json:"identities"`
	Keystores  int              `json:"keystores"`
	Messages   int              `json:"messages"`
	Acks       int              `json:"acks"`
	Skipped    []string         `json:"skipped,omitempty"`
	BackedUp   []string         `json:"backed_up,omitempty"`
}

// IdentityReport lists the system documents migrated for one identity.
type IdentityReport struct {
	Identity string   `json:"identity"`
	Files    []string `json:"files"`
	Dropped  int      `json:"dropped_v1_files"`
}

// v1 → v2 system document paths (relative to an identity's v1 home).
var systemDocs = []struct {
	from, to string
	check    func([]byte) error
}{
	{"poweur-sys/public/capabilities.json", ".poweur/public/capabilities.json", func(b []byte) error { _, err := idpkg.ParseCapabilities(b); return err }},
	{"poweur-sys/relay/contacts.json", ".poweur/relay/contacts.json", func(b []byte) error { _, err := idpkg.ParseContactsFile(b); return err }},
	{"poweur-sys/relay/inbox-policy.json", ".poweur/relay/inbox-policy.json", func(b []byte) error { _, err := idpkg.ParseInboxPolicy(b); return err }},
	{"poweur-sys/relay/analytics.json", ".poweur/relay/analytics.json", validJSONObject},
	{"poweur-sys/relay/connected-apps.json", idpkg.ConnectedAppsPath, func(b []byte) error { _, err := idpkg.ParseConnectedApps(b); return err }},
	{"poweur-sys/relay/devices.json", ".poweur/state/devices.json", func(b []byte) error { _, err := idpkg.ParseDevicesFile(b); return err }},
	{"poweur-sys/relay/groups/self.json", ".poweur/relay/group.json", func(b []byte) error {
		gr, err := idpkg.ParseShareGroup(b)
		if err == nil && !gr.IsGroupIdentity() {
			err = fmt.Errorf("not a group identity roster")
		}
		return err
	}},
}

func validJSONObject(b []byte) error {
	var v map[string]any
	return json.Unmarshal(b, &v)
}

// Run migrates opts.DataDir in place.
func Run(ctx context.Context, opts Options) (Report, error) {
	var report Report
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			fmt.Fprintf(opts.Log, format+"\n", args...)
		}
	}
	root := opts.DataDir
	if root == "" {
		return report, fmt.Errorf("no data directory")
	}
	store, err := fs.Open(root)
	if err != nil {
		return report, err
	}
	eng := engine.New(engine.Options{Store: store})
	put := func(key string, data []byte) error {
		if opts.DryRun {
			return nil
		}
		_, err := store.Put(ctx, key, data)
		return err
	}
	system := func(identity, path string, data []byte) error {
		if opts.DryRun {
			return nil
		}
		writer := engine.WriterOwner
		if strings.HasPrefix(path, ".poweur/state/") {
			writer = engine.WriterRelay
		}
		_, err := eng.SystemWrite(ctx, identity, path, data, writer, nil)
		return err
	}

	homes, err := os.ReadDir(filepath.Join(root, "identities"))
	if err != nil && !os.IsNotExist(err) {
		return report, err
	}
	for _, home := range homes {
		if !home.IsDir() {
			continue
		}
		dir := filepath.Join(root, "identities", home.Name())
		ir, err := migrateIdentity(dir, home.Name(), put, system, &report)
		if err != nil {
			return report, fmt.Errorf("%s: %w", home.Name(), err)
		}
		if ir.Identity != "" {
			report.Identities = append(report.Identities, ir)
			logf("identity %s: %s (%d v1 files dropped)", ir.Identity, strings.Join(ir.Files, ", "), ir.Dropped)
		}
	}

	// Key backups: one document per identity, format unchanged.
	if entries, err := os.ReadDir(filepath.Join(root, "keystore")); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, "keystore", e.Name()))
			if err != nil {
				return report, err
			}
			var entries []json.RawMessage
			if json.Unmarshal(raw, &entries) != nil {
				report.Skipped = append(report.Skipped, "keystore/"+e.Name()+": not a JSON array")
				continue
			}
			if err := put("relay/keystore/"+e.Name(), raw); err != nil {
				return report, err
			}
			report.Keystores++
		}
	}

	// Undelivered mail and acks: one object per entry, format unchanged.
	for _, queue := range []string{"messages", "acks"} {
		base := filepath.Join(root, "spool", queue)
		idDirs, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, idDir := range idDirs {
			if !idDir.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(base, idDir.Name()))
			if err != nil {
				return report, err
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(base, idDir.Name(), f.Name()))
				if err != nil {
					return report, err
				}
				if validJSONObject(raw) != nil {
					report.Skipped = append(report.Skipped, "spool/"+queue+"/"+idDir.Name()+"/"+f.Name()+": corrupt")
					continue
				}
				if err := put("relay/spool/"+queue+"/"+idDir.Name()+"/"+f.Name(), raw); err != nil {
					return report, err
				}
				if queue == "messages" {
					report.Messages++
				} else {
					report.Acks++
				}
			}
		}
	}

	if !opts.DryRun && !opts.KeepSource {
		for _, name := range []string{"identities", "spool", "keystore"} {
			from := filepath.Join(root, name)
			if _, err := os.Stat(from); err != nil {
				continue
			}
			to := from + ".v1-backup"
			if _, err := os.Stat(to); err == nil {
				to = fmt.Sprintf("%s.v1-backup-%d", from, os.Getpid())
			}
			if err := os.Rename(from, to); err != nil {
				return report, err
			}
			report.BackedUp = append(report.BackedUp, filepath.Base(to))
		}
	}
	sort.Strings(report.Skipped)
	for _, s := range report.Skipped {
		logf("skipped %s", s)
	}
	logf("keystores %d, undelivered messages %d, acks %d", report.Keystores, report.Messages, report.Acks)
	return report, nil
}

func migrateIdentity(dir, dirName string, put func(string, []byte) error, system func(string, string, []byte) error, report *Report) (IdentityReport, error) {
	var ir IdentityReport
	raw, err := os.ReadFile(filepath.Join(dir, "poweur-sys/public/id.json"))
	if os.IsNotExist(err) {
		report.Skipped = append(report.Skipped, "identities/"+dirName+": no id.json")
		return ir, nil
	}
	if err != nil {
		return ir, err
	}
	doc, err := idpkg.ParseDocument(raw, true)
	if err != nil {
		report.Skipped = append(report.Skipped, "identities/"+dirName+": invalid id.json: "+err.Error())
		return ir, nil
	}
	identity := strings.ToLower(doc.Identity)
	if sanitized, err := idpkg.SanitizeIdentityDirName(identity); err != nil || sanitized != dirName {
		report.Skipped = append(report.Skipped, "identities/"+dirName+": id.json names "+identity)
		return ir, nil
	}
	ir.Identity = identity
	if err := put("relay/identities/"+dirName+".json", raw); err != nil {
		return ir, err
	}
	if err := system(identity, ".poweur/public/id.json", raw); err != nil {
		return ir, err
	}
	ir.Files = append(ir.Files, "id.json")
	migrated := map[string]bool{"poweur-sys/public/id.json": true}

	// The profile names its avatar by a path in the v1 home; v2 keeps it
	// beside the profile as avatar.<ext>.
	if profileRaw, err := os.ReadFile(filepath.Join(dir, "poweur-sys/public/profile.json")); err == nil {
		migrated["poweur-sys/public/profile.json"] = true
		var profile map[string]any
		if json.Unmarshal(profileRaw, &profile) != nil {
			report.Skipped = append(report.Skipped, identity+" profile.json: invalid JSON")
		} else {
			if ref, _ := profile["avatar"].(string); ref != "" {
				delete(profile, "avatar")
				ext := strings.ToLower(filepath.Ext(ref))
				clean := filepath.Clean("/" + ref)[1:]
				image, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(clean)))
				switch {
				case !idpkg.ValidAvatarName("avatar" + ext):
					report.Skipped = append(report.Skipped, identity+" avatar "+ref+": unsupported type")
				case readErr != nil:
					report.Skipped = append(report.Skipped, identity+" avatar "+ref+": file missing")
				default:
					migrated[clean] = true
					if err := system(identity, ".poweur/public/avatar"+ext, image); err != nil {
						return ir, err
					}
					profile["avatar"] = "avatar" + ext
					ir.Files = append(ir.Files, "avatar"+ext)
				}
			}
			converted, _ := json.Marshal(profile)
			if _, err := idpkg.ParseProfile(converted); err != nil {
				report.Skipped = append(report.Skipped, identity+" profile.json: "+err.Error())
			} else {
				if err := system(identity, ".poweur/public/profile.json", converted); err != nil {
					return ir, err
				}
				ir.Files = append(ir.Files, "profile.json")
			}
		}
	}
	for _, d := range systemDocs {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(d.from)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ir, err
		}
		migrated[d.from] = true
		if err := d.check(b); err != nil {
			report.Skipped = append(report.Skipped, identity+" "+d.from+": "+err.Error())
			continue
		}
		if err := system(identity, d.to, b); err != nil {
			return ir, err
		}
		ir.Files = append(ir.Files, filepath.Base(d.to))
	}
	_ = filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			if !migrated[filepath.ToSlash(rel)] {
				ir.Dropped++
			}
		}
		return nil
	})
	return ir, nil
}
