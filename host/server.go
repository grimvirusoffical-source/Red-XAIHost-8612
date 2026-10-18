package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// KeyView is the API representation of a key record (no hash/salt).
type KeyView struct {
	ID          string     `json:"id"`
	Scope       string     `json:"scope"`
	DB          string     `json:"db"`
	Prefix      string     `json:"prefix,omitempty"`
	Label       string     `json:"label,omitempty"`
	Roles       []string   `json:"roles,omitempty"`
	Created     time.Time  `json:"created"`
	Expires     *time.Time `json:"expires,omitempty"`
	Revoked     *time.Time `json:"revoked,omitempty"`
	RateRPS     float64    `json:"rate_rps,omitempty"`
	RateBurst   int        `json:"rate_burst,omitempty"`
	RotatedFrom string     `json:"rotated_from,omitempty"`
	Status      string     `json:"status"`
}

func viewOf(r KeyRecord, now time.Time) KeyView {
	st := "active"
	if err := r.Active(now); err != nil {
		st = strings.TrimPrefix(err.Error(), "key ")
	}
	return KeyView{ID: r.ID, Scope: r.Scope, DB: r.DB, Prefix: r.Prefix, Label: r.Label, Roles: r.Roles, Created: r.Created,
		Expires: r.Expires, Revoked: r.Revoked, RateRPS: r.RateRPS, RateBurst: r.RateBurst, RotatedFrom: r.RotatedFrom, Status: st}
}

func (h *Host) routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.reqs.Add(1)
		lim := int64(h.Cfg.MaxBodyMB) << 20
		if r.Method == "PUT" && (strings.Contains(r.URL.Path, "/pack/") || strings.Contains(r.URL.Path, "/media/")) {
			lim = int64(h.Cfg.MaxUploadMB) << 20
		}
		r.Body = http.MaxBytesReader(w, r.Body, lim)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		p := r.URL.Path
		switch {
		case p == "/healthz":
			writeJSON(w, 200, map[string]interface{}{"status": "ok", "node": h.NodeID, "role": roleName(h.Cluster), "version": Version})
		case p == "/v1/query", p == "/v1/sql", p == "/v1/stats", p == "/v1/subscribe", strings.HasPrefix(p, "/v1/path/"):
			h.dbRoute(w, r, h.Cfg.DefaultDB, strings.TrimPrefix(p, "/v1/"))
		case strings.HasPrefix(p, "/v1/db/"):
			rest := strings.TrimPrefix(p, "/v1/db/")
			name, sub, _ := strings.Cut(rest, "/")
			if !ValidDBName(name) {
				writeErr(w, 404, "database not found")
				return
			}
			h.dbRoute(w, r, name, sub)
		case p == "/v1/dbs":
			h.dbsRoute(w, r)
		case p == "/v1/keys" || strings.HasPrefix(p, "/v1/keys/"):
			h.keysRoute(w, r)
		case p == "/v1/admin/backup" && r.Method == "GET":
			if pr, ok := h.authz(w, r, ScopeAdmin, AllDBs, "", true); ok {
				h.handleBackup(w, r, pr)
			}
		case strings.HasPrefix(p, "/v1/cluster/"):
			h.clusterRoute(w, r)
		default:
			writeErr(w, 404, "not found")
		}
	})
}

func roleName(c *Cluster) string {
	if c.IsLeader() {
		return "leader"
	}
	return "follower"
}

// authz authenticates and authorizes. When hostLevel the key must be admin on db "*".
func (h *Host) authz(w http.ResponseWriter, r *http.Request, need, db, path string, hostLevel bool) (*Principal, bool) {
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
	if hostLevel && !pr.Key.IsHostAdmin() || !hostLevel && !pr.Key.Allows(need, db, path) {
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Method: r.Method, Route: routeOf(r), Reason: "scope/db/prefix"})
		writeErr(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return pr, true
}

func (h *Host) dbRoute(w http.ResponseWriter, r *http.Request, db, sub string) {
	switch {
	case strings.HasPrefix(sub, "path/"):
		h.pathHandler(w, r, db, strings.TrimPrefix(sub, "path/"))
	case sub == "query" && r.Method == "POST":
		h.queryHandler(w, r, db)
	case sub == "stats" && r.Method == "GET":
		h.statsHandler(w, r, db)
	case sub == "subscribe" && r.Method == "GET":
		h.subscribeHandler(w, r, db)
	default:
		if !h.extrasRoute(w, r, db, sub) {
			writeErr(w, 404, "not found")
		}
	}
}

