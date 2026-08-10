package staging

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type Repository interface {
	StagingBatches(ctx context.Context, taskID string) ([]state.Batch, error)
	StartBatch(ctx context.Context, taskID, batchID string) error
	RecordStagedFiles(ctx context.Context, taskID, batchID string, staged map[string]string) error
	CompleteBatch(ctx context.Context, taskID, batchID string) error
	FailBatch(ctx context.Context, taskID, batchID, message string, permanent bool) error
}

type Remote interface {
	EnsureStagingDirectory(ctx context.Context, remotePath string) error
	ListStagingDirectory(ctx context.Context, remotePath string) ([]baidu.RemoteFile, error)
	TransferFiles(ctx context.Context, link baidu.ShareLink, share baidu.ShareContext, fsIDs []int64, remotePath string) error
}

type Executor struct {
	Repository  Repository
	Remote      Remote
	Link        baidu.ShareLink
	Share       baidu.ShareContext
	MaxAttempts int
	Sleep       func(context.Context, time.Duration) error
}

type stageError struct {
	err       error
	permanent bool
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &stageError{err: err, permanent: true}
}

func (e *Executor) Run(ctx context.Context, taskID string) error {
	if e == nil || e.Repository == nil || e.Remote == nil {
		return fmt.Errorf("staging executor is not configured")
	}
	batches, err := e.Repository.StagingBatches(ctx, taskID)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if err := e.stageBatch(ctx, batch); err != nil {
			var classified *stageError
			isPermanent := errors.As(err, &classified) && classified.permanent
			if recordErr := e.Repository.FailBatch(context.Background(), batch.TaskID, batch.BatchID, err.Error(), isPermanent); recordErr != nil {
				return fmt.Errorf("stage batch failed: %v; record failure: %w", err, recordErr)
			}
			return err
		}
	}
	return nil
}

func (e *Executor) stageBatch(ctx context.Context, batch state.Batch) error {
	if len(batch.Files) == 0 {
		return permanent(fmt.Errorf("batch %q has no files", batch.BatchID))
	}
	if err := e.Repository.StartBatch(ctx, batch.TaskID, batch.BatchID); err != nil {
		return err
	}
	if err := e.Remote.EnsureStagingDirectory(ctx, batch.BaiduStagingPath); err != nil {
		return err
	}
	missing, err := e.reconcile(ctx, batch)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		if err := e.transferSubset(ctx, batch, missing); err != nil {
			return err
		}
	}
	missing, err = e.reconcile(ctx, batch)
	if err != nil {
		return err
	}
	if len(missing) != 0 {
		return fmt.Errorf("batch %q still has %d files missing after transfer", batch.BatchID, len(missing))
	}
	return e.Repository.CompleteBatch(ctx, batch.TaskID, batch.BatchID)
}

func (e *Executor) transferSubset(ctx context.Context, batch state.Batch, files []state.File) error {
	if len(files) == 0 {
		return nil
	}
	attempts := e.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	current := append([]state.File(nil), files...)
	for attempt := 0; attempt < attempts; attempt++ {
		ids, err := sourceIDs(current)
		if err != nil {
			return permanent(err)
		}
		transferErr := e.Remote.TransferFiles(ctx, e.Link, e.Share, ids, batch.BaiduStagingPath)
		missingAll, reconcileErr := e.reconcile(ctx, batch)
		if reconcileErr != nil {
			return reconcileErr
		}
		missing := intersectMissing(current, missingAll)
		if len(missing) == 0 {
			return nil
		}
		progress := len(missing) < len(current)

		var limitErr *baidu.TransferLimitError
		if errors.As(transferErr, &limitErr) {
			return e.transferSplit(ctx, batch, missing, limitErr.Limit)
		}
		if errors.Is(transferErr, baidu.ErrAuthRequired) ||
			errors.Is(transferErr, baidu.ErrPasswordRequired) ||
			errors.Is(transferErr, baidu.ErrVerificationRequired) ||
			errors.Is(transferErr, baidu.ErrQuotaExceeded) {
			return transferErr
		}
		if errors.Is(transferErr, baidu.ErrTransferConflict) {
			if len(missing) == 1 {
				return permanent(fmt.Errorf("single-file staging conflict for %q: %w", missing[0].LogicalPath, baidu.ErrTransferConflict))
			}
			return e.transferSplit(ctx, batch, missing, 0)
		}
		if progress {
			current = missing
			attempt = -1 // progress resets the bounded retry budget for the smaller remainder.
			continue
		}
		if transferErr == nil {
			transferErr = fmt.Errorf("Baidu transfer returned success but no staged objects appeared")
		}
		if attempt+1 >= attempts {
			return transferErr
		}
		if err := e.sleep(ctx, backoff(attempt)); err != nil {
			return err
		}
	}
	return fmt.Errorf("staging retry loop exhausted")
}

