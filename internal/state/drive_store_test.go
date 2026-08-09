package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	_ "modernc.org/sqlite"
)

func TestMigrationFromRealSchemaV2PreservesRecoveryState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state-v2.db")
	db, err := sql.Open("sqlite", path)
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(1, ?), (2, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`
INSERT INTO tasks(id, share_url, extraction_code, status, drive_root_id, last_error, created_at, updated_at)
VALUES('task-v2', 'https://pan.baidu.com/s/1Synthetic', 'abcd', ?, '', '', ?, ?)`, TaskPaused, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`
INSERT INTO directories(task_id, logical_path, drive_id, created_at, updated_at)
VALUES('task-v2', '/alpha', '', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`
INSERT INTO files(task_id, file_id, logical_path, parent_path, name, size, md5, status,
                  baidu_staging_path, local_cache_path, drive_id, retry_count, last_error, created_at, updated_at)
VALUES('task-v2', '101', '/alpha/a.bin', '/alpha', 'a.bin', 7, '0123456789abcdef0123456789abcdef', ?,
       '/BaiduDriveMover/task-v2/b-1/a.bin', 'cache/task-v2/101.bin', '', 2, 'old retry note', ?, ?)`, FileLocalReady, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`
INSERT INTO owned_objects(task_id, scope, object_id, object_path, cleanup_allowed, created_at)
VALUES('task-v2', 'baidu_batch_dir', 'b-1', '/BaiduDriveMover/task-v2/b-1', 0, ?)`, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
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
	task, err := store.GetTask(ctx, "task-v2")
	if err != nil {
		t.Fatal(err)
	}
	if task.DriveRootID != "" || task.DriveRootName != "" {
		t.Fatalf("unexpected migrated Drive root identity: id=%q name=%q", task.DriveRootID, task.DriveRootName)
	}
	file, err := store.GetFile(ctx, "task-v2", "101")
	if err != nil {
		t.Fatal(err)
	}
	if file.Status != FileLocalReady || file.LocalCachePath != "cache/task-v2/101.bin" || file.BaiduStagingPath == "" || file.RetryCount != 2 || file.LastError != "old retry note" {
		t.Fatalf("v2 file recovery state was not preserved: %+v", file)
	}
	var cleanupAllowed int
	var cleanedAt, cleanupError string
	if err := store.db.QueryRowContext(ctx, `
SELECT cleanup_allowed, cleaned_at, last_error FROM owned_objects
WHERE task_id = 'task-v2' AND scope = 'baidu_batch_dir' AND object_id = 'b-1'`).Scan(&cleanupAllowed, &cleanedAt, &cleanupError); err != nil {
		t.Fatal(err)
	}
	if cleanupAllowed != 0 || cleanedAt != "" || cleanupError != "" {
		t.Fatalf("migration changed cleanup provenance: allowed=%d cleaned_at=%q error=%q", cleanupAllowed, cleanedAt, cleanupError)
	}
}

func TestDriveRootIdentityIsWriteOnce(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	createStagingTestTask(t, store, "task-root")
	if err := store.SetTaskDriveRoot(ctx, "task-root", "BaiduDriveMover-task-root", "root-123"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskDriveRoot(ctx, "task-root", "BaiduDriveMover-task-root", "root-123"); err != nil {
		t.Fatalf("idempotent root write failed: %v", err)
	}
	if err := store.SetTaskDriveRoot(ctx, "task-root", "BaiduDriveMover-task-root", "root-other"); err == nil {
		t.Fatal("expected Drive root ID replacement rejection")
	}
	if err := store.SetTaskDriveRoot(ctx, "task-root", "renamed", "root-123"); err == nil {
		t.Fatal("expected Drive root name replacement rejection")
	}
	task, err := store.GetTask(ctx, "task-root")
	if err != nil {
		t.Fatal(err)
	}
	if task.DriveRootID != "root-123" || task.DriveRootName != "BaiduDriveMover-task-root" {
		t.Fatalf("unexpected Drive root state: %+v", task)
	}
}

func TestDriveDirectoriesAreParentFirstAndIDsAreImmutable(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	createStagingTestTask(t, store, "task-dirs")
	if err := store.UpsertManifestPage(ctx, "task-dirs", []manifest.Directory{
		{LogicalPath: "/a/b/c"},
		{LogicalPath: "/z"},
		{LogicalPath: "/a"},
		{LogicalPath: "/a/b"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	dirs, err := store.DriveDirectories(ctx, "task-dirs")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/a", "/z", "/a/b", "/a/b/c"}
	if len(dirs) != len(want) {
		t.Fatalf("directories=%d want=%d", len(dirs), len(want))
	}
	for i := range want {
		if dirs[i].LogicalPath != want[i] {
			t.Fatalf("directory[%d]=%q want=%q", i, dirs[i].LogicalPath, want[i])
		}
	}
	if err := store.RecordDirectoryDriveID(ctx, "task-dirs", "/a", "dir-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDirectoryDriveID(ctx, "task-dirs", "/a", "dir-a"); err != nil {
		t.Fatalf("idempotent directory ID write failed: %v", err)
	}
	if err := store.RecordDirectoryDriveID(ctx, "task-dirs", "/a", "dir-other"); err == nil {
		t.Fatal("expected directory Drive ID replacement rejection")
	}
}

func TestDriveFileRecoveryAdoptsCommittedObjectWithoutDuplicateState(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	createStagingTestTask(t, store, "task-file")
	if err := store.UpsertManifestPage(ctx, "task-file", nil, []manifest.File{{
		SourceID: "201", LogicalPath: "/docs/a.txt", ParentPath: "/docs", Name: "a.txt", Size: 5,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE files SET status = ?, local_cache_path = 'cache/task-file/201.bin'
WHERE task_id = 'task-file' AND file_id = '201'`, FileLocalReady); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordDriveUploaded(ctx, "task-file", "201", "drive-file-201"); err != nil {
		t.Fatal(err)
	}
	file, err := store.GetFile(ctx, "task-file", "201")
	if err != nil {
		t.Fatal(err)
	}
	if file.Status != FileDriveUploaded || file.DriveID != "drive-file-201" {
		t.Fatalf("unexpected adopted Drive state: %+v", file)
	}
	if err := store.RecordDriveUploaded(ctx, "task-file", "201", "other-id"); err == nil {
		t.Fatal("expected Drive file ID replacement rejection")
	}
	if err := store.MarkDriveVerified(ctx, "task-file", "201", "other-id"); err == nil {
		t.Fatal("expected mismatched verifier ID rejection")
	}
	if err := store.MarkDriveVerified(ctx, "task-file", "201", "drive-file-201"); err != nil {
		t.Fatal(err)
	}
	file, err = store.GetFile(ctx, "task-file", "201")
	if err != nil {
		t.Fatal(err)
	}
	if file.Status != FileDriveVerified {
		t.Fatalf("status=%s want=%s", file.Status, FileDriveVerified)
	}
}

func TestRetryableDriveFailureKeepsRecoveryStage(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	createStagingTestTask(t, store, "task-retry")
	if err := store.UpsertManifestPage(ctx, "task-retry", nil, []manifest.File{{
		SourceID: "301", LogicalPath: "/a.bin", ParentPath: "/", Name: "a.bin", Size: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE files SET status = ? WHERE task_id = 'task-retry' AND file_id = '301'`, FileLocalReady); err != nil {
		t.Fatal(err)
	}
	if err := store.StartDriveUpload(ctx, "task-retry", "301"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDriveFailure(ctx, "task-retry", "301", "temporary network failure", false); err != nil {
		t.Fatal(err)
	}
	file, err := store.GetFile(ctx, "task-retry", "301")
	if err != nil {
		t.Fatal(err)
	}
	if file.Status != FileDriveUploading || file.RetryCount != 1 || file.LastError == "" {
		t.Fatalf("retryable Drive failure lost recovery stage: %+v", file)
	}
}
