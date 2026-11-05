// Sitebin viewer runtime. Loaded by the generated wrapper page on a site's
// own origin; reads its config from the inline JSON. With no file chosen it
// opens on the file list (browser.js); every folder and file has its own
// address in the URL fragment (#/docs/, #/docs/report.pdf), and files render
// from /_raw/.
"use strict";

import {
  rendererFor, buildTree, findFolder, parseRoute, folderHash, fileHash,
  baseName, dirName, resolveRelative, fmtBytes, isHiddenPath,
} from "./files.js";
import { renderFolderView, loadPrefs, savePrefs, orderedFiles, icon, loadPdfjs } from "./browser.js";

const ASSETS = "/_sitebin/assets";
const cfg = JSON.parse(document.getElementById("sitebin-config").textContent);
const app = document.getElementById("viewer-app");
const files = (Array.isArray(cfg.files) ? cfg.files : []).filter((f) => f && !isHiddenPath(f.path));
const tree = buildTree(files);
const fileIndex = new Map(files.map((f) => [f.path, f]));

// RAW_BASE is the wrapper's own directory, so raw-file URLs work whether the
// site is served at a subdomain root ("/") or a /v/<id>/ path.
const RAW_BASE = location.pathname.replace(/[^/]*$/, "");

function rawURL(path) {
  return RAW_BASE + "_raw/" + path.split("/").map(encodeURIComponent).join("/");
}

const prefs = loadPrefs(RAW_BASE);

