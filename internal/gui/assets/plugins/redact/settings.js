// Settings › Privacy's Instead of masking, send to, and Masked words and
// Never mask as lists, Masked words as dots until Show (the redact
// plugin's part of Settings). Run after app.js, whose helpers it shares
// (el, icon, openPicker, modelOpt, prefWrite, api), and before start.js
// draws Settings. It wraps renderRedact, so app.js carries none of it: the row
// shows while anything is masked, after the masked-words rows.

// redactRouteRow: where a request masking would change goes instead
// (redactRouteTo, jessyoungyu on X): Off masks it as before; a model whose
// provider the user set to Send requests unmasked (maskRouteModels, on
// this computer or the network) gets it as written, so 「百度这家公司怎么样」
// is answered about Baidu, not a placeholder. Picked in the app's menu,
// saved on its own (settings/redact-route).
function redactRouteRow(s) {
  const models = s.maskRouteModels || [];
  const v = s.redactRouteTo || "";
  const b = el("button", "rt-cond on");
  b.type = "button";
  b.setAttribute("aria-label", t("Model for requests masking would change"));
  const m = models.find((x) => x.id === v);
  if (v) b.append(icon(m?.icon || "generic"), el("span", "", m ? `${m.name || m.id} · ${m.providerName}` : v));
  else b.append(el("span", "", t("Off")));
  b.disabled = !models.length && !v;
  b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "redactRouteTo", label: "model", value: v, options: [
    { value: "", label: t("Off"), note: t("Masked and sent to the model asked for, as before"), icon: "generic", reset: true },
    ...models.map(modelOpt)],
  onPick: (id) => {
    if (id === v) return;
    prefWrite(api("settings/redact-route", { model: id }));
  } }, b, ev);
  const sub = models.length || v
    ? t("A request masking would change goes as written to this model, which you set to take requests unmasked, so it answers about the real names. If it fails, the request fails: it is never masked and sent elsewhere")
    : t("Only a model you set to take requests unmasked can be picked: in the editor of a provider on this computer or your network, turn on Send requests unmasked");
  const r = el("div", "row pref"), who = el("div", "who"), val = el("div", "val");
  who.append(el("div", "name", t("Instead of masking, send to")), el("div", "sub", sub));
  val.append(b);
  r.append(who, val);
  r.id = "redactRouteRow";
  const after = $("#redactWordsWholeRow");
  if (after) after.after(r); else $("#redactList").append(r);
}
// wordList: Masked words and Never mask as a list, one row a value
// (noahhrreion on Discord: many words were hard to keep in one small
// box). A row is edited in place and saved on Enter or on leaving it;
// emptied, or its ×, takes it out. The box under them adds one, or
// several split at commas as the one box was, so what is saved is the
// same list of strings. A redraw keeps the box being typed in, and what
// it holds.
let wordsRedrawing = false;
const splitWords = (v) => v.split(/[,，\n]/).map((w) => w.trim()).filter(Boolean);
// Import… takes many words at once (acmilan_ on Discord: adding them one
// by one was slow), pasted a word a line or split at commas; a word the
// list has, or one pasted twice, is passed over, and the status says how
// many were added. The box open, and what it holds, outlive a redraw.
// Copy puts the list on the clipboard a word a line, as Import takes it.
let wordImport = null;
function wordList(key, list, ph, label, save, savedNow) {
  const box = el("div", "word-list");
  box.dataset.list = key;
  let cur = list.map((w) => ({ w }));
  const put = (next) => {
    if (next.map((o) => o.w).join("\n") === cur.map((o) => o.w).join("\n")) return;
    cur = next;
    return save(next.map((o) => o.w));
  };
  const keys = (i, done, back) => {
    i.onkeydown = (e) => {
      e.stopPropagation();
      if (e.isComposing || e.keyCode === 229) return;
      if (e.key === "Enter") done(true);
      else if (e.key === "Escape") { i.value = back; i.blur(); }
    };
    // a box a redraw takes away saves nothing: wordFocusBack puts it back
    i.onblur = () => { if (!wordsRedrawing && i.isConnected) done(false); };
  };
  for (const o of cur) {
    const line = el("div", "word-line"), i = input(o.w, "");
    i.className = "words";
    i.dataset.word = o.w;
    i.setAttribute("aria-label", label);
    keys(i, (enter) => {
      if (enter) return i.blur();
      if (i.value.trim() === o.w) return;
      const n = cur.indexOf(o);
      if (n >= 0) put([...cur.slice(0, n), ...splitWords(i.value).map((w) => ({ w })), ...cur.slice(n + 1)]);
    }, o.w);
    const x = el("button", "text danger word-x", "×");
    x.type = "button";
    x.title = t("Remove {word}", { word: o.w });
    x.setAttribute("aria-label", x.title);
    // pressed, the box being edited keeps its focus and its words
    x.onmousedown = (e) => e.preventDefault();
    x.onclick = () => put(cur.filter((c) => c !== o));
    line.append(i, x);
    box.append(line);
  }
  const line = el("div", "word-line word-new"), add = input("", ph);
  add.className = "words word-add";
  add.setAttribute("aria-label", label);
  const go = el("button", "text", t("Add"));
  go.type = "button";
  const take = () => {
    const v = splitWords(add.value);
    add.value = "";
    if (v.length) put([...cur, ...v.map((w) => ({ w }))]);
  };
  keys(add, (enter) => { if (enter || add.value.trim()) take(); }, "");
  go.onmousedown = (e) => e.preventDefault();
  go.onclick = () => { take(); add.focus(NO_SCROLL); };
  line.append(add, go);
  box.append(line);

  const imp = el("div", "word-import");
  imp.hidden = wordImport?.key !== key;
  const area = el("textarea", "word-import-in");
  area.rows = 5;
  area.spellcheck = false;
  area.value = wordImport?.key === key ? wordImport.text : "";
  area.placeholder = t("Paste words, one a line or separated by commas");
  area.setAttribute("aria-label", t("Words to import"));
  const count = el("span", "hint word-import-count");
  const bring = el("button", "text primary", t("Import"));
  bring.type = "button";
  const fresh = () => {
    const have = new Set(cur.map((o) => o.w));
    return [...new Set(splitWords(area.value))].filter((w) => !have.has(w));
  };
  const counted = () => {
    const n = fresh().length;
    count.textContent = area.value.trim() ? (n ? t("New words: {n}", { n }) : t("No new words: each is in the list already")) : "";
    bring.disabled = !n;
  };
  const close = () => { wordImport = null; area.value = ""; imp.hidden = true; counted(); };
  area.oninput = () => { wordImport = { key, text: area.value }; counted(); };
  const bringIn = async () => {
    const pasted = new Set(splitWords(area.value)), add = fresh();
    if (!add.length) return;
    close();
    await put([...cur, ...add.map((w) => ({ w }))]);
    // said only once the words are saved: a failed save has said why
    const now = new Set(savedNow?.() || []);
    if (!add.every((w) => now.has(w))) return;
    const had = pasted.size - add.length;
    status(had ? t("Words added: {n} · already in the list: {m}", { n: add.length, m: had }) : t("Words added: {n}", { n: add.length }), "ok");
  };
  area.onkeydown = (e) => {
    e.stopPropagation();
    if (e.isComposing || e.keyCode === 229) return;
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); bringIn(); }
    else if (e.key === "Escape") close();
  };
  bring.onclick = bringIn;
  const no = el("button", "text", t("Cancel"));
  no.type = "button";
  no.onclick = close;
  const bar = el("div", "word-import-bar");
  bar.append(count, el("span", "grow"), no, bring);
  imp.append(area, bar);
  counted();
  box.append(imp);

  const open = el("button", "text word-import-open", t("Import…"));
  open.type = "button";
  open.title = t("Add many words at once, pasted one a line or separated by commas");
  open.onclick = () => {
    if (!imp.hidden) return void close();
    wordImport = { key, text: "" };
    imp.hidden = false;
    area.focus(NO_SCROLL);
  };
  const out = el("button", "text word-copy", t("Copy"));
  out.type = "button";
  out.title = t("Copy the list, one word a line");
  out.disabled = !cur.length;
  out.onclick = () => copy(cur.map((o) => o.w).join("\n"), "", out, t("Words copied: {n}", { n: cur.length }));
  box.tools = [open, out];
  return box;
}
// wordRow: the row a wordList sits under, its words above and the list
// across the row's width
function wordRow(name, sub, list, ...tools) {
  const r = el("div", "row pref word-rows"), who = el("div", "who"), val = el("div", "val");
  who.append(el("div", "name", name), el("div", "sub", sub));
  val.append(...tools);
  r.append(who, val, list);
  return r;
}
// the box typed in before a redraw, found again after it
function wordFocusOf() {
  const i = document.activeElement;
  const box = i?.closest?.(".word-list");
  if (!box || i.tagName !== "INPUT") return null;
  return { key: box.dataset.list, word: i.dataset.word, value: i.value, a: i.selectionStart, b: i.selectionEnd };
}
function wordFocusBack(f) {
  if (!f) return;
  const box = document.querySelector(`.word-list[data-list="${f.key}"]`);
  const i = [...(box?.querySelectorAll("input.words") || [])].find((i) => i.dataset.word === f.word);
  if (!i) return;
  i.value = f.value;
  i.focus(NO_SCROLL);
  try { i.setSelectionRange(f.a, f.b); } catch {}
}
// redactWordsHidden: Masked words drawn as dots until Show, as a key is
// (noahhrreion on Discord: a screen share showed the words kept from
// vendors). Still text boxes, so an IME writes 百度; password boxes only
// where the engine has no text security. Show holds until the window
// reloads.
let redactWordsShown = false;
function redactWordsHidden(r) {
  const dots = CSS.supports("-webkit-text-security", "disc");
  const eye = el("button", "text redact-eye");
  eye.type = "button";
  const set = () => {
    for (const i of r.querySelectorAll("input.words, textarea.word-import-in")) {
      if (i.tagName === "TEXTAREA") { if (dots) i.classList.toggle("words-hidden", !redactWordsShown); continue; }
      if (dots) i.classList.toggle("words-hidden", !redactWordsShown);
      else i.type = redactWordsShown ? "text" : "password";
    }
    eye.textContent = t(redactWordsShown ? "Hide" : "Show");
    eye.setAttribute("aria-pressed", String(redactWordsShown));
  };
  eye.onclick = () => { redactWordsShown = !redactWordsShown; set(); };
  set();
  r.querySelector(".val").append(eye);
}
{
  const draw = renderRedact;
  renderRedact = function (s, keep) {
    const f = wordFocusOf();
    wordsRedrawing = true;
    try { draw(s, keep); } finally { wordsRedrawing = false; }
    const wl = wordList("words", s.redactWords || [], t("names, codenames, hosts"), t("Masked words"), (redactWords) => savePrefs({ ...keep, redactWords }), () => prefs?.redactWords);
    const words = wordRow(t("Masked words"), t("Your own words to keep from vendors, one on each row"), wl, ...wl.tools);
    const at = $("#redactWordsAnyCaseRow");
    if (at) at.before(words); else $("#redactList").append(words);
    redactWordsHidden(words);
    const acts = $("#maskActList");
    // the Masking alert (Must2333 on X) first, with what else happens when
    // a value is found: it was on the Masking page, away from every other
    // masking setting (the scattered settings audit after netube99 on X),
    // and in Settings › Usage before that (nb5p). Gateway mode leaves it
    // out, as it does the other alerts its desktop would show
    if (acts && !gatewayMode) {
      const r = el("div", "row pref");
      r.id = "maskAlertRow";
      const who = el("div", "who");
      who.append(el("div", "name", t("Masking alert")), el("div", "sub wraps", t("A notification when masking swaps a value for the first time: its kind and how many, never the value itself")));
      const v = el("div", "val");
      v.id = "maskAlertSegs";
      v.append(segs([["off", t("Off")], ["on", t("On")]], s.maskAlert ? "on" : "off", (x) => savePrefs({ ...keep, maskAlert: x === "on" })));
      r.append(who, v);
      acts.prepend(r);
    }
    if (acts) {
      const al = wordList("allow", s.redactAllow || [], t("ops@company.com, build-01"), t("Never mask"), (allow) => writingPrefs(api("settings/redact-allow", { allow }))
        .then((ns) => { prefs = ns; renderSettings(); status(t("Saved"), "ok", 1500); })
        .catch(settingsErr), () => prefs?.redactAllow);
      acts.append(wordRow(t("Never mask"), t("Values that always go as written, each whole and in any case, one on each row: a team address, a test key"), al, ...al.tools));
    }
    wordFocusBack(f);
    if (s.redact || s.redactPersonal || (s.redactWords || []).length) redactRouteRow(s);
  };
}

// Plugins › Built-in › Masking's own line (featGo): on, with nothing
// chosen to mask, it says so beside the way to Settings › Privacy, where
// what to mask is chosen (the scattered settings audit after netube99 on X)
featWho.redact = (line, f) => {
  if (!f.on) return;
  // Settings' own read: /api/state's has no masking fields
  const say = () => { const s = prefs; if (s && !(s.redact || s.redactPersonal || s.redactWords?.length)) line.prepend(el("span", "warn", t("Nothing is masked yet")), line.childNodes.length ? " · " : ""); };
  if (prefs) say(); else loadSettings().then(say, () => {});
};
