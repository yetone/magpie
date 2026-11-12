// An agent's Long conversations, one row in its sheet (molianyin4227 on
// Discord: models differ, so one size for every agent fits none; the owner,
// 11-12: two rows for one thing, every agent's above this agent's, read as
// the same setting twice). It shows what this agent does, its own value
// else every agent's, and a change is its own (settings.AgentCompacts);
// every agent's row stays in Settings for when no agent holds it. app.js
// imports this when an agent's sheet holds Long conversations, so it weighs
// nothing on the core.
const compactFill = function (list, a) {
  const kind = kindOf(a);
  const row = el("div", "row pref agent-compact");
  row.dataset.agent = kind;
  const who = el("div", "who"), box = el("div");
  who.append(el("div", "name", t("Long conversations")), el("div", "sub", t("{agent} compacts at 272K or the size picked; the full window is slower and costlier. magpie's models only: a subscription's own models keep their own.", { agent: a.name })));
  row.append(who, box);
  // what Codex's own config holds back, said under the row (renderAgentPrefs)
  const held = el("div", "compact-helds");
  let drawn;
  const draw = () => {
    if (kind === "codex") held.replaceChildren(...list.querySelectorAll("#fullContextSegs .compact-held"));
    // the opened row is built before the settings are read; drawn again then,
    // and only when its values changed, so the page under it holds still
    const s = prefs || {}, own = (s.agentCompacts || {})[kind];
    const key = JSON.stringify([own, s.compactAt, s.fullContext]);
    if (key === drawn) return;
    drawn = key;
    const full = own ? !!own.full : !!s.fullContext;
    const at = own ? own.at || 0 : s.compactAt || 0;
    const set = (body) => writingPrefs(api("settings/agent-compact", { agent: kind, own: true, ...body }))
      .then((ns) => { prefs = ns; renderSettings(); if (row.isConnected) draw(); }).catch(settingsErr);
    const size = contextsText({ "*": at || 272000 });
    const sg = segs([["at", t("Compact at {n}", { n: size.toUpperCase() })], ["full", t("Full window")]], full ? "full" : "at",
      (v) => set({ full: v === "full", at }));
    const num = input(at ? size : "", "272k");
    num.className = "words compact-num";
    num.hidden = full;
    num.title = t("Compact at");
    num.setAttribute("aria-label", t("Compact at"));
    num.onchange = () => {
      const r = parseContexts(num.value);
      if (r.error || Object.keys(r.map).some((k) => k !== "*")) return status(t("Compact at: {v} is not a number of tokens like 272k", { v: num.value.trim() }), "err");
      set({ full: false, at: r.map["*"] || 0 });
    };
    const line = el("div", "compact-at");
    line.append(sg, num);
    box.replaceChildren(line, held);
  };
  draw();
  list.querySelector(".agent-compact")?.remove();
  list.querySelectorAll(".claude-cache, .claude-attribution").forEach((r) => r.remove());
  // every agent's row gives its place to this one (app.css .own-compact)
  list.classList.add("own-compact");
  const above = list.querySelector("#fullContextRow");
  above ? above.after(row) : list.append(row);
  if (kind === "claude") row.after(...claudeCacheRows(list), claudeAttributionRow(list));
  // every agent's values, and Codex's hints, change when that row is drawn
  const seen = new MutationObserver(() => row.isConnected ? draw() : seen.disconnect());
  // Details can build this list before attaching it to the page.
  const segsBox = list.querySelector("#fullContextSegs");
  if (segsBox) seen.observe(segsBox, { childList: true });
};

