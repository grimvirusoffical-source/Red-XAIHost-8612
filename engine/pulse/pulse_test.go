package pulse

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"redx/box"
	"redx/engine/link"
)

const fPlayers = "players.red-xai.db"

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func addr(boxName string, id int64) link.Addr {
	return link.Addr{File: fPlayers, Box: boxName, ID: id, HasID: true}
}
func pa(id int64) link.Addr { return addr("Player", id) }

func rec(t testing.TB, src string) *box.Entry {
	t.Helper()
	root, err := box.ParseDocument(src)
	if err != nil {
		t.Fatalf("parse record: %v\n%s", err, src)
	}
	es := root.Entries()
	if len(es) != 1 {
		t.Fatalf("want one record")
	}
	return es[0]
}

func player(t testing.TB, id int64, health float64) *box.Entry {
	return rec(t, fmt.Sprintf(`{Player[ID=%d]= [ {Name[]} = "p%d", {Health[]} = %v, {IsDead[]} = FALSE ]};`, id, id, health))
}

func field(t testing.TB, s Store, a link.Addr, path string) box.Value {
	t.Helper()
	e, ok, err := s.Get(a)
	if err != nil || !ok {
		t.Fatalf("get %s: %v %v", a, ok, err)
	}
	p, err := box.ParsePath(path)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := lookup(e, p.Segs)
	return v
}

func num(v box.Value) float64 { return v.Num }

const deathRules = `
{PlayerDeath[ON="players.red-xai.db:Player.Health"]} = [
    {When[]}      = "<= 0",
    {SetTarget[]} = "Player.IsDead = TRUE",
    {Timer[]}     = [ {Name[]} = "Respawn", {Seconds[]} = 10,
                      {Do[]} = ;["set Health = 100", "set IsDead = FALSE", "emit player.respawned"]: ],
    {Emit[]}      = "player.died"
];`

func newEngine(t testing.TB, cfg Config, rules string) (*Engine, *link.MemStore) {
	t.Helper()
	ms := link.NewMemStore()
	e := New(ms, cfg)
	if rules != "" {
		if err := e.LoadRules(rules); err != nil {
			t.Fatalf("LoadRules: %v", err)
		}
	}
	t.Cleanup(e.Close)
	return e, ms
}

func drain(ch <-chan Event) []Event {
	var out []Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

func topics(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Topic
	}
	return out
}

// ---------------------------------------------------------------- parsing

func TestParseBlueprintExample(t *testing.T) {
	// The example form from the blueprint: ON=<Box.Field>, When, SetTarget.
	rules, err := ParseRules(`{OnPlayerDeath[ON=PlayerStatsBox.Health]} = [
	    {When[]} = "<= 0",
	    {SetTarget[]} = "PlayerStatsBox.IsDead = TRUE"
	];`)
	if err != nil {
		t.Fatal(err)
	}
	r := rules[0]
	if r.Box != "PlayerStatsBox" || r.File != "" || len(r.OnField) != 1 || r.OnField[0].Name != "Health" || r.Level {
		t.Fatalf("%+v", r)
	}
	set := r.Actions[0].(SetAction)
	if len(set.Local) != 1 || set.Local[0].Name != "IsDead" {
		t.Fatalf("box prefix not stripped: %+v", set.Local)
	}
}

func TestParseFullRule(t *testing.T) {
	rules, err := ParseRules(deathRules + `
{Poison[ON="players.red-xai.db:Player[ID=7]"]} = [ {Trigger[]} = "x", {When[]} = "Health > 1", {Emit[]} = "e" ];`)
	if err == nil {
		t.Fatal("expected error for bad Trigger")
	}
	rules, err = ParseRules(deathRules + `
{Poison[ON="players.red-xai.db:Player[ID=7]"]} = [ {Trigger[]} = "level", {When[]} = "Health > 1 and not IsDead", {Emit[]} = ;["a", "b"]:, {CancelTimer[]} = "Respawn" ];`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatal(len(rules))
	}
	d := rules[0]
	if len(d.Actions) != 3 {
		t.Fatalf("actions %d", len(d.Actions))
	}
	tm := d.Actions[1].(TimerAction)
	if tm.Name != "Respawn" || tm.Duration != 10*time.Second || len(tm.Actions) != 3 {
		t.Fatalf("%+v", tm)
	}
	p := rules[1]
	if !p.Level || !p.HasID || p.ID != 7 || len(p.Actions) != 3 {
		t.Fatalf("%+v", p)
	}
}

