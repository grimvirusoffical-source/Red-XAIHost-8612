package engineadapter_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"redx/host"
)

// req: args are an optional []byte body (first) followed by header name/value string pairs.
func req(t *testing.T, method, url, key string, args ...interface{}) (int, []byte, http.Header) {
	t.Helper()
	var body []byte
	var hdr []string
	for i, a := range args {
		switch v := a.(type) {
		case []byte:
			if i == 0 {
				body = v
			}
		case string:
			hdr = append(hdr, v)
		}
	}
	r, _ := http.NewRequest(method, url, bytes.NewReader(body))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func mkKey(t *testing.T, n *node, scope, db, prefix string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"scope": scope, "db": db, "prefix": prefix})
	st, b, _ := req(t, "POST", n.url+"/v1/keys", n.key, body, "Content-Type", "application/json")
	if st != 201 {
		t.Fatalf("create key %d %s", st, b)
	}
	var m map[string]interface{}
	json.Unmarshal(b, &m)
	return m["key"].(string)
}

const idxDecl = `{PlayerByName[]} = [
    {File[]}   = "players.red-xai.db",
    {Box[]}    = "Player",
    {Fields[]} = ;["Name"]:,
    {Kind[]}   = "exact",
    {Fold[]}   = TRUE
];
{PlayerByPrefix[]} = [
    {File[]}   = "players.red-xai.db",
    {Box[]}    = "Player",
    {Fields[]} = ;["Name"]:,
    {Kind[]}   = "prefix",
    {Fold[]}   = TRUE
];
{PlayerLevel[]} = [
    {File[]}   = "players.red-xai.db",
    {Box[]}    = "Player",
    {Fields[]} = ;["Level"]:,
    {Kind[]}   = "range"
];
{PlayerBio[]} = [
    {File[]}   = "players.red-xai.db",
    {Box[]}    = "Player",
    {Fields[]} = ;["Bio"]:,
    {Kind[]}   = "text"
];
`

func putPlayer(t *testing.T, n *node, id int, name string, level int, bio string) {
	t.Helper()
	body := fmt.Sprintf(`[ {Name[]} = %q, {Level[]} = %d, {Bio[]} = %q ]`, name, level, bio)
	if st, b, _ := req(t, "PUT", fmt.Sprintf("%s/v1/path/players.red-xai.db:Player[ID=%d]", n.url, id), n.key, []byte(body)); st != 200 {
		t.Fatalf("put player %d: %d %s", id, st, b)
	}
}

