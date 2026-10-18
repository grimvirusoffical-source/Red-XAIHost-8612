package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ClusterHook is the seam between the HTTP/data path and replication. The
// bundled implementation (*Cluster) is a simple async leader/follower scheme;
// a consensus-based implementation (Raft/…) could replace it by implementing
// this interface.
type ClusterHook interface {
	IsLeader() bool
	LeaderURL() string // "" on the leader
	// Replicate appends an already-applied write to the log, returning its offset.
	Replicate(e LogEntry) (uint64, error)
	Offset() uint64
	WaitOffset(ctx context.Context, off uint64) error
	// WaitReplicas blocks until n followers acked >= off (or ctx ends); returns acked count.
	WaitReplicas(ctx context.Context, off uint64, n int) int
	Status() ClusterStatus
}

var _ ClusterHook = (*Cluster)(nil)

// Member is one node as known to the leader.
type Member struct {
	ID         string    `json:"id"`
	Addr       string    `json:"addr,omitempty"`
	Role       string    `json:"role"`
	LastSeen   time.Time `json:"last_seen"`
	Acked      uint64    `json:"acked"`
	Lag        uint64    `json:"lag"`
	Healthy    bool      `json:"healthy"`
	SecretHash string    `json:"secret_sha256,omitempty"`
	Joined     time.Time `json:"joined,omitempty"`
}

// ClusterStatus is returned by /v1/cluster/status.
type ClusterStatus struct {
	NodeID      string   `json:"node_id"`
	Role        string   `json:"role"`
	LeaderURL   string   `json:"leader_url,omitempty"`
	ReplID      string   `json:"replication_id"`
	Offset      uint64   `json:"offset"`
	FirstOffset uint64   `json:"first_offset"`
	ParentID    string   `json:"parent_replication_id,omitempty"`
	ParentOff   uint64   `json:"parent_offset,omitempty"`
	Members     []Member `json:"members"`
	TTLTracked  int      `json:"ttl_tracked"`
	LastError   string   `json:"last_error,omitempty"`
	LastSync    string   `json:"last_sync,omitempty"`
	Snapshots   int      `json:"snapshots_installed"`
}

type nodeState struct {
	Role       string `json:"role"` // leader | follower
	LeaderURL  string `json:"leader_url,omitempty"`
	NodeSecret string `json:"node_secret,omitempty"`
	Pin        string `json:"pin_sha256,omitempty"`
	Advertise  string `json:"advertise,omitempty"`
}

type replMeta struct {
	ReplID    string `json:"replid"`
	ParentID  string `json:"parent_replid,omitempty"`
	ParentOff uint64 `json:"parent_offset,omitempty"`
}

// Cluster implements ClusterHook.
type Cluster struct {
	h   *Host
	log *OpLog
	dir string

	mu       sync.Mutex
	st       nodeState
	meta     replMeta
	members  map[string]*Member
	tokens   map[[32]byte]time.Time
	ackCh    chan struct{}
	lastErr  string
	lastSync time.Time
	client   *http.Client
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	loopStop chan struct{}
	ctx      context.Context
	snaps    int
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newCluster(h *Host) (*Cluster, error) {
	dir := filepath.Join(h.Cfg.DataDir, "cluster")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lg, err := OpenOpLog(filepath.Join(dir, "oplog.jsonl"), h.Cfg.ReplLogMax)
	if err != nil {
		return nil, err
	}
	c := &Cluster{h: h, log: lg, dir: dir, members: map[string]*Member{}, tokens: map[[32]byte]time.Time{}, ackCh: make(chan struct{})}
	c.st = nodeState{Role: h.Cfg.Role, Advertise: h.Cfg.Advertise}
	if h.Cfg.Role == "follower" && len(h.Cfg.Peers) > 0 {
		c.st.LeaderURL = strings.TrimRight(h.Cfg.Peers[0], "/")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cluster.json")); err == nil {
		var s nodeState
		if json.Unmarshal(b, &s) == nil && s.Role != "" {
			c.st = s // persisted state (join / promote / repoint) wins over config
			if c.st.Advertise == "" {
				c.st.Advertise = h.Cfg.Advertise
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "repl.json")); err == nil {
		_ = json.Unmarshal(b, &c.meta)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "members.json")); err == nil {
		var ms []*Member
		if json.Unmarshal(b, &ms) == nil {
			for _, m := range ms {
				c.members[m.ID] = m
			}
		}
	}
	if c.meta.ReplID == "" && c.st.Role == "leader" {
		c.meta.ReplID = randHex(20)
		_ = c.saveMeta()
	}
	c.client = c.newClient()
	return c, nil
}

func (c *Cluster) saveMeta() error {
	b, _ := json.Marshal(c.meta)
	return atomicWrite(filepath.Join(c.dir, "repl.json"), b)
}

func (c *Cluster) saveState() error {
	b, _ := json.MarshalIndent(c.st, "", " ")
	return atomicWrite(filepath.Join(c.dir, "cluster.json"), b)
}

func (c *Cluster) saveMembers() {
	ms := make([]*Member, 0, len(c.members))
	for _, m := range c.members {
		ms = append(ms, m)
	}
	b, _ := json.Marshal(ms)
	_ = atomicWrite(filepath.Join(c.dir, "members.json"), b)
}

func atomicWrite(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// newClient builds the node-to-node HTTP client, honouring certificate pinning.
func (c *Cluster) newClient() *http.Client {
	tr := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second}
	if pin := strings.TrimPrefix(c.st.Pin, "sha256:"); pin != "" {
		tr.TLSClientConfig = pinnedTLS(pin)
	}
	return &http.Client{Transport: tr}
}

// pinnedTLS trusts exactly the certificate whose SHA-256 (hex) equals pin.
func pinnedTLS(pin string) *tls.Config {
	pin = strings.ToLower(strings.ReplaceAll(pin, ":", ""))
	return &tls.Config{
		InsecureSkipVerify: true, // verification replaced by the pin check below
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no peer certificate")
			}
			sum := sha256.Sum256(raw[0])
			if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(pin)) != 1 {
				return errors.New("peer certificate does not match pinned fingerprint")
			}
			return nil
		},
	}
}

