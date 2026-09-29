package relay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/poweur/api/internal/crypto"
	idpkg "github.com/poweur/identity"
)

// Group messaging at the relay (EPIC-009 E09-T5): roster reads, fan-out
// authorization, batch coverage, epoch preconditions and the direct-send
// guard.

// groupFixture: a group identity hosted here with three members and one
// admin who is deliberately *not* a member.
type groupFixture struct {
	server *Server
	ts     *httptest.Server
	group  hostedID
	alice  hostedID
	bob    hostedID
	carol  hostedID
	// dana administers the group without being in it: `admins` is an
	// authority list, `members` is the access list, and they are
	// independent.
	dana      hostedID
	stranger  hostedID
	groupName string
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()
	server, ts := newTestRelay(t)
	fx := &groupFixture{
		server:    server,
		ts:        ts,
		group:     registerTestIdentity(t, server, ts, "crew.poweur.net"),
		alice:     registerTestIdentity(t, server, ts, "galice.poweur.net"),
		bob:       registerTestIdentity(t, server, ts, "gbob.poweur.net"),
		carol:     registerTestIdentity(t, server, ts, "gcarol.poweur.net"),
		dana:      registerTestIdentity(t, server, ts, "gdana.poweur.net"),
		stranger:  registerTestIdentity(t, server, ts, "gzed.poweur.net"),
		groupName: "crew.poweur.net",
	}
	fx.setMembership(t, 1,
		[]string{fx.dana.name},
		[]string{fx.alice.name, fx.bob.name, fx.carol.name})
	return fx
}

// setMembership writes the group's own roster, signed by the group key.
func (fx *groupFixture) setMembership(t *testing.T, epoch int, admins, members []string) {
	t.Helper()
	gr := idpkg.ShareGroup{
		Group:     fx.groupName,
		Owner:     fx.groupName,
		Members:   members,
		Admins:    admins,
		Epoch:     epoch,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := gr.Sign(fx.group.priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(gr)
	setSysFile(t, fx.server, fx.groupName, groupRosterPath, string(raw))
}

// challengeFor fetches and signs a one-shot challenge for id.
func challengeFor(t *testing.T, ts *httptest.Server, id hostedID) (string, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + "/auth/challenge?identity=" + id.name)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cr ChallengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(id.priv, []byte(cr.Challenge)))
	return cr.Challenge, sig
}

// readRoster performs GET /groups/{group} as id.
func (fx *groupFixture) readRoster(t *testing.T, id hostedID) (*http.Response, idpkg.ShareGroup) {
	t.Helper()
	challenge, sig := challengeFor(t, fx.ts, id)
	req, _ := http.NewRequest(http.MethodGet, fx.ts.URL+"/groups/"+fx.groupName, nil)
	req.Header.Set("X-Poweur-Identity", id.name)
	req.Header.Set("X-Poweur-Challenge", challenge)
	req.Header.Set("X-Poweur-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var gr idpkg.ShareGroup
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&gr)
	}
	return resp, gr
}

// groupEnvelope builds one signed member envelope for a fan-out.
func groupEnvelope(t *testing.T, sender hostedID, recipient, group string, epoch int, threadID, timestamp, body string) Message {
	t.Helper()
	msg := Message{
		ID:        "msg_" + recipient + "_" + strings.ReplaceAll(timestamp, ":", ""),
		Sender:    sender.name,
		Recipient: recipient,
		Timestamp: timestamp,
		Payload:   base64.RawURLEncoding.EncodeToString([]byte(body + "|" + recipient)),
		ThreadID:  threadID,
		Metadata:  idpkg.GroupMessageMetadata(group, epoch, nil),
		Encryption: &EncryptionMeta{
			Alg:                "x25519-hkdf-chacha20poly1305",
			EphemeralPublicKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
			Nonce:              base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 12)),
		},
	}
	encMeta := &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	}
	canonical := crypto.CanonicalMessageEnvelope(
		msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload,
		msg.ID, msg.SessionID, msg.Type, msg.ThreadID, msg.ExpiresAt, msg.Metadata, encMeta)
	msg.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(sender.priv, []byte(canonical)))
	return msg
}

