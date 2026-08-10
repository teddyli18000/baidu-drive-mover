package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	cleanupengine "github.com/teddyli18000/baidu-drive-mover/internal/cleanup"
	"github.com/teddyli18000/baidu-drive-mover/internal/download"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

const prolongedFileCount = 5000

type prolongedFile struct {
	status       state.FileStatus
	size         int64
	verifiedEver bool
}

type prolongedPipelineState struct {
	files       []prolongedFile
	taskStatus  state.TaskStatus
	lastError   string
	driveRoot   bool
	baiduRoot   bool
	baiduClean  bool
	maxReserved int64
}

func newProlongedPipelineState() *prolongedPipelineState {
	files := make([]prolongedFile, prolongedFileCount)
	for i := range files {
		files[i] = prolongedFile{status: state.FilePlanned, size: int64(512 + (i%31)*97)}
	}
	return &prolongedPipelineState{files: files, baiduRoot: true}
}

func (s *prolongedPipelineState) PipelineProgress(context.Context, string) (state.PipelineProgress, error) {
	var p state.PipelineProgress
	p.Total = len(s.files)
	for _, file := range s.files {
		switch file.status {
		case state.FileDiscovered:
			p.Discovered++
		case state.FilePlanned:
			p.Planned++
		case state.FileBaiduStaging:
			p.BaiduStaging++
		case state.FileBaiduStaged:
			p.BaiduStaged++
		case state.FileDownloading:
			p.Downloading++
		case state.FileLocalReady:
			p.LocalReady++
			p.ReservedCache += file.size
		case state.FileDriveUploading:
			p.DriveUploading++
			p.ReservedCache += file.size
		case state.FileDriveUploaded:
			p.DriveUploaded++
			p.ReservedCache += file.size
		case state.FileDriveVerified:
			p.DriveVerified++
			p.ReservedCache += file.size
		case state.FileCleanupPending:
			p.CleanupPending++
			p.ReservedCache += file.size
		case state.FileDone:
			p.Done++
		case state.FileFailedRetryable:
			p.FailedRetryable++
		case state.FileFailedPermanent:
			p.FailedPermanent++
		}
	}
	p.DriveRootReady = s.driveRoot
	p.BaiduTaskRootRegistered = s.baiduRoot
	p.BaiduTaskRootCleaned = s.baiduClean
	if p.ReservedCache > s.maxReserved {
		s.maxReserved = p.ReservedCache
	}
	return p, nil
}

func (s *prolongedPipelineState) UpdateTaskStatus(_ context.Context, _ string, status state.TaskStatus, lastError string) error {
	s.taskStatus = status
	s.lastError = lastError
	return nil
}

type prolongedFailures struct {
	stage    map[int]bool
	download map[int]bool
	drive    map[int]bool
	cleanup  map[int]bool
	calls    map[string]int
}

func newProlongedFailures() *prolongedFailures {
	return &prolongedFailures{
		stage:    map[int]bool{3: true, 17: true, 41: true},
		download: map[int]bool{5: true, 23: true, 47: true},
		drive:    map[int]bool{7: true, 29: true, 53: true},
		cleanup:  map[int]bool{11: true, 31: true, 59: true},
		calls:    make(map[string]int),
	}
}

func (f *prolongedFailures) maybe(stage string, schedule map[int]bool) error {
	f.calls[stage]++
	if schedule[f.calls[stage]] {
		delete(schedule, f.calls[stage])
		return fmt.Errorf("synthetic transient %s outage", stage)
	}
	return nil
}

