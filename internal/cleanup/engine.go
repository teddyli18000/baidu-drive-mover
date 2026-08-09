package cleanup

import (
	"context"
	"errors"
	"fmt"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type Repository interface {
	CleanupCandidates(ctx context.Context, taskID string, limit int) ([]state.Batch, error)
	AuthorizeBatchCleanup(ctx context.Context, taskID, batchID string) (state.CleanupBatch, error)
	MarkLocalCacheCleanupDone(ctx context.Context, taskID, fileID string) error
	MarkBaiduBatchCleanupDone(ctx context.Context, taskID, batchID string) error
	RecordLocalCacheCleanupFailure(ctx context.Context, taskID, fileID, message string) error
	RecordBaiduBatchCleanupFailure(ctx context.Context, taskID, batchID, message string) error
	ValidateBatchCleanupCompleteness(ctx context.Context, taskID, batchID string) error
	CompleteBatchCleanup(ctx context.Context, taskID, batchID string) error
	TaskRootCleanupCandidate(ctx context.Context, taskID string) (bool, error)
	AuthorizeTaskRootCleanup(ctx context.Context, taskID string) (state.CleanupObject, error)
	MarkBaiduTaskRootCleanupDone(ctx context.Context, taskID string) error
	RecordBaiduTaskRootCleanupFailure(ctx context.Context, taskID, message string) error
}

type Remote interface {
	ListStagingPathForCleanup(ctx context.Context, remotePath string) ([]baidu.RemoteFile, error)
	DeleteStagingPath(ctx context.Context, remotePath string) error
}

type Engine struct {
	Layout     *runtimepath.Layout
	Repository Repository
	Remote     Remote
	MaxBatches int
}

type Summary struct {
	BatchesDone  int
	FilesDone    int
	BytesFreed   int64
	TaskRootDone bool
}

func (e *Engine) Run(ctx context.Context, taskID string) (Summary, error) {
	if e == nil || e.Layout == nil || e.Repository == nil || e.Remote == nil {
		return Summary{}, fmt.Errorf("cleanup engine is not configured")
	}
	limit := e.MaxBatches
	if limit <= 0 {
		limit = 32
	}
	var summary Summary
	for summary.BatchesDone < limit {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		candidates, err := e.Repository.CleanupCandidates(ctx, taskID, limit-summary.BatchesDone)
		if err != nil {
			return summary, err
		}
		if len(candidates) == 0 {
			break
		}
		batch, err := e.Repository.AuthorizeBatchCleanup(ctx, taskID, candidates[0].BatchID)
		if err != nil {
			return summary, err
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if err := e.cleanupBatch(ctx, batch); err != nil {
			return summary, err
		}
		if err := e.Repository.ValidateBatchCleanupCompleteness(ctx, taskID, batch.BatchID); err != nil {
			return summary, fmt.Errorf("validate cleanup batch %q: %w", batch.BatchID, err)
		}
		if err := e.Repository.CompleteBatchCleanup(ctx, taskID, batch.BatchID); err != nil {
			return summary, fmt.Errorf("complete cleanup batch %q: %w", batch.BatchID, err)
		}
		summary.BatchesDone++
		summary.FilesDone += len(batch.Files)
		for _, file := range batch.Files {
			summary.BytesFreed += file.Size
		}
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	rootDone, err := e.cleanupTaskRoot(ctx, taskID)
	if err != nil {
		return summary, err
	}
	summary.TaskRootDone = rootDone
	return summary, nil
}

func (e *Engine) cleanupBatch(ctx context.Context, batch state.CleanupBatch) error {
	filesByID := make(map[string]state.File, len(batch.Files))
	filesByRemotePath := make(map[string]state.File, len(batch.Files))
	for _, file := range batch.Files {
		if _, exists := filesByID[file.FileID]; exists {
			return fmt.Errorf("duplicate cleanup file identity %q", file.FileID)
		}
		if file.BaiduStagingPath == "" {
			return fmt.Errorf("cleanup file %q has no registered Baidu staging path", file.FileID)
		}
		if _, exists := filesByRemotePath[file.BaiduStagingPath]; exists {
			return fmt.Errorf("duplicate cleanup Baidu staging path %q", file.BaiduStagingPath)
		}
		filesByID[file.FileID] = file
		filesByRemotePath[file.BaiduStagingPath] = file
	}
	if len(batch.LocalObjects) != len(batch.Files) {
		return fmt.Errorf("cleanup batch %q has %d local objects for %d files", batch.BatchID, len(batch.LocalObjects), len(batch.Files))
	}
	for _, object := range batch.LocalObjects {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, ok := filesByID[object.ObjectID]
		if !ok {
			return fmt.Errorf("cleanup local object %q has no matching batch file", object.ObjectID)
		}
		if object.CleanedAt != "" {
			continue
		}
		if err := e.Layout.RemoveTempFile(object.ObjectPath); err != nil {
			_ = e.Repository.RecordLocalCacheCleanupFailure(context.Background(), batch.TaskID, file.FileID, err.Error())
			return fmt.Errorf("remove local cache for %q: %w", file.LogicalPath, err)
		}
		if err := e.Repository.MarkLocalCacheCleanupDone(ctx, batch.TaskID, file.FileID); err != nil {
			return fmt.Errorf("persist local cleanup for %q: %w", file.LogicalPath, err)
		}
	}

	if batch.BaiduObject.CleanedAt == "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := e.Remote.ListStagingPathForCleanup(ctx, batch.BaiduObject.ObjectPath)
		if errors.Is(err, baidu.ErrStagingNotFound) {
			if markErr := e.Repository.MarkBaiduBatchCleanupDone(ctx, batch.TaskID, batch.BatchID); markErr != nil {
				return fmt.Errorf("persist already-missing Baidu batch %q: %w", batch.BatchID, markErr)
			}
			return nil
		}
		if err != nil {
			_ = e.Repository.RecordBaiduBatchCleanupFailure(context.Background(), batch.TaskID, batch.BatchID, err.Error())
			return fmt.Errorf("inspect Baidu staging batch %q before cleanup: %w", batch.BatchID, err)
		}
		for _, item := range items {
			expected, ok := filesByRemotePath[item.Path]
			if !ok || item.IsDir || item.Size != expected.Size {
				err := fmt.Errorf("Baidu staging batch %q contains an unexpected or changed object at %q", batch.BatchID, item.Path)
				_ = e.Repository.RecordBaiduBatchCleanupFailure(context.Background(), batch.TaskID, batch.BatchID, err.Error())
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.Remote.DeleteStagingPath(ctx, batch.BaiduObject.ObjectPath); err != nil && !errors.Is(err, baidu.ErrStagingNotFound) {
			_ = e.Repository.RecordBaiduBatchCleanupFailure(context.Background(), batch.TaskID, batch.BatchID, err.Error())
			return fmt.Errorf("remove Baidu staging batch %q: %w", batch.BatchID, err)
		}
		if err := e.Repository.MarkBaiduBatchCleanupDone(ctx, batch.TaskID, batch.BatchID); err != nil {
			return fmt.Errorf("persist Baidu cleanup for %q: %w", batch.BatchID, err)
		}
	}
	return nil
}

func (e *Engine) cleanupTaskRoot(ctx context.Context, taskID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	candidate, err := e.Repository.TaskRootCleanupCandidate(ctx, taskID)
	if err != nil {
		return false, err
	}
	if !candidate {
		return false, nil
	}
	object, err := e.Repository.AuthorizeTaskRootCleanup(ctx, taskID)
	if err != nil {
		return false, err
	}
	if object.CleanedAt != "" {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	items, err := e.Remote.ListStagingPathForCleanup(ctx, object.ObjectPath)
	if errors.Is(err, baidu.ErrStagingNotFound) {
		if markErr := e.Repository.MarkBaiduTaskRootCleanupDone(ctx, taskID); markErr != nil {
			return false, fmt.Errorf("persist already-missing Baidu task root: %w", markErr)
		}
		return true, nil
	}
	if err != nil {
		_ = e.Repository.RecordBaiduTaskRootCleanupFailure(context.Background(), taskID, err.Error())
		return false, fmt.Errorf("inspect Baidu task root before cleanup: %w", err)
	}
	if len(items) != 0 {
		err := fmt.Errorf("Baidu task root contains %d unexpected objects", len(items))
		_ = e.Repository.RecordBaiduTaskRootCleanupFailure(context.Background(), taskID, err.Error())
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := e.Remote.DeleteStagingPath(ctx, object.ObjectPath); err != nil && !errors.Is(err, baidu.ErrStagingNotFound) {
		_ = e.Repository.RecordBaiduTaskRootCleanupFailure(context.Background(), taskID, err.Error())
		return false, fmt.Errorf("remove empty Baidu task root: %w", err)
	}
	if err := e.Repository.MarkBaiduTaskRootCleanupDone(ctx, taskID); err != nil {
		return false, fmt.Errorf("persist Baidu task-root cleanup: %w", err)
	}
	return true, nil
}
