package media

import (
	"errors"
	"fmt"
)

// Errors. Use errors.Is.
var (
	ErrInvalidName = errors.New("media: invalid name")
	ErrNotFound    = errors.New("media: not found")
	// ErrUnsupported: the bytes are not a supported audio, video or image format.
	ErrUnsupported = errors.New("media: unsupported or unrecognised content type")
	// ErrTypeMismatch: the declared type does not match what the bytes are.
	ErrTypeMismatch = errors.New("media: declared type does not match content")
	ErrEmpty        = errors.New("media: empty file")
	ErrTooLarge     = errors.New("media: too large")
	// ErrCorrupt: a manifest, chunk or file does not match its checksum or size.
	ErrCorrupt = errors.New("media: corrupt data")
	// ErrBadRange: a Range header could not be parsed (HTTP says to ignore it).
	ErrBadRange = errors.New("media: malformed range")
	// ErrMultiRange: several ranges were requested; only one is supported.
	ErrMultiRange = errors.New("media: multiple ranges are not supported")
	// ErrUnsatisfiable: the range starts at or after the end of the file.
	ErrUnsatisfiable = errors.New("media: range not satisfiable")
)

func corruptf(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, a...))
}
