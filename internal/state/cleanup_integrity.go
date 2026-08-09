package state

import (
	"context"
	"fmt"
)

func (s *Store) ValidateBatchCleanupCompleteness(ctx context.Context, taskID, batchID string) error {
	var fileCount int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM batch_files WHERE task_id = ? AND batch_id = ?`, taskID, batchID).Scan(&fileCount); err != nil {
		return fmt.Errorf("count cleanup batch files: %w", err)
	}
	if fileCount == 0 {
		return fmt.Errorf("cleanup batch %q has no registered files", batchID)
	}
	var localCount int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM owned_objects o
WHERE o.task_id = ? AND o.scope = ?
  AND o.object_id IN (
    SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?
  )`, taskID, ownedScopeLocalCacheFile, taskID, batchID).Scan(&localCount); err != nil {
		return fmt.Errorf("count local cleanup provenance: %w", err)
	}
	if localCount != fileCount {
		return fmt.Errorf("cleanup batch %q has %d local provenance rows for %d files", batchID, localCount, fileCount)
	}
	var baiduCount int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM owned_objects
WHERE task_id = ? AND scope = ? AND object_id = ?`, taskID, ownedScopeBaiduBatchDir, batchID).Scan(&baiduCount); err != nil {
		return fmt.Errorf("count Baidu batch cleanup provenance: %w", err)
	}
	if baiduCount != 1 {
		return fmt.Errorf("cleanup batch %q has %d Baidu provenance rows", batchID, baiduCount)
	}
	var incomplete int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM owned_objects o
WHERE o.task_id = ? AND (
    (o.scope = ? AND o.object_id = ?) OR
    (o.scope = ? AND o.object_id IN (
        SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?
    ))
) AND (o.cleanup_allowed != 1 OR o.cleaned_at = '')`,
		taskID, ownedScopeBaiduBatchDir, batchID, ownedScopeLocalCacheFile, taskID, batchID).Scan(&incomplete); err != nil {
		return fmt.Errorf("count incomplete cleanup provenance: %w", err)
	}
	if incomplete != 0 {
		return fmt.Errorf("cleanup batch %q still has %d unreconciled objects", batchID, incomplete)
	}
	return nil
}
