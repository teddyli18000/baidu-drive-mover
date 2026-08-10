package state

import (
	"context"
	"fmt"
	"time"
)

// ListResumableTasks returns unfinished tasks newest-first. Failed and
// completed tasks require deliberate inspection instead of automatic replay.
func (s *Store) ListResumableTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, share_url, extraction_code, status, scan_completed, drive_root_id, drive_root_name, last_error, created_at, updated_at
FROM tasks
WHERE status IN (?, ?, ?, ?, ?, ?)
ORDER BY updated_at DESC, id DESC`,
		TaskNew, TaskAuthRequired, TaskScanning, TaskRunning, TaskPaused, TaskBlocked)
	if err != nil {
		return nil, fmt.Errorf("list resumable tasks: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var task Task
		var scanCompleted int
		var created, updated string
		if err := rows.Scan(
			&task.ID, &task.ShareURL, &task.ExtractionCode, &task.Status, &scanCompleted,
			&task.DriveRootID, &task.DriveRootName, &task.LastError, &created, &updated,
		); err != nil {
			return nil, fmt.Errorf("scan resumable task: %w", err)
		}
		var err error
		task.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse resumable task created_at: %w", err)
		}
		task.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, fmt.Errorf("parse resumable task updated_at: %w", err)
		}
		task.ScanCompleted = scanCompleted == 1
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resumable tasks: %w", err)
	}
	return tasks, nil
}

// HasNonCompletedTasks reports whether any task still needs recovery,
// inspection, or migration. Runtime-wide cleanup is allowed only when this is
// false, because profiles, credentials, logs, tools, and the database are
// shared by every task in the executable folder.
func (s *Store) HasNonCompletedTasks(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE status != ?`, TaskCompleted).Scan(&count); err != nil {
		return false, fmt.Errorf("count non-completed tasks: %w", err)
	}
	return count > 0, nil
}

// CompleteTaskScan atomically records that the full share manifest was
// observed. A partial manifest must never be handed to the transfer pipeline.
func (s *Store) CompleteTaskScan(ctx context.Context, taskID string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE tasks
SET scan_completed = 1, status = ?, last_error = '', updated_at = ?
WHERE id = ? AND scan_completed = 0 AND status = ?`,
		TaskPaused, time.Now().UTC().Format(time.RFC3339Nano), taskID, TaskScanning)
	if err != nil {
		return fmt.Errorf("complete task scan: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read completed task scan result: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("complete task scan affected %d tasks", affected)
	}
	return nil
}
