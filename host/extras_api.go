package host

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// REST surface for indexes, packs, link resolution and pulse rules. All routes live
// under /v1/db/{db}/ (the default database has no shorthand for these).
//
//	GET    idx                                   list index sets            read, key without path prefix
//	PUT    idx/{set}                             define a set (Box decl)    admin on the db, no prefix
//	DELETE idx/{set}                             drop a set                 admin on the db, no prefix
//	POST   idx/{set}/rebuild                     rescan                     admin on the db, no prefix
//	GET    idx/{set}/check                       drift report               read, no prefix
//	GET    idx/{set}/{index}/query?mode=...      exact|prefix|range|text    read; hits filtered by the key's prefix
//	GET    pack                                  list packs                 read, no prefix
//	GET    pack/{pack}/ls[/folder]               list a folder              read, prefix on "<pack>.red-xai-pack:<folder>"
//	GET    pack/{pack}/stat/{path}               describe a file/folder     read, prefix
//	GET    pack/{pack}/file/{path}               download (Range, HEAD)     read, prefix
//	PUT    pack/{pack}/file/{path}               upload (streamed)          write, prefix
//	DELETE pack/{pack}/file/{path}               delete a file              write, prefix
//	POST   pack/{pack}/gc                        free unreferenced chunks   admin on the db, no prefix
//	GET    pack/{pack}/fsck[?deep=1]             integrity report           admin on the db, no prefix
//	GET    path/{p}?resolve=1[&depth=n]          read with links followed   read, prefix (targets filtered too)
//	GET    pulse                                 rules + timers             read, no prefix
//	PUT    pulse                                 replace the rule set       admin on the db, no prefix
//	DELETE pulse                                 remove all rules           admin on the db, no prefix
//	GET    pulse/subscribe  (WebSocket)          fired events               read; events filtered by the key's prefix

var setNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// authzScope authenticates and checks scope + database only. The caller applies the
// key's path prefix itself (by filtering results).
func (h *Host) authzScope(w http.ResponseWriter, r *http.Request, need, db string) (*Principal, bool) {
	pr, wait, err := h.Auth.Authenticate(r)
	switch {
	case errors.Is(err, ErrThrottled):
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
		return nil, false
	case err != nil:
		h.authBad.Add(1)
		w.Header().Set("WWW-Authenticate", `Bearer realm="redx"`)
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	if scopeRank[pr.Key.Scope] < scopeRank[need] || (pr.Key.DB != AllDBs && pr.Key.DB != db) {
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Method: r.Method, Route: routeOf(r), Reason: "scope/db"})
		writeErr(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return pr, true
}

func allowFor(k *KeyRecord) func(string) bool {
	if k.Prefix == "" {
		return nil
	}
	pre := k.Prefix
	return func(p string) bool { return PathHasPrefix(p, pre) }
}

func (h *Host) extrasOf(w http.ResponseWriter, db string) (Extras, bool) {
	b, err := h.Reg.Get(db)
	if err != nil {
		h.mapErr(w, err)
		return nil, false
	}
	ex, ok := b.(Extras)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "this database backend does not support indexes, packs, links or rules")
		return nil, false
	}
	return ex, true
}

// WriteAux applies an index/pulse definition change on the leader and replicates it
// (definitions are small text files; packs and index contents are NOT replicated).
func (h *Host) WriteAux(db, kind, name, src string, del bool) (uint64, error) {
	if !h.Cluster.IsLeader() {
		return 0, ErrReadOnly
	}
	b, err := h.Reg.Get(db)
	if err != nil {
		return 0, err
	}
	h.pause.RLock()
	defer h.pause.RUnlock()
	h.opMu.Lock()
	defer h.opMu.Unlock()
	if err := applyAux(b, kind, name, src, del); err != nil {
		return 0, err
	}
	p := kind
	if name != "" {
		p += "/" + name
	}
	reason := ""
	if del {
		reason = "delete"
	}
	return h.Cluster.Replicate(LogEntry{DB: db, Op: "aux", Path: p, Value: src, Reason: reason})
}

func applyAux(b Backend, kind, name, src string, del bool) error {
	ex, ok := b.(Extras)
	if !ok {
		return ErrUnsupported
	}
	switch kind {
	case "idx":
		if del {
			return ex.IndexDrop(name)
		}
		return ex.IndexDefine(name, src)
	case "pulse":
		if del {
			src = ""
		}
		return ex.PulseDefine(src)
	case "sec":
		if del {
			src = ""
		}
		return ex.SecDefine(src)
	case "code":
		if del {
			src = ""
		}
		return ex.CodeDefine(name, src)
	}
	return fmt.Errorf("%w: unknown definition kind %q", ErrInvalid, kind)
}

