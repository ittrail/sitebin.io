// Sitebin workspace — the site page (ELv2). The core's edit.js runs this
// page: it reads the site, renders every control into the ids site.html
// carries and owns every rule. This file adds the workspace around it — the
// ticket header, the tab bar, Overview, People and Activity, J/K and Esc —
// and reads edit.js's globals (site, tabs, api, cwd, formsData, render) only.
"use strict";

(() => {
  const { ic, esc, fmtDate, ago, daysTo, fmtBytes, post, toast, fail, confirm, menu } = WS;
  const el = (id) => document.getElementById(id);
  const WD = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
  const LIVE = ["overview", "files", "addresses", "forms", "containers", "people", "activity", "settings"];
  let pane = "overview", me = null, activity = null, chart = null, analyticsState = null, formsLoaded = false, week = null;
  // The four site types of the mockup are edit.js's mode plus its SPA switch.
  const TYPES = [
    ["static", "Static", "Serves your files as they are.", { mode: "webserver", spa_fallback: false }],
    ["spa", "Single-page app", "Unknown paths get index.html.", { mode: "webserver", spa_fallback: true }],
    ["viewer", "File viewer", "Shows the files with previews and a file list.", { mode: "viewer" }],
    ["container", "Container", "Runs the project its compose file declares.", { mode: "container" }],
  ];
  const typeOf = WS.typeKey;
  const CACHE = { auto: "Automatic", revalidate: "Always check", hour: "An hour", day: "A day", week: "A week", year: "A year", off: "Off" };

  // ---- the shell: sidebar, and this site's row in the account state ----
  WS.load().then(() => {
    WS.renderSide({ view: "sites" });
    if (site) { me = WS.state.sites.find((s) => s.id === site.id) || null; paint(); }
  }).catch(() => {});

  // ---- hooks into edit.js ----
  const editRender = window.render;
  window.render = function () { editRender(); paint(); };
  const tabsShow = tabs.show.bind(tabs);
  tabs.show = function (id, opts) { tabsShow(id, opts); bar(); };
  const editRenderForms = window.renderForms;
  window.renderForms = function () { editRenderForms(); formsLoaded = true; formsOff(); if (site) { bar(); if (pane === "overview" && (!tabs.active || tabs.active === "live")) overview(); } };
  // edit.js keeps the shown folder in the URL; the workspace keeps its tab
  // there too, so a reload opens the same tab. The folder's hash is edit.js's
  // only on Files.
  const editSyncHash = window.syncHash;
  window.syncHash = function () {
    if (tabs.active !== "live") return;
    if (pane === "files" && typeof cwd === "string" && cwd) { editSyncHash(); return; }
    const want = pane === "overview" ? "" : "#" + pane;
    if (location.hash !== want) history.replaceState(null, "", location.pathname + location.search + want);
  };
  const tabsRegister = tabs.register.bind(tabs);
  tabs.register = function (t) { tabsRegister(t); bar(); };
  window.addEventListener("popstate", () => { if (site) { pane = paneFromHash(); showPane(); } });

  function paneFromHash() {
    const h = location.hash.replace(/^#/, "");
    if (h.startsWith("dir=")) return "files";
    const first = h.split(/[\/?]/)[0];
    return LIVE.includes(first) ? first : "overview";
  }
  pane = paneFromHash();

  // ---- painting ----
  function paint() {
    if (!site) return;
    loadChart();
    header();
    crumbs();
    settings();
    bar();
    showPane();
  }

  // ---- Settings: the parts the workspace arranges itself ----
  function settings() {
    const s = site, cur = typeOf(s);
    colourTagEditor();
    // The date the owner sees; edit.js keeps date and time in #e-expires.
    if (document.activeElement !== el("ws-expires")) el("ws-expires").value = el("e-expires").value.slice(0, 10);
    const ctOK = !el("mode-container").classList.contains("hidden");
    el("ws-types").innerHTML = TYPES.filter(([k]) => k !== "container" || ctOK || cur === "container").map(([k, l, d]) =>
      '<button type="button" role="radio" class="tcard' + (cur === k ? " on" : "") + '" aria-checked="' + (cur === k) + '" data-type="' + k + '"><b>' + esc(l) + "</b><span>" + esc(d) + "</span></button>").join("");
    el("ws-notfoundcard").classList.toggle("hidden", el("notfoundwrap").classList.contains("hidden"));
    const admin = s.role === "admin";
    el("ws-expiry-ro").classList.toggle("hidden", !admin);
    if (admin) el("ws-expiry-ro").textContent = (s.expires_at ? fmtDate(s.expires_at, true) : "Never") + ". Only the owner can change it.";
  }
  function setType(k) {
    const t = TYPES.find((x) => x[0] === k);
    if (!t || typeOf(site) === k) return;
    const body = Object.assign({}, t[3]);
    // the SPA switch belongs to the web server; leaving it alone elsewhere
    if (body.mode !== "webserver") delete body.spa_fallback;
    put(body, "Site type: " + t[1]);
  }

  function addr() { return (site.primary_url || site.view_url || "").replace(/^https?:\/\//, "").replace(/\/$/, ""); }

  function header() {
    const s = site;
    const admin = s.role === "admin";
    const status = WS.statusOf({
      locked: s.locked, role: s.role, mode: s.mode, container: s.container, expires_at: s.expires_at,
      domains: (s.pending_domains || []).map((p) => ({ Domain: p.domain, Pending: true })),
    });
    el("ws-stamps").innerHTML = '<span class="stamp ' + status.cls + '">' + esc(status.stamp) + "</span>" +
      (s.view_password_protected ? '<span class="stamp s-off">' + ic("lock") + "Password</span>" : "") +
      (admin ? '<span class="stamp s-info">You are admin</span>' : "") +
      (me && (me.admins || []).length ? '<span class="stamp s-info">Shared</span>' : "");
    el("site-id").classList.toggle("unnamed", !s.name);
    el("site-id").textContent = s.name || "Untitled drop";
    document.title = (s.name || s.id) + " — Sitebin";
    el("view-link").textContent = addr();
    el("ws-copy").innerHTML = ic("copy");
    el("ws-open").innerHTML = ic("ext");
    el("ws-open").href = s.primary_url || s.view_url;
    // The site's own list, current after every save (edit.js re-renders);
    // the colours come from the account's tags.
    const tags = (s.tags || []).map((n) => ({ name: n, color: tagColor(n) }));
    el("ws-head-tags").innerHTML = tags.map((t) => '<span class="tchip tag-' + esc(t.color || "grey") + '">' + esc(t.name) + "</span>").join("") +
      '<button class="btn ghost sm" type="button" data-go="settings">' + ic("plus") + "Tag</button>";
    const expSoon = s.expires_at && daysTo(s.expires_at) <= 7;
    el("ws-facts").innerHTML = [
      ["Type", WS.typeShort(s)],
      ["Size", fmtBytes(s.usage.bytes)],
      ["Files", (s.usage.files || 0).toLocaleString("en")],
      week != null ? ["Visitors 7d", week.toLocaleString("en")] : ["Views", typeof s.views === "number" ? s.views.toLocaleString("en") : "—"],
      ["Expires", s.expires_at ? fmtDate(s.expires_at) : "Never", expSoon],
    ].map(([k, v, w]) => '<div><span class="k">' + k + '</span><span class="v' + (w ? " warn" : "") + '">' + esc(v) + "</span></div>").join("");
    el("ws-acts").innerHTML =
      '<a class="btn primary" href="' + esc(s.primary_url || s.view_url) + '" target="_blank" rel="noopener">' + ic("ext") + "Open site</a>" +
      '<button class="btn" type="button" data-act="upload">' + ic("upload") + "Upload</button>" +
      '<button class="btn" type="button" data-act="download">' + ic("download") + "Download</button>" +
      '<button class="btn icon" type="button" data-act="more" aria-label="More actions" title="More actions">' + ic("dots") + "</button>";
  }

  function tagColor(n) {
    const t = ((WS.state && WS.state.tags) || []).find((x) => x.name.toLowerCase() === String(n).toLowerCase());
    return t ? t.color : "grey";
  }
  // edit.js redraws its tag editor on every change, a draft included.
  function colourTagEditor() {
    for (const c of el("e-tags").querySelectorAll(".chip")) {
      const want = "tag-" + tagColor(c.querySelector(".name")?.textContent || "");
      if (!c.classList.contains(want)) { c.className = "chip " + want; }
    }
  }
  new MutationObserver(colourTagEditor).observe(el("e-tags"), { childList: true });

  function crumbs() {
    let o = WS.order();
    // Opened from elsewhere (search, a link): the list it came from does not
    // hold it, so Back and J/K go by all sites.
    if (o && !o.ids.includes(site.id)) o = null;
    const back = "/account" + ((o && o.hash) || "#/sites");
    const ids = (o && o.ids) || [];
    const i = ids.indexOf(site.id);
    el("ws-crumbs").innerHTML = '<button class="btn ghost icon burger" type="button" data-ws-burger aria-label="Menu">' + ic("menu") + "</button>" +
      '<a class="btn ghost sm" href="' + esc(back) + '" title="Back to the list (Esc)">' + ic("arrowl") + esc((o && o.label) || "All sites") + '</a><span class="sep">/</span><span class="cur">' + esc(site.name || site.id) + "</span>" +
      (i >= 0 && ids.length > 1 ? '<span class="pager"><button class="btn ghost icon" type="button" data-step="-1" aria-label="Previous site">' + ic("chevl") + "</button>" + (i + 1) + " of " + ids.length +
        '<button class="btn ghost icon" type="button" data-step="1" aria-label="Next site">' + ic("chevr") + "</button><kbd>J</kbd><kbd>K</kbd></span>" : "");
  }

  function tabsList() {
    const t = [["overview", "Overview"], ["files", "Files", site.usage.files], ["addresses", "Addresses", (site.custom_domains || []).length + (site.pending_domains || []).length]];
    t.push(["forms", "Forms", formsData && formsData.enabled ? formsData.forms.length : ""]);
    if (tabs.list.some((x) => x.id === "analytics")) t.push(["analytics", "Analytics"]);
    if (tabs.list.some((x) => x.id === "git")) t.push(["git", "Git"]);
    if (site.mode === "container") t.push(["containers", "Containers", (site.container && site.container.services || []).length]);
    t.push(["people", "People", me ? 1 + (me.admins || []).length + (me.role === "admin" ? 1 : 0) : ""], ["activity", "Activity"], ["settings", "Settings"]);
    return t;
  }
  function current() { return tabs.active === "live" || !tabs.active ? pane : tabs.active; }
  function bar() {
    if (!site) return;
    const cur = current();
    el("ws-tabs").innerHTML = tabsList().map(([k, l, n]) => '<button role="tab" type="button" aria-selected="' + (cur === k) + '" class="' + (cur === k ? "on" : "") + '" data-go="' + k + '">' + l + (n || n === 0 ? ' <span class="n">' + Number(n).toLocaleString("en") + "</span>" : "") + "</button>").join("");
  }

  function go(to) {
    if (to === "git" || to === "analytics") { tabs.show(to, { push: true }); return; }
    if (!LIVE.includes(to)) to = "overview";
    if (to === "containers" && site.mode !== "container") to = "overview";
    pane = to;
    const hash = to === "files" && typeof cwd === "string" && cwd ? "#dir=" + cwd.split("/").map(encodeURIComponent).join("/") : to === "overview" ? "" : "#" + to;
    if (location.hash !== hash) history.pushState(null, "", location.pathname + location.search + hash);
    if (tabs.active !== "live") tabs.show("live"); else bar();
    showPane();
    const v = el("view"), tb = el("ws-tabs");
    if (v.scrollTop > tb.offsetTop) v.scrollTop = tb.offsetTop;
  }

  function showPane() {
    if (tabs.active && tabs.active !== "live") { bar(); return; }
    if (pane === "containers" && site.mode !== "container") pane = "overview";
    document.querySelectorAll(".wpane").forEach((p) => p.classList.toggle("on", p.dataset.pane === pane));
    formsOff();
    el("ws-cachecard").classList.toggle("hidden", site.mode === "container");
    if (pane === "overview") overview();
    if (pane === "people") people();
    if (pane === "activity") activityPane();
    bar();
  }

  // The forms card stays hidden where forms are off; once edit.js has
  // answered, the pane says so instead of standing empty.
  function formsOff() { el("ws-forms-off").classList.toggle("hidden", !formsLoaded || !el("card-forms").classList.contains("hidden")); }

  // ---- Overview ----
  function attention() {
    const s = site, a = [];
    if (s.expires_at && daysTo(s.expires_at) <= 7) { const d = new Date(s.expires_at); a.push(["s-warn", "clock", "Goes offline on " + WD[d.getDay()] + ", " + fmtDate(d) + ".", s.role === "admin" ? "" : "Extend", "settings"]); }
    for (const p of s.pending_domains || []) a.push(["s-warn", "ring", p.domain + " is waiting for its DNS record.", "Show the record", "addresses"]);
    if (s.start_page_missing) a.push(["s-warn", "files", "No index.html at the root: visitors see a 404 there.", "Fix it", "settings"]);
    if (!s.name) a.push(["s-info", "edit", "No name yet, so every list shows its address.", "Name it", "settings"]);
    if (s.mode === "container" && s.container && (!s.container.enabled || s.container.status !== "running")) a.push([s.container.status === "error" ? "s-bad" : "s-off", "stop", s.container.enabled ? "The project is " + (s.container.status || "not running") + "." : "The project is stopped.", "Open", "containers"]);
    if (s.mode === "webserver" && !s.spa_fallback && !s.not_found_serves && !s.start_page_missing) a.push(["s-info", "files", "No 404 page of its own: a mistyped address shows Sitebin’s.", "Add one", "settings"]);
    if (analyticsState && s.analytics_available && !analyticsOn()) a.push(["s-off", "chart", "Analytics are off.", "Turn on", "analytics"]);
    return a;
  }
  function addressRows() {
    const rows = [];
    const red = site.domain_redirects || {};
    for (const d of site.custom_domains || []) {
      const r = red[d];
      rows.push([d, '<span class="st s-ok">' + ic("check") + "Verified</span>", r && r.in_effect ? "Redirects to " + r.to + " · " + r.code : "Serves the site"]);
    }
    for (const p of site.pending_domains || []) rows.push([p.domain, '<span class="st s-warn">' + ic("ring") + "Needs DNS</span>", "Nothing until its record exists"]);
    if (site.mode === "container") {
      for (const a of site.addresses || []) rows.push([(a.url || a.address || "").replace(/^https?:\/\//, ""), '<span class="st s-ok">' + ic("check") + "Always yours</span>", "Port " + (a.endpoint || "")]);
    } else {
      const va = site.view_address || {};
      rows.push([site.view_url.replace(/^https?:\/\//, "").replace(/\/$/, ""), '<span class="st ' + (va.mode === "off" ? "s-off" : "s-ok") + '">' + ic(va.mode === "off" ? "stop" : "check") + (va.mode === "off" ? "Off" : "Always yours") + "</span>",
        va.mode === "redirect" ? "Redirects to " + (va.to || "") : va.mode === "off" ? "Off: a custom domain serves instead" : "Serves the site"]);
    }
    return rows;
  }
  // The analytics state carries the site's switch in its settings.
  const analyticsOn = () => !!(analyticsState && analyticsState.settings && analyticsState.settings.enabled);
  async function loadChart() {
    if (chart || !site.analytics_available) return;
    chart = { loading: true };
    try {
      analyticsState = await api("GET", "/analytics");
      if (!analyticsOn()) { chart = { off: true }; return; }
      const local = (d) => d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
      const ov = await api("GET", "/analytics/overview?from=" + local(new Date(Date.now() - 13 * WS.DAY)) + "&to=" + local(new Date()));
      chart = { series: ov.series || [], totals: ov.totals };
      // the header's figure is the list's: unique visitors over the last 7 days
      const wk = await api("GET", "/analytics/overview?from=" + local(new Date(Date.now() - 6 * WS.DAY)) + "&to=" + local(new Date()));
      if (wk && wk.totals) { week = wk.totals.visitors; header(); }
    } catch { chart = { off: true, err: true }; }
    if (pane === "overview" && (!tabs.active || tabs.active === "live")) overview();
  }
  function chartHTML() {
    if (!site.analytics_available) return '<p class="d" style="margin:6px 0 0">Analytics are not part of this site’s plan.</p>';
    if (!chart || chart.loading) return '<p class="d" style="margin:6px 0 0">Loading…</p>';
    if (chart.off) return '<p class="d" style="margin:6px 0 0">' + (chart.err ? "The statistics could not be read." : "Analytics are off for this site, so there is nothing to show.") + ' <button class="btn sm" type="button" data-go="analytics">Open analytics</button></p>';
    const vals = chart.series.map((p) => p.visitors), max = Math.max(1, ...vals);
    const cols = chart.series.map((p) => { const d = new Date(p.t); return '<div class="col" tabindex="0" aria-label="' + WD[d.getDay()] + " " + fmtDate(d) + ": " + p.visitors + ' visitors"><i style="height:' + Math.max(1.5, p.visitors / max * 100) + '%"></i><span class="tip"><b>' + p.visitors.toLocaleString("en") + "</b> visitors · " + p.views.toLocaleString("en") + " views<br>" + WD[d.getDay()] + ", " + fmtDate(d) + "</span></div>"; }).join("");
    const first = chart.series[0], mid = chart.series[Math.floor(chart.series.length / 2)];
    return '<p class="d" style="margin:0 0 6px">' + chart.totals.visitors.toLocaleString("en") + " visitors, " + chart.totals.views.toLocaleString("en") + " page views.</p>" +
      '<div class="wchart" style="height:150px" role="group" aria-label="Visitors per day, last 14 days"><span class="ymax">' + max.toLocaleString("en") + "</span>" + cols + "</div>" +
      '<div class="xlab"><span>' + (first ? fmtDate(first.t) : "") + "</span><span>" + (mid ? fmtDate(mid.t) : "") + "</span><span>today</span></div>" +
      '<details class="tview"><summary>Show as a table</summary><div class="tblwrap"><table class="tbl"><thead><tr><th>Day</th><th class="r">Visitors</th><th class="r">Views</th></tr></thead><tbody>' +
      chart.series.slice().reverse().map((p) => '<tr><td class="num">' + fmtDate(p.t) + '</td><td class="num r">' + p.visitors + '</td><td class="num r">' + p.views + "</td></tr>").join("") + "</tbody></table></div></details>";
  }
  function overview() {
    loadChart();
    if (!activity) loadActivity();
    const s = site, att = attention();
    const forms = formsData && formsData.enabled ? formsData.forms : [];
    const ct = s.container || {};
    el("pane-overview").innerHTML = '<div class="wgrid"><div class="wcol">' +
      '<section><h3 class="bh">Addresses<span class="acts"><button class="btn ghost sm" type="button" data-go="addresses">Manage addresses</button></span></h3><div class="tblwrap"><table class="tbl"><thead><tr><th>Address</th><th>State</th><th>What it does</th></tr></thead><tbody>' +
      addressRows().map(([a, stt, does]) => '<tr><td><span class="mono" style="font-size:12.5px;color:var(--amber)">' + esc(a) + "</span></td><td>" + stt + '</td><td class="dim" style="font-size:12.5px">' + esc(does) + "</td></tr>").join("") + "</tbody></table></div></section>" +
      '<section class="panel"><h3>Visitors, last 14 days<span class="acts"><button class="btn ghost sm" type="button" data-go="analytics">Open analytics</button></span></h3>' + chartHTML() + "</section>" +
      '<section class="panel"><h3>Recent activity<span class="acts"><button class="btn ghost sm" type="button" data-go="activity">All activity</button></span></h3>' + (activity ? timeline(activity.slice(0, 4)) : '<p class="d" style="margin:0">Loading…</p>') + "</section>" +
      '</div><div class="wcol">' +
      '<section class="panel"><h3>Needs attention</h3>' + (att.length ? '<ul class="attn">' + att.map(([c, i, t, b, tab]) => '<li><span class="' + c + '">' + ic(i) + '</span><span class="t">' + esc(t) + "</span>" + (b ? '<button class="btn sm" type="button" data-go="' + tab + '">' + esc(b) + "</button>" : "") + "</li>").join("") + "</ul>" : '<p class="allgood">' + ic("check") + "Nothing here needs you.</p>") + "</section>" +
      '<section class="panel"><h3>At a glance</h3><dl class="kv">' +
      "<dt>Owner</dt><dd>" + (s.role === "admin" ? esc(s.shared_by || "another account") : "You") + (me && (me.admins || []).length ? ' <span class="faint">+ ' + me.admins.length + " admin" + (me.admins.length > 1 ? "s" : "") + "</span>" : "") + "</dd>" +
      "<dt>Type</dt><dd>" + esc(WS.typeLong(s)) + "</dd>" +
      "<dt>Created</dt><dd>" + esc(fmtDate(s.created_at, true)) + "</dd>" +
      "<dt>Last change</dt><dd>" + esc(ago(s.updated_at)) + "</dd>" +
      "<dt>Expires</dt><dd>" + (s.expires_at ? esc(fmtDate(s.expires_at, true)) + ' <span class="faint">' + (s.expiry_renews ? "by your plan, renews on every change" : s.role === "admin" ? "set by the owner" : "set by you") + "</span>" : "Never") + "</dd>" +
      "<dt>View password</dt><dd>" + (s.view_password_protected ? "On" : "Off") + "</dd>" +
      (s.mode !== "container" ? "<dt>Caching</dt><dd>" + esc(CACHE[s.cache || "auto"] || s.cache) + "</dd>" : "") +
      (s.git_available ? "<dt>Git</dt><dd>" + (s.usage.git_bytes ? "Repository, " + esc(fmtBytes(s.usage.git_bytes)) : "No repository yet") + "</dd>" : "") +
      "<dt>Storage</dt><dd>" + esc(fmtBytes(s.usage.bytes)) + " of " + esc(fmtBytes(s.usage.max_bytes)) + "</dd>" +
      (typeof s.views === "number" ? "<dt>Views</dt><dd>" + s.views.toLocaleString("en") + (s.last_seen ? ' <span class="faint">last ' + esc(ago(s.last_seen)) + "</span>" : "") + "</dd>" : "") +
      "</dl></section>" +
      (s.mode === "container" ? '<section class="panel"><h3>Containers<span class="acts"><button class="btn ghost sm" type="button" data-go="containers">Open</button></span></h3><div class="lines">' +
        ((ct.services || []).length ? ct.services.map((v) => '<div class="line">' + ic("box") + '<div class="grow"><span class="a">' + esc(v.name) + ' <span class="faint">' + esc(v.image || "") + '</span></span></div><span class="st ' + (v.state === "running" ? "s-ok" : "s-off") + '">' + ic(v.state === "running" ? "dot" : "stop") + esc(v.state || "—") + "</span></div>").join("") : '<div class="line"><span class="faint">' + esc(ct.enabled ? ct.status || "Starting" : "Stopped") + "</span></div>") + "</div></section>" : "") +
      '<section class="panel"><h3>Forms<span class="acts"><button class="btn ghost sm" type="button" data-go="forms">' + (forms.length ? "Open" : "Add a form") + "</button></span></h3>" +
      (forms.length ? '<div class="lines">' + forms.map((f) => '<div class="line">' + ic("mail") + '<div class="grow"><span class="a">' + esc(f.name || f.key) + '</span><span class="s">' + esc(f.recipient || "") + (f.status ? " · " + esc(f.status) : "") + "</span></div></div>").join("") + "</div>" :
        '<p class="d" style="margin:0">' + (formsData && formsData.enabled ? "No forms. A form mails what visitors send to an address you choose." : "Forms are not available for this site.") + "</p>") + "</section>" +
      "</div></div>";
  }

  // ---- Activity ----
  const KIND = (a) => /^domain|view-address/.test(a) ? "addresses" : /share|transfer|adopt|rotate|admin|invite/.test(a) ? "people" : /settings|form|analytics|container|git-delete|cache/.test(a) ? "settings" : "content";
  const human = (a) => (a || "").replace(/[-_]/g, " ").replace(/^./, (c) => c.toUpperCase());
  function line(x) {
    const who = x.actor === "you" ? "you" : x.actor === "owner" ? "the owner" : x.actor === "admin" ? "an admin" : x.actor === "edit password" ? "the edit password" : "";
    const via = [x.surface, who && "by " + who].filter(Boolean).join(" · ");
    return { w: x.time, txt: human(x.action) + (x.files ? " · " + x.files + " file" + (x.files > 1 ? "s" : "") : "") + (x.detail ? " · " + x.detail : "") + (x.times > 1 ? " · ×" + x.times : ""), via, k: KIND(x.action) };
  }
  function timeline(list) {
    if (!list.length) return '<p class="d" style="margin:0">Nothing recorded yet.</p>';
    return '<ul class="timeline">' + list.map((x) => { const l = line(x); return "<li><b>" + esc(l.txt) + '</b><span class="w">' + esc(ago(l.w)) + (l.via ? " · " + esc(l.via) : "") + "</span></li>"; }).join("") + "</ul>";
  }
  async function loadActivity() {
    activity = [];
    try {
      const res = await fetch("/account/api/sites/" + encodeURIComponent(site.id) + "/activity", { headers: { Accept: "application/json" } });
      activity = res.ok ? await res.json() : [];
    } catch { activity = []; }
    if (pane === "overview") overview();
    if (pane === "activity") activityPane();
  }
  let actFilter = "all";
  function activityPane() {
    if (!activity) { loadActivity(); el("pane-activity").innerHTML = '<p class="loading">Loading…</p>'; return; }
    const kinds = [["all", "All"], ["content", "Content"], ["addresses", "Addresses"], ["people", "People"], ["settings", "Settings"]];
    const list = actFilter === "all" ? activity : activity.filter((x) => KIND(x.action) === actFilter);
    el("pane-activity").innerHTML = '<div class="toolbar"><div class="wchips">' + kinds.map(([k, l]) => '<button class="wchip' + (actFilter === k ? " on" : "") + '" type="button" data-act-kind="' + k + '">' + l + " <small>" + (k === "all" ? activity.length : activity.filter((x) => KIND(x.action) === k).length) + "</small></button>").join("") +
      '</div><div class="right"><button class="btn ghost sm" type="button" data-act-reload>' + ic("refresh") + "Refresh</button></div></div>" +
      '<section class="panel">' + timeline(list) + '</section><p class="faint" style="font-size:12px">Kept for 90 days. Addresses and browsers are not shown here.</p>';
  }

  // ---- People ----
  function people() {
    const box = el("pane-people");
    if (!me) { box.innerHTML = '<section class="panel"><p class="d" style="margin:0">' + (WS.state ? "Who manages this site is decided by its owner." : "Loading…") + "</p></section>"; return; }
    if (me.role === "admin") {
      box.innerHTML = '<div class="wgrid2"><section class="panel"><h3>Owner</h3><p class="d" style="margin:0">' + esc(me.shared_by) + " owns this site and decides who manages it.</p></section>" +
        '<section class="panel"><h3>You</h3><p class="d">Admin' + (me.shared_until ? " until " + esc(me.shared_until) : "") + ". You can change files, addresses, forms and settings. Deleting, transferring and sharing stay with the owner.</p>" +
        '<button class="btn sm" type="button" data-people="leave">' + ic("x") + "Leave this site</button></section></div>";
      return;
    }
    const adm = me.admins || [], inv = me.open_invites || [];
    box.innerHTML = '<div class="wgrid"><div class="wcol">' +
      '<section><h3 class="bh">Who manages this site</h3><div class="tblwrap"><table class="tbl"><thead><tr><th>Account</th><th>Role</th><th>Until</th><th></th></tr></thead><tbody>' +
      '<tr><td>You <span class="faint">' + esc(WS.state.account.email) + '</span></td><td>Owner</td><td class="num">—</td><td></td></tr>' +
      adm.map((a) => "<tr><td>" + esc(a.Email) + (a.Paused ? '<br><span class="faint" style="font-size:12px">' + esc(a.Paused) + "</span>" : "") + '</td><td>Admin</td><td class="num">' + esc(a.Until || "—") + '</td><td class="r"><button class="btn sm" type="button" data-people="end" data-ref="' + esc(a.Ref) + '">End share</button></td></tr>').join("") +
      inv.map((i) => "<tr><td>" + esc(i.Email) + ' <span class="faint">invited</span></td><td>Admin</td><td class="num">' + esc(i.Until || "—") + '</td><td class="r"><button class="btn sm" type="button" data-people="cancel" data-ref="' + esc(i.ID) + '">Withdraw</button></td></tr>').join("") +
      "</tbody></table></div></section>" +
      (me.can_share ? '<section class="panel"><h3>Share with an admin</h3><p class="d">They manage the site with their own account. Deleting, transferring and sharing stay with you.</p>' +
        '<form class="field" id="shareform"><input type="email" name="email" required maxlength="254" placeholder="Their account’s email" aria-label="Email"><input type="date" name="until" aria-label="Until, optional" style="flex:0 0 170px"><button class="btn sm primary" type="submit">Invite</button></form></section>' :
        '<section class="panel"><h3>Share with an admin</h3><p class="d" style="margin:0">Sharing is not part of your plan. <a href="/account#/plan">See the plans</a></p></section>') +
      '</div><div class="wcol">' +
      '<section class="panel"><h3>Transfer</h3><p class="d">Give the site to another account, with its addresses. Nothing moves until they accept.</p><a class="btn sm" href="/account/transfer?site=' + encodeURIComponent(site.id) + '">' + ic("swap") + "Start a transfer</a></section>" +
      '<section class="panel"><h3>Edit password</h3><p class="d">For tools that sign in with the site’s own password. A reset ends every upload token of this site; the new one is shown once.</p><button class="btn sm" type="button" data-people="rotate">' + ic("key") + "Reset edit password</button></section>" +
      "</div></div>";
    el("shareform")?.addEventListener("submit", async (e) => {
      e.preventDefault();
      const f = new FormData(e.target);
      try { await post("/account/sites/" + site.id + "/share", { email: f.get("email"), until: f.get("until") }); toast("Invitation sent."); await refreshState(); } catch (err) { fail(err); }
    });
  }
  async function refreshState() { await WS.load(); me = WS.state.sites.find((s) => s.id === site.id) || null; WS.renderSide({ view: "sites" }); paint(); }

  // ---- actions ----
  async function deleteSite() {
    const doms = (site.custom_domains || []).concat((site.pending_domains || []).map((p) => p.domain));
    if (!(await confirm("Delete " + (site.name || site.id) + "?", "Its " + site.usage.files + (site.usage.files === 1 ? " file" : " files") + (doms.length ? " and " + doms.join(", ") : "") + (site.usage.git_bytes ? " and its Git repository" : "") + " go with it. There is no undo.", "Delete site", true))) return;
    try {
      await api("DELETE", "");
      const o = WS.order();
      location.href = "/account" + ((o && o.hash) || "#/sites");
    } catch (err) { fail(err); }
  }
  function moreMenu(btn) {
    const own = site.role !== "admin";
    menu(btn, [
      { label: "Rename", icon: "edit", run: () => { go("settings"); el("e-name").focus(); } },
      { label: "Tags", icon: "tag", run: () => { go("settings"); el("e-tag").focus(); } },
      { label: "Add a domain", icon: "globe", run: () => { go("addresses"); el("e-domain").focus(); } },
      { label: "Download ZIP", icon: "download", run: () => el("download-zip").click() },
      { sep: true },
      own && me && me.can_share && { label: "Share with an admin", icon: "share", run: () => go("people") },
      own && { label: "Transfer", icon: "swap", href: "/account/transfer?site=" + encodeURIComponent(site.id) },
      own && { label: "Reset edit password", icon: "key", run: () => peopleAction("rotate") },
      !own && { label: "Leave this site", icon: "x", run: () => peopleAction("leave") },
      own && { sep: true },
      own && { label: "Delete…", icon: "trash", danger: true, run: deleteSite },
    ]);
  }
  async function peopleAction(what, ref) {
    try {
      if (what === "rotate") {
        if (!(await confirm("Reset the edit password?", "A new password replaces the old one at once, and every upload token of the site ends. It is shown once.", "Reset password"))) return;
        await post("/account/sites/" + site.id + "/rotate", {});
      } else if (what === "leave") {
        if (!(await confirm("Leave this site?", "You stop managing it; the owner keeps it.", "Leave"))) return;
        await post("/account/sites/" + site.id + "/leave", {});
        location.href = "/account#/sites";
        return;
      } else if (what === "end") {
        if (!(await confirm("End this share?", "They stop managing the site at once.", "End share"))) return;
        await post("/account/sites/" + site.id + "/share/" + encodeURIComponent(ref) + "/end", {});
        toast("The share has ended.");
      } else if (what === "cancel") {
        await post("/account/invites/" + encodeURIComponent(ref) + "/cancel", {});
        toast("Invitation withdrawn.");
      }
      await refreshState();
    } catch (err) { fail(err); }
  }

  function step(dir) {
    const o = WS.order(), st = WS.state;
    if (!o || !st) return;
    const i = o.ids.indexOf(site.id);
    if (i < 0 || o.ids.length < 2) return;
    for (let k = 1; k < o.ids.length; k++) {
      const s = st.sites.find((x) => x.id === o.ids[(i + dir * k + o.ids.length * k) % o.ids.length]);
      if (s && s.edit_url) { o.last = s.id; try { sessionStorage.setItem("ws-order", JSON.stringify(o)); } catch {} WS.openSite(s, pane === "overview" ? "" : pane); return; }
    }
  }

  // ---- events ----
  document.addEventListener("click", (e) => {
    const g = e.target.closest("[data-go]");
    if (g) { e.preventDefault(); go(g.dataset.go); return; }
    const a = e.target.closest("[data-act]");
    if (a) {
      if (a.dataset.act === "upload") { go("files"); el("e-pick-files").click(); }
      else if (a.dataset.act === "download") el("download-zip").click();
      else if (a.dataset.act === "more") moreMenu(a);
      return;
    }
    if (e.target.closest("#ws-copy")) { navigator.clipboard?.writeText(site.primary_url || site.view_url).then(() => toast("Copied.")); return; }
    const ty = e.target.closest("[data-type]");
    if (ty) { setType(ty.dataset.type); return; }
    const st = e.target.closest("[data-step]");
    if (st) { step(+st.dataset.step); return; }
    const p = e.target.closest("[data-people]");
    if (p) { peopleAction(p.dataset.people, p.dataset.ref); return; }
    const k = e.target.closest("[data-act-kind]");
    if (k) { actFilter = k.dataset.actKind; activityPane(); return; }
    if (e.target.closest("[data-act-reload]")) { activity = null; activityPane(); }
  });
  // Saving the expiry: the date picked here, at the time of day the expiry
  // already has (a plan's term keeps its hour), else at the day's end. Runs
  // before edit.js's own handler, which then saves #e-expires as always.
  document.addEventListener("click", (e) => {
    if (!e.target.closest("#save-expiry")) return;
    const day = el("ws-expires").value, cur = el("e-expires").value;
    el("e-expires").value = day ? day + "T" + (cur.length >= 16 ? cur.slice(11, 16) : "23:59") : "";
  }, true);
  el("ws-expires").addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); el("save-expiry").click(); } });
  // The workspace asks before deleting, in a dialog, and returns to the list
  // afterwards, instead of edit.js's second click and its blank page.
  el("delete-site").addEventListener("click", (e) => { e.stopImmediatePropagation(); e.preventDefault(); deleteSite(); }, true);
  document.addEventListener("keydown", (e) => {
    if (!site || e.ctrlKey || e.metaKey || e.altKey) return;
    if (/INPUT|SELECT|TEXTAREA/.test(document.activeElement.tagName) || document.querySelector(".scrim.on") || !el("editor-overlay").classList.contains("hidden")) return;
    if (e.key === "j") step(1);
    else if (e.key === "k") step(-1);
    else if (e.key === "Escape") { const o = WS.order(); location.href = "/account" + ((o && o.hash) || "#/sites"); }
  });
})();
