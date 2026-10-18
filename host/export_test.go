package host

// ValidPackPathForTest exposes validPackPath to the external test package.
func ValidPackPathForTest(p string) bool { return validPackPath(p, false) }

// ParseRangeForTest exposes parseRange.
func ParseRangeForTest(h string, size int64) (off, n int64, ok, unsat bool) {
	return parseRange(h, size)
}
