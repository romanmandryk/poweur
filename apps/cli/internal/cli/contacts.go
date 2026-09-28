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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Contacts, inbox policy & requests (EPIC-007 E07-T3/T4). Contacts live at
// poweur-sys/relay/contacts.json in the owner's tree (written over DAV, so
// they sync across devices like any file); the relay enforces the inbox
// policy from the same zone. Keys are pinned at add/accept time (TOFU).

const (
	contactsTreePath    = ".poweur/relay/contacts.json"
	inboxPolicyTreePath = ".poweur/relay/inbox-policy.json"
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
func fetchContacts(ctx context.Context, relayURL, identityValue string, priv ed25519.PrivateKey) (idpkg.ContactsFile, error) {
	raw, status, err := readSysFile(ctx, relayURL, identityValue, priv, contactsTreePath)
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

func putContacts(ctx context.Context, relayURL, identityValue string, priv ed25519.PrivateKey, file idpkg.ContactsFile) error {
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return writeSysFile(ctx, relayURL, identityValue, priv, contactsTreePath, raw)
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
	if err := fs.Parse(normalizeArgs(args, nil)); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: poweur contacts %s <identity> [--petname=...]\n",
			map[string]string{"added": "add", "blocked": "block", "accepted": "accept"}[verb])
		return 1
	}
	target := strings.ToLower(fs.Arg(0))
	relayURL, identityValue, token, ok := loadSysSession(*useIdentity, stderr)
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
	// Pinning is trust-on-first-use, so this is the moment the pin is worth
	// verifying — print the safety number while the user is still here.
	if entry.PinnedKey != "" {
		fmt.Fprintf(stdout, "safety number: %s\n", idpkg.FingerprintOrKey(entry.PinnedKey))
	}
	return 0
}

