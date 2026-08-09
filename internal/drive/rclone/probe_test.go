package rclone

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type probeRunner struct {
	result Result
	err    error
	calls  []capturedCall
}

func (r *probeRunner) Run(_ context.Context, executable string, args []string, env []string) (Result, error) {
	r.calls = append(r.calls, capturedCall{Executable: executable, Args: append([]string(nil), args...), Env: append([]string(nil), env...)})
	return r.result, r.err
}

func TestProbeDriveAcceptsValidMachineMetadata(t *testing.T) {
	layout := testClientLayout(t)
	runner := &probeRunner{result: Result{Stdout: `[]`}}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ProbeDrive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || runner.calls[0].Args[0] != "lsjson" {
		t.Fatalf("unexpected probe calls: %+v", runner.calls)
	}
}

func TestProbeDriveClassifiesExpiredOAuthWithoutLeakingRawDiagnostic(t *testing.T) {
	layout := testClientLayout(t)
	runner := &probeRunner{
		result: Result{Stderr: "SECRET refresh token failed: invalid_grant status code: 401"},
		err:    errors.New("exit status 1"),
	}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	err = client.ProbeDrive(context.Background())
	if !errors.Is(err, ErrDriveAuthRequired) {
		t.Fatalf("error=%v want ErrDriveAuthRequired", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("raw OAuth diagnostic leaked: %q", err.Error())
	}
}

func TestProbeDriveSanitizesNonAuthFailure(t *testing.T) {
	layout := testClientLayout(t)
	runner := &probeRunner{
		result: Result{Stderr: "SECRET DNS transport details"},
		err:    errors.New("network down"),
	}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	err = client.ProbeDrive(context.Background())
	if err == nil || errors.Is(err, ErrDriveAuthRequired) {
		t.Fatalf("unexpected error classification: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "DNS transport details") {
		t.Fatalf("raw connectivity diagnostic leaked: %q", err.Error())
	}
}

func TestProbeDriveRejectsInvalidSuccessPayload(t *testing.T) {
	layout := testClientLayout(t)
	runner := &probeRunner{result: Result{Stdout: "not-json"}}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ProbeDrive(context.Background()); err == nil {
		t.Fatal("expected invalid metadata rejection")
	}
}
