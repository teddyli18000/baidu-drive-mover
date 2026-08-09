package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakeTreeRemote struct {
	rootID   string
	rootName string
	roots    []remoteItem
	dirs     map[string][]remoteItem
	calls    []string
	nextID   int
}

func newFakeTreeRemote() *fakeTreeRemote {
	return &fakeTreeRemote{dirs: map[string][]remoteItem{"/": {}}, nextID: 1}
}

func (f *fakeTreeRemote) RunBase(_ context.Context, command string, args ...string) (rclone.Result, error) {
	f.calls = append(f.calls, "base "+command+" "+strings.Join(args, " "))
	switch command {
	case "lsjson":
		data, _ := json.Marshal(f.roots)
		return rclone.Result{Stdout: string(data)}, nil
	case "mkdir":
		if len(args) == 0 {
			return rclone.Result{}, fmt.Errorf("missing root target")
		}
		prefix := rclone.RemoteName + ":"
		if !strings.HasPrefix(args[0], prefix) {
			return rclone.Result{}, fmt.Errorf("unexpected root target %q", args[0])
		}
		name := strings.TrimPrefix(args[0], prefix)
		id := fmt.Sprintf("root-%d", f.nextID)
		f.nextID++
		f.rootName = name
		f.rootID = id
		f.roots = append(f.roots, remoteItem{ID: id, Name: name, IsDir: true})
		return rclone.Result{}, nil
	default:
		return rclone.Result{}, fmt.Errorf("unexpected base command %q", command)
	}
}

func (f *fakeTreeRemote) RunTask(_ context.Context, rootID, command string, args ...string) (rclone.Result, error) {
	f.calls = append(f.calls, "task["+rootID+"] "+command+" "+strings.Join(args, " "))
	if rootID == "" || (f.rootID != "" && rootID != f.rootID) {
		return rclone.Result{}, fmt.Errorf("wrong task root %q", rootID)
	}
	switch command {
	case "lsjson":
		if len(args) == 0 {
			return rclone.Result{}, fmt.Errorf("missing parent path")
		}
		logical, err := fakeLogicalFromRemote(args[0])
		if err != nil {
			return rclone.Result{}, err
		}
		data, _ := json.Marshal(f.dirs[logical])
		return rclone.Result{Stdout: string(data)}, nil
	case "mkdir":
		if len(args) == 0 {
			return rclone.Result{}, fmt.Errorf("missing directory target")
		}
		logical, err := fakeLogicalFromRemote(args[0])
		if err != nil {
			return rclone.Result{}, err
		}
		parent := path.Dir(logical)
		if parent == "." {
			parent = "/"
		}
		id := fmt.Sprintf("dir-%d", f.nextID)
		f.nextID++
		f.dirs[parent] = append(f.dirs[parent], remoteItem{ID: id, Name: path.Base(logical), IsDir: true})
		if _, exists := f.dirs[logical]; !exists {
			f.dirs[logical] = []remoteItem{}
		}
		return rclone.Result{}, nil
	default:
		return rclone.Result{}, fmt.Errorf("unexpected task command %q", command)
	}
}

