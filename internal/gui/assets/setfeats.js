// Settings' search, through Plugins › Built-in too: a built-in's switch,
// and the settings it has there or on its own page (Gateway's Recent calls
// at a time), were found by nothing in Settings, and one switched off took
// its Settings rows away with it, so a search for it said no setting
// matched (the scattered settings audit after netube99 on X). Each built-in
// whose name, words or settings match is listed after Settings' own rows,
// off ones too, and Open goes to its row in Plugins › Built-in. Loaded by
// app.js the first time a search runs, as provlinks.js is.

// the built-in an Open is for: its row is lit (and scrolled to once) each
// time Plugins › Built-in draws it in the next few seconds, as the list is
// drawn again when the plugins are read
let featAt = null, featAtEnd = 0;
{
  const draw = window.renderBuiltins;
  window.renderBuiltins = (box, list, done) => {
    draw(box, list, done);
    const row = featAt && box.querySelector(`.feat-row[data-id="${featAt.id}"]`);
    if (!row) return;
    if (!featAt.scrolled) row.scrollIntoView({ block: "center" });
    featAt.scrolled = true;
    row.classList.add("flash");
  };
}
window.setFeatsFind = function (words) {
  let box = document.getElementById("setFeats");
  if (!box) {
    box = el("section", "set-page set-feats");
    box.id = "setFeats";
    box.append(el("div", "list prefs"));
    $("#view-settings").append(box);
  }
  const list = box.firstChild;
  const say = (s) => (s ? s + " " + t(s) : "");
  const found = ((words.length && prefs?.builtins) || []).filter((f) => {
    const props = Object.values(f.schema?.properties || {});
    const text = [say(f.name), say(f.sub), ...props.map((p) => say(p.title) + " " + say(p.description))].join(" ").toLowerCase();
    return words.every((w) => text.includes(w));
  });
  // drawn again only when it changes: a draw is a change Settings'
  // observer runs the search again on
  const key = document.documentElement.lang + " " + found.map((f) => f.id + (f.on ? "+" : "-")).join(" ");
  if (box.dataset.key === key) return found.length;
  box.dataset.key = key;
  list.replaceChildren();
  for (const f of found) {
    const r = el("div", "row pref feat-find");
    r.dataset.id = f.id;
    const who = el("div", "who");
    who.append(el("div", "name", t(f.name)));
    const sub = [f.on ? "" : t("Off"), f.sub ? t(f.sub) : ""].filter(Boolean).join(" · ");
    if (sub) who.append(el("div", "sub", sub));
    const go = el("button", "text", t("Open"));
    go.type = "button";
    go.onclick = () => {
      featAt = { id: f.id, scrolled: false };
      clearTimeout(featAtEnd);
      featAtEnd = setTimeout(() => { featAt = null; }, 3000);
      window.pluginTabTo?.("builtin");
      show("plugins");
    };
    r.append(who, go);
    list.append(r);
  }
  box.dataset.part = t("Plugins") + " › " + t("Built-in");
  box.hidden = !found.length;
  return found.length;
};
