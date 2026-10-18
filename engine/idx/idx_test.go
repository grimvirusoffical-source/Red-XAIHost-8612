package idx

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"redx/box"
	"redx/engine/link"
)

const pf = "players.red-xai.db"

// rec builds a record entry: kv is key, value, key, value ... where value is
// string, int, float64, bool, []string, []float64 or *box.Box.
func rec(name string, id int64, kv ...interface{}) (Addr, *box.Entry) {
	b := box.NewBox()
	for i := 0; i < len(kv); i += 2 {
		var v box.Value
		switch x := kv[i+1].(type) {
		case string:
			v = box.String(x)
		case int:
			v = box.Int(int64(x))
		case float64:
			v = box.Number(x)
		case bool:
			v = box.Bool(x)
		case []string:
			var vs []box.Value
			for _, s := range x {
				vs = append(vs, box.String(s))
			}
			v = box.Array(vs...)
		case []float64:
			var vs []box.Value
			for _, s := range x {
				vs = append(vs, box.Number(s))
			}
			v = box.Array(vs...)
		case *box.Box:
			v = box.BoxValue(x)
		default:
			panic("bad kv")
		}
		b.Put(box.NewEntry(kv[i].(string), nil, v))
	}
	e := box.NewEntry(name, &id, box.BoxValue(b))
	return link.EntryAddr(pf, e), e
}

func put(t testing.TB, s Store, name string, id int64, kv ...interface{}) Addr {
	t.Helper()
	a, e := rec(name, id, kv...)
	if err := s.Put(a, e); err != nil {
		t.Fatal(err)
	}
	return a
}

