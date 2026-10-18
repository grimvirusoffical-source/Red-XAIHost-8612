package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// stream returns a copy of the client without the overall request timeout, for large
// uploads and downloads (cancel through the context instead).
func (c *Client) stream() *http.Client {
	hc := *c.HTTP
	hc.Timeout = 0
	return &hc
}

func dbSeg(db string) string {
	if db == "" {
		db = "default"
	}
	return "/v1/db/" + db
}

func escPath(p string) string { return (&url.URL{Path: p}).EscapedPath() }

func apiErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	msg := strings.TrimSpace(string(b))
	if i := strings.Index(msg, `"error":"`); i >= 0 {
		msg = strings.TrimSuffix(msg[i+9:], `"}`)
	}
	return &Error{resp.StatusCode, msg}
}

// ---- indexes ----

// IndexHit is one query result.
type IndexHit struct {
	Path   string `json:"path"`
	Field  string `json:"field"`
	Value  string `json:"value"`
	Record string `json:"record,omitempty"`
}

// IndexResult is the response to an index query.
type IndexResult struct {
	Hits  []IndexHit `json:"hits"`
	Count int        `json:"count"`
	Mode  string     `json:"mode"`
	Note  string     `json:"note,omitempty"`
}

// IdxQuery runs a query; params are the REST query parameters (mode, field, value, lo, hi, q, op, limit, record ...).
func (c *Client) IdxQuery(ctx context.Context, db, set, index string, params url.Values) (*IndexResult, error) {
	var out IndexResult
	err := c.JSON(ctx, "GET", dbSeg(db)+"/idx/"+url.PathEscape(set)+"/"+url.PathEscape(index)+"/query?"+params.Encode(), nil, &out)
	return &out, err
}

// IdxList returns the raw JSON of the index sets.
func (c *Client) IdxList(ctx context.Context, db string, out interface{}) error {
	return c.JSON(ctx, "GET", dbSeg(db)+"/idx", nil, out)
}

// IdxDefine stores a declaration (Box text) as an index set.
func (c *Client) IdxDefine(ctx context.Context, db, set, decl string) error {
	st, _, b, err := c.Raw(ctx, "PUT", dbSeg(db)+"/idx/"+url.PathEscape(set), []byte(decl), map[string]string{"Content-Type": "text/x-redx-box"})
	if err != nil {
		return err
	}
	if st != 200 {
		return &Error{st, errMsg(b)}
	}
	return nil
}

func errMsg(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.Index(s, `"error":"`); i >= 0 {
		s = strings.TrimSuffix(s[i+9:], `"}`)
	}
	return s
}

// IdxDrop removes an index set.
func (c *Client) IdxDrop(ctx context.Context, db, set string) error {
	return c.JSON(ctx, "DELETE", dbSeg(db)+"/idx/"+url.PathEscape(set), nil, nil)
}

// IdxRebuild rescans the data and rebuilds the set.
func (c *Client) IdxRebuild(ctx context.Context, db, set string) error {
	return c.JSON(ctx, "POST", dbSeg(db)+"/idx/"+url.PathEscape(set)+"/rebuild", nil, nil)
}

// IdxCheck returns the drift report.
func (c *Client) IdxCheck(ctx context.Context, db, set string, out interface{}) error {
	return c.JSON(ctx, "GET", dbSeg(db)+"/idx/"+url.PathEscape(set)+"/check", nil, out)
}

// ---- packs ----

// PackFile describes a pack entry.
type PackFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
	ChunkSize int    `json:"chunk_size,omitempty"`
}

func packURL(db, pack, verb, path string) string {
	p := dbSeg(db) + "/pack/" + url.PathEscape(pack) + "/" + verb
	if path != "" {
		p += "/" + escPath(path)
	}
	return p
}

