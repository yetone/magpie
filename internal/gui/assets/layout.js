// Run at the start of the body, before any UI can paint. app.js may still
// be downloading when the header appears, so its layout must already match
// the browser, desktop window or tray panel.
(function () {
  var mode = new URLSearchParams(location.search).get("mode") || "window";
  var web = !!(window.bootPrefs && window.bootPrefs.web);
  document.body.classList.add(mode);
  if (window.bootPrefs && window.bootPrefs.reduceMotion) window.holdStill(true);
  if (web) document.body.classList.add("web");
  // Preserve the existing iOS viewport policy before app.js runs.
  if (web && (/iP(hone|ad|od)/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1))) {
    var viewport = document.querySelector('meta[name="viewport"]');
    if (viewport) viewport.setAttribute("content", "width=device-width, initial-scale=1, maximum-scale=1, viewport-fit=cover");
  }
  if (!web && /^Mac/.test(navigator.platform)) document.body.classList.add("mac");
  if (!web && /^Linux/.test(navigator.platform)) document.body.classList.add("linux");
  if (/^Win/.test(navigator.platform)) document.documentElement.classList.add("win");
  // the panel opens the main window from the edge nearest the tray: its
  // foot over Windows' bottom taskbar, its header elsewhere, until the app
  // says where the panel was placed (panelside.go, magpiePanelOpenAt)
  if (mode === "panel") document.body.dataset.openAt = /^Win/.test(navigator.platform) ? "foot" : "head";
  // --bar: see app.css, .view
  function bar() { var p = document.createElement("div"); p.className = "view"; p.style.cssText = "position:absolute;visibility:hidden;height:9px"; document.body.appendChild(p); document.documentElement.style.setProperty("--bar", p.offsetWidth - p.clientWidth + "px"); p.remove(); }
  bar();
  addEventListener("resize", bar);
})();
