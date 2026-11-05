// Sitebin viewer: the file list. Draws a folder as a details table or a grid
// of previews, with a filter, sorting and a tile-size slider, and remembers
// the visitor's choices. Every file name is set as text, never as markup.
"use strict";

import {
  baseName, dirName, iconFor, typeLabel, rendererFor, itemCount,
  folderHash, fileHash, sortEntries, filterTree, defaultView,
  fmtBytes, fmtDate, plural, SORT_KEYS,
} from "./files.js";

const ASSETS = "/_sitebin/assets";

export const TILE_MIN = 96, TILE_MAX = 288, TILE_DEFAULT = 160;
const PDF_PREVIEW_MAX = 25 * 1024 * 1024;

// ---- the visitor's choices ----

const DEFAULT_PREFS = { view: null, sort: "name", dir: "asc", tile: TILE_DEFAULT };

function prefsKey(base) {
  return "sitebin-viewer:" + base;
}

export function loadPrefs(base) {
  const p = { ...DEFAULT_PREFS };
  try {
    const saved = JSON.parse(localStorage.getItem(prefsKey(base)) || "{}");
    if (saved.view === "grid" || saved.view === "details") p.view = saved.view;
    if (SORT_KEYS.includes(saved.sort)) p.sort = saved.sort;
    if (saved.dir === "asc" || saved.dir === "desc") p.dir = saved.dir;
    if (Number.isFinite(saved.tile)) p.tile = Math.min(TILE_MAX, Math.max(TILE_MIN, saved.tile));
  } catch { /* storage unavailable: defaults */ }
  return p;
}

export function savePrefs(base, prefs) {
  try { localStorage.setItem(prefsKey(base), JSON.stringify(prefs)); } catch { /* not kept */ }
}

// orderedFiles is a folder's files in the visitor's order: what ‹ › walks.
export function orderedFiles(node, prefs) {
  return sortEntries([], node.files, prefs.sort, prefs.dir).files;
}

// ---- small DOM helpers ----

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

const ICONS = {
  folder: '<path d="M3 7.5A2.5 2.5 0 0 1 5.5 5h4l2 2.2h7A2.5 2.5 0 0 1 21 9.7v7.8a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 17.5z"/>',
  file: '<path d="M6 3h8l5 5v11.5A1.5 1.5 0 0 1 17.5 21h-11A1.5 1.5 0 0 1 5 19.5v-15A1.5 1.5 0 0 1 6.5 3z"/><path d="M14 3v5h5"/>',
  image: '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><circle cx="9" cy="10" r="1.8"/><path d="M4 17l5-4.5 3.5 3 3-2.5L20 17"/>',
  video: '<rect x="3.5" y="5.5" width="17" height="13" rx="2"/><path d="M10.5 9.5v5l4.2-2.5z"/>',
  audio: '<path d="M9 17.5V6l10-2v11.5"/><circle cx="6.5" cy="17.5" r="2.5"/><circle cx="16.5" cy="15.5" r="2.5"/>',
  pdf: '<path d="M6 3h8l5 5v11.5A1.5 1.5 0 0 1 17.5 21h-11A1.5 1.5 0 0 1 5 19.5v-15A1.5 1.5 0 0 1 6.5 3z"/><path d="M14 3v5h5M8.5 13h7M8.5 16.5h5"/>',
  doc: '<path d="M6 3h8l5 5v11.5A1.5 1.5 0 0 1 17.5 21h-11A1.5 1.5 0 0 1 5 19.5v-15A1.5 1.5 0 0 1 6.5 3z"/><path d="M14 3v5h5M8.5 12h7M8.5 15h7M8.5 18h4"/>',
  table: '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="M3.5 9.5h17M3.5 14.5h17M9.5 4.5v15"/>',
  code: '<path d="M9 8l-4 4 4 4M15 8l4 4-4 4"/>',
  archive: '<rect x="4" y="3.5" width="16" height="17" rx="2"/><path d="M12 3.5v2M12 7.5v2M12 11.5v2"/><rect x="10.3" y="14" width="3.4" height="3.6" rx=".8"/>',
  app: '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="M3.5 8.5h17M7 6.5h.01M9.5 6.5h.01M12 12.5v4.5M9.8 14.8l2.2 2.2 2.2-2.2"/>',
  font: '<path d="M5 19l5.5-14h3L19 19M7.6 13.5h8.8"/>',
  download: '<path d="M12 4v11M7.5 10.5L12 15l4.5-4.5M5 19.5h14"/>',
  details: '<path d="M4 6h16M4 12h16M4 18h16"/>',
  grid: '<rect x="4" y="4" width="6.5" height="6.5" rx="1.2"/><rect x="13.5" y="4" width="6.5" height="6.5" rx="1.2"/><rect x="4" y="13.5" width="6.5" height="6.5" rx="1.2"/><rect x="13.5" y="13.5" width="6.5" height="6.5" rx="1.2"/>',
  up: '<path d="M12 19V5M6 11l6-6 6 6"/>',
  prev: '<path d="M15 5l-7 7 7 7"/>',
  next: '<path d="M9 5l7 7-7 7"/>',
  down: '<path d="M12 5v14M6 13l6 6 6-6"/>',
};

