package media

import (
	"bytes"
	"encoding/binary"
	"io"
)

// Info is the metadata Probe could read. Zero means unknown.
type Info struct {
	Width, Height     int
	DurationMs        int64
	DurationEstimated bool // derived from bitrate and size (constant-bitrate MP3), not from a header
}

const maxDimension = 1 << 20

// at reads up to n bytes at off; short or failed reads return what was read.
func at(r io.ReaderAt, size, off int64, n int) []byte {
	if off < 0 || off >= size || n <= 0 {
		return nil
	}
	if int64(n) > size-off {
		n = int(size - off)
	}
	b := make([]byte, n)
	m, _ := r.ReadAt(b, off)
	return b[:m]
}

func le16(b []byte, o int) int {
	if o < 0 || o+2 > len(b) {
		return 0
	}
	return int(binary.LittleEndian.Uint16(b[o:]))
}
func le32(b []byte, o int) uint32 {
	if o < 0 || o+4 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint32(b[o:])
}
func be16(b []byte, o int) int {
	if o < 0 || o+2 > len(b) {
		return 0
	}
	return int(binary.BigEndian.Uint16(b[o:]))
}
func be32(b []byte, o int) uint32 {
	if o < 0 || o+4 > len(b) {
		return 0
	}
	return binary.BigEndian.Uint32(b[o:])
}
func be64(b []byte, o int) uint64 {
	if o < 0 || o+8 > len(b) {
		return 0
	}
	return binary.BigEndian.Uint64(b[o:])
}

// Probe reads dimensions and duration for a sniffed file. It performs a
// bounded number of small reads and never panics on malformed input; any
// field it cannot determine stays zero.
func Probe(s Sniffed, r io.ReaderAt, size int64) Info {
	var in Info
	switch s.MIME {
	case "image/png":
		b := at(r, size, 0, 24)
		if len(b) == 24 && string(b[12:16]) == "IHDR" {
			in.Width, in.Height = int(be32(b, 16)), int(be32(b, 20))
		}
	case "image/gif":
		b := at(r, size, 0, 10)
		if len(b) == 10 {
			in.Width, in.Height = le16(b, 6), le16(b, 8)
		}
	case "image/jpeg":
		in.Width, in.Height = jpegSize(r, size)
	case "image/webp":
		in.Width, in.Height = webpSize(at(r, size, 0, 32))
	case "audio/wav":
		in.DurationMs = wavDuration(r, size)
	case "audio/mpeg":
		in.DurationMs, in.DurationEstimated = mp3Duration(r, size)
	case "audio/ogg", "video/ogg":
		in.DurationMs = oggDuration(r, size)
	case "video/mp4", "audio/mp4":
		in.Width, in.Height, in.DurationMs = mp4Info(r, size)
	}
	if in.Width < 0 || in.Height < 0 || in.Width > maxDimension || in.Height > maxDimension {
		in.Width, in.Height = 0, 0
	}
	if in.Width == 0 || in.Height == 0 {
		in.Width, in.Height = 0, 0
	}
	if in.DurationMs < 0 {
		in.DurationMs = 0
	}
	return in
}

func jpegSize(r io.ReaderAt, size int64) (int, int) {
	pos := int64(2)
	for i := 0; i < 1024; i++ {
		b := at(r, size, pos, 4)
		if len(b) < 2 || b[0] != 0xFF {
			return 0, 0
		}
		m := b[1]
		if m == 0xFF { // fill byte
			pos++
			continue
		}
		if m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7) { // no payload
			pos += 2
			continue
		}
		if m == 0xD9 || m == 0xDA { // end of image / start of scan: no frame header found
			return 0, 0
		}
		if len(b) < 4 {
			return 0, 0
		}
		l := int64(be16(b, 2))
		if l < 2 {
			return 0, 0
		}
		if m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			f := at(r, size, pos+4, 5) // precision(1) height(2) width(2)
			if len(f) < 5 {
				return 0, 0
			}
			return be16(f, 3), be16(f, 1)
		}
		pos += 2 + l
	}
	return 0, 0
}

func webpSize(b []byte) (int, int) {
	if len(b) < 30 {
		return 0, 0
	}
	switch string(b[12:16]) {
	case "VP8 ":
		if b[23] == 0x9d && b[24] == 0x01 && b[25] == 0x2a {
			return le16(b, 26) & 0x3fff, le16(b, 28) & 0x3fff
		}
	case "VP8L":
		if b[20] == 0x2f {
			v := le32(b, 21)
			return int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1
		}
	case "VP8X":
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1
	}
	return 0, 0
}

func wavDuration(r io.ReaderAt, size int64) int64 {
	var byteRate int64
	var dataSize int64 = -1
	pos := int64(12)
	for i := 0; i < 64 && pos+8 <= size; i++ {
		h := at(r, size, pos, 8)
		if len(h) < 8 {
			break
		}
		id, n := string(h[:4]), int64(le32(h, 4))
		switch id {
		case "fmt ":
			f := at(r, size, pos+8, 16)
			if len(f) == 16 {
				byteRate = int64(le32(f, 8))
			}
		case "data":
			dataSize = n
			if n == 0xFFFFFFFF || pos+8+n > size {
				dataSize = size - pos - 8
			}
		}
		if byteRate > 0 && dataSize >= 0 {
			break
		}
		pos += 8 + n + (n & 1)
	}
	if byteRate <= 0 || dataSize < 0 {
		return 0
	}
	return dataSize * 1000 / byteRate
}

