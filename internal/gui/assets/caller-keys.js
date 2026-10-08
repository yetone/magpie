// Caller keys use the same account list and edit controls as provider keys.
let gatewayKeys = null, gatewayKeyDraft = null, gatewayKeysBusy = false;
// gatewayOwnDraft is the value typed for a new key of the user's own (one
// its clients already send, love1sbug on X); "" lets magpie make one
let gatewayOwnDraft = "";
// gatewayAccountNames maps a key's stored account entry ("<provider>/<id>",
// #905) to how it is shown; what no provider has now is shown as it is kept
let gatewayAccountNames = {};
let gatewayKeysRead = 0;
// gatewayLimit is the key whose limit editor is open, and its draft
// (#585): what is picked in it is sent only by its Save.
let gatewayLimit = null;
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
  if (!id || !providers?.gateway.lan) return;
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
    gatewayAccountNames = out.accountNames || gatewayAccountNames;
    gatewayKeyDraft = null;
    if (action === "limit-key" || !gatewayKeys.some((k) => k.id === gatewayLimit?.id)) gatewayLimit = null;
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
      // Keep the other controls in place when blur precedes their click.
      i.replaceWith(b);
      if (save && i.value.trim() !== k.name) await gatewayKeyAction("rename-key", { key: k.id, name: i.value });
    };
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") done(true); else if (e.key === "Escape") done(false); };
    i.onblur = () => done(true);
    b.replaceWith(i);
    i.focus({ preventScroll: true });
  };
  return b;
}

function gatewayKeyForm() {
  const box = el("div", "acc adding");
  const name = input(gatewayKeyDraft, t("Key name, e.g. Laptop"));
  name.setAttribute("aria-label", t("Gateway key name"));
  name.oninput = () => { gatewayKeyDraft = name.value; };
  const own = input(gatewayOwnDraft, t("Your own key (optional)"), "password");
  own.setAttribute("aria-label", t("Gateway key value"));
  own.title = t("Leave empty and magpie makes a key. Or enter one your clients already send, from another gateway, so they keep working.");
  own.autocomplete = "off";
  own.oninput = () => { gatewayOwnDraft = own.value; };
  const add = el("button", "text primary", t("Create"));
  const go = async () => {
    add.disabled = true;
    const mine = own.value.trim() !== "";
    const out = await gatewayKeyAction("add-key", mine ? { name: name.value, secret: own.value } : { name: name.value });
    if (!out) { add.disabled = false; return; }
    gatewayOwnDraft = "";
    status(t(mine ? "Gateway key created. Clients that send this key reach magpie now." : "Gateway key created. Use Copy on its row to connect a client."), "ok");
  };
  add.onclick = go;
  const cancelled = () => { gatewayKeyDraft = null; gatewayOwnDraft = ""; renderGatewayKeys(); };
  for (const i of [name, own]) i.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && !add.disabled) go();
    else if (e.key === "Escape") cancelled();
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelled;
  const fields = el("div", "kf");
  fields.append(name, own);
  const bar = el("div", "kb");
  bar.append(el("span", "grow"), cancel, add);
  box.append(fields, bar);
  queueMicrotask(() => name.focus({ preventScroll: true }));
  return box;
}

function copyCallerKeyBtn(k) {
  const cp = copyBtn("", t("Gateway key"));
  cp.setAttribute("aria-label", t("Copy gateway key"));
  cp.onclick = async () => {
    try {
      const out = await api("caller-keys/copy-key", { key: k.id });
      await copy(out.secret, t("Gateway key"), cp);
    } catch (e) { status(t(e.message), "err"); }
  };
  return cp;
}

