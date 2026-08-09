package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/download"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakeDownloadAPI struct {
	loggedIn bool
	data     map[string][]byte
}

func (a *fakeDownloadAPI) HasLoginCookies() bool { return a.loggedIn }

func (a *fakeDownloadAPI) OpenDownload(_ context.Context, remotePath string, offset int64) (*baidu.DownloadStream, error) {
	if !a.loggedIn {
		return nil, baidu.ErrAuthRequired
	}
	data, ok := a.data[remotePath]
	if !ok {
		return nil, fmt.Errorf("missing fake remote %q", remotePath)
	}
	if offset < 0 || offset > int64(len(data)) {
		return nil, baidu.ErrRangeNotSatisfiable
	}
	remaining := data[offset:]
	return &baidu.DownloadStream{
		Body:      io.NopCloser(bytes.NewReader(remaining)),
		Start:     offset,
		Remaining: int64(len(remaining)),
		Total:     int64(len(data)),
		Partial:   offset > 0,
	}, nil
}

func TestDownloadRunnerDownloadsStagedFile(t *testing.T) {
	layout, store, taskID, remotePath := downloadRunnerFixture(t, []byte("hello"))
	cookiePath, _ := layout.JoinTemp("auth", "baidu.cookies")
	cookieStore := baidu.CookieStore{Path: cookiePath}
	if err := cookieStore.Save("BDUSS=fake; STOKEN=fake"); err != nil {
		t.Fatal(err)
	}
	browser := &fakeBrowser{cookies: "BDUSS=unused; STOKEN=unused"}
	api := &fakeDownloadAPI{loggedIn: true, data: map[string][]byte{remotePath: []byte("hello")}}
	var output bytes.Buffer
	runner := &DownloadRunner{
		Layout:        layout,
		Store:         store,
		Browser:       browser,
		CookieStore:   cookieStore,
		Output:        &output,
		MaxCacheBytes: 1 << 20,
		NewClient: func(cookie string) (DownloadBaiduAPI, error) {
			api.loggedIn = strings.Contains(cookie, "BDUSS=") && strings.Contains(cookie, "STOKEN=")
			return api, nil
		},
	}
	summary, err := runner.Run(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesReady != 1 || browser.calls != 0 {
		t.Fatalf("summary=%+v browser_calls=%d", summary, browser.calls)
	}
	file, err := store.GetFile(context.Background(), taskID, "1001")
	if err != nil {
		t.Fatal(err)
	}
	if file.Status != state.FileLocalReady {
		t.Fatalf("status=%s", file.Status)
	}
}

func TestDownloadRunnerRefreshesStaleLoginOnce(t *testing.T) {
	layout, store, taskID, remotePath := downloadRunnerFixture(t, []byte("fresh"))
	cookiePath, _ := layout.JoinTemp("auth", "baidu.cookies")
	cookieStore := baidu.CookieStore{Path: cookiePath}
	if err := cookieStore.Save("BDUSS=stale; STOKEN=stale"); err != nil {
		t.Fatal(err)
	}
	browser := &fakeBrowser{cookies: "BDUSS=fresh; STOKEN=fresh"}
	var creations int
	runner := &DownloadRunner{
		Layout:        layout,
		Store:         store,
		Browser:       browser,
		CookieStore:   cookieStore,
		Output:        &bytes.Buffer{},
		MaxCacheBytes: 1 << 20,
		NewClient: func(cookie string) (DownloadBaiduAPI, error) {
			creations++
			fresh := strings.Contains(cookie, "fresh")
			return &fakeDownloadAPI{loggedIn: fresh, data: map[string][]byte{remotePath: []byte("fresh")}}, nil
		},
	}
	if _, err := runner.Run(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	if browser.calls != 1 {
		t.Fatalf("browser_calls=%d want=1 (clients=%d)", browser.calls, creations)
	}
}

func TestDownloadRunnerReportsWatermarkPauseWithoutFailure(t *testing.T) {
	layout, store, taskID, remotePath := downloadRunnerFixture(t, []byte("0123456789"))
	cookiePath, _ := layout.JoinTemp("auth", "baidu.cookies")
	cookieStore := baidu.CookieStore{Path: cookiePath}
	if err := cookieStore.Save("BDUSS=fake; STOKEN=fake"); err != nil {
		t.Fatal(err)
	}
	api := &fakeDownloadAPI{loggedIn: true, data: map[string][]byte{remotePath: []byte("0123456789")}}
	runner := &DownloadRunner{
		Layout:        layout,
		Store:         store,
		Browser:       &fakeBrowser{cookies: "BDUSS=unused; STOKEN=unused"},
		CookieStore:   cookieStore,
		Output:        &bytes.Buffer{},
		MaxCacheBytes: 5,
		NewClient:     func(string) (DownloadBaiduAPI, error) { return api, nil },
	}
	_, err := runner.Run(context.Background(), taskID)
	var oversized *download.OversizedCacheFileError
	if !strings.Contains(fmt.Sprint(err), "local cache limit") || !asOversized(err, &oversized) {
		t.Fatalf("expected oversized cache blocker, got %v", err)
	}
	task, taskErr := store.GetTask(context.Background(), taskID)
	if taskErr != nil {
		t.Fatal(taskErr)
	}
	if task.Status != state.TaskBlocked {
		t.Fatalf("task status=%s want=%s", task.Status, state.TaskBlocked)
	}
}

func asOversized(err error, target **download.OversizedCacheFileError) bool {
	if err == nil {
		return false
	}
	value, ok := err.(*download.OversizedCacheFileError)
	if !ok {
		return false
	}
	*target = value
	return true
}

func downloadRunnerFixture(t *testing.T, data []byte) (*runtimepath.Layout, *state.Store, string, string) {
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
	ctx := context.Background()
	taskID := "task-download-runner"
	if err := store.CreateTask(ctx, state.Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManifestPage(ctx, taskID, nil, []manifest.File{{
		SourceID:    "1001",
		LogicalPath: "/folder/file.bin",
		ParentPath:  "/folder",
		Name:        "file.bin",
		Size:        int64(len(data)),
	}}); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil || len(batches) != 1 {
		t.Fatalf("plan err=%v batches=%d", err, len(batches))
	}
	batch := batches[0]
	if err := store.StartBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	remotePath := batch.BaiduStagingPath + "/file.bin"
	if err := store.RecordStagedFiles(ctx, taskID, batch.BatchID, map[string]string{"1001": remotePath}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	return layout, store, taskID, remotePath
}
