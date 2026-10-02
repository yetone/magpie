// Sessions: every session of an agent on this computer, by the folder it
// ran in, to pick up again or to delete. A delete asks first, in magpie's
// own dialog, and moves the session's files into magpie's trash; the Trash
// lists them, each with a Restore and a Delete forever, and can be emptied;
// erasing for good asks in the same dialog, and is only ever done when the
// reader asks for it. A session written to in the last minute
// may still be running, and is left as it is. Everything comes from
// /api/sessions/manage.
(() => {
  const page = $("#view-sessions");
  if (!page) return;

  let data = null;        // /api/sessions/manage
  let failed = "";
  let agent = "";
  try { agent = localStorage.getItem("magpie.sessionsAgent") || ""; } catch {}
  let query = "";
  let trashOn = false;
  const picked = new Set();   // ids picked to delete, of the agent shown
  const opened = new Set();   // folders unfolded, by cwd
  let openedFor = "";         // the agent the folders were first unfolded for
  let detail = "";            // the session opened to its details
  let loading = 0;
  let fitObserver = null;

  const TRASH = "M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5M7 7v4M9 7v4";
  const TERM = "M3 4.5 6 7.5 3 10.5M7.5 11.5h5.5";
  const UNDO = "M5.5 3.5 2.5 6.5l3 3M2.5 6.5H10a3.5 3.5 0 0 1 0 7H7";

  async function load(name) {
    const n = ++loading;
    if (!data) draw();
    try {
      const d = await api("sessions/manage?agent=" + encodeURIComponent(name ?? agent));
      if (n !== loading) return;
      data = { ...d, agents: d?.agents || [], sessions: d?.sessions || [], trash: d?.trash || [], agent: d?.agent || "" };
      failed = "";
      if (agent !== d.agent) { agent = d.agent; picked.clear(); }
    } catch (e) {
      if (n !== loading) return;
      failed = e.message;
    }
    if (view === "sessions") draw();
  }
  window.loadSessionsPage = load;

  const current = () => data?.agents.find((a) => a.agent === data.agent);
  const shown = () => {
    const q = query.trim().toLowerCase();
    const list = data?.sessions || [];
    return q ? list.filter((s) => [s.title, s.cwd, s.id].some((x) => (x || "").toLowerCase().includes(q))) : list;
  };

  function draw() {
    const keepFocus = page.querySelector(".sm-agent-pick:focus, .sm-agents .opt:focus");
    fitObserver?.disconnect();
    if (page.querySelector(".sm-agent-pick.open")) closeProtoMenu();
    page.classList.toggle("loading", !data?.sessions && !failed);
    page.replaceChildren(head(), body());
    const wrap = page.querySelector(".sm-switch");
    const tabs = wrap.querySelector(".sm-agents");
    const pick = wrap.querySelector(".sm-agent-pick");
    const fit = () => {
      const room = parseFloat(getComputedStyle(wrap).width);
      if (!room) return;
      const focused = document.activeElement;
      // Measure the actual labels, including language, font and text zoom,
      // rather than guessing a breakpoint or a number of agents.
      tabs.hidden = false;
      const compact = tabs.scrollWidth > Math.floor(room);
      tabs.hidden = compact || !data?.agents.length;
      pick.hidden = !compact || !current();
      if (!compact && pick.classList.contains("open")) closeProtoMenu();
      if (compact && tabs.contains(focused)) pick.focus({ preventScroll: true });
      else if (!compact && focused === pick) tabs.querySelector(".on")?.focus({ preventScroll: true });
    };
    fitObserver = new ResizeObserver(fit);
    fitObserver.observe(wrap);
    fit();
    if (keepFocus) (pick.hidden ? tabs.querySelector(".on") : pick)?.focus({ preventScroll: true });
  }

  function chooseAgent(name) {
    if (name === data.agent && !trashOn) return;
    trashOn = false;
    agent = name;
    try { localStorage.setItem("magpie.sessionsAgent", name); } catch {}
    picked.clear();
    detail = "";
    data = { ...data, agent: name, sessions: null };
    draw();
    load(name);
  }

  function head() {
    const h = el("div", "usage-head sm-head");
    const wrap = el("div", "sm-switch");
    const tabs = el("div", "segs regions sm-agents");
    for (const x of data?.agents || []) {
      const on = x.agent === data.agent && !trashOn;
      const b = el("button", "opt" + (on ? " on" : ""));
      b.type = "button";
      b.setAttribute("aria-pressed", String(on));
      b.append(icon(x.icon), el("span", "", x.name), el("em", "", String(x.count)));
      b.onclick = () => chooseAgent(x.agent);
      tabs.append(b);
    }
    const a = current();
    const pick = el("button", "sess-pick sm-agent-pick");
    pick.type = "button";
    pick.hidden = !a;
    pick.disabled = (data?.agents.length || 0) < 2;
    pick.setAttribute("aria-haspopup", "menu");
    pick.setAttribute("aria-expanded", "false");
    if (a) {
      pick.title = a.name;
      pick.append(icon(a.icon), el("span", "", a.name), el("em", "", String(a.count)), svg(CHEV, 11, 1.6));
      pick.onclick = (e) => {
        e.stopPropagation();
        if (pick.classList.contains("open")) return closeProtoMenu();
        const opts = data.agents.map((x) => ({ v: x.agent, name: x.name, note: String(x.count), literalName: true }));
        openProtoMenu(pick, opts, data.agent, (name) => {
          pick.focus({ preventScroll: true });
          chooseAgent(name);
        }, "Agent", "sm-agent-menu");
      };
    }
    wrap.append(tabs, pick);
    h.append(wrap);
    const q = el("input", "sess-filter sm-filter");
    q.type = "search";
    q.spellcheck = false;
    q.autocomplete = "off";
    q.placeholder = t("Filter sessions");
    q.value = query;
    q.hidden = trashOn;
    q.oninput = () => { query = q.value; redrawList(); };
    q.onkeydown = (e) => { if (e.key === "Escape" && q.value) { e.stopPropagation(); q.value = ""; query = ""; redrawList(); } };
    const tr = el("button", "text sm-trash-btn" + (trashOn ? " on" : ""));
    tr.type = "button";
    tr.setAttribute("aria-pressed", String(trashOn));
    tr.append(svg(TRASH, 13, 1.4), el("span", "", t("Trash")));
    if (data?.trash.length) tr.append(el("em", "", String(data.trash.length)));
    tr.onclick = () => { trashOn = !trashOn; draw(); };
    h.append(q, tr);
    return h;
  }

  function body() {
    const box = el("div", "sm-body");
    if (failed) { box.append(el("div", "empty-state", failed)); return box; }
    if (!data || (!trashOn && !data.sessions)) {
      const l = el("div", "list sm-group");
      for (let i = 0; i < 4; i++) { const r = el("div", "row"); r.append(el("span", "skeleton sk-line")); l.append(r); }
      box.append(l);
      return box;
    }
    if (trashOn) { box.append(...trash()); return box; }
    if (!data.agents.length) {
      const e = el("div", "empty-state");
      e.append(el("b", "", t("No sessions yet")), el("span", "", t("Claude Code's, Codex's, OpenCode's and Pi's sessions on this computer show up here, by the folder they ran in.")));
      box.append(e);
      return box;
    }
    const a = current();
    if (a && !a.deletable) box.append(el("p", "usage-note sm-note", t("magpie can list {agent}'s sessions and resume them, but not delete them: they aren't kept as files of their own.", { agent: a.name })));
    box.append(selectBar(), el("div", "sm-tree"));
    queueMicrotask(redrawList);
    return box;
  }

  // the bar over the tree: pick every session shown, and delete those picked
  function selectBar() {
    const bar = el("div", "row-head sm-bar");
    const a = current();
    if (!a?.deletable) { bar.hidden = true; return bar; }
    const list = shown();
    const all = el("input", "sm-check");
    all.type = "checkbox";
    all.checked = list.length > 0 && list.every((s) => picked.has(s.id));
    all.indeterminate = !all.checked && list.some((s) => picked.has(s.id));
    all.setAttribute("aria-label", t("Select every session shown"));
    all.onchange = () => {
      for (const s of list) all.checked ? picked.add(s.id) : picked.delete(s.id);
      redrawList();
    };
    const n = el("span", "note sm-count", picked.size ? t("{n} selected", { n: picked.size }) : t(list.length === 1 ? "{n} session" : "{n} sessions", { n: list.length }));
    const del = el("button", "text danger sm-delete");
    del.type = "button";
    del.disabled = !picked.size;
    del.append(svg(TRASH, 13, 1.4), el("span", "", t("Delete")));
    del.onclick = () => askDelete([...picked]);
    bar.append(all, n, el("span", "grow"), del);
    return bar;
  }

  function redrawList() {
    const tree = page.querySelector(".sm-tree");
    if (!tree || !data) return;
    const bar = page.querySelector(".sm-bar");
    if (bar) bar.replaceWith(selectBar());
    const list = shown();
    // the folder of the latest session is open the first time an agent is shown
    if (openedFor !== data.agent) {
      openedFor = data.agent;
      opened.clear();
      if (list[0]) opened.add(list[0].cwd || "");
    }
    const groups = new Map();
    for (const s of list) {
      const k = s.cwd || "";
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k).push(s);
    }
    tree.replaceChildren();
    if (!list.length) {
      tree.append(el("div", "empty-state", query ? t("No session matches.") : t("No sessions yet")));
      return;
    }
    const q = query.trim();
    for (const [cwd, items] of groups) {
      const open = !!q || opened.has(cwd);
      const g = el("div", "list sm-group");
      const r = el("div", "row sm-folder");
      const fold = el("button", "fold");
      fold.type = "button";
      fold.setAttribute("aria-expanded", String(open));
      fold.append(svg(CHEV_R, 10, 1.6), el("span", "name", cwd ? baseName(cwd) : t("No folder")));
      fold.onclick = () => {
        if (opened.has(cwd)) opened.delete(cwd); else opened.add(cwd);
        redrawList();
      };
      r.append(fold, el("span", "sub sm-path", cwd), el("span", "grow"), el("span", "note", t(items.length === 1 ? "{n} session" : "{n} sessions", { n: items.length })));
      r.title = cwd;
      g.append(r);
      if (open) for (const s of items) g.append(item(s));
      tree.append(g);
    }
  }

  function item(s) {
    const a = current();
    const wrap = el("div", "sess-item sm-item" + (detail === s.id ? " open" : ""));
    const r = el("div", "row sess sm-sess");
    r.dataset.id = s.id;
    if (a?.deletable) {
      const c = el("input", "sm-check");
      c.type = "checkbox";
      c.checked = picked.has(s.id);
      c.setAttribute("aria-label", t("Select"));
      c.onclick = (e) => e.stopPropagation();
      c.onchange = () => { c.checked ? picked.add(s.id) : picked.delete(s.id); const bar = page.querySelector(".sm-bar"); if (bar) bar.replaceWith(selectBar()); };
      r.append(c);
    }
    const who = el("div", "who");
    who.append(el("div", "name", s.title || t("(no prompt)")));
    who.append(el("div", "sub", [ago(s.last), s.messages ? t(s.messages === 1 ? "{n} message" : "{n} messages", { n: s.messages }) : "", fmtBytes(s.size), s.id.slice(0, 8)].filter(Boolean).join(" · ")));
    r.append(who);
    if (s.resume) {
      const res = el("button", "sess-resume", t("Resume"));
      res.type = "button";
      res.title = t("Copy the command that resumes it: {cmd}", { cmd: s.resume });
      res.onclick = async (e) => {
        e.stopPropagation();
        await copy(s.resume, t("Resume command"));
        res.textContent = t("Copied");
        res.classList.add("done");
        clearTimeout(res.copiedT);
        res.copiedT = setTimeout(() => { res.textContent = t("Resume"); res.classList.remove("done"); }, 1400);
      };
      r.append(res);
      if (data.terminal) {
        const term = el("button", "copy sess-term");
        term.type = "button";
        term.title = t("Open in session terminal");
        term.append(svg(TERM, 13, 1.6));
        term.onclick = (e) => {
          e.stopPropagation();
          api("sessions/terminal", { agent: s.agent, id: s.id }).then(() => status(t("Opening in session terminal"), "ok"), (err) => status(err.message, "err"));
        };
        r.append(term);
      }
    }
    if (a?.deletable) {
      const del = el("button", "copy sm-del");
      del.type = "button";
      del.title = t("Delete");
      del.setAttribute("aria-label", t("Delete"));
      del.append(svg(TRASH, 13, 1.4));
      del.onclick = (e) => { e.stopPropagation(); askDelete([s.id]); };
      r.append(del);
    }
    r.onclick = () => {
      if (window.getSelection()?.toString()) return;
      detail = detail === s.id ? "" : s.id;
      redrawList();
    };
    wrap.append(r);
    if (detail === s.id) wrap.append(details(s));
    return wrap;
  }

  function details(s) {
    const d = el("div", "sess-detail");
    const line = (k, v, extra) => {
      const l = el("div", "sess-line");
      l.append(el("span", "k", k), v instanceof Node ? v : el("span", "v", v));
      if (extra) l.append(extra);
      d.append(l);
    };
    line(t("Time"), stamp(s.start || s.last) + " – " + stamp(s.last));
    if (s.cwd) line(t("Folder"), s.cwd);
    line(t("Session ID"), s.id, copyBtn(s.id, t("Session id")));
    if (s.resume) line(t("Resume"), el("code", "", s.resume), copyBtn(s.resume, t("Resume command")));
    if (s.path) line(t("File"), s.path + (s.files > 1 ? " " + t("+{n} more", { n: s.files - 1 }) : ""));
    return d;
  }

  // askDelete asks in magpie's dialog before the sessions go to its trash
  function askDelete(ids) {
    const byID = new Map((data?.sessions || []).map((s) => [s.id, s]));
    const list = ids.map((id) => byID.get(id)).filter(Boolean);
    if (!list.length) return;
    const a = current();
    const ed = el("div", "editor sm-ask");
    const h = el("div", "ehead");
    h.append(icon(a.icon), el("b", "", list.length === 1 ? t("Delete this session?") : t("Delete {n} sessions?", { n: list.length })));
    ed.append(h);
    const names = el("ul", "sm-ask-list");
    for (const s of list.slice(0, 5)) names.append(el("li", "", s.title || s.id));
    if (list.length > 5) names.append(el("li", "more", t("+{n} more", { n: list.length - 5 })));
    ed.append(names);
    ed.append(el("p", "lib-confirm", t("Their files are moved to magpie's trash ({dir}), not erased: Trash puts them back. A session written to in the last minute is left alone, as {agent} may still be running it.", { dir: data.trashDir, agent: a.name })));
    const bar = el("div", "bar");
    const go = el("button", "text primary danger-fill", t("Delete"));
    go.type = "button";
    go.onclick = async (e) => {
      e.stopPropagation();
      go.disabled = true;
      go.classList.add("busy");
      let out;
      try {
        out = await api("sessions/delete", { agent: data.agent, ids: list.map((s) => s.id) });
      } catch (err) {
        go.disabled = false;
        go.classList.remove("busy");
        status(err.message, "err");
        return;
      }
      closeConfirmAsk();
      for (const id of out.deleted) { picked.delete(id); if (detail === id) detail = ""; }
      const active = out.refused.filter((r) => r.active);
      if (out.refused.length) {
        const first = out.refused[0], s = byID.get(first.id);
        status(active.length ? t(active.length === 1 ? "{title} is still being written to; close it in {agent} and try again in a minute" : "{n} sessions are still being written to; close them in {agent} and try again in a minute", { title: s?.title || first.id, n: active.length, agent: a.name })
          : (s?.title || first.id) + ": " + first.error, "err");
      } else {
        status(t(out.deleted.length === 1 ? "Moved to magpie's trash" : "{n} sessions moved to magpie's trash", { n: out.deleted.length }), "ok");
      }
      await load();
    };
    const cancel = el("button", "text", t("Cancel"));
    cancel.type = "button";
    cancel.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
    bar.append(el("span", "grow"), cancel, go);
    ed.append(bar);
    confirmAsk = ed;
    openModal(ed);
    $("#modal").classList.add("lib");
    cancel.focus({ preventScroll: true });
  }

  // askPurge asks in magpie's dialog before trashed sessions are erased for
  // good: the ones listed, or the whole trash (all)
  function askPurge(list, all) {
    if (!list.length) return;
    const ed = el("div", "editor sm-ask sm-ask-purge");
    const h = el("div", "ehead");
    h.append(svg(TRASH, 15, 1.4), el("b", "", all ? t("Empty magpie's trash?") : t("Delete this session forever?")));
    ed.append(h);
    const names = el("ul", "sm-ask-list");
    for (const x of list.slice(0, 5)) names.append(el("li", "", x.title || x.id));
    if (list.length > 5) names.append(el("li", "more", t("+{n} more", { n: list.length - 5 })));
    ed.append(names);
    ed.append(el("p", "lib-confirm", all ? t("Every session in magpie's trash is erased for good: it can't be restored.") : t("Its files are erased for good: it can't be restored.")));
    const bar = el("div", "bar");
    const go = el("button", "text primary danger-fill", all ? t("Empty trash") : t("Delete forever"));
    go.type = "button";
    go.onclick = async (e) => {
      e.stopPropagation();
      go.disabled = true;
      go.classList.add("busy");
      let out;
      try {
        out = await api("sessions/purge", all ? { all: true } : { keys: list.map((x) => x.key) });
      } catch (err) {
        go.disabled = false;
        go.classList.remove("busy");
        status(err.message, "err");
        return;
      }
      closeConfirmAsk();
      if (out.refused.length) {
        const first = out.refused[0], x = list.find((y) => y.key === first.key);
        status((x?.title || x?.id || first.key) + ": " + first.error, "err");
      } else {
        status(t(out.purged.length === 1 ? "Erased for good" : "{n} sessions erased for good", { n: out.purged.length }), "ok");
      }
      await load();
    };
    const cancel = el("button", "text", t("Cancel"));
    cancel.type = "button";
    cancel.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
    bar.append(el("span", "grow"), cancel, go);
    ed.append(bar);
    confirmAsk = ed;
    openModal(ed);
    $("#modal").classList.add("lib");
    cancel.focus({ preventScroll: true });
  }

  function trash() {
    const out = [];
    const items = data.trash;
    if (items.length) {
      const bar = el("div", "row-head sm-bar sm-trash-bar");
      const empty = el("button", "text danger sm-empty");
      empty.type = "button";
      empty.append(svg(TRASH, 13, 1.4), el("span", "", t("Empty trash")));
      empty.onclick = () => askPurge(items, true);
      bar.append(el("span", "note sm-count", t(items.length === 1 ? "{n} session" : "{n} sessions", { n: items.length })), el("span", "grow"), empty);
      out.push(bar);
    }
    if (!items.length) {
      const e = el("div", "empty-state");
      e.append(el("b", "", t("Trash is empty")), el("span", "", t("Sessions deleted here wait in magpie's trash, to be restored.")));
      out.push(e);
    } else {
      const l = el("div", "list sm-group sm-trash");
      for (const x of items) {
        const r = el("div", "row sess");
        r.append(icon(x.icon));
        const who = el("div", "who");
        who.append(el("div", "name", x.title || x.id));
        who.append(el("div", "sub", [x.name, x.cwd ? baseName(x.cwd) : "", t("deleted {when}", { when: ago(x.deleted) }), fmtBytes(x.size)].filter(Boolean).join(" · ")));
        who.title = x.items.map((i) => i.from).join("\n");
        r.append(who);
        const b = el("button", "sess-resume sm-restore");
        b.type = "button";
        b.append(svg(UNDO, 12, 1.5), el("span", "", t("Restore")));
        b.onclick = async (e) => {
          e.stopPropagation();
          b.disabled = true;
          try {
            await api("sessions/restore", { key: x.key });
            status(t("{title} restored", { title: x.title || x.id }), "ok");
          } catch (err) {
            b.disabled = false;
            status(err.message, "err");
            return;
          }
          await load();
        };
        r.append(b);
        const del = el("button", "copy sm-del sm-purge");
        del.type = "button";
        del.title = t("Delete forever");
        del.setAttribute("aria-label", t("Delete forever"));
        del.append(svg(TRASH, 13, 1.4));
        del.onclick = (e) => { e.stopPropagation(); askPurge([x], false); };
        r.append(del);
        l.append(r);
      }
      out.push(l);
    }
    out.push(el("p", "usage-note", t("Deleted sessions are kept in {dir} until you erase them here; magpie never erases them by itself.", { dir: data.trashDir })));
    return out;
  }
})();
