// Progressive enhancement: everything works without this file via plain
// forms. With it, notes open in a dialog, deletes ask first, Ctrl/Cmd+Enter
// submits, and the board refreshes itself.
(() => {
  "use strict";
  const REFRESH_MS = 30000;
  const dialog = document.getElementById("editor");

  // ---- confirmations -------------------------------------------------------
  document.addEventListener("submit", (e) => {
    const msg = e.target.dataset && e.target.dataset.confirm;
    if (msg && !confirm(msg)) e.preventDefault();
  });

  // ---- Ctrl/Cmd+Enter submits the surrounding form ---------------------------
  document.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && e.target.form) {
      e.preventDefault();
      e.target.form.requestSubmit();
    }
  });

  // ---- editor dialog ---------------------------------------------------------
  if (dialog) {
    document.addEventListener("click", async (e) => {
      const a = e.target.closest("a.note-open");
      if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0) return;
      e.preventDefault();
      try {
        const res = await fetch(a.href + "?fragment=1", { credentials: "same-origin" });
        if (!res.ok || res.redirected) throw new Error(res.status);
        dialog.innerHTML = await res.text();
      } catch {
        location.href = a.href; // fall back to the full editor page
        return;
      }
      dialog.showModal();
      const ta = dialog.querySelector("textarea");
      if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); }
    });

    dialog.addEventListener("click", (e) => {
      if (e.target.closest("[data-close]")) { e.preventDefault(); dialog.close(); }
      else if (e.target === dialog) dialog.close(); // click on the backdrop
    });
    dialog.addEventListener("close", () => { dialog.innerHTML = ""; });
  }

  // live color preview in the editor (dialog and full page)
  document.addEventListener("change", (e) => {
    const r = e.target;
    if (r.name !== "color" || !r.dataset.bg) return;
    const form = r.closest(".editor");
    form.style.setProperty("--bg", r.dataset.bg);
    form.style.setProperty("--fg", r.dataset.fg);
  });

  // ---- auto refresh ----------------------------------------------------------
  const app = document.getElementById("app");
  if (!app) return;

  const busy = () =>
    (dialog && dialog.open) ||
    app.querySelector("details[open]") ||
    [...app.querySelectorAll("textarea, input:not([type=checkbox])")].some(
      (el) => el === document.activeElement || el.value.trim() !== "" && el.defaultValue !== el.value
    );

  let refreshing = false;
  async function refresh() {
    if (refreshing || document.hidden || busy()) return;
    refreshing = true;
    try {
      const res = await fetch(location.pathname + "?fragment=1", { credentials: "same-origin" });
      if (res.redirected) { location.href = res.url; return; }
      if (!res.ok) return;
      const html = await res.text();
      if (busy()) return;
      const nav = document.getElementById("nav-toggle");
      const open = nav && nav.checked;
      app.innerHTML = html;
      const nav2 = document.getElementById("nav-toggle");
      if (nav2) nav2.checked = open;
    } catch {
      /* offline: try again later */
    } finally {
      refreshing = false;
    }
  }
  setInterval(refresh, REFRESH_MS);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
})();
