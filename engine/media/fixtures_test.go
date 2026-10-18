package media

import (
	"bytes"
	"encoding/binary"
)

func u32be(v uint32) []byte      { return binary.BigEndian.AppendUint32(nil, v) }
func u32le(v uint32) []byte      { return binary.LittleEndian.AppendUint32(nil, v) }
func u16le(v uint16) []byte      { return binary.LittleEndian.AppendUint16(nil, v) }
func u16be(v uint16) []byte      { return binary.BigEndian.AppendUint16(nil, v) }
func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func makePNG(w, h int, pad int) []byte {
	ihdr := cat(u32be(uint32(w)), u32be(uint32(h)), []byte{8, 2, 0, 0, 0})
	return cat([]byte("\x89PNG\r\n\x1a\n"), u32be(13), []byte("IHDR"), ihdr, u32be(0),
		u32be(uint32(pad)), []byte("IDAT"), bytes.Repeat([]byte{0x5a}, pad), u32be(0),
		u32be(0), []byte("IEND"), u32be(0xAE426082))
}

func makeGIF(w, h int) []byte {
	return cat([]byte("GIF89a"), u16le(uint16(w)), u16le(uint16(h)), []byte{0, 0, 0}, []byte{0x3b})
}

// JPEG with an oversized APP1 (EXIF-like) segment before the frame header.
func makeJPEG(w, h int, exif int) []byte {
	app0 := cat([]byte{0xFF, 0xE0}, u16be(16), []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))
	app1 := cat([]byte{0xFF, 0xE1}, u16be(uint16(exif+2)), bytes.Repeat([]byte{0x11}, exif))
	sof := cat([]byte{0xFF, 0xC0}, u16be(17), []byte{8}, u16be(uint16(h)), u16be(uint16(w)),
		[]byte{3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1})
	sos := cat([]byte{0xFF, 0xDA}, u16be(12), []byte{3, 1, 0, 2, 0x11, 3, 0x11, 0, 0x3f, 0}, bytes.Repeat([]byte{0x77}, 40))
	return cat([]byte{0xFF, 0xD8}, app0, app1, sof, sos, []byte{0xFF, 0xD9})
}

func riff(form string, body []byte) []byte {
	return cat([]byte("RIFF"), u32le(uint32(4+len(body))), []byte(form), body)
}

func makeWebPLossy(w, h int) []byte {
	// frame tag(3) + start code + w(2) + h(2)
	data := cat([]byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a}, u16le(uint16(w)), u16le(uint16(h)), bytes.Repeat([]byte{0}, 20))
	return riff("WEBP", cat([]byte("VP8 "), u32le(uint32(len(data))), data))
}

func makeWebPLossless(w, h int) []byte {
	v := uint32(w-1) | uint32(h-1)<<14
	data := cat([]byte{0x2f}, u32le(v), bytes.Repeat([]byte{0}, 20))
	return riff("WEBP", cat([]byte("VP8L"), u32le(uint32(len(data))), data))
}

func makeWebPExtended(w, h int) []byte {
	cw, ch := w-1, h-1
	data := cat([]byte{0x10, 0, 0, 0}, []byte{byte(cw), byte(cw >> 8), byte(cw >> 16)}, []byte{byte(ch), byte(ch >> 8), byte(ch >> 16)})
	return riff("WEBP", cat([]byte("VP8X"), u32le(uint32(len(data))), data))
}

// 16-bit stereo 44.1 kHz PCM with `seconds` of audio (data is scaled down by
// using a smaller declared rate so the fixture stays small).
func makeWAV(byteRate, dataLen int, declaredData int) []byte {
	fmtc := cat(u16le(1), u16le(1), u32le(uint32(byteRate)), u32le(uint32(byteRate)), u16le(1), u16le(8))
	body := cat([]byte("fmt "), u32le(16), fmtc, []byte("LIST"), u32le(5), []byte("abcde"), []byte{0},
		[]byte("data"), u32le(uint32(declaredData)), bytes.Repeat([]byte{0x80}, dataLen))
	return riff("WAVE", body)
}

