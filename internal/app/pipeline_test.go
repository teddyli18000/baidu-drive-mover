package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	cleanupengine "github.com/teddyli18000/baidu-drive-mover/internal/cleanup"
	"github.com/teddyli18000/baidu-drive-mover/internal/download"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type fakePipelineState struct {
	progress state.PipelineProgress
	status   state.TaskStatus
	lastErr  string
}

func (s *fakePipelineState) PipelineProgress(context.Context, string) (state.PipelineProgress, error) {
	return s.progress, nil
}

func (s *fakePipelineState) UpdateTaskStatus(_ context.Context, _ string, status state.TaskStatus, lastError string) error {
	s.status = status
	s.lastErr = lastError
	return nil
}

func TestPipelineProgressesInBoundedStageDownloadDriveCleanupPasses(t *testing.T) {
	st := &fakePipelineState{progress: state.PipelineProgress{Total: 1, Planned: 1}}
	var calls []string
	runner := &PipelineRunner{
		State: st,
		MaxCacheBytes: 100,
		Cleanup: func(context.Context, string) (cleanupengine.Summary, error) {
			calls = append(calls, "cleanup")
			st.progress.CleanupPending--
			st.progress.Done++
			st.progress.ReservedCache = 0
			return cleanupengine.Summary{BatchesDone: 1, FilesDone: 1, BytesFreed: 10}, nil
		},
		Drive: func(context.Context, string) (DriveSummary, error) {
			calls = append(calls, "drive")
			st.progress.LocalReady--
			st.progress.DriveVerified++
			st.progress.DriveRootReady = true
			return DriveSummary{RootID: "root", FilesVerified: 1, BytesVerified: 10}, nil
		},
		Download: func(context.Context, string) (download.Summary, error) {
			calls = append(calls, "download")
			st.progress.BaiduStaged--
			st.progress.LocalReady++
			st.progress.ReservedCache = 10
			return download.Summary{FilesReady: 1, BytesReady: 10}, nil
		},
		Stage: func(_ context.Context, _ string, maxBytes int64) (StageSummary, error) {
			calls = append(calls, "stage")
			if maxBytes != 100 {
				t.Fatalf("stage max bytes=%d want=100", maxBytes)
			}
			st.progress.Planned--
			st.progress.BaiduStaged++
			return StageSummary{FilesStaged: 1}, nil
		},
	}
	summary, err := runner.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"stage", "download", "drive", "cleanup"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	if st.status != state.TaskCompleted || !summary.Final.Complete() || summary.Final.Done != 1 {
		t.Fatalf("unexpected completion status=%s summary=%+v", st.status, summary)
	}
}

func TestPipelinePrioritizesCleanupToReleaseWatermark(t *testing.T) {
	st := &fakePipelineState{progress: state.PipelineProgress{Total: 2, DriveVerified: 2, ReservedCache: 80, DriveRootReady: true}}
	var calls []string
	runner := &PipelineRunner{
		State: st,
		MaxCacheBytes: 100,
		Cleanup: func(context.Context, string) (cleanupengine.Summary, error) {
			calls = append(calls, "cleanup")
			st.progress.DriveVerified = 0
			st.progress.Done = 2
			st.progress.ReservedCache = 0
			return cleanupengine.Summary{BatchesDone: 1, FilesDone: 2, BytesFreed: 80}, nil
		},
		Drive: func(context.Context, string) (DriveSummary, error) {
			calls = append(calls, "drive")
			return DriveSummary{}, nil
		},
		Download: func(context.Context, string) (download.Summary, error) {
			calls = append(calls, "download")
			return download.Summary{}, nil
		},
		Stage: func(context.Context, string, int64) (StageSummary, error) {
			calls = append(calls, "stage")
			return StageSummary{}, nil
		},
	}
	if _, err := runner.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"cleanup"}) {
		t.Fatalf("cleanup was not exclusive priority: %v", calls)
	}
}

func TestPipelineEmptyShareStillInitializesDriveRoot(t *testing.T) {
	st := &fakePipelineState{}
	var calls []string
	runner := &PipelineRunner{
		State: st,
		MaxCacheBytes: 100,
		Cleanup: func(context.Context, string) (cleanupengine.Summary, error) { return cleanupengine.Summary{}, nil },
		Drive: func(context.Context, string) (DriveSummary, error) {
			calls = append(calls, "drive")
			st.progress.DriveRootReady = true
			return DriveSummary{RootID: "empty-root"}, nil
		},
		Download: func(context.Context, string) (download.Summary, error) { return download.Summary{}, nil },
		Stage: func(context.Context, string, int64) (StageSummary, error) { return StageSummary{}, nil },
	}
	if _, err := runner.Run(context.Background(), "empty"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"drive"}) || st.status != state.TaskCompleted {
		t.Fatalf("empty share was not completed through Drive root: calls=%v status=%s", calls, st.status)
	}
}

func TestPipelineStopsWhenDurableStateDoesNotChange(t *testing.T) {
	st := &fakePipelineState{progress: state.PipelineProgress{Total: 1, Planned: 1}}
	runner := &PipelineRunner{
		State: st,
		MaxCacheBytes: 100,
		Cleanup: func(context.Context, string) (cleanupengine.Summary, error) { return cleanupengine.Summary{}, nil },
		Drive: func(context.Context, string) (DriveSummary, error) { return DriveSummary{}, nil },
		Download: func(context.Context, string) (download.Summary, error) { return download.Summary{}, nil },
		Stage: func(context.Context, string, int64) (StageSummary, error) { return StageSummary{}, nil },
	}
	_, err := runner.Run(context.Background(), "stuck")
	if !errors.Is(err, ErrPipelineNoProgress) {
		t.Fatalf("expected ErrPipelineNoProgress, got %v", err)
	}
	if st.status != state.TaskBlocked {
		t.Fatalf("stuck pipeline status=%s want BLOCKED", st.status)
	}
}

func TestPipelineCancellationPausesWithoutRunningStages(t *testing.T) {
	st := &fakePipelineState{progress: state.PipelineProgress{Total: 1, Planned: 1}}
	calls := 0
	runner := &PipelineRunner{
		State: st,
		MaxCacheBytes: 100,
		Cleanup: func(context.Context, string) (cleanupengine.Summary, error) { calls++; return cleanupengine.Summary{}, nil },
		Drive: func(context.Context, string) (DriveSummary, error) { calls++; return DriveSummary{}, nil },
		Download: func(context.Context, string) (download.Summary, error) { calls++; return download.Summary{}, nil },
		Stage: func(context.Context, string, int64) (StageSummary, error) { calls++; return StageSummary{}, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.Run(ctx, "cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if calls != 0 || st.status != state.TaskPaused {
		t.Fatalf("cancelled pipeline ran work or wrong status: calls=%d status=%s", calls, st.status)
	}
}
