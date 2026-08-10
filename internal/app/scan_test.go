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

type scanAuthState struct {
	scanCalls     int
	authFailures  int
	clientCookies []string
	accessTokens  []string
	scanTokens    []string
}

type scanAuthAPI struct {
	state  *scanAuthState
	cookie string
}

func (a *scanAuthAPI) AccessSharePage(context.Context, baidu.ShareLink) (baidu.ShareContext, error) {
	token := "token-" + a.cookie
	a.state.accessTokens = append(a.state.accessTokens, token)
	return baidu.ShareContext{BDSToken: token, ShareID: "1", ShareUK: "2"}, nil
}

func (a *scanAuthAPI) VerifyPassword(context.Context, baidu.ShareLink, baidu.ShareContext, string) error {
	return nil
}

func (a *scanAuthAPI) Scan(ctx context.Context, taskID string, _ baidu.ShareLink, share baidu.ShareContext, sink manifest.Sink) error {
	a.state.scanCalls++
	a.state.scanTokens = append(a.state.scanTokens, share.BDSToken)
	if err := sink.UpsertManifestPage(ctx, taskID,
		[]manifest.Directory{{LogicalPath: "/folder"}},
		[]manifest.File{{SourceID: "101", LogicalPath: "/folder/file.bin", ParentPath: "/folder", Name: "file.bin", Size: 42}},
	); err != nil {
		return err
	}
	if a.state.scanCalls <= a.state.authFailures {
		return baidu.ErrAuthRequired
	}
	return nil
}

func (a *scanAuthAPI) CookieString() string { return a.cookie }

func (a *scanAuthAPI) HasLoginCookies() bool {
	return strings.Contains(a.cookie, "BDUSS=") && strings.Contains(a.cookie, "STOKEN=")
}

type canceledScanAPI struct{}

func (canceledScanAPI) AccessSharePage(context.Context, baidu.ShareLink) (baidu.ShareContext, error) {
	return baidu.ShareContext{BDSToken: "cancel-token", ShareID: "1", ShareUK: "2"}, nil
}

func (canceledScanAPI) VerifyPassword(context.Context, baidu.ShareLink, baidu.ShareContext, string) error {
	return nil
}

func (canceledScanAPI) Scan(context.Context, string, baidu.ShareLink, baidu.ShareContext, manifest.Sink) error {
	return context.Canceled
}

func (canceledScanAPI) CookieString() string { return "BDUSS=initial; STOKEN=initial" }

func (canceledScanAPI) HasLoginCookies() bool { return true }

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
	task, err := runner.Store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !task.ScanCompleted {
		t.Fatal("successful scan did not record scan completion")
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

func TestScanRunnerReauthenticatesAfterMidScanAuthExpiry(t *testing.T) {
	browser := &fakeBrowser{cookies: "BDUSS=refreshed; STOKEN=refreshed"}
	runner, store := newRunnerForTest(t, "BDUSS=initial; STOKEN=initial", "", browser, false)
	state := &scanAuthState{authFailures: 1}
	runner.NewClient = func(cookieHeader string) (BaiduAPI, error) {
		state.clientCookies = append(state.clientCookies, cookieHeader)
		return &scanAuthAPI{state: state, cookie: cookieHeader}, nil
	}

	taskID, stats, err := runner.Run(context.Background(), "https://pan.baidu.com/s/1Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 || stats.Directories != 1 || stats.Bytes != 42 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if browser.calls != 1 {
		t.Fatalf("browser login calls=%d want=1", browser.calls)
	}
	if state.scanCalls != 2 {
		t.Fatalf("scan calls=%d want=2", state.scanCalls)
	}
	if len(state.clientCookies) != 2 || state.clientCookies[1] != browser.cookies {
		t.Fatalf("client cookies=%v want refreshed cookie on rebuild", state.clientCookies)
	}
	if len(state.accessTokens) != 2 || len(state.scanTokens) != 2 || state.scanTokens[1] != state.accessTokens[1] || state.scanTokens[1] == state.scanTokens[0] {
		t.Fatalf("share context was not refreshed after login: access=%v scan=%v", state.accessTokens, state.scanTokens)
	}
	savedCookie, err := runner.CookieStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if savedCookie != browser.cookies {
		t.Fatalf("saved cookie=%q want %q", savedCookie, browser.cookies)
	}
	task, err := store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !task.ScanCompleted {
		t.Fatal("recovered scan did not record scan completion")
	}
}

func TestScanRunnerBoundsRepeatedAuthExpiry(t *testing.T) {
	browser := &fakeBrowser{cookies: "BDUSS=refreshed; STOKEN=refreshed"}
	runner, store := newRunnerForTest(t, "BDUSS=initial; STOKEN=initial", "", browser, false)
	state := &scanAuthState{authFailures: 2}
	runner.NewClient = func(cookieHeader string) (BaiduAPI, error) {
		state.clientCookies = append(state.clientCookies, cookieHeader)
		return &scanAuthAPI{state: state, cookie: cookieHeader}, nil
	}

	taskID, _, err := runner.Run(context.Background(), "https://pan.baidu.com/s/1Synthetic")
	if !errors.Is(err, baidu.ErrAuthRequired) {
		t.Fatalf("error=%v want auth required", err)
	}
	if browser.calls != 1 {
		t.Fatalf("browser login calls=%d want=1", browser.calls)
	}
	if state.scanCalls != 2 || len(state.clientCookies) != 2 {
		t.Fatalf("scan calls=%d client calls=%d want 2 each", state.scanCalls, len(state.clientCookies))
	}
	task, getErr := store.GetTask(context.Background(), taskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if task.ScanCompleted {
		t.Fatal("failed scan was marked complete")
	}
}

func TestScanRunnerPropagatesCancellationUnchanged(t *testing.T) {
	browser := &fakeBrowser{cookies: "BDUSS=unused; STOKEN=unused"}
	runner, _ := newRunnerForTest(t, "BDUSS=initial; STOKEN=initial", "", browser, false)
	runner.NewClient = func(string) (BaiduAPI, error) { return canceledScanAPI{}, nil }

	_, _, err := runner.Run(context.Background(), "https://pan.baidu.com/s/1Synthetic")
	if err != context.Canceled {
		t.Fatalf("error=%v want exact context.Canceled", err)
	}
	if browser.calls != 0 {
		t.Fatalf("browser login calls=%d want=0", browser.calls)
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

func TestScanRunnerResumesIncompleteScanWithSameTask(t *testing.T) {
	browser := &fakeBrowser{cookies: "BDUSS=unused; STOKEN=unused"}
	runner, store := newRunnerForTest(t, "BDUSS=fake; STOKEN=fake", "", browser, false)
	const taskID = "task-resume-scan"
	if err := store.CreateTask(context.Background(), state.Task{
		ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManifestPage(context.Background(), taskID, nil, []manifest.File{{
		SourceID: "101", LogicalPath: "/folder/file.bin", ParentPath: "/folder", Name: "file.bin", Size: 42,
	}}); err != nil {
		t.Fatal(err)
	}

	resumedID, stats, err := runner.Resume(context.Background(), state.Task{
		ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumedID != taskID || stats.Files != 1 || stats.Bytes != 42 {
		t.Fatalf("resume id=%q stats=%+v", resumedID, stats)
	}
	task, err := store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !task.ScanCompleted {
		t.Fatal("resumed scan was not marked complete")
	}
}
