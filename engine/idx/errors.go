package idx

import (
	"errors"
	"fmt"
)

// Query and index errors.
var (
	ErrFieldRequired = errors.New("idx: field required")
	ErrUnknownField  = errors.New("idx: unknown field")
	ErrWrongKind     = errors.New("idx: query does not match the index kind")
	ErrBadQuery      = errors.New("idx: bad query")
	ErrRebuilding    = errors.New("idx: rebuild already in progress")
	// ErrCorrupt is wrapped by snapshot errors (bad seal, truncation, bad rows).
	ErrCorrupt = errors.New("idx: corrupt snapshot")
	// ErrNotExist is returned by Blob.Load when there is no snapshot.
	ErrNotExist = errors.New("idx: snapshot does not exist")
)

func wrongKind(d Decl, want Kind) error {
	return fmt.Errorf("%w: index %s is %s, query needs %s", ErrWrongKind, d.Name, d.Kind, want)
}

func badQuery(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrBadQuery, fmt.Sprintf(format, a...))
}
