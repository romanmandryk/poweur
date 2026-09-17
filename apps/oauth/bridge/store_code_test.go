package bridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFinishTxnWithCodeIsOneWrite(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	id := txnIDFrom(t, h.browser().get(authorizeQuery("rp", rpRedirect, "openid")).location)
	expires := h.now.Add(codeTTL)
	grant := CodeGrant{ClientID: "rp", RedirectURI: rpRedirect}
	accept := func(t *Txn) error { return t.usable() }

	// A refused check writes neither.
	refuse := errors.New("no")
	if _, err := h.store.FinishTxnWithCode(ctx, id, func(*Txn) error { return refuse }, "hash-a", grant, expires); !errors.Is(err, refuse) {
		t.Fatalf("refused = %v", err)
	}
	if h.txn(id).Done {
		t.Fatal("refused check finished the transaction")
	}
	if _, err := h.store.RedeemCode(ctx, "hash-a", func(*CodeGrant) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused check stored a code: %v", err)
	}

	// A code that cannot be stored leaves the transaction unfinished.
	if err := h.store.PutCode(ctx, "taken", grant, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.FinishTxnWithCode(ctx, id, accept, "taken", grant, expires); err == nil {
		t.Fatal("duplicate code hash accepted")
	}
	if h.txn(id).Done {
		t.Fatal("failed code insert still finished the transaction")
	}

	// Success does both, once.
	if _, err := h.store.FinishTxnWithCode(ctx, id, accept, "hash-b", grant, expires); err != nil {
		t.Fatal(err)
	}
	if !h.txn(id).Done {
		t.Fatal("transaction not finished")
	}
	if _, err := h.store.FinishTxnWithCode(ctx, id, accept, "hash-c", grant, expires); !errors.Is(err, errTxnDone) {
		t.Fatalf("second finish = %v", err)
	}
	if _, err := h.store.RedeemCode(ctx, "hash-c", func(*CodeGrant) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second finish stored a code: %v", err)
	}
}

func TestRedeemCodeSpendsOnlyWhatItAccepts(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	if err := h.store.PutCode(ctx, "c", CodeGrant{ClientID: "rp"}, h.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	wrong := &grantRefusal{"nope"}
	if _, err := h.store.RedeemCode(ctx, "c", func(*CodeGrant) error { return wrong }); !errors.Is(err, wrong) {
		t.Fatalf("refusal = %v", err)
	}
	g, err := h.store.RedeemCode(ctx, "c", func(g *CodeGrant) error { return nil })
	if err != nil || g.ClientID != "rp" {
		t.Fatalf("redeem = %v %v", g, err)
	}
	// Spent: even a check that would refuse sees reuse (and revocation).
	if _, err := h.store.RedeemCode(ctx, "c", func(*CodeGrant) error { return wrong }); !errors.Is(err, errCodeReused) {
		t.Fatalf("reuse = %v", err)
	}
}
