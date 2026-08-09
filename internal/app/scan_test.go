package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakeBrowser struct {
	calls   int
	cookies string
}

func (b *fakeBrowser) Login(context.Context) (string, error) {
	b.calls++
	return b.cookies, nil
}

type fakeAPI struct {
	loggedIn        bool
	requirePassword bool
	verified        bool
}

func (a *fakeAPI) AccessSharePage(context.Context, baidu.ShareLink) (baidu.ShareContext, error) {
	if !a.loggedIn {
		return baidu.ShareContext{}, baidu.ErrAuthRequired
	}
	return baidu.ShareContext{BDSToken: "fake-token", ShareID: "1", ShareUK: "2"}, nil
}

func (a *fakeAPI) VerifyPassword(_ context.Context, _ baidu.ShareLink, _ baidu.ShareContext, password string) error {
	if password != "a1b2" {
		return baidu.ErrWrongPassword
	}
	a.verified = true
	return nil
}

func (a *fakeAPI) Scan(ctx context.Context, taskID string, _ baidu.ShareLink, _ baidu.ShareContext, sink manifest.Sink) error {
	if a.requirePassword && !a.verified {
		return baidu.ErrPasswordRequired
	}
	return sink.UpsertManifestPage(ctx, taskID,
		[]manifest.Directory{{LogicalPath: "/folder"}},
		[]manifest.File{{SourceID: "101", LogicalPath: "/folder/file.bin", ParentPath: "/folder", Name: "file.bin", Size: 42}},
	)
}

func (a *fakeAPI) CookieString() string {
	value := "BDUSS=fake-bduss; STOKEN=fake-stoken"
	if a.verified {
		value += "; BDCLND=fake-verified"
	}
	return value
}

func (a *fakeAPI) HasLoginCookies() bool { return a.loggedIn }

func newRunnerForTest(t *testing.T, initialCookies, input string, browser *fakeBrowser, requirePassword bool) (*ScanRunner, *state.Store) {
	t.Helper()
	layout, err := runtimepath.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(layout.Temp, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cookiePath, err := layout.JoinTemp("auth", "baidu.cookies")
	if err != nil {
		t.Fatal(err)
	}
	cookieStore := baidu.CookieStore{Path: cookiePath}
	if initialCookies != "" {
		if err := cookieStore.Save(initialCookies); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	runner := &ScanRunner{
		Layout:      layout,
		Store:       store,
		Browser:     browser,
		Input:       bufio.NewReader(strings.NewReader(input)),
		Output:      &output,
		CookieStore: cookieStore,
		NewClient: func(cookieHeader string) (BaiduAPI, error) {
			return &fakeAPI{
				loggedIn:        strings.Contains(cookieHeader, "BDUSS=") && strings.Contains(cookieHeader, "STOKEN="),
				requirePassword: requirePassword,
			}, nil
		},
	}
	return runner, store
}

func TestScanRunnerUsesDedicatedBrowserOnlyWhenLoginMissing(t *testing.T) {
	browser := &fakeBrowser{cookies: "BDUSS=fake-bduss; STOKEN=fake-stoken"}
	runner, _ := newRunnerForTest(t, "", "", browser, false)
	taskID, stats, err := runner.Run(context.Background(), "https://pan.baidu.com/s/1Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if taskID == "" || stats.Files != 1 || stats.Directories != 1 || stats.Bytes != 42 {
		t.Fatalf("unexpected result task=%q stats=%+v", taskID, stats)
	}
	if browser.calls != 1 {
		t.Fatalf("browser login calls=%d want=1", browser.calls)
	}
}

func TestScanRunnerUsesPasswordFromLinkWithoutPrompt(t *testing.T) {
	browser := &fakeBrowser{cookies: "BDUSS=unused; STOKEN=unused"}
	runner, store := newRunnerForTest(t, "BDUSS=fake; STOKEN=fake", "", browser, true)
	taskID, stats, err := runner.Run(context.Background(), "https://pan.baidu.com/s/1Synthetic?pwd=a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 || browser.calls != 0 {
		t.Fatalf("unexpected result stats=%+v browser_calls=%d", stats, browser.calls)
	}
	task, err := store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.ExtractionCode != "a1b2" {
		t.Fatalf("extraction code not persisted: %q", task.ExtractionCode)
	}
	if strings.Contains(task.ShareURL, "a1b2") || strings.Contains(task.ShareURL, "pwd=") {
		t.Fatalf("password leaked into stored share URL: %q", task.ShareURL)
	}
}

func TestScanRunnerPromptsWhenScannerRequiresPassword(t *testing.T) {
	browser := &fakeBrowser{cookies: "BDUSS=unused; STOKEN=unused"}
	runner, _ := newRunnerForTest(t, "BDUSS=fake; STOKEN=fake", "wrong\na1b2\n", browser, true)
	_, stats, err := runner.Run(context.Background(), "https://pan.baidu.com/s/1Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestScanRunnerRejectsInvalidLinkBeforeCreatingTask(t *testing.T) {
	browser := &fakeBrowser{cookies: "BDUSS=fake; STOKEN=fake"}
	runner, _ := newRunnerForTest(t, "BDUSS=fake; STOKEN=fake", "", browser, false)
	_, _, err := runner.Run(context.Background(), "https://example.invalid/s/1Synthetic")
	if err == nil {
		t.Fatal("expected invalid link error")
	}
	if errors.Is(err, baidu.ErrAuthRequired) {
		t.Fatalf("unexpected auth error for invalid link: %v", err)
	}
}
