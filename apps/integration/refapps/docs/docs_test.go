package docs

import (
	"encoding/json"
	"strings"
	"testing"
)

const base = "# Plan\n\nIntro paragraph.\n\nMiddle paragraph.\n\nClosing paragraph.\n"

func TestMergeCombinesDifferentParagraphs(t *testing.T) {
	ours := strings.Replace(base, "Intro paragraph.", "Intro, edited by alice.", 1)
	theirs := strings.Replace(base, "Closing paragraph.", "Closing, edited by bob.", 1)
	merged, conflicts := Merge(base, ours, theirs, "alice", "bob")
	if conflicts != 0 || merged != "# Plan\n\nIntro, edited by alice.\n\nMiddle paragraph.\n\nClosing, edited by bob.\n" {
		t.Fatalf("conflicts=%d merged=%q", conflicts, merged)
	}
}

func TestMergeAdjacentParagraphs(t *testing.T) {
	ours := strings.Replace(base, "Middle paragraph.", "Middle by alice.", 1)
	theirs := strings.Replace(base, "Closing paragraph.", "Closing by bob.", 1)
	merged, conflicts := Merge(base, ours, theirs, "alice", "bob")
	if conflicts != 0 || merged != "# Plan\n\nIntro paragraph.\n\nMiddle by alice.\n\nClosing by bob.\n" {
		t.Fatalf("conflicts=%d merged=%q", conflicts, merged)
	}
}

func TestMergeInsertionsAndDeletions(t *testing.T) {
	ours := base + "\nNew ending by alice.\n"
	theirs := strings.Replace(base, "Middle paragraph.\n\n", "", 1)
	merged, conflicts := Merge(base, ours, theirs, "alice", "bob")
	if conflicts != 0 || merged != "# Plan\n\nIntro paragraph.\n\nClosing paragraph.\n\nNew ending by alice.\n" {
		t.Fatalf("conflicts=%d merged=%q", conflicts, merged)
	}
	// Both sides making the same change is not a conflict.
	same := strings.Replace(base, "Middle", "Centre", 1)
	if merged, conflicts := Merge(base, same, same, "a", "b"); conflicts != 0 || merged != same {
		t.Fatalf("identical edits: %d %q", conflicts, merged)
	}
}

func TestMergeSameParagraphKeepsBoth(t *testing.T) {
	ours := strings.Replace(base, "Middle paragraph.", "Middle by alice.", 1)
	theirs := strings.Replace(base, "Middle paragraph.", "Middle by bob.", 1)
	merged, conflicts := Merge(base, ours, theirs, "alice", "bob")
	if conflicts != 1 {
		t.Fatalf("conflicts = %d", conflicts)
	}
	for _, want := range []string{"<<<<<<< alice\nMiddle by alice.\n=======\nMiddle by bob.\n>>>>>>> bob", "Intro paragraph.", "Closing paragraph."} {
		if !strings.Contains(merged, want) {
			t.Fatalf("merged lost %q:\n%s", want, merged)
		}
	}
}

func record(t *testing.T, r Record) string {
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestReduceComments(t *testing.T) {
	first, _ := NewComment("Intro paragraph.", "Needs a date")
	second, _ := NewComment("Closing paragraph.", "Typo here")
	entries := []Entry{
		{Author: "carol", Text: record(t, first)},
		{Author: "carol", Text: record(t, second)},
		{Author: "carol", Text: record(t, first)},                    // duplicate id: ignored
		{Author: "mallory", Text: record(t, ResolveComment(first.ID))}, // not the author or owner
		{Author: "alice", Text: record(t, ResolveComment(first.ID))},   // the owner
		{Author: "carol", Text: record(t, ResolveComment(second.ID))},  // the author
		{Author: "carol", Text: "not json"},
	}
	comments := Reduce("alice", entries)
	if len(comments) != 2 {
		t.Fatalf("comments = %+v", comments)
	}
	if !comments[0].Resolved || comments[0].ResolvedBy != "alice" || !comments[1].Resolved || comments[1].ResolvedBy != "carol" {
		t.Fatalf("resolution: %+v", comments)
	}
	if _, err := NewComment("x", " "); err == nil {
		t.Fatal("empty comment accepted")
	}
}

func TestPaths(t *testing.T) {
	folder, doc, comments, assets := Paths("Roadmap")
	if folder != "/Docs/Roadmap" || doc != "/Docs/Roadmap/doc.md" || comments != "/Docs/Roadmap/comments.jsonl" || assets != "/Docs/Roadmap/assets" {
		t.Fatal(folder, doc, comments, assets)
	}
}
