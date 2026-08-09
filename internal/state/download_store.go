package state

import (
	"context"
	"fmt"
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE files
SET status = ?, local_cache_path = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id = ? AND status IN (?, ?)`,
		FileLocalReady, cachePath, now, taskID, fileID, FileDownloading, FileLocalReady)
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
	err := s.db.QueryRowContext(ctx, `
SELECT task_id, file_id, logical_path, parent_path, name, size, md5, status,
       baidu_staging_path, local_cache_path, drive_id, retry_count, last_error,
       created_at, updated_at
FROM files WHERE task_id = ? AND file_id = ?`, taskID, fileID).Scan(
		&file.TaskID, &file.FileID, &file.LogicalPath, &file.ParentPath, &file.Name, &file.Size, &file.MD5,
		&file.Status, &file.BaiduStagingPath, &file.LocalCachePath, &file.DriveID, &file.RetryCount, &file.LastError,
		&file.CreatedAt, &file.UpdatedAt,
	)
	if err != nil {
		return File{}, fmt.Errorf("get file %q: %w", fileID, err)
	}
	return file, nil
}
