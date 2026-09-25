// Sitebin edit page: unlock with the edit password, then manage files,
// settings, domains and the site's lifecycle.
"use strict";

const $ = (id) => document.getElementById(id);

const editID = location.pathname.split("/").filter(Boolean).pop();
const pwKey = "sb-pw-" + editID;
let sitePw = sessionStorage.getItem(pwKey) || "";
let site = null; // last payload from the server

// ---- helpers ----

function fmtBytes(n) {
  if (n < 1024) return n + " B";
  const units = ["KB", "MB", "GB"];
  let u = -1;
  do { n /= 1024; u++; } while (n >= 1024 && u < units.length - 1);
  return n.toFixed(n >= 10 ? 0 : 1) + " " + units[u];
}

let toastTimer;
function toast(msg, isErr) {
  const el = $("toast");
  el.textContent = msg;
  el.classList.toggle("err", !!isErr);
  el.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), 3200);
}

// authHeaders are sent with every API call. X-Sitebin-Session asks the server
// to honour the browser's account session: a signed-in owner needs no edit
// password. A page on another origin cannot send this header, which is what
// makes that safe (see sessionOwns in internal/httpapi/server.go).
function authHeaders() {
  return { "X-Edit-Password": sitePw, "X-Sitebin-Session": "1" };
}

async function api(method, path, body, isForm) {
  const headers = authHeaders();
  if (body && !isForm) headers["Content-Type"] = "application/json";
  const res = await fetch("/api/sites/" + editID + path, {
    method,
    headers,
    body: body ? (isForm ? body : JSON.stringify(body)) : undefined,
  });
  let data = {};
  try { data = await res.json(); } catch {}
  if (!res.ok) throw Object.assign(new Error(data.error || res.statusText), { status: res.status, accountURL: data.account_url });
  return data;
}

// ---- unlock flow ----

function unlock() {
  $("lock").classList.add("hidden");
  $("app").classList.remove("hidden");
  render();
  loadDir(dirFromHash());
  loadForms();
}

$("lockform").addEventListener("submit", async (e) => {
  e.preventDefault();
  sitePw = $("lockpw").value;
  if (!sitePw) return;
  try {
    site = await api("GET", "");
    sessionStorage.setItem(pwKey, sitePw);
    unlock();
  } catch (err) {
    $("lockerr").textContent =
      err.status === 429 ? "Too many attempts — wait a moment." : "Wrong password — check your claim ticket.";
  }
});

// boot opens the site without asking whenever it can: for its signed-in
// owner (the session), or with a password this tab already entered. Only
// otherwise does the lock screen appear.
(async function boot() {
  try {
    site = await api("GET", "");
    unlock();
  } catch (err) {
    if (sitePw) {
      sessionStorage.removeItem(pwKey);
      sitePw = "";
    }
    if (err.accountURL) {
      $("lock-account-link").href = err.accountURL;
      $("lock-account").classList.remove("hidden");
    }
    $("lock").classList.remove("hidden");
    $("lockpw").focus();
  }
})();

// ---- rendering ----

