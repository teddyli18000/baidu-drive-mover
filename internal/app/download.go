package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/download"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type DownloadBaiduAPI interface {
	HasLoginCookies() bool
	download.Remote
}

type DownloadClientFactory func(cookieHeader string) (DownloadBaiduAPI, error)

type DownloadRunner struct {
	Layout        *runtimepath.Layout
	Store         *state.Store
	Browser       BrowserLogin
	NewClient     DownloadClientFactory
	CookieStore   baidu.CookieStore
	Output        io.Writer
	Logger        *slog.Logger
	MaxCacheBytes int64
}

func (r *DownloadRunner) Run(ctx context.Context, taskID string) (download.Summary, error) {
	if r == nil || r.Layout == nil || r.Store == nil || r.Browser == nil || r.NewClient == nil || r.Output == nil {
		return download.Summary{}, fmt.Errorf("download runner is not fully configured")
	}
	if _, err := r.Store.GetTask(ctx, taskID); err != nil {
		return download.Summary{}, err
	}
	if err := r.Store.UpdateTaskStatus(ctx, taskID, state.TaskRunning, ""); err != nil {
		return download.Summary{}, err
	}

	var (
		summary download.Summary
		runErr  error
	)
	for sessionAttempt := 0; sessionAttempt < 2; sessionAttempt++ {
		api, err := r.prepareDownloadClient(ctx, sessionAttempt > 0)
		if err != nil {
			runErr = err
			break
		}
		engine := &download.Engine{
			Layout:        r.Layout,
			Repository:    r.Store,
			Remote:        api,
			MaxCacheBytes: r.MaxCacheBytes,
		}
		summary, runErr = engine.Run(ctx, taskID)
		if errors.Is(runErr, baidu.ErrAuthRequired) && sessionAttempt == 0 {
			continue
		}
		break
	}

	if runErr != nil {
		status := state.TaskPaused
		var oversized *download.OversizedCacheFileError
		if errors.As(runErr, &oversized) ||
			errors.Is(runErr, baidu.ErrAuthRequired) ||
			errors.Is(runErr, baidu.ErrVerificationRequired) ||
			errors.Is(runErr, baidu.ErrQuotaExceeded) {
			status = state.TaskBlocked
		}
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = state.TaskPaused
		}
		_ = r.Store.UpdateTaskStatus(context.Background(), taskID, status, safeError(runErr))
		return summary, runErr
	}

	if err := r.Store.UpdateTaskStatus(ctx, taskID, state.TaskPaused, ""); err != nil {
		return summary, err
	}
	if r.Logger != nil {
		r.Logger.Info("bounded Baidu download pass completed",
			"task_id", taskID,
			"files_ready", summary.FilesReady,
			"bytes_ready", summary.BytesReady,
			"paused_by_watermark", summary.PausedByWatermark,
		)
	}
	return summary, nil
}

func (r *DownloadRunner) prepareDownloadClient(ctx context.Context, forceLogin bool) (DownloadBaiduAPI, error) {
	cookieValue, err := r.CookieStore.Load()
	if err != nil {
		return nil, err
	}
	api, err := r.NewClient(cookieValue)
	if err != nil {
		return nil, err
	}
	if forceLogin || !api.HasLoginCookies() {
		fmt.Fprintln(r.Output, "百度登录需要刷新，已打开独立 Chrome 窗口。完成登录后会自动继续。")
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
