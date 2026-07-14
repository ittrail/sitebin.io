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

async function api(method, path, body, isForm) {
  const headers = { "X-Edit-Password": sitePw };
  if (body && !isForm) headers["Content-Type"] = "application/json";
  const res = await fetch("/api/sites/" + editID + path, {
    method,
    headers,
    body: body ? (isForm ? body : JSON.stringify(body)) : undefined,
  });
  let data = {};
  try { data = await res.json(); } catch {}
  if (!res.ok) throw Object.assign(new Error(data.error || res.statusText), { status: res.status });
  return data;
}

// ---- unlock flow ----

$("lockform").addEventListener("submit", async (e) => {
  e.preventDefault();
  sitePw = $("lockpw").value;
  if (!sitePw) return;
  try {
    site = await api("GET", "");
    sessionStorage.setItem(pwKey, sitePw);
    $("lock").classList.add("hidden");
    $("app").classList.remove("hidden");
    render();
  } catch (err) {
    $("lockerr").textContent =
      err.status === 429 ? "Too many attempts — wait a moment." : "Wrong password — check your claim ticket.";
  }
});

(async function boot() {
  if (!sitePw) return;
  try {
    site = await api("GET", "");
    $("lock").classList.add("hidden");
    $("app").classList.remove("hidden");
    render();
  } catch {
    sessionStorage.removeItem(pwKey);
    sitePw = "";
  }
})();

// ---- rendering ----

function render() {
  $("site-id").textContent = site.id;
  $("view-link").href = site.view_url;
  $("site-meta").textContent =
    "created " + new Date(site.created_at).toLocaleString() +
    " · updated " + new Date(site.updated_at).toLocaleString();

  // files
  const rows = $("filerows");
  rows.innerHTML = "";
  $("file-count").textContent = site.files.length + " file" + (site.files.length === 1 ? "" : "s");
  if (!site.files.length) {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 3;
    td.style.color = "var(--ink-faint)";
    td.textContent = "No files yet — drop some below.";
    tr.appendChild(td);
    rows.appendChild(tr);
  }
  for (const f of site.files) {
    const tr = document.createElement("tr");
    const p = document.createElement("td");
    p.className = "fpath";
    const link = document.createElement("a");
    link.href = rawURL(f.path);
    link.target = "_blank";
    link.rel = "noopener";
    link.textContent = f.path;
    p.appendChild(link);
    const s = document.createElement("td");
    s.className = "fsize";
    s.textContent = fmtBytes(f.size);
    const act = document.createElement("td");
    act.className = "fact";
    const del = document.createElement("button");
    del.className = "btn small danger";
    del.textContent = "Delete";
    del.addEventListener("click", async () => {
      try {
        site = await api("DELETE", "/files/" + encodePath(f.path));
        render();
        toast("Deleted " + f.path);
      } catch (err) { toast(err.message, true); }
    });
    act.appendChild(del);
    tr.append(p, s, act);
    rows.appendChild(tr);
  }

  const pct = Math.min(100, (site.usage.bytes / site.usage.max_bytes) * 100);
  $("usage-fill").style.width = pct + "%";
  $("usage-note").textContent =
    fmtBytes(site.usage.bytes) + " of " + fmtBytes(site.usage.max_bytes) +
    " · " + site.usage.files + "/" + site.usage.max_files + " files";

  // mode + entry
  document.querySelector(`input[name=emode][value=${site.mode}]`).checked = true;
  $("entrywrap").classList.toggle("hidden", site.mode !== "viewer");
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

  if (site.expires_at) {
    const d = new Date(site.expires_at);
    $("e-expires").value = new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
    $("expiry-status").textContent = "Expires " + d.toLocaleString();
  } else {
    $("e-expires").value = "";
    $("expiry-status").textContent = "No expiry — the site stays up.";
  }

  // domains
  const dr = $("domainrows");
  dr.innerHTML = "";
  if (!site.custom_domains.length) {
    dr.innerHTML = '<div class="domainrow" style="color:var(--ink-faint)">No custom domains yet.</div>';
  }
  for (const d of site.custom_domains) {
    const row = document.createElement("div");
    row.className = "domainrow";
    const name = document.createElement("span");
    name.className = "d";
    name.textContent = d;
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

function encodePath(p) {
  return p.split("/").map(encodeURIComponent).join("/");
}
function rawURL(p) {
  const base = site.view_url.replace(/\/$/, "");
  return base + (site.mode === "viewer" ? "/_raw/" : "/") + encodePath(p);
}

// ---- settings actions ----

async function put(body, okMsg) {
  try {
    site = await api("PUT", "", body);
    render();
    if (okMsg) toast(okMsg);
  } catch (err) {
    toast(err.message, true);
    render(); // restore actual state
  }
}

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

$("save-expiry").addEventListener("click", () => {
  const v = $("e-expires").value;
  if (!v) { toast("Pick a date first", true); return; }
  put({ expires_at: new Date(v).toISOString() }, "Expiry saved");
});
$("clear-expiry").addEventListener("click", () => put({ expires_at: null }, "Expiry cleared"));

// ---- uploads ----

async function uploadFiles(files, isZip) {
  const fd = new FormData();
  if (isZip) fd.append("zip", files[0], files[0].name);
  else for (const f of files) fd.append("files", f.file || f, f.path || f.name);
  const replace = $("replace-all").checked ? "?replace=true" : "";
  try {
    site = await api("POST", "/files" + replace, fd, true);
    $("replace-all").checked = false;
    render();
    toast("Files uploaded");
  } catch (err) { toast(err.message, true); }
}

$("download-zip").addEventListener("click", async () => {
  try {
    const res = await fetch("/api/sites/" + editID + "/download", {
      headers: { "X-Edit-Password": sitePw },
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
    toast("Added " + d + " — set the DNS record to go live");
  } catch (err) { toast(err.message, true); }
});
$("e-domain").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); $("e-add-domain").click(); }
});

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
