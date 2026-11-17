// Speed beside Model and Effort (Tengs999 on X): a line of an agent's
// opened row, in the tray panel and under the window's New sessions, for a
// model with a fast mode. app.js imports this the first time the line shows
// (speedLazy, and Details before it measures), so it weighs nothing on the
// core.
// speedNow: the speed agent a's requests for the model option o go at, and
// what decides it, as the gateway decides it: the agent's own fast pick
// first (fastPick, which goes over Codex's Standard), else the model's own
// speed (its Model settings, ModelTiers), else, for Codex, its service tier
// in Settings (codexTiered), which an Ultrafast ask also keeps over a Fast
// pick (withFast). from: "agent", "model", "codex", "ask" (as Codex's /fast
// asks) or "" (the usual speed)
function speedNow(a, o) {
  const s = prefs || state.settings || {};
  let tier = o.fast ? (o.ultra ? "ultrafast" : "priority") : "", from = o.fast ? "agent" : "";
  const own = !tier && s.modelTiers?.[o.ref];
  if (own === "priority" || own === "ultrafast") tier = own, from = "model";
  if (kindOf(a) === "codex") {
    const ct = s.codexServiceTier || "";
    if (ct === "ultrafast" && !o.ultraOk && !tier) tier = "priority", from = "codex";
    else if (ct === "ultrafast" && o.ultraOk && tier !== "ultrafast") tier = "ultrafast", from = "codex";
    else if (ct === "priority" && !tier) tier = "priority", from = "codex";
    else if (!ct && !tier) from = "ask";
  }
  return { tier, from };
}
const SPEED_NAME = { "": "Usual", priority: "Fast", ultrafast: "Ultrafast" };

// speedField: the Speed beside Model and Effort (Tengs999 on X), for a
// model with a fast mode: the speed the requests go at now, what decides
// it, and the app's menu that sets the agent's own fast pick, the very
// switch the model's bolt in the picker is (#954, #1954)
window.speedField = function (a) {
  const f = a.fields.find((x) => x.key === "model");
  const o = f?.value && optionFor(f, f.value);
  if (!o?.fastFor || !o.ref) return null;
  const now = speedNow(a, o), name = o.label || o.value, ag = { agent: a.name, model: name };
  const b = el("button", "field ag-speed" + (now.tier ? " set" : ""));
  b.type = "button";
  b.dataset.key = "\0speed";
  b.setAttribute("aria-haspopup", "menu");
  const v = el("span", "v" + (now.tier ? "" : " empty"));
  if (now.tier) v.append(boltIcon(now.tier === "ultrafast", 11));
  v.append(t(now.from === "ask" ? "As Codex asks" : SPEED_NAME[now.tier]));
  if (now.from === "model" || now.from === "codex") v.append(el("span", "ag-speed-by", " · " + t(now.from === "model" ? "model setting" : "Codex setting")));
  b.append(el("span", "k", t("Speed")), v, el("span", "chev", svg(CHEV, 11, 1.7)));
  const tierName = t(SPEED_NAME[now.tier]);
  b.title = t("{label}: {value}", { label: t("Speed"), value: now.from === "ask" ? t("As Codex asks") + " (" + t("/fast in Codex") + ")" : tierName })
    + (now.from === "model" ? "\n" + t("{model}'s own speed, in its Model settings, sends it {tier} for every agent with no pick of its own", { ...ag, tier: tierName })
      : now.from === "codex" ? "\n" + t("Codex's service tier in Settings sends every Codex request {tier}, over this pick", { tier: tierName })
      : "");
  b.setAttribute("aria-label", b.title);
  b.onclick = (ev) => {
    ev.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    // the default as the gateway goes on without a pick of the agent's own
    const bare = speedNow(a, { ...o, fast: false, ultra: false });
    const dflt = bare.from === "model" ? t("{tier}, as the model is set for every agent", { tier: t(SPEED_NAME[bare.tier]) })
      : bare.from === "codex" ? t("{tier}, as Codex's service tier is set in Settings", { tier: t(SPEED_NAME[bare.tier]) })
      : bare.from === "ask" ? t("/fast in Codex") : t("the usual speed");
    const opts = [
      { v: "", name: "Default", note: dflt },
      { v: "priority", name: "Fast", note: o.ownFast ? "asked of your provider, where it serves it" : "quicker, at a higher price" },
      ...(o.ultraOk ? [{ v: "ultrafast", name: "Ultrafast", note: "where the account offers it, Fast where it doesn't" }] : []),
    ];
    const cur = o.fast ? (o.ultra ? "ultrafast" : "priority") : "";
    openProtoMenu(b, opts, cur, async (v) => {
      if (v === cur) return;
      const was = { fast: !!o.fast, ultra: !!(o.fast && o.ultra) };
      setFast(o, !!v, v === "ultrafast");
      renderAgents();
      try {
        await api("agent-fast", { for: o.fastFor, ref: o.ref, fast: !!v, ...(v === "ultrafast" ? { ultra: true } : {}) });
        status(!v && bare.tier ? t("{label}: {value}", { label: t("Speed for {agent}", ag), value: dflt })
          : t(v === "ultrafast" ? "{model} is sent Ultrafast for {agent}" : v ? "{model} is sent fast for {agent}" : "{model} is sent at the usual speed for {agent}", ag), "ok");
      } catch (e) {
        setFast(o, was.fast, was.ultra);
        renderAgents();
        statusErr(e);
      }
    }, t("Speed for {agent}", ag), "", "left");
  };
  return b;
};
