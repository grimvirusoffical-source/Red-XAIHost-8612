package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// REST surface added in 0.2.0 (all under /v1/db/{db}/, /v1/sql is the default-db shorthand).
//
//	POST   sql                          RXQL: {"q": "...", "params": [...], "explain": false}   read; writes need write
//	GET    media                        list libraries                                         read, no prefix
//	GET    media/{lib}                  list items (manifests)                                 read; filtered by prefix
//	GET    media/{lib}/stat/{name}      one manifest                                           read, prefix
//	GET    media/{lib}/file/{name}      the bytes: HTTP Range / If-Range / HEAD / ETag         read, prefix
//	PUT    media/{lib}/file/{name}      upload (?type=image/png declared type, sniffed)        write, prefix
//	DELETE media/{lib}/file/{name}      delete                                                 write, prefix
//	GET    media/{lib}/fsck[?deep=1]    integrity report                                       admin, no prefix
//	POST   media/{lib}/sweep            remove orphaned data                                   admin, no prefix
//	GET    sec                          loaded policy summary (source for admins)              read, no prefix
//	PUT    sec / DELETE sec             replace / remove the API-managed .red-xai-sec          admin, no prefix
//	GET    code                         scripts                                                read, no prefix
//	PUT    code/file/{file}             define a .red-xai-code file (replicated)               admin, no prefix
//	DELETE code/file/{file}              remove it                                              admin, no prefix
//	POST   code/run/{script}            run a script {"args": [...]}                           write

const sqlTimeout = 30 * time.Second

func secPrincipal(k *KeyRecord) SecPrincipal {
	return SecPrincipal{KeyID: k.ID, Scope: k.Scope, Prefix: k.Prefix, Roles: k.Roles}
}

// splitResource turns an engine path into the (file, box, field) the policy engine addresses.
// Record ids are dropped; indexes inside the field path are dropped too.
func splitResource(p string) (file, box, field string) {
	file, rest, ok := strings.Cut(p, ":")
	if !ok {
		return p, "", ""
	}
	j := strings.IndexAny(rest, "[.")
	if j < 0 {
		return file, rest, ""
	}
	box, rest = rest[:j], rest[j:]
	if strings.HasPrefix(rest, "[") {
		if k := strings.IndexByte(rest, ']'); k >= 0 {
			rest = rest[k+1:]
		}
	}
	rest = strings.TrimPrefix(rest, ".")
	var sb strings.Builder
	depth := 0
	for _, c := range rest {
		switch {
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			sb.WriteRune(c)
		}
	}
	return file, box, sb.String()
}

// readAllow combines the key's path prefix with the database policy for record-granular
// filters (index hits, link targets, events). nil = no restriction at all.
func (h *Host) readAllow(ex Extras, k *KeyRecord) func(string) bool {
	pre := allowFor(k)
	if pre == nil && !ex.SecActive() {
		return nil
	}
	sp := secPrincipal(k)
	return func(p string) bool {
		if pre != nil && !pre(p) {
			return false
		}
		f, b, _ := splitResource(p)
		return ex.SecAllow(sp, "read", f, b, "")
	}
}