function renderGatewayKeys() {
  const box = $("#gatewayKeys");
  $("#gatewayKeysBlock").hidden = !providers?.gateway.lan;
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
    rm.onclick = () => askGatewayKey(k, false);
    const rotate = el("button", "text quiet", t("Rotate key"));
    rotate.onclick = () => askGatewayKey(k, true);
    row.append(tick, gatewayRename(k), el("span", "plan mono", k.masked), el("span", "grow"), gatewayModelsBadge(k), gatewayLimitBadge(k), copyCallerKeyBtn(k), rotate, rm);
    const under = gatewayLimit?.id === k.id ? gatewayLimitEditor(k) : k.used ? gatewayLimitLine(k) : null;
    if (under) row.classList.add("with-limit"), row.append(under);
    list.append(row);
  }
  if (gatewayKeyDraft !== null) list.append(gatewayKeyForm());
  else if (!gatewayKeys.length) list.append(el("div", "empty-state", t("No gateway keys yet")));
  box.append(list);
}

function askGatewayKey(k, rotate) {
  const ed = el("div", "editor");
  const head = el("div", "ehead");
  head.append(el("b", "", t(rotate ? "Rotate gateway key?" : "Remove gateway key?")));
  ed.append(head, el("p", "lib-confirm", t(rotate
    ? "Clients using {name} will need the new key. Its name and usage history stay the same."
    : "Clients using {name} will lose access from other computers. Its usage history is kept.", { name: k.name })));
  const bar = el("div", "bar");
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
  const go = el("button", "text primary", t(rotate ? "Rotate key" : "Remove"));
  go.onclick = async (e) => {
    e.stopPropagation();
    if (go.disabled) return;
    go.disabled = true;
    const out = await gatewayKeyAction(rotate ? "rotate-key" : "remove-key", { key: k.id });
    if (!out) { go.disabled = false; return; }
    closeConfirmAsk();
    if (rotate) status(t("Gateway key rotated. Update clients on other computers."), "ok");
  };
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  cancel.focus({ preventScroll: true });
}

// ---- the models a gateway key may use (#882), and the accounts (#905) ----

// accountShown is a stored account entry ("<provider>/<id>", #905) as the
// badge says it: by who is signed in, the id itself when no provider has it.
const accountShown = (a) => gatewayAccountNames[a] || a;

// the key whose models menu waits on its list: the badge drawn last opens
// it, so a redraw of the keys while it loads (a rename's reply, the
// providers' poll) doesn't drop the click
let keyModelsLoading = null;

