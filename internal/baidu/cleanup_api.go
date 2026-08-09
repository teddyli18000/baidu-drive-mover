package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

var ErrStagingNotFound = errors.New("Baidu staging object not found")

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
