// Command sitebin is the Sitebin backend and all-in-one entrypoint.
//
//	sitebin run          backend + cleanup + supervised Caddy
//	sitebin caddyfile    print the generated Caddyfile and exit
//	sitebin cleanup      run one cleanup sweep and exit
//	sitebin healthcheck  probe the internal health endpoint (container HEALTHCHECK)
//	sitebin list         list all sites (operator)
//	sitebin reports      list filed abuse reports (operator)
//	sitebin provenance <id|domain|ip|cidr>  a site's trail, or every site seen from an address
//	sitebin lock <id|domain> [reason…]    lock a site: served to nobody, frozen, kept as evidence
//	sitebin unlock <id|domain>            lift a lock
//	sitebin hold <id|domain>              evidence hold on a locked site: a case is open, keep it past the lock retention
//	sitebin unhold <id|domain>            release the evidence hold
//	sitebin delete [--force] <id|domain>  operator takedown of a site (--force for a locked one)
//	sitebin scan <id|domain>|--all [--lock]  run the abuse rules over sites on disk
//	sitebin backup [file]       write a tar.gz of the data dir
//	sitebin restore <file>      restore the data dir from a backup
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	case "run":
		if err := serve(); err != nil {
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
		fmt.Printf("removed %d site(s): expired, or locked past the lock retention\n", n)
	case "healthcheck":
		cfg := mustConfig()
		if err := healthcheck(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		fmt.Println("ok")
	case "delete":
		force, args := forceFlag(os.Args[2:])
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "usage: sitebin delete [--force] <view-id|edit-id|domain>")
			os.Exit(2)
		}
		if err := deleteSite(mustStore(mustConfig()), os.Stdout, args[0], force); err != nil {
			fmt.Fprintln(os.Stderr, "delete failed:", err)
			os.Exit(1)
		}
	case "lock":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: sitebin lock <view-id|edit-id|domain> [reason…]")
			os.Exit(2)
		}
		if err := lockSite(mustStore(mustConfig()), os.Stdout, os.Args[2], strings.Join(os.Args[3:], " ")); err != nil {
			fmt.Fprintln(os.Stderr, "lock failed:", err)
			os.Exit(1)
		}
	case "unlock":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: sitebin unlock <view-id|edit-id|domain>")
			os.Exit(2)
		}
		if err := unlockSite(mustStore(mustConfig()), os.Stdout, os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "unlock failed:", err)
			os.Exit(1)
		}
	case "hold", "unhold":
		if len(os.Args) != 3 {
			fmt.Fprintf(os.Stderr, "usage: sitebin %s <view-id|edit-id|domain>\n", cmd)
			os.Exit(2)
		}
		if err := holdSite(mustStore(mustConfig()), os.Stdout, os.Args[2], cmd == "hold", time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, cmd+" failed:", err)
			os.Exit(1)
		}
	case "scan":
		all, lock, key := false, false, ""
		for _, a := range os.Args[2:] {
			switch a {
			case "--all":
				all = true
			case "--lock":
				lock = true
			default:
				key = a
			}
		}
		if all == (key != "") {
			fmt.Fprintln(os.Stderr, "usage: sitebin scan <view-id|edit-id|domain> [--lock]")
			fmt.Fprintln(os.Stderr, "       sitebin scan --all [--lock]")
			os.Exit(2)
		}
		if err := scanSites(mustStore(mustConfig()), os.Stdout, key, all, lock); err != nil {
			fmt.Fprintln(os.Stderr, "scan failed:", err)
			os.Exit(1)
		}
	case "list":
		if err := listSites(mustStore(mustConfig()), os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "list failed:", err)
			os.Exit(1)
		}
	case "provenance":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: sitebin provenance <view-id|edit-id|domain|ip|cidr>")
			os.Exit(2)
		}
		if err := showProvenance(mustStore(mustConfig()), os.Stdout, os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "provenance failed:", err)
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
		fmt.Printf("sitebin %s (%s edition%s)\n", version, edition, editionDetail())
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
	// Nobody may claim a custom domain under the view namespace.
	st.ReserveDomains(cfg.ViewDomain)
	// A custom domain is attached only once its DNS proves it belongs to the
	// site — unless the operator switched that off for a trusted instance.
	// The CNAME route needs the view host, which only exists with subdomain
	// views.
	viewDomain := ""
	if cfg.SubdomainViews() {
		viewDomain = cfg.ViewDomain
	}
	if cfg.DomainVerification == config.DomainVerifyOff {
		st.SetDomainVerifier(store.TrustingVerifier{}, viewDomain)
	} else {
		st.SetDomainVerifier(store.NewDNSVerifier(), viewDomain)
	}
	st.SetOperatorZones(cfg.OperatorDomains)
	// Every command that reads or sweeps locks agrees on when one expires:
	// the server's sweep, `sitebin cleanup`, `sitebin list`, `sitebin hold`.
	st.SetLockRetention(cfg.LockRetention)
	return st
}

