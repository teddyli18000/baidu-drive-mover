package drive

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/teddyli18000/baidu-drive-mover/internal/drive/rclone"
	runtimepath "github.com/teddyli18000/baidu-drive-mover/internal/runtime"
	"github.com/teddyli18000/baidu-drive-mover/internal/state"
)

var opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type UploadState interface {
	GetTask(ctx context.Context, id string) (state.Task, error)
	DriveUploadCandidates(ctx context.Context, taskID string, limit int) ([]state.File, error)
	StartDriveUpload(ctx context.Context, taskID, fileID string) error
	RecordDriveUploaded(ctx context.Context, taskID, fileID, driveID string) error
	MarkDriveVerified(ctx context.Context, taskID, fileID, driveID string) error
	RecordDriveFailure(ctx context.Context, taskID, fileID, message string, permanent bool) error
}

type UploadRemote interface {
	RunTask(ctx context.Context, rootID, command string, args ...string) (rclone.Result, error)
}

type Uploader struct {
	Layout     *runtimepath.Layout
	State      UploadState
	Remote     UploadRemote
	RetryDelay func(context.Context, time.Duration) error
}

const maxSafeUploadAttempts = 3

type UploadSummary struct {
	FilesVerified int
	BytesVerified int64
}

type uploadPermanentError struct{ err error }

func (e *uploadPermanentError) Error() string { return e.err.Error() }
func (e *uploadPermanentError) Unwrap() error { return e.err }

func uploadPermanent(err error) error {
	if err == nil {
		return nil
	}
	return &uploadPermanentError{err: err}
}

func (u *Uploader) Run(ctx context.Context, taskID string) (UploadSummary, error) {
	if u == nil || u.Layout == nil || u.State == nil || u.Remote == nil {
		return UploadSummary{}, fmt.Errorf("Drive uploader is not configured")
	}
	task, err := u.State.GetTask(ctx, taskID)
	if err != nil {
		return UploadSummary{}, err
	}
	rootID := strings.TrimSpace(task.DriveRootID)
	if rootID == "" || strings.TrimSpace(task.DriveRootName) == "" {
		return UploadSummary{}, fmt.Errorf("Drive task root is not initialized")
	}

	var summary UploadSummary
	for {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		files, err := u.State.DriveUploadCandidates(ctx, taskID, 64)
		if err != nil {
			return summary, err
		}
		if len(files) == 0 {
			return summary, nil
		}
		file := files[0]
		verified, err := u.uploadOne(ctx, rootID, file)
		if err != nil {
			var permanent *uploadPermanentError
			isPermanent := errors.As(err, &permanent)
			if recordErr := u.State.RecordDriveFailure(context.Background(), file.TaskID, file.FileID, err.Error(), isPermanent); recordErr != nil {
				return summary, fmt.Errorf("Drive upload failed; recording failure also failed: %w", recordErr)
			}
			return summary, err
		}
		if verified {
			summary.FilesVerified++
			summary.BytesVerified += file.Size
		}
	}
}

