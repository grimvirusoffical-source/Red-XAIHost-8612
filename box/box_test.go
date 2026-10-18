package box

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

const sample = `
<< RedX sample >>
{AccountVault[ID=1]= [
    {Name[]} = "Josh \"J\"\n",
    {Level[]} = 42.5,
    {Neg[]} = -7,
    {Alive[]} = TRUE,
    {Gone[]} = NELL,
    {Tags[]} = ;[1, 2, ;["x", FALSE]:]:,
    {Inventory[id=10]} = [
        {Sword[]} = "steel",
        {Count[ID=3]} = 5
    ],
    {Inventory[id=11]} = [ ],
    {Guild[]} = "accounts.red-xai.db":AccountVault[ID=1] @> "guilds.red-xai.db":GuildBox[ID=4],
    {Cleanup[]} = "a":B[ID=1].Key @!> "s":Q[ID=1],
    {Ref[]} = "a":B[ID=1],
    {Tagged[LOCK=3, ON=PlayerStatsBox.Health, Note="hi there"]} = 1
]True, MODE = db, Owner = "me", Version = 3};

{Sess[ID=2]= [
    {Otp[]} = "123456" (30),
    {Seen[]} = TRUE (~60),
    {Hot[]} = 12345 (5)*,
    {Sub[]} = [ {A[]} = 1 ] (300)
] (3600) False};

{UserGuildLink[1]} = "accounts.red-xai.db":AccountVault[ID=1] @> "guilds.red-xai.db":GuildBox[ID=4];
{OnPlayerDeath[ON=PlayerStatsBox.Health]} = [
    {When[]} = "<= 0",
    {SetTarget[]} = "PlayerStatsBox.IsDead = TRUE"
];
`

func TestParseRoundTrip(t *testing.T) {
	doc, err := ParseDocument(sample)
	if err != nil {
		t.Fatal(err)
	}
	out := Serialize(doc)
	doc2, err := ParseDocument(out)
	if err != nil {
		t.Fatalf("reparse failed: %v\n%s", err, out)
	}
	if !doc.Box().Equal(doc2.Box()) {
		t.Fatalf("round trip mismatch:\n%s", out)
	}
	if out2 := Serialize(doc2); out2 != out {
		t.Fatalf("serialization not stable:\n%s\n---\n%s", out, out2)
	}
	// compact form also round-trips
	c := Format(doc, FormatOptions{Compact: true})
	doc3, err := ParseDocument(c)
	if err != nil || !doc.Box().Equal(doc3.Box()) {
		t.Fatalf("compact round trip failed: %v", err)
	}
	// spot checks
	e, err := Resolve(doc, mustSegs(t, "AccountVault[ID=1].Inventory[id=10].Count[ID=3]"))
	if err != nil || e.Value.Num != 5 {
		t.Fatalf("resolve: %v %v", e, err)
	}
	e, _ = Resolve(doc, mustSegs(t, "AccountVault[ID=1].Name"))
	if e.Value.Str != "Josh \"J\"\n" {
		t.Fatalf("string escapes: %q", e.Value.Str)
	}
	e, _ = Resolve(doc, mustSegs(t, "Sess[ID=2].Hot"))
	if e.TTL.Kind != TTLCheckpoint || e.TTL.Seconds != 5 {
		t.Fatalf("ckpt marker: kind=%d secs=%d", e.TTL.Kind, e.TTL.Seconds)
	}
	e, _ = Resolve(doc, mustSegs(t, "Sess[ID=2].Seen"))
	if e.TTL.Kind != TTLSliding || e.TTL.Seconds != 60 {
		t.Fatalf("sliding marker: kind=%d secs=%d", e.TTL.Kind, e.TTL.Seconds)
	}
	top, _ := Resolve(doc, mustSegs(t, "AccountVault[ID=1]"))
	if !top.Value.Box.HasFlag || !top.Value.Box.Flag || len(top.Value.Box.Meta) != 3 {
		t.Fatalf("flag/meta lost: %+v", top.Value.Box)
	}
	e, _ = Resolve(doc, mustSegs(t, "UserGuildLink[1]"))
	if e.Value.Kind != KindLink || e.Value.Link.Op != "@>" || e.Value.Link.To.File != "guilds.red-xai.db" {
		t.Fatalf("link: %+v", e.Value)
	}
}

// Box() helper only for tests: the document is the root box.
func (b *Box) Box() *Box { return b }

