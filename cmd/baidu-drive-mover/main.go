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
	scanURL := fs.String("scan", "", "scan a Baidu share link")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "BaiduDriveMover %s (commit %s, built %s)\n", version.Version, version.Commit, version.BuildDate)
		return 0
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
	rawLink := strings.TrimSpace(*scanURL)
	if rawLink == "" {
		fmt.Fprint(stdout, "请粘贴百度网盘分享链接: ")
		line, readErr := reader.ReadString('\n')
		if readErr != nil && line == "" {
			fmt.Fprintf(stderr, "读取分享链接失败: %v\n", readErr)
			return 1
		}
		rawLink = strings.TrimSpace(line)
	}
	cookiePath, err := layout.JoinTemp("auth", "baidu.cookies")
	if err != nil {
		logger.Error("cannot create Baidu cookie path", "error", err)
		return 1
	}
	runner := &app.ScanRunner{
		Layout:      layout,
		Store:       store,
		Browser:     browserauth.NewChromeBaiduLogin(layout),
		Input:       reader,
		Output:      stdout,
		Logger:      logger,
		CookieStore: baidu.CookieStore{Path: cookiePath},
		NewClient: func(cookieHeader string) (app.BaiduAPI, error) {
			return baidu.NewClient(cookieHeader)
		},
	}
	taskID, stats, err := runner.Run(ctx, rawLink)
	if err != nil {
		logger.Error("Baidu share scan failed", "error", err)
		return 1
	}
	fmt.Fprintf(stdout, "扫描完成：%d 个文件夹，%d 个文件，共 %s。\n", stats.Directories, stats.Files, formatBytes(stats.Bytes))
	fmt.Fprintf(stdout, "任务 %s 的目录清单已保存；当前版本不会执行转存或删除。\n", taskID)
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
