package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

func TestSelectResumableTask(t *testing.T) {
	tasks := []state.Task{{ID: "latest"}, {ID: "older"}}
	selected, err := selectResumableTask(tasks, "", false)
	if err != nil || selected == nil || selected.ID != "latest" {
		t.Fatalf("default selection=%+v err=%v", selected, err)
	}
	selected, err = selectResumableTask(tasks, "older", false)
	if err != nil || selected == nil || selected.ID != "older" {
		t.Fatalf("explicit selection=%+v err=%v", selected, err)
	}
	if _, err := selectResumableTask(tasks, "missing", false); err == nil {
		t.Fatal("expected missing task rejection")
	}
	selected, err = selectResumableTask(tasks, "", true)
	if err != nil || selected != nil {
		t.Fatalf("new selection=%+v err=%v", selected, err)
	}
}

func TestPrintResumableTasks(t *testing.T) {
	var output bytes.Buffer
	printResumableTasks(&output, []state.Task{{
		ID: "task-1", Status: state.TaskPaused, ScanCompleted: true,
		ShareURL: "https://pan.baidu.com/s/1Sanitized",
	}})
	text := output.String()
	for _, want := range []string{"task-1", "PAUSED", "扫描完成", "1Sanitized"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q missing %q", text, want)
		}
	}
}

func TestIsResumableTaskRejectsTerminalStates(t *testing.T) {
	for _, status := range []state.TaskStatus{state.TaskNew, state.TaskAuthRequired, state.TaskScanning, state.TaskRunning, state.TaskPaused, state.TaskBlocked} {
		if !isResumableTask(state.Task{Status: status}) {
			t.Fatalf("status %s should be resumable", status)
		}
	}
	for _, status := range []state.TaskStatus{state.TaskCompleted, state.TaskFailed} {
		if isResumableTask(state.Task{Status: status}) {
			t.Fatalf("terminal status %s should not be resumable", status)
		}
	}
}
