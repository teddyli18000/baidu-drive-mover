package browserauth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
)

type ChromeBaiduLogin struct {
	Layout       *runtimepath.Layout
	PollInterval time.Duration
}

func NewChromeBaiduLogin(layout *runtimepath.Layout) *ChromeBaiduLogin {
	return &ChromeBaiduLogin{Layout: layout, PollInterval: time.Second}
}

func (l *ChromeBaiduLogin) Login(ctx context.Context) (string, error) {
	if l == nil || l.Layout == nil {
		return "", fmt.Errorf("Chrome login runtime layout is missing")
	}
	chromePath, err := FindChrome()
	if err != nil {
		return "", err
	}
	if err := l.Layout.Ensure(); err != nil {
		return "", err
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.UserDataDir(l.Layout.ChromeProfile),
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-background-networking", false),
		chromedp.Flag("disable-crash-reporter", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("noerrdialogs", true),
		chromedp.Flag("disk-cache-dir", l.Layout.BrowserCache),
		chromedp.Env("TEMP="+l.Layout.BrowserTemp, "TMP="+l.Layout.BrowserTemp),
	)
	allocatorCtx, allocatorCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocatorCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx)
	defer browserCancel()
	if err := chromedp.Run(browserCtx, network.Enable(), chromedp.Navigate("https://pan.baidu.com/disk/main")); err != nil {
		return "", fmt.Errorf("open Baidu login in Chrome: %w", err)
	}

	poll := l.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		cookies, err := getBaiduCookies(browserCtx)
		if err == nil {
			value, ready := serializeLoginCookies(cookies)
			if ready {
				return value, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func getBaiduCookies(ctx context.Context) ([]*network.Cookie, error) {
	var cookies []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		cookies, err = network.GetCookies().WithURLs([]string{"https://pan.baidu.com/", "https://www.baidu.com/"}).Do(actionCtx)
		return err
	}))
	return cookies, err
}

func serializeLoginCookies(cookies []*network.Cookie) (string, bool) {
	values := make(map[string]string)
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if domain != "baidu.com" && !strings.HasSuffix(domain, ".baidu.com") {
			continue
		}
		values[cookie.Name] = cookie.Value
	}
	if values["BDUSS"] == "" || values["STOKEN"] == "" {
		return "", false
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; "), true
}

func FindChrome() (string, error) {
	candidates := []string{"chrome.exe", "chrome"}
	if goruntime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	} else {
		candidates = append(candidates, "google-chrome", "google-chrome-stable", "chromium", "chromium-browser")
	}
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if found, err := exec.LookPath(candidate); err == nil {
			return found, nil
		}
		if filepath.IsAbs(candidate) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("Google Chrome was not found; install Chrome or make chrome.exe available")
}