// serve runs the backend, the cleanup sweep and a supervised Caddy child:
// the one shape Sitebin ships in.
func serve() error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg := mustConfig()
	st := mustStore(cfg)

	// Caddy is a child on the same host and reaches the backend over
	// 127.0.0.1, so the backend listeners are bound to loopback unless the
	// operator chose other addresses: the internal one answers Caddy's
	// authz subrequests and must not be reachable from the docker network.
	if cfg.PublicAddr == ":8080" {
		cfg.PublicAddr = "127.0.0.1:8080"
	}
	if cfg.InternalAddr == ":9000" {
		cfg.InternalAddr = "127.0.0.1:9000"
	}

	secret, err := auth.LoadOrCreateSecret(filepath.Join(cfg.DataDir, ".secret"))
	if err != nil {
		return err
	}
	httpapi.Version = version // reported to MCP clients at initialize
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
	// Who the operator is, for operator zones. Only a running server wires
	// it: the one-shot cleanup command leaves it unset, and an unanswerable
	// check changes no domain.
	st.SetOperatorCheck(func(owner string) bool {
		p, ok := ext.Get()
		if !ok {
			return false
		}
		op, ok := p.(ext.OperatorAccounts)
		return ok && op.IsOperator(owner)
	})
	// Account zones: the plan says how many, and only a running server asks.
	st.SetZoneCheck(func(owner string) (int, error) {
		p, ok := ext.Get()
		if !ok {
			return 0, nil
		}
		za, ok := p.(ext.ZoneAccounts)
		if !ok {
			return 0, nil
		}
		return za.ZonesAllowed(owner)
	})
	st.SetZoneNamesPerHour(cfg.ZoneNamesPerHour)
	if len(cfg.OperatorDomains) > 0 {
		slog.Info("operator zones", "zones", strings.Join(cfg.OperatorDomains, ","))
	}
	// The abuse guard runs on every write whatever is configured; what the
	// operator hears of it depends on these two.
	if rs := st.AbuseRules(); rs != nil {
		slog.Info("abuse guard", "rules", rs.Rules(), "destinations", rs.Destinations(),
			"rules_file", filepath.Join(cfg.DataDir, store.AbuseRulesFile))
	}
	switch {
	case len(cfg.AbuseAlertsTo) == 0:
		slog.Warn("abuse alerts go to nobody: set SITEBIN_ABUSE_ALERTS_TO (or SITEBIN_ADMIN_ACCOUNTS)")
	case cfg.FormsSMTP == nil:
		slog.Warn("abuse alerts are only logged: they are mailed through the SITEBIN_FORMS_SMTP_* mailer, which is not configured")
	default:
		slog.Info("abuse alerts", "to", strings.Join(cfg.AbuseAlertsTo, ","))
	}
	if cfg.DomainVerification == config.DomainVerifyOff {
		slog.Warn("SITEBIN_DOMAIN_VERIFICATION=off: custom domains are attached without proof of ownership; only safe when every account holder is trusted")
	}
	if p, ok := ext.Get(); len(cfg.EmbedOrigins) > 0 && (!ok || !p.EmbedOriginsAllowed()) {
		slog.Warn("SITEBIN_EMBED_ORIGINS is set, but cross-origin embedding is an enterprise feature; ignoring it in this edition")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	public := newPublicServer(cfg.PublicAddr, api.Public())
	internal := newInternalServer(cfg.InternalAddr, api.Internal())
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
	if cfg.MCPEnabled {
		// Worth its own line: with an issuer set, /mcp answers every call
		// that needs an account with a 401 sign-in challenge, and an operator
		// who cannot see that from the log will read those 401s as an outage.
		slog.Info("mcp server enabled", "path", "/mcp",
			"oauth", cfg.MCPOAuthIssuer != "", "issuer", cfg.MCPOAuthIssuer)
	}
	slog.Info("sitebin backend up",
		"base_domain", cfg.BaseDomain, "public", cfg.PublicAddr,
		"internal", cfg.InternalAddr, "data", cfg.DataDir,
		"version", version, "edition", edition)

	caddyDir := filepath.Join(cfg.DataDir, "caddy")
	if err := os.MkdirAll(caddyDir, 0o755); err != nil {
		return err
	}
	caddyfile := filepath.Join(caddyDir, "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte(caddygen.Generate(cfg)), 0o644); err != nil {
		return err
	}
	caddyDone, err := supervisor.StartCaddy(ctx, caddyfile)
	if err != nil {
		return fmt.Errorf("start caddy: %w", err)
	}
	slog.Info("caddy started", "caddyfile", caddyfile, "https", !cfg.HTTPOnly)

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("shutting down (signal)")
	case err := <-errs:
		runErr = err
	case err := <-caddyDone:
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
	select { // give caddy a moment to drain
	case <-caddyDone:
	case <-time.After(15 * time.Second):
	}
	if runErr != nil && !errors.Is(runErr, http.ErrServerClosed) {
		return runErr
	}
	return nil
}

