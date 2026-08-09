package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type driveTestClient struct {
	ensureCreated bool
	probeCalls    int
	reauthCalls   int
	rootName      string
	rootID        string
	calls         []string
	authOnce      bool
}

func (c *driveTestClient) EnsureDriveRemote(context.Context) (bool, error) {
	return c.ensureCreated, nil
}

func (c *driveTestClient) ProbeDrive(context.Context) error {
	c.probeCalls++
	if c.authOnce && c.probeCalls == 1 {
		return rclone.ErrDriveAuthRequired
	}
	return nil
}

func (c *driveTestClient) ReauthorizeDriveRemote(context.Context) error {
	c.reauthCalls++
	return nil
}

func (c *driveTestClient) RunBase(_ context.Context, command string, args ...string) (rclone.Result, error) {
	c.calls = append(c.calls, "base "+command+" "+strings.Join(args, " "))
	switch command {
	case "lsjson":
		if c.rootID == "" {
			return rclone.Result{Stdout: "[]"}, nil
		}
		return rclone.Result{Stdout: `[{"ID":"` + c.rootID + `","Name":"` + c.rootName + `","IsDir":true}]`}, nil
	case "mkdir":
		if len(args) != 1 || !strings.HasPrefix(args[0], rclone.RemoteName+":") {
			return rclone.Result{}, errors.New("unexpected mkdir target")
		}
		c.rootName = strings.TrimPrefix(args[0], rclone.RemoteName+":")
		c.rootID = "root-test"
		return rclone.Result{}, nil
	default:
		return rclone.Result{}, errors.New("unexpected base command")
	}
}

func (c *driveTestClient) RunTask(_ context.Context, rootID, command string, args ...string) (rclone.Result, error) {
	c.calls = append(c.calls, "task["+rootID+"] "+command+" "+strings.Join(args, " "))
	if rootID == "" || rootID != c.rootID {
		return rclone.Result{}, errors.New("wrong task root")
	}
	return rclone.Result{}, errors.New("no task command expected for empty manifest")
}

func TestDriveRunnerReauthorizesOnceAndPersistsTaskRoot(t *testing.T) {
	layout, store := newDriveRunnerFixture(t, "task-drive")
	client := &driveTestClient{ensureCreated: true, authOnce: true}
	provisionCalls := 0
	var output strings.Builder

	runner := &DriveRunner{
		Layout:     layout,
		Store:      store,
		Output:     &output,
		Process:    noOpProcessRunner{},
		HTTPClient: noOpHTTPDoer{},
		Provision: func(context.Context, *runtimepath.Layout, rclone.Runner, rclone.HTTPDoer) error {
			provisionCalls++
			return nil
		},
		NewClient: func(*runtimepath.Layout, rclone.Runner) (DriveClient, error) {
			return client, nil
		},
	}

	summary, err := runner.Run(context.Background(), "task-drive")
	if err != nil {
		t.Fatal(err)
	}
	if provisionCalls != 1 {
		t.Fatalf("provision calls=%d want=1", provisionCalls)
	}
	if client.probeCalls != 2 || client.reauthCalls != 1 {
		t.Fatalf("probe=%d reauth=%d", client.probeCalls, client.reauthCalls)
	}
	if summary.RootID != "root-test" || summary.FilesVerified != 0 || summary.BytesVerified != 0 {
		t.Fatalf("unexpected Drive summary: %+v", summary)
	}
	task, err := store.GetTask(context.Background(), "task-drive")
	if err != nil {
		t.Fatal(err)
	}
	if task.DriveRootID != "root-test" || task.DriveRootName != "BaiduDriveMover-task-drive" || task.Status != state.TaskPaused {
		t.Fatalf("unexpected persisted task: %+v", task)
	}
	if !strings.Contains(output.String(), "drive.file") {
		t.Fatalf("expected least-privilege success message, output=%q", output.String())
	}
	for _, call := range client.calls {
		lower := strings.ToLower(call)
		for _, forbidden := range []string{" delete ", " purge ", " sync ", " move ", " cleanup "} {
			if strings.Contains(" "+lower+" ", forbidden) {
				t.Fatalf("destructive Drive command observed: %q", call)
			}
		}
	}
}

func TestDriveRunnerBlocksOnProvisionFailureWithoutRemoteWrites(t *testing.T) {
	layout, store := newDriveRunnerFixture(t, "task-provision-fail")
	clientCreated := false
	runner := &DriveRunner{
		Layout:     layout,
		Store:      store,
		Output:     io.Discard,
		Process:    noOpProcessRunner{},
		HTTPClient: noOpHTTPDoer{},
		Provision: func(context.Context, *runtimepath.Layout, rclone.Runner, rclone.HTTPDoer) error {
			return errors.New("synthetic helper hash failure")
		},
		NewClient: func(*runtimepath.Layout, rclone.Runner) (DriveClient, error) {
			clientCreated = true
			return &driveTestClient{}, nil
		},
	}
	if _, err := runner.Run(context.Background(), "task-provision-fail"); err == nil {
		t.Fatal("expected provision failure")
	}
	if clientCreated {
		t.Fatal("Drive client was created after helper provisioning failed")
	}
	task, err := store.GetTask(context.Background(), "task-provision-fail")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != state.TaskBlocked || task.DriveRootID != "" {
		t.Fatalf("unexpected failure state: %+v", task)
	}
}

func TestDriveRunnerCancellationPausesTask(t *testing.T) {
	layout, store := newDriveRunnerFixture(t, "task-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &DriveRunner{
		Layout:     layout,
		Store:      store,
		Output:     io.Discard,
		Process:    noOpProcessRunner{},
		HTTPClient: noOpHTTPDoer{},
		Provision: func(ctx context.Context, _ *runtimepath.Layout, _ rclone.Runner, _ rclone.HTTPDoer) error {
			return ctx.Err()
		},
	}
	if _, err := runner.Run(ctx, "task-cancel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context.Canceled", err)
	}
	task, err := store.GetTask(context.Background(), "task-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != state.TaskPaused {
		t.Fatalf("task status=%s want=%s", task.Status, state.TaskPaused)
	}
}

type noOpProcessRunner struct{}

func (noOpProcessRunner) Run(context.Context, string, []string, []string) (rclone.Result, error) {
	return rclone.Result{}, nil
}

type noOpHTTPDoer struct{}

func (noOpHTTPDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("network must not be used in orchestration tests")
}

func newDriveRunnerFixture(t *testing.T, taskID string) (*runtimepath.Layout, *state.Store) {
	t.Helper()
	layout, err := runtimepath.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(layout.Temp, "drive-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateTask(context.Background(), state.Task{
		ID:       taskID,
		ShareURL: "https://pan.baidu.com/s/1Synthetic",
		Status:   state.TaskPaused,
	}); err != nil {
		t.Fatal(err)
	}
	return layout, store
}
