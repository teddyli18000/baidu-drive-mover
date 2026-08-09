package app

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakeStageAPI struct {
	loggedIn bool
	objects  map[string]map[string]baidu.RemoteFile
	source   map[int64]baidu.RemoteFile
}

func (a *fakeStageAPI) AccessSharePage(context.Context, baidu.ShareLink) (baidu.ShareContext, error) {
	if !a.loggedIn {
		return baidu.ShareContext{}, baidu.ErrAuthRequired
	}
	return baidu.ShareContext{BDSToken: "fake-token", ShareID: "1", ShareUK: "2"}, nil
}
func (a *fakeStageAPI) VerifyPassword(context.Context, baidu.ShareLink, baidu.ShareContext, string) error {
	return nil
}
func (a *fakeStageAPI) CookieString() string { return "BDUSS=fake; STOKEN=fake" }
func (a *fakeStageAPI) HasLoginCookies() bool { return a.loggedIn }
func (a *fakeStageAPI) EnsureStagingDirectory(_ context.Context, remotePath string) error {
	if a.objects[remotePath] == nil {
		a.objects[remotePath] = make(map[string]baidu.RemoteFile)
	}
	return nil
}
func (a *fakeStageAPI) ListStagingDirectory(_ context.Context, remotePath string) ([]baidu.RemoteFile, error) {
	var result []baidu.RemoteFile
	for _, object := range a.objects[remotePath] {
		result = append(result, object)
	}
	return result, nil
}
func (a *fakeStageAPI) TransferFiles(_ context.Context, _ baidu.ShareLink, _ baidu.ShareContext, ids []int64, remotePath string) error {
	if a.objects[remotePath] == nil {
		a.objects[remotePath] = make(map[string]baidu.RemoteFile)
	}
	for _, id := range ids {
		object := a.source[id]
		object.Path = path.Join(remotePath, object.Name)
		a.objects[remotePath][object.Name] = object
	}
	return nil
}

func TestStageRunnerPlansAndStagesManifest(t *testing.T) {
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
	defer store.Close()
	ctx := context.Background()
	taskID := "task-app-stage"
	if err := store.CreateTask(ctx, state.Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	files := []manifest.File{
		{SourceID: "101", LogicalPath: "/a/one.bin", ParentPath: "/a", Name: "one.bin", Size: 10},
		{SourceID: "102", LogicalPath: "/a/two.bin", ParentPath: "/a", Name: "two.bin", Size: 20},
	}
	if err := store.UpsertManifestPage(ctx, taskID, []manifest.Directory{{LogicalPath: "/a"}}, files); err != nil {
		t.Fatal(err)
	}
	cookiePath, _ := layout.JoinTemp("auth", "baidu.cookies")
	cookieStore := baidu.CookieStore{Path: cookiePath}
	if err := cookieStore.Save("BDUSS=fake; STOKEN=fake"); err != nil {
		t.Fatal(err)
	}
	api := &fakeStageAPI{
		loggedIn: true,
		objects:  make(map[string]map[string]baidu.RemoteFile),
		source: map[int64]baidu.RemoteFile{
			101: {Name: "one.bin", Size: 10},
			102: {Name: "two.bin", Size: 20},
		},
	}
	var output bytes.Buffer
	browser := &fakeBrowser{cookies: "BDUSS=unused; STOKEN=unused"}
	runner := &StageRunner{
		Store:       store,
		Browser:     browser,
		CookieStore: cookieStore,
		Output:      &output,
		NewClient: func(cookie string) (StagingBaiduAPI, error) {
			api.loggedIn = strings.Contains(cookie, "BDUSS=") && strings.Contains(cookie, "STOKEN=")
			return api, nil
		},
	}
	summary, err := runner.Run(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesStaged != 2 {
		t.Fatalf("staged=%d want=2", summary.FilesStaged)
	}
	if browser.calls != 0 {
		t.Fatalf("unexpected browser refresh calls=%d", browser.calls)
	}
	batches, err := store.StagingBatches(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 0 {
		t.Fatalf("completed batches still returned for staging: %d", len(batches))
	}
}

func TestStageRunnerRefreshesStaleBaiduLoginOnce(t *testing.T) {
	layout, _ := runtimepath.New(t.TempDir())
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(layout.Temp, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	taskID := "task-auth-refresh"
	if err := store.CreateTask(ctx, state.Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManifestPage(ctx, taskID, nil, []manifest.File{{SourceID: "201", LogicalPath: "/file.bin", ParentPath: "/", Name: "file.bin", Size: 1}}); err != nil {
		t.Fatal(err)
	}
	cookiePath, _ := layout.JoinTemp("auth", "baidu.cookies")
	cookieStore := baidu.CookieStore{Path: cookiePath}
	if err := cookieStore.Save("BDUSS=stale; STOKEN=stale"); err != nil {
		t.Fatal(err)
	}
	browser := &fakeBrowser{cookies: "BDUSS=fresh; STOKEN=fresh"}
	var creations int
	runner := &StageRunner{
		Store:       store,
		Browser:     browser,
		CookieStore: cookieStore,
		Output:      &bytes.Buffer{},
		NewClient: func(cookie string) (StagingBaiduAPI, error) {
			creations++
			fresh := strings.Contains(cookie, "fresh")
			return &fakeStageAPI{
				loggedIn: fresh,
				objects:  make(map[string]map[string]baidu.RemoteFile),
				source:   map[int64]baidu.RemoteFile{201: {Name: "file.bin", Size: 1}},
			}, nil
		},
	}
	if _, err := runner.Run(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	if browser.calls != 1 {
		t.Fatalf("browser refresh calls=%d want=1 (clients=%d)", browser.calls, creations)
	}
	if value, err := cookieStore.Load(); err != nil || !strings.Contains(value, "fresh") {
		t.Fatalf("fresh cookies not persisted: value=%q err=%v", value, err)
	}
}

var _ = fmt.Sprintf
