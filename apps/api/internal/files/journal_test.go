package files

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestIndex(t *testing.T) (*Index, func(string) (string, error)) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "identities", "alice__poweur__net")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	homeFn := func(string) (string, error) { return home, nil }
	return NewIndex(homeFn), homeFn
}

const owner = "alice.poweur.net"

func TestJournalRecordsMutations(t *testing.T) {
	ix, _ := newTestIndex(t)

	ix.RecordMkdir(owner, "private/docs", time.Now(), owner)
	ix.RecordWrite(owner, "private/docs/a.txt", "e1", 3, time.Now(), owner)
	ix.RecordDelete(owner, "private/docs/a.txt", owner)

	recs, latest, gap := ix.Changes(owner, 0, 0, nil)
	if gap {
		t.Fatal("no compaction happened; gap must be false")
	}
	if len(recs) != 3 || latest != 3 {
		t.Fatalf("want 3 records latest=3, got %d records latest=%d", len(recs), latest)
	}
	wantOps := []string{OpMkdir, OpPut, OpDelete}
	for i, rec := range recs {
		if rec.Op != wantOps[i] {
			t.Fatalf("record %d op = %q, want %q", i, rec.Op, wantOps[i])
		}
		if rec.ChangeID != int64(i+1) {
			t.Fatalf("record %d change_id = %d, want %d", i, rec.ChangeID, i+1)
		}
		if rec.Actor != owner {
			t.Fatalf("record %d actor = %q", i, rec.Actor)
		}
	}
	if recs[1].ETag != "e1" || recs[1].Size != 3 {
		t.Fatalf("put record must carry etag+size: %+v", recs[1])
	}
}

func TestJournalRenameIsDeletePlusPuts(t *testing.T) {
	ix, _ := newTestIndex(t)
	ix.RecordMkdir(owner, "private/dir", time.Now(), owner)
	ix.RecordWrite(owner, "private/dir/f1", "e1", 1, time.Now(), owner)
	ix.RecordWrite(owner, "private/dir/f2", "e2", 2, time.Now(), owner)

	ix.RecordRename(owner, "private/dir", "shared/dir", owner)

	recs, _, _ := ix.Changes(owner, 3, 0, nil)
	if len(recs) != 4 {
		t.Fatalf("rename of dir+2 files must journal delete+3 records, got %d: %+v", len(recs), recs)
	}
	if recs[0].Op != OpDelete || recs[0].Path != "private/dir" {
		t.Fatalf("first rename record must delete old prefix: %+v", recs[0])
	}
	seen := map[string]string{}
	for _, rec := range recs[1:] {
		seen[rec.Path] = rec.Op
	}
	if seen["shared/dir"] != OpMkdir || seen["shared/dir/f1"] != OpPut || seen["shared/dir/f2"] != OpPut {
		t.Fatalf("rename must re-create entries at new prefix: %v", seen)
	}
	// change_ids must be strictly increasing across the whole journal.
	all, _, _ := ix.Changes(owner, 0, 0, nil)
	for i := 1; i < len(all); i++ {
		if all[i].ChangeID <= all[i-1].ChangeID {
			t.Fatalf("change ids not strictly increasing: %+v", all)
		}
	}
}

func TestJournalCursorLimitAndVisibility(t *testing.T) {
	ix, _ := newTestIndex(t)
	ix.RecordWrite(owner, "private/a", "e1", 1, time.Now(), owner)
	ix.RecordWrite(owner, "public/b", "e2", 1, time.Now(), owner)
	ix.RecordWrite(owner, "private/c", "e3", 1, time.Now(), owner)

	recs, _, _ := ix.Changes(owner, 1, 0, nil)
	if len(recs) != 2 || recs[0].Path != "public/b" {
		t.Fatalf("since=1 must return records 2..3: %+v", recs)
	}
	recs, _, _ = ix.Changes(owner, 0, 1, nil)
	if len(recs) != 1 || recs[0].ChangeID != 1 {
		t.Fatalf("limit=1 must return the first record: %+v", recs)
	}
	onlyPublic := func(p string) bool { return Under(p, RootPublic) }
	recs, latest, _ := ix.Changes(owner, 0, 0, onlyPublic)
	if len(recs) != 1 || recs[0].Path != "public/b" || latest != 3 {
		t.Fatalf("visibility filter failed: %+v latest=%d", recs, latest)
	}
}

func TestJournalPersistsAcrossReload(t *testing.T) {
	ix, homeFn := newTestIndex(t)
	ix.RecordWrite(owner, "private/a", "e1", 1, time.Now(), owner)
	ix.RecordDelete(owner, "private/a", owner)

	ix2 := NewIndex(homeFn)
	recs, latest, gap := ix2.Changes(owner, 0, 0, nil)
	if gap || len(recs) != 2 || latest != 2 {
		t.Fatalf("journal must reload from disk: %d records latest=%d gap=%v", len(recs), latest, gap)
	}
	if recs[1].Op != OpDelete {
		t.Fatalf("reloaded record mismatch: %+v", recs[1])
	}
}

func TestJournalCompactionForcesResync(t *testing.T) {
	ix, _ := newTestIndex(t)
	// Overflow the record bound so compaction drops the oldest entries.
	for i := 0; i < journalMaxRecords+10; i++ {
		ix.RecordWrite(owner, "private/f", "e", 1, time.Now(), owner)
	}
	_, _, gap := ix.Changes(owner, 0, 0, nil)
	if !gap {
		t.Fatal("cursor 0 must be a gap after compaction")
	}
	recs, latest, gap := ix.Changes(owner, latestCursor(ix), 0, nil)
	if gap || len(recs) != 0 {
		t.Fatalf("current cursor must not be a gap: gap=%v recs=%d latest=%d", gap, len(recs), latest)
	}
}

func latestCursor(ix *Index) int64 { return ix.ChangeID(owner) }
