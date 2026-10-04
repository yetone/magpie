// Run at the start of the body, before any UI can paint. app.js may still
// be downloading when the header appears, so its layout must already match
// the browser, desktop window or tray panel.
(function () {
  var mode = new URLSearchParams(location.search).get("mode") || "window";
  var web = !!(window.bootPrefs && window.bootPrefs.web);
  document.body.classList.add(mode);
  if (web) document.body.classList.add("web");
  if (!web && /^Mac/.test(navigator.platform)) document.body.classList.add("mac");
  if (!web && /^Linux/.test(navigator.platform)) document.body.classList.add("linux");
  if (/^Win/.test(navigator.platform)) document.documentElement.classList.add("win");
})();
