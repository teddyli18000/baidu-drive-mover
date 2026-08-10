package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/teddyli18000/baidu-drive-mover/internal/app"
	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/browserauth"
	downloadengine "github.com/teddyli18000/baidu-drive-mover/internal/download"
	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
	"github.com/teddyli18000/baidu-drive-mover/internal/logsafe"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
	"github.com/teddyli18000/baidu-drive-mover/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("BaiduDriveMover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version")
	checkOnly := fs.Bool("check", false, "validate local runtime/state setup and exit")
	listTasks := fs.Bool("list", false, "list resumable tasks and exit")
	resumeTaskID := fs.String("resume", "", "resume a specific task ID")
	forceNew := fs.Bool("new", false, "start a new task instead of resuming")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "BaiduDriveMover %s (commit %s, built %s)\n", version.Version, version.Commit, version.BuildDate)
		return 0
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "请直接启动程序后粘贴分享链接，不要把链接放在命令行参数里。")
		return 2
	}
	modeCount := 0
	for _, selected := range []bool{*checkOnly, *listTasks, *resumeTaskID != "", *forceNew} {
		if selected {
			modeCount++
		}
	}
	if modeCount > 1 {
		fmt.Fprintln(stderr, "-check、-list、-resume 和 -new 不能同时使用。")
		return 2
	}

	layout, err := runtimepath.FromExecutable()
	if err != nil {
		fmt.Fprintf(stderr, "Startup failed: %v\n", err)
		return 1
	}
	if err := layout.Ensure(); err != nil {
		fmt.Fprintf(stderr, "Startup failed: cannot safely initialize ./temp/: %v\n", err)
		return 1
	}
	instanceLock, err := layout.AcquireInstanceLock()
	if err != nil {
		fmt.Fprintf(stderr, "Startup failed: %v\n", err)
		return 1
	}
	defer instanceLock.Close()

	logFile, err := layout.OpenTempFile("logs/baidu-drive-mover.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "Startup failed: cannot open log under ./temp/: %v\n", err)
		return 1
	}
	defer logFile.Close()
	logger := logsafe.NewLogger(io.MultiWriter(stdout, logFile), slog.LevelInfo)

	store, err := state.Open(layout.StateDB)
	if err != nil {
		logger.Error("state database initialization failed", "error", err)
		return 1
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("BaiduDriveMover started", "version", version.Version)
	if *checkOnly {
		v, err := store.SchemaVersion(ctx)
		if err != nil {
			logger.Error("state database check failed", "error", err)
			return 1
		}
		logger.Info("local safety check passed", "schema_version", v, "runtime_root", layout.Temp)
		return 0
	}

	reader := bufio.NewReader(stdin)
	var resumable []state.Task
	var selectedTask *state.Task
	if *resumeTaskID != "" {
		task, getErr := store.GetTask(ctx, *resumeTaskID)
		if getErr != nil || !isResumableTask(task) {
			fmt.Fprintf(stderr, "任务 %q 不存在、已完成或已永久失败；使用 -list 查看可恢复任务。\n", *resumeTaskID)
			return 2
		}
		selectedTask = &task
	} else if !*forceNew {
		resumable, err = store.ListResumableTasks(ctx)
		if err != nil {
			logger.Error("cannot list resumable tasks", "error", err)
			return 1
		}
	}
	if *listTasks {
		printResumableTasks(stdout, resumable)
		return 0
	}
	if selectedTask == nil {
		selectedTask, err = selectResumableTask(resumable, "", *forceNew)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	cookieStore := baidu.NewCookieStore(layout)
	browser := browserauth.NewChromeBaiduLogin(layout)

	scanRunner := &app.ScanRunner{
		Layout:      layout,
		Store:       store,
		Browser:     browser,
		Input:       reader,
		Output:      stdout,
		Logger:      logger,
		CookieStore: cookieStore,
		NewClient: func(cookieHeader string) (app.BaiduAPI, error) {
			return baidu.NewClient(cookieHeader)
		},
	}
	var taskID string
	var stats manifest.Stats
	if selectedTask != nil {
		taskID = selectedTask.ID
		if selectedTask.ScanCompleted {
			stats, err = store.ManifestStats(ctx, taskID)
			if err == nil {
				fmt.Fprintf(stdout, "继续任务 %s（%s）：%d 个文件夹，%d 个文件，共 %s。\n", taskID, selectedTask.Status, stats.Directories, stats.Files, formatBytes(stats.Bytes))
			}
		} else {
			fmt.Fprintf(stdout, "继续任务 %s 的未完成扫描。\n", taskID)
			taskID, stats, err = scanRunner.Resume(ctx, *selectedTask)
		}
	} else {
		fmt.Fprint(stdout, "请粘贴百度网盘分享链接: ")
		line, readErr := reader.ReadString('\n')
		if readErr != nil && line == "" {
			fmt.Fprintf(stderr, "读取分享链接失败: %v\n", readErr)
			return 1
		}
		taskID, stats, err = scanRunner.Run(ctx, strings.TrimSpace(line))
	}
	if err != nil {
		if taskID != "" && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil) {
			fmt.Fprintf(stdout, "任务 %s 已暂停；下次启动会继续，不会使用不完整清单开始传输。\n", taskID)
			return 0
		}
		logger.Error("Baidu share scan failed", "error", err)
		if taskID != "" {
			fmt.Fprintf(stderr, "任务 %s 的扫描停止在安全状态，可用 -resume %s 重试。\n", taskID, taskID)
		}
		return 1
	}
	if selectedTask == nil || !selectedTask.ScanCompleted {
		fmt.Fprintf(stdout, "扫描完成：%d 个文件夹，%d 个文件，共 %s。\n", stats.Directories, stats.Files, formatBytes(stats.Bytes))
	}

	const maxCacheBytes = downloadengine.DefaultMaxCacheBytes
	stageRunner := &app.StageRunner{
		Store:         store,
		Browser:       browser,
		Output:        stdout,
		Logger:        logger,
		CookieStore:   cookieStore,
		MaxBatches:    1,
		MaxCacheBytes: maxCacheBytes,
		NewClient: func(cookieHeader string) (app.StagingBaiduAPI, error) {
			return baidu.NewClient(cookieHeader)
		},
	}
	downloadRunner := &app.DownloadRunner{
		Layout:        layout,
		Store:         store,
		Browser:       browser,
		Output:        stdout,
		Logger:        logger,
		CookieStore:   cookieStore,
		MaxCacheBytes: maxCacheBytes,
		NewClient: func(cookieHeader string) (app.DownloadBaiduAPI, error) {
			return baidu.NewClient(cookieHeader)
		},
	}
	driveRunner := &app.DriveRunner{
		Layout:     layout,
		Store:      store,
		Output:     stdout,
		Logger:     logger,
		Process:    rclone.OSRunner{},
		HTTPClient: rclone.SecureHTTPClient(),
	}
	cleanupRunner := &app.CleanupRunner{
		Layout:      layout,
		Store:       store,
		Browser:     browser,
		Output:      stdout,
		Logger:      logger,
		CookieStore: cookieStore,
		MaxBatches:  8,
		NewClient: func(cookieHeader string) (app.CleanupBaiduAPI, error) {
			return baidu.NewClient(cookieHeader)
		},
	}

	pipeline := &app.PipelineRunner{
		State:         store,
		Logger:        logger,
		MaxCacheBytes: maxCacheBytes,
		Cleanup:       cleanupRunner.Run,
		Drive:         driveRunner.Run,
		Download:      downloadRunner.Run,
		Stage: func(passCtx context.Context, passTaskID string, maxBatchBytes int64) (app.StageSummary, error) {
			stageRunner.MaxBatchBytes = maxBatchBytes
			return stageRunner.Run(passCtx, passTaskID)
		},
	}

	fmt.Fprintf(stdout, "开始完整流水线：百度暂存 → 本地断点下载 → Drive 核验 → 工具自有暂存清理；本地缓存水位 %s。\n", formatBytes(maxCacheBytes))
	pipelineSummary, err := pipeline.Run(ctx, taskID)
	if err != nil {
		var deferred *app.StagingDeferredByWatermarkError
		if errors.As(err, &deferred) || errors.Is(err, app.ErrPipelineCacheWatermark) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			fmt.Fprintf(stdout, "任务 %s 已暂停，所有已持久化断点会在下次启动时继续。\n", taskID)
			return 0
		}
		logger.Error("full pipeline stopped", "task_id", taskID, "error", err)
		fmt.Fprintf(stderr, "任务 %s 已停止在安全状态；不会扩大清理范围。错误：%v\n", taskID, err)
		return 1
	}

	fmt.Fprintf(stdout,
		"任务 %s 完成：%d/%d 个文件 DONE；Drive 本轮核验 %d 个文件（%s），自动清理 %d 个暂存批次并释放 %s 本地缓存。\n",
		taskID,
		pipelineSummary.Final.Done,
		pipelineSummary.Final.Total,
		pipelineSummary.FilesVerified,
		formatBytes(pipelineSummary.BytesVerified),
		pipelineSummary.BatchesCleaned,
		formatBytes(pipelineSummary.BytesFreed),
	)
	fmt.Fprintln(stdout, "Google Drive 目标文件不会被自动删除；百度回收站也不会被自动清空。")
	if task, taskErr := store.GetTask(ctx, taskID); taskErr == nil && task.DriveRootName != "" {
		fmt.Fprintf(stdout, "Drive 目标文件夹：%s（任务完成前请勿移动或重命名）。\n", task.DriveRootName)
	}
	return 0
}

