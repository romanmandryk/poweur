package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// `poweur email` (EPIC-023 E23-T5): opt an identity in and out of its
// operator's email bridge, send mail through it and read what it delivered.
//
// Mail arrives as `email.message` typed messages from the bridge, so a pickup
// (`poweur inbox`) archives it like any message; `email ls` and `email read`
// show the archive's mail. Sending is an `email.send` to the bridge, signed
// and encrypted like any message.

// maxInlineAttachments is how much attachment data rides inside the
// `email.send` envelope (base64, then encrypted, under the relay's 512 KB);
// anything more is uploaded to the bridge first.
const maxInlineAttachments = 256 * 1024

func runEmail(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: poweur email <status|enable|disable|filters|send|ls|read>")
		return 1
	}
	switch args[0] {
	case "status":
		return runEmailStatus(args[1:], stdout, stderr)
	case "enable":
		return runEmailEnable(args[1:], stdout, stderr)
	case "disable":
		return runEmailDisable(args[1:], stdout, stderr)
	case "filters":
		return runEmailFilters(args[1:], stdout, stderr)
	case "send":
		return runEmailSend(args[1:], stdout, stderr)
	case "ls", "list":
		return runEmailLs(args[1:], stdout, stderr)
	case "read":
		return runEmailRead(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown email subcommand %q (want status, enable, disable, filters, send, ls, read)\n", args[0])
		return 1
	}
}

type emailState struct {
	Identity string `json:"identity"`
	Enabled  bool   `json:"enabled"`
	Address  string `json:"address,omitempty"`
	Bridge   string `json:"bridge,omitempty"`
	Offered  string `json:"offered_bridge,omitempty"`
	// Default: on without having opted in (the relay's bridge, no email.json).
	Default bool              `json:"default,omitempty"`
	OptIn   *idpkg.EmailOptIn `json:"optin,omitempty"`
}

func loadEmailState(ctx context.Context, relayURL, identityValue string, priv ed25519.PrivateKey) (emailState, idpkg.InboxPolicy, error) {
	state := emailState{Identity: identityValue}
	policy := idpkg.InboxPolicy{Version: 1, Mode: idpkg.DefaultInboxMode}
	raw, status, err := readSysFile(ctx, relayURL, identityValue, priv, inboxPolicyTreePath)
	if err != nil {
		return state, policy, err
	}
	if status == http.StatusOK {
		if p, err := idpkg.ParseInboxPolicy(raw); err == nil {
			policy = p
		}
	}
	raw, status, err = readSysFile(ctx, relayURL, identityValue, priv, idpkg.EmailOptInPath)
	if err != nil {
		return state, policy, err
	}
	if status == http.StatusOK {
		if o, err := idpkg.ParseEmailOptIn(raw); err == nil {
			state.OptIn = &o
			state.Address = o.Address
		}
	}
	if caps, err := identity.FetchCapabilities(ctx, identityValue); err == nil {
		state.Offered = caps.Endpoints["email_bridge"]
	}
	// Email is on by default through the relay's bridge; the policy can opt
	// out or name another one (which then needs email.json).
	effective := policy.EmailBridge(state.Offered)
	state.Bridge = effective
	if policy.Email != nil && state.Bridge == "" {
		state.Bridge = policy.Email.Bridge
	}
	switch {
	case effective == "":
	case state.OptIn != nil && strings.EqualFold(state.OptIn.Bridge, effective):
		state.Enabled = true
	case state.OptIn == nil && policy.Email == nil:
		if addr, err := idpkg.EmailAddressFor(identityValue); err == nil {
			state.Enabled, state.Default, state.Address = true, true, addr
		}
	}
	return state, policy, nil
}

