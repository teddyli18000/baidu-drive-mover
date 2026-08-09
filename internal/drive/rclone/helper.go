package rclone

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
)

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func SecureHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many rclone download redirects")
			}
			if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), "downloads.rclone.org") {
				return fmt.Errorf("refusing rclone download redirect to %q", req.URL.String())
			}
			return nil
		},
	}
}

func DownloadPinnedArchive(ctx context.Context, client HTTPDoer) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("rclone archive HTTP client is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ArchiveURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build rclone archive request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download pinned rclone archive: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("download pinned rclone archive returned nil response")
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil {
		if resp.Request.URL.Scheme != "https" || !strings.EqualFold(resp.Request.URL.Hostname(), "downloads.rclone.org") {
			return nil, fmt.Errorf("rclone archive response escaped official origin: %q", resp.Request.URL.String())
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rclone archive returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxArchiveBytes {
		return nil, fmt.Errorf("rclone archive exceeds size limit: %d", resp.ContentLength)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read rclone archive: %w", err)
	}
	if int64(len(data)) > maxArchiveBytes {
		return nil, fmt.Errorf("rclone archive exceeds size limit")
	}
	return data, nil
}

func VerifyPinnedArchive(data []byte) error {
	if actual := sha256Hex(data); !strings.EqualFold(actual, ArchiveSHA256) {
		return fmt.Errorf("rclone archive SHA-256 mismatch: got %s", actual)
	}
	return nil
}

func InstallPinnedArchive(layout *runtimepath.Layout, data []byte) error {
	return installVerifiedArchive(layout, data, ArchiveSHA256, ExecutableSHA256, ExecutableEntry)
}

func installVerifiedArchive(layout *runtimepath.Layout, data []byte, expectedArchiveHash, expectedExeHash, expectedEntry string) error {
	if layout == nil {
		return fmt.Errorf("runtime layout is nil")
	}
	if actual := sha256Hex(data); !strings.EqualFold(actual, expectedArchiveHash) {
		return fmt.Errorf("rclone archive SHA-256 mismatch: got %s", actual)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("parse rclone archive: %w", err)
	}
	var executable []byte
	matches := 0
	for _, entry := range reader.File {
		if !safeArchiveEntry(entry.Name) {
			return fmt.Errorf("unsafe path in rclone archive: %q", entry.Name)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink entry is not allowed in rclone archive: %q", entry.Name)
		}
		if strings.ReplaceAll(entry.Name, "\\", "/") != expectedEntry {
			continue
		}
		matches++
		if entry.FileInfo().IsDir() {
			return fmt.Errorf("expected rclone executable entry is a directory")
		}
		if entry.UncompressedSize64 > uint64(maxExecutableBytes) {
			return fmt.Errorf("rclone executable exceeds size limit")
		}
		rc, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open rclone executable entry: %w", err)
		}
		contents, readErr := io.ReadAll(io.LimitReader(rc, maxExecutableBytes+1))
		closeErr := rc.Close()
		if readErr != nil {
			return fmt.Errorf("read rclone executable entry: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close rclone executable entry: %w", closeErr)
		}
		if int64(len(contents)) > maxExecutableBytes {
			return fmt.Errorf("rclone executable exceeds size limit")
		}
		executable = contents
	}
	if matches != 1 {
		return fmt.Errorf("rclone archive contains %d expected executable entries", matches)
	}
	if actual := sha256Hex(executable); !strings.EqualFold(actual, expectedExeHash) {
		return fmt.Errorf("rclone executable SHA-256 mismatch: got %s", actual)
	}
	if _, err := layout.EnsureTempDir("tools/rclone"); err != nil {
		return err
	}
	file, err := layout.OpenTempFile("tools/rclone/rclone.exe.new", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	writeErr := func() error {
		if _, err := file.Write(executable); err != nil {
			return err
		}
		return file.Sync()
	}()
	closeErr := file.Close()
	if writeErr != nil {
		_ = layout.RemoveTempFile("tools/rclone/rclone.exe.new")
		return fmt.Errorf("write verified rclone executable: %w", writeErr)
	}
	if closeErr != nil {
		_ = layout.RemoveTempFile("tools/rclone/rclone.exe.new")
		return fmt.Errorf("close verified rclone executable: %w", closeErr)
	}
	if err := layout.RenameTempFile("tools/rclone/rclone.exe.new", "tools/rclone/rclone.exe"); err != nil {
		_ = layout.RemoveTempFile("tools/rclone/rclone.exe.new")
		return err
	}
	return VerifyInstalledExecutable(layout)
}

func VerifyInstalledExecutable(layout *runtimepath.Layout) error {
	if layout == nil {
		return fmt.Errorf("runtime layout is nil")
	}
	file, err := layout.OpenTempFile("tools/rclone/rclone.exe", os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open installed rclone helper: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, io.LimitReader(file, maxExecutableBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("hash installed rclone helper: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close installed rclone helper: %w", closeErr)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, ExecutableSHA256) {
		return fmt.Errorf("installed rclone executable SHA-256 mismatch: got %s", actual)
	}
	return nil
}

func Provision(ctx context.Context, layout *runtimepath.Layout, runner Runner, client HTTPDoer) error {
	if layout == nil {
		return fmt.Errorf("runtime layout is nil")
	}
	if err := layout.Ensure(); err != nil {
		return err
	}
	if err := VerifyInstalledExecutable(layout); err != nil {
		archive, downloadErr := DownloadPinnedArchive(ctx, client)
		if downloadErr != nil {
			return downloadErr
		}
		if installErr := InstallPinnedArchive(layout, archive); installErr != nil {
			return installErr
		}
	}
	clientWrapper, err := NewClient(layout, runner)
	if err != nil {
		return err
	}
	return clientWrapper.CheckVersion(ctx)
}

func safeArchiveEntry(name string) bool {
	normalized := strings.ReplaceAll(name, "\\", "/")
	if normalized == "" || strings.HasPrefix(normalized, "/") {
		return false
	}
	if len(normalized) >= 2 && normalized[1] == ':' {
		return false
	}
	trimmed := strings.TrimSuffix(normalized, "/")
	if trimmed == "" {
		return false
	}
	clean := path.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return clean == trimmed
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
