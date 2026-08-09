package runtime

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ResolveTempRelative resolves a slash- or platform-separated relative path
// under the approved runtime temp root.
func (l *Layout) ResolveTempRelative(relative string) (string, error) {
	if l == nil {
		return "", errors.New("nil runtime layout")
	}
	relative = strings.TrimSpace(relative)
	if relative == "" {
		return "", errors.New("empty temp-relative path")
	}
	normalized := strings.ReplaceAll(relative, "\\", "/")
	if filepath.IsAbs(relative) || strings.HasPrefix(normalized, "/") || hasWindowsVolumePrefix(normalized) {
		return "", fmt.Errorf("absolute temp-relative path is not allowed: %q", relative)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("temp-relative path escapes root: %q", relative)
	}
	return l.JoinTemp(clean)
}

func hasWindowsVolumePrefix(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	first := value[0]
	return (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')
}

// EnsureTempDir creates a directory under temp after containment/symlink checks.
func (l *Layout) EnsureTempDir(relative string) (string, error) {
	full, err := l.ResolveTempRelative(relative)
	if err != nil {
		return "", err
	}
	if err := mkdirContained(l.Temp, full); err != nil {
		return "", err
	}
	return full, nil
}

// OpenTempFile opens a regular file below temp. Existing symlinks are refused.
// Parent directories must already exist.
func (l *Layout) OpenTempFile(relative string, flag int, perm fs.FileMode) (*os.File, error) {
	full, err := l.ResolveTempRelative(relative)
	if err != nil {
		return nil, err
	}
	if err := rejectExistingSymlinks(l.Temp, full); err != nil {
		return nil, err
	}
	if info, statErr := os.Lstat(full); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink temp file: %q", full)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("temp file path is a directory: %q", full)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect temp file %q: %w", full, statErr)
	}
	file, err := os.OpenFile(full, flag, perm)
	if err != nil {
		return nil, fmt.Errorf("open temp file %q: %w", full, err)
	}
	return file, nil
}

func (l *Layout) StatTempFile(relative string) (os.FileInfo, error) {
	full, err := l.ResolveTempRelative(relative)
	if err != nil {
		return nil, err
	}
	if err := rejectExistingSymlinks(l.Temp, full); err != nil {
		return nil, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("temp path is not a regular file: %q", full)
	}
	return info, nil
}

func (l *Layout) RemoveTempFile(relative string) error {
	full, err := l.ResolveTempRelative(relative)
	if err != nil {
		return err
	}
	if err := rejectExistingSymlinks(l.Temp, full); err != nil {
		return err
	}
	info, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect temp removal target: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to remove non-regular temp file: %q", full)
	}
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("remove temp file: %w", err)
	}
	return nil
}

func (l *Layout) RenameTempFile(fromRelative, toRelative string) error {
	from, err := l.ResolveTempRelative(fromRelative)
	if err != nil {
		return err
	}
	to, err := l.ResolveTempRelative(toRelative)
	if err != nil {
		return err
	}
	if err := rejectExistingSymlinks(l.Temp, from); err != nil {
		return err
	}
	if err := rejectExistingSymlinks(l.Temp, to); err != nil {
		return err
	}
	if filepath.Dir(from) != filepath.Dir(to) {
		return fmt.Errorf("temp rename must remain in one directory")
	}
	if info, err := os.Lstat(from); err != nil {
		return fmt.Errorf("inspect temp rename source: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("temp rename source is not a regular file")
	}
	if info, err := os.Lstat(to); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("temp rename destination is not a regular file")
		}
		if err := os.Remove(to); err != nil {
			return fmt.Errorf("replace temp rename destination: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect temp rename destination: %w", err)
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
