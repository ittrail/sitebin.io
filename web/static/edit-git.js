// The Git tab of the edit page: how to connect, the repository's status, its
// branches and tags, the history, a read-only file browser and the repository's
// deletion. Loaded after edit.js, which owns api(), site, tabs and toast().
//
// DISPLAY SAFETY. Everything in a repository -- file names, commit messages,
// author names, branch names, file text -- is attacker-controlled. It enters
// the DOM only through textContent / createTextNode (the el() helper below),
// never through innerHTML, and a link's href is never built from it unencoded.
"use strict";

// gitCredentialLines are the `git config` lines that make the Git Credential
// Manager sign in with the account (OAuth), for the repository at repoURL.
// Git hands a credential helper only the scheme and host of a remote, unless
// credential.useHttpPath is set, so the lines are scoped to the instance's
// origin: lines keyed to the repository's URL would never be read. README.md
// and the website's /docs/git/ show the same lines.
function gitCredentialLines(repoURL, o) {
  const base = new URL(repoURL).origin;
  return [
    "git config --global credential." + base + ".provider generic",
    "git config --global credential." + base + ".oauthClientId " + o.client_id,
    "git config --global credential." + base + ".oauthAuthorizeEndpoint " + o.authorize_endpoint,
    "git config --global credential." + base + ".oauthTokenEndpoint " + o.token_endpoint,
    "git config --global credential." + base + ".oauthDeviceEndpoint " + o.device_endpoint,
    "git config --global credential." + base + ".oauthScopes \"" + (o.scopes || []).join(" ") + "\"",
  ];
}

