package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"redx/host/client"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error { *l = append(*l, s); return nil }

func dbOrDefault(g *globals) string {
	if g.db == "" {
		return "default"
	}
	return g.db
}

func readArgOrStdin(p string) string {
	var b []byte
	var err error
	if p == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(p)
	}
	if err != nil {
		die("%v", err)
	}
	return string(b)
}

// ---- idx ----

func cmdIdx(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("idx "+sub, flag.ExitOnError)
	g.bind(fs)
	mode := fs.String("mode", "", "query mode: exact | prefix | range | text | fuzzy (default: inferred from the other flags)")
	field := fs.String("field", "", "query: field to search (required)")
	value := fs.String("value", "", "query: exact value / prefix")
	vtype := fs.String("vtype", "auto", "query: type of --value for exact: auto | string | number | bool")
	lo := fs.String("lo", "", "query range: lower bound")
	hi := fs.String("hi", "", "query range: upper bound")
	loEx := fs.Bool("lo-excl", false, "range: exclude the lower bound")
	hiEx := fs.Bool("hi-excl", false, "range: exclude the upper bound")
	q := fs.String("q", "", "query text/fuzzy: words")
	var terms listFlag
	fs.Var(&terms, "term", "query text: a term (repeatable)")
	op := fs.String("op", "", "query text: and | or (fuzzy defaults to or)")
	limit := fs.Int("limit", 100, "query: maximum hits")
	rec := fs.Bool("record", false, "query: include each matching record")
	pos := parse(fs, args)
	c, cancel := ctx()
	defer cancel()
	cl := g.client()
	db := dbOrDefault(&g)
	switch sub {
	case "query":
		if len(pos) != 2 {
			die("usage: redx idx query SET INDEX --field F [--value V | --lo N --hi N | --q 'words'] [--mode M] [--op and|or] [--limit N] [--record]")
		}
		m := *mode
		if m == "" {
			switch {
			case *lo != "" || *hi != "":
				m = "range"
			case *q != "" || len(terms) > 0:
				m = "text"
			case *value != "":
				m = "exact"
			default:
				die("give --mode, or one of --value, --lo/--hi, --q/--term")
			}
		}
		p := url.Values{"mode": {m}, "field": {*field}, "limit": {strconv.Itoa(*limit)}}
		for k, v := range map[string]string{"value": *value, "lo": *lo, "hi": *hi, "q": *q, "op": *op} {
			if v != "" {
				p.Set(k, v)
			}
		}
		if *vtype != "auto" {
			p.Set("vtype", *vtype)
		}
		for _, t := range terms {
			p.Add("term", t)
		}
		if *loEx {
			p.Set("lo_excl", "1")
		}
		if *hiEx {
			p.Set("hi_excl", "1")
		}
		if *rec {
			p.Set("record", "1")
		}
		res, err := cl.IdxQuery(c, db, pos[0], pos[1], p)
		if err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(res)
			return
		}
		if res.Note != "" {
			fmt.Fprintln(os.Stderr, "note:", res.Note)
		}
		for _, h := range res.Hits {
			fmt.Printf("%s\t%s\t%s\n", h.Path, h.Field, h.Value)
			if h.Record != "" {
				fmt.Println("    " + strings.ReplaceAll(strings.TrimSpace(h.Record), "\n", "\n    "))
			}
		}
		fmt.Fprintf(os.Stderr, "%d hit(s)\n", res.Count)
	case "list":
		var out struct {
			Sets []struct {
				Set     string `json:"set"`
				Indexes []struct {
					Name, File, Kind string
					Fields           []string
					Records, Terms   int
				} `json:"indexes"`
			} `json:"sets"`
		}
		if err := cl.IdxList(c, db, &out); err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(out)
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SET\tINDEX\tKIND\tFILE\tFIELDS\tRECORDS")
		for _, s := range out.Sets {
			for _, ix := range s.Indexes {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", s.Set, ix.Name, ix.Kind, ix.File, strings.Join(ix.Fields, ","), ix.Records)
			}
		}
		tw.Flush()
	case "define":
		if len(pos) != 2 {
			die("usage: redx idx define SET FILE|-    (FILE holds a .red-xai-db-idx declaration)")
		}
		if err := cl.IdxDefine(c, db, pos[0], readArgOrStdin(pos[1])); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "drop":
		if len(pos) != 1 {
			die("usage: redx idx drop SET")
		}
		if err := cl.IdxDrop(c, db, pos[0]); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "rebuild":
		if len(pos) != 1 {
			die("usage: redx idx rebuild SET")
		}
		if err := cl.IdxRebuild(c, db, pos[0]); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "check":
		if len(pos) != 1 {
			die("usage: redx idx check SET")
		}
		var out interface{}
		if err := cl.IdxCheck(c, db, pos[0], &out); err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	default:
		die("unknown idx command %q (query, list, define, drop, rebuild, check)", sub)
	}
}

