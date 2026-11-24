// Work › Artifacts (docs/subsystems/artifacts.md): the pages agents made
// with Magpie Artifacts (`magpie mcp artifacts`), newest first. A row
// opens the page in the browser, at the gateway's address for it; its
// menu opens an earlier version, copies the link, shares it on
// usemagpie.ai (or stops sharing it), or deletes it with every version. The library plugin adds the page (client.js), since the
// library is what gives agents the server; this file is loaded before it.
function artifactsPage() {
  let main = null, list = [], q = "", loaded = false;
  const DOC = "M4.5 2.5h5l3 3v8h-8zM9.5 2.5v3h3M6.5 8.5h4M6.5 11h3";
  const BIN = "M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5M7 7v4M9 7v4";
  const LINK = "M6.75 9.25l2.5-2.5M7.5 4.5l1-1a2.5 2.5 0 013.5 3.5l-1 1M8.5 11.5l-1 1A2.5 2.5 0 014 9l1-1";
  const UNLINK = LINK + "M3 3l10 10";
  const open = (url) => api("open", { url }).catch(statusErr);

  function row(a) {
    const v = a.versions[0] || {};
    const r = el("div", "row art-row");
    r.dataset.id = a.id;
    r.tabIndex = 0;
    r.setAttribute("role", "link");
    r.title = a.description || a.title;
    const who = el("div", "who");
    const sub = [v.agentName || v.agent, ago(a.updated)];
    if (a.versions.length > 1) sub.push(t("{n} versions", { n: a.versions.length }));
    if (a.shared) sub.push(t("Shared"));
    who.append(el("div", "name", a.title), el("div", "sub", sub.filter(Boolean).join(" · ")));
    const ic = el("span", "art-ic");
    ic.append(svg(DOC, 16, 1.4));
    const more = el("button", "copy art-more");
    more.type = "button";
    more.title = t("More");
    more.setAttribute("aria-label", t("More"));
    menuOpener(more);
    more.append(svg(DOTS, 14, 2));
    more.onclick = (e) => {
      e.stopPropagation();
      if (more.classList.contains("open")) return closeAgentMenu();
      const acts = [{ name: "Copy link", icon: COPY_ICON, run: () => copy(a.url, t("Link")) }];
      if (a.versions.length > 1) acts.push({ name: "Earlier versions", icon: DOC, run: () => versions(a, more) });
      if (a.shared) acts.push({ name: "Copy shared link", icon: LINK, sep: true, run: () => copy(a.shared, t("Shared link")) }, { name: "Stop sharing", icon: UNLINK, run: () => unshare(a) });
      else acts.push({ name: "Share…", icon: LINK, sep: true, run: () => share(a) });
      acts.push({ name: "Delete", icon: BIN, sep: true, run: () => remove(a) });
      openRowMenu(more, acts);
    };
    r.onclick = () => open(a.url);
    r.onkeydown = (e) => { if (e.key === "Enter" && e.target === r) open(a.url); };
    r.append(ic, who, more);
    return r;
  }

  // every version, newest first, each opened at its own address
  function versions(a, anchor) {
    const opts = a.versions.map((v) => ({ v: v.url, literalName: true, name: t("Version {n}", { n: v.n }) + (v.label ? " · " + v.label : ""), note: [v.agentName || v.agent, stamp(v.at)].filter(Boolean).join(" · ") }));
    openProtoMenu(anchor, opts, "", (url) => open(url), "Versions");
  }

  // anyone with the link opens it, on usemagpie.ai; the agent's next
  // versions show there too
  async function share(a) {
    const before = document.querySelector("dialog.action-confirm");
    const asked = confirmAction(t("Share {name}?", { name: a.title }), t("Anyone with its link can open it, on usemagpie.ai. Each new version shows there too, until you stop sharing."), t("Share"));
    // sharing loses nothing, so its button isn't the red one; done here, as
    // app.js has no room for a confirmAction that says so (core_budget_test)
    const mine = document.querySelector("dialog.action-confirm");
    if (mine !== before) mine?.querySelector(".danger-fill")?.classList.remove("danger-fill");
    if (!await asked) return;
    status(t("Sharing {name}…", { name: a.title }));
    try {
      const r = await api("artifacts/share", { id: a.id });
      a.shared = r.url;
      draw();
      await copy(r.url, t("Shared link"));
    } catch (e) { statusErr(e); }
  }

  async function unshare(a) {
    if (!await confirmAction(t("Stop sharing {name}?", { name: a.title }), t("Its shared link stops working. It stays on this computer."), t("Stop sharing"))) return;
    try {
      await api("artifacts/unshare", { id: a.id });
      a.shared = "";
      draw();
      status(t("{name} isn't shared any more", { name: a.title }), "ok");
    } catch (e) { statusErr(e); }
  }

  async function remove(a) {
    const said = a.shared ? t("Its link and its shared link stop working, with every version it has.") : t("Its link stops working, with every version it has.");
    if (!await confirmAction(t("Delete {name}?", { name: a.title }), said, t("Delete"))) return;
    try {
      await api("artifacts/delete", { id: a.id });
      list = list.filter((x) => x.id !== a.id);
      draw();
      status(t("{name} deleted", { name: a.title }), "ok");
    } catch (e) { statusErr(e); }
  }

  function draw() {
    if (!main) return;
    const box = main.querySelector("#artList"), find = main.querySelector("#artQ");
    find.closest(".art-head").hidden = list.length < 8;
    const words = q.trim().toLocaleLowerCase();
    const shown = words ? list.filter((a) => (a.title + " " + (a.description || "")).toLocaleLowerCase().includes(words)) : list;
    if (!loaded) return;
    if (!list.length) {
      const e = el("div", "empty-state art-empty");
      e.append(el("b", "", t("No artifacts yet")), el("span", "", t("Pages your agents make appear here.")));
      box.replaceChildren(e);
      return;
    }
    if (!shown.length) {
      box.replaceChildren(el("div", "empty-state", t("Nothing matches")));
      return;
    }
    box.replaceChildren(...shown.map(row));
  }

  async function load() {
    try {
      const r = await api("artifacts");
      list = r?.artifacts || [];
      loaded = true;
      draw();
    } catch (e) { statusErr(e); }
  }

  return {
    key: "artifacts",
    title: "Artifacts",
    html: `
  <div class="art-head" hidden><input class="sess-filter" id="artQ" type="search" spellcheck="false" autocomplete="off" placeholder="Find an artifact" aria-label="Find an artifact" data-tp></div>
  <div class="list art-list" id="artList"></div>
`,
    mount(m) {
      main = m;
      main.querySelector("#artQ").oninput = (e) => { q = e.target.value; draw(); };
    },
    open() { return load(); },
    refresh() { return load(); },
    reload() { return load(); },
    relang() { draw(); },
  };
}