// newPublicServer is the listener Caddy proxies. Caddy fronts it, but it
// proxies bodies through, so a slow client reaches this server: headers are
// bounded tightly, the body generously — a full-size site over a slow link
// takes minutes — and idle keep-alives are reaped.
func newPublicServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
}

// newInternalServer answers Caddy's own subrequests (authz, tls-check) and
// the healthcheck. Nothing on it is slow, so nothing on it may wait long.
func newInternalServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
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

// listSites prints all sites for the operator (`sitebin list`). A locked
// site says so in its row, and the lock's date, author and reason follow on
// a line of their own.
func listSites(st *store.Store, out io.Writer) error {
	sites, err := st.AllSites()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%-26s  %-9s  %5s  %-10s  %-20s  %-15s  %-6s  %s\n", "VIEW-ID", "SIZE", "FILES", "MODE", "CREATED", "FROM", "LOCK", "OWNER/DOMAINS")
	locked := 0
	for _, site := range sites {
		bytes, files, _ := st.Usage(site)
		owner := site.Meta.OwnerAccountID
		if len(site.Meta.CustomDomains) > 0 {
			owner += " " + strings.Join(site.Meta.CustomDomains, ",")
		}
		lock := "-"
		if site.Meta.IsLocked() {
			lock = "LOCKED"
			locked++
		}
		fmt.Fprintf(out, "%-26s  %-9s  %5d  %-10s  %-20s  %-15s  %-6s  %s\n",
			site.ViewID, humanSize(bytes), files, site.Meta.Mode,
			site.Meta.CreatedAt.Format("2006-01-02 15:04"), creatorIP(st, site), lock, strings.TrimSpace(owner))
		if l := site.Meta.Locked; l != nil {
			fmt.Fprintf(out, "    locked %s by %s%s · %s\n", l.At.Format("2006-01-02 15:04"), l.By, reasonSuffix(l.Reason), retentionState(st, l))
		}
	}
	fmt.Fprintf(out, "\n%d site(s), %d locked.\n", len(sites), locked)
	return nil
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
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
		via := r.Via
		if via == "" {
			via = store.ReportViaAPI // before the report page, the API was the only way in
		}
		fmt.Printf("%s  target=%s  site=%s  source=%s  via=%s\n  reason: %s\n",
			r.Time.Format("2006-01-02 15:04:05"), r.Target, r.ViewID, r.Source, via, r.Reason)
		if r.Contact != "" {
			fmt.Printf("  contact: %s\n", r.Contact)
		}
		if r.Details != "" {
			fmt.Printf("  details: %s\n", r.Details)
		}
	}
	fmt.Printf("\n%d report(s). Hold a site as evidence with: sitebin lock <view-id|domain> <reason>; take one down with: sitebin delete <view-id|domain>\n", len(reports))
	return nil
}

func humanSize(n int64) string { return store.HumanBytes(n) }

// findSite resolves what an operator typed: a view id, an edit id, or a
// custom domain.
func findSite(st *store.Store, key string) (*store.Site, error) {
	site, err := st.ByViewID(key)
	if err != nil {
		site, err = st.ByEditID(key)
	}
	if err != nil {
		site, err = st.ByDomain(key)
	}
	if err != nil {
		return nil, fmt.Errorf("no site found for %q", key)
	}
	return site, nil
}

// forceFlag takes --force out of args, wherever it stands.
func forceFlag(args []string) (bool, []string) {
	force := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--force" || a == "-f" {
			force = true
			continue
		}
		rest = append(rest, a)
	}
	return force, rest
}

// deleteSite is the operator/abuse takedown. A locked site is the operator's
// own evidence hold, so deleting one takes --force: typing the takedown for a
// site somebody locked for an abuse report must not quietly end the hold.
func deleteSite(st *store.Store, out io.Writer, key string, force bool) error {
	site, err := findSite(st, key)
	if err != nil {
		return err
	}
	if site.Meta.IsLocked() && !force {
		return fmt.Errorf("site %s is locked%s — unlock it first, or delete it anyway with: sitebin delete --force %s",
			site.ViewID, reasonSuffix(site.Meta.Locked.Reason), site.ViewID)
	}
	del := st.Delete
	if force {
		del = st.ForceDelete
	}
	if err := del(site); err != nil {
		return err
	}
	fmt.Fprintln(out, "deleted site", site.ViewID)
	return nil
}

