package rclone

import (
	"context"
	"fmt"
	"strings"

	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
)

type Client struct {
	layout *runtimepath.Layout
	runner Runner
	exe    string
	config string
	cache  string
	temp   string
}

func NewClient(layout *runtimepath.Layout, runner Runner) (*Client, error) {
	if layout == nil {
		return nil, fmt.Errorf("runtime layout is nil")
	}
	if runner == nil {
		return nil, fmt.Errorf("rclone process runner is nil")
	}
	if err := layout.Ensure(); err != nil {
		return nil, err
	}
	exe, err := layout.ResolveTempRelative("tools/rclone/rclone.exe")
	if err != nil {
		return nil, err
	}
	config, err := layout.ResolveTempRelative("auth/rclone.conf")
	if err != nil {
		return nil, err
	}
	cache, err := layout.ResolveTempRelative("rclone-cache")
	if err != nil {
		return nil, err
	}
	temp, err := layout.ResolveTempRelative("rclone-tmp")
	if err != nil {
		return nil, err
	}
	return &Client{layout: layout, runner: runner, exe: exe, config: config, cache: cache, temp: temp}, nil
}

func (c *Client) CheckVersion(ctx context.Context) error {
	if c == nil || c.layout == nil {
		return fmt.Errorf("rclone client is nil")
	}
	if err := VerifyInstalledExecutable(c.layout); err != nil {
		return err
	}
	result, err := c.run(ctx, "version", false, "", nil)
	if err != nil {
		return fmt.Errorf("run pinned rclone version check: %w", err)
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		output = strings.TrimSpace(result.Stderr)
	}
	if !strings.Contains(output, "rclone "+Version) {
		return fmt.Errorf("unexpected rclone version output")
	}
	return nil
}

func (c *Client) RunBase(ctx context.Context, command string, args ...string) (Result, error) {
	if !allowedBaseCommand(command) {
		return Result{}, fmt.Errorf("rclone base command %q is not allowed", command)
	}
	return c.run(ctx, command, false, "", args)
}

func (c *Client) RunTask(ctx context.Context, rootID, command string, args ...string) (Result, error) {
	rootID = strings.TrimSpace(rootID)
	if rootID == "" || strings.ContainsRune(rootID, '\x00') {
		return Result{}, fmt.Errorf("Drive task root ID is required")
	}
	if !allowedTaskCommand(command) {
		return Result{}, fmt.Errorf("rclone task command %q is not allowed", command)
	}
	return c.run(ctx, command, true, rootID, args)
}

// runSensitive executes a tightly constructed command whose stdout/stderr may
// contain OAuth state or other authentication details. Callers get only a
// sanitized success/failure signal; captured process output is discarded.
func (c *Client) runSensitive(ctx context.Context, command string, args []string) error {
	_, err := c.run(ctx, command, false, "", args)
	if err != nil {
		return fmt.Errorf("rclone authentication command failed")
	}
	return nil
}

func (c *Client) run(ctx context.Context, command string, taskScoped bool, rootID string, args []string) (Result, error) {
	if c == nil || c.runner == nil {
		return Result{}, fmt.Errorf("rclone client is not configured")
	}
	if err := rejectReservedArguments(args); err != nil {
		return Result{}, err
	}
	argv := make([]string, 0, 1+len(args)+10)
	argv = append(argv, command)
	argv = append(argv, args...)
	argv = append(argv,
		"--config", c.config,
		"--cache-dir", c.cache,
		"--temp-dir", c.temp,
	)
	if taskScoped {
		argv = append(argv, "--drive-root-folder-id", rootID)
	}
	env := []string{
		"TEMP=" + c.temp,
		"TMP=" + c.temp,
	}
	return c.runner.Run(ctx, c.exe, argv, env)
}

func allowedBaseCommand(command string) bool {
	switch command {
	case "listremotes", "mkdir", "lsjson":
		return true
	default:
		return false
	}
}

func allowedTaskCommand(command string) bool {
	switch command {
	case "mkdir", "lsjson", "copyto":
		return true
	default:
		return false
	}
}

func rejectReservedArguments(args []string) error {
	reserved := []string{"--config", "--cache-dir", "--temp-dir", "--drive-root-folder-id"}
	for _, arg := range args {
		for _, prefix := range reserved {
			if arg == prefix || strings.HasPrefix(arg, prefix+"=") {
				return fmt.Errorf("rclone argument %q is reserved by the sandbox", prefix)
			}
		}
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("rclone argument contains NUL")
		}
	}
	return nil
}
