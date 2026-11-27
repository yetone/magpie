// A key's pencil, Change key (#1869): its new value pasted in place of
// the masked one, Enter posting keys/replace, Escape leaving it. Moved out
// of app.js, which imports this when the pencil (or a double-click on the
// row) is first used, so it weighs nothing on the core.
//
// Under it, when the provider reads balances (p.keyBalanceAuth), an
// Advanced fold with the key's own account's User ID and access token
// (Kylin_Cloud on X: one relay, keys of two accounts, both showing one
// account's balance, the provider's token being that account's). They
// are posted to keys/balance and read in place of the provider's for this
// key's balance (provider/key_balance_auth.go); a saved token is never
// sent back, only that there is one. Changed by few, so folded, open
// when the key has them.
window.keyEdit = (row, p, k, name) => {
  if (row.querySelector(".key-value-in")) return;
  const i = input("", t("Paste the key's new value"), "password");
  i.className = "rename-in key-value-in";
  let over = false;
  const done = (save) => {
    if (over) return;
    over = true;
    if (save && i.value.trim()) accountAction("keys/replace", { id: p.id, ref: k.id, key: i.value }, t("{key} now uses its new value", { key: k.name || k.masked }));
    else renderProviders();
  };
  const adv = p.keyBalanceAuth && balanceAuth(p, k, () => done(false));
  i.onkeydown = (ev) => { ev.stopPropagation(); if (ev.key === "Enter") done(true); else if (ev.key === "Escape") done(false); };
  // focus going into the fold leaves the box open
  i.onblur = (ev) => (adv && adv.contains(ev.relatedTarget)) || done(!!i.value.trim());
  const shown = row.querySelector(".plan.mono");
  if (shown) shown.replaceWith(i);
  else name.after(i);
  if (adv) row.append(adv);
  i.focus(NO_SCROLL);
};

function balanceAuth(p, k, cancel) {
  const a = k.balanceAuth, who = k.name || k.masked;
  const [box, inner] = advFold({ cls: "key-bal-auth", editor: true, open: !!a });
  const user = input(a?.user || "", t("User ID"));
  user.inputMode = "numeric";
  user.setAttribute("aria-label", t("User ID"));
  const tok = input("", t(a ? "Access token saved" : "Access token"), "password");
  tok.setAttribute("aria-label", t("Access token"));
  tok.title = t("The access token of the account this key belongs to, from the site's personal settings: this key's balance is read with it");
  const save = el("button", "text primary", t("Save"));
  const go = () => {
    if (!tok.value.trim() && !a) return tok.focus(NO_SCROLL);
    save.classList.add("busy");
    accountAction("keys/balance", { id: p.id, ref: k.id, token: tok.value, user: user.value }, t("{key}'s balance is read with its own access token", { key: who }));
  };
  save.onclick = go;
  for (const f of [user, tok]) f.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") go(); else if (e.key === "Escape") cancel(); };
  inner.append(user, tok, save);
  if (a) {
    const rm = el("button", "text quiet", t("Remove"));
    rm.onclick = () => accountAction("keys/balance", { id: p.id, ref: k.id, clear: true }, t("{key}'s balance is read as the provider's", { key: who }));
    inner.append(rm);
  }
  // a click on the fold's summary or buttons keeps the focus where it is,
  // so the key's box doesn't close under it; its fields take it
  box.onmousedown = (e) => e.target.closest("input") || e.preventDefault();
  return box;
}
