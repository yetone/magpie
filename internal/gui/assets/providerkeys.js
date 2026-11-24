// A provider's gateway keys (justforlxz on X: with many keys, ticking a
// provider in each key's "Models this key may use" is a chore): the
// editor's Advanced lists the keys that may use a model and ticks those
// that may use this provider. The ticks are each key's own models
// (access.SetProviderKeys), so this list and the Gateway page's always
// agree; they are kept in the editor's draft and written on Save.
//
// A key that may use every model, as a new one does, is unticked by
// holding it to every other provider there is now and every routing
// group (access.SetProviderKeys; justforlxz on X: locked, as it was, a
// list of such keys did nothing when clicked). One held to this provider
// alone is ticked and can't be unticked here: it would be left no model,
// which reads as every one, so it is changed on the Gateway page.
// app.js imports this when a saved provider's editor draws (gatewayKeysRow),
// so it weighs nothing on the core.
(() => {
  const LOCKED = "\x00";
  const reaches = (k) => k.reach === "all" || k.reach === "whole" || k.reach === "some";
  // the keys ticked now: the saved ones with the draft's changes on top
  const tickedOf = (keys, d) => keys.filter((k) => reaches(k) ? !d.remove.includes(k.id) : d.add.includes(k.id));
  const label = (keys, d) => {
    const n = tickedOf(keys, d).length;
    return n === keys.length ? t("Every key") : n === 0 ? t("No key") : t(n === 1 ? "1 of {m} keys" : "{n} of {m} keys", { n, m: keys.length });
  };
  const noteOf = (k) => {
    const off = k.off ? t("Off") + " · " : "";
    if (k.reach === "all") return off + t("Every provider; unticked, it keeps the others there are now");
    if (k.only) return off + t("Only this provider: change it on the Gateway page");
    if (k.reach === "whole") return off + t("Every model");
    if (k.reach === "some") return off + t("Some models");
    if (k.via?.length) return off + t("Maybe some, through {list}", { list: k.via.join(", ") });
    return off.replace(/ · $/, "");
  };
  window.gatewayKeysFill = function (p, btn, row) {
    let keys = null;
    btn.type = "button";
    btn.setAttribute("aria-label", t("Gateway keys that may use this provider"));
    btn.setAttribute("aria-haspopup", "menu");
    btn.setAttribute("aria-expanded", "false");
    // the draft gains the keys only when one is ticked or unticked, so an
    // editor left untouched asks no Discard
    const none = { id: p.id, add: [], remove: [] };
    const now = () => draft?.gatewayKeys?.id === p.id ? draft.gatewayKeys : none;
    const show = () => {
      btn.textContent = label(keys, now());
      btn.classList.toggle("set", tickedOf(keys, now()).length < keys.length);
    };
    api("provider-keys?provider=" + encodeURIComponent(p.id)).then((out) => {
      if (!btn.isConnected) return;
      keys = out?.keys || [];
      // no key to give: the row says nothing worth its room
      if (!keys.length) { for (const n of row) n.remove(); return; }
      show();
    }, () => { for (const n of row) n.remove(); });
    btn.onclick = (e) => {
      e.preventDefault();
      e.stopPropagation();
      if (!keys) return;
      if (btn.classList.contains("open")) return closeProtoMenu();
      const opts = keys.map((k) => k.only
        ? { v: LOCKED, ticked: true, name: k.name, note: noteOf(k), literalName: true, find: k.id }
        : { v: k.id, name: k.name, note: noteOf(k), literalName: true, find: k.id });
      const picked = tickedOf(keys, now()).filter((k) => !k.only).map((k) => k.id);
      openProtoMenu(btn, opts, picked, (on) => {
        const add = keys.filter((k) => !reaches(k) && on.includes(k.id)).map((k) => k.id);
        const remove = keys.filter((k) => reaches(k) && !k.only && !on.includes(k.id)).map((k) => k.id);
        // back as saved: nothing left to write, nothing to discard
        if (draft) draft.gatewayKeys = add.length || remove.length ? { id: p.id, add, remove } : undefined;
        show();
      }, "Gateway keys that may use this provider", "sess-menu", "right", false, keys.length > 8 ? "Search keys" : "");
    };
  };
  // the draft's changes, written before the provider is saved
  window.saveGatewayKeys = async function (p) {
    const d = draft?.gatewayKeys;
    if (!p || !d || d.id !== p.id || (!d.add.length && !d.remove.length)) return;
    await api("provider-keys", { provider: p.id, add: d.add, remove: d.remove });
    if (draft) draft.gatewayKeys = undefined;
  };
})();
