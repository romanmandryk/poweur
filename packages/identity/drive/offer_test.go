package drive

import (
	"strings"
	"testing"
	"time"
)

func testOffer() ShareOffer {
	s := testShare()
	return ShareOffer{Format: OfferFormat, Share: s, Relay: "relay.poweur.net", Name: "Plans", Kind: KindFolder, OfferedAt: "2026-09-29T08:00:00Z"}
}

func TestShareOfferShapes(t *testing.T) {
	if err := testOffer().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ShareOffer){
		"old format": func(o *ShareOffer) { o.Format = 1 },
		"link share": func(o *ShareOffer) {
			o.Share.Member, o.Share.Link, o.Share.Role, o.Share.NodeKey = "", strings.Repeat("c3", 16), RoleCreate, nil
		},
		"relay path":    func(o *ShareOffer) { o.Relay = "evil.example/redirect" },
		"no relay":      func(o *ShareOffer) { o.Relay = "" },
		"bad kind":      func(o *ShareOffer) { o.Kind = "board" },
		"slash in name": func(o *ShareOffer) { o.Name = "a/b" },
		"bad share":     func(o *ShareOffer) { o.Share.Role = "owner" },
		"bad time":      func(o *ShareOffer) { o.OfferedAt = "yesterday" },
	} {
		o := testOffer()
		mutate(&o)
		if o.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMountsAcceptAndDrop(t *testing.T) {
	var m Mounts
	offer := testOffer()
	accept, err := m.Accept(offer, time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC))
	if err != nil || accept.Validate() != nil || accept.ShareID != offer.Share.ID || len(m.Mounts) != 1 || m.Mounts[0].Name != "Plans" {
		t.Fatalf("accept: %+v %+v %v", accept, m, err)
	}
	// A re-offer of the same node refreshes rather than duplicates.
	offer.Share.ID = strings.Repeat("d4", 16)
	if _, err := m.Accept(offer, time.Now()); err != nil || len(m.Mounts) != 1 || m.Mounts[0].ShareID != offer.Share.ID {
		t.Fatalf("re-offer: %+v", m)
	}
	revoked := ShareRevoked{Format: OfferFormat, Drive: offer.Share.Drive, ShareID: offer.Share.ID, RevokedAt: "2026-09-30T00:00:00Z"}
	if revoked.Validate() != nil || !m.Drop(revoked) || len(m.Mounts) != 0 || m.Drop(revoked) {
		t.Fatalf("drop: %+v", m)
	}
}
