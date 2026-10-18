// Command redx is the RedX command line: keys, databases, data, nodes, backup, doctor.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"redx/host"
	"redx/host/client"
)

type globals struct {
	addr, key, pin, db, dataDir string
	insecure, offline, asJSON   bool
}

func (g *globals) bind(fs *flag.FlagSet) {
	fs.StringVar(&g.addr, "addr", envOr("REDX_ADDR", "http://127.0.0.1:7777"), "redxd address (env REDX_ADDR)")
	fs.StringVar(&g.key, "key", os.Getenv("REDX_KEY"), "API key (prefer env REDX_KEY: flags are visible in process lists)")
	fs.StringVar(&g.pin, "pin", os.Getenv("REDX_PIN"), "sha256 fingerprint of the server's TLS certificate (for self-signed certs)")
	fs.BoolVar(&g.insecure, "insecure", false, "skip TLS verification (NOT recommended; use --pin)")
	fs.StringVar(&g.db, "db", envOr("REDX_DB", ""), "database name (default: the server's default db)")
	fs.StringVar(&g.dataDir, "data-dir", envOr("REDX_DATA_DIR", host.DefaultDataDir()), "data directory (offline commands)")
	fs.BoolVar(&g.offline, "offline", false, "operate directly on the data dir instead of calling a running redxd")
	fs.BoolVar(&g.asJSON, "json", false, "machine readable output")
}

func (g *globals) client() *client.Client { return client.New(g.addr, g.key, g.pin, g.insecure) }

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func die(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "redx: "+format+"\n", a...)
	os.Exit(1)
}

// parse allows flags and positionals to be interleaved.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				os.Exit(0)
			}
			os.Exit(2)
		}
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "key":
		cmdKey(args)
	case "db":
		cmdDB(args)
	case "get", "set", "delete", "del", "query", "stats", "subscribe":
		cmdData(cmd, args)
	case "idx":
		cmdIdx(args)
	case "pack":
		cmdPack(args)
	case "pulse":
		cmdPulse(args)
	case "sql":
		cmdSQL(args)
	case "media":
		cmdMedia(args)
	case "code":
		cmdCode(args)
	case "sec":
		cmdSec(args)
	case "node":
		cmdNode(args)
	case "backup":
		cmdBackup(args)
	case "restore":
		cmdRestore(args)
	case "doctor":
		cmdDoctor(args)
	case "version", "--version", "-version", "-v":
		fmt.Printf("redx %s (%s/%s)\n", host.Version, runtime.GOOS, runtime.GOARCH)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "redx: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `redx - RedX command line

  redx key create --db NAME --scope read|write|admin [--ttl 30d] [--label X] [--prefix PATH] [--roles a,b] [--rate-rps N]
  redx key list | revoke ID | rotate ID [--grace 10m]
  redx db create NAME | list
  redx get PATH | set PATH VALUE [--ttl N --sliding] | delete PATH | query 'Q' | stats | subscribe PATH
  redx get PATH --resolve [--depth N]      follow embedded links
  redx idx define SET FILE | list | drop SET | rebuild SET | check SET
  redx idx query SET INDEX --field F (--value V | --lo N --hi N | --q 'words') [--mode exact|prefix|range|text|fuzzy] [--limit N] [--record]
  redx pack put PACK FILE|- [REMOTE/PATH] | get PACK REMOTE/PATH [--out F] [--range a-b] | ls PACK [FOLDER] | stat PACK PATH | rm PACK PATH | list | gc PACK | fsck PACK [--deep]
  redx sql 'SELECT ...' [--param V ...] [--explain] [--json]      RXQL (SQL-ish); ? placeholders take --param
  redx media put LIB FILE [NAME] [--type image/png] | get LIB NAME [--out F] [--range a-b] | ls LIB | stat LIB NAME | rm LIB NAME | list | fsck LIB [--deep]
  redx code ls | define FILE SRC|- | rm FILE | run SCRIPT [ARG ...]      .red-xai-code scripts
  redx sec show | put FILE|- | rm                                        .red-xai-sec access policy
  redx pulse put FILE | get | rm | subscribe [--topic P] [--changes]
  redx node token | join ADDR --token T | status | promote | repoint ADDR --token T
  redx backup --out FILE [--offline]      redx restore FILE [--force]
  redx doctor [--config FILE]
  redx version | --version

