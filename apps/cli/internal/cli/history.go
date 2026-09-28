package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	cryptoe2e "github.com/poweur/cli/internal/crypto"
	driveclient "github.com/poweur/cli/internal/drive"
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

// historyStore is one identity's archive: one encrypted append file per
// conversation under `.poweur/private/messages/`, plus a replace file of
// read marks (EPIC-020 E20-T11).
type historyStore struct {
	relayURL string
	identity string
	priv     ed25519.PrivateKey
	// encPriv is the identity's X25519 private key: history is sealed to the
	// owner's own encryption key, so this both seals and opens.
	encPriv  []byte
	encPub   []byte
	files    *driveclient.Files
	private  *driveclient.File
	messages *driveclient.File
}

// openHistoryStore prepares the archive for an identity, or reports why it
// cannot. A missing encryption key is not fatal to anything else the CLI
// does, so callers treat the error as "skip archiving" rather than "fail".
func openHistoryStore(useIdentity string) (*historyStore, error) {
	cfg, identityValue, priv, ok := loadIdentityKey(useIdentity, io.Discard)
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
	return &historyStore{
		relayURL: cfg.RelayURL, identity: identityValue, priv: priv,
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

// Append writes one record. A record already stored under its id is skipped,
// so two devices picking up the same message do not duplicate it.
func (h *historyStore) Append(ctx context.Context, record idpkg.HistoryRecord) error {
	return h.appendRecord(ctx, record)
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

// Load reads the whole archive, oldest first.
func (h *historyStore) Load(ctx context.Context) ([]idpkg.HistoryRecord, error) {
	return h.allRecords(ctx)
}

// ReadState loads the read marks (empty when absent).
func (h *historyStore) ReadState(ctx context.Context) (idpkg.ReadState, error) {
	return h.readMarks(ctx)
}

// PutReadState stores the read marks.
func (h *historyStore) PutReadState(ctx context.Context, state idpkg.ReadState) error {
	return h.writeMarks(ctx, state)
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
	if err := store.AppendAll(context.Background(), records); err != nil && !errors.Is(err, errStorageUnavailable) {
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
	limit := fs.Int("limit", 0, "maximum records, from the newest")
	before := fs.Uint64("before", 0, "only records before this position in one conversation")
	threadID := fs.String("thread", "", "only records in this thread")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--keep-unread": true})); err != nil {
		return 1
	}
	if *limit < 0 {
		fmt.Fprintln(stderr, "limit must be zero or positive")
		return 1
	}

	store, err := openHistoryStore(*useIdentity)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	peer := strings.ToLower(fs.Arg(0))
	var positions map[string]uint64
	var records []idpkg.HistoryRecord
	if *before > 0 {
		if peer == "" {
			fmt.Fprintln(stderr, "history --before needs a conversation")
			return 1
		}
		rows, err := store.conversationRecords(ctx, peer)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		positions = map[string]uint64{}
		for _, row := range rows {
			if row.Position < *before {
				records = append(records, row.Record)
				positions[row.Record.ID] = row.Position
			}
		}
	} else if peer != "" {
		rows, err := store.conversationRecords(ctx, peer)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		positions = map[string]uint64{}
		for _, row := range rows {
			records = append(records, row.Record)
			positions[row.Record.ID] = row.Position
		}
	} else {
		records, err = store.Load(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	state, err := store.ReadState(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if *threadID != "" {
		filtered := records[:0]
		for _, r := range records {
			if r.ThreadID == *threadID {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}
	if *limit > 0 && len(records) > *limit {
		records = records[len(records)-*limit:]
	}

	unread := state.Unread(store.identity, records)
	if *jsonOut {
		var cursor uint64
		for _, record := range records {
			if positions[record.ID] > cursor {
				cursor = positions[record.ID]
			}
		}
		payload := map[string]any{"identity": store.identity, "messages": records, "unread": unread}
		if cursor > 0 {
			payload["cursor"] = cursor
		}
		return writeOutput(stdout, true, payload, "")
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
