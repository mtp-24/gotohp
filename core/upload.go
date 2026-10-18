package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FilesDroppedEvent is emitted when files are dropped on any drop zone
type FilesDroppedEvent struct {
	Files    []string `json:"files"`
	DropZone string   `json:"dropZone"`
}

// StartUploadEvent is received from the frontend to add files to the queue.
// The album choice belongs to this drop only.
type StartUploadEvent struct {
	Files         []string `json:"files"`
	AlbumName     string   `json:"albumName"`
	AlbumAutoMode bool     `json:"albumAutoMode"`
}

type UploadBatchStart struct {
	Total      int   `json:"Total"`
	TotalBytes int64 `json:"TotalBytes"`
}

type FileUploadResult struct {
	MediaKey     string   `json:"MediaKey"`
	IsError      bool     `json:"IsError"`
	IsLivePhoto  bool     `json:"IsLivePhoto"`
	Skipped      bool     `json:"Skipped"`
	SkipCode     string   `json:"SkipCode"`
	SkipReason   string   `json:"SkipReason"`
	Error        error    `json:"-"`
	ErrorMessage string   `json:"ErrorMessage"`
	Path         string   `json:"Path"`
	Paths        []string `json:"Paths"`
}

type ThreadStatus struct {
	WorkerID      int    `json:"WorkerID"`
	Status        string `json:"Status"` // "idle", "hashing", "checking", "uploading", "finalizing", "completed", "skipped", "error"
	FilePath      string `json:"FilePath"`
	FileName      string `json:"FileName"`
	Message       string `json:"Message"`
	BytesUploaded int64  `json:"BytesUploaded"`
	BytesTotal    int64  `json:"BytesTotal"`
	Attempt       int    `json:"Attempt"` // Current attempt number (1-based), 0 if not applicable
}

// supportedFormats is a map of file extensions supported by Google Photos (O(1) lookup)
var supportedFormats = map[string]bool{
	// Photo formats
	"avif": true, "bmp": true, "gif": true, "heic": true, "heif": true, "ico": true,
	"jpg": true, "jpeg": true, "png": true, "tif": true, "tiff": true, "webp": true,
	"cr2": true, "cr3": true, "nef": true, "arw": true, "orf": true,
	"raf": true, "rw2": true, "pef": true, "sr2": true, "dng": true,
	// Video formats
	"3gp": true, "3g2": true, "asf": true, "avi": true, "divx": true,
	"m2t": true, "m2ts": true, "m4v": true, "mkv": true, "mmv": true,
	"mod": true, "mov": true, "mp4": true, "mpg": true, "mpeg": true,
	"mts": true, "tod": true, "wmv": true, "ts": true, "webm": true,
}

// isSupportedByGooglePhotos checks if a file extension is supported by Google Photos
func isSupportedByGooglePhotos(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return false
	}
	// Remove the dot and check map
	return supportedFormats[ext[1:]]
}

// appleDoubleMagic is the header of the "._name" sidecar files macOS writes
// next to each file on volumes without native xattr support (exFAT, FAT32, SMB).
var appleDoubleMagic = []byte{0x00, 0x05, 0x16, 0x07}

// isAppleDoubleFile reports whether path is a macOS AppleDouble metadata file.
// These share the original's extension (e.g. "._IMG_0001.JPG") but hold no
// media data, so Google Photos rejects them.
func isAppleDoubleFile(path string) bool {
	if !strings.HasPrefix(filepath.Base(path), "._") {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, len(appleDoubleMagic))
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	return bytes.Equal(header, appleDoubleMagic)
}

// junkFileNames are operating-system metadata files that never hold media.
var junkFileNames = map[string]bool{
	".ds_store": true, "thumbs.db": true, "desktop.ini": true, "ehthumbs.db": true,
}

func scanDirectoryForFiles(path string, recursive bool, excludePattern string, cancelled func() bool, warn func(PreflightWarning)) ([]string, error) {
	var files []string

	if cancelled != nil && cancelled() {
		return nil, context.Canceled
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if cancelled != nil && cancelled() {
			return nil, context.Canceled
		}
		fullPath := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			if excludePattern != "" && entry.Name() == excludePattern {
				continue
			}
			if recursive {
				subFiles, err := scanDirectoryForFiles(fullPath, recursive, excludePattern, cancelled, warn)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return nil, err
					}
					warn(PreflightWarning{
						Paths:   []string{fullPath},
						Code:    "folder-unreadable",
						Message: fmt.Sprintf("Could not read folder %s: %v", entry.Name(), err),
					})
					continue
				}
				files = append(files, subFiles...)
			}
			continue
		}
		if junkFileNames[strings.ToLower(entry.Name())] {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			// Symlinked folders are not followed; they would otherwise be read as files.
			if info, err := os.Stat(fullPath); err == nil && info.IsDir() {
				continue
			}
		}
		files = append(files, fullPath)
	}

	return files, nil
}

// FilterGooglePhotosFiles expands paths into the files that would be uploaded
// under opts: directories are scanned, unsupported files dropped, duplicates removed.
func FilterGooglePhotosFiles(paths []string, opts UploadOptions) ([]string, error) {
	files, _, err := filterGooglePhotosFilesWithWarnings(paths, opts, nil, nil)
	return files, err
}

