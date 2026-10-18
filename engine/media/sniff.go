package media

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// Type is the broad class of an item.
type Type string

const (
	TypeImage Type = "image"
	TypeAudio Type = "audio"
	TypeVideo Type = "video"
)

// SniffLen is how many leading bytes Sniff needs to decide (more is ignored).
const SniffLen = 4096

// Sniffed is the result of looking at the first bytes of a file.
type Sniffed struct {
	Type Type
	MIME string // canonical MIME type
	// Also lists other MIME types a caller may legitimately declare for the
	// same bytes. An MP4 with a generic brand may hold only audio, and an
	// Ogg stream of an unknown codec may be audio or video.
	Also []string
	Ext  string // canonical extension with the dot
}

// Accepts reports whether a declared MIME type (already normalised) matches.
func (s Sniffed) Accepts(mime string) bool {
	if mime == s.MIME {
		return true
	}
	for _, a := range s.Also {
		if a == mime {
			return true
		}
	}
	return false
}

// mimeAliases maps common non-canonical spellings to the canonical type.
var mimeAliases = map[string]string{
	"image/jpg":       "image/jpeg",
	"image/pjpeg":     "image/jpeg",
	"audio/mp3":       "audio/mpeg",
	"audio/mpeg3":     "audio/mpeg",
	"audio/x-mpeg":    "audio/mpeg",
	"audio/x-mp3":     "audio/mpeg",
	"audio/x-wav":     "audio/wav",
	"audio/wave":      "audio/wav",
	"audio/vnd.wave":  "audio/wav",
	"audio/x-m4a":     "audio/mp4",
	"audio/m4a":       "audio/mp4",
	"application/ogg": "audio/ogg",
}

// NormalizeMIME lower-cases, trims, strips parameters ("; charset=...") and
// maps well-known aliases to the canonical name. It returns "" for input
// that is not of the form type/subtype.
func NormalizeMIME(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(s, " \t\r\n") {
		return ""
	}
	if c, ok := mimeAliases[s]; ok {
		return c
	}
	return s
}

// Sniff identifies head (the first bytes of a file) from its magic numbers.
// It returns ErrUnsupported for anything else, including formats that look
// like media but are not on the list (QuickTime, HEIC, AVIF, AAC ADTS...).
func Sniff(head []byte) (Sniffed, error) {
	b := head
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return Sniffed{Type: TypeImage, MIME: "image/png", Ext: ".png"}, nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return Sniffed{Type: TypeImage, MIME: "image/jpeg", Ext: ".jpg"}, nil
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return Sniffed{Type: TypeImage, MIME: "image/gif", Ext: ".gif"}, nil
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return Sniffed{Type: TypeImage, MIME: "image/webp", Ext: ".webp"}, nil
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return Sniffed{Type: TypeAudio, MIME: "audio/wav", Ext: ".wav"}, nil
	case bytes.HasPrefix(b, []byte("OggS")):
		return sniffOgg(b)
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return sniffMP4(b)
	}
	if isMP3(b) {
		return Sniffed{Type: TypeAudio, MIME: "audio/mpeg", Ext: ".mp3"}, nil
	}
	return Sniffed{}, ErrUnsupported
}

func sniffOgg(b []byte) (Sniffed, error) {
	// The first page holds the codec's identification packet right after the segment table.
	if len(b) < 27 || b[4] != 0 || b[5]&0x02 == 0 { // version 0, beginning-of-stream flag
		return Sniffed{}, ErrUnsupported
	}
	nseg := int(b[26])
	start := 27 + nseg
	if start > len(b) {
		return Sniffed{}, ErrUnsupported
	}
	pkt := b[start:]
	switch {
	case bytes.HasPrefix(pkt, []byte("\x01vorbis")), bytes.HasPrefix(pkt, []byte("OpusHead")),
		bytes.HasPrefix(pkt, []byte("\x7fFLAC")), bytes.HasPrefix(pkt, []byte("Speex   ")):
		ext := ".ogg"
		if bytes.HasPrefix(pkt, []byte("OpusHead")) {
			ext = ".opus"
		}
		return Sniffed{Type: TypeAudio, MIME: "audio/ogg", Ext: ext}, nil
	case bytes.HasPrefix(pkt, []byte("\x80theora")):
		return Sniffed{Type: TypeVideo, MIME: "video/ogg", Ext: ".ogv"}, nil
	}
	if len(pkt) >= 8 && (bytes.HasPrefix(pkt, []byte("fishead")) || bytes.HasPrefix(pkt, []byte("\x80kate"))) {
		// Skeleton or other container-level stream: audio or video.
		return Sniffed{Type: TypeAudio, MIME: "audio/ogg", Also: []string{"video/ogg"}, Ext: ".ogg"}, nil
	}
	return Sniffed{}, ErrUnsupported
}

