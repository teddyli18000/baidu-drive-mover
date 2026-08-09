package state

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	_ "modernc.org/sqlite"
)

func TestPlanBatchesSplitsSingleLargeDirectory(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	taskID := "task-large"
	createStagingTestTask(t, store, taskID)

	files := make([]manifest.File, 0, 1201)
	for i := 0; i < 1201; i++ {
		files = append(files, manifest.File{
			SourceID:    fmt.Sprintf("%d", 10000+i),
			LogicalPath: fmt.Sprintf("/bulk/f-%04d.bin", i),
			ParentPath:  "/bulk",
			Name:        fmt.Sprintf("f-%04d.bin", i),
			Size:        int64(i + 1),
		})
	}
	if err := store.UpsertManifestPage(ctx, taskID, []manifest.Directory{{LogicalPath: "/bulk"}}, files); err != nil {
		t.Fatal(err)
	}
	planned, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 7 {
		t.Fatalf("batches=%d want=7", len(planned))
	}
	var total int
	seenIDs := map[string]bool{}
	for _, batch := range planned {
		if batch.FileCount < 1 || batch.FileCount > 200 {
			t.Fatalf("invalid batch size %d", batch.FileCount)
		}
		if batch.LogicalParent != "/bulk" {
			t.Fatalf("unexpected parent %q", batch.LogicalParent)
		}
		if seenIDs[batch.BatchID] {
			t.Fatalf("duplicate batch ID %q", batch.BatchID)
		}
		seenIDs[batch.BatchID] = true
		if batch.BaiduStagingPath == "" {
			t.Fatal("missing staging path")
		}
		total += batch.FileCount
	}
	if total != 1201 {
		t.Fatalf("planned files=%d want=1201", total)
	}

	again, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second planning created %d batches", len(again))
	}
	batches, err := store.StagingBatches(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 7 {
		t.Fatalf("stored batches=%d want=7", len(batches))
	}
}

func TestPlanBatchesNeverMixesLogicalParents(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	taskID := "task-parents"
	createStagingTestTask(t, store, taskID)
	var files []manifest.File
	for i := 0; i < 350; i++ {
		files = append(files, manifest.File{SourceID: fmt.Sprintf("a-%03d", i), LogicalPath: fmt.Sprintf("/a/f-%03d", i), ParentPath: "/a", Name: fmt.Sprintf("f-%03d", i), Size: 1})
	}
	for i := 0; i < 2; i++ {
		files = append(files, manifest.File{SourceID: fmt.Sprintf("b-%03d", i), LogicalPath: fmt.Sprintf("/b/f-%03d", i), ParentPath: "/b", Name: fmt.Sprintf("f-%03d", i), Size: 1})
	}
	if err := store.UpsertManifestPage(ctx, taskID, nil, files); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 {
		t.Fatalf("batches=%d want=3", len(batches))
	}
	for _, batch := range batches {
		for _, file := range batch.Files {
			if file.ParentPath != batch.LogicalParent {
				t.Fatalf("batch %s mixed parent %q into %q", batch.BatchID, file.ParentPath, batch.LogicalParent)
			}
		}
	}
}

func TestPlanBatchesRejectsUnsafeOrLimitSizedInputs(t *testing.T) {
	store := newStagingTestStore(t)
	if _, err := store.PlanBatches(context.Background(), "../escape", 200); err == nil {
		t.Fatal("expected unsafe task ID rejection")
	}
	if _, err := store.PlanBatches(context.Background(), "task-safe", 500); err == nil {
		t.Fatal("expected 500-sized batch rejection")
	}
}

func TestStagingBatchStateRoundTrip(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	taskID := "task-state"
	createStagingTestTask(t, store, taskID)
	if err := store.UpsertManifestPage(ctx, taskID, nil, []manifest.File{
		{SourceID: "1", LogicalPath: "/x/a.bin", ParentPath: "/x", Name: "a.bin", Size: 10},
		{SourceID: "2", LogicalPath: "/x/b.bin", ParentPath: "/x", Name: "b.bin", Size: 20},
	}); err != nil {
		t.Fatal(err)
	}
	planned, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil || len(planned) != 1 {
		t.Fatalf("plan err=%v batches=%d", err, len(planned))
	}
	batch := planned[0]
	if err := store.StartBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordStagedFiles(ctx, taskID, batch.BatchID, map[string]string{
		"1": batch.BaiduStagingPath + "/a.bin",
		"2": batch.BaiduStagingPath + "/b.bin",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	var status BatchStatus
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM batches WHERE task_id = ? AND batch_id = ?`, taskID, batch.BatchID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != BatchStaged {
		t.Fatalf("status=%s want=%s", status, BatchStaged)
	}
	var cleanupAllowed int
	if err := store.db.QueryRowContext(ctx, `SELECT cleanup_allowed FROM owned_objects WHERE task_id = ? AND scope = 'baidu_batch_dir' AND object_id = ?`, taskID, batch.BatchID).Scan(&cleanupAllowed); err != nil {
		t.Fatal(err)
	}
	if cleanupAllowed != 0 {
		t.Fatal("staging batch unexpectedly cleanup-eligible before Drive verification")
	}
}

func TestMigrationFromSchemaV1ToLatest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationV1(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(1, ?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
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
	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("schema=%d want=4", version)
	}
	if _, err := store.db.Exec(`SELECT baidu_staging_path FROM batches LIMIT 1`); err != nil {
		t.Fatalf("missing v2 batches column: %v", err)
	}
	if _, err := store.db.Exec(`SELECT task_id, batch_id, file_id, ordinal FROM batch_files LIMIT 1`); err != nil {
		t.Fatalf("missing batch_files table: %v", err)
	}
	if _, err := store.db.Exec(`SELECT drive_root_name FROM tasks LIMIT 1`); err != nil {
		t.Fatalf("missing v3 task Drive root name column: %v", err)
	}
	if _, err := store.db.Exec(`SELECT cleaned_at, last_error FROM owned_objects LIMIT 1`); err != nil {
		t.Fatalf("missing v4 cleanup outcome columns: %v", err)
	}
}

func newStagingTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createStagingTestTask(t *testing.T, store *Store, taskID string) {
	t.Helper()
	if err := store.CreateTask(context.Background(), Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: TaskPaused}); err != nil {
		t.Fatal(err)
	}
}
