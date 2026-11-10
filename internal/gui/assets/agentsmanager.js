// Manage agents: hide or unhide many agents at once, by search and
// selection. It changes only magpie's arrangement, never an agent's config.
// app.js imports this when Manage is pressed, so it weighs nothing on the
// core.
window.openAgentsManager = function () {
  if (agentsVisibilitySaving) return;
  const selected = new Set();
  const ed = el("div", "editor ag-manager");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Manage agents")));
  const note = el("p", "lib-confirm", t("Hiding only changes this list, not an agent's connection or settings. Unhidden agents may stay under Not set up."));
  const search = el("input", "words ag-manage-search");
  search.type = "search";
  search.placeholder = t("Search agents…");
  search.setAttribute("aria-label", t("Search agents…"));
  const actions = el("div", "ag-manage-actions");
  const rows = el("div", "ag-manage-rows");
  const count = el("span", "ag-manage-count");
  const result = el("p", "ag-manage-result");
  result.setAttribute("role", "status");
  const bar = el("div", "bar");
  const button = (label, run, parent = actions) => {
    const b = el("button", "text", t(label));
    b.type = "button";
    b.onclick = run;
    parent.append(b);
    return b;
  };
  const matching = () => arrangeAgents().all.filter((a) =>
    (a.name + " " + a.id).toLocaleLowerCase().includes(search.value.trim().toLocaleLowerCase()));
  button("Select search results", () => { matching().forEach((a) => selected.add(a.id)); draw(); });
  button("Invert search selection", () => { matching().forEach((a) => selected.has(a.id) ? selected.delete(a.id) : selected.add(a.id)); draw(); });
  button("Select unconnected agents", () => {
    selected.clear();
    matching().filter((a) => !onMagpie(a)).forEach((a) => selected.add(a.id));
    draw();
  });
  button("Clear selection", () => { selected.clear(); draw(); });
  const hide = button("Hide selected", () => save(true), bar);
  const show = button("Unhide selected", () => save(false), bar);
  bar.append(el("span", "grow"));
  button("Done", () => closeConfirmAsk(), bar);
  const controls = () => {
    ed.querySelectorAll("button, input").forEach((b) => { b.disabled = agentsVisibilitySaving; });
    hide.disabled ||= ![...selected].some((id) => !hiddenIds().includes(id));
    show.disabled ||= ![...selected].some((id) => hiddenIds().includes(id));
    count.textContent = t(selected.size === 1 ? "{n} agent selected" : "{n} agents selected", { n: selected.size });
  };
  // Update status in place: a state refresh must keep search, selection and
  // keyboard focus on the control the reader is using.
  ed.refreshAgents = () => {
    const hidden = new Set(hiddenIds());
    for (const row of rows.querySelectorAll(".ag-manage-row")) {
      row.querySelector(".ag-manage-state").textContent = t(hidden.has(row.dataset.id) ? "Hidden" : "Not hidden");
    }
    controls();
  };
  function draw() {
    rows.replaceChildren();
    for (const a of matching()) {
      const row = el("label", "ag-manage-row");
      row.dataset.id = a.id;
      const check = el("input");
      check.type = "checkbox";
      check.checked = selected.has(a.id);
      check.setAttribute("aria-label", a.name);
      check.onchange = () => { check.checked ? selected.add(a.id) : selected.delete(a.id); controls(); };
      row.append(check, icon(a.icon), el("span", "ag-manage-name", a.name), el("span", "ag-manage-state", t(isHidden(a) ? "Hidden" : "Not hidden")));
      rows.append(row);
    }
    if (!rows.children.length) rows.append(el("p", "lib-confirm", t("No matching agents")));
    controls();
  }
  async function save(hidden) {
    if (agentsVisibilitySaving) return;
    const ids = [...selected].filter((id) => hiddenIds().includes(id) !== hidden);
    if (!ids.length) return;
    const next = new Set(hiddenIds());
    ids.forEach((id) => hidden ? next.add(id) : next.delete(id));
    agentsVisibilitySaving = true;
    controls();
    result.textContent = "";
    agentsManageButton($("#agents"));
    try {
      // Commit once, then publish only the arrangement returned by the server.
      // A failed save leaves both the visible list and selection unchanged.
      const s = await api("agents/arrange", {
        order: state.settings?.agentOrder || [], hidden: [...next], shown: state.settings?.agentsShown || [],
      });
      if (hidden) ids.forEach((id) => unhid.delete(id));
      agentsVisibilityRevision++;
      arranged(s);
      selected.clear();
      result.textContent = hidden
        ? t(ids.length === 1 ? "{n} agent hidden" : "{n} agents hidden", { n: ids.length })
        : t(ids.length === 1 ? "{n} agent unhidden" : "{n} agents unhidden", { n: ids.length });
      renderAgents();
    } catch (e) {
      result.textContent = e.message;
    } finally {
      agentsVisibilitySaving = false;
      agentsManageButton($("#agents"));
      draw();
    }
  }
  search.oninput = draw;
  ed.append(head, note, search, actions, count, rows, result, bar);
  draw();
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
};
