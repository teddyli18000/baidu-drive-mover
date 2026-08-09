package state

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *Store) SetTaskDriveRoot(ctx context.Context, taskID, rootName, rootID string) error {
	rootName = strings.TrimSpace(rootName)
	rootID = strings.TrimSpace(rootID)
	if taskID == "" || rootName == "" || rootID == "" {
		return fmt.Errorf("task ID, Drive root name and Drive root ID are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Drive root update: %w", err)
	}
	defer tx.Rollback()

	var existingID, existingName string
	if err := tx.QueryRowContext(ctx, `SELECT drive_root_id, drive_root_name FROM tasks WHERE id = ?`, taskID).Scan(&existingID, &existingName); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("task %q not found", taskID)
		}
		return fmt.Errorf("read Drive root state: %w", err)
	}
	if existingID != "" && existingID != rootID {
		return fmt.Errorf("refusing to replace Drive root ID %q with %q for task %q", existingID, rootID, taskID)
	}
	if existingName != "" && existingName != rootName {
		return fmt.Errorf("refusing to replace Drive root name %q with %q for task %q", existingName, rootName, taskID)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
UPDATE tasks SET drive_root_id = ?, drive_root_name = ?, updated_at = ? WHERE id = ?`,
		rootID, rootName, now, taskID); err != nil {
		return fmt.Errorf("persist Drive root: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, created_at)
VALUES(?, 'drive_task_root', ?, ?, 0, ?)
ON CONFLICT(task_id, scope, object_id) DO UPDATE SET object_path = excluded.object_path`,
		taskID, rootID, rootName, now); err != nil {
		return fmt.Errorf("register Drive root provenance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Drive root update: %w", err)
	}
	return nil
}

func (s *Store) DriveDirectories(ctx context.Context, taskID string) ([]Directory, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT task_id, logical_path, drive_id, created_at, updated_at
FROM directories WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query Drive directories: %w", err)
	}
	defer rows.Close()

	var result []Directory
	for rows.Next() {
		var directory Directory
		var created, updated string
		if err := rows.Scan(&directory.TaskID, &directory.LogicalPath, &directory.DriveID, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan Drive directory: %w", err)
		}
		var err error
		directory.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse directory %q created_at: %w", directory.LogicalPath, err)
		}
		directory.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, fmt.Errorf("parse directory %q updated_at: %w", directory.LogicalPath, err)
		}
		result = append(result, directory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Drive directories: %w", err)
	}
	sort.Slice(result, func(i, j int) bool {
		di := logicalDepth(result[i].LogicalPath)
		dj := logicalDepth(result[j].LogicalPath)
		if di != dj {
			return di < dj
		}
		return result[i].LogicalPath < result[j].LogicalPath
	})
	return result, nil
}

func logicalDepth(value string) int {
	value = strings.Trim(value, "/")
	if value == "" {
		return 0
	}
	return strings.Count(value, "/") + 1
}

