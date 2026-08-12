package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

func TestMigrationV6ClearsInvalidMD5AndRecoversOnlyMatchingFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, taskID := range []string{"recover", "preserve"} {
		if err := store.CreateTask(ctx, Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: TaskBlocked}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertManifestPage(ctx, taskID, nil, []manifest.File{{
			SourceID: "1", LogicalPath: "/a.bin", ParentPath: "/", Name: "a.bin", Size: 1, MD5: "not-a-provider-md5",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE files SET status='FAILED_PERMANENT', md5=' 0123456789abcdef0123456789abcdef ', baidu_staging_path='/BaiduDriveMover/recover/b/a.bin', local_cache_path='cache/recover/1.bin', retry_count=1, last_error='cache MD5 mismatch' WHERE task_id='recover'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE files SET status='FAILED_PERMANENT', md5=' 0123456789abcdef0123456789abcdef ', baidu_staging_path='/BaiduDriveMover/preserve/b/a.bin', retry_count=2, last_error='unrelated permanent failure' WHERE task_id='preserve'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE version=6`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	recoveredTask, err := store.GetTask(ctx, "recover")
	if err != nil {
		t.Fatal(err)
	}
	if recoveredTask.Status != TaskPaused || recoveredTask.LastError != "" {
		t.Fatalf("recovered task status=%s error=%q", recoveredTask.Status, recoveredTask.LastError)
	}
	var status, md5, localCache, lastError string
	var retryCount int
	if err := store.db.QueryRowContext(ctx, `SELECT status, md5, local_cache_path, retry_count, last_error FROM files WHERE task_id='recover'`).Scan(&status, &md5, &localCache, &retryCount, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != string(FileBaiduStaged) || md5 != "" || localCache == "" || retryCount != 0 || lastError != "" {
		t.Fatalf("recovered file status=%s md5=%q cache=%t retries=%d error=%q", status, md5, localCache != "", retryCount, lastError)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status, md5, retry_count, last_error FROM files WHERE task_id='preserve'`).Scan(&status, &md5, &retryCount, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != string(FileFailedPermanent) || md5 != "" || retryCount != 2 || lastError == "" {
		t.Fatalf("preserved file status=%s md5=%q retries=%d error=%q", status, md5, retryCount, lastError)
	}
}
