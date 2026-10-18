package code

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func compile(t testing.TB, src string, params ...string) *Program {
	t.Helper()
	p, err := Compile("t", src, params, nil, Limits{})
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	return p
}

func run(t testing.TB, src string, args ...interface{}) interface{} {
	t.Helper()
	var params []string
	for i := range args {
		params = append(params, fmt.Sprintf("p%d", i))
	}
	p := compile(t, src, params...)
	res, err := p.Run(context.Background(), args, RunOptions{})
	if err != nil {
		t.Fatalf("run %q: %v", src, err)
	}
	return res.Value
}

func runErr(src string, lim Limits, args ...interface{}) (Result, error) {
	var params []string
	for i := range args {
		params = append(params, fmt.Sprintf("p%d", i))
	}
	p, err := Compile("t", src, params, nil, Limits{})
	if err != nil {
		return Result{}, err
	}
	return p.Run(context.Background(), args, RunOptions{Limits: lim})
}

func TestLanguage(t *testing.T) {
	cases := []struct {
		src  string
		want interface{}
	}{
		{`return 1 + 2 * 3 - 4 / 2;`, 5.0},
		{`return (1 + 2) * 3;`, 9.0},
		{`return -2 * 3;`, -6.0},
		{`return - -3;`, 3.0},
		{`return 7 % 3;`, 1.0},
		{`return -7 % 3;`, -1.0},
		{`return 7.5 % 2;`, 1.5},
		{`return 1e3 + 2.5e-1;`, 1000.25},
		{`return 10 / 4;`, 2.5},
		{`return "a" + "b" + "c";`, "abc"},
		{`return "tab\there\n\"q\" \\ nul\0end";`, "tab\there\n\"q\" \\ nul\x00end"},
		{`return len("héllo");`, 5.0},
		{`return len([1, 2, 3]);`, 3.0},
		{`return substr("héllo", 1, 3);`, "éll"},
		{`return substr("hello", 3);`, "lo"},
		{`return substr("hello", 10);`, ""},
		{`return substr("hello", -5, 2);`, "he"},
		{`return substr("hello", 1, -2);`, ""},
		{`return find("hello", "ll");`, 2.0},
		{`return find("héllo", "l");`, 2.0},
		{`return find("hello", "z");`, -1.0},
		{`return find([5, 6, 7], 7);`, 2.0},
		{`return contains("hello", "ell");`, true},
		{`return contains([1, 2], 3);`, false},
		{`return upper("abc") + lower("DEF");`, "ABCdef"},
		{`return str(12);`, "12"},
		{`return str(1.5);`, "1.5"},
		{`return str(true) + str(nil);`, "truenil"},
		{`return str([1, "a", [true, nil]]);`, `[1, "a", [true, nil]]`},
		{`return "n=" + str(3 * 4);`, "n=12"},
		{`return num("12.5");`, 12.5},
		{`return num(" 7 ");`, 7.0},
		{`return num("abc");`, nil},
		{`return num("0x10");`, nil},
		{`return num("inf");`, nil},
		{`return num("NaN");`, nil},
		{`return num("1e400");`, nil},
		{`return num("");`, nil},
		{`return num(true) + num(false);`, 1.0},
		{`return num([1]);`, nil},
		{`return type(1) + type("") + type(nil) + type(true) + type([]);`, "numberstringnilboolarray"},
		{`return abs(-3) + floor(2.7) + ceil(2.1) + round(2.5);`, 3.0 + 2 + 3 + 3},
		{`return min(3, 2) * 10 + max(3, 2);`, 23.0},
		{`return sqrt(16);`, 4.0},
		{`return 1 < 2 and 2 <= 2 and 3 > 2 and 3 >= 3 and 1 != 2 and 1 == 1;`, true},
		{`return "a" < "b" and "b" >= "b" and "abc" < "abd";`, true},
		{`return not true or not false;`, true},
		{`return !false && !(1 > 2);`, true},
		{`return false and (1 / 0 == 1);`, false}, // short circuit
		{`return true or (1 / 0 == 1);`, true},
		{`return nil == nil;`, true},
		{`return 1 == "1";`, false},
		{`return nil == false;`, false},
		{`return "a" == "a" and 1 == 1 and true == true;`, true},
		{`return [1] == [1];`, false}, // arrays are equal only if identical
		{`let a = [1]; let b = a; return a == b;`, true},
		{`let a = [1]; let b = a; push(b, 2); return len(a);`, 2.0}, // shared by reference
		{`let a = [1, 2, 3]; a[1] = 20; return a;`, []interface{}{1.0, 20.0, 3.0}},
		{`let a = [[1, 2], [3]]; a[0][1] = 9; return a[0][1] + a[1][0];`, 12.0},
		{`let a = []; push(a, 1); push(a, "x"); return [pop(a), pop(a), pop(a)];`, []interface{}{"x", 1.0, nil}},
		{`return [];`, []interface{}{}},
		{`return [1, [2, [3]]];`, []interface{}{1.0, []interface{}{2.0, []interface{}{3.0}}}},
		{`return;`, nil},
		{`let x = 1;`, nil},
		{`if true { return 1; } return 2;`, 1.0},
		{`if false { return 1; } else if false { return 2; } else { return 3; }`, 3.0},
		{`if false { return 1; } else if true { return 2; } else { return 3; }`, 2.0},
		{`let n = 0; while n < 10 { n = n + 1; } return n;`, 10.0},
		{`let n = 0; let s = 0; while true { n = n + 1; if n > 10 { break; } if n % 2 == 0 { continue; } s = s + n; } return s;`, 25.0},
		{`let s = 0; for i in 0..5 { s = s + i; } return s;`, 10.0},
		{`let s = 0; for i in 5..0 { s = s + 1; } return s;`, 0.0},
		{`let s = 0; for x in [10, 20, 30] { s = s + x; } return s;`, 60.0},
		{`let s = 0; for i in 0..4 { for j in 0..4 { if j == 2 { break; } s = s + 1; } } return s;`, 8.0},
		{`for i in 0..10 { if i == 3 { return i; } } return -1;`, 3.0},
		{`let a = [1, 2]; let n = 0; for x in a { push(a, x); n = n + 1; } return n;`, 2.0}, // length fixed at loop start
		{`let s = 0; for i in 0..3 { i = i + 10; s = s + i; } return s;`, 33.0},
		{`let x = 1; { let x = 2; } return x;`, 1.0},
		{`let x = 1; { x = 2; } return x;`, 2.0},
		{`let x = 1; let x = 2; return x;`, 2.0},
		{`let r = 0; if true { let t = 5; r = t; } return r;`, 5.0},
		{`fn fact(n) { if n <= 1 { return 1; } return n * fact(n - 1); } return fact(10);`, 3628800.0},
		{`fn fib(n) { if n < 2 { return n; } return fib(n - 1) + fib(n - 2); } return fib(12);`, 144.0},
		{`return later(2); fn later(x) { return x * 2; }`, 4.0}, // functions are hoisted
		{`fn even(n) { if n == 0 { return true; } return odd(n - 1); } fn odd(n) { if n == 0 { return false; } return even(n - 1); } return even(10);`, true},
		{`fn noret() { let x = 1; } return noret();`, nil},
		{`fn fill(a) { push(a, 1); } let a = []; fill(a); fill(a); return len(a);`, 2.0},
		{`// comment
		  let x = 1; // trailing
		  return x + 1;`, 2.0},
		{`return "// not a comment";`, "// not a comment"},
		{`let a = [1,2,3]; let i = 0; let s = 0; while i < len(a) { s = s + a[i]; i = i + 1; } return s;`, 6.0},
	}
	for _, c := range cases {
		got := run(t, c.src)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s\n  got  %#v\n  want %#v", c.src, got, c.want)
		}
	}
}

