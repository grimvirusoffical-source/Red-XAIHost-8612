package host

import (
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize: 4096, WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		if o == "" {
			return true // non-browser client
		}
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	},
}

// subscribeHandler streams events for a path (prefix) over a WebSocket.
// Auth: Authorization: Bearer header, or ?access_token= for browsers (URLs may
// be logged by proxies: prefer the header where possible).
func (h *Host) subscribeHandler(w http.ResponseWriter, r *http.Request, db string) {
	path := r.URL.Query().Get("path")
	pr, ok := h.authzScope(w, r, ScopeRead, db)
	if !ok {
		return
	}
	if err := ValidSubscribePath(path); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !pr.Key.Allows(ScopeRead, db, path) {
		h.Audit.Log(AuditEvent{Event: "auth.denied", KeyID: pr.Key.ID, DB: db, Remote: remoteIP(r), Method: r.Method, Route: routeOf(r), Reason: "scope/db/prefix"})
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	b, err := h.Reg.Get(db)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ch, cancel := b.Subscribe(path)
	defer cancel()
	ex, _ := b.(Extras)
	var sp SecPrincipal
	filter := ex != nil && ex.SecActive()
	if filter {
		sp = secPrincipal(&pr.Key)
	}

	done := make(chan struct{})
	go func() { // reader: detect close, handle pongs
		defer close(done)
		conn.SetReadLimit(1 << 10)
		_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(70 * time.Second)) })
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if filter { // the database policy: drop what the key may not read, redact denied fields
				v, err := ex.SecRead(sp, ev.Path, ev.Value)
				if err != nil {
					continue
				}
				ev.Value = v
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(ev); err != nil {
				return
			}
		case <-tick.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// pulseSubscribe streams fired rule/timer events (and optionally raw changes) of one
// database over a WebSocket. Events about records the key may not read are dropped.
// Query: topic=<pattern> ("" or "*" = all, "prefix*", or exact), changes=1 adds change.* events.
func (h *Host) pulseSubscribe(w http.ResponseWriter, r *http.Request, db string) {
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
	q := r.URL.Query()
	ch, cancel, err := ex.PulseSubscribe(q.Get("topic"), q.Get("changes") == "1", allowFor(&pr.Key))
	if err != nil {
		h.extrasMapErr(w, err)
		return
	}
	defer cancel()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.SetReadLimit(1 << 10)
		_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(70 * time.Second)) })
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(ev); err != nil {
				return
			}
		case <-tick.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
