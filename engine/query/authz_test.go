package query

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"redx/box"
	"redx/engine/link"
	"redx/engine/sec"
)

const testPolicy = `
{Role[1]}  = [ {Name[]} = "reader" ];
{Role[2]}  = [ {Name[]} = "writer" ];
{Role[3]}  = [ {Name[]} = "snoop" ];
{Role[4]}  = [ {Name[]} = "joiner" ];
{Role[5]}  = [ {Name[]} = "deleter" ];
{Role[6]}  = [ {Name[]} = "fullwrite" ];
{Rule[1]}  = [ {Effect[]} = "allow", {Who[]} = "role:reader", {Actions[]} = "read", {Resource[]} = "players.red-xai.db" ];
{Rule[2]}  = [ {Effect[]} = "deny",  {Who[]} = "*", {Actions[]} = "*", {Resource[]} = "players.red-xai.db:Player.Stats.Secret" ];
{Rule[3]}  = [ {Effect[]} = "deny",  {Who[]} = "role:reader", {Actions[]} = "read", {Resource[]} = "players.red-xai.db:Player.Bio" ];
{Rule[4]}  = [ {Effect[]} = "allow", {Who[]} = "role:writer", {Actions[]} = ;["read","write","delete"]:, {Resource[]} = "players.red-xai.db:Player" ];
{Rule[5]}  = [ {Effect[]} = "deny",  {Who[]} = "role:writer", {Actions[]} = "write", {Resource[]} = "players.red-xai.db:Player.Level" ];
{Rule[6]}  = [ {Effect[]} = "allow", {Who[]} = "role:snoop", {Actions[]} = "read", {Resource[]} = "players.red-xai.db:Player.Name" ];
{Rule[7]}  = [ {Effect[]} = "allow", {Who[]} = "role:joiner", {Actions[]} = "read", {Resource[]} = ;["players.red-xai.db","guilds.red-xai.db"]: ];
{Rule[8]}  = [ {Effect[]} = "allow", {Who[]} = "role:deleter", {Actions[]} = ;["read","delete"]:, {Resource[]} = ;["players.red-xai.db","guilds.red-xai.db"]: ];
{Rule[9]}  = [ {Effect[]} = "allow", {Who[]} = "role:fullwrite", {Actions[]} = ;["read","write","delete"]:, {Resource[]} = ;["players.red-xai.db","guilds.red-xai.db","inv.red-xai.db"]: ];
{Rule[10]} = [ {Effect[]} = "allow", {Who[]} = "user:*", {Actions[]} = "read", {Resource[]} = "players.red-xai.db", {Owner[]} = TRUE ];
`

