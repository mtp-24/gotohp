package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type UploadWorkKind string

const (
	UploadWorkSingle    UploadWorkKind = "single"
	UploadWorkLivePhoto UploadWorkKind = "live-photo"
)

type SingleMedia struct {
	Path string
}

type LivePhotoPair struct {
	ContentIdentifier string
	PhotoPath         string
	VideoPath         string
}

type UploadWorkItem struct {
	Kind      UploadWorkKind
	Single    *SingleMedia
	LivePhoto *LivePhotoPair
}

type PreflightWarning struct {
	Paths   []string `json:"Paths"`
	Code    string   `json:"Code"`
	Message string   `json:"Message"`
}

type LivePhotoClassificationOptions struct {
	Enabled             bool
	SkipIncomplete      bool
	IgnoreAppleMetadata bool
	Cancelled           func() bool
	// Progress, when set, is called as each file's metadata is checked.
	Progress func(done, total int)
}

type LivePhotoMetadataReader interface {
	PhotoContentIdentifier(path string) (string, error)
	VideoLivePhotoMetadata(path string) (LivePhotoMetadata, error)
}

type fileLivePhotoMetadataReader struct{}

func (fileLivePhotoMetadataReader) PhotoContentIdentifier(path string) (string, error) {
	return ReadPhotoContentIdentifier(path)
}

func (fileLivePhotoMetadataReader) VideoLivePhotoMetadata(path string) (LivePhotoMetadata, error) {
	return ReadVideoLivePhotoMetadata(path)
}

type indexedLivePhotoMedia struct {
	path  string
	index int
}

type livePhotoCandidates struct {
	photos []indexedLivePhotoMedia
	videos []indexedLivePhotoMedia
}

