package cli

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	"github.com/poweur/cli/internal/session"
	idpkg "github.com/poweur/identity"

	"github.com/poweur/cli/internal/config"
)

// `poweur listen` (EPIC-009 E09-T2): hold the relay's push stream open and
// drain the inbox whenever it says something arrived.
//
// **The socket is notification, the cursor is truth.** Events carry only
// `{type, message_id}` — never a payload — so every one of them is answered
// by the same inbox pickup `poweur inbox` performs. That asymmetry is what
// makes reconnecting free: a dropped connection costs one extra round trip,
// never a message, so the transport needs no delivery guarantees and no
// replay protocol.
//
// This mirrors `streamForever` in `@poweur/client` (`packages/client-ts/src/
// events.ts`) deliberately: same challenge-signed headers, same exponential
// backoff, same reset-on-connect. Go is canonical for the protocol, but here
// the TS client shipped first and the wire behaviour is what has to match.

const (
	// listenBaseBackoff is the first retry delay; it doubles per failure.
	listenBaseBackoff = time.Second
	// listenMaxBackoff caps it. Backoff is politeness toward the relay, not
	// protection for the client — nothing is lost while disconnected.
	listenMaxBackoff = 30 * time.Second
)

// StreamEvent is one server-sent notification. `ready` arrives on connect,
// then `message`, `ack`, `request` and `anon`.
type StreamEvent struct {
	Type      string `json:"type"`
	Identity  string `json:"identity"`
	MessageID string `json:"message_id,omitempty"`
	Timestamp string `json:"timestamp"`
}

// parseSSEFrame decodes one SSE frame — the `data:` lines of it, joined.
// Comment frames (`: keepalive`) and frames whose data is not an event
// object yield ok=false, because there is nothing for a caller to act on.
func parseSSEFrame(frame string) (StreamEvent, bool) {
	var data []string
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	if len(data) == 0 {
		return StreamEvent{}, false
	}
	var event StreamEvent
	if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &event); err != nil {
		return StreamEvent{}, false
	}
	if event.Type == "" {
		return StreamEvent{}, false
	}
	return event, true
}

// readSSE reads frames off r until it ends or ctx is cancelled, handing each
// decoded event to onEvent. A frame ends at a blank line; a partial frame at
// EOF is discarded, since half an event is not an event.
func readSSE(ctx context.Context, r io.Reader, onEvent func(StreamEvent)) error {
	scanner := bufio.NewScanner(r)
	// Notifications are tiny; the cap only has to survive a hostile relay.
	scanner.Buffer(make([]byte, 0, 4096), 64*1024)
	var frame []string
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			frame = append(frame, line)
			continue
		}
		if event, ok := parseSSEFrame(strings.Join(frame, "\n")); ok {
			onEvent(event)
		}
		frame = frame[:0]
	}
	return scanner.Err()
}

// streamEvents opens one stream and reads it to completion. It returns nil
// when the relay closed the stream cleanly, and an error when the stream
// could not be opened — which is the signal streamForever backs off on.
//
// Authenticated with the same challenge-signed headers as the inbox pickup.
// The signature goes in a header rather than the query string on purpose: a
// credential in a URL ends up in every access log between here and the relay.
func streamEvents(ctx context.Context, relayURL, identityValue string,
	sess session.Session, identityPriv ed25519.PrivateKey,
	onOpen func(), onEvent func(StreamEvent)) error {

	challenge, err := FetchChallenge(ctx, relayURL, identityValue)
	if err != nil {
		return err
	}
	signature := ""
	sessionID := ""
	if sessionPriv, perr := sess.PrivateKey(); perr == nil && sess.IsValid() {
		signature = base64.StdEncoding.EncodeToString(ed25519.Sign(sessionPriv, []byte(challenge.Challenge)))
		sessionID = sess.SessionID
	} else if identityPriv != nil {
		// No usable session: the relay accepts the identity key for a local
		// identity, which is also what keeps `listen` working headlessly.
		signature = base64.StdEncoding.EncodeToString(ed25519.Sign(identityPriv, []byte(challenge.Challenge)))
	} else {
		return errors.New("no session or identity key available to authenticate the stream")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(relayURL, "/")+"/events/"+identityValue, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Poweur-Identity", identityValue)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", signature)
	if sessionID != "" {
		req.Header.Set("X-Poweur-Session-Id", sessionID)
	}
	req.Header.Set("Accept", "text/event-stream")
	// Naming the device gives the owner a last_seen in their own
	// devices.json (EPIC-004 E04-T6) — and nowhere else: presence is not
	// user-visible in v1 and no peer can read it.
	applyDeviceHeaders(req.Header)

	// No client timeout: the stream is meant to stay open. Cancellation is
	// the context's job, and the relay sends heartbeats so a dead connection
	// still surfaces as a read error.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return parseErrorResponse("event stream refused", resp)
	}
	if onOpen != nil {
		onOpen()
	}
	return readSSE(ctx, resp.Body, onEvent)
}

