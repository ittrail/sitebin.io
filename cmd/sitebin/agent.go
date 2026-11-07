package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ittrail/sitebin.io/internal/snapshots"
)

// runAgent is `sitebin agent`: run on the instance's HOST, beside its backups,
// it answers the instance over a unix socket the container mounts — the
// snapshots of a site, a snapshot now, and the instance restore. See
// docs/superpowers/specs/2026-11-07-snapshots-and-restore-design.md.
func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	sock := fs.String("socket", "/run/sitebin-agent/agent.sock", "the unix socket to serve on; its folder is bind-mounted into the container")
	gid := fs.Int("socket-gid", -1, "the group the socket belongs to (default: the data directory's group, the container user's)")
	data := fs.String("data", "", "the HOST path of the instance's data directory (required)")
	state := fs.String("state", "/var/lib/sitebin-agent", "where the agent keeps its index and the restore's state")
	source := fs.String("source", "borg", "where the snapshots are: borg (the BORG_* environment) or dir")
	dir := fs.String("dir", "", "with -source dir: the folder of `sitebin backup` archives")
	backupNow := fs.String("backup-now", "", "the command that takes a snapshot now (a site restore asks for one first); empty: none")
	backupLock := fs.String("backup-lock", "", "the file the backup runs flock; held while the instance's data is swapped")
	statusDir := fs.String("status-dir", "", "the folder of the backup runs' last-<mode>.json notes")
	container := fs.String("container", "sitebin", "the instance's container")
	docker := fs.String("docker", "/var/run/docker.sock", "the Docker Engine's socket")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *data == "" {
		return errors.New("-data is required: the host path of the instance's data directory")
	}
	dataDir, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dataDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("-data %s is not a directory", dataDir)
	}
	var src snapshots.Source
	switch *source {
	case "borg":
		src = &snapshots.BorgSource{}
	case "dir":
		if *dir == "" {
			return errors.New("-source dir needs -dir")
		}
		src = &snapshots.DirSource{Dir: *dir}
	default:
		return fmt.Errorf("-source %q: borg or dir", *source)
	}
	owner := ownerOf(dataDir)
	if *gid < 0 && owner != nil {
		*gid = owner[1]
	}
	agent := snapshots.NewAgent(snapshots.AgentConfig{
		Source: src, SourceID: *source, StateDir: *state, DataDir: dataDir,
		BackupNow: strings.Fields(*backupNow), BackupLock: *backupLock, StatusDir: *statusDir,
		Container: *container, Docker: snapshots.NewDockerClient(*docker),
		Extract: func(dst string, r io.Reader) error {
			_, err := restoreFrom(dst, r, restoreOpts{Owner: owner})
			return err
		},
	})

	if err := os.MkdirAll(filepath.Dir(*sock), 0o750); err != nil {
		return err
	}
	os.Remove(*sock)
	ln, err := net.Listen("unix", *sock)
	if err != nil {
		return err
	}
	// Only root and the container's group: customer containers never mount
	// the folder, and nothing else on the host needs it.
	if err := os.Chmod(*sock, 0o660); err != nil {
		return err
	}
	if *gid >= 0 {
		os.Chown(filepath.Dir(*sock), -1, *gid)
		if err := os.Chown(*sock, -1, *gid); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go agent.Run(ctx)
	srv := &http.Server{Handler: agent.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	slog.Info("sitebin agent up", "socket", *sock, "gid", *gid, "data", dataDir, "source", *source, "backup_now", *backupNow != "")
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
