// Command sitebin is the Sitebin backend and all-in-one entrypoint.
//
//	sitebin run          all-in-one: backend + cleanup + supervised Caddy
//	sitebin server       backend only (compose / external Caddy)
//	sitebin caddyfile    print the generated Caddyfile and exit
//	sitebin cleanup      run one cleanup sweep and exit
//	sitebin healthcheck  probe the internal health endpoint (container HEALTHCHECK)
//	sitebin delete <id|domain>  operator takedown of a site
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ittrail/sitebin/internal/auth"
	"github.com/ittrail/sitebin/internal/caddygen"
	"github.com/ittrail/sitebin/internal/cleanup"
	"github.com/ittrail/sitebin/internal/config"
	"github.com/ittrail/sitebin/internal/httpapi"
	"github.com/ittrail/sitebin/internal/store"
	"github.com/ittrail/sitebin/internal/supervisor"
	"github.com/ittrail/sitebin/web"
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "run", "server":
		if err := serve(cmd == "run"); err != nil {
			slog.Error("fatal", "err", err)
			os.Exit(1)
		}
	case "caddyfile":
		cfg := mustConfig()
		fmt.Print(caddygen.Generate(cfg))
	case "cleanup":
		cfg := mustConfig()
		st := mustStore(cfg)
		n, err := cleanup.Sweep(st, time.Now())
		if err != nil {
			slog.Error("cleanup", "err", err)
			os.Exit(1)
		}
		fmt.Printf("removed %d expired site(s)\n", n)
	case "healthcheck":
		cfg := mustConfig()
		if err := healthcheck(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		fmt.Println("ok")
	case "delete":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: sitebin delete <view-id|edit-id|domain>")
			os.Exit(2)
		}
		if err := deleteSite(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "delete failed:", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println("sitebin", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(2)
	}
}

var version = "dev" // set via -ldflags at build time

func mustConfig() config.Config {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(1)
	}
	return cfg
}

func mustStore(cfg config.Config) *store.Store {
	st, err := store.New(cfg.DataDir, cfg.BaseDomain, cfg.MaxSiteBytes, cfg.MaxFiles)
	if err != nil {
		fmt.Fprintln(os.Stderr, "data dir error:", err)
		os.Exit(1)
	}
	return st
}

// serve runs the backend; withCaddy additionally generates the Caddyfile and
// supervises a Caddy child process (the all-in-one shape).
func serve(withCaddy bool) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg := mustConfig()
	st := mustStore(cfg)

	secret, err := auth.LoadOrCreateSecret(filepath.Join(cfg.DataDir, ".secret"))
	if err != nil {
		return err
	}
	api, err := httpapi.New(cfg, st, secret, web.Assets)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	public := &http.Server{Addr: cfg.PublicAddr, Handler: api.Public()}
	internal := &http.Server{Addr: cfg.InternalAddr, Handler: api.Internal()}
	errs := make(chan error, 4)
	go func() { errs <- fmt.Errorf("public listener: %w", public.ListenAndServe()) }()
	go func() { errs <- fmt.Errorf("internal listener: %w", internal.ListenAndServe()) }()
	go cleanup.Run(ctx, st, cfg.CleanupInterval)
	slog.Info("sitebin backend up",
		"base_domain", cfg.BaseDomain, "public", cfg.PublicAddr,
		"internal", cfg.InternalAddr, "data", cfg.DataDir, "version", version)

	var caddyDone <-chan error
	if withCaddy {
		caddyDir := filepath.Join(cfg.DataDir, "caddy")
		if err := os.MkdirAll(caddyDir, 0o755); err != nil {
			return err
		}
		caddyfile := filepath.Join(caddyDir, "Caddyfile")
		if err := os.WriteFile(caddyfile, []byte(caddygen.Generate(cfg)), 0o644); err != nil {
			return err
		}
		caddyDone, err = supervisor.StartCaddy(ctx, caddyfile)
		if err != nil {
			return fmt.Errorf("start caddy: %w", err)
		}
		slog.Info("caddy started", "caddyfile", caddyfile, "https", !cfg.HTTPOnly)
	}

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("shutting down (signal)")
	case err := <-errs:
		runErr = err
	case err := <-caddyOrNever(caddyDone):
		runErr = fmt.Errorf("caddy exited: %w", err)
	}

	stop() // also asks the caddy child to terminate via ctx
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	public.Shutdown(shutdownCtx)
	internal.Shutdown(shutdownCtx)
	if caddyDone != nil {
		select { // give caddy a moment to drain
		case <-caddyDone:
		case <-time.After(15 * time.Second):
		}
	}
	if runErr != nil && !errors.Is(runErr, http.ErrServerClosed) {
		return runErr
	}
	return nil
}

// caddyOrNever adapts a possibly-nil channel for select.
func caddyOrNever(c <-chan error) <-chan error {
	if c != nil {
		return c
	}
	return make(chan error) // never fires
}

func healthcheck(cfg config.Config) error {
	addr := cfg.InternalAddr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://" + addr + "/internal/health")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}

// deleteSite is the operator/abuse takedown: accepts a view id, edit id, or
// custom domain.
func deleteSite(key string) error {
	cfg := mustConfig()
	st := mustStore(cfg)
	site, err := st.ByViewID(key)
	if err != nil {
		site, err = st.ByEditID(key)
	}
	if err != nil {
		site, err = st.ByDomain(key)
	}
	if err != nil {
		return fmt.Errorf("no site found for %q", key)
	}
	if err := st.Delete(site); err != nil {
		return err
	}
	fmt.Println("deleted site", site.ViewID)
	return nil
}