// gatewayModelsBadge picks the models and the accounts a key may use, in
// the app's menu: "All models" on hover when it may use any, else which
// it may, always shown. A pick is sent when the menu closes.
function gatewayModelsBadge(k) {
  const ms = k.models || [];
  const as = k.accounts || [];
  const b = el("button", "amodels key-models" + (ms.length || as.length ? " set" : ""));
  b.type = "button";
  b.textContent = !ms.length && !as.length ? t("All models")
    : ms.length ? (ms.length === 1 && !as.length ? ms[0] : t("{n} models", { n: ms.length }))
      : (as.length === 1 ? accountShown(as[0]) : t("{n} accounts", { n: as.length }));
  b.title = (ms.length ? t("Only these models: {list}", { list: ms.join(", ") }) : t("Pick the models this key may use"))
    + (as.length ? "\n" + t("Only these accounts: {list}", { list: as.map(accountShown).join(", ") }) : "");
  b.setAttribute("aria-label", t("Models this key may use"));
  b.setAttribute("aria-haspopup", "menu");
  b.setAttribute("aria-expanded", "false");
  const open = (models, accounts) => {
    if (!b.isConnected) return;
    const opts = [{ v: "", name: "All models", note: "Any model and any account, now and later" }];
    const seen = new Set();
    // the accounts and keys the key may be held to (#905), each right
    // after its provider's models, not one block at the end: an account
    // by who is signed in, a key by its fingerprint
    const accountsSet = new Set();
    const ofProvider = new Map();
    for (const a of accounts || []) {
      accountsSet.add(a.id);
      const o = { v: a.id, name: a.name, note: a.plan ? a.providerName + " · " + a.plan : a.providerName, literalName: true };
      ofProvider.set(a.provider, [...(ofProvider.get(a.provider) || []), o]);
    }
    let listed = "";  // the provider whose models the list is in
    const endBlock = () => {
      for (const o of ofProvider.get(listed) || []) opts.push(o);
      ofProvider.delete(listed);
      listed = "";
    };
    for (const m of models || []) {
      // a routing group the key names is its with every member in it
      // (Magic_zero on Discord); the groups come first
      if (m.group) {
        endBlock();
        if (!seen.has("group")) {
          seen.add("group");
          opts.push({ v: "group/*", name: "Every routing group", note: "group/*" });
        }
        opts.push({ v: m.id, name: m.name, note: m.id, literalName: true });
        continue;
      }
      if (m.provider !== listed) {
        endBlock();
        listed = m.provider;
      }
      if (!seen.has(m.provider)) {
        seen.add(m.provider);
        opts.push({ v: m.provider + "/*", name: t("Every {provider} model", { provider: m.providerName }), note: m.provider + "/*", literalName: true });
      }
      opts.push({ v: m.id, name: m.name, note: m.id, literalName: true });
    }
    endBlock();
    // a provider with accounts and no model in the list (one kept
    // unlisted, or serving none now) keeps its accounts after the models
    for (const os of ofProvider.values()) opts.push(...os);
    // what the CLI kept that the list hasn't, a pattern, a model gone or
    // an account signed out: behind every live entry, a model gone
    // before an account signed out
    for (const v of ms) if (!opts.some((o) => o.v === v)) opts.push({ v, name: v, note: "Not served now", literalName: true });
    for (const v of as) if (!accountsSet.has(v)) { accountsSet.add(v); opts.push({ v, name: v, note: "Not signed in now", literalName: true }); }
    openProtoMenu(b, opts, [...ms, ...as], (picked) => {
      // one action at a time: gatewayKeyAction is busy while one is sent.
      // The accounts are sent only when they changed: a model picked says
      // nothing of the key's accounts.
      (async () => {
        const pickedModels = picked.filter((v) => !accountsSet.has(v));
        const pickedAccounts = picked.filter((v) => accountsSet.has(v));
        const out = await gatewayKeyAction("models-key", { key: k.id, models: pickedModels });
        if (out) status(t(pickedModels.length ? "{name} may use only the models picked" : "{name} may use every model", { name: k.name }), "ok");
        if (pickedAccounts.length !== as.length || pickedAccounts.some((v) => !as.includes(v))) {
          await gatewayKeyAction("accounts-key", { key: k.id, accounts: pickedAccounts });
        }
      })();
    }, "Models this key may use", "sess-menu", "right");
  };
  b.onclick = async (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    const loading = keyModelsLoading = { id: k.id, open };
    let models = [], accounts = [];
    try { ({ models, accounts } = await api("caller-keys/models")); } catch (err) {
      if (keyModelsLoading === loading) keyModelsLoading = null;
      status(t(err.message), "err");
      return;
    }
    if (keyModelsLoading !== loading) return;
    keyModelsLoading = null;
    loading.open(models, accounts);
  };
  if (keyModelsLoading?.id === k.id) keyModelsLoading.open = open;
  return b;
}

// ---- a gateway key's own limit (#585) ----

const limitPeriods = ["day", "week", "month"];
const limitPer = { day: "per day", week: "per week", month: "per month" };

// limitTokens reads a count as typed: 2000000, 2,000,000, 500k, 2M, 1.5m,
// 100万, 1亿; "" for none, NaN for what isn't one.
function limitTokens(v) {
  const s = String(v ?? "").trim().toLowerCase().replace(/[,\s_]/g, "");
  if (!s) return 0;
  const m = s.match(/^(\d+(?:\.\d+)?)(k|m|b|万|亿)?$/);
  if (!m) return NaN;
  const mult = { k: 1e3, m: 1e6, b: 1e9, "万": 1e4, "亿": 1e8 }[m[2]] || 1;
  return Math.round(parseFloat(m[1]) * mult);
}
function limitCost(v) {
  const s = String(v ?? "").trim().replace(/^[$＄]/, "");
  if (!s) return 0;
  return /^\d+(\.\d+)?$/.test(s) ? parseFloat(s) : NaN;
}
const usd = (n) => "$" + (n >= 100 ? n.toFixed(0) : n.toFixed(2));