function render() {
  // The name, when there is one, is the heading; the view id then moves into
  // the meta line, so the page still says which site this is.
  $("site-id").textContent = site.name || site.id;
  document.title = "Manage " + (site.name || site.id) + " — Sitebin";
  $("view-link").href = site.view_url;
  let meta = (site.name ? site.id + " · " : "") + "created " + new Date(site.created_at).toLocaleString() +
    " · updated " + new Date(site.updated_at).toLocaleString();
  if (typeof site.views === "number") {
    meta += " · " + site.views + (site.views === 1 ? " view" : " views");
    if (site.last_seen) meta += " (last " + new Date(site.last_seen).toLocaleString() + ")";
  }
  $("site-meta").textContent = meta;

  renderFiles();

  const pct = Math.min(100, (site.usage.bytes / site.usage.max_bytes) * 100);
  $("usage-fill").style.width = pct + "%";
  $("usage-note").textContent =
    fmtBytes(site.usage.bytes) + " of " + fmtBytes(site.usage.max_bytes) +
    " · " + site.usage.files + (site.usage.max_files ? "/" + site.usage.max_files : "") + " files";

  // name: never overwrite what is being typed (a container site re-renders
  // on a timer while it starts)
  if (document.activeElement !== $("e-name")) $("e-name").value = site.name || "";
  $("clear-name").classList.toggle("hidden", !site.name);

  // mode + entry
  const ct = site.container || {};
  $("mode-container").classList.toggle("hidden", !ct.available && site.mode !== "container");
  $("mode-sub").textContent = "Web server serves files as-is. File viewer renders one document in the browser." +
    (ct.available ? " Container runs the project that sitebin-container-compose.yaml declares." : "");
  document.querySelector(`input[name=emode][value=${site.mode}]`).checked = true;
  $("card-container").classList.toggle("hidden", site.mode !== "container");
  $("card-domains").classList.toggle("hidden", site.mode === "container");
  if (site.mode === "container") renderContainer();
  $("entrywrap").classList.toggle("hidden", site.mode !== "viewer");
  $("spawrap").classList.toggle("hidden", site.mode !== "webserver");
  $("e-spa").checked = !!site.spa_fallback;
  if (site.mode === "viewer") {
    const sel = $("entry-file");
    sel.innerHTML = "";
    for (const f of site.files) {
      const opt = document.createElement("option");
      opt.value = f.path;
      opt.textContent = f.path;
      if (f.path === site.entry_file) opt.selected = true;
      sel.appendChild(opt);
    }
  }

  // access
  $("viewpw-status").textContent = site.view_password_protected
    ? "Protection is on — visitors need the password."
    : "This site is public.";
  $("clear-viewpw").classList.toggle("hidden", !site.view_password_protected);
  $("e-webdav").checked = site.webdav_enabled;
  $("davinfo").classList.toggle("hidden", !site.webdav_enabled || !site.webdav_url);
  if (site.webdav_url) {
    $("dav-url").textContent = site.webdav_url;
    // Windows/macOS built-in WebDAV clients won't send Basic auth over plain HTTP.
    $("dav-http-note").classList.toggle("hidden", !site.webdav_url.startsWith("http://"));
  }

  // FTP (only shown when the instance has FTP enabled)
  $("ftp-opt").classList.toggle("hidden", !site.ftp_available);
  $("e-ftp").checked = site.ftp_enabled;
  $("ftpinfo").classList.toggle("hidden", !site.ftp_enabled || !site.ftp_url);
  if (site.ftp_url) $("ftp-url").textContent = site.ftp_url;

  const capped = site.expiry_cap_days > 0;
  if (site.expires_at) {
    const d = new Date(site.expires_at);
    $("e-expires").value = new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
    let note = "";
    if (capped) {
      note = site.expiry_renews
        ? " — this plan's sites always have an expiry, renewed to the full term on every change. Set your own date here and it stops renewing."
        : " — this plan's sites always have an expiry. You can bring the date forward, but not push it back.";
    }
    $("expiry-status").textContent = "Expires " + d.toLocaleString() + note;
  } else {
    $("e-expires").value = "";
    $("expiry-status").textContent = "No expiry — the site stays up.";
  }
  $("clear-expiry").classList.toggle("hidden", capped);

  // domains: verified ones serve; pending ones show the record that proves them
  const dr = $("domainrows");
  dr.innerHTML = "";
  const pending = site.pending_domains || [];
  if (!site.custom_domains.length && !pending.length) {
    dr.innerHTML = '<div class="domainrow" style="color:var(--ink-faint)">No custom domains yet.</div>';
  }
  for (const p of pending) {
    const row = document.createElement("div");
    row.className = "domainrow pending";
    const name = document.createElement("span");
    name.className = "d";
    name.textContent = p.domain;
    const tag = document.createElement("span");
    tag.className = "sub";
    tag.textContent = "pending verification";
    const rec = document.createElement("div");
    rec.className = "dnshint";
    rec.style.flexBasis = "100%";
    const txt = document.createElement("div");
    txt.append("Prove you control it with a TXT record: ");
    const n = document.createElement("code"); n.textContent = p.txt_name;
    const v = document.createElement("code"); v.textContent = p.txt_value;
    txt.append(n, " = ", v);
    rec.append(txt);
    if (p.cname_target) {
      const cn = document.createElement("div");
      cn.append("or a CNAME: ");
      const c = document.createElement("code"); c.textContent = p.domain;
      const t = document.createElement("code"); t.textContent = p.cname_target;
      cn.append(c, " \u2192 ", t);
      rec.append(cn);
    }
    const note = document.createElement("div");
    note.textContent = "Checked automatically every few minutes; a claim that never verifies is dropped after 7 days.";
    rec.append(note);
    const check = document.createElement("button");
    check.className = "btn small";
    check.textContent = "Check now";
    check.addEventListener("click", async () => {
      try {
        site = await api("POST", "/domains", { domain: p.domain });
        render();
        toast(site.custom_domains.includes(p.domain) ? "Verified " + p.domain : "Not verified yet: the record is not visible");
      } catch (err) { toast(err.message, true); }
    });
    const rm = document.createElement("button");
    rm.className = "btn small";
    rm.textContent = "Remove";
    rm.addEventListener("click", async () => {
      try {
        site = await api("DELETE", "/domains/" + encodeURIComponent(p.domain));
        render();
        toast("Removed " + p.domain);
      } catch (err) { toast(err.message, true); }
    });
    row.append(name, tag, check, rm, rec);
    dr.appendChild(row);
  }
  for (const d of site.custom_domains) {
    const row = document.createElement("div");
    row.className = "domainrow";
    const name = document.createElement("span");
    name.className = "d";
    name.textContent = d;
    const zone = (site.zone_domains || {})[d];
    if (zone) {
      // Held through the owner's own zone: no record of its own needed.
      const via = document.createElement("span");
      via.className = "via";
      via.textContent = "via zone " + zone;
      name.append(" ", via);
    }
    const rm = document.createElement("button");
    rm.className = "btn small";
    rm.textContent = "Remove";
    rm.addEventListener("click", async () => {
      try {
        site = await api("DELETE", "/domains/" + encodeURIComponent(d));
        render();
        toast("Removed " + d);
      } catch (err) { toast(err.message, true); }
    });
    row.append(name, rm);
    dr.appendChild(row);
  }
  $("dns-target").textContent = site.dns_target;
}

