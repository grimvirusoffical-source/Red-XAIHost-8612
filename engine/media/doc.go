// Package media implements the `.red-xai-media` format: audio, video and
// image blobs with a manifest Box, stored as chunks in engine/pack.
//
// Each item has
//
//   - a data file in the pack (content-addressed 1 MiB chunks, so identical
//     chunks are shared), and
//   - a sealed manifest Box (type, MIME, size, SHA-256, dimensions and
//     duration when they could be read) saved as a small file in the pack.
//
// The content type is sniffed from the first bytes (PNG, JPEG, GIF, WebP,
// MP3, WAV, OGG, MP4). A declared type that disagrees with the sniffed one
// is rejected before anything is stored. There is no transcoding.
//
// Reader gives byte-range access: it implements io.Reader, io.Seeker and
// io.ReaderAt, fetches and verifies only the chunks a range touches, and can
// be handed to net/http.ServeContent for Range requests.
//
// Nothing here is wired to the real storage engines or the host yet.
package media
