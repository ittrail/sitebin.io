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
  png: "image", jpg: "image", jpeg: "image", gif: "image", webp: "image",
  svg: "image", avif: "image", bmp: "image", ico: "image",
  mp4: "video", webm: "video", mov: "video", m4v: "video",
  mp3: "audio", wav: "audio", ogg: "audio", m4a: "audio", flac: "audio",
};

function rendererFor(name) {
  const ext = (name.split(".").pop() || "").toLowerCase();
  return rendererByExt[ext] || "text";
}

function rawURL(path) {
  return "/_raw/" + path.split("/").map(encodeURIComponent).join("/");
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
    else if (kind === "image" || kind === "video" || kind === "audio") renderMedia(target, path, kind);
    else await renderText(target, path);
    loading.remove();
  } catch (err) {
    loading.className = "v-msg err";
    loading.textContent = "Could not display this file — " + err.message + ". Try the download button instead.";
  }
}

show(cfg.entry || (cfg.files[0] && cfg.files[0].path) || "");
