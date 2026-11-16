// Settings › Network and sharing › Advanced › Name: what this magpie is
// called, said in its browser tab's title as "<name> · magpie" (starballl
// on X: several deployed, all titled magpie). MAGPIE_NAME names it while
// this is empty, and is the field's placeholder; a name typed here wins
// over it. app.js imports this when Settings is drawn, so it weighs
// nothing on the core.

window.instanceName = function instanceName(s, keep) {
  const list = $("#netMoreList");
  if (!list) return;
  // a redraw (another setting saved) never takes the field from under
  // whoever is typing in it
  if (list.contains(document.activeElement)) return;
  const field = input(s.name || "", s.nameEnv || "magpie");
  field.className = "words instance-name";
  field.maxLength = 60;
  field.spellcheck = false;
  field.setAttribute("aria-label", t("Name"));
  field.onchange = () => {
    const v = field.value.trim().replace(/\s+/g, " ");
    if (v !== (s.name || "")) savePrefs({ ...keep, name: v });
  };
  field.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") field.blur(); };
  const r = el("div", "row pref instance-name-row");
  const who = el("div", "who");
  who.append(el("div", "name", t("Name")), el("div", "sub", t("Shown in the browser tab’s title")));
  const val = el("div", "val");
  val.append(field);
  r.append(who, val);
  list.replaceChildren(r);
  if (s.name) $("#netMore").open = true;
};
