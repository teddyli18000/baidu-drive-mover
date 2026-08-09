package state

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStoreMigrationAndTaskRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}

	want := Task{ID: "task-1", ShareURL: "https://pan.baidu.com/s/example", ExtractionCode: "abcd", Status: TaskNew}
	if err := store.CreateTask(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.ShareURL != want.ShareURL || got.ExtractionCode != want.ExtractionCode || got.Status != want.Status {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}