func (h *Host) mapErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, 404, "not found")
	case errors.Is(err, ErrNoDB):
		writeErr(w, 404, "database not found")
	case errors.Is(err, ErrInvalid):
		writeErr(w, 400, err.Error())
	case errors.Is(err, ErrReadOnly):
		writeErr(w, 503, err.Error())
	case errors.Is(err, ErrForbidden):
		writeErr(w, 403, "forbidden")
	case errors.Is(err, ErrConflict):
		writeErr(w, 409, err.Error())
	case errors.Is(err, ErrUnavailable):
		writeErr(w, 503, err.Error())
	default:
		h.Logf("internal error: %v", err)
		writeErr(w, 500, "internal error")
	}
}

// readBody reads the (size-limited) body and restores it so the request can still be forwarded.
func readBody(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b
}

func (h *Host) maybeForward(w http.ResponseWriter, r *http.Request) bool {
	if h.Cluster.IsLeader() {
		return false
	}
	if h.Cluster.LeaderURL() == "" {
		writeErr(w, 503, "no leader configured")
		return true
	}
	h.Cluster.proxy().ServeHTTP(w, r)
	return true
}

type putBody struct {
	Value             json.RawMessage `json:"value"`
	Box               *string         `json:"box"`
	TTLSeconds        int             `json:"ttl_seconds"`
	Sliding           bool            `json:"sliding"`
	CheckpointSeconds int             `json:"checkpoint_seconds"`
}

func (h *Host) pathHandler(w http.ResponseWriter, r *http.Request, db, path string) {
	if err := ValidPath(path); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case "GET", "HEAD":
		pr, ok := h.authz(w, r, ScopeRead, db, path, false)
		if !ok {
			return
		}
		if ws := r.Header.Get("X-Redx-Wait-Seq"); ws != "" {
			if n, err := strconv.ParseUint(ws, 10, 64); err == nil {
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer cancel()
				if h.Cluster.WaitOffset(ctx, n) != nil {
					writeErr(w, http.StatusGatewayTimeout, "replica has not reached the requested offset")
					return
				}
			}
		}
		if rv := r.URL.Query().Get("resolve"); rv == "1" || rv == "true" {
			h.resolveRead(w, r, pr, db, path)
			return
		}
		v, err := h.ReadGet(db, path)
		if err != nil {
			h.mapErr(w, err)
			return
		}
		if ex, ok := h.backendExtras(db); ok && ex.SecActive() {
			if v, err = ex.SecRead(secPrincipal(&pr.Key), path, v); err != nil {
				h.auditPolicy(r, pr, db, err)
				h.mapErr(w, err)
				return
			}
		}
		w.Header().Set("X-Redx-Applied", strconv.FormatUint(h.Cluster.Offset(), 10))
		if a := r.Header.Get("Accept"); strings.Contains(a, "text/x-redx-box") || strings.HasPrefix(a, "text/plain") {
			w.Header().Set("Content-Type", "text/x-redx-box; charset=utf-8")
			_, _ = io.WriteString(w, v)
			return
		}
		writeJSON(w, 200, map[string]string{"path": path, "value": v})
	case "PUT":
		pr, ok := h.authz(w, r, ScopeWrite, db, path, false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		val, opts, err := parsePut(r)
		if err != nil {
			h.mapErr(w, err)
			return
		}
		if ex, ok := h.backendExtras(db); ok && ex.SecActive() {
			if err := ex.SecWrite(secPrincipal(&pr.Key), path, val, false); err != nil {
				h.auditPolicy(r, pr, db, err)
				h.mapErr(w, err)
				return
			}
		}
		seq, err := h.WriteSet(db, path, val, opts)
		if err != nil {
			h.mapErr(w, err)
			return
		}
		h.writeAck(w, r, seq)
	case "DELETE":
		pr, ok := h.authz(w, r, ScopeWrite, db, path, false)
		if !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		if ex, ok := h.backendExtras(db); ok && ex.SecActive() {
			if err := ex.SecWrite(secPrincipal(&pr.Key), path, "", true); err != nil {
				h.auditPolicy(r, pr, db, err)
				h.mapErr(w, err)
				return
			}
		}
		seq, err := h.WriteDel(db, path)
		if err != nil {
			h.mapErr(w, err)
			return
		}
		h.writeAck(w, r, seq)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		writeErr(w, 405, "method not allowed")
	}
}