// ---- ClusterHook ----

func (c *Cluster) IsLeader() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.st.Role == "leader" }

func (c *Cluster) LeaderURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.Role == "leader" {
		return ""
	}
	return c.st.LeaderURL
}

func (c *Cluster) Replicate(e LogEntry) (uint64, error) { return c.log.Append(e) }
func (c *Cluster) Offset() uint64                       { return c.log.Last() }

func (c *Cluster) WaitOffset(ctx context.Context, off uint64) error {
	for c.log.Last() < off {
		ch := c.log.Wait()
		if c.log.Last() >= off {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Cluster) ackedCount(off uint64) int {
	n := 0
	for _, m := range c.members {
		if m.Acked >= off {
			n++
		}
	}
	return n
}

func (c *Cluster) WaitReplicas(ctx context.Context, off uint64, n int) int {
	for {
		c.mu.Lock()
		got, ch := c.ackedCount(off), c.ackCh
		c.mu.Unlock()
		if got >= n {
			return got
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return got
		}
	}
}

func (c *Cluster) Status() ClusterStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	last := c.log.Last()
	st := ClusterStatus{NodeID: c.h.NodeID, Role: c.st.Role, LeaderURL: c.st.LeaderURL, ReplID: c.meta.ReplID,
		Offset: last, FirstOffset: c.log.First(), ParentID: c.meta.ParentID, ParentOff: c.meta.ParentOff,
		TTLTracked: c.h.Tracker.Len(), Snapshots: c.snaps, LastError: c.lastErr, Members: []Member{}}
	if !c.lastSync.IsZero() {
		st.LastSync = c.lastSync.UTC().Format(time.RFC3339)
	}
	if c.st.Role == "leader" {
		st.LeaderURL = ""
		st.Members = append(st.Members, Member{ID: c.h.NodeID, Addr: c.st.Advertise, Role: "leader", LastSeen: time.Now().UTC(), Acked: last, Healthy: true})
	}
	now := time.Now()
	for _, m := range c.members {
		mm := *m
		mm.SecretHash = ""
		if last > mm.Acked {
			mm.Lag = last - mm.Acked
		}
		mm.Healthy = now.Sub(mm.LastSeen) < time.Duration(c.h.Cfg.Heartbeat)*5*time.Second+10*time.Second
		st.Members = append(st.Members, mm)
	}
	sort.Slice(st.Members, func(i, j int) bool { return st.Members[i].ID < st.Members[j].ID })
	return st
}

// ---- lifecycle ----

func (c *Cluster) start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	c.ctx = ctx
	// Rebuild TTL deadlines from the retained log (covers anything newer than the last tracker save).
	for from := c.log.First(); ; {
		es, err := c.log.Read(from, 1000)
		if err != nil || len(es) == 0 {
			break
		}
		for _, e := range es {
			c.trackEntry(e)
		}
		from = es[len(es)-1].Seq + 1
	}
	if c.IsLeader() {
		return
	}
	c.startFollower()
}

