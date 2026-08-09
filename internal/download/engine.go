package download

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

const DefaultMaxCacheBytes int64 = 30 << 30

var (
	ErrCacheWatermark = errors.New("local cache high-water mark reached")
	internalIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

type OversizedCacheFileError struct {
	LogicalPath string
	Size        int64
	Limit       int64
}

func (e *OversizedCacheFileError) Error() string {
	return fmt.Sprintf("file %q requires %d bytes but local cache limit is %d", e.LogicalPath, e.Size, e.Limit)
}

type Repository interface {
	DownloadCandidates(ctx context.Context, taskID string, limit int) ([]state.File, error)
	StartDownload(ctx context.Context, taskID, fileID, cachePath string) error
	MarkLocalReady(ctx context.Context, taskID, fileID, cachePath string) error
	RecordDownloadFailure(ctx context.Context, taskID, fileID, message string, permanent bool) error
	ReservedCacheBytes(ctx context.Context) (int64, error)
}

type Remote interface {
	OpenDownload(ctx context.Context, remotePath string, offset int64) (*baidu.DownloadStream, error)
}

type Engine struct {
	Layout        *runtimepath.Layout
	Repository    Repository
	Remote        Remote
	MaxCacheBytes int64
	MaxAttempts   int
	Sleep         func(context.Context, time.Duration) error
}

type Summary struct {
	FilesReady        int
	BytesReady        int64
	PausedByWatermark bool
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

func (e *Engine) Run(ctx context.Context, taskID string) (Summary, error) {
	if e == nil || e.Layout == nil || e.Repository == nil || e.Remote == nil {
		return Summary{}, fmt.Errorf("download engine is not configured")
	}
	if !internalIDPattern.MatchString(taskID) {
		return Summary{}, fmt.Errorf("unsafe download task ID %q", taskID)
	}
	limit := e.MaxCacheBytes
	if limit <= 0 {
		limit = DefaultMaxCacheBytes
	}

	var summary Summary
	for {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		candidates, err := e.Repository.DownloadCandidates(ctx, taskID, 64)
		if err != nil {
			return summary, err
		}
		if len(candidates) == 0 {
			return summary, nil
		}
		file := candidates[0]
		if !internalIDPattern.MatchString(file.FileID) {
			return summary, permanent(fmt.Errorf("unsafe internal file ID %q", file.FileID))
		}
		if file.BaiduStagingPath == "" {
			return summary, permanent(fmt.Errorf("file %q has no verified Baidu staging path", file.LogicalPath))
		}

		if file.Status == state.FileBaiduStaged {
			if file.Size > limit {
				return summary, &OversizedCacheFileError{LogicalPath: file.LogicalPath, Size: file.Size, Limit: limit}
			}
			reserved, err := e.Repository.ReservedCacheBytes(ctx)
			if err != nil {
				return summary, err
			}
			if reserved+file.Size > limit {
				summary.PausedByWatermark = true
				return summary, nil
			}
		}

		ready, err := e.downloadOne(ctx, file)
		if err != nil {
			var p *permanentError
			isPermanent := errors.As(err, &p)
			if recordErr := e.Repository.RecordDownloadFailure(context.Background(), file.TaskID, file.FileID, err.Error(), isPermanent); recordErr != nil {
				return summary, fmt.Errorf("download failed: %v; record failure: %w", err, recordErr)
			}
			return summary, err
		}
		if ready {
			summary.FilesReady++
			summary.BytesReady += file.Size
		}
	}
}

func (e *Engine) downloadOne(ctx context.Context, file state.File) (bool, error) {
	partRelative, binRelative, err := cachePaths(file.TaskID, file.FileID)
	if err != nil {
		return false, permanent(err)
	}
	cacheDir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(partRelative)))
	if _, err := e.Layout.EnsureTempDir(cacheDir); err != nil {
		return false, err
	}

	if ok, err := e.verifyExisting(binRelative, file); err != nil {
		if removeErr := e.Layout.RemoveTempFile(binRelative); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return false, fmt.Errorf("discard invalid completed cache file: %w", removeErr)
		}
	} else if ok {
		if err := e.Repository.MarkLocalReady(ctx, file.TaskID, file.FileID, binRelative); err != nil {
			return false, err
		}
		return true, nil
	}

	partSize, err := e.partSize(partRelative)
	if err != nil {
		return false, err
	}
	if partSize > file.Size {
		if err := e.Layout.RemoveTempFile(partRelative); err != nil {
			return false, err
		}
		partSize = 0
	}
	if partSize == file.Size {
		if ok, verifyErr := e.verifyExisting(partRelative, file); verifyErr == nil && ok {
			if err := e.Layout.RenameTempFile(partRelative, binRelative); err != nil {
				return false, err
			}
			if err := e.Repository.MarkLocalReady(ctx, file.TaskID, file.FileID, binRelative); err != nil {
				return false, err
			}
			return true, nil
		}
		if err := e.Layout.RemoveTempFile(partRelative); err != nil {
			return false, err
		}
		partSize = 0
	}

	if err := e.Repository.StartDownload(ctx, file.TaskID, file.FileID, partRelative); err != nil {
		return false, err
	}
	attempts := e.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		offset, err := e.partSize(partRelative)
		if err != nil {
			return false, err
		}
		stream, err := e.Remote.OpenDownload(ctx, file.BaiduStagingPath, offset)
		if errors.Is(err, baidu.ErrRangeNotHonored) || errors.Is(err, baidu.ErrRangeNotSatisfiable) {
			if offset == 0 {
				return false, err
			}
			if removeErr := e.Layout.RemoveTempFile(partRelative); removeErr != nil {
				return false, removeErr
			}
			attempt--
			continue
		}
		if err != nil {
			if errors.Is(err, baidu.ErrAuthRequired) || errors.Is(err, baidu.ErrVerificationRequired) || errors.Is(err, baidu.ErrQuotaExceeded) {
				return false, err
			}
			lastErr = err
			if attempt+1 < attempts {
				if err := e.sleep(ctx, backoff(attempt)); err != nil {
					return false, err
				}
				continue
			}
			break
		}

		copyErr := e.copyStream(ctx, partRelative, file.Size, offset, stream)
		if copyErr != nil {
			lastErr = copyErr
			if errors.Is(copyErr, context.Canceled) || errors.Is(copyErr, context.DeadlineExceeded) {
				return false, copyErr
			}
			if attempt+1 < attempts {
				if err := e.sleep(ctx, backoff(attempt)); err != nil {
					return false, err
				}
				continue
			}
			break
		}

		ok, verifyErr := e.verifyExisting(partRelative, file)
		if verifyErr == nil && ok {
			if err := e.Layout.RenameTempFile(partRelative, binRelative); err != nil {
				return false, err
			}
			if err := e.Repository.MarkLocalReady(ctx, file.TaskID, file.FileID, binRelative); err != nil {
				return false, err
			}
			return true, nil
		}
		if verifyErr == nil {
			verifyErr = fmt.Errorf("download verification failed")
		}
		lastErr = verifyErr
		if err := e.Layout.RemoveTempFile(partRelative); err != nil {
			return false, err
		}
		if attempt+1 < attempts {
			if err := e.sleep(ctx, backoff(attempt)); err != nil {
				return false, err
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("download retry loop exhausted")
	}
	if isDeterministicVerificationError(lastErr) {
		return false, permanent(lastErr)
	}
	return false, lastErr
}

