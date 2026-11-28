// The ticks under an agent's accounts that only a Claude, Codex or
// Antigravity CLI editor shows: Only switch {agent}'s sign-in, agy's
// Switch automatically under it (#1908), and Restart the Codex app.
// renderAccounts imports this when one of them applies, so it weighs
// nothing on the core for everyone else.

// switchTicks(a, p, several): the rows to put under a's account list.
window.switchTicks = function (a, p, several) {
  const rows = [];
  const cx = a.agent === "codex", agy = a.agent === "agy-cli", ag = { agent: agy ? "agy" : a.agentName };
  if (cx && several || a.agent === "claude" || agy) {
    const [only, cb] = tick(t("Only switch {agent}'s sign-in", ag), p.switchOnly);
    const [tip, on, off] = cx ? [t("When an account is used up, magpie signs {agent} in to the next one, and requests go straight to OpenAI. Open {agent} sessions keep their account until reopened.", ag), t("magpie only switches {agent}'s sign-in; its requests go straight to OpenAI", ag), t("A connected {agent} fails over to your other accounts through magpie again", ag)]
      // agy's sign-in is the one the Antigravity app reads too (#1908)
      : [agy ? t("Using an account signs agy and the Antigravity app in to it. Requests on these accounts don't go through magpie.") : t("When an account is used up, magpie signs {agent} in to the next one. Requests on these accounts don't go through magpie.", ag), t("magpie serves no requests on these accounts"), t("magpie serves requests on these accounts again")];
    only.title = tip;
    cb.onchange = () => accountAction("provider/switchonly", { id: p.id, switchOnly: cb.checked }, cb.checked ? on : off);
    rows.push(el("div", "switch-only", only));
    if (agy && p.switchOnly) rows.push(agyAutoSwitch(p));
  }
  if (cx && (several || p.restartApp)) {
    const [re, cb] = tick(t("Restart the {agent} app when magpie switches its account back", ag), !!p.restartApp);
    re.title = t("Off, the {agent} app stays on its account until you restart it. A used-up account restarts it anyway.", ag);
    cb.onchange = () => accountAction("provider/restartapp", { id: p.id, restartApp: cb.checked }, t(cb.checked ? "magpie restarts the {agent} app when it switches accounts back" : "magpie leaves the {agent} app running when it switches accounts back", ag));
    rows.push(el("div", "restart-app", re));
  }
  return rows;
};

// agyAutoSwitch: off by default, so agy stays on the account the user
// picked. On, when that account runs low, magpie signs agy in to the
// next Antigravity CLI account with room, once agy and the Antigravity
// app have quit (a running one writes its account back over a switch).
// Proxying agy's requests got accounts suspended, so this is all magpie
// does for agy under Only switch (#1908).
function agyAutoSwitch(p) {
  const box = el("div", "auto-switch"), head = el("label", "as-head"), on = !!p.autoSwitch;
  const name = el("span", "as-name", t("Switch {agent}'s account automatically", { agent: "agy" }));
  const sw = libSwitch("use-credits", on, name.textContent);
  head.title = t("When agy's account runs low, magpie signs agy in to the next one with room, once agy and the Antigravity app have quit.");
  head.append(name, sw);
  box.append(head);
  // a switch it waits to make: agy or the app runs on the spent account
  // (provider.AgyWaiting), so the switch that hasn't come is no mystery
  const w = p.agyWait;
  if (w) box.append(el("div", "as-wait", t(w.app || w.pids?.length
    ? "{from} has used its allowance. magpie signs agy in to {to} once agy and the Antigravity app have quit"
    : "{from} has used its allowance. magpie signs agy in to {to} when the Antigravity CLI plugin's agy has ended, within ten minutes", w)));
  sw.onclick = () => { sw.disabled = true; accountAction("provider/autoswitch", { id: p.id, autoSwitch: !on }, t("Saved")); };
  return box;
}
