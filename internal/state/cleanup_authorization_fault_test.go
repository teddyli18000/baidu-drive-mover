package state

import (
	"context"
	"testing"
)

func TestCleanupAuthorizationRejectsChangedBaiduBatchPath(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	batch := createCleanupBatchFixture(t, store, "task-bad-batch-path", true)
	if _, err := store.db.ExecContext(ctx, `
UPDATE batches SET baidu_staging_path = '/BaiduDriveMover/other/attacker'
WHERE task_id = ? AND batch_id = ?`, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthorizeBatchCleanup(ctx, batch.TaskID, batch.BatchID); err == nil {
		t.Fatal("changed Baidu batch path unexpectedly authorized cleanup")
	}
	assertNoCleanupAuthorization(t, store, batch.TaskID)
}

func TestCleanupAuthorizationRejectsMissingDriveIdentity(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	batch := createCleanupBatchFixture(t, store, "task-missing-drive", true)
	if _, err := store.db.ExecContext(ctx, `
UPDATE files SET drive_id = '' WHERE task_id = ? AND file_id = '102'`, batch.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthorizeBatchCleanup(ctx, batch.TaskID, batch.BatchID); err == nil {
		t.Fatal("missing Drive ID unexpectedly authorized cleanup")
	}
	assertNoCleanupAuthorization(t, store, batch.TaskID)
}

func TestCleanupCompletionRejectsMissingAuthorizedProvenanceRow(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	batch := createCleanupBatchFixture(t, store, "task-missing-local-row", true)
	if _, err := store.AuthorizeBatchCleanup(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	for _, fileID := range []string{"101", "102"} {
		if err := store.MarkLocalCacheCleanupDone(ctx, batch.TaskID, fileID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkBaiduBatchCleanupDone(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
DELETE FROM owned_objects WHERE task_id = ? AND scope = ? AND object_id = '102'`, batch.TaskID, ownedScopeLocalCacheFile); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateBatchCleanupCompleteness(ctx, batch.TaskID, batch.BatchID); err == nil {
		t.Fatal("missing local provenance row unexpectedly passed cleanup completeness")
	}
	if err := store.CompleteBatchCleanup(ctx, batch.TaskID, batch.BatchID); err == nil {
		t.Fatal("missing local provenance row should not be accepted by completion gate")
	}
}

func assertNoCleanupAuthorization(t *testing.T, store *Store, taskID string) {
	t.Helper()
	var allowed int
	if err := store.db.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM owned_objects WHERE task_id = ? AND cleanup_allowed != 0`, taskID).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed != 0 {
		t.Fatalf("rejected cleanup authorized %d objects", allowed)
	}
}
