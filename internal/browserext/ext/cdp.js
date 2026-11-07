// The DevTools protocol over chrome.debugger, one attachment per tab. A
// tab is attached while a call works in it and let go 20s after the last
// call ends, as Claude in Chrome does, so a site never sits for minutes
// under a debugger with its focus emulated.
//
// The page never learns it is driven. Runtime is left off (a page can
// tell it is on: Chrome then reads what the page logs, and a logged
// object's getters run), save on a developer's own host or once an agent
// asks for the console. Everything magpie reads or runs in a page runs
// in an isolated world of its own, which the page's scripts can't see or
// patch: nothing of magpie's shows in the page's own JavaScript.

// let go this long after the last call in a tab ends
const IDLE_MS = 20 * 1000;
// a bound on one call (or a typing that goes on after its call)
const BUSY_MS = 5 * 60 * 1000;
const LOG_KEPT = 300;

// tabId → what is known of the page while attached
const tabs = new Map();

export function state(tabId) {
  let t = tabs.get(tabId);
  if (!t) {
    t = {
      tabId, attached: false, attaching: null, idle: 0,
      refs: new Map(), keys: new Map(), next: 1, doc: "",
      logs: [], dialog: null, answer: null, chooser: null,
      inflight: new Map(), lastNet: 0, loading: false, reqs: 0,
      stoppedByUser: false,
      // a frame from another site runs in a process of its own, reached
      // through a session of its own: frame id → session, and session →
      // { frame, parent session }
      children: new Map(), frameOf: new Map(),
      worlds: new Map(), // "session|frame" → magpie's isolated world there
      sentInput: false,
      console: false, // Runtime is on: the page's logs are read
      mouse: null, // where the mouse last was, {x, y}
      lastInput: 0, // when the last action's input ended
      lastWheel: 0,
    };
    tabs.set(tabId, t);
  }
  return t;
}

export function forget(tabId) {
  const t = tabs.get(tabId);
  if (t) clearTimeout(t.idle);
  tabs.delete(tabId);
}

export async function attach(tabId) {
  const t = state(tabId);
  clearTimeout(t.idle);
  t.idle = setTimeout(() => detach(tabId), BUSY_MS);
  t.busy = true;
  if (t.attached) return t;
  if (!t.attaching) {
    t.attaching = (async () => {
      try {
        await chrome.debugger.attach({ tabId }, "1.3");
      } catch (e) {
        const m = String(e && e.message || e);
        if (!/already attached/i.test(m)) throw fault("PAGE_BLOCKED", why(m));
      }
      t.attached = true;
      t.stoppedByUser = false;
      t.children.clear();
      t.frameOf.clear();
      t.worlds.clear();
      if (!t.console) t.console = await devHost(tabId);
      // what Claude in Chrome turns on, and Network, which the page can't
      // see, for knowing when it is quiet. DOM and Accessibility stay off:
      // their calls work without them, and on they keep Blink's whole
      // accessibility tree live while the tab is attached.
      await Promise.all([
        send(tabId, "Page.enable"),
        ...(t.console ? [send(tabId, "Runtime.enable")] : []),
        send(tabId, "Network.enable"),
      ]);
      await send(tabId, "Emulation.setFocusEmulationEnabled", { enabled: true }).catch(() => {});
      await send(tabId, "Page.setInterceptFileChooserDialog", { enabled: false }).catch(() => {});
      // frames from other sites: Chrome 125 and on hands an extension
      // their sessions; before it they stay unreadable, as said in the
      // snapshot
      await autoAttach(tabId);
    })().finally(() => { t.attaching = null; });
  }
  await t.attaching;
  return t;
}

// devHost is whether a tab shows a developer's own page (localhost, a
// .test or .localhost name), whose logs are read from the start: no site
// there watches for a debugger
const DEV = /^(localhost|127\.\d+\.\d+\.\d+|\[::1\]|.+\.localhost|.+\.test)$/i;
async function devHost(tabId) {
  const tab = await chrome.tabs.get(tabId).catch(() => null);
  try { return DEV.test(new URL(tab && (tab.pendingUrl || tab.url)).hostname); } catch { return false; }
}

// watchConsole turns Runtime on in a tab and its frames, so what the page
// logs from now on is read; asked for by browser_console
export async function watchConsole(t) {
  if (t.console) return false;
  t.console = true;
  await send(t.tabId, "Runtime.enable").catch(() => {});
  for (const sid of t.frameOf.keys()) send(t.tabId, "Runtime.enable", {}, sid).catch(() => {});
  return true;
}

