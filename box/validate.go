package box

import "fmt"

// File modes accepted by Validate.
const (
	ModeCold = "db"    // .red-xai.db
	ModeQS   = "qs"    // .red-xai-db-qs
	ModeQSLS = "qs-ls" // .red-xai-db-qs-ls
)

// Validate checks a parsed document against the rules of a file mode and
// returns every violation (positions are those of the offending entry).
//
//	db:    no TTL markers at all.
//	qs:    every leaf entry carries (N) or (~N) — or sits under an ancestor
//	       that does (a sub-box TTL expires the whole subtree); non-empty
//	       container boxes without a TTL are structural and allowed; no '(N)*'.
//	qs-ls: plain entries (immediate journal flush), '(N)*' checkpoint
//	       markers, and optional (N)/(~N) expiry.
func Validate(root *Box, mode string) []error {
	var errs []error
	var walk func(b *Box, covered bool, path string)
	walk = func(b *Box, covered bool, path string) {
		for _, e := range b.Entries() {
			p := path + e.Key
			at := func(msg string, a ...interface{}) {
				errs = append(errs, &Error{e.Line, e.Col, fmt.Sprintf("%s: %s", p, fmt.Sprintf(msg, a...))})
			}
			has := e.TTL.Expiring()
			switch mode {
			case ModeCold:
				if e.TTL.Kind != TTLNone {
					at("TTL/checkpoint markers are not allowed in .red-xai.db files")
				}
			case ModeQS:
				if e.TTL.Kind == TTLCheckpoint {
					at("checkpoint marker (N)* is only valid in .red-xai-db-qs-ls files")
				}
				structural := e.Value.Kind == KindBox && e.Value.Box != nil && e.Value.Box.Len() > 0
				if !has && !covered && !structural {
					at("every entry in a .red-xai-db-qs file must carry (N) or (~N)")
				}
			case ModeQSLS:
				// (N)/(~N) expiry and (N)* checkpoint markers are both allowed
			default:
				at("unknown mode %q", mode)
			}
			if e.Value.Kind == KindBox && e.Value.Box != nil {
				walk(e.Value.Box, covered || has, p+".")
			}
		}
	}
	walk(root, false, "")
	return errs
}
