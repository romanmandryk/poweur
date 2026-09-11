package files

import (
	"path/filepath"
	"sync"
	"testing"
)

// Link-share accounting (E05-T4). The counters are the only thing standing
// between a download cap and a lie, so they are tested for atomicity and
// for surviving a restart.

func newTestLinkStats(t *testing.T) (*LinkStats, string) {
	t.Helper()
	root := t.TempDir()
	home := func(identity string) (string, error) {
		return filepath.Join(root, identity), nil
	}
	return NewLinkStats(home), root
}

func TestLinkStatsReserveAndBytes(t *testing.T) {
	ls, _ := newTestLinkStats(t)

	st := ls.Get("alice.poweur.net", "shr_1")
	if st.Downloads != 0 || st.Bytes != 0 {
		t.Fatalf("unused share must start at zero, got %+v", st)
	}

	st, ok := ls.Reserve("alice.poweur.net", "shr_1", 0)
	if !ok || st.Downloads != 1 {
		t.Fatalf("first reserve: ok=%v %+v", ok, st)
	}
	if st.FirstAt.IsZero() || st.LastAt.IsZero() {
		t.Fatal("reserve must stamp first/last seen")
	}
	ls.AddBytes("alice.poweur.net", "shr_1", 1024)
	ls.AddBytes("alice.poweur.net", "shr_1", 512)
	if got := ls.Get("alice.poweur.net", "shr_1"); got.Bytes != 1536 || got.Downloads != 1 {
		t.Fatalf("bandwidth accounting: %+v", got)
	}
	// A zero or negative transfer is not an error, just nothing to charge.
	ls.AddBytes("alice.poweur.net", "shr_1", 0)
	ls.AddBytes("alice.poweur.net", "shr_1", -5)
	if got := ls.Get("alice.poweur.net", "shr_1").Bytes; got != 1536 {
		t.Fatalf("empty transfer changed the total: %d", got)
	}
}

func TestLinkStatsCap(t *testing.T) {
	ls, _ := newTestLinkStats(t)
	const max = 3
	for i := 1; i <= max; i++ {
		if st, ok := ls.Reserve("alice.poweur.net", "shr_cap", max); !ok || st.Downloads != int64(i) {
			t.Fatalf("reserve %d: ok=%v %+v", i, ok, st)
		}
	}
	if st, ok := ls.Reserve("alice.poweur.net", "shr_cap", max); ok {
		t.Fatalf("reserve past the cap must fail, got %+v", st)
	}
	if !ls.Exhausted("alice.poweur.net", "shr_cap", max) {
		t.Fatal("Exhausted must agree with Reserve")
	}
	// An exhausted share is exhausted only against its own cap.
	if ls.Exhausted("alice.poweur.net", "shr_cap", 0) {
		t.Fatal("cap 0 means unlimited")
	}
	if ls.Exhausted("alice.poweur.net", "shr_other", max) {
		t.Fatal("an untouched share is not exhausted")
	}
}

// A cap that two visitors can race past is not a cap.
func TestLinkStatsReserveIsAtomic(t *testing.T) {
	ls, _ := newTestLinkStats(t)
	const max, racers = 10, 64

	var wg sync.WaitGroup
	granted := make(chan struct{}, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := ls.Reserve("alice.poweur.net", "shr_race", max); ok {
				granted <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(granted)
	if got := len(granted); got != max {
		t.Fatalf("%d racers past a cap of %d", got, max)
	}
	if got := ls.Get("alice.poweur.net", "shr_race").Downloads; got != max {
		t.Fatalf("counter overshot the cap: %d", got)
	}
}

// Counters that forget on restart make expiry and caps meaningless.
func TestLinkStatsSurviveRestart(t *testing.T) {
	root := t.TempDir()
	home := func(identity string) (string, error) { return filepath.Join(root, identity), nil }

	first := NewLinkStats(home)
	first.Reserve("alice.poweur.net", "shr_p", 0)
	first.Reserve("alice.poweur.net", "shr_p", 0)
	first.AddBytes("alice.poweur.net", "shr_p", 4096)

	// A brand-new store over the same directory is what a relay restart is.
	second := NewLinkStats(home)
	got := second.Get("alice.poweur.net", "shr_p")
	if got.Downloads != 2 || got.Bytes != 4096 {
		t.Fatalf("counters did not survive a restart: %+v", got)
	}
	if second.Exhausted("alice.poweur.net", "shr_p", 2) != true {
		t.Fatal("a cap reached before the restart must still be reached after it")
	}
}

func TestLinkStatsAllAndIsolation(t *testing.T) {
	ls, _ := newTestLinkStats(t)
	for _, id := range []string{"shr_b", "shr_a", "shr_c"} {
		ls.Reserve("alice.poweur.net", id, 0)
	}
	ls.Reserve("bob.poweur.net", "shr_z", 0)

	all := ls.All("alice.poweur.net")
	if len(all) != 3 {
		t.Fatalf("want 3 shares, got %d", len(all))
	}
	for i, want := range []string{"shr_a", "shr_b", "shr_c"} {
		if all[i].ShareID != want {
			t.Fatalf("All must be share-id ordered: %+v", all)
		}
	}
	// One owner's counters never appear in another's.
	if bob := ls.All("bob.poweur.net"); len(bob) != 1 || bob[0].ShareID != "shr_z" {
		t.Fatalf("owner isolation: %+v", bob)
	}
}

// A store with no home directory (a relay with no POWEUR_DATA) counts in
// memory instead of crashing.
func TestLinkStatsWithoutHomeDir(t *testing.T) {
	ls := NewLinkStats(nil)
	if _, ok := ls.Reserve("alice.poweur.net", "shr_m", 2); !ok {
		t.Fatal("memory-only reserve must work")
	}
	ls.AddBytes("alice.poweur.net", "shr_m", 10)
	if got := ls.Get("alice.poweur.net", "shr_m"); got.Downloads != 1 || got.Bytes != 10 {
		t.Fatalf("memory-only counters: %+v", got)
	}

	// A nil store is inert rather than a panic, so callers on a relay with
	// the file layer disabled need no special case.
	var nilStats *LinkStats
	if _, ok := nilStats.Reserve("alice.poweur.net", "shr_m", 1); !ok {
		t.Fatal("a nil store must not block a download")
	}
	nilStats.AddBytes("alice.poweur.net", "shr_m", 5)
	if got := nilStats.Get("alice.poweur.net", "shr_m"); got.Downloads != 0 {
		t.Fatalf("nil store must report nothing: %+v", got)
	}
	if nilStats.All("alice.poweur.net") != nil {
		t.Fatal("nil store lists nothing")
	}
}
