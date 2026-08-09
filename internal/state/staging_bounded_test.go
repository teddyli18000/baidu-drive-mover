package state

import (
	"context"
	"fmt"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

func TestPlanBatchesBoundedSplitsByBytesAndCount(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	taskID := "task-bounded"
	createStagingTestTask(t, store, taskID)

	var files []manifest.File
	for i := 0; i < 7; i++ {
		files = append(files, manifest.File{
			SourceID:    fmt.Sprintf("%d", 9000+i),
			LogicalPath: fmt.Sprintf("/bulk/f-%d.bin", i),
			ParentPath:  "/bulk",
			Name:        fmt.Sprintf("f-%d.bin", i),
			Size:        4,
		})
	}
	if err := store.UpsertManifestPage(ctx, taskID, nil, files); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatchesBounded(ctx, taskID, 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 4 {
		t.Fatalf("batches=%d want=4", len(batches))
	}
	wantCounts := []int{2, 2, 2, 1}
	for i, batch := range batches {
		if batch.FileCount != wantCounts[i] {
			t.Fatalf("batch %d count=%d want=%d", i, batch.FileCount, wantCounts[i])
		}
		if batch.TotalBytes > 10 {
			t.Fatalf("batch %d bytes=%d exceeds limit", i, batch.TotalBytes)
		}
	}
}

func TestPlanBatchesBoundedNeverMixesParents(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	taskID := "task-bounded-parents"
	createStagingTestTask(t, store, taskID)
	files := []manifest.File{
		{SourceID: "1", LogicalPath: "/a/one", ParentPath: "/a", Name: "one", Size: 3},
		{SourceID: "2", LogicalPath: "/a/two", ParentPath: "/a", Name: "two", Size: 3},
		{SourceID: "3", LogicalPath: "/b/one", ParentPath: "/b", Name: "one", Size: 3},
	}
	if err := store.UpsertManifestPage(ctx, taskID, nil, files); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatchesBounded(ctx, taskID, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches=%d want=2", len(batches))
	}
	for _, batch := range batches {
		for _, file := range batch.Files {
			if file.ParentPath != batch.LogicalParent {
				t.Fatalf("mixed parent %q into %q", file.ParentPath, batch.LogicalParent)
			}
		}
	}
}

func TestPlanBatchesBoundedIsolatesSingleOversizedFile(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	taskID := "task-bounded-large"
	createStagingTestTask(t, store, taskID)
	if err := store.UpsertManifestPage(ctx, taskID, nil, []manifest.File{
		{SourceID: "1", LogicalPath: "/a/huge", ParentPath: "/a", Name: "huge", Size: 100},
		{SourceID: "2", LogicalPath: "/a/small", ParentPath: "/a", Name: "small", Size: 1},
	}); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatchesBounded(ctx, taskID, 200, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || batches[0].FileCount != 1 || batches[0].TotalBytes != 100 {
		t.Fatalf("oversized file not isolated: %+v", batches)
	}
}