func TestParamsAndArguments(t *testing.T) {
	if got := run(t, `return p0 + p1;`, 2, 3.5); got != 5.5 {
		t.Fatalf("%v", got)
	}
	if got := run(t, `return p0 + "!" ;`, "hi"); got != "hi!" {
		t.Fatalf("%v", got)
	}
	if got := run(t, `return len(p0) + p0[1];`, []interface{}{1, 20, 300}); got != 23.0 {
		t.Fatalf("%v", got)
	}
	if got := run(t, `return [p0, p1, p2];`, nil, true, []string{"a"}); !reflect.DeepEqual(got, []interface{}{nil, true, []interface{}{"a"}}) {
		t.Fatalf("%#v", got)
	}
	// arguments are copied in: the script cannot change the caller's slice
	in := []interface{}{1.0, 2.0}
	p := compile(t, `p0[0] = 99; return p0;`, "p0")
	res, err := p.Run(context.Background(), []interface{}{in}, RunOptions{})
	if err != nil || in[0] != 1.0 || res.Value.([]interface{})[0] != 99.0 {
		t.Fatalf("aliasing: %v %v %v", err, in, res.Value)
	}
	// wrong count, bad types
	if _, err := p.Run(context.Background(), nil, RunOptions{}); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("count: %v", err)
	}
	for _, bad := range []interface{}{map[string]int{}, struct{}{}, make(chan int), func() {}, [][]int{}, 1 + 1i} {
		if _, err := p.Run(context.Background(), []interface{}{bad}, RunOptions{}); !errors.Is(err, ErrBadArguments) {
			t.Errorf("%T: %v", bad, err)
		}
	}
	if _, err := p.Run(context.Background(), []interface{}{[]interface{}{1.0, map[string]int{}}}, RunOptions{}); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("nested bad: %v", err)
	}
	nan := func() float64 { z := 0.0; return z / z }()
	if _, err := p.Run(context.Background(), []interface{}{nan}, RunOptions{}); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("NaN arg: %v", err)
	}
	if _, err := Compile("t", "return 1;", []string{"a", "a"}, nil, Limits{}); err == nil {
		t.Fatal("duplicate params")
	}
	if _, err := Compile("t", "return 1;", []string{"if"}, nil, Limits{}); err == nil {
		t.Fatal("keyword param")
	}
	if _, err := Compile("t", "return 1;", []string{"a b"}, nil, Limits{}); err == nil {
		t.Fatal("bad param name")
	}
}