func (h *Host) extrasMapErr(w http.ResponseWriter, err error) {
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe), errors.Is(err, ErrTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
	case errors.Is(err, ErrUnsupported):
		writeErr(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, ErrForbidden):
		writeErr(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrUnavailable):
		h.Logf("unavailable: %v", err)
		writeErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrMediaType):
		writeErr(w, http.StatusUnsupportedMediaType, err.Error())
	case errors.Is(err, ErrCorrupt):
		h.Logf("integrity failure: %v", err)
		writeErr(w, http.StatusInternalServerError, "stored data failed its integrity check")
	case errors.Is(err, errChecksum):
		writeErr(w, http.StatusUnprocessableEntity, "uploaded data does not match the supplied SHA-256; nothing was stored")
	default:
		h.mapErr(w, err)
	}
}

// ---- dispatch (called from dbRoute) ----

func (h *Host) extrasRoute(w http.ResponseWriter, r *http.Request, db, sub string) bool {
	switch {
	case sub == "idx" || strings.HasPrefix(sub, "idx/"):
		h.idxRoute(w, r, db, strings.TrimPrefix(strings.TrimPrefix(sub, "idx"), "/"))
	case sub == "pack" || strings.HasPrefix(sub, "pack/"):
		h.packRoute(w, r, db, strings.TrimPrefix(strings.TrimPrefix(sub, "pack"), "/"))
	case sub == "pulse" || sub == "pulse/subscribe":
		h.pulseRoute(w, r, db, sub == "pulse/subscribe")
	default:
		return h.servicesRoute(w, r, db, sub)
	}
	return true
}

// ---- indexes ----

func (h *Host) idxRoute(w http.ResponseWriter, r *http.Request, db, rest string) {
	parts := strings.Split(rest, "/")
	if rest == "" {
		if r.Method != "GET" {
			writeErr(w, 405, "method not allowed")
			return
		}
		if _, ok := h.authz(w, r, ScopeRead, db, "", false); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		sets, err := ex.IndexList()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		if sets == nil {
			sets = []IndexSetInfo{}
		}
		writeJSON(w, 200, map[string]interface{}{"sets": sets})
		return
	}
	set := parts[0]
	if !setNameRe.MatchString(set) {
		writeErr(w, 400, "index set names are letters, digits, '_' and '-' (max 64)")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "PUT":
		if _, ok := h.authz(w, r, ScopeAdmin, db, "", false); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		decl, err := declBody(r)
		if err != nil {
			h.mapErr(w, err)
			return
		}
		seq, err := h.WriteAux(db, "idx", set, decl, false)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "idx.define", DB: db, Remote: remoteIP(r), Detail: set})
		h.writeAck(w, r, seq)
	case len(parts) == 1 && r.Method == "DELETE":
		if _, ok := h.authz(w, r, ScopeAdmin, db, "", false); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		seq, err := h.WriteAux(db, "idx", set, "", true)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "idx.drop", DB: db, Remote: remoteIP(r), Detail: set})
		h.writeAck(w, r, seq)
	case len(parts) == 2 && parts[1] == "rebuild" && r.Method == "POST":
		if _, ok := h.authz(w, r, ScopeAdmin, db, "", false); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		if err := ex.IndexRebuild(set); err != nil {
			h.extrasMapErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]interface{}{"ok": true})
	case len(parts) == 2 && parts[1] == "check" && r.Method == "GET":
		if _, ok := h.authz(w, r, ScopeRead, db, "", false); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		d, err := ex.IndexCheck(set)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]interface{}{"indexes": d})
	case len(parts) == 3 && parts[2] == "query" && r.Method == "GET":
		h.idxQuery(w, r, db, set, parts[1])
	default:
		writeErr(w, 404, "not found")
	}
}

func declBody(r *http.Request) (string, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return "", ErrInvalid
	}
	s := string(b)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var o struct {
			Decl string `json:"decl"`
		}
		if json.Unmarshal(b, &o) != nil {
			return "", errWrap(`body must be {"decl": "..."} or plain Box text`)
		}
		s = o.Decl
	}
	if strings.TrimSpace(s) == "" {
		return "", errWrap("empty definition")
	}
	return s, nil
}

