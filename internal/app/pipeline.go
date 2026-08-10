package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	cleanupengine "github.com/teddyli18000/baidu-drive-mover/internal/cleanup"
	"github.com/teddyli18000/baidu-drive-mover/internal/download"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

var (
	ErrPipelineNoProgress      = errors.New("pipeline made no durable progress")
	ErrPipelinePermanentFailed = errors.New("pipeline contains permanently failed files")
	ErrPipelinePassLimit       = errors.New("pipeline pass limit reached")
	ErrPipelineScanIncomplete  = errors.New("pipeline cannot start before the share scan is complete")
	ErrPipelineCacheWatermark  = errors.New("pipeline paused by local cache watermark")
)

type PipelineState interface {
	PipelineProgress(ctx context.Context, taskID string) (state.PipelineProgress, error)
	UpdateTaskStatus(ctx context.Context, taskID string, status state.TaskStatus, lastError string) error
}

type CleanupPass func(ctx context.Context, taskID string) (cleanupengine.Summary, error)
type DrivePass func(ctx context.Context, taskID string) (DriveSummary, error)
type DownloadPass func(ctx context.Context, taskID string) (download.Summary, error)
type StagePass func(ctx context.Context, taskID string, maxBatchBytes int64) (StageSummary, error)

type PipelineRunner struct {
	State         PipelineState
	Cleanup       CleanupPass
	Drive         DrivePass
	Download      DownloadPass
	Stage         StagePass
	Logger        *slog.Logger
	MaxCacheBytes int64
	MaxPasses     int
}

type PipelineSummary struct {
	Passes         int
	BatchesCleaned int
	FilesDone      int
	BytesFreed     int64
	FilesVerified  int
	BytesVerified  int64
	FilesReady     int
	BytesReady     int64
	Final          state.PipelineProgress
}

