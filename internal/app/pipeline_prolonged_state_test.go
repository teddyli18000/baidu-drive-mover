package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"testing"

	cleanupengine "github.com/teddyli18000/baidu-drive-mover/internal/cleanup"
	"github.com/teddyli18000/baidu-drive-mover/internal/download"
	"github.com/teddyli18000/baidu-drive-mover/internal/manifest"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type prolongedStateFaults struct {
	calls map[string]int
	at    map[string]map[int]bool
}

func newProlongedStateFaults() *prolongedStateFaults {
	return &prolongedStateFaults{
		calls: make(map[string]int),
		at: map[string]map[int]bool{
			"stage":    {3: true, 17: true, 41: true},
			"download": {5: true, 23: true, 47: true},
			"drive":    {7: true, 29: true, 53: true},
			"cleanup":  {11: true, 31: true, 59: true},
		},
	}
}

func (f *prolongedStateFaults) inject(stage string) bool {
	f.calls[stage]++
	if !f.at[stage][f.calls[stage]] {
		return false
	}
	delete(f.at[stage], f.calls[stage])
	return true
}

func TestProlongedMigrationPersistsAcrossSQLiteRestarts(t *testing.T) {
	const (
		taskID    = "prolonged-state"
		fileCount = 5000
		watermark = int64(64 << 10)
	)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store := openProlongedStore(t, dbPath)
	defer func() { _ = store.Close() }()

	if err := store.CreateTask(ctx, state.Task{ID: taskID, ShareURL: "https://pan.baidu.com/s/1Synthetic", Status: state.TaskScanning}); err != nil {
		t.Fatal(err)
	}
	directories := []manifest.Directory{
		{LogicalPath: "/group-0"},
		{LogicalPath: "/group-1"},
		{LogicalPath: "/group-2"},
		{LogicalPath: "/group-3"},
		{LogicalPath: "/group-4"},
	}
	for start := 0; start < fileCount; start += 250 {
		end := start + 250
		if end > fileCount {
			end = fileCount
		}
		files := make([]manifest.File, 0, end-start)
		for i := start; i < end; i++ {
			parent := fmt.Sprintf("/group-%d", i%len(directories))
			name := fmt.Sprintf("file-%05d.bin", i)
			files = append(files, manifest.File{
				SourceID:    fmt.Sprintf("%d", 100000+i),
				LogicalPath: path.Join(parent, name),
				ParentPath:  parent,
				Name:        name,
				Size:        int64(512 + (i%31)*97),
			})
		}
		pageDirectories := []manifest.Directory(nil)
		if start == 0 {
			pageDirectories = directories
		}
		if err := store.UpsertManifestPage(ctx, taskID, pageDirectories, files); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PlanBatchesBounded(ctx, taskID, 8, 8<<10); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskDriveRoot(ctx, taskID, "BaiduDriveMover-"+taskID, "drive-root"); err != nil {
		t.Fatal(err)
	}
	for i, directory := range directories {
		if err := store.RecordDirectoryDriveID(ctx, taskID, directory.LogicalPath, fmt.Sprintf("dir-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	faults := newProlongedStateFaults()
	verified := make(map[string]bool, fileCount)
	interruptedDownloads := make(map[string]bool)
	resumedDownloads := 0
	maxReserved := int64(0)
	restarts := 0
	const unrelatedSentinel = "/unrelated/user-data"

	observeWatermark := func() error {
		reserved, err := store.ReservedCacheBytes(ctx)
		if err != nil {
			return err
		}
		if reserved > maxReserved {
			maxReserved = reserved
		}
		if reserved > watermark {
			return fmt.Errorf("cache reservation %d exceeded watermark %d", reserved, watermark)
		}
		return nil
	}

	stagePass := func(_ context.Context, _ string, maxBytes int64) (StageSummary, error) {
		batches, err := store.StagingBatches(ctx, taskID)
		if err != nil || len(batches) == 0 {
			return StageSummary{}, err
		}
		var batch *state.Batch
		for i := range batches {
			if batches[i].TotalBytes <= maxBytes {
				batch = &batches[i]
				break
			}
		}
		if batch == nil {
			return StageSummary{}, nil
		}
		if err := store.StartBatch(ctx, taskID, batch.BatchID); err != nil {
			return StageSummary{}, err
		}
		staged := make(map[string]string, len(batch.Files))
		for _, file := range batch.Files {
			staged[file.FileID] = path.Join(batch.BaiduStagingPath, file.Name)
		}
		if faults.inject("stage") {
			partial := make(map[string]string, len(staged)/2)
			for _, file := range batch.Files[:len(batch.Files)/2] {
				partial[file.FileID] = staged[file.FileID]
			}
			if err := store.RecordStagedFiles(ctx, taskID, batch.BatchID, partial); err != nil {
				return StageSummary{}, err
			}
			return StageSummary{}, errors.New("synthetic partial staging outage")
		}
		if err := store.RecordStagedFiles(ctx, taskID, batch.BatchID, staged); err != nil {
			return StageSummary{}, err
		}
		if err := store.CompleteBatch(ctx, taskID, batch.BatchID); err != nil {
			return StageSummary{}, err
		}
		return StageSummary{FilesStaged: len(batch.Files)}, observeWatermark()
	}

	downloadPass := func(context.Context, string) (download.Summary, error) {
		files, err := store.DownloadCandidates(ctx, taskID, 64)
		if err != nil {
			return download.Summary{}, err
		}
		reserved, err := store.ReservedCacheBytes(ctx)
		if err != nil {
			return download.Summary{}, err
		}
		var summary download.Summary
		for _, file := range files {
			if file.Status == state.FileBaiduStaged && reserved+file.Size > watermark {
				summary.PausedByWatermark = true
				break
			}
			cachePath := path.Join("cache", taskID, file.FileID+".bin")
			if file.Status == state.FileBaiduStaged {
				if err := store.StartDownload(ctx, taskID, file.FileID, path.Join("cache", taskID, file.FileID+".part")); err != nil {
					return summary, err
				}
				reserved += file.Size
				if faults.inject("download") {
					interruptedDownloads[file.FileID] = true
					return summary, errors.New("synthetic ranged download outage")
				}
			} else if interruptedDownloads[file.FileID] {
				resumedDownloads++
				delete(interruptedDownloads, file.FileID)
			}
			if err := store.MarkLocalReady(ctx, taskID, file.FileID, cachePath); err != nil {
				return summary, err
			}
			summary.FilesReady++
			summary.BytesReady += file.Size
		}
		return summary, observeWatermark()
	}

	drivePass := func(context.Context, string) (DriveSummary, error) {
		files, err := store.DriveUploadCandidates(ctx, taskID, 64)
		if err != nil {
			return DriveSummary{}, err
		}
		var summary DriveSummary
		for _, file := range files {
			driveID := "drive-" + file.FileID
			if file.Status == state.FileLocalReady {
				if err := store.StartDriveUpload(ctx, taskID, file.FileID); err != nil {
					return summary, err
				}
				if err := store.RecordDriveUploaded(ctx, taskID, file.FileID, driveID); err != nil {
					return summary, err
				}
				if faults.inject("drive") {
					return summary, errors.New("synthetic Drive post-commit outage")
				}
			}
			if err := store.MarkDriveVerified(ctx, taskID, file.FileID, driveID); err != nil {
				return summary, err
			}
			verified[file.FileID] = true
			summary.FilesVerified++
			summary.BytesVerified += file.Size
		}
		return summary, observeWatermark()
	}

	cleanupPass := func(context.Context, string) (cleanupengine.Summary, error) {
		batches, err := store.CleanupCandidates(ctx, taskID, 1)
		if err != nil {
			return cleanupengine.Summary{}, err
		}
		if len(batches) == 0 {
			candidate, err := store.TaskRootCleanupCandidate(ctx, taskID)
			if err != nil || !candidate {
				return cleanupengine.Summary{}, err
			}
			object, err := store.AuthorizeTaskRootCleanup(ctx, taskID)
			if err != nil {
				return cleanupengine.Summary{}, err
			}
			if object.ObjectPath == unrelatedSentinel || !strings.HasPrefix(object.ObjectPath, "/BaiduDriveMover/"+taskID) {
				return cleanupengine.Summary{}, fmt.Errorf("unsafe task-root cleanup target %q", object.ObjectPath)
			}
			if err := store.MarkBaiduTaskRootCleanupDone(ctx, taskID); err != nil {
				return cleanupengine.Summary{}, err
			}
			return cleanupengine.Summary{TaskRootDone: true}, nil
		}
		batch, err := store.AuthorizeBatchCleanup(ctx, taskID, batches[0].BatchID)
		if err != nil {
			return cleanupengine.Summary{}, err
		}
		var bytesFreed int64
		for i, file := range batch.Files {
			if !verified[file.FileID] {
				return cleanupengine.Summary{}, fmt.Errorf("cleanup selected file %s before Drive verification", file.FileID)
			}
			object := batch.LocalObjects[i]
			if object.ObjectPath == unrelatedSentinel || !strings.HasPrefix(object.ObjectPath, "cache/"+taskID+"/") {
				return cleanupengine.Summary{}, fmt.Errorf("unsafe local cleanup target %q", object.ObjectPath)
			}
			if object.CleanedAt == "" {
				if err := store.MarkLocalCacheCleanupDone(ctx, taskID, file.FileID); err != nil {
					return cleanupengine.Summary{}, err
				}
				if i == 0 && faults.inject("cleanup") {
					return cleanupengine.Summary{}, errors.New("synthetic cleanup persistence outage")
				}
			}
			bytesFreed += file.Size
		}
		if batch.BaiduObject.ObjectPath == unrelatedSentinel || !strings.HasPrefix(batch.BaiduObject.ObjectPath, "/BaiduDriveMover/"+taskID+"/") {
			return cleanupengine.Summary{}, fmt.Errorf("unsafe Baidu cleanup target %q", batch.BaiduObject.ObjectPath)
		}
		if batch.BaiduObject.CleanedAt == "" {
			if err := store.MarkBaiduBatchCleanupDone(ctx, taskID, batch.BatchID); err != nil {
				return cleanupengine.Summary{}, err
			}
		}
		if err := store.CompleteBatchCleanup(ctx, taskID, batch.BatchID); err != nil {
			return cleanupengine.Summary{}, err
		}
		if err := observeWatermark(); err != nil {
			return cleanupengine.Summary{}, err
		}
		return cleanupengine.Summary{BatchesDone: 1, FilesDone: len(batch.Files), BytesFreed: bytesFreed}, nil
	}

	for {
		runner := &PipelineRunner{
			State: store, Cleanup: cleanupPass, Drive: drivePass, Download: downloadPass, Stage: stagePass,
			MaxCacheBytes: watermark, MaxPasses: 20000,
		}
		_, err := runner.Run(ctx, taskID)
		if err == nil {
			break
		}
		if errors.Is(err, ErrPipelineNoProgress) || errors.Is(err, ErrPipelinePermanentFailed) || errors.Is(err, ErrPipelinePassLimit) {
			t.Fatalf("prolonged migration reached terminal error: %v", err)
		}
		restarts++
		if restarts > 32 {
			t.Fatalf("prolonged migration failed to converge after %d restarts: %v", restarts, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store = openProlongedStore(t, dbPath)
	}

	progress, err := store.PipelineProgress(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Complete() || progress.Done != fileCount || len(verified) != fileCount {
		t.Fatalf("final progress=%+v verified=%d", progress, len(verified))
	}
	if maxReserved > watermark || resumedDownloads == 0 || restarts != 12 {
		t.Fatalf("watermark=%d resumed=%d restarts=%d", maxReserved, resumedDownloads, restarts)
	}
	for stage, calls := range faults.calls {
		if calls < 60 && stage != "stage" {
			t.Fatalf("stage %s was not exercised long enough: %d calls", stage, calls)
		}
		if len(faults.at[stage]) != 0 {
			t.Fatalf("stage %s did not consume fault schedule: %v", stage, faults.at[stage])
		}
	}
	storedDirectories, err := store.DriveDirectories(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedDirectories) != len(directories) {
		t.Fatalf("directory count=%d want=%d", len(storedDirectories), len(directories))
	}
	for _, directory := range storedDirectories {
		if directory.DriveID == "" {
			t.Fatalf("directory lost Drive identity: %+v", directory)
		}
	}
}

func openProlongedStore(t *testing.T, dbPath string) *state.Store {
	t.Helper()
	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