// ---- pack ----

func cmdPack(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("pack "+sub, flag.ExitOnError)
	g.bind(fs)
	out := fs.String("out", "", "get: output file (default: the file's name in the current directory; '-' = stdout)")
	force := fs.Bool("force", false, "get: overwrite an existing output file")
	rng := fs.String("range", "", "get: byte range, e.g. 0-99, 100-, -500 (or a full 'bytes=...' value)")
	offset := fs.Int64("offset", -1, "get: first byte (with --length)")
	length := fs.Int64("length", -1, "get: number of bytes (with --offset)")
	deep := fs.Bool("deep", false, "fsck: read and hash every chunk")
	noVerify := fs.Bool("no-verify", false, "put: skip sending the SHA-256 for server-side verification")
	pos := parse(fs, args)
	// pack transfers can be long: no 60 s deadline, Ctrl-C cancels
	ictx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cl := g.client()
	db := dbOrDefault(&g)
	switch sub {
	case "put":
		if len(pos) < 2 || len(pos) > 3 {
			die("usage: redx pack put PACK LOCALFILE|- [REMOTE/PATH]")
		}
		remote := path.Base(filepath.ToSlash(pos[1]))
		if len(pos) == 3 {
			remote = pos[2]
		} else if pos[1] == "-" {
			die("reading stdin needs a remote path: redx pack put PACK - REMOTE/PATH")
		}
		var r io.Reader = os.Stdin
		size, sum := int64(-1), ""
		if pos[1] != "-" {
			f, err := os.Open(pos[1])
			if err != nil {
				die("%v", err)
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil || st.IsDir() {
				die("%s is not a regular file", pos[1])
			}
			size = st.Size()
			if !*noVerify {
				s, _, err := client.SHA256Hex(f)
				if err != nil {
					die("%v", err)
				}
				sum = s
				if _, err := f.Seek(0, 0); err != nil {
					die("%v", err)
				}
			}
			r = f
		}
		info, err := cl.PackPut(ictx, db, pos[0], remote, r, size, sum)
		if err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(info)
			return
		}
		fmt.Printf("stored %s (%d bytes, sha256 %s)\n", info.Path, info.Size, info.SHA256)
	case "get":
		if len(pos) != 2 {
			die("usage: redx pack get PACK REMOTE/PATH [--out FILE|-] [--range a-b | --offset N --length N]")
		}
		hdr := *rng
		if *offset >= 0 {
			if *length > 0 {
				hdr = fmt.Sprintf("%d-%d", *offset, *offset+*length-1)
			} else {
				hdr = fmt.Sprintf("%d-", *offset)
			}
		}
		if hdr != "" && !strings.HasPrefix(hdr, "bytes=") {
			hdr = "bytes=" + hdr
		}
		resp, err := cl.PackGet(ictx, db, pos[0], pos[1], hdr)
		if err != nil {
			die("%v", err)
		}
		defer resp.Body.Close()
		dest := *out
		if dest == "" {
			dest = path.Base(pos[1])
		}
		var w io.Writer = os.Stdout
		var tmp *os.File
		if dest != "-" {
			if _, err := os.Stat(dest); err == nil && !*force {
				die("%s exists (use --force to overwrite or --out to choose another file)", dest)
			}
			if tmp, err = os.CreateTemp(filepath.Dir(dest), ".redx-get-*"); err != nil {
				die("%v", err)
			}
			defer os.Remove(tmp.Name())
			w = tmp
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(w, h), resp.Body)
		if err == nil && resp.ContentLength >= 0 && n != resp.ContentLength {
			err = fmt.Errorf("short transfer: got %d of %d bytes", n, resp.ContentLength)
		}
		if err != nil {
			die("download failed (nothing was kept): %v", err)
		}
		partial := resp.StatusCode == 206
		verified := ""
		if resp.Header.Get("X-Redx-Encrypted") != "" {
			// the ETag is the hash of the stored ciphertext; the AES-GCM stream was authenticated by the server
			fmt.Fprintln(os.Stderr, "note: encrypted pack: decrypted and authenticated by the server (no plaintext hash to compare)")
		} else if !partial {
			want := strings.Trim(strings.TrimPrefix(strings.Trim(resp.Header.Get("ETag"), `"`), "sha256-"), `"`)
			if want == "" || len(want) != 64 {
				die("server sent no SHA-256 ETag; cannot verify (nothing was kept)")
			}
			if hex.EncodeToString(h.Sum(nil)) != want {
				die("SHA-256 mismatch: the downloaded bytes are not the stored file (nothing was kept)")
			}
			verified = ", sha256 verified"
		} else {
			fmt.Fprintln(os.Stderr, "note: ranged read; each chunk was verified by the server, the whole-file hash was not checked")
		}
		if tmp != nil {
			if err := tmp.Close(); err != nil {
				die("%v", err)
			}
			if err := os.Rename(tmp.Name(), dest); err != nil {
				die("%v", err)
			}
			fmt.Fprintf(os.Stderr, "wrote %s (%d bytes%s)\n", dest, n, verified)
		}
	case "ls":
		folder := ""
		if len(pos) == 2 {
			folder = pos[1]
		} else if len(pos) != 1 {
			die("usage: redx pack ls PACK [FOLDER]")
		}
		es, err := cl.PackLs(ictx, db, pos[0], strings.Trim(folder, "/"))
		if err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(es)
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		for _, e := range es {
			if e.IsDir {
				fmt.Fprintf(tw, "%s/\t-\t\n", e.Name)
			} else {
				fmt.Fprintf(tw, "%s\t%d\t%.12s\n", e.Name, e.Size, e.SHA256)
			}
		}
		tw.Flush()
	case "stat":
		if len(pos) != 2 {
			die("usage: redx pack stat PACK PATH")
		}
		f, err := cl.PackStat(ictx, db, pos[0], pos[1])
		if err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(f, "", "  ")
		fmt.Println(string(b))
	case "rm":
		if len(pos) != 2 {
			die("usage: redx pack rm PACK PATH")
		}
		if err := cl.PackRm(ictx, db, pos[0], pos[1]); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "gc", "fsck":
		if len(pos) != 1 {
			die("usage: redx pack %s PACK%s", sub, map[string]string{"gc": "", "fsck": " [--deep]"}[sub])
		}
		var res interface{}
		var err error
		if sub == "gc" {
			err = cl.JSON(ictx, "POST", "/v1/db/"+db+"/pack/"+url.PathEscape(pos[0])+"/gc", nil, &res)
		} else {
			q := ""
			if *deep {
				q = "?deep=1"
			}
			err = cl.JSON(ictx, "GET", "/v1/db/"+db+"/pack/"+url.PathEscape(pos[0])+"/fsck"+q, nil, &res)
		}
		if err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	case "list":
		ps, err := cl.PackList(ictx, db)
		if err != nil {
			die("%v", err)
		}
		for _, p := range ps {
			fmt.Println(p)
		}
	default:
		die("unknown pack command %q (put, get, ls, stat, rm, list, gc, fsck)", sub)
	}
}

