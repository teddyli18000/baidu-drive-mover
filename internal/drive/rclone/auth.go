package rclone

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxConfigBytes = int64(4 << 20)

type RemotePolicy struct {
	Type  string
	Scope string
}

// EnsureDriveRemote creates the private tool-owned rclone remote when missing.
// The OAuth flow is intentionally delegated to rclone's local-browser flow,
// but the scope and config location remain fixed by this application.
func (c *Client) EnsureDriveRemote(ctx context.Context) (bool, error) {
	if c == nil || c.layout == nil {
		return false, fmt.Errorf("rclone client is not configured")
	}
	listed, err := c.driveRemoteListed(ctx)
	if err != nil {
		return false, err
	}
	policy, found, err := c.readDriveRemotePolicy()
	if err != nil {
		return false, err
	}
	if listed != found {
		return false, fmt.Errorf("rclone Drive remote/config state is inconsistent")
	}
	if listed {
		if err := validateDriveRemotePolicy(policy); err != nil {
			return false, err
		}
		return false, nil
	}

	if err := c.runSensitive(ctx, "config", []string{
		"create", RemoteName, "drive",
		"scope", DriveOAuthScope,
		"config_is_local", "true",
		"--no-output",
	}); err != nil {
		return false, err
	}

	listed, err = c.driveRemoteListed(ctx)
	if err != nil {
		return false, err
	}
	policy, found, err = c.readDriveRemotePolicy()
	if err != nil {
		return false, err
	}
	if !listed || !found {
		return false, fmt.Errorf("rclone OAuth completed without a durable Drive remote")
	}
	if err := validateDriveRemotePolicy(policy); err != nil {
		return false, err
	}
	return true, nil
}

// ReauthorizeDriveRemote refreshes OAuth credentials without printing config or
// token material. It reasserts the least-privilege scope during the refresh.
func (c *Client) ReauthorizeDriveRemote(ctx context.Context) error {
	if c == nil || c.layout == nil {
		return fmt.Errorf("rclone client is not configured")
	}
	listed, err := c.driveRemoteListed(ctx)
	if err != nil {
		return err
	}
	policy, found, err := c.readDriveRemotePolicy()
	if err != nil {
		return err
	}
	if !listed || !found {
		return fmt.Errorf("rclone Drive remote is not configured")
	}
	if err := validateDriveRemotePolicy(policy); err != nil {
		return err
	}
	if err := c.runSensitive(ctx, "config", []string{
		"update", RemoteName,
		"scope", DriveOAuthScope,
		"config_is_local", "true",
		"--no-output",
	}); err != nil {
		return err
	}
	policy, found, err = c.readDriveRemotePolicy()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("rclone Drive remote disappeared after reauthorization")
	}
	return validateDriveRemotePolicy(policy)
}

func (c *Client) driveRemoteListed(ctx context.Context) (bool, error) {
	result, err := c.RunBase(ctx, "listremotes", "--name", RemoteName, "--exact")
	if err != nil {
		return false, fmt.Errorf("list rclone Drive remote: %w", err)
	}
	found := false
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line != RemoteName+":" {
			return false, fmt.Errorf("unexpected rclone remote listing output")
		}
		if found {
			return false, fmt.Errorf("duplicate rclone Drive remote listing")
		}
		found = true
	}
	return found, nil
}

func validateDriveRemotePolicy(policy RemotePolicy) error {
	if policy.Type != "drive" {
		return fmt.Errorf("rclone remote %q has unexpected type %q", RemoteName, policy.Type)
	}
	if policy.Scope != DriveOAuthScope {
		return fmt.Errorf("rclone remote %q must use OAuth scope %q", RemoteName, DriveOAuthScope)
	}
	return nil
}

func (c *Client) readDriveRemotePolicy() (RemotePolicy, bool, error) {
	file, err := c.layout.OpenTempFile("auth/rclone.conf", os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return RemotePolicy{}, false, nil
	}
	if err != nil {
		return RemotePolicy{}, false, fmt.Errorf("open rclone config for policy check: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return RemotePolicy{}, false, fmt.Errorf("stat rclone config: %w", err)
	}
	if info.Size() > maxConfigBytes {
		return RemotePolicy{}, false, fmt.Errorf("rclone config exceeds size limit")
	}
	return parseDriveRemotePolicy(io.LimitReader(file, maxConfigBytes+1))
}

func parseDriveRemotePolicy(reader io.Reader) (RemotePolicy, bool, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	inTarget := false
	foundSection := false
	var policy RemotePolicy
	seenType := false
	seenScope := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(line[1 : len(line)-1])
			inTarget = section == RemoteName
			if inTarget {
				if foundSection {
					return RemotePolicy{}, false, fmt.Errorf("duplicate rclone remote section %q", RemoteName)
				}
				foundSection = true
			}
			continue
		}
		if !inTarget {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return RemotePolicy{}, false, fmt.Errorf("malformed rclone config entry in tool remote")
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "type":
			if seenType {
				return RemotePolicy{}, false, fmt.Errorf("duplicate rclone remote type")
			}
			seenType = true
			policy.Type = value
		case "scope":
			if seenScope {
				return RemotePolicy{}, false, fmt.Errorf("duplicate rclone remote scope")
			}
			seenScope = true
			policy.Scope = value
		}
	}
	if err := scanner.Err(); err != nil {
		return RemotePolicy{}, false, fmt.Errorf("read rclone config policy: %w", err)
	}
	if !foundSection {
		return RemotePolicy{}, false, nil
	}
	if !seenType || !seenScope {
		return RemotePolicy{}, false, fmt.Errorf("rclone Drive remote policy is incomplete")
	}
	return policy, true, nil
}
