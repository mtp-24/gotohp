package core

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
)

// MediaInfo describes one copy of a Live Photo component, to tell copies of
// the same shot apart.
type MediaInfo struct {
	// PixelArea is width×height, or 0 when it could not be read.
	PixelArea int64
	// HEVC marks camera-original encoding: a HEIC still or an HEVC video.
	// Exports such as "Most Compatible" are JPEG with H.264 motion.
	HEVC bool
}

// readMediaInfo reads a photo's or video's MediaInfo. It is used only to
// rank copies, so failures leave fields zero instead of returning errors.
func readMediaInfo(path string) MediaInfo {
	ext := strings.ToLower(path[strings.LastIndex(path, ".")+1:])
	info := MediaInfo{HEVC: ext == "heic" || ext == "heif"}
	file, err := os.Open(path)
	if err != nil {
		return info
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return info
	}
	switch ext {
	case "jpg", "jpeg":
		info.PixelArea = jpegPixelArea(file, stat.Size())
	case "heic", "heif":
		info.PixelArea = heifPixelArea(file, stat.Size())
	case "mov", "mp4":
		info.PixelArea, info.HEVC = videoInfo(file, stat.Size())
	}
	return info
}

// jpegPixelArea reads the frame size from the first start-of-frame marker.
func jpegPixelArea(reader io.ReaderAt, size int64) int64 {
	header := make([]byte, 9)
	offset := int64(2) // after SOI
	for offset+4 <= size {
		if _, err := reader.ReadAt(header[:4], offset); err != nil || header[0] != 0xFF {
			return 0
		}
		marker := header[1]
		length := int64(binary.BigEndian.Uint16(header[2:4]))
		// SOF0..SOF15 except DHT (C4), JPG (C8) and DAC (CC).
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if _, err := reader.ReadAt(header[:9], offset); err != nil {
				return 0
			}
			height := int64(binary.BigEndian.Uint16(header[5:7]))
			width := int64(binary.BigEndian.Uint16(header[7:9]))
			return width * height
		}
		if length < 2 {
			return 0
		}
		offset += 2 + length
	}
	return 0
}

// heifPixelArea returns the largest image spatial extent ("ispe") in the
// item properties, which is the full image rather than a thumbnail or tile.
func heifPixelArea(reader io.ReaderAt, size int64) int64 {
	top, err := readDirectMP4Boxes(reader, 0, size)
	if err != nil {
		return 0
	}
	var best int64
	for _, meta := range top {
		if string(meta.typ[:]) != "meta" {
			continue
		}
		// HEIF meta is a full box: skip version and flags.
		for _, iprp := range directChildren(reader, meta.payloadStart+4, meta.end, "iprp") {
			for _, ipco := range directChildren(reader, iprp.payloadStart, iprp.end, "ipco") {
				for _, ispe := range directChildren(reader, ipco.payloadStart, ipco.end, "ispe") {
					payload, err := readBoxPayload(reader, ispe, 64)
					if err != nil || len(payload) < 12 {
						continue
					}
					width := int64(binary.BigEndian.Uint32(payload[4:8]))
					height := int64(binary.BigEndian.Uint32(payload[8:12]))
					best = max(best, width*height)
				}
			}
		}
	}
	return best
}

func directChildren(reader io.ReaderAt, start, end int64, typ string) []mp4Box {
	boxes, err := readDirectMP4Boxes(reader, start, end)
	if err != nil {
		return nil
	}
	var matches []mp4Box
	for _, box := range boxes {
		if string(box.typ[:]) == typ {
			matches = append(matches, box)
		}
	}
	return matches
}

// videoInfo returns the largest track size from the track headers, and
// whether a sample description is HEVC. A tkhd ends with width and height as
// 16.16 fixed point; an stsd lists sample entries whose type is the codec.
func videoInfo(reader io.ReaderAt, size int64) (area int64, hevc bool) {
	_ = walkMP4Boxes(reader, 0, size, func(box mp4Box) error {
		switch string(box.typ[:]) {
		case "tkhd":
			if box.end-box.payloadStart < 8 {
				return nil
			}
			dims := make([]byte, 8)
			if _, err := reader.ReadAt(dims, box.end-8); err != nil {
				return nil
			}
			width := int64(binary.BigEndian.Uint32(dims[:4]) >> 16)
			height := int64(binary.BigEndian.Uint32(dims[4:]) >> 16)
			area = max(area, width*height)
		case "stsd":
			// version/flags, entry count, then the first entry's size and type.
			header := make([]byte, 16)
			if box.end-box.payloadStart < 16 {
				return nil
			}
			if _, err := reader.ReadAt(header, box.payloadStart); err != nil {
				return nil
			}
			switch string(header[12:16]) {
			case "hvc1", "hev1":
				hevc = true
			}
		}
		return nil
	})
	return area, hevc
}
