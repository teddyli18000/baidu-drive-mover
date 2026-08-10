package state

import (
	"fmt"
	"time"
)

type TaskStatus string

const (
	TaskNew          TaskStatus = "NEW"
	TaskAuthRequired TaskStatus = "AUTH_REQUIRED"
	TaskScanning     TaskStatus = "SCANNING"
	TaskRunning      TaskStatus = "RUNNING"
	TaskPaused       TaskStatus = "PAUSED"
	TaskBlocked      TaskStatus = "BLOCKED"
	TaskCompleted    TaskStatus = "COMPLETED"
	TaskFailed       TaskStatus = "FAILED"
)

type FileStatus string

const (
	FileDiscovered      FileStatus = "DISCOVERED"
	FilePlanned         FileStatus = "PLANNED"
	FileBaiduStaging    FileStatus = "BAIDU_STAGING"
	FileBaiduStaged     FileStatus = "BAIDU_STAGED"
	FileDownloading     FileStatus = "DOWNLOADING"
	FileLocalReady      FileStatus = "LOCAL_READY"
	FileDriveUploading  FileStatus = "DRIVE_UPLOADING"
	FileDriveUploaded   FileStatus = "DRIVE_UPLOADED"
	FileDriveVerified   FileStatus = "DRIVE_VERIFIED"
	FileCleanupPending  FileStatus = "CLEANUP_PENDING"
	FileDone            FileStatus = "DONE"
	FileFailedRetryable FileStatus = "FAILED_RETRYABLE"
	FileFailedPermanent FileStatus = "FAILED_PERMANENT"
)

type BatchStatus string

const (
	BatchPending         BatchStatus = "PENDING"
	BatchStaging         BatchStatus = "STAGING"
	BatchStaged          BatchStatus = "STAGED"
	BatchFailedRetryable BatchStatus = "FAILED_RETRYABLE"
	BatchFailedPermanent BatchStatus = "FAILED_PERMANENT"
)

type Task struct {
	ID             string
	ShareURL       string
	ExtractionCode string
	Status         TaskStatus
	ScanCompleted  bool
	DriveRootID    string
	DriveRootName  string
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Directory struct {
	TaskID      string
	LogicalPath string
	DriveID     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type File struct {
	TaskID           string
	FileID           string
	LogicalPath      string
	ParentPath       string
	Name             string
	Size             int64
	MD5              string
	Status           FileStatus
	BaiduStagingPath string
	LocalCachePath   string
	DriveID          string
	RetryCount       int
	LastError        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Batch struct {
	TaskID           string
	BatchID          string
	LogicalParent    string
	BaiduStagingPath string
	Status           BatchStatus
	FileCount        int
	TotalBytes       int64
	RetryCount       int
	LastError        string
	Files            []File
}

var allowedFileTransitions = map[FileStatus]map[FileStatus]bool{
	FileDiscovered:     {FilePlanned: true, FileFailedPermanent: true},
	FilePlanned:        {FileBaiduStaging: true, FileFailedRetryable: true, FileFailedPermanent: true},
	FileBaiduStaging:   {FileBaiduStaged: true, FileFailedRetryable: true, FileFailedPermanent: true},
	FileBaiduStaged:    {FileDownloading: true, FileFailedRetryable: true, FileFailedPermanent: true},
	FileDownloading:    {FileLocalReady: true, FileFailedRetryable: true, FileFailedPermanent: true},
	FileLocalReady:     {FileDriveUploading: true, FileDriveUploaded: true, FileFailedRetryable: true, FileFailedPermanent: true},
	FileDriveUploading: {FileDriveUploaded: true, FileFailedRetryable: true, FileFailedPermanent: true},
	FileDriveUploaded:  {FileDriveVerified: true, FileFailedRetryable: true, FileFailedPermanent: true},
	FileDriveVerified:  {FileCleanupPending: true, FileDone: true},
	FileCleanupPending: {FileDone: true, FileFailedRetryable: true},
	FileFailedRetryable: {
		FilePlanned: true, FileBaiduStaging: true, FileBaiduStaged: true, FileDownloading: true,
		FileLocalReady: true, FileDriveUploading: true, FileDriveUploaded: true,
		FileDriveVerified: true, FileCleanupPending: true, FileFailedPermanent: true,
	},
	FileDone:            {},
	FileFailedPermanent: {},
}

func ValidateFileTransition(from, to FileStatus) error {
	if from == to {
		return nil
	}
	allowed, ok := allowedFileTransitions[from]
	if !ok || !allowed[to] {
		return fmt.Errorf("invalid file state transition %s -> %s", from, to)
	}
	return nil
}