// ---- containers ----

let ctPoll;
function renderContainer() {
  const ct = site.container;
  const st = $("ct-status");
  st.textContent = ct.enabled ? ct.status : "stopped";
  st.className = "ctstatus " + (ct.enabled ? ct.status : "stopped");
  $("ct-start").classList.toggle("hidden", ct.enabled && ct.status !== "error");
  $("ct-stop").classList.toggle("hidden", !ct.enabled);
  $("ct-restart").classList.toggle("hidden", !ct.enabled);

  const msg = $("ct-message");
  msg.classList.toggle("hidden", !ct.message);
  msg.classList.toggle("note", ct.status !== "error");
  msg.textContent = ct.message || "";

  const pending = {};
  for (const p of site.pending_domains || []) pending[p.domain] = p;
  const box = $("ct-services");
  box.innerHTML = "";
  if (!ct.services.length) {
    const empty = document.createElement("div");
    empty.className = "ctsvc";
    empty.style.color = "var(--ink-faint)";
    empty.textContent = ct.status === "error" ? "Nothing is running." :
      "Nothing has started yet. Add sitebin-container-compose.yaml to the site's root.";
    box.appendChild(empty);
  }
  for (const s of ct.services) {
    const row = document.createElement("div");
    row.className = "ctsvc";
    const head = document.createElement("div");
    head.className = "head";
    const name = document.createElement("span"); name.className = "name"; name.textContent = s.name;
    const img = document.createElement("span"); img.className = "img"; img.textContent = s.image;
    const state = document.createElement("span"); state.className = "state " + (s.state || ""); state.textContent = s.state || "—";
    head.append(name, img, state);
    if (s.egress) {
      const f = document.createElement("span"); f.className = "flag"; f.textContent = "egress";
      head.append(f);
    }
    row.append(head);
    if (s.domains && s.domains.length) {
      const maps = document.createElement("div");
      maps.className = "maps";
      for (const d of s.domains) {
        const line = document.createElement("div");
        if (d.url) {
          const a = document.createElement("a");
          a.href = d.url; a.target = "_blank"; a.rel = "noopener";
          a.textContent = d.url.replace(/\/$/, "");
          line.append(a);
        } else {
          line.append(d.domain);
        }
        line.append(" → :" + d.port);
        const p = pending[d.domain];
        if (d.pending) {
          const w = document.createElement("span");
          w.className = "pending";
          w.textContent = p ? "  pending DNS: TXT " + p.txt_name + " = " + p.txt_value +
            (p.cname_target ? " (or CNAME → " + p.cname_target + ")" : "") : "  not attached";
          line.append(w);
        }
        maps.append(line);
      }
      row.append(maps);
    }
    if (s.volumes && s.volumes.length) {
      const v = document.createElement("div");
      v.className = "vols";
      v.textContent = "volumes: " + s.volumes.join(", ");
      row.append(v);
    }
    box.append(row);
  }

  const sel = $("ct-logsvc");
  const current = sel.value;
  sel.innerHTML = "";
  for (const s of ct.services) {
    const o = document.createElement("option");
    o.value = o.textContent = s.name;
    if (s.name === current) o.selected = true;
    sel.append(o);
  }
  $("ct-logrow").classList.toggle("hidden", !ct.services.length);

  clearTimeout(ctPoll);
  if (ct.enabled && ct.status === "starting") {
    ctPoll = setTimeout(async () => {
      try { site = await api("GET", ""); render(); } catch {}
    }, 4000);
  }
}

