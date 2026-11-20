// The line over the Providers list for a retired built-in whose accounts
// haven't moved to its plugin. app.js imports this when /api/providers
// names any (unmoved), so it weighs nothing on the core for everyone else.

// unmovedLine: a retired built-in whose accounts haven't moved to its
// plugin (a failed install): nothing serves them, so it is never hidden.
// An amber dot, what happened and why, and the move, tried again here.
const unmovedSaid = {}; // provider id → { busy } or { why }
window.unmovedLine = function (p) {
  const said = unmovedSaid[p.id];
  const why = said?.why || moveWhy(p, p);
  const b = el("button", "text action", said?.busy || (why ? t("Try again") : t("Move to the plugin")));
  b.title = p.package; // the npm package: for the curious, not the sentence
  b.disabled = b.classList.toggle("busy", !!said?.busy);
  b.onclick = async () => {
    unmovedSaid[p.id] = { busy: t("Installing the plugin and checking each account…") };
    renderMovable();
    try {
      providers = await api("provider/move", { id: p.id });
      delete unmovedSaid[p.id];
      renderProviders();
      await movedSay(p);
    } catch (e) {
      unmovedSaid[p.id] = { why: moveWhy({ why: e.why, error: e.message }, p) };
      renderMovable();
    }
  };
  const card = depLine("unmoved", t("{name}'s built-in subscription is retired, and its accounts haven't moved to its plugin yet", { name: p.name }), why ? t("Nothing serves them until they move: {error}", { error: why }) : t("Nothing serves them until they move. Moving keeps your accounts, models and agents as they are."), b);
  card.dataset.id = p.id;
  return card;
};
