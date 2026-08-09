package state

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
	"time"
)

const (
	ownedScopeLocalCacheFile = "local_cache_file"
	ownedScopeBaiduBatchDir   = "baidu_batch_dir"
	ownedScopeBaiduTaskRoot   = "baidu_task_root"
)

type CleanupObject struct {
	TaskID         string
	Scope          string
	ObjectID       string
	ObjectPath     string
	CleanupAllowed bool
	CleanedAt      string
	LastError      string
}

type CleanupBatch struct {
	TaskID           string
	BatchID          string
	BaiduStagingPath string
	Files            []File
	LocalObjects     []CleanupObject
	BaiduObject      CleanupObject
}

func expectedLocalCachePath(taskID, fileID string) (string, error) {
	if !stagingComponentPattern.MatchString(taskID) || !stagingComponentPattern.MatchString(fileID) {
		return "", fmt.Errorf("unsafe local cache identity task=%q file=%q", taskID, fileID)
	}
	return path.Join("cache", taskID, fileID+".bin"), nil
}

func expectedBaiduBatchPath(taskID, batchID string) (string, error) {
	if !stagingComponentPattern.MatchString(taskID) || !stagingComponentPattern.MatchString(batchID) {
		return "", fmt.Errorf("unsafe Baidu cleanup identity task=%q batch=%q", taskID, batchID)
	}
	return path.Join(baiduStagingRoot, taskID, batchID), nil
}