// icon builds an inline SVG from the constant table above (no user data).
export function icon(name, cls) {
  const s = el("span", "v-ico" + (cls ? " " + cls : ""));
  s.setAttribute("aria-hidden", "true");
  s.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">' + (ICONS[name] || ICONS.file) + "</svg>";
  return s;
}

// ---- previews ----

// Previews load only for tiles on screen. PDF first pages render two at a
// time and are kept for the page's lifetime; a queued one whose tiles have
// all left the page (scrolled past in a folder that was left, a new filter)
// is dropped before it fetches anything.
const pdfPreviews = new Map(); // path -> { promise, thumbs: Set<Element>, url }
const PDF_PREVIEWS_KEPT = 200;
const pdfKept = []; // paths whose blob: URL is held, oldest first

// keepPdfPreview holds a rendered page, and lets the oldest go past the cap:
// a long scroll through a PDF folder must not keep every page in memory. An
// image already on screen keeps its picture when its URL is revoked.
function keepPdfPreview(path, url) {
  pdfKept.push(path);
  while (pdfKept.length > PDF_PREVIEWS_KEPT) {
    const old = pdfPreviews.get(pdfKept.shift());
    if (old && old.url) {
      URL.revokeObjectURL(old.url);
      pdfPreviews.delete(old.path);
    }
  }
}
let pdfWorker = null;
let pdfRunning = 0;
const pdfQueue = [];

// loadPdfjs loads the vendored pdf.js once, with ONE worker for the whole
// page: the file view and the previews share it, so stepping through a
// folder of PDFs never starts a worker per file.
export function loadPdfjs() {
  return import(ASSETS + "/vendor/pdf.min.mjs").then((pdfjs) => {
    pdfjs.GlobalWorkerOptions.workerSrc = ASSETS + "/vendor/pdf.worker.min.mjs";
    if (!pdfWorker) pdfWorker = new pdfjs.PDFWorker();
    return { pdfjs, worker: pdfWorker };
  });
}

function runPdfQueue() {
  while (pdfRunning < 2 && pdfQueue.length) {
    const job = pdfQueue.shift();
    pdfRunning++;
    job().finally(() => { pdfRunning--; runPdfQueue(); });
  }
}

function pdfPreview(url, path, width, thumb) {
  const known = pdfPreviews.get(path);
  if (known) {
    known.thumbs.add(thumb);
    return known.promise;
  }
  const entry = { promise: null, thumbs: new Set([thumb]), url: null, path };
  entry.promise = new Promise((resolve) => {
    pdfQueue.push(async () => {
      if (![...entry.thumbs].some((t) => t.isConnected)) {
        // Nobody is looking any more: drop it, and let a later tile ask again.
        pdfPreviews.delete(path);
        resolve(null);
        return;
      }
      let doc = null;
      try {
        const { pdfjs, worker } = await loadPdfjs();
        // Range requests only: a large PDF is never fetched whole for a tile.
        doc = await pdfjs.getDocument({
          url, worker, disableAutoFetch: true, disableStream: true,
        }).promise;
        const page = await doc.getPage(1);
        const base = page.getViewport({ scale: 1 });
        const vp = page.getViewport({ scale: width / base.width });
        const canvas = document.createElement("canvas");
        canvas.width = Math.ceil(vp.width);
        canvas.height = Math.ceil(vp.height);
        await page.render({ canvasContext: canvas.getContext("2d"), viewport: vp }).promise;
        const blob = await new Promise((r) => canvas.toBlob(r, "image/jpeg", 0.85));
        if (!blob) { resolve(null); return; }
        entry.url = URL.createObjectURL(blob);
        keepPdfPreview(path, entry.url);
        resolve(entry.url);
      } catch {
        resolve(null);
      } finally {
        if (doc) doc.destroy();
      }
    });
    runPdfQueue();
  });
  pdfPreviews.set(path, entry);
  return entry.promise;
}