// filterGooglePhotosFilesWithWarnings is FilterGooglePhotosFiles with
// cancellation, also returning warnings for folders that could not be read.
// found, when set, is called with the running count of selected files.
func filterGooglePhotosFilesWithWarnings(paths []string, opts UploadOptions, cancelled func() bool, found func(int)) ([]string, []PreflightWarning, error) {
	var supportedFiles []string
	var warnings []PreflightWarning
	warn := func(w PreflightWarning) { warnings = append(warnings, w) }
	type seenUploadFile struct {
		canonicalPath string
		info          os.FileInfo
	}
	seenFiles := make(map[string][]seenUploadFile)
	appendFile := func(path string, info os.FileInfo) {
		if !opts.DisableUnsupportedFilesFilter && !isSupportedByGooglePhotos(path) {
			return
		}
		if isAppleDoubleFile(path) {
			return
		}
		canonicalPath := canonicalUploadPath(path)
		bucketKey := canonicalPath
		if runtime.GOOS == "windows" {
			bucketKey = strings.ToLower(bucketKey)
		}
		if info == nil {
			info, _ = os.Stat(path)
		}
		for _, seen := range seenFiles[bucketKey] {
			if canonicalPath == seen.canonicalPath ||
				(info != nil && seen.info != nil && os.SameFile(info, seen.info)) {
				return
			}
		}
		seenFiles[bucketKey] = append(seenFiles[bucketKey], seenUploadFile{
			canonicalPath: canonicalPath,
			info:          info,
		})
		supportedFiles = append(supportedFiles, path)
		if found != nil {
			found(len(supportedFiles))
		}
	}

	for _, path := range paths {
		if cancelled != nil && cancelled() {
			return nil, nil, context.Canceled
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			return nil, nil, fmt.Errorf("error accessing path %s: %w", path, err)
		}

		if fileInfo.IsDir() {
			files, err := scanDirectoryForFiles(path, opts.Recursive, opts.ExcludePattern, cancelled, warn)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil, nil, err
				}
				return nil, nil, fmt.Errorf("error scanning directory %s: %w", path, err)
			}

			for _, file := range files {
				if cancelled != nil && cancelled() {
					return nil, nil, context.Canceled
				}
				appendFile(file, nil)
			}
		} else {
			appendFile(path, fileInfo)
		}
	}

	return supportedFiles, warnings, nil
}

