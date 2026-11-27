// provcombo.js: the one way the window has the reader pick providers (the
// owner, 11-27: "最好是个Combo Box，另外要有供应商的Icon"). Each row and
// each chip has the provider's logo, typing narrows the list, and the
// arrows, Enter and Escape move through it. Three shapes, one list:
//
//   provField(box, o)  a field of chips with an × each, typed into to
//                      add more (Routing's Leave out, Sync's Not synced)
//   provPick(btn, o)   a filter's button, its logo and name on it, that
//                      drops the list with its search field atop
//   provPop(anchor, o) that list alone, for an action (Merge into…)
//
// o.items are {id, name, icon?, note?}; a row with no icon takes its
// provider's from /api/providers. Imported the first time one is drawn
// (provCombo in app.js), so the core scripts don't carry it. The guard,
// tests/provider-pick-guard.test.cjs, fails when a provider is picked any
// other way.

let pcN = 0;
const pcIcon = (x) => icon(x.icon || providers?.providers?.find((p) => p.id === x.id)?.icon || "generic");
const pcHit = (q) => {
  const words = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return (x) => words.every((w) => (x.name + " " + x.id).toLowerCase().includes(w));
};

// pcList: the listbox under a field or a search. s holds items, all (a
// first row for none, a filter's "All providers"), q, on (the value: an
// id, or a Set of them), at (the row the keys are on) and pick.
function pcList(s) {
  const ul = s.ul || (s.ul = el("ul", "pc-list"));
  ul.id ||= "pc" + ++pcN;
  ul.setAttribute("role", "listbox");
  if (s.multi) ul.setAttribute("aria-multiselectable", "true");
  ul.setAttribute("aria-label", t(s.label));
  const hit = s.items.filter(pcHit(s.q));
  s.rows = [...(s.all && !s.q.trim() ? [{ id: "", name: t(s.all), icon: "generic" }] : []), ...hit];
  if (s.at >= s.rows.length) s.at = s.rows.length - 1;
  ul.replaceChildren(...s.rows.map((x, i) => {
    const on = s.multi ? s.on.has(x.id) : s.on === x.id;
    const li = el("li", "pc-opt" + (on ? " cur" : "") + (i === s.at ? " sel" : ""));
    li.id = ul.id + "-" + i;
    li.setAttribute("role", "option");
    li.setAttribute("aria-selected", String(on));
    li.title = x.id;
    li.append(pcIcon(x), el("span", "v", x.name));
    if (x.note) li.append(el("span", "n", x.note));
    const c = el("span", "check");
    c.append(svg(CHECK, 13, 1.9));
    li.append(c);
    li.onmousedown = (e) => e.preventDefault(); // the field keeps its focus
    li.onmousemove = () => { if (s.at !== i) { s.at = i; pcAt(s); } };
    li.onclick = (e) => { e.stopPropagation(); s.pick(x.id, e); };
    return li;
  }));
  if (!s.rows.length) ul.append(el("li", "pc-none", t(s.items.length ? "No match" : s.empty || "No match")));
  pcAt(s);
}
// pcAt marks the row the keys are on and keeps it in the list's view,
// the page's own scroll untouched
function pcAt(s) {
  [...s.ul.children].forEach((li, i) => li.classList.toggle("sel", i === s.at));
  const li = s.ul.children[s.at];
  s.input.setAttribute("aria-activedescendant", li && s.at >= 0 ? li.id : "");
  if (!li || s.at < 0) return;
  const top = li.offsetTop - s.ul.offsetTop, h = s.ul.clientHeight;
  if (top < s.ul.scrollTop) s.ul.scrollTop = top - 5;
  else if (top + li.offsetHeight > s.ul.scrollTop + h) s.ul.scrollTop = top + li.offsetHeight - h + 5;
}
// pcKeys: Up and Down along the rows, Enter picks the one they are on
function pcKeys(e, s) {
  const n = s.rows.length;
  if ((e.key === "ArrowDown" || e.key === "ArrowUp") && n) {
    e.preventDefault();
    s.at = s.at < 0 ? (e.key === "ArrowDown" ? 0 : n - 1) : (s.at + (e.key === "ArrowDown" ? 1 : n - 1)) % n;
    pcAt(s);
    return true;
  }
  if (e.key === "Enter" && s.rows[s.at]) {
    e.preventDefault();
    s.pick(s.rows[s.at].id, e);
    return true;
  }
  return false;
}
function pcInput(s, label) {
  const q = el("input", "pc-in");
  q.type = "text";
  q.autocomplete = "off";
  q.spellcheck = false;
  q.setAttribute("role", "combobox");
  q.setAttribute("aria-autocomplete", "list");
  q.setAttribute("aria-expanded", "false");
  q.setAttribute("aria-controls", s.ul.id);
  q.setAttribute("aria-label", t(label));
  return q;
}

