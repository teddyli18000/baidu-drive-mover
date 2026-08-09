package rclone

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

type authRunner struct {
	layoutConfig string
	calls        []capturedCall
	listPresent  bool
}

func (r *authRunner) Run(_ context.Context, executable string, args []string, env []string) (Result, error) {
	r.calls = append(r.calls, capturedCall{Executable: executable, Args: append([]string(nil), args...), Env: append([]string(nil), env...)})
	if len(args) == 0 {
		return Result{}, fmt.Errorf("missing command")
	}
	switch args[0] {
	case "listremotes":
		if r.listPresent {
			return Result{Stdout: RemoteName + ":\n"}, nil
		}
		return Result{}, nil
	case "config":
		if len(args) < 2 {
			return Result{}, fmt.Errorf("missing config subcommand")
		}
		switch args[1] {
		case "create":
			if !containsSequence(args, "scope", DriveOAuthScope) || !containsSequence(args, "config_is_local", "true") || !containsArg(args, "--no-output") {
				return Result{}, fmt.Errorf("unsafe config create arguments: %v", args)
			}
			if err := os.WriteFile(r.layoutConfig, []byte("["+RemoteName+"]\ntype = drive\nscope = "+DriveOAuthScope+"\ntoken = SECRET\n"), 0o600); err != nil {
				return Result{}, err
			}
			r.listPresent = true
			return Result{Stdout: "SECRET SHOULD BE DISCARDED"}, nil
		case "update":
			if !containsSequence(args, "scope", DriveOAuthScope) || !containsSequence(args, "config_is_local", "true") || !containsArg(args, "--no-output") {
				return Result{}, fmt.Errorf("unsafe config update arguments: %v", args)
			}
			return Result{Stdout: "SECRET SHOULD BE DISCARDED"}, nil
		default:
			return Result{}, fmt.Errorf("unexpected config subcommand %q", args[1])
		}
	default:
		return Result{}, fmt.Errorf("unexpected command %q", args[0])
	}
}

func TestEnsureDriveRemoteCreatesExplicitDriveFileScope(t *testing.T) {
	layout := testClientLayout(t)
	runner := &authRunner{layoutConfig: layout.RcloneConfig}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.EnsureDriveRemote(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected remote creation")
	}
	policy, found, err := client.readDriveRemotePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if !found || policy.Type != "drive" || policy.Scope != DriveOAuthScope {
		t.Fatalf("unexpected policy found=%v policy=%+v", found, policy)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.Args, " "), "show") || strings.Contains(strings.Join(call.Args, " "), "dump") {
			t.Fatalf("credential-display command reached runner: %v", call.Args)
		}
	}
}

func TestEnsureDriveRemoteRejectsBroadOrTamperedScope(t *testing.T) {
	layout := testClientLayout(t)
	if err := os.WriteFile(layout.RcloneConfig, []byte("["+RemoteName+"]\ntype = drive\nscope = drive\ntoken = SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &authRunner{layoutConfig: layout.RcloneConfig, listPresent: true}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.EnsureDriveRemote(context.Background()); err == nil {
		t.Fatal("expected broad scope rejection")
	}
	if len(runner.calls) != 1 || runner.calls[0].Args[0] != "listremotes" {
		t.Fatalf("tampered policy should block before config mutation: %+v", runner.calls)
	}
}

func TestEnsureDriveRemoteRequiresListingAndConfigToAgree(t *testing.T) {
	layout := testClientLayout(t)
	runner := &authRunner{layoutConfig: layout.RcloneConfig, listPresent: true}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.EnsureDriveRemote(context.Background()); err == nil {
		t.Fatal("expected listed/config inconsistency rejection")
	}
}

func TestReauthorizeDriveRemoteReassertsScopeWithoutConfigOutput(t *testing.T) {
	layout := testClientLayout(t)
	if err := os.WriteFile(layout.RcloneConfig, []byte("["+RemoteName+"]\ntype = drive\nscope = "+DriveOAuthScope+"\ntoken = SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &authRunner{layoutConfig: layout.RcloneConfig, listPresent: true}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ReauthorizeDriveRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawUpdate bool
	for _, call := range runner.calls {
		if len(call.Args) >= 2 && call.Args[0] == "config" && call.Args[1] == "update" {
			sawUpdate = true
			if !containsSequence(call.Args, "scope", DriveOAuthScope) || !containsArg(call.Args, "--no-output") {
				t.Fatalf("reauthorize failed to pin scope/output: %v", call.Args)
			}
		}
	}
	if !sawUpdate {
		t.Fatal("expected config update reauthorization")
	}
}

func TestParseDriveRemotePolicyRejectsDuplicateSensitivePolicyKeys(t *testing.T) {
	_, _, err := parseDriveRemotePolicy(strings.NewReader("[" + RemoteName + "]\ntype = drive\nscope = drive.file\nscope = drive\n"))
	if err == nil {
		t.Fatal("expected duplicate scope rejection")
	}
}

func containsSequence(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

func containsArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}
