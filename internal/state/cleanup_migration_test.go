package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMigrationV3ToV4NeverAuthorizesCleanup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state-v3.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationV1(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := migrationV2(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := migrationV3(ctx, tx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(1, ?), (2, ?), (3, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`
INSERT INTO tasks(id, share_url, extraction_code, status, drive_root_id, drive_root_name, last_error, created_at, updated_at)
VALUES('task-v3', 'https://pan.baidu.com/s/1Synthetic', '', ?, 'drive-root-v3', 'BaiduDriveMover-task-v3', '', ?, ?)`, TaskPaused, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, created_at)
VALUES
('task-v3', 'baidu_task_root', 'task-v3', '/BaiduDriveMover/task-v3', 0, ?),
('task-v3', 'baidu_batch_dir', 'b-v3', '/BaiduDriveMover/task-v3/b-v3', 0, ?),
('task-v3', 'drive_task_root', 'drive-root-v3', 'BaiduDriveMover-task-v3', 0, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("schema=%d want=4", version)
	}
	task, err := store.GetTask(ctx, "task-v3")
	if err != nil {
		t.Fatal(err)
	}
	if task.DriveRootID != "drive-root-v3" || task.DriveRootName != "BaiduDriveMover-task-v3" {
		t.Fatalf("Drive root identity changed during migration: %+v", task)
	}

	rows, err := store.db.QueryContext(ctx, `
SELECT scope, cleanup_allowed, cleaned_at, last_error
FROM owned_objects WHERE task_id = 'task-v3' ORDER BY scope`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var scope, cleanedAt, lastError string
		var allowed int
		if err := rows.Scan(&scope, &allowed, &cleanedAt, &lastError); err != nil {
			t.Fatal(err)
		}
		if allowed != 0 || cleanedAt != "" || lastError != "" {
			t.Fatalf("migration authorized or completed cleanup for %s: allowed=%d cleaned_at=%q error=%q", scope, allowed, cleanedAt, lastError)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("owned object count=%d want=3", count)
	}
}
