// SPDX-License-Identifier: Apache-2.0
package media

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

// The header is read by framy before ffmpeg sees the file: the picture's
// size, so a file that declares a billion pixels is refused before anything
// decodes it, and its colours.
//
// An iPhone shoots in Display P3 and says so with an ICC profile. ffmpeg
// keeps the profile of a plain JPEG but loses it when it joins the tiles of
// a HEIC, and models and Telegram read pixels as sRGB anyway, so framy finds
// the profile itself and has ffmpeg turn the pixels into sRGB.

const (
	// headBytes is how much of a JPEG, PNG or WebP is read for its header:
	// the profile and the size come before the picture data.
	headBytes = 8 << 20
	// maxMeta bounds a HEIF's meta box; an iPhone's is a few kilobytes.
	maxMeta = 16 << 20
	// maxICC bounds a profile; Display P3's is about 500 bytes.
	maxICC = 4 << 20
)

type header struct {
	format Format
	// width and height are the largest picture the header declares: for a
	// HEIF, of any item, tiles and thumbnails included. Zero when it names
	// none.
	width, height int
	p3            bool
}

func (h header) pixels() int64 { return int64(h.width) * int64(h.height) }

// Info is what a picture's header says: its format and size.
type Info struct {
	Format        Format
	Width, Height int
}

// Inspect reads the header of a picture of size bytes, without decoding it.
// For a HEIF the size is that of its largest item.
func Inspect(r io.ReaderAt, size int64) (Info, error) {
	h, err := readHeader(r, size)
	return Info{Format: h.format, Width: h.width, Height: h.height}, err
}

func readFile(f *os.File) (header, error) {
	st, err := f.Stat()
	if err != nil {
		return header{}, err
	}
	return readHeader(f, st.Size())
}

// readHeader sniffs a file and reads its header.
func readHeader(f io.ReaderAt, size int64) (header, error) {
	head := make([]byte, min(size, headBytes))
	if _, err := io.ReadFull(io.NewSectionReader(f, 0, int64(len(head))), head); err != nil {
		return header{}, err
	}
	h := header{format: Sniff(head)}
	switch h.format {
	case JPEG:
		h.width, h.height = jpegSize(head)
		h.p3 = iccIsP3(jpegICC(head))
	case PNG:
		h.width, h.height = pngSize(head)
		icc, cicpP3 := pngColor(head)
		h.p3 = cicpP3 || iccIsP3(icc)
	case WebP:
		h.width, h.height = webpSize(head)
		h.p3 = iccIsP3(webpChunk(head, "ICCP"))
	case HEIF, AVIF:
		meta, err := heifMeta(f, size)
		if err != nil {
			return header{}, err
		}
		ipco := child(child(meta, "iprp"), "ipco")
		h.width, h.height = heifSize(ipco)
		h.p3 = heifIsP3(ipco)
	}
	return h, nil
}

// jpegSegments calls fn for each marker segment before the image data.
func jpegSegments(b []byte, fn func(marker byte, seg []byte)) {
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return
		}
		marker := b[i+1]
		switch {
		case marker == 0xFF:
			// Fill bytes before a marker.
			i++
			continue
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD8):
			i += 2
			continue
		case marker == 0xDA || marker == 0xD9:
			return
		}
		n := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if n < 2 || i+2+n > len(b) {
			return
		}
		fn(marker, b[i+4:i+2+n])
		i += 2 + n
	}
}

// jpegSize reads the frame header: any SOF marker but DHT, JPG and DAC.
func jpegSize(b []byte) (w, h int) {
	jpegSegments(b, func(marker byte, seg []byte) {
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC && len(seg) >= 5 && w == 0 {
			h = int(binary.BigEndian.Uint16(seg[1:3]))
			w = int(binary.BigEndian.Uint16(seg[3:5]))
		}
	})
	return w, h
}