func TestRuntimeErrors(t *testing.T) {
	bad := map[string]string{
		`return 1 + "a";`:                             "cannot apply",
		`return "a" + 1;`:                             "cannot add",
		`return "a" - "b";`:                           "cannot apply",
		`return "a" < 1;`:                             "cannot compare",
		`return [1] + [2];`:                           "cannot apply",
		`return nil + 1;`:                             "cannot apply",
		`if 1 { }`:                                    "condition",
		`while "x" { }`:                               "condition",
		`return not 1;`:                               "not needs",
		`return 1 and true;`:                          "condition",
		`return -"a";`:                                "cannot negate",
		`return [1][1];`:                              "outside the array",
		`return [1][-1];`:                             "outside the array",
		`return [1][0.5];`:                            "whole number",
		`return [1]["a"];`:                            "whole number",
		`return nil[0];`:                              "only arrays",
		`return "abc"[0];`:                            "only arrays",
		`let a = [1]; a[3] = 1;`:                      "outside the array",
		`let a = 5; a[0] = 1;`:                        "only arrays",
		`return 1 / 0;`:                               "division by zero",
		`return 1 % 0;`:                               "remainder",
		`return 1e308 * 10;`:                          "numeric overflow",
		`return 1e308 + 1e308;`:                       "numeric overflow",
		`return sqrt(-1);`:                            "negative",
		`return sqrt("x");`:                           "number",
		`return len(5);`:                              "len()",
		`return push(1, 2);`:                          "push()",
		`return pop("a");`:                            "pop()",
		`return substr(1, 2);`:                        "substr",
		`return substr("a", 1.5);`:                    "whole",
		`return find(1, 2);`:                          "find()",
		`return find("a", 1);`:                        "needs a string",
		`return abs("a");`:                            "number",
		`return upper(1);`:                            "string",
		`return min("a", 1);`:                         "numbers",
		`return nothere;`:                             "undefined variable",
		`x = 1;`:                                      "undeclared",
		`let a = 1; { let b = 2; } return b;`:         "undefined variable",
		`for i in "abc" { }`:                          "array or a range",
		`for i in 0..1.5 { }`:                         "whole numbers",
		`for i in nil..3 { }`:                         "whole numbers",
		`fn f() { return g; } let g = 1; return f();`: "undefined variable", // no globals inside functions
	}
	for src, want := range bad {
		_, err := runErr(src, Limits{})
		var re *RuntimeError
		if !errors.As(err, &re) {
			t.Errorf("%s: want RuntimeError, got %v", src, err)
			continue
		}
		if !strings.Contains(re.Error(), want) {
			t.Errorf("%s: error %q lacks %q", src, re.Error(), want)
		}
		if re.Line < 1 || re.Col < 1 || re.Script != "t" {
			t.Errorf("%s: no position: %+v", src, re)
		}
	}
	// positions point at the offending token
	_, err := runErr("let a = 1;\nlet b = a +\n  \"x\";\n", Limits{})
	var re *RuntimeError
	if !errors.As(err, &re) || re.Line != 2 || re.Col != 11 {
		t.Fatalf("position: %v", err)
	}
}