func TestProlongedMigrationConvergesAcrossOutagesRestartsAndWatermarkPressure(t *testing.T) {
	const watermark = int64(64 << 10)
	ps := newProlongedPipelineState()
	failures := newProlongedFailures()

	cleanupPass := func(context.Context, string) (cleanupengine.Summary, error) {
		if err := failures.maybe("cleanup", failures.cleanup); err != nil {
			return cleanupengine.Summary{}, err
		}
		var summary cleanupengine.Summary
		for i := range ps.files {
			if ps.files[i].status != state.FileDriveVerified {
				continue
			}
			if !ps.files[i].verifiedEver {
				t.Fatalf("cleanup selected unverified file %d", i)
			}
			ps.files[i].status = state.FileDone
			summary.FilesDone++
			summary.BytesFreed += ps.files[i].size
		}
		allDone := true
		for _, file := range ps.files {
			if file.status != state.FileDone {
				allDone = false
				break
			}
		}
		if allDone {
			ps.baiduClean = true
			summary.TaskRootDone = true
		}
		if summary.FilesDone > 0 {
			summary.BatchesDone = 1
		}
		return summary, nil
	}

	drivePass := func(context.Context, string) (DriveSummary, error) {
		if err := failures.maybe("drive", failures.drive); err != nil {
			return DriveSummary{}, err
		}
		ps.driveRoot = true
		var summary DriveSummary
		for i := range ps.files {
			if ps.files[i].status != state.FileLocalReady {
				continue
			}
			ps.files[i].status = state.FileDriveVerified
			ps.files[i].verifiedEver = true
			summary.FilesVerified++
			summary.BytesVerified += ps.files[i].size
		}
		return summary, nil
	}

	downloadPass := func(context.Context, string) (download.Summary, error) {
		if err := failures.maybe("download", failures.download); err != nil {
			return download.Summary{}, err
		}
		progress, _ := ps.PipelineProgress(context.Background(), "task")
		available := watermark - progress.ReservedCache
		var summary download.Summary
		for i := range ps.files {
			if ps.files[i].status != state.FileBaiduStaged || ps.files[i].size > available {
				continue
			}
			ps.files[i].status = state.FileLocalReady
			available -= ps.files[i].size
			summary.FilesReady++
			summary.BytesReady += ps.files[i].size
		}
		return summary, nil
	}

	stagePass := func(_ context.Context, _ string, maxBytes int64) (StageSummary, error) {
		if err := failures.maybe("stage", failures.stage); err != nil {
			return StageSummary{}, err
		}
		var used int64
		var summary StageSummary
		for i := range ps.files {
			if ps.files[i].status != state.FilePlanned {
				continue
			}
			if used+ps.files[i].size > maxBytes {
				continue
			}
			ps.files[i].status = state.FileBaiduStaged
			used += ps.files[i].size
			summary.FilesStaged++
			if used >= maxBytes {
				break
			}
		}
		return summary, nil
	}

	restarts := 0
	for {
		runner := &PipelineRunner{
			State:         ps,
			Cleanup:       cleanupPass,
			Drive:         drivePass,
			Download:      downloadPass,
			Stage:         stagePass,
			MaxCacheBytes: watermark,
			MaxPasses:     20000,
		}
		_, err := runner.Run(context.Background(), "task")
		if err == nil {
			break
		}
		if errors.Is(err, ErrPipelineNoProgress) || errors.Is(err, ErrPipelinePermanentFailed) {
			t.Fatalf("prolonged migration hit non-transient terminal error: %v", err)
		}
		restarts++
		if restarts > 32 {
			t.Fatalf("migration failed to converge after %d restarts: %v", restarts, err)
		}
	}

	progress, err := ps.PipelineProgress(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Complete() || progress.Done != prolongedFileCount {
		t.Fatalf("final progress not complete: %+v", progress)
	}
	if ps.taskStatus != state.TaskCompleted {
		t.Fatalf("task status=%s want COMPLETED", ps.taskStatus)
	}
	if ps.maxReserved > watermark {
		t.Fatalf("cache reservation exceeded watermark: max=%d watermark=%d", ps.maxReserved, watermark)
	}
	for i, file := range ps.files {
		if file.status != state.FileDone || !file.verifiedEver {
			t.Fatalf("file %d final state=%s verified=%v", i, file.status, file.verifiedEver)
		}
	}
	if restarts == 0 {
		t.Fatal("fault schedule failed to exercise restart path")
	}
	for stage, calls := range failures.calls {
		if calls <= 1 {
			t.Fatalf("stage %s was not exercised repeatedly: %d calls", stage, calls)
		}
	}
}