func sniffMP4(b []byte) (Sniffed, error) {
	size := binary.BigEndian.Uint32(b[:4])
	if size != 1 && size < 16 {
		return Sniffed{}, ErrUnsupported
	}
	brand := string(b[8:12])
	switch brand {
	case "M4A ", "M4B ", "M4P ":
		return Sniffed{Type: TypeAudio, MIME: "audio/mp4", Ext: ".m4a"}, nil
	case "M4V ", "M4VH", "M4VP":
		return Sniffed{Type: TypeVideo, MIME: "video/mp4", Ext: ".mp4"}, nil
	case "qt  ", "heic", "heix", "hevc", "hevx", "mif1", "msf1", "avif", "avis", "crx ":
		return Sniffed{}, ErrUnsupported // QuickTime, HEIF/AVIF stills, Canon raw
	}
	// isom, iso2..iso6, mp41, mp42, avc1, dash, 3gp*, MSNV ...: a generic
	// MP4 family brand. It is video unless it only holds audio tracks.
	for _, c := range brand {
		if c < 0x20 || c > 0x7e {
			return Sniffed{}, ErrUnsupported
		}
	}
	return Sniffed{Type: TypeVideo, MIME: "video/mp4", Also: []string{"audio/mp4"}, Ext: ".mp4"}, nil
}

// MPEG audio layer III: ID3v2 tag or a valid frame header at the start.
func isMP3(b []byte) bool {
	if len(b) >= 10 && string(b[:3]) == "ID3" && b[3] != 0xFF && b[4] != 0xFF &&
		b[6]&0x80 == 0 && b[7]&0x80 == 0 && b[8]&0x80 == 0 && b[9]&0x80 == 0 && b[3] >= 2 && b[3] <= 4 {
		size := int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9])
		next := 10 + size
		if b[5]&0x10 != 0 {
			next += 10 // footer
		}
		if next+4 <= len(b) { // the frame after the tag is in view: check it
			return mp3Frame(b[next:]) != nil
		}
		return true
	}
	return mp3Frame(b) != nil
}

type mp3Hdr struct {
	version    int // 1, 2 (MPEG-2) or 25 (MPEG-2.5)
	bitrate    int // bits per second
	sampleRate int
	padding    int
	channels   int
	samples    int // samples per frame
	frameLen   int
}

var (
	mp3BitratesV1L3 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mp3BitratesV2L3 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	mp3Rates        = map[int][3]int{1: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 25: {11025, 12000, 8000}}
)

// mp3Frame parses an MPEG audio layer III frame header, nil if invalid.
func mp3Frame(b []byte) *mp3Hdr {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return nil
	}
	vbits := (b[1] >> 3) & 3
	layer := (b[1] >> 1) & 3
	if vbits == 1 || layer != 1 { // reserved version; only layer III
		return nil
	}
	h := &mp3Hdr{}
	switch vbits {
	case 3:
		h.version = 1
	case 2:
		h.version = 2
	case 0:
		h.version = 25
	}
	bi := int(b[2] >> 4)
	si := int(b[2]>>2) & 3
	if bi == 0 || bi == 15 || si == 3 {
		return nil
	}
	if h.version == 1 {
		h.bitrate = mp3BitratesV1L3[bi] * 1000
		h.samples = 1152
	} else {
		h.bitrate = mp3BitratesV2L3[bi] * 1000
		h.samples = 576
	}
	h.sampleRate = mp3Rates[h.version][si]
	h.padding = int(b[2]>>1) & 1
	h.channels = 2
	if b[3]>>6 == 3 {
		h.channels = 1
	}
	coef := 144
	if h.version != 1 {
		coef = 72
	}
	h.frameLen = coef*h.bitrate/h.sampleRate + h.padding
	if h.frameLen < 4 {
		return nil
	}
	return h
}
