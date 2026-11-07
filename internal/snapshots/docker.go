package snapshots

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// DockerAPI is the little of the Docker Engine the instance restore needs.
type DockerAPI interface {
	// Stop stops a container (30 s grace); one already stopped is fine.
	Stop(ctx context.Context, name string) error
	// Start starts a container; one already running is fine.
	Start(ctx context.Context, name string) error
	// Health answers the container's health status ("healthy", "starting",
	// "unhealthy"), "running" for a running container without a health
	// check, else its state.
	Health(ctx context.Context, name string) (string, error)
	// RemoveManaged removes every container a Sitebin runtime made
	// (label io.sitebin.managed=true) and answers how many.
	RemoveManaged(ctx context.Context) (int, error)
}

// dockerClient speaks to the Docker Engine on its unix socket.
type dockerClient struct{ c *http.Client }

// NewDockerClient answers a DockerAPI on the engine's socket.
func NewDockerClient(sock string) DockerAPI {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	return &dockerClient{c: &http.Client{Transport: tr, Timeout: 2 * time.Minute}}
}

func (d *dockerClient) do(ctx context.Context, method, path string, ok ...int) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}
	res, err := d.c.Do(req)
	if err != nil {
		return nil, err
	}
	for _, c := range ok {
		if res.StatusCode == c {
			return res, nil
		}
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	res.Body.Close()
	return nil, fmt.Errorf("docker %s %s: %d %s", method, path, res.StatusCode, b)
}

func (d *dockerClient) Stop(ctx context.Context, name string) error {
	res, err := d.do(ctx, "POST", "/containers/"+url.PathEscape(name)+"/stop?t=30", 204, 304)
	if err == nil {
		res.Body.Close()
	}
	return err
}

func (d *dockerClient) Start(ctx context.Context, name string) error {
	res, err := d.do(ctx, "POST", "/containers/"+url.PathEscape(name)+"/start", 204, 304)
	if err == nil {
		res.Body.Close()
	}
	return err
}

func (d *dockerClient) Health(ctx context.Context, name string) (string, error) {
	res, err := d.do(ctx, "GET", "/containers/"+url.PathEscape(name)+"/json", 200)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var doc struct {
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return "", err
	}
	if doc.State.Health != nil {
		return doc.State.Health.Status, nil
	}
	if doc.State.Running {
		return "running", nil
	}
	return doc.State.Status, nil
}

func (d *dockerClient) RemoveManaged(ctx context.Context) (int, error) {
	filters := url.QueryEscape(`{"label":["io.sitebin.managed=true"]}`)
	res, err := d.do(ctx, "GET", "/containers/json?all=1&filters="+filters, 200)
	if err != nil {
		return 0, err
	}
	var list []struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(res.Body).Decode(&list)
	res.Body.Close()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range list {
		r, err := d.do(ctx, "DELETE", "/containers/"+url.PathEscape(c.ID)+"?force=1", 204, 404)
		if err != nil {
			return n, err
		}
		r.Body.Close()
		n++
	}
	return n, nil
}
