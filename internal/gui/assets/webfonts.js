// A browser tab's fonts: the ones on the computer the browser runs on,
// which may not be the one magpie runs on (Norcleeh on Discord: magpie in
// Docker, its web GUI listed the container's fonts, never his own). Only
// a tab loads this (app.js loadFonts imports it); the app's windows list the
// installed fonts magpie reads itself.
//
// A tab's list is the server's families it draws, plus:
// - its computer's own, from the Local Font Access API (Chromium, on
//   https or localhost), asked the first time the family menu is opened,
//   since it needs a press and asks the user's permission once; read at
//   once when that permission is already given;
// - the families chosen in this tab (kept in localStorage by fonts.js);
// - any family typed into the menu's search that this browser draws, for
//   Safari, Firefox, a plain-http address or a refused permission.
// "Draws" is a canvas probe: text in the family measures apart from
// monospace's or serif's.
window.webFonts = (() => {
  let ctx, local = null, asked = false, base = [];
  const extra = [], seen = new Map();
  const quoted = window.desktopFonts.quoted;
  // The two generics' widths are measured once: each family then costs one
  // or two measures, not two or four.
  let generic;
  const width = (f) => (ctx.font = "72px " + f, ctx.measureText("mmmlli WwQ@ 0123 中文").width);
  function drawn(name) {
    if (!seen.has(name)) {
      ctx ||= document.createElement("canvas").getContext("2d");
      generic ||= ["monospace", "serif"].map((b) => [b, width(b)]);
      seen.set(name, !!name && generic.some(([b, w]) => width(quoted(name) + "," + b) !== w));
    }
    return seen.get(name);
  }
  const face = (family, name = "Regular", weight = 400, style = "normal", stretch = 100) => ({ family, name, weight, style, stretch });
  const regular = (family) => [face(family), face(family, "Bold", 700)];
  const same = window.desktopFonts.same;
  // a face's CSS traits from the style name the system gives it
  // ("SemiBold Condensed Italic")
  const first = (rules, s, d) => (rules.find(([re]) => re.test(s)) || [, d])[1];
  const WEIGHTS = [[/thin|hairline/, 100], [/(extra|ultra)-?light/, 200], [/light/, 300], [/(semi|demi)-?bold/, 600],
    [/(extra|ultra)-?bold/, 800], [/black|heavy/, 900], [/medium/, 500], [/bold/, 700]];
  const STRETCHES = [[/ultra-?condensed/, 50], [/extra-?condensed/, 62.5], [/semi-?condensed/, 87.5], [/condensed|narrow/, 75],
    [/semi-?expanded/, 112.5], [/extra-?expanded/, 150], [/ultra-?expanded/, 200], [/expanded|wide/, 125]];
  function group(data) {
    const fams = new Map();
    for (const d of data) {
      if (!d.family) continue;
      const s = String(d.style || "").toLowerCase().replace(/\s+/g, "");
      const f = face(d.family, d.style || "Regular", first(WEIGHTS, s, 400), /italic/.test(s) ? "italic" : /oblique/.test(s) ? "oblique" : "normal", first(STRETCHES, s, 100));
      const styles = fams.get(d.family) || [];
      if (!styles.some((o) => o.weight === f.weight && o.style === f.style && o.stretch === f.stretch)) styles.push(f);
      fams.set(d.family, styles);
    }
    return [...fams].map(([name, styles]) => ({ name, styles: styles.sort((a, b) => a.weight - b.weight || (a.style > b.style) - (a.style < b.style) || a.stretch - b.stretch) }));
  }
  function merge() {
    const out = new Map();
    for (const f of [...base, ...(local || []), ...extra]) out.set(f.name, f);
    const own = window.desktopFonts.own() || {};
    for (const c of [own.uiFont, own.codeFont]) {
      if (typeof c?.family !== "string" || !drawn(c.family)) continue;
      const f = out.get(c.family) || { name: c.family, styles: regular(c.family) };
      out.set(c.family, f.styles.some((s) => same(s, c)) ? f : { ...f, styles: [...f.styles, c] });
    }
    return [...out.values()].sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
  }
  // called in the press that opens the menu: queryLocalFonts needs it
  async function read() {
    local = group(await window.queryLocalFonts());
  }
  // list: the tab's families, from the server's (/api/fonts)
  // The probe yields every few milliseconds: a measure of a family not yet
  // drawn loads it, and probing a Mac's whole list at once held the click
  // that opened Settings in one task, 2.6 s with the CPU slowed 4x.
  async function list(server) {
    const out = [];
    let start = performance.now();
    for (const f of server) {
      if (drawn(f.name)) out.push(f);
      if (performance.now() - start > 8) await new Promise((r) => setTimeout(r)), start = performance.now();
    }
    base = out;
    if (!asked && window.queryLocalFonts) try {
      if ((await navigator.permissions.query({ name: "local-fonts" })).state === "granted") await read(), asked = true;
    } catch {}
    return merge();
  }
  // ask: the first time a family menu opens where the browser can tell
  // this computer's fonts, they join the list (app.js fontList)
  const catalogue = () => window.desktopFonts.catalogue(fontList = merge());
  function ask() {
    if (asked || !window.queryLocalFonts) return;
    asked = true; // a refusal isn't asked again on this page
    return read().then(catalogue, () => {});
  }
  // typing: the open family menu's search also takes a family the list
  // lacks; it is offered when this browser draws it, and chosen for key
  function typing(key) {
    const box = protoMenu?.box, q = box?.querySelector(".sc-q");
    if (!q) return;
    q.placeholder = t("Search or type a font name…");
    q.setAttribute("aria-label", q.placeholder);
    const b = el("button", "pm-item typed-font"), words = el("span", "pm-words"), name = el("span", "pm-name"), note = el("span", "pm-note");
    b.type = "button";
    b.hidden = true;
    words.append(name, note);
    b.append(el("span", "pm-tick"), words);
    box.lastChild.before(b); // above Refresh fonts
    hoverFocus(b);
    let family = "";
    q.addEventListener("input", () => {
      family = q.value.trim();
      const low = family.toLowerCase(), ok = drawn(family);
      b.hidden = !family || (fontList || []).some((f) => f.name.toLowerCase() === low);
      b.disabled = !ok;
      name.textContent = t("Use “{name}”", { name: family });
      note.textContent = ok ? "" : t("This browser can't find this font");
    });
    // Enter takes it when no listed family matched (app.js took that one)
    q.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.defaultPrevented && !b.hidden && !b.disabled) b.click(); });
    b.onclick = (e) => {
      e.stopPropagation();
      closeProtoMenu();
      const styles = regular(family);
      extra.push({ name: family, styles });
      catalogue();
      chooseFont(key, styles[0]);
    };
  }
  return { list, ask, typing, drawn };
})();
