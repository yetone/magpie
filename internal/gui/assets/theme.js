// Apply the saved theme and text size (boot.js) before styles first apply.
// A theme requested by the page's address takes precedence. Waiting for
// app.js at the foot of the page let the header paint in the system theme
// and default size first, then change as the page loaded.
(function () {
  var asked = new URLSearchParams(location.search).get("theme");
  var saved = window.bootPrefs && window.bootPrefs.theme;
  var theme = asked || (saved && saved !== "system" ? saved : "");
  if (theme) document.documentElement.dataset.theme = theme;
  var prefs = window.bootPrefs;
  if (prefs) document.documentElement.style.setProperty("--zoom", prefs.web ? 1 : (prefs.textSize || 100) / 100);
})();