// secFile gates a whole-file resource (a pack or media library) with the policy.
func (h *Host) secFile(w http.ResponseWriter, ex Extras, pr *Principal, action, file string) bool {
	if !ex.SecAllow(secPrincipal(&pr.Key), action, file, "", "") {
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, Reason: "policy " + action + " " + file})
		writeErr(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

// backendExtras returns the Extras of a database without answering errors.
func (h *Host) backendExtras(db string) (Extras, bool) {
	b, err := h.Reg.Get(db)
	if err != nil {
		return nil, false
	}
	ex, ok := b.(Extras)
	return ex, ok
}

func (h *Host) servicesRoute(w http.ResponseWriter, r *http.Request, db, sub string) bool {
	switch {
	case sub == "sql":
		h.sqlHandler(w, r, db)
	case sub == "sec":
		h.secRoute(w, r, db)
	case sub == "code" || strings.HasPrefix(sub, "code/"):
		h.codeRoute(w, r, db, strings.TrimPrefix(strings.TrimPrefix(sub, "code"), "/"))
	case sub == "media" || strings.HasPrefix(sub, "media/"):
		h.mediaRoute(w, r, db, strings.TrimPrefix(strings.TrimPrefix(sub, "media"), "/"))
	default:
		return false
	}
	return true
}

func isWriteSQL(q string) bool {
	q = strings.TrimLeft(q, " \t\r\n")
	i := 0
	for i < len(q) && (q[i] >= 'A' && q[i] <= 'Z' || q[i] >= 'a' && q[i] <= 'z') {
		i++
	}
	switch strings.ToUpper(q[:i]) {
	case "INSERT", "UPDATE", "DELETE":
		return true
	}
	return false
}

// ---- SQL ----

type sqlReq struct {
	Q       string        `json:"q"`
	Params  []interface{} `json:"params"`
	Args    []interface{} `json:"args"`
	Explain bool          `json:"explain"`
}

func (h *Host) sqlHandler(w http.ResponseWriter, r *http.Request, db string) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		writeErr(w, 405, "method not allowed")
		return
	}
	pr, ok := h.authzScope(w, r, ScopeRead, db)
	if !ok {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.extrasMapErr(w, err)
		return
	}
	var req sqlReq
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if dec.Decode(&req) != nil || strings.TrimSpace(req.Q) == "" {
			writeErr(w, 400, `body must be {"q": "...", "params": [...]}`)
			return
		}
		if req.Params == nil {
			req.Params = req.Args
		}
	} else {
		req.Q = string(body)
	}
	if strings.TrimSpace(req.Q) == "" {
		writeErr(w, 400, "empty query")
		return
	}
	write := !req.Explain && isWriteSQL(req.Q)
	if write && scopeRank[pr.Key.Scope] < scopeRank[ScopeWrite] {
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Reason: "sql write needs write scope"})
		writeErr(w, 403, "forbidden")
		return
	}
	if write && h.maybeForward(w, r) {
		return
	}
	ex, ok := h.extrasOf(w, db)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sqlTimeout)
	defer cancel()
	if write {
		h.pause.RLock()
		defer h.pause.RUnlock()
	}
	res, err := ex.SQL(ctx, secPrincipal(&pr.Key), req.Q, req.Params, req.Explain)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Reason: "sql: " + err.Error()})
		}
		h.extrasMapErr(w, err)
		return
	}
	if write {
		h.Audit.Log(AuditEvent{Event: "sql.write", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Detail: res.Kind + " affected=" + strconv.Itoa(res.Affected)})
	}
	w.Header().Set("X-Redx-Applied", strconv.FormatUint(h.Cluster.Offset(), 10))
	out := map[string]interface{}{"kind": res.Kind, "columns": res.Columns, "rows": res.Rows, "rows_box": res.RowsBox,
		"count": len(res.Rows), "affected": res.Affected, "access": res.Access, "scanned": res.Scanned}
	if res.Columns == nil {
		out["columns"] = []string{}
	}
	if len(res.Paths) > 0 {
		out["paths"] = res.Paths
	}
	if len(res.Cascaded) > 0 {
		out["cascaded"] = res.Cascaded
	}
	if len(res.Plan) > 0 {
		out["plan"] = res.Plan
	}
	writeJSON(w, 200, out)
}

// ---- sec ----

func (h *Host) secRoute(w http.ResponseWriter, r *http.Request, db string) {
	switch r.Method {
	case "GET":
		pr, ok := h.authz(w, r, ScopeRead, db, "", false)
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
		info, err := ex.SecInfo(pr.Key.Scope == ScopeAdmin)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		writeJSON(w, 200, info)
	case "PUT", "DELETE":
		pr, ok := h.authz(w, r, ScopeAdmin, db, "", false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		src, del := "", r.Method == "DELETE"
		if !del {
			var err error
			if src, err = definitionBody(r, "policy"); err != nil {
				h.mapErr(w, err)
				return
			}
		}
		seq, err := h.WriteAux(db, "sec", "", src, del)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "sec.define", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Detail: "delete=" + strconv.FormatBool(del)})
		h.writeAck(w, r, seq)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

// definitionBody reads plain text or {"<field>": "..."} JSON.
func definitionBody(r *http.Request, field string) (string, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return "", ErrInvalid
	}
	s := string(b)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var o map[string]string
		if json.Unmarshal(b, &o) != nil {
			return "", errWrap(`body must be {"` + field + `": "..."} or plain text`)
		}
		s = o[field]
	}
	if strings.TrimSpace(s) == "" {
		return "", errWrap("empty definition")
	}
	return s, nil
}

