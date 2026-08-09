package rclone

import (
	"context"
	"testing"
)

func TestDriveRemoteListedUsesNativeListremotesWithoutUnsupportedFilters(t *testing.T) {
	layout := testClientLayout(t)
	runner := &captureRunner{result: Result{Stdout: RemoteName + ":\n"}}
	client, err := NewClient(layout, runner)
	if err != nil {
		t.Fatal(err)
	}
	found, err := client.driveRemoteListed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected tool remote")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls=%d want=1", len(runner.calls))
	}
	got := runner.calls[0].Args
	if len(got) < 7 || got[0] != "listremotes" {
		t.Fatalf("unexpected argv: %v", got)
	}
	for _, arg := range got[1:] {
		if arg == "--name" || arg == "--exact" {
			t.Fatalf("unsupported listremotes filter reached rclone: %v", got)
		}
	}
}
