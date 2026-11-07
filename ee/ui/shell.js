// Sitebin workspace — the shell every workspace page shares (ELv2): the
// sidebar, Ctrl+K, dialogs, menus, the toast, and the two ways to talk to the
// server. It loads on the account page (app.js), on the site page (site.js)
// and on every server-rendered page shown inside the workspace frame.
//
// DISPLAY SAFETY: everything from the server is text. It reaches the DOM only
// through esc() inside templates or through textContent.
"use strict";

const WS = (() => {
  const ICONS = {
    search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
    sites: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M7 6.5h.01M10 6.5h.01"/>',
    share: '<circle cx="9" cy="8" r="3.5"/><path d="M3 20c0-3.3 2.7-6 6-6s6 2.7 6 6"/><path d="M16 4.5a3.5 3.5 0 0 1 0 7M18 14c2 .8 3 2.8 3 6"/>',
    inbox: '<path d="M3 13h5l1.5 3h5L16 13h5"/><path d="M5 5h14l2 8v6H3v-6z"/>',
    globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.8 3.5 5.8 3.5 9s-1 6.2-3.5 9c-2.5-2.8-3.5-5.8-3.5-9s1-6.2 3.5-9"/>',
    key: '<circle cx="8" cy="15" r="4"/><path d="m11 12 9-9M16 7l3 3M14 9l2 2"/>',
    gauge: '<path d="M4 18a8 8 0 1 1 16 0"/><path d="m12 18 4-6"/>',
    user: '<circle cx="12" cy="8" r="4"/><path d="M4 21c0-4 3.6-7 8-7s8 3 8 7"/>',
    radar: '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4.5"/><path d="M12 12 18 6"/>',
    list: '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
    flag: '<path d="M5 21V4M5 4h11l-2 4 2 4H5"/>',
    box: '<path d="m12 3 8 4.5v9L12 21l-8-4.5v-9z"/><path d="m4 7.5 8 4.5 8-4.5M12 12v9"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    dots: '<circle cx="5" cy="12" r="1.2"/><circle cx="12" cy="12" r="1.2"/><circle cx="19" cy="12" r="1.2"/>',
    x: '<path d="M6 6l12 12M18 6 6 18"/>',
    copy: '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h3"/>',
    ext: '<path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>',
    upload: '<path d="M12 16V4M7 9l5-5 5 5M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3"/>',
    edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/>',
    tag: '<path d="M3 12V4h8l10 10-8 8z"/><circle cx="7.5" cy="8.5" r="1.2"/>',
    clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
    lock: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
    check: '<path d="m5 12 4.5 4.5L19 7"/>',
    alert: '<path d="M12 3 2 20h20z"/><path d="M12 10v4M12 17h.01"/>',
    dot: '<circle cx="12" cy="12" r="4.5" fill="currentColor" stroke="none"/>',
    ring: '<circle cx="12" cy="12" r="6" stroke-dasharray="3 3"/>',
    stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>',
    pause: '<path d="M9 6v12M15 6v12"/>',
    arrow: '<path d="M5 12h14M13 6l6 6-6 6"/>',
    arrowl: '<path d="M19 12H5M11 6l-6 6 6 6"/>',
    chevl: '<path d="m15 6-6 6 6 6"/>',
    chevr: '<path d="m9 6 6 6-6 6"/>',
    chevd: '<path d="m6 9 6 6 6-6"/>',
    git: '<circle cx="6" cy="6" r="2.2"/><circle cx="6" cy="18" r="2.2"/><circle cx="18" cy="9" r="2.2"/><path d="M6 8.2v7.6M18 11.2c0 3-3 3.8-9.8 5.6"/>',
    mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>',
    chart: '<path d="M4 20V10M10 20V4M16 20v-7M22 20H2"/>',
    files: '<path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z"/><path d="M14 3v5h5"/>',
    folder: '<path d="M3 6a1 1 0 0 1 1-1h5l2 2h9a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1z"/>',
    shield: '<path d="M12 3 4 6v6c0 4.5 3.4 8 8 9 4.6-1 8-4.5 8-9V6z"/>',
    gear: '<circle cx="12" cy="12" r="3"/><path d="M12 2v3M12 19v3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M2 12h3M19 12h3M4.9 19.1 7 17M17 7l2.1-2.1"/>',
    swap: '<path d="M4 8h14l-3-3M20 16H6l3 3"/>',
    trash: '<path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/>',
    refresh: '<path d="M20 11a8 8 0 0 0-14.5-4.5L4 8M4 4v4h4M4 13a8 8 0 0 0 14.5 4.5L20 16M20 20v-4h-4"/>',
    download: '<path d="M12 4v12M7 11l5 5 5-5M4 20h16"/>',
    menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
    hold: '<path d="M12 3v18M5 7h14M5 7l-3 7h6zM19 7l-3 7h6zM8 21h8"/>',
    layout: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/>',
    out: '<path d="M15 4h4a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1h-4M10 17l5-5-5-5M15 12H4"/>',
  };
  const ic = (n, cls) => '<svg class="i' + (cls ? " " + cls : "") + '" viewBox="0 0 24 24" aria-hidden="true">' + (ICONS[n] || "") + "</svg>";
  const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const $ = (sel, root) => (root || document).querySelector(sel);
  const MON = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  const DAY = 864e5;
  const toDate = (s) => (s instanceof Date ? s : new Date(s));
  function fmtDate(s, year) {
    if (!s) return "";
    const d = toDate(s), now = new Date();
    return d.getDate() + " " + MON[d.getMonth()] + (year || d.getFullYear() !== now.getFullYear() ? " " + d.getFullYear() : "");
  }
  function ago(s) {
    if (!s) return "";
    const d = toDate(s);
    if (isNaN(d) || d.getFullYear() < 2000) return "";
    const m = Math.round((Date.now() - d) / 6e4);
    if (m < 1) return "just now";
    if (m < 60) return m + " min ago";
    const h = Math.round(m / 60);
    if (h < 24) return h + " h ago";
    const dd = Math.round(h / 24);
    return dd === 1 ? "yesterday" : dd < 14 ? dd + " days ago" : fmtDate(d);
  }
  const daysTo = (s) => Math.ceil((toDate(s) - Date.now()) / DAY);
  function fmtBytes(n) {
    n = Number(n) || 0;
    if (n < 1024) return n + " B";
    const u = ["KB", "MB", "GB", "TB"];
    let i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
    return n.toFixed(n >= 10 ? 0 : 1) + " " + u[i];
  }
  const TYPE = { webserver: "Static", viewer: "File viewer", container: "Container" };
  // A site's type as the mockup names it: the web server splits into Static
  // and Single-page app. Short in lists and the ticket, long elsewhere.
  const typeKey = (s) => s.mode === "webserver" ? (s.spa || s.spa_fallback ? "spa" : "static") : s.mode;
  const TYPE_SHORT = { static: "Static", spa: "SPA", viewer: "Viewer", container: "Container" };
  const TYPE_LONG = { static: "Static", spa: "Single-page app", viewer: "File viewer", container: "Container" };
  const typeShort = (s) => TYPE_SHORT[typeKey(s)] || s.mode || "";
  const typeLong = (s) => TYPE_LONG[typeKey(s)] || s.mode || "";
  const editIDOf = (url) => (url || "").split("/").filter(Boolean).pop() || "";
  const pathOf = (url) => { try { return new URL(url, location.href).pathname; } catch { return url || ""; } };

  // ---- state ----
  let state = null;
  async function load() {
    const res = await fetch("/account/api/state", { headers: { Accept: "application/json" }, cache: "no-store" });
    if (res.status === 401) { location.href = "/account/login"; throw new Error("signed out"); }
    if (!res.ok) throw new Error("The account could not be read (" + res.status + ").");
    state = await res.json();
    return state;
  }

  // statusOf is a site row's status, for the table and the ticket.
  function statusOf(s) {
    if (s.locked) return { cls: "s-bad", icon: "lock", label: "Locked", stamp: "Locked" };
    if (s.role === "admin" && s.shared_paused) return { cls: "s-off", icon: "pause", label: "Paused", stamp: "Share paused" };
    if (s.container && s.mode === "container") {
      const st = s.container.status || "";
      if (!s.container.enabled) return { cls: "s-off", icon: "stop", label: "Stopped", stamp: "Stopped" };
      if (st && st !== "running") {
        const bad = st === "error" || st === "failed";
        return { cls: bad ? "s-bad" : "s-warn", icon: bad ? "alert" : "clock", label: st[0].toUpperCase() + st.slice(1), stamp: st };
      }
    }
    if ((s.domains || []).some((d) => d.Pending)) return { cls: "s-warn", icon: "ring", label: "Needs DNS", stamp: "Needs DNS" };
    if (s.expires_at && daysTo(s.expires_at) <= 7) {
      const n = Math.max(0, daysTo(s.expires_at));
      return { cls: "s-warn", icon: "clock", label: "Expires in " + n + " d", stamp: "Expires in " + n + (n === 1 ? " day" : " days") };
    }
    return { cls: "s-ok", icon: "dot", label: "Live", stamp: "Live" };
  }
  const primaryAddr = (s) => {
    const d = (s.domains || []).find((x) => !x.Pending && !x.RedirectTo);
    return d ? d.Domain : (s.view_url || "").replace(/^https?:\/\//, "").replace(/\/$/, "");
  };
  const primaryURL = (s) => {
    const d = (s.domains || []).find((x) => !x.Pending && !x.RedirectTo);
    return d ? d.URL : s.view_url;
  };

  // ---- talking to the server ----

  // post sends a form to one of the account routes the classic pages use,
  // asking for a JSON answer (uiHeader). A message (a secret shown once, a
  // refusal) is shown in a dialog; a redirect is returned; a handoff to
  // another origin is followed.
  async function post(url, fields, opts) {
    const body = new URLSearchParams();
    body.set("csrf", state ? state.account.csrf : "");
    for (const [k, v] of Object.entries(fields || {})) {
      if (Array.isArray(v)) v.forEach((x) => body.append(k, x));
      else if (v !== undefined && v !== null) body.set(k, v);
    }
    const res = await fetch(url, { method: "POST", body, headers: { "X-Sitebin-UI": "json", Accept: "application/json" } });
    const ct = res.headers.get("Content-Type") || "";
    if (ct.includes("application/json")) {
      const a = await res.json();
      if (a.leave) { location.href = a.leave; return a; }
      if (a.redirect && a.redirect.startsWith("/account/login")) { location.href = a.redirect; return a; }
      if (a.message) {
        await dialog({ title: a.message.title, body: a.message.body, secret: a.message.detail, err: !a.ok && !(opts && opts.quiet) });
        if (!a.ok) throw Object.assign(new Error(a.message.title), { shown: true });
      }
      if (!res.ok) throw new Error(a.error || res.statusText);
      return a;
    }
    const text = (await res.text()).trim();
    if (ct.includes("text/html")) {
      // A page the route renders itself (a refusal with its reasons): its
      // heading and text, read as text, in a dialog.
      const doc = new DOMParser().parseFromString(text, "text/html");
      const main = doc.querySelector("main") || doc.body;
      const title = (main.querySelector("h1,h2,h3") || {}).textContent || (res.ok ? "Done" : "Not possible");
      const body = Array.from(main.querySelectorAll("p,li,.inline-status")).map((el) => el.textContent.trim()).filter(Boolean).join(" ").slice(0, 600);
      await dialog({ title: title.trim(), body, err: !res.ok });
      if (!res.ok) throw Object.assign(new Error(title), { shown: true });
      return { ok: true, page: true };
    }
    if (!res.ok) throw new Error(text && text.length < 300 ? text : "The server refused (" + res.status + ").");
    return { ok: true };
  }

  // submit posts a real form: for the multi-step flows (transfer, account
  // deletion, checkout) whose next step is a page of its own.
  function submit(url, fields) {
    const f = document.createElement("form");
    f.method = "post";
    f.action = url;
    const add = (k, v) => { const i = document.createElement("input"); i.type = "hidden"; i.name = k; i.value = v; f.appendChild(i); };
    add("csrf", state ? state.account.csrf : "");
    for (const [k, v] of Object.entries(fields || {})) (Array.isArray(v) ? v : [v]).forEach((x) => add(k, x));
    document.body.appendChild(f);
    f.submit();
  }

  // siteAPI calls the site API the edit page uses, on the browser's session.
  async function siteAPI(editID, method, path, body) {
    const headers = { "X-Sitebin-Session": "1" };
    if (body) headers["Content-Type"] = "application/json";
    const res = await fetch("/api/sites/" + editID + path, { method, headers, body: body ? JSON.stringify(body) : undefined });
    let data = {};
    try { data = await res.json(); } catch {}
    if (!res.ok) throw Object.assign(new Error(data.error || res.statusText), { status: res.status });
    return data;
  }

  async function download(editID, name) {
    const res = await fetch("/api/sites/" + editID + "/download", { headers: { "X-Sitebin-Session": "1" } });
    if (!res.ok) throw new Error("The download was refused (" + res.status + ").");
    const blob = await res.blob();
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = (name || editID) + ".zip";
    document.body.appendChild(a);
    a.click();
    setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
  }

  // ---- toast, dialogs, menus ----
  let toastTimer;
  function toast(msg, isErr) {
    let el = document.getElementById("toast");
    if (!el) { el = document.createElement("div"); el.id = "toast"; el.setAttribute("role", "status"); document.body.appendChild(el); }
    el.textContent = msg;
    el.classList.toggle("err", !!isErr);
    el.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.remove("show"), 3400);
  }
  function fail(err) { if (!err || !err.shown) toast(err && err.message ? err.message : String(err), true); }

  let scrim;
  function overlay() {
    if (!scrim) {
      scrim = document.createElement("div");
      scrim.className = "scrim";
      document.body.appendChild(scrim);
      scrim.addEventListener("mousedown", (e) => { if (e.target === scrim && scrim._close) scrim._close(null); });
    }
    return scrim;
  }
  // dialog shows a message, a secret shown once, or a small form; it resolves
  // with the chosen action's value (and the form's fields), or null.
  function dialog(o) {
    return new Promise((resolve) => {
      const sc = overlay();
      const acts = o.actions || [{ label: o.secret ? "I have stored it" : "OK", value: true, primary: true }];
      sc.innerHTML = '<div class="dlg" role="dialog" aria-modal="true" aria-labelledby="dlg-t"><h2 id="dlg-t"' + (o.err ? ' style="color:var(--danger)"' : "") + ">" + esc(o.title || "") + "</h2>" +
        (o.body ? "<p>" + esc(o.body) + "</p>" : "") + (o.html || "") +
        (o.secret ? '<code class="secret" id="dlg-secret">' + esc(o.secret) + '</code>' : "") +
        '<div class="acts">' + (o.secret && navigator.clipboard ? '<button class="btn" data-copy>' + ic("copy") + "Copy</button>" : "") +
        acts.map((a, i) => '<button class="btn' + (a.primary ? " primary" : "") + (a.danger ? " solid-danger" : "") + '" data-i="' + i + '">' + esc(a.label) + "</button>").join("") + "</div></div>";
      sc.classList.add("on");
      const box = sc.firstChild;
      const close = (v) => { sc.classList.remove("on"); sc.innerHTML = ""; sc._close = null; resolve(v); };
      sc._close = close;
      box.addEventListener("click", (e) => {
        const c = e.target.closest("[data-copy]");
        if (c) { navigator.clipboard.writeText(o.secret).then(() => { c.textContent = "Copied"; }); return; }
        const b = e.target.closest("[data-i]");
        if (!b) return;
        const a = acts[+b.dataset.i];
        if (a.value === null || a.value === undefined) return close(null);
        const vals = {};
        box.querySelectorAll("[name]").forEach((el) => { vals[el.name] = el.type === "checkbox" ? el.checked : el.value; });
        close(o.html ? { value: a.value, fields: vals } : a.value);
      });
      box.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && e.target.tagName === "INPUT") { e.preventDefault(); box.querySelector(".btn.primary, .btn.solid-danger")?.click(); }
      });
      (box.querySelector("input,select,textarea") || box.querySelector(".btn.primary") || box.querySelector("button")).focus();
    });
  }
  function confirm(title, body, label, danger) {
    return dialog({ title, body, actions: [{ label: "Cancel", value: null }, { label, value: true, primary: !danger, danger }] }).then((v) => v === true);
  }
  function closeOverlay() { if (scrim && scrim._close) { scrim._close(null); return true; } return false; }

  function closeMenu() { document.querySelectorAll(".menu").forEach((m) => m.remove()); }
  // menu opens a popover of actions under btn. An item is {label, icon, run}
  // or {label, icon, href}, {sep: true}; danger colours it.
  function menu(btn, items) {
    const open = document.querySelector(".menu");
    closeMenu();
    if (open && open._for === btn) return;
    const m = document.createElement("div");
    m.className = "menu";
    m.setAttribute("role", "menu");
    m._for = btn;
    for (const it of items.filter(Boolean)) {
      if (it.sep) { m.appendChild(document.createElement("hr")); continue; }
      const el = document.createElement(it.href ? "a" : "button");
      if (it.href) { el.href = it.href; if (it.blank) { el.target = "_blank"; el.rel = "noopener"; } } else el.type = "button";
      if (it.danger) el.className = "dz";
      el.innerHTML = ic(it.icon || "dot") + esc(it.label);
      el.setAttribute("role", "menuitem");
      if (it.run) el.addEventListener("click", (e) => { e.preventDefault(); closeMenu(); it.run(); });
      m.appendChild(el);
    }
    document.body.appendChild(m);
    const r = btn.getBoundingClientRect(), h = m.offsetHeight;
    m.style.left = Math.max(8, Math.min(r.right - m.offsetWidth, innerWidth - m.offsetWidth - 8)) + "px";
    m.style.top = (r.bottom + h + 8 > innerHeight ? Math.max(8, r.top - h - 6) : r.bottom + 6) + "px";
    m.querySelector("button,a")?.focus();
  }
  document.addEventListener("mousedown", (e) => { if (!e.target.closest(".menu")) { const m = document.querySelector(".menu"); if (m && !m._for.contains(e.target)) closeMenu(); } });

  // ---- the sidebar ----
  const onApp = () => document.body.classList.contains("ws-app");
  const link = (hash) => (onApp() ? hash : "/account" + hash);
  const tagKeyColor = (t) => "tag-" + (t.color || "grey");

  function renderSide(active) {
    const side = document.getElementById("side");
    if (!side || !state) return;
    active = active || {};
    const a = state.account, sites = state.sites;
    const nav = (view, icon, label, extra, hash) => '<a class="nav-i' + (active.view === view ? " on" : "") + '" href="' + link(hash || "#/" + view) + '">' + ic(icon) + '<span class="l">' + esc(label) + "</span>" + (extra || "") + "</a>";
    const n = (x) => '<span class="n">' + x + "</span>";
    const inbox = (state.takeovers || []).length + (state.invites || []).length;
    let tags = "";
    const byGroup = {};
    for (const t of state.tags) (byGroup[t.group || ""] ||= []).push(t);
    for (const g of state.groups) {
      const list = byGroup[g.id];
      if (!list || !list.length) continue;
      tags += '<div class="nav-g">' + esc(g.name) + "</div>" + list.map(tagLink).join("");
    }
    const loose = (byGroup[""] || []).concat(Object.keys(byGroup).filter((k) => k && !state.groups.some((g) => g.id === k)).flatMap((k) => byGroup[k]));
    if (loose.length && state.groups.length) tags += '<div class="nav-g">No group</div>';
    tags += loose.map(tagLink).join("");
    function tagLink(t) {
      return '<a class="nav-i nsub ' + tagKeyColor(t) + (active.tag === t.key ? " on" : "") + '" href="' + link("#/tag/" + encodeURIComponent(t.key)) + '"><span class="tdot"></span><span class="l">' + esc(t.name) + "</span>" + n(t.count) + "</a>";
    }
    const untagged = sites.filter((s) => !s.tags.length).length;
    const admin = a.is_admin;
    side.innerHTML =
      '<a class="wordmark" href="' + link("#/sites") + '"><svg viewBox="0 0 64 64" aria-hidden="true"><rect x="4" y="4" width="56" height="56" rx="14" fill="#101725" stroke="#2A3550" stroke-width="2"/><rect x="16" y="16" width="32" height="6" rx="3" fill="#F5B84D"/><path d="M32 30v14M25 38l7 7 7-7" stroke="#5B8CFF" stroke-width="4.5" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>sitebin</a>' +
      '<a class="who" href="' + link("#/account") + '" style="text-decoration:none"><span class="av">' + esc((a.email || "?")[0]) + '</span><div style="min-width:0"><div class="e">' + esc(a.email) + '</div><div class="p">' + esc(a.tier ? a.tier + " plan" : "") + "</div></div></a>" +
      '<button class="jump" type="button" data-ws-palette>' + ic("search") + "Search or jump<kbd>Ctrl K</kbd></button>" +
      nav("sites", "sites", "All sites", n(sites.length)) +
      nav("shared", "share", "Shared with me", n(sites.filter((s) => s.role === "admin").length)) +
      nav("inbox", "inbox", "Inbox", inbox ? '<span class="badge">' + inbox + "</span>" : "") +
      '<div class="navh">Tags<a href="/account/tags">Manage</a></div>' + tags +
      '<a class="nav-i nsub' + (active.view === "untagged" ? " on" : "") + '" href="' + link("#/untagged") + '"><span class="tdot none"></span><span class="l">Untagged</span>' + n(untagged) + "</a>" +
      '<div class="navh">Account</div>' +
      nav("domains", "globe", "Domains & zones", n(sites.reduce((c, s) => c + (s.role === "owner" ? (s.domains || []).length : 0), 0))) +
      nav("tokens", "key", "API tokens & agents", n(state.tokens.length)) +
      nav("plan", "gauge", "Plan & usage") +
      nav("account", "user", "Account") +
      (admin ? '<div class="navh">Instance<span class="stamp s-warn">Operator</span></div>' +
        nav("op", "radar", "Overview", '<span class="badge red hidden" id="ws-op-badge"></span>') +
        nav("register", "list", "Register", '<span class="n" id="ws-op-n-register"></span>') + nav("reports", "flag", "Reports", '<span class="n" id="ws-op-n-reports"></span>') + (a.containers ? nav("containers", "box", "Containers", '<span class="n" id="ws-op-n-containers"></span>') : "") : "") +
      '<div class="spacer"></div>' +
      '<a class="btn primary publish" href="' + esc(a.publish_url || "/") + '">' + ic("plus") + "Publish a new site</a>" +
      '<button class="btn ghost uiswitch" type="button" data-ws-classic title="Your choice is kept for this account; switch back here at any time.">' + ic("layout") + "Classic interface</button>";
  }

  // ---- Ctrl+K ----
  let pal, pItems = [], pSel = 0;
  const VIEWS = [["Sites", "sites", "#/sites"], ["Shared with me", "share", "#/shared"], ["Inbox", "inbox", "#/inbox"], ["Domains & zones", "globe", "#/domains"],
    ["API tokens & agents", "key", "#/tokens"], ["Plan & usage", "gauge", "#/plan"], ["Account", "user", "#/account"], ["Manage tags", "tag", "/account/tags"]];
  const OPVIEWS = [["Instance overview", "radar", "#/op"], ["Register", "list", "#/register"], ["Abuse reports", "flag", "#/reports"], ["Containers", "box", "#/containers"]];
  function paletteOpen() {
    if (!state) return;
    if (!pal) {
      pal = document.createElement("div");
      pal.className = "scrim";
      pal.innerHTML = '<div class="palette" role="dialog" aria-label="Search and jump"><div class="q">' + ic("search") + '<input placeholder="Search sites, domains, views" autocomplete="off" aria-label="Search"><kbd>Esc</kbd></div><div class="res"></div></div>';
      document.body.appendChild(pal);
      pal.addEventListener("mousedown", (e) => { if (e.target === pal) paletteClose(); });
      const inp = pal.querySelector("input");
      inp.addEventListener("input", () => { pSel = 0; paletteRender(); });
      inp.addEventListener("keydown", (e) => {
        if (e.key === "Escape") { e.preventDefault(); paletteClose(); }
        else if (e.key === "ArrowDown") { e.preventDefault(); pSel = Math.min(pSel + 1, pItems.length - 1); paletteRender(); }
        else if (e.key === "ArrowUp") { e.preventDefault(); pSel = Math.max(pSel - 1, 0); paletteRender(); }
        else if (e.key === "Enter") { e.preventDefault(); paletteRun(pSel); }
      });
      pal.querySelector(".res").addEventListener("click", (e) => { const o = e.target.closest(".opt"); if (o) paletteRun(+o.dataset.n); });
    }
    pal.classList.add("on");
    const inp = pal.querySelector("input");
    inp.value = "";
    pSel = 0;
    paletteRender();
    inp.focus();
  }
  function paletteClose() { if (pal) pal.classList.remove("on"); }
  function paletteRender() {
    const q = pal.querySelector("input").value.toLowerCase().trim();
    const hay = (s) => (s.name + " " + s.view_url + " " + (s.domains || []).map((d) => d.Domain).join(" ") + " " + s.tags.map((t) => t.name).join(" ")).toLowerCase();
    const sites = state.sites.filter((s) => !q || hay(s).includes(q)).slice(0, 8);
    const doms = q ? state.sites.flatMap((s) => (s.domains || []).map((d) => [d.Domain, s])).filter(([d]) => d.includes(q)).slice(0, 5) : [];
    const views = VIEWS.concat(state.account.is_admin ? OPVIEWS : []).filter(([l]) => !q || l.toLowerCase().includes(q));
    pItems = sites.map((s) => ["Sites", s.name || "Untitled drop", primaryAddr(s), "sites", () => openSite(s)])
      .concat(doms.map(([d, s]) => ["Domains", d, s.name || primaryAddr(s), "globe", () => openSite(s, "addresses")]))
      .concat(views.map(([l, i, h]) => ["Go to", l, "", i, () => { location.href = h.startsWith("#") ? link(h) : h; }]));
    pSel = Math.min(pSel, Math.max(0, pItems.length - 1));
    let last = "";
    pal.querySelector(".res").innerHTML = pItems.length ? pItems.map(([g, l, sub, i], k) => (g !== last ? '<div class="gh">' + esc(last = g) + "</div>" : "") +
      '<div class="opt' + (k === pSel ? " on" : "") + '" data-n="' + k + '">' + ic(i) + esc(l) + '<span class="sub">' + esc(sub) + "</span></div>").join("") :
      '<div class="empty" style="padding:22px">Nothing found for “' + esc(q) + "”.</div>";
    pal.querySelector(".opt.on")?.scrollIntoView({ block: "nearest" });
  }
  function paletteRun(k) { const it = pItems[k]; if (!it) return; paletteClose(); it[4](); }

  // openSite goes to a site's own page. A share the owner's plan has paused
  // has no page now; the reason is told instead.
  function openSite(s, tab) {
    if (!s.edit_url) { toast(s.shared_note || "This site cannot be opened right now.", true); return; }
    location.href = pathOf(s.edit_url) + (tab ? "#" + tab : "");
  }

  // The list a site page steps through with J/K: the order the list showed.
  function saveOrder(ids, label, hash) {
    try { sessionStorage.setItem("ws-order", JSON.stringify({ ids, label, hash })); } catch {}
  }
  function order() {
    try { return JSON.parse(sessionStorage.getItem("ws-order") || "null"); } catch { return null; }
  }

  // ---- global keys and clicks ----
  document.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") { e.preventDefault(); paletteOpen(); return; }
    if (e.key === "Escape") {
      if (pal && pal.classList.contains("on")) { paletteClose(); e.stopImmediatePropagation(); return; }
      if (closeOverlay()) { e.stopImmediatePropagation(); return; }
      if (document.querySelector(".menu")) { closeMenu(); e.stopImmediatePropagation(); }
    }
  }, true);
  document.addEventListener("click", (e) => {
    if (e.target.closest("[data-ws-palette]")) { paletteOpen(); return; }
    if (e.target.closest("[data-ws-classic]")) { setUI("classic"); return; }
    if (e.target.closest("[data-ws-burger]")) { document.querySelector(".app")?.classList.toggle("side-open"); return; }
    const nav = e.target.closest(".side a");
    if (nav) document.querySelector(".app")?.classList.remove("side-open");
  });

  async function setUI(ui) {
    try { await post("/account/ui", { ui }); location.href = "/account"; } catch (err) { fail(err); }
  }

  // A framed server page (transfer, tags, the console's trail) gets the
  // sidebar too; which entry it belongs to follows from its path.
  if (document.body.classList.contains("ws-frame")) {
    const p = location.pathname;
    const active = p.startsWith("/account/admin/containers") ? "containers" : p.startsWith("/account/admin/reports") ? "reports" :
      p.startsWith("/account/admin") ? "register" : p.startsWith("/account/tags") ? "tags" : p.startsWith("/account/adopt") ? "inbox" : "sites";
    load().then(() => renderSide({ view: active })).catch(() => {});
    const view = document.getElementById("view");
    if (view) view.insertAdjacentHTML("afterbegin", '<button class="btn ghost icon burger" data-ws-burger aria-label="Menu" style="margin-bottom:8px">' + ic("menu") + "</button>");
  }

  return {
    ic, esc, $, fmtDate, ago, daysTo, fmtBytes, TYPE, typeKey, typeShort, typeLong, editIDOf, pathOf, DAY,
    load, get state() { return state; }, statusOf, primaryAddr, primaryURL,
    post, submit, siteAPI, download, toast, fail, dialog, confirm, closeOverlay, menu, closeMenu,
    renderSide, link, paletteOpen, openSite, saveOrder, order, setUI,
  };
})();
