package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const tempDirName = "temp"

// Layout owns every persistent/runtime path used by BaiduDriveMover.
// Nothing outside Temp is writable by the application.
type Layout struct {
	Base          string
	Temp          string
	StateDB       string
	Config        string
	Auth          string
	ChromeProfile string
	BrowserTemp   string
	BrowserCache  string
	Cache         string
	Logs          string
	Tasks         string
	Tools         string
	RcloneCache   string
	RcloneTemp    string
	RcloneToolDir string
	RcloneExe     string
	RcloneConfig  string
}

// FromExecutable builds the runtime layout beside the running executable.
func FromExecutable() (*Layout, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil, fmt.Errorf("normalize executable path: %w", err)
	}
	return New(filepath.Dir(exe))
}

// New builds a layout rooted at base. It is primarily useful for tests.
func New(base string) (*Layout, error) {
	if strings.TrimSpace(base) == "" {
		return nil, errors.New("runtime base path is empty")
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, fmt.Errorf("normalize runtime base: %w", err)
	}
	abs = filepath.Clean(abs)
	temp := filepath.Join(abs, tempDirName)
	auth := filepath.Join(temp, "auth")
	tools := filepath.Join(temp, "tools")
	rcloneToolDir := filepath.Join(tools, "rclone")
	return &Layout{
		Base:          abs,
		Temp:          temp,
		StateDB:       filepath.Join(temp, "state.db"),
		Config:        filepath.Join(temp, "config.json"),
		Auth:          auth,
		ChromeProfile: filepath.Join(temp, "chrome-profile"),
		BrowserTemp:   filepath.Join(temp, "browser-tmp"),
		BrowserCache:  filepath.Join(temp, "browser-cache"),
		Cache:         filepath.Join(temp, "cache"),
		Logs:          filepath.Join(temp, "logs"),
		Tasks:         filepath.Join(temp, "tasks"),
		Tools:         tools,
		RcloneCache:   filepath.Join(temp, "rclone-cache"),
		RcloneTemp:    filepath.Join(temp, "rclone-tmp"),
		RcloneToolDir: rcloneToolDir,
		RcloneExe:     filepath.Join(rcloneToolDir, "rclone.exe"),
		RcloneConfig:  filepath.Join(auth, "rclone.conf"),
	}, nil
}

// Ensure creates only the approved runtime directories below ./temp.
func (l *Layout) Ensure() error {
	if l == nil {
		return errors.New("nil runtime layout")
	}
	if err := l.validateTempLocation(); err != nil {
		return err
	}
	for _, dir := range []string{
		l.Temp, l.Auth, l.ChromeProfile, l.BrowserTemp, l.BrowserCache,
		l.Cache, l.Logs, l.Tasks, l.Tools, l.RcloneCache, l.RcloneTemp, l.RcloneToolDir,
	} {
		if err := mkdirContained(l.Temp, dir); err != nil {
			return err
		}
	}
	return nil
}

func (l *Layout) validateTempLocation() error {
	expected := filepath.Clean(filepath.Join(l.Base, tempDirName))
	if !samePath(expected, filepath.Clean(l.Temp)) {
		return fmt.Errorf("runtime temp path must be %q, got %q", expected, l.Temp)
	}
	return nil
}

