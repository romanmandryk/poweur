package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/poweur/cli/internal/config"
	driveclient "github.com/poweur/cli/internal/drive"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
	protocol "github.com/poweur/identity/drive"
)

func runDriveFiles(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("drive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	use := fs.String("use-identity", "", "identity")
	target := fs.String("drive", "", "another identity's drive, reached through your shares")
	jsonOut := fs.Bool("json", false, "JSON output")
	publish := fs.Bool("public", false, "mkdir: a public folder at the top of the drive, readable by anyone at https://<id>/pub/<name>/")
	offset := fs.Int64("offset", 0, "plaintext byte offset for get")
	length := fs.Int64("length", -1, "plaintext byte count for get; default is the rest of the file")
	baseVersion := fs.String("base", "", "put: the version this edit started from; if the file changed since, exit 3 (conflict) without writing")
	version := fs.String("version", "", "get: an earlier version (from drive history) instead of the latest")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true, "--public": true, "--force": true})) != nil {
		return 1
	}
	count := 1
	switch args[0] {
	case "put", "get", "mv":
		count = 2
	case "shared":
		count = 0
	}
	if fs.NArg() != count {
		fmt.Fprintln(stderr, "usage: poweur drive mkdir|rm|list <remote-path>; put <local-file> <remote-path> [--base <version>]; get <remote-path> <local-file> [--version <version>]; mv <remote-path> <remote-path>; shared --drive <identity>; every command takes --drive <identity> to work in a drive shared with you [--json]")
		return 1
	}
	files, ok := openDriveFiles(*use, *target, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	parent := func(remote string) (*driveclient.File, string, error) {
		remote = strings.TrimPrefix(remote, "/")
		at := strings.LastIndex(remote, "/")
		dir, base := "", remote
		if at >= 0 {
			dir, base = remote[:at], remote[at+1:]
		}
		base, e := protocol.NormalizeName(base)
		if e != nil {
			return nil, "", e
		}
		folder, e := files.Resolve(ctx, dir)
		return folder, base, e
	}
	var result any
	var err error
	var conflict *driveConflict
	run := func() error {
		switch args[0] {
		case "shared":
			// A member's paths start at these node IDs: /<node>/sub/path.
			shared, e := files.Shared(ctx)
			if e != nil {
				return e
			}
			rows := []map[string]any{}
			for _, file := range shared {
				rows = append(rows, map[string]any{"node": file.Manifest.Node, "kind": file.Manifest.Kind, "version": file.Manifest.Version})
			}
			result = rows
		case "list":
			folder, e := files.Resolve(ctx, fs.Arg(0))
			if e != nil {
				return e
			}
			children, e := files.List(ctx, folder)
			if e != nil {
				return e
			}
			rows := []map[string]any{}
			for _, child := range children {
				rows = append(rows, map[string]any{"node": child.Manifest.Node, "name": child.Name, "kind": child.Manifest.Kind, "version": child.Manifest.Version})
			}
			result = rows
		case "mkdir":
			folder, base, e := parent(fs.Arg(0))
			if e != nil {
				return e
			}
			create := files.Create
			if *publish {
				create = files.CreatePublic
			}
			file, e := create(ctx, folder, base, protocol.KindFolder, nil)
			if e != nil {
				return e
			}
			out := map[string]string{"node": file.Manifest.Node, "name": file.Name}
			if file.Public {
				out["public"] = "true"
			}
			result = out
		case "put":
			input, e := os.Open(fs.Arg(0))
			if e != nil {
				return e
			}
			defer input.Close()
			folder, base, e := parent(fs.Arg(1))
			if e != nil {
				return e
			}
			children, e := files.List(ctx, folder)
			if e != nil {
				return e
			}
			var target *driveclient.File
			for _, child := range children {
				if child.Name == base {
					target = child
					break
				}
			}
			if *baseVersion != "" && (target == nil || target.Manifest.Version != *baseVersion) {
				head := ""
				if target != nil {
					head = target.Manifest.Version
				}
				conflict = &driveConflict{Path: fs.Arg(1), Base: *baseVersion, Head: head}
				return conflict
			}
			if target == nil {
				target, e = files.Create(ctx, folder, base, protocol.KindFile, input)
			} else {
				e = files.Replace(ctx, target, input)
			}
			// The relay refuses a replace whose parent is no longer the head:
			// someone committed between our read and our write.
			var driveErr *driveclient.Error
			if *baseVersion != "" && errors.As(e, &driveErr) && driveErr.Status == 409 {
				conflict = &driveConflict{Path: fs.Arg(1), Base: *baseVersion}
				return conflict
			}
			if e != nil {
				return e
			}
			result = map[string]string{"node": target.Manifest.Node, "version": target.Manifest.Version, "name": target.Name}
		case "get":
			file, e := files.Resolve(ctx, fs.Arg(0))
			if e != nil {
				return e
			}
			output, e := os.CreateTemp(filepath.Dir(fs.Arg(1)), ".poweur-download-*")
			if e != nil {
				return e
			}
			defer os.Remove(output.Name())
			defer output.Close()
			if *version != "" {
				if *offset != 0 || *length >= 0 {
					return errors.New("--version cannot be combined with --offset or --length")
				}
				e = files.ReadVersion(ctx, file, *version, output)
			} else if *offset != 0 || *length >= 0 {
				e = files.ReadRange(ctx, file, *offset, *length, output)
			} else {
				e = files.Read(ctx, file, output)
			}
			if e != nil {
				return e
			}
			if e = output.Sync(); e != nil {
				return e
			}
			if e = output.Close(); e != nil {
				return e
			}
			if e = os.Rename(output.Name(), fs.Arg(1)); e != nil {
				return e
			}
			result = map[string]string{"node": file.Manifest.Node, "path": fs.Arg(1)}
		case "mv":
			file, e := files.Resolve(ctx, fs.Arg(0))
			if e != nil {
				return e
			}
			folder, base, e := parent(fs.Arg(1))
			if e != nil {
				return e
			}
			if e = files.Move(ctx, file, folder, base); e != nil {
				return e
			}
			result = map[string]string{"node": file.Manifest.Node, "name": file.Name}
		case "rm":
			file, e := files.Resolve(ctx, fs.Arg(0))
			if e != nil {
				return e
			}
			if e = files.Remove(ctx, file); e != nil {
				return e
			}
			result = map[string]string{"removed": file.Manifest.Node}
		}
		return nil
	}
	if err = run(); err != nil {
		if conflict != nil {
			if *jsonOut {
				_ = writeOutput(stdout, true, conflict, "")
			}
			fmt.Fprintln(stderr, conflict)
			return exitConflict
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, *jsonOut, result, fmt.Sprint(result))
}

// openDriveFiles opens a drive as the caller: their own, or with target
// another identity's drive (on that drive's relay), which they reach through
// the shares they hold. It never loads anyone else's private keys.
func openDriveFiles(use, target string, stderr io.Writer) (*driveclient.Files, bool) {
	cfg, name, key, ok := loadIdentityKey(use, stderr)
	if !ok {
		return nil, false
	}
	encryption, err := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, name))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, false
	}
	relay := cfg.RelayURL
	target = strings.ToLower(strings.TrimSpace(target))
	if target != "" && target != name {
		if relay, err = identityRelayURL(context.Background(), cfg, target); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, false
		}
	} else {
		target = ""
	}
	files := &driveclient.Files{Client: &driveclient.Client{Relay: relay, Identity: name, Drive: target, Key: key, Cache: driveclient.FileCache{Dir: filepath.Join(home, ".poweur", "cache", "drive-chunks")}}, EncryptionKey: encryption}
	files.Authors = func(author string) (ed25519.PublicKey, error) {
		res, err := identity.ResolveIdentity(context.Background(), author)
		if err != nil {
			return nil, err
		}
		return idpkg.ParseEd25519PublicKey(res.Document.PublicKey)
	}
	files.EncryptionKeys = func(ctx context.Context, member string) ([]byte, error) {
		res, err := identity.ResolveIdentity(ctx, member)
		if err != nil {
			return nil, err
		}
		return idpkg.ParseX25519PublicKey(res.Document.EncryptionPublicKey)
	}
	// Versions written by a group's members are checked against the group's
	// roster, which the caller can read only if they are in the group.
	files.GroupMembers = func(ctx context.Context, group string) ([]string, error) {
		groupRelay, err := identityRelayURL(ctx, cfg, group)
		if err != nil {
			return nil, err
		}
		roster, err := fetchGroupRoster(ctx, groupRelay, group, name, key)
		if err != nil {
			return nil, err
		}
		return append(append([]string(nil), roster.Members...), roster.Admins...), nil
	}
	return files, true
}

