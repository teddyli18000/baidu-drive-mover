package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 6

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("state database path is empty")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite (%s): %w", pragma, err)
		}
	}
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("database schema %d is newer than supported %d", current, schemaVersion)
	}
	if current == schemaVersion {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()

	if current < 1 {
		if err := migrationV1(ctx, tx); err != nil {
			return err
		}
		if err := recordMigration(ctx, tx, 1); err != nil {
			return err
		}
		current = 1
	}
	if current < 2 {
		if err := migrationV2(ctx, tx); err != nil {
			return err
		}
		if err := recordMigration(ctx, tx, 2); err != nil {
			return err
		}
		current = 2
	}
	if current < 3 {
		if err := migrationV3(ctx, tx); err != nil {
			return err
		}
		if err := recordMigration(ctx, tx, 3); err != nil {
			return err
		}
		current = 3
	}
	if current < 4 {
		if err := migrationV4(ctx, tx); err != nil {
			return err
		}
		if err := recordMigration(ctx, tx, 4); err != nil {
			return err
		}
		current = 4
	}
	if current < 5 {
		if err := migrationV5(ctx, tx); err != nil {
			return err
		}
		if err := recordMigration(ctx, tx, 5); err != nil {
			return err
		}
		current = 5
	}
	if current < 6 {
		if err := migrationV6(ctx, tx); err != nil {
			return err
		}
		if err := recordMigration(ctx, tx, 6); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

func recordMigration(ctx context.Context, tx *sql.Tx, version int) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record schema migration %d: %w", version, err)
	}
	return nil
}

