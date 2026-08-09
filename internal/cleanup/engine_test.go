package cleanup

import (
	"context"
	"errors"
	"os"
	"path"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakeCleanupRemote struct {
	deleteCalls []string
	listCalls   []string
	deleteErr   error
	listErr     error
	listItems   []baidu.RemoteFile
}

func (r *fakeCleanupRemote) DeleteStagingPath(_ context.Context, remotePath string) error {
	r.deleteCalls = append(r.deleteCalls, remotePath)
	return r.deleteErr
}

func (r *fakeCleanupRemote) ListStagingPathForCleanup(_ context.Context, remotePath string) ([]baidu.RemoteFile, error) {
	r.listCalls = append(r.listCalls, remotePath)
	return append([]baidu.RemoteFile(nil), r.listItems...), r.listErr
}

func TestCleanupEngineRemovesOnlyAuthorizedBatchAndEmptyTaskRoot(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-engine")
	remote := &fakeCleanupRemote{}
	engine := &Engine{Layout: layout, Repository: store, Remote: remote}
	summary, err := engine.Run(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.BatchesDone != 1 || summary.FilesDone != 2 || summary.BytesFreed != 7 || !summary.TaskRootDone {
		t.Fatalf("unexpected cleanup summary: %+v", summary)
	}
	taskRoot := "/BaiduDriveMover/" + batch.TaskID
	wantDeletes := []string{batch.BaiduStagingPath, taskRoot}
	if len(remote.deleteCalls) != len(wantDeletes) {
		t.Fatalf("unexpected Baidu cleanup calls: %v", remote.deleteCalls)
	}
	for i := range wantDeletes {
		if remote.deleteCalls[i] != wantDeletes[i] {
			t.Fatalf("delete[%d]=%q want=%q", i, remote.deleteCalls[i], wantDeletes[i])
		}
	}
	if len(remote.listCalls) != 1 || remote.listCalls[0] != taskRoot {
		t.Fatalf("task root was not inspected exactly once: %v", remote.listCalls)
	}
	for _, fileID := range []string{"101", "102"} {
		rel := path.Join("cache", batch.TaskID, fileID+".bin")
		if _, err := layout.StatTempFile(rel); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cache %s still exists or failed unexpectedly: %v", rel, err)
		}
		file, err := store.GetFile(context.Background(), batch.TaskID, fileID)
		if err != nil {
			t.Fatal(err)
		}
		if file.Status != state.FileDone {
			t.Fatalf("file %s status=%s want DONE", fileID, file.Status)
		}
	}
	registered, cleaned, err := store.BaiduTaskRootCleanupState(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !registered || !cleaned {
		t.Fatalf("task root cleanup state registered=%v cleaned=%v", registered, cleaned)
	}
	reserved, err := store.ReservedCacheBytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("reserved cache bytes=%d want=0", reserved)
	}
}

func TestCleanupEngineRestartsAfterLocalDeletionAndBaiduFailure(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-restart")
	remote := &fakeCleanupRemote{deleteErr: errors.New("synthetic Baidu outage")}
	engine := &Engine{Layout: layout, Repository: store, Remote: remote}
	if _, err := engine.Run(context.Background(), batch.TaskID); err == nil {
		t.Fatal("expected first cleanup pass to fail at Baidu deletion")
	}
	for _, fileID := range []string{"101", "102"} {
		rel := path.Join("cache", batch.TaskID, fileID+".bin")
		if _, err := layout.StatTempFile(rel); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("local cache was not removed before remote failure: %v", err)
		}
		file, err := store.GetFile(context.Background(), batch.TaskID, fileID)
		if err != nil {
			t.Fatal(err)
		}
		if file.Status != state.FileCleanupPending {
			t.Fatalf("file %s status=%s want CLEANUP_PENDING", fileID, file.Status)
		}
	}
	reserved, err := store.ReservedCacheBytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 7 {
		t.Fatalf("reservation released before whole batch cleanup: %d", reserved)
	}

	remote.deleteErr = baidu.ErrStagingNotFound
	summary, err := engine.Run(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.BatchesDone != 1 || summary.FilesDone != 2 || !summary.TaskRootDone {
		t.Fatalf("restart did not complete cleanup: %+v", summary)
	}
	if len(remote.deleteCalls) != 3 {
		t.Fatalf("remote delete calls=%d want=3 (%v)", len(remote.deleteCalls), remote.deleteCalls)
	}
	reserved, err = store.ReservedCacheBytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("reservation remains after recovered cleanup: %d", reserved)
	}
}

