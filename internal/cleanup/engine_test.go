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
	calls []string
	err   error
}

func (r *fakeCleanupRemote) DeleteStagingPath(_ context.Context, remotePath string) error {
	r.calls = append(r.calls, remotePath)
	return r.err
}

func TestCleanupEngineRemovesOnlyAuthorizedBatchAndReleasesReservation(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-engine")
	remote := &fakeCleanupRemote{}
	engine := &Engine{Layout: layout, Repository: store, Remote: remote}
	summary, err := engine.Run(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.BatchesDone != 1 || summary.FilesDone != 2 || summary.BytesFreed != 7 {
		t.Fatalf("unexpected cleanup summary: %+v", summary)
	}
	if len(remote.calls) != 1 || remote.calls[0] != batch.BaiduStagingPath {
		t.Fatalf("unexpected Baidu cleanup calls: %v", remote.calls)
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
	remote := &fakeCleanupRemote{err: errors.New("synthetic Baidu outage")}
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

	remote.err = baidu.ErrStagingNotFound
	summary, err := engine.Run(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.BatchesDone != 1 || summary.FilesDone != 2 {
		t.Fatalf("restart did not complete cleanup: %+v", summary)
	}
	if len(remote.calls) != 2 {
		t.Fatalf("remote delete calls=%d want=2", len(remote.calls))
	}
	reserved, err = store.ReservedCacheBytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("reservation remains after recovered cleanup: %d", reserved)
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
