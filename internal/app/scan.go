package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type BrowserLogin interface {
	Login(ctx context.Context) (string, error)
}

// A scan pass may refresh the dedicated browser session once after the
// service reports that the current authentication has expired. Keeping this
// bound explicit prevents a persistent auth failure from prompting forever.
const maxScanAuthRetries = 1

type BaiduAPI interface {
	AccessSharePage(ctx context.Context, link baidu.ShareLink) (baidu.ShareContext, error)
	VerifyPassword(ctx context.Context, link baidu.ShareLink, share baidu.ShareContext, password string) error
	Scan(ctx context.Context, taskID string, link baidu.ShareLink, share baidu.ShareContext, sink manifest.Sink) error
	CookieString() string
	HasLoginCookies() bool
}

type ClientFactory func(cookieHeader string) (BaiduAPI, error)

type ScanRunner struct {
	Layout      *runtimepath.Layout
	Store       *state.Store
	Browser     BrowserLogin
	NewClient   ClientFactory
	Input       *bufio.Reader
	Output      io.Writer
	Logger      *slog.Logger
	CookieStore baidu.CookieStore
}

func (r *ScanRunner) Run(ctx context.Context, rawLink string) (string, manifest.Stats, error) {
	if r == nil || r.Layout == nil || r.Store == nil || r.Browser == nil || r.NewClient == nil || r.Input == nil || r.Output == nil {
		return "", manifest.Stats{}, fmt.Errorf("scan runner is not fully configured")
	}
	link, err := baidu.ParseShareLink(rawLink)
	if err != nil {
		return "", manifest.Stats{}, err
	}
	taskID, err := newTaskID()
	if err != nil {
		return "", manifest.Stats{}, err
	}
	if err := r.Store.CreateTask(ctx, state.Task{
		ID:             taskID,
		ShareURL:       link.SanitizedURL(),
		ExtractionCode: link.Password,
		Status:         state.TaskScanning,
	}); err != nil {
		return "", manifest.Stats{}, err
	}
	return r.scanTask(ctx, taskID, link)
}

// Resume continues an existing incomplete scan with the same durable task ID.
// Re-scanning is safe because manifest writes enforce stable source identity.
func (r *ScanRunner) Resume(ctx context.Context, task state.Task) (string, manifest.Stats, error) {
	if err := r.validate(); err != nil {
		return task.ID, manifest.Stats{}, err
	}
	if task.ID == "" || task.ShareURL == "" {
		return task.ID, manifest.Stats{}, fmt.Errorf("resumable task identity is incomplete")
	}
	if task.ScanCompleted {
		return task.ID, manifest.Stats{}, fmt.Errorf("task %q scan is already complete", task.ID)
	}
	link, err := baidu.ParseShareLink(task.ShareURL)
	if err != nil {
		return task.ID, manifest.Stats{}, fmt.Errorf("parse stored share link: %w", err)
	}
	link.Password = task.ExtractionCode
	if err := r.Store.UpdateTaskStatus(ctx, task.ID, state.TaskScanning, ""); err != nil {
		return task.ID, manifest.Stats{}, err
	}
	return r.scanTask(ctx, task.ID, link)
}

func (r *ScanRunner) validate() error {
	if r == nil || r.Layout == nil || r.Store == nil || r.Browser == nil || r.NewClient == nil || r.Input == nil || r.Output == nil {
		return fmt.Errorf("scan runner is not fully configured")
	}
	return nil
}

