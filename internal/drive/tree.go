package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

type TreeState interface {
	GetTask(ctx context.Context, id string) (state.Task, error)
	SetTaskDriveRoot(ctx context.Context, taskID, rootName, rootID string) error
	DriveDirectories(ctx context.Context, taskID string) ([]state.Directory, error)
	RecordDirectoryDriveID(ctx context.Context, taskID, logicalPath, driveID string) error
}

type TreeRemote interface {
	RunBase(ctx context.Context, command string, args ...string) (rclone.Result, error)
	RunTask(ctx context.Context, rootID, command string, args ...string) (rclone.Result, error)
}

type TreeBuilder struct {
	State  TreeState
	Remote TreeRemote
}

type remoteItem struct {
	ID     string            `json:"ID"`
	Name   string            `json:"Name"`
	Path   string            `json:"Path"`
	IsDir  bool              `json:"IsDir"`
	Size   int64             `json:"Size"`
	Hashes map[string]string `json:"Hashes"`
}

func (b *TreeBuilder) Ensure(ctx context.Context, taskID string) (string, error) {
	if b == nil || b.State == nil || b.Remote == nil {
		return "", fmt.Errorf("Drive tree builder is not configured")
	}
	task, err := b.State.GetTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	rootID := strings.TrimSpace(task.DriveRootID)
	rootName := strings.TrimSpace(task.DriveRootName)
	if rootName == "" {
		if err := validateTaskComponent(taskID); err != nil {
			return "", err
		}
		rootName = "BaiduDriveMover-" + taskID
	}
	if rootID == "" {
		rootID, err = b.ensureTaskRoot(ctx, taskID, rootName)
		if err != nil {
			return "", err
		}
	} else if task.DriveRootName == "" {
		return "", fmt.Errorf("Drive root ID exists without its durable root name")
	}

	directories, err := b.State.DriveDirectories(ctx, taskID)
	if err != nil {
		return "", err
	}
	for _, directory := range directories {
		if err := b.ensureDirectory(ctx, taskID, rootID, directory); err != nil {
			return "", err
		}
	}
	return rootID, nil
}

func (b *TreeBuilder) ensureTaskRoot(ctx context.Context, taskID, rootName string) (string, error) {
	matches, err := b.listNamedDirectoriesBase(ctx, "/", rootName)
	if err != nil {
		return "", err
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("Drive task root %q is ambiguous: %d same-name folders", rootName, len(matches))
	}
	if len(matches) == 0 {
		target := rclone.RemoteName + ":" + rootName
		if _, err := b.Remote.RunBase(ctx, "mkdir", target); err != nil {
			return "", fmt.Errorf("create Drive task root: %w", err)
		}
		matches, err = b.listNamedDirectoriesBase(ctx, "/", rootName)
		if err != nil {
			return "", err
		}
	}
	if len(matches) != 1 || strings.TrimSpace(matches[0].ID) == "" {
		return "", fmt.Errorf("Drive task root %q could not be reconciled unambiguously", rootName)
	}
	if err := b.State.SetTaskDriveRoot(ctx, taskID, rootName, matches[0].ID); err != nil {
		return "", err
	}
	return matches[0].ID, nil
}

func (b *TreeBuilder) ensureDirectory(ctx context.Context, taskID, rootID string, directory state.Directory) error {
	logical, err := validateLogicalPath(directory.LogicalPath, false)
	if err != nil {
		return err
	}
	parent := path.Dir(logical)
	if parent == "." {
		parent = "/"
	}
	name := path.Base(logical)
	matches, err := b.listNamedDirectoriesTask(ctx, rootID, parent, name)
	if err != nil {
		return err
	}
	if len(matches) > 1 {
		return fmt.Errorf("Drive directory %q is ambiguous: %d same-name folders", logical, len(matches))
	}
	persisted := strings.TrimSpace(directory.DriveID)
	if persisted != "" {
		if len(matches) != 1 || matches[0].ID != persisted {
			return fmt.Errorf("Drive directory %q no longer matches persisted ID %q", logical, persisted)
		}
		return nil
	}
	if len(matches) == 0 {
		target, err := remotePath(logical)
		if err != nil {
			return err
		}
		if _, err := b.Remote.RunTask(ctx, rootID, "mkdir", target); err != nil {
			return fmt.Errorf("create Drive directory %q: %w", logical, err)
		}
		matches, err = b.listNamedDirectoriesTask(ctx, rootID, parent, name)
		if err != nil {
			return err
		}
	}
	if len(matches) != 1 || strings.TrimSpace(matches[0].ID) == "" {
		return fmt.Errorf("Drive directory %q could not be reconciled unambiguously", logical)
	}
	return b.State.RecordDirectoryDriveID(ctx, taskID, logical, matches[0].ID)
}

func (b *TreeBuilder) listNamedDirectoriesBase(ctx context.Context, logicalParent, name string) ([]remoteItem, error) {
	remote, err := remotePath(logicalParent)
	if err != nil {
		return nil, err
	}
	result, err := b.Remote.RunBase(ctx, "lsjson", remote, "--no-modtime", "--no-mimetype")
	if err != nil {
		return nil, fmt.Errorf("list Drive parent %q: %w", logicalParent, err)
	}
	return exactDirectoryMatches(result.Stdout, name)
}

func (b *TreeBuilder) listNamedDirectoriesTask(ctx context.Context, rootID, logicalParent, name string) ([]remoteItem, error) {
	remote, err := remotePath(logicalParent)
	if err != nil {
		return nil, err
	}
	result, err := b.Remote.RunTask(ctx, rootID, "lsjson", remote, "--no-modtime", "--no-mimetype")
	if err != nil {
		return nil, fmt.Errorf("list task Drive parent %q: %w", logicalParent, err)
	}
	return exactDirectoryMatches(result.Stdout, name)
}

func exactDirectoryMatches(raw, name string) ([]remoteItem, error) {
	var items []remoteItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("parse rclone directory listing: %w", err)
	}
	var matches []remoteItem
	for _, item := range items {
		if item.Name != name {
			continue
		}
		if !item.IsDir || strings.TrimSpace(item.ID) == "" {
			return nil, fmt.Errorf("same-name Drive object is not the expected directory")
		}
		matches = append(matches, item)
	}
	return matches, nil
}

func validateTaskComponent(taskID string) error {
	if taskID == "" || strings.ContainsAny(taskID, "/\\\x00") || taskID == "." || taskID == ".." {
		return fmt.Errorf("unsafe task ID for Drive root: %q", taskID)
	}
	return nil
}