async function containerAction(action, okMsg) {
  try {
    site = await api("POST", "/containers/" + action);
    render();
    toast(okMsg);
  } catch (err) { toast(err.message, true); }
}
$("ct-start").addEventListener("click", () => containerAction("start", "Starting…"));
$("ct-restart").addEventListener("click", () => containerAction("restart", "Restarting…"));
$("ct-stop").addEventListener("click", () => containerAction("stop", "Stopped"));
$("ct-logs").addEventListener("click", async () => {
  const svc = $("ct-logsvc").value;
  if (!svc) return;
  try {
    const res = await fetch("/api/sites/" + editID + "/containers/" + encodeURIComponent(svc) + "/logs?tail=200", {
      headers: authHeaders(),
    });
    const text = await res.text();
    if (!res.ok) {
      let m = text;
      try { m = JSON.parse(text).error; } catch {}
      throw new Error(m || "could not read the log");
    }
    const pre = $("ct-log");
    pre.textContent = text || "(no output yet)";
    pre.classList.remove("hidden");
    pre.scrollTop = pre.scrollHeight;
  } catch (err) { toast(err.message, true); }
});

function encodePath(p) {
  return p.split("/").map(encodeURIComponent).join("/");
}
function rawURL(p) {
  const base = site.view_url.replace(/\/$/, "");
  return base + (site.mode === "viewer" ? "/_raw/" : "/") + encodePath(p);
}

// ---- folder browser ----
//
// The Files card shows one folder at a time, from GET /dir: sub-folders
// first, then files, at most DIR_SHOW of them until "Show more". The folder
// is kept in the URL fragment, so the browser's back button and a reload
// work too.

const DIR_SHOW = 15;
let cwd = "";             // the folder shown, "" for the top
let dirEntries = [];      // its entries
let dirTruncated = false; // the server cut a huge folder
let dirLoaded = false;
let dirShowAll = false;

function joinPath(dir, name) { return dir ? dir + "/" + name : name; }
function parentOf(dir) { return dir.includes("/") ? dir.slice(0, dir.lastIndexOf("/")) : ""; }
function dirHash(dir) { return dir ? "#dir=" + encodePath(dir) : ""; }

function dirFromHash() {
  const m = /^#dir=(.*)$/.exec(location.hash);
  if (!m) return "";
  try { return decodeURIComponent(m[1]); } catch { return ""; }
}

async function loadDir(dir) {
  let d;
  try {
    d = await api("GET", "/dir?path=" + encodeURIComponent(dir));
  } catch (err) {
    // The folder is gone (deleted, replaced): show the nearest one that is left.
    if (err.status === 404 && dir) return loadDir(parentOf(dir));
    toast(err.message, true);
    return;
  }
  if (d.path !== cwd) dirShowAll = false;
  cwd = d.path;
  dirEntries = d.entries || [];
  dirTruncated = !!d.truncated;
  dirLoaded = true;
  const hash = dirHash(cwd);
  if (location.hash !== hash) history.replaceState(null, "", location.pathname + location.search + hash);
  renderFiles();
}

function openDir(dir) {
  const hash = dirHash(dir);
  if (location.hash !== hash) history.pushState(null, "", location.pathname + location.search + hash);
  dirShowAll = false;
  loadDir(dir);
}

function refreshDir() { return loadDir(cwd); }

window.addEventListener("popstate", () => { if (site) loadDir(dirFromHash()); });

