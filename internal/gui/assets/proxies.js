// Settings › Network's saved proxies: their rows, the test and the form.
// app.js imports this when Settings draws them, so it weighs nothing on the core.

let proxyForm = null; // { id, name, url, user, hasPass, err } while one is added or edited
const proxyChecks = {}; // id -> { busy, out }
window.renderSavedProxies = function (s) {
  const box = $("#proxiesList");
  box.replaceChildren();
  const done = (ns) => { prefs = ns; proxyForm = null; renderSettings(); };
  for (const p of s.proxies || []) {
    if (proxyForm && proxyForm.id === p.id) { box.append(proxyFormRow(done)); continue; }
    const r = el("div", "row pref saved-proxy");
    const who = el("div", "who");
    const sub = el("div", "sub");
    who.append(el("div", "name", p.name), sub);
    const chk = proxyChecks[p.id];
    const out = chk?.out || p.check;
    const line = [p.user ? p.user + "@" + p.url : p.url];
    if (chk?.busy) line.push(t("Testing…"));
    else if (out) line.push(out.ok ? t("Reachable, {ms} ms", { ms: out.ms }) : t("Unreachable: {error}", { error: out.error || "" }));
    if (p.uses?.length) line.push(t("Used by {n}", { n: p.uses.length }));
    sub.textContent = sub.title = line.join(" · ");
    sub.classList.toggle("err", !!out && !out.ok && !chk?.busy);
    const val = el("div", "val");
    const test = el("button", "text", chk?.busy ? t("Testing…") : t("Test"));
    test.disabled = !!chk?.busy;
    test.onclick = () => {
      proxyChecks[p.id] = { busy: true };
      renderSettings();
      api("proxies/test", { id: p.id })
        .then((o) => { proxyChecks[p.id] = { out: o }; })
        .catch((e) => { proxyChecks[p.id] = { out: { ok: false, error: t(e.message) } }; })
        .finally(renderSettings);
    };
    const edit = el("button", "text", t("Edit"));
    edit.onclick = () => { proxyForm = { id: p.id, name: p.name, url: p.url, user: p.user || "", hasPass: !!p.hasPass }; renderSettings(); };
    const del = el("button", "text", t("Delete"));
    del.disabled = !!p.uses?.length;
    del.onclick = () => writingPrefs(api("proxies/delete", { id: p.id })).then(done).catch(statusTErr);
    val.append(test, edit, del);
    r.append(who, val);
    box.append(r);
  }
  if (proxyForm && !proxyForm.id) box.append(proxyFormRow(done));
  else if (!proxyForm) {
    const r = el("div", "row pref");
    const add = el("button", "text", t("Add proxy"));
    add.onclick = () => { proxyForm = { id: "", name: "", url: "", user: "", hasPass: false }; renderSettings(); };
    r.append(el("div", "who", ""), el("div", "val"));
    r.lastChild.append(add);
    box.append(r);
  }
};
function proxyFormRow(done) {
  const f = proxyForm;
  const r = el("div", "row pref proxy-form");
  const name = input(f.name, t("Name"));
  const url = input(f.url, "http://127.0.0.1:7890");
  const user = input(f.user, t("Username (optional)"));
  const pass = input("", f.hasPass ? "••••••••" : t("Password (optional)"), "password");
  name.setAttribute("aria-label", t("Name"));
  url.setAttribute("aria-label", t("Proxy address"));
  user.setAttribute("aria-label", t("Username (optional)"));
  pass.setAttribute("aria-label", t("Password (optional)"));
  const err = el("div", "sub err", f.err || "");
  const save = el("button", "text", t("Save"));
  const cancel = el("button", "text", t("Cancel"));
  const body = () => {
    const b = { id: f.id, name: name.value.trim(), url: url.value.trim(), user: user.value.trim() };
    if (pass.value) b.pass = pass.value; // blank keeps the saved password
    return b;
  };
  save.onclick = () => {
    if (!name.value.trim() || !url.value.trim()) return (name.value.trim() ? url : name).focus();
    const b = body();
    Object.assign(f, { name: b.name, url: b.url, user: b.user });
    writingPrefs(api("proxies/save", b)).then(done).catch((e) => { f.err = t(e.message); renderSettings(); });
  };
  cancel.onclick = () => { proxyForm = null; renderSettings(); };
  for (const x of [name, url, user, pass]) {
    x.className = "words proxy-credential";
    x.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save.onclick(); else if (e.key === "Escape") cancel.onclick(); };
  }
  const val = el("div", "proxy-controls");
  val.append(name, url, user, pass, cancel, save);
  r.append(val, err);
  err.hidden = !f.err;
  return r;
}
