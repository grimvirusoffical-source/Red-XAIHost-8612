package query

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"redx/box"
	"redx/engine/idx"
)

// toValue converts a Go argument for a ? placeholder to a Box value.
func toValue(a interface{}, maxBytes int) (box.Value, error) {
	switch t := a.(type) {
	case nil:
		return box.Nell(), nil
	case box.Value:
		return t, nil
	case string:
		if len(t) > maxBytes {
			return box.Value{}, fmt.Errorf("string argument of %d bytes exceeds the %d byte limit", len(t), maxBytes)
		}
		if !utf8.ValidString(t) {
			return box.Value{}, fmt.Errorf("string argument is not valid UTF-8")
		}
		return box.String(t), nil
	case bool:
		return box.Bool(t), nil
	case json.Number:
		f, err := t.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return box.Value{}, fmt.Errorf("bad number %q", string(t))
		}
		return box.Number(f), nil
	case []interface{}:
		arr := make([]box.Value, len(t))
		for i, x := range t {
			v, err := toValue(x, maxBytes)
			if err != nil {
				return box.Value{}, err
			}
			arr[i] = v
		}
		return box.Array(arr...), nil
	}
	rv := reflect.ValueOf(a)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return box.Number(float64(rv.Int())), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return box.Number(float64(rv.Uint())), nil
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return box.Value{}, fmt.Errorf("NaN and Inf are not allowed")
		}
		return box.Number(f), nil
	case reflect.String:
		return toValue(rv.String(), maxBytes)
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
			break // []byte is not a Box value
		}
		arr := make([]box.Value, rv.Len())
		for i := range arr {
			v, err := toValue(rv.Index(i).Interface(), maxBytes)
			if err != nil {
				return box.Value{}, err
			}
			arr[i] = v
		}
		return box.Array(arr...), nil
	}
	return box.Value{}, fmt.Errorf("unsupported argument type %T", a)
}

func truthy(v box.Value) bool { return v.Kind == box.KindBool && v.Bool }

func valEq(a, b box.Value) bool { return a.Kind == b.Kind && a.Equal(b) }

// cmpOrder orders two values of the same orderable kind (number/number or
// string/string). ok is false for anything else.
func cmpOrder(a, b box.Value) (int, bool) {
	switch {
	case a.Kind == box.KindNumber && b.Kind == box.KindNumber:
		switch {
		case a.Num < b.Num:
			return -1, true
		case a.Num > b.Num:
			return 1, true
		}
		return 0, true
	case a.Kind == box.KindString && b.Kind == box.KindString:
		return strings.Compare(a.Str, b.Str), true
	}
	return 0, false
}

// sortRank gives ORDER BY a total order over all kinds:
// NELL < bool < number < string < array < box < link.
func sortRank(v box.Value) int {
	switch v.Kind {
	case box.KindNell:
		return 0
	case box.KindBool:
		return 1
	case box.KindNumber:
		return 2
	case box.KindString:
		return 3
	case box.KindArray:
		return 4
	case box.KindBox:
		return 5
	}
	return 6
}

func cmpSort(a, b box.Value) int {
	ra, rb := sortRank(a), sortRank(b)
	if ra != rb {
		if ra < rb {
			return -1
		}
		return 1
	}
	switch a.Kind {
	case box.KindBool:
		switch {
		case a.Bool == b.Bool:
			return 0
		case !a.Bool:
			return -1
		}
		return 1
	case box.KindNumber, box.KindString:
		c, _ := cmpOrder(a, b)
		return c
	case box.KindNell:
		return 0
	}
	return strings.Compare(a.String(), b.String())
}

func arith(op string, a, b box.Value) box.Value {
	if a.Kind != box.KindNumber || b.Kind != box.KindNumber {
		return box.Nell()
	}
	var r float64
	switch op {
	case "+":
		r = a.Num + b.Num
	case "-":
		r = a.Num - b.Num
	case "*":
		r = a.Num * b.Num
	case "/":
		if b.Num == 0 {
			return box.Nell()
		}
		r = a.Num / b.Num
	}
	if math.IsInf(r, 0) || math.IsNaN(r) {
		return box.Nell()
	}
	return box.Number(r)
}

// likePrefix interprets a LIKE pattern. Only a single trailing % is
// supported (prefix match); a pattern without % is an exact match.
func likePrefix(pat string) (prefix string, exact bool, err error) {
	i := strings.IndexByte(pat, '%')
	switch {
	case i < 0:
		return pat, true, nil
	case i == len(pat)-1:
		return pat[:i], false, nil
	}
	return "", false, fmt.Errorf("LIKE supports only a trailing %% (prefix match), as in 'abc%%'; pattern %q has a %% elsewhere", clip(pat, 40))
}

