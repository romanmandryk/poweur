package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Contacts, inbox policy & requests (EPIC-007 E07-T3/T4). Contacts live at
// poweur-sys/relay/contacts.json in the owner's tree (written over DAV, so
// they sync across devices like any file); the relay enforces the inbox
// policy from the same zone. Keys are pinned at add/accept time (TOFU).

const (
	contactsTreePath    = "poweur-sys/relay/contacts.json"
	inboxPolicyTreePath = "poweur-sys/relay/inbox-policy.json"
)

func runContacts(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur contacts <ls|add|request|accept|block|rm>")
		return 1
	}
	switch args[0] {
	case "ls":
		return runContactsLs(args[1:], stdout, stderr)
	case "add":
		return runContactsSet(args[1:], stdout, stderr, idpkg.ContactAccepted, "added")
	case "accept":
		return runContactsAccept(args[1:], stdout, stderr)
	case "request":
		return runContactsRequest(args[1:], stdout, stderr)
	case "block":
		return runContactsSet(args[1:], stdout, stderr, idpkg.ContactBlocked, "blocked")
	case "rm":
		return runContactsRm(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown contacts subcommand (want ls, add, request, accept, block, rm)")
		return 1
	}
}

// fetchContacts loads the identity's contacts.json (empty file when absent).
func fetchContacts(ctx context.Context, relayURL, identityValue, token string) (idpkg.ContactsFile, error) {
	raw, status, err := davGetBytes(ctx, relayURL, identityValue, token, contactsTreePath)
	if err != nil {
		return idpkg.ContactsFile{}, err
	}
	if status == http.StatusNotFound {
		return idpkg.ContactsFile{Version: 1}, nil
	}
	if status != http.StatusOK {
		return idpkg.ContactsFile{}, fmt.Errorf("fetch contacts: HTTP %d", status)
	}
	return idpkg.ParseContactsFile(raw)
}

func putContacts(ctx context.Context, relayURL, identityValue, token string, file idpkg.ContactsFile) error {
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return davPutBytes(ctx, relayURL, identityValue, token, contactsTreePath, raw)
}

// resolvePin resolves the current signing key of a contact for pinning.
func resolvePin(ctx context.Context, contact string) (string, error) {
	res, err := identity.ResolveIdentity(ctx, contact)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s to pin their key: %w", contact, err)
	}
	pub, err := idpkg.ParseEd25519PublicKey(res.Document.PublicKey)
	if err != nil {
		return "", fmt.Errorf("resolved key for %s is invalid: %w", contact, err)
	}
	return idpkg.FormatEd25519PublicKey(pub), nil
}

