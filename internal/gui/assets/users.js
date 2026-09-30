// Gateway users are callers, independent of the vendors' upstream accounts.
let gatewayUsers = null, gatewayUserDraft = null, gatewayUsersBusy = false;
async function loadGatewayUsers() {
  try {
    const out = await api("users");
    gatewayUsers = out.users || [];
    renderGatewayUsers();
    if (view === "gateway" && providers) renderConnect();
  } catch (e) {
    $("#gatewayUsers").replaceChildren(el("div", "none", t(e.message)));
  }
}

async function gatewayUserAction(action, body) {
  if (gatewayUsersBusy) return null;
  gatewayUsersBusy = true;
  try {
    const out = await api("users/" + action, body);
    gatewayUsers = out.users || [];
    gatewayUserDraft = null;
    renderGatewayUsers();
    if (view === "gateway" && providers) renderConnect();
    return out;
  } catch (e) {
    status(t(e.message), "err");
    return null;
  } finally {
    gatewayUsersBusy = false;
  }
}

function gatewayRename(name, action, body) {
  const b = el("button", "n rename", name);
  b.title = t("Rename");
  b.onclick = () => {
    const i = input(name, t("Name"));
    i.className = "rename-in";
    i.setAttribute("aria-label", t("Name"));
    let finished = false;
    const done = async (save) => {
      if (finished) return;
      finished = true;
      if (save && i.value.trim() !== name) await gatewayUserAction(action, { ...body, name: i.value });
      renderGatewayUsers();
    };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") done(true); else if (e.key === "Escape") done(false); };
    i.onblur = () => done(true);
    b.replaceWith(i);
    i.focus();
  };
  return b;
}

function gatewayKeyForm(user) {
  const box = el("div", "acc adding");
  const name = input(gatewayUserDraft.name, t(user ? "Key name, e.g. Laptop" : "User name, e.g. Alice"));
  name.setAttribute("aria-label", t(user ? "Caller key name" : "User name"));
  name.oninput = () => { gatewayUserDraft.name = name.value; };
  const add = el("button", "text primary", t(user ? "Create key" : "Create user"));
  const go = async () => {
    add.disabled = true;
    const out = await gatewayUserAction(user ? "add-key" : "add-user", { user: user?.id || "", name: name.value });
    if (!out) { add.disabled = false; return; }
    if (out.secret) {
      status(t("Key created. Use Copy on its row to connect a client."), "ok");
    }
  };
  add.onclick = go;
  name.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && !add.disabled) go();
    else if (e.key === "Escape") { gatewayUserDraft = null; renderGatewayUsers(); }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = () => { gatewayUserDraft = null; renderGatewayUsers(); };
  const fields = el("div", "kf");
  fields.append(name);
  const bar = el("div", "kb");
  bar.append(el("span", "grow"), cancel, add);
  box.append(fields, bar);
  queueMicrotask(() => name.focus());
  return box;
}

function renderGatewayUsers() {
  const box = $("#gatewayUsers");
  box.replaceChildren();
  const begin = (user) => { gatewayUserDraft = { user: user?.id || "", name: "" }; renderGatewayUsers(); };
  $("#addGatewayUser").onclick = () => begin(null);
  if (gatewayUsers === null) return;
  if (!gatewayUsers.length && !gatewayUserDraft) box.append(el("div", "none", t("No users yet. Create a user to issue caller keys.")));
  if (gatewayUserDraft && !gatewayUserDraft.user) {
    const list = el("div", "accts");
    list.append(gatewayKeyForm(null));
    box.append(list);
  }
  for (const u of gatewayUsers) {
    const list = el("div", "accts gateway-user" + (u.off ? " off" : ""));
    list.dataset.user = u.id;
    const head = el("div", "acc");
    const enabled = el("button", "dot tick");
    enabled.title = t(u.off ? "Enable user" : "Disable user");
    enabled.setAttribute("aria-label", enabled.title);
    if (!u.off) enabled.append(svg(CHECK, 10, 2.2));
    enabled.onclick = () => gatewayUserAction(u.off ? "on-user" : "off-user", { user: u.id });
    const remove = el("button", "text quiet", t("Remove"));
    remove.title = t("Remove user and revoke all their keys");
    remove.onclick = () => gatewayUserAction("remove-user", { user: u.id });
    head.append(enabled, gatewayRename(u.name, "rename-user", { user: u.id }), el("span", "plan", t("{n} keys", { n: u.keys.length })), el("span", "grow"), remove);
    list.append(head);
    for (const k of u.keys) {
      const row = el("div", "acc gateway-key" + (k.off || u.off ? " off" : ""));
      row.dataset.key = k.id;
      const tick = el("button", "dot tick");
      tick.title = t(k.off ? "Enable key" : "Disable key");
      tick.setAttribute("aria-label", tick.title);
      if (!k.off) tick.append(svg(CHECK, 10, 2.2));
      tick.onclick = () => gatewayUserAction(k.off ? "on-key" : "off-key", { user: u.id, key: k.id });
      const cp = copyBtn("", t("Caller key"));
      cp.setAttribute("aria-label", t("Copy caller key"));
      cp.onclick = async () => {
        try {
          const out = await api("users/copy-key", { user: u.id, key: k.id });
          await copy(out.secret, t("Caller key"), cp);
        } catch (e) { status(t(e.message), "err"); }
      };
      const rm = el("button", "text quiet", t("Remove"));
      rm.onclick = () => gatewayUserAction("remove-key", { user: u.id, key: k.id });
      row.append(tick, gatewayRename(k.name, "rename-key", { user: u.id, key: k.id }), el("span", "plan mono", k.masked), el("span", "grow"), cp, rm);
      list.append(row);
    }
    if (gatewayUserDraft?.user === u.id) list.append(gatewayKeyForm(u));
    else {
      const add = el("button", "acc add", t("＋ Create key"));
      add.onclick = () => begin(u);
      list.append(add);
    }
    box.append(list);
  }
}