func (c *Cluster) startFollower() {
	c.loopStop = make(chan struct{})
	stop := c.loopStop
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.followLoop(c.ctx, stop)
	}()
}

func (c *Cluster) stopFollower() {
	if c.loopStop != nil {
		close(c.loopStop)
		c.loopStop = nil
	}
}

func (c *Cluster) stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	c.log.Close()
}

func (c *Cluster) trackEntry(e LogEntry) {
	switch e.Op {
	case "set":
		c.h.Tracker.Apply(e.DB, e.Path, e.Opts, e.At)
	case "del":
		c.h.Tracker.Remove(e.DB, e.Path)
	}
}

// ---- follower ----

func (c *Cluster) nodeHeaders(req *http.Request) {
	c.mu.Lock()
	secret := c.st.NodeSecret
	adv := c.st.Advertise
	c.mu.Unlock()
	if secret == "" {
		secret = c.h.Cfg.ClusterKey
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("X-Redx-Node", c.h.NodeID)
	if adv != "" {
		req.Header.Set("X-Redx-Addr", adv)
	}
}

type logResp struct {
	ReplID       string     `json:"replid"`
	Entries      []LogEntry `json:"entries"`
	Last         uint64     `json:"last"`
	NeedSnapshot bool       `json:"need_snapshot"`
	Members      []Member   `json:"members"`
	Error        string     `json:"error"`
}

func (c *Cluster) setErr(err error) {
	c.mu.Lock()
	if err == nil {
		c.lastErr = ""
		c.lastSync = time.Now()
	} else {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
}

func (c *Cluster) followLoop(ctx context.Context, stop <-chan struct{}) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		default:
		}
		err := c.pullOnce(ctx, stop)
		c.setErr(err)
		if err == nil {
			backoff = time.Second
			continue
		}
		c.h.Logf("replication: %v (retry in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-time.After(backoff):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

func (c *Cluster) pullOnce(ctx context.Context, stop <-chan struct{}) error {
	c.mu.Lock()
	leader := c.st.LeaderURL
	replid := c.meta.ReplID
	c.mu.Unlock()
	if leader == "" {
		return errors.New("no leader configured")
	}
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx2.Done():
		}
	}()
	next := c.log.Last() + 1
	q := url.Values{"from": {strconv.FormatUint(next, 10)}, "replid": {replid}, "wait_ms": {strconv.Itoa(c.h.Cfg.Heartbeat * 1000)}, "limit": {"500"}}
	req, _ := http.NewRequestWithContext(ctx2, "GET", leader+"/v1/cluster/log?"+q.Encode(), nil)
	c.nodeHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var lr logResp
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	_ = json.Unmarshal(body, &lr)
	switch {
	case resp.StatusCode == http.StatusConflict && lr.NeedSnapshot:
		c.h.Logf("replication: leader requires full snapshot (replid/offset not resumable)")
		return c.installSnapshot(ctx2, leader)
	case resp.StatusCode != 200:
		return fmt.Errorf("leader answered %d: %s", resp.StatusCode, lr.Error)
	}
	c.mu.Lock()
	if lr.ReplID != "" && lr.ReplID != c.meta.ReplID {
		c.meta.ReplID = lr.ReplID // partial resync across a failover
		_ = c.saveMeta()
	}
	if len(lr.Members) > 0 {
		c.members = map[string]*Member{}
		for i := range lr.Members {
			m := lr.Members[i]
			c.members[m.ID] = &m
		}
	}
	c.mu.Unlock()
	for _, e := range lr.Entries {
		if err := c.applyEntry(e); err != nil {
			if errors.Is(err, ErrNeedResync) {
				c.h.Logf("replication: backend asked for a full resync at seq %d", e.Seq)
				return c.installSnapshot(ctx2, leader)
			}
			return fmt.Errorf("apply seq %d: %w", e.Seq, err)
		}
	}
	return nil
}

