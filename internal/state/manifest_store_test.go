package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

func TestManifestUpsertIsIdempotent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CreateTask(ctx, Task{ID: "task-manifest", ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: TaskScanning}); err != nil {
		t.Fatal(err)
	}
	dirs := []manifest.Directory{{LogicalPath: "/empty"}}
	files := []manifest.File{{SourceID: "1001", LogicalPath: "/a.txt", ParentPath: "/", Name: "a.txt", Size: 12, MD5: "fake-md5"}}
	for i := 0; i < 2; i++ {
		if err := store.UpsertManifestPage(ctx, "task-manifest", dirs, files); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := store.ManifestStats(ctx, "task-manifest")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Directories != 1 || stats.Files != 1 || stats.Bytes != 12 {
		t.Fatalf("unexpected manifest stats: %+v", stats)
	}
}