const loaded = {};
function loadScript(src) {
  if (!loaded[src]) {
    loaded[src] = new Promise((resolve, reject) => {
      const s = document.createElement("script");
      s.src = src;
      s.onload = resolve;
      s.onerror = () => reject(new Error("failed to load " + src));
      document.head.appendChild(s);
    });
  }
  return loaded[src];
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

// ---- chrome ----

function mark() {
  const a = el("a", "v-mark");
  a.href = folderHash("");
  a.title = "All files";
  a.setAttribute("aria-label", "All files");
  a.innerHTML =
    '<svg viewBox="0 0 64 64" aria-hidden="true"><rect x="4" y="4" width="56" height="56" rx="14" fill="#101725" stroke="#2A3550" stroke-width="2"/><rect x="16" y="16" width="32" height="6" rx="3" fill="#F5B84D"/><path d="M32 30v14M25 38l7 7 7-7" stroke="#5B8CFF" stroke-width="4.5" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  return a;
}

// crumbs is the way up: "Files / docs / 2024 / report.pdf", every folder a
// link, the last part the current place.
function crumbs(path, isFile) {
  const nav = el("nav", "v-crumbs");
  nav.setAttribute("aria-label", "Location");
  const segs = path ? path.split("/") : [];
  const last = segs.length - 1;
  const add = (label, href, here) => {
    if (nav.childNodes.length) nav.appendChild(el("span", "v-crumb-sep", "/"));
    if (here) {
      const isLast = nav.querySelectorAll(".v-crumb").length === (path ? path.split("/").length : 0);
      const c = el("span", "v-crumb" + (isLast ? " is-current" + (isFile ? " is-file" : "") : ""), label);
      if (isLast) c.setAttribute("aria-current", "page");
      nav.appendChild(c);
    } else {
      const a = el("a", "v-crumb", label);
      a.href = href;
      nav.appendChild(a);
    }
  };
  add("Files", folderHash(""), segs.length === 0);
  segs.forEach((seg, i) => {
    const folder = segs.slice(0, i + 1).join("/");
    // A folder the list does not show (a chosen entry in .private/) is named,
    // not linked: its address would find nothing.
    add(seg, folderHash(folder), i === last || !findFolder(tree, folder));
  });
  return nav;
}

// nearestFolder is the file's folder, or the closest one above it that the
// list shows.
function nearestFolder(path) {
  let dir = dirName(path);
  while (dir && !findFolder(tree, dir)) dir = dirName(dir);
  return dir;
}

function navButton(label, iconName, href) {
  const a = el("a", "v-navbtn");
  a.setAttribute("aria-label", label);
  a.title = label;
  if (href) a.href = href;
  else a.setAttribute("aria-disabled", "true");
  a.appendChild(icon(iconName));
  return a;
}

// neighbours are the previous and next file in the file's folder, in the
// order the visitor sorted that folder.
function neighbours(path) {
  const node = findFolder(tree, dirName(path));
  if (!node) return null;
  const list = orderedFiles(node, prefs);
  const i = list.findIndex((f) => f.path === path);
  if (i < 0 || list.length < 2) return null;
  return { prev: list[i - 1] || null, next: list[i + 1] || null, pos: i + 1, total: list.length };
}

function fileChrome(path) {
  const bar = el("header", "v-chrome");
  bar.appendChild(mark());
  bar.appendChild(crumbs(path, true));
  const info = fileIndex.get(path);
  if (info) bar.appendChild(el("span", "v-size", fmtBytes(info.size)));
  bar.appendChild(el("span", "v-spacer"));
  const n = neighbours(path);
  if (n) {
    const step = el("div", "v-step");
    step.appendChild(navButton("Previous file", "prev", n.prev && fileHash(n.prev.path)));
    step.appendChild(el("span", "v-pos", n.pos + " / " + n.total));
    step.appendChild(navButton("Next file", "next", n.next && fileHash(n.next.path)));
    bar.appendChild(step);
  }
  const all = el("a", "v-dl v-files-btn");
  all.href = folderHash(nearestFolder(path));
  all.title = "Show the files in this folder";
  all.appendChild(icon("grid"));
  all.appendChild(el("span", "v-btnlabel", "Files"));
  bar.appendChild(all);
  const dl = el("a", "v-dl");
  dl.href = rawURL(path);
  dl.setAttribute("download", baseName(path));
  dl.title = "Download";
  dl.appendChild(icon("download"));
  dl.appendChild(el("span", "v-btnlabel", "Download"));
  bar.appendChild(dl);
  return bar;
}

function folderChrome(path) {
  const bar = el("header", "v-chrome");
  bar.appendChild(mark());
  bar.appendChild(crumbs(path, false));
  bar.appendChild(el("span", "v-spacer"));
  return bar;
}

// ---- renderers ----

// renderPDF draws every page. It stops, and frees the document, as soon as
// the visitor has moved on: stepping through a folder of large PDFs must not
// leave each one downloading and rendering into a page nobody sees.
async function renderPDF(stage, path) {
  const { pdfjs, worker } = await loadPdfjs();
  if (!stage.isConnected) return;
  const task = pdfjs.getDocument({ url: rawURL(path), worker });
  onLeave(() => task.destroy());
  const doc = await task.promise;
  if (!stage.isConnected) return;
  const wrap = el("div", "v-pdf");
  stage.appendChild(wrap);
  const maxWidth = Math.min(stage.clientWidth - 8, 900);
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  for (let i = 1; i <= doc.numPages; i++) {
    if (!stage.isConnected) return;
    const page = await doc.getPage(i);
    const base = page.getViewport({ scale: 1 });
    const scale = maxWidth / base.width;
    const vp = page.getViewport({ scale: scale * dpr });
    const canvas = document.createElement("canvas");
    canvas.width = vp.width;
    canvas.height = vp.height;
    canvas.style.width = Math.floor(vp.width / dpr) + "px";
    wrap.appendChild(canvas);
    await page.render({ canvasContext: canvas.getContext("2d"), viewport: vp }).promise;
  }
}

async function renderMarkdown(stage, path) {
  await Promise.all([
    loadScript(ASSETS + "/vendor/markdown-it.min.js"),
    loadScript(ASSETS + "/vendor/purify.min.js"),
    loadScript(ASSETS + "/vendor/highlight.min.js"),
    loadCSS(ASSETS + "/vendor/highlight-theme.min.css"),
  ]);
  const res = await fetch(rawURL(path));
  if (!res.ok) throw new Error("could not load the file (" + res.status + ")");
  const src = await res.text();
  const md = window.markdownit({
    html: false,
    linkify: true,
    typographer: true,
    highlight: (code, lang) => {
      try {
        if (lang && window.hljs.getLanguage(lang)) {
          return window.hljs.highlight(code, { language: lang }).value;
        }
      } catch {}
      return "";
    },
  });
  const article = el("article", "paper");
  article.innerHTML = window.DOMPurify.sanitize(md.render(src));
  linkSiblings(article, path);
  stage.appendChild(article);
}

// linkSiblings points a document's relative links and images at the site:
// a link to another file of the site opens it in the viewer, an image loads
// from /_raw/. Absolute URLs, site-absolute paths and anchors stay as written.
function linkSiblings(root, docPath) {
  const dir = dirName(docPath);
  const target = (ref) => {
    if (!ref || /^[a-z][a-z0-9+.-]*:/i.test(ref) || ref.startsWith("#") || ref.startsWith("/")) return null;
    const bare = ref.split(/[?#]/)[0];
    if (!bare) return null;
    let decoded;
    try { decoded = decodeURIComponent(bare); } catch { return null; }
    return resolveRelative(dir, decoded);
  };
  for (const a of root.querySelectorAll("a[href]")) {
    const p = target(a.getAttribute("href"));
    if (p == null) continue;
    if (fileIndex.has(p)) a.setAttribute("href", fileHash(p));
    else if (findFolder(tree, p)) a.setAttribute("href", folderHash(p));
  }
  for (const img of root.querySelectorAll("img[src]")) {
    const p = target(img.getAttribute("src"));
    if (p != null && fileIndex.has(p)) img.setAttribute("src", rawURL(p));
  }
}

async function renderDocx(stage, path) {
  await loadScript(ASSETS + "/vendor/jszip.min.js");
  await loadScript(ASSETS + "/vendor/docx-preview.min.js");
  const res = await fetch(rawURL(path));
  if (!res.ok) throw new Error("could not load the file (" + res.status + ")");
  const blob = await res.blob();
  const holder = el("div", "paper docx");
  stage.appendChild(holder);
  await window.docx.renderAsync(blob, holder, null, {
    ignoreLastRenderedPageBreak: true,
    experimental: true,
  });
}

async function renderText(stage, path) {
  await Promise.all([
    loadScript(ASSETS + "/vendor/highlight.min.js"),
    loadCSS(ASSETS + "/vendor/highlight-theme.min.css"),
  ]);
  // At most CAP bytes leave the server: a Range request, so an unknown large
  // file never downloads whole just to be previewed. A server that ignores
  // Range answers 200 with everything, which is cut below as before.
  const CAP = 2 * 1024 * 1024;
  const res = await fetch(rawURL(path), { headers: { Range: "bytes=0-" + CAP } });
  if (!res.ok) throw new Error("could not load the file (" + res.status + ")");
  let bytes = new Uint8Array(await res.arrayBuffer());
  // A NUL byte in the first 8 KiB means binary, as git and grep decide it:
  // offer the download instead of a screen of noise.
  if (bytes.subarray(0, 8192).includes(0)) { renderDownload(stage, path); return; }
  let truncated = false;
  if (bytes.length > CAP) { bytes = bytes.subarray(0, CAP); truncated = true; }
  const text = new TextDecoder().decode(bytes);
  const box = el("div", "v-code");
  const pre = el("pre");
  const code = el("code", null, text);
  const ext = (path.split(".").pop() || "").toLowerCase();
  if (window.hljs.getLanguage(ext)) code.className = "language-" + ext;
  pre.appendChild(code);
  box.appendChild(pre);
  stage.appendChild(box);
  try { window.hljs.highlightElement(code); } catch {}
  if (truncated) stage.appendChild(el("p", "v-msg", "Preview truncated — download for the full file."));
}

// renderDownload is the view for a file the browser cannot show: its name,
// its size and the download, nothing fetched.
function renderDownload(stage, path) {
  const info = fileIndex.get(path);
  const box = el("div", "v-download");
  box.appendChild(el("p", "v-download-name", path.split("/").pop()));
  if (info) box.appendChild(el("p", "v-download-size", fmtBytes(info.size)));
  box.appendChild(el("p", "v-download-note", "No preview for this kind of file."));
  const a = el("a", "v-download-btn", "Download");
  a.href = rawURL(path);
  a.setAttribute("download", path.split("/").pop());
  box.appendChild(a);
  stage.appendChild(box);
}

// parseDelimited splits CSV/TSV text into rows, honoring quoted fields.
function parseDelimited(text, delim) {
  const rows = [];
  let row = [], field = "", i = 0, inQ = false;
  while (i < text.length) {
    const c = text[i];
    if (inQ) {
      if (c === '"') {
        if (text[i + 1] === '"') { field += '"'; i += 2; continue; }
        inQ = false; i++; continue;
      }
      field += c; i++; continue;
    }
    if (c === '"') { inQ = true; i++; continue; }
    if (c === delim) { row.push(field); field = ""; i++; continue; }
    if (c === "\r") { i++; continue; }
    if (c === "\n") { row.push(field); rows.push(row); row = []; field = ""; i++; continue; }
    field += c; i++;
  }
  if (field !== "" || row.length) { row.push(field); rows.push(row); }
  return rows.filter((r) => r.length > 1 || (r.length === 1 && r[0] !== ""));
}

async function renderTable(stage, path) {
  const res = await fetch(rawURL(path));
  if (!res.ok) throw new Error("could not load the file (" + res.status + ")");
  const text = await res.text();
  const delim = /\.tsv$/i.test(path) ? "\t" : ",";
  const rows = parseDelimited(text, delim);
  if (!rows.length) { stage.appendChild(el("p", "v-msg", "Empty file.")); return; }

  const wrap = el("div", "v-table-wrap");
  const table = el("table", "v-table");
  const thead = el("thead");
  const htr = el("tr");
  rows[0].forEach((h) => htr.appendChild(el("th", null, h)));
  thead.appendChild(htr);
  table.appendChild(thead);
  const tbody = el("tbody");
  for (let r = 1; r < rows.length; r++) {
    const tr = el("tr");
    for (let c = 0; c < rows[0].length; c++) tr.appendChild(el("td", null, rows[r][c] ?? ""));
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  wrap.appendChild(table);
  stage.appendChild(wrap);
  const note = el("p", "v-msg", (rows.length - 1) + " rows × " + rows[0].length + " columns");
  note.style.textAlign = "left";
  stage.appendChild(note);
}

async function renderNotebook(stage, path) {
  await Promise.all([
    loadScript(ASSETS + "/vendor/markdown-it.min.js"),
    loadScript(ASSETS + "/vendor/purify.min.js"),
    loadScript(ASSETS + "/vendor/highlight.min.js"),
    loadCSS(ASSETS + "/vendor/highlight-theme.min.css"),
  ]);
  const res = await fetch(rawURL(path));
  if (!res.ok) throw new Error("could not load the file (" + res.status + ")");
  const nb = await res.json();
  const md = window.markdownit({ html: false, linkify: true });
  const art = el("article", "paper notebook");
  const lang = (nb.metadata && nb.metadata.language_info && nb.metadata.language_info.name) || "python";
  const src = (cell) => Array.isArray(cell.source) ? cell.source.join("") : (cell.source || "");

  for (const cell of nb.cells || []) {
    if (cell.cell_type === "markdown") {
      const d = el("div", "nb-md");
      d.innerHTML = window.DOMPurify.sanitize(md.render(src(cell)));
      art.appendChild(d);
    } else if (cell.cell_type === "code") {
      const pre = el("pre", "nb-code");
      const code = el("code", "language-" + lang, src(cell));
      pre.appendChild(code);
      art.appendChild(pre);
      try { window.hljs.highlightElement(code); } catch {}
      for (const out of cell.outputs || []) renderNbOutput(art, out);
    }
  }
  stage.appendChild(art);
}

function renderNbOutput(art, out) {
  const data = out.data || {};
  if (data["image/png"]) {
    const img = el("img", "nb-out-img");
    img.src = "data:image/png;base64," + (Array.isArray(data["image/png"]) ? data["image/png"].join("") : data["image/png"]);
    art.appendChild(img);
    return;
  }
  let text = "";
  if (out.output_type === "stream") text = Array.isArray(out.text) ? out.text.join("") : out.text;
  else if (out.output_type === "error") text = (out.traceback || []).join("\n").replace(/\x1b\[[0-9;]*m/g, "");
  else if (data["text/plain"]) text = Array.isArray(data["text/plain"]) ? data["text/plain"].join("") : data["text/plain"];
  if (text) art.appendChild(el("pre", "nb-out", text));
}

function renderMedia(stage, path, kind) {
  const wrap = el("div", "v-media");
  let m;
  if (kind === "image") {
    m = document.createElement("img");
    m.alt = path;
  } else if (kind === "video") {
    m = document.createElement("video");
    m.controls = true;
  } else {
    m = document.createElement("audio");
    m.controls = true;
  }
  m.src = rawURL(path);
  wrap.appendChild(m);
  stage.appendChild(wrap);
}

function loadCSS(href) {
  if (document.querySelector(`link[href="${href}"]`)) return Promise.resolve();
  return new Promise((resolve) => {
    const l = document.createElement("link");
    l.rel = "stylesheet";
    l.href = href;
    l.onload = resolve;
    l.onerror = resolve;
    document.head.appendChild(l);
  });
}

// ---- views ----

let current = { kind: "", path: "" };
let folderView = null;
const scrollByFolder = new Map();
let leaving = [];

// onLeave registers work to stop when the visitor leaves the current view.
function onLeave(fn) {
  leaving.push(fn);
}

function leave() {
  if (current.kind === "folder") scrollByFolder.set(current.path, window.scrollY);
  if (folderView) { folderView.cleanup(); folderView = null; }
  const fns = leaving;
  leaving = [];
  for (const fn of fns) {
    try { fn(); } catch { /* already gone */ }
  }
}

function showFolder(path) {
  leave();
  current = { kind: "folder", path };
  const node = findFolder(tree, path);
  app.innerHTML = "";
  app.appendChild(folderChrome(path));
  const stage = el("main", "v-stage v-stage-files");
  app.appendChild(stage);
  document.title = path ? baseName(path) : "Files";
  folderView = renderFolderView(stage, node, {
    prefs, rawURL, isRoot: path === "",
    onPrefs: (p) => savePrefs(RAW_BASE, p),
  });
  const y = scrollByFolder.get(path) || 0;
  requestAnimationFrame(() => window.scrollTo(0, y));
}

async function showFile(path) {
  leave();
  current = { kind: "file", path };
  app.innerHTML = "";
  app.appendChild(fileChrome(path));
  const stage = el("main", "v-stage");
  app.appendChild(stage);
  window.scrollTo(0, 0);
  document.title = baseName(path);
  const loading = el("p", "v-msg", "Rendering " + baseName(path) + "…");
  stage.appendChild(loading);
  try {
    const kind = rendererFor(path);
    const target = el("div");
    stage.appendChild(target);
    if (kind === "pdf") await renderPDF(target, path);
    else if (kind === "markdown") await renderMarkdown(target, path);
    else if (kind === "docx") await renderDocx(target, path);
    else if (kind === "table") await renderTable(target, path);
    else if (kind === "notebook") await renderNotebook(target, path);
    else if (kind === "image" || kind === "video" || kind === "audio") renderMedia(target, path, kind);
    else if (kind === "download") renderDownload(target, path);
    else await renderText(target, path);
    loading.remove();
    scrollToAnchor();
  } catch (err) {
    if (!stage.isConnected) return; // the visitor moved on
    loading.className = "v-msg err";
    loading.textContent = "Could not display this file — " + err.message + ". Try the download button instead.";
  }
}

function showMissing() {
  leave();
  current = { kind: "missing", path: "" };
  app.innerHTML = "";
  app.appendChild(folderChrome(""));
  const stage = el("main", "v-stage");
  app.appendChild(stage);
  document.title = "Not found";
  const box = el("div", "v-msg");
  box.appendChild(el("p", null, "There is no file at this address."));
  const a = el("a", "v-dl v-missing-link", "Show all files");
  a.href = folderHash("");
  box.appendChild(a);
  stage.appendChild(box);
}

// scrollToAnchor follows a link that arrived with an anchor of the document
// itself (#install): the browser looked for it before the file was rendered.
function scrollToAnchor() {
  if (parseRoute(location.hash).type !== "other") return;
  let id;
  try { id = decodeURIComponent(location.hash.slice(1)); } catch { return; }
  const target = document.getElementById(id);
  if (target) target.scrollIntoView();
}

// route shows what the fragment names. No fragment: the start page — the
// chosen file, or the list. An anchor inside the shown document (a table of
// contents, #install) leaves the view alone; arriving with one opens the
// start page.
function route() {
  const r = parseRoute(location.hash);
  if (r.type === "other" && current.kind) return;
  if (r.type === "start" || r.type === "other") {
    if (cfg.entry) return showFile(cfg.entry);
    return showFolder("");
  }
  if (r.type === "folder") return findFolder(tree, r.path) ? showFolder(r.path) : showMissing();
  if (r.type === "file") {
    if (fileIndex.has(r.path) || (cfg.entry && r.path === cfg.entry)) return showFile(r.path);
    if (findFolder(tree, r.path)) return showFolder(r.path);
  }
  return showMissing();
}

// Keys: left/right step through a folder's files, Escape goes up to the
// folder, "/" finds a file. Never while typing or while a control or player
// has the focus, never as a held key's repeats, and the arrows not on a
// table, text or notebook, which scroll sideways with them.
const SCROLLS_SIDEWAYS = new Set(["table", "text", "notebook"]);
document.addEventListener("keydown", (e) => {
  if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || e.defaultPrevented) return;
  const t = e.target;
  if (t && (t.isContentEditable || /^(INPUT|SELECT|TEXTAREA|VIDEO|AUDIO|BUTTON)$/.test(t.tagName))) return;
  if (current.kind === "file") {
    // The address is ahead of the view: a second key press can come before
    // the hashchange of the first, and must step on from where that went.
    const r = parseRoute(location.hash);
    const here = r.type === "file" && fileIndex.has(r.path) ? r.path : current.path;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      if (e.repeat || SCROLLS_SIDEWAYS.has(rendererFor(here))) return;
      const n = neighbours(here);
      const to = n && (e.key === "ArrowLeft" ? n.prev : n.next);
      if (to) { e.preventDefault(); location.hash = fileHash(to.path); }
    } else if (e.key === "Escape") {
      e.preventDefault();
      location.hash = folderHash(nearestFolder(here));
    }
  } else if (current.kind === "folder" && e.key === "/" && folderView) {
    e.preventDefault();
    folderView.focusFilter();
  }
});

window.addEventListener("hashchange", route);
route();