func likeMatch(v box.Value, prefix string, exact bool) bool {
	if v.Kind != box.KindString {
		return false
	}
	if exact {
		return v.Str == prefix
	}
	return strings.HasPrefix(v.Str, prefix)
}

func matchTokens(v box.Value, into map[string]bool) {
	switch v.Kind {
	case box.KindString:
		if utf8.ValidString(v.Str) {
			for _, t := range idx.Tokenize(v.Str) {
				into[t] = true
			}
		}
	case box.KindArray:
		for _, x := range v.Arr {
			matchTokens(x, into)
		}
	}
}

func matchValue(v box.Value, terms []string, any bool) bool {
	if len(terms) == 0 {
		return false
	}
	have := map[string]bool{}
	matchTokens(v, have)
	for _, t := range terms {
		if any && have[t] {
			return true
		}
		if !any && !have[t] {
			return false
		}
	}
	return !any
}

func termsOf(v box.Value) []string {
	if v.Kind != box.KindString || !utf8.ValidString(v.Str) {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range idx.Tokenize(v.Str) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// fmtValue renders a value for EXPLAIN output and error messages.
func fmtValue(v box.Value) string {
	switch v.Kind {
	case box.KindNell:
		return "NELL"
	case box.KindString:
		return "'" + strings.ReplaceAll(clip(v.Str, 40), "'", "''") + "'"
	case box.KindNumber:
		return strconv.FormatFloat(v.Num, 'g', -1, 64)
	case box.KindBool:
		if v.Bool {
			return "TRUE"
		}
		return "FALSE"
	case box.KindArray:
		return "[" + strconv.Itoa(len(v.Arr)) + " items]"
	case box.KindBox:
		return "{box}"
	}
	return "{link}"
}

// valueSize approximates the memory a value costs in a result.
func valueSize(v box.Value) int {
	switch v.Kind {
	case box.KindString:
		return 16 + len(v.Str)
	case box.KindArray:
		n := 24
		for _, x := range v.Arr {
			n += valueSize(x)
		}
		return n
	case box.KindBox:
		n := 32
		if v.Box != nil {
			for _, e := range v.Box.Entries() {
				n += 32 + len(e.Key) + valueSize(e.Value)
			}
		}
		return n
	case box.KindLink:
		if v.Link != nil {
			return 32 + len(v.Link.String())
		}
	}
	return 16
}

// exprString renders an expression for EXPLAIN. Parameters are shown with
// their bound values.
func exprString(e Expr, params []box.Value) string {
	switch x := e.(type) {
	case nil:
		return ""
	case *Lit:
		return fmtValue(x.V)
	case *Param:
		if x.N < len(params) {
			return fmtValue(params[x.N])
		}
		return "?"
	case *Field:
		return x.Raw
	case *Unary:
		return "-" + exprString(x.X, params)
	case *Not:
		return "NOT " + exprString(x.X, params)
	case *Binary:
		op := x.Op
		if op == "and" || op == "or" {
			op = strings.ToUpper(op)
		}
		return "(" + exprString(x.L, params) + " " + op + " " + exprString(x.R, params) + ")"
	case *In:
		parts := make([]string, len(x.List))
		for i, v := range x.List {
			parts[i] = exprString(v, params)
		}
		neg := ""
		if x.Neg {
			neg = "NOT "
		}
		return exprString(x.X, params) + " " + neg + "IN (" + strings.Join(parts, ", ") + ")"
	case *Like:
		neg := ""
		if x.Neg {
			neg = "NOT "
		}
		return exprString(x.X, params) + " " + neg + "LIKE " + exprString(x.Pat, params)
	case *Between:
		neg := ""
		if x.Neg {
			neg = "NOT "
		}
		return exprString(x.X, params) + " " + neg + "BETWEEN " + exprString(x.Lo, params) + " AND " + exprString(x.Hi, params)
	case *IsNell:
		if x.Neg {
			return exprString(x.X, params) + " IS NOT NELL"
		}
		return exprString(x.X, params) + " IS NELL"
	case *Match:
		fn := "MATCH"
		if x.Any {
			fn = "MATCH_ANY"
		}
		return fn + "(" + x.Field.Raw + ", " + exprString(x.Terms, params) + ")"
	}
	return "?"
}

// LikePrefixArg turns text into a LIKE argument that matches strings starting
// with exactly that text: it appends the trailing %. Text that itself
// contains % is refused, because the language has no escape for it (a % in
// user input would otherwise act as a wildcard).
func LikePrefixArg(text string) (string, error) {
	if strings.Contains(text, "%") {
		return "", fmt.Errorf("query: prefix %q contains %%, which LIKE cannot match literally", clip(text, 40))
	}
	return text + "%", nil
}