// jpegICC joins the APP2 ICC_PROFILE segments in their order.
func jpegICC(b []byte) []byte {
	parts := map[int][]byte{}
	count := 0
	jpegSegments(b, func(marker byte, seg []byte) {
		if marker == 0xE2 && len(seg) > 14 && string(seg[:12]) == "ICC_PROFILE\x00" {
			parts[int(seg[12])] = seg[14:]
			count = int(seg[13])
		}
	})
	var icc []byte
	for k := 1; k <= count; k++ {
		p, ok := parts[k]
		if !ok || len(icc)+len(p) > maxICC {
			return nil
		}
		icc = append(icc, p...)
	}
	return icc
}

func pngSize(b []byte) (w, h int) {
	if len(b) < 24 || string(b[12:16]) != "IHDR" {
		return 0, 0
	}
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24]))
}

// pngColor reads iCCP, and cICP's primaries: 12 is Display P3.
func pngColor(b []byte) (icc []byte, cicpP3 bool) {
	for i := 8; i+12 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[i : i+4]))
		typ := string(b[i+4 : i+8])
		if n < 0 || n > len(b) || i+12+n > len(b) || typ == "IDAT" {
			break
		}
		data := b[i+8 : i+8+n]
		switch typ {
		case "cICP":
			if len(data) >= 1 && data[0] == 12 {
				cicpP3 = true
			}
		case "iCCP":
			if z := bytes.IndexByte(data, 0); z >= 0 && z+2 <= len(data) {
				if r, err := zlib.NewReader(bytes.NewReader(data[z+2:])); err == nil {
					icc, _ = io.ReadAll(io.LimitReader(r, maxICC))
				}
			}
		}
		i += 12 + n
	}
	return icc, cicpP3
}

// webpChunk returns the payload of a RIFF chunk of an extended WebP.
func webpChunk(b []byte, typ string) []byte {
	for i := 12; i+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		if n < 0 || n > len(b) || i+8+n > len(b) {
			break
		}
		if string(b[i:i+4]) == typ {
			return b[i+8 : i+8+n]
		}
		i += 8 + n + n%2
	}
	return nil
}

// webpSize reads the canvas of an extended WebP, or the frame of a simple
// lossy or lossless one.
func webpSize(b []byte) (w, h int) {
	if c := webpChunk(b, "VP8X"); len(c) >= 10 {
		return int(le24(c[4:7])) + 1, int(le24(c[7:10])) + 1
	}
	if c := webpChunk(b, "VP8L"); len(c) >= 5 && c[0] == 0x2F {
		bits := binary.LittleEndian.Uint32(c[1:5])
		return int(bits&0x3FFF) + 1, int(bits>>14&0x3FFF) + 1
	}
	if c := webpChunk(b, "VP8 "); len(c) >= 10 && c[3] == 0x9D && c[4] == 0x01 && c[5] == 0x2A {
		return int(binary.LittleEndian.Uint16(c[6:8]) & 0x3FFF), int(binary.LittleEndian.Uint16(c[8:10]) & 0x3FFF)
	}
	return 0, 0
}

func le24(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 }

// errNoMeta: an ISO BMFF file without the meta box of an image.
var errNoMeta = errors.New("no meta box")

// heifMeta finds the top-level meta box and returns its children, after the
// full box's version and flags. It may come after the picture data.
func heifMeta(r io.ReaderAt, size int64) ([]byte, error) {
	var hdr [16]byte
	for off := int64(0); off+8 <= size; {
		if _, err := r.ReadAt(hdr[:8], off); err != nil {
			return nil, err
		}
		n, head := int64(binary.BigEndian.Uint32(hdr[:4])), int64(8)
		switch n {
		case 0:
			n = size - off
		case 1:
			if _, err := r.ReadAt(hdr[8:16], off+8); err != nil {
				return nil, err
			}
			n, head = int64(binary.BigEndian.Uint64(hdr[8:16])), 16
		}
		if n < head || off+n > size || n < 0 {
			return nil, errNoMeta
		}
		if string(hdr[4:8]) == "meta" {
			if n-head < 4 || n-head > maxMeta {
				return nil, errNoMeta
			}
			meta := make([]byte, n-head)
			if _, err := r.ReadAt(meta, off+head); err != nil {
				return nil, err
			}
			return meta[4:], nil
		}
		off += n
	}
	return nil, errNoMeta
}