func TestParseRuleErrors(t *testing.T) {
	bad := map[string]string{
		"no ON":              `{R[]} = [ {When[]} = "Health <= 0", {Emit[]} = "x" ];`,
		"no When":            `{R[ON="Player.Health"]} = [ {Emit[]} = "x" ];`,
		"no actions":         `{R[ON="Player.Health"]} = [ {When[]} = "<= 0" ];`,
		"not a box":          `{R[ON="Player.Health"]} = 5;`,
		"shorthand no field": `{R[ON="Player"]} = [ {When[]} = "<= 0", {Emit[]} = "x" ];`,
		"bad expr":           `{R[ON="Player.Health"]} = [ {When[]} = "<= ", {Emit[]} = "x" ];`,
		"unknown key":        `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {Frobnicate[]} = "x" ];`,
		"bad set":            `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {SetTarget[]} = "IsDead TRUE" ];`,
		"set no target":      `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {SetTarget[]} = "5 = 6" ];`,
		"set whole rec":      `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {SetTarget[]} = "\"f\":Player[ID=1] = 5" ];`,
		"bad verb":           `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {Do[]} = "explode now" ];`,
		"timer no secs":      `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {Timer[]} = [ {Name[]} = "t", {Emit[]} = "x" ] ];`,
		"timer zero":         `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {Timer[]} = [ {Name[]} = "t", {Seconds[]} = 0, {Emit[]} = "x" ] ];`,
		"timer no name":      `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {Timer[]} = [ {Seconds[]} = 1, {Emit[]} = "x" ] ];`,
		"timer no acts":      `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {Timer[]} = [ {Name[]} = "t", {Seconds[]} = 1 ] ];`,
		"wild ON field":      `{R[ON="Player.*"]} = [ {When[]} = "<= 0", {Emit[]} = "x" ];`,
		"box syntax":         `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {Emit[]} = "x" ]` + " garbage",
		"emit spaces":        `{R[ON="Player.Health"]} = [ {When[]} = "<= 0", {Do[]} = "emit two words" ];`,
		"when wrong type":    `{R[ON="Player.Health"]} = [ {When[]} = 5, {Emit[]} = "x" ];`,
	}
	for name, src := range bad {
		if _, err := ParseRules(src); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	e, _ := newEngine(t, Config{}, deathRules)
	if err := e.LoadRules(deathRules); !errors.Is(err, ErrRuleExists) {
		t.Fatalf("duplicate rule: %v", err)
	}
	// all-or-nothing: second rule is bad, first must not be added
	err := e.LoadRules(`{Good[ON="Player.Health"]} = [ {When[]} = "<= 0", {Emit[]} = "x" ]; {Bad[ON="Player.Health"]} = 5;`)
	if err == nil || len(e.Rules()) != 1 {
		t.Fatalf("LoadRules must be atomic: %v %v", err, e.Rules())
	}
}

// ---------------------------------------------------------------- expressions

func evalCond(t *testing.T, src string, recSrc string) (bool, error) {
	t.Helper()
	c, err := parseCondition(src, "Player", nil)
	if err != nil {
		return false, err
	}
	var r *box.Entry
	if recSrc != "" {
		r = rec(t, recSrc)
	}
	v, err := c.eval(&env{rec: r})
	return truthy(v), err
}

func TestExpressions(t *testing.T) {
	r := `{Player[ID=1]= [ {Health[]} = 5, {Name[]} = "ann", {Alive[]} = TRUE, {Stats[]} = [ {Hp[]} = 7, {Tags[]} = ;["a"]: ],
	   {Inventory[id=10]} = [ {Count[]} = 3 ] ]};`
	cases := []struct {
		src  string
		want bool
	}{
		{`Health == 5`, true}, {`Health = 5`, true}, {`Health != 5`, false}, {`Health <> 4`, true},
		{`Health < 6 and Health > 4`, true}, {`Health <= 5 and Health >= 5`, true},
		{`Health > 5 or Alive`, true}, {`not Alive`, false}, {`not (Health > 9)`, true},
		{`Name == "ann"`, true}, {`Name < "bob"`, true}, {`Name > "bob"`, false},
		{`Stats.Hp == 7`, true}, {`Player.Health == 5`, true}, {`Inventory[id=10].Count == 3`, true},
		{`Missing == NELL`, true}, {`Missing == null`, true}, {`Missing < 3`, false}, {`Missing != 3`, true},
		{`Health + 2 * 3 == 11`, true}, {`(Health + 2) * 3 == 21`, true}, {`-Health == -5`, true},
		{`Health - 1 - 1 == 3`, true}, {`Health / 2 == 2.5`, true}, {`Health*2>9`, true},
		{`(Health > 1 and Health < 9) or false`, true}, {`(Health + 1) > 5`, true}, {`(Alive)`, true},
		{`Name + "!" == "ann!"`, true}, {`Alive == TRUE`, true}, {`Health == "5"`, false},
		{`Health > 1 and (Name == "x" or Alive)`, true}, {`NOT Alive OR Health==5`, true},
		{`1e1 == 10`, true}, {`Stats.Tags`, true}, {`Stats.Nope`, false},
	}
	for _, c := range cases {
		got, err := evalCond(t, c.src, r)
		if err != nil || got != c.want {
			t.Errorf("%q: got %v err %v, want %v", c.src, got, err, c.want)
		}
	}
	for _, src := range []string{`Name - 1 > 0`, `Health / 0 > 1`, `Missing + 1 > 0`, `-Name == 1`} {
		if _, err := evalCond(t, src, r); err == nil {
			t.Errorf("%q: expected evaluation error", src)
		}
	}
	for _, src := range []string{``, `Health ==`, `(Health`, `Health > > 1`, `"unterminated`, `Health # 1`, `Health 1`, `Stats.* == 1`, `and`} {
		if _, err := parseCondition(src, "Player", nil); err == nil {
			t.Errorf("%q: expected parse error", src)
		}
	}
}

// ---------------------------------------------------------------- the example

func TestDeathRespawnFlow(t *testing.T) {
	clk := NewManualClock(t0)
	e, ms := newEngine(t, Config{Clock: clk}, deathRules)
	ch, cancel, err := e.Subscribe("*", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	res, err := e.Put(pa(1), player(t, 1, 100))
	if err != nil || len(res.Fired) != 0 {
		t.Fatalf("healthy write: %+v %v", res, err)
	}
	// Health reaches 0
	res, err = e.Set(pa(1), "Health", box.Number(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fired) != 1 || res.Fired[0].Rule != "PlayerDeath" || len(res.Errors) != 0 || len(res.Blocked) != 0 {
		t.Fatalf("%+v", res)
	}
	if !field(t, ms, pa(1), "IsDead").Bool {
		t.Fatal("IsDead was not set")
	}
	tm := e.Timers()
	if len(tm) != 1 || tm[0].Name != "Respawn" || !tm[0].Due.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("timers: %+v", tm)
	}
	got := topics(drain(ch))
	want := []string{"pulse.timer.started", "player.died"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v", got, want)
	}

	// Taking more damage while dead does not re-fire the edge-triggered rule
	// and does not restart the timer.
	clk.Advance(4 * time.Second)
	res, _ = e.Set(pa(1), "Health", box.Number(-20))
	if len(res.Fired) != 0 {
		t.Fatalf("edge rule re-fired: %+v", res)
	}
	if tm := e.Timers(); len(tm) != 1 || !tm[0].Due.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("timer must keep its deadline: %+v", tm)
	}
	clk.Advance(5*time.Second + 900*time.Millisecond) // t = 9.9s
	if field(t, ms, pa(1), "Health").Num != -20 || len(e.Timers()) != 1 {
		t.Fatal("respawned too early")
	}
	drain(ch)
	clk.Advance(100 * time.Millisecond) // t = 10s
	if field(t, ms, pa(1), "Health").Num != 100 || field(t, ms, pa(1), "IsDead").Bool {
		t.Fatalf("respawn did not happen: %v", field(t, ms, pa(1), "Health"))
	}
	if len(e.Timers()) != 0 {
		t.Fatal("timer should be gone after firing")
	}
	got = topics(drain(ch))
	if strings.Join(got, ",") != "pulse.timer.fired,player.respawned" {
		t.Fatalf("events after respawn: %v", got)
	}
	// can die again: the edge re-armed because Health went back above 0
	res, _ = e.Set(pa(1), "Health", box.Number(0))
	if len(res.Fired) != 1 || len(e.Timers()) != 1 {
		t.Fatalf("second death: %+v", res)
	}
	// event payload carries a snapshot of the record
	for _, ev := range drain(ch) {
		if ev.Topic == "player.died" {
			if ev.Data == nil || ev.Addr != pa(1) || ev.Rule != "PlayerDeath" || ev.Kind != KindEmit {
				t.Fatalf("event: %+v", ev)
			}
		}
	}
}

func TestEventSeqIncreasesAndPublishChanges(t *testing.T) {
	clk := NewManualClock(t0)
	e, _ := newEngine(t, Config{Clock: clk, PublishChanges: true}, deathRules)
	ch, cancel, _ := e.Subscribe("*", 100)
	defer cancel()
	e.Put(pa(1), player(t, 1, 100))
	e.Set(pa(1), "Health", box.Number(0))
	evs := drain(ch)
	var last uint64
	changes := 0
	for _, ev := range evs {
		if ev.Seq <= last {
			t.Fatalf("seq not increasing: %v", evs)
		}
		last = ev.Seq
		if ev.Kind == KindChange {
			changes++
			if ev.Data == nil {
				t.Fatal("change without data")
			}
		}
		if ev.Time.IsZero() {
			t.Fatal("no time")
		}
	}
	// 2 caller writes + IsDead write
	if changes != 3 {
		t.Fatalf("changes=%d events=%v", changes, topics(evs))
	}
	// pattern filtering
	ch2, c2, _ := e.Subscribe("change", 10)
	defer c2()
	e.Set(pa(1), "Name", box.String("zed"))
	if evs := drain(ch2); len(evs) != 1 {
		t.Fatalf("%v", topics(evs))
	}
}

func TestPinnedAndFileFilters(t *testing.T) {
	rules := `
{OnlyP2[ON="players.red-xai.db:Player[ID=2].Health"]} = [ {When[]} = "<= 0", {Emit[]} = "p2.dead" ];
{OtherFile[ON="npcs.red-xai.db:Player.Health"]} = [ {When[]} = "<= 0", {Emit[]} = "npc.dead" ];
{AnyFile[ON=Monster.Hp]} = [ {When[]} = "<= 0", {Emit[]} = "monster.dead" ];`
	e, _ := newEngine(t, Config{}, rules)
	ch, cancel, _ := e.Subscribe("*", 100)
	defer cancel()
	e.Put(pa(1), player(t, 1, 5))
	e.Put(pa(2), player(t, 2, 5))
	e.Set(pa(1), "Health", box.Number(0))
	e.Set(pa(2), "Health", box.Number(0))
	if got := topics(drain(ch)); strings.Join(got, ",") != "p2.dead" {
		t.Fatalf("got %v", got)
	}
	m := link.Addr{File: "any.red-xai.db", Box: "Monster", ID: 1, HasID: true}
	e.Put(m, rec(t, `{Monster[ID=1]= [ {Hp[]} = 1 ]};`))
	e.Set(m, "Hp", box.Number(0))
	if got := topics(drain(ch)); strings.Join(got, ",") != "monster.dead" {
		t.Fatalf("got %v", got)
	}
	// a record created already at 0 fires too (no previous state)
	e.Put(pa(3), player(t, 3, 0))
	if got := topics(drain(ch)); len(got) != 0 {
		t.Fatalf("P3 has no pinned rule: %v", got)
	}
}

func TestEdgeVsLevel(t *testing.T) {
	rules := `
{Edge[ON="players.red-xai.db:Player.Health"]} = [ {When[]} = "< 50", {Emit[]} = "edge" ];
{Level[ON="players.red-xai.db:Player.Health"]} = [ {Trigger[]} = "level", {When[]} = "< 50", {Emit[]} = "level" ];`
	e, _ := newEngine(t, Config{}, rules)
	ch, cancel, _ := e.Subscribe("*", 100)
	defer cancel()
	e.Put(pa(1), player(t, 1, 100))
	for _, h := range []float64{40, 30, 20} {
		e.Set(pa(1), "Health", box.Number(h))
	}
	if got := strings.Join(topics(drain(ch)), ","); got != "edge,level,level,level" {
		t.Fatalf("got %s", got)
	}
	// level rules ignore writes that do not touch the watched field
	e.Set(pa(1), "Name", box.String("x"))
	if got := topics(drain(ch)); len(got) != 0 {
		t.Fatalf("level rule fired for unrelated write: %v", got)
	}
	// back above the line re-arms the edge
	e.Set(pa(1), "Health", box.Number(90))
	e.Set(pa(1), "Health", box.Number(10))
	if got := strings.Join(topics(drain(ch)), ","); got != "edge,level" {
		t.Fatalf("got %s", got)
	}
}

func TestRulesSeeTheTriggeringWrite(t *testing.T) {
	// Both rules are decided from the same write before any action runs.
	rules := `
{Revive[ON="players.red-xai.db:Player.Health"]} = [ {When[]} = "<= 0", {SetTarget[]} = "Health = 10" ];
{MarkDead[ON="players.red-xai.db:Player.Health"]} = [ {When[]} = "<= 0", {SetTarget[]} = "IsDead = TRUE" ];`
	e, ms := newEngine(t, Config{}, rules)
	e.Put(pa(1), player(t, 1, 100))
	res, _ := e.Set(pa(1), "Health", box.Number(0))
	if len(res.Fired) != 2 || res.Fired[0].Rule != "Revive" || res.Fired[1].Rule != "MarkDead" {
		t.Fatalf("%+v", res)
	}
	if field(t, ms, pa(1), "Health").Num != 10 || !field(t, ms, pa(1), "IsDead").Bool {
		t.Fatal("documented semantics: both rules fire, in definition order")
	}
}

// ---------------------------------------------------------------- loops

func TestRuleChainLoopPingPong(t *testing.T) {
	rules := `
{A[ON="players.red-xai.db:Player.X"]} = [ {When[]} = "== 1", {SetTarget[]} = "X = 2" ];
{B[ON="players.red-xai.db:Player.X"]} = [ {When[]} = "== 2", {SetTarget[]} = "X = 1" ];`
	e, ms := newEngine(t, Config{}, rules)
	ch, cancel, _ := e.Subscribe("pulse.*", 100)
	defer cancel()
	res, err := e.Put(pa(1), rec(t, `{Player[ID=1]= [ {X[]} = 1 ]};`))
	if err != nil {
		t.Fatal(err)
	}
	// Put X=1 -> A sets 2 -> B sets 1 -> A would fire again: blocked.
	if len(res.Fired) != 2 || res.Fired[0].Rule != "A" || res.Fired[1].Rule != "B" {
		t.Fatalf("fired: %+v", res.Fired)
	}
	if len(res.Blocked) != 1 || res.Blocked[0].Rule != "A" || res.Blocked[0].Reason != "loop" {
		t.Fatalf("blocked: %+v", res.Blocked)
	}
	if v := field(t, ms, pa(1), "X").Num; v != 1 {
		t.Fatalf("X=%v", v)
	}
	evs := drain(ch)
	if len(evs) != 1 || evs[0].Kind != KindLoop || evs[0].Topic != TopicLoop || evs[0].Rule != "A" {
		t.Fatalf("loop event: %+v", evs)
	}
	// the engine is still usable and loop-protected again for the next write
	res, _ = e.Set(pa(1), "X", box.Number(2))
	if len(res.Blocked) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestSelfTriggeringLevelRuleIsStopped(t *testing.T) {
	rules := `{Count[ON="players.red-xai.db:Player.N"]} = [ {Trigger[]} = "level", {When[]} = "< 100", {SetTarget[]} = "N = N + 1" ];`
	e, ms := newEngine(t, Config{}, rules)
	e.Put(pa(1), rec(t, `{Player[ID=1]= [ {N[]} = 0 ]};`))
	res, _ := e.Set(pa(1), "N", box.Number(0)) // no-op write: still triggers evaluation
	_ = res
	res, _ = e.Set(pa(1), "N", box.Number(5))
	if len(res.Fired) != 1 || len(res.Blocked) != 1 || res.Blocked[0].Reason != "loop" {
		t.Fatalf("%+v", res)
	}
	if v := field(t, ms, pa(1), "N").Num; v != 6 {
		t.Fatalf("N=%v, the rule must run exactly once per chain", v)
	}
}

func chainRules(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, `{R%d[ON="players.red-xai.db:B%d.V"]} = [ {When[]} = "== 1", {SetTarget[]} = "\"players.red-xai.db\":B%d[ID=1].V = 1" ];`+"\n", i, i, i+1)
	}
	return sb.String()
}

