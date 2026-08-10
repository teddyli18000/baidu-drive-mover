package runtime

import (
	"errors"
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
	for _, p := range []string{
		layout.Temp, layout.Auth, layout.ChromeProfile, layout.BrowserTemp, layout.BrowserCache,
		layout.Cache, layout.Logs, layout.Tasks, layout.Tools, layout.RcloneCache, layout.RcloneTemp, layout.RcloneToolDir,
	} {
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
	for _, p := range []string{layout.RcloneExe, layout.RcloneConfig} {
		if !isWithin(layout.Temp, p, false) {
			t.Fatalf("rclone runtime file path escapes temp: %s", p)
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

func TestRemoveRuntimeRootRemovesEverythingAndIsIdempotent(t *testing.T) {
	base := t.TempDir()
	layout, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	child, err := layout.JoinTemp("cache", "task", "payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(child), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := layout.RemoveRuntimeRoot(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(layout.Temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected runtime temp root removed, got %v", err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("expected runtime base retained, got %v", err)
	}
	if err := layout.RemoveRuntimeRoot(); err != nil {
		t.Fatalf("missing runtime root should be idempotent: %v", err)
	}
}

func TestRemoveRuntimeRootRefusesTamperedTempPath(t *testing.T) {
	base := t.TempDir()
	layout, err := New(base)
	if err != nil {
		t.Fatal(err)
	}

	tampered := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(tampered, 0o700); err != nil {
		t.Fatal(err)
	}
	layout.Temp = tampered
	if err := layout.RemoveRuntimeRoot(); err == nil {
		t.Fatal("expected tampered temp path to be rejected")
	}
	if _, err := os.Stat(tampered); err != nil {
		t.Fatalf("tampered path should remain untouched: %v", err)
	}
}

func TestRemoveRuntimeRootRejectsSymlinkRootAndDescendants(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows symlink creation may require developer mode")
	}

	t.Run("root", func(t *testing.T) {
		base := t.TempDir()
		layout, err := New(base)
		if err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.Symlink(outside, layout.Temp); err != nil {
			t.Fatal(err)
		}
		if err := layout.RemoveRuntimeRoot(); err == nil {
			t.Fatal("expected symlink temp root to be rejected")
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("symlink target should remain untouched: %v", err)
		}
	})

	t.Run("descendant", func(t *testing.T) {
		base := t.TempDir()
		layout, err := New(base)
		if err != nil {
			t.Fatal(err)
		}
		if err := layout.Ensure(); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		link := filepath.Join(layout.Temp, "link")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if err := layout.RemoveRuntimeRoot(); err == nil {
			t.Fatal("expected symlink descendant to be rejected")
		}
		if _, err := os.Stat(layout.Temp); err != nil {
			t.Fatalf("runtime temp root should remain after refusal: %v", err)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Fatalf("symlink descendant should remain after refusal: %v", err)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("symlink target should remain untouched: %v", err)
		}
	})
}
