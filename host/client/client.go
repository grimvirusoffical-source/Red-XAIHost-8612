// Package client is a small HTTP client for redxd (used by the redx CLI).
package client

import (
	"bytes"
	"context"
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
	"net/url"
	"strings"
	"time"
)

// Client talks to one redxd.
type Client struct {
	Base string
	Key  string
	HTTP *http.Client
}

// New creates a client. pin (sha256 hex of the server certificate, optional) lets
// you trust a self-signed certificate safely; insecure disables verification.
func New(base, key, pin string, insecure bool) *Client {
	base = strings.TrimRight(base, "/")
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	tr := &http.Transport{TLSHandshakeTimeout: 10 * time.Second}
	switch {
	case pin != "":
		p := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(pin, "sha256:"), ":", ""))
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("no certificate")
				}
				s := sha256.Sum256(raw[0])
				if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(s[:])), []byte(p)) != 1 {
					return errors.New("server certificate does not match --pin")
				}
				return nil
			}}
	case insecure:
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{Base: base, Key: key, HTTP: &http.Client{Transport: tr, Timeout: 60 * time.Second}}
}

// Error is a non-2xx response.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s", e.Status, e.Msg) }

// Raw performs a request and returns the status, headers and body.
func (c *Client) Raw(ctx context.Context, method, path string, body []byte, hdr map[string]string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	return resp.StatusCode, resp.Header, b, err
}

// JSON performs a JSON request; out may be nil.
func (c *Client) JSON(ctx context.Context, method, path string, in, out interface{}) error {
	var body []byte
	hdr := map[string]string{}
	if in != nil {
		body, _ = json.Marshal(in)
		hdr["Content-Type"] = "application/json"
	}
	st, _, b, err := c.Raw(ctx, method, path, body, hdr)
	if err != nil {
		return err
	}
	if st < 200 || st > 299 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return &Error{st, e.Error}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// DataPath builds /v1/db/<db>/path/<path> (db "" = default db).
func DataPath(db, path string) string {
	esc := (&url.URL{Path: path}).EscapedPath()
	if db == "" {
		return "/v1/path/" + esc
	}
	return "/v1/db/" + db + "/path/" + esc
}

// Get fetches a value as Box text.
func (c *Client) Get(ctx context.Context, db, path string) (string, error) {
	st, _, b, err := c.Raw(ctx, "GET", DataPath(db, path), nil, map[string]string{"Accept": "text/x-redx-box"})
	if err != nil {
		return "", err
	}
	if st != 200 {
		return "", &Error{st, strings.TrimSpace(string(b))}
	}
	return string(b), nil
}

// Put stores Box text.
func (c *Client) Put(ctx context.Context, db, path, value string, ttl int, sliding bool) error {
	q := ""
	if ttl > 0 {
		q = fmt.Sprintf("?ttl=%d&sliding=%v", ttl, sliding)
	}
	st, _, b, err := c.Raw(ctx, "PUT", DataPath(db, path)+q, []byte(value), map[string]string{"Content-Type": "text/x-redx-box"})
	if err != nil {
		return err
	}
	if st != 200 {
		return &Error{st, strings.TrimSpace(string(b))}
	}
	return nil
}

// Delete removes a path.
func (c *Client) Delete(ctx context.Context, db, path string) error {
	return c.JSON(ctx, "DELETE", DataPath(db, path), nil, nil)
}

// Query runs a query.
func (c *Client) Query(ctx context.Context, db, q string) ([]string, error) {
	p := "/v1/query"
	if db != "" {
		p = "/v1/db/" + db + "/query"
	}
	var out struct {
		Results []string `json:"results"`
	}
	err := c.JSON(ctx, "POST", p, map[string]string{"q": q}, &out)
	return out.Results, err
}

// KeyViewAlias mirrors the key view returned by the API.
type KeyViewAlias struct {
	ID      string     `json:"id"`
	Scope   string     `json:"scope"`
	DB      string     `json:"db"`
	Prefix  string     `json:"prefix,omitempty"`
	Label   string     `json:"label,omitempty"`
	Roles   []string   `json:"roles,omitempty"`
	Created time.Time  `json:"created"`
	Expires *time.Time `json:"expires,omitempty"`
	Revoked *time.Time `json:"revoked,omitempty"`
	Status  string     `json:"status"`
}
