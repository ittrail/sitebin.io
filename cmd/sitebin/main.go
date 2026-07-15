// Command sitebin is the Sitebin backend and all-in-one entrypoint.
//
//	sitebin run          all-in-one: backend + cleanup + supervised Caddy
//	sitebin server       backend only (compose / external Caddy)
//	sitebin caddyfile    print the generated Caddyfile and exit
//	sitebin cleanup      run one cleanup sweep and exit
//	sitebin healthcheck  probe the internal health endpoint (container HEALTHCHECK)
//	sitebin list         list all sites (operator)
//	sitebin reports      list filed abuse reports (operator)
//	sitebin delete <id|domain>  operator takedown of a site
//	sitebin backup [file]       write a tar.gz of the data dir
//	sitebin restore <file>      restore the data dir from a backup
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

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/caddygen"
	"github.com/ittrail/sitebin.io/internal/cleanup"
	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/ftp"
	"github.com/ittrail/sitebin.io/internal/httpapi"
	"github.com/ittrail/sitebin.io/internal/store"
	"github.com/ittrail/sitebin.io/internal/supervisor"
	"github.com/ittrail/sitebin.io/web"
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
	case "list":
		if err := listSites(); err != nil {
			fmt.Fprintln(os.Stderr, "list failed:", err)
			os.Exit(1)
		}
	case "reports":
		if err := listReports(); err != nil {
			fmt.Fprintln(os.Stderr, "reports failed:", err)
			os.Exit(1)
		}
	case "backup":
		out := ""
		if len(os.Args) > 2 {
			out = os.Args[2]
		}
		if err := backup(out); err != nil {
			fmt.Fprintln(os.Stderr, "backup failed:", err)
			os.Exit(1)
		}
	case "restore":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: sitebin restore <backup.tar.gz>")
			os.Exit(2)
		}
		if err := restore(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "restore failed:", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Printf("sitebin %s (%s edition)\n", version, edition)
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

	// In the all-in-one image Caddy is a child on the same host and reaches the
	// backend over 127.0.0.1, so there is no reason to expose the backend
	// listeners on all interfaces. Bind them to loopback unless the operator
	// overrode the addresses (the compose split needs 0.0.0.0 + a reachable
	// BackendHost).
	if withCaddy {
		if cfg.PublicAddr == ":8080" {
			cfg.PublicAddr = "127.0.0.1:8080"
		}
		if cfg.InternalAddr == ":9000" {
			cfg.InternalAddr = "127.0.0.1:9000"
		}
	}

	secret, err := auth.LoadOrCreateSecret(filepath.Join(cfg.DataDir, ".secret"))
	if err != nil {
		return err
	}
	api, err := httpapi.New(cfg, st, secret, web.Assets)
	if err != nil {
		return err
	}

	// Initialize the premium extension when this is an enterprise build with a
	// provider registered; the community build has none and stays fully open.
	if p, ok := ext.Get(); ok {
		if err := p.Init(extHost{cfg: cfg, secret: secret, sites: api.SiteService()}); err != nil {
			return fmt.Errorf("init %s extension: %w", p.Name(), err)
		}
		slog.Info("extension active", "name", p.Name(), "version", p.Version(),
			"accounts_enabled", p.AccountsEnabled())
	}
	if p, ok := ext.Get(); len(cfg.EmbedOrigins) > 0 && (!ok || !p.EmbedOriginsAllowed()) {
		slog.Warn("SITEBIN_EMBED_ORIGINS is set, but cross-origin embedding is an enterprise feature; ignoring it in this edition")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	public := &http.Server{Addr: cfg.PublicAddr, Handler: api.Public()}
	internal := &http.Server{Addr: cfg.InternalAddr, Handler: api.Internal()}
	errs := make(chan error, 4)
	go func() { errs <- fmt.Errorf("public listener: %w", public.ListenAndServe()) }()
	go func() { errs <- fmt.Errorf("internal listener: %w", internal.ListenAndServe()) }()
	go cleanup.Run(ctx, st, cfg.CleanupInterval)

	// Optional FTP server (off by default). Login is the edit UUID + edit
	// password; each session is confined to that site's files.
	var ftpSrv *ftp.Server
	if cfg.FTPEnabled {
		ftpSrv, err = ftp.New(cfg, api)
		if err != nil {
			return fmt.Errorf("ftp server: %w", err)
		}
		go func() { errs <- fmt.Errorf("ftp listener: %w", ftpSrv.ListenAndServe()) }()
		scheme := "ftp (plaintext)"
		if cfg.FTPTLSCert != "" {
			scheme = "ftps (TLS)"
		}
		slog.Warn("FTP enabled", "addr", cfg.FTPAddr, "mode", scheme,
			"passive_ports", fmt.Sprintf("%d-%d", cfg.FTPPasvMin, cfg.FTPPasvMax))
	}
	slog.Info("sitebin backend up",
		"base_domain", cfg.BaseDomain, "public", cfg.PublicAddr,
		"internal", cfg.InternalAddr, "data", cfg.DataDir,
		"version", version, "edition", edition)

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
	if ftpSrv != nil {
		ftpSrv.Stop()
	}
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

// listSites prints all sites for the operator (`sitebin list`).
func listSites() error {
	cfg := mustConfig()
	st := mustStore(cfg)
	sites, err := st.AllSites()
	if err != nil {
		return err
	}
	fmt.Printf("%-26s  %-9s  %5s  %-10s  %-20s  %s\n", "VIEW-ID", "SIZE", "FILES", "MODE", "CREATED", "OWNER/DOMAINS")
	for _, site := range sites {
		bytes, files, _ := st.Usage(site)
		owner := site.Meta.OwnerAccountID
		if len(site.Meta.CustomDomains) > 0 {
			owner += " " + strings.Join(site.Meta.CustomDomains, ",")
		}
		fmt.Printf("%-26s  %-9s  %5d  %-10s  %-20s  %s\n",
			site.ViewID, humanSize(bytes), files, site.Meta.Mode,
			site.Meta.CreatedAt.Format("2006-01-02 15:04"), strings.TrimSpace(owner))
	}
	fmt.Printf("\n%d site(s).\n", len(sites))
	return nil
}

// listReports prints filed abuse reports (`sitebin reports`).
func listReports() error {
	cfg := mustConfig()
	st := mustStore(cfg)
	reports, err := st.ListReports()
	if err != nil {
		return err
	}
	if len(reports) == 0 {
		fmt.Println("No reports.")
		return nil
	}
	for _, r := range reports {
		fmt.Printf("%s  target=%s  site=%s  ip=%s\n  reason: %s\n",
			r.Time.Format("2006-01-02 15:04:05"), r.Target, r.ViewID, r.IP, r.Reason)
		if r.Details != "" {
			fmt.Printf("  details: %s\n", r.Details)
		}
	}
	fmt.Printf("\n%d report(s). Take down a site with: sitebin delete <view-id|domain>\n", len(reports))
	return nil
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	const u = "KMGT"
	f, i := float64(n), -1
	for f >= 1024 && i < len(u)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f%cB", f, u[i])
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
