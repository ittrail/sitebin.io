//go:build ee

package containers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// apiVersion is the Engine API this client speaks. 1.45 (Docker 26) is the
// first with VolumeOptions.Subpath, which mounting one folder of a named data
// volume needs.
const apiVersion = "v1.45"

// Client is a small Docker Engine API client: the dozen calls the runtime
// makes, over the unix socket or TCP (a socket proxy). The official SDK would
// add a very large dependency tree to a binary whose selling point is having
// almost none.
type Client struct {
	hc   *http.Client
	base string
}

// errNotFound is the Engine's 404.
var errNotFound = errors.New("not found")

// NewClient connects to host: unix:///var/run/docker.sock or tcp://h:port.
func NewClient(host string) (*Client, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("docker host %q: %w", host, err)
	}
	tr := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	base := ""
	switch u.Scheme {
	case "unix":
		sock := u.Path
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}
		base = "http://docker"
	case "tcp", "http":
		base = "http://" + u.Host
	default:
		return nil, fmt.Errorf("docker host %q: use unix:// or tcp://", host)
	}
	return &Client{hc: &http.Client{Transport: tr}, base: base + "/" + apiVersion}, nil
}

// engineError is a non-2xx answer with the Engine's own message.
type engineError struct {
	Status int
	Msg    string
}

func (e *engineError) Error() string { return fmt.Sprintf("docker: %d %s", e.Status, e.Msg) }

