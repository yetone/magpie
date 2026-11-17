// Kimi Code's subagents (XiaoLing_Arisa on X), one line of its opened row:
// the model each starts on, its effort, and the pool, the other models its
// lead may start one on, as Kimi Code's [secondary_model] has them
// (kimi_secondary.go). app.js imports this the first time the line shows
// (connectPanel), so it weighs nothing on the core.
const SUB_POOL = "subagent pool";
window.kimiSubLine = function (a, line, fieldBtn) {
  const sub = a.fields.find((f) => f.label === SUB_MODEL);
  const eff = a.fields.find((f) => f.label === SUB_EFFORT);
  const l = line(fieldBtn(sub, "ag-pick"));
  if (eff && (eff.options.length || eff.value)) {
    const b = fieldBtn(sub.value ? eff : { ...eff, value: "" }, "ag-eff");
    // Kimi Code takes an effort only beside a subagent model
    b.disabled = !sub.value;
    b.title = subEffortTitle(a, eff, optionFor(eff, eff.value)) + (sub.value ? "" : "\n" + t("Set a subagent model first"));
    b.setAttribute("aria-label", b.title);
    l.append(b);
  }
  l.append(kimiPoolButton(a));
  return l;
};

// kimiPoolButton: the other models Kimi Code's lead may start a subagent
// on, ticked in one menu; the subagent model stays among them
function kimiPoolButton(a) {
  const f = a.fields.find((x) => x.label === SUB_POOL);
  const sub = a.fields.find((x) => x.label === SUB_MODEL);
  const names = (v) => String(v || "").split("\n").filter(Boolean);
  const others = names(f.value).filter((v) => v !== sub?.value);
  const b = el("button", "field ag-pick ag-pool");
  b.type = "button";
  b.dataset.key = f.key;
  const label = (v) => optionFor(f, v)?.label || v;
  b.append(svg("M8 3.25v9.5M3.25 8h9.5", 12, 1.6), el("span", "v" + (others.length ? "" : " empty"), others.length ? others.map(label).join(", ") : t("More models")));
  const c = el("span", "chev");
  c.append(svg(CHEV, 11, 1.7));
  b.append(c);
  b.title = t("Models the lead may start a subagent on") + (others.length ? ":\n" + others.map(label).join("\n") : "");
  b.setAttribute("aria-label", b.title);
  // the row is drawn again after each tick: read its fields as they are now
  const live = (label) => state.agents.find((x) => x.id === a.id)?.fields.find((x) => x.label === label);
  b.onclick = (ev) => {
    openPicker({ id: "", name: "", fields: [] }, { key: f.key, label: "model", value: "", multi: true,
      options: f.options.filter((o) => o.value), picked: (id) => names(live(SUB_POOL)?.value).includes(id),
      onPick: async (id) => {
        const p = live(SUB_POOL), s = live(SUB_MODEL)?.value;
        if (!id || !p) return;
        let have = names(p.value);
        // the subagent model's own tick stays: Kimi wants it in the pool
        if (id === s && have.includes(id)) return;
        have = have.includes(id) ? have.filter((x) => x !== id) : [...have, id];
        if (s && have.length && !have.includes(s)) have.unshift(s);
        try {
          state = await api("set", { agent: a.id, field: p.key, value: have.join("\n") });
          renderAgents();
          // the menu stays on the row drawn again, for the next tick
          const at = document.querySelector(`.agent[data-id="${CSS.escape(a.id)}"] .ag-pool`);
          if (pick?.field.key === f.key && at) { pick.anchor = at; at.classList.add("open"); renderList(); }
        } catch (err) { status(t(err.message), "err"); }
      } }, b, ev);
  };
  return b;
}