func (r *PipelineRunner) Run(ctx context.Context, taskID string) (PipelineSummary, error) {
	if r == nil || r.State == nil || r.Cleanup == nil || r.Drive == nil || r.Download == nil || r.Stage == nil {
		return PipelineSummary{}, fmt.Errorf("pipeline runner is not fully configured")
	}
	if r.MaxCacheBytes <= 0 {
		return PipelineSummary{}, fmt.Errorf("pipeline cache watermark must be positive")
	}
	maxPasses := r.MaxPasses
	if maxPasses <= 0 {
		maxPasses = 100000
	}

	var summary PipelineSummary
	for pass := 1; pass <= maxPasses; pass++ {
		if err := ctx.Err(); err != nil {
			_ = r.State.UpdateTaskStatus(context.Background(), taskID, state.TaskPaused, safeError(err))
			return summary, err
		}
		before, err := r.State.PipelineProgress(ctx, taskID)
		if err != nil {
			return summary, err
		}
		if before.ScanIncomplete {
			err := ErrPipelineScanIncomplete
			_ = r.State.UpdateTaskStatus(context.Background(), taskID, state.TaskBlocked, safeError(err))
			summary.Final = before
			return summary, err
		}
		if before.FailedPermanent > 0 {
			err := fmt.Errorf("%w: %d files", ErrPipelinePermanentFailed, before.FailedPermanent)
			_ = r.State.UpdateTaskStatus(context.Background(), taskID, state.TaskFailed, safeError(err))
			summary.Final = before
			return summary, err
		}
		if before.Complete() {
			if err := r.State.UpdateTaskStatus(ctx, taskID, state.TaskCompleted, ""); err != nil {
				return summary, err
			}
			summary.Final = before
			return summary, nil
		}

		summary.Passes = pass
		current := before
		downloadPausedByWatermark := false

		if current.HasCleanupWork() {
			cleanupSummary, err := r.Cleanup(ctx, taskID)
			if err != nil {
				return r.fail(ctx, taskID, summary, err)
			}
			summary.BatchesCleaned += cleanupSummary.BatchesDone
			summary.FilesDone += cleanupSummary.FilesDone
			summary.BytesFreed += cleanupSummary.BytesFreed
			current, err = r.State.PipelineProgress(ctx, taskID)
			if err != nil {
				return summary, err
			}
			if current.Complete() {
				if err := r.State.UpdateTaskStatus(ctx, taskID, state.TaskCompleted, ""); err != nil {
					return summary, err
				}
				summary.Final = current
				return summary, nil
			}
		}

		if current.HasDriveWork() || (current.Total == 0 && !current.DriveRootReady) {
			driveSummary, err := r.Drive(ctx, taskID)
			if err != nil {
				return r.fail(ctx, taskID, summary, err)
			}
			summary.FilesVerified += driveSummary.FilesVerified
			summary.BytesVerified += driveSummary.BytesVerified
			current, err = r.State.PipelineProgress(ctx, taskID)
			if err != nil {
				return summary, err
			}
		}

		if current.HasDownloadWork() {
			downloadSummary, err := r.Download(ctx, taskID)
			if err != nil {
				return r.fail(ctx, taskID, summary, err)
			}
			downloadPausedByWatermark = downloadSummary.PausedByWatermark
			summary.FilesReady += downloadSummary.FilesReady
			summary.BytesReady += downloadSummary.BytesReady
			current, err = r.State.PipelineProgress(ctx, taskID)
			if err != nil {
				return summary, err
			}
		}

		available := r.MaxCacheBytes - current.ReservedCache
		if !downloadPausedByWatermark && available > 0 && current.HasStageWork() {
			if _, err := r.Stage(ctx, taskID, available); err != nil {
				return r.fail(ctx, taskID, summary, err)
			}
			current, err = r.State.PipelineProgress(ctx, taskID)
			if err != nil {
				return summary, err
			}
		}

		if current.FailedPermanent > 0 {
			err := fmt.Errorf("%w: %d files", ErrPipelinePermanentFailed, current.FailedPermanent)
			_ = r.State.UpdateTaskStatus(context.Background(), taskID, state.TaskFailed, safeError(err))
			summary.Final = current
			return summary, err
		}
		if current.Complete() {
			if err := r.State.UpdateTaskStatus(ctx, taskID, state.TaskCompleted, ""); err != nil {
				return summary, err
			}
			summary.Final = current
			return summary, nil
		}
		if current == before {
			if downloadPausedByWatermark || (current.HasStageWork() && current.ReservedCache >= r.MaxCacheBytes) {
				err := ErrPipelineCacheWatermark
				_ = r.State.UpdateTaskStatus(context.Background(), taskID, state.TaskPaused, safeError(err))
				summary.Final = current
				return summary, err
			}
			err := fmt.Errorf("%w: total=%d done=%d retryable=%d reserved=%d", ErrPipelineNoProgress, current.Total, current.Done, current.FailedRetryable, current.ReservedCache)
			_ = r.State.UpdateTaskStatus(context.Background(), taskID, state.TaskBlocked, safeError(err))
			summary.Final = current
			return summary, err
		}
		if r.Logger != nil {
			r.Logger.Info("pipeline durable pass completed",
				"task_id", taskID,
				"pass", pass,
				"done", current.Done,
				"total", current.Total,
				"reserved_cache", current.ReservedCache,
			)
		}
	}

	progress, err := r.State.PipelineProgress(ctx, taskID)
	if err != nil {
		return summary, err
	}
	err = fmt.Errorf("%w: %d", ErrPipelinePassLimit, maxPasses)
	_ = r.State.UpdateTaskStatus(context.Background(), taskID, state.TaskBlocked, safeError(err))
	summary.Final = progress
	return summary, err
}

func (r *PipelineRunner) fail(ctx context.Context, taskID string, summary PipelineSummary, err error) (PipelineSummary, error) {
	status := state.TaskBlocked
	var deferred *StagingDeferredByWatermarkError
	if errors.As(err, &deferred) || errors.Is(err, ErrPipelineCacheWatermark) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		status = state.TaskPaused
	}
	_ = r.State.UpdateTaskStatus(context.Background(), taskID, status, safeError(err))
	if progress, progressErr := r.State.PipelineProgress(context.Background(), taskID); progressErr == nil {
		summary.Final = progress
	}
	return summary, err
}