func (h *Host) writeAck(w http.ResponseWriter, r *http.Request, seq uint64) {
	w.Header().Set("X-Redx-Seq", strconv.FormatUint(seq, 10))
	acked := -1
	if v := r.Header.Get("X-Redx-Wait-Replicas"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			acked = h.Cluster.WaitReplicas(ctx, seq, n)
			cancel()
			w.Header().Set("X-Redx-Replicas-Acked", strconv.Itoa(acked))
		}
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "seq": seq})
}

func parsePut(r *http.Request) (string, SetOpts, error) {
	var o SetOpts
	q := r.URL.Query()
	o.TTLSeconds, _ = strconv.Atoi(q.Get("ttl"))
	o.Sliding = q.Get("sliding") == "true" || q.Get("sliding") == "1"
	o.CheckpointSeconds, _ = strconv.Atoi(q.Get("checkpoint"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", o, ErrInvalid
	}
	var val string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var pb putBody
		if err := json.Unmarshal(body, &pb); err != nil {
			return "", o, errWrap("invalid JSON body")
		}
		switch {
		case pb.Box != nil:
			val = *pb.Box
		case len(pb.Value) > 0:
			if val, err = JSONToBox(pb.Value); err != nil {
				return "", o, err
			}
		default:
			return "", o, errWrap(`JSON body needs "value" or "box"`)
		}
		if pb.TTLSeconds > 0 {
			o.TTLSeconds = pb.TTLSeconds
		}
		o.Sliding = o.Sliding || pb.Sliding
		if pb.CheckpointSeconds > 0 {
			o.CheckpointSeconds = pb.CheckpointSeconds
		}
	} else {
		val = string(body)
	}
	if o.TTLSeconds < 0 || o.CheckpointSeconds < 0 || o.TTLSeconds > 10*365*86400 {
		return "", o, errWrap("invalid ttl/checkpoint")
	}
	if o.Sliding && o.TTLSeconds == 0 {
		return "", o, errWrap("sliding requires ttl")
	}
	return val, o, nil
}

type wrapErr struct{ msg string }

func (e wrapErr) Error() string { return e.msg }
func (e wrapErr) Unwrap() error { return ErrInvalid }
func errWrap(m string) error    { return wrapErr{m} }

func (h *Host) queryHandler(w http.ResponseWriter, r *http.Request, db string) {
	pr, ok := h.authz(w, r, ScopeRead, db, "", false)
	if !ok {
		return
	}
	if pr.Key.Prefix != "" {
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Reason: "query not allowed for prefix-restricted key"})
		writeErr(w, 403, "forbidden: queries are not available to path-prefix restricted keys")
		return
	}
	if ex, ok := h.backendExtras(db); ok && ex.SecActive() && pr.Key.Scope != ScopeAdmin {
		// raw path queries return whole records, which a field-level policy rule cannot be applied to
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Reason: "path query refused: database has a .red-xai-sec policy; use /sql"})
		writeErr(w, 403, "forbidden: this database has an access policy; use /v1/sql (path queries are admin-only here)")
		return
	}
	body, _ := io.ReadAll(r.Body)
	q := string(body)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var b struct {
			Q string `json:"q"`
		}
		if json.Unmarshal(body, &b) != nil || b.Q == "" {
			writeErr(w, 400, `body must be {"q": "..."}`)
			return
		}
		q = b.Q
	}
	if strings.TrimSpace(q) == "" {
		writeErr(w, 400, "empty query")
		return
	}
	b, err := h.Reg.Get(db)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	res, err := b.Query(q)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	if res == nil {
		res = []string{}
	}
	writeJSON(w, 200, map[string]interface{}{"results": res, "count": len(res)})
}

