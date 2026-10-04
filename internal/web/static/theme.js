// Light/dark theme for every page. Loaded in <head>, before the page
// renders, so there's no flash of the wrong theme: it sets data-theme on
// <html>, which switches the color tokens in style.css.
//
// The choice is the viewer's, stored in localStorage ("mo-theme"); without
// one the page follows the system setting. Other open tabs pick up a
// change through the storage event. Buttons with data-theme-set="light" /
// "dark" (the dashboard header's toggle) set it.

(function () {
  const KEY = "mo-theme";
  const media = window.matchMedia("(prefers-color-scheme: dark)");

  function stored() {
    try {
      const t = localStorage.getItem(KEY);
      return t === "light" || t === "dark" ? t : null;
    } catch (_) {
      return null;
    }
  }

  function syncButtons(theme) {
    for (const b of document.querySelectorAll("[data-theme-set]")) {
      const on = b.dataset.themeSet === theme;
      b.classList.toggle("is-active", on);
      b.setAttribute("aria-pressed", String(on));
    }
  }

  function apply() {
    const theme = stored() || (media.matches ? "dark" : "light");
    document.documentElement.dataset.theme = theme;
    syncButtons(theme);
  }

  function set(theme) {
    try { localStorage.setItem(KEY, theme); } catch (_) { /* this tab still switches */ }
    document.documentElement.dataset.theme = theme;
    syncButtons(theme);
  }

  apply();
  window.addEventListener("storage", (e) => { if (e.key === KEY) apply(); });
  media.addEventListener("change", apply);
  document.addEventListener("DOMContentLoaded", () => {
    for (const b of document.querySelectorAll("[data-theme-set]")) {
      b.addEventListener("click", () => set(b.dataset.themeSet));
    }
    syncButtons(document.documentElement.dataset.theme);
  });
})();