// ---- code ----

func (h *Host) codeRoute(w http.ResponseWriter, r *http.Request, db, rest string) {
	switch {
	case rest == "" && r.Method == "GET":
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
		sc, err := ex.CodeList()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		if sc == nil {
			sc = []CodeScript{}
		}
		writeJSON(w, 200, map[string]interface{}{"scripts": sc})
	case strings.HasPrefix(rest, "file/") && (r.Method == "PUT" || r.Method == "DELETE"):
		file := strings.TrimPrefix(rest, "file/")
		if !setNameRe.MatchString(file) {
			writeErr(w, 400, "code file names are letters, digits, '_' and '-' (max 64)")
			return
		}
		pr, ok := h.authz(w, r, ScopeAdmin, db, "", false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		src, del := "", r.Method == "DELETE"
		if !del {
			var err error
			if src, err = definitionBody(r, "source"); err != nil {
				h.mapErr(w, err)
				return
			}
		}
		seq, err := h.WriteAux(db, "code", file, src, del)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "code.define", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Detail: file})
		h.writeAck(w, r, seq)
	case strings.HasPrefix(rest, "run/") && r.Method == "POST":
		script := strings.TrimPrefix(rest, "run/")
		if !setNameRe.MatchString(script) {
			writeErr(w, 400, "bad script name")
			return
		}
		pr, ok := h.authzScope(w, r, ScopeWrite, db) // scripts may write; every record they touch is checked again
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		var req struct {
			Args []interface{} `json:"args"`
		}
		if b, _ := io.ReadAll(r.Body); len(bytes.TrimSpace(b)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.UseNumber()
			if dec.Decode(&req) != nil {
				writeErr(w, 400, `body must be {"args": [...]}`)
				return
			}
		}
		args := make([]interface{}, len(req.Args))
		for i, a := range req.Args {
			if n, ok := a.(json.Number); ok {
				f, err := n.Float64()
				if err != nil {
					writeErr(w, 400, "bad number argument")
					return
				}
				a = f
			}
			args[i] = a
		}
		ex, ok := h.extrasOf(w, db)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), sqlTimeout)
		defer cancel()
		h.pause.RLock()
		res, err := ex.CodeRun(ctx, secPrincipal(&pr.Key), script, args)
		h.pause.RUnlock()
		h.Audit.Log(AuditEvent{Event: "code.run", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Detail: script + " host_calls=" + strconv.Itoa(res.HostCalls)})
		if err != nil {
			if errors.Is(err, ErrForbidden) {
				h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Reason: "script " + script + ": " + err.Error()})
			}
			h.extrasMapErr(w, err)
			return
		}
		w.Header().Set("X-Redx-Applied", strconv.FormatUint(h.Cluster.Offset(), 10))
		writeJSON(w, 200, res)
	default:
		writeErr(w, 404, "not found")
	}
}

// ---- media ----

func mediaACL(lib, name string) string { return lib + ".red-xai-media:" + name }

func validMediaName(n string) bool {
	if n == "" || len(n) > 255 || strings.ContainsAny(n, "/\\:") || n == "." || n == ".." {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f || r == 0xfffd {
			return false
		}
	}
	return true
}

func (h *Host) mediaRoute(w http.ResponseWriter, r *http.Request, db, rest string) {
	if rest == "" {
		if r.Method != "GET" {
			writeErr(w, 405, "method not allowed")
			return
		}
		pr, ok := h.authz(w, r, ScopeRead, db, "", false)
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
		libs, err := ex.MediaLibs()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		out := []string{}
		for _, l := range libs {
			if ex.SecAllow(secPrincipal(&pr.Key), "read", l+".red-xai-media", "", "") {
				out = append(out, l)
			}
		}
		writeJSON(w, 200, map[string]interface{}{"libraries": out})
		return
	}
	lib, rest2, _ := strings.Cut(rest, "/")
	verb, name, _ := strings.Cut(rest2, "/")
	if !setNameRe.MatchString(lib) {
		writeErr(w, 400, "media library names are letters, digits, '_' and '-' (max 64)")
		return
	}
	file := lib + ".red-xai-media"
	switch {
	case verb == "" && r.Method == "GET":
		pr, ok := h.authzScope(w, r, ScopeRead, db)
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
		if !h.secFile(w, ex, pr, "read", file) {
			return
		}
		items, err := ex.MediaList(lib)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		allow := allowFor(&pr.Key)
		out := []MediaInfo{}
		for _, it := range items {
			if allow == nil || allow(mediaACL(lib, it.Name)) {
				out = append(out, it)
			}
		}
		writeJSON(w, 200, map[string]interface{}{"library": lib, "items": out, "count": len(out)})
	case verb == "fsck" && r.Method == "GET", verb == "sweep" && r.Method == "POST":
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
		if verb == "fsck" {
			res, err := ex.MediaFsck(lib, r.URL.Query().Get("deep") == "1")
			if err != nil {
				h.extrasMapErr(w, err)
				return
			}
			writeJSON(w, 200, res)
			return
		}
		h.pause.RLock()
		n, err := ex.MediaSweep(lib)
		h.pause.RUnlock()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]interface{}{"removed": n})
	case (verb == "stat" || verb == "file") && validMediaName(name):
		h.mediaItem(w, r, db, lib, verb, name)
	case verb == "stat" || verb == "file":
		writeErr(w, 400, "invalid media name")
	default:
		writeErr(w, 404, "not found")
	}
}

