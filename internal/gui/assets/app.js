// magpie — one state object per view, rendered into a list. No framework.
const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);
const params = new URLSearchParams(location.search);
const mode = params.get("mode") || "window";
document.body.classList.add(mode);
// The Mac window draws its title bar inside the page (the traffic lights);
// on Linux the page's header is the whole title bar (plainTitlebar), so it
// has the name, the close button and a double-click to maximise.
if (/^Mac/.test(navigator.platform)) document.body.classList.add("mac");
if (/^Linux/.test(navigator.platform)) document.body.classList.add("linux");
// The window is dragged by its header, and only where the header says so
// (--wails-draggable), so the tabs and buttons in it stay plain clicks.
// Outside the app — a browser on the gateway's page — there is no runtime.
const winRuntime = mode === "window" ? import("/wails/runtime.js").catch(() => null) : Promise.resolve(null);
if (params.get("theme")) document.documentElement.dataset.theme = params.get("theme");
// the saved language and theme from boot.js, so the first paint is in them
if (window.bootPrefs) {
  const b = window.bootPrefs;
  if (!params.get("theme") && b.theme && b.theme !== "system") document.documentElement.dataset.theme = b.theme;
  setLocale(b.lang);
}

let state = { agents: [], profiles: [], catalog: "", settings: {} };
let prefs = null; // the settings page: theme, lang, version, dir, gateway
let providers = null; // { providers, presets, gateway }
let view = "agents";
let showAllAgents = false; // the agents no one has set anything on, folded away
let period = "30d"; // usage window
let usage = null;   // last usage summary
let pick = null; // { agent, field, options, items, cursor, anchor }
let editing = null; // provider id being edited; { preset } or { custom: true } for a new one
let draft = null; // the editor's working copy
let naming = null; // the provider whose models' names and levels are open in its editor
let adding = false; // the preset sheet is open
let importing = null; // a magpie://import link waiting for a yes: { provider, error, replaces }
let importingApps = null; // the Import from other apps dialog: { sources, picks }
// the gateway tab's choices, kept per machine
let flavor = params.get("flavor") || localStorage.getItem("magpie.flavor") || "openai"; // which API the snippets speak
let lang = params.get("lang") || localStorage.getItem("magpie.lang") || "shell";        // which snippet
let exampleModel = localStorage.getItem("magpie.model") || "";  // the model in the snippets
const expandedCalls = new Set(); // recent-call ids whose wire bodies are open
let savedModelFavorites = [];
try { savedModelFavorites = JSON.parse(localStorage.getItem("magpie.modelFavorites") || "[]"); } catch {}
const modelFavorites = new Set(Array.isArray(savedModelFavorites) ? savedModelFavorites : []);
// A model through magpie is starred as its catalog id, which is the same in
// every agent; the agent's own models by their value. Stars kept by value
// before still count.
const favoriteKey = (o) => o.ref || o.value;
const isFavorite = (o) => modelFavorites.has(favoriteKey(o)) || modelFavorites.has(o.value);

async function api(path, body) {
  const res = await fetch("/api/" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return null;
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}
function svg(d, size = 12, stroke = 1.6) {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 16 16");
  s.setAttribute("width", size);
  s.setAttribute("height", size);
  s.innerHTML = `<path d="${d}" fill="none" stroke="currentColor" stroke-width="${stroke}" stroke-linecap="round" stroke-linejoin="round"/>`;
  return s;
}
const CHEV = "m5.5 6.5 2.5 2.5 2.5-2.5";
const CHEV_R = "m6.5 4.5 3 3.5-3 3.5";
const CHECK = "m3.5 8.5 3 3 6-7";
const PLUS = "M8 3.5v9M3.5 8h9";
const COPY_ICON = "M5.5 5.5V3.5h7v7h-2M3.5 5.5h7v7h-7z";

// A brand icon: colour logos are images, mono logos take the text colour.
// Nothing is ever invented: a model with no known vendor keeps the slot
// empty, and a custom provider shows a plain outline ("generic").
function icon(name) {
  const e = el("span", "ic");
  if (name === "generic") {
    e.classList.add("generic");
    e.append(svg("M8 2.2 13.2 5.1v5.8L8 13.8 2.8 10.9V5.1Z M8 8v5.8 M2.8 5.1 8 8l5.2-2.9", 16, 1.4));
    return e;
  }
  if (name?.startsWith("file:")) {
    // a picture the user gave their own provider
    const img = el("img");
    img.src = "/api/icons/" + encodeURIComponent(name.slice(5));
    img.alt = "";
    img.draggable = false;
    img.onerror = () => e.replaceWith(icon("generic"));
    e.append(img);
    return e;
  }
  if (name) {
    if (name.endsWith("-color") || name === "crush" || name === "zcode" || name === "alma" || name === "typesafe") {
      const img = el("img");
      img.src = `icons/${name}.${name === "crush" || name === "zcode" || name === "alma" || name === "typesafe" ? "png" : "svg"}`;
      img.alt = "";
      img.draggable = false;
      e.append(img);
    } else {
      const m = el("span", "mask");
      m.style.setProperty("--i", `url(icons/${name}.svg)`);
      e.append(m);
    }
    return e;
  }
  e.classList.add("blank");
  return e;
}

function status(msg, kind = "", ms = 3500) {
  const s = $("#status");
  s.textContent = msg;
  s.title = msg;
  s.className = "status " + kind;
  clearTimeout(status.t);
  if (msg) status.t = setTimeout(() => { s.textContent = ""; s.className = "status"; }, ms);
}

// ---------- agents view ----------

function optionFor(field, value) {
  return field.options.find((o) => o.value === value);
}

// Rows in the shape of the list while magpie first reads the agents; a
// reload keeps the rows it has until the new ones are in.
function renderAgentsLoading() {
  const page = $("#view-agents");
  page.classList.add("loading");
  page.setAttribute("aria-busy", "true");
  const list = $("#agents");
  list.replaceChildren();
  for (let i = 0; i < 5; i++) {
    const row = el("div", "row agent ag-sk-row");
    const who = el("div", "who");
    who.append(el("span", "skeleton ag-sk-name"));
    const fields = el("div", "fields");
    fields.append(el("span", "skeleton ag-sk-field"), el("span", "skeleton ag-sk-field"));
    row.append(el("span", "skeleton ag-sk-icon"), who, fields);
    list.append(row);
  }
}

function renderAgents() {
  const page = $("#view-agents");
  page.classList.remove("loading");
  page.removeAttribute("aria-busy");
  const list = $("#agents");
  list.replaceChildren();
  if (!state.agents.length) {
    const e = el("div", "empty-state");
    e.append(el("b", "", t("No agents found")), el("span", "", t("Install Claude Code, Codex, Gemini CLI, OpenCode… and magpie will list them here.")));
    list.append(e);
  }
  const { shown: used, folded } = arrangeAgents();
  const agentRow = (a, inFold) => {
    const row = el("div", "row agent");
    row.dataset.id = a.id;
    row.title = a.path;
    row.oncontextmenu = (ev) => { ev.preventDefault(); openAgentMenu(row.querySelector(".ag-handle"), a, inFold); };
    const who = el("div", "who");
    who.append(el("div", "name", a.name));
    // the model picker takes the wide column, everything else the narrow one,
    // so the controls line up down the list
    const fields = el("div", "fields");
    const wide = (f) => f.label === "model" || f.label === "large";
    const shownFields = a.fields.filter((f) => !TIERS.includes(f.label));
    const tiers = tierMenu(a);
    if (tiers) shownFields.push(tiers);
    const sorted = shownFields.sort((x, y) => wide(y) - wide(x) || extra(x) - extra(y));
    const plain = sorted.filter((f) => !extra(f)).length;
    for (const f of sorted) {
      if (extra(f)) { fields.append(extraField(a, f)); continue; }
      const b = el("button", "field " + (plain === 1 ? "solo" : wide(f) ? "main" : "side"));
      const opt = optionFor(f, f.value);
      b.title = t("{label}: {value}", { label: t(f.label), value: f.value || t("agent default") }) + (opt?.note ? ` · ${opt.note}` : "");
      const effort = f.key === "effort" || f.label === "effort" || f.label === "thinking";
      if (opt?.icon || opt?.icons?.length) b.append(optionIcon(opt));
      // a field with no logo of its own still leads with an icon: how much
      // effort, or the agent's own for its default, as the picker shows it
      else if (effort) b.append(effortIcon(f));
      else if (!f.value && !f.menu && a.icon) b.append(icon(a.icon));
      else if (!wide(f) || !f.value) b.append(el("span", "k", t(f.label)));
      const shown = f.menu ? f.summary : effort ? effortName(opt || { value: f.value }) : (opt?.label || f.value || t(FOLLOWS_MODEL.includes(f.label) ? "same as model" : "default"));
      if (f.menu) b.title = f.options.map((o) => `${o.label}: ${o.note}`).join("\n");
      b.append(el("span", "v" + (f.value || f.custom ? "" : " empty"), shown));
      const c = el("span", "chev");
      c.append(svg(CHEV, 11, 1.7));
      b.append(c);
      b.dataset.key = f.key;
      b.onclick = (ev) => openPicker(a, f, b, ev);
      fields.append(b);
    }
    // one hidden by hand gives its way back in words, rather than being a
    // greyed row whose way back is its menu. One nothing is set on isn't
    // hidden: setting something on it brings it up the list.
    if (inFold && isHidden(a)) {
      row.classList.add("put-away");
      const back = el("button", "ag-show");
      back.type = "button";
      back.title = t("Hidden by you · show it in the list again");
      back.append(svg(EYE, 12, 1.5), el("span", "", t("Show")));
      back.onclick = (e) => { e.stopPropagation(); setAgentHidden(a, false); };
      who.append(back);
    }
    // on the name's own line, so the row keeps its height and the pickers
    // their columns
    if (a.drift) {
      row.classList.add("drifted");
      who.append(driftFix(a));
    }
    row.append(agentHandle(a, row, inFold), who, fields);
    return row;
  };
  // the extras column is there for every row once any agent has one, so the
  // pickers keep lining up down the list
  list.classList.toggle("extras", state.agents.some((a) => a.fields.some(extra) || tierMenu(a)));
  if (!folded.length) {
    for (const a of used) list.append(agentRow(a));
  } else {
    // the ones in use stay put; the rest unroll beneath them like a scroll
    for (const a of used) list.append(agentRow(a));
    const fold = el("div", "agent-fold" + (showAllAgents ? " open" : ""));
    const inner = el("div", "agent-fold-inner");
    inner.inert = !showAllAgents;
    fold.style.setProperty("--n", folded.length);
    // the ones hidden by hand, then the ones nothing is set on, each under
    // a line that says which they are
    const byHand = folded.filter(isHidden), unset = folded.filter((a) => !isHidden(a));
    let i = 0;
    for (const [group, cap] of [[byHand, "Hidden"], [unset, "Not set up"]]) {
      if (!group.length) continue;
      const c = el("div", "agent-fold-cap", t(cap));
      c.style.setProperty("--i", i);
      inner.append(c);
      for (const a of group) {
        const row = agentRow(a, true);
        row.style.setProperty("--i", i++);
        inner.append(row);
      }
    }
    fold.append(inner);
    const more = el("button", "agent-more");
    const label = el("span", "", "");
    const chev = el("span", "chev");
    chev.append(svg(CHEV, 10, 1.8));
    more.append(label, chev);
    const labelFor = () => {
      // what is folded, and how many of them were hidden by hand
      label.textContent = showAllAgents ? t("Show less")
        : !unset.length ? t("{n} hidden agents", { n: byHand.length })
        : byHand.length ? t("Show {n} more ({h} hidden)", { n: folded.length, h: byHand.length })
        : t("Show {n} more", { n: folded.length });
      more.setAttribute("aria-expanded", String(showAllAgents));
    };
    labelFor();
    // settled: the soft edge goes, and a folded scroll gives the panel its room
    // back. A hidden window never ends its transition, so a timer backs it up.
    let settle;
    const settled = () => {
      clearTimeout(settle);
      fold.classList.remove("moving");
      fit();
    };
    fold.addEventListener("transitionend", (e) => { if (e.target === fold) settled(); });
    more.onclick = () => {
      showAllAgents = !showAllAgents;
      // the panel's edge moves with the scroll, on the same beat and curve
      const room = inner.scrollHeight;
      fit(showAllAgents ? room : -room, showAllAgents ? UNROLL : ROLLUP);
      clearTimeout(settle);
      settle = setTimeout(settled, 900);
      fold.classList.add("moving");
      fold.classList.toggle("open", showAllAgents);
      inner.inert = !showAllAgents;
      labelFor();
      if (!matchMedia("(prefers-reduced-motion: reduce)").matches) {
        label.animate([{ opacity: 0, transform: "translateY(3px)" }, { opacity: 1, transform: "none" }], { duration: 260, easing: "cubic-bezier(.22, 1, .36, 1)" });
      }
    };
    list.append(fold, more);
  }

  const chips = $("#profiles");
  chips.replaceChildren();
  if (!state.profiles.length) chips.append(el("span", "hint", t("none yet · save the setup to switch back in one click")));
  for (const p of state.profiles) {
    const c = el("button", "chip");
    const lib = profileLibrary(p.library);
    c.title = [p.summary, lib].filter(Boolean).join("\n");
    c.append(el("span", "", p.name));
    if (lib) c.append(el("span", "lib"));
    // the setup as it is now, saved over this profile
    const u = el("span", "x", "↻");
    u.title = t("Update to the current setup");
    u.onclick = (ev) => { ev.stopPropagation(); profileAction("save", p.name, true); };
    const x = el("span", "x", "×");
    x.title = t("Delete profile");
    x.onclick = (ev) => { ev.stopPropagation(); profileAction("delete", p.name); };
    c.append(u, x);
    c.onclick = () => profileAction("use", p.name);
    chips.append(c);
  }
  fit(0, agentsGlide);
  agentsGlide = null;
}

// driftNote: under the name of an agent whose config something else
// rewrote since magpie set it — the row still shows a magpie model while the
// agent no longer reaches magpie, or it was put back on a model of its own —
// what happened, and the one click that sets it again.
// driftFix is the one thing a drifted agent shows: an amber pill after its
// name that sets magpie's settings again. What is off is its tooltip; taking
// the config as it is now is in the row's menu.
function driftFix(a) {
  const d = a.drift, f = a.fields.find((x) => x.key === d.field);
  const want = (f && optionFor(f, d.want)?.label) || d.want;
  const fix = el("button", "ag-fix");
  fix.type = "button";
  fix.title = `${t(DRIFT_WHY[d.kind] || DRIFT_WHY.unwired, { agent: a.name, model: want })}\n${d.detail}`;
  fix.setAttribute("aria-label", t("Apply again"));
  fix.append(svg(REAPPLY, 11, 1.8), el("span", "", t("Apply again")));
  fix.onclick = (e) => { e.stopPropagation(); reapplyAgent(a, fix); };
  return fix;
}

const DRIFT_WHY = {
  unwired: "{agent} no longer goes through magpie — its config was changed",
  replaced: "{agent} was switched off {model} outside magpie",
  bypassed: "{agent} was used without going through magpie — restart it after applying",
};

const REAPPLY = "M13.5 8a5.5 5.5 0 1 1-1.6-3.9M13.5 2.5v3.25h-3.25";

async function keepAgent(a) {
  try { state = await api("agents/keep/" + a.id, {}); renderAgents(); } catch (e) { status(e.message, "err"); }
}

// reapplyAgent writes what magpie set on the agent into its config again.
async function reapplyAgent(a, btn) {
  btn?.classList.add("busy");
  try {
    state = await api("agents/reapply/" + a.id, {});
    renderAgents();
    document.querySelector(`.agent[data-id="${CSS.escape(a.id)}"] .field`)?.classList.add("flash");
    const msg = t("{agent} goes through magpie again", { agent: a.name });
    if (state.notice) status(`${msg}. ${state.notice}`, "warn", 9000);
    else status(msg, "ok");
  } catch (e) {
    btn?.classList.remove("busy");
    status(e.message, "err");
  }
}

// ---------- the agents' order, and the ones put away ----------
// Kept in magpie's own settings (agentOrder, agentsHidden, agentsShown),
// never in an agent's files. An agent the order doesn't name — one
// installed since — follows the ordered ones, in magpie's own order.

let agentsGlide = null; // how the panel's edge moves after the next render

const agentUsed = (a) => a.fields.some((f) => f.value);
const isHidden = (a) => (state.settings?.agentsHidden || []).includes(a.id);

// arrangeAgents: the rows in view, in order, and the folded rest. Folded is
// what was hidden by hand, and what nothing is set on — noise in a picker —
// unless it was shown by hand or nothing is set on any (a fresh magpie has
// nothing to show otherwise).
function arrangeAgents() {
  const s = state.settings || {};
  const order = s.agentOrder || [], hidden = new Set(s.agentsHidden || []);
  const rank = (a) => { const i = order.indexOf(a.id); return i < 0 ? order.length : i; };
  const all = state.agents.map((a, i) => [a, i]).sort(([x, i], [y, j]) => rank(x) - rank(y) || i - j).map(([a]) => a);
  const anyUsed = all.some((a) => !hidden.has(a.id) && agentUsed(a));
  // what is set on it alone decides where one not hidden goes: pinned in view
  // by hand, one cleared stayed up among the set ones with nothing to say why
  const inView = (a) => !hidden.has(a.id) && (!anyUsed || agentUsed(a));
  return { all, shown: all.filter(inView), folded: all.filter((a) => !inView(a)) };
}

async function saveArrangement(order, hidden, shown) {
  const prev = state.settings;
  state.settings = { ...prev, agentOrder: order, agentsHidden: hidden, agentsShown: shown };
  renderAgents();
  try {
    const s = await api("agents/arrange", { order, hidden, shown });
    state.settings = { ...state.settings, agentOrder: s.agentOrder || [], agentsHidden: s.agentsHidden || [], agentsShown: s.agentsShown || [] };
  } catch (e) {
    state.settings = prev;
    renderAgents();
    status(e.message, "err");
  }
}

// moveAgent puts the agent at index `to` among the rows in view; the folded
// ones keep their places after them.
function moveAgent(id, to) {
  const { shown, folded } = arrangeAgents();
  const ids = shown.map((a) => a.id);
  const from = ids.indexOf(id);
  if (from < 0 || to < 0 || to >= ids.length || to === from) return;
  ids.splice(to, 0, ...ids.splice(from, 1));
  const s = state.settings || {};
  saveArrangement([...ids, ...folded.map((a) => a.id)], s.agentsHidden || [], []);
}

function setAgentHidden(a, hide) {
  const s = state.settings || {};
  const { all } = arrangeAgents();
  let hidden = (s.agentsHidden || []).filter((x) => x !== a.id);
  if (hide) hidden.push(a.id);
  // the rows that change go on the panel's edge, as the fold does
  agentsGlide = hide ? ROLLUP : UNROLL;
  saveArrangement(all.map((x) => x.id), hidden, []);
  // hidden, it says where it went, since the row goes out of sight
  status(t(hide ? "{agent} hidden · find it under Hidden at the bottom" : "{agent} shown", { agent: a.name }), "ok", hide ? 4000 : 1800);
}

const ALT = /^Mac/.test(navigator.platform) ? "⌥" : "Alt+";
const GRIP = "M6 4h.01M10 4h.01M6 8h.01M10 8h.01M6 12h.01M10 12h.01";
const EYE_OFF = "M6.6 3.7A6.9 6.9 0 0 1 8 3.5c3.75 0 6.25 4.5 6.25 4.5a11 11 0 0 1-1.5 2M4.4 4.4C2.7 5.55 1.75 8 1.75 8S4.25 12.5 8 12.5c1.2 0 2.25-.45 3.1-1.05M6.75 6.75a1.75 1.75 0 0 0 2.5 2.5M2 2l12 12";
const EYE = "M1.75 8S4.25 3.5 8 3.5 14.25 8 14.25 8 11.75 12.5 8 12.5 1.75 8 1.75 8ZM8 9.75a1.75 1.75 0 1 0 0-3.5 1.75 1.75 0 0 0 0 3.5Z";

// agentHandle is the row's logo, which is also its handle: drag it to move
// the row, click it (or right-click the row) for Move up, Move down and
// Hide; Alt+↑/↓ moves it from the keyboard.
function agentHandle(a, row, inFold) {
  const b = el("button", "ag-handle");
  b.type = "button";
  b.setAttribute("aria-label", t("Arrange {agent}", { agent: a.name }));
  b.setAttribute("aria-haspopup", "menu");
  b.title = inFold ? t(isHidden(a) ? "Show {agent}" : "Hide {agent}", { agent: a.name }) : t("Drag to reorder · click to move or hide");
  const grip = el("span", "grip");
  grip.append(svg(GRIP, 14, 2.4));
  b.append(icon(a.icon), grip);
  b.onkeydown = (e) => {
    if (inFold || !e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
    e.preventDefault();
    const { shown } = arrangeAgents();
    const i = shown.findIndex((x) => x.id === a.id);
    moveAgent(a.id, i + (e.key === "ArrowUp" ? -1 : 1));
    // the rows were drawn anew: keep the keyboard on this one
    $(`#agents .row.agent[data-id="${CSS.escape(a.id)}"] .ag-handle`)?.focus();
  };
  b.onclick = (e) => { if (!b.dataset.dragged) openAgentMenu(b, a, inFold); delete b.dataset.dragged; };
  if (!inFold) b.onpointerdown = (e) => dragAgent(e, b, row);
  return b;
}

// dragAgent moves a row in view up and down the list with the pointer; the
// others make room as it passes, and letting go keeps the new order.
function dragAgent(e, handle, row) {
  if (e.button !== 0) return;
  const list = $("#agents");
  const rows = [...list.children].filter((r) => r.classList.contains("agent"));
  if (rows.length < 2) return;
  const y0 = e.clientY, from = rows.indexOf(row);
  const tops = rows.map((r) => r.offsetTop), h = row.offsetHeight;
  let dragging = false, to = from;
  const move = (ev) => {
    const dy = ev.clientY - y0;
    if (!dragging) {
      if (Math.abs(dy) < 4) return;
      dragging = true;
      handle.dataset.dragged = "1";
      closeAgentMenu();
      list.classList.add("sorting");
      row.classList.add("dragging");
    }
    // the row follows the pointer, kept within the list
    const min = tops[0] - tops[from], max = tops[rows.length - 1] + rows[rows.length - 1].offsetHeight - h - tops[from];
    const d = Math.max(min, Math.min(max, dy));
    row.style.transform = `translateY(${d}px)`;
    const mid = tops[from] + d + h / 2;
    // past the middle of a row below (or above), the dragged one takes its place
    if (d > 0) to = rows.slice(from + 1).filter((r, k) => mid >= tops[from + 1 + k] + r.offsetHeight / 2).length + from;
    else to = from - rows.slice(0, from).filter((r, k) => mid <= tops[k] + r.offsetHeight / 2).length;
    rows.forEach((r, i) => {
      if (i === from) return;
      const shift = i > from && i <= to ? -h : i < from && i >= to ? h : 0;
      r.style.transform = shift ? `translateY(${shift}px)` : "";
    });
  };
  const up = () => {
    handle.removeEventListener("pointermove", move);
    handle.removeEventListener("pointerup", up);
    handle.removeEventListener("pointercancel", up);
    if (!dragging) return;
    // the row lands where it was let go, then the list is drawn in the new order
    row.classList.remove("dragging");
    row.classList.add("landing");
    row.style.transform = `translateY(${tops[to] - tops[from] + (to > from ? rows[to].offsetHeight - h : 0)}px)`;
    setTimeout(() => {
      list.classList.remove("sorting");
      moveAgent(row.dataset.id, to);
      if (to === from) renderAgents();
      setTimeout(() => delete handle.dataset.dragged, 0);
    }, 160);
  };
  handle.setPointerCapture(e.pointerId);
  handle.addEventListener("pointermove", move);
  handle.addEventListener("pointerup", up);
  handle.addEventListener("pointercancel", up);
}

let agentMenu = null;
function closeAgentMenu() {
  if (!agentMenu) return;
  agentMenu.anchor.classList.remove("open");
  agentMenu.box.remove();
  document.removeEventListener("mousedown", agentMenu.outside, true);
  document.removeEventListener("keydown", agentMenu.keys, true);
  document.removeEventListener("scroll", closeAgentMenu, true);
  removeEventListener("resize", closeAgentMenu);
  agentMenu = null;
}
function openAgentMenu(anchor, a, inFold) {
  if (!anchor) return;
  const again = agentMenu?.anchor === anchor;
  closeAgentMenu();
  if (again) return;
  const { shown } = arrangeAgents();
  const i = shown.findIndex((x) => x.id === a.id);
  const acts = inFold && isHidden(a)
    ? [{ name: "Show", icon: EYE, run: () => setAgentHidden(a, false) }]
    : inFold
    ? [{ name: "Hide", icon: EYE_OFF, run: () => setAgentHidden(a, true) }]
    : [
        { name: "Move up", icon: "M8 12.5v-9M4 7.25l4-3.75 4 3.75", key: ALT + "↑", off: i <= 0, run: () => moveAgent(a.id, i - 1) },
        { name: "Move down", icon: "M8 3.5v9M4 8.75l4 3.75 4-3.75", key: ALT + "↓", off: i < 0 || i >= shown.length - 1, run: () => moveAgent(a.id, i + 1) },
        // for a config rewritten in a way magpie can't see: set it again anyway
        ...(a.drift || a.fields.some((f) => optionFor(f, f.value)?.ref) ? [{ name: "Apply again", icon: REAPPLY, sep: true, run: () => reapplyAgent(a) }] : []),
        ...(a.drift?.kind === "replaced" ? [{ name: "Keep current settings", icon: CHECK, run: () => keepAgent(a) }] : []),
        { name: "Hide", icon: EYE_OFF, sep: true, run: () => setAgentHidden(a, true) },
      ];
  const box = el("div", "pop row-menu");
  box.setAttribute("role", "menu");
  const items = [];
  for (const o of acts) {
    if (o.sep) box.append(el("div", "rm-sep"));
    const b = el("button", "rm-item");
    b.type = "button";
    b.setAttribute("role", "menuitem");
    b.disabled = !!o.off;
    b.append(svg(o.icon, 13, 1.5), el("span", "rm-name", t(o.name)));
    if (o.key) b.append(el("span", "rm-key", o.key));
    b.onclick = (e) => { e.stopPropagation(); closeAgentMenu(); o.run(); };
    b.onmouseenter = () => b.focus({ preventScroll: true });
    box.append(b);
    if (!o.off) items.push(b);
  }
  document.body.append(box);
  const r = anchor.getBoundingClientRect(), w = box.offsetWidth, hh = box.offsetHeight, pad = 8;
  let y = r.bottom + 5;
  if (y + hh > innerHeight - pad && r.top - 5 - hh >= pad) { y = r.top - 5 - hh; box.classList.add("up"); }
  box.style.left = Math.max(pad, Math.min(r.left - 4, innerWidth - w - pad)) + "px";
  box.style.top = Math.max(pad, y) + "px";
  anchor.classList.add("open");
  const outside = (e) => { if (!box.contains(e.target) && !anchor.contains(e.target)) closeAgentMenu(); };
  const keys = (e) => {
    const k = items.indexOf(document.activeElement);
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closeAgentMenu(); anchor.focus(); }
    else if (e.key === "Tab") closeAgentMenu();
    else if ((e.key === "ArrowDown" || e.key === "ArrowUp") && items.length) {
      e.preventDefault(); e.stopPropagation();
      const n = items.length, at = k < 0 ? (e.key === "ArrowDown" ? n - 1 : 0) : k;
      items[(at + (e.key === "ArrowDown" ? 1 : n - 1)) % n].focus();
    }
  };
  document.addEventListener("mousedown", outside, true);
  document.addEventListener("keydown", keys, true);
  document.addEventListener("scroll", closeAgentMenu, true);
  addEventListener("resize", closeAgentMenu);
  agentMenu = { box, anchor, outside, keys };
  items[0]?.focus({ preventScroll: true });
}

// Claude Code's opus/sonnet/haiku/fable can each have a model of their own
// once it runs through magpie. They share one button, which lists the four;
// picking one opens the model picker for it.
const TIERS = ["opus", "sonnet", "haiku", "fable"];
// fields that fall back to the agent's model when unset
const FOLLOWS_MODEL = [...TIERS, "subagents"];

// A field that follows the model unless set — Codex's subagents, Claude
// Code's tiers — is a small square after the pickers rather than a third
// picker, which a row has no room for: it wrapped onto a line of its own.
const extra = (f) => f.key === "tiers" || FOLLOWS_MODEL.includes(f.label);
const EXTRA_GLYPH = {
  subagents: "M4.5 2.75v10.5M4.5 9.25c0-2.2 1.6-3.75 3.9-3.75h3.35M9.9 3.6l1.9 1.9-1.9 1.9",
  tiers: "M8 2.6 2.75 5.4 8 8.2l5.25-2.8zM2.75 8.1 8 10.9l5.25-2.8M2.75 10.8 8 13.6l5.25-2.8",
};
function extraField(a, f) {
  const set = !!(f.value || f.custom);
  const b = el("button", "field extra" + (set ? " set" : ""));
  b.append(svg(EXTRA_GLYPH[f.label] || EXTRA_GLYPH.tiers, 13, 1.5));
  const opt = optionFor(f, f.value);
  b.title = f.menu
    ? t("{label}: {value}", { label: t(f.label), value: f.summary }) + "\n" + f.options.map((o) => `${o.label}: ${o.note}`).join("\n")
    : t("{label}: {value}", { label: t(f.label), value: opt?.label || f.value || t("same as model") });
  b.setAttribute("aria-label", b.title);
  b.dataset.key = f.key;
  b.onclick = (ev) => openPicker(a, f, b, ev);
  return b;
}

function tierMenu(a) {
  const tiers = a.fields.filter((f) => TIERS.includes(f.label));
  if (!tiers.length || !tiers.some((f) => f.options.length)) return null;
  const main = a.fields.find((f) => f.key === "model");
  const mainName = optionFor(main, main.value)?.label || main.value;
  const custom = tiers.filter((f) => f.value);
  const name = (f) => optionFor(f, f.value)?.label || f.value;
  return {
    key: "tiers", label: "tiers", value: "", menu: true, custom: custom.length > 0,
    summary: custom.length ? custom.map((f) => f.label).join(", ") : t("same as model"),
    options: tiers.map((f) => ({
      value: f.key, label: f.label, icon: optionFor(f, f.value)?.icon || optionFor(main, main.value)?.icon,
      note: f.value ? name(f) : t("same as model ({model})", { model: mainName }),
    })),
  };
}

// The tray panel has no scrollbars to speak of, so it grows to fit instead.
// The agents' scroll unrolls and rolls up on these, in app.css as in the
// panel's own height.
const UNROLL = { ms: 620, ease: ".22,1,.36,1" };
const ROLLUP = { ms: 420, ease: ".4,0,.2,1" };

// tintPanel hands the colour the panel's page shows to the system, to paint
// under the page, which then leaves its own background clear: the page is
// drawn a frame or two after the panel grows, and what shows at the new edge
// meanwhile is that colour, not a dark band. ms is how long a change of theme
// fades.
async function tintPanel(ms = 0) {
  if (mode !== "panel") return;
  const probe = tintPanel.probe || (tintPanel.probe = document.body.appendChild(el("div", "tint-probe")));
  const c = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
  c.fillStyle = "#010203";
  const none = c.fillStyle;
  c.fillStyle = getComputedStyle(probe).backgroundColor;
  if (c.fillStyle === none) return; // a colour the canvas can't read
  // what the page shows: its thin paint over the webview's white
  const paint = c.fillStyle;
  c.fillStyle = "#fff";
  c.fillRect(0, 0, 1, 1);
  c.fillStyle = paint;
  c.fillRect(0, 0, 1, 1);
  const rgba = [...c.getImageData(0, 0, 1, 1).data].join(",");
  if (rgba === tintPanel.last) return;
  tintPanel.last = rgba;
  try {
    const r = await api("window/tint?c=" + rgba + "&ms=" + ms, {});
    document.body.classList.toggle("tinted", !!r?.ok);
  } catch {
    tintPanel.last = null;
    document.body.classList.remove("tinted");
  }
}
if (mode === "panel") {
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => tintPanel(450));
}

