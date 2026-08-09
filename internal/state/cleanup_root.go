package state

import (
	"context"
	"fmt"
	"path"
)

func expectedBaiduTaskRootPath(taskID string) (string, error) {
	if !stagingComponentPattern.MatchString(taskID) {
		return "", fmt.Errorf("unsafe Baidu task cleanup identity %q", taskID)
	}
	return path.Join(baiduStagingRoot, taskID), nil
}

func (s *Store) TaskRootCleanupCandidate(ctx context.Context, taskID string) (bool, error) {
	var rootCount int
	var cleanedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MAX(cleaned_at), '')
FROM owned_objects
WHERE task_id = ? AND scope = ? AND object_id = ?`, taskID, ownedScopeBaiduTaskRoot, taskID).Scan(&rootCount, &cleanedAt)
	if err != nil {
		return false, fmt.Errorf("query Baidu task-root provenance: %w", err)
	}
	if rootCount == 0 || cleanedAt != "" {
		return false, nil
	}
	var unfinishedFiles int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE task_id = ? AND status != ?`, taskID, FileDone).Scan(&unfinishedFiles); err != nil {
		return false, fmt.Errorf("count unfinished files before task-root cleanup: %w", err)
	}
	if unfinishedFiles != 0 {
		return false, nil
	}
	batchCount, cleanedBatchCount, err := s.taskRootBatchCleanupCounts(ctx, taskID)
	if err != nil {
		return false, err
	}
	return batchCount == cleanedBatchCount, nil
}

func (s *Store) AuthorizeTaskRootCleanup(ctx context.Context, taskID string) (CleanupObject, error) {
	expected, err := expectedBaiduTaskRootPath(taskID)
	if err != nil {
		return CleanupObject{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CleanupObject{}, fmt.Errorf("begin task-root cleanup authorization: %w", err)
	}
	defer tx.Rollback()
	var unfinishedFiles int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE task_id = ? AND status != ?`, taskID, FileDone).Scan(&unfinishedFiles); err != nil {
		return CleanupObject{}, fmt.Errorf("count unfinished files before task-root authorization: %w", err)
	}
	if unfinishedFiles != 0 {
		return CleanupObject{}, fmt.Errorf("Baidu task root is not cleanup eligible: %d files are not DONE", unfinishedFiles)
	}
	var batchCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE task_id = ?`, taskID).Scan(&batchCount); err != nil {
		return CleanupObject{}, fmt.Errorf("count task batches before root authorization: %w", err)
	}
	var cleanedBatchCount int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM owned_objects
WHERE task_id = ? AND scope = ? AND cleanup_allowed = 1 AND cleaned_at != ''`, taskID, ownedScopeBaiduBatchDir).Scan(&cleanedBatchCount); err != nil {
		return CleanupObject{}, fmt.Errorf("count cleaned batch provenance before root authorization: %w", err)
	}
	if batchCount != cleanedBatchCount {
		return CleanupObject{}, fmt.Errorf("Baidu task root is not cleanup eligible: %d batches but %d cleaned provenance rows", batchCount, cleanedBatchCount)
	}
	object, err := readOwnedObjectTx(ctx, tx, taskID, ownedScopeBaiduTaskRoot, taskID)
	if err != nil {
		return CleanupObject{}, fmt.Errorf("read Baidu task-root provenance: %w", err)
	}
	if object.ObjectPath != expected {
		return CleanupObject{}, fmt.Errorf("Baidu task-root provenance path %q does not match %q", object.ObjectPath, expected)
	}
	if object.CleanedAt != "" {
		if err := tx.Commit(); err != nil {
			return CleanupObject{}, err
		}
		return object, nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE owned_objects SET cleanup_allowed = 1, last_error = ''
WHERE task_id = ? AND scope = ? AND object_id = ? AND object_path = ?`, taskID, ownedScopeBaiduTaskRoot, taskID, expected); err != nil {
		return CleanupObject{}, fmt.Errorf("authorize Baidu task-root cleanup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CleanupObject{}, fmt.Errorf("commit task-root cleanup authorization: %w", err)
	}
	object, err = s.readOwnedObject(ctx, taskID, ownedScopeBaiduTaskRoot, taskID)
	if err != nil {
		return CleanupObject{}, err
	}
	if !object.CleanupAllowed || object.ObjectPath != expected {
		return CleanupObject{}, fmt.Errorf("Baidu task-root cleanup authorization did not persist")
	}
	return object, nil
}

func (s *Store) MarkBaiduTaskRootCleanupDone(ctx context.Context, taskID string) error {
	return s.MarkCleanupObjectDone(ctx, taskID, ownedScopeBaiduTaskRoot, taskID)
}

func (s *Store) RecordBaiduTaskRootCleanupFailure(ctx context.Context, taskID, message string) error {
	return s.RecordCleanupObjectFailure(ctx, taskID, ownedScopeBaiduTaskRoot, taskID, message)
}

func (s *Store) BaiduTaskRootCleanupState(ctx context.Context, taskID string) (registered bool, cleaned bool, err error) {
	var count int
	var cleanedAt string
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MAX(cleaned_at), '')
FROM owned_objects
WHERE task_id = ? AND scope = ? AND object_id = ?`, taskID, ownedScopeBaiduTaskRoot, taskID).Scan(&count, &cleanedAt); err != nil {
		return false, false, fmt.Errorf("read Baidu task-root cleanup state: %w", err)
	}
	return count > 0, cleanedAt != "", nil
}

func (s *Store) taskRootBatchCleanupCounts(ctx context.Context, taskID string) (int, int, error) {
	var batchCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE task_id = ?`, taskID).Scan(&batchCount); err != nil {
		return 0, 0, fmt.Errorf("count task batches before root cleanup: %w", err)
	}
	var cleanedBatchCount int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM owned_objects
WHERE task_id = ? AND scope = ? AND cleanup_allowed = 1 AND cleaned_at != ''`, taskID, ownedScopeBaiduBatchDir).Scan(&cleanedBatchCount); err != nil {
		return 0, 0, fmt.Errorf("count cleaned batch provenance before root cleanup: %w", err)
	}
	return batchCount, cleanedBatchCount, nil
}