func policy(t testing.TB) *sec.Policy {
	t.Helper()
	p, err := sec.Parse(testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func as(roles ...string) sec.Principal { return sec.Principal{ID: "u-" + roles[0], Roles: roles} }

func run1(t testing.TB, f *fixture, pr sec.Principal, q string, args ...interface{}) (*Result, error) {
	t.Helper()
	return f.db.Exec(context.Background(), pr, q, args...)
}

func TestAuthorizerRequired(t *testing.T) {
	if _, err := New(Config{Store: link.NewMemStore()}); !errors.Is(err, ErrNoAuth) {
		t.Fatal(err)
	}
	// a nil *sec.Policy denies everything
	f := newFixture(t, fixOpt{auth: (*sec.Policy)(nil)})
	if _, err := run1(t, f, anyone, "SELECT _id FROM players.Player"); !errors.Is(err, sec.ErrDenied) {
		t.Fatal(err)
	}
}

func TestDeniedQueryNeverTouchesStorage(t *testing.T) {
	f := newFixture(t, fixOpt{auth: policy(t)})
	for _, q := range []string{
		"SELECT * FROM players.Player",
		"SELECT * FROM players.Player WHERE _id = 1",
		"UPDATE players.Player SET Name = 'x' WHERE _id = 1",
		"DELETE FROM players.Player WHERE _id = 1",
		"INSERT INTO players.Player (_id) VALUES (50)",
	} {
		_, err := run1(t, f, as("nobody"), q)
		if !errors.Is(err, sec.ErrDenied) {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if f.cs.scans.Load()+f.cs.gets.Load()+f.cs.puts.Load()+f.cs.dels.Load() != 0 {
		t.Fatalf("storage was touched: scans=%d gets=%d puts=%d dels=%d", f.cs.scans.Load(), f.cs.gets.Load(), f.cs.puts.Load(), f.cs.dels.Load())
	}
	// a forbidden file and a missing file look the same to an unauthorized caller
	_, e1 := run1(t, f, as("nobody"), "SELECT _id FROM players.Player")
	_, e2 := run1(t, f, as("nobody"), "SELECT _id FROM nosuchfile.Player")
	if !errors.Is(e1, sec.ErrDenied) || !errors.Is(e2, sec.ErrDenied) {
		t.Fatalf("%v / %v", e1, e2)
	}
	// ... while an authorized caller does learn that the file is missing
	if _, err := run1(t, f, as("reader"), "SELECT _id FROM nosuchfile.Player"); !errors.Is(err, sec.ErrDenied) {
		t.Fatal("reader has no rule for nosuchfile, so this is a deny too:", err)
	}
}

func TestAuthorizeBeforeRead(t *testing.T) {
	var firstScans int64 = -1
	var calls atomic.Int64
	var f *fixture
	az := authFunc(func(pr sec.Principal, act sec.Action, res sec.Resource) sec.Decision {
		if calls.Add(1) == 1 {
			atomic.StoreInt64(&firstScans, f.cs.scans.Load()+f.cs.gets.Load())
		}
		return sec.Decision{Allowed: true}
	})
	f = newFixture(t, fixOpt{auth: az})
	f.q(t, "SELECT Name FROM players.Player WHERE Level > 1")
	if atomic.LoadInt64(&firstScans) != 0 {
		t.Fatal("the first authorization must come before the first storage read")
	}
	// every record's read is authorized per (box, owner) and fields once each: the cache bounds the calls
	if c := calls.Load(); c > 10 {
		t.Fatalf("%d Authorize calls for one query: decisions should be cached", c)
	}
}

type authFunc func(sec.Principal, sec.Action, sec.Resource) sec.Decision

func (f authFunc) Authorize(p sec.Principal, a sec.Action, r sec.Resource) sec.Decision {
	return f(p, a, r)
}

func TestReadMasking(t *testing.T) {
	f := newFixture(t, fixOpt{auth: policy(t)})
	rd := as("reader")
	r, err := run1(t, f, rd, "SELECT Name, Stats.Gold FROM players.Player WHERE _id = 1")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, col(r, "Name"), "ann")
	// explicitly asking for a denied field is an error
	for _, q := range []string{
		"SELECT Stats.Secret FROM players.Player",
		"SELECT Name FROM players.Player WHERE Stats.Secret = 's1'",
		"SELECT Name FROM players.Player ORDER BY Bio",
		"SELECT Bio FROM players.Player",
	} {
		if _, err := run1(t, f, rd, q); !errors.Is(err, sec.ErrDenied) {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// * and whole boxes are filtered silently, at every depth
	r, err = run1(t, f, rd, "SELECT * FROM players.Player WHERE _id = 1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Columns {
		if c == "Bio" {
			t.Fatal("Bio leaked through *")
		}
	}
	r, err = run1(t, f, rd, "SELECT Stats FROM players.Player WHERE _id = 1")
	if err != nil {
		t.Fatal(err)
	}
	st := r.Rows[0][0].Box
	if st == nil || st.Len() != 1 || st.Get("Gold", 0, false) == nil || st.Get("Secret", 0, false) != nil {
		t.Fatalf("Secret leaked through the Stats box: %v", r.Rows[0][0])
	}
	// a whole-file query cannot use a denied field to learn anything: it reads as NELL
	r, err = run1(t, f, rd, `SELECT _id FROM "players.red-xai.db" WHERE Stats.Secret = 's1'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 0 {
		t.Fatal("filter on a denied field matched")
	}
	// a user without the deny (writer) is also covered by rule 2 (everyone)
	if _, err := run1(t, f, as("writer"), "SELECT Stats.Secret FROM players.Player"); !errors.Is(err, sec.ErrDenied) {
		t.Fatal(err)
	}
	// writer may read Bio
	if _, err := run1(t, f, as("writer"), "SELECT Bio FROM players.Player"); err != nil {
		t.Fatal(err)
	}
}

func TestDenyOnPrefixCoversChildren(t *testing.T) {
	p, err := sec.Parse(`
{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "players.red-xai.db" ];
{Rule[2]} = [ {Effect[]} = "deny",  {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "players.red-xai.db:Player.Stats" ];
`)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, fixOpt{auth: p})
	if _, err := run1(t, f, anyone, "SELECT Stats.Gold FROM players.Player"); !errors.Is(err, sec.ErrDenied) {
		t.Fatalf("a deny on Stats must cover Stats.Gold: %v", err)
	}
	r, err := run1(t, f, anyone, `SELECT Name, Stats.Gold AS g FROM "players.red-xai.db" WHERE _id = 1 AND _box = 'Player'`)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, col(r, "g"), "NELL")
}

func TestFieldOnlyGrantDoesNotOpenTheBox(t *testing.T) {
	f := newFixture(t, fixOpt{auth: policy(t)})
	// the snoop role may read Player.Name but has no read on the box itself
	if _, err := run1(t, f, as("snoop"), "SELECT Name FROM players.Player"); !errors.Is(err, sec.ErrDenied) {
		t.Fatal(err)
	}
}

func TestWriteAuthorization(t *testing.T) {
	f := newFixture(t, fixOpt{auth: policy(t), links: true})
	wr := as("writer")
	// reader cannot write at all
	for _, q := range []string{
		"UPDATE players.Player SET Name = 'x' WHERE _id = 1",
		"INSERT INTO players.Player (_id, Name) VALUES (60, 'x')",
		"DELETE FROM players.Player WHERE _id = 1",
	} {
		if _, err := run1(t, f, as("reader"), q); !errors.Is(err, sec.ErrDenied) {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// writer: Name allowed, Level explicitly denied
	r, err := run1(t, f, wr, "UPDATE players.Player SET Name = 'zed' WHERE _id = 1")
	if err != nil || r.Affected != 1 {
		t.Fatal(err, r)
	}
	if _, err := run1(t, f, wr, "UPDATE players.Player SET Name = 'q', Level = 1 WHERE _id = 2"); !errors.Is(err, sec.ErrDenied) {
		t.Fatalf("Level write must be denied: %v", err)
	}
	ent, _, _ := f.mem.Get(addrOf(playersFile, "Player", 2))
	if ent.Value.Box.Get("Name", 0, false).Value.Str != "bob" {
		t.Fatal("a denied UPDATE must change nothing")
	}
	if _, err := run1(t, f, wr, "INSERT INTO players.Player (_id, Name, Level) VALUES (61, 'n', 1)"); !errors.Is(err, sec.ErrDenied) {
		t.Fatal(err)
	}
	if _, ok, _ := f.mem.Get(addrOf(playersFile, "Player", 61)); ok {
		t.Fatal("denied INSERT stored a record")
	}
	if _, err := run1(t, f, wr, "INSERT INTO players.Player (_id, Name) VALUES (62, 'n')"); err != nil {
		t.Fatal(err)
	}
	// the denied field cannot be probed through an UPDATE's WHERE either
	if _, err := run1(t, f, wr, "UPDATE players.Player SET Name = 'x' WHERE Stats.Secret = 's1'"); !errors.Is(err, sec.ErrDenied) {
		t.Fatal(err)
	}
	// writer may delete in Player
	if r, err := run1(t, f, wr, "DELETE FROM players.Player WHERE _id = 62"); err != nil || r.Affected != 1 {
		t.Fatal(err, r)
	}
}

func TestCascadeDeleteIsAuthorizedPerTarget(t *testing.T) {
	f := newFixture(t, fixOpt{auth: policy(t), links: true})
	must(t, link.PutDoc(f.mem, "inv.red-xai.db", `{Inv[100]} = [ {Items[]} = 3 ];`))
	e, _, _ := f.mem.Get(addrOf(playersFile, "Player", 1))
	l, _ := link.ParseLink(`"players.red-xai.db":Player[ID=1] @!> "inv.red-xai.db":Inv[ID=100]`)
	e.Value.Box.Put(box.NewEntry("Inv", nil, box.LinkValue(l)))
	must(t, f.mem.Put(addrOf(playersFile, "Player", 1), e))
	// deleter can delete players but has no rule at all for inv
	_, err := run1(t, f, as("deleter"), "DELETE FROM players.Player WHERE _id = 1")
	if !errors.Is(err, sec.ErrDenied) {
		t.Fatalf("cascade into a forbidden file must be refused: %v", err)
	}
	if _, ok, _ := f.mem.Get(addrOf(playersFile, "Player", 1)); !ok {
		t.Fatal("the player must not be deleted when its cascade is refused")
	}
	if _, ok, _ := f.mem.Get(addrOf("inv.red-xai.db", "Inv", 100)); !ok {
		t.Fatal("inv must survive")
	}
	r, err := run1(t, f, as("fullwrite"), "DELETE FROM players.Player WHERE _id = 1")
	if err != nil || r.Affected != 1 || len(r.Cascaded) != 1 {
		t.Fatal(err, r)
	}
}

func TestJoinTargetAuthorization(t *testing.T) {
	f := newFixture(t, fixOpt{auth: policy(t), links: true})
	q := "SELECT p.Name, g.Title FROM players.Player p JOIN p.Guild g WHERE p._id = 1"
	r, err := run1(t, f, as("reader"), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 0 { // reader may not read guilds: the target is invisible, like a dangling link
		t.Fatal("joined a record the principal may not read")
	}
	r, err = run1(t, f, as("reader"), "SELECT p.Name, g.Title FROM players.Player p LEFT JOIN p.Guild g WHERE p._id = 1")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, col(r, "g.Title"), "NELL")
	r, err = run1(t, f, as("joiner"), q)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, col(r, "g.Title"), "Red Order")
}

func TestOwnerRules(t *testing.T) {
	owner := func(a link.Addr, e *box.Entry) string {
		if e != nil && e.Value.Kind == box.KindBox {
			if n := e.Value.Box.Get("Name", 0, false); n != nil {
				return n.Value.Str
			}
		}
		return ""
	}
	f := newFixture(t, fixOpt{auth: policy(t), owner: owner})
	bob := sec.Principal{ID: "bob"}
	r, err := run1(t, f, bob, "SELECT Name FROM players.Player")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, col(r, "Name"), "bob") // only the record bob owns
	r, err = run1(t, f, bob, `SELECT _box, Name FROM "players.red-xai.db"`)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, col(r, "Name"), "bob")
}

func TestExplainDoesNotLeak(t *testing.T) {
	f := newFixture(t, fixOpt{auth: policy(t)})
	if _, err := f.db.Explain(context.Background(), as("nobody"), "SELECT * FROM players.Player"); !errors.Is(err, sec.ErrDenied) {
		t.Fatal(err)
	}
	if _, err := f.db.Explain(context.Background(), as("reader"), "SELECT Bio FROM players.Player"); !errors.Is(err, sec.ErrDenied) {
		t.Fatal(err)
	}
	if _, err := f.db.Explain(context.Background(), as("reader"), "SELECT Name FROM players.Player"); err != nil {
		t.Fatal(err)
	}
}