// lockSite places the operator's lock on a site from the command line: it is
// served to nobody from the next request on, frozen for its owner, and kept
// past its expiry — up to the lock retention, unless an evidence hold is
// placed. The running server needs no signal — every gate reads the lock
// from meta.json — and its container runtime stops a locked project on its
// next pass. Locking a locked site replaces the lock, which turns a
// suspension's lock into the operator's own; the lock's date, the
// retention's clock, stays.
func lockSite(st *store.Store, out io.Writer, key, reason string) error {
	site, err := findSite(st, key)
	if err != nil {
		return err
	}
	if _, err := st.SetLock(site, &store.SiteLock{Reason: reason, By: store.LockByAdmin}); err != nil {
		return err
	}
	l := site.Meta.Locked
	fmt.Fprintf(out, "locked site %s%s (locked since %s)\n", site.ViewID, reasonSuffix(l.Reason), l.At.Format("2006-01-02 15:04"))
	fmt.Fprintf(out, "It is served to nobody, frozen for its owner and kept past its expiry; %s.\n", retentionState(st, l))
	fmt.Fprintln(out, "Lift it with: sitebin unlock "+site.ViewID)
	return nil
}

// retentionState says what the lock retention does with a lock: purge the
// site on a date, nothing while an evidence hold stands, or nothing at all.
func retentionState(st *store.Store, l *store.SiteLock) string {
	if h := l.Hold; h != nil {
		out := fmt.Sprintf("held (case open) since %s by %s", h.At.Format("2006-01-02"), h.By)
		if end, ok := st.LockRetentionEnds(l); ok {
			out += ", retention ends " + end.Format("2006-01-02")
		}
		return out
	}
	if at, ok := st.LockPurgeAt(l); ok {
		return "purge due " + at.Format("2006-01-02 15:04")
	}
	return "kept until unlocked (SITEBIN_LOCK_RETENTION_DAYS=0)"
}

// holdSite places or releases the evidence hold on a locked site: while a
// case, investigation or proceeding is open, the sweep does not purge it
// past the lock retention. Like a lock it lives in meta.json, where the
// running server's sweep reads it; no signal is needed.
func holdSite(st *store.Store, out io.Writer, key string, hold bool, now time.Time) error {
	site, err := findSite(st, key)
	if err != nil {
		return err
	}
	var h *store.LockHold
	if hold {
		h = &store.LockHold{At: now, By: store.HoldByCLI}
	}
	changed, err := st.SetHold(site, h)
	if errors.Is(err, store.ErrNotLocked) {
		return fmt.Errorf("site %s is not locked; an evidence hold keeps a locked site — lock it first with: sitebin lock %s <reason>", site.ViewID, site.ViewID)
	}
	if err != nil {
		return err
	}
	l := site.Meta.Locked
	switch {
	case hold && !changed:
		fmt.Fprintf(out, "site %s is already held: %s\n", site.ViewID, retentionState(st, l))
	case hold:
		fmt.Fprintf(out, "evidence hold placed on site %s: it is kept past the lock retention until you release it with: sitebin unhold %s\n", site.ViewID, site.ViewID)
	case !changed:
		fmt.Fprintf(out, "site %s had no evidence hold; %s\n", site.ViewID, retentionState(st, l))
	default:
		fmt.Fprintf(out, "evidence hold released on site %s; %s", site.ViewID, retentionState(st, l))
		if at, ok := st.LockPurgeAt(l); ok && !at.After(now) {
			fmt.Fprint(out, " — already past, so the next sweep purges the site")
		}
		fmt.Fprintln(out)
	}
	return nil
}

// unlockSite lifts any lock. The site's expiry applies again: one already
// past means the next sweep deletes the site after the usual grace.
func unlockSite(st *store.Store, out io.Writer, key string) error {
	site, err := findSite(st, key)
	if err != nil {
		return err
	}
	changed, err := st.SetLock(site, nil)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(out, "site %s was not locked\n", site.ViewID)
		return nil
	}
	fmt.Fprintf(out, "unlocked site %s: it is served again", site.ViewID)
	if at := site.Meta.ExpiresAt; at != nil {
		fmt.Fprintf(out, ", and its expiry (%s) applies again", at.Format("2006-01-02 15:04"))
		if at.Before(time.Now()) {
			fmt.Fprint(out, " — it has passed, so the next sweep deletes the site")
		}
	}
	fmt.Fprintln(out)
	return nil
}
