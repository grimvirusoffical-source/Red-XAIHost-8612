package code

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// builtin is a pure function available to every script. Built-ins touch
// nothing outside the interpreter.
type builtin struct {
	name     string
	min, max int
	fn       func(m *machine, c *callExpr, args []value) (value, error)
}

func (b *builtin) arity() string {
	if b.min == b.max {
		return strconv.Itoa(b.min)
	}
	return strconv.Itoa(b.min) + " to " + strconv.Itoa(b.max)
}

var builtins map[string]*builtin

func init() {
	list := []*builtin{
		{"len", 1, 1, biLen},
		{"str", 1, 1, biStr},
		{"num", 1, 1, biNum},
		{"type", 1, 1, func(m *machine, c *callExpr, a []value) (value, error) { return typeName(a[0]), nil }},
		{"push", 2, 2, biPush},
		{"pop", 1, 1, biPop},
		{"abs", 1, 1, numFn(math.Abs)},
		{"floor", 1, 1, numFn(math.Floor)},
		{"ceil", 1, 1, numFn(math.Ceil)},
		{"round", 1, 1, numFn(math.Round)},
		{"sqrt", 1, 1, biSqrt},
		{"min", 2, 2, biMinMax(false)},
		{"max", 2, 2, biMinMax(true)},
		{"substr", 2, 3, biSubstr},
		{"find", 2, 2, biFind},
		{"contains", 2, 2, biContains},
		{"upper", 1, 1, strFn(strings.ToUpper)},
		{"lower", 1, 1, strFn(strings.ToLower)},
	}
	builtins = make(map[string]*builtin, len(list))
	for _, b := range list {
		builtins[b.name] = b
	}
}

func biLen(m *machine, c *callExpr, a []value) (value, error) {
	switch t := a[0].(type) {
	case string:
		return float64(utf8.RuneCountInString(t)), m.charge(int64(len(t)/256), c.pos)
	case *array:
		return float64(len(t.elems)), nil
	}
	return nil, m.fail(c.pos, "len() needs a string or an array, got %s", typeName(a[0]))
}

func biStr(m *machine, c *callExpr, a []value) (value, error) {
	var sb strings.Builder
	n := 0
	if err := show(a[0], m.lim.MaxStringLen, &n, 0, &sb); err != nil {
		return nil, m.failErr(c.pos, "str() result", err)
	}
	if err := m.charge(int64(n), c.pos); err != nil {
		return nil, err
	}
	return sb.String(), m.allocString(sb.Len(), c.pos)
}

func biNum(m *machine, c *callExpr, a []value) (value, error) {
	switch t := a[0].(type) {
	case float64:
		return t, nil
	case bool:
		if t {
			return 1.0, nil
		}
		return 0.0, nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" || len(s) > 64 || strings.Trim(s, "0123456789+-.eE") != "" {
			return nil, nil // not a plain decimal number: nil, so scripts can test for it
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, nil
		}
		return f, nil
	}
	return nil, nil
}

func biPush(m *machine, c *callExpr, a []value) (value, error) {
	arr, ok := a[0].(*array)
	if !ok {
		return nil, m.fail(c.pos, "push() needs an array as its first argument, got %s", typeName(a[0]))
	}
	if len(arr.elems)+1 > m.lim.MaxArrayLen {
		return nil, m.failErr(c.pos, "push()", ErrArrayLimit)
	}
	if err := m.allocCells(1, c.pos); err != nil {
		return nil, err
	}
	arr.elems = append(arr.elems, a[1])
	return float64(len(arr.elems)), nil
}

func biPop(m *machine, c *callExpr, a []value) (value, error) {
	arr, ok := a[0].(*array)
	if !ok {
		return nil, m.fail(c.pos, "pop() needs an array, got %s", typeName(a[0]))
	}
	if len(arr.elems) == 0 {
		return nil, nil
	}
	v := arr.elems[len(arr.elems)-1]
	arr.elems[len(arr.elems)-1] = nil
	arr.elems = arr.elems[:len(arr.elems)-1]
	return v, nil
}

