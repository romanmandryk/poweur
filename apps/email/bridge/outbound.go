package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/poweur/identity"
)

// Outbound turns `email.send` messages in the bridge's inbox into mail.
//
// Pickup is at-least-once: envelopes are copied into the local queue before
// the relay is told to forget them, and every send carries the client's
// send_id, so a crash anywhere repeats work without repeating mail.
type Outbound struct {
	cfg       Config
	relay     *Relay
	store     *Store
	dir       *Directory
	blobs     *Blobs
	transport Transport
	encPriv   []byte
	log       *slog.Logger
	metrics   *Metrics
	now       func() time.Time

	wake chan struct{}
	mu   sync.Mutex
}

// retry schedule for temporary submission failures; after the last one the
// send is refused.
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Wake asks for a pickup now (an event arrived).
func (o *Outbound) Wake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Run picks up and processes until ctx ends: on every event and at least
// every interval.
func (o *Outbound) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := o.Pickup(ctx); err != nil && ctx.Err() == nil {
			o.log.Warn("outbound: pickup failed", "err", err)
		}
		o.ProcessDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-o.wake:
		}
	}
}

// Pickup copies the bridge's inbox into the local queue, then consumes it.
func (o *Outbound) Pickup(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	cursor, err := o.store.Get(ctx, "inbox_cursor")
	if err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		envs, next, err := o.relay.Pickup(ctx, cursor)
		if err != nil {
			return err
		}
		for _, env := range envs {
			raw, _ := json.Marshal(env)
			if err := o.store.Enqueue(ctx, env.Sender+"\n"+env.ID, string(raw)); err != nil {
				return err
			}
		}
		if next != "" && next != cursor {
			if err := o.relay.Consume(ctx, next); err != nil {
				return err
			}
			if err := o.store.Set(ctx, "inbox_cursor", next); err != nil {
				return err
			}
			cursor = next
		}
		if len(envs) == 0 {
			return nil
		}
	}
	return nil
}

// ProcessDue handles every queued envelope whose time has come.
func (o *Outbound) ProcessDue(ctx context.Context) {
	items, err := o.store.Due(ctx, 50)
	if err != nil {
		o.log.Warn("outbound: queue read failed", "err", err)
		return
	}
	for _, it := range items {
		var env Envelope
		if err := json.Unmarshal([]byte(it.Envelope), &env); err != nil {
			_ = o.store.Dequeue(ctx, it.ID)
			continue
		}
		retry, err := o.handle(ctx, env)
		switch {
		case retry && it.Attempts < len(retryDelays):
			_ = o.store.Retry(ctx, it.ID, retryDelays[it.Attempts])
			o.metrics.Inc("email_outbound_total", "state", identity.EmailStateDeferred)
		default:
			if retry {
				// Out of attempts: say so, once.
				o.giveUp(ctx, env, err)
			}
			_ = o.store.Dequeue(ctx, it.ID)
		}
	}
}