// heifSize is the largest ispe, the size property of an item: the grid's
// canvas, a tile, a thumbnail or an auxiliary picture.
func heifSize(ipco []byte) (w, h int) {
	for _, ispe := range children(ipco, "ispe") {
		if len(ispe) < 12 {
			continue
		}
		iw, ih := int(binary.BigEndian.Uint32(ispe[4:8])), int(binary.BigEndian.Uint32(ispe[8:12]))
		if int64(iw)*int64(ih) > int64(w)*int64(h) {
			w, h = iw, ih
		}
	}
	return w, h
}

// heifIsP3 looks for a colr property: a profile with P3's colorants, or
// nclx primaries 12.
func heifIsP3(ipco []byte) bool {
	for _, colr := range children(ipco, "colr") {
		if len(colr) < 4 {
			continue
		}
		switch string(colr[:4]) {
		case "nclx":
			if len(colr) >= 6 && binary.BigEndian.Uint16(colr[4:6]) == 12 {
				return true
			}
		case "prof", "rICC":
			if iccIsP3(colr[4:]) {
				return true
			}
		}
	}
	return false
}

// displayP3 are Display P3's colorants as an ICC profile stores them,
// adapted to D50: red, green and blue, X Y Z each.
var displayP3 = [3][3]float64{
	{0.5151, 0.2412, -0.0011},
	{0.2920, 0.6922, 0.0419},
	{0.1571, 0.0666, 0.7841},
}

// iccIsP3 compares a profile's colorants with Display P3's. Other wide
// profiles, such as Adobe RGB, are kept as they are.
func iccIsP3(icc []byte) bool {
	if len(icc) < 132 || string(icc[36:40]) != "acsp" || string(icc[16:20]) != "RGB " {
		return false
	}
	n := int(binary.BigEndian.Uint32(icc[128:132]))
	if n > 1000 || 132+12*n > len(icc) {
		return false
	}
	channel := map[string]int{"rXYZ": 0, "gXYZ": 1, "bXYZ": 2}
	found := 0
	for i := range n {
		e := icc[132+12*i:]
		c, ok := channel[string(e[:4])]
		if !ok {
			continue
		}
		off := int(binary.BigEndian.Uint32(e[4:8]))
		if off < 0 || off+20 > len(icc) || string(icc[off:off+4]) != "XYZ " {
			return false
		}
		for k := range 3 {
			v := float64(int32(binary.BigEndian.Uint32(icc[off+8+4*k:]))) / 65536
			if math.Abs(v-displayP3[c][k]) > 0.002 {
				return false
			}
		}
		found++
	}
	return found == 3
}

// child returns the payload of the first box of type typ in b.
func child(b []byte, typ string) []byte {
	for _, box := range boxes(b) {
		if box.typ == typ {
			return box.data
		}
	}
	return nil
}

func children(b []byte, typ string) [][]byte {
	var out [][]byte
	for _, box := range boxes(b) {
		if box.typ == typ {
			out = append(out, box.data)
		}
	}
	return out
}

type box struct {
	typ  string
	data []byte
}

// boxes splits b into ISO BMFF boxes; a box cut off at the end of b ends
// the list.
func boxes(b []byte) []box {
	var out []box
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b[:4]))
		typ := string(b[4:8])
		head := uint64(8)
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return out
			}
			size, head = binary.BigEndian.Uint64(b[8:16]), 16
		}
		if size < head || size > uint64(len(b)) {
			return out
		}
		out = append(out, box{typ: typ, data: b[head:size]})
		b = b[size:]
	}
	return out
}
