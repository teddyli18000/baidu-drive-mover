package baidu

import (
	"errors"
	"fmt"
)

var (
	ErrAuthRequired         = errors.New("Baidu authentication required")
	ErrPasswordRequired     = errors.New("share extraction code required")
	ErrWrongPassword        = errors.New("wrong share extraction code")
	ErrShareExpired         = errors.New("Baidu share is unavailable or expired")
	ErrVerificationRequired = errors.New("Baidu security verification required")
)

type RemoteError struct {
	Operation string
	Errno     int
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("Baidu %s failed with errno %d", e.Operation, e.Errno)
}
