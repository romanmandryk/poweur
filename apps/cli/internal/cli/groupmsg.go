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
	"sort"
	"strings"
	"time"

	"github.com/poweur/cli/internal/config"
	cryptoe2e "github.com/poweur/cli/internal/crypto"
	"github.com/poweur/cli/internal/identity"
	idpkg "github.com/poweur/identity"
)

// Group messaging in the CLI (EPIC-009 E09-T5).
//
// `poweur group send` is the whole of v1's send path: read the group's
// roster from the relay that hosts it, resolve each member's published
// encryption key, seal the plaintext once per member, and post all of those
// envelopes as one batch. The relay expands nothing the client did not
// already address — it checks that the batch covers the membership and
// delivers.
//
// Nothing here is new cryptography. Each envelope is built and signed by
// exactly the same code path a direct `poweur send` uses; the only thing
// that differs is that there are N of them and that two reserved metadata
// keys say which group and which membership epoch they belong to.

// GroupFanoutRequest is the body of POST /groups/{group}/messages.
type GroupFanoutRequest struct {
	Group     string    `json:"group"`
	Epoch     int       `json:"epoch"`
	Envelopes []Message `json:"envelopes"`
}

// GroupDelivery is one member's outcome.
type GroupDelivery struct {
	Recipient string `json:"recipient"`
	ID        string `json:"id"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
}

// GroupFanoutResponse is the per-member breakdown the relay returns.
//
// There is no single verdict, on purpose: a fan-out that reached three
// members and missed one is neither a success nor a failure, and the ticks
// are per member. "3 / 4 delivered" is this client's rollup of them.
type GroupFanoutResponse struct {
	Group     string          `json:"group"`
	Epoch     int             `json:"epoch"`
	Delivered []GroupDelivery `json:"delivered"`
	Failed    []GroupDelivery `json:"failed"`
}

// fetchGroupRoster reads the signed membership document from the relay
// hosting the group, and verifies it against the group's own key.
//
// Verifying here rather than trusting the response is the point: the roster
// decides who a message is encrypted for, so a relay that could edit it
// could add a reader. The group's public key comes from resolving the group
// as the identity it is.
func fetchGroupRoster(ctx context.Context, relayURL, group, actor string, actorPriv ed25519.PrivateKey) (idpkg.ShareGroup, error) {
	group = strings.ToLower(strings.TrimSpace(group))
	challenge, err := FetchChallenge(ctx, relayURL, actor)
	if err != nil {
		return idpkg.ShareGroup{}, err
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(actorPriv, []byte(challenge.Challenge)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL+"/groups/"+group, nil)
	if err != nil {
		return idpkg.ShareGroup{}, err
	}
	req.Header.Set("X-Poweur-Identity", actor)
	req.Header.Set("X-Poweur-Challenge", challenge.Challenge)
	req.Header.Set("X-Poweur-Signature", signature)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return idpkg.ShareGroup{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// The relay answers a stranger exactly as it answers "no such
		// group", so this message has to cover both without guessing.
		return idpkg.ShareGroup{}, fmt.Errorf("%s is not a group you can read here (not a group identity on this relay, or you are not in it)", group)
	}
	if resp.StatusCode != http.StatusOK {
		return idpkg.ShareGroup{}, parseErrorResponse("group roster request failed", resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return idpkg.ShareGroup{}, err
	}
	roster, err := idpkg.ParseShareGroup(raw)
	if err != nil {
		return idpkg.ShareGroup{}, err
	}
	if !roster.IsGroupIdentity() || !strings.EqualFold(roster.Group, group) {
		return idpkg.ShareGroup{}, fmt.Errorf("relay returned a document that does not name %s", group)
	}
	resolved, err := identity.ResolveIdentity(ctx, group)
	if err != nil {
		return idpkg.ShareGroup{}, fmt.Errorf("cannot resolve the group's own key: %w", err)
	}
	groupPub, err := idpkg.ParseEd25519PublicKey(resolved.Document.PublicKey)
	if err != nil {
		return idpkg.ShareGroup{}, fmt.Errorf("group %s published an invalid signing key: %w", group, err)
	}
	if err := roster.VerifySignature(groupPub); err != nil {
		return idpkg.ShareGroup{}, fmt.Errorf("group membership document does not verify against %s's key: %w", group, err)
	}
	return roster, nil
}

func runGroupSend(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("group send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	msgType := fs.String("type", "", "envelope message type (default chat.text)")
	thread := fs.String("thread", "", "sub-thread inside the group (default: the group's main thread)")
	expiresAt := fs.String("expires", "", "RFC3339 timestamp after which this message stops being meaningful")
	meta := &metaFlag{}
	fs.Var(meta, "meta", "envelope metadata as key=value (repeatable; plaintext — addressing, not content)")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: poweur group send <group-id> <message> [--thread <name>]")
		return 1
	}
	group := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	plaintext := fs.Arg(1)
	if !idpkg.IsGroupIdentityName(group) {
		fmt.Fprintln(stderr, "a group identity needs a full Poweur ID (e.g. team.acme.poweur.net)")
		return 1
	}
	// The reserved keys are the client's to set, not the caller's: letting
	// `--meta group=...` through would let a typo address a fan-out at a
	// group the batch was not built for.
	if err := idpkg.ReservedGroupMetadataError(meta.Map()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := validateOutgoingEnvelope(*msgType, "", *expiresAt, meta.Map()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	sender := resolveIdentity(*useIdentity, cfg.Identity)
	if sender == "" || cfg.KeysDir == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	senderPriv, err := identity.LoadPrivateKey(identity.KeyPath(cfg.KeysDir, sender))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// A group can only be messaged through the relay that hosts it: that
	// relay is the only party that can read the membership document, so it
	// is the only one that can expand a fan-out.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resolvedGroup, err := identity.ResolveIdentity(ctx, group)
	if err != nil {
		fmt.Fprintf(stderr, "cannot resolve the relay hosting %s: %v\n", group, err)
		return 1
	}
	relayURL := strings.TrimRight(strings.TrimSpace(resolvedGroup.Document.Relay), "/")
	if relayURL == "" {
		fmt.Fprintf(stderr, "cannot resolve the relay hosting %s: identity document has no relay\n", group)
		return 1
	}
	if !strings.HasPrefix(relayURL, "http://") && !strings.HasPrefix(relayURL, "https://") {
		relayURL = schemeFromConfig(cfg) + "://" + relayURL
	}

	roster, err := fetchGroupRoster(ctx, relayURL, group, sender, senderPriv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !roster.HasMember(sender) {
		// Being an admin is authority over the roster, not a seat in the
		// conversation. Saying so here is kinder than a 403 from the relay.
		fmt.Fprintf(stderr, "%s is not a member of %s, so cannot send to it\n", sender, group)
		return 1
	}
	recipients := idpkg.GroupFanoutRecipients(roster, sender)
	if len(recipients) == 0 {
		fmt.Fprintf(stderr, "%s has no other members to deliver to\n", group)
		return 1
	}
	if len(roster.Members) > idpkg.MaxGroupFanoutMembers {
		fmt.Fprintf(stderr, "%s has %d members; v1 fan-out is limited to %d\n",
			group, len(roster.Members), idpkg.MaxGroupFanoutMembers)
		return 1
	}

	threadID := idpkg.GroupThreadID(group, *thread)
	if err := idpkg.ValidateGroupThreadID(group, threadID); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	metadata := idpkg.GroupMessageMetadata(group, roster.Epoch, meta.Map())
	timestamp := time.Now().UTC().Format(time.RFC3339)

	// One envelope per member, each sealed to that member's own published
	// encryption key. A member without one is refused *before* anything is
	// sent: sending to the rest and silently dropping them would make a
	// group conversation quietly incomplete for everyone in it.
	batch := GroupFanoutRequest{Group: group, Epoch: roster.Epoch}
	for _, member := range recipients {
		encPub, err := identity.LookupEncryptionKey(ctx, member)
		if err != nil {
			fmt.Fprintf(stderr, "cannot look up encryption key for group member %s: %v\n", member, err)
			return 1
		}
		if len(encPub) != 32 {
			fmt.Fprintf(stderr, "group member %s has no published encryption key; refusing to send in plaintext.\n"+
				"Ask them to run `poweur identity add-encryption-key %s`.\n", member, member)
			return 1
		}
		sealed, err := cryptoe2e.Encrypt(encPub, []byte(plaintext))
		if err != nil {
			fmt.Fprintf(stderr, "encrypt for %s: %v\n", member, err)
			return 1
		}
		msg := Message{
			ID:        identity.NewMessageID(),
			Sender:    sender,
			Recipient: member,
			Timestamp: timestamp,
			Payload:   sealed.Ciphertext,
			Type:      *msgType,
			ThreadID:  threadID,
			ExpiresAt: *expiresAt,
			Metadata:  metadata,
			Encryption: &EncryptionMeta{
				Alg:                cryptoe2e.AlgName,
				EphemeralPublicKey: sealed.EphemeralPublicKey,
				Nonce:              sealed.Nonce,
			},
		}
		msg.Signature = signMessage(senderPriv, msg)
		batch.Envelopes = append(batch.Envelopes, msg)
	}

	out, err := postGroupFanout(ctx, relayURL, group, batch)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// One history record for the whole fan-out, with the group as the peer.
	// The relay never hands a sender their own message back, so this is the
	// only record of what was said — and what was said is one message, not
	// one per member.
	archiveRecords(*useIdentity, []idpkg.HistoryRecord{historyRecordThreaded(
		sender, idpkg.HistoryQueueSent, batch.Envelopes[0].ID, sender,
		group, timestamp, *msgType, threadID, plaintext)}, stderr)

	summary := fmt.Sprintf("sent to %s (epoch %d, thread %s): %d of %d delivered\n",
		group, out.Epoch, threadID, len(out.Delivered), len(batch.Envelopes))
	for _, f := range out.Failed {
		summary += fmt.Sprintf("  ✗ %s: %s %s\n", f.Recipient, f.Status, f.Detail)
	}
	return writeOutput(stdout, *jsonOut, out, summary)
}

// postGroupFanout posts one batch and decodes the per-member breakdown.
func postGroupFanout(ctx context.Context, relayURL, group string, batch GroupFanoutRequest) (GroupFanoutResponse, error) {
	raw, err := json.Marshal(batch)
	if err != nil {
		return GroupFanoutResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL+"/groups/"+group+"/messages", bytes.NewReader(raw))
	if err != nil {
		return GroupFanoutResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return GroupFanoutResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		// The membership moved between reading the roster and posting. The
		// batch is unusable — every envelope was sealed to a stale member
		// set — so the honest answer is "run it again", not a silent retry
		// that sends the message twice if the first one half-landed.
		return GroupFanoutResponse{}, fmt.Errorf(
			"%s's membership changed while this message was being encrypted; run the command again", group)
	}
	if resp.StatusCode >= 400 {
		return GroupFanoutResponse{}, parseErrorResponse("group send failed", resp)
	}
	var out GroupFanoutResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return GroupFanoutResponse{}, err
	}
	return out, nil
}

// runGroupInbox prints one group's conversation out of the local archive.
//
// It reads history rather than the live inbox because a group conversation
// is assembled from both directions — everyone else's messages arrive in the
// inbox, and the owner's own only ever exist in their archive.
func runGroupInbox(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("group inbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useIdentity := fs.String("use-identity", "", "override identity for this command")
	thread := fs.String("thread", "", "show only one sub-thread")
	jsonOut := fs.Bool("json", false, "output json")
	if err := fs.Parse(normalizeArgs(args, map[string]bool{"--json": true})); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: poweur group inbox <group-id> [--thread <name>]")
		return 1
	}
	group := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	owner := resolveIdentity(*useIdentity, cfg.Identity)
	if owner == "" {
		fmt.Fprintln(stderr, "identity not configured")
		return 1
	}
	store, err := openHistoryStore(*useIdentity)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	records, err := store.Load(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	want := ""
	if strings.TrimSpace(*thread) != "" {
		want = idpkg.GroupThreadID(group, *thread)
	}
	matching := filterGroupHistory(records, group, want)
	idpkg.SortHistory(matching)
	if *jsonOut {
		return writeOutput(stdout, true, matching, "")
	}
	if len(matching) == 0 {
		fmt.Fprintf(stdout, "no messages in %s yet\n", group)
		return 0
	}
	for _, r := range matching {
		who := r.Sender
		if strings.EqualFold(who, owner) {
			who = "you"
		}
		fmt.Fprintf(stdout, "[%s] %s: %s%s\n", r.Timestamp, who,
			describeTypedMessage(r.Sender, r.Type, r.Body, true), groupThreadSuffix(group, r.ThreadID))
	}
	return 0
}

// filterGroupHistory selects the archived records belonging to one group.
//
// The thread is the filter, not the peer: an inbound group message is
// archived under the member who sent it (that is who signed it), while the
// owner's own copy is archived under the group. Only `thread_id` is the same
// on both sides — which is exactly the property the group thread rule was
// designed to give.
func filterGroupHistory(records []idpkg.HistoryRecord, group, thread string) []idpkg.HistoryRecord {
	out := make([]idpkg.HistoryRecord, 0, len(records))
	for _, r := range records {
		if idpkg.ValidateGroupThreadID(group, r.ThreadID) != nil {
			continue
		}
		if thread != "" && !strings.EqualFold(r.ThreadID, thread) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// groupThreadSuffix renders the sub-thread marker, and nothing at all for
// the group's main thread — labelling every line with the group's own name
// would be noise in a view that is already one group.
func groupThreadSuffix(group, threadID string) string {
	if threadID == "" || strings.EqualFold(threadID, group) {
		return ""
	}
	suffix := threadID
	if len(threadID) > len(group)+1 {
		suffix = threadID[len(group)+1:]
	}
	return " [" + suffix + "]"
}

// groupMemberSummary renders a roster for a human, members first because
// members is the list that decides who receives a message.
func groupMemberSummary(roster idpkg.ShareGroup) string {
	members := append([]string(nil), roster.Members...)
	sort.Strings(members)
	return fmt.Sprintf("%s (epoch %d)\n  members: %s\n  admins:  %s\n",
		roster.Group, roster.Epoch,
		strings.Join(members, ", "), strings.Join(roster.Admins, ", "))
}