Common flags: --addr URL (REDX_ADDR) --key KEY (REDX_KEY) --db NAME --pin SHA256 --offline --data-dir DIR --json
Keys are shown ONCE at creation. Use env REDX_KEY rather than --key to keep them out of process lists.
`)
}

// ---- key ----

func openOfflineKS(g *globals) *host.Keystore {
	pass := os.Getenv("REDX_KEYSTORE_PASSPHRASE")
	ks, err := host.OpenKeystore(g.dataDir+string(os.PathSeparator)+"keys.red-xai-keys", host.KeystoreOptions{Passphrase: pass})
	if err != nil {
		die("%v", err)
	}
	return ks
}

func cmdKey(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("key "+sub, flag.ExitOnError)
	g.bind(fs)
	scope := fs.String("scope", "read", "read|write|admin")
	ttl := fs.String("ttl", "", "expiry, e.g. 30d, 12h (default: never)")
	label := fs.String("label", "", "human label")
	prefix := fs.String("prefix", "", "restrict to this path prefix")
	roles := fs.String("roles", "", "comma separated role names for the database's .red-xai-sec policy")
	rps := fs.Float64("rate-rps", 0, "per-key requests/second (0 = server default, -1 = unlimited)")
	burst := fs.Int("rate-burst", 0, "per-key burst")
	grace := fs.String("grace", "", "rotate: keep the old key valid this long (e.g. 10m)")
	pos := parse(fs, args)
	if g.db == "" && sub == "create" {
		g.db = "*"
	}
	switch sub {
	case "create":
		d, err := host.ParseTTL(*ttl)
		if err != nil {
			die("%v", err)
		}
		var key string
		var info interface{}
		if g.offline {
			ks := openOfflineKS(&g)
			k, rec, err := ks.Create(host.CreateSpec{DB: g.db, Scope: *scope, Prefix: *prefix, Label: *label, Roles: splitCSV(*roles), TTL: d, RateRPS: *rps, RateBurst: *burst})
			if err != nil {
				die("%v", err)
			}
			key, info = k, rec.ID
			fmt.Fprintln(os.Stderr, "note: offline key changes are not replicated to other cluster nodes; create keys through the leader's API in a cluster.")
		} else {
			var out struct {
				Key  string      `json:"key"`
				ID   string      `json:"id"`
				Info interface{} `json:"info"`
			}
			c, cancel := ctx()
			defer cancel()
			req := map[string]interface{}{"db": g.db, "scope": *scope, "prefix": *prefix, "label": *label, "roles": splitCSV(*roles), "ttl": *ttl, "rate_rps": *rps, "rate_burst": *burst}
			if err := g.client().JSON(c, "POST", "/v1/keys", req, &out); err != nil {
				die("%v", err)
			}
			key, info = out.Key, out.ID
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"key": key, "id": info})
			return
		}
		fmt.Printf("%s\n\nkey id: %v   (scope=%s db=%s)\nThis is the ONLY time the key is shown. Store it now.\n", key, info, *scope, g.db)
	case "list":
		var views []client.KeyViewAlias
		if g.offline {
			ks := openOfflineKS(&g)
			for _, r := range ks.List() {
				views = append(views, client.KeyViewAlias{ID: r.ID, Scope: r.Scope, DB: r.DB, Prefix: r.Prefix, Label: r.Label, Created: r.Created, Expires: r.Expires, Revoked: r.Revoked, Status: statusOf(r)})
			}
		} else {
			var out struct {
				Keys []client.KeyViewAlias `json:"keys"`
			}
			c, cancel := ctx()
			defer cancel()
			if err := g.client().JSON(c, "GET", "/v1/keys", nil, &out); err != nil {
				die("%v", err)
			}
			views = out.Keys
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(views)
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSCOPE\tDB\tPREFIX\tLABEL\tSTATUS\tEXPIRES")
		for _, v := range views {
			exp := "never"
			if v.Expires != nil {
				exp = v.Expires.Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.ID, v.Scope, v.DB, v.Prefix, v.Label, v.Status, exp)
		}
		tw.Flush()
	case "revoke":
		if len(pos) != 1 {
			die("usage: redx key revoke ID")
		}
		if g.offline {
			if _, err := openOfflineKS(&g).Revoke(pos[0]); err != nil {
				die("%v", err)
			}
		} else {
			c, cancel := ctx()
			defer cancel()
			if err := g.client().JSON(c, "DELETE", "/v1/keys/"+pos[0], nil, nil); err != nil {
				die("%v", err)
			}
		}
		fmt.Println("revoked", pos[0])
	case "rotate":
		if len(pos) != 1 {
			die("usage: redx key rotate ID [--grace 10m]")
		}
		var key, id string
		if g.offline {
			gr, err := host.ParseTTL(*grace)
			if err != nil {
				die("%v", err)
			}
			k, rec, err := openOfflineKS(&g).Rotate(pos[0], gr)
			if err != nil {
				die("%v", err)
			}
			key, id = k, rec.ID
		} else {
			var out struct {
				Key string `json:"key"`
				ID  string `json:"id"`
			}
			c, cancel := ctx()
			defer cancel()
			if err := g.client().JSON(c, "POST", "/v1/keys/"+pos[0]+"/rotate", map[string]string{"grace": *grace}, &out); err != nil {
				die("%v", err)
			}
			key, id = out.Key, out.ID
		}
		fmt.Printf("%s\n\nnew key id: %s (replaces %s). Shown ONCE.\n", key, id, pos[0])
	default:
		usage()
		os.Exit(2)
	}
}

func statusOf(r host.KeyRecord) string {
	if err := r.Active(time.Now()); err != nil {
		return strings.TrimPrefix(err.Error(), "key ")
	}
	return "active"
}

// ---- db ----

func cmdDB(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("db", flag.ExitOnError)
	g.bind(fs)
	pos := parse(fs, args)
	c, cancel := ctx()
	defer cancel()
	switch sub {
	case "create":
		if len(pos) != 1 {
			die("usage: redx db create NAME")
		}
		if err := g.client().JSON(c, "POST", "/v1/dbs", map[string]string{"name": pos[0]}, nil); err != nil {
			die("%v", err)
		}
		fmt.Println("created database", pos[0])
	case "list":
		var out struct {
			DBs []string `json:"dbs"`
		}
		if err := g.client().JSON(c, "GET", "/v1/dbs", nil, &out); err != nil {
			die("%v", err)
		}
		for _, d := range out.DBs {
			fmt.Println(d)
		}
	default:
		usage()
		os.Exit(2)
	}
}

// ---- data ----

func cmdData(cmd string, args []string) {
	var g globals
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	g.bind(fs)
	ttl := fs.Int("ttl", 0, "set: TTL seconds")
	sliding := fs.Bool("sliding", false, "set: sliding TTL")
	resolve := fs.Bool("resolve", false, "get: follow embedded links (@>) and show their targets")
	depth := fs.Int("depth", 0, "get --resolve: maximum link depth (0 = server default)")
	pos := parse(fs, args)
	c, cancel := ctx()
	defer cancel()
	cl := g.client()
	switch cmd {
	case "get":
		if len(pos) != 1 {
			die("usage: redx get PATH")
		}
		get := func() (string, error) { return cl.Get(c, g.db, pos[0]) }
		if *resolve {
			get = func() (string, error) { return cl.GetResolved(c, dbOrDefault(&g), pos[0], *depth) }
		}
		v, err := get()
		if err != nil {
			die("%v", err)
		}
		fmt.Println(v)
	case "set":
		if len(pos) < 2 {
			die("usage: redx set PATH VALUE   (VALUE '-' reads stdin)")
		}
		val := strings.Join(pos[1:], " ")
		if val == "-" {
			b, _ := io.ReadAll(os.Stdin)
			val = string(b)
		}
		if err := cl.Put(c, g.db, pos[0], val, *ttl, *sliding); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "delete", "del":
		if len(pos) != 1 {
			die("usage: redx delete PATH")
		}
		if err := cl.Delete(c, g.db, pos[0]); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "query":
		if len(pos) < 1 {
			die("usage: redx query 'QUERY'")
		}
		res, err := cl.Query(c, g.db, strings.Join(pos, " "))
		if err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(res)
			return
		}
		for _, r := range res {
			fmt.Println(r)
		}
	case "stats":
		p := "/v1/stats"
		if g.db != "" {
			p = "/v1/db/" + g.db + "/stats"
		}
		var out interface{}
		if err := cl.JSON(c, "GET", p, nil, &out); err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	case "subscribe":
		if len(pos) != 1 {
			die("usage: redx subscribe PATH")
		}
		subscribe(g, pos[0])
	}
}

// ---- node ----

func cmdNode(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("node "+sub, flag.ExitOnError)
	g.bind(fs)
	token := fs.String("token", "", "join token issued by the leader (`redx node token`)")
	advertise := fs.String("advertise", "", "URL other nodes use to reach THIS node, e.g. http://192.168.1.20:7777")
	nodeID := fs.String("node-id", "", "node id (default: persisted id in the data dir)")
	ttl := fs.Int("ttl", 900, "token: lifetime in seconds")
	local := fs.String("local", "", "join: instead of editing the data dir, tell the running local redxd (use with --key of the local node)")
	pos := parse(fs, args)
	switch sub {
	case "token":
		var out struct {
			Token   string `json:"token"`
			Expires string `json:"expires"`
		}
		c, cancel := ctx()
		defer cancel()
		if err := g.client().JSON(c, "POST", "/v1/cluster/join-token", map[string]int{"ttl_seconds": *ttl}, &out); err != nil {
			die("%v", err)
		}
		fmt.Printf("%s\n\nsingle-use join token, expires %s.\nOn the new node:  redx node join %s --token <token> --advertise http://<this-node-ip>:7777\n", out.Token, out.Expires, g.addr)
	case "join":
		if len(pos) != 1 || *token == "" {
			die("usage: redx node join LEADER_ADDR --token T [--advertise URL] [--data-dir DIR] [--pin SHA256]")
		}
		leader := pos[0]
		if !strings.Contains(leader, "://") {
			leader = "http://" + leader
		}
		if *local != "" {
			c, cancel := ctx()
			defer cancel()
			loc := client.New(*local, g.key, g.pin, g.insecure)
			body := map[string]string{"url": leader, "join_token": *token, "pin_sha256": g.pin}
			if err := loc.JSON(c, "POST", "/v1/cluster/leader", body, nil); err != nil {
				die("%v", err)
			}
			fmt.Println("local node is now a follower of", leader)
			return
		}
		id := *nodeID
		if id == "" {
			id = readOrMakeNodeID(g.dataDir)
		}
		c, cancel := ctx()
		defer cancel()
		cl := client.New(leader, "", g.pin, g.insecure)
		jr, err := host.JoinLeader(c, cl.HTTP, leader, *token, id, *advertise)
		if err != nil {
			die("%v", err)
		}
		if err := host.WriteJoinedState(g.dataDir, leader, jr.NodeSecret, g.pin, *advertise); err != nil {
			die("%v", err)
		}
		// The leader registered this node under `id`; make the daemon use the same id.
		_ = os.MkdirAll(g.dataDir, 0o700)
		if err := os.WriteFile(g.dataDir+string(os.PathSeparator)+"node.id", []byte(id+"\n"), 0o600); err != nil {
			die("%v", err)
		}
		fmt.Printf("joined cluster led by %s as node %q. Restart redxd on this machine to start replicating (data dir: %s).\n", leader, id, g.dataDir)
	case "status":
		var cs host.ClusterStatus
		c, cancel := ctx()
		defer cancel()
		if err := g.client().JSON(c, "GET", "/v1/cluster/status", nil, &cs); err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(cs)
			return
		}
		fmt.Printf("node %s  role=%s  offset=%d (log from %d)  replication-id=%s\n", cs.NodeID, cs.Role, cs.Offset, cs.FirstOffset, cs.ReplID)
		if cs.LeaderURL != "" {
			fmt.Printf("leader: %s   last sync: %s   snapshots installed: %d\n", cs.LeaderURL, cs.LastSync, cs.Snapshots)
		}
		if cs.LastError != "" {
			fmt.Println("last error:", cs.LastError)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NODE\tROLE\tADDR\tHEALTHY\tACKED\tLAG\tLAST SEEN")
		for _, m := range cs.Members {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%d\t%d\t%s\n", m.ID, m.Role, m.Addr, m.Healthy, m.Acked, m.Lag, m.LastSeen.Format(time.RFC3339))
		}
		tw.Flush()
	case "promote":
		c, cancel := ctx()
		defer cancel()
		var out map[string]interface{}
		if err := g.client().JSON(c, "POST", "/v1/cluster/promote", nil, &out); err != nil {
			die("%v", err)
		}
		fmt.Printf("promoted: %v\nWARNING: acknowledged writes the old leader had not replicated are lost. Now repoint the other nodes: redx node repoint <this-addr> --token <token from `redx node token`>\n", out)
	case "repoint":
		if len(pos) != 1 {
			die("usage: redx node repoint NEW_LEADER_ADDR --token T   (run against the node to be repointed via --addr/--key)")
		}
		nl := pos[0]
		if !strings.Contains(nl, "://") {
			nl = "http://" + nl
		}
		c, cancel := ctx()
		defer cancel()
		if err := g.client().JSON(c, "POST", "/v1/cluster/leader", map[string]string{"url": nl, "join_token": *token}, nil); err != nil {
			die("%v", err)
		}
		fmt.Println("node now follows", nl)
	default:
		usage()
		os.Exit(2)
	}
}

func readOrMakeNodeID(dir string) string {
	p := dir + string(os.PathSeparator) + "node.id"
	if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b))
	}
	_ = os.MkdirAll(dir, 0o700)
	id := fmt.Sprintf("node-%x", time.Now().UnixNano()&0xffffffffffff)
	_ = os.WriteFile(p, []byte(id+"\n"), 0o600)
	return id
}

// ---- backup / restore ----

func cmdBackup(args []string) {
	var g globals
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	g.bind(fs)
	out := fs.String("out", "", "output file (.tgz)")
	force := fs.Bool("force", false, "offline: copy even if redxd holds the data dir (may be inconsistent)")
	parse(fs, args)
	if *out == "" {
		*out = "redx-backup-" + time.Now().UTC().Format("20060102-150405") + ".tgz"
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		die("%v", err)
	}
	defer f.Close()
	if g.offline {
		if err := host.OfflineBackup(g.dataDir, f, os.Getenv("REDX_KEYSTORE_PASSPHRASE"), *force); err != nil {
			os.Remove(*out)
			die("%v", err)
		}
	} else {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		st, _, body, err := g.client().Raw(c, "GET", "/v1/admin/backup", nil, nil)
		if err != nil || st != 200 {
			os.Remove(*out)
			die("online backup failed (status %d): %v %s", st, err, strings.TrimSpace(string(body)))
		}
		if _, err := f.Write(body); err != nil {
			die("%v", err)
		}
	}
	fmt.Println("backup written to", *out)
}

func cmdRestore(args []string) {
	var g globals
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	g.bind(fs)
	force := fs.Bool("force", false, "move existing databases aside and restore over them")
	pos := parse(fs, args)
	if len(pos) != 1 {
		die("usage: redx restore FILE [--data-dir DIR] [--force]   (stop redxd first)")
	}
	f, err := os.Open(pos[0])
	if err != nil {
		die("%v", err)
	}
	defer f.Close()
	moved, err := host.OfflineRestore(g.dataDir, f, os.Getenv("REDX_KEYSTORE_PASSPHRASE"), *force)
	if err != nil {
		die("%v", err)
	}
	fmt.Println("restored into", g.dataDir)
	if moved != "" {
		fmt.Println("previous databases moved to", moved)
	}
	fmt.Println("Start redxd again. In a cluster, followers will full-resync from this node.")
}

var _ = signal.Notify

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