// extra is room about to be taken (or given back), e.g. by agents unrolling;
// glide moves the panel's edge there over time instead of at once.
function fit(extra = 0, glide) {
  if (mode !== "panel") return;
  const h = $(".top").offsetHeight + $("#agents").offsetHeight + $(".profiles").offsetHeight + $(".foot").offsetHeight + 4 + extra;
  if (h === fit.last) return;
  fit.last = h;
  const still = !glide || matchMedia("(prefers-reduced-motion: reduce)").matches;
  api("window/fit?h=" + h + (still ? "" : "&ms=" + glide.ms + "&ease=" + glide.ease), {});
}

async function load() {
  if (!load.done) renderAgentsLoading();
  if (view === "providers" && !providers) renderProvidersLoading();
  try {
    state = await api("state");
    load.done = true;
    // the library may have drawn itself before the saved language was known
    if (applyPrefs(state.settings) && view === "library") window.loadLibrary?.();
    tintPanel();
    renderAgents();
    // an open provider editor is someone typing: coming back to the window
    // must not rebuild it under them
    if ((view === "providers" || view === "gateway") && !(editing || adding)) await loadProviders();
    if (view === "usage") await loadUsage();
    if (view === "settings") await loadSettings();
  } catch (e) {
    status(e.message, "err");
  }
  renderUpdateBadge();
}

// renderUpdateBadge shows the header's Update pill once a newer magpie is
// downloaded (a click restarts into it) or, where magpie can't replace
// itself, out (a click opens the release page).
async function renderUpdateBadge() {
  const b = $("#update"), label = b.querySelector("span");
  const u = await api("update").catch(() => null);
  // pulling: a click is downloading it again, and restarts once it's in
  const pulling = !!u && !!b.dataset.pulling && ["checking", "downloading", "ready"].includes(u.state);
  if (b.dataset.pulling && !pulling) {
    delete b.dataset.pulling;
    b.classList.remove("busy");
  }
  const on = pulling || (!!u && (u.state === "ready" || u.state === "available" || (u.state === "error" && u.retry)));
  if (b.hidden !== !on) b.hidden = !on;
  if (!on) return;
  const restart = async () => {
    b.classList.add("busy");
    label.textContent = t("Restarting…");
    // an answer means it didn't: the password prompt dismissed, the swap
    // failed, or a newer version is out and downloading first
    const a = await api("update/install", {}).catch(() => ({}));
    if (a) {
      if (["checking", "downloading"].includes(a.state)) b.dataset.pulling = "1";
      else b.classList.remove("busy");
      renderUpdateBadge();
    }
  };
  if (pulling) {
    if (u.state === "ready") {
      delete b.dataset.pulling;
      return restart();
    }
    label.textContent = u.total ? t("Downloading… {p}%", { p: Math.floor((u.done / u.total) * 100) }) : t("Downloading…");
    setTimeout(renderUpdateBadge, 700);
    return;
  }
  if (b.classList.contains("busy")) return;
  // a swap that failed says so where it was clicked, not only in the tooltip
  label.textContent = u.state === "ready" && u.error ? t("Update failed") : t("Update");
  b.title = u.state === "ready" ? t("Restart to update to {v}", { v: u.latest })
    : u.state === "error" ? t("Couldn't download {v}", { v: u.latest }) + " · " + t("Click to try again")
    : u.stuck ? updateStuck(u) + " " + t("Click to open the download page.")
    : t("{v} is out", { v: u.latest });
  if (u.error) b.title += "\n" + u.error;
  b.onclick = () => {
    if (u.state === "ready") return restart();
    if (u.state === "error") {
      b.dataset.pulling = "1";
      b.classList.add("busy");
      label.textContent = t("Downloading…");
      return api("update/install", {}).then(renderUpdateBadge, renderUpdateBadge);
    }
    api("update/install", {}).catch(() => {});
  };
}

// updateStuck says why this magpie can't replace itself where it is.
function updateStuck(u) {
  return u.stuck === "translocated"
    ? t("macOS is running magpie from a temporary copy, so it can't update itself; move magpie to Applications and open it from there.")
    : t("magpie is running from its disk image, so it can't update itself; drag it to Applications and open it from there.");
}

// ---------- picker ----------

function score(q, o) {
  if (!q) return 1;
  const lv = o.value.toLowerCase(), ll = (o.label || "").toLowerCase();
  if (lv === q || ll === q) return 100;
  if (lv.startsWith(q) || ll.startsWith(q)) return 60;
  if (lv.includes(q) || ll.includes(q)) return 40;
  let i = 0;
  for (const ch of lv) if (ch === q[i]) i++;
  if (i === q.length) return 20;
  if ((o.note || "").toLowerCase().includes(q) || (o.group || "").toLowerCase().includes(q)) return 10;
  return 0;
}

function placePop(anchor, w, h) {
  const pop = $("#pop");
  const r = anchor.getBoundingClientRect(), pad = 8;
  pop.style.width = w + "px";
  let x = Math.min(r.left, innerWidth - w - pad);
  let y = r.bottom + 5;
  pop.classList.remove("up");
  pop.style.left = Math.max(pad, x) + "px";
  // h is the most it can be: opened upward, its bottom edge is held to the
  // button, so a short list sits on the button rather than h above it
  if (y + h > innerHeight - pad && r.top - 5 - h >= pad) {
    pop.classList.add("up");
    pop.style.top = "auto";
    pop.style.bottom = innerHeight - r.top + 5 + "px";
    return;
  }
  if (y + h > innerHeight - pad) y = Math.max(pad, innerHeight - pad - h);
  pop.style.bottom = "auto";
  pop.style.top = y + "px";
}

// openPicker drops the option list under a field button. `only` narrows the
// options (the providers page offers one vendor's models at a time).
function openPicker(agent, field, anchor, ev, only) {
  ev.stopPropagation();
  closePicker();
  const cur = field.value;
  let options = field.options.filter((o) => !only || only(o));
  // routing groups come first, before the agent's own models and each provider's
  options = [...options.filter((o) => o.group === ROUTING_GROUPS), ...options.filter((o) => o.group !== ROUTING_GROUPS)];
  const effortPicker = !only && (field.key === "effort" || field.label === "effort" || field.label === "thinking");
  // Current model first, then the rest in catalog order. Effort levels keep
  // their natural low → high order because their position is meaningful.
  const i = options.findIndex((o) => o.value === cur);
  if (!effortPicker && !field.menu && i > 0) { const [c] = options.splice(i, 1); options.unshift({ ...c, group: "" }); }
  else if (i < 0 && cur && !only) options.unshift({ value: cur, note: t("current value") });
  // the agent's own default: magpie's wiring comes out and the key is removed
  if (FOLLOWS_MODEL.includes(field.label)) {
    const main = agent.fields.find((f) => f.key === "model");
    options.unshift({ value: "", label: t("Same as model"), note: optionFor(main, main.value)?.label || main.value, icon: optionFor(main, main.value)?.icon, reset: true });
  } else if (!only && !field.menu && !field.onPick) options.unshift({ value: "", label: t("Default"), note: t("what {agent} ships with", { agent: agent.name }), icon: agent.icon, reset: true });
  const modelPicker = ["model", "small", "large", ...FOLLOWS_MODEL].includes(field.label) && !only;
  pick = { agent, field, options, anchor, cursor: 0, free: !only && !field.menu, modelPicker, effortPicker, groupFilter: "all" };
  anchor.classList.add("open");
  const pop = $("#pop");
  pop.classList.toggle("model-picker", modelPicker);
  pop.classList.toggle("effort-picker", effortPicker);
  pop.hidden = false;
  $("#effortControl").hidden = !effortPicker;
  pop.querySelector(".search").hidden = effortPicker;
  pop.querySelector(".picker-body").hidden = effortPicker;
  placePop(anchor, effortPicker ? 232 : modelPicker ? Math.min(490, innerWidth - 16) : (options.some((o) => o.note && o.note !== o.value) ? 372 : 300), effortPicker ? 96 : modelPicker ? Math.min(420, innerHeight - 16) : 340);
  if (effortPicker) {
    renderEffortPicker();
    $("#effortRange").focus();
    return;
  }
  const q = $("#q");
  q.value = "";
  q.placeholder = modelPicker && extra(field) ? t("{field} — filter, or type any model id…", { field: t(field.label) }) : modelPicker ? t("Filter, or type any model id…") : t("Filter {field}…", { field: t(field.label) });
  filter();
  q.focus();
}

function effortName(option) {
  if (!option?.value) return t("default");
  return t(option.label || option.value);
}

// An effort level as four bars filled up to it: none for the default or
// off, all four for the highest the agent offers.
function effortIcon(f) {
  const levels = (f.options || []).filter((o) => o.value && o.value !== "off");
  const at = levels.findIndex((o) => o.value === f.value);
  const lit = at < 0 ? 0 : Math.max(1, Math.round(((at + 1) / levels.length) * 4));
  const e = el("span", "ic effort-ic");
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 16 16");
  s.innerHTML = [4, 7, 10, 13].map((h, i) =>
    `<rect x="${1.25 + i * 3.6}" y="${14.5 - h}" width="2.6" height="${h}" rx="1" fill="currentColor" opacity="${i < lit ? 1 : 0.28}"/>`).join("");
  e.append(s);
  return e;
}

function renderEffortPicker() {
  const range = $("#effortRange");
  const options = pick.options;
  const selected = Math.max(0, options.findIndex((o) => o.value === pick.field.value));
  range.max = String(Math.max(0, options.length - 1));
  range.value = String(selected);
  $("#effortTitle").textContent = t(pick.field.label);
  $("#effortMin").textContent = effortName(options[0]);
  $("#effortMax").textContent = effortName(options[options.length - 1]);
  // a dot at every level, so the stops show before the thumb gets there
  const ticks = $("#effortTicks");
  ticks.replaceChildren(...options.map((_, i) => {
    const dot = el("i");
    dot.style.setProperty("--at", options.length > 1 ? i / (options.length - 1) : 0);
    return dot;
  }));
  const update = () => {
    const i = Number(range.value);
    [...ticks.children].forEach((dot, j) => { dot.classList.toggle("on", j < i); dot.classList.toggle("cur", j === i); });
    $("#effortValue").textContent = effortName(options[i]);
    const fill = `${options.length > 1 ? 100 * i / (options.length - 1) : 0}%`;
    range.style.setProperty("--fill", fill);
    range.closest(".effort-track").style.setProperty("--fill", fill);
    range.setAttribute("aria-valuetext", effortName(options[i]));
  };
  range.oninput = update;
  range.onchange = () => {
    const opened = pick;
    const option = options[Number(range.value)];
    if (!opened || !option || option.value === opened.field.value) return;
    opened.field.value = option.value;
    const value = opened.anchor.querySelector(".v");
    if (value) {
      value.textContent = effortName(option);
      value.classList.toggle("empty", !option.value);
    }
    opened.anchor.querySelector(".effort-ic")?.replaceWith(effortIcon(opened.field));
    // Persist every settled slider value, but keep the compact control open so
    // the user can compare adjacent levels. Queue writes to preserve ordering
    // when keyboard input changes several stops quickly.
    opened.effortSave = (opened.effortSave || Promise.resolve()).then(async () => {
      const next = await api("set", { agent: opened.agent.id, field: opened.field.key, value: option.value });
      state = next;
      status(`${opened.agent.name} ${t(opened.field.label)} → ${effortName(option)}`, "ok");
    }).catch((e) => status(e.message, "err"));
  };
  range.onkeydown = (ev) => {
    if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); closePicker(); }
  };
  update();
}

function filter() {
  if (!pick) return;
  const q = $("#q").value.trim().toLowerCase();
  let source = pick.options;
  if (pick.modelPicker && pick.groupFilter === "favorites") source = source.filter((o) => isFavorite(o));
  else if (pick.modelPicker && pick.groupFilter !== "all") source = source.filter((o) => o.group === pick.groupFilter || o.reset);
  const scored = source.map((o) => ({ o, i: pick.options.indexOf(o), s: score(q, o) })).filter((x) => x.s > 0);
  // with a query, best matches first; without, catalog order keeps the groups together
  if (q) scored.sort((a, b) => b.s - a.s || a.i - b.i);
  pick.items = scored.map((x) => x.o);
  const typed = $("#q").value.trim();
  if (typed && pick.free && ["model", "small", "large", ...FOLLOWS_MODEL].includes(pick.field.label) && !pick.items.some((o) => o.value === typed)) {
    pick.items.push({ value: typed, note: t("use as typed"), custom: true });
  }
  pick.cursor = 0;
  renderPickerRail();
  renderList();
}

function updatePickerRailSelection() {
  const rail = $("#pickerRail");
  const active = rail.querySelector(`.rail-item[data-group="${CSS.escape(pick?.groupFilter || "all")}"]`);
  for (const b of rail.querySelectorAll(".rail-item")) b.classList.toggle("on", b === active);
  const thumb = rail.querySelector(".rail-thumb");
  if (active && thumb) {
    thumb.style.opacity = "1";
    thumb.style.transform = `translate3d(0, ${active.offsetTop}px, 0)`;
  }
}

function switchPickerGroup(id) {
  if (!pick?.modelPicker || id === pick.groupFilter) return;
  const railItems = [...$("#pickerRail").querySelectorAll(".rail-item")];
  const from = railItems.findIndex((b) => b.dataset.group === pick.groupFilter);
  const to = railItems.findIndex((b) => b.dataset.group === id);
  pick.groupFilter = id;
  updatePickerRailSelection();
  $("#q").focus();

  const list = $("#list");
  pick.groupAnimation?.cancel();
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (reduced) { filter(); return; }
  const direction = to >= from ? 1 : -1;
  const token = (pick.groupTransition || 0) + 1;
  pick.groupTransition = token;
  const out = list.animate([
    { opacity: 1, transform: "translate3d(0, 0, 0)" },
    { opacity: 0, transform: `translate3d(${-direction * 5}px, 0, 0)` },
  ], { duration: 75, easing: "cubic-bezier(.4, 0, 1, 1)", fill: "forwards" });
  pick.groupAnimation = out;
  out.finished.then(() => {
    if (!pick || pick.groupTransition !== token) return;
    out.cancel();
    filter();
    const incoming = list.animate([
      { opacity: 0, transform: `translate3d(${direction * 7}px, 0, 0)` },
      { opacity: 1, transform: "translate3d(0, 0, 0)" },
    ], { duration: 190, easing: "cubic-bezier(.22, 1, .36, 1)" });
    pick.groupAnimation = incoming;
  }).catch(() => {});
}

function renderPickerRail() {
  const rail = $("#pickerRail");
  rail.hidden = !pick?.modelPicker;
  if (!pick?.modelPicker) { rail.replaceChildren(); rail.dataset.signature = ""; return; }
  const groups = [];
  for (const o of pick.options) if (o.group && !groups.includes(o.group)) groups.push(o.group);
  const signature = groups.join("\u001f");
  if (rail.dataset.signature !== signature) {
    rail.replaceChildren();
    rail.dataset.signature = signature;
    rail.append(el("span", "rail-thumb"));
    const add = (id, title, child) => {
      const b = el("button", "rail-item");
      b.dataset.group = id;
      b.title = title;
      b.setAttribute("aria-label", title);
      b.append(child);
      b.onclick = () => switchPickerGroup(id);
      rail.append(b);
    };
    add("all", t("All models"), svg("M3 3h4v4H3zM9 3h4v4H9zM3 9h4v4H3zM9 9h4v4H9z", 15, 1.4));
    add("favorites", t("Favorites"), svg("m8 2 1.8 3.7 4.1.6-3 2.9.7 4.1L8 11.4l-3.6 1.9.7-4.1-3-2.9 4.1-.6z", 16, 1.4));
    if (groups.length) rail.append(el("span", "rail-sep"));
    for (const group of groups) {
      const sample = pick.options.find((o) => o.group === group);
      if (group === ROUTING_GROUPS) add(group, t(group), svg(FAN, 16, 1.5));
      else add(group, group, icon(sample?.groupIcon || sample?.icon || "generic"));
    }
  }
  queueMicrotask(updatePickerRailSelection);
}

function renderList() {
  const list = $("#list");
  list.replaceChildren();
  if (!pick.items.length) { list.append(el("div", "none", t("No matches."))); return; }
  const hasIcons = pick.items.some((o) => o.icon);
  const q = $("#q").value.trim();
  let group = null;
  pick.items.forEach((o, idx) => {
    if (!q && o.group && o.group !== group) list.append(el("li", "group", o.group === ROUTING_GROUPS ? t(o.group) : o.group));
    if (!q) group = o.group ?? group;
    const li = el("li", (idx === pick.cursor ? "sel" : "") + (o.value === pick.field.value ? " cur" : "") + (o.custom ? " custom" : "") + (o.reset ? " reset" : ""));
    li.dataset.i = idx;
    if (hasIcons) li.append(optionIcon(o));
    const words = el("span", "option-words");
    words.append(el("span", "v", o.label || o.value));
    let note = o.note && o.note !== (o.label || o.value) ? o.note : "";
    if (q && o.group && !note) note = o.group;
    if (note) words.append(el("span", "n", note));
    li.append(words);
    if (pick.modelPicker && o.value && !o.custom) {
      const star = el("button", "favorite" + (isFavorite(o) ? " on" : ""));
      star.title = isFavorite(o) ? t("Remove from favorites") : t("Add to favorites");
      star.append(svg("m8 2 1.8 3.7 4.1.6-3 2.9.7 4.1L8 11.4l-3.6 1.9.7-4.1-3-2.9 4.1-.6z", 14, 1.4));
      star.onclick = (ev) => {
        ev.stopPropagation();
        const key = favoriteKey(o);
        if (isFavorite(o)) { modelFavorites.delete(key); modelFavorites.delete(o.value); } else modelFavorites.add(key);
        localStorage.setItem("magpie.modelFavorites", JSON.stringify([...modelFavorites]));
        filter();
      };
      li.append(star);
    }
    const ck = el("span", "check");
    ck.append(svg(CHECK, 12, 1.8));
    li.append(ck);
    li.onmousemove = () => { if (pick.cursor !== idx) { pick.cursor = idx; renderList(); } };
    li.onclick = () => commit(o.value);
    list.append(li);
  });
  list.querySelector(`li[data-i="${pick.cursor}"]`)?.scrollIntoView({ block: "nearest" });
}

function move(d) {
  if (!pick || !pick.items.length) return;
  pick.cursor = (pick.cursor + d + pick.items.length) % pick.items.length;
  renderList();
}

async function commit(value) {
  if (!pick || value == null) return;
  const { agent, field, anchor } = pick;
  const opt = pick.options.find((o) => o.value === value);
  closePicker();
  if (field.onPick) return field.onPick(value, opt); // a picker opened for something other than an agent's setting
  if (field.menu) {
    // a tier chosen from the tiers menu: now its model
    const tier = agent.fields.find((f) => f.key === value);
    if (tier) openPicker(agent, tier, anchor, { stopPropagation() {} });
    return;
  }
  if (value === field.value) return;
  try {
    state = await api("set", { agent: agent.id, field: field.key, value });
    renderAgents();
    const b = document.querySelector(`.agent[data-id="${agent.id}"] .field[data-key="${TIERS.includes(field.label) ? "tiers" : field.key}"]`);
    b?.classList.add("flash");
    const shown = opt?.label || value;
    if (state.notice) status(`${agent.name} → ${shown}. ${state.notice}`, "warn", 9000);
    else status(`${agent.name} ${t(field.label)} → ${shown}`, "ok");
    if (providers) loadProviders();
  } catch (e) {
    status(e.message, "err");
  }
}

function closePicker() {
  if (!pick) return;
  pick.groupAnimation?.cancel();
  pick.anchor.classList.remove("open");
  $("#pop").hidden = true;
  $("#pop").classList.remove("model-picker", "effort-picker");
  $("#pop .search").hidden = false;
  $("#pop .picker-body").hidden = false;
  $("#effortControl").hidden = true;
  pick = null;
}