function renderFiles() {
  const total = site.usage.files;
  $("file-count").textContent = total + " file" + (total === 1 ? "" : "s");
  $("drop-into").textContent = cwd ? " into " + cwd + "/" : "";

  // Back + breadcrumbs: files / app / node_modules
  const crumbs = $("crumbs");
  crumbs.innerHTML = "";
  const back = document.createElement("button");
  back.type = "button";
  back.className = "btn small";
  back.textContent = "\u2190 Back";
  back.disabled = !cwd;
  back.addEventListener("click", () => openDir(parentOf(cwd)));
  const trail = document.createElement("span");
  trail.className = "trail";
  const parts = cwd ? cwd.split("/") : [];
  const crumb = (label, dir, here) => {
    const el = document.createElement(here ? "span" : "button");
    el.textContent = label;
    if (here) {
      el.className = "here";
      el.setAttribute("aria-current", "location");
    } else {
      el.type = "button";
      el.addEventListener("click", () => openDir(dir));
    }
    return el;
  };
  trail.appendChild(crumb("files", "", parts.length === 0));
  parts.forEach((part, i) => {
    const sep = document.createElement("span");
    sep.className = "sep";
    sep.textContent = "/";
    trail.append(sep, crumb(part, parts.slice(0, i + 1).join("/"), i === parts.length - 1));
  });
  crumbs.append(back, trail);

  const rows = $("filerows");
  rows.innerHTML = "";
  const note = (text) => {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 3;
    td.className = "fnote";
    td.textContent = text;
    tr.appendChild(td);
    rows.appendChild(tr);
    return td;
  };
  if (!dirLoaded) { note("Loading\u2026"); return; }
  if (!dirEntries.length) note(cwd ? "This folder is empty." : "No files yet \u2014 drop some below.");
  const shown = dirShowAll ? dirEntries : dirEntries.slice(0, DIR_SHOW);
  for (const en of shown) rows.appendChild(en.dir ? dirRow(en) : fileRow(en));
  if (!dirShowAll && dirEntries.length > DIR_SHOW) {
    const td = note("");
    td.className = "fmore";
    const more = document.createElement("button");
    more.type = "button";
    more.className = "linkbtn";
    more.textContent = "Show " + (dirEntries.length - DIR_SHOW) + " more";
    more.addEventListener("click", () => { dirShowAll = true; renderFiles(); });
    td.appendChild(more);
  }
  if (dirTruncated && (dirShowAll || dirEntries.length <= DIR_SHOW)) {
    note("Only the first " + dirEntries.length + " entries of this folder are listed.");
  }
}

function dirRow(en) {
  const tr = document.createElement("tr");
  tr.className = "dirrow";
  const p = document.createElement("td");
  p.className = "fpath";
  const open = document.createElement("button");
  open.type = "button";
  open.className = "dirlink";
  open.textContent = en.name + "/";
  open.addEventListener("click", () => openDir(joinPath(cwd, en.name)));
  p.appendChild(open);
  const s = document.createElement("td");
  s.className = "fsize";
  s.textContent = "folder";
  const act = document.createElement("td");
  act.className = "fact";
  tr.append(p, s, act);
  return tr;
}

function fileRow(en) {
  const full = joinPath(cwd, en.name);
  const tr = document.createElement("tr");
  const p = document.createElement("td");
  p.className = "fpath";
  if (site.mode === "container") {
    // A container site serves its app, not its files: there is no URL.
    p.textContent = en.name;
  } else {
    const link = document.createElement("a");
    link.href = rawURL(full);
    link.target = "_blank";
    link.rel = "noopener";
    link.textContent = en.name;
    p.appendChild(link);
  }
  const s = document.createElement("td");
  s.className = "fsize";
  s.textContent = fmtBytes(en.size || 0);
  const act = document.createElement("td");
  act.className = "fact";
  if (isEditable(full)) {
    const ed = document.createElement("button");
    ed.className = "btn small";
    ed.textContent = "Edit";
    ed.addEventListener("click", () => openEditor(full));
    act.appendChild(ed);
  }
  const del = document.createElement("button");
  del.className = "btn small danger";
  del.textContent = "Delete";
  del.style.marginLeft = "6px";
  del.addEventListener("click", async () => {
    try {
      site = await api("DELETE", "/files/" + encodePath(full));
      render();
      refreshDir();
      toast("Deleted " + full);
    } catch (err) { toast(err.message, true); }
  });
  act.appendChild(del);
  tr.append(p, s, act);
  return tr;
}

// ---- settings actions ----

async function put(body, okMsg) {
  try {
    site = await api("PUT", "", body);
    refreshDir();
    render();
    if (okMsg) toast(okMsg);
  } catch (err) {
    toast(err.message, true);
    render(); // restore actual state
  }
}

