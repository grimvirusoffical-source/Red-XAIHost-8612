package code

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"redx/box"
)

// Script values are: nil, bool, float64, string and *array. (Go side:
// nil, bool, float64, string, []any.)
type value = interface{}

// array is a script array. Arrays are shared by reference, like in most
// scripting languages, so a script can build cyclic structures; every
// conversion out of the sandbox is therefore depth- and size-bounded.
type array struct{ elems []value }

func typeName(v value) string {
	switch v.(type) {
	case nil:
		return "nil"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case *array:
		return "array"
	}
	return "unknown"
}

const maxConvDepth = 32

// MaxConvertNodes bounds how many values one conversion (arguments to a
// host function, a result, str()) may visit. It stops an array that
// contains itself, or shares sub-arrays, from exploding on conversion.
const MaxConvertNodes = 100_000

type conv struct {
	nodes     int
	maxString int
	onStack   map[*array]bool
}

// toGo converts a script value to plain Go values with fresh slices.
func (c *conv) toGo(v value, depth int) (interface{}, error) {
	if c.nodes++; c.nodes > MaxConvertNodes {
		return nil, ErrValueTooLarge
	}
	switch t := v.(type) {
	case nil, bool, float64:
		return t, nil
	case string:
		return t, nil
	case *array:
		if depth >= maxConvDepth {
			return nil, fmt.Errorf("%w: nesting deeper than %d", ErrValueTooLarge, maxConvDepth)
		}
		if c.onStack[t] {
			return nil, fmt.Errorf("%w: array contains itself", ErrValueTooLarge)
		}
		if c.onStack == nil {
			c.onStack = map[*array]bool{}
		}
		c.onStack[t] = true
		out := make([]interface{}, len(t.elems))
		for i, e := range t.elems {
			x, err := c.toGo(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		delete(c.onStack, t)
		return out, nil
	}
	return nil, fmt.Errorf("%w: unsupported value", ErrInternal)
}

// ToGo converts a script result (never needed by callers directly; Result
// does it) with the standard bounds.
func fromGo(x interface{}, lim Limits, depth int, nodes *int) (value, error) {
	if *nodes++; *nodes > MaxConvertNodes {
		return nil, ErrValueTooLarge
	}
	switch t := x.(type) {
	case nil:
		return nil, nil
	case bool:
		return t, nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, fmt.Errorf("%w: NaN or infinite number", ErrBadArguments)
		}
		return t, nil
	case float32:
		return fromGo(float64(t), lim, depth, nodes)
	case int:
		return float64(t), nil
	case int8:
		return float64(t), nil
	case int16:
		return float64(t), nil
	case int32:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case uint:
		return float64(t), nil
	case uint8:
		return float64(t), nil
	case uint16:
		return float64(t), nil
	case uint32:
		return float64(t), nil
	case uint64:
		return float64(t), nil
	case string:
		if len(t) > lim.MaxStringLen {
			return nil, ErrStringLimit
		}
		return t, nil
	case []interface{}:
		return fromSlice(len(t), func(i int) interface{} { return t[i] }, lim, depth, nodes)
	case []string:
		return fromSlice(len(t), func(i int) interface{} { return t[i] }, lim, depth, nodes)
	case []float64:
		return fromSlice(len(t), func(i int) interface{} { return t[i] }, lim, depth, nodes)
	case []int:
		return fromSlice(len(t), func(i int) interface{} { return t[i] }, lim, depth, nodes)
	case []bool:
		return fromSlice(len(t), func(i int) interface{} { return t[i] }, lim, depth, nodes)
	case box.Value:
		return fromBox(t, lim, depth, nodes)
	}
	return nil, fmt.Errorf("%w: unsupported Go type %T", ErrBadArguments, x)
}

func fromSlice(n int, at func(int) interface{}, lim Limits, depth int, nodes *int) (value, error) {
	if depth >= maxConvDepth {
		return nil, fmt.Errorf("%w: nesting deeper than %d", ErrValueTooLarge, maxConvDepth)
	}
	if n > lim.MaxArrayLen {
		return nil, ErrArrayLimit
	}
	a := &array{elems: make([]value, n)}
	for i := 0; i < n; i++ {
		v, err := fromGo(at(i), lim, depth+1, nodes)
		if err != nil {
			return nil, err
		}
		a.elems[i] = v
	}
	return a, nil
}

// fromBox converts Box scalars and arrays. Boxes and links are refused:
// scripts get plain data, not references into the database.
func fromBox(v box.Value, lim Limits, depth int, nodes *int) (value, error) {
	switch v.Kind {
	case box.KindNell:
		return nil, nil
	case box.KindString:
		return fromGo(v.Str, lim, depth, nodes)
	case box.KindNumber:
		return fromGo(v.Num, lim, depth, nodes)
	case box.KindBool:
		return v.Bool, nil
	case box.KindArray:
		return fromSlice(len(v.Arr), func(i int) interface{} { return v.Arr[i] }, lim, depth, nodes)
	}
	return nil, fmt.Errorf("%w: %s values cannot be passed to scripts", ErrBadArguments, v.Kind)
}

// FromGo converts a Go value (nil, bool, numbers, string, []any, []string,
// []float64, []int, []bool, box.Value without boxes or links) to the form
// scripts use. It is exported for tests and tools; Run does it for you.
func FromGo(x interface{}) (interface{}, error) {
	n := 0
	v, err := fromGo(x, Limits{}.WithDefaults(), 0, &n)
	if err != nil {
		return nil, err
	}
	c := &conv{}
	return c.toGo(v, 0)
}

// ToBox converts a Go result (nil, bool, float64, string, []any) to a box.Value.
func ToBox(x interface{}) (box.Value, error) {
	switch t := x.(type) {
	case nil:
		return box.Nell(), nil
	case bool:
		return box.Bool(t), nil
	case float64:
		return box.Number(t), nil
	case string:
		return box.String(t), nil
	case []interface{}:
		out := make([]box.Value, len(t))
		for i, e := range t {
			v, err := ToBox(e)
			if err != nil {
				return box.Value{}, err
			}
			out[i] = v
		}
		return box.Array(out...), nil
	}
	return box.Value{}, fmt.Errorf("%w: unsupported Go type %T", ErrBadArguments, x)
}

func fmtNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// show renders a value for str(), within maxLen bytes and the node budget.
func show(v value, maxLen int, nodes *int, depth int, sb *strings.Builder) error {
	if *nodes++; *nodes > MaxConvertNodes {
		return ErrValueTooLarge
	}
	if sb.Len() > maxLen {
		return ErrStringLimit
	}
	switch t := v.(type) {
	case nil:
		sb.WriteString("nil")
	case bool:
		sb.WriteString(strconv.FormatBool(t))
	case float64:
		sb.WriteString(fmtNumber(t))
	case string:
		if depth == 0 {
			sb.WriteString(t)
		} else {
			sb.WriteString(strconv.Quote(t))
		}
	case *array:
		if depth >= maxConvDepth {
			sb.WriteString("[...]")
			return nil
		}
		sb.WriteByte('[')
		for i, e := range t.elems {
			if i > 0 {
				sb.WriteString(", ")
			}
			if err := show(e, maxLen, nodes, depth+1, sb); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
	}
	if sb.Len() > maxLen {
		return ErrStringLimit
	}
	return nil
}