func (e *Executor) transferSplit(ctx context.Context, batch state.Batch, files []state.File, serviceLimit int) error {
	if len(files) <= 1 {
		return permanent(fmt.Errorf("cannot split single staging file %q", files[0].LogicalPath))
	}
	chunkSize := serviceLimit
	if chunkSize <= 0 || chunkSize >= len(files) {
		chunkSize = (len(files) + 1) / 2
	}
	if chunkSize < 1 {
		chunkSize = 1
	}
	for start := 0; start < len(files); start += chunkSize {
		end := start + chunkSize
		if end > len(files) {
			end = len(files)
		}
		if err := e.transferSubset(ctx, batch, files[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) reconcile(ctx context.Context, batch state.Batch) ([]state.File, error) {
	remoteFiles, err := e.Remote.ListStagingDirectory(ctx, batch.BaiduStagingPath)
	if err != nil {
		return nil, err
	}
	expected := make(map[string]state.File, len(batch.Files))
	for _, file := range batch.Files {
		if file.Name == "" || strings.Contains(file.Name, "/") || strings.Contains(file.Name, "\\") {
			return nil, permanent(fmt.Errorf("invalid source filename for staging: %q", file.Name))
		}
		if _, exists := expected[file.Name]; exists {
			return nil, permanent(fmt.Errorf("duplicate source filename %q within isolated batch", file.Name))
		}
		expected[file.Name] = file
	}
	staged := make(map[string]string)
	present := make(map[string]bool)
	seenNames := make(map[string]struct{}, len(remoteFiles))
	seenFsIDs := make(map[int64]struct{}, len(remoteFiles))
	for _, remote := range remoteFiles {
		if remote.FsID <= 0 {
			return nil, permanent(&baidu.StagingConflictError{Name: remote.Name})
		}
		if _, exists := seenFsIDs[remote.FsID]; exists {
			return nil, permanent(&baidu.StagingConflictError{Name: remote.Name})
		}
		seenFsIDs[remote.FsID] = struct{}{}
		if _, exists := seenNames[remote.Name]; exists {
			return nil, permanent(&baidu.StagingConflictError{Name: remote.Name})
		}
		seenNames[remote.Name] = struct{}{}
		file, ok := expected[remote.Name]
		if !ok || remote.IsDir {
			return nil, permanent(&baidu.StagingConflictError{Name: remote.Name})
		}
		if remote.Size != file.Size {
			return nil, permanent(&baidu.StagingConflictError{Name: remote.Name})
		}
		if file.MD5 != "" && remote.MD5 != "" && !strings.EqualFold(file.MD5, remote.MD5) {
			return nil, permanent(&baidu.StagingConflictError{Name: remote.Name})
		}
		remotePath := remote.Path
		if remotePath == "" {
			remotePath = path.Join(batch.BaiduStagingPath, remote.Name)
		}
		present[file.FileID] = true
		staged[file.FileID] = remotePath
	}
	if err := e.Repository.RecordStagedFiles(ctx, batch.TaskID, batch.BatchID, staged); err != nil {
		return nil, err
	}
	missing := make([]state.File, 0, len(batch.Files)-len(present))
	for _, file := range batch.Files {
		if !present[file.FileID] {
			missing = append(missing, file)
		}
	}
	return missing, nil
}

func sourceIDs(files []state.File) ([]int64, error) {
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		id, err := strconv.ParseInt(strings.TrimSpace(file.FileID), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid Baidu source fs_id %q for %q", file.FileID, file.LogicalPath)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func intersectMissing(current, missing []state.File) []state.File {
	missingIDs := make(map[string]bool, len(missing))
	for _, file := range missing {
		missingIDs[file.FileID] = true
	}
	result := make([]state.File, 0, len(current))
	for _, file := range current {
		if missingIDs[file.FileID] {
			result = append(result, file)
		}
	}
	return result
}

func (e *Executor) sleep(ctx context.Context, d time.Duration) error {
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
