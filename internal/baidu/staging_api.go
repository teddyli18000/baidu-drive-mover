package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const stagingRemoteRoot = "/BaiduDriveMover"

type RemoteFile struct {
	FsID  int64
	Name  string
	Path  string
	Size  int64
	MD5   string
	IsDir bool
}

type pcsErrorEnvelope struct {
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

type pcsListEnvelope struct {
	pcsErrorEnvelope
	List []struct {
		FsID           int64  `json:"fs_id"`
		Path           string `json:"path"`
		ServerFilename string `json:"server_filename"`
		Size           int64  `json:"size"`
		MD5            string `json:"md5"`
		IsDir          int    `json:"isdir"`
	} `json:"list"`
}

// EnsureStagingDirectory creates a tool-owned staging directory and its
// tool-owned ancestors. It refuses paths outside /BaiduDriveMover.
func (c *Client) EnsureStagingDirectory(ctx context.Context, remotePath string) error {
	clean, err := validateStagingRemotePath(remotePath)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	current := ""
	for _, part := range parts {
		current = path.Join(current, "/"+part)
		if err := c.mkdirPCS(ctx, current); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) mkdirPCS(ctx context.Context, remotePath string) error {
	query := url.Values{}
	query.Set("app_id", pcsAppID)
	query.Set("method", "mkdir")
	query.Set("path", remotePath)
	body, status, err := c.doPCS(ctx, http.MethodPost, "/rest/2.0/pcs/file", query, nil, 2<<20)
	if err != nil {
		return err
	}
	var envelope pcsErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("parse Baidu mkdir response: %w", err)
	}
	if envelope.ErrorCode == 0 && status >= 200 && status < 300 {
		return nil
	}
	if envelope.ErrorCode == 31061 {
		return nil
	}
	return classifyPCSError("mkdir", envelope.ErrorCode, status)
}

// ListStagingDirectory lists one isolated batch directory. Batch directories
// are deliberately capped below 500 items, so the legacy PCS list API does
// not need to enumerate an unbounded source tree.
func (c *Client) ListStagingDirectory(ctx context.Context, remotePath string) ([]RemoteFile, error) {
	clean, err := validateStagingRemotePath(remotePath)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("app_id", pcsAppID)
	query.Set("method", "list")
	query.Set("path", clean)
	query.Set("by", "name")
	query.Set("order", "asc")
	body, status, err := c.doPCSRead(ctx, http.MethodGet, "/rest/2.0/pcs/file", query, nil, 4<<20)
	if err != nil {
		return nil, err
	}
	var envelope pcsListEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse Baidu staging list response: %w", err)
	}
	if envelope.ErrorCode != 0 || status < 200 || status >= 300 {
		return nil, classifyPCSError("list staging directory", envelope.ErrorCode, status)
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

// TransferFiles stages a bounded set of individual source file IDs into a
// unique tool-owned batch directory.
func (c *Client) TransferFiles(ctx context.Context, link ShareLink, share ShareContext, fsIDs []int64, remotePath string) error {
	if len(fsIDs) == 0 {
		return fmt.Errorf("Baidu transfer file list is empty")
	}
	clean, err := validateStagingRemotePath(remotePath)
	if err != nil {
		return err
	}
	if share.UK != "" && share.UK == share.ShareUK {
		return c.copyOwnFiles(ctx, link, share, fsIDs, clean)
	}
	encodedIDs, err := json.Marshal(fsIDs)
	if err != nil {
		return fmt.Errorf("encode Baidu transfer file IDs: %w", err)
	}
	query := url.Values{}
	query.Set("app_id", panAppID)
	query.Set("channel", "chunlei")
	query.Set("clienttype", "0")
	query.Set("web", "1")
	query.Set("bdstoken", share.BDSToken)
	query.Set("shareid", share.ShareID)
	query.Set("from", share.ShareUK)
	form := url.Values{}
	form.Set("fsidlist", string(encodedIDs))
	form.Set("path", clean)
	body, status, err := c.do(ctx, http.MethodPost, "/share/transfer", query, form, link.SanitizedURL(), 4<<20)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("Baidu share transfer returned HTTP %d", status)
	}
	var response struct {
		Errno               int    `json:"errno"`
		ErrMsg              string `json:"errmsg"`
		TargetFileNums      int    `json:"target_file_nums"`
		TargetFileNumsLimit int    `json:"target_file_nums_limit"`
		Info                []struct {
			Errno int    `json:"errno"`
			FsID  int64  `json:"fsid"`
			FsID2 int64  `json:"fs_id"`
			Path  string `json:"path"`
		} `json:"info"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("parse Baidu share transfer response: %w", err)
	}
	if response.Errno == 0 {
		for _, item := range response.Info {
			if item.Errno == 0 {
				continue
			}
			if item.Errno == -30 {
				return ErrTransferConflict
			}
			return &RemoteError{Operation: "share transfer item", Errno: item.Errno}
		}
		return nil
	}
	if response.Errno == 12 && response.TargetFileNumsLimit > 0 && response.TargetFileNums > response.TargetFileNumsLimit {
		return &TransferLimitError{Target: response.TargetFileNums, Limit: response.TargetFileNumsLimit}
	}
	for _, item := range response.Info {
		if item.Errno == -30 {
			return ErrTransferConflict
		}
	}
	switch response.Errno {
	case 4:
		return ErrTransferConflict
	case -6, -7:
		return ErrAuthRequired
	case -9:
		return ErrPasswordRequired
	case 8001, -62:
		return ErrVerificationRequired
	default:
		return &RemoteError{Operation: "share transfer", Errno: response.Errno}
	}
}

func classifyPCSError(operation string, code, status int) error {
	switch code {
	case 31041, 31042, 31045:
		return ErrAuthRequired
	case 31112, 31218:
		return ErrQuotaExceeded
	case 0:
		if status >= 200 && status < 300 {
			return nil
		}
		return fmt.Errorf("Baidu PCS %s returned HTTP %d", operation, status)
	default:
		return &PCSRemoteError{Operation: operation, Code: code}
	}
}

func validateStagingRemotePath(remotePath string) (string, error) {
	if remotePath == "" {
		return "", fmt.Errorf("empty Baidu staging path")
	}
	clean := path.Clean(strings.ReplaceAll(remotePath, "\\", "/"))
	if clean != remotePath {
		return "", fmt.Errorf("non-canonical Baidu staging path %q", remotePath)
	}
	if clean != stagingRemoteRoot && !strings.HasPrefix(clean, stagingRemoteRoot+"/") {
		return "", fmt.Errorf("Baidu staging path escapes tool root: %q", remotePath)
	}
	if strings.Contains(clean, "\x00") {
		return "", fmt.Errorf("invalid Baidu staging path")
	}
	return clean, nil
}

func parseFileID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid Baidu source fs_id %q", value)
	}
	return id, nil
}
