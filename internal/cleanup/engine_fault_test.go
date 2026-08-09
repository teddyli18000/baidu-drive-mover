package cleanup

import (
	"context"
	"errors"
	"os"
	"path"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/baidu"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type cancelAfterAuthorizeRepo struct {
	*state.Store
	cancel context.CancelFunc
}

func (r *cancelAfterAuthorizeRepo) AuthorizeBatchCleanup(ctx context.Context, taskID, batchID string) (state.CleanupBatch, error) {
	batch, err := r.Store.AuthorizeBatchCleanup(ctx, taskID, batchID)
	if err == nil {
		r.cancel()
	}
	return batch, err
}

type cancelAfterFirstLocalRepo struct {
	*state.Store
	cancel context.CancelFunc
	marks  int
}

func (r *cancelAfterFirstLocalRepo) MarkLocalCacheCleanupDone(ctx context.Context, taskID, fileID string) error {
	if err := r.Store.MarkLocalCacheCleanupDone(ctx, taskID, fileID); err != nil {
		return err
	}
	r.marks++
	if r.marks == 1 {
		r.cancel()
	}
	return nil
}

type failBaiduMarkRepo struct {
	*state.Store
	fail bool
}

func (r *failBaiduMarkRepo) MarkBaiduBatchCleanupDone(ctx context.Context, taskID, batchID string) error {
	if r.fail {
		r.fail = false
		return errors.New("synthetic post-delete Baidu persistence failure")
	}
	return r.Store.MarkBaiduBatchCleanupDone(ctx, taskID, batchID)
}

type failCompleteBatchRepo struct {
	*state.Store
	fail bool
}

func (r *failCompleteBatchRepo) CompleteBatchCleanup(ctx context.Context, taskID, batchID string) error {
	if r.fail {
		r.fail = false
		return errors.New("synthetic cleanup completion transaction failure")
	}
	return r.Store.CompleteBatchCleanup(ctx, taskID, batchID)
}

func TestCleanupCancellationAfterAuthorizationStartsNoDelete(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-cancel-authorized")
	ctx, cancel := context.WithCancel(context.Background())
	repo := &cancelAfterAuthorizeRepo{Store: store, cancel: cancel}
	remote := &fakeCleanupRemote{}
	_, err := (&Engine{Layout: layout, Repository: repo, Remote: remote}).Run(ctx, batch.TaskID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if len(remote.deleteCalls) != 0 || len(remote.listCalls) != 0 {
		t.Fatalf("cancellation after authorization started remote cleanup: delete=%v list=%v", remote.deleteCalls, remote.listCalls)
	}
	for _, fileID := range []string{"101", "102"} {
		rel := path.Join("cache", batch.TaskID, fileID+".bin")
		if _, err := layout.StatTempFile(rel); err != nil {
			t.Fatalf("cache %s changed after authorization-only cancellation: %v", rel, err)
		}
		file, err := store.GetFile(context.Background(), batch.TaskID, fileID)
		if err != nil {
			t.Fatal(err)
		}
		if file.Status != state.FileCleanupPending {
			t.Fatalf("file %s status=%s want CLEANUP_PENDING", fileID, file.Status)
		}
	}
}

func TestCleanupCancellationAfterFirstLocalDeleteResumesExactly(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-cancel-mid-local")
	ctx, cancel := context.WithCancel(context.Background())
	repo := &cancelAfterFirstLocalRepo{Store: store, cancel: cancel}
	remote := &fakeCleanupRemote{}
	_, err := (&Engine{Layout: layout, Repository: repo, Remote: remote}).Run(ctx, batch.TaskID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if len(remote.deleteCalls) != 0 {
		t.Fatalf("remote delete started after mid-local cancellation: %v", remote.deleteCalls)
	}
	missing := 0
	present := 0
	for _, fileID := range []string{"101", "102"} {
		rel := path.Join("cache", batch.TaskID, fileID+".bin")
		_, statErr := layout.StatTempFile(rel)
		if errors.Is(statErr, os.ErrNotExist) {
			missing++
		} else if statErr == nil {
			present++
		} else {
			t.Fatal(statErr)
		}
	}
	if missing != 1 || present != 1 {
		t.Fatalf("expected exactly one local deletion before cancellation, missing=%d present=%d", missing, present)
	}

	restartRemote := &fakeCleanupRemote{}
	summary, err := (&Engine{Layout: layout, Repository: store, Remote: restartRemote}).Run(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesDone != 2 || !summary.TaskRootDone {
		t.Fatalf("restart did not complete partial local cleanup: %+v", summary)
	}
}

func TestCleanupRecoversWhenBaiduDeleteSucceededBeforePersistenceFailure(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-baidu-post-delete")
	repo := &failBaiduMarkRepo{Store: store, fail: true}
	remote := &fakeCleanupRemote{}
	if _, err := (&Engine{Layout: layout, Repository: repo, Remote: remote}).Run(context.Background(), batch.TaskID); err == nil {
		t.Fatal("expected synthetic post-delete persistence failure")
	}
	if len(remote.deleteCalls) != 1 || remote.deleteCalls[0] != batch.BaiduStagingPath {
		t.Fatalf("unexpected first-pass deletes: %v", remote.deleteCalls)
	}
	for _, fileID := range []string{"101", "102"} {
		file, err := store.GetFile(context.Background(), batch.TaskID, fileID)
		if err != nil {
			t.Fatal(err)
		}
		if file.Status != state.FileCleanupPending {
			t.Fatalf("file %s status=%s want CLEANUP_PENDING", fileID, file.Status)
		}
	}

	remote.deleteErr = baidu.ErrStagingNotFound
	summary, err := (&Engine{Layout: layout, Repository: repo, Remote: remote}).Run(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesDone != 2 || !summary.TaskRootDone {
		t.Fatalf("restart did not reconcile committed Baidu deletion: %+v", summary)
	}
}

func TestCleanupRecoversWhenBatchCompletionTransactionFails(t *testing.T) {
	layout, store, batch := newCleanupEngineFixture(t, "task-completion-failure")
	repo := &failCompleteBatchRepo{Store: store, fail: true}
	remote := &fakeCleanupRemote{}
	if _, err := (&Engine{Layout: layout, Repository: repo, Remote: remote}).Run(context.Background(), batch.TaskID); err == nil {
		t.Fatal("expected synthetic batch completion failure")
	}
	if len(remote.deleteCalls) != 1 || remote.deleteCalls[0] != batch.BaiduStagingPath {
		t.Fatalf("unexpected first-pass deletes: %v", remote.deleteCalls)
	}
	for _, fileID := range []string{"101", "102"} {
		file, err := store.GetFile(context.Background(), batch.TaskID, fileID)
		if err != nil {
			t.Fatal(err)
		}
		if file.Status != state.FileCleanupPending {
			t.Fatalf("file %s status=%s want CLEANUP_PENDING", fileID, file.Status)
		}
	}

	summary, err := (&Engine{Layout: layout, Repository: repo, Remote: remote}).Run(context.Background(), batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesDone != 2 || !summary.TaskRootDone {
		t.Fatalf("restart did not complete after DB transaction failure: %+v", summary)
	}
	if len(remote.deleteCalls) != 2 {
		t.Fatalf("already-cleaned batch was deleted again: %v", remote.deleteCalls)
	}
	if remote.deleteCalls[1] != "/BaiduDriveMover/"+batch.TaskID {
		t.Fatalf("second delete should be only empty task root: %v", remote.deleteCalls)
	}
}
