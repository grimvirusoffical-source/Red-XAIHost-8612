package code

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"redx/engine/sec"
)

// A generator of well-typed programs, to look for crashes, hangs and
// unbounded growth. Variables: x number, p bool, b string, a array.
// Errors are expected (index out of range, overflow, limits); panics
// (ErrInternal) never are.
type gen struct {
	r     *rand.Rand
	sb    strings.Builder
	depth int
}

func (g *gen) pick(xs ...string) string { return xs[g.r.Intn(len(xs))] }

func (g *gen) num() string {
	g.depth++
	defer func() { g.depth-- }()
	if g.depth > 5 {
		return g.pick("x", "1", "2", "0.5", "7")
	}
	switch g.r.Intn(10) {
	case 0, 1:
		return g.pick("x", "1", "2", "0.5", "7", "1e308", "-3")
	case 2, 3, 4:
		return "(" + g.num() + " " + g.pick("+", "-", "*", "/", "%") + " " + g.num() + ")"
	case 5:
		return g.pick("len(a)", "len(b)", "len("+g.str()+")", "len("+g.arr()+")")
	case 6:
		return g.pick("abs", "floor", "ceil", "round") + "(" + g.num() + ")"
	case 7:
		return g.pick("min", "max") + "(" + g.num() + ", " + g.num() + ")"
	case 8:
		return "f(" + g.num() + ")"
	default:
		return "find(" + g.pick("b", "a", g.str(), g.arr()) + ", " + g.any() + ")"
	}
}

func (g *gen) str() string {
	g.depth++
	defer func() { g.depth-- }()
	if g.depth > 5 {
		return g.pick("b", `"s"`, `""`)
	}
	switch g.r.Intn(7) {
	case 0, 1:
		return g.pick("b", `"s"`, `""`, `"é"`, `"hello"`)
	case 2, 3:
		return "(" + g.str() + " + " + g.str() + ")"
	case 4:
		return "str(" + g.any() + ")"
	case 5:
		return g.pick("upper", "lower") + "(" + g.str() + ")"
	default:
		return "substr(" + g.str() + ", " + g.num() + ", " + g.num() + ")"
	}
}

func (g *gen) arr() string {
	g.depth++
	defer func() { g.depth-- }()
	if g.depth > 5 {
		return g.pick("a", "[]")
	}
	switch g.r.Intn(4) {
	case 0:
		return g.pick("a", "[]")
	case 1:
		return "[" + g.any() + ", " + g.any() + "]"
	case 2:
		return "[" + g.arr() + ", a]"
	default:
		return "a"
	}
}

func (g *gen) boolean() string {
	g.depth++
	defer func() { g.depth-- }()
	if g.depth > 5 {
		return g.pick("p", "true", "false")
	}
	switch g.r.Intn(8) {
	case 0:
		return g.pick("p", "true", "false")
	case 1, 2:
		return "(" + g.num() + " " + g.pick("<", "<=", ">", ">=", "==", "!=") + " " + g.num() + ")"
	case 3:
		return "(" + g.str() + " " + g.pick("<", "==", "!=") + " " + g.str() + ")"
	case 4, 5:
		return "(" + g.boolean() + " " + g.pick("and", "or", "&&") + " " + g.boolean() + ")"
	case 6:
		return "not " + g.boolean()
	default:
		return "contains(" + g.pick("a", "b") + ", " + g.any() + ")"
	}
}

func (g *gen) any() string {
	switch g.r.Intn(6) {
	case 0:
		return g.num()
	case 1:
		return g.str()
	case 2:
		return g.boolean()
	case 3:
		return g.arr()
	case 4:
		return "nil"
	default:
		return "h(" + g.num() + ")"
	}
}

