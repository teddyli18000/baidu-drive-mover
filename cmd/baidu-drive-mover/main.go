package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/teddyli18000/baidu-drive-mover/internal/app"
	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/browserauth"
	downloadengine "github.com/teddyli18000/baidu-drive-mover/internal/download"
	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
	"github.com/teddyli18000/baidu-drive-mover/internal/logsafe"
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

	layout, err := runtimepath.FromExecutable()
	if err != nil {
		fmt.Fprintf(stderr, "Startup failed: %v\n", err)
		return 1
	}
	if err := layout.Ensure(); err != nil {
		fmt.Fprintf(stderr, "Startup failed: cannot safely initialize ./temp/: %v\n", err)
		return 1
	}

	logPath := filepath.Join(layout.Logs, "baidu-drive-mover.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
	fmt.Fprint(stdout, "请粘贴百度网盘分享链接: ")
	line, readErr := reader.ReadString('\n')
	if readErr != nil && line == "" {
		fmt.Fprintf(stderr, "读取分享链接失败: %v\n", readErr)
		return 1
	}
	rawLink := strings.TrimSpace(line)
	cookiePath, err := layout.JoinTemp("auth", "baidu.cookies")
	if err != nil {
		logger.Error("cannot create Baidu cookie path", "error", err)
		return 1
	}
	cookieStore := baidu.CookieStore{Path: cookiePath}
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
	taskID, stats, err := scanRunner.Run(ctx, rawLink)
	if err != nil {
		logger.Error("Baidu share scan failed", "error", err)
		return 1
	}
	fmt.Fprintf(stdout, "扫描完成：%d 个文件夹，%d 个文件，共 %s。\n", stats.Directories, stats.Files, formatBytes(stats.Bytes))

	const maxCacheBytes = downloadengine.DefaultMaxCacheBytes
	reserved, err := store.ReservedCacheBytes(ctx)
	if err != nil {
		logger.Error("cannot calculate local cache reservation", "task_id", taskID, "error", err)
		return 1
	}
	available := maxCacheBytes - reserved
	if available <= 0 {
		_ = store.UpdateTaskStatus(context.Background(), taskID, state.TaskBlocked, "local cache watermark is fully reserved")
		fmt.Fprintf(stdout, "本地缓存已达到 %s 水位，任务 %s 已安全保留，未继续转存。\n", formatBytes(maxCacheBytes), taskID)
		return 0
	}

	fmt.Fprintf(stdout, "开始转存一个受控批次（当前最多允许 %s）……\n", formatBytes(available))
	stageRunner := &app.StageRunner{
		Store:         store,
		Browser:       browser,
		Output:        stdout,
		Logger:        logger,
		CookieStore:   cookieStore,
		MaxBatches:    1,
		MaxBatchBytes: available,
		NewClient: func(cookieHeader string) (app.StagingBaiduAPI, error) {
			return baidu.NewClient(cookieHeader)
		},
	}
	stageSummary, err := stageRunner.Run(ctx, taskID)
	if err != nil {
		logger.Error("bounded Baidu staging failed", "task_id", taskID, "error", err)
		fmt.Fprintf(stderr, "任务 %s 已保留断点；没有自动清理任何百度文件。\n", taskID)
		return 1
	}
	fmt.Fprintf(stdout, "百度暂存通过核对：当前累计 %d 个文件。\n", stageSummary.FilesStaged)

	fmt.Fprintln(stdout, "开始下载到 ./temp/cache/，支持断点续传和重启恢复……")
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
	downloadSummary, err := downloadRunner.Run(ctx, taskID)
	if err != nil {
		logger.Error("Baidu download pass failed", "task_id", taskID, "error", err)
		fmt.Fprintf(stderr, "任务 %s 已保留下载断点；下次恢复会重新校验已有缓存。\n", taskID)
		return 1
	}
	fmt.Fprintf(stdout, "本轮下载完成：%d 个文件，%s 已通过本地校验。\n", downloadSummary.FilesReady, formatBytes(downloadSummary.BytesReady))
	if downloadSummary.PausedByWatermark {
		fmt.Fprintf(stdout, "已达到 %s 本地缓存水位，后续百度转存会保持暂停。\n", formatBytes(maxCacheBytes))
	}

	fmt.Fprintln(stdout, "开始重建 Google Drive 目录树并上传本轮本地文件；每个文件上传后会独立核对 ID、大小和 MD5……")
	driveRunner := &app.DriveRunner{
		Layout:     layout,
		Store:      store,
		Output:     stdout,
		Logger:     logger,
		Process:    rclone.OSRunner{},
		HTTPClient: rclone.SecureHTTPClient(),
	}
	driveSummary, err := driveRunner.Run(ctx, taskID)
	if err != nil {
		logger.Error("Google Drive pass failed", "task_id", taskID, "error", err)
		fmt.Fprintf(stderr, "任务 %s 已保留全部百度暂存、本地缓存和 Drive 断点；未执行任何自动清理。\n", taskID)
		return 1
	}
	fmt.Fprintf(stdout, "Google Drive 本轮核验完成：%d 个文件，%s；任务根目录 ID 已持久化。\n", driveSummary.FilesVerified, formatBytes(driveSummary.BytesVerified))
	fmt.Fprintf(stdout, "任务 %s 已安全保留。当前 v0.5 不删除百度暂存，也不删除 ./temp/cache/；清理与流水线回压将在 v0.6 启用。\n", taskID)
	return 0
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
