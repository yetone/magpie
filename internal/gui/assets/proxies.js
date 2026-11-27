// Settings › Network's saved proxies: their rows, the test and the form.
// app.js imports this when Settings draws them, so it weighs nothing on the core.

// { id, kind, name, url, user, hasPass, hasKey, hostKey, trust, adv, err,
// busy } while one is added or edited; kind is "proxy" or "ssh", and an
// SSH server's url is its host:port, without ssh://
let proxyForm = null;
const proxyChecks = {}; // id -> { busy, out }
const isSSH = (url) => /^ssh:\/\//i.test(url || "");
const bareSSH = (url) => (url || "").replace(/^ssh:\/\//i, "");
// a proxy's failure magpie explained (netproxy.Why) in the reader's
// language, Go's words after it
function proxyWhy(o) {
  const s = t(o.why, { addr: o.addr, was: o.was, now: o.now }), d = o.detail ?? o.error;
  return d ? s + " (" + d + ")" : s;
}
// a check's failure, said as magpie explained it when it could
const proxyFailed = (out) => (out.why ? proxyWhy(out) : out.error || "");
// the country or region a check left from, by the trace page's ISO code,
// named in the reader's language (IcyLiqu1d on X); a code Intl doesn't
// know (Cloudflare's XX, T1) as it came
function proxyRegion(loc) {
  try { return new Intl.DisplayNames([intlLang()], { type: "region" }).of(loc) || loc; } catch { return loc || ""; }
}
// a check as one line: the latency and where it left from, or why it failed
function proxyCheckLine(out) {
  if (!out.ok) return t("Unreachable: {error}", { error: proxyFailed(out) });
  return [t("Reachable, {ms} ms", { ms: out.ms }), out.loc && proxyRegion(out.loc)].filter(Boolean).join(" · ");
}
// testSavedProxy asks the server to test one saved proxy, only when a
// Test (or a Save) is clicked; then draws with what it found
function testSavedProxy(id, draw = renderSettings) {
  proxyChecks[id] = { busy: true };
  draw();
  return api("proxies/test", { id })
    .then((o) => { proxyChecks[id] = { out: o }; })
    .catch((e) => { proxyChecks[id] = { out: { ok: false, error: t(e.message) } }; })
    .finally(draw);
}
// proxyPickTest: the provider editor's Test beside its saved proxy. What
// it found is said where the picker's hint is, until another mode or
// proxy is chosen. An editor opened before Settings was read draws its
// picker again once the saved proxies are in, their names and Saved mode
// with them.
window.proxyPickTest = function (box, saved, hint) {
  if (!prefs) return void loadSettings().then(() => box.isConnected && savedProxyList().length && box.replaceWith(proxyPicker()), () => {});
  if (!savedProxyList().length || saved.nextElementSibling?.classList.contains("proxy-test")) return;
  const test = el("button", "text proxy-test", t("Test"));
  test.type = "button";
  let ref = "";
  const draw = () => {
    const chk = proxyChecks[ref.slice(1)];
    test.disabled = !!chk?.busy;
    test.textContent = chk?.busy ? t("Testing…") : t("Test");
    if (!chk?.out || draft?.proxyMode !== "saved" || draft.proxyRef !== ref) return;
    hint.textContent = hint.title = proxyCheckLine(chk.out);
    hint.classList.toggle("err", !chk.out.ok);
  };
  test.onclick = () => {
    ref = draft?.proxyRef || "";
    if (ref) testSavedProxy(ref.slice(1), draw);
  };
  // another proxy chosen (the button's name) or another mode (its
  // hidden) puts the hint back
  new MutationObserver(() => {
    hint.textContent = t(PROXY_HINT[draft?.proxyMode || ""]);
    hint.title = "";
    hint.classList.remove("err");
  }).observe(saved, { childList: true, characterData: true, subtree: true, attributeFilter: ["hidden"] });
  saved.after(test);
};
window.renderSavedProxies = function (s) {
  const box = $("#proxiesList");
  box.replaceChildren();
  const done = (ns, id) => {
    prefs = ns;
    proxyForm = null;
    // a save is tested at once, so a proxy that can't be used says why now
    if (id) testSavedProxy(id);
    else renderSettings();
  };
  for (const p of s.proxies || []) {
    if (proxyForm && proxyForm.id === p.id) { box.append(proxyFormRow(done)); continue; }
    const r = el("div", "row pref saved-proxy");
    const who = el("div", "who");
    const sub = el("div", "sub");
    who.append(el("div", "name", p.name), sub);
    const chk = proxyChecks[p.id];
    const out = chk?.out || p.check;
    const ssh = isSSH(p.url);
    const addr = ssh ? bareSSH(p.url) : p.url;
    const line = [ssh ? "SSH " + (p.user ? p.user + "@" : "") + addr : p.user ? p.user + "@" + p.url : p.url];
    if (chk?.busy) line.push(t("Testing…"));
    else if (out) line.push(proxyCheckLine(out));
    if (p.uses?.length) line.push(t("Used by {n}", { n: p.uses.length }));
    sub.textContent = line.join(" · ");
    // the exit's own address, for whoever checks it against the proxy's
    sub.title = [sub.textContent, !chk?.busy && out?.ok && out.ip].filter(Boolean).join(" · ");
    sub.classList.toggle("err", !!out && !out.ok && !chk?.busy);
    const val = el("div", "val");
    const test = el("button", "text", chk?.busy ? t("Testing…") : t("Test"));
    test.disabled = !!chk?.busy;
    test.onclick = () => testSavedProxy(p.id);
    const edit = el("button", "text", t("Edit"));
    edit.onclick = () => {
      proxyForm = { id: p.id, kind: ssh ? "ssh" : "proxy", name: p.name, url: addr, user: p.user || "", hasPass: !!p.hasPass, hasKey: !!p.hasKey, hostKey: p.hostKey || "" };
      renderSettings();
    };
    const del = el("button", "text", t("Delete"));
    del.disabled = !!p.uses?.length;
    del.onclick = () => writingPrefs(api("proxies/delete", { id: p.id })).then((ns) => done(ns)).catch(statusTErr);
    val.append(test, edit, del);
    r.append(who, val);
    box.append(r);
  }
  if (proxyForm && !proxyForm.id) box.append(proxyFormRow(done));
  else if (!proxyForm) {
    const r = el("div", "row pref");
    const add = el("button", "text", t("Add proxy"));
    add.onclick = () => { proxyForm = { id: "", kind: "proxy", name: "", url: "", user: "", hasPass: false }; renderSettings(); };
    r.append(el("div", "who", ""), el("div", "val"));
    r.lastChild.append(add);
    box.append(r);
  }
};
function proxyFormRow(done) {
  const f = proxyForm;
  const ssh = f.kind === "ssh";
  const r = el("div", "row pref proxy-form" + (ssh ? " ssh" : ""));
  const name = input(f.name, t("Name"));
  const url = input(f.url, ssh ? "203.0.113.5:22" : "http://127.0.0.1:7890");
  const userText = ssh ? t("User") : t("Username (optional)");
  const passText = ssh ? t("Password") : t("Password (optional)");
  const user = input(f.user, userText);
  const pass = input("", f.hasPass ? "••••••••" : passText, "password");
  name.setAttribute("aria-label", t("Name"));
  url.setAttribute("aria-label", ssh ? t("SSH server") : t("Proxy address"));
  user.setAttribute("aria-label", userText);
  pass.setAttribute("aria-label", passText);
  const err = el("div", "sub err", f.err || "");
  const save = el("button", "text", f.busy ? t("Logging in…") : t("Save"));
  save.disabled = !!f.busy;
  const cancel = el("button", "text", t("Cancel"));
  // what is typed so far, kept when the kind or Advanced redraws the form
  const keep = () => Object.assign(f, { name: name.value, url: url.value, user: user.value });
  // a proxy or an SSH server: an SSH server is logged in to, and magpie
  // carries the requests through it itself (@laiuuui on X: a VPS gives
  // only SSH)
  const kind = segs([["proxy", t("Proxy")], ["ssh", "SSH"]], f.kind, (k) => { keep(); f.kind = k; f.err = ""; renderSettings(); });
  kind.classList.add("proxy-kind");
  let key = null;
  const body = () => {
    const b = { id: f.id, name: name.value.trim(), url: url.value.trim(), user: user.value.trim() };
    if (pass.value) b.pass = pass.value; // blank keeps the saved password
    if (ssh) {
      if (!isSSH(b.url)) b.url = "ssh://" + b.url;
      if (key?.value.trim()) b.key = key.value; // blank keeps the saved key
      if (f.trust) b.trustNewKey = true;
    }
    return b;
  };
  save.onclick = () => {
    const need = [name, url, ...(ssh ? [user] : [])].find((x) => !x.value.trim());
    if (need) return need.focus();
    const b = body();
    keep();
    // an SSH server is logged in to before it is kept, which takes a moment
    f.busy = ssh;
    if (ssh) renderSettings();
    let id = f.id;
    writingPrefs(api("proxies/save", b, (h) => { id = h.get("X-Magpie-Proxy") || id; }))
      .then((ns) => done(ns, id))
      .catch((e) => { f.busy = false; f.err = e.proxy ? proxyWhy(e.proxy) : t(e.message); renderSettings(); });
  };
  cancel.onclick = () => { proxyForm = null; renderSettings(); };
  for (const x of [name, url, user, pass]) {
    x.className = "words proxy-credential";
    x.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save.onclick(); else if (e.key === "Escape") cancel.onclick(); };
  }
  const val = el("div", "proxy-controls");
  val.append(name, url, user, pass, cancel, save);
  r.append(kind, val);
  if (ssh) {
    // the key and the server's trusted host key: set by few, so folded
    // in a Settings row, not an editor: kept as it toggles
    const [adv, inner] = advFold({ cls: "advanced proxy-advanced", editor: true, open: !!f.adv });
    adv.ontoggle = () => { f.adv = adv.open; };
    key = el("textarea", "words proxy-key");
    key.placeholder = f.hasKey ? t("A private key is saved; paste another to replace it") : t("Private key (instead of or with the password)");
    key.setAttribute("aria-label", t("Private key"));
    key.spellcheck = false;
    key.rows = 3;
    key.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") cancel.onclick(); };
    inner.append(key);
    if (f.hostKey) {
      const hk = el("div", "proxy-hostkey");
      const fp = el("div", "sub", f.trust ? t("The key the server shows next is trusted") : t("Host key {fingerprint}", { fingerprint: f.hostKey }));
      fp.title = f.hostKey;
      hk.append(fp);
      if (!f.trust) {
        const again = el("button", "text", t("Trust a new key"));
        again.onclick = () => { keep(); f.trust = true; renderSettings(); };
        hk.append(again);
      }
      inner.append(hk);
    }
    r.append(adv);
  }
  r.append(err);
  err.hidden = !f.err;
  return r;
}