func (u *Uploader) uploadOne(ctx context.Context, rootID string, file state.File) (bool, error) {
	localPath, localMD5, err := u.verifyLocalCache(file)
	if err != nil {
		return false, uploadPermanent(err)
	}
	matches, err := u.listNamedFiles(ctx, rootID, file.ParentPath, file.Name)
	if err != nil {
		return false, err
	}
	if len(matches) > 1 {
		return false, uploadPermanent(fmt.Errorf("Drive file %q is ambiguous: %d same-name objects", file.LogicalPath, len(matches)))
	}
	if len(matches) == 1 {
		if err := verifyRemoteFile(matches[0], file.Size, localMD5); err != nil {
			return false, uploadPermanent(fmt.Errorf("existing Drive object conflicts with %q: %w", file.LogicalPath, err))
		}
		if file.DriveID != "" && file.DriveID != matches[0].ID {
			return false, uploadPermanent(fmt.Errorf("Drive object ID for %q changed from %q to %q", file.LogicalPath, file.DriveID, matches[0].ID))
		}
		if err := u.State.RecordDriveUploaded(ctx, file.TaskID, file.FileID, matches[0].ID); err != nil {
			return false, err
		}
		return u.verifyPersistedRemote(ctx, rootID, file, localMD5, matches[0].ID)
	}
	if file.DriveID != "" {
		return false, uploadPermanent(fmt.Errorf("persisted Drive object %q for %q is no longer present at its logical path", file.DriveID, file.LogicalPath))
	}
	if file.Status == state.FileLocalReady {
		if err := u.State.StartDriveUpload(ctx, file.TaskID, file.FileID); err != nil {
			return false, err
		}
	} else if file.Status != state.FileDriveUploading {
		return false, uploadPermanent(fmt.Errorf("file %q is not eligible for a new Drive upload from %s", file.LogicalPath, file.Status))
	}

	destination, err := remotePath(file.LogicalPath)
	if err != nil {
		return false, uploadPermanent(err)
	}
	var reconcileErr error
	for attempt := 1; ; attempt++ {
		_, copyErr := u.Remote.RunTask(ctx, rootID, "copyto", localPath, destination,
			"--ignore-existing", "--checksum", "--retries", "1", "--low-level-retries", "3")

		// Reconcile even when copyto reports an error. The remote commit may have
		// succeeded immediately before the local process observed a failure.
		matches, reconcileErr = u.listNamedFiles(ctx, rootID, file.ParentPath, file.Name)
		if reconcileErr != nil {
			if copyErr != nil {
				return false, fmt.Errorf("rclone upload failed and Drive reconciliation failed")
			}
			return false, reconcileErr
		}
		if len(matches) > 1 {
			return false, uploadPermanent(fmt.Errorf("Drive upload produced %d same-name objects for %q", len(matches), file.LogicalPath))
		}
		if len(matches) == 1 {
			break
		}
		if copyErr == nil {
			return false, fmt.Errorf("rclone upload returned success but no Drive object was observed")
		}
		if attempt >= maxSafeUploadAttempts {
			return false, fmt.Errorf("rclone upload failed before a matching Drive object could be proven")
		}
		if err := u.waitRetry(ctx, time.Duration(1<<(attempt-1))*time.Second); err != nil {
			return false, err
		}

		// Reconcile again after the delay. Only replay the write when Drive still
		// proves that no same-name destination object exists.
		matches, reconcileErr = u.listNamedFiles(ctx, rootID, file.ParentPath, file.Name)
		if reconcileErr != nil {
			return false, fmt.Errorf("Drive reconciliation before upload retry failed")
		}
		if len(matches) > 1 {
			return false, uploadPermanent(fmt.Errorf("Drive upload produced %d same-name objects for %q", len(matches), file.LogicalPath))
		}
		if len(matches) == 1 {
			break
		}
	}
	if err := verifyRemoteFile(matches[0], file.Size, localMD5); err != nil {
		return false, uploadPermanent(fmt.Errorf("Drive upload evidence for %q failed verification: %w", file.LogicalPath, err))
	}
	if err := u.State.RecordDriveUploaded(ctx, file.TaskID, file.FileID, matches[0].ID); err != nil {
		return false, err
	}
	return u.verifyPersistedRemote(ctx, rootID, file, localMD5, matches[0].ID)
}

