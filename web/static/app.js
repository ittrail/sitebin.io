// Sitebin landing / create flow.
"use strict";

const $ = (id) => document.getElementById(id);

const state = {
  files: [],        // {file: File, path: string}
  zip: null,        // File | null (exclusive with files)
  domains: [],
  userTouchedMode: false,
  uploading: false,
};

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

function showError(msg) {
  const bar = $("errbar");
  bar.textContent = msg;
  bar.classList.add("show");
}
function clearError() { $("errbar").classList.remove("show"); }

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  }
}

const DOC_EXTS = /\.(pdf|md|markdown|docx|txt|png|jpe?g|gif|webp|svg|mp4|webm|mp3|wav|json|log|csv)$/i;

// ---- file staging ----

function addFiles(list) {
  clearError();
  const incoming = Array.from(list);
  if (incoming.length === 1 && /\.zip$/i.test(incoming[0].name) && state.files.length === 0) {
    state.zip = incoming[0];
    state.files = [];
  } else {
    if (state.zip) state.zip = null;
    for (const f of incoming) {
      const path = (f.webkitRelativePath || f.name).replace(/^\/+/, "");
      if (!path) continue;
      const existing = state.files.findIndex((x) => x.path === path);
      if (existing >= 0) state.files.splice(existing, 1);
      state.files.push({ file: f, path });
    }
  }
  render();
}

function suggestMode() {
  if (state.userTouchedMode) return;
  let viewer = false;
  if (!state.zip && state.files.length >= 1) {
    const hasIndex = state.files.some((f) => /(^|\/)index\.html?$/i.test(f.path));
    const allDocs = state.files.every((f) => DOC_EXTS.test(f.path) && !/\.html?$/i.test(f.path));
    viewer = !hasIndex && allDocs;
  }
  const target = viewer ? "viewer" : "webserver";
  document.querySelector(`input[name=mode][value=${target}]`).checked = true;
}

function render() {
  const list = $("filelist");
  list.innerHTML = "";
  const mkChip = (label, size, onRemove) => {
    const chip = document.createElement("span");
    chip.className = "chip";
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = label;
    chip.appendChild(name);
    if (size != null) {
      const sz = document.createElement("span");
      sz.textContent = fmtBytes(size);
      chip.appendChild(sz);
    }
    const x = document.createElement("button");
    x.className = "x";
    x.type = "button";
    x.setAttribute("aria-label", "Remove " + label);
    x.textContent = "×";
    x.addEventListener("click", onRemove);
    chip.appendChild(x);
    return chip;
  };

  if (state.zip) {
    list.appendChild(mkChip(state.zip.name, state.zip.size, () => { state.zip = null; render(); }));
  } else {
    const max = 24;
    state.files.slice(0, max).forEach((f, i) =>
      list.appendChild(mkChip(f.path, f.file.size, () => { state.files.splice(i, 1); render(); })));
    if (state.files.length > max) {
      const more = document.createElement("span");
      more.className = "chip";
      more.textContent = "+" + (state.files.length - max) + " more";
      list.appendChild(more);
    }
  }

  const total = state.zip ? state.zip.size : state.files.reduce((s, f) => s + f.file.size, 0);
  const count = state.zip ? 1 : state.files.length;
  const totalEl = $("total");
  totalEl.classList.toggle("hidden", count === 0);
  totalEl.textContent = count + (count === 1 ? " file · " : " files · ") + fmtBytes(total);

  $("opt-unzip").classList.toggle("hidden", !state.zip);
  $("slab-title").textContent = count === 0 ? "Drop it in the bin" : "Ready to publish";
  $("publish").disabled = state.uploading;
  $("publish-hint").classList.toggle("hidden", count > 0);
  suggestMode();
}

// ---- drag & drop (with folder traversal) ----

const slab = $("slab");
["dragenter", "dragover"].forEach((ev) =>
  slab.addEventListener(ev, (e) => { e.preventDefault(); slab.classList.add("dragover"); }));
["dragleave", "drop"].forEach((ev) =>
  slab.addEventListener(ev, (e) => { e.preventDefault(); slab.classList.remove("dragover"); }));

slab.addEventListener("drop", async (e) => {
  const items = e.dataTransfer.items;
  if (items && items.length && items[0].webkitGetAsEntry) {
    const files = [];
    const walk = (entry, prefix) => new Promise((resolve) => {
      if (entry.isFile) {
        entry.file((f) => { files.push({ file: f, path: prefix + entry.name }); resolve(); }, resolve);
      } else if (entry.isDirectory) {
        const reader = entry.createReader();
        const readAll = () => reader.readEntries(async (entries) => {
          if (!entries.length) return resolve();
          for (const en of entries) await walk(en, prefix + entry.name + "/");
          readAll();
        }, resolve);
        readAll();
      } else resolve();
    });
    const entries = Array.from(items).map((i) => i.webkitGetAsEntry()).filter(Boolean);
    for (const en of entries) await walk(en, "");
    if (files.length === 1 && /\.zip$/i.test(files[0].path) && state.files.length === 0) {
      state.zip = files[0].file;
    } else {
      state.zip = null;
      for (const f of files) {
        const existing = state.files.findIndex((x) => x.path === f.path);
        if (existing >= 0) state.files.splice(existing, 1);
        state.files.push(f);
      }
    }
    render();
  } else if (e.dataTransfer.files.length) {
    addFiles(e.dataTransfer.files);
  }
});

$("pick-files").addEventListener("click", () => $("input-files").click());
$("pick-folder").addEventListener("click", () => $("input-folder").click());
$("pick-zip").addEventListener("click", () => $("input-zip").click());
$("input-files").addEventListener("change", (e) => { addFiles(e.target.files); e.target.value = ""; });
$("input-folder").addEventListener("change", (e) => { addFiles(e.target.files); e.target.value = ""; });
$("input-zip").addEventListener("change", (e) => { addFiles(e.target.files); e.target.value = ""; });

