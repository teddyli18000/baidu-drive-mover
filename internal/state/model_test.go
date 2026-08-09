package state

import "testing"

func TestValidateFileTransition(t *testing.T) {
	valid := [][2]FileStatus{
		{FileDiscovered, FilePlanned},
		{FileBaiduStaged, FileDownloading},
		{FileDriveUploaded, FileDriveVerified},
		{FileDriveVerified, FileCleanupPending},
		{FileCleanupPending, FileDone},
		{FileFailedRetryable, FileDriveUploading},
	}
	for _, pair := range valid {
		if err := ValidateFileTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("expected valid transition %s -> %s: %v", pair[0], pair[1], err)
		}
	}
	invalid := [][2]FileStatus{
		{FileDiscovered, FileDone},
		{FileDownloading, FileDone},
		{FileDone, FileDownloading},
		{FileFailedPermanent, FilePlanned},
	}
	for _, pair := range invalid {
		if err := ValidateFileTransition(pair[0], pair[1]); err == nil {
			t.Fatalf("expected invalid transition %s -> %s", pair[0], pair[1])
		}
	}
}