$("#q").addEventListener("input", filter);
$("#q").addEventListener("keydown", (e) => {
  if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) { e.preventDefault(); move(1); }
  else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) { e.preventDefault(); move(-1); }
  else if (e.key === "Enter") { e.preventDefault(); commit(pick?.items[pick.cursor]?.value); }
  else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closePicker(); }
});
document.addEventListener("mousedown", (e) => { if (pick && !$("#pop").contains(e.target)) closePicker(); });
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || pick) return;
  if (editing !== null || importingApps) cancelEdit();
  else if (mode === "panel") api("window/hide", {});
});

// ---------- profiles ----------

// profileLibrary is what a profile gives out from the Library, in a few
// words; "" for one saved without it.
function profileLibrary(l) {
  if (!l) return "";
  const parts = [];
  if (l.servers) parts.push(t(l.servers === 1 ? "{n} server" : "{n} servers", { n: l.servers }));
  if (l.skills) parts.push(t(l.skills === 1 ? "{n} skill" : "{n} skills", { n: l.skills }));
  if (l.instructions) parts.push(t("instructions"));
  return parts.length ? t("+ Library: {what}", { what: parts.join(t(", ")) }) : t("+ Library: nothing on");
}

async function profileAction(action, name, update) {
  try {
    const data = await api("profile/" + action, { name });
    state = data;
    renderAgents();
    if (action === "use") {
      let msg = t(data.changed === 1 ? "{name} applied · {n} setting changed" : "{name} applied · {n} settings changed", { name, n: data.changed });
      const lib = data.library;
      if (lib?.missing?.length) msg += " · " + t("skipped, no longer in the Library: {names}", { names: lib.missing.map((m) => m.replace(/^\w+:/, "")).join(", ") });
      if (lib?.problems?.length) status(msg + " · " + t("some of the Library couldn't be given; see Library"), "err", 6000);
      else status(msg, "ok", lib?.missing?.length ? 6000 : 3500);
    }
    else if (action === "save") status(t(update ? "Updated {name} to the current setup" : "Saved {name}", { name }), "ok");
    else status(t("Deleted {name}", { name }));
  } catch (e) {
    status(e.message, "err");
  }
}

$("#save").onclick = () => {
  const chips = $("#profiles");
  if (chips.querySelector(".chip-input")) return;
  const input = el("input", "chip-input");
  input.placeholder = t("Profile name");
  input.onkeydown = (e) => {
    if (e.key === "Enter" && input.value.trim()) profileAction("save", input.value.trim());
    else if (e.key === "Escape") input.remove();
    e.stopPropagation();
  };
  input.onblur = () => setTimeout(() => input.remove(), 100);
  chips.prepend(input);
  input.focus();
};

// ---------- providers view ----------
//
// A provider is a vendor plus the key the user pasted. Presets need only the
// key; a custom one needs a name and a base URL too. Every exposed model of
// every provider becomes "provider/model" in the agents' pickers, served by
// the local gateway in whichever API the agent speaks.

async function loadProviders() {
  if (view === "gateway") renderGatewayLoading();
  else if (!providers) renderProvidersLoading();
  providers = await api("providers");
  // a reload's provider may be gone since
  if (typeof editing === "string" && !providers.providers.some((p) => p.id === editing)) editing = null;
  if (!providers.providers.length && editing === null) adding = true;
  if (view === "gateway") renderGatewayView();
  else renderProviders();
}

// Rows in the shape of the list while it is first asked for; a reload keeps
// the list it has until the new one is in.
function renderProvidersLoading() {
  const page = $("#view-providers");
  page.classList.add("loading");
  page.setAttribute("aria-busy", "true");
  const list = $("#providers");
  list.hidden = false;
  list.replaceChildren();
  for (let i = 0; i < 5; i++) {
    const row = el("div", "row provider pv-sk-row");
    const who = el("div", "who pv-sk-who");
    who.append(el("span", "skeleton pv-sk-name"), el("span", "skeleton pv-sk-sub"));
    row.append(el("span", "skeleton pv-sk-icon"), who, el("span", "skeleton pv-sk-key"));
    list.append(row);
  }
  $("#excluded").replaceChildren();
}

// One row per provider: logo, name, the agents pointed at it, key status.
// Everything else lives in the editor, a dialog over the page.
function renderProviders() {
  // Rebuilding the list empties the page for a moment, which clamps its
  // scroll to the top; put it back so closing the editor leaves the reader
  // where they were.
  const view = $("#view-providers"), top = view.scrollTop;
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  closeProtoMenu();
  syncURL();
  const list = $("#providers");
  list.replaceChildren();
  list.hidden = !providers.providers.length;
  let dialog = null; // the editor, if one is open
  for (const p of providers.providers) {
    const open = editing === p.id;
    const row = el("div", "row provider" + (open ? " selected" : ""));
    row.dataset.id = p.id;
    const who = el("div", "who");
    const name = el("div", "name", p.name);
    if (p.sponsored) name.append(el("span", "badge", t("sponsored")));
    const n = p.models.filter((m) => m.on).length;
    const models = n ? t(n === 1 ? "{n} model" : "{n} models", { n }) : t("no models exposed");
    who.append(name, el("div", "sub", (p.account ? t("signed in as {user}", { user: p.account.user }) : p.host) + " · " + models));
    const using = p.agents.filter((a) => a.current);
    const uses = el("div", "uses");
    for (const a of using) {
      const b = el("button", "use");
      b.title = a.group ? t("{name} · {model}, through the routing group {group} — click to change", { name: a.name, model: a.model, group: a.group })
        : t("{name} · {model} — click to change", { name: a.name, model: a.model });
      b.append(icon(a.icon));
      b.onclick = (ev) => pickForAgent(a, p, b, ev);
      uses.append(b);
    }
    let key;
    if (p.account) {
      key = el("span", "key acct", accountPlan(p.account));
      key.title = t("{agent} is signed in; its models are here for every other agent", { agent: p.account.agentName });
    } else {
      key = el("span", "key " + (p.key.set ? (keyPill(p) === p.key.masked ? "on" : "on acct") : p.ready ? "free" : "none"), p.key.set ? keyPill(p) : p.ready ? t("no key") : t("needs a key"));
      key.title = p.key.set ? t("API key {masked}", { masked: p.key.masked }) : p.ready ? t("Local servers need no key") : t("Open the row and paste an API key");
    }
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(icon(p.icon || "generic"), who, uses, key, chev);
    row.onclick = () => { editing = open ? null : p.id; draft = null; renderProviders(); }; // the preset sheet stays as it is under the dialog
    list.append(row);
    if (open) dialog = renderEditor(p);
  }
  renderExcluded();
  dialog = renderAdd() || dialog;
  if (importing) dialog = renderImport(importing);
  if (importingApps) dialog = renderImportApps(importingApps);
  if (dialog) openModal(dialog); else closeModal();
  view.scrollTop = top;
}

// Sign-ins magpie found but leaves alone, so nobody wonders why an agent that
// is clearly logged in is not in the list: the ones the user removed.
function renderExcluded() {
  const box = $("#excluded");
  box.replaceChildren();
  for (const x of providers.excluded) {
    const r = el("div", "excluded");
    r.append(icon(x.agentIcon), el("span", "", ""));
    r.lastChild.append(el("b", "", t(x.signedOut ? "{agent}'s saved accounts aren't offered. " : "{agent} is signed in, but stays out of this list. ", { agent: x.agentName })), x.signedOut ? x.why : t(x.why));
    if (x.provider) {
      const back = el("button", "link", t("Add it back"));
      back.onclick = () => providerAction("show", { id: x.provider }, t("{name} added back", { name: x.agentName }));
      r.lastChild.append(" ", back);
    }
    box.append(r);
  }
}

// accountPlan names a signed-in account's subscription: "ChatGPT Pro", "GitHub".
function accountPlan(a) {
  if (a.agent === "codex") return "ChatGPT" + (a.plan ? " " + a.plan[0].toUpperCase() + a.plan.slice(1) : "");
  if (a.agent === "copilot") return "GitHub";
  if (a.agent === "claude") return "Claude" + (a.plan ? " " + a.plan[0].toUpperCase() + a.plan.slice(1) : "");
  if (a.agent === "cursor") return "Cursor" + (a.plan ? " " + a.plan[0].toUpperCase() + a.plan.slice(1) : "");
  if (a.agent === "grok") return a.plan || "SuperGrok";
  if (a.agent === "gemini" || a.agent === "antigravity") return a.plan || "Google";
  if (a.agent === "zcode") return a.plan || "GLM Coding Plan";
  return t("signed in");
}

// ---------- gateway view ----------
//
// The gateway is one local endpoint speaking four APIs; this tab is the
// page that gets anything else connected to it: base URL, key, model ids,
// and a snippet in whichever language the reader is holding.

// copy asks magpie to put text on the clipboard, as the page's own
// clipboard API is refused inside the app's window; a browser tab on the
// dev UI falls back to it.
async function copy(text, what, btn) {
  const done = () => { status(t("{what} copied", { what }), "ok"); flashCopied(btn); };
  const res = await fetch("/api/copy", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ text }) }).catch(() => null);
  if (res && res.ok) return done();
  try { await navigator.clipboard.writeText(text); done(); }
  catch { status(text); }
}

// flashCopied answers on the button itself: the icon becomes a tick and
// the button takes a green breath, then it reverts on its own. The footer
// toast says what was copied; this says it landed. Clicking again restarts
// the pop and re-arms the timer.
function flashCopied(b) {
  if (!b) return;
  b.classList.remove("done");
  void b.offsetWidth; // restart the animation when clicked again
  b.classList.add("done");
  b.title = t("Copied");
  b.replaceChildren(svg(CHECK, 12, 1.7));
  clearTimeout(b.copiedT);
  b.copiedT = setTimeout(() => {
    b.classList.remove("done");
    b.title = t("Copy");
    b.replaceChildren(svg(COPY_ICON, 12, 1.5));
  }, 1200);
}

function copyBtn(text, what) {
  const b = el("button", "copy");
  b.title = t("Copy");
  b.append(svg(COPY_ICON, 12, 1.5));
  b.onclick = (ev) => { ev.stopPropagation(); copy(text, what, b); };
  return b;
}

function renderGatewayLoading() {
  const page = $("#view-gateway");
  page.classList.add("loading");
  page.setAttribute("aria-busy", "true");
  $("#connectNote").textContent = "";
  $("#callsNote").textContent = "";
  $("#copyModels").hidden = true;

  const gateway = $("#gateway");
  gateway.replaceChildren();
  const mark = el("span", "skeleton gw-sk-dot");
  const who = el("div", "who gw-sk-who");
  who.append(el("span", "skeleton gw-sk-title"), el("span", "skeleton gw-sk-sub"));
  gateway.append(mark, who, el("span", "skeleton gw-sk-url"));

  const connect = $("#connect");
  connect.replaceChildren();
  for (let i = 0; i < 4; i++) {
    connect.append(el("span", "skeleton gw-sk-label"));
    const value = el("div", "gw-sk-field");
    value.append(el("span", "skeleton"), el("span", "skeleton short"));
    connect.append(value);
  }

  const models = $("#gwModels");
  models.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const row = el("div", "row model gw-sk-row");
    row.append(el("span", "skeleton gw-sk-model"), el("span", "grow"), el("span", "skeleton gw-sk-provider"));
    models.append(row);
  }

  const activity = $("#activity");
  activity.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const row = el("div", "call gw-sk-call");
    row.append(el("span", "skeleton time"), el("span", "skeleton agent"), el("span", "skeleton model"), el("span", "grow"), el("span", "skeleton result"));
    activity.append(row);
  }
}

function renderGatewayView() {
  const page = $("#view-gateway");
  page.classList.remove("loading");
  page.removeAttribute("aria-busy");
  renderGateway();
  renderConnect();
  renderGatewayModels();
  renderActivity();
}

// The status card: dot, state, the URL.
function renderGateway() {
  const g = providers.gateway;
  const box = $("#gateway");
  box.replaceChildren();
  const dot = el("span", "dot " + (g.running ? "on" : ""));
  const who = el("div", "who");
  const name = el("div", "name", t("Gateway"));
  name.append(el("span", "state", t(g.running ? (g.mine ? "running" : "running · served by another magpie") : "not running")));
  const routed = new Set();
  for (const p of providers.providers) for (const a of p.agents) if (a.current) routed.add(a.id);
  const n = routed.size;
  who.append(name, el("div", "sub", g.running
    ? [t(g.models === 1 ? "{n} model" : "{n} models", { n: g.models }), n ? t(n === 1 ? "{n} agent routed through it" : "{n} agents routed through it", { n }) : t("no agent routed through it yet"), t("four APIs, one URL")].join(" · ")
    : t("start it with magpie serve, or open magpie at login")));
  const url = el("button", "url");
  url.append(el("code", "", g.url));
  url.title = t("Copy the gateway URL");
  url.onclick = () => copy(g.url, t("Gateway URL"));
  box.append(dot, who, url);
}

// One entry per API the gateway serves: where each SDK's base URL points,
// the env vars the usual tools read, and a request in four dialects.
const FLAVORS = {
  openai: {
    name: "OpenAI", base: (u) => u + "/v1", baseEnv: "OPENAI_BASE_URL", keyEnv: "OPENAI_API_KEY",
    note: "Chat Completions, the API most tools speak. Anything with an OpenAI base-URL setting works.",
    curl: (b, m) => ({ url: `${b}/chat/completions`, headers: ["Authorization: Bearer magpie"],
      body: `{"model": "${m}",\n "messages": [{"role": "user", "content": "hi"}]}` }),
    python: (b, m) => `from openai import OpenAI

client = OpenAI(base_url="${b}", api_key="magpie")
r = client.chat.completions.create(
    model="${m}",
    messages=[{"role": "user", "content": "hi"}],
)
print(r.choices[0].message.content)`,
    node: (b, m) => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${b}", apiKey: "magpie" });
const r = await client.chat.completions.create({
  model: "${m}",
  messages: [{ role: "user", content: "hi" }],
});
console.log(r.choices[0].message.content);`,
  },
  responses: {
    name: "Responses", base: (u) => u + "/v1", baseEnv: "OPENAI_BASE_URL", keyEnv: "OPENAI_API_KEY",
    note: "OpenAI's newer API: reasoning, built-in tool items, encrypted reasoning. Codex speaks this.",
    curl: (b, m) => ({ url: `${b}/responses`, headers: ["Authorization: Bearer magpie"],
      body: `{"model": "${m}", "input": "hi"}` }),
    python: (b, m) => `from openai import OpenAI

client = OpenAI(base_url="${b}", api_key="magpie")
r = client.responses.create(model="${m}", input="hi")
print(r.output_text)`,
    node: (b, m) => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${b}", apiKey: "magpie" });
const r = await client.responses.create({ model: "${m}", input: "hi" });
console.log(r.output_text);`,
  },
  anthropic: {
    name: "Anthropic", base: (u) => u, baseEnv: "ANTHROPIC_BASE_URL", keyEnv: "ANTHROPIC_API_KEY",
    note: "Messages API. Claude Code reads ANTHROPIC_AUTH_TOKEN instead of the key; the Agents tab sets that for you.",
    curl: (b, m) => ({ url: `${b}/v1/messages`, headers: ["x-api-key: magpie", "anthropic-version: 2023-06-01"],
      body: `{"model": "${m}", "max_tokens": 1024,\n "messages": [{"role": "user", "content": "hi"}]}` }),
    python: (b, m) => `import anthropic

client = anthropic.Anthropic(
    base_url="${b}", api_key="magpie",
)
m = client.messages.create(
    model="${m}",
    max_tokens=1024,
    messages=[{"role": "user", "content": "hi"}],
)
print(m.content[0].text)`,
    node: (b, m) => `import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({ baseURL: "${b}", apiKey: "magpie" });
const m = await client.messages.create({
  model: "${m}",
  max_tokens: 1024,
  messages: [{ role: "user", content: "hi" }],
});
console.log(m.content[0].text);`,
  },
  gemini: {
    name: "Gemini", base: (u) => u, baseEnv: "GOOGLE_GEMINI_BASE_URL", keyEnv: "GEMINI_API_KEY",
    note: "Google's generateContent API, v1beta. Gemini CLI and the google-genai SDKs speak this.",
    curl: (b, m) => ({ url: `${b}/v1beta/models/${m}:generateContent`, headers: ["x-goog-api-key: magpie"],
      body: `{"contents": [{"parts": [{"text": "hi"}]}]}` }),
    python: (b, m) => `from google import genai

client = genai.Client(api_key="magpie", http_options={"base_url": "${b}"})
r = client.models.generate_content(model="${m}", contents="hi")
print(r.text)`,
    node: (b, m) => `import { GoogleGenAI } from "@google/genai";

const ai = new GoogleGenAI({
  apiKey: "magpie",
  httpOptions: { baseUrl: "${b}" },
});
const r = await ai.models.generateContent({ model: "${m}", contents: "hi" });
console.log(r.text);`,
  },
};
// Windows gets PowerShell: $env: in place of export, and the curl.exe that
// ships with it (plain curl there is Invoke-WebRequest). The body goes in
// on stdin, since Windows PowerShell drops the quotes inside an argument.
const WIN = /^Win/.test(navigator.platform);
const LANGS = [["shell", WIN ? "PowerShell" : "Shell"], ["curl", "curl"], ["python", "Python"], ["node", "Node"]];

function curlSnippet({ url, headers, body }) {
  headers = [...headers, "Content-Type: application/json"];
  if (WIN) return `@'\n${body}\n'@ | curl.exe ${url} \`\n${headers.map((h) => `  -H "${h}" \``).join("\n")}\n  --data-binary "@-"`;
  return `curl ${url} \\\n${headers.map((h) => `  -H "${h}" \\`).join("\n")}\n  -d '${body.replaceAll("\n", "\n      ")}'`;
}

function envSnippet(vars) {
  return vars.map(([k, v]) => (WIN ? `$env:${k}="${v}"` : `export ${k}=${v}`)).join("\n");
}

// every exposed model, as the ids agents use
function gatewayModels() {
  // the routing groups first, as the agents' pickers list them
  const out = (providers.gateway.groups || []).map((g) => ({ id: g.id, name: g.name, icons: g.icons, group: true,
    provider: { name: [t("routing group"), g.providers.join(", ")].filter(Boolean).join(" · ") } }));
  for (const p of providers.providers) for (const m of p.models) if (m.on) out.push({ id: `${p.id}/${m.id}`, name: m.name, provider: p });
  return out;
}

function segs(items, current, onPick) {
  const box = el("div", "segs");
  const key = items.map(([id]) => id).join("|");
  for (const [id, name] of items) {
    const b = el("button", "opt" + (id === current ? " on" : ""), name);
    b.onclick = () => { for (const x of box.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(box, key); onPick(id); };
    box.append(b);
  }
  queueMicrotask(() => slide(box, key)); // once it is in the page
  return box;
}

function renderConnect() {
  const g = providers.gateway;
  const box = $("#connect");
  box.replaceChildren();
  const models = gatewayModels();
  if (!models.some((m) => m.id === exampleModel)) exampleModel = models[0]?.id || "";
  const model = exampleModel || "provider/model";
  const f = FLAVORS[flavor] || FLAVORS.openai;
  const base = f.base(g.url);
  $("#connectNote").textContent = t("Loopback only · the key can be anything");

  box.append(...field("API", segs(Object.entries(FLAVORS).map(([k, v]) => [k, v.name]), flavor, (id) => { flavor = id; localStorage.setItem("magpie.flavor", id); renderConnect(); }), t(f.note)));

  const b = el("div", "val");
  b.append(el("code", "", base), copyBtn(base, "Base URL"));
  box.append(...field("Base URL", b, t("What {env} takes.", { env: f.baseEnv })));

  const k = el("div", "val");
  k.append(el("code", "", "magpie"), copyBtn("magpie", t("Key")));
  box.append(...field(t("API key"), k, t("{env}=magpie. The gateway trusts everything on loopback, so any value works.", { env: f.keyEnv })));

  const m = el("div", "val");
  m.append(el("code", "", model), copyBtn(model, t("Model id")));
  box.append(...field(t("Model"), m, t(models.length ? "provider/model, as listed below. Click a model there to put it in the snippets." : "No models yet. Add a provider, or sign in to Codex or Copilot.")));

  const ex = el("div", "stack");
  ex.append(segs(LANGS, lang, (id) => { lang = id; localStorage.setItem("magpie.lang", id); renderConnect(); }));
  const code = lang === "shell" ? envSnippet([[f.baseEnv, base], [f.keyEnv, "magpie"]])
    : lang === "curl" ? curlSnippet(f.curl(base, model))
    : f[lang](base, model);
  const pre = el("pre", "snip");
  const c = el("code");
  c.append(highlight(code, lang));
  pre.append(c);
  // the button sits outside the scrolling box, so a long line doesn't carry it off
  const wrap = el("div", "snip-wrap");
  wrap.append(pre, copyBtn(code, t("Snippet")));
  ex.append(wrap);
  box.append(...field(t("Example"), ex, lang === "shell" ? t("Put these in the shell (or the tool's settings) and the tool talks to magpie instead of the vendor.") : ""));
}

// A small highlighter for the four snippet dialects: strings, comments,
// keywords, numbers, calls, and the env vars and flags shells care about.
const KEYWORDS = {
  python: /^(from|import|def|return|await|async|for|in|if|else|None|True|False)$/,
  node: /^(import|from|const|let|await|async|new|return|function|export|default)$/,
  shell: /^(export|curl)$/,
  curl: /^(curl|curl\.exe)$/,
};
function highlight(code, lang) {
  const re = lang === "node"
    ? /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`)|(\/\/.*)|(\b\d+(?:\.\d+)?\b)|([A-Za-z_$][\w$]*)(?=\s*\()|([A-Za-z_$][\w$]*)|(\s+|.)/g
    : /("(?:[^"\\]|\\.)*"|'[^']*'|(?<==)\S+)|(#.*)|(\b\d+(?:\.\d+)?\b(?=[,\s\]}]))|(-{1,2}[A-Za-z][\w-]*)|([A-Z][A-Z0-9_]+)(?==)|([A-Za-z_][\w.]*)(?=\s*\()|([A-Za-z_][\w.]*)|(\\\n|`\n)|(\s+|.)/g;
  const out = document.createDocumentFragment();
  const kw = KEYWORDS[lang] || KEYWORDS.shell;
  let m;
  while ((m = re.exec(code))) {
    let cls = "";
    if (lang === "node") {
      if (m[1]) cls = "s"; else if (m[2]) cls = "c"; else if (m[3]) cls = "n";
      else if (m[4]) cls = kw.test(m[4]) ? "k" : "f"; else if (m[5] && kw.test(m[5])) cls = "k";
    } else {
      if (m[1]) cls = "s"; else if (m[2]) cls = "c"; else if (m[3]) cls = "n"; else if (m[4]) cls = "o";
      else if (m[5]) cls = "v"; else if (m[6]) cls = kw.test(m[6]) ? "k" : "f"; else if (m[7] && kw.test(m[7])) cls = "k";
      else if (m[9]) cls = "o";
    }
    if (cls) out.append(el("span", "tk-" + cls, m[0]));
    else out.append(m[0]);
  }
  return out;
}