function why(m) {
  if (/chrome:\/\/|chrome-extension:\/\/|Cannot access|Cannot attach/i.test(m)) {
    return "Chrome doesn't let an extension control this page (a chrome:// page, the Web Store or another extension's page): open a web page in the tab first";
  }
  if (/Another debugger is already attached/i.test(m)) {
    return "DevTools or another debugger is attached to this tab: close DevTools in it, or use another tab";
  }
  return m;
}

// hold keeps a tab's attachment on while typing that outlived its call
// goes on: it puts off the let-go and never attaches again. A tab that
// was let go, or that the user stopped (Cancel on Chrome's bar), stays
// so: the typing's next key then fails and the typing ends.
export function hold(tabId) {
  const t = tabs.get(tabId);
  if (!t || !t.attached || t.stoppedByUser || t.attaching) return false;
  clearTimeout(t.idle);
  t.idle = setTimeout(() => detach(tabId), BUSY_MS);
  t.busy = true;
  return true;
}

// release lets a tab a call used go IDLE_MS from now, unless typing goes
// on in it. A tab no call used since is left on its clock.
export function release(tabId) {
  const t = tabs.get(tabId);
  if (!t || !t.attached || !t.busy || t.typing) return;
  t.busy = false;
  clearTimeout(t.idle);
  t.idle = setTimeout(() => detach(tabId), IDLE_MS);
}

// releaseAll is release for every attached tab, once no call is running
export function releaseAll() {
  for (const id of tabs.keys()) release(id);
}

export async function detach(tabId) {
  const t = tabs.get(tabId);
  if (!t || !t.attached) return;
  clearTimeout(t.idle);
  // the page's focus is its own again before the debugger goes
  await send(tabId, "Emulation.setFocusEmulationEnabled", { enabled: false }).catch(() => {});
  t.attached = false;
  try { await chrome.debugger.detach({ tabId }); } catch {}
}

// autoAttach asks for the sessions of the frames from other sites that a
// page (or such a frame: session sid) holds, as each one appears
function autoAttach(tabId, sid) {
  return send(tabId, "Target.setAutoAttach", {
    autoAttach: true, waitForDebuggerOnStart: false, flatten: true,
    filter: [{ type: "iframe", exclude: false }, { exclude: true }],
  }, sid).catch(() => {});
}

async function child(t, sid) {
  await Promise.all([
    ...(t.console ? [send(t.tabId, "Runtime.enable", {}, sid)] : []),
    send(t.tabId, "Network.enable", {}, sid),
  ]).catch(() => {});
  await autoAttach(t.tabId, sid);
}

// fault is an error the model is told the kind of, so it knows whether
// trying again can help: STALE_REF, AMBIGUOUS, UNACTIONABLE, BAD_ARGS,
// WAIT_TIMEOUT, SITE_REFUSED, LOAD_FAILED, PAGE_BLOCKED, USER_STOPPED,
// DIALOG_OPEN, OUTCOME_UNKNOWN, SCRIPT_ERROR
export function fault(code, message) {
  const e = new Error(message);
  e.code = code;
  return e;
}

// send sends a command to the tab, or to the session of a frame from
// another site in it (sid)
export function send(tabId, method, params = {}, sid) {
  const to = sid ? { tabId, sessionId: sid } : { tabId };
  if (method.startsWith("Input.")) {
    const t = tabs.get(tabId);
    if (t) t.sentInput = true;
  }
  return chrome.debugger.sendCommand(to, method, params).catch((e) => {
    const m = String(e && e.message || e);
    if (!sid && /Debugger is not attached|No tab with given id|detached/i.test(m)) {
      const t = tabs.get(tabId);
      if (t) t.attached = false;
    }
    throw new Error(m.replace(/^\{"code":-?\d+,"message":"(.*)"\}$/, "$1"));
  });
}

// requests that never end on their own: a page with one open is still
// quiet
const STREAMING = new Set(["EventSource", "WebSocket", "Ping", "CSPViolationReport", "Preflight"]);

