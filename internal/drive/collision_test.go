package drive

import (
	"context"
	"strings"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

func TestTreeBuilderRejectsSameNameFileWhereRootFolderIsExpected(t *testing.T) {
	store := newDriveTreeStore(t)
	ctx := context.Background()
	if err := store.CreateTask(ctx, state.Task{ID: "task-root-type", ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	remote := newFakeTreeRemote()
	name := "BaiduDriveMover-task-root-type"
	remote.roots = []remoteItem{{ID: "file-not-folder", Name: name, IsDir: false, Size: 1}}
	if _, err := (&TreeBuilder{State: store, Remote: remote}).Ensure(ctx, "task-root-type"); err == nil {
		t.Fatal("expected same-name file to block task-root folder creation")
	}
	for _, call := range remote.calls {
		if strings.HasPrefix(call, "base mkdir ") {
			t.Fatalf("cross-type root conflict unexpectedly created a folder: %v", remote.calls)
		}
	}
}

func TestTreeBuilderRejectsSameNameFileWhereDirectoryIsExpected(t *testing.T) {
	store := newDriveTreeStore(t)
	ctx := context.Background()
	if err := store.CreateTask(ctx, state.Task{
		ID: "task-dir-type", ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused,
		DriveRootID: "root-1", DriveRootName: "BaiduDriveMover-task-dir-type",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManifestPage(ctx, "task-dir-type", []manifest.Directory{{LogicalPath: "/docs"}}, nil); err != nil {
		t.Fatal(err)
	}
	remote := newFakeTreeRemote()
	remote.rootID = "root-1"
	remote.dirs["/"] = []remoteItem{{ID: "file-docs", Name: "docs", IsDir: false, Size: 2}}
	if _, err := (&TreeBuilder{State: store, Remote: remote}).Ensure(ctx, "task-dir-type"); err == nil {
		t.Fatal("expected same-name file to block logical directory creation")
	}
	for _, call := range remote.calls {
		if strings.Contains(call, " mkdir ") {
			t.Fatalf("cross-type directory conflict unexpectedly wrote to Drive: %v", remote.calls)
		}
	}
}

func TestUploaderRejectsSameNameDirectoryWhereFileIsExpected(t *testing.T) {
	store, layout, _, _ := newUploadFixture(t, "task-file-type", "501", "/docs/report.bin", []byte("report"))
	remote := newFakeUploadRemote("root-upload")
	remote.files["/docs"] = []remoteItem{{ID: "dir-report", Name: "report.bin", IsDir: true}}
	if _, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-file-type"); err == nil {
		t.Fatal("expected same-name directory to block file upload")
	}
	if countCommand(remote.calls, "copyto") != 0 {
		t.Fatalf("cross-type file conflict unexpectedly wrote to Drive: %v", remote.calls)
	}
}

func TestUploaderCopytoUsesIgnoreExistingRaceGuard(t *testing.T) {
	store, layout, _, _ := newUploadFixture(t, "task-ignore-existing", "502", "/race.bin", []byte("race-safe"))
	remote := newFakeUploadRemote("root-upload")
	if _, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-ignore-existing"); err != nil {
		t.Fatal(err)
	}
	var copyCall string
	for _, call := range remote.calls {
		if strings.HasPrefix(call, "copyto ") {
			copyCall = call
			break
		}
	}
	if copyCall == "" || !strings.Contains(copyCall, "--ignore-existing") {
		t.Fatalf("copyto lacks overwrite-race guard: %v", remote.calls)
	}
}
