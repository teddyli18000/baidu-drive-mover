package drive

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakeUploadRemote struct {
	rootID             string
	files              map[string][]remoteItem
	calls              []string
	nextID             int
	copyErrAfterCommit bool
	mutateSource       bool
}

func newFakeUploadRemote(rootID string) *fakeUploadRemote {
	return &fakeUploadRemote{rootID: rootID, files: map[string][]remoteItem{}, nextID: 1}
}

func (f *fakeUploadRemote) RunTask(_ context.Context, rootID, command string, args ...string) (rclone.Result, error) {
	f.calls = append(f.calls, command+" "+strings.Join(args, " "))
	if rootID != f.rootID {
		return rclone.Result{}, fmt.Errorf("unexpected root ID %q", rootID)
	}
	switch command {
	case "lsjson":
		if len(args) == 0 {
			return rclone.Result{}, fmt.Errorf("missing lsjson path")
		}
		logical, err := fakeLogicalFromRemote(args[0])
		if err != nil {
			return rclone.Result{}, err
		}
		data, _ := json.Marshal(f.files[logical])
		return rclone.Result{Stdout: string(data)}, nil
	case "copyto":
		if len(args) < 2 {
			return rclone.Result{}, fmt.Errorf("missing copyto args")
		}
		source := args[0]
		destination, err := fakeLogicalFromRemote(args[1])
		if err != nil {
			return rclone.Result{}, err
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return rclone.Result{}, err
		}
		hash := md5.Sum(data)
		parent := path.Dir(destination)
		if parent == "." {
			parent = "/"
		}
		item := remoteItem{
			ID:     fmt.Sprintf("file-%d", f.nextID),
			Name:   path.Base(destination),
			IsDir:  false,
			Size:   int64(len(data)),
			Hashes: map[string]string{"MD5": hex.EncodeToString(hash[:])},
		}
		f.nextID++
		f.files[parent] = append(f.files[parent], item)
		if f.mutateSource {
			mutated := append([]byte(nil), data...)
			if len(mutated) > 0 {
				mutated[0] ^= 0xff
			}
			if err := os.WriteFile(source, mutated, 0o600); err != nil {
				return rclone.Result{}, err
			}
		}
		if f.copyErrAfterCommit {
			return rclone.Result{}, errors.New("synthetic post-commit transport failure")
		}
		return rclone.Result{}, nil
	default:
		return rclone.Result{}, fmt.Errorf("unexpected upload command %q", command)
	}
}