const gitTab = (() => {
  // Highlighting runs on text up to this size; the API returns at most
  // 512 KiB, and highlight.js on all of that is visibly slow, so a larger
  // file is shown plain.
  const HIGHLIGHT_MAX = 200 * 1024;

  // A language is applied only from this map of file extensions. There is no
  // auto-detection: a name that is not here is shown as plain text.
  const LANGS = {
    js: "javascript", mjs: "javascript", cjs: "javascript", jsx: "javascript",
    ts: "typescript", tsx: "typescript", json: "json", html: "xml", htm: "xml",
    xml: "xml", svg: "xml", css: "css", scss: "scss", md: "markdown",
    py: "python", rb: "ruby", go: "go", rs: "rust", java: "java", kt: "kotlin",
    c: "c", h: "c", cpp: "cpp", cc: "cpp", hpp: "cpp", cs: "csharp", php: "php",
    sh: "bash", bash: "bash", yml: "yaml", yaml: "yaml", toml: "ini", ini: "ini",
    sql: "sql", swift: "swift", lua: "lua", diff: "diff", patch: "diff",
  };

  const S = {
    info: null,
    refs: [],
    ref: "",       // the branch or tag shown
    commit: null,  // the opened commit's detail, or null
    treeRef: "",   // what the file browser reads: the commit's sha, else ref
    path: "",
    logGen: 0,
    filesGen: 0,
    commitGen: 0,  // the newest commit asked for; an older answer is dropped
    loadedAt: 0,
  };

  const q = (id) => $(id);

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

  async function copy(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast("Copied");
    } catch {
      toast("Copy failed -- select the text instead", true);
    }
  }

  function cmdBlock(lines, label) {
    const wrap = el("div", "gitcmd");
    wrap.appendChild(el("pre", "", lines.join("\n")));
    wrap.appendChild(button(label || "Copy", "", () => copy(lines.join("\n"))));
    return wrap;
  }

  function fmtDate(s) {
    const d = new Date(s);
    return isNaN(d) ? "" : d.toLocaleString();
  }
  function fmtDay(s) {
    const d = new Date(s);
    return isNaN(d) ? "" : d.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
  }

  const qs = (o) => "?" + Object.keys(o).filter((k) => o[k] !== "" && o[k] !== undefined)
    .map((k) => encodeURIComponent(k) + "=" + encodeURIComponent(o[k])).join("&");

  // Downloads carry the edit password in a header, so a plain link cannot do:
  // the click fetches the bytes with api headers and saves the blob.
  async function download(path, filename) {
    try {
      const res = await fetch("/api/sites/" + editID + path, { headers: authHeaders() });
      if (!res.ok) throw new Error("download failed (" + res.status + ")");
      const url = URL.createObjectURL(await res.blob());
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (err) { toast(err.message, true); }
  }

  function downloadLink(text, path, filename) {
    const a = el("a", "", text);
    a.href = "#git";
    a.addEventListener("click", (e) => { e.preventDefault(); download(path, filename); });
    return a;
  }

  // ---- connect ----

  function renderConnect() {
    const box = q("git-connect");
    box.textContent = "";
    const info = S.info;
    const u = el("div", "gitcmd");
    const code = el("code", "", info.url);
    u.appendChild(code);
    u.appendChild(button("Copy URL", "", () => copy(info.url)));
    box.appendChild(u);
    box.appendChild(cmdBlock(["git remote add sitebin " + info.url, "git push sitebin main"], "Copy commands"));

    const ul = el("ul", "gitways");
    if (info.credentials && info.credentials.password) {
      const li = el("li");
      li.appendChild(el("strong", "", "Edit password: "));
      li.appendChild(document.createTextNode("username: "));
      li.appendChild(el("code", "", editID));
      li.appendChild(document.createTextNode(", password: this site's edit password."));
      ul.appendChild(li);
    }
    if (info.credentials && info.credentials.token) {
      const li = el("li");
      li.appendChild(el("strong", "", "API token: "));
      li.appendChild(document.createTextNode("use an account token as the password. Create one under "));
      const a = el("a", "", "API tokens");
      a.href = "/account";
      li.appendChild(a);
      li.appendChild(document.createTextNode("."));
      ul.appendChild(li);
    }
    if (ul.children.length) box.appendChild(ul);

    const o = info.credentials && info.credentials.oauth;
    if (o) {
      const sec = el("div", "gitoauth");
      sec.appendChild(el("strong", "", "Sign in with your account (OAuth)"));
      sec.appendChild(cmdBlock(gitCredentialLines(info.url, o), "Copy all"));
      sec.appendChild(el("p", "sub", "These lines make the account sign-in the way in for every repository on " +
        new URL(info.url).host + ", on this machine: Git asks its credential manager per host, not per repository."));
      const note = el("p", "sub", "Git Credential Manager comes with Git for Windows; on macOS and Linux install it first: ");
      const a = el("a", "", "installation docs");
      a.href = "https://github.com/git-ecosystem/git-credential-manager/blob/release/docs/install.md";
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      note.appendChild(a);
      sec.appendChild(note);
      box.appendChild(sec);
    }
  }

  // ---- status ----

  function renderStatus() {
    const info = S.info;
    const box = q("git-status");
    box.textContent = "";

    // A plan without Git and no repository: there is nothing to connect to,
    // read or keep, only the upgrade.
    const upgradeOnly = info.reason === "plan" && !info.exists;
    q("git-sec-connect").classList.toggle("hidden", upgradeOnly);
    q("git-lifecycle").classList.toggle("hidden", upgradeOnly);
    if (upgradeOnly) {
      const p = el("p", "gitplan", "Your plan does not include Git. ");
      const a = el("a", "", "Upgrade your plan");
      a.href = "/account";
      p.appendChild(a);
      p.appendChild(document.createTextNode(" to keep this site's source in a Git repository beside it."));
      box.appendChild(p);
      return;
    }

    const u = site.usage;
    const gitB = u.git_bytes || 0;
    const filesB = Math.max(0, u.bytes - gitB);
    const bar = el("div", "usagebar split");
    const pf = u.max_bytes ? Math.min(100, filesB / u.max_bytes * 100) : 0;
    const pg = u.max_bytes ? Math.min(100 - pf, gitB / u.max_bytes * 100) : 0;
    const f1 = el("div", "fill"); f1.style.width = pf + "%";
    const f2 = el("div", "fill git"); f2.style.width = pg + "%";
    bar.appendChild(f1); bar.appendChild(f2);
    box.appendChild(bar);
    box.appendChild(el("div", "usagenote",
      "Files " + fmtBytes(filesB) + " · Git " + fmtBytes(gitB) + " · of " + (u.max_bytes ? fmtBytes(u.max_bytes) : "unlimited")));

    const dl = el("dl", "gitfacts");
    const fact = (k, v) => { dl.appendChild(el("dt", "", k)); dl.appendChild(el("dd", "", v)); };
    if (info.exists) {
      fact("Default branch", info.default_branch || "(none yet)");
      fact("Last push", info.last_push
        ? fmtDate(info.last_push.at) + (info.last_push.detail ? " · " + info.last_push.detail : "")
        : "none yet");
    }
    box.appendChild(dl);
    if (info.exists) {
      box.appendChild(el("p", "sub", "Force pushes are fine: the bytes of history one replaces come back " +
        "within a day or two. “Delete repository” below frees all of them at once."));
    }

    const life = q("git-expiry");
    life.textContent = info.expires_at ? "The site expires on " + fmtDay(info.expires_at) + "; the repository goes with it." : "";

    if (info.reason === "plan") {
      const p = el("p", "gitplan", "Your plan has no Git pushes. The repository stays readable. ");
      const a = el("a", "", "Upgrade your plan");
      a.href = "/account";
      p.appendChild(a);
      p.appendChild(document.createTextNode("."));
      box.appendChild(p);
    }
    if (!info.exists) {
      const h = el("div", "gitstart");
      h.appendChild(el("strong", "", "Start here"));
      h.appendChild(cmdBlock(["git remote add sitebin " + info.url, "git push sitebin main"], "Copy"));
      box.appendChild(h);
    }
  }

  // ---- branches and tags ----

  function renderRefs() {
    const box = q("git-refs");
    box.textContent = "";
    if (!S.refs.length) { box.appendChild(el("p", "sub", "No branches yet.")); return; }
    const list = el("div", "gitrefs");
    for (const r of S.refs) {
      const b = el("button", "gitref" + (r.name === S.ref && !S.commit ? " on" : ""));
      b.type = "button";
      b.appendChild(el("span", "kind", r.kind === "tag" ? "tag" : "branch"));
      b.appendChild(el("span", "name", r.name));
      if (r.default) b.appendChild(el("span", "dflt", "default"));
      if (r.commit) b.title = r.commit.subject || "";
      b.addEventListener("click", () => selectRef(r.name));
      list.appendChild(b);
    }
    box.appendChild(list);
  }

  function selectRef(name) {
    S.commitGen++; // a commit still loading must not open over this ref
    S.ref = name;
    S.commit = null;
    S.treeRef = name;
    S.path = "";
    renderRefs();
    renderCommit();
    loadLog();
    loadFiles();
  }

  // ---- history ----

  async function loadLog() {
    const gen = ++S.logGen;
    const box = q("git-log");
    box.textContent = "";
    if (!S.ref) return;
    try {
      const page = await api("GET", "/git/log" + qs({ ref: S.ref }));
      if (gen !== S.logGen) return;
      appendCommits(box, page, gen);
    } catch (err) {
      if (gen === S.logGen) box.appendChild(el("p", "inline-status err", err.message));
    }
  }

  function appendCommits(box, page, gen) {
    const old = box.querySelector(".more");
    if (old) old.remove();
    for (const c of page.commits || []) {
      const row = el("button", "gitcommit");
      row.type = "button";
      row.appendChild(el("span", "sha", c.sha.slice(0, 7)));
      row.appendChild(el("span", "subj", c.subject));
      row.appendChild(el("span", "who", c.author + " · " + fmtDate(c.date)));
      row.addEventListener("click", () => openCommit(c.sha));
      box.appendChild(row);
    }
    if (!(page.commits || []).length) box.appendChild(el("p", "sub", "No commits."));
    if (page.next_skip != null) {
      const more = button("Show more", "more", async () => {
        more.disabled = true;
        try {
          const next = await api("GET", "/git/log" + qs({ ref: page.sha, skip: page.next_skip }));
          if (gen !== S.logGen) return;
          appendCommits(box, next, gen);
        } catch (err) { more.disabled = false; toast(err.message, true); }
      });
      box.appendChild(more);
    }
  }

  async function openCommit(sha) {
    // Only the newest click counts: a slow answer to an earlier one must
    // neither show its commit nor point the Files section at it.
    const gen = ++S.commitGen;
    try {
      const d = await api("GET", "/git/commit/" + encodeURIComponent(sha));
      if (gen !== S.commitGen) return;
      S.commit = d;
      S.treeRef = d.sha;
      S.path = "";
      renderRefs();
      renderCommit();
      loadFiles();
    } catch (err) { if (gen === S.commitGen) toast(err.message, true); }
  }

  function renderCommit() {
    const box = q("git-commit");
    box.textContent = "";
    box.classList.toggle("hidden", !S.commit);
    const d = S.commit;
    if (!d) return;
    const head = el("div", "row");
    head.appendChild(el("strong", "", "Commit " + d.sha.slice(0, 7)));
    head.appendChild(button("Close", "", () => selectRef(S.ref)));
    box.appendChild(head);
    box.appendChild(el("pre", "gitmsg", d.message || d.subject));
    const ppl = el("div", "sub");
    ppl.textContent = "Author: " + d.author + (d.author_email ? " <" + d.author_email + ">" : "") + " · " + fmtDate(d.date) +
      " · Committer: " + d.committer + " · " + fmtDate(d.committer_date);
    box.appendChild(ppl);
    if (d.parents && d.parents.length) {
      const p = el("div", "sub", "Parents: ");
      d.parents.forEach((sha, i) => {
        if (i) p.appendChild(document.createTextNode(", "));
        const a = el("a", "", sha.slice(0, 7));
        a.href = "#git";
        a.addEventListener("click", (e) => { e.preventDefault(); openCommit(sha); });
        p.appendChild(a);
      });
      box.appendChild(p);
    }
    const files = d.files || [];
    const ul = el("ul", "gitchanged");
    for (const f of files) {
      const li = el("li");
      li.appendChild(el("span", "st", f.status));
      li.appendChild(el("span", "fp", f.path));
      li.appendChild(el("span", "ch", f.removed < 0 || f.added < 0 ? "binary" : "+" + f.added + " −" + f.removed));
      ul.appendChild(li);
    }
    box.appendChild(ul);
    if (d.files_truncated) box.appendChild(el("p", "sub", "Only the first " + files.length + " changed files are shown; this commit changes more."));
    box.appendChild(el("p", "sub", "The Files section below now shows this commit."));
  }

  // ---- files ----

  function crumbs() {
    const box = q("git-crumbs");
    box.textContent = "";
    const label = S.commit ? S.commit.sha.slice(0, 7) : S.ref;
    const root = el("button", "", label);
    root.type = "button";
    root.addEventListener("click", () => openPath(""));
    box.appendChild(root);
    const parts = S.path ? S.path.split("/") : [];
    parts.forEach((p, i) => {
      box.appendChild(el("span", "sep", "/"));
      const b = el("button", "", p);
      b.type = "button";
      const target = parts.slice(0, i + 1).join("/");
      b.addEventListener("click", () => openPath(target));
      box.appendChild(b);
    });
    if (S.path) {
      const up = parts.slice(0, -1).join("/");
      box.appendChild(button("Back", "", () => openPath(up)));
    }
  }

  function openPath(p) { S.path = p; loadFiles(); }

  async function loadFiles() {
    const gen = ++S.filesGen;
    crumbs();
    const box = q("git-tree");
    box.textContent = "";
    if (!S.treeRef) return;
    try {
      const t = await api("GET", "/git/tree" + qs({ ref: S.treeRef, path: S.path }));
      if (gen !== S.filesGen) return;
      renderTree(box, t);
    } catch (err) {
      if (gen !== S.filesGen) return;
      // A path that is a file is "not found" as a folder: then, and only
      // then, it is read as a file. Any other failure (no access, busy, the
      // network) is shown as what it is.
      if (err.status !== 404 || !S.path) {
        box.appendChild(el("p", "inline-status err", err.message));
        return;
      }
      try {
        const b = await api("GET", "/git/blob" + qs({ ref: S.treeRef, path: S.path }));
        if (gen !== S.filesGen) return;
        renderBlob(box, b);
      } catch (err2) {
        if (gen === S.filesGen) box.appendChild(el("p", "inline-status err", err2.message));
      }
    }
  }

  function join(dir, name) { return dir ? dir + "/" + name : name; }

  function renderTree(box, t) {
    const entries = t.entries || [];
    if (!entries.length) box.appendChild(el("p", "sub", "This folder is empty."));
    for (const e of entries) {
      const row = el("button", "gitentry " + e.type);
      row.type = "button";
      row.appendChild(el("span", "ic", e.type === "dir" ? "▸" : e.type === "file" ? "·" : "↪"));
      row.appendChild(el("span", "nm", e.name));
      if (e.type === "file") row.appendChild(el("span", "sz", fmtBytes(e.size || 0)));
      if (e.type === "dir") row.addEventListener("click", () => openPath(join(S.path, e.name)));
      else if (e.type === "file") row.addEventListener("click", () => openFile(join(S.path, e.name)));
      else row.disabled = true;
      box.appendChild(row);
    }
    if (t.truncated) box.appendChild(el("p", "sub", "The folder is large; only the first entries are shown."));
  }

  async function openFile(path) {
    const gen = ++S.filesGen;
    const box = q("git-tree");
    S.path = path;
    crumbs();
    box.textContent = "";
    try {
      const b = await api("GET", "/git/blob" + qs({ ref: S.treeRef, path }));
      if (gen !== S.filesGen) return;
      renderBlob(box, b);
    } catch (err) {
      if (gen === S.filesGen) box.appendChild(el("p", "inline-status err", err.message));
    }
  }

  let hljsLoading = null;
  function loadHljs() {
    if (window.hljs) return Promise.resolve();
    if (!hljsLoading) {
      hljsLoading = new Promise((resolve, reject) => {
        const css = document.createElement("link");
        css.rel = "stylesheet";
        css.href = "/_sitebin/assets/vendor/highlight-theme.min.css";
        document.head.appendChild(css);
        const s = document.createElement("script");
        s.src = "/_sitebin/assets/vendor/highlight.min.js";
        s.onload = () => resolve();
        s.onerror = () => { hljsLoading = null; reject(new Error("highlighter failed to load")); };
        document.head.appendChild(s);
      });
    }
    return hljsLoading;
  }

  function langOf(path) {
    const name = path.split("/").pop();
    const i = name.lastIndexOf(".");
    if (i <= 0) return "";
    const l = LANGS[name.slice(i + 1).toLowerCase()];
    return l || "";
  }

  function renderBlob(box, b) {
    const name = S.path.split("/").pop();
    const dlPath = "/git/blob" + qs({ ref: S.treeRef, path: S.path, download: "1" });
    if (b.binary || b.truncated) {
      const p = el("p", "sub", (b.binary ? "Binary file" : "Large file, not shown") + " · " + fmtBytes(b.size) + " · ");
      p.appendChild(downloadLink("Download", dlPath, name));
      box.appendChild(p);
      return;
    }
    const head = el("p", "sub", fmtBytes(b.size) + " · ");
    head.appendChild(downloadLink("Download", dlPath, name));
    box.appendChild(head);
    const pre = el("pre", "gitfile");
    const code = document.createElement("code");
    code.textContent = b.text; // text, never markup
    pre.appendChild(code);
    box.appendChild(pre);
    const lang = langOf(S.path);
    if (lang && (b.text || "").length <= HIGHLIGHT_MAX) {
      code.className = "language-" + lang;
      loadHljs().then(() => {
        if (!code.isConnected) return;
        try { window.hljs.highlightElement(code); } catch {}
      }).catch(() => {});
    }
  }

  // ---- danger ----

  function renderDanger() {
    const box = q("git-danger");
    box.classList.toggle("hidden", !S.info.exists || S.info.role === "admin");
    const btn = q("git-delete");
    const hint = q("git-delete-hint");
    const size = fmtBytes(site.usage.git_bytes || 0);
    hint.textContent = "Removes the repository and its history (" + size + "). The site's files are not touched.";
    let armed = false, timer;
    btn.onclick = async () => {
      if (!armed) {
        armed = true;
        btn.textContent = "Click again to delete the repository";
        hint.textContent = "This deletes the repository and its history (" + size + "). The site's files are not touched.";
        timer = setTimeout(() => { armed = false; btn.textContent = "Delete repository"; renderDanger(); }, 5000);
        return;
      }
      clearTimeout(timer);
      armed = false;
      btn.textContent = "Delete repository";
      try {
        await api("DELETE", "/git");
        toast("Repository deleted");
        site = await api("GET", "");
        render();
        await show(true);
      } catch (err) { toast(err.message, true); }
    };
  }

  // ---- the tab ----

  // showError says why the tab could not load, in one place: showing the
  // tab again replaces the message (or clears it), never adds another.
  function showError(msg) {
    const panel = q("panel-git");
    let p = q("git-error");
    if (!p) {
      p = el("p", "inline-status err");
      p.id = "git-error";
      panel.insertBefore(p, panel.firstChild);
    }
    p.textContent = msg;
    p.classList.toggle("hidden", !msg);
  }

  async function show(force) {
    // Re-showing the tab keeps what the visitor was reading, unless a
    // minute has passed (a push may have landed since).
    if (!force && S.info && Date.now() - S.loadedAt < 60000) return;
    try {
      S.info = await api("GET", "/git");
    } catch (err) {
      showError(err.message);
      return;
    }
    showError("");
    S.commitGen++;
    S.loadedAt = Date.now();
    renderConnect();
    renderStatus();
    renderDanger();
    const has = S.info.exists;
    for (const id of ["git-sec-refs", "git-sec-history", "git-sec-files"]) q(id).classList.toggle("hidden", !has);
    S.commit = null;
    S.path = "";
    renderCommit();
    if (!has) { S.refs = []; S.ref = ""; S.treeRef = ""; return; }
    try {
      S.refs = (await api("GET", "/git/refs")).refs || [];
    } catch (err) { S.refs = []; toast(err.message, true); }
    const def = S.refs.find((r) => r.default) || S.refs[0];
    S.ref = def ? def.name : "";
    S.treeRef = S.ref;
    renderRefs();
    loadLog();
    loadFiles();
  }

  q("git-zip").addEventListener("click", () => {
    if (!S.treeRef) return;
    download("/git/archive" + qs({ ref: S.treeRef }), editID + "-" + (S.commit ? S.commit.sha.slice(0, 7) : S.ref.replace(/[^A-Za-z0-9._-]/g, "_")) + ".zip");
  });

  return { show: () => show(false), reload: () => show(true), state: S };
})();