func mustSegs(t *testing.T, s string) []Seg {
	t.Helper()
	p, err := ParsePath(s)
	if err != nil {
		t.Fatal(err)
	}
	return p.Segs
}

func TestRandomRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var gen func(depth int) Value
	str := func() string {
		n := rng.Intn(12)
		rs := make([]rune, n)
		pool := []rune("abc XYZ\"\\\n\t{}[]();:,<>=@~*é世😀\x01")
		for i := range rs {
			rs[i] = pool[rng.Intn(len(pool))]
		}
		return string(rs)
	}
	gen = func(depth int) Value {
		switch k := rng.Intn(7); {
		case k == 0:
			return Nell()
		case k == 1:
			return String(str())
		case k == 2:
			return Number(float64(rng.Intn(2000)-1000) / float64(1+rng.Intn(8)))
		case k == 3:
			return Bool(rng.Intn(2) == 0)
		case k == 4 && depth < 3:
			n := rng.Intn(4)
			a := []Value{}
			for i := 0; i < n; i++ {
				a = append(a, gen(depth+1))
			}
			return Array(a...)
		case depth < 3:
			b := NewBox()
			n := rng.Intn(4)
			for i := 0; i < n; i++ {
				e := &Entry{Key: string(rune('A'+i)) + "k", Value: gen(depth + 1)}
				if rng.Intn(2) == 0 {
					e.Attrs = []Attr{{"ID", Int(int64(rng.Intn(5)))}}
				}
				if rng.Intn(3) == 0 {
					e.TTL = TTL{Kind: TTLFixed + uint8(rng.Intn(3)), Seconds: rng.Intn(100)}
				}
				b.Put(e)
			}
			return BoxValue(b)
		}
		return Int(1)
	}
	for i := 0; i < 500; i++ {
		root := NewBox()
		for j := 0; j < 1+rng.Intn(3); j++ {
			root.Put(&Entry{Key: "Top" + string(rune('A'+j)), Attrs: []Attr{{"ID", Int(int64(j))}}, Value: BoxValue(NewBox().withEntry(gen(0)))})
		}
		txt := Serialize(root)
		back, err := ParseDocument(txt)
		if err != nil {
			t.Fatalf("iter %d: %v\n%s", i, err, txt)
		}
		if !root.Equal(back) {
			t.Fatalf("iter %d mismatch\n%s", i, txt)
		}
		// binary round trip
		bin := AppendValue(nil, BoxValue(root))
		v, rest, err := DecodeValue(bin)
		if err != nil || len(rest) != 0 || !v.Box.Equal(root) {
			t.Fatalf("iter %d binary mismatch: %v", i, err)
		}
	}
}

func (b *Box) withEntry(v Value) *Box { b.Put(&Entry{Key: "V", Value: v}); return b }

func TestErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{"{A[ID=1]= [ {K[]} = 1 ", "line 1"},
		{"{A[ID=1]= [\n {K[]} = ,\n]};", "line 2, col 10: expected a value"},
		{"{A[]= [\n {K[]} = 1\n {L[]} = 2 ]};", "line 3, col 2: expected ',' or ']' after entry K"},
		{"{A[]= [ {K[]} = \"x ]};", "unterminated string"},
		{"{A[]= [ {K[]} = hello ]};", "unexpected identifier \"hello\""},
		{"{A[]= [ {K[]} = 1 (~-3) ]};", "whole number"},
		{"{A[]= [ {K[]} = 1 (2.5) ]};", "whole number"},
		{"{A[]= [ {K[]} = 1 (~4)* ]};", "cannot be sliding"},
		{"{A[]= [ {K[]} = 1, {K[]} = 2 ]};", "duplicate key K"},
		{"<< never closed {A[]} = 1", "unterminated comment"},
		{"{A[]= [ {K[]} = ;[1, 2] ]};", "arrays end with"},
		{"{A[]= [ {K[]} = 1 ]} #", "unexpected character '#'"},
		{"{A[]= [ {K[]} = 1 ]True, M 3};", "expected '='"},
		{"A[]", "expected '{'"},
		{"{A[]} 4", "expected '='"},
		{"{A[]= [ {K[]} = \"x\\q\" ]};", "invalid escape"},
	}
	for _, c := range cases {
		_, err := ParseDocument(c.src)
		if err == nil {
			t.Errorf("%q: expected error", c.src)
			continue
		}
		if _, ok := err.(*Error); !ok {
			t.Errorf("%q: error is %T, want *Error", c.src, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %q, want substring %q", c.src, err.Error(), c.want)
		}
	}
	_, err := ParseDocument("{A[]= [\n  {K[]} = ,\n]};")
	if ctx := err.(*Error).Context("{A[]= [\n  {K[]} = ,\n]};"); !strings.Contains(ctx, "^") {
		t.Fatalf("context: %s", ctx)
	}
	// depth bomb must not crash
	deep := strings.Repeat(";[", 5000)
	if _, err := ParseValue(deep); err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Fatalf("depth: %v", err)
	}
}