func TestUploaderUploadsThenIndependentlyVerifies(t *testing.T) {
	store, layout, file, data := newUploadFixture(t, "task-upload", "401", "/docs/hello.txt", []byte("hello drive"))
	remote := newFakeUploadRemote("root-upload")
	summary, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-upload")
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesVerified != 1 || summary.BytesVerified != int64(len(data)) {
		t.Fatalf("summary=%+v", summary)
	}
	updated, err := store.GetFile(context.Background(), file.TaskID, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != state.FileDriveVerified || updated.DriveID == "" {
		t.Fatalf("unexpected final state: %+v", updated)
	}
	if countCommand(remote.calls, "copyto") != 1 || countCommand(remote.calls, "lsjson") < 3 {
		t.Fatalf("expected reconcile + upload + independent verification calls, got %v", remote.calls)
	}
}

func TestUploaderAdoptsCrashCommittedMatchingObjectWithoutCopy(t *testing.T) {
	store, layout, file, data := newUploadFixture(t, "task-adopt", "402", "/a.bin", []byte("already remote"))
	remote := newFakeUploadRemote("root-upload")
	hash := md5.Sum(data)
	remote.files["/"] = []remoteItem{{ID: "existing-402", Name: "a.bin", Size: int64(len(data)), Hashes: map[string]string{"MD5": hex.EncodeToString(hash[:])}}}

	summary, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-adopt")
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesVerified != 1 || countCommand(remote.calls, "copyto") != 0 {
		t.Fatalf("unexpected recovery behavior summary=%+v calls=%v", summary, remote.calls)
	}
	updated, _ := store.GetFile(context.Background(), file.TaskID, file.FileID)
	if updated.Status != state.FileDriveVerified || updated.DriveID != "existing-402" {
		t.Fatalf("unexpected adopted state: %+v", updated)
	}
}

func TestUploaderReconcilesRemoteCommitEvenWhenCopytoReturnsError(t *testing.T) {
	store, layout, _, _ := newUploadFixture(t, "task-postcommit", "403", "/x.bin", []byte("commit then fail"))
	remote := newFakeUploadRemote("root-upload")
	remote.copyErrAfterCommit = true
	summary, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-postcommit")
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesVerified != 1 || countCommand(remote.calls, "copyto") != 1 {
		t.Fatalf("post-commit recovery failed summary=%+v calls=%v", summary, remote.calls)
	}
}

func TestUploaderFailsClosedOnDuplicateSameNameObjects(t *testing.T) {
	store, layout, file, data := newUploadFixture(t, "task-dupefile", "404", "/dupe.txt", []byte("same"))
	hash := md5.Sum(data)
	remote := newFakeUploadRemote("root-upload")
	remote.files["/"] = []remoteItem{
		{ID: "a", Name: "dupe.txt", Size: int64(len(data)), Hashes: map[string]string{"MD5": hex.EncodeToString(hash[:])}},
		{ID: "b", Name: "dupe.txt", Size: int64(len(data)), Hashes: map[string]string{"MD5": hex.EncodeToString(hash[:])}},
	}
	if _, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-dupefile"); err == nil {
		t.Fatal("expected duplicate file conflict")
	}
	if countCommand(remote.calls, "copyto") != 0 {
		t.Fatalf("duplicate conflict unexpectedly wrote to Drive: %v", remote.calls)
	}
	updated, _ := store.GetFile(context.Background(), file.TaskID, file.FileID)
	if updated.Status != state.FileFailedPermanent {
		t.Fatalf("duplicate conflict status=%s", updated.Status)
	}
}

func TestUploaderRejectsSameNameObjectWithMissingOrWrongEvidence(t *testing.T) {
	cases := []struct {
		name   string
		remote remoteItem
	}{
		{name: "missing-md5", remote: remoteItem{ID: "x", Name: "e.bin", Size: 4, Hashes: map[string]string{}}},
		{name: "wrong-size", remote: remoteItem{ID: "x", Name: "e.bin", Size: 5, Hashes: map[string]string{"MD5": "deadbeef"}}},
		{name: "wrong-md5", remote: remoteItem{ID: "x", Name: "e.bin", Size: 4, Hashes: map[string]string{"MD5": "deadbeef"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, layout, _, _ := newUploadFixture(t, "task-evidence-"+tc.name, "405", "/e.bin", []byte("data"))
			remote := newFakeUploadRemote("root-upload")
			remote.files["/"] = []remoteItem{tc.remote}
			if _, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-evidence-"+tc.name); err == nil {
				t.Fatal("expected evidence rejection")
			}
			if countCommand(remote.calls, "copyto") != 0 {
				t.Fatalf("untrusted existing object was overwritten: %v", remote.calls)
			}
		})
	}
}

func TestUploaderRejectsLocalCacheThatChangedSinceBaiduVerification(t *testing.T) {
	store, layout, file, data := newUploadFixture(t, "task-cachechange", "406", "/c.bin", []byte("original"))
	full, err := layout.ResolveTempRelative(file.LocalCachePath)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), data...)
	changed[0] ^= 0xff
	if err := os.WriteFile(full, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	remote := newFakeUploadRemote("root-upload")
	if _, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-cachechange"); err == nil {
		t.Fatal("expected changed local cache rejection")
	}
	if len(remote.calls) != 0 {
		t.Fatalf("local corruption should fail before Drive access: %v", remote.calls)
	}
}

