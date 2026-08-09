package staging

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakeRemote struct {
	mu         sync.Mutex
	dirs       map[string]map[string]baidu.RemoteFile
	source     map[int64]baidu.RemoteFile
	calls      []int
	transferFn func(*fakeRemote, []int64, string) error
}

func newFakeRemote(source map[int64]baidu.RemoteFile) *fakeRemote {
	return &fakeRemote{dirs: make(map[string]map[string]baidu.RemoteFile), source: source}
}

func (r *fakeRemote) EnsureStagingDirectory(_ context.Context, remotePath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dirs[remotePath] == nil {
		r.dirs[remotePath] = make(map[string]baidu.RemoteFile)
	}
	return nil
}

func (r *fakeRemote) ListStagingDirectory(_ context.Context, remotePath string) ([]baidu.RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	objects := r.dirs[remotePath]
	files := make([]baidu.RemoteFile, 0, len(objects))
	for _, object := range objects {
		files = append(files, object)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func (r *fakeRemote) TransferFiles(_ context.Context, _ baidu.ShareLink, _ baidu.ShareContext, ids []int64, remotePath string) error {
	r.mu.Lock()
	r.calls = append(r.calls, len(ids))
	r.mu.Unlock()
	if r.transferFn != nil {
		return r.transferFn(r, ids, remotePath)
	}
	r.add(ids, remotePath)
	return nil
}

func (r *fakeRemote) add(ids []int64, remotePath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dirs[remotePath] == nil {
		r.dirs[remotePath] = make(map[string]baidu.RemoteFile)
	}
	for _, id := range ids {
		object := r.source[id]
		object.Path = path.Join(remotePath, object.Name)
		r.dirs[remotePath][object.Name] = object
	}
}

func TestExecutorSplitsToServiceLimitAndStagesEveryFile(t *testing.T) {
	store, batch, source := stagingFixture(t, 120)
	remote := newFakeRemote(source)
	remote.transferFn = func(r *fakeRemote, ids []int64, remotePath string) error {
		if len(ids) > 50 {
			return &baidu.TransferLimitError{Target: len(ids), Limit: 50}
		}
		r.add(ids, remotePath)
		return nil
	}
	if err := testExecutor(store, remote).Run(context.Background(), batch.TaskID); err != nil {
		t.Fatal(err)
	}
	if got := remote.calls; len(got) != 4 || got[0] != 120 || got[1] != 50 || got[2] != 50 || got[3] != 20 {
		t.Fatalf("unexpected transfer calls: %v", got)
	}
	assertBatchFullyStaged(t, store, batch.TaskID, batch.BatchID, 120)
}

func TestExecutorReconcilesPartialSuccessBeforeRetry(t *testing.T) {
	store, batch, source := stagingFixture(t, 3)
	remote := newFakeRemote(source)
	first := true
	remote.transferFn = func(r *fakeRemote, ids []int64, remotePath string) error {
		if first {
			first = false
			r.add(ids[:1], remotePath)
			return &baidu.RemoteError{Operation: "synthetic partial", Errno: 500}
		}
		r.add(ids, remotePath)
		return nil
	}
	if err := testExecutor(store, remote).Run(context.Background(), batch.TaskID); err != nil {
		t.Fatal(err)
	}
	if got := remote.calls; len(got) != 2 || got[0] != 3 || got[1] != 2 {
		t.Fatalf("partial retry resent completed files: %v", got)
	}
	assertBatchFullyStaged(t, store, batch.TaskID, batch.BatchID, 3)
}

func TestExecutorDoesNotSplitGenericNoProgressFailureStorm(t *testing.T) {
	store, batch, source := stagingFixture(t, 32)
	remote := newFakeRemote(source)
	remote.transferFn = func(_ *fakeRemote, _ []int64, _ string) error {
		return &baidu.RemoteError{Operation: "synthetic outage", Errno: 999}
	}
	executor := testExecutor(store, remote)
	executor.MaxAttempts = 3
	if err := executor.Run(context.Background(), batch.TaskID); err == nil {
		t.Fatal("expected retryable staging failure")
	}
	if len(remote.calls) != 3 {
		t.Fatalf("generic outage should not recursively split; calls=%v", remote.calls)
	}
	stored, err := store.GetBatch(context.Background(), batch.TaskID, batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != state.BatchFailedRetryable {
		t.Fatalf("status=%s want=%s", stored.Status, state.BatchFailedRetryable)
	}
}

func TestExecutorBlocksOnUnexpectedObjectInToolBatch(t *testing.T) {
	store, batch, source := stagingFixture(t, 2)
	remote := newFakeRemote(source)
	remote.dirs[batch.BaiduStagingPath] = map[string]baidu.RemoteFile{
		"ghost.bin": {Name: "ghost.bin", Path: batch.BaiduStagingPath + "/ghost.bin", Size: 1},
	}
	err := testExecutor(store, remote).Run(context.Background(), batch.TaskID)
	var conflict *baidu.StagingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected permanent staging conflict, got %v", err)
	}
	if len(remote.calls) != 0 {
		t.Fatalf("conflict should block before transfer, calls=%v", remote.calls)
	}
	stored, err := store.GetBatch(context.Background(), batch.TaskID, batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != state.BatchFailedPermanent {
		t.Fatalf("batch status=%s", stored.Status)
	}
	failed, err := store.CountFilesByStatus(context.Background(), batch.TaskID, state.FileFailedPermanent)
	if err != nil {
		t.Fatal(err)
	}
	if failed != 2 {
		t.Fatalf("permanent failed files=%d want=2", failed)
	}
}

func TestExecutorSingleFileTransferConflictIsPermanent(t *testing.T) {
	store, batch, source := stagingFixture(t, 1)
	remote := newFakeRemote(source)
	remote.transferFn = func(_ *fakeRemote, _ []int64, _ string) error { return baidu.ErrTransferConflict }
	if err := testExecutor(store, remote).Run(context.Background(), batch.TaskID); err == nil {
		t.Fatal("expected single-file conflict")
	}
	stored, err := store.GetBatch(context.Background(), batch.TaskID, batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != state.BatchFailedPermanent {
		t.Fatalf("status=%s want=%s", stored.Status, state.BatchFailedPermanent)
	}
}

func stagingFixture(t *testing.T, count int) (*state.Store, state.Batch, map[int64]baidu.RemoteFile) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	taskID := fmt.Sprintf("task-fixture-%d", count)
	if err := store.CreateTask(ctx, state.Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	files := make([]manifest.File, 0, count)
	source := make(map[int64]baidu.RemoteFile, count)
	for i := 0; i < count; i++ {
		id := int64(1000 + i)
		name := fmt.Sprintf("f-%03d.bin", i)
		md5 := fmt.Sprintf("fake-md5-%03d", i)
		files = append(files, manifest.File{SourceID: fmt.Sprint(id), LogicalPath: "/bulk/" + name, ParentPath: "/bulk", Name: name, Size: int64(i + 1), MD5: md5})
		source[id] = baidu.RemoteFile{FsID: id + 9000, Name: name, Size: int64(i + 1), MD5: md5}
	}
	if err := store.UpsertManifestPage(ctx, taskID, []manifest.Directory{{LogicalPath: "/bulk"}}, files); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil || len(batches) != 1 {
		t.Fatalf("plan err=%v batches=%d", err, len(batches))
	}
	return store, batches[0], source
}

func testExecutor(store *state.Store, remote *fakeRemote) *Executor {
	link, _ := baidu.ParseShareLink("https://pan.baidu.com/s/1Synthetic")
	return &Executor{
		Repository:  store,
		Remote:      remote,
		Link:        link,
		Share:       baidu.ShareContext{BDSToken: "fake-token", ShareID: "1", ShareUK: "2"},
		MaxAttempts: 3,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	}
}

func assertBatchFullyStaged(t *testing.T, store *state.Store, taskID, batchID string, want int) {
	t.Helper()
	stored, err := store.GetBatch(context.Background(), taskID, batchID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != state.BatchStaged {
		t.Fatalf("batch status=%s want=%s", stored.Status, state.BatchStaged)
	}
	staged, err := store.CountFilesByStatus(context.Background(), taskID, state.FileBaiduStaged)
	if err != nil {
		t.Fatal(err)
	}
	if staged != want {
		t.Fatalf("staged=%d want=%d", staged, want)
	}
}
