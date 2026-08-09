package state

import (
	"context"
	"fmt"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

func (s *Store) UpsertManifestPage(ctx context.Context, taskID string, directories []manifest.Directory, files []manifest.File) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin manifest transaction: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, directory := range directories {
		if directory.LogicalPath == "" {
			return fmt.Errorf("manifest directory path is empty")
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO directories(task_id, logical_path, drive_id, created_at, updated_at)
VALUES(?, ?, '', ?, ?)
ON CONFLICT(task_id, logical_path) DO UPDATE SET updated_at = excluded.updated_at`, taskID, directory.LogicalPath, now, now)
		if err != nil {
			return fmt.Errorf("upsert manifest directory %q: %w", directory.LogicalPath, err)
		}
	}
	for _, file := range files {
		if file.SourceID == "" || file.LogicalPath == "" || file.Name == "" || file.Size < 0 {
			return fmt.Errorf("invalid manifest file at %q", file.LogicalPath)
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO files(
    task_id, file_id, logical_path, parent_path, name, size, md5, status,
    baidu_staging_path, local_cache_path, drive_id, retry_count, last_error, created_at, updated_at
)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, '', '', '', 0, '', ?, ?)
ON CONFLICT(task_id, file_id) DO UPDATE SET
    logical_path = excluded.logical_path,
    parent_path = excluded.parent_path,
    name = excluded.name,
    size = excluded.size,
    md5 = excluded.md5,
    updated_at = excluded.updated_at`, taskID, file.SourceID, file.LogicalPath, file.ParentPath, file.Name, file.Size, file.MD5, FileDiscovered, now, now)
		if err != nil {
			return fmt.Errorf("upsert manifest file %q: %w", file.LogicalPath, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit manifest page: %w", err)
	}
	return nil
}

func (s *Store) ManifestStats(ctx context.Context, taskID string) (manifest.Stats, error) {
	var stats manifest.Stats
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM directories WHERE task_id = ?`, taskID).Scan(&stats.Directories); err != nil {
		return manifest.Stats{}, fmt.Errorf("count manifest directories: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM files WHERE task_id = ?`, taskID).Scan(&stats.Files, &stats.Bytes); err != nil {
		return manifest.Stats{}, fmt.Errorf("count manifest files: %w", err)
	}
	return stats, nil
}

func (s *Store) UpdateTaskStatus(ctx context.Context, taskID string, status TaskStatus, lastError string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`, status, lastError, time.Now().UTC().Format(time.RFC3339Nano), taskID)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read task status update result: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("task %q not found", taskID)
	}
	return nil
}

func (s *Store) UpdateTaskExtractionCode(ctx context.Context, taskID, code string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tasks SET extraction_code = ?, updated_at = ? WHERE id = ?`, code, time.Now().UTC().Format(time.RFC3339Nano), taskID)
	if err != nil {
		return fmt.Errorf("update task extraction code: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("task %q not found", taskID)
	}
	return nil
}
