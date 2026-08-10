package runtime

import (
	"fmt"
	"os"
)

// InstanceLock prevents concurrent processes from sharing one runtime tree.
// The lock is released by the OS when the process exits, including crashes.
type InstanceLock struct {
	file *os.File
}

func (l *Layout) AcquireInstanceLock() (*InstanceLock, error) {
	if l == nil {
		return nil, fmt.Errorf("runtime layout is nil")
	}
	file, err := l.OpenTempFile("instance.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another BaiduDriveMover instance is already using this folder: %w", err)
	}
	return &InstanceLock{file: file}, nil
}

func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
