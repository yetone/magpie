// The account or key each agent uses (nb5p on Discord: Claude Code on one
// key of a relay, Codex on another, so the relay's dashboard shows each
// one's use). The provider editor's Advanced lists the agents set to one,
// each with its account, and adds one; an agent not listed uses every
// account, as before. The choices are kept in the editor's draft and
// written on Save (provider.SetAgentAccount through provider/agentaccount).
// app.js imports this when the editor of a provider of two or more
// accounts or keys draws (agentAccountsRow), so it weighs nothing on the
// core.
(() => {
  // the saved choices with the draft's on top; "" lets an agent go
  const now = (p) => {
    const d = draft?.agentAccounts?.id === p.id ? draft.agentAccounts.set : {};
    const out = { ...(p.agentAccounts || {}), ...d };
    for (const k of Object.keys(out)) if (!out[k]) delete out[k];
    return out;
  };
  const set = (p, agent, ref) => {
    if (!draft) return;
    const d = draft.agentAccounts?.id === p.id ? { ...draft.agentAccounts.set } : {};
    if ((p.agentAccounts?.[agent] || "") === ref) delete d[agent];
    else d[agent] = ref;
    // back as saved: nothing left to write, nothing to discard
    draft.agentAccounts = Object.keys(d).length ? { id: p.id, set: d } : undefined;
  };
  const agentName = (id) => state?.agents?.find((a) => a.id === id)?.name || id;
  const choiceName = (p, ref) => p.agentChoices?.find((c) => c.id.toLowerCase() === String(ref).toLowerCase())?.name || ref;

  window.agentAccountsField = function (p) {
    const box = el("div", "agent-accts");
    const pick = (btn, agent) => {
      if (btn.classList.contains("open")) return closeProtoMenu();
      const opts = [{ v: "", name: "All accounts", note: "" }, ...p.agentChoices.map((c) => ({ v: c.id, name: c.name, note: "", literalName: true }))];
      openProtoMenu(btn, opts, now(p)[agent] || "", (v) => { set(p, agent, v); draw(); }, "Which account this agent uses", "sess-menu", "right", false, opts.length > 9 ? "Search keys" : "");
    };
    const line = (agent, ref) => {
      const row = el("div", "agent-acct");
      row.dataset.agent = agent;
      const who = el("span", "who", agentName(agent));
      who.title = who.textContent;
      const btn = el("button", "rt-cond on", ref ? choiceName(p, ref) : t("All accounts"));
      btn.type = "button";
      btn.setAttribute("aria-haspopup", "menu");
      btn.onclick = (e) => { e.preventDefault(); e.stopPropagation(); pick(btn, agent); };
      row.append(who, btn);
      return [row, btn];
    };
    const draw = (opening) => {
      const pinned = now(p);
      box.replaceChildren();
      let open = null;
      for (const [agent, ref] of Object.entries(pinned)) box.append(line(agent, ref)[0]);
      if (opening && !pinned[opening]) {
        const [row, btn] = line(opening, "");
        box.append(row);
        open = btn;
      }
      const rest = (state?.agents || []).filter((a) => !pinned[a.id] && a.id !== opening);
      if (rest.length) {
        const add = el("button", "text agent-acct-add", t("Add agent"));
        add.type = "button";
        add.onclick = (e) => {
          e.preventDefault();
          e.stopPropagation();
          if (add.classList.contains("open")) return closeProtoMenu();
          const opts = rest.map((a) => ({ v: a.id, name: a.name, note: "", literalName: true }));
          openProtoMenu(add, opts, null, (v) => draw(v), "Which agent", "sess-menu", "left", false, rest.length > 8 ? "Search agents" : "");
        };
        box.append(add);
      }
      if (open) requestAnimationFrame(() => open.isConnected && pick(open, opening));
    };
    draw();
    return field(t("Accounts by agent"), box);
  };

  // the draft's choices, written before the provider is saved
  window.saveAgentAccounts = async function (p) {
    const d = draft?.agentAccounts;
    if (!p || !d || d.id !== p.id) return;
    for (const [agent, account] of Object.entries(d.set)) await api("provider/agentaccount", { id: p.id, agent, account });
    if (draft) draft.agentAccounts = undefined;
  };
})();
