package link

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"redx/box"
)

const (
	fPlayers = "players.red-xai.db"
	fGuilds  = "guilds.red-xai.db"
	fInv     = "inventories.red-xai.db"
)

func mustLink(t *testing.T, s string) *box.Link {
	t.Helper()
	l, err := ParseLink(s)
	if err != nil {
		t.Fatalf("ParseLink(%q): %v", s, err)
	}
	return l
}

func mustRef(t *testing.T, s string) box.Ref {
	t.Helper()
	l := mustLink(t, s)
	return l.From
}

func newStore(t *testing.T, docs map[string]string) *MemStore {
	t.Helper()
	ms := NewMemStore()
	for f, src := range docs {
		if err := PutDoc(ms, f, src); err != nil {
			t.Fatalf("PutDoc %s: %v", f, err)
		}
	}
	return ms
}

func player(id int64) Addr { return Addr{File: fPlayers, Box: "Player", ID: id, HasID: true} }
func guild(id int64) Addr  { return Addr{File: fGuilds, Box: "Guild", ID: id, HasID: true} }
func inv(id int64) Addr    { return Addr{File: fInv, Box: "Inv", ID: id, HasID: true} }

// A small world: players -> guild (read link), player -> inventory (cascade).
func world(t *testing.T) (*MemStore, *Engine) {
	ms := newStore(t, map[string]string{
		fPlayers: `
{Player[ID=1]= [
    {Name[]} = "Ann",
    {Health[]} = 100,
    {Guild[]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10],
    {Bag[]} = "players.red-xai.db":Player[ID=1] @!> "inventories.red-xai.db":Inv[ID=100]
]};
{Player[ID=2]= [
    {Name[]} = "Bob",
    {Guild[]} = "players.red-xai.db":Player[ID=2] @> "guilds.red-xai.db":Guild[ID=10]
]};`,
		fGuilds: `
{Guild[ID=10]= [
    {Title[]} = "Dragons",
    {Stats[]} = [ {Rank[]} = 3 ]
]};`,
		fInv: `
{Inv[ID=100]= [
    {Sword[]} = "steel",
    {Slot[ID=1]} = [ {Item[]} = "potion" ]
]};`,
	})
	return ms, New(ms, Options{})
}

func TestParseLinkOperators(t *testing.T) {
	l := mustLink(t, `"a.red-xai.db":Box[ID=1].Key @> "b.red-xai.db":Other[ID=2]`)
	if l.Op != OpRead || l.To == nil || l.To.File != "b.red-xai.db" || l.From.Segs[0].ID != 1 || len(l.From.Segs) != 2 {
		t.Fatalf("bad read link: %+v", l)
	}
	c := mustLink(t, `"a":B[ID=1] @!> "b":C[ID=2]`)
	if c.Op != OpCascade {
		t.Fatalf("want cascade, got %q", c.Op)
	}
	bare := mustLink(t, `"a":B[ID=1]`)
	if bare.To != nil || bare.Op != "" {
		t.Fatalf("bare ref: %+v", bare)
	}
	if _, err := ParseLink(`42`); err == nil {
		t.Fatal("expected error for non-link")
	}
	if _, err := ParseLink(`"a":B[ID=1] @> `); err == nil {
		t.Fatal("expected error for missing target")
	}
	// round trip through the box serializer
	if got := c.String(); got != `"a":B[ID=1] @!> "b":C[ID=2]` {
		t.Fatalf("String(): %s", got)
	}
}

func TestAddrOf(t *testing.T) {
	a, err := AddrOf(mustRef(t, `"f":Box[ID=7].Key.Sub`))
	if err != nil || a != (Addr{File: "f", Box: "Box", ID: 7, HasID: true}) {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = AddrOf(mustRef(t, `"f":Config`))
	if err != nil || a.HasID {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := AddrOf(mustRef(t, `"f":Box[ID=*]`)); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("wildcard should be invalid: %v", err)
	}
	if _, err := AddrOf(box.Ref{}); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("empty should be invalid: %v", err)
	}
	if got := a.String(); got != `"f":Config` {
		t.Fatal(got)
	}
}

