// The line over the Providers list moving the withdrawn Antigravity
// plugin's accounts to Antigravity (local agy CLI). app.js imports this
// when /api/providers names any (agyMove), so it weighs nothing on the
// core for everyone else.

// agyMoveLine: the withdrawn Antigravity plugin's accounts, which Antigravity
// (local agy CLI) takes as they are (one Google sign-in for both), moved
// with one click (@tongzhenbao on X). Not now hides it until the accounts
// left to move change; an account agy refused is said by name.
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
    agySaid?.why || t("Google suspends accounts it sees used outside its own app. agy signs in with the same Google account, so {users} move as they are.", { users: named.length ? named.join(t(", ")) : t("{n} accounts", { n: users.length }) }), hide, go);
};