// handle processes one envelope. retry=true asks to be called again later.
func (o *Outbound) handle(ctx context.Context, env Envelope) (retry bool, err error) {
	if identity.NormalizeMessageType(env.Type) != identity.MsgTypeEmailSend {
		return false, nil // the bridge only takes sends; anything else is dropped
	}
	sender := strings.ToLower(env.Sender)
	plain, err := o.relay.Open(env)
	if err != nil {
		return false, nil
	}
	var send identity.EmailSend
	if err := json.Unmarshal(plain, &send); err != nil {
		return false, nil
	}
	if err := send.Validate(); err != nil {
		// Tell the sender why, when there is a send id to tell them about.
		if send.SendID != "" && len(send.SendID) <= identity.MaxSendIDLen && identity.ValidateThreadID(send.SendID) == nil {
			o.status(ctx, sender, send.SendID, identity.EmailStateRefused, "", "", err.Error())
		}
		return false, nil
	}

	record, fresh, err := o.store.ClaimSend(ctx, sender, send.SendID)
	if err != nil {
		return true, err
	}
	if !fresh && record.State != identity.EmailStateAccepted && record.State != identity.EmailStateDeferred {
		// A repeat of a send already finished: answer with what happened.
		o.status(ctx, sender, send.SendID, record.State, record.MessageID, "", "")
		return false, nil
	}

	sendFrom, err := o.dir.Opted(ctx, sender)
	if err != nil {
		if errors.Is(err, ErrNoMailbox) {
			o.refuse(ctx, sender, send.SendID, "email is not enabled for "+sender+" through this bridge")
			return false, nil
		}
		return true, err
	}
	if reason := o.policy(ctx, sender, send); reason != "" {
		o.refuse(ctx, sender, send.SendID, reason)
		return false, nil
	}

	attachments, reason := o.resolveAttachments(ctx, sender, send.Attachments)
	if reason != "" {
		o.refuse(ctx, sender, send.SendID, reason)
		return false, nil
	}

	messageID := record.MessageID
	if messageID == "" {
		messageID = NewMessageID(o.cfg.MessageIDDomain)
		_ = o.store.UpdateSend(ctx, sender, send.SendID, identity.EmailStateAccepted, messageID)
	}
	out := Outgoing{
		From:        identity.EmailAddress{Name: send.FromName, Address: sendFrom.OptIn.Address},
		To:          send.To,
		Cc:          send.Cc,
		Subject:     send.Subject,
		Text:        send.Text,
		InReplyTo:   send.InReplyTo,
		References:  send.References,
		MessageID:   messageID,
		Date:        o.now(),
		Attachments: attachments,
	}
	if send.HTML != "" {
		// The composer's HTML is re-sanitized: the bridge does not send
		// scripts or tracking pixels in anyone's name.
		out.HTML = SanitizeHTML(send.HTML)
	}
	recipients := allRecipients(send)
	if footer := o.footerFor(ctx, sender, recipients); footer != "" {
		out.FooterText = footer
		if out.HTML != "" {
			out.FooterHTML = textToHTML(footer)
		}
	}
	msg, err := BuildMIME(out)
	if err != nil {
		o.refuse(ctx, sender, send.SendID, "could not build the message")
		return false, nil
	}

	if !fresh && record.State == identity.EmailStateDeferred {
		o.metrics.Inc("email_outbound_retries_total", "", "")
	}
	sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	err = o.transport.Send(sctx, sendFrom.OptIn.Address, recipients, msg)
	cancel()
	if err != nil {
		var se *SubmitError
		if errors.As(err, &se) && se.Permanent {
			o.refuse(ctx, sender, send.SendID, "the mail server refused it: "+se.Message)
			return false, nil
		}
		_ = o.store.UpdateSend(ctx, sender, send.SendID, identity.EmailStateDeferred, "")
		o.status(ctx, sender, send.SendID, identity.EmailStateDeferred, messageID, "", "the mail server is unavailable; retrying")
		return true, err
	}

	_ = o.store.UpdateSend(ctx, sender, send.SendID, identity.EmailStateSent, messageID)
	_, _ = o.store.Count(ctx, "out:"+sender, 1)
	newOnes := 0
	for _, r := range recipients {
		if known, _ := o.store.Known(ctx, sender, r); !known {
			newOnes++
		}
		_ = o.store.Remember(ctx, sender, r)
	}
	if newOnes > 0 {
		_, _ = o.store.Count(ctx, "new:"+sender, newOnes)
	}
	for _, a := range send.Attachments {
		if a.Upload != "" {
			o.blobs.DropUpload(ctx, a.Upload)
		}
	}
	o.metrics.Inc("email_outbound_total", "state", identity.EmailStateSent)
	o.status(ctx, sender, send.SendID, identity.EmailStateSent, messageID, "", "")
	return false, nil
}

func allRecipients(send identity.EmailSend) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]identity.EmailAddress{send.To, send.Cc, send.Bcc} {
		for _, a := range list {
			addr := strings.ToLower(a.Address)
			if !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}
	return out
}

// policy applies the outbound abuse controls (EPIC-023 E23-T4): daily
// message cap, new-recipient cap (replies to someone who wrote first are not
// new), an optional waiting period after opting in, and the suppression list.
func (o *Outbound) policy(ctx context.Context, sender string, send identity.EmailSend) string {
	if o.cfg.OutMinIdentityAge > 0 {
		first, _ := o.store.Get(ctx, "first_send_seen:"+sender)
		if first == "" {
			first = strconv.FormatInt(o.now().Unix(), 10)
			_ = o.store.Set(ctx, "first_send_seen:"+sender, first)
		}
		at, _ := strconv.ParseInt(first, 10, 64)
		if wait := time.Unix(at, 0).Add(o.cfg.OutMinIdentityAge).Sub(o.now()); wait > 0 {
			return fmt.Sprintf("new addresses can send email after %s; try again in %s", o.cfg.OutMinIdentityAge, wait.Round(time.Minute))
		}
	}
	if o.cfg.OutDailyLimit > 0 {
		if n, _ := o.store.Peek(ctx, "out:"+sender); n >= o.cfg.OutDailyLimit {
			return fmt.Sprintf("daily sending limit reached (%d messages)", o.cfg.OutDailyLimit)
		}
	}
	recipients := allRecipients(send)
	newOnes := 0
	for _, r := range recipients {
		if reason, suppressed, _ := o.store.Suppressed(ctx, r); suppressed {
			return fmt.Sprintf("%s cannot receive email from this bridge (%s)", r, reason)
		}
		if known, _ := o.store.Known(ctx, sender, r); !known {
			newOnes++
		}
	}
	if o.cfg.OutNewRecipients > 0 && newOnes > 0 {
		if n, _ := o.store.Peek(ctx, "new:"+sender); n+newOnes > o.cfg.OutNewRecipients {
			return fmt.Sprintf("daily limit for writing to new addresses reached (%d)", o.cfg.OutNewRecipients)
		}
	}
	return ""
}

