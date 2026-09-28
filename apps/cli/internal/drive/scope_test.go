package drive

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	protocol "github.com/poweur/identity/drive"
)

func TestScope(t *testing.T) {
	const root, child, picked, nested, outside = "root", "child", "picked", "nested", "outside"
	c, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		folder := ""
		switch id {
		case child:
			folder = root
		case nested:
			folder = picked
		case outside:
			folder = ""
		default:
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"id":"%s","folder":"%s"}`, id, folder)
	})
	scope := (&Files{Client: c}).Scope(&File{Manifest: protocol.Manifest{Node: root}}, &File{Manifest: protocol.Manifest{Node: picked}})
	ctx := context.Background()
	if err := scope.allow(ctx, child); err != nil {
		t.Fatal(err)
	}
	if err := scope.allow(ctx, nested); err != nil {
		t.Fatal(err)
	}
	if err := scope.allow(ctx, outside); err == nil {
		t.Fatal("node outside the handle was allowed")
	}
	if err := scope.Move(ctx, &File{Manifest: protocol.Manifest{Node: root}}, &File{Manifest: protocol.Manifest{Node: child}}, "name"); err == nil || !strings.Contains(err.Error(), "cannot be moved") {
		t.Fatalf("scope root move: %v", err)
	}
}