// runContactsSet writes a contact entry in the given state, pinning the
// current resolved key. Used by `add` (accepted) and `block`.
func runContactsSet(args []string, stdout, stderr io.Writer, state, verb string) int {
	fs := flag.NewFlagSet("contacts "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	petname := fs.String("petname", "", "local display name for this contact")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: poweur contacts %s <identity>\n", map[string]string{"added": "add", "blocked": "block"}[verb])
		return 1
	}
	target := strings.ToLower(fs.Arg(0))
	relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()

	pin := ""
	if state == idpkg.ContactAccepted {
		var err error
		if pin, err = resolvePin(ctx, target); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	contacts, err := fetchContacts(ctx, relayURL, identityValue, token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	entry := idpkg.Contact{
		Identity: target, State: state, PinnedKey: pin, Petname: *petname,
		AddedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if existing, found := contacts.Find(target); found {
		entry.AddedAt = existing.AddedAt
		if entry.Petname == "" {
			entry.Petname = existing.Petname
		}
		if entry.PinnedKey == "" {
			entry.PinnedKey = existing.PinnedKey
		}
	}
	contacts = contacts.Upsert(entry)
	if err := putContacts(ctx, relayURL, identityValue, token, contacts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s %s\n", verb, target)
	return 0
}

// runContactsRequest records the outbound request (state=requested) and
// sends a sys.contact.request typed message with an encrypted intro.
func runContactsRequest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("contacts request", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: poweur contacts request <identity> [<intro message>]")
		return 1
	}
	target := strings.ToLower(fs.Arg(0))
	intro := "contact request"
	if fs.NArg() > 1 {
		intro = fs.Arg(1)
	}
	relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	pin, err := resolvePin(ctx, target)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	contacts, err := fetchContacts(ctx, relayURL, identityValue, token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if existing, found := contacts.Find(target); found && existing.State == idpkg.ContactAccepted {
		fmt.Fprintf(stderr, "%s is already an accepted contact\n", target)
		return 1
	}
	contacts = contacts.Upsert(idpkg.Contact{
		Identity: target, State: idpkg.ContactRequested, PinnedKey: pin,
		AddedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err := putContacts(ctx, relayURL, identityValue, token, contacts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Ride the normal send path with the typed envelope.
	if code := runSend([]string{target, intro, "--type", idpkg.MsgTypeContactRequest, "--use-identity", identityValue}, stdout, stderr); code != 0 {
		fmt.Fprintln(stderr, "contact recorded locally as requested, but sending the request message failed")
		return code
	}
	return 0
}

// runContactsAccept pins the sender's key, marks them accepted, and sends a
// sys.contact.accept back (best-effort — their policy decides).
func runContactsAccept(args []string, stdout, stderr io.Writer) int {
	code := runContactsSet(args, stdout, stderr, idpkg.ContactAccepted, "accepted")
	if code != 0 {
		return code
	}
	fsArgs := flag.NewFlagSet("contacts accept", flag.ContinueOnError)
	fsArgs.SetOutput(io.Discard)
	useIdentity := fsArgs.String("use-identity", "", "")
	fsArgs.String("petname", "", "")
	_ = fsArgs.Parse(args)
	target := fsArgs.Arg(0)
	sendArgs := []string{target, "contact request accepted", "--type", idpkg.MsgTypeContactAccept}
	if *useIdentity != "" {
		sendArgs = append(sendArgs, "--use-identity", *useIdentity)
	}
	if sendCode := runSend(sendArgs, io.Discard, stderr); sendCode != 0 {
		fmt.Fprintln(stderr, "note: contact accepted locally, but the acceptance notification could not be sent")
	}
	return 0
}

func runContactsRm(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("contacts rm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur contacts rm <identity>")
		return 1
	}
	relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	contacts, err := fetchContacts(ctx, relayURL, identityValue, token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !contacts.Remove(fs.Arg(0)) {
		fmt.Fprintf(stderr, "%s is not in contacts\n", fs.Arg(0))
		return 1
	}
	if err := putContacts(ctx, relayURL, identityValue, token, contacts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %s\n", fs.Arg(0))
	return 0
}

func runContactsLs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("contacts ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	contacts, err := fetchContacts(context.Background(), relayURL, identityValue, token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeOutput(stdout, true, contacts, "")
	}
	if len(contacts.Contacts) == 0 {
		fmt.Fprintln(stdout, "no contacts")
		return 0
	}
	for _, c := range contacts.Contacts {
		name := c.Identity
		if c.Petname != "" {
			name = fmt.Sprintf("%s (%s)", c.Petname, c.Identity)
		}
		fmt.Fprintf(stdout, "%s\t%s\tpinned=%v\n", name, c.State, c.PinnedKey != "")
	}
	return 0
}

// runRequests drains the pending contact-request queue (challenge-signed
// with the identity key, mirroring inbox fetch).
func runRequests(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("requests", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityForDAV(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	requests, err := fetchRequests(ctx, cfg, identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		return writeOutput(stdout, true, map[string]any{"requests": requests}, "")
	}
	if len(requests) == 0 {
		fmt.Fprintln(stdout, "no pending requests")
		return 0
	}
	for _, req := range requests {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t(accept with `poweur contacts accept %s`)\n",
			req.Sender, req.Type, req.Timestamp, req.Sender)
	}
	return 0
}

// requestEntry mirrors the relay's stored request envelope.
type requestEntry struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type,omitempty"`
	Payload   string `json:"payload"`
}

func fetchRequests(ctx context.Context, cfg config.Config, identityValue string, priv ed25519.PrivateKey) ([]requestEntry, error) {
	if cfg.RelayURL == "" {
		return nil, fmt.Errorf("relay url not configured")
	}
	base := strings.TrimSuffix(cfg.RelayURL, "/")
	chResp, err := http.Get(base + "/auth/challenge?identity=" + identityValue)
	if err != nil {
		return nil, err
	}
	var ch struct {
		Challenge string `json:"challenge"`
	}
	err = json.NewDecoder(chResp.Body).Decode(&ch)
	chResp.Body.Close()
	if err != nil || ch.Challenge == "" {
		return nil, fmt.Errorf("challenge request failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/requests/"+identityValue, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Poweur-Identity", identityValue)
	req.Header.Set("X-Poweur-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(ch.Challenge))))
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("requests fetch failed: %d %s", resp.StatusCode, bytes.TrimSpace(raw))
	}
	var out struct {
		Requests []requestEntry `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Requests, nil
}

// runPolicy shows or sets the inbox policy.
func runPolicy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur policy <show|set open|contacts_only|contacts_and_requests>")
		return 1
	}
	sub := args[0]
	fs := flag.NewFlagSet("policy "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true})); err != nil {
		return 1
	}
	relayURL, identityValue, token, ok := loadShareSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	switch sub {
	case "show":
		raw, status, err := davGetBytes(ctx, relayURL, identityValue, token, inboxPolicyTreePath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		policy := idpkg.InboxPolicy{Version: 1, Mode: idpkg.DefaultInboxMode}
		if status == http.StatusOK {
			if p, err := idpkg.ParseInboxPolicy(raw); err == nil {
				policy = p
			}
		}
		if *jsonOut {
			return writeOutput(stdout, true, policy, "")
		}
		note := ""
		if status == http.StatusNotFound {
			note = " (no policy file — relay default)"
		}
		fmt.Fprintf(stdout, "inbox policy: %s%s\n", policy.Mode, note)
		return 0
	case "set":
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: poweur policy set <open|contacts_only|contacts_and_requests>")
			return 1
		}
		policy := idpkg.InboxPolicy{Version: 1, Mode: fs.Arg(0)}
		if err := policy.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		raw, _ := json.MarshalIndent(policy, "", "  ")
		if err := davPutBytes(ctx, relayURL, identityValue, token, inboxPolicyTreePath, raw); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "inbox policy set to %s\n", policy.Mode)
		return 0
	default:
		fmt.Fprintln(stderr, "unknown policy subcommand (want show, set)")
		return 1
	}
}

// checkPinnedKey enforces E07-T4 at send time: a pinned contact whose
// resolved key changed is refused unless the change is covered by the
// document's previous_keys (legitimate rotation) or --accept-new-key
// re-pins explicitly.
func checkPinnedKey(cfg config.Config, identityValue string, priv ed25519.PrivateKey, recipient string, acceptNewKey bool, stderr io.Writer) int {
	if cfg.RelayURL == "" {
		return 0 // no home relay → no synced contacts to check
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:read", priv)
	if err != nil {
		return 0 // contacts unavailable → fail open (pinning is client defense-in-depth)
	}
	contacts, err := fetchContacts(ctx, cfg.RelayURL, identityValue, tok.Token)
	if err != nil {
		return 0
	}
	contact, found := contacts.Find(recipient)
	if !found || contact.PinnedKey == "" {
		return 0
	}
	res, err := identity.ResolveIdentity(ctx, recipient)
	if err != nil {
		return 0 // resolution problems surface later in the send path
	}
	resolved, err := idpkg.ParseEd25519PublicKey(res.Document.PublicKey)
	if err != nil {
		fmt.Fprintf(stderr, "resolved key for %s is invalid: %v\n", recipient, err)
		return 1
	}
	pinned, err := idpkg.ParseEd25519PublicKey(contact.PinnedKey)
	if err == nil && bytes.Equal(resolved, pinned) {
		return 0 // pin matches
	}
	// Legitimate rotation: the new document lists the pinned key in
	// previous_keys (E01-T5 rotation statement).
	for _, prev := range res.Document.PreviousKeys {
		if prevKey, err := idpkg.ParseEd25519PublicKey(prev.PublicKey); err == nil && bytes.Equal(prevKey, pinned) {
			fmt.Fprintf(stderr, "note: %s rotated their key (old pin found in previous_keys); re-pinning\n", recipient)
			repinContact(ctx, cfg, identityValue, priv, contacts, contact, res.Document.PublicKey, stderr)
			return 0
		}
	}
	if acceptNewKey {
		fmt.Fprintf(stderr, "WARNING: re-pinning %s to a new key on your instruction (--accept-new-key)\n", recipient)
		repinContact(ctx, cfg, identityValue, priv, contacts, contact, res.Document.PublicKey, stderr)
		return 0
	}
	fmt.Fprintf(stderr,
		"REFUSING TO SEND: %s's current key does not match the pinned key and no rotation statement covers it.\n"+
			"  pinned:   %s\n  resolved: %s\n"+
			"This can mean a compromised relay or registrar impersonating your contact.\n"+
			"Verify out of band, then re-send with --accept-new-key to trust the new key.\n",
		recipient, contact.PinnedKey, res.Document.PublicKey)
	return 1
}

// repinContact best-effort updates the stored pin to newKey.
func repinContact(ctx context.Context, cfg config.Config, identityValue string, priv ed25519.PrivateKey, contacts idpkg.ContactsFile, contact idpkg.Contact, newKey string, stderr io.Writer) {
	pub, err := idpkg.ParseEd25519PublicKey(newKey)
	if err != nil {
		return
	}
	contact.PinnedKey = idpkg.FormatEd25519PublicKey(pub)
	contacts = contacts.Upsert(contact)
	tok, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		return
	}
	if err := putContacts(ctx, cfg.RelayURL, identityValue, tok.Token, contacts); err != nil {
		fmt.Fprintf(stderr, "note: failed to persist the new pin: %v\n", err)
	}
}
