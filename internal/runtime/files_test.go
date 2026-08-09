package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTempFileOperationsStayContained(t *testing.T) {
	layout, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.EnsureTempDir("cache/task-safe"); err != nil {
		t.Fatal(err)
	}
	file, err := layout.OpenTempFile("cache/task-safe/file.part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("hello"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := layout.StatTempFile("cache/task-safe/file.part")
	if err != nil || info.Size() != 5 {
		t.Fatalf("stat size=%v err=%v", info, err)
	}
	if err := layout.RenameTempFile("cache/task-safe/file.part", "cache/task-safe/file.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.StatTempFile("cache/task-safe/file.bin"); err != nil {
		t.Fatal(err)
	}
	if err := layout.RemoveTempFile("cache/task-safe/file.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.StatTempFile("cache/task-safe/file.bin"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected removed file, got %v", err)
	}
}

func TestResolveTempRelativeRejectsEscapes(t *testing.T) {
	layout, _ := New(t.TempDir())
	for _, bad := range []string{"../escape", "../../escape", filepath.Join(string(filepath.Separator), "absolute")} {
		if _, err := layout.ResolveTempRelative(bad); err == nil {
			t.Fatalf("expected %q rejected", bad)
		}
	}
}

func TestOpenTempFileRejectsSymlink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows symlink creation may require developer mode")
	}
	layout, _ := New(t.TempDir())
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.EnsureTempDir("cache/task"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	full, _ := layout.ResolveTempRelative("cache/task/link.part")
	if err := os.Symlink(outside, full); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.OpenTempFile("cache/task/link.part", os.O_WRONLY, 0o600); err == nil {
		t.Fatal("expected symlink file rejection")
	}
}