func TestPaths(t *testing.T) {
	good := map[string]string{
		"accounts.red-xai.db:AccountVault[ID=1].Inventory[id=10]": "accounts.red-xai.db:AccountVault[ID=1].Inventory[ID=10]",
		"HotWorldStore.GlobalKillCounter":                         "HotWorldStore.GlobalKillCounter",
		"A[3].B":                                                  "A[ID=3].B",
		"A[ID=*].*":                                               "A[ID=*].*",
		`"my file.red-xai.db":A`:                                  "my file.red-xai.db:A",
	}
	for in, want := range good {
		p, err := ParsePath(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if p.String() != want {
			t.Errorf("%s -> %s want %s", in, p, want)
		}
	}
	for _, bad := range []string{"", "a.", ".a", "a[", "a[x=1]", "a[ID=z]", "a b", "f:"} {
		if _, err := ParsePath(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	doc, _ := ParseDocument(sample)
	p, _ := ParsePath("AccountVault[ID=1].Inventory[ID=*].*")
	if got := Match(doc, p.Segs); len(got) != 2 { // Inventory[10] has 2 children, [11] none -> Sword, Count
		t.Fatalf("match got %d", len(got))
	}
	p, _ = ParsePath("AccountVault[ID=1].Inventory")
	if e, err := Resolve(doc, p.Segs); err != nil || e.ek.ID != 10 { // no ID -> oldest
		t.Fatalf("oldest-id fallback: %v %v", e, err)
	}
	p, _ = ParsePath("AccountVault[ID=1].Name.x")
	if _, err := Resolve(doc, p.Segs); err == nil {
		t.Fatal("expected not found through scalar")
	}
}

func TestValidate(t *testing.T) {
	qs, _ := ParseDocument(`{S[ID=1]= [ {A[]} = 1 (5), {B[]} = [ {C[]} = 2 ] (10), {D[]} = 3 ]};`)
	errs := Validate(qs, ModeQS)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "S.D") {
		t.Fatalf("qs: %v", errs)
	}
	ls, _ := ParseDocument(`{S[ID=1]= [ {A[]} = 1 (5)*, {B[]} = 2 (9) ]};`)
	if errs := Validate(ls, ModeQSLS); len(errs) != 0 {
		t.Fatalf("qs-ls: %v", errs)
	}
	if errs := Validate(ls, ModeQS); len(errs) != 2 { // (5)* not allowed in qs, and carries no expiry
		t.Fatalf("qs on ls doc: %v", errs)
	}
	if errs := Validate(ls, ModeCold); len(errs) != 2 {
		t.Fatalf("db: %v", errs)
	}
}

func TestJSON(t *testing.T) {
	doc, _ := ParseDocument(sample)
	e, _ := Resolve(doc, mustSegs(t, "AccountVault[ID=1]"))
	bs, err := json.Marshal(e.Value)
	if err != nil {
		t.Fatal(err)
	}
	var back Value
	if err := json.Unmarshal(bs, &back); err != nil {
		t.Fatalf("%v\n%s", err, bs)
	}
	bs2, _ := json.Marshal(back)
	if string(bs) != string(bs2) && len(bs) != len(bs2) {
		t.Fatalf("json unstable:\n%s\n%s", bs, bs2)
	}
	if !strings.Contains(string(bs), `"Inventory[ID=10]":{"Sword":"steel"`) {
		t.Fatalf("unexpected json: %s", bs)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(sample)
	f.Add("{A[]= [ {K[]} = 1 (5)* ]True};")
	f.Fuzz(func(t *testing.T, s string) {
		doc, err := ParseDocument(s)
		if err != nil {
			return
		}
		out := Serialize(doc)
		d2, err := ParseDocument(out)
		if err != nil {
			t.Fatalf("serialized output does not reparse: %v\n%s", err, out)
		}
		if !doc.Equal(d2) {
			t.Fatalf("mismatch\n%s", out)
		}
	})
}
