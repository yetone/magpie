// An agent's own context windows (ul3140773077392 on X: dsh compacts
// earlier than Codex at the same window, so its window wants setting
// apart from every other agent's). Picked, never typed as a syntax (the
// owner, 11-12: "128k, gpt-6=1m" in a field was too much to ask): one size
// for all its models, from a menu, and a model of its own with its size on
// a line each, added from the agent's model picker. "*" is all of them,
// any other key the model as the catalog has it (settings.AgentContexts).
// Nothing set is each model's own window. app.js imports this when an
// agent's sheet opens, so it weighs nothing on the core.
const WINDOW_SIZES = [128e3, 200e3, 256e3, 400e3, 512e3, 1e6];
const windowWord = (n) => contextsText({ "*": n }).toUpperCase();

window.agentWindowFill = function (list, a) {
  const kind = kindOf(a).split("#")[0];
  const row = el("div", "row pref agent-window");
  row.dataset.agent = kind;
  const who = el("div", "who"), box = el("div", "window-own");
  who.append(el("div", "name", t("Context window")));
  row.append(who, box);
  const own = () => ({ ...(((prefs || {}).agentContexts || {})[kind] || {}) });
  const main = a.fields.find((f) => f.key === "model");
  const models = (main?.options || []).filter((o) => o.value && !o.reset && !o.run && !o.custom);
  const save = (map) => writingPrefs(api("settings/agent-contexts", { agent: kind, contexts: map }))
    .then((ns) => { prefs = ns; renderSettings(); draw(true); status(t("Takes effect when {agent} next starts", { agent: a.name }), "ok"); }).catch(settingsErr);

  // a size typed when none of the menu's is it: one length, 300k or 1.5m
  const typeIn = (b, cur, done) => {
    const num = input(cur ? contextsText({ "*": cur }) : "", "300k");
    num.className = "words window-in";
    num.spellcheck = false;
    num.setAttribute("aria-label", t("Context window"));
    let gone = false;
    const end = (n) => { if (gone) return; gone = true; n === undefined ? draw(true) : done(n); };
    num.onkeydown = (e) => {
      e.stopPropagation();
      if (e.key === "Escape") end();
      else if (e.key === "Enter") num.blur();
    };
    num.onblur = () => {
      const v = num.value.trim();
      if (!v) return end();
      const r = parseContexts(v);
      if (r.error || Object.keys(r.map).some((k) => k !== "*") || !r.map["*"]) {
        status(t("Context window: {v} is not a length like 128k or 1m", { v }), "err");
        return end();
      }
      end(r.map["*"]);
    };
    b.replaceWith(num);
    num.focus();
  };
  // the sizes, from the app's menu (never a native select); "" is each
  // model's own, offered for all of them only
  const pickSize = (b, cur, none, done) => {
    const odd = cur && !WINDOW_SIZES.includes(cur);
    const opts = [...(none ? [{ v: "", name: t("Each model's own"), note: "" }] : []),
      ...WINDOW_SIZES.map((n) => ({ v: String(n), name: windowWord(n), note: "" })),
      { v: "other", name: odd ? windowWord(cur) : t("Other size…"), note: "" }];
    openProtoMenu(b, opts, !cur ? "" : odd ? "other" : String(cur), (v) => v === "other" ? typeIn(b, cur, done) : done(v ? +v : 0), "Context window", "sess-menu", "right");
  };
  const chip = (text, label, cls) => {
    const b = el("button", "sess-pick " + cls);
    b.type = "button";
    b.setAttribute("aria-label", label);
    b.setAttribute("aria-haspopup", "menu");
    b.setAttribute("aria-expanded", "false");
    b.append(el("span", "", text), svg(CHEV, 11, 1.6));
    return b;
  };
  const modelName = (id) => optNamed(models.find((o) => o.value === id), id);
  const pickModel = (anchor, ev, cur, done) => openPicker(a, { key: "windowModel", label: "model", value: cur || "", options: models, onPick: (v) => { if (v) done(v); } }, anchor, ev);

  let drawn;
  const draw = (again) => {
    const cur = own();
    const key = JSON.stringify(cur);
    if (!again && key === drawn) return;
    drawn = key;
    const lines = [];
    // all its models: the one most set
    const all = chip(cur["*"] ? windowWord(cur["*"]) : t("Each model's own"), t("Context window"), "window-all");
    all.onclick = (e) => {
      e.stopPropagation();
      if (all.classList.contains("open")) return closeProtoMenu();
      pickSize(all, cur["*"] || 0, true, (n) => { const m = own(); if (n) m["*"] = n; else delete m["*"]; save(m); });
    };
    lines.push(all);
    // a model of its own, a line each
    for (const id of Object.keys(cur).filter((k) => k !== "*").sort()) {
      const line = el("div", "window-model");
      line.dataset.model = id;
      const o = models.find((x) => x.value === id);
      const m = el("button", "sess-pick set window-model-b");
      m.type = "button";
      m.setAttribute("aria-haspopup", "menu");
      m.append(icon(o?.icon || "generic"), el("span", "", modelName(id)));
      m.onclick = (ev) => pickModel(m, ev, id, (v) => {
        if (v === id) return;
        const map = own();
        map[v] = map[id];
        delete map[id];
        save(map);
      });
      const s = chip(windowWord(cur[id]), `${modelName(id)}: ${t("Context window")}`, "window-size");
      s.onclick = (e) => {
        e.stopPropagation();
        if (s.classList.contains("open")) return closeProtoMenu();
        pickSize(s, cur[id], false, (n) => { const map = own(); map[id] = n; save(map); });
      };
      const rm = el("button", "window-rm");
      rm.type = "button";
      rm.title = t("Remove");
      rm.setAttribute("aria-label", `${t("Remove")}: ${modelName(id)}`);
      rm.append(svg(CROSS, 10, 1.8));
      rm.onclick = () => { const map = own(); delete map[id]; save(map); };
      line.append(m, s, rm);
      lines.push(line);
    }
    // another model of its own: picked, then its size from the same menu
    if (models.length) {
      const add = el("button", "text window-add", t("Set a model apart"));
      add.type = "button";
      add.onclick = (ev) => pickModel(add, ev, "", (v) => {
        if (own()[v]) return;
        // the picker closes first; the size menu opens from the same button
        requestAnimationFrame(() => add.isConnected && pickSize(add, 0, false, (n) => { const map = own(); map[v] = n; save(map); }));
      });
      lines.push(add);
    }
    box.replaceChildren(...lines);
  };
  draw();
  // drawn again when the settings are read again
  const seen = new MutationObserver(() => row.isConnected ? draw() : seen.disconnect());
  const segsBox = list.querySelector("#fullContextSegs");
  if (segsBox) seen.observe(segsBox, { childList: true });
  list.querySelector(".agent-window")?.remove();
  list.append(row);
};