window.provPop = function (anchor, o) {
  closeProtoMenu();
  const s = { items: o.items, all: o.all, label: o.head, multi: false, on: o.value || "", q: "", at: 0 };
  // the field first: the list's rows name it the row the keys are on
  s.ul = el("ul", "pc-list");
  s.ul.id = "pc" + ++pcN;
  s.input = pcInput(s, o.head);
  s.input.placeholder = t("Search providers…");
  s.input.setAttribute("aria-expanded", "true");
  pcList(s);
  const box = el("div", "pop pc-pop pc-search");
  const top = el("div", "search");
  top.append(svg("M11.5 11.5 14 14 M7 2.5a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9Z", 13, 1.6), s.input);
  box.append(top, s.ul);
  s.at = Math.max(0, s.rows.findIndex((x) => x.id === s.on));
  s.pick = (id, e) => { closeProtoMenu(); o.pick(id, e); };
  s.input.oninput = () => { s.q = s.input.value; s.at = 0; pcList(s); };
  s.input.onkeydown = (e) => { e.stopPropagation(); pcKeys(e, s); };
  document.body.append(box);
  placeMenu(box, anchor, o.align || "left");
  pcAt(s);
  anchor.classList.add("open");
  anchor.setAttribute("aria-expanded", "true");
  holdMenu(box, anchor, () => {}, (e) => { if (scrollCloses(box, e)) closeProtoMenu(); });
  s.input.focus(NO_SCROLL);
};

window.provPick = function (btn, o) {
  btn.hidden = o.items.length < 2 && !o.value;
  btn.classList.add("pc-trig");
  btn.setAttribute("aria-haspopup", "listbox");
  btn.setAttribute("aria-expanded", "false");
  const face = (v) => {
    const cur = o.items.find((x) => x.id === v);
    btn.classList.toggle("set", !!v);
    btn.replaceChildren(...(v ? [pcIcon(cur || { id: v })] : []), el("span", "", v ? cur?.name || v : t(o.all)), svg(CHEV, 11, 1.6));
    btn.title = v || "";
  };
  face(o.value);
  btn.onclick = (e) => {
    e.stopPropagation();
    if (btn.classList.contains("open")) return closeProtoMenu();
    // the button says the pick at once, not when what it reads comes back (#1990)
    provPop(btn, { ...o, pick: (v, e) => { face(v); o.pick(v, e); } });
  };
};

