package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/poweur/identity"
)

type recordedPush struct {
	to      string
	body    []byte
	expires time.Time
}

type pushRecorder struct {
	mu   sync.Mutex
	sent []recordedPush
	err  error
}

func (p *pushRecorder) Push(_ context.Context, to string, body []byte, exp time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.sent = append(p.sent, recordedPush{to, body, exp})
	return nil
}

func TestPushSendsAPromptAndStillNeedsTheCode(t *testing.T) {
	rec := &pushRecorder{}
	h := newHarness(t, func(c *Config) {
		c.Pusher = rec
		c.PushIdentity = "bridge.poweur.org"
	})
	b := h.browser()
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	var ap awaitPage
	if b.get("/t/"+id).data(t, &ap); ap.Push == nil || *ap.Push != (pushPage{From: "bridge.poweur.org", Sent: 0, Left: maxPushesPerTxn}) {
		t.Fatalf("push offer = %+v", ap.Push)
	}

	type answer struct {
		Notice string `json:"notice"`
		Left   int    `json:"left"`
	}
	var got answer
	if status := b.fetchJSON("/t/"+id+"/push", &got); status != http.StatusOK || got != (answer{"sent", maxPushesPerTxn - 1}) {
		t.Fatalf("push = %d %+v", status, got)
	}
	if len(rec.sent) != 1 || rec.sent[0].to != alice {
		t.Fatalf("sent = %+v", rec.sent)
	}
	txn := h.txn(id)
	payload, req, err := identity.ParseAuthRequestPayload(rec.sent[0].body, h.clock())
	if err != nil {
		t.Fatal(err)
	}
	if req.RequestID != txn.RequestID || payload.Client != "Relying Party" || payload.ClientHost != "rp.example" {
		t.Fatalf("payload = %+v", payload)
	}
	if strings.Contains(string(rec.sent[0].body), txn.Match) && len(txn.Match) > 1 && strings.Contains(string(rec.sent[0].body), `"`+txn.Match+`"`) {
		t.Fatal("the match code travelled in the prompt")
	}
	if !rec.sent[0].expires.Equal(txn.RequestExpires) {
		t.Fatalf("prompt expiry %v, request %v", rec.sent[0].expires, txn.RequestExpires)
	}
	// Spacing, then the cap.
	if b.fetchJSON("/t/"+id+"/push", &got); got.Notice != "too-soon" {
		t.Fatalf("second push = %+v", got)
	}
	h.advance(pushSpacing)
	if b.fetchJSON("/t/"+id+"/push", &got); got != (answer{"sent", 1}) {
		t.Fatalf("third push = %+v", got)
	}
	// A plain form post (no script) goes back to the page.
	h.advance(pushSpacing)
	if p := b.post("/t/"+id+"/push", url.Values{}); p.status != http.StatusSeeOther || !strings.HasSuffix(p.location, "push=sent") {
		t.Fatalf("form push = %d %s", p.status, p.location)
	}
	h.advance(pushSpacing)
	if b.fetchJSON("/t/"+id+"/push", &got); got != (answer{"too-many", 0}) {
		t.Fatalf("fourth push = %+v", got)
	}
	if len(rec.sent) != maxPushesPerTxn {
		t.Fatalf("%d prompts sent", len(rec.sent))
	}

	// Approving from the app is the cross-device path: the code is required.
	approval := h.approve(id, h.users[alice])
	if status, _ := h.deliver(approval, txn.Match); status != http.StatusOK {
		t.Fatalf("approval with code = %d", status)
	}
	if st := b.status(id); st.Status != "complete" {
		t.Fatalf("status = %+v", st)
	}
}

func TestPushRefusals(t *testing.T) {
	rec := &pushRecorder{}
	h := newHarness(t, func(c *Config) { c.Pusher = rec; c.PushIdentity = "bridge.poweur.org" })
	b := h.browser()

	// Another browser cannot push this transaction.
	id := b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	if p := h.browser().post("/t/"+id+"/push", url.Values{}); p.status != http.StatusForbidden {
		t.Fatalf("foreign push = %d", p.status)
	}
	// Nothing to push before identify, or after an approval arrived.
	p := b.get(authorizeQuery("rp", rpRedirect, "openid"))
	fresh := txnIDFrom(t, p.location)
	if p := b.post("/t/"+fresh+"/push", url.Values{}); !strings.HasSuffix(p.location, "push=not-pending") {
		t.Fatalf("push before identify = %s", p.location)
	}
	h.deliver(h.approve(id, h.users[alice]), "")
	if p := b.post("/t/"+id+"/push", url.Values{}); !strings.HasSuffix(p.location, "push=not-pending") {
		t.Fatalf("push after approval = %s", p.location)
	}
	// A failing pusher is reported, not hidden.
	rec.err = errors.New("relay refused")
	id = b.identify(authorizeQuery("rp", rpRedirect, "openid"), h.users[alice])
	if p := b.post("/t/"+id+"/push", url.Values{}); !strings.HasSuffix(p.location, "push=failed") {
		t.Fatalf("failed push = %s", p.location)
	}
	if len(rec.sent) != 0 {
		t.Fatalf("sent = %+v", rec.sent)
	}

	// No pusher: no button, and the endpoint says so.
	plain := newHarness(t)
	pb := plain.browser()
	pid := pb.identify(authorizeQuery("rp", rpRedirect, "openid"), plain.users[alice])
	if strings.Contains(pb.get("/t/"+pid).body, "Send to my Poweur app") {
		t.Fatal("push button without a pusher")
	}
	if p := pb.post("/t/"+pid+"/push", url.Values{}); p.status != http.StatusNotFound {
		t.Fatalf("push without a pusher = %d", p.status)
	}
}
