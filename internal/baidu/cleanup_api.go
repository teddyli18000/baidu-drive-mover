package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var ErrStagingNotFound = errors.New("Baidu staging object not found")

// ListStagingPathForCleanup inspects one validated tool-owned staging path and
// preserves an explicit not-found result for crash reconciliation.
func (c *Client) ListStagingPathForCleanup(ctx context.Context, remotePath string) ([]RemoteFile, error) {
	clean, err := validateStagingRemotePath(remotePath)
	if err != nil {
		return nil, err
	}
	if clean == stagingRemoteRoot {
		return nil, fmt.Errorf("refusing cleanup inspection of global Baidu staging root")
	}
	query := url.Values{}
	query.Set("app_id", panAppID)
	query.Set("method", "list")
	query.Set("path", clean)
	query.Set("by", "name")
	query.Set("order", "asc")
	body, status, err := c.doPCS(ctx, http.MethodGet, "/rest/2.0/pcs/file", query, nil, 4<<20)
	if err != nil {
		return nil, err
	}
	var envelope pcsListEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse Baidu cleanup list response: %w", err)
	}
	if envelope.ErrorCode == 31066 || envelope.ErrorCode == 31202 {
		return nil, ErrStagingNotFound
	}
	if envelope.ErrorCode != 0 || status < 200 || status >= 300 {
		return nil, classifyPCSError("inspect staging path for cleanup", envelope.ErrorCode, status)
	}
	files := make([]RemoteFile, 0, len(envelope.List))
	for _, item := range envelope.List {
		files = append(files, RemoteFile{
			FsID:  item.FsID,
			Name:  item.ServerFilename,
			Path:  item.Path,
			Size:  item.Size,
			MD5:   strings.TrimSpace(item.MD5),
			IsDir: item.IsDir == 1,
		})
	}
	return files, nil
}

// DeleteStagingPath deletes exactly one validated tool-staging path. It never
// accepts the global /BaiduDriveMover root and exposes no recycle-bin action.
func (c *Client) DeleteStagingPath(ctx context.Context, remotePath string) error {
	clean, err := validateStagingRemotePath(remotePath)
	if err != nil {
		return err
	}
	if clean == stagingRemoteRoot {
		return fmt.Errorf("refusing to delete global Baidu staging root")
	}
	query := url.Values{}
	query.Set("app_id", panAppID)
	query.Set("method", "delete")
	query.Set("path", clean)
	body, status, err := c.doPCS(ctx, http.MethodPost, "/rest/2.0/pcs/file", query, nil, 2<<20)
	if err != nil {
		return err
	}
	var envelope pcsErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("parse Baidu staging delete response: %w", err)
	}
	if envelope.ErrorCode == 0 && status >= 200 && status < 300 {
		return nil
	}
	if envelope.ErrorCode == 31066 || envelope.ErrorCode == 31202 {
		return ErrStagingNotFound
	}
	return classifyPCSError("delete staging path", envelope.ErrorCode, status)
}
