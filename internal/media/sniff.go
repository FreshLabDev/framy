// SPDX-License-Identifier: Apache-2.0
package media

import (
	"bytes"
	"encoding/binary"
)

// Format is an image format framy takes, as the first bytes tell it. The
// MIME type a client sends is never trusted: Safari converts HEIC on some
// uploads and not on others.
type Format string

const (
	JPEG Format = "jpeg"
	PNG  Format = "png"
	WebP Format = "webp"
	HEIF Format = "heif"
	AVIF Format = "avif"
)

// demuxer is the ffmpeg input format for each, so ffmpeg never guesses one
// from the bytes: a file framy calls a JPEG is read as nothing else.
var demuxer = map[Format]string{
	JPEG: "jpeg_pipe",
	PNG:  "png_pipe",
	WebP: "webp_pipe",
	HEIF: "mov",
	AVIF: "mov",
}

// heifBrands and avifBrands are the ftyp brands of still images. A video's
// brands (isom, mp41, qt) are not among them.
var (
	heifBrands = map[string]bool{"heic": true, "heix": true, "heim": true, "heis": true, "hevc": true, "hevx": true, "mif1": true, "msf1": true}
	avifBrands = map[string]bool{"avif": true, "avis": true}
)

// Sniff names the format of a file from its first bytes, or "".
func Sniff(head []byte) Format {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return JPEG
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return PNG
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return WebP
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		return isobmff(head)
	}
	return ""
}

// isobmff reads the brands of an ftyp box: AVIF wins over HEIF, since an
// AVIF lists mif1 too.
func isobmff(head []byte) Format {
	size := int(binary.BigEndian.Uint32(head[:4]))
	if size < 16 || size > len(head) {
		size = len(head)
	}
	brands := []string{string(head[8:12])}
	for i := 16; i+4 <= size; i += 4 {
		brands = append(brands, string(head[i:i+4]))
	}
	heif := false
	for _, b := range brands {
		if avifBrands[b] {
			return AVIF
		}
		heif = heif || heifBrands[b]
	}
	if heif {
		return HEIF
	}
	return ""
}
