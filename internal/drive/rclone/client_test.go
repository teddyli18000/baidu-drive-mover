package rclone

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
)

type capturedCall struct {
	Executable string
	Args       []string
	Env        []string
}

type captureRunner struct {
	calls  []capturedCall
	result Result
	err    error
}

func (r *captureRunner) Run(_ context.Context, executable string, args []string, env []string) (Result, error) {
	r.calls = append(r.calls, capturedCall{
		Executable: executable,
		Args:       append([]string(nil), args...),
		Env:        append([]string(nil), env...),
	})
	return r.result, r.err
}

func TestTaskCommandAlwaysCarriesRuntimeSandboxAndRootID(t *testing.T) {
	layout := testClientLayout(t)
	runner := &captureRunner{}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RunTask(context.Background(), "root-123", "copyto", `C:\opaque\101.bin`, "bdm-drive:docs/a.txt"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls=%d want=1", len(runner.calls))
	}
	call := runner.calls[0]
	if filepath.Clean(call.Executable) != filepath.Clean(layout.RcloneExe) {
		t.Fatalf("executable=%q want=%q", call.Executable, layout.RcloneExe)
	}
	assertArgPair(t, call.Args, "--config", layout.RcloneConfig)
	assertArgPair(t, call.Args, "--cache-dir", layout.RcloneCache)
	assertArgPair(t, call.Args, "--temp-dir", layout.RcloneTemp)
	assertArgPair(t, call.Args, "--drive-root-folder-id", "root-123")
	assertEnvValue(t, call.Env, "TEMP", layout.RcloneTemp)
	assertEnvValue(t, call.Env, "TMP", layout.RcloneTemp)
	if call.Args[0] != "copyto" {
		t.Fatalf("first argv=%q want=copyto", call.Args[0])
	}
}

func TestBaseCommandCannotSmuggleSandboxOrRootOverrides(t *testing.T) {
	layout := testClientLayout(t)
	runner := &captureRunner{}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	bad := [][]string{
		{"--config", `C:\outside.conf`},
		{"--cache-dir=C:\outside"},
		{"--temp-dir", `C:\outside`},
		{"--drive-root-folder-id=other-root"},
	}
	for _, args := range bad {
		if _, err := client.RunBase(context.Background(), "lsjson", args...); err == nil {
			t.Fatalf("expected reserved argument rejection for %v", args)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner invoked %d times after rejected arguments", len(runner.calls))
	}
}

func TestDangerousRcloneCommandsAreNotExposed(t *testing.T) {
	layout := testClientLayout(t)
	runner := &captureRunner{}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"sync", "move", "moveto", "delete", "purge", "cleanup", "rmdir", "rmdirs"} {
		if _, err := client.RunBase(context.Background(), command, "bdm-drive:anywhere"); err == nil {
			t.Fatalf("base command %q unexpectedly allowed", command)
		}
		if _, err := client.RunTask(context.Background(), "root-1", command, "bdm-drive:anywhere"); err == nil {
			t.Fatalf("task command %q unexpectedly allowed", command)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatal("dangerous command reached process runner")
	}
}

func TestTaskCommandRequiresRootID(t *testing.T) {
	layout := testClientLayout(t)
	runner := &captureRunner{}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RunTask(context.Background(), "", "lsjson", "bdm-drive:"); err == nil {
		t.Fatal("expected missing task root ID rejection")
	}
	if len(runner.calls) != 0 {
		t.Fatal("missing-root command reached runner")
	}
}

func TestCapturedProcessOutputIsBoundedWithoutShortWrite(t *testing.T) {
	buffer := &cappedBuffer{limit: 5}
	input := []byte("0123456789")
	n, err := buffer.Write(input)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(input) {
		t.Fatalf("write count=%d want=%d", n, len(input))
	}
	if got := buffer.String(); !strings.Contains(got, "01234") || !strings.Contains(got, "truncated") {
		t.Fatalf("unexpected bounded output %q", got)
	}
}

func TestMergeEnvironmentReplacesTempCaseInsensitively(t *testing.T) {
	merged := mergeEnvironment([]string{"Path=X", "Temp=outside", "TMP=outside"}, []string{"TEMP=inside", "TMP=inside"})
	assertEnvValue(t, merged, "TEMP", "inside")
	assertEnvValue(t, merged, "TMP", "inside")
	countTemp := 0
	for _, item := range merged {
		if strings.EqualFold(envKey(item), "TEMP") {
			countTemp++
		}
	}
	if countTemp != 1 {
		t.Fatalf("TEMP entries=%d want=1: %v", countTemp, merged)
	}
}

func testClientLayout(t *testing.T) *runtimepath.Layout {
	t.Helper()
	layout, err := runtimepath.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	return layout
}

func assertArgPair(t *testing.T, args []string, key, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return
		}
	}
	t.Fatalf("missing argv pair %q %q in %v", key, value, args)
}

func assertEnvValue(t *testing.T, env []string, key, value string) {
	t.Helper()
	for _, item := range env {
		if strings.EqualFold(envKey(item), key) {
			if item[len(envKey(item))+1:] != value {
				t.Fatalf("env %s=%q want=%q", key, item[len(envKey(item))+1:], value)
			}
			return
		}
	}
	t.Fatalf("missing env %s in %v", key, env)
}
