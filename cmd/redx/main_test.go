package main

import (
	"flag"
	"reflect"
	"testing"

	"redx/host/client"
)

func TestParseInterspersedFlags(t *testing.T) {
	var g globals
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	g.bind(fs)
	ttl := fs.Int("ttl", 0, "")
	pos := parse(fs, []string{"a.red-xai.db:B.K", "--ttl", "30", "value", "--db", "game", "--json"})
	if !reflect.DeepEqual(pos, []string{"a.red-xai.db:B.K", "value"}) || *ttl != 30 || g.db != "game" || !g.asJSON {
		t.Fatalf("pos=%v ttl=%d g=%+v", pos, *ttl, g)
	}
}

func TestDataPathEscaping(t *testing.T) {
	if got := client.DataPath("", "p.red-xai.db:Box[ID=1].Key"); got != "/v1/path/p.red-xai.db:Box%5BID=1%5D.Key" {
		t.Fatalf("got %s", got)
	}
	if got := client.DataPath("game", "a b"); got != "/v1/db/game/path/a%20b" {
		t.Fatalf("got %s", got)
	}
}
