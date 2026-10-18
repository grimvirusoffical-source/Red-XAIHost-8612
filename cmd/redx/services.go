package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"redx/host/client"
)

// ---- sql ----

func cmdSQL(args []string) {
	var g globals
	fs := flag.NewFlagSet("sql", flag.ExitOnError)
	g.bind(fs)
	var params listFlag
	fs.Var(&params, "param", "value for a ? placeholder, in order (repeatable); numbers, true/false and null are typed, anything else is a string; use --sparam to force a string")
	var sparams listFlag
	fs.Var(&sparams, "sparam", "string value for the next placeholder (repeatable, after any --param)")
	explain := fs.Bool("explain", false, "show the plan instead of running the statement")
	pos := parse(fs, args)
	if len(pos) != 1 {
		die(`usage: redx sql 'SELECT Name FROM players WHERE Level > ?' --param 10 [--explain] [--json]   (use - to read the statement from stdin)`)
	}
	q := pos[0]
	if q == "-" {
		q = readArgOrStdin("-")
	}
	var ps []interface{}
	for _, p := range params {
		ps = append(ps, typedParam(p))
	}
	for _, p := range sparams {
		ps = append(ps, p)
	}
	c, cancel := ctx()
	defer cancel()
	res, err := g.client().SQL(c, dbOrDefault(&g), q, ps, *explain)
	if err != nil {
		die("%v", err)
	}
	if g.asJSON {
		json.NewEncoder(os.Stdout).Encode(res)
		return
	}
	switch res.Kind {
	case "select":
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, strings.Join(res.Columns, "\t"))
		for _, r := range res.RowsBox {
			fmt.Fprintln(tw, strings.Join(r, "\t"))
		}
		tw.Flush()
		fmt.Fprintf(os.Stderr, "%d row(s)  access=%s scanned=%d\n", res.Count, res.Access, res.Scanned)
	case "explain":
		fmt.Println(strings.Join(res.Plan, "\n"))
	default:
		fmt.Printf("%s: %d record(s) affected", res.Kind, res.Affected)
		if len(res.Cascaded) > 0 {
			fmt.Printf(" (+%d cascaded)", len(res.Cascaded))
		}
		fmt.Println()
	}
}

