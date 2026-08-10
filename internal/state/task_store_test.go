package state

import (
	"context"
	"path/filepath"
	"testing"
)

func TestListResumableTasksAndCompleteScan(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	for _, task := range []Task{
		{ID: "completed", ShareURL: "https://pan.baidu.com/s/1Done", Status: TaskCompleted, ScanCompleted: true},
		{ID: "failed", ShareURL: "https://pan.baidu.com/s/1Failed", Status: TaskFailed},
		{ID: "partial", ShareURL: "https://pan.baidu.com/s/1Partial", Status: TaskPaused},
	} {
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	tasks, err := store.ListResumableTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "partial" || tasks[0].ScanCompleted {
		t.Fatalf("resumable tasks=%+v", tasks)
	}
	if err := store.UpdateTaskStatus(ctx, "partial", TaskScanning, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteTaskScan(ctx, "partial"); err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(ctx, "partial")
	if err != nil {
		t.Fatal(err)
	}
	if !task.ScanCompleted || task.Status != TaskPaused {
		t.Fatalf("completed scan task=%+v", task)
	}
	if err := store.CompleteTaskScan(ctx, "partial"); err == nil {
		t.Fatal("expected duplicate scan completion to fail")
	}
}