// applyEntry applies a replicated entry locally. Note: TTLs are NOT applied to
// the local store; followers never expire keys, the leader sends explicit dels.
func (c *Cluster) applyEntry(e LogEntry) error {
	h := c.h
	h.pause.RLock()
	defer h.pause.RUnlock()
	switch e.Op {
	case "createdb":
		if err := h.Reg.Ensure(e.DB); err != nil {
			return err
		}
	case "engine":
		if err := h.Reg.Ensure(e.DB); err != nil {
			return err
		}
		b, err := h.Reg.Get(e.DB)
		if err != nil {
			return err
		}
		rb, ok := b.(ReplBackend)
		if !ok {
			return fmt.Errorf("db %q: backend cannot apply engine replication entries", e.DB)
		}
		if err := rb.ApplyReplicated([]byte(e.Value)); err != nil {
			return err
		}
	case "set":
		if err := h.Reg.Ensure(e.DB); err != nil {
			return err
		}
		b, err := h.Reg.Get(e.DB)
		if err != nil {
			return err
		}
		o := e.Opts
		o.TTLSeconds, o.Sliding = 0, false
		if err := b.Set(e.Path, e.Value, o); err != nil {
			return err
		}
	case "del":
		if b, err := h.Reg.Get(e.DB); err == nil {
			if err := b.Delete(e.Path); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
	case "aux":
		if err := h.Reg.Ensure(e.DB); err != nil {
			return err
		}
		b, err := h.Reg.Get(e.DB)
		if err != nil {
			return err
		}
		kind, name, _ := strings.Cut(e.Path, "/")
		if err := applyAux(b, kind, name, e.Value, e.Reason == "delete"); err != nil {
			return fmt.Errorf("apply %s definition for %s: %w", kind, e.DB, err)
		}
	case "key":
		if e.Key != nil {
			if err := h.KS.Upsert(*e.Key); err != nil {
				return err
			}
		}
	default:
		h.Logf("replication: ignoring unknown op %q at seq %d", e.Op, e.Seq)
	}
	c.trackEntry(e)
	return c.log.AppendAt(e)
}

// ---- leader-side HTTP handlers ----

func (c *Cluster) nodeAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	ip := remoteIP(r)
	if wait := c.h.Auth.fails.blocked(ip); wait > 0 {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts")
		return "", false
	}
	h := r.Header.Get("Authorization")
	tok := ""
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		tok = strings.TrimSpace(h[7:])
	}
	id := r.Header.Get("X-Redx-Node")
	ok := false
	if tok != "" && id != "" && len(id) < 128 {
		sum := sha256.Sum256([]byte(tok))
		sh := hex.EncodeToString(sum[:])
		c.mu.Lock()
		for _, m := range c.members {
			if m.SecretHash != "" && subtle.ConstantTimeCompare([]byte(m.SecretHash), []byte(sh)) == 1 && m.ID == id {
				ok = true
			}
		}
		c.mu.Unlock()
		if !ok && c.h.Cfg.ClusterKey != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(c.h.Cfg.ClusterKey)) == 1 {
			ok = true
		}
	}
	if !ok {
		c.h.Auth.fails.record(ip)
		c.h.Audit.Log(AuditEvent{Event: "node.auth.fail", Remote: ip, Route: r.URL.Path})
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	return id, true
}

func (c *Cluster) touchMember(id, addr string, acked uint64, role string) {
	c.mu.Lock()
	m := c.members[id]
	if m == nil {
		m = &Member{ID: id, Joined: time.Now().UTC()} // static-secret node announcing itself
		c.members[id] = m
		defer func() { c.mu.Lock(); c.saveMembers(); c.mu.Unlock() }()
	}
	m.LastSeen = time.Now().UTC()
	if addr != "" {
		m.Addr = addr
	}
	m.Role = role
	if acked > m.Acked || acked < m.Acked {
		m.Acked = acked
	}
	close(c.ackCh)
	c.ackCh = make(chan struct{})
	c.mu.Unlock()
}

