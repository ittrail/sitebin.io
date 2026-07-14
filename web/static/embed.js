// <sitebin-drop> — Sitebin's embeddable "drop files, get a website" component.
//
//   <script src="https://your-instance/_sitebin/embed.js" defer></script>
//   <sitebin-drop instance="https://your-instance"></sitebin-drop>
//
// Attributes:
//   instance    API base URL. Default: the origin this script was loaded
//               from, else the page origin. Requests go to <instance>/api/sites.
//   demo        No network: publishing simulates a response and shows a
//               clearly-marked demo ticket.
//   no-domains  Hide the custom-domains option (community instances).
//   event-only  Don't render the built-in ticket; the host page listens for
//               the event and renders its own.
// Events (bubbling, composed):
//   sitebin-published  detail = the create API response body
//   sitebin-error      detail = {status, error}
// Methods:
//   reset()     Clear staged files/ticket and show the create area again.
//
// Cross-origin note: the instance must allowlist the embedding page's origin
// via SITEBIN_EMBED_ORIGINS (enterprise edition) or the browser will block
// reading the create response. Same-origin use works everywhere.
(function () {
  "use strict";
  if (customElements.get("sitebin-drop")) return;

  const SCRIPT_ORIGIN = (() => {
    try {
      const s = document.currentScript;
      if (s && s.src) return new URL(s.src).origin;
    } catch {}
    return "";
  })();

  const DOC_EXTS = /\.(pdf|md|markdown|docx|txt|png|jpe?g|gif|webp|svg|mp4|webm|mp3|wav|json|log|csv)$/i;

  const STYLE = `
:host {
  --bg: #0c111d; --bg-raise: #121a2b; --bg-card: #141c2e;
  --line: #232e47; --line-soft: #1a2338;
  --ink: #e6eaf3; --ink-dim: #97a3b8; --ink-faint: #5f6c85;
  --amber: #f5b84d; --amber-deep: #d99a26; --blue: #5b8cff;
  --danger: #f26d6d; --ok: #5ecf8a;
  --display: "Space Grotesk", ui-sans-serif, system-ui, sans-serif;
  --body: ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif;
  --mono: ui-monospace, "Cascadia Code", "SF Mono", Menlo, Consolas, monospace;
  --radius: 14px;
  display: block; color: var(--ink); font: 16px/1.6 var(--body);
  text-align: left;
}
*, *::before, *::after { box-sizing: border-box; margin: 0; }
button { font: inherit; }
a { color: var(--blue); text-decoration: none; }
a:hover { text-decoration: underline; }
:focus-visible { outline: 2px solid var(--blue); outline-offset: 2px; border-radius: 4px; }
.hidden { display: none !important; }

.slab {
  position: relative;
  border: 1.5px dashed #34405f;
  border-radius: var(--radius);
  background:
    radial-gradient(circle at 1px 1px, rgba(230, 234, 243, .07) 1px, transparent 1.6px) 0 0/18px 18px,
    linear-gradient(180deg, rgba(255,255,255,.02), transparent 40%),
    var(--bg-raise);
  padding: clamp(28px, 6vw, 56px) clamp(20px, 5vw, 48px);
  text-align: center;
  transition: border-color .18s, transform .18s, box-shadow .18s;
}
.slab.dragover {
  border-color: var(--amber);
  transform: scale(1.008);
  box-shadow: 0 0 0 4px rgba(245, 184, 77, .12), 0 18px 60px rgba(0,0,0,.4);
}
.slab .tray { width: 54px; height: 54px; margin: 0 auto 14px; color: var(--ink-faint); }
.slab.dragover .tray { color: var(--amber); }
.slab h2 { font: 600 22px var(--display); letter-spacing: -.01em; }
.slab .hint { color: var(--ink-dim); margin-top: 6px; font-size: 15px; }
.slab .hint button {
  background: none; border: 0; padding: 0; color: var(--blue);
  font: inherit; cursor: pointer;
}
.slab .hint button:hover { text-decoration: underline; }
.slab .fineprint { margin-top: 16px; font: 12px var(--mono); color: var(--ink-faint); }

.filelist { margin-top: 20px; display: flex; flex-wrap: wrap; gap: 8px; justify-content: center; }
.chip {
  display: inline-flex; align-items: center; gap: 8px;
  background: var(--bg-card); border: 1px solid var(--line);
  border-radius: 9px; padding: 6px 8px 6px 12px;
  font: 13px var(--mono); color: var(--ink-dim); max-width: 100%;
}
.chip .name { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 34ch; }
.chip .x {
  border: 0; background: none; color: var(--ink-faint); cursor: pointer;
  font-size: 15px; line-height: 1; padding: 2px 4px; border-radius: 5px;
}
.chip .x:hover { color: var(--danger); background: rgba(242,109,109,.1); }
.slab .total { margin-top: 12px; font: 12px var(--mono); color: var(--ink-faint); }

.progress { height: 6px; border-radius: 99px; background: var(--line-soft); overflow: hidden; margin-top: 20px; display: none; }
.progress .bar { height: 100%; width: 0%; background: linear-gradient(90deg, var(--amber-deep), var(--amber)); transition: width .15s; }
.slab.uploading .progress { display: block; }

.errbar {
  display: none; margin-top: 16px; padding: 10px 14px; border-radius: 10px;
  background: rgba(242,109,109,.09); border: 1px solid rgba(242,109,109,.35);
  color: #ffb4b4; font-size: 14px; text-align: left;
}
.errbar.show { display: block; }

.options {
  margin-top: 18px;
  border: 1px solid var(--line-soft); border-radius: var(--radius);
  background: var(--bg-card);
}
.options summary {
  cursor: pointer; list-style: none; user-select: none;
  padding: 14px 20px; display: flex; align-items: center; gap: 10px;
  font: 600 14px var(--display); letter-spacing: .02em; color: var(--ink-dim);
}
.options summary::-webkit-details-marker { display: none; }
.options summary .chev { transition: transform .15s; color: var(--ink-faint); }
.options[open] summary .chev { transform: rotate(90deg); }
.options .body { padding: 4px 20px 20px; display: grid; gap: 18px; }

.opt { display: grid; gap: 7px; }
.opt > label, .opt > .optlabel { font-size: 13px; color: var(--ink-dim); font-weight: 500; }
.opt .sub { font-size: 12.5px; color: var(--ink-faint); }
.opt .faint { color: var(--ink-faint); }

.seg { display: inline-flex; background: var(--bg-raise); border: 1px solid var(--line); border-radius: 10px; padding: 3px; gap: 3px; justify-self: start; width: fit-content; }
.seg label { position: relative; }
.seg input { position: absolute; opacity: 0; inset: 0; cursor: pointer; }
.seg span {
  display: inline-block; padding: 7px 16px; border-radius: 8px;
  font: 500 14px var(--body); color: var(--ink-dim); cursor: pointer;
}
.seg input:checked + span { background: #202b45; color: var(--ink); }
.seg input:focus-visible + span { outline: 2px solid var(--blue); outline-offset: 1px; }

input[type=text], input[type=password], input[type=datetime-local] {
  width: 100%; max-width: 380px;
  background: var(--bg-raise); color: var(--ink);
  border: 1px solid var(--line); border-radius: 10px;
  padding: 10px 12px; font: 15px var(--body);
}
input:focus { outline: none; border-color: var(--blue); box-shadow: 0 0 0 3px rgba(91,140,255,.18); }
input::placeholder { color: var(--ink-faint); }

.switch { display: inline-flex; align-items: center; gap: 10px; cursor: pointer; position: relative; }
.switch input { position: absolute; opacity: 0; }
.switch .knob {
  width: 40px; height: 23px; border-radius: 99px; background: var(--line);
  position: relative; transition: background .15s; flex: none;
}
.switch .knob::after {
  content: ""; position: absolute; top: 3px; left: 3px;
  width: 17px; height: 17px; border-radius: 50%; background: var(--ink-dim);
  transition: transform .15s, background .15s;
}
.switch input:checked + .knob { background: var(--amber-deep); }
.switch input:checked + .knob::after { transform: translateX(17px); background: #fff; }
.switch input:focus-visible + .knob { outline: 2px solid var(--blue); outline-offset: 2px; }
.switch .lbl { font-size: 14px; color: var(--ink); }

.row { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }

.btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 8px;
  border: 1px solid var(--line); border-radius: 10px; cursor: pointer;
  background: var(--bg-raise); color: var(--ink);
  padding: 10px 16px; font: 500 14.5px var(--body);
  transition: filter .12s, background .12s;
}
.btn:hover { background: #1b2540; text-decoration: none; }
.btn.primary {
  background: linear-gradient(135deg, var(--amber), var(--amber-deep));
  border-color: transparent; color: #1c1503; font-weight: 650;
}
.btn.primary:hover { filter: brightness(1.06); }
.btn.big { padding: 14px 26px; font-size: 16px; border-radius: 12px; }
.btn:disabled { opacity: .5; cursor: not-allowed; }
.btn.small { padding: 6px 12px; font-size: 13px; border-radius: 8px; }

.publishrow { margin-top: 22px; display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.publishrow .sub { color: var(--ink-faint); font-size: 13.5px; }

.ticket {
  position: relative;
  background: var(--bg-card);
  border: 1px solid #3a3117;
  outline: 1px solid rgba(245,184,77,.35);
  border-radius: var(--radius);
  padding: clamp(24px, 5vw, 40px);
  box-shadow: 0 24px 70px rgba(0,0,0,.45), 0 0 42px rgba(245,184,77,.06);
}
.ticket .stamp {
  display: inline-flex; align-items: center; gap: 8px;
  font: 600 12px var(--mono); letter-spacing: .14em; color: var(--amber);
  border: 1.5px dashed rgba(245,184,77,.5); border-radius: 8px;
  padding: 7px 12px; text-transform: uppercase; margin-bottom: 18px;
}
.ticket h2 { font: 650 26px var(--display); letter-spacing: -.02em; margin-bottom: 4px; }
.ticket .sub { color: var(--ink-dim); font-size: 15px; }
.secretrows { margin-top: 24px; display: grid; gap: 12px; }
.secretrow {
  display: grid; grid-template-columns: 112px 1fr auto; gap: 12px; align-items: center;
  background: var(--bg-raise); border: 1px solid var(--line-soft);
  border-radius: 11px; padding: 12px 14px;
}
.secretrow .k { font: 600 12px var(--mono); letter-spacing: .1em; color: var(--ink-faint); text-transform: uppercase; }
.secretrow .v {
  font: 14.5px var(--mono); color: var(--ink);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.secretrow .v a { color: var(--ink); }
.secretrow .v a:hover { color: var(--blue); }
.secretrow.pass .v { color: var(--amber); }
.copybtn {
  border: 1px solid var(--line); background: transparent; color: var(--ink-dim);
  border-radius: 8px; padding: 7px 12px; font: 500 13px var(--body); cursor: pointer;
}
.copybtn:hover { color: var(--ink); background: var(--bg-card); }
.copybtn.done { color: var(--ok); border-color: rgba(94,207,138,.4); }
.keepnote {
  margin-top: 18px; display: flex; gap: 10px; align-items: flex-start;
  font-size: 14px; color: #ffd9a0;
  background: rgba(245,184,77,.07); border: 1px solid rgba(245,184,77,.28);
  padding: 12px 14px; border-radius: 10px;
}
.keepnote svg { flex: none; width: 18px; height: 18px; margin-top: 2px; }
.ticketactions { margin-top: 22px; display: flex; gap: 10px; flex-wrap: wrap; }
@media (max-width: 560px) {
  .secretrow { grid-template-columns: 1fr; gap: 6px; }
  .secretrow .copybtn { justify-self: start; }
}
@keyframes rise { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: none; } }
.ticket { animation: rise .35s ease-out both; }
@media (prefers-reduced-motion: reduce) {
  * { animation-duration: .01ms !important; transition-duration: .01ms !important; }
}
`;

  const MARKUP = `
<section class="create" part="create">
  <div class="slab" id="slab">
    <svg class="tray" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
      <path d="M12 3v10m0 0l-3.5-3.5M12 13l3.5-3.5" stroke-linecap="round" stroke-linejoin="round"/>
      <path d="M3 13v4a3 3 0 003 3h12a3 3 0 003-3v-4" stroke-linecap="round"/>
      <path d="M3 14h4l2 2h6l2-2h4" stroke-linejoin="round"/>
    </svg>
    <h2 id="slab-title">Drop it in the bin</h2>
    <p class="hint">Drag files or a folder here — or pick
      <button type="button" id="pick-files">files</button>,
      a <button type="button" id="pick-folder">folder</button>,
      or a <button type="button" id="pick-zip">.zip</button>
    </p>
    <div class="filelist" id="filelist"></div>
    <div class="total hidden" id="total"></div>
    <div class="progress"><div class="bar" id="progressbar"></div></div>
    <div class="errbar" id="errbar" role="alert"></div>
    <p class="fineprint" id="fineprint"></p>
  </div>

  <input type="file" id="input-files" multiple class="hidden">
  <input type="file" id="input-folder" webkitdirectory class="hidden">
  <input type="file" id="input-zip" accept=".zip,application/zip" class="hidden">

  <details class="options" id="options">
    <summary>
      <svg class="chev" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true"><path d="M9 6l6 6-6 6" stroke-linecap="round" stroke-linejoin="round"/></svg>
      <span id="options-label">Options — mode, password, expiry, WebDAV, domains</span>
    </summary>
    <div class="body">
      <div class="opt">
        <span class="optlabel" id="mode-label">How should it behave?</span>
        <div class="seg" role="radiogroup" aria-labelledby="mode-label">
          <label><input type="radio" name="mode" value="webserver" checked><span>Web server</span></label>
          <label><input type="radio" name="mode" value="viewer"><span>File viewer</span></label>
        </div>
        <span class="sub">Web server serves your files as-is (index.html is the front door).
          File viewer renders documents — PDF, Markdown, DOCX, images, code — right in the browser.</span>
      </div>

      <div class="opt hidden" id="opt-unzip">
        <label class="switch">
          <input type="checkbox" id="unzip" checked>
          <span class="knob"></span>
          <span class="lbl">Unpack the .zip into individual files</span>
        </label>
        <span class="sub">Off means the archive itself is published as a single file.</span>
      </div>

      <div class="opt">
        <label for="view-password">View password <span class="faint">(optional)</span></label>
        <input type="password" id="view-password" autocomplete="new-password" placeholder="Leave empty for a public site">
        <span class="sub">Visitors must enter this before anything is served.</span>
      </div>

      <div class="opt">
        <label for="expires">Expires <span class="faint">(optional)</span></label>
        <input type="datetime-local" id="expires">
        <span class="sub">After this moment the site stops serving and is cleaned up.</span>
      </div>

      <div class="opt">
        <label class="switch">
          <input type="checkbox" id="webdav">
          <span class="knob"></span>
          <span class="lbl">WebDAV — mount the site as a network drive</span>
        </label>
        <span class="sub">Edit files from Finder, Explorer or rclone. The edit password is the login.</span>
      </div>

      <div class="opt" id="opt-domains">
        <label for="domain">Custom domains <span class="faint">(optional)</span></label>
        <div class="row">
          <input type="text" id="domain" placeholder="docs.example.com" style="max-width:280px">
          <button type="button" class="btn small" id="add-domain">Add</button>
        </div>
        <div class="filelist" id="domainlist" style="justify-content:flex-start"></div>
        <span class="sub">Point the domain at this server and TLS is issued automatically on first visit.</span>
      </div>
    </div>
  </details>

  <div class="publishrow">
    <button class="btn primary big" id="publish" disabled>Publish site</button>
    <span class="sub" id="publish-hint">Add files to publish — or publish an empty site and fill it later.</span>
  </div>
</section>

<section class="done hidden" id="done">
  <div class="ticket">
    <div class="stamp" id="stamp">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M20 6L9 17l-5-5" stroke-linecap="round" stroke-linejoin="round"/></svg>
      <span id="stamp-text">site published</span>
    </div>
    <h2>Your claim ticket</h2>
    <p class="sub" id="ticket-sub">Everything you need to view, share and edit this site.</p>

    <div class="secretrows">
      <div class="secretrow">
        <span class="k">View URL</span>
        <span class="v"><a id="t-view" href="#" target="_blank" rel="noopener"></a></span>
        <button class="copybtn" data-copy="t-view">Copy</button>
      </div>
      <div class="secretrow">
        <span class="k">Edit URL</span>
        <span class="v"><a id="t-edit" href="#" target="_blank" rel="noopener"></a></span>
        <button class="copybtn" data-copy="t-edit">Copy</button>
      </div>
      <div class="secretrow pass">
        <span class="k">Edit password</span>
        <span class="v" id="t-pass"></span>
        <button class="copybtn" data-copy="t-pass">Copy</button>
      </div>
    </div>

    <div class="keepnote" id="keepnote">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 9v4m0 4h.01M10.3 3.9L1.8 18a2 2 0 001.7 3h17a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z" stroke-linecap="round" stroke-linejoin="round"/></svg>
      <span><strong>Save the edit password now.</strong> It is shown only this once — Sitebin keeps just a hash and cannot recover it for you.</span>
    </div>

    <div class="ticketactions">
      <a class="btn primary" id="t-open" href="#" target="_blank" rel="noopener">Open site</a>
      <a class="btn" id="t-editlink" href="#" target="_blank" rel="noopener">Manage site</a>
      <button class="btn" id="t-again">Publish another</button>
    </div>
  </div>
</section>
`;

  function fmtBytes(n) {
    if (n < 1024) return n + " B";
    const units = ["KB", "MB", "GB"];
    let u = -1;
    do { n /= 1024; u++; } while (n >= 1024 && u < units.length - 1);
    return n.toFixed(n >= 10 ? 0 : 1) + " " + units[u];
  }

  class SitebinDrop extends HTMLElement {
    constructor() {
      super();
      this.state = { files: [], zip: null, domains: [], userTouchedMode: false, uploading: false };
    }

    connectedCallback() {
      if (this.shadowRoot) return;
      const root = this.attachShadow({ mode: "open" });
      const style = document.createElement("style");
      style.textContent = STYLE;
      root.appendChild(style);
      const wrap = document.createElement("div");
      wrap.innerHTML = MARKUP;
      root.appendChild(wrap);
      this.$ = (id) => root.getElementById(id);
      this._wire();
      this._applyAttrs();
      this._render();
    }

    get instance() {
      const v = (this.getAttribute("instance") || "").replace(/\/+$/, "");
      return v || SCRIPT_ORIGIN || location.origin;
    }
    get demo() { return this.hasAttribute("demo"); }
    get eventOnly() { return this.hasAttribute("event-only"); }

    _applyAttrs() {
      if (this.hasAttribute("no-domains")) this.$("opt-domains").classList.add("hidden");
      let host = "";
      try { host = new URL(this.instance).host; } catch {}
      this.$("fineprint").textContent = this.demo
        ? "demo mode — nothing leaves your browser"
        : (host ? "publishes to " + host : "");
    }

    // ---- staging ----

    _addFiles(list) {
      this._clearError();
      const s = this.state;
      const incoming = Array.from(list);
      if (incoming.length === 1 && /\.zip$/i.test(incoming[0].name) && s.files.length === 0) {
        s.zip = incoming[0];
        s.files = [];
      } else {
        if (s.zip) s.zip = null;
        for (const f of incoming) {
          const path = (f.webkitRelativePath || f.name).replace(/^\/+/, "");
          if (!path) continue;
          const existing = s.files.findIndex((x) => x.path === path);
          if (existing >= 0) s.files.splice(existing, 1);
          s.files.push({ file: f, path });
        }
      }
      this._render();
    }

    _suggestMode() {
      const s = this.state;
      if (s.userTouchedMode) return;
      let viewer = false;
      if (!s.zip && s.files.length >= 1) {
        const hasIndex = s.files.some((f) => /(^|\/)index\.html?$/i.test(f.path));
        const allDocs = s.files.every((f) => DOC_EXTS.test(f.path) && !/\.html?$/i.test(f.path));
        viewer = !hasIndex && allDocs;
      }
      const target = viewer ? "viewer" : "webserver";
      this.shadowRoot.querySelector(`input[name=mode][value=${target}]`).checked = true;
    }

    _render() {
      const s = this.state;
      const list = this.$("filelist");
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

      if (s.zip) {
        list.appendChild(mkChip(s.zip.name, s.zip.size, () => { s.zip = null; this._render(); }));
      } else {
        const max = 24;
        s.files.slice(0, max).forEach((f, i) =>
          list.appendChild(mkChip(f.path, f.file.size, () => { s.files.splice(i, 1); this._render(); })));
        if (s.files.length > max) {
          const more = document.createElement("span");
          more.className = "chip";
          more.textContent = "+" + (s.files.length - max) + " more";
          list.appendChild(more);
        }
      }

      const total = s.zip ? s.zip.size : s.files.reduce((sum, f) => sum + f.file.size, 0);
      const count = s.zip ? 1 : s.files.length;
      const totalEl = this.$("total");
      totalEl.classList.toggle("hidden", count === 0);
      totalEl.textContent = count + (count === 1 ? " file · " : " files · ") + fmtBytes(total);

      this.$("opt-unzip").classList.toggle("hidden", !s.zip);
      this.$("slab-title").textContent = count === 0 ? "Drop it in the bin" : "Ready to publish";
      this.$("publish").disabled = s.uploading;
      this.$("publish-hint").classList.toggle("hidden", count > 0);
      this._suggestMode();
    }

    _renderDomains() {
      const list = this.$("domainlist");
      list.innerHTML = "";
      this.state.domains.forEach((d, i) => {
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
        x.addEventListener("click", () => { this.state.domains.splice(i, 1); this._renderDomains(); });
        chip.append(name, x);
        list.appendChild(chip);
      });
    }

    _addDomain() {
      const input = this.$("domain");
      const d = input.value.trim().toLowerCase();
      if (!d) return;
      if (!/^[a-z0-9][a-z0-9.-]+\.[a-z0-9-]+$/.test(d)) {
        this._showError("That doesn't look like a domain name");
        return;
      }
      this._clearError();
      if (!this.state.domains.includes(d)) this.state.domains.push(d);
      input.value = "";
      this._renderDomains();
    }

    // ---- errors ----

    _showError(msg) {
      const bar = this.$("errbar");
      bar.textContent = msg;
      bar.classList.add("show");
    }
    _clearError() { this.$("errbar").classList.remove("show"); }

    // ---- publish ----

    _publish() {
      const s = this.state;
      if (s.uploading) return;
      this._clearError();
      s.uploading = true;
      this.$("slab").classList.add("uploading");
      this.$("publish").disabled = true;

      if (this.demo) {
        this._demoPublish();
        return;
      }

      const fd = new FormData();
      fd.append("mode", this.shadowRoot.querySelector("input[name=mode]:checked").value);
      const vp = this.$("view-password").value;
      if (vp) fd.append("view_password", vp);
      const exp = this.$("expires").value;
      if (exp) fd.append("expires_at", new Date(exp).toISOString());
      if (this.$("webdav").checked) fd.append("webdav", "true");
      s.domains.forEach((d) => fd.append("domain", d));

      if (s.zip) {
        if (this.$("unzip").checked) fd.append("zip", s.zip, s.zip.name);
        else fd.append("files", s.zip, s.zip.name);
      } else {
        s.files.forEach((f) => fd.append("files", f.file, f.path));
      }

      const xhr = new XMLHttpRequest();
      xhr.open("POST", this.instance + "/api/sites");
      xhr.upload.addEventListener("progress", (e) => {
        if (e.lengthComputable) this.$("progressbar").style.width = (e.loaded / e.total) * 100 + "%";
      });
      xhr.addEventListener("load", () => {
        this._uploadDone();
        let body = {};
        try { body = JSON.parse(xhr.responseText); } catch {}
        if (xhr.status === 201) {
          this._published(body);
        } else {
          this.$("publish").disabled = false;
          const msg = body.error || "Publishing failed (" + xhr.status + "). Please try again.";
          this._showError(msg);
          this._emit("sitebin-error", { status: xhr.status, error: msg });
        }
      });
      xhr.addEventListener("error", () => {
        this._uploadDone();
        this.$("publish").disabled = false;
        const msg = "Network error — the upload didn't go through. (Cross-origin embeds need SITEBIN_EMBED_ORIGINS on the instance.)";
        this._showError(msg);
        this._emit("sitebin-error", { status: 0, error: msg });
      });
      xhr.send(fd);
    }

    _demoPublish() {
      const bar = this.$("progressbar");
      bar.style.width = "0%";
      let pct = 0;
      const tick = setInterval(() => {
        pct = Math.min(100, pct + 12 + Math.random() * 18);
        bar.style.width = pct + "%";
        if (pct >= 100) {
          clearInterval(tick);
          let host = "sitebin.example";
          try { host = new URL(this.instance).host; } catch {}
          const id = "demo-" + Math.random().toString(36).slice(2, 8);
          const body = {
            demo: true,
            view_url: "https://" + id + "." + host,
            edit_url: "https://" + host + "/e/" + id,
            edit_password: "demo-" + Math.random().toString(36).slice(2, 12),
          };
          this._uploadDone();
          this._published(body);
        }
      }, 90);
    }

    _uploadDone() {
      this.state.uploading = false;
      this.$("slab").classList.remove("uploading");
      this.$("progressbar").style.width = "0%";
    }

    _published(body) {
      this._emit("sitebin-published", body);
      if (this.eventOnly) return;

      this.shadowRoot.querySelector(".create").classList.add("hidden");
      this.$("done").classList.remove("hidden");
      this.$("t-view").textContent = body.view_url;
      this.$("t-edit").textContent = body.edit_url;
      this.$("t-pass").textContent = body.edit_password;

      if (this.demo) {
        this.$("stamp-text").textContent = "demo — nothing was uploaded";
        this.$("ticket-sub").textContent = "On a real instance this ticket is your key to the site.";
        this.$("keepnote").classList.add("hidden");
        ["t-view", "t-edit", "t-open", "t-editlink"].forEach((id) => {
          this.$(id).removeAttribute("href");
        });
      } else {
        this.$("t-view").href = body.view_url;
        this.$("t-edit").href = body.edit_url;
        this.$("t-open").href = body.view_url;
        this.$("t-editlink").href = body.edit_url;
      }
    }

    _emit(name, detail) {
      this.dispatchEvent(new CustomEvent(name, { detail, bubbles: true, composed: true }));
    }

    // ---- public API ----

    reset() {
      const s = this.state;
      s.files = [];
      s.zip = null;
      s.domains = [];
      s.uploading = false;
      this._renderDomains();
      this._render();
      this.$("publish").disabled = false;
      this.$("done").classList.add("hidden");
      this.shadowRoot.querySelector(".create").classList.remove("hidden");
    }

    // ---- wiring ----

    _wire() {
      const $ = this.$;
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
          const s = this.state;
          if (files.length === 1 && /\.zip$/i.test(files[0].path) && s.files.length === 0) {
            s.zip = files[0].file;
          } else {
            s.zip = null;
            for (const f of files) {
              const existing = s.files.findIndex((x) => x.path === f.path);
              if (existing >= 0) s.files.splice(existing, 1);
              s.files.push(f);
            }
          }
          this._render();
        } else if (e.dataTransfer.files.length) {
          this._addFiles(e.dataTransfer.files);
        }
      });

      $("pick-files").addEventListener("click", () => $("input-files").click());
      $("pick-folder").addEventListener("click", () => $("input-folder").click());
      $("pick-zip").addEventListener("click", () => $("input-zip").click());
      ["input-files", "input-folder", "input-zip"].forEach((id) =>
        $(id).addEventListener("change", (e) => { this._addFiles(e.target.files); e.target.value = ""; }));

      this.shadowRoot.querySelectorAll("input[name=mode]").forEach((r) =>
        r.addEventListener("change", () => { this.state.userTouchedMode = true; }));

      $("add-domain").addEventListener("click", () => this._addDomain());
      $("domain").addEventListener("keydown", (e) => {
        if (e.key === "Enter") { e.preventDefault(); this._addDomain(); }
      });

      $("publish").addEventListener("click", () => this._publish());
      $("t-again").addEventListener("click", () => this.reset());

      this.shadowRoot.querySelectorAll(".copybtn").forEach((btn) =>
        btn.addEventListener("click", async () => {
          const ok = await this._copy($(btn.dataset.copy).textContent);
          if (ok) {
            btn.classList.add("done");
            const old = btn.textContent;
            btn.textContent = "Copied";
            setTimeout(() => { btn.classList.remove("done"); btn.textContent = old; }, 1400);
          }
        }));
    }

    async _copy(text) {
      try {
        await navigator.clipboard.writeText(text);
        return true;
      } catch {
        const ta = document.createElement("textarea");
        ta.value = text;
        document.body.appendChild(ta);
        ta.select();
        let ok = false;
        try { ok = document.execCommand("copy"); } catch {}
        ta.remove();
        return ok;
      }
    }
  }

  customElements.define("sitebin-drop", SitebinDrop);
})();
