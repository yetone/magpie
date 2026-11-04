// Magpie Browser: magpie's gateway hands this extension the browser tool
// calls of the agents it serves (magpie mcp browser), and it carries them
// out in this browser's own tabs, signed in to whatever the user is.

import { GATEWAY } from "./config.js";
import { attach, fault, forget, send, settle, sleep, state, waitLoad, watch } from "./cdp.js";
import { find, take } from "./snapshot.js";
import { press as keyPress, typeSlowly } from "./keys.js";

const VERSION = chrome.runtime.getManifest().version;
const ACT_SNAPSHOT_MAX = 30000;

// ---- the gateway connection --------------------------------------------

let ws = null;
let retry = 1000;
let connecting = false;

async function gatewayURL() {
  try {
    const { gateway } = await chrome.storage.local.get("gateway");
    if (gateway) return gateway;
  } catch {}
  return GATEWAY;
}

async function connect() {
  if (connecting || (ws && ws.readyState <= 1)) return;
  connecting = true;
  const base = await gatewayURL();
  let s;
  try {
    s = new WebSocket(base.replace(/\/$/, "") + "/v1/magpie/browser/extension");
  } catch (e) {
    connecting = false;
    setStatus({ connected: false, gateway: base, error: String(e.message || e) });
    return later();
  }
  ws = s;
  s.onopen = () => {
    connecting = false;
    retry = 1000;
    s.send(JSON.stringify({ type: "hello", version: VERSION, agent: navigator.userAgent }));
    setStatus({ connected: true, gateway: base });
  };
  s.onmessage = (ev) => {
    let m;
    try { m = JSON.parse(ev.data); } catch { return; }
    if (m.type === "ping") return s.send(JSON.stringify({ type: "pong" }));
    if (!m.id || !m.tool) return;
    run(m).then((result) => {
      if (s.readyState === 1) s.send(JSON.stringify({ id: m.id, result }));
    });
  };
  s.onclose = () => {
    connecting = false;
    if (ws === s) ws = null;
    setStatus({ connected: false, gateway: base });
    later();
  };
  s.onerror = () => {};
}

let timer = 0;
function later() {
  clearTimeout(timer);
  timer = setTimeout(connect, retry);
  retry = Math.min(retry * 2, 15000);
}

function setStatus(s) {
  chrome.storage.session.set({ status: { ...s, at: Date.now() } }).catch(() => {});
  chrome.action.setBadgeText({ text: s.connected ? "" : "off" }).catch(() => {});
  chrome.action.setBadgeBackgroundColor({ color: "#8a8a8a" }).catch(() => {});
}

