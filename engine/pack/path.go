package pack

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Path limits.
const (
	MaxNameBytes = 255
	MaxPathBytes = 4096
	MaxDepth     = 64
)

var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
}

func isReserved(comp string) bool {
	base := comp
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimRight(base, " "))
	if windowsReserved[base] {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
		return true
	}
	return false
}

func badPath(p, why string) error {
	return fmt.Errorf("%w: %q: %s", ErrInvalidPath, p, why)
}

// checkName validates one path component.
func checkName(comp string) error {
	switch {
	case comp == "":
		return fmt.Errorf("empty name")
	case comp == "." || comp == "..":
		return fmt.Errorf("%q is not allowed", comp)
	case len(comp) > MaxNameBytes:
		return fmt.Errorf("name longer than %d bytes", MaxNameBytes)
	case !utf8.ValidString(comp):
		return fmt.Errorf("name is not valid UTF-8")
	}
	for _, r := range comp {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return fmt.Errorf("control character U+%04X", r)
		case strings.ContainsRune(`/\:<>"|?*`, r):
			return fmt.Errorf("character %q is not allowed", r)
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0xfeff:
			return fmt.Errorf("bidirectional or invisible control character U+%04X", r)
		}
	}
	if strings.HasSuffix(comp, ".") || strings.HasSuffix(comp, " ") {
		return fmt.Errorf("name must not end with a dot or space")
	}
	if isReserved(comp) {
		return fmt.Errorf("%q is a reserved device name on Windows", comp)
	}
	return nil
}

// CleanPath validates a pack path and returns its components. Paths are
// relative, use "/" as the separator and have no empty, "." or ".." parts.
// Rejected: absolute paths, backslashes, drive letters and other ':'
// (also NTFS alternate streams), control characters, invalid UTF-8,
// `<>"|?*`, bidirectional overrides, names ending in '.' or ' ', Windows
// reserved device names (CON, NUL, COM1, ...), names over 255 bytes, paths
// over 4096 bytes or 64 levels deep. "" and "/" mean the root and return no
// components; callers that need a file name must check for that.
//
// There is no Unicode normalisation: names that differ only in case or
// normalisation form are different names.
func CleanPath(p string) ([]string, error) {
	if p == "" || p == "/" {
		return nil, nil
	}
	if len(p) > MaxPathBytes {
		return nil, badPath(p[:32]+"...", "path too long")
	}
	if strings.HasPrefix(p, "/") {
		return nil, badPath(p, "absolute paths are not allowed")
	}
	comps := strings.Split(p, "/")
	if len(comps) > MaxDepth {
		return nil, badPath(p, "path too deep")
	}
	for _, c := range comps {
		if err := checkName(c); err != nil {
			return nil, badPath(p, err.Error())
		}
	}
	return comps, nil
}

func joinPath(comps []string) string { return strings.Join(comps, "/") }
