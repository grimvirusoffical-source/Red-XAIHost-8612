package media

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestSniff(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		mime string
		typ  Type
	}{
		{"png", makePNG(4, 3, 10), "image/png", TypeImage},
		{"jpeg", makeJPEG(10, 5, 100), "image/jpeg", TypeImage},
		{"gif87", append([]byte("GIF87a"), make([]byte, 10)...), "image/gif", TypeImage},
		{"gif89", makeGIF(2, 2), "image/gif", TypeImage},
		{"webp lossy", makeWebPLossy(8, 8), "image/webp", TypeImage},
		{"webp lossless", makeWebPLossless(8, 8), "image/webp", TypeImage},
		{"webp extended", makeWebPExtended(8, 8), "image/webp", TypeImage},
		{"wav", makeWAV(8000, 100, 100), "audio/wav", TypeAudio},
		{"mp3 frames", makeMP3(3, -1, 0), "audio/mpeg", TypeAudio},
		{"mp3 id3", makeMP3(3, 200, 0), "audio/mpeg", TypeAudio},
		{"ogg vorbis", makeOggVorbis(44100, 44100), "audio/ogg", TypeAudio},
		{"ogg opus", makeOggOpus(312, 48000), "audio/ogg", TypeAudio},
		{"ogg theora", makeOggTheora(), "video/ogg", TypeVideo},
		{"mp4 isom", makeMP4("isom", 64, 48, 1000, 1000, false, 10), "video/mp4", TypeVideo},
		{"mp4 mp42", makeMP4("mp42", 64, 48, 1000, 1000, false, 10), "video/mp4", TypeVideo},
		{"m4a", makeMP4("M4A ", 0, 0, 1000, 1000, false, 10), "audio/mp4", TypeAudio},
	}
	for _, c := range cases {
		s, err := Sniff(c.data)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s.MIME != c.mime || s.Type != c.typ || s.Ext == "" {
			t.Errorf("%s: got %+v", c.name, s)
		}
		// only the first SniffLen bytes matter
		if len(c.data) > 64 {
			if s2, err := Sniff(c.data[:64]); err != nil || s2.MIME != c.mime {
				// an ID3 tag longer than the window is accepted without checking the frame
				t.Errorf("%s (64 byte head): %v %v", c.name, s2.MIME, err)
			}
		}
	}
	// generic mp4 brands may be audio-only
	s, _ := Sniff(makeMP4("isom", 0, 0, 1, 1, false, 1))
	if !s.Accepts("audio/mp4") || !s.Accepts("video/mp4") || s.Accepts("video/ogg") {
		t.Fatalf("mp4 acceptance: %+v", s)
	}
	s, _ = Sniff(makeMP4("M4A ", 0, 0, 1, 1, false, 1))
	if s.Accepts("video/mp4") {
		t.Fatal("M4A brand must not be accepted as video")
	}
}

func TestSniffRejects(t *testing.T) {
	bad := map[string][]byte{
		"empty":            nil,
		"one byte":         {0x89},
		"text":             []byte("hello, this is a text file, not media at all"),
		"html":             []byte("<!DOCTYPE html><html><body>x</body></html>"),
		"svg":              []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"),
		"zeros":            make([]byte, 64),
		"png truncated":    []byte("\x89PNG\r\n\x1a"),
		"png bad sig":      []byte("\x89PNG\r\n\x1b\n0000"),
		"gif bad version":  []byte("GIF90a0000"),
		"riff avi":         riff("AVI ", []byte("LIST0000")),
		"riff short":       []byte("RIFF\x00\x00\x00\x00WAV"),
		"adts aac":         {0xFF, 0xF1, 0x50, 0x80, 0x01, 0x7F, 0xFC, 0, 0, 0},
		"mp3 free format":  {0xFF, 0xFB, 0x00, 0x00},
		"mp3 bad bitrate":  {0xFF, 0xFB, 0xF0, 0x00},
		"mp3 bad rate":     {0xFF, 0xFB, 0x9C, 0x00},
		"mp3 layer II":     {0xFF, 0xFD, 0x90, 0x00},
		"mp3 reserved ver": {0xFF, 0xEB, 0x90, 0x00},
		"id3 bad size":     []byte("ID3\x03\x00\x00\xff\xff\xff\xff0000"),
		"id3 then junk":    append(append([]byte("ID3\x03\x00\x00\x00\x00\x00\x02"), 0, 0), []byte("not a frame at all")...),
		"ogg unknown":      oggPage(2, 0, 1, 0, []byte("unknowncodec....")),
		"ogg no bos":       oggPage(0, 0, 1, 0, []byte("\x01vorbis")),
		"ogg short":        []byte("OggS\x00"),
		"quicktime":        makeMP4("qt  ", 0, 0, 1, 1, false, 1),
		"heic":             makeMP4("heic", 0, 0, 1, 1, false, 1),
		"avif":             makeMP4("avif", 0, 0, 1, 1, false, 1),
		"ftyp tiny box":    append(u32be(8), []byte("ftypisom0000")...),
		"ftyp bad brand":   cat(u32be(24), []byte("ftyp"), []byte{0x01, 0x02, 0x03, 0x04}, make([]byte, 12)),
		"exe":              []byte("MZ\x90\x00\x03\x00\x00\x00"),
		"zip":              []byte("PK\x03\x04\x14\x00"),
		"pdf":              []byte("%PDF-1.7\n"),
	}
	for name, b := range bad {
		if s, err := Sniff(b); err == nil {
			t.Errorf("%s: sniffed as %+v, want ErrUnsupported", name, s)
		}
	}
}

