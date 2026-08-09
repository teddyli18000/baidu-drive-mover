package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCreatesOnlyTempTree(t *testing.T) {
	base := t.TempDir()
	layout, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{layout.Temp, layout.Auth, layout.ChromeProfile, layout.Cache, layout.Logs, layout.Tasks, layout.Tools} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected directory: %s", p)
		}
		if !isWithin(layout.Temp, p, true) {
			t.Fatalf("created path outside temp: %s", p)
		}
	}
}

func TestJoinTempRejectsTraversalAndAbsolutePath(t *testing.T) {
	layout, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := layout.JoinTemp("..", "escape"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := layout.JoinTemp(filepath.VolumeName(layout.Temp)+string(filepath.Separator), "escape"); err == nil {
		t.Fatal("expected absolute path to be rejected")
	}
}

func TestSafeRemoveAllRefusesRootAndOutside(t *testing.T) {
	base := t.TempDir()
	layout, _ := New(base)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := layout.SafeRemoveAll(layout.Temp); err == nil {
		t.Fatal("expected temp root deletion refusal")
	}
	if err := layout.SafeRemoveAll(base); err == nil {
		t.Fatal("expected outside deletion refusal")
	}

	target, err := layout.JoinTemp("tasks", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := layout.SafeRemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected target removed, got %v", err)
	}
}

func TestJoinTempRejectsExistingSymlink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows symlink creation may require developer mode; covered by Windows containment tests without privilege assumptions")
	}
	base := t.TempDir()
	layout, _ := New(base)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(layout.Temp, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.JoinTemp("link", "file"); err == nil {
		t.Fatal("expected symlink path rejection")
	}
}
