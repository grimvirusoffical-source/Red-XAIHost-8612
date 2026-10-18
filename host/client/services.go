package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// ---- RXQL ----

// SQLResult is the response of a SQL call.
type SQLResult struct {
	Kind     string              `json:"kind"`
	Columns  []string            `json:"columns"`
	Rows     [][]json.RawMessage `json:"rows"`
	RowsBox  [][]string          `json:"rows_box"`
	Count    int                 `json:"count"`
	Affected int                 `json:"affected"`
	Access   string              `json:"access"`
	Scanned  int                 `json:"scanned"`
	Paths    []string            `json:"paths"`
	Cascaded []string            `json:"cascaded"`
	Plan     []string            `json:"plan"`
}

// SQL runs one RXQL statement with ? parameters.
func (c *Client) SQL(ctx context.Context, db, q string, params []interface{}, explain bool) (*SQLResult, error) {
	var out SQLResult
	err := c.JSON(ctx, "POST", dbSeg(db)+"/sql", map[string]interface{}{"q": q, "params": params, "explain": explain}, &out)
	return &out, err
}

// ---- media ----

// MediaInfo is one media manifest.
type MediaInfo struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	MIME              string `json:"mime"`
	Size              int64  `json:"size"`
	SHA256            string `json:"sha256"`
	Width             int    `json:"width,omitempty"`
	Height            int    `json:"height,omitempty"`
	DurationMS        int64  `json:"duration_ms,omitempty"`
	DurationEstimated bool   `json:"duration_estimated,omitempty"`
}

func mediaURL(db, lib, verb, name string) string {
	p := dbSeg(db) + "/media/" + url.PathEscape(lib)
	if verb != "" {
		p += "/" + verb + "/" + url.PathEscape(name)
	}
	return p
}

// MediaPut streams r into the library; declared (optional) is the type the caller claims.
func (c *Client) MediaPut(ctx context.Context, db, lib, name string, r io.Reader, size int64, declared, sha256hex string) (*MediaInfo, error) {
	u := c.Base + mediaURL(db, lib, "file", name)
	if declared != "" {
		u += "?type=" + url.QueryEscape(declared)
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", u, r)
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
	var m MediaInfo
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return &m, jsonUnmarshal(b, &m)
}

// MediaGet opens a download; rng is an HTTP Range header value ("" = whole item). The caller closes the body.
func (c *Client) MediaGet(ctx context.Context, db, lib, name, rng string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+mediaURL(db, lib, "file", name), nil)
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

// MediaList lists a library.
func (c *Client) MediaList(ctx context.Context, db, lib string) ([]MediaInfo, error) {
	var out struct {
		Items []MediaInfo `json:"items"`
	}
	err := c.JSON(ctx, "GET", mediaURL(db, lib, "", ""), nil, &out)
	return out.Items, err
}

// MediaStat returns one manifest.
func (c *Client) MediaStat(ctx context.Context, db, lib, name string) (*MediaInfo, error) {
	var m MediaInfo
	err := c.JSON(ctx, "GET", mediaURL(db, lib, "stat", name), nil, &m)
	return &m, err
}

// MediaRm deletes an item.
func (c *Client) MediaRm(ctx context.Context, db, lib, name string) error {
	return c.JSON(ctx, "DELETE", mediaURL(db, lib, "file", name), nil, nil)
}

// MediaLibs lists the libraries of a database.
func (c *Client) MediaLibs(ctx context.Context, db string) ([]string, error) {
	var out struct {
		Libraries []string `json:"libraries"`
	}
	err := c.JSON(ctx, "GET", dbSeg(db)+"/media", nil, &out)
	return out.Libraries, err
}

// ---- code and sec ----

// CodeList returns the scripts.
func (c *Client) CodeList(ctx context.Context, db string, out interface{}) error {
	return c.JSON(ctx, "GET", dbSeg(db)+"/code", nil, out)
}

// CodeDefine stores a .red-xai-code file ("" source is not allowed: use CodeRemove).
func (c *Client) CodeDefine(ctx context.Context, db, file, src string) error {
	st, _, b, err := c.Raw(ctx, "PUT", dbSeg(db)+"/code/file/"+url.PathEscape(file), []byte(src), map[string]string{"Content-Type": "text/plain"})
	if err != nil {
		return err
	}
	if st != 200 {
		return &Error{st, errMsg(b)}
	}
	return nil
}

// CodeRemove deletes a code file.
func (c *Client) CodeRemove(ctx context.Context, db, file string) error {
	return c.JSON(ctx, "DELETE", dbSeg(db)+"/code/file/"+url.PathEscape(file), nil, nil)
}

// CodeRun runs a script.
func (c *Client) CodeRun(ctx context.Context, db, script string, args []interface{}, out interface{}) error {
	return c.JSON(ctx, "POST", dbSeg(db)+"/code/run/"+url.PathEscape(script), map[string]interface{}{"args": args}, out)
}

// SecGet returns the policy summary (source included for admin keys).
func (c *Client) SecGet(ctx context.Context, db string, out interface{}) error {
	return c.JSON(ctx, "GET", dbSeg(db)+"/sec", nil, out)
}

// SecPut replaces the API-managed policy file.
func (c *Client) SecPut(ctx context.Context, db, src string) error {
	st, _, b, err := c.Raw(ctx, "PUT", dbSeg(db)+"/sec", []byte(src), map[string]string{"Content-Type": "text/plain"})
	if err != nil {
		return err
	}
	if st != 200 {
		return &Error{st, errMsg(b)}
	}
	return nil
}

// SecDelete removes the API-managed policy file.
func (c *Client) SecDelete(ctx context.Context, db string) error {
	return c.JSON(ctx, "DELETE", dbSeg(db)+"/sec", nil, nil)
}
