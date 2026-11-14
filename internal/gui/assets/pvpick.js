// Picking providers to remove together (eiyuuzaki2333 on X: many
// providers added, removed one at a time), as Routing's groups and
// Library's lists have it: a box on every row, a click picks it (Shift
// picks the rows between it and the last one), and the bar over the list
// picks all or none of those shown, says how many, and removes them after
// one confirmation, each as its editor's Remove does (POST
// /api/providers/delete). Done or Escape ends it; the rows open, switch
// and drag again. app.js imports this when Select is first clicked and
// keeps pvSel, so it weighs nothing on the core.

// the list as it is when not picking: the bar gone, the rows' own parts
// shown again (renderProviders shows Select)
function unbar() {
  $("#view-providers").classList.remove("pv-picking");
  $("#pvSel").hidden = true;
  $("#pvSel").replaceChildren();
}
// a search while picking: the bar's count and all box follow the rows it
// leaves (findProviders, app.js's own listener, runs first)
for (const k of ["input", "keydown"]) $("#pvQ").addEventListener(k, () => pvSel && pvSelSync());
// Escape ends picking, unless a dialog or the search box has it first
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !pvSel || view !== "providers" || confirmationPending || !$("#modal").hidden || e.defaultPrevented) return;
  e.preventDefault();
  endPicking();
});
window.pickedProviderRow = function (p) {
  const row = el("div", "row provider pv-pick" + (p.off ? " off" : ""));
  row.dataset.id = p.id;
  const cb = el("input", "pv-pickbox");
  cb.type = "checkbox";
  cb.setAttribute("aria-label", p.name);
  const who = el("div", "who");
  const n = p.models.filter((m) => m.on).length;
  who.append(el("div", "name", p.name), el("div", "sub", [p.account?.user || p.host, n ? t(n === 1 ? "{n} model" : "{n} models", { n }) : ""].filter(Boolean).join(" · ")));
  row.append(cb, icon(p.icon || "generic"), who);
  cb.onclick = (e) => { e.stopPropagation(); pickProvider(p.id, cb.checked, e.shiftKey); };
  row.onclick = (e) => pickProvider(p.id, !pvSel.has(p.id), e.shiftKey);
  return row;
};
// the rows shown, in the page's order: the search and the fold hide some
const pvShownRows = () => [...document.querySelectorAll("#providers > .row.provider, #offProviders > .row.provider")].filter((r) => !r.hidden && !r.parentElement.hidden);
window.pickProvider = function (id, on, range) {
  if (!pvSel) return;
  const ids = pvShownRows().map((r) => r.dataset.id), i = ids.indexOf(id), j = range ? ids.indexOf(pvSelLast) : -1;
  for (const x of i >= 0 && j >= 0 ? ids.slice(Math.min(i, j), Math.max(i, j) + 1) : [id]) on ? pvSel.add(x) : pvSel.delete(x);
  pvSelLast = id;
  pvSelSync();
};
window.startPicking = function () {
  pvSel = new Set();
  pvSelLast = null;
  editing = null;
  draft = null;
  renderProviders();
};
window.endPicking = function () {
  pvSel = null;
  pvSelLast = null;
  unbar();
  renderProviders();
};
// pvSelBar, after renderProviders drew the list: a provider gone is no
// longer picked, a list too short to pick from ends picking, and each row
// drawn is swapped for its picking row, shown or hidden as the search left
// it, before the bar is drawn
window.pvSelBar = function () {
  for (const id of pvSel) if (!providers.providers.some((p) => p.id === id)) pvSel.delete(id);
  if (providers.providers.length < 2 && !pvSel.size) {
    pvSel = null;
    $("#pvSelect").hidden = providers.providers.length < 2;
    return unbar();
  }
  for (const r of document.querySelectorAll("#providers > .row.provider, #offProviders > .row.provider")) {
    const p = providers.providers.find((x) => x.id === r.dataset.id);
    if (!p) continue;
    const n = pickedProviderRow(p);
    n.hidden = r.hidden;
    const hits = r.querySelector(".pv-hits");
    if (hits) n.querySelector(".who").append(hits);
    r.replaceWith(n);
  }
  const bar = $("#pvSel"), view = $("#view-providers");
  view.classList.add("pv-picking");
  bar.hidden = false;
  const all = el("input", "pv-pickall");
  all.type = "checkbox";
  all.setAttribute("aria-label", t("Select every provider shown"));
  all.onchange = () => {
    for (const r of pvShownRows()) all.checked ? pvSel.add(r.dataset.id) : pvSel.delete(r.dataset.id);
    pvSelSync();
  };
  const rm = el("button", "text danger pv-remove", t("Remove"));
  rm.type = "button";
  rm.onclick = () => removePicked(rm);
  const done = el("button", "text pv-done", t("Done"));
  done.type = "button";
  done.onclick = endPicking;
  bar.replaceChildren(all, el("span", "note"), el("span", "grow"), rm, done);
  pvSelSync();
};
// the boxes, the bar's count and its Remove, as the picks are now
window.pvSelSync = function () {
  if (!pvSel) return;
  for (const r of document.querySelectorAll("#view-providers .row.pv-pick")) {
    const on = pvSel.has(r.dataset.id);
    r.classList.toggle("picked", on);
    r.querySelector(".pv-pickbox").checked = on;
  }
  const shown = pvShownRows().map((r) => r.dataset.id), k = shown.filter((id) => pvSel.has(id)).length;
  const all = $("#pvSel .pv-pickall");
  if (!all) return;
  all.checked = k > 0 && k === shown.length;
  all.indeterminate = k > 0 && k < shown.length;
  all.disabled = !shown.length;
  $("#pvSel .note").textContent = pvSel.size ? t("{n} selected", { n: pvSel.size }) : t("Pick the providers to remove");
  $("#pvSel .pv-remove").disabled = !pvSel.size;
};
// removePicked: the ones picked, named in one confirmation, then removed
// together. One that couldn't be stays picked, and the status says why.
window.removePicked = async function (b) {
  const gone = providers.providers.filter((p) => pvSel?.has(p.id));
  if (!gone.length || b.classList.contains("busy")) return;
  const names = el("ul", "confirm-names");
  for (const p of gone) names.append(el("li", "", p.name));
  const why = document.createDocumentFragment();
  why.append(t(gone.length === 1 ? "Its saved configuration will be removed. Agents using it may switch to another provider or their default model."
    : "Their saved configuration will be removed. Agents using them may switch to another provider or their default model."), names);
  if (!await confirmAction(gone.length === 1 ? t("Remove {name}?", { name: gone[0].name }) : t("Remove {n} providers?", { n: gone.length }), why, t("Remove"))) return;
  const ids = gone.map((p) => p.id);
  saving(b, t("Remove"));
  try {
    providers = await api("providers/delete", { ids });
  } catch (e) {
    doneSaving(b);
    return statusErr(e); // nothing removed: every one stays picked
  }
  const left = providers.unremoved || [], removed = gone.filter((p) => !left.some((u) => u.id === p.id));
  pvSel = left.length ? new Set(left.map((u) => u.id)) : null;
  if (!pvSel) unbar();
  renderProviders();
  const okMsg = removed.length === 1 ? t("{name} removed", { name: removed[0].name }) : t("{n} providers removed", { n: removed.length });
  if (left.length) {
    const p = gone.find((x) => x.id === left[0].id);
    status(okMsg + " · " + t("{name} wasn't removed: {error}", { name: p?.name || left[0].id, error: left[0].error }), "warn", 10000);
  } else saidMoved(okMsg);
  try { state = await api("state"); renderAgents(); } catch (e) { statusErr(e); }
};
