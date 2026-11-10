// Sessions: every session of an agent on this computer, by the folder it
// ran in, to pick up again or to delete. A delete asks first, in magpie's
// own dialog, and moves the session's files into magpie's trash; the Trash
// lists them, each with a Restore and a Delete forever, and can be emptied;
// erasing for good asks in the same dialog, and is only ever done when the
// reader asks for it. A session written to in the last minute
// may still be running, and is left as it is. Each row shows what the
// session spent, as the Usage page's list does, and that list's rows open
// here (#752). Everything comes from /api/sessions/manage.
//
// A page the Sessions plugin adds to the window (kernel.js), its words in
// i18n.js beside this file. Classic script, loaded before app.js: what
// runs as it loads touches neither the page nor app.js. mount, at
// magpie.start, is handed the page; open loads it, relang draws it again
// in a new language, and reveal is the Usage page's way in.
magpie.plugin("sessions", (ctx) => {
  let page = null;

  let data = null;        // /api/sessions/manage
  let failed = "";
  let agent = "";
  try { agent = localStorage.getItem("magpie.sessionsAgent") || ""; } catch {}
  let query = "";
  // The filter also finds a session by what was said in it: the server
  // reads the agent's conversations a moment after the last keystroke, and
  // a new keystroke cancels the read it was on (YOU on Discord: a session
  // the gateway recorded is named ses_…, and its words are how it is
  // remembered). found is that search, for one agent and one query.
  let found = null;       // { agent, needle, hits: Map(id -> hit), busy, cut, unread, searched, total, error }
  let searchCtl = null;
  let searchT = 0;
  const SEARCH_WAIT = 300;
  const needleOf = (q) => q.trim().split(/\s+/).filter(Boolean).join(" ").toLowerCase();
  let trashOn = false;
  // Picking sessions to delete is a mode of its own, as in Finder and Mail:
  // off, no row carries a checkbox and there is no bar over the list
  // (yetone: a box on every row, and a lone Delete under the Trash).
  let selecting = false;
  let othersOpen = false;     // the folders an app or the system made, unfolded
  const picked = new Set();   // ids picked to delete, of the agent shown
  const opened = new Set();   // folders unfolded, by cwd
  const folderBoxes = new Map(); // each folder's box as drawn, by cwd
  let openedFor = "";         // the agent the folders were first unfolded for
  let detail = "";            // the session opened to its details
  const talkOpen = new Set(); // sessions whose conversation is shown, by agent/id
  const talks = new Map();    // what was said in them, as read, by agent/id
  // A session's subagents sit under it, folded (#2261): the conversations
  // its agent started inside it (Claude Code, Qoder, OpenCode, Kimi Code:
  // s.subagents, each opened to what was said in it), and the Codex
  // threads that name it as their parent_session, which are sessions of
  // their own and drawn as such, only one step in. subsOpen is the
  // sessions unfolded, by agent/id; kidsOf is the sessions shown under
  // each one, by its id, and rootOf the top one each nested session is
  // under, as last drawn.
  const subsOpen = new Set();
  let kidsOf = new Map();
  let rootOf = new Map();
  const MAX_DEPTH = 8;
  let loading = 0;
  let fitObserver = null;
  // Each session's output speed as the gateway counts it (/api/speeds,
  // gateway/session_speed.go), by "agent|session", read once a second
  // while the page is in sight and drawn on its rows: a curve of the last
  // two minutes, tokens a second now, and the round's numbers in a
  // popover. A session the gateway hasn't served lately has none.
  let speeds = new Map();
  let speedT = 0;
  let speedBusy = false;
  let speedPop = null;    // { anchor, draw } of the popover open
  let focus = null;           // { agent, id, until }: a session to bring into sight once drawn
  let recordingBusy = false;
  // the agents picked in the consent (RustCats on X), by id, and whether
  // the choice is being saved
  const consentPicked = new Set();
  let consentBusy = false;
  // An open folder draws its latest SM_PAGE sessions and a "Show N more"
  // that adds the next page where it is (ChildhoodAndy on X: with thousands
  // of sessions, switching to the page or to another agent, a keystroke in
  // the filter and opening a session each drew every row again, 17,000
  // elements for 1,000 sessions, and scrolling stuttered). drawnIn is how
  // many of each folder's sessions are drawn, by cwd; it starts again at a
  // page when the agent or the filter changes.
  const SM_PAGE = 100;
  const drawnIn = new Map();
  // The folders are drawn a page at a time too, under FOLDERS in drawnIn:
  // an app that runs Codex once per picture in a folder of its own left
  // 11,358 folders of one session each, and drawing them all made 114,000
  // elements, held every click on the page for over a second, and slowed
  // the Library page after it (yetone).
  const FOLDERS = "\x00folders";
  const OTHERS = "\x00others";

  // A session seen only through magpie's gateway has no folder magpie knows
  // (its agent's files are on another computer, in a container or under
  // another config folder): such sessions are grouped by that, not under
  // "No folder" with the agent's own (lc on Discord).
  const GW = "\x00gateway";
  // A session its agent ran with no project of its own, in a folder the
  // agent made for that one session: ZCode keeps ~/.zcode/workspace/
  // draft-<id> or <ms>-<uuid> (its host, 3.17.2), one for each session
  // started without a project.
  // One "folder" each was 40 folders of one session, and they are no
  // projects of the reader's (flandern1211_58540 on Discord): one group of
  // their own, not "No folder", keeps them apart from sessions whose agent
  // recorded no folder.
  const DRAFTS = "\x00drafts";
  const zcodeDraft = (s) => {
    const home = (data?.home || "").replace(/\\/g, "/").replace(/\/+$/, "");
    if (!home || s.agent !== "zcode" || !s.cwd) return false;
    const c = s.cwd.replace(/\\/g, "/");
    return c === home + "/.zcode/workspace" || c.startsWith(home + "/.zcode/workspace/");
  };
  const groupOf = (s) => zcodeDraft(s) ? DRAFTS : s.cwd || (s.gateway ? GW : "");
  const groupName = (k) => k === GW ? t("Seen through the gateway")
    : k === DRAFTS ? t("Draft sessions") : k ? baseName(k) : t("No folder");

  // A folder an app or the system made, not a project of the reader's: the
  // disk's root, a temp folder, or one under a hidden folder of a home
  // (~/.some-agent/…). They go under a fold of their own after the
  // projects (yetone: "/", "T" and folders named by UUIDs led the list).
  const scratch = (k) => {
    if (!k || k === GW || k === DRAFTS) return false;
    const p = k.replace(/\\/g, "/").replace(/\/+$/, "");
    return p === "" || /^[a-z]:$/i.test(p)
      || /^\/(private\/)?(tmp|var\/tmp|var\/folders)(\/|$)/.test(p)
      || /\/AppData\/Local\/Temp(\/|$)/i.test(p)
      || /^([a-z]:)?\/(Users|home)\/[^/]+\/\.[^/]/i.test(p) || /^\/root\/\./.test(p);
  };
  // where a folder is, as its row says it: the folder it is in, with the
  // home folder as ~; the whole path is the row's title
  const whereOf = (p) => {
    const home = data?.home || "";
    const up = p.replace(/[\\/]+$/, "");
    const cut = Math.max(up.lastIndexOf("/"), up.lastIndexOf("\\"));
    if (home && up === home) return "~";
    const parent = cut < 0 ? "" : cut === 0 ? "/" : up.slice(0, cut);
    if (!home || !parent) return parent;
    return parent === home ? "~" : parent.startsWith(home + "/") || parent.startsWith(home + "\\") ? "~" + parent.slice(home.length) : parent;
  };

  const TRASH = "M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5M7 7v4M9 7v4";
  const TERM = "M3 4.5 6 7.5 3 10.5M7.5 11.5h5.5";
  const UNDO = "M5.5 3.5 2.5 6.5l3 3M2.5 6.5H10a3.5 3.5 0 0 1 0 7H7";
  const MOVE = "M2.5 8h10M9.5 5l3 3-3 3";

  // fillIn asks again, while rows are still pending (a file of theirs not
  // read whole yet: their first lines are what shows), for what the read
  // behind the answer has found; only the list is drawn again, so the
  // filter keeps its focus and an open menu stays.
  let fillT = 0;
  function fillSoon() {
    clearTimeout(fillT);
    if (!data?.sessions?.some((s) => s.pending)) return;
    const n = loading, was = data.agent;
    fillT = setTimeout(async () => {
      if (n !== loading || view !== "sessions") return;
      let d;
      try { d = await api("sessions/manage?agent=" + encodeURIComponent(was)); } catch { return fillSoon(); }
      if (n !== loading || !data || data.agent !== was || d?.agent !== was) return;
      data = { ...data, ...d, agents: d?.agents || [], sessions: (d?.sessions || []).map((s) => ({ ...s, models: s.models || [] })), trash: d?.trash || [], agent: was };
      if (view === "sessions" && !trashOn) redrawList(); // the pages shown stay
      fillSoon();
    }, 1500);
  }

  async function load(name) {
    const n = ++loading;
    clearTimeout(fillT);
    if (!data) draw();
    try {
      const d = await api("sessions/manage?agent=" + encodeURIComponent(name ?? agent));
      if (n !== loading) return;
      // models may be null for a session that spent nothing
      data = { ...d, agents: d?.agents || [], sessions: (d?.sessions || []).map((s) => ({ ...s, models: s.models || [] })), trash: d?.trash || [], agent: d?.agent || "" };
      failed = "";
      talks.clear(); // read again when shown: a session may have gone on
      if (agent !== d.agent) { agent = d.agent; picked.clear(); }
      if (query) searchSoon(); // the same words, in this agent's sessions
    } catch (e) {
      if (n !== loading) return;
      failed = e.message;
    }
    if (view === "sessions") { draw(); speedsOn(); }
    if (n === loading) fillSoon();
  }

  // reveal: the Sessions page, from the Usage page's list (#752):
  // on the session's agent, its folder unfolded and the session opened to
  // its details, brought into sight once it is drawn; with no id, the page
  // on that agent (or as it was). The reader's click is what moves the view.
  function reveal(s, e) {
    if (s.agent && s.agent !== agent) {
      agent = s.agent;
      try { localStorage.setItem("magpie.sessionsAgent", agent); } catch {}
      picked.clear();
      if (data) data = { ...data, agent, sessions: null };
    }
    trashOn = false;
    if (s.id) {
      query = "";
      searchSoon();
      detail = s.id;
      openedFor = agent;
      opened.clear();
      opened.add(groupOf(s));
      focus = scrollOnPurpose(e) ? { agent, id: s.id, until: performance.now() + 5000 } : null;
    }
    show("sessions");
  }

  // bring the session asked for into sight, once its row is drawn
  function toFocus() {
    if (!focus || focus.agent !== data?.agent || !data.sessions) return;
    if (performance.now() > focus.until) { focus = null; return; }
    const r = page.querySelector(`.row.sm-sess[data-id="${CSS.escape(focus.id)}"]`);
    focus = null;
    if (!r) return;
    readerScrolls(1000);
    r.closest(".sm-item").scrollIntoView({ block: "nearest", behavior: "instant" });
    readerLeaves(page);
  }

  const current = () => data?.agents.find((a) => a.agent === data.agent);
  // the search of what was said, when it is of the agent and words shown
  const search = () => found && found.agent === data?.agent && found.needle === needleOf(query) ? found : null;
  const hitOf = (s) => search()?.hits.get(s.id);
  const shown = () => {
    const q = query.trim().toLowerCase();
    const list = data?.sessions || [];
    return q ? list.filter((s) => hitOf(s) || [s.title, s.cwd, s.id, ...(s.models || []).map((m) => m.model)].some((x) => (x || "").toLowerCase().includes(q))) : list;
  };

  // searchSoon searches what was said for the filter's words, once typing
  // pauses; fewer than two letters search nothing
  function searchSoon() {
    clearTimeout(searchT);
    const needle = needleOf(query);
    if (!data?.agent || [...needle].length < 2) {
      searchCtl?.abort();
      searchCtl = null;
      found = null;
      return;
    }
    if (found && found.agent === data.agent && found.needle === needle && !found.busy && !found.error) return;
    searchCtl?.abort();
    searchCtl = null;
    const mine = found = { agent: data.agent, needle, hits: new Map(), busy: true };
    searchT = setTimeout(async () => {
      const ctl = searchCtl = new AbortController();
      try {
        const r = await api(`sessions/search?agent=${encodeURIComponent(mine.agent)}&q=${encodeURIComponent(needle)}`, undefined, undefined, ctl.signal);
        if (found !== mine) return;
        found = { ...mine, busy: false, hits: new Map((r?.hits || []).map((h) => [h.id, h])), cut: !!r?.cut, unread: !!r?.unread, searched: r?.searched || 0, total: r?.total || 0 };
      } catch (e) {
        if (ctl.signal.aborted || found !== mine) return;
        found = { ...mine, busy: false, error: e.message };
      }
      if (searchCtl === ctl) searchCtl = null;
      if (view === "sessions" && !trashOn) { drawnIn.clear(); redrawList(); }
    }, SEARCH_WAIT);
  }

  // what the search is doing, above the list, when it says more than the
  // rows do
  function searchNote() {
    const f = search();
    if (!f) return null;
    let text = "";
    if (f.busy) text = t("Searching what was said…");
    else if (f.error) text = t("Couldn't search what was said: {error}", { error: f.error });
    else if (f.unread) text = t("{agent}'s own files of its sessions can't be read here, so only what magpie recorded through the gateway is searched.", { agent: current()?.name || f.agent });
    else if (f.cut) text = t("Searched what was said in the latest {n} of {total} sessions.", { n: f.searched, total: f.total });
    if (!text) return null;
    const p = el("p", "usage-note sm-note sm-search-note", text);
    p.setAttribute("role", "status");
    return p;
  }

  // the words round where a session said what was searched for
  function hitLine(h) {
    const d = el("div", "sub sm-hit");
    d.append(h.before || "", el("mark", "", h.match), h.after || "");
    d.title = (h.before || "") + h.match + (h.after || "");
    return d;
  }

  function draw() {
    const keepFocus = page.querySelector(".sm-agent-pick:focus, .sm-agents .opt:focus");
    fitObserver?.disconnect();
    if (page.querySelector(".sm-agent-pick.open")) closeProtoMenu();
    page.classList.toggle("loading", !data?.sessions && !failed);
    page.replaceChildren(head(), body());
    const wrap = page.querySelector(".sm-switch");
    const tabs = wrap.querySelector(".sm-agents");
    const pick = wrap.querySelector(".sm-agent-pick");
    const filter = page.querySelector(".sm-filter");
    const fit = () => {
      filter.style.flexBasis = "";
      const room = parseFloat(getComputedStyle(wrap).width);
      if (!room) return;
      const focused = document.activeElement;
      // Measure the actual labels, including language, font and text zoom,
      // rather than guessing a breakpoint or a number of agents.
      tabs.hidden = false;
      // the search gives way to the tabs, down to its least, before the menu
      // takes them (Settings › came into the head beside them)
      // (a narrow window's head is a grid, where it can't)
      const over = tabs.scrollWidth - Math.floor(room);
      const fw = getComputedStyle(filter.parentElement).display === "flex" ? filter.offsetWidth : 0;
      const compact = over > 0 && (!fw || fw - over < 110);
      if (over > 0 && !compact) filter.style.flexBasis = Math.ceil(fw - over) + "px";
      tabs.hidden = compact || !data?.agents.length;
      pick.hidden = !compact || !current();
      if (!compact && pick.classList.contains("open")) closeProtoMenu();
      if (compact && tabs.contains(focused)) pick.focus({ preventScroll: true });
      else if (!compact && focused === pick) tabs.querySelector(".on")?.focus({ preventScroll: true });
    };
    // the head, not the tabs' box, which the search's width changes
    fitObserver = new ResizeObserver(fit);
    fitObserver.observe(page.querySelector(".sm-head"));
    fit();
    if (keepFocus) (pick.hidden ? tabs.querySelector(".on") : pick)?.focus({ preventScroll: true });
  }

  function chooseAgent(name) {
    if (name === data.agent && !trashOn) return;
    trashOn = false;
    selecting = false;
    agent = name;
    try { localStorage.setItem("magpie.sessionsAgent", name); } catch {}
    picked.clear();
    detail = "";
    data = { ...data, agent: name, sessions: null };
    draw();
    load(name);
  }

  // The gateway conversations setting (#1355): a card above the list, shown
  // only where it applies — when this agent has sessions seen through the
  // gateway, in gateway mode, or once recording is on or text is kept. A
  // switch that says what it keeps and where, and, while there is kept
  // text, a delete that says what it deletes and asks first in magpie's own
  // dialog (lee on Discord: a bare checkbox and a lone trash icon under the
  // tabs read as stray controls).
  const recordingApplies = () => typeof data?.recording === "boolean"
    && (data.recording || data.recorded || (typeof gatewayMode !== "undefined" && gatewayMode) || !!data.sessions?.some((s) => s.gateway));

  function recordingCard() {
    const on = data.recording;
    const card = el("div", "list row sm-record" + (on ? " on" : ""));
    const sw = el("button", "lib-switch sm-record-switch" + (on ? " on" : ""));
    sw.type = "button";
    sw.setAttribute("role", "switch");
    sw.setAttribute("aria-checked", String(on));
    sw.disabled = recordingBusy;
    sw.append(el("i"));
    const words = el("div", "sm-record-words");
    const name = el("span", "sm-record-name", t("Record gateway conversations"));
    name.id = "smRecordName";
    sw.setAttribute("aria-labelledby", name.id);
    words.append(name, el("span", "sm-record-sub", t(on
      ? "On: what agents send through the gateway, and the replies, are kept on this computer only, for up to 7 days (256 MiB), to read under each gateway session"
      : "Off: gateway sessions show only what they used. Turned on, their text is kept on this computer only, for up to 7 days (256 MiB)")));
    sw.onclick = async (e) => {
      e.stopPropagation();
      if (sw.disabled) return;
      if (!on && !await confirmAction(t("Record gateway conversations"), t("Prompts, replies and tool results may contain private code and files. Store them on this server for up to 7 days (256 MiB total)? Recognized secrets are masked, but this is not a privacy guarantee."), t("Enable"))) return;
      recordingBusy = true;
      sw.disabled = true;
      try {
        const result = await api("sessions/recording", { on: !on });
        data.recording = result.recording;
      } catch (err) { status(err.message, "err"); }
      finally { recordingBusy = false; draw(); }
    };
    card.append(sw, words);
    if (data.recorded) {
      const clear = el("button", "text sm-record-clear");
      clear.type = "button";
      clear.disabled = recordingBusy;
      clear.append(svg(TRASH, 13, 1.4), el("span", "", t("Delete saved conversations…")));
      clear.onclick = async (e) => {
        e.stopPropagation();
        if (!await confirmAction(t("Stop recording and clear saved conversations"), t("Delete all locally recorded gateway conversation content? Usage totals and native session files are kept."), t("Delete"))) return;
        recordingBusy = true;
        clear.disabled = true;
        try { await api("sessions/recording", { clear: true }); talks.clear(); await load(); }
        catch (err) { status(err.message, "err"); }
        finally { recordingBusy = false; draw(); }
      };
      card.append(clear);
    }
    return card;
  }

  // The consent (RustCats on X: a new install showed every agent's history
  // before anyone agreed to it). Until the reader says which agents'
  // sessions magpie may read (data.reads.asked), it reads none of them and
  // this card comes first: the agents found here, each with its own
  // switch, off; Allow selected reads those picked, and Allow all every
  // agent, those found later too. Settings › Privacy changes it later.
  // In gateway mode no agent's files here are read (reads.gateway), so
  // there is nothing to ask.
  const consentApplies = () => data?.reads && data.reads.asked === false && !data.reads.gateway;

  function consentCard() {
    const found = data.reads.found || [];
    const card = el("div", "sm-consent");
    const title = el("b", "sm-consent-title", t("Show your agents' sessions?"));
    title.id = "smConsentTitle";
    card.setAttribute("role", "group");
    card.setAttribute("aria-labelledby", title.id);
    card.append(title, el("p", "sm-consent-sub", t("magpie reads no agent's session history until you allow it. Pick the agents whose sessions this page may show; what magpie's gateway served is counted either way. You can change this in Settings › Privacy.")));
    if (found.length) {
      const list = el("div", "list sm-consent-list");
      for (const x of found) {
        const on = consentPicked.has(x.agent);
        const row = el("div", "row sm-consent-row");
        const name = el("span", "sm-consent-name", x.name);
        name.id = "smConsent-" + x.agent;
        const sw = el("button", "lib-switch" + (on ? " on" : ""));
        sw.type = "button";
        sw.setAttribute("role", "switch");
        sw.setAttribute("aria-checked", String(on));
        sw.setAttribute("aria-labelledby", name.id);
        sw.disabled = consentBusy;
        sw.append(el("i"));
        sw.onclick = (e) => {
          e.stopPropagation();
          if (consentPicked.has(x.agent)) consentPicked.delete(x.agent); else consentPicked.add(x.agent);
          const now = consentPicked.has(x.agent);
          sw.classList.toggle("on", now);
          sw.setAttribute("aria-checked", String(now));
          const some = card.querySelector(".sm-consent-some");
          if (some) some.disabled = consentBusy || !consentPicked.size;
        };
        row.onclick = () => sw.click();
        row.append(icon(x.icon), name, sw);
        list.append(row);
      }
      card.append(list);
    } else {
      card.append(el("p", "sm-consent-none", t("No agent's sessions were found on this computer yet.")));
    }
    const acts = el("div", "sm-consent-acts");
    const save = async (all) => {
      consentBusy = true;
      draw();
      const agents = {};
      for (const x of found) agents[x.agent] = all || consentPicked.has(x.agent);
      try {
        await api("sessions/reads", { all, agents });
        consentPicked.clear();
        await load();
      } catch (err) { status(err.message, "err"); }
      finally { consentBusy = false; draw(); }
    };
    if (found.length) {
      const some = el("button", "text sm-consent-some", t("Allow selected"));
      some.type = "button";
      some.disabled = consentBusy || !consentPicked.size;
      some.onclick = () => save(false);
      acts.append(some);
    }
    const all = el("button", "text primary sm-consent-all", t("Allow all"));
    all.type = "button";
    all.disabled = consentBusy;
    all.onclick = () => save(true);
    acts.append(all);
    card.append(acts);
    return card;
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
        const opts = data.agents.map((x) => ({ v: x.agent, name: x.name, icon: x.icon, note: String(x.count), literalName: true }));
        openProtoMenu(pick, opts, data.agent, (name) => {
          pick.focus({ preventScroll: true });
          chooseAgent(name);
        }, "Agent", "sm-agent-menu");
        iconItems(opts);
      };
    }
    wrap.append(tabs, pick);
    h.append(wrap);
    const q = el("input", "sess-filter sm-filter");
    q.type = "search";
    q.spellcheck = false;
    q.autocomplete = "off";
    q.placeholder = t("Search sessions");
    q.value = query;
    q.hidden = trashOn;
    q.oninput = () => { query = q.value; drawnIn.clear(); searchSoon(); redrawList(); };
    q.onkeydown = (e) => { if (e.key === "Escape" && q.value) { e.stopPropagation(); q.value = ""; query = ""; drawnIn.clear(); searchSoon(); redrawList(); } };
    const sel = el("button", "text sm-select" + (selecting ? " on" : ""), t(selecting ? "Done" : "Select"));
    sel.type = "button";
    sel.setAttribute("aria-pressed", String(selecting));
    sel.hidden = trashOn || !a?.deletable || !data?.sessions?.some((s) => s.deletable);
    sel.onclick = () => {
      selecting = !selecting;
      if (!selecting) picked.clear();
      draw();
    };
    const tr = el("button", "text sm-trash-btn" + (trashOn ? " on" : ""));
    tr.type = "button";
    tr.setAttribute("aria-pressed", String(trashOn));
    tr.append(svg(TRASH, 13, 1.4), el("span", "", t("Trash")));
    if (data?.trash.length) tr.append(el("em", "", String(data.trash.length)));
    tr.onclick = () => { trashOn = !trashOn; selecting = false; picked.clear(); draw(); speedsOn(); };
    // which agents' history is read is Settings › Privacy's (the scattered
    // settings audit after netube99 on X); gateway mode reads none there
    const go = el("button", "text sm-toset");
    go.type = "button";
    go.append(el("span", "", t("Settings")), " ›");
    go.hidden = !!data?.reads?.gateway;
    go.onclick = (e) => openSettingsAt(e, "privacy", "#sessionReadsList .row");
    h.append(q, sel, tr, go);
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
    if (consentApplies()) box.append(consentCard());
    if (recordingApplies()) box.append(recordingCard());
    if (!data.agents.length) {
      if (consentApplies()) return box;
      const e = el("div", "empty-state");
      e.append(el("b", "", t("No sessions yet")), el("span", "", data.reads?.gateway
        // gateway mode lists no session of this computer's agents
        ? t("The sessions of agents on other computers that use this gateway show up here, by the folder they ran in.")
        : t("Claude Code's, Codex's, Hermes's, OpenCode's and Pi's sessions on this computer show up here, by the folder they ran in.")));
      box.append(e);
      return box;
    }
    const a = current();
    // what can't be deleted says so; the gateway's sessions, which can,
    // say why they can't be resumed and what Delete removes of them
    const locked = data.sessions.filter((s) => !s.deletable);
    if (a && (locked.length || !a.deletable)) {
      const canResume = locked.some((s) => s.resume);
      const key = locked.some((s) => s.read_only)
        ? canResume
          ? "Some {agent} sessions are read only and cannot be deleted."
          : "These {agent} sessions are read only; magpie can list them, but cannot resume or delete them."
        : canResume
          ? "magpie can list {agent}'s sessions and resume them, but not delete them: they aren't kept as files of their own."
          : "magpie can list {agent}'s sessions, but cannot resume or delete them.";
      box.append(el("p", "usage-note sm-note", t(key, { agent: a.name })));
    }
    if (a && data.sessions.some((s) => s.gateway)) {
      box.append(el("p", "usage-note sm-note sm-gw-note", t(data.sessions.every((s) => s.gateway)
        ? "These {agent} sessions were seen only through magpie's gateway: {agent}'s own files of them aren't on this computer (it may run on another one, in a container or with another config folder), so they can't be resumed here. Delete takes one off this list and moves any conversation magpie recorded of it to its trash; Usage keeps what it spent."
        : "Sessions under \"Seen through the gateway\" have no {agent} files on this computer, so they can't be resumed here. Delete takes one off this list and moves any conversation magpie recorded of it to its trash; Usage keeps what it spent.", { agent: a.name })));
    }
    const prov = providerBar();
    if (prov) box.append(prov);
    box.append(selectBar(), el("div", "sm-tree"));
    queueMicrotask(redrawList);
    return box;
  }

  // Codex lists in its history only the sessions made with the provider it
  // uses now; ones made with another (CC Switch's "custom") are there as
  // files but hidden from it (#887). They can be moved to Codex's provider
  // here, picked in a dialog first, and the last move undone.
  const otherProvider = (s) => !s.read_only && s.provider && s.uses_provider && s.provider !== s.uses_provider;
  let lastMove = null; // { agent, moved: [{ id, from, to }] }

  function providerBar() {
    if (data.agent !== "codex") return null;
    const hidden = data.sessions.filter(otherProvider);
    const undo = lastMove?.agent === data.agent && lastMove.moved.length ? lastMove : null;
    if (!hidden.length && !undo) return null;
    const bar = el("div", "row sm-provider");
    if (hidden.length) {
      const from = [...new Set(hidden.map((s) => s.provider))].join(", ");
      const to = hidden[0].uses_provider;
      bar.append(el("span", "sm-provider-text", t(hidden.length === 1
        ? "1 Codex session was made with another provider ({from}): Codex lists only {to}'s sessions in its history now, so it doesn't show there."
        : "{n} Codex sessions were made with another provider ({from}): Codex lists only {to}'s sessions in its history now, so they don't show there.", { n: hidden.length, from, to })));
      const go = el("button", "text sm-provider-move", t("Move to {to}…", { to }));
      go.type = "button";
      go.onclick = (e) => { e.stopPropagation(); askProvider(hidden); };
      bar.append(el("span", "grow"), go);
    } else {
      bar.append(el("span", "sm-provider-text", t(undo.moved.length === 1 ? "Moved 1 session to {to}" : "Moved {n} sessions to {to}", { n: undo.moved.length, to: undo.moved[0].to })), el("span", "grow"));
    }
    if (undo) {
      const back = el("button", "text sm-provider-undo");
      back.type = "button";
      back.append(svg(UNDO, 13, 1.4), el("span", "", t("Undo")));
      back.onclick = async (e) => {
        e.stopPropagation();
        back.disabled = true;
        const out = await moveProviders(undo.moved.map((m) => ({ id: m.id, to: m.from })));
        if (!out) { back.disabled = false; return; }
        lastMove = null;
        if (!out.refused.length) status(t(out.moved.length === 1 ? "Moved 1 session back" : "Moved {n} sessions back", { n: out.moved.length }), "ok");
        await load();
      };
      bar.append(back);
    }
    return bar;
  }

  // moveProviders posts the moves, says what was refused, and gives the
  // reply (null when the post failed)
  async function moveProviders(moves) {
    let out;
    try {
      out = await api("sessions/codex-provider", { moves });
    } catch (err) {
      status(err.message, "err");
      return null;
    }
    if (out.refused.length) {
      const first = out.refused[0], s = data.sessions.find((x) => x.id === first.id);
      const active = out.refused.filter((r) => r.active);
      status(active.length ? t(active.length === 1 ? "{title} is still being written to; close it in {agent} and try again in a minute" : "{n} sessions are still being written to; close them in {agent} and try again in a minute", { title: s?.title || first.id, n: active.length, agent: "Codex" })
        : (s?.title || first.id) + ": " + first.error, "err");
    }
    return out;
  }

  // askProvider: the sessions to move, each ticked, to untick before Move
  function askProvider(list) {
    const to = list[0].uses_provider;
    const chosen = new Set(list.map((s) => s.id));
    const ed = el("div", "editor sm-ask sm-provider-ask");
    const h = el("div", "ehead");
    h.append(icon(current()?.icon || "generic"), el("b", "", t("Move sessions to {to}?", { to })));
    ed.append(h);
    const names = el("ul", "sm-ask-list sm-provider-list");
    const go = el("button", "text primary", t("Move"));
    // over a longer list: how many are picked, Select all (Deselect all
    // once all are) and Invert selection (Cyrus on Discord)
    const pick = el("div", "sm-provider-pick");
    const n = el("span", "note sm-count");
    const all = el("button", "text sm-pick-all");
    const flip = el("button", "text sm-pick-invert", t("Invert selection"));
    all.type = flip.type = "button";
    pick.append(n, el("span", "grow"), all, flip);
    if (list.length > 1) ed.append(pick);
    const boxes = [];
    const sync = () => {
      for (const [c, id] of boxes) c.checked = chosen.has(id);
      go.disabled = !chosen.size;
      n.textContent = t("{n} selected", { n: chosen.size });
      all.textContent = t(chosen.size === list.length ? "Deselect all" : "Select all");
    };
    all.onclick = (e) => { e.stopPropagation(); const on = chosen.size < list.length; for (const s of list) on ? chosen.add(s.id) : chosen.delete(s.id); sync(); };
    flip.onclick = (e) => { e.stopPropagation(); for (const s of list) chosen.has(s.id) ? chosen.delete(s.id) : chosen.add(s.id); sync(); };
    for (const s of list) {
      const li = el("li");
      const lab = el("label");
      const c = el("input", "sm-check");
      c.type = "checkbox";
      c.onchange = () => { c.checked ? chosen.add(s.id) : chosen.delete(s.id); sync(); };
      boxes.push([c, s.id]);
      lab.append(c, el("span", "name", s.title || s.id), el("span", "sm-prov", s.provider), el("span", "note", ago(s.last)));
      li.append(lab);
      names.append(li);
    }
    sync();
    ed.append(names);
    ed.append(el("p", "lib-confirm", t("Only the provider each one names is changed, in its files and in Codex's database: its messages, id, title and archive stay as they are. A copy of its files is kept in magpie's trash folder first, and Undo moves them back. A session written to in the last minute is left alone.")));
    const bar = el("div", "bar");
    go.type = "button";
    go.onclick = async (e) => {
      e.stopPropagation();
      go.disabled = true;
      go.classList.add("busy");
      const out = await moveProviders([...chosen].map((id) => ({ id, to })));
      if (!out) { go.disabled = false; go.classList.remove("busy"); return; }
      closeConfirmAsk();
      lastMove = { agent: data.agent, moved: out.moved };
      if (!out.refused.length) status(t(out.moved.length === 1 ? "Moved 1 session to {to}" : "Moved {n} sessions to {to}", { n: out.moved.length, to }), "ok");
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

  // the bar over the tree while picking: pick every session shown, and
  // delete those picked
  function selectBar() {
    const bar = el("div", "row-head sm-bar");
    const a = current();
    if (!a?.deletable || !selecting) { bar.hidden = true; return bar; }
    const list = shown().filter((s) => s.deletable);
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
    del.onclick = () => askDelete([...picked], wholeFolder([...picked]));
    bar.append(all, n, el("span", "grow"), del);
    return bar;
  }

  // wholeFolder: the folder (a cwd, "" for none) when ids are every
  // session of that one folder, so the dialog names it
  function wholeFolder(ids) {
    const all = data?.sessions || [];
    const set = new Set(ids);
    const cwds = new Set(all.filter((s) => set.has(s.id)).map((s) => groupOf(s)));
    if (cwds.size !== 1) return undefined;
    const [cwd] = cwds;
    return all.filter((s) => groupOf(s) === cwd).every((s) => set.has(s.id)) ? cwd : undefined;
  }

  function redrawList() {
    const tree = page.querySelector(".sm-tree");
    if (!tree || !data) return;
    const bar = page.querySelector(".sm-bar");
    if (bar) bar.replaceWith(selectBar());
    const list = shown();
    // the project of the latest session is open the first time an agent is
    // shown
    if (openedFor !== data.agent) {
      openedFor = data.agent;
      opened.clear();
      drawnIn.clear();
      const first = list.find((s) => !scratch(groupOf(s)) && groupOf(s) !== DRAFTS) || list[0];
      if (first) opened.add(groupOf(first));
    }
    nest(list);
    // a session shown under another stays in sight: its folder and every
    // session it is under are unfolded
    for (const id of [detail, focus?.agent === data.agent ? focus.id : ""]) {
      if (!id || !rootOf.has(id)) continue;
      const byId = new Map(list.map((s) => [s.id, s]));
      for (let p = byId.get(byId.get(id)?.parent_session), n = 0; p && n < MAX_DEPTH; p = byId.get(p.parent_session), n++) subsOpen.add(p.agent + "/" + p.id);
    }
    const groups = new Map();
    for (const s of list) {
      if (rootOf.has(s.id)) continue;
      const k = groupOf(s);
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k).push(s);
    }
    tree.replaceChildren();
    folderBoxes.clear();
    const note = searchNote();
    if (note) tree.append(note);
    if (!list.length) {
      if (!search()?.busy) tree.append(el("div", "empty-state", query ? t("No session matches.") : t("No sessions yet")));
      return;
    }
    const q = query.trim();
    // the projects in one list, the folders apps and the system made
    // folded under them, and the folders agents made one session each
    // (DRAFTS) after the projects: they are no projects, but the reader
    // looks for the sessions in them, so not folded away either
    const all = [...groups].filter(([k]) => !scratch(k) && k !== DRAFTS);
    const rest = [...groups].filter(([k]) => scratch(k));
    const temp = groups.get(DRAFTS);
    if (all.length || temp) {
      const box = el("div", "list sm-folders");
      if (all.length) folderList(box, all, FOLDERS, q);
      if (temp) box.append(folder(DRAFTS, temp, q));
      tree.append(box);
    }
    if (rest.length) tree.append(othersBox(rest, q));
    toFocus();
  }

  // folderList draws into box a page of folders, or as many as were drawn
  // before, and down to the folder of the session opened to its details
  function folderList(box, all, key, q) {
    const top = rootOf.get(detail) || detail;
    const at = detail ? all.findIndex(([, items]) => items.some((s) => s.id === top)) : -1;
    const n = Math.min(all.length, Math.max(drawnIn.get(key) || SM_PAGE, at + 1));
    for (const [cwd, items] of all.slice(0, n)) box.append(folder(cwd, items, q));
    if (n < all.length) box.append(moreFolders(all, n, q, key));
    return box;
  }

  // othersBox is the fold of the folders apps and the system made: shut
  // until the reader opens it, a search finds something in it, or the
  // session opened to its details is in it
  function othersBox(rest, q) {
    const box = el("div", "list sm-others");
    const top = rootOf.get(detail) || detail;
    const open = othersOpen || !!q || (!!detail && rest.some(([, items]) => items.some((s) => s.id === top)));
    const r = el("div", "row sm-others-row");
    const fold = el("button", "fold sm-others-fold");
    fold.type = "button";
    fold.setAttribute("aria-expanded", String(open));
    const words = el("span", "sm-fwords");
    words.append(el("span", "name", t("Other folders")), el("span", "sm-latest", t("Temporary folders, the disk's root and apps' hidden folders")));
    fold.append(svg(CHEV_R, 10, 1.6), words);
    fold.onclick = () => { othersOpen = !open; redrawList(); };
    r.append(fold, el("span", "note", t(rest.length === 1 ? "{n} folder" : "{n} folders", { n: rest.length })), folderSlot());
    box.append(r);
    if (open) folderList(box, rest, OTHERS, q);
    return box;
  }

  // nest finds the sessions shown that are under another one shown: a
  // Codex thread, or a Claude branch, whose parent_session is, by a chain
  // with no loop in it
  function nest(list) {
    const byId = new Map(list.map((s) => [s.id, s]));
    kidsOf = new Map();
    rootOf = new Map();
    for (const s of list) {
      let p = byId.get(s.parent_session), root = null;
      const seen = new Set([s.id]);
      while (p && !seen.has(p.id) && seen.size <= MAX_DEPTH) {
        seen.add(p.id);
        root = p;
        p = byId.get(p.parent_session);
      }
      // a loop, or a chain deeper than is drawn, leaves it on top
      if (!root || p) continue;
      rootOf.set(s.id, root.id);
      const kids = kidsOf.get(s.parent_session) || [];
      kids.push(s);
      kidsOf.set(s.parent_session, kids);
    }
    for (const kids of kidsOf.values()) kids.sort((a, b) => (a.start || a.last || "").localeCompare(b.start || b.last || ""));
  }

  // moreFolders adds the next page of folders above itself, as moreRow does
  // a folder's sessions
  function moreFolders(all, n, q, key) {
    drawnIn.set(key, n);
    const b = el("button", "text sess-page-more sm-more-folders");
    b.type = "button";
    b.dataset.unrolls = ""; // it goes down with the folders it opens
    const label = () => t("Show {n} more folders", { n: Math.min(SM_PAGE, all.length - n) });
    b.textContent = label();
    b.onclick = (e) => {
      e.stopPropagation();
      const next = Math.min(all.length, n + SM_PAGE);
      b.before(...all.slice(n, next).map(([cwd, items]) => folder(cwd, items, q)));
      n = next;
      drawnIn.set(key, n);
      if (n >= all.length) b.remove();
      else b.textContent = label();
    };
    return b;
  }

  // A folder's row: its name and where it is, the latest session's title
  // under them, and when that was and how many there are on the right.
  // What can be done to the whole folder is in its ⋯ menu, shown on hover.
  function folder(cwd, items, q) {
    const open = !!q || opened.has(cwd);
    const g = el("div", "sm-group");
    const r = el("div", "row sm-folder");
    // the sessions under those on top are the folder's too
    const members = [];
    const under = (s, n) => { members.push(s); if (n < MAX_DEPTH) for (const k of kidsOf.get(s.id) || []) under(k, n + 1); };
    for (const s of items) under(s, 0);
    const writable = members.filter((s) => s.deletable);
    // the folder's box picks every session of it shown, folded or not,
    // for the bar's Delete; the bar still counts sessions (#527)
    if (selecting && current()?.deletable && writable.length) {
      const c = el("input", "sm-check sm-folder-check");
      c.type = "checkbox";
      // a session's own box ticked or not shows here at once
      c.sync = () => {
        c.checked = writable.every((s) => picked.has(s.id));
        c.indeterminate = !c.checked && writable.some((s) => picked.has(s.id));
      };
      c.sync();
      folderBoxes.set(cwd, c);
      c.setAttribute("aria-label", t("Select every session in {folder}", { folder: groupName(cwd) }));
      c.onclick = (e) => e.stopPropagation();
      c.onchange = () => {
        for (const s of writable) c.checked ? picked.add(s.id) : picked.delete(s.id);
        redrawList();
      };
      r.append(c);
    }
    const fold = el("button", "fold");
    fold.type = "button";
    fold.setAttribute("aria-expanded", String(open));
    const path = cwd === GW || cwd === DRAFTS ? "" : cwd;
    const line = el("span", "sm-fline");
    line.append(el("span", "name", groupName(cwd)));
    const where = path ? whereOf(path) : "";
    if (where) line.append(el("span", "sub sm-path", where));
    const words = el("span", "sm-fwords");
    words.append(line);
    const latest = members.reduce((x, s) => (s.last || "") > (x.last || "") ? s : x, members[0]);
    if (latest) words.append(el("span", "sm-latest", latest.title || t("(no prompt)")));
    fold.append(svg(CHEV_R, 10, 1.6), words);
    fold.onclick = () => {
      if (opened.has(cwd)) opened.delete(cwd); else opened.add(cwd);
      redrawList();
    };
    const when = el("span", "sm-when");
    if (latest?.last) when.append(el("span", "sm-ago", ago(latest.last)));
    when.append(el("span", "note", t(members.length === 1 ? "{n} session" : "{n} sessions", { n: members.length })));
    r.append(fold, when);
    const acts = [];
    const allInFolder = (data.sessions || []).filter((s) => groupOf(s) === cwd);
    if (data.agent === "claude" && path && allInFolder.every((s) => !s.read_only && !s.gateway)
      && new Set(allInFolder.map((s) => s.wsl || "")).size === 1) {
      acts.push({ name: "Relocate project", icon: MOVE, run: () => askRelocate(cwd, allInFolder[0].wsl || "") });
    }
    if (current()?.deletable && writable.length) {
      acts.push({ name: "Delete all sessions", icon: TRASH, sep: acts.length > 0, run: () => askDelete(writable.map((s) => s.id), wholeFolder(writable.map((s) => s.id))) });
    }
    if (acts.length) {
      const more = el("button", "copy sm-folder-more");
      more.type = "button";
      more.title = t("More");
      more.setAttribute("aria-label", t("More"));
      more.setAttribute("aria-haspopup", "menu");
      more.append(svg(DOTS, 14, 2));
      more.onclick = (e) => {
        e.stopPropagation();
        if (more.classList.contains("open")) return closeAgentMenu();
        openRowMenu(more, acts);
      };
      r.append(more);
      r.oncontextmenu = (e) => { e.preventDefault(); openRowMenu(more, acts); };
    } else r.append(folderSlot());

    r.title = path;
    g.append(r);
    if (open) {
      // a page, or as many as were drawn before, and down to the session
      // opened to its details (from the Usage page's list, #752)
      const top = rootOf.get(detail) || detail;
      const at = detail ? items.findIndex((s) => s.id === top) : -1;
      const n = Math.min(items.length, Math.max(drawnIn.get(cwd) || SM_PAGE, at + 1));
      for (const s of items.slice(0, n)) g.append(item(s));
      if (n < items.length) g.append(moreRow(cwd, items, n));
    }
    return g;
  }

  // moreRow adds the folder's next page of sessions above itself, leaving
  // the rows drawn as they are, so what the reader looks at doesn't move
  function moreRow(cwd, items, n) {
    drawnIn.set(cwd, n);
    const b = el("button", "text sess-page-more sm-more");
    b.type = "button";
    b.dataset.unrolls = ""; // it goes down with the rows it opens
    const label = () => t("Show {n} more", { n: Math.min(SM_PAGE, items.length - n) });
    b.textContent = label();
    b.onclick = (e) => {
      e.stopPropagation();
      const next = Math.min(items.length, n + SM_PAGE);
      b.before(...items.slice(n, next).map((s) => item(s)));
      n = next;
      drawnIn.set(cwd, n);
      if (n >= items.length) b.remove();
      else b.textContent = label();
    };
    return b;
  }

  // lanes says which of a row's controls some session of the list has, so
  // a row without one keeps its room and every row's columns line up
  const laneOf = new WeakMap();
  function lanes() {
    const list = data?.sessions || [];
    let l = laneOf.get(list);
    if (!l) {
      const resume = list.some((s) => s.resume);
      l = { resume, term: resume && !!data.terminal, carry: list.some((s) => s.carry?.length) };
      laneOf.set(list, l);
    }
    return l;
  }
  // slot is a control's room, unseen and out of reach
  function slot(tag, cls, text) {
    const b = el(tag, "sm-slot " + cls, text);
    b.setAttribute("aria-hidden", "true");
    b.inert = true;
    b.tabIndex = -1;
    return b;
  }
  const folderSlot = () => slot("span", "copy sm-folder-slot");
  // undeletableText is why a session magpie lists can't be deleted
  function undeletableText(s, a) {
    if (s.undeletable === "cursor-editor") return t("Cursor's editor keeps this chat in its own database, which it holds open while it runs: delete it in Cursor.");
    return t("This {agent} session is read only, so magpie can't delete it.", { agent: a.name });
  }

  function item(s, depth = 0) {
    const a = current();
    const wrap = el("div", "sess-item sm-item" + (detail === s.id ? " open" : ""));
    wrap.dataset.depth = String(depth);
    const r = el("div", "row sess sm-sess");
    r.dataset.id = s.id;
    if (selecting && a?.deletable && s.deletable) {
      const c = el("input", "sm-check");
      c.type = "checkbox";
      c.checked = picked.has(s.id);
      c.setAttribute("aria-label", t("Select"));
      c.onclick = (e) => e.stopPropagation();
      c.onchange = () => { c.checked ? picked.add(s.id) : picked.delete(s.id); const bar = page.querySelector(".sm-bar"); if (bar) bar.replaceWith(selectBar()); folderBoxes.get(groupOf(s))?.sync(); };
      r.append(c);
    }
    const who = el("div", "who");
    who.append(el("div", "name", s.title || t("(no prompt)")));
    const sub = el("div", "sub", [ago(s.last), sessModelsText(s), s.messages ? t(s.messages === 1 ? "{n} message" : "{n} messages", { n: s.messages }) : "",
      // a gateway session's size is the conversation magpie recorded of it,
      // and none is said when it recorded none; its title is its id already
      s.gateway ? (s.size ? t("{size} recorded", { size: fmtBytes(s.size) }) : "") : fmtBytes(s.size),
      s.title === s.id ? "" : s.id.slice(0, 8)].filter(Boolean).join(" · "));
    if (s.via?.length) sub.title = s.via.map(viaText).join("\n");
    if (s.wsl) sub.prepend(wslBadge(s), " ");
    if (otherProvider(s)) {
      const p = el("span", "sm-prov", s.provider);
      p.title = t("Made with {from}: Codex's history lists only {to}'s sessions now", { from: s.provider, to: s.uses_provider });
      sub.prepend(p, " ");
    }
    who.append(sub);
    // a draft session's folder is a name its agent made: the row says
    // nothing of it otherwise (the group of them is one)
    if (zcodeDraft(s)) r.title = s.cwd;
    const hit = hitOf(s);
    if (hit) who.append(hitLine(hit));
    // what it spent, as the Usage page's list shows it
    r.append(who, ...sessSpent(s));
    r.append(speedCell(s));
    const lane = lanes();
    if (s.resume) {
      const res = el("button", "sess-resume", t("Resume"));
      res.type = "button";
      // as wide whichever it says
      res.dataset.wide = t("Resume");
      res.dataset.wide2 = t("Copied");

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
      if (lane.term) {
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
    } else if (lane.resume) {
      const res = slot("button", "sm-slot-resume", t("Resume"));
      res.dataset.wide = t("Resume");
      res.dataset.wide2 = t("Copied");
      r.append(res);
      if (lane.term) r.append(slot("span", "copy sm-slot-icon"));
    }
    if (s.carry?.length) r.append(carryPick(s));
    else if (lane.carry) {
      const b = slot("button", "sess-pick sm-slot-carry");
      b.append(el("span", "", t("Continue in")), svg(CHEV, 11, 1.6));
      r.append(b);
    }
    // every session of an agent magpie deletes from has a delete; one it
    // can't delete says why on it (ysm119 on Discord: some Cursor rows had
    // none, and every column after it moved)
    if (a?.deletable) {
      const del = el("button", "copy sm-del");
      del.type = "button";
      const why = s.deletable ? "" : undeletableText(s, a);
      del.title = why || t("Delete");
      del.setAttribute("aria-label", why ? t("Delete") + ": " + why : t("Delete"));
      del.append(svg(TRASH, 13, 1.4));
      if (why) {
        del.classList.add("off");
        del.setAttribute("aria-disabled", "true");
        del.onclick = (e) => { e.stopPropagation(); status(why, "err"); };
      } else del.onclick = (e) => { e.stopPropagation(); askDelete([s.id]); };
      r.append(del);
    }
    // only the session opened and the one closed are drawn again
    r.onclick = () => {
      if (window.getSelection()?.toString()) return;
      const was = detail;
      detail = detail === s.id ? "" : s.id;
      if (was && was !== s.id) {
        const old = page.querySelector(`.row.sm-sess[data-id="${CSS.escape(was)}"]`)?.closest(".sm-item");
        const prev = data.sessions.find((x) => x.id === was);
        if (old && prev) old.replaceWith(item(prev, +old.dataset.depth || 0));
      }
      wrap.replaceWith(item(s, depth));
    };
    wrap.append(r);
    if (detail === s.id) wrap.append(details(s));
    const kids = depth < MAX_DEPTH ? kidsOf.get(s.id) || [] : [];
    if (kids.length || s.subagents?.length) wrap.append(subagentsBox(s, kids, depth));
    return wrap;
  }

  // subagentsBox is the fold under a session with its subagents: those
  // its agent started inside it, each opened to its conversation, and the
  // Codex threads it started, drawn as sessions; and the Claude sessions
  // forked from it that went on (role "branch"), named apart
  function subagentsBox(s, kids, depth) {
    const box = el("div", "sm-subs");
    const k = s.agent + "/" + s.id;
    const subs = subTree(s.subagents || []);
    const branches = kids.filter((c) => c.role === "branch").length;
    const n = kids.length + subs.length - branches;
    const words = [];
    if (branches) words.push(t(branches === 1 ? "{n} branch" : "{n} branches", { n: branches }));
    if (n || !branches) words.push(t(n === 1 ? "{n} subagent" : "{n} subagents", { n }));
    const draw = () => {
      const open = subsOpen.has(k) || (!!query.trim() && kids.length > 0);
      const fold = el("button", "fold sm-subs-fold");
      fold.type = "button";
      fold.setAttribute("aria-expanded", String(open));
      fold.append(svg(CHEV_R, 10, 1.6), el("span", "", words.join(" · ")));
      fold.onclick = (e) => {
        e.stopPropagation();
        if (subsOpen.has(k)) subsOpen.delete(k); else subsOpen.add(k);
        draw();
      };
      const out = [fold];
      if (open) {
        const list = el("div", "sm-subs-list");
        for (const c of kids) list.append(item(c, depth + 1));
        for (const [sa, d] of subs) list.append(subRow(s, sa, d));
        out.push(list);
      }
      box.replaceChildren(...out);
    };
    box.style.setProperty("--sub-depth", String(depth));
    draw();
    return box;
  }

  // subTree orders a session's subagents as they started one another,
  // each with how deep it is under the session
  function subTree(subs) {
    const ids = new Set(subs.map((sa) => sa.id));
    const under = new Map();
    for (const sa of subs) {
      const p = sa.parent && ids.has(sa.parent) && sa.parent !== sa.id ? sa.parent : "";
      if (!under.has(p)) under.set(p, []);
      under.get(p).push(sa);
    }
    const out = [];
    const seen = new Set();
    const walk = (p, d) => {
      for (const sa of under.get(p) || []) {
        if (seen.has(sa.id)) continue;
        seen.add(sa.id);
        out.push([sa, Math.min(d, MAX_DEPTH)]);
        walk(sa.id, d + 1);
      }
    };
    walk("", 0);
    // one in a loop of parents is still shown, at the top
    for (const sa of subs) if (!seen.has(sa.id)) { seen.add(sa.id); out.push([sa, 0]); walk(sa.id, 1); }
    return out;
  }

  // subRow is one subagent of s: what it was asked, the agent it ran as,
  // and a click shows what was said in it
  function subRow(s, sa, d) {
    const k = s.agent + "/" + s.id + "/" + sa.id;
    const wrap = el("div", "sm-sub");
    wrap.style.setProperty("--sub-in", String(d));
    const r = el("button", "row sm-sub-row");
    r.type = "button";
    r.dataset.sub = sa.id;
    const who = el("span", "who");
    who.append(el("span", "name", sa.title || sa.kind || sa.id),
      el("span", "sub", [sa.title ? sa.kind : "", sa.last || sa.start ? ago(sa.last || sa.start) : "", sa.id.slice(0, 8)].filter(Boolean).join(" · ")));
    r.append(who);
    const box = el("div", "sess-talk");
    box.dataset.talk = k;
    const show = () => {
      const on = talkOpen.has(k);
      r.setAttribute("aria-expanded", String(on));
      wrap.classList.toggle("open", on);
      box.hidden = !on;
      if (on) drawTalk(box, s, sa.id);
    };
    r.onclick = (e) => {
      e.stopPropagation();
      if (window.getSelection()?.toString()) return;
      if (talkOpen.has(k)) talkOpen.delete(k); else talkOpen.add(k);
      show();
    };
    wrap.append(r, box);
    show();
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
    if (s.wsl) line("WSL", s.wsl);
    if (s.cwd) line(t("Folder"), s.cwd);
    line(t("Session ID"), s.id, copyBtn(s.id, t("Session id")));
    if (s.resume) line(t("Resume"), el("code", "", s.resume), copyBtn(s.resume, t("Resume command")));
    for (const c of s.carry || []) {
      const agent = agentOf(c.agent).name;
      if (!c.convert) line(t("Continue in {agent}", { agent }), el("code", "", c.command), copyBtn(c.command, t("Command")));
      else {
        // made only when asked: a new session of that agent's, this one only read
        const b = copyBtn("", t("Command"));
        b.title = t("Convert, then copy the command");
        b.onclick = (e) => { e.stopPropagation(); carryOver(s, c.agent, false, b); };
        line(t("Continue in {agent}", { agent }), el("span", "v", t("A new {agent} session is made from this conversation", { agent })), b);
      }
    }
    sessUsageDetail(d, s, line);
    if (s.path) line(t("File"), s.path + (s.files > 1 ? " " + t("+{n} more", { n: s.files - 1 }) : ""));
    if (s.transcript) {
      const box = el("div", "sess-talk");
      box.dataset.talk = s.agent + "/" + s.id;
      const b = el("button", "text sess-talk-btn");
      b.type = "button";
      const show = () => {
        const on = talkOpen.has(s.agent + "/" + s.id);
        b.textContent = t(on ? "Hide conversation" : "Show conversation");
        b.setAttribute("aria-expanded", String(on));
        box.hidden = !on;
        if (on) drawTalk(box, s);
      };
      b.onclick = (e) => {
        e.stopPropagation();
        const k = s.agent + "/" + s.id;
        if (talkOpen.has(k)) talkOpen.delete(k); else talkOpen.add(k);
        show();
      };
      line(t("Conversation"), b, exportBtn(s));
      d.append(box);
      show();
    }
    return d;
  }

  // exportBtn saves a session's whole conversation as Markdown (#1276),
  // read from the agent's own file by the server: in magpie web the browser
  // downloads it, in the app it goes to Downloads and the status says where.
  function exportBtn(s) {
    const b = el("button", "text sess-export", t("Export Markdown"));
    b.type = "button";
    b.title = t("Save the whole conversation as a Markdown file");
    const q = "agent=" + encodeURIComponent(s.agent) + "&id=" + encodeURIComponent(s.id);
    b.onclick = async (e) => {
      e.stopPropagation();
      if (web) {
        const a = el("a");
        a.href = "/api/sessions/markdown?" + q;
        a.download = "";
        a.click();
        return;
      }
      if (b.disabled) return;
      b.disabled = true;
      b.classList.add("busy");
      try {
        const r = await api("sessions/export?" + q, {});
        status(t("Saved to {path}", { path: r.path }), "ok");
      } catch (err) {
        status(err.message, "err");
      } finally {
        b.disabled = false;
        b.classList.remove("busy");
      }
    };
    return b;
  }

  // agentOf is an agent as the page names it: of those with sessions, else
  // by its id
  // iconItems gives each agent in the menu just opened its icon, as its button has
  const iconItems = (opts) => document.querySelectorAll(".proto-menu .pm-item").forEach((b, i) => opts[i]?.icon && b.firstChild.after(icon(opts[i].icon)));
  const agentOf = (id) => data?.agents.find((x) => x.agent === id) || { agent: id, name: id, icon: id };

  // carryPick is the other agents that can carry a session on (#845):
  // from its file as it is (oh-my-pi forks a Pi session), or from a new
  // session of theirs converted from it (Claude Code and Codex), in the
  // app's menu: copy the command, or open it in the session terminal.
  function carryPick(s) {
    const b = el("button", "sess-pick sess-carry");
    b.type = "button";
    b.setAttribute("aria-haspopup", "menu");
    b.setAttribute("aria-expanded", "false");
    b.title = t("Continue this session in another agent");
    b.append(el("span", "", t("Continue in")), svg(CHEV, 11, 1.6));
    b.onclick = (e) => {
      e.stopPropagation();
      if (b.classList.contains("open")) return closeProtoMenu();
      const opts = [];
      for (const c of s.carry) {
        const { name, icon: ic } = agentOf(c.agent);
        const title = c.convert ? t("A new {agent} session is made from this conversation", { agent: name }) : c.command;
        opts.push({ v: "copy\x01" + c.agent, name, icon: ic, literalName: true, note: c.convert ? "Convert, then copy the command" : "Copy the command", title });
        if (data.terminal) opts.push({ v: "term\x01" + c.agent, name, icon: ic, literalName: true, note: c.convert ? "Convert, then open in session terminal" : "Open in session terminal", title });
      }
      openProtoMenu(b, opts, "", (v) => {
        b.focus({ preventScroll: true });
        const [how, to] = v.split("\x01");
        const c = s.carry.find((x) => x.agent === to);
        if (!c) return;
        if (c.convert) carryOver(s, to, how === "term");
        else if (how === "copy") copy(c.command, t("Command"));
        else api("sessions/terminal", { agent: s.agent, id: s.id, in: to }).then(() => status(t("Opening in session terminal"), "ok"), (err) => status(err.message, "err"));
      }, "Continue in another agent", "sess-carry-menu", "right");
      iconItems(opts);
    };
    return b;
  }

  // carryOver converts a session for another agent (Claude Code's for
  // Codex, Codex's for Claude Code) when the user picks it, then copies the
  // line that opens the new one, or opens it in the session terminal
  function carryOver(s, to, open, btn) {
    const agent = agentOf(to).name;
    api("sessions/carry", { agent: s.agent, id: s.id, to, open }).then((r) => {
      if (open) status(t("Made a new {agent} session from this one, opening in session terminal", { agent }), "ok");
      else copy(r.command, t("Command"), btn, t("Made a new {agent} session from this one, and copied the command that opens it", { agent }));
    }, (err) => status(err.message, "err"));
  }

  // drawTalk fills box with what was said in a session, read from the
  // agent's own file when it is first shown (only read, never written)
  function drawTalk(box, s, sub = "") {
    const k = s.agent + "/" + s.id + (sub ? "/" + sub : "");
    const got = talks.get(k);
    if (!got) {
      box.replaceChildren(el("p", "cx-none", t("Reading…")));
      const pending = { busy: true };
      talks.set(k, pending);
      const finish = (value) => {
        // A refresh or clear invalidates older responses, even for the same ID.
        if (talks.get(k) !== pending) return;
        talks.set(k, value);
        if (!talkOpen.has(k)) return;
        for (const currentBox of page.querySelectorAll(".sess-talk")) {
          if (currentBox.dataset.talk === k) drawTalk(currentBox, s, sub);
        }
      };
      api("sessions/transcript?agent=" + encodeURIComponent(s.agent) + "&id=" + encodeURIComponent(s.id) + (sub ? "&sub=" + encodeURIComponent(sub) : ""))
        .then((tr) => finish({ tr }), (err) => finish({ err: err.message }));
      return;
    }
    if (got.busy) { box.replaceChildren(el("p", "cx-none", t("Reading…"))); return; }
    if (got.err) { box.replaceChildren(el("p", "cx-none", got.err)); talks.delete(k); return; }
    const parts = got.tr?.parts || [];
    const out = [];
    if (!parts.length) out.push(el("p", "cx-none", t(got.tr?.source === "gateway" ? "No saved gateway content for this session" : "Nothing was said here")));
    // each reply names its model once, at its first part, and again
    // only where the model changes inside it (#2253): the model its own
    // line in the agent's file names, so none where the file names none
    let said = "", fresh = true;
    for (const p of parts) {
      let model = "";
      if (p.role === "assistant") {
        if (p.model && (fresh || p.model !== said)) model = p.model;
        said = p.model || "";
        fresh = false;
      } else if (p.role === "user") fresh = true;
      out.push(ledSaid(p, model));
    }
    if (got.tr?.cut) out.push(el("p", "cx-none", t("There was more than is shown here")));
    out.push(el("p", "cx-src", t(got.tr?.source === "gateway" ? "Recorded gateway traffic only; earlier, expired or uncaptured content may be missing" : "Read from the agent's session file; magpie keeps no copy")));
    box.replaceChildren(...out);
  }

  function askRelocate(from, wsl) {
    const ed = el("div", "editor sm-ask");
    const h = el("div", "ehead");
    h.append(icon("claude"), el("b", "", t("Relocate Claude Code project")));
    ed.append(h, el("p", "sub sm-ask-path", (wsl ? `WSL ${wsl}: ` : "") + from));
    ed.append(el("p", "lib-confirm", t("Close Claude Code and its editor sessions first. This moves the entire project's sessions, not just filtered or selected rows. Existing target storage is never overwritten. Desktop-only sessions are not changed.")));
    const label = el("label", "sm-ask-field", t("New absolute project path"));
    const input = el("input", "");
    input.type = "text";
    input.value = from;
    input.spellcheck = false;
    input.autocomplete = "off";
    input.style.width = "100%";
    label.append(input);
    ed.append(label);
    const info = el("p", "lib-confirm");
    info.setAttribute("aria-live", "polite");
    ed.append(info);
    const bar = el("div", "bar");
    const cancel = el("button", "text", t("Cancel"));
    const go = el("button", "text primary", t("Preview relocation"));
    cancel.type = go.type = "button";
    let preview = null;
    input.oninput = () => { preview = null; info.textContent = ""; go.textContent = t("Preview relocation"); };
    cancel.onclick = () => closeConfirmAsk();
    go.onclick = async () => {
      go.disabled = cancel.disabled = input.disabled = true;
      go.classList.add("busy");
      try {
        const out = await api("sessions/relocate-claude", { from, to: input.value, wsl, token: preview?.token || "" });
        if (out.backup) {
          info.textContent = t("Relocation complete. Original files and recovery manifest: {dir}", { dir: out.backup });
          go.hidden = true;
          input.disabled = true;
          cancel.textContent = t("Close");
          picked.clear();
          opened.add(out.to);
          await load();
        } else {
          preview = out;
          info.textContent = t("Move {n} sessions ({size}) from {from} to {to}. Originals are retained outside Claude's project list. Historical message text is unchanged.", {
            n: out.sessions, size: fmtBytes(out.bytes), from: out.from, to: out.to,
          });
          go.textContent = t("Confirm relocation");
        }
      } catch (err) {
        preview = null;
        info.textContent = err.message;
        go.textContent = t("Preview relocation");
      } finally {
        go.disabled = cancel.disabled = false;
        input.disabled = go.hidden;
        go.classList.remove("busy");
      }
    };
    bar.append(el("span", "grow"), cancel, go);
    ed.append(bar);
    confirmAsk = ed;
    openModal(ed);
    $("#modal").classList.add("lib");
    input.focus({ preventScroll: true });
  }

  // askDelete asks in magpie's dialog before the sessions go to its trash;
  // folder (a cwd, "" for none) when they are every session of one folder
  function askDelete(ids, folder) {
    const byID = new Map((data?.sessions || []).map((s) => [s.id, s]));
    const list = ids.map((id) => byID.get(id)).filter(Boolean);
    if (!list.length) return;
    const a = current();
    const ed = el("div", "editor sm-ask");
    const h = el("div", "ehead");
    const whole = folder !== undefined && list.length > 1;
    // picked folders each whole: how many folders, as well as sessions
    const cwds = new Set(list.map((s) => groupOf(s)));
    const folders = !whole && cwds.size > 1 && (data?.sessions || []).every((s) => !cwds.has(groupOf(s)) || ids.includes(s.id)) ? cwds.size : 0;
    h.append(icon(a.icon), el("b", "", list.length === 1 ? t("Delete this session?")
      : whole ? t("Delete all {n} sessions in {folder}?", { n: list.length, folder: groupName(folder) })
      : folders ? t("Delete all {n} sessions in {k} folders?", { n: list.length, k: folders })
      : t("Delete {n} sessions?", { n: list.length })));
    ed.append(h);
    if (folder && folder !== GW && folder !== DRAFTS) ed.append(el("p", "sub sm-ask-path", folder));
    const names = el("ul", "sm-ask-list");
    for (const s of list.slice(0, 5)) names.append(el("li", "", s.title || s.id));
    if (list.length > 5) names.append(el("li", "more", t("+{n} more", { n: list.length - 5 })));
    ed.append(names);
    // a gateway session has no files of its agent's here: say what goes
    if (list.some((s) => !s.gateway)) ed.append(el("p", "lib-confirm", t("Their files are moved to magpie's trash ({dir}), not erased: Trash puts them back. A session written to in the last minute is left alone, as {agent} may still be running it.", { dir: data.trashDir, agent: a.name })));
    if (list.some((s) => s.gateway)) ed.append(el("p", "lib-confirm sm-gw-confirm", t("A session seen only through the gateway leaves this list, and any conversation magpie recorded of it is moved to magpie's trash ({dir}): Trash puts it back. {agent}'s own files elsewhere are not touched, and Usage keeps what it spent. One with a request in the last minute is left alone.", { dir: data.trashDir, agent: a.name })));
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
        who.append(el("div", "sub", [x.name, x.gateway ? t("Seen through the gateway") : x.cwd ? baseName(x.cwd) : "", t("deleted {when}", { when: ago(x.deleted) }),
          x.gateway ? (x.size ? t("{size} recorded", { size: fmtBytes(x.size) }) : "") : fmtBytes(x.size)].filter(Boolean).join(" · ")));
        who.title = (x.items || []).map((i) => i.from).join("\n");
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
    // the recording's promise holds in the trash too
    if (items.some((x) => x.gateway)) out.push(el("p", "usage-note sm-gw-trash-note", t("Conversations magpie recorded through the gateway still expire after 7 days here, and Delete saved conversations… erases them too.")));
    return out;
  }

  // speedOf is the speed of a row's session: of any of the keys the
  // gateway may know it by (s.keys, sessions.GatewayKeys), the one in
  // flight, else the one heard from last
  function speedOf(s) {
    let best = null;
    for (const k of [s.agent + "|" + s.id, ...(s.keys || [])]) {
      const sp = speeds.get(k);
      if (sp && (!best || sp.live && !best.live || sp.live === best.live && sp.idle < best.idle)) best = sp;
    }
    return best;
  }
  const rateText = (v) => String(Math.round(v));
  // rateParts is "{n} tok/s" in the reader's language, in the parts around n
  const rateParts = () => t("{n} tok/s", { n: "\u0000" }).split("\u0000");
  const NS = "http://www.w3.org/2000/svg";
  function sv(tag, attrs) {
    const n = document.createElementNS(NS, tag);
    for (const k in attrs) n.setAttribute(k, attrs[k]);
    return n;
  }
  // curveShape lays a curve (a value a second, null where nothing of the
  // session's was in flight) on a 300-wide box h high: the runs of seconds
  // with a value, the gaps between two runs, and where the last value is
  function curveShape(curve, h) {
    const vals = curve || [], n = vals.length;
    const max = Math.max(1, ...vals.filter((v) => v != null));
    const x = (i) => n > 1 ? i * 300 / (n - 1) : 300;
    const y = (v) => +(h - 2 - v / max * (h - 8)).toFixed(1);
    const runs = [], gaps = [];
    let run = null, last = -1;
    vals.forEach((v, i) => {
      if (v == null) { if (run) runs.push(run); run = null; return; }
      if (!run && last >= 0 && i - last > 1) gaps.push({ from: x(last), to: x(i), secs: i - last - 1 });
      (run ||= []).push([+x(i).toFixed(1), y(v)]);
      last = i;
    });
    if (run) runs.push(run);
    // a lone second is a short stroke, not nothing
    for (const r of runs) if (r.length === 1) r.push([Math.min(300, r[0][0] + 2.5), r[0][1]]);
    return { runs, gaps, end: last === n - 1 && last >= 0 ? { x: x(last), y: y(vals[last]) } : null };
  }
  // curveSvg draws it: the line, a dashed baseline over each gap, and
  // under the line a light fill in the popover
  function curveSvg(curve, w, h, fill) {
    const shape = curveShape(curve, h);
    const g = sv("svg", { width: w, height: h, viewBox: `0 0 300 ${h}`, preserveAspectRatio: "none", "aria-hidden": "true", class: "sm-curve" });
    const pts = (r) => r.map((p) => p.join(",")).join(" ");
    for (const r of shape.runs) {
      if (fill) g.append(sv("path", { class: "sm-curve-fill", d: `M${r[0][0]},${h} L${pts(r).replaceAll(" ", " L")} L${r[r.length - 1][0]},${h} Z` }));
      g.append(sv("polyline", { class: "sm-curve-line", points: pts(r), "vector-effect": "non-scaling-stroke" }));
    }
    for (const gap of shape.gaps) g.append(sv("line", { class: "sm-curve-gap", x1: gap.from, x2: gap.to, y1: h - 1, y2: h - 1, "vector-effect": "non-scaling-stroke" }));
    return { svg: g, shape };
  }
  // speedCell is a row's speed: its curve and tokens a second now, or the
  // round's average once nothing is in flight; a button that opens the
  // round's numbers. Hidden while the session has no speed.
  function speedCell(s) {
    const b = el("button", "sm-speed");
    b.type = "button";
    b.sess = s;
    b.setAttribute("aria-haspopup", "dialog");
    b.setAttribute("aria-expanded", "false");
    b.onclick = (e) => { e.stopPropagation(); openSpeed(b); };
    fillSpeed(b);
    return b;
  }
  function fillSpeed(b) {
    const sp = speedOf(b.sess);
    const drawn = sp && (sp.live || sp.rated && sp.avg > 0 || sp.curve?.some((v) => v != null));
    b.hidden = !drawn;
    if (!drawn) { b.replaceChildren(); return; }
    const parts = [];
    if (sp.curve?.some((v) => v != null)) parts.push(curveSvg(sp.curve, 88, 24, false).svg);
    const v = el("span", "sm-speed-val" + (sp.live ? " live" : ""));
    if (!sp.rated) v.textContent = "—";
    else if (sp.live) { const [pre, post] = rateParts(); v.append(pre, el("b", "", rateText(sp.now)), el("small", "", post)); }
    else v.textContent = t("Avg {n} tok/s", { n: rateText(sp.avg) });
    parts.push(v);
    b.replaceChildren(...parts);
    b.setAttribute("aria-label", t("Output speed") + ": " + v.textContent);
  }
  function speedPopBody(sp) {
    const head = el("div", "sm-sp-head");
    const [pre, post] = rateParts();
    head.append(pre, el("span", "sm-sp-big", sp.rated ? rateText(sp.live ? sp.now : sp.avg) : "—"), el("span", "sm-sp-unit", post.trim()));
    const chart = el("div", "sm-sp-chart");
    const { svg: g, shape } = curveSvg(sp.curve, 308, 72, true);
    chart.append(g);
    // a gap inside the round is the agent at work between two requests
    for (const gap of shape.gaps) {
      if (gap.secs < 3 || gap.to - gap.from < 40) continue;
      const l = el("span", "sm-sp-gap", t("Tool {n}s", { n: gap.secs }));
      l.style.left = `calc(${gap.from / 3}% + 4px)`;
      chart.append(l);
    }
    if (sp.live && shape.end) {
      const d = el("span", "sm-sp-dot");
      d.style.top = (shape.end.y - 3.5) + "px";
      chart.append(d);
    }
    const stats = el("div", "sm-sp-stats");
    const rate = (n) => sp.rated ? t("{n} tok/s", { n: rateText(n) }) : "—";
    for (const [k, v] of [["Round average", rate(sp.avg)], ["Fastest second", rate(sp.peak)], ["Out so far", sp.rated ? t("{n} tokens", { n: fmtN(sp.output) }) : "—"]]) {
      const c = el("div", "");
      c.append(el("span", "sm-sp-k", t(k)), el("span", "sm-sp-v", v));
      stats.append(c);
    }
    return [head, chart, stats];
  }
  function openSpeed(b) {
    if (speedPop?.anchor === b) return closeProtoMenu();
    closeProtoMenu();
    const box = el("div", "pop sm-speed-pop");
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-label", t("Output speed"));
    const draw = () => {
      const sp = speedOf(b.sess);
      if (!sp || !b.isConnected) return closeProtoMenu();
      box.replaceChildren(...speedPopBody(sp));
    };
    draw();
    document.body.append(box);
    placeMenu(box, b, "right");
    b.classList.add("open");
    b.setAttribute("aria-expanded", "true");
    holdMenu(box, b, () => {}, (e) => { if (scrollCloses(box, e)) closeProtoMenu(); }, () => { speedPop = null; });
    speedPop = { anchor: b, draw };
  }
  const speedsShown = () => view === "sessions" && !document.hidden && !trashOn;
  async function speedTick() {
    if (!speedsShown()) { clearInterval(speedT); speedT = 0; return; }
    if (speedBusy) return;
    speedBusy = true;
    try {
      const list = await api("speeds");
      speeds = new Map((Array.isArray(list) ? list : []).map((sp) => [sp.agent + "|" + sp.session, sp]));
    } catch { return; } finally { speedBusy = false; }
    page?.querySelectorAll(".sm-speed").forEach(fillSpeed);
    speedPop?.draw();
  }
  function speedsOn() {
    if (speedT || !speedsShown()) return;
    speedT = setInterval(speedTick, 1000);
    speedTick();
  }
  document.addEventListener("visibilitychange", speedsOn);

  ctx.slots.add("page", {
    key: "sessions",
    title: "Sessions",
    mount(main) { page = main; },
    open: () => load(),
    relang: () => load(),
    reveal,
  });
});