// ClassifyUploadWork normally pairs files by their embedded Apple content
// identifier. The explicit CLI override uses case-insensitive filename stems;
// it remains one-to-one and still requires valid Live Photo video timing data.
func ClassifyUploadWork(paths []string, options LivePhotoClassificationOptions, reader LivePhotoMetadataReader) ([]UploadWorkItem, []PreflightWarning) {
	if !options.Enabled {
		work := make([]UploadWorkItem, 0, len(paths))
		for _, path := range paths {
			if options.Cancelled != nil && options.Cancelled() {
				return nil, nil
			}
			work = append(work, UploadWorkItem{Kind: UploadWorkSingle, Single: &SingleMedia{Path: path}})
		}
		return work, nil
	}
	if reader == nil {
		reader = fileLivePhotoMetadataReader{}
	}

	candidatesByIdentifier := make(map[string]*livePhotoCandidates)
	warnings := make([]PreflightWarning, 0)
	for index, path := range paths {
		if options.Cancelled != nil && options.Cancelled() {
			return nil, nil
		}
		if options.Progress != nil {
			options.Progress(index+1, len(paths))
		}
		switch livePhotoCandidateType(path) {
		case "photo":
			if options.IgnoreAppleMetadata {
				candidates := candidatesForIdentifier(candidatesByIdentifier, livePhotoFilenameMatchKey(path))
				candidates.photos = append(candidates.photos, indexedLivePhotoMedia{path: path, index: index})
				continue
			}
			identifier, err := reader.PhotoContentIdentifier(path)
			if errors.Is(err, ErrContentIdentifierMissing) {
				continue
			}
			if err != nil {
				warnings = append(warnings, metadataReadWarning(path, err))
				continue
			}
			candidates := candidatesForIdentifier(candidatesByIdentifier, strings.ToLower(identifier))
			candidates.photos = append(candidates.photos, indexedLivePhotoMedia{path: path, index: index})
		case "video":
			metadata, err := reader.VideoLivePhotoMetadata(path)
			if errors.Is(err, ErrContentIdentifierMissing) && !options.IgnoreAppleMetadata {
				continue
			}
			if err != nil && !errors.Is(err, ErrContentIdentifierMissing) {
				warnings = append(warnings, metadataReadWarning(path, err))
				continue
			}
			if !metadata.HasStillImageTime {
				warnings = append(warnings, PreflightWarning{
					Paths:   []string{path},
					Code:    "missing-still-image-time",
					Message: "video has a Live Photo content identifier but no still-image-time marker",
				})
				continue
			}
			identifier := metadata.ContentIdentifier
			if options.IgnoreAppleMetadata {
				identifier = livePhotoFilenameMatchKey(path)
			} else {
				identifier = strings.ToLower(identifier)
			}
			candidates := candidatesForIdentifier(candidatesByIdentifier, identifier)
			candidates.videos = append(candidates.videos, indexedLivePhotoMedia{path: path, index: index})
		}
	}

	pairsByIndex := make(map[int]LivePhotoPair)
	consumed := make(map[int]bool)
	identifiers := sortedLivePhotoIdentifiers(candidatesByIdentifier)
	for _, identifier := range identifiers {
		if options.Cancelled != nil && options.Cancelled() {
			return nil, nil
		}
		candidates := candidatesByIdentifier[identifier]
		if len(candidates.photos) == 1 && len(candidates.videos) == 1 {
			photo := candidates.photos[0]
			video := candidates.videos[0]
			pairIndex := min(photo.index, video.index)
			contentIdentifier := identifier
			if options.IgnoreAppleMetadata {
				contentIdentifier = ""
			}
			pairsByIndex[pairIndex] = LivePhotoPair{
				ContentIdentifier: contentIdentifier,
				PhotoPath:         photo.path,
				VideoPath:         video.path,
			}
			consumed[photo.index] = true
			consumed[video.index] = true
			continue
		}
		// Copies of one shot share its identifier. Pair the copies that can be
		// told apart; the rest upload as separate files.
		if !options.IgnoreAppleMetadata && (len(candidates.photos) > 1 || len(candidates.videos) > 1) {
			pairs, unresolved := resolveDuplicateLivePhotoCandidates(candidates)
			for _, pair := range pairs {
				photo, video := pair[0], pair[1]
				pairsByIndex[min(photo.index, video.index)] = LivePhotoPair{
					ContentIdentifier: identifier,
					PhotoPath:         photo.path,
					VideoPath:         video.path,
				}
				consumed[photo.index] = true
				consumed[video.index] = true
			}
			if len(unresolved.photos) == 0 && len(unresolved.videos) == 0 {
				continue
			}
			candidates = unresolved
		}
		if len(candidates.photos) > 1 || len(candidates.videos) > 1 {
			ambiguousPaths := candidatePaths(candidates)
			code := "ambiguous-identifier"
			message := "Uploaded as separate files: these share one Live Photo ID, so they could not be paired"
			if options.IgnoreAppleMetadata {
				code = "ambiguous-filename-stem"
				message = "Skipped: these share one filename stem, so they could not be paired"
				// Filename matching is an explicit override, so ambiguity must skip
				// every candidate instead of silently falling back to single uploads.
				for _, photo := range candidates.photos {
					consumed[photo.index] = true
				}
				for _, video := range candidates.videos {
					consumed[video.index] = true
				}
			}
			warnings = append(warnings, PreflightWarning{
				Paths:   ambiguousPaths,
				Code:    code,
				Message: message,
			})
			continue
		}
		if options.SkipIncomplete {
			orphanPaths := candidatePaths(candidates)
			for _, photo := range candidates.photos {
				consumed[photo.index] = true
			}
			for _, video := range candidates.videos {
				consumed[video.index] = true
			}
			warnings = append(warnings, PreflightWarning{
				Paths:   orphanPaths,
				Code:    "incomplete-live-photo-skipped",
				Message: "Skipped an incomplete Live Photo because its matching file is not in this queue. You can disable this check in Settings.",
			})
		}
	}

	work := make([]UploadWorkItem, 0, len(paths))
	for index, path := range paths {
		if options.Cancelled != nil && options.Cancelled() {
			return nil, nil
		}
		if pair, ok := pairsByIndex[index]; ok {
			pair := pair
			work = append(work, UploadWorkItem{Kind: UploadWorkLivePhoto, LivePhoto: &pair})
			continue
		}
		if consumed[index] {
			continue
		}
		work = append(work, UploadWorkItem{Kind: UploadWorkSingle, Single: &SingleMedia{Path: path}})
	}
	return work, warnings
}

func sortedLivePhotoIdentifiers(candidatesByIdentifier map[string]*livePhotoCandidates) []string {
	identifiers := make([]string, 0, len(candidatesByIdentifier))
	for identifier := range candidatesByIdentifier {
		identifiers = append(identifiers, identifier)
	}
	sort.Slice(identifiers, func(left, right int) bool {
		return candidateFirstIndex(candidatesByIdentifier[identifiers[left]]) < candidateFirstIndex(candidatesByIdentifier[identifiers[right]])
	})
	return identifiers
}

func candidateFirstIndex(candidates *livePhotoCandidates) int {
	first := int(^uint(0) >> 1)
	for _, photo := range candidates.photos {
		first = min(first, photo.index)
	}
	for _, video := range candidates.videos {
		first = min(first, video.index)
	}
	return first
}