let modelQuery = "";
function renderGatewayModels() {
  const list = $("#gwModels");
  list.replaceChildren();
  const all = gatewayModels();
  // the search sits in the section head, beside Copy all ids; typing
  // redraws only the list, so it keeps its focus
  let q = $("#findModel");
  if (!q) {
    q = input(modelQuery, t("Find a model…"));
    q.id = "findModel";
    q.className = "find";
    q.oninput = () => { modelQuery = q.value; renderGatewayModels(); };
    q.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape" && q.value) { q.value = modelQuery = ""; renderGatewayModels(); } };
    $("#copyModels").before(q);
  }
  q.hidden = all.length < 8;
  const words = modelQuery.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const models = q.hidden ? all : all.filter((m) => {
    const hay = `${m.id} ${m.name || ""} ${m.provider.name}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  $("#copyModels").hidden = !models.length;
  $("#copyModels").onclick = () => copy(models.map((m) => m.id).join("\n"), t("Model ids"));
  if (!all.length) {
    const empty = el("div", "empty-state", "");
    empty.append(el("b", "", t("No models exposed yet")), t("Add a provider, or sign in to Codex or Copilot; their models show up here for every agent."));
    list.append(empty);
    return;
  }
  if (!models.length) {
    list.append(el("div", "none", t("No models match “{q}”", { q: modelQuery.trim() })));
    return;
  }
  for (const m of models) {
    const row = el("div", "row model" + (m.id === exampleModel ? " selected" : ""));
    const who = el("div", "who");
    who.append(el("div", "name", m.id), el("div", "sub", m.name && m.name !== m.id.split("/")[1] ? `${m.name} · ${m.provider.name}` : m.provider.name));
    row.append(m.group ? stackIcon(m.icons) : icon(m.provider.icon || "generic"), who, copyBtn(m.id, t("Model id")));
    row.title = t("Use this model in the snippets");
    row.onclick = () => { exampleModel = m.id; localStorage.setItem("magpie.model", m.id); renderConnect(); renderGatewayModels(); };
    list.append(row);
  }
}

function formatWireBody(raw) {
  if (!raw) return "";
  try { return JSON.stringify(JSON.parse(raw), null, 2); } catch { return raw; }
}

function callBodyPanel(label, raw, truncated) {
  const panel = el("section", "call-body");
  const head = el("div", "call-body-head");
  head.append(el("span", "call-body-label", t(label)));
  if (truncated) head.append(el("span", "call-body-truncated", t("first 256 KB")));
  const formatted = formatWireBody(raw);
  if (formatted) head.append(el("span", "grow"), copyBtn(raw, t(label)));
  panel.append(head);
  const pre = el("pre");
  const code = el("code", "", formatted || t("No body captured"));
  if (!formatted) code.classList.add("empty");
  pre.append(code);
  panel.append(pre);
  return panel;
}

function renderActivity() {
  const g = providers.gateway;
  const box = $("#activity");
  box.replaceChildren();
  $("#callsNote").textContent = g.running && !g.mine ? t("shown by the magpie that serves the gateway") : "";
  const calls = g.calls.slice(0, 20);
  if (!calls.length) { box.append(el("div", "none", t("No requests yet. Point an agent at a model, or run the example above; every call shows up here as it happens."))); return; }
  for (const c of calls) {
    const id = `${c.time}|${c.agent}|${c.model}`;
    const open = expandedCalls.has(id);
    const item = el("div", "call-item" + (open ? " open" : "") + (c.status >= 400 ? " bad" : ""));
    const r = el("div", "call");
    r.setAttribute("role", "button");
    r.setAttribute("tabindex", "0");
    r.setAttribute("aria-expanded", String(open));
    const chev = el("span", "call-chev");
    chev.append(svg(CHEV_R, 11, 1.6));
    r.append(chev);
    r.append(el("span", "when", new Date(c.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })));
    r.append(el("span", "a", c.agent || "—"));
    r.append(el("span", "m", c.model));
    r.append(el("span", "p", c.from === c.to ? c.from : `${c.from} → ${c.to}`));
    r.append(el("span", "grow"));
    r.append(el("span", "st", c.error ? `${c.status} ${c.error}` : `${c.status} · ${c.ms} ms`));
    r.title = open ? t("Hide request and response bodies") : t("Show request and response bodies");
    const toggle = () => {
      if (expandedCalls.has(id)) expandedCalls.delete(id); else expandedCalls.add(id);
      renderActivity();
    };
    r.onclick = toggle;
    r.onkeydown = (ev) => {
      if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); toggle(); }
    };
    item.append(r);
    if (open) {
      const details = el("div", "call-details");
      details.append(
        callBodyPanel("Request Body", c.requestBody, c.requestTruncated),
        callBodyPanel("Response Body", c.responseBody, c.responseTruncated),
      );
      item.append(details);
    }
    box.append(item);
  }
}

// An agent's model field, and whether any of its options come from provider p.
function modelField(a) {
  const agent = state.agents.find((x) => x.id === a.id);
  return agent?.fields.find((f) => f.key === "model") || null;
}
function ofProvider(p) { return new RegExp(`^(magpie/)?${p.id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/`); }

// Pick one of this provider's models for an agent, straight from the row.
function pickForAgent(a, p, btn, ev) {
  const agent = state.agents.find((x) => x.id === a.id);
  const field = modelField(a);
  if (!field) return;
  // on a routing group: the whole picker, the group first, not only this
  // provider's models — picking one would take the agent off the group
  if (a.group) return openPicker(agent, field, btn, ev);
  const pre = ofProvider(p);
  if (!field.options.some((o) => pre.test(o.value))) {
    ev.stopPropagation();
    status(t("{p} exposes no models yet — pick some below", { p: p.name }), "warn");
    editing = p.id; draft = null; renderProviders();
    return;
  }
  openPicker(agent, field, btn, ev, (o) => pre.test(o.value));
}

// The add sheet: presets first (a key is all they need), custom last.
let presetQuery = "";
function renderAdd() {
  const sheet = $("#addSheet");
  sheet.replaceChildren();
  sheet.hidden = !adding;
  $("#addProvider").hidden = adding;
  if (!adding) return null;
  const head = el("div", "row-head");
  head.append(el("span", "label", t(providers.providers.length ? "Add a provider" : "Add your first provider")), el("span", "grow"));
  const q = input(presetQuery, t("Find a vendor…"));
  q.className = "find";
  q.oninput = () => { presetQuery = q.value; drawTiles(); };
  head.append(q);
  const imp = el("button", "text", t("Import…"));
  imp.title = t("Bring over providers set up in other apps");
  imp.onclick = openImportApps;
  head.append(imp);
  if (providers.providers.length) {
    const x = el("button", "text", t("Close"));
    x.onclick = () => { adding = false; editing = null; draft = null; presetQuery = ""; renderProviders(); };
    head.append(x);
  }
  sheet.append(head);
  const tiles = el("div", "tiles");
  sheet.append(tiles);
  const drawTiles = () => {
    tiles.replaceChildren();
    const f = presetQuery.trim().toLowerCase();
    const hit = (pr) => !f || pr.name.toLowerCase().includes(f) || pr.id.includes(f) || hostOf(pr.chat || pr.responses || pr.anthropic).includes(f) || (pr.note || "").toLowerCase().includes(f);
    let any = false;
    const subs = SUBS.filter((x) => !f || x.name.toLowerCase().includes(f) || x.agent.includes(f) || "subscription".includes(f));
    if (subs.length) {
      any = true;
      tiles.append(el("div", "kind", t("Subscriptions · sign in, no key")));
      const grid = el("div", "grid");
      for (const x of subs) grid.append(subTile(x));
      tiles.append(grid);
      const w = subs.find((x) => signing?.agent === x.agent);
      if (w) tiles.append(renderSigning(w));
    }
    for (const [kind, title] of [["vendor", "Vendors"], ["relay", "Relays · many vendors behind one key"], ["local", "On this machine"]]) {
      const ps = providers.presets.filter((p) => p.kind === kind && hit(p));
      if (!ps.length && !(kind === "local" && !f)) continue;
      any = true;
      tiles.append(el("div", "kind", t(title)));
      const grid = el("div", "grid");
      for (const pr of ps) grid.append(tile(pr));
      if (kind === "local" && !f) {
        const c = el("button", "tile custom" + (editing?.custom ? " on" : ""));
        const ic = el("span", "ic plus");
        ic.append(svg(PLUS, 13, 1.8));
        c.append(ic, el("span", "tt"));
        c.lastChild.append(el("span", "n", t("Custom")), el("span", "s", t("any compatible URL")));
        c.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
        grid.append(c);
      }
      tiles.append(grid);
    }
    if (!any) {
      const none = el("div", "none");
      none.append(t("Nothing called “{q}”. ", { q: presetQuery.trim() }));
      const b = el("button", "link", t("Add it as a custom provider"));
      b.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
      none.append(b);
      tiles.append(none);
    }
  };
  drawTiles();
  return editing && typeof editing === "object" ? renderEditor(null, editing.preset) : null;
}

// subTile adds a subscription: one more account when the agent has some.
function subTile(x) {
  const have = providers.providers.find((p) => p.account?.agent === x.agent);
  const n = have ? (have.account.logins?.length || 1) : 0;
  const b = el("button", "tile" + (signing?.agent === x.agent ? " on" : ""));
  b.append(icon(x.icon));
  const tt = el("span", "tt");
  tt.append(el("span", "n", t("{name} subscription", { name: x.name })),
    el("span", "s", !n ? x.plans : x.single ? t("signed in · switch account") : t(n === 1 ? "1 account · add another" : "{n} accounts · add another", { n })));
  b.append(tt);
  b.onclick = () => startSignIn(x.agent);
  return b;
}

function tile(pr) {
  const b = el("button", "tile" + (pr.added ? " added" : "") + (editing?.preset === pr.id ? " on" : ""));
  b.append(icon(pr.icon || "generic"));
  const tt = el("span", "tt");
  const n = el("span", "n", pr.name);
  if (pr.sponsored) n.append(el("span", "badge", t("sponsored")));
  tt.append(n, el("span", "s", pr.note || hostOf(pr.chat || pr.responses || pr.anthropic)));
  b.append(tt);
  if (pr.added) {
    const ck = el("span", "check");
    ck.append(svg(CHECK, 10, 2));
    b.append(ck);
    b.title = t("{name} is already added — open it", { name: pr.name });
    // the first provider made from it, which may not have the preset's id
    const have = providers.providers.find((p) => p.preset === pr.id) || providers.providers.find((p) => p.id === pr.id);
    b.onclick = () => { editing = have?.id ?? pr.id; draft = null; renderProviders(); };
  } else {
    b.onclick = () => { editing = { preset: pr.id }; draft = null; renderProviders(); };
  }
  return b;
}

function field(label, control, hint) {
  const l = el("label", "", label);
  const wrap = el("div");
  wrap.append(control);
  if (hint) wrap.append(el("div", "hint", hint));
  return [l, wrap];
}
function input(value, placeholder, type = "text") {
  const i = el("input");
  i.type = type;
  i.value = value || "";
  i.placeholder = placeholder || "";
  i.spellcheck = false;
  i.autocomplete = "off";
  i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") cancelEdit(); };
  return i;
}
function cancelEdit() { editing = null; draft = null; importing = null; importingApps = null; renderProviders(); }

// Custom request headers: the draft keeps them as an ordered [name, value,
// json?] list so a half-typed row (and its open JSON editor) survives a
// re-render; headersOf folds that back into the object the backend stores,
// dropping rows with an empty name or value (a suggested header left unfilled
// is not sent), keeping the last of two rows that name one header in any
// case, and minifying any value that parses as JSON — a header value is one
// line, so pretty-printing is display-only.
function headerRows(obj) {
  return Object.entries(obj || {}).map(([k, v]) => [k, v, false]);
}
function headersOf(rows) {
  const out = {};
  for (const [k, v] of rows || []) {
    const name = (k || "").trim();
    const val = (v || "").trim();
    if (!name || !val) continue;
    for (const had of Object.keys(out)) if (had.toLowerCase() === name.toLowerCase()) delete out[had];
    // minify only a JSON blob; a plain value like "2.0" must stay verbatim
    out[name] = looksJSON(val) ? minifyJSON(val) : val;
  }
  return out;
}
// minifyJSON collapses a value to one line when it is valid JSON, so a
// pretty-printed blob in the editor never reaches the wire with newlines.
function minifyJSON(s) {
  try { return JSON.stringify(JSON.parse(s)); } catch { return s; }
}
function prettyJSON(s) {
  try { return JSON.stringify(JSON.parse(s), null, 2); } catch { return s; }
}
function looksJSON(s) {
  s = (s || "").trim();
  return s.startsWith("{") || s.startsWith("[");
}
// hints are the optional headers a preset's vendor documents: each one not
// in the list yet is offered as a button that adds its row, value left empty.
function headerEditor(hints = []) {
  const box = el("div", "headers");
  const render = () => {
    box.replaceChildren();
    draft.headers.forEach((row, i) => {
      const line = el("div", "pair");
      const name = input(row[0], t("Header-Name"));
      name.oninput = () => { row[0] = name.value; };

      // the value: a one-line box, or (when the row is expanded) a full-width
      // textarea that pretty-prints JSON for editing. The toggle is always
      // offered — a value only becomes JSON after you paste it in.
      let valCtl;
      if (row[2]) {
        valCtl = el("textarea", "json");
        valCtl.value = prettyJSON(row[1]);
        valCtl.spellcheck = false;
        valCtl.wrap = "off"; // each JSON line stays on one line; scroll instead
        valCtl.rows = Math.min(16, Math.max(4, valCtl.value.split("\n").length));
        valCtl.placeholder = t("value");
        valCtl.oninput = () => {
          row[1] = valCtl.value;
          valCtl.classList.toggle("bad", looksJSON(valCtl.value) && !parses(valCtl.value));
        };
        valCtl.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") cancelEdit(); };
      } else {
        valCtl = input(row[1], t("value"));
        valCtl.oninput = () => { row[1] = valCtl.value; };
      }

      const side = el("div", "side");
      const j = el("button", "text" + (row[2] ? " on" : ""), "{ }");
      j.title = row[2] ? t("Collapse to one line") : t("Edit as JSON");
      j.onclick = () => { row[2] = !row[2]; if (!row[2]) row[1] = minifyJSON(row[1]); render(); };
      side.append(j);
      const del = el("button", "text danger", "×");
      del.title = t("Remove header");
      del.onclick = () => { draft.headers.splice(i, 1); render(); };
      side.append(del);

      if (row[2]) {
        line.classList.add("col");
        const top = el("div", "hhead");
        top.append(name, side);
        line.append(top, valCtl);
      } else {
        line.append(name, valCtl, side);
      }
      box.append(line);
    });
    const adds = el("div", "hadds");
    const add = el("button", "text", t("+ Add header"));
    add.onclick = () => { draft.headers.push(["", "", false]); render(); };
    adds.append(add);
    for (const h of hints) {
      if (draft.headers.some((r) => (r[0] || "").trim().toLowerCase() === h.toLowerCase())) continue;
      const b = el("button", "text", "+ " + h);
      b.title = t("Add the {h} header; its value is yours to fill in", { h });
      b.onclick = () => { draft.headers.push([h, "", false]); render(); [...box.querySelectorAll(".pair")].pop()?.querySelectorAll("input, textarea")[1]?.focus(); };
      adds.append(b);
    }
    box.append(adds);
  };
  render();
  return box;
}
function parses(s) { try { JSON.parse(s); return true; } catch { return false; } }

// iconPicker: a custom provider's icon — one of the built-in ones, or a
// picture of the user's own, which magpie keeps in ~/.config/magpie/icons.
// A routing group's icon: its providers' icons stacked, the first on top,
// so which providers a group routes over shows at a glance.
function stackIcon(icons) {
  if (!icons || icons.length < 2) return icon(icons?.[0] || "generic");
  const e = el("span", "ic-stack");
  for (const n of icons.slice(0, 3)) {
    const d = el("span", "disc");
    d.append(icon(n || "generic"));
    e.append(d);
  }
  if (icons.length > 3) e.append(el("span", "disc more", "+" + (icons.length - 3)));
  return e;
}

function optionIcon(o) {
  return o.icons?.length ? stackIcon(o.icons) : icon(o.icon);
}

// The picker's own section for routing groups (agent.RoutingGroups), and
// its mark on the rail: one model fanning out to several providers.
const ROUTING_GROUPS = "Routing groups";
const dot = (x, y) => `M${x - 1.4} ${y}a1.4 1.4 0 1 0 2.8 0a1.4 1.4 0 1 0 -2.8 0`;
const FAN = [dot(2.9, 8), dot(13.1, 3.4), dot(13.1, 8), dot(13.1, 12.6),
  "M4.3 8h7.4", "M4.3 8c2.6 0 3.2-4.6 5.8-4.6h1.6", "M4.3 8c2.6 0 3.2 4.6 5.8 4.6h1.6"].join(" ");

function iconPicker(ed) {
  const box = el("div", "icon-pick");
  const draw = () => {
    box.replaceChildren();
    const now = el("span", "icon-now");
    now.append(icon(draft.icon || "generic"));
    box.append(now);
    const file = el("input");
    file.type = "file";
    file.accept = "image/png,image/jpeg,image/gif,image/webp,image/x-icon,image/svg+xml,.ico,.svg";
    file.hidden = true;
    file.onchange = async () => {
      const f = file.files[0];
      if (!f) return;
      try {
        // as base64 in JSON: the app's web view drops a File sent as the body
        const bytes = new Uint8Array(await f.arrayBuffer());
        let bin = "";
        for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
        const data = await api("icons", { data: btoa(bin) });
        draft.icon = data.icon;
        editorError("");
        draw();
        syncHead();
      } catch (e) {
        editorError(e.message);
      }
    };
    const choose = el("button", "text", t("Choose a picture…"));
    choose.onclick = () => file.click();
    const builtin = el("button", "text", t("Built-in icons"));
    builtin.onclick = () => { open = !open; draw(); };
    box.append(file, choose, builtin);
    if (draft.icon && draft.icon !== "generic") {
      const reset = el("button", "text", t("Default"));
      reset.onclick = () => { draft.icon = ""; draw(); syncHead(); };
      box.append(reset);
    }
    if (open) {
      const grid = el("div", "icon-grid");
      const names = [...new Set((providers.presets || []).map((p) => p.icon).filter((n) => n && n !== "generic"))].sort();
      for (const n of names) {
        const b = el("button", n === draft.icon ? "on" : "");
        b.title = n.replace(/-color$/, "");
        b.append(icon(n));
        b.onclick = () => { draft.icon = n; open = false; draw(); syncHead(); };
        grid.append(b);
      }
      box.append(grid);
    }
  };
  let open = false;
  // the dialog's title shows the icon too
  const syncHead = () => {
    const old = ed.querySelector(".ehead .ic");
    if (old) old.replaceWith(icon(draft.icon || "generic"));
  };
  draw();
  return box;
}

// ---------- modal ----------
// The provider editor opens as a dialog over the page; Escape, the backdrop
// or Cancel close it.
let modalTimer = 0;
function openModal(content) {
  const m = $("#modal"), d = m.firstElementChild;
  clearTimeout(modalTimer);
  m.classList.remove("out");
  d.classList.remove("swap");
  if (!m.hidden) { void d.offsetWidth; d.classList.add("swap"); } // content changed: a soft refresh, not a re-entrance
  d.replaceChildren(content);
  m.hidden = false;
}
function closeModal() {
  const m = $("#modal");
  if (m.hidden || m.classList.contains("out")) return;
  m.classList.add("out");
  modalTimer = setTimeout(() => { m.hidden = true; m.classList.remove("out"); m.firstElementChild.replaceChildren(); }, 170);
}
$("#modal").onclick = (e) => { if (e.target === e.currentTarget) cancelEdit(); };

// ---------- sliding thumb ----------
// Pills (the nav, every segmented control) have one thumb that glides to the
// selected option instead of each option lighting up on its own.
const thumbs = new Map(); // pill key → where its thumb is, so a re-rendered pill takes over mid-slide
function slide(box, key) {
  let th = box.querySelector(":scope > .thumb");
  if (!th) { th = el("span", "thumb"); box.prepend(th); }
  const on = box.querySelector(":scope > .on");
  if (!on) { th.style.opacity = "0"; return; }
  th.style.opacity = "";
  const to = { x: on.offsetLeft, w: on.offsetWidth };
  const last = thumbs.get(key);
  const from = last && performance.now() - last.at < 300 ? last.from : last; // re-rendered mid-slide: start where the old one started
  const put = (p) => { th.style.transform = `translateX(${p.x}px)`; th.style.width = p.w + "px"; };
  th.classList.add("still");
  put(from || to);
  void th.offsetWidth;
  th.classList.remove("still");
  put(to);
  thumbs.set(key, { ...to, at: performance.now(), from: from || to });
}

const PROTOS = [["chat", "OpenAI", "Chat Completions — most agents"], ["responses", "Responses", "OpenAI Responses — what Codex speaks"], ["anthropic", "Anthropic", "Anthropic Messages — what Claude Code speaks"], ["decide", "Jev", "TypeSafe's decision API — what a routing group asks as a turn begins"]];

// renderEditor: an existing provider (p), a new preset (presetID), or custom.
function renderEditor(p, presetID) {
  const pr = presetID ? providers.presets.find((x) => x.id === presetID) : p?.preset ? providers.presets.find((x) => x.id === p.preset) : null;
  const isNew = !p, custom = !pr && !p?.account, decides = !!(p?.decide || pr?.decide);
  // a preset already added is added again only through "Add another": one
  // more provider of it, under a name and id of its own
  const another = isNew && !!pr?.added;
  draft = draft || (p
    ? { id: p.id, name: p.name, preset: p.preset, chat: p.chat, responses: p.responses, anthropic: p.anthropic, catalog: p.catalog, key: "", api: p.chat ? "openai" : p.anthropic ? "anthropic" : p.responses ? "responses" : "openai", chosen: p.models.filter((m) => m.on).map((m) => m.id), extra: [], headers: headerRows(p.headers), icon: p.icon || "", fallback: [...(p.fallback || [])], unlisted: !!p.unlisted, balanceURL: p.balanceURL || "", balancePath: p.balancePath || "", modelsURL: p.modelsURL || "", contexts: contextsText(p.contexts) }
    : pr
      ? { id: pr.id, name: pr.name, preset: pr.id, key: "", chosen: [], extra: [], headers: [] }
      : { id: "", name: "", preset: "", chat: "", responses: "", anthropic: "", catalog: "", key: "", api: "openai", chosen: [], extra: [], headers: [], icon: "" });
  const ed = el("div", "editor" + (isNew ? " new" : ""));
  ed.onclick = (e) => e.stopPropagation();

  {
    const h = el("div", "ehead");
    h.append(icon(p?.icon || pr?.icon || "generic"), el("b", "", p ? p.name : pr ? pr.name : t("Custom provider")));
    if (pr?.note) h.append(el("span", "note", pr.note));
    h.append(el("span", "grow"));
    const site = pr?.website || p?.website || (p?.host ? "https://" + p.host : "");
    if (site) { const b = el("button", "link", hostOf(site) + " ↗"); b.onclick = () => api("open", { url: site }); h.append(b); }
    ed.append(h);
  }

  // who uses it: just the agents already pointed here, so a click changes
  // one's model. Pointing a new agent at the provider happens in the Agent
  // tab's picker, not here.
  if (p) {
    const on = p.agents.filter((a) => a.current);
    if (on.length) {
      const chips = el("div", "achips");
      for (const a of on) {
        const c = el("button", "achip on");
        c.append(icon(a.icon), el("span", "n", a.name), el("span", "m", a.group || a.model));
        c.title = a.group ? t("{agent} is on the routing group {group}, {model} here among its members — click to change", { agent: a.name, group: a.group, model: a.model })
          : t("{agent} is on {model} — click to change", { agent: a.name, model: a.model });
        c.onclick = (ev) => pickForAgent(a, p, c, ev);
        chips.append(c);
      }
      ed.append(...field(t("Agents"), chips, ""));
    }
  }

  let name, url;
  if (another) {
    name = input(draft.name === pr.name ? "" : draft.name, t("e.g. {name} · Work", { name: pr.name }));
    const hint = el("div", "hint");
    const idHint = () => { hint.textContent = t("id {id} — a number is added if it is taken", { id: draft.id }); };
    name.oninput = () => { draft.name = name.value.trim() || pr.name; draft.id = slug(name.value) || pr.id; idHint(); };
    idHint();
    const wrap = el("div");
    wrap.append(name, hint);
    ed.append(el("label", "", t("Name")), wrap);
  }
  // an added provider's id can change: its models are picked by it, and
  // the agents and routing groups on them move to the new one
  const idField = () => {
    const idIn = input(draft.id, p.id);
    const hint = el("div", "hint");
    const idOf = () => slug(draft.id) || p.id;
    const show = () => {
      hint.textContent = t(decides ? "Routing groups name its models as {id} for their classifier" : "Agents pick its models as {id}", { id: idOf() + "/…" }) +
        (idOf() !== p.id ? " · " + t("agents and routing groups on {id} move to it", { id: p.id + "/…" }) : "");
    };
    idIn.oninput = () => { draft.id = idIn.value; show(); };
    idIn.onblur = () => { draft.id = idIn.value = idOf(); show(); };
    show();
    const w = el("div");
    w.append(idIn, hint);
    ed.append(el("label", "", t("ID")), w);
  };
  if (p && !p.account && !custom) idField();
  let fillEndpoints = () => {};
  if (custom) {
    name = input(draft.name, t("e.g. My Relay"));
    name.oninput = () => { draft.name = name.value; if (isNew) draft.id = slug(name.value); };
    ed.append(...field(t("Name"), name));
    if (p) idField();

    // the base URL is the one the chosen protocol is asked at; a vendor
    // that serves only the Responses API is added (and tested) with that
    // alone, since /chat/completions would only fail (#73)
    const seg = el("div", "segs");
    for (const [v, l, hint] of [["openai", "OpenAI compatible", "…/v1 — chat completions, and responses when the vendor has it"], ["responses", "OpenAI Responses", "…/v1 — for a vendor that serves only the Responses API, not chat completions"], ["anthropic", "Anthropic compatible", "the root URL, what ANTHROPIC_BASE_URL would take"]]) {
      const b = el("button", "opt" + (draft.api === v ? " on" : ""), t(l));
      b.title = t(hint);
      b.onclick = () => {
        if (draft.api === v) return;
        draft[apiField[draft.api]] = "";
        draft.api = v;
        draft[apiField[v]] = url.value;
        for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b);
        slide(seg, "api");
        url.placeholder = v === "anthropic" ? "https://…" : "https://…/v1";
        fillEndpoints();
      };
      seg.append(b);
    }
    queueMicrotask(() => slide(seg, "api"));
    url = input(draft[apiField[draft.api]], draft.api === "anthropic" ? "https://…" : "https://…/v1", "url");
    url.oninput = () => { draft[apiField[draft.api]] = url.value; };
    const urlWrap = el("div", "stack");
    urlWrap.append(seg, url);
    ed.append(...field("Base URL", urlWrap));
  }

  if (p?.account) {
    // the sign-in belongs to the agent; magpie only borrows it
    const a = p.account;
    if (subOf(a.agent)) {
      ed.append(...field(t("Accounts"), renderAccounts(a), p.routing ? t("Tick every account to use; Routing says how requests spread over them.") : subOf(a.agent).single ? t("{agent} keeps one account; the gateway runs it for every request. Signing in to another replaces it.", { agent: a.agentName }) : subOf(a.agent).own ? t("The gateway uses the first. Tick more and it moves on to the next when the one before it is out of quota. {agent} itself stays signed in as it is.", { agent: a.agentName }) : t("{agent} signs in to the first. Tick more and the gateway moves on to the next when the one before it is out of quota. Sessions already running keep theirs until restarted.", { agent: a.agentName })));
      if ((a.logins || []).filter((l) => l.active || l.on).length > 1) ed.append(...renderRouting(p));
    } else {
      const acct = el("div", "acct");
      acct.append(icon(a.agentIcon), el("span", "n", a.user), el("span", "plan", accountPlan(a)));
      ed.append(...field(t("Account"), acct, t("{agent}'s sign-in, read from its own files. Sign out there and this provider goes away.", { agent: a.agentName })));
    }
    ed.append(...field(t("Models"), renderModels(p), ""));
    ed.append(...field(t("Fallback"), renderFallback(p), fallbackHint(p)));
    if (p.chat || p.responses || p.anthropic) ed.append(...field(t("Endpoints"), renderEndpoints(p, p)));
    const bar = el("div", "bar");
    // removing only hides it from magpie; the agent stays signed in
    const del = el("button", "text danger", t("Remove"));
    del.title = t("{agent} stays signed in; magpie just stops offering it", { agent: a.agentName });
    del.onclick = () => providerAction("delete", { id: p.id }, t("{name} removed", { name: p.name }));
    bar.append(del, el("span", "grow"));
    const cancel = el("button", "text", t("Cancel"));
    cancel.onclick = cancelEdit;
    const saveBtn = el("button", "text primary", t("Save"));
    saveBtn.onclick = () => { saveBtn.classList.add("busy"); providerAction("save", { id: p.id, models: draft.chosen, unlisted: draft.unlisted, fallback: draft.fallback }, t("{name} saved", { name: p.name })); };
    bar.append(cancel, saveBtn);
    ed.append(bar);
    return ed;
  }

  const key = input(draft.key || "", p?.key.set ? t("{masked} · paste a new key to replace it", { masked: p.key.masked }) : t(pr?.noKey || p?.key.optional ? "optional for local servers" : "paste an API key"), "password");
  key.oninput = () => { draft.key = key.value; };
  key.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter" && isNew) save(); else if (e.key === "Escape") cancelEdit(); };
  const side = el("div", "side");
  const eye = el("button", "text", t("Show"));
  let revealed = false; // the saved key is in the box, not a draft
  eye.onclick = async () => {
    if (key.type === "password") {
      if (!key.value && p?.key.set) {
        try { key.value = (await api("provider/key", { id: p.id })).key; revealed = true; } catch (e) { status(e.message, "err"); return; }
      }
      key.type = "text"; eye.textContent = t("Hide");
    } else {
      if (revealed && !draft.key) key.value = "";
      revealed = false;
      key.type = "password"; eye.textContent = t("Show");
    }
  };
  side.append(eye);
  const keysUrl = p?.keysUrl || pr?.keysUrl;
  if (keysUrl) { const b = el("button", "link", t("Get a key ↗")); b.onclick = () => api("open", { url: keysUrl }); side.append(b); }
  const keyWrap = el("div", "pair");
  keyWrap.append(key, side);
  if (p?.keyList?.length) ed.append(...field(t("Accounts"), renderKeyAccounts(p), p.routing ? t("Tick every key to use; Routing says how requests spread over them.") : t("Tick every key to use. Requests go to the first; when it runs out of quota or hits a rate limit, the next ticked key takes over.")));
  if (p?.keyList?.filter((k) => k.on).length > 1) ed.append(...renderRouting(p));
  else ed.append(...field(t("API key"), keyWrap, isNew ? t("Kept in ~/.config/magpie/providers.json, readable by you alone. Nothing is read from your shell.") : ""));

  // A user-defined provider can have its own picture; presets keep theirs.
  if (custom) ed.append(...field(t("Icon"), iconPicker(ed), ""));

  // Request headers of the user's own, for a preset's provider as much as a
  // custom one — which workspace a key is for, who the app is; signed-in
  // accounts returned above use the agent's own authentication headers.
  if (custom) {
    if (!draft.headers.length) draft.headers.push(["", ""]);
    ed.append(...field(t("Headers"), headerEditor(), t("Extra HTTP headers sent to the vendor, applied after auth. For gateways that need a private scheme.")));
  } else {
    ed.append(...field(t("Headers"), headerEditor(pr?.headerHints || []), t("Optional headers sent with every request to {p}, applied after auth.", { p: pr?.name || p?.name })));
  }

  // a vendor that tells the whole account's balance only to a token of its
  // own (AiHubMix's system access token), where a key knows just its own
  if (p?.balanceToken?.takes) {
    const tok = input(draft.balanceToken || "", p.balanceToken.set && !draft.clearBalanceToken ? t("saved · paste a new one to replace it") : t("optional · the account's system access token"), "password");
    tok.oninput = () => { draft.balanceToken = tok.value.trim(); };
    tok.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") cancelEdit(); };
    const pair = el("div", "pair");
    pair.append(tok);
    if (p.balanceToken.set && !draft.clearBalanceToken) {
      const side = el("div", "side");
      const drop = el("button", "text", t("Remove"));
      drop.onclick = () => { draft.clearBalanceToken = true; draft.balanceToken = ""; tok.value = ""; tok.placeholder = t("optional · the account's system access token"); drop.remove(); };
      side.append(drop);
      pair.append(side);
    }
    ed.append(...field(t("Account balance"), pair, t("A key tells only what is left on itself. For the whole account's balance on the Usage page, generate a System Access Token in {p}'s settings and paste it here; it is used for nothing else.", { p: pr?.name || p.name })));
  }

  // a relay that offers several regional endpoints, or a vendor whose plans
  // are served at their own: one selector, and the provider's base URLs follow it
  let refreshEndpoints = () => {};
  if (pr?.regions?.length) {
    const seg = el("div", "segs");
    const cur = pr.regions.find((r) => r.chat && r.chat === (draft.chat || pr.chat)) || pr.regions[0];
    for (const r of pr.regions) {
      const b = el("button", "opt" + (r.id === cur.id ? " on" : ""), t(r.name));
      b.onclick = () => {
        draft.chat = r.chat || ""; draft.responses = r.responses || ""; draft.anthropic = r.anthropic || "";
        for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b);
        slide(seg, "regions");
        refreshEndpoints();
      };
      seg.append(b);
    }
    queueMicrotask(() => slide(seg, "regions"));
    ed.append(...field(t(pr.regionLabel || "Region"), seg, t("which endpoint {p} is reached through", { p: pr.name })));
  }

  if (p) ed.append(...field(t("Models"), renderModels(p), ""));
  {
    // the window agents are told a model has, over what the vendor or
    // models.dev says: one for all of them, and model=size for one
    const cx = input(draft.contexts || "", t("e.g. 128k · or gpt-6=1m, comma separated"));
    cx.oninput = () => { draft.contexts = cx.value; };
    if (!decides) ed.append(...field(t("Context window"), cx, t("How long a request the models take, told to the agents; empty leaves it to the vendor and models.dev")));
  }
  if (p && !decides) ed.append(...field(t("Fallback"), renderFallback(p), fallbackHint(p)));
  else if (custom) {
    const ex = input(draft.extra.join(", "), t("model ids, comma separated · e.g. gpt-5.5, claude-sonnet-5"));
    ex.oninput = () => { draft.extra = ex.value.split(/[,\s]+/).filter(Boolean); };
    ed.append(...field(t("Models"), ex, t("Optional: magpie asks the vendor for its list after saving.")));
  }

  if (!custom) {
    const ebox = el("div");
    refreshEndpoints = () => {
      const base = p || pr || {};
      const src = { chat: draft.chat || base.chat || "", responses: draft.responses || base.responses || "", anthropic: draft.anthropic || base.anthropic || "", decide: base.decide || "" };
      ebox.replaceChildren(renderEndpoints(p, src));
    };
    refreshEndpoints();
    ed.append(...field(t("Endpoints"), ebox, ""));
  }

  if (custom) {
    const more = el("details", "more");
    more.append(el("summary", "", t("More endpoints")));
    const inner = el("div", "inner");
    // the other protocols' URLs, drawn again when the base URL's changes
    const eps = el("div");
    eps.style.display = "contents";
    fillEndpoints = () => {
      eps.replaceChildren();
      const add = (label, key, ph, hint) => {
        if (apiField[draft.api] === key) return;
        const i = input(draft[key], ph, "url");
        i.oninput = () => { draft[key] = i.value; };
        eps.append(...field(t(label), i, t(hint)));
      };
      add("OpenAI URL", "chat", "https://…/v1", "if the vendor also serves chat completions");
      add("Anthropic URL", "anthropic", "https://…", "if the vendor also serves Anthropic messages");
      add("Responses URL", "responses", "https://…/v1", "if the vendor serves the OpenAI Responses API (Codex uses it natively)");
    };
    fillEndpoints();
    inner.append(eps);
    const mu = input(draft.modelsURL, "https://…/v1/models", "url");
    mu.oninput = () => { draft.modelsURL = mu.value; };
    inner.append(...field(t("Models URL"), mu, t("Where the vendor lists its models, when that isn't under the base URL; asked with the key")));
    const cat = input(draft.catalog, t("models.dev ids, e.g. openai, deepseek"));
    cat.oninput = () => { draft.catalog = cat.value; };
    inner.append(...field(t("Catalog"), cat, t("Display names and reasoning levels for the models; for a gateway that serves several vendors, list them all, first match wins")));
    const bal = input(draft.balanceURL, "https://…/api/usage/token", "url");
    bal.oninput = () => { draft.balanceURL = bal.value; };
    inner.append(...field(t("Balance URL"), bal, t("Where the vendor tells what is left on the key, asked with it like a chat request; shown on the Usage page")));
    const balPath = input(draft.balancePath, "data.balance");
    balPath.oninput = () => { draft.balancePath = balPath.value; };
    inner.append(...field(t("Balance field"), balPath, t("Where the amount is in the reply, e.g. data.balance; it can be a sum with + - * / and brackets, e.g. data.total / 500000 or (1 - credits.used / 70) %; \"$\" in front adds the sign, \"%\" after it shows a percent; several, each with a label, go apart by \";\", e.g. 5h: a.used / a.cap %; $credits.left")));
    more.append(inner);
    ed.append(more);
  }

  const bar = el("div", "bar");
  if (p) {
    const del = el("button", "text danger", t("Remove"));
    del.onclick = () => providerAction("delete", { id: p.id }, t("{name} removed", { name: p.name }));
    bar.append(del);
  }
  if (p && pr) {
    // another key of the vendor, or the same key for another workspace
    const more = el("button", "text", t("Add another {name}", { name: pr.name }));
    more.title = t("One more {name} provider, with its own key, headers and models", { name: pr.name });
    more.onclick = () => { adding = true; editing = { preset: pr.id }; draft = null; renderProviders(); };
    bar.append(more);
  }
  bar.append(el("span", "grow"));
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelEdit;
  const saveBtn = el("button", "text primary", t(isNew ? "Add" : "Save"));
  const save = () => {
    // new: an Add never replaces a provider that has the id already
    const body = { id: p ? slug(draft.id) || p.id : draft.id, from: p?.id, name: draft.name, preset: draft.preset, key: draft.key || "", chat: draft.chat, responses: draft.responses, anthropic: draft.anthropic, catalog: draft.catalog, models: p ? draft.chosen : draft.extra, headers: headersOf(draft.headers), new: isNew };
    if (custom) { body.icon = draft.icon || "generic"; body.balanceURL = (draft.balanceURL || "").trim(); body.balancePath = (draft.balancePath || "").trim(); body.modelsURL = (draft.modelsURL || "").trim(); }
    if (p) { body.fallback = draft.fallback; body.unlisted = draft.unlisted; }
    const cx = parseContexts(draft.contexts || "");
    if (cx.error) return editorError(t("Context window: {v} is not a length like 128k or 1m", { v: cx.error }), "warn");
    body.contexts = cx.map;
    if (draft.balanceToken) body.balanceToken = draft.balanceToken;
    else if (draft.clearBalanceToken) body.clearBalanceToken = true;
    if (isNew && custom && !body.name) { name.focus(); return editorError(t("Give it a name"), "warn"); }
    if (isNew && custom && !body.chat && !body.anthropic && !body.responses) { url.focus(); return editorError(t("A base URL is needed"), "warn"); }
    editorError("");
    saveBtn.classList.add("busy");
    providerAction("save", body, t(isNew ? "{name} added" : "{name} saved", { name: draft.name || draft.id }));
  };
  saveBtn.onclick = save;
  bar.append(cancel, saveBtn);
  ed.append(bar);
  setTimeout(() => (isNew ? (custom || another ? name : key) : null)?.focus(), 0);
  return ed;
}

// contextsText is a provider's contexts as the editor shows them: the one
// for all its models first, then model=size.
function contextsText(cx) {
  if (!cx) return "";
  const size = (n) => n % 1e6 === 0 ? n / 1e6 + "m" : n % 1e3 === 0 ? n / 1e3 + "k" : String(n);
  const out = cx["*"] ? [size(cx["*"])] : [];
  for (const [id, n] of Object.entries(cx).sort()) if (id !== "*") out.push(id + "=" + size(n));
  return out.join(", ");
}

// parseContexts reads "128k, gpt-6=1m" back: sizes by model id, "*" for
// all; error is the first part that isn't a size.
function parseContexts(text) {
  const map = {};
  for (const part of text.split(/[,，\n]/).map((x) => x.trim()).filter(Boolean)) {
    const i = part.lastIndexOf("=");
    const id = i < 0 ? "*" : part.slice(0, i).trim(), v = (i < 0 ? part : part.slice(i + 1)).trim().toLowerCase().replace(/_/g, "");
    const m = /^(\d+(?:\.\d+)?)([km]?)$/.exec(v);
    if (!m || !id) return { error: part };
    const n = Math.round(parseFloat(m[1]) * (m[2] === "m" ? 1e6 : m[2] === "k" ? 1e3 : 1));
    if (n > 0) map[id] = n;
  }
  return { map };
}

// fetchImportIcon asks the server to download the vendor's own logo, named
// by the link. It swaps the header mark when it lands; a failure is silent
// (the generic outline stays), since the icon is decoration, not the deal.
function fetchImportIcon(p, head, ed) {
  const host = hostOf(p.iconUrl);
  const note = el("div", "hint", t("Fetching {host}’s icon…", { host: host || t("the vendor") }));
  ed.append(note);
  api("import/icon", { url: p.iconUrl }).then((r) => {
    p.icon = r.icon;
    delete p.iconUrl;
    const old = head.firstChild;
    const now = icon(p.icon);
    old ? old.replaceWith(now) : head.prepend(now);
    note.remove();
  }).catch(() => note.remove());
}

// renderImport: what a magpie://import link would add, for the user to
// check. Nothing is saved until they press Add; the key stays hidden unless
// they ask to see it.
// Providers other apps (CC Switch, Alma) have set up, for the user to pick
// from. magpie only reads those apps; the keys stay on the server side and
// the dialog sees them masked.
async function openImportApps() {
  importingApps = { loading: true, sources: [], picks: {} };
  renderProviders();
  try {
    const sources = await api("importapps");
    const picks = {};
    for (const s of sources) for (const it of s.items) {
      if (it.skip || it.status === "same") continue;
      picks[s.id + "\n" + it.ref] = { on: !it.off && (it.status !== "taken" || !!it.keyOf), mode: it.keyOf ? "key" : "add" };
    }
    if (!importingApps) return;
    importingApps = { sources, picks };
  } catch (e) {
    if (!importingApps) return;
    importingApps = { error: e.message, sources: [], picks: {} };
  }
  renderProviders();
}

// appIcon is an import source's logo; Claude Code has its mark among the
// vendor icons rather than an app tile of its own.
const appIcon = (id) => id === "claude-code" ? "icons/claudecode-color.svg" : id === "codex" ? "icons/codex-color.svg" : `icons/app-${id}.png`;

function renderImportApps(ia) {
  const ed = el("div", "editor new importapps");
  ed.onclick = (e) => e.stopPropagation();
  const h = el("div", "ehead");
  h.append(el("b", "", t("Import from other apps")));
  ed.append(h);
  const bar = el("div", "bar");
  const count = el("span", "note grow");
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelEdit;
  const go = el("button", "text primary", t("Import"));
  const recount = () => {
    const n = Object.values(ia.picks).filter((x) => x.on).length;
    count.textContent = n ? t("{n} selected", { n }) : "";
    go.disabled = !n;
  };
  bar.append(count, cancel, go);
  if (ia.loading) {
    ed.append(el("div", "appnote", t("Reading other apps…")), bar);
    go.disabled = true;
    return ed;
  }
  if (ia.error) {
    ed.append(el("div", "warnbox", ia.error), bar);
    go.disabled = true;
    return ed;
  }
  ed.append(el("div", "appnote", t("magpie reads these apps' settings and changes nothing in them. Pick the providers to bring over.")));
  // one tab per app magpie can import from, so which ones it can is plain
  // at a glance; each shows how many providers it has to bring over
  const tabs = el("div", "apptabs");
  tabs.setAttribute("role", "tablist");
  const list = el("div", "applist");
  const secs = {};
  const pickable = (s) => s.items.some((it) => ia.picks[s.id + "\n" + it.ref]);
  if (!ia.sources.some((s) => s.id === ia.tab)) {
    ia.tab = (ia.sources.find(pickable) || ia.sources.find((s) => s.found) || ia.sources[0])?.id;
  }
  const showTab = (id) => {
    ia.tab = id;
    for (const [sid, [tab, sec]] of Object.entries(secs)) {
      tab.classList.toggle("on", sid === id);
      tab.setAttribute("aria-selected", String(sid === id));
      sec.hidden = sid !== id;
    }
    list.scrollTop = 0;
  };
  for (const s of ia.sources) {
    const sec = el("div", "appsrc");
    const tab = el("button", "apptab" + (s.found && !s.error ? "" : " missing"));
    tab.setAttribute("role", "tab");
    const tlogo = el("img", "applogo");
    tlogo.src = appIcon(s.id);
    tlogo.alt = "";
    tlogo.draggable = false;
    const n = s.items.filter((it) => ia.picks[s.id + "\n" + it.ref]).length;
    tab.append(tlogo, el("span", "", s.name), el("span", "count", s.found && !s.error ? String(n) : "–"));
    tab.title = s.found ? (s.error || t(n === 1 ? "1 provider to bring over" : "{n} providers to bring over", { n })) : t("Not found on this computer");
    tab.onclick = () => showTab(s.id);
    tabs.append(tab);
    secs[s.id] = [tab, sec];
    const sh = el("div", "apphead");
    const logo = el("img", "applogo");
    logo.src = appIcon(s.id);
    logo.alt = "";
    logo.draggable = false;
    sh.append(logo, el("b", "", s.name), el("code", "", s.path.replace(/^\/Users\/[^/]+|^\/home\/[^/]+/, "~")));
    sec.append(sh);
    if (!s.found) sec.append(el("div", "appempty", t("Not found on this computer")));
    else if (s.error) sec.append(el("div", "appempty", s.error));
    else if (!s.items.length) sec.append(el("div", "appempty", t("No providers in it")));
    // everything this app has that can come over, on or off at once
    const mine = s.items.map((it) => ia.picks[s.id + "\n" + it.ref]).filter(Boolean);
    const boxes = [];
    const all = el("input");
    all.type = "checkbox";
    const allState = () => {
      const n = mine.filter((x) => x.on).length;
      all.checked = n > 0 && n === mine.length;
      all.indeterminate = n > 0 && n < mine.length;
    };
    const tick = () => { allState(); recount(); };
    for (const it of s.items) sec.append(importAppRow(ia, s, it, tick, boxes));
    if (mine.length) {
      const lab = el("label", "appall");
      all.onchange = () => {
        for (const x of mine) x.on = all.checked;
        for (const b of boxes) b.checked = all.checked;
        tick();
      };
      lab.append(all, el("span", "", t("Select all")));
      sh.append(lab);
      allState();
    }
    list.append(sec);
  }
  ed.append(tabs, list);
  showTab(ia.tab);
  go.onclick = async () => {
    const picks = [];
    for (const [k, v] of Object.entries(ia.picks)) {
      if (!v.on) continue;
      const [source, ref] = k.split("\n");
      picks.push({ source, ref, mode: v.mode });
    }
    go.classList.add("busy");
    try {
      const r = await api("importapps", { picks });
      providers = r.state;
      importingApps = null;
      adding = false;
      editing = null;
      draft = null;
      renderProviders();
      state = await api("state");
      renderAgents();
      status(t("Imported {n}: {names}", { n: r.added.length, names: r.added.join(", ") }), "ok");
    } catch (e) {
      go.classList.remove("busy");
      if (!editorError(e.message, "err")) status(e.message, "err");
    }
  };
  ed.append(bar);
  recount();
  return ed;
}

function importAppRow(ia, s, it, recount, boxes) {
  const p = it.provider;
  const pick = ia.picks[s.id + "\n" + it.ref];
  const row = el("label", "approw" + (pick ? "" : " dim"));
  const box = el("input");
  box.type = "checkbox";
  box.checked = !!pick?.on;
  box.disabled = !pick;
  box.onchange = () => { pick.on = box.checked; recount(); };
  if (pick) boxes.push(box);
  const who = el("div", "appwho");
  const name = el("div", "name");
  name.append(el("span", "", p.name || it.ref));
  if (it.from) name.append(el("span", "from", it.from));
  who.append(name);
  const bits = [];
  const host = hostOf(p.anthropic || p.chat || p.responses || "");
  if (it.skip) bits.push(t(it.skip));
  else {
    if (host) bits.push(host);
    if (p.key) bits.push(p.key);
    if (p.models?.length) bits.push(t(p.models.length === 1 ? "1 model" : "{n} models", { n: p.models.length }));
  }
  who.append(el("div", "sub", bits.join(" · ")));
  if (pick && it.off) who.append(el("div", "sub", t(it.off)));
  if (pick && (it.keyOf || it.status === "taken")) {
    const opts = [];
    if (it.keyOf) opts.push(["key", t("Add as another key")]);
    opts.push(["add", t(it.status === "taken" ? "Keep both" : "Add as a new provider")]);
    if (it.status === "taken") opts.push(["replace", t("Replace it")]);
    who.append(el("div", "sub", t("magpie has {name} already", { name: it.existing })));
    const sg = segs(opts, pick.mode, (m) => { pick.mode = m; if (!pick.on) { pick.on = box.checked = true; recount(); } });
    sg.onclick = (e) => e.preventDefault(); // a click on a choice is not a click on the checkbox
    who.append(sg);
  }
  let tag = null;
  if (it.status === "same") tag = el("span", "apptag", t("Already added"));
  else if (it.skip) tag = el("span", "apptag", t("Can't import"));
  else if (it.status === "taken") tag = el("span", "apptag", t("Name in use"));
  else if (it.keyOf) tag = el("span", "apptag", t("Same vendor"));
  row.append(box, icon(p.icon || "generic"), who);
  if (tag) row.append(tag);
  return row;
}

function renderImport(im) {
  const ed = el("div", "editor new import");
  ed.onclick = (e) => e.stopPropagation();
  const p = im.provider || {};
  const h = el("div", "ehead");
  h.append(icon(p.icon || "generic"), el("b", "", im.error ? t("Import link") : p.name));
  if (!im.error) h.append(el("span", "note", t("from a link")));
  ed.append(h);
  const bar = el("div", "bar");
  bar.append(el("span", "grow"));
  const cancel = el("button", "text", t(im.error ? "Close" : "Cancel"));
  cancel.onclick = cancelEdit;
  bar.append(cancel);
  if (im.error) {
    ed.append(el("div", "warnbox", t("This link can't be imported: {e}", { e: im.error })), bar);
    return ed;
  }

  const hosts = [...new Set([p.chat, p.responses, p.anthropic].filter(Boolean).map(hostOf))];
  ed.append(el("div", "warnbox", t("Added from a link. Your prompts and this key will go to {hosts}; add it only if you trust the site that sent you here.", { hosts: hosts.join(", ") })));

  const name = input(im.name ?? p.name, t("e.g. My Relay"));
  name.oninput = () => { im.name = name.value; };
  ed.append(...field(t("Name"), name));

  const key = input(im.key ?? p.key ?? "", t(p.key ? "" : "paste an API key"), "password");
  key.oninput = () => { im.key = key.value; };
  key.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") add(); else if (e.key === "Escape") cancelEdit(); };
  const side = el("div", "side");
  const eye = el("button", "text", t("Show"));
  eye.onclick = () => { const on = key.type === "password"; key.type = on ? "text" : "password"; eye.textContent = t(on ? "Hide" : "Show"); };
  side.append(eye);
  if (p.keysUrl && !p.key) { const b = el("button", "link", t("Get a key ↗")); b.onclick = () => api("open", { url: p.keysUrl }); side.append(b); }
  const keyWrap = el("div", "pair");
  keyWrap.append(key, side);
  ed.append(...field(t("API key"), keyWrap, p.key ? t("From the link. Kept in ~/.config/magpie/providers.json, readable by you alone.") : ""));

  ed.append(...field(t("Endpoints"), renderEndpoints(null, p), ""));
  if (p.models?.length) {
    const chips = el("div", "mchips");
    for (const m of p.models) chips.append(el("span", "mchip on", m));
    ed.append(...field(t("Models"), chips, ""));
  }
  if (im.replaces) ed.append(el("div", "warnbox soft", t("Replaces your {name}, key and all.", { name: im.replaces })));

  // The link may name the vendor's own logo — an explicit icon= wins over
  // whatever the catalog or preset gave. magpie fetches it here (the dialog
  // being open is the confirmation), once, quietly, and only ever into its
  // icons folder; the fallback mark stays when it fails.
  if (p.iconUrl) fetchImportIcon(p, h, ed);

  const addBtn = el("button", "text primary", t(im.replaces ? "Replace" : "Add"));
  const add = () => {
    const n = (im.name ?? p.name).trim();
    if (!n) { name.focus(); return status(t("Give it a name"), "warn"); }
    addBtn.classList.add("busy");
    providerAction("save", { ...p, name: n, key: (im.key ?? p.key ?? "").trim() }, t("{name} added", { name: n }));
  };
  addBtn.onclick = add;
  bar.append(addBtn);
  ed.append(bar);
  setTimeout(() => (p.key ? addBtn : key).focus(), 0);
  return ed;
}

// Which of the vendor's models the agents get to see: click to toggle, type
// to add one the vendor's list lacks, Refresh to ask the vendor again.
// The endpoints a provider serves, with a Test that reports against each one.
function renderEndpoints(p, src) {
  const eps = el("div", "eps");
  const slots = {};
  const urls = src || {};
  for (const [proto, label, hint] of PROTOS) {
    if (!urls[proto]) continue;
    const e = el("div", "ep");
    const pl = el("span", "pl", label);
    pl.title = t(hint);
    e.append(pl, el("code", "", urls[proto]), slots[proto] = el("span", "res"));
    eps.append(e);
  }
  if (p) {
    const test = el("button", "text action", t("Test"));
    test.title = t("Send a tiny request through each endpoint");
    test.onclick = async () => {
      test.classList.add("busy");
      for (const s of Object.values(slots)) { s.className = "res wait"; s.textContent = "…"; }
      try {
        const r = await api("provider/test", { id: p.id });
        for (const x of r.results) {
          const s = slots[x.protocol];
          if (!s) continue;
          s.className = "res " + (x.ok ? "ok" : "bad");
          s.replaceChildren();
          s.append(svg(x.ok ? CHECK : "M4.5 4.5l7 7M11.5 4.5l-7 7", 10, 2));
          s.append(el("span", "", x.ok ? `${x.ms} ms` : x.status ? `${x.status} · ${x.error}` : x.error));
          s.title = x.ok ? t("model {model}", { model: x.model }) : x.error;
        }
      } catch (e) { for (const s of Object.values(slots)) { s.className = "res"; s.textContent = ""; } status(e.message, "err"); }
      test.classList.remove("busy");
    };
    eps.append(test);
  }
  return eps;
}

function renderModels(p) {
  const box = el("div", "models");
  const chips = el("div", "mchips");
  const names = el("div", "mnames");
  const q = p.models.length > 24 ? input("", t("filter {n} models…", { n: p.models.length })) : null;
  const draw = () => {
    chips.replaceChildren();
    const f = (q?.value || "").trim().toLowerCase();
    let shown = 0;
    for (const m of p.models) {
      const on = draft.chosen.includes(m.id);
      if (f && !m.id.toLowerCase().includes(f) && !(m.name || "").toLowerCase().includes(f) && !(m.default || "").toLowerCase().includes(f) && !on) continue;
      const c = el("button", "mchip" + (on ? " on" : ""));
      c.append(el("span", "", m.name && m.name !== m.id ? m.name : m.id));
      if (m.default) c.title = `${m.id} · ${m.default}`;
      else if (m.name && m.name !== m.id) c.title = m.id;
      c.onclick = () => { draft.chosen = on ? draft.chosen.filter((x) => x !== m.id) : [...draft.chosen, m.id]; draw(); };
      chips.append(c);
      if (++shown >= 80 && !f) { chips.append(el("span", "hint", t("… {n} more, filter to find them", { n: p.models.length - shown }))); break; }
    }
    for (const id of draft.chosen) {
      if (p.models.some((m) => m.id === id)) continue;
      const c = el("button", "mchip on own");
      c.append(el("span", "", id));
      c.title = t("Added by hand");
      c.onclick = () => { draft.chosen = draft.chosen.filter((x) => x !== id); draw(); };
      chips.append(c);
    }
    if (!p.models.length && !draft.chosen.length) chips.append(el("span", "hint", t("The vendor's list is empty. Refresh, or type a model id.")));
    drawNames();
    why.textContent = p.decide ? t("Agents never see them: a routing group picks one as its classifier.")
      : draft.unlisted ? t("Agents don't see them: only the routing groups they are in use them.")
      : t(draft.chosen.length ? "Agents see the models picked." : "None picked: agents see the vendor's list, up to {n}.", { n: 24 });
  };
  // the names and reasoning levels of the models agents see: saved at once,
  // apart from the editor's Save, as they change nothing but what is shown
  const drawNames = () => {
    names.replaceChildren();
    names.hidden = naming !== p.id;
    if (names.hidden) return;
    const ids = draft.chosen.length ? draft.chosen : p.models.filter((m) => m.on).map((m) => m.id);
    if (!ids.length) { names.append(el("span", "hint", t("Pick a model first."))); return; }
    for (const id of ids) {
      const m = p.models.find((x) => x.id === id) || { id, name: id };
      const own = m.default || m.name || m.id;
      const row = el("div", "mname");
      const name = input(m.default ? m.name : "", own);
      name.title = t("The name agents and magpie show for {id}; empty for its own", { id: m.id });
      const save = () => {
        const v = name.value.trim();
        if (v === (m.default ? m.name : "")) return;
        accountAction("provider/name", { id: p.id, model: m.id, modelName: v }, v ? t("{id} is called {name}", { id: m.id, name: v }) : t("{id} has its own name again", { id: m.id }));
      };
      name.onchange = save;
      name.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") name.blur(); else if (e.key === "Escape") { name.value = m.default ? m.name : ""; name.blur(); } };
      const who = el("div", "mwho");
      who.append(name, el("code", "", m.id));
      row.append(who);
      const levels = m.efforts || [];
      if (levels.length > 1) {
        const lv = el("div", "mlevels");
        lv.title = t("Reasoning levels agents are offered");
        const kept = m.kept?.length ? m.kept : levels;
        for (const l of levels) {
          const [tk, cb] = tick(t(l), kept.includes(l));
          cb.onchange = () => {
            const next = levels.filter((x) => x === l ? cb.checked : kept.includes(x));
            if (!next.length) { cb.checked = true; status(t("Keep at least one level"), "err"); return; }
            accountAction("provider/efforts", { id: p.id, model: m.id, efforts: next.length === levels.length ? [] : next }, t("{id}: {levels}", { id: m.id, levels: next.map((x) => t(x)).join(", ") }));
          };
          lv.append(tk);
        }
        row.append(lv);
      }
      if (m.default || m.kept?.length) {
        const reset = el("button", "text action", t("Restore default"));
        reset.title = t("Its own name and every reasoning level it has");
        reset.onclick = async () => {
          reset.classList.add("busy");
          try { if (m.default) await api("provider/name", { id: p.id, model: m.id, modelName: "" }); }
          catch (e) { status(e.message, "err"); reset.classList.remove("busy"); return; }
          accountAction("provider/efforts", { id: p.id, model: m.id, efforts: [] }, t("{id} is as its provider has it again", { id: m.id }));
        };
        row.append(reset);
      }
      names.append(row);
    }
  };
  if (q) { q.oninput = draw; box.append(q); }
  box.append(chips, names);
  const foot = el("div", "mfoot");
  const add = input("", t("add a model id…"));
  add.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && add.value.trim()) { const id = add.value.trim(); if (!draft.chosen.includes(id)) draft.chosen.push(id); add.value = ""; draw(); }
    else if (e.key === "Escape") cancelEdit();
  };
  const refresh = el("button", "text action", t("Refresh"));
  refresh.title = t("Ask the vendor which models it serves");
  refresh.onclick = async () => {
    refresh.classList.add("busy");
    try {
      const r = await api("provider/models", { id: p.id });
      status(t("{p}: {n} models", { p: p.name, n: r.count }), "ok");
      const chosen = draft.chosen;
      await loadProviders();
      draft = draft || {};
      draft.chosen = chosen;
      renderProviders();
    } catch (e) { status(e.message, "err"); refresh.classList.remove("busy"); }
  };
  const rename = el("button", "text action" + (naming === p.id ? " on" : ""), t("Names & levels"));
  rename.title = t("Rename the models agents see, or offer fewer of their reasoning levels");
  rename.onclick = () => { naming = naming === p.id ? null : p.id; rename.classList.toggle("on", naming === p.id); drawNames(); };
  foot.append(add, refresh, rename);
  if (p.fetched) foot.append(el("span", "hint", t("vendor list · {when}", { when: p.fetched })));
  // a signed-in account's list, until the vendor gives one, is magpie's own
  else if (p.models.length) foot.append(el("span", "hint", t(p.account ? "magpie's list · Refresh asks the vendor" : p.decide ? "Jev's names · Refresh asks the vendor" : "from models.dev · Refresh asks the vendor")));
  if (p.fetched && !p.account) {
    // the fetched list stands in for the picks when none are made
    const forget = el("button", "text action", t("Forget"));
    forget.title = t("Drop the list fetched from the vendor; the models.dev one is used until Refresh");
    forget.onclick = async () => {
      forget.classList.add("busy");
      try { await api("provider/unfetch", { id: p.id }); const chosen = draft.chosen; await loadProviders(); draft.chosen = chosen; renderProviders(); }
      catch (e) { status(e.message, "err"); forget.classList.remove("busy"); }
    };
    foot.append(forget);
  }
  box.append(foot);
  const why = el("div", "hint");
  const [tk, cb] = tick(t("Only through routing groups"), !!draft.unlisted);
  cb.onchange = () => { draft.unlisted = cb.checked; draw(); };
  tk.title = t("Its models leave the list agents pick from; the routing groups they are in still use them");
  if (!p.decide) box.append(tk);
  box.append(why);
  draw();
  return box;
}

// renderRouting: how the gateway spreads requests over the keys or
// accounts a provider has on. It takes effect at once, like ticking one.
const ROUTINGS = [
  ["", "Smart", "The first takes requests while it has quota to spare; when it runs low, the one with the most left takes over. One out of credit sits out half an hour, one out of quota until it resets, one rate limited as long as the vendor asks, and one that fails a minute, longer each time it fails again."],
  ["order", "In order", "Requests go to the first; the next takes over when the one before runs out of quota, hits a rate limit or fails."],
  ["rotate", "In turn", "Each turn of a conversation goes to the next one, spreading the load evenly; the requests within a turn stay where it began, so the prompt cache holds, and one that fails is passed over while it rests."],
  ["usage", "Least used first", "Each request goes to the one used least: a subscription by the share of its allowance used, a key by the tokens it served in the last hours."],
];
function renderRouting(p) {
  const cur = ROUTINGS.find(([id]) => id === (p.routing || "")) || ROUTINGS[0];
  const pick = segs(ROUTINGS.map(([id, name]) => [id, t(name)]), cur[0], (routing) => {
    const r = ROUTINGS.find(([id]) => id === routing);
    accountAction("provider/route", { id: p.id, routing }, t("{name}: {routing}", { name: p.name, routing: t(r[1]) }));
  });
  return field(t("Routing"), pick, t(cur[2]));
}

// renderFallback: where requests go when this provider can't take them —
// out of quota, rate limited, overloaded or down — tried top to bottom.
function renderFallback(p) {
  const box = el("div", "fallback");
  const list = el("div", "fbl");
  const sugg = el("div", "mchips");
  const q = input("", t("add a model: filter, or type provider/model…"));
  const all = [];
  for (const o of providers.providers) {
    if (!o.ready) continue;
    for (const m of o.models) if (m.on) all.push({ id: o.id + "/" + m.id, label: o.name + " · " + (m.name || m.id), icon: o.icon });
  }
  let open = false;
  const add = (id) => { if (id && !draft.fallback.includes(id)) draft.fallback.push(id); q.value = ""; draw(); };
  const draw = () => {
    list.replaceChildren();
    draft.fallback.forEach((id, i) => {
      const known = all.find((x) => x.id === id);
      const row = el("div", "fbrow");
      row.append(el("span", "i", String(i + 1)), icon(known?.icon || "generic"), el("span", "n", known ? known.label : id), el("span", "grow"));
      if (!known) row.title = t("No provider serves {id} now; it is skipped", { id });
      if (i) {
        const up = el("button", "text", t("Up"));
        up.onclick = () => { draft.fallback.splice(i - 1, 0, draft.fallback.splice(i, 1)[0]); draw(); };
        row.append(up);
      }
      const rm = el("button", "text", t("Remove"));
      rm.onclick = () => { draft.fallback.splice(i, 1); draw(); };
      row.append(rm);
      list.append(row);
    });
    sugg.replaceChildren();
    if (!open && !q.value.trim()) return;
    const f = q.value.trim().toLowerCase();
    const hits = all.filter((x) => !draft.fallback.includes(x.id) && !x.id.startsWith(p.id + "/") && (x.id + " " + x.label).toLowerCase().includes(f));
    for (const x of hits.slice(0, 12)) {
      const c = el("button", "mchip");
      c.append(el("span", "", x.label));
      c.title = x.id;
      c.onmousedown = (e) => e.preventDefault(); // keep the box focused
      c.onclick = () => add(x.id);
      sugg.append(c);
    }
    if (!hits.length && f) sugg.append(el("span", "hint", f.includes("/") ? t("Enter adds {id}", { id: q.value.trim() }) : t("No model matches")));
  };
  q.onfocus = () => { open = true; draw(); };
  q.onblur = () => { open = false; draw(); };
  q.oninput = draw;
  q.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && q.value.trim()) add(q.value.trim().includes("/") ? q.value.trim() : sugg.querySelector(".mchip")?.title);
    else if (e.key === "Escape") cancelEdit();
  };
  box.append(list, q, sugg);
  draw();
  return box;
}
function fallbackHint(p) {
  return t("When {name} is out of quota, rate limited or down, a request goes to these instead, top first. It only happens before any of the reply is sent, and {name} then sits out a minute.", { name: p.name });
}

// ---------- subscriptions ----------
//
// A Claude or ChatGPT subscription is added here, not in a terminal: magpie
// opens the vendor's own sign-in in the browser, takes the account when it
// comes back, and lists it with the others — any of them one click from
// being the one in use.

const SUBS = [
  { agent: "claude", name: "Claude", icon: "claude-color", plans: "Pro · Max · Team" },
  { agent: "codex", name: "ChatGPT", icon: "openai", plans: "Plus · Pro · Business" },
  // cursor-agent keeps one account; signing in again replaces it
  { agent: "cursor", name: "Cursor", icon: "cursor", plans: "Pro · Ultra · Teams", single: true },
  // so does Grok Build
  { agent: "grok", name: "Grok (SuperGrok)", icon: "xai", plans: "SuperGrok · X Premium+", own: true },
  // signed in with GitHub's device code; the editors' own sign-in stays theirs
  { agent: "copilot", name: "Copilot", icon: "githubcopilot", plans: "Pro · Pro+ · Business", own: true },
  // Z.ai's GLM Coding Plan, signed in as ZCode does; ZCode's own account is read too
  { agent: "zcode", name: "ZCode (GLM Coding Plan)", icon: "zcode", plans: "Lite · Pro · Max", own: true },
  // devin's credentials.toml keeps one account too
  { agent: "devin", name: "Devin", icon: "devin", plans: "Pro · Enterprise", single: true },
  // Google's sign-ins; Gemini CLI's own account is read too
  { agent: "gemini", name: "Gemini CLI", icon: "geminicli-color", plans: "Code Assist Standard · Enterprise", own: true },
  { agent: "antigravity", name: "Antigravity", icon: "antigravity-color", plans: "Google AI Pro · Ultra · free", risk: true },
];
const subOf = (agent) => SUBS.find((x) => x.agent === agent);
let signing = null; // the sign-in under way: { id, agent, url, state, installing, error }
const signingOpen = () => signing?.state === "waiting" || signing?.state === "installing";
let justAdded = ""; // the account that just came in, to greet it

async function startSignIn(agent, risky) {
  if (signingOpen()) api("signin/" + signing.id + "/cancel", {}).catch(() => {});
  // an account Google may suspend is added only once that is said
  if (subOf(agent)?.risk && !risky) {
    signing = { agent, state: "risk" };
    renderProviders();
    return;
  }
  signing = { agent, state: "starting" };
  renderProviders();
  try {
    signing = await api("signin", { agent });
    renderProviders();
    followSignIn(signing.id);
  } catch (e) {
    signing = { agent, state: "failed", error: e.message };
    renderProviders();
  }
}

async function followSignIn(id) {
  while (signing?.id === id && signingOpen()) {
    await new Promise((r) => setTimeout(r, 800));
    let st;
    try { st = await api("signin/" + id); } catch { continue; }
    if (signing?.id !== id) continue;
    if (st.state === "waiting" || st.state === "installing") {
      // the CLI it needed is in: now the vendor's page can open
      if (signing.state === "installing" && st.state === "waiting" && st.url) api("open", { url: st.url }).catch(() => {});
      if (signing.state !== st.state || signing.url !== st.url) { signing = st; renderProviders(); }
      continue;
    }
    if (st.state === "done") {
      signing = null;
      justAdded = st.user;
      delete loginUsage[st.agent]; // what was fetched before has nothing on the new account
      providers = await api("providers");
      const p = providers.providers.find((x) => x.account?.agent === st.agent);
      if (p) { editing = p.id; draft = null; adding = false; presetQuery = ""; }
      renderProviders();
      status(st.using ? t("Signed in as {user}", { user: st.user }) : t("{user} added — switch to it any time", { user: st.user }), "ok");
      state = await api("state");
      renderAgents();
      setTimeout(() => { justAdded = ""; }, 2000);
      return;
    }
    signing = st.state === "canceled" ? null : st;
    renderProviders();
  }
}

function cancelSignIn() {
  if (signing?.id) api("signin/" + signing.id + "/cancel", {}).catch(() => {});
  signing = null;
  renderProviders();
}

// renderSigning: where a sign-in stands, in place of the button that
// started it — waiting on the browser, or what went wrong.
function renderSigning(sub) {
  const box = el("div", "signing" + (signing.state === "failed" ? " failed" : ""));
  const tt = el("span", "tt");
  if (signing.state === "risk") {
    box.append(el("span", "mark", "!"));
    tt.append(el("span", "n", t("{name} accounts can be suspended", { name: sub.name })),
      el("span", "s", t("Google may suspend an Antigravity account it sees used outside Antigravity. Use one you can afford to lose.")));
    box.append(tt);
    const go = el("button", "text primary", t("Sign in anyway"));
    go.onclick = () => startSignIn(sub.agent, true);
    const close = el("button", "text", t("Cancel"));
    close.onclick = cancelSignIn;
    box.append(close, go);
    return box;
  }
  if (signing.state === "failed") {
    box.append(el("span", "mark", "!"));
    tt.append(el("span", "n", t("Sign-in didn't finish")), el("span", "s", signing.error || ""));
    box.append(tt);
    const again = el("button", "text primary", t("Try again"));
    again.onclick = () => startSignIn(sub.agent, true);
    const close = el("button", "text", t("Cancel"));
    close.onclick = cancelSignIn;
    box.append(close, again);
    return box;
  }
  box.append(el("span", "spinner"));
  if (signing.state === "installing") {
    tt.append(el("span", "n", t("Installing {cli}…", { cli: signing.installing })),
      el("span", "s", t("{name} is used through its own CLI, which isn't on this computer yet. magpie is installing it with the official installer; the sign-in page opens as soon as it's done.", { name: sub.name })));
    box.append(tt);
    const x = el("button", "text", t("Cancel"));
    x.onclick = cancelSignIn;
    box.append(x);
    return box;
  }
  tt.append(el("span", "n", t("Finish signing in to {name} in your browser", { name: sub.name })),
    el("span", "s", signing.code ? t("magpie opened GitHub's device page. Enter this code there; the account shows up here as soon as you're done.") : t("magpie opened the sign-in page. The account shows up here as soon as you're done.")));
  if (signing.code) {
    const code = el("span", "devcode");
    code.append(el("code", "", signing.code), copyBtn(signing.code, t("Code")));
    tt.append(code);
  }
  box.append(tt);
  if (signing.url) {
    const acts = el("span", "acts");
    const open = el("button", "link", t("Open again"));
    open.onclick = () => api("open", { url: signing.url }).catch(() => {});
    const cp = el("button", "link", t("Copy link"));
    cp.onclick = () => copy(signing.url, t("Sign-in link"), cp);
    acts.append(open, cp);
    tt.append(acts);
  }
  const x = el("button", "text", t("Cancel"));
  x.onclick = cancelSignIn;
  box.append(x);
  return box;
}

// renderAccounts: every account of an agent magpie has, the one the agent
// is signed in to first, and a way to add another. Like keys, any number
// can be ticked: the gateway moves to the next ticked account when the
// first is out of quota. Each shows how much of its allowance is used, so
// which one to go to next is plain to see.
function renderAccounts(a) {
  const sub = subOf(a.agent);
  const list = el("div", "accts");
  let ls = a.logins?.length ? [...a.logins] : [{ user: a.user, plan: a.plan, active: true, on: true }];
  ls.sort((x, y) => (y.active ? 1 : 0) - (x.active ? 1 : 0));
  const several = ls.filter((l) => l.active || l.on).length > 1;
  const quota = loginUsageOf(a.agent);
  for (const l of ls) {
    const on = l.active || l.on;
    const row = el("div", "acc" + (on ? " in-use" : " off") + (l.user === justAdded ? " new" : ""));
    const dot = el("button", "dot tick");
    if (on) dot.append(svg(CHECK, 10, 2.2));
    if (l.active) {
      dot.title = sub?.own ? t("The gateway uses this account first") : t("{agent} is signed in to this account", { agent: a.agentName });
      dot.classList.add("fixed");
    } else {
      dot.title = on ? t("Stop using this account") : t("Use this account too");
      dot.onclick = () => accountAction("login/" + (on ? "off" : "on"), { agent: a.agent, user: l.user });
    }
    row.append(dot, el("span", "n", l.user), el("span", "plan", accountPlan({ agent: a.agent, plan: l.plan })), el("span", "grow"));
    if (l.active) {
      row.append(el("span", "using", several ? t("First") : t("In use")));
    } else {
      const forget = el("button", "text quiet", t("Remove"));
      forget.title = t("magpie forgets this account's sign-in; the account itself is untouched");
      forget.onclick = () => accountAction("login/forget", { agent: a.agent, user: l.user }, t("{user} removed", { user: l.user }));
      const use = el("button", "text", on ? t("Make first") : t("Use"));
      use.title = sub?.own ? t("The gateway uses this account first") : t("Sign {agent} in to this account", { agent: a.agentName });
      use.onclick = () => { use.classList.add("busy"); accountAction("login/switch", { agent: a.agent, user: l.user }, sub?.own ? t("The gateway now uses {user} first", { user: l.user }) : t("{agent} is now signed in as {user}", { agent: a.agentName, user: l.user })); };
      row.append(forget, use);
    }
    row.append(accountQuota(l.lapsed ? { [l.user]: { error: l.lapsed } } : quota, l.user));
    list.append(row);
  }
  if (signing?.agent === a.agent) list.append(renderSigning(sub));
  else {
    const add = el("button", "acc add");
    const ic = el("span", "dot");
    ic.append(svg(PLUS, 10, 1.8));
    add.append(ic, el("span", "n", t(sub.single ? "Sign in to another {name} account" : "Add another {name} account", { name: sub.name })));
    add.onclick = () => startSignIn(a.agent);
    list.append(add);
  }
  return list;
}

// Each account's allowance comes from the vendor and takes a moment, so it
// loads on its own and fills the rows in when it's there.
const loginUsage = {}; // agent → { at, data: { user: quota }, loading }
function loginUsageOf(agent) {
  const u = (loginUsage[agent] ||= {});
  if (!u.loading && !(Date.now() - (u.at || 0) < 60000)) {
    u.loading = api("login/usage?agent=" + agent)
      .then((d) => { u.data = d || {}; }, () => { u.data = u.data || {}; })
      .finally(() => {
        u.at = Date.now(); u.loading = null;
        if (providers?.providers.find((p) => p.id === editing)?.account?.agent === agent && !document.querySelector(".editor .rename-in, .editor input:focus")) renderProviders();
      });
  }
  return u.data;
}

// quotaError says why an allowance can't be read: a Google account with
// no Cloud project named can't be used at all until one is, so that is
// said outright; anything else is in the tooltip.
function quotaError(err) {
  if (/sign-in has expired/.test(err)) return t("Signed out — add this account again to use it");
  if (/no longer supported for Gemini Code Assist for individuals/.test(err)) return t("Google no longer serves personal accounts to Gemini CLI — hover for more");
  if (/magpie accounts project/.test(err)) return t("Needs a Google Cloud project — hover for how");
  if (/^Antigravity (hasn't set|won't serve)/.test(err)) return t("Antigravity hasn't set this account up — hover for why");
  if (/violation of Terms of Service/i.test(err)) return t("Google has suspended this account — hover for details");
  if (/access token is invalid or expired|didn't take the access token/.test(err)) return t("AiHubMix didn't take the access token — paste a new one in the provider's settings");
  if (/this key has no limit/.test(err)) return t("This key has no limit — add the account's access token in the provider's settings to see its balance");
  return t("Usage unavailable");
}

// accountQuota: an account's allowance as a line of small meters under its
// name, the reset time on the ones nearly used up.
function accountQuota(data, user) {
  const line = el("div", "aq");
  if (!data) {
    line.append(el("span", "skeleton sk-aq"), el("span", "skeleton sk-aq"));
    return line;
  }
  const q = data[user];
  if (!q || q.error || !q.windows?.length) {
    line.append(el("span", "aq-none", q?.error ? quotaError(q.error) : t("No usage reported")));
    if (q?.error) line.title = q.error;
    return line;
  }
  // the two rolling windows fit a line; the per-model ones go in its tooltip
  line.title = q.windows.slice(2).map((w) => t(w.name) + " " + quotaText(w)).join(" · ");
  for (const w of q.windows.slice(0, 2)) {
    const used = Math.max(0, Math.min(100, w.used));
    const m = el("span", "aq-w" + (used >= 90 ? " full" : ""));
    const track = el("span", "aq-track");
    const fill = el("i");
    fill.style.width = quotaFill(w) + "%";
    track.append(fill);
    m.append(el("span", "aq-n", t(w.name)), track, el("b", "", quotaText(w)));
    if (w.resetsAt) {
      const at = new Date(w.resetsAt);
      m.title = t("Resets {when}", { when: at.toLocaleString() });
      if (used >= 80) m.append(el("span", "aq-r", t("resets {in}", { in: untilText(at) })));
    }
    line.append(m);
  }
  return line;
}

// A window reads as how much of it is used, or — as the vendors' own apps
// show it — how much is left, the bar filling with that; one choice for
// every meter, kept for next time. The vendor's own count, where it gives
// one, stands before the percentage.
let quotaLeft = false;
try { quotaLeft = localStorage.getItem("magpie.quotaLeft") === "1"; } catch {}
function quotaFill(w) {
  const used = Math.round(Math.max(0, Math.min(100, w.used)));
  return quotaLeft ? 100 - used : used;
}
function quotaText(w) {
  const pct = t(quotaLeft ? "{n} left" : "{n} used", { n: quotaFill(w) + "%" });
  return w.display ? w.display + " · " + pct : pct;
}
function setQuotaLeft(on) {
  quotaLeft = on;
  try { localStorage.setItem("magpie.quotaLeft", on ? "1" : "0"); } catch {}
  renderQuotas();
}

function untilText(at) {
  const mins = Math.max(1, Math.round((at - Date.now()) / 60000));
  if (mins < 60) return t("in {n}m", { n: mins });
  const h = Math.round(mins / 60);
  if (h < 48) return t("in {n}h", { n: h });
  return t("in {n}d", { n: Math.round(h / 24) });
}

// accountAction changes which account a provider uses, or which it has,
// and leaves its editor open on the result.
async function accountAction(path, body, okMsg) {
  try {
    providers = await api(path, body);
    renderProviders();
    state = await api("state");
    renderAgents();
    if (okMsg) status(okMsg, "ok");
    return true;
  } catch (e) {
    if (!editorError(e.message, "err")) status(e.message, "err");
    document.querySelector(".editor .busy")?.classList.remove("busy");
    return false;
  }
}

// renderKeyAccounts: a key provider's accounts, one per key, the same list
// a subscription has. addingKey holds the half-typed new one.
let addingKey = null;
function renderKeyAccounts(p) {
  const list = el("div", "accts");
  const several = p.keyList.filter((k) => k.on).length > 1;
  for (const k of p.keyList) {
    const row = el("div", "acc" + (k.on ? " in-use" : " off") + (k.id === justAdded ? " new" : ""));
    // the dot is the switch: every key ticked is in use
    const dot = el("button", "dot tick");
    if (k.on) dot.append(svg(CHECK, 10, 2.2));
    dot.title = k.on ? t("Stop using this key") : t("Use this key too");
    dot.onclick = () => accountAction("keys/" + (k.on ? "off" : "on"), { id: p.id, ref: k.id });
    const name = el("button", "n rename" + (k.name ? "" : " mono"), k.name || k.masked);
    name.title = t("Rename");
    name.onclick = () => {
      const i = input(k.name, t("Name, e.g. Personal or Team"));
      i.className = "rename-in";
      const done = (save) => {
        if (save && i.value.trim() !== (k.name || "")) accountAction("keys/rename", { id: p.id, ref: k.id, name: i.value });
        else renderProviders();
      };
      i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") done(true); else if (e.key === "Escape") done(false); };
      i.onblur = () => done(true);
      name.replaceWith(i);
      i.focus();
    };
    row.append(dot, name);
    if (k.name) row.append(el("span", "plan mono", k.masked));
    const proto = protoPicker(p, k.protocol, (v) => accountAction("keys/protocol", { id: p.id, ref: k.id, protocol: v }));
    if (proto) row.append(proto);
    row.append(el("span", "grow"));
    if (!k.active || several) {
      const rm = el("button", "text quiet", t("Remove"));
      rm.onclick = () => accountAction("keys/remove", { id: p.id, ref: k.id }, t("Key removed"));
      row.append(rm);
    }
    if (k.active) row.append(el("span", "using", several ? t("First") : t("In use")));
    else if (k.on) {
      const first = el("button", "text", t("Make first"));
      first.onclick = () => { first.classList.add("busy"); accountAction("keys/use", { id: p.id, ref: k.id }, t("{name} tries {key} first", { name: p.name, key: k.name || k.masked })); };
      row.append(first);
    }
    list.append(row);
  }
  if (addingKey?.id === p.id) {
    const box = el("div", "acc adding");
    const name = input(addingKey.name, t("Name, e.g. Team"));
    name.oninput = () => { addingKey.name = name.value; };
    const key = input(addingKey.key, t("paste an API key"), "password");
    key.oninput = () => { addingKey.key = key.value; };
    const add = el("button", "text primary", t("Add"));
    const go = async () => {
      add.classList.add("busy");
      const id = await keyFingerprint(key.value.trim());
      if (await accountAction("keys/add", { id: p.id, name: name.value, key: key.value, protocol: addingKey.protocol || "" }, t("Key added — it takes over when the ones before it run out"))) {
        addingKey = null; justAdded = id; renderProviders(); setTimeout(() => { justAdded = ""; }, 2000);
      }
    };
    add.onclick = go;
    for (const i of [name, key]) i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") go(); else if (e.key === "Escape") { addingKey = null; renderProviders(); } };
    const x = el("button", "text", t("Cancel"));
    x.onclick = () => { addingKey = null; renderProviders(); };
    const fields = el("div", "kf");
    fields.append(name, key);
    const proto = protoPicker(p, addingKey.protocol || "", (v) => { addingKey.protocol = v; });
    const bar = el("div", "kb");
    if (p.keysUrl) { const g = el("button", "link", t("Get a key ↗")); g.onclick = () => api("open", { url: p.keysUrl }); bar.append(g); }
    if (proto) bar.append(proto);
    bar.append(el("span", "grow"), x, add);
    box.append(fields, bar);
    list.append(box);
    queueMicrotask(() => (addingKey.name ? key : name).focus());
  } else {
    const add = el("button", "acc add");
    const ic = el("span", "dot");
    ic.append(svg(PLUS, 10, 1.8));
    add.append(ic, el("span", "n", t("Add another key")));
    add.onclick = () => { addingKey = { id: p.id, name: "", key: "" }; renderProviders(); };
    list.append(add);
  }
  return list;
}

// protoPicker: which protocol a key works with. Some relays hand out one
// key for Anthropic and another for OpenAI; a key set to one is used on
// that endpoint only, and the gateway sends each request to the key that
// suits it. Only offered when the provider has more than one endpoint.
const PROTO_OPTS = [
  { v: "", pill: "Any protocol", name: "Any protocol", note: "Used on every endpoint" },
  { v: "anthropic", pill: "Anthropic", name: "Anthropic", note: "Messages API · Claude Code, Claude" },
  { v: "chat", pill: "Chat", name: "Chat Completions", note: "OpenAI API · GPT models, most agents" },
  { v: "responses", pill: "Responses", name: "Responses", note: "OpenAI Responses API · Codex" },
];

// protoPicker is the key row's protocol badge; clicking it drops a small menu
// that says what each choice is for.
function protoPicker(p, value, onChange) {
  value = value || "";
  const have = ["anthropic", "chat", "responses"].filter((x) => p[x]);
  if (have.length < 2 && !value) return null;
  const opts = PROTO_OPTS.filter((o) => !o.v || have.includes(o.v) || o.v === value);
  const pill = el("button", "proto" + (value ? " set" : ""));
  pill.type = "button";
  pill.title = t("Some relays give out a key per protocol. Set it here and the gateway sends each request to the key that fits: Claude models to the Anthropic key, GPT models to the OpenAI one.");
  const paint = () => pill.replaceChildren(el("span", "", t(PROTO_OPTS.find((o) => o.v === value).pill)), svg(CHEV, 11, 1.6));
  paint();
  pill.onclick = (e) => {
    e.stopPropagation();
    if (pill.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(pill, opts, value, (v) => {
      if (v === value) return;
      value = v;
      pill.classList.toggle("set", !!v);
      paint();
      onChange(v);
    });
  };
  return pill;
}

let protoMenu = null;
function closeProtoMenu() {
  if (!protoMenu) return;
  protoMenu.anchor.classList.remove("open");
  protoMenu.box.remove();
  document.removeEventListener("mousedown", protoMenu.outside, true);
  document.removeEventListener("keydown", protoMenu.keys, true);
  document.removeEventListener("scroll", closeProtoMenu, true);
  removeEventListener("resize", closeProtoMenu);
  protoMenu = null;
}
function openProtoMenu(anchor, opts, value, choose) {
  closeProtoMenu();
  const box = el("div", "pop proto-menu");
  box.setAttribute("role", "menu");
  box.append(el("div", "pm-head", t("Protocol this key speaks")));
  const items = opts.map((o) => {
    const b = el("button", "pm-item" + (o.v === value ? " on" : ""));
    b.type = "button";
    b.setAttribute("role", "menuitemradio");
    b.setAttribute("aria-checked", o.v === value);
    const tick = el("span", "pm-tick");
    if (o.v === value) tick.append(svg(CHECK, 12, 1.9));
    const words = el("span", "pm-words");
    words.append(el("span", "pm-name", t(o.name)), el("span", "pm-note", t(o.note)));
    b.append(tick, words);
    b.onclick = (e) => { e.stopPropagation(); closeProtoMenu(); choose(o.v); };
    b.onmouseenter = () => b.focus({ preventScroll: true });
    box.append(b);
    return b;
  });
  document.body.append(box);
  // under the pill, or above it when the window runs out
  const r = anchor.getBoundingClientRect(), w = box.offsetWidth, h = box.offsetHeight, pad = 8;
  let y = r.bottom + 5;
  if (y + h > innerHeight - pad && r.top - 5 - h >= pad) { y = r.top - 5 - h; box.classList.add("up"); }
  box.style.left = Math.max(pad, Math.min(r.left, innerWidth - w - pad)) + "px";
  box.style.top = Math.max(pad, y) + "px";
  anchor.classList.add("open");
  const outside = (e) => { if (!box.contains(e.target) && !anchor.contains(e.target)) closeProtoMenu(); };
  const keys = (e) => {
    const i = items.indexOf(document.activeElement);
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closeProtoMenu(); anchor.focus(); }
    else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault(); e.stopPropagation();
      const n = items.length, from = i < 0 ? (e.key === "ArrowDown" ? n - 1 : 0) : i;
      items[(from + (e.key === "ArrowDown" ? 1 : n - 1)) % n].focus();
    }
  };
  document.addEventListener("mousedown", outside, true);
  document.addEventListener("keydown", keys, true);
  document.addEventListener("scroll", closeProtoMenu, true); // it is pinned to the window; the dialog moving under it would strand it
  addEventListener("resize", closeProtoMenu);
  protoMenu = { box, anchor, outside, keys };
  (items.find((b) => b.classList.contains("on")) || items[0]).focus({ preventScroll: true });
}

// keyPill is a key provider's row badge: the key in use, or how many are.
function keyPill(p) {
  const on = (p.keyList || []).filter((k) => k.on);
  if (on.length > 1) return t("{n} keys", { n: on.length });
  return on[0]?.name || p.key.masked;
}

// keyFingerprint is the id the backend gives a key, to greet a new one.
async function keyFingerprint(key) {
  try {
    const h = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(key)));
    return [...h.slice(0, 5)].map((b) => b.toString(16).padStart(2, "0")).join("");
  } catch { return ""; }
}

// apiField is the draft's URL a custom provider's base URL fills, by the
// protocol chosen for it.
const apiField = { openai: "chat", responses: "responses", anthropic: "anthropic" };

async function providerAction(action, body, okMsg, base = "provider/") {
  try {
    providers = await api(base + action, body);
    editing = null;
    draft = null;
    importing = null;
    adding = false;
    presetQuery = "";
    renderProviders();
    state = await api("state");
    renderAgents();
    if (okMsg) status(okMsg, "ok");
  } catch (e) {
    if (!editorError(e.message, "err")) status(e.message, "err");
    document.querySelector(".editor .busy")?.classList.remove("busy");
  }
}

// editorError shows what went wrong inside the open provider editor, by its
// buttons, until the next edit or try; the page's status line sits behind
// the dialog. False when no editor is open.
function editorError(msg, kind = "err") {
  const ed = document.querySelector(".editor");
  if (!ed) return false;
  let box = ed.querySelector(".editor-error");
  if (!msg) { box?.remove(); return true; }
  if (!box) {
    box = el("div", "editor-error");
    box.setAttribute("role", "alert");
    const bar = ed.querySelector(":scope > .bar");
    bar ? bar.before(box) : ed.append(box);
    ed.addEventListener("input", () => box.remove(), { once: true });
  }
  box.className = "editor-error " + kind;
  box.textContent = msg;
  return true;
}

function slug(s) { return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, ""); }
function hostOf(u) { try { return new URL(u.includes("://") ? u : "https://" + u).host; } catch { return ""; } }

$("#addProvider").onclick = () => { adding = true; editing = null; draft = null; renderProviders(); };

// ---------- usage ----------

const PERIODS = [["today", "Today"], ["7d", "7 days"], ["30d", "30 days"], ["all", "All"]];

// The subscriptions' quotas come from the vendors and can take a while (or
// never come without a proxy), so they load on their own and the local log
// never waits for them. They don't depend on the period either.
let quotas = null;
async function loadUsage() {
  renderUsageLoading();
  loadQuotas();
  usage = await api("usage?period=" + period);
  renderUsage();
}

let quotasLoading = null;
function loadQuotas() {
  if (quotasLoading) return quotasLoading;
  quotasLoading = api("usage/quotas")
    .then((q) => { quotas = q || []; }, () => { quotas = quotas || []; })
    .finally(() => { quotasLoading = null; renderQuotas(); });
  return quotasLoading;
}

function renderUsageLoading() {
  const view = $("#view-usage");
  view.classList.add("loading");
  view.setAttribute("aria-busy", "true");
  const seg = $("#period");
  seg.replaceChildren();
  for (const [id, name] of PERIODS) {
    const b = el("button", "opt" + (id === period ? " on" : ""), t(name));
    b.disabled = true;
    seg.append(b);
  }
  slide(seg, "period");
  $("#usageCost").replaceChildren(el("span", "skeleton sk-cost"));
  renderQuotas();
  const stats = $("#stats");
  stats.classList.remove("empty");
  stats.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const tile = el("div", "kpi loading-kpi");
    tile.append(el("span", "skeleton sk-number"), el("span", "skeleton sk-label"));
    stats.append(tile);
  }
  $("#chart").hidden = true;
  for (const id of ["usageAgents", "usageModels"]) $("#" + id).hidden = true;
  for (const h of $$("#view-usage .row-head")) h.hidden = true;
  $("#usageNote").textContent = "";
}

function fmtN(n) {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e7) return Math.round(n / 1e6) + "M";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e5) return Math.round(n / 1e3) + "K";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}
function fmtCost(t) {
  if (!t.cost && t.unpriced) return "";
  const c = t.cost;
  const s = c >= 100 ? c.toFixed(0) : c >= 1 ? c.toFixed(2) : c.toFixed(3);
  return "$" + s + (t.unpriced ? "+" : "");
}
const tokensOf = (t) => t.input + t.output;

function renderQuotas() {
  const subscriptions = $("#subscriptionUsage");
  subscriptions.replaceChildren();
  // used or left: only there when some card has a window to read
  const mode = $("#quotaMode");
  mode.hidden = !quotas?.some((q) => !q.balance && !q.error && q.windows?.length);
  if (!mode.hidden) {
    mode.replaceChildren();
    for (const [left, name] of [[false, "Used"], [true, "Left"]]) {
      const b = el("button", "opt" + (left === quotaLeft ? " on" : ""), t(name));
      b.title = t(left ? "Show how much of each window is left" : "Show how much of each window is used");
      b.onclick = () => { if (left !== quotaLeft) setQuotaLeft(left); };
      mode.append(b);
    }
    slide(mode, "quotaMode");
  }
  if (!quotas) {
    subscriptions.hidden = false;
    for (let i = 0; i < 2; i++) {
      const card = el("div", "subscription-card skeleton-card");
      card.append(el("span", "skeleton sk-title"), el("span", "skeleton sk-line"), el("span", "skeleton sk-line short"));
      subscriptions.append(card);
    }
    return;
  }
  subscriptions.hidden = !quotas.length;
  // an agent with several accounts is one card, a section per account
  const groups = [];
  for (const sub of quotas) {
    const g = sub.user && groups.find((x) => x[0].user && x[0].provider === sub.provider);
    if (g) g.push(sub); else groups.push([sub]);
  }
  for (const subs of groups) {
    const first = subs[0];
    const card = el("div", "subscription-card" + (first.user ? " several" : ""));
    const head = el("div", "subscription-head");
    head.append(icon(first.icon), el("b", "", first.name));
    if (!first.user && first.plan) head.append(el("span", "plan", first.plan));
    card.append(head);
    for (const sub of subs) {
      if (sub.user) {
        const who = el("div", "subscription-account");
        const u = el("span", "user", sub.user);
        u.title = sub.user;
        who.append(u);
        if (sub.plan) who.append(el("span", "plan", sub.plan));
        card.append(who);
      }
      card.append(quotaWindows(sub));
    }
    subscriptions.append(card);
  }
}

// quotaWindows: one account's allowance as meters, or why there are none.
function quotaWindows(sub) {
  if (sub.balance) {
    const b = el("div", "quota-balance");
    b.append(el("span", "", t("Balance")), el("b", "", sub.balance));
    return b;
  }
  if (sub.error) {
    const e = el("div", "subscription-error", quotaError(sub.error));
    e.title = sub.error;
    return e;
  }
  const windows = el("div", "quota-windows");
  for (const w of sub.windows) {
    const quota = el("div", "quota");
    const labels = el("div", "quota-labels");
    const n = el("button", "quota-n", quotaText(w));
    n.title = quotaText(w) + "\n" + t(quotaLeft ? "Show how much of each window is used" : "Show how much of each window is left");
    n.onclick = () => setQuotaLeft(!quotaLeft);
    labels.append(el("span", "", t(w.name)), n);
    const track = el("div", "quota-track");
    const fill = el("i");
    fill.style.width = `${quotaFill(w)}%`;
    track.append(fill);
    quota.append(labels, track);
    if (w.resetsAt) quota.title = t("Resets {when}", { when: new Date(w.resetsAt).toLocaleString() });
    windows.append(quota);
  }
  return windows;
}

function renderUsage() {
  const u = usage;
  const view = $("#view-usage");
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  const seg = $("#period");
  seg.replaceChildren();
  for (const [id, name] of PERIODS) {
    const b = el("button", "opt" + (id === period ? " on" : ""), t(name));
    b.onclick = () => { for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(seg, "period"); period = id; loadUsage().catch((e) => status(e.message, "err")); };
    seg.append(b);
  }
  slide(seg, "period");
  const cost = $("#usageCost");
  cost.replaceChildren();
  const c = fmtCost(u);
  if (c) {
    cost.append(el("b", "", "≈" + c), el("span", "", t("list price")));
    cost.title = u.unpriced ? t(u.unpriced === 1 ? "{n} call had no known price and is not counted" : "{n} calls had no known price and are not counted", { n: u.unpriced }) : t("At each model's list price on models.dev");
  } else if (u.calls) {
    cost.append(el("span", "", t("no price for these models")));
  }

  const stats = $("#stats");
  stats.replaceChildren();
  const empty = !u.calls;
  $("#chart").hidden = empty;
  for (const id of ["usageAgents", "usageModels"]) $("#" + id).hidden = empty;
  for (const h of $$("#view-usage .row-head")) h.hidden = empty;
  if (empty) {
    stats.classList.add("empty");
    const none = { today: "No calls today.", "7d": "No calls in the last 7 days.", "30d": "No calls in the last 30 days.", all: "No calls yet." }[period];
    stats.append(el("div", "none", t(none) + " " + t("Point an agent at a catalog model and use it; every call through the gateway is counted here.")));
    $("#usageNote").textContent = "";
    return;
  }
  stats.classList.remove("empty");
  const tile = (n, label, sub, title) => {
    const t = el("div", "kpi");
    if (title) t.title = title;
    t.append(el("b", "", n), el("span", "", label));
    if (sub) t.append(el("small", "", sub));
    stats.append(t);
  };
  tile(fmtN(tokensOf(u)), t("tokens"), t("{a} in · {b} out", { a: fmtN(u.input), b: fmtN(u.output) }));
  // cache reads are billed at a fraction of input, so how much of the prompt
  // came from cache is the number that explains the bill; input here already
  // excludes the cached tokens (the gateway subtracts them). The written
  // count is secondary and only fits in the tooltip.
  const promptTokens = u.input + u.cache_read;
  const hit = u.cache_read && promptTokens ? t("hit rate {p}", { p: Math.round(100 * u.cache_read / promptTokens) + "%" }) : "";
  tile(fmtN(u.cache_read), t("cache read"), hit, u.cache_write ? t("{n} written", { n: fmtN(u.cache_write) }) : "");
  tile(fmtN(u.reasoning), t("reasoning"), t("inside output"));
  tile(String(u.calls), t(u.calls === 1 ? "call" : "calls"), u.errors ? t("{n} failed", { n: u.errors }) : "");

  // the timeline: one bar per hour, day or week; output sits on top of input
  const chart = $("#chart");
  chart.replaceChildren();
  const bars = el("div", "bars");
  const peak = Math.max(1, ...u.series.map(tokensOf));
  const labels = el("div", "labels");
  const n = u.series.length;
  const every = n <= 8 ? 1 : n <= 31 ? Math.ceil(n / 6) : Math.ceil(n / 5);
  u.series.forEach((p, i) => {
    const b = el("div", "bar");
    const inp = el("i", "in"), out = el("i", "out");
    inp.style.height = (100 * p.input / peak).toFixed(1) + "%";
    out.style.height = (100 * p.output / peak).toFixed(1) + "%";
    b.append(out, inp);
    const when = u.bucket === "hour" ? `${p.label}:00` : u.bucket === "week" ? t("week of {label}", { label: p.label }) : p.label;
    b.title = p.calls ? t(p.calls === 1 ? "{when} · {tokens} tokens · {n} call" : "{when} · {tokens} tokens · {n} calls", { when, tokens: fmtN(tokensOf(p)), n: p.calls }) + (fmtCost(p) ? " · ≈" + fmtCost(p) : "") : t("{when} · nothing", { when });
    bars.append(b);
    const last = i === n - 1 && (n - 1) % every >= every / 2;
    labels.append(el("span", "", i % every === 0 || last ? p.label : ""));
  });
  chart.append(el("div", "peak", fmtN(peak)), bars, labels);

  const total = Math.max(1, tokensOf(u));
  const list = (id, groups) => {
    const box = $("#" + id);
    box.replaceChildren();
    for (const g of groups) {
      const r = el("div", "row stat");
      r.append(icon(g.icon || "generic"));
      const who = el("div", "who");
      who.append(el("div", "name", g.name));
      const sub = [];
      if (g.sub) sub.push(g.sub);
      sub.push(t(g.calls === 1 ? "{n} call" : "{n} calls", { n: g.calls }));
      if (g.errors) sub.push(t("{n} failed", { n: g.errors }));
      who.append(el("div", "sub", sub.join(" · ")));
      r.append(who);
      const share = el("div", "share");
      const fill = el("i");
      fill.style.width = Math.max(1.5, 100 * tokensOf(g) / total).toFixed(1) + "%";
      share.append(fill);
      share.title = t("{n}% of tokens", { n: Math.round(100 * tokensOf(g) / total) });
      r.append(share);
      const num = el("div", "num");
      num.append(el("b", "", fmtN(tokensOf(g))), el("small", "", t("{a} in · {b} out", { a: fmtN(g.input), b: fmtN(g.output) }) + (g.cache_read ? " · " + t("{n} cached", { n: fmtN(g.cache_read) }) : "")));
      r.append(num);
      r.append(el("div", "cost", fmtCost(g) ? "≈" + fmtCost(g) : ""));
      box.append(r);
    }
  };
  list("usageAgents", u.agents);
  list("usageModels", u.models);
  $("#usageNote").textContent = t("Counted from the providers' own usage reports on every call through the gateway · {path}", { path: u.path });
}

// ---------- settings ----------
//
// Three choices (palette, language, what the tray icon opens) and the facts
// people come looking for:
// the version, where magpie keeps its files, the gateway's address.

const THEMES = [["system", "System"], ["light", "Light"], ["dark", "Dark"]];
const LOCALES = [["system", "System"], ["en", "English"], ["zh", "中文"]];
const TRAYS = [["panel", "Quick panel"], ["window", "Main window"]];

// applyPrefs paints and speaks as the saved settings say. A ?theme= or
// ?locale= in the URL wins, so a forced look stays forced.
function applyPrefs(s) {
  s = s || {};
  const root = document.documentElement;
  if (!params.get("theme")) {
    const want = !s.theme || s.theme === "system" ? undefined : s.theme;
    if (root.dataset.theme !== want) {
      if (applyPrefs.ready) { // not on the first paint
        root.classList.add("theming");
        clearTimeout(applyPrefs.t);
        applyPrefs.t = setTimeout(() => root.classList.remove("theming"), 450);
      }
      if (want) root.dataset.theme = want; else delete root.dataset.theme;
      if (applyPrefs.ready) tintPanel(450);
    }
  }
  applyPrefs.ready = true;
  const was = locale;
  setLocale(s.lang);
  if (was !== locale && mode === "window") queueMicrotask(() => slide($("#nav"), "nav"));
  return was !== locale;
}

async function loadSettings() {
  prefs = await api("settings");
  renderSettings();
}

const DISCORD_SVG = '<svg viewBox="0 0 24 24" width="13" height="13" fill="currentColor" aria-hidden="true"><path d="M20.317 4.3698a19.7913 19.7913 0 00-4.8851-1.5152.0741.0741 0 00-.0785.0371c-.211.3753-.4447.8648-.6083 1.2495-1.8447-.2762-3.68-.2762-5.4868 0-.1636-.3933-.4058-.8742-.6177-1.2495a.077.077 0 00-.0785-.037 19.7363 19.7363 0 00-4.8852 1.515.0699.0699 0 00-.0321.0277C.5334 9.0458-.319 13.5799.0992 18.0578a.0824.0824 0 00.0312.0561c2.0528 1.5076 4.0413 2.4228 5.9929 3.0294a.0777.0777 0 00.0842-.0276c.4616-.6304.8731-1.2952 1.226-1.9942a.076.076 0 00-.0416-.1057c-.6528-.2476-1.2743-.5495-1.8722-.8923a.077.077 0 01-.0076-.1277c.1258-.0943.2517-.1923.3718-.2914a.0743.0743 0 01.0776-.0105c3.9278 1.7933 8.18 1.7933 12.0614 0a.0739.0739 0 01.0785.0095c.1202.099.246.1981.3728.2924a.077.077 0 01-.0066.1276 12.2986 12.2986 0 01-1.873.8914.0766.0766 0 00-.0407.1067c.3604.698.7719 1.3628 1.225 1.9932a.076.076 0 00.0842.0286c1.961-.6067 3.9495-1.5219 6.0023-3.0294a.077.077 0 00.0313-.0552c.5004-5.177-.8382-9.6739-3.5485-13.6604a.061.061 0 00-.0312-.0286zM8.02 15.3312c-1.1825 0-2.1569-1.0857-2.1569-2.419 0-1.3332.9555-2.4189 2.157-2.4189 1.2108 0 2.1757 1.0952 2.1568 2.419 0 1.3332-.9555 2.4189-2.1569 2.4189zm7.9748 0c-1.1825 0-2.1569-1.0857-2.1569-2.419 0-1.3332.9554-2.4189 2.1569-2.4189 1.2108 0 2.1757 1.0952 2.1568 2.419 0 1.3332-.946 2.4189-2.1568 2.4189Z"/></svg>';

function renderSettings() {
  const s = prefs;
  const keep = { theme: s.theme, lang: s.lang, tray: s.tray, dock: !!s.dock, proxy: s.proxy || "",
    redact: !!s.redact, redactPersonal: !!s.redactPersonal, redactWords: s.redactWords || [] };
  $("#themeSegs").replaceChildren(segs(THEMES.map(([id, name]) => [id, t(name)]), s.theme, (theme) => savePrefs({ ...keep, theme })));
  $("#langSegs").replaceChildren(segs(LOCALES.map(([id, name]) => [id, t(name)]), s.lang, (lang) => savePrefs({ ...keep, lang })));
  $("#traySegs").replaceChildren(segs(TRAYS.map(([id, name]) => [id, t(name)]), s.tray || "panel", (tray) => savePrefs({ ...keep, tray })));
  // the Dock is the Mac's
  $("#dockRow").hidden = !document.body.classList.contains("mac");
  $("#dockSegs").replaceChildren(segs([["off", t("Hide")], ["on", t("Show")]], s.dock ? "on" : "off", (v) => savePrefs({ ...keep, dock: v === "on" })));
  renderProxy(s, keep);
  renderRedact(s, keep);
  renderSync();

  const about = $("#about");
  about.replaceChildren();
  const row = (name, sub, value, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name));
    if (sub) who.append(el("div", "sub", sub));
    const val = el("div", "val");
    if (value) val.append(el("code", "", value));
    val.append(...tools);
    r.append(who, val);
    about.append(r);
    return r;
  };
  renderUpdate(row(t("Version"), "", s.version));
  const open = el("button", "text", t("Open"));
  open.onclick = () => api("settings/reveal", {}).catch((e) => status(e.message, "err"));
  row(t("Config folder"), t("providers, profiles and these settings"), s.dir, copyBtn(s.dir, t("Path")), open);
  row(t("Gateway URL"), t("the address every agent is pointed at"), s.gateway, copyBtn(s.gateway, t("Gateway URL")));
  const join = el("button", "discord");
  join.innerHTML = DISCORD_SVG;
  join.append(el("span", "", t("Join Discord")));
  join.title = "discord.gg/vGSnD3ZKQF";
  join.onclick = () => api("open", { url: "https://discord.gg/vGSnD3ZKQF" }).catch(() => {});
  row(t("Community"), t("questions, ideas and feedback, on Discord"), "", join);
}

// renderSync: the Settings page's sync and backup — WebDAV keeping the
// setup the same on every computer, and a sealed file to carry by hand.
// One of the three opens a form below its row at a time.
let syncOpen = ""; // "dav" | "export" | "import"
let syncView = null;
async function renderSync(v) {
  const box = $("#syncList");
  if (v) syncView = v;
  else if (!syncView) {
    syncView = await api("davsync").catch(() => ({}));
  }
  v = syncView;
  box.replaceChildren();
  const row = (name, sub, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name));
    const s = el("div", "sub", sub);
    who.append(s);
    const val = el("div", "val");
    val.append(...tools);
    r.append(who, val);
    box.append(r);
    return s;
  };
  const btn = (label, fn, cls = "text") => { const b = el("button", cls, label); b.onclick = fn; return b; };
  const toggle = (id) => () => { syncOpen = syncOpen === id ? "" : id; renderSync(); };
  const parts = (ps) => ps.map((p) => t({ providers: "providers", settings: "settings", profiles: "profiles", agents: "agents' models" }[p])).join(t(", "));

  // WebDAV
  let status = t("Keeps providers, settings, profiles and agents' models the same on every computer");
  if (v.on) {
    const host = (() => { try { return new URL(v.url).host; } catch { return v.url; } })();
    status = v.error ? t("Couldn't sync: {error}", { error: v.error })
      : v.last ? t("Synced {when} · {host}", { when: syncWhen(v.last), host }) : t("Not synced yet · {host}", { host });
  }
  const sub = row(t("WebDAV sync"), status, ...(v.on
    ? [btn(t("Sync now"), async (e) => { e.target.classList.add("busy"); renderSync(await api("davsync/now", {}).catch((x) => ({ ...v, error: x.message }))); }),
       btn(t(syncOpen === "dav" ? "Close" : "Edit"), toggle("dav"))]
    : [btn(t(syncOpen === "dav" ? "Close" : "Set up"), toggle("dav"))]));
  if (v.error) sub.classList.add("bad");
  if (v.notice) {
    const n = v.notice, r = el("div", "row pref sync-note");
    const lines = [];
    if (n.here?.length) lines.push(t("Replaced here by newer ones from another computer: {parts}", { parts: parts(n.here) }));
    if (n.there?.length) lines.push(t("Replaced on the server by this computer's newer ones: {parts}", { parts: parts(n.there) }));
    const who = el("div", "who");
    for (const l of lines) who.append(el("div", "sub", l));
    who.append(el("div", "sub", t("The copies replaced are kept in the sync folder.")));
    const val = el("div", "val");
    val.append(btn(t("Show"), () => api("davsync/reveal", {}).catch((e) => status(e.message, "err"))), btn(t("OK"), async () => renderSync(await api("davsync/dismiss", {}))));
    r.append(who, val);
    box.append(r);
  }
  if (syncOpen === "dav") box.append(davForm(v));

  // export and import
  row(t("Export"), t("Everything above in one file, sealed with a passphrase, to carry to another computer"), btn(t(syncOpen === "export" ? "Close" : "Export…"), toggle("export")));
  if (syncOpen === "export") box.append(exportForm());
  row(t("Import"), t("Bring in a file exported from magpie"), btn(t(syncOpen === "import" ? "Close" : "Import…"), toggle("import")));
  if (syncOpen === "import") box.append(importForm());
}

// refreshAfterSync: what a sync or an import brought in reaches the other
// pages, leaving this one (and what it says was done) as it is.
function refreshAfterSync() {
  providers = null;
  api("state").then((s) => { state = s; renderAgents(); }).catch(() => {});
}

function syncWhen(iso) {
  const d = new Date(iso);
  const time = d.toLocaleTimeString(locale === "zh" ? "zh-CN" : undefined, { hour: "2-digit", minute: "2-digit" });
  return new Date().toDateString() === d.toDateString() ? t("at {time}", { time }) : d.toLocaleDateString(locale === "zh" ? "zh-CN" : undefined) + " " + time;
}

function tick(label, on) {
  const l = el("label", "tick");
  const c = el("input");
  c.type = "checkbox";
  c.checked = on;
  l.append(c, el("span", "", label));
  return [l, c];
}

function syncBar(ed, err, ...tools) {
  const bar = el("div", "bar");
  bar.append(...tools);
  ed.append(el("div", "editor-error", ""), bar);
  return (msg) => { ed.querySelector(".editor-error").textContent = msg || ""; };
}

function davForm(v) {
  const ed = el("div", "editor sync-form");
  const url = input(v.url || "", "https://dav.jianguoyun.com/dav/");
  const user = input(v.user || "", t("user name"));
  const pass = input("", v.passwordSet ? t("saved · type a new one to replace it") : t("password, or an app password"), "password");
  const phrase = input("", v.passphraseSet ? t("saved · type a new one to replace it") : t("the same on every computer"), "password");
  const [keysL, keys] = tick(t("Providers' API keys"), v.keys !== false);
  const [agentsL, agents] = tick(t("Agents' models"), v.agents !== false);
  const what = el("div", "stack");
  what.append(keysL, agentsL);
  ed.append(...field(t("Address"), url, t("A folder named magpie is made in it.")),
    ...field(t("User"), user),
    ...field(t("Password"), pass),
    ...field(t("Passphrase"), phrase, t("The file is sealed with it on this computer; the server only ever sees it sealed. Keep it: without it the file can't be opened.")),
    ...field(t("Also sync"), what));
  const save = el("button", "text primary", t(v.on ? "Save" : "Turn on"));
  const off = v.on ? el("button", "text danger", t("Turn off")) : el("span");
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", off, el("span", "grow"), cancel, save);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  off.onclick = async () => { syncOpen = ""; renderSync(await api("davsync/off", {}).catch(() => null) || undefined); };
  save.onclick = async () => {
    if (!v.passphraseSet && !phrase.value) return say(t("Pick a passphrase: the file is sealed with it"));
    save.classList.add("busy");
    try {
      const r = await api("davsync/save", { url: url.value.trim(), user: user.value.trim(), password: pass.value, passphrase: phrase.value, keys: keys.checked, agents: agents.checked });
      if (!r.error) syncOpen = "";
      renderSync(r);
      if (r.error) return;
      refreshAfterSync();
    } catch (e) {
      save.classList.remove("busy");
      say(e.message);
    }
  };
  return ed;
}

function exportForm() {
  const ed = el("div", "editor sync-form");
  const p1 = input("", t("passphrase"), "password");
  const p2 = input("", t("again"), "password");
  const [keysL, keys] = tick(t("With the providers' API keys"), true);
  ed.append(...field(t("Passphrase"), p1, t("Needed to open the file. Subscriptions aren't in it: sign in to them on the other computer.")),
    ...field("", p2), ...field("", keysL));
  const go = el("button", "text primary", t("Export"));
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", el("span", "grow"), cancel, go);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  go.onclick = async () => {
    if (!p1.value) return say(t("Pick a passphrase: the file is sealed with it"));
    if (p1.value !== p2.value) return say(t("The two passphrases differ"));
    go.classList.add("busy");
    try {
      const r = await api("backup/export", { pass: p1.value, keys: keys.checked });
      ed.replaceChildren(el("div", "done", t("Saved to {path}", { path: r.path })));
    } catch (e) {
      go.classList.remove("busy");
      say(e.message);
    }
  };
  return ed;
}

function importForm() {
  const ed = el("div", "editor sync-form");
  let data = "";
  const file = el("input");
  file.type = "file";
  file.accept = ".magpie-backup";
  file.hidden = true;
  const name = el("span", "fname", t("No file chosen"));
  const pick = el("button", "text", t("Choose…"));
  pick.onclick = () => file.click();
  file.onchange = async () => {
    const f = file.files[0];
    if (!f) return;
    // as base64 in JSON: the app's web view drops a File sent as the body
    const bytes = new Uint8Array(await f.arrayBuffer());
    let bin = "";
    for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    data = btoa(bin);
    name.textContent = f.name;
  };
  const pf = el("div", "pair");
  pf.append(name, pick, file);
  const pass = input("", t("passphrase"), "password");
  const [agentsL, agents] = tick(t("Set the agents' models too"), true);
  ed.append(...field(t("File"), pf), ...field(t("Passphrase"), pass), ...field("", agentsL, t("Providers with the same id are replaced; one that came without a key keeps the key it has here.")));
  const go = el("button", "text primary", t("Import"));
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", el("span", "grow"), cancel, go);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  go.onclick = async () => {
    if (!data) return say(t("Choose a file first"));
    go.classList.add("busy");
    try {
      const r = await api("backup/import", { data, pass: pass.value, agents: agents.checked });
      const lines = [t("Providers: {added} added, {replaced} replaced; {profiles} profiles; {agents} agent settings changed", { added: r.Added, replaced: r.Replaced, profiles: r.Profiles, agents: r.Agents })];
      if (r.NeedKey?.length) lines.push(t("Needs a key: {names}", { names: r.NeedKey.join(", ") }));
      ed.replaceChildren(...lines.map((l) => el("div", "done", l)));
      refreshAfterSync();
    } catch (e) {
      go.classList.remove("busy");
      say(e.message);
    }
  };
  return ed;
}

// renderProxy: magpie's own requests to vendors follow the system proxy on
// their own; this row says which one, and lets it be turned off or set.
let proxyCustom = false; // Custom picked, nothing typed yet
function renderProxy(s, keep) {
  const cur = !s.proxy ? "auto" : s.proxy === "direct" ? "off" : "custom";
  const mode = proxyCustom ? "custom" : cur;
  const sub = $("#proxySub");
  sub.textContent = {
    settings: t("Requests to vendors go through {proxy}", { proxy: s.proxyNow }),
    system: t("Following the system proxy, {proxy}", { proxy: s.proxyNow }),
    environment: t("Following HTTPS_PROXY, {proxy}", { proxy: s.proxyNow }),
    off: t("Off: requests to vendors go direct"),
    none: t("No system proxy found; requests to vendors go direct"),
  }[s.proxySource] || "";
  const box = $("#proxySegs");
  box.replaceChildren();
  const pick = (id) => {
    proxyCustom = id === "custom";
    if (id === "auto") savePrefs({ ...keep, proxy: "" });
    else if (id === "off") savePrefs({ ...keep, proxy: "direct" });
    else renderProxy(s, keep);
  };
  if (mode === "custom") {
    const i = input(cur === "custom" ? s.proxy : "", "http://127.0.0.1:7890");
    i.className = "proxy";
    const save = () => {
      const v = i.value.trim();
      if (!v || v === s.proxy) return;
      proxyCustom = false;
      savePrefs({ ...keep, proxy: v });
    };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save(); else if (e.key === "Escape") { proxyCustom = false; renderProxy(s, keep); } };
    i.onblur = save;
    box.append(i);
    if (proxyCustom) queueMicrotask(() => i.focus());
  }
  box.append(segs([["auto", t("Auto")], ["off", t("Off")], ["custom", t("Custom")]], mode, pick));
}

// renderRedact: what the gateway masks before a request goes to a vendor —
// secrets, personal data, the user's own words — and puts back in what the
// vendor answers.
function renderRedact(s, keep) {
  const box = $("#redactList");
  box.replaceChildren();
  const row = (name, sub, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name), el("div", "sub", sub));
    const val = el("div", "val");
    val.append(...tools);
    r.append(who, val);
    box.append(r);
  };
  const onOff = (on, fn) => segs([["off", t("Off")], ["on", t("On")]], on ? "on" : "off", (v) => fn(v === "on"));
  row(t("Mask secrets"), t("API keys, private keys, tokens and passwords go to vendors as placeholders, and come back as they were"),
    onOff(s.redact, (redact) => savePrefs({ ...keep, redact })));
  row(t("Mask personal data"), t("Emails, phone numbers, ID and bank card numbers too"),
    onOff(s.redactPersonal, (redactPersonal) => savePrefs({ ...keep, redactPersonal })));
  const words = (s.redactWords || []).join(", ");
  const i = input(words, t("names, codenames, hosts"));
  i.className = "words";
  const save = () => {
    const v = i.value.split(/[,，\n]/).map((w) => w.trim()).filter(Boolean);
    if (v.join(", ") === words) return;
    savePrefs({ ...keep, redactWords: v });
  };
  i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save(); else if (e.key === "Escape") { i.value = words; i.blur(); } };
  i.onblur = save;
  row(t("Masked words"), t("Your own words to keep from vendors, separated by commas"), i);
}

// renderUpdate fills in the version row: whether a newer magpie is out.
// The app checks and downloads on its own, so usually the row just offers
// the restart; a check can also be asked for.
async function renderUpdate(r, u) {
  u = u || await api("update").catch(() => null);
  if (!u || !r.isConnected) return;
  const who = r.querySelector(".who"), val = r.querySelector(".val");
  const sub = who.querySelector(".sub") || who.appendChild(el("div", "sub"));
  sub.title = "";
  for (const b of val.querySelectorAll("button")) b.remove();
  const btn = (label, fn) => { const b = el("button", "text", label); b.onclick = fn; val.append(b); };
  const check = async () => {
    sub.textContent = t("Checking for updates…");
    renderUpdate(r, await api("update/check", {}).catch((e) => ({ state: "error", error: e.message })));
  };
  switch (u.state) {
    case "ready":
      sub.textContent = t("{v} is downloaded", { v: u.latest }) + (u.error ? " · " + u.error : "");
      // back with an answer only when it didn't restart
      btn(t("Restart to update"), async () => renderUpdate(r, await api("update/install", {}).catch(() => null) || undefined));
      break;
    case "available":
      sub.textContent = t("{v} is out", { v: u.latest });
      if (u.stuck) sub.textContent += " · " + updateStuck(u);
      btn(t("Download"), () => api("update/install", {}));
      break;
    case "downloading":
      sub.textContent = t("Downloading {v}…", { v: u.latest });
      if (u.total) sub.textContent += " " + Math.floor((u.done / u.total) * 100) + "% · " + t("{done} of {total} MB", { done: (u.done / 1e6).toFixed(1), total: (u.total / 1e6).toFixed(1) });
      else if (u.done) sub.textContent += " " + t("{done} MB", { done: (u.done / 1e6).toFixed(1) });
      setTimeout(() => renderUpdate(r), 700);
      break;
    case "checking":
      sub.textContent = t("Checking for updates…");
      setTimeout(() => renderUpdate(r), 1000);
      break;
    case "latest":
      sub.textContent = t("Up to date");
      btn(t("Check"), check);
      break;
    case "error":
      // the reason in sight: "timed out" says try a proxy, a 404 says wait
      sub.textContent = t(u.latest ? "Couldn't download {v}" : "Couldn't check for updates", { v: u.latest }) + (u.error ? " · " + u.error.replace(/^Get "[^"]*": /, "") : "");
      sub.title = u.error || "";
      btn(t("Check"), check);
      break;
    default: // built from source, or not asked yet
      sub.textContent = "";
  }
}

async function savePrefs(body) {
  try {
    prefs = await api("settings", body);
    state.settings = prefs;
    const spoke = applyPrefs(prefs);
    renderSettings();
    if (spoke) { renderAgents(); providers = null; usage = null; }
    status(t("Saved"), "ok", 1500);
  } catch (e) {
    status(e.message, "err");
  }
}

// ---------- header / footer ----------

// ---------- where the reader is ----------
// Each view stays scrolled where the reader put it. Only the reader moves it
// — the wheel or trackpad, a touch, the keys that scroll, Tab, a drag — or
// code that says so first with scrollOnPurpose(). Anything else that moves
// it is put back before it's painted: a part of the page redrawn, and
// measured while it was briefly shorter, pulls the page up to what was left
// of it (WebKit has no scroll anchoring), and WebKit scrolls a field it
// focuses to the middle of the view. Never set a view's scrollTop, or
// scrollIntoView inside one, without scrollOnPurpose().
let purposeUntil = 0;
function scrollOnPurpose(ms = 1000) { purposeUntil = Math.max(purposeUntil, performance.now() + ms); }
const SCROLL_KEYS = new Set(["PageUp", "PageDown", "Home", "End", "ArrowUp", "ArrowDown", " "]);
addEventListener("wheel", () => scrollOnPurpose(250), { capture: true, passive: true });
addEventListener("touchmove", () => scrollOnPurpose(250), { capture: true, passive: true });
addEventListener("pointermove", (e) => { if (e.buttons) scrollOnPurpose(250); }, { capture: true, passive: true });
addEventListener("keydown", (e) => {
  const typing = e.target.closest?.("input, textarea, select, [contenteditable]");
  if (e.key === "Tab" || (!typing && SCROLL_KEYS.has(e.key))) scrollOnPurpose(400);
}, true);
const readerAt = new WeakMap();
function backToReader(v) {
  if (v.hidden) return;
  const want = Math.min(readerAt.get(v) || 0, Math.max(0, v.scrollHeight - v.clientHeight));
  if (Math.abs(v.scrollTop - want) < 1) return;
  v.scrollTop = want;
  // a field focused out of sight still comes into view, no further than needed
  const f = document.activeElement;
  if (f && f !== document.body && v.contains(f)) {
    const r = f.getBoundingClientRect(), b = v.getBoundingClientRect();
    if (r.bottom > b.bottom || r.top < b.top) { scrollOnPurpose(); f.scrollIntoView({ block: "nearest" }); }
  }
}
for (const v of document.querySelectorAll(".view")) {
  v.addEventListener("scroll", () => {
    if (v.hidden) return;
    if (performance.now() < purposeUntil) readerAt.set(v, v.scrollTop);
    else backToReader(v);
  }, { passive: true });
}

function show(v) {
  view = v;
  if (mode === "window") { for (const b of $("#nav").querySelectorAll("button")) b.classList.toggle("on", b.dataset.view === v); slide($("#nav"), "nav"); }
  $("#prefs").classList.toggle("on", v === "settings");
  for (const id of ["agents", "providers", "gateway", "routing", "usage", "library", "settings"]) $("#view-" + id).hidden = v !== id;
  // back to where the reader was in it, and again once it has what it loads
  const back = () => backToReader($("#view-" + v));
  requestAnimationFrame(back);
  closePicker();
  if (v !== "providers" && editing !== null) cancelEdit();
  if (v === "providers" || v === "gateway" || v === "routing") loadProviders().then(back, (e) => status(e.message, "err"));
  if (v === "usage") loadUsage().then(back, (e) => status(e.message, "err"));
  if (v === "settings") loadSettings().then(back, (e) => status(e.message, "err"));
  if (v === "library") window.loadLibrary?.()?.then(back);
  syncURL();
}

// The tab, and the provider open in it, are kept in the address so a
// reload comes back to them.
function syncURL() {
  if (mode !== "window") return;
  const q = new URLSearchParams(location.search);
  if (view === "agents") q.delete("view"); else q.set("view", view);
  if (view === "providers" && typeof editing === "string") q.set("edit", editing); else q.delete("edit");
  const s = q.size ? "?" + q : location.pathname;
  if (s !== location.search) history.replaceState(null, "", s);
}
if (mode === "window") for (const b of $("#nav").querySelectorAll("button")) b.onclick = () => { show(b.dataset.view); b.blur(); };
$("#prefs").onclick = () => { if (mode === "window") show("settings"); else api("window/main?view=settings", {}); $("#prefs").blur(); };

$("#sync").onclick = async () => {
  const b = $("#sync");
  if (b.classList.contains("spin")) return;
  b.classList.add("spin");
  try {
    state = await api("sync", {});
    renderAgents();
    if (providers) await loadProviders();
    status(t("Model lists refreshed"), "ok");
  } catch (e) {
    status(t("Sync failed: {e}", { e: e.message }), "err");
  } finally {
    // stop at the end of a turn, not wherever the reply caught it (#16)
    const svg = b.querySelector("svg");
    if (svg.getAnimations().length) svg.addEventListener("animationiteration", () => b.classList.remove("spin"), { once: true });
    else b.classList.remove("spin");
  }
};
$("#open").onclick = () => api("window/main", {});
$("#openMain").onclick = () => api("window/main", {});
$("#quit").onclick = () => api("window/quit", {});
$("#winclose").onclick = () => winRuntime.then((w) => w?.Window.Close()); // hides it: the tray stays
$(".top").addEventListener("dblclick", (e) => {
  if (document.body.classList.contains("linux") && !e.target.closest("button, nav")) winRuntime.then((w) => w?.Window.ToggleMaximise());
});
if (mode === "window") { $("#open").remove(); $("#openMain").remove(); $("#quit").remove(); }
else { $("#nav").remove(); }
if (mode !== "window" || !document.body.classList.contains("linux")) $("#winclose").remove();

// Config files may change underneath us (another magpie, an editor); reload when
// the panel comes back into view.
// The magpie in the corner flaps and wags its tail as the window opens and
// when the pointer comes over it.
function wag() {
  const logo = document.querySelector(".brand .logo");
  if (!logo || logo.classList.contains("wag") || matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  logo.classList.add("wag");
  logo.querySelector(".tail").addEventListener("animationend", () => logo.classList.remove("wag"), { once: true });
}
document.querySelector(".brand")?.addEventListener("mouseenter", wag);
setTimeout(wag, 250);

// A narrow window has no room for the whole header: the name goes, leaving
// the magpie, and Update becomes its arrow; narrower still, the tabs stop
// centring and take the room between.
function fitTop() {
  const top = $(".top"), nav = $("#nav"), brand = $(".brand"), actions = $(".actions");
  const fits = () => {
    const a = actions.getBoundingClientRect();
    const left = brand.offsetParent ? brand.getBoundingClientRect().right
      : top.getBoundingClientRect().left + parseFloat(getComputedStyle(top).paddingLeft);
    if (!nav?.offsetParent) return left + 8 <= a.left;
    const n = nav.getBoundingClientRect();
    return left + 8 <= n.left && n.right + 8 <= a.left;
  };
  top.classList.remove("tight", "cramped");
  if (fits()) return;
  top.classList.add("tight");
  if (!fits()) top.classList.add("cramped");
}
const topFit = new ResizeObserver(fitTop);
for (const e of [".top", ".brand", ".actions"]) topFit.observe($(e));
// the nav's tabs change size after its thumb was put under one — the header
// tightening, the fonts arriving, another language — so it's put there again
if (mode === "window") new ResizeObserver(() => {
  const th = $("#nav > .thumb"), on = $("#nav > .on");
  if (!th || !on) return;
  th.classList.add("still");
  th.style.transform = `translateX(${on.offsetLeft}px)`;
  th.style.width = on.offsetWidth + "px";
  thumbs.set("nav", { x: on.offsetLeft, w: on.offsetWidth, at: 0 });
  requestAnimationFrame(() => th.classList.remove("still"));
}).observe($("#nav"));
document.fonts?.ready.then(fitTop);

document.addEventListener("visibilitychange", () => { if (!document.hidden) { load(); wag(); } });
// an agent's config can be rewritten, or the agent run round magpie, while the
// window is up: ask what drifted now and then, and redraw only on a change —
// never under an open menu
setInterval(async () => {
  if (document.hidden || !state?.agents || document.querySelector(".pop:not([hidden])")) return;
  let drift;
  try { drift = await api("drift"); } catch { return; }
  let changed = false;
  for (const a of state.agents) {
    const d = drift[a.id] || undefined;
    if (JSON.stringify(d) !== JSON.stringify(a.drift)) { a.drift = d; changed = true; }
  }
  if (changed) renderAgents();
}, 15000);
window.addEventListener("focus", load);
setInterval(renderUpdateBadge, 15 * 60 * 1000); // a window left open still hears of a new version
// Opened on a magpie://import link: fetch what it describes (once — the
// id is spent) and ask before adding it.
if (mode === "window" && params.get("import")) {
  const id = params.get("import");
  params.delete("import");
  history.replaceState(null, "", "?" + params);
  api("import/" + encodeURIComponent(id)).then((im) => {
    importing = im;
    if (providers && view === "providers") renderProviders();
  }).catch(() => {});
}
if (mode === "window" && params.get("view") === "providers" && params.get("edit")) editing = params.get("edit");
if (mode === "window" && ["providers", "gateway", "routing", "usage", "library", "settings"].includes(params.get("view"))) show(params.get("view"));
else if (mode === "window") slide($("#nav"), "nav");
load();