func TestCleanupEngineRefusesTaskRootContainingUnknownObject(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-unknown-root")
	remote := &fakeCleanupRemote{listItems: []baidu.RemoteFile{{Name: "unknown", Path: "/BaiduDriveMover/task-unknown-root/unknown", IsDir: true}}}
	engine := &Engine{Layout: layout, Repository: store, Remote: remote}
	if _, err := engine.Run(context.Background(), batch.TaskID); err == nil {
		t.Fatal("expected non-empty task root to block deletion")
	}
	taskRoot := "/BaiduDriveMover/" + batch.TaskID
	for _, deleted := range remote.deleteCalls {
		if deleted == taskRoot {
			t.Fatalf("non-empty task root was deleted: %v", remote.deleteCalls)
		}
	}
	registered, cleaned, err := store.BaiduTaskRootCleanupState(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !registered || cleaned {
		t.Fatalf("unexpected task root cleanup state registered=%v cleaned=%v", registered, cleaned)
	}
}

func TestCleanupEngineReconcilesAlreadyMissingTaskRoot(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-missing-root")
	remote := &fakeCleanupRemote{listErr: baidu.ErrStagingNotFound}
	engine := &Engine{Layout: layout, Repository: store, Remote: remote}
	summary, err := engine.Run(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.TaskRootDone {
		t.Fatal("already-missing task root was not reconciled")
	}
	taskRoot := "/BaiduDriveMover/" + batch.TaskID
	for _, deleted := range remote.deleteCalls {
		if deleted == taskRoot {
			t.Fatalf("already-missing task root should not require delete: %v", remote.deleteCalls)
		}
	}
}

func newCleanupEngineFixture(t *testing.T, taskID string) (*runtimepath.Layout, *state.Store, state.Batch) {
	t.Helper()
	ctx := context.Background()
	layout, err := runtimepath.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(layout.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateTask(ctx, state.Task{
		ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManifestPage(ctx, taskID, nil, []manifest.File{
		{SourceID: "101", LogicalPath: "/docs/a.bin", ParentPath: "/docs", Name: "a.bin", Size: 3},
		{SourceID: "102", LogicalPath: "/docs/b.bin", ParentPath: "/docs", Name: "b.bin", Size: 4},
	}); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil || len(batches) != 1 {
		t.Fatalf("plan err=%v count=%d", err, len(batches))
	}
	batch := batches[0]
	if err := store.StartBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordStagedFiles(ctx, taskID, batch.BatchID, map[string]string{
		"101": batch.BaiduStagingPath + "/a.bin",
		"102": batch.BaiduStagingPath + "/b.bin",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.EnsureTempDir(path.Join("cache", taskID)); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id   string
		data []byte
	}{
		{id: "101", data: []byte("abc")},
		{id: "102", data: []byte("defg")},
	} {
		rel := path.Join("cache", taskID, item.id+".bin")
		file, err := layout.OpenTempFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(item.data); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkLocalReady(ctx, taskID, item.id, rel); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordDriveUploaded(ctx, taskID, item.id, "drive-"+item.id); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkDriveVerified(ctx, taskID, item.id, "drive-"+item.id); err != nil {
			t.Fatal(err)
		}
	}
	return layout, store, batch
}
