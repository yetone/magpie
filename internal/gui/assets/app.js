// magpie — one state object per view, rendered into a list. No framework.
const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);
const params = new URLSearchParams(location.search);
const mode = params.get("mode") || "window";
document.body.classList.add(mode);
// The panel loads Wails for ExecJS readiness, but stays attached to the tray.
if (mode === "panel") $("header.top").style.setProperty("--wails-draggable", "no-drag");
// `magpie web`: the page in a browser tab, with no window of the app's
// around it — it opens links itself, and what is the desktop's is left out
const web = !!window.bootPrefs?.web;
if (web) document.body.classList.add("web");
// iOS zooms the page into a field it focuses whose text is under 16px, and
// leaves it zoomed; at most 1 stops that, and Safari still lets a pinch zoom
if (web && (/iP(hone|ad|od)/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1))) {
  document.querySelector('meta[name="viewport"]')?.setAttribute("content", "width=device-width, initial-scale=1, maximum-scale=1, viewport-fit=cover");
}
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
// Outside the app — a browser on the gateway's page, or on `magpie web` —
// there is no runtime, and it isn't asked for (a 404 in the browser's
// console, Jorben on Discord).
// The panel also needs runtime readiness to drain Go's queued ExecJS calls.
const winRuntime = !web ? import("/wails/runtime.js").catch(() => null) : Promise.resolve(null);
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
let period = "today"; // usage window
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
// a streamed reply's body is read as its reply, its events or as it came;
// the pick is one for every call and kept, the events drawn per call
let sseView = "events";
try { sseView = localStorage.getItem("magpie.sseView") || "events"; } catch {}
const sseShown = new Map(); // call id → how many events are drawn
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
  if (!res.ok) {
    const err = new Error(data?.error || `${res.status} ${res.statusText}`);
    if (data?.why) err.why = data.why; // a failed move's reason, said in the reader's language
    throw err;
  }
  // the accounts it names, for Hide accounts to know them (#568); a
  // session's or a library's words aren't looked through
  if (data && typeof data === "object" && !/^(sessions|library|skills|plugins|market)/.test(path)) noteAccounts(data);
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
const CROSS = "M4.5 4.5l7 7M11.5 4.5l-7 7";
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

// directSaid: a model the agent asks its own vendor for itself, magpie not
// in the way (Claude Code on its own sign-in), so its config names no magpie
// endpoint — which read as magpie having failed to set it up
function directSaid(a, opt) {
  if (!opt?.direct) return "";
  return t("Not through magpie: {agent} asks {vendor} for it directly, with its own sign-in or key, so {path} has no magpie endpoint — that is expected.", { agent: a.name, vendor: opt.direct, path: a.path });
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

let agentArranging = false, agentRenderPending = false;
function renderAgents() {
  if (agentArranging) { agentRenderPending = true; return; }
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
    // an effort or ultracode the model has none of (Claude Code on Haiku
    // 4.5, ultracode short of xhigh) isn't drawn at all, nor are subagents
    // with no model to go on (Claude Code's, until it runs through magpie)
    const none = (f) => (f.key === "effort" || f.key === "ultracode" || f.label === "subagents" || f.label === SUB_EFFORT) && !f.options.length && !f.value;
    const shownFields = a.fields.filter((f) => !TIERS.includes(f.label) && !TIER_EFFORTS.includes(f.label) && !none(f));
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
      if (f.value && opt?.direct) b.title += "\n" + directSaid(a, opt);
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
      // none lit for the default, off, none or auto, or for an agent that has
      // no such setting
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
// update or use is on its way dimmed until the answer is in. A click on a
// chip shows what the profile holds, and its Apply applies it (#467): it was
// applied at once, so what it held could be seen only by applying it over
// the setup in use.
function renderProfiles() {
  const chips = $("#profiles");
  if (!state.profiles.some((p) => p.name === profileOpen)) profileOpen = null;
  chips.replaceChildren();
  $(".profiles > .chip-input")?.remove(); // a name field open goes with the list it was for
  $(".profiles").classList.remove("naming", "detailed");
  $("#save").textContent = t("＋ Save current");
  $("#profN").textContent = state.profiles.length || "";
  if (!state.profiles.length) chips.append(el("span", "hint", t("none yet · save the setup to switch back in one click")));
  for (const p of state.profiles) {
    const c = el("button", "chip");
    c._profile = p.name;
    if (profilePending.has(p.name)) c.classList.add("pending");
    const lib = profileLibrary(p.library);
    c.title = [p.summary, lib].filter(Boolean).join("\n");
    c.append(el("span", "", p.name));
    if (lib) c.append(el("span", "lib"));
    // the setup as it is now, saved over this profile; it, and ×, on a
    // second click (#478)
    const u = el("span", "x");
    u._arm = "save";
    u.onclick = (ev) => { ev.stopPropagation(); armOrDo(p.name, "save"); };
    const x = el("span", "x");
    x._arm = "delete";
    x.onclick = (ev) => { ev.stopPropagation(); armOrDo(p.name, "delete"); };
    c.append(u, x);
    paintArm(c);
    c.setAttribute("aria-expanded", "false");
    c.onclick = () => toggleProfile(c, p);
    chips.append(c);
  }
  const open = state.profiles.find((p) => p.name === profileOpen);
  if (open) showProfile([...chips.children].find((c) => c._profile === open.name), open);
}

// profileArm is a chip's ↻ or × clicked once: it then reads "Overwrite?"
// or "Delete?", and a second click does it (#478: either acted on the first
// click, so a slip overwrote or deleted a profile). It goes after a few
// seconds, on Escape, or when the other one is clicked.
let profileArm = null;
const ARM = {
  save: { glyph: "↻", title: "Update to the current setup", armed: "Overwrite?", armedTitle: "Click again to update {name} to the current setup" },
  delete: { glyph: "×", title: "Delete profile", armed: "Delete?", armedTitle: "Click again to delete {name}" },
};

function armOrDo(name, action) {
  if (profileArm?.name === name && profileArm.action === action) {
    disarmProfile();
    profileAction(action, name, action === "save");
    return;
  }
  clearTimeout(profileArm?.timer);
  profileArm = { name, action, timer: setTimeout(disarmProfile, 3500) };
  for (const c of $("#profiles").children) paintArm(c);
}

function disarmProfile() {
  if (!profileArm) return false;
  clearTimeout(profileArm.timer);
  profileArm = null;
  for (const c of $("#profiles").children) paintArm(c);
  return true;
}

// paintArm draws a chip's ↻ and × as profileArm has them.
function paintArm(c) {
  for (const s of c.querySelectorAll?.(":scope > .x") || []) {
    const a = ARM[s._arm], on = profileArm?.name === c._profile && profileArm.action === s._arm;
    s.classList.toggle("arm", on);
    s.textContent = on ? t(a.armed) : a.glyph;
    s.title = on ? t(a.armedTitle, { name: c._profile }) : t(a.title);
  }
}

// profileOpen is the profile whose details are shown, by name. However they
// close — a second click on the chip, their ×, Escape, Apply, the panel's
// list closed — they stay closed, the list opening again on the chips alone
// (#478: closed by hand they came back with the list).
let profileOpen = null;

function toggleProfile(chip, p) {
  if (profileOpen === p.name) closeProfileDetail();
  else {
    profileOpen = p.name;
    showProfile(chip, p);
    fit();
  }
}

// closeProfileDetail closes the details shown, if any: false if none were.
// Details on the page close whether or not profileOpen still names them.
function closeProfileDetail() {
  if (!profileOpen && !$("#profiles > .prof-detail")) return false;
  profileOpen = null;
  $("#profiles > .prof-detail")?.remove();
  $(".profiles").classList.remove("detailed");
  for (const c of $("#profiles").querySelectorAll(".chip.on")) { c.classList.remove("on"); c.setAttribute("aria-expanded", "false"); }
  fit();
  return true;
}

// profileEscape is Escape for the profiles: it takes back a ↻ or × waiting
// for its second click, else closes the details, else the panel's list —
// one at a time, and never the tray panel with them (#478: Escape on the
// details hid the whole panel). False if there was nothing to close.
function profileEscape() {
  if (disarmProfile()) return true;
  const chip = $("#profiles .chip.on");
  if (closeProfileDetail()) { chip?.focus({ preventScroll: true }); return true; }
  if (mode === "panel" && profBox.classList.contains("open")) { closeProfiles(); profBtn.focus({ preventScroll: true }); return true; }
  return false;
}

// showProfile draws what a profile holds, by agent, under its chip's line;
// in the panel, where the list opens upward from its foot, over it, its
// Apply at its foot by the chip, so the chip clicked stays where it is as the
// details come in.
function showProfile(chip, p) {
  const chips = $("#profiles"), box = $(".profiles");
  chips.querySelector(":scope > .prof-detail")?.remove();
  box.classList.remove("detailed");
  for (const c of chips.querySelectorAll(".chip.on")) { c.classList.remove("on"); c.setAttribute("aria-expanded", "false"); }
  if (!chip) return;
  chip.classList.add("on");
  chip.setAttribute("aria-expanded", "true");
  const panel = mode === "panel";
  const was = chip.getBoundingClientRect().top;
  const line = [...chips.querySelectorAll(":scope > .chip")].filter((c) => c.offsetTop === chip.offsetTop);
  const d = profileDetail(p, panel);
  box.classList.add("detailed"); // the window's label and Save at the chips' first line
  if (panel) line[0].before(d);
  else line[line.length - 1].after(d);
  // grown past its height, the panel's list scrolls the details in above
  // the chip rather than the chip down
  if (panel) box.scrollTop += chip.getBoundingClientRect().top - was;
}

function profileDetail(p, footed) {
  const d = el("div", "prof-detail");
  d.setAttribute("role", "group");
  d.setAttribute("aria-label", p.name);
  const head = el("div", "pd-head");
  const apply = el("button", "text primary pd-apply", t("Apply"));
  apply.type = "button";
  apply.title = t("Apply {name} to the agents", { name: p.name });
  apply.onclick = () => profileAction("use", p.name);
  const close = el("button", "pd-close", "×");
  close.type = "button";
  close.title = t("Close");
  close.setAttribute("aria-label", t("Close"));
  close.onclick = () => {
    const chip = $("#profiles .chip.on");
    closeProfileDetail();
    chip?.focus({ preventScroll: true });
  };
  head.append(el("span", "pd-name", p.name), apply, close);
  if (!footed) d.append(head);
  const groups = p.agents || [];
  if (!groups.length) d.append(el("div", "pd-none", t("No agent settings saved")));
  for (const g of groups) {
    const sec = el("div", "pd-agent");
    const gh = el("div", "pd-gh");
    gh.append(icon(g.icon || "generic"), el("span", "pd-gn", g.name));
    const rows = el("dl", "pd-fields");
    const row = (label, value, cls) => {
      const v = el("dd", cls || "", value);
      v.title = value;
      rows.append(el("dt", "", label), v);
    };
    for (const f of g.fields || []) {
      const effort = f.key === "effort" || f.label === "effort" || f.label === "thinking";
      // a Claude Code tier left to follow the main model (#480), and to what
      const main = f.follows && g.fields.find((x) => x.key === f.follows && !x.hidden)?.value;
      if (f.hidden) row(t(f.label), "••••••", "pd-hidden");
      else if (f.follows) row(t(f.label), main ? t("follows the main model ({model})", { model: main }) : t("follows the main model"), "pd-default");
      else if (!f.value) row(t(f.label), t("agent default"), "pd-default");
      else row(t(f.label), effort ? effortName({ value: f.value }) : f.value);
    }
    if (g.servers?.length) row(t("MCP servers"), g.servers.join(t(", ")));
    if (g.skills?.length) row(t("Skills"), g.skills.join(t(", ")));
    if (g.instructions) row(t("Instructions"), t("on"));
    sec.append(gh, rows);
    d.append(sec);
  }
  if (groups.some((g) => g.fields?.some((f) => f.hidden))) d.append(el("div", "pd-none", t("•••••• looks like a key or a token, and is not shown")));
  if (footed) { head.classList.add("pd-foot"); d.append(head); }
  return d;
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

// An unplugged plug: the agent taken off magpie.
const UNPLUG = "M6 2v3M10 2v3M4.5 5h7v2.5a3.5 3.5 0 0 1-7 0zM8 11v3M2 2l12 12";
const DISCONNECT_TIP = "Take out everything magpie wrote into {agent}'s config and put back what it had before";

// askDisconnect takes magpie out of an agent's config once asked: every
// field magpie set, and its endpoint and key, give way to what the agent
// had before magpie (agent.Disconnect). Picking Default isn't that: it is
// the agent as installed.
function askDisconnect(a) {
  const ed = el("div", "editor disconnect-ask");
  const head = el("div", "ehead");
  head.append(icon(a.icon), el("b", "", t("Disconnect {agent} from magpie?", { agent: a.name })));
  ed.append(head);
  ed.append(el("p", "lib-confirm", t("magpie takes out everything it wrote into {agent}'s config — its endpoint, key, models and effort — and puts back the settings {agent} had before. magpie's providers and accounts stay as they are.", { agent: a.name })));
  const bar = el("div", "bar");
  const go = el("button", "text primary danger-fill", t("Disconnect"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    try {
      state = await api("agents/disconnect/" + a.id, {});
      closeConfirmAsk();
      renderAgents();
      const msg = t("{agent} no longer goes through magpie; its own settings are back", { agent: a.name });
      if (state.notice) status(`${msg}. ${t(state.notice)}`, "warn", 9000);
      else status(msg, "ok");
    } catch (err) {
      go.disabled = false;
      go.classList.remove("busy");
      status(err.message, "err");
    }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  cancel.focus({ preventScroll: true });
}

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
    if (state.notice) status(`${msg}. ${t(state.notice)}`, "warn", 9000);
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

// set up: a model, a provider, a role given one — what magpie is there for.
// How the agent's own model is run (ultracode, an effort) is not: turning
// Claude Code's ultracode on folded every other agent away under Show more
const tweak = (f) => f.key === "ultracode" || f.key === "effort" || f.key.endsWith("_effort");
const agentUsed = (a) => a.added || a.fields.some((f) => f.value && !tweak(f));

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
  // hidden, it says where it went, since the row goes out of sight; shown
  // with nothing set on it, it stays folded under Not set up, and says why
  const still = !hide && arrangeAgents().folded.includes(a);
  status(t(hide ? "{agent} hidden · find it under Hidden at the bottom"
    : still ? "{agent} is no longer hidden · it stays under Not set up until a model is picked for it" : "{agent} shown", { agent: a.name }), "ok", hide || still ? 4000 : 1800);
}

const ALT = /^Mac/.test(navigator.platform) ? "⌥" : "Alt+";
const EYE_OFF = "M6.6 3.7A6.9 6.9 0 0 1 8 3.5c3.75 0 6.25 4.5 6.25 4.5a11 11 0 0 1-1.5 2M4.4 4.4C2.7 5.55 1.75 8 1.75 8S4.25 12.5 8 12.5c1.2 0 2.25-.45 3.1-1.05M6.75 6.75a1.75 1.75 0 0 0 2.5 2.5M2 2l12 12";
const EYE = "M1.75 8S4.25 3.5 8 3.5 14.25 8 14.25 8 11.75 12.5 8 12.5 1.75 8 1.75 8ZM8 9.75a1.75 1.75 0 1 0 0-3.5 1.75 1.75 0 0 0 0 3.5Z";

// agentHandle is the row's logo, which is also its handle: drag it to move
// the row, click it (or right-click the row) for Move up, Move down and
// Hide; Alt+↑/↓ moves it from the keyboard. The grip that says so is drawn
// in the row's margin on hover (app.css), not over the logo.
function agentHandle(a, row, inFold) {
  const b = el("button", "ag-handle");
  b.type = "button";
  b.setAttribute("aria-label", t("Arrange {agent}", { agent: a.name }));
  b.setAttribute("aria-haspopup", "menu");
  b.title = inFold ? t(isHidden(a) ? "Show {agent}" : "Hide {agent}", { agent: a.name }) : t(a.wired ? "Drag to reorder · click to move, hide or disconnect from magpie" : "Drag to reorder · click to move or hide");
  b.append(icon(a.icon));
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
  if (agentArranging) return;
  const list = $("#agents");
  agentArranging = dragRows(e, handle, row, list, [...list.children].filter((r) => r.classList.contains("agent")),
    (to) => moveAgent(row.dataset.id, to), closeAgentMenu, () => {
      agentArranging = false;
      if (agentRenderPending) { agentRenderPending = false; renderAgents(); }
    });
}

// Shared pointer sorter. Measure once, move only transforms on animation frames,
// and commit once on release. No DOM rebuilds or network calls during a drag.
// The same primitive serves agent handles and handle-free account rows.
function dragRows(e, handle, row, list, rows, commit, start = () => {}, idle = () => {}) {
  if (e.button !== 0 || e.isPrimary === false || rows.length < 2) return false;
  const from = rows.indexOf(row);
  if (from < 0) return false;
  const rects = rows.map((r) => r.getBoundingClientRect());
  const tops = rects.map((r) => r.top), heights = rects.map((r) => r.height), h = heights[from];
  let scroll = list.parentElement;
  while (scroll && !/(auto|scroll)/.test(getComputedStyle(scroll).overflowY)) scroll = scroll.parentElement;
  scroll ||= document.scrollingElement;
  const bounds = scroll.getBoundingClientRect(), scroll0 = scroll.scrollTop;
  let dragging = false, ended = false, to = from, y = e.clientY, frame = 0, lastTime = 0;
  const y0 = y, pointer = e.pointerId;
  const paint = (time) => {
    frame = 0;
    if (!row.isConnected) return finish(false);
    if (!dragging) return;
    const dt = Math.min(32, lastTime ? time - lastTime : 16);
    lastTime = time;
    const edge = 36;
    const speed = y < bounds.top + edge ? -Math.min(1, (bounds.top + edge - y) / edge)
      : y > bounds.bottom - edge ? Math.min(1, (y - bounds.bottom + edge) / edge) : 0;
    if (speed) scroll.scrollTop += speed * dt * .6;
    const dy = y - y0 + scroll.scrollTop - scroll0;
    const lo = tops[0] - tops[from], hi = tops.at(-1) + heights.at(-1) - h - tops[from];
    const d = Math.max(lo, Math.min(hi, dy));
    row.style.transform = `translateY(${d}px)`;
    const mid = tops[from] + d + h / 2;
    to = from;
    if (d > 0) { while (to < rows.length - 1 && mid >= tops[to + 1] + heights[to + 1] / 2) to++; }
    else { while (to > 0 && mid <= tops[to - 1] + heights[to - 1] / 2) to--; }
    // held at the top or the foot it goes there, though the first row is a
    // pixel shorter than the rest (they have a border over them) and its
    // middle was out of reach (#499)
    if (d <= lo && from > 0) to = 0;
    else if (d >= hi && from < rows.length - 1) to = rows.length - 1;
    rows.forEach((r, i) => {
      if (i === from) return;
      const shift = i > from && i <= to ? -h : i < from && i >= to ? h : 0;
      r.style.transform = shift ? `translateY(${shift}px)` : "";
    });
    if (speed) frame = requestAnimationFrame(paint);
  };
  const move = (ev) => {
    if (ev.pointerId !== pointer) return;
    if (!row.isConnected || !handle.isConnected) return finish(false, ev);
    y = ev.clientY;
    if (!dragging) {
      if (Math.abs(y - y0) < 4) return;
      dragging = true;
      handle.dataset.dragged = "1";
      start();
      list.classList.add("sorting");
      row.classList.add("dragging");
      getSelection()?.removeAllRanges();
      handle.setPointerCapture(pointer);
    }
    ev.preventDefault();
    if (!frame) frame = requestAnimationFrame(paint);
  };
  const finish = (save, ev) => {
    if (ended || (ev?.pointerId != null && ev.pointerId !== pointer)) return;
    if (save && dragging && ev) {
      y = ev.clientY;
      cancelAnimationFrame(frame);
      paint(performance.now());
    }
    if (ended) return;
    ended = true;
    cancelAnimationFrame(frame);
    document.removeEventListener("pointermove", move);
    document.removeEventListener("pointerup", up);
    document.removeEventListener("pointercancel", cancel);
    document.removeEventListener("keydown", keys, true);
    handle.removeEventListener("lostpointercapture", cancel);
    removeEventListener("blur", cancel);
    removeEventListener("resize", cancel);
    if (handle.hasPointerCapture(pointer)) handle.releasePointerCapture(pointer);
    list.classList.remove("sorting");
    row.classList.remove("dragging");
    rows.forEach((r) => { r.style.transform = ""; });
    if (save && dragging && to !== from) commit(to);
    idle();
    // The click dispatched after pointerup must not rename, toggle or remove.
    setTimeout(() => delete handle.dataset.dragged, 0);
  };
  const up = (ev) => finish(true, ev);
  const cancel = (ev) => finish(false, ev);
  const keys = (ev) => { if (ev.key === "Escape") { ev.preventDefault(); ev.stopImmediatePropagation(); finish(false); } };
  document.addEventListener("pointermove", move, { passive: false });
  document.addEventListener("pointerup", up);
  document.addEventListener("pointercancel", cancel);
  document.addEventListener("keydown", keys, true);
  handle.addEventListener("lostpointercapture", cancel);
  addEventListener("blur", cancel);
  addEventListener("resize", cancel);
  return true;
}

// dragCards is dragRows for cards laid out in a grid: the card follows the
// pointer both ways, the slot nearest its middle is where it goes, and the
// cards between move a slot over to make room.
function dragCards(e, handle, card, list, cards, commit, idle = () => {}) {
  if (e.button !== 0 || e.isPrimary === false || cards.length < 2) return false;
  const from = cards.indexOf(card);
  if (from < 0) return false;
  const rects = cards.map((c) => c.getBoundingClientRect());
  let scroll = list.parentElement;
  while (scroll && !/(auto|scroll)/.test(getComputedStyle(scroll).overflowY)) scroll = scroll.parentElement;
  scroll ||= document.scrollingElement;
  const bounds = scroll.getBoundingClientRect(), scroll0 = scroll.scrollTop;
  let dragging = false, ended = false, to = from, x = e.clientX, y = e.clientY, frame = 0, lastTime = 0;
  const x0 = x, y0 = y, pointer = e.pointerId;
  const r0 = rects[from];
  const paint = (time) => {
    frame = 0;
    if (!card.isConnected) return finish(false);
    if (!dragging) return;
    const dt = Math.min(32, lastTime ? time - lastTime : 16);
    lastTime = time;
    const edge = 36;
    const speed = y < bounds.top + edge ? -Math.min(1, (bounds.top + edge - y) / edge)
      : y > bounds.bottom - edge ? Math.min(1, (y - bounds.bottom + edge) / edge) : 0;
    if (speed) scroll.scrollTop += speed * dt * .6;
    const dx = x - x0, dy = y - y0 + scroll.scrollTop - scroll0;
    card.style.transform = `translate(${dx}px, ${dy}px)`;
    const cx = r0.left + r0.width / 2 + dx, cy = r0.top + r0.height / 2 + dy;
    let best = Infinity;
    rects.forEach((r, i) => {
      const d = (cx - r.left - r.width / 2) ** 2 + (cy - r.top - r.height / 2) ** 2;
      if (d < best) { best = d; to = i; }
    });
    cards.forEach((c, i) => {
      if (i === from) return;
      const j = i > from && i <= to ? i - 1 : i < from && i >= to ? i + 1 : i;
      c.style.transform = j !== i ? `translate(${rects[j].left - rects[i].left}px, ${rects[j].top - rects[i].top}px)` : "";
    });
    if (speed) frame = requestAnimationFrame(paint);
  };
  const move = (ev) => {
    if (ev.pointerId !== pointer) return;
    if (!card.isConnected || !handle.isConnected) return finish(false, ev);
    x = ev.clientX;
    y = ev.clientY;
    if (!dragging) {
      if (Math.hypot(x - x0, y - y0) < 4) return;
      dragging = true;
      handle.dataset.dragged = "1";
      list.classList.add("sorting");
      card.classList.add("dragging");
      getSelection()?.removeAllRanges();
      handle.setPointerCapture(pointer);
    }
    ev.preventDefault();
    if (!frame) frame = requestAnimationFrame(paint);
  };
  const finish = (save, ev) => {
    if (ended || (ev?.pointerId != null && ev.pointerId !== pointer)) return;
    if (save && dragging && ev) {
      x = ev.clientX;
      y = ev.clientY;
      cancelAnimationFrame(frame);
      paint(performance.now());
    }
    if (ended) return;
    ended = true;
    cancelAnimationFrame(frame);
    document.removeEventListener("pointermove", move);
    document.removeEventListener("pointerup", up);
    document.removeEventListener("pointercancel", cancel);
    document.removeEventListener("keydown", keys, true);
    handle.removeEventListener("lostpointercapture", cancel);
    removeEventListener("blur", cancel);
    removeEventListener("resize", cancel);
    if (handle.hasPointerCapture(pointer)) handle.releasePointerCapture(pointer);
    list.classList.remove("sorting");
    card.classList.remove("dragging");
    cards.forEach((c) => { c.style.transform = ""; });
    if (save && dragging && to !== from) commit(to);
    idle();
    setTimeout(() => delete handle.dataset.dragged, 0);
  };
  const up = (ev) => finish(true, ev);
  const cancel = (ev) => finish(false, ev);
  const keys = (ev) => { if (ev.key === "Escape") { ev.preventDefault(); ev.stopImmediatePropagation(); finish(false); } };
  document.addEventListener("pointermove", move, { passive: false });
  document.addEventListener("pointerup", up);
  document.addEventListener("pointercancel", cancel);
  document.addEventListener("keydown", keys, true);
  handle.addEventListener("lostpointercapture", cancel);
  addEventListener("blur", cancel);
  addEventListener("resize", cancel);
  return true;
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
  const reapply = a.drift || a.fields.some((f) => optionFor(f, f.value)?.ref);
  const acts = inFold && isHidden(a)
    ? [{ name: "Show", icon: EYE, run: () => setAgentHidden(a, false) }]
    : inFold
    ? [{ name: "Hide", icon: EYE_OFF, run: () => setAgentHidden(a, true) }]
    : [
        { name: "Move up", icon: "M8 12.5v-9M4 7.25l4-3.75 4 3.75", key: ALT + "↑", off: i <= 0, run: () => moveAgent(a.id, i - 1) },
        { name: "Move down", icon: "M8 3.5v9M4 8.75l4 3.75 4-3.75", key: ALT + "↓", off: i < 0 || i >= shown.length - 1, run: () => moveAgent(a.id, i + 1) },
        // for a config rewritten in a way magpie can't see: set it again anyway
        ...(reapply ? [{ name: "Apply again", icon: REAPPLY, sep: true, run: () => reapplyAgent(a) }] : []),
        ...(a.drift?.kind === "replaced" ? [{ name: "Keep current settings", icon: CHECK, run: () => keepAgent(a) }] : []),
        // everything magpie wrote comes out, what the user had goes back (Fate on Discord)
        ...(a.wired ? [{ name: "Disconnect from magpie", icon: UNPLUG, sep: !reapply, tip: t(DISCONNECT_TIP, { agent: a.name }), run: () => askDisconnect(a) }] : []),
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
    if (o.tip) b.title = o.tip;
    // an item that is off can say why, under its name and as its title
    if (o.why) { b.classList.add("has-why"); b.title = o.why; b.querySelector(".rm-name").append(el("span", "rm-why", o.why)); }
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
// and each an effort of its own (#536), in the same menu, under its model
const TIER_EFFORTS = TIERS.map((tier) => tier + " effort");
// fields that fall back to the agent's model when unset: omp's smol and
// slow roles take the session's, as its subagents do ("smol", not "small":
// other agents' small model is a picker of its own)
const FOLLOWS_MODEL = [...TIERS, "subagents", "smol", "slow"];

// A field that follows the model unless set — Codex's subagents, Claude
// Code's tiers, omp's roles — is a small square after the pickers rather
// than a third picker, which a row has no room for: it wrapped onto a line
// of its own. So is Codex's sign-in, ChatGPT or magpie as its provider,
// and the effort its subagents start at (#469).
const SUB_EFFORT = "subagent effort";
const extra = (f) => f.key === "tiers" || FOLLOWS_MODEL.includes(f.label) || f.label === "sign-in" || f.key === "ultracode" || f.label === SUB_EFFORT;
const EXTRA_GLYPH = {
  subagents: "M4.5 2.75v10.5M4.5 9.25c0-2.2 1.6-3.75 3.9-3.75h3.35M9.9 3.6l1.9 1.9-1.9 1.9",
  // a feather for omp's smol role, an hourglass for its slow one
  smol: "M12.75 3.25c-4.5 0-8 3.5-8 8v1.5M12.75 3.25c0 4-2.75 7-6.75 7.25M4.75 12.75l-1.5 1.5",
  slow: "M4.5 2.5h7M4.5 13.5h7M5.25 2.5c0 3 5.5 3 5.5 5.5s-5.5 2.5-5.5 5.5M10.75 2.5c0 3-5.5 3-5.5 5.5s5.5 2.5 5.5 5.5",
  tiers: "M8 2.6 2.75 5.4 8 8.2l5.25-2.8zM2.75 8.1 8 10.9l5.25-2.8M2.75 10.8 8 13.6l5.25-2.8",
  "sign-in": "M8 2.5a2.75 2.75 0 1 1 0 5.5 2.75 2.75 0 0 1 0-5.5zM3 13.5c.4-2.4 2.4-3.9 5-3.9s4.6 1.5 5 3.9",
  ultracode: "M3 4.25h4M3 8h2.5M3 11.75h4M9.5 4.25l3.5 3.75-3.5 3.75",
  // the subagents' branch, with effort's rising bars after it
  [SUB_EFFORT]: "M3.25 2.75v10.5M3.25 9.25c0-2.2 1.6-3.75 3.9-3.75h.6M9.25 13.25v-2M11.5 13.25v-4M13.75 13.25v-6",
};
function extraField(a, f) {
  if (f.key === "ultracode") return ultracodeToggle(a, f);
  const set = !!(f.value || f.custom);
  const b = el("button", "field extra" + (set ? " set" : ""));
  b.append(svg(EXTRA_GLYPH[f.label] || EXTRA_GLYPH.tiers, 13, 1.5));
  const opt = optionFor(f, f.value);
  b.title = f.label === SUB_EFFORT ? subEffortTitle(a, f, opt) : f.menu ? menuTitle(f)
    : t("{label}: {value}", { label: t(f.label), value: t(opt?.label || f.value || "same as model") }) + (opt?.note && !FOLLOWS_MODEL.includes(f.label) ? "\n" + t(opt.note) : "");
  b.setAttribute("aria-label", b.title);
  b.dataset.key = f.key;
  b.onclick = (ev) => openPicker(a, f, b, ev);
  return b;
}

// menuTitle: a menu square's title, its summary and then each of its entries
function menuTitle(f) {
  return t("{label}: {value}", { label: t(f.label), value: f.summary }) + "\n" + f.options.map((o) => `${t(o.label)}: ${o.note}`).join("\n");
}

// subEffortTitle: the effort subagents start at, and unset, what that means:
// Codex runs one at the session's effort, or, given a model of its own, at
// that model's default; Claude Code's (#536) at the effort it asks for
function subEffortTitle(a, f, opt) {
  return t("{label}: {value}", { label: t(f.label), value: effortName(opt || { value: f.value }) }) +
    (f.value ? "" : "\n" + t(a.id === "claude" ? "the effort Claude Code asks for" : "the session's effort, or the subagent model's own default"));
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
  // Claude Desktop has no model of its own here: a tier unset runs on a
  // Claude model of that tier magpie serves, else on the chat's model
  const main = a.fields.find((f) => f.key === "model");
  const mainName = main && (optionFor(main, main.value)?.label || main.value);
  const unset = main ? t("same as model ({model})", { model: mainName }) : t("a Claude model of the tier, else the chat's model");
  // a tier's effort, offered while there are levels to pick (Claude Code
  // through magpie); unset, the tier runs at the effort Claude Code asks
  const effortOf = (f) => a.fields.find((e) => e.label === f.label + " effort" && (e.options.length || e.value));
  const level = (e) => e?.value ? effortName(optionFor(e, e.value) || { value: e.value }) : "";
  const custom = tiers.filter((f) => f.value || effortOf(f)?.value);
  const name = (f) => optionFor(f, f.value)?.label || f.value;
  return {
    key: "tiers", label: "tiers", value: "", menu: true, custom: custom.length > 0,
    summary: custom.length ? custom.map((f) => level(effortOf(f)) ? `${f.label} (${level(effortOf(f))})` : f.label).join(", ") : main ? t("same as model") : t("not set"),
    options: tiers.flatMap((f) => {
      const model = {
        value: f.key, label: f.label, icon: optionFor(f, f.value)?.icon || (main && optionFor(main, main.value)?.icon),
        note: f.value ? name(f) : unset,
      };
      const e = effortOf(f);
      return e ? [model, { value: e.key, label: t(e.label), effortOf: e, note: level(e) || t("the effort Claude Code asks for") }] : [model];
    }),
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
  const tallest = Math.max($("#agents").offsetHeight + extra, $("#panelQuota").offsetHeight, $("#panelRouting").offsetHeight, $("#panelUsage").offsetHeight);
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
      if (view === "sessions") window.loadSessionsPage?.();
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
  cliBehindOnce();
  whatsNewOnce();
}

// installFrom is what a restart to update tells the app: the window's tab,
// for the new version to open its window there (Windows, Linux).
function installFrom() {
  return mode === "window" ? { view } : {};
}

// updateBusy says what a restart would cut short (#577): the agents'
// requests in flight through the gateway, the Claude Code turns waiting on
// their tool results.
function updateBusyText(b) {
  const parts = [];
  if (b?.requests) parts.push(t("{n} in flight", { n: b.requests }));
  if (b?.tools) parts.push(t("{n} awaiting tool results", { n: b.tools }));
  return parts.join(" · ");
}

// updatePath asks for the update's state or the release notes in the
// page's language: the notes follow Settings' language, Chinese where a
// release has it (freecss on Discord).
function updatePath(p) {
  return p + (p.includes("?") ? "&" : "?") + "lang=" + encodeURIComponent(locale);
}

// renderUpdateBadge shows the header's Update pill once a newer magpie is
// downloaded (a click restarts into it) or, where magpie can't replace
// itself, out (a click opens the release page). The user may keep it away:
// for good (Settings), or for this version (its ×, or a right-click), until
// a newer one is out. magpie still downloads it and puts it in on quitting.
async function renderUpdateBadge() {
  const b = $("#update"), label = b.querySelector("span");
  const u = await api(updatePath("update")).catch(() => null);
  // pulling: a click is downloading it again, and restarts once it's in
  const pulling = !!u && !!b.dataset.pulling && ["checking", "downloading", "ready"].includes(u.state);
  if (b.dataset.pulling && !pulling) {
    delete b.dataset.pulling;
    b.classList.remove("busy");
  }
  const on = pulling || (!!u && !updatePillOff(u.latest) && (u.state === "ready" || u.state === "available" || (u.state === "error" && u.retry)));
  if (b.hidden !== !on) b.hidden = !on;
  if (!on) return;
  const hide = $("#updateHide");
  hide.setAttribute("aria-label", t("Hide until the next version"));
  hide.onclick = b.oncontextmenu = (e) => {
    e.preventDefault();
    skipUpdate(u.latest);
  };
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
      if (a.waiting) b.dataset.armed = String(Date.now());
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
  // a restart waiting for the gateway to be idle (#577): a click restarts
  // at once; one just after the click that armed it is taken as a double
  // click, not as "now"
  if (u.state === "ready" && u.waiting) {
    label.textContent = t("Waiting to update");
    b.title = t("magpie restarts to update to {v} once the agents' requests through the gateway have finished: {busy}. Click to restart now.", { v: u.latest, busy: updateBusyText(u.busy) || "…" });
    b.onclick = () => {
      if (Date.now() - (+b.dataset.armed || 0) < 1500) return;
      b.classList.add("busy");
      label.textContent = t("Restarting…");
      api("update/install", { ...installFrom(), when: "now" }).then((a) => {
        if (!a) return backAsNew(u.current);
        b.classList.remove("busy");
        renderUpdateBadge();
      }, () => backAsNew(u.current));
    };
    clearTimeout(b.waitPoll);
    b.waitPoll = setTimeout(() => {
      api(updatePath("update")).then(() => renderUpdateBadge(), () => backAsNew(u.current));
    }, 1500);
    return;
  }
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

// updatePillOff is whether the user keeps the Update pill away for v.
function updatePillOff(v) {
  const s = state?.settings || {};
  return !!s.noUpdatePill || (!!v && s.updateSkip === v);
}

// skipUpdate hides the Update pill until a version newer than v is out
// ("" shows it again), and says where it can be brought back.
async function skipUpdate(v) {
  $("#update").hidden = true;
  try {
    const ns = await writingPrefs(api("settings/update-skip", { version: v }));
    prefs = ns;
    if (state) state.settings = ns;
    if (v) status(t("Update hidden until the next version; Settings still has it"), "ok", 3000);
  } catch (e) {
    status(e.message, "err");
  }
  renderUpdateBadge();
  if (view === "settings" && prefs) renderSettings();
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

// ---------- what's new ----------
// After an update the window (or magpie web's page) shows what changed in
// every release since the one last run, once (a Discord user: to see if
// their issue was fixed). Not in the tray's panel, where a dialog has no
// room; Settings' version row opens it again, and with an update waiting,
// that one's notes first.
const ISSUES = "https://github.com/yetone/magpie/issues/";
let whatsNewAsked = false;
// cliBehindOnce: the terminal's magpie is a copied file rather than the
// installer's link, so it can't follow the app's updates (#531's lesson: a
// stale command wedged the WebDAV sync). The window can't tell a stale copy
// from a current one — that would mean running the old binary, whose
// start-up migrates settings — so this is advice, shown once per load, only
// in the window (the panel is too small), and dismissable until the next
// version.
let cliBehindAsked = false;
function cliBehindOnce() {
  if (cliBehindAsked || mode === "panel" || !$("#modal").hidden) return; // another dialog owns the screen: the advice waits for the next load
  cliBehindAsked = true;
  if (!state?.cliBehind) return;
  const ed = el("div", "editor");
  const head = el("div", "ehead");
  head.append(el("b", "", t("The `magpie` command is a copy")));
  ed.append(head);
  ed.append(el("p", "lib-confirm", t("The `magpie` command at {path} is a copied file, not the installer's link to the app: it won't follow the app's updates, and an old copy can break what a new one fixed. Re-run the installer, or link it by hand.", { path: state.cliBehind })));
  const bar = el("div", "bar");
  const quiet = el("button", "text", t("Hide until the next version"));
  quiet.onclick = async (e) => {
    e.stopPropagation();
    try { await api("cli-behind/quiet", {}); } catch (err) { return status(err.message, "err"); }
    closeConfirmAsk();
  };
  const ok = el("button", "text primary", t("OK"));
  ok.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
  bar.append(el("span", "grow"), quiet, ok);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  ok.focus({ preventScroll: true });
}
async function whatsNewOnce() {
  if (whatsNewAsked || mode === "panel" || document.hidden || !$("#modal").hidden) return;
  whatsNewAsked = true;
  const w = await api(updatePath("whatsnew")).catch(() => null);
  // something else opened meanwhile: the notes wait for the next load
  if (!w?.show || !w.releases?.length || !$("#modal").hidden) { if (w?.show) whatsNewAsked = false; return; }
  api("whatsnew/seen", {}).catch(() => {});
  showWhatsNew(w.releases, true);
}

// openWhatsNew is Settings' way back to the notes: the current version's (or
// those since the last update), after the waiting update's when u has one.
// The notes that couldn't be had (the site failing, #661) say so, with a
// retry and the release page, apart from a version that has none.
async function openWhatsNew(u, b) {
  if (b) { b.disabled = true; b.classList.add("busy"); }
  const w = await api(updatePath("whatsnew?all=1")).catch(() => null);
  if (b) { b.disabled = false; b.classList.remove("busy"); }
  const list = [...(w?.releases || [])];
  if (u?.notes && u.latest && ["ready", "available", "downloading"].includes(u.state) && !list.some((r) => r.version === u.latest)) {
    list.unshift({ version: u.latest, notes: u.notes, url: u.url, pending: true });
  }
  const failed = !w || !!w.error;
  if (!list.length && !w?.current) return status(t("Couldn't load the release notes"), "err");
  showWhatsNew(list, false, { failed, current: w?.current, url: w?.url, retry: () => openWhatsNew(u) });
}

// auto: shown by itself after an update, with "Don't show again today"
// (#525: releases come many a day); Settings' row opens it without one.
// more (from Settings): failed, the current version's notes couldn't be
// had, retry asks again; current and url, the version and its release
// page, for a dialog with no notes.
function showWhatsNew(releases, auto, more = {}) {
  const ed = el("div", "editor whatsnew");
  const head = el("div", "ehead");
  head.append(el("b", "", t("What's new in {v}", { v: "v" + (releases[0]?.version || more.current) })));
  ed.append(head);
  for (const r of releases) {
    const sec = el("section", "wn-rel");
    const h = el("div", "wn-ver");
    h.append(el("b", "", "v" + r.version));
    if (r.pending) h.append(el("span", "badge", t("Not installed yet")));
    sec.append(h, noteBlocks(r.notes));
    ed.append(sec);
  }
  if (more.failed || !releases.length) {
    const sec = el("section", "wn-rel wn-none" + (more.failed ? " wn-failed" : ""));
    const h = el("div", "wn-ver");
    if (more.current) h.append(el("b", "", "v" + more.current));
    const p = el("p", "wn-msg", more.failed ? t("Couldn't load the release notes") : t("No release notes provided."));
    const acts = el("div", "wn-acts");
    if (more.failed && more.retry) {
      const again = el("button", "text", t("Try again"));
      again.onclick = async (e) => {
        e.stopPropagation();
        again.disabled = true;
        again.classList.add("busy");
        await more.retry();
      };
      acts.append(again);
    }
    if (more.url) acts.append(noteLink(t("Open the release page"), more.url));
    sec.append(h, p, acts);
    ed.append(sec);
  }
  const bar = el("div", "bar");
  const ok = el("button", "text primary", t("Close"));
  ok.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
  if (auto) {
    // told at once, so Escape or a click outside keeps it as well as Close
    const [quiet, box] = tick(t("Don't show again today"), false);
    quiet.classList.add("wn-quiet");
    box.onchange = () => api("whatsnew/today", { on: box.checked }).catch((e) => status(e.message, "err"));
    bar.append(quiet);
  }
  bar.append(el("span", "grow"), ok);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  ok.focus({ preventScroll: true });
}

// noteBlocks draws a release's markdown as text: headings, bullets,
// paragraphs, and inline bold, code and links. Nothing in it is taken as
// HTML; #123 links the issue.
function noteBlocks(md) {
  const box = el("div", "wn-notes");
  let list = null;
  for (const line of String(md || "").split(/\r?\n/)) {
    const h = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    const li = /^\s*[-*+]\s+(.*)$/.exec(line);
    if (h) {
      list = null;
      box.append(inlineMD(el("div", "wn-h wn-h" + Math.min(h[1].length, 4)), h[2]));
    } else if (li) {
      if (!list) box.append(list = el("ul"));
      list.append(inlineMD(el("li"), li[1]));
    } else if (!line.trim()) {
      list = null;
    } else if (list && /^\s{2,}\S/.test(line)) {
      inlineMD(list.lastElementChild, " " + line.trim());
    } else {
      list = null;
      box.append(inlineMD(el("p"), line.trim()));
    }
  }
  return box;
}

// inlineMD appends text to e with **bold**, `code`, [links](https://…),
// bare https:// links and #123 issue links; only http(s) addresses link.
function inlineMD(e, s) {
  const re = /\*\*(.+?)\*\*|`([^`]+)`|\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|(https?:\/\/[^\s<>)]+)|(^|[\s(\[])#(\d+)\b/g;
  let at = 0, m;
  while ((m = re.exec(s))) {
    if (m.index > at) e.append(s.slice(at, m.index));
    if (m[1] !== undefined) e.append(inlineMD(el("b"), m[1]));
    else if (m[2] !== undefined) e.append(el("code", "", m[2]));
    else if (m[3] !== undefined) e.append(noteLink(m[3], m[4]));
    else if (m[5] !== undefined) e.append(noteLink(m[5], m[5]));
    else e.append(m[6], noteLink("#" + m[7], ISSUES + m[7]));
    at = re.lastIndex;
  }
  if (at < s.length) e.append(s.slice(at));
  return e;
}

// a link in the notes opens in the browser, as the app's other links do
function noteLink(text, url) {
  const a = el("a", "wn-link", text);
  a.href = url;
  a.target = "_blank";
  a.rel = "noopener";
  a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); api("open", { url }).catch(() => {}); };
  return a;
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
  w = Math.min(w, innerWidth - pad * 2);
  h = Math.min(h, innerHeight - pad * 2);
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

// ultracodeToggle: Claude Code's ultracode as a square that a click turns
// on or off, lit while on; nothing to pick between. What it does, and that
// an open session keeps what it started with, is said as it changes.
function ultracodeToggle(a, f) {
  const on = f.value === "on";
  const b = el("button", "field extra" + (on ? " set" : ""));
  b.append(svg(EXTRA_GLYPH.ultracode, 13, 1.5));
  b.title = t("{label}: {value}", { label: "ultracode", value: t(on ? "on" : "off") }) + "\n" + t("Claude plans a workflow for each substantive task");
  b.setAttribute("aria-label", b.title);
  b.setAttribute("aria-pressed", String(on));
  b.dataset.key = f.key;
  b.onclick = async (ev) => {
    ev.stopPropagation();
    try {
      state = await api("set", { agent: a.id, field: f.key, value: on ? "" : "on" });
      renderAgents();
      const msg = t("{agent} ultracode → {value}", { agent: a.name, value: t(on ? "off" : "on") });
      if (state.notice) status(`${msg}. ${t(state.notice)}`, "warn", 9000);
      else status(msg, "ok");
    } catch (e) { status(e.message, "err"); }
  };
  return b;
}

// oneRowPerModel: an alias and its dated id (claude-opus-4-5 and
// claude-opus-4-5-20251101, models.dev's two names of one model) are one
// row, the alias's; the dated one's while it is the value set, or starred
// where the alias isn't, so a pick or a star made before still shows (#496).
function oneRowPerModel(options, cur) {
  const byValue = new Map(options.map((o) => [o.value, o]));
  const gone = new Set();
  for (const o of options) {
    const alias = o.alias && byValue.get(o.alias);
    if (alias) gone.add(o.value === cur || (alias.value !== cur && isFavorite(o) && !isFavorite(alias)) ? alias : o);
  }
  return options.filter((o) => !gone.has(o));
}

// foldSame: magpie's rows for the very account the agent is signed in to
// itself (Claude Code's Claude subscription, added in magpie too) are the
// agent's own models a second time; they fold into one row that a click
// opens, for the few who want them (failover to more accounts, a fix that
// only magpie's way has). The value set stays a row of its own (#496). The
// row comes after the agent's own rows of its group: the account is listed
// after the providers the user added, and in its place the group was
// headed twice. A query shows every row it finds, for Enter to pick.
function foldSame(items) {
  if (!pick.modelPicker || pick.groupFilter === "favorites" || $("#q").value.trim()) return items;
  const same = items.filter((o) => o.same && o.value !== pick.field.value);
  if (!same.length) return items;
  const first = same[0];
  const row = { fold: true, open: !!pick.unfold, n: same.length, group: first.group, icon: first.icon,
    account: (first.note || "").replace(/ · via magpie$/, "") };
  const out = items.filter((o) => !same.includes(o));
  const own = out.findLastIndex((o) => o.group === first.group);
  out.splice(own >= 0 ? own + 1 : items.indexOf(first), 0, row, ...(pick.unfold ? same : []));
  return out;
}

// toggleFold opens or closes the folded rows, the fold's row staying under
// the pointer (or the keys' cursor) where it was
function toggleFold() {
  pick.unfold = !pick.unfold;
  filter((o) => o.fold);
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
  const effortPicker = !only && (field.key === "effort" || field.label === "effort" || field.label === "thinking" || field.label === SUB_EFFORT || TIER_EFFORTS.includes(field.label));
  options = oneRowPerModel(options, cur);
  // Current model first, then the rest in catalog order. Effort levels keep
  // their natural low → high order because their position is meaningful.
  const i = options.findIndex((o) => o.value === cur);
  if (!effortPicker && !field.menu && i > 0) { const [c] = options.splice(i, 1); options.unshift({ ...c, group: "" }); }
  else if (i < 0 && cur && !only) options.unshift({ value: cur, note: t("current value") });
  // the agent's own default: magpie's wiring comes out and the key is removed
  if (FOLLOWS_MODEL.includes(field.label)) {
    const main = agent.fields.find((f) => f.key === "model");
    options.unshift(main ? { value: "", label: t("Same as model"), note: optionFor(main, main.value)?.label || main.value, icon: optionFor(main, main.value)?.icon, reset: true }
      : { value: "", label: t("Not set"), note: t("a Claude model of the tier, else the chat's model"), icon: agent.icon, reset: true });
  } else if (!only && !field.menu && !field.onPick && !options.some((o) => o.value === "")) options.unshift({ value: "", label: t("Default"), note: t("what {agent} ships with", { agent: agent.name }), icon: agent.icon, reset: true });
  // Default is the agent as installed; this is the agent as it was before
  // magpie, beside it so the two aren't taken for each other
  if (agent.wired && field.key === "model" && !only && !field.menu && !field.onPick) {
    const at = options.findIndex((o) => !o.reset);
    options.splice(at < 0 ? options.length : at, 0, { value: "\0disconnect", label: t("Disconnect from magpie"), note: t("put back what {agent} had before magpie", { agent: agent.name }), svg: UNPLUG, reset: true, run: () => askDisconnect(agent) });
  }
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
  // On touch screens leave the keyboard closed until the filter is tapped,
  // so opening a model list leaves room to browse its choices.
  if (!web || !matchMedia("(pointer: coarse)").matches) q.focus();
}

function effortName(option) {
  if (!option?.value) return t("default");
  return t(option.label || option.value);
}

// The thinking levels agents name, lowest first; off and none are no
// thinking at all.
const EFFORT_ORDER = ["off", "none", "minimal", "low", "medium", "high", "xhigh", "max"];

// effortStops are an effort field's options with its value among them when
// it isn't one, as the panel's slider has its stops: a level in its place
// (max above high), anything else — unset, auto, one magpie doesn't know —
// first.
function effortStops(f) {
  const options = f?.options || [];
  if (!f || options.some((o) => o.value === f.value)) return options;
  const r = EFFORT_ORDER.indexOf(f.value);
  const i = r < 0 ? 0 : options.findIndex((o) => EFFORT_ORDER.indexOf(o.value) > r);
  const cut = i < 0 ? options.length : i;
  return [...options.slice(0, cut), { value: f.value }, ...options.slice(cut)];
}

// effortLit: how many of n bars an effort lights, as far as its stop goes
// among the levels: none for the default, off, none, auto or a value that
// isn't a level, all n for the highest.
function effortLit(f, n) {
  const levels = effortStops(f).filter((o) => o.value && !["off", "none", "auto"].includes(o.value) &&
    (EFFORT_ORDER.includes(o.value) || f.options?.includes(o)));
  const at = levels.findIndex((o) => o.value === f.value);
  return at < 0 ? 0 : Math.max(1, Math.round(((at + 1) / levels.length) * n));
}

// An effort level as four bars filled up to it.
function effortIcon(f) {
  const lit = effortLit(f, 4);
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
  const e = el("span", "eff");
  e.dataset.l = effortLit(f, 3);
  e.append(el("i"), el("i"), el("i"));
  if (f) e.title = t("{label}: {value}", { label: t(f.label), value: effortName(optionFor(f, f.value) || { value: f.value }) });
  return e;
}

// effortSeg: an opened panel row's effort, as the effort picker's slider,
// drawn rather than native so the thumb glides from stop to stop. A level
// shows at once, bars and all; the write follows, in order, so a slow agent
// config never holds the thumb (巨卡).
function effortSeg(a, f) {
  // a value that isn't one of the levels offered is a stop of its own, as
  // the picker's current value is (shown as the lowest level, a touch wrote
  // that level over it)
  const options = effortStops(f);
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
      effortSaid(a, f, o);
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

// effortSaid: an effort set, and what the agent says of it, such as that an
// open Claude Code session keeps the level it started with.
function effortSaid(a, f, o) {
  const msg = `${a.name} ${t(f.label)} → ${effortName(o)}`;
  if (state.notice) status(`${msg}. ${t(state.notice)}`, "warn", 9000);
  else status(msg, "ok");
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
    // a square (the subagents' effort) says it in its title, lit while set
    if (opened.field.label === SUB_EFFORT) {
      opened.anchor.classList.toggle("set", !!option.value);
      opened.anchor.title = subEffortTitle(opened.agent, opened.field, option);
      opened.anchor.setAttribute("aria-label", opened.anchor.title);
    }
    // a tier's effort, opened from the tiers' square: that square's title
    // and light follow the level
    if (TIER_EFFORTS.includes(opened.field.label)) {
      const menu = tierMenu(opened.agent);
      if (menu) {
        opened.anchor.classList.toggle("set", menu.custom);
        opened.anchor.title = menuTitle(menu);
        opened.anchor.setAttribute("aria-label", opened.anchor.title);
      }
    }
    // Persist every settled slider value, but keep the compact control open so
    // the user can compare adjacent levels. Queue writes to preserve ordering
    // when keyboard input changes several stops quickly.
    opened.effortSave = (opened.effortSave || Promise.resolve()).then(async () => {
      const next = await api("set", { agent: opened.agent.id, field: opened.field.key, value: option.value });
      state = next;
      effortSaid(opened.agent, opened.field, option);
    }).catch((e) => status(e.message, "err"));
  };
  range.onkeydown = (ev) => {
    if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); closePicker(); }
  };
  update();
}

// filter lists the options the query finds; keep: the row for the cursor,
// the first by default
function filter(keep) {
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
  pick.items = foldSame(pick.items);
  // a model the filter finds among those kept for routing groups, which
  // aren't offered: said why, rather than missing without a word
  pick.kept = pick.modelPicker && q ? (state?.unlisted || []).filter((m) => [m.id, m.name].some((s) => s?.toLowerCase().includes(q))).slice(0, 3) : [];
  pick.cursor = typeof keep === "function" ? Math.max(0, pick.items.findIndex(keep)) : 0;
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

// rateTag: the plan's credits a request to a model costs, as its vendor
// lists them (Qoder's 0.5×, WorkBuddy's x0.03), small and grey by it, so a
// cheap one is picked without opening the vendor's app (01huadalang on
// Discord); a discount's price before it struck through, as Qoder's
// client shows it (a free one's beside its FREE)
function rateTag(rate = 0, was = 0) {
  if (!(rate > 0) && !(was > rate)) return null;
  const times = (n) => (n < 0.01 ? "<0.01" : String(+n.toFixed(2))) + "×";
  const tag = el("span", "badge rate");
  if (was > rate) tag.append(el("s", "", times(was)));
  if (rate > 0) tag.append(el("span", "", times(rate)));
  tag.title = rate > 0
    ? (was > rate ? t("costs {rate} credits a request, {was} before the discount", { rate: times(rate), was: times(was) }) : t("costs {rate} credits a request", { rate: times(rate) }))
    : t("free now, {was} before the discount", { was: times(was) });
  return tag;
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
  if (!pick.items.length) { list.append(el("div", "none", t("No matches."))); for (const m of pick.kept || []) list.append(keptNote(m)); return; }
  const hasIcons = pick.items.some((o) => o.icon || o.effortOf);
  const q = $("#q").value.trim();
  let group = null;
  pick.items.forEach((o, idx) => {
    if (!q && o.group && o.group !== group) list.append(el("li", "group", o.group === ROUTING_GROUPS ? t(o.group) : o.group));
    if (!q) group = o.group ?? group;
    if (o.fold) { list.append(foldRow(o, idx, hasIcons)); return; }
    const li = el("li", (idx === pick.cursor ? "sel" : "") + (o.value === pick.field.value ? " cur" : "") + (o.custom ? " custom" : "") + (o.reset ? " reset" : ""));
    li.dataset.i = idx;
    if (hasIcons) li.append(optionIcon(o));
    const words = el("span", "option-words");
    // a choice of magpie's own (Codex's sign-in) reads in the page's language
    const own = pick.field.label === "sign-in";
    words.append(el("span", "v", own ? t(o.label || o.value) : o.label || o.value));
    if (o.free || (pick.modelPicker && o.value && namedFree(o.value, o.label))) words.append(freeBadge(o.free));
    const rate = rateTag(o.rate, o.rateWas);
    if (rate) words.append(rate);
    const ctx = contextTag(o.context, o.label);
    if (ctx) words.append(ctx);
    let note = o.note && o.note !== (o.label || o.value) ? (own ? t(o.note) : o.note) : "";
    if (q && o.group && !note) note = o.group;
    if (note) words.append(el("span", "n", note));
    li.append(words);
    if (o.run) li.title = t(DISCONNECT_TIP, { agent: pick.agent.name });
    if (pick.modelPicker && o.value && !o.custom && !o.run) {
      const star = el("button", "favorite" + (isFavorite(o) ? " on" : ""));
      star.title = isFavorite(o) ? t("Remove from favorites") : t("Add to favorites");
      star.setAttribute("aria-pressed", String(isFavorite(o)));
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
  for (const m of pick.kept || []) list.append(keptNote(m));
  list.querySelector(`li[data-i="${pick.cursor}"]`)?.scrollIntoView({ block: "nearest" });
}

// foldRow: the one row the models on the agent's own account through
// magpie fold into (foldSame), opened or closed at a click
function foldRow(o, idx, hasIcons) {
  const li = el("li", "fold" + (o.open ? " open" : "") + (idx === pick.cursor ? " sel" : ""));
  li.dataset.i = idx;
  li.setAttribute("role", "button");
  li.setAttribute("aria-expanded", String(o.open));
  if (hasIcons) li.append(optionIcon(o));
  const words = el("span", "option-words");
  words.append(el("span", "v", t(o.n === 1 ? "{n} model via magpie" : "{n} models via magpie", { n: o.n })));
  words.append(el("span", "n", o.account ? t("{account} · the account {agent} is signed in to", { account: o.account, agent: pick.agent.name }) : t("the account {agent} is signed in to", { agent: pick.agent.name })));
  li.append(words);
  const chev = el("span", "chev");
  chev.append(svg("m6 4 4 4-4 4", 12, 1.6));
  li.append(chev);
  li.title = t(o.open ? "Fold them away again" : "The same models as {agent}'s own, through magpie: click to show them", { agent: pick.agent.name });
  li.onmousemove = () => { if (pick.cursor !== idx) { pick.cursor = idx; renderList(); } };
  // the filter keeps the keys: the picker stays open, and the arrows,
  // Enter and Esc go on working
  li.onmousedown = (ev) => ev.preventDefault();
  li.onclick = (ev) => { ev.stopPropagation(); toggleFold(); };
  return li;
}

// keptNote: a model of a provider set to "Only through routing groups",
// found by the picker's filter. It isn't offered: in a group, that group
// is, picked here at a click; in none, nothing uses it, and a click makes
// a group of it.
function keptNote(m) {
  const li = el("li", "kept");
  li.append(icon(m.icon || "generic"));
  const words = el("span", "kept-words");
  const model = m.name || m.id;
  words.append(el("b", "", model));
  const via = m.groups.map((g) => pick.options.find((o) => o.ref === g && o.group === ROUTING_GROUPS)).filter(Boolean);
  const act = el("button", "text action");
  if (via.length) {
    words.append(el("span", "", t("{provider} is used only through routing groups: agents reach {model} by picking {group}.", { provider: m.provider, model, group: via.map((o) => o.label || o.value).join(", ") })));
    act.textContent = t("Pick {group}", { group: via[0].label || via[0].value });
    act.onclick = (ev) => { ev.stopPropagation(); commit(via[0].value); };
  } else {
    words.append(el("span", "", t("{provider} is used only through routing groups, and {model} is in none, so no agent can use it. Make a group of it, or untick “Only through routing groups” in {provider}'s models.", { provider: m.provider, model })));
    act.textContent = t("Make a routing group of it");
    act.onclick = (ev) => {
      ev.stopPropagation();
      closePicker();
      if (mode !== "window") api("window/main?view=routing&newgroup=" + encodeURIComponent(m.id), {}).catch((e) => status(e.message, "err"));
      else window.newGroupWith?.(m.id, m.name, ev);
    };
  }
  li.append(words, act);
  return li;
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
  if (opt?.run) return opt.run(); // an act rather than a value (Disconnect from magpie)
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
    if (state.notice) status(`${agent.name} → ${shown}. ${t(state.notice)}`, "warn", 9000);
    else if (opt?.direct) status(`${agent.name} ${t(field.label)} → ${shown} · ${t("straight to {vendor}, not through magpie", { vendor: opt.direct })}`, "ok", 6000);
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
  else if (e.key === "Enter") { e.preventDefault(); if (pick?.items[pick.cursor]?.fold) toggleFold(); else commit(pick?.items[pick.cursor]?.value); }
  else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closePicker(); }
});
document.addEventListener("mousedown", (e) => {
    if (pick && !$("#pop").contains(e.target) && !pick.anchor.contains(e.target)) closePicker();
});
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || pick) return;
  if (editing !== null || importingApps) cancelEdit();
  else if (adding && !$("#view-providers").hidden && providers.providers.length) closeAddSheet();
  else if (profileEscape()) e.preventDefault();
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
      // the details close, renderAgents having drawn them again (#489:
      // profileOpen dropped first, closeProfileDetail found nothing open and
      // left them there, their × doing nothing)
      closeProfileDetail();
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
  // a providers.json that can't be read is not a first use: no Add sheet
  if (!providers.providers.length && editing === null && !providers.fileError) adding = true;
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
  renderArchive();
  providersWhileFetching();
}

// Accounts' lists still on their way from their vendors (#541: the page no
// longer waits for them): asked again every few seconds until they are in,
// and drawn again only when something changed — never under an open editor.
function providersWhileFetching() {
  clearTimeout(providersWhileFetching.timer);
  if (!providers?.fetching) return;
  providersWhileFetching.timer = setTimeout(async () => {
    if (!["providers", "gateway", "routing"].includes(view) || editing !== null || adding || document.hidden) return;
    let next;
    try { next = await api("providers"); } catch { return; }
    const json = (p) => JSON.stringify({ ...p, fetching: false, gateway: { ...p.gateway, calls: 0 } });
    if (json(next) !== json(providers)) await loadProviders().catch(() => {});
    else { providers.fetching = next.fetching; providersWhileFetching(); }
  }, 2500);
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
  $("#offHead").hidden = $("#offProviders").hidden = true;
  $("#excluded").replaceChildren();
}

// Providers switched off are kept apart, below the ones on, under a
// "Turned off (N)" fold that starts folded: among the rest they took the
// room of the ones in use (01huadalang on Discord). The fold is
// remembered, per viewer.
let offFolded = true;
try { offFolded = localStorage.getItem("magpie.offProvidersOpen") !== "1"; } catch {}
$("#foldOff").prepend(svg(CHEV_R, 11, 1.6));
$("#foldOff").onclick = () => {
  offFolded = !offFolded;
  try { localStorage.setItem("magpie.offProvidersOpen", offFolded ? "0" : "1"); } catch {}
  renderProviders();
};

// One row per provider: logo, name, the agents pointed at it, key status.
// Everything else lives in the editor, a dialog over the page.
function renderProviders() {
  if (accountArranging || providerArranging) { accountRenderPending = true; return; }
  // Rebuilding the list empties the page for a moment, which clamps its
  // scroll to the top; put it back so closing the editor leaves the reader
  // where they were.
  const view = $("#view-providers"), top = view.scrollTop;
  keepIcons($("#providers"), $("#offProviders"), $("#addSheet"), $("#excluded"));
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  closeProtoMenu();
  syncURL();
  const onList = $("#providers"), offList = $("#offProviders");
  onList.replaceChildren();
  offList.replaceChildren();
  const offs = providers.providers.filter((p) => p.off).length;
  onList.hidden = offs === providers.providers.length;
  $("#offHead").hidden = !offs;
  offList.hidden = !offs || offFolded;
  const fold = $("#foldOff");
  fold.setAttribute("aria-expanded", String(!offFolded));
  fold.title = t(offFolded ? "Show the providers turned off" : "Fold the providers turned off away");
  $("#offCount").textContent = offs ? String(offs) : "";
  let dialog = null; // the editor, if one is open
  for (const p of providers.providers) {
    const list = p.off ? offList : onList;
    const open = editing === p.id;
    const row = el("div", "row provider" + (open ? " selected" : "") + (p.off ? " off" : ""));
    row.dataset.id = p.id;
    const who = el("div", "who");
    const name = el("div", "name", p.name);
    if (p.sponsored) name.append(el("span", "badge", t("sponsored")));
    if (p.off) name.append(el("span", "badge off", t("Switched off")));
    if (p.move && p.move.state !== "plugin") name.append(deprecatedBadge());
    const n = p.models.filter((m) => m.on).length;
    // a provider that only draws images (Settings → Image generation) says so
    const models = n ? t(n === 1 ? "{n} model" : "{n} models", { n })
      : p.draws ? t(p.draws === 1 ? "{n} image model" : "{n} image models", { n: p.draws }) : t("no models exposed");
    // every account in use refused by its vendor: signed out, as the
    // accounts list says of each, not a green "signed in"
    const inUse = (p.account?.logins || []).filter((l) => l.on || l.active);
    const lapsed = inUse.length > 0 && inUse.every((l) => l.lapsed);
    who.append(name, el("div", "sub", (p.account ? t(lapsed ? "{user} is signed out" : "signed in as {user}", { user: p.account.user }) : p.host) + " · " + models));
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
    if (p.account && lapsed) {
      key = el("span", "key acct none", t("Signed out"));
      key.title = t("Signed out — add this account again to use it");
    } else if (p.account) {
      // the row names the vendor: a plan too long for the pill goes without it
      const plan = accountPlan(p.account), bare = plan.startsWith(p.name + " ") ? plan.slice(p.name.length + 1) : "";
      key = el("span", "key acct", bare && plan.length > 15 ? bare : plan);
      key.title = t("{agent} is signed in; its models are here for every other agent", { agent: p.account.agentName });
    } else {
      key = el("span", "key " + (p.key.set ? (keyPill(p) === p.key.masked ? "on" : "on acct") : p.ready ? "free" : "none"), p.key.set ? keyPill(p) : p.ready ? t("no key") : t("needs a key"));
      key.title = p.key.set ? t("API key {masked}", { masked: p.key.masked }) : p.ready ? t("Local servers need no key") : t("Open the row and paste an API key");
    }
    // every pill is one width so the dots line up down the list; a label too long for it ends in "…" and is said whole on hover
    const label = key.textContent;
    key.textContent = "";
    key.append(el("span", "", label));
    if (!key.title.includes(label)) key.title = (p.account && !lapsed ? accountPlan(p.account) : label) + " · " + key.title;
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    // a provider on is listed, and tried, in the order the user puts it in
    // (#499): its logo is the handle, as an agent row's is
    row.append(p.off ? icon(p.icon || "generic") : providerHandle(p, row), who, uses, key, providerSwitch(p), chev);
    row.onclick = () => { editing = open ? null : p.id; draft = null; renderProviders(); }; // the preset sheet stays as it is under the dialog
    list.append(row);
    if (open) dialog = renderEditor(p);
  }
  renderExcluded();
  renderFileError();
  renderMovable();
  dialog = renderAdd() || dialog;
  if (importing) dialog = renderImport(importing);
  if (importingApps) dialog = renderImportApps(importingApps);
  keptIcons = null;
  view.scrollTop = top; // first: a closing dialog folds into its row where it is
  if (dialog) openModal(dialog); else closeModal();
}

// providerHandle is an on row's logo, which is also its handle (#499):
// drag it to move the row among the ones on, Alt+↑/↓ moves it from the
// keyboard, and a click opens the row as a click anywhere on it does. The
// grip that says so is drawn in the row's margin on hover (app.css). The
// order is the one providers are tried in too: a model several serve goes
// to the higher one first.
let providerArranging = false;
function providerHandle(p, row) {
  const b = el("button", "ag-handle pv-handle");
  b.type = "button";
  b.setAttribute("aria-label", t("Arrange {agent}", { agent: p.name }));
  b.title = t("Drag to reorder — a model several providers serve goes to the higher one first · Alt+↑/↓ to move");
  b.append(icon(p.icon || "generic"));
  b.onkeydown = (e) => {
    if (!e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
    e.preventDefault();
    e.stopPropagation();
    const ids = providers.providers.filter((x) => !x.off).map((x) => x.id);
    arrangeProvider(p.id, ids.indexOf(p.id) + (e.key === "ArrowUp" ? -1 : 1), true);
  };
  // the click that ends a drag doesn't open the row
  b.addEventListener("click", (e) => { if (b.dataset.dragged) { e.stopPropagation(); e.preventDefault(); } });
  b.onpointerdown = (e) => {
    if (providerArranging) return;
    const list = $("#providers");
    providerArranging = dragRows(e, b, row, list, [...list.children].filter((r) => r.classList.contains("provider")),
      (to) => arrangeProvider(p.id, to), closeProtoMenu, () => {
        providerArranging = false;
        if (accountRenderPending && !accountArranging) { accountRenderPending = false; renderProviders(); }
      });
  };
  return b;
}

// arrangeProvider puts the provider at index `to` among the ones on; the
// ones switched off keep their places among the rest. The list is drawn in
// its new order at once and put back if the save fails.
async function arrangeProvider(id, to, keyboard) {
  const prev = providers;
  const on = prev.providers.filter((p) => !p.off).map((p) => p.id);
  const from = on.indexOf(id);
  if (from < 0 || to < 0 || to >= on.length || to === from) return;
  on.splice(to, 0, ...on.splice(from, 1));
  let k = 0;
  const order = prev.providers.map((p) => (p.off ? p.id : on[k++]));
  const byId = new Map(prev.providers.map((p) => [p.id, p]));
  const handle = () => $(`#providers .row.provider[data-id="${CSS.escape(id)}"] .pv-handle`);
  providers = { ...prev, providers: order.map((x) => byId.get(x)) };
  renderProviders();
  if (keyboard) handle()?.focus({ preventScroll: true });
  const seq = ++providerArrangeSeq;
  let next;
  try {
    next = await api("providers/arrange", { order });
    status(t("Provider order saved"), "ok");
  } catch (e) {
    next = prev;
    status(e.message, "err");
  }
  // a later move, made while this one was saved, has the last word
  if (seq !== providerArrangeSeq) return;
  const held = document.activeElement === handle();
  providers = next;
  renderProviders();
  if (held) handle()?.focus({ preventScroll: true });
}
let providerArrangeSeq = 0;

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

// renderFileError: over the list, that providers.json is there but can't
// be read, so the list is the signed-in accounts alone: magpie left the
// file as it is, and nothing is saved over it until it is fixed or moved.
function renderFileError() {
  const box = $("#fileError");
  if (!box) return;
  box.replaceChildren();
  box.hidden = !providers.fileError;
  if (box.hidden) return;
  const r = el("div", "signing file-error");
  r.setAttribute("role", "alert");
  r.append(el("span", "mark", "!"));
  const tt = el("span", "tt");
  tt.append(el("span", "n", t("Your providers file can't be read")),
    el("span", "s", t("magpie left it unchanged and lists no providers from it. Fix the file or move it aside, then reopen this page; until then, changes to providers are refused.")),
    el("span", "s", providers.fileError));
  r.append(tt);
  box.append(r);
}

// DEPRECATED_WHY: why a built-in subscription a community plugin can run
// is deprecated, said wherever it is marked so.
const DEPRECATED_WHY = "Some subscriptions are reached in ways their vendors' terms may not allow. So that magpie itself isn't banned over them, subscription providers are being decoupled from magpie and run by community plugins instead.";
// deprecatedSub: a built-in subscription with a plugin, not moved onto it
const deprecatedSub = (agent) => (providers?.movable || []).includes(agent) && !movedSub(agent);
// unusedSub: a deprecated built-in with no account in magpie, which the
// add sheet leaves to the Plugins page
const unusedSub = (agent) => deprecatedSub(agent) && !(providers?.providers || []).some((p) => p.account?.agent === agent);
// replacedSub: a deprecated built-in whose plugin is installed and serves
// it, under the plugin's own id (the built-in's with "-plugin") until moved;
// its accounts, if any, still work, and its plugin's Move brings them over
const replacedSub = (agent) => deprecatedSub(agent) && (providers?.plugins || []).some((x) => x.pid === agent);
function deprecatedBadge() {
  const b = el("span", "badge deprecated", t("Deprecated"));
  b.title = t(DEPRECATED_WHY);
  return b;
}

// renderMovable: over the list, the signed-in built-in subscriptions that
// are deprecated, why, and the way to their plugins: for one, its editor
// at its Runs on; for several, the Plugins tab, where each has its own Move
// (opening the first one's editor read as if the notice were about it
// alone). Hidden, it stays hidden until another one is signed in.
function renderMovable() {
  const box = $("#movable");
  if (!box) return;
  box.replaceChildren();
  const ps = providers.providers.filter((p) => p.move && p.move.state !== "plugin" && p.account);
  let hid = "";
  try { hid = localStorage.getItem("magpie.deprecatedHidden") || ""; } catch {}
  const ids = ps.map((p) => p.id).join(",");
  box.hidden = !ps.length || hid === ids;
  if (box.hidden) return;
  const names = ps.map((p) => p.name).join(t(", "));
  // one quiet line over the list, as calm as its rows: an amber dot, what
  // happened and why, and the two answers on the right (the owner: 这个太丑了 —
  // it was a tinted box with a tile, a badge, three lines and a purple button)
  const card = el("div", "deprecation");
  card.setAttribute("role", "note");
  const body = el("div", "dep-body");
  body.append(el("div", "dep-head", ps.length > 1 ? t("These built-in subscriptions are deprecated: {names}", { names }) : t("{name}'s built-in subscription is deprecated", { name: names })),
    el("p", "dep-why", [t(DEPRECATED_WHY), t("Moving keeps your accounts, models and agents as they are.")].join(locale === "zh" ? "" : " ")));
  const acts = el("div", "dep-acts");
  const hide = el("button", "text", t("Not now"));
  hide.title = t("Hide this until another deprecated subscription is signed in");
  hide.onclick = () => { try { localStorage.setItem("magpie.deprecatedHidden", ids); } catch {} renderMovable(); };
  const review = el("button", "text action", ps.length > 1 ? t("Review in Plugins") : t("Review the move"));
  // the editor opens over the list: the page itself doesn't move
  review.onclick = () => {
    if (ps.length > 1) { openPlugins(); return; }
    editing = ps[0].id; draft = null; renderProviders();
  };
  acts.append(hide, review);
  card.append(el("span", "dep-dot"), body, acts);
  box.append(card);
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
    closeConfirmAsk();
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  cancel.focus();
}

// accountPlan is the account's chip: its vendor and plan, the plan alone
// when it already names the vendor (a plugin's own "Zed Pro").
function accountPlan(a) {
  // a plugin beside a built-in (cursor-plugin) by the built-in's
  const agent = a.builtin || a.agent;
  const cap = (s) => s[0].toUpperCase() + s.slice(1);
  // "Xiaomi MiMo" and "MiMo 高阶" overlap in "MiMo": said once
  const named = (name, plan) => {
    if (!plan) return name;
    const w = name.split(" ");
    for (let i = 0; i < w.length; i++) {
      if ((plan.toLowerCase() + " ").startsWith(w.slice(i).join(" ").toLowerCase() + " ")) return [...w.slice(0, i), plan].join(" ");
    }
    return name + " " + plan;
  };
  if (agent === "codex") return named("ChatGPT", a.plan && cap(a.plan));
  if (agent === "copilot") return named("GitHub", a.plan && cap(a.plan));
  if (agent === "claude") return named("Claude", a.plan && cap(a.plan));
  if (agent === "cursor") return named("Cursor", a.plan && cap(a.plan));
  if (agent === "grok") return a.plan || "SuperGrok";
  if (agent === "gemini" || agent === "antigravity") return a.plan || "Google";
  if (agent === "zcode") return a.plan || "GLM Coding Plan";
  if (agent === "workbuddy") return a.plan || "WorkBuddy";
  if (agent === "workbuddy-ai") return a.plan || "WorkBuddy AI";
  if (agent === "commandcode-plan") return named("Command Code", a.plan && t(a.plan));
  if (agent === "qoder") return named("Qoder", a.plan && t(a.plan));
  if (agent === "qoder-cn") return named("Qoder CN", a.plan && t(a.plan));
  if (agent === "zed") return named("Zed", a.plan && t(a.plan));
  if (agent === "mimo-app") return named("Xiaomi MiMo", a.plan && t(a.plan));
  return a.plan || t("signed in");
}

// ---------- gateway view ----------
//
// The gateway is one local endpoint speaking four APIs; this tab is the
// page that gets anything else connected to it: base URL, key, model ids,
// and a snippet in whichever language the reader is holding.

// copy asks magpie to put text on the clipboard, as the page's own
// clipboard API is refused inside the app's window; a browser tab on the
// dev UI falls back to it. A tab on `magpie web` reached over plain http
// from another computer (a NAS's) has no clipboard API at all, so the
// older copy command is the last try there.
async function copy(text, what, btn, message, failure) {
  const done = () => { status(message || t("{what} copied", { what }), "ok"); flashCopied(btn); };
  const res = await fetch("/api/copy", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ text }) }).catch(() => null);
  if (res && res.ok) return done();
  try { await navigator.clipboard.writeText(text); return done(); } catch {}
  if (copyByCommand(text)) return done();
  status(failure || text, failure ? "err" : undefined);
}

// copyByCommand copies text with the page's copy command, through a
// textarea out of sight that is never scrolled to, the focus going back
// where it was.
function copyByCommand(text) {
  const was = document.activeElement;
  const box = document.createElement("textarea");
  box.value = text;
  box.setAttribute("readonly", "");
  box.style.cssText = "position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none";
  document.body.append(box);
  let ok = false;
  try {
    box.focus({ preventScroll: true });
    box.select();
    ok = document.execCommand("copy");
  } catch { ok = false; }
  box.remove();
  if (was && was.focus) was.focus({ preventScroll: true });
  return ok;
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
  $("#gatewayKeysBlock").hidden = !providers.gateway.lan;
  if (!providers.gateway.lan) gatewayKeyDraft = null;
  if (gatewayKeyDraft === null && gatewayLimit === null && !$("#gatewayKeys .rename-in")) renderGatewayKeys();
  renderConnect();
  renderGatewayModels();
  renderActivity();
}

// The status card: dot, state, the URL.
function renderGateway() {
  const g = providers.gateway;
  const box = $("#gateway");
  box.replaceChildren();
  // an older magpie keeping the port sends every agent's request its own
  // way, without this version's fixes (#506): said in place of the counts
  const older = g.running && !g.mine && g.older;
  const dot = el("span", "dot " + (older ? "old" : g.running ? "on" : ""));
  const who = el("div", "who");
  const name = el("div", "name", t("Gateway"));
  name.append(el("span", "state", older
    ? t("running · served by magpie {v}, older than this one", { v: g.version })
    : t(g.running ? (g.mine ? "running" : "running · served by another magpie") : "not running")));
  const routed = new Set();
  for (const p of providers.providers) for (const a of p.agents) if (a.current) routed.add(a.id);
  const n = routed.size;
  if (older) {
    who.append(name, el("div", "sub old", t("Agents' requests go through magpie {v} and are sent as it sends them, without this version's fixes. Quit that magpie (a magpie serve, another copy) and this one takes the gateway over within 15 seconds.", { v: g.version })));
  } else who.append(name, el("div", "sub", g.running
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
    curl: (b, m, k = "magpie") => ({ url: `${b}/chat/completions`, headers: [`Authorization: Bearer ${k}`],
      body: `{"model": "${m}",\n "messages": [{"role": "user", "content": "hi"}]}` }),
    python: (b, m, k = "magpie") => `from openai import OpenAI

client = OpenAI(base_url="${b}", api_key="${k}")
r = client.chat.completions.create(
    model="${m}",
    messages=[{"role": "user", "content": "hi"}],
)
print(r.choices[0].message.content)`,
    node: (b, m, k = "magpie") => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${b}", apiKey: "${k}" });
const r = await client.chat.completions.create({
  model: "${m}",
  messages: [{ role: "user", content: "hi" }],
});
console.log(r.choices[0].message.content);`,
  },
  responses: {
    name: "Responses", base: (u) => u + "/v1", baseEnv: "OPENAI_BASE_URL", keyEnv: "OPENAI_API_KEY",
    note: "OpenAI's newer API: reasoning, built-in tool items, encrypted reasoning. Codex speaks this.",
    curl: (b, m, k = "magpie") => ({ url: `${b}/responses`, headers: [`Authorization: Bearer ${k}`],
      body: `{"model": "${m}", "input": "hi"}` }),
    python: (b, m, k = "magpie") => `from openai import OpenAI

client = OpenAI(base_url="${b}", api_key="${k}")
r = client.responses.create(model="${m}", input="hi")
print(r.output_text)`,
    node: (b, m, k = "magpie") => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${b}", apiKey: "${k}" });
const r = await client.responses.create({ model: "${m}", input: "hi" });
console.log(r.output_text);`,
  },
  anthropic: {
    name: "Anthropic", base: (u) => u, baseEnv: "ANTHROPIC_BASE_URL", keyEnv: "ANTHROPIC_API_KEY",
    note: "Messages API. Claude Code reads ANTHROPIC_AUTH_TOKEN instead of the key; the Agents tab sets that for you.",
    curl: (b, m, k = "magpie") => ({ url: `${b}/v1/messages`, headers: [`x-api-key: ${k}`, "anthropic-version: 2023-06-01"],
      body: `{"model": "${m}", "max_tokens": 1024,\n "messages": [{"role": "user", "content": "hi"}]}` }),
    python: (b, m, k = "magpie") => `import anthropic

client = anthropic.Anthropic(
    base_url="${b}", api_key="${k}",
)
m = client.messages.create(
    model="${m}",
    max_tokens=1024,
    messages=[{"role": "user", "content": "hi"}],
)
print(m.content[0].text)`,
    node: (b, m, k = "magpie") => `import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({ baseURL: "${b}", apiKey: "${k}" });
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
    curl: (b, m, k = "magpie") => ({ url: `${b}/v1beta/models/${m}:generateContent`, headers: [`x-goog-api-key: ${k}`],
      body: `{"contents": [{"parts": [{"text": "hi"}]}]}` }),
    python: (b, m, k = "magpie") => `from google import genai

client = genai.Client(api_key="${k}", http_options={"base_url": "${b}"})
r = client.models.generate_content(model="${m}", contents="hi")
print(r.text)`,
    node: (b, m, k = "magpie") => `import { GoogleGenAI } from "@google/genai";

const ai = new GoogleGenAI({
  apiKey: "${k}",
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
    provider: { name: [t("routing group"), g.providers.join(", ")].filter(Boolean).join(" · ") },
    efforts: g.efforts || [], images: !!g.images, context: g.context, output: g.output, members: g.members || [] }));
  // a provider switched off is skipped: the gateway refuses its models, so
  // they are not ids an agent can use, and listing them here would show a
  // model the picker's own list, the Gateway count and the Connect example
  // all say works but a request to it turns away (the same off gate the
  // backend's providerEntries and the fallback picker already honour).
  // the levels agents are offered: those kept, else all it has; one whose
  // levels aren't known (given) offers only those it was given
  const offered = (m) => m.kept?.length || m.given ? m.kept || [] : m.efforts || [];
  for (const p of providers.providers) { if (p.off) continue; for (const m of p.models) if (m.on) out.push({ id: `${p.id}/${m.id}`, name: m.name, provider: p, context: m.context, efforts: offered(m), images: !!m.images }); }
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

// An installed list's order, the reader's pick, remembered for each list
// (#481): its names A→Z or Z→A, and for a list that has one, a view of its
// own first (the skills by where they came from). sortOf(list) is the pick,
// sortBy(list, …) the control; a list missing or unreadable in storage
// takes the first.
const NAME_SORTS = [["az", "A→Z"], ["za", "Z→A"]];
function sortOf(list, items = NAME_SORTS) {
  let v = "";
  try { v = localStorage.getItem("magpie.sort." + list) || ""; } catch {}
  return items.some(([id]) => id === v) ? v : items[0][0];
}
function sortBy(list, items, onPick) {
  const box = segs(items, sortOf(list, items), (id) => {
    try { localStorage.setItem("magpie.sort." + list, id); } catch {}
    onPick(id);
  });
  box.classList.add("sortby");
  box.dataset.sort = list;
  const tips = { az: t("Names from A to Z"), za: t("Names from Z to A") };
  for (const [i, b] of [...box.querySelectorAll(".opt")].entries()) if (tips[items[i][0]]) b.title = tips[items[i][0]];
  return box;
}
// a comparer of names for a pick: A→Z, or Z→A
const byName = (pick, nameOf = (x) => x.name) => (a, b) => (pick === "za" ? -1 : 1) * nameOf(a).localeCompare(nameOf(b));

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
  const urls = [g.url, ...(g.lanURLs || [])];
  if (!urls.includes(connectURL)) connectURL = g.url;
  const remote = connectURL !== g.url;
  const keys = g.lan ? (gatewayKeys || []).filter((k) => !k.off) : [];
  if (!keys.some((k) => k.id === connectKeyID)) {
    connectKeyID = "";
    connectSecret = null;
  }
  if (remote && !connectKeyID && keys.length) {
    queueMicrotask(() => selectConnectKey(keys[0].id));
  }
  const key = keys.find((k) => k.id === connectKeyID);
  const secret = key ? (connectSecret?.id === key.id ? connectSecret.secret : "") : remote ? "" : "magpie";
  const base = f.base(connectURL);
  const fold = $("#foldConnect");
  fold.setAttribute("aria-expanded", String(!connectFolded));
  fold.title = t(connectFolded ? "Show how to connect" : "Fold Connect away");
  box.hidden = connectFolded;
  // folded, the head keeps the one thing reached for: the address, to copy
  const note = $("#connectNote");
  note.replaceChildren();
  note.classList.toggle("brief", connectFolded);
  if (connectFolded) note.append(el("code", "", base), copyBtn(base, "Base URL"));
  else note.textContent = t(g.open ? "Open to the network · anyone who reaches it can use any key" : remote ? "Local network · an enabled gateway key is required" : g.lan && gatewayKeys?.length ? "Use a gateway key to track usage" : "Loopback only · the key can be anything");

  box.append(...field("API", segs(Object.entries(FLAVORS).map(([k, v]) => [k, v.name]), flavor, (id) => { flavor = id; localStorage.setItem("magpie.flavor", id); renderConnect(); }), t(f.note)));

  const b = el("div", "val");
  if (urls.length > 1) b.append(connectPick("connectAddress", "Address", base,
    urls.map((u) => ({ v: u, name: f.base(u), note: t(u === g.url ? "This computer" : "Local network") })), connectURL,
    (u) => { connectURL = u; renderConnect(); }));
  else b.append(el("code", "", base));
  b.append(copyBtn(base, "Base URL"));
  box.append(...field("Base URL", b, t("What {env} takes.", { env: f.baseEnv })));

  const k = el("div", "val");
  const options = keys.map((k) => ({ v: k.id, name: k.name, literalName: true, note: k.masked }));
  if (!remote) options.unshift({ v: "", name: "This computer", note: t("Any key · no key attribution") });
  const keyLabel = remote || key ? "Gateway key" : "API key";
  if (!g.lan) k.append(el("code", "", "magpie"));
  else if (options.length) k.append(connectPick("connectKey", keyLabel, key ? key.name : remote ? t("Choose a gateway key") : t("This computer"),
    options, connectKeyID, selectConnectKey));
  else k.append(el("code", "", t("Create a gateway key above to connect")));
  if (key) k.append(copyCallerKeyBtn(key));
  else if (!remote) k.append(copyBtn("magpie", t("Key")));
  box.append(...field(t(keyLabel), k, t(g.lan && (remote || gatewayKeys?.length)
    ? "Choose a gateway key to use as {env}; usage is tracked by key."
    : "{env}=magpie. The gateway trusts everything on loopback, so any value works.", { env: f.keyEnv })));

  const m = el("div", "val");
  m.append(el("code", "", model), copyBtn(model, t("Model id")));
  box.append(...field(t("Model"), m, t(models.length ? "provider/model, as listed below. Click a model there to put it in the snippets." : "No models yet. Add a provider, or sign in to Codex or Copilot.")));

  const ex = el("div", "stack");
  ex.append(segs(LANGS, lang, (id) => { lang = id; localStorage.setItem("magpie.lang", id); renderConnect(); }));
  const code = !secret ? t(key ? "Loading gateway key…" : "Create a gateway key above to connect")
    : lang === "shell" ? envSnippet([[f.baseEnv, base], [f.keyEnv, secret]])
    : lang === "curl" ? curlSnippet(f.curl(base, model, secret))
    : f[lang](base, model, secret);
  const pre = el("pre", "snip");
  const c = el("code");
  c.append(highlight(code, lang));
  pre.append(c);
  // the button sits outside the scrolling box, so a long line doesn't carry it off
  const wrap = el("div", "snip-wrap");
  wrap.append(pre);
  if (secret) wrap.append(copyBtn(code, t("Snippet")));
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
  q.hidden = all.length < 5 || list.hidden;
  q.title = t("Words match a model's id, name, provider or a group's models");
  const words = modelQuery.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const models = q.hidden ? all : all.filter((m) => {
    const hay = `${m.id} ${m.name || ""} ${m.provider.name} ${(m.members || []).join(" ")}`.toLowerCase();
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
    who.append(name, el("div", "sub", m.name && m.name !== m.id.split("/")[1] ? `${m.name} · ${m.provider.name}` : m.provider.name));
    row.append(m.group ? stackIcon(m.icons) : icon(m.provider.icon || "generic"), who, modelInfo(m), copyBtn(m.id, t("Model id")));
    row.title = t("Use this model in the snippets");
    row.onclick = () => { exampleModel = m.id; localStorage.setItem("magpie.model", m.id); renderConnect(); renderGatewayModels(); };
    list.append(row);
  }
}

// What agents are told of a model on the Gateway list (ARNO on Discord:
// 支持显示模型或者模型组更多的信息): its reasoning levels, whether it takes
// images and the context it holds, as small chips at the row's end, and in
// full in their tooltip; a group's are what its members all have, and the
// tooltip names its models.
const IMAGE_GLYPH = "M2.5 3.5h11v9h-11zM2.5 10.5l3.25-3.25 3 3 2-2 2.75 2.75M10.25 6.75a.75.75 0 1 0 0-.01";
function effortSpan(levels) {
  const say = levels.map((l) => t(l));
  return say.length > 2 ? `${say[0]}–${say[say.length - 1]}` : say.join(" / ");
}
function modelInfo(m) {
  const box = el("span", "minfo");
  const lines = [];
  const levels = m.efforts || [];
  if (levels.length) {
    box.append(el("span", "badge mi-effort", effortSpan(levels)));
    lines.push(t("Reasoning: {levels}", { levels: levels.map((l) => t(l)).join(", ") }));
  } else lines.push(t("Reasoning levels: none known"));
  if (m.images) {
    const c = el("span", "badge mi-img");
    c.append(svg(IMAGE_GLYPH, 11, 1.4));
    box.append(c);
  }
  lines.push(t(m.images ? (m.group ? "Accepts images (every model in it does)" : "Accepts images") : "Text only"));
  if (m.context) {
    box.append(el("span", "badge mi-ctx", ctxShort(m.context)));
    lines.push(t(m.group ? "Context: {n} tokens (the least of its models')" : "Context: {n} tokens", { n: m.context.toLocaleString() }));
  }
  if (m.output) lines.push(t("Output: up to {n} tokens", { n: m.output.toLocaleString() }));
  if (m.group && m.members?.length) lines.push(t("Models: {models}", { models: m.members.join(", ") }));
  box.title = lines.join("\n");
  return box;
}

function formatWireBody(raw) {
  if (!raw) return "";
  try { return JSON.stringify(JSON.parse(raw), null, 2); } catch { return raw; }
}

// A streamed body (server-sent events) as its events: each one's name and
// its data, the data lines joined. Anything else is not one: null.
function parseSSE(raw) {
  if (!raw || !/^\s*(?::[^\n]*\n\s*)*(event|data|id|retry):/.test(raw)) return null;
  const events = [];
  let ev = null;
  const done = () => { if (ev && ev.data.length) events.push({ event: ev.event, data: ev.data.join("\n") }); ev = null; };
  for (const line of raw.split(/\r?\n/)) {
    if (line === "") { done(); continue; }
    if (line[0] === ":") continue; // a comment, a keep-alive
    const i = line.indexOf(":");
    const field = i < 0 ? line : line.slice(0, i);
    let value = i < 0 ? "" : line.slice(i + 1);
    if (value[0] === " ") value = value.slice(1);
    ev = ev || { event: "", data: [] };
    if (field === "event") ev.event = value;
    else if (field === "data") ev.data.push(value);
  }
  done();
  return events.length ? events : null;
}

const jsonOr = (s) => { try { return JSON.parse(s); } catch { return undefined; } };

// What a stream adds up to — its text, reasoning and tool calls, how it
// stopped and what it used — for the Anthropic Messages, OpenAI Responses,
// Chat Completions and Gemini streams; null for one it can't read.
function sseReply(events) {
  const out = {};
  const blocks = [];      // Anthropic content blocks, Chat tool calls, Responses items, in order
  const byKey = new Map();
  const block = (key, init) => { let b = byKey.get(key); if (!b) { b = { ...init }; byKey.set(key, b); blocks.push(b); } return b; };
  let kind = "";
  for (const e of events) {
    const d = jsonOr(e.data);
    if (!d || typeof d !== "object") continue;
    const type = d.type || e.event || "";
    if (d.error) { out.error = d.error; continue; }
    if (type === "message_start" && d.message) {
      kind = "anthropic";
      out.id = d.message.id; out.model = d.message.model;
      if (d.message.usage) out.usage = { ...d.message.usage };
    } else if (type === "content_block_start" && d.content_block) {
      kind = "anthropic";
      const cb = d.content_block;
      const b = block("a" + d.index, { type: cb.type });
      if (cb.type === "tool_use" || cb.type === "server_tool_use") { b.id = cb.id; b.name = cb.name; b.input = ""; }
      else if (cb.type === "text") b.text = cb.text || "";
      else if (cb.type === "thinking") b.thinking = cb.thinking || "";
      else Object.assign(b, cb);
    } else if (type === "content_block_delta" && d.delta) {
      const b = block("a" + d.index, { type: "text" });
      const x = d.delta;
      if (x.type === "text_delta") b.text = (b.text || "") + x.text;
      else if (x.type === "thinking_delta") b.thinking = (b.thinking || "") + x.thinking;
      else if (x.type === "input_json_delta") b.input = (b.input || "") + x.partial_json;
    } else if (type === "message_delta") {
      if (d.delta?.stop_reason) out.stop_reason = d.delta.stop_reason;
      if (d.usage) out.usage = { ...out.usage, ...d.usage };
    } else if (type.startsWith("response.")) {
      kind = "responses";
      if (d.response) {
        out.id = d.response.id; out.model = d.response.model; out.status = d.response.status;
        if (d.response.usage) out.usage = d.response.usage;
        if (d.response.error) out.error = d.response.error;
        if (d.response.incomplete_details) out.incomplete = d.response.incomplete_details;
        if (type === "response.completed" && d.response.output?.length) out.output = d.response.output;
      }
      const key = "r" + (d.item_id || d.item?.id || d.output_index);
      if (type === "response.output_item.added" || type === "response.output_item.done") {
        const it = d.item || {};
        const b = block(key, { type: it.type });
        if (it.type === "function_call" || it.type === "custom_tool_call") { b.call_id = it.call_id; b.name = it.name; }
        if (type === "response.output_item.done") {
          if (it.arguments !== undefined) b.arguments = it.arguments;
          if (it.input !== undefined) b.input = it.input;
          const text = (it.content || []).map((c) => c.text || c.refusal || "").join("");
          if (text) b.text = text;
          const sum = (it.summary || []).map((c) => c.text || "").join("\n\n");
          if (sum) b.summary = sum;
        }
      } else if (type === "response.output_text.delta" || type === "response.refusal.delta") {
        const b = block(key, { type: "message" }); b.text = (b.text || "") + d.delta;
      } else if (type === "response.reasoning_summary_text.delta") {
        const b = block(key, { type: "reasoning" }); b.summary = (b.summary || "") + d.delta;
      } else if (type === "response.reasoning_text.delta") {
        const b = block(key, { type: "reasoning" }); b.text = (b.text || "") + d.delta;
      } else if (type === "response.function_call_arguments.delta") {
        const b = block(key, { type: "function_call" }); b.arguments = (b.arguments || "") + d.delta;
      } else if (type === "response.custom_tool_call_input.delta") {
        const b = block(key, { type: "custom_tool_call" }); b.input = (b.input || "") + d.delta;
      }
    } else if (Array.isArray(d.choices)) {
      kind = "chat";
      if (d.id) out.id = d.id;
      if (d.model) out.model = d.model;
      if (d.usage) out.usage = d.usage;
      for (const c of d.choices) {
        const x = c.delta || c.message || {};
        const msg = block("c" + (c.index || 0), { type: "message" });
        if (typeof x.content === "string") msg.content = (msg.content || "") + x.content;
        const r = x.reasoning_content ?? x.reasoning;
        if (typeof r === "string") msg.reasoning = (msg.reasoning || "") + r;
        for (const tc of x.tool_calls || []) {
          const b = block("c" + (c.index || 0) + ":" + (tc.index ?? tc.id), { type: "tool_call" });
          if (tc.id) b.id = tc.id;
          if (tc.function?.name) b.name = (b.name || "") + tc.function.name;
          if (tc.function?.arguments) b.arguments = (b.arguments || "") + tc.function.arguments;
        }
        if (c.finish_reason) out.finish_reason = c.finish_reason;
      }
    } else if (Array.isArray(d.candidates)) {
      kind = "gemini";
      if (d.modelVersion) out.model = d.modelVersion;
      if (d.usageMetadata) out.usage = d.usageMetadata;
      for (const c of d.candidates) {
        for (const p of c.content?.parts || []) {
          if (typeof p.text === "string") {
            const b = block("g" + (c.index || 0) + (p.thought ? "t" : ""), { type: p.thought ? "thought" : "text" });
            b.text = (b.text || "") + p.text;
          } else if (p.functionCall) blocks.push({ type: "functionCall", ...p.functionCall });
        }
        if (c.finishReason) out.finishReason = c.finishReason;
      }
    }
  }
  if (!kind && !out.error) return null;
  // the Responses API's own finished output is the reply, when it came
  const content = (out.output || blocks).filter((b) => !(kind === "chat" && b.type === "message" && !b.content && !b.reasoning));
  // a tool's arguments read as the object they spell, when they are whole
  for (const b of content) {
    for (const k of ["input", "arguments"]) {
      if (typeof b[k] !== "string") continue;
      const v = b[k] === "" && k === "input" ? {} : jsonOr(b[k]);
      if (v !== undefined) b[k] = v;
    }
  }
  const reply = {};
  for (const k of ["id", "model", "status"]) if (out[k] !== undefined) reply[k] = out[k];
  if (content.length) reply[{ anthropic: "content", responses: "output", gemini: "parts" }[kind] || "choices"] = content;
  for (const k of ["stop_reason", "finish_reason", "finishReason", "incomplete", "usage", "error"]) if (out[k] !== undefined) reply[k] = out[k];
  return reply;
}

// The data of each event, as JSON where it is JSON, under its name.
function sseEventNode(e) {
  const box = el("span", "sse-ev");
  if (e.event) box.append(el("span", "sse-name", "event: " + e.event + "\n"));
  box.append(sseData(e.data));
  return box;
}
function sseData(data) {
  const d = jsonOr(data);
  return d !== null && typeof d === "object" ? JSON.stringify(d, null, 2) : data; // [DONE] as it is
}
const SSE_PAGE = 200; // events drawn at a time: a long stream stays quick to open

function sseBodyPanel(label, raw, truncated, id) {
  const events = parseSSE(raw);
  if (!events) return null;
  const reply = sseReply(events);
  const panel = el("section", "call-body sse");
  const head = el("div", "call-body-head");
  head.append(el("span", "call-body-label", t(label)));
  if (truncated) head.append(el("span", "call-body-truncated", typeof truncated === "string" ? truncated : t("first 256 KB")));
  const views = [["reply", t("Reply")], ["events", t("Events")], ["raw", t("Raw")]].filter(([v]) => v !== "reply" || reply);
  let view = views.some(([v]) => v === sseView) ? sseView : "events";
  const count = el("span", "call-body-count", t(events.length === 1 ? "1 event" : "{n} events", { n: events.length }));
  const pick = segs(views, view, (v) => { view = sseView = v; try { localStorage.setItem("magpie.sseView", v); } catch {} draw(); });
  pick.classList.add("sse-views");
  const copyB = copyBtn("", t(label)); // what it copies is set below
  head.append(count, el("span", "grow"), pick, copyB);
  panel.append(head);
  const pre = el("pre");
  // a long stream's events come a page at a time; what draws more sits
  // under the box, where it stays as they come in rather than at the end
  // of what is drawn
  const foot = el("div", "call-body-foot");
  const shownNote = el("span");
  const moreB = el("button", "link", "");
  foot.append(shownNote, el("span", "grow"), moreB);
  panel.append(pre, foot);
  let code = null, shown = 0;
  const more = () => {
    const upto = Math.min(events.length, Math.max(sseShown.get(id) || 0, shown + SSE_PAGE));
    for (; shown < upto; shown++) code.append(sseEventNode(events[shown]));
    if (shown > SSE_PAGE) sseShown.set(id, shown);
    foot.hidden = view !== "events" || shown >= events.length;
    shownNote.textContent = t("{n} of {total} events shown", { n: shown, total: events.length });
    moreB.textContent = t("Show {n} more events", { n: Math.min(SSE_PAGE, events.length - shown) });
  };
  moreB.onclick = (ev) => { ev.stopPropagation(); more(); };
  const draw = () => {
    // the box keeps its height across a switch, so what is below it, and
    // the page, stay where they are
    if (pre.isConnected) pre.style.minHeight = pre.offsetHeight + "px";
    code = el("code");
    pre.replaceChildren(code);
    pre.scrollTop = 0;
    shown = 0;
    if (view === "reply") code.textContent = JSON.stringify(reply, null, 2);
    else if (view === "raw") code.textContent = raw;
    if (view === "events") more(); else foot.hidden = true;
  };
  // what is copied is what is shown: the reply, every event, or the body
  copyB.onclick = (ev) => {
    ev.stopPropagation();
    const text = view === "reply" ? JSON.stringify(reply, null, 2) : view === "raw" ? raw
      : events.map((e) => (e.event ? "event: " + e.event + "\n" : "") + sseData(e.data)).join("\n\n");
    copy(text, t(label), copyB);
  };
  draw();
  return panel;
}

function callBodyPanel(label, raw, truncated, id) {
  if (id) {
    const sse = sseBodyPanel(label, raw, truncated, id);
    if (sse) return sse;
  }
  const panel = el("section", "call-body");
  const head = el("div", "call-body-head");
  head.append(el("span", "call-body-label", t(label)));
  if (truncated) head.append(el("span", "call-body-truncated", typeof truncated === "string" ? truncated : t("first 256 KB")));
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

// The request archive: with it on, each call's headers and bodies, secrets
// taken out, go to the S3 bucket sync keeps its backup in, and a request's
// row on the Usage page reads its own back from there (gateway/archive.go).
// Its switch sits over that list, at its right (Jorben on Discord); what it
// keeps, and where, or what it needs first, is its tooltip, and a failed
// upload says so beside it. The switch is the same element across redraws,
// changed in place, so one being pressed or focused is never swapped out.
let archiveSaving = false;
function renderArchive() {
  const box = $("#ledArchive");
  if (!box) return;
  // not said Off before the gateway's state is in
  box.hidden = !providers?.gateway;
  if (box.hidden) return;
  const a = providers.gateway.archive || {};
  let b = box.querySelector(".led-arch-sw");
  if (!b) {
    b = el("button", "led-arch-sw");
    b.type = "button";
    b.setAttribute("role", "switch");
    const sw = el("span", "lib-switch");
    sw.append(el("i"));
    b.append(el("span", "led-arch-name"), sw);
    b.onclick = () => {
      if (archiveSaving) return;
      archiveSaving = true;
      renderArchive();
      api("settings/archive", { on: !providers.gateway.archive?.on })
        .then((na) => { providers.gateway.archive = na; })
        .catch((e) => status(t(e.message), "err"))
        .finally(() => { archiveSaving = false; renderArchive(); });
    };
    box.append(b);
  }
  b.classList.toggle("on", !!a.on);
  b.querySelector(".lib-switch").classList.toggle("on", !!a.on);
  b.setAttribute("aria-checked", a.on ? "true" : "false");
  b.disabled = archiveSaving;
  b.querySelector(".led-arch-name").textContent = t("Request archive");
  b.title = a.bucket ? t("Keeps each call’s headers and bodies, secrets taken out, in {where}", { where: a.bucket })
    : t("Keeps each call’s headers and bodies, secrets taken out, in your S3 bucket. Set up Sync and backup in Settings with an s3:// address first");
  let err = box.querySelector(".led-arch-err");
  if (a.on && a.error) {
    if (!err) { err = el("span", "led-arch-err"); box.prepend(err); }
    err.textContent = t("Upload failed");
    err.title = t("Last upload failed: {e}", { e: a.error });
  } else err?.remove();
}

// What was read back from the archive, by "<date>/<id>": the archive's
// copy, or {busy} while it is read, or {error}.
const archivedCalls = new Map();

function headersPanel(label, first, headers) {
  const panel = el("section", "call-body");
  const head = el("div", "call-body-head");
  const lines = [first, ...Object.keys(headers || {}).sort().flatMap((k) => headers[k].map((v) => `${k}: ${v}`))].filter(Boolean);
  head.append(el("span", "call-body-label", label), el("span", "grow"), copyBtn(lines.join("\n"), label));
  const pre = el("pre");
  pre.append(el("code", "", lines.join("\n")));
  panel.append(head, pre);
  return panel;
}

// The archived file, whole, however large: to the browser in magpie web,
// to Downloads in the app (#447).
async function downloadArchive(name, b) {
  const [date, aid] = name.split("/");
  const q = `date=${encodeURIComponent(date)}&id=${encodeURIComponent(aid)}`;
  if (web) {
    const a = el("a");
    a.href = "/api/archive/file?" + q;
    a.download = "";
    a.click();
    return;
  }
  b.classList.add("busy");
  b.disabled = true;
  try {
    const r = await api("archive/export?" + q, {});
    status(t("Saved to {path}", { path: r.path }), "ok");
  } catch (e) {
    status(t(e.message), "err");
  } finally {
    b.classList.remove("busy");
    b.disabled = false;
  }
}

// one body read back from the archive: shown, or, past 256 KB, only its
// size — the server leaves it out — for the file to be downloaded whole
function archiveBodyPanel(label, part, id) {
  if (part.omitted) {
    const panel = el("section", "call-body");
    const head = el("div", "call-body-head");
    head.append(el("span", "call-body-label", t(label)), el("span", "call-body-count", fmtBytes(part.size)));
    panel.append(head, el("p", "call-archive-big", t("Too long to show here. Download the archive to read it whole.")));
    return panel;
  }
  // cut where the archive stops: its limit, or 256 KB in one from before #447
  const cut = part.truncated && (part.size ? t("first {n} of {size}", { n: fmtBytes(new Blob([part.body]).size), size: fmtBytes(part.size) }) : true);
  return callBodyPanel(label, part.body, cut, id);
}

// A call the archive kept, by "<date>/<id>": read back when asked, and its
// file downloaded, drawn again by redraw as it is read — under a request's
// row on the Usage page (the Gateway page's recent calls show their own
// bodies, and no longer the archive's copy as well).
function archivePanel(name, id, redraw) {
  const box = el("section", "call-archive");
  const [date, aid] = name.split("/");
  const got = archivedCalls.get(name);
  const head = el("div", "call-body-head");
  head.append(el("span", "call-body-label", t("Request archive")), el("code", "call-archive-id", name), el("span", "grow"));
  box.append(head);
  const dl = el("button", "text call-archive-dl", t("Download"));
  dl.onclick = (ev) => { ev.stopPropagation(); downloadArchive(name, dl); };
  if (!got || got.busy || got.error) {
    const b = el("button", "text", t(got?.busy ? "Fetching…" : "Fetch from archive"));
    b.disabled = !!got?.busy;
    b.onclick = (ev) => {
      ev.stopPropagation();
      archivedCalls.set(name, { busy: true });
      redraw();
      api(`archive?date=${encodeURIComponent(date)}&id=${encodeURIComponent(aid)}`)
        .then((a) => archivedCalls.set(name, a))
        .catch((e) => archivedCalls.set(name, { error: t(e.message) }))
        .finally(redraw);
    };
    head.append(b);
    if (got?.error) box.append(el("div", "call-archive-err", got.error));
    else head.append(dl); // one not in the bucket has nothing to download
    return box;
  }
  head.append(el("span", "call-body-truncated call-archive-note", t("Secrets taken out")), dl);
  if (got.large) {
    box.append(el("p", "call-archive-big", t("This archive is {size}, too large to show here. Download it to read it.", { size: got.bytes > 0 ? fmtBytes(got.bytes) : t("very large") })));
    return box;
  }
  const grid = el("div", "call-details");
  grid.append(
    headersPanel(t("Request Headers"), `${got.request.method || ""} ${got.request.path || ""}`.trim(), got.request.headers),
    headersPanel(t("Response Headers"), got.response.status ? `HTTP ${got.response.status}` : "", got.response.headers),
    archiveBodyPanel("Request Body", got.request),
    archiveBodyPanel("Response Body", got.response, id + "|archive"),
  );
  box.append(grid);
  return box;
}

function renderActivity() {
  const g = providers.gateway;
  const box = $("#activity");
  // a body being read keeps its place as new calls come in
  const kept = new Map();
  for (const item of box.querySelectorAll(".call-item[data-id]")) kept.set(item.dataset.id, [...item.querySelectorAll("pre")].map((p) => p.scrollTop));
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
    r.append(el("span", "st", c.error ? `${c.status} ${c.error}` : `${c.status} · ${ledTook(c.ms)}` + (c.ttft ? " · " + t("TTFT {ms}", { ms: ledTook(c.ttft) }) : "")));
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
        callBodyPanel("Response Body", c.responseBody, c.responseTruncated, id),
      );
      item.dataset.id = id;
      item.append(details);
    }
    box.append(item);
  }
  for (const item of box.querySelectorAll(".call-item[data-id]")) {
    const tops = kept.get(item.dataset.id) || [];
    item.querySelectorAll("pre").forEach((p, i) => { if (tops[i]) p.scrollTop = tops[i]; });
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
let presetQuery = "", addReturnPick = null;
function renderAdd() {
  const sheet = $("#addSheet");
  // a redraw (providers reloaded) gives the focus back where it was in it
  const active = document.activeElement, hadFocus = sheet.contains(active);
  const focusPick = hadFocus && active.closest("[data-pick]")?.dataset.pick;
  const selection = hadFocus && active.matches(".find") ? [active.selectionStart, active.selectionEnd] : null;
  sheet.replaceChildren();
  // A sheet put away by a redraw rather than rolled up (a sign-in done
  // opens its account's editor, an import or an action closes it) takes
  // what it held with it, as closeAddSheet does: a click in it still held
  // the page where the sheet had been, and the room made for that was left
  // under a list now ending far above it — the page blank (#509).
  if (!adding && !sheet.hidden) {
    const view = $("#view-providers");
    if (held?.v === view) held = null;
    setRoom(view, 0);
  }
  sheet.hidden = !adding;
  // Duplicate and Add another open a new provider's editor with no sheet
  if (!adding) return editing && typeof editing === "object" ? renderEditor(null, editing.preset) : null;
  const head = el("div", "row-head");
  head.append(el("span", "label", t(providers.providers.length ? "Add a provider" : "Add your first provider")), el("span", "grow"));
  const q = input(presetQuery, t("Find a vendor…"));
  q.className = "find";
  // Escape empties the search, then closes the sheet; it never hides the tray
  q.onkeydown = (e) => {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopPropagation();
    if (q.value) { q.value = presetQuery = ""; drawTiles(); } else if (providers.providers.length) closeAddSheet();
  };
  q.oninput = () => { presetQuery = q.value; drawTiles(); };
  head.append(q);
  const imp = el("button", "text", t("Import…"));
  imp.title = t("Bring over providers set up in other apps");
  imp.onclick = openImportApps;
  head.append(imp);
  if (providers.providers.length) {
    const x = el("button", "text", t("Close"));
    x.onclick = closeAddSheet;
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
    // one moved onto its plugin stays where it was, signing in through it;
    // a deprecated one whose plugin is installed is that plugin's row alone
    // (ARNO on Discord: Qoder CN twice), and one with no account isn't
    // offered at all: it is the Plugins page's, installed from there
    // (yetone: 对于新用户来说，这里应该只显示内置的 provider), unless it is
    // signing in now. A search naming one finds it there.
    const named = (x) => !f || x.name.toLowerCase().includes(f) || x.agent.includes(f) || "subscription".includes(f);
    const subs = SUBS.filter((x) => !(replacedSub(x.agent) || unusedSub(x.agent)) || signing?.agent === x.agent).map((x) => subOf(x.agent)).filter(named);
    const inPlugins = f && SUBS.some((x) => unusedSub(x.agent) && signing?.agent !== x.agent && named(subOf(x.agent)));
    if (subs.length || inPlugins) {
      any = true;
      const grid = section("Subscriptions", "sign in, no key");
      for (const x of subs) grid.append(subTile(x));
      if (!f || inPlugins) grid.append(morePluginsTile(f));
      const w = subs.find((x) => signing?.agent === x.agent);
      if (w) tiles.append(renderSigning(w));
    }
    const plugged = pluginSubs().filter((x) => !(movedSub(x.agent) && SUBS.some((y) => y.agent === x.agent))).filter((x) => !f || x.name.toLowerCase().includes(f) || x.agent.includes(f) || "plugin".includes(f));
    if (plugged.length) {
      any = true;
      const grid = section("From plugins", "signed in by an OpenCode plugin or a pi package");
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
  if (hadFocus && $("#modal").hidden) {
    (focusPick && sheet.querySelector(`[data-pick="${CSS.escape(focusPick)}"]`) || q).focus({ preventScroll: true });
    if (selection) q.setSelectionRange(...selection);
  }
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
function morePluginsTile(q) {
  const b = el("button", "tile more-plugins");
  b.dataset.pick = "plugins";
  const nm = el("span", "nm");
  nm.append(el("span", "n", t("More in Plugins")));
  const ic = el("span", "puzzle");
  ic.append(svg(PUZZLE, 15, 1.4));
  b.append(ic, nm, svg(CHEV_R, 11, 1.6));
  b.title = t("Subscriptions magpie doesn't sign in to itself: install a plugin for one in the Plugins tab");
  b.onclick = () => openPlugins(q);
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
  if (x.hint) b.title += "\n" + t(x.hint);
  // deprecated: badged (one with no account isn't offered here)
  if (!x.plugin && deprecatedSub(x.agent) && n) {
    b.querySelector(".nm").append(deprecatedBadge());
    b.title += "\n" + t("Deprecated") + " · " + t(DEPRECATED_WHY);
  }
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
// keep: leaving the page, a sign-in under way in the editor goes on; the
// editor put away (Cancel, Escape, a click outside) takes it with it (#526)
function cancelEdit(keep) {
  if (keep !== true) dropSigningIn(editing);
  editing = null; draft = null; importing = null; importingApps = null; renderProviders();
}

// modelPrefsOfDraft: the names, levels and images staged in the editor's
// Names & levels, for its Save, or nothing when none changed.
function modelPrefsOfDraft() {
  const x = draft?.modelPrefs;
  return x && Object.keys(x).length ? x : undefined;
}

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

// concurrencyField: how many requests each of the provider's keys or
// accounts may have out at the vendor at once (Discord, Lemon: a Codex
// account is risk-controlled past five or six); more wait their turn and
// go in order. The draft keeps what is typed as concurrency, "" for none
// set — a plugin's provider then takes what its plugin says, shown as the
// placeholder — and concurrencyOfDraft is what is saved.
function concurrencyField(p) {
  const plugin = p?.pluginConcurrency || 0;
  const box = input(draft.concurrency ?? "", plugin ? t("{n}, as its plugin says", { n: plugin }) : t("No limit"), "number");
  box.classList.add("concurrency");
  box.min = "0";
  box.max = "1000";
  box.step = "1";
  box.inputMode = "numeric";
  box.oninput = () => { draft.concurrency = box.value; };
  return field(t("Concurrency"), box, plugin ? t("Over it, requests queue and go out in order; empty takes the plugin's {n}, 0 is no limit", { n: plugin }) : t("Over it, requests queue and go out in order; 0 or empty is no limit"));
}
function concurrencyDraft(p) {
  return { concurrency: p?.maxConcurrency == null ? "" : String(p.maxConcurrency) };
}
// concurrencyOfDraft is the draft's limit as it is saved — null for none
// set, else the number — or undefined when what is typed isn't one.
function concurrencyOfDraft() {
  const v = String(draft.concurrency ?? "").trim();
  if (!v) return null;
  if (!/^\d+$/.test(v) || +v > 1000) return undefined;
  return +v;
}
function concurrencyError(ed) {
  ed.querySelector("input.concurrency")?.focus({ preventScroll: true });
  return editorError(t("Concurrency: a whole number from 0 to 1000"), "warn");
}
function proxyDraft(p) {
  const v = (p?.proxy || "").trim();
  // each account's own (accountProxies), by its name in lower case
  const accountProxies = {};
  for (const [u, x] of Object.entries(p?.accountProxies || {})) accountProxies[u] = { mode: x === "direct" ? "direct" : "custom", url: x === "direct" ? "" : x };
  return { proxyMode: !v ? "" : v === "direct" ? "direct" : "custom", proxyURL: v && v !== "direct" ? v : "", accountProxies };
}
// asTyped: the editor's form as it stands, for Refresh and the Tests to
// ask with before a Save — a key just pasted over the saved one is the one
// tried, and nothing is saved by it. Outside the editor, nothing.
function asTyped() {
  if (!draft) return {};
  const body = { typed: true, key: (draft.key || "").trim(), chat: (draft.chat || "").trim(), responses: (draft.responses || "").trim(), anthropic: (draft.anthropic || "").trim(), modelsURL: (draft.modelsURL || "").trim() };
  // a System One base is asked at POST …/systemone, not on the three APIs
  if (draft.api === "decide") body.decide = (draft.decide || "").trim();
  if (draft.headers) body.headers = headersOf(draft.headers);
  const proxy = draft.proxyMode === undefined ? null : proxyOfDraft();
  if (proxy !== null) body.proxy = proxy;
  return body;
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
  if (o.effortOf) return effortIcon(o.effortOf);
  if (o.svg) { const e = el("span", "ic"); e.append(svg(o.svg, 16, 1.4)); return e; }
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
  // Names & levels scrolls inside the editor body, so keep its own position.
  const names = !fresh && d.querySelector(".mnames:not([hidden])");
  const namesAt = names && { provider: names.dataset.provider, top: names.scrollTop };
  m.classList.remove("out");
  d.classList.remove("swap");
  if (!fresh) { void d.offsetWidth; d.classList.add("swap"); } // content changed: a soft refresh, not a re-entrance
  frame(content);
  d.replaceChildren(content);
  m.hidden = false;
  const body = content.querySelector(":scope > .ebody");
  if (body) body.scrollTop = top; // a re-render keeps the place
  const nextNames = content.querySelector(".mnames:not([hidden])");
  if (namesAt && nextNames?.dataset.provider === namesAt.provider) nextNames.scrollTop = namesAt.top;
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
    // an editor opened from the add sheet gives the focus back to its option
    const sheet = $("#addSheet");
    if (adding && !sheet.hidden && !$("#view-providers").hidden) {
      const option = addReturnPick && sheet.querySelector(`[data-pick="${CSS.escape(addReturnPick)}"]`);
      (option || sheet.querySelector(".find"))?.focus({ preventScroll: true });
    }
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

const PROTOS = [["chat", "OpenAI", "Chat Completions — most agents"], ["responses", "Responses", "OpenAI Responses — what Codex speaks"], ["anthropic", "Anthropic", "Anthropic Messages — what Claude Code speaks"], ["decide", "System One", "A decision API on System One (TypeSafe's Jev, a gateway's, or Bailian's decision model) — what a routing group asks as a turn begins"]];
// apiLabel: the name an API (a protocol) goes by in the editor
const apiLabel = (proto) => (PROTOS.find(([k]) => k === proto) || [])[1] || proto;
const decideOnly = (p) => !!p?.decide && !(p.chat || p.responses || p.anthropic);
const decidesModel = (p, id) => !!p.decide && (decideOnly(p) || id.split("/").some((s) => /^jev(?:-|$)/i.test(s)));
// modelAPIs: the APIs one of p's models can be asked on alone, those it has
// a URL for when it has more than one — a custom provider's, a preset's or
// a subscription's alike (01huadalang on Discord: 一个 api 里有很多模型但是不同协议;
// OpenCode Go's DeepSeek answers on Responses too)
const modelAPIs = (p) => {
  const urls = ["chat", "responses", "anthropic"].filter((k) => ((draft?.id === p.id ? draft[k] : undefined) ?? p[k] ?? "").trim());
  return urls.length < 2 ? [] : PROTOS.filter(([k]) => urls.includes(k));
};

// pickedOf: the models the user picked for p, as its editor starts them.
// Only the picks: with none, agents are served the vendor's list (its
// first 24), which the models list marks on too; taking those as picks
// drew them ticked on opening and the next Save wrote them as picks, so
// Select none and Save came back as 24 ticked (#614).
function pickedOf(p) {
  const on = p.models.filter((m) => m.on);
  if (!Array.isArray(p.chosen)) return on.map((m) => m.id); // no picks said: those served, as before
  const picks = new Set(p.chosen);
  return on.filter((m) => picks.has(m.id)).map((m) => m.id);
}

// draftOf is a saved provider as its editor's form holds it.
function draftOf(p) {
  return { id: p.id, name: p.name, preset: p.preset, chat: p.chat, responses: p.responses, anthropic: p.anthropic, catalog: p.catalog, decide: p.decide || "", key: "", api: p.chat ? "openai" : p.anthropic ? "anthropic" : p.responses ? "responses" : p.decide ? "decide" : "openai", chosen: pickedOf(p), extra: [], headers: headerRows(p.headers), icon: p.icon || "", fallback: [...(p.fallback || [])], unlisted: !!p.unlisted, searches: !!p.searches, balanceURL: p.balanceURL || "", balancePath: p.balancePath || "", modelsURL: p.modelsURL || "", contexts: contextsText(p.contexts), keysUrl: p.keysUrl || "", ...proxyDraft(p), ...concurrencyDraft(p) };
}

// duplicateProvider opens the Add form on a copy of p (#268): its URLs,
// headers, models, balance and proxy, under a name of its own. The key is
// p's unless another is pasted: the Add takes it from p, with its other
// keys (copyOf). A signed-in account has no copy.
function duplicateProvider(p) {
  const name = t("{name} copy", { name: p.name });
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
      : { id: "", name: "", preset: "", chat: "", responses: "", anthropic: "", decide: "", catalog: "", key: "", api: "openai", chosen: [], extra: [], headers: [], icon: "" });
  const ed = el("div", "editor" + (isNew ? " new" : ""));
  ed.onclick = (e) => e.stopPropagation();

  {
    const h = el("div", "ehead");
    const copyOf = draft.copyOf && providers.providers.find((x) => x.id === draft.copyOf);
    h.append(icon(p?.icon || (copyOf && draft.icon) || pr?.icon || "generic"), el("b", "", p ? p.name : copyOf ? t("Copy of {name}", { name: copyOf.name }) : pr ? pr.name : t("Custom provider")));
    if (pr?.note) h.append(el("span", "note", t(pr.note)));
    h.append(el("span", "grow"));
    // a plugin's provider has plugin://<id> for its base, and its id is no
    // address to open: only a host with a dot or a port makes a link
    const site = pr?.website || p?.website || (/[.:]/.test(p?.host || "") ? "https://" + p.host : "");
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
      hint.textContent = t(decideOnly(p) ? "Routing groups name its models as {id} for their classifier" : "Agents pick its models as {id}", { id: idOf() + "/…" }) +
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
  // the Web search row, shown while there is an API it can search on
  const searchable = () => !!((draft.anthropic || "").trim() || (draft.responses || "").trim());
  let showSearch = () => {};
  if (custom) {
    name = input(draft.name, t("e.g. My Relay"));
    name.oninput = () => { draft.name = name.value; if (isNew) draft.id = slug(name.value); };
    ed.append(...field(t("Name"), name));
    if (p) idField();

    // the base URL is the one the chosen protocol is asked at; a vendor
    // that serves only the Responses API is added (and tested) with that
    // alone, since /chat/completions would only fail (#73)
    const seg = el("div", "segs");
    for (const [v, l, hint] of [["openai", "OpenAI compatible", "…/v1 — chat completions, and responses when the vendor has it"], ["responses", "OpenAI Responses", "…/v1 — for a vendor that serves only the Responses API, not chat completions"], ["anthropic", "Anthropic compatible", "the root URL, what ANTHROPIC_BASE_URL would take"], ["decide", "System One", "a decision API's root, POST …/systemone under it: its models are a routing group's classifier, never an agent's"]]) {
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
        showApi(v);
      };
      b.dataset.api = v;
      seg.append(b);
    }
    // the protocol the base URL is for, shown as picked: by its button,
    // or by a detection taken (useDetected)
    const showApi = (v) => {
      draft.api = v;
      url.value = draft[apiField[v]] || "";
      for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x.dataset.api === v);
      slide(seg, "api");
      url.placeholder = v === "anthropic" ? "https://…" : "https://…/v1";
      fillEndpoints();
      showSearch();
    };
    queueMicrotask(() => slide(seg, "api"));
    url = input(draft[apiField[draft.api]], draft.api === "anthropic" ? "https://…" : "https://…/v1", "url");
    url.oninput = () => { draft[apiField[draft.api]] = url.value; showSearch(); };
    const urlWrap = el("div", "stack");
    urlWrap.append(seg, url);
    // the APIs that answered a detection, taken for the provider: their
    // URLs set, one that wasn't found there (404, 405) cleared, and the
    // base URL's protocol one of those that answered
    const useDetected = (rs) => {
      for (const x of rs) {
        if (x.ok) draft[x.protocol] = x.base;
        else if ((x.status === 404 || x.status === 405) && (draft[x.protocol] || "").trim().replace(/\/+$/, "") === x.base) draft[x.protocol] = "";
      }
      const v = draft[apiField[draft.api]] ? draft.api : ["openai", "responses", "anthropic", "decide"].find((a) => draft[apiField[a]]);
      showApi(v || draft.api);
      draft.onModelPrefs?.(); // the APIs a model can be given follow the URLs
    };
    urlWrap.append(detectAPIs(p, () => url.value, useDetected));
    ed.append(...field("Base URL", urlWrap));
  }

  if (p?.account) {
    // the sign-in belongs to the agent; magpie only borrows it
    const a = p.account;
    if (subOf(a.agent)) {
      ed.append(...field(t("Accounts"), renderAccounts(a, p), p.routing ? t("Tick every account to use; Routing says how requests spread over them.") : subOf(a.agent).single ? t("{agent} keeps one account; the gateway runs it for every request. Signing in to another replaces it.", { agent: a.agentName }) : subOf(a.agent).own && (a.logins || []).some((l) => l.own) ? t("The gateway uses the first. Tick more and it moves on to the next when the one before it is out of quota. {agent} itself stays signed in as it is.", { agent: a.agentName && a.agentName !== a.agent ? a.agentName : p.name }) : subOf(a.agent).own || subOf(a.agent).plugin ? t("The gateway uses the first. Tick more and it moves on to the next when the one before it is out of quota.") : t("{agent} signs in to the first. Tick more and the gateway moves on to the next when the one before it is out of quota. Sessions already running keep theirs until restarted.", { agent: a.agentName })));
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
    ed.append(...concurrencyField(p));
    // a plugin's provider is reached inside magpie: its plugin:// URLs go
    // nowhere to show or test
    const urls = [p.chat, p.responses, p.anthropic].filter(Boolean);
    if (urls.length && !urls.some((u) => u.startsWith("plugin://"))) ed.append(...field(t("Endpoints"), renderEndpoints(p, p)));
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
      const maxConcurrency = concurrencyOfDraft();
      if (maxConcurrency === undefined) return concurrencyError(ed);
      saveBtn.classList.add("busy"); providerAction("save", { id: p.id, models: chosenIds(), unlisted: draft.unlisted, fallback: draft.fallback, contexts: cx.map, proxy, accountProxies: own.map, maxConcurrency, modelPrefs: modelPrefsOfDraft(), ...routingOfDraft(p) }, t("{name} saved", { name: p.name })); };
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
  const key = input(draft.key || "", p?.key.set ? t("{masked} · paste a new key to replace it", { masked: p.key.masked }) : copied?.key.set ? t("{masked} · {name}'s key, or paste another", { masked: copied.key.masked, name: copied.name }) : t(pr?.keyHint || (pr?.noKey || p?.key.optional ? "optional for local servers" : "paste an API key")), "password");
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
  ed.append(...concurrencyField(p));

  // a relay in front of Anthropic's or OpenAI's API searches the web as
  // they do, which magpie can't tell from its host (#359): a client's web
  // search goes to it as sent, rather than through magpie's own. Only its
  // Anthropic or Responses API can be asked to: a Chat one has no such tool
  if (custom) {
    const [stk, scb] = tick(t("Searches the web by itself"), !!draft.searches);
    scb.onchange = () => { draft.searches = scb.checked; };
    const row = field(t("Web search"), stk, t("For a relay in front of Anthropic's or OpenAI's own API: Claude Code's WebSearch and Codex's web_search go to it as they were sent, not through magpie's search. magpie doesn't automatically search with it for other models, but you can name it as the searcher."));
    showSearch = () => { for (const e of row) e.style.display = searchable() ? "" : "none"; };
    showSearch();
    ed.append(...row);
  }

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
  if (p?.stepPlan) ed.append(...field(t("Step Plan allowances"), renderStepPlan(p.stepPlan), t("A key tells only the balance. The Step Plan's 5-hour, weekly and credit windows are told only to a StepFun sign-in: bring yours here once and magpie keeps it (for 30 days), used for nothing else.")));

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
  let refreshEndpoints = () => {}, onRegion = () => {};
  if (pr?.regions?.length) {
    // Bedrock's ten regions don't fit the editor's width: they scroll
    const seg = el("div", "segs regions");
    const cur = pr.regions.find((r) => r.chat && r.chat === (draft.chat || pr.chat)) || pr.regions.find((r) => r.decide && (r.id === draft.region || (!draft.region && workspaceOf(r.decide, draft.decide ?? p?.decide ?? pr.decide) !== null))) || pr.regions[0];
    for (const r of pr.regions) {
      const b = el("button", "opt" + (r.id === cur.id ? " on" : ""), t(r.name));
      b.onclick = () => {
        draft.chat = r.chat || ""; draft.responses = r.responses || ""; draft.anthropic = r.anthropic || "";
        draft.keysUrl = r.keysUrl || "";
        onRegion(r);
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
    if (!decideOnly(p || pr)) ed.append(...field(t("Context window"), contextPicks(p, cx), t("How long a request the models take, told to the agents; empty leaves it to the vendor and models.dev")));
  }
  if (p && !decideOnly(p)) ed.append(...field(t("Fallback"), renderFallback(p), fallbackHint(p)));
  else if (custom) {
    const ex = input(draft.extra.join(", "), t("model ids, comma separated · e.g. gpt-5.5, claude-sonnet-5"));
    const pick = addFormModels(ex);
    ex.oninput = () => { draft.extra = ex.value.split(/[,\s]+/).filter(Boolean); pick.draw(); };
    ed.append(...field(t("Models"), pick.box, t("Optional: Fetch models to pick from the vendor's list, or type ids; none picked, magpie asks for the list after saving.")));
  }

  // Jev's endpoint can be the one its gateway's docs give — Cloudflare's
  // names the account, …/accounts/<id>/ai/run — in place of the preset's
  if (decides && !custom) {
    if (draft.decide === undefined) draft.decide = p?.decide || pr?.decide || "";
    const du = input(draft.decide, pr?.decide || "https://…", "url");
    // Bailian's is at the host of the key's workspace (#647): its id is
    // typed on its own, the plan's address filled in with it
    if (!draft.region && pr?.regions?.some((r) => r.decide)) draft.region = (pr.regions.find((r) => r.decide && workspaceOf(r.decide, draft.decide) !== null) || pr.regions[0]).id;
    const tmpl = () => (pr?.regions || []).find((r) => r.id === draft.region)?.decide || (draft.region ? "" : pr?.decide) || "";
    const wsIn = input((draft.workspace ?? workspaceOf(tmpl(), draft.decide)) || "", t("e.g. ws-…, from the Bailian console"));
    wsIn.classList.add("workspace-id");
    const wsRow = field(t("Workspace ID"), wsIn, t("The workspace your API key belongs to, in the Bailian console; its decision model is asked at that workspace's host"));
    const fill = () => {
      const tp = tmpl();
      const ws = tp.includes(WORKSPACE);
      for (const x of wsRow) x.hidden = !ws;
      if (!ws) return;
      draft.decide = du.value = tp.split(WORKSPACE).join((draft.workspace ?? wsIn.value).trim() || WORKSPACE);
    };
    wsIn.oninput = () => { draft.workspace = wsIn.value; fill(); };
    du.oninput = () => { draft.decide = du.value; };
    onRegion = (r) => { draft.region = r.id; if (r.decide) { draft.decide = du.value = r.decide; fill(); } };
    ed.append(...wsRow);
    fill();
    ed.append(...field(t(decideOnly(p || pr) && !/jev/i.test(pr?.name || p?.name || "") ? "System One endpoint" : "Jev endpoint"), du, tmpl().includes(WORKSPACE) ? t("The address the decision model is asked at, POST …/systemone under it; the workspace ID above fills it in") : t("The address Jev is asked at; paste the one from your gateway's docs, e.g. Cloudflare's …/accounts/<account id>/ai/run")));
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
        i.oninput = () => { draft[key] = i.value; showSearch(); };
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
    inner.append(...field(t("Balance field"), balPair, t("Where the amount is in the reply, e.g. data.balance; it can be a sum with + - * / and brackets, e.g. data.total / 500000 or (1 - credits.used / 70) %; \"$\" in front adds the sign, \"%\" after it shows a percent, with a bar; several, each with a label, go apart by \";\", e.g. 5h: a.used / a.cap %; $credits.left")));
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
    more.onclick = () => { editing = { preset: pr.id }; draft = null; renderProviders(); };
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
    if (decides || custom) body.decide = (custom && draft.api !== "decide" && !p?.decide ? "" : draft.decide || "").trim();
    if ((body.decide || "").includes(WORKSPACE)) { ed.querySelector(".workspace-id")?.focus({ preventScroll: true }); return editorError(t("Give the workspace ID your API key belongs to, or pick the Token Plan"), "warn"); }
    if (custom) { body.icon = draft.icon || "generic"; body.balanceURL = (draft.balanceURL || "").trim(); body.balancePath = (draft.balancePath || "").trim(); body.modelsURL = (draft.modelsURL || "").trim(); }
    if (p) { body.fallback = draft.fallback; body.unlisted = draft.unlisted; body.modelPrefs = modelPrefsOfDraft(); Object.assign(body, routingOfDraft(p)); }
    body.searches = !!draft.searches && searchable();
    const cx = parseContexts(draft.contexts || "");
    if (cx.error) return editorError(t("Context window: {v} is not a length like 128k or 1m", { v: cx.error }), "warn");
    body.contexts = cx.map;
    body.proxy = proxyOfDraft();
    if (body.proxy === null) { ed.querySelector(".proxy-url")?.focus(); return editorError(t("Proxy: type its address, like http://127.0.0.1:7890"), "warn"); }
    body.maxConcurrency = concurrencyOfDraft();
    if (body.maxConcurrency === undefined) return concurrencyError(ed);
    if (draft.balanceToken) body.balanceToken = draft.balanceToken;
    else if (draft.clearBalanceToken) body.clearBalanceToken = true;
    if (team) {
      // both or neither: {} clears them
      const org = (draft.zhipuTeam.org || "").trim(), project = (draft.zhipuTeam.project || "").trim();
      if (!org !== !project) { ed.querySelector(org ? ".team-project" : ".team-org")?.focus({ preventScroll: true }); return editorError(t("Team plan: give both the organization ID and the project ID"), "warn"); }
      body.zhipuTeam = org ? { org, project } : {};
    }
    if (isNew && custom && !body.name) { name.focus(); return editorError(t("Give it a name"), "warn"); }
    if (isNew && custom && !body.chat && !body.anthropic && !body.responses && !body.decide) { url.focus(); return editorError(t("A base URL is needed"), "warn"); }
    if (endpoint && !body.chat && !body.responses) { endpoint.focus(); return editorError(t(pr.endpointNeeded || "Your resource's endpoint is needed"), "warn"); }
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
        const r = await api("provider/test", { ...asTyped(), id: p.id });
        for (const x of r.results) {
          const s = slots[x.protocol];
          if (!s) continue;
          s.className = "res " + (x.ok ? "ok" : "bad");
          s.replaceChildren();
          s.append(svg(x.ok ? CHECK : "M4.5 4.5l7 7M11.5 4.5l-7 7", 10, 2));
          s.append(el("span", "", x.ok ? ledTook(x.ms) : x.status ? `${x.status} · ${x.error}` : x.error));
          s.title = x.ok ? t("model {model}", { model: x.model }) : x.error;
        }
      } catch (e) { for (const s of Object.values(slots)) { s.className = "res"; s.textContent = ""; } status(e.message, "err"); }
      test.classList.remove("busy");
    };
    eps.append(test);
  }
  return eps;
}

// showRest: the button at the foot of a long model list that draws the
// models left out of it (#685: "… 4 more, filter to find them" named none of
// them, so there was nothing to filter by). It goes down with what it opens.
function showRest(n, open) {
  const b = el("button", "text action mchips-rest", t("Show the other {n}", { n }));
  b.dataset.unrolls = "";
  b.onclick = (e) => { e.preventDefault(); open(); };
  return b;
}

// detectAPIs: which of the APIs magpie speaks to a vendor answer at the base
// URL typed (01huadalang on Discord: 一键检测支持什么协议) — Detect sends
// each the smallest request there, with the key typed (or the saved one)
// and a model typed or picked from the vendor's list, and shows what each
// answered; "Use these" takes those that answered for the provider. With
// a model typed for a saved provider and only some APIs answering, that
// model alone can be asked on one of them (staged with Names & levels).
// "Each picked model" asks every model picked on each API (01huadalang:
// 应该能看出来选择的模型支持情况…有的仅支持 response 有的双协议), a few at
// a time, and shows model by API; its "Use these" also gives each model
// that answered on one API only that API, staged as Names & levels does.
// DETECT_MAX: the most models one detection asks (provider.DetectMax)
const DETECT_MAX = 30;
// addFormModels: the add form's models, typed in (ex) or picked from the
// vendor's list, asked with the form as it stands before anything is saved
// (#578: 添加供应商的时候，希望添加可以获取全模型的按钮或者下拉框). A chip
// ticked or unticked is in draft.extra, which the Save sends as the models.
function addFormModels(ex) {
  const box = el("div", "stack add-models");
  const row = el("div", "detect-row");
  const go = el("button", "text action fetch-models", t("Fetch models"));
  go.title = t("Ask the vendor for its model list with the URL and key typed, to pick from; nothing is saved");
  row.append(ex, go);
  const out = el("div", "models add-models-list");
  out.hidden = true;
  box.append(row, out);
  let listed = [], showAll = false;
  const q = input("", "");
  q.classList.add("add-models-filter");
  q.oninput = () => draw();
  q.onkeydown = (e) => e.stopPropagation();
  const chips = el("div", "mchips");
  const acts = el("div", "detect-acts");
  const sync = () => { ex.value = draft.extra.join(", "); };
  const draw = () => {
    if (!listed.length) return;
    chips.replaceChildren();
    const f = q.value.trim().toLowerCase();
    let shown = 0, hidden = 0;
    const match = listed.filter((m) => !f || m.id.toLowerCase().includes(f) || (m.name || "").toLowerCase().includes(f));
    for (const m of match) {
      const on = draft.extra.includes(m.id);
      // past the first 120 only the picked are drawn, till the rest are asked for
      if (shown >= 120 && !f && !showAll && !on) { hidden++; continue; }
      const c = el("button", "mchip" + (on ? " on" : ""));
      c.append(el("span", "", m.name || m.id));
      if (m.name) c.title = m.id;
      c.dataset.model = m.id;
      c.onclick = (e) => { e.preventDefault(); draft.extra = on ? draft.extra.filter((x) => x !== m.id) : [...draft.extra, m.id]; sync(); draw(); };
      chips.append(c);
      shown++;
    }
    if (hidden) chips.append(showRest(hidden, () => { showAll = true; draw(); }));
    if (f && !match.length) chips.append(el("span", "hint", t("No model here matches “{q}”.", { q: q.value.trim() })));
    acts.replaceChildren();
    const picked = draft.extra.filter((id) => listed.some((m) => m.id === id)).length;
    acts.append(el("span", "hint", t("{n} of {all} picked", { n: picked, all: listed.length })));
    const all = el("button", "text action", t(f ? "Pick those shown" : "Pick all"));
    all.onclick = (e) => { e.preventDefault(); draft.extra = [...draft.extra, ...match.map((m) => m.id).filter((id) => !draft.extra.includes(id))]; sync(); draw(); };
    const none = el("button", "text action", t("Pick none"));
    none.onclick = (e) => { e.preventDefault(); const ids = new Set(listed.map((m) => m.id)); draft.extra = draft.extra.filter((id) => !ids.has(id)); sync(); draw(); };
    acts.append(all, none);
  };
  go.onclick = async (e) => {
    e.preventDefault();
    const body = { ...asTyped(), preset: draft.preset || "", name: draft.name || "" };
    if (!body.chat && !body.responses && !body.anthropic && !body.modelsURL) { status(t("Type the base URL first"), "warn"); return; }
    go.classList.add("busy");
    out.hidden = false;
    out.replaceChildren(el("span", "hint", t("Asking the vendor…")));
    try {
      const r = await api("provider/list", body);
      listed = r?.models || [];
      out.replaceChildren();
      if (!listed.length) { out.append(el("span", "hint", t("The vendor's list is empty. Type the model ids instead."))); return; }
      q.placeholder = t("filter {n} models…", { n: listed.length });
      // the picks' count and Pick all / none above the chips, which a pick
      // can make a line longer or shorter, so they stay where they are
      if (listed.length > 24) out.append(q);
      out.append(acts, chips);
      draw();
    } catch (err) { out.replaceChildren(el("span", "hint err", err.message)); }
    finally { go.classList.remove("busy"); }
  };
  return { box, draw };
}

function detectAPIs(p, base, use) {
  const box = el("div", "detect");
  const row = el("div", "detect-row");
  const go = el("button", "text action", t("Detect APIs"));
  go.title = t("Send the smallest request to each API (OpenAI chat completions, Responses, Anthropic messages) at this URL, to see which answer");
  const each = el("button", "text action detect-each", t("Each picked model"));
  each.title = t("Ask every model picked below on each API, to see which API serves which model (up to {n}, a few at a time)", { n: DETECT_MAX });
  const model = input(draft.detectModel || "", t("model to try · empty picks one from the vendor's list"));
  model.classList.add("detect-model");
  model.oninput = () => { draft.detectModel = model.value; };
  model.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") go.click(); else if (e.key === "Escape") cancelEdit(); };
  row.append(go, model, each);
  const out = el("div", "eps detect-out");
  out.hidden = true;
  box.append(row, out);
  // the APIs by row: each one's URL and how it answered
  const byAPI = (results) => {
    for (const x of results) {
      const e = el("div", "ep");
      e.dataset.api = x.protocol;
      const [, label, hint] = PROTOS.find(([k]) => k === x.protocol) || [x.protocol, x.protocol, ""];
      const pl = el("span", "pl", label);
      pl.title = t(hint);
      const res = el("span", "res " + (x.ok ? "ok" : "bad"));
      res.append(svg(x.ok ? CHECK : CROSS, 10, 2));
      const why = x.status ? `${x.status} · ${x.error}` : t(x.error || "");
      res.append(el("span", "", x.ok ? ledTook(x.ms) : why));
      res.title = (x.model ? t("model {model}", { model: x.model }) : "") + (x.ok ? "" : "\n" + why);
      e.append(pl, el("code", "", x.base || "—"), res);
      out.append(e);
    }
  };
  const ask = async (btn, extra) => {
    const typedBase = (base() || "").trim();
    if (!typedBase && !(draft.chat || draft.responses || draft.anthropic || "").trim()) { status(t("Type the base URL first"), "warn"); return null; }
    btn.classList.add("busy");
    out.hidden = false;
    out.replaceChildren(el("span", "hint", t("Asking each API…")));
    try {
      // the URL typed is asked as each API takes it, not as the field
      // holding it would send it; URLs given under More endpoints as they are
      const body = { ...asTyped(), id: p?.id, base: typedBase, ...extra };
      for (const k of ["chat", "responses", "anthropic"]) if (body[k] === typedBase) body[k] = "";
      const r = await api("provider/detect", body);
      out.replaceChildren();
      return r;
    } catch (e) { out.replaceChildren(); out.hidden = true; status(e.message, "err"); return null; }
    finally { btn.classList.remove("busy"); }
  };
  go.onclick = async () => {
    const m = model.value.trim();
    const r = await ask(go, { model: m });
    if (!r) return;
    byAPI(r.results);
    const ok = r.results.filter((x) => x.ok);
    const acts = el("div", "detect-acts");
    if (!ok.length) acts.append(el("span", "hint", t("None answered: check the URL and the key, or type a model the vendor serves")));
    else {
      const take = el("button", "text action", t("Use these"));
      take.title = t("Set the URLs of the APIs that answered; one not found there is cleared");
      take.onclick = () => { use(r.results); take.disabled = true; take.textContent = t("Taken · save to keep"); };
      acts.append(take);
      // one model the provider serves, answering on some APIs only:
      // it can be asked on one of them alone
      const pm = p && m && p.models.find((x) => x.id === m);
      if (pm && ok.length < r.results.filter((x) => x.base).length) {
        for (const x of ok) {
          const label = apiLabel(x.protocol);
          const b = el("button", "text action", t("Ask {model} on {api} only", { model: m, api: label }));
          b.title = t("Staged in Names & levels and made with the Save; Auto there gives it back");
          b.onclick = () => {
            stageAPI(pm, x.protocol);
            if (!draft[x.protocol]) use(r.results);
            for (const o of acts.querySelectorAll(".pick-api")) o.classList.toggle("on", o === b);
            draft.onModelPrefs?.();
            status(t("{model} is asked on {api} once saved", { model: m, api: label }), "ok");
          };
          b.classList.add("pick-api");
          acts.append(b);
        }
      }
    }
    out.append(acts);
  };
  // a model's API staged in Names & levels: the one it has saved stages nothing
  const stageAPI = (pm, proto) => {
    const prefs = draft.modelPrefs = draft.modelPrefs || {};
    const id = pm.id;
    prefs[id] = prefs[id] || {};
    if ((pm.api || "") === proto) delete prefs[id].api; else prefs[id].api = proto;
    if (!Object.keys(prefs[id]).length) delete prefs[id];
  };
  each.onclick = async () => {
    if (draft.api === "decide") { status(t("A decision API is asked with one model: type it and press Detect APIs"), "warn"); return; }
    const ids = chosenIds();
    if (!ids.length) { status(t("Pick models first, or type one and press Detect APIs"), "warn"); return; }
    const r = await ask(each, { detectModels: ids });
    if (!r) return;
    byAPI(r.results);
    // the APIs asked: those with a URL
    const asked = r.results.filter((x) => x.base).map((x) => x.protocol);
    const grid = el("div", "detect-grid");
    grid.style.gridTemplateColumns = `minmax(0, 1.3fr) repeat(${asked.length}, minmax(0, 1fr))`;
    grid.append(el("span", "dg-head", t("Model")), ...asked.map((a) => el("span", "dg-head", apiLabel(a))));
    // a model that answered on one API alone, of two or more asked, is
    // given that one by the Use these
    const only = {};
    for (const md of r.models) {
      const name = el("code", "dg-model", md.model);
      name.title = md.model;
      grid.append(name);
      const okOn = [];
      for (const a of asked) {
        const x = md.results.find((y) => y.protocol === a) || {};
        const cell = el("span", "dg-cell " + (x.ok ? "ok" : "bad"));
        cell.dataset.model = md.model;
        cell.dataset.api = a;
        cell.append(svg(x.ok ? CHECK : CROSS, 10, 2));
        const why = x.status ? `${x.status} · ${x.error || ""}` : t(x.error || "");
        cell.append(el("span", "", x.ok ? ledTook(x.ms) : why));
        cell.title = `${md.model} · ${apiLabel(a)}\n` + (x.ok ? t("Answered in {took}", { took: ledTook(x.ms) }) : why);
        if (x.ok) okOn.push(a);
        grid.append(cell);
      }
      if (okOn.length === 1 && asked.length > 1) only[md.model] = okOn[0];
    }
    out.append(grid);
    if (ids.length > r.models.length) out.append(el("span", "hint", t("Only the first {n} of {all} models were asked", { n: r.models.length, all: ids.length })));
    const acts = el("div", "detect-acts");
    if (!r.results.some((x) => x.ok)) acts.append(el("span", "hint", t("None answered: check the URL and the key, or type a model the vendor serves")));
    else {
      const take = el("button", "text action", t("Use these"));
      // only a saved provider's models are given an API; a new one's, in
      // Names & levels once it is saved
      const give = p ? Object.entries(only).map(([id, a]) => [p.models.find((x) => x.id === id), a]).filter(([pm]) => pm) : [];
      take.title = give.length ? t("Set the URLs of the APIs that answered, and give each model that answered on one API only that API (staged in Names & levels, made with the Save)")
        : t("Set the URLs of the APIs that answered; one not found there is cleared");
      take.onclick = () => {
        for (const [pm, a] of give) stageAPI(pm, a);
        use(r.results);
        take.disabled = true;
        take.textContent = t("Taken · save to keep");
        if (give.length) status(t("{n} models are asked on the one API that answered them once saved", { n: give.length }), "ok");
      };
      acts.append(take);
      if (give.length) acts.append(el("span", "hint", give.map(([pm, a]) => t("{model}: {api} only", { model: pm.id, api: apiLabel(a) })).join(" · ")));
    }
    out.append(acts);
  };
  return box;
}

// modelTestWhy: why a provider's models can't each be sent a test request
// (the server's providerJSON.modelTest), in words, or "" when they can
function modelTestWhy(p) {
  if (p.modelTest === "decide") return t("A classifier's models aren't sent test requests: Test under Endpoints asks Jev's endpoint for them.");
  if (p.modelTest === "own-api") return t("{name} is reached through its own API, which magpie translates each agent request for, so a test request can't be sent to it on its own. Ask the model from an agent to try it.", { name: p.name });
  return "";
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
    // the API it was asked on said, where the answer says (01huadalang:
    // 右键选择测试只能测试出是不是通的，不能说什么协议是通的)
    const via = x?.protocol ? apiLabel(x.protocol) : "";
    c.title = !x ? t("Testing…") : x.ok ? (via ? t("Answered via {api} in {took}", { api: via, took: ledTook(x.ms) }) : t("Answered in {took}", { took: ledTook(x.ms) })) : (via ? via + " · " : "") + (x.status ? x.status + " · " : "") + x.error;
  };
  // a chip's right-click (or the menu key) tests that model alone: Test models
  // asks every one, and a list of many takes a while (yonghe, Discord). A
  // signed-in account is tested as a key is; where no test request can be
  // sent the menu still opens, its item off and saying why (ARNO, Discord:
  // 有些provider里的模型可以右击检测，有些却不可以)
  const noTest = modelTestWhy(p);
  // the API a model is asked on, set beside its test (01huadalang: a
  // preset's or a subscription's model, not only a custom provider's):
  // staged as Names & levels stages it, and made with the Save
  const apis = modelAPIs(p);
  const apiNow = (id) => draft.modelPrefs?.[id]?.api ?? p.models.find((m) => m.id === id)?.api ?? "";
  const pickAPI = (c, id) => {
    const m = p.models.find((x) => x.id === id) || { id };
    const auto = (m.auto || []).map(apiLabel).join(" · ");
    const opts = [{ v: "", name: "Auto", note: auto ? t("As its vendor's list says: {apis}", { apis: auto }) : "Each API the provider has, as it answers" },
      ...apis.map(([k, l, hint]) => ({ v: k, name: l, note: hint }))];
    openProtoMenu(c, opts, apiNow(id), (v) => {
      if (v === apiNow(id)) return;
      const prefs = draft.modelPrefs = draft.modelPrefs || {};
      prefs[id] = prefs[id] || {};
      if (v === (m.api || "")) delete prefs[id].api; else prefs[id].api = v;
      if (!Object.keys(prefs[id]).length) delete prefs[id];
      draw();
      status(v ? t("{model} is asked on {api} once saved", { model: id, api: apiLabel(v) }) : t("{model} is asked as its vendor's list says once saved", { model: id }), "ok");
    }, "API this model is asked on", "model-api-menu");
  };
  const menu = (c, id) => {
    const noTest = decidesModel(p, id) ? modelTestWhy({ modelTest: "decide" }) : modelTestWhy(p);
    if (!noTest) c.title = (c.title ? c.title + "\n" : "") + t("Right-click to test just this model");
    c.oncontextmenu = (e) => {
      e.preventDefault();
      const again = agentMenu?.anchor === c || protoMenu?.anchor === c;
      closeAgentMenu();
      closeProtoMenu();
      if (again) return;
      const acts = [noTest ? { name: "Test this model", icon: "M5.5 3.75v8.5L12.25 8z", off: true, why: noTest, run() {} }
        : { name: "Test this model", icon: "M5.5 3.75v8.5L12.25 8z", run: () => testOne(id) }];
      if (apis.length && !decidesModel(p, id)) {
        const now = apiNow(id);
        acts.push({ name: now ? t("Asked on {api}…", { api: apiLabel(now) }) : t("Asked on: Auto…"), icon: "M2.5 5h11M2.5 11h11M10.5 2.5 13.5 5l-3 2.5M5.5 8.5 2.5 11l3 2.5", tip: t("Pick the API this model is asked on"), run: () => pickAPI(c, id) });
      }
      openRowMenu(c, acts);
    };
    // the API it is asked on, when one is picked (or staged)
    const now = apis.length && !decidesModel(p, id) ? apiNow(id) : "";
    if (now) {
      const tag = el("span", "badge mapi-tag", apiLabel(now));
      tag.title = t("Asked on {api} alone; right-click to change", { api: apiLabel(now) });
      c.append(tag);
    }
  };
  const box = el("div", "models");
  const chips = el("div", "mchips");
  const names = el("div", "mnames");
  names.dataset.provider = p.id;
  const q = p.models.length > 24 ? input("", t("filter {n} models…", { n: p.models.length })) : null;
  let showAll = false;
  const draw = () => {
    if (agentMenu && chips.contains(agentMenu.anchor)) closeAgentMenu();
    chips.replaceChildren();
    const f = (q?.value || "").trim().toLowerCase();
    let shown = 0, hidden = 0;
    // with none picked agents are served the vendor's list, up to 24 of
    // it: those are drawn as served, not as left out (#614: the one model
    // unticked and saved was still "1 model" on the card)
    const served = draft.chosen.length || decideOnly(p) || draft.unlisted ? new Set() : new Set(p.models.slice(0, 24).map((m) => m.id));
    // a filter is a search: what it doesn't match is left out, picked or not
    for (const m of p.models) {
      const on = draft.chosen.includes(m.id);
      if (f && !m.id.toLowerCase().includes(f) && !(m.name || "").toLowerCase().includes(f) && !(m.default || "").toLowerCase().includes(f)) continue;
      // past the first 80 only the picked are drawn, till the rest are
      // asked for: a picked model is never out of sight (#685: 10 picked,
      // 7 shown, and "4 more, filter to find them" with no names to filter by)
      if (shown >= 80 && !f && !showAll && !on) { hidden++; continue; }
      const c = el("button", "mchip" + (on ? " on" : served.has(m.id) ? " auto" : ""));
      c.append(el("span", "", m.name && m.name !== m.id ? m.name : m.id));
      // one the plan serves at no cost to it (WorkBuddy's x0.00 credits)
      // or one its vendor names free (#185)
      const free = m.free || namedFree(m.id, m.name);
      if (free) c.append(el("span", "badge free", t("free")));
      const rate = rateTag(m.rate, m.rateWas);
      if (rate) c.append(rate);
      const ctx = contextTag(m.context, m.name);
      if (ctx) c.append(ctx);
      if (m.default) c.title = `${m.id} · ${m.default}`;
      else if (m.name && m.name !== m.id) c.title = m.id;
      if (free) c.title = (c.title || m.id) + " · " + t(m.free ? "free: it doesn't use the plan's credits" : "free: so its name says");
      if (served.has(m.id)) c.title = (c.title || m.id) + "\n" + t("Agents see it: none are picked, so the vendor's list is used");
      tested(c, m.id);
      menu(c, m.id);
      // a served one is on as a picked one is: a click leaves it out,
      // the rest of what is served picked in its place
      c.onclick = () => {
        draft.chosen = on ? draft.chosen.filter((x) => x !== m.id) : served.has(m.id) ? [...served].filter((x) => x !== m.id) : [...draft.chosen, m.id];
        draw();
      };
      chips.append(c);
      shown++;
    }
    if (hidden) chips.append(showRest(hidden, () => { showAll = true; draw(); }));
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
    why.textContent = decideOnly(p) ? t("Agents never see them: a routing group picks one as its classifier.")
      : draft.unlisted ? t("Agents don't see them: only the routing groups they are in use them.")
      : t(draft.chosen.length ? "Agents see the models picked." : "None picked: agents see the vendor's list, up to {n}. To show them none, tick Only through routing groups, or switch the provider off.", { n: 24 });
    drawLost();
  };
  // those of its models in no routing group, while it is kept for groups:
  // nothing can use them, which is said here rather than left for the
  // reader to find them gone from every picker; each, once that is saved,
  // makes a group of itself at a click
  const drawLost = () => {
    lost.replaceChildren();
    const ids = draft.chosen.length ? draft.chosen : p.models.filter((m) => m.on).map((m) => m.id);
    const none = decideOnly(p) || !draft.unlisted ? [] : ids.filter((id) => !p.groups?.[id]?.length && !decidesModel(p, id));
    lost.hidden = !none.length;
    if (!none.length) return;
    lost.append(t("In no routing group, so no agent can use them now: {models}.", { models: none.slice(0, 8).join(", ") + (none.length > 8 ? " …" : "") }));
    if (!p.unlisted) { lost.append(" " + t("Once saved, make a group of them in Routing.")); return; }
    for (const id of none.slice(0, 4)) {
      const b = el("button", "text action", t("Make a routing group of {model}", { model: id }));
      b.onclick = (ev) => { if (mode !== "window") api("window/main?view=routing&newgroup=" + encodeURIComponent(p.id + "/" + id), {}); else window.newGroupWith?.(p.id + "/" + id, "", ev); };
      lost.append(b);
    }
  };
  // the names and reasoning levels of the models agents see, and whether
  // they see images: staged in draft.modelPrefs and made with the editor's
  // Save, as the rest of it is, Cancel dropping them. Each tick used to be
  // saved on its own, every one rewriting the agents' files, so picking a
  // model's levels lagged a click behind (ARNO on Discord).
  const drawNames = () => {
    names.replaceChildren();
    names.hidden = naming !== p.id;
    if (names.hidden) return;
    const ids = draft.chosen.length ? draft.chosen : p.models.filter((m) => m.on).map((m) => m.id);
    if (!ids.length) { names.append(el("span", "hint", t("Pick a model first."))); return; }
    const prefs = draft.modelPrefs = draft.modelPrefs || {};
    draft.onModelPrefs = drawNames;
    // the APIs a model can be asked on alone: those of a key's provider
    // it has a URL for, when it has more than one (01huadalang on Discord:
    // 一个 api 里有很多模型但是不同协议)
    const apis = modelAPIs(p);
    for (const id of ids) {
      const m = p.models.find((x) => x.id === id) || { id, name: id };
      const own = m.default || m.name || m.id;
      // what is staged for it, and what it is saved as
      const pref = () => prefs[id] = prefs[id] || {};
      const savedName = m.default ? m.name : "";
      const levels = m.efforts || [];
      const savedKept = m.kept?.length || m.given ? m.kept || [] : levels;
      const nameNow = () => prefs[id]?.name ?? savedName;
      const keptNow = () => prefs[id]?.efforts ? (prefs[id].efforts.length || m.given ? prefs[id].efforts : levels) : savedKept;
      const imagesNow = () => prefs[id]?.ownImages ? !!m.ownImages : prefs[id]?.images ?? !!m.images;
      const row = el("div", "mname");
      const name = input(nameNow(), own);
      name.title = t("The name agents and magpie show for {id}; empty for its own", { id: m.id });
      name.onchange = () => {
        const v = name.value.trim();
        if (v === savedName) delete pref().name; else pref().name = v;
        drawReset();
      };
      name.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") name.blur(); else if (e.key === "Escape") { name.value = nameNow(); name.blur(); } };
      const who = el("div", "mwho");
      who.append(name, el("code", "", m.id));
      row.append(who);
      // the model of other providers' it is, for the routing groups magpie
      // finds, when its id is one no rule matches up with theirs
      // (Volcengine's deepseek-v4-1-flash-260910 and others'
      // deepseek-v4.1-flash, #583); empty merges it by its own id, which
      // the box shows as it is merged
      const sameNow = () => prefs[id]?.same ?? m.same ?? "";
      const sameBox = el("label", "msame");
      const same = input(sameNow(), m.merge || m.id);
      same.title = t("The model other providers serve that {id} is the same as: the routing groups magpie finds put them together. Empty: by its own id", { id: m.id });
      same.spellcheck = false;
      same.onchange = () => {
        const v = same.value.trim();
        if (v === (m.same || "")) delete pref().same; else pref().same = v;
        drawReset();
      };
      same.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") same.blur(); else if (e.key === "Escape") { same.value = sameNow(); same.blur(); } };
      sameBox.append(el("span", "", t("Same as")), same);
      row.append(sameBox);
      const [img, imgCb] = tick(t("Accepts images"), imagesNow());
      img.title = t("Whether agents are told {id} can see images", { id: m.id });
      imgCb.onchange = () => {
        const x = pref();
        delete x.ownImages;
        if (imgCb.checked === !!m.images) delete x.images;
        else x.images = imgCb.checked;
        drawReset();
      };
      row.append(img);
      const apiNow = () => prefs[id]?.api ?? m.api ?? "";
      let apiSeg = null;
      if (apis.length || m.api) {
        const items = [["", t("Auto")], ...apis.map(([k, l]) => [k, l])];
        if (m.api && !items.some(([k]) => k === m.api)) items.push([m.api, (PROTOS.find(([k]) => k === m.api) || [])[1] || m.api]);
        apiSeg = segs(items, apiNow(), (v) => {
          if (v === (m.api || "")) delete pref().api; else pref().api = v;
          drawReset();
        });
        apiSeg.classList.add("mapi");
        apiSeg.title = t("The API {id} is asked on. Auto: as the vendor's list says, else each URL the provider has; pick one when the vendor serves it on that one only", { id: m.id });
        for (const [i, b] of [...apiSeg.querySelectorAll(".opt")].entries()) {
          b.dataset.api = items[i][0];
          if (items[i][0]) b.title = t((PROTOS.find(([k]) => k === items[i][0]) || [])[2] || "");
        }
        row.append(apiSeg);
      }
      // a model whose levels aren't known (m.given) can be given any
      // of them, and none again
      const boxes = [];
      if (levels.length > 1) {
        const lv = el("div", "mlevels");
        lv.title = t(m.given ? "Its reasoning levels aren't known: tick the ones it takes" : "Reasoning levels agents are offered");
        for (const l of levels) {
          const [tk, cb] = tick(t(l), keptNow().includes(l));
          cb.onchange = () => {
            const kept = keptNow();
            const next = levels.filter((x) => x === l ? cb.checked : kept.includes(x));
            if (!next.length && !m.given) { cb.checked = true; status(t("Keep at least one level"), "err"); return; }
            const all = next.length === levels.length && !m.given;
            if (all ? !m.kept?.length : next.join() === savedKept.join()) delete pref().efforts;
            else pref().efforts = all ? [] : next;
            drawReset();
          };
          boxes.push([l, cb]);
          lv.append(tk);
        }
        row.append(lv);
      }
      // what the Save will make of it differs from what it is: said beside
      // it, so a change not yet saved isn't taken for one made
      const unsaved = el("span", "hint munsaved", t("unsaved"));
      unsaved.title = t("Made when the provider is saved; Cancel drops it");
      const reset = el("button", "text action", t("Restore default"));
      reset.title = t("Its own name, every reasoning level it has, whether it sees images, the API it is asked on, and the model it is the same as");
      reset.onclick = () => {
        prefs[id] = {};
        if (m.default) prefs[id].name = "";
        if (m.kept?.length) prefs[id].efforts = [];
        if (m.imageSet) prefs[id].ownImages = true;
        name.value = nameNow();
        imgCb.checked = imagesNow();
        for (const [l, cb] of boxes) cb.checked = keptNow().includes(l);
        if (m.api) prefs[id].api = "";
        if (m.same) prefs[id].same = "";
        same.value = sameNow();
        if (apiSeg) { for (const b of apiSeg.querySelectorAll(".opt")) b.classList.toggle("on", b.dataset.api === apiNow()); slide(apiSeg, "api"); }
        drawReset();
      };
      const drawReset = () => {
        const x = prefs[id];
        if (x && !Object.keys(x).length) delete prefs[id];
        unsaved.hidden = !prefs[id];
        // staged back to its own already, there is nothing to restore
        const images = prefs[id]?.ownImages ? false : prefs[id]?.images !== undefined ? prefs[id].images !== !!m.ownImages : !!m.imageSet;
        const custom = nameNow() !== "" || (prefs[id]?.efforts ? prefs[id].efforts.length > 0 : !!m.kept?.length) || images || apiNow() !== "" || sameNow() !== "";
        reset.hidden = !custom;
      };
      row.append(unsaved, reset);
      drawReset();
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
  // just the free ones (Discord: 是否可以支持只选择免费模型), offered only
  // when the list has both free and paid models: with none free it would
  // pick nothing, with all free it is Select all
  const isFree = (m) => m.free || namedFree(m.id, m.name);
  if (p.models.some(isFree) && !p.models.every(isFree)) {
    const freeOn = el("button", "text action", t("Free only"));
    freeOn.title = t("Pick only the free models (those the filter shows), unpicking the others");
    freeOn.onclick = () => {
      const f = (q?.value || "").trim().toLowerCase();
      draft.chosen = p.models.filter((m) => isFree(m) && (!f || m.id.toLowerCase().includes(f) || (m.name || "").toLowerCase().includes(f) || (m.default || "").toLowerCase().includes(f))).map((m) => m.id);
      draw();
    };
    bulk.append(freeOn);
  }
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
      const r = await api("provider/models", { ...asTyped(), id: p.id });
      status(t("{p}: {n} models", { p: p.name, n: r.count }), "ok");
      // the redraw keeps the editor's draft, the picks in it with it: none
      // are put back by hand, which put them in whichever editor was open
      // by then, another provider's too (#464)
      await loadProviders();
    } catch (e) { status(e.message, "err"); refresh.classList.remove("busy"); }
  };
  // each model the agents see gets a tiny request of its own: a vendor
  // that answers can still have a model that doesn't
  const testAll = el("button", "text action", t("Test models"));
  testAll.title = t("Send a tiny request to each model agents see, to find the ones that don't answer") + "\n" + t("Right-click a model to test just it");
  testAll.onclick = async () => {
    const ids = (draft.chosen.length ? draft.chosen : p.models.filter((m) => m.on).map((m) => m.id)).filter((id) => !decidesModel(p, id));
    if (!ids.length) { status(t("Pick a model first."), "err"); return; }
    testAll.classList.add("busy");
    const got = modelTests[p.id] = {};
    for (const id of ids) got[id] = null;
    draw();
    try {
      const r = await api("provider/test", { ...asTyped(), id: p.id, test: ids });
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
      const r = await api("provider/test", { ...asTyped(), id: p.id, test: [id] });
      const x = got[id] = r.results[0];
      const via = x.protocol ? apiLabel(x.protocol) : "";
      const error = (x.status ? x.status + " · " : "") + x.error;
      status(x.ok ? (via ? t("{model} answered via {api} in {took}", { model: id, api: via, took: ledTook(x.ms) }) : t("{model} answered in {took}", { model: id, took: ledTook(x.ms) }))
        : via ? t("{model} didn't answer via {api}: {error}", { model: id, api: via, error }) : t("{model} didn't answer: {error}", { model: id, error }), x.ok ? "ok" : "err");
    } catch (e) { delete got[id]; status(e.message, "err"); }
    draw();
  };
  const rename = el("button", "text action" + (naming === p.id ? " on" : ""), t("Names & levels"));
  rename.title = t("Rename the models agents see, or offer fewer of their reasoning levels");
  rename.onclick = () => { naming = naming === p.id ? null : p.id; rename.classList.toggle("on", naming === p.id); drawNames(); };
  foot.append(add, refresh);
  if (noTest) { testAll.disabled = true; testAll.title = noTest; }
  foot.append(testAll);
  foot.append(rename);
  if (p.fetched) foot.append(el("span", "hint", t("vendor list · {when}", { when: ago(p.fetched) })));
  // a signed-in account's list, until the vendor gives one, is magpie's own
  else if (p.models.length) foot.append(el("span", "hint", t(p.account ? "magpie's list · Refresh asks the vendor" : decideOnly(p) ? "Jev's names · Refresh asks the vendor" : "from models.dev · Refresh asks the vendor")));
  if (p.fetched && !p.account) {
    // the fetched list stands in for the picks when none are made
    const forget = el("button", "text action", t("Forget"));
    forget.title = t("Drop the list fetched from the vendor; the models.dev one is used until Refresh");
    forget.onclick = async () => {
      forget.classList.add("busy");
      try { await api("provider/unfetch", { id: p.id }); await loadProviders(); } // the picks stay in the draft, as on a Refresh
      catch (e) { status(e.message, "err"); forget.classList.remove("busy"); }
    };
    foot.append(forget);
  }
  box.append(foot);
  const why = el("div", "hint");
  const [tk, cb] = tick(t("Only through routing groups"), !!draft.unlisted);
  cb.onchange = () => { draft.unlisted = cb.checked; draw(); };
  tk.title = t("Its models leave the list agents pick from; the routing groups they are in still use them");
  if (!decideOnly(p)) box.append(tk);
  const lost = el("div", "hint model-hint warn");
  box.append(why, lost);
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
// accounts a provider has on, and how long a conversation stays with the
// one that answered it. Both are staged in the editor's draft (routing,
// affinity) and made with its Save, as the rest of the editor is: a click
// shows the option and what it does, nothing is sent or redrawn, and
// Cancel drops it (01huadalang on Discord: each click saved and redrew,
// so looking through what each says lagged).
const ROUTINGS = [
  ["", "Smart", "The first takes requests while it has quota to spare; when it runs low, the one with the most left takes over. One out of credit sits out half an hour, one out of quota until it resets, one rate limited as long as the vendor asks, and one that fails a minute, longer each time it fails again."],
  ["order", "In order", "Requests go to the first; the next takes over when the one before runs out of quota, hits a rate limit or fails."],
  ["rotate", "In turn", "Each turn of a conversation goes to the next one, spreading the load evenly; the requests within a turn stay where it began, so the prompt cache holds, and one that fails is passed over while it rests."],
  ["usage", "Least used first", "Each request goes to the one used least: a subscription by the share of its allowance used, a key by the tokens it served in the last hours."],
  ["pace", "Weekly pace", "Each request goes to the subscription with the most of its week left per hour until it renews — the one with the most to lose at its reset — so less of each week is lost at its reset; one at 90% or more waits until the others can't answer. A key goes by the tokens it served in the last hours."],
];
// as on the Routing page's list of these (routing.js AFF_OPTS)
const STAYS = [
  ["", "Auto", "A conversation stays with the account or key that answered it while what the vendor cached of it is worth keeping — within a turn always, across turns while it's fresh."],
  ["session", "Session", "A conversation stays with the account or key that answered it for the whole session, while it can answer."],
  ["turn", "Within a turn", "A conversation stays put within a turn, while the agent sends tool results back; when you speak again, routing decides afresh."],
  ["off", "Off", "Every request is routed afresh, whoever answered its conversation before."],
];
// routingOfDraft: what the editor's Save sends of them — only what was
// picked and differs from what is saved
function routingOfDraft(p) {
  const out = {};
  if (draft?.routing !== undefined && draft.routing !== (p.routing || "")) out.routing = draft.routing;
  if (draft?.affinity !== undefined && draft.affinity !== (p.affinity || "")) out.affinity = draft.affinity;
  return out;
}
function renderRouting(p) {
  const routingNow = () => draft.routing ?? (p.routing || "");
  const affinityNow = () => draft.affinity ?? (p.affinity || "");
  // what Codex or Claude Code sends past magpie goes to the account it is
  // signed in to, which magpie moves on once Smart would count it spent
  // and back once the first has room (provider.KeepOnAnAccountWithRoom,
  // #209, #408)
  const a = p.account;
  // — at 98%, In order once used up (#530), never when kept on the first (#524)
  const own = (routing) => !a || (a.agent !== "codex" && a.agent !== "claude") ? ""
    : " " + (keptLogin(p)
      ? t("Routing picks the account for each request through magpie, in the accounts' order; {agent} on its own stays signed in to {user}, whatever it has left.", { agent: a.agentName, user: keptLogin(p) })
      : p.keepLogin
      ? t("Routing picks the account for each request through magpie; {agent} on its own stays signed in to the first account, whatever it has left.", { agent: a.agentName })
      : routing === "order"
        ? t("Routing picks the account for each request through magpie; {agent} on its own uses the one it is signed in to, which magpie moves to the next ticked account with room once it is used up, and back to the first once that has room again.", { agent: a.agentName })
        : t("Routing picks the account for each request through magpie; {agent} on its own uses the one it is signed in to, which magpie moves to the next ticked account with room once it is 98% used, and back to the first once that has room again.", { agent: a.agentName }));
  // what the Save will make of it differs from what it is: said beside it
  const unsaved = () => {
    const u = el("span", "hint munsaved", t("unsaved"));
    u.title = t("Made when the provider is saved; Cancel drops it");
    return u;
  };
  const rHint = el("div", "hint"), rUnsaved = unsaved();
  const drawRouting = () => {
    const cur = ROUTINGS.find(([id]) => id === routingNow()) || ROUTINGS[0];
    rHint.textContent = t(cur[2]) + own(cur[0]);
    rUnsaved.hidden = routingNow() === (p.routing || "");
  };
  const rPick = segs(ROUTINGS.map(([id, name]) => [id, t(name)]), routingNow(), (routing) => { draft.routing = routing; drawRouting(); });
  const rRow = el("div", "route-pick");
  rRow.append(rPick, rUnsaved);
  const rWrap = el("div");
  rWrap.append(rRow, rHint);
  const sHint = el("div", "hint"), sUnsaved = unsaved();
  const drawStays = () => {
    const cur = STAYS.find(([id]) => id === affinityNow()) || STAYS[0];
    sHint.textContent = t(cur[2]);
    sUnsaved.hidden = affinityNow() === (p.affinity || "");
  };
  const sPick = segs(STAYS.map(([id, name]) => [id, t(name)]), affinityNow(), (affinity) => { draft.affinity = affinity; drawStays(); });
  const sRow = el("div", "route-pick");
  sRow.append(sPick, sUnsaved);
  const sWrap = el("div");
  sWrap.append(sRow, sHint);
  drawRouting();
  drawStays();
  return [el("label", "", t("Routing")), rWrap, el("label", "", t("Stays")), sWrap];
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
    if (!o.ready || o.off) continue;
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
    // only the first dozen are drawn: the rest are said to be there
    if (hits.length > 12) sugg.append(el("span", "hint", t("{n} more — type to narrow", { n: hits.length - 12 })));
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
//
// Some of these are deprecated: once moved onto their community plugin
// (providers.onPlugins, the default for a new sign-in), the plugin signs in
// and serves them, and subOf hands back the plugin's sign-in. A change to
// such a built-in's sign-in on the Go side doesn't reach those users; see
// AGENTS.md (PLUGIN-SERVED).

const SUBS = [
  // both can also come from CLIProxyAPI's auth files or the agent's own (importing below)
  // Anthropic has banned accounts it saw used from other tools: said before one is added
  { agent: "claude", name: "Claude", icon: "claude-color", plans: "Pro · Max · Team", importable: true, risk: true,
    riskNote: "Anthropic may suspend or ban a Claude account it sees used outside its own apps. magpie sends requests through Claude Code, but Anthropic may still act on them; you use it at your own risk. Use an account you can afford to lose." },
  { agent: "codex", name: "ChatGPT", icon: "openai", plans: "Plus · Pro · Business", importable: true },
  // cursor-agent keeps one account; signing in again replaces it
  { agent: "cursor", name: "Cursor", icon: "cursor", plans: "Pro · Ultra · Teams", single: true },
  // so does Grok Build
  { agent: "grok", name: "Grok (SuperGrok)", icon: "xai", plans: "SuperGrok · X Premium+", own: true },
  // signed in with GitHub's device code; the editors' own sign-in stays theirs
  { agent: "copilot", name: "Copilot", icon: "githubcopilot", plans: "Free · Education · Pro · Pro+ · Business · Enterprise", own: true },
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
  // Qoder's two sites: qoder.com, and Qoder CN (qoder.cn), where accounts made
  // with Alibaba Cloud or a phone number live and can't sign in on qoder.com;
  // hint says which accounts each is for
  { agent: "qoder", get name() { return t("Qoder (international)"); }, icon: "qoder", plans: "Pro", own: true, risk: true,
    hint: "For accounts on qoder.com, the international site.",
    riskNote: "Qoder has no public API for this; magpie signs requests as its desktop client would, which Qoder may treat as third-party use and act on. Use an account you can afford to lose." },
  { agent: "qoder-cn", name: "Qoder CN", icon: "qoder", plans: "Pro", own: true, risk: true,
    hint: "For accounts on qoder.cn: signed in with an Alibaba Cloud account or a phone number.",
    riskNote: "Qoder has no public API for this; magpie signs requests as its desktop client would, which Qoder may treat as third-party use and act on. Use an account you can afford to lose." },
  // the devin CLI's own account is read; more are signed in beside it, each in a data folder of magpie's
  { agent: "devin", name: "Devin", icon: "devin", plans: "Pro · Enterprise", own: true },
  // Zed's hosted models (Zed Pro, its trial), signed in at zed.dev as the editor is
  { agent: "zed", name: "Zed", icon: "zed", plans: "Pro · Student · Business", own: true, risk: true,
    riskNote: "Zed serves these models to its own editor; magpie signs requests as the editor would, which Zed may treat as third-party use and act on. Use an account you can afford to lose." },
  // Factory's plans (Droid's account), signed in with WorkOS's device code as droid does; droid's own login stays its own
  // or added by an API key (fk-…), as droid takes FACTORY_API_KEY (the import box)
  { agent: "factory", name: "Factory", icon: "factory", plans: "Pro · Plus · Max", own: true, risk: true, importable: true,
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
// subOf: the subscription an agent id is. One moved onto its plugin signs
// in through the plugin, still named, drawn, warned about and asked where
// as it was; it keeps as many accounts as the plugin does (Cursor's took
// one, its plugin takes more).
const subOf = (agent) => {
  const own = SUBS.find((x) => x.agent === agent);
  const pl = pluginSubs().find((x) => x.agent === agent);
  if (own && pl && movedSub(agent)) {
    const { name, icon, plans, risk, riskNote, hint, sites, importable } = own;
    // a Factory API key is kept by the built-in, which the plugin replaces
    return { ...pl, name, icon, plans, risk, riskNote, hint, sites, importable: importable && agent !== "factory", moved: true };
  }
  return own || pl;
};
const movedSub = (agent) => (providers?.onPlugins || []).includes(agent);

// pluginMethod: the plugin's way to sign in a moved built-in takes when
// the built-in had one click — its first, the browser's, or on a site the
// one for that site (ZCode's "ZCode: Z.ai GLM Coding Plan"). None for a
// plugin's own provider, which asks.
function pluginMethod(sub, site) {
  if (!sub.moved) return undefined;
  const label = (sub.sites || []).find(([id]) => id === site)?.[1];
  const at = label ? sub.plugin.methods.findIndex((m) => (m.label || "").includes(label)) : -1;
  return at < 0 ? 0 : at;
}

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
  if (agent === "factory") {
    return {
      from: t("Add accounts by their Factory API keys (fk-…), as Droid takes FACTORY_API_KEY"),
      title: t("Add Factory accounts by API key"),
      intro: t("Paste one or more Factory API keys (fk-…, from app.factory.ai/settings/api-keys), one a line, or choose a file with them. Each key is checked with Factory before it is added."),
      checking: t("Checking the keys with Factory…"),
      checks: t("Each key is asked whose account it is, as Droid does with FACTORY_API_KEY."),
      instead: t("Use an API key instead…"),
      row: t("Add an account by API key…"),
    };
  }
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
  if (subOf(agent)?.plugin) return startPluginSignIn(subOf(agent), pluginMethod(subOf(agent), site));
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
      if (signing.state !== st.state || signing.url !== st.url || signing.code !== st.code) { signing = { ...st, site: signing.site, method: signing.method }; renderProviders(); }
      continue;
    }
    if (st.state === "done") return signedIn(st);
    // a failed one keeps its site and way, for Try again
    signing = st.state === "canceled" ? null : { ...st, site: signing.site, method: signing.method };
    renderProviders();
  }
}

// signedIn: a sign-in done — the account opened in the editor.
async function signedIn(st) {
  signing = null;
  justAdded = st.user;
  delete loginUsage[st.agent]; // what was fetched before has nothing on the new account
  providers = await api("providers?wait=1"); // with its vendor's list (#204)
  const p = providers.providers.find((x) => x.account?.agent === st.agent);
  if (p) { editing = p.id; draft = null; adding = false; presetQuery = ""; }
  renderProviders();
  const who = st.user || subOf(st.agent)?.name || st.agent;
  // signed in but listed nowhere (#155): say so rather than "added"
  if (!p) status(t("{user} signed in, but magpie can't list it — please report this", { user: who }), "err");
  // an account listed already is said to be, not added (#413)
  else if (st.again) status(t("{user} is already listed — its sign-in was renewed", { user: who }), "ok");
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
  // the key's field is titled by the way's label, as OpenCode's dialog
  // is, unless that only says "API key"; the plugin's placeholder hints
  // at what the key looks like
  const way = key ? sub.plugin.methods?.[flow.method] || {} : {};
  const own = (way.label || "").trim();
  const label = key ? (own && own.toLowerCase() !== "api key" ? own : t("{name} API key", { name: sub.name })) : q.message;
  tt.append(el("span", "n", label));
  const inp = input(flow.value || "", key ? (way.placeholder || "") : (q.placeholder || ""), key ? "password" : "text");
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

// dropSigningIn: the editor of provider id closing (Cancel, Save, Remove)
// leaves no sign-in of its account waiting behind it, to come back with the
// editor (#526: 无论我点击底部的移除、取消、保存都没办法); an import being
// read is left to finish.
function dropSigningIn(id) {
  const p = typeof id === "string" && providers?.providers?.find((x) => x.id === id);
  if (!p?.account || !signing || signing.agent !== p.account.agent || signing.state === "importing") return;
  if (signing.id) api("signin/" + signing.id + "/cancel", {}).catch(() => {});
  signing = null;
}

// renderSigning: where a sign-in stands, in place of the button that
// started it — waiting on the browser, or what went wrong.
function renderSigning(sub) {
  const box = el("div", "signing" + (signing.state === "failed" ? " failed" : ""));
  const tt = el("span", "tt");
  if (signing.state === "risk") {
    box.append(el("span", "mark", "!"));
    tt.append(el("span", "n", t("{name} accounts can be suspended", { name: sub.name })));
    if (sub.hint) tt.append(el("span", "s", t(sub.hint)));
    tt.append(el("span", "s", t(sub.riskNote || "Google may suspend an Antigravity account it sees used outside Antigravity. Use one you can afford to lose.")));
    box.append(tt);
    const go = el("button", "text primary", t("Sign in anyway"));
    go.onclick = () => startSignIn(sub.agent, true);
    const close = el("button", "text", t("Cancel"));
    close.onclick = cancelSignIn;
    if (sub.importable) {
      const imp = el("button", "text", importSay(sub.agent).instead || t("Import instead…"));
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
      : signing.pasteCode ? t("magpie opened the sign-in page. Paste the code it shows below.")
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
  if (signing.pasteCallback || signing.pasteCode || signing.pasteKey) {
    const flow = signing;
    const what = flow.pasteCode ? t("Code") : flow.pasteKey ? t("API key") : t("Callback URL");
    if (flow.pasteKey) {
      // Command Code's page posts its key to magpie unseen: a browser that
      // can't reach magpie (Docker) has no address to paste, so a key made
      // on the keys page finishes it, as Command Code's CLI takes one
      const s = el("span", "s", t("If the page can't reach magpie (it runs on a server or in Docker), make an API key on {name}'s keys page and paste it here.", { name: sub.name }) + " ");
      if (flow.keysURL) {
        const keys = el("button", "link", t("Open the keys page"));
        keys.onclick = () => api("open", { url: flow.keysURL }).catch(() => {});
        s.append(keys);
      }
      tt.append(s);
    } else if (!flow.pasteCode) tt.append(el("span", "s", t("If the page the browser ends on won't load (magpie runs on a server or in Docker), copy its whole address and paste it here.")));
    const form = el("form", "callback-form");
    const url = input(flow.callbackURL || "", what, flow.pasteKey ? "password" : "text");
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
  // a plugin's browser sign-in whose page can't reach magpie (Docker: the
  // Command Code plugin's Studio posts its key to 127.0.0.1) can be left
  // for the plugin's own API key way, without starting over
  const keyWay = sub.plugin && signing.method != null && !signing.pasteCode && signing.state === "waiting"
    ? (sub.plugin.methods || []).findIndex((m) => m.type === "api") : -1;
  if (keyWay >= 0 && keyWay !== signing.method) {
    const acts = tt.querySelector(".acts") || tt.appendChild(el("span", "acts"));
    const k = el("button", "link", t("Use an API key instead"));
    k.title = t("If the page can't reach magpie (it runs on a server or in Docker)");
    k.onclick = () => {
      if (signing?.id) api("signin/" + signing.id + "/cancel", {}).catch(() => {});
      startPluginSignIn(sub, keyWay);
    };
    acts.append(k);
  }
  if (sub.importable) {
    // an account another tool is signed in to comes in from its file
    const imp = el("button", "link", importSay(sub.agent).instead || t("Import from a file instead…"));
    imp.title = importSay(sub.agent).from;
    imp.onclick = () => startImport(sub.agent);
    const acts = tt.querySelector(".acts") || tt.appendChild(el("span", "acts"));
    acts.append(imp);
  }
  // the one way to put it away (#526): a Close beside Open again did what
  // this does, two buttons for one thing
  const x = el("button", "text", t("Cancel"));
  x.title = t("Stop waiting for this sign-in");
  x.onclick = cancelSignIn;
  box.append(x);
  return box;
}

// The backend returns the native key/login order and the new First account.
// Do not keep a second display order that can disagree with routing.
let accountArranging = false, accountSaving = false, accountRenderPending = false;
function accountArrangementDone() {
  accountArranging = false;
  if (accountRenderPending) { accountRenderPending = false; renderProviders(); }
}
function arrangeAccountRows(list, p) {
  const rows = [...list.children].filter((r) => r.dataset.accountId);
  if (rows.length < 2) return;
  list.classList.add("reorderable");
  const move = async (row, to) => {
    const current = [...list.children].filter((r) => r.dataset.accountId);
    const from = current.indexOf(row);
    if (accountSaving || to < 0 || to >= current.length || to === from) return;
    accountSaving = accountArranging = true;
    list.setAttribute("aria-busy", "true");
    const before = [...list.children];
    const focus = document.activeElement === row;
    list.insertBefore(row, to > from ? current[to].nextSibling : current[to]);
    current.splice(to, 0, ...current.splice(from, 1));
    if (focus) row.focus({ preventScroll: true });
    const order = current.map((r) => r.dataset.accountId);
    try {
      providers = await api("provider/arrange", { id: p.id, accountOrder: order });
      accountRenderPending = true;
      status(t("Account order saved"), "ok");
    } catch (e) {
      list.replaceChildren(...before);
      // A failed account switch can still have refreshed the agent's sign-in.
      // Reconcile with the backend rather than claiming a local rollback undid it.
      try { providers = await api("providers"); } catch (_) { /* keep the last known list */ }
      accountRenderPending = true;
      status(e.message, "err");
    } finally {
      accountSaving = false;
      list.removeAttribute("aria-busy");
      accountArrangementDone();
      if (focus) document.querySelector(`.accts [data-account-id="${CSS.escape(row.dataset.accountId)}"]`)?.focus({ preventScroll: true });
    }
  };
  for (const row of rows) {
    row.tabIndex = 0;
    row.setAttribute("role", "group");
    row.setAttribute("aria-label", row.querySelector(".n").textContent + " · " + t("Drag to reorder · Alt+↑/↓ to move"));
    row.title = t("Drag to reorder · Alt+↑/↓ to move");
    row.addEventListener("click", (e) => {
      if (row.dataset.dragged) { e.preventDefault(); e.stopImmediatePropagation(); }
    }, true);
    row.onkeydown = (e) => {
      if (e.target !== row || !e.altKey || !["ArrowUp", "ArrowDown"].includes(e.key) || accountArranging) return;
      e.preventDefault(); e.stopPropagation();
      const current = [...list.children].filter((r) => r.dataset.accountId);
      move(row, current.indexOf(row) + (e.key === "ArrowUp" ? -1 : 1));
    };
    row.onpointerdown = (e) => {
      if (accountArranging || e.target.closest("input, textarea, select, a, [contenteditable=true], button:not(.rename)")) return;
      // Text selection is native; drag the row's background/empty space instead.
      if (e.target.closest(".n:not(button), .plan, .aq, .acct-models")) return;
      const current = [...list.children].filter((r) => r.dataset.accountId);
      accountArranging = dragRows(e, row, row, list, current, (to) => move(row, to), () => {},
        () => { if (!accountSaving) accountArrangementDone(); });
    };
  }
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

// keptLogin: the account Codex or Claude Code is kept signed in to in
// place of the first (#524), "" when none — the gateway then tries the
// accounts in their order, the one signed in to at its own place
function keptLogin(p) {
  const a = p?.account;
  if (!a || (a.agent !== "codex" && a.agent !== "claude") || !p.keepLogin || !p.keepLoginAs) return "";
  const as = p.keepLoginAs.toLowerCase();
  return (a.logins || []).find((l) => (l.user || "").toLowerCase() === as)?.user || "";
}

// loginsInOrder: an agent's accounts as its provider lists them and the
// gateway tries them, the one it is signed in to first — unless it is
// kept signed in to one of the user's choosing (p given), when the order
// is the accounts' own
function loginsInOrder(a, p) {
  const ls = a.logins?.length ? [...a.logins] : [{ user: a.user, plan: a.plan, active: true, on: true }];
  if (p && keptLogin(p)) return ls;
  return ls.sort((x, y) => (y.active ? 1 : 0) - (x.active ? 1 : 0));
}

function renderAccounts(a, p) {
  const sub = subOf(a.agent);
  const list = el("div", "accts");
  const ls = loginsInOrder(a, p);
  const several = ls.filter((l) => (l.active && !l.paused) || l.on).length > 1;
  // kept signed in to one of the user's choosing (#524), the first is the
  // first in use in the order, which Make first sets without a sign-in
  const kept = keptLogin(p);
  const firstUser = kept ? ls.find((l) => (l.active || l.on) && !l.paused && !l.lapsed)?.user : ls.find((l) => l.active)?.user;
  const makeFirst = (l) => {
    const b = el("button", "text", t("Make first"));
    b.title = t("The gateway uses this account first; {agent} stays signed in to {user}", { agent: a.agentName, user: kept });
    b.onclick = () => { b.classList.add("busy"); accountAction("provider/arrange", { id: p.id, accountOrder: [l.user, ...ls.filter((x) => x !== l).map((x) => x.user)] }, t("The gateway now uses {user} first", { user: l.user })); };
    return b;
  };
  // the account Claude Code or Codex is signed in to can be paused while
  // another is on: the gateway passes over it, the agent staying signed in
  // to it (#263)
  const pausable = (a.agent === "claude" || a.agent === "codex") && ls.some((l) => !l.active && l.on);
  const quota = loginUsageOf(a.agent);
  // the first, which magpie signed the agent out of while it was spent:
  // it is signed back in once it has room (#408)
  const back = ls.find((l) => l.returns && !l.active);
  for (const l of ls) {
    const on = !l.paused && (l.active || l.on);
    const row = el("div", "acc" + (on ? " in-use" : " off") + (l.user === justAdded ? " new" : ""));
    row.dataset.accountId = l.user;
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
    row.append(dot, el("span", "n", l.user), el("span", "plan", accountPlan({ agent: a.agent, builtin: a.builtin, plan: l.plan })));
    // its own models (#474), when there is another account to send the rest to
    const [amPill, amBox] = ls.length > 1 || accountModelsOf(p, l.user).length ? accountModels(p, l.user, false, l.user) : [];
    if (amPill) row.append(amPill);
    row.append(el("span", "grow"));
    if (l.active && kept && l.user !== firstUser) {
      // signed in to, kept so, and tried at its place in the order
      const signed = el("span", "using", l.paused ? t("Paused") : t("Signed in"));
      signed.title = t("{agent} is kept signed in to this account; requests through magpie go to the accounts in their order", { agent: a.agentName });
      row.append(signed);
      if (on) row.append(makeFirst(l));
    } else if (l.active) {
      const using = el("span", "using", l.paused ? t("Paused") : back ? t("First for now") : several ? t("First") : t("In use"));
      if (back && !l.paused) using.title = t("{user} was nearly used up, so magpie signed {agent} in to this one; it goes back to {user} once that has room again", { user: back.user, agent: a.agentName });
      row.append(using);
      if (a.agent === "qoder" || a.agent === "qoder-cn" || l.own) {
        const forget = el("button", "text quiet", t("Remove"));
        if (l.own) forget.title = forgetOwnTitle(a);
        forget.onclick = () => accountAction("login/forget", { agent: a.agent, user: l.user }, t("{user} removed", { user: l.user }));
        row.append(forget);
      }
    } else {
      const forget = el("button", "text quiet", t("Remove"));
      forget.title = l.own ? forgetOwnTitle(a) : t("magpie forgets this account's sign-in; the account itself is untouched");
      forget.onclick = () => accountAction("login/forget", { agent: a.agent, user: l.user }, t("{user} removed", { user: l.user }));
      if (l === back) {
        const again = el("span", "using", t("First again once it has room"));
        again.title = t("magpie signs {agent} back in to this account once it has room again", { agent: a.agentName });
        row.append(again);
      }
      if (kept) {
        // the sign-in stays where it is kept: Make first is the order's,
        // and an account not in use is ticked on by its dot
        row.append(forget);
        if (l.user === firstUser) row.append(el("span", "using", t("First")));
        else if (on) row.append(makeFirst(l));
      } else {
        const use = el("button", "text", on ? t("Make first") : t("Use"));
        use.title = sub?.own ? t("The gateway uses this account first") : t("Sign {agent} in to this account", { agent: a.agentName });
        use.onclick = () => { use.classList.add("busy"); accountAction("login/switch", { agent: a.agent, user: l.user }, sub?.own ? t("The gateway now uses {user} first", { user: l.user }) : t("{agent} is now signed in as {user}", { agent: a.agentName, user: l.user })); };
        row.append(forget, use);
      }
    }
    row.append(accountQuota(l.lapsed ? { [l.user]: { error: l.lapsed } } : quota, l.user));
    row.classList.add("with-aq"); // not :has(.aq), which Safari 15.0 lacks (#220)
    if (amBox) row.append(amBox);
    list.append(row);
  }
  // magpie signs Codex or Claude Code in to the next account when the
  // first runs low (#209, #408); this keeps it on the first instead, or on
  // an account of the user's choosing, the gateway still spreading requests
  // over the ticked ones in their order (#524)
  if ((a.agent === "codex" || a.agent === "claude") && several && p) {
    const box = el("div", "keep-login");
    const [keep, cb] = tick(t("Keep {agent} signed in to", { agent: a.agentName }), !!p.keepLogin);
    keep.title = t("magpie won't sign {agent} in to another account when the first runs low; requests through magpie still go to the other ticked accounts as Routing says", { agent: a.agentName });
    // the app's own menu, as every other pick in it, not a native select
    let keepAs = kept;
    const opts = [{ v: "", name: t("the first account"), note: "", literalName: true }];
    for (const l of ls) if (!l.lapsed) opts.push({ v: l.user, name: l.user, note: "", literalName: true });
    const as = el("button", "sess-pick keep-as" + (keepAs ? " set" : ""));
    as.type = "button";
    as.title = t("The account {agent} stays signed in to; the first is the one the gateway uses first", { agent: a.agentName });
    as.dataset.value = keepAs;
    as.setAttribute("aria-haspopup", "menu");
    as.setAttribute("aria-expanded", "false");
    as.append(el("span", "", keepAs || t("the first account")), svg(CHEV, 11, 1.6));
    const post = (keepLogin) => {
      const body = { id: p.id, keepLogin };
      if (keepLogin && keepAs) body.keepLoginAs = keepAs;
      accountAction("provider/keeplogin", body, !keepLogin ? t("magpie moves {agent} to an account with room again", { agent: a.agentName })
        : keepAs ? t("{agent} stays signed in to {user}", { agent: a.agentName, user: keepAs }) : t("{agent} stays signed in to the first account", { agent: a.agentName }));
    };
    cb.onchange = () => post(cb.checked);
    // picking one keeps it so, ticked or not
    as.onclick = (e) => {
      e.stopPropagation();
      if (as.classList.contains("open")) return closeProtoMenu();
      openProtoMenu(as, opts, keepAs, (v) => {
        if (v === keepAs && cb.checked) return;
        keepAs = v;
        as.dataset.value = v;
        as.classList.toggle("set", !!v);
        as.firstChild.textContent = v || t("the first account");
        cb.checked = true;
        post(true);
      }, "Account to stay signed in to", "sess-menu");
    };
    box.append(keep, as);
    list.append(box);
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
      imp.append(ic2, el("span", "n", importSay(a.agent).row || t("Import accounts from a file…")));
      imp.title = importSay(a.agent).from;
      imp.onclick = () => startImport(a.agent);
      list.append(imp);
    }
  }
  arrangeAccountRows(list, p);
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
  tt.append(el("span", "n", say.title || t("Import {name} accounts", { name: sub.name })), el("span", "s", say.intro));
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
  return balanceError(err) || t("Allowance unavailable");
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
  if (q?.asOf && !q.error) {
    line.classList.add("stale");
    line.append(el("span", "aq-asof", asOfText(q)));
  }
  // an account on no plan says so, whatever else can be read of it
  const noPlan = q?.plan === "No plan" ? [t("No plan")] : [];
  if (q && !q.error && !q.windows?.length && q.balance) {
    // no rolling limits, only what is left to spend
    line.append(el("span", "aq-none", [...noPlan, t("Balance") + " " + q.balance].join(" · ")));
    return line;
  }
  if (!q || q.error || !q.windows?.length) {
    line.append(el("span", "aq-none", [...noPlan, q?.error ? quotaError(q.error) : t("No allowance reported")].join(" · ")));
    if (q?.error) line.title = q.error;
    return line;
  }
  // the two rolling windows fit a line; the per-model ones go in its
  // tooltip; per-model windows of a family are the family's one
  const counted = q.windows.filter((w) => !w.unlimited);
  const ws = familyWindows(counted);
  if (!ws.length) { line.append(el("span", "aq-none", t("Unlimited"))); return line; }
  if (ws.some((w) => w.members)) return poolLine(line, ws, q);
  line.title = ws.slice(2).map((w) => w.tiers ? tiersText(w) : t(w.name) + " " + quotaText(w)).join(ws !== counted ? "\n" : " · ");
  if (q.asOf) line.title = [line.title, asOfText(q)].filter(Boolean).join("\n");
  for (const w of ws.slice(0, 2)) {
    const used = Math.max(0, Math.min(100, w.used));
    const m = el("span", "aq-w" + (used >= 90 ? " full" : ""));
    const track = el("span", "aq-track");
    const fill = el("i");
    fill.style.width = quotaFill(w) + "%";
    track.append(fill);
    m.append(el("span", "aq-n", t(w.name)), track, el("b", "", quotaText(w)));
    if (w.resetsAt) {
      const at = new Date(w.resetsAt);
      m.title = resetText(at, at.toLocaleString());
      if (used >= 80) m.append(el("span", "aq-r", resetInText(at)));
    }
    if (w.tiers) m.title = tiersText(w);
    line.append(m);
  }
  return line;
}

// poolLine: an account row's pools, each its name then its 5-hour and
// weekly meters (Gemini 5h ▬ 95% 7d ▬ 75%), two pools on the line and the
// rest in its tooltip.
function poolLine(line, ws, q) {
  const pools = [];
  for (const w of ws) {
    const k = w.members ? w.pool : "\0" + pools.length;
    const p = pools.find((x) => x.k === k);
    if (p) p.ws.push(w); else pools.push({ k, name: w.members ? w.pool : t(w.name), ws: [w] });
  }
  line.title = pools.slice(2).map((p) => p.ws.map((w) => t(w.name) + " " + quotaText(w)).join(" · ")).join("\n");
  if (q.asOf) line.title = [line.title, asOfText(q)].filter(Boolean).join("\n");
  for (const p of pools.slice(0, 2)) {
    const m = el("span", "aq-w aq-pool" + (p.ws.some((w) => w.used >= 90) ? " full" : ""));
    m.append(el("span", "aq-n", p.name));
    for (const w of p.ws) {
      const track = el("span", "aq-track");
      const fill = el("i");
      fill.style.width = quotaFill(w) + "%";
      track.append(fill);
      m.append(el("span", "aq-k", w.window ? shortWindow(w.window) : ""), track, el("b", "", quotaText(w)));
    }
    const w = p.ws[0];
    m.title = p.ws.map((x) => t(x.name) + " " + quotaText(x) + (x.resetsAt ? " · " + resetText(new Date(x.resetsAt), new Date(x.resetsAt).toLocaleString()) : "")).join("\n")
      + (w.members ? "\n\n" + poolTip(w) : w.tiers ? "\n\n" + tiersText(w) : "");
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
  if (w.unlimited) return t("Unlimited");
  const used = Math.max(0, Math.min(100, w.used));
  const n = quotaLeft ? 100 - used : used;
  const pct = t(quotaLeft ? "{n} left" : "{n} used", { n: (Number.isInteger(n) ? n : n.toFixed(1)) + "%" });
  const count = quotaCount(w);
  return count ? count + " · " + pct : pct;
}
// quotaCount: a window's own count, when its vendor counts in amounts
// (WorkBuddy's credits, #659) — "355 / 500 credits" used, or what is left
// of them, as the share beside it says; the vendor's own text otherwise.
const countFormat = new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 });
function quotaCount(w) {
  if (!(w.limit > 0)) return w.display || "";
  const amount = Math.max(0, w.amount || 0), n = quotaLeft ? Math.max(0, w.limit - amount) : amount;
  return (countFormat.format(n) + " / " + countFormat.format(w.limit) + (w.unit ? " " + t(w.unit) : "")).trim();
}
// familyWindows: an account's per-model windows as one a model family.
// Antigravity reports each level of each model (Gemini 3.1 Pro (High), (Low),
// Gemini 3.7 Flash (Low), (Medium), (High)…), which over several accounts
// reads as a wall of meters (01huadalang on Discord); Gemini and Claude
// first, then any other. A family's figure is its tightest window, the most
// used, the soonest to start again of those; its windows go along as tiers
// for its tooltip and for "Every model". Windows that name no family, or an
// account with a window a family already, are as they were.
const FAMILY_FIRST = ["Gemini", "Claude"];
function familyWindows(ws) {
  if (ws?.some(isPool)) return poolWindows(ws);
  if (!ws?.some((w) => w.family)) return ws;
  const fams = new Map();
  for (const w of ws) {
    const k = w.family ? "f:" + w.family : "w:" + fams.size;
    if (!fams.has(k)) fams.set(k, []);
    fams.get(k).push(w);
  }
  if (fams.size === ws.length) return ws;
  const soon = (w) => (w.resetsAt ? new Date(w.resetsAt).getTime() : Infinity);
  const out = [];
  for (const tiers of fams.values()) {
    if (!tiers[0].family) { out.push(tiers[0]); continue; }
    const top = tiers.reduce((a, b) => (b.used > a.used || (b.used === a.used && soon(b) < soon(a)) ? b : a));
    out.push({ ...top, name: top.family, tiers });
  }
  const rank = (w) => (w.tiers && FAMILY_FIRST.includes(w.name) ? FAMILY_FIRST.indexOf(w.name) : FAMILY_FIRST.length);
  return out.map((w, i) => [w, i]).sort(([a, i], [b, j]) => rank(a) - rank(b) || i - j).map(([w]) => w);
}
// poolWindows: an account's windows one row a pool, where models share one
// allowance (Antigravity's Gemini, Claude & GPT): the pool's 5-hour window
// then its weekly one, each named with its pool, its models along as
// members for its tooltip (a user on Discord: the three models read the
// same, show the 5 hours and the week left a group). The models' windows
// in no pool go on as families.
const isPool = (w) => w.pool && !w.family;
function poolWindows(ws) {
  const pools = new Map();
  for (const w of ws.filter(isPool)) {
    if (!pools.has(w.pool)) pools.set(w.pool, []);
    pools.get(w.pool).push(w);
  }
  const hours = (w) => {
    const m = /^(\d+)\s*(hour|day|week)s?$/i.exec(w.name || "");
    return m ? m[1] * { hour: 1, day: 24, week: 168 }[m[2].toLowerCase()] : Infinity;
  };
  const first = (p) => { const i = FAMILY_FIRST.indexOf(p.split(/[ &]/)[0]); return i < 0 ? FAMILY_FIRST.length : i; };
  const out = [];
  for (const pool of [...pools.keys()].sort((a, b) => first(a) - first(b))) {
    const members = ws.filter((w) => w.family && w.pool === pool);
    for (const w of pools.get(pool).sort((a, b) => hours(a) - hours(b)))
      out.push({ ...w, name: pool + " · " + t(w.name), window: w.name, members });
  }
  const rest = ws.filter((w) => !isPool(w) && !(w.pool && pools.has(w.pool)));
  const fam = familyWindows(rest);
  return out.concat(fam);
}
// pooledModels: the models a whole account's card lists under "Every model"
// when its windows are a pool's: the models' own, not the pools' again.
const pooledModels = (ws) => (ws?.some(isPool) ? ws.filter((w) => !isPool(w)) : ws);
// poolTip: what a pool's window counts, and its models, for its tooltip.
function poolTip(w) {
  return t("{pool}: one allowance for these models", { pool: w.pool }) + "\n" + tiersText({ tiers: w.members });
}
// ringName: a window's name short enough for a ring: a pool's first word
// and its span (Gemini 5h, Claude 7d).
function ringName(w) {
  return w.window ? w.pool.split(/[ &]/)[0] + " " + shortWindow(w.window) : shortWindow(w.name);
}

// tiersText: a family's windows, one a line, for its tooltip.
function tiersText(w) {
  return (w.tiers || []).map((x) => t(x.name) + " " + quotaText(x)
    + (x.resetsAt && x.used > 0 ? " · " + resetInText(new Date(x.resetsAt)) : "")).join("\n");
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
  if (at <= Date.now()) return t("Reset time passed");
  const mins = Math.max(1, Math.round((at - Date.now()) / 60000));
  if (mins < 60) return t("in {n}m", { n: mins });
  const h = Math.round(mins / 60);
  if (h < 48) return t("in {n}h", { n: h });
  return t("in {n}d", { n: Math.round(h / 24) });
}

function resetText(at, when = at <= Date.now() ? at.toLocaleString() : resetClock(at)) {
  return t(at <= Date.now() ? "Reset time passed {when}" : "Resets {when}", { when });
}

function resetInText(at) {
  return at <= Date.now() ? t("Reset time passed") : t("resets {in}", { in: untilText(at) });
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

// An account's or key's own models (#474): one of several can be kept for
// some of the provider's models only — a small allowance kept for the cheap
// ones — and the gateway never sends it another. Unset, it serves all the
// provider's, as before. The pill on its row says which it serves; a click
// opens the provider's models under it as chips, picked and saved apart
// from the editor's Save. acctModels is the row being picked: { id, ref,
// chosen }.
let acctModels = null;
function accountModelsOf(p, ref) { return p.accountModels?.[String(ref).toLowerCase()] || []; }
// It gives the pill, and the open picker the row ends with, or null.
function accountModels(p, ref, isKey, name) {
  const own = accountModelsOf(p, ref);
  const pill = el("button", "amodels" + (own.length ? " set" : ""), own.length ? t(own.length === 1 ? "1 model" : "{n} models", { n: own.length }) : t("All models"));
  pill.title = own.length ? t("Serves only {models}", { models: own.join(", ") }) : t(isKey ? "Every model of the provider goes to this key. Click to keep it for some only" : "Every model of the provider goes to this account. Click to keep it for some only");
  const open = acctModels?.id === p.id && acctModels.ref === ref;
  pill.classList.toggle("open", open);
  pill.setAttribute("aria-expanded", open ? "true" : "false");
  pill.onclick = () => { acctModels = open ? null : { id: p.id, ref, chosen: [...own] }; renderProviders(); };
  if (!open) return [pill, null];
  const box = el("div", "acct-models");
  const chips = el("div", "mchips");
  // the models the provider serves agents, and any the account has that it no longer lists
  const ids = p.models.filter((m) => m.on).map((m) => m.id);
  for (const id of acctModels.chosen) if (!ids.includes(id)) ids.push(id);
  const foot = el("div", "acm-foot");
  const save = el("button", "text primary", t("Save"));
  const post = async (allow, msg) => {
    const was = acctModels;
    acctModels = null;
    if (!await accountAction("provider/accountmodels", { id: p.id, account: ref, allow }, msg)) { acctModels = was; renderProviders(); }
  };
  const draw = () => {
    chips.replaceChildren();
    for (const id of ids) {
      const m = p.models.find((x) => x.id === id);
      const on = acctModels.chosen.includes(id);
      const c = el("button", "mchip" + (on ? " on" : ""));
      c.append(el("span", "", m?.name && m.name !== id ? m.name : id));
      if (m?.name && m.name !== id) c.title = id;
      c.onclick = () => { acctModels.chosen = on ? acctModels.chosen.filter((x) => x !== id) : [...acctModels.chosen, id]; draw(); };
      chips.append(c);
    }
    if (!ids.length) chips.append(el("span", "hint", t("Pick the provider's models first.")));
    save.disabled = !acctModels.chosen.length;
  };
  draw();
  save.onclick = () => { save.classList.add("busy"); post(acctModels.chosen, t("{who} serves only {models}", { who: name, models: acctModels.chosen.join(", ") })); };
  const all = el("button", "text action", t("All models"));
  all.title = t("Every model of the provider, as an account without a list of its own");
  all.onclick = () => own.length ? post([], t("{who} serves every model again", { who: name })) : (acctModels = null, renderProviders());
  const x = el("button", "text", t("Cancel"));
  x.onclick = () => { acctModels = null; renderProviders(); };
  foot.append(el("span", "hint", t(isKey ? "Only the models picked go to this key; the others go to the provider's other keys." : "Only the models picked go to this account; the others go to the provider's other accounts.")), el("span", "grow"), all, x, save);
  box.append(chips, foot);
  return [pill, box];
}

// renderKeyAccounts: a key provider's accounts, one per key, the same list
// a subscription has. addingKey holds the half-typed new one.
let addingKey = null;
function renderKeyAccounts(p) {
  const list = el("div", "accts");
  const several = p.keyList.filter((k) => k.on).length > 1;
  for (const k of p.keyList) {
    const row = el("div", "acc" + (k.on ? " in-use" : " off") + (k.id === justAdded ? " new" : ""));
    row.dataset.accountId = k.id;
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
    // its own models (#474), when there is another key to send the rest to
    const [amPill, amBox] = p.keyList.length > 1 || accountModelsOf(p, k.id).length ? accountModels(p, k.id, true, k.name || k.masked) : [];
    if (amPill) row.append(amPill);
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
    if (amBox) row.append(amBox), row.classList.add("with-am");
    list.append(row);
  }
  if (addingKey?.id === p.id) {
    const box = el("div", "acc adding");
    // the key is what's needed; a name is only for telling keys apart
    const name = input(addingKey.name, t("Name (optional), e.g. Team"));
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
    queueMicrotask(() => key.focus());
  } else {
    const add = el("button", "acc add");
    const ic = el("span", "dot");
    ic.append(svg(PLUS, 10, 1.8));
    add.append(ic, el("span", "n", t("Add another key")));
    add.onclick = () => { addingKey = { id: p.id, name: "", key: "" }; renderProviders(); };
    list.append(add);
  }
  arrangeAccountRows(list, p);
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
  if (protoMenu.anchor.hasAttribute("aria-expanded")) protoMenu.anchor.setAttribute("aria-expanded", "false");
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
    words.append(el("span", "pm-name", o.literalName ? o.name : t(o.name)), el("span", "pm-note", t(o.note)));
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
  if (anchor.hasAttribute("aria-expanded")) anchor.setAttribute("aria-expanded", "true");
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
const apiField = { openai: "chat", responses: "responses", anthropic: "anthropic", decide: "decide" };

// respellURL turns a base URL into the one protocol api is asked at: the
// root for Anthropic, which adds /v1 itself, …/v1 for OpenAI's two.
// WORKSPACE stands for a Bailian workspace's id in a decision API's
// address (provider.WorkspaceID); workspaceOf is the id url has in its
// place in tmpl ("" for none), or null when url isn't at tmpl's host.
const WORKSPACE = "{WorkspaceId}";
function workspaceOf(tmpl, url) {
  if (!tmpl || !url) return null;
  const strip = (u) => u.trim().replace(/\/+$/, "").replace(/\/systemone$/, "");
  const [a, b] = strip(tmpl).split(WORKSPACE);
  const u = strip(url);
  if (b === undefined) return u === a ? "" : null;
  if (u.length < a.length + b.length || !u.startsWith(a) || !u.endsWith(b)) return null;
  const ws = u.slice(a.length, u.length - b.length);
  if (ws === WORKSPACE) return "";
  return /[/.]/.test(ws) ? null : ws;
}

function respellURL(u, api) {
  u = u.trim().replace(/\/+$/, "");
  if (api === "anthropic") return u.replace(/\/v1$/, "");
  if (api === "decide") u = u.replace(/\/systemone$/, "");
  return /\/v\d+[a-z]*$/.test(u) || !/^https?:\/\/[^/]+$/.test(u) ? u : u + "/v1";
}

// renderMove: a built-in subscription a community plugin can run — which
// of the two runs it, and the way to the other. Accounts, models and the
// agents on them stay as they are either way.
// moveWhy is why a move failed, in the reader's language: m is the move
// as the page has it, {why, error}.
function moveWhy(m, p) {
  const a = m.why?.args || {};
  switch (m.why?.code) {
    case "offline": return t("magpie couldn't reach npm to install the plugin. Check the network or proxy, then try again.");
    case "install": return t("npm couldn't install the plugin: {line}", { line: a.line });
    case "lapsed": return t("every {name} account needs signing in again. Sign one in above, then move.", { name: p.name });
    case "unserved": return a.user
      ? t("the plugin doesn't serve {models} for {user}, though the built-in does. Untick them under Models, or keep the built-in.", { models: a.models, user: a.user })
      : t("the plugin doesn't serve {models}. Untick them under Models, or keep the built-in.", { models: a.models });
    case "account": return t("{user} doesn't work through the plugin: {error}", { user: a.user, error: a.error });
  }
  return m.error || "";
}

// renderMove: which runs the subscription, magpie's built-in or its
// community plugin, and the move between them. The editor stays open
// through it: the button says what it is doing, a failure is said under
// it, and on success the field turns over where the reader is.
function renderMove(p) {
  const m = p.move;
  const box = el("div", "stack move");
  const onPlugin = m.state === "plugin";
  const said = el("div", "", onPlugin
    ? t("The community {name} plugin, with the accounts you had here.", { name: p.name })
    : t("magpie's built-in · or the community {name} plugin, with the same accounts", { name: p.name }));
  said.title = m.package; // the npm package: for the curious, not the sentence
  const why = el("div", "move-why");
  why.setAttribute("role", "alert");
  const say = (text) => { why.textContent = text; why.hidden = !text; };
  const failed = m.state === "failed" && (m.why || m.error);
  say(failed ? t("It stays built-in: {error}", { error: moveWhy(m, p) }) : "");
  const idle = () => onPlugin ? t("Use the built-in again") : failed ? t("Try again") : t("Move to the plugin");
  const b = el("button", "text action", idle()); // a button, not a line of text: it changes what runs the subscription
  b.onclick = async () => {
    b.classList.add("busy");
    b.disabled = true;
    say("");
    b.textContent = onPlugin ? t("Moving back…") : t("Installing the plugin and checking each account…");
    try {
      providers = await api("provider/" + (onPlugin ? "moveback" : "move"), { id: p.id });
      draft = null; // the provider changed under it
      renderProviders(); // the editor stays open, turned over, where it was
      const now = providers.providers.find((x) => x.id === p.id);
      const accts = now?.account?.logins?.length || 0, models = (now?.models || []).filter((x) => x.on).length;
      saidMoved(onPlugin ? t("{name} is built-in again, with its accounts", { name: p.name })
        : t("{name} now runs on its plugin — {accounts}, {models}. Use the built-in again from here any time.", {
          name: p.name,
          accounts: t(accts === 1 ? "{n} account" : "{n} accounts", { n: accts }),
          models: t(models === 1 ? "{n} model" : "{n} models", { n: models }),
        }));
      state = await api("state");
      renderAgents();
    } catch (e) {
      say(onPlugin ? e.message : t("It stays built-in: {error}", { error: moveWhy({ why: e.why, error: e.message }, p) }));
      b.classList.remove("busy");
      b.disabled = false;
      b.textContent = onPlugin ? t("Use the built-in again") : t("Try again");
    }
  };
  box.append(said);
  if (!onPlugin) {
    const note = el("div", "dep-note");
    note.append(el("span", "badge deprecated", t("Deprecated")), el("span", "", t(DEPRECATED_WHY)));
    box.append(note);
  }
  box.append(b, why);
  return field(t("Runs on"), box, onPlugin ? t("Going back puts every account, with the plugin's newer sign-ins, back into the built-in.") : t("If an account doesn't work through the plugin, nothing changes."));
}

async function providerAction(action, body, okMsg, base = "provider/") {
  try {
    const was = editing;
    providers = await api(base + action, body);
    dropSigningIn(was); // saved or removed, the editor takes its sign-in with it
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
function saidMoved(okMsg, list = providers?.moved) {
  // one magpie couldn't move (its file unwritable) is still on the model
  // gone, and the change made all the same
  let stuck = false;
  const moved = (list || []).map((m) => {
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

// closeAddSheet rolls the sheet up. The page doesn't hold on to the close
// clicked in it: what goes away goes, and the list ends at the view's foot as
// it did before the sheet opened. Held, the view kept the sheet's height as
// empty room under the list (the blank #433 was about).
function closeAddSheet() {
  const view = $("#view-providers"), sheet = $("#addSheet");
  if (!adding) return;
  if (held?.v === view) held = null;
  setRoom(view, 0);
  addReturnPick = null;
  rollUpSheet(sheet, () => {
    adding = false; editing = null; draft = null; presetQuery = "";
    renderProviders();
    setRoom(view, 0);
    // back from stepping aside now, not when the observer next looks, so it
    // can take the focus
    view.querySelector(".after-list").classList.remove("away");
    $("#addProvider").focus({ preventScroll: true });
  });
}
$("#addSheet").addEventListener("click", (e) => {
  addReturnPick = e.target.closest("[data-pick]")?.dataset.pick || null;
}, true);

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
let quotaHist = []; // each account's windows over time, for the curves (#651)
// asked: the reader opened the page, so a Claude account's usage is read
// at once, by running Claude Code's own /usage, rather than when its last
// reading is due (every 5 to 15 minutes, at random, once Claude Code was used).
async function loadUsage(asked) {
  renderUsageTab();
  renderUsageEvery(); // the refresh's tooltip says what it reads on this tab
  if (asked) loadQuotas(true);
  if (usageTab === "sessions") return loadSessions();
  if (usageTab === "requests") return loadLedger();
  renderUsageLoading();
  if (!asked) loadQuotas();
  usage = await api("usage?period=" + period);
  renderUsage();
}

// An asked load is never swallowed by one already on its way that wasn't:
// it goes after it.
let quotasLoading = null;
function loadQuotas(asked) {
  if (quotasLoading && (!asked || quotasLoading.asked)) return quotasLoading;
  const p = Promise.resolve(quotasLoading).catch(() => {})
    .then(() => Promise.all([api("usage/quotas" + (asked ? "?asked=1" : "")), api("usage/quotas/history?days=35").catch(() => null)]))
    .then(([q, h]) => { quotas = q || []; quotasAt = Date.now(); if (h) quotaHist = h; }, () => { quotas = quotas || []; })
    .finally(() => { if (quotasLoading === p) quotasLoading = null; renderQuotas(); });
  p.asked = !!asked;
  quotasLoading = p;
  return p;
}

// the period picker, in the page's head, for the Overview and Requests
function renderPeriod(loading) {
  const seg = $("#period");
  seg.replaceChildren();
  for (const [id, name] of PERIODS) {
    const b = el("button", "opt" + (id === period ? " on" : ""), t(name));
    b.disabled = !!loading;
    b.onclick = () => { for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(seg, "period"); period = id; ledDay = ""; ledOffset = 0; loadUsage().catch((e) => status(e.message, "err")); };
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
  for (const id of ["usageAgents", "usageModels", "usageKeys"]) $("#" + id).hidden = true;
  for (const h of $$("#view-usage .row-head")) h.hidden = true;
  $("#usageNote").textContent = "";
}

// a count as a short number: 1.33 亿, 68.1 万 in Chinese, 133M in English
// and in Chinese with Settings' K/M/B units (westernUnits) — every count on
// the Usage page and in the panel, tokens or requests, says it this one way
let westernUnits = false;
function fmtN(n) {
  if (locale === "zh" && !westernUnits) {
    if (n >= 1e8) return +(n / 1e8).toFixed(2) + " 亿";
    if (n >= 1e4) return +(n / 1e4).toFixed(1) + " 万";
    return String(n);
  }
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
  document.dispatchEvent(new Event("magpie-costs-changed"));
}
const tokensOf = (t) => t.input + t.output;

function renderQuotas() {
  renderPanelQuota();
  const subscriptions = $("#subscriptionUsage");
  const restoreFlash = keepQuotaFlash(subscriptions);
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
  renderQuotaTrend();
  if (!quotas) {
    subscriptions.hidden = false;
    for (let i = 0; i < 2; i++) {
      const card = el("div", "subscription-card skeleton-card");
      card.append(el("span", "skeleton sk-title"), el("span", "skeleton sk-line"), el("span", "skeleton sk-line short"));
      subscriptions.append(card);
    }
    return;
  }
  if (usageArranging) { usageRenderPending = true; return; }
  subscriptions.hidden = !quotas.length;
  // an agent with several accounts is one card, a section per account
  const groups = [];
  for (const sub of byUsageOrder(quotas)) {
    const g = sub.user && groups.find((x) => x[0].user && x[0].provider === sub.provider);
    if (g) g.push(sub); else groups.push([sub]);
  }
  for (const subs of groups) {
    const first = subs[0];
    // several accounts: each in full or in brief, its bars without the
    // rest (whqtian, ARNO)
    const several = subs.length > 1;
    const card = el("div", "subscription-card" + (first.user ? " several" : ""));
    card.dataset.key = first.provider;
    card.dataset.provider = first.provider;
    if (!first.user) card.dataset.card = first.provider;
    const head = el("div", "subscription-head");
    head.append(groups.length > 1 ? usageHandle(first, card) : icon(first.icon), el("b", "", first.name));
    if (!first.user && (first.plan || first.until)) head.append(planSpan(first));
    card.append(head);
    for (const sub of subs) {
      // "Every model" by the account, or the card's name: where the click
      // was, whichever way the meters under it grow or shrink
      const [meters, every] = familyQuota(sub);
      const brief = several && !usageAcctOpen(sub, subs);
      if (sub.user) {
        const who = el("div", "subscription-account" + (brief ? " brief" : ""));
        who.dataset.card = trayCardID(sub);
        const u = el("span", "user", sub.user);
        u.title = sub.user;
        who.append(u);
        if (sub.plan || sub.until) who.append(planSpan(sub));
        if (every) who.append(every);
        if (several) who.append(usageAcctFold(sub, !brief));
        card.append(who);
      } else if (every) head.append(every);
      // in brief: each window's name, figure and bar, when it resets in
      // its tooltip; the curve and the rest only in full
      if (brief) meters.classList.add("brief");
      card.append(meters);
      // what was left over time, against an even burn (#651)
      const curve = !brief && !sub.error && quotaCurve(sub);
      if (curve) card.append(curve);
      // WorkBuddy's credits, day by day, as magpie counted them (#568)
      if (!brief && sub.daily && !sub.error) card.append(creditDays(sub));
      // what is left besides the windows, under them
      if (sub.balance && sub.windows?.length && !sub.error) card.append(balanceRow(sub, "What is left on the account besides its windows", false));
      // windows standing in for ones that couldn't be read just now say
      // when they were read (a balance alone says it in its row)
      const read = !brief && sub.windows?.length && !sub.error && readWhen(sub);
      if (read) card.append(read);
      if (!brief && sub.resets?.count) {
        const r = el("div", "quota-resets");
        r.append(resetsWords(sub.resets));
        const use = el("button", "text", t("Use a reset"));
        use.title = resetUseTitle(sub);
        use.onclick = () => askReset(sub);
        // only Codex's are spent from here: a GLM team's are spent on
        // bigmodel.cn, a plugin's wherever its vendor spends them
        if (!sub.resets.byWindow && sub.provider === "codex") {
          const auto = autoResetButton(sub, "text auto-reset");
          if (auto) r.append(auto);
          r.append(use);
        }
        card.append(r);
      }
    }
    // in the card's head, beside its name: no line of its own
    if (several) head.append(usageMore(subs, "text quota-more"));
    subscriptions.append(card);
  }
  restoreFlash();
  requestAnimationFrame(focusQuotaCard);
}

// A subscription with several accounts shows each of them, one in full —
// the one that answered last, else the first — and the others in brief,
// their bars alone, a click opening one (whqtian on Discord: 只显示一个账号
// 即可，其他的可以点击展开; ARNO, 17 subscriptions and 29 accounts: 以前每个
// 账号所占的空间很小，基本能一页看到所有账号… 关了magpie后又得一个个展开).
// The tray panel, with less room, leaves the ones in brief out. Each
// account's full or brief is remembered by provider and account
// (magpie.usageAccounts), for the window and the panel alike; a provider
// opened before that (magpie.usageOpen) starts with every account in full.
let usageOpen = new Set(), usageAccts = {};
try { usageOpen = new Set(JSON.parse(localStorage.getItem("magpie.usageOpen") || "[]")); } catch {}
try { usageAccts = JSON.parse(localStorage.getItem("magpie.usageAccounts") || "{}") || {}; } catch {}
addEventListener("storage", (e) => {
  if (e.key !== "magpie.usageOpen" && e.key !== "magpie.usageAccounts") return;
  try { usageOpen = new Set(JSON.parse(localStorage.getItem("magpie.usageOpen") || "[]")); } catch {}
  try { usageAccts = JSON.parse(localStorage.getItem("magpie.usageAccounts") || "{}") || {}; } catch {}
  renderQuotas();
});
function usageShown(subs) {
  let pick = subs[0];
  for (const q of subs) if (q.lastServedAt && (!pick.lastServedAt || q.lastServedAt > pick.lastServedAt)) pick = q;
  return pick;
}
const usageAcctKey = (q) => q.provider + "\n" + (q.user || "").toLowerCase();
// usageAcctOpen: whether an account of a card with several is in full
function usageAcctOpen(q, subs) {
  const v = usageAccts[usageAcctKey(q)];
  if (typeof v === "boolean") return v;
  return usageOpen.has(q.provider) || q === usageShown(subs);
}
function setUsageAccts(list, open) {
  for (const q of list) usageAccts[usageAcctKey(q)] = typeof open === "function" ? open(q) : open;
  try { localStorage.setItem("magpie.usageAccounts", JSON.stringify(usageAccts)); } catch {}
  renderQuotas();
}
// the card's button: the accounts in brief (or, in the panel, left out) in
// full, or every one but the one in sight back in brief
function usageMore(subs, cls) {
  const closed = subs.filter((q) => !usageAcctOpen(q, subs));
  const panel = mode === "panel";
  // the panel draws the one in sight when none is in full
  const n = closed.length - (panel && closed.length === subs.length ? 1 : 0);
  const b = el("button", cls, n
    ? t(panel ? (n === 1 ? "Show 1 more account" : "Show {n} more accounts") : (n === 1 ? "Show 1 more account in full" : "Show {n} more accounts in full"), { n })
    : t(panel ? "Show fewer accounts" : "Show the other accounts in brief"));
  b.type = "button";
  b.setAttribute("aria-expanded", String(!n));
  b.onclick = () => {
    const pick = usageShown(subs);
    if (n) setUsageAccts(closed, true); else setUsageAccts(subs, (q) => q === pick);
    if (!panel) backToReader($("#view-usage"));
  };
  return b;
}
// an account's own fold, at its row's end: in full or in brief
function usageAcctFold(q, open) {
  const b = el("button", "quota-acct-fold");
  b.type = "button";
  b.append(svg(CHEV, 11, 1.6));
  b.setAttribute("aria-expanded", String(open));
  b.title = t(open ? "Show this account in brief" : "Show this account in full");
  b.onclick = () => {
    setUsageAccts([q], !open);
    backToReader($("#view-usage"));
  };
  return b;
}

// The Usage page's cards in the order they were dragged to (settings
// usageOrder, by provider id), as the Agents page's rows are; one it doesn't
// name keeps magpie's own order after them. The tray panel's Usage tab
// follows it too.
function byUsageOrder(list) {
  const order = state.settings?.usageOrder || [];
  const rank = (q) => { const i = order.indexOf(q.provider); return i < 0 ? order.length : i; };
  return list.map((q, i) => [q, i]).sort(([a, i], [b, j]) => rank(a) - rank(b) || i - j).map(([q]) => q);
}

// usageHandle is a card's logo, which is also its handle: drag it to move
// the card among the others, Alt+arrows move it from the keyboard. The grip
// that says so shows by the logo on hover (app.css).
let usageArranging = false, usageRenderPending = false;
function usageHandle(sub, card) {
  const b = el("button", "ag-handle us-handle");
  b.type = "button";
  b.setAttribute("aria-label", t("Arrange {agent}", { agent: sub.name }));
  b.title = t("Drag to reorder · Alt+arrow keys to move");
  b.append(icon(sub.icon));
  b.onkeydown = (e) => {
    const step = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 }[e.key];
    if (!e.altKey || !step) return;
    e.preventDefault();
    const keys = usageKeys();
    moveUsage(sub.provider, keys.indexOf(sub.provider) + step);
    // the cards were drawn anew: keep the keyboard on this one
    $(`#subscriptionUsage > [data-key="${CSS.escape(sub.provider)}"] .us-handle`)?.focus({ preventScroll: true });
  };
  b.onpointerdown = (e) => {
    if (usageArranging) return;
    const list = $("#subscriptionUsage");
    usageArranging = dragCards(e, b, card, list, [...list.children], (to) => moveUsage(sub.provider, to), () => {
      usageArranging = false;
      if (usageRenderPending) { usageRenderPending = false; renderQuotas(); }
    });
  };
  return b;
}

const usageKeys = () => [...new Set([...$("#subscriptionUsage").children].map((c) => c.dataset.key))];

// moveUsage puts the card at index `to` among those on the page; what the
// order named that isn't on it now (an account signed out) keeps its place
// after them. Drawn at once, put back if the save fails.
async function moveUsage(key, to) {
  const keys = usageKeys();
  const from = keys.indexOf(key);
  if (from < 0 || to < 0 || to >= keys.length || to === from) return;
  keys.splice(to, 0, ...keys.splice(from, 1));
  const prev = state.settings;
  const order = [...keys, ...(prev.usageOrder || []).filter((k) => !keys.includes(k))];
  state.settings = { ...prev, usageOrder: order };
  renderQuotas();
  try {
    const s = await api("usage/arrange", { order });
    state.settings = { ...state.settings, usageOrder: s.usageOrder || [] };
  } catch (e) {
    state.settings = prev;
    renderQuotas();
    status(e.message, "err");
  }
}

// creditDays: the credits an account used each day of the period, as
// magpie counted them from its readings of the vendor's meter, which tells
// only what the cycle has used so far — a bar a day, and the period's sum;
// a day before magpie first read it is not known rather than nothing.
function creditDays(sub) {
  const box = el("div", "credit-days");
  const { since, days } = sub.daily;
  const by = new Map(days.map((d) => [d.day, d.used]));
  const key = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  const today = new Date();
  today.setHours(12, 0, 0, 0);
  // the period's days, the first from when counting began for All
  let n = { today: 1, "7d": 7, "30d": 30 }[period];
  if (!n) n = Math.min(120, Math.max(1, Math.round((today - new Date(since + "T12:00:00")) / 864e5) + 1));
  const list = [];
  for (let i = n - 1; i >= 0; i--) {
    const d = new Date(today);
    d.setDate(d.getDate() - i);
    list.push({ day: key(d), date: d, used: by.get(key(d)) || 0, known: key(d) >= since });
  }
  const num = (v) => (Math.round(v * 100) / 100).toLocaleString(locale === "zh" ? "zh-CN" : undefined);
  const dayName = (d) => d.toLocaleDateString(locale === "zh" ? "zh-CN" : undefined, { month: "short", day: "numeric" });
  const sum = list.reduce((a, d) => a + d.used, 0);
  const head = el("div", "cd-head");
  const name = el("span", "", t("Credits used per day"));
  name.title = t("Counted from magpie's readings of the vendor's meter, since {date}: what is used while magpie isn't reading it is counted on the day it next does", { date: dayName(new Date(since + "T12:00:00")) });
  head.append(name, el("b", "", n === 1 ? t("{n} today", { n: num(sum) }) : t("{n} in {days} days", { n: num(sum), days: n })));
  box.append(head);
  if (n > 1) {
    const bars = el("div", "cd-bars");
    const peak = Math.max(...list.map((d) => d.used), 0);
    for (const d of list) {
      const b = el("span", "cd-day" + (d.known ? "" : " unknown"));
      const fill = el("i");
      fill.style.height = d.known && peak ? Math.max(d.used ? 6 : 0, 100 * d.used / peak).toFixed(1) + "%" : "0%";
      b.append(fill);
      b.title = d.known ? t("{date}: {n} credits", { date: dayName(d.date), n: num(d.used) }) : t("{date}: not counted — magpie began counting on {since}", { date: dayName(d.date), since: dayName(new Date(since + "T12:00:00")) });
      bars.append(b);
    }
    box.append(bars);
  }
  if (since > list[0].day) box.append(el("div", "cd-note", t("Counted since {date}", { date: dayName(new Date(since + "T12:00:00")) })));
  return box;
}

// What was left of an account's windows over time (#651), from magpie's
// readings of them (and those of the computers sharing usage with it): a
// line a window, the percent left, broken where the window started again;
// a dashed line from the window's start to its reset is an even burn, and
// a thin upright one is now. "2 days" is the last two days; "Cycle" the
// current cycle of the account's longest window, its start to its reset;
// "Off" draws none, on the Usage page or on the tray's cards (ARNO on
// Discord: 额度面板变得非常混乱和难以观看了). One pick in the allowances'
// head turns every card, remembered, and the tray panel follows it.
let quotaRange = "cycle";
try { const v = localStorage.getItem("magpie.quotaRange"); if (v === "2d" || v === "off") quotaRange = v; } catch {}
const QUOTA_RANGES = [["off", "Off", "No curves on the cards"], ["2d", "2 days", "The last two days"], ["cycle", "Cycle", "The current cycle, from the window's start to its reset"]];
addEventListener("storage", (e) => {
  if (e.key !== "magpie.quotaRange") return;
  const v = QUOTA_RANGES.some(([id]) => id === e.newValue) ? e.newValue : "cycle";
  if (v !== quotaRange) { quotaRange = v; renderQuotas(); }
});
// renderQuotaTrend: the allowances' head's pick of the curves' range, there
// once some account has readings to draw
function renderQuotaTrend() {
  const b = $("#quotaTrend");
  if (!b) return;
  b.hidden = !quotas?.some((q) => !q.error && quotaLines(q).length);
  if (b.hidden) return;
  const cur = QUOTA_RANGES.find(([id]) => id === quotaRange);
  b.classList.toggle("set", quotaRange !== "off");
  b.replaceChildren(el("span", "", t("Trends: {range}", { range: t(cur[1]) })), svg(CHEV, 11, 1.6));
  b.title = t("What was left over time, under each account's meters");
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(b, QUOTA_RANGES.map(([v, name, note]) => ({ v, name, note })), quotaRange, setQuotaRange, "Left over time", "sess-menu");
  };
}

function quotaHistOf(sub) {
  const user = (sub.user || "").toLowerCase();
  return quotaHist?.find?.((h) => h.provider === sub.provider && h.user === user);
}
// the lines of sub's windows, as its card lists them, with points
function quotaLines(sub) {
  const h = quotaHistOf(sub);
  if (!h) return [];
  const out = [];
  for (const w of sub.windows || []) {
    if (w.unlimited || w.family) continue;
    // a pool's window is kept under the pool's name and its own
    const name = w.pool ? w.pool + " · " + w.name : w.name;
    const l = h.lines.find((x) => x.name === name);
    if (!l?.points?.length || out.some((x) => x.name === l.name)) continue;
    out.push({ name, label: w.pool ? w.pool + " · " + t(w.name) : t(w.name), window: w, points: l.points.map((p) => ({
      at: Date.parse(p.at), left: p.left,
      start: p.start ? Date.parse(p.start) : null, reset: p.resetsAt ? Date.parse(p.resetsAt) : null,
    })) });
  }
  return out;
}
// quotaCycleBreak: whether b, read after a, is of its window started again,
// as magpie's own record of them says (quota_history.go's newCycle)
function quotaCycleBreak(a, b) {
  if (a.reset == null && b.reset == null) return b.left > a.left + 5;
  if (a.reset == null || b.reset == null) return true;
  if (a.reset <= b.at) return true;
  const moved = b.reset - a.reset, slack = 5 * 60e3;
  return moved > b.at - a.at + slack || moved < -slack;
}
// the current cycle of a line: its start and its reset, the window's as
// the card has it, else its last reading's
function quotaCycle(l, now) {
  const last = l.points[l.points.length - 1];
  let reset = l.window.resetsAt ? Date.parse(l.window.resetsAt) : last.reset;
  let start = last.start != null && last.reset != null && reset != null ? last.start + (reset - last.reset) : last.start;
  if (start == null) { // its first reading of this cycle
    let i = l.points.length - 1;
    while (i > 0 && !quotaCycleBreak(l.points[i - 1], l.points[i])) i--;
    start = l.points[i].at;
  }
  if (reset != null && reset <= now) reset = null;
  return { start: Math.min(start, now), reset };
}
function quotaSpan(lines, range, now) {
  if (range === "2d") return [now - 48 * 3600e3, now];
  let x0 = Infinity, x1 = now;
  for (const l of lines) {
    const c = quotaCycle(l, now);
    x0 = Math.min(x0, c.start);
    if (c.reset) x1 = Math.max(x1, c.reset);
  }
  if (!(x0 < now)) x0 = now - 3600e3;
  return [x0, x1];
}
// quotaPaths: a line's points in [x0, x1] as path data on a w×h box, a
// new subpath at each cycle's start; the point before x0 and the one after
// x1 go along, so the line reaches the box's edges
function quotaPaths(points, x0, x1, w, h) {
  const X = (t) => ((t - x0) / (x1 - x0)) * w, Y = (v) => h - (Math.max(0, Math.min(100, v)) / 100) * h;
  let d = "", prev = null;
  for (let i = 0; i < points.length; i++) {
    const p = points[i];
    const next = points[i + 1];
    if (p.at < x0 && (!next || next.at < x0 || quotaCycleBreak(p, next))) continue;
    if (prev && p.at > x1 && prev.at > x1) break;
    const brk = !prev || quotaCycleBreak(prev, p);
    d += (brk ? "M" : "L") + X(p.at).toFixed(1) + " " + Y(p.left).toFixed(1);
    if (brk && (!points[i + 1] || quotaCycleBreak(p, points[i + 1]))) d += "h0.01"; // a lone point is a dot
    prev = p;
  }
  return d;
}
function quotaTimeText(at, now) {
  const d = new Date(at), lang = locale === "zh" ? "zh-CN" : undefined;
  if (Math.abs(at - now) < 60e3) return t("Now");
  const sameDay = d.toDateString() === new Date(now).toDateString();
  const time = d.toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit", hour12: false });
  return sameDay ? time : d.toLocaleDateString(lang, { month: "short", day: "numeric" }) + " " + time;
}
// drawQuotaPlot puts lines' curves for [x0, x1] in svg, a w×h box:
// the grid, the even burns, the lines, and now.
function drawQuotaPlot(g, lines, x0, x1, w, h, now, thin) {
  g.replaceChildren();
  if (!thin) for (const v of [0, 50, 100]) g.append(sv("line", { x1: 0, x2: w, y1: h - (v / 100) * h, y2: h - (v / 100) * h, class: "qc-grid" }));
  const X = (t) => ((t - x0) / (x1 - x0)) * w;
  lines.forEach((l, i) => {
    const color = `var(--c${(i % 7) + 1})`;
    const c = quotaCycle(l, now);
    // a window far shorter than the span (a 5-hour one over a week) is
    // drawn faint, without its even burn, an all but upright dash
    const short = !thin && c.reset && c.reset - c.start < (x1 - x0) / 12;
    if (c.reset && c.start < c.reset && !short) {
      const e = sv("line", { x1: X(c.start), y1: 0, x2: X(c.reset), y2: h, class: "qc-even" }, { stroke: color });
      g.append(e);
    }
    const d = quotaPaths(l.points, x0, x1, w, h);
    if (d) g.append(sv("path", { d, class: "qc-line" + (short ? " qc-short" : ""), "data-name": l.name }, { stroke: color }));
  });
  if (now >= x0 && now <= x1) g.append(sv("line", { x1: X(now), x2: X(now), y1: 0, y2: h, class: "qc-now" }));
}
// quotaCurve: an account's windows over time, under its meters, with the
// range turned for every card at once
function quotaCurve(sub) {
  if (quotaRange === "off") return null;
  const lines = quotaLines(sub);
  if (!lines.length) return null;
  const box = el("div", "quota-curve");
  const head = el("div", "qc-head");
  const range = el("span", "qc-range");
  head.append(el("span", "", t("Left over time")), range);
  const W = 300, H = 60;
  const g = sv("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", class: "qc-plot", role: "img" });
  const axis = el("div", "qc-axis");
  const legend = el("div", "qc-legend");
  lines.forEach((l, i) => {
    const k = el("span", "qc-key");
    const sw = el("i");
    sw.style.background = `var(--c${(i % 7) + 1})`;
    const last = l.points[l.points.length - 1];
    k.append(sw, el("span", "", l.label), el("b", "", Math.round(last.left) + "%"));
    k.title = t("{name}: {n} left, read {when}", { name: l.label, n: Math.round(last.left) + "%", when: quotaTimeText(last.at, Date.now()) });
    legend.append(k);
  });
  box.draw = () => {
    const now = Date.now();
    const [x0, x1] = quotaSpan(lines, quotaRange, now);
    drawQuotaPlot(g, lines, x0, x1, W, H, now);
    const r = t(quotaRange === "2d" ? "2 days" : "Cycle");
    range.textContent = r;
    g.setAttribute("aria-label", t("Left over time") + " · " + r);
    axis.replaceChildren(el("span", "", quotaTimeText(x0, now)), el("span", "", quotaTimeText(x1, now)));
  };
  box.append(head, g, axis, legend);
  box.draw();
  return box;
}
function setQuotaRange(id) {
  if (id === quotaRange) return;
  const was = quotaRange;
  quotaRange = id;
  try { localStorage.setItem("magpie.quotaRange", id); } catch {}
  // turned on or off, the cards are made again; a range redraws them
  if (was === "off" || id === "off") return renderQuotas();
  renderQuotaTrend();
  for (const b of document.querySelectorAll(".quota-curve")) b.draw?.();
}
// quotaSpark: the tray card's thin line of a window's current cycle, the
// even burn faint under it; none while the curves are off
function quotaSpark(q, w) {
  if (quotaRange === "off") return null;
  const lines = quotaLines({ ...q, windows: [w.window ? { ...w, name: w.window } : w] });
  if (!lines.length) return null;
  const now = Date.now(), c = quotaCycle(lines[0], now);
  const x1 = c.reset || now;
  if (!(c.start < x1)) return null;
  const W = 200, H = 16;
  const g = sv("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", class: "pq-spark", role: "img" });
  g.setAttribute("aria-label", t("{name}: this cycle", { name: t(w.name) }));
  drawQuotaPlot(g, lines, c.start, x1, W, H, now, true);
  const s = el("span", "pq-spark-box");
  s.title = t("{name}: what was left this cycle, against an even burn", { name: t(w.name) });
  s.append(g);
  return s;
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

// The tray panel is four tabs over the one page: the agents, the allowances
// of every subscription and key (the "usage" tab, as it was named before),
// what the requests add up to (the "stats" tab, the window's Requests made
// small) and the gateway's latest requests (routing.js draws those). The saved
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
  disarmProfile();
  closeProfileDetail(); // closed as by hand or by Apply, they open on the chips
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
  // Escape closes them in profileEscape, and the name field's is its own
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
  // the tab remembered is drawn as the page loads, before what the tab is drawn with,
  // further down, is set: a microtask later, when all of it is
  queueMicrotask(panelUseShown);
  fit();
}
// Scroll to a requested card, or resume its highlight after a redraw.
function flashCard(card, target = card, elapsed = 0) {
  if (target) {
    // The native menu-bar click has no DOM event; permit this requested scroll.
    readerScrolls(1000);
    target.scrollIntoView({ block: "nearest", behavior: "instant" });
  }
  card.classList.remove("flash");
  void card.offsetWidth; // restart even if this card is still highlighted
  card.classList.add("flash");
  const animation = card.getAnimations().find((a) => a.animationName === "flash");
  if (animation) animation.currentTime = elapsed;
  card.onanimationend = (e) => {
    if (e.target !== card || e.animationName !== "flash") return;
    card.classList.remove("flash");
    card.onanimationend = null;
  };
}

// A focus refresh rebuilds quota cards. Carry the active animation's time
// to its replacement, preserving scroll position and the remaining duration.
function keepQuotaFlash(box) {
  const view = box.closest(".view"), scroll = view?.scrollTop;
  const flashes = [...box.querySelectorAll(".subscription-card.flash, .pq-card.flash")].flatMap((card) => {
    const animation = card.getAnimations().find((a) => a.animationName === "flash");
    if (!animation) return [];
    const selector = card.matches(".pq-card") ? `.pq-card[data-card="${CSS.escape(card.dataset.card)}"]`
      : `.subscription-card[data-provider="${CSS.escape(card.dataset.provider)}"]`;
    return [{ selector, elapsed: animation.currentTime || 0 }];
  });
  return () => {
    for (const { selector, elapsed } of flashes) {
      const card = box.querySelector(selector);
      if (card) flashCard(card, null, elapsed);
    }
    if (flashes.length && view) {
      view.scrollTop = scroll;
      readerLeaves(view);
    }
  };
}

// Keep the request until quota data has produced the target's DOM node.
function focusQuotaCard() {
  if (!quotaFocus) return;
  if (performance.now() > quotaFocusUntil) { quotaFocus = ""; return; }
  const box = mode === "panel" ? $("#panelQuota") : $("#subscriptionUsage");
  if (mode === "panel") setPanelTab("usage");
  else if (view !== "usage" || usageTab !== "usage") return;
  const id = CSS.escape(quotaFocus);
  const target = box.querySelector(`[data-card="${id}"]`) || box.querySelector(`[data-provider="${id}"]`);
  // The panel omits accounts in brief; the main window draws them short.
  // The one asked for is opened in full.
  const quota = quotas?.find((q) => trayCardID(q) === quotaFocus);
  if (quota?.user && !usageAcctOpen(quota, quotas.filter((q) => q.user && q.provider === quota.provider))) {
    setUsageAccts([quota], true);
    return; // the redraw queues focus once the account is visible
  }
  if (!target) {
    if (quotas && !quotasLoading) quotaFocus = "";
    return;
  }
  quotaFocus = "";
  flashCard(target.closest(".subscription-card, .pq-card"), target);
}

// Go calls this when a menu-bar quota cell opens the panel.
function panelQuotaFocus(id) {
  if (mode !== "panel") return;
  quotaFocus = id;
  quotaFocusUntil = performance.now() + 5000;
  setPanelTab("usage");
  requestAnimationFrame(focusQuotaCard);
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

// The panel's Usage tab: what the requests of a period add up to — four
// totals, a small chart of them by the hour or day, and who they were of —
// as the window's Requests draws it, from the same answer (a page of one row
// is all the panel asks for). A provider picked lists only its requests, and
// the chart then tells its models apart. A click on someone in the ranking
// picks them; "Open Usage" takes the window to their requests.
let panelUse = null; // the answer for the period, and provider, shown
let panelUsePeriod = "today", panelUseProvider = "", panelUseMetric = "tokens", panelUseComputer = "", panelUseDay = "";
try {
  const p = localStorage.getItem("magpie.panelUsePeriod"), m = localStorage.getItem("magpie.panelUseMetric");
  if (["today", "7d", "30d"].includes(p)) panelUsePeriod = p;
  if (["tokens", "cost", "calls"].includes(m)) panelUseMetric = m;
} catch {}
let panelUseAt = 0;
const PANEL_USE_PERIODS = [["today", "Today"], ["7d", "7 days"], ["30d", "30 days"]];

// One read at a time: a 10 s timer must not start a second behind a slow one,
// and a read asked for while one is (a period or a provider picked, the tab
// shown) is kept and served when it is done, so a pick is never dropped.
let panelUseFlight = null, panelUseQueued = false;
function loadPanelUse() {
  if (mode !== "panel") return Promise.resolve();
  if (panelUseFlight) { panelUseQueued = true; return panelUseFlight; }
  const q = new URLSearchParams({ period: panelUsePeriod, limit: "1" });
  if (panelUseProvider) q.set("provider", panelUseProvider);
  if (panelUseComputer) q.set("computer", panelUseComputer);
  if (panelUseDay) q.set("day", panelUseDay);
  const want = q.toString();
  panelUseAt = performance.now();
  // what is shown stays, dimmed, till the answer comes: the panel doesn't
  // shrink to a skeleton and lose where it was scrolled to
  $("#panelUsage").classList.add("pu-loading");
  const read = async () => {
    try {
      const l = await api("usage/requests?" + want);
      const now = new URLSearchParams({ period: panelUsePeriod, limit: "1" });
      if (panelUseProvider) now.set("provider", panelUseProvider);
      if (panelUseComputer) now.set("computer", panelUseComputer);
      if (panelUseDay) now.set("day", panelUseDay);
      if (now.toString() !== want) return; // another period or provider was picked meanwhile
      panelUse = l;
      $("#panelUsage").classList.remove("pu-loading");
      if (panelTab === "stats") renderPanelUse();
    } catch (e) {
      // the read failed: nothing is on its way for it, so the dimming comes off
      $("#panelUsage").classList.remove("pu-loading");
      throw e;
    }
  };
  const flight = read().finally(() => {
    if (panelUseFlight !== flight) return;
    panelUseFlight = null;
    if (!panelUseQueued) return;
    panelUseQueued = false;
    return loadPanelUse(); // include the latest pick or manual refresh in this promise
  });
  panelUseFlight = flight;
  return flight;
}
// the tab is shown: what it has is drawn, and read again if it is old
function panelUseShown() {
  if (mode !== "panel" || panelTab !== "stats") return;
  renderPanelUse();
  if (!panelUse || performance.now() - panelUseAt > 5e3) loadPanelUse().catch(() => {});
}
if (mode === "panel") {
  // requests come while it is looked at
  setInterval(() => { if (panelTab === "stats" && !document.hidden) loadPanelUse().catch(() => {}); }, 10e3);
  // and the panel opened again reads them at once, not at the next tick
  const again = () => { if (panelTab === "stats" && !document.hidden && performance.now() - panelUseAt > 2e3) loadPanelUse().catch(() => {}); };
  window.addEventListener("focus", again);
  document.addEventListener("visibilitychange", again);
}

function renderPanelUse() {
  const box = $("#panelUsage");
  if (mode !== "panel" || !box) return;
  const v = $("#view-agents"), keep = v.scrollTop;
  const focusDay = box.contains(document.activeElement) ? document.activeElement.dataset.day : "";
  const l = panelUse;
  box.hidden = false;
  const bar = el("div", "pu-bar");
  const per = el("div", "segs");
  for (const [id, name] of PANEL_USE_PERIODS) {
    const b = el("button", "opt" + (id === panelUsePeriod ? " on" : ""), t(name));
    b.onclick = () => {
      if (id === panelUsePeriod) return;
      panelUsePeriod = id;
      panelUseDay = "";
      try { localStorage.setItem("magpie.panelUsePeriod", id); } catch {}
      renderPanelUse();
      loadPanelUse().catch(() => {});
    };
    per.append(b);
  }
  const pick = el("button", "sess-pick");
  pick.type = "button";
  const opts = (l?.providers || []).map((p) => ({ v: p.id, name: t(p.name), note: "" }));
  if (panelUseProvider && l && !opts.some((o) => o.v === panelUseProvider)) panelUseProvider = "";
  sessPick(pick, "All providers", panelUseProvider, opts, "Provider", (id) => { panelUseProvider = id; renderPanelUse(); loadPanelUse().catch(() => {}); });
  // the computers whose usage sync shares (#542): none, and it isn't there
  const comp = el("button", "sess-pick");
  comp.type = "button";
  const copts = computerOpts(l?.computers);
  if (panelUseComputer && l && !copts.some((o) => o.v === panelUseComputer)) panelUseComputer = "";
  sessPick(comp, "All computers", panelUseComputer, copts, "Computer", (id) => { panelUseComputer = id; renderPanelUse(); loadPanelUse().catch(() => {}); });
  const open = el("button", "text", t("Open Usage"));
  open.type = "button";
  open.onclick = (e) => {
    api("window/main?" + new URLSearchParams({ view: "usage", tab: "requests", ...(panelUseProvider ? { provider: panelUseProvider } : {}), ...(panelUseComputer ? { computer: panelUseComputer } : {}) }), {});
    e.currentTarget.blur();
  };
  // as the window's Usage has it: what is shown read again now
  const again = el("button", "icon pu-again");
  again.type = "button";
  again.title = t("Refresh now");
  again.setAttribute("aria-label", t("Refresh now"));
  again.append(svg("M13.5 8a5.5 5.5 0 1 1-1.6-3.9M13.5 2.5v3h-3", 13));
  again.onclick = (e) => {
    const b = e.currentTarget;
    b.blur();
    b.classList.add("spin");
    loadPanelUse().catch(() => {}).finally(() => setTimeout(() => $("#panelUsage .pu-again")?.classList.remove("spin"), 300));
  };
  bar.append(per, ...(copts.length ? [comp] : []), pick, el("span", "grow"), again, open);
  const out = [bar, el("p", "usage-note", t("Gateway and session-log calls; local rejections excluded from totals."))];
  if (!l) {
    out.push(el("span", "skeleton pu-sk"), el("span", "skeleton pu-sk"));
  } else if (!l.total && !l.day) {
    const none = el("div", "pu-none");
    none.append(el("b", "", t(panelUseProvider || panelUsePeriod !== "today" ? "No requests here" : "No requests today")), t("Every request an agent sends to magpie, and every call the agents' own session files record, is counted here."));
    out.push(none);
  } else {
    const prompt = l.input + l.cache_write + l.cache_read;
    const tot = el("div", "pu-tot");
    const blk = (label, value, sub, cls, subCls) => {
      const b = el("div", "blk");
      b.append(el("span", "k", t(label)), el("span", "v" + (cls ? " " + cls : ""), value));
      if (sub) b.append(el("span", "sub" + (subCls ? " " + subCls : ""), sub));
      tot.append(b);
    };
    // the total counts the cache's tokens too, so they're named beside in and out
    const cached = l.cache_read + l.cache_write;
    blk("Tokens", fmtN(allTokens(l)), t("{a} in · {b} out", { a: fmtN(l.input), b: fmtN(l.output) }) + (cached ? " · " + t("{n} cached", { n: fmtN(cached) }) : ""));
    blk("Requests", ledNum(l.calls), l.errors ? t("{n} failed", { n: ledNum(l.errors) }) : t("none failed"), "", l.errors ? "bad" : "");
    const c = fmtCost(l);
    blk("Cost", c ? "≈" + c : "—", l.unpriced ? t("{n} unpriced", { n: l.unpriced }) : t("effective prices"), c ? "cost" : "");
    blk("Cache hit rate", prompt ? Math.round((100 * l.cache_read) / prompt) + "%" : "—", t("of the prompt"));
    const card = el("div", "pu-card");
    const head = el("div", "led-bar");
    const met = el("div", "segs");
    for (const [id, name] of LED_METRICS) {
      const b = el("button", "opt" + (id === panelUseMetric ? " on" : ""), t(name));
      b.onclick = () => {
        if (id === panelUseMetric) return;
        panelUseMetric = id;
        try { localStorage.setItem("magpie.panelUseMetric", id); } catch {}
        renderPanelUse();
      };
      met.append(b);
    }
    head.append(met);
    // one provider's chart tells its models apart; all of them, the providers
    const split = panelUseProvider ? "model" : "provider";
    const chart = el("div", "led-chart"), rank = el("div", "led-rank");
    card.append(head, chart, rank);
    out.push(tot, card);
    box.replaceChildren(...out);
    slide(per, "puPeriod");
    slide(met, "puMetric");
    drawLedColumns(chart, l, split, panelUseMetric, true, (day) => {
      panelUseDay = panelUseDay === day ? "" : day;
      loadPanelUse().catch(() => {});
    }, focusDay);
    rank.chart = chart;
    drawLedRank(rank, l, split, panelUseMetric, "", (x) => {
      if (split !== "provider") return; // a model is not a way in: the picker has the providers
      panelUseProvider = x.id;
      renderPanelUse();
      loadPanelUse().catch(() => {});
    }, true);
    if (v.scrollTop !== keep) v.scrollTop = keep;
    fit();
    return;
  }
  box.replaceChildren(...out);
  slide(per, "puPeriod");
  if (v.scrollTop !== keep) v.scrollTop = keep;
  fit();
}

// The Usage tab: accounts under their vendor, each window a ring with its
// share in it, when the plan ends and the windows start again under the
// account; balances last, as figures.
function renderPanelQuota() {
  const box = $("#panelQuota");
  if (mode !== "panel" || !box) return;
  const subs = byUsageOrder((quotas || []).filter((q) => q.balance || q.error || q.windows?.length));
  const none = !!quotas && !subs.length;
  const usageTab = $('#ptabs [data-ptab="usage"]');
  if (usageTab.hidden !== none) {
    usageTab.hidden = none;
    // the tabs part the row anew: the card goes where its tab is now
    if (!(none && panelTab === "usage")) slide($("#ptabs"), "ptabs");
  }
  if (none && panelTab === "usage") setPanelTab("agents");
  box.hidden = none;
  const restoreFlash = keepQuotaFlash(box);
  box.replaceChildren();
  if (none) { quotaFocus = ""; fit(); return; }
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
    // the accounts in full; none of them, the one in sight
    const full = qs.length > 1 ? qs.filter((q) => usageAcctOpen(q, qs)) : qs;
    for (const q of full.length ? full : [usageShown(qs)]) g.append(panelQuotaCard(q));
    if (qs.length > 1) g.append(usageMore(qs, "pq-more"));
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
      card.dataset.card = trayCardID(q);
      card.title = [q.name, q.user].filter(Boolean).join(" · ");
      // whose balance, at a glance: the provider's logo before its name
      const who = el("span", "pq-sub pq-bn");
      who.append(icon(q.icon || "generic"), el("span", "", q.name));
      card.append(who);
      // a balance field with several amounts: the first as the figure,
      // the others each a quiet line, a percent a meter (#420)
      const parts = q.balanceParts?.length ? q.balanceParts : [{ text: q.balance }];
      for (const [i, p] of parts.entries()) {
        if (!i && !p.label) card.append(el("b", "pq-amt", p.text));
        else if (!i) {
          const lead = el("span", "pq-lead");
          lead.append(el("b", "pq-amt", p.text), el("span", "", p.label));
          card.append(lead);
        } else {
          const line = el("span", "pq-sub pq-bp");
          line.append(el("span", "", p.label || ""), el("b", "", p.text));
          card.append(line);
        }
        if (p.percent != null) card.append(balanceMeter(p.percent));
      }
      // standing in for a reading that failed just now: as of when
      if (q.asOf) {
        card.classList.add("stale");
        card.title += "\n" + asOfText(q);
        card.append(el("span", "pq-sub pq-asof", t("As of {when}", { when: stamp(q.asOf) })));
      }
      grid.append(card);
    }
    g.append(grid);
    box.append(g);
  }
  restoreFlash();
  panelAge();
  fit();
  requestAnimationFrame(focusQuotaCard);
}

// asOfText: an allowance standing in for one that couldn't be read just
// now (a vendor rate limiting its usage endpoint) says when it was read.
function asOfText(q, short = false) {
  const text = short ? t("As of {when}", { when: stamp(q.asOf) })
    : t("As of {when} — couldn't be read just now", { when: new Date(q.asOf).toLocaleString() });
  return q.windows?.some((w) => w.resetsAt && new Date(w.resetsAt).getTime() <= Date.now())
    ? text + " · " + t("A cached window has expired; current allowance is unknown") : text;
}

// shortWindow: "5 hours" as 5h, "7 days" as 7d; any other name as it is.
function shortWindow(name) {
  const m = /^(\d+)\s*(minute|hour|day|week|month)s?$/i.exec(name || "");
  return m ? m[1] + { minute: "m", hour: "h", day: "d", week: "w", month: "mo" }[m[2].toLowerCase()] : t(name);
}

function panelQuotaCard(q) {
  const card = el("div", "pq-card");
  card.dataset.card = trayCardID(q);
  card.append(el("span", "pq-user", q.user || q.name));
  card.title = [q.name, q.user, q.plan, q.until ? planTerm(q) : "", q.balance && t("Balance") + " " + q.balance].filter(Boolean).join(" · ");
  if (q.error) {
    card.classList.add("err");
    card.append(el("span", "pq-sub err", quotaError(q.error)));
    card.title += "\n" + q.error;
    return card;
  }
  if (q.asOf) {
    card.classList.add("stale");
    card.title += "\n" + asOfText(q);
  }
  // a pool's 5-hour and weekly rings, two pools of them, else three
  const fam = familyWindows(q.windows.filter((w) => !w.unlimited));
  if (!fam.length && q.windows.some((w) => w.unlimited)) { card.append(el("span", "pq-sub", t("Unlimited"))); return card; }
  const ws = fam.slice(0, fam.some((w) => w.members) ? 4 : 3);
  // when the windows begun start again: the first bare, the others by name
  const begun = ws.filter((w) => w.resetsAt && w.used > 0);
  card.append(el("span", "pq-sub", begun.length
    ? begun.map((w, i) => {
      const at = new Date(w.resetsAt);
      return at <= Date.now() ? (i || w.members ? ringName(w) + " " : "") + resetText(at)
        : (i || w.members ? ringName(w) + " " : "↻ ") + resetClock(at);
    }).join(" · ")
    : t("Not used yet")));
  const rings = el("span", "pq-rings");
  for (const w of ws) {
    const used = Math.max(0, Math.min(100, w.used));
    const r = el("span", "pq-ring" + (used >= 90 ? " full" : ""));
    const dial = el("span", "pq-dial");
    dial.style.setProperty("--p", quotaFill(w));
    dial.append(el("b", "", quotaFill(w) + "%"));
    // a pool's ring: its first word over its span, too long for one line
    const rn = el("span", "pq-rn", w.window ? undefined : ringName(w));
    if (w.window) {
      rn.classList.add("pq-rn2");
      rn.append(el("span", "", w.pool.split(/[ &]/)[0]), el("span", "", shortWindow(w.window)));
    }
    r.append(dial, rn);
    r.title = t(w.name) + " · " + quotaText(w) + (w.resetsAt ? "\n" + resetText(new Date(w.resetsAt), new Date(w.resetsAt).toLocaleString()) + (new Date(w.resetsAt) > Date.now() ? " · " + untilText(new Date(w.resetsAt)) : "") : "")
      + (w.tiers ? "\n\n" + tiersText(w) + "\n" : "")
      + (w.members ? "\n\n" + poolTip(w) + "\n" : "")
      + "\n" + t(quotaLeft ? "Show how much of each window is used" : "Show how much of each window is left");
    // used or left turns here too, as on the Usage page (#124)
    r.onclick = () => setQuotaLeft(!quotaLeft);
    rings.append(r);
  }
  card.append(rings);
  // the first window's cycle, a thin line under the card (#651)
  const spark = ws[0] && !ws[0].tiers ? quotaSpark(q, ws[0]) : null;
  if (spark) card.append(spark);
  if (q.asOf) card.append(el("span", "pq-sub pq-asof", asOfText(q, true)));
  if (q.resets?.count) {
    const r = el("div", "pq-resets");
    r.append(resetsWords(q.resets));
    const use = el("button", "pq-use", t("Use one…"));
    use.title = resetUseTitle(q);
    use.onclick = () => askReset(q);
    if (!q.resets.byWindow && q.provider === "codex") { // as on the Usage page
      const auto = autoResetButton(q, "pq-use pq-auto");
      if (auto) r.append(auto);
      r.append(use);
    }
    card.append(r);
  }
  return card;
}

// resetsWords: a Codex account's rate-limit resets, "↺ 2 resets · until
// Sat 22:30", the date only when one of them runs out. A GLM Coding team
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
// first, so no one hesitates for fear of losing one that lasts longer.
function resetUseTitle(q) {
  const r = q.resets;
  return (r.until
    ? t("Uses the reset that runs out first ({when}), never one that lasts longer.", { when: new Date(r.until).toLocaleString() })
    : t("Uses one of its resets; none of them runs out."))
    + "\n" + t("This account's windows start again at once, as if none had been used. You're asked before anything is spent.");
}

// autoResetButton turns on or off a Codex account spending a
// reset by itself: once its week is used up and no other account can
// answer, one a week at most, and one about to run out unused shortly
// before it does. Off unless the user turns it on; nothing for
// an account with no name to keep it by.
function autoResetButton(q, cls) {
  const kept = { codex: "codexAutoReset" }[q.provider];
  if (!q.user || !kept) return null;
  const who = q.user.toLowerCase();
  const on = !!(state.settings?.[kept] || []).includes(who);
  const b = el("button", cls + (on ? " on" : ""), t("Auto-use"));
  b.setAttribute("aria-pressed", String(on));
  b.title = t(on ? "On: a reset is used by itself when this account's week is used up and no other account can answer, one a week at most, and one about to run out unused is used shortly before it does. Click to turn it off."
    : "Use a reset by itself when this account's week is used up and no other account can answer, one a week at most. A reset about to run out is used shortly before it does, if the account has been used. The five hours running out never uses one.");
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

// askReset: spending a Codex account's reset can't be taken
// back, so it asks first; then it says what came of it and reads the
// usage again.
let confirmAsk = null;
function askReset(q) {
  const ed = el("div", "editor reset-ask");
  const head = el("div", "ehead");
  head.append(icon(q.icon || "codex"), el("b", "", t("Use a Codex reset?")));
  ed.append(head);
  const who = q.user || q.name;
  ed.append(el("p", "lib-confirm", t(q.resets.count === 1
    ? "{who} has 1 reset. Using it starts its windows again at once, as if none of them had been used. It can't be undone."
    : "{who} has {n} resets. Using one starts its windows again at once, as if none of them had been used. It can't be undone.", { who, n: q.resets.count })));
  if (q.resets.until) ed.append(el("p", "lib-confirm", t("The one used is the one that runs out first, {when}.", { when: new Date(q.resets.until).toLocaleString() })));
  // nothing used yet: a reset would start nothing again
  if (!q.windows?.some((w) => w.used > 0)) ed.append(el("p", "lib-confirm reset-idle", t("None of its windows has been used yet, so there is nothing to start again.")));
  const bar = el("div", "bar");
  const go = el("button", "text primary", t("Use a reset"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    try {
      const out = await api("usage/codex-reset", { user: q.user || "" });
      closeConfirmAsk();
      status(who + ": " + resetOutcome(out), out.code === "reset" ? "ok" : "err");
      loadQuotas();
    } catch (err) {
      go.disabled = false;
      go.classList.remove("busy");
      status(err.message, "err");
    }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  go.focus();
}
function closeConfirmAsk() {
  if (!confirmAsk) return;
  confirmAsk = null;
  closeModal().then(() => { if (!confirmAsk) $("#modal").classList.remove("lib"); });
}
// the dialog is the providers page's: while this asks, its backdrop and
// Escape closes only the confirmation (in the panel it would hide the window)
$("#modal").addEventListener("click", (e) => {
  if (confirmAsk && e.target === e.currentTarget) { e.stopImmediatePropagation(); closeConfirmAsk(); }
}, true);
document.addEventListener("keydown", (e) => {
  if (confirmAsk && e.key === "Escape") { e.preventDefault(); e.stopImmediatePropagation(); closeConfirmAsk(); }
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

// balanceRow: what is left on an account, as a figure; a balance field
// with several amounts, each on a line of its own, its label quiet and the
// first the one that counts, a percent a meter (amber from 90%, as the
// panel's rings) (#420). Under it, when it was read, unless the card
// says that under its windows (when false).
function balanceRow(sub, why, when = true) {
  const parts = sub.balanceParts;
  const b = el("div", "quota-balance" + (parts?.length ? " parts" : ""));
  b.title = t(why);
  if (!parts?.length) b.append(el("span", "", t("Balance")), el("b", "", sub.balance));
  for (const [i, p] of (parts || []).entries()) {
    const row = el("div", "bal-part" + (i ? "" : " lead"));
    row.append(el("span", "", p.label || (i ? "" : t("Balance"))), el("b", "", p.text));
    if (p.percent != null) row.append(balanceMeter(p.percent));
    b.append(row);
  }
  const read = when && readWhen(sub);
  if (read) b.append(read);
  return b;
}

// balanceMeter: a balance field's percent, as a meter.
function balanceMeter(percent) {
  const share = Math.max(0, Math.min(100, percent));
  const track = el("div", "quota-track" + (share >= 90 ? " full" : ""));
  const fill = el("i");
  fill.style.width = share + "%";
  track.append(fill);
  return track;
}

// readWhen: when a card's figures were read, quietly under them: "As of
// …" for one standing in for a reading that failed just now, else how
// long ago, kept current.
function readWhen(q) {
  if (q.asOf) {
    const s = el("div", "quota-read stale", asOfText(q));
    s.title = asOfText(q);
    return s;
  }
  if (!q.readAt) return null;
  const s = el("div", "quota-read", t("Updated {when}", { when: ago(q.readAt) }));
  s.dataset.ago = q.readAt;
  s.title = new Date(q.readAt).toLocaleString();
  return s;
}
setInterval(() => {
  for (const s of document.querySelectorAll(".quota-read[data-ago]")) s.textContent = t("Updated {when}", { when: ago(s.dataset.ago) });
}, 30000);

// familyQuota: an account's windows one a model family where they name
// one, and "Every model", for above them, turning to each window and back
// in place: the card grows or shrinks below it, the page doesn't move.
const everyModel = new Set(); // provider|user shown window by window
function familyQuota(sub) {
  const fam = sub.error ? sub.windows : familyWindows(sub.windows);
  if (fam === sub.windows) return [quotaWindows(sub), null];
  const key = sub.provider + "|" + (sub.user || "");
  const models = pooledModels(sub.windows);
  const pooled = models !== sub.windows;
  const shown = () => quotaWindows({ ...sub, windows: everyModel.has(key) ? models : fam });
  let box = shown();
  const b = el("button", "text quota-every");
  const label = () => {
    const all = everyModel.has(key);
    b.textContent = all ? t(pooled ? "By group" : "By family") : t("Every model ({n})", { n: models.length });
    b.title = all ? t(pooled ? "Each group of models' 5-hour and weekly allowance, shared by its models" : "One figure a model family, its most used model's") : t("Each model's allowance, level by level");
    b.setAttribute("aria-expanded", String(all));
  };
  label();
  b.onclick = () => {
    if (everyModel.has(key)) everyModel.delete(key); else everyModel.add(key);
    const next = shown();
    if (box.classList.contains("brief")) next.classList.add("brief");
    box.replaceWith(next);
    box = next;
    label();
  };
  return [box, b];
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
    quota.append(labels);
    if (!w.unlimited) quota.append(track);
    // when it starts again, on the clock and how long until then
    if (w.resetsAt) {
      const at = new Date(w.resetsAt);
      const r = el("div", "quota-reset");
      r.append(el("span", "", resetText(at) + (at > Date.now() ? " ·" : "")));
      if (at > Date.now()) r.append(" ", el("span", "", untilText(at)));
      quota.append(r);
      quota.title = resetText(at, at.toLocaleString());
    }
    // a model family's figure: its models, level by level, in its tooltip
    if (w.tiers) quota.title = t("{family}: the most used of its models", { family: w.name }) + "\n" + tiersText(w);
    // a pool's: when it starts again, and the models it counts
    if (w.members) quota.title = [quota.title, poolTip(w)].filter(Boolean).join("\n");
    windows.append(quota);
  }
  quotaFit.observe(windows);
  return windows;
}

// Keep the last date and as many earlier dates as fit, without clipping their
// text. Visibility keeps each label aligned to its bar; resizing restores them.
function fitChartLabels() {
  const narrow = web && matchMedia("(max-width: 760px), (pointer: coarse) and (max-width: 1024px)").matches;
  for (const labels of document.querySelectorAll(".chart .labels")) {
    const spans = [...labels.children];
    for (const span of spans) span.style.visibility = "";
    if (!narrow || !labels.offsetWidth) continue;
    const box = labels.getBoundingClientRect();
    let right = box.right + 8;
    for (const span of spans.reverse()) {
      if (!span.textContent) continue;
      const range = document.createRange();
      range.selectNodeContents(span);
      const r = range.getBoundingClientRect();
      if (r.right + 8 > right + 1 || r.left < box.left - 1) span.style.visibility = "hidden";
      else right = r.left;
    }
  }
}
new ResizeObserver(fitChartLabels).observe($("#view-usage"));

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
    cost.append(el("b", "", "≈" + c), el("span", "", t("effective prices")));
    cost.title = u.unpriced ? t(u.unpriced === 1 ? "{n} call had no known price and is not counted" : "{n} calls had no known price and are not counted", { n: u.unpriced }) : t("Estimated using effective model prices, including custom prices");
  } else if (u.calls) {
    cost.append(el("span", "", t("no price for these models")));
  }

  const stats = $("#stats");
  stats.replaceChildren();
  const empty = !u.calls;
  $("#chart").hidden = empty;
  for (const id of ["usageAgents", "usageModels"]) $("#" + id).hidden = empty;
  for (const h of $$("#view-usage .row-head")) h.hidden = empty;
  $("#usageKeysHead").hidden = $("#usageKeys").hidden = empty || !u.callerKeys?.length;
  // each subscription account's share, by the account that answered (#557)
  $("#usageAccountsHead").hidden = $("#usageAccounts").hidden = empty || !u.accounts?.length;
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
  fitChartLabels();

  const total = Math.max(1, tokensOf(u));
  const list = (id, groups) => {
    const box = $("#" + id);
    box.replaceChildren();
    for (const g of groups) {
      const r = el("div", "row stat");
      r.append(icon(g.icon || "generic"));
      const who = el("div", "who");
      who.append(el("div", "name" + (g.name ? "" : " faint"), g.name || t("account not recorded")));
      const sub = [];
      if (g.sub) sub.push(g.sub);
      sub.push(t(g.calls === 1 ? "{n} call" : "{n} calls", { n: g.calls }));
      if (g.errors) sub.push(t("{n} failed", { n: g.errors }));
      // how long the streamed replies took to begin, and how fast they
      // wrote after (#196)
      if (g.timed) {
        sub.push(t("TTFT {ms}", { ms: ledTook(Math.round(g.ttft_ms / g.timed)) }));
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
  list("usageAccounts", u.accounts || []);
  list("usageKeys", u.callerKeys || []);
  $("#usageNote").textContent = t("Counted from the providers' own usage reports on every call through the gateway · {path}", { path: u.path });
}

// ---------- requests ----------
//
// The ledger: every request of the period, one row each, newest first —
// what the agent asked for, where it went, the model sent and the one the
// reply says answered, the tokens, what they cost at effective prices, how long
// it took and how it ended — to set beside a vendor's own bill. The
// server pages it (/api/usage/requests) and saves it whole as CSV.

let ledger = null; // the page shown: { rows, offset, total, agents, …totals }
let ledOffset = 0, ledAgent = "", ledProvider = "", ledAccount = "", ledCallerKey = "", ledFailed = false, ledQuery = "", ledModel = "", ledRoute = 0, ledComputer = "", ledDay = "";

// computerOpts are the Computer filter's choices: this computer, the others
// together, and each other one, as sync shares their usage (#542); none
// while no other computer's is here.
function computerOpts(cs) {
  if (!cs?.length) return [];
  const others = cs.filter((c) => c.id !== "this");
  return [{ v: "this", name: t("This computer"), note: "" },
    ...(others.length > 1 ? [{ v: "others", name: t("Other computers"), note: "" }] : []),
    ...others.map((c) => ({ v: c.id, name: c.name || c.id, note: "" }))];
}
const LED_PAGE = 100;
// Remember the chart metric; start each app load split by model.
let ledMetric = "tokens", ledSplit = "model";
try {
  const m = localStorage.getItem("magpie.ledMetric");
  if (["tokens", "cost", "calls"].includes(m)) ledMetric = m;
} catch {}

let ledRouteInfo = null, ledBeforeRoute = null;
window.openUsageRoute = (route) => {
  if (!ledBeforeRoute) ledBeforeRoute = { period, ledOffset, ledAgent, ledProvider, ledAccount, ledCallerKey, ledFailed, ledQuery, ledModel, ledComputer, ledDay };
  ledRoute = route.id;
  ledRouteInfo = route;
  ledOffset = 0; ledAgent = ""; ledProvider = ""; ledAccount = ""; ledCallerKey = ""; ledFailed = false; ledQuery = ""; ledModel = ""; ledComputer = "";
  ledDay = "";
  $("#ledQ").value = "";
  period = "all";
  usageTab = "requests";
  ledger = null;
  show("usage");
};

function ledParams(extra) {
  const q = new URLSearchParams({ period });
  if (ledDay) q.set("day", ledDay);
  if (ledAgent) q.set("agent", ledAgent);
  if (ledProvider) q.set("provider", ledProvider);
  if (ledAccount) q.set("account", ledAccount);
  if (ledComputer) q.set("computer", ledComputer);
  if (ledCallerKey) q.set("callerKey", ledCallerKey);
  if (ledRoute) q.set("route", ledRoute);
  if (ledFailed) q.set("failed", "1");
  if (ledModel) q.set("model", ledModel);
  else if (ledQuery.trim()) q.set("q", ledQuery.trim());
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
// another than the one sent; plain when it is that one under a dated name,
// or the member a remote magpie's routing group sent it to, said in its title
function ledServed(r) {
  if (!r.served) return el("span", "faint", "—");
  if (r.routed && !r.swapped) {
    const k = el("span", "muted model-value routed", r.served);
    k.title = window.routedWhy ? window.routedWhy({ model: r.model, served: r.served }) : "";
    return k;
  }
  if (!r.swapped) return el("span", "model-value", r.served);
  const k = el("span", "swap", r.served);
  k.title = window.swapWhy ? window.swapWhy({ model: r.model, served: r.served }) : "";
  return k;
}

// a request that failed: told by its status, or, for one read from a
// session file, which records none, by the error that ended it
const ledFailed_ = (r) => r.status >= 400 || !!r.err;
const ledKey = (r) => [r.t, r.agent, r.model, r.rid, r.session, r.computer || ""].join("|");
const ledOpen = new Set(); // the rows opened, kept across a refresh

// what is known of one request beyond its row: the id its vendor gave it,
// the path it went to, and, if it failed, what the vendor said
function ledDetail(r, cols) {
  const dl = el("dl", "led-dl");
  const add = (name, value, cls) => {
    if (value === "" || value == null) return;
    dl.append(el("dt", "", t(name)), el("dd", cls || "", value));
  };
  if (ledFailed_(r)) {
    add("Status", r.status ? String(r.status) : "—", "bad");
    add("Error type", r.err_type, "bad");
    add(r.source === "log" ? "Error" : "Upstream said", r.err, "said");
  }
  if (r.computerName) add("Computer", r.computerName);
  add("Request ID", r.rid);
  add("Endpoint", r.ep);
  add("Session ID", r.session);
  if (r.ttft_ms) add("First token", ledTook(r.ttft_ms));
  if (r.reasoning) add("Reasoning tokens", ledNum(r.reasoning));
  if (r.session_provider) add("Recorded provider ID", r.session_provider);
  if (r.session_account) add("Session account", r.session_account);
  if (r.source === "log" && r.session_account && r.session_official_login) add("Login method", t("Official login"));
  if (r.pricing_model) add("API price reference", r.pricing_model);
  // with the archive on, a request it has no copy of says so: from before
  // it was on, or not through the gateway
  if (!r.archive && !r.computer && (providers?.gateway?.archive?.on ?? state.settings?.requestArchive)) add("Request archive", t("Not archived"), "muted");
  if (r.source === "log") add("Data source", t("Read from the agent's session file. The account is shown only when local metadata identifies it; no service provider is inferred."), "muted");
  const tr = el("tr", "led-detail");
  const td = el("td");
  td.colSpan = cols;
  const box = el("div", "led-box");
  box.append(dl.childElementCount ? dl : el("span", "faint", t("Nothing more was kept of this request")));
  // the call as the request archive kept it, when it was on (#447)
  if (r.archive) {
    const ab = el("div", "led-archive");
    const draw = () => ab.replaceChildren(archivePanel(r.archive, "led|" + ledKey(r), draw));
    draw();
    box.append(ab);
  }
  // what was said: read from the agent's session file, when the row is
  // opened; one made on another computer has its files there, and sync
  // brings none of what was said
  const cx = el("div", "led-cx");
  if (r.computer) {
    cx.append(el("p", "cx-none", t("What was said stays on {computer}: sync shares the usage alone.", { computer: r.computerName || r.computer })));
  } else {
    cx.append(el("p", "cx-none", t("Loading…")));
    ledLoadContent(r).then((c) => { if (cx.isConnected) cx.replaceChildren(ledContentBox(c)); });
  }
  box.append(cx);
  td.append(box);
  tr.append(td);
  return tr;
}

// What was said in a request — what its agent was given, and what came back —
// is in the agent's own session file, and read from it when a row is opened
// (the server finds the call by its session and time); magpie keeps no copy.
// What came is kept here while the list is looked at, so a row opened again, or
// drawn again as the list is read anew, doesn't ask again.
const ledContent = new Map();
const ledExpanded = new WeakSet();
function ledLoadContent(r) {
  const key = ledKey(r);
  if (!ledContent.has(key)) {
    if (ledContent.size > 60) ledContent.delete(ledContent.keys().next().value);
    const t0 = new Date(r.t).getTime();
    // a call of a session file is at its own time; a request the gateway logged began
    // a little before the file wrote it, and the call is in the span it took
    const [a, b] = r.source === "log" ? [t0 - 1, t0 + 1] : [t0 - 5e3, t0 + (r.ms || 0) + 30e3];
    // the window can hold the next calls of a quick tool loop too: the one that
    // ended nearest the request's own end is its call
    const at = r.source === "log" ? t0 : t0 + (r.ms || 0);
    const q = new URLSearchParams({ agent: r.agent, session: r.native_session || r.session || "", from: new Date(a).toISOString(), to: new Date(b).toISOString(), at: new Date(at).toISOString() });
    const pending = api("usage/requests/content?" + q).catch(() => ({ found: false, why: "read" })).then((c) => {
      if (!c.found && ledContent.get(key) === pending) ledContent.delete(key);
      return c;
    });
    ledContent.set(key, pending);
  }
  return ledContent.get(key);
}
const LED_WHY = {
  session: "No session was named with this request, so its session file can't be found",
  agent: "magpie reads the session files of Claude Code, Claude Desktop and Codex only",
  missing: "This request isn't in the agent's session files: they may be deleted, moved, or not written yet",
  read: "The session file couldn't be read",
};
const LED_KINDS = { tool_use: "Tool call", tool_result: "Tool result", thinking: "Thinking", context: "Context", image: "Image" };
const LED_ROLES = { user: "You", assistant: "Assistant", tool: "Tool" };

// one thing said: who said it, and its words in a box; long ones show a part and
// unroll, and reasoning and what the agent put in itself are folded
function ledSaid(p) {
  const head = el("div", "cx-h");
  head.append(el("span", "cx-role " + p.role, t(LED_KINDS[p.kind] || LED_ROLES[p.role] || p.role)));
  if (p.name) head.append(el("code", "cx-name", p.name));
  const words = el("pre", "cx-t", p.text);
  if (p.cut) words.append(el("em", "cx-cut", "\n" + t("… {n} more characters not shown", { n: ledNum(p.cut) })));
  if (p.kind === "thinking" || p.kind === "context") {
    const d = el("details", "cx-part fold");
    const sum = el("summary");
    sum.append(head);
    d.append(sum, words);
    return d;
  }
  const part = el("div", "cx-part");
  part.append(head, words);
  if (p.text.length > 700 || p.text.split("\n").length > 9) {
    part.classList.toggle("clamp", !ledExpanded.has(p));
    const more = el("button", "text cx-more", t(ledExpanded.has(p) ? "Collapse content" : "Show full content"));
    more.type = "button";
    more.onclick = () => {
      const clamped = part.classList.toggle("clamp");
      if (clamped) ledExpanded.delete(p); else ledExpanded.add(p);
      more.textContent = t(clamped ? "Show full content" : "Collapse content");
    };
    part.append(more);
  }
  return part;
}

function ledContentBox(c) {
  const box = el("div", "cx");
  if (!c.found) {
    box.append(el("p", "cx-none", t(LED_WHY[c.why] || LED_WHY.read)));
    return box;
  }
  for (const [name, parts] of [["Input", c.input || []], ["Output", c.output || []]]) {
    const sec = el("section", "cx-sec");
    sec.append(el("h4", "", t(name)));
    if (!parts.length) sec.append(el("p", "cx-none", t("Nothing was said here")));
    for (const p of parts) sec.append(ledSaid(p));
    box.append(sec);
  }
  if (c.cut) box.append(el("p", "cx-none", t("There was more than is shown here")));
  box.append(el("p", "cx-src", t("Read from the agent's session file; magpie keeps no copy")));
  return box;
}

// ---- the totals and the trend over the requests listed
//
// A strip of four totals, then the trend of one metric — tokens, cost or
// requests — as columns by the hour, day or week, each told apart by provider,
// agent or model, beside a ranking of the same: which is the chart's legend
// and a way in (a click on a provider or agent lists only its requests). The
// server sums it all (the ledger's "series" and "by"); the tray panel's Usage
// tab draws the same from the same answer.

// a cost as an axis says it: the currency Settings picks, no more digits than it takes
function ledMoney(v) {
  let sign = "$";
  if (currency === "cny" && fx.rate > 0) { v *= fx.rate; sign = "¥"; }
  return sign + (v >= 10 ? v.toFixed(0) : v >= 1 ? v.toFixed(1) : v.toFixed(2));
}

const LED_METRICS = [["tokens", "Tokens"], ["cost", "Cost"], ["calls", "Requests"]];
const LED_SPLITS = [["model", "Model"], ["provider", "Provider"], ["agent", "Agent"]];
const LED_SHOWN = 7; // told apart in a chart; the rest are "Other"
const allTokens = (x) => x.input + x.output + x.cache_read + x.cache_write;
// a metric's value of a point or a share, as the chart counts it
const ledValue = (m, x) => m === "cost" ? +x.cost || 0 : m === "calls" ? +x.calls || 0 : allTokens(x);
// of a point's part, which has only its calls, tokens and cost
const ledPart = (m, x) => m === "cost" ? +x.cost || 0 : m === "calls" ? +x.calls || 0 : +x.tokens || 0;
// a metric as its axis and its values say it
const ledFormat = (m, v) => m === "cost" ? ledMoney(v) : m === "calls" ? ledNum(Math.round(v)) : fmtN(Math.round(v));
const ledFormatLong = (m, v) => m === "cost" ? (fmtCost({ cost: v, unpriced: 0 }) || "—") : ledNum(Math.round(v));

// an axis's top with four steps under it: round numbers, the top at least the largest value
function ledAxis(max) {
  if (!(max > 0)) return 4;
  const e = 10 ** Math.floor(Math.log10(max / 4));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * e * 4 >= max) return m * e * 4;
  return 10 * e * 4;
}

// what a chart tells apart, most first, and the colour of each: those of the
// ranking's top, and the rest together as "Other"
function ledCategories(l, split, metric) {
  const list = (l.by?.[split] || []).slice().sort((a, b) => ledValue(metric, b) - ledValue(metric, a) || allTokens(b) - allTokens(a));
  const top = list.slice(0, LED_SHOWN).map((x, i) => ({ ...x, color: `var(--c${i + 1})` }));
  return { top, rest: list.slice(LED_SHOWN), list };
}

// when a point is: the hour, the day or the week it stands for
function ledWhen(p, bucket) {
  const d = new Date(p.time), lang = locale === "zh" ? "zh-CN" : "en";
  if (bucket === "hour") return d.toLocaleString(lang, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false });
  const day = d.toLocaleDateString(lang, { year: "numeric", month: "2-digit", day: "2-digit" });
  return bucket === "week" ? t("week of {label}", { label: day }) : day;
}
function ledTick(p, bucket) {
  const d = new Date(p.time);
  if (bucket === "hour") return String(d.getHours()).padStart(2, "0") + ":00";
  return d.toLocaleDateString(locale === "zh" ? "zh-CN" : "en", { month: "numeric", day: "numeric" });
}

const SVGNS = "http://www.w3.org/2000/svg";
function sv(tag, attrs, style) {
  const e = document.createElementNS(SVGNS, tag);
  for (const k in attrs) e.setAttribute(k, attrs[k]);
  if (style) Object.assign(e.style, style);
  return e;
}

// the columns: one per point of the answer's series, its part of each thing
// told apart stacked in the colours of the ranking. box is where it goes, its
// size the plot's; compact is for the tray panel, which has little room.
function drawLedColumns(box, l, split, metric, compact, chooseDay, focusDay) {
  focusDay ??= box.contains(document.activeElement) ? document.activeElement.dataset.day : "";
  if (chooseDay) box.chooseDay = chooseDay;
  box.replaceChildren();
  const plot = el("div", "plot");
  box.append(plot);
  const W = plot.clientWidth, H = plot.clientHeight, pts = l.series || [], n = pts.length;
  if (W < 100 || !n) return;
  const { top } = ledCategories(l.chartBy ? { by: l.chartBy } : l, split, metric);
  const totals = pts.map((p) => ledValue(metric, p));
  const max = Math.max(0, ...totals);
  if (!(max > 0)) {
    plot.append(el("div", "none", t(metric === "cost" ? "No known price for these requests" : "Nothing in this period")));
    return;
  }
  const topV = ledAxis(max);
  const selectable = l.bucket === "day" && !!box.chooseDay;
  const g = sv("svg", { viewBox: `0 0 ${W} ${H}`, role: selectable ? "group" : "img" });
  g.setAttribute("aria-label", t("Usage trend"));
  // the side's labels, measured as drawn: the plot starts where the widest
  // ends ("8000 万", "¥1250" are wider than "80M"), so none reaches past the
  // card's padding
  plot.append(g);
  const labs = [0, 1, 2, 3, 4].map((i) => {
    const lab = sv("text", { "text-anchor": "end", class: "axis" });
    lab.textContent = ledFormat(metric, topV * (1 - i / 4));
    g.append(lab);
    return lab;
  });
  const widest = Math.max(0, ...labs.map((lab) => { try { return lab.getComputedTextLength(); } catch { return 0; } }));
  const M = { l: widest > 0 ? Math.ceil(widest) + 6 : compact ? 34 : 44, r: 6, t: 8, b: 22 };
  const pw = W - M.l - M.r, ph = H - M.t - M.b, slot = pw / n, bw = Math.max(2, Math.min(compact ? 14 : 30, slot * 0.68));
  const Y = (v) => M.t + ph - (ph * v) / topV;
  labs.forEach((lab, i) => {
    const y = M.t + (ph * i) / 4;
    lab.setAttribute("x", M.l - 6);
    lab.setAttribute("y", y + 3.5);
    g.insertBefore(sv("line", { x1: M.l, x2: W - M.r, y1: y, y2: y, class: "grid" }), lab);
  });
  const room = Math.max(2, Math.floor(pw / (compact ? 46 : 62))), every = Math.ceil(n / room);
  pts.forEach((p, i) => {
    if (i % every) return;
    const x = sv("text", { x: M.l + slot * (i + 0.5), y: H - 6, "text-anchor": "middle", class: "axis" });
    x.textContent = ledTick(p, l.bucket);
    g.append(x);
  });
  const hot = sv("rect", { class: "hot", y: M.t, height: ph, rx: 3, width: slot }, { display: "none" });
  g.append(hot);
  const selected = pts.findIndex((p) => p.time.slice(0, 10) === l.day);
  if (selectable && selected >= 0) g.append(sv("rect", { class: "hot", x: M.l + slot * selected, y: M.t, height: ph, rx: 3, width: slot }));
  // each column: what the top of the ranking had of it, from the bottom up,
  // and what is left of its total as "Other"
  const segs = [];
  pts.forEach((p, i) => {
    const x = M.l + slot * (i + 0.5) - bw / 2;
    let y0 = 0;
    const draw = (key, color, v) => {
      if (!(v > 0)) return;
      const r = sv("rect", { x, width: bw, y: Y(y0 + v), height: Math.max(0.5, Y(y0) - Y(y0 + v)), rx: 1, class: "col", "data-k": key, "data-day": p.time.slice(0, 10), "data-color": color }, { fill: color });
      g.append(r);
      segs.push(r);
      y0 += v;
    };
    const by = p.by?.[split] || {};
    let stacked = 0;
    for (const c of top) { const v = ledPart(metric, by[c.id] || {}); stacked += v; draw(c.id, c.color, v); }
    draw("\0other", "var(--faint)", Math.max(0, totals[i] - stacked));
  });
  if (selectable) pts.forEach((p, i) => {
    const day = p.time.slice(0, 10);
    const hit = sv("rect", { class: "led-day", x: M.l + slot * i, y: M.t, width: slot, height: ph, fill: "transparent", role: "button", tabindex: 0, "aria-label": ledWhen(p, l.bucket), "aria-pressed": day === l.day, "data-day": day });
    const pick = () => box.chooseDay(day);
    hit.onclick = pick;
    hit.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); pick(); } };
    g.append(hit);
  });

  const tip = el("div", "tip");
  tip.hidden = true;
  plot.append(g, tip);
  const name = (c) => t(c.name || c.id || "—");
  const at = (e) => {
    const r = g.getBoundingClientRect();
    const i = Math.max(0, Math.min(n - 1, Math.floor(((e.clientX - r.left) / r.width * W - M.l) / slot)));
    const p = pts[i];
    hot.setAttribute("x", M.l + slot * i);
    hot.style.display = "";
    tip.replaceChildren(el("b", "", ledWhen(p, l.bucket)));
    const by = p.by?.[split] || {};
    const rows = top.map((c) => [name(c), c.color, ledPart(metric, by[c.id] || {})]);
    rows.push([t("Other"), "var(--faint)", Math.max(0, totals[i] - rows.reduce((a, x) => a + x[2], 0))]);
    for (const [nm, color, v] of rows.filter((x) => x[2] > 0).sort((a, b) => b[2] - a[2])) {
      const row = el("div");
      const sw = el("i");
      sw.style.background = color;
      row.append(sw, el("span", "", nm), el("em", "", ledFormatLong(metric, v)));
      tip.append(row);
    }
    const sum = el("div", "sum");
    sum.append(el("span", "", t("Total")), el("em", "", ledFormatLong(metric, totals[i])));
    if (metric !== "calls" && p.calls) sum.lastChild.append(" · " + t(p.calls === 1 ? "{n} request" : "{n} requests", { n: ledNum(p.calls) }));
    tip.append(sum);
    tip.hidden = false;
    const x = M.l + slot * (i + 0.5), w = tip.offsetWidth;
    tip.style.left = Math.max(0, x + 14 + w > W ? x - w - 14 : x + 14) + "px";
  };
  g.onpointermove = at;
  g.onpointerdown = at;
  g.onpointerleave = () => { hot.style.display = "none"; tip.hidden = true; };
  // the ranking asks for one thing to stand out
  box.emphasize = (key) => {
    for (const r of segs) {
      const dimDay = l.day && r.dataset.day !== l.day;
      r.style.opacity = dimDay || key != null && r.dataset.k !== key ? ".22" : "";
      r.style.fill = dimDay ? "var(--faint)" : r.dataset.color;
    }
  };
  box.emphasize(null);
  if (focusDay) box.querySelector(`.led-day[data-day="${CSS.escape(focusDay)}"]`)?.focus({ preventScroll: true });
}

// the ranking: who the requests were of, by the metric, the most first —
// each with its share, its other figures, and a click that lists only its
// requests (a provider or an agent), or those of its model. picked is the
// one the filter has picked, if any; the ones past the chart's colours are
// left out of it and said in a line.
function drawLedRank(box, l, split, metric, picked, choose, compact) {
  // a redraw (a pick, a refresh) leaves the ranking scrolled where it was
  const kept = box.scrollTop;
  box.replaceChildren();
  const { top, rest, list } = ledCategories(l, split, metric);
  if (l.chartBy) {
    const colors = ledCategories({ by: l.chartBy }, split, metric).top;
    for (const x of top) x.color = colors.find((c) => c.id === x.id)?.color || "var(--faint)";
  }
  if (!list.length) return;
  const sum = list.reduce((a, x) => a + ledValue(metric, x), 0) || 1;
  const leader = Math.max(1, ledValue(metric, top[0]));
  const one = (x, color, plain) => {
    const b = el("button", "rk" + (plain ? " plain" : "") + (picked && picked === x.id ? " on" : ""));
    b.type = "button";
    const v = ledValue(metric, x);
    const l1 = el("div", "rk-a");
    const sw = el("i", "rk-sw");
    sw.style.background = color;
    l1.append(sw);
    if (x.icon && split !== "model") l1.append(icon(x.icon));
    const nm = el("span", "rk-nm", t(x.name || x.id || "—"));
    nm.title = t(x.name || x.id || "");
    l1.append(nm, el("span", "rk-val", ledFormatLong(metric, v)));
    const bar = el("div", "rk-bar"), fill = el("i");
    fill.style.width = Math.max(1.5, (100 * v) / leader).toFixed(1) + "%";
    fill.style.background = color;
    bar.append(fill);
    const more = [];
    if (metric !== "calls") more.push(t(x.calls === 1 ? "{n} request" : "{n} requests", { n: ledNum(x.calls) }));
    if (metric !== "tokens") more.push(t("{n} tokens", { n: fmtN(allTokens(x)) }));
    if (metric !== "cost" && x.cost) more.push("≈" + fmtCost(x));
    const prompt = x.input + x.cache_write + x.cache_read;
    if (prompt && !compact) more.push(t("hit rate {p}", { p: Math.round((100 * x.cache_read) / prompt) + "%" }));
    more.push(Math.round((100 * v) / sum) + "%");
    const l2 = el("div", "rk-b");
    l2.append(more.join(" · "));
    if (x.errors) l2.append(" · ", Object.assign(el("span", "rk-bad"), { textContent: t("{n} failed", { n: x.errors }) }));
    b.append(l1, bar, l2);
    if (!plain) b.onclick = () => choose(x);
    b.onpointerenter = () => box.chart?.emphasize?.(x.id);
    b.onpointerleave = () => box.chart?.emphasize?.(null);
    return b;
  };
  for (const x of top) box.append(one(x, x.color));
  if (rest.length) {
    const tot = rest.reduce((acc, x) => {
      for (const k of ["calls", "errors", "input", "output", "cache_read", "cache_write", "cost"]) acc[k] = (acc[k] || 0) + (x[k] || 0);
      return acc;
    }, { id: "\0other", name: t("Other ({n})", { n: rest.length }) });
    box.append(one(tot, "var(--faint)", true));
  }
  box.scrollTop = kept;
  box.onscroll = () => ledRankEdges(box);
  ledRankEdges(box);
}

// a ranking that scrolls fades out at the edge with more beyond it, and its
// rail (the window's) shows how much there is and where it is scrolled to
function ledRankEdges(box) {
  const end = box.scrollHeight - box.clientHeight;
  box.classList.toggle("more-above", end > 1 && box.scrollTop > 1);
  box.classList.toggle("more-below", end > 1 && box.scrollTop < end - 1);
  const rail = box.rail;
  if (!rail) return;
  rail.hidden = !(end > 1) || !box.offsetParent;
  if (rail.hidden) return;
  const h = box.clientHeight, thumb = Math.max(24, (h * h) / box.scrollHeight);
  rail.style.top = box.offsetTop + "px";
  rail.style.height = h + "px";
  rail.firstChild.style.height = thumb + "px";
  rail.firstChild.style.transform = `translateY(${((h - thumb) * box.scrollTop) / end}px)`;
}

// the rail's thumb drags the ranking; a click on the rail takes it there
function ledRail(rail, box) {
  box.rail = rail;
  rail.onpointerdown = (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    const end = box.scrollHeight - box.clientHeight, thumb = rail.firstChild.offsetHeight, room = rail.clientHeight - thumb;
    if (end <= 0 || room <= 0) return;
    if (e.target !== rail.firstChild) {
      // the thumb's middle to where the rail was clicked
      const y = e.clientY - rail.getBoundingClientRect().top - thumb / 2;
      box.scrollTop = (Math.max(0, Math.min(room, y)) / room) * end;
    }
    const y0 = e.clientY, top0 = box.scrollTop;
    rail.setPointerCapture(e.pointerId);
    rail.classList.add("drag");
    rail.onpointermove = (m) => { box.scrollTop = top0 + ((m.clientY - y0) / room) * end; };
    rail.onpointerup = rail.onpointercancel = () => {
      rail.classList.remove("drag");
      rail.onpointermove = rail.onpointerup = rail.onpointercancel = null;
    };
  };
}

function renderLedgerDash(l) {
  const dash = $("#ledDash");
  dash.hidden = !l.total && !l.day;
  if (dash.hidden) return;
  const total = allTokens(l);
  const prompt = l.input + l.cache_write + l.cache_read;
  const rate = prompt ? l.cache_read / prompt : 0;
  const strip = $("#ledKpi");
  strip.replaceChildren();
  const block = (label, value, sub, cls, title) => {
    const b = el("div", "blk");
    if (title) b.title = title;
    b.append(el("span", "k", t(label)), el("span", "v" + (cls ? " " + cls : ""), value));
    if (sub) b.append(sub);
    strip.append(b);
    return b;
  };
  const line = (...parts) => { const s = el("span", "sub"); s.append(...parts); return s; };
  block("Tokens", fmtN(total), line(t("{a} in · {b} out", { a: fmtN(l.input), b: fmtN(l.output) }), " · ", t("{a} cache", { a: fmtN(l.cache_read + l.cache_write) })), "", ledNum(total));
  block("Requests", ledNum(l.calls), line(l.errors ? t("{n} failed", { n: ledNum(l.errors) }) + " · " + t("{p} succeeded", { p: (100 * (1 - l.errors / l.calls)).toFixed(l.errors ? 1 : 0) + "%" }) : t("none failed")));
  const c = fmtCost(l);
  block("Cost", c ? "≈" + c : "—", line(l.unpriced ? t(l.unpriced === 1 ? "{n} call had no known price and is not counted" : "{n} calls had no known price and are not counted", { n: l.unpriced }) : t("effective prices")), c ? "cost" : "");
  const hit = block("Cache hit rate", (100 * rate).toFixed(1) + "%", null, "", t("The share of the prompt read from the cache"));
  const meter = el("div", "meter"), fill = el("i");
  fill.style.width = (100 * rate).toFixed(1) + "%";
  meter.append(fill);
  hit.append(meter);
  drawLedTrend();
}

function drawLedTrend() {
  const l = ledger;
  if (!l || $("#ledDash").hidden) return;
  // the two switches
  const pill = (host, key, opts, cur, pick) => {
    host.replaceChildren();
    for (const [id, name] of opts) {
      const b = el("button", "opt" + (id === cur ? " on" : ""), t(name));
      b.onclick = () => { if (id !== cur) pick(id); };
      host.append(b);
    }
    slide(host, key);
  };
  pill($("#ledMetric"), "ledMetric", LED_METRICS, ledMetric, (id) => { ledMetric = id; try { localStorage.setItem("magpie.ledMetric", id); } catch {} drawLedTrend(); });
  pill($("#ledSplit"), "ledSplit", LED_SPLITS, ledSplit, (id) => { ledSplit = id; drawLedTrend(); });
  const chart = $("#ledChart"), rank = $("#ledRank");
  drawLedColumns(chart, l, ledSplit, ledMetric, false, (day) => {
    ledDay = ledDay === day ? "" : day;
    ledOffset = 0;
    loadLedger().catch((e) => status(e.message, "err"));
  });
  rank.chart = chart;
  if (!rank.rail) ledRail($("#ledRail"), rank);
  const picked = ledSplit === "provider" ? ledProvider : ledSplit === "agent" ? ledAgent : ledModel;
  drawLedRank(rank, l, ledSplit, ledMetric, picked, (x) => {
    // a click lists only that one's requests; on the one listed, all again
    if (ledSplit === "provider") ledProvider = ledProvider === x.id ? "" : x.id;
    else if (ledSplit === "agent") ledAgent = ledAgent === x.id ? "" : x.id;
    else { const q = $("#ledQ"); ledModel = ledModel === x.id ? "" : x.id; ledQuery = ledModel; q.value = ledQuery; }
    ledOffset = 0;
    loadLedger().catch((e) => status(e.message, "err"));
  }, false);
}
// a window that changes size redraws the chart at its new width
new ResizeObserver(() => $("#ledWrap").style.setProperty("--ledw", $("#ledWrap").clientWidth + "px")).observe($("#ledWrap"));
new ResizeObserver(() => {
  if (!ledger || usageTab !== "requests" || view !== "usage" || $("#ledDash").hidden) return;
  drawLedColumns($("#ledChart"), ledger, ledSplit, ledMetric, false);
  ledRankEdges($("#ledRank"));
}).observe($("#ledChart"));

const LED_COLS = [
  ["Time"], ["Agent"], ["Requested"], ["Provider · account"], ["Sent"], ["Served"], ["Effort"],
  ["In", "n"], ["Out", "n"], ["Cache write", "n"], ["Cache read", "n"], ["Cost", "n"], ["Duration", "n"], ["Status"],
];

function renderLedger() {
  const l = ledger;
  const callers = l.callerKeys || [];
  const providers = l.providers || [];
  const missingAgent = ledAgent && !l.agents.some((a) => a.id === ledAgent);
  const missingProvider = ledProvider && !providers.some((p) => p.id === ledProvider);
  const accounts = l.accounts || [];
  const missingAccount = ledAccount && !accounts.some((a) => a.id === ledAccount);
  const missingCaller = ledCallerKey && !callers.some((k) => k.id === ledCallerKey);
  const computers = computerOpts(l.computers);
  const missingComputer = ledComputer && !computers.some((c) => c.v === ledComputer);
  if (missingAgent || missingProvider || missingAccount || missingCaller || missingComputer) {
    if (missingComputer) ledComputer = "";
    if (missingAccount) ledAccount = "";
    if (missingAgent) ledAgent = "";
    if (missingProvider) ledProvider = "";
    if (missingCaller) ledCallerKey = "";
    ledOffset = 0;
    // Reload the rows too: this response still belongs to the missing filters.
    loadLedger().catch((e) => status(e.message, "err"));
    return;
  }
  const view = $("#view-usage");
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  renderPeriod();
  renderArchive();

  const cost = $("#usageCost");
  cost.replaceChildren();
  cost.title = "";
  const c = fmtCost(l);
  if (c) {
    cost.append(el("b", "", "≈" + c), el("span", "", t("effective prices")));
    cost.title = l.unpriced ? t(l.unpriced === 1 ? "{n} call had no known price and is not counted" : "{n} calls had no known price and are not counted", { n: l.unpriced }) : t("Estimated using effective model prices, including custom prices");
  }

  // the filters: the agents with calls in the period, and failures alone
  sessPick($("#ledComputer"), "All computers", ledComputer, computers, "Computer", (v) => { ledComputer = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  sessPick($("#ledAgent"), "All agents", ledAgent, l.agents.map((a) => ({ v: a.id, name: a.name, note: "" })), "Agent", (v) => { ledAgent = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  sessPick($("#ledProvider"), "All providers", ledProvider, providers.map((p) => ({ v: p.id, name: t(p.name), note: "" })), "Provider", (v) => { ledProvider = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  sessPick($("#ledAccount"), "All accounts", ledAccount, accounts.map((a) => ({ v: a.id, name: a.id, note: (a.providers || []).map((p) => t(p)).join(" · ") })), "Account", (v) => { ledAccount = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
  sessPick($("#ledKey"), "All gateway keys", ledCallerKey, callers.map((k) => ({
    v: k.id, name: k.name, note: "",
  })), "Gateway keys", (v) => { ledCallerKey = v; ledOffset = 0; loadLedger().catch((e) => status(e.message, "err")); });
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
  const routeFilter = $("#ledRoute");
  routeFilter.hidden = !ledRoute;
  $("#ledRouteLabel").textContent = ledRouteInfo ? t("Request: {what}", { what: new Date(ledRouteInfo.time).toLocaleString(locale === "zh" ? "zh-CN" : "en") + " · " + ledRouteInfo.model }) : "";
  $("#ledRouteClear").title = t("Clear filter");
  $("#ledRouteClear").setAttribute("aria-label", t("Clear filter"));
  $("#ledRouteClear").onclick = () => {
    ledRoute = 0; ledRouteInfo = null;
    if (ledBeforeRoute) ({ period, ledOffset, ledAgent, ledProvider, ledAccount = "", ledCallerKey, ledFailed, ledQuery, ledModel, ledComputer = "", ledDay = "" } = ledBeforeRoute);
    ledBeforeRoute = null;
    $("#ledQ").value = ledQuery;
    loadLedger().catch((e) => status(e.message, "err"));
  };
  renderLedgerDash(l);

  const wrap = $("#ledWrap");
  const pager = $("#ledPager");
  if (!l.total) {
    wrap.classList.add("none");
    const filtered = ledDay || ledRoute || ledAgent || ledProvider || ledAccount || ledComputer || ledCallerKey || ledModel || ledFailed || ledQuery.trim();
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
    const bad = ledFailed_(r);
    const key = ledKey(r);
    const tr = el("tr", "led-row" + (bad ? " bad" : "") + (ledOpen.has(key) ? " open" : ""));
    tr.tabIndex = 0;
    tr.setAttribute("aria-expanded", ledOpen.has(key) ? "true" : "false");
    const td = (child, cls, title) => {
      const c = el("td", cls || "");
      if (typeof child === "string") c.textContent = child; else c.append(child);
      if (title) c.title = title;
      tr.append(c);
      return c;
    };
    const when = r.route_id ? el("button", "text led-route-link", ledTime(r.t)) : ledTime(r.t);
    if (r.route_id) {
      when.title = t("View routing");
      // the link opens the route, not the row's details as well
      when.onclick = (e) => { e.stopPropagation(); window.openRoute(r.route_id, r.t).catch((err) => status(err.message, "err")); };
    }
    td(when, "when", new Date(r.t).toLocaleString(locale === "zh" ? "zh-CN" : "en"));
    const who = el("span", "who");
    // an agent on another computer, whose magpie passed the request on
    const name = r.agentName || r.agent;
    // one made on another computer, whose usage sync brought (#542)
    const at = r.computerName ? t("{agent} · on {computer}", { agent: name, computer: r.computerName }) : name;
    who.append(icon(r.icon || "generic"), el("span", "", [r.via ? t("{agent} · via {host}", { agent: at, host: r.via }) : at, r.callerKeyLabel || r.callerKeyName].filter(Boolean).join(" · ")));
    td(who, "", [r.kind, r.session && t("session {id}", { id: r.session })].filter(Boolean).join(" · "));
    td(r.req || "—", "model" + (r.req ? "" : " faint"), r.req || t("Not kept for requests before this version"));
    const local = r.source === "log";
    const where = local ? (r.session_account || t("Local session")) : t(r.providerName) + (r.host ? " · " + r.host : "");
    const wc = td(el("div", "where-name", where), "where", where);
    const badges = el("div", "source-badges");
    if (local && r.session_account && r.session_official_login) {
      const badge = el("span", "src official", t("OFFICIAL"));
      badge.title = t("Official login confirmed for this account by local login metadata. This does not establish the route or authentication used for this request.");
      badges.append(badge);
    }
    let access = "";
    if (!local && r.access === "subscription") access = "Subscription";
    else if (!local && r.access === "api") access = "API";
    if (access) {
      const badge = el("span", "src access access-" + r.access, t(access));
      badge.title = t(access);
      badges.append(badge);
    }
    if (local && r.session_account) {
      const k = el("span", "src local", t("Local session"));
      k.title = t("Read from the agent's session file. The account is shown only when local metadata identifies it; no service provider is inferred.");
      badges.append(k);
    }
    if (badges.childElementCount) wc.append(badges);
    // the model sent is what the gateway sent the vendor: a session file has none
    // The log names a model, but does not capture the outbound HTTP request.
    td(r.model || "—", "model " + (r.model ? "model-value" : "faint"), r.source === "log" && r.model ? t("Model recorded in the local session log; the outbound HTTP request was not captured.") : r.model);
    td(ledServed(r), "model");
    td(r.effort || "—", r.effort ? "" : "faint");
    td(ledNum(r.in), "n");
    td(ledNum(r.out), "n", r.reasoning ? t("{n} reasoning, inside output", { n: ledNum(r.reasoning) }) : "");
    td(ledNum(r.cache_write), "n" + (r.cache_write ? "" : " faint"));
    td(ledNum(r.cache_read), "n" + (r.cache_read ? "" : " faint"));
    const cost = td(r.priced ? "≈" + fmtCost({ cost: r.cost, unpriced: 0 }) : "—", "n cost" + (r.priced ? "" : " faint"), r.priced ? (r.pricing_model ? t("API price reference") + ": " + r.pricing_model : "") : t("No known price for this model"));
    if (r.priced && r.pricing_model) {
      const reference = el("div", "price-reference", t("Price reference: {model}", { model: r.pricing_model }));
      reference.title = t("Model used for the API price estimate; this is not an observed model forwarding event.");
      cost.append(reference);
    }
    // a session file has the times of its lines: a call took about from the line that
    // asked for it to its last; some have none, and are dashes, not 0 ms
    const timed = r.source === "log";
    const untimed = !Number.isFinite(r.ms) || r.ms <= 0;
    const durationBand = untimed ? "" : r.ms <= 10000 ? "fast" : r.ms <= 30000 ? "slow" : "long";
    td(untimed ? "—" : (timed ? "≈" : "") + ledTook(r.ms), "n duration " + (untimed ? "faint" : "duration-" + durationBand),
      [!untimed ? t("{n} ms", { n: ledNum(r.ms) }) : "", !untimed ? t("Duration colors: ≤10 s green · 10–30 s amber · >30 s red") : "", timed && !untimed ? t("About: told from the session file's times, a little more or less than it took") : "", r.ttft_ms ? t("TTFT {ms}", { ms: ledTook(r.ttft_ms) }) : ""].filter(Boolean).join(" · "));
    const st = el("span", "st");
    // a status when the gateway logged the call; a session file has none,
    // and says only whether it went well
    let word = r.status ? String(r.status) : r.source === "log" ? (bad ? r.err_type || t("Failed") : t("Succeeded")) : "—";
    if (r.status && bad && r.err_type) word += " · " + r.err_type;
    st.append(el("i", "dot"), document.createTextNode(word));
    td(st, "", bad ? (r.err || t("Failed: the agent was answered {status}", { status: r.status })) : "");
    const detail = ledOpen.has(key) ? ledDetail(r, LED_COLS.length) : null;
    const toggle = () => {
      if (ledOpen.delete(key)) { tr.classList.remove("open"); tr.setAttribute("aria-expanded", "false"); tr.nextElementSibling?.classList.contains("led-detail") && tr.nextElementSibling.remove(); return; }
      ledOpen.add(key);
      tr.classList.add("open");
      tr.setAttribute("aria-expanded", "true");
      tr.after(ledDetail(r, LED_COLS.length));
    };
    // a click that ends a text selection is the reader copying, not asking
    tr.onclick = () => { if (!String(getSelection() || "")) toggle(); };
    tr.onkeydown = (e) => { if (e.target === tr && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); toggle(); } };
    table.tBodies[0].append(tr);
    if (detail) table.tBodies[0].append(detail);
  }
  wrap.replaceChildren(table);
  wrap.style.setProperty("--ledw", wrap.clientWidth + "px");

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
  $("#ledNote").textContent = t("Each request's tokens as its provider reported them; the cost estimated using effective model prices, including custom prices. Export CSV saves every page.");
}

{
  let typing = 0;
  $("#ledQ").oninput = (e) => {
    ledQuery = e.target.value;
    ledModel = "";
    clearTimeout(typing);
    typing = setTimeout(() => { ledOffset = 0; loadLedger().catch((err) => status(err.message, "err")); }, 250);
  };
  $("#ledQ").onkeydown = (e) => {
    if (e.key === "Escape" && e.target.value) { e.stopPropagation(); e.target.value = ""; ledQuery = ""; ledModel = ""; ledOffset = 0; loadLedger().catch((err) => status(err.message, "err")); }
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
let quotaFocus = ""; // pending provider/account card requested by the menu bar
let quotaFocusUntil = 0; // expire before a late quota response can move the reader
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
  const unpricedNote = tot.unpriced ? t("Not counted: {models}, with no known price", { models: [...unpriced].join(", ") }) : t("Estimated using effective model prices, including custom prices");
  if (c) {
    cost.append(el("b", "", "≈" + c), el("span", "", t("effective prices")));
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
    tile(c ? "≈" + c : "—", t("cost"), t("at effective prices"), unpricedNote);
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
    chart.fitCal = () => sessCalFit(chart, days, value, tip);
    return;
  }
  chart.fitCal = null;
  if (shape === "calendar") {
    chart.replaceChildren();
    chart.classList.add("cal");
    drawHead();
    const wrap = el("div", "sess-cal-wrap");
    wrap.append(sessCalendar(days, value, tip), sessCalSide(days, value, show));
    chart.append(wrap);
    chart.querySelector(".sess-chart-head").append(sessLegend());
    chart.fitCal = () => sessCalFit(chart, days, value, tip);
    chart.fitCal();
    sessCalWidth.observe(chart);
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
  fitChartLabels();

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

// a week's column in the calendar, cell and gap: as wide as weeks are counted
// by, and as wide as one may grow to take what is left over
const SESS_WEEK = 20, SESS_WEEK_MAX = 24;

// sessCalFit lays the calendar across its card (John on Discord: 这个活跃度怎么
// 没有铺满全部宽度呢？): a range of a few months took a few hundred pixels of a
// wide card and left the rest empty. The weeks before the range come in as
// empty cells, GitHub's way, up to a year in all, and the cells grow a little
// to take what is left; a narrow card gets none and its cells shrink.
const sessCalWidth = new ResizeObserver((es) => { for (const e of es) e.target.fitCal?.(); });
function sessCalFit(chart, days, value, tip) {
  const wrap = chart.querySelector(".sess-cal-wrap"), cal = wrap?.querySelector(".sess-cal");
  if (!cal || !wrap.clientWidth) return;
  const ws = getComputedStyle(wrap), side = wrap.querySelector(".sess-cal-side");
  const wd = Math.max(0, ...[...cal.querySelectorAll(".wd")].map((l) => l.getBoundingClientRect().width));
  const room = wrap.clientWidth - wd - (ws.flexDirection === "row" ? side.getBoundingClientRect().width + (parseFloat(ws.columnGap) || 0) : 0);
  const weeks = Math.ceil(((days[0].day.getDay() + 6) % 7 + days.length) / 7);
  const pad = Math.max(0, Math.min(53, Math.floor(room / SESS_WEEK)) - weeks);
  if (String(pad) !== cal.dataset.pad) cal.replaceWith(sessCalendar(days, value, tip, pad));
}

// sessCalendar is the range as weeks of days, Monday on top, each day as
// dark as it is busy among the others, after pad empty weeks before it
function sessCalendar(days, value, tip, pad = 0) {
  const lv = sessLevels(days.map(value));
  const lead = (days[0].day.getDay() + 6) % 7 + 7 * pad;
  const weeks = Math.ceil((lead + days.length) / 7);
  const cal = el("div", "sess-cal");
  cal.dataset.pad = pad;
  cal.style.gridTemplateColumns = `auto repeat(${weeks}, minmax(0, 1fr))`;
  cal.style.maxWidth = `calc(2.6em + ${weeks * (pad ? SESS_WEEK_MAX : SESS_WEEK)}px)`;
  const names = sessWeekdays();
  for (const i of [0, 2, 4]) {
    const l = el("span", "wd", names[i]);
    l.style.gridArea = `${i + 2} / 1`;
    cal.append(l);
  }
  const loc = locale === "zh" ? "zh-CN" : "en";
  let month = -1, labelAt = -9;
  // the days before the range, from the Monday it is laid out from: their
  // months named as the range's are, the days themselves left blank
  const before = Array.from({ length: pad ? lead : 0 }, (_, i) => {
    const d = new Date(days[0].day);
    d.setDate(d.getDate() - lead + i);
    return { day: d, before: true };
  });
  [...before, ...days].forEach((x, i) => {
    const k = (pad ? 0 : lead) + i, w = Math.floor(k / 7), wd = k % 7;
    if ((wd === 0 || i === 0) && x.day.getMonth() !== month) {
      month = x.day.getMonth();
      // a month mostly gone when the weeks before begin is left unnamed, so
      // as not to crowd out the next one's name
      if (w - labelAt >= 3 && !(x.before && x.day.getDate() > 14)) {
        const m = el("span", "mo", x.day.toLocaleDateString(loc, month === 0 && weeks > 20 ? { month: "short", year: "numeric" } : { month: "short" }));
        m.style.gridArea = `1 / ${w + 2}`;
        cal.append(m);
        labelAt = w;
      }
    }
    const c = x.before ? el("s") : el("i", "l" + lv(value(x)));
    c.style.gridArea = `${wd + 2} / ${w + 2}`;
    if (!x.before) c.title = tip(x, sessDay(x.day));
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
  const top = el("div", "sess-bars tools");
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
  line(t("Session ID"), s.id, copyBtn(s.id, t("Session id")));
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
// Cmd+, (Ctrl+, off the Mac) opens Settings, as it does in a Mac app
// (#670), from the window or the panel alike and from a text box too; in a
// browser tab it is the browser's.
if (!web) addEventListener("keydown", (e) => {
  const mod = /^Mac/.test(navigator.platform) ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  if (!mod || e.altKey || e.shiftKey || e.repeat || (e.key !== "," && e.code !== "Comma")) return;
  e.preventDefault();
  openSettings();
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
  if (westernUnits !== !!s.westernUnits) {
    westernUnits = !!s.westernUnits;
    if (applyPrefs.painted) { renderCosts(); if (mode === "panel" && panelTab === "stats") renderPanelUse(); }
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
  // the WebDAV setup can have been changed from outside the window (magpie
  // webdav at the terminal): the page's copy of it is dropped, so the page
  // is drawn from a fresh read. Coming back to the window is safe with a
  // form open: load() keeps off this path while one is, so it is never
  // rebuilt under whoever is typing in it
  syncView = null;
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
    if (on) thumbsUnderPicks($("#" + list));
  }
}
// a pill drawn while its card was hidden measured nothing: its thumb is
// put under the option picked, still, once the card is shown
function thumbsUnderPicks(box) {
  for (const th of box.querySelectorAll(".segs > .thumb")) {
    const opt = th.parentElement.querySelector(":scope > .on");
    if (!opt || !opt.offsetParent || parseFloat(th.style.width) === opt.offsetWidth) continue;
    th.classList.add("still");
    th.style.transform = `translateX(${opt.offsetLeft}px)`;
    th.style.width = opt.offsetWidth + "px";
    void th.offsetWidth;
    th.classList.remove("still");
  }
}
// A tab list's keys: the arrows, Home and End move along its shown tabs,
// and the tab reached is clicked, so the page is held as for a click
function tabKeys(tabs, sel) {
  tabs.onkeydown = (e) => {
    const shown = [...tabs.querySelectorAll(sel + ":not([hidden])")];
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
{
  const tabs = $("#warmTabs");
  tabs.onclick = (e) => {
    const b = e.target.closest("button[data-warm]");
    if (b) setWarmTab(b.dataset.warm, true);
  };
  tabKeys(tabs, "button[data-warm]");
}

// The Settings page is a page per part (#471: one long scroll of some 45
// rows): a tab for each over them, in the warm-ups' look, its rows alone
// under it. The part is kept in the address (?view=settings&tab=privacy) so
// a reload comes back to it, and remembered for the window opened again.
// A part with no row to show here (every one hidden on this system) has no
// tab. A click on a tab, like every click, leaves the page where it is: the
// tabs at the top stay where they were and the part comes in under them.
const SET_TABS = ["general", "usage", "network", "models", "privacy", "otel", "sync", "about"];
let setTab = "general";
try { const k = localStorage.getItem("magpie.settingsTab"); if (SET_TABS.includes(k)) setTab = k; } catch {}
let setShown = setTab; // the part shown: the one picked, or the first there
// A part has a tab while a row of it shows: one not hidden itself nor within
// it (a service's card under the warm-ups' tabs counts, picked or not). A
// part not drawn yet, with no rows at all, keeps its tab.
const partShows = (page) => {
  const rows = [...page.querySelectorAll(".row")];
  return !rows.length || rows.some((r) => {
    for (let n = r; n && n !== page; n = n.parentElement) if (n.hidden && !n.matches('[role="tabpanel"]')) return false;
    return true;
  });
};
function setSetTab(tab, remember) {
  if (remember) {
    setTab = tab;
    try { localStorage.setItem("magpie.settingsTab", tab); } catch {}
  }
  for (const id of SET_TABS) $("#setTab-" + id).hidden = !partShows($("#setPage-" + id));
  if (!SET_TABS.includes(tab) || $("#setTab-" + tab).hidden) tab = SET_TABS.find((id) => !$("#setTab-" + id).hidden) || "general";
  for (const id of SET_TABS) {
    const b = $("#setTab-" + id), on = id === tab;
    b.classList.toggle("on", on);
    b.setAttribute("aria-selected", String(on));
    b.tabIndex = on ? 0 : -1;
    $("#setPage-" + id).hidden = !on;
    if (on) thumbsUnderPicks($("#setPage-" + id));
  }
  setShown = tab;
  $("#setTabs").setAttribute("aria-label", t("Settings"));
  if (view === "settings") syncURL();
}
{
  const tabs = $("#setTabs");
  tabs.onclick = (e) => {
    const b = e.target.closest("button[data-set]");
    if (b) setSetTab(b.dataset.set, true);
  };
  tabKeys(tabs, "button[data-set]");
  // opened on a part (a reload, a link): that part, and remembered
  if (mode === "window" && params.get("view") === "settings" && SET_TABS.includes(params.get("tab"))) {
    setTab = params.get("tab");
    try { localStorage.setItem("magpie.settingsTab", setTab); } catch {}
  }
  setSetTab(setTab);
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
  // a closed window's webview let go (#580): the app's windows alone
  $("#lightweightRow").hidden = web;
  $("#lightweightSegs").replaceChildren(segs([["off", t("Off")], ["on", t("On")]], s.lightweight ? "on" : "off",
    (v) => savePrefs({ ...keep, lightweight: v === "on" })));
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
  renderGitHubToken(s);
  renderImages(s, keep);
  renderSearch(s, keep);
  renderRedact(s, keep);
  renderOTel(s, keep);
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
  // what changed in this version, and in one waiting; a build from source
  // has no notes
  if (/^v?\d+\.\d+\.\d+$/.test(s.version || "")) {
    const notes = el("button", "text", t("Open"));
    notes.onclick = async () => openWhatsNew(await api(updatePath("update")).catch(() => null), notes);
    row(t("What's new"), t("The release notes since the last update"), "", notes).classList.add("whatsnew-row");
  }
  // the header's Update pill, kept away for good or for one version; the
  // version row above still says what is out and offers it
  const pill = row(t("Update button"), t("Shows in the header when a newer magpie is out"),
    "", segs([["off", t("Off")], ["on", t("On")]], s.noUpdatePill ? "off" : "on", (v) => savePrefs({ ...keep, noUpdatePill: v === "off" }).then(renderUpdateBadge)));
  pill.classList.add("update-pill-row");
  if (s.updateSkip && !s.noUpdatePill) {
    api(updatePath("update")).then((u) => {
      if (!pill.isConnected || !u || u.latest !== s.updateSkip) return;
      pill.querySelector(".sub").textContent = t("Hidden for {v} until a newer version is out", { v: u.latest });
      const back = el("button", "text", t("Show again"));
      back.onclick = () => skipUpdate("");
      pill.querySelector(".val").prepend(back);
    }, () => {});
  }
  // whether magpie asks for a newer version (and downloads it) by itself,
  // and how often; off, only the version row's Check asks (#472)
  row(t("Automatic updates"), t("Checks for a newer magpie and downloads it"),
    "", segs([["off", t("Off")], ["on", t("On")]], s.noAutoUpdate ? "off" : "on", (v) => savePrefs({ ...keep, noAutoUpdate: v === "off" }))).classList.add("update-auto-row");
  // kept in place while off, dimmed, its height the same (a row or a line
  // taken away would shorten the page under the click), for when they are
  // turned on again
  row(t("Check every"), s.noAutoUpdate ? t("While automatic updates are on") : t("How often magpie looks for a newer version"), "",
    segs(UPDATE_EVERY.map((m) => [m, m < 60 ? t("{n} min", { n: m }) : t("{n} h", { n: m / 60 })]), s.updateEvery || 360,
      (updateEvery) => savePrefs({ ...keep, updateEvery }))).classList.add("update-every-row", ...(s.noAutoUpdate ? ["off"] : []));
  const open = el("button", "text", t("Open"));
  open.onclick = () => api("settings/reveal", {}).catch((e) => status(e.message, "err"));
  // portable (a data folder beside magpie, #508): everything is in there
  row(t("Config folder"), s.portable ? t("Portable: everything magpie keeps, in the data folder beside it") : t("providers, profiles and these settings"), s.dir, copyBtn(s.dir, t("Path")), open);
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
  // the parts' tabs, one gone whose rows are all hidden here
  setSetTab(setTab);
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
  // the app's own menu, not a native select
  select.dataset.value = chosen;
  select.replaceChildren(el("span", "", options.find((app) => app.id === chosen).name), svg(CHEV, 11, 1.6));
  select.onclick = (e) => {
    e.stopPropagation();
    if (select.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(select, options.map((app) => ({ v: app.id, name: app.name, note: "", literalName: true })), chosen, (v) => {
      if (v === chosen) return;
      // drawn at once; the save draws it again with what was kept
      select.dataset.value = v;
      select.firstChild.textContent = options.find((app) => app.id === v).name;
      savePrefs({ ...keep, sessionTerminal: v === "system" ? "" : v });
    }, "Session terminal", "sess-menu");
  };
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
  const parts = (ps) => ps.map((p) => t({ providers: "providers", settings: "settings", profiles: "profiles", agents: "agents' models", library: "library", usage: "usage" }[p])).join(t(", "));

  // WebDAV or S3
  // off: what it would keep the same, named from the view's toggles as the
  // CLI names them, so the page can't promise a part the setup leaves out.
  // A view that says nothing of the toggles (a read that failed) reads as
  // all on, as Status' off view is.
  const goes = ["settings", "profiles"];
  if (v.agents !== false) goes.push("agents");
  if (v.library !== false) goes.push("library");
  if (v.usage) goes.push("usage");
  let status = t("Keeps {parts} the same on every computer", {
    parts: t(v.keys === false ? "providers without their API keys" : "providers with their API keys") + t(", ") + parts(goes) });
  const s3 = v.on && v.kind === "s3";
  if (v.on) {
    const hostOf = (u) => { try { return new URL(u).host; } catch { return u; } };
    // a bucket is named by its address, and the server it is on unless AWS
    const host = s3 ? [v.url, v.endpoint && hostOf(v.endpoint.includes("://") ? v.endpoint : "https://" + v.endpoint)].filter(Boolean).join(" · ") : hostOf(v.url);
    status = v.error ? t("Couldn't sync: {error}", { error: v.error })
      : v.last ? t("Synced {when} · {host}", { when: syncWhen(v.last), host }) : t("Not synced yet · {host}", { host });
    // the other kind's server, kept from before sync moved here
    if (v.other) status += " · " + t("{kind} settings kept", { kind: v.other.kind === "s3" ? "S3" : "WebDAV" });
    if (!v.error && v.usageError) status += " · " + t("Couldn't share usage: {error}", { error: v.usageError });
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
// typed in the other is still there on going back. The kind not synced to
// shows the server kept from before sync moved from it (ARNO on Discord:
// trying S3 wiped the WebDAV setup); saving it moves sync back there.
function davForm(v) {
  const ed = el("div", "editor sync-form");
  const active = v.on ? (v.kind === "s3" ? "s3" : "webdav") : "";
  let kind = active || "webdav";
  const saved = t("saved · type a new one to replace it");
  const dav = v.kind === "s3" ? v.other || {} : v, bk = v.kind === "s3" ? v : v.other || {};
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
  // usage: off unless picked, each computer its own (#542)
  const [useL, use] = tick(t("Usage: this computer's calls, for the Usage page on the others"), !!v.usage);
  const what = el("div", "stack");
  what.append(keysL, agentsL, libL, useL);
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
  const names = { webdav: "WebDAV", s3: "S3" };
  // which one is synced to, and that the other's settings stay
  const to = field(t("Sync to"), segs([["webdav", "WebDAV"], ["s3", "S3"]].map(([id, n]) => [id, id === active ? t("{kind} · on", { kind: n }) : n]), kind, (k) => { kind = k; show(); }), " ");
  const where = to[1].querySelector(".hint");
  const save = el("button", "text primary", "");
  const show = () => {
    for (const x of davFields) x.hidden = kind !== "webdav";
    for (const x of s3Fields) x.hidden = kind !== "s3";
    const other = kind === "s3" ? "webdav" : "s3";
    where.textContent = !active ? ""
      : kind === active ? (v.other ? t("Syncing here now. The {other} settings are kept, not synced to: pick {other} to see them.", { other: names[other] }) : t("Syncing here now."))
      : t("{active} is synced to now. Saving moves sync here; the {active} settings are kept for moving back.", { active: names[active] });
    where.hidden = !active;
    save.textContent = !active ? t("Turn on") : kind === active ? t("Save") : t("Move sync to {kind}", { kind: names[kind] });
  };
  ed.append(...to,
    ...davFields, ...s3Fields,
    ...field(t("Passphrase"), phrase, t("The file is sealed with it on this computer; the server only ever sees it sealed. Keep it: without it the file can't be opened.")),
    ...field(t("Also sync"), what, t("Usage goes as a file a day: times, tokens, models and costs, never what was said. Turn it on on each computer that shares its own.")));
  show();
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
      const r = await api("davsync/save", { ...where, passphrase: phrase.value, keys: keys.checked, agents: agents.checked, library: lib.checked, ...(use.checked || v.usage ? { usage: use.checked } : {}) });
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
  // 万 and 亿 are Chinese's alone: in English a count is always K, M and B
  $("#unitsRow").hidden = locale !== "zh";
  $("#unitsSegs").replaceChildren(segs([[false, t("万 / 亿")], [true, t("K / M / B")]], !!s.westernUnits,
    (v) => savePrefs({ ...keep, westernUnits: v })));
  renderAlerts(s, keep);
  // the agents' lists name a model with its provider's after it, all but
  // the names the user gave (#92), or none (#335): set on its own, so the
  // agents are told
  const suffix = s.plainNames ? "off" : s.plainOwnNames ? "own" : "on";
  $("#plainNamesSegs").replaceChildren(segs([["off", t("Off")], ["own", t("Not on names I set")], ["on", t("On")]], suffix, (v) =>
    writingPrefs(api("settings/plain-names", { mode: v })).then((ns) => { prefs = ns; renderSettings(); }).catch((e) => { status(t(e.message), "err"); renderSettings(); })));
  // Codex's OpenAI models in multi-agent V1, so their subagents can run on
  // magpie's other models (#141)
  $("#codexAgentsV1Segs").replaceChildren(segs([["off", t("Off")], ["on", t("On")]], s.codexAgentsV1 ? "on" : "off", (v) =>
    writingPrefs(api("settings/codex-agents-v1", { on: v === "on" })).then((ns) => { prefs = ns; renderSettings(); }).catch((e) => { status(t(e.message), "err"); renderSettings(); })));
  const rate = s.fx?.rate;
  const currencySub = $("#currencySub");
  currencySub.textContent = t("What a cost — the Usage page's, the tray panel's, the TUI's and the CLI's — is shown as; a vendor's own balance, already in its own currency, is never converted");
  currencySub.title = rate ? t("1 USD = {rate} CNY{when}", { rate: rate.toFixed(2), when: s.fx.at ? " · " + (s.fx.stale ? t("last fetched {when}", { when: syncWhen(s.fx.at) }) : t("fetched {when}", { when: syncWhen(s.fx.at) })) : "" }) : "";
  $("#trayUsageRow").hidden = web;
  if (web) return;
  const mac = document.body.classList.contains("mac");
  $("#trayUsageSub").textContent = mac ? t("Show your subscriptions' allowances beside magpie's icon in the menu bar, side by side, refreshed every few minutes")
    : t("Show your subscriptions' allowances when pointing at magpie's tray icon, refreshed every few minutes");
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
  // only the Mac's menu bar draws the cards, with their logos or without
  $("#trayLogosRow").hidden = !ids.length || !mac;
  $("#trayLogosSegs").replaceChildren(segs([["off", t("Off")], ["on", t("On")]], s.trayNoLogos ? "off" : "on",
    (v) => savePrefs({ ...keep, trayNoLogos: v === "off" })));
}
const TRAY_EVERY = [1, 3, 5, 10, 30];
// how often magpie checks for updates by itself, in minutes (settings.UpdateEveries)
const UPDATE_EVERY = [30, 60, 360, 1440];

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

// renderGitHubToken: the GitHub token the library's requests to GitHub's
// API carry (checking skills for updates), which raises GitHub's limit from
// 60 requests an hour to 5,000. The page is told a masked one only, and
// whether it is the one set here or GITHUB_TOKEN / GH_TOKEN's.
let githubTokenErr = "", githubTokenDraft = "";
function renderGitHubToken(s) {
  const box = $("#githubList");
  box.replaceChildren();
  const r = el("div", "row pref github-token");
  const who = el("div", "who");
  const sub = el("div", "sub");
  who.append(el("div", "name", t("GitHub token")), sub);
  const val = el("div", "val");
  r.append(who, val);
  box.append(r);
  const set = (token) => writingPrefs(api("settings/github-token", { token }))
    .then((ns) => { prefs = ns; githubTokenErr = githubTokenDraft = ""; renderSettings(); status(t("Saved"), "ok", 1500); })
    .catch((e) => { githubTokenErr = t(e.message); status(t(e.message), "err"); renderSettings(); });
  const why = t("The library checks skills for updates with it: GitHub allows 5,000 requests an hour with a token, 60 without. It needs no scopes.");
  if (s.githubTokenFrom === "settings") {
    sub.textContent = sub.title = why;
    const x = el("button", "text", t("Remove"));
    x.onclick = () => set("");
    val.append(el("code", "", s.githubTokenMask || ""), x);
    return;
  }
  sub.textContent = githubTokenErr || (s.githubTokenFrom
    ? t("Using {env} from the environment ({mask}); a token set here comes first.", { env: s.githubTokenFrom, mask: s.githubTokenMask || "" }) + " " + why
    : why);
  if (githubTokenErr) sub.classList.add("err");
  sub.title = sub.textContent;
  const i = input(githubTokenDraft, "ghp_… / github_pat_…", "password");
  i.className = "words github-token-input";
  i.setAttribute("aria-label", t("GitHub token"));
  const save = el("button", "text", t("Save"));
  save.onclick = () => { const v = i.value.trim(); if (!v) return i.focus(); set(v); };
  i.oninput = () => { githubTokenDraft = i.value; };
  i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save.onclick(); };
  const get = el("button", "link", t("Create one ↗"));
  get.onclick = () => api("open", { url: "https://github.com/settings/tokens/new?description=magpie" }).catch(() => {});
  val.append(i, get, save);
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

// renderAlerts draws the usage alerts (#368): a notification when a window
// that routing counts reaches the share set, or a balance falls to the
// amount set, each once; Off, or the number in a field beside On.
function renderAlerts(s, keep) {
  const problem = s.notifyProblem === "denied" ? t("Notifications are turned off for magpie in the system's settings")
    : s.notifyProblem === "unavailable" ? t("Notifications can't be shown on this system") : "";
  const sub = (id, what) => {
    const box = $(id);
    box.textContent = what;
    box.title = what;
    // the problem said whole, the line let wrap for it
    box.classList.toggle("wraps", !!problem);
    if (problem) box.append(" · ", el("span", "warn", problem));
  };
  sub("#usageAlertSub", t("A notification when a 5-hour, weekly or monthly window reaches this share used, once each time it runs"));
  sub("#balanceAlertSub", t("A notification when a balance falls to this amount, in its own currency or credits, once until it is topped up"));
  const field = (box, value, label, unit, ok, save) => {
    box.replaceChildren();
    if (value) {
      const i = input(String(value), "", "number");
      i.className = "at num";
      i.inputMode = "decimal";
      i.setAttribute("aria-label", label);
      i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") i.blur(); };
      i.onchange = () => {
        const n = Number(i.value);
        if (!ok(n)) { i.value = String(value); return; }
        if (n !== value) save(n);
      };
      box.append(i);
      if (unit) box.append(el("span", "unit", unit));
    }
    return box;
  };
  field($("#usageAlertSegs"), s.usageAlert || 0, t("Share used"), "%", (n) => Number.isInteger(n) && n >= 1 && n <= 100,
    (n) => savePrefs({ ...keep, usageAlert: n }))
    .append(segs([["off", t("Off")], ["on", t("On")]], s.usageAlert ? "on" : "off", (v) => savePrefs({ ...keep, usageAlert: v === "on" ? s.usageAlert || 80 : 0 })));
  field($("#balanceAlertSegs"), s.balanceAlert || 0, t("Amount"), "", (n) => Number.isFinite(n) && n > 0,
    (n) => savePrefs({ ...keep, balanceAlert: n }))
    .append(segs([["off", t("Off")], ["on", t("On")]], s.balanceAlert ? "on" : "off", (v) => savePrefs({ ...keep, balanceAlert: v === "on" ? s.balanceAlert || 5 : 0 })));
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
    v === "off" ? t("Agents given Magpie Image can't generate images or videos: the tool says it is off")
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

// renderSearch: the web search APIs a model's search goes to when no
// provider can search (#419) — one row each, in the order they are tried,
// and a row to add one: which API, its key, and the address of one the user
// runs (SearXNG). They are set on their own; one magpie refuses is said in
// the row, what was typed kept.
let searchDraft = { vendor: "tavily", key: "", url: "", err: "" };
function renderSearch(s, keep) {
  const box = $("#searchList");
  box.replaceChildren();
  renderSearcher(s, keep, box);
  const row = (name, sub, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name), el("div", "sub", sub));
    const val = el("div", "val");
    val.append(...tools);
    r.append(who, val);
    box.append(r);
    return r;
  };
  const set = (body, done) => writingPrefs(api("settings/search-api", body))
    .then((ns) => { prefs = ns; searchDraft.err = ""; done?.(); renderSettings(); status(t("Saved"), "ok", 1500); })
    .catch((e) => { searchDraft.err = e.message; status(e.message, "err"); renderSettings(); });
  const vendors = s.searchVendors || [];
  const d = searchDraft;
  if (!vendors.some((v) => v.id === d.vendor)) d.vendor = vendors[0]?.id || "";
  const vendor = () => vendors.find((v) => v.id === d.vendor) || {};
  const pick = el("button", "proto pick search-vendor");
  pick.type = "button";
  pick.setAttribute("aria-label", t("Search API"));
  const key = input(d.key, t("API key"), "password");
  const url = input(d.url, "https://searx.example.com");
  key.className = "words search-key";
  url.className = "words search-url";
  key.setAttribute("aria-label", t("API key"));
  url.setAttribute("aria-label", t("Address"));
  const get = el("button", "link", t("Get a key ↗"));
  get.onclick = () => vendor().keysURL && api("open", { url: vendor().keysURL });
  const draw = () => {
    const v = vendor();
    pick.replaceChildren(el("span", "", v.name || ""), svg(CHEV, 11, 1.6));
    url.hidden = !v.needURL;
    key.placeholder = v.needURL ? t("API key, if it needs one") : t("API key");
    get.hidden = !v.keysURL;
  };
  pick.onclick = (e) => {
    e.stopPropagation();
    if (pick.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(pick, vendors.map((v) => ({ v: v.id, name: v.name, note: v.needURL ? t("your own") : "" })), d.vendor,
      (id) => { d.vendor = id; draw(); }, "Search API");
  };
  const add = el("button", "text", t("Add"));
  add.onclick = () => {
    d.key = key.value.trim(); d.url = url.value.trim();
    if (vendor().needURL ? !d.url : !d.key) return (vendor().needURL ? url : key).focus();
    set({ vendor: d.vendor, key: d.key, url: vendor().needURL ? d.url : "" },
      () => { searchDraft = { vendor: d.vendor, key: "", url: "", err: "" }; });
  };
  for (const i of [key, url]) {
    i.oninput = () => { d.key = key.value; d.url = url.value; };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") add.onclick(); };
  }
  draw();
  const by = s.searchProvider ? t("Now done by {who}; these come after it", { who: s.searchProvider })
    : t("No provider can search, so these are asked");
  const head = row(t("Search APIs"), d.err || t("When a model can't search the web, magpie searches for it with these, in this order, and gives it what they found") + " · " + by,
    pick, key, url, get, add);
  head.classList.add("rule-row", "search-add");
  head.querySelector(".val").classList.add("rule-add");
  if (d.err) head.querySelector(".sub").classList.add("err");
  (s.searchAPIs || []).forEach((a, n) => {
    const x = el("button", "text", t("Remove"));
    x.onclick = () => set({ vendor: a.vendor, remove: true });
    const what = [a.key || (a.ready ? "" : t("needs its key")), a.url].filter(Boolean).join(" · ");
    row(`${n + 1}. ${a.name}`, what, x).classList.add("search-api");
  });
}

// renderSearcher: the provider that searches the web for a model that
// can't — the one magpie picks, or one named, by itself (its small model)
// or with a model of it. One named that is gone, off or can't search gives
// way to magpie's pick, which the row says. Relays said to search can be
// named, but are never picked automatically (#359), and the row says so.
function renderSearcher(s, keep, box) {
  const choices = s.searchChoices || [];
  const v = s.searcher || "";
  const named = (id) => {
    const [pid, ...rest] = id.split("/");
    const c = choices.find((x) => x.id === pid);
    if (!c) return id;
    if (!rest.length) return `${c.name} · ${c.small}`;
    const m = c.models.find((x) => x.id === id);
    return `${m ? m.name : rest.join("/")} · ${c.name}`;
  };
  const icOf = (id) => choices.find((x) => x.id === id.split("/")[0])?.icon;
  const r = el("div", "row pref searcher-row");
  const who = el("div", "who");
  const sub = el("div", "sub", t("When a model can't search the web, this provider searches for it, and gives it what it found"));
  if (v && s.searchUnused) {
    const why = { gone: t("it is no longer in magpie"), off: t("it is turned off"), cant: t("it can't search the web by itself"), nomodel: t("it lists no model") }[s.searchUnused] || s.searchUnused;
    sub.append(" · ", el("span", "warn searcher-unused", t("{who} isn't used: {why}, so magpie picks one", { who: named(v), why })));
  }
  if (s.searchRelays?.length) sub.append(" · ", el("span", "searcher-relays",
    t("Relays said to search ({names}) are never picked automatically: they would spend the relay's quota on other models' searches; if one refuses magpie's own request, magpie falls back", { names: s.searchRelays.join(", ") })));
  who.append(el("div", "name", t("Searches for other models")), sub);
  const b = el("button", "rt-cond on searcher-pick");
  b.type = "button";
  b.setAttribute("aria-label", t("Searches for other models"));
  if (v && !s.searchUnused) b.append(icon(icOf(v) || "generic"), el("span", "", named(v)));
  else {
    if (s.searchAuto) b.append(icon(choices[0]?.icon || "generic"));
    b.append(el("span", "", t("Automatic") + " · " + (s.searchAuto || t("no provider that searches"))));
  }
  const options = [{ value: "", label: t("Automatic"), note: s.searchAuto || t("no provider that searches"), reset: true }];
  for (const c of choices) {
    options.push({ value: c.id, label: t("{model}, its small model", { model: c.small }), note: c.name, icon: c.icon, group: c.name });
    for (const m of c.models) options.push({ value: m.id, label: m.name || m.id, note: c.name, icon: c.icon, group: c.name, ref: m.id });
  }
  b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "searcher", label: "model", value: v, options,
    onPick: (id) => { if (id !== v) savePrefs({ ...keep, searcher: id }); } }, b, ev);
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

function renderOTel(s, keep) {
  const box = $("#otelList");
  box.replaceChildren();
  let config = { ...(s.otel || {}) };
  const section = (name) => box.append(el("div", "otel-section", t(name)));
  const row = (id, name, sub, control) => {
    const r = el("div", "row pref");
    r.id = id;
    const who = el("div", "who");
    who.append(el("div", "name", t(name)), el("div", "sub", t(sub)));
    const val = el("div", "val");
    val.append(control);
    r.append(who, val);
    if (id === "otelEndpointRow" || id === "otelHeadersRow" || id === "otelBodiesRow") r.classList.add("otel-stacked");
    box.append(r);
  };
  const save = (change) => {
    config = { ...config, ...change };
    savePrefs({ ...keep, otel: { ...config } });
  };
  section("Collector connection");
  row("otelExportRow", "OTLP export", "Send traces to your collector. Choose what to include below; account credentials stay local",
    segs([["off", t("Off")], ["on", t("On")]], config.enabled ? "on" : "off", (v) => save({ enabled: v === "on" })));
  const endpoint = input(config.endpoint || "", "http://localhost:4318", "url");
  endpoint.className = "words";
  endpoint.setAttribute("aria-label", t("OTLP endpoint"));
  endpoint.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") endpoint.blur(); };
  endpoint.onchange = () => save({ endpoint: endpoint.value.trim().replace(/\/+$/, "") });
  row("otelEndpointRow", "OTLP endpoint", "Base URL of your collector, or Langfuse's /api/public/otel endpoint", endpoint);
  const headers = input(Object.entries(config.headers || {}).map(([k, v]) => `${k}=${encodeURIComponent(v)}`).join(","), "Authorization=Bearer%20token", "password");
  headers.className = "words";
  headers.setAttribute("aria-label", t("OTLP headers"));
  headers.autocomplete = "off";
  headers.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") headers.blur(); };
  headers.onchange = () => {
    const values = {};
    try {
      for (const part of headers.value.split(",").filter((p) => p.trim())) {
        const i = part.indexOf("=");
        if (i < 1) throw new Error(t("Use comma-separated name=value headers"));
        values[part.slice(0, i).trim()] = decodeURIComponent(part.slice(i + 1).trim());
      }
      save({ headers: values });
    } catch (e) { toast(e.message, true); }
  };
  row("otelHeadersRow", "OTLP headers", "Comma-separated name=value pairs; percent-encode spaces and commas in values", headers);
  section("Export scope");
  row("otelMetricsRow", "Export metrics", "Also send duration and token histograms. Leave off for a traces-only service such as Langfuse",
    segs([["off", t("Off")], ["on", t("On")]], config.metrics ? "on" : "off", (v) => save({ metrics: v === "on" })));
  row("otelSessionsRow", "Trace agent conversations", "Reads all local sessions from Codex, Pi, Oh My Pi, Claude Code, Cowork, OpenCode and Gemini CLI, including calls not routed through Magpie. When content is included below, exports prompts, replies, tool arguments and tool output (including file contents and command output), with secrets masked",
    segs([["off", t("Off")], ["on", t("On")]], config.sessions ? "on" : "off", (v) => save({ sessions: v === "on" })));
  section("Trace content");
  const mode = !config.bodies ? "metadata" : config.bodiesWhole ? "full" : "limited";
  const content = segs([["metadata", t("Metadata only")], ["limited", t("Up to 256 KB")], ["full", t("Full content")]], mode,
    (v) => {
      for (const button of content.querySelectorAll("button")) button.setAttribute("aria-pressed", String(button.classList.contains("on")));
      save({ bodies: v !== "metadata", bodiesWhole: v === "full" });
    });
  content.setAttribute("role", "group");
  content.setAttribute("aria-label", t("Request and response content"));
  for (const [i, button] of [...content.querySelectorAll("button")].entries()) {
    button.setAttribute("aria-pressed", String(["metadata", "limited", "full"][i] === mode));
  }
  const descriptions = {
    metadata: "Send model, token, status and timing metadata only. Prompts, replies and tool content stay local",
    limited: "Include requests and replies as trace input and output, with secrets masked. Each body is limited to 256 KB",
    full: "Include complete requests and replies, with secrets masked. Large content may be refused by your collector",
  };
  row("otelBodiesRow", "Request and response content", descriptions[mode], content);
  if (s.otelEnv) box.append(el("div", "sub otel-env", t("Environment variables override these saved OTLP preferences")));
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
// machines. Gateway keys and connection examples live together in Gateway.
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
  row(t("Share on local network"), t("Agents on other computers on this network can use magpie’s models with a gateway key from Gateway"), "",
    segs([["off", t("Off")], ["on", t("On")]], s.lan ? "on" : "off", (v) => set({ on: v === "on" })));
  if (!s.lan) return;
  let urls = s.lanURLs || [], sub = "";
  // in a container magpie finds only the container's own addresses; the
  // page opened over the network was reached at the host's, so the
  // gateway is offered there, on its port
  if (s.lanContainer) {
    const host = location.hostname, port = urls.length ? new URL(urls[0]).port : "";
    if (web && host && port && !/^(localhost|127\.|\[::1\]$)/.test(host)) {
      urls = ["http://" + host + ":" + port]; // an IPv6 hostname comes bracketed
      sub = t("Where this page was opened, on the gateway’s port; MAGPIE_PUBLIC_URL sets another");
    } else sub = t("The container’s own addresses, which other devices can’t reach: set MAGPIE_PUBLIC_URL to the host’s");
  }
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
  if (sub) row(t("In a container"), sub, "").classList.add("lan-container");
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
    u = await api(updatePath("update")).catch(() => null);
    if (!u || !r.isConnected || (updateSeq.get(r) || 0) !== seq) return;
  } else if (!r.isConnected) return;
  const who = r.querySelector(".who"), val = r.querySelector(".val");
  const sub = who.querySelector(".sub") || who.appendChild(el("div", "sub"));
  sub.title = "";
  sub.classList.remove("wraps");
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
    try { next = await api(updatePath("update/check"), {}); }
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
    case "ready": {
      sub.textContent = t("{v} is downloaded", { v: u.latest }) + (u.error ? " · " + u.error : "");
      // a failed install says why in full, and offers the release page to
      // put the new version in by hand
      sub.title = u.error || "";
      if (u.error) sub.classList.add("wraps");
      // back with an answer only when it didn't restart: a busy gateway
      // has the restart wait for it to be idle (#577)
      const install = async (when) => {
        const a = await api("update/install", { ...installFrom(), ...(when ? { when } : {}) }).catch(() => ({ state: "error" }));
        if (a) return renderUpdate(r, a.current ? a : undefined);
        sub.textContent = t("Restarting…");
        backAsNew(u.current);
      };
      const busyNow = updateBusyText(u.busy);
      if (u.waiting) {
        sub.textContent += " · " + t("restarts once the gateway is idle") + (busyNow ? " · " + busyNow : "");
        btn(t("Restart now"), () => install("now"));
        btn(t("Cancel"), () => install("cancel"));
        // the wait ends in a restart: a read that finds no one is that
        const seen = updateSeq.get(r) || 0;
        setTimeout(async () => {
          if (!r.isConnected || (updateSeq.get(r) || 0) !== seen) return;
          const n = await api(updatePath("update")).catch(() => null);
          if (!r.isConnected || (updateSeq.get(r) || 0) !== seen) return;
          if (!n) {
            sub.textContent = t("Restarting…");
            for (const b of val.querySelectorAll("button")) b.remove();
            return backAsNew(u.current);
          }
          renderUpdate(r, n);
        }, 1000);
        break;
      }
      if (u.gaveUp) sub.textContent += " · " + t("the gateway stayed busy for an hour, so magpie didn't restart; it updates when you restart or quit it");
      else if (busyNow) sub.textContent += " · " + busyNow;
      if (u.gaveUp) sub.classList.add("wraps");
      btn(t("Restart to update"), () => install());
      if (u.error && u.url) btn(t("Download"), () => (web ? window.open(u.url, "_blank", "noopener") : api("open", { url: u.url })));
      break;
    }
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
      if (u.error) sub.classList.add("wraps");
      btn(t("Check"), check);
      break;
    case "": // not asked yet: with automatic updates off, only this asks
      sub.textContent = prefs && prefs.noAutoUpdate ? t("Automatic updates are off") : "";
      btn(t("Check"), check);
      break;
    default: // built from source
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
  return { theme: s.theme, lang: s.lang, tray: s.tray, dock: !!s.dock, dockWindow: !!s.dockWindow, lightweight: !!s.lightweight, proxy: s.proxy || "",
    sessionTerminal: s.sessionTerminal || "",
    otel: s.otel || {},
    trayUsages: s.trayUsages || [],
    redact: !!s.redact, redactPersonal: !!s.redactPersonal, redactWords: s.redactWords || [], codexWarmup: s.codexWarmup || "",
    claudeWarmup: s.claudeWarmup || "", codexWarmAt: s.codexWarmAt || "", claudeWarmAt: s.claudeWarmAt || "", workbuddyCheckin: !!s.workbuddyCheckin, noStats: !!s.noStats,
    noUpdatePill: !!s.noUpdatePill, noAutoUpdate: !!s.noAutoUpdate, updateEvery: s.updateEvery || 360,
    trayUsage: s.trayUsage || "", trayUsageEvery: s.trayUsageEvery || 3, trayNoLogos: !!s.trayNoLogos, vision: s.vision || "", imageGen: s.imageGen || "", searcher: s.searcher || "", currency: s.currency || "usd",
    westernUnits: !!s.westernUnits, usageAlert: s.usageAlert || 0, balanceAlert: s.balanceAlert || 0 };
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
// A new purposeful scroll supersedes any still-pending quota navigation.
const readerScrolls = (ms) => { quotaFocus = ""; purposeUntil = Math.max(purposeUntil, performance.now() + ms); held = null; };
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
// A flick goes on scrolling after the finger is lifted, with no touch event
// to say so: each of its scrolls keeps the next one the reader's, till it
// comes to rest. Put back, a phone's page jerked to and fro under the
// finger's flick (jiakun_zhao on X: 滚动会抽搐).
let flingUntil = 0;
const flings = () => { flingUntil = performance.now() + 250; readerScrolls(250); };
addEventListener("touchend", flings, { capture: true, passive: true });
addEventListener("scroll", () => { if (performance.now() < flingUntil) flings(); }, { capture: true, passive: true });
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
// Where what's in the view ends, its padding at the foot included, the room
// aside. Not scrollHeight less the room: scrollHeight is never less than the
// view is tall, so under a list shorter than the view (the agents rolled up
// in a tall window) it counted the empty foot as content, the room made was
// too short to hold the list, and each frame's hold and fit took turns
// adding and taking it away, the list swinging between two places (#355).
function contentEnd(v) {
  const top = v.getBoundingClientRect().top - v.scrollTop;
  let end = 0;
  for (const c of v.children) {
    if (c.classList.contains("view-room") || !c.getClientRects().length) continue;
    end = Math.max(end, c.getBoundingClientRect().bottom + (parseFloat(getComputedStyle(c).marginBottom) || 0) - top);
  }
  return end + (parseFloat(getComputedStyle(v).paddingBottom) || 0);
}
// only as much room as keeps the view where it is: none once the content
// reaches the view's foot again, or the view is back at its top
function fitRoom(v) {
  const r = roomOf(v);
  if (r) setRoom(v, v.scrollTop < 1 ? 0 : Math.min(r, v.scrollTop + v.clientHeight - contentEnd(v)));
}
// A view drawn again whole (the Library's page) has none of what was clicked
// left, nor its parents: what's held is then what is now where it was, of
// the same kind (#458)
const pathIn = (v, n) => { const p = []; for (; n !== v; n = n.parentElement) p.unshift(Array.prototype.indexOf.call(n.parentElement.children, n)); return p; };
const atPath = (v, p) => p.reduce((n, i) => n?.children[i], v);
function standIn(h) {
  for (const c of h.chain) {
    const s = atPath(h.v, c[2]);
    if (s && s !== c[0] && s.tagName === c[0].tagName && s.classList[0] === c[0].classList[0] && atRest(s)) { c[0] = s; return c; }
  }
}
function hold(h) {
  const a = h.chain.find(([n]) => atRest(n)) || (h.chain.some(([n]) => n.isConnected) ? null : standIn(h));
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
    if (want > max) setRoom(v, want + v.clientHeight - contentEnd(v));
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
  purposeUntil = flingUntil = 0; // what came before the click (Space pressed on a button, a tremble, the lift of a tap) is no scroll
  const v = e.target.closest?.(".view");
  if (!v || v.hidden) { held = null; return; }
  // a click before a frame has held the one before it (a tab list's keys
  // pressed in quick turn) holds that one first: the page it shrank is put
  // back, so this one is taken where the reader left it, not at the top
  if (held?.v === v) hold(held);
  const chain = [];
  const from = e.target.closest?.("[data-unrolls]")?.parentElement || e.target;
  for (let n = from; n && n !== v; n = n.parentElement) {
    for (const m of [n, n.previousElementSibling, n.nextElementSibling]) if (m instanceof HTMLElement && m.offsetParent) chain.push([m, onScreen(m, v), pathIn(v, m)]);
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
  if (mode === "window") { for (const b of $("#nav").querySelectorAll("button")) b.classList.toggle("on", b.dataset.view === v); slide($("#nav"), "nav"); navInSight(); }
  $("#prefs").classList.toggle("on", v === "settings");
  for (const id of ["agents", "providers", "gateway", "routing", "usage", "sessions", "analytics", "library", "plugins", "settings"]) $("#view-" + id).hidden = v !== id;
  // back to where the reader was in it, and again once it has what it loads
  const back = () => backToReader($("#view-" + v));
  requestAnimationFrame(back);
  closePicker();
  closeAgentModels();
  if (v !== "providers" && editing !== null) cancelEdit(true);
  if (v === "gateway") loadGatewayKeys();
  if (v === "providers" || v === "gateway" || v === "routing") loadProviders().then(back, (e) => status(e.message, "err"));
  if (v === "usage") loadUsage(true).then(back, (e) => status(e.message, "err"));
  if (v === "settings") loadSettings().then(back, (e) => status(e.message, "err"));
  if (v === "library") window.loadLibrary?.()?.then(back);
  if (v === "plugins") window.loadPlugins?.()?.then(back);
  if (v === "sessions") window.loadSessionsPage?.()?.then(back);
  if (v === "analytics") {
    const p = typeof period === "string" ? period : "30d";
    window.loadAnalytics?.(p)?.then?.(back) || back();
  }
  syncURL();
}

// The tab, and the provider open in it, are kept in the address so a
// reload comes back to them.
function syncURL() {
  if (mode !== "window") return;
  const q = new URLSearchParams(location.search);
  if (view === "agents") q.delete("view"); else q.set("view", view);
  if (view === "providers" && typeof editing === "string") q.set("edit", editing); else q.delete("edit");
  if (view === "settings") q.set("tab", setShown); else q.delete("tab");
  const s = q.size ? "?" + q : location.pathname;
  if (s !== location.search) history.replaceState(null, "", s);
}
if (mode === "window") for (const b of $("#nav").querySelectorAll("button")) b.onclick = () => { show(b.dataset.view); b.blur(); };
// openSettings: the Settings page, in the window (the panel opens it there)
function openSettings() {
  if (mode !== "window") { api("window/main?view=settings", {}); return; }
  // a pick list or a menu open over the page is put away with it
  closeProtoMenu();
  closeAgentMenu();
  show("settings");
}
$("#prefs").onclick = () => { openSettings(); $("#prefs").blur(); };
$("#openAnalytics")?.addEventListener("click", () => { show("analytics"); $("#openAnalytics")?.blur(); });

$("#sync").onclick = async () => {
  const b = $("#sync");
  if (b.classList.contains("spin")) return;
  b.classList.add("spin");
  // a newer magpie is looked for too: the Update pill beside it shows once
  // it's in (inaction on Discord looked for it here, not in Settings)
  api(updatePath("update/check"), {}).then(renderUpdateBadge, () => {});
  try {
    state = await api("sync", {});
    renderAgents();
    if (providers) await loadProviders();
    status(t("Model lists refreshed"), "ok");
  } catch (e) {
    status(t("Sync failed: {e}", { e: e.message }), "err");
  } finally {
    // stop at the end of a turn, not wherever the reply caught it (#16).
    // The turn itself is told to be the last, so it ends on the compositor's
    // clock: waiting for animationiteration and then dropping the class
    // jumped the icon back by however far it had turned while the page was
    // busy drawing the reply — the twitch at the end of every refresh.
    const spin = b.querySelector("svg").getAnimations()[0];
    const stop = () => b.classList.remove("spin");
    if (spin?.effect?.updateTiming) {
      spin.effect.updateTiming({ iterations: (spin.effect.getComputedTiming().currentIteration || 0) + 1 });
      spin.finished.then(stop, stop);
    } else stop();
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

// magpie web on a phone, or a browser as narrow: the header is two rows,
// the magpie and the icons over the tabs, which scroll sideways when they
// don't fit, the one open kept in sight
function phoneWeb() { return document.body.classList.contains("web") && matchMedia("(max-width: 760px), (pointer: coarse) and (max-width: 1024px)").matches; }
function navInSight() {
  const nav = $("#nav"), on = nav?.querySelector("button.on");
  if (!phoneWeb() || !on || nav.scrollWidth <= nav.clientWidth) return;
  const l = on.offsetLeft - 16, r = on.offsetLeft + on.offsetWidth + 16 - nav.clientWidth;
  if (nav.scrollLeft > l) nav.scrollLeft = l;
  else if (nav.scrollLeft < r) nav.scrollLeft = r;
}
matchMedia("(max-width: 760px), (pointer: coarse) and (max-width: 1024px)").addEventListener?.("change", () => { fitTop(); navInSight(); });

// A narrow window has no room for the whole header: the name goes, leaving
// the magpie, and Update becomes its arrow; narrower still, the tabs stop
// centring and take the room between, and at the narrowest they draw in,
// further still when they don't fit (the 560px window at 150%).
function fitTop() {
  const top = $(".top"), nav = $("#nav"), brand = $(".brand"), actions = $(".actions");
  // a phone's browser: the tabs have a row of their own (app.css), at their size
  if (phoneWeb()) { top.classList.remove("tight", "cramped", "inrow", "crowded", "packed"); return; }
  const fits = () => {
    const a = actions.getBoundingClientRect();
    // the buttons sit against the padding; under a page zoom (the text size)
    // their edge can measure a hair past it, which isn't running off (#457)
    if (a.right > top.getBoundingClientRect().right - parseFloat(getComputedStyle(top).paddingRight) + 0.5) return false;
    const left = brand.offsetParent ? brand.getBoundingClientRect().right
      : top.getBoundingClientRect().left + parseFloat(getComputedStyle(top).paddingLeft);
    if (!nav?.offsetParent) return left + 8 <= a.left;
    const n = nav.getBoundingClientRect();
    return left + 8 <= n.left && n.right + 8 <= a.left;
  };
  top.classList.remove("tight", "cramped", "inrow", "crowded", "packed");
  if (fits()) return;
  top.classList.add("tight");
  if (fits()) return;
  // the tabs closer together are tried in the middle first (#442)
  top.classList.add("cramped");
  if (fits()) return;
  top.classList.add("inrow");
  if (fits()) return;
  top.classList.add("crowded");
  if (!fits()) top.classList.add("packed");
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
// The Usage page reads its numbers again while it is looked at, as often as
// the reader says — off, or every 5 s to a minute (a request through the gateway
// shows within that; the subscriptions' windows each minute at most, the vendors'
// answers being cached behind them, and the sessions each 15 s) — redrawn only
// on a change. The button beside it reads them now.
const USAGE_EVERY = [[0, "Off"], [5, "5 s"], [10, "10 s"], [30, "30 s"], [60, "1 min"]];
let usageEvery = 5; // seconds; 0 is off
try {
  const v = localStorage.getItem("magpie.usageEvery");
  if (USAGE_EVERY.some(([n]) => String(n) === v)) usageEvery = Number(v);
} catch {}
let usageLast = 0, usageReadAt = 0, sessionsAt = 0, quotasAsked = 0;

function renderUsageEvery() {
  const cur = USAGE_EVERY.find(([n]) => n === usageEvery);
  const b = $("#usageEvery");
  b.replaceChildren(el("span", "", t(cur[1])), svg(CHEV, 11, 1.6));
  b.title = t("Refresh every");
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(b, USAGE_EVERY.map(([n, name]) => ({ v: String(n), name: t(name), note: "" })), String(usageEvery), (v) => {
      usageEvery = Number(v);
      try { localStorage.setItem("magpie.usageEvery", v); } catch {}
      usageLast = performance.now();
      renderUsageEvery();
    }, "Refresh every", "sess-menu");
  };
  const r = $("#usageReload");
  // on the Overview it reads the allowances again too (#486: they had a
  // Refresh of their own beside it)
  const what = usageTab === "usage" ? t("Refresh now, the allowances too; a Claude account's is read by running Claude Code's own /usage") : t("Refresh now");
  r.title = usageReadAt ? what + " · " + t("Updated {time}", { time: new Date(usageReadAt).toLocaleTimeString(locale === "zh" ? "zh-CN" : undefined, { hour12: false }) }) : what;
  r.setAttribute("aria-label", t("Refresh now"));
  r.onclick = async () => {
    r.classList.add("busy");
    try { await refreshUsage(true); } finally { r.classList.remove("busy"); r.blur(); }
  };
}

// refreshUsage reads what the tab shown draws; now is the reader asking, which
// reads the page it is on too, and the sessions and the allowances.
// A read that outlives its interval must not have another started behind it:
// one is in flight at a time, and a read asked for while one is (the timer, or
// the reader's button, which may not be swallowed) is served when it is done,
// the reader's own if either asked for it. The promise covers that second read
// too, so the button turns while the reader's own is still to come.
let usageFlight = null, usageQueued = false, usageQueuedNow = false;
function refreshUsage(now = false) {
  usageLast = performance.now();
  if (usageFlight) {
    usageQueued = true;
    usageQueuedNow = usageQueuedNow || now;
    return usageFlight; // this promise includes the queued read
  }
  const run = async (want) => {
    try {
      if (usageTab === "sessions") {
        if (sessions && (want || performance.now() - sessionsAt > 15e3)) { sessionsAt = performance.now(); await loadSessions(); }
      } else if (usageTab === "requests") {
        // the newest page takes the requests as they come; an older one stays put
        if (ledger && (want || !ledOffset)) await loadLedger(true);
      } else {
        // the reader asking reads the allowances afresh, a Claude account's by
        // running Claude Code's own /usage (the backend runs it at most once in 30s)
        let q = null;
        if (want) { quotasAsked = performance.now(); q = loadQuotas(true); }
        else if (usage && performance.now() - quotasAsked > 60e3) { quotasAsked = performance.now(); loadQuotas(); }
        if (usage) {
          const p = period;
          const u = await api("usage?period=" + p);
          if (view === "usage" && usageTab === "usage" && p === period && JSON.stringify(u) !== JSON.stringify(usage)) { usage = u; renderUsage(); }
        }
        await q;
      }
      usageReadAt = Date.now();
      renderUsageEvery();
    } catch {}
  };
  const flight = Promise.resolve().then(() => run(now)).then(() => {
    if (usageFlight !== flight) return;
    usageFlight = null;
    if (!usageQueued) return;
    const want = usageQueuedNow;
    usageQueued = usageQueuedNow = false;
    // one more read, the reader's own if either asked for it
    return refreshUsage(want);
  });
  usageFlight = flight;
  return flight;
}
renderUsageEvery();
setInterval(() => {
  if (!usageEvery || view !== "usage" || document.hidden || document.querySelector(".pop:not([hidden])")) return;
  if (performance.now() - usageLast >= usageEvery * 1000) refreshUsage();
}, 1000);
window.addEventListener("focus", load);
setInterval(renderUpdateBadge, 15 * 60 * 1000); // a window left open still hears of a new version
// renderPluginDot puts a dot on Plugins while a plugin's update waits for
// the reader (someone else's plugin, or one pinned; the community's update
// by themselves)
async function renderPluginDot() {
  const b = mode === "window" && document.querySelector('#nav button[data-view="plugins"]');
  if (!b) return;
  const u = await api("plugins/updates").catch(() => null);
  const n = u?.waiting?.length || 0;
  b.classList.toggle("has-dot", n > 0);
  if (n) b.title = t(n === 1 ? "An update for {name} is out" : "Updates for {n} plugins are out", { n, name: u.waiting[0].package });
  else b.removeAttribute("title");
}
window.renderPluginDot = renderPluginDot;
renderPluginDot();
window.addEventListener("focus", renderPluginDot);
setInterval(renderPluginDot, 15 * 60 * 1000);
// ---------- hiding accounts, for a screenshot to share ----------
// Routing and Usage each have a Hide accounts button, one setting for both,
// which the tray panel follows too. Each account on the page — an email
// address, or a name that isn't one (WorkBuddy's nickname or phone number,
// a plugin's account id, #568), in a row, a sentence, a tooltip — is
// swapped for blurred stand-in letters while it's on, as the page redraws
// too; the account itself is kept aside to put back.
//
// accountNames are the names of subscription accounts magpie has told the
// page of (noteAccounts, from what the API answered), beside the accounts
// of the providers and allowances it holds now.
const accountNames = new Set();
// generic words an account can't be named, lest every one of them on the
// page is hidden
const NOT_ACCOUNTS = new Set(["default", "own", "desktop", "account", "main", "local", "unknown", "none", "free", "pro"]);
function noteAccount(s) {
  if (typeof s !== "string") return;
  s = s.trim();
  if (s.length >= 2 && s.length <= 80 && !/[\n\r]/.test(s)) accountNames.add(s);
}
// noteAccounts takes the accounts out of an answer of the API: a user, an
// account named, or a route's account (kind "account", who)
function noteAccounts(x, depth = 0) {
  if (!x || typeof x !== "object" || depth > 8) return;
  if (Array.isArray(x)) { for (const v of x) noteAccounts(v, depth + 1); return; }
  for (const [k, v] of Object.entries(x)) {
    if (typeof v === "string") {
      if (k === "user" || k === "account" || (k === "who" && x.kind === "account")) noteAccount(v);
    } else if (k === "who" && x.kind === "account" && Array.isArray(v)) for (const w of v) noteAccount(w);
    else if (v && typeof v === "object") noteAccounts(v, depth + 1);
  }
}
window.noteAccounts = noteAccounts;
(() => {
  const EYE = "M2 12s3.5-8 10-8 10 8 10 8-3.5 8-10 8-10-8-10-8zM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z";
  const EYE_OFF = "M9.9 4.2A10.4 10.4 0 0 1 12 4c6.5 0 10 8 10 8a17.6 17.6 0 0 1-2.2 3.2M6.6 6.6C3.9 8.4 2 12 2 12s3.5 8 10 8a9.7 9.7 0 0 0 5.4-1.6M9.9 9.9a3 3 0 0 0 4.2 4.2M2 2l20 20";
  // an address a vendor has half masked itself (Zhipu's abc***gh@…) is one
  // address still, the letters before its stars hidden too
  const EMAIL = /[\w.+*•-]+@[\w*•-]+(?:\.[\w*•-]+)+/g, IS_EMAIL = new RegExp("^" + EMAIL.source + "$");
  // stand-in letters of the account's shape, the same each time it's drawn:
  // blurred, they read as a name without being one
  const dots = (s) => { let h = 7; return s.replace(/[^@.]/g, (c) => (h = (h * 31 + c.charCodeAt(0)) >>> 0, "aeiounrstlcmdh"[h % 14])); };
  const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  // names: the accounts that aren't addresses, longest first so one inside
  // another is hidden whole; a name that is a provider's, an agent's or a
  // card's own (WorkBuddy's account with no nickname is "WorkBuddy") is not
  // one to hide
  function names() {
    const all = new Set(accountNames), not = new Set(NOT_ACCOUNTS);
    for (const p of providers?.providers || []) {
      for (const n of [p.id, p.name, p.account?.agentName, p.account?.agent]) if (n) not.add(String(n).toLowerCase());
      if (p.account) { all.add((p.account.user || "").trim()); for (const l of p.account.logins || []) all.add((l.user || "").trim()); }
    }
    for (const q of quotas || []) {
      for (const n of [q.provider, q.name, q.plan]) if (n) not.add(String(n).toLowerCase());
      all.add((q.user || "").trim());
    }
    for (const a of state?.agents || []) for (const n of [a.id, a.name]) if (n) not.add(String(n).toLowerCase());
    return [...all].filter((n) => n.length >= 2 && !IS_EMAIL.test(n) && !not.has(n.toLowerCase())).sort((a, b) => b.length - a.length);
  }
  let findKey = null, find = EMAIL;
  // finder is the pattern of everything to hide now: an address, or a name
  function finder() {
    const ns = names(), key = ns.join("\n");
    if (key !== findKey) {
      findKey = key;
      find = ns.length ? new RegExp(EMAIL.source + "|" + ns.map(esc).join("|"), "g") : EMAIL;
    }
    return find;
  }
  // a name found inside a longer word ("dev" in "developer") isn't the
  // account; an address always is
  const WORD = /[A-Za-z0-9_]/;
  function* hits(s, re) {
    re.lastIndex = 0; // matchAll starts where the pattern's last test ended
    for (const m of s.matchAll(re)) {
      const name = !m[0].includes("@");
      if (name && ((WORD.test(m[0][0]) && WORD.test(s[m.index - 1] || "")) || (WORD.test(m[0].at(-1)) && WORD.test(s[m.index + m[0].length] || "")))) continue;
      yield m;
    }
  }
  // what a page redraws is masked before it's painted; masking isn't
  // itself watched, so it can't set itself off again
  const OBS = { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ["title", "aria-label"] };
  const SKIP = new Set(["SCRIPT", "STYLE", "OPTION", "TEXTAREA"]);
  let masked = false;
  try { masked = localStorage.getItem("magpie.maskEmails") === "1"; } catch {}
  // the pages it hides on: Routing's and Usage's, each with its button; in
  // the tray panel, the whole of it, as the window's setting says
  const targets = mode === "panel" ? [["#view-agents", null]] : [["#view-routing", "#rtMask"], ["#view-usage", "#usageMask"], ["#view-analytics", null]];
  const pages = targets.map(([v, b]) => {
    const view = $(v), btn = b && $(b);
    if (!view) return () => {};
    function mask() {
      const re = finder();
      const walk = document.createTreeWalker(view, NodeFilter.SHOW_TEXT), found = [];
      for (let n; (n = walk.nextNode());) {
        if (n.data.length < 2 || SKIP.has(n.parentElement?.tagName) || n.parentElement?.closest(".pii")) continue;
        re.lastIndex = 0;
        if (re.test(n.data)) found.push(n);
      }
      for (const n of found) {
        const bits = [];
        let last = 0;
        for (const m of hits(n.data, re)) {
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
      for (const [attr, prop] of [["title", "piiTitle"], ["aria-label", "piiAriaLabel"]]) {
        for (const e of view.querySelectorAll("[" + attr + "]")) {
          // one masked already reads as an account too, its stars and all
          if (prop in e.dataset) continue;
          const val = e.getAttribute(attr);
          if (!val) continue;
          let out = "", last = 0;
          for (const m of hits(val, re)) {
            out += val.slice(last, m.index) + m[0].replace(m[0].includes("@") ? /[^@.]/g : /./gu, "•"); // a tooltip can't blur
            last = m.index + m[0].length;
          }
          if (!last) continue;
          e.dataset[prop] = val;
          e.setAttribute(attr, out + val.slice(last));
          if (attr === "title") e.title = out + val.slice(last);
        }
      }
    }
    function unmask() {
      for (const s of view.querySelectorAll(".pii")) s.replaceWith(s.dataset.raw);
      for (const r of view.querySelectorAll(".pii-run")) r.replaceWith(r.textContent);
      view.normalize();
      for (const e of view.querySelectorAll("[data-pii-title]")) { e.title = e.dataset.piiTitle; delete e.dataset.piiTitle; }
      for (const e of view.querySelectorAll("[data-pii-aria-label]")) { e.setAttribute("aria-label", e.dataset.piiAriaLabel); delete e.dataset.piiAriaLabel; }
    }
    const watch = new MutationObserver(() => {
      if (!masked) return;
      watch.disconnect();
      mask();
      watch.observe(view, OBS);
    });
    if (btn) btn.onclick = () => {
      setMasked(!masked);
      // pixelated in when asked for, not again each time the page redraws
      view.classList.add("masking");
      clearTimeout(btn._t);
      btn._t = setTimeout(() => view.classList.remove("masking"), 450);
    };
    return (on) => {
      if (btn) {
        btn.setAttribute("aria-pressed", String(on));
        // what it is now, in its icon and its words: an open eye while the
        // accounts show, struck through once they're hidden
        btn.querySelector("path").setAttribute("d", on ? EYE_OFF : EYE);
        const label = btn.querySelector("[data-t]");
        label.dataset.en = on ? "Accounts hidden" : "Hide accounts";
        label.textContent = t(label.dataset.en);
        // and how to have them back, for one who never turned it on (#653)
        btn.dataset.enTitle = on ? "Accounts are masked for a screenshot — click to show them"
          : "Mask the accounts on this page — email addresses and account names — for a screenshot to share";
        btn.title = t(btn.dataset.enTitle);
      }
      view.classList.toggle("masked", on);
      // a name learnt since it was masked: hidden afresh
      if (on) { watch.disconnect(); unmask(); mask(); watch.observe(view, OBS); }
      else { watch.disconnect(); unmask(); }
    };
  });
  function setMasked(on, keep) {
    masked = on;
    if (!keep) try { localStorage.setItem("magpie.maskEmails", on ? "1" : "0"); } catch {}
    for (const set of pages) set(on);
  }
  // turned in the window: the tray panel (or another window) follows
  window.addEventListener("storage", (e) => {
    if (e.key === "magpie.maskEmails" && (e.newValue === "1") !== masked) setMasked(e.newValue === "1", true);
  });
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
// opened from the tray panel's Usage tab: on the Requests of one provider or agent
if (mode === "window" && params.get("view") === "usage") {
  // An explicit destination takes precedence over the remembered tab.
  const tab = params.get("tab");
  if (tab === "requests" || tab === "usage") usageTab = tab;
  quotaFocus = usageTab === "usage" ? (params.get("card") || params.get("provider") || "") : "";
  quotaFocusUntil = performance.now() + 5000;
  ledProvider = params.get("provider") || "";
  ledAgent = params.get("agent") || "";
  ledComputer = params.get("computer") || "";
  const u = new URL(location.href);
  for (const k of ["tab", "provider", "agent", "computer", "card"]) u.searchParams.delete(k);
  history.replaceState(null, "", u);
}
if (mode === "window" && ["providers", "gateway", "routing", "usage", "sessions", "analytics", "library", "plugins", "settings"].includes(params.get("view"))) show(params.get("view"));
else if (mode === "window") slide($("#nav"), "nav");
load();
