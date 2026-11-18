// advGroups puts the provider editor's Advanced rows under four small
// headings (item 19 of the 11-17 scan: some thirty rows in one run read as
// a wall). Each row keeps its own order inside its group, and a row this
// list doesn't name stays with the row before it. A heading shows only
// when Advanced holds rows of two groups or more. app.js's groupAdvanced
// imports this when an editor first opens, so it weighs nothing on the
// core.
(() => {
  const GROUPS = [
    ["Routing", ["Routing", "Rate limited", "Stays", "Resets", "Warm up", "Check in", "Claude Code", "Fallback", "Rests", "Errors"]],
    ["Models", ["Context window", "Max output", "Compaction", "Compact at", "Images", "Web search"]],
    ["Limits and cost", ["Requests at once", "Accounts at once", "Requests per minute", "Queue size", "Price rate", "Free for me", "Billing", "Rate windows", "Account balance", "Balance URL", "Balance field"]],
    ["Connection", ["Headers", "Extra body", "Reply headers", "Request headers to pass", "Proxy", "Upstream", "Key header", "User-Agent", "Codex client", "Masking", "Certificate", "HTTP version", "WebSocket", "Agents"]],
  ];
  window.advGroups = function (box) {
    if (box.dataset.grouped) return;
    box.dataset.grouped = "1";
    const of = new Map();
    GROUPS.forEach(([, labels], i) => labels.forEach((k) => of.set(t(k), i)));
    // a row is its label and the control after it; anything else (the
    // balance slot, a placeholder a lazy module fills) is a row of its own
    const rows = [];
    for (let n = box.firstChild; n; ) {
      const next = n.nextSibling;
      if (n.nodeName === "LABEL" && next) {
        rows.push({ nodes: [n, next], key: n.firstChild?.textContent?.trim() });
        n = next.nextSibling;
      } else {
        rows.push({ nodes: [n], key: n.classList?.contains("bal-slot") ? t("Account balance") : "" });
        n = next;
      }
    }
    let last = GROUPS.length - 1;
    for (const r of rows) last = r.group = of.has(r.key) ? of.get(r.key) : last;
    const used = new Set(rows.map((r) => r.group));
    const heads = used.size > 1;
    for (const [i, [name]] of GROUPS.entries()) {
      if (!used.has(i)) continue;
      if (heads) box.append(el("div", "adv-head", t(name)));
      for (const r of rows) if (r.group === i) box.append(...r.nodes);
    }
  };
})();
