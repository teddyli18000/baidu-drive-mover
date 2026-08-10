package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

const ownCopyMaxItems = 100

var errOwnedSourcesResolved = errors.New("owned Baidu source paths resolved")

type ownedSourceFile struct {
	FsID int64
	Path string
	Name string
}

type ownedSourceResolver struct {
	wanted   map[int64]struct{}
	resolved map[int64]ownedSourceFile
}

func (r *ownedSourceResolver) UpsertManifestPage(_ context.Context, _ string, _ []manifest.Directory, files []manifest.File) error {
	for _, file := range files {
		fsID, err := strconv.ParseInt(file.SourceID, 10, 64)
		if err != nil || fsID <= 0 {
			return fmt.Errorf("resolve owned Baidu source with invalid fs_id")
		}
		if _, wanted := r.wanted[fsID]; !wanted {
			continue
		}
		if file.SourcePath == "" || file.Name == "" {
			return fmt.Errorf("resolve owned Baidu source %d without a safe path", fsID)
		}
		candidate := ownedSourceFile{FsID: fsID, Path: file.SourcePath, Name: file.Name}
		if previous, exists := r.resolved[fsID]; exists && previous != candidate {
			return fmt.Errorf("Baidu source fs_id %d resolved to conflicting paths", fsID)
		}
		r.resolved[fsID] = candidate
	}
	if len(r.resolved) == len(r.wanted) {
		return errOwnedSourcesResolved
	}
	return nil
}

func (c *Client) copyOwnFiles(ctx context.Context, link ShareLink, share ShareContext, fsIDs []int64, remotePath string) error {
	if len(fsIDs) > ownCopyMaxItems {
		return &TransferLimitError{Target: len(fsIDs), Limit: ownCopyMaxItems}
	}
	wanted := make(map[int64]struct{}, len(fsIDs))
	for _, fsID := range fsIDs {
		if fsID <= 0 {
			return fmt.Errorf("invalid owned Baidu source fs_id")
		}
		if _, duplicate := wanted[fsID]; duplicate {
			return fmt.Errorf("duplicate owned Baidu source fs_id %d", fsID)
		}
		wanted[fsID] = struct{}{}
	}
	resolver := &ownedSourceResolver{wanted: wanted, resolved: make(map[int64]ownedSourceFile, len(wanted))}
	err := c.Scan(ctx, "owned-source-resolution", link, share, resolver)
	if err != nil && !errors.Is(err, errOwnedSourcesResolved) {
		return fmt.Errorf("resolve owned Baidu source paths: %w", err)
	}
	if len(resolver.resolved) != len(wanted) {
		return fmt.Errorf("resolve owned Baidu source paths: found %d of %d requested files", len(resolver.resolved), len(wanted))
	}

	items := make([]map[string]string, 0, len(fsIDs))
	for _, fsID := range fsIDs {
		source := resolver.resolved[fsID]
		items = append(items, map[string]string{
			"path":    source.Path,
			"dest":    remotePath,
			"newname": source.Name,
			"ondup":   "fail",
		})
	}
	fileList, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("encode owned Baidu copy list: %w", err)
	}
	query := url.Values{}
	query.Set("opera", "copy")
	query.Set("async", "0")
	query.Set("bdstoken", share.BDSToken)
	query.Set("app_id", panAppID)
	query.Set("channel", "chunlei")
	query.Set("clienttype", "0")
	query.Set("web", "1")
	form := url.Values{}
	form.Set("filelist", string(fileList))
	body, status, err := c.do(ctx, http.MethodPost, "/api/filemanager", query, form, "https://pan.baidu.com/disk/home", 4<<20)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("Baidu owned-file copy returned HTTP %d", status)
	}
	var response struct {
		Errno int `json:"errno"`
		Info  []struct {
			Errno int `json:"errno"`
		} `json:"info"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("parse Baidu owned-file copy response: %w", err)
	}
	if response.Errno != 0 {
		return classifyOwnedCopyError(response.Errno)
	}
	for _, item := range response.Info {
		if item.Errno != 0 {
			return classifyOwnedCopyError(item.Errno)
		}
	}
	return nil
}

func classifyOwnedCopyError(errno int) error {
	switch errno {
	case -6, -7:
		return ErrAuthRequired
	case -30, 4:
		return ErrTransferConflict
	case 8001, -62:
		return ErrVerificationRequired
	default:
		return &RemoteError{Operation: "owned-file copy", Errno: errno}
	}
}
