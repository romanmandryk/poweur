package cli

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	driveclient "github.com/poweur/cli/internal/drive"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
	protocol "github.com/poweur/identity/drive"
)

func runDriveFiles(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("drive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	use := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "JSON output")
	offset := fs.Int64("offset", 0, "plaintext byte offset for get")
	length := fs.Int64("length", -1, "plaintext byte count for get; default is the rest of the file")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})) != nil {
		return 1
	}
	count := 1
	if args[0] == "put" || args[0] == "get" || args[0] == "mv" {
		count = 2
	}
	if fs.NArg() != count {
		fmt.Fprintln(stderr, "usage: poweur drive mkdir|rm|list <remote-path>; put <local-file> <remote-path>; get <remote-path> <local-file>; mv <remote-path> <remote-path> [--json]")
		return 1
	}
	files, ok := openDriveFiles(*use, stderr)
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
	run := func() error {
		switch args[0] {
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
			file, e := files.Create(ctx, folder, base, protocol.KindFolder, nil)
			if e != nil {
				return e
			}
			result = map[string]string{"node": file.Manifest.Node, "name": file.Name}
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
			if target == nil {
				target, e = files.Create(ctx, folder, base, protocol.KindFile, input)
			} else {
				e = files.Replace(ctx, target, input)
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
			if *offset != 0 || *length >= 0 {
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
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, *jsonOut, result, fmt.Sprint(result))
}

func openDriveFiles(use string, stderr io.Writer) (*driveclient.Files, bool) {
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
	files := &driveclient.Files{Client: &driveclient.Client{Relay: cfg.RelayURL, Identity: name, Key: key, Cache: driveclient.FileCache{Dir: filepath.Join(home, ".poweur", "cache", "drive-chunks")}}, EncryptionKey: encryption}
	files.Authors = func(author string) (ed25519.PublicKey, error) {
		res, err := identity.ResolveIdentity(context.Background(), author)
		if err != nil {
			return nil, err
		}
		return idpkg.ParseEd25519PublicKey(res.Document.PublicKey)
	}
	return files, true
}
