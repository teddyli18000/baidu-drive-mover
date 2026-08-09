package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/staging"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

const defaultStagingBatchSize = 200

type StagingBaiduAPI interface {
	AccessSharePage(ctx context.Context, link baidu.ShareLink) (baidu.ShareContext, error)
	VerifyPassword(ctx context.Context, link baidu.ShareLink, share baidu.ShareContext, password string) error
	CookieString() string
	HasLoginCookies() bool
	staging.Remote
}

type StageClientFactory func(cookieHeader string) (StagingBaiduAPI, error)

type StageRunner struct {
	Store       *state.Store
	Browser     BrowserLogin
	NewClient   StageClientFactory
	CookieStore baidu.CookieStore
	Output      io.Writer
	Logger      *slog.Logger
	BatchSize   int
}

type StageSummary struct {
	FilesStaged int
}

func (r *StageRunner) Run(ctx context.Context, taskID string) (StageSummary, error) {
	if r == nil || r.Store == nil || r.Browser == nil || r.NewClient == nil || r.Output == nil {
		return StageSummary{}, fmt.Errorf("stage runner is not fully configured")
	}
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return StageSummary{}, err
	}
	link, err := baidu.ParseShareLink(task.ShareURL)
	if err != nil {
		return StageSummary{}, fmt.Errorf("reload stored Baidu share link: %w", err)
	}
	batchSize := r.BatchSize
	if batchSize <= 0 {
		batchSize = defaultStagingBatchSize
	}
	if _, err := r.Store.PlanBatches(ctx, taskID, batchSize); err != nil {
		return StageSummary{}, err
	}
	if err := r.Store.UpdateTaskStatus(ctx, taskID, state.TaskRunning, ""); err != nil {
		return StageSummary{}, err
	}

	var runErr error
	for sessionAttempt := 0; sessionAttempt < 2; sessionAttempt++ {
		forceLogin := sessionAttempt > 0
		api, share, sessionErr := r.prepareSession(ctx, link, task.ExtractionCode, forceLogin)
		if sessionErr != nil {
			if errors.Is(sessionErr, baidu.ErrAuthRequired) && sessionAttempt == 0 {
				continue
			}
			runErr = sessionErr
			break
		}
		executor := &staging.Executor{
			Repository: r.Store,
			Remote:     api,
			Link:       link,
			Share:      share,
		}
		runErr = executor.Run(ctx, taskID)
		if errors.Is(runErr, baidu.ErrAuthRequired) && sessionAttempt == 0 {
			continue
		}
		break
	}
	if runErr != nil {
		status := state.TaskPaused
		if errors.Is(runErr, baidu.ErrAuthRequired) || errors.Is(runErr, baidu.ErrVerificationRequired) || errors.Is(runErr, baidu.ErrQuotaExceeded) {
			status = state.TaskBlocked
		}
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = state.TaskPaused
		}
		_ = r.Store.UpdateTaskStatus(context.Background(), taskID, status, safeError(runErr))
		return StageSummary{}, runErr
	}

	staged, err := r.Store.CountFilesByStatus(ctx, taskID, state.FileBaiduStaged)
	if err != nil {
		return StageSummary{}, err
	}
	if err := r.Store.UpdateTaskStatus(ctx, taskID, state.TaskPaused, ""); err != nil {
		return StageSummary{}, err
	}
	if r.Logger != nil {
		r.Logger.Info("Baidu staging completed", "task_id", taskID, "files_staged", staged)
	}
	return StageSummary{FilesStaged: staged}, nil
}

func (r *StageRunner) prepareSession(ctx context.Context, link baidu.ShareLink, extractionCode string, forceLogin bool) (StagingBaiduAPI, baidu.ShareContext, error) {
	cookieValue, err := r.CookieStore.Load()
	if err != nil {
		return nil, baidu.ShareContext{}, err
	}
	api, err := r.NewClient(cookieValue)
	if err != nil {
		return nil, baidu.ShareContext{}, err
	}
	if forceLogin || !api.HasLoginCookies() {
		fmt.Fprintln(r.Output, "百度登录需要刷新，已打开独立 Chrome 窗口。完成登录后会自动继续。")
		cookieValue, err = r.Browser.Login(ctx)
		if err != nil {
			return nil, baidu.ShareContext{}, err
		}
		if err := r.CookieStore.Save(cookieValue); err != nil {
			return nil, baidu.ShareContext{}, err
		}
		api, err = r.NewClient(cookieValue)
		if err != nil {
			return nil, baidu.ShareContext{}, err
		}
	}
	share, err := api.AccessSharePage(ctx, link)
	if errors.Is(err, baidu.ErrAuthRequired) && !forceLogin {
		return nil, baidu.ShareContext{}, baidu.ErrAuthRequired
	}
	if err != nil {
		return nil, baidu.ShareContext{}, err
	}
	if extractionCode != "" {
		if err := api.VerifyPassword(ctx, link, share, extractionCode); err != nil {
			return nil, baidu.ShareContext{}, err
		}
		if err := r.CookieStore.Save(api.CookieString()); err != nil {
			return nil, baidu.ShareContext{}, err
		}
	}
	return api, share, nil
}
