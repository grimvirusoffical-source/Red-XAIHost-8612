package sec

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

const goodPolicy = `
{Scope[1]} = [ {Name[]} = "players.read", {Actions[]} = ;["read"]:, {Resource[]} = "players.red-xai.db" ];
{Scope[2]} = [ {Name[]} = "chat.write", {Actions[]} = ;["read","write"]:, {Resource[]} = ;["chat.red-xai-db-qs","lobby.red-xai-db-qs:Msg"]: ];
{Role[1]} = [ {Name[]} = "guest", {Scopes[]} = ;["players.read"]: ];
{Role[2]} = [ {Name[]} = "player", {Inherits[]} = ;["guest"]:, {Scopes[]} = ;["chat.write"]: ];
{Role[3]} = [ {Name[]} = "admin", {Inherits[]} = ;["player"]: ];
{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = ;["role:admin"]:, {Actions[]} = "*", {Resource[]} = "*" ];
{Rule[2]} = [ {Effect[]} = "deny", {Who[]} = ;["*"]:, {Actions[]} = ;["read","write"]:, {Resource[]} = "players.red-xai.db:Player.Email" ];
{Rule[3]} = [ {Effect[]} = "allow", {Who[]} = ;["user:*"]:, {Actions[]} = ;["read","write"]:, {Resource[]} = "players.red-xai.db:Player.Email", {Owner[]} = TRUE ];
{Rule[4]} = [ {Effect[]} = "deny", {Who[]} = ;["role:guest"]:, {Actions[]} = ;["write","delete"]:, {Resource[]} = "players.red-xai.db", {Priority[]} = 5 ];
{Encrypt[1]} = [ {File[]} = ;["vault*","*.secret"]:, {Key[]} = "main-2026" ];
`

