package state

import (
	"context"
	"fmt"
	"path"
	"time"
)

func (s *Store) DownloadCandidates(ctx context.Context, taskID string, limit int) ([]File, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT task_id, file_id, logical_path, parent_path, name, size, md5, status,
       baidu_staging_path, local_cache_path, drive_id, retry_count, last_error
FROM files
WHERE task_id = ? AND status IN (?, ?)
ORDER BY logical_path
LIMIT ?`, taskID, FileBaiduStaged, FileDownloading, limit)
	if err != nil {
		return nil, fmt.Errorf("query download candidates: %w", err)
	}
	defer rows.Close()
	var files []File
	for rows.Next() {
		var file File
		if err := rows.Scan(
			&file.TaskID, &file.FileID, &file.LogicalPath, &file.ParentPath, &file.Name, &file.Size, &file.MD5,
			&file.Status, &file.BaiduStagingPath, &file.LocalCachePath, &file.DriveID, &file.RetryCount, &file.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan download candidate: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate download candidates: %w", err)
	}
	return files, nil
}

func (s *Store) StartDownload(ctx context.Context, taskID, fileID, cachePath string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE files
SET status = ?, local_cache_path = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id = ? AND status IN (?, ?)`,
		FileDownloading, cachePath, now, taskID, fileID, FileBaiduStaged, FileDownloading)
	if err != nil {
		return fmt.Errorf("start download %q: %w", fileID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("file %q is not eligible for download", fileID)
	}
	return nil
}

func (s *Store) MarkLocalReady(ctx context.Context, taskID, fileID, cachePath string) error {
	if !stagingComponentPattern.MatchString(taskID) || !stagingComponentPattern.MatchString(fileID) {
		return fmt.Errorf("unsafe local cache identity task=%q file=%q", taskID, fileID)
	}
	expected := path.Join("cache", taskID, fileID+".bin")
	if cachePath != expected {
		return fmt.Errorf("local-ready cache path %q does not match registered opaque path %q", cachePath, expected)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin local-ready update: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE files
SET status = ?, local_cache_path = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id = ? AND status IN (?, ?, ?)`,
		FileLocalReady, expected, now, taskID, fileID, FileBaiduStaged, FileDownloading, FileLocalReady)
	if err != nil {
		return fmt.Errorf("mark local file ready %q: %w", fileID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("file %q is not eligible for local-ready state", fileID)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, created_at)
VALUES(?, 'local_cache_file', ?, ?, 0, ?)
ON CONFLICT(task_id, scope, object_id) DO NOTHING`, taskID, fileID, expected, now); err != nil {
		return fmt.Errorf("register local cache provenance for %q: %w", fileID, err)
	}
	var registered string
	var cleanupAllowed int
	var cleanedAt string
	if err := tx.QueryRowContext(ctx, `
SELECT object_path, cleanup_allowed, cleaned_at
FROM owned_objects
WHERE task_id = ? AND scope = 'local_cache_file' AND object_id = ?`, taskID, fileID).Scan(&registered, &cleanupAllowed, &cleanedAt); err != nil {
		return fmt.Errorf("read local cache provenance for %q: %w", fileID, err)
	}
	if registered != expected || cleanupAllowed != 0 || cleanedAt != "" {
		return fmt.Errorf("local cache provenance for %q is inconsistent", fileID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit local-ready provenance for %q: %w", fileID, err)
	}
	return nil
}

func (s *Store) RecordDownloadFailure(ctx context.Context, taskID, fileID, message string, permanent bool) error {
	status := FileDownloading
	if permanent {
		status = FileFailedPermanent
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE files
SET status = ?, retry_count = retry_count + 1, last_error = ?, updated_at = ?
WHERE task_id = ? AND file_id = ? AND status IN (?, ?)`,
		status, truncateStateError(message), now, taskID, fileID, FileBaiduStaged, FileDownloading)
	if err != nil {
		return fmt.Errorf("record download failure %q: %w", fileID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("file %q is not eligible for download failure update", fileID)
	}
	return nil
}

// ReservedCacheBytes returns the full expected byte reservation for local
// objects that must remain in cache. Full-size reservation prevents concurrent
// stages from overcommitting disk while partial files are still small.
func (s *Store) ReservedCacheBytes(ctx context.Context) (int64, error) {
	var bytes int64
	if err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(size), 0)
FROM files
WHERE status IN (?, ?, ?, ?, ?, ?)`,
		FileDownloading, FileLocalReady, FileDriveUploading, FileDriveUploaded, FileDriveVerified, FileCleanupPending,
	).Scan(&bytes); err != nil {
		return 0, fmt.Errorf("calculate reserved cache bytes: %w", err)
	}
	return bytes, nil
}

func (s *Store) GetFile(ctx context.Context, taskID, fileID string) (File, error) {
	var file File
	var created, updated string
	err := s.db.QueryRowContext(ctx, `
SELECT task_id, file_id, logical_path, parent_path, name, size, md5, status,
       baidu_staging_path, local_cache_path, drive_id, retry_count, last_error,
       created_at, updated_at
FROM files WHERE task_id = ? AND file_id = ?`, taskID, fileID).Scan(
		&file.TaskID, &file.FileID, &file.LogicalPath, &file.ParentPath, &file.Name, &file.Size, &file.MD5,
		&file.Status, &file.BaiduStagingPath, &file.LocalCachePath, &file.DriveID, &file.RetryCount, &file.LastError,
		&created, &updated,
	)
	if err != nil {
		return File{}, fmt.Errorf("get file %q: %w", fileID, err)
	}
	file.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return File{}, fmt.Errorf("parse file %q created_at: %w", fileID, err)
	}
	file.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return File{}, fmt.Errorf("parse file %q updated_at: %w", fileID, err)
	}
	return file, nil
}