func (u *Uploader) waitRetry(ctx context.Context, delay time.Duration) error {
	if u.RetryDelay != nil {
		return u.RetryDelay(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (u *Uploader) verifyPersistedRemote(ctx context.Context, rootID string, file state.File, initialLocalMD5, expectedID string) (bool, error) {
	_, currentLocalMD5, err := u.verifyLocalCache(file)
	if err != nil {
		return false, uploadPermanent(fmt.Errorf("revalidate local cache before Drive verification: %w", err))
	}
	if !strings.EqualFold(currentLocalMD5, initialLocalMD5) {
		return false, uploadPermanent(fmt.Errorf("local cache changed during Drive transfer for %q", file.LogicalPath))
	}
	matches, err := u.listNamedFiles(ctx, rootID, file.ParentPath, file.Name)
	if err != nil {
		return false, err
	}
	if len(matches) != 1 {
		return false, uploadPermanent(fmt.Errorf("independent Drive verification for %q found %d same-name objects", file.LogicalPath, len(matches)))
	}
	if matches[0].ID != expectedID {
		return false, uploadPermanent(fmt.Errorf("independent Drive verification ID changed for %q", file.LogicalPath))
	}
	if err := verifyRemoteFile(matches[0], file.Size, currentLocalMD5); err != nil {
		return false, uploadPermanent(fmt.Errorf("independent Drive verification failed for %q: %w", file.LogicalPath, err))
	}
	if err := u.State.MarkDriveVerified(ctx, file.TaskID, file.FileID, expectedID); err != nil {
		return false, err
	}
	return true, nil
}

func (u *Uploader) listNamedFiles(ctx context.Context, rootID, logicalParent, name string) ([]remoteItem, error) {
	parent, err := validateLogicalPath(logicalParent, true)
	if err != nil {
		return nil, uploadPermanent(err)
	}
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "/") {
		return nil, uploadPermanent(fmt.Errorf("invalid logical Drive filename %q", name))
	}
	remote, err := remotePath(parent)
	if err != nil {
		return nil, uploadPermanent(err)
	}
	result, err := u.Remote.RunTask(ctx, rootID, "lsjson", remote,
		"--hash", "--hash-type", "MD5", "--no-modtime", "--no-mimetype")
	if err != nil {
		return nil, fmt.Errorf("list Drive files under %q: %w", parent, err)
	}
	items, err := parseRemoteItems(result.Stdout)
	if err != nil {
		return nil, err
	}
	var matches []remoteItem
	for _, item := range items {
		if item.Name != name {
			continue
		}
		if item.IsDir || strings.TrimSpace(item.ID) == "" {
			return nil, uploadPermanent(fmt.Errorf("same-name Drive object is not the expected regular file for %q", name))
		}
		matches = append(matches, item)
	}
	return matches, nil
}

func parseRemoteItems(raw string) ([]remoteItem, error) {
	var items []remoteItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("parse rclone Drive listing: %w", err)
	}
	return items, nil
}

func verifyRemoteFile(item remoteItem, expectedSize int64, expectedMD5 string) error {
	if item.IsDir || strings.TrimSpace(item.ID) == "" {
		return fmt.Errorf("destination is not an identified regular file")
	}
	if item.Size != expectedSize {
		return fmt.Errorf("size=%d want=%d", item.Size, expectedSize)
	}
	remoteMD5 := hashValue(item.Hashes, "MD5")
	if remoteMD5 == "" {
		return fmt.Errorf("Drive MD5 is missing")
	}
	if !strings.EqualFold(remoteMD5, expectedMD5) {
		return fmt.Errorf("Drive MD5 mismatch")
	}
	return nil
}

func hashValue(hashes map[string]string, name string) string {
	for key, value := range hashes {
		if strings.EqualFold(key, name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (u *Uploader) verifyLocalCache(file state.File) (string, string, error) {
	if !opaqueIDPattern.MatchString(file.TaskID) || !opaqueIDPattern.MatchString(file.FileID) {
		return "", "", fmt.Errorf("unsafe opaque cache identity for %q", file.LogicalPath)
	}
	logical, err := validateLogicalPath(file.LogicalPath, false)
	if err != nil {
		return "", "", err
	}
	parent := path.Dir(logical)
	if parent == "." {
		parent = "/"
	}
	if file.ParentPath != parent || file.Name != path.Base(logical) {
		return "", "", fmt.Errorf("logical file metadata is inconsistent for %q", file.LogicalPath)
	}
	expectedRelative := path.Join("cache", file.TaskID, file.FileID+".bin")
	if file.LocalCachePath != expectedRelative {
		return "", "", fmt.Errorf("local cache path for %q is not the registered opaque path", file.LogicalPath)
	}
	full, err := u.Layout.ResolveTempRelative(expectedRelative)
	if err != nil {
		return "", "", err
	}
	cacheFile, err := u.Layout.OpenTempFile(expectedRelative, os.O_RDONLY, 0)
	if err != nil {
		return "", "", fmt.Errorf("open verified local cache for %q: %w", file.LogicalPath, err)
	}
	defer cacheFile.Close()
	info, err := cacheFile.Stat()
	if err != nil {
		return "", "", fmt.Errorf("stat verified local cache for %q: %w", file.LogicalPath, err)
	}
	if info.Size() != file.Size {
		return "", "", fmt.Errorf("local cache size for %q is %d, want %d", file.LogicalPath, info.Size(), file.Size)
	}
	hash := md5.New()
	if _, err := io.Copy(hash, cacheFile); err != nil {
		return "", "", fmt.Errorf("hash local cache for %q: %w", file.LogicalPath, err)
	}
	localMD5 := hex.EncodeToString(hash.Sum(nil))
	if file.MD5 != "" && !strings.EqualFold(strings.TrimSpace(file.MD5), localMD5) {
		return "", "", fmt.Errorf("local cache MD5 for %q no longer matches source manifest", file.LogicalPath)
	}
	return full, localMD5, nil
}
