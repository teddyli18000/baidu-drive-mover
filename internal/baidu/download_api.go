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
	ErrRangeNotHonored     = errors.New("Baidu download server did not honor requested range")
	ErrRangeNotSatisfiable = errors.New("Baidu download range is not satisfiable")
)

type DownloadStream struct {
	Body        io.ReadCloser
	Start       int64
	End         int64
	Remaining   int64
	Total       int64
	Partial     bool
	ContentType string
}

// OpenDownload opens a streaming PCS download for a tool-owned staged file.
// The returned body must be closed by the caller. offset=0 keeps the legacy
// full-file request; offset>0 requests bytes=offset- for resumable download.
func (c *Client) OpenDownload(ctx context.Context, remotePath string, offset int64) (*DownloadStream, error) {
	return c.OpenDownloadRange(ctx, remotePath, offset, -1)
}

// OpenDownloadRange opens a bounded or open-ended PCS byte range for a
// tool-owned staged file. end is inclusive; end=-1 means through EOF.
//
// A bounded request always requires HTTP 206 and an exact Content-Range start
// and end. This makes the primitive safe for disposable-runner relay workers:
// callers never need to accept more bytes than the chunk they scheduled.
func (c *Client) OpenDownloadRange(ctx context.Context, remotePath string, start, end int64) (*DownloadStream, error) {
	clean, err := validateStagingRemotePath(remotePath)
	if err != nil {
		return nil, err
	}
	if start < 0 {
		return nil, fmt.Errorf("negative Baidu download start %d", start)
	}
	if end < -1 || (end >= 0 && end < start) {
		return nil, fmt.Errorf("invalid Baidu download range %d-%d", start, end)
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

	ranged := start > 0 || end >= 0
	if end >= 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	} else if start > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
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
	if ranged && response.StatusCode != http.StatusPartialContent {
		response.Body.Close()
		return nil, ErrRangeNotHonored
	}

	stream := &DownloadStream{
		Body:        response.Body,
		Start:       0,
		End:         response.ContentLength - 1,
		Remaining:   response.ContentLength,
		Total:       response.ContentLength,
		Partial:     response.StatusCode == http.StatusPartialContent,
		ContentType: response.Header.Get("Content-Type"),
	}
	if response.StatusCode == http.StatusPartialContent {
		actualStart, actualEnd, total, err := parseContentRange(response.Header.Get("Content-Range"))
		if err != nil {
			response.Body.Close()
			return nil, err
		}
		if actualStart != start {
			response.Body.Close()
			return nil, fmt.Errorf("Baidu range started at %d instead of %d: %w", actualStart, start, ErrRangeNotHonored)
		}
		if end >= 0 && actualEnd != end {
			response.Body.Close()
			return nil, fmt.Errorf("Baidu range ended at %d instead of %d: %w", actualEnd, end, ErrRangeNotHonored)
		}
		stream.Start = actualStart
		stream.End = actualEnd
		stream.Total = total
		stream.Remaining = actualEnd - actualStart + 1
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