func livePhotoCandidateType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".heic", ".heif", ".jpg", ".jpeg":
		return "photo"
	case ".mov":
		return "video"
	default:
		return ""
	}
}

func livePhotoFilenameMatchKey(path string) string {
	cleanPath := filepath.Clean(path)
	directory := filepath.Dir(cleanPath)
	baseName := filepath.Base(cleanPath)
	stem := strings.ToLower(strings.TrimSuffix(baseName, filepath.Ext(baseName)))
	return filepath.Join(directory, stem)
}

func candidatesForIdentifier(candidatesByIdentifier map[string]*livePhotoCandidates, identifier string) *livePhotoCandidates {
	candidates := candidatesByIdentifier[identifier]
	if candidates == nil {
		candidates = &livePhotoCandidates{}
		candidatesByIdentifier[identifier] = candidates
	}
	return candidates
}

func metadataReadWarning(path string, err error) PreflightWarning {
	return PreflightWarning{
		Paths:   []string{path},
		Code:    "metadata-read-failed",
		Message: fmt.Sprintf("could not read Live Photo metadata: %v", err),
	}
}

// resolveDuplicateLivePhotoCandidates pairs candidates that share one
// identifier when the copies can be told apart. A still and its video sit in
// the same folder, so each folder is matched on its own first; within a
// folder, or across folders when no folder pairs, the camera's HEIC wins over
// exported copies. It returns the pairs as [photo, video] and the candidates
// of folders that remain ambiguous. Leftover single copies are in neither and
// upload as separate files.
func resolveDuplicateLivePhotoCandidates(candidates *livePhotoCandidates) ([][2]indexedLivePhotoMedia, *livePhotoCandidates) {
	byDir := make(map[string]*livePhotoCandidates)
	var dirs []string
	group := func(path string) *livePhotoCandidates {
		dir := filepath.Dir(path)
		if byDir[dir] == nil {
			byDir[dir] = &livePhotoCandidates{}
			dirs = append(dirs, dir)
		}
		return byDir[dir]
	}
	for _, photo := range candidates.photos {
		g := group(photo.path)
		g.photos = append(g.photos, photo)
	}
	for _, video := range candidates.videos {
		g := group(video.path)
		g.videos = append(g.videos, video)
	}

	pairInGroup := func(g *livePhotoCandidates) (*[2]indexedLivePhotoMedia, bool) {
		if len(g.videos) != 1 {
			return nil, false
		}
		if len(g.photos) == 1 {
			return &[2]indexedLivePhotoMedia{g.photos[0], g.videos[0]}, true
		}
		if photo, ok := originalLivePhotoStill(g.photos); ok {
			return &[2]indexedLivePhotoMedia{photo, g.videos[0]}, true
		}
		return nil, false
	}

	var pairs [][2]indexedLivePhotoMedia
	unresolved := &livePhotoCandidates{}
	for _, dir := range dirs {
		g := byDir[dir]
		if pair, ok := pairInGroup(g); ok {
			pairs = append(pairs, *pair)
			continue
		}
		if len(g.photos) > 1 || len(g.videos) > 1 {
			unresolved.photos = append(unresolved.photos, g.photos...)
			unresolved.videos = append(unresolved.videos, g.videos...)
		}
	}
	if len(pairs) == 0 && len(unresolved.photos) == 0 && len(unresolved.videos) == 0 {
		// Still and video sit in different folders: match across them.
		if pair, ok := pairInGroup(candidates); ok {
			return [][2]indexedLivePhotoMedia{*pair}, unresolved
		}
		return nil, candidates
	}
	return pairs, unresolved
}

// originalLivePhotoStill picks the still to pair when several photos share a
// Live Photo identifier: the single HEIC/HEIF among them, which is what the
// camera wrote. It fails when there is no single such photo.
func originalLivePhotoStill(photos []indexedLivePhotoMedia) (indexedLivePhotoMedia, bool) {
	if len(photos) < 2 {
		return indexedLivePhotoMedia{}, false
	}
	var found indexedLivePhotoMedia
	count := 0
	for _, photo := range photos {
		switch strings.ToLower(filepath.Ext(photo.path)) {
		case ".heic", ".heif":
			found = photo
			count++
		}
	}
	return found, count == 1
}

func candidatePaths(candidates *livePhotoCandidates) []string {
	paths := make([]string, 0, len(candidates.photos)+len(candidates.videos))
	for _, photo := range candidates.photos {
		paths = append(paths, photo.path)
	}
	for _, video := range candidates.videos {
		paths = append(paths, video.path)
	}
	return paths
}
