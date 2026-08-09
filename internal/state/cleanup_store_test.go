package state

import (
	"context"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

func TestBatchCleanupRequiresEveryFileVerifiedAndNeverAuthorizesDrive(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	batch := createCleanupBatchFixture(t, store, "task-clean", false)

	if _, err := store.AuthorizeBatchCleanup(ctx, batch.TaskID, batch.BatchID); err == nil {
		t.Fatal("expected mixed verified/unverified batch to be rejected")
	}
	var allowed int
	if err := store.db.QueryRowContext(ctx, `
SELECT cleanup_allowed FROM owned_objects
WHERE task_id = ? AND scope = 'baidu_batch_dir' AND object_id = ?`, batch.TaskID, batch.BatchID).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed != 0 {
		t.Fatal("rejected cleanup unexpectedly authorized Baidu deletion")
	}

	verifyFixtureFile(t, store, batch.TaskID, "102", "drive-102")
	authorized, err := store.AuthorizeBatchCleanup(ctx, batch.TaskID, batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorized.Files) != 2 || len(authorized.LocalObjects) != 2 || !authorized.BaiduObject.CleanupAllowed {
		t.Fatalf("unexpected authorized cleanup batch: %+v", authorized)
	}
	for _, object := range authorized.LocalObjects {
		if !object.CleanupAllowed || object.CleanedAt != "" {
			t.Fatalf("local cleanup object not pending authorization: %+v", object)
		}
	}
	var driveAuthorized int
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM owned_objects
WHERE task_id = ? AND scope IN ('drive_file','drive_directory','drive_task_root') AND cleanup_allowed != 0`, batch.TaskID).Scan(&driveAuthorized); err != nil {
		t.Fatal(err)
	}
	if driveAuthorized != 0 {
		t.Fatalf("Drive objects became cleanup authorized: %d", driveAuthorized)
	}
	for _, fileID := range []string{"101", "102"} {
		file, err := store.GetFile(ctx, batch.TaskID, fileID)
		if err != nil {
			t.Fatal(err)
		}
		if file.Status != FileCleanupPending {
			t.Fatalf("file %s status=%s want CLEANUP_PENDING", fileID, file.Status)
		}
	}
}

func TestBatchCleanupCompletionRequiresEveryAuthorizedObjectReconciled(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	batch := createCleanupBatchFixture(t, store, "task-complete", true)
	if _, err := store.AuthorizeBatchCleanup(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatchCleanup(ctx, batch.TaskID, batch.BatchID); err == nil {
		t.Fatal("expected completion before deletion evidence to fail")
	}
	for _, fileID := range []string{"101", "102"} {
		if err := store.MarkCleanupObjectDone(ctx, batch.TaskID, ownedScopeLocalCacheFile, fileID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CompleteBatchCleanup(ctx, batch.TaskID, batch.BatchID); err == nil {
		t.Fatal("expected completion before Baidu deletion evidence to fail")
	}
	if err := store.MarkCleanupObjectDone(ctx, batch.TaskID, ownedScopeBaiduBatchDir, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatchCleanup(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	for _, fileID := range []string{"101", "102"} {
		file, err := store.GetFile(ctx, batch.TaskID, fileID)
		if err != nil {
			t.Fatal(err)
		}
		if file.Status != FileDone {
			t.Fatalf("file %s status=%s want DONE", fileID, file.Status)
		}
	}
}

func TestCleanupAuthorizationBackfillsOnlyProvableV05LocalPath(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	batch := createCleanupBatchFixture(t, store, "task-backfill", true)
	if _, err := store.db.ExecContext(ctx, `DELETE FROM owned_objects WHERE task_id = ? AND scope = 'local_cache_file'`, batch.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthorizeBatchCleanup(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatalf("provable v0.5 local provenance should be backfilled: %v", err)
	}

	store2 := newStagingTestStore(t)
	batch2 := createCleanupBatchFixture(t, store2, "task-bad-path", true)
	if _, err := store2.db.ExecContext(ctx, `
UPDATE owned_objects SET object_path = 'cache/other/attacker.bin'
WHERE task_id = ? AND scope = 'local_cache_file' AND object_id = '101'`, batch2.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store2.AuthorizeBatchCleanup(ctx, batch2.TaskID, batch2.BatchID); err == nil {
		t.Fatal("expected mismatched local provenance path rejection")
	}
}

func TestMarkLocalReadyRejectsNonOpaquePath(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	createStagingTestTask(t, store, "task-local")
	if err := store.UpsertManifestPage(ctx, "task-local", nil, []manifest.File{{
		SourceID: "501", LogicalPath: "/a.bin", ParentPath: "/", Name: "a.bin", Size: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatches(ctx, "task-local", 200)
	if err != nil || len(batches) != 1 {
		t.Fatalf("plan err=%v batches=%d", err, len(batches))
	}
	batch := batches[0]
	if err := store.StartBatch(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordStagedFiles(ctx, batch.TaskID, batch.BatchID, map[string]string{
		"501": batch.BaiduStagingPath + "/a.bin",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatch(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkLocalReady(ctx, "task-local", "501", "cache/other/501.bin"); err == nil {
		t.Fatal("expected non-opaque local-ready path rejection")
	}
}

func createCleanupBatchFixture(t *testing.T, store *Store, taskID string, verifySecond bool) Batch {
	t.Helper()
	ctx := context.Background()
	createStagingTestTask(t, store, taskID)
	if err := store.UpsertManifestPage(ctx, taskID, nil, []manifest.File{
		{SourceID: "101", LogicalPath: "/docs/a.bin", ParentPath: "/docs", Name: "a.bin", Size: 3},
		{SourceID: "102", LogicalPath: "/docs/b.bin", ParentPath: "/docs", Name: "b.bin", Size: 4},
	}); err != nil {
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
	if err := store.RecordStagedFiles(ctx, taskID, batch.BatchID, map[string]string{
		"101": batch.BaiduStagingPath + "/a.bin",
		"102": batch.BaiduStagingPath + "/b.bin",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	for _, fileID := range []string{"101", "102"} {
		cachePath, err := expectedLocalCachePath(taskID, fileID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkLocalReady(ctx, taskID, fileID, cachePath); err != nil {
			t.Fatal(err)
		}
	}
	verifyFixtureFile(t, store, taskID, "101", "drive-101")
	if verifySecond {
		verifyFixtureFile(t, store, taskID, "102", "drive-102")
	}
	return batch
}

func verifyFixtureFile(t *testing.T, store *Store, taskID, fileID, driveID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.RecordDriveUploaded(ctx, taskID, fileID, driveID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDriveVerified(ctx, taskID, fileID, driveID); err != nil {
		t.Fatal(err)
	}
}
