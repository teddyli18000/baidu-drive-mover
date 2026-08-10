package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrRangeNotHonored     = errors.New("Baidu download server did not honor resume range")
	ErrRangeNotSatisfiable = errors.New("Baidu download range is not satisfiable")
)

type DownloadStream struct {
	Body        io.ReadCloser
	Start       int64
	Remaining   int64
	Total       int64
	Partial     bool
	ContentType string
}

// OpenDownload opens a streaming PCS download for a tool-owned staged file.
// The returned body must be closed by the caller.
func (c *Client) OpenDownload(ctx context.Context, remotePath string, offset int64) (*DownloadStream, error) {
	clean, err := validateStagingRemotePath(remotePath)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, fmt.Errorf("negative Baidu download offset %d", offset)
	}
	query := url.Values{}
	query.Set("app_id", pcsAppID)
	query.Set("method", "download")
	query.Set("path", clean)
	reference := &url.URL{Path: "/rest/2.0/pcs/file", RawQuery: query.Encode()}
	u := c.pcsBaseURL.ResolveReference(reference)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Baidu download request: %w", err)
	}
	request.Header.Set("User-Agent", defaultUserAgent)
	request.Header.Set("Accept", "*/*")
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("open Baidu download stream: %w", err)
	}

	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		response.Body.Close()
		return nil, ErrRangeNotSatisfiable
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		defer response.Body.Close()
		return nil, classifyDownloadHTTPError(response)
	}
	if offset > 0 && response.StatusCode != http.StatusPartialContent {
		response.Body.Close()
		return nil, ErrRangeNotHonored
	}

	stream := &DownloadStream{
		Body:        response.Body,
		Start:       0,
		Remaining:   response.ContentLength,
		Total:       response.ContentLength,
		Partial:     response.StatusCode == http.StatusPartialContent,
		ContentType: response.Header.Get("Content-Type"),
	}
	if response.StatusCode == http.StatusPartialContent {
		start, end, total, err := parseContentRange(response.Header.Get("Content-Range"))
		if err != nil {
			response.Body.Close()
			return nil, err
		}
		if start != offset {
			response.Body.Close()
			return nil, fmt.Errorf("Baidu resume started at %d instead of %d: %w", start, offset, ErrRangeNotHonored)
		}
		stream.Start = start
		stream.Total = total
		if end >= start {
			stream.Remaining = end - start + 1
		}
	}
	return stream, nil
}

func classifyDownloadHTTPError(response *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var envelope pcsErrorEnvelope
	if json.Unmarshal(data, &envelope) == nil && envelope.ErrorCode != 0 {
		return classifyPCSError("download", envelope.ErrorCode, response.StatusCode)
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ErrAuthRequired
	}
	return fmt.Errorf("Baidu download returned HTTP %d", response.StatusCode)
}

func parseContentRange(value string) (start, end, total int64, err error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "bytes ") {
		return 0, 0, 0, fmt.Errorf("invalid Baidu Content-Range %q", value)
	}
	rangeAndTotal := strings.TrimSpace(value[len("bytes "):])
	rangePart, totalPart, ok := strings.Cut(rangeAndTotal, "/")
	if !ok || totalPart == "*" {
		return 0, 0, 0, fmt.Errorf("invalid Baidu Content-Range %q", value)
	}
	startPart, endPart, ok := strings.Cut(rangePart, "-")
	if !ok {
		return 0, 0, 0, fmt.Errorf("invalid Baidu Content-Range %q", value)
	}
	start, err = strconv.ParseInt(startPart, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, 0, fmt.Errorf("invalid Baidu Content-Range start %q", value)
	}
	end, err = strconv.ParseInt(endPart, 10, 64)
	if err != nil || end < start {
		return 0, 0, 0, fmt.Errorf("invalid Baidu Content-Range end %q", value)
	}
	total, err = strconv.ParseInt(totalPart, 10, 64)
	if err != nil || total <= end {
		return 0, 0, 0, fmt.Errorf("invalid Baidu Content-Range total %q", value)
	}
	return start, end, total, nil
}