func (s *Store) RecordDirectoryDriveID(ctx context.Context, taskID, logicalPath, driveID string) error {
	driveID = strings.TrimSpace(driveID)
	if taskID == "" || logicalPath == "" || driveID == "" {
		return fmt.Errorf("task ID, logical directory path and Drive ID are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin directory Drive update: %w", err)
	}
	defer tx.Rollback()

	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT drive_id FROM directories WHERE task_id = ? AND logical_path = ?`, taskID, logicalPath).Scan(&existing); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("directory %q not found for task %q", logicalPath, taskID)
		}
		return fmt.Errorf("read directory Drive ID: %w", err)
	}
	if existing != "" && existing != driveID {
		return fmt.Errorf("refusing to replace Drive directory ID %q with %q for %q", existing, driveID, logicalPath)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
UPDATE directories SET drive_id = ?, updated_at = ? WHERE task_id = ? AND logical_path = ?`,
		driveID, now, taskID, logicalPath); err != nil {
		return fmt.Errorf("persist directory Drive ID: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, created_at)
VALUES(?, 'drive_directory', ?, ?, 0, ?)
ON CONFLICT(task_id, scope, object_id) DO UPDATE SET object_path = excluded.object_path`,
		taskID, driveID, logicalPath, now); err != nil {
		return fmt.Errorf("register Drive directory provenance: %w", err)
	}
	return tx.Commit()
}

func (s *Store) DriveUploadCandidates(ctx context.Context, taskID string, limit int) ([]File, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT task_id, file_id, logical_path, parent_path, name, size, md5, status,
       baidu_staging_path, local_cache_path, drive_id, retry_count, last_error
FROM files
WHERE task_id = ? AND status IN (?, ?, ?)
ORDER BY logical_path
LIMIT ?`, taskID, FileLocalReady, FileDriveUploading, FileDriveUploaded, limit)
	if err != nil {
		return nil, fmt.Errorf("query Drive upload candidates: %w", err)
	}
	defer rows.Close()
	var files []File
	for rows.Next() {
		var file File
		if err := rows.Scan(
			&file.TaskID, &file.FileID, &file.LogicalPath, &file.ParentPath, &file.Name, &file.Size, &file.MD5,
			&file.Status, &file.BaiduStagingPath, &file.LocalCachePath, &file.DriveID, &file.RetryCount, &file.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan Drive upload candidate: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Drive upload candidates: %w", err)
	}
	return files, nil
}

func (s *Store) StartDriveUpload(ctx context.Context, taskID, fileID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE files SET status = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id = ? AND status IN (?, ?)`,
		FileDriveUploading, now, taskID, fileID, FileLocalReady, FileDriveUploading)
	if err != nil {
		return fmt.Errorf("start Drive upload %q: %w", fileID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("file %q is not eligible for Drive upload", fileID)
	}
	return nil
}

// RecordDriveUploaded records an independently observed remote object ID after
// either an upload process or crash-reconciliation. LOCAL_READY is accepted so
// a remote object committed before a previous local state update can be adopted
// without uploading a duplicate.
func (s *Store) RecordDriveUploaded(ctx context.Context, taskID, fileID, driveID string) error {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return fmt.Errorf("Drive file ID is empty")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Drive uploaded update: %w", err)
	}
	defer tx.Rollback()

	var status FileStatus
	var existingID, logicalPath string
	if err := tx.QueryRowContext(ctx, `SELECT status, drive_id, logical_path FROM files WHERE task_id = ? AND file_id = ?`, taskID, fileID).Scan(&status, &existingID, &logicalPath); err != nil {
		return fmt.Errorf("read Drive upload state: %w", err)
	}
	if status != FileLocalReady && status != FileDriveUploading && status != FileDriveUploaded {
		return fmt.Errorf("file %q is not eligible for Drive uploaded state from %s", fileID, status)
	}
	if existingID != "" && existingID != driveID {
		return fmt.Errorf("refusing to replace Drive file ID %q with %q for %q", existingID, driveID, logicalPath)
	}
	if err := ValidateFileTransition(status, FileDriveUploaded); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
UPDATE files SET status = ?, drive_id = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id = ?`, FileDriveUploaded, driveID, now, taskID, fileID); err != nil {
		return fmt.Errorf("persist Drive uploaded state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, created_at)
VALUES(?, 'drive_file', ?, ?, 0, ?)
ON CONFLICT(task_id, scope, object_id) DO UPDATE SET object_path = excluded.object_path`,
		taskID, driveID, logicalPath, now); err != nil {
		return fmt.Errorf("register Drive file provenance: %w", err)
	}
	return tx.Commit()
}

func (s *Store) MarkDriveVerified(ctx context.Context, taskID, fileID, driveID string) error {
	driveID = strings.TrimSpace(driveID)
	if driveID == "" {
		return fmt.Errorf("verified Drive file ID is empty")
	}
	var status FileStatus
	var existingID string
	if err := s.db.QueryRowContext(ctx, `SELECT status, drive_id FROM files WHERE task_id = ? AND file_id = ?`, taskID, fileID).Scan(&status, &existingID); err != nil {
		return fmt.Errorf("read Drive verification state: %w", err)
	}
	if existingID == "" || existingID != driveID {
		return fmt.Errorf("Drive verification ID %q does not match persisted ID %q for file %q", driveID, existingID, fileID)
	}
	if status != FileDriveUploaded && status != FileDriveVerified {
		return fmt.Errorf("file %q is not eligible for Drive verification from %s", fileID, status)
	}
	if err := ValidateFileTransition(status, FileDriveVerified); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE files SET status = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND file_id = ? AND drive_id = ?`,
		FileDriveVerified, time.Now().UTC().Format(time.RFC3339Nano), taskID, fileID, driveID)
	if err != nil {
		return fmt.Errorf("mark Drive file verified: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("file %q Drive verification state changed concurrently", fileID)
	}
	return nil
}

func (s *Store) RecordDriveFailure(ctx context.Context, taskID, fileID, message string, permanent bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Drive failure update: %w", err)
	}
	defer tx.Rollback()

	var current FileStatus
	if err := tx.QueryRowContext(ctx, `SELECT status FROM files WHERE task_id = ? AND file_id = ?`, taskID, fileID).Scan(&current); err != nil {
		return fmt.Errorf("read Drive failure state: %w", err)
	}
	if current != FileLocalReady && current != FileDriveUploading && current != FileDriveUploaded {
		return fmt.Errorf("file %q is not in a Drive-recoverable state: %s", fileID, current)
	}
	next := current
	if permanent {
		next = FileFailedPermanent
		if err := ValidateFileTransition(current, next); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE files SET status = ?, retry_count = retry_count + 1, last_error = ?, updated_at = ?
WHERE task_id = ? AND file_id = ?`,
		next, truncateStateError(message), time.Now().UTC().Format(time.RFC3339Nano), taskID, fileID); err != nil {
		return fmt.Errorf("record Drive failure: %w", err)
	}
	return tx.Commit()
}
