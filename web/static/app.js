// Sitebin landing page. The create flow itself lives in the <sitebin-drop>
// component (served at /_sitebin/embed.js); this script renders the full
// claim ticket (QR code, copy-all) when the component reports a publish.
"use strict";

const $ = (id) => document.getElementById(id);

// ---- helpers ----

let toastTimer;
function toast(msg, isErr) {
  const el = $("toast");
  el.textContent = msg;
  el.classList.toggle("err", !!isErr);
  el.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), 3200);
}

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

document.addEventListener("sitebin-published", (e) => showTicket(e.detail));

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
  $("drop").reset();
  $("done").classList.add("hidden");
  $("create").classList.remove("hidden");
  window.scrollTo({ top: 0, behavior: "smooth" });
});

// ---- init ----

$("api-hint").textContent =
  'curl -F "files=@index.html" ' + location.origin + "/api/sites";