func (e *engineError) Is(target error) bool { return target == errNotFound && e.Status == 404 }

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	resp, err := c.raw(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// raw sends a request and returns the response if it is a 2xx (or a 304,
// which the Engine uses for "already started / already stopped").
func (c *Client) raw(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	if resp.StatusCode/100 == 2 || resp.StatusCode == 304 {
		return resp, nil
	}
	defer resp.Body.Close()
	var msg struct {
		Message string `json:"message"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(b, &msg) != nil || msg.Message == "" {
		msg.Message = strings.TrimSpace(string(b))
	}
	return nil, &engineError{Status: resp.StatusCode, Msg: msg.Message}
}

// Ping reports whether the Engine answers.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, "GET", "/_ping", nil, nil, nil)
}

// ImageExists reports whether ref is present locally.
func (c *Client) ImageExists(ctx context.Context, ref string) (bool, error) {
	err := c.do(ctx, "GET", "/images/"+ref+"/json", nil, nil, nil)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Pull fetches repo@digest. The Engine streams progress and reports a
// failure INSIDE the stream with a 200, so the stream is read to the end.
func (c *Client) Pull(ctx context.Context, repo, digest string) error {
	resp, err := c.raw(ctx, "POST", "/images/create", url.Values{"fromImage": {repo}, "tag": {digest}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("docker: pull %s: %w", repo, err)
		}
		if m.Error != "" {
			return fmt.Errorf("docker: pull %s: %s", repo, m.Error)
		}
	}
}

// ContainerSummary is one entry of a container listing.
type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

func labelFilter(labels ...string) url.Values {
	f, _ := json.Marshal(map[string][]string{"label": labels})
	return url.Values{"filters": {string(f)}}
}

// ContainerList lists containers (running or not) carrying every label.
func (c *Client) ContainerList(ctx context.Context, labels ...string) ([]ContainerSummary, error) {
	q := labelFilter(labels...)
	q.Set("all", "1")
	var out []ContainerSummary
	return out, c.do(ctx, "GET", "/containers/json", q, nil, &out)
}

// ContainerCreate creates a container and returns its id.
func (c *Client) ContainerCreate(ctx context.Context, name string, body CreateBody) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, "POST", "/containers/create", url.Values{"name": {name}}, body, &out)
	return out.ID, err
}

// ContainerStart starts a container.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/containers/"+id+"/start", nil, nil, nil)
}

// ContainerRemove stops (with a grace period) and removes a container, and
// any anonymous volume it had.
func (c *Client) ContainerRemove(ctx context.Context, id string) error {
	// A container that will not stop is removed by force below, so the
	// stop's own error changes nothing.
	_ = c.do(ctx, "POST", "/containers/"+id+"/stop", url.Values{"t": {"10"}}, nil, nil)
	err := c.do(ctx, "DELETE", "/containers/"+id, url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

// Inspect is the part of a container inspection the runtime reads.
type Inspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

// ContainerInspect inspects a container by id or name.
func (c *Client) ContainerInspect(ctx context.Context, id string) (Inspect, error) {
	var out Inspect
	return out, c.do(ctx, "GET", "/containers/"+id+"/json", nil, nil, &out)
}

// ContainerLogs returns the last tail lines of stdout and stderr,
// demultiplexed. Containers run without a TTY, so the Engine frames each
// chunk with an 8-byte header.
func (c *Client) ContainerLogs(ctx context.Context, id string, tail int) (string, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {strconv.Itoa(tail)}}
	resp, err := c.raw(ctx, "GET", "/containers/"+id+"/logs", q, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return demux(io.LimitReader(resp.Body, 1<<20))
}

func demux(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	var out strings.Builder
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out.String(), nil
			}
			return out.String(), err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if _, err := io.CopyN(&out, br, int64(n)); err != nil {
			return out.String(), nil
		}
	}
}

// NetworkSummary is one entry of a network listing.
type NetworkSummary struct {
	ID     string            `json:"Id"`
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// NetworkList lists networks carrying every label.
func (c *Client) NetworkList(ctx context.Context, labels ...string) ([]NetworkSummary, error) {
	var out []NetworkSummary
	return out, c.do(ctx, "GET", "/networks", labelFilter(labels...), nil, &out)
}

// NetworkExists reports whether a network of that name exists.
func (c *Client) NetworkExists(ctx context.Context, name string) (bool, error) {
	err := c.do(ctx, "GET", "/networks/"+name, nil, nil, nil)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	return err == nil, err
}

// NetworkCreate creates a bridge network.
func (c *Client) NetworkCreate(ctx context.Context, name string, internal bool, labels, options map[string]string) error {
	body := map[string]any{"Name": name, "Driver": "bridge", "Internal": internal, "Labels": labels, "Options": options}
	return c.do(ctx, "POST", "/networks/create", nil, body, nil)
}

// NetworkRemove removes a network; one already gone is not an error.
func (c *Client) NetworkRemove(ctx context.Context, name string) error {
	err := c.do(ctx, "DELETE", "/networks/"+name, nil, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

// NetworkConnect attaches a container, with aliases. One already attached is
// not an error: this runs on every scan to re-attach Sitebin after a
// redeploy.
func (c *Client) NetworkConnect(ctx context.Context, network, container string, aliases []string) error {
	body := map[string]any{"Container": container, "EndpointConfig": map[string]any{"Aliases": aliases}}
	err := c.do(ctx, "POST", "/networks/"+network+"/connect", nil, body, nil)
	var ee *engineError
	if errors.As(err, &ee) && (ee.Status == 403 || ee.Status == 409) && strings.Contains(ee.Msg, "already") {
		return nil
	}
	return err
}

// NetworkDisconnect detaches a container; one not attached is not an error.
func (c *Client) NetworkDisconnect(ctx context.Context, network, container string) error {
	body := map[string]any{"Container": container, "Force": true}
	err := c.do(ctx, "POST", "/networks/"+network+"/disconnect", nil, body, nil)
	var ee *engineError
	if errors.Is(err, errNotFound) || (errors.As(err, &ee) && strings.Contains(ee.Msg, "not connected")) {
		return nil
	}
	return err
}

// ---- container create body: only the fields the runtime sets ----

// CreateBody is the Engine's container config.
type CreateBody struct {
	Image            string            `json:"Image"`
	Hostname         string            `json:"Hostname,omitempty"`
	User             string            `json:"User,omitempty"`
	Env              []string          `json:"Env,omitempty"`
	Cmd              []string          `json:"Cmd,omitempty"`
	WorkingDir       string            `json:"WorkingDir,omitempty"`
	Labels           map[string]string `json:"Labels,omitempty"`
	HostConfig       HostConfig        `json:"HostConfig"`
	NetworkingConfig *NetworkingConfig `json:"NetworkingConfig,omitempty"`
}

// HostConfig is the Engine's host config.
type HostConfig struct {
	Memory         int64             `json:"Memory,omitempty"`
	MemorySwap     int64             `json:"MemorySwap,omitempty"`
	NanoCpus       int64             `json:"NanoCpus,omitempty"`
	PidsLimit      int64             `json:"PidsLimit,omitempty"`
	CapDrop        []string          `json:"CapDrop,omitempty"`
	SecurityOpt    []string          `json:"SecurityOpt,omitempty"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs,omitempty"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	Mounts         []Mount           `json:"Mounts,omitempty"`
	RestartPolicy  RestartPolicy     `json:"RestartPolicy"`
	LogConfig      LogConfig         `json:"LogConfig"`
	NetworkMode    string            `json:"NetworkMode,omitempty"`
	Runtime        string            `json:"Runtime,omitempty"`
	Init           bool              `json:"Init,omitempty"`
}

// Mount is one entry of HostConfig.Mounts.
type Mount struct {
	Type          string         `json:"Type"`
	Source        string         `json:"Source"`
	Target        string         `json:"Target"`
	ReadOnly      bool           `json:"ReadOnly,omitempty"`
	VolumeOptions *VolumeOptions `json:"VolumeOptions,omitempty"`
}

// VolumeOptions selects one directory of a named volume.
type VolumeOptions struct {
	NoCopy  bool   `json:"NoCopy,omitempty"`
	Subpath string `json:"Subpath,omitempty"`
}

// RestartPolicy is the Engine's restart policy.
type RestartPolicy struct {
	Name string `json:"Name"`
}

// LogConfig is the Engine's log driver config.
type LogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config,omitempty"`
}

// NetworkingConfig attaches the container at creation.
type NetworkingConfig struct {
	EndpointsConfig map[string]EndpointConfig `json:"EndpointsConfig"`
}

// EndpointConfig is one network attachment.
type EndpointConfig struct {
	Aliases []string `json:"Aliases,omitempty"`
}