func (h *Host) mediaItem(w http.ResponseWriter, r *http.Request, db, lib, verb, name string) {
	acl, file := mediaACL(lib, name), lib+".red-xai-media"
	switch {
	case (verb == "stat" && r.Method == "GET") || (verb == "file" && (r.Method == "GET" || r.Method == "HEAD")):
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
		if !h.secFile(w, ex, pr, "read", file) {
			return
		}
		if verb == "stat" {
			m, err := ex.MediaStat(lib, name)
			if err != nil {
				h.extrasMapErr(w, err)
				return
			}
			writeJSON(w, 200, m)
			return
		}
		f, err := ex.MediaOpen(lib, name)
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		defer f.Close()
		m := f.Info()
		hd := w.Header()
		hd.Set("Content-Type", m.MIME)
		hd.Set("ETag", `"sha256-`+m.SHA256+`"`)
		hd.Set("X-Redx-Media-Type", m.Type)
		hd.Set("Content-Security-Policy", "sandbox; default-src 'none'")
		hd.Set("Content-Disposition", `inline; filename="`+strings.NewReplacer(`"`, "", `\`, "", "\r", "", "\n", "").Replace(name)+`"`)
		// ServeContent implements Range (single and multi), If-Range, If-None-Match and HEAD; the media
		// reader verifies every chunk it reads. A read error after the headers aborts the connection.
		http.ServeContent(w, r, "", time.Time{}, f)
	case verb == "file" && r.Method == "PUT":
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
		if !h.secFile(w, ex, pr, "write", file) {
			return
		}
		want := strings.ToLower(strings.TrimSpace(firstNonEmpty(r.Header.Get("X-Redx-SHA256"), r.URL.Query().Get("sha256"))))
		if want != "" && (len(want) != 64 || strings.Trim(want, "0123456789abcdef") != "") {
			writeErr(w, 400, "sha256 must be 64 hex characters")
			return
		}
		declared := r.URL.Query().Get("type")
		if declared == "" {
			ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
			if strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") {
				declared = ct
			}
		}
		h.pause.RLock()
		sr := &sumReader{r: r.Body, h: sha256.New(), want: want}
		m, err := ex.MediaPut(lib, name, declared, sr)
		h.pause.RUnlock()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "media.put", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Detail: lib + " " + m.MIME + " size=" + strconv.FormatInt(m.Size, 10)})
		w.Header().Set("ETag", `"sha256-`+m.SHA256+`"`)
		writeJSON(w, 201, m)
	case verb == "file" && r.Method == "DELETE":
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
		if !h.secFile(w, ex, pr, "delete", file) {
			return
		}
		h.pause.RLock()
		err := ex.MediaDelete(lib, name)
		h.pause.RUnlock()
		if err != nil {
			h.extrasMapErr(w, err)
			return
		}
		h.Audit.Log(AuditEvent{Event: "media.delete", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Detail: lib})
		writeJSON(w, 200, map[string]interface{}{"ok": true})
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		writeErr(w, 405, "method not allowed")
	}
}

func (h *Host) auditPolicy(r *http.Request, pr *Principal, db string, err error) {
	if errors.Is(err, ErrForbidden) {
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Method: r.Method, Route: routeOf(r), Reason: "policy"})
	}
}
