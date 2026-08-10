package baidu

import (
	"testing"

	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
)

func TestRuntimeCookieStoreUsesContainedPath(t *testing.T) {
	layout, err := runtimepath.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	store := NewCookieStore(layout)
	if err := store.Save(" BDUSS=fake; STOKEN=fake \n"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if value != "BDUSS=fake; STOKEN=fake" {
		t.Fatalf("cookie value=%q", value)
	}

	escape := CookieStore{Layout: layout, Relative: "../outside.cookies"}
	if err := escape.Save("secret"); err == nil {
		t.Fatal("expected traversal cookie path to be rejected")
	}
}
