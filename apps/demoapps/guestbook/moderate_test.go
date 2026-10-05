package guestbook

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func seedLog(t *testing.T) (*MemStore, []Entry) {
	t.Helper()
	entries := []Entry{
		{ID: "gb_000000000001", Identity: "alice.poweur.net", Message: "first", At: "2026-10-01T10:00:00Z"},
		{ID: "gb_000000000002", Identity: "bobbob.poweur.net", Message: "second\n\nwith two paragraphs", At: "2026-10-01T11:00:00Z"},
		{ID: "gb_000000000003", Identity: "carol1.poweur.net", Message: "third", At: "2026-10-02T09:30:00Z"},
	}
	store := NewMemStore()
	var page1, page2 string
	page1 = formatEntry(entries[0]) + formatEntry(entries[1])
	page2 = formatEntry(entries[2])
	ctx := context.Background()
	store.Write(ctx, pageName(1), []byte(page1))
	store.Write(ctx, pageName(2), []byte(page2))
	return store, entries
}

func TestRemoveEntryCutsOnlyThatEntry(t *testing.T) {
	ctx := context.Background()
	store, entries := seedLog(t)
	got, err := RemoveEntry(ctx, store, entries[1].ID, Guard{Identity: "bobbob.poweur.net", At: "2026-10-01T11"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Page != pageName(1) || got.Message != entries[1].Message {
		t.Fatalf("removed %+v", got)
	}
	data, _ := store.Read(ctx, pageName(1))
	if string(data) != formatEntry(entries[0]) {
		t.Fatalf("page 1 is now %q, want only the first entry", data)
	}
	other, _ := store.Read(ctx, pageName(2))
	if string(other) != formatEntry(entries[2]) {
		t.Fatalf("page 2 changed: %q", other)
	}
}

func TestRemoveEntryTheLastOnAPageAndTheFirst(t *testing.T) {
	ctx := context.Background()
	store, entries := seedLog(t)
	if _, err := RemoveEntry(ctx, store, entries[0].ID, Guard{}, false); err != nil {
		t.Fatal(err)
	}
	data, _ := store.Read(ctx, pageName(1))
	if string(data) != formatEntry(entries[1]) {
		t.Fatalf("page 1 is %q", data)
	}
	if _, err := RemoveEntry(ctx, store, entries[2].ID, Guard{}, false); err != nil {
		t.Fatal(err)
	}
	data, _ = store.Read(ctx, pageName(2))
	if len(parseLog(data)) != 0 {
		t.Fatalf("page 2 still has entries: %q", data)
	}
}

func TestRemoveEntryDryRunAndGuards(t *testing.T) {
	ctx := context.Background()
	store, entries := seedLog(t)
	before, _ := store.Read(ctx, pageName(1))

	if _, err := RemoveEntry(ctx, store, entries[1].ID, Guard{}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveEntry(ctx, store, entries[1].ID, Guard{Identity: "mallory.poweur.net"}, false); err == nil || !strings.Contains(err.Error(), "nothing removed") {
		t.Fatalf("a wrong identity must refuse, got %v", err)
	}
	if _, err := RemoveEntry(ctx, store, entries[1].ID, Guard{At: "2026-09"}, false); err == nil {
		t.Fatal("a wrong time must refuse")
	}
	if _, err := RemoveEntry(ctx, store, "gb_nope", Guard{}, false); !errors.Is(err, ErrNoSuchEntry) {
		t.Fatalf("unknown id: %v", err)
	}
	after, _ := store.Read(ctx, pageName(1))
	if !bytes.Equal(before, after) {
		t.Fatal("dry runs and refusals must not change the log")
	}
}

func TestRunAdmin(t *testing.T) {
	ctx := context.Background()
	store, entries := seedLog(t)
	var out, errb bytes.Buffer

	if code := RunAdmin(ctx, store, []string{"list"}, &out, &errb); code != 0 || strings.Count(out.String(), "\n") != 3 || !strings.Contains(out.String(), entries[2].ID) {
		t.Fatalf("list: code %d out %q err %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := RunAdmin(ctx, store, []string{"remove", entries[0].ID}, &out, &errb); code != 0 || !strings.Contains(out.String(), "dry run") {
		t.Fatalf("dry run: code %d out %q", code, out.String())
	}
	if all, _ := ListEntries(ctx, store); len(all) != 3 {
		t.Fatal("a dry run removed something")
	}
	out.Reset()
	if code := RunAdmin(ctx, store, []string{"remove", entries[0].ID, "--identity", "alice.poweur.net", "--yes"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "removed ") {
		t.Fatalf("remove: code %d out %q err %q", code, out.String(), errb.String())
	}
	if all, _ := ListEntries(ctx, store); len(all) != 2 {
		t.Fatalf("%d entries left, want 2", len(all))
	}
	if code := RunAdmin(ctx, store, []string{"remove"}, &out, &errb); code != 2 {
		t.Fatalf("missing id: code %d", code)
	}
}