func (e *Engine) copyStream(ctx context.Context, partRelative string, expectedSize, offset int64, stream *baidu.DownloadStream) error {
	if stream == nil || stream.Body == nil {
		return fmt.Errorf("nil Baidu download stream")
	}
	defer stream.Body.Close()
	if stream.Total >= 0 && stream.Total != expectedSize {
		return permanent(fmt.Errorf("Baidu download total size %d does not match expected %d", stream.Total, expectedSize))
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := e.Layout.OpenTempFile(partRelative, flags, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("seek partial cache file: %w", err)
		}
		if err := file.Truncate(offset); err != nil {
			return fmt.Errorf("truncate partial cache file to resume offset: %w", err)
		}
	}

	buffer := make([]byte, 1<<20)
	_, err = io.CopyBuffer(file, &contextReader{ctx: ctx, reader: stream.Body}, buffer)
	if syncErr := file.Sync(); err == nil && syncErr != nil {
		err = syncErr
	}
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	closed = true
	if err != nil {
		return fmt.Errorf("stream Baidu download: %w", err)
	}
	return nil
}

func (e *Engine) verifyExisting(relative string, file state.File) (bool, error) {
	info, err := e.Layout.StatTempFile(relative)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() != file.Size {
		return false, fmt.Errorf("cache size mismatch for %q: got %d want %d", file.LogicalPath, info.Size(), file.Size)
	}
	if strings.TrimSpace(file.MD5) == "" {
		return true, nil
	}
	actual, err := e.md5File(relative)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(actual, strings.TrimSpace(file.MD5)) {
		return false, fmt.Errorf("cache MD5 mismatch for %q: got %s want %s", file.LogicalPath, actual, file.MD5)
	}
	return true, nil
}

func (e *Engine) md5File(relative string) (string, error) {
	file, err := e.Layout.OpenTempFile(relative, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash cache file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (e *Engine) partSize(relative string) (int64, error) {
	info, err := e.Layout.StatTempFile(relative)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func cachePaths(taskID, fileID string) (part, bin string, err error) {
	if !internalIDPattern.MatchString(taskID) || !internalIDPattern.MatchString(fileID) {
		return "", "", fmt.Errorf("unsafe cache identifier")
	}
	base := filepath.ToSlash(filepath.Join("cache", taskID, fileID))
	return base + ".part", base + ".bin", nil
}

func isDeterministicVerificationError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "md5 mismatch") || strings.Contains(text, "total size") || strings.Contains(text, "size mismatch")
}

func (e *Engine) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 4 {
		attempt = 4
	}
	return 500 * time.Millisecond * time.Duration(1<<attempt)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, err
}