// JoinTemp returns a path below temp and rejects traversal or absolute escapes.
func (l *Layout) JoinTemp(parts ...string) (string, error) {
	if l == nil {
		return "", errors.New("nil runtime layout")
	}
	candidate := l.Temp
	for _, part := range parts {
		if part == "" {
			continue
		}
		if filepath.IsAbs(part) {
			return "", fmt.Errorf("absolute path component is not allowed: %q", part)
		}
		candidate = filepath.Join(candidate, part)
	}
	candidate = filepath.Clean(candidate)
	if !isWithin(l.Temp, candidate, true) {
		return "", fmt.Errorf("path escapes runtime temp root: %q", candidate)
	}
	if err := rejectExistingSymlinks(l.Temp, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

// SafeRemoveAll removes a registered temp descendant. It refuses to delete
// the temp root itself and rejects symlink-based escapes.
func (l *Layout) SafeRemoveAll(target string) error {
	if l == nil {
		return errors.New("nil runtime layout")
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("normalize delete target: %w", err)
	}
	target = filepath.Clean(target)
	if samePath(target, l.Temp) || !isWithin(l.Temp, target, false) {
		return fmt.Errorf("refusing to delete path outside temp descendants: %q", target)
	}
	if err := rejectExistingSymlinks(l.Temp, target); err != nil {
		return err
	}
	return os.RemoveAll(target)
}

// RemoveRuntimeRoot removes the complete tool-owned runtime root after a
// successful run. The root itself is never passed to SafeRemoveAll: every
// immediate child is validated and removed first, then the now-empty temp
// directory is removed with an exact os.Remove call. This keeps a concurrent
// or unexpected new entry fail-closed instead of broadening the deletion
// target.
func (l *Layout) RemoveRuntimeRoot() error {
	if l == nil {
		return errors.New("nil runtime layout")
	}
	if err := l.validateTempLocation(); err != nil {
		return err
	}

	temp, err := filepath.Abs(filepath.Clean(l.Temp))
	if err != nil {
		return fmt.Errorf("normalize runtime temp path: %w", err)
	}

	info, err := os.Lstat(temp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runtime temp root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime temp root is a symlink: %q", temp)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime temp root is not a directory: %q", temp)
	}

	// Walk before deleting anything so a pre-existing symlink anywhere below
	// temp leaves the complete runtime tree untouched.
	if err := rejectSymlinkDescendants(temp); err != nil {
		return err
	}

	entries, err := os.ReadDir(temp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enumerate runtime temp root: %w", err)
	}
	for _, entry := range entries {
		child := filepath.Join(temp, entry.Name())
		if err := l.SafeRemoveAll(child); err != nil {
			return fmt.Errorf("remove runtime temp child %q: %w", entry.Name(), err)
		}
	}

	// Re-check the root immediately before removing it. SafeRemoveAll checks
	// the same containment invariant for each child; this check prevents a
	// root replacement (including a symlink) from being accepted at the final
	// boundary.
	info, err = os.Lstat(temp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("re-inspect runtime temp root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime temp root became a symlink: %q", temp)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime temp root is no longer a directory: %q", temp)
	}
	if err := os.Remove(temp); err != nil {
		return fmt.Errorf("remove runtime temp root: %w", err)
	}
	return nil
}

func rejectSymlinkDescendants(root string) error {
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect runtime path %q: %w", path, walkErr)
		}
		if path == root {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed in runtime path: %q", path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func mkdirContained(root, dir string) error {
	if !isWithin(root, dir, true) {
		return fmt.Errorf("directory escapes runtime temp root: %q", dir)
	}
	if err := rejectExistingSymlinks(root, dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime directory %q: %w", dir, err)
	}
	return nil
}

func isWithin(root, target string, allowRoot bool) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rootAbs = filepath.Clean(rootAbs)
	targetAbs = filepath.Clean(targetAbs)
	if samePath(rootAbs, targetAbs) {
		return allowRoot
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return allowRoot
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func rejectExistingSymlinks(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if !isWithin(rootAbs, targetAbs, true) {
		return fmt.Errorf("path escapes runtime temp root: %q", targetAbs)
	}

	if info, err := os.Lstat(rootAbs); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime temp root is a symlink: %q", rootAbs)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect runtime temp root: %w", err)
	}

	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == "." {
		return err
	}
	current := rootAbs
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return fmt.Errorf("inspect runtime path %q: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed in runtime path: %q", current)
		}
	}
	return nil
}

func samePath(a, b string) bool {
	if os.PathSeparator == '\\' {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
