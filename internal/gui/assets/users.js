// Caller keys use the same account list and edit controls as provider keys.
let gatewayKeys = null, gatewayKeyDraft = null, gatewayKeysBusy = false;
let gatewayKeysRead = 0;
let connectURL = "", connectKeyID = "", connectSecret = null;

function connectPick(id, label, text, options, value, choose) {
  const b = el("button", "val");
  b.id = id;
  b.type = "button";
  b.setAttribute("aria-label", t(label));
  b.append(el("code", "", text), svg(CHEV, 11, 1.6));
  b.onclick = () => {
    if (b.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(b, options, value, choose, label);
  };
  return b;
}

async function selectConnectKey(id) {
  connectKeyID = id;
  connectSecret = null;
  renderConnect();
  if (!id) return;
  const key = gatewayKeys?.find((k) => k.id === id && !k.off);
  if (!key) return;
  try {
    const out = await api("caller-keys/copy-key", { key: id });
    if (connectKeyID !== id || gatewayKeys?.find((k) => k.id === id) !== key) return;
    connectSecret = { id, secret: out.secret };
    if (view === "gateway") renderConnect();
  } catch (e) { status(t(e.message), "err"); }
}
async function loadGatewayKeys() {
  const read = ++gatewayKeysRead;
  try {
    const out = await api("caller-keys");
    if (read !== gatewayKeysRead) return;
    // A fresh list also refreshes a selected credential, which may have rotated.
    connectSecret = null;
    gatewayKeys = out.keys || [];
    renderGatewayKeys();
    if (view === "gateway" && providers) selectConnectKey(connectKeyID);
  } catch (e) {
    if (read !== gatewayKeysRead) return;
    $("#gatewayKeys").replaceChildren(el("div", "none", t(e.message)));
  }
}

async function gatewayKeyAction(action, body) {
  if (gatewayKeysBusy) return null;
  gatewayKeysBusy = true;
  gatewayKeysRead++;
  try {
    const out = await api("caller-keys/" + action, body);
    gatewayKeysRead++;
    connectSecret = null;
    gatewayKeys = out.keys || [];
    gatewayKeyDraft = null;
    renderGatewayKeys();
    if (view === "gateway" && providers) selectConnectKey(connectKeyID);
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
  name.setAttribute("aria-label", t("API key name"));
  name.oninput = () => { gatewayKeyDraft = name.value; };
  const add = el("button", "text primary", t("Create key"));
  const go = async () => {
    add.disabled = true;
    const out = await gatewayKeyAction("add-key", { name: name.value });
    if (!out) { add.disabled = false; return; }
    status(t("API key created. Use Copy on its row to connect a client."), "ok");
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

function copyCallerKeyBtn(k) {
  const cp = copyBtn("", t("API key"));
  cp.setAttribute("aria-label", t("Copy API key"));
  cp.onclick = async () => {
    try {
      const out = await api("caller-keys/copy-key", { key: k.id });
      await copy(out.secret, t("API key"), cp);
    } catch (e) { status(t(e.message), "err"); }
  };
  return cp;
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
    const rm = el("button", "text quiet", t("Remove"));
    rm.onclick = () => gatewayKeyAction("remove-key", { key: k.id });
    const rotate = el("button", "text quiet", t("New key"));
    rotate.onclick = async () => {
      if (await gatewayKeyAction("rotate-key", { key: k.id })) status(t("API key rotated. The old key no longer works."), "ok");
    };
    row.append(tick, gatewayRename(k), el("span", "plan mono", k.masked), el("span", "grow"), copyCallerKeyBtn(k), rotate, rm);
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
