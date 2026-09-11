package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Client-side message history (EPIC-009 E09-T1, client half).
//
// The relay spool is a delivery buffer, not an archive: a pickup drains it.
// So the moment a message is read is the only moment it can be kept, and this
// file is where the CLI keeps it — sealed, in the owner-only zone of their own
// tree, which means it survives a reinstall and is readable from any device
// that holds the identity's encryption key.
//
// The format lives in packages/identity (`history.go`) because the TypeScript
// client and the web app write the same files.

// historyStore is one identity's archive, over DAV.
type historyStore struct {
	relayURL string
	identity string
	token    string
	// encPriv is the identity's X25519 private key: history is sealed to the
	// owner's own encryption key, so this both seals and opens.
	encPriv []byte
	encPub  []byte
}

// openHistoryStore prepares the archive for an identity, or reports why it
// cannot. A missing encryption key is not fatal to anything else the CLI
// does, so callers treat the error as "skip archiving" rather than "fail".
func openHistoryStore(useIdentity string) (*historyStore, error) {
	cfg, identityValue, priv, ok := loadIdentityForDAV(useIdentity, io.Discard)
	if !ok {
		return nil, fmt.Errorf("identity not configured")
	}
	encPriv, err := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, identityValue))
	if err != nil || encPriv == nil {
		return nil, fmt.Errorf("no local encryption key for %s: history needs one to seal to", identityValue)
	}
	encPub, err := cryptoe2e.PublicFromPrivate(encPriv)
	if err != nil {
		return nil, err
	}
	tok, err := MintDAVToken(context.Background(), cfg.RelayURL, identityValue, "", "dav:full", priv)
	if err != nil {
		return nil, err
	}
	return &historyStore{
		relayURL: cfg.RelayURL, identity: identityValue, token: tok.Token,
		encPriv: encPriv, encPub: encPub,
	}, nil
}

// seal wraps a document for the owner's eyes only, using the same envelope
// messages already use with the owner as their own recipient.
func (h *historyStore) seal(doc any) ([]byte, error) {
	plaintext, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	sealed, err := cryptoe2e.Encrypt(h.encPub, plaintext)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(idpkg.SealedDocument{
		Version:            idpkg.HistoryVersion,
		Alg:                cryptoe2e.AlgName,
		EphemeralPublicKey: sealed.EphemeralPublicKey,
		Nonce:              sealed.Nonce,
		Ciphertext:         sealed.Ciphertext,
	}, "", "  ")
}

// open reverses seal.
func (h *historyStore) open(raw []byte, into any) error {
	doc, err := idpkg.ParseSealedDocument(raw)
	if err != nil {
		return err
	}
	plaintext, err := cryptoe2e.Decrypt(h.encPriv, cryptoe2e.EncryptedPayload{
		Ciphertext:         doc.Ciphertext,
		EphemeralPublicKey: doc.EphemeralPublicKey,
		Nonce:              doc.Nonce,
	})
	if err != nil {
		return err
	}
	return json.Unmarshal(plaintext, into)
}

// Append writes one record. Writes are idempotent by construction — the path
// is a function of (timestamp, id) — so re-archiving a message a second
// device already stored costs a PUT and changes nothing.
func (h *historyStore) Append(ctx context.Context, record idpkg.HistoryRecord) error {
	record.Version = idpkg.HistoryVersion
	if err := record.Validate(); err != nil {
		return err
	}
	body, err := h.seal(record)
	if err != nil {
		return err
	}
	path := idpkg.HistoryPath(record.Timestamp, record.ID)
	if err := davPutBytes(ctx, h.relayURL, h.identity, h.token, path, body); err != nil {
		// WebDAV PUT does not create parent collections, and the month shard
		// is new on the first message of every month. Making it and retrying
		// keeps the common path a single request instead of an MKCOL before
		// every write.
		if mkErr := h.ensureShard(ctx, record.Timestamp); mkErr != nil {
			return err
		}
		return davPutBytes(ctx, h.relayURL, h.identity, h.token, path, body)
	}
	return nil
}

