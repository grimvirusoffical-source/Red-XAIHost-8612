package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"redx/host"
	"redx/host/internal/stubstore"
)

type node struct {
	H   *host.Host
	Srv *httptest.Server
	URL string
	Dir string
	Key string // bootstrap admin key (leader only)

	mu      sync.Mutex
	setOpts []host.SetOpts
}

type recStore struct {
	host.Backend
	n *node
}

func (r *recStore) Set(p, v string, o host.SetOpts) error {
	r.n.mu.Lock()
	r.n.setOpts = append(r.n.setOpts, o)
	r.n.mu.Unlock()
	return r.Backend.Set(p, v, o)
}

func baseCfg(dir string) host.Config {
	c := host.DefaultConfig()
	c.DataDir = dir
	c.ArgonMemKiB, c.ArgonTime = 64, 1
	c.Heartbeat = 1
	c.LogLevel = "quiet"
	return c
}

// startNode starts a node on 127.0.0.1:<random>. mod may adjust the config using the node's URL.
func startNode(t *testing.T, dir string, mod func(c *host.Config, url string)) *node {
	t.Helper()
	n := &node{Dir: dir}
	n.Srv = httptest.NewUnstartedServer(nil)
	n.URL = "http://" + n.Srv.Listener.Addr().String()
	cfg := baseCfg(dir)
	cfg.Advertise = n.URL
	if mod != nil {
		mod(&cfg, n.URL)
	}
	h, err := host.New(cfg, func(d string, follower bool) (host.Backend, error) {
		b, err := stubstore.Open(d, follower)
		if err != nil {
			return nil, err
		}
		return &recStore{b, n}, nil
	}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	n.H = h
	n.Key = h.BootstrapKey
	n.Srv.Config.Handler = h.Handler()
	n.Srv.Start()
	t.Cleanup(n.Stop)
	return n
}

func (n *node) Stop() {
	if n.Srv != nil {
		n.Srv.Close()
		n.Srv = nil
	}
	if n.H != nil {
		n.H.Close()
		n.H = nil
	}
}

func (n *node) opts() []host.SetOpts {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]host.SetOpts(nil), n.setOpts...)
}

func do(t *testing.T, method, url, key, body string, hdr ...string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func jsonOf(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad json %q: %v", s, err)
	}
	return m
}

func mkKey(t *testing.T, n *node, req map[string]interface{}) (key, id string) {
	t.Helper()
	b, _ := json.Marshal(req)
	st, body, _ := do(t, "POST", n.URL+"/v1/keys", n.Key, string(b), "Content-Type", "application/json")
	if st != 201 {
		t.Fatalf("create key: %d %s", st, body)
	}
	m := jsonOf(t, body)
	return m["key"].(string), m["id"].(string)
}

func eventually(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", what)
}

var _ = bytes.NewReader
var _ = context.Background