function saveName() {
  const v = $("e-name").value.trim();
  if (v === (site.name || "")) return;
  put({ name: v }, v ? "Named " + v : "Name removed");
}
$("save-name").addEventListener("click", saveName);
$("e-name").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); saveName(); }
});
$("clear-name").addEventListener("click", () => {
  $("e-name").value = "";
  put({ name: "" }, "Name removed");
});

document.querySelectorAll("input[name=emode]").forEach((r) =>
  r.addEventListener("change", () => put({ mode: r.value }, "Mode switched to " + r.value)));

$("entry-file").addEventListener("change", (e) =>
  put({ entry_file: e.target.value }, "Viewer now shows " + e.target.value));

$("set-viewpw").addEventListener("click", () => {
  const v = $("e-viewpw").value;
  if (!v) { toast("Type a password first", true); return; }
  $("e-viewpw").value = "";
  put({ view_password: v }, "View password set");
});
$("clear-viewpw").addEventListener("click", () => put({ view_password: "" }, "Protection removed"));

$("e-webdav").addEventListener("change", (e) =>
  put({ webdav_enabled: e.target.checked }, e.target.checked ? "WebDAV enabled" : "WebDAV disabled"));

$("e-ftp").addEventListener("change", (e) =>
  put({ ftp_enabled: e.target.checked }, e.target.checked ? "FTP enabled" : "FTP disabled"));

$("e-spa").addEventListener("change", (e) =>
  put({ spa_fallback: e.target.checked }, e.target.checked ? "SPA fallback on" : "SPA fallback off"));

$("save-expiry").addEventListener("click", () => {
  const v = $("e-expires").value;
  if (!v) { toast("Pick a date first", true); return; }
  put({ expires_at: new Date(v).toISOString() }, "Expiry saved");
});
$("clear-expiry").addEventListener("click", () => put({ expires_at: null }, "Expiry cleared"));

// ---- uploads ----

async function uploadFiles(files, isZip) {
  const fd = new FormData();
  const replaceAll = $("replace-all").checked;
  // Plain files land in the folder being shown; a zip and a replace-all act on
  // the whole site.
  const into = isZip || replaceAll ? "" : cwd;
  if (isZip) fd.append("zip", files[0], files[0].name);
  else for (const f of files) fd.append("files", f.file || f, joinPath(into, f.path || f.name));
  const replace = replaceAll ? "?replace=true" : "";
  try {
    site = await api("POST", "/files" + replace, fd, true);
    $("replace-all").checked = false;
    render();
    refreshDir();
    toast("Files uploaded");
  } catch (err) { toast(err.message, true); }
}

// ---- in-browser editor ----

const EDITABLE = /\.(html?|css|js|mjs|json|md|markdown|txt|svg|xml|csv|tsv|yaml|yml|toml|ini|log|c|cc|cpp|h|go|py|rb|rs|ts|tsx|jsx|sh|conf)$/i;
function isEditable(path) { return EDITABLE.test(path); }

let editingPath = null;
async function openEditor(path) {
  try {
    const res = await fetch("/api/sites/" + editID + "/content/" + encodePath(path), {
      headers: authHeaders(),
    });
    if (!res.ok) {
      const d = await res.json().catch(() => ({}));
      throw new Error(d.error || "could not open file (" + res.status + ")");
    }
    $("editor-text").value = await res.text();
    $("editor-name").textContent = path;
    editingPath = path;
    $("editor-overlay").classList.remove("hidden");
    $("editor-text").focus();
  } catch (err) { toast(err.message, true); }
}

function closeEditor() {
  $("editor-overlay").classList.add("hidden");
  editingPath = null;
}

$("editor-cancel").addEventListener("click", closeEditor);
$("editor-overlay").addEventListener("click", (e) => { if (e.target.id === "editor-overlay") closeEditor(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !$("editor-overlay").classList.contains("hidden")) closeEditor(); });

$("editor-save").addEventListener("click", async () => {
  if (!editingPath) return;
  const name = editingPath;
  const fd = new FormData();
  fd.append("files", new Blob([$("editor-text").value]), name);
  try {
    site = await api("POST", "/files", fd, true);
    closeEditor();
    render();
    refreshDir();
    toast("Saved " + name);
  } catch (err) { toast(err.message, true); }
});