// ensureShard MKCOLs the archive directory and this record's month, ignoring
// "already exists".
func (h *historyStore) ensureShard(ctx context.Context, timestamp string) error {
	for _, dir := range []string{idpkg.HistoryDir, idpkg.HistoryDir + "/" + idpkg.HistoryShard(timestamp)} {
		req, err := http.NewRequestWithContext(ctx, "MKCOL", davFileURL(h.relayURL, h.identity, dir), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+h.token)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusMethodNotAllowed &&
			resp.StatusCode != http.StatusConflict {
			return fmt.Errorf("create %s: HTTP %d", dir, resp.StatusCode)
		}
	}
	return nil
}

// AppendAll archives a batch, reporting the first failure but attempting all
// of them: one unreadable record is not a reason to lose the rest.
func (h *historyStore) AppendAll(ctx context.Context, records []idpkg.HistoryRecord) error {
	var firstErr error
	for _, record := range records {
		if err := h.Append(ctx, record); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// manifestPaths lists the archive's files through the relay's sync manifest,
// filtered to the history subtree. PROPFIND would work equally well; the
// manifest is one authenticated GET that already returns a sorted list.
func (h *historyStore) manifestPaths(ctx context.Context) ([]string, error) {
	u := strings.TrimSuffix(h.relayURL, "/") + "/sync/" + url.PathEscape(h.identity) +
		"/manifest?paths=" + url.QueryEscape(idpkg.HistoryDir)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("history manifest: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}
	var paths []string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if first { // header line
			first = false
			continue
		}
		var entry struct {
			Path string `json:"path"`
			Dir  bool   `json:"dir"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Dir || !strings.HasSuffix(entry.Path, ".json") {
			continue
		}
		if entry.Path == idpkg.HistoryReadStatePath {
			continue
		}
		paths = append(paths, entry.Path)
	}
	return paths, scanner.Err()
}

// Load reads the whole archive, oldest first. A record that cannot be opened
// is skipped rather than fatal: one corrupt file must not hide the rest of
// someone's history.
func (h *historyStore) Load(ctx context.Context) ([]idpkg.HistoryRecord, error) {
	paths, err := h.manifestPaths(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]idpkg.HistoryRecord, 0, len(paths))
	for _, path := range paths {
		raw, status, err := davGetBytes(ctx, h.relayURL, h.identity, h.token, path)
		if err != nil || status != http.StatusOK {
			continue
		}
		var record idpkg.HistoryRecord
		if err := h.open(raw, &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	idpkg.SortHistory(records)
	return records, nil
}

// ReadState loads the read marks (empty when absent).
func (h *historyStore) ReadState(ctx context.Context) (idpkg.ReadState, error) {
	raw, status, err := davGetBytes(ctx, h.relayURL, h.identity, h.token, idpkg.HistoryReadStatePath)
	if err != nil {
		return idpkg.ReadState{}, err
	}
	if status == http.StatusNotFound {
		return idpkg.ReadState{Version: idpkg.HistoryVersion, Conversations: map[string]idpkg.ReadMark{}}, nil
	}
	if status != http.StatusOK {
		return idpkg.ReadState{}, fmt.Errorf("read-state: HTTP %d", status)
	}
	var state idpkg.ReadState
	if err := h.open(raw, &state); err != nil {
		return idpkg.ReadState{}, err
	}
	if state.Conversations == nil {
		state.Conversations = map[string]idpkg.ReadMark{}
	}
	return state, nil
}

// PutReadState stores the read marks.
func (h *historyStore) PutReadState(ctx context.Context, state idpkg.ReadState) error {
	state.Version = idpkg.HistoryVersion
	if err := state.Validate(); err != nil {
		return err
	}
	body, err := h.seal(state)
	if err != nil {
		return err
	}
	if err := davPutBytes(ctx, h.relayURL, h.identity, h.token, idpkg.HistoryReadStatePath, body); err != nil {
		if mkErr := h.ensureShard(ctx, ""); mkErr != nil {
			return err
		}
		return davPutBytes(ctx, h.relayURL, h.identity, h.token, idpkg.HistoryReadStatePath, body)
	}
	return nil
}

// archiveRecords is the fire-and-forget hook the messaging commands call.
// Archiving is never allowed to fail a send or an inbox read: the message has
// already moved, and refusing to print it because the archive was unreachable
// would lose it twice.
func archiveRecords(useIdentity string, records []idpkg.HistoryRecord, stderr io.Writer) {
	if len(records) == 0 {
		return
	}
	store, err := openHistoryStore(useIdentity)
	if err != nil {
		fmt.Fprintf(stderr, "note: message history not saved (%v)\n", err)
		return
	}
	if err := store.AppendAll(context.Background(), records); err != nil {
		fmt.Fprintf(stderr, "note: message history not saved (%v)\n", err)
	}
}

// runHistory prints the archive, optionally narrowed to one conversation,
// and marks it read. `poweur inbox` shows what just arrived; this shows what
// was ever said.
func runHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "identity")
	jsonOut := fs.Bool("json", false, "output json")
	keepUnread := fs.Bool("keep-unread", false, "do not mark the shown conversations as read")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--keep-unread": true})); err != nil {
		return 1
	}

	store, err := openHistoryStore(*useIdentity)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	records, err := store.Load(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	state, err := store.ReadState(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	peer := strings.ToLower(fs.Arg(0))
	if peer != "" {
		filtered := records[:0:0]
		for _, r := range records {
			if r.Peer(store.identity) == peer {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}

	unread := state.Unread(store.identity, records)
	if *jsonOut {
		return writeOutput(stdout, true, map[string]any{
			"identity": store.identity,
			"messages": records,
			"unread":   unread,
		}, "")
	}

	if len(records) == 0 {
		fmt.Fprintln(stdout, "no message history")
		return 0
	}
	for _, r := range records {
		who := r.Sender
		switch {
		case r.Queue == idpkg.HistoryQueueSent:
			who = "→ " + r.Recipient
		case r.Queue == idpkg.HistoryQueueAnonymous:
			who = "ANONYMOUS"
		}
		fmt.Fprintf(stdout, "[%s] %s: %s\n", r.Timestamp, who, r.Body)
	}
	for p, n := range unread {
		if n > 0 {
			fmt.Fprintf(stdout, "%d unread from %s\n", n, p)
		}
	}

	if *keepUnread {
		return 0
	}
	// Showing someone their messages is what reading them means; the mark
	// moves so the count they see next can be zero.
	next := state
	for _, r := range records {
		if r.Queue == idpkg.HistoryQueueSent || strings.EqualFold(r.Sender, store.identity) {
			continue
		}
		next = next.MarkRead(r.Peer(store.identity), r.Timestamp, r.ID)
	}
	if err := store.PutReadState(ctx, next); err != nil {
		fmt.Fprintln(stderr, "note: read marks not saved:", err)
	}
	return 0
}

// historyRecordFrom builds a record for an inbound message the CLI has just
// decrypted. Body is the plaintext — an archive of ciphertext nobody holds an
// ephemeral key for is not an archive.
func historyRecordFrom(owner, queue, id, sender, recipient, timestamp, msgType, body string) idpkg.HistoryRecord {
	return historyRecordThreaded(owner, queue, id, sender, recipient, timestamp, msgType, "", body)
}

// historyRecordThreaded is the same with the conversation thread carried
// through, so `poweur history` regroups the way the live inbox did.
func historyRecordThreaded(owner, queue, id, sender, recipient, timestamp, msgType, threadID, body string) idpkg.HistoryRecord {
	if recipient == "" {
		recipient = owner
	}
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	return idpkg.HistoryRecord{
		Version: idpkg.HistoryVersion, ID: id, Sender: sender, Recipient: recipient,
		Timestamp: timestamp, Type: msgType, ThreadID: threadID, Queue: queue, Body: body,
	}
}
