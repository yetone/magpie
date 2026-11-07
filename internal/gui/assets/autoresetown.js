// Codex's auto-use rows, in its provider's editor (PROV_PREFS): each ChatGPT account's switches
// and, under them, how early a reset about to expire is used. app.js
// imports this when renderSettings draws them (renderAutoResetOwn), so it weighs
// nothing on the core.

// renderAutoResetOwn lists each ChatGPT account under
// "Codex: auto-use resets", with its switch, the same say as on the
// account's card (settings' codexAutoReset, the CLI's quota auto-reset).
// Avalon on Discord: it was only on the accounts' cards, not in the
// window's Settings. The row shows while a Codex
// account is signed in, or one is still on.
window.autoResetOwnPaint = function (s) {
  const head = $("#codexAutoResetRow");
  const list = head.parentElement;
  list.querySelectorAll(".ar-own").forEach((r) => r.remove());
  const on = s.codexAutoReset || [];
  const users = [...(s.codexUsers || [])];
  for (const u of on) if (!users.some((x) => x.toLowerCase() === u)) users.push(u);
  head.hidden = !users.length;
  // each account's resets, read as the providers' editor reads them,
  // with Use beside them (husky_bobo on X looked for it here)
  const held = users.length ? loginUsageOf("codex") : null;
  if (users.length && !held) loginUsage.codex?.loading?.then(() => { if (!head.hidden && loginUsage.codex?.data) autoResetOwnPaint(prefs || s); });
  let after = head;
  for (const user of users) {
    const r = el("div", "row pref ar-own");
    r.dataset.user = user;
    const who = el("div", "who");
    const name = el("div", "name", user);
    name.title = user;
    who.append(name);
    const q = held && Object.entries(held).find(([u]) => u.toLowerCase() === user.toLowerCase())?.[1];
    if (q?.resets?.count && !q.error && !q.resets.byWindow) {
      const qu = { ...q, provider: "codex", user };
      const sub = el("div", "sub ar-own-resets");
      const use = el("button", "text", t("Use a reset"));
      use.title = resetUseTitle(qu);
      use.setAttribute("aria-haspopup", "dialog");
      use.onclick = (e) => { e.stopPropagation(); askReset(qu); };
      sub.append(resetsWords(qu.resets, qu), " ", use);
      who.append(sub);
    }
    // each of the two times its own Off/On (#2078)
    const was = on.includes(user.toLowerCase()) ? autoResetWhenOf(s.codexAutoResetOnly?.[user.toLowerCase()]) : { week: false, expiring: false };
    const cases = el("div", "ar-own-cases");
    for (const c of AUTO_RESET_CASES) {
      const line = el("div", "ar-own-case");
      line.dataset.case = c.id;
      const say = el("span", "ar-own-say", t(c.say));
      say.title = t(was[c.id] ? c.on : c.off, resetLeadVars());
      const pick = segs([["off", t("Off")], ["on", t("On")]], was[c.id] ? "on" : "off", async (v) => {
        try {
          prefs = await writingPrefs(api("settings/codex-auto-reset", { user, on: v === "on", when: c.id }));
          if (state) state.settings = prefs;
          const now = (prefs.codexAutoReset || []).includes(user.toLowerCase()) ? autoResetWhenOf(prefs.codexAutoResetOnly?.[user.toLowerCase()]) : { week: false, expiring: false };
          status(autoResetStatus(user, now), "ok");
          renderSettings();
          if (quotas) renderQuotas(); // the account's card says the same
        } catch (e) {
          statusTErr(e);
          renderSettings();
        }
      });
      pick.setAttribute("aria-label", t("Auto-use resets") + " · " + t(c.say) + " · " + user);
      line.append(say, pick);
      cases.append(line);
    }
    r.append(who, cases);
    after.after(r);
    after = r;
  }
  renderResetLead(s, head.hidden);
};

// renderResetLead: how long before a reset about to run out unused it is
// used, under the accounts' switches, one for them all; a pick from the
// app's menu (settings/codex-reset-lead). A value set outside the choices
// (Custom…, or the CLI) is shown and kept (shangyuan0818 on Discord).
function renderResetLead(s, hidden) {
  const row = $("#codexResetLeadRow");
  if (!row) return;
  row.hidden = hidden;
  // a redraw leaves Custom…'s field being typed in
  const own = $("#codexResetLeadPick .reset-lead-own");
  if (own && !own.dataset.done) return;
  const cur = resetLeadMinutes(s);
  const leads = RESET_LEADS.includes(cur) ? RESET_LEADS : [...RESET_LEADS, cur].sort((a, b) => a - b);
  const name = (m) => t("{lead} before", { lead: resetLeadWords(m) });
  const b = el("button", "sess-pick reset-lead");
  b.type = "button";
  b.setAttribute("aria-haspopup", "menu");
  b.setAttribute("aria-label", t("How early before one expires") + ": " + name(cur));
  // every choice in one cell, the current one seen: the button is as wide
  // as the widest, so a pick doesn't rewrap the row and move the page
  const say = el("span", "rl-say");
  for (const m of leads) {
    const w = el("span", m === cur ? "on" : "", name(m));
    if (m !== cur) w.setAttribute("aria-hidden", "true");
    say.append(w);
  }
  b.append(say, svg(CHEV, 11, 1.6));
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    const opts = leads.map((m) => ({ v: String(m), name: name(m), note: m === 30 ? "Default" : "", literalName: true }));
    openProtoMenu(b, [...opts, { v: "\x01", name: "Custom…" }], String(cur), (v) => v === "\x01" ? resetLeadOwn(b, cur, set) : set(Number(v)), "Used before it expires", "sess-menu", "right");
  };
  const set = async (m) => {
    if (m === cur) return renderSettings();
    try {
      prefs = await writingPrefs(api("settings/codex-reset-lead", { minutes: m }));
      if (state) state.settings = prefs;
      renderSettings();
      if (quotas) renderQuotas(); // the cards' titles say it too
    } catch (err) {
      statusTErr(err);
      renderSettings();
    }
  };
  $("#codexResetLeadPick").replaceChildren(b);
}

// resetLeadOwn: Custom…'s field of minutes in the button's place; Enter or
// leaving saves, Escape doesn't, one out of range is said, not written.
function resetLeadOwn(b, cur, set) {
  const least = RESET_LEADS[0], most = RESET_LEADS.at(-1);
  const i = input(String(cur), least + "–" + most);
  i.className = "words compact-num reset-lead-own";
  i.inputMode = "numeric";
  i.setAttribute("aria-label", t("Minutes"));
  let done = false;
  const finish = (save) => {
    if (done) return;
    done = true;
    i.dataset.done = "1";
    const v = i.value.trim(), m = /^\d+$/.test(v) ? Number(v) : NaN;
    if (!save || !v) return renderSettings();
    if (!(m >= least && m <= most)) {
      status(t("Minutes is a whole number from 3 to 1440"), "err");
      return renderSettings();
    }
    set(m);
  };
  i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") finish(true); else if (e.key === "Escape") finish(false); };
  i.onblur = () => finish(true);
  // in the button's box: the row doesn't rewrap and move the page
  i.style.width = b.offsetWidth + "px";
  i.style.height = b.offsetHeight + "px";
  b.replaceWith(i);
  i.focus(NO_SCROLL);
}