func selectResumableTask(tasks []state.Task, requestedID string, forceNew bool) (*state.Task, error) {
	if forceNew {
		return nil, nil
	}
	if requestedID != "" {
		for i := range tasks {
			if tasks[i].ID == requestedID {
				return &tasks[i], nil
			}
		}
		return nil, fmt.Errorf("任务 %q 不存在、已完成或需要人工处理；使用 -list 查看可恢复任务。", requestedID)
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	return &tasks[0], nil
}

func isResumableTask(task state.Task) bool {
	switch task.Status {
	case state.TaskNew, state.TaskAuthRequired, state.TaskScanning, state.TaskRunning, state.TaskPaused, state.TaskBlocked:
		return true
	default:
		return false
	}
}

func printResumableTasks(output io.Writer, tasks []state.Task) {
	if len(tasks) == 0 {
		fmt.Fprintln(output, "没有可恢复任务。")
		return
	}
	fmt.Fprintln(output, "可恢复任务（默认继续最近更新的一项）：")
	for _, task := range tasks {
		scanState := "扫描未完成"
		if task.ScanCompleted {
			scanState = "扫描完成"
		}
		fmt.Fprintf(output, "  %s  %s  %s  %s\n", task.ID, task.Status, scanState, task.ShareURL)
	}
}

func formatBytes(bytes int64) string {
	const unit = int64(1024)
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := unit, 0
	for n := bytes / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