func typedParam(s string) interface{} {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null", "NELL":
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// ---- media ----

func cmdMedia(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("media "+sub, flag.ExitOnError)
	g.bind(fs)
	out := fs.String("out", "", "get: output file (default: the item's name; '-' = stdout)")
	force := fs.Bool("force", false, "get: overwrite an existing output file")
	rng := fs.String("range", "", "get: byte range, e.g. 0-99, 100-, -500")
	typ := fs.String("type", "", "put: declared type (image/png, audio/mpeg, video/mp4, or just image|audio|video); refused if the bytes disagree")
	deep := fs.Bool("deep", false, "fsck: read every chunk")
	noVerify := fs.Bool("no-verify", false, "put: skip sending the SHA-256 for server-side verification")
	pos := parse(fs, args)
	ictx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cl := g.client()
	db := dbOrDefault(&g)
	switch sub {
	case "put":
		if len(pos) < 2 || len(pos) > 3 {
			die("usage: redx media put LIB LOCALFILE|- [NAME] [--type image/png]")
		}
		name := filepath.Base(pos[1])
		if len(pos) == 3 {
			name = pos[2]
		} else if pos[1] == "-" {
			die("reading stdin needs a name: redx media put LIB - NAME")
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
		declared := *typ
		m, err := cl.MediaPut(ictx, db, pos[0], name, r, size, declared, sum)
		if err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(m)
			return
		}
		fmt.Printf("stored %s: %s %s, %d bytes%s, sha256 %s\n", m.Name, m.Type, m.MIME, m.Size, dims(m), m.SHA256)
	case "get":
		if len(pos) != 2 {
			die("usage: redx media get LIB NAME [--out FILE|-] [--range a-b]")
		}
		hdr := *rng
		if hdr != "" && !strings.HasPrefix(hdr, "bytes=") {
			hdr = "bytes=" + hdr
		}
		resp, err := cl.MediaGet(ictx, db, pos[0], pos[1], hdr)
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
		verified := ""
		if resp.StatusCode == 200 {
			want := strings.TrimPrefix(strings.Trim(resp.Header.Get("ETag"), `"`), "sha256-")
			if len(want) != 64 || hex.EncodeToString(h.Sum(nil)) != want {
				die("SHA-256 mismatch or missing ETag: the downloaded bytes are not verified (nothing was kept)")
			}
			verified = ", sha256 verified"
		} else {
			fmt.Fprintln(os.Stderr, "note: ranged read; each chunk was verified by the server, the whole-item hash was not checked")
		}
		if tmp != nil {
			if err := tmp.Close(); err != nil {
				die("%v", err)
			}
			if err := os.Rename(tmp.Name(), dest); err != nil {
				die("%v", err)
			}
			fmt.Fprintf(os.Stderr, "wrote %s (%d bytes, %s%s)\n", dest, n, resp.Header.Get("Content-Type"), verified)
		}
	case "ls":
		if len(pos) != 1 {
			die("usage: redx media ls LIB")
		}
		items, err := cl.MediaList(ictx, db, pos[0])
		if err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(items)
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		for _, m := range items {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%.12s\n", m.Name, m.MIME, m.Size, strings.TrimSpace(dims(&m)), m.SHA256)
		}
		tw.Flush()
	case "stat":
		if len(pos) != 2 {
			die("usage: redx media stat LIB NAME")
		}
		m, err := cl.MediaStat(ictx, db, pos[0], pos[1])
		if err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		fmt.Println(string(b))
	case "rm":
		if len(pos) != 2 {
			die("usage: redx media rm LIB NAME")
		}
		if err := cl.MediaRm(ictx, db, pos[0], pos[1]); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "list":
		libs, err := cl.MediaLibs(ictx, db)
		if err != nil {
			die("%v", err)
		}
		for _, l := range libs {
			fmt.Println(l)
		}
	case "fsck":
		if len(pos) != 1 {
			die("usage: redx media fsck LIB [--deep]")
		}
		q := ""
		if *deep {
			q = "?deep=1"
		}
		var res interface{}
		if err := cl.JSON(ictx, "GET", "/v1/db/"+db+"/media/"+pos[0]+"/fsck"+q, nil, &res); err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	default:
		die("unknown media command %q (put, get, ls, stat, rm, list, fsck)", sub)
	}
}

func dims(m *client.MediaInfo) string {
	switch {
	case m.Width > 0 && m.Height > 0:
		return fmt.Sprintf(" %dx%d", m.Width, m.Height)
	case m.DurationMS > 0:
		return fmt.Sprintf(" %.1fs", float64(m.DurationMS)/1000)
	}
	return ""
}

// ---- code ----

func cmdCode(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("code "+sub, flag.ExitOnError)
	g.bind(fs)
	pos := parse(fs, args)
	c, cancel := ctx()
	defer cancel()
	cl := g.client()
	db := dbOrDefault(&g)
	switch sub {
	case "ls":
		var out struct {
			Scripts []struct {
				File   string   `json:"file"`
				Name   string   `json:"name"`
				Params []string `json:"params"`
				Uses   []string `json:"uses"`
			} `json:"scripts"`
		}
		if err := cl.CodeList(c, db, &out); err != nil {
			die("%v", err)
		}
		if g.asJSON {
			json.NewEncoder(os.Stdout).Encode(out)
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		for _, s := range out.Scripts {
			fmt.Fprintf(tw, "%s\t(%s)\tuses %s\t%s\n", s.Name, strings.Join(s.Params, ", "), strings.Join(s.Uses, ","), s.File)
		}
		tw.Flush()
	case "define":
		if len(pos) != 2 {
			die("usage: redx code define FILENAME SOURCE|-    (SOURCE holds a .red-xai-code document)")
		}
		if err := cl.CodeDefine(c, db, pos[0], readArgOrStdin(pos[1])); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "rm":
		if len(pos) != 1 {
			die("usage: redx code rm FILENAME")
		}
		if err := cl.CodeRemove(c, db, pos[0]); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "run":
		if len(pos) < 1 {
			die("usage: redx code run SCRIPT [ARG ...]   (numbers, true/false, null are typed; the rest are strings)")
		}
		var as []interface{}
		for _, a := range pos[1:] {
			as = append(as, typedParam(a))
		}
		var out interface{}
		if err := cl.CodeRun(c, db, pos[0], as, &out); err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	default:
		die("unknown code command %q (ls, define, rm, run)", sub)
	}
}

// ---- sec ----

func cmdSec(args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	var g globals
	fs := flag.NewFlagSet("sec "+sub, flag.ExitOnError)
	g.bind(fs)
	pos := parse(fs, args)
	c, cancel := ctx()
	defer cancel()
	cl := g.client()
	db := dbOrDefault(&g)
	switch sub {
	case "show":
		var out interface{}
		if err := cl.SecGet(c, db, &out); err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	case "put":
		if len(pos) != 1 {
			die("usage: redx sec put FILE|-    (FILE holds a .red-xai-sec policy)")
		}
		if err := cl.SecPut(c, db, readArgOrStdin(pos[0])); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	case "rm":
		if err := cl.SecDelete(c, db); err != nil {
			die("%v", err)
		}
		fmt.Println("ok")
	default:
		die("unknown sec command %q (show, put, rm)", sub)
	}
}