func TestTreeBuilderCreatesRootAndParentFirstDirectories(t *testing.T) {
	store := newDriveTreeStore(t)
	ctx := context.Background()
	if err := store.CreateTask(ctx, state.Task{ID: "task-tree", ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManifestPage(ctx, "task-tree", []manifest.Directory{
		{LogicalPath: "/a/b/c"},
		{LogicalPath: "/empty"},
		{LogicalPath: "/a"},
		{LogicalPath: "/a/b"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	remote := newFakeTreeRemote()
	builder := &TreeBuilder{State: store, Remote: remote}
	rootID, err := builder.Ensure(ctx, "task-tree")
	if err != nil {
		t.Fatal(err)
	}
	if rootID == "" || rootID != remote.rootID {
		t.Fatalf("rootID=%q remote=%q", rootID, remote.rootID)
	}
	task, err := store.GetTask(ctx, "task-tree")
	if err != nil {
		t.Fatal(err)
	}
	if task.DriveRootID != rootID || task.DriveRootName != "BaiduDriveMover-task-tree" {
		t.Fatalf("unexpected task Drive root: %+v", task)
	}
	dirs, err := store.DriveDirectories(ctx, "task-tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range dirs {
		if directory.DriveID == "" {
			t.Fatalf("directory %q missing Drive ID", directory.LogicalPath)
		}
	}
	mkdirOrder := taskMkdirPaths(remote.calls)
	want := []string{"/a", "/empty", "/a/b", "/a/b/c"}
	if len(mkdirOrder) != len(want) {
		t.Fatalf("mkdir order=%v want=%v", mkdirOrder, want)
	}
	for i := range want {
		if mkdirOrder[i] != want[i] {
			t.Fatalf("mkdir[%d]=%q want=%q; calls=%v", i, mkdirOrder[i], want[i], remote.calls)
		}
	}
	for _, call := range remote.calls {
		if strings.HasPrefix(call, "task[") && !strings.Contains(call, "task["+rootID+"]") {
			t.Fatalf("post-root operation escaped persisted root ID: %q", call)
		}
	}
}

func TestTreeBuilderRecoversCrashCreatedRootWithoutDuplicate(t *testing.T) {
	store := newDriveTreeStore(t)
	ctx := context.Background()
	if err := store.CreateTask(ctx, state.Task{ID: "task-crash", ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	remote := newFakeTreeRemote()
	remote.rootID = "existing-root"
	remote.rootName = "BaiduDriveMover-task-crash"
	remote.roots = []remoteItem{{ID: remote.rootID, Name: remote.rootName, IsDir: true}}
	builder := &TreeBuilder{State: store, Remote: remote}
	rootID, err := builder.Ensure(ctx, "task-crash")
	if err != nil {
		t.Fatal(err)
	}
	if rootID != "existing-root" {
		t.Fatalf("rootID=%q", rootID)
	}
	for _, call := range remote.calls {
		if strings.HasPrefix(call, "base mkdir ") {
			t.Fatalf("crash-recovered root was duplicated: %v", remote.calls)
		}
	}
}

func TestTreeBuilderFailsClosedOnDuplicateRoot(t *testing.T) {
	store := newDriveTreeStore(t)
	ctx := context.Background()
	if err := store.CreateTask(ctx, state.Task{ID: "task-dupe", ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	remote := newFakeTreeRemote()
	name := "BaiduDriveMover-task-dupe"
	remote.roots = []remoteItem{
		{ID: "root-a", Name: name, IsDir: true},
		{ID: "root-b", Name: name, IsDir: true},
	}
	if _, err := (&TreeBuilder{State: store, Remote: remote}).Ensure(ctx, "task-dupe"); err == nil {
		t.Fatal("expected duplicate task root rejection")
	}
}

func TestTreeBuilderFailsClosedWhenPersistedDirectoryIDChanges(t *testing.T) {
	store := newDriveTreeStore(t)
	ctx := context.Background()
	if err := store.CreateTask(ctx, state.Task{ID: "task-moved", ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused, DriveRootID: "root-1", DriveRootName: "BaiduDriveMover-task-moved"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManifestPage(ctx, "task-moved", []manifest.Directory{{LogicalPath: "/a"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDirectoryDriveID(ctx, "task-moved", "/a", "dir-persisted"); err != nil {
		t.Fatal(err)
	}
	remote := newFakeTreeRemote()
	remote.rootID = "root-1"
	remote.dirs["/"] = []remoteItem{{ID: "dir-other", Name: "a", IsDir: true}}
	if _, err := (&TreeBuilder{State: store, Remote: remote}).Ensure(ctx, "task-moved"); err == nil {
		t.Fatal("expected persisted directory identity mismatch rejection")
	}
}

func TestValidateLogicalPathRejectsTraversalAndNonCanonicalForms(t *testing.T) {
	bad := []string{"", "relative", "/a/../b", "/a//b", "/a/./b", "/trailing/", "/bad\x00name"}
	for _, value := range bad {
		if _, err := validateLogicalPath(value, false); err == nil {
			t.Fatalf("unsafe logical path accepted: %q", value)
		}
	}
	for _, value := range []string{"/a", "/a/b", "/空 格/文件夹", `/back\\slash`} {
		if _, err := validateLogicalPath(value, false); err != nil {
			t.Fatalf("valid service path %q rejected: %v", value, err)
		}
	}
}

func newDriveTreeStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func fakeLogicalFromRemote(remote string) (string, error) {
	prefix := rclone.RemoteName + ":"
	if !strings.HasPrefix(remote, prefix) {
		return "", fmt.Errorf("unexpected remote path %q", remote)
	}
	rel := strings.TrimPrefix(remote, prefix)
	if rel == "" {
		return "/", nil
	}
	return "/" + rel, nil
}

func taskMkdirPaths(calls []string) []string {
	var result []string
	prefix := " mkdir " + rclone.RemoteName + ":"
	for _, call := range calls {
		if !strings.HasPrefix(call, "task[") || !strings.Contains(call, prefix) {
			continue
		}
		index := strings.Index(call, prefix)
		remote := call[index+len(" mkdir "):]
		logical, err := fakeLogicalFromRemote(strings.Fields(remote)[0])
		if err == nil {
			result = append(result, logical)
		}
	}
	return result
}