func emailFlags(name string, stderr io.Writer) (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet("email "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs, fs.String("use-identity", "", "identity"), fs.Bool("json", false, "output json")
}

func runEmailStatus(args []string, stdout, stderr io.Writer) int {
	fs, useIdentity, jsonOut := emailFlags("status", stderr)
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	_, identityValue, priv, ok := loadIdentityKey(*useIdentity, stderr)
	if !ok {
		return 1
	}
	cfg, _ := config.Load()
	state, _, err := loadEmailState(context.Background(), cfg.RelayURL, identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	human := ""
	switch {
	case state.Enabled && state.Default:
		human = fmt.Sprintf("email: on (default) — %s through %s (poweur email disable to opt out)\n", state.Address, state.Bridge)
	case state.Enabled:
		human = fmt.Sprintf("email: on — %s through %s\n", state.Address, state.Bridge)
	case state.Offered != "":
		addr, _ := idpkg.EmailAddressFor(identityValue)
		human = fmt.Sprintf("email: off — %s is available through %s (poweur email enable)\n", addr, state.Offered)
	default:
		human = "email: off — this relay offers no email bridge\n"
	}
	return writeOutput(stdout, *jsonOut, state, human)
}

func writeInboxPolicyDoc(ctx context.Context, relayURL, identityValue string, priv ed25519.PrivateKey, policy idpkg.InboxPolicy) error {
	if policy.Version == 0 {
		policy.Version = 1
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(policy, "", "  ")
	return writeSysFile(ctx, relayURL, identityValue, priv, inboxPolicyTreePath, raw)
}

func runEmailEnable(args []string, stdout, stderr io.Writer) int {
	fs, useIdentity, jsonOut := emailFlags("enable", stderr)
	bridgeFlag := fs.String("bridge", "", "the email bridge's Poweur ID (default: the one your relay advertises)")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityKey(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	state, policy, err := loadEmailState(ctx, cfg.RelayURL, identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	bridge := strings.ToLower(strings.TrimSpace(*bridgeFlag))
	if bridge == "" {
		bridge = state.Offered
	}
	if bridge == "" {
		fmt.Fprintln(stderr, "this relay offers no email bridge; pass --bridge=<id>")
		return 1
	}
	optIn, err := idpkg.NewEmailOptIn(identityValue, bridge, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := optIn.Sign(priv); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Policy first: the moment email.json is public the bridge may deliver.
	policy.Email = &idpkg.EmailPolicy{Allow: true, Bridge: bridge}
	if err := writeInboxPolicyDoc(ctx, cfg.RelayURL, identityValue, priv, policy); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	raw, _ := json.MarshalIndent(optIn, "", "  ")
	if err := writeSysFile(ctx, cfg.RelayURL, identityValue, priv, idpkg.EmailOptInPath, raw); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	out := emailState{Identity: identityValue, Enabled: true, Address: optIn.Address, Bridge: bridge, OptIn: &optIn}
	return writeOutput(stdout, *jsonOut, out, fmt.Sprintf("email on: %s through %s\n", optIn.Address, bridge))
}

func deleteSysFile(ctx context.Context, relayURL, identityValue string, priv ed25519.PrivateKey, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, sysFileURL(relayURL, identityValue, path), nil)
	if err != nil {
		return err
	}
	if err := setOwnerAuth(req, relayURL, identityValue, priv); err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 2 {
		return nil
	}
	return parseErrorResponse("remove "+path+" failed", resp)
}

func runEmailDisable(args []string, stdout, stderr io.Writer) int {
	fs, useIdentity, jsonOut := emailFlags("disable", stderr)
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityKey(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	_, policy, err := loadEmailState(ctx, cfg.RelayURL, identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// The public statement goes first: the bridge stops accepting mail as
	// soon as its cache of email.json expires.
	if err := deleteSysFile(ctx, cfg.RelayURL, identityValue, priv, idpkg.EmailOptInPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Always written: without an email block, email is on by default.
	if policy.Email != nil {
		policy.Email.Allow = false
	} else {
		policy.Email = &idpkg.EmailPolicy{Allow: false}
	}
	if err := writeInboxPolicyDoc(ctx, cfg.RelayURL, identityValue, priv, policy); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, *jsonOut, emailState{Identity: identityValue}, "email off\n")
}

func parseAddressList(values []string) ([]idpkg.EmailAddress, error) {
	var out []idpkg.EmailAddress
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			a, err := parseMailbox(part)
			if err != nil {
				return nil, err
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// parseMailbox accepts `a@b`, `<a@b>` and `Name <a@b>`.
func parseMailbox(s string) (idpkg.EmailAddress, error) {
	s = strings.TrimSpace(s)
	a := idpkg.EmailAddress{Address: s}
	if i := strings.LastIndex(s, "<"); i >= 0 && strings.HasSuffix(s, ">") {
		a = idpkg.EmailAddress{Name: strings.Trim(strings.TrimSpace(s[:i]), `"`), Address: s[i+1 : len(s)-1]}
	}
	if err := a.Validate(); err != nil {
		return idpkg.EmailAddress{}, err
	}
	return a, nil
}

func runEmailSend(args []string, stdout, stderr io.Writer) int {
	fs, useIdentity, jsonOut := emailFlags("send", stderr)
	var to, cc, bcc, attach, refs stringList
	fs.Var(&to, "to", "recipient (repeatable, or comma-separated; `Name <a@b>` allowed)")
	fs.Var(&cc, "cc", "copy recipient (repeatable)")
	fs.Var(&bcc, "bcc", "blind copy recipient (repeatable)")
	fs.Var(&attach, "attach", "file to attach (repeatable)")
	fs.Var(&refs, "references", "message ids this replies within (repeatable)")
	subject := fs.String("subject", "", "subject line")
	text := fs.String("text", "", "message text (- reads stdin)")
	textFile := fs.String("text-file", "", "read the message text from a file")
	htmlFile := fs.String("html-file", "", "also send this HTML (the bridge sanitizes it)")
	inReplyTo := fs.String("in-reply-to", "", "Message-ID of the email this answers")
	fromName := fs.String("from-name", "", "display name for the From line")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		fmt.Fprintln(stderr, "usage: poweur email send --to=<address> --subject=<s> --text=<t> [--attach=<file>]")
		return 1
	}
	cfg, identityValue, priv, ok := loadIdentityKey(*useIdentity, stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	state, _, err := loadEmailState(ctx, cfg.RelayURL, identityValue, priv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !state.Enabled {
		fmt.Fprintln(stderr, "email is not enabled for this identity (poweur email enable)")
		return 1
	}
	body := *text
	switch {
	case *textFile != "":
		raw, err := os.ReadFile(*textFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		body = string(raw)
	case body == "-":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		body = string(raw)
	}
	send := idpkg.EmailSend{
		Version: idpkg.EmailPayloadVersion, SendID: idpkg.NewThreadID(), FromName: *fromName,
		Subject: *subject, Text: body, InReplyTo: *inReplyTo, References: refs,
	}
	if send.To, err = parseAddressList(to); err == nil {
		if send.Cc, err = parseAddressList(cc); err == nil {
			send.Bcc, err = parseAddressList(bcc)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *htmlFile != "" {
		raw, err := os.ReadFile(*htmlFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		send.HTML = string(raw)
	}
	if len(attach) > 0 {
		atts, err := prepareEmailAttachments(ctx, identityValue, priv, state.Bridge, attach)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		send.Attachments = atts
	}
	if err := send.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	payload, _ := json.Marshal(send)
	var out bytes.Buffer
	sendArgs := []string{state.Bridge, string(payload), "--type=" + idpkg.MsgTypeEmailSend, "--use-identity=" + identityValue, "--json"}
	if code := runSend(sendArgs, &out, stderr); code != 0 {
		return code
	}
	var result map[string]any
	_ = json.Unmarshal(out.Bytes(), &result)
	result["send_id"] = send.SendID
	return writeOutput(stdout, *jsonOut, result, fmt.Sprintf("email handed to %s (send %s); `poweur inbox` shows its status\n", state.Bridge, send.SendID))
}

// prepareEmailAttachments inlines small attachments and uploads the rest to
// the bridge, sealed to its key.
func prepareEmailAttachments(ctx context.Context, identityValue string, priv ed25519.PrivateKey, bridge string, paths []string) ([]idpkg.EmailOutAttachment, error) {
	type file struct {
		name, mime string
		data       []byte
	}
	var files []file
	total := 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(p)
		kind := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		if kind == "" {
			kind = http.DetectContentType(data)
		}
		files = append(files, file{name: name, mime: kind, data: data})
		total += len(data)
	}
	var out []idpkg.EmailOutAttachment
	for _, f := range files {
		a := idpkg.EmailOutAttachment{Name: f.name, MIME: f.mime, Size: int64(len(f.data))}
		if total <= maxInlineAttachments {
			a.Data = base64.StdEncoding.EncodeToString(f.data)
		} else {
			id, sum, err := uploadEmailAttachment(ctx, identityValue, priv, bridge, f.data)
			if err != nil {
				return nil, fmt.Errorf("upload %s: %w", f.name, err)
			}
			a.Upload, a.SHA256 = id, sum
		}
		out = append(out, a)
	}
	return out, nil
}

func uploadEmailAttachment(ctx context.Context, identityValue string, priv ed25519.PrivateKey, bridge string, data []byte) (string, string, error) {
	caps, err := identity.FetchCapabilities(ctx, bridge)
	if err != nil {
		return "", "", fmt.Errorf("bridge capabilities: %w", err)
	}
	api := strings.TrimRight(caps.Endpoints["email_api"], "/")
	if api == "" {
		return "", "", fmt.Errorf("%s advertises no upload endpoint; attachments must fit %d KB", bridge, maxInlineAttachments/1024)
	}
	encPub, err := identity.LookupEncryptionKey(ctx, bridge)
	if err != nil || len(encPub) != 32 {
		return "", "", fmt.Errorf("bridge encryption key: %v", err)
	}
	uploadID := idpkg.NewThreadID()
	sealed, err := idpkg.Seal(encPub, data, idpkg.EmailUploadSealDomain, idpkg.EmailBlobContext(uploadID))
	if err != nil {
		return "", "", err
	}
	body, _ := json.Marshal(sealed)
	digest := sha256.Sum256(body)
	issued := time.Now().UTC().Format(time.RFC3339)
	nonceBytes := make([]byte, 16)
	_, _ = rand.Read(nonceBytes)
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	canonical := idpkg.CanonicalEmailUpload(identityValue, uploadID, hex.EncodeToString(digest[:]), int64(len(body)), issued, nonce)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, api+"/v1/uploads/"+uploadID, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Poweur-Identity", identityValue)
	req.Header.Set("X-Poweur-Timestamp", issued)
	req.Header.Set("X-Poweur-Nonce", nonce)
	req.Header.Set("X-Poweur-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(canonical))))
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("bridge refused the upload (%d): %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	plainSum := sha256.Sum256(data)
	return uploadID, hex.EncodeToString(plainSum[:]), nil
}

// mailEntry is one archived mail message, newest first in `email ls`.
type mailEntry struct {
	N      int                 `json:"n"`
	ID     string              `json:"id"`
	At     string              `json:"at"`
	Kind   string              `json:"kind"` // received | sent | status
	Email  *idpkg.EmailMessage `json:"email,omitempty"`
	Send   *idpkg.EmailSend    `json:"send,omitempty"`
	Status *idpkg.EmailStatus  `json:"status,omitempty"`
	// Settings: one of our own email.settings (kind "settings").
	Settings *idpkg.EmailSettings `json:"settings,omitempty"`
	Unread   bool                 `json:"unread,omitempty"`
	Bridge   string               `json:"bridge"`
}

// loadMail picks up the inbox (so new mail is archived), then reads every
// mail record from the archive: email.message and email.status from the
// trusted bridge, and our own email.send copies.
func loadMail(useIdentity string, stderr io.Writer) ([]mailEntry, *historyStore, idpkg.ReadState, string, error) {
	_ = runInbox([]string{"--use-identity=" + useIdentity}, io.Discard, io.Discard)
	cfg, identityValue, priv, ok := loadIdentityKey(useIdentity, stderr)
	if !ok {
		return nil, nil, idpkg.ReadState{}, "", errors.New("identity not configured")
	}
	ctx := context.Background()
	state, _, err := loadEmailState(ctx, cfg.RelayURL, identityValue, priv)
	if err != nil {
		return nil, nil, idpkg.ReadState{}, "", err
	}
	bridge := state.Bridge
	if bridge == "" {
		return nil, nil, idpkg.ReadState{}, "", errors.New("email has never been enabled for this identity")
	}
	store, err := openHistoryStore(identityValue)
	if err != nil {
		return nil, nil, idpkg.ReadState{}, "", err
	}
	records, err := store.conversationRecords(ctx, bridge)
	if err != nil {
		return nil, nil, idpkg.ReadState{}, "", err
	}
	read, _ := store.ReadState(ctx)
	var out []mailEntry
	for _, row := range records {
		r := row.Record
		e := mailEntry{ID: r.ID, At: r.Timestamp, Bridge: bridge}
		switch idpkg.NormalizeMessageType(r.Type) {
		case idpkg.MsgTypeEmailMessage:
			if !strings.EqualFold(r.Sender, bridge) {
				continue
			}
			var m idpkg.EmailMessage
			if json.Unmarshal([]byte(r.Body), &m) != nil {
				continue
			}
			e.Kind, e.Email = "received", &m
			e.Unread = !read.Covers(bridge, idpkg.EmailThreadKey(m, r.ID), r.Timestamp, r.ID)
		case idpkg.MsgTypeEmailStatus:
			if !strings.EqualFold(r.Sender, bridge) {
				continue
			}
			var s idpkg.EmailStatus
			if json.Unmarshal([]byte(r.Body), &s) != nil {
				continue
			}
			e.Kind, e.Status = "status", &s
		case idpkg.MsgTypeEmailSend:
			var s idpkg.EmailSend
			if json.Unmarshal([]byte(r.Body), &s) != nil {
				continue
			}
			e.Kind, e.Send = "sent", &s
		case idpkg.MsgTypeEmailSettings:
			var s idpkg.EmailSettings
			if strings.EqualFold(r.Sender, bridge) || json.Unmarshal([]byte(r.Body), &s) != nil {
				continue
			}
			e.Kind, e.Settings = "settings", &s
		default:
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	for i := range out {
		out[i].N = i + 1
	}
	return out, store, read, bridge, nil
}

func runEmailLs(args []string, stdout, stderr io.Writer) int {
	fs, useIdentity, jsonOut := emailFlags("ls", stderr)
	limit := fs.Int("limit", 20, "how many to list")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	mail, _, _, _, err := loadMail(*useIdentity, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	statuses := map[string]string{}
	for _, e := range mail {
		if e.Status != nil {
			if _, seen := statuses[e.Status.SendID]; !seen {
				statuses[e.Status.SendID] = e.Status.State
			}
		}
	}
	var shown []mailEntry
	for _, e := range mail {
		if e.Kind == "received" || e.Kind == "sent" {
			shown = append(shown, e)
		}
	}
	if *limit > 0 && len(shown) > *limit {
		shown = shown[:*limit]
	}
	if *jsonOut {
		return writeOutput(stdout, true, map[string]any{"mail": shown, "statuses": statuses}, "")
	}
	if len(shown) == 0 {
		fmt.Fprintln(stdout, "no email")
		return 0
	}
	for _, e := range shown {
		switch e.Kind {
		case "received":
			mark := " "
			if e.Unread {
				mark = "•"
			}
			fmt.Fprintf(stdout, "%s %3d  %s  %-30s  %s%s\n", mark, e.N, e.At, truncateRunes(e.Email.From.String(), 30), e.Email.Subject, authNote(e.Email.Auth))
		case "sent":
			state := statuses[e.Send.SendID]
			if state == "" {
				state = "pending"
			}
			to := ""
			if len(e.Send.To) > 0 {
				to = e.Send.To[0].Address
			}
			fmt.Fprintf(stdout, "  %3d  %s  → %-28s  %s [%s]\n", e.N, e.At, truncateRunes(to, 28), e.Send.Subject, state)
		}
	}
	return 0
}

func authNote(a idpkg.EmailAuth) string {
	switch {
	case a.Spam == "spam":
		return "  [spam]"
	case a.Verdict == idpkg.EmailFailed:
		return "  [failed authentication]"
	case a.Verdict != idpkg.EmailVerified:
		return "  [unverified sender]"
	}
	return ""
}

func truncateRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func runEmailRead(args []string, stdout, stderr io.Writer) int {
	fs, useIdentity, jsonOut := emailFlags("read", stderr)
	htmlOut := fs.String("html", "", "write the (sanitized) HTML part to this file")
	saveDir := fs.String("save", "", "save the attachments into this directory")
	rawOut := fs.String("raw", "", "write the original message (.eml) to this file")
	keepUnread := fs.Bool("keep-unread", false, "do not mark it read")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--keep-unread": true})); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur email read <n|message-id> [--html=<file>] [--save=<dir>] [--raw=<file>]")
		return 1
	}
	mail, store, read, bridge, err := loadMail(*useIdentity, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var found *mailEntry
	n, _ := strconv.Atoi(fs.Arg(0))
	for i := range mail {
		if mail[i].ID == fs.Arg(0) || (n > 0 && mail[i].N == n) {
			found = &mail[i]
			break
		}
	}
	if found == nil || found.Kind == "status" {
		fmt.Fprintln(stderr, "no such email (see poweur email ls)")
		return 1
	}
	if found.Kind == "sent" {
		return writeOutput(stdout, *jsonOut, found, fmt.Sprintf("To: %s\nSubject: %s\n\n%s\n", joinAddresses(found.Send.To), found.Send.Subject, found.Send.Text))
	}
	m := found.Email
	ctx := context.Background()
	var encPriv []byte
	if *saveDir != "" || *rawOut != "" {
		cfg, err := config.Load()
		if err == nil {
			encPriv, _ = identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, store.identity))
		}
		if encPriv == nil {
			fmt.Fprintln(stderr, "no local encryption key to open attachments with")
			return 1
		}
	}
	if *htmlOut != "" && m.HTML != "" {
		if err := os.WriteFile(*htmlOut, []byte(m.HTML), 0o600); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *saveDir != "" {
		if err := os.MkdirAll(*saveDir, 0o700); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for _, a := range m.Attachments {
			data, err := attachmentBytes(ctx, encPriv, a)
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", a.Name, err)
				continue
			}
			name := filepath.Base(strings.ReplaceAll(a.Name, "\\", "/"))
			if name == "." || name == "/" || name == "" {
				name = "attachment"
			}
			if err := os.WriteFile(filepath.Join(*saveDir, name), data, 0o600); err != nil {
				fmt.Fprintln(stderr, err)
			}
		}
	}
	if *rawOut != "" && m.Raw != nil {
		data, err := fetchEmailBlob(ctx, encPriv, *m.Raw)
		if err != nil {
			fmt.Fprintln(stderr, "original:", err)
		} else if err := os.WriteFile(*rawOut, data, 0o600); err != nil {
			fmt.Fprintln(stderr, err)
		}
	}
	if !*keepUnread && found.Unread {
		next := read.MarkThreadRead(bridge, idpkg.EmailThreadKey(*m, found.ID), found.At, found.ID)
		_ = store.PutReadState(ctx, next)
	}
	if *jsonOut {
		return writeOutput(stdout, true, found, "")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s%s\n", m.From.String(), authNote(m.Auth))
	if len(m.To) > 0 {
		fmt.Fprintf(&b, "To: %s\n", joinAddresses(m.To))
	}
	if len(m.Cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\n", joinAddresses(m.Cc))
	}
	fmt.Fprintf(&b, "Date: %s\nSubject: %s\n", m.Date, m.Subject)
	if m.MessageID != "" {
		fmt.Fprintf(&b, "Message-ID: %s\n", m.MessageID)
	}
	for _, a := range m.Attachments {
		fmt.Fprintf(&b, "Attachment: %s (%s, %d bytes)\n", a.Name, a.MIME, a.Size)
	}
	text := m.Text
	if text == "" && m.HTML != "" {
		text = "[HTML only — read it with --html=<file>]"
	}
	fmt.Fprintf(&b, "\n%s\n", text)
	fmt.Fprint(stdout, b.String())
	return 0
}

func joinAddresses(list []idpkg.EmailAddress) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}

func attachmentBytes(ctx context.Context, encPriv []byte, a idpkg.EmailAttachment) ([]byte, error) {
	if a.Data != "" {
		return base64.StdEncoding.DecodeString(a.Data)
	}
	if a.Blob == nil {
		return nil, errors.New("attachment has no content")
	}
	return fetchEmailBlob(ctx, encPriv, *a.Blob)
}

// fetchEmailBlob downloads a blob the bridge keeps for us and opens it.
func fetchEmailBlob(ctx context.Context, encPriv []byte, ref idpkg.EmailBlobRef) ([]byte, error) {
	if !strings.HasPrefix(ref.URL, "https://") && !strings.HasPrefix(ref.URL, "http://") {
		return nil, errors.New("blob url must be http(s)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, errors.New("expired on the bridge")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blob fetch: HTTP %d", resp.StatusCode)
	}
	var sealed idpkg.SealedPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*idpkg.MaxEmailBlobBytes)).Decode(&sealed); err != nil {
		return nil, err
	}
	plain, err := idpkg.OpenSeal(encPriv, sealed, idpkg.EmailBlobSealDomain, idpkg.EmailBlobContext(ref.ID))
	if err != nil {
		return nil, err
	}
	if ref.SHA256 != "" {
		sum := sha256.Sum256(plain)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), ref.SHA256) {
			return nil, errors.New("blob does not match its hash")
		}
	}
	return plain, nil
}