const maxIndexLimit = 10000

func (h *Host) idxQuery(w http.ResponseWriter, r *http.Request, db, set, index string) {
	pr, ok := h.authzScope(w, r, ScopeRead, db)
	if !ok {
		return
	}
	if h.maybeForward(w, r) {
		return
	}
	q := r.URL.Query()
	iq := IndexQuery{Mode: strings.ToLower(q.Get("mode")), Field: q.Get("field"), Value: q.Get("value"), VType: q.Get("vtype"),
		Op: strings.ToLower(q.Get("op")), Record: q.Get("record") == "1" || q.Get("record") == "true"}
	note := ""
	if iq.Mode == "fuzzy" {
		iq.Mode = "text"
		if iq.Op == "" {
			iq.Op = "or"
		}
		note = "fuzzy is an any-word text match (op=or); the engine has no edit-distance search"
	}
	if iq.Mode == "" {
		writeErr(w, 400, "mode is required: exact, prefix, range, text (or fuzzy)")
		return
	}
	if iq.Field == "" {
		writeErr(w, 400, "field is required")
		return
	}
	var err error
	if iq.Limit, err = atoiDefault(q.Get("limit"), 100); err != nil || iq.Limit < 1 {
		writeErr(w, 400, "limit must be a positive integer")
		return
	}
	if iq.Limit > maxIndexLimit {
		iq.Limit = maxIndexLimit
	}
	for _, kv := range []struct {
		name string
		dst  **float64
	}{{"lo", &iq.Lo}, {"hi", &iq.Hi}} {
		if s := q.Get(kv.name); s != "" {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				writeErr(w, 400, kv.name+" must be a number")
				return
			}
			*kv.dst = &f
		}
	}
	iq.LoExcl = q.Get("lo_excl") == "1" || q.Get("lo_excl") == "true"
	iq.HiExcl = q.Get("hi_excl") == "1" || q.Get("hi_excl") == "true"
	iq.Terms = append(iq.Terms, q["term"]...)
	if t := strings.TrimSpace(q.Get("q")); t != "" {
		iq.Terms = append(iq.Terms, t)
	}
	ex, ok := h.extrasOf(w, db)
	if !ok {
		return
	}
	hits, err := ex.IndexQuery(set, index, iq, h.readAllow(ex, &pr.Key))
	if err != nil {
		h.extrasMapErr(w, err)
		return
	}
	if ex.SecActive() { // field-level rules: a hit on a field the caller may not read is dropped; records are redacted
		sp, kept := secPrincipal(&pr.Key), hits[:0]
		for _, ht := range hits {
			f, b, _ := splitResource(ht.Path)
			if !ex.SecAllow(sp, "read", f, b, ht.Field) {
				continue
			}
			if ht.Record != "" {
				rec, err := ex.SecRead(sp, ht.Path, ht.Record)
				if err != nil {
					continue
				}
				ht.Record = rec
			}
			kept = append(kept, ht)
		}
		hits = kept
	}
	if hits == nil {
		hits = []IndexHit{}
	}
	out := map[string]interface{}{"hits": hits, "count": len(hits), "mode": iq.Mode}
	if note != "" {
		out["note"] = note
	}
	writeJSON(w, 200, out)
}

func atoiDefault(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	return strconv.Atoi(s)
}

// ---- packs ----

func packACLPath(pack, p string) string { return pack + ".red-xai-pack:" + p }

// validPackPath is a conservative pre-check mirroring the engine's rules, so the
// key's prefix ACL is always applied to the literal canonical path.
func validPackPath(p string, allowEmpty bool) bool {
	if p == "" {
		return allowEmpty
	}
	if len(p) > 4096 || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." || len(c) > 255 {
			return false
		}
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' || r == ':' || r == 0xfffd || strings.ContainsRune("<>\"|?*", r) {
			return false
		}
	}
	return true
}