func mustIdx(t testing.TB, src string) *Index {
	t.Helper()
	ds, err := ParseDecls(src)
	if err != nil || len(ds) != 1 {
		t.Fatalf("ParseDecls: %v %v", ds, err)
	}
	ix, err := New(ds[0])
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func decl(name, kind, fields string, extra string) string {
	return fmt.Sprintf(`{%s[]} = [ {File[]} = %q, {Box[]} = "Player", {Fields[]} = %s, {Kind[]} = %q%s ];`,
		name, pf, fields, kind, extra)
}

func ids(hs []Hit) []int64 {
	out := []int64{}
	for _, h := range hs {
		out = append(out, h.Addr.ID)
	}
	return out
}

func eq(t *testing.T, got, want []int64) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// ---- declarations --------------------------------------------------------

func TestParseDecls(t *testing.T) {
	src := `
{PlayerByName[]} = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = "Name", {Kind[]} = "Exact", {Fold[]} = TRUE ];
{Stats[ID=2]} = [ {File[]} = "players.red-xai.db", {Fields[]} = ;["Level", "Stats.Gold"]:, {Kind[]} = "range" ];
`
	ds, err := ParseDecls(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0].Name != "PlayerByName" || ds[0].Kind != KindExact || !ds[0].Fold ||
		ds[0].Box != "Player" || ds[1].Name != "Stats[ID=2]" || len(ds[1].Fields) != 2 || ds[1].Box != "" {
		t.Fatalf("%+v", ds)
	}
	// round trip through FormatDecls
	out := FormatDecls(ds)
	ds2, err := ParseDecls(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for i := range ds {
		if ds[i].hash() != ds2[i].hash() {
			t.Fatalf("round trip changed %s\n%s", ds[i].Name, out)
		}
	}
}

func TestParseDeclsErrors(t *testing.T) {
	cases := map[string]string{
		"not a box":     `{A[]} = 1;`,
		"no file":       `{A[]} = [ {Fields[]} = "x", {Kind[]} = "exact" ];`,
		"no kind":       `{A[]} = [ {File[]} = "f", {Fields[]} = "x" ];`,
		"bad kind":      `{A[]} = [ {File[]} = "f", {Fields[]} = "x", {Kind[]} = "fuzzy" ];`,
		"no fields":     `{A[]} = [ {File[]} = "f", {Kind[]} = "exact" ];`,
		"empty fields":  `{A[]} = [ {File[]} = "f", {Fields[]} = ;[]:, {Kind[]} = "exact" ];`,
		"dup field":     `{A[]} = [ {File[]} = "f", {Fields[]} = ;["x","x"]:, {Kind[]} = "exact" ];`,
		"wild field":    `{A[]} = [ {File[]} = "f", {Fields[]} = "*", {Kind[]} = "exact" ];`,
		"file in field": `{A[]} = [ {File[]} = "f", {Fields[]} = "g:x", {Kind[]} = "exact" ];`,
		"fold range":    `{A[]} = [ {File[]} = "f", {Fields[]} = "x", {Kind[]} = "range", {Fold[]} = TRUE ];`,
		"unknown entry": `{A[]} = [ {File[]} = "f", {Fields[]} = "x", {Kind[]} = "exact", {Colour[]} = "red" ];`,
		"fields type":   `{A[]} = [ {File[]} = "f", {Fields[]} = 3, {Kind[]} = "exact" ];`,
		"syntax":        `{A[]} = [`,
	}
	for name, src := range cases {
		_, err := ParseDecls(src)
		if err == nil {
			t.Errorf("%s: expected an error", name)
		} else if name != "syntax" && !errors.Is(err, ErrBadDecl) {
			t.Errorf("%s: error does not wrap ErrBadDecl: %v", name, err)
		}
	}
	if _, err := ParseManager(decl("A", "exact", `"x"`, "") + decl("A", "exact", `"y"`, "")); err == nil {
		t.Error("duplicate names should fail") // box parser rejects duplicate keys or the manager does
	}
}

// ---- exact ----------------------------------------------------------------

func TestExact(t *testing.T) {
	ix := mustIdx(t, decl("ByName", "exact", `;["Name", "Tags", "Stats.Lvl", "Alive"]:`, ""))
	st := &ixStore{ix: ix}
	st.put(t, 1, "Name", "Ann", "Tags", []string{"x", "y"}, "Alive", true)
	st.put(t, 2, "Name", "ann", "Tags", []string{"y"}, "Alive", false)
	st.put(t, 3, "Name", "Bob", "Stats", func() *box.Box {
		b := box.NewBox()
		b.Put(box.NewEntry("Lvl", nil, box.Int(7)))
		return b
	}(), "Alive", true)
	st.put(t, 4, "Name", "7", "Stats", func() *box.Box {
		b := box.NewBox()
		b.Put(box.NewEntry("Lvl", nil, box.String("7")))
		return b
	}())

	h, err := ix.Exact("Name", box.String("Ann"), 0)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, ids(h), []int64{1}) // case sensitive
	h, _ = ix.Exact("Tags", box.String("y"), 0)
	eq(t, ids(h), []int64{1, 2}) // array values, stable address order
	h, _ = ix.Exact("Alive", box.Bool(true), 0)
	eq(t, ids(h), []int64{1, 3})
	h, _ = ix.Exact("Stats.Lvl", box.Int(7), 0)
	eq(t, ids(h), []int64{3}) // number does not equal string "7"
	h, _ = ix.Exact("Stats.Lvl", box.String("7"), 0)
	eq(t, ids(h), []int64{4})
	h, _ = ix.Exact("Name", box.String("7"), 0)
	eq(t, ids(h), []int64{4})
	h, _ = ix.Exact("Tags", box.String("y"), 1)
	eq(t, ids(h), []int64{1})

	// replace: old values disappear
	st.put(t, 1, "Name", "Zed")
	h, _ = ix.Exact("Name", box.String("Ann"), 0)
	eq(t, ids(h), []int64{})
	h, _ = ix.Exact("Tags", box.String("x"), 0)
	eq(t, ids(h), []int64{})
	h, _ = ix.Exact("Name", box.String("Zed"), 0)
	eq(t, ids(h), []int64{1})
	// delete
	st.del(2)
	h, _ = ix.Exact("Name", box.String("ann"), 0)
	eq(t, ids(h), []int64{})

	// errors
	if _, err := ix.Exact("", box.String("a"), 0); !errors.Is(err, ErrFieldRequired) {
		t.Fatal(err)
	}
	if _, err := ix.Exact("Nope", box.String("a"), 0); !errors.Is(err, ErrUnknownField) {
		t.Fatal(err)
	}
	if _, err := ix.Exact("Name", box.Nell(), 0); !errors.Is(err, ErrBadQuery) {
		t.Fatal(err)
	}
	if _, err := ix.Prefix("Name", "a", 0); !errors.Is(err, ErrWrongKind) {
		t.Fatal(err)
	}
}

func TestExactFoldAndSingleField(t *testing.T) {
	ix := mustIdx(t, decl("ByName", "exact", `"Name"`, `, {Fold[]} = TRUE`))
	st := &ixStore{ix: ix}
	st.put(t, 1, "Name", "Ann")
	st.put(t, 2, "Name", "ANN")
	st.put(t, 3, "Name", "Anna")
	h, err := ix.Exact("", box.String("aNn"), 0) // "" ok with a single field
	if err != nil {
		t.Fatal(err)
	}
	eq(t, ids(h), []int64{1, 2})
}

func TestScopeFileAndBox(t *testing.T) {
	ix := mustIdx(t, decl("ByName", "exact", `"Name"`, ""))
	st := &ixStore{ix: ix}
	st.put(t, 1, "Name", "Ann")
	// other box, other file: ignored
	a, e := rec("Monster", 2, "Name", "Ann")
	ix.OnPut(a, e)
	a, e = rec("Player", 3, "Name", "Ann")
	a.File = "other.db"
	ix.OnPut(a, e)
	h, _ := ix.Exact("Name", box.String("Ann"), 0)
	eq(t, ids(h), []int64{1})
	if ix.Stats().Records != 1 {
		t.Fatal(ix.Stats())
	}
}

// ---- prefix ---------------------------------------------------------------

func TestPrefix(t *testing.T) {
	ix := mustIdx(t, decl("P", "prefix", `;["Name", "Alias"]:`, `, {Fold[]} = TRUE`))
	st := &ixStore{ix: ix}
	st.put(t, 5, "Name", "alpha")
	st.put(t, 2, "Name", "Alphabet")
	st.put(t, 9, "Name", "beta", "Alias", "Alp")
	st.put(t, 1, "Name", "alpha") // same key as 5: order by id
	st.put(t, 7, "Name", "Alpine", "Alias", "alpine2")

	h, err := ix.Prefix("Name", "AL", 0)
	if err != nil {
		t.Fatal(err)
	}
	// ordered by value (alpha, alpha, alphabet, alpine) then address
	eq(t, ids(h), []int64{1, 5, 2, 7})
	h, _ = ix.Prefix("Name", "alp", 2)
	eq(t, ids(h), []int64{1, 5})
	h, _ = ix.Prefix("Alias", "alp", 0)
	eq(t, ids(h), []int64{9, 7}) // "alp" < "alpine2"
	h, _ = ix.Prefix("Name", "", 0)
	eq(t, ids(h), []int64{1, 5, 2, 7, 9}) // empty prefix = everything in key order, once each
	h, _ = ix.Prefix("Name", "zzz", 0)
	eq(t, ids(h), []int64{})
	if h, _ = ix.Prefix("Name", "alpha", 0); h[0].Value.Str != "alpha" {
		t.Fatalf("%v", h[0].Value)
	}
	st.del(1)
	h, _ = ix.Prefix("Name", "alpha", 0)
	eq(t, ids(h), []int64{5, 2})
}

func TestPrefixCaseSensitive(t *testing.T) {
	ix := mustIdx(t, decl("P", "prefix", `"Name"`, ""))
	st := &ixStore{ix: ix}
	st.put(t, 1, "Name", "Alpha")
	st.put(t, 2, "Name", "alpha")
	h, _ := ix.Prefix("Name", "Al", 0)
	eq(t, ids(h), []int64{1})
}

// ---- range ----------------------------------------------------------------

func TestRange(t *testing.T) {
	ix := mustIdx(t, decl("R", "range", `;["Level", "Scores"]:`, ""))
	st := &ixStore{ix: ix}
	st.put(t, 1, "Level", 10)
	st.put(t, 2, "Level", 5)
	st.put(t, 3, "Level", 10)
	st.put(t, 4, "Level", -3.5)
	st.put(t, 5, "Level", 0)
	st.put(t, 6, "Level", "12") // strings are not numbers: not indexed
	st.put(t, 7, "Level", 1e9)
	st.put(t, 8, "Scores", []float64{1, 50, 99})

	h, err := ix.Range("Level", Incl(5), Incl(10), 0)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, ids(h), []int64{2, 1, 3}) // by number, then id
	h, _ = ix.Range("Level", Excl(5), Excl(10), 0)
	eq(t, ids(h), []int64{})
	h, _ = ix.Range("Level", Excl(5), nil, 0)
	eq(t, ids(h), []int64{1, 3, 7})
	h, _ = ix.Range("Level", nil, Excl(5), 0)
	eq(t, ids(h), []int64{4, 5})
	h, _ = ix.Range("Level", nil, nil, 3)
	eq(t, ids(h), []int64{4, 5, 2})
	h, _ = ix.Range("Level", Incl(-1), Incl(0), 0)
	eq(t, ids(h), []int64{5})
	h, _ = ix.Range("Scores", Incl(40), Incl(60), 0)
	eq(t, ids(h), []int64{8})
	if h[0].Value.Num != 50 {
		t.Fatalf("%v", h[0].Value)
	}
	h, _ = ix.Range("Scores", Incl(0), nil, 0)
	eq(t, ids(h), []int64{8}) // once, though three values match
	h, _ = ix.Range("Level", Incl(7), Incl(3), 0)
	eq(t, ids(h), []int64{}) // inverted range is empty
	if _, err := ix.Range("Level", Incl(nan()), nil, 0); !errors.Is(err, ErrBadQuery) {
		t.Fatal(err)
	}
	// update moves the record
	st.put(t, 2, "Level", 500)
	h, _ = ix.Range("Level", Incl(100), Incl(1000), 0)
	eq(t, ids(h), []int64{2})
	h, _ = ix.Range("Level", Incl(5), Incl(5), 0)
	eq(t, ids(h), []int64{})
}

