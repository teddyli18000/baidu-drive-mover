package app

import "fmt"

type StagingBatchTooLargeError struct {
	BatchID string
	Bytes   int64
	Limit   int64
}

func (e *StagingBatchTooLargeError) Error() string {
	return fmt.Sprintf("staging batch %s requires %d bytes but the global cache limit is %d", e.BatchID, e.Bytes, e.Limit)
}
