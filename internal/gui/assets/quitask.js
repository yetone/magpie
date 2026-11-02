// The question a Quit the user chose puts while agents are connected to
// magpie's gateway (quitask.go; Aisiverto on X: Codex failed with 502s
// after magpie was quit, and nothing said why). It names the agents that
// stop working, says that closing the window keeps magpie running, and
// offers Quit, Disconnect and quit (each agent's own settings put back,
// as its row's Disconnect does), or Cancel, with Don't ask again above
// them (zhujiu on Discord), which Settings › General › Ask before quitting
// turns back on. app.js imports this when the
// question is put, so it weighs nothing on the core.
window.askQuitSheet = async function () {
  let agents;
  try { ({ agents } = await api("quit")); } catch { agents = null; }
  // nothing connected any more (disconnected meanwhile): the Quit goes on
  if (agents && !agents.length) return api("window/quit?now=1", {});
  const ed = el("div", "editor quit-ask");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Quit magpie?")));
  ed.append(head);
  const one = agents?.length === 1;
  ed.append(el("p", "lib-confirm", one
    ? t("{agent} goes through magpie, so it stops working while magpie is closed.", { agent: agents[0].name })
    : t("These agents go through magpie, so they stop working while magpie is closed.")));
  if (agents) {
    const list = el("ul", "quit-agents");
    for (const a of agents) {
      const li = el("li");
      li.append(icon(a.icon), el("span", "", a.name));
      list.append(li);
    }
    ed.append(list);
  }
  const mac = document.body.classList.contains("mac");
  ed.append(el("p", "ag-note", mac ? t("Closing the window keeps magpie running in the menu bar.")
    : t("Closing the window keeps magpie running in the system tray.")));
  // Don't ask again goes with the Quit chosen (noAsk=1 saves it), not with a Cancel
  const [never, box] = tick(t("Don't ask again"), false);
  never.classList.add("quit-never");
  never.title = t("Settings › General › Ask before quitting turns it back on");
  ed.append(never);
  const bye = () => api("window/quit?now=1" + (box.checked ? "&noAsk=1" : ""), {});
  const bar = el("div", "bar");
  const quit = el("button", "text primary", t("Quit"));
  const off = el("button", "text", t("Disconnect and quit"));
  off.title = t("Puts back each agent's own settings first, so they work without magpie");
  const cancel = el("button", "text", t("Cancel"));
  const busy = (b, on) => {
    for (const x of [quit, off, cancel, box]) x.disabled = on;
    b.classList.toggle("busy", on);
  };
  quit.onclick = async (e) => {
    e.stopPropagation();
    busy(quit, true);
    try { await bye(); } catch (err) { busy(quit, false); statusErr(err); }
  };
  // each agent put back as its row's Disconnect puts it back; one that
  // can't be leaves magpie open, with the rest still listed
  off.onclick = async (e) => {
    e.stopPropagation();
    busy(off, true);
    for (const a of agents || []) {
      try {
        state = await api("agents/disconnect/" + a.id, {});
      } catch (err) {
        busy(off, false);
        status(a.name + ": " + t(err.message), "err");
        return;
      }
    }
    try { await bye(); } catch (err) { busy(off, false); statusErr(err); }
  };
  cancel.onclick = cancelAsk;
  if (agents) bar.append(off);
  bar.append(el("span", "grow"), cancel, quit);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  quit.focus(NO_SCROLL);
};