// fanout builds a full batch from sender to every other member.
func (fx *groupFixture) fanout(t *testing.T, sender hostedID, epoch int, threadID, body string, recipients []string) GroupFanoutRequest {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339)
	req := GroupFanoutRequest{Group: fx.groupName, Epoch: epoch}
	for _, r := range recipients {
		req.Envelopes = append(req.Envelopes, groupEnvelope(t, sender, r, fx.groupName, epoch, threadID, ts, body))
	}
	return req
}

func (fx *groupFixture) post(t *testing.T, req GroupFanoutRequest) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(req)
	resp, err := http.Post(fx.ts.URL+"/groups/"+fx.groupName+"/messages", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

// inboxOf drains an identity's spool through the relay's own store.
func (fx *groupFixture) inboxOf(t *testing.T, id hostedID) []string {
	t.Helper()
	var out []string
	for _, m := range fx.server.inbox.Drain(id.name) {
		out = append(out, m.Sender+"|"+m.ThreadID+"|"+m.Metadata[idpkg.MetaGroup])
	}
	return out
}

// The happy path: a member fans out and every other member is spooled. The
// sender gets no copy of their own message — the relay never hands one back.
func TestGroupFanoutDeliversToEveryMember(t *testing.T) {
	fx := newGroupFixture(t)
	req := fx.fanout(t, fx.alice, 1, fx.groupName, "hello crew",
		[]string{fx.bob.name, fx.carol.name})
	resp, body := fx.post(t, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("fan-out: %d %s", resp.StatusCode, body)
	}
	var out GroupFanoutResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Delivered) != 2 || len(out.Failed) != 0 {
		t.Fatalf("delivered=%v failed=%v", out.Delivered, out.Failed)
	}
	if out.Epoch != 1 {
		t.Fatalf("epoch = %d", out.Epoch)
	}
	for _, member := range []hostedID{fx.bob, fx.carol} {
		got := fx.inboxOf(t, member)
		if len(got) != 1 {
			t.Fatalf("%s inbox = %v", member.name, got)
		}
		want := fx.alice.name + "|" + fx.groupName + "|" + fx.groupName
		if got[0] != want {
			t.Fatalf("%s got %q, want %q", member.name, got[0], want)
		}
	}
	if got := fx.inboxOf(t, fx.alice); len(got) != 0 {
		t.Fatalf("the sender must not receive their own fan-out: %v", got)
	}
	// An admin who is not a member gets nothing: `admins` is authority over
	// the roster, not access to the conversation.
	if got := fx.inboxOf(t, fx.dana); len(got) != 0 {
		t.Fatalf("a non-member admin must not receive the fan-out: %v", got)
	}
}