func hitPaths(t *testing.T, b []byte) []string {
	t.Helper()
	var r struct {
		Hits []struct{ Path, Field, Value, Record string } `json:"hits"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("bad json %s", b)
	}
	var out []string
	for _, h := range r.Hits {
		out = append(out, h.Path)
	}
	return out
}

func eq(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestIndexAPI(t *testing.T) {
	n := start(t, nil)
	D := n.url + "/v1/db/default"
	// records written BEFORE the index exists are picked up by the initial build
	putPlayer(t, n, 1, "Ann", 5, "red dragon slayer")
	putPlayer(t, n, 2, "Annabel", 12, "blue mage")
	if st, b, _ := req(t, "PUT", D+"/idx/players", n.key, []byte("not a declaration {{")); st != 400 {
		t.Fatalf("bad decl must be 400, got %d %s", st, b)
	}
	if st, b, _ := req(t, "PUT", D+"/idx/players", n.key, []byte(idxDecl)); st != 200 {
		t.Fatalf("define %d %s", st, b)
	}
	// ... and writes AFTER it go through the observer
	putPlayer(t, n, 3, "Bob", 12, "Red wizard")
	q := func(qs string) (int, []byte) {
		st, b, _ := req(t, "GET", D+"/idx/players/"+qs, n.key, nil)
		return st, b
	}
	if st, b := q("PlayerByName/query?mode=exact&field=Name&value=ann"); st != 200 || !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=1]") {
		t.Fatalf("exact (folded): %d %s", st, b)
	}
	if st, b := q("PlayerByPrefix/query?mode=prefix&field=Name&value=ANN"); st != 200 || !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=1]", "players.red-xai.db:Player[ID=2]") {
		t.Fatalf("prefix: %d %s", st, b)
	}
	if st, b := q("PlayerLevel/query?mode=range&field=Level&lo=10&hi=12"); st != 200 || !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=2]", "players.red-xai.db:Player[ID=3]") {
		t.Fatalf("range: %d %s", st, b)
	}
	if st, b := q("PlayerLevel/query?mode=range&field=Level&lo=5&lo_excl=1&limit=1"); st != 200 || !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=2]") {
		t.Fatalf("range excl+limit: %d %s", st, b)
	}
	if st, b := q("PlayerBio/query?mode=text&field=Bio&q=red+dragon"); st != 200 || !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=1]") {
		t.Fatalf("text and: %d %s", st, b)
	}
	st, b := q("PlayerBio/query?mode=fuzzy&field=Bio&q=red+mage")
	if st != 200 || !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=1]", "players.red-xai.db:Player[ID=2]", "players.red-xai.db:Player[ID=3]") || !strings.Contains(string(b), "no edit-distance") {
		t.Fatalf("fuzzy alias must be any-word match and say so: %d %s", st, b)
	}
	if st, b := q("PlayerBio/query?mode=text&field=Bio&q=red&record=1"); st != 200 || !strings.Contains(string(b), `"record"`) {
		t.Fatalf("record include: %d %s", st, b)
	}
	// field-level write re-indexes the record; delete removes it
	if st, _, _ := req(t, "PUT", D+"/path/players.red-xai.db:Player[ID=1].Name", n.key, []byte(`"Zed"`)); st != 200 {
		t.Fatal("field write")
	}
	if _, b := q("PlayerByName/query?mode=exact&field=Name&value=ann"); len(hitPaths(t, b)) != 0 {
		t.Fatalf("renamed record still indexed under old name: %s", b)
	}
	if _, b := q("PlayerByName/query?mode=exact&field=Name&value=zed"); !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=1]") {
		t.Fatalf("renamed record not found under new name: %s", b)
	}
	if st, _, _ := req(t, "DELETE", D+"/path/players.red-xai.db:Player[ID=3]", n.key, nil); st != 200 {
		t.Fatal("delete")
	}
	if _, b := q("PlayerLevel/query?mode=range&field=Level&lo=12&hi=12"); !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=2]") {
		t.Fatalf("deleted record still in index: %s", b)
	}
	// errors
	for _, c := range []struct {
		qs   string
		want int
	}{
		{"PlayerByName/query?mode=range&field=Name&lo=1", 400},    // wrong kind
		{"PlayerByName/query?mode=exact&field=Nope&value=x", 400}, // unknown field
		{"PlayerByName/query?field=Name&value=x", 400},            // no mode
		{"PlayerByName/query?mode=exact&value=x", 400},            // no field
		{"PlayerLevel/query?mode=range&field=Level&lo=abc", 400},  // bad number
		{"PlayerByName/query?mode=exact&field=Name&limit=0", 400}, // bad limit
		{"Nope/query?mode=exact&field=Name&value=x", 404},         // unknown index
		{"PlayerByName/query?mode=weird&field=Name&value=x", 400}, // bad mode
	} {
		if st, b := q(c.qs); st != c.want {
			t.Errorf("%s: got %d want %d (%s)", c.qs, st, c.want, b)
		}
	}
	if st, _, _ := req(t, "GET", D+"/idx/nosuch/PlayerByName/query?mode=exact&field=Name&value=x", n.key, nil); st != 404 {
		t.Errorf("unknown set must be 404, got %d", st)
	}
	// listing + check + rebuild
	if st, b, _ := req(t, "GET", D+"/idx", n.key, nil); st != 200 || !strings.Contains(string(b), "PlayerLevel") {
		t.Fatalf("list %d %s", st, b)
	}
	if st, b, _ := req(t, "GET", D+"/idx/players/check", n.key, nil); st != 200 || strings.Contains(string(b), `"stale":true`) {
		t.Fatalf("check %d %s", st, b)
	}
	if st, _, _ := req(t, "POST", D+"/idx/players/rebuild", n.key, nil); st != 200 {
		t.Fatal("rebuild")
	}
	// drop
	if st, _, _ := req(t, "DELETE", D+"/idx/players", n.key, nil); st != 200 {
		t.Fatal("drop")
	}
	if st, _, _ := req(t, "GET", D+"/idx/players/PlayerByName/query?mode=exact&field=Name&value=zed", n.key, nil); st != 404 {
		t.Fatal("dropped set must be gone")
	}
}

func TestIndexACLAndScopes(t *testing.T) {
	n := start(t, nil)
	D := n.url + "/v1/db/default"
	req(t, "PUT", D+"/idx/players", n.key, []byte(idxDecl))
	putPlayer(t, n, 1, "Ann", 5, "x")
	putPlayer(t, n, 2, "Ann", 6, "y")
	ro := mkKey(t, n, "read", "default", "")
	rw := mkKey(t, n, "write", "default", "")
	pre := mkKey(t, n, "read", "default", "players.red-xai.db:Player[ID=2]")
	other := mkKey(t, n, "admin", "otherdb", "")
	query := D + "/idx/players/PlayerByName/query?mode=exact&field=Name&value=ann"

	if st, _, _ := req(t, "GET", query, ""); st != 401 {
		t.Fatalf("no key: %d", st)
	}
	if st, _, _ := req(t, "GET", query, "rx_live_"+strings.Repeat("a", 43)); st != 401 {
		t.Fatalf("bad key: %d", st)
	}
	if st, b, _ := req(t, "GET", query, ro); st != 200 || len(hitPaths(t, b)) != 2 {
		t.Fatalf("read key: %d %s", st, b)
	}
	if st, b, _ := req(t, "GET", query, pre); st != 200 || !eq(hitPaths(t, b), "players.red-xai.db:Player[ID=2]") {
		t.Fatalf("prefix key must only see its record: %d %s", st, b)
	}
	if st, _, _ := req(t, "GET", query, other); st != 403 {
		t.Fatalf("key for another db: %d", st)
	}
	// definitions are admin-only, and never for prefix-restricted keys
	for _, k := range []string{ro, rw, pre} {
		if st, _, _ := req(t, "PUT", D+"/idx/x", k, []byte(idxDecl)); st != 403 {
			t.Fatalf("non-admin define: %d", st)
		}
		if st, _, _ := req(t, "POST", D+"/idx/players/rebuild", k, nil); st != 403 {
			t.Fatalf("non-admin rebuild: %d", st)
		}
	}
	if st, _, _ := req(t, "GET", D+"/idx", pre); st != 403 {
		t.Fatalf("prefix key must not list definitions: %d", st)
	}
	if st, _, _ := req(t, "GET", D+"/idx", ro); st != 200 {
		t.Fatalf("read key lists: %d", st)
	}
	prefAdmin := mkKey(t, n, "admin", "default", "players.red-xai.db:")
	if st, _, _ := req(t, "PUT", D+"/idx/x", prefAdmin, []byte(idxDecl)); st != 403 {
		t.Fatalf("prefix-restricted admin must not define: %d", st)
	}
}

func TestIndexSurvivesRestartAndTTLFiltering(t *testing.T) {
	dir := t.TempDir()
	n := start(t, func(c *host.Config, _ string) { c.DataDir = dir })
	D := n.url + "/v1/db/default"
	req(t, "PUT", D+"/idx/players", n.key, []byte(idxDecl))
	putPlayer(t, n, 1, "Ann", 5, "x")
	n.srv.Close()
	n.h.Close()
	n2 := start(t, func(c *host.Config, _ string) { c.DataDir = dir })
	n2.key = n.key
	putPlayer(t, n2, 2, "Ann", 6, "y") // write before the first query: index not open yet, must still be right
	st, b, _ := req(t, "GET", n2.url+"/v1/db/default/idx/players/PlayerByName/query?mode=exact&field=Name&value=ann", n2.key, nil)
	if st != 200 || len(hitPaths(t, b)) != 2 {
		t.Fatalf("after restart: %d %s", st, b)
	}
}

// ---- packs ----

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(7)).Read(b)
	return b
}

func TestPackAPI(t *testing.T) {
	n := start(t, nil)
	P := n.url + "/v1/db/default/pack/media"
	data := randBytes(2*1024*1024 + 12345) // three 1 MiB chunks
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])

	// upload with a wrong checksum stores nothing
	if st, b, _ := req(t, "PUT", P+"/file/a/big.bin", n.key, data, "X-Redx-SHA256", strings.Repeat("0", 64)); st != 422 {
		t.Fatalf("checksum mismatch must be 422, got %d %s", st, b)
	}
	if st, _, _ := req(t, "GET", P+"/stat/a/big.bin", n.key, nil); st != 404 {
		t.Fatalf("failed upload must leave nothing behind, stat=%d", st)
	}
	st, b, h := req(t, "PUT", P+"/file/a/big.bin", n.key, data, "X-Redx-SHA256", want)
	if st != 201 {
		t.Fatalf("upload %d %s", st, b)
	}
	var info host.PackFile
	json.Unmarshal(b, &info)
	if info.Size != int64(len(data)) || info.SHA256 != want || h.Get("ETag") != `"sha256-`+want+`"` {
		t.Fatalf("info %+v etag %s", info, h.Get("ETag"))
	}
	// full download is byte-identical
	st, got, h := req(t, "GET", P+"/file/a/big.bin", n.key, nil)
	if st != 200 || !bytes.Equal(got, data) || h.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("download: %d len=%d", st, len(got))
	}
	if st, _, h := req(t, "HEAD", P+"/file/a/big.bin", n.key, nil); st != 200 || h.Get("Content-Length") != fmt.Sprint(len(data)) {
		t.Fatalf("head: %d %v", st, h)
	}
	if st, _, _ := req(t, "GET", P+"/file/a/big.bin", n.key, nil, "If-None-Match", `"sha256-`+want+`"`); st != 304 {
		t.Fatalf("etag revalidation: %d", st)
	}
	// ranges, including ones crossing chunk boundaries
	n1 := int64(1 << 20)
	for _, c := range []struct {
		hdr      string
		from, to int64
	}{
		{"bytes=0-0", 0, 1},
		{"bytes=10-99", 10, 100},
		{"bytes=1048570-1048590", n1 - 6, n1 + 15},
		{fmt.Sprintf("bytes=%d-", 2*n1+5), 2*n1 + 5, int64(len(data))},
		{"bytes=-100", int64(len(data)) - 100, int64(len(data))},
		{fmt.Sprintf("bytes=%d-%d", n1-1, 2*n1), n1 - 1, 2*n1 + 1},
		{fmt.Sprintf("bytes=0-%d", len(data)+1000), 0, int64(len(data))},
	} {
		st, got, h := req(t, "GET", P+"/file/a/big.bin", n.key, nil, "Range", c.hdr)
		if st != 206 || !bytes.Equal(got, data[c.from:c.to]) {
			t.Errorf("%s: status %d len %d (want %d bytes)", c.hdr, st, len(got), c.to-c.from)
		}
		if cr := h.Get("Content-Range"); st == 206 && cr != fmt.Sprintf("bytes %d-%d/%d", c.from, c.to-1, len(data)) {
			t.Errorf("%s: Content-Range %q", c.hdr, cr)
		}
	}
	if st, _, h := req(t, "GET", P+"/file/a/big.bin", n.key, nil, "Range", fmt.Sprintf("bytes=%d-", len(data))); st != 416 || h.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(data)) {
		t.Errorf("unsatisfiable: %d %v", st, h)
	}
	if st, got, _ := req(t, "GET", P+"/file/a/big.bin", n.key, nil, "Range", "bytes=0-5,10-20"); st != 200 || len(got) != len(data) {
		t.Errorf("multi-range is ignored (full 200): %d", st)
	}
	if st, got, _ := req(t, "GET", P+"/file/a/big.bin", n.key, nil, "Range", "bytes=0-5", "If-Range", `"other"`); st != 200 || len(got) != len(data) {
		t.Errorf("stale If-Range gives the whole file: %d", st)
	}
	// ls / stat / list packs
	if st, b, _ := req(t, "GET", P+"/ls", n.key, nil); st != 200 || !strings.Contains(string(b), `"name":"a"`) {
		t.Fatalf("ls root %d %s", st, b)
	}
	if st, b, _ := req(t, "GET", P+"/ls/a", n.key, nil); st != 200 || !strings.Contains(string(b), "big.bin") {
		t.Fatalf("ls a %d %s", st, b)
	}
	if st, b, _ := req(t, "GET", n.url+"/v1/db/default/pack", n.key, nil); st != 200 || !strings.Contains(string(b), `"media"`) {
		t.Fatalf("list packs %d %s", st, b)
	}
	if st, _, _ := req(t, "GET", P+"/ls/nope", n.key, nil); st != 404 {
		t.Fatalf("ls missing folder: %d", st)
	}
	if st, _, _ := req(t, "GET", n.url+"/v1/db/default/pack/ghost/ls", n.key, nil); st != 404 {
		t.Fatalf("ls missing pack must be 404 and must not create it: %d", st)
	}
	if _, err := os.Stat(filepath.Join(n.h.Cfg.DataDir, "dbs", "default", "ghost.red-xai-pack")); err == nil {
		t.Fatal("reading a missing pack created it")
	}
	// path hygiene
	for _, bad := range []string{"../x", "a/../../x", "a//b", "a/./b", "a%5Cb", "a:b", "CON", "a/b%00c", "x/"} {
		if st, _, _ := req(t, "PUT", P+"/file/"+bad, n.key, []byte("x")); st != 400 && st != 404 && st != 301 {
			t.Errorf("path %q accepted: %d", bad, st)
		}
	}
	// replacing a file, deleting
	if st, _, _ := req(t, "PUT", P+"/file/a/big.bin", n.key, []byte("tiny")); st != 201 {
		t.Fatal("replace")
	}
	if _, got, _ := req(t, "GET", P+"/file/a/big.bin", n.key, nil); string(got) != "tiny" {
		t.Fatalf("replaced content %q", got)
	}
	if st, _, _ := req(t, "DELETE", P+"/file/a/big.bin", n.key, nil); st != 200 {
		t.Fatal("delete")
	}
	if st, _, _ := req(t, "GET", P+"/file/a/big.bin", n.key, nil); st != 404 {
		t.Fatal("deleted file still there")
	}
	// type safety: html is never served as html
	req(t, "PUT", P+"/file/x.html", n.key, []byte("<script>1</script>"))
	_, _, h = req(t, "GET", P+"/file/x.html", n.key, nil)
	if h.Get("Content-Type") != "application/octet-stream" || !strings.Contains(h.Get("Content-Disposition"), "attachment") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unsafe serving headers: %v", h)
	}
	req(t, "PUT", P+"/file/p.png", n.key, []byte("\x89PNG"))
	if _, _, h = req(t, "GET", P+"/file/p.png", n.key, nil); h.Get("Content-Type") != "image/png" {
		t.Fatalf("png type: %v", h)
	}
}

func TestPackIntegrityAndLimits(t *testing.T) {
	n := start(t, func(c *host.Config, _ string) { c.MaxUploadMB = 1 })
	P := n.url + "/v1/db/default/pack/m"
	if st, _, _ := req(t, "PUT", P+"/file/big", n.key, randBytes(2<<20)); st != 413 {
		t.Fatalf("over max_upload_mb must be 413, got %d", st)
	}
	if st, _, _ := req(t, "GET", P+"/stat/big", n.key, nil); st != 404 {
		t.Fatal("rejected upload left a file")
	}
	data := randBytes(1<<20 - 1000)
	if st, _, _ := req(t, "PUT", P+"/file/f", n.key, data); st != 201 {
		t.Fatal("small upload")
	}
	// Damage the stored chunk on disk: the download must fail loudly (error or aborted transfer), never return silently wrong bytes.
	root := filepath.Join(n.h.Cfg.DataDir, "dbs", "default", "m.red-xai-pack", "chunks")
	nchunks := 0
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			raw, _ := os.ReadFile(p)
			raw[len(raw)/2] ^= 0xff
			os.WriteFile(p, raw, 0o600)
			nchunks++
		}
		return nil
	})
	if nchunks == 0 {
		t.Fatal("no chunk file found")
	}
	r, _ := http.NewRequest("GET", P+"/file/f", nil)
	r.Header.Set("Authorization", "Bearer "+n.key)
	resp, err := http.DefaultClient.Do(r)
	if err == nil {
		b, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 && rerr == nil && bytes.Equal(b, data) {
			t.Fatal("corrupt data was served as if intact")
		}
		if resp.StatusCode == 200 && rerr == nil {
			t.Fatalf("corrupt data served with a clean 200 (len %d)", len(b))
		}
	}
	st, _, _ := req(t, "GET", P+"/file/f", n.key, nil, "Range", "bytes=10-20")
	if st != 500 {
		t.Fatalf("ranged read of a corrupt chunk must be 500, got %d", st)
	}
	// re-uploading heals it
	if st, _, _ := req(t, "PUT", P+"/file/f", n.key, data); st != 201 {
		t.Fatal("re-upload")
	}
	if _, got, _ := req(t, "GET", P+"/file/f", n.key, nil); !bytes.Equal(got, data) {
		t.Fatal("not healed")
	}
}

func TestPackScopesAndPrefix(t *testing.T) {
	n := start(t, nil)
	P := n.url + "/v1/db/default/pack/media"
	req(t, "PUT", P+"/file/public/a.txt", n.key, []byte("pub"))
	req(t, "PUT", P+"/file/private/b.txt", n.key, []byte("sec"))
	ro := mkKey(t, n, "read", "default", "")
	rw := mkKey(t, n, "write", "default", "")
	pub := mkKey(t, n, "write", "default", "media.red-xai-pack:public")
	otherDB := mkKey(t, n, "write", "elsewhere", "")

	if st, _, _ := req(t, "GET", P+"/file/public/a.txt", ""); st != 401 {
		t.Fatalf("no key %d", st)
	}
	if st, _, _ := req(t, "GET", P+"/file/public/a.txt", "rx_live_"+strings.Repeat("b", 43)); st != 401 {
		t.Fatalf("bad key %d", st)
	}
	if st, _, _ := req(t, "PUT", P+"/file/public/c.txt", ro, []byte("x")); st != 403 {
		t.Fatalf("read key must not upload: %d", st)
	}
	if st, _, _ := req(t, "DELETE", P+"/file/public/a.txt", ro); st != 403 {
		t.Fatalf("read key must not delete: %d", st)
	}
	if st, _, _ := req(t, "PUT", P+"/file/public/c.txt", rw, []byte("x")); st != 201 {
		t.Fatalf("write key upload: %d", st)
	}
	if st, _, _ := req(t, "GET", P+"/file/public/a.txt", otherDB); st != 403 {
		t.Fatalf("other db key: %d", st)
	}
	// prefix key: public/ only
	if st, b, _ := req(t, "GET", P+"/file/public/a.txt", pub); st != 200 || string(b) != "pub" {
		t.Fatalf("prefix key read allowed path: %d", st)
	}
	if st, _, _ := req(t, "GET", P+"/file/private/b.txt", pub); st != 403 {
		t.Fatalf("prefix key read outside: %d", st)
	}
	if st, _, _ := req(t, "PUT", P+"/file/private/z.txt", pub, []byte("x")); st != 403 {
		t.Fatalf("prefix key write outside: %d", st)
	}
	if st, _, _ := req(t, "PUT", P+"/file/publicity/z.txt", pub, []byte("x")); st != 403 {
		t.Fatalf("prefix must be boundary-aware (publicity/): %d", st)
	}
	if st, _, _ := req(t, "PUT", P+"/file/public/sub/z.txt", pub, []byte("x")); st != 201 {
		t.Fatalf("prefix key write inside: %d", st)
	}
	if st, _, _ := req(t, "GET", P+"/file/public/../private/b.txt", pub); st == 200 {
		t.Fatalf("traversal through an allowed prefix: %d", st)
	}
	if st, _, _ := req(t, "GET", P+"/ls/public", pub); st != 200 {
		t.Fatalf("ls inside prefix: %d", st)
	}
	if st, _, _ := req(t, "GET", P+"/ls", pub); st != 403 {
		t.Fatalf("ls root with prefix key must be forbidden (would reveal siblings): %d", st)
	}
	if st, _, _ := req(t, "GET", P+"/ls/private", pub); st != 403 {
		t.Fatalf("ls outside prefix: %d", st)
	}
	if st, _, _ := req(t, "GET", n.url+"/v1/db/default/pack", pub); st != 403 {
		t.Fatalf("list packs with prefix key: %d", st)
	}
	// the key's data prefix does not grant pack access and vice versa
	dataKey := mkKey(t, n, "write", "default", "players.red-xai.db:")
	if st, _, _ := req(t, "GET", P+"/file/public/a.txt", dataKey); st != 403 {
		t.Fatalf("data-prefix key reading a pack: %d", st)
	}
}

// ---- links ----

func TestResolveOnRead(t *testing.T) {
	n := start(t, nil)
	put := func(path, v string) {
		if st, b, _ := req(t, "PUT", n.url+"/v1/path/"+path, n.key, []byte(v)); st != 200 {
			t.Fatalf("put %s: %d %s", path, st, b)
		}
	}
	put("guilds.red-xai.db:Guild[ID=10]", `[ {Name[]} = "Dragons", {Motto[]} = "fire" ]`)
	put("secret.red-xai.db:Vault[ID=1]", `[ {Code[]} = "1234" ]`)
	put("players.red-xai.db:Player[ID=1]", `[ {Name[]} = "Ann", {Guild[]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10], {Vault[]} = "players.red-xai.db":Player[ID=1] @> "secret.red-xai.db":Vault[ID=1], {Gone[]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=99] ]`)
	get := func(path, key, qs string) (int, map[string]interface{}) {
		st, b, _ := req(t, "GET", n.url+"/v1/path/"+path+qs, key, nil)
		m := map[string]interface{}{}
		json.Unmarshal(b, &m)
		return st, m
	}
	// without resolve: the link is returned as a link
	if st, m := get("players.red-xai.db:Player[ID=1].Guild", n.key, ""); st != 200 || !strings.Contains(fmt.Sprint(m["value"]), "@>") {
		t.Fatalf("plain get should show the link: %d %v", st, m)
	}
	st, m := get("players.red-xai.db:Player[ID=1].Guild", n.key, "?resolve=1")
	if st != 200 || !strings.Contains(fmt.Sprint(m["value"]), "Dragons") || strings.Contains(fmt.Sprint(m["value"]), "@>") || m["resolved"] != true {
		t.Fatalf("resolve field: %d %v", st, m)
	}
	st, m = get("players.red-xai.db:Player[ID=1].Guild.Motto", n.key, "?resolve=1")
	if st != 200 || !strings.Contains(fmt.Sprint(m["value"]), "fire") {
		t.Fatalf("resolve through the link: %d %v", st, m)
	}
	st, m = get("players.red-xai.db:Player[ID=1]", n.key, "?resolve=1")
	if st != 200 || !strings.Contains(fmt.Sprint(m["value"]), "Dragons") || m["dangling"] == nil {
		t.Fatalf("whole record: dangling must be reported: %d %v", st, m)
	}
	if st, _ := get("players.red-xai.db:Player[ID=7]", n.key, "?resolve=1"); st != 404 {
		t.Fatalf("missing record: %d", st)
	}
	if st, _ := get("players.red-xai.db:Player[ID=*]", n.key, "?resolve=1"); st != 400 {
		t.Fatalf("wildcard resolve: %d", st)
	}
	if st, _ := get("players.red-xai.db:Player[ID=1]", n.key, "?resolve=1&depth=99"); st != 400 {
		t.Fatalf("bad depth: %d", st)
	}
	// a key limited to players/guilds must not reach the vault through a link
	pk := mkKey(t, n, "read", "default", "players.red-xai.db:Player[ID=1]")
	st, m = get("players.red-xai.db:Player[ID=1]", pk, "?resolve=1")
	v := fmt.Sprint(m["value"])
	if st != 200 || strings.Contains(v, "1234") || strings.Contains(v, "Dragons") {
		t.Fatalf("prefix-restricted resolve leaked linked data: %d %s", st, v)
	}
	if st, _ := get("secret.red-xai.db:Vault[ID=1]", pk, "?resolve=1"); st != 403 {
		t.Fatalf("direct read of vault with prefix key: %d", st)
	}
	gk := mkKey(t, n, "read", "default", "players.red-xai.db:")
	st, m = get("players.red-xai.db:Player[ID=1]", gk, "?resolve=1")
	v = fmt.Sprint(m["value"])
	if st != 200 || strings.Contains(v, "1234") || strings.Contains(v, "Dragons") {
		t.Fatalf("prefix key players.red-xai.db: leaked linked data from other files: %d %s", st, v)
	}
	// text/plain accept
	r, _ := http.NewRequest("GET", n.url+"/v1/path/players.red-xai.db:Player[ID=1].Guild?resolve=1", nil)
	r.Header.Set("Authorization", "Bearer "+n.key)
	r.Header.Set("Accept", "text/x-redx-box")
	resp, _ := http.DefaultClient.Do(r)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Dragons") {
		t.Fatalf("box accept: %s", b)
	}
}

// ---- pulse ----

const deathRule = `{PlayerDeath[ON="players.red-xai.db:Player[ID=*].Health"]} = [
    {When[]}      = "<= 0",
    {SetTarget[]} = "Player.IsDead = TRUE",
    {Emit[]}      = "player.died"
];
`

func wsDial(t *testing.T, url, key string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(url, "http")
	c, resp, err := websocket.DefaultDialer.Dial(u, http.Header{"Authorization": {"Bearer " + key}})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (%d)", url, err, code)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func readEvent(t *testing.T, c *websocket.Conn, d time.Duration) (host.PulseEvent, bool) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(d))
	var ev host.PulseEvent
	if err := c.ReadJSON(&ev); err != nil {
		return ev, false
	}
	return ev, true
}

func TestPulseRulesAndWebSocket(t *testing.T) {
	n := start(t, nil)
	D := n.url + "/v1/db/default"
	if st, b, _ := req(t, "PUT", D+"/pulse", n.key, []byte("{Bad[]} = [ {When[]} = ")); st != 400 {
		t.Fatalf("bad rules: %d %s", st, b)
	}
	if st, b, _ := req(t, "PUT", D+"/pulse", n.key, []byte(deathRule)); st != 200 {
		t.Fatalf("define: %d %s", st, b)
	}
	if st, b, _ := req(t, "GET", D+"/pulse", n.key, nil); st != 200 || !strings.Contains(string(b), "PlayerDeath") {
		t.Fatalf("get rules: %d %s", st, b)
	}
	ws := wsDial(t, D+"/pulse/subscribe", n.key)
	put := func(path, v string) {
		if st, b, _ := req(t, "PUT", n.url+"/v1/path/"+path, n.key, []byte(v)); st != 200 {
			t.Fatalf("put %s %d %s", path, st, b)
		}
	}
	put("players.red-xai.db:Player[ID=1]", `[ {Name[]} = "Ann", {Health[]} = 10 ]`)
	put("players.red-xai.db:Player[ID=1].Health", `0`) // field write through the rule engine
	ev, ok := readEvent(t, ws, 3*time.Second)
	if !ok || ev.Topic != "player.died" || ev.Rule != "PlayerDeath" || ev.Path != "players.red-xai.db:Player[ID=1]" {
		t.Fatalf("expected player.died, got %+v ok=%v", ev, ok)
	}
	_, b, _ := req(t, "GET", n.url+"/v1/path/players.red-xai.db:Player[ID=1].IsDead", n.key, nil)
	if !strings.Contains(string(b), "TRUE") {
		t.Fatalf("rule action did not run: %s", b)
	}
	// topic filter + whole-record write
	ws2 := wsDial(t, D+"/pulse/subscribe?topic=nothing.*", n.key)
	put("players.red-xai.db:Player[ID=2]", `[ {Name[]} = "Bob", {Health[]} = 0 ]`)
	if ev, ok := readEvent(t, ws, 3*time.Second); !ok || ev.Path != "players.red-xai.db:Player[ID=2]" {
		t.Fatalf("second death on ws: %+v %v", ev, ok)
	}
	if ev, ok := readEvent(t, ws2, 400*time.Millisecond); ok {
		t.Fatalf("topic filter ignored: %+v", ev)
	}
	// ACL: a key limited to Player[ID=1] only sees events for it
	pk := mkKey(t, n, "read", "default", "players.red-xai.db:Player[ID=1]")
	wsp := wsDial(t, D+"/pulse/subscribe", pk)
	put("players.red-xai.db:Player[ID=3]", `[ {Name[]} = "Cy", {Health[]} = 0 ]`)
	put("players.red-xai.db:Player[ID=1].Health", `5`)
	put("players.red-xai.db:Player[ID=1].Health", `-1`)
	ev, ok = readEvent(t, wsp, 3*time.Second)
	if !ok || ev.Path != "players.red-xai.db:Player[ID=1]" {
		t.Fatalf("prefix key must only get its own record's events: %+v %v", ev, ok)
	}
	// permissions
	ro := mkKey(t, n, "read", "default", "")
	if st, _, _ := req(t, "PUT", D+"/pulse", ro, []byte(deathRule)); st != 403 {
		t.Fatalf("read key defining rules: %d", st)
	}
	if st, _, _ := req(t, "GET", D+"/pulse", pk); st != 403 {
		t.Fatalf("prefix key reading rule source: %d", st)
	}
	if st, _, _ := req(t, "GET", D+"/pulse/subscribe", ""); st != 401 {
		t.Fatalf("ws without key: %d", st)
	}
	// remove rules: writes no longer fire
	if st, _, _ := req(t, "DELETE", D+"/pulse", n.key, nil); st != 200 {
		t.Fatal("delete rules")
	}
	put("players.red-xai.db:Player[ID=4]", `[ {Name[]} = "Di", {Health[]} = 0 ]`)
	for { // drain what was already queued; nothing about ID=4 may arrive
		ev, ok := readEvent(t, ws, 500*time.Millisecond)
		if !ok {
			break
		}
		if strings.Contains(ev.Path, "ID=4") {
			t.Fatalf("rule fired after removal: %+v", ev)
		}
	}
}

func TestPulseTimerEvents(t *testing.T) {
	n := start(t, nil)
	D := n.url + "/v1/db/default"
	rule := `{Respawn[ON="players.red-xai.db:Player[ID=*].Health"]} = [
    {When[]}  = "<= 0",
    {Timer[]} = [ {Name[]} = "Back", {Seconds[]} = 1, {Do[]} = ;["emit player.respawned"]: ]
];
`
	if st, b, _ := req(t, "PUT", D+"/pulse", n.key, []byte(rule)); st != 200 {
		t.Fatalf("define %d %s", st, b)
	}
	ws := wsDial(t, D+"/pulse/subscribe?topic=player.*", n.key)
	req(t, "PUT", n.url+"/v1/path/players.red-xai.db:Player[ID=1]", n.key, []byte(`[ {Health[]} = 0 ]`))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ev, ok := readEvent(t, ws, time.Until(deadline))
		if ok && ev.Topic == "player.respawned" {
			return
		}
	}
	t.Fatal("timer event never delivered")
}

func TestChangesBridge(t *testing.T) {
	n := start(t, nil)
	D := n.url + "/v1/db/default"
	ws := wsDial(t, D+"/pulse/subscribe?changes=1&topic=change*", n.key)
	req(t, "PUT", n.url+"/v1/path/players.red-xai.db:Player[ID=5]", n.key, []byte(`[ {Name[]} = "Ed" ]`))
	ev, ok := readEvent(t, ws, 3*time.Second)
	if !ok || !strings.HasPrefix(ev.Topic, "change") || ev.Path != "players.red-xai.db:Player[ID=5]" {
		t.Fatalf("change event: %+v %v", ev, ok)
	}
}

// ---- cluster ----

func TestExtrasOnFollowerAndPromotion(t *testing.T) {
	leader := start(t, nil)
	f := start(t, func(c *host.Config, _ string) {
		c.Role = "follower"
		c.Peers = []string{leader.url}
		c.NodeID = "x-f1"
	})
	f.key = leader.key
	eventually(t, "keys replicated", 6*time.Second, func() bool { return f.h.KS.Count() > 0 })
	LD, FD := leader.url+"/v1/db/default", f.url+"/v1/db/default"
	if st, b, _ := req(t, "PUT", FD+"/idx/players", leader.key, []byte(idxDecl)); st != 200 { // forwarded to the leader
		t.Fatalf("define via follower: %d %s", st, b)
	}
	if st, b, _ := req(t, "PUT", FD+"/pulse", leader.key, []byte(deathRule)); st != 200 {
		t.Fatalf("pulse via follower: %d %s", st, b)
	}
	putPlayer(t, leader, 1, "Ann", 5, "x")
	// definitions are replicated as files
	for _, name := range []string{"players.red-xai-db-idx", "rules.red-xai-db-pulse"} {
		p := filepath.Join(f.h.Cfg.DataDir, "dbs", "default", name)
		eventually(t, "replicated "+name, 6*time.Second, func() bool { _, err := os.Stat(p); return err == nil })
	}
	// queries sent to the follower are forwarded to the leader (index contents are leader-local)
	if st, b, _ := req(t, "GET", FD+"/idx/players/PlayerByName/query?mode=exact&field=Name&value=ann", leader.key, nil); st != 200 || len(hitPaths(t, b)) != 1 {
		t.Fatalf("query via follower: %d %s", st, b)
	}
	// pack upload through a follower lands on the leader
	if st, _, _ := req(t, "PUT", FD+"/pack/m/file/a.txt", leader.key, []byte("hello")); st != 201 {
		t.Fatalf("upload via follower: %d", st)
	}
	if st, b, _ := req(t, "GET", LD+"/pack/m/file/a.txt", leader.key, nil); st != 200 || string(b) != "hello" {
		t.Fatalf("leader has the file: %d %s", st, b)
	}
	// resolve is served from the follower's own replicated data
	req(t, "PUT", leader.url+"/v1/path/guilds.red-xai.db:Guild[ID=1]", leader.key, []byte(`[ {Name[]} = "G" ]`))
	req(t, "PUT", leader.url+"/v1/path/players.red-xai.db:Player[ID=9]", leader.key, []byte(`[ {Guild[]} = "players.red-xai.db":Player[ID=9] @> "guilds.red-xai.db":Guild[ID=1] ]`))
	eventually(t, "resolve on follower", 6*time.Second, func() bool {
		st, b, _ := req(t, "GET", f.url+"/v1/path/players.red-xai.db:Player[ID=9].Guild?resolve=1", leader.key, nil)
		return st == 200 && strings.Contains(string(b), `G`) && !strings.Contains(string(b), "@>")
	})
	// WebSocket subscriptions made on a follower are proxied to the leader (that is where rules run)
	wsf := wsDial(t, FD+"/pulse/subscribe", leader.key)
	req(t, "PUT", leader.url+"/v1/path/players.red-xai.db:Player[ID=30]", leader.key, []byte(`[ {Name[]} = "Fw", {Health[]} = 0 ]`))
	if ev, ok := readEvent(t, wsf, 3*time.Second); !ok || ev.Topic != "player.died" {
		t.Fatalf("event through follower proxy: %+v %v", ev, ok)
	}
	wsf.Close()
	// promote: the follower rebuilds indexes from its replicated data and runs the stored rules
	if st, b, _ := req(t, "POST", f.url+"/v1/cluster/promote", leader.key, nil); st != 200 {
		t.Fatalf("promote %d %s", st, b)
	}
	if st, b, _ := req(t, "GET", FD+"/idx/players/PlayerByName/query?mode=exact&field=Name&value=ann", leader.key, nil); st != 200 || len(hitPaths(t, b)) != 1 {
		t.Fatalf("index after promotion: %d %s", st, b)
	}
	ws := wsDial(t, FD+"/pulse/subscribe", leader.key)
	req(t, "PUT", f.url+"/v1/path/players.red-xai.db:Player[ID=20]", leader.key, []byte(`[ {Name[]} = "New", {Health[]} = 0 ]`))
	if ev, ok := readEvent(t, ws, 3*time.Second); !ok || ev.Topic != "player.died" {
		t.Fatalf("rules after promotion: %+v %v", ev, ok)
	}
	if st, _, _ := req(t, "GET", FD+"/pack/m/file/a.txt", leader.key, nil); st != 404 {
		t.Logf("note: pack contents are leader-local (promoted node status %d)", st)
	}
}

func TestPackGCAndFsck(t *testing.T) {
	n := start(t, nil)
	P := n.url + "/v1/db/default/pack/m"
	req(t, "PUT", P+"/file/a.bin", n.key, randBytes(1500000))
	if st, b, _ := req(t, "GET", P+"/fsck?deep=1", n.key); st != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("fsck %d %s", st, b)
	}
	req(t, "DELETE", P+"/file/a.bin", n.key)
	st, b, _ := req(t, "POST", P+"/gc", n.key)
	if st != 200 || !strings.Contains(string(b), `"chunks_removed":2`) {
		t.Fatalf("gc %d %s", st, b)
	}
	rw := mkKey(t, n, "write", "default", "")
	pre := mkKey(t, n, "admin", "default", "m.red-xai-pack:")
	for _, k := range []string{rw, pre} {
		if st, _, _ := req(t, "POST", P+"/gc", k); st != 403 {
			t.Fatalf("gc by non-admin/prefix key: %d", st)
		}
		if st, _, _ := req(t, "GET", P+"/fsck", k); st != 403 {
			t.Fatalf("fsck by non-admin/prefix key: %d", st)
		}
	}
	if st, _, _ := req(t, "GET", P+"/gc", n.key); st != 405 {
		t.Fatal("gc must be POST")
	}
}
