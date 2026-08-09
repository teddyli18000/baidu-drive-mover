package state

import (
	"context"
	"fmt"
)

type PipelineProgress struct {
	Total            int
	Discovered       int
	Planned          int
	BaiduStaging     int
	BaiduStaged      int
	Downloading      int
	LocalReady       int
	DriveUploading   int
	DriveUploaded    int
	DriveVerified    int
	CleanupPending   int
	Done             int
	FailedRetryable  int
	FailedPermanent  int
	ReservedCache    int64
	DriveRootReady   bool
}

func (p PipelineProgress) Complete() bool {
	if p.Total == 0 {
		return p.DriveRootReady
	}
	return p.Done == p.Total
}

func (p PipelineProgress) HasDriveWork() bool {
	return p.LocalReady+p.DriveUploading+p.DriveUploaded > 0
}

func (p PipelineProgress) HasDownloadWork() bool {
	return p.BaiduStaged+p.Downloading > 0
}

func (p PipelineProgress) HasStageWork() bool {
	return p.Discovered+p.Planned+p.BaiduStaging+ p.FailedRetryable > 0
}

func (p PipelineProgress) HasCleanupWork() bool {
	return p.DriveVerified+p.CleanupPending > 0
}

func (s *Store) PipelineProgress(ctx context.Context, taskID string) (PipelineProgress, error) {
	var progress PipelineProgress
	rows, err := s.db.QueryContext(ctx, `
SELECT status, COUNT(*)
FROM files
WHERE task_id = ?
GROUP BY status`, taskID)
	if err != nil {
		return PipelineProgress{}, fmt.Errorf("query pipeline progress: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status FileStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return PipelineProgress{}, fmt.Errorf("scan pipeline progress: %w", err)
		}
		progress.Total += count
		switch status {
		case FileDiscovered:
			progress.Discovered = count
		case FilePlanned:
			progress.Planned = count
		case FileBaiduStaging:
			progress.BaiduStaging = count
		case FileBaiduStaged:
			progress.BaiduStaged = count
		case FileDownloading:
			progress.Downloading = count
		case FileLocalReady:
			progress.LocalReady = count
		case FileDriveUploading:
			progress.DriveUploading = count
		case FileDriveUploaded:
			progress.DriveUploaded = count
		case FileDriveVerified:
			progress.DriveVerified = count
		case FileCleanupPending:
			progress.CleanupPending = count
		case FileDone:
			progress.Done = count
		case FileFailedRetryable:
			progress.FailedRetryable = count
		case FileFailedPermanent:
			progress.FailedPermanent = count
		}
	}
	if err := rows.Err(); err != nil {
		return PipelineProgress{}, fmt.Errorf("iterate pipeline progress: %w", err)
	}
	reserved, err := s.ReservedCacheBytes(ctx)
	if err != nil {
		return PipelineProgress{}, err
	}
	progress.ReservedCache = reserved
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return PipelineProgress{}, err
	}
	progress.DriveRootReady = task.DriveRootID != "" && task.DriveRootName != ""
	return progress, nil
}
