// magpie — one state object per view, rendered into a list. No framework.
const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);
const params = new URLSearchParams(location.search);
const mode = params.get("mode") || "window";
document.body.classList.add(mode);
// `magpie web`: the page in a browser tab, with no window of the app's
// around it — it opens links itself, and what is the desktop's is left out
const web = !!window.bootPrefs?.web;
if (web) document.body.classList.add("web");
// The Mac window draws its title bar inside the page (the traffic lights);
// on Linux the page's header is the whole title bar (plainTitlebar), so it
// has the name, the close button and a double-click to maximise.
if (!web && /^Mac/.test(navigator.platform)) document.body.classList.add("mac");
if (!web && /^Linux/.test(navigator.platform)) document.body.classList.add("linux");
// Windows: its own UI faces by name, Chinese in Microsoft YaHei UI rather
// than whatever the webview falls back to for it
if (/^Win/.test(navigator.platform)) document.documentElement.classList.add("win");
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
  document.documentElement.style.setProperty("--zoom", b.web ? 1 : (b.textSize || 100) / 100);
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
let connectFolded = false; // Connect folded away under its heading
try { connectFolded = localStorage.getItem("magpie.gwConnectFolded") === "1"; } catch {}
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
  if (web && path === "open") { window.open(body.url, "_blank", "noopener"); return null; }
  const res = await fetch("/api/" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return null;
  // a body that isn't JSON (a proxy's or a plain http.Error) is the error
  // itself, not WebKit's "did not match the expected pattern"
  const text = await res.text();
  let data;
  try { data = text ? JSON.parse(text) : null; } catch {
    throw new Error(text.trim().slice(0, 200) || `${res.status} ${res.statusText}`);
  }
  if (!res.ok) throw new Error(data?.error || `${res.status} ${res.statusText}`);
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
const OUT = "M6.5 3.5h-3v9h9v-3M9 3.5h3.5V7M12.5 3.5 7.5 8.5";
const COPY_ICON = "M5.5 5.5V3.5h7v7h-2M3.5 5.5h7v7h-7z";
const PUZZLE = "M6.2 2.8h2.3a1.3 1.3 0 1 1 2.5 0h2.2V5a1.3 1.3 0 1 0 0 2.6v5.6H3V7.6a1.3 1.3 0 1 1 0-2.6V2.8z";

// A brand icon: colour logos are images, mono logos take the text colour.
// Nothing is ever invented: a model with no known vendor keeps the slot
// empty, and a custom provider shows a plain outline ("generic").
// A page redrawn whole (the Providers page, as a dialog opens or closes)
// hands its icons to keptIcons first and the new rows take them back: a
// logo made afresh loads its picture again and blinks out for a moment.
let keptIcons = null;
function keepIcons(...roots) {
  keptIcons = new Map();
  for (const r of roots) for (const e of r.querySelectorAll(".ic[data-icon]")) {
    if (!keptIcons.has(e.dataset.icon)) keptIcons.set(e.dataset.icon, []);
    keptIcons.get(e.dataset.icon).push(e);
  }
}
function icon(name) {
  const kept = keptIcons?.get(name || "")?.shift();
  if (kept) { kept.removeAttribute("title"); return kept; }
  const e = el("span", "ic");
  e.dataset.icon = name || "";
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
    if (name.endsWith("-color") || name === "crush" || name === "zcode" || name === "alma" || name === "hanako" || name === "cindy" || name === "typesafe") {
      const img = el("img");
      img.src = `icons/${name}.${name === "crush" || name === "zcode" || name === "alma" || name === "hanako" || name === "cindy" || name === "typesafe" ? "png" : "svg"}`;
      img.alt = "";
      img.draggable = false;
      e.append(img);
    } else {
      const m = el("span", "mask");
      m.style.setProperty("--i", `url(icons/${name}.svg)`);
      e.append(m);
      // a mask that fails to load draws nothing, and says nothing
      const probe = new Image();
      probe.onerror = () => {
        e.classList.add("generic");
        e.replaceChildren(...icon("generic").childNodes);
      };
      probe.src = `icons/${name}.svg`;
    }
    return e;
  }
  e.classList.add("blank");
  return e;
}

function status(msg, kind = "", ms = kind === "err" ? 8000 : 3500) {
  const s = $("#status");
  s.textContent = msg;
  s.title = msg;
  s.className = "status " + kind;
  // with a dialog open the footer is under its scrim: the pill floats over both
  const m = $("#modal");
  s.classList.toggle("lift", !!msg && !m.hidden && !m.classList.contains("out"));
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
    // the panel says what is set in words at the right; the window has two
    // setting columns
    const rest = el("div", mode === "panel" ? "ag-sk-sum" : "fields");
    if (mode === "panel") rest.append(el("span", "skeleton ag-sk-value"), el("span", "skeleton ag-sk-bars"));
    else rest.append(el("span", "skeleton ag-sk-field"), el("span", "skeleton ag-sk-field"));
    row.append(el("span", "skeleton ag-sk-icon"), who, rest);
    list.append(row);
  }
  fit(); // the panel as tall as the rows, not as it was
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
  if (mode === "panel") $("#ptabN").textContent = state.agents.length || "";
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
    // the squares share one cell, side by side: two (Codex's subagents and
    // sign-in) wrapped the second under the first
    const extras = el("span", "extras-cell");
    for (const f of sorted) {
      if (extra(f)) { extras.append(extraField(a, f)); continue; }
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
    // an agent that takes the gateway only from its environment (agy): a
    // square that copies the command starting it on magpie
    if (a.launch) extras.append(launchButton(a));
    if (extras.childNodes.length) fields.append(extras);
    // an app that takes magpie by a link of its own (Cindy) has nothing to
    // pick: its row opens the link, and the app asks to add magpie
    if (a.import && mode !== "panel") fields.append(importButton(a));
    // the panel shows what's set as words, and a row's controls only
    // once it's opened: one row at a time, in place
    let sum = null, openBox = null;
    const effortOf = (f) => f.key === "effort" || f.label === "effort" || f.label === "thinking";
    if (mode === "panel" && a.import) {
      // in words like the rest, the row itself the link
      sum = el("span", "ag-sum");
      sum.append(el("span", "v" + (a.added ? "" : " empty"), a.added ? "magpie" : t("Add magpie")), el("span"));
      const c = el("span", "chev");
      c.append(svg(OUT, 10, 1.6));
      sum.append(c);
      row.title = importButton(a).title;
      row.onclick = (ev) => {
        if (ev.target.closest(".ag-handle, .ag-fix, .ag-show")) return;
        importButton(a).click();
      };
    } else if (mode === "panel") {
      sum = el("span", "ag-sum");
      const main = sorted.find((f) => !extra(f) && !f.menu && !effortOf(f));
      const opt = main && optionFor(main, main.value);
      // the model in words, its logo coming in beside them on hover
      const v = el("span", "v" + (main?.value ? "" : " empty"));
      if (main?.value && (opt?.icon || opt?.icons?.length)) {
        const mi = el("span", "mi");
        mi.setAttribute("aria-hidden", "true");
        mi.append(el("span", "mi-in"));
        mi.firstChild.append(optionIcon(opt));
        // its width to open to, read as the pointer comes onto the row:
        // before :hover is styled, so the box eases open from nothing
        row.addEventListener("pointerenter", () => {
          if (!mi.style.getPropertyValue("--w")) mi.style.setProperty("--w", mi.firstChild.offsetWidth + "px");
        });
        v.append(mi);
      }
      v.append(el("span", "vt", main ? (opt?.label || main.value || t("default")) : ""));
      sum.append(v);
      // how much effort as three bars, in a column of its own down the list:
      // none lit for the default, or for an agent that has no such setting
      const ef = a.fields.find(effortOf);
      sum.append(effortBars(ef));
      const c = el("span", "chev");
      c.append(svg(CHEV, 10, 1.6));
      sum.append(c);
      // opened: each setting on a line of its own, named, and the effort as
      // its levels side by side
      // .ag-in clips while the row opens or closes, .ag-body holds the lines
      openBox = el("div", "ag-open");
      const body = el("div", "ag-body");
      openBox.append(el("div", "ag-in"));
      openBox.firstChild.append(body);
      for (const b of [...fields.querySelectorAll(":scope > .field")]) {
        const f = sorted.find((x) => x.key === b.dataset.key) || (b.dataset.key === "tiers" ? tiers : null);
        if (f && effortOf(f)) { body.append(effortSeg(a, f)); continue; }
        if (f && !b.querySelector(":scope > .k")) b.prepend(el("span", "k", t(f.label)));
        body.append(b);
      }
      if (extras.childNodes.length) body.append(extras);
      row.classList.toggle("open", panelOpenAgent === a.id);
      row.setAttribute("aria-expanded", String(panelOpenAgent === a.id));
      row.onclick = (ev) => {
        if (ev.target.closest(".ag-open, .ag-handle, .ag-fix, .ag-show")) return;
        const open = panelOpenAgent !== a.id;
        panelOpenAgent = open ? a.id : null;
        // the rows ease open and shut, and the panel's edge moves with them:
        // it goes now to where they will be, over the same time and curve
        let grow = open ? openBox.querySelector(".ag-body").offsetHeight : 0;
        for (const r of $("#agents").querySelectorAll(".row.agent.open")) {
          grow -= r.querySelector(".ag-in")?.offsetHeight || 0;
          r.classList.remove("open");
          r.setAttribute("aria-expanded", "false");
        }
        row.classList.toggle("open", open);
        row.setAttribute("aria-expanded", String(open));
        fit(grow, open ? ROW_OPEN : ROW_CLOSE);
        const settle = (ev) => {
          if (ev.target !== openBox || ev.propertyName !== "grid-template-rows") return;
          openBox.removeEventListener("transitionend", settle);
          fit();
        };
        openBox.addEventListener("transitionend", settle);
      };
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
    // the CLI's version, and an update when one is out (#202); the panel's
    // name column has no room for it
    if (mode !== "panel") who.append(cliTag(a));
    // which of magpie's models its lists show, on a line under the name
    if (mode !== "panel" && a.models) {
      const line = el("div", "ag-models-line");
      line.append(modelsEntry(a));
      who.append(line);
      who.classList.add("with-models");
    }
    row.append(agentHandle(a, row, inFold), who);
    if (sum) row.append(sum, ...(openBox ? [openBox] : []));
    else row.append(fields);
    return row;
  };
  // the extras column is there for every row once any agent has one, so the
  // pickers keep lining up down the list
  list.classList.toggle("extras", state.agents.some((a) => a.fields.some(extra) || tierMenu(a) || a.launch));
  // as wide as the row with the most squares
  list.style.setProperty("--extras", Math.max(1, ...state.agents.map((a) => a.fields.filter((f) => extra(f) && !TIERS.includes(f.label)).length + (tierMenu(a) ? 1 : 0) + (a.launch ? 1 : 0))));
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
    more.dataset.unrolls = ""; // it goes down with the rows it opens
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
    more.onclick = (e) => {
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
      if (showAllAgents) unrollInView(fold, more, e);
    };
    // the button, then what it unrolls: the rest come in under it, and the
    // rows above stay where they are (the scroll unrolling above the button
    // pushed it off the foot of a panel at its tallest, or, held there, drew
    // the whole list up past it)
    list.append(more, fold);
  }

  renderProfiles();
  fit(0, agentsGlide);
  agentsGlide = null;
}

// renderProfiles draws the saved profiles as chips, a chip whose save,
// update or use is on its way dimmed until the answer is in.
function renderProfiles() {
  const chips = $("#profiles");
  chips.replaceChildren();
  $(".profiles > .chip-input")?.remove(); // a name field open goes with the list it was for
  $(".profiles").classList.remove("naming");
  $("#save").textContent = t("＋ Save current");
  $("#profN").textContent = state.profiles.length || "";
  if (!state.profiles.length) chips.append(el("span", "hint", t("none yet · save the setup to switch back in one click")));
  for (const p of state.profiles) {
    const c = el("button", "chip");
    if (profilePending.has(p.name)) c.classList.add("pending");
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

// ---------- the agents' CLIs (#202) ----------
// Each agent's CLI shows its version after its name, faint; when a newer one
// is out and magpie knows how the CLI was installed (its own updater, npm,
// bun, pnpm, Homebrew), a pill beside it updates it. The versions come after
// the rows are drawn, never holding them up; one magpie can't tell how it
// was installed shows its version alone.

let cliInfo = {}; // agent id → { version, latest, via, command, update }
const cliBusy = new Set(); // the ones being updated now
const CLI_UP = "M8 2.5v8M4.5 7 8 10.5 11.5 7M3.5 13.5h9";
const CLI_SPIN = "M13.5 8a5.5 5.5 0 1 1-5.5-5.5";

function cliTag(a) {
  const box = el("span", "ag-cli");
  const c = cliInfo[a.id];
  if (!c?.version) return box;
  const v = el("span", "ag-ver", c.version);
  v.title = !c.via ? t("{agent} {v} · magpie can't tell how it was installed — update it the way you installed it", { agent: a.name, v: c.version })
    : c.update ? t("{agent} {v} is installed · {latest} is out", { agent: a.name, v: c.version, latest: c.latest })
    : t("{agent} {v} · up to date", { agent: a.name, v: c.version });
  box.append(v);
  if (c.update || cliBusy.has(a.id)) {
    const b = el("button", "ag-up");
    b.type = "button";
    b.title = t("Updates with {cmd}", { cmd: c.command });
    paintCLIButton(b, c, cliBusy.has(a.id));
    b.onclick = (e) => { e.stopPropagation(); updateCLI(a, b); };
    box.append(b);
  }
  return box;
}

function paintCLIButton(b, c, busy) {
  b.classList.toggle("busy", busy);
  b.setAttribute("aria-busy", String(busy));
  const text = busy ? t("Updating…") : t("Update to {v}", { v: c.latest });
  // named even where a tight row shows only the arrow
  b.setAttribute("aria-label", text);
  b.replaceChildren(svg(busy ? CLI_SPIN : CLI_UP, 11, 1.8), el("span", "", text));
}

// paintCLI draws an agent's CLI again where it is, the row left as it is
function paintCLI(id) {
  const a = state?.agents?.find((x) => x.id === id);
  if (!a) return;
  for (const old of document.querySelectorAll(`#agents .row.agent[data-id="${CSS.escape(id)}"] .ag-cli`)) old.replaceWith(cliTag(a));
}

let cliLoading = null;
async function loadCLIs(again = 0) {
  if (mode === "panel" || cliLoading) return;
  cliLoading = (async () => {
    let r;
    try { r = await api("agents/cli"); } catch { return; } // it just isn't shown
    const next = r?.agents || {};
    const was = cliInfo;
    cliInfo = next;
    for (const id of new Set([...Object.keys(was), ...Object.keys(next)])) {
      if (JSON.stringify(was[id]) !== JSON.stringify(next[id])) paintCLI(id);
    }
    // some were still being asked: they are ready in a moment
    if (r?.pending && again < 3) setTimeout(() => loadCLIs(again + 1), 4000);
  })();
  try { await cliLoading; } finally { cliLoading = null; }
}

async function updateCLI(a, btn) {
  if (cliBusy.has(a.id)) return;
  cliBusy.add(a.id);
  paintCLIButton(btn, cliInfo[a.id] || {}, true); // in place: what was clicked stays
  try {
    const c = await api("agents/cli/" + encodeURIComponent(a.id), {});
    cliInfo[a.id] = c;
    status(t("{agent} updated to {v}", { agent: a.name, v: c.version }), "ok");
  } catch (e) {
    status(e.message, "err", 12000);
    cliBusy.delete(a.id);
    await loadCLIs(); // what it is now
  } finally {
    cliBusy.delete(a.id);
    paintCLI(a.id);
  }
}

// ---------- the agents' order, and the ones put away ----------
// Kept in magpie's own settings (agentOrder, agentsHidden, agentsShown),
// never in an agent's files. An agent the order doesn't name — one
// installed since — follows the ordered ones, in magpie's own order.

let agentsGlide = null; // how the panel's edge moves after the next render
let panelOpenAgent = null; // the one agent row the panel has opened

const agentUsed = (a) => a.added || a.fields.some((f) => f.value);

// importButton: the one control of an app magpie is added to by its import
// link: magpie, once the app has it, or an offer to add it
function importButton(a) {
  const b = el("button", "field solo import");
  b.type = "button";
  b.title = a.added
    ? t("{name} has magpie as a provider · click to add it again", { name: a.name })
    : t("Opens {name} to add magpie as a provider — confirm it there", { name: a.name });
  b.append(icon("magpie"), el("span", "v" + (a.added ? "" : " empty"), a.added ? "magpie" : t("Add magpie")));
  const c = el("span", "chev");
  c.append(svg(OUT, 11, 1.6));
  b.append(c);
  b.onclick = (ev) => {
    ev.stopPropagation();
    if (web) location.href = a.import;
    else api("open", { url: a.import });
  };
  return b;
}
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

// ---------- an agent's model list ----------

// modelsEntry: the line under an agent's name counting the models its
// lists show ("All 41 models", or "Showing 5 / 32 models"); it opens the
// list to take models out of them and put them back.
function modelsEntry(a) {
  const b = el("button", "ag-models");
  b.type = "button";
  fillModelsEntry(b, a);
  b.onclick = (ev) => openAgentModels(a, b, ev);
  // the rows drawn again while its list is open: the list stays, held to
  // the new line
  if (agentModels?.a.id === a.id) {
    b.classList.add("open");
    agentModels.anchor = b;
    if (agentModels.count) { a.models = agentModels.count; fillModelsEntry(b, a); }
  }
  return b;
}
function fillModelsEntry(b, a) {
  const c = a.models;
  b.replaceChildren();
  // the words in one box, so the flex line keeps the spaces round the number
  const words = el("span");
  if (c.shown >= c.listed) words.append(t("All {n} models", { n: c.listed }));
  else {
    const [pre, post] = t("Showing {shown} / {listed} models", { listed: c.listed }).split("{shown}");
    words.append(pre, el("b", "", String(c.shown)), post || "");
  }
  b.append(words);
  const ch = el("span", "chev");
  ch.append(svg(CHEV_R, 10, 1.6));
  b.append(ch);
  b.title = t("Pick which models {agent} lists", { agent: a.name });
}

const ctxShort = (n) => !n ? "" : n >= 1e6 ? (n % 1e6 ? (n / 1e6).toFixed(1) : n / 1e6) + "M" : Math.round(n / 1e3) + "K";

let agentModels = null;
// the list being fetched, before its box is up: a second click on the line
// takes it back, as it would close the box, and another open or a close
// supersedes it — each fetch that came back drew a box of its own, and the
// one agentModels no longer held stayed on the screen, nothing closing it
let agentModelsLoading = null;
function closeAgentModels() {
  agentModelsLoading?.drop();
  const m = agentModels;
  if (!m) return;
  agentModels = null;
  m.anchor.classList.remove("open");
  popGhost(m.box);
  m.box.remove();
  document.removeEventListener("mousedown", m.outside, true);
  document.removeEventListener("keydown", m.keys, true);
  document.removeEventListener("scroll", m.scrolled, true);
  removeEventListener("resize", closeAgentModels);
  // the agents' pickers list what's left once the writes are in
  if (m.changed) m.saving.then(async () => { state = await api("state"); renderAgents(); }).catch(() => {});
}

async function openAgentModels(a, anchor, ev) {
  ev.stopPropagation();
  const again = agentModels?.a.id === a.id || agentModelsLoading?.id === a.id;
  closeAgentModels();
  closePicker();
  if (again) return;
  // a click elsewhere while it loads is one away from it, as it is once open
  const loading = agentModelsLoading = {
    id: a.id,
    away: (e) => { if (!anchor.contains(e.target)) loading.drop(); },
    drop() {
      if (agentModelsLoading === loading) agentModelsLoading = null;
      document.removeEventListener("mousedown", loading.away, true);
    },
  };
  document.addEventListener("mousedown", loading.away, true);
  let models;
  try { models = (await api("agent-models/" + encodeURIComponent(a.id))).models; }
  catch (e) {
    if (agentModelsLoading === loading) { loading.drop(); status(e.message, "err"); }
    return;
  }
  if (agentModelsLoading !== loading) return;
  loading.drop();
  if (!anchor.isConnected) return;
  const box = el("div", "pop am-pop");
  box.setAttribute("role", "dialog");
  box.setAttribute("aria-label", t("{agent}'s model list", { agent: a.name }));
  const head = el("div", "am-head");
  head.append(el("div", "am-t", t("{agent}'s model list", { agent: a.name })),
    el("div", "am-d", t("Models turned off don't show in {agent}'s model picker; other agents can still use them.", { agent: a.name })));
  const tools = el("div", "am-tools");
  const search = el("label", "am-search");
  search.innerHTML = '<svg viewBox="0 0 16 16" width="12" height="12"><circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="m10.5 10.5 3 3" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>';
  const q = el("input");
  q.type = "text";
  q.spellcheck = false;
  q.autocomplete = "off";
  q.placeholder = t("Search models");
  search.append(q);
  const seg = el("div", "am-seg");
  const segAll = el("button", "on", t("All")), segOn = el("button", "", t("Shown"));
  segAll.type = segOn.type = "button";
  seg.append(segAll, segOn);
  tools.append(search, seg);
  const list = el("div", "am-list");
  const foot = el("div", "am-foot");
  // every one at once, the model in use aside
  const hideAll = el("button", "am-reset am-hide", t("Hide all"));
  const reset = el("button", "am-reset", t("Show all"));
  hideAll.type = reset.type = "button";
  foot.append(el("span", "", t("New models are shown")), el("span", "sp"), hideAll, el("span", "am-dot", "·"), reset);
  box.append(head, tools, list, foot);

  // groups as the catalog has them, routing groups first; a long one
  // starts folded, unless the agent is set to a model in it
  const groups = [];
  for (const m of models) {
    let g = groups.find((x) => x.name === m.group);
    if (!g) groups.push(g = { name: m.group, icon: m.icon, models: [] });
    g.models.push(m);
  }
  groups.sort((x, y) => (y.name === ROUTING_GROUPS) - (x.name === ROUTING_GROUPS));
  const shut = new Set(groups.filter((g) => groups.length > 1 && g.models.length > 8 && !g.models.some((m) => m.inUse)).map((g) => g.name));
  // under "Shown", one just turned off stays until the view changes
  let onlyShown = false, kept = new Set();

  const me = agentModels = { a, anchor, box, saving: Promise.resolve(), changed: false };
  const save = () => {
    me.changed = true;
    const hidden = models.filter((m) => m.hidden).map((m) => m.id);
    const count = { shown: models.length - hidden.length, listed: models.length };
    me.count = a.models = count;
    fillModelsEntry(me.anchor, a);
    me.saving = me.saving.then(() => api("agent-models/" + encodeURIComponent(a.id), { hidden }))
      .catch((e) => status(e.message, "err"));
  };
  const draw = () => {
    const top = list.scrollTop;
    list.replaceChildren();
    const words = q.value.trim().toLowerCase();
    for (const g of groups) {
      const rows = g.models.filter((m) => (!onlyShown || !m.hidden || kept.has(m.id)) &&
        (!words || [m.name, m.id, g.name].some((s) => s.toLowerCase().includes(words))));
      if (!rows.length) continue;
      const on = g.models.filter((m) => !m.hidden).length;
      const folded = !words && shut.has(g.name);
      const sec = el("section", "am-g" + (folded ? " shut" : "") + (g.name === ROUTING_GROUPS ? " routes" : ""));
      const gh = el("div", "am-gh");
      const fold = el("button", "am-fold");
      fold.type = "button";
      fold.setAttribute("aria-expanded", String(!folded));
      const tw = el("span", "tw");
      tw.append(svg(CHEV, 10, 1.7));
      fold.append(tw, g.name === ROUTING_GROUPS ? svg(FAN, 14, 1.5) : icon(g.icon || "generic"),
        el("span", "gn", g.name === ROUTING_GROUPS ? t(g.name) : g.name), el("span", "c", `${on} / ${g.models.length}`));
      fold.onclick = () => { shut.has(g.name) ? shut.delete(g.name) : shut.add(g.name); draw(); };
      const free = g.models.filter((m) => !m.inUse);
      const allOn = free.every((m) => !m.hidden);
      const all = el("button", "am-all", allOn ? t("Hide all") : t("Show all"));
      all.type = "button";
      all.hidden = !free.length;
      all.onclick = () => {
        for (const m of free) { m.hidden = allOn; if (allOn) kept.add(m.id); }
        save();
        draw();
      };
      gh.append(fold, all);
      sec.append(gh);
      if (!folded) {
        const body = el("div", "am-rows");
        for (const m of rows) {
          const r = el("button", "am-mr" + (m.hidden ? " off" : "") + (m.inUse ? " lock" : ""));
          r.type = "button";
          r.setAttribute("role", "menuitemcheckbox");
          r.setAttribute("aria-checked", String(!m.hidden));
          r.title = m.inUse ? t("{agent} is set to it, so it stays", { agent: a.name }) : m.id;
          const n = el("span", "n", m.name);
          if (m.inUse) n.append(el("span", "am-tag", t("Current")));
          const ck = el("span", "ck");
          if (!m.hidden) ck.append(svg("m3.5 8.5 3 3 6-7", 12, 1.9));
          // a routing group: its providers' icons stacked, as everywhere
          // else, a lone one in a disc like them; a model: its maker's
          // logo, or an empty slot, so the names line up
          const route = g.name === ROUTING_GROUPS;
          let lg;
          if (route && m.icons?.length > 1) lg = stackIcon(m.icons);
          else if (route && m.icons?.length) {
            const d = el("span", "disc");
            d.append(icon(m.icons[0] || "generic"));
            lg = el("span", "ic-stack");
            lg.append(d);
          } else lg = m.logo && !route ? icon(m.logo) : el("span", "ic");
          if (route && !m.icons?.length) lg.append(svg(FAN, 14, 1.5));
          lg.classList.add("lg");
          r.append(lg, n, el("span", "x", ctxShort(m.context)), ck);
          if (m.inUse) r.setAttribute("aria-disabled", "true");
          else r.onclick = () => {
            m.hidden = !m.hidden;
            if (m.hidden) kept.add(m.id);
            save();
            draw();
          };
          body.append(r);
        }
        sec.append(body);
      }
      list.append(sec);
    }
    if (!list.childNodes.length) list.append(el("div", "am-none", t("No matches.")));
    list.scrollTop = top;
    reset.disabled = !models.some((m) => m.hidden);
    hideAll.disabled = !models.some((m) => !m.hidden && !m.inUse);
  };
  q.oninput = () => { list.scrollTop = 0; draw(); };
  const view = (shown) => {
    onlyShown = shown;
    kept = new Set();
    segAll.classList.toggle("on", !shown);
    segOn.classList.toggle("on", shown);
    list.scrollTop = 0;
    draw();
  };
  segAll.onclick = () => view(false);
  segOn.onclick = () => view(true);
  reset.onclick = () => {
    for (const m of models) m.hidden = false;
    save();
    draw();
  };
  hideAll.onclick = () => {
    for (const m of models) if (!m.inUse && !m.hidden) { m.hidden = true; kept.add(m.id); }
    save();
    draw();
  };
  draw();

  document.body.append(box);
  // under the line, or over it where there's no room; the list scrolls
  const r = anchor.getBoundingClientRect(), pad = 8;
  const w = Math.min(380, innerWidth - pad * 2);
  box.style.width = w + "px";
  const x = Math.max(pad, Math.min(r.left - 10, innerWidth - w - pad));
  box.style.left = x + "px";
  box.style.setProperty("--ox", Math.max(18, Math.min(w - 18, r.left + Math.min(r.width, 60) / 2 - x)) + "px");
  const below = innerHeight - r.bottom - 8 - pad, above = r.top - 8 - pad;
  const want = Math.min(560, box.scrollHeight);
  if (want > below && above > below) {
    box.classList.add("up");
    box.style.bottom = innerHeight - r.top + 8 + "px";
    box.style.maxHeight = Math.min(560, above) + "px";
  } else {
    box.style.top = r.bottom + 8 + "px";
    box.style.maxHeight = Math.min(560, below) + "px";
  }
  anchor.classList.add("open");
  me.outside = (e) => { if (!box.contains(e.target) && !me.anchor.contains(e.target)) closeAgentModels(); };
  me.keys = (e) => {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopPropagation();
    closeAgentModels();
    me.anchor.focus({ preventScroll: true });
  };
  me.scrolled = (e) => { if (!box.contains(e.target)) closeAgentModels(); };
  document.addEventListener("mousedown", me.outside, true);
  document.addEventListener("keydown", me.keys, true);
  document.addEventListener("scroll", me.scrolled, true);
  addEventListener("resize", closeAgentModels);
  q.focus({ preventScroll: true });
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
  openRowMenu(anchor, acts);
}
// openRowMenu: a small menu of acts under anchor, the one an agent row's
// right-click opens, and a model chip's
function openRowMenu(anchor, acts) {
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
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closeAgentMenu(); anchor.focus({ preventScroll: true }); }
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
// fields that fall back to the agent's model when unset: omp's smol and
// slow roles take the session's, as its subagents do ("smol", not "small":
// other agents' small model is a picker of its own)
const FOLLOWS_MODEL = [...TIERS, "subagents", "smol", "slow"];

// A field that follows the model unless set — Codex's subagents, Claude
// Code's tiers, omp's roles — is a small square after the pickers rather
// than a third picker, which a row has no room for: it wrapped onto a line
// of its own. So is Codex's sign-in, ChatGPT or magpie as its provider.
const extra = (f) => f.key === "tiers" || FOLLOWS_MODEL.includes(f.label) || f.label === "sign-in";
const EXTRA_GLYPH = {
  subagents: "M4.5 2.75v10.5M4.5 9.25c0-2.2 1.6-3.75 3.9-3.75h3.35M9.9 3.6l1.9 1.9-1.9 1.9",
  // a feather for omp's smol role, an hourglass for its slow one
  smol: "M12.75 3.25c-4.5 0-8 3.5-8 8v1.5M12.75 3.25c0 4-2.75 7-6.75 7.25M4.75 12.75l-1.5 1.5",
  slow: "M4.5 2.5h7M4.5 13.5h7M5.25 2.5c0 3 5.5 3 5.5 5.5s-5.5 2.5-5.5 5.5M10.75 2.5c0 3-5.5 3-5.5 5.5s5.5 2.5 5.5 5.5",
  tiers: "M8 2.6 2.75 5.4 8 8.2l5.25-2.8zM2.75 8.1 8 10.9l5.25-2.8M2.75 10.8 8 13.6l5.25-2.8",
  "sign-in": "M8 2.5a2.75 2.75 0 1 1 0 5.5 2.75 2.75 0 0 1 0-5.5zM3 13.5c.4-2.4 2.4-3.9 5-3.9s4.6 1.5 5 3.9",
};
function extraField(a, f) {
  const set = !!(f.value || f.custom);
  const b = el("button", "field extra" + (set ? " set" : ""));
  b.append(svg(EXTRA_GLYPH[f.label] || EXTRA_GLYPH.tiers, 13, 1.5));
  const opt = optionFor(f, f.value);
  b.title = f.menu
    ? t("{label}: {value}", { label: t(f.label), value: f.summary }) + "\n" + f.options.map((o) => `${o.label}: ${o.note}`).join("\n")
    : t("{label}: {value}", { label: t(f.label), value: t(opt?.label || f.value || "same as model") }) + (opt?.note && !FOLLOWS_MODEL.includes(f.label) ? "\n" + t(opt.note) : "");
  b.setAttribute("aria-label", b.title);
  b.dataset.key = f.key;
  b.onclick = (ev) => openPicker(a, f, b, ev);
  return b;
}

// launchButton copies the command that starts an agent on magpie, for one
// that takes the gateway only from its environment (agy)
function launchButton(a) {
  const b = el("button", "field extra launch");
  b.type = "button";
  b.append(svg(LAUNCH_GLYPH, 13, 1.5));
  b.title = t("{name} takes magpie only from its environment · click to copy the command that starts it:", { name: a.name }) + "\n" + a.launch;
  b.setAttribute("aria-label", b.title);
  b.onclick = (ev) => {
    ev.stopPropagation();
    copy(a.launch, t("Launch command"), null, t("Copied — run it to start {name} on magpie", { name: a.name }));
  };
  return b;
}
const LAUNCH_GLYPH = "M2.5 3.5h11v9h-11zM5 6.5l2 1.75L5 10M8.5 10h2.5";

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
// A row opens and closes on a critically damped spring, as iOS moves things:
// it sets off gently, not at a jump, and settles without a long tail. The
// curve is that spring fitted to a cubic-bezier, which the panel's edge can
// take too; app.css has the same (--row-spring).
const ROW_OPEN = { ms: 480, ease: ".25,.3,.1,1" };
const ROW_CLOSE = { ms: 400, ease: ".25,.3,.1,1" };
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

// tintTitleBar paints Windows' own title bar the window's page colour, dark
// or light as the page is, so the bar and the page are one surface as on the
// Mac, not a grey strip above it (light over a dark page). wait lets a change
// of theme finish fading first: the colour read is where it ends.
function tintTitleBar(wait = 0) {
  if (mode !== "window" || web || !document.documentElement.classList.contains("win")) return;
  clearTimeout(tintTitleBar.t);
  tintTitleBar.t = setTimeout(async () => {
    const c = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
    c.fillStyle = "#fff";
    c.fillRect(0, 0, 1, 1);
    c.fillStyle = getComputedStyle(document.body).backgroundColor;
    c.fillRect(0, 0, 1, 1);
    const [r, g, b] = c.getImageData(0, 0, 1, 1).data;
    const rgba = [r, g, b, 255].join(",");
    if (rgba === tintTitleBar.last) return;
    tintTitleBar.last = rgba;
    // the bar's own text and buttons light on a dark page, dark on a light one
    const dark = 0.2126 * r + 0.7152 * g + 0.0722 * b < 128;
    try { await api("window/titlebar?c=" + rgba + "&dark=" + (dark ? 1 : 0), {}); } catch { tintTitleBar.last = null; }
  }, wait);
}
if (mode === "window") {
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => tintTitleBar(460));
}

// extra is room about to be taken (or given back), e.g. by agents unrolling;
// glide moves the panel's edge there over time instead of at once.
// The panel is as tall as its tallest tab, not the one showing, so it keeps
// its height as the tabs change and a shorter tab leaves room below (#124):
// every tab is laid out at once for the measure, before anything is drawn.
function fit(extra = 0, glide) {
  if (mode !== "panel") return;
  const body = document.body, tab = body.dataset.ptab;
  delete body.dataset.ptab;
  // extra is only ever the agents' (a row opening, the scroll unrolling)
  const tallest = Math.max($("#agents").offsetHeight + extra, $("#panelQuota").offsetHeight, $("#panelRouting").offsetHeight);
  if (tab) body.dataset.ptab = tab;
  const h = $(".top").offsetHeight + $("#ptabs").offsetHeight + tallest + $(".foot").offsetHeight + 4;
  if (h === fit.last) return;
  fit.last = h;
  const still = !glide || matchMedia("(prefers-reduced-motion: reduce)").matches;
  api("window/fit?h=" + h + (still ? "" : "&ms=" + glide.ms + "&ease=" + glide.ease), {});
}

async function load() {
  if (!load.done) renderAgentsLoading();
  // the gateway page too waits for the state first: its skeleton, not a
  // blank page, until then (#123)
  if (view === "providers" && !providers) renderProvidersLoading();
  if (view === "gateway" && !providers) renderGatewayLoading();
  try {
    const since = prefsWrites;
    const next = await api("state");
    // a setting changed while this was on its way (it can take seconds):
    // what came back is from before it, and would put the old theme back
    if (!prefsSettled(since) && load.done) next.settings = state.settings;
    state = next;
    load.done = true;
    // the library may have drawn itself before the saved language was known
    if (applyPrefs(state.settings, state.fx)) {
      if (view === "library") window.loadLibrary?.();
      if (view === "plugins") window.loadPlugins?.();
    }
    tintPanel();
    tintTitleBar();
    renderAgents();
    loadCLIs(); // after the rows, never holding them up
    if (mode === "panel") { renderPanelQuota(); loadQuotas(); }
    // an open provider editor is someone typing: coming back to the window
    // must not rebuild it under them
    if ((view === "providers" || view === "gateway") && !(editing || adding)) await loadProviders();
    if (view === "usage") await loadUsage();
    // so is an open sync form (WebDAV, export, import): its passwords are
    // never sent back, so a rebuild would empty it
    if (view === "settings" && !syncOpen) await loadSettings();
  } catch (e) {
    status(e.message, "err");
  }
  renderUpdateBadge();
}

// installFrom is what a restart to update tells the app: the window's tab,
// for the new version to open its window there (Windows, Linux).
function installFrom() {
  return mode === "window" ? { view } : {};
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
    const a = await api("update/install", installFrom()).catch(() => ({}));
    if (!a) return backAsNew(u.current);
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
    if (web && u.url) return window.open(u.url, "_blank", "noopener");
    api("update/install", {}).catch(() => {});
  };
}

// backAsNew waits, in magpie web, for the version the page restarted into
// to answer in its place, and reloads the page from it (#111).
async function backAsNew(was) {
  if (!web) return;
  for (let i = 0; i < 180; i++) {
    await new Promise((r) => setTimeout(r, 1000));
    const u = await fetch("/api/update").then((r) => (r.ok ? r.json() : null)).catch(() => null);
    if (u && u.current !== was) return location.reload();
  }
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
  let x = Math.max(pad, Math.min(r.left, innerWidth - w - pad));
  let y = r.bottom + 5;
  pop.classList.remove("up");
  pop.style.left = x + "px";
  // it grows out of the button's middle, or near the edge it's held to
  pop.style.setProperty("--ox", Math.max(16, Math.min(w - 16, r.left + r.width / 2 - x)) + "px");
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
  const again = pick?.anchor === anchor;
  closePicker();
  if (again) return;
  const cur = field.value;
  let options = field.options.filter((o) => !only || only(o));
  // routing groups come first, before the agent's own models and each
  // provider's; only the picker's own choices (Automatic, Off) above them
  options = [...options.filter((o) => o.reset), ...options.filter((o) => !o.reset && o.group === ROUTING_GROUPS), ...options.filter((o) => !o.reset && o.group !== ROUTING_GROUPS)];
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
  } else if (!only && !field.menu && !field.onPick && !options.some((o) => o.value === "")) options.unshift({ value: "", label: t("Default"), note: t("what {agent} ships with", { agent: agent.name }), icon: agent.icon, reset: true });
  const modelPicker = ["model", "small", "large", ...FOLLOWS_MODEL].includes(field.label) && !only;
  pick = { agent, field, options, anchor, cursor: 0, free: !only && !field.menu, modelPicker, effortPicker, groupFilter: "all" };
  anchor.classList.add("open");
  const pop = $("#pop");
  pop.classList.toggle("model-picker", modelPicker);
  pop.classList.toggle("effort-picker", effortPicker);
  // a choice explained in a sentence (Codex's sign-in) shows all of it
  pop.classList.toggle("explained", field.label === "sign-in");
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

// effortBars: the panel's effort at a glance, three bars lit up to it.
function effortBars(f) {
  const levels = (f?.options || []).filter((o) => o.value && o.value !== "off");
  const at = f ? levels.findIndex((o) => o.value === f.value) : -1;
  const e = el("span", "eff");
  e.dataset.l = at < 0 ? 0 : Math.max(1, Math.round(((at + 1) / levels.length) * 3));
  e.append(el("i"), el("i"), el("i"));
  if (f) e.title = t("{label}: {value}", { label: t(f.label), value: effortName(optionFor(f, f.value) || { value: f.value }) });
  return e;
}

// effortSeg: an opened panel row's effort, as the effort picker's slider,
// drawn rather than native so the thumb glides from stop to stop. A level
// shows at once, bars and all; the write follows, in order, so a slow agent
// config never holds the thumb (巨卡).
function effortSeg(a, f) {
  // a value that isn't one of the levels (unset, or one magpie doesn't list)
  // is a stop of its own, as the picker's current value is: shown as the
  // lowest level, a touch wrote that level over it
  const options = f.options.some((o) => o.value === f.value) ? f.options : [{ value: f.value }, ...f.options];
  const last = Math.max(1, options.length - 1);
  const box = el("div", "effort-control");
  const head = el("div", "effort-head");
  const value = el("b");
  head.append(el("span", "", t(f.label)), value);
  const track = el("div", "eslide");
  track.tabIndex = 0;
  track.setAttribute("role", "slider");
  track.setAttribute("aria-label", t(f.label));
  track.setAttribute("aria-valuemin", "0");
  track.setAttribute("aria-valuemax", String(options.length - 1));
  const rail = el("span", "rail");
  rail.append(el("span", "fill"));
  const ticks = el("span", "effort-ticks");
  ticks.setAttribute("aria-hidden", "true");
  ticks.append(...options.map((_, i) => {
    const dot = el("i");
    dot.style.setProperty("--at", i / last);
    return dot;
  }));
  track.append(rail, ticks, el("span", "knob"));
  const ends = el("div", "effort-ends");
  ends.setAttribute("aria-hidden", "true");
  ends.append(el("span", "", effortName(options[0])), el("span", "", effortName(options[options.length - 1])));
  box.append(head, track, ends);
  let at = options.findIndex((o) => o.value === f.value);
  const show = (i) => {
    at = i;
    track.style.setProperty("--p", i / last);
    [...ticks.children].forEach((dot, j) => { dot.classList.toggle("on", j < i); dot.classList.toggle("cur", j === i); });
    value.textContent = effortName(options[i]);
    track.setAttribute("aria-valuenow", String(i));
    track.setAttribute("aria-valuetext", effortName(options[i]));
  };
  let saving = Promise.resolve();
  const commit = () => {
    const o = options[at];
    if (!o || o.value === f.value) return;
    f.value = o.value;
    box.closest(".row")?.querySelector(".ag-sum .eff")?.replaceWith(effortBars(f));
    saving = saving.then(async () => {
      state = await api("set", { agent: a.id, field: f.key, value: o.value });
      status(`${a.name} ${t(f.label)} → ${effortName(o)}`, "ok");
    }).catch((e) => { status(e.message, "err"); renderAgents(); });
  };
  const stopAt = (x) => {
    const r = rail.getBoundingClientRect();
    return Math.round(Math.max(0, Math.min(1, (x - r.left) / r.width)) * last);
  };
  track.onpointerdown = (ev) => {
    track.setPointerCapture(ev.pointerId);
    track.classList.add("drag");
    show(stopAt(ev.clientX));
  };
  track.onpointermove = (ev) => { if (track.classList.contains("drag")) show(stopAt(ev.clientX)); };
  track.onpointerup = track.onpointercancel = () => {
    if (!track.classList.contains("drag")) return;
    track.classList.remove("drag");
    commit();
  };
  track.onkeydown = (ev) => {
    const to = { ArrowLeft: at - 1, ArrowDown: at - 1, ArrowRight: at + 1, ArrowUp: at + 1, Home: 0, End: options.length - 1 }[ev.key];
    if (to === undefined) return;
    ev.preventDefault();
    show(Math.max(0, Math.min(options.length - 1, to)));
    commit();
  };
  show(at);
  return box;
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

// a rail icon's name, beside it on the right: the browser's own tooltip
// came up under the pointer, over the icon below — Devin's name on ZCode's
// Z, as if that were ZCode's (Elan on X). Hovering from one icon to the
// next moves it at once; the first waits a moment, as a tooltip does.
let railTip = null, railTipTimer = 0, railTipShownAt = 0;
function showRailTip(b, now) {
  clearTimeout(railTipTimer);
  const warm = railTip?.classList.contains("on") || performance.now() - railTipShownAt < 400;
  const show = () => {
    if (!b.isConnected || !pick?.modelPicker) return;
    if (!railTip) { railTip = el("div", "rail-tip"); railTip.setAttribute("role", "tooltip"); document.body.append(railTip); }
    railTip.textContent = b.getAttribute("aria-label");
    const r = b.getBoundingClientRect(), w = railTip.offsetWidth;
    const right = r.right + 8 + w <= innerWidth - 4;
    railTip.style.left = (right ? r.right + 8 : Math.max(4, r.left - 8 - w)) + "px";
    railTip.style.top = r.top + r.height / 2 + "px";
    railTip.classList.toggle("left", !right);
    railTip.classList.add("on");
    railTipShownAt = performance.now();
  };
  if (now || warm) show(); else railTipTimer = setTimeout(show, 450);
}
function hideRailTip() {
  clearTimeout(railTipTimer);
  if (railTip?.classList.contains("on")) { railTip.classList.remove("on"); railTipShownAt = performance.now(); }
}
$("#pickerRail").addEventListener("scroll", hideRailTip, { passive: true });

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
      b.setAttribute("aria-label", title);
      b.append(child);
      b.onclick = () => { hideRailTip(); switchPickerGroup(id); };
      b.onpointerenter = () => showRailTip(b, false);
      b.onpointerleave = hideRailTip;
      b.onfocus = () => { if (b.matches(":focus-visible")) showRailTip(b, true); };
      b.onblur = hideRailTip;
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

// namedFree: a model its vendor names free (OpenRouter's foo/bar:free, a
// free-model), "free" as a word of its id or name, not freedom-7b (#185)
function namedFree(...names) {
  return names.some((n) => /(^|[^a-z])free($|[^a-z])/i.test(n || ""));
}

// freeBadge: the green FREE by a model, and why it is free
function freeBadge(plan) {
  const f = el("span", "badge free", t("free"));
  f.title = t(plan ? "free: it doesn't use the plan's credits" : "free: so its name says");
  return f;
}

// contextTag: the small grey 1M by a model that holds a million tokens or
// more (Cursor's names said it, and no longer do); the usual 128K–400K
// aren't marked, as nearly every model has one of those; nor is a name
// that says it itself ("GPT-5.5 Mini (1M)", kept to tell it from another)
function contextTag(n, name) {
  if (!(n >= 1e6) || /\b\d+M\b/.test(name || "")) return null;
  const m = n / 1e6;
  const tag = el("span", "badge ctx", (Number.isInteger(m) ? m : m.toFixed(1)) + "M");
  tag.title = t("holds {n} tokens", { n: n.toLocaleString() });
  return tag;
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
    // a choice of magpie's own (Codex's sign-in) reads in the page's language
    const own = pick.field.label === "sign-in";
    words.append(el("span", "v", own ? t(o.label || o.value) : o.label || o.value));
    if (o.free || (pick.modelPicker && o.value && namedFree(o.value, o.label))) words.append(freeBadge(o.free));
    const ctx = contextTag(o.context, o.label);
    if (ctx) words.append(ctx);
    let note = o.note && o.note !== (o.label || o.value) ? (own ? t(o.note) : o.note) : "";
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
  // The pick shows at once: the row is drawn with it before magpie has
  // written the config and answered with the whole state, which can take
  // seconds (every agent's lists are read again for it). The answer then
  // draws what the config really says; a refused pick puts the old one back.
  const was = field.value;
  const seq = commit.seq = (commit.seq || 0) + 1;
  field.value = value;
  const picked = performance.now();
  // the green flash runs on through the row drawn again with the answer
  const flash = () => {
    const b = document.querySelector(`.agent[data-id="${CSS.escape(agent.id)}"] .field[data-key="${TIERS.includes(field.label) ? "tiers" : field.key}"]`);
    const gone = performance.now() - picked;
    if (!b || gone > 1200) return;
    b.style.animationDelay = `${-gone}ms`;
    b.classList.add("flash");
  };
  renderAgents();
  flash();
  try {
    const next = await api("set", { agent: agent.id, field: field.key, value });
    // a later pick is already on its way: its answer is the one to draw
    if (seq !== commit.seq) return;
    state = next;
    renderAgents();
    flash();
    const shown = opt?.label || value;
    if (state.notice) status(`${agent.name} → ${shown}. ${state.notice}`, "warn", 9000);
    else status(`${agent.name} ${t(field.label)} → ${shown}`, "ok");
    if (providers) loadProviders();
  } catch (e) {
    if (seq === commit.seq && field.value === value) {
      field.value = was;
      renderAgents();
    }
    status(e.message, "err");
  }
}

// popGhost leaves a likeness of the picker where it was, to fade and sink
// away while the real one is already put back for its next opening.
function popGhost(pop) {
  if (pop.hidden || matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  const g = pop.cloneNode(true);
  g.removeAttribute("id");
  g.querySelectorAll("[id]").forEach((e) => e.removeAttribute("id"));
  g.classList.add("leaving");
  g.setAttribute("aria-hidden", "true");
  g.inert = true;
  pop.after(g);
  // what a clone doesn't carry: where its lists were scrolled, what was typed
  const from = pop.querySelectorAll("*"), to = g.querySelectorAll("*");
  from.forEach((e, i) => {
    if (e.scrollTop) to[i].scrollTop = e.scrollTop;
    if (e.tagName === "INPUT") to[i].value = e.value;
  });
  const done = () => g.remove();
  g.addEventListener("animationend", done, { once: true });
  setTimeout(done, 400);
}

function closePicker() {
  if (!pick) return;
  hideRailTip();
  pick.groupAnimation?.cancel();
  popGhost($("#pop"));
  pick.anchor.classList.remove("open");
  $("#pop").hidden = true;
  $("#pop").classList.remove("model-picker", "effort-picker", "explained");
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
document.addEventListener("mousedown", (e) => {
    if (pick && !$("#pop").contains(e.target) && !pick.anchor.contains(e.target)) closePicker();
});
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

// profilePending are the profiles a save, update, delete or use is on its
// way for. What the click does shows at once — the chip saved is there, the
// one deleted gone, the name field closed — rather than when magpie has read
// every agent again for the answer: that took seconds, and a Save or × that
// changed nothing for that long looked broken. An error puts the list back.
const profilePending = new Set();

async function profileAction(action, name, update) {
  const before = state.profiles;
  if (action === "delete") state.profiles = before.filter((p) => p.name !== name);
  else if (action === "save" && !before.some((p) => p.name === name)) {
    // where magpie will list it: by name, as Go's sort.Strings has them
    state.profiles = [...before, { name, summary: "" }].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  }
  if (action !== "delete") profilePending.add(name);
  renderProfiles();
  fit();
  try {
    const data = await api("profile/" + action, { name });
    profilePending.delete(name);
    state = data;
    renderAgents();
    if (action === "use") {
      closeProfiles(); // the agents, as they are now, in sight
      let msg = t(data.changed === 1 ? "{name} applied · {n} setting changed" : "{name} applied · {n} settings changed", { name, n: data.changed });
      const lib = data.library;
      if (lib?.missing?.length) msg += " · " + t("skipped, no longer in the Library: {names}", { names: lib.missing.map((m) => m.replace(/^\w+:/, "")).join(", ") });
      if (lib?.problems?.length) status(msg + " · " + t("some of the Library couldn't be given; see Library"), "err", 6000);
      else status(msg, "ok", lib?.missing?.length ? 6000 : 3500);
    }
    else if (action === "save") status(t(update ? "Updated {name} to the current setup" : "Saved {name}", { name }), "ok");
    else status(t("Deleted {name}", { name }));
  } catch (e) {
    profilePending.delete(name);
    state.profiles = before;
    renderProfiles();
    fit();
    status(e.message, "err");
  }
}

// ＋ Save current opens a name field beside it, and the button is Save; a
// click on it saves as Enter does (it did nothing: the field lost focus to
// it and went). The field goes where the button is, not at the head of the
// list: in the panel it drew the list up under the tabs, the field's top
// cut off, or put the button out of sight below it.
const saveCurrent = $("#save");
const saveField = () => $(".profiles > .chip-input");
const closeSave = (input) => {
  input.remove();
  if (!saveField()) { saveCurrent.textContent = t("＋ Save current"); $(".profiles").classList.remove("naming"); }
};
saveCurrent.onmousedown = (e) => { if (saveField()) e.preventDefault(); }; // the field keeps focus
saveCurrent.onclick = () => {
  const open = saveField();
  if (open) {
    if (open.value.trim()) profileAction("save", open.value.trim());
    else open.focus({ preventScroll: true });
    return;
  }
  const input = el("input", "chip-input");
  input.placeholder = t("Profile name");
  input.onkeydown = (e) => {
    if (e.key === "Enter" && input.value.trim()) profileAction("save", input.value.trim());
    else if (e.key === "Escape") closeSave(input);
    e.stopPropagation();
  };
  input.onblur = () => setTimeout(() => closeSave(input), 100);
  saveCurrent.before(input);
  $(".profiles").classList.add("naming"); // for the panel's hint: :has() came in Safari 15.4 (#220)
  saveCurrent.textContent = t("Save");
  input.focus({ preventScroll: true });
};

// ---------- providers view ----------
//
// A provider is a vendor plus the key the user pasted. Presets need only the
// key; a custom one needs a name and a base URL too. Every exposed model of
// every provider becomes "provider/model" in the agents' pickers, served by
// the local gateway in whichever API the agent speaks.

async function loadProviders() {
  // the skeletons only while there is nothing yet: a reload keeps what is
  // drawn, and where the reader is in it, until the new one is in
  if (!providers) { if (view === "gateway") renderGatewayLoading(); else renderProvidersLoading(); }
  // the gateway page opened from another that had the list: drawn from it
  // at once, not left as empty cards until the new one is in (#123)
  else if (view === "gateway" && !$("#gateway").childElementCount) renderGatewayView();
  const was = providers;
  providers = await api("providers");
  // a reload's provider may be gone since
  if (typeof editing === "string" && !providers.providers.some((p) => p.id === editing)) editing = null;
  if (!providers.providers.length && editing === null) adding = true;
  if (view === "gateway") {
    // the gateway page is looked at and left open: coming back to the window
    // redraws it only when something on it changed, and when only the calls
    // did, only them (an account's last sign-in check isn't on it)
    const json = (a) => JSON.stringify(a, (k, v) => (k === "seen" ? undefined : v));
    const same = (a, b) => json(a) === json(b);
    if (was && !$("#view-gateway").classList.contains("loading") && same({ ...was, gateway: { ...was.gateway, calls: 0 } }, { ...providers, gateway: { ...providers.gateway, calls: 0 } })) {
      if (!same(was.gateway.calls, providers.gateway.calls)) renderActivity();
    } else renderGatewayView();
    backToReader($("#view-gateway"));
  } else renderProviders();
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
  keepIcons($("#providers"), $("#addSheet"), $("#excluded"));
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
    const row = el("div", "row provider" + (open ? " selected" : "") + (p.off ? " off" : ""));
    row.dataset.id = p.id;
    const who = el("div", "who");
    const name = el("div", "name", p.name);
    if (p.sponsored) name.append(el("span", "badge", t("sponsored")));
    if (p.off) name.append(el("span", "badge off", t("Switched off")));
    const n = p.models.filter((m) => m.on).length;
    // a provider that only draws images (Settings → Image generation) says so
    const models = n ? t(n === 1 ? "{n} model" : "{n} models", { n })
      : p.draws ? t(p.draws === 1 ? "{n} image model" : "{n} image models", { n: p.draws }) : t("no models exposed");
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
    // every pill is one width so the dots line up down the list; a label too long for it ends in "…" and is said whole on hover
    const label = key.textContent;
    key.textContent = "";
    key.append(el("span", "", label));
    if (!key.title.includes(label)) key.title = label + " · " + key.title;
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(icon(p.icon || "generic"), who, uses, key, providerSwitch(p), chev);
    row.onclick = () => { editing = open ? null : p.id; draft = null; renderProviders(); }; // the preset sheet stays as it is under the dialog
    list.append(row);
    if (open) dialog = renderEditor(p);
  }
  renderExcluded();
  dialog = renderAdd() || dialog;
  if (importing) dialog = renderImport(importing);
  if (importingApps) dialog = renderImportApps(importingApps);
  keptIcons = null;
  view.scrollTop = top; // first: a closing dialog folds into its row where it is
  if (dialog) openModal(dialog); else closeModal();
}

// providerSwitch turns a provider off and on (#163): off, it stays with its
// keys and settings, but agents are given none of its models and no request
// goes to it (a key out of quota, say), without removing it.
function providerSwitch(p) {
  const on = !p.off;
  const s = el("button", "lib-switch pswitch" + (on ? " on" : ""));
  s.setAttribute("role", "switch");
  s.setAttribute("aria-checked", on ? "true" : "false");
  s.setAttribute("aria-label", t("Use {name}", { name: p.name }));
  s.title = on ? t("On · switch it off and agents no longer get its models; its keys and settings are kept") : t("Off · switch it on and agents get its models again");
  s.append(el("i"));
  s.onclick = (e) => { e.stopPropagation(); switchProvider(p, !on, s); };
  return s;
}

// switchProvider saves it on or off at once, leaving an editor open as it
// was; the agents' own model lists follow.
async function switchProvider(p, on, s) {
  s?.classList.toggle("on", on);
  try {
    providers = await api("provider/" + (on ? "on" : "off"), { id: p.id });
    renderProviders();
    state = await api("state");
    renderAgents();
    saidMoved(t(on ? "{name} is on" : "{name} is off: agents no longer get its models", { name: p.name }));
  } catch (e) {
    s?.classList.toggle("on", !on);
    status(e.message, "err");
  }
}

// Sign-ins magpie found but leaves alone, so nobody wonders why an agent that
// is clearly logged in is not in the list: the ones the user removed.
function renderExcluded() {
  const box = $("#excluded");
  box.replaceChildren();
  for (const x of providers.excluded) {
    if (x.quiet) continue; // dismissed: the Add sheet offers it back (#116)
    const r = el("div", "excluded");
    r.append(icon(x.agentIcon), el("span", "", ""));
    r.lastChild.append(el("b", "", t(x.signedOut ? "{agent}'s saved accounts aren't offered. " : "{agent} is signed in, but stays out of this list. ", { agent: x.agentName })), x.signedOut ? x.why : t(x.why));
    if (x.provider) {
      const back = el("button", "link", t("Add it back"));
      back.onclick = () => providerAction("show", { id: x.provider }, t("{name} added back", { name: x.agentName }));
      const quiet = el("button", "link", t("Don't remind me"));
      quiet.title = t("Hide this line; Add a provider still offers it back");
      quiet.onclick = () => providerAction("quiet", { id: x.provider });
      r.lastChild.append(" ", back, " · ", quiet);
    }
    // an agent signed out here (a banned account logged out, say) lists
    // its saved accounts nowhere else, so this is where they are removed
    if (x.signedOut && x.users?.length) {
      const rm = el("button", "link", t("Remove"));
      rm.title = t("magpie forgets the accounts it saved; {agent}'s own files are left as they are", { agent: x.agentName });
      rm.onclick = () => askForgetSaved(x);
      r.lastChild.append(" ", rm);
    }
    box.append(r);
  }
}

// askForgetSaved asks before magpie drops the accounts it saved of an agent
// signed out here, as a reset asks; it forgets only magpie's copies, never
// the agent's own files or keychain. It is the reset's dialog, so the
// backdrop and Escape close it the same way.
function askForgetSaved(x) {
  const ed = el("div", "editor forget-ask");
  const head = el("div", "ehead");
  head.append(icon(x.agentIcon), el("b", "", t(x.users.length === 1 ? "Remove {agent}'s saved account?" : "Remove {agent}'s {n} saved accounts?", { agent: x.agentName, n: x.users.length })));
  ed.append(head);
  ed.append(el("p", "lib-confirm", t("magpie forgets its copy of {users}. {agent}'s own files and sign-in, and the account itself, are left as they are.", { users: x.users.join(", "), agent: x.agentName })));
  const bar = el("div", "bar");
  const go = el("button", "text primary danger-fill", t("Remove"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    for (const [i, user] of x.users.entries()) {
      const last = i === x.users.length - 1;
      if (!await accountAction("login/forget", { agent: x.agent, user }, last ? t("{user} removed", { user: x.users.join(", ") }) : "")) {
        go.disabled = false;
        go.classList.remove("busy");
        return;
      }
    }
    closeResetAsk();
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = (e) => { e.stopPropagation(); closeResetAsk(); };
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  resetAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  cancel.focus();
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
  if (a.agent === "workbuddy") return a.plan || "WorkBuddy";
  if (a.agent === "workbuddy-ai") return a.plan || "WorkBuddy AI";
  if (a.agent === "commandcode-plan") return "Command Code" + (a.plan ? " " + t(a.plan) : "");
  if (a.agent === "qoder") return "Qoder" + (a.plan ? " " + t(a.plan) : "");
  if (a.agent === "zed") return "Zed" + (a.plan ? " " + t(a.plan) : "");
  if (a.agent === "mimo-app") return "Xiaomi MiMo" + (a.plan ? " " + t(a.plan) : "");
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
async function copy(text, what, btn, message) {
  const done = () => { status(message || t("{what} copied", { what }), "ok"); flashCopied(btn); };
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

function copyBtn(text, what, message) {
  const b = el("button", "copy");
  b.title = t("Copy");
  b.append(svg(COPY_ICON, 12, 1.5));
  b.onclick = (ev) => { ev.stopPropagation(); copy(text, what, b, message); };
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
  connect.hidden = connectFolded;
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
  for (const p of providers.providers) for (const m of p.models) if (m.on) out.push({ id: `${p.id}/${m.id}`, name: m.name, provider: p, context: m.context });
  return out;
}

let segsMade = 0;
function segs(items, current, onPick) {
  const box = el("div", "segs");
  const kind = items.map(([id]) => id).join("|");
  box.dataset.kind = kind;
  // where its thumb was is remembered per control, not per set of options:
  // every Off/On on the settings page has the same, and each would slide in
  // from where the one clicked was as the page redraws. A control is known
  // by its place: the nearest element with an id, and which of the same
  // options it is there; one not in the page yet by itself only.
  let key = kind + "#" + ++segsMade;
  for (const [id, name] of items) {
    const b = el("button", "opt" + (id === current ? " on" : ""), name);
    b.onclick = () => { for (const x of box.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(box, key); onPick(id); };
    box.append(b);
  }
  queueMicrotask(() => { // once it is in the page
    const home = box.isConnected && box.parentElement.closest("[id]");
    if (home) key = kind + "@" + home.id + ":" + [...home.querySelectorAll(".segs")].filter((x) => x.dataset.kind === kind).indexOf(box);
    slide(box, key);
  });
  return box;
}

// Connect is set up once and seldom looked at again, so it folds away under
// its heading, the base URL left beside it; the fold is remembered
// (connectFolded, with the tab's other choices at the top).
$("#foldConnect").prepend(svg(CHEV_R, 11, 1.6));
$("#foldConnect").onclick = () => {
  connectFolded = !connectFolded;
  try { localStorage.setItem("magpie.gwConnectFolded", connectFolded ? "1" : "0"); } catch {}
  renderConnect();
  backToReader($("#view-gateway"));
};

function renderConnect() {
  const g = providers.gateway;
  const box = $("#connect");
  box.replaceChildren();
  const models = gatewayModels();
  if (!models.some((m) => m.id === exampleModel)) exampleModel = models[0]?.id || "";
  const model = exampleModel || "provider/model";
  const f = FLAVORS[flavor] || FLAVORS.openai;
  const base = f.base(g.url);
  const fold = $("#foldConnect");
  fold.setAttribute("aria-expanded", String(!connectFolded));
  fold.title = t(connectFolded ? "Show how to connect" : "Fold Connect away");
  box.hidden = connectFolded;
  // folded, the head keeps the one thing reached for: the address, to copy
  const note = $("#connectNote");
  note.replaceChildren();
  note.classList.toggle("brief", connectFolded);
  if (connectFolded) note.append(el("code", "", base), copyBtn(base, "Base URL"));
  else note.textContent = t("Loopback only · the key can be anything");

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
    : /("(?:[^"\\]|\\.)*"|'[^']*')|(#.*)|(\b\d+(?:\.\d+)?\b(?=[,\s\]}]))|(-{1,2}[A-Za-z][\w-]*)|([A-Z][A-Z0-9_]+)(?==)|([A-Za-z_][\w.]*)(?=\s*\()|([A-Za-z_][\w.]*)|(\\\n|`\n)|(\s+|.)/g;
  const out = document.createDocumentFragment();
  const kw = KEYWORDS[lang] || KEYWORDS.shell;
  let m;
  while ((m = re.exec(code))) {
    // an unquoted value after "=" is a string too; found here, as a
    // lookbehind in the pattern is a syntax error before Safari 16.4 (#220)
    if (lang !== "node" && !m[1] && code[m.index - 1] === "=") {
      const v = /\S+/y;
      v.lastIndex = m.index;
      const s = v.exec(code);
      if (s) {
        out.append(el("span", "tk-s", s[0]));
        re.lastIndex = m.index + s[0].length;
        continue;
      }
    }
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
// The model list can be folded away, so Recent calls sits under Connect;
// the fold is remembered.
let modelsFolded = false;
try { modelsFolded = localStorage.getItem("magpie.gwModelsFolded") === "1"; } catch {}
$("#foldModels").prepend(svg(CHEV_R, 11, 1.6));
$("#foldModels").onclick = () => {
  modelsFolded = !modelsFolded;
  try { localStorage.setItem("magpie.gwModelsFolded", modelsFolded ? "1" : "0"); } catch {}
  renderGatewayModels();
  backToReader($("#view-gateway"));
};

function renderGatewayModels() {
  const list = $("#gwModels");
  list.replaceChildren();
  const all = gatewayModels();
  const fold = $("#foldModels");
  fold.setAttribute("aria-expanded", String(!modelsFolded));
  fold.title = t(modelsFolded ? "Show the models" : "Fold the models away");
  $("#modelsCount").textContent = all.length ? String(all.length) : "";
  list.hidden = modelsFolded && all.length > 0;
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
  q.hidden = all.length < 8 || list.hidden;
  const words = modelQuery.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const models = q.hidden ? all : all.filter((m) => {
    const hay = `${m.id} ${m.name || ""} ${m.provider.name}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  $("#copyModels").hidden = !models.length || list.hidden;
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
    const name = el("div", "name", m.id);
    if (namedFree(m.id, m.name)) name.append(freeBadge(false));
    const ctx = contextTag(m.context, m.name);
    if (ctx) name.append(ctx);
    who.append(name, el("div", "sub", m.name && m.name !== m.id.split("/")[1] ? `${m.name} · ${m.provider.name}` : m.provider.name));
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
    r.append(el("span", "st", c.error ? `${c.status} ${c.error}` : `${c.status} · ${c.ms} ms` + (c.ttft ? " · " + t("TTFT {ms}", { ms: `${c.ttft} ms` }) : "")));
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
    x.onclick = () => rollUpSheet(sheet, () => { adding = false; editing = null; draft = null; presetQuery = ""; renderProviders(); });
    head.append(x);
  }
  sheet.append(head);
  const tiles = el("div", "tiles");
  sheet.append(tiles);
  const drawTiles = () => {
    tiles.replaceChildren();
    const f = presetQuery.trim().toLowerCase();
    const hit = (pr) => !f || pr.name.toLowerCase().includes(f) || pr.id.includes(f) || hostOf(pr.chat || pr.responses || pr.anthropic).includes(f) || (pr.note || "").toLowerCase().includes(f);
    // a section: its name, what it is in a word, and its rows, three to a line
    const section = (title, hint) => {
      const k = el("div", "kind");
      k.append(el("b", "", t(title)));
      if (hint) k.append(el("span", "", t(hint)));
      const grid = el("div", "grid");
      tiles.append(k, grid);
      return grid;
    };
    let any = false;
    const subs = SUBS.filter((x) => !(providers.onPlugins || []).includes(x.agent)).filter((x) => !f || x.name.toLowerCase().includes(f) || x.agent.includes(f) || "subscription".includes(f));
    if (subs.length) {
      any = true;
      const grid = section("Subscriptions", "sign in, no key");
      for (const x of subs) grid.append(subTile(x));
      if (!f) grid.append(morePluginsTile());
      const w = subs.find((x) => signing?.agent === x.agent);
      if (w) tiles.append(renderSigning(w));
    }
    const plugged = pluginSubs().filter((x) => !f || x.name.toLowerCase().includes(f) || x.agent.includes(f) || "plugin".includes(f));
    if (plugged.length) {
      any = true;
      const grid = section("From plugins", "signed in by an OpenCode plugin");
      for (const x of plugged) grid.append(subTile(x));
      const w = plugged.find((x) => signing?.agent === x.agent);
      if (w) tiles.append(renderSigning(w));
    }
    const gone = (providers.excluded || []).filter((x) => x.quiet && x.provider && (!f || x.agentName.toLowerCase().includes(f) || x.agent.includes(f)));
    if (gone.length) {
      any = true;
      const grid = section("Removed from magpie", "still signed in");
      for (const x of gone) {
        const b = pickRow(x.agentIcon, x.agentName);
        b.append(el("span", "st", t("Add it back")));
        b.onclick = () => providerAction("show", { id: x.provider }, t("{name} added back", { name: x.agentName }));
        grid.append(b);
      }
    }
    for (const [kind, title, hint] of [["vendor", "Vendors", "the makers' own APIs"], ["relay", "Relays", "one key, many vendors"], ["local", "On this machine", ""]]) {
      // a vendor's China endpoint is a preset of its own: one row with the global one
      const rows = providers.presets.filter((p) => p.kind === kind && !globalOf(p))
        .map((p) => [p, chinaOf(p)]).filter(([p, cn]) => hit(p) || (cn && hit(cn)));
      if (!rows.length) continue;
      any = true;
      const grid = section(title, hint);
      for (const [pr, cn] of rows) grid.append(cn ? pairTile(pr, cn) : tile(pr));
    }
    if (!any) {
      const none = el("div", "none");
      none.append(t("Nothing called “{q}”. ", { q: presetQuery.trim() }));
      const b = el("button", "link", t("Add it as a custom provider"));
      b.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
      const pl = el("button", "link", t("look for a plugin"));
      pl.onclick = () => openPlugins(presetQuery.trim());
      none.append(b, t(", or "), pl);
      tiles.append(none);
    } else if (!f) {
      // a vendor not listed: one line under them all
      const foot = el("div", "custom-foot");
      const c = el("button", "custom" + (editing?.custom ? " on" : ""));
      c.append(svg(PLUS, 13, 1.8), el("span", "", t("Custom provider")));
      c.dataset.pick = "custom";
      c.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
      foot.append(c, el("span", "hint", t("any OpenAI or Anthropic compatible URL")));
      tiles.append(foot);
    }
  };
  drawTiles();
  return editing && typeof editing === "object" ? renderEditor(null, editing.preset) : null;
}

// pickRow is one row of the add sheet: an icon and a name, nothing framing
// them; what more there is to say goes in its title.
function pickRow(ic, name, cls = "") {
  const b = el("button", "tile" + cls);
  b.dataset.pick = name; // the dialog it opens folds back into it, re-rendered
  const nm = el("span", "nm");
  nm.append(el("span", "n", name));
  b.append(icon(ic), nm);
  return b;
}

// morePluginsTile: the add sheet's way to the Plugins tab, where more
// subscriptions are, each signed in to by a plugin
function morePluginsTile() {
  const b = el("button", "tile more-plugins");
  b.dataset.pick = "plugins";
  const nm = el("span", "nm");
  nm.append(el("span", "n", t("More in Plugins")));
  const ic = el("span", "puzzle");
  ic.append(svg(PUZZLE, 15, 1.4));
  b.append(ic, nm, svg(CHEV_R, 11, 1.6));
  b.title = t("Subscriptions magpie doesn't sign in to itself: install a plugin for one in the Plugins tab");
  b.onclick = () => openPlugins();
  return b;
}

// openPlugins: the Plugins tab, looking for q when there is one
function openPlugins(q) {
  if (mode !== "window") { api("window/main?view=plugins", {}).catch(() => {}); return; }
  show("plugins");
  window.pluginQuery?.(q || "");
}

// pluginSignIn: a plugin's provider signed in to as every subscription is,
// in the Providers add sheet
function pluginSignIn(id) {
  closeModal();
  show("providers");
  adding = true; editing = null; draft = null; presetQuery = "";
  renderProviders();
  startSignIn(id);
}

// openProvider: a provider opened in the Providers tab
function openProvider(id) {
  closeModal();
  show("providers");
  adding = false; editing = id; draft = null; presetQuery = "";
  renderProviders();
  syncURL();
}

// a row already added: a green dot after its name, and how many accounts
// when a subscription has more than one
function markAdded(b, n = 1) {
  b.classList.add("added");
  const nm = b.querySelector(".nm");
  nm.append(el("span", "have"));
  if (n > 1) nm.append(el("span", "cnt", String(n)));
}

// the add sheet names a row without what its title tells: a subscription's
// plan in brackets, a preset's long name
const shortName = (name) => name.replace(/\s*[(（][^()（）]*[)）]\s*$/, "") || name;

// a vendor's China endpoint is a preset of its own, id-cn beside the global id
const chinaOf = (pr) => providers.presets.find((x) => x.id === pr.id + "-cn");
const globalOf = (pr) => pr.id.endsWith("-cn") ? providers.presets.find((x) => x.id === pr.id.slice(0, -3)) : null;

// pairTile is a vendor's global and China presets as one row; the editor
// switches between them. A click adds the one not added yet, or opens the
// provider when both are.
function pairTile(pr, cn) {
  const both = [pr, cn];
  const b = pickRow(pr.icon || "generic", shortName(pr.short || pr.name), both.some((x) => editing?.preset === x.id) ? " on" : "");
  const added = both.filter((x) => x.added);
  b.title = both.map((x) => t(x === pr ? "Global" : "China") + " " + hostOf(x.chat || x.responses || x.anthropic) + (x.added ? " · " + t("Added") : "")).join("\n");
  if (added.length) markAdded(b);
  const next = both.find((x) => !x.added);
  b.onclick = () => {
    if (next) { editing = { preset: next.id }; draft = null; }
    else { editing = presetProvider(pr)?.id ?? pr.id; draft = null; }
    renderProviders();
  };
  return b;
}

// the first provider made from a preset, which may not have the preset's id
const presetProvider = (pr) => providers.providers.find((p) => p.preset === pr.id) || providers.providers.find((p) => p.id === pr.id);

// subTile adds a subscription: one more account when the agent has some.
function subTile(x) {
  const have = providers.providers.find((p) => p.account?.agent === x.agent);
  const n = have ? (have.account.logins?.length || 1) : 0;
  const b = pickRow(x.icon, shortName(x.name), signing?.agent === x.agent ? " on" : "");
  b.title = t("{name} subscription", { name: x.name }) + " · " + x.plans;
  if (n) {
    markAdded(b, x.single ? 1 : n);
    b.title += "\n" + (x.single ? t("Signed in") : t(n === 1 ? "1 account" : "{n} accounts", { n })) + " · " + t(x.single ? "signed in · click to switch account" : "click to add another account");
  }
  b.onclick = () => startSignIn(x.agent);
  return b;
}

function tile(pr) {
  const b = pickRow(pr.icon || "generic", pr.short || pr.name, editing?.preset === pr.id ? " on" : "");
  if (pr.sponsored) b.querySelector(".nm").append(el("span", "badge", t("sponsored")));
  b.title = (pr.short ? pr.name + " · " : "") + (pr.note ? t(pr.note) : hostOf(pr.chat || pr.responses || pr.anthropic));
  if (pr.added) {
    markAdded(b);
    b.title = t("{name} is already added — open it", { name: pr.name });
    const have = presetProvider(pr);
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

// proxyPicker: the proxy one provider's requests go through (#237) — the
// one in Settings, none, or its own — so Codex can go through a proxy
// while a vendor at home goes direct. The draft keeps the choice as
// proxyMode ("" global, "direct", "custom") and the address as proxyURL;
// proxyOfDraft is what is saved.
const PROXY_HINT = {
  "": "Follows the proxy in Settings",
  direct: "Requests to it go direct, whatever the proxy in Settings",
  custom: "Requests to it go through this proxy: http://, https:// or socks5://",
};
function proxyPicker() {
  const box = el("div", "stack proxy-pick");
  const hint = el("div", "hint", t(PROXY_HINT[draft.proxyMode || ""]));
  const addr = input(draft.proxyURL || "", "http://127.0.0.1:7890");
  addr.className = "proxy-url";
  addr.classList.toggle("off", draft.proxyMode !== "custom");
  addr.oninput = () => { draft.proxyURL = addr.value; };
  const seg = segs([["", t("Global proxy")], ["direct", t("Direct")], ["custom", t("Custom")]], draft.proxyMode || "", (v) => {
    draft.proxyMode = v;
    addr.classList.toggle("off", v !== "custom");
    hint.textContent = t(PROXY_HINT[v]);
  });
  seg.classList.add("proxy-mode");
  // the address sits beside the options, its room kept while it is not
  // asked for, so picking one never changes the dialog's height (a
  // centred dialog would move under the pointer)
  const row = el("div", "proxy-row");
  row.append(seg, addr);
  box.append(row, hint);
  return box;
}
function proxyDraft(p) {
  const v = (p?.proxy || "").trim();
  // each account's own (accountProxies), by its name in lower case
  const accountProxies = {};
  for (const [u, x] of Object.entries(p?.accountProxies || {})) accountProxies[u] = { mode: x === "direct" ? "direct" : "custom", url: x === "direct" ? "" : x };
  return { proxyMode: !v ? "" : v === "direct" ? "direct" : "custom", proxyURL: v && v !== "direct" ? v : "", accountProxies };
}
// proxyOfDraft is the draft's proxy as it is saved, or null when Custom
// has no address yet.
function proxyOfDraft() {
  if (draft.proxyMode === "direct") return "direct";
  if (draft.proxyMode !== "custom") return "";
  return (draft.proxyURL || "").trim() || null;
}

// accountProxyPicker: one proxy per account of a subscription holding
// several (gakki: one Codex account through one proxy, another through
// another) — the provider's own (the row above), none, or its own address.
// Each account is a line of its name over the same row as the provider's,
// the address's room kept while it isn't asked for, so a pick moves
// nothing. null for a subscription with one account: the row above is its.
const ACCOUNT_PROXY_HINT = "Each account can go through a proxy of its own; Provider's proxy is the one above";
function accountProxyPicker(a) {
  const ls = [...(a?.logins || [])];
  if (ls.length < 2) return null;
  ls.sort((x, y) => (y.active ? 1 : 0) - (x.active ? 1 : 0));
  draft.accountProxies = draft.accountProxies || {};
  const box = el("div", "acct-proxies");
  for (const l of ls) {
    const k = l.user.toLowerCase();
    const cur = draft.accountProxies[k] || { mode: "", url: "" };
    const line = el("div", "acct-proxy");
    line.dataset.user = l.user;
    const who = el("span", "who", l.user);
    who.title = l.user;
    const addr = input(cur.url || "", "http://127.0.0.1:7890");
    addr.className = "proxy-url";
    addr.classList.toggle("off", cur.mode !== "custom");
    addr.oninput = () => { draft.accountProxies[k] = { ...(draft.accountProxies[k] || { mode: "custom" }), url: addr.value }; };
    const seg = segs([["", t("Provider's proxy")], ["direct", t("Direct")], ["custom", t("Custom")]], cur.mode || "", (v) => {
      draft.accountProxies[k] = { ...(draft.accountProxies[k] || {}), mode: v };
      addr.classList.toggle("off", v !== "custom");
    });
    seg.classList.add("proxy-mode");
    const row = el("div", "proxy-row");
    row.append(seg, addr);
    line.append(who, row);
    box.append(line);
  }
  box.append(el("div", "hint", t(ACCOUNT_PROXY_HINT)));
  return box;
}
// accountProxiesOfDraft is each account's own proxy as it is saved — the
// accounts that follow the provider's left out — or { missing: user } when
// one's Custom has no address yet.
function accountProxiesOfDraft() {
  const out = {};
  for (const [u, x] of Object.entries(draft.accountProxies || {})) {
    if (x.mode === "direct") out[u] = "direct";
    else if (x.mode === "custom") {
      const v = (x.url || "").trim();
      if (!v) return { missing: u };
      out[u] = v;
    }
  }
  return { map: out };
}

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
    // the site's own icon, looked for from the base URL typed above
    const site = el("button", "text", t("From the website"));
    site.title = t("Look for the icon of the site the base URL is on");
    site.onclick = async () => {
      const base = draft[apiField[draft.api]] || draft.chat || draft.responses || draft.anthropic || "";
      site.disabled = true;
      site.textContent = t("Looking…");
      try {
        const data = await api("icons/favicon", { url: base });
        draft.icon = data.icon;
        editorError("");
        draw();
        syncHead();
      } catch (e) {
        editorError(e.message);
        site.disabled = false;
        site.textContent = t("From the website");
      }
    };
    const builtin = el("button", "text", t("Built-in icons"));
    builtin.onclick = () => { open = !open; draw(); };
    box.append(file, choose, site, builtin);
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
// or Cancel close it. As on iOS it grows out of what was pressed, on a
// spring, and closing folds it back into that (still there, re-rendered or
// not); with nothing pressed it rises from a little below.
const SPRING = CSS.supports?.("animation-timing-function", "linear(0, 1)")
  // a damped spring (response .42 s, damping .8): 1.5% over, settled at 570 ms
  ? "linear(0, 0.0203, 0.0723, 0.1448, 0.2292, 0.3188, 0.4086, 0.4951, 0.576, 0.6498, 0.7157, 0.7735, 0.8232, 0.8654, 0.9005, 0.9293, 0.9525, 0.9708, 0.9849, 0.9956, 1.0033, 1.0087, 1.0122, 1.0142, 1.015, 1.0151, 1.0146, 1.0136, 1.0124, 1.0111, 1.0097, 1.0084, 1.0071, 1.0059, 1.0049, 1.0039, 1.0031, 1.0024, 1.0018, 1.0013, 1)"
  : "cubic-bezier(.2, .9, .25, 1.02)";
const IOS_EASE = "cubic-bezier(.32, .72, 0, 1)";
const calm = () => matchMedia("(prefers-reduced-motion: reduce)").matches;
// what was last pressed, and a way to find it again once re-rendered
let pressed = null;
document.addEventListener("pointerdown", (e) => {
  const at = e.target.closest?.("[data-pick], .row.provider[data-id], button");
  if (!at || at.closest("#modal")) return;
  const find = at.dataset.pick ? `[data-pick="${CSS.escape(at.dataset.pick)}"]` : at.matches(".row.provider") ? `.row.provider[data-id="${CSS.escape(at.dataset.id)}"]` : null;
  pressed = { el: at, find, time: performance.now() };
}, true);
let modalOrigin = null, modalDone = null;
// originRect: where the dialog came from, as it is now, or null when it's gone
function originRect(o) {
  const at = o && (o.el.isConnected ? o.el : o.find ? document.querySelector(o.find) : null);
  if (!at || at.closest("[hidden]")) return null;
  const r = at.getBoundingClientRect();
  return r.width && r.height && r.bottom > 0 && r.top < innerHeight ? r : null;
}
// fromRect: the transform putting the dialog (at `to`) over the rect `from`
function fromRect(from, to) {
  const s = Math.max(.3, Math.min(1, from.width / to.width));
  const dx = from.left + from.width / 2 - (to.left + to.width / 2), dy = from.top + from.height / 2 - (to.top + to.height / 2);
  return `translate(${dx}px, ${dy}px) scale(${s})`;
}
function openModal(content) {
  const m = $("#modal"), d = m.firstElementChild;
  const fresh = m.hidden || m.classList.contains("out");
  const top = m.hidden ? 0 : d.querySelector(".ebody")?.scrollTop || 0;
  m.classList.remove("out");
  d.classList.remove("swap");
  if (!fresh) { void d.offsetWidth; d.classList.add("swap"); } // content changed: a soft refresh, not a re-entrance
  frame(content);
  d.replaceChildren(content);
  m.hidden = false;
  const body = content.querySelector(":scope > .ebody");
  if (body) body.scrollTop = top; // a re-render keeps the place
  if (!fresh) return;
  for (const a of [...m.getAnimations(), ...d.getAnimations()]) a.cancel();
  d.style.opacity = d.style.transform = m.style.opacity = "";
  modalDone = null;
  modalOrigin = pressed && performance.now() - pressed.time < 1000 ? pressed : null;
  pressed = null;
  m.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 280, easing: "ease-out" });
  if (calm()) { d.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 160 }); return; }
  const to = d.getBoundingClientRect(), from = originRect(modalOrigin);
  d.animate([{ transform: from ? fromRect(from, to) : "translateY(24px) scale(.94)" }, { transform: "none" }], { duration: 570, easing: SPRING });
  d.animate([{ opacity: 0 }, { opacity: 1 }], { duration: from ? 200 : 240, easing: "ease-out" });
}
// frame holds an editor's head and its buttons still while the fields
// between them scroll.
function frame(ed) {
  if (!ed.matches(".editor") || ed.querySelector(":scope > .ebody")) return;
  const body = el("div", "ebody");
  body.append(...[...ed.children].filter((c) => !c.matches(".ehead, .bar")));
  ed.querySelector(":scope > .bar") ? ed.querySelector(":scope > .bar").before(body) : ed.append(body);
  ed.classList.add("framed");
}
function closeModal() {
  const m = $("#modal"), d = m.firstElementChild;
  if (m.hidden) return Promise.resolve();
  if (m.classList.contains("out")) return modalDone || Promise.resolve();
  m.classList.add("out");
  for (const a of [...m.getAnimations(), ...d.getAnimations()]) a.commitStyles?.(), a.cancel();
  // where it would sit at rest, whatever an opening cut short left it at
  const was = d.style.transform;
  d.style.transform = "none";
  const to = d.getBoundingClientRect(), from = calm() ? null : originRect(modalOrigin);
  d.style.transform = was;
  const shape = { duration: from ? 380 : 260, easing: IOS_EASE, fill: "forwards" };
  const moves = [
    m.animate([{ opacity: 0 }], { ...shape, easing: "ease-out" }),
    calm() ? d.animate([{ opacity: 0 }], { duration: 140, fill: "forwards" })
      : d.animate([{ transform: from ? fromRect(from, to) : "translateY(14px) scale(.95)" }], shape),
  ];
  // it fades as it lands, the last part of the way
  if (!calm()) moves.push(d.animate([{ offset: from ? .45 : .2, opacity: getComputedStyle(d).opacity }, { opacity: 0 }], shape));
  const done = modalDone = Promise.all(moves.map((a) => a.finished)).then(() => {
    if (modalDone !== done) return;
    modalDone = null;
    m.hidden = true;
    m.classList.remove("out");
    d.replaceChildren();
    for (const a of [...m.getAnimations(), ...d.getAnimations()]) a.cancel();
    d.style.opacity = d.style.transform = m.style.opacity = "";
  }, () => {});
  return done;
}
$("#modal").onclick = (e) => { if (e.target === e.currentTarget) cancelEdit(); };

// ---------- sliding thumb ----------
// Pills (the nav, every segmented control) have one thumb that glides to the
// selected option instead of each option lighting up on its own.
const thumbs = new Map(); // control position → where a re-rendered thumb resumes its slide
function thumbKey(box, choices) {
  const path = [];
  for (let node = box; node?.parentElement; node = node.parentElement) {
    if (node.id) return node.id + "/" + path.reverse().join("/") + ":" + choices;
    path.push(Array.prototype.indexOf.call(node.parentElement.children, node));
  }
  return null;
}
function slide(box, key) {
  let th = box.querySelector(":scope > .thumb");
  const fresh = !th;
  if (fresh) { th = el("span", "thumb"); box.prepend(th); }
  const on = box.querySelector(":scope > .on");
  if (!on) { th.style.opacity = "0"; return; }
  th.style.opacity = "";
  const to = { x: on.offsetLeft, w: on.offsetWidth };
  const control = thumbKey(box, key);
  const last = control && thumbs.get(control);
  let from = to;
  const put = (p) => { th.style.transform = `translateX(${p.x}px)`; th.style.width = p.w + "px"; };
  if (fresh) {
    from = last ? (performance.now() - last.at < 300 ? last.from : last) : to;
    th.classList.add("still");
    put(from);
    void th.offsetWidth;
    th.classList.remove("still");
  } else if (last) from = { x: last.x, w: last.w };
  put(to);
  if (control) thumbs.set(control, { ...to, from, at: performance.now() });
}

const PROTOS = [["chat", "OpenAI", "Chat Completions — most agents"], ["responses", "Responses", "OpenAI Responses — what Codex speaks"], ["anthropic", "Anthropic", "Anthropic Messages — what Claude Code speaks"], ["decide", "Jev", "Jev's decision API (TypeSafe's, or a gateway's) — what a routing group asks as a turn begins"]];

// draftOf is a saved provider as its editor's form holds it.
function draftOf(p) {
  return { id: p.id, name: p.name, preset: p.preset, chat: p.chat, responses: p.responses, anthropic: p.anthropic, catalog: p.catalog, key: "", api: p.chat ? "openai" : p.anthropic ? "anthropic" : p.responses ? "responses" : "openai", chosen: p.models.filter((m) => m.on).map((m) => m.id), extra: [], headers: headerRows(p.headers), icon: p.icon || "", fallback: [...(p.fallback || [])], unlisted: !!p.unlisted, balanceURL: p.balanceURL || "", balancePath: p.balancePath || "", modelsURL: p.modelsURL || "", contexts: contextsText(p.contexts), keysUrl: p.keysUrl || "", ...proxyDraft(p) };
}

// duplicateProvider opens the Add form on a copy of p (#268): its URLs,
// headers, models, balance and proxy, under a name of its own. The key is
// p's unless another is pasted: the Add takes it from p, with its other
// keys (copyOf). A signed-in account has no copy.
function duplicateProvider(p) {
  const name = t("{name} copy", { name: p.name });
  adding = true;
  editing = p.preset && providers.presets.some((x) => x.id === p.preset) ? { preset: p.preset } : { custom: true };
  const d = draftOf(p);
  draft = { ...d, id: slug(name), name, chosen: [], extra: d.chosen, copyOf: p.id };
  if (p.zhipuTeam) draft.zhipuTeam = { org: p.zhipuTeam.org || "", project: p.zhipuTeam.project || "" };
  renderProviders();
}

// renderEditor: an existing provider (p), a new preset (presetID), or custom.
function renderEditor(p, presetID) {
  // its own icons, not the page's kept ones, which the rows after it take back
  const kept = keptIcons;
  keptIcons = null;
  try { return drawEditor(p, presetID); } finally { keptIcons = kept; }
}
function drawEditor(p, presetID) {
  const pr = presetID ? providers.presets.find((x) => x.id === presetID) : p?.preset ? providers.presets.find((x) => x.id === p.preset) : null;
  const isNew = !p, custom = !pr && !p?.account, decides = !!(p?.decide || pr?.decide);
  // a preset already added is added again only through "Add another": one
  // more provider of it, under a name and id of its own
  const another = isNew && !!pr?.added;
  draft = draft || (p
    ? draftOf(p)
    : pr
      ? { id: pr.id, name: pr.name, preset: pr.id, key: "", chosen: [], extra: [], headers: [] }
      : { id: "", name: "", preset: "", chat: "", responses: "", anthropic: "", catalog: "", key: "", api: "openai", chosen: [], extra: [], headers: [], icon: "" });
  const ed = el("div", "editor" + (isNew ? " new" : ""));
  ed.onclick = (e) => e.stopPropagation();

  {
    const h = el("div", "ehead");
    const copyOf = draft.copyOf && providers.providers.find((x) => x.id === draft.copyOf);
    h.append(icon(p?.icon || (copyOf && draft.icon) || pr?.icon || "generic"), el("b", "", p ? p.name : copyOf ? t("Copy of {name}", { name: copyOf.name }) : pr ? pr.name : t("Custom provider")));
    if (pr?.note) h.append(el("span", "note", t(pr.note)));
    h.append(el("span", "grow"));
    const site = pr?.website || p?.website || (p?.host ? "https://" + p.host : "");
    if (site) { const b = el("button", "link", hostOf(site) + " ↗"); b.onclick = () => api("open", { url: site }); h.append(b); }
    if (p) h.append(providerSwitch(p));
    ed.append(h);
    if (p?.off) ed.append(el("div", "hint off-note", t("Switched off: agents aren't given its models and no request goes to it. Its keys and settings are kept; switch it on to use it again.")));
  }

  // a vendor's global and China endpoints are presets of their own, one row
  // in the add sheet: here the new provider picks between them, the key kept
  const pair = isNew && pr ? (globalOf(pr) ? [globalOf(pr), pr] : chinaOf(pr) ? [pr, chinaOf(pr)] : null) : null;
  if (pair) {
    const seg = el("div", "segs area");
    pair.forEach((x, i) => {
      const b = el("button", "opt" + (x.id === pr.id ? " on" : ""), t(i ? "China" : "Global"));
      b.title = hostOf(x.chat || x.responses || x.anthropic) + (x.added ? " · " + t("Added") : "");
      b.onclick = () => {
        if (x.id === pr.id) return;
        editing = { preset: x.id };
        draft = { id: x.id, name: x.name, preset: x.id, key: draft.key, chosen: [], extra: [], headers: [] };
        renderProviders();
      };
      seg.append(b);
    });
    queueMicrotask(() => slide(seg, "area"));
    ed.append(...field(t("Region"), seg, ""));
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
        // each protocol keeps its own URL (#105). One typed here and not
        // saved moves to a protocol without one, spelled as that protocol
        // wants it: the kind was picked after the URL (#73). A saved URL
        // stays where it is, and one not given yet stays empty.
        const from = apiField[draft.api], to = apiField[v];
        if (!draft[to] && draft[from] && draft[from] !== (p?.[from] || "")) {
          draft[to] = respellURL(draft[from], v);
          draft[from] = p?.[from] || "";
        }
        draft.api = v;
        url.value = draft[to] || "";
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
      ed.append(...field(t("Accounts"), renderAccounts(a), p.routing ? t("Tick every account to use; Routing says how requests spread over them.") : subOf(a.agent).single ? t("{agent} keeps one account; the gateway runs it for every request. Signing in to another replaces it.", { agent: a.agentName }) : subOf(a.agent).plugin ? t("The gateway uses the first. Tick more and it moves on to the next when the one before it is out of quota.") : subOf(a.agent).own ? t("The gateway uses the first. Tick more and it moves on to the next when the one before it is out of quota. {agent} itself stays signed in as it is.", { agent: a.agentName }) : t("{agent} signs in to the first. Tick more and the gateway moves on to the next when the one before it is out of quota. Sessions already running keep theirs until restarted.", { agent: a.agentName })));
      if ((a.logins || []).filter((l) => l.active || l.on).length > 1) ed.append(...renderRouting(p));
      if (p.move) ed.append(...renderMove(p));
    } else {
      const acct = el("div", "acct");
      acct.append(icon(a.agentIcon), el("span", "n", a.user), el("span", "plan", accountPlan(a)));
      ed.append(...field(t("Account"), acct, t("{agent}'s sign-in, read from its own files. Sign out there and this provider goes away.", { agent: a.agentName })));
    }
    ed.append(...field(t("Models"), renderModels(p), ""));
    if (p.drawIds?.length) ed.append(...renderDrawers(p));
    // a subscription's window too: Codex's backend says 272K for models
    // that take 872K (#120)
    const cx = input(draft.contexts || "", t("e.g. 128k · or gpt-6=1m, comma separated"));
    ed.append(...field(t("Context window"), contextPicks(p, cx), t("How long a request the models take, told to the agents; empty leaves it to the vendor and models.dev")));
    ed.append(...field(t("Fallback"), renderFallback(p), fallbackHint(p)));
    const proxies = el("div", "stack");
    proxies.append(proxyPicker());
    const perAccount = subOf(a.agent) ? accountProxyPicker(a) : null;
    if (perAccount) proxies.append(perAccount);
    ed.append(...field(t("Proxy"), proxies));
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
    saveBtn.onclick = () => {
      const cx = parseContexts(draft.contexts || "");
      if (cx.error) return editorError(t("Context window: {v} is not a length like 128k or 1m", { v: cx.error }), "warn");
      const proxy = proxyOfDraft();
      if (proxy === null) { ed.querySelector(".proxy-url")?.focus(); return editorError(t("Proxy: type its address, like http://127.0.0.1:7890"), "warn"); }
      const own = accountProxiesOfDraft();
      if (own.missing) {
        const line = [...ed.querySelectorAll(".acct-proxy")].find((x) => x.dataset.user.toLowerCase() === own.missing);
        line?.querySelector(".proxy-url")?.focus({ preventScroll: true });
        return editorError(t("Proxy of {user}: type its address, like http://127.0.0.1:7890", { user: line?.dataset.user || own.missing }), "warn");
      }
      saveBtn.classList.add("busy"); providerAction("save", { id: p.id, models: chosenIds(), unlisted: draft.unlisted, fallback: draft.fallback, contexts: cx.map, proxy, accountProxies: own.map }, t("{name} saved", { name: p.name })); };
    bar.append(cancel, saveBtn);
    ed.append(bar);
    return ed;
  }

  // a vendor reached at the user's own resource (Azure OpenAI): no URL of
  // the preset's, the one the resource is at is typed or pasted here, and
  // magpie puts it on the API the preset speaks when it is saved
  let endpoint = null;
  if (pr?.endpoint) {
    if (draft.chat === undefined) { draft.chat = p?.chat || ""; draft.responses = p?.responses || ""; }
    endpoint = input(draft.chat || draft.responses || "", pr.endpoint, "url");
    endpoint.classList.add("endpoint");
    endpoint.oninput = () => { draft.chat = draft.responses = endpoint.value.trim(); refreshEndpoints(); };
    ed.append(...field(t("Endpoint"), endpoint, pr.endpointHint ? t(pr.endpointHint) : ""));
  }

  // null, not false, when there is none: false?.key is undefined, and a
  // provider with no key read .set of it (willz: a local Ollama without a
  // key opened no editor, its row just toggling)
  const copied = (!p && draft.copyOf && providers.providers.find((x) => x.id === draft.copyOf)) || null;
  const key = input(draft.key || "", p?.key.set ? t("{masked} · paste a new key to replace it", { masked: p.key.masked }) : copied?.key.set ? t("{masked} · {name}'s key, or paste another", { masked: copied.key.masked, name: copied.name }) : t(pr?.noKey || p?.key.optional ? "optional for local servers" : "paste an API key"), "password");
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
  // the link follows the plan picked: a region's keysUrl goes with its
  // endpoints, and one without falls back to the preset's own page
  if (keysUrl) { const b = el("button", "link", t("Get a key ↗")); b.onclick = () => api("open", { url: draft?.keysUrl || pr?.keysUrl || p?.keysUrl }); side.append(b); }
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
  ed.append(...field(t("Proxy"), proxyPicker()));

  // a vendor that tells the whole account's balance only to a token of its
  // own (AiHubMix's system access token), where a key knows just its own
  // — or a custom provider's, whose Balance URL may not be named yet: a
  // token pasted there says at once which URL it wants (balanceFix)
  let balFix = null;
  if (p?.balanceToken?.takes || custom) {
    const saved = !!p?.balanceToken?.set;
    const tok = input(draft.balanceToken || "", saved && !draft.clearBalanceToken ? t("saved · paste a new one to replace it") : t("optional · the account's system access token"), "password");
    tok.oninput = () => { draft.balanceToken = tok.value.trim(); };
    tok.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") cancelEdit(); };
    const pair = el("div", "pair");
    pair.append(tok);
    if (saved && !draft.clearBalanceToken) {
      const side = el("div", "side");
      const drop = el("button", "text", t("Remove"));
      drop.onclick = () => { draft.clearBalanceToken = true; draft.balanceToken = ""; tok.value = ""; tok.placeholder = t("optional · the account's system access token"); drop.remove(); balFix?.refresh(); };
      side.append(drop);
      pair.append(side);
    }
    const tokHelp = custom || p.balanceURL
      ? t("What the Balance URL is asked with in place of the key, when it wants the account's own token: a new-api relay's System Access Token, or a sub2api panel's login token (a JWT, sent as a Bearer); it is used for nothing else.")
      : t("A key tells only what is left on itself. For the whole account's balance on the Usage page, generate a System Access Token in {p}'s settings and paste it here; it is used for nothing else.", { p: pr?.name || p.name });
    const [label, wrap] = field(t("Account balance"), pair, tokHelp);
    if (custom) {
      balFix = balanceFix(p);
      wrap.append(balFix);
      // whatever is typed — the token, the Balance URL, a header — may
      // make it or unmake it
      ed.addEventListener("input", () => balFix.refresh());
    }
    ed.append(label, wrap);
  }

  // StepFun tells a Step Plan's 5-hour, weekly and credit windows only to
  // its platform's sign-in, never to a key: signed in once in a window of
  // magpie's, the Usage page shows them
  if (p?.stepPlan) ed.append(...field(t("Step Plan usage"), renderStepPlan(p.stepPlan), t("A key tells only the balance. The Step Plan's 5-hour, weekly and credit windows are told only to a StepFun sign-in: bring yours here once and magpie keeps it (for 30 days), used for nothing else.")));

  // a key on a team's GLM Coding Plan is told the team's windows only with
  // the team's organization and project, which the console shows (#236)
  const team = p ? !!p.zhipuTeam : !!pr?.zhipuTeam;
  if (team) {
    draft.zhipuTeam = draft.zhipuTeam || { org: p?.zhipuTeam?.org || "", project: p?.zhipuTeam?.project || "" };
    const org = input(draft.zhipuTeam.org, t("optional · only for a team plan"));
    org.classList.add("team-org");
    org.oninput = () => { draft.zhipuTeam.org = org.value; };
    const proj = input(draft.zhipuTeam.project, t("optional · only for a team plan"));
    proj.classList.add("team-project");
    proj.oninput = () => { draft.zhipuTeam.project = proj.value; };
    ed.append(...field(t("Team org ID"), org, ""));
    ed.append(...field(t("Team project ID"), proj, t("Only for a key on a team's GLM Coding Plan: both are in {p}'s console, under the team's organization and project. With them the Usage page shows the team's 5-hour and weekly windows.", { p: pr?.name || p.name })));
  }

  // a relay that offers several regional endpoints, or a vendor whose plans
  // are served at their own: one selector, and the provider's base URLs follow it
  let refreshEndpoints = () => {};
  if (pr?.regions?.length) {
    // Bedrock's ten regions don't fit the editor's width: they scroll
    const seg = el("div", "segs regions");
    const cur = pr.regions.find((r) => r.chat && r.chat === (draft.chat || pr.chat)) || pr.regions[0];
    for (const r of pr.regions) {
      const b = el("button", "opt" + (r.id === cur.id ? " on" : ""), t(r.name));
      b.onclick = () => {
        draft.chat = r.chat || ""; draft.responses = r.responses || ""; draft.anthropic = r.anthropic || "";
        draft.keysUrl = r.keysUrl || "";
        for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b);
        slide(seg, "regions");
        refreshEndpoints();
      };
      seg.append(b);
    }
    queueMicrotask(() => {
      slide(seg, "regions");
      // the region in use in view, where they scroll
      const on = seg.querySelector(":scope > .on");
      if (on && seg.scrollWidth > seg.clientWidth) seg.scrollLeft = on.offsetLeft - (seg.clientWidth - on.offsetWidth) / 2;
    });
    ed.append(...field(t(pr.regionLabel || "Region"), seg, t("which endpoint {p} is reached through", { p: pr.name })));
  }

  if (p) ed.append(...field(t("Models"), renderModels(p), ""));
  if (p?.drawIds?.length) ed.append(...renderDrawers(p));
  {
    // the window agents are told a model has, over what the vendor or
    // models.dev says: one for all of them, and model=size for one
    const cx = input(draft.contexts || "", t("e.g. 128k · or gpt-6=1m, comma separated"));
    if (!decides) ed.append(...field(t("Context window"), contextPicks(p, cx), t("How long a request the models take, told to the agents; empty leaves it to the vendor and models.dev")));
  }
  if (p && !decides) ed.append(...field(t("Fallback"), renderFallback(p), fallbackHint(p)));
  else if (custom) {
    const ex = input(draft.extra.join(", "), t("model ids, comma separated · e.g. gpt-5.5, claude-sonnet-5"));
    ex.oninput = () => { draft.extra = ex.value.split(/[,\s]+/).filter(Boolean); };
    ed.append(...field(t("Models"), ex, t("Optional: magpie asks the vendor for its list after saving.")));
  }

  // Jev's endpoint can be the one its gateway's docs give — Cloudflare's
  // names the account, …/accounts/<id>/ai/run — in place of the preset's
  if (decides) {
    if (draft.decide === undefined) draft.decide = p?.decide || pr?.decide || "";
    const du = input(draft.decide, pr?.decide || "https://…", "url");
    du.oninput = () => { draft.decide = du.value; };
    ed.append(...field(t("Jev endpoint"), du, t("The address Jev is asked at; paste the one from your gateway's docs, e.g. Cloudflare's …/accounts/<account id>/ai/run")));
  }

  if (!custom && !(decides && !p)) {
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
    bal.classList.add("bal-url");
    bal.oninput = () => { draft.balanceURL = bal.value; };
    inner.append(...field(t("Balance URL"), bal, t("Where the vendor tells what is left on the key, asked with it like a chat request; {key} in it or in a header is each key's own, for a vendor that takes the key in the URL (…?key={key}); shown on the Usage page")));
    const balPath = input(draft.balancePath, "data.balance");
    balPath.classList.add("bal-path");
    balPath.oninput = () => { draft.balancePath = balPath.value; };
    // asked as the form has it, before a Save: what the Usage page would show
    const balPair = el("div", "pair");
    balPair.append(balPath);
    if (p) {
      const side = el("div", "side");
      const check = el("button", "text action", t("Check balance"));
      check.title = t("Ask the Balance URL now, as the form has it");
      side.append(check);
      balPair.append(side);
      const res = el("div", "bal-res");
      check.onclick = async () => {
        check.classList.add("busy");
        res.className = "bal-res wait"; res.textContent = "…"; res.title = "";
        const body = { id: p.id, balanceURL: (draft.balanceURL || "").trim(), balancePath: (draft.balancePath || "").trim(), headers: headersOf(draft.headers) };
        if (draft.balanceToken) body.balanceToken = draft.balanceToken;
        else if (draft.clearBalanceToken) body.clearBalanceToken = true;
        try {
          const r = await api("provider/balance", body);
          res.className = "bal-res " + (r.error ? "bad" : r.ok ? "ok" : "");
          res.textContent = r.error ? balanceError(r.error) || r.error : r.ok ? t("Balance") + " " + r.amount : t("No Balance URL to ask");
          res.title = r.error || "";
        } catch (e) { res.className = "bal-res bad"; res.textContent = e.message; }
        check.classList.remove("busy");
      };
      balPair.append(res);
      balPair.classList.add("wrap");
    }
    inner.append(...field(t("Balance field"), balPair, t("Where the amount is in the reply, e.g. data.balance; it can be a sum with + - * / and brackets, e.g. data.total / 500000 or (1 - credits.used / 70) %; \"$\" in front adds the sign, \"%\" after it shows a percent; several, each with a label, go apart by \";\", e.g. 5h: a.used / a.cap %; $credits.left")));
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
  if (p) {
    const dup = el("button", "text", t("Duplicate"));
    dup.title = t("A new provider with {name}'s URLs, key, headers, models and balance settings, to change before adding", { name: p.name });
    dup.onclick = () => duplicateProvider(p);
    bar.append(dup);
  }
  bar.append(el("span", "grow"));
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelEdit;
  const saveBtn = el("button", "text primary", t(isNew ? "Add" : "Save"));
  const save = () => {
    // new: an Add never replaces a provider that has the id already
    const body = { id: p ? slug(draft.id) || p.id : draft.id, from: p?.id, name: draft.name, preset: draft.preset, key: draft.key || "", chat: draft.chat, responses: draft.responses, anthropic: draft.anthropic, catalog: draft.catalog, models: p ? chosenIds() : draft.extra, headers: headersOf(draft.headers), new: isNew };
    if (isNew && draft.copyOf) body.copyOf = draft.copyOf;
    if (decides) body.decide = (draft.decide || "").trim();
    if (custom) { body.icon = draft.icon || "generic"; body.balanceURL = (draft.balanceURL || "").trim(); body.balancePath = (draft.balancePath || "").trim(); body.modelsURL = (draft.modelsURL || "").trim(); }
    if (p) { body.fallback = draft.fallback; body.unlisted = draft.unlisted; }
    const cx = parseContexts(draft.contexts || "");
    if (cx.error) return editorError(t("Context window: {v} is not a length like 128k or 1m", { v: cx.error }), "warn");
    body.contexts = cx.map;
    body.proxy = proxyOfDraft();
    if (body.proxy === null) { ed.querySelector(".proxy-url")?.focus(); return editorError(t("Proxy: type its address, like http://127.0.0.1:7890"), "warn"); }
    if (draft.balanceToken) body.balanceToken = draft.balanceToken;
    else if (draft.clearBalanceToken) body.clearBalanceToken = true;
    if (team) {
      // both or neither: {} clears them
      const org = (draft.zhipuTeam.org || "").trim(), project = (draft.zhipuTeam.project || "").trim();
      if (!org !== !project) { ed.querySelector(org ? ".team-project" : ".team-org")?.focus({ preventScroll: true }); return editorError(t("Team plan: give both the organization ID and the project ID"), "warn"); }
      body.zhipuTeam = org ? { org, project } : {};
    }
    if (isNew && custom && !body.name) { name.focus(); return editorError(t("Give it a name"), "warn"); }
    if (isNew && custom && !body.chat && !body.anthropic && !body.responses) { url.focus(); return editorError(t("A base URL is needed"), "warn"); }
    if (endpoint && !body.chat && !body.responses) { endpoint.focus(); return editorError(t("Your resource's endpoint is needed"), "warn"); }
    editorError("");
    saveBtn.classList.add("busy");
    providerAction("save", body, t(isNew ? "{name} added" : "{name} saved", { name: draft.name || draft.id }));
  };
  saveBtn.onclick = save;
  bar.append(cancel, saveBtn);
  ed.append(bar);
  setTimeout(() => (isNew ? (custom || another ? name : endpoint || key) : null)?.focus(), 0);
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

// contextPicks puts the usual windows under the context field, one click
// each, and, when some models take more than they are said to (Codex's
// GPT-6: 272K said, 872K taken), their own most: sizes nobody remembers
// (#120). The pick matching what is typed is lit.
function contextPicks(p, cx) {
  const size = (n) => contextsText({ "*": n });
  const picks = [128e3, 200e3, 256e3, 1e6].map((n) => ({ label: size(n).toUpperCase(), value: size(n) }));
  const big = (p?.models || []).filter((m) => m.max > (m.context || 0));
  if (big.length) {
    const tops = [...new Set(big.map((m) => m.max))];
    const value = () => {
      const on = big.filter((m) => draft.chosen?.includes(m.id));
      return contextsText(Object.fromEntries((on.length ? on : big).map((m) => [m.id, m.max])));
    };
    picks.push({
      label: tops.length === 1 ? t("Each model's most · {n}", { n: size(tops[0]).toUpperCase() }) : t("Each model's most"),
      title: big.map((m) => `${m.id}: ${size(m.max).toUpperCase()}`).join("\n"), value, most: true,
    });
  }
  const row = el("div", "cxpicks");
  const same = (a, b) => JSON.stringify(parseContexts(a).map || {}) === JSON.stringify(parseContexts(b).map || {});
  const light = () => {
    for (const [i, b] of [...row.children].entries()) {
      const v = picks[i].value;
      b.classList.toggle("on", !!cx.value.trim() && same(cx.value, typeof v === "function" ? v() : v));
    }
  };
  for (const pk of picks) {
    const b = el("button", "cxpick" + (pk.most ? " most" : ""), pk.label);
    b.type = "button";
    if (pk.title) b.title = pk.title;
    b.onclick = () => {
      cx.value = typeof pk.value === "function" ? pk.value() : pk.value;
      draft.contexts = cx.value;
      light();
    };
    row.append(b);
  }
  cx.oninput = () => { draft.contexts = cx.value; light(); };
  light();
  const wrap = el("div", "cxfield");
  wrap.append(cx, row);
  return wrap;
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
    for (const m of p.models) {
      const c = el("span", "mchip on", m);
      if (namedFree(m)) c.append(freeBadge(false));
      chips.append(c);
    }
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

// modelTests: what each provider's models answered Test models, by id; a
// model still being asked is null
const modelTests = {};

// chosenIds: the models picked, with the ids still in the add box, which
// a Save takes as if Enter had been pressed on them
function chosenIds() {
  const typed = (draft.typed || "").split(/[,\s]+/).filter(Boolean);
  return [...draft.chosen, ...typed.filter((id) => !draft.chosen.includes(id))].filter((id, i, all) => all.indexOf(id) === i);
}

function renderModels(p) {
  // a chip's dot: how its model answered, when it has been asked
  const tested = (c, id) => {
    const got = modelTests[p.id];
    if (!got || !(id in got)) return;
    const x = got[id];
    c.append(el("span", "tdot " + (!x ? "wait" : x.ok ? "ok" : "bad")));
    c.title = !x ? t("Testing…") : x.ok ? t("Answered in {ms} ms", { ms: x.ms }) : (x.status ? x.status + " · " : "") + x.error;
  };
  // a chip's right-click (or the menu key) tests that model alone: Test models
  // asks every one, and a list of many takes a while (yonghe, Discord)
  const testable = !p.decide && !p.account;
  const menu = (c, id) => {
    if (!testable) return;
    c.title = (c.title ? c.title + "\n" : "") + t("Right-click to test just this model");
    c.oncontextmenu = (e) => {
      e.preventDefault();
      const again = agentMenu?.anchor === c;
      closeAgentMenu();
      if (!again) openRowMenu(c, [{ name: "Test this model", icon: "M5.5 3.75v8.5L12.25 8z", run: () => testOne(id) }]);
    };
  };
  const box = el("div", "models");
  const chips = el("div", "mchips");
  const names = el("div", "mnames");
  const q = p.models.length > 24 ? input("", t("filter {n} models…", { n: p.models.length })) : null;
  const draw = () => {
    if (agentMenu && chips.contains(agentMenu.anchor)) closeAgentMenu();
    chips.replaceChildren();
    const f = (q?.value || "").trim().toLowerCase();
    let shown = 0;
    // a filter is a search: what it doesn't match is left out, picked or not
    for (const m of p.models) {
      const on = draft.chosen.includes(m.id);
      if (f && !m.id.toLowerCase().includes(f) && !(m.name || "").toLowerCase().includes(f) && !(m.default || "").toLowerCase().includes(f)) continue;
      const c = el("button", "mchip" + (on ? " on" : ""));
      c.append(el("span", "", m.name && m.name !== m.id ? m.name : m.id));
      // one the plan serves at no cost to it (WorkBuddy's x0.00 credits)
      // or one its vendor names free (#185)
      const free = m.free || namedFree(m.id, m.name);
      if (free) c.append(el("span", "badge free", t("free")));
      const ctx = contextTag(m.context, m.name);
      if (ctx) c.append(ctx);
      if (m.default) c.title = `${m.id} · ${m.default}`;
      else if (m.name && m.name !== m.id) c.title = m.id;
      if (free) c.title = (c.title || m.id) + " · " + t(m.free ? "free: it doesn't use the plan's credits" : "free: so its name says");
      tested(c, m.id);
      menu(c, m.id);
      c.onclick = () => { draft.chosen = on ? draft.chosen.filter((x) => x !== m.id) : [...draft.chosen, m.id]; draw(); };
      chips.append(c);
      if (++shown >= 80 && !f) { chips.append(el("span", "hint", t("… {n} more, filter to find them", { n: p.models.length - shown }))); break; }
    }
    for (const id of draft.chosen) {
      if (p.models.some((m) => m.id === id) || (f && !id.toLowerCase().includes(f))) continue;
      const c = el("button", "mchip on own");
      c.append(el("span", "", id));
      c.title = t("Added by hand");
      tested(c, id);
      menu(c, id);
      c.onclick = () => { draft.chosen = draft.chosen.filter((x) => x !== id); draw(); };
      chips.append(c);
    }
    if (!p.models.length && !draft.chosen.length) chips.append(el("span", "hint", t("The vendor's list is empty. Refresh, or type a model id.")));
    // the list is of models agents chat with: image, embedding and speech
    // models are left out of it, and an image model is set in Settings
    else if (f && !chips.children.length) chips.append(el("span", "hint", t("No model here matches “{q}”. Image, embedding and speech models aren't listed, as agents can't chat with them: pick an image model in Settings → Images.", { q: q.value.trim() })));
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
      const [img, imgCb] = tick(t("Accepts images"), !!m.images);
      img.title = t("Whether agents are told {id} can see images", { id: m.id });
      imgCb.onchange = () => accountAction("provider/images", { id: p.id, model: m.id, images: imgCb.checked },
        imgCb.checked ? t("{id} accepts images", { id: m.id }) : t("{id} does not accept images", { id: m.id }));
      row.append(img);
      const levels = m.efforts || [];
      // a model whose levels aren't known (m.given) can be given any
      // of them, and none again
      if (levels.length > 1) {
        const lv = el("div", "mlevels");
        lv.title = t(m.given ? "Its reasoning levels aren't known: tick the ones it takes" : "Reasoning levels agents are offered");
        const kept = m.kept?.length || m.given ? m.kept || [] : levels;
        for (const l of levels) {
          const [tk, cb] = tick(t(l), kept.includes(l));
          cb.onchange = () => {
            const next = levels.filter((x) => x === l ? cb.checked : kept.includes(x));
            if (!next.length && !m.given) { cb.checked = true; status(t("Keep at least one level"), "err"); return; }
            accountAction("provider/efforts", { id: p.id, model: m.id, efforts: next.length === levels.length && !m.given ? [] : next },
              next.length ? t("{id}: {levels}", { id: m.id, levels: next.map((x) => t(x)).join(", ") }) : t("{id} is as its provider has it again", { id: m.id }));
          };
          lv.append(tk);
        }
        row.append(lv);
      }
      if (m.default || m.kept?.length || m.imageSet) {
        const reset = el("button", "text action", t("Restore default"));
        reset.title = t("Its own name, every reasoning level it has, and whether it sees images");
        reset.onclick = async () => {
          reset.classList.add("busy");
          try {
            if (m.default) await api("provider/name", { id: p.id, model: m.id, modelName: "" });
            if (m.imageSet) await api("provider/images", { id: p.id, model: m.id, images: null });
          } catch (e) { status(e.message, "err"); reset.classList.remove("busy"); return; }
          accountAction("provider/efforts", { id: p.id, model: m.id, efforts: [] }, t("{id} is as its provider has it again", { id: m.id }));
        };
        row.append(reset);
      }
      names.append(row);
    }
  };
  // every model at once (those the filter shows, when there is one), or none
  const bulk = el("div", "mbulk");
  const allOn = el("button", "text action", t("Select all"));
  allOn.title = t("Pick every model listed (those the filter shows)");
  allOn.onclick = () => {
    const f = (q?.value || "").trim().toLowerCase();
    const ids = p.models.filter((m) => !f || m.id.toLowerCase().includes(f) || (m.name || "").toLowerCase().includes(f) || (m.default || "").toLowerCase().includes(f)).map((m) => m.id);
    draft.chosen = [...draft.chosen, ...ids.filter((id) => !draft.chosen.includes(id))];
    draw();
  };
  const allOff = el("button", "text action", t("Select none"));
  allOff.title = t("Unpick every model");
  allOff.onclick = () => { draft.chosen = []; draw(); };
  bulk.append(allOn, allOff);
  if (q || p.models.length > 1) {
    if (q) { q.oninput = draw; bulk.prepend(q); }
    box.append(bulk);
  }
  box.append(chips, names);
  const foot = el("div", "mfoot");
  const add = input(draft.typed || "", t("add a model id…"));
  add.oninput = () => { draft.typed = add.value; };
  add.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && add.value.trim()) { draft.chosen = chosenIds(); draft.typed = add.value = ""; draw(); }
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
  // each model the agents see gets a tiny request of its own: a vendor
  // that answers can still have a model that doesn't
  const testAll = el("button", "text action", t("Test models"));
  testAll.title = t("Send a tiny request to each model agents see, to find the ones that don't answer") + "\n" + t("Right-click a model to test just it");
  testAll.onclick = async () => {
    const ids = draft.chosen.length ? draft.chosen : p.models.filter((m) => m.on).map((m) => m.id);
    if (!ids.length) { status(t("Pick a model first."), "err"); return; }
    testAll.classList.add("busy");
    const got = modelTests[p.id] = {};
    for (const id of ids) got[id] = null;
    draw();
    try {
      const r = await api("provider/test", { id: p.id, test: ids });
      r.results.forEach((x, i) => { got[ids[i]] = x; });
      const bad = r.results.filter((x) => !x.ok).length;
      status(bad ? t("{n} of {all} models didn't answer", { n: bad, all: ids.length }) : t("All {n} models answered", { n: ids.length }), bad ? "err" : "ok");
    } catch (e) { delete modelTests[p.id]; status(e.message, "err"); }
    testAll.classList.remove("busy");
    draw();
  };
  // one model, its dot and title as Test models leaves them, the others'
  // results kept
  const testOne = async (id) => {
    const got = modelTests[p.id] = modelTests[p.id] || {};
    got[id] = null;
    draw();
    try {
      const r = await api("provider/test", { id: p.id, test: [id] });
      const x = got[id] = r.results[0];
      status(x.ok ? t("{model} answered in {ms} ms", { model: id, ms: x.ms }) : t("{model} didn't answer: {error}", { model: id, error: (x.status ? x.status + " · " : "") + x.error }), x.ok ? "ok" : "err");
    } catch (e) { delete got[id]; status(e.message, "err"); }
    draw();
  };
  const rename = el("button", "text action" + (naming === p.id ? " on" : ""), t("Names & levels"));
  rename.title = t("Rename the models agents see, or offer fewer of their reasoning levels");
  rename.onclick = () => { naming = naming === p.id ? null : p.id; rename.classList.toggle("on", naming === p.id); drawNames(); };
  foot.append(add, refresh);
  if (!p.decide && !p.account) foot.append(testAll);
  foot.append(rename);
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

// renderDrawers: the provider's image models. Agents don't chat with them,
// so they aren't among its Models; they are listed to say they are there,
// and one is picked in Settings → Images.
function renderDrawers(p) {
  const chips = el("div", "mchips");
  for (const id of p.drawIds) chips.append(el("span", "mchip ro", id));
  return field(t("Image models"), chips, t("Agents don't chat with these, so they aren't among its models: the one that draws is picked in Settings → Images."));
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
  // what Codex or Claude Code sends past magpie goes to the account it is
  // signed in to, which magpie moves on once Smart would count it spent
  // (provider.KeepOnAnAccountWithRoom, #209)
  const a = p.account;
  const own = a && (a.agent === "codex" || a.agent === "claude")
    ? " " + t("Routing picks the account for each request through magpie; {agent} on its own uses the one it is signed in to, which magpie moves to the next ticked account with room once it is 98% used.", { agent: a.agentName })
    : "";
  return field(t("Routing"), pick, t(cur[2]) + own);
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
  // both can also come from CLIProxyAPI's auth files or the agent's own (importing below)
  { agent: "claude", name: "Claude", icon: "claude-color", plans: "Pro · Max · Team", importable: true },
  { agent: "codex", name: "ChatGPT", icon: "openai", plans: "Plus · Pro · Business", importable: true },
  // cursor-agent keeps one account; signing in again replaces it
  { agent: "cursor", name: "Cursor", icon: "cursor", plans: "Pro · Ultra · Teams", single: true },
  // so does Grok Build
  { agent: "grok", name: "Grok (SuperGrok)", icon: "xai", plans: "SuperGrok · X Premium+", own: true },
  // signed in with GitHub's device code; the editors' own sign-in stays theirs
  { agent: "copilot", name: "Copilot", icon: "githubcopilot", plans: "Pro · Pro+ · Business", own: true },
  // Z.ai's GLM Coding Plan, signed in as ZCode does; ZCode's own account is read too
  // sites: where the account is, Z.ai's or BigModel's (智谱), asked before
  // the sign-in opens; a team's plan (团队套餐) is signed in on its site too
  { agent: "zcode", name: "ZCode (GLM Coding Plan)", icon: "zcode", plans: "Lite · Pro · Max · Team", own: true,
    sites: [["zai", "Z.ai", "z.ai"], ["bigmodel", "BigModel (智谱)", "bigmodel.cn"]] },
  // Tencent's CodeBuddy plan, signed in as WorkBuddy does; WorkBuddy's own account is read too
  { agent: "workbuddy", name: "WorkBuddy (CodeBuddy)", icon: "workbuddy-color", plans: "Free · Pro", own: true },
  // the same plan sold abroad, WorkBuddy AI (workbuddy.ai / codebuddy.ai), its accounts its own
  { agent: "workbuddy-ai", get name() { return t("WorkBuddy AI (international)"); }, icon: "workbuddy-color", plans: "Free · Pro", own: true },
  // a commandcode.ai plan, signed in as its CLI does; the CLI's own key is read too
  // a Go plan is asked at the CLI's private /alpha/generate, which Command Code
  // said on X may get an account banned when used from other tools
  { agent: "commandcode-plan", name: "Command Code", icon: "commandcode", plans: "Go · Pro · GOAT · Max · Ultra", own: true, risk: true,
    riskNote: "A Go plan account is used through Command Code's private interface, which Command Code may treat as a breach of its terms and ban the account for. Pro, Max and the other plans use its Provider API. Use a Go account you can afford to lose." },
  { agent: "qoder", name: "Qoder", icon: "qoder", plans: "Pro", own: true, risk: true,
    riskNote: "Qoder has no public API for this; magpie signs requests as its desktop client would, which Qoder may treat as third-party use and act on. Use an account you can afford to lose." },
  // the devin CLI's own account is read; more are signed in beside it, each in a data folder of magpie's
  { agent: "devin", name: "Devin", icon: "devin", plans: "Pro · Enterprise", own: true },
  // Zed's hosted models (Zed Pro, its trial), signed in at zed.dev as the editor is
  { agent: "zed", name: "Zed", icon: "zed", plans: "Pro · Student · Business", own: true, risk: true,
    riskNote: "Zed serves these models to its own editor; magpie signs requests as the editor would, which Zed may treat as third-party use and act on. Use an account you can afford to lose." },
  // Factory's plans (Droid's account), signed in with WorkOS's device code as droid does; droid's own login stays its own
  { agent: "factory", name: "Factory", icon: "factory", plans: "Pro · Plus · Max", own: true, risk: true,
    riskNote: "Factory serves these models to its own Droid CLI; magpie signs requests as Droid would, which Factory may treat as third-party use and act on. Use an account you can afford to lose." },
  // Xiaomi MiMo's models (its free offer, MiMo plans), signed in at account.xiaomi.com as MiMo's app is
  { agent: "mimo-app", name: "Xiaomi MiMo", icon: "mimocode", plans: "Free · Starter · Plus · Pro · Ultra", own: true, risk: true,
    riskNote: "Xiaomi serves these models to its own MiMo app; magpie signs requests as the app would, which Xiaomi may treat as third-party use and act on. Use an account you can afford to lose." },
  // Kiro's own sign-in page (Google, GitHub, Builder ID, Identity Center); kiro-cli's or the IDE's is read too
  { agent: "kiro", name: "Kiro", icon: "kiro-color", plans: "Free · Pro · Pro+ · Power", own: true },
  // Google's sign-ins; Gemini CLI's own account is read too
  { agent: "gemini", name: "Gemini CLI", icon: "geminicli-color", plans: "Code Assist Standard · Enterprise", own: true },
  // accounts can also come from another tool's export (Antigravity Cockpit, Antigravity Manager, CLIProxyAPI)
  { agent: "antigravity", name: "Antigravity", icon: "antigravity-color", plans: "Google AI Pro · Ultra · free", risk: true, importable: true },
];
const subOf = (agent) => SUBS.find((x) => x.agent === agent) || pluginSubs().find((x) => x.agent === agent);

// pluginSubs: the providers OpenCode plugins sign in to (Settings →
// Plugins), as subscriptions like the built-in ones. The plugin, not
// magpie, signs in and carries the requests.
function pluginSubs() {
  return (providers.plugins || []).map((x) => ({
    agent: x.id, pid: x.pid, name: x.name, icon: x.icon, plugin: x, own: true,
    get plans() { return t("from the plugin {spec}", { spec: x.spec }); },
  }));
}

// importSay: what the import of an app's accounts says — where its files
// come from, and who they are checked with. A ChatGPT or Claude sign-in is
// refreshed as it comes in, which spends the file's refresh token.
function importSay(agent) {
  if (agent === "codex" || agent === "claude") {
    const vendor = agent === "codex" ? "ChatGPT" : "Claude";
    const own = agent === "codex" ? "Codex's auth.json" : "Claude Code's .credentials.json";
    return {
      from: t("Bring in accounts from CLIProxyAPI's auth files or {own}", { own }),
      intro: t("Choose or paste CLIProxyAPI's auth files (JSON) or {own}. Each account's sign-in is refreshed with {vendor} before it is added.", { own, vendor }),
      spent: t("Refreshing it spends the file's sign-in: the tool it came from will need to sign in again to use that account."),
      checking: t("Checking the accounts with {vendor}…", { vendor }),
      checks: t("Each account's sign-in is refreshed and its account looked up, as signing in does."),
    };
  }
  return {
    from: t("Bring in accounts exported from Antigravity Cockpit, Antigravity Manager or CLIProxyAPI"),
    intro: t("Choose or paste an export from Antigravity Cockpit, Antigravity Manager or CLIProxyAPI — JSON, or refresh tokens one a line. Each account is checked with Google before it is added."),
    checking: t("Checking the accounts with Google…"),
    checks: t("Each account's sign-in is refreshed and its project looked up, as signing in does."),
  };
}
let signing = null; // the sign-in under way: { id, agent, url, state, installing, error }
const signingOpen = () => signing?.state === "waiting" || signing?.state === "installing";
let justAdded = ""; // the account that just came in, to greet it

async function startSignIn(agent, risky, site) {
  if (signingOpen()) api("signin/" + signing.id + "/cancel", {}).catch(() => {});
  if (subOf(agent)?.plugin) return startPluginSignIn(subOf(agent));
  // an account Google may suspend is added only once that is said
  if (subOf(agent)?.risk && !risky) {
    signing = { agent, state: "risk" };
    renderProviders();
    return;
  }
  // one on more than one site says which first
  if (subOf(agent)?.sites && !site) {
    signing = { agent, state: "site" };
    renderProviders();
    return;
  }
  signing = { agent, site, state: "starting" };
  renderProviders();
  try {
    signing = await api("signin", site ? { agent, site } : { agent });
    signing.site = site;
    if (web && signing.url) api("open", { url: signing.url });
    renderProviders();
    followSignIn(signing.id);
  } catch (e) {
    signing = { agent, site, state: "failed", error: e.message };
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
      if (signing.state !== st.state || signing.url !== st.url) { signing = { ...st, site: signing.site }; renderProviders(); }
      continue;
    }
    if (st.state === "done") return signedIn(st);
    signing = st.state === "canceled" ? null : { ...st, site: signing.site };
    renderProviders();
  }
}

// signedIn: a sign-in done — the account opened in the editor.
async function signedIn(st) {
  signing = null;
  justAdded = st.user;
  delete loginUsage[st.agent]; // what was fetched before has nothing on the new account
  providers = await api("providers");
  const p = providers.providers.find((x) => x.account?.agent === st.agent);
  if (p) { editing = p.id; draft = null; adding = false; presetQuery = ""; }
  renderProviders();
  const who = st.user || subOf(st.agent)?.name || st.agent;
  // signed in but listed nowhere (#155): say so rather than "added"
  if (!p) status(t("{user} signed in, but magpie can't list it — please report this", { user: who }), "err");
  else status(st.using ? t("Signed in as {user}", { user: who }) : t("{user} added — switch to it any time", { user: who }), "ok");
  state = await api("state");
  renderAgents();
  setTimeout(() => { justAdded = ""; }, 2000);
}

// startPluginSignIn: a plugin's provider signed in to as OpenCode's
// `auth login` does — the way to sign in, the questions that way asks,
// then the browser (and the code its page shows, pasted back) or a key.
async function startPluginSignIn(sub, method, inputs = {}) {
  const ms = sub.plugin.methods || [];
  if (!ms.length) {
    signing = { agent: sub.agent, state: "failed", error: t("The plugin has no way to sign in to {name}", { name: sub.name }) };
    return renderProviders();
  }
  if (method == null) {
    if (ms.length > 1) {
      signing = { agent: sub.agent, state: "method" };
      return renderProviders();
    }
    method = 0;
  }
  signing = { agent: sub.agent, method, inputs, state: "starting" };
  renderProviders();
  try {
    const r = await api("plugin-signin/prompt", { provider: sub.pid, method, inputs });
    if (signing?.agent !== sub.agent) return;
    if (r.prompt) {
      signing = { agent: sub.agent, method, inputs, prompt: r.prompt, state: "prompt" };
      return renderProviders();
    }
    if (ms[method].type === "api") {
      signing = { agent: sub.agent, method, inputs, state: "key" };
      return renderProviders();
    }
    signing = await api("plugin-signin", { provider: sub.pid, method, inputs });
    signing.method = method;
    if (web && signing.url) api("open", { url: signing.url });
    renderProviders();
    followSignIn(signing.id);
  } catch (e) {
    if (signing?.agent !== sub.agent) return;
    signing = { agent: sub.agent, method, state: "failed", error: e.message };
    renderProviders();
  }
}

// pluginAnswer: one of a sign-in method's questions answered, checked by
// the plugin before the next is asked.
async function pluginAnswer(sub, value) {
  const flow = signing;
  try {
    const r = await api("plugin-signin/prompt", { provider: sub.pid, method: flow.method, inputs: flow.inputs, key: flow.prompt.key, value });
    if (signing !== flow) return;
    if (r.error) {
      signing = { ...flow, error: r.error, value };
      return renderProviders();
    }
    startPluginSignIn(sub, flow.method, r.inputs);
  } catch (e) {
    if (signing === flow) { signing = { ...flow, error: e.message, value }; renderProviders(); }
  }
}

// pluginKey: an "api" method's key, which the plugin keeps.
async function pluginKey(sub, key) {
  const flow = signing;
  signing = { ...flow, busy: true };
  renderProviders();
  try {
    const st = await api("plugin-signin", { provider: sub.pid, method: flow.method, inputs: flow.inputs, key });
    if (signing?.agent === sub.agent) signedIn(st);
  } catch (e) {
    if (signing?.agent === sub.agent) { signing = { ...flow, error: e.message }; renderProviders(); }
  }
}

// renderPluginAsk: a plugin sign-in's step before the browser — the way
// to sign in, a question, or the key.
function renderPluginAsk(sub) {
  const box = el("div", "signing plugin-ask");
  const tt = el("span", "tt");
  box.append(tt);
  const close = el("button", "text", t("Cancel"));
  close.onclick = cancelSignIn;
  if (signing.state === "method") {
    tt.append(el("span", "n", t("How do you sign in to {name}?", { name: sub.name })),
      el("span", "s", t("The plugin {spec} signs in and sends {name}'s requests; magpie only passes them on.", { spec: sub.plugin.spec, name: sub.name })));
    const ch = el("div", "choices");
    box.append(close, ch);
    sub.plugin.methods.forEach((m, i) => {
      const b = el("button", "text primary", m.label || t(m.type === "api" ? "API key" : "Browser"));
      b.dataset.method = String(i);
      b.onclick = () => startPluginSignIn(sub, i);
      ch.append(b);
    });
    return box;
  }
  const q = signing.prompt;
  const flow = signing;
  const form = el("form", "callback-form");
  const why = el("span", "s why", flow.error || "");
  if (flow.state === "prompt" && q.type === "select") {
    tt.append(el("span", "n", q.message), why);
    const ch = el("div", "choices");
    box.append(close, ch);
    for (const o of q.options || []) {
      const b = el("button", "text primary", o.label);
      if (o.hint) b.title = o.hint;
      b.onclick = () => pluginAnswer(sub, o.value);
      ch.append(b);
    }
    return box;
  }
  const key = flow.state === "key";
  const label = key ? t("{name} API key", { name: sub.name }) : q.message;
  tt.append(el("span", "n", label));
  const inp = input(flow.value || "", key ? "" : (q.placeholder || ""), key ? "password" : "text");
  inp.setAttribute("aria-label", label);
  inp.autocomplete = "off";
  inp.disabled = !!flow.busy;
  const go = el("button", "text primary", t(key ? "Sign in" : "Next"));
  go.type = "submit";
  go.disabled = !!flow.busy;
  inp.onkeydown = (e) => e.stopPropagation();
  const submit = (e) => {
    e.preventDefault();
    if (signing !== flow || flow.busy) return;
    if (key) pluginKey(sub, inp.value);
    else pluginAnswer(sub, inp.value);
  };
  form.onsubmit = submit;
  go.onclick = submit;
  form.append(inp, go);
  tt.append(form, why);
  box.append(close);
  setTimeout(() => { if (inp.isConnected && !inp.disabled) inp.focus({ preventScroll: true }); });
  return box;
}

// renderStepPlan: a StepFun provider's platform sign-in, which the user
// makes in their own browser and brings here with magpie's bookmarklet
function renderStepPlan(sp) {
  const box = el("div", "stepplan");
  if (sp.signedIn) {
    const row = el("div", "pair");
    const side = el("div", "side");
    const out = el("button", "text", t("Sign out"));
    out.onclick = async () => {
      try { providers = await api("stepfun/" + sp.site + "/signout", {}); renderProviders(); } catch (e) { status(e.message, "err"); }
    };
    side.append(out);
    row.append(el("span", "state", t("Signed in · the Usage page shows the Step Plan")), side);
    box.append(row);
    return box;
  }
  const steps = el("ol", "steps");
  const s1 = el("li");
  const open = el("button", "link", t("Sign in to StepFun in your browser ↗"));
  open.onclick = () => api("open", { url: sp.url });
  s1.append(open);
  const s2 = el("li");
  // dragged to the bookmarks bar it is a bookmark; a click here does nothing
  const bm = el("a", "bookmarklet", "magpie · StepFun");
  bm.href = sp.bookmarklet;
  bm.onclick = (e) => e.preventDefault();
  const cp = el("button", "link", t("copy it"));
  cp.onclick = () => copy(sp.bookmarklet, t("Bookmarklet"), cp);
  s2.append(document.createTextNode(t("Drag ")), bm, document.createTextNode(t(" to the bookmarks bar (or ")), cp, document.createTextNode(t(" as a bookmark's URL), then click it on the signed-in page")));
  const s3 = el("li");
  const pair = el("div", "pair");
  const paste = input("", t("paste what it copied"), "password");
  const go = el("button", "text", t("Save"));
  const save = async () => {
    if (!paste.value.trim()) return;
    go.disabled = paste.disabled = true;
    try {
      providers = await api("stepfun/" + sp.site + "/session", { text: paste.value });
      renderProviders();
      status(t("Signed in to StepFun"), "ok");
    } catch (e) {
      go.disabled = paste.disabled = false;
      status(e.message, "err");
    }
  };
  go.onclick = save;
  paste.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save(); };
  paste.onpaste = () => setTimeout(save);
  const side = el("div", "side");
  side.append(go);
  pair.append(paste, side);
  s3.append(pair);
  steps.append(s1, s2, s3);
  box.append(steps);
  return box;
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
      el("span", "s", t(sub.riskNote || "Google may suspend an Antigravity account it sees used outside Antigravity. Use one you can afford to lose.")));
    box.append(tt);
    const go = el("button", "text primary", t("Sign in anyway"));
    go.onclick = () => startSignIn(sub.agent, true);
    const close = el("button", "text", t("Cancel"));
    close.onclick = cancelSignIn;
    if (sub.importable) {
      const imp = el("button", "text", t("Import instead…"));
      imp.title = importSay(sub.agent).from;
      imp.onclick = () => startImport(sub.agent);
      box.append(close, imp, go);
    } else box.append(close, go);
    return box;
  }
  if (signing.state === "site") {
    // ZCode: a Z.ai account or a BigModel (智谱) one, a team's seat included
    tt.append(el("span", "n", t("Where is your {name} account?", { name: sub.name })),
      el("span", "s", t("Sign in where your GLM Coding Plan was bought, a team's plan too: z.ai, or bigmodel.cn for 智谱.")));
    box.append(tt);
    const close = el("button", "text", t("Cancel"));
    close.onclick = cancelSignIn;
    box.append(close);
    for (const [id, label, host] of sub.sites) {
      const b = el("button", "text primary", t(label));
      b.dataset.site = id;
      b.title = host;
      b.onclick = () => startSignIn(sub.agent, true, id);
      box.append(b);
    }
    return box;
  }
  if (signing.state === "import" || signing.state === "importing" || signing.state === "imported") return renderLoginImport(sub);
  if (signing.state === "method" || signing.state === "prompt" || signing.state === "key") return renderPluginAsk(sub);
  if (signing.state === "failed") {
    box.append(el("span", "mark", "!"));
    tt.append(el("span", "n", t("Sign-in didn't finish")), el("span", "s", signing.error || ""));
    box.append(tt);
    const again = el("button", "text primary", t("Try again"));
    again.onclick = () => sub.plugin ? startPluginSignIn(sub, signing.method) : startSignIn(sub.agent, true, signing.site);
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
    el("span", "s", signing.state === "starting" ? t("Starting the sign-in…")
      : sub.plugin ? (signing.instructions || (signing.pasteCode ? t("magpie opened the sign-in page. Paste the code it shows below.") : t("magpie opened the sign-in page. The account shows up here as soon as you're done.")))
      : signing.code && sub.agent === "factory" ? t("magpie opened Factory's sign-in page. Check it shows this code and confirm it; the account shows up here as soon as you're done.")
      : signing.code ? t("magpie opened GitHub's device page. Enter this code there; the account shows up here as soon as you're done.") : t("magpie opened the sign-in page. The account shows up here as soon as you're done.")));
  if (signing.code) {
    const code = el("span", "devcode");
    code.append(el("code", "", signing.code), copyBtn(signing.code, t("Code")));
    tt.append(code);
  }
  box.append(tt);
  if (signing.url) {
    // the link itself, to select or copy into another browser or profile
    // than the one magpie opened it in
    const link = el("span", "signlink");
    const u = el("code", "", signing.url);
    u.title = t("Open it in another browser or profile: copy it there");
    const cp = copyBtn(signing.url, t("Sign-in link"));
    cp.title = t("Copy link");
    link.append(u, cp);
    tt.append(link);
    const acts = el("span", "acts");
    const open = el("button", "link", t("Open again"));
    open.onclick = () => api("open", { url: signing.url }).catch(() => {});
    acts.append(open);
    tt.append(acts);
  }
  if (signing.pasteCallback || signing.pasteCode) {
    const flow = signing;
    const what = flow.pasteCode ? t("Code") : t("Callback URL");
    if (!flow.pasteCode) tt.append(el("span", "s", t("If the browser cannot return to magpie, paste its final callback URL here.")));
    const form = el("form", "callback-form");
    const url = input(flow.callbackURL || "", what);
    url.setAttribute("aria-label", what);
    url.autocomplete = "off";
    url.disabled = !!flow.callbackSubmitted || !!flow.callbackSubmitting;
    const submit = el("button", "text primary", t("Finish sign-in"));
    submit.type = "submit";
    submit.disabled = url.disabled || !url.value.trim();
    const why = el("span", "s why", flow.callbackError || "");
    url.oninput = () => { flow.callbackURL = url.value; submit.disabled = !!flow.callbackSubmitted || !!flow.callbackSubmitting || !url.value.trim(); };
    const finish = async (e) => {
      e.preventDefault();
      if (flow.callbackSubmitted || flow.callbackSubmitting || signing !== flow) return;
      flow.callbackSubmitting = true;
      submit.disabled = true;
      url.disabled = true;
      why.textContent = "";
      try {
        await api("signin/" + flow.id + "/callback", { url: url.value });
        flow.callbackSubmitted = true;
        url.disabled = true;
      } catch (err) {
        if (signing !== flow) return;
        flow.callbackError = why.textContent = err.message;
        url.disabled = false;
        submit.disabled = !url.value.trim();
      } finally {
        flow.callbackSubmitting = false;
      }
    };
    form.onsubmit = finish;
    submit.onclick = finish;
    form.append(url, submit);
    tt.append(form, why);
  }
  if (sub.importable) {
    // an account another tool is signed in to comes in from its file
    const imp = el("button", "link", t("Import from a file instead…"));
    imp.title = importSay(sub.agent).from;
    imp.onclick = () => startImport(sub.agent);
    const acts = tt.querySelector(".acts") || tt.appendChild(el("span", "acts"));
    acts.append(imp);
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
// forgetOwnTitle: what Remove does to the agent's own sign-in, which magpie
// only reads — it is hidden, and shows again when the agent signs in anew.
function forgetOwnTitle(a) {
  return t("magpie stops showing and using {agent}'s own sign-in; its files are left as they are, and it shows again when {agent} signs in anew", { agent: a.agentName });
}

function renderAccounts(a) {
  const sub = subOf(a.agent);
  const list = el("div", "accts");
  let ls = a.logins?.length ? [...a.logins] : [{ user: a.user, plan: a.plan, active: true, on: true }];
  ls.sort((x, y) => (y.active ? 1 : 0) - (x.active ? 1 : 0));
  const several = ls.filter((l) => (l.active && !l.paused) || l.on).length > 1;
  // the account Claude Code or Codex is signed in to can be paused while
  // another is on: the gateway passes over it, the agent staying signed in
  // to it (#263)
  const pausable = (a.agent === "claude" || a.agent === "codex") && ls.some((l) => !l.active && l.on);
  const quota = loginUsageOf(a.agent);
  for (const l of ls) {
    const on = !l.paused && (l.active || l.on);
    const row = el("div", "acc" + (on ? " in-use" : " off") + (l.user === justAdded ? " new" : ""));
    const dot = el("button", "dot tick");
    if (on) dot.append(svg(CHECK, 10, 2.2));
    if (l.active && (pausable || l.paused)) {
      dot.title = l.paused ? t("Resume: the gateway uses this account first again") : t("Pause: the gateway uses the other accounts, {agent} stays signed in to this one", { agent: a.agentName });
      dot.onclick = () => accountAction("login/" + (l.paused ? "on" : "off"), { agent: a.agent, user: l.user });
    } else if (l.active) {
      dot.title = sub?.own ? t("The gateway uses this account first") : t("{agent} is signed in to this account", { agent: a.agentName });
      dot.classList.add("fixed");
    } else {
      dot.title = on ? t("Stop using this account") : t("Use this account too");
      dot.onclick = () => accountAction("login/" + (on ? "off" : "on"), { agent: a.agent, user: l.user });
    }
    row.append(dot, el("span", "n", l.user), el("span", "plan", accountPlan({ agent: a.agent, plan: l.plan })), el("span", "grow"));
    if (l.active) {
      row.append(el("span", "using", l.paused ? t("Paused") : several ? t("First") : t("Current")));
      if (a.agent === "qoder" || l.own) {
        const forget = el("button", "text quiet", t("Remove"));
        if (l.own) forget.title = forgetOwnTitle(a);
        forget.onclick = () => accountAction("login/forget", { agent: a.agent, user: l.user }, t("{user} removed", { user: l.user }));
        row.append(forget);
      }
    } else {
      const forget = el("button", "text quiet", t("Remove"));
      forget.title = l.own ? forgetOwnTitle(a) : t("magpie forgets this account's sign-in; the account itself is untouched");
      forget.onclick = () => accountAction("login/forget", { agent: a.agent, user: l.user }, t("{user} removed", { user: l.user }));
      const use = el("button", "text", on ? t("Make first") : t("Use"));
      use.title = sub?.own ? t("The gateway uses this account first") : t("Sign {agent} in to this account", { agent: a.agentName });
      use.onclick = () => { use.classList.add("busy"); accountAction("login/switch", { agent: a.agent, user: l.user }, sub?.own ? t("The gateway now uses {user} first", { user: l.user }) : t("{agent} is now signed in as {user}", { agent: a.agentName, user: l.user })); };
      row.append(forget, use);
    }
    row.append(accountQuota(l.lapsed ? { [l.user]: { error: l.lapsed } } : quota, l.user));
    row.classList.add("with-aq"); // not :has(.aq), which Safari 15.0 lacks (#220)
    list.append(row);
  }
  if (a.agent === "codex" && providers?.codexDaemon) list.append(renderCodexDaemon(providers.codexDaemon));
  if (signing?.agent === a.agent) list.append(renderSigning(sub));
  else {
    const add = el("button", "acc add");
    const ic = el("span", "dot");
    ic.append(svg(PLUS, 10, 1.8));
    add.append(ic, el("span", "n", t(sub.single ? "Sign in to another {name} account" : "Add another {name} account", { name: sub.name })));
    add.onclick = () => startSignIn(a.agent);
    list.append(add);
    if (sub.importable) {
      const imp = el("button", "acc add");
      const ic2 = el("span", "dot");
      ic2.append(svg(PLUS, 10, 1.8));
      imp.append(ic2, el("span", "n", t("Import accounts from a file…")));
      imp.title = importSay(a.agent).from;
      imp.onclick = () => startImport(a.agent);
      list.append(imp);
    }
  }
  return list;
}

// renderCodexDaemon: Codex's background app-server read the sign-in when it
// started, so after a switch the Codex sessions that attach to it are still
// on the account before (user) until it restarts. magpie doesn't restart it
// unasked: that ends the Codex sessions running on it.
function renderCodexDaemon(user) {
  const box = el("div", "signing daemon");
  box.append(el("span", "mark", "!"));
  const tt = el("span", "tt");
  tt.append(el("span", "n", t("Codex's background service is still signed in as {user}", { user })),
    el("span", "s", t("Restart it to use the new account. Running Codex sessions will be interrupted.")));
  box.append(tt);
  const later = el("button", "text", t("Later"));
  later.onclick = () => accountAction("codex/daemon/dismiss", {});
  const go = el("button", "text primary", t("Restart"));
  go.title = "codex app-server daemon restart";
  go.onclick = () => { go.classList.add("busy"); accountAction("codex/daemon/restart", {}, t("Codex's background service restarted")); };
  box.append(later, go);
  return box;
}

// Accounts brought in from another tool's export instead of signing in
// again: the files' text (or what is pasted) goes to magpie, which checks
// each account with the vendor before keeping it, and says what became of
// each. What is read stays here only until it is sent; the box masks it.
function startImport(agent) {
  if (signingOpen()) api("signin/" + signing.id + "/cancel", {}).catch(() => {});
  signing = { agent, state: "import", text: "", files: [] };
  renderProviders();
  document.querySelector(".signing.import textarea")?.focus();
}

async function runImport(agent) {
  const files = signing.files.map((f) => f.text);
  if (signing.text.trim()) files.push(signing.text);
  if (!files.length) return;
  signing = { agent, state: "importing" };
  renderProviders();
  try {
    const r = await api("signin/import", { agent, files });
    if (signing?.agent !== agent) return;
    providers = r.providers;
    const added = r.results.filter((x) => x.status === "added" || x.status === "updated");
    signing = { agent, state: "imported", results: r.results };
    delete loginUsage[agent];
    const p = providers.providers.find((x) => x.account?.agent === agent);
    if (p && added.length) {
      editing = p.id; draft = null; adding = false; presetQuery = "";
      justAdded = added[0].user;
      setTimeout(() => { justAdded = ""; }, 2000);
    }
    renderProviders();
    const failed = r.results.filter((x) => x.status === "failed").length;
    status(t("{n} added, {m} not added", { n: added.length, m: failed }), added.length || !failed ? "ok" : "err");
    state = await api("state");
    renderAgents();
  } catch (e) {
    if (signing?.agent !== agent) return;
    signing = { agent, state: "import", text: "", files: [], error: e.message };
    renderProviders();
  }
}

const importStatus = { added: "Added", updated: "Updated with this sign-in", exists: "Already in magpie", failed: "Not added" };

function renderLoginImport(sub) {
  const box = el("div", "signing import");
  const tt = el("span", "tt");
  if (signing.state === "importing") {
    box.append(el("span", "spinner"));
    const say = importSay(sub.agent);
    tt.append(el("span", "n", say.checking), el("span", "s", say.checks));
    box.append(tt);
    return box;
  }
  if (signing.state === "imported") {
    box.append(el("span", "mark", "✓"));
    tt.append(el("span", "n", t("Import finished")));
    const rs = el("span", "results");
    for (const r of signing.results || []) {
      const row = el("span", "res " + r.status);
      row.append(el("span", "u", r.user), el("span", "st", t(importStatus[r.status] || r.status)));
      if (r.error) { row.title = r.error; row.append(el("span", "why", r.error)); }
      rs.append(row);
    }
    tt.append(rs);
    box.append(tt);
    const done = el("button", "text", t("Done"));
    done.onclick = cancelSignIn;
    box.append(done);
    return box;
  }
  box.append(el("span", "mark", "↑"));
  const say = importSay(sub.agent);
  tt.append(el("span", "n", t("Import {name} accounts", { name: sub.name })), el("span", "s", say.intro));
  if (say.spent) tt.append(el("span", "s", say.spent));
  if (sub.risk) tt.append(el("span", "s", t(sub.riskNote || "Google may suspend an Antigravity account it sees used outside Antigravity. Use one you can afford to lose.")));
  const area = el("textarea");
  area.rows = 3;
  area.spellcheck = false;
  area.autocomplete = "off";
  area.placeholder = t("…or paste it here");
  area.value = signing.text || "";
  const file = el("input");
  file.type = "file";
  file.multiple = true;
  file.accept = ".json,.txt,application/json,text/plain";
  file.hidden = true;
  const names = el("span", "fname", signing.files.map((f) => f.name).join(", "));
  const go = el("button", "text primary", t("Import"));
  const ready = () => { go.disabled = !(signing.files.length || signing.text.trim()); };
  area.oninput = () => { signing.text = area.value; ready(); };
  file.onchange = async () => {
    const picked = [];
    for (const f of file.files) {
      if (f.size > 4 << 20) { status(t("{name} is too big to be an export", { name: f.name }), "err"); continue; }
      picked.push({ name: f.name, text: await f.text() });
    }
    signing.files = picked;
    names.textContent = picked.map((f) => f.name).join(", ");
    ready();
  };
  const pick = el("button", "link", t("Choose files…"));
  pick.onclick = () => file.click();
  const acts = el("span", "acts");
  acts.append(pick, names, file);
  tt.append(acts, area);
  if (signing.error) tt.append(el("span", "s why", signing.error));
  box.append(tt);
  go.onclick = () => runImport(sub.agent);
  ready();
  const close = el("button", "text", t("Cancel"));
  close.onclick = cancelSignIn;
  box.append(close, go);
  return box;
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
        if (providers?.providers.find((p) => p.id === editing)?.account?.agent === agent && !document.querySelector(".editor .rename-in, .editor input:focus, .signing.import textarea:focus")) renderProviders();
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
  return balanceError(err) || t("Usage unavailable");
}

// balanceError: a balance that couldn't be read, said plainly where magpie
// knows the fix (see balance.go); "" for any other.
function balanceError(err) {
  if (/takes the API key, not the access token/.test(err)) return t("The Balance URL …/api/usage/token takes the API key, not the access token — set it to …/api/user/self in the provider's settings");
  if (/New-Api-User/i.test(err)) return t("Add the header New-Api-User = your user ID (shown in the site's personal settings) to the provider's Headers");
  return "";
}

// balanceFix: under a custom provider's balance token, what its Balance URL
// wants in place of what is there, with the one click that sets it:
// new-api's /api/usage/token is asked with the key alone and never with the
// token (balance.go doesn't send it there), and a token without a URL is
// asked nowhere; new-api's /api/user/self takes the token, with the user's
// id in New-Api-User, which is said too while no such header is set. A URL
// of any other kind (a sub2api panel's) is left alone: a token's shape
// can't tell the two apart, new-api's own sign-ins being JWTs too.
function balanceFix(p) {
  const box = el("div", "bal-fix");
  const origin = (u) => { try { const x = new URL(u); return /^https?:$/.test(x.protocol) ? x.origin : ""; } catch { return ""; } };
  box.refresh = () => {
    box.replaceChildren();
    box.className = "bal-fix";
    const tok = !!draft.balanceToken || (!!p?.balanceToken?.set && !draft.clearBalanceToken);
    if (!tok) return;
    const u = (draft.balanceURL || "").trim();
    let path = "";
    try { path = new URL(u).pathname.replace(/\/+$/, ""); } catch {}
    let why = "";
    if (path === "/api/usage/token") why = t("…/api/usage/token takes the API key, not this token: a new-api relay tells the account's balance to the token at /api/user/self.");
    else if (!u) why = t("The token needs a Balance URL: a new-api relay tells the account's balance to it at /api/user/self.");
    if (why) {
      box.classList.add("warn");
      box.append(el("span", "", why));
      const site = origin(u) || origin(draft.chat || draft.anthropic || draft.responses || "");
      if (!site) return;
      const to = site + "/api/user/self";
      const use = el("button", "text action", t("Use {url}", { url: to }));
      use.onclick = () => {
        draft.balanceURL = to;
        // /api/usage/token's fields aren't in /api/user/self's reply; the
        // account's quota is, $1 to 500000 of it
        if (!(draft.balancePath || "").trim() || /total_(available|granted|used)|unlimited_quota/.test(draft.balancePath)) draft.balancePath = "$data.quota / 500000";
        const ed = box.closest(".editor");
        const set = (sel, v) => { const i = ed?.querySelector(sel); if (i) i.value = v; };
        set(".bal-url", draft.balanceURL);
        set(".bal-path", draft.balancePath);
        // what changed, in view below
        const more = ed?.querySelector("details.more");
        if (more) more.open = true;
        box.refresh();
      };
      box.append(use);
      return;
    }
    if (path === "/api/user/self" && !(draft.headers || []).some((h) => (h[0] || "").trim().toLowerCase() === "new-api-user" && (h[1] || "").trim())) {
      box.append(el("span", "", t("A new-api relay also wants the header New-Api-User = your user ID (shown in the site's personal settings): add it under Headers.")));
    }
  };
  box.refresh();
  return box;
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
  // an account on no plan says so, whatever else can be read of it
  const noPlan = q?.plan === "No plan" ? [t("No plan")] : [];
  if (q && !q.error && !q.windows?.length && q.balance) {
    // no rolling limits, only what is left to spend
    line.append(el("span", "aq-none", [...noPlan, t("Balance") + " " + q.balance].join(" · ")));
    return line;
  }
  if (!q || q.error || !q.windows?.length) {
    line.append(el("span", "aq-none", [...noPlan, q?.error ? quotaError(q.error) : t("No usage reported")].join(" · ")));
    if (q?.error) line.title = q.error;
    return line;
  }
  // the two rolling windows fit a line; the per-model ones go in its tooltip
  line.title = q.windows.slice(2).map((w) => t(w.name) + " " + quotaText(w)).join(" · ");
  if (q.asOf) line.title = [line.title, asOfText(q)].filter(Boolean).join("\n");
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
// every meter and the menu bar, kept in the settings (#122). The vendor's
// own count, where it gives one, stands before the percentage.
let quotaLeft = false;
function quotaFill(w) {
  const used = Math.round(Math.max(0, Math.min(100, w.used)));
  return quotaLeft ? 100 - used : used;
}
function quotaText(w) {
  const used = Math.max(0, Math.min(100, w.used));
  const n = quotaLeft ? 100 - used : used;
  const pct = t(quotaLeft ? "{n} left" : "{n} used", { n: (Number.isInteger(n) ? n : n.toFixed(1)) + "%" });
  return w.display ? w.display + " · " + pct : pct;
}
async function setQuotaLeft(on) {
  quotaLeft = on;
  renderQuotas();
  // the panel's "used"/"left" lights up a moment, so a click on a ring is
  // seen to have turned every ring, not to have done nothing (#184)
  for (const m of document.querySelectorAll(".pq-mode")) m.classList.add("flash");
  try {
    prefs = await writingPrefs(api("settings/quota-left", { on }));
    state.settings = prefs;
    if (view === "settings") renderSettings();
  } catch (e) {
    status(e.message, "err");
  }
}

// resetClock is when a window starts again, on the clock: "14:30" today,
// "tomorrow 09:00", "Thu 14:30" later this week (Monday to Sunday), else
// "Oct 12 08:05": a bare weekday in next week read as this week's (#181).
function resetClock(at, now = new Date()) {
  const lang = locale === "zh" ? "zh-CN" : undefined;
  const time = at.toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit", hour12: false });
  const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const days = Math.round((day(at) - day(now)) / 864e5);
  if (days <= 0) return time;
  if (days === 1) return t("tomorrow {time}", { time });
  if (days <= 6 - ((now.getDay() + 6) % 7)) return at.toLocaleDateString(lang, { weekday: "short" }) + " " + time;
  return at.toLocaleDateString(lang, { month: "short", day: "numeric" }) + " " + time;
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
    saidMoved(okMsg);
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
    if (k.active) row.append(el("span", "using", several ? t("First") : t("Current")));
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
  document.removeEventListener("scroll", protoMenu.scroll, true);
  removeEventListener("resize", closeProtoMenu);
  const done = protoMenu.done;
  protoMenu = null;
  done?.();
}
// openProtoMenu picks one of opts, or several when value is a list: each
// ticked or unticked in turn with the menu kept open, choose given the
// ticked ones, in the menu's order, once it closes (and only if they
// changed); "" is none of them and closes it, "\x00" a note to read.
function openProtoMenu(anchor, opts, value, choose, head = "Protocol this key speaks", cls = "") {
  closeProtoMenu();
  const multi = Array.isArray(value);
  let picked = multi ? [...value] : null;
  const isOn = (v) => !multi ? v === value : v === "" ? !picked.length : picked.includes(v);
  const box = el("div", "pop proto-menu" + (cls ? " " + cls : ""));
  box.setAttribute("role", "menu");
  box.append(el("div", "pm-head", t(head)));
  const tick = (b, o) => {
    b.classList.toggle("on", isOn(o.v));
    b.setAttribute("aria-checked", isOn(o.v));
    b.firstChild.replaceChildren(...(isOn(o.v) ? [svg(CHECK, 12, 1.9)] : []));
  };
  const items = opts.map((o) => {
    const b = el("button", "pm-item");
    b.type = "button";
    b.setAttribute("role", multi && o.v && o.v !== "\x00" ? "menuitemcheckbox" : "menuitemradio");
    const words = el("span", "pm-words");
    words.append(el("span", "pm-name", t(o.name)), el("span", "pm-note", t(o.note)));
    b.append(el("span", "pm-tick"), words);
    tick(b, o);
    b.onclick = (e) => {
      e.stopPropagation();
      if (!multi) { closeProtoMenu(); choose(o.v); return; }
      if (o.v === "\x00") return;
      if (!o.v) { picked = []; closeProtoMenu(); return; }
      picked = picked.includes(o.v) ? picked.filter((v) => v !== o.v) : [...picked, o.v];
      items.forEach((it, i) => tick(it, opts[i]));
    };
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
  // Scrolling the menu keeps it open; scrolling outside moves its anchor.
  const scroll = (e) => { if (!box.contains(e.target)) closeProtoMenu(); };
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
  document.addEventListener("scroll", scroll, true);
  addEventListener("resize", closeProtoMenu);
  const done = multi ? () => {
    if (picked.length === value.length && picked.every((v) => value.includes(v))) return;
    choose(opts.map((o) => o.v).filter((v) => picked.includes(v)));
  } : null;
  protoMenu = { box, anchor, outside, keys, scroll, done };
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

// respellURL turns a base URL into the one protocol api is asked at: the
// root for Anthropic, which adds /v1 itself, …/v1 for OpenAI's two.
function respellURL(u, api) {
  u = u.trim().replace(/\/+$/, "");
  if (api === "anthropic") return u.replace(/\/v1$/, "");
  return /\/v\d+[a-z]*$/.test(u) || !/^https?:\/\/[^/]+$/.test(u) ? u : u + "/v1";
}

// renderMove: a built-in subscription a community plugin can run — which
// of the two runs it, and the way to the other. Accounts, models and the
// agents on them stay as they are either way.
function renderMove(p) {
  const m = p.move;
  const box = el("div", "stack move");
  const onPlugin = m.state === "plugin";
  box.append(el("div", "", onPlugin
    ? t("The community plugin {pkg}, with the accounts you had here.", { pkg: m.package })
    : t("magpie's built-in sign-in. The community plugin {pkg} can run it instead, with the same accounts and models.", { pkg: m.package })));
  if (m.state === "failed" && m.error) box.append(el("div", "hint", t("The last move didn't go through, so it stays built-in: {error}", { error: m.error })));
  const b = el("button", "text", onPlugin ? t("Use the built-in again") : t("Move to the plugin"));
  b.onclick = () => {
    b.classList.add("busy");
    b.disabled = true;
    providerAction(onPlugin ? "moveback" : "move", { id: p.id }, onPlugin ? t("{name} is built-in again", { name: p.name }) : t("{name} runs on its plugin now", { name: p.name }));
  };
  box.append(b);
  return field(t("Runs on"), box, onPlugin ? t("Going back puts every account, with the plugin's newer sign-ins, back into the built-in.") : t("If an account doesn't work through the plugin, nothing changes."));
}

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
    saidMoved(okMsg);
  } catch (e) {
    // a Remove may have gone through before what failed: the list as it is
    // now, and the editor of a provider gone closes, rather than stay open
    // on it for a second Remove to say there is no such provider
    if (action === "delete" && typeof editing === "string") {
      try {
        providers = await api("providers");
        if (!providers.providers.some((p) => p.id === editing)) {
          editing = null; draft = null;
          renderProviders();
          return status(e.message, "err");
        }
      } catch { /* the error below says enough */ }
    }
    if (!editorError(e.message, "err")) status(e.message, "err");
    document.querySelector(".editor .busy")?.classList.remove("busy");
  }
}

// saidMoved says what was done, and which agents it moved off models it
// took away (a provider switched off or removed, the last account signed
// out: #200), each to the same model elsewhere or back to its default.
function saidMoved(okMsg) {
  // one magpie couldn't move (its file unwritable) is still on the model
  // gone, and the change made all the same
  let stuck = false;
  const moved = (providers.moved || []).map((m) => {
    const who = m.field === "model" ? m.agent : m.agent + " " + m.field;
    if (m.error) { stuck = true; return t("{agent} is still on {model}, which magpie no longer serves: {error}", { agent: who, model: m.from, error: m.error }); }
    return m.to ? t("{agent} moved to {model}", { agent: who, model: m.to })
      : t("{agent} is back on its default", { agent: who });
  });
  const msg = [okMsg, ...moved].filter(Boolean).join(" · ");
  if (msg) status(msg, stuck ? "warn" : "ok", stuck ? 12000 : moved.length ? 9000 : undefined);
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

// the sheet opens below the list: it unrolls on the rows' spring and the
// view goes down with it. The button stays at the view's foot however long
// the list is; with the sheet already open it takes the view down to it,
// and it steps aside while the sheet's head is in sight.
// moreSubs: beside Add provider, the reminder that more subscriptions are
// a plugin away — three of them in a stack, and where to find them
{
  const b = $("#moreSubs");
  for (const ic of ["cursor", "githubcopilot", "gemini-color"]) b.firstElementChild.append(icon(ic));
  b.append(svg(CHEV_R, 10, 1.7));
  b.onclick = () => { openPlugins(); b.blur(); };
}

$("#addProvider").onclick = (e) => {
  const view = $("#view-providers"), sheet = $("#addSheet");
  if (!adding) {
    adding = true; editing = null; draft = null; renderProviders();
    return unrollSheet(view, sheet, e);
  }
  if (!scrollOnPurpose(e, 700)) return;
  const from = view.scrollTop;
  const to = Math.min(from + sheet.getBoundingClientRect().top - view.getBoundingClientRect().top - 12, view.scrollHeight - view.clientHeight);
  if (calm()) { view.scrollTop = to; return; }
  const t0 = performance.now(), ease = (x) => 1 - Math.pow(1 - x, 3);
  let set = from;
  const step = (now) => {
    if (Math.abs(view.scrollTop - set) > 2) return; // the reader took it
    const x = Math.min(1, (now - t0) / 520);
    view.scrollTop = Math.round(from + (to - from) * ease(x));
    set = view.scrollTop;
    if (x < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
};
new IntersectionObserver(([en]) => {
  $("#view-providers .after-list").classList.toggle("away", !en.target.hidden && en.isIntersecting);
}, { root: $("#view-providers") }).observe($("#addSheet"));

// unrollSheet opens a sheet just drawn at the foot of a view from nothing to
// its height, what's in it easing down into place, and takes the view down
// with it until the sheet's top is 12px under the view's. The scroll is led
// by the height, frame by frame, from when the sheet reaches the view's foot
// to when it is whole: it scrolls only into room the sheet has made, so it
// never runs ahead to be held back and jump, nor stops short; and it is
// the view's own scrollTop, which WebKit animates where it won't a smooth
// scrollIntoView. The reader scrolling meanwhile has the view from then on.
function unrollSheet(view, sheet, e) {
  const from = view.scrollTop, room = view.scrollHeight - view.clientHeight;
  const to = Math.max(from, Math.min(from + sheet.getBoundingClientRect().top - view.getBoundingClientRect().top - 12, room));
  const h = sheet.offsetHeight;
  // how tall the sheet is when it reaches the view's foot, where the view
  // can start to move: from there the view goes down as it grows
  const x0 = Math.max(0, h - (room - from));
  const go = scrollOnPurpose(e, ROW_OPEN.ms + 300); // opened by a click, not by code
  if (!h || matchMedia("(prefers-reduced-motion: reduce)").matches) { if (go) view.scrollTop = to; return; }
  const pad = getComputedStyle(sheet);
  sheet.style.overflow = "hidden";
  const grow = sheet.animate([
    { height: "0px", paddingTop: "0px", paddingBottom: "0px" },
    { height: h + "px", paddingTop: pad.paddingTop, paddingBottom: pad.paddingBottom },
  ], { duration: ROW_OPEN.ms + 60, easing: `cubic-bezier(${ROW_OPEN.ease})` });
  for (const c of sheet.children) {
    c.animate([{ opacity: 0, transform: "translateY(-6px)" }, { opacity: 1, transform: "none" }],
      { duration: 340, delay: 60, easing: "cubic-bezier(.22, 1, .36, 1)", fill: "backwards" });
  }
  // what the view is at once the sheet starts at no height: a page that
  // was scrolled to its end is clamped shorter (WebKit), not moved by the reader
  let set = view.scrollTop;
  const follow = () => {
    if (!go || Math.abs(view.scrollTop - set) > 2) return; // the reader took it
    const done = grow.playState === "finished";
    view.scrollTop = Math.round(from + (to - from) * (done ? 1 : Math.min(1, Math.max(0, sheet.offsetHeight - x0) / (h - x0))));
    set = view.scrollTop; // as far as there was room for
    if (!done) requestAnimationFrame(follow);
  };
  const end = () => { sheet.style.overflow = ""; };
  grow.finished.then(() => { end(); follow(); }, end);
  requestAnimationFrame(follow);
}

// unrollInView: the agents' scroll unrolls under the button clicked for it,
// and in a view with no more room below (the panel at its tallest, the
// window scrolled to its end) the view goes down with it, led by the scroll's
// foot frame by frame, so what unrolls comes into sight — but never so far
// that the button goes out of it at the top. The reader scrolling meanwhile
// has the view from then on.
function unrollInView(fold, button, e) {
  const v = fold.closest(".view");
  if (!v || !scrollOnPurpose(e, UNROLL.ms + 400)) return;
  let set = v.scrollTop;
  const until = performance.now() + UNROLL.ms + 300;
  const step = () => {
    if (Math.abs(v.scrollTop - set) > 2) return false; // the reader took it
    const b = v.getBoundingClientRect(), pad = parseFloat(getComputedStyle(v).paddingBottom) || 0;
    const want = v.scrollTop + Math.min(fold.getBoundingClientRect().bottom + pad - b.bottom, button.getBoundingClientRect().top - b.top - 4);
    const to = Math.round(Math.max(set, Math.min(want, v.scrollHeight - v.clientHeight)));
    if (to !== v.scrollTop) v.scrollTop = to;
    set = v.scrollTop;
    return true;
  };
  // on each frame's layout, before it is painted, as well as on the frame
  const grown = new ResizeObserver(() => { if (!step()) grown.disconnect(); });
  grown.observe(fold);
  const frame = () => {
    if (step() && performance.now() < until) requestAnimationFrame(frame);
    else grown.disconnect();
  };
  requestAnimationFrame(frame);
}

// rollUpSheet closes it the other way, quicker, and then does what closing
// it does.
function rollUpSheet(sheet, then) {
  if (matchMedia("(prefers-reduced-motion: reduce)").matches) return then();
  const pad = getComputedStyle(sheet);
  sheet.style.overflow = "hidden";
  const a = sheet.animate([
    { height: sheet.offsetHeight + "px", paddingTop: pad.paddingTop, paddingBottom: pad.paddingBottom, opacity: 1 },
    { height: "0px", paddingTop: "0px", paddingBottom: "0px", opacity: 0 },
  ], { duration: ROLLUP.ms - 80, easing: `cubic-bezier(${ROLLUP.ease})`, fill: "forwards" });
  // held shut until it is hidden, then let go, so it is never seen whole again
  const done = () => { then(); a.cancel(); sheet.style.overflow = ""; };
  a.finished.then(done, done);
}

// ---------- usage ----------

const PERIODS = [["today", "Today"], ["7d", "7 days"], ["30d", "30 days"], ["all", "All"]];

// The subscriptions' quotas come from the vendors and can take a while (or
// never come without a proxy), so they load on their own and the local log
// never waits for them. They don't depend on the period either.
let quotas = null;
let quotasAt = 0; // when they came in
async function loadUsage() {
  renderUsageTab();
  if (usageTab === "sessions") return loadSessions();
  if (usageTab === "requests") return loadLedger();
  renderUsageLoading();
  loadQuotas();
  usage = await api("usage?period=" + period);
  renderUsage();
}

let quotasLoading = null;
function loadQuotas() {
  if (quotasLoading) return quotasLoading;
  quotasLoading = api("usage/quotas")
    .then((q) => { quotas = q || []; quotasAt = Date.now(); }, () => { quotas = quotas || []; })
    .finally(() => { quotasLoading = null; renderQuotas(); });
  return quotasLoading;
}

// the period picker, in the page's head, for the Overview and Requests
function renderPeriod(loading) {
  const seg = $("#period");
  seg.replaceChildren();
  for (const [id, name] of PERIODS) {
    const b = el("button", "opt" + (id === period ? " on" : ""), t(name));
    b.disabled = !!loading;
    b.onclick = () => { for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(seg, "period"); period = id; ledOffset = 0; loadUsage().catch((e) => status(e.message, "err")); };
    seg.append(b);
  }
  slide(seg, "period");
}

function renderUsageLoading() {
  const view = $("#view-usage");
  view.classList.add("loading");
  view.setAttribute("aria-busy", "true");
  renderPeriod(true);
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
// currency is Settings' choice of what a cost shows as: usd (its own price)
// or cny, converted with fx (the rate this session last got from magpie,
// with when that was and whether it's stale — kept only for the tooltip;
// see applyPrefs, which fills both from what /api/settings answers).
let currency = "usd";
let fx = { rate: 0, at: null, stale: true };
function fmtCost(t) {
  if (!t.cost && t.unpriced) return "";
  let c = t.cost, sign = "$";
  if (currency === "cny" && fx.rate > 0) { c = c * fx.rate; sign = "¥"; }
  const s = c >= 100 ? c.toFixed(0) : c >= 1 ? c.toFixed(2) : c.toFixed(3);
  return sign + s + (t.unpriced ? "+" : "");
}
// renderCosts redraws whatever on the Usage page shows a cost, once the
// currency changes — the numbers alone, not the page around them, so a
// click on the setting never moves anything it isn't showing (#212)
function renderCosts() {
  if (usage) renderUsage();
  if (ledger && usageTab === "requests") renderLedger();
  if (sessions) renderSessions();
}
const tokensOf = (t) => t.input + t.output;

function renderQuotas() {
  renderPanelQuota();
  const subscriptions = $("#subscriptionUsage");
  subscriptions.replaceChildren();
  // the allowances' own heading, apart from the period's cost: used or
  // left turns their meters, and is only there when some card has one (a
  // balance is only ever what is left)
  $("#quotaHead").hidden = !!quotas && !quotas.length;
  const mode = $("#quotaMode");
  mode.hidden = !quotas?.some((q) => !q.error && q.windows?.length);
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
    if (!first.user && (first.plan || first.until)) head.append(planSpan(first));
    card.append(head);
    for (const sub of subs) {
      if (sub.user) {
        const who = el("div", "subscription-account");
        const u = el("span", "user", sub.user);
        u.title = sub.user;
        who.append(u);
        if (sub.plan || sub.until) who.append(planSpan(sub));
        card.append(who);
      }
      card.append(quotaWindows(sub));
      // what is left besides the windows, under them
      if (sub.balance && sub.windows?.length && !sub.error) card.append(balanceRow(sub, "What is left on the account besides its windows"));
      if (sub.resets?.count) {
        const r = el("div", "quota-resets");
        r.append(resetsWords(sub.resets));
        const use = el("button", "text", t("Use a reset"));
        use.title = resetUseTitle(sub);
        use.onclick = () => askReset(sub);
        if (!sub.resets.byWindow) { // a GLM team's are spent on bigmodel.cn
          const auto = autoResetButton(sub, "text auto-reset");
          if (auto) r.append(auto);
          r.append(use);
        }
        card.append(r);
      }
    }
    subscriptions.append(card);
  }
}

// planTerm says when a plan's paid time ends: renewed then, over, or
// either (the vendor doesn't say which), as short as a panel row needs.
function planTerm(q) {
  if (!q.until) return "";
  const date = new Date(q.until).toLocaleDateString(locale === "zh" ? "zh-CN" : undefined, { month: "short", day: "numeric" });
  return t(q.renew === "auto" ? "Renews {date}" : q.renew === "off" ? "Expires {date}" : "Until {date}", { date });
}
function planSpan(q) {
  const s = el("span", "plan", [q.plan, planTerm(q)].filter(Boolean).join(" · "));
  if (q.until) s.title = t(q.renew === "auto" ? "Renews {date}" : q.renew === "off" ? "Expires {date}" : "Until {date}", { date: new Date(q.until).toLocaleString() });
  return s;
}

// The tray panel is three tabs over the one page: the agents, the allowances
// of every subscription and key (the "usage" tab, as it was named before)
// and the gateway's latest requests (routing.js draws those). The saved
// profiles open from Profiles at the Agents tab's foot, over the list (a tab
// of their own was one too many). The tab is remembered.
const profBtn = $("#profBtn"), profBox = $(".profiles");
function placeProfiles() {
  if (!profBox.classList.contains("open")) return;
  const r = profBtn.getBoundingClientRect(), top = $("#ptabs").getBoundingClientRect().bottom;
  profBox.style.bottom = Math.round(innerHeight - r.top + 6) + "px";
  profBox.style.maxHeight = Math.max(120, Math.round(r.top - 6 - top - 4)) + "px";
}
function openProfiles() {
  profBox.classList.add("open");
  profBtn.setAttribute("aria-expanded", "true");
  placeProfiles();
}
function closeProfiles() {
  if (!profBox.classList.contains("open")) return;
  profBox.classList.remove("open");
  profBtn.setAttribute("aria-expanded", "false");
  const f = $(".profiles > .chip-input");
  if (f) closeSave(f);
}
if (mode === "panel") {
  profBtn.hidden = false;
  document.body.append(profBox); // over the page, not in the list's scroll
  profBtn.onclick = () => profBox.classList.contains("open") ? closeProfiles() : openProfiles();
  document.addEventListener("mousedown", (e) => {
    if (profBox.classList.contains("open") && !profBox.contains(e.target) && !profBtn.contains(e.target)) closeProfiles();
  }, true);
  // the name field's Escape is its own (it stops there)
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && profBox.classList.contains("open")) { closeProfiles(); profBtn.focus({ preventScroll: true }); } });
  addEventListener("resize", placeProfiles);
}
let panelTab = "agents";
try { panelTab = localStorage.getItem("magpie.panelTab") || "agents"; } catch {}
function setPanelTab(tab) {
  const tabs = $("#ptabs");
  // no usage to show, no tab for it (nor for profiles: they open from the foot)
  const b = tabs.querySelector(`[data-ptab="${tab}"]`);
  if (!b || b.hidden) tab = "agents";
  closeProfiles();
  panelTab = tab;
  try { localStorage.setItem("magpie.panelTab", tab); } catch {}
  document.body.dataset.ptab = tab;
  for (const b of tabs.querySelectorAll("button")) {
    b.classList.toggle("on", b.dataset.ptab === tab);
    b.setAttribute("aria-selected", String(b.dataset.ptab === tab));
  }
  // the card under the tab picked glides to it, as on every other pill
  slide(tabs, "ptabs");
  panelAge();
  window.panelRoutingShown?.();
  fit();
}
if (mode === "panel") {
  const tabs = $("#ptabs");
  tabs.hidden = false;
  tabs.setAttribute("role", "tablist");
  for (const b of tabs.querySelectorAll("button")) {
    b.setAttribute("role", "tab");
    b.onclick = () => setPanelTab(b.dataset.ptab);
  }
  setPanelTab(panelTab);
  // the bar drawn before the panel has its width puts the card again, still;
  // the panel's height moving (a tab's own, a row opening) leaves it gliding
  let width = 0;
  new ResizeObserver(([e]) => {
    const w = Math.round(e.contentRect.width);
    if (w === width) return;
    width = w;
    const th = tabs.querySelector(":scope > .thumb");
    th?.classList.add("still");
    slide(tabs, "ptabs");
    void th?.offsetWidth;
    th?.classList.remove("still");
  }).observe(tabs);
}

// The Usage tab: accounts under their vendor, each window a ring with its
// share in it, when the plan ends and the windows start again under the
// account; balances last, as figures.
function renderPanelQuota() {
  const box = $("#panelQuota");
  if (mode !== "panel" || !box) return;
  const subs = (quotas || []).filter((q) => q.balance || q.error || q.windows?.length);
  const none = !!quotas && !subs.length;
  const usageTab = $('#ptabs [data-ptab="usage"]');
  if (usageTab.hidden !== none) {
    usageTab.hidden = none;
    // the tabs part the row anew: the card goes where its tab is now
    if (!(none && panelTab === "usage")) slide($("#ptabs"), "ptabs");
  }
  if (none && panelTab === "usage") setPanelTab("agents");
  box.hidden = none;
  box.replaceChildren();
  if (none) { fit(); return; }
  if (!quotas) {
    for (let i = 0; i < 2; i++) {
      const card = el("div", "pq-card");
      card.append(el("span", "skeleton sk-aq"), el("span", "skeleton sk-ring"));
      box.append(card);
    }
    fit();
    return;
  }
  const groups = new Map();
  const bals = [];
  for (const q of subs) {
    // a balance with windows (Command Code's credits beside its 5-hour and
    // weekly windows) is the windows' card; a balance alone, a figure
    if (q.balance && !q.windows?.length) { bals.push(q); continue; }
    if (!groups.has(q.name)) groups.set(q.name, []);
    groups.get(q.name).push(q);
  }
  for (const [name, qs] of groups) {
    const g = el("div", "pq-group");
    const head = el("div", "pq-gh");
    head.append(icon(qs[0].icon), el("span", "pq-gn", name));
    // several accounts: how many, a quiet count by the name; one: its plan
    // and until when, at the right
    if (qs.length > 1) {
      const n = el("span", "pq-count", String(qs.length));
      n.title = t("{n} accounts", { n: qs.length });
      head.append(n);
    } else {
      const note = [qs[0].plan, qs[0].until ? planTerm(qs[0]) : "", qs[0].balance].filter(Boolean).join(" · ");
      head.append(el("span", "pq-gnote" + (qs[0].renew === "off" ? " ends" : ""), note));
    }
    // whether the rings say what is used or what is left, once above them,
    // turned here as by a ring (#184)
    if (qs.some((q) => !q.error && q.windows?.length)) {
      const m = el("button", "pq-mode", t(quotaLeft ? "Left" : "Used"));
      m.title = t(quotaLeft ? "Show how much of each window is used" : "Show how much of each window is left");
      m.onclick = () => setQuotaLeft(!quotaLeft);
      head.append(m);
    }
    g.append(head);
    for (const q of qs) g.append(panelQuotaCard(q));
    box.append(g);
  }
  if (bals.length) {
    const g = el("div", "pq-group");
    const head = el("div", "pq-gh");
    head.append(el("span", "pq-gn", t("Balances")));
    g.append(head);
    const grid = el("div", "pq-bals");
    for (const q of bals) {
      const card = el("div", "pq-card bal");
      card.title = [q.name, q.user].filter(Boolean).join(" · ");
      // whose balance, at a glance: the provider's logo before its name
      const who = el("span", "pq-sub pq-bn");
      who.append(icon(q.icon || "generic"), el("span", "", q.name));
      card.append(who, el("b", "pq-amt", q.balance));
      grid.append(card);
    }
    g.append(grid);
    box.append(g);
  }
  panelAge();
  fit();
}

// asOfText: an allowance standing in for one that couldn't be read just
// now (a vendor rate limiting its usage endpoint) says when it was read.
function asOfText(q) {
  return t("As of {when} — couldn't be read just now", { when: new Date(q.asOf).toLocaleString() });
}

// shortWindow: "5 hours" as 5h, "7 days" as 7d; any other name as it is.
function shortWindow(name) {
  const m = /^(\d+)\s*(minute|hour|day|week|month)s?$/i.exec(name || "");
  return m ? m[1] + { minute: "m", hour: "h", day: "d", week: "w", month: "mo" }[m[2].toLowerCase()] : t(name);
}

function panelQuotaCard(q) {
  const card = el("div", "pq-card");
  card.append(el("span", "pq-user", q.user || q.name));
  card.title = [q.name, q.user, q.plan, q.until ? planTerm(q) : "", q.balance && t("Balance") + " " + q.balance].filter(Boolean).join(" · ");
  if (q.error) {
    card.classList.add("err");
    card.append(el("span", "pq-sub err", quotaError(q.error)));
    card.title += "\n" + q.error;
    return card;
  }
  if (q.asOf) card.title += "\n" + asOfText(q);
  const ws = q.windows.slice(0, 3);
  // when the windows begun start again: the first bare, the others by name
  const begun = ws.filter((w) => w.resetsAt && w.used > 0);
  card.append(el("span", "pq-sub", begun.length
    ? begun.map((w, i) => (i ? shortWindow(w.name) + " " : "↻ ") + resetClock(new Date(w.resetsAt))).join(" · ")
    : t("Not used yet")));
  const rings = el("span", "pq-rings");
  for (const w of ws) {
    const used = Math.max(0, Math.min(100, w.used));
    const r = el("span", "pq-ring" + (used >= 90 ? " full" : ""));
    const dial = el("span", "pq-dial");
    dial.style.setProperty("--p", quotaFill(w));
    dial.append(el("b", "", quotaFill(w) + "%"));
    r.append(dial, el("span", "pq-rn", shortWindow(w.name)));
    r.title = t(w.name) + " · " + quotaText(w) + (w.resetsAt ? "\n" + t("Resets {when}", { when: new Date(w.resetsAt).toLocaleString() }) + " · " + untilText(new Date(w.resetsAt)) : "")
      + "\n" + t(quotaLeft ? "Show how much of each window is used" : "Show how much of each window is left");
    // used or left turns here too, as on the Usage page (#124)
    r.onclick = () => setQuotaLeft(!quotaLeft);
    rings.append(r);
  }
  card.append(rings);
  if (q.resets?.count) {
    const r = el("div", "pq-resets");
    r.append(resetsWords(q.resets));
    const use = el("button", "pq-use", t("Use one…"));
    use.title = resetUseTitle(q);
    use.onclick = () => askReset(q);
    if (!q.resets.byWindow) {
      const auto = autoResetButton(q, "pq-use pq-auto");
      if (auto) r.append(auto);
      r.append(use);
    }
    card.append(r);
  }
  return card;
}

// resetsWords: a Codex account's rate-limit resets, or a Claude account's
// usage-limit resets, "↺ 2 resets · until Sat 22:30", the date only when
// one of them runs out. A GLM Coding team
// plan's are counted by window, "↺ 2 five-hour resets · 1 weekly reset",
// and spent on the vendor's page, not here.
function resetsWords(r) {
  const w = el("span", "resets-words");
  if (r.byWindow) {
    const parts = [];
    if (r.fiveHour) parts.push(t(r.fiveHour === 1 ? "1 five-hour reset" : "{n} five-hour resets", { n: r.fiveHour }));
    if (r.weekly) parts.push(t(r.weekly === 1 ? "1 weekly reset" : "{n} weekly resets", { n: r.weekly }));
    w.append(el("span", "resets-n", "↺ " + parts.join(" · ")));
    w.title = t("The team plan's resets, used on bigmodel.cn or z.ai");
    if (r.until) {
      const at = new Date(r.until);
      w.append(el("span", "resets-until", " · " + t("until {when}", { when: resetClock(at) })));
      w.title += "\n" + t("The first runs out {when}", { when: at.toLocaleString() });
    }
    return w;
  }
  w.append(el("span", "resets-n", "↺ " + t(r.count === 1 ? "1 reset" : "{n} resets", { n: r.count })));
  if (r.until) {
    const at = new Date(r.until);
    w.append(el("span", "resets-until", " · " + t("until {when}", { when: resetClock(at) })));
    w.title = t("The first runs out {when}", { when: at.toLocaleString() });
  }
  return w;
}

// resetUseTitle says which reset a use spends: the one that runs out
// first, so no one hesitates for fear of losing one that lasts longer; a
// Claude account's, the one Anthropic names next.
function resetUseTitle(q) {
  const r = q.resets;
  return (r.until && q.provider === "claude"
    ? t("Uses the reset Anthropic names next, good until {when}.", { when: new Date(r.until).toLocaleString() })
    : r.until
    ? t("Uses the reset that runs out first ({when}), never one that lasts longer.", { when: new Date(r.until).toLocaleString() })
    : t("Uses one of its resets; none of them runs out."))
    + "\n" + t("This account's windows start again at once, as if none had been used. You're asked before anything is spent.");
}

// autoResetButton turns on or off a Codex or Claude account spending a
// reset by itself: once its week is used up and no other account can
// answer, one a week at most. Off unless the user turns it on; nothing for
// an account with no name to keep it by.
function autoResetButton(q, cls) {
  const kept = { codex: "codexAutoReset", claude: "claudeAutoReset" }[q.provider];
  if (!q.user || !kept) return null;
  const who = q.user.toLowerCase();
  const on = !!(state.settings?.[kept] || []).includes(who);
  const b = el("button", cls + (on ? " on" : ""), t("Auto-use"));
  b.setAttribute("aria-pressed", String(on));
  b.title = t(on ? "On: a reset is used by itself when this account's week is used up and no other account can answer, one a week at most. Click to turn it off."
    : "Use a reset by itself when this account's week is used up and no other account can answer, one a week at most. The five hours running out never uses one.");
  b.onclick = async (e) => {
    e.stopPropagation();
    b.disabled = true;
    try {
      prefs = await writingPrefs(api("settings/" + q.provider + "-auto-reset", { user: q.user, on: !on }));
      state.settings = prefs;
      status(t(on ? "{who} no longer uses a reset by itself" : "{who} uses a reset by itself once its week is used up", { who: q.user }), "ok");
      renderQuotas();
    } catch (err) {
      b.disabled = false;
      status(err.message, "err");
    }
  };
  return b;
}

// askReset: spending a Codex or Claude account's reset can't be taken
// back, so it asks first; then it says what came of it and reads the
// usage again.
let resetAsk = null;
function askReset(q) {
  const claude = q.provider === "claude";
  const ed = el("div", "editor reset-ask");
  const head = el("div", "ehead");
  head.append(icon(q.icon || (claude ? "claude-color" : "codex")), el("b", "", t(claude ? "Use a Claude reset?" : "Use a Codex reset?")));
  ed.append(head);
  const who = q.user || q.name;
  ed.append(el("p", "lib-confirm", t(q.resets.count === 1
    ? "{who} has 1 reset. Using it starts its windows again at once, as if none of them had been used. It can't be undone."
    : "{who} has {n} resets. Using one starts its windows again at once, as if none of them had been used. It can't be undone.", { who, n: q.resets.count })));
  if (q.resets.until) ed.append(el("p", "lib-confirm", t(claude ? "The one used is the one Anthropic names next, good until {when}." : "The one used is the one that runs out first, {when}.", { when: new Date(q.resets.until).toLocaleString() })));
  // nothing used yet: a reset would start nothing again
  if (!q.windows?.some((w) => w.used > 0)) ed.append(el("p", "lib-confirm reset-idle", t("None of its windows has been used yet, so there is nothing to start again.")));
  const bar = el("div", "bar");
  const go = el("button", "text primary", t("Use a reset"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    try {
      const out = await api(claude ? "usage/claude-reset" : "usage/codex-reset", { user: q.user || "" });
      closeResetAsk();
      status(who + ": " + resetOutcome(out), out.code === "reset" ? "ok" : "err");
      loadQuotas();
    } catch (err) {
      go.disabled = false;
      go.classList.remove("busy");
      status(err.message, "err");
    }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = (e) => { e.stopPropagation(); closeResetAsk(); };
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  resetAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  go.focus();
}
function closeResetAsk() {
  if (!resetAsk) return;
  resetAsk = null;
  closeModal().then(() => { if (!resetAsk) $("#modal").classList.remove("lib"); });
}
// the dialog is the providers page's: while this asks, its backdrop and
// Escape close only this (in the panel, Escape would hide the window)
$("#modal").addEventListener("click", (e) => {
  if (resetAsk && e.target === e.currentTarget) { e.stopImmediatePropagation(); closeResetAsk(); }
}, true);
document.addEventListener("keydown", (e) => {
  if (resetAsk && e.key === "Escape") { e.preventDefault(); e.stopImmediatePropagation(); closeResetAsk(); }
}, true);
function resetOutcome(out) {
  switch (out.code) {
    case "reset": return t(out.windows === 1 ? "1 window started again" : "{n} windows started again", { n: out.windows });
    case "nothing_to_reset": return t("nothing to start again — no window has been used, and the reset is kept");
    case "no_credit": return t("no reset left on the account");
    case "already_redeemed":
    case "already_used": return t("that reset was already used");
    // Anthropic's
    case "not_limited": return t("nothing to reset — no window is used up yet, and the reset is kept");
    case "cooldown": return t("a reset was used a short while ago — try again later");
    case "ineligible": return t("the account can't use a reset");
    case "unavailable": return t("resets can't be used right now — try again later");
  }
  return out.code;
}

// panelAge: on the Usage tab, the footer says how old what it shows is.
function panelAge() {
  const a = $("#pqAge");
  if (!a) return;
  a.hidden = mode !== "panel" || panelTab !== "usage" || !quotasAt;
  if (!a.hidden) a.textContent = t("Updated {when}", { when: ago(quotasAt) });
}
if (mode === "panel") setInterval(panelAge, 30000);

// quotaFit puts every window's count under its name once one's doesn't fit
// beside it, so windows side by side read alike rather than one count up
// by its name and the next a line below (#90)
const quotaFit = new ResizeObserver((es) => {
  // a count's own width, its parts laid end to end: once stacked it spans
  // the row and may be two lines, so its box no longer says
  const wide = (e) => [...e.children].reduce((w, c) => w + c.getBoundingClientRect().width, 0) + 4 * (e.children.length - 1);
  for (const { target: g } of es) {
    const wraps = [...g.querySelectorAll(".quota-labels")].some((l) => {
      const [name, n] = l.children;
      return name.getBoundingClientRect().width + 6 + wide(n) > l.clientWidth;
    });
    g.classList.toggle("stacked", wraps);
  }
});

// balanceRow: what is left on an account, as a figure.
function balanceRow(sub, why) {
  const b = el("div", "quota-balance");
  b.title = t(why);
  b.append(el("span", "", t("Balance")), el("b", "", sub.balance));
  return b;
}

// quotaWindows: one account's allowance as meters, or why there are none.
function quotaWindows(sub) {
  if (sub.balance && !sub.windows?.length) return balanceRow(sub, "What is left on the account: the vendor tells only this, so Used / Left leaves it as it is");
  if (sub.error) {
    const e = el("div", "subscription-error", quotaError(sub.error));
    e.title = sub.error;
    return e;
  }
  const windows = el("div", "quota-windows");
  for (const w of sub.windows) {
    const quota = el("div", "quota");
    const labels = el("div", "quota-labels");
    // the count and its share in parts, so a count too long for its
    // window's width goes to a second line at the · rather than cut short
    const n = el("button", "quota-n");
    const [count, share] = quotaText(w).split(" · ");
    n.append(el("span", "", share ? count + " ·" : count));
    if (share) n.append(" ", el("span", "", share));
    n.title = quotaText(w) + "\n" + t(quotaLeft ? "Show how much of each window is used" : "Show how much of each window is left");
    n.onclick = () => setQuotaLeft(!quotaLeft);
    labels.append(el("span", "", t(w.name)), n);
    const track = el("div", "quota-track");
    const fill = el("i");
    fill.style.width = `${quotaFill(w)}%`;
    track.append(fill);
    quota.append(labels, track);
    // when it starts again, on the clock and how long until then
    if (w.resetsAt) {
      const at = new Date(w.resetsAt);
      const r = el("div", "quota-reset");
      r.append(el("span", "", t("Resets {when}", { when: resetClock(at) }) + " ·"), " ", el("span", "", untilText(at)));
      quota.append(r);
      quota.title = t("Resets {when}", { when: at.toLocaleString() });
    }
    windows.append(quota);
  }
  quotaFit.observe(windows);
  return windows;
}

function renderUsage() {
  const u = usage;
  const view = $("#view-usage");
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  renderPeriod();
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
      // how long the streamed replies took to begin, and how fast they
      // wrote after (#196)
      if (g.timed) {
        const ms = Math.round(g.ttft_ms / g.timed);
        sub.push(t("TTFT {ms}", { ms: ms < 1000 ? t("{n} ms", { n: ms }) : t("{n} s", { n: (ms / 1000).toFixed(1) }) }));
        if (g.decode_ms > 0) sub.push(t("{n} tok/s", { n: Math.round(g.decode_out / (g.decode_ms / 1000)) }));
      }
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

// ---------- requests ----------
//
// The ledger: every request of the period, one row each, newest first —
// what the agent asked for, where it went, the model sent and the one the
// reply says answered, the tokens, what they cost at list price, how long
// it took and how it ended — to set beside a vendor's own bill. The
// server pages it (/api/usage/requests) and saves it whole as CSV.

let ledger = null; // the page shown: { rows, offset, total, agents, …totals }
let ledOffset = 0, ledAgent = "", ledFailed = false, ledQuery = "";
const LED_PAGE = 100;

function ledParams(extra) {
  const q = new URLSearchParams({ period });
  if (ledAgent) q.set("agent", ledAgent);
  if (ledFailed) q.set("failed", "1");
  if (ledQuery.trim()) q.set("q", ledQuery.trim());
  if (extra) for (const k in extra) q.set(k, extra[k]);
  return q.toString();
}

// quiet: a refresh while it's looked at, redrawn only on a change
async function loadLedger(quiet) {
  if (!ledger && !quiet) renderLedgerLoading();
  const want = ledParams({ offset: ledOffset, limit: LED_PAGE });
  const l = await api("usage/requests?" + want);
  if (want !== ledParams({ offset: ledOffset, limit: LED_PAGE })) return; // another page or filter was picked meanwhile
  if (quiet && JSON.stringify(l) === JSON.stringify(ledger)) return;
  ledger = l;
  if (view === "usage" && usageTab === "requests") renderLedger();
}

function renderLedgerLoading() {
  const view = $("#view-usage");
  view.classList.add("loading");
  view.setAttribute("aria-busy", "true");
  renderPeriod(true);
  $("#usageCost").replaceChildren(el("span", "skeleton sk-cost"));
  $("#ledSum").textContent = "";
  $("#ledPager").hidden = true;
  const wrap = $("#ledWrap");
  wrap.classList.remove("none");
  wrap.replaceChildren();
  for (let i = 0; i < 5; i++) {
    const r = el("div", "led-sk");
    r.append(el("span", "skeleton sk-line"));
    wrap.append(r);
  }
}

const ledNum = (n) => (n || 0).toLocaleString(locale === "zh" ? "zh-CN" : "en");
const ledTook = (ms = 0) => ms < 1000 ? t("{n} ms", { n: ms }) : t("{n} s", { n: (ms / 1000).toFixed(ms < 10e3 ? 1 : 0) });
function ledTime(when) {
  const d = new Date(when), now = new Date();
  const opts = { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false };
  if (d.toDateString() !== now.toDateString()) Object.assign(opts, { month: "short", day: "numeric" });
  return d.toLocaleString(locale === "zh" ? "zh-CN" : "en", opts);
}
// the model the reply named: amber, as the Routing page's tag, when it is
// another than the one sent; plain when it is that one under a dated name
function ledServed(r) {
  if (!r.served) return el("span", "faint", "—");
  if (!r.swapped) return el("span", "muted", r.served);
  const k = el("span", "swap", r.served);
  k.title = window.swapWhy ? window.swapWhy({ model: r.model, served: r.served }) : "";
  return k;
}

const LED_COLS = [
  ["Time"], ["Agent"], ["Requested"], ["Provider · account"], ["Sent"], ["Served"], ["Effort"],
  ["In", "n"], ["Out", "n"], ["Cache write", "n"], ["Cache read", "n"], ["Cost", "n"], ["Duration", "n"], ["Status"],
];

function renderLedger() {
  const l = ledger;
  const view = $("#view-usage");
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  renderPeriod();

  const cost = $("#usageCost");
  cost.replaceChildren();
  cost.title = "";
  const c = fmtCost(l);
  if (c) {
    cost.append(el("b", "", "≈" + c), el("span", "", t("list price")));
    cost.title = l.unpriced ? t(l.unpriced === 1 ? "{n} call had no known price and is not counted" : "{n} calls had no known price and are not counted", { n: l.unpriced }) : t("At each model's list price on models.dev");
  }

  // the filters: the agents with calls in the period, and failures alone
  if (ledAgent && !l.agents.some((a) => a.id === ledAgent)) ledAgent = "";
  sessPick($("#ledAgent"), "All agents", ledAgent, l.agents.map((a) => ({ v: a.id, name: a.name, note: "" })), "Agent", (v) => { ledAgent = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  const seg = $("#ledStatus");
  seg.replaceChildren();
  for (const [on, name] of [[false, "All"], [true, "Failed"]]) {
    const b = el("button", "opt" + (on === ledFailed ? " on" : ""), t(name));
    b.onclick = () => {
      if (on === ledFailed) return;
      for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b);
      slide(seg, "ledStatus");
      ledFailed = on; ledOffset = 0;
      loadLedger().catch((e) => status(e.message, "err"));
    };
    seg.append(b);
  }
  slide(seg, "ledStatus");
  $("#ledExport").disabled = !l.total;

  const sum = [t(l.total === 1 ? "{n} request" : "{n} requests", { n: ledNum(l.total) })];
  if (l.total) {
    sum.push(t("{n} in", { n: fmtN(l.input) }), t("{n} out", { n: fmtN(l.output) }), t("{n} cache write", { n: fmtN(l.cache_write) }), t("{n} cache read", { n: fmtN(l.cache_read) }));
    if (l.errors) sum.push(t("{n} failed", { n: l.errors }));
  }
  $("#ledSum").textContent = sum.join(" · ");

  const wrap = $("#ledWrap");
  const pager = $("#ledPager");
  if (!l.total) {
    wrap.classList.add("none");
    const filtered = ledAgent || ledFailed || ledQuery.trim();
    const none = { today: "No calls today.", "7d": "No calls in the last 7 days.", "30d": "No calls in the last 30 days.", all: "No calls yet." }[period];
    wrap.replaceChildren(el("div", "led-none", filtered ? t("No requests match these filters.") : t(none)));
    pager.hidden = true;
    $("#ledNote").textContent = "";
    return;
  }
  wrap.classList.remove("none");
  const table = el("table", "led");
  const head = el("tr");
  for (const [name, cls] of LED_COLS) head.append(el("th", cls || "", t(name)));
  table.append(el("thead"), el("tbody"));
  table.tHead.append(head);
  for (const r of l.rows) {
    const bad = r.status >= 400;
    const tr = el("tr", bad ? "bad" : "");
    const td = (child, cls, title) => {
      const c = el("td", cls || "");
      if (typeof child === "string") c.textContent = child; else c.append(child);
      if (title) c.title = title;
      tr.append(c);
      return c;
    };
    td(ledTime(r.t), "when", new Date(r.t).toLocaleString(locale === "zh" ? "zh-CN" : "en"));
    const who = el("span", "who");
    who.append(icon(r.icon || "generic"), el("span", "", r.agentName || r.agent));
    td(who, "", [r.kind, r.session && t("session {id}", { id: r.session })].filter(Boolean).join(" · "));
    td(r.req || "—", "model" + (r.req ? "" : " faint"), r.req || t("Not kept for requests before this version"));
    const where = r.providerName + (r.host ? " · " + r.host : "");
    td(where, "where", where);
    td(r.model || "—", "model", r.model);
    td(ledServed(r), "model");
    td(r.effort || "—", r.effort ? "" : "faint");
    td(ledNum(r.in), "n");
    td(ledNum(r.out), "n", r.reasoning ? t("{n} reasoning, inside output", { n: ledNum(r.reasoning) }) : "");
    td(ledNum(r.cache_write), "n" + (r.cache_write ? "" : " faint"));
    td(ledNum(r.cache_read), "n" + (r.cache_read ? "" : " faint"));
    td(r.priced ? "≈" + fmtCost({ cost: r.cost, unpriced: 0 }) : "—", "n cost" + (r.priced ? "" : " faint"), r.priced ? "" : t("No known price for this model"));
    td(ledTook(r.ms), "n", r.ttft_ms ? t("TTFT {ms}", { ms: ledTook(r.ttft_ms) }) : "");
    const st = el("span", "st");
    st.append(el("i", "dot"), document.createTextNode(r.status ? String(r.status) : "—"));
    td(st, "", bad ? t("Failed: the agent was answered {status}", { status: r.status }) : "");
    table.tBodies[0].append(tr);
  }
  wrap.replaceChildren(table);

  // a page at a time: the newest first
  pager.hidden = l.total <= LED_PAGE;
  pager.replaceChildren();
  if (!pager.hidden) {
    const prev = el("button", "text", t("Newer"));
    const next = el("button", "text", t("Older"));
    prev.disabled = l.offset <= 0;
    next.disabled = l.offset + l.rows.length >= l.total;
    prev.onclick = () => { ledOffset = Math.max(0, l.offset - LED_PAGE); loadLedger().catch((e) => status(e.message, "err")); };
    next.onclick = () => { ledOffset = l.offset + LED_PAGE; loadLedger().catch((e) => status(e.message, "err")); };
    pager.append(prev, el("span", "", t("{a}–{b} of {n}", { a: ledNum(l.offset + 1), b: ledNum(l.offset + l.rows.length), n: ledNum(l.total) })), next);
  }
  $("#ledNote").textContent = t("Each request's tokens as its provider reported them; the cost at the model's list price. Export CSV saves every page.");
}

{
  let typing = 0;
  $("#ledQ").oninput = (e) => {
    ledQuery = e.target.value;
    clearTimeout(typing);
    typing = setTimeout(() => { ledOffset = 0; loadLedger().catch((err) => status(err.message, "err")); }, 250);
  };
  $("#ledQ").onkeydown = (e) => {
    if (e.key === "Escape" && e.target.value) { e.stopPropagation(); e.target.value = ""; ledQuery = ""; ledOffset = 0; loadLedger().catch((err) => status(err.message, "err")); }
  };
  $("#ledExport").onclick = async () => {
    const b = $("#ledExport");
    // in a browser the file comes to it; in the app it goes to Downloads
    if (web) {
      const a = el("a");
      a.href = "/api/usage/requests.csv?" + ledParams();
      a.download = "";
      a.click();
      return;
    }
    b.classList.add("busy");
    try {
      const r = await api("usage/requests/export?" + ledParams(), {});
      status(t(r.rows === 1 ? "Saved {n} request to {path}" : "Saved {n} requests to {path}", { n: ledNum(r.rows), path: r.path }), "ok");
    } catch (e) {
      status(e.message, "err");
    } finally {
      b.classList.remove("busy");
    }
  };
}

// ---------- sessions ----------
//
// The agents' own sessions, read from their session files: what each cost,
// and the command that picks it up again. A segment of the Usage page.

const USAGE_TABS = [["usage", "Overview"], ["requests", "Requests"], ["sessions", "Sessions"]];
let usageTab = "usage";
try { const k = localStorage.getItem("magpie.usageTab"); if (USAGE_TABS.some(([id]) => id === k)) usageTab = k; } catch {}
let sessions = null; // { sessions, terminal, dirs }
let sessAgent = "all";
let sessQuery = "";
// The totals and the chart are every session's, by day, over a range; the
// list is the latest sessions within it. [id, name, days (0: all)]
const SESS_RANGES = [["today", "Today", 1], ["7d", "7 days", 7], ["30d", "30 days", 30], ["90d", "90 days", 90], ["all", "All", 0]];
let sessRange = "30d";
// what the activity chart counts each day
const SESS_METRICS = [["tokens", "Tokens"], ["output", "Output tokens"], ["messages", "Messages"], ["sessions", "Sessions"], ["cost", "Cost"], ["active", "Active"]];
let sessMetric = "tokens";
// what the top sessions are the top by
const SESS_TOPS = [["tokens", "Tokens"], ["cost", "Cost"], ["active", "Active"]];
let sessTopBy = "tokens";
try {
  const r = localStorage.getItem("magpie.sessRange");
  if (SESS_RANGES.some(([id]) => id === r)) sessRange = r;
  const m = localStorage.getItem("magpie.sessMetric");
  if (SESS_METRICS.some(([id]) => id === m)) sessMetric = m;
  const b = localStorage.getItem("magpie.sessTop");
  if (SESS_TOPS.some(([id]) => id === b)) sessTopBy = b;
} catch {}
let sessStats = null; // { from, to, days: [{ date, usage, active }], agents } for sessRange
let sessModel = ""; // "" for every model
let sessFolder = ""; // "" for every folder
const sessOpen = new Set(); // agent:id of the sessions opened to their details
// the sessions of the range summed up under the filters, by the server:
// { count, median, p90, days, top: { tokens, cost, active } }, and the
// query it answers
let sessOver = null;
let sessOverAt = "";
let sessOverLoading = "";
let sessTopOpen = ""; // the top session opened to its details
const sessFull = new Map(); // a top session's key → the session read whole, or "…" while it is read

function renderUsageTab() {
  const seg = $("#usageTab");
  seg.replaceChildren();
  for (const [id, name] of USAGE_TABS) {
    const b = el("button", "opt" + (id === usageTab ? " on" : ""), t(name));
    b.onclick = () => {
      if (id === usageTab) return;
      usageTab = id;
      try { localStorage.setItem("magpie.usageTab", id); } catch {}
      $("#usageCost").replaceChildren();
      loadUsage().catch((e) => status(e.message, "err"));
    };
    seg.append(b);
  }
  slide(seg, "usageTab");
  const on = usageTab === "sessions";
  $("#period").hidden = on;
  $("#sessRange").hidden = !on;
  $("#usagePane").hidden = usageTab !== "usage";
  $("#ledgerPane").hidden = usageTab !== "requests";
  $("#sessionsPane").hidden = !on;
}

const sessDays = () => SESS_RANGES.find(([id]) => id === sessRange)[2];
async function loadSessions() {
  const first = !sessions || !sessStats;
  if (first) renderSessionsLoading();
  const range = sessRange, q = sessOverQ();
  const stop = first ? sessWatchIndex() : () => {};
  let s, st, o;
  try {
    [s, st, o] = await Promise.all([api("sessions"), api("sessions/stats?days=" + sessDays()), api("sessions/overview?" + q)]);
  } finally { stop(); }
  if (range !== sessRange) return; // another range was picked meanwhile; its load draws
  const fresh = q === sessOverQ(); // no filter changed meanwhile
  if (sessions && sessStats && JSON.stringify(s) === JSON.stringify(sessions) && JSON.stringify(st) === JSON.stringify(sessStats) &&
    (!fresh || JSON.stringify(o) === JSON.stringify(sessOver))) return;
  sessions = s;
  sessStats = st;
  if (fresh) { sessOver = o; sessOverAt = q; }
  if (view === "usage" && usageTab === "sessions") renderSessions();
}

// the overview's query: the range and the filters
const sessOverQ = () => new URLSearchParams({ days: sessDays(), agent: sessAgent === "all" ? "" : sessAgent, model: sessModel, cwd: sessFolder }).toString();
// loadSessOverview reads the overview again when a filter changes, and draws
// the page once it is in; one read at a time for a query
function loadSessOverview() {
  const q = sessOverQ();
  if (sessOverLoading === q) return;
  sessOverLoading = q;
  const done = () => { if (sessOverLoading === q) sessOverLoading = ""; };
  api("sessions/overview?" + q).then((o) => {
    done();
    if (q !== sessOverQ()) return;
    sessOver = o;
    sessOverAt = q;
    if (view === "usage" && usageTab === "sessions" && sessions && sessStats) renderSessions();
  }, (e) => { done(); status(e.message, "err"); });
}

function renderSessionsLoading() {
  const view = $("#view-usage");
  view.classList.add("loading");
  view.setAttribute("aria-busy", "true");
  $("#usageCost").replaceChildren(el("span", "skeleton sk-cost"));
  renderSessRange(true);
  $("#sessAgent").replaceChildren();
  $("#sessModel").hidden = $("#sessFolder").hidden = true;
  $("#sessChart").hidden = true;
  $("#sessGrid").hidden = true;
  $("#sessListHead").hidden = true;
  const stats = $("#sessStats");
  stats.classList.remove("empty");
  stats.classList.add("six");
  stats.replaceChildren();
  for (let i = 0; i < 6; i++) {
    const tile = el("div", "kpi loading-kpi");
    tile.append(el("span", "skeleton sk-number"), el("span", "skeleton sk-label"));
    stats.append(tile);
  }
  const list = $("#sessList");
  list.hidden = false;
  list.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const r = el("div", "row sess-sk");
    r.append(el("span", "skeleton sk-title"), el("span", "skeleton sk-line short"));
    list.append(r);
  }
  $("#sessNote").textContent = t("Reading the agents' session files…");
}

const SESS_INDEX_SHOW = 700;

// sessWatchIndex asks how far the reading of the session files has got
// while the page waits on it; a read of the kept index, over in a moment,
// shows the skeleton alone, and one that takes a while the indexing show.
// It returns the stop.
function sessWatchIndex() {
  let on = true, timer = 0, shown = false, since = 0;
  const tick = async () => {
    if (!on) return;
    let p = null;
    try { p = await api("sessions/progress"); } catch {}
    if (!on) return;
    // only a real indexing run is shown: a read of the kept index, however
    // long, keeps the skeleton, and so does the catch-up read of the few
    // files the agents wrote to since (every reload has some, while an agent
    // is at work), over well before SESS_INDEX_SHOW; the show would play
    // again from nought for it
    since = p?.indexing ? since || Date.now() : 0;
    const long = since && Date.now() - since >= SESS_INDEX_SHOW;
    if (p && (long || shown) && view === "usage" && usageTab === "sessions") {
      const stats = $("#sessStats");
      let hero = stats.querySelector(".sess-indexing");
      if (!hero) {
        hero = sessIndexHero();
        stats.replaceChildren(hero);
      }
      // an earlier watch's stop may have taken the class off the hero kept
      stats.classList.add("indexing");
      shown = true;
      hero.update(p);
    }
    timer = setTimeout(tick, 350);
  };
  timer = setTimeout(tick, 150);
  return () => {
    on = false;
    clearTimeout(timer);
    if (shown) $("#sessStats").classList.remove("indexing");
  };
}

// sessIndexHero is the show put on while the session files are read: the
// words and how far it has got on the left, on the right a field of dots, the
// heatmap's cells, that light up as the files land, a wave running through
// them; a hairline along the foot. Still with reduced motion.
const SESS_TIPS = [
  "Read once, kept: after this the page opens from the index in a blink",
  "Only what changed is read again, from where it was left",
  "Every session is read on this computer; nothing leaves it",
  "The biggest files go first, so none is left running on alone",
];
function sessIndexHero() {
  const hero = el("div", "sess-indexing");
  hero.setAttribute("role", "status");
  const field = el("div", "si-field");
  const cv = el("canvas", "si-canvas");
  cv.setAttribute("aria-hidden", "true");
  field.append(cv);
  const words = el("div", "si-words");
  const head = el("div", "si-head");
  head.append(el("i", "si-spin"), el("span", "si-title", t("Indexing your sessions")));
  const pct = el("div", "si-pct");
  const num = el("b", "", "");
  pct.append(num, el("span", "", "%"));
  const line = el("div", "si-line", t("Looking for session files…"));
  const tip = el("div", "si-tip", t(SESS_TIPS[0]));
  words.append(head, pct, line, tip);
  const bar = el("div", "si-bar");
  const fill = el("i", "");
  bar.append(fill);
  hero.append(words, field, bar);

  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  let frac = 0, shown = 0, known = false, tipAt = Date.now(), tipI = 0, rate = 0, last = null;
  let done = false;
  hero.update = (p) => {
    if (!(p.indexing && p.bytes > 0) && known) {
      // the run is over while the page is put together: held full, never
      // back to the unknown state or a second run from nought
      if (!done) {
        done = true;
        frac = 1;
        fill.style.width = "100%";
        hero.classList.add("done");
        line.textContent = t("Indexed · putting the page together…");
      }
      return;
    }
    if (done) return;
    known = !!p.indexing && p.bytes > 0;
    const f = known ? Math.min(1, p.read / p.bytes) : 0;
    // a steady speed, for the time left
    if (known && last && p.read > last.read) {
      const r = (p.read - last.read) / ((Date.now() - last.at) / 1000);
      rate = rate ? rate * 0.8 + r * 0.2 : r;
    }
    if (known) last = { read: p.read, at: Date.now() };
    frac = Math.max(frac, f);
    hero.classList.toggle("known", known);
    fill.style.width = known ? (frac * 100).toFixed(1) + "%" : "";
    if (known) {
      line.textContent = t("{done} of {files} files · {read} of {bytes}", { done: fmtN(p.done), files: fmtN(p.files), read: fmtBytes(p.read), bytes: fmtBytes(p.bytes) });
      const left = rate > 0 ? (p.bytes - p.read) / rate : 0;
      if (left > 3) line.textContent += " · " + t("about {t} left", { t: left < 60 ? t("{n}s", { n: Math.ceil(left) }) : t("{n} min", { n: Math.ceil(left / 60) }) });
    } else line.textContent = t("Reading the kept index…");
    if (Date.now() - tipAt > 4500) {
      tipAt = Date.now();
      tipI = (tipI + 1) % SESS_TIPS.length;
      tip.classList.remove("in");
      void tip.offsetWidth;
      tip.textContent = t(SESS_TIPS[tipI]);
      tip.classList.add("in");
    }
    if (reduced) paint(0);
  };

  // the drawing: each dot has its turn, mostly left to right, a little
  // scattered, so the edge of what is read is ragged like rain landing
  const ctx = cv.getContext("2d");
  const GAP = 14;
  let W = 0, H = 0, dpr = 1, dots = [], ink = "#1c1c21", accent = "#4f46e5", frame = 0;
  const colours = () => {
    const c = getComputedStyle(hero);
    ink = c.getPropertyValue("--fg").trim() || ink;
    accent = c.getPropertyValue("--accent").trim() || accent;
  };
  const size = () => {
    dpr = devicePixelRatio || 1;
    const r = cv.getBoundingClientRect();
    if (!r.width || !r.height) return false;
    if (r.width !== W || r.height !== H) {
      W = r.width; H = r.height;
      cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
      const cols = Math.max(1, Math.floor((W - 4) / GAP)), rows = Math.max(1, Math.floor((H - 4) / GAP));
      const ox = (W - (cols - 1) * GAP) / 2, oy = (H - (rows - 1) * GAP) / 2;
      dots = [];
      for (let j = 0; j < rows; j++) for (let i = 0; i < cols; i++) {
        const seed = Math.random();
        dots.push({ x: ox + i * GAP, y: oy + j * GAP, nx: cols > 1 ? i / (cols - 1) : 0, ny: rows > 1 ? j / (rows - 1) : 0, seed, turn: (cols > 1 ? i / (cols - 1) : 0) * 0.86 + seed * 0.14 });
      }
    }
    return true;
  };
  function paint(now) {
    if (!size()) return;
    if (frame++ % 30 === 0) colours();
    const T = now / 1000;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    shown += (frac - shown) * (reduced ? 1 : 0.06);
    // unknown: a band of light sweeps across and on again
    const scan = ((T * 0.32) % 1.4) - 0.2;
    // a faint glow where the reading is
    const gx = (known ? shown * 0.86 + 0.07 : scan) * W;
    const glow = ctx.createRadialGradient(gx, H / 2, 0, gx, H / 2, H * 0.9);
    glow.addColorStop(0, hexA(accent, reduced ? 0.06 : 0.1));
    glow.addColorStop(1, hexA(accent, 0));
    ctx.fillStyle = glow;
    ctx.fillRect(0, 0, W, H);
    for (const d of dots) {
      const wave = reduced ? 0.5 : 0.5 + 0.5 * Math.sin(d.nx * 7 + d.ny * 2.4 - T * 1.5 + d.seed * 0.8);
      let a = 0.08 + 0.05 * wave, r = 1.05 + 0.2 * wave, c = ink;
      if (known) {
        const since = shown - d.turn;
        if (since >= 0) {
          // read: resting in the accent, just landed brighter and bigger
          const fresh = Math.max(0, 1 - since / 0.07);
          c = accent;
          a = 0.4 + 0.32 * wave * (0.3 + d.seed) + 0.5 * fresh;
          r = 1.25 + 0.25 * wave + 1.1 * fresh * fresh;
        } else if (since > -0.035 && !reduced) {
          // about to land: a flicker
          const near = 1 + since / 0.035;
          if (Math.sin(T * 9 + d.seed * 40) > 0.3) { c = accent; a = 0.1 + 0.35 * near; }
        }
      } else if (!reduced) {
        const g = Math.exp(-(((d.nx - scan) / 0.07) ** 2));
        if (g > 0.02) { c = accent; a = Math.max(a, 0.9 * g * (0.55 + 0.45 * d.seed)); r += 1 * g; }
      }
      ctx.globalAlpha = Math.min(1, a);
      ctx.fillStyle = c;
      ctx.beginPath();
      ctx.arc(d.x, d.y, r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
    num.textContent = known ? String(Math.floor(shown * 100)) : "";
  }
  // it runs until the page takes it away
  const loop = (now) => {
    if (!hero.isConnected) return;
    paint(now);
    requestAnimationFrame(loop);
  };
  if (!reduced) requestAnimationFrame(loop);
  else requestAnimationFrame(() => paint(0));
  return hero;
}
// hexA is a #rrggbb colour at an alpha
function hexA(hex, a) {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return `rgba(79,70,229,${a})`;
  const n = parseInt(m[1], 16);
  return `rgba(${n >> 16},${(n >> 8) & 255},${n & 255},${a})`;
}
function fmtBytes(n) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (i && n < 10 ? n.toFixed(1) : Math.round(n)) + " " + u[i];
}

const sessKey = (s) => s.agent + ":" + s.id;
const sessTokens = (s) => s.input + s.output;
// a session's cost: "—" when none of its models has a known price
function sessCost(s) {
  if (!s.models.some((m) => m.priced && (m.input || m.output || m.cache_read || m.cache_write))) return "—";
  return "≈" + fmtCost({ cost: s.cost, unpriced: s.unpriced });
}
function ago(when) {
  const sec = (new Date(when) - Date.now()) / 1000;
  const rtf = new Intl.RelativeTimeFormat(locale === "zh" ? "zh-CN" : "en", { numeric: "auto" });
  for (const [unit, n] of [["year", 31536000], ["month", 2592000], ["week", 604800], ["day", 86400], ["hour", 3600], ["minute", 60]]) {
    if (Math.abs(sec) >= n) return rtf.format(Math.round(sec / n), unit);
  }
  return t("just now");
}
function stamp(when) {
  return new Date(when).toLocaleString(locale === "zh" ? "zh-CN" : undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
const baseName = (p) => (p || "").replace(/[\\/]+$/, "").split(/[\\/]/).pop() || p;

// the range picker, in the page's head where the Overview's period is
function renderSessRange(loading) {
  const seg = $("#sessRange");
  seg.replaceChildren();
  for (const [id, name] of SESS_RANGES) {
    const b = el("button", "opt" + (id === sessRange ? " on" : ""), t(name));
    b.disabled = !!loading;
    b.onclick = () => {
      if (id === sessRange) return;
      sessRange = id;
      try { localStorage.setItem("magpie.sessRange", id); } catch {}
      sessStats = null;
      loadSessions().catch((e) => status(e.message, "err"));
    };
    seg.append(b);
  }
  slide(seg, "sessRange");
}

// a "YYYY-MM-DD" as a local date, and back
const sessDate = (d) => { const [y, m, day] = d.split("-").map(Number); return new Date(y, m - 1, day); };
const sessISO = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const sessDay = (d) => d.toLocaleDateString(locale === "zh" ? "zh-CN" : "en", { month: "short", day: "numeric" });
// a length of time, as hours and minutes
function fmtDur(sec) {
  const m = Math.round(sec / 60);
  if (!sec) return "0";
  if (m < 1) return t("<1m");
  if (m < 60) return t("{m}m", { m });
  return t("{h}h {m}m", { h: Math.floor(m / 60), m: m % 60 });
}

// sessPick is a filter button that drops a menu of what there is to pick
function sessPick(btn, all, value, opts, head, choose) {
  btn.hidden = opts.length < 2 && !value;
  const cur = opts.find((o) => o.v === value);
  btn.classList.toggle("set", !!value);
  btn.replaceChildren(el("span", "", value ? (cur?.name || value) : t(all)), svg(CHEV, 11, 1.6));
  btn.title = value || "";
  btn.onclick = (e) => {
    e.stopPropagation();
    if (btn.classList.contains("open")) return closeProtoMenu();
    openSessCombo(btn, all, opts, value, choose, head);
  };
}

// openSessCombo is a filter's menu with a search field over it: thousands of
// folders open at once, as only the first matches are drawn, and a few
// letters of a name or path find the one wanted.
const SESS_COMBO_MAX = 150;
function openSessCombo(anchor, all, opts, value, choose, head) {
  closeProtoMenu();
  const box = el("div", "pop proto-menu sess-menu");
  box.setAttribute("role", "menu");
  const top = el("div", "sc-top");
  const q = el("input", "sc-q");
  q.type = "search";
  q.placeholder = t("Search {n}…", { n: fmtN(opts.length) });
  q.setAttribute("aria-label", t(head));
  q.autocomplete = "off";
  q.spellcheck = false;
  top.append(el("div", "pm-head", t(head)), q);
  const list = el("div", "sc-list");
  box.append(top, list);
  let items = [];
  const item = (o) => {
    const b = el("button", "pm-item" + (o.v === value ? " on" : ""));
    b.type = "button";
    b.setAttribute("role", "menuitemradio");
    b.setAttribute("aria-checked", o.v === value);
    const tick = el("span", "pm-tick");
    if (o.v === value) tick.append(svg(CHECK, 12, 1.9));
    const words = el("span", "pm-words");
    words.append(el("span", "pm-name", o.name), el("span", "pm-note", o.note));
    b.title = o.v;
    b.append(tick, words);
    b.onclick = (e) => { e.stopPropagation(); closeProtoMenu(); choose(o.v); };
    b.onmouseenter = () => b.focus({ preventScroll: true });
    return b;
  };
  const draw = () => {
    const words = q.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
    const hit = !words.length ? opts : opts.filter((o) => { const h = (o.name + " " + o.v).toLowerCase(); return words.every((w) => h.includes(w)); });
    const shown = hit.slice(0, SESS_COMBO_MAX);
    // the one picked stays in reach when it is past the first ones
    const cur = !words.length && value && !shown.some((o) => o.v === value) && opts.find((o) => o.v === value);
    items = [...(words.length ? [] : [{ v: "", name: t(all), note: "" }]), ...(cur ? [cur] : []), ...shown].map(item);
    list.replaceChildren(...items);
    if (!hit.length) list.append(el("div", "sc-none", t("No match")));
    else if (hit.length > shown.length) list.append(el("div", "sc-more", t("{n} more — type to narrow", { n: fmtN(hit.length - shown.length) })));
  };
  draw();
  q.oninput = () => { draw(); box.scrollTop = 0; };
  document.body.append(box);
  const r = anchor.getBoundingClientRect(), w = box.offsetWidth, h = box.offsetHeight, pad = 8;
  let y = r.bottom + 5;
  if (y + h > innerHeight - pad && r.top - 5 - h >= pad) { y = r.top - 5 - h; box.classList.add("up"); }
  box.style.left = Math.max(pad, Math.min(r.left, innerWidth - w - pad)) + "px";
  box.style.top = Math.max(pad, y) + "px";
  anchor.classList.add("open");
  const outside = (e) => { if (!box.contains(e.target) && !anchor.contains(e.target)) closeProtoMenu(); };
  const scroll = (e) => { if (!box.contains(e.target)) closeProtoMenu(); };
  const keys = (e) => {
    const i = items.indexOf(document.activeElement);
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closeProtoMenu(); anchor.focus(); }
    else if ((e.key === "ArrowDown" || e.key === "ArrowUp") && items.length) {
      e.preventDefault(); e.stopPropagation();
      const n = items.length, from = i < 0 ? (e.key === "ArrowDown" ? n - 1 : 0) : i;
      items[(from + (e.key === "ArrowDown" ? 1 : n - 1)) % n].focus();
    } else if (e.key === "Enter" && document.activeElement === q) {
      e.preventDefault();
      const o = items[0];
      if (o) o.click();
    } else if (i >= 0 && e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      // typing on a row goes on in the search
      q.focus({ preventScroll: true });
    }
  };
  document.addEventListener("mousedown", outside, true);
  document.addEventListener("keydown", keys, true);
  document.addEventListener("scroll", scroll, true);
  addEventListener("resize", closeProtoMenu);
  protoMenu = { box, anchor, outside, keys, scroll };
  q.focus({ preventScroll: true });
}

function renderSessions() {
  const view = $("#view-usage");
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  renderSessRange();
  const all = sessions?.sessions || [];
  const st = sessStats || { from: "", to: "", days: [], agents: {} };
  const rows = st.days.flatMap((d) => d.usage.map((u) => ({ ...u, date: d.date })));
  const acts = st.days.flatMap((d) => d.active.map((a) => ({ ...a, date: d.date })));

  // one segment per agent that has sessions, or usage in the range
  const agents = [...new Map([...all.map((s) => [s.agent, s.name]), ...Object.entries(st.agents || {})]).entries()];
  if (sessAgent !== "all" && !agents.some(([id]) => id === sessAgent)) sessAgent = "all";
  const seg = $("#sessAgent");
  seg.replaceChildren();
  seg.hidden = agents.length < 2;
  for (const [id, name] of [["all", t("All")], ...agents]) {
    const b = el("button", "opt" + (id === sessAgent ? " on" : ""), name);
    b.onclick = () => { sessAgent = id; renderSessions(); };
    seg.append(b);
  }
  slide(seg, "sessAgent");

  // the model and folder filters offer what the range has, under the other filters
  const byAgent = (x) => sessAgent === "all" || x.agent === sessAgent;
  const byModel = (x) => !sessModel || x.model === sessModel;
  const byFolder = (x) => !sessFolder || x.cwd === sessFolder;
  const spent = (list, key) => {
    const m = new Map();
    for (const r of list) if (r[key]) m.set(r[key], (m.get(r[key]) || 0) + r.input + r.output);
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  };
  const models = spent(rows.filter((r) => byAgent(r) && byFolder(r)), "model").map(([v, n]) => ({ v, name: v, note: t("{n} tokens", { n: fmtN(n) }) }));
  const folders = spent(rows.filter((r) => byAgent(r) && byModel(r)), "cwd").map(([v, n]) => ({ v, name: baseName(v), note: v + " · " + t("{n} tokens", { n: fmtN(n) }) }));
  sessPick($("#sessModel"), "All models", sessModel, models, "Model", (v) => { sessModel = v; renderSessions(); });
  sessPick($("#sessFolder"), "All folders", sessFolder, folders, "Folder", (v) => { sessFolder = v; renderSessions(); });

  // the list: the latest sessions active in the range, under every filter
  const from = sessDays() && st.from ? sessDate(st.from) : null;
  const q = sessQuery.trim().toLowerCase();
  const list = all.filter((s) => byAgent(s) && (!sessFolder || s.cwd === sessFolder) &&
    (!sessModel || s.models.some((m) => m.model === sessModel)) && (!from || new Date(s.last) >= from) &&
    (!q || [s.title, s.cwd, s.id, s.name, ...s.models.map((m) => m.model)].some((x) => (x || "").toLowerCase().includes(q))));

  // the range's totals, of every session under the filters
  const used = rows.filter((r) => byAgent(r) && byModel(r) && byFolder(r));
  const tot = { input: 0, output: 0, cache_read: 0, cache_write: 0, cost: 0, unpriced: 0 };
  const unpriced = new Set();
  for (const r of used) {
    for (const k of ["input", "output", "cache_read", "cache_write", "cost"]) tot[k] += r[k];
    if (!r.priced) unpriced.add(r.model);
  }
  tot.unpriced = unpriced.size;
  // active time isn't told apart by model
  const active = sessModel ? null : acts.filter((a) => byAgent(a) && byFolder(a)).reduce((n, a) => n + a.seconds, 0);

  const cost = $("#usageCost");
  cost.replaceChildren();
  cost.title = "";
  const c = tot.cost ? fmtCost(tot) : "";
  const unpricedNote = tot.unpriced ? t("Not counted: {models}, with no known price", { models: [...unpriced].join(", ") }) : t("At each model's list price on models.dev");
  if (c) {
    cost.append(el("b", "", "≈" + c), el("span", "", t("list price")));
    cost.title = unpricedNote;
  }

  // the overview is read again for the filters as they are now; until it is
  // in, the last one stands, dimmed
  if (sessOverAt !== sessOverQ()) loadSessOverview();
  const ov = sessOver || {};
  const stale = sessOverAt !== sessOverQ();

  const stats = $("#sessStats");
  stats.replaceChildren();
  const box = $("#sessList");
  box.replaceChildren();
  const chart = $("#sessChart");
  const grid = $("#sessGrid");
  const head = $("#sessListHead");
  if (!used.length && !list.length) {
    stats.classList.remove("six");
    stats.classList.add("empty");
    const filtered = sessAgent !== "all" || sessModel || sessFolder || q;
    stats.append(el("div", "none", !all.length && !rows.length ? t("No sessions yet. Claude Code's, Codex's, OpenCode's and Pi's sessions on this computer show up here, with what each cost and the command that resumes it.") : filtered ? t("No session matches.") : t("Nothing in this range.")));
    box.hidden = true;
    chart.hidden = true;
    grid.hidden = true;
    // the search stays where it was typed, to be cleared
    head.hidden = !q;
  } else {
    stats.classList.remove("empty");
    stats.classList.add("six");
    const tile = (n, label, sub, title, cls) => {
      const e = el("div", "kpi" + (cls ? " " + cls : ""));
      if (title) e.title = title;
      e.append(el("b", "", n), el("span", "", label));
      if (sub) e.append(el("small", "", sub));
      stats.append(e);
    };
    const days = new Set(used.map((r) => r.date)).size;
    // how many sessions, and what the middle one and the one in ten spent
    tile(ov.count == null ? "—" : fmtN(ov.count), t("sessions"), ov.count ? t("median {m} · p90 {p}", { m: fmtN(ov.median), p: fmtN(ov.p90) }) : "",
      t("Tokens in and out per session: the middle session's, and what nine in ten stay under"), stale ? "stale" : "");
    tile(fmtN(tot.input + tot.output), t("tokens"), t("{a} in · {b} out", { a: fmtN(tot.input), b: fmtN(tot.output) }));
    tile(c ? "≈" + c : "—", t("cost"), t("at list price"), unpricedNote);
    const prompt = tot.input + tot.cache_read;
    tile(fmtN(tot.cache_read), t("cache read"), tot.cache_read && prompt ? t("hit rate {p}", { p: Math.round(100 * tot.cache_read / prompt) + "%" }) : "", tot.cache_write ? t("{n} written", { n: fmtN(tot.cache_write) }) : "");
    tile(active == null ? "—" : fmtDur(active), t("active"), active == null ? t("not kept by model") : t(days === 1 ? "on {n} day" : "on {n} days", { n: days }),
      t("The time the sessions were at work: the pauses between one message and the next, each under five minutes"));
    // how many folders, and how much of it the first took
    const byCwd = sessSums(used, "cwd");
    const lead = byCwd[0], spentAll = byCwd.reduce((n, f) => n + f.n, 0);
    tile(String(byCwd.length), t("projects"), !lead ? "" : byCwd.length > 1 && spentAll ? t("{p} in {name}", { p: Math.round(100 * lead.n / spentAll) + "%", name: baseName(lead.v) }) : baseName(lead.v), lead?.v || "");

    const actsIn = sessModel ? null : acts.filter((a) => byAgent(a) && byFolder(a));
    renderSessChart(chart, st, used, ov, actsIn);
    grid.hidden = false;
    // the folders and models to pick from, as the pickers offer them
    sessBars($("#sessProjects"), "Projects", sessSums(rows.filter((r) => byAgent(r) && byModel(r)), "cwd"), sessFolder, (v) => { sessFolder = v; renderSessions(); }, baseName);
    sessBars($("#sessModels"), "Models", sessSums(rows.filter((r) => byAgent(r) && byFolder(r)), "model"), sessModel, (v) => { sessModel = v; renderSessions(); }, (v) => v);
    renderSessHours($("#sessHours"), actsIn);
    renderSessTop();
    renderSessShape();
    renderSessTools();
    renderSessSkills();
    head.hidden = !list.length && !q;
    box.hidden = !list.length && !q;
    for (const s of list) box.append(sessionItem(s));
    if (!list.length && q) box.append(el("div", "empty-state", t("No session matches.")));
  }
  const dirs = (sessions?.dirs || []).join(" · ");
  $("#sessNote").textContent = t("Totals count every session in the agents' own files; the list is the latest {n} by activity · {dirs}", { n: all.length, dirs });
}

// sessSums adds up usage rows by one of their fields, the most tokens first
function sessSums(list, key) {
  const m = new Map();
  for (const r of list) {
    if (!r[key]) continue;
    const s = m.get(r[key]) || { v: r[key], n: 0, cost: 0, unpriced: 0 };
    s.n += r.input + r.output;
    s.cost += r.cost;
    if (!r.priced) s.unpriced++;
    m.set(r[key], s);
  }
  return [...m.values()].sort((a, b) => b.n - a.n);
}

// sessLevels grades values 0 (none) to 4 by the quartiles of those above 0,
// as a calendar of contributions does
function sessLevels(values) {
  const nz = values.filter((v) => v > 0).sort((a, b) => a - b);
  if (!nz.length) return () => 0;
  const q = (p) => nz[Math.min(nz.length - 1, Math.floor(p * nz.length))];
  const [a, b, c, top] = [q(0.25), q(0.5), q(0.75), nz[nz.length - 1]];
  return (v) => v <= 0 ? 0 : v >= top ? 4 : v <= a ? 1 : v <= b ? 2 : v <= c ? 3 : 4;
}
function sessLegend() {
  const l = el("span", "sess-legend");
  l.append(el("span", "", t("Less")));
  for (let i = 0; i <= 4; i++) l.append(el("i", "l" + i));
  l.append(el("span", "", t("More")));
  return l;
}
// the short names of the days of the week, Monday first
function sessWeekdays() {
  const loc = locale === "zh" ? "zh-CN" : "en";
  return Array.from({ length: 7 }, (_, i) => new Date(2024, 0, 1 + i).toLocaleDateString(loc, { weekday: "short" }));
}
function sessCardHead(title) {
  const h = el("div", "sess-card-head");
  h.append(el("span", "label", t(title)));
  return h;
}
function sessSegs(opts, cur, choose) {
  const seg = el("div", "segs");
  for (const [id, name, off] of opts) {
    const b = el("button", "opt" + (id === cur ? " on" : ""), t(name));
    if (off) { b.disabled = true; b.title = off; }
    b.onclick = () => { if (id !== cur) choose(id); };
    seg.append(b);
  }
  return seg;
}

// renderSessChart draws the range's activity: day by day as bars up to a
// hundred days, as a calendar up to a year, week by week past that. It counts
// tokens (output on top of input, as on the Overview), output tokens alone,
// messages, sessions at work, cost or active time.
function renderSessChart(chart, st, used, ov, acts) {
  const perDay = ov.days, msgs = ov.messages;
  const first = sessDate(st.from), last = sessDate(st.to);
  const n = Math.round((last - first) / 864e5) + 1;
  chart.hidden = !(n >= 2);
  if (chart.hidden) { chart.dataset.drawn = ""; return; }
  const days = [], at = new Map();
  for (let i = 0, d = new Date(first); i < n; i++, d.setDate(d.getDate() + 1)) {
    const x = { day: new Date(d), input: 0, output: 0, cost: 0, unpriced: 0, sessions: perDay?.length === n ? perDay[i] : 0,
      messages: msgs?.length === n ? msgs[i] : 0, active: 0 };
    days.push(x);
    at.set(sessISO(d), x);
  }
  for (const r of used) {
    const x = at.get(r.date);
    if (!x) continue;
    x.input += r.input; x.output += r.output; x.cost += r.cost;
    if (!r.priced) x.unpriced++;
  }
  for (const a of acts || []) { const x = at.get(a.date); if (x) x.active += a.seconds; }
  const off = { sessions: perDay?.length === n ? "" : t("Still counting"), messages: msgs?.length === n ? "" : t("Still counting"), active: acts ? "" : t("not kept by model") };
  const metric = off[sessMetric] ? "tokens" : sessMetric;
  const value = (x) => metric === "cost" ? x.cost : metric === "sessions" ? x.sessions : metric === "messages" ? x.messages :
    metric === "active" ? x.active : metric === "output" ? x.output : x.input + x.output;
  const show = (v) => metric === "cost" ? "≈" + fmtCost({ cost: v }) : metric === "active" ? fmtDur(v) : fmtN(v);
  const tip = (x, when) => {
    const parts = [x.input + x.output ? t("{n} tokens", { n: fmtN(x.input + x.output) }) : "", x.cost && fmtCost(x) ? "≈" + fmtCost(x) : "",
      x.messages ? t(x.messages === 1 ? "{n} message" : "{n} messages", { n: fmtN(x.messages) }) : "",
      x.sessions ? t(x.sessions === 1 ? "{n} session" : "{n} sessions", { n: x.sessions }) : "", x.active ? t("{d} active", { d: fmtDur(x.active) }) : ""].filter(Boolean);
    return parts.length ? when + " · " + parts.join(" · ") : t("{when} · nothing", { when });
  };
  const shape = n <= 100 ? "day" : n <= 371 ? "calendar" : "week";
  // the page reads the sessions again every few seconds, and while an agent
  // is at work today's numbers change each time: the same days by the same
  // metric keep their bars, filled in again where they are, so the one under
  // the pointer — today's, as often as not — keeps its tooltip (#309)
  const drawn = [shape, metric, st.from, n, locale, ...Object.values(off).map(Boolean)].join("|");
  const same = chart.dataset.drawn === drawn;
  chart.dataset.drawn = drawn;
  chart.redraw = () => renderSessChart(chart, st, used, ov, acts); // the metrics kept draw what is in now

  if (same && shape === "calendar") {
    const lv = sessLevels(days.map(value));
    chart.querySelectorAll(".sess-cal > i").forEach((c, i) => {
      c.className = "l" + lv(value(days[i]));
      c.title = tip(days[i], sessDay(days[i].day));
    });
    chart.querySelector(".sess-cal-side").replaceWith(sessCalSide(days, value, show));
    return;
  }
  if (shape === "calendar") {
    chart.replaceChildren();
    chart.classList.add("cal");
    drawHead();
    const wrap = el("div", "sess-cal-wrap");
    wrap.append(sessCalendar(days, value, tip), sessCalSide(days, value, show));
    chart.append(wrap);
    chart.querySelector(".sess-chart-head").append(sessLegend());
    return;
  }

  // bars: a day each, or a week
  const step = shape === "week" ? 7 : 1;
  const buckets = [];
  for (let i = 0; i < n; i += step) {
    const b = { day: days[i].day, input: 0, output: 0, cost: 0, unpriced: 0, sessions: 0, messages: 0, active: 0 };
    for (const x of days.slice(i, i + step)) for (const k of ["input", "output", "cost", "unpriced", "sessions", "messages", "active"]) b[k] += x[k];
    buckets.push(b);
  }
  const peak = Math.max(metric === "cost" ? 0.001 : 1, ...buckets.map(value));
  const fill = (bar, b) => {
    const [top, low] = bar.children;
    if (metric === "tokens") {
      top.style.height = (100 * b.output / peak).toFixed(1) + "%";
      low.style.height = (100 * b.input / peak).toFixed(1) + "%";
    } else top.style.height = (100 * value(b) / peak).toFixed(1) + "%";
    const label = sessDay(b.day);
    bar.title = tip(b, step === 7 ? t("week of {label}", { label }) : label);
  };
  if (same) {
    chart.querySelector(".sess-chart-head .peak").textContent = show(peak);
    const bars = chart.querySelectorAll(".bars > .bar");
    buckets.forEach((b, i) => fill(bars[i], b));
    return;
  }
  chart.replaceChildren();
  chart.classList.remove("cal");
  drawHead().append(el("span", "peak", show(peak)));
  const bars = el("div", "bars");
  const labels = el("div", "labels");
  const k = buckets.length;
  const every = k <= 8 ? 1 : k <= 31 ? Math.ceil(k / 6) : Math.ceil(k / 5);
  buckets.forEach((b, i) => {
    const bar = el("div", "bar");
    if (metric === "tokens") bar.append(el("i", "out"), el("i", "in"));
    else bar.append(el("i", "out"));
    fill(bar, b);
    bars.append(bar);
    const label = sessDay(b.day);
    const end = i === k - 1 && (k - 1) % every >= every / 2;
    labels.append(el("span", "", i % every === 0 || end ? label : ""));
  });
  chart.append(bars, labels);

  function drawHead() {
    const head = el("div", "sess-chart-head");
    const seg = sessSegs(SESS_METRICS.map(([id, name]) => [id, name, off[id]]), metric, (id) => {
      sessMetric = id;
      try { localStorage.setItem("magpie.sessMetric", id); } catch {}
      chart.redraw();
    });
    head.append(el("span", "label", t(shape === "week" ? "By week" : shape === "day" ? "By day" : "Activity")), seg, el("span", "grow"));
    chart.append(head);
    slide(seg, "sessMetric");
    return head;
  }
}

// sessCalendar is the range as weeks of days, Monday on top, each day as
// dark as it is busy among the others
function sessCalendar(days, value, tip) {
  const lv = sessLevels(days.map(value));
  const lead = (days[0].day.getDay() + 6) % 7;
  const weeks = Math.ceil((lead + days.length) / 7);
  const cal = el("div", "sess-cal");
  cal.style.gridTemplateColumns = `auto repeat(${weeks}, minmax(0, 1fr))`;
  cal.style.maxWidth = `calc(2.6em + ${weeks * 20}px)`;
  const names = sessWeekdays();
  for (const i of [0, 2, 4]) {
    const l = el("span", "wd", names[i]);
    l.style.gridArea = `${i + 2} / 1`;
    cal.append(l);
  }
  const loc = locale === "zh" ? "zh-CN" : "en";
  let month = -1, labelAt = -9;
  days.forEach((x, i) => {
    const k = lead + i, w = Math.floor(k / 7), wd = k % 7;
    if ((wd === 0 || i === 0) && x.day.getMonth() !== month) {
      month = x.day.getMonth();
      if (w - labelAt >= 3) {
        const m = el("span", "mo", x.day.toLocaleDateString(loc, month === 0 && weeks > 20 ? { month: "short", year: "numeric" } : { month: "short" }));
        m.style.gridArea = `1 / ${w + 2}`;
        cal.append(m);
        labelAt = w;
      }
    }
    const c = el("i", "l" + lv(value(x)));
    c.style.gridArea = `${wd + 2} / ${w + 2}`;
    c.title = tip(x, sessDay(x.day));
    cal.append(c);
  });
  return cal;
}

// beside the calendar: the days at work, the longest run of them, the busiest
function sessCalSide(days, value, show) {
  const side = el("div", "sess-cal-side");
  const fact = (label, v, sub) => {
    const f = el("div", "fact");
    f.append(el("b", "", v), el("span", "", label));
    if (sub) f.append(el("small", "", sub));
    side.append(f);
  };
  const busy = (x) => x.input + x.output || x.active || x.sessions;
  let run = 0, streak = 0, best = null;
  for (const x of days) {
    run = busy(x) ? run + 1 : 0;
    streak = Math.max(streak, run);
    if (value(x) > 0 && (!best || value(x) > value(best))) best = x;
  }
  fact(t("days at work"), `${days.filter(busy).length} / ${days.length}`);
  fact(t("longest streak"), t(streak === 1 ? "{n} day" : "{n} days", { n: streak }));
  if (best) fact(t("busiest day"), sessDay(best.day), show(value(best)));
  return side;
}

// renderSessHours lays the range's active time out by day of the week and
// hour of the day, in this computer's time zone
function renderSessHours(box, acts) {
  box.replaceChildren();
  const head = sessCardHead("By hour");
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  head.append(el("span", "grow"), el("span", "tz", tz));
  box.append(head);
  if (!acts) return box.append(el("div", "sess-none", t("Active time isn't kept by model.")));
  const grid = Array.from({ length: 7 }, () => new Array(24).fill(0));
  let total = 0;
  for (const a of acts) {
    if (!a.hours) continue;
    const wd = (sessDate(a.date).getDay() + 6) % 7;
    a.hours.forEach((s, h) => { grid[wd][h] += s; total += s; });
  }
  if (!total) return box.append(el("div", "sess-none", t("No active time in this range.")));
  const lv = sessLevels(grid.flat());
  const names = sessWeekdays();
  const hh = (h) => String(h).padStart(2, "0") + ":00";
  const map = el("div", "sess-hours");
  let best = [0, 0];
  grid.forEach((row, wd) => {
    map.append(el("span", "wd", names[wd]));
    row.forEach((s, h) => {
      const c = el("i", "l" + lv(s));
      c.title = `${names[wd]} ${hh(h)} · ` + (s ? fmtDur(s) : t("nothing"));
      if (s > grid[best[0]][best[1]]) best = [wd, h];
      map.append(c);
    });
  });
  map.append(el("span", ""));
  for (let h = 0; h < 24; h++) map.append(el("span", "hr", h % 6 === 0 ? String(h) : ""));
  box.append(map);
  const foot = el("div", "sess-card-foot");
  foot.append(el("span", "", t("Busiest at {when}", { when: `${names[best[0]]} ${hh(best[1])}` })), el("span", "grow"), sessLegend());
  box.append(foot);
}

// renderSessTop lists the sessions that spent the most in the range, under
// the filters; a click opens one to its details, read whole from its files
function renderSessTop() {
  const box = $("#sessTop");
  const ov = sessOver || {};
  box.replaceChildren();
  box.classList.toggle("stale", sessOverAt !== sessOverQ());
  const head = sessCardHead("Top sessions");
  const seg = sessSegs(SESS_TOPS, sessTopBy, (id) => {
    sessTopBy = id;
    try { localStorage.setItem("magpie.sessTop", id); } catch {}
    renderSessTop();
  });
  head.append(el("span", "grow"), seg);
  box.append(head);
  slide(seg, "sessTop");
  const list = ov.top?.[sessTopBy] || [];
  const body = el("div", "sess-tops");
  if (!list.length) body.append(el("div", "sess-none", ov.top ? t("Nothing in this range.") : "…"));
  const icons = new Map((sessions?.sessions || []).map((s) => [s.agent, s.icon]));
  const names = sessStats?.agents || {};
  for (const s of list) {
    const open = sessTopOpen === s.key;
    const item = el("div", "sess-item" + (open ? " open" : ""));
    const r = el("div", "row sess sess-top");
    r.append(icon(icons.get(s.agent) || "generic"));
    const who = el("div", "who");
    who.append(el("div", "name", s.title || t("(no prompt)")));
    const sub = el("div", "sub", [s.cwd ? baseName(s.cwd) : "", names[s.agent] || s.agent, s.models?.[0], ago(s.last)].filter(Boolean).join(" · "));
    sub.title = s.cwd || "";
    who.append(sub);
    r.append(who);
    const fc = fmtCost({ cost: s.cost, unpriced: !s.priced });
    const v = sessTopBy === "cost" ? (fc ? "≈" + fc : "—") : sessTopBy === "active" ? fmtDur(s.active) : fmtN(s.input + s.output);
    r.append(el("div", "v", v));
    r.onclick = () => {
      if (window.getSelection()?.toString()) return;
      sessTopOpen = open ? "" : s.key;
      const had = sessFull.get(s.key);
      if (!open && (!had || had.error)) {
        sessFull.set(s.key, "…");
        api("sessions/one?key=" + encodeURIComponent(s.key)).then((full) => sessFull.set(s.key, full), (e) => sessFull.set(s.key, { error: e.message }))
          .then(() => { if (sessTopOpen === s.key && view === "usage" && usageTab === "sessions") renderSessTop(); });
      }
      renderSessTop();
    };
    item.append(r);
    if (open) {
      const full = sessFull.get(s.key);
      item.append(!full || full === "…" ? el("div", "sess-detail sess-wait", t("Reading the session…")) : full.error ? el("div", "sess-detail sess-wait", full.error) : sessionDetail(full));
    }
    body.append(item);
  }
  box.append(body);
}

// the ways a session's shape is told, and the one shown
const SESS_SHAPES = [["messages", "Messages"], ["minutes", "Length"], ["autonomy", "Tool calls"]];
let sessShapeBy = "messages";
try { const v = localStorage.getItem("magpie.sessShape"); if (SESS_SHAPES.some(([id]) => id === v)) sessShapeBy = v; } catch {}

// renderSessShape counts the range's sessions by how long they ran: by
// messages, by minutes at work, or by tool calls a prompt
function renderSessShape() {
  const box = $("#sessShape");
  const ov = sessOver || {};
  box.replaceChildren();
  box.classList.toggle("stale", sessOverAt !== sessOverQ());
  const sh = ov.shape?.[sessShapeBy];
  const head = sessCardHead("Session shape");
  const seg = sessSegs(SESS_SHAPES, sessShapeBy, (id) => {
    sessShapeBy = id;
    try { localStorage.setItem("magpie.sessShape", id); } catch {}
    renderSessShape();
  });
  head.append(seg, el("span", "grow"), el("span", "tz", sh ? t(sh.total === 1 ? "{n} session" : "{n} sessions", { n: fmtN(sh.total) }) : ""));
  box.append(head);
  slide(seg, "sessShape");
  if (!sh?.total) return box.append(el("div", "sess-none", ov.shape ? t("Nothing in this range.") : "…"));
  const label = (i) => {
    const lo = sh.edges[i], hi = sh.edges[i + 1];
    if (sessShapeBy === "autonomy" && lo === 0) return "<1";
    const r = hi == null ? lo + "+" : hi - 1 === lo ? String(lo) : `${lo}–${hi - 1}`;
    return sessShapeBy === "minutes" ? t("{r}m", { r }) : r;
  };
  const what = { messages: "{r} messages", minutes: "{r} minutes at work", autonomy: "{r} tool calls a prompt" }[sessShapeBy];
  const peak = Math.max(1, ...sh.counts);
  const bars = el("div", "sess-shape");
  sh.counts.forEach((c, i) => {
    const col = el("div", "col");
    const pct = Math.round(100 * c / sh.total);
    col.title = t(what, { r: label(i) }) + " · " + t(c === 1 ? "{n} session" : "{n} sessions", { n: fmtN(c) }) + ` (${pct}%)`;
    // a bar as tall as its share of the busiest bin, on a baseline; an
    // empty bin a stub, so no grey column reads as a bar of its own
    const plot = el("div", "plot");
    const fill = el("i", c ? "" : "none");
    fill.style.height = c ? `max(4px, calc((100% - 20px) * ${(c / peak).toFixed(3)}))` : "";
    plot.append(el("b", c ? "" : "zero", fmtN(c)), fill);
    // the busiest bin in full, when one is busiest
    col.classList.toggle("peak", c === peak && c > 0 && sh.counts.filter((n) => n === peak).length === 1);
    col.append(plot, el("span", "", label(i)));
    bars.append(col);
  });
  box.append(bars);
  // what the two axes are, said plainly
  const axis = { messages: "messages in a session, prompts and replies", minutes: "minutes a session was at work", autonomy: "tool calls the agent made on its own for each prompt" }[sessShapeBy];
  const foot = el("div", "sess-card-foot");
  foot.append(el("span", "", t("Across: {axis} · Height: sessions", { axis: t(axis) })));
  box.append(foot);
}

// the colours of the kinds of tool, as the calls are told apart
const TOOL_CATS = { Bash: "#e0823d", Edit: "#4f8cf0", Read: "#2fb087", Write: "#9b6cf0", Grep: "#e0608f", Glob: "#d4a72c", Task: "#35a9c7", Tool: "#7c83f2", Other: "#9aa0a8" };
const toolColor = (c) => TOOL_CATS[c] || TOOL_CATS.Other;
// a kind's name, as its tools are named, "Other" aside
const toolKind = (c) => c === "Other" ? t("Other") : c;
function toolDot(c) {
  const d = el("i", "dot");
  d.style.background = toolColor(c);
  return d;
}

// renderSessTools is what the sessions called their tools for: the most
// called, by kind, and week by week
function renderSessTools() {
  const box = $("#sessTools");
  const tu = sessOver?.tools;
  box.replaceChildren();
  box.classList.toggle("stale", sessOverAt !== sessOverQ());
  const head = sessCardHead("Tool use");
  head.append(el("span", "grow"));
  if (tu?.calls) head.append(el("span", "tz", t("{n} calls", { n: fmtN(tu.calls) }) + " · " + t(tu.sessions === 1 ? "{n} session" : "{n} sessions", { n: fmtN(tu.sessions) })));
  box.append(head);
  if (!tu?.calls) return box.append(el("div", "sess-none", tu ? t("No tool calls in this range.") : "…"));
  const cols = el("div", "sess-tools-cols");

  // the most called, each with its kind's dot, calls, sessions and share
  const top = el("div", "sess-bars");
  const peak = Math.max(1, ...tu.top.map((x) => x.calls));
  for (const x of tu.top) {
    const r = el("div", "sess-bar tool");
    r.title = `${x.name} · ${toolKind(x.category)}`;
    const n = el("span", "n");
    n.append(toolDot(x.category), el("span", "", x.name));
    const track = el("span", "track");
    const fill = el("i");
    fill.style.width = Math.max(1.5, 100 * x.calls / peak).toFixed(1) + "%";
    fill.style.background = toolColor(x.category);
    track.append(fill);
    r.append(n, track, el("span", "v", fmtN(x.calls)), el("span", "s", t(x.sessions === 1 ? "{n} session" : "{n} sessions", { n: fmtN(x.sessions) })),
      el("span", "p", Math.round(100 * x.calls / tu.calls) + "%"));
    top.append(r);
  }
  cols.append(top);

  const side = el("div", "sess-tools-side");
  // by kind: one bar of every call, and each kind's count
  const mix = el("div", "sess-mix");
  const kinds = el("div", "sess-kinds");
  for (const c of tu.categories) {
    const seg = el("i");
    seg.style.flexGrow = c.calls;
    seg.style.background = toolColor(c.name);
    seg.title = `${toolKind(c.name)} · ${t("{n} calls", { n: fmtN(c.calls) })} (${Math.round(100 * c.calls / tu.calls)}%)`;
    mix.append(seg);
    const k = el("span", "kind");
    k.title = seg.title;
    k.append(toolDot(c.name), el("span", "", toolKind(c.name)), el("b", "", fmtN(c.calls)));
    kinds.append(k);
  }
  side.append(mix, kinds);

  // week by week, the last sixteen, each kind stacked
  const weeks = tu.weeks.slice(-16);
  if (weeks.length > 1) {
    side.append(el("div", "sess-sub", t("By week")));
    const order = tu.categories.map((c) => c.name);
    const total = (w) => Object.values(w.calls).reduce((a, b) => a + b, 0);
    const wpeak = Math.max(1, ...weeks.map(total));
    const bars = el("div", "sess-weeks");
    for (const w of weeks) {
      const bar = el("div", "bar");
      const n = total(w);
      bar.title = t("week of {label}", { label: sessDay(sessDate(w.start)) }) + " · " + t("{n} calls", { n: fmtN(n) });
      const stack = el("div", "stack");
      stack.style.height = (n ? Math.max(2, 100 * n / wpeak) : 0).toFixed(1) + "%";
      for (const c of order) {
        if (!w.calls[c]) continue;
        const part = el("i");
        part.style.flexGrow = w.calls[c];
        part.style.background = toolColor(c);
        stack.append(part);
      }
      bar.append(stack);
      bars.append(bar);
    }
    const labels = el("div", "sess-weeks-labels");
    labels.append(el("span", "", sessDay(sessDate(weeks[0].start))), el("span", "", sessDay(sessDate(weeks[weeks.length - 1].start))));
    side.append(bars, labels);
  }
  cols.append(side);
  box.append(cols);
}

// renderSessSkills lists the skills the sessions called up, the most first
function renderSessSkills() {
  const box = $("#sessSkills");
  const su = sessOver?.skills;
  box.replaceChildren();
  box.classList.toggle("stale", sessOverAt !== sessOverQ());
  const head = sessCardHead("Top skills");
  head.append(el("span", "grow"));
  if (su?.calls) head.append(el("span", "tz", t("{n} calls", { n: fmtN(su.calls) }) + " · " + t(su.count === 1 ? "{n} skill" : "{n} skills", { n: fmtN(su.count) })));
  box.append(head);
  if (!su?.calls) return box.append(el("div", "sess-none", su ? t("No skill was called up in this range.") : "…"));
  const names = sessStats?.agents || {};
  const peak = Math.max(1, ...su.top.map((x) => x.calls));
  const body = el("div", "sess-skills");
  for (const x of su.top) {
    const r = el("div", "sess-skill");
    const line = el("div", "line");
    const track = el("span", "track");
    const fill = el("i");
    fill.style.width = Math.max(1.5, 100 * x.calls / peak).toFixed(1) + "%";
    track.append(fill);
    line.append(el("span", "n", x.name), track, el("span", "v", fmtN(x.calls)));
    const agents = Object.entries(x.agents || {}).sort((a, b) => b[1] - a[1]);
    const share = agents.map(([a, c]) => `${names[a] || a} ${Math.round(100 * c / x.calls)}%`).join(" · ");
    const where = (x.projects || []).map((p) => baseName(p.name)).join(", ");
    const sub = el("div", "sub", [t(x.sessions === 1 ? "{n} session" : "{n} sessions", { n: fmtN(x.sessions) }),
      x.last ? t("last {when}", { when: sessDay(sessDate(x.last)) }) : "", share, where].filter(Boolean).join(" · "));
    sub.title = (x.projects || []).map((p) => `${p.name} · ${t("{n} calls", { n: fmtN(p.calls) })}`).join("\n");
    r.append(line, sub);
    body.append(r);
  }
  if (su.count > su.top.length) body.append(el("div", "sess-more", t("+{n} more", { n: su.count - su.top.length })));
  box.append(body);
}

// sessBars is a card of folders or models as bars of the tokens each took;
// a click shows that one alone, and a click on it again every one
const SESS_BARS = 6;
function sessBars(box, title, items, cur, choose, name) {
  box.replaceChildren();
  box.append(sessCardHead(title));
  const body = el("div", "sess-bars");
  let shown = items.slice(0, SESS_BARS);
  const picked = cur && items.find((x) => x.v === cur);
  if (picked && !shown.includes(picked)) shown = [...shown.slice(0, SESS_BARS - 1), picked];
  const peak = Math.max(1, ...items.map((x) => x.n));
  for (const x of shown) {
    const r = el("button", "sess-bar" + (x.v === cur ? " on" : ""));
    r.type = "button";
    r.title = [name(x.v) !== x.v ? x.v : "", x.v === cur ? t("Click again to show every one") : t("Click to show only this")].filter(Boolean).join("\n");
    const track = el("span", "track");
    const fill = el("i");
    fill.style.width = Math.max(1.5, 100 * x.n / peak).toFixed(1) + "%";
    track.append(fill);
    const fc = x.cost ? fmtCost(x) : "";
    r.append(el("span", "n", name(x.v)), track, el("span", "v", fmtN(x.n)), el("span", "c", fc ? "≈" + fc : ""));
    r.onclick = () => choose(x.v === cur ? "" : x.v);
    body.append(r);
  }
  if (!items.length) body.append(el("div", "sess-none", t("Nothing in this range.")));
  if (items.length > shown.length) body.append(el("div", "sess-more", t("+{n} more", { n: items.length - shown.length })));
  box.append(body);
}

function sessionItem(s) {
  const key = sessKey(s);
  const item = el("div", "sess-item" + (sessOpen.has(key) ? " open" : ""));
  const r = el("div", "row sess");
  r.append(icon(s.icon || "generic"));
  const who = el("div", "who");
  who.append(el("div", "name", s.title || t("(no prompt)")));
  // where magpie's gateway sent its calls, the most first: a routing
  // group's member and the reasoning it was asked for
  const via = s.via?.length ? "→ " + [s.via[0].model, s.via[0].effort].filter(Boolean).join(" · ") + (s.via.length > 1 ? " +" + (s.via.length - 1) : "") : "";
  const sub = el("div", "sub", [s.cwd ? baseName(s.cwd) : "", s.models.slice(0, 2).map((m) => m.model).join(", ") + (s.models.length > 2 ? " +" + (s.models.length - 2) : "") + (via ? " " + via : ""), ago(s.last)].filter(Boolean).join(" · "));
  sub.title = [s.cwd, ...(s.via || []).map(viaText)].filter(Boolean).join("\n");
  who.append(sub);
  r.append(who);
  const num = el("div", "num");
  num.append(el("b", "", fmtN(sessTokens(s))), el("small", "", t("{a} in · {b} out", { a: fmtN(s.input), b: fmtN(s.output) }) + (s.cache_read ? " · " + t("{n} cached", { n: fmtN(s.cache_read) }) : "")));
  r.append(num);
  const sc = sessCost(s);
  const cost = el("div", "cost" + (sc === "—" ? " none" : ""), sc);
  if (sc === "—") cost.title = t("No known price for {models}", { models: s.models.map((m) => m.model).join(", ") || "—" });
  else if (s.unpriced) cost.title = t("Not counted: {models}, with no known price", { models: s.models.filter((m) => !m.priced).map((m) => m.model).join(", ") });
  r.append(cost);
  if (s.resume) {
    const res = el("button", "sess-resume", t("Resume"));
    res.title = t("Copy the command that resumes it: {cmd}", { cmd: s.resume });
    res.onclick = async (ev) => {
      ev.stopPropagation();
      await copy(s.resume, t("Resume command"));
      res.textContent = t("Copied");
      res.classList.add("done");
      clearTimeout(res.copiedT);
      res.copiedT = setTimeout(() => { res.textContent = t("Resume"); res.classList.remove("done"); }, 1400);
    };
    r.append(res);
    if (sessions?.terminal) {
      const term = el("button", "copy sess-term");
      term.title = t("Open in session terminal");
      term.append(svg("M3 4.5 6 7.5 3 10.5M7.5 11.5h5.5", 13, 1.6));
      term.onclick = (ev) => {
        ev.stopPropagation();
        api("sessions/terminal", { agent: s.agent, id: s.id }).then(() => status(t("Opening in session terminal"), "ok"), (e) => status(e.message, "err"));
      };
      r.append(term);
    }
  }
  r.onclick = () => {
    if (window.getSelection()?.toString()) return;
    if (sessOpen.has(key)) sessOpen.delete(key); else sessOpen.add(key);
    item.replaceWith(sessionItem(s));
  };
  item.append(r);
  if (sessOpen.has(key)) item.append(sessionDetail(s));
  return item;
}

function sessionDetail(s) {
  const d = el("div", "sess-detail");
  const line = (label, value, extra) => {
    const l = el("div", "sess-line");
    l.append(el("span", "k", label));
    const v = el("span", "v", value);
    l.append(v);
    if (extra) l.append(extra);
    d.append(l);
  };
  line(t("Time"), stamp(s.start) + " – " + stamp(s.last));
  if (s.cwd) line(t("Folder"), s.cwd);
  line(t("Session"), s.id, copyBtn(s.id, t("Session id")));
  if (s.resume) {
    const code = el("code", "", s.resume);
    const l = el("div", "sess-line");
    l.append(el("span", "k", t("Resume")), code, copyBtn(s.resume, t("Resume command")));
    d.append(l);
  }
  if (s.models.length) {
    const m = el("div", "sess-models");
    for (const x of s.models) {
      m.append(el("span", "model", x.model),
        el("span", "n", t("{a} in · {b} out", { a: fmtN(x.input), b: fmtN(x.output) }) + (x.cache_read ? " · " + t("{n} cached", { n: fmtN(x.cache_read) }) : "") + (x.cache_write ? " · " + t("{n} written", { n: fmtN(x.cache_write) }) : "")),
        el("span", "c" + (x.priced ? "" : " none"), x.priced ? "≈" + fmtCost({ cost: x.cost }) : "—"));
    }
    d.append(m);
  }
  // what the gateway sent the session's calls to, at what reasoning
  (s.via || []).forEach((v, i) => line(i ? "" : t("Routed"), viaText(v) + " · " + t("{n} tokens", { n: fmtN(v.tokens) })));
  if (s.path) line(t("File"), s.path);
  return d;
}

// one place a session's calls went through magpie, in words
function viaText(v) {
  return `${v.provider}/${v.model}` + (v.effort ? " · " + v.effort : "") + " · " + t("{n} calls", { n: v.calls });
}

$("#sessQ").oninput = (e) => { sessQuery = e.target.value; if (sessions) renderSessions(); };
$("#sessQ").onkeydown = (e) => { if (e.key === "Escape" && e.target.value) { e.stopPropagation(); e.target.value = ""; sessQuery = ""; if (sessions) renderSessions(); } };

// ---------- settings ----------
//
// Three choices (palette, language, what the tray icon opens) and the facts
// people come looking for:
// the version, where magpie keeps its files, the gateway's address.

const THEMES = [["system", "System"], ["light", "Light"], ["dark", "Dark"]];
const LOCALES = [["system", "System"], ["en", "English"], ["zh", "中文"]];
const TRAYS = [["panel", "Quick panel"], ["window", "Main window"]];
const CURRENCIES = [["usd", "$ USD"], ["cny", "¥ CNY"]];
// The text size is the windows' own zoom, as a browser's Ctrl/Cmd +: the
// page is laid out again in larger CSS pixels, so everything it measures is
// as at 100%, and magpie sizes the windows around it (textsize.go). None is
// under 100%: WebView2 and WebKitGTK zoom no smaller through Wails.
const TEXT_SIZES = [100, 110, 125, 150];
const textSizeKeys = () => /^Mac/.test(navigator.platform) ? "⌘+ ⌘− ⌘0" : "Ctrl+ Ctrl− Ctrl+0";
// --zoom is the text size for what is placed against the window's own
// drawing, which does not grow with the page: the Mac's traffic lights.
function applyZoom(n) {
  document.documentElement.style.setProperty("--zoom", web ? 1 : (n || 100) / 100);
}
let textSize = window.bootPrefs?.textSize || 100;
async function setTextSize(n) {
  if (web || n === textSize) return;
  textSize = n;
  applyZoom(n);
  try {
    prefs = await writingPrefs(api("settings/text-size", { size: n }));
    state.settings = prefs;
    if (view === "settings") renderSettings();
  } catch (e) {
    status(e.message, "err");
  }
}
// Ctrl/Cmd + = and − step through the sizes, 0 goes back to 100%, in the
// window and the panel alike; in a browser tab they are the browser's.
if (!web) addEventListener("keydown", (e) => {
  const mod = /^Mac/.test(navigator.platform) ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  if (!mod || e.altKey) return;
  const k = e.code === "NumpadAdd" || e.key === "=" || e.key === "+" ? 1
    : e.code === "NumpadSubtract" || e.key === "-" || e.key === "_" ? -1
    : e.code === "Digit0" || e.code === "Numpad0" || e.key === "0" ? 0 : null;
  if (k === null) return;
  e.preventDefault();
  const i = TEXT_SIZES.indexOf(textSize);
  const n = k === 0 ? 100 : TEXT_SIZES[Math.max(0, Math.min(TEXT_SIZES.length - 1, (i < 0 ? 0 : i) + k))];
  if (n === textSize) return;
  setTextSize(n);
  status(t("Text size {n}%", { n }));
}, true);

// applyPrefs paints and speaks as the saved settings say, costs at the
// exchange rate given (rate, /api/state's fx) or the settings' own. A
// ?theme= or ?locale= in the URL wins, so a forced look stays forced.
function applyPrefs(s, rate) {
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
      if (applyPrefs.ready) { tintPanel(450); tintTitleBar(460); }
    }
  }
  applyPrefs.ready = true;
  // this browser's choice from before it was a setting, carried over once
  let kept = null;
  try { kept = localStorage.getItem("magpie.quotaLeft"); localStorage.removeItem("magpie.quotaLeft"); } catch {}
  if (kept === "1" && !s.quotaLeft) { s.quotaLeft = true; setQuotaLeft(true); }
  if (quotaLeft !== !!s.quotaLeft) {
    quotaLeft = !!s.quotaLeft;
    if (applyPrefs.painted) renderQuotas();
  }
  // the rate comes in /api/settings' answer (s.fx) or, from /api/state,
  // beside the settings rather than in them (rate): a cost drawn at start,
  // before Settings is ever opened, needs it from there (#212: cny still
  // picked after a restart, yet every cost back in $, the rate being 0).
  // A rate of 0 is state's for usd, which never looks one up: kept out.
  const got = rate?.rate > 0 ? rate : s.fx?.rate > 0 ? s.fx : null;
  const moved = !!got && got.rate !== fx.rate;
  if (got) fx = got;
  if (currency !== (s.currency || "usd") || (moved && currency === "cny")) {
    currency = s.currency || "usd";
    if (applyPrefs.painted) renderCosts();
  }
  applyPrefs.painted = true;
  if (!prefsBusy && (s.textSize || 100) !== textSize) { textSize = s.textSize || 100; applyZoom(textSize); }
  const was = locale;
  setLocale(s.lang);
  if (was !== locale && mode === "window") queueMicrotask(() => slide($("#nav"), "nav"));
  return was !== locale;
}

// Omarchy's bar: magpie's icon there as a widget of its own (Settings → Bar
// icon), offered only in the app on Omarchy
let barIcon = null;
function renderBarIcon() {
  $("#barIconRow").hidden = !barIcon?.available;
  if (!barIcon?.available) return;
  $("#barIconSegs").replaceChildren(segs([["off", t("Off")], ["on", t("On")]], barIcon.on ? "on" : "off", (v) =>
    api("omarchy/widget", { on: v === "on" }).then((b) => { barIcon = b; renderBarIcon(); })
      .catch((e) => { status(t(e.message), "err"); renderBarIcon(); })));
}

async function loadSettings() {
  const since = prefsWrites;
  if (window.bootPrefs?.omarchy && !barIcon) api("omarchy/widget").then((b) => { barIcon = b; renderBarIcon(); }).catch(() => {});
  const s = await api("settings");
  if (!prefsSettled(since) && prefs) return; // the save draws the page when it's in
  prefs = s;
  renderSettings();
}

// Writes of the settings, counted as they start and end: a read of them
// (the state, the settings) that one overlapped is from before it, and is
// not to undo it (a theme picked while the window's state was being read
// went back to the old one when the read came in, the picker showing the
// new).
let prefsWrites = 0, prefsBusy = 0;
async function writingPrefs(p) {
  prefsWrites++;
  prefsBusy++;
  try { return await p; } finally { prefsBusy--; prefsWrites++; }
}
const prefsSettled = (since) => !prefsBusy && since === prefsWrites;

const GITHUB_SVG = '<svg viewBox="0 0 24 24" width="13" height="13" fill="currentColor" aria-hidden="true"><path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12"/></svg>';
const DISCORD_SVG = '<svg viewBox="0 0 24 24" width="13" height="13" fill="currentColor" aria-hidden="true"><path d="M20.317 4.3698a19.7913 19.7913 0 00-4.8851-1.5152.0741.0741 0 00-.0785.0371c-.211.3753-.4447.8648-.6083 1.2495-1.8447-.2762-3.68-.2762-5.4868 0-.1636-.3933-.4058-.8742-.6177-1.2495a.077.077 0 00-.0785-.037 19.7363 19.7363 0 00-4.8852 1.515.0699.0699 0 00-.0321.0277C.5334 9.0458-.319 13.5799.0992 18.0578a.0824.0824 0 00.0312.0561c2.0528 1.5076 4.0413 2.4228 5.9929 3.0294a.0777.0777 0 00.0842-.0276c.4616-.6304.8731-1.2952 1.226-1.9942a.076.076 0 00-.0416-.1057c-.6528-.2476-1.2743-.5495-1.8722-.8923a.077.077 0 01-.0076-.1277c.1258-.0943.2517-.1923.3718-.2914a.0743.0743 0 01.0776-.0105c3.9278 1.7933 8.18 1.7933 12.0614 0a.0739.0739 0 01.0785.0095c.1202.099.246.1981.3728.2924a.077.077 0 01-.0066.1276 12.2986 12.2986 0 01-1.873.8914.0766.0766 0 00-.0407.1067c.3604.698.7719 1.3628 1.225 1.9932a.076.076 0 00.0842.0286c1.961-.6067 3.9495-1.5219 6.0023-3.0294a.077.077 0 00.0313-.0552c.5004-5.177-.8382-9.6739-3.5485-13.6604a.061.061 0 00-.0312-.0286zM8.02 15.3312c-1.1825 0-2.1569-1.0857-2.1569-2.419 0-1.3332.9555-2.4189 2.157-2.4189 1.2108 0 2.1757 1.0952 2.1568 2.419 0 1.3332-.9555 2.4189-2.1569 2.4189zm7.9748 0c-1.1825 0-2.1569-1.0857-2.1569-2.419 0-1.3332.9554-2.4189 2.1569-2.4189 1.2108 0 2.1757 1.0952 2.1568 2.419 0 1.3332-.946 2.4189-2.1568 2.4189Z"/></svg>';

// The Settings page's warm-ups and check-in are one section whose heading
// is a tab per service (Codex, Claude Code, WorkBuddy), the card under it
// showing the picked one's rows. The tab is remembered; WorkBuddy's, there
// only while an account is signed in or the check-in is on, falls back to
// Codex's when it is not. A click on a tab leaves the page where it is, as
// every click does (see "where the reader is"): a shorter card under it at
// the page's end gets room kept at the view's foot.
const WARM_TABS = { codex: "codexWarmList", claude: "claudeWarmList", wb: "wbList" };
let warmTab = "codex";
try { const k = localStorage.getItem("magpie.warmTab"); if (k in WARM_TABS) warmTab = k; } catch {}
function setWarmTab(tab, remember) {
  if (remember) {
    warmTab = tab;
    try { localStorage.setItem("magpie.warmTab", tab); } catch {}
  }
  if (!(tab in WARM_TABS) || $("#warmTab-" + tab).hidden) tab = "codex";
  for (const [id, list] of Object.entries(WARM_TABS)) {
    const b = $("#warmTab-" + id), on = id === tab;
    b.classList.toggle("on", on);
    b.setAttribute("aria-selected", String(on));
    b.tabIndex = on ? 0 : -1;
    $("#" + list).hidden = !on;
    // a pill drawn while its card was hidden measured nothing: its thumb is
    // put under the option picked, still, once the card is shown
    if (on) for (const th of $("#" + list).querySelectorAll(".segs > .thumb")) {
      const opt = th.parentElement.querySelector(":scope > .on");
      if (!opt || parseFloat(th.style.width) === opt.offsetWidth) continue;
      th.classList.add("still");
      th.style.transform = `translateX(${opt.offsetLeft}px)`;
      th.style.width = opt.offsetWidth + "px";
      void th.offsetWidth;
      th.classList.remove("still");
    }
  }
}
{
  const tabs = $("#warmTabs");
  tabs.onclick = (e) => {
    const b = e.target.closest("button[data-warm]");
    if (b) setWarmTab(b.dataset.warm, true);
  };
  // the arrows, Home and End move along the tabs, as a tab list's do: the
  // tab reached is clicked, so the page is held as for a click
  tabs.onkeydown = (e) => {
    const shown = [...tabs.querySelectorAll("button[data-warm]:not([hidden])")];
    const i = shown.indexOf(document.activeElement);
    if (i < 0) return;
    const j = { ArrowLeft: i - 1, ArrowRight: i + 1, Home: 0, End: shown.length - 1 }[e.key];
    if (j === undefined) return;
    e.preventDefault();
    e.stopPropagation();
    const b = shown[(j + shown.length) % shown.length];
    b.focus({ preventScroll: true });
    b.click();
  };
}

function renderSettings() {
  const s = prefs;
  const keep = prefsKeep(s);
  prefsBase = keep;
  $("#themeSegs").replaceChildren(segs(THEMES.map(([id, name]) => [id, t(name)]), s.theme, (theme) => savePrefs({ ...keep, theme })));
  $("#langSegs").replaceChildren(segs(LOCALES.map(([id, name]) => [id, t(name)]), s.lang, (lang) => savePrefs({ ...keep, lang })));
  // a browser tab has its own zoom, and magpie leaves it to it
  $("#textSizeRow").hidden = web;
  $("#textSizeSub").textContent = t("Everything in magpie's windows, larger; {keys} too", { keys: textSizeKeys() });
  $("#textSizeSegs").replaceChildren(segs(TEXT_SIZES.map((n) => [n, n + "%"]), s.textSize || 100, (n) => setTextSize(n)));
  $("#traySegs").replaceChildren(segs(TRAYS.map(([id, name]) => [id, t(name)]), s.tray || "panel", (tray) => savePrefs({ ...keep, tray })));
  // the Dock is the Mac's; the tray and the login item the app's
  $("#dockRow").hidden = !document.body.classList.contains("mac");
  $("#traySegs").parentElement.hidden = $("#loginSegs").parentElement.hidden = web;
  $("#dockSegs").replaceChildren(segs([["off", t("Hide")], ["window", t("With window")], ["on", t("Show")]],
    s.dock ? "on" : s.dockWindow ? "window" : "off", (v) => savePrefs({ ...keep, dock: v === "on", dockWindow: v === "window" })));
  renderSessionTerminal(s, keep);
  renderBarIcon();
  // the system's record, set on its own, not with the other choices
  $("#loginSegs").replaceChildren(segs([["off", t("Off")], ["on", t("On")]], s.login ? "on" : "off", (v) =>
    writingPrefs(api("settings/login", { on: v === "on" })).then((ns) => { prefs = ns; renderSettings(); }).catch((e) => { status(t(e.message), "err"); renderSettings(); })));
  // Codex's and Claude Code's warm-ups and WorkBuddy's check-in, one tab
  // each over one card, the rows' names not saying the service again.
  // A ChatGPT account's next window started as soon as the last resets
  $("#warmSegs").replaceChildren(segs([["off", t("Off")], ["week", t("Weekly")], ["all", t("Weekly and 5-hour")]],
    s.codexWarmup || "off", (v) => savePrefs({ ...keep, codexWarmup: v === "off" ? "" : v })));
  $("#warmSub").textContent = t("One tiny request starts the next window at once")
    + (s.codexWarmed ? " · " + t("last started {when}", { when: syncWhen(s.codexWarmed) }) : "");
  // and a Claude account's, the request sent through Claude Code
  $("#claudeWarmSegs").replaceChildren(segs([["off", t("Off")], ["week", t("Weekly")], ["all", t("Weekly and 5-hour")]],
    s.claudeWarmup || "off", (v) => savePrefs({ ...keep, claudeWarmup: v === "off" ? "" : v })));
  $("#claudeWarmSub").textContent = t("One tiny request through Claude Code (Haiku) starts the next window at once")
    + (s.claudeWarmed ? " · " + t("last started {when}", { when: syncWhen(s.claudeWarmed) }) : "");
  // and the 5-hour windows started at a time of day, so they line up with it
  renderWarmAt($("#warmAtSegs"), $("#warmAtSub"), s.codexWarmAt, s.codexWarmup, "",
    (v) => savePrefs({ ...keep, codexWarmAt: v }));
  renderWarmAt($("#claudeWarmAtSegs"), $("#claudeWarmAtSub"), s.claudeWarmAt, s.claudeWarmup, t("Sent through Claude Code."),
    (v) => savePrefs({ ...keep, claudeWarmAt: v }));
  // WorkBuddy's daily check-in pressed for each account, its tab shown
  // while one is signed in
  $("#warmTab-wb").hidden = !s.workbuddy && !s.workbuddyCheckin;
  $("#warmTabs").setAttribute("aria-label", t("Warm-up and check-in"));
  setWarmTab(warmTab);
  $("#wbCheckinSegs").replaceChildren(segs([["off", t("Off")], ["on", t("On")]], s.workbuddyCheckin ? "on" : "off",
    (v) => savePrefs({ ...keep, workbuddyCheckin: v === "on" })));
  $("#wbCheckinSub").textContent = [t("Claims each signed-in China account's check-in credits once a day"),
    ...(s.workbuddyCheckins || []).map(wbCheckinLine)].filter(Boolean).join(" · ");
  $("#wbCheckinSub").title = t("As pressing 签到 in WorkBuddy does");
  renderTrayUsage(s, keep);
  renderProxy(s, keep);
  renderImages(s, keep);
  renderRedact(s, keep);
  renderLAN(s);
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
  // issues and the code are on GitHub, for those who'd rather not use Discord
  const repo = el("button", "github");
  repo.innerHTML = GITHUB_SVG;
  repo.append(el("span", "", "GitHub"));
  repo.title = "github.com/yetone/magpie";
  repo.onclick = () => api("open", { url: "https://github.com/yetone/magpie" }).catch(() => {});
  row(t("Community"), t("questions, ideas and feedback, on Discord or GitHub"), "", join, repo);
}

function renderSessionTerminal(s, keep) {
  const row = $("#sessionTerminalRow");
  row.hidden = !document.body.classList.contains("mac");
  if (row.hidden) return;
  const select = $("#sessionTerminalSelect");
  const apps = [...(s.terminalApps || [])];
  if (!apps.some((app) => app.id === "com.apple.Terminal")) apps.unshift({ id: "com.apple.Terminal", name: "Terminal" });
  const chosen = s.sessionTerminal || "system";
  // a default that is not a terminal opens Terminal, as the server does
  const defaultApp = apps.find((app) => app.id === s.terminalDefault) || apps.find((app) => app.id === "com.apple.Terminal");
  const options = [{ id: "system", name: t("System default ({name})", { name: defaultApp.name }) }];
  for (const app of apps) {
    if (app.id === defaultApp.id && app.id !== chosen) continue;
    options.push({ id: app.id, name: app.id === defaultApp.id ? t("{name} (fixed)", { name: app.name }) : app.name });
  }
  if (!options.some((app) => app.id === chosen)) {
    options.push({ id: chosen, name: t("Unavailable app ({id})", { id: chosen }) });
  }
  select.replaceChildren(...options.map((app) => {
    const option = el("option", "", app.name);
    option.value = app.id;
    return option;
  }));
  select.value = chosen;
  select.onchange = () => savePrefs({ ...keep, sessionTerminal: select.value === "system" ? "" : select.value });
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
  const parts = (ps) => ps.map((p) => t({ providers: "providers", settings: "settings", profiles: "profiles", agents: "agents' models", library: "library" }[p])).join(t(", "));

  // WebDAV or S3
  let status = t("Keeps providers, settings, profiles, agents' models and the library the same on every computer");
  const s3 = v.on && v.kind === "s3";
  if (v.on) {
    const hostOf = (u) => { try { return new URL(u).host; } catch { return u; } };
    // a bucket is named by its address, and the server it is on unless AWS
    const host = s3 ? [v.url, v.endpoint && hostOf(v.endpoint.includes("://") ? v.endpoint : "https://" + v.endpoint)].filter(Boolean).join(" · ") : hostOf(v.url);
    status = v.error ? t("Couldn't sync: {error}", { error: v.error })
      : v.last ? t("Synced {when} · {host}", { when: syncWhen(v.last), host }) : t("Not synced yet · {host}", { host });
  }
  const sub = row(t(s3 ? "S3 sync" : v.on ? "WebDAV sync" : "WebDAV or S3 sync"), status, ...(v.on
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

// davForm: the sync's settings, a WebDAV folder's or an S3 bucket's (#296).
// Both sets of fields are made and the kind picked shows one, so what was
// typed in the other is still there on going back.
function davForm(v) {
  const ed = el("div", "editor sync-form");
  let kind = v.kind === "s3" ? "s3" : "webdav";
  const saved = t("saved · type a new one to replace it");
  const dav = v.kind === "s3" ? {} : v, bk = v.kind === "s3" ? v : {};
  const url = input(dav.url || "", "https://dav.jianguoyun.com/dav/");
  const user = input(dav.user || "", t("user name"));
  const pass = input("", dav.passwordSet ? saved : t("password, or an app password"), "password");
  // s3://bucket/prefix, as the address is kept
  const [bucketName, prefixName] = (() => { const m = /^s3:\/\/([^/]*)\/?(.*)$/i.exec(bk.url || ""); return m ? [m[1], m[2]] : ["", ""]; })();
  const endpoint = input(bk.endpoint || "", "https://<account>.r2.cloudflarestorage.com");
  const bucket = input(bucketName, t("bucket name"));
  const prefix = input(prefixName, t("optional"));
  const region = input(bk.region || "", t("us-east-1, or auto for R2"));
  const keyID = input(bk.user || "", "AKIA…");
  const secret = input("", bk.passwordSet ? saved : t("secret access key"), "password");
  const [pathL, pathStyle] = tick(t("Path-style: the bucket in the path, not the host name"), !!bk.pathStyle);
  const phrase = input("", v.passphraseSet ? saved : t("the same on every computer"), "password");
  const [keysL, keys] = tick(t("Providers' API keys"), v.keys !== false);
  const [agentsL, agents] = tick(t("Agents' models"), v.agents !== false);
  const [libL, lib] = tick(t("Library: instructions, MCP servers and skills"), v.library !== false);
  const what = el("div", "stack");
  what.append(keysL, agentsL, libL);
  const davFields = [...field(t("Address"), url, t("A folder named magpie is made in it.")),
    ...field(t("User"), user),
    ...field(t("Password"), pass)];
  const s3Fields = [...field(t("Endpoint"), endpoint, t("Empty for AWS S3; for R2, B2, MinIO and the like, their S3 API address.")),
    ...field(t("Bucket"), bucket),
    ...field(t("Prefix"), prefix, t("The backup goes in a folder named magpie under it.")),
    ...field(t("Region"), region),
    ...field(t("Access key"), keyID),
    ...field(t("Secret"), secret),
    ...field("", pathL, t("MinIO and most NAS servers need it."))];
  const show = () => {
    for (const x of davFields) x.hidden = kind !== "webdav";
    for (const x of s3Fields) x.hidden = kind !== "s3";
  };
  ed.append(...field(t("Sync to"), segs([["webdav", "WebDAV"], ["s3", "S3"]], kind, (k) => { kind = k; show(); })),
    ...davFields, ...s3Fields,
    ...field(t("Passphrase"), phrase, t("The file is sealed with it on this computer; the server only ever sees it sealed. Keep it: without it the file can't be opened.")),
    ...field(t("Also sync"), what));
  show();
  const save = el("button", "text primary", t(v.on ? "Save" : "Turn on"));
  const off = v.on ? el("button", "text danger", t("Turn off")) : el("span");
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", off, el("span", "grow"), cancel, save);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  off.onclick = async () => { syncOpen = ""; renderSync(await api("davsync/off", {}).catch(() => null) || undefined); };
  save.onclick = async () => {
    const b = bucket.value.trim(), p = prefix.value.trim().replace(/^\/+|\/+$/g, "");
    if (kind === "s3" && !b) return say(t("Name the bucket"));
    if (!v.passphraseSet && !phrase.value) return say(t("Pick a passphrase: the file is sealed with it"));
    save.classList.add("busy");
    const where = kind === "s3"
      ? { url: "s3://" + b + (p ? "/" + p : ""), user: keyID.value.trim(), password: secret.value, endpoint: endpoint.value.trim(), region: region.value.trim(), pathStyle: pathStyle.checked }
      : { url: url.value.trim(), user: user.value.trim(), password: pass.value };
    try {
      const r = await api("davsync/save", { ...where, passphrase: phrase.value, keys: keys.checked, agents: agents.checked, library: lib.checked });
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
  const [libL, lib] = tick(t("With the library: instructions, MCP servers and skills"), true);
  const what = el("div", "stack");
  what.append(keysL, libL);
  ed.append(...field(t("Passphrase"), p1, t("Needed to open the file. Subscriptions aren't in it: sign in to them on the other computer.")),
    ...field("", p2), ...field("", what));
  const go = el("button", "text primary", t("Export"));
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", el("span", "grow"), cancel, go);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  go.onclick = async () => {
    if (!p1.value) return say(t("Pick a passphrase: the file is sealed with it"));
    if (p1.value !== p2.value) return say(t("The two passphrases differ"));
    go.classList.add("busy");
    try {
      const r = await api("backup/export", { pass: p1.value, keys: keys.checked, library: lib.checked });
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
  const [libL, lib] = tick(t("Bring in the library too: instructions, MCP servers and skills"), true);
  const what = el("div", "stack");
  what.append(agentsL, libL);
  ed.append(...field(t("File"), pf), ...field(t("Passphrase"), pass), ...field("", what, t("Providers with the same id are replaced; one that came without a key keeps the key it has here. The library replaces the one here, which is kept with its backups.")));
  const go = el("button", "text primary", t("Import"));
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", el("span", "grow"), cancel, go);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  go.onclick = async () => {
    if (!data) return say(t("Choose a file first"));
    go.classList.add("busy");
    try {
      const r = await api("backup/import", { data, pass: pass.value, agents: agents.checked, library: lib.checked });
      const lines = [t("Providers: {added} added, {replaced} replaced; {profiles} profiles; {agents} agent settings changed", { added: r.Added, replaced: r.Replaced, profiles: r.Profiles, agents: r.Agents })];
      if (r.NeedKey?.length) lines.push(t("Needs a key: {names}", { names: r.NeedKey.join(", ") }));
      if (r.Library) lines.push(t("Library brought in and written into the agents"));
      for (const p of r.LibraryProblems || []) lines.push(t("{agent} couldn't get {what}: {error}", { agent: p.agent, what: p.what, error: p.error }));
      ed.replaceChildren(...lines.map((l) => el("div", "done", l)));
      refreshAfterSync();
    } catch (e) {
      go.classList.remove("busy");
      say(e.message);
    }
  };
  return ed;
}

// trayCardID names a Usage page card as settings.TrayUsage does: its
// provider, and the account when there is one.
const trayCardID = (q) => q.user ? q.provider + "|" + q.user : q.provider;
// IN_USE ends the id of a card that follows the subscription's account in
// use, the one the gateway goes to first, rather than naming one
const IN_USE = "|*";

// renderTrayUsage: the subscriptions and plans whose windows show beside
// the tray icon, side by side. The cards are the Usage page's, asked for
// when the menu opens; any number are ticked in it.
function renderTrayUsage(s, keep) {
  $("#quotaLeftSegs").replaceChildren(segs([[false, t("Used")], [true, t("Left")]], !!s.quotaLeft,
    (on) => { if (on !== quotaLeft) setQuotaLeft(on); }));
  $("#currencySegs").replaceChildren(segs(CURRENCIES.map(([id, name]) => [id, t(name)]), s.currency || "usd", (v) => savePrefs({ ...keep, currency: v })));
  // the agents' lists name a model with its provider's after it, or alone
  // (#335): set on its own, so the agents are told
  $("#plainNamesSegs").replaceChildren(segs([["off", t("Off")], ["on", t("On")]], s.plainNames ? "off" : "on", (v) =>
    writingPrefs(api("settings/plain-names", { on: v === "off" })).then((ns) => { prefs = ns; renderSettings(); }).catch((e) => { status(t(e.message), "err"); renderSettings(); })));
  const rate = s.fx?.rate;
  const currencySub = $("#currencySub");
  currencySub.textContent = t("What a cost — the Usage page's, the tray panel's, the TUI's and the CLI's — is shown as; a vendor's own balance, already in its own currency, is never converted");
  currencySub.title = rate ? t("1 USD = {rate} CNY{when}", { rate: rate.toFixed(2), when: s.fx.at ? " · " + (s.fx.stale ? t("last fetched {when}", { when: syncWhen(s.fx.at) }) : t("fetched {when}", { when: syncWhen(s.fx.at) })) : "" }) : "";
  $("#trayUsageRow").hidden = web;
  if (web) return;
  const mac = document.body.classList.contains("mac");
  $("#trayUsageSub").textContent = mac ? t("Show your subscriptions' use beside magpie's icon in the menu bar, side by side, refreshed every few minutes")
    : t("Show your subscriptions' use when pointing at magpie's tray icon, refreshed every few minutes");
  const ids = s.trayUsages || (s.trayUsage ? [s.trayUsage] : []);
  const pill = el("button", "proto pick" + (ids.length ? " set" : ""));
  pill.type = "button";
  const paint = () => {
    const card = (quotas || []).find((q) => trayCardID(q) === ids[0] || q.provider + IN_USE === ids[0]);
    pill.replaceChildren(el("span", "", !ids.length ? t("Off") : ids.length > 1 ? t("{n} subscriptions", { n: ids.length })
      : card ? card.name : ids[0].split("|")[0]), svg(CHEV, 11, 1.6));
  };
  paint();
  if (ids.length === 1 && !quotas) loadQuotas().then(paint);
  pill.onclick = async (e) => {
    e.stopPropagation();
    if (pill.classList.contains("open")) return closeProtoMenu();
    if (!quotas) { pill.classList.add("busy"); await loadQuotas(); pill.classList.remove("busy"); paint(); }
    const cards = (quotas || []).filter((q) => !q.error && (q.windows?.length || q.balance));
    const opts = [{ v: "", name: "Off", note: "" }];
    for (const q of cards) {
      // a subscription with several accounts: the one in use, whichever
      // it is now, before each by name
      if (q.user && !opts.some((o) => o.v === q.provider + IN_USE) && cards.filter((c) => c.provider === q.provider).length > 1)
        opts.push({ v: q.provider + IN_USE, name: q.name, note: "Account in use" });
      opts.push({ v: trayCardID(q), name: q.name, note: [q.plan, q.user].filter(Boolean).join(" · ") });
    }
    // one ticked that isn't there now (signed out, or not answering) stays
    // to be unticked
    for (const id of ids) if (!opts.some((o) => o.v === id)) opts.push({ v: id, name: id.split("|")[0], note: id.endsWith(IN_USE) ? "Account in use" : id.split("|")[1] || "" });
    if (!cards.length) opts.push({ v: "\x00", name: "No subscriptions yet", note: "Sign in to one, or add a plan's key, and it shows on the Usage page" });
    openProtoMenu(pill, opts, ids, (trayUsages) => savePrefs({ ...keep, trayUsages }), "Shown beside the icon");
  };
  $("#trayUsagePick").replaceChildren(pill);
  // how often it is asked for again, and whether it reads as used or left —
  // the Usage page's choice too (#122)
  $("#trayEveryRow").hidden = !ids.length;
  $("#trayEverySegs").replaceChildren(segs(TRAY_EVERY.map((m) => [m, t("{n} min", { n: m })]), s.trayUsageEvery || 3,
    (trayUsageEvery) => savePrefs({ ...keep, trayUsageEvery })));
}
const TRAY_EVERY = [1, 3, 5, 10, 30];

// renderProxy: magpie's own requests to vendors, and its update checks, follow the system proxy on
// their own; this row says which one, and lets it be turned off or set.
let proxyCustom = false; // Custom picked, nothing typed yet
function renderProxy(s, keep) {
  const cur = !s.proxy ? "auto" : s.proxy === "direct" ? "off" : "custom";
  const mode = proxyCustom ? "custom" : cur;
  const sub = $("#proxySub");
  sub.textContent = {
    settings: t("Requests to vendors and update checks go through {proxy}", { proxy: s.proxyNow }),
    system: t("Following the system proxy, {proxy}", { proxy: s.proxyNow }),
    environment: t("Following HTTPS_PROXY, {proxy}", { proxy: s.proxyNow }),
    off: t("Off: requests to vendors and update checks go direct"),
    none: t("No system proxy found; requests to vendors and update checks go direct"),
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

// renderWarmAt draws a daily warm-up's control: Off, or a time of day in
// a time field, saved as it is changed; On picks 06:00 to begin with, and
// the field is there only while it is on. via says how the request goes.
function renderWarmAt(box, sub, at, onReset, via, save) {
  // the purpose on the line; the fine print, too long for it, in the
  // title: what is left be, and how it goes with the warm-up on reset
  const what = t("Starts each account's 5-hour window at this time every day");
  sub.textContent = what;
  sub.title = [what, t("06:00 gives three by 21:00."), via, t("One tiny request, sent only to an account whose 5-hour window isn't running then."),
    t("A computer asleep then sends it on waking, up to an hour late; later than that, the day is left be."),
    onReset === "all" ? t("With Weekly and 5-hour on, a window that would still be running then isn't started on its reset: the windows follow one another from this time.") : ""].filter(Boolean).join("\n");
  box.replaceChildren();
  if (at) {
    const i = input(at, "06:00", "time");
    i.className = "at";
    i.setAttribute("aria-label", t("Time of day"));
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") i.blur(); };
    i.onchange = () => { if (i.value && i.value !== at) save(i.value); };
    box.append(i);
  }
  box.append(segs([["off", t("Off")], ["on", t("On")]], at ? "on" : "off", (v) => save(v === "on" ? at || "06:00" : "")));
}

// renderImages: the model that describes images to a model that can't see
// them — the one magpie picks, one named, or none, and then such an image
// is turned away.
function renderImages(s, keep) {
  const box = $("#imagesList");
  box.replaceChildren();
  const models = s.visionModels || [];
  const named = (id) => {
    const m = models.find((x) => x.id === id);
    return m ? `${m.name || m.id} · ${m.providerName}` : id;
  };
  const r = el("div", "row pref");
  const who = el("div", "who");
  const v = s.vision || "";
  who.append(el("div", "name", t("Image recognition")), el("div", "sub",
    v === "off" ? t("A model that can't see images is sent none: a request with one in its latest message is turned away")
    : t("When the model in use can't see images, this one describes them to it, once for each image")));
  const b = el("button", "rt-cond on");
  const icOf = (id) => models.find((x) => x.id === id)?.icon;
  if (v === "off") b.append(el("span", "", t("Off")));
  else if (v) b.append(icon(icOf(v) || "generic"), el("span", "", named(v)));
  else {
    if (s.visionAuto) b.append(icon(icOf(s.visionAuto) || "generic"));
    b.append(el("span", "", s.visionAuto ? t("Automatic") + " · " + named(s.visionAuto) : t("Automatic") + " · " + t("no model that sees")));
  }
  // a routing group (no provider of its own) goes with the others, as in
  // an agent's picker, not in a group of its own with its own rail button
  const opt = (x) => ({ value: x.id, label: x.name || x.id, note: x.providerName, icon: x.icon, group: x.provider ? x.providerName : ROUTING_GROUPS, ref: x.id });
  b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "vision", label: "model", value: v, options: [
    { value: "", label: t("Automatic"), note: s.visionAuto ? named(s.visionAuto) : t("no model that sees"), reset: true },
    { value: "off", label: t("Off"), note: t("images turned away"), reset: true },
    ...models.map(opt)],
  onPick: (id) => { if (id !== v) savePrefs({ ...keep, vision: id }); } }, b, ev);
  const val = el("div", "val");
  val.append(b);
  r.append(who, val);
  box.append(r);
  renderImageGen(s, keep, box);
}

// renderImageGen: the model magpie's generate_image tool draws with — the
// one magpie picks, one named, or none — and where agents are given the tool.
function renderImageGen(s, keep, box) {
  const models = s.imageGenModels || [];
  const named = (id) => {
    const m = models.find((x) => x.id === id);
    return m ? `${m.name || m.id} · ${m.providerName}` : id;
  };
  const icOf = (id) => models.find((x) => x.id === id)?.icon;
  const v = s.imageGen || "";
  const r = el("div", "row pref");
  const who = el("div", "who");
  const sub = el("div", "sub",
    v === "off" ? t("Agents given Magpie Image can't generate images: the tool says it is off")
    : t("The model Magpie Image draws with. Give an agent the tool from Library → MCP servers → Discover → Magpie Image; images are saved in its project"));
  who.append(el("div", "name", t("Image generation")), sub);
  const b = el("button", "rt-cond on");
  if (v === "off") b.append(el("span", "", t("Off")));
  else if (v) b.append(icon(icOf(v) || "generic"), el("span", "", named(v)));
  else {
    if (s.imageGenAuto) b.append(icon(icOf(s.imageGenAuto) || "generic"));
    b.append(el("span", "", s.imageGenAuto ? t("Automatic") + " · " + named(s.imageGenAuto) : t("Automatic") + " · " + t("no model that draws")));
  }
  // a routing group (no provider of its own) goes with the others, as in
  // an agent's picker, not in a group of its own with its own rail button
  const opt = (x) => ({ value: x.id, label: x.name || x.id, note: x.providerName, icon: x.icon, group: x.provider ? x.providerName : ROUTING_GROUPS, ref: x.id });
  b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "imageGen", label: "model", value: v, options: [
    { value: "", label: t("Automatic"), note: s.imageGenAuto ? named(s.imageGenAuto) : t("no model that draws"), reset: true },
    { value: "off", label: t("Off"), note: t("no images generated"), reset: true },
    ...models.map(opt)],
  onPick: (id) => { if (id !== v) savePrefs({ ...keep, imageGen: id }); } }, b, ev);
  const val = el("div", "val");
  val.append(b);
  r.append(who, val);
  box.append(r);
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
  renderRedactRules(s, row);
  row(t("Count me as a user"), t("Once a day, a random id for this computer with magpie's version and system — nothing you use magpie for"),
    onOff(!s.noStats, (on) => savePrefs({ ...keep, noStats: !on })));
}

// renderRedactRules: the user's own rules for secrets magpie's don't know, a
// gateway's oc_sk_… key say (#195) — one row each, and a row to add one by a
// prefix or a regular expression. They are set on their own, all of them each
// time, so one magpie can't use (a pattern that doesn't compile) is said in
// the row, what was typed kept, and the rest stay as they were.
let ruleDraft = { kind: "", by: "prefix", match: "", err: "" };
function renderRedactRules(s, row) {
  const rules = s.redactRules || [];
  const set = (next, done) => writingPrefs(api("settings/redact-rules", { rules: next }))
    .then((ns) => { prefs = ns; ruleDraft.err = ""; done?.(); renderSettings(); status(t("Saved"), "ok", 1500); })
    .catch((e) => { ruleDraft.err = e.message; status(e.message, "err"); renderSettings(); });
  const d = ruleDraft;
  const kind = input(d.kind, "API_KEY");
  const match = input(d.match, d.by === "prefix" ? "oc_sk_" : "oc_sk_[A-Za-z0-9]{20,}");
  kind.className = "words rule-kind";
  match.className = "words rule-match";
  const by = segs([["prefix", t("Prefix")], ["regex", t("Regex")]], d.by, (v) => { d.by = v; match.placeholder = v === "prefix" ? "oc_sk_" : "oc_sk_[A-Za-z0-9]{20,}"; });
  const add = el("button", "text", t("Add"));
  add.onclick = () => {
    d.kind = kind.value; d.match = match.value.trim();
    if (!d.match) return match.focus();
    const r = { kind: d.kind.trim(), [d.by === "prefix" ? "prefix" : "regex"]: d.match };
    d.err = "";
    set([...rules, r], () => { ruleDraft = { kind: "", by: d.by, match: "", err: "" }; });
  };
  for (const i of [kind, match]) {
    i.oninput = () => { d.kind = kind.value; d.match = match.value; };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") add.onclick(); };
  }
  row(t("Masking rules"), d.err || t("Secrets magpie doesn't know, such as a gateway's own keys: what they start with, or a regular expression. Masked while Mask secrets is on"),
    kind, by, match, add);
  // its words above in full, the fields on a line of their own under them
  const head = $("#redactList").lastElementChild;
  head.classList.add("rule-row");
  head.querySelector(".val").classList.add("rule-add");
  if (d.err) head.querySelector(".sub").classList.add("err");
  // and the rules under it, each by the name its placeholders have
  rules.forEach((r, n) => {
    const x = el("button", "text", t("Remove"));
    x.onclick = () => set(rules.filter((_, i) => i !== n));
    row(r.kind, r.prefix ? t("Starts with {p}", { p: r.prefix }) : t("Matches {re}", { re: r.regex }), x);
  });
}

// renderLAN: the gateway shared on the local network, for agents on other
// machines — the addresses they use it at and the key they must send.
let lanSelectedURL = "", lanProtocol = "openai";
function renderLAN(s) {
  const box = $("#lanList");
  box.replaceChildren();
  const row = (name, sub, value, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name));
    if (sub) who.append(el("div", "sub", sub));
    const val = el("div", "val");
    if (value) val.append(el("code", "", value));
    val.append(...tools);
    r.append(who, val);
    box.append(r);
    return r;
  };
  const set = (body) => writingPrefs(api("settings/lan", body)).then((ns) => { prefs = ns; renderSettings(); })
    .catch((e) => { status(t(e.message), "err"); renderSettings(); });
  row(t("Share on local network"), t("Agents on other computers on this network can use magpie’s models, with the API key below"), "",
    segs([["off", t("Off")], ["on", t("On")]], s.lan ? "on" : "off", (v) => set({ on: v === "on" })));
  if (!s.lan) return;
  const urls = s.lanURLs || [];
  if (!urls.length) row(t("Address"), t("This computer has no local network address right now"), "");
  else {
    if (!urls.includes(lanSelectedURL)) lanSelectedURL = urls[0];
    const controls = el("div", "lan-address-controls");
    const address = urls.length === 1 ? el("code", "lan-address-text") : el("button", "proto pick lan-interface");
    if (urls.length > 1) {
      address.type = "button";
      address.setAttribute("aria-label", t("Address"));
      address.onclick = (e) => {
        e.stopPropagation();
        if (address.classList.contains("open")) return closeProtoMenu();
        const suffix = lanProtocol === "openai" ? "/v1" : "";
        openProtoMenu(address, urls.map((u) => ({ v: u, name: u + suffix, note: "" })), lanSelectedURL,
          (v) => { lanSelectedURL = v; update(); }, "Address", "lan-address-menu");
      };
    }
    const copyControl = el("span", "lan-copy");
    const update = () => {
      const label = lanProtocol === "openai" ? "OpenAI" : "Anthropic";
      const url = lanSelectedURL + (lanProtocol === "openai" ? "/v1" : "");
      if (urls.length === 1) address.textContent = url;
      else address.replaceChildren(el("span", "lan-url", url), svg(CHEV, 11, 1.6));
      address.title = url;
      const what = t("{label} address", { label });
      const button = copyBtn(url, what, t("Copied {label} address", { label }));
      button.setAttribute("aria-label", t("Copy") + " " + what);
      copyControl.replaceChildren(button);
    };
    controls.append(segs([["openai", "OpenAI"], ["anthropic", "Anthropic"]], lanProtocol,
      (v) => { lanProtocol = v; update(); }), address, copyControl);
    row(t("Address"), "", "", controls).classList.add("lan-address-row");
    update();
  }
  const again = el("button", "text", t("New key"));
  again.onclick = () => set({ on: true, newKey: true });
  row(t("API key"), t("Other computers send it as their API key; a new one stops the old from working"),
    s.lanKey.slice(0, 14) + "…", copyBtn(s.lanKey, t("API key")), again);
}

// renderUpdate fills in the version row: whether a newer magpie is out.
// The app checks and downloads on its own, so usually the row just offers
// the restart; a check can also be asked for. That check leaves the button
// where it is, dimmed, and a second click does nothing. A read still out
// from before the answer is not drawn over it. A row drawn again reads the
// current state, and keeps asking while a check or a download is under way.
let updateBusy = false;
const updateSeq = new WeakMap();

async function renderUpdate(r, u) {
  const seq = updateSeq.get(r) || 0;
  if (u == null) {
    u = await api("update").catch(() => null);
    if (!u || !r.isConnected || (updateSeq.get(r) || 0) !== seq) return;
  } else if (!r.isConnected) return;
  const who = r.querySelector(".who"), val = r.querySelector(".val");
  const sub = who.querySelector(".sub") || who.appendChild(el("div", "sub"));
  sub.title = "";
  for (const b of val.querySelectorAll("button")) b.remove();
  const btn = (label, fn, dim) => {
    const b = el("button", "text" + (dim ? " busy" : ""), label);
    if (dim) b.disabled = true;
    b.onclick = fn;
    val.append(b);
    return b;
  };
  // one check at a time. The button stays; the click only dims it until the
  // answer, and a second click is ignored rather than drawn as "checking".
  const check = async () => {
    if (updateBusy) return;
    updateBusy = true;
    const mine = (updateSeq.get(r) || 0) + 1;
    updateSeq.set(r, mine);
    sub.textContent = t("Checking for updates…");
    for (const b of val.querySelectorAll("button")) {
      b.disabled = true;
      b.classList.add("busy");
    }
    let next;
    try { next = await api("update/check", {}); }
    catch (e) { next = { state: "error", error: e.message }; }
    updateBusy = false;
    if (!r.isConnected || (updateSeq.get(r) || 0) !== mine) return;
    updateSeq.set(r, mine + 1); // the answer stands; a read still out is older
    renderUpdate(r, next);
  };
  const again = (ms) => {
    const seen = updateSeq.get(r) || 0;
    setTimeout(() => { if (r.isConnected && (updateSeq.get(r) || 0) === seen) renderUpdate(r); }, ms);
  };
  switch (u.state) {
    case "ready":
      sub.textContent = t("{v} is downloaded", { v: u.latest }) + (u.error ? " · " + u.error : "");
      // back with an answer only when it didn't restart
      btn(t("Restart to update"), async () => {
        const a = await api("update/install", installFrom()).catch(() => ({ state: "error" }));
        if (a) return renderUpdate(r, a.current ? a : undefined);
        sub.textContent = t("Restarting…");
        backAsNew(u.current);
      });
      break;
    case "available":
      sub.textContent = t("{v} is out", { v: u.latest });
      if (u.stuck) sub.textContent += " · " + updateStuck(u);
      btn(t("Download"), () => (web && u.url ? window.open(u.url, "_blank", "noopener") : api("update/install", {})));
      break;
    case "downloading":
      sub.textContent = t("Downloading {v}…", { v: u.latest });
      if (u.total) sub.textContent += " " + Math.floor((u.done / u.total) * 100) + "% · " + t("{done} of {total} MB", { done: (u.done / 1e6).toFixed(1), total: (u.total / 1e6).toFixed(1) });
      else if (u.done) sub.textContent += " " + t("{done} MB", { done: (u.done / 1e6).toFixed(1) });
      again(700);
      break;
    case "checking":
      sub.textContent = t("Checking for updates…");
      btn(t("Check"), check, true);
      again(1000);
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

// wbCheckinLine is how an account's last WorkBuddy check-in went: today's
// (a Beijing day) with the credits and the streak, an earlier one by its day.
function wbCheckinLine(r) {
  const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
  if (r.day !== today) {
    return r.outcome === "claimed" || r.outcome === "done" ? t("{user} checked in {day}", { user: r.user, day: r.day }) : "";
  }
  switch (r.outcome) {
    case "claimed":
    case "done":
      return t("{user} checked in today", { user: r.user }) + (r.credit ? " +" + r.credit : "")
        + (r.streak ? ", " + t("{n}-day streak", { n: r.streak }) : "");
    case "ineligible":
      return t("{user} is not eligible", { user: r.user });
    case "inactive":
      return t("{user}: no check-in event now", { user: r.user });
    default:
      return t("{user} couldn't check in, tried again later", { user: r.user });
  }
}

// prefsKeep is what the settings page sends of s, all of it each time.
function prefsKeep(s) {
  return { theme: s.theme, lang: s.lang, tray: s.tray, dock: !!s.dock, dockWindow: !!s.dockWindow, proxy: s.proxy || "",
    sessionTerminal: s.sessionTerminal || "",
    trayUsages: s.trayUsages || [],
    redact: !!s.redact, redactPersonal: !!s.redactPersonal, redactWords: s.redactWords || [], codexWarmup: s.codexWarmup || "",
    claudeWarmup: s.claudeWarmup || "", codexWarmAt: s.codexWarmAt || "", claudeWarmAt: s.claudeWarmAt || "", workbuddyCheckin: !!s.workbuddyCheckin, noStats: !!s.noStats,
    trayUsage: s.trayUsage || "", trayUsageEvery: s.trayUsageEvery || 3, vision: s.vision || "", imageGen: s.imageGen || "", currency: s.currency || "usd" };
}

// savePrefs sends what the page was drawn with (prefsBase) and the choice
// made on it. The saves go one after another, each with the choices before
// it: two made quickly (a theme, then a language) each sent the page as it
// was drawn, the second undoing the first, and their answers could come
// back in either order. The page is painted and drawn again once the last
// is in.
let prefsBase = {}, prefsQueue = Promise.resolve(), prefsQueued = 0;
function savePrefs(body) {
  const change = Object.fromEntries(Object.entries(body).filter(([k, v]) => JSON.stringify(v) !== JSON.stringify(prefsBase[k])));
  prefsQueued++;
  // a save waiting its turn counts as under way
  prefsQueue = writingPrefs(prefsQueue.catch(() => {}).then(async () => {
    let failed = false;
    try {
      prefs = await api("settings", { ...prefsKeep(prefs), ...change });
      if (state) state.settings = prefs;
    } catch (e) {
      failed = true;
      status(e.message, "err");
    }
    if (--prefsQueued) return;
    // what is saved, a choice that failed put back
    const spoke = applyPrefs(prefs);
    renderSettings();
    if (spoke) { renderAgents(); providers = null; usage = null; }
    if (!failed) status(t("Saved"), "ok", 1500);
  }));
  return prefsQueue;
}

// ---------- header / footer ----------

// ---------- where the reader is ----------
// Two rules keep the page under the reader, held here for every view so no
// part of the app has to remember them:
//
// - A view moves only for the reader: the wheel or trackpad, a touch, the
//   keys that scroll, Tab, a drag. Anything else that scrolls it is put
//   back before it's painted: a part redrawn and measured while briefly
//   shorter pulls the page up to what was left of it (WebKit has no scroll
//   anchoring), WebKit scrolls a field it focuses to the middle of the view,
//   and code sets scrollTop.
// - What the reader clicks stays where it is on the screen while what the
//   click does redraws around it — the part above it grown or shrunk, the
//   control itself drawn again, a load come in — till the reader scrolls or
//   clicks again, or it has settled. A click is never a scroll: a tab, a
//   filter, a day, a toggle leaves the page where it was. When what it does
//   leaves the page shorter under it (a list emptied), the view keeps room
//   at its foot for it to stay, room that goes as the reader scrolls back.
//   A control under what it unrolls (data-unrolls: "Show 7 more") is the
//   exception: it goes down with what it opens, and what's held is the
//   part it is in, so the rows open downwards rather than the page riding
//   up past them. A view at its top stays at its top: what comes in above
//   the control there moves it down instead of scrolling itself out of sight.
//
// Code moves a view only in answer to a click that asks to go somewhere, and
// shows that with the reader's event: scrollOnPurpose(e). Called without one
// (from a load, a timer, a helper that other clicks share) it is refused,
// and whatever scroll follows is put back.
let purposeUntil = 0, held = null;
const readerScrolls = (ms) => { purposeUntil = Math.max(purposeUntil, performance.now() + ms); held = null; };
function scrollOnPurpose(e, ms = 1000) {
  if (!e?.isTrusted || performance.now() - e.timeStamp > 1000) {
    console.warn("magpie: a scroll not asked for by the reader was refused");
    return false;
  }
  readerScrolls(ms);
  return true;
}
const SCROLL_KEYS = new Set(["PageUp", "PageDown", "Home", "End", "ArrowUp", "ArrowDown", " "]);
addEventListener("wheel", () => readerScrolls(250), { capture: true, passive: true });
addEventListener("touchmove", () => readerScrolls(250), { capture: true, passive: true });
// a drag, not the tremble of a click
let downAt = null;
addEventListener("pointerdown", (e) => { downAt = [e.clientX, e.clientY]; }, { capture: true, passive: true });
addEventListener("pointermove", (e) => {
  if (e.buttons && downAt && Math.hypot(e.clientX - downAt[0], e.clientY - downAt[1]) > 6) readerScrolls(250);
}, { capture: true, passive: true });
addEventListener("keydown", (e) => {
  const typing = e.target.closest?.("input, textarea, select, [contenteditable]");
  if (e.key === "Tab" || (!typing && SCROLL_KEYS.has(e.key))) readerScrolls(400);
}, true);
const readerAt = new WeakMap();
// Where the reader is is a number, the view's scrollTop, unless the view
// names a part of itself to keep in place (keepInView): a part under others
// that redraw on their own (the Routing page's groups, under the live stage
// and lists), which the number alone lets slide as what's above it grows or
// shrinks. Then where the reader is is where that part is on the screen,
// taken as the reader leaves it (a scroll of theirs, a click held), and the
// view follows it wherever the parts above take it: scroll anchoring, which
// WebKit lacked and which putting the number back undid.
const pinOf = new Map(), pinAt = new WeakMap();
function keepInView(v, part) {
  pinOf.set(v, part);
  v.style.overflowAnchor = "none"; // the browser's own would anchor on another part, and fight this
  pinSizes.observe(v);
  for (const c of v.children) pinSizes.observe(c);
}
function readerLeaves(v) {
  readerAt.set(v, v.scrollTop);
  // a view at its top stays at its top: nothing in it is kept in place
  const p = pinOf.has(v) && v.scrollTop >= 1 ? pinOf.get(v)() : null;
  pinAt.set(v, p ? [p, onScreen(p, v)] : null);
}
function pinnedAt(v) {
  const a = pinAt.get(v);
  return a && a[0].isConnected && a[0].offsetParent ? v.scrollTop + onScreen(a[0], v) - a[1] : null;
}
// laid out, not yet painted: a view with a part kept in place follows it
// as soon as what's above it has changed size, before the reader sees it
const pinSizes = new ResizeObserver(() => {
  for (const v of pinOf.keys()) if (!v.hidden && held?.v !== v && performance.now() >= purposeUntil) backToReader(v);
});
function backToReader(v) {
  if (v.hidden) return;
  const pinned = pinnedAt(v);
  const want = Math.max(0, Math.min(pinned ?? readerAt.get(v) ?? 0, v.scrollHeight - v.clientHeight));
  if (pinned != null) readerAt.set(v, want);
  if (Math.abs(v.scrollTop - want) < 1) return;
  v.scrollTop = want;
  // a field focused out of sight still comes into view, no further than needed
  const f = document.activeElement;
  if (f && f !== document.body && v.contains(f)) {
    const r = f.getBoundingClientRect(), b = v.getBoundingClientRect();
    if (r.bottom > b.bottom || r.top < b.top) { readerScrolls(1000); f.scrollIntoView({ block: "nearest" }); }
  }
}
// Where an element is on the screen in its view. What's held is the element
// clicked or, once it's gone or hidden (drawn again), the nearest still
// there of its neighbours, its parents and theirs; one moving as it plays
// (a row springing open) is passed over, so the page doesn't follow the play.
const onScreen = (n, v) => n.getBoundingClientRect().top - v.getBoundingClientRect().top;
// Only a play that moves it counts: a colour or a fade easing in (the hover
// of the button just clicked, its label fading to its new words) leaves it
// where it is. Were those passed over too, the page would be held by
// something further up while the button slid away, and snap back to the
// button once its hover had faded — and a button slid out from under the
// pointer and back fades its hover again, so the page swung between the two.
const MOVES = /^(transform|translate|rotate|scale|top|bottom|left|right|inset|margin|offset-|position)/;
const moving = (a) => a.playState === "running" && (a.transitionProperty ? MOVES.test(a.transitionProperty)
  : !a.effect?.getKeyframes || a.effect.getKeyframes().some((k) => Object.keys(k).some((p) => MOVES.test(p.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase())))));
const atRest = (n) => n.isConnected && n.offsetParent && !n.getAnimations().some(moving);
// The room is an empty block last in the view (padding at its foot would
// count in its height only a frame later), put back when a redraw of the
// view takes it out.
const room = new WeakMap(); // px kept at a view's foot, past its content
const roomOf = (v) => (v.querySelector(":scope > .view-room") ? room.get(v) || 0 : 0);
function setRoom(v, px) {
  px = Math.max(0, Math.round(px));
  let r = v.querySelector(":scope > .view-room");
  if (!px) { r?.remove(); room.delete(v); return; }
  if (!r) { r = document.createElement("div"); r.className = "view-room"; r.setAttribute("aria-hidden", "true"); }
  if (r !== v.lastElementChild) v.append(r);
  r.style.height = px + "px";
  room.set(v, px);
}
// only as much room as keeps the view where it is: none once the content
// reaches the view's foot again
function fitRoom(v) {
  const r = roomOf(v);
  if (r) setRoom(v, Math.min(r, v.scrollTop + v.clientHeight - (v.scrollHeight - r)));
}
function hold(h) {
  const a = h.chain.find(([n]) => atRest(n));
  if (!a) return;
  const v = h.v, d = onScreen(a[0], v) - a[1];
  if (Math.abs(d) >= 1) {
    let want = v.scrollTop + d;
    const max = v.scrollHeight - v.clientHeight;
    // a view at its top when clicked stays there rather than be given room
    // to scroll down: a chip saved in the panel's Profiles came in above the
    // button held, and room made for the button slid the chip up under the
    // tabs, out of sight
    if (want > max && h.top) want = max;
    if (want > max) setRoom(v, roomOf(v) + want - max);
    v.scrollTop = want;
  }
  fitRoom(v);
  readerLeaves(v);
}
let holding = false; // one frame loop, whatever the clicks
// A part that grows or shrinks as it plays (the agents' scroll unrolling
// above the button that unrolls it) is held again as soon as it is laid out,
// before it's painted: a frame's loop sees it only as the frame before left
// it, a frame late, so what was clicked would tremble by as much as it grew
// in a frame.
const heldSizes = new ResizeObserver(() => { if (held) hold(held); });
function keepHeld() {
  const h = held;
  if (h && (h.v.hidden || performance.now() > h.until)) held = null;
  if (!held) { holding = false; heldSizes.disconnect(); return; }
  hold(held);
  requestAnimationFrame(keepHeld);
}
addEventListener("click", (e) => {
  purposeUntil = 0; // what came before the click (Space pressed on a button, a tremble) is no scroll
  const v = e.target.closest?.(".view");
  if (!v || v.hidden) { held = null; return; }
  const chain = [];
  const from = e.target.closest?.("[data-unrolls]")?.parentElement || e.target;
  for (let n = from; n && n !== v; n = n.parentElement) {
    for (const m of [n, n.previousElementSibling, n.nextElementSibling]) if (m instanceof HTMLElement && m.offsetParent) chain.push([m, onScreen(m, v)]);
  }
  held = chain.length ? { v, chain, until: performance.now() + 4000, top: v.scrollTop < 1 } : null;
  heldSizes.disconnect();
  if (held) for (const c of v.children) if (!c.classList.contains("view-room")) heldSizes.observe(c);
  if (held && !holding) { holding = true; requestAnimationFrame(keepHeld); }
}, true);
for (const v of document.querySelectorAll(".view")) {
  v.addEventListener("scroll", () => {
    if (v.hidden) return;
    if (performance.now() < purposeUntil) { fitRoom(v); readerLeaves(v); }
    else if (held?.v === v) hold(held);
    else backToReader(v);
  }, { passive: true });
}

function show(v) {
  view = v;
  if (mode === "window") { for (const b of $("#nav").querySelectorAll("button")) b.classList.toggle("on", b.dataset.view === v); slide($("#nav"), "nav"); }
  $("#prefs").classList.toggle("on", v === "settings");
  for (const id of ["agents", "providers", "gateway", "routing", "usage", "library", "plugins", "settings"]) $("#view-" + id).hidden = v !== id;
  // back to where the reader was in it, and again once it has what it loads
  const back = () => backToReader($("#view-" + v));
  requestAnimationFrame(back);
  closePicker();
  closeAgentModels();
  if (v !== "providers" && editing !== null) cancelEdit();
  if (v === "providers" || v === "gateway" || v === "routing") loadProviders().then(back, (e) => status(e.message, "err"));
  if (v === "usage") loadUsage().then(back, (e) => status(e.message, "err"));
  if (v === "settings") loadSettings().then(back, (e) => status(e.message, "err"));
  if (v === "library") window.loadLibrary?.()?.then(back);
  if (v === "plugins") window.loadPlugins?.()?.then(back);
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
  // a newer magpie is looked for too: the Update pill beside it shows once
  // it's in (inaction on Discord looked for it here, not in Settings)
  api("update/check", {}).then(renderUpdateBadge, () => {});
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
// centring and take the room between, and at the narrowest they draw in.
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
  top.classList.remove("tight", "cramped", "crowded");
  if (fits()) return;
  top.classList.add("tight");
  if (fits()) return;
  top.classList.add("cramped");
  if (!fits()) top.classList.add("crowded");
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
  thumbs.set(thumbKey($("#nav"), "nav"), { x: on.offsetLeft, w: on.offsetWidth });
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
// the usage page counts on while it is looked at: a request through the
// gateway shows within seconds, the subscriptions' windows each minute (the
// vendors' answers are cached behind them) — redrawn only on a change
let usageTicks = 0;
setInterval(async () => {
  if (view !== "usage" || document.hidden || document.querySelector(".pop:not([hidden])")) return;
  if (usageTab === "sessions") {
    // the agents write their session files as they go
    if (++usageTicks % 3 === 0 && sessions) loadSessions().catch(() => {});
    return;
  }
  if (usageTab === "requests") {
    // the newest page takes the requests as they come; an older one stays put
    if (ledger && !ledOffset) loadLedger(true).catch(() => {});
    return;
  }
  if (!usage) return;
  if (++usageTicks % 12 === 0) loadQuotas();
  const p = period;
  let u;
  try { u = await api("usage?period=" + p); } catch { return; }
  if (view !== "usage" || usageTab !== "usage" || p !== period || JSON.stringify(u) === JSON.stringify(usage)) return;
  usage = u;
  renderUsage();
}, 5000);
window.addEventListener("focus", load);
setInterval(renderUpdateBadge, 15 * 60 * 1000); // a window left open still hears of a new version
// ---------- hiding emails, for a screenshot to share ----------
// Routing and Usage each have a Hide emails button, one setting for both.
// Each email address on the page — an account's, in a row, a sentence,
// a tooltip — is swapped for blurred stand-in letters while it's on, as the
// page redraws too; the address itself is kept aside to put back.
(() => {
  const EYE = "M2 12s3.5-8 10-8 10 8 10 8-3.5 8-10 8-10-8-10-8zM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z";
  const EYE_OFF = "M9.9 4.2A10.4 10.4 0 0 1 12 4c6.5 0 10 8 10 8a17.6 17.6 0 0 1-2.2 3.2M6.6 6.6C3.9 8.4 2 12 2 12s3.5 8 10 8a9.7 9.7 0 0 0 5.4-1.6M9.9 9.9a3 3 0 0 0 4.2 4.2M2 2l20 20";
  // an address a vendor has half masked itself (Zhipu's abc***gh@…) is one
  // address still, the letters before its stars hidden too
  const EMAIL = /[\w.+*•-]+@[\w*•-]+(?:\.[\w*•-]+)+/g, IS_EMAIL = new RegExp(EMAIL.source);
  // stand-in letters of the address's shape, the same each time it's drawn:
  // blurred, they read as a name without being one
  const dots = (s) => { let h = 7; return s.replace(/[^@.]/g, (c) => (h = (h * 31 + c.charCodeAt(0)) >>> 0, "aeiounrstlcmdh"[h % 14])); };
  // what a page redraws is masked before it's painted; masking isn't
  // itself watched, so it can't set itself off again
  const OBS = { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ["title"] };
  let masked = false;
  try { masked = localStorage.getItem("magpie.maskEmails") === "1"; } catch {}
  const pages = [["#view-routing", "#rtMask"], ["#view-usage", "#usageMask"]].map(([v, b]) => {
    const view = $(v), btn = $(b);
    function mask() {
      const walk = document.createTreeWalker(view, NodeFilter.SHOW_TEXT), found = [];
      for (let n; (n = walk.nextNode());) if (n.data.includes("@") && IS_EMAIL.test(n.data) && !n.parentElement?.closest(".pii")) found.push(n);
      for (const n of found) {
        const bits = [];
        let last = 0;
        for (const m of n.data.matchAll(EMAIL)) {
          if (m.index > last) bits.push(n.data.slice(last, m.index));
          const s = el("span", "pii", dots(m[0]));
          s.dataset.raw = m[0];
          bits.push(s);
          last = m.index + m[0].length;
        }
        if (!last) continue;
        if (last < n.data.length) bits.push(n.data.slice(last));
        // one piece still, where the text was: in a flex row each would
        // otherwise stand as an item of its own
        if (bits.length > 1) { const run = el("span", "pii-run"); run.append(...bits); n.replaceWith(run); }
        else n.replaceWith(...bits);
      }
      for (const e of view.querySelectorAll("[title]")) {
        // one masked already reads as an address too, its stars and all
        if ("piiTitle" in e.dataset || !e.title.includes("@") || !IS_EMAIL.test(e.title)) continue;
        e.dataset.piiTitle = e.title;
        e.title = e.title.replace(EMAIL, (m) => m.replace(/[^@.]/g, "•")); // a tooltip can't blur
      }
    }
    function unmask() {
      for (const s of view.querySelectorAll(".pii")) s.replaceWith(s.dataset.raw);
      for (const r of view.querySelectorAll(".pii-run")) r.replaceWith(r.textContent);
      view.normalize();
      for (const e of view.querySelectorAll("[data-pii-title]")) { e.title = e.dataset.piiTitle; delete e.dataset.piiTitle; }
    }
    const watch = new MutationObserver(() => {
      if (!masked) return;
      watch.disconnect();
      mask();
      watch.observe(view, OBS);
    });
    btn.onclick = () => {
      setMasked(!masked);
      // pixelated in when asked for, not again each time the page redraws
      view.classList.add("masking");
      clearTimeout(btn._t);
      btn._t = setTimeout(() => view.classList.remove("masking"), 450);
    };
    return (on) => {
      btn.setAttribute("aria-pressed", String(on));
      // what it is now, in its icon and its words: an open eye while the
      // addresses show, struck through once they're hidden
      btn.querySelector("path").setAttribute("d", on ? EYE_OFF : EYE);
      const label = btn.querySelector("[data-t]");
      label.dataset.en = on ? "Emails hidden" : "Hide emails";
      label.textContent = t(label.dataset.en);
      view.classList.toggle("masked", on);
      if (on) { mask(); watch.observe(view, OBS); }
      else { watch.disconnect(); unmask(); }
    };
  });
  function setMasked(on) {
    masked = on;
    try { localStorage.setItem("magpie.maskEmails", on ? "1" : "0"); } catch {}
    for (const set of pages) set(on);
  }
  setMasked(masked);
})();

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
if (mode === "window" && ["providers", "gateway", "routing", "usage", "library", "plugins", "settings"].includes(params.get("view"))) show(params.get("view"));
else if (mode === "window") slide($("#nav"), "nav");
load();
