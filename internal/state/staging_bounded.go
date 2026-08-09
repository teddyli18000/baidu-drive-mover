package state

import (
	"context"
	"fmt"
	"path"
	"time"
)

// PlanBatchesBounded is the byte-aware staging planner used once the local
// cache becomes a downstream constraint. A single source file may exceed
// maxBytes; it is isolated into its own batch so the caller can block before
// any remote write instead of silently omitting it.
func (s *Store) PlanBatchesBounded(ctx context.Context, taskID string, maxFiles int, maxBytes int64) ([]Batch, error) {
	if maxBytes <= 0 {
		return s.PlanBatches(ctx, taskID, maxFiles)
	}
	if maxFiles <= 0 || maxFiles >= 500 {
		return nil, fmt.Errorf("staging batch size must be between 1 and 499, got %d", maxFiles)
	}
	if !stagingComponentPattern.MatchString(taskID) {
		return nil, fmt.Errorf("unsafe task ID for Baidu staging: %q", taskID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin bounded batch planning: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
SELECT file_id, logical_path, parent_path, name, size, md5, status
FROM files
WHERE task_id = ? AND status = ?
ORDER BY parent_path, logical_path`, taskID, FileDiscovered)
	if err != nil {
		return nil, fmt.Errorf("query discovered files: %w", err)
	}
	var discovered []File
	for rows.Next() {
		var file File
		file.TaskID = taskID
		if err := rows.Scan(&file.FileID, &file.LogicalPath, &file.ParentPath, &file.Name, &file.Size, &file.MD5, &file.Status); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan discovered file: %w", err)
		}
		discovered = append(discovered, file)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate discovered files: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close discovered file rows: %w", err)
	}
	if len(discovered) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty bounded batch plan: %w", err)
		}
		return nil, nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	taskRoot := path.Join(baiduStagingRoot, taskID)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, created_at)
VALUES(?, 'baidu_task_root', ?, ?, 0, ?)
ON CONFLICT(task_id, scope, object_id) DO NOTHING`, taskID, taskID, taskRoot, now); err != nil {
		return nil, fmt.Errorf("register Baidu task root: %w", err)
	}

	var planned []Batch
	for parentStart := 0; parentStart < len(discovered); {
		parent := discovered[parentStart].ParentPath
		parentEnd := parentStart
		for parentEnd < len(discovered) && discovered[parentEnd].ParentPath == parent {
			parentEnd++
		}
		parentFiles := discovered[parentStart:parentEnd]
		for chunkStart := 0; chunkStart < len(parentFiles); {
			chunkEnd := chunkStart
			var totalBytes int64
			for chunkEnd < len(parentFiles) && chunkEnd-chunkStart < maxFiles {
				next := parentFiles[chunkEnd]
				if chunkEnd > chunkStart && totalBytes+next.Size > maxBytes {
					break
				}
				totalBytes += next.Size
				chunkEnd++
				if totalBytes >= maxBytes {
					break
				}
			}
			if chunkEnd == chunkStart {
				chunkEnd++
				totalBytes = parentFiles[chunkStart].Size
			}
			files := append([]File(nil), parentFiles[chunkStart:chunkEnd]...)
			batchID := deterministicBatchID(taskID, parent, files)
			remotePath := path.Join(taskRoot, batchID)

			if _, err := tx.ExecContext(ctx, `
INSERT INTO batches(task_id, batch_id, logical_parent, status, file_count, total_bytes, retry_count, last_error, created_at, updated_at, baidu_staging_path)
VALUES(?, ?, ?, ?, ?, ?, 0, '', ?, ?, ?)`,
				taskID, batchID, parent, BatchPending, len(files), totalBytes, now, now, remotePath); err != nil {
				return nil, fmt.Errorf("insert bounded staging batch %q: %w", batchID, err)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, created_at)
VALUES(?, 'baidu_batch_dir', ?, ?, 0, ?)
ON CONFLICT(task_id, scope, object_id) DO NOTHING`, taskID, batchID, remotePath, now); err != nil {
				return nil, fmt.Errorf("register Baidu batch directory %q: %w", batchID, err)
			}
			for ordinal, file := range files {
				if _, err := tx.ExecContext(ctx, `
INSERT INTO batch_files(task_id, batch_id, file_id, ordinal)
VALUES(?, ?, ?, ?)`, taskID, batchID, file.FileID, ordinal); err != nil {
					return nil, fmt.Errorf("add file %q to bounded batch %q: %w", file.FileID, batchID, err)
				}
				result, err := tx.ExecContext(ctx, `
UPDATE files SET status = ?, updated_at = ?
WHERE task_id = ? AND file_id = ? AND status = ?`, FilePlanned, now, taskID, file.FileID, FileDiscovered)
				if err != nil {
					return nil, fmt.Errorf("mark file %q planned: %w", file.FileID, err)
				}
				changed, err := result.RowsAffected()
				if err != nil || changed != 1 {
					return nil, fmt.Errorf("file %q changed while planning", file.FileID)
				}
				files[ordinal].Status = FilePlanned
			}
			planned = append(planned, Batch{
				TaskID:           taskID,
				BatchID:          batchID,
				LogicalParent:    parent,
				BaiduStagingPath: remotePath,
				Status:           BatchPending,
				FileCount:        len(files),
				TotalBytes:       totalBytes,
				Files:            files,
			})
			chunkStart = chunkEnd
		}
		parentStart = parentEnd
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bounded batch plan: %w", err)
	}
	return planned, nil
}
