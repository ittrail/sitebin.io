// Sitebin viewer runtime. Loaded by the generated wrapper page on a site's
// own origin; reads its config from the inline JSON and renders the entry
// file fetched from /_raw/.
"use strict";

const ASSETS = "/_sitebin/assets";
const cfg = JSON.parse(document.getElementById("sitebin-config").textContent);
const app = document.getElementById("viewer-app");

const rendererByExt = {
  pdf: "pdf",
  md: "markdown", markdown: "markdown", mdown: "markdown",
  docx: "docx",
  csv: "table", tsv: "table",
  ipynb: "notebook",
  png: "image", jpg: "image", jpeg: "image", gif: "image", webp: "image",
  svg: "image", avif: "image", bmp: "image", ico: "image",
  mp4: "video", webm: "video", mov: "video", m4v: "video",
  mp3: "audio", wav: "audio", ogg: "audio", m4a: "audio", flac: "audio",
};

function rendererFor(name) {
  const ext = (name.split(".").pop() || "").toLowerCase();
  return rendererByExt[ext] || "text";
}

// RAW_BASE is the wrapper's own directory, so raw-file URLs work whether the
// site is served at a subdomain root ("/") or a /v/<id>/ path.
const RAW_BASE = location.pathname.replace(/[^/]*$/, "");

function rawURL(path) {
  return RAW_BASE + "_raw/" + path.split("/").map(encodeURIComponent).join("/");
}

function fmtBytes(n) {
  if (n == null) return "";
  if (n < 1024) return n + " B";
  const units = ["KB", "MB", "GB"];
  let u = -1;
  do { n /= 1024; u++; } while (n >= 1024 && u < units.length - 1);
  return n.toFixed(n >= 10 ? 0 : 1) + " " + units[u];
}

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

function buildChrome(current) {
  const bar = el("header", "v-chrome");
  const mark = el("a", "v-mark");
  mark.href = "/";
  mark.title = "Site home";
  mark.innerHTML =
    '<svg viewBox="0 0 64 64" aria-hidden="true"><rect x="4" y="4" width="56" height="56" rx="14" fill="#101725" stroke="#2A3550" stroke-width="2"/><rect x="16" y="16" width="32" height="6" rx="3" fill="#F5B84D"/><path d="M32 30v14M25 38l7 7 7-7" stroke="#5B8CFF" stroke-width="4.5" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  bar.appendChild(mark);

  const info = cfg.files.find((f) => f.path === current);
  bar.appendChild(el("span", "v-name", current || "no file"));
  if (info) bar.appendChild(el("span", "v-size", fmtBytes(info.size)));
  bar.appendChild(el("span", "v-spacer"));

  if (cfg.files.length > 1) {
    const sel = document.createElement("select");
    sel.setAttribute("aria-label", "Choose file");
    for (const f of cfg.files) {
      const opt = document.createElement("option");
      opt.value = f.path;
      opt.textContent = f.path;
      if (f.path === current) opt.selected = true;
      sel.appendChild(opt);
    }
    sel.addEventListener("change", () => show(sel.value));
    bar.appendChild(sel);
  }

  if (current) {
    const dl = el("a", "v-dl", "Download");
    dl.href = rawURL(current);
    dl.setAttribute("download", current.split("/").pop());
    bar.appendChild(dl);
  }
  return bar;
}

// ---- renderers ----

async function renderPDF(stage, path) {
  const pdfjs = await import(ASSETS + "/vendor/pdf.min.mjs");
  pdfjs.GlobalWorkerOptions.workerSrc = ASSETS + "/vendor/pdf.worker.min.mjs";
  const doc = await pdfjs.getDocument(rawURL(path)).promise;
  const wrap = el("div", "v-pdf");
  stage.appendChild(wrap);
  const maxWidth = Math.min(stage.clientWidth - 8, 900);
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  for (let i = 1; i <= doc.numPages; i++) {
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
  stage.appendChild(article);
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
  const res = await fetch(rawURL(path));
  if (!res.ok) throw new Error("could not load the file (" + res.status + ")");
  let text = await res.text();
  const CAP = 2 * 1024 * 1024;
  let truncated = false;
  if (text.length > CAP) { text = text.slice(0, CAP); truncated = true; }
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

// ---- boot ----

async function show(path) {
  app.innerHTML = "";
  app.appendChild(buildChrome(path));
  const stage = el("main", "v-stage");
  app.appendChild(stage);

  if (!path) {
    stage.appendChild(el("p", "v-msg", "This site has no files yet. Add one via its edit page."));
    return;
  }
  document.title = path.split("/").pop();
  const loading = el("p", "v-msg", "Rendering " + path.split("/").pop() + "…");
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
    else await renderText(target, path);
    loading.remove();
  } catch (err) {
    loading.className = "v-msg err";
    loading.textContent = "Could not display this file — " + err.message + ". Try the download button instead.";
  }
}

show(cfg.entry || (cfg.files[0] && cfg.files[0].path) || "");
