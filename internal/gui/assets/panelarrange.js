// The tray panel's arranging of its Allowances tab, loaded when Arrange is
// pressed (app.js's arrange button), so it weighs nothing on the core.

// The Allowances tab's arranging (H20 on Discord): a row a subscription,
// in the order the Usage page's cards have, its logo the handle to move it
// (drag, or Alt+arrows) and a pill to hide it from this tab or show it
// again. Hiding is the panel's alone; routing and the rest still use it.
window.panelArrange = function panelArrange(subs) {
  const g = el("div", "pq-group pq-arranging");
  const head = el("div", "pq-gh");
  head.append(el("span", "pq-gn", t("Arrange")));
  const done = el("button", "pq-mode pq-done", t("Done"));
  done.type = "button";
  done.onclick = () => { panelArranging = false; renderPanelQuota(); };
  head.append(done);
  g.append(head, el("p", "pq-anote", t("Hiding is for this panel only: routing, the Usage page and the menu bar still use what's hidden. The order is the Usage page's too.")));
  const hidden = new Set(state.settings?.panelUsageHidden || []);
  const list = el("div", "pq-alist");
  const seen = new Set();
  const gone = panelPageCut(subs).gone;
  for (const q of subs) {
    if (seen.has(q.provider)) continue;
    seen.add(q.provider);
    const page = gone(q.provider), off = page || hidden.has(q.provider);
    const row = el("div", "pq-arow" + (off ? " put-away" : ""));
    row.dataset.key = q.provider;
    const h = el("button", "ag-handle pq-ahandle");
    h.type = "button";
    h.setAttribute("aria-label", t("Arrange {agent}", { agent: q.name }));
    h.title = t("Drag to reorder · Alt+arrow keys to move");
    h.append(icon(q.icon || "generic"));
    h.onkeydown = (e) => {
      const step = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 }[e.key];
      if (!e.altKey || !step) return;
      e.preventDefault();
      moveUsage(q.provider, usageKeys().indexOf(q.provider) + step);
      $(`#panelQuota .pq-arow[data-key="${CSS.escape(q.provider)}"] .pq-ahandle`)?.focus(NO_SCROLL);
    };
    h.onpointerdown = (e) => {
      if (usageArranging) return;
      usageArranging = dragCards(e, h, row, list, [...list.children], (to) => moveUsage(q.provider, to), () => {
        usageArranging = false;
        if (usageRenderPending) { usageRenderPending = false; renderQuotas(); }
      });
    };
    const pill = el("button", "pq-more pq-show", t(off ? "Show" : "Hide"));
    pill.type = "button";
    pill.setAttribute("aria-pressed", String(!off));
    pill.title = t(off ? "Show {name} in this panel" : "Hide {name} from this panel", { name: q.name });
    pill.onclick = () => hidePanelUsage(q.provider, !off, page);
    row.append(h, el("span", "pq-aname", q.name), off ? el("span", "pq-hidden", t("Hidden")) : "", pill);
    list.append(row);
  }
  g.append(list);
  return g;
};

// hidePanelUsage hides a subscription from the panel's Allowances tab, or
// shows it again: drawn at once, put back if the save fails. One the Usage
// page hides every card of (page) is shown there again too, as the panel
// leaves out what that page hides (onkashiko on X).
async function hidePanelUsage(key, hide, page) {
  const prev = state.settings;
  const cur = (prev.panelUsageHidden || []).filter((k) => k !== key);
  const panelHidden = hide ? [...cur, key] : cur;
  const cardsHidden = !hide && page ? cardsShownFor(key) : void 0;
  state.settings = { ...prev, panelUsageHidden: panelHidden, ...(cardsHidden && { usageCardsHidden: cardsHidden }) };
  const again = () => {
    renderPanelQuota();
    $(`#panelQuota .pq-arow[data-key="${CSS.escape(key)}"] .pq-show`)?.focus(NO_SCROLL);
  };
  again();
  try {
    const s = await api("usage/arrange", { panelHidden, cardsHidden });
    state.settings = { ...state.settings, panelUsageHidden: s.panelUsageHidden || [], usageCardsHidden: s.usageCardsHidden || [] };
  } catch (e) {
    state.settings = prev;
    again();
    statusErr(e);
  }
}
