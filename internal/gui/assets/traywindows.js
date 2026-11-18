// Settings › Menu bar windows, Menu bar Claude logo, and Windows' Tray icon number. app.js imports this only when the menu bar
// shows a card (renderTrayUsage), so it weighs nothing on the core.

// which of a subscription's windows its menu bar card shows, by kind
// (settings.TrayWindows, StrikingRaw on X): a pill each for those ticked
// with two or more, every window unless some are picked. A window the card shows only when picked
// (trayExtra: money aside, one model's, Cursor's Grok Bot, CrazyJal on X)
// is offered too; its usual windows ticked alone are no pick at all.
window.trayWindowsRow = function trayWindowsRow(s, keep, ids, mac) {
  // a menu bar is the Mac's: Windows and Linux call it the tray
  if (!mac) for (const e of document.querySelectorAll("#setPage-usage [data-t]")) {
    const w = TRAY_WORDS[e.dataset.en];
    if (w) { e.dataset.en = w; e.textContent = t(w); }
  }
  // Windows' tray shows no text: the first card's figure is its icon, or
  // the bird (Benjaminliang_1 on X)
  $("#trayNumberRow").hidden = !ids.length || !document.documentElement.classList.contains("win");
  $("#trayNumberSegs").replaceChildren(segs([["off", t("Off")], ["on", t("On")]], s.trayNoNumber ? "off" : "on",
    (v) => savePrefs({ ...keep, trayNoNumber: v === "off" })));
  trayClaudeRow(s, keep, ids, mac);
  const pick = s.trayWindows || {}, box = $("#trayWindowsPick"), subs = [];
  for (const pid of new Set(ids.filter((id) => !id.startsWith("usage:")).map((id) => id.split("|")[0]))) {
    const ws = [], q = (quotas || []).find((q) => q.provider === pid);
    for (const w of q?.windows || []) if (w.trayKind && !ws.some((x) => x.v === w.trayKind)) ws.push({ v: w.trayKind, name: w.name, note: "", extra: !!w.trayExtra });
    if (ws.length > 1) subs.push([pid, q.name, ws]);
  }
  // Windows' tray icon draws the first one's figure (traynumber.go), so
  // which window it is matters there too
  $("#trayWindowsRow").hidden = !subs.length;
  if (!quotas && ids.some((id) => !id.startsWith("usage:"))) loadQuotas().then(() => box.isConnected && trayWindowsRow(s, keep, ids, mac));
  box.replaceChildren(...subs.map(([pid, name, ws]) => {
    const on = (pick[pid] || []).filter((k) => ws.some((w) => w.v === k)), b = el("button", "proto pick" + (on.length ? " set" : ""));
    b.type = "button";
    b.append(el("span", "", name + " · " + (on.length ? on.map((k) => t(ws.find((w) => w.v === k).name)).join(" · ") : t("All windows"))), svg(CHEV, 11, 1.6));
    b.onclick = (e) => {
      e.stopPropagation();
      if (b.classList.contains("open")) return closeProtoMenu();
      openProtoMenu(b, [{ v: "", name: "All windows", note: "" }, ...ws], on, (v) => {
        const next = { ...pick };
        const usual = ws.filter((w) => !w.extra);
        if (v.length && !(v.length === usual.length && usual.every((w) => v.includes(w.v)))) next[pid] = v; else delete next[pid];
        savePrefs({ ...keep, trayWindows: next });
      }, mac ? "Shown in the menu bar" : "Shown in the tray");
    };
    return b;
  }));
};

// the logos of Claude's cards (gui.trayClaudeLogos)
const TRAY_CLAUDE_LOGOS = ["claude-color", "claudecode-color"];

// Claude's cards in the Mac's menu bar with Claude's logo or Anthropic's
// A\ (settings.TrayAnthropicMark, qingnianxiaozhe on X): asked only where a
// ticked card is Claude's and the logos are shown. Until the cards are read,
// a Claude Code one goes by its id.
function trayClaudeRow(s, keep, ids, mac) {
  const claude = ids.some((id) => {
    const q = (quotas || []).find((q) => trayCardID(q) === id || q.provider + IN_USE === id);
    return q ? TRAY_CLAUDE_LOGOS.includes(q.icon) : id.split("|")[0] === "claude";
  });
  $("#trayClaudeRow").hidden = !mac || !!s.trayNoLogos || !claude;
  const opt = (logo, name) => {
    logo.setAttribute("role", "img");
    logo.setAttribute("aria-label", name);
    return logo;
  };
  $("#trayClaudeSegs").replaceChildren(segs([["claude", opt(icon("claude-color"), "Claude")], ["anthropic", opt(anthropicMark(), "Anthropic")]],
    s.trayAnthropicMark ? "anthropic" : "claude", (v) => savePrefs({ ...keep, trayAnthropicMark: v === "anthropic" })));
}

// Anthropic's A\ as the menu bar draws it (gui.trayAnthropicMark)
function anthropicMark() {
  const e = el("span", "ic anthropic-mark");
  e.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" fill-rule="evenodd" d="M0.6 20 6.84 4h3.6l6.24 16h-3.4l-1.4-3.6H5.4L4 20Zm6.05-6.8h3.98L8.64 8.1Z"/><path fill="currentColor" d="M12.84 4h3.4l6.24 16h-3.4Z"/></svg>';
  return e;
}

const TRAY_WORDS = {
  "Allowances in the menu bar": "Allowances in the tray",
  "Menu bar windows": "Tray windows",
  "Menu bar refresh": "Tray refresh",
  "Menu bar Claude logo": "Tray Claude logo",
  "Menu bar pace": "Tray pace",
  "Menu bar cost": "Tray cost",
  "Menu bar currency": "Tray currency",
  "What magpie's requests cost, beside their tokens in the menu bar and its tooltip; off, the tokens alone": "What magpie's requests cost, beside their tokens in the tray and its tooltip; off, the tokens alone",
  "How much of each window is used, or how much is left, on the Usage page, the panel and the menu bar": "How much of each window is used, or how much is left, on the Usage page, the panel and the tray",
  "When I ask: opening or refreshing the Usage page, a card's refresh, and magpie quota or accounts read them; routing, the menu bar and alerts go by the last reading till then. An account a vendor turns away for its quota is still read.": "When I ask: opening or refreshing the Usage page, a card's refresh, and magpie quota or accounts read them; routing, the tray and alerts go by the last reading till then. An account a vendor turns away for its quota is still read.",
};
