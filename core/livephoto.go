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

func (fileLivePhotoMetadataReader) MediaInfo(path string) MediaInfo {
	return readMediaInfo(path)
}

// livePhotoMediaInfoReader is an optional LivePhotoMetadataReader extension
// that describes encoding and resolution, used to match copies of one Live
// Photo.
type livePhotoMediaInfoReader interface {
	MediaInfo(path string) MediaInfo
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

	mediaInfo := func(string) MediaInfo { return MediaInfo{} }
	if infoReader, ok := reader.(livePhotoMediaInfoReader); ok {
		mediaInfo = infoReader.MediaInfo
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
				// MP4s are mostly ordinary videos from other cameras; one the
				// Live Photo reader cannot parse simply uploads on its own.
				if !strings.EqualFold(filepath.Ext(path), ".mp4") {
					warnings = append(warnings, metadataReadWarning(path, err))
				}
				continue
			}
			if !metadata.HasStillImageTime && strings.EqualFold(filepath.Ext(path), ".mp4") {
				// An ordinary MP4, not Live Photo motion: upload it on its own.
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
		// told apart for certain; any group left in doubt is skipped below.
		unresolvedCopies := false
		if !options.IgnoreAppleMetadata && (len(candidates.photos) > 1 || len(candidates.videos) > 1) {
			pairs, unresolved := resolveDuplicateLivePhotoCandidates(candidates, mediaInfo)
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
			unresolvedCopies = true
		}
		if unresolvedCopies || len(candidates.photos) > 1 || len(candidates.videos) > 1 {
			ambiguousPaths := candidatePaths(candidates)
			code := "ambiguous-identifier"
			message := "Skipped: several copies share one Live Photo ID and their files do not show which motion belongs to which still. Use Upload anyway to upload them as separate files."
			if options.IgnoreAppleMetadata {
				code = "ambiguous-filename-stem"
				message = "Skipped: these share one filename stem, so they could not be paired"
			}
			// Guessing could permanently attach the wrong motion, and uploading
			// the copies separately loses the Live Photo, so skip them all and
			// leave the choice to the user.
			for _, photo := range candidates.photos {
				consumed[photo.index] = true
			}
			for _, video := range candidates.videos {
				consumed[video.index] = true
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
	// Google Takeout exports Live Photo motion as .MP4 with Apple's metadata
	// intact, so MP4s join pairing when they carry it.
	case ".mov", ".mp4":
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

// resolveDuplicateLivePhotoCandidates pairs copies of one Live Photo that
// share its identifier, but only when the files leave no doubt which motion
// belongs to which still: the identifier is Apple's only link, and every copy
// carries it. A still and its video sit in the same folder, so each folder is
// matched on its own first, falling back to all folders together when no
// folder pairs. Within a group, stills and videos are matched by encoding
// (HEIC with HEVC; exported JPEG with H.264), and only when each encoding has
// exactly one still and one video and their resolutions do not contradict
// the match.
//
// It returns the pairs as [photo, video] and the candidates of groups that
// remain ambiguous. Leftover stills without a video of their own are in
// neither and upload as separate files.
func resolveDuplicateLivePhotoCandidates(candidates *livePhotoCandidates, mediaInfo func(string) MediaInfo) ([][2]indexedLivePhotoMedia, *livePhotoCandidates) {
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

	var pairs [][2]indexedLivePhotoMedia
	unresolved := &livePhotoCandidates{}
	for _, dir := range dirs {
		g := byDir[dir]
		if len(g.videos) == 0 {
			continue // stills only: they upload as separate files
		}
		if groupPairs, ok := pairCopies(g.photos, g.videos, mediaInfo); ok {
			pairs = append(pairs, groupPairs...)
			continue
		}
		unresolved.photos = append(unresolved.photos, g.photos...)
		unresolved.videos = append(unresolved.videos, g.videos...)
	}
	if len(pairs) == 0 && len(unresolved.photos) == 0 && len(unresolved.videos) == 0 {
		// Stills and videos sit in different folders: match across them.
		if allPairs, ok := pairCopies(candidates.photos, candidates.videos, mediaInfo); ok {
			return allPairs, unresolved
		}
		return nil, candidates
	}
	return pairs, unresolved
}

// pairCopies matches stills to videos by encoding. It succeeds only when
// every video has exactly one still of the same encoding, at most one video
// is camera-original (HEVC) and one exported (H.264), and, with two pairs, the
// higher-resolution still is not given the lower-resolution video. Anything
// else is ambiguous. A still without a video of its encoding is left out and
// uploads on its own.
func pairCopies(photos, videos []indexedLivePhotoMedia, mediaInfo func(string) MediaInfo) ([][2]indexedLivePhotoMedia, bool) {
	if len(videos) == 0 || len(videos) > 2 {
		return nil, false
	}
	type classified struct {
		media indexedLivePhotoMedia
		info  MediaInfo
	}
	byEncoding := func(media []indexedLivePhotoMedia) map[bool][]classified {
		classes := make(map[bool][]classified)
		for _, m := range media {
			info := mediaInfo(m.path)
			classes[info.HEVC] = append(classes[info.HEVC], classified{m, info})
		}
		return classes
	}
	stills, motions := byEncoding(photos), byEncoding(videos)

	var pairs [][2]indexedLivePhotoMedia
	var matched [][2]classified
	for _, hevc := range []bool{true, false} {
		if len(motions[hevc]) == 0 {
			continue
		}
		if len(motions[hevc]) != 1 || len(stills[hevc]) != 1 {
			return nil, false
		}
		matched = append(matched, [2]classified{stills[hevc][0], motions[hevc][0]})
		pairs = append(pairs, [2]indexedLivePhotoMedia{stills[hevc][0].media, motions[hevc][0].media})
	}
	if len(matched) == 2 {
		first, second := matched[0], matched[1]
		stillOrder := compareArea(first[0].info, second[0].info)
		videoOrder := compareArea(first[1].info, second[1].info)
		if stillOrder*videoOrder < 0 {
			return nil, false // the higher-resolution still would get the lower-resolution video
		}
	}
	return pairs, true
}

// compareArea returns 1, -1 or 0 as a is larger, smaller or the same size as
// b, or 0 when either size is unknown.
func compareArea(a, b MediaInfo) int {
	switch {
	case a.PixelArea <= 0 || b.PixelArea <= 0 || a.PixelArea == b.PixelArea:
		return 0
	case a.PixelArea > b.PixelArea:
		return 1
	default:
		return -1
	}
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
