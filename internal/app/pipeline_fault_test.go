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

func TestPipelineStopsAtFailingStageWithoutRunningLaterWork(t *testing.T) {
	tests := []struct {
		name     string
		progress state.PipelineProgress
		failAt   string
		want     []string
	}{
		{name: "cleanup", progress: state.PipelineProgress{Total: 1, CleanupPending: 1, DriveRootReady: true, BaiduTaskRootRegistered: true}, failAt: "cleanup", want: []string{"cleanup"}},
		{name: "drive", progress: state.PipelineProgress{Total: 1, LocalReady: 1}, failAt: "drive", want: []string{"drive"}},
		{name: "download", progress: state.PipelineProgress{Total: 1, BaiduStaged: 1}, failAt: "download", want: []string{"download"}},
		{name: "stage", progress: state.PipelineProgress{Total: 1, Planned: 1}, failAt: "stage", want: []string{"stage"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakePipelineState{progress: tc.progress}
			var calls []string
			boom := errors.New("synthetic " + tc.failAt + " failure")
			runner := &PipelineRunner{
				State:         st,
				MaxCacheBytes: 100,
				Cleanup: func(context.Context, string) (cleanupengine.Summary, error) {
					calls = append(calls, "cleanup")
					if tc.failAt == "cleanup" {
						return cleanupengine.Summary{}, boom
					}
					return cleanupengine.Summary{}, nil
				},
				Drive: func(context.Context, string) (DriveSummary, error) {
					calls = append(calls, "drive")
					if tc.failAt == "drive" {
						return DriveSummary{}, boom
					}
					return DriveSummary{}, nil
				},
				Download: func(context.Context, string) (download.Summary, error) {
					calls = append(calls, "download")
					if tc.failAt == "download" {
						return download.Summary{}, boom
					}
					return download.Summary{}, nil
				},
				Stage: func(context.Context, string, int64) (StageSummary, error) {
					calls = append(calls, "stage")
					if tc.failAt == "stage" {
						return StageSummary{}, boom
					}
					return StageSummary{}, nil
				},
			}
			_, err := runner.Run(context.Background(), "task")
			if !errors.Is(err, boom) {
				t.Fatalf("expected %v, got %v", boom, err)
			}
			if !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("calls=%v want=%v", calls, tc.want)
			}
			if st.status != state.TaskBlocked {
				t.Fatalf("status=%s want BLOCKED", st.status)
			}
		})
	}
}

func TestPipelineStageCancellationPausesImmediately(t *testing.T) {
	st := &fakePipelineState{progress: state.PipelineProgress{Total: 1, Planned: 1}}
	var calls []string
	runner := &PipelineRunner{
		State:         st,
		MaxCacheBytes: 100,
		Cleanup:       func(context.Context, string) (cleanupengine.Summary, error) { return cleanupengine.Summary{}, nil },
		Drive:         func(context.Context, string) (DriveSummary, error) { return DriveSummary{}, nil },
		Download:      func(context.Context, string) (download.Summary, error) { return download.Summary{}, nil },
		Stage: func(context.Context, string, int64) (StageSummary, error) {
			calls = append(calls, "stage")
			return StageSummary{}, context.Canceled
		},
	}
	_, err := runner.Run(context.Background(), "task")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"stage"}) || st.status != state.TaskPaused {
		t.Fatalf("cancelled stage continued or wrong status: calls=%v status=%s", calls, st.status)
	}
}

func TestPipelinePermanentFailureStopsBeforeAnyStage(t *testing.T) {
	st := &fakePipelineState{progress: state.PipelineProgress{Total: 2, FailedPermanent: 1, Planned: 1}}
	calls := 0
	runner := &PipelineRunner{
		State:         st,
		MaxCacheBytes: 100,
		Cleanup: func(context.Context, string) (cleanupengine.Summary, error) {
			calls++
			return cleanupengine.Summary{}, nil
		},
		Drive:    func(context.Context, string) (DriveSummary, error) { calls++; return DriveSummary{}, nil },
		Download: func(context.Context, string) (download.Summary, error) { calls++; return download.Summary{}, nil },
		Stage:    func(context.Context, string, int64) (StageSummary, error) { calls++; return StageSummary{}, nil },
	}
	_, err := runner.Run(context.Background(), "task")
	if !errors.Is(err, ErrPipelinePermanentFailed) {
		t.Fatalf("expected permanent pipeline failure, got %v", err)
	}
	if calls != 0 || st.status != state.TaskFailed {
		t.Fatalf("permanent failure ran stages or wrong status: calls=%d status=%s", calls, st.status)
	}
}
