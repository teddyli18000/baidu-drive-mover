package state

import (
	"context"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
)

func TestManifestSourceIDCannotRebindLogicalPath(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	createStagingTestTask(t, store, "task-rebind")
	first := manifest.File{SourceID: "101", LogicalPath: "/a.bin", ParentPath: "/", Name: "a.bin", Size: 7, MD5: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := store.UpsertManifestPage(ctx, "task-rebind", nil, []manifest.File{first}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.LogicalPath = "/b.bin"
	second.Name = "b.bin"
	if err := store.UpsertManifestPage(ctx, "task-rebind", nil, []manifest.File{second}); err == nil {
		t.Fatal("expected source ID path rebinding rejection")
	}
	file, err := store.GetFile(ctx, "task-rebind", "101")
	if err != nil {
		t.Fatal(err)
	}
	if file.LogicalPath != "/a.bin" || file.Name != "a.bin" {
		t.Fatalf("rejected rebind changed persisted identity: %+v", file)
	}
}

func TestManifestRejectsFileDirectoryCollisionBothDirections(t *testing.T) {
	ctx := context.Background()

	store := newStagingTestStore(t)
	createStagingTestTask(t, store, "task-dir-first")
	if err := store.UpsertManifestPage(ctx, "task-dir-first", []manifest.Directory{{LogicalPath: "/same"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManifestPage(ctx, "task-dir-first", nil, []manifest.File{{
		SourceID: "1", LogicalPath: "/same", ParentPath: "/", Name: "same", Size: 1,
	}}); err == nil {
		t.Fatal("expected file over existing directory rejection")
	}

	store2 := newStagingTestStore(t)
	createStagingTestTask(t, store2, "task-file-first")
	if err := store2.UpsertManifestPage(ctx, "task-file-first", nil, []manifest.File{{
		SourceID: "2", LogicalPath: "/same", ParentPath: "/", Name: "same", Size: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store2.UpsertManifestPage(ctx, "task-file-first", []manifest.Directory{{LogicalPath: "/same"}}, nil); err == nil {
		t.Fatal("expected directory over existing file rejection")
	}
}

func TestManifestAllowsMD5EnrichmentButRejectsConflictingMD5(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	createStagingTestTask(t, store, "task-md5")
	file := manifest.File{SourceID: "9", LogicalPath: "/a.bin", ParentPath: "/", Name: "a.bin", Size: 4}
	if err := store.UpsertManifestPage(ctx, "task-md5", nil, []manifest.File{file}); err != nil {
		t.Fatal(err)
	}
	file.MD5 = "0123456789abcdef0123456789abcdef"
	if err := store.UpsertManifestPage(ctx, "task-md5", nil, []manifest.File{file}); err != nil {
		t.Fatalf("MD5 enrichment failed: %v", err)
	}
	persisted, err := store.GetFile(ctx, "task-md5", "9")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MD5 != file.MD5 {
		t.Fatalf("MD5=%q want=%q", persisted.MD5, file.MD5)
	}
	file.MD5 = "ffffffffffffffffffffffffffffffff"
	if err := store.UpsertManifestPage(ctx, "task-md5", nil, []manifest.File{file}); err == nil {
		t.Fatal("expected conflicting MD5 rejection")
	}
}

func TestManifestPreservesSpacesAndRejectsNonCanonicalPath(t *testing.T) {
	store := newStagingTestStore(t)
	ctx := context.Background()
	createStagingTestTask(t, store, "task-names")
	name := "  report .txt  "
	if err := store.UpsertManifestPage(ctx, "task-names", nil, []manifest.File{{
		SourceID: "77", LogicalPath: "/" + name, ParentPath: "/", Name: name, Size: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	file, err := store.GetFile(ctx, "task-names", "77")
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != name || file.LogicalPath != "/"+name {
		t.Fatalf("spaces were normalized unexpectedly: name=%q path=%q", file.Name, file.LogicalPath)
	}
	if err := store.UpsertManifestPage(ctx, "task-names", nil, []manifest.File{{
		SourceID: "78", LogicalPath: "/a/../b.bin", ParentPath: "/", Name: "b.bin", Size: 1,
	}}); err == nil {
		t.Fatal("expected non-canonical logical path rejection")
	}
}