func (r *ScanRunner) scanTask(ctx context.Context, taskID string, link baidu.ShareLink) (string, manifest.Stats, error) {

	fail := func(runErr error) (string, manifest.Stats, error) {
		status := state.TaskBlocked
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = state.TaskPaused
		}
		_ = r.Store.UpdateTaskStatus(context.Background(), taskID, status, safeError(runErr))
		return taskID, manifest.Stats{}, runErr
	}

	cookieValue, err := r.CookieStore.Load()
	if err != nil {
		return fail(err)
	}
	api, err := r.NewClient(cookieValue)
	if err != nil {
		return fail(err)
	}
	if !api.HasLoginCookies() {
		api, err = r.loginAndCreateClient(ctx)
		if err != nil {
			return fail(err)
		}
	}
	share, err := api.AccessSharePage(ctx, link)
	if errors.Is(err, baidu.ErrAuthRequired) {
		api, err = r.loginAndCreateClient(ctx)
		if err != nil {
			return fail(err)
		}
		share, err = api.AccessSharePage(ctx, link)
	}
	if err != nil {
		return fail(err)
	}

	if link.Password != "" {
		password, verifyErr := r.verifyPassword(ctx, api, link, share, link.Password)
		if verifyErr != nil {
			return fail(verifyErr)
		}
		link.Password = password
		if err := r.Store.UpdateTaskExtractionCode(ctx, taskID, password); err != nil {
			return fail(err)
		}
		if err := r.CookieStore.Save(api.CookieString()); err != nil {
			return fail(err)
		}
	}

	authRetries := 0
	for {
		err = api.Scan(ctx, taskID, link, share, r.Store)
		if errors.Is(err, baidu.ErrAuthRequired) && authRetries < maxScanAuthRetries {
			authRetries++
			api, err = r.loginAndCreateClient(ctx)
			if err != nil {
				return fail(err)
			}
			// ShareContext contains session-bound values such as BDSToken. A
			// newly authenticated client must never continue with the stale
			// context that caused the authentication failure.
			share, err = api.AccessSharePage(ctx, link)
			if err != nil {
				return fail(err)
			}
			if link.Password != "" {
				if err := api.VerifyPassword(ctx, link, share, link.Password); err != nil {
					return fail(err)
				}
				if err := r.CookieStore.Save(api.CookieString()); err != nil {
					return fail(err)
				}
			}
			continue
		}
		if !errors.Is(err, baidu.ErrPasswordRequired) {
			break
		}
		password, verifyErr := r.verifyPassword(ctx, api, link, share, "")
		if verifyErr != nil {
			return fail(verifyErr)
		}
		link.Password = password
		if err := r.Store.UpdateTaskExtractionCode(ctx, taskID, password); err != nil {
			return fail(err)
		}
		if err := r.CookieStore.Save(api.CookieString()); err != nil {
			return fail(err)
		}
	}
	if err != nil {
		return fail(err)
	}
	stats, err := r.Store.ManifestStats(ctx, taskID)
	if err != nil {
		return fail(err)
	}
	if err := r.Store.CompleteTaskScan(ctx, taskID); err != nil {
		return fail(err)
	}
	if r.Logger != nil {
		r.Logger.Info("Baidu share scan completed", "task_id", taskID, "directories", stats.Directories, "files", stats.Files, "bytes", stats.Bytes)
	}
	return taskID, stats, nil
}

func (r *ScanRunner) loginAndCreateClient(ctx context.Context) (BaiduAPI, error) {
	fmt.Fprintln(r.Output, "需要登录百度网盘，已打开独立 Chrome 登录窗口。完成登录后会自动继续。")
	cookieValue, err := r.Browser.Login(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.CookieStore.Save(cookieValue); err != nil {
		return nil, err
	}
	return r.NewClient(cookieValue)
}

func (r *ScanRunner) verifyPassword(ctx context.Context, api BaiduAPI, link baidu.ShareLink, share baidu.ShareContext, initial string) (string, error) {
	password := strings.TrimSpace(initial)
	for attempts := 0; attempts < 5; attempts++ {
		if password == "" {
			fmt.Fprint(r.Output, "请输入分享提取码: ")
			line, err := r.Input.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return "", fmt.Errorf("read extraction code: %w", err)
			}
			password = strings.TrimSpace(line)
			if password == "" {
				if errors.Is(err, io.EOF) {
					return "", io.EOF
				}
				continue
			}
		}
		err := api.VerifyPassword(ctx, link, share, password)
		if err == nil {
			return password, nil
		}
		if !errors.Is(err, baidu.ErrWrongPassword) {
			return "", err
		}
		fmt.Fprintln(r.Output, "提取码不正确，请重新输入。")
		password = ""
	}
	return "", fmt.Errorf("too many incorrect extraction-code attempts: %w", baidu.ErrWrongPassword)
}

func newTaskID() (string, error) {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate task ID: %w", err)
	}
	return "task-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(buffer), nil
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}