func mustPolicy(t *testing.T, src string) *Policy {
	t.Helper()
	p, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

func TestDefaultDeny(t *testing.T) {
	var nilp *Policy
	res := Resource{File: "players.red-xai.db", Box: "Player", Field: "Name"}
	pr := Principal{ID: "u1", Roles: []string{"admin"}}
	if d := nilp.Authorize(pr, ActionRead, res); d.Allowed {
		t.Fatal("nil policy must deny")
	}
	empty := mustPolicy(t, "")
	if d := empty.Authorize(pr, ActionRead, res); d.Allowed || d.Rule != "" {
		t.Fatalf("empty policy must deny by default, got %+v", d)
	}
	p := mustPolicy(t, goodPolicy)
	cases := []struct {
		name string
		pr   Principal
		act  Action
		res  Resource
	}{
		{"anonymous", Principal{}, ActionRead, res},
		{"unknown role", Principal{ID: "x", Roles: []string{"nope"}}, ActionRead, res},
		{"unknown scope", Principal{ID: "x", Scopes: []string{"players.read.typo"}}, ActionRead, res},
		{"undeclared scope held directly", Principal{ID: "x", Scopes: []string{"admin"}}, ActionRead, res},
		{"guest writes", Principal{ID: "g", Roles: []string{"guest"}}, ActionWrite, res},
		{"guest other file", Principal{ID: "g", Roles: []string{"guest"}}, ActionRead, Resource{File: "secrets.red-xai.db"}},
		{"unknown action", pr, Action("drop"), res},
		{"wildcard action is not a request", pr, ActionAny, res},
		{"empty action", pr, "", res},
		{"no file", pr, ActionRead, Resource{}},
		{"field without box", pr, ActionRead, Resource{File: "a", Field: "f"}},
		{"control char in resource", pr, ActionRead, Resource{File: "a\x00b"}},
	}
	for _, c := range cases {
		if d := p.Authorize(c.pr, c.act, c.res); d.Allowed {
			t.Errorf("%s: want deny, got %+v", c.name, d)
		}
	}
	if err := p.Check(Principal{}, ActionRead, res); !errors.Is(err, ErrDenied) {
		t.Fatalf("Check must wrap ErrDenied, got %v", err)
	}
	if err := p.Check(pr, ActionRead, res); err != nil {
		t.Fatalf("admin read: %v", err)
	}
}

func TestScopesRolesAndInheritance(t *testing.T) {
	p := mustPolicy(t, goodPolicy)
	read := Resource{File: "players.red-xai.db", Box: "Player", Field: "Name"}
	guest := Principal{ID: "g", Roles: []string{"guest"}}
	player := Principal{ID: "p", Roles: []string{"player"}}
	if d := p.Authorize(guest, ActionRead, read); !d.Allowed || !strings.Contains(d.Rule, "players.read") {
		t.Fatalf("guest read via scope: %+v", d)
	}
	// player inherits guest's scope
	if d := p.Authorize(player, ActionRead, read); !d.Allowed {
		t.Fatalf("inherited scope: %+v", d)
	}
	// chat.write has two resources, one with a box
	if !p.Authorize(player, ActionWrite, Resource{File: "chat.red-xai-db-qs", Box: "Anything"}).Allowed {
		t.Fatal("chat write")
	}
	if !p.Authorize(player, ActionWrite, Resource{File: "lobby.red-xai-db-qs", Box: "Msg", Field: "Text"}).Allowed {
		t.Fatal("lobby Msg write")
	}
	if p.Authorize(player, ActionWrite, Resource{File: "lobby.red-xai-db-qs", Box: "Other"}).Allowed {
		t.Fatal("lobby Other write must be denied")
	}
	if p.Authorize(player, ActionWrite, Resource{File: "lobby.red-xai-db-qs"}).Allowed {
		t.Fatal("pattern naming a box must not cover a request for the whole file")
	}
	// scope held directly (API key style) works without any role
	key := Principal{Scopes: []string{"chat.write"}}
	if !p.Authorize(key, ActionRead, Resource{File: "chat.red-xai-db-qs"}).Allowed {
		t.Fatal("direct scope")
	}
	if p.Authorize(key, ActionDelete, Resource{File: "chat.red-xai-db-qs"}).Allowed {
		t.Fatal("scope does not grant delete")
	}
	// a role does not leak sideways: guest has no chat scope
	if p.Authorize(guest, ActionRead, Resource{File: "chat.red-xai-db-qs"}).Allowed {
		t.Fatal("guest must not read chat")
	}
	if got := strings.Join(p.Roles(), ","); got != "admin,guest,player" {
		t.Fatalf("roles %s", got)
	}
	if got := strings.Join(p.Scopes(), ","); got != "chat.write,players.read" {
		t.Fatalf("scopes %s", got)
	}
}

func TestRulePrecedence(t *testing.T) {
	p := mustPolicy(t, goodPolicy)
	email := Resource{File: "players.red-xai.db", Box: "Player", Field: "Email", Owner: "u7"}
	admin := Principal{ID: "a", Roles: []string{"admin"}}
	owner := Principal{ID: "u7", Roles: []string{"player"}}
	other := Principal{ID: "u8", Roles: []string{"player"}}

	// admin allow "*" (less specific) loses to the more specific deny on Email at equal priority
	d := p.Authorize(admin, ActionRead, email)
	if d.Allowed || d.Rule != "Rule[ID=2]" {
		t.Fatalf("specific deny should beat broad allow, got %+v", d)
	}
	// but the same admin may read the Name field (deny pattern does not match)
	if !p.Authorize(admin, ActionRead, Resource{File: "players.red-xai.db", Box: "Player", Field: "Name"}).Allowed {
		t.Fatal("admin reads Name")
	}
	// owner: Rule 3 (allow, same specificity as the deny Rule 2) loses the tie to deny
	d = p.Authorize(owner, ActionRead, email)
	if d.Allowed || d.Rule != "Rule[ID=2]" {
		t.Fatalf("deny must win an equal-specificity tie, got %+v", d)
	}
	// non-owner denied too
	if p.Authorize(other, ActionRead, email).Allowed {
		t.Fatal("non-owner")
	}
	// priority beats specificity: guests are denied write/delete on the whole file at priority 5
	p2 := mustPolicy(t, `
{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "write", {Resource[]} = "f:B.Field" ];
{Rule[2]} = [ {Effect[]} = "deny", {Who[]} = "*", {Actions[]} = "write", {Resource[]} = "f", {Priority[]} = 1 ];
`)
	if d := p2.Authorize(Principal{}, ActionWrite, Resource{File: "f", Box: "B", Field: "Field"}); d.Allowed || d.Rule != "Rule[ID=2]" {
		t.Fatalf("higher priority must win over specificity: %+v", d)
	}
	// negative priority loses to a default-priority rule
	p3 := mustPolicy(t, `
{Rule[1]} = [ {Effect[]} = "deny", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f", {Priority[]} = -1 ];
{Rule[2]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f" ];
`)
	if !p3.Authorize(Principal{}, ActionRead, Resource{File: "f"}).Allowed {
		t.Fatal("priority 0 allow should beat priority -1 deny")
	}
	// literal beats wildcard at equal segment count
	p4 := mustPolicy(t, `
{Rule[1]} = [ {Effect[]} = "deny", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f*" ];
{Rule[2]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "file1" ];
`)
	if !p4.Authorize(Principal{}, ActionRead, Resource{File: "file1"}).Allowed {
		t.Fatal("literal pattern should be more specific than wildcard")
	}
	if p4.Authorize(Principal{}, ActionRead, Resource{File: "file2"}).Allowed {
		t.Fatal("wildcard deny applies to file2")
	}
	// scope grant vs rule deny at the same priority and specificity: deny wins
	p5 := mustPolicy(t, `
{Scope[1]} = [ {Name[]} = "s", {Actions[]} = "read", {Resource[]} = "f" ];
{Rule[1]} = [ {Effect[]} = "deny", {Who[]} = "scope:s", {Actions[]} = "read", {Resource[]} = "f" ];
`)
	if p5.Authorize(Principal{Scopes: []string{"s"}}, ActionRead, Resource{File: "f"}).Allowed {
		t.Fatal("deny must beat a scope grant in a tie")
	}
	// order of rules in the document must not matter
	p6 := mustPolicy(t, `
{Rule[2]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f" ];
{Rule[1]} = [ {Effect[]} = "deny", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f" ];
`)
	if p6.Authorize(Principal{}, ActionRead, Resource{File: "f"}).Allowed {
		t.Fatal("deny wins regardless of order")
	}
}

func TestOwnerAndUsers(t *testing.T) {
	p := mustPolicy(t, `
{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "user:*", {Actions[]} = ;["read","write"]:, {Resource[]} = "g:Save", {Owner[]} = TRUE ];
{Rule[2]} = [ {Effect[]} = "allow", {Who[]} = "user:boss", {Actions[]} = "delete", {Resource[]} = "g" ];
`)
	save := Resource{File: "g", Box: "Save", Owner: "u1"}
	if !p.Authorize(Principal{ID: "u1"}, ActionWrite, save).Allowed {
		t.Fatal("owner write")
	}
	if p.Authorize(Principal{ID: "u2"}, ActionWrite, save).Allowed {
		t.Fatal("non-owner write")
	}
	if p.Authorize(Principal{}, ActionWrite, Resource{File: "g", Box: "Save"}).Allowed {
		t.Fatal("anonymous must not match owner rule with empty owner")
	}
	if p.Authorize(Principal{ID: "u1"}, ActionWrite, Resource{File: "g", Box: "Save"}).Allowed {
		t.Fatal("resource without owner must not match owner rule")
	}
	if !p.Authorize(Principal{ID: "boss"}, ActionDelete, Resource{File: "g", Box: "Save"}).Allowed {
		t.Fatal("user:boss delete")
	}
	if p.Authorize(Principal{ID: "boss2"}, ActionDelete, Resource{File: "g"}).Allowed {
		t.Fatal("user id must match exactly")
	}
}

func TestFilterFields(t *testing.T) {
	p := mustPolicy(t, goodPolicy)
	admin := Principal{ID: "a", Roles: []string{"admin"}}
	got := p.FilterFields(admin, ActionRead, "players.red-xai.db", "Player", "u7", []string{"Name", "Email", "Level"})
	if strings.Join(got, ",") != "Name,Level" {
		t.Fatalf("got %v", got)
	}
}

func TestEncryptionFor(t *testing.T) {
	p := mustPolicy(t, goodPolicy)
	for _, f := range []string{"vault1.red-xai-pack", "x.secret"} {
		if k, ok := p.EncryptionFor(f); !ok || k != "main-2026" {
			t.Fatalf("%s: %q %v", f, k, ok)
		}
	}
	if _, ok := p.EncryptionFor("players.red-xai.db"); ok {
		t.Fatal("players must not require encryption")
	}
	var nilp *Policy
	if _, ok := nilp.EncryptionFor("vault"); ok {
		t.Fatal("nil policy")
	}
}

func TestParseErrors(t *testing.T) {
	bad := map[string]string{
		"not box syntax":      `{Rule[1] = `,
		"unknown top-level":   `{Admin[1]} = [ {Name[]} = "x" ];`,
		"scalar top-level":    `{Role[1]} = "x";`,
		"unknown key (typo)":  `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f", {Priorty[]} = 9 ];`,
		"bad effect":          `{Rule[1]} = [ {Effect[]} = "permit", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f" ];`,
		"missing who":         `{Rule[1]} = [ {Effect[]} = "allow", {Actions[]} = "read", {Resource[]} = "f" ];`,
		"bad who":             `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "everyone", {Actions[]} = "read", {Resource[]} = "f" ];`,
		"undeclared role":     `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "role:x", {Actions[]} = "read", {Resource[]} = "f" ];`,
		"undeclared scope":    `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "scope:x", {Actions[]} = "read", {Resource[]} = "f" ];`,
		"bad action":          `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "root", {Resource[]} = "f" ];`,
		"empty actions":       `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = ;[]:, {Resource[]} = "f" ];`,
		"bad pattern":         `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f:" ];`,
		"bad pattern 2":       `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f:B." ];`,
		"empty file pattern":  `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = ":B" ];`,
		"priority range":      `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f", {Priority[]} = 5000 ];`,
		"priority fraction":   `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f", {Priority[]} = 1.5 ];`,
		"owner not bool":      `{Rule[1]} = [ {Effect[]} = "allow", {Who[]} = "*", {Actions[]} = "read", {Resource[]} = "f", {Owner[]} = "yes" ];`,
		"dup role":            `{Role[1]} = [ {Name[]} = "a" ]; {Role[2]} = [ {Name[]} = "a" ];`,
		"dup scope":           `{Scope[1]} = [ {Name[]} = "a", {Actions[]} = "read", {Resource[]} = "f" ]; {Scope[2]} = [ {Name[]} = "a", {Actions[]} = "read", {Resource[]} = "f" ];`,
		"role bad name":       `{Role[1]} = [ {Name[]} = "a b" ];`,
		"role unknown scope":  `{Role[1]} = [ {Name[]} = "a", {Scopes[]} = "nope" ];`,
		"role unknown parent": `{Role[1]} = [ {Name[]} = "a", {Inherits[]} = "nope" ];`,
		"inherit cycle":       `{Role[1]} = [ {Name[]} = "a", {Inherits[]} = "b" ]; {Role[2]} = [ {Name[]} = "b", {Inherits[]} = "a" ];`,
		"self inherit":        `{Role[1]} = [ {Name[]} = "a", {Inherits[]} = "a" ];`,
		"encrypt bad key id":  `{Encrypt[1]} = [ {File[]} = "v*", {Key[]} = "bad key" ];`,
		"encrypt no key":      `{Encrypt[1]} = [ {File[]} = "v*" ];`,
		"name wrong type":     `{Role[1]} = [ {Name[]} = 7 ];`,
		"array has non-str":   `{Role[1]} = [ {Name[]} = "a", {Scopes[]} = ;[1]: ];`,
		"duplicate key":       `{Role[1]} = [ {Name[]} = "a", {Name[2]} = "b" ];`,
	}
	for name, src := range bad {
		_, err := Parse(src)
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if !errors.Is(err, ErrBadPolicy) {
			t.Errorf("%s: error should wrap ErrBadPolicy: %v", name, err)
		}
	}
	if _, err := Parse(strings.Repeat(" ", MaxPolicyBytes+1)); !errors.Is(err, ErrBadPolicy) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestGlob(t *testing.T) {
	yes := [][2]string{{"*", ""}, {"*", "abc"}, {"a*c", "abc"}, {"a*c", "ac"}, {"a?c", "abc"}, {"*.db", "x.red-xai.db"},
		{"a*b*c", "aXXbYYc"}, {"**", "x"}, {"abc", "abc"}, {"*a", "bba"}, {"a*", "a/b/c"}}
	no := [][2]string{{"a?c", "ac"}, {"abc", "abd"}, {"a*c", "abd"}, {"*a", "ab"}, {"", "a"}, {"a", ""}, {"?", ""}, {"abc", "ABC"}}
	for _, c := range yes {
		if !glob(c[0], c[1]) {
			t.Errorf("glob(%q,%q) should match", c[0], c[1])
		}
	}
	for _, c := range no {
		if glob(c[0], c[1]) {
			t.Errorf("glob(%q,%q) should not match", c[0], c[1])
		}
	}
	// no catastrophic backtracking
	if glob(strings.Repeat("a*", 50)+"b", strings.Repeat("a", 5000)) {
		t.Fatal("unexpected match")
	}
}

func TestAuthorizeConcurrent(t *testing.T) {
	p := mustPolicy(t, goodPolicy)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pr := Principal{ID: fmt.Sprint("u", i), Roles: []string{"player"}}
			for j := 0; j < 500; j++ {
				if !p.Authorize(pr, ActionRead, Resource{File: "players.red-xai.db", Box: "Player", Field: "Name"}).Allowed {
					t.Error("expected allow")
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
