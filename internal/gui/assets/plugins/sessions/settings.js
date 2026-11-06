// The sessions plugin's part of Settings: General's Session terminal and
// Privacy's Agents' session history. Run after app.js, whose helpers it
// shares (el, segs, svg, api, writingPrefs, savePrefs, openProtoMenu,
// status), and before start.js draws Settings; drawn whether the plugin is
// on or off.

function renderSessionTerminal(s, keep) {
  const row = $("#sessionTerminalRow");
  // a Mac's, and Windows' and Linux's when a terminal is found there
  const mac = document.body.classList.contains("mac");
  const apps = [...(s.terminalApps || [])];
  row.hidden = web || !mac && !apps.length;
  if (row.hidden) return;
  const select = $("#sessionTerminalSelect");
  if (mac && !apps.some((app) => app.id === "com.apple.Terminal")) apps.unshift({ id: "com.apple.Terminal", name: "Terminal" });
  const chosen = s.sessionTerminal || "system";
  // a default that is not a terminal opens Terminal, as the server does
  const defaultApp = apps.find((app) => app.id === s.terminalDefault) || apps.find((app) => app.id === "com.apple.Terminal") || apps[0];
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

// renderSessionReads: which agents' own session history magpie reads
// (RustCats on X: a new install showed every agent's sessions before
// anyone agreed). The Sessions page asks once; here it changes later.
// "Agents found later" is whether an agent with no row of its own is
// read, and drawn from the settings at once; each agent found on this
// computer has a row of its own, read from /api/sessions/reads when the
// Privacy part is shown (loadSessionReads).
let sessionReads = null; // /api/sessions/reads, as last read
let sessionReadsSeq = 0;
function renderSessionReads(s) {
  const box = $("#sessionReadsList");
  const r = sessionReads || { asked: !!s?.sessionReads?.asked, all: !!s?.sessionReads?.all, found: [] };
  // gateway mode reads no agent's session history here, whatever these
  // say (ownSessionsOff), so they are left out
  const gw = !!(s?.gatewayOn || r.gateway);
  box.hidden = gw;
  box.previousElementSibling.hidden = gw;
  if (gw) return;
  const save = (body) => {
    const n = ++sessionReadsSeq;
    writingPrefs(api("sessions/reads", body))
      .then((got) => { if (n === sessionReadsSeq) { sessionReads = got; renderSessionReads(prefs); } })
      .catch((e) => { status(t(e.message), "err"); renderSessionReads(prefs); });
  };
  const row = (name, sub, on, fn) => {
    const rw = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name));
    if (sub) who.append(el("div", "sub", sub));
    const val = el("div", "val");
    val.append(segs([["off", t("Off")], ["on", t("On")]], on ? "on" : "off", (v) => {
      if ((v === "on") !== on) fn(v === "on");
    }));
    rw.append(who, val);
    return rw;
  };
  const rows = [row(t("Agents found later"), t("Read the session history of an agent with no row here, one installed later too. Off, magpie reads only the agents turned on below; what its gateway served is counted either way"),
    r.asked && r.all, (on) => {
      // the agents with a row keep what their rows show
      const agents = {};
      for (const a of r.found || []) agents[a.agent] = !!a.on;
      save(Object.keys(agents).length ? { all: on, agents } : { all: on });
    })];
  for (const a of r.found || []) {
    const rw = row(a.name, "", a.on, (on) => save({ agents: { [a.agent]: on } }));
    rw.dataset.agent = a.agent;
    rows.push(rw);
  }
  box.replaceChildren(...rows);
}
function loadSessionReads() {
  const n = ++sessionReadsSeq;
  api("sessions/reads").then((got) => {
    if (n !== sessionReadsSeq) return;
    sessionReads = got;
    renderSessionReads(prefs);
  }).catch(() => {}); // the row drawn from the settings stays
}