// nextBackoff doubles delay, capped at max. Split out because the reconnect
// policy is the part worth testing without a relay.
func nextBackoff(delay, max time.Duration) time.Duration {
	doubled := delay * 2
	if doubled > max || doubled <= 0 {
		return max
	}
	return doubled
}

// streamLoop keeps a stream open until ctx is cancelled, reconnecting with
// exponential backoff. `connect` reports a successful connection by calling
// the onOpen it is handed, which is what resets the backoff — reconnecting
// after an hour of uptime should not start at 30 seconds.
//
// `sleep` is injected so the reconnect policy can be tested in microseconds.
func streamLoop(ctx context.Context, base, max time.Duration,
	connect func(ctx context.Context, onOpen func()) error,
	onError func(error),
	sleep func(ctx context.Context, d time.Duration)) {

	delay := base
	for ctx.Err() == nil {
		// onOpen resets the backoff, so a stream that opened at all — even
		// one that later dropped mid-read — retries from base rather than
		// from wherever the last outage left the delay.
		err := connect(ctx, func() { delay = base })
		if ctx.Err() != nil {
			return
		}
		if err != nil && onError != nil {
			onError(err)
		}
		if ctx.Err() != nil {
			return
		}
		sleep(ctx, delay)
		delay = nextBackoff(delay, max)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// runListen implements `poweur listen`.
func runListen(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	jsonOut := fs.Bool("json", false, "output json")
	once := fs.Bool("once", false, "exit after the first notification (for scripts and tests)")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true, "--once": true})); err != nil {
		return 1
	}
	identityValue := resolveIdentity(*useIdentity, cfg.Identity)
	if identityValue == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	if cfg.RelayURL == "" {
		fmt.Fprintln(stderr, "relay url not configured")
		return 1
	}
	identityPriv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, identityValue))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The drain is exactly what `poweur inbox` does — same decryption, same
	// tick-2 acks, same history archive. A notification must not mean a
	// weaker pickup than a poll, or `listen` would quietly consume messages
	// the journal never records. It reports whether anything arrived, which
	// is what `--once` exits on.
	drain := func() bool {
		sess, err := ensureSession(ctx, cfg.RelayURL, identityValue, identityPriv)
		if err != nil {
			fmt.Fprintln(stderr, "session error:", err)
			return false
		}
		payload, err := fetchInboxWithSessionRetry(ctx, cfg.RelayURL, identityValue, identityPriv, sess)
		if err != nil {
			fmt.Fprintln(stderr, "pickup failed:", err)
			return false
		}
		delivered := renderInboxPayload(payload, inboxRender{
			cfg:          cfg,
			identity:     identityValue,
			useIdentity:  *useIdentity,
			identityPriv: identityPriv,
			session:      sess,
			jsonOut:      *jsonOut,
			// A quiet stream should stay quiet: printing "no messages" on
			// every heartbeat-adjacent event would drown the real output.
			quietWhenEmpty: true,
		}, stdout, stderr)
		return delivered
	}

	fmt.Fprintf(stderr, "listening as %s (ctrl-c to stop)\n", identityValue)
	streamLoop(ctx, listenBaseBackoff, listenMaxBackoff,
		func(ctx context.Context, onOpen func()) error {
			sess, _ := session.Load(identityValue)
			return streamEvents(ctx, cfg.RelayURL, identityValue, sess, identityPriv,
				func() {
					onOpen()
					retryOutboxForIdentity(ctx, identityValue, false, stdout, stderr)
					// Catch up on connect: anything that arrived while the
					// stream was down is already sitting at the cursor. For
					// `--once` that catch-up counts as the notification —
					// otherwise a script would wait for a second message it
					// has no reason to expect.
					if drain() && *once {
						stop()
					}
				},
				func(event StreamEvent) {
					if event.Type == "ready" {
						return // the open handler already drained
					}
					drain()
					if *once {
						stop()
					}
				})
		},
		func(err error) {
			fmt.Fprintf(stderr, "stream dropped, retrying: %v\n", err)
		},
		sleepCtx)
	return 0
}

