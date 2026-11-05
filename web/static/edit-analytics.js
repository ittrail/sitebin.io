// The Analytics tab of the edit page: a site's statistics as Umami shows them
// -- an overview with its chart and tables, events, funnels, goals, realtime
// -- and the settings that decide what is counted. Loaded after edit.js,
// which owns api(), authHeaders(), site, tabs, toast() and fmtBytes().
//
// DISPLAY SAFETY. Every value in a record is chosen by the site's visitors:
// paths, titles, referrers, event names and data, UTM values, and on a forged
// or imported record the country, browser and language as well. It enters
// the DOM only through textContent / createTextNode (the el() helper below),
// never through innerHTML, and no href is built from it except an https
// referrer link, built through URL (referrerURL) and opened with
// rel="noopener noreferrer nofollow".
//
// STATE lives in the hash, so a view can be linked:
//   #analytics/<subtab>?range=7d&f=dim:value&f=...&cmp=1
//   #analytics/<subtab>?from=YYYY-MM-DD&to=YYYY-MM-DD&...   (a custom range)
// The tabs mechanism (edit.js) picks the tab from "#analytics"; this file
// owns the rest of the hash. See "The tab" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md.
"use strict";

const analyticsTab = (() => {
  const SUBTABS = [
    ["overview", "Overview"], ["events", "Events"], ["funnels", "Funnels"],
    ["goals", "Goals"], ["realtime", "Realtime"], ["settings", "Settings"],
  ];
  // Every range is local dates in the site's time zone, except the last 24
  // hours, which the API answers itself.
  const RANGES = [
    ["today", "Today"], ["last24h", "Last 24 hours"], ["yesterday", "Yesterday"],
    ["week", "This week"], ["7d", "Last 7 days"], ["month", "This month"],
    ["30d", "Last 30 days"], ["90d", "Last 90 days"], ["year", "This year"],
    ["12m", "Last 12 months"], ["custom", "Custom"],
  ];
  const DEFAULT_RANGE = "7d";
  const TOP = 10;          // rows a table shows before "More"
  const PAGE = 100;        // rows a page of "More" shows
  const POLL_MS = 10000;   // the realtime refresh
  const MAX_FILTERS = 20;  // the API's cap
  const MAX_SPAN = 760;    // days a range spans at most, unless the retention is shorter
  const RELOAD_MS = 60000; // re-showing a view this soon keeps what is shown
  const UTM = ["utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"];
  // Rows the engine writes itself (internal/analytics): the visits without a
  // referrer, and what a day summary left out.
  const DIRECT = "(direct)", OTHER = "(other)";

  const DIM_LABELS = {
    path: "Page", entry: "Entry page", exit: "Exit page", title: "Title", referrer: "Referrer",
    channel: "Channel", utm_source: "UTM source", utm_medium: "UTM medium", utm_campaign: "UTM campaign",
    utm_term: "UTM term", utm_content: "UTM content", click_id: "Click ID", browser: "Browser", os: "OS",
    device: "Device", screen: "Screen", country: "Country", region: "Region", city: "City",
    language: "Language", address: "Address", tag: "Tag", event: "Event",
  };

  // The overview's tables, in Umami's groups. "utm" is the UTM table, whose
  // parameter is picked with its own selector.
  const GROUPS = [
    { id: "pages", title: "Pages", picks: [["path", "Pages"], ["entry", "Entry"], ["exit", "Exit"], ["title", "Titles"]] },
    { id: "sources", title: "Sources", picks: [["referrer", "Referrers"], ["channel", "Channels"], ["utm", "UTM"]] },
    { id: "env", title: "Environment", picks: [["browser", "Browsers"], ["os", "OS"], ["device", "Devices"], ["screen", "Screens"]] },
    { id: "location", title: "Location", picks: [["country", "Countries"], ["region", "Regions"], ["city", "Cities"]] },
    { id: "languages", title: "Languages", picks: [["language", "Languages"]] },
    { id: "events", title: "Events", picks: [["event", "Events"]] },
  ];

  const S = {
    state: null,     // GET /analytics
    built: false,
    sub: "overview",
    range: DEFAULT_RANGE,
    from: "", to: "", // a custom range
    filters: [],     // [{dim, value}]
    cmp: false,
    lastHash: "",    // the view last shown, put back when the tab is clicked again
    gen: 0,          // the newest render; an older render's answers are dropped
    key: "",         // the view the body shows, and when it was loaded
    loadedAt: 0,
    totals: null,    // the overview's totals, for the tables' share bars
    pick: { pages: "path", sources: "referrer", env: "browser", location: "country", languages: "language", events: "event" },
    utm: "utm_source",
    expanded: null,  // {group, offset}: the table opened with "More"
    cards: {},       // group id -> its card in the overview
    event: "",       // the event whose data the Events sub-tab shows
    eventOffset: 0,  // the Events sub-tab's page...
    eventKey: "",    // ...within this range and these filters
    editing: null,   // {kind: "funnel"|"goal", id}: the editor open, id "" for a new one
    poll: null,      // the realtime timer
    pollGen: 0,
  };

  const q = (id) => $(id);

  // ---- small helpers ----

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  function button(text, cls, onClick) {
    const b = el("button", "btn small" + (cls ? " " + cls : ""), text);
    b.type = "button";
    b.addEventListener("click", onClick);
    return b;
  }

  const SVG = "http://www.w3.org/2000/svg";
  function svgEl(tag, attrs, cls) {
    const e = document.createElementNS(SVG, tag);
    for (const k in attrs) e.setAttribute(k, attrs[k]);
    if (cls) e.setAttribute("class", cls);
    return e;
  }

  async function copy(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast("Copied");
    } catch {
      toast("Copy failed -- select the text instead", true);
    }
  }

  function cmdBlock(text, label) {
    const wrap = el("div", "an-cmd");
    wrap.appendChild(el("pre", "", text));
    wrap.appendChild(button(label || "Copy", "", () => copy(text)));
    return wrap;
  }

  function switchEl(label, checked, onChange) {
    const l = el("label", "switch");
    const i = el("input");
    i.type = "checkbox";
    i.checked = checked;
    l.append(i, el("span", "knob"), el("span", "lbl", label));
    if (onChange) i.addEventListener("change", () => onChange(i.checked, i));
    return l;
  }

  function fmtNum(n) { return (n || 0).toLocaleString(); }
  const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
  function fmtBig(n) { return n >= 10000 ? compact.format(n) : fmtNum(n); }
  function fmtPct(f) {
    const p = (f || 0) * 100;
    return (p > 0 && p < 1 ? "<1" : p < 10 && p % 1 ? p.toFixed(1) : Math.round(p)) + "%";
  }
  function fmtDuration(s) {
    s = Math.round(s || 0);
    if (s < 60) return s + "s";
    if (s < 3600) return Math.floor(s / 60) + "m " + String(s % 60).padStart(2, "0") + "s";
    return Math.floor(s / 3600) + "h " + String(Math.floor(s / 60) % 60).padStart(2, "0") + "m";
  }

  function tz() { return (S.state && S.state.effective_timezone) || "UTC"; }

  // fmtTime writes an instant in the site's zone, which is where its days are.
  function fmtTime(t, opts) {
    const d = new Date(t);
    if (isNaN(d)) return "";
    try { return d.toLocaleString(undefined, Object.assign({ timeZone: tz() }, opts)); } catch { return d.toLocaleString(undefined, opts); }
  }
  function fmtBucket(t, unit, long) {
    switch (unit) {
      case "minute": return fmtTime(t, { hour: "2-digit", minute: "2-digit" });
      case "hour": return fmtTime(t, long ? { weekday: "short", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" } : { hour: "2-digit", minute: "2-digit" });
      case "month": return fmtTime(t, { month: long ? "long" : "short", year: "numeric" });
      default: return fmtTime(t, long ? { weekday: "short", month: "short", day: "numeric", year: "numeric" } : { month: "short", day: "numeric" });
    }
  }

  let regionNames = null, languageNames = null;
  try { regionNames = new Intl.DisplayNames(["en"], { type: "region" }); } catch {}
  try { languageNames = new Intl.DisplayNames(["en"], { type: "language" }); } catch {}
  function named(dn, v) {
    try {
      const n = dn && dn.of(v);
      return n && n !== v ? n + " (" + v + ")" : v;
    } catch { return v; }
  }

  function dimLabel(d) {
    if (DIM_LABELS[d]) return DIM_LABELS[d];
    if (d.startsWith("query:")) return "Query " + d.slice(6);
    if (d.startsWith("data:")) return "Event data " + d.slice(5);
    return d;
  }
  function knownDim(d) { return Object.prototype.hasOwnProperty.call(DIM_LABELS, d) || /^(query|data):[^:]+$/.test(d); }

  // display is how a table shows a value: a country or a language by its
  // name (with the code), the engine's own rows in words, the rest as stored.
  function display(dim, v) {
    if (v === OTHER) return "Other";
    if (dim === "referrer" && v === DIRECT) return "Direct";
    if (dim === "country") return named(regionNames, v);
    if (dim === "language") return named(languageNames, v);
    if (dim === "device" && /^[a-z]+$/.test(v)) return v.charAt(0).toUpperCase() + v.slice(1);
    return v;
  }

  // referrerURL is the https link for a stored referrer (host + path), or ""
  // when it is anything else: a referrer that kept a scheme (android-app://,
  // and whatever a forged record holds, javascript: included) is never a link.
  function referrerURL(v) {
    if (!/^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:\d{1,5})?(\/|$)/i.test(v) || v.indexOf(".") < 0) return "";
    try {
      const u = new URL("https://" + v);
      return u.protocol === "https:" && u.hostname.includes(".") ? u.href : "";
    } catch { return ""; }
  }

  function extLink(href, label) {
    const a = el("a", "an-ext", label || "↗");
    a.href = href;
    a.target = "_blank";
    a.rel = "noopener noreferrer nofollow";
    a.title = "Open " + href;
    return a;
  }

  // ---- dates and the query ----

  function todayIn(zone) {
    try {
      const parts = new Intl.DateTimeFormat("en-US", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(new Date());
      const get = (t) => parts.find((p) => p.type === t).value;
      return get("year") + "-" + get("month") + "-" + get("day");
    } catch { return new Date().toISOString().slice(0, 10); }
  }
  const DAY = 86400000;
  const dayNum = (s) => Date.UTC(+s.slice(0, 4), +s.slice(5, 7) - 1, +s.slice(8, 10)) / DAY;
  const dayStr = (n) => new Date(n * DAY).toISOString().slice(0, 10);
  const addDays = (s, n) => dayStr(dayNum(s) + n);
  const isDate = (s) => /^\d{4}-\d{2}-\d{2}$/.test(s || "") && !isNaN(dayNum(s));

  // maxSpan is the longest range the API answers, in days: the retention,
  // at most 760 (a retention of 0 keeps every day).
  function maxSpan() {
    const r = (S.state && S.state.retention_days) || 0;
    return r > 0 && r < MAX_SPAN ? r : MAX_SPAN;
  }

  // clampSpan moves a range's first day up so that it spans at most
  // maxSpan() days: "This year" or "Last 12 months" on a site kept for 30
  // days asks for the 30 days there are, never for a range the API refuses.
  function clampSpan(per) {
    if (!per.from || !per.to) return per;
    const earliest = addDays(per.to, -(maxSpan() - 1));
    return per.from < earliest ? { from: earliest, to: per.to } : per;
  }

  // period is the API's range for the view: range=last24h, or from and to.
  function period() {
    return S.range === "last24h" ? { range: "last24h" } : clampSpan(datesOf(S.range));
  }

  // datesOf is a range's first and last local date, before clampSpan.
  function datesOf(range) {
    if (range === "custom") return { from: S.from, to: S.to };
    const t = todayIn(tz());
    switch (range) {
      case "today": return { from: t, to: t };
      case "yesterday": { const y = addDays(t, -1); return { from: y, to: y }; }
      case "week": return { from: addDays(t, -((new Date(dayNum(t) * DAY).getUTCDay() + 6) % 7)), to: t }; // from Monday
      case "month": return { from: t.slice(0, 8) + "01", to: t };
      case "30d": return { from: addDays(t, -29), to: t };
      case "90d": return { from: addDays(t, -89), to: t };
      case "year": return { from: t.slice(0, 5) + "01-01", to: t };
      case "12m": return { from: new Date(Date.UTC(+t.slice(0, 4), +t.slice(5, 7) - 12, 1)).toISOString().slice(0, 10), to: t };
      default: return { from: addDays(t, -6), to: t };
    }
  }

  function query(extra, noFilters) {
    const p = new URLSearchParams();
    const per = period();
    for (const k in per) p.set(k, per[k]);
    if (!noFilters) for (const f of S.filters) p.append("filter", f.dim + ":" + f.value);
    for (const k in extra || {}) if (extra[k] !== undefined && extra[k] !== "") p.set(k, extra[k]);
    return "?" + p.toString();
  }

  // parseFilter splits dim:value as the API does: a query:<name> or
  // data:<key> dimension holds a colon of its own.
  function parseFilter(s) {
    let i = s.indexOf(":");
    if (i < 0) return null;
    let dim = s.slice(0, i);
    if (dim === "query" || dim === "data") {
      const j = s.indexOf(":", i + 1);
      if (j < 0) return null;
      dim = s.slice(0, j);
      i = j;
    }
    return knownDim(dim) ? { dim, value: s.slice(i + 1) } : null;
  }

  // ---- the hash ----

  function readHash() {
    const m = /^#analytics(?:\/([a-z]*))?(?:\?(.*))?$/.exec(location.hash);
    const p = new URLSearchParams((m && m[2]) || "");
    S.sub = m && SUBTABS.some((t) => t[0] === m[1]) ? m[1] : "overview";
    const from = p.get("from"), to = p.get("to"), range = p.get("range");
    if (isDate(from) && isDate(to) && from <= to) {
      S.range = "custom"; S.from = from; S.to = to;
    } else {
      S.range = RANGES.some((r) => r[0] === range && r[0] !== "custom") ? range : DEFAULT_RANGE;
    }
    S.filters = [];
    for (const raw of p.getAll("f").slice(0, MAX_FILTERS)) {
      const f = parseFilter(raw);
      if (f && !S.filters.some((x) => x.dim === f.dim)) S.filters.push(f);
    }
    S.cmp = p.get("cmp") === "1";
  }

  function hashOf() {
    const p = new URLSearchParams();
    if (S.range === "custom") { p.set("from", S.from); p.set("to", S.to); } else p.set("range", S.range);
    for (const f of S.filters) p.append("f", f.dim + ":" + f.value);
    if (S.cmp) p.set("cmp", "1");
    return "#analytics/" + S.sub + "?" + p.toString();
  }

  // navigate shows the view S now describes, and puts it in the URL: push
  // for the reader's own steps, so Back goes back through them.
  function navigate(push) {
    const h = hashOf();
    S.lastHash = h;
    if (location.hash !== h) {
      const url = location.pathname + location.search + h;
      if (push) history.pushState(null, "", url); else history.replaceState(null, "", url);
    }
    render(true);
  }

  function addFilter(dim, value) {
    const rest = S.filters.filter((f) => f.dim !== dim); // a dimension holds one value at a time
    if (rest.length >= MAX_FILTERS) { toast("At most " + MAX_FILTERS + " filters", true); return; }
    S.filters = rest.concat([{ dim, value }]);
    S.expanded = null;
    if (S.sub !== "overview" && S.sub !== "events") S.sub = "overview";
    navigate(true);
  }

  function removeFilter(i) {
    S.filters = S.filters.filter((_, j) => j !== i);
    S.expanded = null;
    navigate(true);
  }

  // ---- the frame: notice, header, sub-tabs ----

  function build() {
    const panel = q("panel-analytics");
    panel.textContent = "";
    const err = el("p", "inline-status err hidden");
    err.id = "an-error";
    const notice = el("div", "ctmsg note hidden");
    notice.id = "an-notice";

    const head = el("section", "an-head");
    head.id = "an-head";
    const row = el("div", "row");
    const sel = el("select");
    sel.id = "an-range";
    sel.setAttribute("aria-label", "Date range");
    for (const [id, label] of RANGES) {
      const o = el("option", "", label);
      o.value = id;
      sel.appendChild(o);
    }
    sel.addEventListener("change", () => {
      if (sel.value === "custom") {
        const per = period();
        q("an-from").value = per.from || addDays(todayIn(tz()), -1);
        q("an-to").value = per.to || todayIn(tz());
        q("an-custom").classList.remove("hidden");
        q("an-from").focus();
        return;
      }
      S.range = sel.value;
      S.expanded = null;
      navigate(true);
    });
    const custom = el("span", "an-custom hidden");
    custom.id = "an-custom";
    const from = el("input"), to = el("input");
    from.type = to.type = "date";
    from.id = "an-from";
    to.id = "an-to";
    from.setAttribute("aria-label", "From");
    to.setAttribute("aria-label", "To");
    const apply = button("Apply", "", () => {
      if (!isDate(from.value) || !isDate(to.value) || from.value > to.value) { toast("Pick a first and a last day, in that order", true); return; }
      const per = clampSpan({ from: from.value, to: to.value });
      if (per.from !== from.value) toast("A range spans at most " + maxSpan() + " days here, so it starts on " + per.from);
      S.range = "custom"; S.from = per.from; S.to = per.to;
      S.expanded = null;
      navigate(true);
    });
    custom.append(from, el("span", "sub", "to"), to, apply);
    const cmp = switchEl("Compare with previous period", false, (on) => { S.cmp = on; navigate(true); });
    cmp.id = "an-cmp";
    row.append(sel, custom, cmp);
    const chips = el("div", "an-chips");
    chips.id = "an-chips";
    const note = el("p", "sub hidden");
    note.id = "an-headnote";
    head.append(row, chips, note);

    const nav = el("nav", "an-subtabs");
    nav.id = "an-subtabs";
    nav.setAttribute("role", "tablist");
    nav.setAttribute("aria-label", "Analytics sections");
    for (const [id, label] of SUBTABS) {
      const b = el("button", "an-subtab", label);
      b.type = "button";
      b.id = "an-sub-" + id;
      b.setAttribute("role", "tab");
      b.setAttribute("aria-controls", "an-body");
      b.addEventListener("click", () => {
        if (S.sub === id) return;
        S.sub = id;
        S.editing = null;
        navigate(true);
      });
      b.addEventListener("keydown", (e) => {
        const i = SUBTABS.findIndex((t) => t[0] === id), n = SUBTABS.length;
        const j = e.key === "ArrowRight" ? (i + 1) % n : e.key === "ArrowLeft" ? (i + n - 1) % n : -1;
        if (j < 0) return;
        e.preventDefault();
        q("an-sub-" + SUBTABS[j][0]).focus();
      });
      nav.appendChild(b);
    }
    const body = el("div", "an-body");
    body.id = "an-body";
    body.setAttribute("role", "tabpanel");
    // The frame shows once the state is loaded: nothing in it works before.
    for (const e of [head, nav, body]) e.classList.add("hidden");
    panel.append(err, notice, head, nav, body);

    // Realtime polls only while it can be seen: another tab, another
    // sub-tab or a hidden page stops it.
    new MutationObserver(() => { if (panel.classList.contains("hidden")) stopPoll(); })
      .observe(panel, { attributes: true, attributeFilter: ["class"] });
    document.addEventListener("visibilitychange", () => {
      if (document.hidden) stopPoll();
      else if (pollWanted() && !S.poll) startPoll();
    });
    S.built = true;
  }

  function paintFrame() {
    const st = S.state;
    // The plan notice: nothing new is counted, what was counted stays readable.
    const n = q("an-notice");
    n.textContent = "";
    if (st.reason === "plan") {
      n.appendChild(el("strong", "", "Your plan does not include analytics. "));
      n.appendChild(document.createTextNode("Nothing new is counted; what was recorded stays here to read. "));
      const a = el("a", "", "Upgrade your plan");
      const u = st.upgrade_url || "";
      a.href = /^https?:\/\//.test(u) || u.startsWith("/") ? u : "/account";
      n.appendChild(a);
      n.appendChild(document.createTextNode(" to count again."));
    } else if (st.reason === "instance") {
      n.textContent = "Analytics is turned off on this instance. Nothing new is counted; what was recorded stays here to read.";
    }
    n.classList.toggle("hidden", !n.textContent);

    for (const [id] of SUBTABS) {
      const b = q("an-sub-" + id);
      b.setAttribute("aria-selected", String(id === S.sub));
      b.tabIndex = id === S.sub ? 0 : -1;
    }
    q("an-range").value = S.range;
    q("an-custom").classList.toggle("hidden", S.range !== "custom");
    if (S.range === "custom") { const per = period(); q("an-from").value = per.from; q("an-to").value = per.to; }
    q("an-cmp").classList.toggle("hidden", S.sub !== "overview");
    q("an-cmp").querySelector("input").checked = S.cmp;

    const chips = q("an-chips");
    chips.textContent = "";
    S.filters.forEach((f, i) => {
      const c = el("span", "an-chip");
      c.append(el("span", "k", dimLabel(f.dim)), el("span", "v", display(f.dim, f.value)));
      const x = el("button", "x", "×");
      x.type = "button";
      x.setAttribute("aria-label", "Remove the filter " + dimLabel(f.dim) + " " + f.value);
      x.addEventListener("click", () => removeFilter(i));
      c.appendChild(x);
      chips.appendChild(c);
    });
    if (S.filters.length > 1) {
      const all = el("button", "linkbtn", "Clear filters");
      all.type = "button";
      all.addEventListener("click", () => { S.filters = []; S.expanded = null; navigate(true); });
      chips.appendChild(all);
    }
    chips.classList.toggle("hidden", !S.filters.length);

    // The header stays across the sub-tabs, with the range and filters kept;
    // Realtime and the settings take neither, so there it rests.
    const still = S.sub === "realtime" || S.sub === "settings";
    q("an-head").classList.toggle("an-still", still);
    for (const c of q("an-head").querySelectorAll("select, input, button")) c.disabled = still;
    const note = q("an-headnote");
    note.textContent = S.sub === "realtime" ? "Realtime always shows the last 30 minutes, without filters." :
      S.sub === "settings" ? "The range and the filters apply to the statistics." : "";
    note.classList.toggle("hidden", !note.textContent);
  }

  // showError shows msg above the frame ("" hides it), with a button that
  // tries again when retry is given.
  function showError(msg, retry) {
    const p = q("an-error");
    p.textContent = msg;
    if (msg && retry) {
      p.appendChild(document.createTextNode(" "));
      p.appendChild(button("Try again", "", retry));
    }
    p.classList.toggle("hidden", !msg);
  }

  // frame shows or hides the header, the sub-tabs and the body.
  function frame(on) {
    for (const id of ["an-head", "an-subtabs", "an-body"]) q(id).classList.toggle("hidden", !on);
  }

  // ---- the tab ----

  async function loadState() {
    S.state = await api("GET", "/analytics");
  }

  async function show() {
    if (!S.built) build();
    // Clicking the tab names only "#analytics": put back the view last shown.
    if (/^#analytics\/?$/.test(location.hash) && S.lastHash) {
      history.replaceState(null, "", location.pathname + location.search + S.lastHash);
    }
    const was = S.sub;
    readHash();
    // Back or Forward to another sub-tab leaves an open editor behind.
    if (S.sub !== was) S.editing = null;
    try {
      await loadState();
    } catch (err) {
      // Without a state nothing in the frame can work: it stays hidden
      // until a retry loads one. A later failure keeps the last view.
      if (!S.state) frame(false);
      showError(err.message, () => { showError(""); show(); });
      return;
    }
    showError("");
    frame(true);
    if (tabs.active !== "analytics") return; // left while it loaded
    const h = hashOf();
    S.lastHash = h;
    if (location.hash !== h) history.replaceState(null, "", location.pathname + location.search + h);
    render(false);
  }

  // render paints the frame and the sub-tab. Without force, a view shown
  // less than a minute ago is kept as it is.
  function render(force) {
    if (!S.state) return; // show() loads it first, and retries
    stopPoll();
    showError(""); // an error belongs to the view it came from
    paintFrame();
    const key = hashOf();
    if (!force && key === S.key && Date.now() - S.loadedAt < RELOAD_MS && S.sub !== "realtime" && S.sub !== "settings") return;
    S.key = key;
    S.loadedAt = Date.now();
    const gen = ++S.gen;
    switch (S.sub) {
      case "events": return renderEvents(gen);
      case "funnels": return renderFunnels(gen);
      case "goals": return renderGoals(gen);
      case "realtime": return renderRealtime(gen);
      case "settings": return renderSettings();
      default: return renderOverview(gen);
    }
  }

  function body() { return q("an-body"); }

  // busy keeps the last render at reduced opacity while the next loads: no
  // skeleton, no jump.
  function busy(on) { body().classList.toggle("an-busy", on); }

  function loadFailed(gen, err) {
    if (gen !== S.gen) return;
    S.key = ""; // showing the tab again tries again
    busy(false);
    const box = el("div", "card");
    box.appendChild(el("p", "inline-status err", err.message));
    if (S.filters.length) box.appendChild(button("Clear filters", "", () => { S.filters = []; navigate(true); }));
    body().replaceChildren(box);
  }

  function settingsOf(st) {
    const s = st.settings || {};
    return {
      enabled: !!s.enabled, mode: s.mode || "server", timezone: s.timezone || "",
      addresses: s.addresses || [], exclude: s.exclude || [], query: s.query || [],
      funnels: s.funnels || [], goals: s.goals || [],
    };
  }

  // putSettings saves the whole settings object; a refusal carries the field
  // the server named.
  async function putSettings(settings) {
    const res = await fetch("/api/sites/" + editID + "/analytics", {
      method: "PUT",
      headers: Object.assign(authHeaders(), { "Content-Type": "application/json" }),
      body: JSON.stringify(settings),
    });
    let data = {};
    try { data = await res.json(); } catch {}
    if (!res.ok) throw Object.assign(new Error(data.error || res.statusText), { status: res.status, field: data.field || "" });
    S.state = data;
    return data;
  }

  const noData = () => !S.state.first_day;

  // emptyCard is what a sub-tab shows with nothing recorded: the switch when
  // analytics is off, else what to do for the mode.
  function emptyCard() {
    const st = S.state, set = settingsOf(st);
    const card = el("section", "card an-empty");
    if (!set.enabled) {
      card.appendChild(el("h3", "", "Analytics is off"));
      if (st.reason === "plan") {
        card.appendChild(el("p", "", "Your plan does not include analytics, so nothing is counted."));
        return card;
      }
      card.appendChild(switchEl("Count this site's visits", false, async (on, input) => {
        if (!on) return;
        input.disabled = true;
        try {
          await putSettings(Object.assign(set, { enabled: true }));
          toast("Analytics is on");
          render(true);
        } catch (err) {
          input.checked = false;
          toast(err.message, true);
        } finally { input.disabled = false; }
      }));
      card.appendChild(el("p", "sub", "Turning it on counts this site's page views without cookies and without storing " +
        "IP addresses: which pages are read, where visitors came from, and their browser, device and country."));
      return card;
    }
    card.appendChild(el("h3", "", "Nothing recorded yet"));
    if (set.mode === "script") {
      card.appendChild(el("p", "", "This site counts in script mode: add the script to your pages, and their page views and events arrive here."));
      for (const s of scriptSnippets()) {
        if (s.label) card.appendChild(el("p", "sub", s.label));
        card.appendChild(cmdBlock(s.line));
      }
    } else {
      card.appendChild(el("p", "", "Page views are counted as soon as a browser opens a page of this site. " +
        "Add the script under Settings too if you want events."));
    }
    return card;
  }

  // offNote says that a site with data no longer counts.
  function offNote() {
    const st = S.state;
    if (settingsOf(st).enabled || st.reason) return null;
    const n = el("div", "ctmsg note");
    n.appendChild(document.createTextNode("Analytics is off for this site: nothing new is counted. "));
    const b = el("button", "linkbtn", "Open the settings");
    b.type = "button";
    b.addEventListener("click", () => { S.sub = "settings"; navigate(true); });
    n.appendChild(b);
    return n;
  }

  // ---- overview ----

  const METRICS = [
    ["visitors", "Visitors", "Visitors are counted per day: someone who comes back on another day counts again, and a range adds up its days."],
    ["visits", "Visits", "A visit ends after 30 minutes without a page view."],
    ["views", "Views", ""],
    ["bounce_rate", "Bounce rate", "Visits that saw one page only."],
    ["duration_s", "Visit duration", "The average time from a visit's first page view to its last."],
  ];

  function metricValue(key, v) {
    if (key === "bounce_rate") return fmtPct(v);
    if (key === "duration_s") return fmtDuration(v);
    return fmtBig(v);
  }

  // delta is a metric's change against the period before: relative for
  // counts and durations, in points for the bounce rate, where down is good.
  function delta(key, cur, prev) {
    const d = el("div", "an-delta");
    let text, dir;
    if (key === "bounce_rate") {
      const pts = Math.round((cur - prev) * 100);
      dir = Math.sign(pts);
      text = (pts > 0 ? "+" : pts < 0 ? "−" : "±") + Math.abs(pts) + " pts";
    } else if (!prev) {
      dir = cur ? 1 : 0;
      text = cur ? "new" : "±0%";
    } else {
      const pct = Math.round((cur - prev) / prev * 100);
      dir = Math.sign(pct);
      text = (pct > 0 ? "+" : pct < 0 ? "−" : "±") + Math.abs(pct) + "%";
    }
    const good = key === "bounce_rate" ? dir < 0 : dir > 0;
    d.classList.add(dir === 0 ? "flat" : good ? "good" : "bad");
    d.append(el("span", "arrow", dir > 0 ? "▲" : dir < 0 ? "▼" : "•"), el("span", "", text));
    d.appendChild(el("span", "was", " vs " + metricValue(key, prev)));
    return d;
  }

  function metricsBar(ov) {
    const bar = el("div", "an-metrics");
    // A period before with no visit at all has nothing to compare with: a
    // bounce rate "up 60 points" from nothing would only mislead.
    const nothingBefore = ov.previous && !ov.previous.visits && !ov.previous.views;
    for (const [key, label, hint] of METRICS) {
      const m = el("div", "an-metric");
      const l = el("div", "lbl", label);
      if (hint) { l.title = hint; l.classList.add("hint"); }
      m.append(l, el("div", "val", metricValue(key, ov.totals[key])));
      if (nothingBefore) m.appendChild(el("div", "an-delta flat", "nothing before"));
      else if (ov.previous) m.appendChild(delta(key, ov.totals[key], ov.previous[key]));
      bar.appendChild(m);
    }
    return bar;
  }

  async function renderOverview(gen) {
    if (noData()) { busy(false); body().replaceChildren(emptyCard()); return; }
    busy(true);
    let ov;
    try {
      ov = await api("GET", "/analytics/overview" + query(S.cmp ? { compare: "1" } : null));
    } catch (err) { return loadFailed(gen, err); }
    if (gen !== S.gen) return;
    S.totals = ov.totals;
    const frag = document.createDocumentFragment();
    const off = offNote();
    if (off) frag.appendChild(off);
    const top = el("section", "card");
    top.appendChild(metricsBar(ov));
    if (ov.over_cap > 0) {
      top.appendChild(el("p", "an-cap", fmtNum(ov.over_cap) + " records over the daily limit were not stored."));
    }
    top.appendChild(barChart(ov.series || [], ov.unit, {
      visitors: true,
      label: "Views and visitors per " + (ov.unit || "day"),
    }));
    frag.appendChild(top);
    const grid = el("div", "an-grid");
    S.cards = {};
    for (const g of GROUPS) {
      const card = panelCard(g);
      S.cards[g.id] = card;
      grid.appendChild(card);
    }
    frag.appendChild(grid);
    body().replaceChildren(frag);
    busy(false);
    for (const g of GROUPS) loadPanel(g);
  }

  function dimOf(g) {
    const p = S.pick[g.id];
    return p === "utm" ? S.utm : p;
  }

  function panelCard(g) {
    const card = el("section", "card an-panel");
    const h = el("h3", "", g.title);
    if (g.picks.length > 1) {
      const picks = el("span", "an-picks");
      for (const [d, label] of g.picks) {
        const b = el("button", "an-pick", label);
        b.type = "button";
        b.dataset.pick = d;
        b.addEventListener("click", () => {
          if (S.pick[g.id] === d) return;
          S.pick[g.id] = d;
          if (S.expanded && S.expanded.group === g.id) S.expanded.offset = 0;
          loadPanel(g);
        });
        picks.appendChild(b);
      }
      h.appendChild(picks);
    }
    card.appendChild(h);
    if (g.id === "sources") {
      // The UTM table's parameter: the five utm_* and the site's own kept
      // query parameters.
      const sel = el("select", "an-utm hidden");
      sel.setAttribute("aria-label", "UTM parameter");
      const names = UTM.concat(((S.state.settings || {}).query || []).map((n) => "query:" + n));
      for (const d of names) {
        const o = el("option", "", d.startsWith("query:") ? "?" + d.slice(6) : d);
        o.value = d;
        sel.appendChild(o);
      }
      sel.value = names.includes(S.utm) ? S.utm : "utm_source";
      sel.addEventListener("change", () => {
        S.utm = sel.value;
        if (S.expanded && S.expanded.group === g.id) S.expanded.offset = 0; // another list starts at its top
        loadPanel(g);
      });
      card.appendChild(sel);
    }
    card.appendChild(el("div", "an-tbl"));
    card.appendChild(el("div", "an-foot"));
    if (g.id === "location") {
      // The attribution the database's licence asks for: DB-IP's with its
      // link (the server names it; only an https link is made one).
      const geo = S.state.geo || {};
      const p = el("p", "an-attr");
      if (!geo.present) {
        p.textContent = "This instance has no GeoIP database, so visits carry no location (SITEBIN_ANALYTICS_GEOIP).";
      } else if (geo.attribution && /^https:\/\/[^\/]/.test(geo.attribution_url || "")) {
        const a = el("a", "", geo.attribution);
        a.href = geo.attribution_url;
        a.target = "_blank";
        a.rel = "noopener noreferrer";
        p.appendChild(a);
      } else {
        p.textContent = geo.attribution || "";
      }
      if (p.textContent) card.appendChild(p);
    }
    return card;
  }

  async function loadPanel(g) {
    const card = S.cards[g.id];
    if (!card || !card.isConnected) return;
    const my = (card.anGen = (card.anGen || 0) + 1);
    const dim = dimOf(g);
    const wide = !!(S.expanded && S.expanded.group === g.id);
    const offset = wide ? S.expanded.offset : 0;
    card.classList.toggle("an-wide", wide);
    for (const b of card.querySelectorAll(".an-pick")) b.setAttribute("aria-pressed", String(b.dataset.pick === S.pick[g.id]));
    const utm = card.querySelector(".an-utm");
    if (utm) utm.classList.toggle("hidden", S.pick[g.id] !== "utm");
    const tbl = card.querySelector(".an-tbl"), foot = card.querySelector(".an-foot");
    tbl.classList.add("an-busy");
    let res;
    try {
      res = await api("GET", "/analytics/table" + query({ dim, limit: wide ? PAGE : TOP, offset }));
    } catch (err) {
      if (my !== card.anGen) return;
      tbl.classList.remove("an-busy");
      tbl.replaceChildren(el("p", "inline-status err", err.message));
      foot.textContent = "";
      return;
    }
    if (my !== card.anGen || !card.isConnected) return;
    tbl.classList.remove("an-busy");
    const all = S.totals ? S.totals.visitors : 0;
    tbl.replaceChildren(rowsList(res.rows || [], dim, {
      share: (r) => (all > 0 ? r.visitors / all : 0),
      onPick: (v) => addFilter(dim, v),
    }));
    foot.textContent = "";
    if (wide) foot.appendChild(pager(res.total, offset, (o) => { S.expanded.offset = o; loadPanel(g); }, () => expand(null)));
    else if (res.total > TOP) foot.appendChild(button("More", "an-more", () => expand(g.id)));
  }

  // expand opens one table's full list, a page at a time; null closes it.
  function expand(id) {
    const was = S.expanded && S.expanded.group;
    S.expanded = id ? { group: id, offset: 0 } : null;
    for (const g of GROUPS) if (g.id === id || g.id === was) loadPanel(g);
    const card = id ? S.cards[id] : S.cards[was];
    if (card) card.scrollIntoView({ block: "nearest" });
  }

  function pager(total, offset, go, close) {
    const p = el("div", "an-pager");
    const last = Math.min(total, offset + PAGE);
    p.appendChild(el("span", "sub", (total ? offset + 1 : 0) + "–" + last + " of " + fmtNum(total)));
    const prev = button("Previous", "", () => go(Math.max(0, offset - PAGE)));
    prev.disabled = offset === 0;
    const next = button("Next", "", () => go(offset + PAGE));
    next.disabled = last >= total;
    p.append(prev, next);
    if (close) p.appendChild(button("Show top " + TOP, "", close));
    return p;
  }

  const COLS_EVENT = [["Count", (r) => r.count], ["Visitors", (r) => r.visitors]];
  const COLS_VISITS = [["Visitors", (r) => r.visitors], ["Views", (r) => r.views]];

  // rowsList is a table: the value with its share bar, then its numbers
  // (opt.cols, by default visitors and views, or count and visitors for
  // events). Clicking a value calls opt.onPick (a filter, or the event to
  // show); Other cannot be picked. Every value is text.
  function rowsList(rows, dim, opt) {
    const cols = opt.cols || (dim === "event" ? COLS_EVENT : COLS_VISITS);
    const box = el("div", "an-rows" + (cols.length === 1 ? " an-one" : ""));
    const head = el("div", "an-th");
    head.appendChild(el("span", "", opt.label || dimLabel(dim)));
    for (const [name] of cols) head.appendChild(el("span", "", name));
    box.appendChild(head);
    if (!rows.length) { box.appendChild(el("p", "sub an-none", "No data in this range.")); return box; }
    for (const r of rows) {
      const line = el("div", "an-row" + (opt.selected !== undefined && opt.selected === r.value ? " on" : ""));
      line.anValue = r.value;
      const lab = el("div", "lab");
      const top = el("div", "an-valrow");
      const pickable = opt.onPick && r.value !== OTHER;
      const v = el(pickable ? "button" : "span", "an-val" + (r.value === OTHER ? " other" : ""), display(dim, r.value));
      if (pickable) {
        v.type = "button";
        v.title = opt.pickHint || "Filter by this " + dimLabel(dim).toLowerCase();
        v.addEventListener("click", () => opt.onPick(r.value));
      }
      top.appendChild(v);
      if (dim === "referrer") {
        const href = referrerURL(r.value);
        if (href) top.appendChild(extLink(href));
      }
      lab.append(top, shareBar(opt.share(r)));
      line.appendChild(lab);
      cols.forEach(([, get], i) => line.appendChild(el("span", i ? "n dim" : "n", fmtNum(get(r)))));
      box.appendChild(line);
    }
    return box;
  }

  function shareBar(f) {
    f = Math.max(0, Math.min(1, f || 0));
    const w = el("div", "an-sharewrap");
    const bar = el("div", "an-share");
    const fill = el("i");
    fill.style.width = (f * 100).toFixed(1) + "%";
    bar.appendChild(fill);
    w.append(bar, el("span", "pct", fmtPct(f)));
    return w;
  }

  // ---- the chart ----

  // niceScale picks the axis top and step: 4 steps of 1, 2 or 5 × 10^k.
  function niceScale(max) {
    if (!(max > 0)) return { step: 1, top: 4 };
    const raw = max / 4;
    const mag = Math.pow(10, Math.floor(Math.log10(raw)));
    const norm = raw / mag;
    const step = Math.max(1, (norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10) * mag);
    return { step, top: step * Math.ceil(max / step) };
  }

  function fmtTick(v) { return v >= 10000 ? compact.format(v) : v.toLocaleString(); }

  // barChart draws views as bars and, with opts.visitors, visitors as a line
  // on the same axis (both are counts). Each bar's column is its hover and
  // keyboard target; the table below is the chart's twin.
  function barChart(points, unit, opts) {
    const wrap = el("div", "an-chart");
    if (opts.visitors) {
      const lg = el("div", "an-legend");
      const k1 = el("span", "an-key"); k1.append(el("i", "sw"), el("span", "", "Views"));
      const k2 = el("span", "an-key"); k2.append(el("i", "ln"), el("span", "", "Visitors"));
      lg.append(k1, k2);
      wrap.appendChild(lg);
    }
    const plot = el("div", "an-plot");
    const tip = el("div", "an-tip hidden");
    tip.setAttribute("aria-live", "polite");
    plot.appendChild(tip);
    wrap.appendChild(plot);
    let active = -1, bars = [], cx = null, yOf = null, marker = null, svg = null, width = 0;

    function setActive(i) {
      if (active >= 0 && bars[active]) bars[active].classList.remove("on");
      active = i;
      if (i < 0 || !points[i]) {
        tip.classList.add("hidden");
        if (marker) marker.setAttribute("visibility", "hidden");
        return;
      }
      if (bars[i]) bars[i].classList.add("on");
      const p = points[i];
      if (marker) {
        marker.setAttribute("cx", cx(i));
        marker.setAttribute("cy", yOf(p.visitors));
        marker.setAttribute("visibility", "visible");
      }
      tip.textContent = "";
      tip.appendChild(el("div", "t", fmtBucket(p.t, unit, true)));
      const line = (cls, v, name) => {
        const r = el("div", "r");
        r.append(el("i", cls), el("strong", "", fmtNum(v)), el("span", "", name));
        tip.appendChild(r);
      };
      line("sw", p.views, "Views");
      if (opts.visitors) line("ln", p.visitors, "Visitors");
      tip.classList.remove("hidden");
      const tw = tip.offsetWidth;
      tip.style.left = Math.max(0, Math.min(width - tw, cx(i) - tw / 2)) + "px";
    }

    function draw() {
      const W = Math.floor(plot.clientWidth);
      if (!W || W === width) return;
      width = W;
      const H = 220, ml = 44, mr = 8, mt = 14, mb = 26;
      const pw = W - ml - mr, ph = H - mt - mb;
      const n = points.length;
      let max = 0;
      for (const p of points) max = Math.max(max, p.views, opts.visitors ? p.visitors : 0);
      const { step, top } = niceScale(max);
      yOf = (v) => mt + ph - (v / top) * ph;
      const band = pw / Math.max(1, n);
      const bw = Math.max(1, Math.min(24, band * 0.7, band - 2));
      cx = (i) => ml + band * i + band / 2;
      if (svg) svg.remove();
      svg = svgEl("svg", { width: W, height: H, viewBox: "0 0 " + W + " " + H, tabindex: "0", role: "img",
        "aria-label": opts.label + ". Use the left and right arrow keys to read each bar." });
      const grid = svgEl("g", {}, "an-gridlines");
      for (let v = 0; v <= top; v += step) {
        grid.appendChild(svgEl("line", { x1: ml, x2: W - mr, y1: yOf(v), y2: yOf(v) }, v === 0 ? "base" : ""));
        const t = svgEl("text", { x: ml - 8, y: yOf(v), "text-anchor": "end", "dominant-baseline": "middle" });
        t.textContent = fmtTick(v);
        grid.appendChild(t);
      }
      svg.appendChild(grid);
      bars = [];
      const gb = svgEl("g", {});
      points.forEach((p, i) => {
        if (!(p.views > 0)) { bars.push(null); return; }
        const x = cx(i) - bw / 2, y = yOf(p.views), yb = mt + ph, h = yb - y;
        const r = Math.min(4, bw / 2, h);
        // rounded at the data end, square at the baseline
        const d = "M" + x + "," + yb + "V" + (y + r) + "Q" + x + "," + y + " " + (x + r) + "," + y +
          "H" + (x + bw - r) + "Q" + (x + bw) + "," + y + " " + (x + bw) + "," + (y + r) + "V" + yb + "Z";
        const path = svgEl("path", { d }, "an-bar");
        gb.appendChild(path);
        bars.push(path);
      });
      svg.appendChild(gb);
      if (opts.visitors && n) {
        if (n === 1) {
          svg.appendChild(svgEl("circle", { cx: cx(0), cy: yOf(points[0].visitors), r: 4 }, "an-dot"));
        } else {
          const d = points.map((p, i) => (i ? "L" : "M") + cx(i) + "," + yOf(p.visitors)).join("");
          svg.appendChild(svgEl("path", { d }, "an-vhalo"));
          svg.appendChild(svgEl("path", { d }, "an-vline"));
        }
        marker = svgEl("circle", { r: 4, cx: 0, cy: 0, visibility: "hidden" }, "an-dot");
        svg.appendChild(marker);
      } else marker = null;
      // x labels: as many as fit. Hours are labelled on round hours, and
      // across days the midnight label names the day.
      const xs = svgEl("g", {}, "an-xlabels");
      const fit = Math.max(2, Math.floor(pw / 72));
      let at = (i) => i % Math.max(1, Math.ceil(n / fit)) === 0;
      let midnight = () => false;
      if (unit === "hour") {
        const hours = points.map((p) => parseInt(fmtTime(p.t, { hour: "numeric", hourCycle: "h23" }), 10) || 0);
        const every = [1, 2, 3, 4, 6, 12, 24].find((s) => Math.ceil(n / s) <= fit) || 24;
        at = (i) => hours[i] % every === 0;
        if (n > 24) midnight = (i) => hours[i] === 0;
      }
      for (let i = 0; i < n; i++) {
        if (!at(i)) continue;
        const t = svgEl("text", { x: cx(i), y: H - 8, "text-anchor": "middle" }, midnight(i) ? "day" : "");
        t.textContent = midnight(i) ? fmtTime(points[i].t, { month: "short", day: "numeric" }) : fmtBucket(points[i].t, unit, false);
        xs.appendChild(t);
      }
      svg.appendChild(xs);
      const hits = svgEl("g", {});
      points.forEach((_, i) => {
        const r = svgEl("rect", { x: ml + band * i, y: mt, width: band, height: ph }, "an-hit");
        r.addEventListener("pointerenter", () => setActive(i));
        hits.appendChild(r);
      });
      svg.appendChild(hits);
      svg.addEventListener("pointerleave", () => setActive(-1));
      svg.addEventListener("blur", () => setActive(-1));
      svg.addEventListener("keydown", (e) => {
        let i = -1;
        if (e.key === "ArrowRight") i = Math.min(n - 1, active + 1);
        else if (e.key === "ArrowLeft") i = active < 0 ? n - 1 : Math.max(0, active - 1);
        else if (e.key === "Home") i = 0;
        else if (e.key === "End") i = n - 1;
        else if (e.key === "Escape") { setActive(-1); return; }
        if (i < 0) return;
        e.preventDefault();
        setActive(i);
      });
      plot.insertBefore(svg, tip);
      const was = active;
      active = -1;
      if (was >= 0) setActive(was);
    }
    new ResizeObserver(draw).observe(plot);

    // the table twin, built when it is opened
    const twin = el("details", "an-twin");
    twin.appendChild(el("summary", "", "Show as table"));
    twin.addEventListener("toggle", () => {
      if (!twin.open || twin.querySelector("table")) return;
      const t = el("table", "an-table");
      const hr = el("tr");
      hr.append(el("th", "", unit === "hour" || unit === "minute" ? "Time" : unit === "month" ? "Month" : "Day"), el("th", "", "Views"));
      if (opts.visitors) hr.appendChild(el("th", "", "Visitors"));
      t.appendChild(hr);
      for (const p of points) {
        const tr = el("tr");
        tr.append(el("td", "", fmtBucket(p.t, unit, true)), el("td", "", fmtNum(p.views)));
        if (opts.visitors) tr.appendChild(el("td", "", fmtNum(p.visitors)));
        t.appendChild(tr);
      }
      twin.appendChild(t);
    });
    wrap.appendChild(twin);
    return wrap;
  }

  // ---- events ----

  async function renderEvents(gen) {
    if (noData()) { busy(false); body().replaceChildren(emptyCard()); return; }
    // Another range or other filters: the list starts at its first page.
    const key = query();
    if (key !== S.eventKey) { S.eventKey = key; S.eventOffset = 0; }
    busy(true);
    let res;
    try {
      res = await api("GET", "/analytics/table" + query({ dim: "event", limit: PAGE, offset: S.eventOffset }));
    } catch (err) { return loadFailed(gen, err); }
    if (gen !== S.gen) return;
    const rows = res.rows || [];
    if (!rows.length && S.eventOffset > 0 && res.total > 0) {
      // A page past the end (the list shrank meanwhile): the last one.
      S.eventOffset = Math.floor((res.total - 1) / PAGE) * PAGE;
      return render(true);
    }
    if (!rows.some((r) => r.value === S.event)) S.event = (rows.find((r) => r.value !== OTHER) || {}).value || "";
    const frag = document.createDocumentFragment();
    const off = offNote();
    if (off) frag.appendChild(off);
    const card = el("section", "card");
    card.appendChild(el("h3", "", "Events"));
    if (!rows.length) {
      card.appendChild(el("p", "sub", "No events in this range. The script sends them: umami.track(\"name\") or a " +
        "data-umami-event attribute on an element, and every form submission is the event sitebin:form."));
    }
    const data = el("section", "card an-evdata");
    if (rows.length) {
      // A ranked bar chart: each event's count against the most frequent.
      const maxCount = Math.max(1, ...rows.map((r) => r.count || 0));
      card.appendChild(rowsList(rows, "event", {
        share: (r) => (r.count || 0) / maxCount,
        onPick: (v) => {
          S.event = v;
          for (const line of card.querySelectorAll(".an-row")) line.classList.toggle("on", line.anValue === v);
          renderEventData(gen, data);
        },
        pickHint: "Show this event's data",
        selected: S.event,
      }));
      if (res.total > PAGE) {
        card.appendChild(pager(res.total, S.eventOffset, (o) => { S.eventOffset = o; render(true); }));
      }
    }
    frag.append(card, data);
    body().replaceChildren(frag);
    busy(false);
    renderEventData(gen, data);
  }

  async function renderEventData(gen, box) {
    if (!box) return;
    box.textContent = "";
    box.classList.toggle("hidden", !S.event);
    if (!S.event) return;
    const name = S.event;
    const h = el("h3");
    h.append(document.createTextNode("Event data · "), el("span", "an-evname", name));
    box.appendChild(h);
    const filterBtn = button("Filter by this event", "", () => addFilter("event", name));
    box.appendChild(filterBtn);
    const list = el("div", "an-keys");
    box.appendChild(list);
    let res;
    try {
      res = await api("GET", "/analytics/event-data" + query({ name }));
    } catch (err) {
      if (gen === S.gen && S.event === name) list.appendChild(el("p", "inline-status err", err.message));
      return;
    }
    if (gen !== S.gen || S.event !== name) return;
    const keys = res.keys || [];
    if (!keys.length) { list.appendChild(el("p", "sub", "This event carried no data in this range.")); return; }
    for (const k of keys) {
      const vals = k.values || [];
      const sum = vals.reduce((a, v) => a + (v.count || 0), 0) || 1;
      // A data key with a colon cannot be filtered on (the API splits there).
      const filterable = !k.key.includes(":");
      list.appendChild(rowsList(vals, "data:" + k.key, {
        label: k.key,
        cols: [["Count", (r) => r.count]],
        share: (r) => r.count / sum,
        onPick: filterable ? (v) => addFilter("data:" + k.key, v) : null,
        pickHint: "Filter by this value",
      }));
    }
  }

  // ---- funnels and goals ----

  function newID(prefix) {
    const r = new Uint8Array(6);
    crypto.getRandomValues(r);
    return prefix + "-" + Array.from(r, (b) => (b % 36).toString(36)).join("");
  }

  function stepText(st) {
    const w = el("span", "what");
    w.append(el("span", "kind", st.type === "event" ? "Event" : "Page"), el("span", "", st.value));
    return w;
  }

  // armed is a delete button that asks for a second click.
  function armedDelete(label, onConfirm) {
    let armed = false, timer;
    const b = button(label, "danger", async () => {
      if (!armed) {
        armed = true;
        b.textContent = "Click again to delete";
        timer = setTimeout(() => { armed = false; b.textContent = label; }, 4000);
        return;
      }
      clearTimeout(timer);
      armed = false;
      b.textContent = label;
      await onConfirm();
    });
    return b;
  }

  async function saveList(kind, list, errLine) {
    const set = settingsOf(S.state);
    set[kind] = list;
    try {
      await putSettings(set);
      return true;
    } catch (err) {
      if (errLine) errLine.textContent = err.message;
      else toast(err.message, true);
      return false;
    }
  }

  const PATH_HELP = "A page step matches the path as Umami does: * stands for any characters, slashes included, " +
    "so /blog/* matches every page under /blog/. An event step names an event, such as signup or sitebin:form.";

  async function renderFunnels(gen) {
    busy(true);
    let res = { funnels: [] };
    if (!noData()) {
      try {
        res = await api("GET", "/analytics/funnels" + query());
      } catch (err) { return loadFailed(gen, err); }
    }
    if (gen !== S.gen) return;
    const saved = settingsOf(S.state).funnels;
    const byID = {};
    for (const r of res.funnels || []) byID[r.id] = r;
    const frag = document.createDocumentFragment();
    const off = offNote();
    if (off) frag.appendChild(off);
    const bar = el("div", "row an-bar-actions");
    const add = button("Add funnel", "primary", () => { S.editing = { kind: "funnel", id: "" }; render(true); });
    add.disabled = saved.length >= 20 || !!S.editing;
    bar.append(add, el("span", "sub", saved.length + " of 20 funnels"));
    frag.appendChild(bar);
    if (S.editing && S.editing.kind === "funnel" && !S.editing.id) frag.appendChild(funnelEditor(null));
    if (!saved.length && !S.editing) {
      const c = el("section", "card an-empty");
      c.appendChild(el("h3", "", "No funnels yet"));
      c.appendChild(el("p", "sub", "A funnel shows how many visitors go through a series of pages or events in order, " +
        "and where the others drop off."));
      frag.appendChild(c);
    }
    for (const f of saved) {
      if (S.editing && S.editing.kind === "funnel" && S.editing.id === f.id) { frag.appendChild(funnelEditor(f)); continue; }
      frag.appendChild(funnelCard(f, byID[f.id]));
    }
    body().replaceChildren(frag);
    busy(false);
  }

  function funnelCard(f, r) {
    const card = el("section", "card an-funnel");
    const h = el("h3");
    h.append(el("span", "an-title", f.name));
    const acts = el("span", "an-acts");
    acts.append(
      button("Edit", "", () => { S.editing = { kind: "funnel", id: f.id }; render(true); }),
      armedDelete("Delete", async () => {
        if (await saveList("funnels", settingsOf(S.state).funnels.filter((x) => x.id !== f.id))) { toast("Funnel deleted"); render(true); }
      }));
    h.appendChild(acts);
    card.appendChild(h);
    card.appendChild(el("p", "sub", "Each step within " + f.window_minutes + " minute" + (f.window_minutes === 1 ? "" : "s") +
      " of the step before, on the same day."));
    const steps = (r && r.steps) || [];
    f.steps.forEach((st, i) => {
      const res = steps[i] || { visitors: 0, drop_off: 0, conversion: 0 };
      const row = el("div", "an-step");
      row.append(el("span", "no", String(i + 1)), stepText(st));
      const nums = el("span", "nums");
      nums.append(el("strong", "", fmtNum(res.visitors)), el("span", "", " visitors · " + fmtPct(res.conversion)));
      row.appendChild(nums);
      const track = el("div", "an-track");
      const fill = el("i");
      fill.style.width = (Math.max(0, Math.min(1, res.conversion)) * 100).toFixed(1) + "%";
      track.appendChild(fill);
      row.appendChild(track);
      if (i > 0) {
        const prev = (steps[i - 1] || {}).visitors || 0;
        row.appendChild(el("span", "an-drop", res.drop_off ? "−" + fmtNum(res.drop_off) + " dropped off (" + fmtPct(prev ? res.drop_off / prev : 0) + ")" : "no one dropped off"));
      }
      card.appendChild(row);
    });
    return card;
  }

  function field(label, input, help) {
    const o = el("div", "opt");
    const l = el("label", "", label);
    if (input.id) l.htmlFor = input.id;
    o.append(l, input);
    if (help) o.appendChild(el("span", "sub", help));
    return o;
  }

  function stepTypeSelect(value) {
    const sel = el("select", "an-type");
    for (const [v, label] of [["path", "Page path"], ["event", "Event"]]) {
      const o = el("option", "", label);
      o.value = v;
      sel.appendChild(o);
    }
    sel.value = value === "event" ? "event" : "path";
    return sel;
  }

  function funnelEditor(f) {
    const draft = f ? JSON.parse(JSON.stringify(f)) :
      { id: newID("f"), name: "", steps: [{ type: "path", value: "/" }, { type: "path", value: "" }], window_minutes: 60 };
    const card = el("section", "card an-editor");
    card.appendChild(el("h3", "", f ? "Edit funnel" : "New funnel"));
    const form = el("div", "an-form");
    const name = el("input");
    name.type = "text";
    name.id = "an-fn-name";
    name.maxLength = 100;
    name.value = draft.name;
    name.placeholder = "e.g. Sign-up";
    const win = el("input");
    win.type = "number";
    win.id = "an-fn-window";
    win.min = "1";
    win.max = "1440";
    win.value = String(draft.window_minutes || 60);
    const stepsBox = el("div", "an-steps-edit");
    const addStep = button("Add step", "", () => { draft.steps.push({ type: "path", value: "" }); paintSteps(); });
    function paintSteps() {
      stepsBox.textContent = "";
      draft.steps.forEach((st, i) => {
        const r = el("div", "an-steprow");
        const type = stepTypeSelect(st.type);
        type.setAttribute("aria-label", "Step " + (i + 1) + " type");
        type.addEventListener("change", () => { st.type = type.value; val.placeholder = st.type === "event" ? "signup" : "/pricing"; });
        const val = el("input");
        val.type = "text";
        val.value = st.value;
        val.maxLength = 300;
        val.placeholder = st.type === "event" ? "signup" : "/pricing";
        val.setAttribute("aria-label", "Step " + (i + 1));
        val.addEventListener("input", () => { st.value = val.value; });
        const rm = button("Remove", "", () => { draft.steps.splice(i, 1); paintSteps(); });
        rm.disabled = draft.steps.length <= 2;
        r.append(el("span", "no", String(i + 1)), type, val, rm);
        stepsBox.appendChild(r);
      });
      addStep.disabled = draft.steps.length >= 8;
    }
    paintSteps();
    const err = el("p", "inline-status err an-ferr");
    const save = button("Save funnel", "primary", async () => {
      draft.name = name.value.trim();
      draft.window_minutes = parseInt(win.value, 10);
      draft.steps = draft.steps.map((s) => ({ type: s.type, value: s.value.trim() }));
      err.textContent = "";
      if (!draft.name) { err.textContent = "Give the funnel a name."; return; }
      if (!(draft.window_minutes >= 1 && draft.window_minutes <= 1440)) { err.textContent = "The window is 1 to 1440 minutes."; return; }
      if (draft.steps.some((s) => !s.value || (s.type === "path" && s.value[0] !== "/"))) {
        err.textContent = "Every step needs a value; a page path starts with /.";
        return;
      }
      const list = settingsOf(S.state).funnels.slice();
      const i = list.findIndex((x) => x.id === draft.id);
      if (i >= 0) list[i] = draft; else list.push(draft);
      save.disabled = true;
      const ok = await saveList("funnels", list, err);
      save.disabled = false;
      if (ok) { S.editing = null; toast("Funnel saved"); render(true); }
    });
    const cancel = button("Cancel", "", () => { S.editing = null; render(true); });
    const addRow = el("div", "row");
    addRow.appendChild(addStep);
    form.append(
      field("Name", name),
      field("Window, in minutes", win, "How long a visitor may take from one step to the next (1 to 1440)."),
      el("div", "optlabel", "Steps, in order (2 to 8)"), stepsBox, addRow,
      el("p", "sub", PATH_HELP + " A visitor is known for one day, so a funnel never links two days."),
      err);
    const btns = el("div", "row");
    btns.append(save, cancel);
    form.appendChild(btns);
    card.appendChild(form);
    setTimeout(() => name.focus(), 0);
    return card;
  }

  async function renderGoals(gen) {
    busy(true);
    let res = { goals: [] };
    if (!noData()) {
      try {
        res = await api("GET", "/analytics/goals" + query());
      } catch (err) { return loadFailed(gen, err); }
    }
    if (gen !== S.gen) return;
    const saved = settingsOf(S.state).goals;
    const byID = {};
    for (const r of res.goals || []) byID[r.id] = r;
    const frag = document.createDocumentFragment();
    const off = offNote();
    if (off) frag.appendChild(off);
    const bar = el("div", "row an-bar-actions");
    const add = button("Add goal", "primary", () => { S.editing = { kind: "goal", id: "" }; render(true); });
    add.disabled = saved.length >= 30 || !!S.editing;
    bar.append(add, el("span", "sub", saved.length + " of 30 goals"));
    frag.appendChild(bar);
    if (S.editing && S.editing.kind === "goal" && !S.editing.id) frag.appendChild(goalEditor(null));
    if (!saved.length && !S.editing) {
      const c = el("section", "card an-empty");
      c.appendChild(el("h3", "", "No goals yet"));
      c.appendChild(el("p", "sub", "A goal counts the visitors who reach a page or send an event, and their share of all visitors."));
      frag.appendChild(c);
    }
    const list = el("section", "card an-goals");
    let shown = 0;
    for (const g of saved) {
      if (S.editing && S.editing.kind === "goal" && S.editing.id === g.id) { frag.appendChild(goalEditor(g)); continue; }
      list.appendChild(goalRow(g, byID[g.id]));
      shown++;
    }
    if (shown) {
      list.insertBefore(el("h3", "", "Goals"), list.firstChild);
      frag.appendChild(list);
    }
    body().replaceChildren(frag);
    busy(false);
  }

  function goalRow(g, r) {
    r = r || { completions: 0, visitors: 0, rate: 0 };
    const row = el("div", "an-goal");
    const head = el("div", "an-goalhead");
    const what = el("div", "an-goalname");
    what.append(el("strong", "an-title", g.name), stepText(g));
    const acts = el("span", "an-acts");
    acts.append(
      button("Edit", "", () => { S.editing = { kind: "goal", id: g.id }; render(true); }),
      armedDelete("Delete", async () => {
        if (await saveList("goals", settingsOf(S.state).goals.filter((x) => x.id !== g.id))) { toast("Goal deleted"); render(true); }
      }));
    head.append(what, acts);
    const nums = el("div", "an-goalnums");
    nums.append(el("strong", "", fmtNum(r.visitors)), el("span", "", " visitors · " + fmtPct(r.rate) + " of all · " +
      fmtNum(r.completions) + " completion" + (r.completions === 1 ? "" : "s")));
    const track = el("div", "an-track");
    const fill = el("i");
    fill.style.width = (Math.max(0, Math.min(1, r.rate)) * 100).toFixed(1) + "%";
    track.appendChild(fill);
    row.append(head, nums, track);
    return row;
  }

  function goalEditor(g) {
    const draft = g ? Object.assign({}, g) : { id: newID("g"), name: "", type: "path", value: "" };
    const card = el("section", "card an-editor");
    card.appendChild(el("h3", "", g ? "Edit goal" : "New goal"));
    const form = el("div", "an-form");
    const name = el("input");
    name.type = "text";
    name.id = "an-goal-name";
    name.maxLength = 100;
    name.value = draft.name;
    name.placeholder = "e.g. Contact form sent";
    const type = stepTypeSelect(draft.type);
    type.id = "an-goal-type";
    const val = el("input");
    val.type = "text";
    val.id = "an-goal-value";
    val.maxLength = 300;
    val.value = draft.value;
    val.placeholder = draft.type === "event" ? "sitebin:form" : "/thanks";
    type.addEventListener("change", () => { val.placeholder = type.value === "event" ? "sitebin:form" : "/thanks"; });
    const err = el("p", "inline-status err an-ferr");
    const save = button("Save goal", "primary", async () => {
      const next = { id: draft.id, name: name.value.trim(), type: type.value, value: val.value.trim() };
      err.textContent = "";
      if (!next.name) { err.textContent = "Give the goal a name."; return; }
      if (!next.value || (next.type === "path" && next.value[0] !== "/")) { err.textContent = "A goal needs a value; a page path starts with /."; return; }
      const list = settingsOf(S.state).goals.slice();
      const i = list.findIndex((x) => x.id === next.id);
      if (i >= 0) list[i] = next; else list.push(next);
      save.disabled = true;
      const ok = await saveList("goals", list, err);
      save.disabled = false;
      if (ok) { S.editing = null; toast("Goal saved"); render(true); }
    });
    const cancel = button("Cancel", "", () => { S.editing = null; render(true); });
    form.append(field("Name", name), field("Counts", type), field("Page path or event name", val, PATH_HELP), err);
    const btns = el("div", "row");
    btns.append(save, cancel);
    form.appendChild(btns);
    card.appendChild(form);
    setTimeout(() => name.focus(), 0);
    return card;
  }

  // ---- realtime ----

  function pollWanted() {
    return S.built && tabs.active === "analytics" && S.sub === "realtime" && !document.hidden &&
      !q("panel-analytics").classList.contains("hidden");
  }

  function stopPoll() {
    S.pollGen++;
    clearTimeout(S.poll);
    S.poll = null;
  }

  let paintRealtime = null;

  // startPoll asks for the last 30 minutes now and every 10 s after each
  // answer, for as long as the sub-tab can be seen.
  function startPoll() {
    stopPoll();
    if (!paintRealtime) return;
    const g = S.pollGen;
    const tick = async () => {
      S.poll = null;
      if (g !== S.pollGen || !pollWanted()) return;
      S.poll = -1; // a request on its way
      try {
        const rt = await api("GET", "/analytics/realtime");
        if (g !== S.pollGen) return;
        paintRealtime(rt);
      } catch (err) {
        if (g !== S.pollGen) return;
        showError(err.message);
        if (err.status === 401 || err.status === 403) {
          // Signed out, or no longer allowed: asking again cannot help.
          // Showing the sub-tab again starts over.
          S.poll = null;
          paintRealtime = null;
          return;
        }
      }
      if (g === S.pollGen && pollWanted()) S.poll = setTimeout(tick, POLL_MS);
      else if (g === S.pollGen) S.poll = null;
    };
    tick();
  }

  function renderRealtime() {
    busy(false);
    if (noData()) { body().replaceChildren(emptyCard()); paintRealtime = null; return; }
    const top = el("section", "card an-rt");
    const hero = el("div", "an-hero");
    const count = el("span", "num", "–");
    const cap = el("span", "cap");
    cap.append(el("i", "an-pulse"), el("span", "", "visitors in the last 5 minutes"));
    hero.append(count, cap);
    const chartBox = el("div");
    top.append(hero, el("h4", "an-sub", "Views per minute, last 30 minutes"), chartBox);
    const grid = el("div", "an-grid");
    const mk = (title) => { const c = el("section", "card an-panel"); c.appendChild(el("h3", "", title)); const b = el("div"); c.appendChild(b); grid.appendChild(c); return b; };
    const pages = mk("Pages"), refs = mk("Referrers"), countries = mk("Countries");
    const latestCard = el("section", "card");
    latestCard.appendChild(el("h3", "", "Latest"));
    const latest = el("div", "an-latest");
    latestCard.appendChild(latest);
    body().replaceChildren(top, grid, latestCard);

    paintRealtime = (rt) => {
      if (!top.isConnected) return;
      showError("");
      count.textContent = fmtNum(rt.active);
      chartBox.replaceChildren(barChart(rt.per_minute || [], "minute", { visitors: false, label: "Views per minute" }));
      // Each table's bars are shares of its own rows: no filter here, and
      // a visitor of the last 30 minutes is counted once per value.
      const table = (box, rows, dim, key) => {
        const all = rows.reduce((a, r) => a + (r[key] || 0), 0);
        box.replaceChildren(rowsList(rows, dim, { share: (r) => (all ? r[key] / all : 0) }));
      };
      table(pages, rt.pages || [], "path", "views");
      table(refs, rt.referrers || [], "referrer", "visitors");
      table(countries, rt.countries || [], "country", "visitors");
      latest.textContent = "";
      const items = rt.latest || [];
      if (!items.length) latest.appendChild(el("p", "sub", "Nothing in the last 30 minutes."));
      for (const r of items) {
        const line = el("div", "an-lrow");
        line.append(
          el("span", "t", fmtTime(r.t, { hour: "2-digit", minute: "2-digit", second: "2-digit" })),
          el("span", "k " + (r.k === "e" ? "ev" : "pv"), r.k === "e" ? "event" : "view"),
          el("span", "w", r.k === "e" ? r.n || "" : r.p || ""),
          el("span", "m", [r.c ? display("country", r.c) : "", r.b, display("device", r.dv || "")].filter(Boolean).join(" · ")));
        latest.appendChild(line);
      }
    };
    startPoll();
  }

  // ---- settings ----

  function isPathView(a) {
    return !!site && a.endsWith("/v/" + site.id) && a.indexOf("/") === a.length - ("/v/" + site.id).length;
  }

  // scriptSnippets is the script tag per mount: one for the addresses at a
  // host's root, one for each host/path address. Path views get none.
  function scriptSnippets() {
    const out = [];
    const mounts = new Map();
    let root = false;
    for (const a of S.state.addresses || []) {
      if (isPathView(a)) continue;
      const i = a.indexOf("/");
      if (i < 0) { root = true; continue; }
      const m = a.slice(i);
      if (!mounts.has(m)) mounts.set(m, []);
      mounts.get(m).push(a);
    }
    if (root) out.push({ label: "", line: '<script defer src="/_sitebin/analytics.js"></script>' });
    for (const [m, addrs] of mounts) {
      out.push({ label: "On " + addrs.join(", ") + ":", line: '<script defer src="' + m + '/_sitebin/analytics.js"></script>' });
    }
    return out;
  }

  function optOutURL(a) {
    const i = a.indexOf("/");
    const host = i < 0 ? a : a.slice(0, i), mount = i < 0 ? "" : a.slice(i);
    let port = "";
    try { port = new URL(site.view_url).port; } catch {}
    try {
      return new URL((location.protocol === "http:" ? "http:" : "https:") + "//" + host + (port ? ":" + port : "") +
        mount + "/_sitebin/analytics/opt-out").href;
    } catch { return ""; }
  }

  const PRIVACY = [
    ["English", "This site counts visits with Sitebin Analytics, run by our host on our behalf. It stores no IP " +
      "addresses and cannot recognise you on another day or on another site. It sets no cookies, except one if you ask " +
      "not to be counted. We see which pages are read, where visitors came from, their browser, device and country, " +
      "and actions such as sending a form."],
    ["Deutsch", "Diese Website zählt Besuche mit Sitebin Analytics, das unser Hoster in unserem Auftrag betreibt. Es " +
      "speichert keine IP-Adressen und kann Sie weder an einem anderen Tag noch auf einer anderen Website wiedererkennen. " +
      "Es setzt keine Cookies, außer einem, wenn Sie nicht gezählt werden möchten. Wir sehen, welche Seiten gelesen " +
      "werden, woher Besucher kommen, Browser, Gerät und Land sowie Aktionen wie das Absenden eines Formulars."],
  ];

  function lines(text, sep) {
    return text.split(sep).map((s) => s.trim()).filter(Boolean);
  }

  function renderSettings() {
    busy(false);
    const st = S.state;
    const set = settingsOf(st);
    const frag = document.createDocumentFragment();
    const errs = {};
    const ferr = (name) => { const p = el("p", "inline-status err an-ferr"); errs[name] = p; return p; };
    const dirty = () => { unsaved.classList.remove("hidden"); };

    // collection: the switch and the mode
    const c1 = el("section", "card an-form");
    c1.appendChild(el("h3", "", "Collection"));
    const on = switchEl("Count this site's visits", set.enabled, dirty);
    const onInput = on.querySelector("input");
    c1.appendChild(on);
    c1.appendChild(el("p", "sub", "No cookies (only one, if a visitor asks not to be counted), no IP addresses: a visitor is known for one day only, by an id that cannot be traced back."));
    const seg = el("div", "seg");
    seg.setAttribute("role", "radiogroup");
    seg.setAttribute("aria-label", "How visits are counted");
    for (const [v, label] of [["server", "Server"], ["script", "Script"]]) {
      const l = el("label");
      const r = el("input");
      r.type = "radio";
      r.name = "an-mode";
      r.value = v;
      r.checked = set.mode === v;
      r.addEventListener("change", () => { dirty(); paintModeNote(); });
      l.append(r, el("span", "", label));
      seg.appendChild(l);
    }
    const modeNote = el("p", "sub");
    const mode = () => (seg.querySelector("input:checked") || {}).value || "server";
    function paintModeNote() {
      modeNote.textContent = mode() === "script"
        ? "Script: only what the script on your pages sends is counted -- page views, titles, screen sizes and events. " +
          "Pages without it are not counted, and neither are visitors without JavaScript. This matches Umami's numbers."
        : "Server: every page a browser opens is counted here, with no change to the site. Bots and prefetches are left out. " +
          "The script on your pages can add events; its page views are ignored, so nothing counts twice.";
    }
    paintModeNote();
    c1.append(field("How visits are counted", seg), modeNote, ferr("mode"), ferr("enabled"));
    frag.appendChild(c1);

    // the script
    const c2 = el("section", "card");
    c2.appendChild(el("h3", "", "Script"));
    const snips = scriptSnippets();
    if (!snips.length) {
      c2.appendChild(el("p", "", "This site is reached only through its path view (/v/" + site.id + "/), where the script and " +
        "the opt-out page are not available. In server mode its page views are still counted; in script mode nothing is."));
    } else {
      c2.appendChild(el("p", "sub", "Add it to your pages for events (and, in script mode, for page views):"));
      for (const s of snips) {
        if (s.label) c2.appendChild(el("p", "sub", s.label));
        c2.appendChild(cmdBlock(s.line));
      }
      c2.appendChild(el("p", "sub", "Moving from Umami: replace Umami's script src with this one and keep its data- attributes; " +
        "umami.track and data-umami-event keep working."));
      c2.appendChild(el("p", "sub", "Browsers may keep the previous version of the script for up to an hour after analytics is turned on or off."));
      if ((st.addresses || []).some(isPathView)) {
        c2.appendChild(el("p", "sub", "The path view /v/" + site.id + "/ has no script and no opt-out page."));
      }
    }
    frag.appendChild(c2);

    // what is counted
    const c3 = el("section", "card an-form");
    c3.appendChild(el("h3", "", "What is counted"));
    const addrs = st.addresses || [];
    const every = switchEl("Count every address of this site", !set.addresses.length, () => { dirty(); paintAddrs(); });
    const everyInput = every.querySelector("input");
    const addrBox = el("div", "an-addrs");
    for (const a of addrs) {
      const l = el("label", "chk");
      const c = el("input");
      c.type = "checkbox";
      c.value = a;
      c.checked = set.addresses.includes(a);
      c.addEventListener("change", dirty);
      l.append(c, el("span", "", a));
      addrBox.appendChild(l);
    }
    function paintAddrs() { addrBox.classList.toggle("hidden", everyInput.checked); }
    paintAddrs();
    c3.append(field("Addresses", every), addrBox, ferr("addresses"));
    const ex = el("textarea");
    ex.id = "an-exclude";
    ex.rows = 4;
    ex.spellcheck = false;
    ex.value = set.exclude.join("\n");
    ex.placeholder = "/admin/**";
    ex.addEventListener("input", dirty);
    c3.append(field("Never count these paths", ex, "One path per line, at most 50. * stands for any characters within one " +
      "path segment, ** for any characters at all: /admin/** leaves out everything under /admin/, /drafts/* only one level."), ferr("exclude"));
    const qp = el("textarea");
    qp.id = "an-query";
    qp.rows = 2;
    qp.spellcheck = false;
    qp.value = set.query.join("\n");
    qp.placeholder = "ref";
    qp.addEventListener("input", dirty);
    c3.append(field("Also keep these query parameters", qp, "Names, one per line, at most 20. The utm_* parameters are " +
      "always kept; ad click ids are kept as names only, never their values."), ferr("query"));
    const zone = el("select");
    zone.id = "an-tz";
    const dflt = el("option", "", "Default (" + (set.timezone ? "the instance's" : st.effective_timezone) + ")");
    dflt.value = "";
    zone.appendChild(dflt);
    let zones = [];
    try { zones = Intl.supportedValuesOf("timeZone"); } catch {}
    if (!zones.includes("UTC")) zones = ["UTC"].concat(zones);
    if (set.timezone && !zones.includes(set.timezone)) zones = [set.timezone].concat(zones);
    for (const z of zones) {
      const o = el("option", "", z);
      o.value = z;
      zone.appendChild(o);
    }
    zone.value = set.timezone;
    zone.addEventListener("change", dirty);
    c3.append(field("Time zone", zone, "Days and hours are counted in it. A change applies from the next visit on."), ferr("timezone"));
    const saveRow = el("div", "row");
    const general = ferr("_");
    const unsaved = el("span", "sub hidden", "Unsaved changes");
    const save = button("Save settings", "primary", async () => {
      for (const k in errs) errs[k].textContent = "";
      const next = settingsOf(S.state);
      next.enabled = onInput.checked;
      next.mode = mode();
      next.addresses = everyInput.checked ? [] : Array.from(addrBox.querySelectorAll("input:checked"), (i) => i.value);
      if (!everyInput.checked && !next.addresses.length) {
        errs.addresses.textContent = "Tick at least one address, or count every address.";
        return;
      }
      next.exclude = lines(ex.value, /\n/);
      next.query = lines(qp.value, /[\s,]+/);
      next.timezone = zone.value;
      save.disabled = true;
      try {
        await putSettings(next);
        toast("Settings saved");
        render(true);
      } catch (err) {
        const box = errs[err.field] || general;
        box.textContent = err.message;
        box.scrollIntoView({ block: "nearest" });
      } finally { save.disabled = false; }
    });
    saveRow.append(save, unsaved);
    c3.append(saveRow, general);
    frag.appendChild(c3);

    // the opt-out
    const c4 = el("section", "card");
    c4.appendChild(el("h3", "", "Opt-out"));
    const counted = (set.addresses.length ? set.addresses : addrs).filter((a) => !isPathView(a));
    if (!counted.length) {
      c4.appendChild(el("p", "sub", "There is no opt-out page on a path view (/v/" + site.id + "/)."));
    } else {
      c4.appendChild(el("p", "sub", "Link this page from your privacy policy: one click there and this browser is no longer counted."));
      const ul = el("ul", "an-links");
      for (const a of counted) {
        const href = optOutURL(a);
        if (!href) continue;
        const li = el("li");
        const link = el("a", "", href);
        link.href = href;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        li.appendChild(link);
        ul.appendChild(li);
      }
      c4.appendChild(ul);
    }
    frag.appendChild(c4);

    // storage
    const c5 = el("section", "card");
    c5.appendChild(el("h3", "", "Storage"));
    const dl = el("dl", "an-facts");
    const fact = (k, v) => { dl.appendChild(el("dt", "", k)); dl.appendChild(el("dd", "", v)); };
    fact("Stored", fmtBytes(st.bytes || 0));
    fact("First day", st.first_day || "nothing yet");
    fact("Kept for", st.retention_days ? st.retention_days + " days, then deleted day by day" : "as long as the site exists");
    fact("Daily limit", st.daily_cap ? fmtNum(st.daily_cap) + " records a day; the rest are counted, not stored" : "none");
    if ((st.imported || []).length) fact("Imported from Umami", st.imported.map((r) => r.from + " – " + r.to).join(", "));
    c5.appendChild(dl);
    c5.appendChild(el("p", "sub", "Deleting the site deletes its analytics. They are not counted against the site's storage."));
    frag.appendChild(c5);

    // the privacy policy paragraph
    const c6 = el("section", "card");
    c6.appendChild(el("h3", "", "For your privacy policy"));
    c6.appendChild(el("p", "sub", "You decide to count visits, so you are responsible for it; Sitebin processes the statistics on your behalf. " +
      "A paragraph to copy:"));
    for (const [lang, text] of PRIVACY) {
      const b = el("div", "an-quote");
      b.appendChild(el("div", "lang", lang));
      b.appendChild(el("p", "", text));
      b.appendChild(button("Copy", "", () => copy(text)));
      c6.appendChild(b);
    }
    frag.appendChild(c6);

    body().replaceChildren(frag);
  }

  return { show, state: S };
})();
