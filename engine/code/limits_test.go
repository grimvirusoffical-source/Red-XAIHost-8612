package code

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStepLimit(t *testing.T) {
	for _, src := range []string{
		`while true { }`,
		`let i = 0; while true { i = i + 1; }`,
		`for i in 0..1e18 { }`,
		`fn f() { while true { } } f();`,
		`let a = [1]; while true { for x in a { } }`,
	} {
		res, err := runErr(src, Limits{MaxSteps: 1000})
		if !errors.Is(err, ErrStepLimit) || !errors.Is(err, ErrLimit) {
			t.Errorf("%s: %v", src, err)
		}
		if res.Steps > 1000+2 {
			t.Errorf("%s: ran %d steps, limit 1000", src, res.Steps)
		}
	}
	// the default budget stops an unbounded loop quickly
	start := time.Now()
	_, err := runErr(`while true { }`, Limits{})
	if !errors.Is(err, ErrStepLimit) || time.Since(start) > 3*time.Second {
		t.Fatalf("default limit: %v after %v", err, time.Since(start))
	}
	// a program that fits the budget exactly still works
	res, err := runErr(`let s = 0; for i in 0..10 { s = s + i; } return s;`, Limits{MaxSteps: 200})
	if err != nil || res.Value != 45.0 || res.Steps == 0 || res.Steps > 200 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestTimeLimitAndCancel(t *testing.T) {
	// a huge step budget, so only the clock can stop it
	start := time.Now()
	res, err := runErr(`while true { }`, Limits{MaxSteps: 1 << 50, MaxDuration: 40 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, ErrLimit) {
		t.Fatalf("timeout: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v to stop a 40ms limit", d)
	}
	if res.Elapsed < 30*time.Millisecond {
		t.Fatalf("stopped early: %v", res.Elapsed)
	}
	// the context's deadline
	p := compile(t, `while true { }`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = p.Run(ctx, nil, RunOptions{Limits: Limits{MaxSteps: 1 << 50, MaxDuration: time.Minute}})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("ctx deadline: %v", err)
	}
	// cancellation from another goroutine
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel2() }()
	_, err = p.Run(ctx2, nil, RunOptions{Limits: Limits{MaxSteps: 1 << 50, MaxDuration: time.Minute}})
	if !errors.Is(err, ErrCanceled) || errors.Is(err, ErrTimeout) {
		t.Fatalf("cancel: %v", err)
	}
	// already cancelled: nothing runs to completion
	ctx3, cancel3 := context.WithCancel(context.Background())
	cancel3()
	q := compile(t, `let s = 0; for i in 0..100000 { s = s + 1; } return s;`)
	if _, err := q.Run(ctx3, nil, RunOptions{}); !errors.Is(err, ErrCanceled) {
		t.Fatalf("pre-cancelled: %v", err)
	}
}

func TestMemoryishLimits(t *testing.T) {
	big := int64(1 << 40)
	cases := []struct {
		name string
		src  string
		lim  Limits
		want error
	}{
		{"string doubling", `let s = "x"; while true { s = s + s; }`, Limits{MaxSteps: big, MaxStringLen: 1000}, ErrStringLimit},
		{"string doubling default", `let s = "x"; while true { s = s + s; }`, Limits{}, ErrStringLimit},
		{"string literal", `return "0123456789";`, Limits{MaxStringLen: 5}, ErrStringLimit},
		{"upper of long", `let s = "abcdef"; return upper(s);`, Limits{MaxStringLen: 5}, ErrStringLimit},
		{"str of array", `return str([1111111111, 2222222222, 3333333333]);`, Limits{MaxStringLen: 20}, ErrStringLimit},
		{"push growth", `let a = []; while true { push(a, 1); }`, Limits{MaxSteps: big, MaxArrayLen: 100}, ErrArrayLimit},
		{"push growth default", `let a = []; while true { push(a, 1); }`, Limits{MaxSteps: big}, ErrArrayLimit},
		{"array literal", `return [1, 2, 3, 4];`, Limits{MaxArrayLen: 3}, ErrArrayLimit},
		{"cumulative alloc (arrays)", `while true { let a = [1, 2, 3, 4, 5, 6, 7, 8]; }`, Limits{MaxSteps: big, MaxAlloc: 10000}, ErrAllocLimit},
		{"cumulative alloc (strings)", `while true { let a = "abcdefghij" + "klmnop"; }`, Limits{MaxSteps: big, MaxAlloc: 10000}, ErrAllocLimit},
		{"cumulative alloc (str)", `while true { let a = str(123456); }`, Limits{MaxSteps: big, MaxAlloc: 10000}, ErrAllocLimit},
		{"cumulative alloc (push)", `let a = []; while true { push(a, 1); pop(a); }`, Limits{MaxSteps: big, MaxAlloc: 10000}, ErrAllocLimit},
		{"depth", `fn f(n) { return f(n + 1); } return f(0);`, Limits{}, ErrDepthLimit},
		{"depth small", `fn f(n) { if n == 0 { return 0; } return 1 + f(n - 1); } return f(6);`, Limits{MaxDepth: 5}, ErrDepthLimit},
		{"mutual recursion", `fn a() { return b(); } fn b() { return a(); } return a();`, Limits{}, ErrDepthLimit},
	}
	for _, c := range cases {
		start := time.Now()
		_, err := runErr(c.src, c.lim)
		if !errors.Is(err, c.want) || !errors.Is(err, ErrLimit) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("%s: slow (%v)", c.name, time.Since(start))
		}
	}
	// within the limit it works, and the exact boundary is allowed
	if res, err := runErr(`return [1, 2, 3];`, Limits{MaxArrayLen: 3}); err != nil || len(res.Value.([]interface{})) != 3 {
		t.Fatalf("at the array limit: %v", err)
	}
	if res, err := runErr(`return "abcde";`, Limits{MaxStringLen: 5}); err != nil || res.Value != "abcde" {
		t.Fatalf("at the string limit: %v", err)
	}
	if res, err := runErr(`fn f(n) { if n == 0 { return 0; } return 1 + f(n - 1); } return f(4);`, Limits{MaxDepth: 5}); err != nil || res.Value != 4.0 {
		t.Fatalf("at the depth limit: %v", err)
	}
	// alloc is reported
	res, _ := runErr(`let a = [1, 2, 3]; return len(a);`, Limits{})
	if res.Alloc != 3*elemCost {
		t.Fatalf("alloc accounting: %d", res.Alloc)
	}
}