function showPreview(thumb, media) {
  media.classList.add("v-thumb-media");
  thumb.appendChild(media);
  thumb.classList.add("has-media");
}

function loadPreview(thumb, file, rawURL) {
  const kind = rendererFor(file.path);
  if (kind === "image") {
    const img = new Image();
    img.decoding = "async";
    img.alt = "";
    img.onload = () => showPreview(thumb, img);
    img.src = rawURL(file.path);
  } else if (kind === "video") {
    // One frame becomes a still and the player is let go at once: browsers
    // allow only a few media players per page (fewer on phones), so a tile
    // must not keep one.
    const v = document.createElement("video");
    v.muted = true;
    v.playsInline = true;
    v.preload = "metadata";
    const release = () => { v.removeAttribute("src"); v.load(); };
    v.onloadeddata = () => {
      try {
        const scale = Math.min(1, 480 / (v.videoWidth || 480));
        const c = document.createElement("canvas");
        c.width = Math.max(1, Math.round((v.videoWidth || 480) * scale));
        c.height = Math.max(1, Math.round((v.videoHeight || 270) * scale));
        c.getContext("2d").drawImage(v, 0, 0, c.width, c.height);
        c.toBlob((blob) => {
          release();
          if (!blob || !thumb.isConnected) return;
          const url = URL.createObjectURL(blob);
          const img = new Image();
          img.alt = "";
          img.onload = () => { URL.revokeObjectURL(url); showPreview(thumb, img); };
          img.src = url;
        }, "image/jpeg", 0.85);
      } catch {
        release();
      }
    };
    v.onerror = release;
    v.src = rawURL(file.path) + "#t=0.5";
  } else if (kind === "pdf" && file.size <= PDF_PREVIEW_MAX) {
    const width = Math.min(480, Math.round(TILE_MAX * Math.min(window.devicePixelRatio || 1, 2)));
    pdfPreview(rawURL(file.path), file.path, width, thumb).then((url) => {
      if (!url || !thumb.isConnected) return;
      const img = new Image();
      img.alt = "";
      img.onload = () => showPreview(thumb, img);
      img.src = url;
      thumb.classList.add("is-page");
    });
  }
}

function hasPreview(file) {
  const kind = rendererFor(file.path);
  return kind === "image" || kind === "video" || (kind === "pdf" && file.size <= PDF_PREVIEW_MAX);
}

// ---- the folder view ----

