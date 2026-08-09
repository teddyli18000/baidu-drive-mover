package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/teddyli18000/baidu-drive-mover/internal/drive"
	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type DriveClient interface {
	EnsureDriveRemote(ctx context.Context) (bool, error)
	ProbeDrive(ctx context.Context) error
	ReauthorizeDriveRemote(ctx context.Context) error
	drive.TreeRemote
}

type DriveClientFactory func(layout *runtimepath.Layout, runner rclone.Runner) (DriveClient, error)
type DriveProvisioner func(ctx context.Context, layout *runtimepath.Layout, runner rclone.Runner, client rclone.HTTPDoer) error

type DriveRunner struct {
	Layout     *runtimepath.Layout
	Store      *state.Store
	Output     io.Writer
	Logger     *slog.Logger
	Process    rclone.Runner
	HTTPClient rclone.HTTPDoer
	Provision  DriveProvisioner
	NewClient  DriveClientFactory
}

type DriveSummary struct {
	RootID        string
	FilesVerified int
	BytesVerified int64
}

func (r *DriveRunner) Run(ctx context.Context, taskID string) (DriveSummary, error) {
	if r == nil || r.Layout == nil || r.Store == nil || r.Output == nil || r.Process == nil || r.HTTPClient == nil {
		return DriveSummary{}, fmt.Errorf("Drive runner is not fully configured")
	}
	provision := r.Provision
	if provision == nil {
		provision = rclone.Provision
	}
	newClient := r.NewClient
	if newClient == nil {
		newClient = func(layout *runtimepath.Layout, runner rclone.Runner) (DriveClient, error) {
			return rclone.NewClient(layout, runner)
		}
	}
	if _, err := r.Store.GetTask(ctx, taskID); err != nil {
		return DriveSummary{}, err
	}
	if err := r.Store.UpdateTaskStatus(ctx, taskID, state.TaskRunning, ""); err != nil {
		return DriveSummary{}, err
	}

	fmt.Fprintln(r.Output, "正在准备受校验的 Google Drive 传输组件……")
	if err := provision(ctx, r.Layout, r.Process, r.HTTPClient); err != nil {
		return r.fail(ctx, taskID, DriveSummary{}, err)
	}
	client, err := newClient(r.Layout, r.Process)
	if err != nil {
		return r.fail(ctx, taskID, DriveSummary{}, err)
	}

	fmt.Fprintln(r.Output, "正在检查 Google Drive 授权；首次使用时浏览器会自动打开。")
	created, err := client.EnsureDriveRemote(ctx)
	if err != nil {
		return r.fail(ctx, taskID, DriveSummary{}, err)
	}
	if created {
		fmt.Fprintln(r.Output, "Google Drive 已按最小权限 drive.file 完成授权。")
	}
	if err := client.ProbeDrive(ctx); err != nil {
		if !errors.Is(err, rclone.ErrDriveAuthRequired) {
			return r.fail(ctx, taskID, DriveSummary{}, err)
		}
		fmt.Fprintln(r.Output, "Google Drive 授权已失效，浏览器将重新打开以刷新授权。")
		if err := client.ReauthorizeDriveRemote(ctx); err != nil {
			return r.fail(ctx, taskID, DriveSummary{}, err)
		}
		if err := client.ProbeDrive(ctx); err != nil {
			return r.fail(ctx, taskID, DriveSummary{}, err)
		}
	}

	builder := &drive.TreeBuilder{State: r.Store, Remote: client}
	rootID, err := builder.Ensure(ctx, taskID)
	if err != nil {
		return r.fail(ctx, taskID, DriveSummary{}, err)
	}
	fmt.Fprintln(r.Output, "Google Drive 任务目录和逻辑目录树已核对。")

	uploader := &drive.Uploader{Layout: r.Layout, State: r.Store, Remote: client}
	uploadSummary, err := uploader.Run(ctx, taskID)
	if err != nil {
		return r.fail(ctx, taskID, DriveSummary{RootID: rootID, FilesVerified: uploadSummary.FilesVerified, BytesVerified: uploadSummary.BytesVerified}, err)
	}
	summary := DriveSummary{RootID: rootID, FilesVerified: uploadSummary.FilesVerified, BytesVerified: uploadSummary.BytesVerified}
	if err := r.Store.UpdateTaskStatus(ctx, taskID, state.TaskPaused, ""); err != nil {
		return summary, err
	}
	if r.Logger != nil {
		r.Logger.Info("verified Google Drive pass completed",
			"task_id", taskID,
			"drive_root_id", rootID,
			"files_verified", summary.FilesVerified,
			"bytes_verified", summary.BytesVerified,
		)
	}
	return summary, nil
}

func (r *DriveRunner) fail(ctx context.Context, taskID string, summary DriveSummary, err error) (DriveSummary, error) {
	status := state.TaskBlocked
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		status = state.TaskPaused
	}
	_ = r.Store.UpdateTaskStatus(context.Background(), taskID, status, safeError(err))
	return summary, err
}