func TestUploaderDetectsLocalMutationDuringTransfer(t *testing.T) {
	store, layout, _, _ := newUploadFixture(t, "task-toctou", "407", "/t.bin", []byte("transfer-bytes"))
	remote := newFakeUploadRemote("root-upload")
	remote.mutateSource = true
	if _, err := (&Uploader{Layout: layout, State: store, Remote: remote}).Run(context.Background(), "task-toctou"); err == nil {
		t.Fatal("expected in-transfer local mutation rejection")
	}
	file, err := store.GetFile(context.Background(), "task-toctou", "407")
	if err != nil {
		t.Fatal(err)
	}
	if file.Status != state.FileFailedPermanent {
		t.Fatalf("mutation status=%s want permanent failure", file.Status)
	}
}

func TestLocalReadyStateRejectsNonOpaqueRegisteredCachePath(t *testing.T) {
	store, _, file, _ := newUploadFixture(t, "task-badcache", "409", "/bad.bin", []byte("safe"))
	if err := store.MarkLocalReady(context.Background(), file.TaskID, file.FileID, "cache/other/attacker.bin"); err == nil {
		t.Fatal("expected non-opaque cache path rejection at state boundary")
	}
	after, err := store.GetFile(context.Background(), file.TaskID, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LocalCachePath != file.LocalCachePath {
		t.Fatalf("rejected cache path changed persisted path: %q", after.LocalCachePath)
	}
}

func newUploadFixture(t *testing.T, taskID, fileID, logical string, data []byte) (*state.Store, *runtimepath.Layout, state.File, []byte) {
	t.Helper()
	return newUploadFixtureWithCachePath(t, taskID, fileID, logical, data, path.Join("cache", taskID, fileID+".bin"))
}

func newUploadFixtureWithCachePath(t *testing.T, taskID, fileID, logical string, data []byte, cachePath string) (*state.Store, *runtimepath.Layout, state.File, []byte) {
	t.Helper()
	ctx := context.Background()
	layout, err := runtimepath.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(layout.Temp, "upload-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateTask(ctx, state.Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskPaused}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskDriveRoot(ctx, taskID, "BaiduDriveMover-"+taskID, "root-upload"); err != nil {
		t.Fatal(err)
	}
	hash := md5.Sum(data)
	parent := path.Dir(logical)
	if parent == "." {
		parent = "/"
	}
	if err := store.UpsertManifestPage(ctx, taskID, nil, []manifest.File{{
		SourceID: fileID, LogicalPath: logical, ParentPath: parent, Name: path.Base(logical), Size: int64(len(data)), MD5: hex.EncodeToString(hash[:]),
	}}); err != nil {
		t.Fatal(err)
	}
	batches, err := store.PlanBatches(ctx, taskID, 200)
	if err != nil || len(batches) != 1 {
		t.Fatalf("plan batches err=%v count=%d", err, len(batches))
	}
	batch := batches[0]
	if err := store.StartBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordStagedFiles(ctx, taskID, batch.BatchID, map[string]string{fileID: batch.BaiduStagingPath + "/" + path.Base(logical)}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteBatch(ctx, taskID, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(cachePath, "cache/"+taskID+"/") {
		dir := path.Dir(cachePath)
		if _, err := layout.EnsureTempDir(dir); err != nil {
			t.Fatal(err)
		}
		full, err := layout.ResolveTempRelative(cachePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkLocalReady(ctx, taskID, fileID, cachePath); err != nil {
		t.Fatal(err)
	}
	file, err := store.GetFile(ctx, taskID, fileID)
	if err != nil {
		t.Fatal(err)
	}
	return store, layout, file, data
}

func countCommand(calls []string, command string) int {
	count := 0
	for _, call := range calls {
		if strings.HasPrefix(call, command+" ") {
			count++
		}
	}
	return count
}
