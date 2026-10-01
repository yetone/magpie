// The saved theme (boot.js), or one the page's address asks for, on the page
// before its styles first apply: set only once app.js ran, at the foot of the
// page, a window kept dark under a light system was first drawn light, and
// the header's buttons faded from light to dark as it loaded. app.js sets it
// again; this one is for the first paint.
(function () {
  var asked = new URLSearchParams(location.search).get("theme");
  var saved = window.bootPrefs && window.bootPrefs.theme;
  var theme = asked || (saved && saved !== "system" ? saved : "");
  if (theme) document.documentElement.dataset.theme = theme;
})();
