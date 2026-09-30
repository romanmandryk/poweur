package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/poweur/api/internal/config"
	"github.com/poweur/api/internal/dns"
	"github.com/poweur/api/internal/drive"
	"github.com/poweur/api/internal/drive/provider/fs"
	"github.com/poweur/api/internal/migrate"
	"github.com/poweur/api/internal/relay"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate-v1" {
		os.Exit(migrateV1(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "copy-store" {
		os.Exit(copyStore(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "quotas" {
		os.Exit(quotas(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "identities" {
		os.Exit(identities(os.Args[2:]))
	}
	os.Exit(run())
}

// quotas shows or edits the per-identity storage quota overrides the relay
// keeps in its store (relay/storage-quotas.json); the relay picks up a change
// within a minute, without a restart.
//
//	poweur-relay quotas                    list overrides
//	poweur-relay quotas set <id> <size>    e.g. 2GiB, 500MB, 0 for unlimited
//	poweur-relay quotas unset <id>         back to the relay default
func quotas(args []string) int {
	cfg := config.FromEnv()
	store, err := drive.Open(cfg)
	if err != nil || store == nil {
		fmt.Fprintln(os.Stderr, "quotas: no store configured (set POWEUR_DATA or STORAGE_PROVIDER=s3 and S3_*)", err)
		return 1
	}
	ctx := context.Background()
	edit := func(fn func(map[string]json.RawMessage) error) int {
		values, err := relay.EditQuotas(ctx, store, fn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "quotas:", err)
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(values)
		return 0
	}
	switch {
	case len(args) == 0:
		return edit(func(map[string]json.RawMessage) error { return nil })
	case len(args) == 3 && args[0] == "set":
		if _, err := relay.ParseQuotaSize(args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "quotas:", err)
			return 2
		}
		return edit(func(doc map[string]json.RawMessage) error {
			raw, _ := json.Marshal(args[2])
			doc[strings.ToLower(strings.TrimSpace(args[1]))] = raw
			return nil
		})
	case len(args) == 2 && args[0] == "unset":
		return edit(func(doc map[string]json.RawMessage) error {
			delete(doc, strings.ToLower(strings.TrimSpace(args[1])))
			return nil
		})
	}
	fmt.Fprintln(os.Stderr, "usage: poweur-relay quotas [set <identity> <size> | unset <identity>]")
	return 2
}

// identities lists and edits the operator's holds on hosted identities (the
// relay's relay/suspended-identities.json). The relay picks a change up within
// 15 seconds, without a restart. Run it with docker exec, like quotas: having
// the relay's environment is the authority, so there is no admin token.
//
//	poweur-relay identities                       list holds
//	poweur-relay identities suspend <id> <reason> block the ID and its name
//	poweur-relay identities unsuspend <id>        lift a suspension
//	poweur-relay identities delete <id> --yes     suspend, then erase its data
//	poweur-relay identities release <id>          let a deleted name be claimed again
func identities(args []string) int {
	cfg := config.FromEnv()
	store, err := drive.Open(cfg)
	if err != nil || store == nil {
		fmt.Fprintln(os.Stderr, "identities: no store configured (set POWEUR_DATA or STORAGE_PROVIDER=s3 and S3_*)", err)
		return 1
	}
	ctx := context.Background()
	show := func(doc map[string]relay.Suspension) int {
		ids := make([]string, 0, len(doc))
		for id := range doc {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			sp := doc[id]
			state := "suspended"
			if sp.Deleted {
				state = "deleted"
			}
			fmt.Printf("%-9s %s  %s  %s\n", state, id, sp.At, sp.Reason)
		}
		if len(ids) == 0 {
			fmt.Println("no identities are suspended or deleted")
		}
		return 0
	}
	edit := func(fn func(map[string]relay.Suspension) error) int {
		doc, err := relay.EditSuspensions(ctx, store, fn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "identities:", err)
			return 1
		}
		return show(doc)
	}
	name := func(i int) string { return strings.ToLower(strings.TrimSpace(args[i])) }
	now := func() string { return time.Now().UTC().Format(time.RFC3339) }
	switch {
	case len(args) == 0:
		return edit(func(map[string]relay.Suspension) error { return nil })
	case len(args) >= 3 && args[0] == "suspend":
		reason := strings.TrimSpace(strings.Join(args[2:], " "))
		return edit(func(doc map[string]relay.Suspension) error {
			if prev, ok := doc[name(1)]; ok && prev.Deleted {
				return errors.New(name(1) + " is already deleted")
			}
			doc[name(1)] = relay.Suspension{Reason: reason, At: now()}
			return nil
		})
	case len(args) == 2 && args[0] == "unsuspend":
		return edit(func(doc map[string]relay.Suspension) error {
			sp, ok := doc[name(1)]
			switch {
			case !ok:
				return errors.New(name(1) + " is not suspended")
			case sp.Deleted:
				return errors.New(name(1) + " is deleted: its data is gone; use `release` to free the name")
			}
			delete(doc, name(1))
			return nil
		})
	case len(args) >= 2 && args[0] == "delete":
		confirmed := len(args) == 3 && args[2] == "--yes"
		if !confirmed {
			fmt.Fprintf(os.Stderr, "identities: this erases %s's ID document, keys backup, messages and whole drive from the store, permanently.\n"+
				"Backups aside, there is no undo. Run again with --yes once you are sure.\n", name(1))
			return 2
		}
		// Hold the name first: the ID is unusable within 15 seconds and stays
		// unclaimable even after its data is gone.
		if code := edit(func(doc map[string]relay.Suspension) error {
			sp := doc[name(1)]
			if sp.At == "" {
				sp.At = now()
			}
			if sp.Reason == "" {
				sp.Reason = "deleted by the operator"
			}
			sp.Deleted = true
			doc[name(1)] = sp
			return nil
		}); code != 0 {
			return code
		}
		removed, err := relay.DeleteIdentityData(ctx, store, name(1))
		fmt.Printf("erased %d objects for %s\n", removed, name(1))
		if err != nil {
			fmt.Fprintln(os.Stderr, "identities: stopped part-way (run the same command again to finish):", err)
			return 1
		}
		fmt.Println("the relay keeps the ID in memory until it restarts; the hold keeps it unusable until then")
		return 0
	case len(args) == 2 && args[0] == "release":
		return edit(func(doc map[string]relay.Suspension) error {
			if _, ok := doc[name(1)]; !ok {
				return errors.New(name(1) + " has no hold")
			}
			delete(doc, name(1))
			return nil
		})
	}
	fmt.Fprintln(os.Stderr, "usage: poweur-relay identities [suspend <id> <reason> | unsuspend <id> | delete <id> --yes | release <id>]")
	return 2
}

// copyStore copies a relay's objects from a data directory into the store the
// environment configures (STORAGE_PROVIDER=s3 …); run it with the relay stopped.
func copyStore(args []string) int {
	flags := flag.NewFlagSet("copy-store", flag.ContinueOnError)
	from := flags.String("from", "", "data directory to copy from (the fs provider root)")
	dry := flags.Bool("dry-run", false, "count what would be copied, write nothing")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	cfg := config.FromEnv()
	if *from == "" || cfg.StorageProvider != config.StorageS3 {
		fmt.Fprintln(os.Stderr, "copy-store: pass --from <dir> and configure STORAGE_PROVIDER=s3 and the S3_* variables")
		return 2
	}
	source, err := fs.Open(*from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "copy-store:", err)
		return 1
	}
	target, err := drive.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "copy-store: destination:", err)
		return 1
	}
	if _, err := migrate.CopyStore(context.Background(), source, target, *dry, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "copy-store:", err)
		return 1
	}
	return 0
}

// migrateV1 converts a v1 POWEUR_DATA to the v2 layout; run it with the relay
// stopped. See internal/migrate.
func migrateV1(args []string) int {
	flags := flag.NewFlagSet("migrate-v1", flag.ContinueOnError)
	data := flags.String("data", os.Getenv("POWEUR_DATA"), "the relay's data directory")
	dry := flags.Bool("dry-run", false, "read and report, write nothing")
	keep := flags.Bool("keep-source", false, "leave the v1 trees in place instead of renaming them to *.v1-backup")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	report, err := migrate.Run(context.Background(), migrate.Options{DataDir: *data, DryRun: *dry, KeepSource: *keep, Log: os.Stderr})
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(report)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate-v1:", err)
		return 1
	}
	return 0
}
func run() int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := config.LoadDotEnv(".env"); err != nil {
		logger.Warn("dotenv unavailable")
	}
	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid relay configuration", "error", err.Error())
		return 1
	}
	var resolver dns.Resolver = dns.NewNetResolver()
	// Several local relays reach each other's hosted identities through a
	// hosts file (tests, development); never with the SSRF guard on.
	if path := os.Getenv("RESOLVER_HOSTS_FILE"); path != "" && cfg.ResolverAllowPrivate {
		resolver = &dns.HostsFileResolver{Path: path, Next: resolver}
	}
	server := relay.NewServer(cfg, resolver, dns.NewProviderFactory(cfg))
	if err := server.DriveError(); err != nil {
		logger.Error("drive storage unavailable", "error", err.Error())
		return 1
	}
	if err := server.StartTelemetry(context.Background(), os.Stdout); err != nil {
		logger.Error("telemetry initialization failed")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: server.Router(), ReadHeaderTimeout: 10 * time.Second}
	failures := make(chan error, 1)
	go func() { failures <- httpServer.ListenAndServe() }()
	code := 0
	select {
	case <-ctx.Done():
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped")
			code = 1
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
	flush, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	_ = server.Close(flush)
	return code
}
