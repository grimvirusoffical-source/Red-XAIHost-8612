package sec

// glob matches s against pattern where '*' matches any run of bytes
// (including none) and '?' matches exactly one byte. Matching is
// case-sensitive and bytewise. It is iterative with single-star
// backtracking, so it is O(len(s)*len(pattern)) at worst.
func glob(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]):
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func hasWild(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '*' || s[i] == '?' {
			return true
		}
	}
	return false
}