// ---- pulse ----

func cmdPulse(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("pulse "+sub, flag.ExitOnError)
	g.bind(fs)
	topic := fs.String("topic", "", "subscribe: topic pattern (\"\" = all, \"prefix*\", or exact)")
	changes := fs.Bool("changes", false, "subscribe: also deliver raw change.* events")
	pos := parse(fs, args)
	c, cancel := ctx()
	defer cancel()
	cl := g.client()
	db := dbOrDefault(&g)
	switch sub {
	case "put":
		if len(pos) != 1 {
			die("usage: redx pulse put FILE|-    (FILE holds .red-xai-db-pulse rules; replaces the current set)")
		}
		if err := cl.PulsePut(c, db, readArgOrStdin(pos[0])); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "get":
		var out interface{}
		if err := cl.PulseGet(c, db, &out); err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	case "rm":
		if err := cl.PulseDelete(c, db); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "subscribe":
		q := url.Values{}
		if *topic != "" {
			q.Set("topic", *topic)
		}
		if *changes {
			q.Set("changes", "1")
		}
		ictx, stop := signal.NotifyContext(c, os.Interrupt)
		defer stop()
		conn, resp, err := cl.Dial(ictx, "/v1/db/"+db+"/pulse/subscribe", q)
		if err != nil {
			if resp != nil {
				die("subscribe refused: %s", resp.Status)
			}
			die("%v", err)
		}
		defer conn.Close()
		go func() { <-ictx.Done(); conn.Close() }()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if ictx.Err() == nil {
					fmt.Fprintln(os.Stderr, "connection closed:", strings.TrimSpace(err.Error()))
				}
				return
			}
			fmt.Println(string(msg))
		}
	default:
		die("unknown pulse command %q (put, get, rm, subscribe)", sub)
	}
}
