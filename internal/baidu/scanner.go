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
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

const shareListPageSize = 100

type shareListItem struct {
	FsID           int64  `json:"fs_id"`
	ServerFilename string `json:"server_filename"`
	Path           string `json:"path"`
	IsDir          int    `json:"isdir"`
	Size           int64  `json:"size"`
	MD5            string `json:"md5"`
}

type shareListResponse struct {
	Errno int             `json:"errno"`
	List  []shareListItem `json:"list"`
}

func (c *Client) Scan(ctx context.Context, taskID string, link ShareLink, share ShareContext, sink manifest.Sink) error {
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("scan task ID is empty")
	}
	if sink == nil {
		return fmt.Errorf("manifest sink is nil")
	}
	start := normalizeRemotePath(link.StartPath)
	queue := []string{start}
	queued := map[string]bool{start: true}
	visited := make(map[string]bool)

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := queue[0]
		queue = queue[1:]
		if visited[current] {
			continue
		}
		visited[current] = true

		for pageNumber := 1; ; pageNumber++ {
			items, err := c.listSharePage(ctx, link, share, current, pageNumber)
			if err != nil {
				return err
			}
			directories := make([]manifest.Directory, 0)
			files := make([]manifest.File, 0)
			for _, item := range items {
				fullPath := normalizeRemotePath(item.Path)
				if item.Path == "" {
					fullPath = normalizeRemotePath(path.Join(current, item.ServerFilename))
				}
				logicalPath, err := relativeLogicalPath(start, fullPath)
				if err != nil {
					return fmt.Errorf("Baidu returned path outside selected share root: %w", err)
				}
				if item.IsDir == 1 {
					if fullPath == current {
						continue
					}
					if logicalPath != "/" {
						directories = append(directories, manifest.Directory{LogicalPath: logicalPath})
					}
					if !queued[fullPath] {
						queued[fullPath] = true
						queue = append(queue, fullPath)
					}
					continue
				}
				if item.FsID == 0 {
					return fmt.Errorf("Baidu returned file without fs_id at %q", logicalPath)
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
					LogicalPath: logicalPath,
					ParentPath:  parent,
					Name:        path.Base(logicalPath),
					Size:        item.Size,
					MD5:         strings.TrimSpace(item.MD5),
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
		body, status, err := c.do(ctx, http.MethodPost, "/share/list", query, form, link.SanitizedURL(), 8<<20)
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

func relativeLogicalPath(start, full string) (string, error) {
	start = normalizeRemotePath(start)
	full = normalizeRemotePath(full)
	if start == "/" {
		return full, nil
	}
	if full == start {
		return "/", nil
	}
	prefix := strings.TrimSuffix(start, "/") + "/"
	if !strings.HasPrefix(full, prefix) {
		return "", fmt.Errorf("%q is outside %q", full, start)
	}
	return normalizeRemotePath(strings.TrimPrefix(full, start)), nil
}
