// A provider's accounts together (CryptoSarom on X: how much of a model is
// left matters more than which account it is on). app.js imports this once
// some provider has two accounts (quotaAllJS), and the Routing page once a
// group's row says what its accounts have left, so it weighs nothing on
// the core.
//
// mergeQuotas: one provider's accounts as one figure a window. Only what
// reads alike is put together: a counted window (WorkBuddy's credits) adds
// its amounts and limits, a wallet its balances, each in one unit; a window
// said only as a share is averaged, and only over accounts on one plan,
// whose windows are the same size, so the mean is the share of their
// allowance together. A share is never added to a count. An account whose
// reading failed is not 0 but unknown: it is left out and counted as that;
// so is a window an earlier reading (asOf) stands in for whose reset has
// passed. A window only the vendor's text says has nothing to add up and is
// left out. A window the account can't spend, held by another used up that
// starts again after it (heldBy: 0% of 7 days left, 100% of 5 hours), has
// nothing left to count: it adds its whole as used (#4791). Unlimited only where every account read says so, and one
// account's window alone is that account's, already below: only what two
// or more have goes together (a Plus and a Pro account have no 5 hours in
// common). The windows keep their family and pool, so familyWindows folds
// them as an account's. accounts: those with a reading; unknown: those
// without.
//
// quotaAll: the strip of them under a Usage card's or the tray panel's
// head: "All 3 accounts", each window's figure and a thin bar, and how many
// couldn't be read. Nothing when fewer than two accounts read, or nothing
// of theirs adds up. No account is named in it (Hide accounts). A click on
// it, or its chevron, folds the accounts under it away (justfreefall on X),
// remembered by provider for the window and the panel alike
// (magpie.usageAllFolded): quotaAllFolded says whether a provider's are.
function mergeQuotas(subs, now = Date.now()) {
  const read = subs.filter((q) => !q.error && (q.windows?.length || q.balance));
  const unknown = subs.length - read.length;
  const plans = new Set(read.map((q) => q.plan || ""));
  const by = new Map();
  for (const q of read) {
    for (const w of q.windows || []) {
      if (w.text) continue;
      const kind = w.unlimited ? "u" : w.limit > 0 ? "c" : w.balance != null ? "b" : w.used != null ? "p:" + (q.plan || "") : "";
      if (!kind) continue;
      const k = JSON.stringify([w.name, w.family || "", w.pool || "", w.unit || "", kind]);
      let m = by.get(k);
      if (!m) by.set(k, (m = { w, kind, plan: q.plan || "", n: 0, stale: 0, held: 0, amount: 0, limit: 0, balance: 0, used: 0, soon: null }));
      if (q.asOf && w.resetsAt && Date.parse(w.resetsAt) <= now) { m.stale++; continue; }
      m.n++;
      const held = w.heldBy && kind !== "u" && kind !== "b";
      if (held) m.held++;
      if (kind === "c") { m.amount += held ? w.limit : Math.max(0, w.amount || 0); m.limit += w.limit; }
      else if (kind === "b") m.balance += w.balance;
      else if (kind !== "u") m.used += held ? 100 : to100(w.used);
      const at = w.resetsAt && Date.parse(w.resetsAt);
      if (at > now && (m.soon == null || at < m.soon)) m.soon = at;
    }
  }
  const windows = [];
  for (const m of by.values()) {
    if (m.n < 2) continue; // one account's is already below
    if (m.kind === "u" && m.n < read.length) continue;
    const { w } = m;
    const out = { name: w.name, family: w.family, pool: w.pool, unit: w.unit, accounts: m.n, unknown: m.stale, held: m.held, how: m.kind[0] };
    if (m.kind === "u") out.unlimited = true;
    else if (m.kind === "c") Object.assign(out, { amount: m.amount, limit: m.limit, used: (100 * m.amount) / m.limit });
    else if (m.kind === "b") out.balance = m.balance;
    else {
      out.used = Math.round((10 * m.used) / m.n) / 10;
      if (plans.size > 1) out.plan = m.plan;
    }
    if (m.soon) out.firstReset = new Date(m.soon).toISOString();
    windows.push(out);
  }
  return { accounts: read.length, unknown, windows };
}
// quotaAll: the strip (above)
function quotaAll(subs, cls = "", max = Infinity) {
  if (subs.length < 2) return null;
  const all = mergeQuotas(subs);
  if (all.accounts < 2 || !all.windows.length) return null;
  const box = el("div", "quota-all" + (cls ? " " + cls : ""));
  const lead = el("span", "quota-all-head", t("All {n} accounts", { n: subs.length }));
  lead.title = t("These accounts' windows together: amounts of one unit are added up; a window said only as a share is the mean of the accounts on one plan, which have the same allowance. Each account is below.");
  const p = subs[0].provider, open = !quotaAllFolded(p);
  const fold = el("button", "quota-acct-fold quota-all-fold");
  fold.type = "button";
  fold.append(svg(CHEV, 11, 1.6));
  fold.setAttribute("aria-expanded", String(open));
  fold.title = t(open ? "Fold" : "Unfold");
  box.dataset.fold = p;
  box.append(lead, fold);
  box.onclick = () => {
    // text being selected in it is not a click on it
    if (String(getSelection()).trim() && box.contains(getSelection().anchorNode)) return;
    const had = document.activeElement === fold;
    setQuotaAllFolded(p, open);
    if (had) document.querySelector(`.quota-all[data-fold="${CSS.escape(p)}"] .quota-all-fold`)?.focus(NO_SCROLL);
    if (mode !== "panel") backToReader($("#view-usage"));
  };
  const ws = familyWindows(all.windows);
  for (const w of ws.slice(0, max)) {
    const item = el("span", "quota-all-w");
    const name = t(w.name) + (w.plan ? " · " + planName(w.plan) : "");
    item.append(el("span", "quota-all-name", name), el("span", "quota-all-n", quotaText(w)));
    if (!w.unlimited && !quotaNoShare(w)) {
      const bar = el("span", "quota-all-bar" + (to100(w.used) >= 90 ? " full" : ""));
      const fill = el("i");
      fill.style.width = quotaFill(w) + "%";
      bar.append(fill);
      item.append(bar);
    }
    item.title = tipLines(
      name + " · " + quotaText(w),
      t(w.how === "p" ? "The mean of {n} accounts on one plan" : w.how === "u" ? "Unlimited on each of {n} accounts" : "Added up over {n} accounts", { n: w.accounts }),
      w.unknown && t("{n} more read before it started again: left out", { n: w.unknown }),
      w.held && t("{n} of them can't spend theirs before a longer window used up starts again: counted as none left", { n: w.held }),
      w.firstReset && t("The first of them starts again {when}", { when: resetClock(new Date(w.firstReset)) }),
      w.tiers && "\n" + tiersText(w));
    box.append(item);
  }
  if (all.unknown) {
    const u = el("span", "quota-all-unknown", t("{n} couldn't be read", { n: all.unknown }));
    u.title = t("An account whose reading failed isn't counted as empty or full: it is left out of these figures");
    box.append(u);
  }
  return box;
}
// read on first use: loading this file touches nothing of app.js's
let allFolded;
const allFolds = () => (allFolded ||= new Set(kept("usageAllFolded", [])));
globalThis.addEventListener?.("storage", (e) => { if (e.key === "magpie.usageAllFolded") { allFolded = null; renderQuotas(); } });
const quotaAllFolded = (p) => allFolds().has(p);
// setQuotaAllFolded: true when it changed
function setQuotaAllFolded(p, fold) {
  if (fold === allFolds().has(p)) return false;
  if (fold) allFolded.add(p); else allFolded.delete(p);
  keep("usageAllFolded", [...allFolded]);
  renderQuotas();
  return true;
}
window.mergeQuotas = mergeQuotas;
window.quotaAllFolded = quotaAllFolded;
window.setQuotaAllFolded = setQuotaAllFolded;
window.quotaAll = quotaAll;
