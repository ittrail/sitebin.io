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
