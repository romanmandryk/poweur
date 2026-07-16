package files

import "testing"

func TestCleanPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"/", "", false},
		{"/private/notes.txt", "private/notes.txt", false},
		{"private//notes.txt", "private/notes.txt", false},
		{"/private/../../../etc/passwd", "etc/passwd", false}, // Clean collapses; root check happens in ValidateTreePath
		{"/private/a\x00b", "", true},
		{"/private/" + string(rune(0x07)), "", true},
		{"/private/.poweur-evil", "", true},
		{"/public/.poweur-web-public", "public/.poweur-web-public", false},
	}
	for _, c := range cases {
		got, err := CleanPath(c.in)
		if c.wantErr != (err != nil) {
			t.Fatalf("CleanPath(%q) err=%v want err=%v", c.in, err, c.wantErr)
		}
		if err == nil && got != c.want {
			t.Fatalf("CleanPath(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestValidateTreePathRoots(t *testing.T) {
	if _, err := ValidateTreePath("/etc/passwd"); err == nil {
		t.Fatal("unknown top-level root must be rejected")
	}
	if _, err := ValidateTreePath("/private/ok.txt"); err != nil {
		t.Fatalf("valid root rejected: %v", err)
	}
	if p, err := ValidateTreePath("/"); err != nil || p != "" {
		t.Fatalf("root path: %q %v", p, err)
	}
}

func TestCleanPathLimits(t *testing.T) {
	long := "private/"
	for i := 0; i < MaxDepth+1; i++ {
		long += "d/"
	}
	if _, err := CleanPath(long); err == nil {
		t.Fatal("over-deep path must be rejected")
	}
	seg := make([]byte, MaxSegmentBytes+1)
	for i := range seg {
		seg[i] = 'a'
	}
	if _, err := CleanPath("private/" + string(seg)); err == nil {
		t.Fatal("over-long segment must be rejected")
	}
}

func TestParseScope(t *testing.T) {
	sc, err := ParseScope("dav:rw:/apps/net.poweur.tasks/")
	if err != nil {
		t.Fatalf("scope parse: %v", err)
	}
	if !sc.Allows("apps/net.poweur.tasks/todo.json", AccessWrite) {
		t.Fatal("path-scoped rw must allow writes inside prefix")
	}
	if sc.Allows("private/other.txt", AccessRead) {
		t.Fatal("path-scoped token must not reach other roots")
	}
	if _, err := ParseScope("dav:everything"); err == nil {
		t.Fatal("unknown scope must be rejected")
	}
	ro, _ := ParseScope("dav:read")
	if ro.Allows("private/x", AccessWrite) {
		t.Fatal("read scope must not allow writes")
	}
}