// a message every 20s keeps the service worker alive while connected;
// the alarm brings it back to reconnect when Chrome stopped it
setInterval(() => { if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: "ping" })); }, 20000);
chrome.alarms.create("magpie-reconnect", { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener(() => connect());
chrome.runtime.onStartup.addListener(() => connect());
chrome.runtime.onInstalled.addListener(() => connect());
chrome.storage.onChanged.addListener((c, area) => {
  if (area === "local" && c.gateway) {
    retry = 1000;
    if (ws) ws.close();
    else connect();
  }
});
chrome.runtime.onMessage.addListener((m, _s, reply) => {
  if (m && m.type === "reconnect") {
    retry = 1000;
    if (ws) ws.close();
    connect();
    reply({ ok: true });
  }
});
connect();

// ---- sessions: each agent's tab ------------------------------------------

// Each agent session (gateway.BrowserSessionHeader: one `magpie mcp
// browser`, or one caller of magpie's MCP router) works in a tab of its
// own and opens its tabs in a tab group of its own, so two agents at once
// never act in one tab unless one picks the other's by its id.
const sessions = new Map(); // session → tabId
const groups = new Map(); // session → tab group id
let loaded = false;

async function loadSessions() {
  if (loaded) return;
  loaded = true;
  try {
    const { sessions: s, groups: g } = await chrome.storage.session.get(["sessions", "groups"]);
    for (const [k, v] of Object.entries(s || {})) sessions.set(k, v);
    for (const [k, v] of Object.entries(g || {})) groups.set(k, v);
  } catch {}
}

function saveSessions() {
  chrome.storage.session.set({ sessions: Object.fromEntries(sessions), groups: Object.fromEntries(groups) }).catch(() => {});
}

// heldByOther is whether another session works in tabId
function heldByOther(session, tabId) {
  for (const [k, v] of sessions) if (k !== session && v === tabId) return true;
  return false;
}

chrome.tabs.onRemoved.addListener((tabId) => {
  forget(tabId);
  for (const [k, v] of sessions) if (v === tabId) sessions.delete(k);
  saveSessions();
});

function tabArg(v) {
  if (v === undefined || v === null || v === "") return null;
  const m = /^t?(\d+)$/i.exec(String(v).trim());
  if (!m) throw fault("BAD_ARGS", `"${v}" isn't a tab: tabs look like t123, from browser_tabs`);
  return Number(m[1]);
}

// the tab a call works in: the one it names, else the session's, else a
// new one of the session's own. The user's tabs are theirs: an agent never
// starts in the one they are looking at (it navigated it away under them),
// and works in one of theirs only when it names it.
async function tabFor(session, args) {
  await loadSessions();
  const named = tabArg(args.tab);
  if (named !== null) {
    await chrome.tabs.get(named).catch(() => { throw fault("BAD_ARGS", `there is no tab t${named} now: call browser_tabs for the open ones`); });
    sessions.set(session, named);
    saveSessions();
    return named;
  }
  const had = sessions.get(session);
  if (had !== undefined) {
    const ok = await chrome.tabs.get(had).then(() => true, () => false);
    if (ok) return had;
  }
  return (await open(session, "about:blank", false)).id;
}

// open is a new tab of the session's, in its tab group, behind the tab the
// user is looking at unless front: in the window they used last, or a
// window of its own, not brought forward, when none is open.
async function open(session, url, front) {
  let w = await chrome.windows.getLastFocused({ windowTypes: ["normal"] }).catch(() => null);
  let tab;
  if (w) tab = await chrome.tabs.create({ url, active: !!front, windowId: w.id });
  else {
    w = await chrome.windows.create({ url, focused: !!front });
    tab = w.tabs[0];
  }
  await group(session, tab.id, tab.windowId);
  sessions.set(session, tab.id);
  saveSessions();
  return tab;
}

// group puts a tab the session opened in the session's tab group in that
// window: "Magpie" for the first session, "Magpie 2", "Magpie 3" for the
// ones working beside it. A group no live session has (one an earlier run
// left) is taken over by its title.
async function group(session, tabId, windowId) {
  try {
    const had = groups.get(session);
    if (had !== undefined) {
      const g = await chrome.tabGroups.get(had).catch(() => null);
      if (g && g.windowId === windowId) return await chrome.tabs.group({ tabIds: [tabId], groupId: g.id });
    }
    const others = new Set([...groups].filter(([k]) => k !== session).map(([, v]) => v));
    const inWindow = await chrome.tabGroups.query({ windowId });
    for (let n = 1; ; n++) {
      const title = n === 1 ? "Magpie" : `Magpie ${n}`;
      const g = inWindow.find((x) => x.title === title);
      if (g && others.has(g.id)) continue;
      let id;
      if (g) id = await chrome.tabs.group({ tabIds: [tabId], groupId: g.id });
      else {
        id = await chrome.tabs.group({ tabIds: [tabId], createProperties: { windowId } });
        await chrome.tabGroups.update(id, { title, color: "grey" });
      }
      groups.set(session, id);
      saveSessions();
      return;
    }
  } catch {}
}

// ---- running a call ------------------------------------------------------

const text = (s) => ({ type: "text", text: s });
const fail = (s) => ({ content: [text(s)], isError: true });

// what each kind of failure means for trying again, said after it
const AFTER = {
  STALE_REF: "Take a snapshot and use a ref from it.",
  AMBIGUOUS: "Take a snapshot and pick one.",
  UNACTIONABLE: "The same call fails the same way until the page changes.",
  BAD_ARGS: "Fix the arguments; the call did nothing.",
  WAIT_TIMEOUT: "The page may still be working: wait again, or take a snapshot to see where it is.",
  SITE_REFUSED: "The site turned the browser away. Don't try again at once: tell the user, who may need to sign in or wait.",
  LOAD_FAILED: "The address didn't load: check it, or the user's connection.",
  PAGE_BLOCKED: "Nothing works in this tab until that changes: use another tab.",
  SCREENSHOT_FAILED: "Go on without the picture: take a snapshot to read the page.",
  OUTCOME_UNKNOWN: "It may or may not have happened: take a snapshot to see before doing it again, since a form or a purchase could go twice.",
};
const GONE = /Execution context was destroyed|Cannot find context|Inspected target navigated or closed|Target closed|No node with given id|Could not find node|Node with given id does not belong/i;

// failure answers with the kind of failure first (STALE_REF: …) and what
// it means for trying again
function failure(e) {
  const m = String((e && e.message) || e);
  const code = (e && e.code) || (GONE.test(m) ? "STALE_REF" : "");
  if (!code) return fail(m);
  const after = AFTER[code] || "";
  return fail(`${code}: ${m}${after && !m.includes(after) ? "\n" + after : ""}`);
}

async function run(m) {
  try {
    const tool = TOOLS[m.tool];
    if (!tool) return fail(`magpie's browser extension has no tool ${m.tool}: update it (magpie writes it again on start; reload it in chrome://extensions)`);
    return await tool(m.session || "default", m.args || {});
  } catch (e) {
    return failure(e);
  }
}

async function ready(session, args) {
  const tabId = await tabFor(session, args);
  const t = state(tabId);
  if (t.stoppedByUser) {
    t.stoppedByUser = false;
    throw fault("USER_STOPPED", "the user stopped Magpie Browser in this tab (Cancel on Chrome's bar): ask them before going on. The next call attaches to it again.");
  }
  t.by = session; // whose call this is: act moves that session on to a tab it opens
  await attach(tabId);
  await send(tabId, "Emulation.setFocusEmulationEnabled", { enabled: true }).catch(() => {});
  return t;
}

async function head(t) {
  const tab = await chrome.tabs.get(t.tabId).catch(() => null);
  return tab ? `Tab t${t.tabId}: ${tab.title || "(untitled)"} — ${tab.url}` : `Tab t${t.tabId}`;
}

async function page(t, max = ACT_SNAPSHOT_MAX, opts = {}) {
  const s = await take(t, { maxChars: max, ...opts });
  return `${await head(t)}\n${s.count} refs\n\n${s.text || "(the page shows nothing)"}`;
}

function notices(t) {
  const out = [];
  if (t.answered) {
    out.push(`A ${t.answered.type} dialog ("${t.answered.message}") was ${t.answered.accept ? "accepted" : "dismissed"} as asked.`);
    t.answered = null;
  }
  if (t.dialog) out.push(`A ${t.dialog.type} dialog is open: "${t.dialog.message}". Answer it with browser_dialog before anything else.`);
  return out;
}

const dialogOpen = (t) => fail("DIALOG_OPEN: " + notices(t).join("\n"));

// roles a click is for: one that changes nothing on the page did nothing
// a reader can see (a checkbox's tick is no change to the document)
const PRESSES = new Set(["button", "link", "clickable", "menuitem", "tab", "treeitem", "PopUpButton", "DisclosureTriangle"]);

// look is what a click can be seen to change: the address, the page's
// scroll, its requests and its document (the element's frame and the main
// one); null where a part can't be read
async function look(t, el) {
  const tab = await chrome.tabs.get(t.tabId).catch(() => null);
  const scroll = await send(t.tabId, "Runtime.evaluate", { expression: "scrollX + ',' + scrollY", returnByValue: true }).then((r) => r.result.value, () => null);
  const doc = [await watch(t)];
  if (el && (el.sid || el.frame)) doc.push(await watch(t, el.frame, el.sid));
  return { url: tab && tab.url, scroll, reqs: t.reqs, doc: doc.map((d) => (d ? d[0] : null)) };
}

// act runs an action, waits for the page to settle and answers with what
// happened and the page after it. A dialog the action opens stops the
// page; it is reported rather than waited on.
async function act(t, what, fn, el = null, press = false) {
  if (t.dialog) return dialogOpen(t);
  const all = await chrome.tabs.query({});
  const before = new Set(all.map((x) => x.id));
  // the tab the user has in front in this tab's window: a tab a link opens
  // from a tab behind it is shown, with that one named as its opener
  const mine = all.find((x) => x.id === t.tabId);
  const shown = mine && all.find((x) => x.active && x.windowId === mine.windowId);
  const behind = shown && shown.id !== t.tabId ? shown : null;
  const was = press ? await look(t, el) : null;
  t.sentInput = false;
  let opened = null;
  // a tab the link opens comes up in front of the user's: their tab is
  // given back at once, not after the new page has loaded
  const onCreated = (tab) => {
    if (before.has(tab.id) || !(tab.openerTabId === t.tabId || (behind && tab.openerTabId === behind.id) || !opened)) return;
    opened = tab;
    if (behind && (tab.openerTabId === t.tabId || tab.openerTabId === behind.id)) chrome.tabs.update(behind.id, { active: true }).catch(() => {});
  };
  chrome.tabs.onCreated.addListener(onCreated);
  let said;
  try {
    const p = fn();
    p.catch(() => {});
    said = await Promise.race([
      p,
      (async () => { while (!t.dialog) await sleep(100); return ""; })(),
    ]);
  } catch (e) {
    // the page went away under an action already under way: what it did
    // isn't known
    if (t.sentInput && !e.code) throw fault("OUTCOME_UNKNOWN", `${what.replace(/\.$/, "")} was under way when the page changed (${String(e.message || e).slice(0, 200)})`);
    throw e;
  } finally {
    setTimeout(() => chrome.tabs.onCreated.removeListener(onCreated), 1500);
  }
  if (!t.dialog) await settle(t, 2500, 400, el ? [el] : []);
  const out = [];
  if (said) out.push(said);
  else out.push(what);
  out.push(...notices(t));
  if (t.dialog) return { content: [text(out.join("\n"))] };
  if (opened && (opened.openerTabId === t.tabId || (behind && opened.openerTabId === behind.id))) {
    // a link that opens a new tab: the agent goes on in it, in its group,
    // and the user keeps the tab they had in front
    if (behind) {
      await chrome.tabs.update(behind.id, { active: true }).catch(() => {});
      if (t.by) await group(t.by, opened.id, opened.windowId).catch(() => {});
    }
    if (t.by) { sessions.set(t.by, opened.id); saveSessions(); }
    const nt = state(opened.id);
    await attach(opened.id);
    await waitLoad(nt, 15000);
    await settle(nt);
    out.push(`It opened a new tab, t${opened.id}; calls now go to it (browser_tabs select to go back).`);
    out.push("", await page(nt));
    return { content: [text(out.join("\n"))] };
  }
  if (was && !opened && !t.answered) {
    const now = await look(t, el);
    const same = now.url === was.url && now.scroll === was.scroll && now.reqs === was.reqs &&
      now.doc.every((d, i) => d !== null && was.doc[i] !== null && d === was.doc[i]);
    if (same) out.push(`NO_EFFECT: nothing on the page changed after it: no new content, no request, no new address. It may not have worked, or it acts out of sight (a copy to the clipboard). Check with a screenshot before trying again, and never press a send, buy or post button a second time to see.`);
  }
  out.push("", await page(t));
  return { content: [text(out.join("\n"))] };
}

// point is where to click an element: the middle of its first box that
// isn't covered by something else, scrolled into view
async function point(t, el) {
  if (el.sid) await reveal(t, el.sid);
  await send(t.tabId, "DOM.scrollIntoViewIfNeeded", { backendNodeId: el.backend }, el.sid).catch(() => {});
  const off = await offset(t, el.sid);
  let quads;
  try {
    quads = (await send(t.tabId, "DOM.getContentQuads", { backendNodeId: el.backend }, el.sid)).quads;
  } catch {
    quads = [];
  }
  if (!quads.length) throw fault("UNACTIONABLE", `${el.ref} (${el.role} "${el.name}") has no box on the page (hidden or collapsed): open what holds it first, or take a snapshot`);
  const q = quads[0];
  const xs = [q[0], q[2], q[4], q[6]], ys = [q[1], q[3], q[5], q[7]];
  const x0 = Math.min(...xs), x1 = Math.max(...xs), y0 = Math.min(...ys), y1 = Math.max(...ys);
  const tries = [[0.5, 0.5], [0.3, 0.5], [0.7, 0.5], [0.5, 0.3], [0.5, 0.7], [0.2, 0.2], [0.8, 0.8]];
  let cover = "";
  for (const [fx, fy] of tries) {
    const x = x0 + (x1 - x0) * fx, y = y0 + (y1 - y0) * fy;
    const r = await send(t.tabId, "Runtime.callFunctionOn", {
      objectId: el.objectId,
      functionDeclaration: `function(x, y) {
        if (this.ownerDocument.defaultView !== window.top) return "";
        const e = document.elementFromPoint(x, y);
        if (!e) return "outside";
        if (e === this || this.contains(e) || (e.shadowRoot && e.contains(this))) return "";
        let r = e; while (r && r.shadowRoot && r !== this) { const d = r.shadowRoot.elementFromPoint(x, y); if (!d || d === r) break; r = d; if (r === this || this.contains(r)) return ""; }
        if (this.contains && e.contains(this) && getComputedStyle(e).pointerEvents !== "none" && e.tagName === "LABEL") return "";
        const s = (e.innerText || e.getAttribute("aria-label") || "").trim().replace(/\\s+/g, " ").slice(0, 60);
        return "<" + e.tagName.toLowerCase() + (e.id ? "#" + e.id : "") + (e.className && typeof e.className === "string" ? "." + e.className.trim().split(/\\s+/).slice(0, 2).join(".") : "") + ">" + (s ? " \\"" + s + "\\"" : "");
      }`,
      arguments: [{ value: x }, { value: y }],
      returnByValue: true,
    }, el.sid).catch(() => ({ result: { value: "" } }));
    const v = r.result.value;
    // the mouse goes to the tab, in the page's own coordinates
    if (!v) return { x: x + off.x, y: y + off.y };
    if (v !== "outside") cover = v;
  }
  if (cover) throw fault("UNACTIONABLE", `${el.ref} (${el.role} "${el.name}") is covered by ${cover}: close or move what covers it (a dialog, a cookie banner, a menu) and try again`);
  throw fault("UNACTIONABLE", `${el.ref} (${el.role} "${el.name}") is outside the window: scroll to it (browser_scroll) and try again`);
}

// reveal scrolls the frames from other sites that hold an element (its
// session sid) into view, the innermost first
async function reveal(t, sid) {
  for (let n = 0; sid && n < 8; n++) {
    const f = t.frameOf.get(sid);
    if (!f) return;
    try {
      const { backendNodeId } = await send(t.tabId, "DOM.getFrameOwner", { frameId: f.frame }, f.parent);
      await send(t.tabId, "DOM.scrollIntoViewIfNeeded", { backendNodeId }, f.parent);
    } catch {}
    sid = f.parent;
  }
}

// offset is where the document of a frame from another site (session sid)
// starts in the tab's: that frame's session measures from its own corner
async function offset(t, sid) {
  let x = 0, y = 0;
  for (let n = 0; sid && n < 8; n++) {
    const f = t.frameOf.get(sid);
    if (!f) throw fault("STALE_REF", "the frame that element was in has gone");
    const { backendNodeId } = await send(t.tabId, "DOM.getFrameOwner", { frameId: f.frame }, f.parent);
    const { model } = await send(t.tabId, "DOM.getBoxModel", { backendNodeId }, f.parent);
    x += model.content[0];
    y += model.content[1];
    sid = f.parent;
  }
  return { x, y };
}

const BUTTONS = { left: "left", right: "right", middle: "middle" };
const MODS = { Alt: 1, Control: 2, Ctrl: 2, Meta: 4, Command: 4, Cmd: 4, Shift: 8 };

async function mouse(t, x, y, opts = {}) {
  const button = BUTTONS[opts.button] || "left";
  const modifiers = (opts.modifiers || []).reduce((a, m) => a | (MODS[m] || 0), 0);
  const count = opts.double ? 2 : 1;
  await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseMoved", x, y, modifiers });
  await sleep(30 + Math.random() * 40);
  for (let i = 1; i <= count; i++) {
    await send(t.tabId, "Input.dispatchMouseEvent", { type: "mousePressed", x, y, button, clickCount: i, modifiers, buttons: 1 });
    await sleep(40 + Math.random() * 50);
    await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button, clickCount: i, modifiers });
  }
}

