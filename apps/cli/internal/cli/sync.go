package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	driveclient "github.com/poweur/cli/internal/drive"
	"github.com/poweur/cli/internal/sync"
)

const syncUsage = `usage: poweur sync <run|pull|push|status|watch|service> <local-dir>
    [--drive <owner> --folder /<shared-node-id>]   a folder shared with you (default: your own drive)
    [--path <prefix> ...]                          selective sync (default: everything but .poweur)
    watch: [--settle 2s] [--interval 3s] [--timeout D]; service: [--launchd|--systemd]`

// runSync keeps a local directory and an encrypted drive folder in step
// (EPIC-020 E20-T9). Merges happen here; the relay only refuses stale writes.
func runSync(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, syncUsage)
		return 1
	}
	sub := args[0]
	switch sub {
	case "run", "pull", "push", "status", "watch", "service":
	default:
		fmt.Fprintln(stderr, syncUsage)
		return 1
	}
	fs := flag.NewFlagSet("sync "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	use := fs.String("use-identity", "", "identity to sync as")
	target := fs.String("drive", "", "the drive to sync (default: your own)")
	folder := fs.String("folder", "", "remote folder: /<shared-node-id>[/path] in a drive shared with you, or a path in your own")
	var roots pathList
	var groups pathList
	fs.Var(&groups, "group", "sync a folder shared with this group you are in")
	fs.Var(&roots, "path", "a path to sync (repeatable; default: everything but .poweur)")
	settle := fs.Duration("settle", 2*time.Second, "watch: upload a file only after it has been unchanged this long")
	interval := fs.Duration("interval", 3*time.Second, "watch: how often to look for local changes")
	timeout := fs.Duration("timeout", 0, "watch: stop after this long (default: run until interrupted)")
	launchd := fs.Bool("launchd", false, "service: print a launchd agent (macOS)")
	systemd := fs.Bool("systemd", false, "service: print a systemd user unit (Linux)")
	if fs.Parse(normalizeArgs(args[1:], map[string]bool{"--launchd": true, "--systemd": true})) != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, syncUsage)
		return 1
	}
	root, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if sub == "service" {
		return writeSyncService(stdout, stderr, root, *launchd, *systemd, args[1:])
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	files, ok := openDriveFiles(*use, *target, stderr)
	if !ok {
		return 1
	}
	owner := files.Client.Drive == "" || files.Client.Drive == files.Client.Identity
	if !owner && *folder == "" {
		fmt.Fprintln(stderr, "a drive shared with you syncs one folder: --folder /<shared-node-id> (see `poweur drive shared --drive <owner>`)")
		return 1
	}
	// The device headers let the relay record how far this device synced.
	files.Client.Headers = deviceHeaders()
	if !joinGroups(files, *use, groups, stderr) {
		return 1
	}
	drive := files.Client.Drive
	if drive == "" {
		drive = files.Client.Identity
	}

	state, err := sync.LoadState(root)
	if err != nil {
		fmt.Fprintf(stderr, "sync state: %v\n", err)
		return 1
	}
	remoteFolder := "/" + strings.Trim(*folder, "/")
	if remoteFolder == "/" {
		remoteFolder = ""
	}
	if state.Drive != "" && (state.Drive != drive || state.Folder != remoteFolder) {
		fmt.Fprintf(stderr, "%s already syncs %s%s\n", root, state.Drive, state.Folder)
		return 1
	}
	state.Identity, state.Drive, state.Folder = files.Client.Identity, drive, remoteFolder
	engine := &sync.Engine{
		Root:   root,
		Remote: &sync.DriveRemote{Files: files, Folder: remoteFolder, Owner: owner},
		State:  state,
		Ignore: sync.LoadIgnore(root),
		Roots:  roots,
		Device: syncDeviceName(),
		Logf:   func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) },
	}
	ctx := context.Background()

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
		printSyncList(stdout, "conflict", st.Conflicts)
		if st.RemoteChanged {
			fmt.Fprintln(stdout, "remote: changed since the last sync")
		} else {
			fmt.Fprintln(stdout, "remote: no changes since the last sync")
		}
		return 0
	case "pull":
		rep, err := engine.Pull(ctx)
		return reportSync(stdout, stderr, rep, err)
	case "push":
		rep, err := engine.Push(ctx)
		return reportSync(stdout, stderr, rep, err)
	case "run":
		rep, err := engine.Run(ctx)
		if code := reportSync(stdout, stderr, rep, err); code != 0 {
			return code
		}
		if rep.Empty() {
			fmt.Fprintln(stdout, "already in sync")
		}
		return 0
	default:
		return watchSync(ctx, engine, files.Client, *settle, *interval, *timeout, stdout, stderr)
	}
}

