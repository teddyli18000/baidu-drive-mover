package download

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakeDownloadRemote struct {
	mu        sync.Mutex
	data      map[string][]byte
	offsets   []int64
	openFn    func(*fakeDownloadRemote, string, int64) (*baidu.DownloadStream, error)
	openCalls int
}

func (r *fakeDownloadRemote) OpenDownload(_ context.Context, remotePath string, offset int64) (*baidu.DownloadStream, error) {
	r.mu.Lock()
	r.offsets = append(r.offsets, offset)
	r.openCalls++
	r.mu.Unlock()
	if r.openFn != nil {
		return r.openFn(r, remotePath, offset)
	}
	data := r.data[remotePath]
	if offset < 0 || offset > int64(len(data)) {
		return nil, baidu.ErrRangeNotSatisfiable
	}
	remaining := data[offset:]
	return &baidu.DownloadStream{
		Body:      io.NopCloser(strings.NewReader(string(remaining))),
		Start:     offset,
		Remaining: int64(len(remaining)),
		Total:     int64(len(data)),
		Partial:   offset > 0,
	}, nil
}

func TestFreshDownloadUsesOpaqueCacheNameAndVerifies(t *testing.T) {
	fixture := newDownloadFixture(t, []downloadSeed{{id: "101", name: `bad:name?.txt`, data: []byte("hello world")}})
	engine := testDownloadEngine(fixture.layout, fixture.store, fixture.remote, 1<<20)
	summary, err := engine.Run(context.Background(), fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesReady != 1 || summary.BytesReady != 11 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	file, err := fixture.store.GetFile(context.Background(), fixture.taskID, "101")
	if err != nil {
		t.Fatal(err)
	}
	if file.Status != state.FileLocalReady || file.LocalCachePath != "cache/"+fixture.taskID+"/101.bin" {
		t.Fatalf("unexpected file state: %+v", file)
	}
	if strings.Contains(file.LocalCachePath, "bad:name") || strings.Contains(file.LocalCachePath, "?") {
		t.Fatalf("source name leaked into cache path: %q", file.LocalCachePath)
	}
	full, err := fixture.layout.ResolveTempRelative(file.LocalCachePath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(full)
	if err != nil || string(data) != "hello world" {
		t.Fatalf("cache data=%q err=%v", data, err)
	}
}

func TestDownloadResumesExistingPart(t *testing.T) {
	fixture := newDownloadFixture(t, []downloadSeed{{id: "201", name: "resume.bin", data: []byte("abcdefghij")}})
	part, _, _ := cachePaths(fixture.taskID, "201")
	if _, err := fixture.layout.EnsureTempDir(filepath.ToSlash(filepath.Dir(filepath.FromSlash(part)))); err != nil {
		t.Fatal(err)
	}
	file, err := fixture.layout.OpenTempFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("abcd"))
	_ = file.Close()
	if err := fixture.store.StartDownload(context.Background(), fixture.taskID, "201", part); err != nil {
		t.Fatal(err)
	}
	if _, err := testDownloadEngine(fixture.layout, fixture.store, fixture.remote, 1<<20).Run(context.Background(), fixture.taskID); err != nil {
		t.Fatal(err)
	}
	if len(fixture.remote.offsets) != 1 || fixture.remote.offsets[0] != 4 {
		t.Fatalf("resume offsets=%v", fixture.remote.offsets)
	}
}

func TestIgnoredRangeSafelyRestartsFromZero(t *testing.T) {
	fixture := newDownloadFixture(t, []downloadSeed{{id: "301", name: "range.bin", data: []byte("0123456789")}})
	part, _, _ := cachePaths(fixture.taskID, "301")
	_, _ = fixture.layout.EnsureTempDir(filepath.ToSlash(filepath.Dir(filepath.FromSlash(part))))
	file, _ := fixture.layout.OpenTempFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	_, _ = file.Write([]byte("01234"))
	_ = file.Close()
	if err := fixture.store.StartDownload(context.Background(), fixture.taskID, "301", part); err != nil {
		t.Fatal(err)
	}
	fixture.remote.openFn = func(r *fakeDownloadRemote, remotePath string, offset int64) (*baidu.DownloadStream, error) {
		if offset > 0 {
			return nil, baidu.ErrRangeNotHonored
		}
		data := r.data[remotePath]
		return &baidu.DownloadStream{Body: io.NopCloser(strings.NewReader(string(data))), Total: int64(len(data)), Remaining: int64(len(data))}, nil
	}
	if _, err := testDownloadEngine(fixture.layout, fixture.store, fixture.remote, 1<<20).Run(context.Background(), fixture.taskID); err != nil {
		t.Fatal(err)
	}
	if len(fixture.remote.offsets) != 2 || fixture.remote.offsets[0] != 5 || fixture.remote.offsets[1] != 0 {
		t.Fatalf("offsets=%v want=[5 0]", fixture.remote.offsets)
	}
}

func TestInterruptedDownloadLeavesResumablePart(t *testing.T) {
	fixture := newDownloadFixture(t, []downloadSeed{{id: "401", name: "interrupt.bin", data: []byte("abcdefghij")}})
	fixture.remote.openFn = func(_ *fakeDownloadRemote, _ string, _ int64) (*baidu.DownloadStream, error) {
		return &baidu.DownloadStream{Body: io.NopCloser(&failAfterReader{data: []byte("abcd"), err: context.Canceled}), Total: 10}, nil
	}
	_, err := testDownloadEngine(fixture.layout, fixture.store, fixture.remote, 1<<20).Run(context.Background(), fixture.taskID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	part, _, _ := cachePaths(fixture.taskID, "401")
	info, statErr := fixture.layout.StatTempFile(part)
	if statErr != nil || info.Size() != 4 {
		t.Fatalf("partial size=%v err=%v", info, statErr)
	}
	file, _ := fixture.store.GetFile(context.Background(), fixture.taskID, "401")
	if file.Status != state.FileDownloading {
		t.Fatalf("status=%s want=%s", file.Status, state.FileDownloading)
	}
}

func TestExistingCompletedBinReconcilesWithoutNetwork(t *testing.T) {
	fixture := newDownloadFixture(t, []downloadSeed{{id: "501", name: "existing.bin", data: []byte("ready")}})
	_, bin, _ := cachePaths(fixture.taskID, "501")
	_, _ = fixture.layout.EnsureTempDir(filepath.ToSlash(filepath.Dir(filepath.FromSlash(bin))))
	file, _ := fixture.layout.OpenTempFile(bin, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	_, _ = file.Write([]byte("ready"))
	_ = file.Close()
	if _, err := testDownloadEngine(fixture.layout, fixture.store, fixture.remote, 1<<20).Run(context.Background(), fixture.taskID); err != nil {
		t.Fatal(err)
	}
	if fixture.remote.openCalls != 0 {
		t.Fatalf("existing verified bin caused %d network calls", fixture.remote.openCalls)
	}
}

func TestRepeatedMD5MismatchBecomesPermanent(t *testing.T) {
	fixture := newDownloadFixture(t, []downloadSeed{{id: "601", name: "hash.bin", data: []byte("wrong-body"), expectedMD5: md5Hex([]byte("right-body"))}})
	engine := testDownloadEngine(fixture.layout, fixture.store, fixture.remote, 1<<20)
	engine.MaxAttempts = 2
	_, err := engine.Run(context.Background(), fixture.taskID)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "md5 mismatch") {
		t.Fatalf("expected md5 failure, got %v", err)
	}
	file, _ := fixture.store.GetFile(context.Background(), fixture.taskID, "601")
	if file.Status != state.FileFailedPermanent {
		t.Fatalf("status=%s want=%s", file.Status, state.FileFailedPermanent)
	}
	if fixture.remote.openCalls != 2 {
		t.Fatalf("open calls=%d want=2", fixture.remote.openCalls)
	}
}

func TestCacheWatermarkStopsBeforeOvercommit(t *testing.T) {
	fixture := newDownloadFixture(t, []downloadSeed{
		{id: "701", name: "a.bin", data: []byte("1234567890")},
		{id: "702", name: "b.bin", data: []byte("abcdefghij")},
	})
	summary, err := testDownloadEngine(fixture.layout, fixture.store, fixture.remote, 15).Run(context.Background(), fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesReady != 1 || !summary.PausedByWatermark {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if fixture.remote.openCalls != 1 {
		t.Fatalf("network calls=%d want=1", fixture.remote.openCalls)
	}
}

func TestSingleFileLargerThanCacheLimitIsBlocked(t *testing.T) {
	fixture := newDownloadFixture(t, []downloadSeed{{id: "801", name: "large.bin", data: []byte("0123456789")}})
	_, err := testDownloadEngine(fixture.layout, fixture.store, fixture.remote, 5).Run(context.Background(), fixture.taskID)
	var oversized *OversizedCacheFileError
	if !errors.As(err, &oversized) {
		t.Fatalf("expected oversized cache error, got %v", err)
	}
	if fixture.remote.openCalls != 0 {
		t.Fatalf("oversized file should not start network download")
	}
}

type downloadSeed struct {
	id          string
	name        string
	data        []byte
	expectedMD5 string
}

type downloadFixture struct {
	layout *runtimepath.Layout
	store  *state.Store
	remote *fakeDownloadRemote
	taskID string
}

func newDownloadFixture(t *testing.T, seeds []downloadSeed) downloadFixture {
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
	taskID := "task-download"
	ctx := context.Background()
	if err := store.CreateTask(ctx, state.Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	manifestFiles := make([]manifest.File, 0, len(seeds))
	remoteData := make(map[string][]byte)
	for i, seed := range seeds {
		md5Value := seed.expectedMD5
		if md5Value == "" {
			md5Value = md5Hex(seed.data)
		}
		manifestFiles = append(manifestFiles, manifest.File{
			SourceID:    seed.id,
			LogicalPath: fmt.Sprintf("/folder/%s", seed.name),
			ParentPath:  "/folder",
			Name:        seed.name,
			Size:        int64(len(seed.data)),
			MD5:         md5Value,
		})
		remotePath := fmt.Sprintf("/BaiduDriveMover/%s/batch/%s", taskID, seed.name)
		remoteData[remotePath] = seed.data
		_ = i
	}
	if err := store.UpsertManifestPage(ctx, taskID, []manifest.Directory{{LogicalPath: "/folder"}}, manifestFiles); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil || len(batches) == 0 {
		t.Fatalf("plan err=%v batches=%d", err, len(batches))
	}
	for _, batch := range batches {
		if err := store.StartBatch(ctx, taskID, batch.BatchID); err != nil {
			t.Fatal(err)
		}
		staged := make(map[string]string)
		for _, file := range batch.Files {
			remotePath := fmt.Sprintf("/BaiduDriveMover/%s/batch/%s", taskID, file.Name)
			staged[file.FileID] = remotePath
		}
		if err := store.RecordStagedFiles(ctx, taskID, batch.BatchID, staged); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteBatch(ctx, taskID, batch.BatchID); err != nil {
			t.Fatal(err)
		}
	}
	return downloadFixture{
		layout: layout,
		store:  store,
		remote: &fakeDownloadRemote{data: remoteData},
		taskID: taskID,
	}
}

func testDownloadEngine(layout *runtimepath.Layout, store *state.Store, remote *fakeDownloadRemote, limit int64) *Engine {
	return &Engine{
		Layout:        layout,
		Repository:    store,
		Remote:        remote,
		MaxCacheBytes: limit,
		MaxAttempts:   3,
		Sleep:         func(context.Context, time.Duration) error { return nil },
	}
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

type failAfterReader struct {
	data []byte
	err  error
	done bool
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.data), nil
	}
	return 0, r.err
}