func (h *Host) statsHandler(w http.ResponseWriter, r *http.Request, db string) {
	pr, ok := h.authz(w, r, ScopeRead, db, "", false)
	if !ok {
		return
	}
	out := map[string]interface{}{}
	dbs := map[string]map[string]float64{}
	if pr.Key.DB == AllDBs && r.URL.Path == "/v1/stats" {
		for _, n := range h.Reg.List() {
			if b, err := h.Reg.Get(n); err == nil {
				dbs[n] = b.Stats()
			}
		}
	} else if b, err := h.Reg.Get(db); err == nil {
		dbs[db] = b.Stats()
	}
	out["dbs"] = dbs
	if pr.Key.IsHostAdmin() {
		out["host"] = map[string]interface{}{
			"version": Version, "node_id": h.NodeID, "role": roleName(h.Cluster), "uptime_seconds": int(time.Since(h.started).Seconds()),
			"requests": h.reqs.Load(), "auth_failures": h.authBad.Load(), "replication_offset": h.Cluster.Offset(),
			"ttl_tracked": h.Tracker.Len(), "keys": h.KS.Count(),
		}
	}
	writeJSON(w, 200, out)
}

func (h *Host) dbsRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		pr, ok := h.authz(w, r, ScopeRead, AllDBs, "", false)
		_ = pr
		if !ok {
			// A db-scoped key may list only its own database.
			return
		}
		writeJSON(w, 200, map[string]interface{}{"dbs": h.Reg.List()})
	case "POST":
		if _, ok := h.authz(w, r, ScopeAdmin, AllDBs, "", true); !ok {
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		var b struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(readBody(r), &b) != nil || b.Name == "" {
			writeErr(w, 400, `body must be {"name": "..."}`)
			return
		}
		seq, err := h.WriteCreateDB(b.Name)
		switch {
		case errors.Is(err, ErrDBExists):
			writeErr(w, 409, "database already exists")
		case err != nil:
			h.mapErr(w, err)
		default:
			h.Audit.Log(AuditEvent{Event: "db.create", DB: b.Name, Remote: remoteIP(r)})
			writeJSON(w, 201, map[string]interface{}{"ok": true, "name": b.Name, "seq": seq})
		}
	default:
		writeErr(w, 405, "method not allowed")
	}
}

// ---- keys API ----

type createKeyReq struct {
	DB        string   `json:"db"`
	Scope     string   `json:"scope"`
	Prefix    string   `json:"prefix"`
	Label     string   `json:"label"`
	Roles     []string `json:"roles"`
	TTL       string   `json:"ttl"`
	RateRPS   float64  `json:"rate_rps"`
	RateBurst int      `json:"rate_burst"`
}

// canManage: may principal administer a key bound to db?
func canManage(p *Principal, db string) bool {
	return p.Key.Scope == ScopeAdmin && p.Key.Prefix == "" && (p.Key.DB == AllDBs || p.Key.DB == db)
}

