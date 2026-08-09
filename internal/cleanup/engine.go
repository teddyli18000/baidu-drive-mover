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
	CompleteBatchCleanup(ctx context.Context, taskID, batchID string) error
}

type Remote interface {
	DeleteStagingPath(ctx context.Context, remotePath string) error
}

type Engine struct {
	Layout     *runtimepath.Layout
	Repository Repository
	Remote     Remote
	MaxBatches int
}

type Summary struct {
	BatchesDone int
	FilesDone   int
	BytesFreed  int64
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
			return summary, nil
		}
		batch, err := e.Repository.AuthorizeBatchCleanup(ctx, taskID, candidates[0].BatchID)
		if err != nil {
			return summary, err
		}
		if err := e.cleanupBatch(ctx, batch); err != nil {
			return summary, err
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
	return summary, nil
}

func (e *Engine) cleanupBatch(ctx context.Context, batch state.CleanupBatch) error {
	for i, object := range batch.LocalObjects {
		if object.CleanedAt != "" {
			continue
		}
		file := batch.Files[i]
		if err := e.Layout.RemoveTempFile(object.ObjectPath); err != nil {
			_ = e.Repository.RecordLocalCacheCleanupFailure(context.Background(), batch.TaskID, file.FileID, err.Error())
			return fmt.Errorf("remove local cache for %q: %w", file.LogicalPath, err)
		}
		if err := e.Repository.MarkLocalCacheCleanupDone(ctx, batch.TaskID, file.FileID); err != nil {
			return fmt.Errorf("persist local cleanup for %q: %w", file.LogicalPath, err)
		}
	}

	if batch.BaiduObject.CleanedAt == "" {
		err := e.Remote.DeleteStagingPath(ctx, batch.BaiduObject.ObjectPath)
		if err != nil && !errors.Is(err, baidu.ErrStagingNotFound) {
			_ = e.Repository.RecordBaiduBatchCleanupFailure(context.Background(), batch.TaskID, batch.BatchID, err.Error())
			return fmt.Errorf("remove Baidu staging batch %q: %w", batch.BatchID, err)
		}
		if err := e.Repository.MarkBaiduBatchCleanupDone(ctx, batch.TaskID, batch.BatchID); err != nil {
			return fmt.Errorf("persist Baidu cleanup for %q: %w", batch.BatchID, err)
		}
	}
	return nil
}
