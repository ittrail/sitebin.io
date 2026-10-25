//go:build ee

package containers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContainerUpdateAndInfo(t *testing.T) {
	var method, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.45/info":
			io.WriteString(w, `{"NCPU":6,"MemTotal":16762245120,"Name":"host"}`)
		case "/v1.45/containers/gone/update":
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"No such container: gone"}`)
		default:
			method, path = r.Method, r.URL.Path
			body = map[string]any{}
			json.NewDecoder(r.Body).Decode(&body)
			io.WriteString(w, `{"Warnings":[]}`)
		}
	}))
	defer srv.Close()
	c, err := NewClient("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := c.ContainerUpdate(ctx, "sb-x-app", Resources{Memory: 1 << 30, MemorySwap: 1 << 30, NanoCpus: 1e9, PidsLimit: 512}); err != nil {
		t.Fatal(err)
	}
	if method != "POST" || path != "/v1.45/containers/sb-x-app/update" {
		t.Errorf("request = %s %s", method, path)
	}
	want := map[string]any{"Memory": float64(1 << 30), "MemorySwap": float64(1 << 30), "NanoCpus": float64(1e9), "PidsLimit": float64(512)}
	if len(body) != len(want) {
		t.Errorf("body = %v", body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, body[k], v)
		}
	}
	if err := c.ContainerUpdate(ctx, "gone", Resources{Memory: 1}); !errors.Is(err, errNotFound) {
		t.Errorf("a missing container: %v", err)
	}

	info, err := c.Info(ctx)
	if err != nil || info.NCPU != 6 || info.MemTotal != 16762245120 {
		t.Errorf("info = %+v, %v", info, err)
	}
}

func TestContainerStats(t *testing.T) {
	var path, query string
	var connect map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/connect"):
			connect = map[string]any{}
			json.NewDecoder(r.Body).Decode(&connect)
		default:
			path, query = r.URL.Path, r.URL.RawQuery
			io.WriteString(w, `{"read":"2026-10-25T09:00:10.5Z","networks":{"eth0":{"rx_bytes":100,"tx_bytes":200},"egress0":{"rx_bytes":3000,"tx_bytes":4000}},
				"memory_stats":{"usage":130000000,"limit":2147483648,"stats":{"inactive_file":1000000}},
				"cpu_stats":{"cpu_usage":{"total_usage":68395000}},"pids_stats":{"current":12,"limit":512}}`)
		}
	}))
	defer srv.Close()
	c, _ := NewClient("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	st, err := c.ContainerStats(context.Background(), "sb-x-app")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1.45/containers/sb-x-app/stats" || query != "one-shot=true&stream=false" {
		t.Errorf("request = %s ? %s", path, query)
	}
	if st.Memory.Usage != 130000000 || st.Memory.Stats["inactive_file"] != 1000000 || st.CPU.Usage.Total != 68395000 ||
		st.Pids.Current != 12 || st.Networks["egress0"].Rx != 3000 || st.Networks["eth0"].Tx != 200 || st.Read.Second() != 10 {
		t.Errorf("stats = %+v", st)
	}

	if err := c.NetworkConnect(context.Background(), "sb-x-egress", "cid", "egress0"); err != nil {
		t.Fatal(err)
	}
	ec, _ := connect["EndpointConfig"].(map[string]any)
	opts, _ := ec["DriverOpts"].(map[string]any)
	if connect["Container"] != "cid" || opts["com.docker.network.endpoint.ifname"] != "egress0" {
		t.Errorf("connect body = %v", connect)
	}
	c.NetworkConnect(context.Background(), "sb-x", "self", "")
	if ec, _ := connect["EndpointConfig"].(map[string]any); ec["DriverOpts"] != nil {
		t.Errorf("an unnamed connect sent driver options: %v", connect)
	}
}
