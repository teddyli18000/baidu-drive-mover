package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

const baiduStagingRoot = "/BaiduDriveMover"

var stagingComponentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// PlanBatches atomically groups undispatched files by logical parent directory,
// chunks them below maxFiles, assigns deterministic batch IDs, and records the
// remote paths as tool-owned before any Baidu write can occur.
func (s *Store) PlanBatches(ctx context.Context, taskID string, maxFiles int) ([]Batch, error) {
	if maxFiles <= 0 || maxFiles >= 500 {
		return nil, fmt.Errorf("staging batch size must be between 1 and 499, got %d", maxFiles)
	}
	if !stagingComponentPattern.MatchString(taskID) {
		return nil, fmt.Errorf("unsafe task ID for Baidu staging: %q", taskID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin batch planning: %w", err)
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
	defer rows.Close()

	var discovered []File
	for rows.Next() {
		var file File
		file.TaskID = taskID
		if err := rows.Scan(&file.FileID, &file.LogicalPath, &file.ParentPath, &file.Name, &file.Size, &file.MD5, &file.Status); err != nil {
			return nil, fmt.Errorf("scan discovered file: %w", err)
		}
		discovered = append(discovered, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate discovered files: %w", err)
	}
	if len(discovered) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty batch plan: %w", err)
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
	for start := 0; start < len(discovered); {
		parent := discovered[start].ParentPath
		end := start
		for end < len(discovered) && discovered[end].ParentPath == parent {
			end++
		}
		parentFiles := discovered[start:end]
		for chunkStart := 0; chunkStart < len(parentFiles); chunkStart += maxFiles {
			chunkEnd := chunkStart + maxFiles
			if chunkEnd > len(parentFiles) {
				chunkEnd = len(parentFiles)
			}
			files := append([]File(nil), parentFiles[chunkStart:chunkEnd]...)
			batchID := deterministicBatchID(taskID, parent, files)
			remotePath := path.Join(taskRoot, batchID)
			var totalBytes int64
			for _, file := range files {
				totalBytes += file.Size
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO batches(task_id, batch_id, logical_parent, status, file_count, total_bytes, retry_count, last_error, created_at, updated_at, baidu_staging_path)
VALUES(?, ?, ?, ?, ?, ?, 0, '', ?, ?, ?)`,
				taskID, batchID, parent, BatchPending, len(files), totalBytes, now, now, remotePath); err != nil {
				return nil, fmt.Errorf("insert staging batch %q: %w", batchID, err)
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
					return nil, fmt.Errorf("add file %q to batch %q: %w", file.FileID, batchID, err)
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
		}
		start = end
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit batch plan: %w", err)
	}
	return planned, nil
}

func deterministicBatchID(taskID, parent string, files []File) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(taskID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(parent))
	for _, file := range files {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(file.FileID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(file.LogicalPath))
	}
	return "b-" + hex.EncodeToString(hash.Sum(nil))[:20]
}

func (s *Store) StagingBatches(ctx context.Context, taskID string) ([]Batch, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT batch_id, logical_parent, baidu_staging_path, status, file_count, total_bytes, retry_count, last_error
FROM batches
WHERE task_id = ? AND status IN (?, ?, ?)
ORDER BY batch_id`, taskID, BatchPending, BatchStaging, BatchFailedRetryable)
	if err != nil {
		return nil, fmt.Errorf("query staging batches: %w", err)
	}
	defer rows.Close()

	var batches []Batch
	for rows.Next() {
		var batch Batch
		batch.TaskID = taskID
		if err := rows.Scan(&batch.BatchID, &batch.LogicalParent, &batch.BaiduStagingPath, &batch.Status, &batch.FileCount, &batch.TotalBytes, &batch.RetryCount, &batch.LastError); err != nil {
			return nil, fmt.Errorf("scan staging batch: %w", err)
		}
		batches = append(batches, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate staging batches: %w", err)
	}
	for i := range batches {
		files, err := s.batchFiles(ctx, taskID, batches[i].BatchID)
		if err != nil {
			return nil, err
		}
		batches[i].Files = files
	}
	return batches, nil
}

func (s *Store) batchFiles(ctx context.Context, taskID, batchID string) ([]File, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT f.file_id, f.logical_path, f.parent_path, f.name, f.size, f.md5, f.status,
       f.baidu_staging_path, f.local_cache_path, f.drive_id, f.retry_count, f.last_error
FROM batch_files bf
JOIN files f ON f.task_id = bf.task_id AND f.file_id = bf.file_id
WHERE bf.task_id = ? AND bf.batch_id = ?
ORDER BY bf.ordinal`, taskID, batchID)
	if err != nil {
		return nil, fmt.Errorf("query batch files: %w", err)
	}
	defer rows.Close()
	var files []File
	for rows.Next() {
		var file File
		file.TaskID = taskID
		if err := rows.Scan(&file.FileID, &file.LogicalPath, &file.ParentPath, &file.Name, &file.Size, &file.MD5, &file.Status,
			&file.BaiduStagingPath, &file.LocalCachePath, &file.DriveID, &file.RetryCount, &file.LastError); err != nil {
			return nil, fmt.Errorf("scan batch file: %w", err)
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (s *Store) StartBatch(ctx context.Context, taskID, batchID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch start: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE batches SET status = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND batch_id = ? AND status IN (?, ?, ?)`,
		BatchStaging, now, taskID, batchID, BatchPending, BatchStaging, BatchFailedRetryable)
	if err != nil {
		return fmt.Errorf("start batch %q: %w", batchID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("batch %q is not available for staging", batchID)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE files SET status = ?, updated_at = ?
WHERE task_id = ? AND file_id IN (
    SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?
) AND status IN (?, ?)`, FileBaiduStaging, now, taskID, taskID, batchID, FilePlanned, FileFailedRetryable); err != nil {
		return fmt.Errorf("mark batch files staging: %w", err)
	}
	return tx.Commit()
}

func (s *Store) RecordStagedFiles(ctx context.Context, taskID, batchID string, staged map[string]string) error {
	if len(staged) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin staged-file update: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var batchPath string
	if err := tx.QueryRowContext(ctx, `
SELECT baidu_staging_path
FROM batches
WHERE task_id = ? AND batch_id = ?`, taskID, batchID).Scan(&batchPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("staging batch %q not found", batchID)
		}
		return fmt.Errorf("load staging batch %q path: %w", batchID, err)
	}
	validated := make(map[string]string, len(staged))
	for fileID, remotePath := range staged {
		var fileName string
		if err := tx.QueryRowContext(ctx, `
SELECT f.name
FROM batch_files bf
JOIN files f ON f.task_id = bf.task_id AND f.file_id = bf.file_id
WHERE bf.task_id = ? AND bf.batch_id = ? AND bf.file_id = ?`, taskID, batchID, fileID).Scan(&fileName); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("file %q is not part of staging batch %q", fileID, batchID)
			}
			return fmt.Errorf("load staged file %q: %w", fileID, err)
		}
		expectedPath := path.Join(batchPath, fileName)
		if remotePath != expectedPath {
			return fmt.Errorf("refusing Baidu staging path %q for file %q; expected %q", remotePath, fileID, expectedPath)
		}
		validated[fileID] = expectedPath
	}
	for fileID, remotePath := range validated {
		result, err := tx.ExecContext(ctx, `
UPDATE files SET status = ?, baidu_staging_path = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id = ?
  AND file_id IN (SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?)
  AND status IN (?, ?, ?)`,
			FileBaiduStaged, remotePath, now, taskID, fileID, taskID, batchID, FilePlanned, FileBaiduStaging, FileFailedRetryable)
		if err != nil {
			return fmt.Errorf("mark file %q staged: %w", fileID, err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			var status FileStatus
			var existingRemotePath string
			queryErr := tx.QueryRowContext(ctx, `
SELECT status, baidu_staging_path FROM files WHERE task_id = ? AND file_id = ?`, taskID, fileID).Scan(&status, &existingRemotePath)
			if queryErr != nil || !stagingConfirmationAlreadyConsumed(status) || existingRemotePath != remotePath {
				return fmt.Errorf("file %q is not eligible for staged confirmation", fileID)
			}
		}
	}
	return tx.Commit()
}

func stagingConfirmationAlreadyConsumed(status FileStatus) bool {
	switch status {
	case FileBaiduStaged,
		FileDownloading,
		FileLocalReady,
		FileDriveUploading,
		FileDriveUploaded,
		FileDriveVerified,
		FileCleanupPending,
		FileDone:
		return true
	default:
		return false
	}
}

func (s *Store) CompleteBatch(ctx context.Context, taskID, batchID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch completion: %w", err)
	}
	defer tx.Rollback()
	var remaining int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM batch_files bf
JOIN files f ON f.task_id = bf.task_id AND f.file_id = bf.file_id
WHERE bf.task_id = ? AND bf.batch_id = ?
  AND f.status NOT IN (?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, batchID,
		FileBaiduStaged, FileDownloading, FileLocalReady, FileDriveUploading,
		FileDriveUploaded, FileDriveVerified, FileCleanupPending, FileDone,
	).Scan(&remaining); err != nil {
		return fmt.Errorf("count unstaged batch files: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("batch %q still has %d unstaged files", batchID, remaining)
	}
	result, err := tx.ExecContext(ctx, `UPDATE batches SET status = ?, last_error = '', updated_at = ? WHERE task_id = ? AND batch_id = ?`,
		BatchStaged, time.Now().UTC().Format(time.RFC3339Nano), taskID, batchID)
	if err != nil {
		return fmt.Errorf("complete batch %q: %w", batchID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("batch %q not found", batchID)
	}
	return tx.Commit()
}

func (s *Store) FailBatch(ctx context.Context, taskID, batchID, message string, permanent bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch failure update: %w", err)
	}
	defer tx.Rollback()
	status := BatchFailedRetryable
	fileStatus := FileBaiduStaging
	if permanent {
		status = BatchFailedPermanent
		fileStatus = FileFailedPermanent
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
UPDATE batches SET status = ?, retry_count = retry_count + 1, last_error = ?, updated_at = ?
WHERE task_id = ? AND batch_id = ?`, status, truncateStateError(message), now, taskID, batchID); err != nil {
		return fmt.Errorf("record batch failure: %w", err)
	}
	if permanent {
		if _, err := tx.ExecContext(ctx, `
UPDATE files SET status = ?, last_error = ?, updated_at = ?
WHERE task_id = ? AND file_id IN (
    SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?
) AND status != ?`, fileStatus, truncateStateError(message), now, taskID, taskID, batchID, FileBaiduStaged); err != nil {
			return fmt.Errorf("record permanent file failure: %w", err)
		}
	}
	return tx.Commit()
}

func truncateStateError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

// rawDB exposes the sql handle only to same-package tests/helpers.
func (s *Store) rawDB() *sql.DB { return s.db }