// provField keeps itself across the page drawing it again: box._pc holds
// it, so the text being typed and the open list stay. Picks are made in
// the field and handed on (o.pick) as the list closes, or at once when a
// chip's × is clicked with it closed.
window.provField = function (box, o) {
  const s = box._pc || pcField(box);
  s.o = o;
  s.items = o.items;
  s.empty = o.empty;
  if (!s.open) s.on = new Set(o.value);
  s.input.placeholder = s.on.size ? "" : t(o.placeholder);
  s.input.setAttribute("aria-label", t(o.label));
  s.paint();
  if (s.open) pcList(s);
};
function pcField(box) {
  const s = { multi: true, q: "", at: 0, open: false };
  box._pc = s;
  box.classList.add("pc-field");
  const chips = el("span", "pc-chips");
  s.ul = el("ul", "pc-list");
  s.ul.id = "pc" + ++pcN;
  s.input = pcInput(s, "");
  const chev = svg(CHEV, 11, 1.6);
  box.replaceChildren(chips, s.input, chev);
  const pop = el("div", "pop pc-pop");
  pop.append(s.ul);
  const changed = () => [...s.on].sort().join("\n") !== [...s.o.value].sort().join("\n");
  const send = () => { if (changed()) s.o.pick([...s.on]); };
  s.paint = () => {
    s.label = s.o.label;
    chips.replaceChildren(...[...s.on].map((id) => {
      const x = s.items.find((y) => y.id === id) || { id, name: id };
      const c = el("span", "pc-chip");
      c.title = x.name;
      const rm = el("button", "pc-x");
      rm.type = "button";
      rm.tabIndex = -1;
      rm.setAttribute("aria-label", t("Remove {name}", { name: x.name }));
      rm.append(svg(CROSS, 9, 1.8));
      rm.onmousedown = (e) => e.preventDefault();
      rm.onclick = (e) => { e.stopPropagation(); drop(id); };
      c.append(pcIcon(x), el("span", "", x.name), rm);
      return c;
    }));
    s.input.placeholder = s.on.size ? "" : t(s.o.placeholder);
  };
  // the list follows the field as a chip comes or goes and it grows a line
  const place = () => { pop.classList.remove("up"); pop.style.maxHeight = ""; placeMenu(pop, box); };
  const drop = (id) => {
    s.on.delete(id);
    s.paint();
    if (s.open) { pcList(s); place(); } else send();
  };
  s.pick = (id) => {
    s.on.has(id) ? s.on.delete(id) : s.on.add(id);
    s.q = s.input.value = "";
    s.paint();
    pcList(s);
    place();
  };
  const open = () => {
    if (s.open) return;
    closeProtoMenu();
    s.open = true;
    s.at = 0;
    pcList(s);
    pop.style.width = Math.max(box.offsetWidth, 220) + "px";
    document.body.append(pop);
    place();
    box.classList.add("open");
    s.input.setAttribute("aria-expanded", "true");
    holdMenu(pop, box, () => {}, (e) => { if (scrollCloses(pop, e)) closeProtoMenu(); }, () => {
      s.open = false;
      s.q = s.input.value = "";
      s.input.setAttribute("aria-expanded", "false");
      s.input.setAttribute("aria-activedescendant", "");
      box.classList.remove("open");
      send();
    });
  };
  // a row's hidden lines (settingshints.js) don't come over the list
  for (const ev of ["focusin", "pointerover"]) box.addEventListener(ev, (e) => e.stopPropagation());
  s.input.onfocus = () => box.classList.add("focus");
  s.input.onblur = () => { box.classList.remove("focus"); if (s.open && protoMenu?.box === pop) closeProtoMenu(); };
  s.input.oninput = () => { s.q = s.input.value; s.at = 0; open(); pcList(s); place(); };
  s.input.onkeydown = (e) => {
    if (e.key === "Backspace" && !s.input.value && s.on.size) { e.preventDefault(); drop([...s.on].pop()); return; }
    if (!s.open && (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter")) { e.preventDefault(); open(); return; }
    if (s.open) { e.stopPropagation(); pcKeys(e, s); }
  };
  box.onmousedown = (e) => {
    if (e.target === s.input) return;
    e.preventDefault();
    s.input.focus(NO_SCROLL);
  };
  box.onclick = (e) => {
    e.stopPropagation();
    if (!s.open) { s.input.focus(NO_SCROLL); open(); } else if (e.target !== s.input) closeProtoMenu();
  };
  return s;
}