// renderFolderView draws node into stage. ctx: { prefs, base, rawURL,
// isRoot, onPrefs(prefs) }. It returns a cleanup function.
export function renderFolderView(stage, node, ctx) {
  const prefs = ctx.prefs;
  let query = "";
  let observer = null;

  const wrap = el("section", "v-files");
  const bar = el("div", "v-toolbar");
  const filter = el("input", "v-filter");
  filter.type = "search";
  filter.placeholder = node.path ? "Filter this folder" : "Filter all files";
  filter.setAttribute("aria-label", filter.placeholder);
  const summary = el("span", "v-summary");
  const spacer = el("span", "v-spacer");

  const sortSel = el("select", "v-sort");
  sortSel.setAttribute("aria-label", "Sort by");
  for (const [k, label] of [["name", "Name"], ["mtime", "Modified"], ["size", "Size"], ["type", "Type"]]) {
    const o = el("option", null, "Sort: " + label);
    o.value = k;
    sortSel.appendChild(o);
  }
  const dirBtn = el("button", "v-tbtn v-dir");
  dirBtn.type = "button";

  const views = el("div", "v-seg");
  views.setAttribute("role", "group");
  views.setAttribute("aria-label", "View");
  const detailsBtn = el("button", "v-tbtn");
  detailsBtn.type = "button";
  detailsBtn.title = "Details";
  detailsBtn.setAttribute("aria-label", "Details view");
  detailsBtn.appendChild(icon("details"));
  const gridBtn = el("button", "v-tbtn");
  gridBtn.type = "button";
  gridBtn.title = "Grid";
  gridBtn.setAttribute("aria-label", "Grid view");
  gridBtn.appendChild(icon("grid"));
  views.append(detailsBtn, gridBtn);

  const slider = el("input", "v-slider");
  slider.type = "range";
  slider.min = String(TILE_MIN);
  slider.max = String(TILE_MAX);
  slider.step = "8";
  slider.setAttribute("aria-label", "Tile size");

  bar.append(filter, summary, spacer, sortSel, dirBtn, views, slider);
  const listing = el("div", "v-listing");
  wrap.append(bar, listing);
  stage.appendChild(wrap);

  const view = () => prefs.view || defaultView(node);

  const commit = () => {
    ctx.onPrefs(prefs);
    draw();
  };

  filter.addEventListener("input", () => { query = filter.value; draw(); });
  sortSel.addEventListener("change", () => { prefs.sort = sortSel.value; commit(); });
  dirBtn.addEventListener("click", () => { prefs.dir = prefs.dir === "asc" ? "desc" : "asc"; commit(); });
  detailsBtn.addEventListener("click", () => { prefs.view = "details"; commit(); });
  gridBtn.addEventListener("click", () => { prefs.view = "grid"; commit(); });
  slider.addEventListener("input", () => {
    prefs.tile = Number(slider.value);
    const grid = listing.querySelector(".v-grid");
    if (grid) grid.style.setProperty("--tile", prefs.tile + "px");
  });
  slider.addEventListener("change", () => ctx.onPrefs(prefs));

  function syncToolbar() {
    const v = view();
    sortSel.value = prefs.sort;
    dirBtn.textContent = "";
    dirBtn.appendChild(icon(prefs.dir === "asc" ? "up" : "down"));
    dirBtn.title = prefs.dir === "asc" ? "Ascending" : "Descending";
    dirBtn.setAttribute("aria-label", "Sort " + (prefs.dir === "asc" ? "ascending" : "descending") + ", click to reverse");
    detailsBtn.setAttribute("aria-pressed", String(v === "details"));
    gridBtn.setAttribute("aria-pressed", String(v === "grid"));
    slider.value = String(prefs.tile);
    slider.hidden = v !== "grid";
  }

  function draw() {
    if (observer) { observer.disconnect(); observer = null; }
    syncToolbar();
    listing.textContent = "";
    const filtering = query.trim() !== "";
    const src = filtering ? filterTree(node, query) : { folders: [...node.folders.values()], files: node.files };
    const { folders, files } = sortEntries(src.folders, src.files, prefs.sort, prefs.dir);

    if (filtering) {
      summary.textContent = plural(folders.length + files.length, "match", "matches");
    } else {
      const parts = [];
      if (node.folders.size) parts.push(plural(node.folders.size, "folder", "folders"));
      parts.push(plural(node.files.length, "file", "files"));
      if (node.count) parts.push(fmtBytes(node.size));
      summary.textContent = parts.join(" · ");
    }

    if (!folders.length && !files.length) {
      const msg = filtering ? "Nothing here matches “" + query.trim() + "”."
        : ctx.isRoot ? "This site has no files yet." : "This folder is empty.";
      listing.appendChild(el("p", "v-msg", msg));
      return;
    }
    if (view() === "grid") drawGrid(folders, files, filtering);
    else drawDetails(folders, files, filtering);
  }

  // where shows, under a filter result, the folder it sits in.
  function where(path) {
    const parent = dirName(path);
    const rel = node.path ? parent.slice(node.path.length + (parent.length > node.path.length ? 1 : 0)) : parent;
    return rel ? "in " + rel : "";
  }

  function drawDetails(folders, files, filtering) {
    const table = el("table", "v-details");
    const thead = el("thead");
    const htr = el("tr");
    const cols = [["name", "Name", ""], ["size", "Size", "col-size"], ["type", "Type", "col-type"], ["mtime", "Modified", "col-mod"]];
    for (const [key, label, cls] of cols) {
      const th = el("th", cls);
      th.scope = "col";
      const b = el("button", "v-colbtn", label);
      b.type = "button";
      if (prefs.sort === key) {
        th.setAttribute("aria-sort", prefs.dir === "asc" ? "ascending" : "descending");
        b.appendChild(icon(prefs.dir === "asc" ? "up" : "down", "v-sorted"));
      }
      b.addEventListener("click", () => {
        if (prefs.sort === key) prefs.dir = prefs.dir === "asc" ? "desc" : "asc";
        else { prefs.sort = key; prefs.dir = key === "mtime" || key === "size" ? "desc" : "asc"; }
        commit();
      });
      th.appendChild(b);
      htr.appendChild(th);
    }
    const dlh = el("th", "col-dl");
    dlh.appendChild(el("span", "v-sr", "Download"));
    htr.appendChild(dlh);
    thead.appendChild(htr);
    table.appendChild(thead);

    const tbody = el("tbody");
    const row = (href, iconName, name, sub, size, type, mod, dlPath) => {
      const tr = el("tr");
      const td = el("td", "col-name");
      const a = el("a", "v-rowlink");
      a.href = href;
      a.appendChild(icon(iconName, iconName === "folder" ? "is-folder" : ""));
      const label = el("span", "v-rowname");
      label.appendChild(el("span", "v-fname", name));
      if (sub) label.appendChild(el("span", "v-sub", sub));
      a.appendChild(label);
      td.appendChild(a);
      tr.appendChild(td);
      tr.appendChild(el("td", "col-size", size));
      tr.appendChild(el("td", "col-type", type));
      tr.appendChild(el("td", "col-mod", mod));
      const dl = el("td", "col-dl");
      if (dlPath) {
        const d = el("a", "v-rowdl");
        d.href = ctx.rawURL(dlPath);
        d.setAttribute("download", baseName(dlPath));
        d.title = "Download";
        d.setAttribute("aria-label", "Download " + baseName(dlPath));
        d.appendChild(icon("download"));
        dl.appendChild(d);
      }
      tr.appendChild(dl);
      // The whole row opens the entry; the links inside keep their own clicks.
      tr.addEventListener("click", (e) => {
        if (e.target.closest("a, button") || window.getSelection().toString()) return;
        location.hash = href;
      });
      tbody.appendChild(tr);
    };
    for (const f of folders) {
      row(folderHash(f.path), "folder", f.name, filtering ? where(f.path) : "",
        plural(itemCount(f), "item", "items"), "Folder", fmtDate(f.newest), null);
    }
    for (const f of files) {
      row(fileHash(f.path), iconFor(f.path), f.name, filtering ? where(f.path) : "",
        fmtBytes(f.size), typeLabel(f.path), fmtDate(f.mtime), f.path);
    }
    table.appendChild(tbody);
    listing.appendChild(table);
  }

  function drawGrid(folders, files, filtering) {
    const grid = el("div", "v-grid");
    grid.style.setProperty("--tile", prefs.tile + "px");
    const pending = new Map();
    const tile = (href, iconName, name, meta, sub, file) => {
      const a = el("a", "v-tile");
      a.href = href;
      const thumb = el("div", "v-thumb");
      thumb.appendChild(icon(iconName, iconName === "folder" ? "is-folder" : ""));
      if (file && iconName !== "folder") {
        const ext = baseName(file.path).includes(".") ? baseName(file.path).split(".").pop() : "";
        // The badge stays under a preview (hidden by CSS) for when it fails.
        if (ext) thumb.appendChild(el("span", "v-ext", ext.slice(0, 5).toUpperCase()));
        if (hasPreview(file)) pending.set(thumb, file);
      }
      a.appendChild(thumb);
      a.appendChild(el("span", "v-tile-name", name));
      a.appendChild(el("span", "v-tile-meta", sub ? sub : meta));
      a.title = name;
      grid.appendChild(a);
    };
    for (const f of folders) {
      tile(folderHash(f.path), "folder", f.name, plural(itemCount(f), "item", "items"), filtering ? where(f.path) : "", null);
    }
    for (const f of files) {
      tile(fileHash(f.path), iconFor(f.path), f.name, fmtBytes(f.size), filtering ? where(f.path) : "", f);
    }
    listing.appendChild(grid);
    if (!pending.size) return;
    if (!("IntersectionObserver" in window)) {
      for (const [thumb, file] of pending) loadPreview(thumb, file, ctx.rawURL);
      return;
    }
    observer = new IntersectionObserver((entries) => {
      for (const en of entries) {
        if (!en.isIntersecting) continue;
        observer.unobserve(en.target);
        const file = pending.get(en.target);
        if (file) loadPreview(en.target, file, ctx.rawURL);
      }
    }, { rootMargin: "300px 0px" });
    for (const thumb of pending.keys()) observer.observe(thumb);
  }

  draw();
  return {
    focusFilter: () => filter.focus(),
    cleanup: () => { if (observer) observer.disconnect(); },
  };
}