func migrationV1(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    share_url TEXT NOT NULL,
    extraction_code TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    drive_root_id TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);`,
		`CREATE TABLE directories (
    task_id TEXT NOT NULL,
    logical_path TEXT NOT NULL,
    drive_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(task_id, logical_path),
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
);`,
		`CREATE TABLE files (
    task_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    logical_path TEXT NOT NULL,
    parent_path TEXT NOT NULL,
    name TEXT NOT NULL,
    size INTEGER NOT NULL DEFAULT 0 CHECK(size >= 0),
    md5 TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    baidu_staging_path TEXT NOT NULL DEFAULT '',
    local_cache_path TEXT NOT NULL DEFAULT '',
    drive_id TEXT NOT NULL DEFAULT '',
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK(retry_count >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(task_id, file_id),
    UNIQUE(task_id, logical_path),
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
);`,
		`CREATE TABLE batches (
    task_id TEXT NOT NULL,
    batch_id TEXT NOT NULL,
    logical_parent TEXT NOT NULL,
    status TEXT NOT NULL,
    file_count INTEGER NOT NULL DEFAULT 0 CHECK(file_count >= 0),
    total_bytes INTEGER NOT NULL DEFAULT 0 CHECK(total_bytes >= 0),
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK(retry_count >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(task_id, batch_id),
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
);`,
		`CREATE TABLE owned_objects (
    task_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    object_id TEXT NOT NULL,
    object_path TEXT NOT NULL DEFAULT '',
    cleanup_allowed INTEGER NOT NULL DEFAULT 0 CHECK(cleanup_allowed IN (0,1)),
    created_at TEXT NOT NULL,
    PRIMARY KEY(task_id, scope, object_id),
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
);`,
		`CREATE INDEX idx_files_task_status ON files(task_id, status);`,
		`CREATE INDEX idx_batches_task_status ON batches(task_id, status);`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema v1: %w", err)
		}
	}
	return nil
}

func migrationV2(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`ALTER TABLE batches ADD COLUMN baidu_staging_path TEXT NOT NULL DEFAULT '';`,
		`CREATE TABLE batch_files (
    task_id TEXT NOT NULL,
    batch_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK(ordinal >= 0),
    PRIMARY KEY(task_id, batch_id, file_id),
    UNIQUE(task_id, file_id),
    FOREIGN KEY(task_id, batch_id) REFERENCES batches(task_id, batch_id) ON DELETE CASCADE,
    FOREIGN KEY(task_id, file_id) REFERENCES files(task_id, file_id) ON DELETE CASCADE
);`,
		`CREATE INDEX idx_batch_files_order ON batch_files(task_id, batch_id, ordinal);`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema v2: %w", err)
		}
	}
	return nil
}

func migrationV3(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`ALTER TABLE tasks ADD COLUMN drive_root_name TEXT NOT NULL DEFAULT '';`,
		`CREATE INDEX idx_directories_task_path ON directories(task_id, logical_path);`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema v3: %w", err)
		}
	}
	return nil
}

func migrationV4(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`ALTER TABLE owned_objects ADD COLUMN cleaned_at TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE owned_objects ADD COLUMN last_error TEXT NOT NULL DEFAULT '';`,
		`CREATE INDEX idx_owned_objects_cleanup ON owned_objects(task_id, scope, cleanup_allowed, cleaned_at);`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema v4: %w", err)
		}
	}
	return nil
}

func migrationV5(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`ALTER TABLE tasks ADD COLUMN scan_completed INTEGER NOT NULL DEFAULT 0 CHECK(scan_completed IN (0,1));`,
		`CREATE INDEX idx_tasks_resume ON tasks(status, updated_at DESC);`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema v5: %w", err)
		}
	}
	return nil
}

func migrationV6(ctx context.Context, tx *sql.Tx) error {
	invalidMD5 := `(TRIM(md5) != '' AND (LENGTH(TRIM(md5)) != 32 OR LOWER(TRIM(md5)) GLOB '*[^0-9a-f]*'))`
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
UPDATE tasks
SET status = 'PAUSED', last_error = '', updated_at = ?
WHERE status = 'BLOCKED'
  AND EXISTS (
    SELECT 1 FROM files
    WHERE files.task_id = tasks.id
      AND files.status = 'FAILED_PERMANENT'
      AND files.baidu_staging_path != ''
      AND LOWER(files.last_error) LIKE '%md5 mismatch%'
      AND `+invalidMD5+`
  )`, now); err != nil {
		return fmt.Errorf("recover tasks blocked by invalid provider MD5: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE files
SET status = 'BAIDU_STAGED', md5 = '', retry_count = 0, last_error = '', updated_at = ?
WHERE status = 'FAILED_PERMANENT'
  AND baidu_staging_path != ''
  AND LOWER(last_error) LIKE '%md5 mismatch%'
  AND `+invalidMD5, now); err != nil {
		return fmt.Errorf("recover files failed by invalid provider MD5: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE files SET md5 = '' WHERE `+invalidMD5); err != nil {
		return fmt.Errorf("clear invalid provider MD5 values: %w", err)
	}
	return nil
}

func (s *Store) CreateTask(ctx context.Context, task Task) error {
	if task.ID == "" || task.ShareURL == "" {
		return errors.New("task ID and share URL are required")
	}
	now := time.Now().UTC()
	if task.Status == "" {
		task.Status = TaskNew
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tasks(id, share_url, extraction_code, status, scan_completed, drive_root_id, drive_root_name, last_error, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, task.ID, task.ShareURL, task.ExtractionCode, task.Status, boolInt(task.ScanCompleted), task.DriveRootID, task.DriveRootName, task.LastError, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	return nil
}

func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	var task Task
	var scanCompleted int
	var created, updated string
	err := s.db.QueryRowContext(ctx, `
SELECT id, share_url, extraction_code, status, scan_completed, drive_root_id, drive_root_name, last_error, created_at, updated_at
FROM tasks WHERE id = ?`, id).Scan(&task.ID, &task.ShareURL, &task.ExtractionCode, &task.Status, &scanCompleted, &task.DriveRootID, &task.DriveRootName, &task.LastError, &created, &updated)
	if err != nil {
		return Task{}, err
	}
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Task{}, fmt.Errorf("parse task created_at: %w", err)
	}
	task.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Task{}, fmt.Errorf("parse task updated_at: %w", err)
	}
	task.ScanCompleted = scanCompleted == 1
	return task, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}