$("download-zip").addEventListener("click", async () => {
  try {
    const res = await fetch("/api/sites/" + editID + "/download", {
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error("download failed (" + res.status + ")");
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = site.id + ".zip";
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  } catch (err) {
    toast(err.message, true);
  }
});

$("e-pick-files").addEventListener("click", () => $("e-input-files").click());
$("e-pick-folder").addEventListener("click", () => $("e-input-folder").click());
$("e-pick-zip").addEventListener("click", () => $("e-input-zip").click());
$("e-input-files").addEventListener("change", (e) => {
  const fs = Array.from(e.target.files).map((f) => ({ file: f, path: f.webkitRelativePath || f.name }));
  if (fs.length) uploadFiles(fs);
  e.target.value = "";
});
$("e-input-folder").addEventListener("change", (e) => {
  const fs = Array.from(e.target.files).map((f) => ({ file: f, path: f.webkitRelativePath || f.name }));
  if (fs.length) uploadFiles(fs);
  e.target.value = "";
});
$("e-input-zip").addEventListener("change", (e) => {
  if (e.target.files.length) uploadFiles(e.target.files, true);
  e.target.value = "";
});

const mini = $("dropmini");
["dragenter", "dragover"].forEach((ev) =>
  mini.addEventListener(ev, (e) => { e.preventDefault(); mini.classList.add("dragover"); }));
["dragleave", "drop"].forEach((ev) =>
  mini.addEventListener(ev, (e) => { e.preventDefault(); mini.classList.remove("dragover"); }));
mini.addEventListener("drop", (e) => {
  const files = Array.from(e.dataTransfer.files);
  if (!files.length) return;
  if (files.length === 1 && /\.zip$/i.test(files[0].name)) uploadFiles(files, true);
  else uploadFiles(files.map((f) => ({ file: f, path: f.name })));
});

// ---- domains ----

$("e-add-domain").addEventListener("click", async () => {
  const d = $("e-domain").value.trim().toLowerCase();
  if (!d) return;
  try {
    site = await api("POST", "/domains", { domain: d });
    $("e-domain").value = "";
    render();
    if (site.custom_domains.includes(d)) toast("Verified and attached " + d);
    else toast("Claimed " + d + ": create the DNS record shown to verify it");
  } catch (err) { toast(err.message, true); }
});
$("e-domain").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); $("e-add-domain").click(); }
});

// ---- forms ----

let formsData = null;
let editingKey = null;

const FORM_STATUS = {
  pending: ["Awaiting confirmation", "The recipient has an email with a link to confirm. Until then, submissions are refused."],
  active: ["Active", ""],
  stopped: ["Stopped by recipient", "The recipient stopped these emails. Resend the confirmation to ask again."],
  paused: ["Paused — plan limit", "This site's plan allows fewer forms. It resumes when the plan allows it again."],
};

async function loadForms() {
  try {
    formsData = await api("GET", "/forms");
  } catch {
    formsData = null;
  }
  renderForms();
}

function smallButton(label, onClick) {
  const b = document.createElement("button");
  b.className = "btn small";
  b.textContent = label;
  b.addEventListener("click", onClick);
  return b;
}

// armedButton asks for a second click within four seconds before it acts.
function armedButton(label, armedLabel, onConfirm) {
  const b = smallButton(label, async () => {
    if (!b.dataset.armed) {
      b.dataset.armed = "1";
      b.textContent = armedLabel;
      b.classList.add("danger");
      setTimeout(() => {
        delete b.dataset.armed;
        b.textContent = label;
        b.classList.remove("danger");
      }, 4000);
      return;
    }
    await onConfirm();
  });
  return b;
}

function showSnippet(row, snippet) {
  let pre = row.querySelector("pre.snippet");
  if (!pre) {
    pre = document.createElement("pre");
    pre.className = "snippet";
    row.append(pre);
  }
  pre.textContent = snippet;
}

