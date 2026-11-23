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

  // ---- editor: Bearbeiten | Vorschau -----------------------------------------
  const showTabs = (root) => root.querySelectorAll(".editor .tabs").forEach((t) => { t.hidden = false; });
  showTabs(document);
  if (dialog) new MutationObserver(() => showTabs(dialog)).observe(dialog, { childList: true });

  document.addEventListener("click", async (e) => {
    const tab = e.target.closest(".editor [data-tab]");
    if (!tab) return;
    const form = tab.closest(".editor");
    const ta = form.querySelector("textarea");
    const pv = form.querySelector(".preview");
    const preview = tab.dataset.tab === "preview";
    form.querySelectorAll("[data-tab]").forEach((b) => b.setAttribute("aria-selected", String(b === tab)));
    if (preview) {
      pv.style.minHeight = ta.offsetHeight + "px";
      try {
        const res = await fetch("/preview", {
          method: "POST", credentials: "same-origin", body: new URLSearchParams({ body: ta.value }),
        });
        if (!res.ok || res.redirected) throw new Error(res.status);
        pv.innerHTML = await res.text();
      } catch {
        pv.textContent = "Vorschau gerade nicht verfügbar.";
      }
      if (tab.getAttribute("aria-selected") !== "true") return; // switched back meanwhile
    }
    ta.hidden = preview;
    pv.hidden = !preview;
    if (!preview) ta.focus();
  });

  // ---- auto refresh ----------------------------------------------------------
  const app = document.getElementById("app");
  if (!app) return;

  // ---- drag projects in the sidebar to reorder them ---------------------------
  let dragged = null;
  let draggedFrom = -1;
  // Only siblings (same parent project) swap places; the server counts the
  // index among them.
  const movable = (li) =>
    [...li.parentNode.querySelectorAll("li[data-move]")].filter((x) => x.dataset.parent === li.dataset.parent);

  app.addEventListener("dragstart", (e) => {
    const li = e.target.closest && e.target.closest(".projects li[data-move]");
    if (!li) return;
    dragged = li;
    draggedFrom = movable(li).indexOf(li);
    li.classList.add("dragging");
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", li.querySelector(".pname").textContent);
  });

  app.addEventListener("dragover", (e) => {
    if (!dragged) return;
    const li = e.target.closest(".projects li[data-move]");
    if (!li || li.dataset.parent !== dragged.dataset.parent) return;
    e.preventDefault();
    if (li === dragged) return;
    const r = li.getBoundingClientRect();
    // Below a sibling means after its whole subtree; above it means right before it.
    let before = li;
    if (e.clientY > r.top + r.height / 2) {
      const sibs = movable(li);
      const next = sibs[sibs.indexOf(li) + 1];
      before = next === dragged ? dragged.nextSibling : next || null;
      if (!next) {
        // last sibling: skip its subtree (deeper items that follow it)
        const depth = (el) => Number(el.style.getPropertyValue("--depth") || 0);
        let n = li.nextElementSibling;
        while (n && depth(n) > depth(li)) n = n.nextElementSibling;
        before = n;
      }
    }
    if (before !== dragged) li.parentNode.insertBefore(dragged, before);
  });

  app.addEventListener("drop", (e) => { if (dragged) e.preventDefault(); });

  app.addEventListener("dragend", async () => {
    if (!dragged) return;
    const li = dragged;
    dragged = null;
    li.classList.remove("dragging");
    const to = movable(li).indexOf(li);
    if (to === draggedFrom) return;
    try {
      const res = await fetch(li.dataset.move, {
        method: "POST", credentials: "same-origin", body: new URLSearchParams({ to: String(to) }),
      });
      if (!res.ok) throw new Error(res.status);
    } catch {
      /* the refresh below shows the order the server has */
    }
    refresh(); // the sidebar already shows the new order; this syncs the rest
  });

  const busy = () =>
    dragged ||
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