// PackPut streams r to the pack. A non-empty sha256 makes the server verify the upload (nothing is stored on mismatch).
func (c *Client) PackPut(ctx context.Context, db, pack, path string, r io.Reader, size int64, sha256hex string) (*PackFile, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", c.Base+packURL(db, pack, "file", path), r)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/octet-stream")
	if sha256hex != "" {
		req.Header.Set("X-Redx-SHA256", sha256hex)
	}
	resp, err := c.stream().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return nil, apiErr(resp)
	}
	var f PackFile
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return &f, jsonUnmarshal(b, &f)
}

// PackGet opens a download. rng is an HTTP Range header value ("" = whole file). The caller closes the body.
func (c *Client) PackGet(ctx context.Context, db, pack, path, rng string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+packURL(db, pack, "file", path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := c.stream().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		defer resp.Body.Close()
		return nil, apiErr(resp)
	}
	return resp, nil
}

// PackLs lists a folder.
func (c *Client) PackLs(ctx context.Context, db, pack, folder string) ([]PackFile, error) {
	var out struct {
		Entries []PackFile `json:"entries"`
	}
	err := c.JSON(ctx, "GET", packURL(db, pack, "ls", folder), nil, &out)
	return out.Entries, err
}

// PackStat describes one entry.
func (c *Client) PackStat(ctx context.Context, db, pack, path string) (*PackFile, error) {
	var f PackFile
	err := c.JSON(ctx, "GET", packURL(db, pack, "stat", path), nil, &f)
	return &f, err
}

// PackRm deletes a file.
func (c *Client) PackRm(ctx context.Context, db, pack, path string) error {
	return c.JSON(ctx, "DELETE", packURL(db, pack, "file", path), nil, nil)
}

// PackList lists the packs of a database.
func (c *Client) PackList(ctx context.Context, db string) ([]string, error) {
	var out struct {
		Packs []string `json:"packs"`
	}
	err := c.JSON(ctx, "GET", dbSeg(db)+"/pack", nil, &out)
	return out.Packs, err
}

// SHA256Hex hashes r.
func SHA256Hex(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// ---- links, pulse, websockets ----

// GetResolved reads path with embedded links followed (Box text).
func (c *Client) GetResolved(ctx context.Context, db, path string, depth int) (string, error) {
	q := "?resolve=1"
	if depth > 0 {
		q += fmt.Sprintf("&depth=%d", depth)
	}
	st, _, b, err := c.Raw(ctx, "GET", DataPath(db, path)+q, nil, map[string]string{"Accept": "text/x-redx-box"})
	if err != nil {
		return "", err
	}
	if st != 200 {
		return "", &Error{st, errMsg(b)}
	}
	return string(b), nil
}

// PulseGet returns the rule set state as raw JSON-decoded data.
func (c *Client) PulseGet(ctx context.Context, db string, out interface{}) error {
	return c.JSON(ctx, "GET", dbSeg(db)+"/pulse", nil, out)
}

// PulsePut replaces the rule set with Box text.
func (c *Client) PulsePut(ctx context.Context, db, src string) error {
	st, _, b, err := c.Raw(ctx, "PUT", dbSeg(db)+"/pulse", []byte(src), map[string]string{"Content-Type": "text/x-redx-box"})
	if err != nil {
		return err
	}
	if st != 200 {
		return &Error{st, errMsg(b)}
	}
	return nil
}

// PulseDelete removes every rule.
func (c *Client) PulseDelete(ctx context.Context, db string) error {
	return c.JSON(ctx, "DELETE", dbSeg(db)+"/pulse", nil, nil)
}

// Dial opens a WebSocket to path (e.g. "/v1/db/x/pulse/subscribe"), honouring --pin / --insecure.
func (c *Client) Dial(ctx context.Context, path string, q url.Values) (*websocket.Conn, *http.Response, error) {
	u, err := url.Parse(c.Base)
	if err != nil {
		return nil, nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path
	u.RawQuery = q.Encode()
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	if tr, ok := c.HTTP.Transport.(*http.Transport); ok {
		d.TLSClientConfig = tr.TLSClientConfig
	}
	return d.DialContext(ctx, u.String(), http.Header{"Authorization": {"Bearer " + c.Key}})
}

func jsonUnmarshal(b []byte, v interface{}) error { return json.Unmarshal(b, v) }