// gatewayLimitBadge opens a key's limit editor: "Limit" on hover when it
// has none, else what it has used, "1.2M / 2M · day", always shown.
function gatewayLimitBadge(k) {
  const u = k.used;
  const b = el("button", "amodels limit" + (u ? " set" : "") + (u?.spent ? " spent" : "") + (gatewayLimit?.id === k.id ? " open" : ""));
  b.type = "button";
  if (!u) b.textContent = t("Limit");
  else {
    const parts = [];
    if (u.tokenLimit) parts.push(fmtN(u.tokens) + " / " + fmtN(u.tokenLimit));
    if (u.costLimit) parts.push(usd(u.cost) + " / " + usd(u.costLimit));
    b.textContent = (u.spent ? t("Spent") + " · " : "") + parts.join(" · ") + " · " + t(limitPer[u.period] || u.period);
  }
  b.title = t(u ? "Change this key's limit" : "Set a limit for this key");
  b.setAttribute("aria-label", b.title);
  b.setAttribute("aria-expanded", gatewayLimit?.id === k.id ? "true" : "false");
  b.onclick = (e) => {
    e.preventDefault();
    if (gatewayLimit?.id === k.id) gatewayLimit = null;
    else {
      const l = k.limit || {};
      gatewayLimit = { id: k.id, period: l.period || "day", tokens: l.tokens ? String(l.tokens) : "", cost: l.cost ? String(l.cost) : "", cacheReads: !!l.cacheReads };
    }
    renderGatewayKeys();
  };
  return b;
}

// gatewayLimitLine says what a limited key has used, has left and when
// it resets, under its row.
function gatewayLimitLine(k) {
  const u = k.used;
  const line = el("div", "key-limit" + (u.spent ? " spent" : ""));
  const parts = [];
  if (u.tokenLimit) parts.push(t("{used} of {limit} tokens · {left} left", { used: fmtN(u.tokens), limit: fmtN(u.tokenLimit), left: fmtN(u.tokensLeft) }));
  if (u.costLimit) parts.push(t("{used} of {limit} estimated · {left} left", { used: usd(u.cost), limit: usd(u.costLimit), left: usd(u.costLeft) }));
  const at = new Date(u.reset);
  parts.push(t(u.spent ? "refused until {when}" : "resets {when}", { when: resetClock(at) }));
  if (u.inFlight) parts.push(t(u.inFlight === 1 ? "1 in flight" : "{n} in flight", { n: u.inFlight }));
  line.textContent = parts.join(" · ");
  line.title = t("Counted from {start}; resets {when}", { start: new Date(u.start).toLocaleString(), when: at.toLocaleString() })
    + (u.unpriced ? "\n" + t("{n} calls without a known price aren't in the cost", { n: u.unpriced }) : "");
  return line;
}