func (g *gen) stmts(n int) {
	g.depth++
	defer func() { g.depth-- }()
	for i := 0; i < n; i++ {
		if g.depth > 4 {
			g.sb.WriteString("x = " + g.num() + ";\n")
			continue
		}
		switch g.r.Intn(14) {
		case 0:
			g.sb.WriteString("x = " + g.num() + ";\n")
		case 1:
			g.sb.WriteString("b = " + g.str() + ";\n")
		case 2:
			g.sb.WriteString("p = " + g.boolean() + ";\n")
		case 3:
			g.sb.WriteString("a = " + g.arr() + ";\n")
		case 4:
			g.sb.WriteString("if " + g.boolean() + " {\n")
			g.stmts(1 + g.r.Intn(2))
			g.sb.WriteString("} else {\n")
			g.stmts(1)
			g.sb.WriteString("}\n")
		case 5:
			g.sb.WriteString("while " + g.boolean() + " {\n")
			g.stmts(1 + g.r.Intn(3))
			g.sb.WriteString("}\n")
		case 6:
			g.sb.WriteString("for i in 0.." + fmt.Sprint(g.r.Intn(30)) + " {\n")
			g.stmts(1 + g.r.Intn(2))
			g.sb.WriteString("if " + g.boolean() + " { break; }\n}\n")
		case 7:
			g.sb.WriteString("for e in a {\n")
			g.stmts(1)
			g.sb.WriteString("}\n")
		case 8:
			g.sb.WriteString("push(a, " + g.any() + ");\n")
		case 9:
			g.sb.WriteString("a[" + g.num() + "] = " + g.any() + ";\n")
		case 10:
			g.sb.WriteString("while x < " + fmt.Sprint(g.r.Intn(2000)) + " { x = x + 1; " + g.pick("b = b + b;", "push(a, x);", "a = [a, a];", "") + " }\n")
		case 11:
			g.sb.WriteString("return " + g.any() + ";\n")
		default:
			g.sb.WriteString(g.any() + ";\n")
		}
	}
}

func genProgram(seed int64) string {
	g := &gen{r: rand.New(rand.NewSource(seed))}
	g.sb.WriteString("fn f(n) { if n > 3 { return n; } return f(n + 1) + 1; }\n")
	g.sb.WriteString("let a = [1, 2, 3]; let b = \"s\"; let x = 0; let p = true;\n")
	g.stmts(3 + g.r.Intn(6))
	return g.sb.String()
}

func fuzzHost() (*Host, RunOptions) {
	pol, _ := sec.Parse(`{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "exec", {Resource[]} = "host:h" ];`)
	h, _ := NewHost(HostFunc{Name: "h", Fn: func(ctx context.Context, a []interface{}) (interface{}, error) { return a[0], nil }})
	return h, RunOptions{Auth: pol, Host: h, Limits: Limits{MaxSteps: 3000, MaxDuration: 100 * time.Millisecond, MaxAlloc: 1 << 16, MaxHostCalls: 20, MaxStringLen: 4096, MaxArrayLen: 256, MaxDepth: 8}}
}

func runFuzzed(t testing.TB, src string) (ok bool, err error) {
	p, cerr := Compile("fuzz", src, nil, []string{"h"}, Limits{})
	if cerr != nil {
		return false, cerr
	}
	_, o := fuzzHost()
	start := time.Now()
	_, err = p.Run(context.Background(), nil, o)
	if errors.Is(err, ErrInternal) {
		t.Fatalf("internal error (a bug) for program:\n%s\n%v", src, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("run took %v for program:\n%s", d, src)
	}
	return true, err
}

func TestGeneratedProgramsNeverCrashOrHang(t *testing.T) {
	compiled, finished, limited := 0, 0, 0
	for seed := int64(0); seed < 3000; seed++ {
		src := genProgram(seed)
		ok, err := runFuzzed(t, src)
		if !ok {
			t.Fatalf("generator produced a program that does not compile (seed %d): %v\n%s", seed, err, src)
		}
		compiled++
		if err == nil {
			finished++
		} else if errors.Is(err, ErrLimit) {
			limited++
		}
	}
	t.Logf("%d programs: %d finished, %d hit a limit, %d failed with a script error", compiled, finished, limited, compiled-finished-limited)
	if finished < 100 || limited < 20 {
		t.Fatalf("generator is not exercising enough paths: %d finished, %d limited", finished, limited)
	}
}

func TestTokenSoupNeverPanics(t *testing.T) {
	words := []string{"let", "fn", "if", "else", "while", "for", "in", "return", "break", "continue", "true", "false", "nil", "and", "or", "not",
		"a", "b", "f", "h", "len", "push", "1", "2.5", "1e9", `"s"`, `"`, "(", ")", "{", "}", "[", "]", ",", ";", "=", "==", "+", "-", "*", "/", "%",
		"<", ">", "..", ".", "!", "&&", "||", "@", "#", "\\", "\x00", "é", "\n", " "}
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		for n := r.Intn(40); n >= 0; n-- {
			sb.WriteString(words[r.Intn(len(words))])
			sb.WriteByte(' ')
		}
		runFuzzed(t, sb.String())
	}
}

func FuzzCompileRun(f *testing.F) {
	for seed := int64(0); seed < 20; seed++ {
		f.Add(genProgram(seed))
	}
	f.Add(`let s = "x"; while true { s = s + s; }`)
	f.Add(`fn f(n) { return f(n + 1); } return f(0);`)
	f.Add(`let a = [1]; for i in 0..60 { a = [a, a]; } return str(a);`)
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 4096 {
			return
		}
		runFuzzed(t, src)
	})
}
