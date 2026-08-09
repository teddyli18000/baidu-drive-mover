package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	cleanupengine "github.com/teddyli18000/baidu-drive-mover/internal/cleanup"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type CleanupBaiduAPI interface {
	HasLoginCookies() bool
	cleanupengine.Remote
}

type CleanupClientFactory func(cookieHeader string) (CleanupBaiduAPI, error)

type CleanupRunner struct {
	Layout      *runtimepath.Layout
	Store       *state.Store
	Browser     BrowserLogin
	NewClient   CleanupClientFactory
	CookieStore baidu.CookieStore
	Output      io.Writer
	Logger      *slog.Logger
	MaxBatches  int
}

func (r *CleanupRunner) Run(ctx context.Context, taskID string) (cleanupengine.Summary, error) {
	if r == nil || r.Layout == nil || r.Store == nil || r.Browser == nil || r.NewClient == nil || r.Output == nil {
		return cleanupengine.Summary{}, fmt.Errorf("cleanup runner is not fully configured")
	}
	if _, err := r.Store.GetTask(ctx, taskID); err != nil {
		return cleanupengine.Summary{}, err
	}
	candidates, err := r.Store.CleanupCandidates(ctx, taskID, r.MaxBatches)
	if err != nil {
		return cleanupengine.Summary{}, err
	}
	rootCandidate, err := r.Store.TaskRootCleanupCandidate(ctx, taskID)
	if err != nil {
		return cleanupengine.Summary{}, err
	}
	if len(candidates) == 0 && !rootCandidate {
		return cleanupengine.Summary{}, nil
	}
	if err := r.Store.UpdateTaskStatus(ctx, taskID, state.TaskRunning, ""); err != nil {
		return cleanupengine.Summary{}, err
	}

	var (
		summary cleanupengine.Summary
		runErr  error
	)
	for sessionAttempt := 0; sessionAttempt < 2; sessionAttempt++ {
		api, err := r.prepareCleanupClient(ctx, sessionAttempt > 0)
		if err != nil {
			runErr = err
			break
		}
		engine := &cleanupengine.Engine{
			Layout:     r.Layout,
			Repository: r.Store,
			Remote:     api,
			MaxBatches: r.MaxBatches,
		}
		summary, runErr = engine.Run(ctx, taskID)
		if errors.Is(runErr, baidu.ErrAuthRequired) && sessionAttempt == 0 {
			continue
		}
		break
	}
	if runErr != nil {
		status := state.TaskPaused
		if errors.Is(runErr, baidu.ErrAuthRequired) ||
			errors.Is(runErr, baidu.ErrVerificationRequired) ||
			errors.Is(runErr, baidu.ErrQuotaExceeded) {
			status = state.TaskBlocked
		}
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || ctx.Err() != nil {
			status = state.TaskPaused
		}
		_ = r.Store.UpdateTaskStatus(context.Background(), taskID, status, safeError(runErr))
		return summary, runErr
	}
	if err := r.Store.UpdateTaskStatus(ctx, taskID, state.TaskPaused, ""); err != nil {
		return summary, err
	}
	if r.Logger != nil && (summary.BatchesDone > 0 || summary.TaskRootDone) {
		r.Logger.Info("tool-owned cleanup pass completed",
			"task_id", taskID,
			"batches_done", summary.BatchesDone,
			"files_done", summary.FilesDone,
			"bytes_freed", summary.BytesFreed,
			"task_root_done", summary.TaskRootDone,
		)
	}
	return summary, nil
}

func (r *CleanupRunner) prepareCleanupClient(ctx context.Context, forceLogin bool) (CleanupBaiduAPI, error) {
	cookieValue, err := r.CookieStore.Load()
	if err != nil {
		return nil, err
	}
	api, err := r.NewClient(cookieValue)
	if err != nil {
		return nil, err
	}
	if forceLogin || !api.HasLoginCookies() {
		fmt.Fprintln(r.Output, "百度登录需要刷新，已打开独立 Chrome 窗口。完成登录后会继续清理工具自己的暂存目录。")
		cookieValue, err = r.Browser.Login(ctx)
		if err != nil {
			return nil, err
		}
		if err := r.CookieStore.Save(cookieValue); err != nil {
			return nil, err
		}
		api, err = r.NewClient(cookieValue)
		if err != nil {
			return nil, err
		}
	}
	return api, nil
}