// opensATab is whether el is, or is in, a link that opens in a new tab
async function opensATab(t, el) {
  return await call(t, el, `function () {
    const a = this.closest && this.closest("a[href], area[href]");
    const to = a && (a.getAttribute("target") || (document.querySelector("base[target]") || {}).target || "").toLowerCase();
    return !!to && !["_self", "_parent", "_top"].includes(to) && !/^(javascript|mailto|tel):/i.test(a.getAttribute("href"));
  }`).catch(() => false);
}

async function call(t, el, fn, args = []) {
  const r = await send(t.tabId, "Runtime.callFunctionOn", {
    objectId: el.objectId, functionDeclaration: fn, arguments: args.map((value) => ({ value })),
    returnByValue: true, awaitPromise: true,
  }, el.sid);
  if (r.exceptionDetails) throw new Error((r.exceptionDetails.exception && r.exceptionDetails.exception.description) || r.exceptionDetails.text);
  return r.result.value;
}

function need(args, ...names) {
  for (const n of names) {
    if (args[n] === undefined || args[n] === null || args[n] === "") throw fault("BAD_ARGS", `${n} is needed`);
  }
}

function withScheme(u) {
  u = String(u).trim();
  if (/^[a-z][a-z0-9+.-]*:/i.test(u)) return u;
  if (/^(localhost|127\.|\[::1\])/i.test(u)) return "http://" + u;
  return "https://" + u;
}