func TestGroupFanoutRejections(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest)
		wantStatus int
		wantError  string
	}{
		{
			name: "sender is not a member",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				*req = fx.fanout(t, fx.stranger, 1, fx.groupName, "let me in",
					[]string{fx.alice.name, fx.bob.name, fx.carol.name})
			},
			wantStatus: http.StatusForbidden,
			wantError:  "not_a_member",
		},
		{
			name: "an admin who is not a member may not send",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				*req = fx.fanout(t, fx.dana, 1, fx.groupName, "from the ops account",
					[]string{fx.alice.name, fx.bob.name, fx.carol.name})
			},
			wantStatus: http.StatusForbidden,
			wantError:  "not_a_member",
		},
		{
			name: "stale epoch",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				fx.setMembership(t, 2, []string{fx.dana.name},
					[]string{fx.alice.name, fx.bob.name})
			},
			wantStatus: http.StatusConflict,
			wantError:  "group_epoch_stale",
		},
		{
			name: "a member left out of the batch",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				req.Envelopes = req.Envelopes[:1]
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_group_message",
		},
		{
			name: "a non-member addressed in the batch",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				ts := req.Envelopes[0].Timestamp
				req.Envelopes = append(req.Envelopes,
					groupEnvelope(t, fx.alice, fx.stranger.name, fx.groupName, 1, fx.groupName, ts, "hello crew"))
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_group_message",
		},
		{
			name: "a thread that does not belong to the group",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				*req = fx.fanout(t, fx.alice, 1, "some-other-thread", "hello crew",
					[]string{fx.bob.name, fx.carol.name})
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_group_message",
		},
		{
			name: "envelopes disagree about the thread",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				ts := req.Envelopes[0].Timestamp
				req.Envelopes[1] = groupEnvelope(t, fx.alice, fx.carol.name, fx.groupName, 1,
					fx.groupName+":side", ts, "hello crew")
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_group_message",
		},
		{
			name: "envelope metadata names another group",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				for i := range req.Envelopes {
					req.Envelopes[i].Metadata = idpkg.GroupMessageMetadata("other.poweur.net", 1, nil)
				}
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_group_message",
		},
		{
			name: "a tampered payload breaks the signature",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				req.Envelopes[1].Payload = base64.RawURLEncoding.EncodeToString([]byte("something else"))
			},
			wantStatus: http.StatusUnauthorized,
			wantError:  "unauthorized",
		},
		{
			name: "an unencrypted envelope",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				req.Envelopes[0].Encryption = nil
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "encryption_required",
		},
		{
			name: "an empty batch",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				req.Envelopes = nil
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_group_message",
		},
		{
			name: "a duplicated recipient",
			mutate: func(t *testing.T, fx *groupFixture, req *GroupFanoutRequest) {
				req.Envelopes = append(req.Envelopes, req.Envelopes[0])
			},
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid_group_message",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newGroupFixture(t)
			req := fx.fanout(t, fx.alice, 1, fx.groupName, "hello crew",
				[]string{fx.bob.name, fx.carol.name})
			tc.mutate(t, fx, &req)
			resp, body := fx.post(t, req)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d (want %d): %s", resp.StatusCode, tc.wantStatus, body)
			}
			if !strings.Contains(string(body), tc.wantError) {
				t.Fatalf("body %s does not mention %q", body, tc.wantError)
			}
			// A refused batch delivers nothing at all: half a fan-out is
			// worse than none, because the members who got it would reply
			// into a conversation the others never saw.
			for _, m := range []hostedID{fx.bob, fx.carol, fx.stranger} {
				if got := fx.inboxOf(t, m); len(got) != 0 {
					t.Fatalf("%s received %v from a refused batch", m.name, got)
				}
			}
		})
	}
}

