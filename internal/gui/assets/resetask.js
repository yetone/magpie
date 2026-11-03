// A Codex reset's confirmation (askReset in app.js): spending a reset
// can't be taken back, so it asks first, names the account and which of
// its resets is used, and lets the user pick another (01huadalang on
// Discord). app.js imports this when a reset's Use is pressed, so it
// weighs nothing on the core.
window.askResetSheet = function (q) {
  const ed = el("div", "editor reset-ask");
  const head = el("div", "ehead");
  head.append(icon(q.icon || "codex"), el("b", "", t("Use a Codex reset?")));
  ed.append(head);
  const who = q.alias || q.user || q.name;
  ed.append(el("p", "lib-confirm", t(q.resets.count === 1
    ? "{who} has 1 reset. Using it starts its windows again at once, as if none of them had been used. It can't be undone."
    : "{who} has {n} resets. Using one starts its windows again at once, as if none of them had been used. It can't be undone.", { who, n: q.resets.count })));
  // a remote magpie's card (office/codex, #3850): that magpie spends it,
  // on the account signed in there
  const remote = q.provider !== "codex" && q.from;
  if (remote) ed.append(el("p", "lib-confirm reset-remote", t("The magpie on {computer}, where this account is signed in, uses it.", { computer: q.from })));
  // which of them is used: the one that runs out first unless the user
  // picks another of those listed one by one (01huadalang on Discord),
  // and the dialog names the one it spends
  const cards = q.resets.each?.length > 1 && q.resets.each.every((c) => c.id) ? q.resets.each : null;
  let chosen = 0;
  const which = el("p", "lib-confirm reset-which");
  const sooner = el("p", "lib-confirm reset-sooner");
  const cardName = (i, at = resetClock) => {
    const c = cards[i];
    return c.until ? t("Reset {n} runs out {when}", { n: i + 1, when: at(new Date(c.until)) }) : t("Reset {n} never runs out", { n: i + 1 });
  };
  const sayWhich = () => {
    const full = (d) => d.toLocaleString(intlLang());
    if (!chosen) {
      which.textContent = q.resets.until ? t("The one used is the one that runs out first, {when}.", { when: full(new Date(q.resets.until)) }) : "";
      which.hidden = !q.resets.until;
      sooner.hidden = true;
      return;
    }
    const c = cards[chosen];
    which.textContent = c.until ? t("The one used is {who}'s reset that runs out {when}.", { who, when: full(new Date(c.until)) })
      : t("The one used is {who}'s reset that never runs out.", { who });
    which.hidden = false;
    // one that runs out sooner is left, and lost if it isn't used in time
    sooner.textContent = cards[0].until ? t("The one that runs out first, {when}, is kept, and is lost if it isn't used by then.", { when: full(new Date(cards[0].until)) }) : "";
    sooner.hidden = !cards[0].until || cards[0].until === c.until;
  };
  if (cards) {
    const row = el("div", "reset-pick");
    const pick = el("button", "sess-pick reset-card");
    pick.type = "button";
    pick.setAttribute("aria-haspopup", "menu");
    pick.setAttribute("aria-expanded", "false");
    pick.append(el("span", "", cardName(0)), svg(CHEV, 11, 1.6));
    pick.title = cardName(0, (d) => d.toLocaleString(intlLang()));
    pick.onclick = (e) => {
      e.stopPropagation();
      if (pick.classList.contains("open")) return closeProtoMenu();
      openProtoMenu(pick, cards.map((c, i) => ({ v: String(i), name: cardName(i), note: i ? "" : t("Used unless you pick another"), literalName: true })), String(chosen), (v) => {
        chosen = Number(v);
        pick.firstChild.textContent = cardName(chosen);
        pick.title = cardName(chosen, (d) => d.toLocaleString(intlLang()));
        pick.classList.toggle("set", chosen > 0);
        sayWhich();
      }, "Reset to use", "sess-menu");
    };
    row.append(el("span", "reset-pick-label", t("Reset to use")), pick);
    ed.append(row);
  }
  sayWhich();
  ed.append(which, sooner);
  // nothing used yet: a reset would start nothing again
  if (!q.windows?.some((w) => w.used > 0)) ed.append(el("p", "lib-confirm reset-idle", t("None of its windows has been used yet, so there is nothing to start again.")));
  const bar = el("div", "bar");
  const go = el("button", "text primary", t("Use a reset"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    try {
      const ask = { user: q.user || "" };
      if (remote) ask.provider = q.provider;
      if (chosen) ask.credit = cards[chosen].id;
      const out = await api("usage/codex-reset", ask);
      // the providers' editor and Settings read the account's windows
      // and resets on their own, kept a minute: read again, as Usage is
      // (#1491); asked from the editor, closing goes back to it
      delete loginUsage.codex;
      closeConfirmAsk();
      status(who + ": " + resetOutcome(out), out.code === "reset" ? "ok" : "err");
      if (editing && view === "providers") renderProviders();
      if (window.autoResetOwnPaint && $("#codexAutoResetRow")) autoResetOwnPaint(prefs);
      loadQuotas();
    } catch (err) {
      go.disabled = false;
      go.classList.remove("busy");
      status(remote ? remoteResetError(err.message) : err.message, "err");
    }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelAsk;
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  // asked from a provider's editor (roamlog on X): Cancel goes back to it
  if (editing && view === "providers") ed.back = renderProviders;
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  go.focus();
};

// remoteResetError says why a remote magpie used no reset (remote_quotas.go)
function remoteResetError(err) {
  if (/^this key is read-only on the remote magpie/.test(err)) return t("This key is read-only on that computer's magpie — give this provider a key with full access to use a reset from here");
  if (/didn't say in time whether the reset was used/.test(err)) return t("That computer didn't say in time whether the reset was used — it may have been. Refresh the card before asking again");
  if (/^remote magpie doesn't share its quotas/.test(err)) return t("That computer's magpie can't use a reset from here yet — update magpie there");
  if (/^remote magpie holds this key/.test(err)) return t("That computer's magpie holds this key to a budget, models or accounts, so it uses none of its resets — give this provider a key with full access");
  if (/^remote magpie isn't shared/.test(err)) return t("That computer's magpie isn't shared — turn on Settings → Network and sharing → Share on local network there");
  if (/^remote magpie didn't take this key/.test(err)) return t("That computer's magpie didn't take this key — use one of its gateway keys in this provider");
  if (/^couldn't reach the remote magpie/.test(err)) return t("Couldn't reach that computer's magpie — check that it is running and its address");
  return err;
}

// what a reset came to, in words (moved here from app.js; only this sheet says it)
function resetOutcome(out) {
  switch (out.code) {
    case "reset": return t(out.windows === 1 ? "1 window started again" : "{n} windows started again", { n: out.windows });
    case "nothing_to_reset": return t("nothing to start again — no window has been used, and the reset is kept");
    case "no_credit": return t("no reset left on the account");
    case "already_redeemed":
    case "already_used": return t("that reset was already used");
    // Anthropic's
    case "not_limited": return t("nothing to reset — no window is used up yet, and the reset is kept");
    case "cooldown": return t("a reset was used a short while ago — try again later");
    case "ineligible": return t("the account can't use a reset");
    case "unavailable": return t("resets can't be used right now — try again later");
  }
  return out.code;
}
