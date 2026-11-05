// Sitebin viewer: what the file list knows about files, without the DOM —
// the folder tree, the addresses in the URL fragment, sorting, file kinds and
// formatting. browser.js draws the list, viewer.js routes and renders files.
"use strict";

// Which renderer shows a file, by extension. Kept equal to internal/viewer's
// table by TestViewerJSMirrorsRenderers.
export const rendererByExt = {
  pdf: "pdf",
  md: "markdown", markdown: "markdown", mdown: "markdown",
  docx: "docx",
  csv: "table", tsv: "table",
  ipynb: "notebook",
  png: "image", jpg: "image", jpeg: "image", gif: "image", webp: "image",
  svg: "image", avif: "image", bmp: "image", ico: "image",
  mp4: "video", webm: "video", mov: "video", m4v: "video",
  mp3: "audio", wav: "audio", ogg: "audio", m4a: "audio", flac: "audio",
  // Files the browser cannot show: a download, never printed as text.
  exe: "download", msi: "download", dmg: "download", pkg: "download",
  app: "download", apk: "download", ipa: "download", deb: "download",
  rpm: "download", appimage: "download",
  zip: "download", "7z": "download", rar: "download", tar: "download",
  gz: "download", tgz: "download", bz2: "download", xz: "download",
  zst: "download", iso: "download", img: "download", bin: "download",
  dll: "download", so: "download", dylib: "download", jar: "download",
  wasm: "download", woff: "download", woff2: "download", ttf: "download",
  otf: "download", eot: "download", psd: "download", ai: "download",
  sketch: "download", fig: "download", blend: "download",
  xlsx: "download", xls: "download", pptx: "download", ppt: "download",
  doc: "download", odt: "download", ods: "download", odp: "download",
  epub: "download", heic: "download", tif: "download", tiff: "download",
  mkv: "download", avi: "download", wmv: "download", flv: "download",
  sqlite: "download", db: "download",
};

export function baseName(path) {
  return path.slice(path.lastIndexOf("/") + 1);
}

export function dirName(path) {
  const i = path.lastIndexOf("/");
  return i < 0 ? "" : path.slice(0, i);
}

// extOf is the lower-case extension without the dot, "" for none. A leading
// dot alone (".env") is a name, not an extension.
export function extOf(path) {
  const name = baseName(path);
  const i = name.lastIndexOf(".");
  return i > 0 ? name.slice(i + 1).toLowerCase() : "";
}

export function rendererFor(path) {
  return rendererByExt[extOf(path)] || "text";
}

// isHiddenPath is internal/viewer's rule (TestViewerJSHidesTheSameNames):
// dotfiles and dot-folders, and operating-system clutter, anywhere in the
// path. The server leaves them out of the page; this hides them in a page
// written before it did.
export function isHiddenPath(path) {
  return path.split("/").some((seg) => seg.startsWith(".") ||
    ["__macosx", "thumbs.db", "desktop.ini"].includes(seg.toLowerCase()));
}

const ARCHIVES = new Set(["zip", "7z", "rar", "tar", "gz", "tgz", "bz2", "xz", "zst", "jar"]);
const APPS = new Set(["exe", "msi", "dmg", "pkg", "app", "apk", "ipa", "deb", "rpm", "appimage", "iso", "img", "bin", "dll", "so", "dylib", "wasm"]);
const FONTS = new Set(["woff", "woff2", "ttf", "otf", "eot"]);
const OFFICE = new Set(["doc", "docx", "odt", "xls", "xlsx", "ods", "ppt", "pptx", "odp", "epub"]);
const CODE = new Set(["js", "mjs", "ts", "tsx", "jsx", "json", "html", "htm", "css", "go", "py", "rb", "rs", "java", "c", "h", "cpp", "cs", "php", "sh", "ps1", "yaml", "yml", "toml", "xml", "sql", "ipynb"]);

// iconFor names the icon a file gets in the list: image, video, audio, pdf,
// doc, table, code, archive, app, font or file.
export function iconFor(path) {
  const ext = extOf(path);
  const kind = rendererByExt[ext];
  if (kind === "image" || ext === "heic" || ext === "tif" || ext === "tiff" || ext === "psd") return "image";
  if (kind === "video" || ext === "mkv" || ext === "avi" || ext === "wmv" || ext === "flv") return "video";
  if (kind === "audio") return "audio";
  if (kind === "pdf") return "pdf";
  if (kind === "table" || ext === "xlsx" || ext === "xls" || ext === "ods") return "table";
  if (kind === "markdown" || OFFICE.has(ext) || ext === "txt") return "doc";
  if (CODE.has(ext)) return "code";
  if (ARCHIVES.has(ext)) return "archive";
  if (APPS.has(ext)) return "app";
  if (FONTS.has(ext)) return "font";
  return "file";
}

const TYPE_LABELS = {
  pdf: "PDF document", markdown: "Markdown", docx: "Word document",
  table: "Table", notebook: "Jupyter notebook", image: "Image",
  video: "Video", audio: "Audio",
};

// typeLabel is the Type column: a word for what the viewer renders, else the
// extension ("ZIP file"), else "File".
export function typeLabel(path) {
  const ext = extOf(path);
  const kind = rendererByExt[ext];
  if (kind && TYPE_LABELS[kind]) return TYPE_LABELS[kind];
  return ext ? ext.toUpperCase() + " file" : "File";
}

// ---- the tree ----

function newFolder(name, path) {
  return { name, path, folders: new Map(), files: [], size: 0, count: 0, newest: 0 };
}

