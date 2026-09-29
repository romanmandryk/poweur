package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	driveclient "github.com/poweur/cli/internal/drive"
	"github.com/poweur/cli/internal/identity"
	protocol "github.com/poweur/identity/drive"
)

func runDriveOps(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: poweur drive history|tail <path> [--from=1]; append <path> <file>; trim <log> <snapshot>; watch; share add <path> <member>|rm <id>|ls; link create <path>|rm <id>; transfer <path> --to <drive> [--into </shared-node-id/path>|--to-node <id>]; any command takes --drive <identity> [--json]"
	fs := flag.NewFlagSet("drive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	use := fs.String("use-identity", "", "identity")
	target := fs.String("drive", "", "another identity's drive, reached through your shares")
	jsonOut := fs.Bool("json", false, "JSON output")
	from := fs.Uint64("from", 0, "first record position")
	role := fs.String("role", "read", "share or link role")
	to := fs.String("to", "", "destination drive")
	toNode := fs.String("to-node", "", "node id already created on the destination")
	into := fs.String("into", "", "destination folder on --to: /<shared-node-id>[/path] as a member, or a path when --to is your own drive")
	password := fs.String("password", "", "link password")
	expires := fs.String("expires", "", "RFC3339 expiry")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})) != nil {
		return 1
	}
	ctx := context.Background()
	fail := func(err error) int {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch args[0] {
	case "watch":
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		cfg, name, key, ok := loadIdentityKey(*use, stderr)
		if !ok {
			return 1
		}
		client := &driveclient.Client{Relay: cfg.RelayURL, Identity: name, Key: key}
		err := client.Subscribe(ctx, func(event driveclient.Event) error {
			if *jsonOut {
				raw, _ := json.Marshal(event)
				fmt.Fprintln(stdout, string(raw))
				return nil
			}
			fmt.Fprintf(stdout, "%s %s\n", event.Type, event.Timestamp)
			return nil
		})
		if err != nil && ctx.Err() == nil {
			return fail(err)
		}
		return 0
	case "share":
		if fs.NArg() < 1 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		files, ok := openDriveFiles(*use, *target, stderr)
		if !ok {
			return 1
		}
		switch fs.Arg(0) {
		case "ls":
			shares, err := files.Client.Shares(ctx)
			if err != nil {
				return fail(err)
			}
			return writeOutput(stdout, *jsonOut, map[string]any{"shares": shares}, fmt.Sprint(len(shares)))
		case "rm":
			if fs.NArg() != 2 {
				fmt.Fprintln(stderr, usage)
				return 1
			}
			if err := files.Client.Unshare(ctx, fs.Arg(1)); err != nil {
				return fail(err)
			}
			return writeOutput(stdout, *jsonOut, map[string]string{"removed": fs.Arg(1)}, fs.Arg(1)+"\n")
		case "add":
			if fs.NArg() != 3 {
				fmt.Fprintln(stderr, usage)
				return 1
			}
			file, err := files.Resolve(ctx, fs.Arg(1))
			if err != nil {
				return fail(err)
			}
			memberKey, err := identity.LookupEncryptionKey(ctx, fs.Arg(2))
			if err != nil || len(memberKey) != 32 {
				if err == nil {
					err = fmt.Errorf("no encryption key for %s", fs.Arg(2))
				}
				return fail(err)
			}
			share, err := files.ShareWith(ctx, file, fs.Arg(2), memberKey, *role, *expires)
			if err != nil {
				return fail(err)
			}
			result := map[string]string{"id": share.ID, "node": share.Node, "member": share.Member, "role": share.Role}
			return writeOutput(stdout, *jsonOut, result, share.ID+"\n")
		default:
			fmt.Fprintln(stderr, usage)
			return 1
		}
	case "link":
		if fs.NArg() < 1 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		files, ok := openDriveFiles(*use, *target, stderr)
		if !ok {
			return 1
		}
		switch fs.Arg(0) {
		case "rm":
			if fs.NArg() != 2 {
				fmt.Fprintln(stderr, usage)
				return 1
			}
			if err := files.Client.Unshare(ctx, fs.Arg(1)); err != nil {
				return fail(err)
			}
			return writeOutput(stdout, *jsonOut, map[string]string{"removed": fs.Arg(1)}, fs.Arg(1)+"\n")
		case "create":
			if fs.NArg() != 2 {
				fmt.Fprintln(stderr, usage)
				return 1
			}
			file, err := files.Resolve(ctx, fs.Arg(1))
			if err != nil {
				return fail(err)
			}
			share, fragment, err := files.Link(ctx, file, *role, *expires, *password)
			if err != nil {
				return fail(err)
			}
			result := map[string]string{"id": share.ID, "link": share.Link, "role": share.Role, "fragment": base64.RawURLEncoding.EncodeToString(fragment)}
			return writeOutput(stdout, *jsonOut, result, share.Link+"#"+result["fragment"]+"\n")
		default:
			fmt.Fprintln(stderr, usage)
			return 1
		}
	}
	files, ok := openDriveFiles(*use, *target, stderr)
	if !ok {
		return 1
	}
	switch args[0] {
	case "history":
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		file, err := files.Resolve(ctx, fs.Arg(0))
		if err != nil {
			return fail(err)
		}
		versions, err := files.Client.History(ctx, file.Manifest.Node)
		if err != nil {
			return fail(err)
		}
		return writeOutput(stdout, *jsonOut, map[string]any{"node": file.Manifest.Node, "versions": versions}, strings.Join(versions, "\n")+"\n")
	case "append":
		if fs.NArg() != 2 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		plain, err := os.ReadFile(fs.Arg(1))
		if err != nil {
			return fail(err)
		}
		file, created, err := ensureAppend(ctx, files, fs.Arg(0))
		if err != nil {
			return fail(err)
		}
		pos, err := files.Append(ctx, file, plain)
		if err != nil {
			return fail(err)
		}
		return writeOutput(stdout, *jsonOut, map[string]any{"node": file.Manifest.Node, "position": pos, "created": created}, fmt.Sprintf("%s %d\n", file.Manifest.Node, pos))
	case "tail":
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		file, err := files.Resolve(ctx, fs.Arg(0))
		if err != nil {
			return fail(err)
		}
		records, err := files.Tail(ctx, file, *from)
		if err != nil {
			return fail(err)
		}
		return writeOutput(stdout, *jsonOut, records, fmt.Sprint(len(records))+"\n")
	case "trim":
		if fs.NArg() != 2 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		file, err := files.Resolve(ctx, fs.Arg(0))
		if err != nil {
			return fail(err)
		}
		log, err := driveclient.OpenLog(ctx, files, file, func(state json.RawMessage, entry driveclient.Entry) (json.RawMessage, error) {
			var rows []json.RawMessage
			if len(state) > 0 && string(state) != "null" {
				if err := json.Unmarshal(state, &rows); err != nil {
					return nil, err
				}
			}
			encoded, err := json.Marshal(string(entry.Plaintext))
			if err != nil {
				return nil, err
			}
			rows = append(rows, encoded)
			return json.Marshal(rows)
		}, []byte("[]"))
		if err != nil {
			return fail(err)
		}
		parent, name, err := splitRemote(fs.Arg(1))
		if err != nil {
			return fail(err)
		}
		folder, err := files.Resolve(ctx, parent)
		if err != nil {
			return fail(err)
		}
		snap, err := log.Snapshot(ctx, folder, name)
		if err != nil {
			return fail(err)
		}
		return writeOutput(stdout, *jsonOut, map[string]any{"node": snap.Manifest.Node, "version": snap.Manifest.Version, "through": log.Through()}, snap.Manifest.Version+"\n")
	case "transfer":
		if fs.NArg() != 1 || *to == "" || (*toNode == "" && *into == "") {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		file, err := files.Resolve(ctx, fs.Arg(0))
		if err != nil {
			return fail(err)
		}
		if *toNode != "" {
			if _, err = files.Client.Commit(ctx, driveclient.Commit{Transfer: &driveclient.Transfer{Node: file.Manifest.Node, To: *to, ToNode: *toNode}}); err != nil {
				return fail(err)
			}
			return writeOutput(stdout, *jsonOut, map[string]string{"node": file.Manifest.Node, "to": *to, "to_node": *toNode}, *toNode+"\n")
		}
		// Re-create the subtree on the destination as its owner or as a
		// member with a share there, then retire it here.
		dst, ok := openDriveFiles(*use, *to, stderr)
		if !ok {
			return 1
		}
		parent, err := dst.Resolve(ctx, *into)
		if err != nil {
			return fail(err)
		}
		copied, err := files.Transfer(ctx, file, dst, parent)
		if err != nil {
			return fail(err)
		}
		return writeOutput(stdout, *jsonOut, map[string]string{"node": file.Manifest.Node, "to": *to, "to_node": copied.Manifest.Node, "name": copied.Name}, copied.Manifest.Node+"\n")
	default:
		fmt.Fprintln(stderr, usage)
		return 1
	}
}

func ensureAppend(ctx context.Context, files *driveclient.Files, remote string) (*driveclient.File, bool, error) {
	file, err := files.Resolve(ctx, remote)
	if err == nil {
		return file, false, nil
	}
	parentPath, name, err := splitRemote(remote)
	if err != nil {
		return nil, false, err
	}
	folder, err := files.Resolve(ctx, parentPath)
	if err != nil {
		return nil, false, err
	}
	created, err := files.CreateAppend(ctx, folder, name)
	return created, true, err
}
func splitRemote(remote string) (string, string, error) {
	remote = strings.TrimPrefix(remote, "/")
	at := strings.LastIndex(remote, "/")
	dir, base := "", remote
	if at >= 0 {
		dir, base = remote[:at], remote[at+1:]
	}
	base, err := protocol.NormalizeName(base)
	return dir, base, err
}