// runContactsRequest records the outbound request (state=requested) and
// sends a sys.contact.request typed message with an encrypted intro.
func runContactsRequest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("contacts request", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	if err := fs.Parse(normalizeArgs(args, nil)); err != nil {
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
	relayURL, identityValue, token, ok := loadSysSession(*useIdentity, stderr)
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
	_ = fsArgs.Parse(normalizeArgs(args, nil))
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
	if err := fs.Parse(normalizeArgs(args, nil)); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur contacts rm <identity>")
		return 1
	}
	relayURL, identityValue, token, ok := loadSysSession(*useIdentity, stderr)
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
	relayURL, identityValue, token, ok := loadSysSession(*useIdentity, stderr)
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
		// The fingerprint is the pin's user-facing form (E07-T4): the point
		// of a pin is that somebody compared it out of band, and nobody
		// compares 43 characters of base64.
		pin := "not pinned"
		if c.PinnedKey != "" {
			pin = idpkg.FingerprintOrKey(c.PinnedKey)
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", name, c.State, pin)
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
	cfg, identityValue, priv, ok := loadIdentityKey(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	requests, err := fetchRequests(ctx, cfg, identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// The queue carries answers as well as questions: a contact accept is
	// routed here rather than into the message stream, in every inbox mode.
	// Reading the queue is therefore the moment to finish those handshakes —
	// and the drain means this is the only chance to see them.
	var accepts []string
	var pending []requestEntry
	for _, req := range requests {
		if req.Type == idpkg.MsgTypeContactAccept {
			accepts = append(accepts, req.Sender)
			continue
		}
		pending = append(pending, req)
	}
	promoteAcceptedContacts(ctx, *useIdentity, accepts, stdout, stderr)

	// Show what the stranger actually said (E07-T3): accept-or-block is a
	// judgement, and the intro is the only evidence the format carries.
	encPriv, _ := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, identityValue))
	decryptRequestIntros(pending, encPriv)

	if *jsonOut {
		return writeOutput(stdout, true, map[string]any{"requests": pending, "accepted": accepts}, "")
	}
	if len(pending) == 0 {
		fmt.Fprintln(stdout, "no pending requests")
		return 0
	}
	for _, req := range pending {
		if line, ok := pendingShareInstruction(req, cfg.KeysDir); ok {
			fmt.Fprint(stdout, line)
			continue
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t(accept with `poweur contacts accept %s`)\n", req.Sender, req.Type, req.Timestamp, req.Sender)
		if req.Plaintext != "" {
			fmt.Fprintf(stdout, "  🔒 %s\n", req.Plaintext)
		}
	}
	return 0
}

// requestEntry mirrors the relay's stored request envelope. Plaintext is
// filled in locally by decryptRequestIntros — it is never on the wire.
type requestEntry struct {
	ID         string            `json:"id"`
	Sender     string            `json:"sender"`
	Recipient  string            `json:"recipient"`
	Timestamp  string            `json:"timestamp"`
	Type       string            `json:"type,omitempty"`
	ThreadID   string            `json:"thread_id,omitempty"`
	ExpiresAt  string            `json:"expires_at,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Payload    string            `json:"payload"`
	Encryption *EncryptionMeta   `json:"encryption,omitempty"`
	Plaintext  string            `json:"plaintext,omitempty"`
}

// decryptRequestIntros opens each request's E2E-encrypted intro in place.
//
// The intro is the entire point of the requests queue: `contacts_and_requests`
// exists so a stranger can say who they are before you decide, and a queue
// that shows a name and a timestamp asks you to accept or block someone on
// nothing at all. Failures are written into Plaintext rather than returned —
// one unreadable intro must not hide the rest of the queue, and the drain
// means there is no second chance to look.
func decryptRequestIntros(requests []requestEntry, encPriv []byte) {
	for i := range requests {
		req := &requests[i]
		if req.Encryption == nil || req.Encryption.Alg == "" {
			continue
		}
		if encPriv == nil {
			req.Plaintext = "[encrypted: no local encryption key]"
			continue
		}
		plaintext, err := cryptoe2e.Decrypt(encPriv, cryptoe2e.EncryptedPayload{
			Ciphertext:         req.Payload,
			EphemeralPublicKey: req.Encryption.EphemeralPublicKey,
			Nonce:              req.Encryption.Nonce,
		})
		if err != nil {
			req.Plaintext = fmt.Sprintf("[decrypt failed: %v]", err)
			continue
		}
		req.Plaintext = string(plaintext)
	}
}

// pendingShareInstruction formats one drained share offer or claim. Shares
// return with storage v2 (EPIC-020 E20-T7); until then an offer or claim is
// reported and cannot be acted on. Other request types return ok=false.
func pendingShareInstruction(req requestEntry, keysDir string) (string, bool) {
	switch req.Type {
	case idpkg.MsgTypeShareOffer, idpkg.MsgTypeShareClaim:
		return fmt.Sprintf("%s\t%s\t%s\t(shares are not available until the new storage lands)\n",
			req.Sender, req.Type, req.Timestamp), true
	default:
		return "", false
	}
}

func saveRequestDocument(keysDir, messageID string, body []byte) (string, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return "", fmt.Errorf("request has no id")
	}
	dir := filepath.Join(filepath.Dir(keysDir), "requests")
	if keysDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".poweur", "requests")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, messageID)
	if len(safe) > 80 {
		safe = safe[:80]
	}
	path := filepath.Join(dir, safe+".json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// challengeSignedGet performs an owner-drain GET (requests / anon queues):
// fetch a challenge, sign it with the identity key, call the endpoint.
func challengeSignedGet(ctx context.Context, cfg config.Config, identityValue string, priv ed25519.PrivateKey, path string) ([]byte, error) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+identityValue, nil)
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
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s fetch failed: %d %s", path, resp.StatusCode, bytes.TrimSpace(raw))
	}
	return raw, nil
}

func fetchRequests(ctx context.Context, cfg config.Config, identityValue string, priv ed25519.PrivateKey) ([]requestEntry, error) {
	raw, err := challengeSignedGet(ctx, cfg, identityValue, priv, "/requests/")
	if err != nil {
		return nil, err
	}
	var out struct {
		Requests []requestEntry `json:"requests"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
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
	anonAllow := fs.Bool("anon-allow", false, "accept anonymous (unsigned) messages")
	anonChallenge := fs.String("anon-challenge", "", "challenge for anonymous senders: none | pow | verified | payment")
	anonBits := fs.Int("anon-bits", 0, "proof-of-work difficulty in bits (0 = relay default; each +1 doubles the work)")
	anonMaxBytes := fs.Int("anon-max-bytes", 0, "max anonymous payload bytes (0 = default 4096)")
	anonMaxPerDay := fs.Int("anon-max-per-day", 0, "max accepted anonymous messages per day (0 = default 20)")
	readReceipts := fs.Bool("read-receipts", true, "send read receipts")
	var noReadFor stringList
	fs.Var(&noReadFor, "no-read-receipt-for", "identity that must not receive read receipts (repeatable)")
	var trustedAuth stringList
	fs.Var(&trustedAuth, "trusted-auth", "sign-in service (OAuth bridge) allowed to send you sign-in prompts (repeatable; \"none\" clears)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args[1:], map[string]bool{"--json": true, "--anon-allow": true})); err != nil {
		return 1
	}
	relayURL, identityValue, token, ok := loadSysSession(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	switch sub {
	case "show":
		raw, status, err := readSysFile(ctx, relayURL, identityValue, token, inboxPolicyTreePath)
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
		if policy.Anonymous != nil && policy.Anonymous.Allow {
			fmt.Fprintf(stdout, "anonymous: allowed, challenge=%s", policy.Anonymous.EffectiveChallenge())
			if policy.Anonymous.EffectiveChallenge() == idpkg.AnonChallengePow {
				fmt.Fprintf(stdout, " (%d bits)", idpkg.ClampPowBits(policy.Anonymous.PowBits))
			}
			fmt.Fprintf(stdout, ", max %d bytes, %d/day\n",
				policy.Anonymous.EffectiveMaxBytes(), policy.Anonymous.EffectiveMaxPerDay())
		} else {
			fmt.Fprintln(stdout, "anonymous: denied (default)")
		}
		if len(policy.TrustedAuthServices) > 0 {
			fmt.Fprintf(stdout, "sign-in prompts from: %s\n", strings.Join(policy.TrustedAuthServices, ", "))
		} else {
			fmt.Fprintln(stdout, "sign-in prompts: from nobody (default)")
		}
		if policy.ReadReceipts == nil {
			fmt.Fprintln(stdout, "read receipts: enabled (default)")
		} else {
			fmt.Fprintf(stdout, "read receipts: enabled=%t", policy.ReadReceipts.Enabled)
			if len(policy.ReadReceipts.DisabledFor) > 0 {
				fmt.Fprintf(stdout, ", disabled for %s", strings.Join(policy.ReadReceipts.DisabledFor, ", "))
			}
			fmt.Fprintln(stdout)
		}
		return 0
	case "set":
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: poweur policy set <open|contacts_only|contacts_and_requests> [--anon-allow --anon-challenge=pow --anon-bits=N]")
			return 1
		}
		policy := idpkg.InboxPolicy{Version: 1, Mode: fs.Arg(0)}
		policy.ReadReceipts = &idpkg.ReadReceiptPolicy{Enabled: *readReceipts, DisabledFor: noReadFor}
		// Trusted sign-in services survive a mode change unless named here.
		if len(trustedAuth) > 0 {
			if !(len(trustedAuth) == 1 && strings.EqualFold(trustedAuth[0], "none")) {
				for _, s := range trustedAuth {
					policy.TrustedAuthServices = append(policy.TrustedAuthServices, strings.ToLower(strings.TrimSpace(s)))
				}
			}
		} else if raw, status, err := readSysFile(ctx, relayURL, identityValue, token, inboxPolicyTreePath); err == nil && status == http.StatusOK {
			if prev, err := idpkg.ParseInboxPolicy(raw); err == nil {
				policy.TrustedAuthServices = prev.TrustedAuthServices
			}
		}
		if *anonAllow || *anonChallenge != "" || *anonBits > 0 {
			policy.Anonymous = &idpkg.AnonymousPolicy{
				Allow:     *anonAllow,
				Challenge: *anonChallenge,
				PowBits:   *anonBits,
				MaxBytes:  *anonMaxBytes,
				MaxPerDay: *anonMaxPerDay,
			}
		}
		if err := policy.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		raw, _ := json.MarshalIndent(policy, "", "  ")
		if err := writeSysFile(ctx, relayURL, identityValue, token, inboxPolicyTreePath, raw); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		anonNote := ""
		if policy.Anonymous != nil && policy.Anonymous.Allow {
			anonNote = fmt.Sprintf(" (anonymous allowed, challenge=%s)", policy.Anonymous.EffectiveChallenge())
		}
		if len(policy.TrustedAuthServices) > 0 {
			anonNote += fmt.Sprintf(" (sign-in prompts from %s)", strings.Join(policy.TrustedAuthServices, ", "))
		}
		fmt.Fprintf(stdout, "inbox policy set to %s%s\n", policy.Mode, anonNote)
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
	contacts, err := fetchContacts(ctx, cfg.RelayURL, identityValue, priv)
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
	// Print the fingerprints first and the full keys after: the fingerprint
	// is what the user can actually read to their contact over the phone,
	// which is the verification this message is asking them to perform.
	fmt.Fprintf(stderr,
		"REFUSING TO SEND: %s's current key does not match the pinned key and no rotation statement covers it.\n"+
			"  pinned safety number:   %s\n"+
			"  resolved safety number: %s\n"+
			"  pinned key:   %s\n  resolved key: %s\n"+
			"This can mean a compromised relay or registrar impersonating your contact.\n"+
			"Read the safety numbers to %s over a channel you already trust; if they match theirs,\n"+
			"re-send with --accept-new-key to trust the new key.\n",
		recipient,
		idpkg.FingerprintOrKey(contact.PinnedKey), idpkg.FingerprintOrKey(res.Document.PublicKey),
		contact.PinnedKey, res.Document.PublicKey, recipient)
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
	if err := putContacts(ctx, cfg.RelayURL, identityValue, priv, contacts); err != nil {
		fmt.Fprintf(stderr, "note: failed to persist the new pin: %v\n", err)
	}
}

// promoteAcceptedContacts finishes handshakes this identity started.
//
// `contacts request` records the target as `requested` and pins the key it
// addressed. Their answer — a `sys.contact.accept` — arrives later in the
// requests queue (in every inbox mode), and until something acts on it our
// own contacts still say `requested`. That matters beyond cosmetics: our
// own policy reads the same file, so a `contacts_only` inbox goes on
// bouncing the person who just accepted us.
//
// Promotion is deliberately narrow. Only someone we ourselves asked is
// promoted, and only while the key we pinned when we asked is still theirs —
// an accept is a reason to finish what we started, never a reason to re-pin.
// Callers pass the senders of the accepts they just read; the helper is
// best-effort and reports what it changed on stderr.
func promoteAcceptedContacts(ctx context.Context, useIdentity string, senders []string, stdout, stderr io.Writer) {
	if len(senders) == 0 {
		return
	}
	relayURL, identityValue, token, ok := loadSysSession(useIdentity, io.Discard)
	if !ok {
		return
	}
	contacts, err := fetchContacts(ctx, relayURL, identityValue, token)
	if err != nil {
		return
	}
	changed := false
	for _, sender := range senders {
		existing, found := contacts.Find(strings.ToLower(sender))
		if !found || existing.State != idpkg.ContactRequested {
			continue
		}
		pin, err := resolvePin(ctx, existing.Identity)
		if err != nil {
			fmt.Fprintf(stderr, "note: %s accepted, but their key could not be resolved: %v\n", existing.Identity, err)
			continue
		}
		if existing.PinnedKey != "" && pin != existing.PinnedKey {
			fmt.Fprintf(stderr, "warning: %s accepted, but their key changed since you asked — "+
				"left as requested; run `poweur contacts accept %s` to trust the new key\n",
				existing.Identity, existing.Identity)
			continue
		}
		existing.State = idpkg.ContactAccepted
		existing.PinnedKey = pin
		contacts = contacts.Upsert(existing)
		changed = true
		fmt.Fprintf(stdout, "🤝 %s accepted your contact request\n", existing.Identity)
	}
	if !changed {
		return
	}
	if err := putContacts(ctx, relayURL, identityValue, token, contacts); err != nil {
		fmt.Fprintln(stderr, "note: could not record the acceptance:", err)
	}
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	if v = strings.TrimSpace(v); v != "" {
		*s = append(*s, v)
	}
	return nil
}