// buildTree turns the flat file list into folders. Every folder knows its
// direct children and, summed over everything below it, its size, file count
// and newest modification time.
export function buildTree(files) {
  const root = newFolder("", "");
  for (const f of files) {
    if (!f || typeof f.path !== "string" || !f.path) continue;
    const segs = f.path.split("/");
    const name = segs.pop();
    let node = root;
    const chain = [root];
    for (const seg of segs) {
      let next = node.folders.get(seg);
      if (!next) {
        next = newFolder(seg, node.path ? node.path + "/" + seg : seg);
        node.folders.set(seg, next);
      }
      node = next;
      chain.push(node);
    }
    const mtime = Number.isFinite(f.mtime) ? f.mtime : 0;
    node.files.push({ name, path: f.path, size: f.size || 0, mtime });
    for (const n of chain) {
      n.size += f.size || 0;
      n.count += 1;
      if (mtime > n.newest) n.newest = mtime;
    }
  }
  return root;
}

export function findFolder(root, path) {
  if (!path) return root;
  let node = root;
  for (const seg of path.split("/")) {
    node = node.folders.get(seg);
    if (!node) return null;
  }
  return node;
}

// itemCount is what a folder holds directly: its folders and files.
export function itemCount(node) {
  return node.folders.size + node.files.length;
}

// ---- addresses ----

function encodePath(path) {
  return path.split("/").map(encodeURIComponent).join("/");
}

export function folderHash(path) {
  return "#/" + (path ? encodePath(path) + "/" : "");
}

export function fileHash(path) {
  return "#/" + encodePath(path);
}

// parseRoute reads the fragment: "" is the start page, "#/" the top folder,
// "#/a/b/" a folder, "#/a/b.pdf" a file (or a folder written without its
// slash — the caller decides). A fragment that does not start with "#/" is
// "other": an anchor inside the shown document (a table of contents,
// #install) or an old #page=3 link, never an address of the list. A "#/"
// address with bad percent-encoding or a "." / ".." segment is "missing".
export function parseRoute(hash) {
  if (!hash || hash === "#") return { type: "start" };
  if (!hash.startsWith("#/")) return { type: "other" };
  let rest = hash.slice(2);
  const folder = rest === "" || rest.endsWith("/");
  if (folder) rest = rest.replace(/\/+$/, "");
  let path;
  try {
    path = rest.split("/").map(decodeURIComponent).join("/");
  } catch {
    return { type: "missing" };
  }
  if (path.split("/").some((s) => s === "." || s === "..") || (rest && path.split("/").some((s) => s === ""))) {
    return { type: "missing" };
  }
  return { type: folder ? "folder" : "file", path };
}

// resolveRelative resolves a link written in a document at dir ("a/b") to a
// site path, or null when it leaves the site.
export function resolveRelative(dir, href) {
  const out = dir ? dir.split("/") : [];
  for (const seg of href.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") {
      if (!out.length) return null;
      out.pop();
    } else {
      out.push(seg);
    }
  }
  return out.join("/");
}

// ---- sorting ----

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

export const SORT_KEYS = ["name", "mtime", "size", "type"];

// sortEntries returns the folders and files of a listing in the visitor's
// order. Folders always come first; a file without a date sorts last by
// date whatever the direction.
export function sortEntries(folders, files, key, dir) {
  const sign = dir === "desc" ? -1 : 1;
  const byName = (a, b) => collator.compare(a.name, b.name);
  const fKey = {
    name: (a, b) => byName(a, b),
    size: (a, b) => (a.size - b.size) || byName(a, b),
    mtime: (a, b) => (a.newest - b.newest) || byName(a, b),
    type: (a, b) => byName(a, b),
  }[key] || byName;
  const sortedFolders = [...folders].sort((a, b) => sign * fKey(a, b));
  const sortedFiles = [...files].sort((a, b) => {
    if (key === "mtime") {
      if (!a.mtime !== !b.mtime) return a.mtime ? -1 : 1;
      return sign * ((a.mtime - b.mtime) || byName(a, b));
    }
    if (key === "size") return sign * ((a.size - b.size) || byName(a, b));
    if (key === "type") return sign * (collator.compare(typeLabel(a.path), typeLabel(b.path)) || byName(a, b));
    return sign * byName(a, b);
  });
  return { folders: sortedFolders, files: sortedFiles };
}

// filterTree lists everything below node whose name contains query
// (case-insensitive): folders and files, flat.
export function filterTree(node, query) {
  const q = query.trim().toLocaleLowerCase();
  const folders = [], files = [];
  const walk = (n) => {
    for (const f of n.folders.values()) {
      if (f.name.toLocaleLowerCase().includes(q)) folders.push(f);
      walk(f);
    }
    for (const f of n.files) {
      if (f.name.toLocaleLowerCase().includes(q)) files.push(f);
    }
  };
  if (q) walk(node);
  return { folders, files };
}

// defaultView is the view a folder opens in until the visitor picks one:
// the grid when most of its files are pictures or videos.
export function defaultView(node) {
  if (!node.files.length) return "details";
  const visual = node.files.filter((f) => {
    const k = iconFor(f.path);
    return k === "image" || k === "video";
  }).length;
  return visual / node.files.length >= 0.6 ? "grid" : "details";
}

// ---- formatting ----

export function fmtBytes(n) {
  if (n == null) return "";
  if (n < 1024) return n + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let u = -1;
  do { n /= 1024; u++; } while (n >= 1024 && u < units.length - 1);
  return n.toFixed(n >= 10 ? 0 : 1) + " " + units[u];
}

const dateFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export function fmtDate(mtime) {
  if (!mtime) return "—";
  return dateFmt.format(new Date(mtime * 1000));
}

export function plural(n, one, many) {
  return n + " " + (n === 1 ? one : many);
}
