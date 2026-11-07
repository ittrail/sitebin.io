//go:build ee

package ee

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/snapshots"
)

// The operator's Backups view in the workspace: the instance's snapshots and
// the backup runs, as the agent on the host reports them, and the restore
// of the whole instance to one of them (or back to a kept copy). The agent
// does the work; the console asks it. See
// docs/superpowers/specs/2026-11-07-snapshots-and-restore-design.md.

// snapshotsClient answers the agent's client when the instance has one
// (SITEBIN_SNAPSHOTS_AGENT), else nil.
func (p *provider) snapshotsClient() *snapshots.Client {
	p.snapOnce.Do(func() {
		if p.snap != nil {
			return
		}
		if h, ok := p.host.(ext.SnapshotsHost); ok && h.SnapshotsAgent() != "" {
			p.snap = snapshots.NewClient(h.SnapshotsAgent())
		}
	})
	return p.snap
}

func (p *provider) handleAdminBackups(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.adminAccount(r); !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	c := p.snapshotsClient()
	if c == nil {
		writeJSONStatus(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out := map[string]any{"available": true}
	for key, path := range map[string]string{"status": "/v1/status", "snapshots": "/v1/snapshots", "copies": "/v1/instance/copies"} {
		raw, err := c.Raw(ctx, path)
		if err != nil {
			slog.Error("admin backups: agent", "path", path, "err", err)
			out["error"] = "The backups agent on the host does not answer. Is sitebin-agent running?"
			continue
		}
		out[key] = raw
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, http.StatusOK, out)
}

// agentAnswer passes an agent error on with its status.
func agentAnswer(w http.ResponseWriter, err error) {
	var ae *snapshots.AgentError
	if errors.As(err, &ae) {
		msg := ae.Msg
		if msg == "" {
			msg = http.StatusText(ae.Code)
		}
		writeJSONStatus(w, ae.Code, map[string]string{"error": msg})
		return
	}
	writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": "The backups agent on the host does not answer."})
}

// handleAdminBackupRestore starts the instance restore. The operator types
// the word "restore": everything since the snapshot is replaced.
func (p *provider) handleAdminBackupRestore(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	c := p.snapshotsClient()
	if !ok || c == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !p.checkCSRF(r, acc) {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	snap, cp := r.PostFormValue("snapshot"), r.PostFormValue("copy")
	if strings.ToLower(strings.TrimSpace(r.PostFormValue("confirm"))) != "restore" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": `Type "restore" to confirm.`})
		return
	}
	if (snap == "") == (cp == "") {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "Pick one snapshot or one kept copy."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	st, err := c.RestoreInstance(ctx, snap, cp)
	if err != nil {
		slog.Error("admin backups: instance restore refused", "admin", acc.ID, "snapshot", snap, "copy", cp, "err", err)
		agentAnswer(w, err)
		return
	}
	slog.Warn("operator started an instance restore", "admin", acc.ID, "snapshot", snap, "copy", cp)
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"ok": true, "restore": st})
}

func (p *provider) handleAdminBackupCopyDelete(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	c := p.snapshotsClient()
	if !ok || c == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !p.checkCSRF(r, acc) {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := c.DeleteCopy(ctx, name); err != nil {
		agentAnswer(w, err)
		return
	}
	slog.Info("operator deleted a kept copy of the data", "admin", acc.ID, "copy", name)
	writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true})
}
