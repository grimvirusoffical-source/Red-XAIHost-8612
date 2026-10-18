package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// JSONToBox converts a JSON value to Box-grammar literal text (best effort):
//
//	string -> "..." | number -> 42.5 | bool -> TRUE/FALSE | null -> NELL
//	array  -> ;[a, b]:  | object -> [ {key[]} = value, ... ]
//
// Clients who need exact Box syntax should send {"box": "..."} or text/x-redx-box.
func JSONToBox(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := writeBox(&sb, v, 0); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func writeBox(sb *strings.Builder, v interface{}, depth int) error {
	if depth > 64 {
		return fmt.Errorf("%w: value nested too deeply", ErrInvalid)
	}
	switch t := v.(type) {
	case nil:
		sb.WriteString("NELL")
	case bool:
		if t {
			sb.WriteString("TRUE")
		} else {
			sb.WriteString("FALSE")
		}
	case json.Number:
		sb.WriteString(t.String())
	case string:
		sb.WriteString(strconv.Quote(t))
	case []interface{}:
		sb.WriteString(";[")
		for i, e := range t {
			if i > 0 {
				sb.WriteString(", ")
			}
			if err := writeBox(sb, e, depth+1); err != nil {
				return err
			}
		}
		sb.WriteString("]:")
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("[ ")
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			if k == "" || strings.ContainsAny(k, "{}[]=,\"\\ \t\r\n") {
				return fmt.Errorf("%w: key %q is not a valid Box key", ErrInvalid, k)
			}
			sb.WriteString("{" + k + "[]} = ")
			if err := writeBox(sb, t[k], depth+1); err != nil {
				return err
			}
		}
		sb.WriteString(" ]")
	default:
		return fmt.Errorf("%w: unsupported JSON value", ErrInvalid)
	}
	return nil
}

// ValidPath performs defensive checks on engine paths received over HTTP.
func ValidPath(p string) error {
	if p == "" || len(p) > 4096 {
		return fmt.Errorf("%w: empty or too long path", ErrInvalid)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: control character in path", ErrInvalid)
		}
	}
	file := p
	if i := strings.IndexByte(p, ':'); i >= 0 {
		file = p[:i]
	}
	if strings.ContainsAny(file, `/\`) || strings.Contains(file, "..") {
		return fmt.Errorf("%w: illegal file component", ErrInvalid)
	}
	return nil
}

// ValidSubscribePath checks a subscription pattern. A subscription needs a file and a colon:
// "file.red-xai.db:Box[ID=*]", "file:*", "file:Box.Field" or "file:" (= every box of the file).
// A bare file name or an empty path can never match an event, so it is refused with an error
// instead of an idle stream.
func ValidSubscribePath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: path is required: a path pattern like file.red-xai.db:Box[ID=*] or file.red-xai.db:*", ErrInvalid)
	}
	if err := ValidPath(p); err != nil {
		return err
	}
	if file, _, ok := strings.Cut(p, ":"); !ok || file == "" {
		return fmt.Errorf("%w: %q is a bare name: subscribe to a path pattern like file.red-xai.db:Box[ID=*] or file.red-xai.db:*", ErrInvalid, p)
	}
	return nil
}