// watchSync runs a sync, then waits for a change on either side: an event
// on the drive's change stream, or a local edit seen by polling. Uploads
// wait until a file has settled. The stream reconnects with backoff, which
// also resumes after sleep; a periodic full pass covers anything missed.
func watchSync(ctx context.Context, engine *sync.Engine, client *driveclient.Client, settle, interval, timeout time.Duration, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	engine.Settle = settle
	wake := make(chan struct{}, 1)
	poke := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	stream := *client
	stream.Headers = nil
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			err := stream.Subscribe(ctx, func(event driveclient.Event) error {
				backoff = time.Second
				if event.Type != "ready" {
					poke()
				}
				return nil
			})
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				fmt.Fprintf(stderr, "change stream: %v (reconnecting)\n", err)
			}
			poke() // something may have changed while we were away
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()

	fmt.Fprintf(stdout, "watching %s\n", engine.Root)
	lastLocal, _ := engine.LocalSnapshot()
	fullPass := time.NewTicker(time.Minute)
	defer fullPass.Stop()
	poll := time.NewTicker(interval)
	defer poll.Stop()
	pending := true
	for {
		if pending {
			pending = false
			rep, err := engine.Run(ctx)
			if err != nil && ctx.Err() == nil {
				fmt.Fprintf(stderr, "sync: %v\n", err)
			} else {
				printSyncReport(stdout, rep)
			}
			if len(rep.Deferred) > 0 {
				time.AfterFunc(settle, poke)
			}
			lastLocal, _ = engine.LocalSnapshot()
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return 0
			}
			return 0
		case <-wake:
			pending = true
		case <-fullPass.C:
			engine.State.Cursor = ""
			pending = true
		case <-poll.C:
			if now, err := engine.LocalSnapshot(); err == nil && now != lastLocal {
				pending = true
			}
		}
	}
}

func reportSync(stdout, stderr io.Writer, rep sync.Report, err error) int {
	printSyncReport(stdout, rep)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func syncDeviceName() string {
	if name := strings.TrimSpace(os.Getenv("POWEUR_DEVICE_NAME")); name != "" {
		return name
	}
	host, _ := os.Hostname()
	return strings.TrimSuffix(host, ".local")
}

func printSyncList(w io.Writer, label string, paths []string) {
	for _, p := range paths {
		fmt.Fprintf(w, "%s: %s\n", label, p)
	}
}

func printSyncReport(w io.Writer, rep sync.Report) {
	for _, group := range []struct {
		label string
		paths []string
	}{
		{"mkdir (local)", rep.MkdirL}, {"downloaded", rep.Downloaded}, {"merged", rep.Merged},
		{"moved to trash", rep.DeletedL}, {"mkdir (remote)", rep.MkdirR}, {"uploaded", rep.Uploaded},
		{"deleted (remote)", rep.DeletedR},
	} {
		for _, p := range group.paths {
			fmt.Fprintf(w, "%s: %s\n", group.label, p)
		}
	}
	for _, p := range rep.Conflicts {
		fmt.Fprintf(w, "CONFLICT: %s\n", p)
	}
}

// writeSyncService prints a user service that runs `poweur sync watch` at
// login: a launchd agent on macOS, a systemd user unit on Linux.
func writeSyncService(stdout, stderr io.Writer, root string, launchd, systemd bool, raw []string) int {
	if launchd == systemd {
		fmt.Fprintln(stderr, "service: choose --launchd or --systemd")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "poweur"
	}
	// Carry the sync options, not the service flags or the directory.
	var extra []string
	for i := 0; i < len(raw); i++ {
		arg := raw[i]
		switch {
		case arg == "--launchd" || arg == "--systemd" || arg == root || filepath.Clean(arg) == filepath.Clean(root):
		case strings.HasPrefix(arg, "--") && !strings.Contains(arg, "=") && i+1 < len(raw) && !strings.HasPrefix(raw[i+1], "--"):
			if abs, _ := filepath.Abs(raw[i+1]); abs == root {
				extra = append(extra, arg)
			} else {
				extra = append(extra, arg, raw[i+1])
			}
			i++
		default:
			if abs, _ := filepath.Abs(arg); abs != root {
				extra = append(extra, arg)
			}
		}
	}
	args := append([]string{exe, "sync", "watch", root}, extra...)
	label := "org.poweur.sync." + strings.Trim(strings.ReplaceAll(strings.ToLower(filepath.Base(root)), " ", "-"), ".")
	if launchd {
		fmt.Fprintf(stdout, `<?xml version="1.0" encoding="UTF-8"?>
<!-- Save as ~/Library/LaunchAgents/%s.plist, then: launchctl load -w <that file> -->
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, label, plistStrings(args), filepath.Join(root, ".poweur-sync.log"), filepath.Join(root, ".poweur-sync.log"))
		return 0
	}
	fmt.Fprintf(stdout, `# Save as ~/.config/systemd/user/%s.service, then:
#   systemctl --user daemon-reload && systemctl --user enable --now %s
[Unit]
Description=Poweur sync of %s
After=network-online.target

[Service]
ExecStart=%s
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, label, label, root, shellJoin(args))
	return 0
}

func plistStrings(args []string) string {
	var b strings.Builder
	for _, a := range args {
		a = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(a)
		fmt.Fprintf(&b, "    <string>%s</string>\n", a)
	}
	return b.String()
}

func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'\\$") {
			a = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`).Replace(a) + `"`
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// pathList is a repeatable flag of drive paths, without surrounding slashes.
type pathList []string

func (s *pathList) String() string { return strings.Join(*s, ",") }
func (s *pathList) Set(v string) error {
	v = strings.Trim(strings.TrimSpace(v), "/")
	if v != "" {
		*s = append(*s, v)
	}
	return nil
}
