// Work › Artifacts (docs/subsystems/artifacts.md): the pages agents made
// with Magpie Artifacts (`magpie mcp artifacts`), newest first. A row
// opens the page in the browser, at the gateway's address for it; its
// menu opens an earlier version, copies the link, installs it as an app
// (the page opens with #install, which asks the browser), shares it on
// usemagpie.ai (or stops sharing it), or deletes it with every version. The library plugin adds the page (client.js), since the
// library is what gives agents the server; this file is loaded before it.
function artifactsPage() {
  let main = null, list = [], q = "", loaded = false, makers = null;
  const DOC = "M4.5 2.5h5l3 3v8h-8zM9.5 2.5v3h3M6.5 8.5h4M6.5 11h3";
  const BIN = "M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5M7 7v4M9 7v4";
  const LINK = "M6.75 9.25l2.5-2.5M7.5 4.5l1-1a2.5 2.5 0 013.5 3.5l-1 1M8.5 11.5l-1 1A2.5 2.5 0 014 9l1-1";
  const UNLINK = LINK + "M3 3l10 10";
  const APP = "M4.5 2.5h7a2 2 0 012 2v7a2 2 0 01-2 2h-7a2 2 0 01-2-2v-7a2 2 0 012-2zM8 5.5v5M6 8.5l2 2 2-2";
  const open = (url) => api("open", { url }).catch(statusErr);

  // who made a version: its agent, or the page itself (artifact.publish)
  const by = (v) => (v.agent === "page" ? t("Saved from the page") : v.agentName || v.agent);

  function row(a) {
    const v = a.versions[0] || {};
    const r = el("div", "row art-row");
    r.dataset.id = a.id;
    r.tabIndex = 0;
    r.setAttribute("role", "link");
    r.title = a.description || a.title;
    const who = el("div", "who");
    const sub = [by(v), ago(a.updated)];
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
      acts.push({ name: "Install as app", icon: APP, run: () => open(a.url + "#install") });
      if (a.shared) acts.push({ name: "Open shared page", icon: LINK, sep: true, run: () => openShared(a) }, { name: "Copy shared link", icon: COPY_ICON, run: () => copy(a.shared, t("Shared link")) }, { name: "Stop sharing", icon: UNLINK, run: () => unshare(a) });
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
    const opts = a.versions.map((v) => ({ v: v.url, literalName: true, name: t("Version {n}", { n: v.n }) + (v.label ? " · " + v.label : ""), note: [by(v), stamp(v.at)].filter(Boolean).join(" · ") }));
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

  // the shared page as its owner sees it there: a one-time code that
  // usemagpie.ai trades for a key this browser keeps (site/live.js)
  async function openShared(a) {
    try {
      const r = await api("artifacts/owner-link", { id: a.id });
      await open(r.url);
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
      box.replaceChildren(empty());
      return;
    }
    if (!shown.length) {
      box.replaceChildren(el("div", "empty-state", t("Nothing matches")));
      return;
    }
    box.replaceChildren(...shown.map(row));
  }

  // With none yet, the page says what one is and how to get one: a few
  // things to ask an agent for, each copied with a click, and which agents
  // can make one, or where to give them the server (the owner, 11-24: the
  // empty page told no one what it was for).
  const ASKS = ["Make a one-page report of this week's git commits, with a chart", "Draw this project's architecture as a diagram", "Make a small Pomodoro timer I can use"];
  // names joined as the GUI's language joins a list ("A、B和C")
  const listed = (names) => { try { return new Intl.ListFormat(intlLang(), { type: "conjunction" }).format(names); } catch { return names.join(", "); } };
  function empty() {
    const e = el("div", "empty-state art-empty");
    e.append(el("b", "", t("No artifacts yet")), el("span", "", t("Ask an agent for a page instead of text: a report, a chart, a prototype or a small tool. It's kept here with every version, to open or share.")));
    const asks = el("div", "art-asks");
    for (const k of ASKS) {
      const b = el("button", "art-ask", t(k));
      b.type = "button";
      b.append(svg(COPY_ICON, 13, 1.4));
      b.title = t("Copy");
      // the glyph alone turns to a check a moment: flashCopied is for a
      // button that is only its glyph, and wiped the words (owner, 11-24)
      b.onclick = async () => {
        await copy(t(k), t("Prompt"));
        b.lastChild.replaceWith(svg(CHECK, 13, 1.7));
        clearTimeout(b.copiedT);
        b.copiedT = setTimeout(() => b.lastChild.replaceWith(svg(COPY_ICON, 13, 1.4)), 1200);
      };
      asks.append(b);
    }
    e.append(asks);
    if (makers) {
      const who = el("div", "art-who");
      const go = el("button", "link", makers.length ? t("Manage") : t("Turn it on in the Library"));
      go.type = "button";
      go.onclick = async () => { if (await window.show("library") !== false) magpie.call("library", "server", "magpie-artifacts"); };
      // a few by name; more as a count, the names on hover (25 filled
      // three lines, owner 11-24)
      if (makers.length > 3) {
        const n = el("span", "", t("{n} agents can make one", { n: makers.length }));
        n.title = listed(makers);
        who.append(n, " · ");
      } else if (makers.length) who.append(t("Agents that can make one: {names}", { names: listed(makers) }), " · ");
      else who.append(t("No agent can make one yet."), " ");
      who.append(go);
      e.append(who);
    }
    return e;
  }

  async function load() {
    try {
      const r = await api("artifacts");
      list = r?.artifacts || [];
      makers = Array.isArray(r?.agents) ? r.agents : null;
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
