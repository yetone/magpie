// Caller keys use the same account list and edit controls as provider keys.
let gatewayKeys = null, gatewayKeyDraft = null, gatewayKeysBusy = false;
async function loadGatewayKeys() {
  try {
    const out = await api("caller-keys");
    gatewayKeys = out.keys || [];
    renderGatewayKeys();
    if (view === "gateway" && providers) renderConnect();
  } catch (e) {
    $("#gatewayKeys").replaceChildren(el("div", "none", t(e.message)));
  }
}

async function gatewayKeyAction(action, body) {
  if (gatewayKeysBusy) return null;
  gatewayKeysBusy = true;
  try {
    const out = await api("caller-keys/" + action, body);
    gatewayKeys = out.keys || [];
    gatewayKeyDraft = null;
    renderGatewayKeys();
    if (view === "gateway" && providers) renderConnect();
    return out;
  } catch (e) {
    status(t(e.message), "err");
    return null;
  } finally {
    gatewayKeysBusy = false;
  }
}

function gatewayRename(k) {
  const b = el("button", "n rename", k.name);
  b.title = t("Rename");
  b.onclick = () => {
    const i = input(k.name, t("Name"));
    i.className = "rename-in";
    i.setAttribute("aria-label", t("Name"));
    let finished = false;
    const done = async (save) => {
      if (finished) return;
      finished = true;
      if (save && i.value.trim() !== k.name) await gatewayKeyAction("rename-key", { key: k.id, name: i.value });
      renderGatewayKeys();
    };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") done(true); else if (e.key === "Escape") done(false); };
    i.onblur = () => done(true);
    b.replaceWith(i);
    i.focus();
  };
  return b;
}

function gatewayKeyForm() {
  const box = el("div", "acc adding");
  const name = input(gatewayKeyDraft, t("Key name, e.g. Laptop"));
  name.setAttribute("aria-label", t("Caller key name"));
  name.oninput = () => { gatewayKeyDraft = name.value; };
  const add = el("button", "text primary", t("Create key"));
  const go = async () => {
    add.disabled = true;
    const out = await gatewayKeyAction("add-key", { name: name.value });
    if (!out) { add.disabled = false; return; }
    status(t("Key created. Use Copy on its row to connect a client."), "ok");
  };
  add.onclick = go;
  name.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && !add.disabled) go();
    else if (e.key === "Escape") { gatewayKeyDraft = null; renderGatewayKeys(); }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = () => { gatewayKeyDraft = null; renderGatewayKeys(); };
  const fields = el("div", "kf");
  fields.append(name);
  const bar = el("div", "kb");
  bar.append(el("span", "grow"), cancel, add);
  box.append(fields, bar);
  queueMicrotask(() => name.focus());
  return box;
}

function renderGatewayKeys() {
  const box = $("#gatewayKeys");
  box.replaceChildren();
  $("#addGatewayKey").onclick = () => { gatewayKeyDraft = ""; renderGatewayKeys(); };
  if (gatewayKeys === null) return;
  const list = el("div", "accts");
  for (const k of gatewayKeys) {
    const row = el("div", "acc" + (k.off ? " off" : " in-use"));
    row.dataset.key = k.id;
    const tick = el("button", "dot tick");
    tick.title = t(k.off ? "Enable key" : "Disable key");
    tick.setAttribute("aria-label", tick.title);
    if (!k.off) tick.append(svg(CHECK, 10, 2.2));
    tick.onclick = () => gatewayKeyAction(k.off ? "on-key" : "off-key", { key: k.id });
    const cp = copyBtn("", t("Caller key"));
    cp.setAttribute("aria-label", t("Copy caller key"));
    cp.onclick = async () => {
      try {
        const out = await api("caller-keys/copy-key", { key: k.id });
        await copy(out.secret, t("Caller key"), cp);
      } catch (e) { status(t(e.message), "err"); }
    };
    const rm = el("button", "text quiet", t("Remove"));
    rm.onclick = () => gatewayKeyAction("remove-key", { key: k.id });
    row.append(tick, gatewayRename(k), el("span", "plan mono", k.masked), el("span", "grow"), cp, rm);
    list.append(row);
  }
  if (gatewayKeyDraft !== null) list.append(gatewayKeyForm());
  else {
    const add = el("button", "acc add", t("＋ Create key"));
    add.onclick = () => { gatewayKeyDraft = ""; renderGatewayKeys(); };
    list.append(add);
  }
  box.append(list);
}
