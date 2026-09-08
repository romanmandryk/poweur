package relay

import (
	"testing"
	"time"
)

// The hub is the part with sharp edges: it is written to from every message
// POST and read from every open stream.
func TestHubDeliversToEverySubscriber(t *testing.T) {
	h := newHub()
	_, first, ok := h.subscribe("bob.poweur.net", 0)
	if !ok {
		t.Fatal("subscribe refused")
	}
	_, second, _ := h.subscribe("BOB.poweur.net", 0) // case must not split the room
	h.publish("bob.poweur.net", streamEvent{Type: "message", MessageID: "m1"})

	for i, channel := range []<-chan streamEvent{first, second} {
		select {
		case event := <-channel:
			if event.MessageID != "m1" {
				t.Fatalf("subscriber %d got %+v", i, event)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d never heard about the message", i)
		}
	}
}

// A POST must never wait on a reader that stopped reading.
func TestPublishDropsRatherThanBlocking(t *testing.T) {
	h := newHub()
	h.subscribe("bob.poweur.net", 0)

	done := make(chan struct{})
	go func() {
		for i := 0; i < eventBuffer*4; i++ {
			h.publish("bob.poweur.net", streamEvent{Type: "message"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a full subscriber buffer")
	}
}

func TestSubscribeRespectsThePerIdentityLimit(t *testing.T) {
	h := newHub()
	if _, _, ok := h.subscribe("bob.poweur.net", 1); !ok {
		t.Fatal("first stream refused")
	}
	if _, _, ok := h.subscribe("bob.poweur.net", 1); ok {
		t.Fatal("second stream accepted past the limit")
	}
	// A different identity is unaffected.
	if _, _, ok := h.subscribe("alice.poweur.net", 1); !ok {
		t.Fatal("limit leaked across identities")
	}
}

func TestUnsubscribeFreesTheSlot(t *testing.T) {
	h := newHub()
	id, _, _ := h.subscribe("bob.poweur.net", 1)
	h.unsubscribe("bob.poweur.net", id)
	if h.count("bob.poweur.net") != 0 {
		t.Fatal("subscriber still counted after unsubscribe")
	}
	if _, _, ok := h.subscribe("bob.poweur.net", 1); !ok {
		t.Fatal("slot was not released")
	}
	// Publishing to nobody is fine.
	h.publish("nobody.poweur.net", streamEvent{Type: "message"})
}