chrome.debugger.onEvent.addListener((src, method, p) => {
  const t = tabs.get(src.tabId);
  if (!t) return;
  const sid = src.sessionId;
  // a frame's own session: its requests, logs and frames count; its
  // page events aren't the tab's
  if (sid && method.startsWith("Page.")) return;
  const req = (id) => (sid ? sid + ":" + id : id);
  switch (method) {
    case "Target.attachedToTarget": {
      const info = p.targetInfo || {};
      if (info.type !== "iframe") break;
      t.children.set(info.targetId, p.sessionId);
      t.frameOf.set(p.sessionId, { frame: info.targetId, parent: sid });
      child(t, p.sessionId);
      break;
    }
    case "Target.detachedFromTarget": {
      const f = t.frameOf.get(p.sessionId);
      if (f) {
        if (t.children.get(f.frame) === p.sessionId) t.children.delete(f.frame);
        t.frameOf.delete(p.sessionId);
      }
      for (const k of t.worlds.keys()) if (k.startsWith(p.sessionId + "|")) t.worlds.delete(k);
      break;
    }
    case "Network.requestWillBeSent":
      if (!STREAMING.has(p.type) && !/^data:|^blob:/.test(p.request.url)) t.inflight.set(req(p.requestId), Date.now());
      t.lastNet = Date.now();
      t.reqs++;
      break;
    case "Network.loadingFinished":
    case "Network.loadingFailed":
      t.inflight.delete(req(p.requestId));
      t.lastNet = Date.now();
      if (method === "Network.loadingFailed" && !p.canceled && p.type === "Document") log(t, "network", `failed: ${p.errorText}`);
      break;
    case "Network.responseReceived":
      if (p.response.status >= 400) log(t, "network", `${p.response.status} ${p.type} ${p.response.url.slice(0, 300)}`);
      // what the site answered the page itself with
      if (!sid && p.type === "Document" && (!t.mainFrame || p.frameId === t.mainFrame)) t.docStatus = p.response.status;
      // a streamed answer (server-sent events, a long poll) isn't waited for
      if (/text\/event-stream/i.test(p.response.mimeType || "")) t.inflight.delete(req(p.requestId));
      break;
    case "Page.frameStartedLoading":
      if (!p.frameId || p.frameId === t.mainFrame) t.loading = true;
      break;
    case "Page.loadEventFired":
      t.loading = false;
      break;
    case "Page.frameNavigated":
      if (!p.frame.parentId) {
        t.mainFrame = p.frame.id;
        t.inflight.clear();
        // a developer's own page: its logs are read from the start
        try { if (!t.console && DEV.test(new URL(p.frame.url).hostname)) watchConsole(t); } catch {}
      }
      break;
    case "Runtime.consoleAPICalled": {
      const text = (p.args || []).map((a) => a.value !== undefined ? String(a.value) : (a.description || a.type)).join(" ");
      log(t, p.type === "warning" ? "warn" : p.type, text);
      break;
    }
    case "Runtime.exceptionThrown": {
      const d = p.exceptionDetails || {};
      log(t, "error", (d.exception && d.exception.description) || d.text || "exception");
      break;
    }
    case "Page.javascriptDialogOpening":
      if (t.answer) {
        const a = t.answer;
        t.answer = null;
        send(src.tabId, "Page.handleJavaScriptDialog", { accept: a.accept, promptText: a.promptText }).catch(() => {});
        t.answered = { type: p.type, message: p.message, accept: a.accept };
      } else {
        t.dialog = { type: p.type, message: p.message, defaultPrompt: p.defaultPrompt };
      }
      break;
    case "Page.javascriptDialogClosed":
      t.dialog = null;
      break;
    case "Page.fileChooserOpened":
      if (t.chooser) t.chooser(p);
      break;
  }
});

chrome.debugger.onDetach.addListener((src, reason) => {
  const t = tabs.get(src.tabId);
  if (!t) return;
  t.attached = false;
  t.children.clear();
  t.frameOf.clear();
  t.worlds.clear();
  // the user pressed Cancel on Chrome's bar: they want it stopped
  if (reason === "canceled_by_user") t.stoppedByUser = true;
});

