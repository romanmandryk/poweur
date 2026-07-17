package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/poweur/cli/internal/sync"
)

// `poweur sync` (EPIC-004 E04-T4): one-shot rsync-like reconciliation of a
// local directory against the identity's relay-hosted tree.
//
//	poweur sync pull   <dir>   apply remote changes locally (conflict-aware)
//	poweur sync push   <dir>   upload local changes
//	poweur sync run    <dir>   pull then push (bidirectional one-shot)
//	poweur sync status <dir>   show pending changes on both sides
func runSync(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur sync <pull|push|run|status> <local-dir> [--path <prefix> ...]")
		return 1
	}
	sub := args[0]
	switch sub {
	case "pull", "push", "run", "status":
	default:
		fmt.Fprintln(stderr, "unknown sync subcommand (want pull, push, run, status)")
		return 1
	}
	fs := flag.NewFlagSet("sync "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity to authenticate as")
	audience := fs.String("audience", "", "tree owner to sync against (default: own tree)")
	relayFlag := fs.String("relay", "", "relay to sync with (default: configured relay)")
	var pathFilters stringList
	fs.Var(&pathFilters, "path", "tree prefix to sync (repeatable; default: public, shared, private, apps)")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "sync needs exactly one local directory")
		return 1
	}
	root, err := filepath.Abs(rest[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	relayURL := cfg.RelayURL
	if *relayFlag != "" {
		relayURL = *relayFlag
	}
	owner := *audience
	if owner == "" {
		owner = identityValue
	}
	ctx := context.Background()
	tok, err := MintDAVToken(ctx, relayURL, identityValue, owner, "", priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	state, err := sync.LoadState(root)
	if err != nil {
		fmt.Fprintf(stderr, "sync state: %v\n", err)
		return 1
	}
	if state.Identity != "" && !strings.EqualFold(state.Identity, owner) {
		fmt.Fprintf(stderr, "directory is already syncing with %s (not %s)\n", state.Identity, owner)
		return 1
	}
	state.Identity = owner
	engine := &sync.Engine{
		Root:   root,
		Remote: &sync.HTTPRemote{RelayURL: relayURL, Identity: owner, Token: tok.Token},
		State:  state,
		Ignore: sync.LoadIgnore(root),
		Roots:  pathFilters,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(stderr, format+"\n", a...)
		},
	}

	switch sub {
	case "status":
		st, err := engine.Status(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printSyncList(stdout, "local new", st.LocalNew)
		printSyncList(stdout, "local modified", st.LocalModified)
		printSyncList(stdout, "local deleted", st.LocalDeleted)
		if st.NeedsResync {
			fmt.Fprintln(stdout, "remote: full resync required (first sync or journal gap)")
		} else {
			fmt.Fprintf(stdout, "remote: %d pending change(s)\n", st.RemotePending)
		}
		return 0
	case "pull":
		rep, err := engine.Pull(ctx)
		printSyncReport(stdout, rep)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "push":
		rep, err := engine.Push(ctx)
		printSyncReport(stdout, rep)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	default: // run
		pullRep, err := engine.Pull(ctx)
		printSyncReport(stdout, pullRep)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		pushRep, err := engine.Push(ctx)
		printSyncReport(stdout, pushRep)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if pullRep.Empty() && pushRep.Empty() {
			fmt.Fprintln(stdout, "already in sync")
		}
		return 0
	}
}

func printSyncList(w io.Writer, label string, paths []string) {
	for _, p := range paths {
		fmt.Fprintf(w, "%s: %s\n", label, p)
	}
}

func printSyncReport(w io.Writer, rep sync.Report) {
	for _, p := range rep.MkdirL {
		fmt.Fprintf(w, "mkdir (local): %s\n", p)
	}
	for _, p := range rep.Downloaded {
		fmt.Fprintf(w, "downloaded: %s\n", p)
	}
	for _, p := range rep.DeletedL {
		fmt.Fprintf(w, "deleted (local): %s\n", p)
	}
	for _, p := range rep.MkdirR {
		fmt.Fprintf(w, "mkdir (remote): %s\n", p)
	}
	for _, p := range rep.Uploaded {
		fmt.Fprintf(w, "uploaded: %s\n", p)
	}
	for _, p := range rep.DeletedR {
		fmt.Fprintf(w, "deleted (remote): %s\n", p)
	}
	for _, p := range rep.Conflicts {
		fmt.Fprintf(w, "CONFLICT — local version saved as: %s\n", p)
	}
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	v = strings.Trim(strings.TrimSpace(v), "/")
	if v != "" {
		*s = append(*s, v)
	}
	return nil
}