const TOOLS = {
  async browser_tabs(session, args) {
    await loadSessions();
    const action = args.action || "list";
    if (action === "new") {
      // behind the user's tab; front shows it, when the user asked to see it
      const tab = await open(session, args.url ? withScheme(args.url) : "about:blank", args.front === true);
      const t = state(tab.id);
      await attach(tab.id);
      if (args.url) { await waitLoad(t); await settle(t); }
      return { content: [text(`Opened t${tab.id}; calls now go to it.\n\n` + (args.url ? await page(t) : await head(t)))] };
    }
    if (action === "select") {
      need(args, "tab");
      const t = await ready(session, { tab: args.tab });
      return { content: [text(`Calls now go to t${t.tabId}.\n\n` + await page(t))] };
    }
    if (action === "close") {
      const id = args.tab ? tabArg(args.tab) : sessions.get(session);
      if (id === undefined || id === null) throw fault("BAD_ARGS", "tab is needed");
      await chrome.tabs.remove(id);
      return { content: [text(`Closed t${id}.`)] };
    }
    const all = await chrome.tabs.query({});
    const mine = sessions.get(session);
    const titles = new Map((await chrome.tabGroups.query({}).catch(() => [])).map((g) => [g.id, g.title]));
    const lines = all.map((x) => `${x.id === mine ? "*" : heldByOther(session, x.id) ? "~" : x.active ? "+" : " "} t${x.id}${titles.get(x.groupId) ? ` [${titles.get(x.groupId)}]` : ""} ${x.title ? JSON.stringify(x.title.slice(0, 100)) : ""} ${x.url ? x.url.slice(0, 200) : ""}`);
    return { content: [text(`${all.length} tabs (* is the one calls go to, ~ one another agent works in: leave it to them, + a window's shown tab):\n` + lines.join("\n"))] };
  },

  async browser_navigate(session, args) {
    const t = await ready(session, args);
    const go = args.go || (["back", "forward", "reload"].includes(args.url) ? args.url : "");
    let said;
    if (go === "reload") {
      await send(t.tabId, "Page.reload", {});
      said = "Reloaded.";
    } else if (go === "back" || go === "forward") {
      const h = await send(t.tabId, "Page.getNavigationHistory");
      const e = h.entries[h.currentIndex + (go === "back" ? -1 : 1)];
      if (!e) throw fault("BAD_ARGS", `there is no page to go ${go} to`);
      await send(t.tabId, "Page.navigateToHistoryEntry", { entryId: e.id });
      said = `Went ${go} to ${e.url}.`;
    } else {
      need(args, "url");
      const url = withScheme(args.url);
      t.loading = true;
      t.docStatus = 0;
      const r = await send(t.tabId, "Page.navigate", { url });
      if (r.errorText) { t.loading = false; throw fault("LOAD_FAILED", `${url} didn't load: ${r.errorText}`); }
      said = `Opened ${url}.`;
    }
    const loaded = await waitLoad(t);
    await settle(t);
    if (!loaded) said += " (It is still loading after 30s.)";
    const out = [said, ...notices(t), "", await page(t)].join("\n");
    // a sign-in wall, a bot check or a rate limit: the page shown is the
    // site's refusal, not what was asked for
    if (go !== "back" && go !== "forward" && [401, 403, 429, 999].includes(t.docStatus)) {
      return fail(`SITE_REFUSED: the site answered ${t.docStatus} (${t.docStatus === 429 ? "too many requests" : t.docStatus === 401 ? "not signed in" : "refused"}).\n${AFTER.SITE_REFUSED}\n\n${out}`);
    }
    return { content: [text(out)] };
  },

  async browser_snapshot(session, args) {
    const t = await ready(session, args);
    if (t.dialog) return dialogOpen(t);
    const s = await take(t, { interactive: !!args.interactive, ref: args.ref, maxChars: Math.min(Number(args.maxChars) || 60000, 200000) });
    return { content: [text(`${await head(t)}\n${s.count} refs, ${s.interactive} to act on\n\n${s.text || "(the page shows nothing)"}`)] };
  },

  async browser_click(session, args) {
    need(args, "ref");
    const t = await ready(session, args);
    const el = await find(t, args.ref);
    let how = args;
    if (!args.button && !args.double && !(args.modifiers || []).length && await opensATab(t, el) && !(await chrome.tabs.get(t.tabId)).active) {
      // a link that opens a tab, clicked in a tab behind the user's: with
      // Cmd (Ctrl) held, as a person opens one in the background, so the
      // new tab never comes up in front of the user's for a moment
      how = { ...args, modifiers: [/Mac/.test(navigator.userAgent) ? "Meta" : "Control"] };
    }
    return act(t, `Clicked ${el.ref} (${el.role} "${el.name}").`, async () => {
      const p = await point(t, el);
      await mouse(t, p.x, p.y, how);
    }, el, PRESSES.has(el.role) && !args.button && !args.double);
  },

  async browser_hover(session, args) {
    need(args, "ref");
    const t = await ready(session, args);
    const el = await find(t, args.ref);
    return act(t, `Hovering over ${el.ref}.`, async () => {
      const p = await point(t, el);
      await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseMoved", x: p.x, y: p.y });
    }, el);
  },

  async browser_type(session, args) {
    need(args, "ref");
    if (typeof args.text !== "string") throw fault("BAD_ARGS", "text is needed");
    const t = await ready(session, args);
    const el = await find(t, args.ref);
    return act(t, `Typed into ${el.ref}${args.submit ? " and pressed Enter" : ""}.`, async () => {
      const p = await point(t, el).catch(() => null);
      if (p) await mouse(t, p.x, p.y);
      const ok = await call(t, el, `function(clear) {
        const e = this;
        const edit = (x) => x && (x.isContentEditable || /^(INPUT|TEXTAREA)$/.test(x.tagName));
        let to = edit(e) ? e : (e.querySelector && e.querySelector('input,textarea,[contenteditable=""],[contenteditable="true"]')) || e;
        if (document.activeElement !== to && !(to.contains && to.contains(document.activeElement))) to.focus();
        const a = document.activeElement;
        if (!edit(a) && !(a && a.isContentEditable)) return false;
        if (/^(INPUT|TEXTAREA)$/.test(a.tagName)) {
          if (clear) a.select(); else { try { const n = a.value.length; a.setSelectionRange(n, n); } catch {} }
        } else {
          const s = getSelection(), r = document.createRange();
          r.selectNodeContents(a);
          if (!clear) r.collapse(false);
          s.removeAllRanges(); s.addRange(r);
        }
        return true;
      }`, [args.clear !== false]);
      if (!ok) throw fault("UNACTIONABLE", `${el.ref} (${el.role} "${el.name}") doesn't take text: click the field itself`);
      if (args.clear !== false) await keyPress(t, "Delete");
      if (args.slowly) await typeSlowly(t, args.text);
      else if (args.text) await send(t.tabId, "Input.insertText", { text: args.text });
      if (args.submit) await keyPress(t, "Enter");
    }, el);
  },

  async browser_press(session, args) {
    need(args, "key");
    const t = await ready(session, args);
    let el = null;
    if (args.ref) el = await find(t, args.ref);
    return act(t, `Pressed ${args.key}.`, async () => {
      if (el) await call(t, el, "function(){ this.focus() }");
      await keyPress(t, args.key);
    }, el);
  },

  async browser_select(session, args) {
    need(args, "ref");
    const values = Array.isArray(args.values) ? args.values.map(String) : args.value !== undefined ? [String(args.value)] : [];
    if (!values.length) throw fault("BAD_ARGS", "values is needed");
    const t = await ready(session, args);
    const el = await find(t, args.ref);
    return act(t, `Chose ${values.map((v) => JSON.stringify(v)).join(", ")} in ${el.ref}.`, async () => {
      const why = await call(t, el, `function(values) {
        const s = this.tagName === "SELECT" ? this : this.querySelector && this.querySelector("select");
        if (!s) return "is not a native list: click it, then click the option in the snapshot";
        const opts = [...s.options], picked = [];
        for (const v of values) {
          const l = v.trim().toLowerCase();
          const o = opts.find((o) => o.value === v) || opts.find((o) => o.label.trim().toLowerCase() === l) || opts.find((o) => o.label.toLowerCase().includes(l));
          if (!o) return "has no option " + JSON.stringify(v) + "; it has: " + opts.map((o) => o.label.trim()).slice(0, 60).join(", ");
          picked.push(o);
        }
        if (!s.multiple && picked.length > 1) return "takes one value";
        for (const o of opts) o.selected = picked.includes(o);
        s.dispatchEvent(new Event("input", { bubbles: true }));
        s.dispatchEvent(new Event("change", { bubbles: true }));
        return "";
      }`, [values]);
      if (why) throw fault(/^takes one/.test(why) ? "BAD_ARGS" : "UNACTIONABLE", `${el.ref} (${el.role} "${el.name}") ${why}`);
    }, el);
  },

  async browser_scroll(session, args) {
    const t = await ready(session, args);
    const m = await send(t.tabId, "Page.getLayoutMetrics");
    const vw = m.cssVisualViewport || m.layoutViewport;
    let x = vw.clientWidth / 2, y = vw.clientHeight / 2;
    if (args.ref) {
      const el = await find(t, args.ref);
      if (!args.direction) {
        return act(t, `Scrolled ${el.ref} into view.`, async () => {
          if (el.sid) await reveal(t, el.sid);
          await send(t.tabId, "DOM.scrollIntoViewIfNeeded", { backendNodeId: el.backend }, el.sid);
        }, el);
      }
      const off = await offset(t, el.sid);
      const q = (await send(t.tabId, "DOM.getContentQuads", { backendNodeId: el.backend }, el.sid).catch(() => ({ quads: [] }))).quads[0];
      if (q) { x = (q[0] + q[4]) / 2 + off.x; y = (q[1] + q[5]) / 2 + off.y; }
    }
    const dir = args.direction || "down";
    const amount = Number(args.amount) || Math.round((dir === "left" || dir === "right" ? vw.clientWidth : vw.clientHeight) * 0.8);
    const dx = dir === "left" ? -amount : dir === "right" ? amount : 0;
    const dy = dir === "up" ? -amount : dir === "down" ? amount : 0;
    return act(t, `Scrolled ${dir} ${amount}px.`, async () => {
      await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: dx, deltaY: dy });
      await sleep(250);
      const r = await send(t.tabId, "Runtime.evaluate", { expression: "[scrollX, scrollY, document.documentElement.scrollHeight, innerHeight]", returnByValue: true });
      const [sx, sy, h, ih] = r.result.value;
      return `Scrolled ${dir} ${amount}px; the page is at ${Math.round(sy)} of ${Math.max(0, h - ih)}px down${sx ? `, ${Math.round(sx)}px across` : ""}.`;
    });
  },

  async browser_drag(session, args) {
    need(args, "from", "to");
    const t = await ready(session, args);
    const a = await find(t, args.from);
    const b = await find(t, args.to);
    return act(t, `Dragged ${a.ref} onto ${b.ref}.`, async () => {
      const p = await point(t, a);
      const q = await point(t, b);
      await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseMoved", x: p.x, y: p.y });
      await send(t.tabId, "Input.dispatchMouseEvent", { type: "mousePressed", x: p.x, y: p.y, button: "left", clickCount: 1, buttons: 1 });
      try {
        for (let i = 1; i <= 12; i++) {
          await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseMoved", x: p.x + ((q.x - p.x) * i) / 12, y: p.y + ((q.y - p.y) * i) / 12, button: "left", buttons: 1 });
          await sleep(25);
        }
      } finally {
        await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseReleased", x: q.x, y: q.y, button: "left", clickCount: 1 });
      }
    }, a);
  },

  async browser_upload(session, args) {
    need(args, "ref");
    const files = Array.isArray(args.paths) ? args.paths.map(String) : [];
    if (!files.length) throw fault("BAD_ARGS", "paths is needed: the files' full paths on this computer");
    const t = await ready(session, args);
    const el = await find(t, args.ref);
    return act(t, `Chose ${files.length} file${files.length > 1 ? "s" : ""} in ${el.ref}.`, async () => {
      const isInput = await call(t, el, 'function(){ return this.tagName === "INPUT" && this.type === "file" }');
      const set = (backendNodeId, sid) => send(t.tabId, "DOM.setFileInputFiles", { files, backendNodeId }, sid).catch((e) => {
        const m = String(e.message || e);
        if (/not allowed|permission|denied/i.test(m)) throw fault("UNACTIONABLE", "Chrome didn't let Magpie Browser choose files: turn on \"Allow access to file URLs\" for Magpie Browser in chrome://extensions");
        throw e;
      });
      if (isInput) return void (await set(el.backend, el.sid));
      // a button that opens the file picker: the picker is caught and
      // answered with the files
      await send(t.tabId, "Page.setInterceptFileChooserDialog", { enabled: true });
      try {
        const opened = new Promise((resolve, reject) => {
          t.chooser = resolve;
          setTimeout(() => reject(fault("UNACTIONABLE", `clicking ${el.ref} opened no file picker: use the file field's ref`)), 5000);
        });
        const p = await point(t, el);
        await mouse(t, p.x, p.y);
        const c = await opened;
        await set(c.backendNodeId);
      } finally {
        t.chooser = null;
        await send(t.tabId, "Page.setInterceptFileChooserDialog", { enabled: false }).catch(() => {});
      }
    }, el);
  },

  async browser_wait(session, args) {
    const t = await ready(session, args);
    const bound = Math.min(Number(args.timeout) || 10, 60) * 1000;
    if (args.seconds) {
      await sleep(Math.min(Number(args.seconds), 60) * 1000);
      return { content: [text([`Waited ${args.seconds}s.`, ...notices(t), "", await page(t)].join("\n"))] };
    }
    if (!args.text && !args.textGone && !args.url) throw fault("BAD_ARGS", "text, textGone, url or seconds is needed");
    const end = Date.now() + bound;
    for (;;) {
      if (t.dialog) return dialogOpen(t);
      const r = await send(t.tabId, "Runtime.evaluate", { expression: "[location.href, document.body ? document.body.innerText : '']", returnByValue: true }).catch(() => null);
      if (r && r.result && r.result.value) {
        const [href, body] = r.result.value;
        const ok = (!args.text || body.includes(args.text)) && (!args.textGone || !body.includes(args.textGone)) && (!args.url || href.includes(args.url));
        if (ok) break;
      }
      if (Date.now() > end) throw fault("WAIT_TIMEOUT", `waited ${bound / 1000}s and the page still ${args.text ? `doesn't show "${args.text}"` : args.textGone ? `shows "${args.textGone}"` : `isn't at ${args.url}`}`);
      await sleep(250);
    }
    await settle(t);
    return { content: [text(["Done waiting.", "", await page(t)].join("\n"))] };
  },

  async browser_screenshot(session, args) {
    const t = await ready(session, args);
    if (t.dialog) return dialogOpen(t);
    const params = { format: args.png ? "png" : "jpeg", ...(args.png ? {} : { quality: 80 }) };
    if (args.ref) {
      const el = await find(t, args.ref);
      if (el.sid) await reveal(t, el.sid);
      await send(t.tabId, "DOM.scrollIntoViewIfNeeded", { backendNodeId: el.backend }, el.sid).catch(() => {});
      const off = await offset(t, el.sid);
      const { model } = await send(t.tabId, "DOM.getBoxModel", { backendNodeId: el.backend }, el.sid);
      const b = model.border;
      const xs = [b[0], b[2], b[4], b[6]].map((v) => v + off.x), ys = [b[1], b[3], b[5], b[7]].map((v) => v + off.y);
      const lm = await send(t.tabId, "Page.getLayoutMetrics");
      const vv = lm.cssVisualViewport;
      params.clip = { x: Math.min(...xs) + vv.pageX, y: Math.min(...ys) + vv.pageY, width: Math.max(1, Math.max(...xs) - Math.min(...xs)), height: Math.max(1, Math.max(...ys) - Math.min(...ys)), scale: 1 };
      params.captureBeyondViewport = true;
    } else if (args.fullPage) {
      const lm = await send(t.tabId, "Page.getLayoutMetrics");
      const c = lm.cssContentSize || lm.contentSize;
      params.clip = { x: 0, y: 0, width: c.width, height: Math.min(c.height, 12000), scale: 1 };
      params.captureBeyondViewport = true;
    }
    const shot = () => send(t.tabId, "Page.captureScreenshot", params);
    // Chrome draws a tab behind the user's for the picture (the first one
    // can take a few seconds); the tab is never brought forward for it,
    // which took the user's tab from under them for a moment each time
    const r = await Promise.race([shot(), sleep(20000).then(() => null)]);
    if (!r) throw fault("SCREENSHOT_FAILED", "the tab, behind the one the user is looking at, wasn't drawn in 20s; it is left behind rather than brought in front of the user");
    return { content: [{ type: "image", data: r.data, mimeType: args.png ? "image/png" : "image/jpeg" }, text(await head(t))] };
  },

  async browser_evaluate(session, args) {
    need(args, "script");
    const t = await ready(session, args);
    if (t.dialog) return dialogOpen(t);
    const src = String(args.script).trim();
    // async may touch its parameters (async()=>…), and a parameter list
    // holds no parentheses, so (async () => …)() stays an expression.
    const fn = /^(async\b\s*)?(function\b|\([^()]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)/.test(src);
    let r;
    if (args.ref) {
      const el = await find(t, args.ref);
      r = await send(t.tabId, "Runtime.callFunctionOn", {
        objectId: el.objectId, returnByValue: true, awaitPromise: true,
        functionDeclaration: fn ? `function() { return (${src})(this); }` : `function() { return (${src}); }`,
      }, el.sid);
    } else {
      r = await send(t.tabId, "Runtime.evaluate", { expression: fn ? `(${src})()` : src, returnByValue: true, awaitPromise: true, userGesture: true });
    }
    if (r.exceptionDetails) {
      const d = r.exceptionDetails;
      throw fault("SCRIPT_ERROR", "the script threw: " + ((d.exception && d.exception.description) || d.text));
    }
    let out = r.result.value === undefined ? (r.result.description || r.result.type) : JSON.stringify(r.result.value, null, 1);
    if (out && out.length > 20000) out = out.slice(0, 20000) + "\n… (cut at 20000 characters)";
    return { content: [text([out === undefined ? "undefined" : out, ...notices(t)].join("\n"))] };
  },

  async browser_text(session, args) {
    const t = await ready(session, args);
    if (t.dialog) return dialogOpen(t);
    const max = Math.min(Number(args.maxChars) || 40000, 200000);
    const r = await send(t.tabId, "Runtime.evaluate", { expression: "document.body ? document.body.innerText : ''", returnByValue: true });
    let s = String(r.result.value || "").replace(/\n{3,}/g, "\n\n").trim();
    if (s.length > max) s = s.slice(0, max) + `\n… (cut at ${max} characters of ${s.length})`;
    return { content: [text(`${await head(t)}\n\n${s || "(the page has no text)"}`)] };
  },

  async browser_console(session, args) {
    const t = await ready(session, args);
    const levels = args.level === "errors" ? ["error", "assert"] : args.level === "network" ? ["network"] : null;
    let logs = t.logs.filter((l) => !levels || levels.includes(l.level));
    const n = Math.min(Number(args.limit) || 50, 300);
    logs = logs.slice(-n);
    if (!logs.length) return { content: [text("Nothing logged since Magpie Browser started watching this tab.")] };
    return { content: [text(logs.map((l) => `[${new Date(l.at).toISOString().slice(11, 19)}] ${l.level}: ${l.text}`).join("\n"))] };
  },

  async browser_dialog(session, args) {
    const tabId = await tabFor(session, args);
    const t = state(tabId);
    await attach(tabId);
    const accept = args.accept !== false;
    if (t.dialog) {
      const d = t.dialog;
      await send(tabId, "Page.handleJavaScriptDialog", { accept, ...(args.promptText !== undefined ? { promptText: String(args.promptText) } : {}) });
      t.dialog = null;
      await settle(t);
      return { content: [text(`${accept ? "Accepted" : "Dismissed"} the ${d.type} dialog ("${d.message}").\n\n` + await page(t))] };
    }
    t.answer = { accept, promptText: args.promptText !== undefined ? String(args.promptText) : undefined };
    return { content: [text(`No dialog is open. The next one in t${tabId} will be ${accept ? "accepted" : "dismissed"}.`)] };
  },
};