// A stale batch comes back with the current membership, so the client can
// re-encrypt without a second round trip.
func TestGroupFanoutStaleEpochReturnsCurrentMembership(t *testing.T) {
	fx := newGroupFixture(t)
	req := fx.fanout(t, fx.alice, 1, fx.groupName, "hello crew",
		[]string{fx.bob.name, fx.carol.name})
	fx.setMembership(t, 2, []string{fx.dana.name}, []string{fx.alice.name, fx.bob.name})
	resp, body := fx.post(t, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Error string            `json:"error"`
		Group idpkg.ShareGroup  `json:"group"`
		Extra map[string]string `json:"-"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Group.Epoch != 2 {
		t.Fatalf("returned epoch = %d, want 2", out.Group.Epoch)
	}
	if out.Group.HasMember(fx.carol.name) {
		t.Fatal("the returned membership should reflect carol's removal")
	}
	// And the rebuilt batch, against the new epoch and roster, goes through.
	retry := fx.fanout(t, fx.alice, 2, fx.groupName, "hello crew", []string{fx.bob.name})
	resp, body = fx.post(t, retry)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry: %d %s", resp.StatusCode, body)
	}
	if got := fx.inboxOf(t, fx.carol); len(got) != 0 {
		t.Fatalf("a removed member must not receive the next message: %v", got)
	}
}

// Sub-threads inside a group are accepted as long as they are prefixed with
// the group's own ID.
func TestGroupFanoutSubThread(t *testing.T) {
	fx := newGroupFixture(t)
	thread := idpkg.GroupThreadID(fx.groupName, "design")
	req := fx.fanout(t, fx.alice, 1, thread, "about the layout",
		[]string{fx.bob.name, fx.carol.name})
	resp, body := fx.post(t, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("sub-thread fan-out: %d %s", resp.StatusCode, body)
	}
	got := fx.inboxOf(t, fx.bob)
	if len(got) != 1 || !strings.Contains(got[0], "|"+fx.groupName+":design|") {
		t.Fatalf("bob got %v, want the sub-thread", got)
	}
}

func TestGroupRosterRead(t *testing.T) {
	fx := newGroupFixture(t)

	// A member reads the roster and gets the document as signed, so they
	// can verify the group's own signature rather than trust the relay.
	resp, gr := fx.readRoster(t, fx.bob)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member read: %d", resp.StatusCode)
	}
	if err := gr.VerifySignature(fx.group.pub); err != nil {
		t.Fatalf("roster does not verify against the group key: %v", err)
	}
	if gr.Epoch != 1 || len(gr.Members) != 3 {
		t.Fatalf("roster = %+v", gr)
	}

	// An admin who is not a member may read it too: administering a group
	// you are not in is a supported shape.
	resp, _ = fx.readRoster(t, fx.dana)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin read: %d", resp.StatusCode)
	}

	// A stranger gets exactly the answer a non-existent group gives. A
	// distinguishable refusal would answer "is X in this group?", which is
	// the oracle cross-relay resolution was deferred to avoid.
	resp, _ = fx.readRoster(t, fx.stranger)
	strangerBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stranger read: %d %s", resp.StatusCode, strangerBody)
	}
	missing, _ := http.NewRequest(http.MethodGet, fx.ts.URL+"/groups/nosuch.poweur.net", nil)
	challenge, sig := challengeFor(t, fx.ts, fx.stranger)
	missing.Header.Set("X-Poweur-Identity", fx.stranger.name)
	missing.Header.Set("X-Poweur-Challenge", challenge)
	missing.Header.Set("X-Poweur-Signature", sig)
	mresp, err := http.DefaultClient.Do(missing)
	if err != nil {
		t.Fatal(err)
	}
	missingBody, _ := io.ReadAll(mresp.Body)
	mresp.Body.Close()
	if mresp.StatusCode != http.StatusNotFound || string(missingBody) != string(strangerBody) {
		t.Fatalf("a non-member and a non-existent group must be indistinguishable:\n %d %s\n %d %s",
			resp.StatusCode, strangerBody, mresp.StatusCode, missingBody)
	}
}

func TestGroupRosterReadUnauthenticated(t *testing.T) {
	fx := newGroupFixture(t)
	resp, err := http.Get(fx.ts.URL + "/groups/" + fx.groupName)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// A challenge signed by the wrong key is refused, so holding someone's
// Poweur ID is not enough to read a roster.
func TestGroupRosterReadForgedSignature(t *testing.T) {
	fx := newGroupFixture(t)
	challenge, _ := challengeFor(t, fx.ts, fx.bob)
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(fx.stranger.priv, []byte(challenge)))
	req, _ := http.NewRequest(http.MethodGet, fx.ts.URL+"/groups/"+fx.groupName, nil)
	req.Header.Set("X-Poweur-Identity", fx.bob.name)
	req.Header.Set("X-Poweur-Challenge", challenge)
	req.Header.Set("X-Poweur-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// A direct send may not claim group provenance for a group this relay
// hosts: that path is the fan-out endpoint, where membership and epoch are
// actually checked. Without this, a stranger's signed message would be
// filed into the group's conversation by every recipient's client.
func TestDirectSendCannotForgeLocalGroupProvenance(t *testing.T) {
	fx := newGroupFixture(t)
	msg := groupEnvelope(t, fx.stranger, fx.bob.name, fx.groupName, 1, fx.groupName,
		time.Now().UTC().Format(time.RFC3339), "I am totally in this group")
	raw, _ := json.Marshal(msg)
	resp, err := http.Post(fx.ts.URL+"/messages", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "/groups/") {
		t.Fatalf("the refusal should point at the fan-out endpoint: %s", body)
	}
	if got := fx.inboxOf(t, fx.bob); len(got) != 0 {
		t.Fatalf("bob received a forged group message: %v", got)
	}
}

// Even a real member must use the fan-out endpoint, so that "sent to the
// group" always means every member got it.
func TestDirectSendByMemberStillNeedsFanout(t *testing.T) {
	fx := newGroupFixture(t)
	msg := groupEnvelope(t, fx.alice, fx.bob.name, fx.groupName, 1, fx.groupName,
		time.Now().UTC().Format(time.RFC3339), "just to you, but labelled group")
	raw, _ := json.Marshal(msg)
	resp, err := http.Post(fx.ts.URL+"/messages", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
	}
}

// A direct message with unrelated metadata is untouched by the group guard.
func TestDirectSendWithUnrelatedMetadataStillWorks(t *testing.T) {
	fx := newGroupFixture(t)
	msg := groupEnvelope(t, fx.alice, fx.bob.name, fx.groupName, 1, "", time.Now().UTC().Format(time.RFC3339), "hi")
	msg.Metadata = map[string]string{"mime": "text/plain"}
	msg.ThreadID = ""
	encMeta := &crypto.EncryptionMeta{
		Alg:                msg.Encryption.Alg,
		EphemeralPublicKey: msg.Encryption.EphemeralPublicKey,
		Nonce:              msg.Encryption.Nonce,
	}
	canonical := crypto.CanonicalMessageEnvelope(
		msg.Sender, msg.Recipient, msg.Timestamp, msg.Payload,
		msg.ID, msg.SessionID, msg.Type, msg.ThreadID, msg.ExpiresAt, msg.Metadata, encMeta)
	msg.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fx.alice.priv, []byte(canonical)))
	raw, _ := json.Marshal(msg)
	resp, err := http.Post(fx.ts.URL+"/messages", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", resp.StatusCode, body)
	}
}

// A group larger than the v1 fan-out cap is refused rather than quietly
// taking longer and longer. The cap is the MLS study's revisit threshold.
func TestGroupFanoutRefusesOversizedGroup(t *testing.T) {
	fx := newGroupFixture(t)
	members := []string{fx.alice.name}
	for i := 0; i < idpkg.MaxGroupFanoutMembers; i++ {
		members = append(members, fmt.Sprintf("m%03d.poweur.net", i))
	}
	fx.setMembership(t, 2, []string{fx.dana.name}, members)
	req := fx.fanout(t, fx.alice, 2, fx.groupName, "hello", members[1:])
	resp, body := fx.post(t, req)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "group_too_large") {
		t.Fatalf("body = %s", body)
	}
}

// An envelope over the 512 KB message cap is refused even inside a batch:
// the cap bounds what lands in one inbox, and a group message lands in an
// inbox like any other.
func TestGroupFanoutKeepsPerEnvelopeCap(t *testing.T) {
	fx := newGroupFixture(t)
	req := fx.fanout(t, fx.alice, 1, fx.groupName, "hello crew",
		[]string{fx.bob.name, fx.carol.name})
	req.Envelopes[0].Payload = strings.Repeat("A", maxMessageBytes+1)
	resp, body := fx.post(t, req)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", resp.StatusCode, body)
	}
}

// A group whose membership document is signed by somebody else is not a
// group here — the same answer as no group at all.
func TestGroupWithForgedMembershipIsNotResolvable(t *testing.T) {
	fx := newGroupFixture(t)
	forged := idpkg.ShareGroup{
		Group:     fx.groupName,
		Owner:     fx.groupName,
		Members:   []string{fx.stranger.name},
		Admins:    []string{fx.stranger.name},
		Epoch:     1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	// Signed by the stranger and stored in the group's own drive (the
	// relay stores what the owner commits; the signature is the gate).
	if err := forged.Sign(fx.stranger.priv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(forged)
	setSysFile(t, fx.server, fx.groupName, groupRosterPath, string(raw))

	rosterResp, _ := fx.readRoster(t, fx.stranger)
	rosterResp.Body.Close()
	if rosterResp.StatusCode != http.StatusNotFound {
		t.Fatalf("a forged membership document must not resolve: %d", rosterResp.StatusCode)
	}
}

// The public epoch endpoint answers 0 for an identity that is not a group,
// so it tells a stranger nothing about which identities are groups.
func TestGroupEpochEndpoint(t *testing.T) {
	fx := newGroupFixture(t)
	for name, want := range map[string]float64{fx.groupName: 1, fx.alice.name: 0} {
		resp, err := http.Get(fx.ts.URL + "/groups/" + name + "/epoch")
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]float64
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || out["epoch"] != want {
			t.Fatalf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
	resp, _ := http.Get(fx.ts.URL + "/groups/nobody.poweur.net/epoch")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unhosted: %d", resp.StatusCode)
	}
}