// inboxRender carries the context an inbox drain needs to print, ack and
// archive what it picked up.
type inboxRender struct {
	cfg          config.Config
	identity     string
	useIdentity  string
	identityPriv ed25519.PrivateKey
	session      session.Session
	jsonOut      bool
	// quietWhenEmpty suppresses the "no messages" line. `poweur inbox` wants
	// it (a one-shot poll with no output looks broken); `poweur listen`
	// does not.
	quietWhenEmpty bool
}

// renderInboxPayload decodes an inbox response and does everything the
// pickup implies: promote accepted contacts, print acks and messages,
// decrypt what it can, emit tick-2 acks for what decrypted, archive to
// history. Shared by `poweur inbox` and `poweur listen` so a push-driven
// pickup is never weaker than a polled one.
//
// Reports whether the pickup carried anything. `poweur inbox` ignores that
// (it exits 0 either way — an empty inbox is not an error); `poweur listen`
// uses it for `--once`.
func renderInboxPayload(payload []byte, r inboxRender, stdout, stderr io.Writer) bool {
	cfg, identityValue, identityPriv, sess := r.cfg, r.identity, r.identityPriv, r.session

	var inbox struct {
		Messages []struct {
			ID         string            `json:"id"`
			Sender     string            `json:"sender"`
			Recipient  string            `json:"recipient"`
			Timestamp  string            `json:"timestamp"`
			Payload    string            `json:"payload"`
			Signature  string            `json:"signature"`
			Type       string            `json:"type,omitempty"`
			ThreadID   string            `json:"thread_id,omitempty"`
			ExpiresAt  string            `json:"expires_at,omitempty"`
			Metadata   map[string]string `json:"metadata,omitempty"`
			SessionID  string            `json:"session_id,omitempty"`
			Encryption *EncryptionMeta   `json:"encryption,omitempty"`
		} `json:"messages"`
		Acks []Ack `json:"acks"`
	}
	if err := json.Unmarshal(payload, &inbox); err != nil {
		fmt.Fprintln(stderr, err)
		return false
	}
	delivered := len(inbox.Messages) > 0 || len(inbox.Acks) > 0

	// --json hands the raw response over untouched: a script asked for the
	// wire shape, not for this function's rendering of it. The acks and
	// history archiving below are the human path only.
	if r.jsonOut {
		fmt.Fprintln(stdout, string(payload))
		return delivered
	}

	// An accept arrives in the requests queue in every inbox mode; older
	// relays under `open` delivered it as ordinary inbox chat, so the inbox
	// drain still finishes a handshake we started. `runRequests` does the
	// same on its side.
	var accepts []string
	for _, msg := range inbox.Messages {
		if msg.Type == idpkg.MsgTypeContactAccept && msg.Sender != "" {
			accepts = append(accepts, msg.Sender)
		}
	}
	promoteAcceptedContacts(context.Background(), r.useIdentity, accepts, stdout, stderr)

	// Surface delivery/read acks for previously-sent messages
	// before printing inbound payloads. The ack stream is independent of
	// the message stream — an empty inbox can still carry acks.
	for _, ack := range inbox.Acks {
		applyInboundAck(identityValue, ack)
		ticks := "✓✓"
		verb := "delivered to"
		if ack.State == AckStateRead {
			ticks, verb = "✓✓✓", "read by"
		}
		fmt.Fprintf(stdout, "%s [%s] %s %s %s (msg %s)\n", ticks, ack.Timestamp, ack.State, verb, ack.Sender, ack.MessageID)
	}

	if !delivered {
		if !r.quietWhenEmpty {
			fmt.Fprintln(stdout, "no messages")
		}
		return false
	}

	encPriv, _ := identity.LoadEncryptionPrivateKey(identity.EncryptionKeyPath(cfg.KeysDir, identityValue))
	readPolicy, readPolicyOK := loadInboxPolicyForReceipts(context.Background(), cfg, identityValue, identityPriv)

	// What the drain hands over exists nowhere else once this call returns,
	// so everything that opens is archived before the function can fail.
	var archive []idpkg.HistoryRecord
	defer func() { archiveRecords(r.useIdentity, archive, stderr) }()

	for _, msg := range inbox.Messages {
		display := msg.Payload
		decrypted := false
		if msg.Encryption != nil && msg.Encryption.Alg != "" {
			if encPriv == nil {
				display = "[encrypted: no local encryption key]"
			} else {
				plaintext, err := cryptoe2e.Decrypt(encPriv, cryptoe2e.EncryptedPayload{
					Ciphertext:         msg.Payload,
					EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
					Nonce:              msg.Encryption.Nonce,
				})
				if err != nil {
					display = fmt.Sprintf("[decrypt failed: %v]", err)
				} else {
					display = string(plaintext)
					decrypted = true
				}
			}
		}
		prefix := "  "
		if decrypted {
			prefix = "🔒"
		}
		// `chat.text` reads as it always has; anything else gets the generic
		// line, because the CLI cannot present an application's payload and
		// showing the raw plaintext would be showing someone else's JSON.
		rendered := describeTypedMessage(msg.Sender, msg.Type, display, decrypted)
		if decrypted && idpkg.NormalizeMessageType(msg.Type) == idpkg.MsgTypeChatAttachment {
			if ref, err := idpkg.ParseAttachmentMetadata(msg.Metadata); err == nil {
				rendered = fmt.Sprintf("attachment %s (%s, %d bytes) — poweur attachment get %s %s --sha256 %s",
					ref.Name, ref.MIME, ref.Size, ref.Owner, ref.Path, ref.SHA256)
			}
		}
		fmt.Fprintf(stdout, "%s [%s] %s: %s%s%s\n", prefix, msg.Timestamp, msg.Sender,
			rendered, threadSuffix(msg.ThreadID), expirySuffix(msg.ExpiresAt))

		if decrypted {
			record := historyRecordThreaded(identityValue, idpkg.HistoryQueueInbox,
				msg.ID, msg.Sender, msg.Recipient, msg.Timestamp, msg.Type, msg.ThreadID, display)
			record.ExpiresAt, record.Metadata = msg.ExpiresAt, msg.Metadata
			archive = append(archive, record)
		}

		// Tick-2 ack: only emit when we actually decrypted the message,
		// i.e. we have proof the inbound message reached the client.
		// Failed decrypts (no key, wrong key, corruption) intentionally
		// stay at tick 1 on the sender's side.
		if decrypted && msg.ID != "" && msg.Sender != "" {
			recipientForAck := msg.Recipient
			if recipientForAck == "" {
				recipientForAck = identityValue
			}
			if err := emitMessageAck(context.Background(), cfg, identityValue, identityPriv, msg.ID, msg.Sender, recipientForAck, AckStateDeliveredClient, sess); err != nil {
				fmt.Fprintf(stderr, "warning: failed to send delivery ack for %s: %v\n", msg.ID, err)
			}
			// The human CLI prints the body, so this pickup is also a read. If
			// policy cannot be loaded, fail closed and disclose nothing.
			if readPolicyOK && readPolicy.SendsReadReceiptsTo(msg.Sender) {
				if err := emitMessageAck(context.Background(), cfg, identityValue, identityPriv, msg.ID, msg.Sender, recipientForAck, AckStateRead, sess); err != nil {
					fmt.Fprintf(stderr, "warning: failed to send read receipt for %s: %v\n", msg.ID, err)
				}
			}
		}
	}
	return true
}

func loadInboxPolicyForReceipts(ctx context.Context, cfg config.Config, identityValue string, priv ed25519.PrivateKey) (idpkg.InboxPolicy, bool) {
	policy := idpkg.InboxPolicy{Version: 1, Mode: idpkg.DefaultInboxMode}
	token, err := MintDAVToken(ctx, cfg.RelayURL, identityValue, identityValue, "dav:read", priv)
	if err != nil {
		return policy, false
	}
	raw, status, err := davGetBytes(ctx, cfg.RelayURL, identityValue, token.Token, inboxPolicyTreePath)
	if err != nil {
		return policy, false
	}
	if status == http.StatusNotFound {
		return policy, true
	}
	if status != http.StatusOK {
		return policy, false
	}
	parsed, err := idpkg.ParseInboxPolicy(raw)
	if err != nil {
		return policy, false
	}
	return parsed, true
}
