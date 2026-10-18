package code

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"redx/engine/sec"
)

const testPolicy = `
{Scope[1]} = [ {Name[]} = "players.read", {Actions[]} = "read", {Resource[]} = "players.red-xai.db:Player" ];
{Scope[2]} = [ {Name[]} = "players.write", {Actions[]} = ;["read","write"]:, {Resource[]} = "players.red-xai.db:Player.Health" ];
{Scope[3]} = [ {Name[]} = "log", {Actions[]} = "exec", {Resource[]} = "host:log" ];
{Role[1]} = [ {Name[]} = "game", {Scopes[]} = ;["players.read","players.write","log"]: ];
{Role[2]} = [ {Name[]} = "viewer", {Scopes[]} = ;["players.read"]: ];
{Rule[1]} = [ {Effect[]} = "deny", {Who[]} = "*", {Actions[]} = "*", {Resource[]} = "players.red-xai.db:Player.Email" ];
`

// fakeDB is an in-memory stand-in for the real engines.
type fakeDB struct {
	mu   sync.Mutex
	vals map[string]float64
	log  []string
}

func newFakeDB() *fakeDB {
	return &fakeDB{vals: map[string]float64{"Player/1/Health": 50, "Player/1/Email": 7}}
}

func (d *fakeDB) host(t testing.TB) *Host {
	fieldRes := func(args []interface{}) (sec.Resource, error) {
		if len(args) < 2 {
			return sec.Resource{}, errors.New("need box id and field")
		}
		f, ok := args[1].(string)
		if !ok {
			return sec.Resource{}, errors.New("field must be a string")
		}
		return sec.Resource{File: "players.red-xai.db", Box: "Player", Field: f}, nil
	}
	h, err := NewHost(
		HostFunc{Name: "db_get", Action: sec.ActionRead, Resource: fieldRes,
			Fn: func(ctx context.Context, a []interface{}) (interface{}, error) {
				d.mu.Lock()
				defer d.mu.Unlock()
				v, ok := d.vals[fmt.Sprintf("Player/%v/%v", a[0], a[1])]
				if !ok {
					return nil, nil
				}
				return v, nil
			}},
		HostFunc{Name: "db_set", Action: sec.ActionWrite, Resource: fieldRes,
			Fn: func(ctx context.Context, a []interface{}) (interface{}, error) {
				d.mu.Lock()
				defer d.mu.Unlock()
				d.vals[fmt.Sprintf("Player/%v/%v", a[0], a[1])] = a[2].(float64)
				return true, nil
			}},
		HostFunc{Name: "log", Resource: FixedResource("host", "log", ""),
			Fn: func(ctx context.Context, a []interface{}) (interface{}, error) {
				d.mu.Lock()
				defer d.mu.Unlock()
				d.log = append(d.log, fmt.Sprint(a...))
				return nil, nil
			}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func policy(t testing.TB) *sec.Policy {
	p, err := sec.Parse(testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const healSrc = `
let hp = db_get(id, "Health");
let nhp = min(100, hp + amount);
db_set(id, "Health", nhp);
log("healed " + str(id) + " to " + str(nhp));
return nhp;`

func healProgram(t testing.TB) *Program {
	p, err := Compile("heal", healSrc, []string{"id", "amount"}, []string{"db_get", "db_set", "log"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHostCallsAreAuthorized(t *testing.T) {
	pol := policy(t)
	prog := healProgram(t)
	args := []interface{}{1, 30}

	// allowed principal
	db := newFakeDB()
	res, err := prog.Run(context.Background(), args, RunOptions{Principal: sec.Principal{ID: "srv", Roles: []string{"game"}}, Auth: pol, Host: db.host(t)})
	if err != nil || res.Value != 80.0 || db.vals["Player/1/Health"] != 80 || len(db.log) != 1 {
		t.Fatalf("allowed run: %v %+v %v", err, res, db.vals)
	}
	if res.HostCalls != 3 || len(res.Calls) != 3 {
		t.Fatalf("calls: %+v", res)
	}
	for _, c := range res.Calls {
		if !c.Allowed || c.Reason == "" {
			t.Fatalf("call record: %+v", c)
		}
	}

	denied := map[string]RunOptions{
		"read-only role": {Principal: sec.Principal{ID: "v", Roles: []string{"viewer"}}, Auth: pol},
		"anonymous":      {Auth: pol},
		"unknown role":   {Principal: sec.Principal{ID: "x", Roles: []string{"admin"}}, Auth: pol},
		"no authorizer":  {Principal: sec.Principal{ID: "srv", Roles: []string{"game"}}},
		"nil policy":     {Principal: sec.Principal{ID: "srv", Roles: []string{"game"}}, Auth: (*sec.Policy)(nil)},
	}
	for name, o := range denied {
		db := newFakeDB()
		o.Host = db.host(t)
		res, err := prog.Run(context.Background(), args, o)
		if !errors.Is(err, sec.ErrDenied) {
			t.Errorf("%s: want ErrDenied, got %v", name, err)
		}
		if db.vals["Player/1/Health"] != 50 || len(db.log) != 0 {
			t.Errorf("%s: the denied script changed state: %v %v", name, db.vals, db.log)
		}
		if len(res.Calls) == 0 || res.Calls[len(res.Calls)-1].Allowed {
			t.Errorf("%s: denial not recorded: %+v", name, res.Calls)
		}
	}

	// a viewer may read but not write: the first call works, the second is refused
	db = newFakeDB()
	_, err = prog.Run(context.Background(), args, RunOptions{Principal: sec.Principal{ID: "v", Roles: []string{"viewer"}}, Auth: pol, Host: db.host(t)})
	if !errors.Is(err, sec.ErrDenied) || !strings.Contains(err.Error(), "db_set") {
		t.Fatalf("viewer: %v", err)
	}
}

func TestResourceComesFromTheHostNotTheScript(t *testing.T) {
	pol := policy(t)
	db := newFakeDB()
	game := RunOptions{Principal: sec.Principal{ID: "srv", Roles: []string{"game"}}, Auth: pol, Host: db.host(t)}
	// the game role may read Player but a deny rule hides Email from everybody
	p, _ := Compile("t", `return db_get(1, field);`, []string{"field"}, []string{"db_get"}, Limits{})
	if res, err := p.Run(context.Background(), []interface{}{"Health"}, game); err != nil || res.Value != 50.0 {
		t.Fatalf("Health: %v %v", err, res.Value)
	}
	if _, err := p.Run(context.Background(), []interface{}{"Email"}, game); !errors.Is(err, sec.ErrDenied) {
		t.Fatalf("Email must be denied: %v", err)
	}
	// a script cannot smuggle a different resource through odd arguments
	for _, f := range []string{"Email", "Health\x00", "Health ", "health", ""} {
		_, err := p.Run(context.Background(), []interface{}{f}, game)
		if f == "Health" {
			continue
		}
		if err == nil && f != "health" && f != "Health " && f != "" {
			t.Errorf("field %q was allowed", f)
		}
	}
	// the resource function can refuse a call outright (here: a non-string field)
	q, _ := Compile("t", `return db_get(1, 5);`, nil, []string{"db_get"}, Limits{})
	if _, err := q.Run(context.Background(), nil, game); !errors.Is(err, sec.ErrDenied) {
		t.Fatalf("resource refusal: %v", err)
	}
	// a host call that is not allowed never reaches the host function
	called := false
	h, _ := NewHost(HostFunc{Name: "boom", Action: sec.ActionDelete, Resource: FixedResource("players.red-xai.db", "Player", ""),
		Fn: func(ctx context.Context, a []interface{}) (interface{}, error) { called = true; return nil, nil }})
	r, _ := Compile("t", `boom();`, nil, []string{"boom"}, Limits{})
	if _, err := r.Run(context.Background(), nil, RunOptions{Principal: game.Principal, Auth: pol, Host: h}); !errors.Is(err, sec.ErrDenied) || called {
		t.Fatalf("delete must be denied before the function runs: %v called=%v", err, called)
	}
}

func TestEscapeAttempts(t *testing.T) {
	pol := policy(t)
	db := newFakeDB()
	game := RunOptions{Principal: sec.Principal{ID: "srv", Roles: []string{"game"}}, Auth: pol, Host: db.host(t)}

	// 1. functions that do not exist in the language, or are not declared, do not compile
	for _, src := range []string{
		`return read_file("/etc/passwd");`, `return open("/etc/passwd");`, `return http_get("http://x");`,
		`return os("ls");`, `return system("ls");`, `return exec("ls");`, `return eval("1+1");`, `return import("os");`,
		`return require("fs");`, `return getattr(a, "b");`, `return call("db_get");`, `return __import__("os");`,
		`return db_get(1, "Health");`, // host function exists, but this script did not declare it
		`return log("x");`, `return print("x");`, `return time();`, `return now();`, `return rand();`, `return sleep(1);`,
		`return globals();`, `return env("HOME");`, `return len.call();`,
	} {
		_, err := Compile("t", src, nil, nil, Limits{})
		if !errors.Is(err, ErrBadScript) {
			t.Errorf("%s: compiled (%v)", src, err)
		}
	}
	// 2. dynamic ways of naming a function are not in the grammar
	for _, src := range []string{
		`let f = "db_get"; return f(1, "Health");`, // calling a variable
		`let f = [db_get]; return f[0](1);`,        // function values
		`return ("db_get")(1);`,                    // call on an expression
		`let a = db_get; return a;`,                // a bare name is just an undefined variable
		`return db_get (1, "Health") (2);`,
	} {
		_, err := Compile("t", src, nil, []string{"db_get"}, Limits{})
		if err == nil {
			// a bare `db_get` is just an (undefined) variable name: it must fail at run time
			p, _ := Compile("t", src, nil, []string{"db_get"}, Limits{})
			if _, rerr := p.Run(context.Background(), nil, game); rerr == nil {
				t.Errorf("%s: ran", src)
			}
		}
	}
	// 3. a variable named like a host function or built-in does not become callable
	if p, err := Compile("t", `let log = 5; return log("x");`, nil, nil, Limits{}); err == nil {
		t.Errorf("shadowed call compiled: %v", p)
	}
	if res, err := Compile("t", `let len = 5; return len("abc");`, nil, nil, Limits{}); err != nil {
		t.Fatal(err)
	} else if out, err := res.Run(context.Background(), nil, RunOptions{}); err != nil || out.Value != 3.0 {
		t.Fatalf("calls always mean the built-in, never the variable: %v %v", err, out.Value)
	}
	// 4. user functions cannot take the name of a built-in or declared host function
	for _, src := range []string{`fn len(x) { return 1; }`, `fn log(x) { return 1; }`, `fn push() { }`} {
		if _, err := Compile("t", src, nil, []string{"log"}, Limits{}); err == nil {
			t.Errorf("%s compiled", src)
		}
	}
	// 5. Uses cannot name built-ins or keywords
	for _, u := range []string{"len", "if", "", "a b", "x-y", "9a", strings.Repeat("a", 65)} {
		if _, err := Compile("t", "return 1;", nil, []string{u}, Limits{}); err == nil {
			t.Errorf("Uses %q accepted", u)
		}
	}
	// 6. declared, but the host did not provide it
	p, _ := Compile("t", `return not_provided(1);`, nil, []string{"not_provided"}, Limits{})
	res, err := p.Run(context.Background(), nil, game)
	if !errors.Is(err, ErrNotGranted) || len(res.Calls) != 1 || res.Calls[0].Allowed {
		t.Fatalf("not provided: %v %+v", err, res.Calls)
	}
	if _, err := p.Run(context.Background(), nil, RunOptions{Auth: pol}); !errors.Is(err, ErrNotGranted) {
		t.Fatalf("nil host: %v", err)
	}
	// 7. text that looks like code in data is just data
	q, _ := Compile("t", `return p0;`, []string{"p0"}, nil, Limits{})
	out, err := q.Run(context.Background(), []interface{}{`"; db_set(1,"Health",0); //`}, game)
	if err != nil || db.vals["Player/1/Health"] != 50 || out.HostCalls != 0 {
		t.Fatalf("injection: %v", err)
	}
	// 8. nothing outside the sandbox changed during all of this
	if db.vals["Player/1/Health"] != 50 || len(db.log) != 0 {
		t.Fatalf("state changed: %v %v", db.vals, db.log)
	}
	// 9. the host-call limit
	loop, _ := Compile("t", `while true { log("x"); }`, nil, []string{"log"}, Limits{MaxHostCalls: 5})
	db2 := newFakeDB()
	_, err = loop.Run(context.Background(), nil, RunOptions{Principal: game.Principal, Auth: pol, Host: db2.host(t), Limits: Limits{MaxSteps: 1 << 40}})
	if !errors.Is(err, ErrHostCallLimit) || len(db2.log) != 5 {
		t.Fatalf("host call limit: %v (%d calls)", err, len(db2.log))
	}
	// 10. script A cannot see script B's variables: each run is a new world
	a, _ := Compile("a", `let secret = 42; return secret;`, nil, nil, Limits{})
	b, _ := Compile("b", `return secret;`, nil, nil, Limits{})
	a.Run(context.Background(), nil, RunOptions{})
	if _, err := b.Run(context.Background(), nil, RunOptions{}); err == nil {
		t.Fatal("state leaked between runs")
	}
}

func TestHostFunctionBoundary(t *testing.T) {
	pol, _ := sec.Parse(`{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "*", {Resource[]} = "*" ];`)
	allow := RunOptions{Auth: pol}
	mk := func(fn func(ctx context.Context, a []interface{}) (interface{}, error)) RunOptions {
		h, err := NewHost(HostFunc{Name: "h", Fn: fn})
		if err != nil {
			t.Fatal(err)
		}
		o := allow
		o.Host = h
		return o
	}
	call, _ := Compile("t", `return h(1, "two", [3, [4]], nil, true);`, nil, []string{"h"}, Limits{})

	// arguments arrive as plain Go values
	var got []interface{}
	_, err := call.Run(context.Background(), nil, mk(func(ctx context.Context, a []interface{}) (interface{}, error) { got = a; return nil, nil }))
	if err != nil || fmt.Sprintf("%#v", got) != `[]interface {}{1, "two", []interface {}{3, []interface {}{4}}, interface {}(nil), true}` {
		t.Fatalf("args: %v %#v", err, got)
	}
	// they are copies: the host cannot reach the script's arrays
	mut, _ := Compile("t", `let a = [1, 2]; h(a); return a;`, nil, []string{"h"}, Limits{})
	res, err := mut.Run(context.Background(), nil, mk(func(ctx context.Context, a []interface{}) (interface{}, error) {
		a[0].([]interface{})[0] = 99.0
		return nil, nil
	}))
	if err != nil || res.Value.([]interface{})[0] != 1.0 {
		t.Fatalf("host mutated the script's array: %v %v", err, res.Value)
	}
	// results come back converted: ints, slices
	res, err = call.Run(context.Background(), nil, mk(func(ctx context.Context, a []interface{}) (interface{}, error) {
		return []interface{}{int64(3), "x", []string{"y"}, nil, true, 2.5, uint8(7)}, nil
	}))
	if err != nil || fmt.Sprintf("%v", res.Value) != "[3 x [y] <nil> true 2.5 7]" {
		t.Fatalf("results: %v %v", err, res.Value)
	}
	// bad results
	for name, v := range map[string]interface{}{
		"map":       map[string]int{"a": 1},
		"struct":    struct{ A int }{1},
		"func":      func() {},
		"NaN":       func() float64 { z := 0.0; return z / z }(),
		"bad slice": []interface{}{map[string]int{}},
		"long str":  strings.Repeat("x", DefaultMaxStringLen+1),
		"long arr":  make([]interface{}, DefaultMaxArrayLen+1),
		"deep":      deepSlice(100),
		"pointer":   new(int),
	} {
		_, err := call.Run(context.Background(), nil, mk(func(ctx context.Context, a []interface{}) (interface{}, error) { return v, nil }))
		if err == nil {
			t.Errorf("%s: result accepted", name)
		}
	}
	// errors and panics are contained
	sentinel := errors.New("disk on fire")
	_, err = call.Run(context.Background(), nil, mk(func(ctx context.Context, a []interface{}) (interface{}, error) { return nil, sentinel }))
	if !errors.Is(err, ErrHostFunc) || !errors.Is(err, sentinel) {
		t.Fatalf("host error: %v", err)
	}
	_, err = call.Run(context.Background(), nil, mk(func(ctx context.Context, a []interface{}) (interface{}, error) { panic("kaboom") }))
	if !errors.Is(err, ErrHostFunc) || !strings.Contains(err.Error(), "kaboom") || errors.Is(err, ErrInternal) {
		t.Fatalf("host panic: %v", err)
	}
	var nilmap map[string]int
	_, err = call.Run(context.Background(), nil, mk(func(ctx context.Context, a []interface{}) (interface{}, error) { nilmap["x"] = 1; return nil, nil }))
	if !errors.Is(err, ErrHostFunc) {
		t.Fatalf("runtime panic in host: %v", err)
	}
	// the host function's context carries the run's deadline
	var dl time.Time
	var ok bool
	_, err = call.Run(context.Background(), nil, func() RunOptions {
		o := mk(func(ctx context.Context, a []interface{}) (interface{}, error) {
			dl, ok = ctx.Deadline()
			return nil, nil
		})
		o.Limits = Limits{MaxDuration: 5 * time.Second}
		return o
	}())
	if err != nil || !ok || time.Until(dl) > 5*time.Second || time.Until(dl) < 3*time.Second {
		t.Fatalf("host ctx deadline: %v %v %v", err, ok, dl)
	}
	// a host function that honours ctx ends the run at the time limit
	start := time.Now()
	slow := mk(func(ctx context.Context, a []interface{}) (interface{}, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return nil, nil
		}
	})
	slow.Limits = Limits{MaxDuration: 40 * time.Millisecond}
	if _, err := call.Run(context.Background(), nil, slow); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("slow host function: %v after %v", err, time.Since(start))
	}
	// a host function that ignores ctx and overruns: the run fails when it returns
	over := mk(func(ctx context.Context, a []interface{}) (interface{}, error) {
		time.Sleep(80 * time.Millisecond)
		return 1.0, nil
	})
	over.Limits = Limits{MaxDuration: 20 * time.Millisecond}
	if _, err := call.Run(context.Background(), nil, over); !errors.Is(err, ErrTimeout) {
		t.Fatalf("overrun: %v", err)
	}
	// NewHost validation
	good := func(context.Context, []interface{}) (interface{}, error) { return nil, nil }
	for name, hf := range map[string]HostFunc{
		"builtin name": {Name: "len", Fn: good}, "keyword": {Name: "if", Fn: good}, "empty": {Name: "", Fn: good},
		"spaces": {Name: "a b", Fn: good}, "nil fn": {Name: "x"}, "bad action": {Name: "x", Fn: good, Action: "root"},
		"wildcard action": {Name: "x", Fn: good, Action: sec.ActionAny},
	} {
		if _, err := NewHost(hf); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := NewHost(HostFunc{Name: "x", Fn: good}, HostFunc{Name: "x", Fn: good}); err == nil {
		t.Error("duplicate accepted")
	}
	h, _ := NewHost(HostFunc{Name: "b", Fn: good}, HostFunc{Name: "a", Fn: good})
	if strings.Join(h.Names(), ",") != "a,b" || (*Host)(nil).Names() != nil {
		t.Error("Names")
	}
}

func deepSlice(n int) interface{} {
	var v interface{} = 1.0
	for i := 0; i < n; i++ {
		v = []interface{}{v}
	}
	return v
}

func TestInternalPanicIsContained(t *testing.T) {
	// A corrupted program (impossible through Compile) must not crash the caller.
	p := compile(t, `return 1;`)
	p.body.stmts = append([]stmt{(*letStmt)(nil)}, p.body.stmts...)
	_, err := p.Run(context.Background(), nil, RunOptions{})
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("want ErrInternal, got %v", err)
	}
}

const fileSrc = `
{Script[1]} = [
    {Name[]}   = "heal",
    {Params[]} = ;["id", "amount"]:,
    {Uses[]}   = ;["db_get", "db_set", "log"]:,
    {Limits[]} = [ {MaxSteps[]} = 500, {TimeoutMs[]} = 200, {MaxHostCalls[]} = 10 ],
    {Source[]} = "let hp = db_get(id, \"Health\");\nlet nhp = min(100, hp + amount);\ndb_set(id, \"Health\", nhp);\nlog(\"healed\");\nreturn nhp;"
];
{Script[2]} = [ {Name[]} = "double", {Params[]} = "x", {Source[]} = "return x * 2;" ];
{Script[3]} = [ {Name[]} = "spin", {Limits[]} = [ {MaxSteps[]} = 100 ], {Source[]} = "while true { }" ];
`

func TestFileFormat(t *testing.T) {
	f, err := ParseFile(fileSrc)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Scripts()) != 3 {
		t.Fatal("scripts")
	}
	if _, ok := f.Script("nope"); ok {
		t.Fatal("unknown script found")
	}
	heal, ok := f.Script("heal")
	if !ok || heal.Name() != "heal" || strings.Join(heal.Params(), ",") != "id,amount" || strings.Join(heal.Uses(), ",") != "db_get,db_set,log" {
		t.Fatalf("heal: %+v", heal)
	}
	if l := heal.Limits(); l.MaxSteps != 500 || l.MaxDuration != 200*time.Millisecond || l.MaxHostCalls != 10 {
		t.Fatalf("limits: %+v", l)
	}
	db := newFakeDB()
	res, err := heal.Run(context.Background(), []interface{}{1, 25}, RunOptions{Principal: sec.Principal{ID: "s", Roles: []string{"game"}}, Auth: policy(t), Host: db.host(t)})
	if err != nil || res.Value != 75.0 {
		t.Fatalf("run from file: %v %v", err, res.Value)
	}
	d, _ := f.Script("double")
	if r, err := d.Run(context.Background(), []interface{}{21}, RunOptions{}); err != nil || r.Value != 42.0 {
		t.Fatalf("double: %v", err)
	}
	spin, _ := f.Script("spin")
	if r, err := spin.Run(context.Background(), nil, RunOptions{}); !errors.Is(err, ErrStepLimit) || r.Steps > 102 {
		t.Fatalf("script limit from file: %v %d", err, r.Steps)
	}
	// ToBox / FromGo round trip
	bv, err := ToBox([]interface{}{1.0, "a", nil, true, []interface{}{}})
	if err != nil || bv.String() == "" {
		t.Fatal(err)
	}
	if _, err := ToBox(map[string]int{}); err == nil {
		t.Fatal("ToBox of a map")
	}
	if g, err := FromGo([]string{"a", "b"}); err != nil || fmt.Sprint(g) != "[a b]" {
		t.Fatalf("FromGo: %v %v", g, err)
	}
}

func TestFileFormatErrors(t *testing.T) {
	ok := `{Script[1]} = [ {Name[]} = "a", {Source[]} = "return 1;" ];`
	if _, err := ParseFile(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"not box syntax":    `{Script[1] = `,
		"other entry":       `{Thing[1]} = [ {Name[]} = "a", {Source[]} = "return 1;" ];`,
		"scalar":            `{Script[1]} = "x";`,
		"no name":           `{Script[1]} = [ {Source[]} = "return 1;" ];`,
		"no source":         `{Script[1]} = [ {Name[]} = "a" ];`,
		"source not string": `{Script[1]} = [ {Name[]} = "a", {Source[]} = 5 ];`,
		"bad name":          `{Script[1]} = [ {Name[]} = "a b", {Source[]} = "return 1;" ];`,
		"keyword name":      `{Script[1]} = [ {Name[]} = "while", {Source[]} = "return 1;" ];`,
		"unknown entry":     `{Script[1]} = [ {Name[]} = "a", {Source[]} = "return 1;", {Sorce[]} = "x" ];`,
		"duplicate name":    `{Script[1]} = [ {Name[]} = "a", {Source[]} = "return 1;" ]; {Script[2]} = [ {Name[]} = "a", {Source[]} = "return 2;" ];`,
		"bad params":        `{Script[1]} = [ {Name[]} = "a", {Params[]} = ;[1]:, {Source[]} = "return 1;" ];`,
		"dup params":        `{Script[1]} = [ {Name[]} = "a", {Params[]} = ;["x","x"]:, {Source[]} = "return 1;" ];`,
		"uses builtin":      `{Script[1]} = [ {Name[]} = "a", {Uses[]} = "len", {Source[]} = "return 1;" ];`,
		"limits not box":    `{Script[1]} = [ {Name[]} = "a", {Limits[]} = 5, {Source[]} = "return 1;" ];`,
		"unknown limit":     `{Script[1]} = [ {Name[]} = "a", {Limits[]} = [ {MaxStep[]} = 5 ], {Source[]} = "return 1;" ];`,
		"zero limit":        `{Script[1]} = [ {Name[]} = "a", {Limits[]} = [ {MaxSteps[]} = 0 ], {Source[]} = "return 1;" ];`,
		"fraction limit":    `{Script[1]} = [ {Name[]} = "a", {Limits[]} = [ {MaxSteps[]} = 1.5 ], {Source[]} = "return 1;" ];`,
		"negative limit":    `{Script[1]} = [ {Name[]} = "a", {Limits[]} = [ {MaxSteps[]} = -5 ], {Source[]} = "return 1;" ];`,
		"syntax in source":  `{Script[1]} = [ {Name[]} = "a", {Source[]} = "return 1 +;" ];`,
		"undeclared call":   `{Script[1]} = [ {Name[]} = "a", {Source[]} = "return read_file(1);" ];`,
	}
	for name, src := range bad {
		_, err := ParseFile(src)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !errors.Is(err, ErrBadFile) && !errors.Is(err, ErrBadScript) {
			t.Errorf("%s: error does not wrap ErrBadFile or ErrBadScript: %v", name, err)
		}
	}
	if _, err := ParseFile(strings.Repeat(" ", MaxFileBytes+1)); !errors.Is(err, ErrBadFile) {
		t.Fatalf("oversize: %v", err)
	}
	// a syntax error names the script
	_, err := ParseFile(`{Script[1]} = [ {Name[]} = "broken", {Source[]} = "let x = ;" ];`)
	var se *SyntaxError
	if !errors.As(err, &se) || se.Script != "broken" {
		t.Fatalf("%v", err)
	}
	// compile-time ceiling applies to files
	if _, err := ParseFileLimits(ok, Limits{MaxSourceBytes: 3}); err == nil {
		t.Fatal("source ceiling ignored")
	}
}

// The package must stay inside the sandbox rules it enforces: no OS, network,
// process, reflection, unsafe or goroutine use in non-test code.
func TestPackageHasNoAmbientAuthority(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"os", "os/exec", "os/signal", "net", "net/http", "syscall", "unsafe", "reflect", "plugin",
			"io/ioutil", "runtime/debug", "log", "log/slog", "math/rand", "crypto/rand", "path/filepath"} {
			if bytes.Contains(src, []byte("\t\""+banned+"\"\n")) {
				t.Errorf("%s imports %s", f, banned)
			}
		}
		for _, banned := range []string{"\n\tgo ", " go func", "os.Getenv", "fmt.Print", "time.Sleep"} {
			if bytes.Contains(src, []byte(banned)) {
				t.Errorf("%s contains %q", f, banned)
			}
		}
	}
}