func TestValueExplosionIsBounded(t *testing.T) {
	// a DAG that is tiny in memory and astronomically large when expanded
	dag := `let a = [1]; for i in 0..60 { a = [a, a]; } `
	for _, src := range []string{
		dag + `return a;`,
		dag + `return str(a);`,
		`let a = [1]; push(a, a); return a;`,                 // cycle
		`let a = [1]; let b = [a]; push(a, b); return b;`,    // longer cycle
		`let a = []; for i in 0..100 { a = [a]; } return a;`, // too deep
	} {
		start := time.Now()
		_, err := runErr(src, Limits{})
		if err == nil || !errors.Is(err, ErrLimit) {
			t.Errorf("%s: want a limit error, got %v", src, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("%s: slow", src)
		}
	}
	// str() of a cycle terminates (depth-limited) without an error
	res, err := runErr(`let a = [1]; push(a, a); return len(str(a)) > 0;`, Limits{})
	if err != nil && !errors.Is(err, ErrLimit) {
		t.Fatalf("str(cycle): %v", err)
	}
	_ = res
	// comparing cycles does not recurse
	if res, err := runErr(`let a = [1]; push(a, a); let b = [1]; push(b, b); return a == b;`, Limits{}); err != nil || res.Value != false {
		t.Fatalf("cycle equality: %v %v", err, res.Value)
	}
}

func TestCompileTimeLimits(t *testing.T) {
	deep := func(open, close string, n int) string {
		return strings.Repeat(open, n) + "1" + strings.Repeat(close, n)
	}
	for name, src := range map[string]string{
		"parens":        "return " + deep("(", ")", 1000) + ";",
		"arrays":        "return " + deep("[", "]", 1000) + ";",
		"blocks":        strings.Repeat("{", 1000) + strings.Repeat("}", 1000),
		"ifs":           strings.Repeat("if true {", 1000) + strings.Repeat("}", 1000),
		"unary":         "return " + strings.Repeat("-", 5000) + "1;",
		"nots":          "return " + strings.Repeat("not ", 5000) + "true;",
		"else-if chain": "if false {} " + strings.Repeat("else if false {} ", 2000),
		"index chain":   "let a = [1]; return a" + strings.Repeat("[0]", 5000) + ";",
		"long sum":      "return 1" + strings.Repeat(" + 1", 5000) + ";",
		"huge source":   "let x = 1;" + strings.Repeat(" ", DefaultMaxSourceBytes),
		"nested calls":  "return " + strings.Repeat("abs(", 1000) + "1" + strings.Repeat(")", 1000) + ";",
		"too many args": "return min(" + strings.Repeat("1,", 100) + "1);",
	} {
		start := time.Now()
		_, err := Compile("t", src, nil, nil, Limits{})
		if err == nil {
			t.Errorf("%s: compiled", name)
		} else if !errors.Is(err, ErrBadScript) {
			t.Errorf("%s: %v", name, err)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s: slow", name)
		}
	}
	// legitimate deep-ish code still compiles
	if _, err := Compile("t", "return "+deep("(", ")", 40)+";", nil, nil, Limits{}); err != nil {
		t.Fatalf("40 levels: %v", err)
	}
	if _, err := Compile("t", "return 1"+strings.Repeat(" + 1", 500)+";", nil, nil, Limits{}); err != nil {
		t.Fatalf("500-term sum: %v", err)
	}
	// a stricter nesting ceiling applies
	if _, err := CompileWith("t", "return "+deep("(", ")", 10)+";", nil, nil, Limits{}, Limits{MaxNesting: 5}); err == nil {
		t.Fatal("ceiling nesting")
	}
	if _, err := CompileWith("t", "return 1;      ", nil, nil, Limits{}, Limits{MaxSourceBytes: 5}); err == nil {
		t.Fatal("ceiling source size")
	}
}

func TestScriptLimitsCannotRaiseTheCeiling(t *testing.T) {
	p, err := Compile("t", `let i = 0; while true { i = i + 1; }`, nil, nil, Limits{MaxSteps: 1 << 40, MaxDuration: time.Hour, MaxAlloc: 1 << 60})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Run(context.Background(), nil, RunOptions{Limits: Limits{MaxSteps: 500, MaxDuration: time.Second}})
	if !errors.Is(err, ErrStepLimit) || res.Steps > 502 {
		t.Fatalf("ceiling not enforced: %v steps=%d", err, res.Steps)
	}
	// but a script can ask for less
	q, _ := Compile("t", `while true { }`, nil, nil, Limits{MaxSteps: 50})
	res, err = q.Run(context.Background(), nil, RunOptions{Limits: Limits{MaxSteps: 5000}})
	if !errors.Is(err, ErrStepLimit) || res.Steps > 52 {
		t.Fatalf("script's lower limit ignored: %v steps=%d", err, res.Steps)
	}
	e := Effective(Limits{MaxSteps: 10, MaxDepth: 3}, Limits{MaxSteps: 100, MaxDepth: 2, MaxHostCalls: 7})
	if e.MaxSteps != 10 || e.MaxDepth != 2 || e.MaxHostCalls != 7 || e.MaxStringLen != DefaultMaxStringLen {
		t.Fatalf("Effective: %+v", e)
	}
	// the time limit as well
	r, _ := Compile("t", `while true { }`, nil, nil, Limits{MaxSteps: 1 << 50, MaxDuration: time.Hour})
	start := time.Now()
	_, err = r.Run(context.Background(), nil, RunOptions{Limits: Limits{MaxSteps: 1 << 50, MaxDuration: 30 * time.Millisecond}})
	if !errors.Is(err, ErrTimeout) || time.Since(start) > 2*time.Second {
		t.Fatalf("script raised the time ceiling: %v", err)
	}
}

func TestNumericOverflowAndSpecials(t *testing.T) {
	for _, src := range []string{`let x = 1e308; x = x * 10; return x;`, `let x = 1; while true { x = x * 1e100; }`, `return 0 * (1e308 * 10);`} {
		if _, err := runErr(src, Limits{}); err == nil {
			t.Errorf("%s: no error", src)
		}
	}
	// no NaN or Inf can ever be produced
	res, err := runErr(`return [1e308, -1e308, 5e-324];`, Limits{})
	if err != nil || len(res.Value.([]interface{})) != 3 {
		t.Fatalf("%v", err)
	}
}

func TestDeterministicAndConcurrent(t *testing.T) {
	p := compile(t, `fn f(n) { if n < 2 { return n; } return f(n - 1) + f(n - 2); } let a = []; for i in 0..10 { push(a, f(i)); } return a;`)
	first, err := p.Run(context.Background(), nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				r, err := p.Run(context.Background(), nil, RunOptions{})
				if err != nil || fmt.Sprint(r.Value) != fmt.Sprint(first.Value) || r.Steps != first.Steps || r.Alloc != first.Alloc {
					t.Errorf("run differs: %v %v", err, r.Steps)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestNoGoroutinesStarted(t *testing.T) {
	before := runtime.NumGoroutine()
	p := compile(t, `while true { }`)
	for i := 0; i < 20; i++ {
		p.Run(context.Background(), nil, RunOptions{Limits: Limits{MaxSteps: 1 << 30, MaxDuration: 2 * time.Millisecond}})
		p.Run(context.Background(), nil, RunOptions{Limits: Limits{MaxSteps: 100}})
	}
	time.Sleep(20 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines: %d before, %d after", before, after)
	}
}