func TestParseLinkFile(t *testing.T) {
	src := `
<< relations >>
{UserGuildLink[1]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10];
{Cleanup[2]} = "players.red-xai.db":Player[ID=1] @!> "inventories.red-xai.db":Inv[ID=100];
`
	ls, err := ParseLinkFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if ls.Len() != 2 {
		t.Fatalf("len %d", ls.Len())
	}
	ed := ls.Outgoing(player(1))
	if len(ed) != 2 || ed[0].Op != OpRead || ed[1].Op != OpCascade || ed[1].Via != "link:Cleanup[ID=2]" {
		t.Fatalf("outgoing: %+v", ed)
	}
	// round trip
	ls2, err := ParseLinkFile(ls.String())
	if err != nil || ls2.Len() != 2 {
		t.Fatalf("round trip: %v\n%s", err, ls.String())
	}
	for _, bad := range []string{
		`{X[]} = 5;`,
		`{X[]} = "a":B[ID=1];`,                // bare ref is not a standalone link
		`{X[]} = "a":B[ID=*] @> "b":C[ID=1];`, // wildcard
		`{X[1]} = "a":B[ID=1] @> "b":C[ID=1]; {X[2]} = "a":B[ID=1] @> "b":C[ID=1]; {Y[1]} = [ {A[]} = 1 ];`, // non-link entry
		`{X[]} = "a":B[ID=1] @> `,
	} {
		if _, err := ParseLinkFile(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	if err := ls.Add("UserGuildLink[ID=1]", mustLink(t, `"a":B[ID=1] @> "b":C[ID=1]`)); err == nil {
		t.Fatal("duplicate name must fail")
	}
	if err := ls.Add("N", mustLink(t, `"a":B[ID=1]`)); err == nil {
		t.Fatal("bare ref must fail")
	}
}

func TestResolveAcrossFiles(t *testing.T) {
	_, e := world(t)
	// whole record
	v, err := e.Resolve(mustRef(t, `"guilds.red-xai.db":Guild[ID=10]`))
	if err != nil || v.Kind != box.KindBox {
		t.Fatalf("%v %v", v, err)
	}
	// deep field via nested path
	v, err = e.Resolve(mustRef(t, `"guilds.red-xai.db":Guild[ID=10].Stats.Rank`))
	if err != nil || v.Kind != box.KindNumber || v.Num != 3 {
		t.Fatalf("%v %v", v, err)
	}
	v, err = e.Resolve(mustRef(t, `"inventories.red-xai.db":Inv[ID=100].Slot[ID=1].Item`))
	if err != nil || v.Str != "potion" {
		t.Fatalf("%v %v", v, err)
	}
	// follow the link stored in a field
	rec, _, _ := e.Get(player(1))
	gl, _ := box.Resolve(rec.Value.Box, []box.Seg{{Name: "Guild"}})
	v, err = e.ResolveLink(gl.Value.Link)
	if err != nil || v.Kind != box.KindBox {
		t.Fatalf("ResolveLink: %v %v", v, err)
	}
	// resolved values are copies
	v.Box.Put(box.NewEntry("Hacked", nil, box.Bool(true)))
	again, _ := e.Resolve(mustRef(t, `"guilds.red-xai.db":Guild[ID=10]`))
	if again.Box.Get("Hacked", 0, false) != nil {
		t.Fatal("Resolve leaked internal state")
	}
}

func TestResolveDangling(t *testing.T) {
	_, e := world(t)
	cases := map[string]string{
		`"guilds.red-xai.db":Guild[ID=99]`:         ReasonMissingRecord,
		`"nofile.red-xai.db":Guild[ID=10]`:         ReasonMissingRecord,
		`"guilds.red-xai.db":Guild[ID=10].Nope`:    ReasonMissingField,
		`"guilds.red-xai.db":Guild[ID=10].Title.X`: ReasonMissingField, // Title is a scalar
		`"guilds.red-xai.db":Guild[ID=*]`:          ReasonInvalidRef,
	}
	for src, reason := range cases {
		_, err := e.Resolve(mustRef(t, src))
		var de *DanglingError
		if !errors.Is(err, ErrDangling) || !errors.As(err, &de) || de.Items[0].Reason != reason {
			t.Errorf("%s: want %s, got %v", src, reason, err)
		}
	}
}

func TestExpand(t *testing.T) {
	_, e := world(t)
	x, err := e.Expand(player(1), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Dangling) != 0 || len(x.Cycles) != 0 {
		t.Fatalf("%+v", x)
	}
	g, _ := box.Resolve(x.Entry.Value.Box, []box.Seg{{Name: "Guild"}})
	if g.Value.Kind != box.KindBox {
		t.Fatalf("guild not expanded: %v", g.Value)
	}
	title, _ := box.Resolve(g.Value.Box, []box.Seg{{Name: "Title"}})
	if title.Value.Str != "Dragons" {
		t.Fatal(title.Value)
	}
	bag, _ := box.Resolve(x.Entry.Value.Box, []box.Seg{{Name: "Bag"}})
	if bag.Value.Kind != box.KindBox {
		t.Fatal("bag not expanded")
	}
	// original untouched
	orig, _, _ := e.Get(player(1))
	if og, _ := box.Resolve(orig.Value.Box, []box.Seg{{Name: "Guild"}}); og.Value.Kind != box.KindLink {
		t.Fatal("Expand mutated stored record")
	}
}

func TestExpandDanglingCycleDepthAndSubpath(t *testing.T) {
	ms := newStore(t, map[string]string{
		"c.red-xai.db": `
{A[ID=1]= [ {Next[]} = "c.red-xai.db":A[ID=1] @> "c.red-xai.db":B[ID=2], {Gone[]} = "c.red-xai.db":A[ID=1] @> "c.red-xai.db":Z[ID=9],
            {Pick[]} = "c.red-xai.db":A[ID=1] @> "c.red-xai.db":B[ID=2].Val, {BadPick[]} = "c.red-xai.db":A[ID=1] @> "c.red-xai.db":B[ID=2].Nope,
            {List[]} = ;["c.red-xai.db":A[ID=1] @> "c.red-xai.db":B[ID=2]]: ]};
{B[ID=2]= [ {Val[]} = 7, {Back[]} = "c.red-xai.db":B[ID=2] @> "c.red-xai.db":A[ID=1], {Self[]} = "c.red-xai.db":B[ID=2] ]};`,
	})
	e := New(ms, Options{})
	a := Addr{File: "c.red-xai.db", Box: "A", ID: 1, HasID: true}
	x, err := e.Expand(a, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Dangling) != 2 { // Gone (missing record) and BadPick (missing field)
		t.Fatalf("dangling: %v", x.Dangling)
	}
	reasons := []string{x.Dangling[0].Reason, x.Dangling[1].Reason}
	if !(contains(reasons, ReasonMissingRecord) && contains(reasons, ReasonMissingField)) {
		t.Fatalf("reasons %v", reasons)
	}
	// B.Back -> A closes a cycle; B.Self -> B is a cycle. Each expansion of B (Next and List[0]) reports both.
	if len(x.Cycles) < 2 {
		t.Fatalf("cycles: %v", x.Cycles)
	}
	next, _ := box.Resolve(x.Entry.Value.Box, []box.Seg{{Name: "Next"}})
	if next.Value.Kind != box.KindBox {
		t.Fatalf("Next not expanded: %v", next.Value)
	}
	back, _ := box.Resolve(next.Value.Box, []box.Seg{{Name: "Back"}})
	if back.Value.Kind != box.KindLink {
		t.Fatal("cyclic link must be left in place")
	}
	pick, _ := box.Resolve(x.Entry.Value.Box, []box.Seg{{Name: "Pick"}})
	if pick.Value.Kind != box.KindNumber || pick.Value.Num != 7 {
		t.Fatalf("subpath link: %v", pick.Value)
	}
	list, _ := box.Resolve(x.Entry.Value.Box, []box.Seg{{Name: "List"}})
	if list.Value.Kind != box.KindArray || list.Value.Arr[0].Kind != box.KindBox {
		t.Fatalf("array link: %v", list.Value)
	}
	// depth 1 expands A's direct links but leaves nested ones
	x1, _ := e.Expand(a, 1)
	n1, _ := box.Resolve(x1.Entry.Value.Box, []box.Seg{{Name: "Next"}})
	b1, _ := box.Resolve(n1.Value.Box, []box.Seg{{Name: "Self"}})
	if b1.Value.Kind != box.KindLink {
		t.Fatal("depth limit not applied")
	}
	// missing root
	if _, err := e.Expand(Addr{File: "c.red-xai.db", Box: "Nope"}, 0); !errors.Is(err, ErrDangling) {
		t.Fatalf("missing root: %v", err)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestCheckDangling(t *testing.T) {
	ms, e := world(t)
	// healthy world
	d, err := e.CheckDangling()
	if err != nil || len(d) != 0 {
		t.Fatalf("healthy: %v %v", d, err)
	}
	// make guild vanish behind the engine's back, and add standalone links
	if _, err := ms.Delete(guild(10)); err != nil {
		t.Fatal(err)
	}
	e.Links().Add("L1", mustLink(t, `"players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10]`))
	e.Links().Add("L2", mustLink(t, `"players.red-xai.db":Player[ID=77] @> "players.red-xai.db":Player[ID=1]`))
	d, err = e.CheckDangling()
	if err != nil {
		t.Fatal(err)
	}
	// Player1.Guild, Player2.Guild, L1 target, L2 source
	if len(d) != 4 {
		t.Fatalf("want 4 dangling, got %d: %v", len(d), d)
	}
	var src int
	for _, x := range d {
		if x.Reason == ReasonMissingSource {
			src++
		}
	}
	if src != 1 {
		t.Fatalf("missing-source count %d", src)
	}
	// scoped to one file
	d, _ = e.CheckDangling(fInv)
	if len(d) != 0 {
		t.Fatalf("scoped: %v", d)
	}
}

func TestPutValidation(t *testing.T) {
	ms, e := world(t)
	good := box.NewEntry("Player", ptr(5), box.BoxValue(box.NewBox()))
	if err := e.Put(player(6), good, PutOptions{}); !errors.Is(err, ErrEntryMismatch) {
		t.Fatalf("id mismatch: %v", err)
	}
	if err := e.Put(guild(5), good, PutOptions{}); !errors.Is(err, ErrEntryMismatch) {
		t.Fatalf("key mismatch: %v", err)
	}
	if err := e.Put(player(5), good, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	// RequireTargets
	b := box.NewBox()
	b.Put(box.NewEntry("G", nil, box.LinkValue(mustLink(t, `"players.red-xai.db":Player[ID=7] @> "guilds.red-xai.db":Guild[ID=404]`))))
	bad := box.NewEntry("Player", ptr(7), box.BoxValue(b))
	err := e.Put(player(7), bad, PutOptions{RequireTargets: true})
	var de *DanglingError
	if !errors.As(err, &de) || len(de.Items) != 1 {
		t.Fatalf("want dangling error, got %v", err)
	}
	if _, ok, _ := ms.Get(player(7)); ok {
		t.Fatal("rejected write must not be stored")
	}
	if err := e.Put(player(7), bad, PutOptions{}); err != nil {
		t.Fatal(err) // allowed when targets are not required
	}
	d, _ := e.CheckDangling(fPlayers)
	if len(d) != 1 {
		t.Fatalf("dangling after lenient put: %v", d)
	}
}

func ptr(i int64) *int64 { return &i }

func TestDeleteCascadeAcrossFiles(t *testing.T) {
	ms, e := world(t)
	res, err := e.Delete(player(1), DeleteOptions{Inbound: InboundReport})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 2 || res.Deleted[0] != inv(100) || res.Deleted[1] != player(1) {
		t.Fatalf("expected leaves first [inv, player]: %v", res.Deleted)
	}
	if ms.Len(fInv) != 0 {
		t.Fatal("cascade target not deleted")
	}
	if ms.Len(fGuilds) != 1 || ms.Len(fPlayers) != 1 {
		t.Fatal("@> targets must not be deleted")
	}
	if len(res.Inbound) != 0 {
		t.Fatalf("inbound: %v", res.Inbound)
	}
	if res.RootMissing || len(res.Cycles) != 0 {
		t.Fatalf("%+v", res)
	}
	// deleting again is a no-op, not an error
	res, err = e.Delete(player(1), DeleteOptions{})
	if err != nil || !res.RootMissing || len(res.Deleted) != 0 {
		t.Fatalf("second delete: %+v %v", res, err)
	}
}

func TestDeleteReadLinkDoesNotCascade(t *testing.T) {
	ms, e := world(t)
	if _, err := e.Delete(player(2), DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if ms.Len(fGuilds) != 1 {
		t.Fatal("deleting a record must not delete @> targets")
	}
}

func TestDeleteStandaloneCascadeAndPrune(t *testing.T) {
	ms, e := world(t)
	e.Links().Add("Own[1]", mustLink(t, `"players.red-xai.db":Player[ID=2] @!> "guilds.red-xai.db":Guild[ID=10]`))
	e.Links().Add("Other[2]", mustLink(t, `"guilds.red-xai.db":Guild[ID=10] @> "players.red-xai.db":Player[ID=1]`))
	res, err := e.Delete(player(2), DeleteOptions{PruneLinks: true})
	if err != nil {
		t.Fatal(err)
	}
	if ms.Len(fGuilds) != 0 {
		t.Fatal("standalone cascade must delete the guild")
	}
	if len(res.Deleted) != 2 || len(res.PrunedLinks) != 2 || e.Links().Len() != 0 {
		t.Fatalf("%+v links=%d", res, e.Links().Len())
	}
	// without PruneLinks the links stay (and show up as dangling)
	_, e2 := world(t)
	e2.Links().Add("Own[1]", mustLink(t, `"players.red-xai.db":Player[ID=2] @!> "guilds.red-xai.db":Guild[ID=10]`))
	if _, err := e2.Delete(player(2), DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if e2.Links().Len() != 1 {
		t.Fatal("link must be kept without PruneLinks")
	}
	d, _ := e2.CheckDangling()
	if len(d) == 0 {
		t.Fatal("kept link should be reported dangling")
	}
}

// chain builds N records R1 @!> R2 @!> ... in one file.
func cascadeRing(t *testing.T, n int, closeRing bool) (*MemStore, *Engine) {
	ms := NewMemStore()
	for i := 1; i <= n; i++ {
		b := box.NewBox()
		if i < n || closeRing {
			next := i%n + 1
			b.Put(box.NewEntry("Next", nil, box.LinkValue(mustLink(t,
				fmt.Sprintf(`"r.red-xai.db":N[ID=%d] @!> "r.red-xai.db":N[ID=%d]`, i, next)))))
		}
		id := int64(i)
		if err := ms.Put(Addr{File: "r.red-xai.db", Box: "N", ID: id, HasID: true}, box.NewEntry("N", &id, box.BoxValue(b))); err != nil {
			t.Fatal(err)
		}
	}
	return ms, New(ms, Options{})
}

func nAddr(i int) Addr { return Addr{File: "r.red-xai.db", Box: "N", ID: int64(i), HasID: true} }

func TestDeleteCycleTerminates(t *testing.T) {
	for _, n := range []int{1, 2, 5, 50} {
		ms, e := cascadeRing(t, n, true)
		res, err := e.Delete(nAddr(1), DeleteOptions{})
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(res.Deleted) != n || ms.Len("r.red-xai.db") != 0 {
			t.Fatalf("n=%d deleted %d left %d", n, len(res.Deleted), ms.Len("r.red-xai.db"))
		}
		if len(res.Cycles) != 1 {
			t.Fatalf("n=%d: want exactly the closing edge reported, got %v", n, res.Cycles)
		}
		if res.Deleted[len(res.Deleted)-1] != nAddr(1) {
			t.Fatalf("root must be last: %v", res.Deleted)
		}
	}
}

func TestDeleteSelfCascadeAndDiamond(t *testing.T) {
	// self link
	ms := NewMemStore()
	id := int64(1)
	b := box.NewBox()
	b.Put(box.NewEntry("Me", nil, box.LinkValue(mustLink(t, `"s":N[ID=1] @!> "s":N[ID=1]`))))
	ms.Put(Addr{"s", "N", 1, true}, box.NewEntry("N", &id, box.BoxValue(b)))
	e := New(ms, Options{})
	res, err := e.Delete(Addr{"s", "N", 1, true}, DeleteOptions{})
	if err != nil || len(res.Deleted) != 1 || len(res.Cycles) != 1 {
		t.Fatalf("%+v %v", res, err)
	}

	// diamond: A -> B, A -> C, B -> D, C -> D (not a cycle, D deleted once)
	ms = NewMemStore()
	mk := func(name string, targets ...string) {
		id := int64(1)
		b := box.NewBox()
		for i, tg := range targets {
			b.Put(box.NewEntry(fmt.Sprintf("L%d", i), nil, box.LinkValue(mustLink(t,
				fmt.Sprintf(`"d":%s[ID=1] @!> "d":%s[ID=1]`, name, tg)))))
		}
		ms.Put(Addr{"d", name, 1, true}, box.NewEntry(name, &id, box.BoxValue(b)))
	}
	mk("A", "B", "C")
	mk("B", "D")
	mk("C", "D")
	mk("D")
	e = New(ms, Options{})
	res, err = e.Delete(Addr{"d", "A", 1, true}, DeleteOptions{})
	if err != nil || len(res.Deleted) != 4 || len(res.Cycles) != 0 {
		t.Fatalf("diamond: %+v %v", res, err)
	}
	if ms.DeleteOrder()[0].Box != "D" {
		t.Fatalf("D (deepest) must go first: %v", ms.DeleteOrder())
	}
}

func TestDeleteCycleNotDetectedFalsePositive(t *testing.T) {
	// A->B, A->C, C->B : revisit of B is a diamond, not a cycle.
	ms := NewMemStore()
	mk := func(name string, targets ...string) {
		id := int64(1)
		b := box.NewBox()
		for i, tg := range targets {
			b.Put(box.NewEntry(fmt.Sprintf("L%d", i), nil, box.LinkValue(mustLink(t,
				fmt.Sprintf(`"d":%s[ID=1] @!> "d":%s[ID=1]`, name, tg)))))
		}
		ms.Put(Addr{"d", name, 1, true}, box.NewEntry(name, &id, box.BoxValue(b)))
	}
	mk("A", "B", "C")
	mk("C", "B")
	mk("B")
	res, err := New(ms, Options{}).Delete(Addr{"d", "A", 1, true}, DeleteOptions{})
	if err != nil || len(res.Deleted) != 3 || len(res.Cycles) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	// non-tree cycle: A->B, A->C, B->C, C->B must be reported
	ms = NewMemStore()
	mk = func(name string, targets ...string) {
		id := int64(1)
		b := box.NewBox()
		for i, tg := range targets {
			b.Put(box.NewEntry(fmt.Sprintf("L%d", i), nil, box.LinkValue(mustLink(t,
				fmt.Sprintf(`"d":%s[ID=1] @!> "d":%s[ID=1]`, name, tg)))))
		}
		ms.Put(Addr{"d", name, 1, true}, box.NewEntry(name, &id, box.BoxValue(b)))
	}
	mk("A", "B", "C")
	mk("B", "C")
	mk("C", "B")
	res, err = New(ms, Options{}).Delete(Addr{"d", "A", 1, true}, DeleteOptions{})
	if err != nil || len(res.Deleted) != 3 || len(res.Cycles) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDeleteDanglingCascadeTargetSkipped(t *testing.T) {
	ms, e := world(t)
	ms.Delete(inv(100)) // Player1.Bag now points nowhere
	res, err := e.Delete(player(1), DeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 1 || len(res.Dangling) != 1 || res.Dangling[0].Reason != ReasonMissingRecord {
		t.Fatalf("%+v", res)
	}
}

func TestDeleteBadCascadeTarget(t *testing.T) {
	ms := newStore(t, map[string]string{
		"x": `{A[ID=1]= [ {L[]} = "x":A[ID=1] @!> "x":B[ID=1].Field ]}; {B[ID=1]= [ {Field[]} = 1 ]};`,
	})
	e := New(ms, Options{})
	_, err := e.Delete(Addr{"x", "A", 1, true}, DeleteOptions{})
	if !errors.Is(err, ErrBadCascadeTarget) {
		t.Fatalf("got %v", err)
	}
	if ms.Len("x") != 2 {
		t.Fatal("nothing may be deleted when the plan is rejected")
	}
	// wildcard cascade targets are reported, not followed
	ms = newStore(t, map[string]string{
		"x": `{A[ID=1]= [ {L[]} = "x":A[ID=1] @!> "x":B[ID=*] ]};`,
	})
	res, err := New(ms, Options{}).Delete(Addr{"x", "A", 1, true}, DeleteOptions{})
	if err != nil || len(res.Dangling) != 1 || res.Dangling[0].Reason != ReasonInvalidRef {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDeleteCascadeReject(t *testing.T) {
	ms, e := world(t)
	_, err := e.Delete(player(1), DeleteOptions{Cascade: CascadeReject})
	var ce *CascadeError
	if !errors.Is(err, ErrCascadeRejected) || !errors.As(err, &ce) || len(ce.Edges) != 1 {
		t.Fatalf("got %v", err)
	}
	if ms.Len(fPlayers) != 2 || ms.Len(fInv) != 1 {
		t.Fatal("rejected delete must not delete anything")
	}
	// a record without cascades is fine under CascadeReject
	if _, err := e.Delete(player(2), DeleteOptions{Cascade: CascadeReject}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteCascadeNone(t *testing.T) {
	ms, e := world(t)
	res, err := e.Delete(player(1), DeleteOptions{Cascade: CascadeNone})
	if err != nil || len(res.Deleted) != 1 || ms.Len(fInv) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDeleteInboundRestrict(t *testing.T) {
	ms, e := world(t)
	// Guild 10 is referenced by Player1 and Player2 (@>).
	_, err := e.Delete(guild(10), DeleteOptions{Inbound: InboundRestrict})
	var re *RestrictError
	if !errors.Is(err, ErrRestricted) || !errors.As(err, &re) || len(re.Blockers) != 2 {
		t.Fatalf("got %v", err)
	}
	if ms.Len(fGuilds) != 1 {
		t.Fatal("restricted delete must not delete")
	}
	// standalone link also blocks
	e.Links().Add("S", mustLink(t, `"players.red-xai.db":Player[ID=1] @> "inventories.red-xai.db":Inv[ID=100]`))
	_, err = e.Delete(inv(100), DeleteOptions{Inbound: InboundRestrict})
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("standalone link should block: %v", err)
	}
	// the owner Player1 also cascades into inv: deleting Player1 (with cascade) is fine
	// because everything that points at inv is inside the delete set.
	res, err := e.Delete(player(1), DeleteOptions{Inbound: InboundRestrict})
	if err != nil {
		t.Fatalf("internal references must not block: %v", err)
	}
	if len(res.Deleted) != 2 {
		t.Fatal(res.Deleted)
	}
	// Report mode: delete succeeds and lists what dangles (Player2.Guild)
	res, err = e.Delete(guild(10), DeleteOptions{Inbound: InboundReport})
	if err != nil || len(res.Inbound) != 1 || res.Inbound[0].Owner != player(2) {
		t.Fatalf("%+v %v", res, err)
	}
	d, _ := e.CheckDangling()
	if len(d) == 0 {
		t.Fatal("CheckDangling should now report the orphaned reference")
	}
}

func TestDeleteDryRunAndLimits(t *testing.T) {
	ms, e := world(t)
	res, err := e.PlanDelete(player(1), DeleteOptions{})
	if err != nil || !res.DryRun || len(res.Planned) != 2 || len(res.Deleted) != 0 || ms.Len(fInv) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	// dry run still enforces policies
	if _, err := e.PlanDelete(guild(10), DeleteOptions{Inbound: InboundRestrict}); !errors.Is(err, ErrRestricted) {
		t.Fatal(err)
	}
	// record limit
	_, e2 := cascadeRing(t, 10, false)
	if _, err := e2.Delete(nAddr(1), DeleteOptions{MaxRecords: 5}); !errors.Is(err, ErrTooManyRecords) {
		t.Fatalf("got %v", err)
	}
	res, err = e2.Delete(nAddr(1), DeleteOptions{})
	if err != nil || len(res.Deleted) != 10 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDeletePartialFailureThenRetry(t *testing.T) {
	ms, e := cascadeRing(t, 4, false) // 1 -> 2 -> 3 -> 4
	fail := true
	ms.DeleteErr = func(a Addr) error {
		if fail && a == nAddr(2) {
			return errors.New("disk full")
		}
		return nil
	}
	res, err := e.Delete(nAddr(1), DeleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got %v", err)
	}
	// leaves first: 4 and 3 were deleted, 2 failed, 1 (the owner) still exists
	if len(res.Deleted) != 2 || res.Deleted[0] != nAddr(4) || res.Deleted[1] != nAddr(3) {
		t.Fatalf("%v", res.Deleted)
	}
	if _, ok, _ := ms.Get(nAddr(1)); !ok {
		t.Fatal("owner must survive a failed cascade so the delete can be retried")
	}
	fail = false
	res, err = e.Delete(nAddr(1), DeleteOptions{})
	if err != nil || ms.Len("r.red-xai.db") != 0 {
		t.Fatalf("retry: %+v %v left=%d", res, err, ms.Len("r.red-xai.db"))
	}
	if len(res.Dangling) != 1 { // 2 -> 3 now points at a deleted record and was skipped
		t.Fatalf("retry should report the already-deleted target: %+v", res.Dangling)
	}
}

func TestConcurrentOperations(t *testing.T) {
	ms := NewMemStore()
	e := New(ms, Options{})
	const n = 40
	for i := 1; i <= n; i++ {
		p := player(int64(i))
		c := inv(int64(i))
		pb := box.NewBox()
		pb.Put(box.NewEntry("Bag", nil, box.LinkValue(mustLink(t,
			fmt.Sprintf(`"players.red-xai.db":Player[ID=%d] @!> "inventories.red-xai.db":Inv[ID=%d]`, i, i)))))
		pb.Put(box.NewEntry("G", nil, box.LinkValue(mustLink(t,
			`"players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10]`))))
		if err := e.Put(p, box.NewEntry("Player", &p.ID, box.BoxValue(pb)), PutOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := e.Put(c, box.NewEntry("Inv", &c.ID, box.BoxValue(box.NewBox())), PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	g := guild(10)
	e.Put(g, box.NewEntry("Guild", &g.ID, box.BoxValue(box.NewBox())), PutOptions{})

	var wg sync.WaitGroup
	// deleters
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := e.Delete(player(int64(i)), DeleteOptions{Inbound: InboundReport}); err != nil {
				t.Errorf("delete %d: %v", i, err)
			}
		}(i)
	}
	// readers / scanners / writers running at the same time
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for k := 0; k < 50; k++ {
				i := int64(k%n + 1)
				e.Expand(player(i), 0)
				e.Resolve(inv(i).Ref())
				e.CheckDangling()
				e.Outgoing(player(i))
				ls := fmt.Sprintf("L%d_%d", r, k)
				e.Links().Add(ls, mustLink(t, `"players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10]`))
				e.Links().Remove(ls)
				e.Put(guild(int64(100+r)), box.NewEntry("Guild", ptr(int64(100+r)), box.BoxValue(box.NewBox())), PutOptions{})
			}
		}(r)
	}
	wg.Wait()
	if ms.Len(fPlayers) != 0 || ms.Len(fInv) != 0 {
		t.Fatalf("after concurrent cascades: players=%d inv=%d", ms.Len(fPlayers), ms.Len(fInv))
	}
}

func TestConcurrentDeleteSameRoot(t *testing.T) {
	_, e := cascadeRing(t, 20, true)
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := e.Delete(nAddr(1), DeleteOptions{})
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			total += len(res.Deleted)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 20 {
		t.Fatalf("each record must be deleted exactly once across racers, got %d", total)
	}
}
