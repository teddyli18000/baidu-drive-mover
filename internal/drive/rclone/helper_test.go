package rclone

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
)

func TestInstallPinnedArchiveRejectsHashMismatchBeforeWrite(t *testing.T) {
	layout := newRcloneTestLayout(t)
	if err := InstallPinnedArchive(layout, []byte("not the pinned archive")); err == nil {
		t.Fatal("expected archive hash mismatch")
	}
	if _, err := os.Stat(layout.RcloneExe); !os.IsNotExist(err) {
		t.Fatalf("helper should not be written after archive mismatch, stat err=%v", err)
	}
}

func TestInstallVerifiedArchiveRejectsZipSlip(t *testing.T) {
	layout := newRcloneTestLayout(t)
	exe := []byte("synthetic executable")
	archive := makeTestZip(t, []zipFixture{
		{Name: "../escape.txt", Data: []byte("escape")},
		{Name: "synthetic/rclone.exe", Data: exe},
	})
	if err := installVerifiedArchive(layout, archive, sha256Hex(archive), sha256Hex(exe), "synthetic/rclone.exe"); err == nil {
		t.Fatal("expected zip-slip entry rejection")
	}
	if _, err := os.Stat(layout.RcloneExe); !os.IsNotExist(err) {
		t.Fatalf("helper should not be written after zip-slip rejection, stat err=%v", err)
	}
}

func TestInstallVerifiedArchiveWritesOnlyVerifiedExecutable(t *testing.T) {
	layout := newRcloneTestLayout(t)
	exe := []byte("synthetic verified executable")
	archive := makeTestZip(t, []zipFixture{
		{Name: "synthetic/README.txt", Data: []byte("ignored")},
		{Name: "synthetic/rclone.exe", Data: exe},
	})
	if err := installVerifiedArchive(layout, archive, sha256Hex(archive), sha256Hex(exe), "synthetic/rclone.exe"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(layout.RcloneExe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, exe) {
		t.Fatalf("installed helper differs: got=%q want=%q", got, exe)
	}
	if err := verifyInstalledExecutableHash(layout, sha256Hex(exe)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(layout.RcloneToolDir, "README.txt")); !os.IsNotExist(err) {
		t.Fatalf("non-executable archive entry must not be extracted, stat err=%v", err)
	}
}

func TestSafeArchiveEntryRejectsWindowsAndPOSIXEscapes(t *testing.T) {
	bad := []string{"../evil", "a/../../evil", "/absolute", `C:\\evil`, `..\\evil`, "a/../evil"}
	for _, value := range bad {
		if safeArchiveEntry(value) {
			t.Fatalf("unsafe archive entry accepted: %q", value)
		}
	}
	good := []string{"rclone-v1/rclone.exe", "rclone-v1/README.txt", "rclone-v1/"}
	for _, value := range good {
		if !safeArchiveEntry(value) {
			t.Fatalf("safe archive entry rejected: %q", value)
		}
	}
}

func TestDownloadPinnedArchiveUsesFixedOfficialURLAndRejectsEscapedResponse(t *testing.T) {
	client := doerFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != ArchiveURL {
			t.Fatalf("request URL=%q want=%q", req.URL.String(), ArchiveURL)
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader([]byte("archive"))),
			ContentLength: 7,
			Request:       req,
		}, nil
	})
	got, err := DownloadPinnedArchive(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "archive" {
		t.Fatalf("downloaded %q", got)
	}

	escaped := doerFunc(func(req *http.Request) (*http.Response, error) {
		other, _ := http.NewRequest(http.MethodGet, "https://example.invalid/rclone.zip", nil)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil)), Request: other}, nil
	})
	if _, err := DownloadPinnedArchive(context.Background(), escaped); err == nil {
		t.Fatal("expected escaped final download origin rejection")
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

type zipFixture struct {
	Name string
	Data []byte
}

func makeTestZip(t *testing.T, fixtures []zipFixture) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, fixture := range fixtures {
		entry, err := writer.Create(fixture.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(fixture.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func newRcloneTestLayout(t *testing.T) *runtimepath.Layout {
	t.Helper()
	layout, err := runtimepath.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	return layout
}