// MPEG-1 Layer III, 128 kbit/s, 44.1 kHz, stereo: 417-byte frames.
func mp3Frames(n int, xingFrames int) []byte {
	var out []byte
	for i := 0; i < n; i++ {
		f := make([]byte, 417)
		copy(f, []byte{0xFF, 0xFB, 0x90, 0x00})
		if i == 0 && xingFrames > 0 {
			copy(f[4+32:], "Xing")
			copy(f[4+32+4:], u32be(1))
			copy(f[4+32+8:], u32be(uint32(xingFrames)))
		}
		out = append(out, f...)
	}
	return out
}

func syncsafe(n int) []byte {
	return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

func makeMP3(frames int, id3 int, xing int) []byte {
	var out []byte
	if id3 >= 0 {
		out = cat([]byte("ID3\x03\x00\x00"), syncsafe(id3), bytes.Repeat([]byte{0}, id3))
	}
	return cat(out, mp3Frames(frames, xing))
}

func oggPage(flags byte, granule int64, serial, seq uint32, packet []byte) []byte {
	h := cat([]byte("OggS"), []byte{0, flags}, binary.LittleEndian.AppendUint64(nil, uint64(granule)),
		u32le(serial), u32le(seq), u32le(0), []byte{1, byte(len(packet))})
	return cat(h, packet)
}

func makeOggVorbis(rate int, lastGranule int64) []byte {
	id := cat([]byte("\x01vorbis"), u32le(0), []byte{2}, u32le(uint32(rate)), u32le(0), u32le(128000), u32le(0), []byte{0xb8, 1})
	return cat(oggPage(2, 0, 7, 0, id), oggPage(0, 100, 7, 1, bytes.Repeat([]byte{1}, 50)), oggPage(4, lastGranule, 7, 2, bytes.Repeat([]byte{2}, 30)))
}

func makeOggOpus(preSkip int, lastGranule int64) []byte {
	id := cat([]byte("OpusHead"), []byte{1, 2}, u16le(uint16(preSkip)), u32le(48000), u16le(0), []byte{0})
	return cat(oggPage(2, 0, 9, 0, id), oggPage(4, lastGranule, 9, 1, bytes.Repeat([]byte{3}, 30)))
}

func makeOggTheora() []byte {
	return oggPage(2, 0, 3, 0, cat([]byte("\x80theora"), bytes.Repeat([]byte{0}, 30)))
}

func mp4box(typ string, body ...[]byte) []byte {
	b := cat(body...)
	return cat(u32be(uint32(8+len(b))), []byte(typ), b)
}

func makeMP4(brand string, w, h int, timescale, duration uint32, moovAtEnd bool, mdat int) []byte {
	ftyp := mp4box("ftyp", []byte(brand), u32be(0), []byte(brand))
	mvhd := mp4box("mvhd", []byte{0, 0, 0, 0}, u32be(0), u32be(0), u32be(timescale), u32be(duration), bytes.Repeat([]byte{0}, 80))
	// tkhd v0: ver/flags(4) ctime(4) mtime(4) id(4) res(4) dur(4) res(8) layer(2) alt(2) vol(2) res(2) matrix(36) w(4) h(4)
	tkhd := mp4box("tkhd", []byte{0, 0, 0, 7}, u32be(0), u32be(0), u32be(1), u32be(0), u32be(duration), bytes.Repeat([]byte{0}, 8),
		bytes.Repeat([]byte{0}, 8), bytes.Repeat([]byte{0}, 36), u32be(uint32(w)<<16), u32be(uint32(h)<<16))
	audioTkhd := mp4box("tkhd", []byte{0, 0, 0, 7}, u32be(0), u32be(0), u32be(2), u32be(0), u32be(duration), bytes.Repeat([]byte{0}, 8),
		bytes.Repeat([]byte{0}, 8), bytes.Repeat([]byte{0}, 36), u32be(0), u32be(0))
	moov := mp4box("moov", mvhd, mp4box("trak", audioTkhd), mp4box("trak", tkhd))
	if w == 0 {
		moov = mp4box("moov", mvhd, mp4box("trak", audioTkhd))
	}
	mdatBox := mp4box("mdat", bytes.Repeat([]byte{0xAB}, mdat))
	if moovAtEnd {
		return cat(ftyp, mdatBox, moov)
	}
	return cat(ftyp, moov, mdatBox)
}