func (h *Host) keysRoute(w http.ResponseWriter, r *http.Request) {
	pr, wait, err := h.Auth.Authenticate(r)
	if err != nil {
		if errors.Is(err, ErrThrottled) {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
			writeErr(w, 429, "rate limit exceeded")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="redx"`)
		writeErr(w, 401, "unauthorized")
		return
	}
	if pr.Key.Scope != ScopeAdmin {
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, Remote: remoteIP(r), Route: r.URL.Path, Reason: "admin required"})
		writeErr(w, 403, "forbidden")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/keys"), "/")
	now := time.Now()
	switch {
	case rest == "" && r.Method == "GET":
		out := []KeyView{}
		for _, k := range h.KS.List() {
			if canManage(pr, k.DB) {
				out = append(out, viewOf(k, now))
			}
		}
		writeJSON(w, 200, map[string]interface{}{"keys": out})
	case rest == "" && r.Method == "POST":
		var req createKeyReq
		if json.Unmarshal(readBody(r), &req) != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		if req.DB == "" {
			req.DB = pr.Key.DB
		}
		if !canManage(pr, req.DB) {
			h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: req.DB, Remote: remoteIP(r), Reason: "cannot manage that db"})
			writeErr(w, 403, "forbidden")
			return
		}
		ttl, err := ParseTTL(req.TTL)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if h.maybeForward(w, r) {
			return
		}
		key, rec, err := h.KS.Create(CreateSpec{DB: req.DB, Scope: req.Scope, Prefix: req.Prefix, Label: req.Label, Roles: req.Roles, TTL: ttl, RateRPS: req.RateRPS, RateBurst: req.RateBurst})
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		h.Audit.Log(AuditEvent{Event: "key.create", KeyID: pr.Key.ID, DB: rec.DB, Remote: remoteIP(r), Detail: "new key " + rec.ID + " scope " + rec.Scope})
		writeJSON(w, 201, map[string]interface{}{"key": key, "id": rec.ID, "info": viewOf(rec, now), "warning": "this is the only time the key is shown; store it now"})
	default:
		parts := strings.Split(rest, "/")
		id := parts[0]
		target, ok := h.KS.Get(id)
		if !ok || !canManage(pr, target.DB) {
			writeErr(w, 404, "key not found")
			return
		}
		switch {
		case len(parts) == 1 && r.Method == "GET":
			writeJSON(w, 200, viewOf(target, now))
		case len(parts) == 1 && r.Method == "DELETE":
			if h.maybeForward(w, r) {
				return
			}
			rec, err := h.KS.Revoke(id)
			if err != nil {
				writeErr(w, 500, "internal error")
				return
			}
			h.Audit.Log(AuditEvent{Event: "key.revoke", KeyID: pr.Key.ID, DB: rec.DB, Remote: remoteIP(r), Detail: "revoked " + id})
			writeJSON(w, 200, viewOf(rec, now))
		case len(parts) == 2 && parts[1] == "rotate" && r.Method == "POST":
			var req struct {
				Grace string `json:"grace"`
			}
			_ = json.Unmarshal(readBody(r), &req)
			grace, err := ParseTTL(req.Grace)
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			if h.maybeForward(w, r) {
				return
			}
			key, rec, err := h.KS.Rotate(id, grace)
			if err != nil {
				writeErr(w, 409, err.Error())
				return
			}
			h.Audit.Log(AuditEvent{Event: "key.rotate", KeyID: pr.Key.ID, DB: rec.DB, Remote: remoteIP(r), Detail: "rotated " + id + " -> " + rec.ID})
			writeJSON(w, 201, map[string]interface{}{"key": key, "id": rec.ID, "info": viewOf(rec, now), "replaces": id, "warning": "this is the only time the key is shown; store it now"})
		default:
			writeErr(w, 405, "method not allowed")
		}
	}
}

// ---- cluster routes ----

func (h *Host) clusterRoute(w http.ResponseWriter, r *http.Request) {
	c := h.Cluster
	switch strings.TrimPrefix(r.URL.Path, "/v1/cluster/") {
	case "log":
		c.handleLog(w, r)
	case "snapshot":
		c.handleSnapshot(w, r)
	case "join":
		if r.Method != "POST" {
			writeErr(w, 405, "method not allowed")
			return
		}
		c.handleJoin(w, r)
	case "join-token":
		if r.Method != "POST" {
			writeErr(w, 405, "method not allowed")
			return
		}
		if pr, ok := h.authz(w, r, ScopeAdmin, AllDBs, "", true); ok {
			if h.maybeForward(w, r) {
				return
			}
			c.handleJoinToken(w, r, pr)
		}
	case "status":
		if _, ok := h.authz(w, r, ScopeAdmin, AllDBs, "", true); ok {
			writeJSON(w, 200, c.Status())
		}
	case "promote":
		if r.Method != "POST" {
			writeErr(w, 405, "method not allowed")
			return
		}
		if pr, ok := h.authz(w, r, ScopeAdmin, AllDBs, "", true); ok {
			c.handlePromote(w, r, pr)
		}
	case "leader":
		if r.Method != "POST" {
			writeErr(w, 405, "method not allowed")
			return
		}
		pr, ok := h.authz(w, r, ScopeAdmin, AllDBs, "", true)
		if !ok {
			return
		}
		var b struct {
			URL       string `json:"url"`
			JoinToken string `json:"join_token"`
			Pin       string `json:"pin_sha256"`
		}
		if json.NewDecoder(r.Body).Decode(&b) != nil || b.URL == "" {
			writeErr(w, 400, `body must be {"url": "...", "join_token": "..."}`)
			return
		}
		if err := c.Repoint(r.Context(), b.URL, b.JoinToken, b.Pin); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		h.Audit.Log(AuditEvent{Event: "node.repoint", KeyID: pr.Key.ID, Remote: remoteIP(r), Detail: b.URL})
		writeJSON(w, 200, map[string]interface{}{"ok": true, "role": "follower", "leader": b.URL})
	default:
		writeErr(w, 404, "not found")
	}
}