document.querySelectorAll("input[name=mode]").forEach((r) =>
  r.addEventListener("change", () => { state.userTouchedMode = true; }));

// ---- domains ----

function renderDomains() {
  const list = $("domainlist");
  list.innerHTML = "";
  state.domains.forEach((d, i) => {
    const chip = document.createElement("span");
    chip.className = "chip";
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = d;
    const x = document.createElement("button");
    x.className = "x";
    x.type = "button";
    x.textContent = "×";
    x.setAttribute("aria-label", "Remove " + d);
    x.addEventListener("click", () => { state.domains.splice(i, 1); renderDomains(); });
    chip.append(name, x);
    list.appendChild(chip);
  });
}

function addDomain() {
  const input = $("domain");
  const d = input.value.trim().toLowerCase();
  if (!d) return;
  if (!/^[a-z0-9][a-z0-9.-]+\.[a-z0-9-]+$/.test(d)) {
    toast("That doesn't look like a domain name", true);
    return;
  }
  if (!state.domains.includes(d)) state.domains.push(d);
  input.value = "";
  renderDomains();
}
$("add-domain").addEventListener("click", addDomain);
$("domain").addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); addDomain(); } });

// ---- publish ----

$("publish").addEventListener("click", () => {
  if (state.uploading) return;
  clearError();
  state.uploading = true;
  slab.classList.add("uploading");
  $("publish").disabled = true;

  const fd = new FormData();
  fd.append("mode", document.querySelector("input[name=mode]:checked").value);
  const vp = $("view-password").value;
  if (vp) fd.append("view_password", vp);
  const exp = $("expires").value;
  if (exp) fd.append("expires_at", new Date(exp).toISOString());
  if ($("webdav").checked) fd.append("webdav", "true");
  state.domains.forEach((d) => fd.append("domain", d));

  if (state.zip) {
    if ($("unzip").checked) fd.append("zip", state.zip, state.zip.name);
    else fd.append("files", state.zip, state.zip.name);
  } else {
    state.files.forEach((f) => fd.append("files", f.file, f.path));
  }

  const xhr = new XMLHttpRequest();
  xhr.open("POST", "/api/sites");
  xhr.upload.addEventListener("progress", (e) => {
    if (e.lengthComputable) $("progressbar").style.width = (e.loaded / e.total) * 100 + "%";
  });
  xhr.addEventListener("load", () => {
    state.uploading = false;
    slab.classList.remove("uploading");
    $("progressbar").style.width = "0%";
    let body = {};
    try { body = JSON.parse(xhr.responseText); } catch {}
    if (xhr.status === 201) {
      showTicket(body);
    } else {
      $("publish").disabled = false;
      showError(body.error || "Publishing failed (" + xhr.status + "). Please try again.");
    }
  });
  xhr.addEventListener("error", () => {
    state.uploading = false;
    slab.classList.remove("uploading");
    $("publish").disabled = false;
    showError("Network error — the upload didn't go through.");
  });
  xhr.send(fd);
});

// ---- ticket ----

// renderQR draws a QR of the view URL into #t-qr (dependency-free lib).
function renderQR(url) {
  const box = $("t-qr");
  if (typeof qrcode !== "function") { box.parentElement.classList.add("hidden"); return; }
  try {
    const qr = qrcode(0, "M");
    qr.addData(url);
    qr.make();
    box.innerHTML = qr.createSvgTag({ cellSize: 4, margin: 1, scalable: true });
  } catch {
    box.parentElement.classList.add("hidden");
  }
}

function showTicket(body) {
  $("create").classList.add("hidden");
  const done = $("done");
  done.classList.remove("hidden");

  $("t-view").textContent = body.view_url;
  $("t-view").href = body.view_url;
  renderQR(body.view_url);
  $("t-edit").textContent = body.edit_url;
  $("t-edit").href = body.edit_url;
  $("t-pass").textContent = body.edit_password;
  $("t-open").href = body.view_url;
  $("t-editlink").href = body.edit_url;

  if (body.warnings) body.warnings.forEach((w) => toast(w, true));

  sessionStorage.setItem("sb-ticket", JSON.stringify({
    view: body.view_url, edit: body.edit_url,
  }));
  done.scrollIntoView({ behavior: "smooth", block: "start" });
}

document.querySelectorAll(".copybtn").forEach((btn) =>
  btn.addEventListener("click", async () => {
    const ok = await copyText($(btn.dataset.copy).textContent);
    if (ok) {
      btn.classList.add("done");
      const old = btn.textContent;
      btn.textContent = "Copied";
      setTimeout(() => { btn.classList.remove("done"); btn.textContent = old; }, 1400);
    }
  }));

$("t-copyall").addEventListener("click", async () => {
  const text = [
    "View URL:      " + $("t-view").textContent,
    "Edit URL:      " + $("t-edit").textContent,
    "Edit password: " + $("t-pass").textContent,
  ].join("\n");
  if (await copyText(text)) toast("Ticket copied to clipboard");
});

$("t-again").addEventListener("click", () => {
  state.files = [];
  state.zip = null;
  state.domains = [];
  renderDomains();
  render();
  $("publish").disabled = false;
  $("done").classList.add("hidden");
  $("create").classList.remove("hidden");
  window.scrollTo({ top: 0, behavior: "smooth" });
});

// ---- init ----

$("api-hint").textContent =
  'curl -F "files=@index.html" ' + location.origin + "/api/sites";
render();