func TestNormalizeMIME(t *testing.T) {
	cases := map[string]string{
		"image/png": "image/png", " IMAGE/PNG ": "image/png", "image/png; charset=binary": "image/png",
		"image/jpg": "image/jpeg", "audio/x-wav": "audio/wav", "audio/mp3": "audio/mpeg", "audio/x-m4a": "audio/mp4",
		"application/ogg": "audio/ogg",
		"":                "", "png": "", "image/": "", "/png": "", "a/b/c": "", "image/ png": "", "image\n/png": "",
	}
	for in, want := range cases {
		if got := NormalizeMIME(in); got != want {
			t.Errorf("NormalizeMIME(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProbeValues(t *testing.T) {
	type want struct {
		w, h     int
		ms       int64
		estimate bool
	}
	cases := []struct {
		name string
		data []byte
		want want
	}{
		{"png", makePNG(640, 480, 1000), want{w: 640, h: 480}},
		{"gif", makeGIF(33, 17), want{w: 33, h: 17}},
		{"jpeg with big exif", makeJPEG(1920, 1080, 30000), want{w: 1920, h: 1080}},
		{"webp lossy", makeWebPLossy(321, 123), want{w: 321, h: 123}},
		{"webp lossless", makeWebPLossless(1000, 777), want{w: 1000, h: 777}},
		{"webp extended", makeWebPExtended(4000, 3000), want{w: 4000, h: 3000}},
		{"wav 2s", makeWAV(1000, 2000, 2000), want{ms: 2000}},
		{"wav streaming size", makeWAV(1000, 1500, 0xFFFFFFFF), want{ms: 1500}},
		{"wav data larger than file", makeWAV(1000, 500, 5000), want{ms: 500}},
		{"mp3 cbr estimate", makeMP3(100, -1, 0), want{ms: int64(100*417) * 8 * 1000 / 128000, estimate: true}},
		{"mp3 cbr id3", makeMP3(100, 500, 0), want{ms: int64(100*417) * 8 * 1000 / 128000, estimate: true}},
		{"mp3 xing", makeMP3(10, 300, 1000), want{ms: 1000 * 1152 * 1000 / 44100}},
		{"ogg vorbis", makeOggVorbis(44100, 44100*3), want{ms: 3000}},
		{"ogg opus preskip", makeOggOpus(312, 48000*2+312), want{ms: 2000}},
		{"ogg theora (unknown)", makeOggTheora(), want{}},
		{"mp4 moov first", makeMP4("isom", 1280, 720, 1000, 5500, false, 100), want{w: 1280, h: 720, ms: 5500}},
		{"mp4 moov last", makeMP4("mp42", 640, 360, 600, 3000, true, 200000), want{w: 640, h: 360, ms: 5000}},
		{"m4a audio only", makeMP4("M4A ", 0, 0, 1000, 2500, false, 10), want{ms: 2500}},
	}
	for _, c := range cases {
		sn, err := Sniff(c.data)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		in := Probe(sn, bytes.NewReader(c.data), int64(len(c.data)))
		if in.Width != c.want.w || in.Height != c.want.h || in.DurationMs != c.want.ms || in.DurationEstimated != c.want.estimate {
			t.Errorf("%s: got %+v, want %+v", c.name, in, c.want)
		}
	}
}

// Probe must not panic or loop on damaged input.
func TestProbeGarbage(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	seeds := [][]byte{
		makePNG(10, 10, 20), makeGIF(5, 5), makeJPEG(10, 10, 300), makeWebPLossy(5, 5), makeWebPLossless(5, 5),
		makeWebPExtended(5, 5), makeWAV(1000, 300, 300), makeMP3(5, 100, 0), makeMP3(5, 100, 5), makeOggVorbis(44100, 100),
		makeOggOpus(10, 1000), makeMP4("isom", 64, 64, 1000, 1000, true, 100), makeMP4("isom", 64, 64, 1000, 1000, false, 100),
	}
	for iter := 0; iter < 4000; iter++ {
		b := append([]byte(nil), seeds[iter%len(seeds)]...)
		for k := rng.Intn(6); k >= 0; k-- {
			switch rng.Intn(4) {
			case 0:
				b[rng.Intn(len(b))] = byte(rng.Intn(256))
			case 1:
				b = b[:rng.Intn(len(b))+1]
			case 2:
				i := rng.Intn(len(b))
				b[i] ^= 0xFF
			case 3:
				i := rng.Intn(len(b))
				for j := i; j < len(b) && j < i+4; j++ {
					b[j] = 0xFF
				}
			}
		}
		sn, err := Sniff(b)
		if err != nil {
			continue
		}
		in := Probe(sn, bytes.NewReader(b), int64(len(b)))
		if in.Width < 0 || in.Height < 0 || in.DurationMs < 0 || (in.Width == 0) != (in.Height == 0) {
			t.Fatalf("iteration %d: nonsense probe result %+v", iter, in)
		}
	}
	// declared sizes far beyond the data
	huge := cat(u32be(0xFFFFFFF0), []byte("moov"), make([]byte, 50))
	huge = append(makeMP4("isom", 0, 0, 1, 1, false, 0)[:24], huge...)
	sn, err := Sniff(huge)
	if err != nil {
		t.Fatal(err)
	}
	Probe(sn, bytes.NewReader(huge), int64(len(huge)))
}
