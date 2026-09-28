package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Abuse reports and shareable blocklists (EPIC-007 E07-T5).
//
// Recipient consent is a wall around one inbox. These two commands are what a
// person can do *outside* their own wall: tell the operator who is accountable
// for a sender (`poweur report`), and pool the block decisions a community has
// already made (`poweur blocks`). Neither is enforcement — no relay suspends
// anybody on a count, and no import happens without the user asking — but both
// move information to where it can be acted on, which is the part the protocol
// was missing.

func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	reason := fs.String("reason", idpkg.AbuseReasonSpam,
		"why: "+strings.Join(idpkg.AbuseReasons(), " | "))
	note := fs.String("note", "", "optional note for the operator (no message content is sent)")
	messageIDs := fs.String("message-ids", "", "comma-separated message ids as evidence")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: poweur report <identity> [--reason=%s] [--note=...] [--message-ids=id,id]\n",
			strings.Join(idpkg.AbuseReasons(), "|"))
		return 1
	}
	subject := strings.ToLower(strings.TrimSpace(fs.Arg(0)))

	cfg, identityValue, priv, ok := loadIdentityKey(*useIdentity, stderr)
	if !ok {
		return 1
	}

	var ids []string
	for _, id := range strings.Split(*messageIDs, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	report := idpkg.NewAbuseReport(identityValue, subject, *reason, ids, *note)
	if err := report.Sign(priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// The report goes to the relay that hosts the subject — the operator who
	// can actually do something — not to our own relay and not to the subject.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target, err := resolveRecipientRelayURL(ctx, subject, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "cannot resolve the relay hosting %s: %v\n", subject, err)
		return 1
	}
	raw, _ := json.Marshal(report)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(target, "/")+"/abuse", bytes.NewReader(raw))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fmt.Fprintf(stderr, "relay rejected the report (%d): %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		return 1
	}
	var out struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Status == "" {
		out.Status = "recorded"
	}
	return writeOutput(stdout, *jsonOut, map[string]any{
		"subject": subject, "reason": report.Reason, "relay": target, "status": out.Status, "report": report,
	}, fmt.Sprintf("reported %s to %s for %s (%s)\n"+
		"the report carries message ids and your note — never message content, which the operator could not read anyway\n",
		subject, target, report.Reason, out.Status))
}

func runBlocks(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur blocks <export|import>")
		return 1
	}
	switch args[0] {
	case "export":
		return runBlocksExport(args[1:], stdout, stderr)
	case "import":
		return runBlocksImport(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown blocks subcommand (want export, import)")
		return 1
	}
}