func TestChainDepthLimit(t *testing.T) {
	const n = 20
	e, ms := newEngine(t, Config{MaxChainDepth: 5}, chainRules(n))
	for i := 0; i <= n; i++ {
		b := fmt.Sprintf("B%d", i)
		ms.Put(addr(b, 1), rec(t, fmt.Sprintf(`{%s[ID=1]= [ {V[]} = 0 ]};`, b)))
	}
	res, err := e.Set(addr("B0", 1), "V", box.Number(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fired) != 5 {
		t.Fatalf("fired %d: %+v", len(res.Fired), res.Fired)
	}
	if len(res.Blocked) != 1 || res.Blocked[0].Reason != "depth" || res.Blocked[0].Rule != "R5" {
		t.Fatalf("blocked: %+v", res.Blocked)
	}
	if field(t, ms, addr("B5", 1), "V").Num != 1 || field(t, ms, addr("B6", 1), "V").Num != 0 {
		t.Fatal("chain length wrong")
	}
	// within the default limit the whole chain of 20 works... up to 16
	e2, ms2 := newEngine(t, Config{}, chainRules(n))
	for i := 0; i <= n; i++ {
		b := fmt.Sprintf("B%d", i)
		ms2.Put(addr(b, 1), rec(t, fmt.Sprintf(`{%s[ID=1]= [ {V[]} = 0 ]};`, b)))
	}
	res, _ = e2.Set(addr("B0", 1), "V", box.Number(1))
	if len(res.Fired) != 16 || len(res.Blocked) != 1 {
		t.Fatalf("default limit: fired %d blocked %v", len(res.Fired), res.Blocked)
	}
}

func TestMutualRulesAcrossRecordsTerminate(t *testing.T) {
	// P1.V=1 sets P2.V=1, which sets P1.V=0 ... then P1's rule would need V==1 again.
	rules := `
{R1[ON="players.red-xai.db:Player[ID=1].V"]} = [ {When[]} = "== 1", {SetTarget[]} = "\"players.red-xai.db\":Player[ID=2].V = 1" ];
{R2[ON="players.red-xai.db:Player[ID=2].V"]} = [ {Trigger[]} = "level", {When[]} = ">= 0", {SetTarget[]} = "\"players.red-xai.db\":Player[ID=1].V = 1 - \"players.red-xai.db\":Player[ID=1].V" ];`
	e, ms := newEngine(t, Config{}, rules)
	ms.Put(pa(1), rec(t, `{Player[ID=1]= [ {V[]} = 0 ]};`))
	ms.Put(pa(2), rec(t, `{Player[ID=2]= [ {V[]} = 0 ]};`))
	done := make(chan Result, 1)
	go func() { r, _ := e.Set(pa(1), "V", box.Number(1)); done <- r }()
	select {
	case res := <-done:
		if len(res.Fired) > 2 {
			t.Fatalf("%+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rule loop did not terminate")
	}
}

// ---------------------------------------------------------------- timers

func TestTimerRestartCancelDeleteRemove(t *testing.T) {
	rules := `
{Arm[ON="players.red-xai.db:Player.Armed"]} = [ {When[]} = "== TRUE",
   {Timer[]} = [ {Name[]} = "boom", {Seconds[]} = 5, {Emit[]} = "boom" ] ];
{Disarm[ON="players.red-xai.db:Player.Armed"]} = [ {When[]} = "== FALSE", {CancelTimer[]} = "boom" ];`
	clk := NewManualClock(t0)
	e, _ := newEngine(t, Config{Clock: clk}, rules)
	ch, cancel, _ := e.Subscribe("boom", 10)
	defer cancel()
	e.Put(pa(1), rec(t, `{Player[ID=1]= [ {Armed[]} = FALSE ]};`))
	e.Set(pa(1), "Armed", box.Bool(true))
	clk.Advance(3 * time.Second)
	// re-arm: false then true restarts the timer from now
	e.Set(pa(1), "Armed", box.Bool(false))
	if len(e.Timers()) != 0 || clk.Pending() != 0 {
		t.Fatalf("cancel did not stop the timer: %v pending=%d", e.Timers(), clk.Pending())
	}
	e.Set(pa(1), "Armed", box.Bool(true))
	clk.Advance(4 * time.Second)
	if len(drain(ch)) != 0 {
		t.Fatal("cancelled timer fired")
	}
	clk.Advance(time.Second)
	if len(drain(ch)) != 1 {
		t.Fatal("timer should fire once, 5s after the last start")
	}
	// restart replaces: two starts, one fire
	e.Set(pa(1), "Armed", box.Bool(true)) // already true: no edge, no start
	e.Set(pa(1), "Armed", box.Bool(false))
	e.Set(pa(1), "Armed", box.Bool(true))
	e.Set(pa(1), "Armed", box.Bool(true))
	clk.Advance(10 * time.Second)
	if n := len(drain(ch)); n != 1 {
		t.Fatalf("fired %d times", n)
	}

	// deleting the record cancels its timers
	e.Set(pa(1), "Armed", box.Bool(false))
	e.Set(pa(1), "Armed", box.Bool(true))
	if len(e.Timers()) != 1 {
		t.Fatal("timer not started")
	}
	if ok, err := e.Delete(pa(1)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(e.Timers()) != 0 || clk.Pending() != 0 {
		t.Fatal("delete must cancel timers")
	}
	// removing a rule cancels its timers
	e.Put(pa(2), rec(t, `{Player[ID=2]= [ {Armed[]} = TRUE ]};`))
	if len(e.Timers()) != 1 {
		t.Fatal("timer not started on create")
	}
	if !e.RemoveRule("Arm") || e.RemoveRule("Arm") {
		t.Fatal("RemoveRule result")
	}
	if len(e.Timers()) != 0 {
		t.Fatal("RemoveRule must cancel timers")
	}
	clk.Advance(time.Minute)
	if len(drain(ch)) != 0 {
		t.Fatal("removed rule's timer fired")
	}
}

func TestTimerOnDeletedRecordAndClose(t *testing.T) {
	clk := NewManualClock(t0)
	e, ms := newEngine(t, Config{Clock: clk}, deathRules)
	ch, cancel, _ := e.Subscribe("pulse.error", 10)
	defer cancel()
	e.Put(pa(1), player(t, 1, 0)) // created dead: fires immediately
	if len(e.Timers()) != 1 {
		t.Fatal("no timer")
	}
	// record vanishes behind the engine's back (direct store write)
	ms.Delete(pa(1))
	clk.Advance(10 * time.Second)
	evs := drain(ch)
	if len(evs) != 1 || evs[0].Kind != KindError || !strings.Contains(evs[0].Err, "no longer exists") {
		t.Fatalf("%+v", evs)
	}
	// Close stops everything
	e.Put(pa(2), player(t, 2, 0))
	e.Close()
	if len(e.Timers()) != 0 || clk.Pending() != 0 {
		t.Fatal("Close must cancel timers")
	}
	clk.Advance(time.Minute)
	e.Put(pa(3), player(t, 3, 0)) // rules still run, timers are ignored
	if len(e.Timers()) != 0 {
		t.Fatal("timers must not start after Close")
	}
}

func TestTimerGenerationLimit(t *testing.T) {
	// A timer whose actions retrigger the rule that started it: a perpetual
	// ticker. Each hop is one generation; the chain is cut after the limit.
	rules := `{Tick[ON="players.red-xai.db:Player.Run"]} = [ {When[]} = "== TRUE",
	  {Timer[]} = [ {Name[]} = "t", {Seconds[]} = 1, {Do[]} = ;["set Run = FALSE", "set Run = TRUE"]: ] ];`
	clk := NewManualClock(t0)
	e, _ := newEngine(t, Config{Clock: clk, MaxTimerGenerations: 3}, rules)
	ch, cancel, _ := e.Subscribe("pulse.*", 1000)
	defer cancel()
	e.Put(pa(1), rec(t, `{Player[ID=1]= [ {Run[]} = FALSE ]};`))
	e.Set(pa(1), "Run", box.Bool(true))
	clk.Advance(time.Hour)
	var fired, loops int
	for _, ev := range drain(ch) {
		switch ev.Topic {
		case TopicTimerFired:
			fired++
		case TopicLoop:
			loops++
			if !strings.Contains(ev.Err, "timer-generation") {
				t.Fatalf("%s", ev.Err)
			}
		}
	}
	// generations 0,1,2 run their actions; generation 3 fires but is cut.
	if fired != 4 || loops != 1 || len(e.Timers()) != 0 || clk.Pending() != 0 {
		t.Fatalf("fired=%d loops=%d timers=%v pending=%d", fired, loops, e.Timers(), clk.Pending())
	}
}

func TestTimerRealClock(t *testing.T) {
	rules := `{Quick[ON="players.red-xai.db:Player.Go"]} = [ {When[]} = "== TRUE",
	  {Timer[]} = [ {Name[]} = "q", {Seconds[]} = 0.05, {SetTarget[]} = "Done = TRUE", {Emit[]} = "quick.done" ] ];`
	e, ms := newEngine(t, Config{}, rules)
	ch, cancel, _ := e.Subscribe("quick.done", 1)
	defer cancel()
	e.Put(pa(1), rec(t, `{Player[ID=1]= [ {Go[]} = FALSE, {Done[]} = FALSE ]};`))
	start := time.Now()
	e.Set(pa(1), "Go", box.Bool(true))
	select {
	case ev := <-ch:
		if d := time.Since(start); d < 40*time.Millisecond || d > 3*time.Second {
			t.Fatalf("timer fired after %v", d)
		} else if ev.Data == nil {
			t.Fatal("no snapshot")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timer never fired")
	}
	if !field(t, ms, pa(1), "Done").Bool {
		t.Fatal("timer action did not write")
	}
}

// ---------------------------------------------------------------- cross-record + errors

func TestCrossRecordReadAndWrite(t *testing.T) {
	rules := `{Bonus[ON="players.red-xai.db:Player.Kills"]} = [ {When[]} = ">= 3",
	  {SetTarget[]} = "\"guilds.red-xai.db\":Guild[ID=1].Score = \"guilds.red-xai.db\":Guild[ID=1].Score + Kills * 10" ];`
	e, ms := newEngine(t, Config{}, rules)
	g := link.Addr{File: "guilds.red-xai.db", Box: "Guild", ID: 1, HasID: true}
	ms.Put(g, rec(t, `{Guild[ID=1]= [ {Score[]} = 5 ]};`))
	e.Put(pa(1), rec(t, `{Player[ID=1]= [ {Kills[]} = 0 ]};`))
	res, _ := e.Set(pa(1), "Kills", box.Number(3))
	if len(res.Fired) != 1 || len(res.Errors) != 0 {
		t.Fatalf("%+v", res)
	}
	if v := field(t, ms, g, "Score").Num; v != 35 {
		t.Fatalf("score %v", v)
	}
	// missing remote record -> error reported, write itself succeeded
	ms.Delete(g)
	e.Set(pa(1), "Kills", box.Number(1))
	res, err := e.Set(pa(1), "Kills", box.Number(4))
	if err != nil || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "does not exist") {
		t.Fatalf("%+v %v", res, err)
	}
	if field(t, ms, pa(1), "Kills").Num != 4 {
		t.Fatal("the triggering write must stay stored")
	}
}

func TestConditionAndActionErrors(t *testing.T) {
	rules := `
{BadCond[ON="players.red-xai.db:Player.Name"]} = [ {When[]} = "Name - 1 > 0", {Emit[]} = "never" ];
{StopsAtError[ON="players.red-xai.db:Player.Health"]} = [ {When[]} = "<= 0", {SetTarget[]} = "Health = Health / 0", {Emit[]} = "not.reached" ];
{Ok[ON="players.red-xai.db:Player.Health"]} = [ {When[]} = "<= 0", {Emit[]} = "ok" ];`
	e, _ := newEngine(t, Config{}, rules)
	ch, cancel, _ := e.Subscribe("*", 100)
	defer cancel()
	e.Put(pa(1), player(t, 1, 10)) // BadCond already fails here (Name is a string)
	drain(ch)
	res, err := e.Set(pa(1), "Health", box.Number(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 2 { // BadCond (condition) + StopsAtError (action)
		t.Fatalf("errors: %v", res.Errors)
	}
	got := topics(drain(ch))
	if strings.Contains(strings.Join(got, ","), "not.reached") || !strings.Contains(strings.Join(got, ","), "ok") {
		t.Fatalf("events %v", got)
	}
	var errEvents int
	for _, x := range got {
		if x == TopicError {
			errEvents++
		}
	}
	if errEvents != 2 {
		t.Fatalf("error events: %v", got)
	}
}

func TestSetFieldCreatesNestedAndNoopSkipsWrite(t *testing.T) {
	rules := `{Init[ON="players.red-xai.db:Player.Health"]} = [ {When[]} = "<= 0", {SetTarget[]} = ;["Stats.Deaths = 1", "Inventory[id=3].Count = 2", "Stats.Deaths = 1"]: ];`
	cs := &countingStore{Store: link.NewMemStore()}
	e := New(cs, Config{})
	defer e.Close()
	if err := e.LoadRules(rules); err != nil {
		t.Fatal(err)
	}
	e.Put(pa(1), player(t, 1, 5))
	cs.puts.Store(0)
	e.Set(pa(1), "Health", box.Number(0))
	if v := field(t, cs, pa(1), "Stats.Deaths"); v.Num != 1 {
		t.Fatalf("%v", v)
	}
	if v := field(t, cs, pa(1), "Inventory[id=3].Count"); v.Num != 2 {
		t.Fatalf("%v", v)
	}
	// Set(Health), Stats.Deaths, Inventory: the repeated Stats.Deaths = 1 is a no-op
	if n := cs.puts.Load(); n != 3 {
		t.Fatalf("puts=%d", n)
	}
	// setting a field below a scalar fails cleanly
	if _, err := e.Set(pa(1), "Name.Sub", box.Number(1)); err == nil {
		t.Fatal("expected error")
	}
	if _, err := e.Set(pa(99), "Name", box.Number(1)); err == nil {
		t.Fatal("expected error for missing record")
	}
}

type countingStore struct {
	Store
	puts atomic.Int64
}

func (c *countingStore) Put(a link.Addr, e *box.Entry) error { c.puts.Add(1); return c.Store.Put(a, e) }
func (c *countingStore) Get(a link.Addr) (*box.Entry, bool, error) {
	return c.Store.Get(a)
}

func TestPutValidation(t *testing.T) {
	e, _ := newEngine(t, Config{}, "")
	if _, err := e.Put(pa(1), player(t, 2, 1)); !errors.Is(err, ErrEntryMismatch) {
		t.Fatal(err)
	}
	if _, err := e.Put(addr("Other", 1), player(t, 1, 1)); !errors.Is(err, ErrEntryMismatch) {
		t.Fatal(err)
	}
	if _, err := e.Put(pa(1), nil); !errors.Is(err, ErrEntryMismatch) {
		t.Fatal(err)
	}
}

type failStore struct {
	Store
	fail atomic.Bool
}

func (f *failStore) Put(a link.Addr, e *box.Entry) error {
	if f.fail.Load() {
		return errors.New("disk full")
	}
	return f.Store.Put(a, e)
}

func TestStoreFailureIsReturned(t *testing.T) {
	fs := &failStore{Store: link.NewMemStore()}
	e := New(fs, Config{})
	defer e.Close()
	fs.fail.Store(true)
	if _, err := e.Put(pa(1), player(t, 1, 1)); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("%v", err)
	}
}

// ---------------------------------------------------------------- bus

func TestBus(t *testing.T) {
	b := NewBus()
	all, cAll := b.Subscribe("*", 4)
	pre, cPre := b.Subscribe("pulse.*", 4)
	exact, cExact := b.Subscribe("player.died", 4)
	b.Publish(Event{Topic: "player.died"})
	b.Publish(Event{Topic: "pulse.loop"})
	b.Publish(Event{Topic: "other"})
	if len(drain(all)) != 3 || len(drain(pre)) != 1 || len(drain(exact)) != 1 {
		t.Fatal("pattern matching")
	}
	// slow subscriber never blocks the publisher; drops are counted
	for i := 0; i < 10; i++ {
		b.Publish(Event{Topic: "x"})
	}
	if b.Dropped() != 6 {
		t.Fatalf("dropped %d", b.Dropped())
	}
	cAll()
	cAll() // idempotent
	if _, ok := <-all; ok {
		// buffered events may remain; drain until closed
		for range all {
		}
	}
	cPre()
	cExact()
	if b.Subscribers() != 0 {
		t.Fatal("subscribers left")
	}
	b.Publish(Event{Topic: "x"}) // no subscribers: no panic
}

type sinkOnly struct{ n atomic.Int64 }

func (s *sinkOnly) Publish(Event) { s.n.Add(1) }

func TestCustomPublisher(t *testing.T) {
	s := &sinkOnly{}
	e, _ := newEngine(t, Config{Publisher: s, Clock: NewManualClock(t0)}, deathRules)
	if _, _, err := e.Subscribe("*", 1); !errors.Is(err, ErrNoSubscriber) {
		t.Fatalf("%v", err)
	}
	if e.Bus() != nil {
		t.Fatal("no bus expected")
	}
	e.Put(pa(1), player(t, 1, 0))
	if s.n.Load() == 0 {
		t.Fatal("custom publisher got nothing")
	}
}

// ---------------------------------------------------------------- concurrency

func TestConcurrentWritesTimersAndSubscribers(t *testing.T) {
	clk := NewManualClock(t0)
	e, ms := newEngine(t, Config{Clock: clk}, deathRules)
	const players = 50
	for i := int64(1); i <= players; i++ {
		e.Put(pa(i), player(t, i, 100))
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var received atomic.Int64
	for s := 0; s < 4; s++ {
		wg.Add(1)
		ch, cancel, _ := e.Subscribe("player.*", 8)
		go func() {
			defer wg.Done()
			defer cancel()
			for {
				select {
				case <-ch:
					received.Add(1)
				case <-stop:
					return
				}
			}
		}()
	}
	// churn subscribers while publishing
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, cancel, _ := e.Subscribe("*", 1)
			cancel()
		}
	}()
	var writers sync.WaitGroup
	for i := int64(1); i <= players; i++ {
		writers.Add(1)
		go func(i int64) {
			defer writers.Done()
			for k := 0; k < 20; k++ {
				e.Set(pa(i), "Name", box.String(fmt.Sprintf("n%d", k)))
				e.Timers()
			}
			if _, err := e.Set(pa(i), "Health", box.Number(0)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	writers.Wait()
	if n := len(e.Timers()); n != players {
		t.Fatalf("want %d respawn timers, got %d", players, n)
	}
	clk.Advance(10 * time.Second)
	close(stop)
	wg.Wait()
	for i := int64(1); i <= players; i++ {
		if field(t, ms, pa(i), "Health").Num != 100 || field(t, ms, pa(i), "IsDead").Bool {
			t.Fatalf("player %d did not respawn", i)
		}
	}
	if len(e.Timers()) != 0 {
		t.Fatal("timers left")
	}
}

func TestConcurrentRealClockTimers(t *testing.T) {
	rules := `{Quick[ON="players.red-xai.db:Player.Go"]} = [ {When[]} = "== TRUE",
	  {Timer[]} = [ {Name[]} = "q", {Seconds[]} = 0.02, {SetTarget[]} = "Done = TRUE" ] ];`
	e, ms := newEngine(t, Config{}, rules)
	const n = 30
	var wg sync.WaitGroup
	for i := int64(1); i <= n; i++ {
		wg.Add(1)
		go func(i int64) {
			defer wg.Done()
			e.Put(pa(i), rec(t, fmt.Sprintf(`{Player[ID=%d]= [ {Go[]} = FALSE, {Done[]} = FALSE ]};`, i)))
			e.Set(pa(i), "Go", box.Bool(true))
			// racing restart + cancel-by-delete on every third record
			if i%3 == 0 {
				e.Set(pa(i), "Go", box.Bool(false))
				e.Set(pa(i), "Go", box.Bool(true))
			}
		}(i)
	}
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.Timers()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := int64(1); i <= n; i++ {
		if !field(t, ms, pa(i), "Done").Bool {
			t.Fatalf("player %d timer never ran", i)
		}
	}
}

func TestManualClock(t *testing.T) {
	c := NewManualClock(t0)
	var order []int
	c.AfterFunc(2*time.Second, func() { order = append(order, 2) })
	st := c.AfterFunc(time.Second, func() { order = append(order, 1) })
	c.AfterFunc(3*time.Second, func() {
		order = append(order, 3)
		c.AfterFunc(time.Second, func() { order = append(order, 4) }) // scheduled while advancing
	})
	if !st.Stop() || st.Stop() {
		t.Fatal("Stop semantics")
	}
	c.Advance(10 * time.Second)
	if fmt.Sprint(order) != "[2 3 4]" || !c.Now().Equal(t0.Add(10*time.Second)) || c.Pending() != 0 {
		t.Fatalf("%v %v", order, c.Now())
	}
}