func mp3Duration(r io.ReaderAt, size int64) (ms int64, estimated bool) {
	pos := int64(0)
	if h := at(r, size, 0, 10); len(h) == 10 && string(h[:3]) == "ID3" {
		pos = 10 + (int64(h[6])<<21 | int64(h[7])<<14 | int64(h[8])<<7 | int64(h[9]))
		if h[5]&0x10 != 0 {
			pos += 10
		}
	}
	// the first frame header, allowing a little junk before it
	win := at(r, size, pos, 4096)
	off := -1
	var f *mp3Hdr
	for i := 0; i+4 <= len(win); i++ {
		if win[i] == 0xFF {
			if f = mp3Frame(win[i:]); f != nil {
				off = i
				break
			}
		}
	}
	if f == nil {
		return 0, false
	}
	frame := pos + int64(off)
	// Xing/Info header after the side information
	side := 32
	if f.version == 1 && f.channels == 1 {
		side = 17
	} else if f.version != 1 {
		side = 17
		if f.channels == 1 {
			side = 9
		}
	}
	x := at(r, size, frame+4+int64(side), 16)
	if len(x) >= 12 && (string(x[:4]) == "Xing" || string(x[:4]) == "Info") && be32(x, 4)&1 != 0 {
		if frames := int64(be32(x, 8)); frames > 0 {
			return frames * int64(f.samples) * 1000 / int64(f.sampleRate), false
		}
	}
	if v := at(r, size, frame+4+32, 18); len(v) >= 18 && string(v[:4]) == "VBRI" {
		if frames := int64(be32(v, 14)); frames > 0 {
			return frames * int64(f.samples) * 1000 / int64(f.sampleRate), false
		}
	}
	audio := size - frame
	if t := at(r, size, size-128, 3); string(t) == "TAG" {
		audio -= 128
	}
	if audio <= 0 || f.bitrate <= 0 {
		return 0, false
	}
	return audio * 8 * 1000 / int64(f.bitrate), true
}

func oggDuration(r io.ReaderAt, size int64) int64 {
	first := at(r, size, 0, 512)
	if len(first) < 28 {
		return 0
	}
	start := 27 + int(first[26])
	if start >= len(first) {
		return 0
	}
	pkt := first[start:]
	var rate int64
	var preSkip int64
	switch {
	case bytes.HasPrefix(pkt, []byte("\x01vorbis")) && len(pkt) >= 16:
		rate = int64(le32(pkt, 12))
	case bytes.HasPrefix(pkt, []byte("OpusHead")) && len(pkt) >= 12:
		rate = 48000
		preSkip = int64(le16(pkt, 10))
	default:
		return 0
	}
	if rate <= 0 {
		return 0
	}
	const tail = 65307 // largest possible Ogg page
	n := int64(tail)
	if n > size {
		n = size
	}
	t := at(r, size, size-n, int(n))
	for {
		i := bytes.LastIndex(t, []byte("OggS"))
		if i < 0 {
			return 0
		}
		if i+27 <= len(t) && t[i+4] == 0 {
			g := int64(binary.LittleEndian.Uint64(t[i+6:]))
			if g < 0 { // -1: no packet finishes on this page
				t = t[:i]
				continue
			}
			g -= preSkip
			if g < 0 {
				return 0
			}
			return g * 1000 / rate
		}
		t = t[:i]
	}
}

const maxMoov = 16 << 20

func mp4Info(r io.ReaderAt, size int64) (w, h int, durMs int64) {
	pos := int64(0)
	for i := 0; i < 4096 && pos+8 <= size; i++ {
		hd := at(r, size, pos, 16)
		if len(hd) < 8 {
			return
		}
		bsize := int64(be32(hd, 0))
		typ := string(hd[4:8])
		hdr := int64(8)
		if bsize == 1 {
			if len(hd) < 16 {
				return
			}
			bsize = int64(be64(hd, 8))
			hdr = 16
		} else if bsize == 0 {
			bsize = size - pos
		}
		if bsize < hdr || bsize > size-pos {
			return
		}
		if typ == "moov" {
			if bsize-hdr > maxMoov {
				return
			}
			body := at(r, size, pos+hdr, int(bsize-hdr))
			return parseMoov(body)
		}
		pos += bsize
	}
	return
}

// children iterates the boxes in b, calling fn(type, content) until it returns false.
func children(b []byte, fn func(typ string, body []byte) bool) {
	pos := 0
	for pos+8 <= len(b) {
		sz := int(be32(b, pos))
		hdr := 8
		if sz == 1 {
			s := be64(b, pos+8)
			if s > uint64(len(b)) {
				return
			}
			sz, hdr = int(s), 16
		} else if sz == 0 {
			sz = len(b) - pos
		}
		if sz < hdr || pos+sz > len(b) {
			return
		}
		if !fn(string(b[pos+4:pos+8]), b[pos+hdr:pos+sz]) {
			return
		}
		pos += sz
	}
}

func parseMoov(moov []byte) (w, h int, durMs int64) {
	children(moov, func(typ string, body []byte) bool {
		switch typ {
		case "mvhd":
			var ts, dur uint64
			if len(body) > 0 && body[0] == 1 {
				ts, dur = uint64(be32(body, 20)), be64(body, 24)
			} else {
				ts, dur = uint64(be32(body, 12)), uint64(be32(body, 16))
			}
			if ts > 0 && dur < 1<<50 {
				durMs = int64(dur * 1000 / ts)
			}
		case "trak":
			if w == 0 {
				children(body, func(t2 string, b2 []byte) bool {
					if t2 == "tkhd" && len(b2) > 0 {
						off := 76
						if b2[0] == 1 {
							off = 88
						}
						tw, th := int(be32(b2, off)>>16), int(be32(b2, off+4)>>16)
						if tw > 0 && th > 0 {
							w, h = tw, th
						}
						return false
					}
					return true
				})
			}
		}
		return true
	})
	return
}