func nan() float64 { z := 0.0; return z / z }

// ---- text -----------------------------------------------------------------

func TestText(t *testing.T) {
	ix := mustIdx(t, decl("T", "text", `;["Bio", "Notes"]:`, ""))
	st := &ixStore{ix: ix}
	st.put(t, 3, "Bio", "Red dragon slayer, loves FIRE.")
	st.put(t, 1, "Bio", "Blue dragon; loves ice and snow")
	st.put(t, 2, "Bio", "Red-haired healer", "Notes", "dragon whisperer")
	st.put(t, 4, "Bio", "Café owner, naïve über-fan 42")
	st.put(t, 5, "Bio", "")

	h, err := ix.Text("Bio", And, []string{"dragon", "loves"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, ids(h), []int64{1, 3})
	h, _ = ix.Text("Bio", And, []string{"RED dragon"}, 0) // one string = two tokens
	eq(t, ids(h), []int64{3})
	h, _ = ix.Text("Bio", Or, []string{"snow", "healer", "nothing"}, 0)
	eq(t, ids(h), []int64{1, 2})
	h, _ = ix.Text("Bio", And, []string{"dragon", "nothing"}, 0)
	eq(t, ids(h), []int64{})
	h, _ = ix.Text("Bio", Or, []string{"red"}, 0)
	eq(t, ids(h), []int64{2, 3}) // "Red-haired" splits on '-'
	h, _ = ix.Text("Notes", Or, []string{"dragon"}, 0)
	eq(t, ids(h), []int64{2}) // fields are separate
	h, _ = ix.Text("Bio", And, []string{"café", "ÜBER", "42"}, 0)
	eq(t, ids(h), []int64{4})
	h, _ = ix.Text("Bio", Or, []string{"dragon"}, 2)
	eq(t, ids(h), []int64{1, 3})
	if _, err := ix.Text("Bio", And, []string{"?!"}, 0); !errors.Is(err, ErrBadQuery) {
		t.Fatal(err)
	}
	if _, err := ix.Text("Bio", "xor", []string{"a"}, 0); !errors.Is(err, ErrBadQuery) {
		t.Fatal(err)
	}
	st.put(t, 3, "Bio", "now a quiet farmer")
	h, _ = ix.Text("Bio", Or, []string{"dragon"}, 0)
	eq(t, ids(h), []int64{1})
	st.del(1)
	h, _ = ix.Text("Bio", Or, []string{"dragon"}, 0)
	eq(t, ids(h), []int64{})
}

func TestTermLimits(t *testing.T) {
	ix := mustIdx(t, decl("T", "text", `"Bio"`, ""))
	var words []string
	for i := 0; i < MaxTermsPerRecord+500; i++ {
		words = append(words, fmt.Sprintf("w%05d", i))
	}
	a, e := rec("Player", 1, "Bio", strings.Join(words, " "), "x", "y")
	ix.OnPut(a, e)
	s := ix.Stats()
	if s.Terms != MaxTermsPerRecord || s.Truncated != 1 {
		t.Fatalf("%+v", s)
	}
	// over-long tokens and invalid UTF-8 are skipped, not panics
	a, e = rec("Player", 2, "Bio", strings.Repeat("a", MaxTermBytes+1)+" ok \xff\xfe")
	ix.OnPut(a, e)
	if h, _ := ix.Text("Bio", Or, []string{"ok"}, 0); len(h) != 0 {
		t.Fatalf("string with invalid utf-8 should be skipped, got %v", h)
	}
	a, e = rec("Player", 3, "Bio", strings.Repeat("a", MaxTermBytes+1)+" ok")
	ix.OnPut(a, e)
	if h, _ := ix.Text("Bio", Or, []string{"ok"}, 0); !reflect.DeepEqual(ids(h), []int64{3}) {
		t.Fatalf("%v", h)
	}
}

// ---- observed store, rebuild, stale --------------------------------------

type ixStore struct {
	ix *Index
	m  map[int64]bool
}

func (s *ixStore) put(t *testing.T, id int64, kv ...interface{}) {
	t.Helper()
	a, e := rec("Player", id, kv...)
	s.ix.OnPut(a, e)
}
func (s *ixStore) del(id int64) {
	s.ix.OnDelete(Addr{File: pf, Box: "Player", ID: id, HasID: true})
}

func newMgr(t testing.TB) *Manager {
	t.Helper()
	m, err := ParseManager(
		decl("ByName", "exact", `"Name"`, "") +
			decl("ByLevel", "range", `"Level"`, "") +
			decl("ByBio", "text", `"Bio"`, ""))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fill(t testing.TB, s Store, n int) {
	for i := 1; i <= n; i++ {
		put(t, s, "Player", int64(i), "Name", fmt.Sprintf("p%d", i), "Level", i%10, "Bio", fmt.Sprintf("player number %d likes %s", i, []string{"cats", "dogs"}[i%2]))
	}
}

func TestObservedStoreKeepsIndexInSync(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	os := NewObservedStore(mem, m)
	var _ link.Store = os // usable wherever a link.Store is
	fill(t, os, 20)
	if d, err := m.Check(mem); err != nil || len(d) != 0 {
		t.Fatalf("stale: %v %v", d, err)
	}
	h, _ := m.Index("ByName").Exact("Name", box.String("p7"), 0)
	eq(t, ids(h), []int64{7})
	h, _ = m.Index("ByLevel").Range("Level", Incl(9), Incl(9), 0)
	eq(t, ids(h), []int64{9, 19})

	ok, err := os.Delete(Addr{File: pf, Box: "Player", ID: 7, HasID: true})
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := os.Delete(Addr{File: pf, Box: "Player", ID: 7, HasID: true}); ok {
		t.Fatal("second delete should report false")
	}
	h, _ = m.Index("ByName").Exact("Name", box.String("p7"), 0)
	eq(t, ids(h), []int64{})
	if d, _ := m.Check(mem); len(d) != 0 {
		t.Fatalf("stale after delete: %v", d)
	}
	// inner store errors are not observed
	before := m.Index("ByName").Stats().Seq
	bad := NewObservedStore(failing{mem}, m)
	a, e := rec("Player", 99, "Name", "ghost")
	if err := bad.Put(a, e); err == nil {
		t.Fatal("expected error")
	}
	if m.Index("ByName").Stats().Seq != before {
		t.Fatal("failed put was observed")
	}
}

type failing struct{ Store }

func (failing) Put(Addr, *box.Entry) error { return errors.New("disk full") }

func TestStaleDetectedAndRebuilt(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	os := NewObservedStore(mem, m)
	fill(t, os, 10)

	// writes that bypass the wrapper make the index stale
	put(t, mem, "Player", 3, "Name", "changed", "Level", 3, "Bio", "x")
	put(t, mem, "Player", 50, "Name", "new", "Level", 1, "Bio", "y")
	mem.Delete(Addr{File: pf, Box: "Player", ID: 5, HasID: true})
	d, err := m.Check(mem)
	if err != nil {
		t.Fatal(err)
	}
	dn := d["ByName"]
	if !dn.Stale() || len(dn.Changed) != 1 || dn.Changed[0].ID != 3 || len(dn.Missing) != 1 || dn.Missing[0].ID != 50 ||
		len(dn.Extra) != 1 || dn.Extra[0].ID != 5 {
		t.Fatalf("diff %+v", dn)
	}
	// a change to an unindexed field is not staleness
	if _, ok := d["ByLevel"]; !ok {
		t.Fatal("ByLevel changed too (id 50 added, id 5 deleted)")
	}
	if err := m.RebuildAll(mem); err != nil {
		t.Fatal(err)
	}
	if d, _ := m.Check(mem); len(d) != 0 {
		t.Fatalf("still stale: %v", d)
	}
	h, _ := m.Index("ByName").Exact("Name", box.String("changed"), 0)
	eq(t, ids(h), []int64{3})
}

func TestUnindexedFieldChangeIsNotStale(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	put(t, mem, "Player", 1, "Name", "a", "Level", 1, "Bio", "x", "Other", "1")
	m.RebuildAll(mem)
	put(t, mem, "Player", 1, "Name", "a", "Level", 1, "Bio", "x", "Other", "2")
	if d, _ := m.Check(mem); len(d) != 0 {
		t.Fatal(d)
	}
}

func TestRebuildMatchesIncremental(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	mem := link.NewMemStore()
	live := newMgr(t)
	os := NewObservedStore(mem, live)
	for i := 0; i < 400; i++ {
		id := int64(rng.Intn(60) + 1)
		if rng.Intn(4) == 0 {
			os.Delete(Addr{File: pf, Box: "Player", ID: id, HasID: true})
			continue
		}
		put(t, os, "Player", id, "Name", fmt.Sprintf("n%d", rng.Intn(10)), "Level", rng.Intn(20), "Bio", fmt.Sprintf("w%d w%d", rng.Intn(5), rng.Intn(5)))
	}
	fresh := newMgr(t)
	if err := fresh.RebuildAll(mem); err != nil {
		t.Fatal(err)
	}
	for _, n := range live.Names() {
		a, b := live.Index(n).Stats(), fresh.Index(n).Stats()
		if a.Digest != b.Digest || a.Records != b.Records || a.Terms != b.Terms {
			t.Fatalf("%s: incremental %+v rebuilt %+v", n, a, b)
		}
	}
	for lv := 0; lv < 20; lv++ {
		a, _ := live.Index("ByLevel").Range("Level", Incl(float64(lv)), Incl(float64(lv)+2), 0)
		b, _ := fresh.Index("ByLevel").Range("Level", Incl(float64(lv)), Incl(float64(lv)+2), 0)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("range %d differs", lv)
		}
	}
}

// ---- brute force comparison ----------------------------------------------

func TestAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	mem := link.NewMemStore()
	m, err := ParseManager(
		decl("E", "exact", `"S"`, "") + decl("P", "prefix", `"S"`, "") +
			decl("R", "range", `"N"`, "") + decl("T", "text", `"S"`, ""))
	if err != nil {
		t.Fatal(err)
	}
	os := NewObservedStore(mem, m)
	words := []string{"ant", "bee", "cat", "dog", "eel", "fox", "gnu"}
	type row struct {
		s string
		n float64
	}
	truth := map[int64]row{}
	for i := 0; i < 600; i++ {
		id := int64(rng.Intn(100) + 1)
		if rng.Intn(5) == 0 {
			os.Delete(Addr{File: pf, Box: "Player", ID: id, HasID: true})
			delete(truth, id)
			continue
		}
		r := row{s: words[rng.Intn(7)] + " " + words[rng.Intn(7)], n: float64(rng.Intn(50)) / 2}
		truth[id] = r
		put(t, os, "Player", id, "S", r.s, "N", r.n)
	}
	sortedIDs := func(f func(row) bool) []int64 {
		out := []int64{}
		for id, r := range truth {
			if f(r) {
				out = append(out, id)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	for _, r := range []row{{"ant bee", 0}, {"cat cat", 0}, {"zzz", 0}} {
		h, _ := m.Index("E").Exact("S", box.String(r.s), 0)
		eq(t, ids(h), sortedIDs(func(x row) bool { return x.s == r.s }))
	}
	for _, p := range []string{"", "a", "ant b", "dog ", "q"} {
		h, _ := m.Index("P").Prefix("S", p, 0)
		got := ids(h)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		eq(t, got, sortedIDs(func(x row) bool { return strings.HasPrefix(x.s, p) }))
	}
	for i := 0; i < 30; i++ {
		lo, hi := float64(rng.Intn(25))/2, float64(rng.Intn(25))/2
		loX, hiX := rng.Intn(2) == 0, rng.Intn(2) == 0
		lb, hb := &Bound{lo, loX}, &Bound{hi, hiX}
		h, _ := m.Index("R").Range("N", lb, hb, 0)
		// ordering by value then id
		for j := 1; j < len(h); j++ {
			a, b := h[j-1], h[j]
			if a.Value.Num > b.Value.Num || (a.Value.Num == b.Value.Num && a.Addr.ID > b.Addr.ID) {
				t.Fatalf("range not ordered: %v", h)
			}
		}
		got := ids(h)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		eq(t, got, sortedIDs(func(x row) bool {
			return (x.n > lo || (x.n == lo && !loX)) && (x.n < hi || (x.n == hi && !hiX))
		}))
	}
	for i := 0; i < 20; i++ {
		a, b := words[rng.Intn(7)], words[rng.Intn(7)]
		has := func(x row, w string) bool {
			for _, t := range strings.Fields(x.s) {
				if t == w {
					return true
				}
			}
			return false
		}
		h, _ := m.Index("T").Text("S", And, []string{a, b}, 0)
		eq(t, ids(h), sortedIDs(func(x row) bool { return has(x, a) && has(x, b) }))
		h, _ = m.Index("T").Text("S", Or, []string{a, b}, 0)
		eq(t, ids(h), sortedIDs(func(x row) bool { return has(x, a) || has(x, b) }))
	}
}

// ---- snapshots, crashes ---------------------------------------------------

func TestSnapshotRoundTripAndVerify(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	os := NewObservedStore(mem, m)
	fill(t, os, 30)
	blob := NewMemBlob()
	if err := m.Save(blob, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	want := map[string]Stats{}
	for _, n := range m.Names() {
		want[n] = m.Index(n).Stats()
	}

	m2 := newMgr(t)
	res, err := m2.Load(blob, mem, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != LoadedVerified {
			t.Fatalf("%+v", r)
		}
		g := m2.Index(r.Name).Stats()
		if g != want[r.Name] {
			t.Fatalf("%s: %+v vs %+v", r.Name, g, want[r.Name])
		}
	}
	h, _ := m2.Index("ByBio").Text("Bio", And, []string{"cats", "number"}, 0)
	if len(h) != 15 {
		t.Fatalf("%d", len(h))
	}
}

func TestSnapshotStaleIsRebuilt(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	os := NewObservedStore(mem, m)
	fill(t, os, 10)
	blob := NewMemBlob()
	m.Save(blob, SaveOptions{})
	// source changes after the snapshot (e.g. crash before the next save)
	put(t, mem, "Player", 3, "Name", "after", "Level", 3, "Bio", "x")

	m2 := newMgr(t)
	res, err := m2.Load(blob, mem, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]LoadResult{}
	for _, r := range res {
		got[r.Name] = r
	}
	if got["ByName"].Status != Rebuilt || got["ByName"].Reason != "stale" || got["ByBio"].Reason != "stale" {
		t.Fatalf("%+v", got)
	}
	if got["ByLevel"].Status != LoadedVerified { // Level of id 3 is unchanged
		t.Fatalf("ByLevel should still be fresh: %+v", got["ByLevel"])
	}
	if h, _ := m2.Index("ByName").Exact("Name", box.String("after"), 0); len(h) != 1 {
		t.Fatal("rebuilt index does not see the change")
	}
	if d, _ := m2.Check(mem); len(d) != 0 {
		t.Fatalf("%v", d)
	}
}

func TestSnapshotSourceSeqTrust(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	os := NewObservedStore(mem, m)
	fill(t, os, 5)
	blob := NewMemBlob()
	os.Locked(func() { m.Save(blob, SaveOptions{SourceSeq: 77, HasSourceSeq: true}) })

	// matching seq: trusted, no scan at all
	m2 := newMgr(t)
	res, err := m2.Load(blob, panicScanner{}, LoadOptions{SourceSeq: 77, HasSourceSeq: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != LoadedTrusted {
			t.Fatalf("%+v", r)
		}
	}
	if m2.Index("ByName").Stats().Records != 5 {
		t.Fatal("not loaded")
	}
	// different seq: falls back to a scan
	put(t, mem, "Player", 1, "Name", "z", "Level", 1, "Bio", "q")
	m3 := newMgr(t)
	res, _ = m3.Load(blob, mem, LoadOptions{SourceSeq: 78, HasSourceSeq: true})
	if res[0].Status != Rebuilt || res[0].Reason != "stale" {
		t.Fatalf("%+v", res[0])
	}
	// snapshot without a recorded seq is never trusted
	blob2 := NewMemBlob()
	m3.Save(blob2, SaveOptions{})
	m4 := newMgr(t)
	res, _ = m4.Load(blob2, mem, LoadOptions{SourceSeq: 78, HasSourceSeq: true})
	if res[0].Status != LoadedVerified {
		t.Fatalf("%+v", res[0])
	}
}

type panicScanner struct{}

func (panicScanner) Scan(string, func(Addr, *box.Entry) bool) error {
	panic("source must not be scanned")
}

func TestSnapshotMissingCorruptTruncatedDeclChanged(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	os := NewObservedStore(mem, m)
	fill(t, os, 12)
	blob := NewMemBlob()
	m.Save(blob, SaveOptions{})
	good, _ := blob.Load("ByName")

	check := func(name string, mutate func([]byte) []byte, wantPrefix string) {
		t.Helper()
		b := NewMemBlob()
		m.Save(b, SaveOptions{})
		if mutate != nil {
			d, _ := b.Load("ByName")
			b.Save("ByName", mutate(d))
		} else {
			delete(b.m, "ByName")
		}
		m2 := newMgr(t)
		res, err := m2.Load(b, mem, LoadOptions{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r := res[0]
		if r.Name != "ByName" || r.Status != Rebuilt || !strings.HasPrefix(r.Reason, wantPrefix) {
			t.Fatalf("%s: %+v", name, r)
		}
		// the rebuilt index is correct
		h, _ := m2.Index("ByName").Exact("Name", box.String("p5"), 0)
		eq(t, ids(h), []int64{5})
		if d, _ := m2.Check(mem); len(d) != 0 {
			t.Fatalf("%s: %v", name, d)
		}
	}
	check("missing", nil, "missing")
	check("truncated half", func(d []byte) []byte { return d[:len(d)/2] }, "corrupt")
	check("truncated one byte", func(d []byte) []byte { return d[:len(d)-1] }, "corrupt")
	check("truncated to zero", func(d []byte) []byte { return nil }, "corrupt")
	check("flipped byte", func(d []byte) []byte {
		d = append([]byte(nil), d...)
		d[len(d)/3] ^= 0x01
		return d
	}, "corrupt")
	check("garbage", func(d []byte) []byte { return []byte("hello world") }, "corrupt")
	check("appended junk", func(d []byte) []byte { return append(append([]byte(nil), d...), "junk"...) }, "corrupt")
	_ = good

	// a snapshot written by a different declaration is not reused
	d2, _ := ParseManager(decl("ByName", "prefix", `"Name"`, "")) // same name, other kind
	res, err := d2.Load(blob, mem, LoadOptions{})
	if err != nil || res[0].Status != Rebuilt || res[0].Reason != "declaration changed" {
		t.Fatalf("%+v %v", res, err)
	}
}

// A validly sealed snapshot whose contents contradict its own count/digest
// (or other index's data) is still rejected.
func TestSnapshotInconsistentContents(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	fill(t, NewObservedStore(mem, m), 4)
	b := NewMemBlob()
	m.Save(b, SaveOptions{})
	d, _ := b.Load("ByName")
	body, err := unseal(d)
	if err != nil {
		t.Fatal(err)
	}
	// drop one record row but re-seal: count/digest no longer match
	lines := strings.Split(body, "\n")
	var kept []string
	dropped := false
	for _, l := range lines {
		if !dropped && strings.Contains(l, "{Rec[ID=2]}") {
			dropped = true
			continue
		}
		kept = append(kept, l)
	}
	if !dropped {
		t.Fatalf("test needs to find a record row in:\n%s", body)
	}
	b.Save("ByName", seal(strings.Join(kept, "\n")))
	res, err := newMgr(t).Load(b, mem, LoadOptions{})
	if err != nil || res[0].Status != Rebuilt || !strings.HasPrefix(res[0].Reason, "corrupt") {
		t.Fatalf("%+v %v", res[0], err)
	}
}

func TestDirBlobAtomicAndCrash(t *testing.T) {
	dir := t.TempDir()
	mem := link.NewMemStore()
	m := newMgr(t)
	fill(t, NewObservedStore(mem, m), 15)
	db := DirBlob{Dir: filepath.Join(dir, "snaps")}
	if err := m.Save(db, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	// a crash during a later Save leaves a partial temp file; it must not matter
	if err := os.WriteFile(filepath.Join(db.Dir, ".tmp-crashed"), []byte("{Index[] = [ half a snap"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := newMgr(t).Load(db, mem, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != LoadedVerified {
			t.Fatalf("%+v", r)
		}
	}
	// truncate the real file on disk (torn write that bypassed the rename)
	var snap string
	ents, _ := os.ReadDir(db.Dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "ByName") {
			snap = filepath.Join(db.Dir, e.Name())
		}
	}
	st, err := os.Stat(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(snap, st.Size()*2/3); err != nil {
		t.Fatal(err)
	}
	m2 := newMgr(t)
	res, err = m2.Load(db, mem, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != Rebuilt || !strings.HasPrefix(res[0].Reason, "corrupt") {
		t.Fatalf("%+v", res[0])
	}
	if d, _ := m2.Check(mem); len(d) != 0 {
		t.Fatal(d)
	}
	// saving again repairs the file
	if err := m2.Save(db, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	res, _ = newMgr(t).Load(db, mem, LoadOptions{})
	if res[0].Status != LoadedVerified {
		t.Fatalf("%+v", res[0])
	}
	// hostile names stay inside the directory
	for _, n := range []string{"../../etc/passwd", `..\x`, "a/b", ""} {
		fn := db.fileName(n)
		if strings.ContainsAny(fn, `/\`) || strings.Contains(fn, "..") {
			t.Fatalf("unsafe file name %q", fn)
		}
	}
}

func TestSnapshotAwkwardStrings(t *testing.T) {
	ix := mustIdx(t, decl("P", "exact", `;["A", "B"]:`, ""))
	vals := []string{"plain", "q\"uote", "back\\slash", "new\nline", "tab\t", "nul\x00byte", "unicode é 日本 \u2028", "", "{Seal[]} = \"", ";]:"}
	for i, v := range vals {
		a, e := rec("Player", int64(i+1), "A", v, "B", v+"!")
		ix.OnPut(a, e)
	}
	mem := link.NewMemStore()
	// the source holds the same data so verification passes
	for i, v := range vals {
		put(t, mem, "Player", int64(i+1), "A", v, "B", v+"!")
	}
	m := &Manager{byName: map[string]*Index{ix.Name(): ix}, indexes: []*Index{ix}}
	b := NewMemBlob()
	m.Save(b, SaveOptions{})
	ix2 := mustIdx(t, decl("P", "exact", `;["A", "B"]:`, ""))
	m2 := &Manager{byName: map[string]*Index{ix2.Name(): ix2}, indexes: []*Index{ix2}}
	res, err := m2.Load(b, mem, LoadOptions{})
	if err != nil || res[0].Status != LoadedVerified {
		t.Fatalf("%+v %v", res, err)
	}
	for i, v := range vals {
		h, _ := ix2.Exact("A", box.String(v), 0)
		eq(t, ids(h), []int64{int64(i + 1)})
	}
}

// ---- concurrency ----------------------------------------------------------

func TestConcurrentPutAndQuery(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	os := NewObservedStore(mem, m)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 400; i++ {
				id := int64(w*1000 + rng.Intn(50))
				if rng.Intn(5) == 0 {
					os.Delete(Addr{File: pf, Box: "Player", ID: id, HasID: true})
				} else {
					put(t, os, "Player", id, "Name", fmt.Sprintf("n%d", rng.Intn(8)), "Level", rng.Intn(10), "Bio", fmt.Sprintf("a%d b%d", rng.Intn(4), rng.Intn(4)))
				}
			}
		}(w)
	}
	var qwg sync.WaitGroup
	for q := 0; q < 4; q++ {
		qwg.Add(1)
		go func(q int) {
			defer qwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				h, err := m.Index("ByName").Exact("Name", box.String("n3"), 0)
				if err != nil {
					t.Error(err)
					return
				}
				for i := 1; i < len(h); i++ {
					if !lessAddr(h[i-1].Addr, h[i].Addr) {
						t.Errorf("unordered result")
						return
					}
				}
				if _, err := m.Index("ByLevel").Range("Level", Incl(2), Incl(5), 0); err != nil {
					t.Error(err)
					return
				}
				if _, err := m.Index("ByBio").Text("Bio", Or, []string{"a1", "b2"}, 0); err != nil {
					t.Error(err)
					return
				}
				m.Index("ByName").Stats()
			}
		}(q)
	}
	wg.Wait()
	close(stop)
	qwg.Wait()
	if d, err := m.Check(mem); err != nil || len(d) != 0 {
		t.Fatalf("index diverged under concurrency: %v %v", d, err)
	}
}

func TestConcurrentWritesDuringRebuildAreNotLost(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	os := NewObservedStore(mem, m)
	fill(t, os, 200)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewSource(9))
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			id := int64(rng.Intn(300) + 1)
			if i%4 == 0 {
				os.Delete(Addr{File: pf, Box: "Player", ID: id, HasID: true})
			} else {
				put(t, os, "Player", id, "Name", fmt.Sprintf("n%d", rng.Intn(50)), "Level", rng.Intn(10), "Bio", "x")
			}
		}
	}()
	for i := 0; i < 15; i++ {
		if err := m.RebuildAll(mem); err != nil {
			t.Fatal(err)
		}
		m.Index("ByName").Exact("Name", box.String("n1"), 0)
	}
	// two concurrent rebuilds of one index: the second is refused, not corrupting
	ix := m.Index("ByName")
	var rw sync.WaitGroup
	var refused, done int32
	var mu sync.Mutex
	for i := 0; i < 4; i++ {
		rw.Add(1)
		go func() {
			defer rw.Done()
			err := ix.Rebuild(mem)
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, ErrRebuilding) {
				refused++
			} else if err == nil {
				done++
			} else {
				t.Error(err)
			}
		}()
	}
	rw.Wait()
	close(stop)
	wg.Wait()
	if done == 0 {
		t.Fatal("no rebuild succeeded")
	}
	if d, err := m.Check(mem); err != nil || len(d) != 0 {
		t.Fatalf("lost writes: %v %v", d, err)
	}
}

func TestRebuildScanErrorKeepsOldIndex(t *testing.T) {
	mem := link.NewMemStore()
	m := newMgr(t)
	fill(t, NewObservedStore(mem, m), 5)
	ix := m.Index("ByName")
	if err := ix.Rebuild(errScanner{}); err == nil {
		t.Fatal("expected error")
	}
	if ix.Stats().Records != 5 {
		t.Fatal("old index lost")
	}
	// and a later rebuild still works (rebuilding flag was cleared)
	if err := ix.Rebuild(mem); err != nil {
		t.Fatal(err)
	}
}

type errScanner struct{}

func (errScanner) Scan(string, func(Addr, *box.Entry) bool) error { return errors.New("io error") }

func BenchmarkPutExact(b *testing.B) {
	ix := mustIdx(b, decl("E", "exact", `"Name"`, ""))
	for i := 0; i < b.N; i++ {
		a, e := rec("Player", int64(i%10000), "Name", fmt.Sprintf("n%d", i%500))
		ix.OnPut(a, e)
	}
}