func (h *Host) packRoute(w http.ResponseWriter, r *http.Request, db, rest string) {
	if rest == "" {
		if r.Method != "GET" {
			writeErr(w, 405, "method not allowed")
			return
		}
		if _, ok := h.authz(w, r, ScopeRead, db, "", false); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		ps, err := ex.PackList()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		if ps == nil {
			ps = []string{}
		}
		writeJSON(w, 200, map[string]interface{}{"packs": ps})
		return
	}
	name, rest2, _ := strings.Cut(rest, "/")
	verb, arg, _ := strings.Cut(rest2, "/")
	if !setNameRe.MatchString(name) {
		writeErr(w, 400, "pack names are letters, digits, '_' and '-' (max 64)")
		return
	}
	if verb == "gc" || verb == "fsck" {
		h.packMaint(w, r, db, name, verb)
		return
	}
	switch verb {
	case "ls", "stat", "file":
	default:
		writeErr(w, 404, "not found")
		return
	}
	if !validPackPath(arg, verb == "ls") {
		writeErr(w, 400, "invalid pack path")
		return
	}
	acl := packACLPath(name, arg)
	switch {
	case verb == "ls" && r.Method == "GET":
		pr, ok := h.authz(w, r, ScopeRead, db, acl, false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		if !h.secFile(w, ex, pr, "read", name+".red-xai-pack") {
			return
		}
		fs, err := ex.PackLs(name, arg)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		if fs == nil {
			fs = []PackFile{}
		}
		writeJSON(w, 200, map[string]interface{}{"pack": name, "folder": arg, "entries": fs})
	case verb == "stat" && r.Method == "GET":
		pr, ok := h.authz(w, r, ScopeRead, db, acl, false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		if !h.secFile(w, ex, pr, "read", name+".red-xai-pack") {
			return
		}
		f, err := ex.PackStat(name, arg)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		writeJSON(w, 200, f)
	case verb == "file":
		h.packFile(w, r, db, name, arg, acl)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		writeErr(w, 405, "method not allowed")
	}
}

// packMaint: POST gc / GET fsck. Admin on the db, never for prefix-restricted keys.
func (h *Host) packMaint(w http.ResponseWriter, r *http.Request, db, name, verb string) {
	wantMethod := map[string]string{"gc": "POST", "fsck": "GET"}[verb]
	if r.Method != wantMethod {
		writeErr(w, 405, "method not allowed")
		return
	}
	if _, ok := h.authz(w, r, ScopeAdmin, db, "", false); !ok {
		return
	}
	if h.maybeForward(w, r) {
		return
	}
	ex, ok := h.extrasOf(w, db)
	if !ok {
		return
	}
	if verb == "gc" {
		h.pause.RLock()
		res, err := ex.PackGC(name)
		h.pause.RUnlock()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "pack.gc", DB: db, Remote: remoteIP(r), Detail: "pack=" + name})
		writeJSON(w, 200, res)
		return
	}
	res, err := ex.PackFsck(name, r.URL.Query().Get("deep") == "1")
	if err != nil {
		h.extrasMapErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

var errChecksum = errors.New("checksum mismatch")

// sumReader hashes what passes through and, at EOF, fails the read when the digest
// differs from want, so the pack aborts before it commits the file.
type sumReader struct {
	r    io.Reader
	h    hash.Hash
	want string
}

func (s *sumReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.h.Write(p[:n])
	if err == io.EOF && s.want != "" {
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(s.h.Sum(nil))), []byte(s.want)) != 1 {
			return n, errChecksum
		}
	}
	return n, err
}