func canonicalUploadPath(path string) string {
	canonicalPath, err := filepath.Abs(path)
	if err != nil {
		canonicalPath = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(canonicalPath); err == nil {
		canonicalPath = resolved
	}
	return filepath.Clean(canonicalPath)
}

// UploadFile uploads a single file and returns its media key.
func UploadFile(ctx context.Context, api *Api, filePath string, opts UploadOptions, workerID int, reporter UploadReporter) (string, error) {
	if reporter == nil {
		reporter = NopReporter{}
	}
	mediaKey, _, err := uploadSingleFile(ctx, api, filePath, opts, workerID, reporter)
	return mediaKey, err
}

// uploadSingleFile uploads one file. existed reports that the file was already
// in the library, in which case mediaKey is the existing item's key.
func uploadSingleFile(ctx context.Context, api *Api, filePath string, opts UploadOptions, workerID int, reporter UploadReporter) (mediaKey string, existed bool, err error) {
	fileName := filepath.Base(filePath)
	mediakey := ""

	// Determine the timestamp to use for the upload.
	// Default to file mtime, but allow filename-based timestamp to take precedence if enabled.
	var uploadTimestamp int64
	var initialSize int64
	if info, err := os.Stat(filePath); err == nil {
		uploadTimestamp = info.ModTime().Unix()
		initialSize = info.Size()
	}
	if opts.SetDateFromFilename {
		if t, ok := parseTimestampFromFilename(filePath); ok {
			uploadTimestamp = t.Unix()
		}
	}

	// Stage 1: Hashing
	reporter.ThreadStatus(ThreadStatus{
		WorkerID: workerID,
		Status:   "hashing",
		FilePath: filePath,
		FileName: fileName,
		Message:  "Hashing...",
	})

	sha1_hash_bytes, err := CalculateSHA1(ctx, filePath)
	if err != nil {
		return "", false, fmt.Errorf("error calculating hash file: %w", err)
	}

	sha1_hash_b64 := base64.StdEncoding.EncodeToString([]byte(sha1_hash_bytes))

	// Stage 2: Checking if exists in library
	if !opts.ForceUpload {
		reporter.ThreadStatus(ThreadStatus{
			WorkerID: workerID,
			Status:   "checking",
			FilePath: filePath,
			FileName: fileName,
			Message:  "Checking if file exists in library...",
		})

		mediakey, err = api.FindRemoteMediaByHash(sha1_hash_bytes)
		if err != nil {
			// Non-fatal: log via callback and continue with upload
			reporter.ThreadStatus(ThreadStatus{
				WorkerID: workerID,
				Status:   "checking",
				FilePath: filePath,
				FileName: fileName,
				Message:  fmt.Sprintf("Hash check warning: %v, proceeding with upload", err),
			})
		}
		if len(mediakey) > 0 {
			reporter.ThreadStatus(ThreadStatus{
				WorkerID: workerID,
				Status:   "completed",
				FilePath: filePath,
				FileName: fileName,
				Message:  "Already in library",
			})
			// Nothing will be uploaded for this file.
			reporter.TotalBytesDelta(-initialSize)
			if opts.DeleteFromHost {
				if err := os.Remove(filePath); err != nil {
					return mediakey, true, fmt.Errorf("file exists in library but failed to delete local copy: %w", err)
				}
			}
			return mediakey, true, nil
		}
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return "", false, fmt.Errorf("error getting file info: %w", err)
	}

	// Stage 3: Uploading
	fileSize := fileInfo.Size()
	reporter.ThreadStatus(ThreadStatus{
		WorkerID:      workerID,
		Status:        "uploading",
		FilePath:      filePath,
		FileName:      fileName,
		Message:       "Uploading...",
		BytesUploaded: 0,
		BytesTotal:    fileSize,
	})

	token, err := api.GetUploadToken(sha1_hash_b64, fileSize)
	if err != nil {
		return "", false, fmt.Errorf("error uploading file: %w", err)
	}

	// Create progress callback for upload
	progressCallback := func(bytesUploaded, bytesTotal int64, attempt int) {
		message := "Uploading..."
		if attempt > 1 {
			message = fmt.Sprintf("Retrying... (attempt %d)", attempt)
		}
		reporter.ThreadStatus(ThreadStatus{
			WorkerID:      workerID,
			Status:        "uploading",
			FilePath:      filePath,
			FileName:      fileName,
			Message:       message,
			BytesUploaded: bytesUploaded,
			BytesTotal:    bytesTotal,
			Attempt:       attempt,
		})
	}

	newToken := func() (string, error) { return api.GetUploadToken(sha1_hash_b64, fileSize) }
	finalizeToken, err := api.UploadFileWithNewSessions(ctx, filePath, token, newToken, progressCallback)
	if err != nil {
		return "", false, fmt.Errorf("error uploading file: %w", err)
	}
	commitToken, err := finalizeToken.legacyCommitToken()
	if err != nil {
		return "", false, fmt.Errorf("error decoding upload finalize token: %w", err)
	}

	// Stage 4: Finalizing
	reporter.ThreadStatus(ThreadStatus{
		WorkerID: workerID,
		Status:   "finalizing",
		FilePath: filePath,
		FileName: fileName,
		Message:  "Committing upload...",
	})

	mediaKey, err = api.CommitUpload(commitToken, fileInfo.Name(), sha1_hash_bytes, uploadTimestamp)
	if err != nil {
		return "", false, fmt.Errorf("error committing file: %w", err)
	}

	if len(mediaKey) == 0 {
		return "", false, fmt.Errorf("media key not received")
	}

	if opts.DeleteFromHost {
		if err := os.Remove(filePath); err != nil {
			return mediaKey, false, fmt.Errorf("uploaded successfully but failed to delete file: %w", err)
		}
	}

	return mediaKey, false, nil
}

// uploadWorkItem uploads one queue item. skipped means a Live Photo was left
// alone by policy; existed means a single file was already in the library.
func uploadWorkItem(ctx context.Context, api *Api, item UploadWorkItem, opts UploadOptions, workerID int, reporter UploadReporter) (mediaKey string, skipped, existed bool, err error) {
	switch item.Kind {
	case UploadWorkSingle:
		if item.Single == nil || item.LivePhoto != nil {
			return "", false, false, fmt.Errorf("invalid single-media work item")
		}
		mediaKey, existed, err := uploadSingleFile(ctx, api, item.Single.Path, opts, workerID, reporter)
		return mediaKey, false, existed, err
	case UploadWorkLivePhoto:
		if item.LivePhoto == nil || item.Single != nil {
			return "", false, false, fmt.Errorf("invalid Live Photo work item")
		}
		mediaKey, skipped, err := uploadLivePhotoWithCallback(ctx, api, *item.LivePhoto, LivePhotoUploadOptions{
			Policy:                     buildLivePhotoCommitPolicy(api),
			DeleteFromHost:             opts.DeleteFromHost,
			SetDateFromFilename:        opts.SetDateFromFilename,
			UpdateExistingPhotosToLive: opts.UpdateExistingPhotosToLive,
		}, workerID, reporter)
		return mediaKey, skipped, false, err
	default:
		return "", false, false, fmt.Errorf("unsupported upload work kind %q", item.Kind)
	}
}

func uploadWorkPaths(item UploadWorkItem) []string {
	if item.Kind == UploadWorkLivePhoto && item.LivePhoto != nil {
		return []string{item.LivePhoto.PhotoPath, item.LivePhoto.VideoPath}
	}
	if item.Single != nil {
		return []string{item.Single.Path}
	}
	return nil
}

func uploadWorkPrimaryPath(item UploadWorkItem) string {
	paths := uploadWorkPaths(item)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}
