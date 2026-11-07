// Sitebin workspace — the account page (ELv2): the site list and every view
// of the sidebar. Every change posts to the route the classic dashboard posts
// to (WS.post), so the server decides exactly as it always has.
"use strict";

(() => {
  const { ic, esc, fmtDate, ago, daysTo, fmtBytes, statusOf, primaryAddr, primaryURL, post, submit, siteAPI, toast, fail, dialog, confirm, menu, editIDOf } = WS;
  const view = document.getElementById("view");
  const insp = document.getElementById("insp");
  const app = document.getElementById("app");
  const S = {
    status: "all", q: "", group: "none", sort: "updated", picked: new Set(), visitors: {},
    reg: null, regQ: "", regFilter: "", regOpen: null, reports: null, containers: null, health: null, confirm: null,
  };
  const st = () => WS.state;
  const WD = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

  function route() {
    const h = location.hash.replace(/^#\/?/, "");
    const i = h.indexOf("/");
    const v = i < 0 ? h : h.slice(0, i);
    let arg = i < 0 ? "" : h.slice(i + 1);
    try { arg = decodeURIComponent(arg); } catch {}
    return { view: v || "sites", arg };
  }

  async function reload() {
    try { await WS.load(); } catch (err) { fail(err); }
    render();
  }

  // ---- boot ----
  (async () => {
    try {
      await WS.load();
    } catch (err) {
      view.innerHTML = '<div class="banner err">' + esc(err.message) + "</div>";
      return;
    }
    const notice = document.body.dataset.notice;
    render();
    if (notice) toast(notice);
  })();
  // A hash change before the state arrived (a link straight to #/…) waits
  // for the boot, which renders the hash it finds.
  // Every view switch also reloads the account's state quietly, so an offer,
  // an invitation or another device's change shows without a reload.
  window.addEventListener("hashchange", () => {
    if (!WS.state) return;
    S.picked.clear(); S.confirm = null; render(); view.scrollTop = 0;
    const at = location.hash;
    WS.load().then(() => { if (location.hash === at && !document.querySelector(".scrim.on")) render(); }).catch(() => {});
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && S.regOpen) { S.regOpen = null; S.confirm = null; renderInsp(); }
    if (e.key === "/" && !/INPUT|SELECT|TEXTAREA/.test(document.activeElement.tagName)) {
      const q = document.getElementById("q");
      if (q) { e.preventDefault(); q.focus(); }
    }
  });

  function render() {
    const r = route();
    const side = r.view === "tag" ? { tag: r.arg } : { view: r.view };
    WS.renderSide(side);
    if (!["register", "op", "reports"].includes(r.view)) { S.regOpen = null; renderInsp(); }
    const fn = {
      sites: renderSites, shared: renderSites, tag: renderSites, untagged: renderSites,
      inbox: renderInbox, domains: renderDomains, tokens: renderTokens, plan: renderPlan, account: renderAccount,
      op: renderOp, register: renderRegister, reports: renderReports, containers: renderContainers,
    }[r.view] || renderSites;
    if (["op", "register", "reports", "containers"].includes(r.view) && !st().account.is_admin) { location.hash = "#/sites"; return; }
    fn(r);
    if (st().account.is_admin) opBadge();
  }

  function head(title, lede, acts) {
    return '<header class="vh"><button class="btn ghost icon burger" data-ws-burger aria-label="Menu">' + ic("menu") + "</button><div><h1>" + title + '</h1><p class="lede">' + lede + "</p></div>" +
      '<div class="acts">' + (acts || "") + "</div></header>" + licence();
  }
  function licence() {
    const l = st().license;
    return l ? '<div class="banner ' + (l.Severity === "err" ? "err" : "") + '"><strong>Sitebin Enterprise licence</strong>&nbsp;' + esc(l.Text) + "</div>" : "";
  }
  const tagChip = (t) => '<span class="tchip tag-' + esc(t.color || "grey") + '">' + esc(t.name) + "</span>";
  const tagByKey = (k) => st().tags.find((t) => t.key === k);
  const groupOfKey = (k) => (tagByKey(k) || {}).group || "";
  const siteByID = (id) => st().sites.find((s) => s.id === id);
  const label = (s) => s.name || primaryAddr(s);
  const owned = (s) => s.role === "owner";

  // ---- the site list ----

  const FILTERS = {
    all: () => true,
    live: (s) => statusOf(s).label === "Live",
    expiring: (s) => !s.locked && s.expires_at && daysTo(s.expires_at) <= 7,
    dns: (s) => (s.domains || []).some((d) => d.Pending),
    shared: (s) => s.role === "admin" || (s.admins || []).length > 0 || (s.open_invites || []).length > 0,
    containers: (s) => s.mode === "container",
    locked: (s) => !!s.locked,
  };
  function scope(r) {
    let rows = st().sites.slice();
    if (r.view === "shared") rows = rows.filter((s) => s.role === "admin");
    if (r.view === "tag") rows = rows.filter((s) => s.tags.some((t) => t.key === r.arg));
    if (r.view === "untagged") rows = rows.filter((s) => !s.tags.length);
    return rows;
  }
  function filtered(r) {
    let rows = scope(r).filter(FILTERS[S.status] || FILTERS.all);
    if (S.q) {
      const q = S.q.toLowerCase();
      rows = rows.filter((s) => (s.name + " " + s.view_url + " " + (s.domains || []).map((d) => d.Domain).join(" ") + " " + s.tags.map((t) => t.name).join(" ")).toLowerCase().includes(q));
    }
    const upd = (s) => new Date(s.updated_at && !s.updated_at.startsWith("0001") ? s.updated_at : s.created_at);
    const by = {
      updated: (a, b) => upd(b) - upd(a),
      name: (a, b) => label(a).localeCompare(label(b)),
      expiry: (a, b) => (a.expires_at ? new Date(a.expires_at) : 9e15) - (b.expires_at ? new Date(b.expires_at) : 9e15),
      size: (a, b) => (b.bytes + b.git_bytes) - (a.bytes + a.git_bytes),
      visitors: (a, b) => (S.visitors[b.id] ?? -1) - (S.visitors[a.id] ?? -1),
    }[S.sort] || (() => 0);
    return rows.sort(by);
  }

  function rowHTML(s) {
    const t = statusOf(s);
    const doms = s.domains || [];
    const first = doms[0];
    const dom = first ? '<span class="doms' + (first.Pending ? " pend" : "") + '">' + esc(first.Domain) + (first.Pending ? " · DNS" : "") + (doms.length > 1 ? '<span class="more">+' + (doms.length - 1) + "</span>" : "") + "</span>" : '<span class="faint mono" style="font-size:12px">—</span>';
    const marks = [
      s.role === "admin" ? '<span class="smark" title="Shared with you by ' + esc(s.shared_by) + '">' + ic("share") + "you are admin</span>" : "",
      owned(s) && ((s.admins || []).length || (s.open_invites || []).length) ? '<span class="smark">' + ic("share") + "shared</span>" : "",
      s.view_password ? '<span class="smark" style="color:var(--ink-faint)">' + ic("lock") + "password</span>" : "",
    ].join(" ");
    const exp = s.locked ? '<span class="exp faint">kept while locked</span>' : s.expires_at ?
      '<span class="exp' + (daysTo(s.expires_at) <= 7 ? " soon" : "") + '">' + fmtDate(s.expires_at) + "<small>in " + Math.max(0, daysTo(s.expires_at)) + " days" + (s.expiry_by_owner ? "" : " · plan") + "</small></span>" : '<span class="exp faint">never</span>';
    const vis = S.visitors[s.id];
    const upd = s.updated_at && !s.updated_at.startsWith("0001") ? s.updated_at : s.created_at;
    return '<tr class="row' + (S.picked.has(s.id) ? " picked" : "") + (s.locked ? " locked" : "") + (WS.order()?.last === s.id ? " open" : "") + '" data-site="' + s.id + '">' +
      '<td class="cb"><input type="checkbox" data-pick="' + s.id + '"' + (S.picked.has(s.id) ? " checked" : "") + ' aria-label="Select ' + esc(label(s)) + '"></td>' +
      '<td><span class="sname' + (s.name ? "" : " unnamed") + '">' + (s.name ? esc(s.name) : "Untitled drop") + " " + marks + '</span><span class="saddr">' + esc((s.view_url || "").replace(/^https?:\/\//, "")) + "</span></td>" +
      '<td><span class="st ' + t.cls + '">' + ic(t.icon) + esc(t.label) + "</span></td>" +
      '<td class="hide-sm">' + dom + "</td>" +
      '<td class="hide-md num">' + esc(WS.typeShort(s)) + "</td>" +
      '<td class="hide-sm"><div class="tags">' + s.tags.slice(0, 2).map(tagChip).join("") + (s.tags.length > 2 ? '<span class="tchip tmore" title="' + esc(s.tags.slice(2).map((t) => t.name).join(", ")) + '">+' + (s.tags.length - 2) + "</span>" : "") + "</div></td>" +
      '<td class="hide-md num r" data-vis="' + s.id + '">' + (vis == null ? "—" : vis.toLocaleString("en")) + "</td>" +
      '<td class="hide-md num">' + esc(ago(upd)) + "</td>" +
      "<td>" + exp + "</td>" +
      '<td class="r"><button class="btn ghost icon rowmenu" type="button" data-menu="' + s.id + '" aria-label="Actions for ' + esc(label(s)) + '">' + ic("dots") + "</button></td></tr>";
  }

  function needsItems() {
    const out = [];
    for (const s of st().sites) {
      if (owned(s) && !s.locked && s.expires_at && daysTo(s.expires_at) <= 7) {
        const d = new Date(s.expires_at);
        out.push(["clock", "<b>" + esc(label(s)) + "</b> goes offline on " + WD[d.getDay()] + ", " + fmtDate(d) + ".", "Extend", () => WS.openSite(s, "settings")]);
      }
    }
    for (const s of st().sites) for (const d of s.domains || []) {
      if (d.Pending) out.push(["globe", '<span class="mono">' + esc(d.Domain) + "</span> is waiting for its DNS record.", "Show the record", () => WS.openSite(s, "addresses")]);
    }
    for (const t of st().takeovers) out.push(["inbox", "<b>" + esc(t.From) + "</b> offers you " + esc(t.Count) + ". Accept by " + esc(t.AcceptBy) + ".", "Review the offer", () => { location.hash = "#/inbox"; }]);
    for (const i of st().invites) out.push(["share", "You are invited to manage <b>" + esc(i.Site) + "</b>.", "Review the invitation", () => { location.hash = "#/inbox"; }]);
    for (const s of st().sites) if (s.locked && owned(s)) out.push(["lock", "<b>" + esc(label(s)) + "</b> is locked by the operator" + (s.locked.reason ? ": " + esc(s.locked.reason) : "") + ".", "Open", () => WS.openSite(s)]);
    return out;
  }

  // "21 of 500 sites, 4.8 GB in use": the plan caps how many sites an
  // account owns (never how much they hold together: storage is per site),
  // so the "of" is the site cap, counted over owned sites. No cap, no "of".
  function siteCountLede(total) {
    const cap = (st().plan && st().plan.caps && st().plan.caps.sites) || 0;
    if (cap > 0) {
      const own = st().sites.filter(owned);
      return own.length + " of " + cap + " sites, " + fmtBytes(own.reduce((c, s) => c + s.bytes + s.git_bytes, 0)) + " in use.";
    }
    return st().sites.length + (st().sites.length === 1 ? " site, " : " sites, ") + fmtBytes(total) + " in use.";
  }

  function renderSites(r) {
    const all = scope(r), rows = filtered(r);
    const groups = st().groups;
    const t = r.view === "tag" ? tagByKey(r.arg) : null;
    const title = r.view === "shared" ? "Shared with me" : r.view === "untagged" ? "Untagged sites" : t ? '<span class="tdot tag-' + esc(t.color) + '" style="width:12px;height:12px"></span>' + esc(t.name) : "Sites";
    const total = st().sites.reduce((c, s) => c + s.bytes + s.git_bytes, 0);
    const lede = r.view === "shared" ? "Sites other accounts made you an admin of." : r.view === "tag" || r.view === "untagged" ? all.length + " of " + st().sites.length + " sites." :
      siteCountLede(total);
    const cnt = (k) => all.filter(FILTERS[k]).length;
    const chip = (k, l) => (k !== "all" && k !== "live" && !cnt(k) ? "" : '<button class="wchip' + (S.status === k ? " on" : "") + '" type="button" data-status="' + k + '">' + l + " <small>" + cnt(k) + "</small></button>");
    // Grouping by a tag group is offered for the groups these sites use;
    // past two, they share one button with a menu, so the bar stays one row.
    const used = groups.filter((g) => all.some((s) => s.tags.some((x) => groupOfKey(x.key) === g.id)));
    if (S.group.startsWith("g:") && !used.some((g) => "g:" + g.id === S.group)) S.group = "none";
    const curG = used.find((g) => "g:" + g.id === S.group);
    const segs = [["none", ic("list") + "List"]]
      .concat(used.length <= 2 ? used.map((g) => ["g:" + g.id, ic("tag") + "By " + esc(g.name)]) : [[curG ? S.group : "g:*", ic("tag") + (curG ? "By " + esc(curG.name) : "By tag group") + ic("chevd")]])
      .concat([["status", "By status"], ["type", "By type"]]);
    if (!segs.some(([k]) => k === S.group)) S.group = "none";

    let body;
    if (!all.length) {
      body = '<tr><td colspan="10"><div class="empty">' + (r.view === "sites" ? 'No sites yet. <a class="btn sm primary" href="' + esc(st().account.publish_url) + '">' + ic("plus") + "Publish one</a> — it will be owned by this account." : "No site here.") + "</div></td></tr>";
    } else if (!rows.length) {
      body = '<tr><td colspan="10"><div class="empty">No site matches. <button class="btn sm" type="button" data-clear>Clear the filter</button></div></td></tr>';
    } else if (S.group === "none") {
      body = rows.map(rowHTML).join("");
    } else {
      const key = S.group.startsWith("g:") ? (s) => { const gid = S.group.slice(2); const c = s.tags.find((x) => groupOfKey(x.key) === gid); return c ? c.name : "No " + (groups.find((g) => g.id === gid) || {}).name; }
        : S.group === "status" ? (s) => statusOf(s).label.replace(/\d+ d$/, "soon") : (s) => WS.typeLong(s);
      const by = {};
      rows.forEach((s) => (by[key(s)] ||= []).push(s));
      body = Object.entries(by).map(([k, list]) => {
        const tg = st().tags.find((x) => x.name === k);
        return '<tr class="grp"><td colspan="10">' + (tg ? '<span class="tdot tag-' + esc(tg.color) + '" style="margin-right:7px"></span>' : "") + esc(k) + '<span class="faint">' + list.length + "</span></td></tr>" + list.map(rowHTML).join("");
      }).join("");
    }
    const needs = r.view === "sites" && localStorage.getItem("ws-needs-hidden") !== new Date().toDateString() ? needsItems() : [];
    const shown = needs.slice(0, 4);
    view.innerHTML = head(title, esc(lede),
      '<div class="wseg" role="group" aria-label="Group by">' + segs.map(([k, l]) => '<button type="button" class="' + (S.group === k ? "on" : "") + '" data-group="' + esc(k) + '">' + l + "</button>").join("") + "</div>" +
      '<a class="btn primary" href="' + esc(st().account.publish_url) + '">' + ic("plus") + "Publish a new site</a>") +
      (shown.length ? '<section class="needs" id="needs" style="--n:' + shown.length + '"><div class="head"><b>' + needs.length + "</b><span>" + (needs.length === 1 ? "thing needs you" : "things need you") + '</span><br><button class="btn ghost sm" type="button" style="margin:8px 0 0 -10px" data-hide-needs>Hide for today</button></div>' +
        shown.map(([i, html, b], k) => '<div class="it"><p>' + ic(i) + "<span>" + html + '</span></p><button class="btn sm" type="button" data-need="' + k + '">' + esc(b) + "</button></div>").join("") + "</section>" : "") +
      '<div class="toolbar"><label class="search">' + ic("search") + '<input id="q" type="search" placeholder="Filter by name, address, domain or tag" value="' + esc(S.q) + '" aria-label="Filter sites"></label>' +
      '<div class="wchips">' + chip("all", "All") + chip("live", "Live") + chip("expiring", "Expiring soon") + chip("dns", "Needs DNS") + chip("shared", "Shared") + chip("containers", "Containers") + chip("locked", "Locked") + "</div>" +
      '<div class="right"><select id="sort" class="wsel" aria-label="Sort">' + [["updated", "Recently changed"], ["name", "Name"], ["expiry", "Expires first"], ["size", "Largest first"], ["visitors", "Most visitors"]].map(([v, l]) => '<option value="' + v + '"' + (S.sort === v ? " selected" : "") + ">" + l + "</option>").join("") + "</select></div></div>" +
      '<div class="tblwrap" id="sitetable"><table class="tbl"><thead><tr><th class="cb"><input type="checkbox" data-pickall aria-label="Select all"' + (rows.length && rows.every((s) => S.picked.has(s.id)) ? " checked" : "") + '></th><th>Site</th><th>Status</th><th class="hide-sm">Address</th><th class="hide-md">Type</th><th class="hide-sm">Tags</th><th class="hide-md r">Visitors 7d</th><th class="hide-md">Changed</th><th>Expires</th><th></th></tr></thead><tbody>' + body + "</tbody></table></div>" +
      (S.picked.size ? bulkBar() : "");
    WS.saveOrder(rows.map((s) => s.id), r.view === "shared" ? "Shared with me" : t ? t.name : r.view === "untagged" ? "Untagged" : "All sites", location.hash || "#/sites");
    // events
    view.querySelector("#q").addEventListener("input", (e) => { S.q = e.target.value; const pos = e.target.selectionStart; renderSites(route()); const q = document.getElementById("q"); q.focus(); q.setSelectionRange(pos, pos); });
    view.querySelector("#sort").addEventListener("change", (e) => { S.sort = e.target.value; renderSites(route()); });
    view.onclick = (e) => {
      const el = e.target;
      if (el.closest("[data-status]")) { S.status = el.closest("[data-status]").dataset.status; renderSites(route()); return; }
      if (el.closest("[data-group]")) {
        const b = el.closest("[data-group]");
        if (used.length > 2 && b.dataset.group.startsWith("g:")) { menu(b, used.map((g) => ({ label: "By " + g.name, icon: "tag", run: () => { S.group = "g:" + g.id; renderSites(route()); } }))); return; }
        S.group = b.dataset.group; renderSites(route()); return;
      }
      if (el.closest("[data-clear]")) { S.status = "all"; S.q = ""; renderSites(route()); return; }
      if (el.closest("[data-hide-needs]")) { localStorage.setItem("ws-needs-hidden", new Date().toDateString()); renderSites(route()); toast("Hidden until tomorrow. Nothing in it is lost."); return; }
      if (el.closest("[data-need]")) { shown[+el.closest("[data-need]").dataset.need][3](); return; }
      if (el.closest("[data-pickall]")) { const on = el.checked; rows.forEach((s) => (on ? S.picked.add(s.id) : S.picked.delete(s.id))); renderSites(route()); return; }
      if (el.closest("[data-pick]")) { const id = el.dataset.pick; el.checked ? S.picked.add(id) : S.picked.delete(id); renderSites(route()); return; }
      if (el.closest("[data-menu]")) { const b = el.closest("[data-menu]"); siteMenu(b, siteByID(b.dataset.menu)); return; }
      if (el.closest("[data-bulk]")) { bulk(el.closest("[data-bulk]").dataset.bulk); return; }
      const row = el.closest("tr.row");
      if (row && !el.closest("a,button,input")) {
        const o = WS.order(); if (o) { o.last = row.dataset.site; WS.saveOrder(o.ids, o.label, o.hash); try { sessionStorage.setItem("ws-order", JSON.stringify(o)); } catch {} }
        WS.openSite(siteByID(row.dataset.site));
      }
    };
    loadVisitors(rows);
  }

  function bulkBar() {
    return '<div class="bulk" id="bulk"><b>' + S.picked.size + ' selected</b>' +
      '<button class="btn sm" type="button" data-bulk="tags">' + ic("tag") + "Tags</button>" +
      '<button class="btn sm" type="button" data-bulk="expiry">' + ic("clock") + "Set expiry</button>" +
      '<button class="btn sm" type="button" data-bulk="transfer">' + ic("swap") + "Transfer</button>" +
      '<button class="btn sm" type="button" data-bulk="download">' + ic("download") + "Download</button>" +
      '<button class="btn sm danger" type="button" data-bulk="delete">' + ic("trash") + "Delete</button>" +
      '<button class="btn ghost sm" type="button" data-bulk="clear">Clear</button></div>';
  }

  // Visitors of the last 7 days, from each site's own analytics, for the
  // rows on screen: four at a time, once per page load.
  const localDate = (d) => d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
  let visQueue = [], visBusy = 0;
  function loadVisitors(rows) {
    for (const s of rows) if (!(s.id in S.visitors) && s.edit_url && !s.locked && !visQueue.includes(s)) visQueue.push(s);
    pump();
  }
  function pump() {
    while (visBusy < 4 && visQueue.length) {
      const s = visQueue.shift();
      if (s.id in S.visitors) continue;
      S.visitors[s.id] = null;
      visBusy++;
      const to = new Date(), from = new Date(Date.now() - 6 * WS.DAY);
      siteAPI(editIDOf(s.edit_url), "GET", "/analytics/overview?from=" + localDate(from) + "&to=" + localDate(to))
        .then((ov) => { S.visitors[s.id] = ov && ov.totals ? ov.totals.visitors : null; })
        .catch(() => {})
        .finally(() => {
          visBusy--;
          const cell = document.querySelector('[data-vis="' + s.id + '"]');
          if (cell && S.visitors[s.id] != null) cell.textContent = S.visitors[s.id].toLocaleString("en");
          pump();
        });
    }
  }

  // ---- actions on one site ----

  function siteMenu(btn, s) {
    const own = owned(s);
    menu(btn, [
      { label: "Open site", icon: "ext", href: primaryURL(s), blank: true },
      s.edit_url && { label: "Manage", icon: "edit", run: () => WS.openSite(s) },
      s.edit_url && { label: "Upload files", icon: "upload", run: () => WS.openSite(s, "files") },
      (own || !s.shared_paused) && { label: "Rename", icon: "edit", run: () => rename(s) },
      (own || !s.shared_paused) && { label: "Tags", icon: "tag", run: () => editTags(s) },
      s.edit_url && { label: "Add a domain", icon: "globe", run: () => WS.openSite(s, "addresses") },
      { sep: true },
      own && s.can_share && { label: "Share with an admin", icon: "share", run: () => share(s) },
      own && { label: "Transfer", icon: "swap", href: "/account/transfer?site=" + encodeURIComponent(s.id) },
      own && !s.locked && { label: "Reset edit password", icon: "key", run: () => rotate(s) },
      s.edit_url && !s.locked && { label: "Download ZIP", icon: "download", run: () => WS.download(editIDOf(s.edit_url), s.name || s.id).catch(fail) },
      s.role === "admin" && { label: "Leave this site", icon: "x", run: () => leave(s) },
      own && !s.locked && { sep: true },
      own && !s.locked && { label: "Delete…", icon: "trash", danger: true, run: () => del(s) },
    ]);
  }
  async function rename(s) {
    const v = await dialog({ title: s.name ? "Rename" : "Add a name", html: '<div class="field"><input type="text" name="name" maxlength="60" value="' + esc(s.name) + '" placeholder="e.g. Client docs" aria-label="Name"></div>',
      actions: [{ label: "Cancel", value: null }, { label: "Save", value: true, primary: true }] });
    if (!v) return;
    try { await post("/account/sites/" + s.id + "/name", { name: v.fields.name }); toast("Name saved."); reload(); } catch (err) { fail(err); }
  }
  async function editTags(s) {
    const opts = st().tags.map((t) => '<option value="' + esc(t.name) + '">').join("");
    const groups = st().groups;
    const html = '<div class="tags" style="gap:6px;margin-bottom:12px">' + (s.tags.length ? s.tags.map((t) => '<span class="tchip tag-' + esc(t.color) + '">' + esc(t.name) + ' <button type="button" data-rm="' + esc(t.name) + '" aria-label="Remove ' + esc(t.name) + '">×</button></span>').join("") : '<span class="faint">No tags yet.</span>') + "</div>" +
      '<div class="field"><input type="text" name="tag" maxlength="30" list="ws-tagopts" placeholder="New or existing tag" aria-label="Tag"><datalist id="ws-tagopts">' + opts + "</datalist>" +
      (groups.length ? '<select name="group" aria-label="Group, for a new tag" title="Used only when the tag is new"><option value="">No group</option>' + groups.map((g) => '<option value="' + esc(g.id) + '">' + esc(g.name) + "</option>").join("") + "</select>" : "") + "</div>";
    const p = dialog({ title: "Tags of " + label(s), body: s.role === "admin" ? "These tags are yours; the owner does not see them." : "", html, actions: [{ label: "Done", value: null }, { label: "Add tag", value: true, primary: true }] });
    document.querySelector(".dlg")?.addEventListener("click", async (e) => {
      const b = e.target.closest("[data-rm]");
      if (!b) return;
      WS.closeOverlay();
      try { await post("/account/sites/" + s.id + "/tags/remove", { tag: b.dataset.rm, back: "" }); await WS.load(); render(); editTags(siteByID(s.id)); } catch (err) { fail(err); }
    });
    const v = await p;
    if (!v || !v.fields.tag) return;
    try { await post("/account/sites/" + s.id + "/tags/add", { tag: v.fields.tag, group: v.fields.group || "", back: "" }); await WS.load(); render(); editTags(siteByID(s.id)); } catch (err) { fail(err); }
  }
  async function share(s) {
    const v = await dialog({ title: "Share " + label(s), body: "They manage the site with their own account. Deleting, transferring and sharing stay with you.",
      html: '<div class="field"><input type="email" name="email" maxlength="254" placeholder="Their account’s email" aria-label="Email"><input type="date" name="until" aria-label="Until, optional" style="flex:0 0 170px"></div>',
      actions: [{ label: "Cancel", value: null }, { label: "Invite", value: true, primary: true }] });
    if (!v) return;
    try { await post("/account/sites/" + s.id + "/share", { email: v.fields.email, until: v.fields.until }); toast("Invitation sent."); reload(); } catch (err) { fail(err); }
  }
  async function rotate(s) {
    if (!(await confirm("Reset the edit password of " + label(s) + "?", "A new password replaces the old one at once, and every upload token of the site ends. It is shown once.", "Reset password"))) return;
    try { await post("/account/sites/" + s.id + "/rotate", {}); } catch (err) { fail(err); }
  }
  async function leave(s) {
    if (!(await confirm("Leave " + label(s) + "?", "You stop managing it; " + (s.shared_by || "the owner") + " keeps it.", "Leave"))) return;
    try { await post("/account/sites/" + s.id + "/leave", {}); toast("You left the site."); reload(); } catch (err) { fail(err); }
  }
  async function del(s) {
    const doms = (s.domains || []).map((d) => d.Domain);
    const body = "Its " + s.files + (s.files === 1 ? " file" : " files") + (doms.length ? " and " + doms.join(", ") : "") + (s.git_bytes ? " and its Git repository" : "") + " go with it. There is no undo.";
    if (!(await confirm("Delete " + label(s) + "?", body, "Delete site", true))) return;
    try { await post("/account/sites/" + s.id + "/delete", {}); toast("Site deleted."); S.picked.delete(s.id); reload(); } catch (err) { fail(err); }
  }

  async function bulk(kind) {
    const sel = [...S.picked].map(siteByID).filter(Boolean);
    if (kind === "clear") { S.picked.clear(); render(); return; }
    if (kind === "transfer") {
      const mine = sel.filter(owned);
      if (!mine.length) { toast("Only sites you own can be transferred.", true); return; }
      location.href = "/account/transfer?" + mine.map((s) => "site=" + encodeURIComponent(s.id)).join("&");
      return;
    }
    if (kind === "download") {
      for (const s of sel.filter((x) => x.edit_url && !x.locked)) { try { await WS.download(editIDOf(s.edit_url), s.name || s.id); } catch (err) { fail(err); } }
      return;
    }
    if (kind === "tags") {
      const v = await dialog({ title: "Tags for " + sel.length + " sites", html: '<div class="field"><input type="text" name="tag" maxlength="30" list="ws-tagopts2" placeholder="Tag" aria-label="Tag"><datalist id="ws-tagopts2">' + st().tags.map((t) => '<option value="' + esc(t.name) + '">').join("") + "</datalist></div>",
        actions: [{ label: "Cancel", value: null }, { label: "Remove from all", value: "remove" }, { label: "Add to all", value: "add", primary: true }] });
      if (!v || !v.fields.tag) return;
      let n = 0;
      for (const s of sel) { try { await post("/account/sites/" + s.id + "/tags/" + v.value, { tag: v.fields.tag, back: "" }, { quiet: true }); n++; } catch (err) { fail(err); } }
      toast((v.value === "add" ? "Tagged " : "Untagged ") + n + " of " + sel.length + " sites.");
      reload();
      return;
    }
    if (kind === "expiry") {
      const mine = sel.filter((s) => owned(s) && s.edit_url && !s.locked);
      if (!mine.length) { toast("Only the owner sets an expiry.", true); return; }
      const v = await dialog({ title: "Expiry for " + mine.length + " sites", body: "Sites you only manage as an admin keep theirs.", html: '<div class="field"><input type="date" name="date" aria-label="Expiry date"></div>',
        actions: [{ label: "Cancel", value: null }, { label: "Clear the expiry", value: "clear" }, { label: "Set", value: "set", primary: true }] });
      if (!v) return;
      if (v.value === "set" && !v.fields.date) { toast("Pick a date first.", true); return; }
      const body = { expires_at: v.value === "set" ? new Date(v.fields.date + "T23:59:00").toISOString() : null };
      let n = 0;
      for (const s of mine) { try { await siteAPI(editIDOf(s.edit_url), "PUT", "", body); n++; } catch (err) { fail(new Error(label(s) + ": " + err.message)); } }
      toast("Expiry updated on " + n + " of " + mine.length + " sites.");
      reload();
      return;
    }
    if (kind === "delete") {
      const mine = sel.filter((s) => owned(s) && !s.locked);
      if (!mine.length) { toast("Only sites you own can be deleted.", true); return; }
      if (!(await confirm("Delete " + mine.length + " sites?", mine.map(label).join(", ") + " — with their files, domains and repositories. There is no undo.", "Delete " + mine.length + " sites", true))) return;
      let n = 0;
      for (const s of mine) { try { await post("/account/sites/" + s.id + "/delete", {}, { quiet: true }); n++; S.picked.delete(s.id); } catch (err) { fail(err); } }
      toast("Deleted " + n + " of " + mine.length + " sites.");
      reload();
    }
  }

  // ---- inbox ----
  function renderInbox() {
    const t = st().takeovers, inv = st().invites;
    view.innerHTML = head("Inbox", "Offers and invitations from other accounts. Nothing happens until you accept.") +
      t.map((o) => '<article class="offer"><div class="l"><span class="stamp s-warn">Transfer</span><div class="w">Accept by<b>' + esc(o.AcceptBy) + "</b>(UTC)</div></div>" +
        '<div class="r"><h3>' + esc(o.From) + " offers you " + esc(o.Count) + "</h3><p>Accepting takes all of them at once; declining leaves them with the sender.</p>" +
        "<ul>" + o.Sites.map((s) => "<li>" + ic("sites") + "<span><b>" + esc(s.Label) + "</b>" + (s.Domains || []).map((d) => ' <span class="mono dim" style="font-size:12px">' + esc(d) + "</span>").join("") +
          (s.Expires ? '<br><span class="faint">Expires on ' + esc(s.Expires) + " (set by the sender). Once it is yours, you can change it.</span>" : "") +
          ((s.Forms || []).length ? '<br><span class="faint">Its forms send to ' + s.Forms.map(esc).join(", ") + "; you can change that once it is yours.</span>" : "") + "</span></li>").join("") + "</ul>" +
        (o.Unavailable ? '<div class="banner err">This offer is not available right now.</div>' :
          (o.NeedsPlan ? '<div class="banner err">Your plan cannot take over sites. <a href="#/plan">See the plans</a></div>' : "") +
          "<ul>" + (o.Lines || []).map((l) => '<li><span class="' + (l.OK ? "ok" : "no") + '">' + ic(l.OK ? "check" : "x") + "</span><span><b>" + esc(l.Label) + "</b>" + (l.Site ? " · " + esc(l.Site) : "") + ": " + esc(l.Detail) + "</span></li>").join("") + "</ul>") +
        '<div class="field">' + (o.CanAccept ? '<button class="btn sm primary" type="button" data-accept="' + esc(o.ID) + '">Accept all ' + o.Sites.length + "</button>" : "") +
        '<button class="btn sm" type="button" data-decline="' + esc(o.ID) + '">Decline</button></div></div></article>').join("") +
      inv.map((i) => '<article class="offer"><div class="l"><span class="stamp s-info">Invitation</span>' + (i.Until ? '<div class="w">Admin until<b>' + esc(i.Until) + "</b></div>" : "") + "</div>" +
        '<div class="r"><h3>Manage “' + esc(i.Site) + '”</h3><p>' + esc(i.From) + " invites you to be an admin. You can change its files, addresses, forms and settings; deleting, transferring and sharing stay with the owner.</p>" +
        (i.NeedsPlan ? '<div class="banner err" style="margin-top:10px">Your plan cannot be an admin of a shared site. <a href="#/plan">See the plans</a></div>' : "") +
        (i.PlanUnknown ? '<div class="banner err" style="margin-top:10px">Not available right now, try again in a minute.</div>' : "") +
        '<div class="field" style="margin-top:12px">' + (i.CanAccept ? '<button class="btn sm primary" type="button" data-iaccept="' + esc(i.ID) + '">Accept</button>' : "") +
        '<button class="btn sm" type="button" data-idecline="' + esc(i.ID) + '">Decline</button></div></div></article>').join("") +
      (!t.length && !inv.length ? '<div class="panel"><div class="empty" style="padding:26px">Nothing waiting. Offers and invitations to ' + esc(st().account.email) + " appear here.</div></div>" : "") +
      '<section class="panel" id="adopt"><h3>Adopt an anonymous drop</h3><p class="d">Published a site without an account? Enter its address and its edit password to take it into this account. Its address and its edit password stay as they are.</p>' +
      '<form class="field" id="adoptform"><input type="text" name="site" required maxlength="2048" placeholder="https://…" aria-label="Site address" style="flex:2"><input type="password" name="password" required maxlength="256" placeholder="Edit password" aria-label="Edit password"><button class="btn sm primary" type="submit">Adopt</button></form></section>';
    view.onclick = async (e) => {
      const a = e.target.closest("[data-accept]");
      if (a) { const o = t.find((x) => x.ID === a.dataset.accept); submit("/account/transfer/" + encodeURIComponent(o.ID) + "/accept", { site: o.Sites.map((s) => s.ID) }); return; }
      const d = e.target.closest("[data-decline]");
      if (d && (await confirm("Decline this offer?", "The sites stay with the sender.", "Decline"))) { try { await post("/account/transfer/" + encodeURIComponent(d.dataset.decline) + "/decline", {}); toast("Declined."); reload(); } catch (err) { fail(err); } return; }
      const ia = e.target.closest("[data-iaccept]");
      if (ia) { try { await post("/account/invites/" + encodeURIComponent(ia.dataset.iaccept) + "/accept", {}); toast("Accepted: it is under Shared with me."); reload(); } catch (err) { fail(err); } return; }
      const idc = e.target.closest("[data-idecline]");
      if (idc) { try { await post("/account/invites/" + encodeURIComponent(idc.dataset.idecline) + "/decline", {}); toast("Declined."); reload(); } catch (err) { fail(err); } }
    };
    view.querySelector("#adoptform").addEventListener("submit", (e) => { e.preventDefault(); const f = new FormData(e.target); submit("/account/adopt", { site: f.get("site"), password: f.get("password") }); });
  }

  // ---- domains & zones ----
  function renderDomains() {
    const rows = st().sites.flatMap((s) => (s.domains || []).map((d) => ({ d, s })));
    const zones = st().zones, max = st().zones_max;
    view.innerHTML = head("Domains &amp; zones", rows.length + (rows.length === 1 ? " domain" : " domains") + " on " + new Set(rows.map((r) => r.s.id)).size + " sites" + (zones.length ? ", " + zones.length + (zones.length === 1 ? " zone." : " zones.") : "."),
      "") +
      '<div class="tblwrap" style="margin-bottom:22px"><table class="tbl"><thead><tr><th>Domain</th><th>Site</th><th>State</th><th class="hide-sm">What it does</th><th></th></tr></thead><tbody>' +
      (rows.length ? rows.map(({ d, s }) => '<tr class="row" data-open="' + s.id + '"><td><span class="mono" style="color:' + (d.Pending ? "rgba(245,184,77,.7)" : "var(--amber)") + '">' + esc(d.Domain) + "</span>" +
        (d.Pending ? '<div class="dnsbox" data-dns="' + esc(d.Domain) + '" data-edit="' + esc(editIDOf(s.edit_url)) + '"><span class="faint" style="font-size:12px">Loading the record…</span></div>' : "") + "</td>" +
        '<td><span class="sname" style="font-size:13px">' + esc(label(s)) + "</span></td>" +
        "<td>" + (d.Pending ? '<span class="st s-warn">' + ic("ring") + "Needs DNS</span>" : '<span class="st s-ok">' + ic("check") + "Verified</span>") + "</td>" +
        '<td class="hide-sm dim" style="font-size:12.5px">' + (d.Pending ? "Nothing until its record exists" : d.RedirectTo ? "Redirects to " + esc(d.RedirectTo) : "Serves the site") + "</td>" +
        '<td class="r"><button class="btn ghost sm" type="button" data-manage="' + s.id + '">Manage</button></td></tr>').join("") :
        '<tr><td colspan="5"><div class="empty">No custom domains yet. Add one on a site’s Addresses tab.</div></td></tr>') + "</tbody></table></div>" +
      (zones.length || max ? '<section class="panel"><h3>Zones <span class="faint mono" style="font-size:12px">' + zones.length + (max ? " of " + max : "") + "</span></h3>" +
        '<p class="d">A zone is a domain you own and point at Sitebin as a whole — <code>*.example.com</code>. Prove it once, and every name under it attaches to your sites at once, with no DNS record per name and no limit on how many. Nobody else can use a name in your zone.</p>' +
        (zones.length ? '<div class="lines" style="margin-bottom:12px">' + zones.map((z) => '<div class="line" style="align-items:flex-start">' + ic("globe") + '<div class="grow"><span class="a">*.' + esc(z.Zone) + "</span>" +
          (z.Verified ? '<span class="s' + (z.FailingSince ? " warn" : "") + '">' + (z.FailingSince ? "The TXT record is gone. Put it back, or the zone is released three days after it was first missed." : (z.Names || []).length + " name(s) in use" + (z.Names || []).map((n) => " · " + esc(n.Domain)).join("")) + "</span>" :
            '<span class="s warn">Pending' + ((z.Conflicts || []).length ? ": another account holds " + z.Conflicts.map(esc).join(", ") + " inside it; the zone verifies once that is gone." : ". Point *." + esc(z.Zone) + " here (a CNAME to this instance, or an A record), add the TXT record, then check. Unproven claims are dropped after 7 days.") + "</span>") +
          '<span class="s mono" style="margin-top:4px">TXT ' + esc(z.TXTName) + " = " + esc(z.TXTValue) + "</span></div>" +
          (z.Verified && !z.FailingSince ? '<span class="st s-ok">' + ic("check") + "Verified</span>" : '<button class="btn sm" type="button" data-zcheck="' + esc(z.Zone) + '">' + ic("refresh") + "Check now</button>") +
          '<button class="btn sm danger" type="button" data-zrm="' + esc(z.Zone) + '">Remove</button></div>').join("") + "</div>" : "") +
        (max ? '<form class="field" id="zoneform"><input type="text" name="zone" maxlength="253" placeholder="example.com" aria-label="Zone"><button class="btn sm primary" type="submit">' + ic("plus") + "Add zone</button></form>" +
          '<p class="faint" style="font-size:12px;margin-top:8px">Removing a zone detaches nothing at once: names that relied on it need their own DNS proof within three days.</p>' :
          '<p class="d" style="margin:0">Your current plan does not include zones. What you hold keeps working; new zones and new names need a plan that includes them.</p>') + "</section>" : "");
    view.onclick = async (e) => {
      const m = e.target.closest("[data-manage]");
      if (m) { WS.openSite(siteByID(m.dataset.manage), "addresses"); return; }
      const zc = e.target.closest("[data-zcheck]");
      if (zc) { try { await post("/account/zones", { zone: zc.dataset.zcheck }); await reload(); toast("Checked."); } catch (err) { fail(err); } return; }
      const zr = e.target.closest("[data-zrm]");
      if (zr) { if (await confirm("Remove the zone " + zr.dataset.zrm + "?", "Names that relied on it need their own DNS proof within three days.", "Remove zone", true)) { try { await post("/account/zones/" + encodeURIComponent(zr.dataset.zrm) + "/delete", {}); reload(); } catch (err) { fail(err); } } return; }
      const ck = e.target.closest("[data-dcheck]");
      if (ck) { try { await siteAPI(ck.dataset.edit, "POST", "/domains", { domain: ck.dataset.dcheck }); toast("Checked: see the result below."); renderDomains(); } catch (err) { fail(err); } return; }
      const row = e.target.closest("tr.row");
      if (row && !e.target.closest("button,a,.dnsbox")) WS.openSite(siteByID(row.dataset.open), "addresses");
    };
    view.querySelector("#zoneform")?.addEventListener("submit", async (e) => { e.preventDefault(); const z = new FormData(e.target).get("zone"); try { await post("/account/zones", { zone: z }); reload(); } catch (err) { fail(err); } });
    // the records of pending domains, from the site's own API
    view.querySelectorAll("[data-dns]").forEach(async (box) => {
      try {
        const site = await siteAPI(box.dataset.edit, "GET", "");
        const p = (site.pending_domains || []).find((x) => x.domain === box.dataset.dns);
        if (!p) { box.innerHTML = ""; return; }
        box.innerHTML = '<span class="k">Set one of these at your DNS provider:</span>' +
          '<div class="code"><span>TXT ' + esc(p.txt_name) + " = " + esc(p.txt_value) + "</span></div>" +
          (p.cname_target ? '<div class="code"><span>CNAME ' + esc(p.domain) + " → " + esc(p.cname_target) + "</span></div>" : "") +
          '<div class="field"><button class="btn sm" type="button" data-dcheck="' + esc(p.domain) + '" data-edit="' + esc(box.dataset.edit) + '">' + ic("refresh") + "Check now</button>" +
          (p.checked_at ? '<span class="faint" style="font-size:12px">Last check ' + esc(ago(p.checked_at)) + (p.check_result ? ": " + esc(p.check_result) : "") + "</span>" : "") + "</div>";
      } catch { box.innerHTML = ""; }
    });
  }

  // ---- tokens & agents ----
  function renderTokens() {
    const toks = st().tokens;
    view.innerHTML = head("API tokens &amp; agents", "A token creates sites and manages the ones this account owns — no per-site edit password needed. It cannot change your account.",
      '<button class="btn primary" type="button" data-new>' + ic("plus") + "Create token</button>") +
      '<div class="cols2"><div class="tblwrap"><table class="tbl"><thead><tr><th>Name</th><th>Token</th><th>Created</th><th></th></tr></thead><tbody>' +
      (toks.length ? toks.map((t) => "<tr><td><b>" + (t.name ? esc(t.name) : '<span class="faint">Unnamed token</span>') + '</b></td><td class="num">' + esc(t.prefix) + '…</td><td class="num">' + esc(fmtDate(t.created, true)) + '</td><td class="r"><button class="btn sm danger" type="button" data-revoke="' + esc(t.id) + '">Revoke</button></td></tr>').join("") :
        '<tr><td colspan="4"><div class="empty">No tokens yet.</div></td></tr>') + "</tbody></table></div>" +
      '<section class="panel"><h3>Connect an AI agent</h3><p class="d">Point any MCP client at this address. With a token, send it as <code>Authorization: Bearer &lt;token&gt;</code>.</p>' +
      '<div class="code"><span>' + esc(st().account.mcp_endpoint) + '</span><button class="btn ghost icon" type="button" data-copy="' + esc(st().account.mcp_endpoint) + '" aria-label="Copy">' + ic("copy") + "</button></div>" +
      '<p class="d" style="margin-top:12px">See <a href="https://sitebin.io/docs/mcp/" rel="noreferrer noopener" target="_blank">the MCP docs</a>.</p></section></div>';
    view.onclick = async (e) => {
      if (e.target.closest("[data-new]")) {
        const v = await dialog({ title: "Create an API token", body: "It is shown once. Name it after what uses it.", html: '<div class="field"><input type="text" name="name" maxlength="60" placeholder="what is it for?" aria-label="Name"></div>', actions: [{ label: "Cancel", value: null }, { label: "Create token", value: true, primary: true }] });
        if (!v) return;
        try { await post("/account/tokens", { name: v.fields.name }); reload(); } catch (err) { fail(err); }
        return;
      }
      const r = e.target.closest("[data-revoke]");
      if (r && (await confirm("Revoke this token?", "Anything that uses it stops working at once.", "Revoke", true))) { try { await post("/account/tokens/" + encodeURIComponent(r.dataset.revoke) + "/delete", {}); toast("Revoked."); reload(); } catch (err) { fail(err); } return; }
      const c = e.target.closest("[data-copy]");
      if (c && navigator.clipboard) navigator.clipboard.writeText(c.dataset.copy).then(() => toast("Copied."));
    };
  }

  // ---- plan & usage ----
  function renderPlan() {
    const p = st().plan, c = p.caps, sites = st().sites.filter(owned);
    const meter = (name, used, cap, txt, note) => { const pct = cap > 0 ? Math.min(100, used / cap * 100) : 0; const warn = cap > 0 && used / cap >= 0.85;
      return '<div class="meter' + (warn ? " warn" : "") + '"><div class="top"><b>' + name + '</b><span class="v">' + txt + "</span></div>" + (cap > 0 ? '<div class="bar"><i style="width:' + Math.max(1.5, pct) + '%"></i></div>' : "") + (warn && note ? '<div class="note">' + ic("alert") + note + "</div>" : "") + "</div>"; };
    const doms = sites.reduce((n, s) => n + (s.domains || []).length, 0), forms = sites.reduce((n, s) => n + s.forms, 0), bytes = sites.reduce((n, s) => n + s.bytes + s.git_bytes, 0);
    const lim = (n, unit) => (n > 0 ? n.toLocaleString("en") + (unit || "") : "unlimited");
    view.innerHTML = head("Plan &amp; usage", esc((st().account.tier || "Your") + " plan."),
      (p.portal ? '<button class="btn" type="button" data-portal>Manage subscription</button>' : "") + (st().account.cancel_url ? '<a class="btn ghost" href="' + esc(st().account.cancel_url) + '" rel="noopener">Cancel contracts here</a>' : "")) +
      '<div class="cols2"><section class="panel"><h3>What you use</h3><p class="d">Measured against what your plan allows.</p>' +
      meter("Sites", sites.length, c.sites, sites.length + (c.sites > 0 ? " of " + c.sites : "")) +
      meter("Storage", bytes, 0, fmtBytes(bytes) + " · each site up to " + (c.site_bytes > 0 ? fmtBytes(c.site_bytes) : "unlimited")) +
      meter("Custom domains", doms, 0, doms + " · up to " + lim(c.custom_domains) + " per site") +
      meter("Zones", st().zones.length, c.zones, st().zones.length + (c.zones > 0 ? " of " + c.zones : " · none on this plan"), "Every name under a zone attaches without a record.") +
      meter("Forms", forms, 0, forms + (c.forms > 0 ? " · up to " + c.forms + " per site" : "")) +
      meter("Container sites", sites.filter((s) => s.mode === "container").length, 0, sites.filter((s) => s.mode === "container").length + (c.containers > 0 ? " · up to " + c.containers + " running services" : " · not on this plan")) +
      "</section>" +
      '<section class="panel"><h3>' + esc(st().account.tier || "Plan") + "</h3><dl class=\"kv\">" +
      "<dt>Site lifetime</dt><dd>" + (c.expiry_days > 0 ? c.expiry_days + " days, renewed on every change" : "No expiry unless you set one") + "</dd>" +
      "<dt>Files per site</dt><dd>" + lim(c.files) + "</dd>" +
      "<dt>Included</dt><dd>" + [c.git && "Git", c.analytics && "analytics", c.webdav && "WebDAV", c.sharing && "sharing and transfers"].filter(Boolean).join(", ") + "</dd></dl>" +
      (p.tiers.length ? '<h3 style="margin-top:18px">Change plan</h3><div class="field" style="margin-top:8px">' + p.tiers.map((t) =>
        t.Current ? '<button class="btn sm primary" type="button" disabled>' + esc(t.Label) + (t.Price ? " · " + esc(t.Price) : "") + " (current)</button>" :
        t.Paid && p.checkout ? '<button class="btn sm" type="button" data-upgrade="' + esc(t.ID) + '">Upgrade to ' + esc(t.Label) + (t.Price ? " · " + esc(t.Price) : "") + "</button>" :
        !t.Paid && p.self_select ? '<button class="btn sm" type="button" data-tier="' + esc(t.ID) + '">Switch to ' + esc(t.Label) + "</button>" :
        '<button class="btn sm" type="button" disabled>' + esc(t.Label) + (t.Price ? " · " + esc(t.Price) : "") + "</button>").join("") + "</div>" : "") +
      "</section></div>";
    view.onclick = async (e) => {
      if (e.target.closest("[data-portal]")) { submit("/account/billing/portal", {}); return; }
      const u = e.target.closest("[data-upgrade]");
      if (u) { submit("/account/upgrade", { tier: u.dataset.upgrade }); return; }
      const t = e.target.closest("[data-tier]");
      if (t && (await confirm("Switch your plan?", "Your sites get the new plan's limits at once.", "Switch"))) { try { await post("/account/tier", { tier: t.dataset.tier }); toast("Plan changed."); reload(); } catch (err) { fail(err); } }
    };
  }

  // ---- account ----
  function renderAccount() {
    const a = st().account;
    view.innerHTML = head("Account", esc(a.email) + (a.tier ? " · " + esc(a.tier) + " plan" : "")) +
      '<div class="cols2"><div class="wcol">' +
      '<section class="panel"><h3>Interface</h3><p class="d">Your choice is kept for this account, on every device. Switch whenever you like.</p>' +
      '<div class="wseg"><button type="button" class="on">Workspace</button><button type="button" data-ws-classic>Classic</button></div></section>' +
      '<section class="panel"><h3>Sign-in and data</h3><p class="d">' + (a.account_url ? "Password, sign-in methods, sessions and data export live in your account console." : "Signed in as " + esc(a.email) + ".") + "</p>" +
      '<div class="field">' + (a.account_url ? '<a class="btn" href="' + esc(a.account_url) + '" rel="noopener">' + ic("ext") + "Manage account</a>" : "") +
      '<button class="btn" type="button" data-signout>' + ic("out") + "Sign out</button></div></section></div>" +
      '<section class="panel" style="border-color:rgba(242,109,109,.3)"><h3 style="color:var(--danger)">Delete account</h3>' +
      (a.stack_deletion ? '<p class="d">Your account is deleted where your sign-in lives. When it is, every site, token and record this instance holds for you is removed as well.</p><a class="btn danger" href="' + esc(a.deletion_url) + '" rel="noopener">Delete account</a>' :
        '<p class="d">Removes this account, every site it owns and every API token. You are asked to confirm on the next page.</p><button class="btn danger" type="button" data-delacc>Delete account and all sites</button>') +
      (a.cancel_url ? '<p class="d" style="margin-top:14px"><a href="' + esc(a.cancel_url) + '" rel="noopener">Cancel contracts here</a></p>' : "") + "</section></div>";
    view.onclick = (e) => {
      if (e.target.closest("[data-signout]")) submit("/account/logout", {});
      if (e.target.closest("[data-delacc]")) submit("/account/delete", {});
    };
  }

  // ---- the instance (operator) ----
  async function adminGet(path) {
    const res = await fetch("/account/api/admin/" + path, { headers: { Accept: "application/json" }, cache: "no-store" });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || "The console could not be read (" + res.status + ").");
    return data;
  }
  async function opBadge() {
    try {
      if (!S.reg || S.regStale) { S.reg = await adminGet("register"); S.regStale = false; }
      const f = S.reg.Figures, n = f.Scanner + f.Flagged + f.Reports;
      const b = document.getElementById("ws-op-badge");
      if (b) { b.textContent = n; b.classList.toggle("hidden", !n); }
      // the counts beside Register, Reports and Containers, as the mockup has them
      const put = (id, v) => { const e = document.getElementById(id); if (e) e.textContent = Number(v || 0).toLocaleString("en"); };
      put("ws-op-n-register", f.Sites);
      put("ws-op-n-reports", f.Reports);
      put("ws-op-n-containers", (S.reg.Rows || []).filter((r) => r.Mode === "container").length);
    } catch {}
  }
  function rowFor(id) { return (S.regRows || []).find((r) => r.ViewID === id) || (S.reg && S.reg.Rows.find((r) => r.ViewID === id)); }

  // The last complete backup, from the note `sitebin backup` leaves. A day
  // and a bit is the nightly rhythm; older than that, something stopped it.
  function backupLine(b) {
    if (!b) return '<div class="line">' + ic("download") + '<div class="grow"><b>Backups</b><span class="s">None recorded on this data. <span class="mono">sitebin backup</span> leaves a note when it completes.</span></div><span class="st s-off">' + ic("dot") + "None</span></div>";
    const age = Date.now() - new Date(b.at).getTime(), stale = age > 26 * 3600e3;
    const when = new Date(b.at), today = new Date().toDateString() === when.toDateString();
    const hm = String(when.getHours()).padStart(2, "0") + ":" + String(when.getMinutes()).padStart(2, "0");
    return '<div class="line">' + ic("download") + '<div class="grow"><b>Backups</b><span class="s">Last: ' + hm + " " + (today ? "today" : fmtDate(b.at, true)) + ", " + fmtBytes(b.bytes) + (b.skipped ? ", " + b.skipped + " skipped" : "") + '.</span></div><span class="st ' + (stale ? "s-warn" : "s-ok") + '">' + ic(stale ? "alert" : "check") + (stale ? ago(b.at) : "OK") + "</span></div>";
  }

  async function renderOp() {
    view.innerHTML = head("Instance", esc(location.hostname) + ", as the operator. Everything that needs a decision first.") + '<p class="loading">Reading the instance…</p>';
    let reg, rep, health;
    try { [reg, rep, health] = await Promise.all([adminGet("register"), adminGet("reports"), adminGet("health")]); } catch (err) { view.innerHTML = head("Instance", "") + '<div class="banner err">' + esc(err.message) + "</div>"; return; }
    S.reg = reg; S.regRows = reg.Rows; S.reports = rep; S.health = health;
    const f = reg.Figures, now = Date.now();
    const tri = [];
    for (const r of reg.Rows) {
      if (r.FindingLines && r.FindingLines.length && !(r.Locked && r.Held)) tri.push(["s-bad", "alert", r.Locked ? "Held by the scanner" : "Scanner findings", r.FindingLines[0], r.ViewID, "Review"]);
      else if (r.Violations > 0 && !r.Locked) tri.push(["s-warn", "shield", "CSP blocked " + r.Violations + " requests", (r.BlockedText || "") + (r.Origin === "mcp" ? " · created by an agent" : ""), r.ViewID, "Review"]);
    }
    for (const x of (rep.Rows || []).slice(0, 5)) tri.push(["s-bad", "flag", "Abuse report: " + x.Reason, x.Target + (x.Details ? " · “" + x.Details + "”" : ""), x.Site ? x.Site.ViewID : "", x.Site ? "Review" : "Reports"]);
    const held = reg.Rows.filter((r) => r.Held);
    if (held.length) tri.push(["s-info", "hold", held.length + (held.length === 1 ? " evidence hold" : " evidence holds"), held.map((r) => r.Name || r.ViewID).join(", "), held[0].ViewID, "Show"]);
    const used = health.data_total > 0 && health.data_free >= 0 ? health.data_total - health.data_free : -1;
    const pct = used >= 0 ? Math.round(used / health.data_total * 100) : -1;
    const diskText = used >= 0 ? fmtBytes(used) + " used of " + fmtBytes(health.data_total) + ". Site data " + fmtBytes(f.Bytes) + (health.backup ? "; the last backup was " + fmtBytes(health.backup.bytes) + " (backups on the same disk count too)" : "") + "." : "";
    if (pct >= 70) tri.push([pct >= 90 ? "s-bad" : "s-warn", "gauge", "Disk " + pct + " % full", "Site data " + fmtBytes(f.Bytes) + (health.backup ? ", last backup " + fmtBytes(health.backup.bytes) : "") + " on the same disk", "", "Details"]);
    const anonSoon = reg.Rows.filter((r) => !r.Owner && r.ExpiringNow).length;
    if (anonSoon) tri.push(["s-off", "clock", anonSoon + " anonymous drops expire this week", "Nothing to do: the sweep removes them", "", "Show"]);
    const word = { "s-bad": "Act now", "s-warn": "Look soon", "s-info": "Open case", "s-off": "FYI" };
    const L = health.license;
    view.innerHTML = head("Instance", esc(location.hostname) + ", as the operator. Everything that needs a decision first.") +
      '<section class="figures">' + [["Sites", f.Sites, ""], ["Account-owned", f.Owned, "owned"], ["Anonymous", f.Anonymous, "anon"], ["Stored", fmtBytes(f.Bytes), null], ["Flagged", f.Flagged + f.Scanner, "flagged", f.Flagged + f.Scanner ? "bad" : ""], ["Locked", f.Locked, "locked"]]
        .map(([k, v, flt, cls]) => (flt === null ? '<div class="fig">' : '<button class="fig ' + (cls || "") + '" type="button" data-regfilter="' + flt + '">') + '<span class="k">' + k + '</span><span class="v">' + esc(v) + "</span>" + (flt === null ? "</div>" : "</button>")).join("") + "</section>" +
      '<div class="cols2"><section class="panel" style="padding:6px 4px"><h3 style="padding:10px 14px 4px">Needs a decision</h3>' +
      (tri.length ? '<ul class="triage">' + tri.map(([c, i, t, sub, id, b], k) => '<li><span class="st ' + c + '">' + ic(i) + word[c] + '</span><div class="what"><b>' + esc(t) + "</b><span>" + esc(sub) + (id ? ' · <span class="mono">' + esc(id.slice(0, 10)) + "…</span>" : "") + '</span></div><button class="btn sm' + (c === "s-bad" ? " primary" : "") + '" type="button" data-tri="' + k + '">' + b + "</button></li>").join("") + "</ul>" :
        '<p class="allgood" style="padding:6px 14px 14px">' + ic("check") + "Nothing needs a decision.</p>") + "</section>" +
      '<section class="panel"><h3>Health</h3><p class="d">Checked just now.</p><div class="lines">' +
      '<div class="line">' + ic("shield") + '<div class="grow"><b>Licence</b><span class="s">' + esc(L.notice || (L.state === "licensed" ? (L.plan ? L.plan + ", " : "") + "until " + fmtDate(L.expires_at, true) + ". Renews daily." : L.state)) + '</span></div><span class="st ' + (L.restricted ? "s-bad" : L.state === "licensed" ? "s-ok" : "s-warn") + '">' + ic(L.state === "licensed" ? "check" : "alert") + esc(L.state) + "</span></div>" +
      '<div class="line">' + ic("gauge") + '<div class="grow"><b>Disk</b><span class="s">' + (used >= 0 ? fmtBytes(used) + " of " + fmtBytes(health.data_total) : health.data_free >= 0 ? fmtBytes(health.data_free) + " free" : "Not measured here") + "</span></div>" + (pct >= 0 ? '<span class="st ' + (pct >= 90 ? "s-bad" : pct >= 70 ? "s-warn" : "s-ok") + '">' + ic(pct >= 70 ? "alert" : "check") + (pct >= 70 ? pct + " %" : "OK") + "</span>" : "") + "</div>" +
      backupLine(health.backup) +
      (health.containers ? '<div class="line">' + ic("box") + '<div class="grow"><b>Containers</b><span class="s">' + health.running + " running, " + health.stopped + " stopped, " + health.failing + ' failing</span></div><span class="st ' + (health.failing ? "s-warn" : "s-ok") + '">' + ic(health.failing ? "alert" : "check") + (health.failing ? "Look" : "OK") + "</span></div>" : "") +
      '<div class="line">' + ic("mail") + '<div class="grow"><b>Mail</b><span class="s">' + (health.mailer && health.forms_mail ? "Forms and account mailer configured" : health.mailer ? "Account mailer configured; forms mail is off" : health.forms_mail ? "Forms mailer configured; no account mail goes out" : "Not configured: no mail goes out") + '</span></div><span class="st ' + (health.mailer ? "s-ok" : "s-off") + '">' + ic(health.mailer ? "check" : "dot") + (health.mailer ? "OK" : "Off") + "</span></div>" +
      "</div></section></div>";
    view.onclick = (e) => {
      const fl = e.target.closest("[data-regfilter]");
      if (fl) { S.regFilter = fl.dataset.regfilter; S.regQ = ""; S.regOpen = null; location.hash = "#/register"; return; }
      const t = e.target.closest("[data-tri]");
      if (t) {
        const x = tri[+t.dataset.tri];
        if (x[4]) { S.regOpen = x[4]; renderInsp(); }
        else if (x[5] === "Containers") location.hash = "#/containers";
        else if (x[5] === "Reports") location.hash = "#/reports";
        else if (x[5] === "Details") toast(diskText || "The disk could not be measured here.");
        else { S.regFilter = "expiring"; location.hash = "#/register"; }
      }
    };
    renderInsp();
  }

  // A lock as the side ticket states it: who placed it, and what the
  // retention will do (the date is the core's PurgeAt, never computed here).
  const LOCKBY = { scanner: "by the scanner", account: "with the owner's suspension" };
  const retention = (r) => {
    const l = r.Locked;
    if (l.Hold) return "Held as evidence since " + fmtDate(l.Hold.At, true) + (l.PurgeAt ? "; retention runs to " + fmtDate(l.PurgeAt, true) : "");
    if (l.PurgeAt) return (new Date(l.PurgeAt) <= new Date() ? "Purged at the next sweep" : "Purged on " + fmtDate(l.PurgeAt, true)) + " unless you hold it";
    return "Kept until you unlock it";
  };
  // The register's dates are the server's calendar days ("2026-12-16"): an
  // operator's expiry means through that day in the server's zone, so it is
  // written as that day, never converted to the browser's zone.
  const dayText = (t, year) => { const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(t || ""); return m ? fmtDate(new Date(+m[1], +m[2] - 1, +m[3]), year) : t || ""; };
  // "2026-11-06 23:12" (the register's server text) as the workspace writes dates.
  const isoWhen = (t) => { const m = /^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2})$/.exec(t || ""); return m ? fmtDate(m[1] + "T" + m[2], true) + ", " + m[2] : t; };
  // The trail's words: provenance records a surface and a credential.
  const human = (a) => (a || "").replace(/[-_]/g, " ").replace(/^./, (c) => c.toUpperCase());
  const SURF = { ui: "in the browser", api: "through the API", mcp: "by an agent (MCP)", "upload-token": "with an upload token", webdav: "over WebDAV", ftp: "over FTP", git: "by a Git push", dashboard: "from the dashboard" };
  const AUTH = { none: "anonymously", session: "signed in", token: "with an API token", oauth: "with OAuth", password: "with the edit password" };
  const createdHow = (t) => { const [sf, au] = (t || "").split(" · "); return [SURF[sf] || sf, AUTH[au] || au].filter(Boolean).join(", "); };

  async function renderRegister() {
    const flt = [["", "All"], ["owned", "Account-owned"], ["anon", "Anonymous"], ["mcp", "Agent (MCP)"], ["flagged", "Flagged"], ["locked", "Locked"], ["expiring", "Expiring 7d"]];
    const chipsHTML = (n) => flt.map(([k, l]) => '<button class="wchip' + (S.regFilter === k ? " on" : "") + '" type="button" data-f="' + k + '">' + l + (n ? " <small>" + (n[k] || 0) + "</small>" : "") + "</button>").join("");
    view.innerHTML = head("Register", "Every site on this instance: yours, other accounts’ and anonymous drops.") +
      '<form class="toolbar" id="regform"><label class="search" style="max-width:440px">' + ic("search") + '<input id="rq" type="search" placeholder="Name, view id, owner, domain — or an IP / CIDR" value="' + esc(S.regQ) + '" aria-label="Search the register"></label>' +
      '<div class="wchips">' + chipsHTML(S.regCounts) + "</div></form>" +
      '<div id="regbody"><p class="loading">Reading the register…</p></div>';
    view.querySelector("#regform").addEventListener("submit", (e) => { e.preventDefault(); S.regQ = view.querySelector("#rq").value.trim(); renderRegister(); });
    view.onclick = (e) => {
      const f = e.target.closest("[data-f]");
      if (f) { S.regFilter = f.dataset.f; renderRegister(); return; }
      const ip = e.target.closest("[data-ip]");
      if (ip) { e.preventDefault(); S.regQ = ip.dataset.ip; renderRegister(); return; }
      const row = e.target.closest("tr.row");
      if (row && !e.target.closest("a,button")) { S.regOpen = row.dataset.reg; S.confirm = null; view.querySelectorAll("tr.row").forEach((r) => r.classList.toggle("open", r.dataset.reg === S.regOpen)); renderInsp(); }
    };
    let reg;
    try { reg = await adminGet("register?q=" + encodeURIComponent(S.regQ) + "&filter=" + encodeURIComponent(S.regFilter)); } catch (err) { view.querySelector("#regbody").innerHTML = '<div class="banner err">' + esc(err.message) + "</div>"; return; }
    S.regRows = reg.Rows;
    S.regCounts = reg.Counts || null;
    view.querySelector("#regform .wchips").innerHTML = chipsHTML(S.regCounts);
    const ipPanel = reg.IP ? '<section class="panel" style="border-style:dashed;border-color:rgba(245,184,77,.45)"><h3>Seen from ' + esc(reg.IP.Query) + ": " + reg.IP.Sites + " site(s), " + (reg.IP.Accounts || []).length + " account(s)</h3>" +
      ((reg.IP.Accounts || []).length ? (reg.IP.Accounts || []).map((a) => '<div class="line" style="align-items:flex-start">' + ic("user") + '<div class="grow"><a href="/account/admin/accounts/' + encodeURIComponent(a.ID) + '"><b>' + esc(a.Email) + "</b></a>" + (a.Suspended ? '<span class="susp">Suspended</span>' : "") +
        (a.Rows || []).map((x) => '<span class="s mono">' + esc(x.When) + " · " + esc(x.Action) + (x.Site ? " " + esc(x.Site) : "") + (x.What ? " · " + esc(x.What) : "") + " · " + esc(x.IP) + "</span>").join("") + "</div></div>").join("") : '<p class="d">No account was seen from this address.</p>') + "</section>" : "";
    view.querySelector("#regbody").innerHTML = ipPanel + '<p class="faint" style="font-size:12px;margin:-4px 0 10px">' + reg.Shown + " shown.</p>" +
      '<div class="tblwrap"><table class="tbl"><thead><tr><th>Site</th><th>Owner</th><th>Status</th><th class="hide-md">Origin</th><th class="hide-md">Type</th><th class="hide-sm r">Size</th><th class="hide-md">Created</th><th>Expires</th></tr></thead><tbody>' +
      (reg.Rows.length ? reg.Rows.map((r) => { const t = regStatus(r); return '<tr class="row' + (S.regOpen === r.ViewID ? " open" : "") + (r.Locked ? " locked" : "") + '" data-reg="' + esc(r.ViewID) + '">' +
        '<td><span class="sname' + (r.Name ? "" : " unnamed") + '">' + (r.Name ? esc(r.Name) : "Untitled drop") + '</span><span class="saddr">' + esc(r.ViewID) + (r.DomainsText ? " · " + esc(r.DomainsText) : "") + "</span></td>" +
        '<td><span class="owner' + (r.Owner ? "" : " anon") + '">' + esc(r.OwnerLabel) + (r.OwnerSuspended ? '<span class="susp" title="' + esc(r.OwnerSuspended) + '">Suspended</span>' : "") + "</span></td>" +
        '<td><span class="st ' + t.cls + '">' + ic(t.icon) + esc(t.label) + "</span></td>" +
        '<td class="hide-md"><span class="orig' + (r.Origin === "mcp" ? " mcp" : "") + '">' + esc(r.Origin || "—") + "</span></td>" +
        '<td class="hide-md num">' + esc(WS.typeShort({ mode: r.Mode })) + "</td>" +
        '<td class="hide-sm num r" title="' + r.Files + (r.Files === 1 ? " file" : " files") + '">' + esc(r.SizeText) + "</td>" +
        '<td class="hide-md num">' + esc(dayText(r.CreatedText)) + "</td>" +
        "<td>" + (r.Locked ? '<span class="exp faint">held</span>' : r.ExpiryValue ? '<span class="exp' + (r.ExpiringNow ? " soon" : "") + '">' + esc(dayText(r.ExpiryValue)) + "</span>" : '<span class="exp faint">never</span>') + "</td></tr>"; }).join("") :
        '<tr><td colspan="8"><div class="empty">No site matches.</div></td></tr>') + "</tbody></table></div>";
    renderInsp();
  }
  function regStatus(r) {
    if (r.Locked) return { cls: "s-bad", icon: "lock", label: r.Held ? "Locked · hold" : "Locked" };
    if ((r.FindingLines || []).length || r.Violations > 0) return { cls: "s-warn", icon: "flag", label: "Flagged" };
    if (r.ExpiringNow) return { cls: "s-off", icon: "clock", label: "Expires soon" };
    return { cls: "s-ok", icon: "dot", label: "Live" };
  }

  // The register's side ticket: the operator's actions on one site.
  function renderInsp() {
    const r = S.regOpen && rowFor(S.regOpen);
    if (!r) { app.classList.remove("insp-open"); insp.innerHTML = ""; return; }
    app.classList.add("insp-open");
    const t = regStatus(r), prov = S.reg && S.reg.Prov ? S.reg.Prov[r.ViewID] : null;
    const days = S.health ? S.health.retention_days : (S.reg ? S.reg.RetentionDays : 0);
    let acts;
    if (S.confirm) {
      const c = S.confirm;
      const text = {
        lock: "Lock this site? It stops being served at once, its owner can no longer change, download or delete it, and it is kept past its expiry as evidence" + (days ? " — the sweep purges it " + days + " days after the lock unless you place an evidence hold." : "."),
        keep: "Keep this site locked as your own lock? A lock placed by a suspension is lifted when the owner is unsuspended; yours stays until you unlock it.",
        unlock: "Unlock this site? It is served again at once, its owner can change it again, and its expiry applies again — a date already past means the next sweep deletes it.",
        hold: "Place an evidence hold? Do it while a case about it is open: the site and its records are kept past the lock retention until you release the hold.",
        unhold: "Release the evidence hold? Do it once the case is closed: the lock retention applies again" + (r.PurgePast ? ", and it has passed — the next sweep purges the site." : "."),
        delete: (r.Locked ? "This site is LOCKED — held as evidence. " : "") + "Delete it permanently? Its " + r.Files + " file(s) and any custom domain go with it. This cannot be undone.",
      }[c];
      acts = '<div class="confirm">' + esc(text) + ((c === "lock" || c === "keep") ? '<div class="field" style="margin-top:10px"><input type="text" id="lockreason" maxlength="200" placeholder="Reason — shown to the owner" value="' + esc(r.Locked ? r.Locked.Reason : "") + '" aria-label="Reason"></div>' : "") +
        '<div class="row2"><button class="btn sm ' + (c === "unlock" || c === "hold" ? "primary" : "solid-danger") + '" type="button" data-do="' + c + '">Yes, ' + (c === "unhold" ? "release" : c) + "</button><button class=\"btn sm\" type=\"button\" data-do=\"cancel\">Cancel</button></div></div>";
    } else {
      acts = '<div class="field">' + (r.Locked ? '<button class="btn sm" type="button" data-ask="unlock">' + ic("lock") + "Unlock</button>" + (r.LockedByMachine ? '<button class="btn sm" type="button" data-ask="keep" title="Make it your own lock">Keep</button>' : "") +
        (r.Held ? '<button class="btn sm danger" type="button" data-ask="unhold">Release hold</button>' : days ? '<button class="btn sm" type="button" data-ask="hold">' + ic("hold") + "Hold as evidence</button>" : "") :
        '<button class="btn sm danger" type="button" data-ask="lock">' + ic("lock") + "Lock</button>") + "</div>" +
        '<form class="field" id="expform" style="margin-top:8px"><input type="date" name="expires" value="' + esc(r.ExpiryValue || "") + '" aria-label="Expiry"><button class="btn sm" type="submit">Set expiry</button></form>' +
        '<div style="margin-top:12px"><button class="btn sm danger" type="button" data-ask="delete">' + ic("trash") + "Delete for good</button></div>";
    }
    insp.innerHTML = '<article class="rticket"><div class="stub"><div class="top"><span class="stamp ' + t.cls + '">' + esc(t.label) + "</span>" + (r.Origin ? '<span class="stamp s-off">' + esc(r.Origin) + "</span>" : "") +
      '<button class="btn ghost icon close" type="button" data-close aria-label="Close">' + ic("x") + "</button></div>" +
      '<h2 class="' + (r.Name ? "" : "unnamed") + '">' + (r.Name ? esc(r.Name) : "Untitled drop") + "</h2>" +
      '<div class="addr"><a href="' + esc(r.ViewURL) + '" target="_blank" rel="noreferrer noopener">' + esc(r.ViewID) + "</a></div>" +
      '<div class="facts"><div><span class="k">Owner</span><span class="v" title="' + esc(r.OwnerLabel) + '">' + esc(r.Owner ? r.OwnerLabel.split("@")[0] : "Anonymous") + '</span></div><div><span class="k">Size</span><span class="v">' + esc(r.SizeText) + '</span></div><div><span class="k">Created</span><span class="v">' + esc(dayText(r.CreatedText)) + '</span></div><div><span class="k">Expires</span><span class="v">' + (r.Locked ? "Held" : r.ExpiryValue ? esc(dayText(r.ExpiryValue)) : "Never") + "</span></div></div></div>" +
      '<div class="perf" aria-hidden="true"></div><div class="tbody">' +
      (r.Locked ? '<div class="sec"><h4>' + ic("lock") + 'Lock</h4><dl class="kv"><dt>Since</dt><dd>' + esc(fmtDate(r.Locked.At, true)) + ", " + esc(LOCKBY[r.Locked.By] || "by the operator") + "</dd>" + (r.Locked.Reason ? "<dt>Reason</dt><dd>" + esc(r.Locked.Reason) + "</dd>" : "") + "<dt>Retention</dt><dd>" + esc(retention(r)) + "</dd></dl></div>" : "") +
      ((r.FindingLines || []).length ? '<div class="sec"><h4>' + ic("alert") + 'Findings<button class="btn sm" type="button" data-do="review">Dismiss</button></h4>' + r.FindingLines.map((x) => '<div class="finding bad">' + esc(x) + "</div>").join("") + (r.MoreFindings ? '<div class="finding">… and ' + r.MoreFindings + " more</div>" : "") + "</div>" : "") +
      (r.Violations ? '<div class="sec"><h4>' + ic("shield") + "CSP reports</h4><p>" + r.Violations + " blocked" + (r.Reporters ? " from " + r.Reporters + " source(s)" : "") + ": " + esc(r.BlockedText) + "</p></div>" : "") +
      ((r.DomainsText || (r.ShareLines || []).length) ? '<div class="sec"><h4>Addresses and sharing</h4><p>' + esc(r.DomainsText) + "</p>" + (r.ShareLines || []).map((x) => "<p>" + esc(x) + "</p>").join("") + "</div>" : "") +
      '<div class="sec"><h4>Owner</h4><div class="lines"><div class="line">' + ic("user") + '<div class="grow"><span class="a">' + esc(r.OwnerLabel) + "</span>" + (r.OwnerSuspended ? '<span class="s warn">' + esc(r.OwnerSuspended) + "</span>" : "") + "</div>" + (r.Owner ? '<a class="btn sm" href="/account/admin/accounts/' + encodeURIComponent(r.Owner) + '">Account</a>' : "") + "</div></div></div>" +
      (prov ? '<div class="sec"><h4>Trail<a class="btn ghost sm" href="' + esc(prov.Trail) + '">Full trail</a></h4><ul class="timeline">' +
        (prov.LastText ? (() => { const [act, ...when] = prov.LastText.split(" · "); return '<li class="' + ((r.FindingLines || []).length ? "hot" : "") + '"><b>' + esc(human(act)) + '</b><span class="w">' + esc(isoWhen(when.join(" · "))) + (prov.LastIP ? ' · <a href="#" data-ip="' + esc(prov.LastIP) + '" title="' + esc(prov.LastUA) + '">' + esc(prov.LastIP) + "</a>" : "") + "</span></li>"; })() : "") +
        (prov.CreatedIP || prov.CreatedText ? "<li><b>Created " + esc(createdHow(prov.CreatedText)) + '</b><span class="w">' + esc(dayText(r.CreatedText, true)) + (prov.CreatedIP ? ' · <a href="#" data-ip="' + esc(prov.CreatedIP) + '" title="' + esc(prov.CreatedUA) + '">' + esc(prov.CreatedIP) + "</a> · everything from this address" : "") + "</span></li>" : "") + "</ul></div>" :
        '<div class="sec"><h4>Trail<a class="btn ghost sm" href="/account/admin/sites/' + encodeURIComponent(r.ViewID) + '/trail">Full trail</a></h4></div>') +
      '<div class="sec"><h4>Operator actions</h4>' + acts + "</div></div></article>";
    insp.onclick = async (e) => {
      if (e.target.closest("[data-close]")) { S.regOpen = null; S.confirm = null; renderInsp(); view.querySelectorAll("tr.row.open").forEach((x) => x.classList.remove("open")); return; }
      const ip = e.target.closest("[data-ip]");
      if (ip) { e.preventDefault(); S.regQ = ip.dataset.ip; S.regFilter = ""; if (route().view === "register") renderRegister(); else location.hash = "#/register"; return; }
      const ask = e.target.closest("[data-ask]");
      if (ask) { S.confirm = ask.dataset.ask; renderInsp(); insp.querySelector("#lockreason, [data-do]")?.focus(); return; }
      const d = e.target.closest("[data-do]");
      if (!d) return;
      const what = d.dataset.do;
      if (what === "cancel") { S.confirm = null; renderInsp(); return; }
      const route2 = { lock: "lock", keep: "lock", unlock: "unlock", hold: "hold", unhold: "unhold", delete: "delete", review: "review" }[what];
      const fields = what === "lock" || what === "keep" ? { reason: insp.querySelector("#lockreason")?.value || "" } : {};
      try {
        await post("/account/admin/sites/" + encodeURIComponent(r.ViewID) + "/" + route2, fields);
        toast({ lock: "Locked: served to nobody, frozen for its owner.", keep: "The lock is yours now.", unlock: "Unlocked: served again.", hold: "Evidence hold placed.", unhold: "Hold released.", delete: "Deleted.", review: "Findings dismissed." }[what]);
        S.confirm = null;
        if (what === "delete") S.regOpen = null;
        S.regStale = true;
        refreshOperator();
      } catch (err) { fail(err); }
    };
    insp.querySelector("#expform")?.addEventListener("submit", async (e) => {
      e.preventDefault();
      try { await post("/account/admin/sites/" + encodeURIComponent(r.ViewID) + "/expiry", { expires: new FormData(e.target).get("expires") }); toast("Expiry updated."); S.regStale = true; refreshOperator(); } catch (err) { fail(err); }
    });
  }
  function refreshOperator() {
    const v = route().view;
    if (v === "register") renderRegister(); else if (v === "op") renderOp(); else if (v === "reports") renderReports(); else renderInsp();
  }

  async function renderReports() {
    view.innerHTML = head("Reports", "Filed through the report page and the API, newest first, kept for 14 days.") + '<p class="loading">Reading the reports…</p>';
    let rep;
    try { rep = await adminGet("reports"); } catch (err) { view.innerHTML = head("Reports", "") + '<div class="banner err">' + esc(err.message) + "</div>"; return; }
    S.reports = rep;
    view.innerHTML = head("Reports", "Filed through the report page and the API, newest first, kept for 14 days.") +
      '<section class="panel" style="padding:4px">' + ((rep.Rows || []).length ? '<ul class="triage">' + rep.Rows.map((x, k) => '<li><span class="st s-warn">' + ic("flag") + esc(x.Reason) + "</span>" +
        '<div class="what"><b class="mono" style="font-size:12.5px;color:var(--amber);overflow-wrap:anywhere">' + esc(x.Target) + "</b><span>" + esc(x.TimeText) + " · " + esc(x.Via) + (x.Source ? " · " + esc(x.Source) : "") + (x.Details ? " · “" + esc(x.Details) + "”" : "") + (x.Contact ? ' · <a href="mailto:' + esc(x.Contact) + '">' + esc(x.Contact) + "</a>" : "") + "</span>" +
        "<span>" + (x.Site ? "site " + esc(x.Site.ViewID) + " · " + esc(x.OwnerLabel) + (x.LockText ? ' · <b style="color:var(--danger)">LOCKED</b> ' + esc(x.LockText) : "") : x.ViewID ? "site " + esc(x.ViewID) + " no longer exists" : "not resolved to a site on this instance") + "</span></div>" +
        (x.Site ? (x.LockText ? '<button class="btn sm" type="button" data-rsite="' + esc(x.Site.ViewID) + '">Open</button>' : '<div class="field" style="flex-wrap:nowrap"><input type="text" data-reason="' + k + '" value="' + esc(x.LockReason) + '" aria-label="Reason, shown to the owner" style="min-width:180px"><button class="btn sm danger" type="button" data-rlock="' + k + '">Lock site</button></div>') : "") + "</li>").join("") + "</ul>" :
        '<div class="empty">No reports on file.</div>') + "</section>";
    view.onclick = async (e) => {
      const o = e.target.closest("[data-rsite]");
      if (o) { S.regOpen = o.dataset.rsite; S.regQ = o.dataset.rsite; location.hash = "#/register"; return; }
      const l = e.target.closest("[data-rlock]");
      if (!l) return;
      const x = rep.Rows[+l.dataset.rlock];
      const reason = view.querySelector('[data-reason="' + l.dataset.rlock + '"]').value;
      if (!(await confirm("Lock this site?", "It stops being served at once and is kept as evidence. The reason is shown to its owner.", "Lock site", true))) return;
      try { await post("/account/admin/sites/" + encodeURIComponent(x.Site.ViewID) + "/lock?return=reports", { reason }); toast("Locked."); renderReports(); } catch (err) { fail(err); }
    };
  }

  async function renderContainers() {
    view.innerHTML = head("Containers", "Every container project on this instance, against its limits.") + '<p class="loading">Reading the projects…</p>';
    let c;
    if (!st().account.containers) { view.innerHTML = head("Containers", "This instance runs no container sites.") + '<section class="panel"><div class="empty">Container sites are not enabled on this instance.</div></section>'; return; }
    try { c = await adminGet("containers"); } catch (err) { view.innerHTML = head("Containers", "") + '<div class="banner err">' + esc(err.message) + "</div>"; return; }
    const cell = (l) => '<span class="' + (l.Own ? "" : "faint") + '">' + esc(l.Value) + "</span>";
    view.innerHTML = head("Containers", esc(c.Projects + " projects, " + c.Running + " running" + (c.Host ? " · host " + c.Host : "")), '<button class="btn" type="button" data-refresh>' + ic("refresh") + "Refresh</button>") +
      '<section class="figures" style="grid-template-columns:repeat(4,minmax(0,1fr))">' +
      [["Memory used", c.MemUsed || "—"], ["CPU used", c.CPUUsed || "—"], ["Reserved", (c.MemReserved || "—") + (c.CPUReserve ? " · " + c.CPUReserve : "")], ["Traffic in / out", (c.TrafficIn || "—") + " / " + (c.TrafficOut || "—")]].map(([k, v]) => '<div class="fig"><span class="k">' + k + '</span><span class="v" style="font-size:18px">' + esc(v) + "</span></div>").join("") + "</section>" +
      (c.AsOf ? '<p class="faint" style="font-size:12px;margin:-12px 0 14px">Measured ' + esc(c.AsOf) + ".</p>" : "") +
      '<section class="panel"><h3>Instance defaults</h3><p class="d">In effect: ' + esc(c.Defaults) + ". Empty fields use the environment (" + esc(c.EnvMemory) + " MB, " + esc(c.EnvCPUs) + " CPU, " + esc(c.EnvPids) + " processes). A change never restarts an app.</p>" +
      (c.DefaultsErr ? '<div class="banner err">' + esc(c.DefaultsErr) + "</div>" : "") +
      '<form class="field" id="defform"><input type="text" name="memory_mb" value="' + esc(c.SavedMemory) + '" placeholder="' + esc(c.EnvMemory) + '" aria-label="Memory in MB" style="max-width:150px"><span class="faint">MB</span><input type="text" name="cpus" value="' + esc(c.SavedCPUs) + '" placeholder="' + esc(c.EnvCPUs) + '" aria-label="CPUs" style="max-width:110px"><span class="faint">CPU</span><input type="text" name="pids" value="' + esc(c.SavedPids) + '" placeholder="' + esc(c.EnvPids) + '" aria-label="Processes" style="max-width:110px"><span class="faint">processes</span><button class="btn sm primary" type="submit">Save defaults</button></form></section>' +
      ((c.Rows || []).length ? c.Rows.map((p, k) => '<section class="panel"><h3>' + esc(p.Name || p.ViewID) + ' <span class="faint mono" style="font-size:12px;font-weight:400">' + esc(p.Owner) + " · " + esc(p.Status) + "</span></h3>" +
        (p.Message ? '<p class="d">' + esc(p.Message) + "</p>" : "") + (p.UpdateErr ? '<div class="banner err">' + esc(p.UpdateErr) + "</div>" : "") + (p.Web ? '<p class="d">Web traffic: ' + esc(p.Web) + "</p>" : "") +
        '<div class="tblwrap"><table class="tbl"><thead><tr><th>Service</th><th class="hide-md">Image</th><th>State</th><th>Memory</th><th>CPU</th><th>Processes</th><th class="hide-md">Usage</th><th>Own limits</th></tr></thead><tbody>' +
        (p.Services || []).map((s, j) => '<tr><td class="mono">' + esc(s.Name) + (s.Missing ? ' <span class="faint">(gone)</span>' : "") + '</td><td class="hide-md num">' + esc(s.Image) + '</td><td><span class="st ' + (s.State === "running" ? "s-ok" : "s-off") + '">' + ic(s.State === "running" ? "dot" : "stop") + esc(s.State || "—") + "</span>" + (s.Updating ? '<br><span class="faint" style="font-size:11px">updating: ' + esc(s.Updating) + "</span>" : "") + "</td><td>" + cell(s.Memory) + "</td><td>" + cell(s.CPUs) + "</td><td>" + cell(s.Pids) + '</td><td class="hide-md num">' + esc(s.Usage || "—") + "</td>" +
          '<td><form class="field" data-limits="' + k + ":" + j + '" style="flex-wrap:nowrap"><input type="text" name="memory_mb" value="' + esc(s.OwnMemory) + '" placeholder="MB" style="min-width:0;width:70px" aria-label="Memory"><input type="text" name="cpus" value="' + esc(s.OwnCPUs) + '" placeholder="CPU" style="min-width:0;width:60px" aria-label="CPUs"><input type="text" name="pids" value="' + esc(s.OwnPids) + '" placeholder="procs" style="min-width:0;width:64px" aria-label="Processes"><button class="btn sm" type="submit">Set</button>' + (s.HasOwn ? '<button class="btn sm ghost" type="button" data-clear="' + k + ":" + j + '">Defaults</button>' : "") + "</form></td></tr>").join("") +
        "</tbody></table></div></section>").join("") : '<section class="panel"><div class="empty">No container projects.</div></section>');
    view.onclick = async (e) => {
      if (e.target.closest("[data-refresh]")) { renderContainers(); return; }
      const cl = e.target.closest("[data-clear]");
      if (cl) { const [k, j] = cl.dataset.clear.split(":"); const p = c.Rows[k]; try { await post("/account/admin/containers/sites/" + encodeURIComponent(p.ViewID) + "/limits", { service: p.Services[j].Name, clear: "1" }); toast("The defaults apply again."); renderContainers(); } catch (err) { fail(err); } }
    };
    view.querySelector("#defform").addEventListener("submit", async (e) => { e.preventDefault(); const f = Object.fromEntries(new FormData(e.target)); try { await post("/account/admin/containers/defaults", f); toast("Defaults saved."); renderContainers(); } catch (err) { fail(err); } });
    view.querySelectorAll("[data-limits]").forEach((form) => form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const [k, j] = form.dataset.limits.split(":"); const p = c.Rows[k];
      const f = Object.fromEntries(new FormData(form)); f.service = p.Services[j].Name;
      try { await post("/account/admin/containers/sites/" + encodeURIComponent(p.ViewID) + "/limits", f); toast("Limits saved; the app keeps running."); renderContainers(); } catch (err) { fail(err); }
    }));
  }
})();
