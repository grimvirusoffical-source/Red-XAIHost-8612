package query

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"redx/box"
	"redx/engine/idx"
	"redx/engine/link"
	"redx/engine/sec"
)

const playersFile = "players.red-xai.db"
const guildsFile = "guilds.red-xai.db"

// countStore wraps a link.Store and counts storage calls, so tests can prove
// what the planner did.
type countStore struct {
	link.Store
	scans, gets, puts, dels atomic.Int64
}

func (c *countStore) Scan(f string, fn func(link.Addr, *box.Entry) bool) error {
	c.scans.Add(1)
	return c.Store.Scan(f, fn)
}
func (c *countStore) Get(a link.Addr) (*box.Entry, bool, error) {
	c.gets.Add(1)
	return c.Store.Get(a)
}
func (c *countStore) Put(a link.Addr, e *box.Entry) error {
	c.puts.Add(1)
	return c.Store.Put(a, e)
}
func (c *countStore) Delete(a link.Addr) (bool, error) {
	c.dels.Add(1)
	return c.Store.Delete(a)
}

const idxDecls = `
{ByName[]}  = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Name"]:, {Kind[]} = "exact" ];
{ByClass[]} = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Class"]:, {Kind[]} = "exact", {Fold[]} = TRUE ];
{ByNamePfx[]} = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Name"]:, {Kind[]} = "prefix" ];
{ByLevel[]} = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Level", "Stats.Gold"]:, {Kind[]} = "range" ];
{ByBio[]}   = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Bio"]:, {Kind[]} = "text" ];
`

const playerDoc = `
{Player[1]} = [ {Name[]} = "ann",   {Level[]} = 5,  {Class[]} = "Mage",    {Bio[]} = "red dragon slayer", {Tags[]} = ;["a","b"]:, {Stats[]} = [ {Gold[]} = 100, {Secret[]} = "s1" ], {Guild[]} = "players.red-xai.db":Player[ID=1].Guild @> "guilds.red-xai.db":Guild[ID=10] ];
{Player[2]} = [ {Name[]} = "bob",   {Level[]} = 12, {Class[]} = "warrior", {Bio[]} = "blue knight",       {Stats[]} = [ {Gold[]} = 5,   {Secret[]} = "s2" ], {Guild[]} = "players.red-xai.db":Player[ID=2].Guild @> "guilds.red-xai.db":Guild[ID=10] ];
{Player[3]} = [ {Name[]} = "bobby", {Level[]} = 7,  {Class[]} = "MAGE",    {Bio[]} = "red knight of the dragon", {Stats[]} = [ {Gold[]} = 250, {Secret[]} = "s3" ] ];
{Player[4]} = [ {Name[]} = "cy",    {Level[]} = 7,  {Stats[]} = [ {Gold[]} = 0 ], {Guild[]} = "players.red-xai.db":Player[ID=4].Guild @> "guilds.red-xai.db":Guild[ID=99] ];
{Player[5]} = [ {Name[]} = "dee",   {Class[]} = "healer", {Level[]} = "seven", {Guild[]} = "players.red-xai.db":Player[ID=5].Guild @> "guilds.red-xai.db":Guild[ID=10] ];
{Npc[1]}    = [ {Name[]} = "ann", {Level[]} = 50 ];
`

const guildDoc = `
{Guild[10]} = [ {Title[]} = "Red Order", {Owner[]} = "players.red-xai.db":Guild[ID=10].Owner @> "players.red-xai.db":Player[ID=2], {Rank[]} = 3 ];
`

type fixture struct {
	mem   *link.MemStore
	cs    *countStore
	store link.Store // what the query DB uses (observed when indexed)
	mgr   *idx.Manager
	links *link.Engine
	db    *DB
}

type fixOpt struct {
	indexes bool
	links   bool
	auth    Authorizer
	lim     Limits
	owner   func(link.Addr, *box.Entry) string
	noDocs  bool
}

func newFixture(t testing.TB, o fixOpt) *fixture {
	t.Helper()
	f := &fixture{mem: link.NewMemStore()}
	if !o.noDocs {
		must(t, link.PutDoc(f.mem, playersFile, playerDoc))
		must(t, link.PutDoc(f.mem, guildsFile, guildDoc))
	}
	f.cs = &countStore{Store: f.mem}
	f.store = f.cs
	cfg := Config{Auth: o.auth, Limits: o.lim, OwnerOf: o.owner}
	if cfg.Auth == nil {
		cfg.Auth = AllowAll{}
	}
	if o.indexes {
		m, err := idx.ParseManager(idxDecls)
		must(t, err)
		must(t, m.RebuildAll(f.cs))
		f.mgr = m
		f.store = idx.NewObservedStore(f.cs, m)
		cfg.Indexes = m
	}
	if o.links {
		f.links = link.New(f.store, link.Options{})
		cfg.Links = f.links
	}
	cfg.Store = f.store
	db, err := New(cfg)
	must(t, err)
	f.db = db
	return f
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var anyone = sec.Principal{ID: "tester"}

func (f *fixture) q(t testing.TB, query string, args ...interface{}) *Result {
	t.Helper()
	res, err := f.db.Exec(context.Background(), anyone, query, args...)
	if err != nil {
		t.Fatalf("%s\n  error: %v", query, err)
	}
	return res
}

func (f *fixture) qerr(t testing.TB, query string, args ...interface{}) error {
	t.Helper()
	_, err := f.db.Exec(context.Background(), anyone, query, args...)
	if err == nil {
		t.Fatalf("%s\n  expected an error", query)
	}
	return err
}

// col returns the values of one column rendered as short strings.
func col(res *Result, name string) []string {
	ci := -1
	for i, c := range res.Columns {
		if c == name {
			ci = i
		}
	}
	var out []string
	for _, r := range res.Rows {
		if ci < 0 {
			out = append(out, "<nocol>")
			continue
		}
		out = append(out, show(r[ci]))
	}
	return out
}

func show(v box.Value) string {
	switch v.Kind {
	case box.KindNell:
		return "NELL"
	case box.KindString:
		return v.Str
	case box.KindNumber:
		return fmt.Sprint(v.Num)
	case box.KindBool:
		return fmt.Sprint(v.Bool)
	}
	return v.String()
}

func eq(t testing.TB, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got [%s], want [%s]", strings.Join(got, ","), strings.Join(want, ","))
	}
}

func mkEntry(t testing.TB, key string, id int64) *box.Entry {
	t.Helper()
	return box.NewEntry(key, &id, box.BoxValue(box.NewBox()))
}

func addrOf(file, boxName string, id int64) link.Addr {
	return link.Addr{File: file, Box: boxName, ID: id, HasID: true}
}

type linkAddr = link.Addr

func linkParse(s string) (*box.Link, error) { return link.ParseLink(s) }