func numFn(f func(float64) float64) func(*machine, *callExpr, []value) (value, error) {
	return func(m *machine, c *callExpr, a []value) (value, error) {
		x, ok := a[0].(float64)
		if !ok {
			return nil, m.fail(c.pos, "expected a number, got %s", typeName(a[0]))
		}
		return f(x), nil
	}
}

func strFn(f func(string) string) func(*machine, *callExpr, []value) (value, error) {
	return func(m *machine, c *callExpr, a []value) (value, error) {
		s, ok := a[0].(string)
		if !ok {
			return nil, m.fail(c.pos, "expected a string, got %s", typeName(a[0]))
		}
		if err := m.charge(int64(len(s)/64), c.pos); err != nil {
			return nil, err
		}
		r := f(s)
		return r, m.allocString(len(r), c.pos)
	}
}

func biSqrt(m *machine, c *callExpr, a []value) (value, error) {
	x, ok := a[0].(float64)
	if !ok {
		return nil, m.fail(c.pos, "sqrt() needs a number, got %s", typeName(a[0]))
	}
	if x < 0 {
		return nil, m.fail(c.pos, "sqrt() of a negative number")
	}
	return math.Sqrt(x), nil
}

func biMinMax(max bool) func(*machine, *callExpr, []value) (value, error) {
	return func(m *machine, c *callExpr, a []value) (value, error) {
		x, ok1 := a[0].(float64)
		y, ok2 := a[1].(float64)
		if !ok1 || !ok2 {
			return nil, m.fail(c.pos, "min/max need two numbers")
		}
		if (x > y) == max {
			return x, nil
		}
		return y, nil
	}
}

// substr(s, start[, count]) counts runes; out-of-range values are clipped.
func biSubstr(m *machine, c *callExpr, a []value) (value, error) {
	s, ok := a[0].(string)
	st, ok2 := a[1].(float64)
	if !ok || !ok2 || st != math.Trunc(st) {
		return nil, m.fail(c.pos, "substr(string, start[, count]) needs a string and whole numbers")
	}
	rs := []rune(s)
	if err := m.charge(int64(len(rs)/64), c.pos); err != nil {
		return nil, err
	}
	n := float64(len(rs))
	if len(a) == 3 {
		cnt, ok3 := a[2].(float64)
		if !ok3 || cnt != math.Trunc(cnt) {
			return nil, m.fail(c.pos, "substr count must be a whole number")
		}
		n = cnt
	}
	if st < 0 {
		st = 0
	}
	if st > float64(len(rs)) {
		st = float64(len(rs))
	}
	end := st + n
	if n < 0 {
		end = st
	}
	if end > float64(len(rs)) {
		end = float64(len(rs))
	}
	out := string(rs[int(st):int(end)])
	return out, m.allocString(len(out), c.pos)
}

// find(string, sub) is the rune index of sub or -1; find(array, x) the index or -1.
func biFind(m *machine, c *callExpr, a []value) (value, error) {
	switch t := a[0].(type) {
	case string:
		sub, ok := a[1].(string)
		if !ok {
			return nil, m.fail(c.pos, "find(string, sub) needs a string")
		}
		if err := m.charge(int64(len(t)/64), c.pos); err != nil {
			return nil, err
		}
		i := strings.Index(t, sub)
		if i < 0 {
			return -1.0, nil
		}
		return float64(utf8.RuneCountInString(t[:i])), nil
	case *array:
		if err := m.charge(int64(len(t.elems)), c.pos); err != nil {
			return nil, err
		}
		for i, e := range t.elems {
			if equal(e, a[1]) {
				return float64(i), nil
			}
		}
		return -1.0, nil
	}
	return nil, m.fail(c.pos, "find() needs a string or an array, got %s", typeName(a[0]))
}

func biContains(m *machine, c *callExpr, a []value) (value, error) {
	r, err := biFind(m, c, a)
	if err != nil {
		return nil, err
	}
	return r.(float64) >= 0, nil
}