func TestSyntaxErrors(t *testing.T) {
	bad := map[string]string{
		`let x = 1`:                        "expected \";\"",
		`let x = ;`:                        "unexpected",
		`return 1 +;`:                      "unexpected",
		`let 5 = 1;`:                       "variable name",
		`let if = 1;`:                      "variable name",
		`let fn = 1;`:                      "variable name",
		`1 = 2;`:                           "cannot assign",
		`f() = 2;`:                         "cannot assign",
		`"a`:                               "unterminated",
		"\"a\nb\"":                         "unterminated",
		`"bad \q escape"`:                  "escape",
		`let x = @;`:                       "unexpected character",
		`let x = $y;`:                      "unexpected character",
		"let x = `a`;":                     "unexpected character",
		`let x = 'a';`:                     "unexpected character",
		`let x = 1abc;`:                    "malformed number",
		`let x = 1e;`:                      "malformed number",
		`let x = 0x10;`:                    "malformed number",
		`let x = 1e999;`:                   "out of range",
		`if true { `:                       "missing }",
		`if true 1;`:                       "expected \"{\"",
		`else { }`:                         "else without if",
		`while { }`:                        "unexpected",
		`break;`:                           "outside a loop",
		`continue;`:                        "outside a loop",
		`if true { break; }`:               "outside a loop",
		`while true { fn f() { } }`:        "top level",
		`if true { fn f() { } }`:           "top level",
		`fn f() { } fn f() { }`:            "declared twice",
		`fn f(a, a) { }`:                   "duplicate parameter",
		`fn f(a b) { }`:                    "expected \",\"",
		`fn len() { }`:                     "collides",
		`fn f(x) { } return f();`:          "takes 1 arguments",
		`fn f() { } return f(1);`:          "takes 0 arguments",
		`return len();`:                    "takes 1 arguments",
		`return len(1, 2);`:                "takes 1 arguments",
		`return substr("a");`:              "takes 2 to 3 arguments",
		`return nosuch(1);`:                "unknown function",
		`return read_file("x");`:           "unknown function",
		`return eval("1");`:                "unknown function",
		`return import("os");`:             "unknown function",
		`return exec("ls");`:               "unknown function",
		`return (len)(1);`:                 "expected \";\"",
		`let a = [len]; return a[0]("x");`: "expected \";\"",
		`return a.b;`:                      "unexpected character",
		`return a::b;`:                     "unexpected character",
		`return [1, 2;`:                    "expected",
		`return (1;`:                       "expected \")\"",
		`return 1 2;`:                      "expected \";\"",
		`let x = fn;`:                      "unexpected",
		`return true(1);`:                  "expected \";\"",
		`for i in { }`:                     "unexpected",
		"let x = \xff;":                    "UTF-8",
		`return "a" "b";`:                  "expected \";\"",
	}
	for src, want := range bad {
		_, err := Compile("t", src, nil, nil, Limits{})
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("%q: want SyntaxError, got %v", src, err)
			continue
		}
		if !errors.Is(err, ErrBadScript) {
			t.Errorf("%q: should wrap ErrBadScript", src)
		}
		if !strings.Contains(se.Error(), want) {
			t.Errorf("%q: error %q lacks %q", src, se.Error(), want)
		}
		if se.Line < 1 || se.Col < 1 {
			t.Errorf("%q: no position", src)
		}
	}
	_, err := Compile("t", "let a = 1;\nlet b = ;", nil, nil, Limits{})
	var se *SyntaxError
	if !errors.As(err, &se) || se.Line != 2 || se.Col != 9 || se.Script != "t" {
		t.Fatalf("position: %v", err)
	}
}