func (c *Cluster) handleLog(w http.ResponseWriter, r *http.Request) {
	id, ok := c.nodeAuth(w, r)
	if !ok {
		return
	}
	if !c.IsLeader() {
		writeErr(w, http.StatusMisdirectedRequest, "not the leader")
		return
	}
	q := r.URL.Query()
	from, _ := strconv.ParseUint(q.Get("from"), 10, 64)
	if from == 0 {
		from = 1
	}
	replid := q.Get("replid")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	waitMS, _ := strconv.Atoi(q.Get("wait_ms"))
	if waitMS > 30000 {
		waitMS = 30000
	}
	c.mu.Lock()
	cur, parent, poff := c.meta.ReplID, c.meta.ParentID, c.meta.ParentOff
	c.mu.Unlock()
	last, first := c.log.Last(), c.log.First()

	resumable := false
	switch {
	case replid == cur || (replid == "" && from == 1):
		resumable = from >= first && from <= last+1
	case parent != "" && replid == parent:
		resumable = from-1 <= poff && from >= first && from <= last+1
	}
	if !resumable {
		c.touchMember(id, r.Header.Get("X-Redx-Addr"), 0, "follower")
		writeJSON(w, http.StatusConflict, logResp{NeedSnapshot: true, ReplID: cur, Last: last})
		return
	}
	// "from" implies the follower applied from-1.
	c.touchMember(id, r.Header.Get("X-Redx-Addr"), from-1, "follower")
	es, err := c.log.Read(from, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(es) == 0 && waitMS > 0 {
		ch := c.log.Wait()
		if c.log.Last() < from {
			select {
			case <-ch:
			case <-time.After(time.Duration(waitMS) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		es, err = c.log.Read(from, limit)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, logResp{ReplID: cur, Entries: es, Last: c.log.Last(), Members: c.memberList()})
}

func (c *Cluster) memberList() []Member {
	st := c.Status()
	return st.Members
}

func (c *Cluster) handleJoinToken(w http.ResponseWriter, r *http.Request, p *Principal) {
	if !c.IsLeader() {
		writeErr(w, 409, "join tokens are issued by the leader")
		return
	}
	var body struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body)
	ttl := time.Duration(body.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	tok := "rxj_" + randBase62(43)
	sum := sha256.Sum256([]byte(tok))
	exp := time.Now().Add(ttl)
	c.mu.Lock()
	for k, v := range c.tokens {
		if time.Now().After(v) {
			delete(c.tokens, k)
		}
	}
	c.tokens[sum] = exp
	c.mu.Unlock()
	c.h.Audit.Log(AuditEvent{Event: "join.token.create", KeyID: p.Key.ID, Remote: remoteIP(r)})
	writeJSON(w, 200, map[string]interface{}{"token": tok, "expires": exp.UTC().Format(time.RFC3339), "single_use": true})
}

type joinReq struct {
	Token  string `json:"token"`
	NodeID string `json:"node_id"`
	Addr   string `json:"addr"`
}

type joinResp struct {
	NodeSecret string `json:"node_secret"`
	LeaderID   string `json:"leader_id"`
	ReplID     string `json:"replid"`
}

func (c *Cluster) handleJoin(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if wait := c.h.Auth.fails.blocked(ip); wait > 0 {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts")
		return
	}
	if !c.IsLeader() {
		writeErr(w, 409, "not the leader")
		return
	}
	var jr joinReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&jr); err != nil || jr.Token == "" || jr.NodeID == "" || len(jr.NodeID) > 128 {
		writeErr(w, 400, "token and node_id required")
		return
	}
	sum := sha256.Sum256([]byte(jr.Token))
	c.mu.Lock()
	exp, ok := c.tokens[sum]
	if ok {
		delete(c.tokens, sum) // single use, consumed even if expired
	}
	c.mu.Unlock()
	if !ok || time.Now().After(exp) {
		c.h.Auth.fails.record(ip)
		c.h.Audit.Log(AuditEvent{Event: "join.fail", Remote: ip, Reason: "invalid or expired join token"})
		writeErr(w, 401, "invalid or expired join token")
		return
	}
	if jr.NodeID == c.h.NodeID {
		writeErr(w, 409, "node_id collides with the leader")
		return
	}
	secret := "rxn_" + randBase62(43)
	ssum := sha256.Sum256([]byte(secret))
	c.mu.Lock()
	c.members[jr.NodeID] = &Member{ID: jr.NodeID, Addr: jr.Addr, Role: "follower", LastSeen: time.Now().UTC(), SecretHash: hex.EncodeToString(ssum[:]), Joined: time.Now().UTC()}
	c.saveMembers()
	rid := c.meta.ReplID
	c.mu.Unlock()
	c.h.Audit.Log(AuditEvent{Event: "node.join", Remote: ip, Detail: "node " + jr.NodeID})
	writeJSON(w, 200, joinResp{NodeSecret: secret, LeaderID: c.h.NodeID, ReplID: rid})
}

// ---- promote / repoint ----

func (c *Cluster) handlePromote(w http.ResponseWriter, r *http.Request, p *Principal) {
	c.mu.Lock()
	if c.st.Role == "leader" {
		c.mu.Unlock()
		writeErr(w, 409, "already the leader")
		return
	}
	c.stopFollowerLocked()
	off := c.log.Last()
	c.meta.ParentID, c.meta.ParentOff = c.meta.ReplID, off
	c.meta.ReplID = randHex(20)
	c.st.Role, c.st.LeaderURL = "leader", ""
	_ = c.saveMeta()
	_ = c.saveState()
	c.members = map[string]*Member{}
	nr := c.meta.ReplID
	c.mu.Unlock()
	c.h.setFollowerMode(false) // re-arms engine expiry
	c.h.Tracker.ResetSliding(time.Now())
	c.h.Audit.Log(AuditEvent{Event: "node.promote", KeyID: p.Key.ID, Remote: remoteIP(r), Detail: fmt.Sprintf("offset %d", off)})
	writeJSON(w, 200, map[string]interface{}{"role": "leader", "replication_id": nr, "offset": off,
		"note": "acknowledged writes the old leader had not yet replicated are lost; repoint other nodes with `redx node repoint`"})
}

func (c *Cluster) stopFollowerLocked() {
	if c.loopStop != nil {
		close(c.loopStop)
		c.loopStop = nil
	}
}

// Repoint makes this node a follower of leaderURL (demoting it if it is a leader).
func (c *Cluster) Repoint(ctx context.Context, leaderURL, joinToken, pin string) error {
	leaderURL = strings.TrimRight(leaderURL, "/")
	if _, err := url.Parse(leaderURL); err != nil || !strings.HasPrefix(leaderURL, "http") {
		return errors.New("leader url must start with http:// or https://")
	}
	c.mu.Lock()
	c.stopFollowerLocked()
	adv := c.st.Advertise
	c.mu.Unlock()
	st := nodeState{Role: "follower", LeaderURL: leaderURL, Pin: pin, Advertise: adv, NodeSecret: ""}
	if joinToken != "" {
		cl := &http.Client{Timeout: 20 * time.Second}
		if pin != "" {
			cl.Transport = &http.Transport{TLSClientConfig: pinnedTLS(strings.TrimPrefix(pin, "sha256:"))}
		}
		jr, err := JoinLeader(ctx, cl, leaderURL, joinToken, c.h.NodeID, adv)
		if err != nil {
			return err
		}
		st.NodeSecret = jr.NodeSecret
	} else {
		c.mu.Lock()
		st.NodeSecret = c.st.NodeSecret // keep (e.g. static cluster_secret in use)
		c.mu.Unlock()
	}
	c.mu.Lock()
	wasLeader := c.st.Role == "leader"
	c.st = st
	_ = c.saveState()
	c.client = c.newClient()
	c.members = map[string]*Member{}
	if wasLeader {
		// Keep our replid so the new leader can compare; it will demand a snapshot
		// if we are ahead of its history.
		c.h.Logf("demoted to follower of %s", leaderURL)
	}
	c.mu.Unlock()
	c.h.setFollowerMode(true)
	c.startFollower()
	return nil
}

// JoinLeader performs the join handshake against a leader.
func JoinLeader(ctx context.Context, cl *http.Client, leaderURL, token, nodeID, addr string) (*joinResp, error) {
	b, _ := json.Marshal(joinReq{Token: token, NodeID: nodeID, Addr: addr})
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(leaderURL, "/")+"/v1/cluster/join", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("join refused (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var jr joinResp
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, err
	}
	return &jr, nil
}

// WriteJoinedState writes cluster.json for an offline `redx node join`.
func WriteJoinedState(dataDir, leaderURL, secret, pin, advertise string) error {
	dir := filepath.Join(dataDir, "cluster")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(nodeState{Role: "follower", LeaderURL: strings.TrimRight(leaderURL, "/"), NodeSecret: secret, Pin: pin, Advertise: advertise}, "", " ")
	return atomicWrite(filepath.Join(dir, "cluster.json"), b)
}

// ---- leader forwarding ----

func (c *Cluster) proxy() *httputil.ReverseProxy {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, _ := url.Parse(c.st.LeaderURL)
	p := httputil.NewSingleHostReverseProxy(u)
	p.Transport = c.client.Transport
	node := c.h.NodeID
	d := p.Director
	p.Director = func(r *http.Request) {
		d(r)
		r.Header.Set("X-Redx-Forwarded-By", node)
		r.Host = u.Host
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeErr(w, http.StatusBadGateway, "leader unreachable")
	}
	return p
}