function renderForms() {
  const card = $("card-forms");
  if (!formsData || !formsData.enabled) {
    card.classList.add("hidden");
    return;
  }
  card.classList.remove("hidden");
  $("forms-count").textContent = formsData.used + " of " + formsData.limit;
  $("forms-none").classList.toggle("hidden", formsData.limit > 0 || formsData.forms.length > 0);
  $("form-edit").classList.toggle("hidden", !editingKey && formsData.forms.length >= formsData.limit);
  $("f-files-wrap").classList.toggle("hidden", formsData.max_files === 0);

  const rows = $("formrows");
  rows.innerHTML = "";
  for (const f of formsData.forms) {
    const [label, hint] = FORM_STATUS[f.status] || [f.status, ""];
    const row = document.createElement("div");
    row.className = "formrow";
    const head = document.createElement("div");
    head.className = "formhead";
    const name = document.createElement("span");
    name.className = "fname";
    name.textContent = f.name;
    const tag = document.createElement("span");
    tag.className = "fstatus " + f.status;
    tag.textContent = label;
    const to = document.createElement("span");
    to.className = "fto";
    to.textContent = "→ " + f.recipient;
    head.append(name, tag, to);
    row.append(head);
    if (hint) {
      const p = document.createElement("div");
      p.className = "sub";
      p.textContent = hint;
      row.append(p);
    }
    const actions = document.createElement("div");
    actions.className = "row";
    actions.append(
      smallButton("Copy snippet", async () => {
        try {
          await navigator.clipboard.writeText(f.snippet);
          toast("Snippet copied — paste it into a page");
        } catch {
          showSnippet(row, f.snippet);
        }
      }),
      smallButton("Edit", () => startFormEdit(f)),
    );
    if (f.status === "pending" || f.status === "stopped") {
      actions.append(smallButton("Resend confirmation", async () => {
        try {
          formsData = await api("POST", "/forms/" + f.key + "/confirmation");
          renderForms();
          toast("Confirmation sent to " + f.recipient);
        } catch (err) { toast(err.message, true); }
      }));
    }
    actions.append(armedButton("Delete", "Click again to delete", async () => {
      try {
        await api("DELETE", "/forms/" + f.key);
        if (editingKey === f.key) resetFormEditor();
        await loadForms();
        toast("Deleted " + f.name);
      } catch (err) { toast(err.message, true); }
    }));
    row.append(actions);
    rows.appendChild(row);
  }
}

function startFormEdit(f) {
  editingKey = f.key;
  $("f-name").value = f.name;
  $("f-recipient").value = f.recipient;
  $("f-captcha").checked = f.captcha;
  $("f-files").checked = f.files;
  $("f-redirect").value = f.redirect || "";
  $("f-save").textContent = "Save form";
  $("f-cancel").classList.remove("hidden");
  renderForms();
  $("f-name").focus();
}

function resetFormEditor() {
  editingKey = null;
  for (const id of ["f-name", "f-recipient", "f-redirect"]) $(id).value = "";
  $("f-captcha").checked = false;
  $("f-files").checked = false;
  $("f-save").textContent = "Add form";
  $("f-cancel").classList.add("hidden");
}

$("f-save").addEventListener("click", async () => {
  const body = {
    name: $("f-name").value.trim(),
    recipient: $("f-recipient").value.trim(),
    captcha: $("f-captcha").checked,
    // With attachments off instance-wide the box is hidden, and a stale tick
    // in it would make every save a 400.
    files: formsData.max_files === 0 ? false : $("f-files").checked,
    redirect: $("f-redirect").value.trim(),
  };
  const wasEditing = editingKey;
  try {
    formsData = wasEditing
      ? await api("PUT", "/forms/" + wasEditing, body)
      : await api("POST", "/forms", body);
    resetFormEditor();
    renderForms();
    if (formsData.warnings && formsData.warnings.length) toast(formsData.warnings[0], true);
    else toast(wasEditing ? "Saved" : "Added — " + body.recipient + " has an email to confirm");
  } catch (err) { toast(err.message, true); }
});
$("f-cancel").addEventListener("click", () => { resetFormEditor(); renderForms(); });

// ---- danger ----

let armed = false;
$("delete-site").addEventListener("click", async () => {
  if (!armed) {
    armed = true;
    $("delete-site").textContent = "Click again to delete forever";
    $("delete-hint").textContent = "Really delete " + site.id + "? This cannot be undone.";
    setTimeout(() => {
      armed = false;
      $("delete-site").textContent = "Delete this site";
      $("delete-hint").textContent = "Removes all files, domains and URLs. There is no undo.";
    }, 5000);
    return;
  }
  try {
    await api("DELETE", "");
    sessionStorage.removeItem(pwKey);
    document.body.innerHTML =
      '<div class="lockwrap"><div class="card"><h1>Site deleted</h1>' +
      '<p style="color:var(--ink-dim)">All files and URLs are gone. ' +
      '<a href="/">Publish something new</a>.</p></div></div>';
  } catch (err) { toast(err.message, true); }
});