function log(t, level, text) {
  t.logs.push({ at: Date.now(), level, text: String(text).slice(0, 2000) });
  if (t.logs.length > LOG_KEPT) t.logs.splice(0, t.logs.length - LOG_KEPT);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
export { sleep };

// A MutationObserver in an isolated world of a frame: the page can't see
// it, and it counts what the page changes. Each read answers [how many
// changes so far, ms since the last one].
const WATCH = `(() => {
  if (!window.__magpieWatch) {
    const w = window.__magpieWatch = { n: 0, at: 0 };
    new MutationObserver((r) => { w.n += r.length; w.at = Date.now(); })
      .observe(document, { subtree: true, childList: true, attributes: true, characterData: true });
  }
  const w = window.__magpieWatch;
  return [w.n, w.at ? Date.now() - w.at : 1e9];
})()`;

async function mainFrame(t) {
  if (t.mainFrame) return t.mainFrame;
  try { t.mainFrame = (await send(t.tabId, "Page.getFrameTree")).frameTree.frame.id; } catch {}
  return t.mainFrame;
}

const STALE_WORLD = /Cannot find context|context with specified id|Execution context was destroyed|Inspected target navigated/i;

// world is magpie's isolated world in a frame (the tab's main frame
// without one, or the frame of session sid): its execution context id, a
// new one for each document
export async function world(t, frame, sid) {
  if (!frame) frame = sid ? t.frameOf.get(sid)?.frame : await mainFrame(t);
  if (!frame) throw new Error("the page has no frame to read");
  const k = (sid || "") + "|" + frame;
  let ctx = t.worlds.get(k);
  if (!ctx) {
    ctx = (await send(t.tabId, "Page.createIsolatedWorld", { frameId: frame, worldName: "magpie" }, sid)).executionContextId;
    t.worlds.set(k, ctx);
  }
  return ctx;
}

// inWorld runs fn(ctx) in magpie's world of a frame, in a new one when
// the document it had is gone
async function inWorld(t, where, fn) {
  for (let i = 0; ; i++) {
    let ctx;
    try {
      ctx = await world(t, where.frame, where.sid);
      return await fn(ctx);
    } catch (e) {
      if (i > 0 || !STALE_WORLD.test(String(e && e.message || e))) throw e;
      if (where.frame) t.worlds.delete((where.sid || "") + "|" + where.frame);
      else for (const [k, v] of t.worlds) if (v === ctx) t.worlds.delete(k);
      if (!where.frame && !where.sid) t.mainFrame = null; // a new document may be a new frame
    }
  }
}

// read evaluates an expression in magpie's world of the main frame (or
// where: {frame, sid}) and answers its value
export async function read(t, expression, where = {}) {
  return inWorld(t, where, async (contextId) => {
    const r = await send(t.tabId, "Runtime.evaluate", { expression, contextId, returnByValue: true, awaitPromise: true }, where.sid);
    if (r.exceptionDetails) throw new Error((r.exceptionDetails.exception && r.exceptionDetails.exception.description) || r.exceptionDetails.text);
    return r.result.value;
  });
}

// evaluate is read answering the remote object, for the caller to walk
export async function evaluate(t, expression, objectGroup, where = {}) {
  return inWorld(t, where, async (contextId) => (await send(t.tabId, "Runtime.evaluate", { expression, contextId, objectGroup }, where.sid)).result);
}

// resolve is an element (by backendNodeId) as an object in magpie's world
// of the frame its document is
export async function resolve(t, backendNodeId, where = {}, objectGroup = "magpie-act") {
  return inWorld(t, where, async (executionContextId) =>
    (await send(t.tabId, "DOM.resolveNode", { backendNodeId, objectGroup, executionContextId }, where.sid)).object);
}

// watch reads the changes in a frame (the tab's main frame without one),
// or null when it can't be read
export async function watch(t, frame, sid) {
  try {
    const v = await read(t, WATCH, { frame, sid });
    return Array.isArray(v) ? v : null;
  } catch { return null; }
}

// settle waits for the page to go quiet after an action: no load under
// way, no request open and nothing on the page changed for a short spell
// (in the frames given, the main one besides), or until the bound. A
// frame that can't be watched is waited for by its requests alone.
export async function settle(t, bound = 2500, quiet = 400, frames = []) {
  const end = Date.now() + bound;
  await sleep(60);
  const watched = [{}, ...frames.filter((f) => f && (f.frame || f.sid))];
  while (Date.now() < end) {
    const now = Date.now();
    for (const [id, at] of t.inflight) if (now - at > 10000) t.inflight.delete(id); // a long poll
    if (!t.loading && t.inflight.size === 0 && now - t.lastNet >= quiet) {
      const reads = await Promise.all(watched.map((f) => watch(t, f.frame, f.sid)));
      if (reads.every((r) => !r || r[1] >= quiet)) return;
    }
    await sleep(80);
  }
}

// waitLoad waits for the main frame to finish loading after a navigation.
export async function waitLoad(t, bound = 30000) {
  const end = Date.now() + bound;
  await sleep(100);
  while (Date.now() < end) {
    if (!t.loading) {
      try {
        const v = await read(t, "document.readyState");
        if (v === "complete" || v === "interactive") return true;
      } catch {}
    }
    await sleep(150);
  }
  return false;
}
