package guestbook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
)

// Moderation by hand: list the log's entries, and remove one by its id. An entry is
// identified by id; --identity and --at make the operator say who and when as well, so a
// mistyped id cannot remove somebody else's note. The running guestbook builds each new post
// on what the store holds now (see add), so removing an entry while it runs is safe, and the
// page drops the entry within a minute (Refresh).

// ListedEntry is an entry and the log file that holds it.
type ListedEntry struct {
	Entry
	Page string
}

// ListEntries returns every entry in the log, oldest first.
func ListEntries(ctx context.Context, store Store) ([]ListedEntry, error) {
	pages, err := pageNames(ctx, store)
	if err != nil {
		return nil, err
	}
	var out []ListedEntry
	for _, name := range pages {
		data, err := store.Read(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, e := range parseLog(data) {
			out = append(out, ListedEntry{Entry: e, Page: name})
		}
	}
	return out, nil
}

func pageNames(ctx context.Context, store Store) ([]string, error) {
	names, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	var pages []string
	for _, n := range names {
		if _, ok := parsePageName(n); ok {
			pages = append(pages, n)
		}
	}
	sort.Strings(pages)
	return pages, nil
}

// Guard is what the operator says the entry is. An empty field is not checked; At is a prefix
// of the entry's RFC 3339 time, so "2026-10-01T19:23" is enough.
type Guard struct {
	Identity string
	At       string
}

var ErrNoSuchEntry = errors.New("no entry with that id")

// RemoveEntry deletes the entry with this id from the log. With dryRun it only finds it.
func RemoveEntry(ctx context.Context, store Store, id string, guard Guard, dryRun bool) (ListedEntry, error) {
	pages, err := pageNames(ctx, store)
	if err != nil {
		return ListedEntry{}, err
	}
	for _, name := range pages {
		data, err := store.Read(ctx, name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return ListedEntry{}, err
		}
		rest, removed, ok := removeBlock(data, id)
		if !ok {
			continue
		}
		found := ListedEntry{Entry: removed, Page: name}
		if guard.Identity != "" && removed.Identity != guard.Identity {
			return found, fmt.Errorf("entry %s is by %s, not %s: nothing removed", id, removed.Identity, guard.Identity)
		}
		if guard.At != "" && !strings.HasPrefix(removed.At, guard.At) {
			return found, fmt.Errorf("entry %s was written at %s, not %s: nothing removed", id, removed.At, guard.At)
		}
		if dryRun {
			return found, nil
		}
		return found, store.Write(ctx, name, rest)
	}
	return ListedEntry{}, fmt.Errorf("%w: %s", ErrNoSuchEntry, id)
}

// removeBlock cuts the entry with this id out of a log file and leaves every other byte alone.
func removeBlock(data []byte, id string) (rest []byte, removed Entry, ok bool) {
	lines := strings.SplitAfter(string(data), "\n")
	start := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, logMarker) {
			continue
		}
		if start >= 0 {
			block := strings.Join(lines[start:i], "")
			return []byte(strings.Join(lines[:start], "") + strings.Join(lines[i:], "")), firstEntry(block), true
		}
		m := logMarkerLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if m == nil {
			continue
		}
		if e, err := parseMarker(m[1]); err == nil && e.ID == id {
			start = i
		}
	}
	if start < 0 {
		return nil, Entry{}, false
	}
	block := strings.Join(lines[start:], "")
	return []byte(strings.Join(lines[:start], "")), firstEntry(block), true
}

func firstEntry(block string) Entry {
	if entries := parseLog([]byte(block)); len(entries) > 0 {
		return entries[0]
	}
	return Entry{}
}

// RunAdmin implements `guestbook entries …` for an operator on the host.
//
//	guestbook entries list
//	guestbook entries remove <id> [--identity alice.poweur.net] [--at 2026-10-01T19:23] [--yes]
//
// remove only says what it would do until --yes is given.
func RunAdmin(ctx context.Context, store Store, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: guestbook entries list | remove <id> [--identity ID] [--at TIME] [--yes]")
		return 2
	}
	switch args[0] {
	case "list":
		entries, err := ListEntries(ctx, store)
		if err != nil {
			fmt.Fprintln(stderr, "guestbook:", err)
			return 1
		}
		for _, e := range entries {
			fmt.Fprintf(stdout, "%s  %s  %-28s  %s\n", e.ID, e.At, e.Identity, oneLine(e.Message, 60))
		}
		return 0
	case "remove":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			fmt.Fprintln(stderr, "usage: guestbook entries remove <id> [--identity ID] [--at TIME] [--yes]")
			return 2
		}
		id := args[1]
		var guard Guard
		yes := false
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--yes":
				yes = true
			case "--identity", "--at":
				if i+1 >= len(args) {
					fmt.Fprintf(stderr, "guestbook: %s needs a value\n", args[i])
					return 2
				}
				if args[i] == "--identity" {
					guard.Identity = args[i+1]
				} else {
					guard.At = args[i+1]
				}
				i++
			default:
				fmt.Fprintf(stderr, "guestbook: unknown option %s\n", args[i])
				return 2
			}
		}
		e, err := RemoveEntry(ctx, store, id, guard, !yes)
		if err != nil {
			fmt.Fprintln(stderr, "guestbook:", err)
			return 1
		}
		verb := "would remove"
		if yes {
			verb = "removed"
		}
		fmt.Fprintf(stdout, "%s %s (%s, %s) from %s: %s\n", verb, e.ID, e.Identity, e.At, e.Page, oneLine(e.Message, 60))
		if !yes {
			fmt.Fprintln(stdout, "dry run: add --yes to remove it")
		}
		return 0
	}
	fmt.Fprintf(stderr, "guestbook: unknown command %q\n", args[0])
	return 2
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
