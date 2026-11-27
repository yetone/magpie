// The lines over the Providers list about the withdrawn Antigravity
// plugin: moving its accounts to Antigravity (local agy CLI), and the
// plugin itself still on. app.js imports this when /api/providers names
// any (agyMove, withdrawn), so it weighs nothing on the core for everyone
// else.

// agyMoveLine: the withdrawn Antigravity plugin's accounts, which Antigravity
// (local agy CLI) takes as they are (one Google sign-in for both), moved
// with one click (@tongzhenbao on X). Not now hides it until the accounts
// left to move change; an account agy refused is said by name. Google can
// suspend an agy-cli account too (WdBlink_ on X), so while it has no
// account of its own the line says AGY_RISK, the sign-in's risk step, before
// Move is clicked.
let agySaid = null; // { busy } or { why }
window.agyMoveLine = function () {
  const users = providers.agyMove || [];
  if (!users.length) return null;
  const ids = users.join(",");
  let hid = "";
  try { hid = localStorage.getItem("magpie.agyMoveHidden") || ""; } catch {}
  if (hid === ids && !agySaid) return null;
  const named = users.filter(Boolean);
  const hide = el("button", "text", t("Not now"));
  hide.onclick = () => { try { localStorage.setItem("magpie.agyMoveHidden", ids); } catch {} agySaid = null; renderMovable(); };
  const go = el("button", "text action", agySaid?.busy || (agySaid?.why ? t("Try again") : t("Move")));
  go.disabled = go.classList.toggle("busy", !!agySaid?.busy);
  go.onclick = async () => {
    agySaid = { busy: t("Installing agy and checking each account…") };
    renderMovable();
    try {
      providers = await api("provider/toagy", {});
      const res = providers.agyMoved || [];
      const failed = res.filter((r) => r.status === "failed");
      const ok = res.length - failed.length;
      agySaid = failed.length ? { why: failed.map((r) => t("{user}: {error}", { user: r.user, error: r.error })).join(" · ") } : null;
      renderProviders();
      if (ok) status(t("{n} moved to Antigravity (local agy CLI)", { n: ok }), failed.length ? "warn" : "ok");
    } catch (e) {
      agySaid = { why: e.message };
      renderMovable();
    }
  };
  return depLine("agy-move", t("Antigravity's accounts can move to Antigravity (local agy CLI)"),
    agySaid?.why || [t("{users} move as they are.", { users: named.length ? named.join(t(", ")) : t("{n} accounts", { n: users.length }) }), hasLogins("agy-cli") ? "" : t(AGY_RISK)].filter(Boolean).join(/^(zh|ja)/.test(locale) ? "" : " "), hide, go);
};

// withdrawnLine: a withdrawn plugin still on. Beside the plugin to use
// instead, which has every account it has (the owner's screenshot, 11-21:
// agy-cli signed in, still told to use it), its requests still go out
// through the withdrawn one, so the line says so and turns it off; off,
// it is kept with its sign-ins and can be turned on again from Plugins.
// Otherwise it says the risk and leads to the plugin to use instead.
window.withdrawnLine = function (w) {
  if (!w.beside) {
    const go = el("button", "text action", t("Use {name}", { name: w.instead }));
    go.onclick = () => openPlugins("agy");
    return depLine("withdrawn", t("{name}'s plugin can get your account suspended", { name: w.name }), t("{name} runs your own agy, but Google may still suspend an account it sees used from another agent.", { name: w.instead }), go);
  }
  const go = el("button", "text action", t("Turn off {name}'s plugin", { name: w.name }));
  go.onclick = async () => {
    go.disabled = true;
    try {
      await api("plugins/off", { spec: w.spec, off: true });
      providers = await api("providers");
      renderProviders();
    } catch (e) {
      go.disabled = false;
      status(e.message, "err");
    }
  };
  return depLine("withdrawn", t("{name}'s plugin is still on beside {instead}", { name: w.name, instead: w.instead }),
    t("{instead} has your accounts now, so {name}'s plugin can be turned off.", { name: w.name, instead: w.instead }), go);
};