// identityRelayURL is the relay an identity's document names, as a URL.
func identityRelayURL(ctx context.Context, cfg config.Config, id string) (string, error) {
	res, err := identity.ResolveIdentity(ctx, id)
	if err != nil {
		return "", fmt.Errorf("cannot resolve the relay hosting %s: %w", id, err)
	}
	relay := strings.TrimRight(strings.TrimSpace(res.Document.Relay), "/")
	if relay == "" {
		return "", fmt.Errorf("%s names no relay", id)
	}
	if !strings.HasPrefix(relay, "http://") && !strings.HasPrefix(relay, "https://") {
		relay = schemeFromConfig(cfg) + "://" + relay
	}
	return relay, nil
}

// exitConflict is `drive put --base`'s exit code when the file moved on:
// read the new head, merge, and put again with it as the base.
const exitConflict = 3

type driveConflict struct {
	Path string `json:"path"`
	Base string `json:"base"`
	Head string `json:"head,omitempty"`
}

func (c *driveConflict) MarshalJSON() ([]byte, error) {
	type plain driveConflict
	return json.Marshal(struct {
		Error string `json:"error"`
		*plain
	}{"conflict", (*plain)(c)})
}

func (c *driveConflict) Error() string {
	if c.Head != "" {
		return fmt.Sprintf("conflict: %s changed since %s (now %s)", c.Path, c.Base, c.Head)
	}
	return fmt.Sprintf("conflict: %s changed since %s", c.Path, c.Base)
}
