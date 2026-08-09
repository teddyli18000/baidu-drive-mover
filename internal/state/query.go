package state

import (
	"context"
	"fmt"
)

func (s *Store) GetBatch(ctx context.Context, taskID, batchID string) (Batch, error) {
	var batch Batch
	batch.TaskID = taskID
	err := s.db.QueryRowContext(ctx, `
SELECT batch_id, logical_parent, baidu_staging_path, status, file_count, total_bytes, retry_count, last_error
FROM batches WHERE task_id = ? AND batch_id = ?`, taskID, batchID).Scan(
		&batch.BatchID, &batch.LogicalParent, &batch.BaiduStagingPath, &batch.Status,
		&batch.FileCount, &batch.TotalBytes, &batch.RetryCount, &batch.LastError,
	)
	if err != nil {
		return Batch{}, fmt.Errorf("get batch %q: %w", batchID, err)
	}
	files, err := s.batchFiles(ctx, taskID, batchID)
	if err != nil {
		return Batch{}, err
	}
	batch.Files = files
	return batch, nil
}

func (s *Store) CountFilesByStatus(ctx context.Context, taskID string, status FileStatus) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE task_id = ? AND status = ?`, taskID, status).Scan(&count); err != nil {
		return 0, fmt.Errorf("count task files by status: %w", err)
	}
	return count, nil
}
