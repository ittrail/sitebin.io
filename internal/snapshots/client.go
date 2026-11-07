package snapshots

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// Client is the instance's side: it asks the agent over the socket the
// container mounts (SITEBIN_SNAPSHOTS_AGENT).
type Client struct {
	base string
	hc   *http.Client
}

// NewClient answers a client of the agent listening on the unix socket sock.
// Calls carry their own deadlines: a site's files can take minutes.
func NewClient(sock string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	return &Client{base: "http://agent", hc: &http.Client{Transport: tr}}
}

// NewClientURL answers a client of an agent at base (tests).
func NewClientURL(base string, hc *http.Client) *Client { return &Client{base: base, hc: hc} }

// AgentError is an answer of the agent other than success.
type AgentError struct {
	Code int
	Msg  string
}

func (e *AgentError) Error() string { return fmt.Sprintf("agent: %d %s", e.Code, e.Msg) }

// Is makes a 404 ErrNotFound.
func (e *AgentError) Is(target error) bool {
	return target == ErrNotFound && e.Code == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if res.StatusCode/100 == 2 {
		return res, nil
	}
	defer res.Body.Close()
	var e struct {
		Error string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&e)
	return nil, &AgentError{Code: res.StatusCode, Msg: e.Error}
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	res, err := c.do(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(v)
}

func sitePath(id, view string) string {
	return "/v1/snapshots/" + url.PathEscape(id) + "/sites/" + url.PathEscape(view)
}

// Versions answers the versions of site view, and how many snapshots are
// not indexed yet.
func (c *Client) Versions(ctx context.Context, view string) ([]Version, int, error) {
	var out struct {
		Versions []Version `json:"versions"`
		Pending  int       `json:"pending"`
	}
	err := c.getJSON(ctx, "/v1/sites/"+url.PathEscape(view)+"/versions", &out)
	return out.Versions, out.Pending, err
}

// Meta answers the site's meta.json in snapshot id.
func (c *Client) Meta(ctx context.Context, id, view string) ([]byte, error) {
	res, err := c.do(ctx, "GET", sitePath(id, view)+"/meta", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(io.LimitReader(res.Body, 4<<20))
}

// Files answers a tar of the site's files/ in snapshot id. A stream cut
// short is a tar error to the reader.
func (c *Client) Files(ctx context.Context, id, view string) (io.ReadCloser, error) {
	res, err := c.do(ctx, "GET", sitePath(id, view)+"/files", nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// BackupNow has the agent take a snapshot and answers it.
func (c *Client) BackupNow(ctx context.Context) (Snapshot, error) {
	res, err := c.do(ctx, "POST", "/v1/backup-now", map[string]any{})
	if err != nil {
		return Snapshot{}, err
	}
	defer res.Body.Close()
	var s Snapshot
	err = json.NewDecoder(res.Body).Decode(&s)
	return s, err
}

// Raw answers a GET of the agent's API as it came (status, snapshots), for
// the operator's view to pass on.
func (c *Client) Raw(ctx context.Context, path string) (json.RawMessage, error) {
	var m json.RawMessage
	err := c.getJSON(ctx, path, &m)
	return m, err
}

// RestoreInstance starts the instance restore to snapshot, or to the kept
// copy copyName.
func (c *Client) RestoreInstance(ctx context.Context, snapshot, copyName string) (RestoreState, error) {
	body := map[string]string{}
	if snapshot != "" {
		body["snapshot"] = snapshot
	} else {
		body["copy"] = copyName
	}
	res, err := c.do(ctx, "POST", "/v1/instance/restore", body)
	if err != nil {
		return RestoreState{}, err
	}
	defer res.Body.Close()
	var s RestoreState
	err = json.NewDecoder(res.Body).Decode(&s)
	return s, err
}

// DeleteCopy deletes a kept copy of the data.
func (c *Client) DeleteCopy(ctx context.Context, name string) error {
	res, err := c.do(ctx, "DELETE", "/v1/instance/copies/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}
