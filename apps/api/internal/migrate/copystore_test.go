package migrate

import (
	"context"
	"testing"

	"github.com/poweur/api/internal/drive/engine"
	"github.com/poweur/api/internal/drive/provider/fs"
	"github.com/poweur/api/internal/storage"
)

func TestCopyStore(t *testing.T) {
	ctx := context.Background()
	root, _ := v1Tree(t)
	if _, err := Run(ctx, Options{DataDir: root}); err != nil {
		t.Fatal(err)
	}
	from, _ := fs.Open(root)
	to, _ := fs.Open(t.TempDir())

	dry, err := CopyStore(ctx, from, to, true, nil)
	if err != nil || dry.Copied == 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if page, _ := to.List(ctx, "", "", 10); len(page.Objects) != 0 {
		t.Fatal("dry run wrote objects")
	}
	report, err := CopyStore(ctx, from, to, false, nil)
	if err != nil || report.Copied != dry.Copied || report.Skipped != 0 {
		t.Fatalf("copy: %+v %v", report, err)
	}
	// The copy serves the same drive, identity index and spool.
	a, b := engine.New(engine.Options{Store: from}), engine.New(engine.Options{Store: to})
	ca, _, _ := a.Changes(ctx, "alice.poweur.net", 0, 0)
	cb, _, err := b.Changes(ctx, "alice.poweur.net", 0, 0)
	if err != nil || len(ca) != len(cb) || len(ca) == 0 {
		t.Fatalf("changes: %d vs %d %v", len(ca), len(cb), err)
	}
	if raw, _, err := b.SystemRead(ctx, "alice.poweur.net", ".poweur/public/profile.json"); err != nil || len(raw) == 0 {
		t.Fatalf("profile on the copy: %v", err)
	}
	ids, _ := storage.OpenIdentityStore(to)
	inbox, _ := storage.OpenInboxStore(to)
	if pending, _ := inbox.Since("alice.poweur.net", ""); !ids.Exists("alice.poweur.net") || len(pending) != 1 {
		t.Fatal("relay registries not copied")
	}
	// Running again copies nothing.
	again, err := CopyStore(ctx, from, to, false, nil)
	if err != nil || again.Copied != 0 || again.Skipped != report.Copied {
		t.Fatalf("rerun: %+v %v", again, err)
	}
}