// CleanupCandidates returns batches whose files are all verified or already in
// the durable cleanup-recovery state. It does not authorize deletion.
func (s *Store) CleanupCandidates(ctx context.Context, taskID string, limit int) ([]Batch, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT b.batch_id, b.logical_parent, b.baidu_staging_path, b.status, b.file_count, b.total_bytes,
       b.retry_count, b.last_error
FROM batches b
WHERE b.task_id = ?
  AND NOT EXISTS (
    SELECT 1
    FROM batch_files bf
    JOIN files f ON f.task_id = bf.task_id AND f.file_id = bf.file_id
    WHERE bf.task_id = b.task_id AND bf.batch_id = b.batch_id
      AND f.status NOT IN (?, ?)
  )
  AND EXISTS (
    SELECT 1 FROM batch_files bf
    WHERE bf.task_id = b.task_id AND bf.batch_id = b.batch_id
  )
ORDER BY b.batch_id
LIMIT ?`, taskID, FileDriveVerified, FileCleanupPending, limit)
	if err != nil {
		return nil, fmt.Errorf("query cleanup candidates: %w", err)
	}
	defer rows.Close()
	var batches []Batch
	for rows.Next() {
		var batch Batch
		batch.TaskID = taskID
		if err := rows.Scan(&batch.BatchID, &batch.LogicalParent, &batch.BaiduStagingPath, &batch.Status,
			&batch.FileCount, &batch.TotalBytes, &batch.RetryCount, &batch.LastError); err != nil {
			return nil, fmt.Errorf("scan cleanup candidate: %w", err)
		}
		batches = append(batches, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cleanup candidates: %w", err)
	}
	return batches, nil
}

// AuthorizeBatchCleanup is the destructive-action gate. It verifies all
// provenance and file states and commits cleanup_allowed before any caller may
// delete a local or Baidu object.
func (s *Store) AuthorizeBatchCleanup(ctx context.Context, taskID, batchID string) (CleanupBatch, error) {
	expectedBatchPath, err := expectedBaiduBatchPath(taskID, batchID)
	if err != nil {
		return CleanupBatch{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CleanupBatch{}, fmt.Errorf("begin cleanup authorization: %w", err)
	}
	defer tx.Rollback()

	var batch CleanupBatch
	batch.TaskID = taskID
	batch.BatchID = batchID
	if err := tx.QueryRowContext(ctx, `
SELECT baidu_staging_path FROM batches WHERE task_id = ? AND batch_id = ?`, taskID, batchID).Scan(&batch.BaiduStagingPath); err != nil {
		return CleanupBatch{}, fmt.Errorf("read cleanup batch: %w", err)
	}
	if batch.BaiduStagingPath != expectedBatchPath {
		return CleanupBatch{}, fmt.Errorf("batch %q staging path %q does not match registered path %q", batchID, batch.BaiduStagingPath, expectedBatchPath)
	}

	fileRows, err := tx.QueryContext(ctx, `
SELECT f.task_id, f.file_id, f.logical_path, f.parent_path, f.name, f.size, f.md5, f.status,
       f.baidu_staging_path, f.local_cache_path, f.drive_id, f.retry_count, f.last_error
FROM batch_files bf
JOIN files f ON f.task_id = bf.task_id AND f.file_id = bf.file_id
WHERE bf.task_id = ? AND bf.batch_id = ?
ORDER BY bf.ordinal`, taskID, batchID)
	if err != nil {
		return CleanupBatch{}, fmt.Errorf("query cleanup batch files: %w", err)
	}
	for fileRows.Next() {
		var file File
		if err := fileRows.Scan(&file.TaskID, &file.FileID, &file.LogicalPath, &file.ParentPath, &file.Name,
			&file.Size, &file.MD5, &file.Status, &file.BaiduStagingPath, &file.LocalCachePath, &file.DriveID,
			&file.RetryCount, &file.LastError); err != nil {
			fileRows.Close()
			return CleanupBatch{}, fmt.Errorf("scan cleanup batch file: %w", err)
		}
		if file.Status != FileDriveVerified && file.Status != FileCleanupPending {
			fileRows.Close()
			return CleanupBatch{}, fmt.Errorf("file %q is not cleanup eligible from %s", file.FileID, file.Status)
		}
		if strings.TrimSpace(file.DriveID) == "" {
			fileRows.Close()
			return CleanupBatch{}, fmt.Errorf("file %q has no persisted Drive ID", file.FileID)
		}
		expectedLocal, err := expectedLocalCachePath(taskID, file.FileID)
		if err != nil {
			fileRows.Close()
			return CleanupBatch{}, err
		}
		if file.LocalCachePath != expectedLocal {
			fileRows.Close()
			return CleanupBatch{}, fmt.Errorf("file %q local cache path %q does not match %q", file.FileID, file.LocalCachePath, expectedLocal)
		}
		batch.Files = append(batch.Files, file)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return CleanupBatch{}, fmt.Errorf("iterate cleanup batch files: %w", err)
	}
	if err := fileRows.Close(); err != nil {
		return CleanupBatch{}, fmt.Errorf("close cleanup batch rows: %w", err)
	}
	if len(batch.Files) == 0 {
		return CleanupBatch{}, fmt.Errorf("batch %q has no files", batchID)
	}

	baiduObject, err := readOwnedObjectTx(ctx, tx, taskID, ownedScopeBaiduBatchDir, batchID)
	if err != nil {
		return CleanupBatch{}, fmt.Errorf("read Baidu batch provenance: %w", err)
	}
	if baiduObject.ObjectPath != expectedBatchPath {
		return CleanupBatch{}, fmt.Errorf("Baidu batch provenance path %q does not match %q", baiduObject.ObjectPath, expectedBatchPath)
	}
	batch.BaiduObject = baiduObject

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, file := range batch.Files {
		expectedLocal, _ := expectedLocalCachePath(taskID, file.FileID)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, cleaned_at, last_error, created_at)
VALUES(?, ?, ?, ?, 0, '', '', ?)
ON CONFLICT(task_id, scope, object_id) DO NOTHING`, taskID, ownedScopeLocalCacheFile, file.FileID, expectedLocal, now); err != nil {
			return CleanupBatch{}, fmt.Errorf("backfill local cache provenance for %q: %w", file.FileID, err)
		}
		localObject, err := readOwnedObjectTx(ctx, tx, taskID, ownedScopeLocalCacheFile, file.FileID)
		if err != nil {
			return CleanupBatch{}, fmt.Errorf("read local cache provenance for %q: %w", file.FileID, err)
		}
		if localObject.ObjectPath != expectedLocal {
			return CleanupBatch{}, fmt.Errorf("local cache provenance for %q points to %q, want %q", file.FileID, localObject.ObjectPath, expectedLocal)
		}
		batch.LocalObjects = append(batch.LocalObjects, localObject)
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE files SET status = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id IN (
    SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?
) AND status = ?`, FileCleanupPending, now, taskID, taskID, batchID, FileDriveVerified); err != nil {
		return CleanupBatch{}, fmt.Errorf("mark batch cleanup pending: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE owned_objects SET cleanup_allowed = 1, last_error = ''
WHERE task_id = ? AND scope = ? AND object_id = ?`, taskID, ownedScopeBaiduBatchDir, batchID); err != nil {
		return CleanupBatch{}, fmt.Errorf("authorize Baidu batch cleanup: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE owned_objects SET cleanup_allowed = 1, last_error = ''
WHERE task_id = ? AND scope = ? AND object_id IN (
    SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?
)`, taskID, ownedScopeLocalCacheFile, taskID, batchID); err != nil {
		return CleanupBatch{}, fmt.Errorf("authorize local cache cleanup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CleanupBatch{}, fmt.Errorf("commit cleanup authorization: %w", err)
	}
	return s.GetAuthorizedCleanupBatch(ctx, taskID, batchID)
}

func (s *Store) GetAuthorizedCleanupBatch(ctx context.Context, taskID, batchID string) (CleanupBatch, error) {
	expectedBatchPath, err := expectedBaiduBatchPath(taskID, batchID)
	if err != nil {
		return CleanupBatch{}, err
	}
	var batch CleanupBatch
	batch.TaskID = taskID
	batch.BatchID = batchID
	if err := s.db.QueryRowContext(ctx, `SELECT baidu_staging_path FROM batches WHERE task_id = ? AND batch_id = ?`, taskID, batchID).Scan(&batch.BaiduStagingPath); err != nil {
		return CleanupBatch{}, err
	}
	if batch.BaiduStagingPath != expectedBatchPath {
		return CleanupBatch{}, fmt.Errorf("authorized batch path changed")
	}
	files, err := s.batchFiles(ctx, taskID, batchID)
	if err != nil {
		return CleanupBatch{}, err
	}
	for _, file := range files {
		if file.Status != FileCleanupPending {
			return CleanupBatch{}, fmt.Errorf("file %q is not cleanup pending", file.FileID)
		}
		if strings.TrimSpace(file.DriveID) == "" {
			return CleanupBatch{}, fmt.Errorf("file %q lost Drive identity during cleanup", file.FileID)
		}
		batch.Files = append(batch.Files, file)
		object, err := s.readOwnedObject(ctx, taskID, ownedScopeLocalCacheFile, file.FileID)
		if err != nil {
			return CleanupBatch{}, err
		}
		expectedLocal, _ := expectedLocalCachePath(taskID, file.FileID)
		if !object.CleanupAllowed || object.ObjectPath != expectedLocal {
			return CleanupBatch{}, fmt.Errorf("local cleanup provenance for %q is not authorized", file.FileID)
		}
		batch.LocalObjects = append(batch.LocalObjects, object)
	}
	object, err := s.readOwnedObject(ctx, taskID, ownedScopeBaiduBatchDir, batchID)
	if err != nil {
		return CleanupBatch{}, err
	}
	if !object.CleanupAllowed || object.ObjectPath != expectedBatchPath {
		return CleanupBatch{}, fmt.Errorf("Baidu batch cleanup is not authorized")
	}
	batch.BaiduObject = object
	return batch, nil
}

func (s *Store) MarkCleanupObjectDone(ctx context.Context, taskID, scope, objectID string) error {
	if scope != ownedScopeLocalCacheFile && scope != ownedScopeBaiduBatchDir && scope != ownedScopeBaiduTaskRoot {
		return fmt.Errorf("cleanup scope %q is not deletable", scope)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE owned_objects
SET cleaned_at = CASE WHEN cleaned_at = '' THEN ? ELSE cleaned_at END, last_error = ''
WHERE task_id = ? AND scope = ? AND object_id = ? AND cleanup_allowed = 1`,
		time.Now().UTC().Format(time.RFC3339Nano), taskID, scope, objectID)
	if err != nil {
		return fmt.Errorf("mark cleanup object done: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("cleanup object %s/%s is not authorized", scope, objectID)
	}
	return nil
}

func (s *Store) RecordCleanupObjectFailure(ctx context.Context, taskID, scope, objectID, message string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE owned_objects SET last_error = ?
WHERE task_id = ? AND scope = ? AND object_id = ? AND cleanup_allowed = 1 AND cleaned_at = ''`,
		truncateStateError(message), taskID, scope, objectID)
	if err != nil {
		return fmt.Errorf("record cleanup object failure: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("cleanup object %s/%s is not pending", scope, objectID)
	}
	return nil
}

func (s *Store) CompleteBatchCleanup(ctx context.Context, taskID, batchID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cleanup completion: %w", err)
	}
	defer tx.Rollback()
	var pending int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM batch_files bf
JOIN files f ON f.task_id = bf.task_id AND f.file_id = bf.file_id
WHERE bf.task_id = ? AND bf.batch_id = ? AND f.status != ?`, taskID, batchID, FileCleanupPending).Scan(&pending); err != nil {
		return fmt.Errorf("count cleanup-pending files: %w", err)
	}
	if pending != 0 {
		return fmt.Errorf("batch %q has %d files outside CLEANUP_PENDING", batchID, pending)
	}
	var incomplete int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM owned_objects o
WHERE o.task_id = ? AND (
    (o.scope = ? AND o.object_id = ?) OR
    (o.scope = ? AND o.object_id IN (
        SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?
    ))
) AND (o.cleanup_allowed != 1 OR o.cleaned_at = '')`,
		taskID, ownedScopeBaiduBatchDir, batchID, ownedScopeLocalCacheFile, taskID, batchID).Scan(&incomplete); err != nil {
		return fmt.Errorf("count incomplete cleanup objects: %w", err)
	}
	if incomplete != 0 {
		return fmt.Errorf("batch %q still has %d cleanup objects not reconciled", batchID, incomplete)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE files SET status = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id IN (
    SELECT file_id FROM batch_files WHERE task_id = ? AND batch_id = ?
) AND status = ?`, FileDone, time.Now().UTC().Format(time.RFC3339Nano), taskID, taskID, batchID, FileCleanupPending)
	if err != nil {
		return fmt.Errorf("mark cleaned files done: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("batch %q has no cleanup-pending files", batchID)
	}
	return tx.Commit()
}

func (s *Store) readOwnedObject(ctx context.Context, taskID, scope, objectID string) (CleanupObject, error) {
	return readOwnedObjectRow(s.db.QueryRowContext(ctx, `
SELECT task_id, scope, object_id, object_path, cleanup_allowed, cleaned_at, last_error
FROM owned_objects WHERE task_id = ? AND scope = ? AND object_id = ?`, taskID, scope, objectID))
}

func readOwnedObjectTx(ctx context.Context, tx *sql.Tx, taskID, scope, objectID string) (CleanupObject, error) {
	return readOwnedObjectRow(tx.QueryRowContext(ctx, `
SELECT task_id, scope, object_id, object_path, cleanup_allowed, cleaned_at, last_error
FROM owned_objects WHERE task_id = ? AND scope = ? AND object_id = ?`, taskID, scope, objectID))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func readOwnedObjectRow(row rowScanner) (CleanupObject, error) {
	var object CleanupObject
	var allowed int
	if err := row.Scan(&object.TaskID, &object.Scope, &object.ObjectID, &object.ObjectPath, &allowed, &object.CleanedAt, &object.LastError); err != nil {
		return CleanupObject{}, err
	}
	object.CleanupAllowed = allowed == 1
	return object, nil
}