// runBlocksExport turns this identity's blocked contacts into one signed
// document, published into their own tree (where an EPIC-005 share can
// distribute it) and optionally written to a local file.
func runBlocksExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("blocks export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	name := fs.String("name", "", "human label for the list")
	out := fs.String("out", "", "also write the signed document to this local file")
	noPublish := fs.Bool("no-publish", false, "do not write "+idpkg.BlocklistTreePath+" into your own tree")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--no-publish": true})); err != nil {
		return 1
	}

	cfg, identityValue, priv, okID := loadIdentityKey(*useIdentity, stderr)
	if !okID {
		return 1
	}
	ctx := context.Background()
	relayURL := cfg.RelayURL
	token := priv
	contacts, err := fetchContacts(ctx, relayURL, identityValue, token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	list := idpkg.BlocklistFromContacts(identityValue, *name, contacts)
	if err := list.Sign(priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	published := ""
	if !*noPublish {
		if err := writeSysFile(ctx, relayURL, identityValue, token, idpkg.BlocklistTreePath, raw); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		published = idpkg.BlocklistTreePath
	}
	if *out != "" {
		if err := os.WriteFile(*out, raw, 0o600); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	human := fmt.Sprintf("exported %d block(s) signed by %s\n", len(list.Entries), identityValue)
	if published != "" {
		human += fmt.Sprintf("published to %s — share that path (`poweur share add`) with whoever should be able to adopt it\n", published)
	}
	if *out != "" {
		human += fmt.Sprintf("wrote %s\n", *out)
	}
	return writeOutput(stdout, *jsonOut, list, human)
}

// runBlocksImport adopts somebody else's list, after checking they really
// signed it.
func runBlocksImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("blocks import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	file := fs.String("file", "", "read the signed list from a local file instead of a publisher's tree")
	path := fs.String("path", idpkg.BlocklistTreePath, "tree path to read from the publisher")
	force := fs.Bool("force", false, "also block identities you have accepted as contacts")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--force": true, "--dry-run": true})); err != nil {
		return 1
	}
	if *file == "" && fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur blocks import <publisher-identity> [--path=...] | --file <path> [--force] [--dry-run]")
		return 1
	}

	cfg, identityValue, priv, ok := loadIdentityKey(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	relayURL := cfg.RelayURL
	token := priv

	raw, code := readBlocklistSource(ctx, fs, *file, *path, cfg, identityValue, priv, stderr)
	if code != 0 {
		return code
	}
	list, err := idpkg.ParseBlocklist(raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// The signature is the whole point: without it, "adopt this list" is
	// "let whoever served this file add names to your blocklist".
	res, err := identity.ResolveIdentity(ctx, list.Publisher)
	if err != nil {
		fmt.Fprintf(stderr, "cannot resolve publisher %s to check their signature: %v\n", list.Publisher, err)
		return 1
	}
	pub, err := idpkg.ParseEd25519PublicKey(res.Document.PublicKey)
	if err != nil {
		fmt.Fprintf(stderr, "publisher %s has an invalid key: %v\n", list.Publisher, err)
		return 1
	}
	if err := list.VerifySignature(pub); err != nil {
		fmt.Fprintf(stderr, "REFUSING TO IMPORT: %v\n"+
			"the list does not carry a valid signature from %s — it may have been altered in transit.\n",
			err, list.Publisher)
		return 1
	}

	contacts, err := fetchContacts(ctx, relayURL, identityValue, token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	updated, merge := idpkg.ApplyBlocklist(contacts, identityValue, list, *force)
	if !*dryRun && len(merge.Blocked) > 0 {
		if err := putContacts(ctx, relayURL, identityValue, token, updated); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *jsonOut {
		return writeOutput(stdout, true, map[string]any{
			"publisher": list.Publisher, "name": list.Name, "dry_run": *dryRun,
			"blocked": merge.Blocked, "already_blocked": merge.AlreadyBlocked,
			"skipped_accepted": merge.SkippedAccepted, "skipped_self": merge.SkippedSelf,
		}, "")
	}
	verb := "blocked"
	if *dryRun {
		verb = "would block"
	}
	fmt.Fprintf(stdout, "%s: %s %d, already blocked %d\n",
		list.Publisher, verb, len(merge.Blocked), len(merge.AlreadyBlocked))
	for _, id := range merge.Blocked {
		fmt.Fprintf(stdout, "  %s %s\n", verb, id)
	}
	// Named, never silent: a list that quietly cut somebody off from a person
	// they chose would make adopting one an act of self-harm.
	for _, id := range merge.SkippedAccepted {
		fmt.Fprintf(stdout, "  skipped %s — you accepted them as a contact (use --force to block anyway)\n", id)
	}
	if merge.SkippedSelf {
		fmt.Fprintf(stdout, "  skipped yourself — the list names %s\n", identityValue)
	}
	return 0
}

// readBlocklistSource loads the document from a local file or the publisher's
// own tree (which is how an EPIC-005 share delivers one).
//
// The tree read needs a token minted for the *publisher's* audience — a share
// grant is what turns that from a 403 into a document — and minted at the
// publisher's own relay, since a grant is enforced by whoever stores the file
// and no operator honours another operator's tokens.
//
// `--file` stays for every other way a list travels: handed over out of band,
// pulled from a web page, mailed. The signature is what makes that safe, so
// the transport does not have to be.
func readBlocklistSource(ctx context.Context, fs *flag.FlagSet, file, path string, cfg config.Config, identityValue string, priv ed25519.PrivateKey, stderr io.Writer) ([]byte, int) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return nil, 1
		}
		return raw, 0
	}
	publisher := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	relayURL := cfg.RelayURL
	if resolved, err := resolveRecipientRelayURL(ctx, publisher, cfg); err == nil && resolved != "" {
		relayURL = resolved
	}
	raw, status, err := readSysFile(ctx, relayURL, publisher, priv, path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		fmt.Fprintf(stderr, "%s has not shared %s with you (ask them to `poweur share add %s --with=%s`)\n",
			publisher, path, path, identityValue)
		return nil, 1
	}
	if status != http.StatusOK {
		fmt.Fprintf(stderr, "cannot read %s from %s: HTTP %d\n", path, publisher, status)
		return nil, 1
	}
	return raw, 0
}
