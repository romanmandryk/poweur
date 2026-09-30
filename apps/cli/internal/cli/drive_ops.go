package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	driveclient "github.com/poweur/cli/internal/drive"
	idpkg "github.com/poweur/identity"
	protocol "github.com/poweur/identity/drive"
)

func runDriveOps(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: poweur drive history|tail <path> [--from=1]; append <path> <file>; trim <log> <snapshot>; watch [--drive <id>] [--count N] [--timeout D]; share add <path> <member> [--no-offer]|rm <id> [--no-rotate]|ls; rotate <path>; accept <offer.json|->; mounts; link create <path> [--password p]|rm <id> [--no-rotate]|get <url> <local-file> [--path <file-in-folder>] [--password p]; transfer <path> --to <drive> [--into </shared-node-id/path>|--to-node <id>]; any command takes --drive <identity> [--json]"
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
	noOffer := fs.Bool("no-offer", false, "share or revoke without messaging the member")
	noRotate := fs.Bool("no-rotate", false, "revoke without re-keying the node (writes there wait for `drive rotate`)")
	count := fs.Int("count", 0, "watch: exit after this many change events (the ready event not counted)")
	inside := fs.String("path", "", "link get: a file inside a linked folder")
	var groups pathList
	fs.Var(&groups, "group", "open a drive shared with this group you are in (repeatable)")
	timeout := fs.Duration("timeout", 0, "watch: exit after this long (e.g. 30s)")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true, "--no-offer": true, "--no-rotate": true})) != nil {
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
		// A drive shared with you streams from the owner's relay.
		if drive := strings.ToLower(strings.TrimSpace(*target)); drive != "" && drive != name {
			relay, err := identityRelayURL(ctx, cfg, drive)
			if err != nil {
				return fail(err)
			}
			client.Relay, client.Drive = relay, drive
		}
		if *timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, *timeout)
			defer cancel()
		}
		seen := 0
		errEnough := errors.New("enough events")
		err := client.Subscribe(ctx, func(event driveclient.Event) error {
			if event.Type != "ready" {
				seen++
			}
			if *jsonOut {
				raw, _ := json.Marshal(event)
				fmt.Fprintln(stdout, string(raw))
			} else {
				fmt.Fprintf(stdout, "%s %s\n", event.Type, event.Timestamp)
			}
			if *count > 0 && seen >= *count {
				return errEnough
			}
			return nil
		})
		switch {
		case errors.Is(err, errEnough):
			return 0
		case *count > 0 && seen < *count:
			// Asked for events that did not come: the stream ended or timed out.
			if err == nil {
				err = errors.New("the change stream ended")
			}
			return fail(err)
		case err != nil && ctx.Err() == nil:
			return fail(err)
		}
		return 0
	case "share":
		if fs.NArg() < 1 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		files, ok := openDriveFiles(*use, *target, stderr)
		if !ok || !joinGroups(files, *use, groups, stderr) {
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
			var revoked *protocol.Share
			if shares, err := files.Client.Shares(ctx); err == nil {
				for i := range shares {
					if shares[i].ID == fs.Arg(1) {
						revoked = &shares[i]
					}
				}
			}
			if err := files.Client.Unshare(ctx, fs.Arg(1)); err != nil {
				return fail(err)
			}
			if !*noRotate && !rotateAfterRevoke(ctx, files, revoked, stderr) {
				return 1
			}
			if revoked != nil && revoked.Member != "" && !*noOffer {
				notice := protocol.ShareRevoked{Format: protocol.OfferFormat, Drive: revoked.Drive, ShareID: revoked.ID, RevokedAt: time.Now().UTC().Format(time.RFC3339)}
				if err := sendDriveMessage(*use, revoked.Member, idpkg.MsgTypeShareRevoked, revoked.ID, notice, stderr); err != nil {
					fmt.Fprintln(stderr, "revoked, but the member was not told:", err)
				}
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
			// A group is shared with through its current group key.
			memberKey, err := shareRecipientKey(ctx, fs.Arg(2))
			if err != nil {
				return fail(err)
			}
			share, err := files.ShareWith(ctx, file, fs.Arg(2), memberKey, *role, *expires)
			if err != nil {
				return fail(err)
			}
			if !*noOffer {
				if err := offerShare(*use, files, file, share, stderr); err != nil {
					fmt.Fprintln(stderr, "shared, but the offer was not sent:", err)
				}
			}
			result := map[string]string{"id": share.ID, "node": share.Node, "member": share.Member, "role": share.Role}
			return writeOutput(stdout, *jsonOut, result, share.ID+"\n")
		default:
			fmt.Fprintln(stderr, usage)
			return 1
		}
	case "rotate":
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		files, ok := openDriveFiles(*use, *target, stderr)
		if !ok || !joinGroups(files, *use, groups, stderr) {
			return 1
		}
		node, err := files.Resolve(ctx, fs.Arg(0))
		if err != nil {
			return fail(err)
		}
		rotated, err := files.Rotate(ctx, node)
		if err != nil {
			return fail(err)
		}
		reportStale(rotated, stderr)
		return writeOutput(stdout, *jsonOut, map[string]any{"node": rotated.File.Manifest.Node, "generation": rotated.File.Manifest.Generation, "reissued": rotated.Reissued, "stale": staleIDs(rotated)}, fmt.Sprintf("%s %d\n", rotated.File.Manifest.Node, rotated.File.Manifest.Generation))
	case "link":
		if fs.NArg() < 1 {
			fmt.Fprintln(stderr, usage)
			return 1
		}
		// Opening a link needs no identity: it is what someone without an ID does.
		if fs.Arg(0) == "get" {
			if fs.NArg() != 3 {
				fmt.Fprintln(stderr, usage)
				return 1
			}
			result, err := runLinkGet(ctx, fs.Arg(1), *inside, *password, fs.Arg(2), stderr)
			if err != nil {
				return fail(err)
			}
			return writeOutput(stdout, *jsonOut, result, result["path"]+"\n")
		}
		files, ok := openDriveFiles(*use, *target, stderr)
		if !ok || !joinGroups(files, *use, groups, stderr) {
			return 1
		}
		switch fs.Arg(0) {
		case "rm":
			if fs.NArg() != 2 {
				fmt.Fprintln(stderr, usage)
				return 1
			}
			var revoked *protocol.Share
			if shares, err := files.Client.Shares(ctx); err == nil {
				for i := range shares {
					if shares[i].ID == fs.Arg(1) {
						revoked = &shares[i]
					}
				}
			}
			if err := files.Client.Unshare(ctx, fs.Arg(1)); err != nil {
				return fail(err)
			}
			if !*noRotate && !rotateAfterRevoke(ctx, files, revoked, stderr) {
				return 1
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
			result["url"] = linkURL(files.Client.Relay, share.Drive, share.Link, result["fragment"])
			return writeOutput(stdout, *jsonOut, result, result["url"]+"\n")
		default:
			fmt.Fprintln(stderr, usage)
			return 1
		}
	}
	files, ok := openDriveFiles(*use, *target, stderr)
	if !ok || !joinGroups(files, *use, groups, stderr) {
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

// rotateAfterRevoke re-keys the revoked share's node when the relay asks for
// it, so writes there resume and the remaining members get re-issued shares.
func rotateAfterRevoke(ctx context.Context, files *driveclient.Files, revoked *protocol.Share, stderr io.Writer) bool {
	if revoked == nil || !protocol.KeyBearing(revoked.Role) {
		return true
	}
	rotated, err := files.RotateIfRequired(ctx, revoked.Node)
	if err != nil {
		fmt.Fprintln(stderr, "revoked, but the keys were not rotated (writes there wait for `poweur drive rotate`):", err)
		return false
	}
	reportStale(rotated, stderr)
	return true
}
func reportStale(rotated *driveclient.Rotated, stderr io.Writer) {
	if rotated == nil {
		return
	}
	for _, s := range rotated.Stale {
		who := s.Member
		if s.Link != "" {
			who = "link " + s.Link
		}
		fmt.Fprintf(stderr, "share %s (%s) predates the rotation and must be recreated\n", s.ID, who)
	}
}
func staleIDs(rotated *driveclient.Rotated) []string {
	ids := []string{}
	for _, s := range rotated.Stale {
		ids = append(ids, s.ID)
	}
	return ids
}
