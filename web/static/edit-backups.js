// The Backups tab of the edit page (and of the workspace's site page, which
// runs this file too): the versions of the site's files the instance's
// snapshots hold, newest first, each downloadable as a ZIP or restorable.
// Loaded after edit.js, which owns api(), authHeaders(), site, editID, tabs,
// render() and toast(). Shown only when the site payload says
// backups_available. Text from the server enters the DOM as text only.
"use strict";

const backupsTab = (() => {
  const KIND = { hourly: "Hourly", nightly: "Nightly", predeploy: "Before a deploy", archive: "Backup" };
  let data = null, loading = false, busy = "";

  const el = (tag, attrs, ...kids) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (k === "class") n.className = v;
      else if (k === "text") n.textContent = v;
      else n.setAttribute(k, v);
    }
    for (const k of kids) if (k != null) n.append(k);
    return n;
  };
  const when = (iso) => new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
  const panel = () => $("panel-backups");

  // The workspace has its own dialog; the classic page asks the browser.
  // WS is a top-level const of the workspace's shell.js: visible to this
  // script, but not a property of window.
  async function ask(title, body, ok) {
    if (typeof WS !== "undefined" && typeof WS.confirm === "function") return WS.confirm(title, body, ok, true);
    return window.confirm(title + "\n\n" + body);
  }

  async function load() {
    if (loading) return;
    loading = true;
    try {
      data = await api("GET", "/backups");
    } catch (err) {
      data = { error: err.message || "The backups could not be read." };
    }
    loading = false;
    draw();
  }

  function intro() {
    const s = site || {};
    const box = el("section", { class: "card" },
      el("h3", { text: "Backups" }),
      el("p", { class: "sub", text: "Snapshots of this site’s files: every hour for the last two days, and every night for 30 days. Restoring one replaces the site’s files with those of that time. Settings, addresses, forms, sharing and the Git repository stay as they are." }),
      el("p", { class: "sub", text: "Before a restore, a snapshot of the files as they are now is taken, so a restore can be undone: they are the newest version in this list afterwards." }));
    if (s.mode === "container") {
      box.append(el("p", { class: "sub", text: "The project stops while its files are restored and starts again afterwards. A database the app was writing when a snapshot was taken may not be consistent in it: keep your own export of anything you cannot lose." }));
    }
    return box;
  }

  function draw() {
    const p = panel();
    if (!p) return;
    p.replaceChildren(intro());
    const card = el("section", { class: "card" }, el("h3", { text: "Versions" }));
    p.append(card);
    if (!data) { card.append(el("p", { class: "sub", text: "Loading…" })); return; }
    if (data.error) {
      card.append(el("p", { class: "sub", text: data.error }), el("button", { class: "btn small", type: "button", "data-backups": "reload", text: "Try again" }));
      return;
    }
    const vs = data.versions || [];
    if (!vs.length) {
      card.append(el("p", { class: "sub", text: data.pending ? "The snapshots are still being read. Look again in a few minutes." : "No snapshot holds this site yet. The first one is taken within the hour." }));
      return;
    }
    const tbody = el("tbody");
    for (const v of vs) {
      const label = el("div", null, el("b", { text: when(v.at) }));
      const sub = [KIND[v.kind] || v.kind];
      if (v.first && when(v.first) !== when(v.at)) sub.push("unchanged since " + when(v.first));
      if (v.current) sub.push("the files now");
      label.append(el("div", { class: "sub", text: sub.join(" · ") }));
      const acts = el("td", { style: "text-align:right;white-space:nowrap" },
        el("button", { class: "btn small", type: "button", "data-backups": "download", "data-snapshot": v.snapshot, text: "Download ZIP" }), " ",
        el("button", { class: "btn small" + (v.current ? "" : " primary"), type: "button", "data-backups": "restore", "data-snapshot": v.snapshot, "data-at": v.at, "data-files": String(v.files), text: "Restore…" }));
      if (busy) acts.querySelectorAll("button").forEach((b) => { b.disabled = true; });
      tbody.append(el("tr", null, el("td", null, label),
        el("td", { class: "sub", style: "white-space:nowrap", text: v.files.toLocaleString("en") + (v.files === 1 ? " file" : " files") + " · " + fmtSize(v.bytes) }),
        acts));
    }
    card.append(el("table", { class: "filetable" }, tbody));
    if (data.pending) card.append(el("p", { class: "sub", text: "Some snapshots are still being read; more versions may appear." }));
    if (busy) card.append(el("p", { class: "sub", text: busy }));
  }

  // fmtSize is edit.js's byte formatter when there is one.
  function fmtSize(n) { return typeof fmtBytes === "function" ? fmtBytes(n) : n + " B"; }

  async function download(snap) {
    try {
      const res = await fetch("/api/sites/" + editID + "/backups/" + encodeURIComponent(snap) + "/download", { headers: authHeaders() });
      if (!res.ok) { let m = ""; try { m = (await res.json()).error; } catch {} throw new Error(m || "download failed (" + res.status + ")"); }
      const url = URL.createObjectURL(await res.blob());
      const a = document.createElement("a");
      a.href = url;
      a.download = site.id + "-" + snap + ".zip";
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
    } catch (err) { toast(err.message, true); }
  }

  async function restore(snap, at, files) {
    const n = Number(files) || 0;
    const ok = await ask("Restore the files of " + when(at) + "?",
      "The site’s files are replaced by the " + n.toLocaleString("en") + (n === 1 ? " file" : " files") + " of that time. A snapshot of the files as they are now is taken first, so you can undo this." +
      (site.mode === "container" ? " The project stops while the files are restored and starts again afterwards." : ""),
      "Restore");
    if (!ok) return;
    busy = "Restoring… this can take a minute for a large site.";
    draw();
    try {
      const res = await api("POST", "/backups/" + encodeURIComponent(snap) + "/restore");
      site = res;
      if (typeof render === "function") render();
      toast("Restored the files of " + when(at) + ".");
    } catch (err) {
      toast(err.message, true);
    }
    busy = "";
    data = null;
    load();
  }

  document.addEventListener("click", (e) => {
    const b = e.target.closest("[data-backups]");
    if (!b || !panel() || !panel().contains(b)) return;
    if (b.dataset.backups === "reload") { data = null; draw(); load(); }
    else if (b.dataset.backups === "download") download(b.dataset.snapshot);
    else if (b.dataset.backups === "restore") restore(b.dataset.snapshot, b.dataset.at, b.dataset.files);
  });

  return {
    show() {
      // A reload on every showing: a new snapshot appears every hour.
      if (!busy) { data = null; draw(); load(); }
    },
  };
})();