func (h *Host) packFile(w http.ResponseWriter, r *http.Request, db, pack, p, acl string) {
	switch r.Method {
	case "GET", "HEAD":
		pr, ok := h.authz(w, r, ScopeRead, db, acl, false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		if !h.secFile(w, ex, pr, "read", pack+".red-xai-pack") {
			return
		}
		h.packDownload(w, r, ex, pack, p)
	case "PUT":
		pr, ok := h.authz(w, r, ScopeWrite, db, acl, false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		if !h.Cluster.IsLeader() {
			writeErr(w, 503, ErrReadOnly.Error())
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		if !h.secFile(w, ex, pr, "write", pack+".red-xai-pack") {
			return
		}
		want := strings.ToLower(strings.TrimSpace(firstNonEmpty(r.Header.Get("X-Redx-SHA256"), r.URL.Query().Get("sha256"))))
		if want != "" && (len(want) != 64 || strings.Trim(want, "0123456789abcdef") != "") {
			writeErr(w, 400, "sha256 must be 64 hex characters")
			return
		}
		h.pause.RLock()
		sr := &sumReader{r: r.Body, h: sha256.New(), want: want}
		f, err := ex.PackPut(pack, p, sr)
		h.pause.RUnlock()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "pack.put", DB: db, Remote: remoteIP(r), Detail: fmt.Sprintf("pack=%s size=%d", pack, f.Size)})
		w.Header().Set("ETag", `"sha256-`+f.SHA256+`"`)
		writeJSON(w, 201, f)
	case "DELETE":
		pr, ok := h.authz(w, r, ScopeWrite, db, acl, false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		if !h.Cluster.IsLeader() {
			writeErr(w, 503, ErrReadOnly.Error())
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		if !h.secFile(w, ex, pr, "delete", pack+".red-xai-pack") {
			return
		}
		h.pause.RLock()
		err := ex.PackDelete(pack, p)
		h.pause.RUnlock()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "pack.delete", DB: db, Remote: remoteIP(r), Detail: "pack=" + pack})
		writeJSON(w, 200, map[string]interface{}{"ok": true})
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		writeErr(w, 405, "method not allowed")
	}
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// safeTypes are served inline with their real type; everything else is an opaque download.
var safeTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".wav": "audio/wav", ".mp4": "video/mp4", ".webm": "video/webm",
	".pdf": "application/pdf", ".txt": "text/plain; charset=utf-8", ".json": "application/json", ".csv": "text/csv; charset=utf-8",
}

// parseRange handles a single "bytes=a-b" range. ok=false means "ignore the header" (absent, malformed or
// multi-range); unsat=true means a 416.
func parseRange(h string, size int64) (off, n int64, ok, unsat bool) {
	if !strings.HasPrefix(h, "bytes=") || strings.Contains(h, ",") || size == 0 {
		return 0, 0, false, false
	}
	a, b, found := strings.Cut(strings.TrimSpace(h[6:]), "-")
	if !found {
		return 0, 0, false, false
	}
	switch {
	case a == "": // suffix: last N bytes
		sn, err := strconv.ParseInt(b, 10, 64)
		if err != nil || sn <= 0 {
			if err == nil {
				return 0, 0, true, true
			}
			return 0, 0, false, false
		}
		if sn > size {
			sn = size
		}
		return size - sn, sn, true, false
	default:
		s, err := strconv.ParseInt(a, 10, 64)
		if err != nil || s < 0 {
			return 0, 0, false, false
		}
		e := size - 1
		if b != "" {
			e, err = strconv.ParseInt(b, 10, 64)
			if err != nil || e < s {
				return 0, 0, false, false
			}
		}
		if s >= size {
			return 0, 0, true, true
		}
		if e >= size {
			e = size - 1
		}
		return s, e - s + 1, true, false
	}
}

func (h *Host) packDownload(w http.ResponseWriter, r *http.Request, ex Extras, pack, p string) {
	info, err := ex.PackStat(pack, p)
	if err != nil {
		h.extrasMapErr(w, err)
		return
	}
	if info.IsDir {
		writeErr(w, 400, "that is a folder; use ls")
		return
	}
	etag := `"sha256-` + info.SHA256 + `"`
	hd := w.Header()
	hd.Set("ETag", etag)
	hd.Set("Accept-Ranges", "bytes")
	if info.Encrypted {
		h.packDownloadEncrypted(w, r, ex, pack, p, etag)
		return
	}
	ct := safeTypes[strings.ToLower(path.Ext(p))]
	if ct == "" {
		ct = "application/octet-stream"
		hd.Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", `\`, "").Replace(path.Base(p))+`"`)
	}
	hd.Set("Content-Type", ct)
	hd.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	off, n, partial, unsat := int64(0), int64(-1), false, false
	if rg := r.Header.Get("Range"); rg != "" {
		if ir := r.Header.Get("If-Range"); ir == "" || ir == etag {
			off, n, partial, unsat = parseRange(rg, info.Size)
		}
	}
	if unsat {
		hd.Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size))
		writeErr(w, http.StatusRequestedRangeNotSatisfiable, "range not satisfiable")
		return
	}
	length := info.Size
	if partial {
		length = n
	} else {
		off, n = 0, -1
	}
	if r.Method == "HEAD" {
		hd.Set("Content-Length", strconv.FormatInt(length, 10))
		if partial {
			hd.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+n-1, info.Size))
			w.WriteHeader(http.StatusPartialContent)
		}
		return
	}
	rc, _, err := ex.PackOpen(pack, p, off, n)
	if err != nil {
		h.extrasMapErr(w, err)
		return
	}
	defer rc.Close()
	// Fetch (and verify) the first block before committing to a 200/206, so early failures are real errors.
	br := bufio.NewReaderSize(rc, 64<<10)
	if _, err := br.Peek(1); err != nil && err != io.EOF {
		h.extrasMapErr(w, err)
		return
	}
	hd.Set("Content-Length", strconv.FormatInt(length, 10))
	if partial {
		hd.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+n-1, info.Size))
		w.WriteHeader(http.StatusPartialContent)
	}
	if _, err := io.Copy(w, br); err != nil {
		// Headers are out: abort the connection so the client sees a truncated transfer, never a clean-looking short file.
		h.Logf("pack download aborted (integrity or I/O error): %v", err)
		panic(http.ErrAbortHandler)
	}
}

// packDownloadEncrypted streams a decrypted file. The plaintext length is not stored, so there is no
// Content-Length and no Range: the whole file is sent (HEAD returns headers only).
func (h *Host) packDownloadEncrypted(w http.ResponseWriter, r *http.Request, ex Extras, pack, p, etag string) {
	hd := w.Header()
	hd.Set("Accept-Ranges", "none")
	hd.Set("X-Redx-Encrypted", "1")
	ct := safeTypes[strings.ToLower(path.Ext(p))]
	if ct == "" {
		ct = "application/octet-stream"
		hd.Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", `\`, "").Replace(path.Base(p))+`"`)
	}
	hd.Set("Content-Type", ct)
	hd.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if r.Method == "HEAD" {
		return
	}
	rc, _, err := ex.PackOpen(pack, p, 0, -1)
	if err != nil {
		h.extrasMapErr(w, err)
		return
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 64<<10)
	if _, err := br.Peek(1); err != nil && err != io.EOF {
		h.extrasMapErr(w, err) // wrong key, tampered or plaintext-where-ciphertext-expected: a real error, nothing sent
		return
	}
	if _, err := io.Copy(w, br); err != nil {
		h.Logf("encrypted pack download aborted: %v", err)
		panic(http.ErrAbortHandler)
	}
}

// ---- link resolution (GET path?resolve=1) ----

func (h *Host) resolveRead(w http.ResponseWriter, r *http.Request, pr *Principal, db, p string) {
	ex, ok := h.extrasOf(w, db)
	if !ok {
		return
	}
	depth, err := atoiDefault(r.URL.Query().Get("depth"), 0)
	if err != nil || depth < 0 || depth > 32 {
		writeErr(w, 400, "depth must be 0..32")
		return
	}
	res, err := ex.Resolve(p, depth, h.readAllow(ex, &pr.Key))
	if err != nil {
		h.extrasMapErr(w, err)
		return
	}
	if ex.SecActive() {
		if res.Value, err = ex.SecRead(secPrincipal(&pr.Key), p, res.Value); err != nil {
			h.auditPolicy(r, pr, db, err)
			h.extrasMapErr(w, err)
			return
		}
	}
	w.Header().Set("X-Redx-Applied", strconv.FormatUint(h.Cluster.Offset(), 10))
	if a := r.Header.Get("Accept"); strings.Contains(a, "text/x-redx-box") || strings.HasPrefix(a, "text/plain") {
		w.Header().Set("Content-Type", "text/x-redx-box; charset=utf-8")
		_, _ = io.WriteString(w, res.Value)
		return
	}
	out := map[string]interface{}{"path": p, "value": res.Value, "resolved": true}
	if len(res.Dangling) > 0 {
		out["dangling"] = res.Dangling
	}
	if len(res.Cycles) > 0 {
		out["cycles"] = res.Cycles
	}
	writeJSON(w, 200, out)
}

// ---- pulse ----

func (h *Host) pulseRoute(w http.ResponseWriter, r *http.Request, db string, subscribe bool) {
	if subscribe {
		if r.Method != "GET" {
			writeErr(w, 405, "method not allowed")
			return
		}
		h.pulseSubscribe(w, r, db)
		return
	}
	switch r.Method {
	case "GET":
		if _, ok := h.authz(w, r, ScopeRead, db, "", false); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		st, err := ex.PulseGet()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		writeJSON(w, 200, st)
	case "PUT", "DELETE":
		if _, ok := h.authz(w, r, ScopeAdmin, db, "", false); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		src, del := "", r.Method == "DELETE"
		if !del {
			var err error
			if src, err = declBody(r); err != nil {
				h.mapErr(w, err)
				return
			}
		}
		seq, err := h.WriteAux(db, "pulse", "", src, del)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "pulse.define", DB: db, Remote: remoteIP(r), Detail: strconv.FormatBool(del)})
		h.writeAck(w, r, seq)
	default:
		writeErr(w, 405, "method not allowed")
	}
}
