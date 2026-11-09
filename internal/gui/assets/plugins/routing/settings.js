// The routing plugin's part of Plugins › Built-in, run after app.js, whose
// featWho and helpers (el, t, featuresOff) it uses.
// Plugins › Built-in: Routing groups on while Routing is off have no page,
// as theirs is Routing's, though agents still pick them: the row says so,
// and turns Routing on with its own switch (netube99 on X)
featWho.groups = (line, f) => {
  if (!f.on || !featuresOff.has("routing")) return;
  const on = el("button", "link", t("Turn on Routing"));
  on.type = "button";
  on.onclick = () => line.closest(".feat-row")?.parentElement.querySelector('.feat-row[data-id="routing"] .lib-switch')?.click();
  line.append(el("span", "warn", t("Their page is on Routing, which is off")), " · ", on);
};
