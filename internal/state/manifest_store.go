package state

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
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
		logicalPath, err := validateManifestPath(directory.LogicalPath, false)
		if err != nil {
			return fmt.Errorf("invalid manifest directory %q: %w", directory.LogicalPath, err)
		}
		var conflictingFileID string
		err = tx.QueryRowContext(ctx, `
SELECT file_id FROM files WHERE task_id = ? AND logical_path = ?`, taskID, logicalPath).Scan(&conflictingFileID)
		if err == nil {
			return fmt.Errorf("manifest logical path %q is already a file (%s)", logicalPath, conflictingFileID)
		}
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("check manifest directory collision %q: %w", logicalPath, err)
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO directories(task_id, logical_path, drive_id, created_at, updated_at)
VALUES(?, ?, '', ?, ?)
ON CONFLICT(task_id, logical_path) DO UPDATE SET updated_at = excluded.updated_at`, taskID, logicalPath, now, now)
		if err != nil {
			return fmt.Errorf("upsert manifest directory %q: %w", logicalPath, err)
		}
	}
	for _, file := range files {
		logicalPath, err := validateManifestPath(file.LogicalPath, false)
		if err != nil {
			return fmt.Errorf("invalid manifest file path %q: %w", file.LogicalPath, err)
		}
		parentPath, err := validateManifestPath(file.ParentPath, true)
		if err != nil {
			return fmt.Errorf("invalid manifest parent path %q: %w", file.ParentPath, err)
		}
		if file.SourceID == "" || file.Name == "" || file.Size < 0 {
			return fmt.Errorf("invalid manifest file at %q", file.LogicalPath)
		}
		if path.Dir(logicalPath) != parentPath || path.Base(logicalPath) != file.Name {
			return fmt.Errorf("manifest file metadata is inconsistent at %q", logicalPath)
		}
		var directoryMarker string
		err = tx.QueryRowContext(ctx, `
SELECT logical_path FROM directories WHERE task_id = ? AND logical_path = ?`, taskID, logicalPath).Scan(&directoryMarker)
		if err == nil {
			return fmt.Errorf("manifest logical path %q is already a directory", logicalPath)
		}
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("check manifest file collision %q: %w", logicalPath, err)
		}

		var existingPath, existingParent, existingName, existingMD5 string
		var existingSize int64
		err = tx.QueryRowContext(ctx, `
SELECT logical_path, parent_path, name, size, md5
FROM files WHERE task_id = ? AND file_id = ?`, taskID, file.SourceID).Scan(
			&existingPath, &existingParent, &existingName, &existingSize, &existingMD5,
		)
		if err == nil {
			if existingPath != logicalPath || existingParent != parentPath || existingName != file.Name || existingSize != file.Size {
				return fmt.Errorf("manifest source ID %q attempted to rebind from %q to %q", file.SourceID, existingPath, logicalPath)
			}
			incomingMD5 := strings.TrimSpace(file.MD5)
			if existingMD5 != "" && incomingMD5 != "" && !strings.EqualFold(existingMD5, incomingMD5) {
				return fmt.Errorf("manifest source ID %q changed MD5 for %q", file.SourceID, logicalPath)
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE files
SET md5 = CASE WHEN md5 = '' AND ? != '' THEN ? ELSE md5 END,
    updated_at = ?
WHERE task_id = ? AND file_id = ?`, incomingMD5, incomingMD5, now, taskID, file.SourceID); err != nil {
				return fmt.Errorf("refresh manifest file %q: %w", logicalPath, err)
			}
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("read existing manifest source ID %q: %w", file.SourceID, err)
		}

		_, err = tx.ExecContext(ctx, `
INSERT INTO files(
    task_id, file_id, logical_path, parent_path, name, size, md5, status,
    baidu_staging_path, local_cache_path, drive_id, retry_count, last_error, created_at, updated_at
)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, '', '', '', 0, '', ?, ?)`, taskID, file.SourceID, logicalPath, parentPath, file.Name, file.Size, strings.TrimSpace(file.MD5), FileDiscovered, now, now)
		if err != nil {
			return fmt.Errorf("insert manifest file %q: %w", logicalPath, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit manifest page: %w", err)
	}
	return nil
}

func validateManifestPath(raw string, allowRoot bool) (string, error) {
	if raw == "" || strings.ContainsRune(raw, '\x00') || !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("path must be an absolute non-empty POSIX path")
	}
	clean := path.Clean(raw)
	if clean != raw {
		return "", fmt.Errorf("path is not canonical")
	}
	if clean == "/" && !allowRoot {
		return "", fmt.Errorf("root is not a storable manifest object")
	}
	return clean, nil
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