func (o *Outbound) resolveAttachments(ctx context.Context, sender string, list []identity.EmailOutAttachment) ([]OutAttachment, string) {
	var out []OutAttachment
	var total int64
	for _, a := range list {
		var data []byte
		var err error
		if a.Data != "" {
			data, err = base64.StdEncoding.DecodeString(a.Data)
			if err != nil {
				return nil, "attachment " + a.Name + " is not valid base64"
			}
		} else {
			data, err = o.blobs.TakeUpload(ctx, sender, a.Upload, o.encPriv)
			if err != nil {
				return nil, "attachment " + a.Name + ": " + err.Error()
			}
			if a.SHA256 != "" {
				sum := sha256.Sum256(data)
				if !strings.EqualFold(hex.EncodeToString(sum[:]), a.SHA256) {
					return nil, "attachment " + a.Name + " does not match its hash"
				}
			}
		}
		total += int64(len(data))
		if total > identity.MaxEmailOutboundBytes {
			return nil, fmt.Sprintf("message too large (max %d MB)", identity.MaxEmailOutboundBytes>>20)
		}
		out = append(out, OutAttachment{Name: a.Name, MIME: a.MIME, Data: data})
	}
	return out, ""
}

// footerFor returns the growth footer when the operator wants one here:
// always, or the first time this sender writes to any of these recipients.
func (o *Outbound) footerFor(ctx context.Context, sender string, recipients []string) string {
	switch o.cfg.Footer {
	case "always":
	case "first":
		first := false
		for _, r := range recipients {
			if already, err := o.store.FooterSent(ctx, sender, r); err == nil && !already {
				first = true
			}
		}
		if !first {
			return ""
		}
	default:
		return ""
	}
	return RenderFooter(o.cfg.FooterText, sender, o.cfg.SignupURL)
}

func (o *Outbound) refuse(ctx context.Context, sender, sendID, reason string) {
	_ = o.store.UpdateSend(ctx, sender, sendID, identity.EmailStateRefused, "")
	o.metrics.Inc("email_outbound_total", "state", identity.EmailStateRefused)
	o.status(ctx, sender, sendID, identity.EmailStateRefused, "", "", reason)
}

func (o *Outbound) giveUp(ctx context.Context, env Envelope, err error) {
	plain, oerr := o.relay.Open(env)
	if oerr != nil {
		return
	}
	var send identity.EmailSend
	if json.Unmarshal(plain, &send) != nil || send.SendID == "" {
		return
	}
	reason := "could not reach the mail server"
	if err != nil {
		o.log.Warn("outbound: giving up", "err", err)
	}
	o.refuse(ctx, strings.ToLower(env.Sender), send.SendID, reason)
}

// status tells the sender what happened, best effort.
func (o *Outbound) status(ctx context.Context, sender, sendID, state, messageID, recipient, reason string) {
	st := identity.EmailStatus{
		Version: identity.EmailPayloadVersion, SendID: sendID, State: state, MessageID: messageID,
		Recipient: recipient, Reason: reason, At: o.now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(st)
	if _, err := o.relay.Send(ctx, sender, identity.MsgTypeEmailStatus, raw, ""); err != nil {
		o.log.Info("outbound: status not delivered", "state", state, "err", err)
	}
}

// Bounced handles a delivery report the inbound path matched to one of our
// sends: a status for the sender, and suppression for a hard failure.
func (o *Outbound) Bounced(ctx context.Context, send SendRecord, b identity.EmailBounce) {
	if b.Action != "failed" {
		return
	}
	if strings.HasPrefix(b.Status, "5") && b.Recipient != "" {
		_ = o.store.Suppress(ctx, b.Recipient, "bounced "+b.Status)
	}
	_ = o.store.UpdateSend(ctx, send.Sender, send.SendID, identity.EmailStateBounced, "")
	o.metrics.Inc("email_outbound_total", "state", identity.EmailStateBounced)
	reason := b.Diagnostic
	if reason == "" {
		reason = "delivery failed " + b.Status
	}
	o.status(ctx, send.Sender, send.SendID, identity.EmailStateBounced, send.MessageID, b.Recipient, reason)
}
