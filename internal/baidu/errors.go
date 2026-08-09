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
	ErrQuotaExceeded        = errors.New("Baidu storage quota exceeded")
	ErrTransferConflict     = errors.New("Baidu staging transfer conflict")
)

type RemoteError struct {
	Operation string
	Errno     int
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("Baidu %s failed with errno %d", e.Operation, e.Errno)
}

type PCSRemoteError struct {
	Operation string
	Code      int
}

func (e *PCSRemoteError) Error() string {
	return fmt.Sprintf("Baidu PCS %s failed with error_code %d", e.Operation, e.Code)
}

type TransferLimitError struct {
	Target int
	Limit  int
}

func (e *TransferLimitError) Error() string {
	return fmt.Sprintf("Baidu transfer contains %d files but account limit is %d", e.Target, e.Limit)
}

type StagingConflictError struct {
	Name string
}

func (e *StagingConflictError) Error() string {
	return fmt.Sprintf("Baidu staging contains conflicting object %q", e.Name)
}
