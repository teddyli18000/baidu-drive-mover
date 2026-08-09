package state

import (
	"context"
	"testing"
)

func TestTaskRootCleanupRequiresEveryBatchProvenanceRowCleaned(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	batch := createCleanupBatchFixture(t, store, "task-root-gate", true)
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
	if err := store.ValidateBatchCleanupCompleteness(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatchCleanup(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.TaskRootCleanupCandidate(ctx, batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !candidate {
		t.Fatal("fully cleaned batch should make task root eligible")
	}

	store2 := newStagingTestStore(t)
	batch2 := createCleanupBatchFixture(t, store2, "task-root-missing", true)
	if _, err := store2.AuthorizeBatchCleanup(ctx, batch2.TaskID, batch2.BatchID); err != nil {
		t.Fatal(err)
	}
	for _, fileID := range []string{"101", "102"} {
		if err := store2.MarkLocalCacheCleanupDone(ctx, batch2.TaskID, fileID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store2.MarkBaiduBatchCleanupDone(ctx, batch2.TaskID, batch2.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store2.CompleteBatchCleanup(ctx, batch2.TaskID, batch2.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := store2.db.ExecContext(ctx, `
DELETE FROM owned_objects WHERE task_id = ? AND scope = ? AND object_id = ?`, batch2.TaskID, ownedScopeBaiduBatchDir, batch2.BatchID); err != nil {
		t.Fatal(err)
	}
	candidate, err = store2.TaskRootCleanupCandidate(ctx, batch2.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate {
		t.Fatal("missing batch provenance row unexpectedly allowed task-root cleanup")
	}
	if _, err := store2.AuthorizeTaskRootCleanup(ctx, batch2.TaskID); err == nil {
		t.Fatal("missing batch provenance row unexpectedly authorized task-root cleanup")
	}
}

func TestPipelineCompletionWaitsForBaiduTaskRootCleanup(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	batch := createCleanupBatchFixture(t, store, "task-progress-root", true)
	if err := store.SetTaskDriveRoot(ctx, batch.TaskID, "BaiduDriveMover-"+batch.TaskID, "drive-root"); err != nil {
		t.Fatal(err)
	}
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
	if err := store.ValidateBatchCleanupCompleteness(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatchCleanup(ctx, batch.TaskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	progress, err := store.PipelineProgress(ctx, batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Done != progress.Total || !progress.BaiduTaskRootRegistered || progress.BaiduTaskRootCleaned || progress.Complete() {
		t.Fatalf("pipeline completed before task-root cleanup: %+v", progress)
	}
	if !progress.HasCleanupWork() {
		t.Fatal("pipeline did not surface pending task-root cleanup")
	}
	if _, err := store.AuthorizeTaskRootCleanup(ctx, batch.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkBaiduTaskRootCleanupDone(ctx, batch.TaskID); err != nil {
		t.Fatal(err)
	}
	progress, err = store.PipelineProgress(ctx, batch.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Complete() {
		t.Fatalf("pipeline did not complete after task-root cleanup: %+v", progress)
	}
}