// gatewayLimitEditor stages a key's limit: nothing is sent until Save.
function gatewayLimitEditor(k) {
  const d = gatewayLimit;
  const box = el("div", "acct-models key-limit-ed");
  box.onclick = (e) => e.stopPropagation();
  const kept = k.limit || {};
  const changed = () => d.period !== (kept.period || "day") || limitTokens(d.tokens) !== (kept.tokens || 0)
    || limitCost(d.cost) !== (kept.cost || 0) || d.cacheReads !== !!kept.cacheReads;
  const fields = el("div", "klf");
  // the app's own menu, not a native select
  const period = el("button", "sess-pick klf-period");
  period.type = "button";
  period.setAttribute("aria-label", t("Limit window"));
  period.setAttribute("aria-haspopup", "menu");
  period.setAttribute("aria-expanded", "false");
  const paintPeriod = () => {
    period.dataset.value = d.period;
    period.replaceChildren(el("span", "", t(limitPer[d.period])), svg(CHEV, 11, 1.6));
  };
  paintPeriod();
  const tokens = input(d.tokens, t("No token cap"));
  tokens.setAttribute("aria-label", t("Token limit"));
  tokens.inputMode = "decimal";
  const cost = input(d.cost, t("No cost cap"));
  cost.setAttribute("aria-label", t("Estimated cost limit, US$"));
  cost.inputMode = "decimal";
  const tl = el("label", "klf-field");
  tl.append(el("span", "klf-name", t("Tokens")), tokens);
  const cl = el("label", "klf-field");
  cl.append(el("span", "klf-name", t("Estimated cost, US$")), cost);
  const pl = el("label", "klf-field");
  pl.append(el("span", "klf-name", t("Window")), period);
  fields.append(pl, tl, cl);
  const cache = el("label", "klf-check");
  const cb = el("input");
  cb.type = "checkbox";
  cb.checked = d.cacheReads;
  cache.append(cb, el("span", "", t("Count cache reads too")));
  const hint = el("div", "hint", t("Tokens are each call's uncached input, output and cache writes. The cost is an estimate at the Usage page's prices, not a bill. Windows are calendar ones in this computer's time (a week from Monday). Requests from this computer without a key aren't limited."));
  const err = el("div", "hint klf-err");
  const unsaved = el("span", "hint munsaved", t("unsaved"));
  unsaved.title = t("Made when you Save; Cancel drops it");
  const save = el("button", "text primary", t("Save"));
  const update = () => {
    const bad = Number.isNaN(limitTokens(d.tokens)) ? t("Tokens is a count, like 2000000 or 2M")
      : Number.isNaN(limitCost(d.cost)) ? t("The cost is US dollars, like 5 or 2.50") : "";
    err.textContent = bad;
    err.hidden = !bad;
    unsaved.hidden = !changed();
    save.disabled = !!bad || !changed();
  };
  period.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (period.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(period, limitPeriods.map((p) => ({ v: p, name: t(limitPer[p]), note: "", literalName: true })), d.period, (v) => {
      d.period = v;
      paintPeriod();
      update();
    }, "Limit window", "sess-menu");
  };
  tokens.oninput = () => { d.tokens = tokens.value; update(); };
  cost.oninput = () => { d.cost = cost.value; update(); };
  cb.onchange = () => { d.cacheReads = cb.checked; update(); };
  const close = () => { gatewayLimit = null; renderGatewayKeys(); };
  for (const i of [tokens, cost]) i.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Escape") close();
    else if (e.key === "Enter" && !save.disabled) save.click();
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = close;
  save.onclick = async () => {
    if (save.disabled) return;
    save.disabled = true;
    const tk = limitTokens(d.tokens), c = limitCost(d.cost);
    const limit = tk || c ? { period: d.period, tokens: tk, cost: c, cacheReads: d.cacheReads } : null;
    const out = await gatewayKeyAction("limit-key", { key: k.id, limit });
    if (!out) { update(); return; }
    status(t(limit ? "Limit saved for {name}" : "Limit removed for {name}", { name: k.name }), "ok");
  };
  const foot = el("div", "acm-foot");
  foot.append(cache, el("span", "grow"), unsaved);
  if (k.limit) {
    const off = el("button", "text", t("No limit"));
    off.title = t("Clears the caps; Save makes it");
    off.onclick = () => { d.tokens = ""; d.cost = ""; tokens.value = ""; cost.value = ""; update(); };
    foot.append(off);
  }
  foot.append(cancel, save);
  box.append(fields, hint, err);
  if (k.used) box.append(gatewayLimitLine(k));
  box.append(foot);
  update();
  return box;
}