// Claude Code's prompt cache, beside its compaction (Linka on Discord: the
// Usage card offers no way to change one that is already the cheapest):
// the main conversation's and the subagents', each 5 min, 1 hour or
// Claude Code's own default, which takes the key out of its settings.json
// (agent.ClaudeCacheSet). One a variable sets shows it, unchangeable here.
function claudeCacheRows(list) {
  const KNOBS = [["cache_ttl", "Prompt cache · main conversation"], ["subagent_cache_ttl", "Prompt cache · subagents"]];
  const OPTS = [["", "Claude Code's default"], ["5m", "5 min"], ["1h", "1 hour"]];
  return KNOBS.map(([knob, label]) => {
    const row = el("div", "row pref claude-cache");
    row.dataset.knob = knob;
    const who = el("div", "who"), val = el("div", "val");
    who.append(el("div", "name", t(label)));
    row.append(who, val);
    let drawn;
    const draw = () => {
      const c = ((prefs || {}).claudeCache || []).find((x) => x.knob === knob) || {};
      const key = JSON.stringify(c);
      if (key === drawn) return;
      drawn = key;
      const cur = c.value || "", known = OPTS.find(([v]) => v === cur);
      const b = el("button", "sess-pick");
      b.type = "button";
      b.setAttribute("aria-label", t(label));
      b.setAttribute("aria-haspopup", "menu");
      b.setAttribute("aria-expanded", "false");
      b.append(el("span", "", known ? t(known[1]) : cur), svg(CHEV, 11, 1.6));
      b.disabled = !!c.locked;
      b.onclick = (e) => {
        e.stopPropagation();
        if (b.classList.contains("open")) return closeProtoMenu();
        openProtoMenu(b, OPTS.map(([v, name]) => ({ v, name, note: "" })), cur, (v) => {
          if (v === cur) return;
          writingPrefs(api("settings/claude-cache", { knob, value: v }))
            .then((ns) => { prefs = ns; renderSettings(); if (row.isConnected) draw(); }).catch(settingsErr);
        }, label, "sess-menu", "right");
      };
      val.replaceChildren(b);
      who.querySelector(".sub")?.remove();
      if (c.locked) who.append(el("div", "sub", t("{name} is set and comes first", { name: c.locked })));
    };
    draw();
    // drawn again when the settings are read again
    const seen = new MutationObserver(() => row.isConnected ? draw() : seen.disconnect());
    const box = list.querySelector("#fullContextSegs");
    if (box) seen.observe(box, { childList: true });
    return row;
  });
}

// Claude Code's Co-Authored-By trailer on the commits it makes (9_bishi on
// X): On is Claude Code's own default, Off writes attribution.commit = "" in
// its settings.json (agent.ClaudeAttributionSet). The user's own trailer
// text is shown as it is and not changed here.
function claudeAttributionRow(list) {
  const label = "Commit signature (Co-Authored-By)";
  const row = el("div", "row pref claude-attribution");
  const who = el("div", "who"), val = el("div", "val");
  who.append(el("div", "name", t(label)));
  row.append(who, val);
  let drawn;
  const draw = () => {
    const cur = (prefs || {}).claudeAttribution || "on";
    if (cur === drawn) return;
    drawn = cur;
    const b = el("button", "sess-pick");
    b.type = "button";
    b.setAttribute("aria-label", t(label));
    b.setAttribute("aria-haspopup", "menu");
    b.setAttribute("aria-expanded", "false");
    b.append(el("span", "", t(cur === "off" ? "Off" : cur === "custom" ? "Your own text" : "On")), svg(CHEV, 11, 1.6));
    b.disabled = cur === "custom";
    b.onclick = (e) => {
      e.stopPropagation();
      if (b.classList.contains("open")) return closeProtoMenu();
      openProtoMenu(b, [["on", "On"], ["off", "Off"]].map(([v, name]) => ({ v, name, note: "" })), cur, (v) => {
        if (v === cur) return;
        writingPrefs(api("settings/claude-attribution", { on: v === "on" }))
          .then((ns) => { prefs = ns; renderSettings(); if (row.isConnected) draw(); }).catch(settingsErr);
      }, label, "sess-menu", "right");
    };
    val.replaceChildren(b);
  };
  draw();
  const seen = new MutationObserver(() => row.isConnected ? draw() : seen.disconnect());
  const box = list.querySelector("#fullContextSegs");
  if (box) seen.observe(box, { childList: true });
  return row;
}

// Its own context windows come in the same sheet, last (agentwindow.js).
window.agentCompactFill = function (list, a, ids) {
  if (ids.includes("fullContextRow")) compactFill(list, a);
  if (a.fields.length) import("./agentwindow.js").then(() => agentWindowFill(list, a), () => {});
};
