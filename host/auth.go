package host

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Principal is an authenticated caller.
type Principal struct {
	Key KeyRecord
}

// Authenticator authenticates HTTP requests against the Keystore.
type Authenticator struct {
	ks       *Keystore
	audit    *Auditor
	keyLim   *Limiter     // per-key request limiter
	fails    *failTracker // per-remote-IP failed-auth limiter
	defRPS   float64
	defBurst int
	auditOK  bool
}

// NewAuthenticator wires the pieces together.
func NewAuthenticator(ks *Keystore, au *Auditor, defRPS float64, defBurst int, auditOK bool) *Authenticator {
	return &Authenticator{ks: ks, audit: au, keyLim: NewLimiter(nil), fails: newFailTracker(20, time.Minute), defRPS: defRPS, defBurst: defBurst, auditOK: auditOK}
}

func remoteIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// bearer extracts the presented key. The access_token query parameter is
// accepted only for WebSocket upgrades (browsers cannot set headers).
func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:])
		}
		return ""
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return r.URL.Query().Get("access_token")
	}
	return ""
}

// Errors returned by Authenticate (map to HTTP status in the server).
var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrThrottled    = errors.New("too many requests")
)

// Authenticate verifies the request's key. On success it applies the per-key
// rate limit (returns ErrThrottled + retry hint). Failures are audited
// (without the presented key) and rate-limited per source IP.
func (a *Authenticator) Authenticate(r *http.Request) (*Principal, time.Duration, error) {
	ip := remoteIP(r)
	// Guard against brute force / argon2 CPU exhaustion: 20 failures/min/IP.
	if wait := a.fails.blocked(ip); wait > 0 {
		return nil, wait, ErrThrottled
	}
	key := bearer(r)
	if key == "" {
		a.fail(r, ip, "", "missing bearer token")
		return nil, 0, ErrUnauthorized
	}
	rec, err := a.ks.Verify(key)
	if err != nil {
		id := ""
		if rec != nil {
			id = rec.ID
		}
		a.fail(r, ip, id, err.Error())
		return nil, 0, ErrUnauthorized
	}
	rps, burst := rec.RateRPS, rec.RateBurst
	if rps == 0 {
		rps, burst = a.defRPS, a.defBurst
	}
	if ok, wait := a.keyLim.Allow(rec.ID, rps, burst); !ok {
		a.audit.Log(AuditEvent{Event: "ratelimit", KeyID: rec.ID, Remote: ip, Method: r.Method})
		return &Principal{Key: *rec}, wait, ErrThrottled
	}
	if a.auditOK {
		a.audit.Log(AuditEvent{Event: "auth.ok", KeyID: rec.ID, Remote: ip, Method: r.Method, Route: routeOf(r)})
	}
	return &Principal{Key: *rec}, 0, nil
}

func (a *Authenticator) fail(r *http.Request, ip, id, reason string) {
	a.fails.record(ip)
	a.audit.Log(AuditEvent{Event: "auth.fail", KeyID: id, Remote: ip, Method: r.Method, Route: routeOf(r), Reason: reason})
}

func routeOf(r *http.Request) string {
	p := r.URL.Path
	for _, pre := range []string{"/v1/path/", "/v1/db/"} {
		if strings.HasPrefix(p, pre) {
			return pre + "…" // do not log data paths
		}
	}
	return p
}

type failTracker struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string]*failEnt
}

type failEnt struct {
	n     int
	start time.Time
}

func newFailTracker(max int, w time.Duration) *failTracker {
	return &failTracker{max: max, window: w, m: map[string]*failEnt{}}
}

func (f *failTracker) blocked(ip string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.m[ip]
	if e == nil {
		return 0
	}
	if rem := f.window - time.Since(e.start); rem > 0 {
		if e.n >= f.max {
			return rem
		}
		return 0
	}
	delete(f.m, ip)
	return 0
}

func (f *failTracker) record(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.m) > 20000 {
		f.m = map[string]*failEnt{}
	}
	e := f.m[ip]
	if e == nil || time.Since(e.start) >= f.window {
		e = &failEnt{start: time.Now()}
		f.m[ip] = e
	}
	e.n++
}
