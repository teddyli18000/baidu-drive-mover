package rclone

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Runner interface {
	Run(ctx context.Context, executable string, args []string, env []string) (Result, error)
}

type OSRunner struct {
	MaxOutputBytes int
}

func (r OSRunner) Run(ctx context.Context, executable string, args []string, env []string) (Result, error) {
	limit := r.MaxOutputBytes
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	stdout := &cappedBuffer{limit: limit}
	stderr := &cappedBuffer{limit: limit}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = sandboxEnvironment(os.Environ(), env)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	}
	return result, err
}

type cappedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		take := len(p)
		if take > remaining {
			take = remaining
		}
		b.buf = append(b.buf, p[:take]...)
	}
	if len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	if !b.truncated {
		return string(b.buf)
	}
	return string(b.buf) + "\n[output truncated]"
}

func sandboxEnvironment(base, overrides []string) []string {
	filtered := make([]string, 0, len(base)+len(overrides))
	for _, existing := range base {
		key := envKey(existing)
		if key == "" || strings.HasPrefix(strings.ToUpper(key), "RCLONE_") {
			continue
		}
		filtered = append(filtered, existing)
	}
	return mergeEnvironment(filtered, overrides)
}

func mergeEnvironment(base, overrides []string) []string {
	result := append([]string(nil), base...)
	for _, override := range overrides {
		key := envKey(override)
		if key == "" || strings.HasPrefix(strings.ToUpper(key), "RCLONE_") {
			continue
		}
		filtered := result[:0]
		for _, existing := range result {
			if !strings.EqualFold(envKey(existing), key) {
				filtered = append(filtered, existing)
			}
		}
		result = append(filtered, override)
	}
	return result
}

func envKey(value string) string {
	index := strings.IndexByte(value, '=')
	if index <= 0 {
		return ""
	}
	return value[:index]
}
