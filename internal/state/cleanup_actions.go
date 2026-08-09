package state

import "context"

func (s *Store) MarkLocalCacheCleanupDone(ctx context.Context, taskID, fileID string) error {
	return s.MarkCleanupObjectDone(ctx, taskID, ownedScopeLocalCacheFile, fileID)
}

func (s *Store) MarkBaiduBatchCleanupDone(ctx context.Context, taskID, batchID string) error {
	return s.MarkCleanupObjectDone(ctx, taskID, ownedScopeBaiduBatchDir, batchID)
}

func (s *Store) RecordLocalCacheCleanupFailure(ctx context.Context, taskID, fileID, message string) error {
	return s.RecordCleanupObjectFailure(ctx, taskID, ownedScopeLocalCacheFile, fileID, message)
}

func (s *Store) RecordBaiduBatchCleanupFailure(ctx context.Context, taskID, batchID, message string) error {
	return s.RecordCleanupObjectFailure(ctx, taskID, ownedScopeBaiduBatchDir, batchID, message)
}
