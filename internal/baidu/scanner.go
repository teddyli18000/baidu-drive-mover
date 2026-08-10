package baidu

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

const (
	shareListPageSize         = 100
	maxSharePagesPerDirectory = 100000
)

type scanDirectory struct {
	remotePath  string
	logicalPath string
	initial     bool
}

type shareListItem struct {
	FsID           int64  `json:"fs_id"`
	ServerFilename string `json:"server_filename"`
	Path           string `json:"path"`
	IsDir          int64  `json:"isdir"`
	Size           int64  `json:"size"`
	MD5            string `json:"md5"`
}

func (item *shareListItem) UnmarshalJSON(data []byte) error {
	var wire struct {
		FsID           json.RawMessage `json:"fs_id"`
		ServerFilename string          `json:"server_filename"`
		Path           string          `json:"path"`
		IsDir          json.RawMessage `json:"isdir"`
		Size           json.RawMessage `json:"size"`
		MD5            string          `json:"md5"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	fsID, err := parseJSONInt64(wire.FsID)
	if err != nil {
		return fmt.Errorf("invalid fs_id: %w", err)
	}
	isDir, err := parseJSONInt64(wire.IsDir)
	if err != nil {
		return fmt.Errorf("invalid isdir: %w", err)
	}
	if isDir != 0 && isDir != 1 {
		return fmt.Errorf("invalid isdir: expected 0 or 1, got %d", isDir)
	}
	size, err := parseJSONInt64(wire.Size)
	if err != nil {
		return fmt.Errorf("invalid size: %w", err)
	}
	if size < 0 {
		return fmt.Errorf("invalid size: expected a non-negative integer, got %d", size)
	}
	*item = shareListItem{
		FsID:           fsID,
		ServerFilename: wire.ServerFilename,
		Path:           wire.Path,
		IsDir:          isDir,
		Size:           size,
		MD5:            wire.MD5,
	}
	return nil
}

func parseJSONInt64(raw json.RawMessage) (int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, fmt.Errorf("value is missing or null")
	}

	var numeric int64
	if err := json.Unmarshal(trimmed, &numeric); err == nil {
		return numeric, nil
	}

	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return 0, fmt.Errorf("must be an integer or decimal string")
	}
	if text == "" {
		return 0, fmt.Errorf("decimal string is empty")
	}
	digits := text
	if digits[0] == '-' {
		digits = digits[1:]
	}
	if digits == "" {
		return 0, fmt.Errorf("decimal string contains no digits")
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("decimal string contains non-digit characters")
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decimal string is outside int64 range: %w", err)
	}
	return value, nil
}

type shareListResponse struct {
	Errno int             `json:"errno"`
	List  []shareListItem `json:"list"`

	listPresent bool
}

func (response *shareListResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Errno int             `json:"errno"`
		List  json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	response.Errno = wire.Errno
	listJSON := bytes.TrimSpace(wire.List)
	if len(listJSON) == 0 || bytes.Equal(listJSON, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(listJSON, &response.List); err != nil {
		return fmt.Errorf("invalid list: %w", err)
	}
	response.listPresent = true
	return nil
}

func (c *Client) Scan(ctx context.Context, taskID string, link ShareLink, share ShareContext, sink manifest.Sink) error {
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("scan task ID is empty")
	}
	if sink == nil {
		return fmt.Errorf("manifest sink is nil")
	}
	start := normalizeRemotePath(link.StartPath)
	queue := []scanDirectory{{remotePath: start, logicalPath: "/", initial: true}}
	queuedRemote := map[string]string{start: "/"}
	queuedLogical := map[string]string{"/": start}
	visited := make(map[string]bool)

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := queue[0]
		queue = queue[1:]
		if visited[current.remotePath] {
			continue
		}
		visited[current.remotePath] = true
		seenPages := make(map[[32]byte]int)
		initialRemoteParent := ""
		initialRemoteParentSet := false

		for pageNumber := 1; ; pageNumber++ {
			if pageNumber > maxSharePagesPerDirectory {
				return fmt.Errorf("share directory %q exceeded pagination safety ceiling", current.remotePath)
			}
			items, err := c.listSharePage(ctx, link, share, current.remotePath, pageNumber)
			if err != nil {
				return err
			}
			fingerprint := sharePageFingerprint(items)
			if previous, exists := seenPages[fingerprint]; exists {
				return fmt.Errorf("share directory %q pagination made no progress: page %d repeats page %d", current.remotePath, pageNumber, previous)
			}
			seenPages[fingerprint] = pageNumber

			directories := make([]manifest.Directory, 0)
			files := make([]manifest.File, 0)
			pagePaths := make(map[string]string, len(items))
			for _, item := range items {
				name, err := safeShareEntryName(item.ServerFilename)
				if err != nil {
					return err
				}
				logicalPath := normalizeRemotePath(path.Join(current.logicalPath, name))
				expectedFullPath := normalizeRemotePath(path.Join(current.remotePath, name))
				fullPath := expectedFullPath
				if strings.TrimSpace(item.Path) != "" {
					fullPath, err = canonicalRemoteItemPath(item.Path)
					if err != nil {
						return err
					}
				}
				if current.initial {
					if path.Base(fullPath) != name {
						return fmt.Errorf("Baidu returned root item path %q whose final component does not match %q", fullPath, name)
					}
					remoteParent := normalizeRemotePath(path.Dir(fullPath))
					if !initialRemoteParentSet {
						initialRemoteParent = remoteParent
						initialRemoteParentSet = true
					} else if remoteParent != initialRemoteParent {
						return fmt.Errorf("Baidu returned root items from inconsistent remote parents %q and %q", initialRemoteParent, remoteParent)
					}
				} else if fullPath != expectedFullPath {
					return fmt.Errorf("Baidu returned child path %q for %q outside expected parent %q", fullPath, name, current.remotePath)
				}
				kind := "file"
				if item.IsDir == 1 {
					kind = "directory"
				}
				if previousKind, exists := pagePaths[logicalPath]; exists {
					return fmt.Errorf("Baidu page contains duplicate logical path %q (%s and %s)", logicalPath, previousKind, kind)
				}
				pagePaths[logicalPath] = kind

				if item.IsDir == 1 {
					if fullPath == current.remotePath {
						return fmt.Errorf("Baidu directory %q resolved to its own parent", name)
					}
					if logicalPath != "/" {
						directories = append(directories, manifest.Directory{LogicalPath: logicalPath})
					}
					if previousLogical, exists := queuedRemote[fullPath]; exists {
						return fmt.Errorf("Baidu returned remote directory %q more than once for logical paths %q and %q", fullPath, previousLogical, logicalPath)
					}
					if previousRemote, exists := queuedLogical[logicalPath]; exists {
						return fmt.Errorf("Baidu returned logical directory %q more than once for remote paths %q and %q", logicalPath, previousRemote, fullPath)
					}
					queuedRemote[fullPath] = logicalPath
					queuedLogical[logicalPath] = fullPath
					queue = append(queue, scanDirectory{remotePath: fullPath, logicalPath: logicalPath})
					continue
				}
				if item.FsID <= 0 {
					return fmt.Errorf("Baidu returned file without valid fs_id at %q", logicalPath)
				}
				if item.Size < 0 {
					return fmt.Errorf("Baidu returned negative file size at %q", logicalPath)
				}
				parent := path.Dir(logicalPath)
				if parent == "." {
					parent = "/"
				}
				files = append(files, manifest.File{
					SourceID:    strconv.FormatInt(item.FsID, 10),
					SourcePath:  fullPath,
					LogicalPath: logicalPath,
					ParentPath:  parent,
					Name:        name,
					Size:        item.Size,
					MD5:         normalizeShareMD5(item.MD5),
				})
			}
			if len(directories) > 0 || len(files) > 0 {
				if err := sink.UpsertManifestPage(ctx, taskID, directories, files); err != nil {
					return fmt.Errorf("persist share manifest page: %w", err)
				}
			}
			if len(items) < shareListPageSize {
				break
			}
		}
	}
	return nil
}

func (c *Client) listSharePage(ctx context.Context, link ShareLink, share ShareContext, directory string, pageNumber int) ([]shareListItem, error) {
	for attempt := 0; attempt < c.maxListRetries; attempt++ {
		query := url.Values{}
		query.Set("bdstoken", share.BDSToken)
		if directory == "/" {
			query.Set("root", "1")
		} else {
			query.Set("root", "0")
		}
		query.Set("web", "5")
		query.Set("app_id", panAppID)
		query.Set("shorturl", link.ShortURL)
		query.Set("channel", "chunlei")
		query.Set("page", strconv.Itoa(pageNumber))
		query.Set("num", strconv.Itoa(shareListPageSize))
		form := url.Values{"dir": []string{directory}}
		body, status, err := c.doRead(ctx, http.MethodPost, "/share/list", query, form, link.SanitizedURL(), 8<<20)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 400 {
			return nil, fmt.Errorf("Baidu share listing returned HTTP %d", status)
		}
		var response shareListResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("parse Baidu share listing: %w", err)
		}
		switch response.Errno {
		case 0:
			if !response.listPresent {
				return nil, fmt.Errorf("parse Baidu share listing: successful response is missing a valid list array")
			}
			return response.List, nil
		case -9:
			return nil, ErrPasswordRequired
		case -6, -7:
			return nil, ErrAuthRequired
		case 8001, -62:
			return nil, ErrVerificationRequired
		case 4:
			if attempt+1 < c.maxListRetries {
				delay := 500 * time.Millisecond * time.Duration(1<<attempt)
				if err := c.sleep(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &RemoteError{Operation: "share listing after retries", Errno: response.Errno}
		default:
			return nil, &RemoteError{Operation: "share listing", Errno: response.Errno}
		}
	}
	return nil, fmt.Errorf("Baidu share listing retry loop exhausted")
}

func safeShareEntryName(raw string) (string, error) {
	name := raw
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("Baidu returned unsafe empty/dot filename %q", raw)
	}
	if strings.ContainsRune(name, '\x00') || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("Baidu returned unsafe path separator in filename %q", raw)
	}
	return name, nil
}

func normalizeShareMD5(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) != 32 {
		return ""
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return ""
		}
	}
	return strings.ToLower(value)
}

func canonicalRemoteItemPath(raw string) (string, error) {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\\') {
		return "", fmt.Errorf("Baidu returned unsafe non-absolute item path %q", raw)
	}
	clean := path.Clean(raw)
	if clean != raw {
		return "", fmt.Errorf("Baidu returned non-canonical item path %q", raw)
	}
	return clean, nil
}

func sharePageFingerprint(items []shareListItem) [32]byte {
	ordered := append([]shareListItem(nil), items...)
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if left.FsID != right.FsID {
			return left.FsID < right.FsID
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.ServerFilename != right.ServerFilename {
			return left.ServerFilename < right.ServerFilename
		}
		if left.IsDir != right.IsDir {
			return left.IsDir < right.IsDir
		}
		if left.Size != right.Size {
			return left.Size < right.Size
		}
		return left.MD5 < right.MD5
	})
	hash := sha256.New()
	var buffer [8]byte
	for _, item := range ordered {
		binary.LittleEndian.PutUint64(buffer[:], uint64(item.FsID))
		_, _ = hash.Write(buffer[:])
		binary.LittleEndian.PutUint64(buffer[:], uint64(item.Size))
		_, _ = hash.Write(buffer[:])
		binary.LittleEndian.PutUint64(buffer[:], uint64(item.IsDir))
		_, _ = hash.Write(buffer[:])
		for _, value := range []string{item.ServerFilename, item.Path, strings.TrimSpace(item.MD5)} {
			binary.LittleEndian.PutUint64(buffer[:], uint64(len(value)))
			_, _ = hash.Write(buffer[:])
			_, _ = hash.Write([]byte(value))
		}
	}
	var fingerprint [32]byte
	copy(fingerprint[:], hash.Sum(nil))
	return fingerprint
}
