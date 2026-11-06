// A page as the model reads it: its accessibility tree as indented text,
// each element it can act on given a ref (e3) that stays the element's
// from one snapshot to the next, for as long as the page keeps it.

import { evaluate, fault, read, resolve, send } from "./cdp.js";

const INTERACTIVE = new Set([
  "button", "link", "textbox", "searchbox", "checkbox", "radio", "combobox",
  "listbox", "option", "menuitem", "menuitemcheckbox", "menuitemradio", "tab",
  "switch", "slider", "spinbutton", "treeitem", "textarea", "ComboBox",
  "PopUpButton", "MenuListPopup", "DisclosureTriangle", "columnheader",
]);
// wrappers that say nothing of their own: their children are shown in
// their place
const TRANSPARENT = new Set([
  "generic", "none", "presentation", "GenericContainer", "Section", "LayoutTable",
  "LayoutTableRow", "LayoutTableCell", "Div", "Span", "Pre", "Abbr", "Ruby",
  "LabelText", "group", "Canvas", "superscript", "subscript", "emphasis", "strong",
  "paragraph", "Paragraph", "time", "mark", "code",
]);
const SKIP = new Set(["InlineTextBox", "LineBreak", "ListMarker"]);
const NAMED_MAX = 200;
const TEXT_MAX = 500;
const STATES = ["checked", "selected", "expanded", "pressed", "disabled", "required", "invalid", "focused", "readonly"];

// elements a page made clickable with a listener of its own (a div with a
// pointer cursor) that the accessibility tree calls generic
const CLICKABLE = `(() => {
  const inter = 'a,button,input,select,textarea,summary,label,[role],[contenteditable=""],[contenteditable="true"]';
  const out = [];
  const all = document.querySelectorAll('body *');
  for (let i = 0; i < all.length && out.length < 300; i++) {
    const el = all[i];
    if (el.closest(inter)) continue;
    const cs = getComputedStyle(el);
    if (cs.cursor !== 'pointer' && !el.hasAttribute('onclick')) continue;
    const p = el.parentElement;
    if (p && p !== document.body && getComputedStyle(p).cursor === 'pointer') continue;
    const r = el.getBoundingClientRect();
    if (r.width < 4 || r.height < 4 || cs.visibility === 'hidden') continue;
    out.push(el);
  }
  return out;
})()`;

// run in magpie's isolated world: the page's scripts never see the scan
async function clickables(t) {
  const tabId = t.tabId;
  const ids = new Set();
  try {
    const r = await evaluate(t, CLICKABLE, "magpie-snap");
    if (!r || !r.objectId) return ids;
    const props = await send(tabId, "Runtime.getProperties", { objectId: r.objectId, ownProperties: true });
    await Promise.all(props.result.filter((p) => /^\d+$/.test(p.name) && p.value && p.value.objectId).map(async (p) => {
      try {
        const d = await send(tabId, "DOM.describeNode", { objectId: p.value.objectId });
        ids.add(d.node.backendNodeId);
      } catch {}
    }));
  } catch {} finally {
    send(tabId, "Runtime.releaseObjectGroup", { objectGroup: "magpie-snap" }).catch(() => {});
  }
  return ids;
}

// the page's frames, read beside the main one: a frame of the page's own
// process through the tab, a frame from another site (a process of its
// own) through its session. Keyed by the session the owner element is in
// and its backendNodeId, since each process numbers its nodes itself.
async function frames(t) {
  const out = new Map(); // "session:backend" → { nodes, sid, frame } | null
  const done = new Set();
  let left = 30;
  const read = async (sid, depth) => {
    if (depth > 4) return;
    let tree;
    try { tree = (await send(t.tabId, "Page.getFrameTree", {}, sid)).frameTree; } catch { return; }
    const kids = [];
    (function walk(f) { for (const c of f.childFrames || []) { kids.push(c.frame); walk(c); } })(tree);
    // a frame from another site is left out of its parent's tree: it is
    // known by the session attached for it
    for (const [, v] of t.frameOf) if ((v.parent || undefined) === sid && !kids.some((k) => k.id === v.frame)) kids.push({ id: v.frame });
    await Promise.all(kids.map(async (f) => {
      if (done.has(f.id)) return;
      let owner;
      // a frame whose element is in another process is read from there
      try { owner = (await send(t.tabId, "DOM.getFrameOwner", { frameId: f.id }, sid)).backendNodeId; } catch { return; }
      if (done.has(f.id) || left <= 0) return;
      done.add(f.id);
      left--;
      const key = (sid || "") + ":" + owner;
      const own = t.children.get(f.id);
      try {
        if (own) {
          const { nodes } = await send(t.tabId, "Accessibility.getFullAXTree", {}, own);
          out.set(key, { nodes, sid: own, frame: f.id });
          await read(own, depth + 1);
        } else {
          const { nodes } = await send(t.tabId, "Accessibility.getFullAXTree", { frameId: f.id }, sid);
          out.set(key, { nodes, sid, frame: f.id });
        }
      } catch {
        out.set(key, null);
      }
    }));
  };
  await read(undefined, 0);
  return out;
}

const clip = (s, n) => (s.length > n ? s.slice(0, n - 1) + "…" : s);
const val = (v) => (v && v.value !== undefined && v.value !== null ? String(v.value) : "");
const q = (s) => JSON.stringify(s);

// take reads the page and gives each actionable element its ref.
export async function take(t, opts = {}) {
  const tabId = t.tabId;
  const [{ nodes }, clickable, inner, info] = await Promise.all([
    send(tabId, "Accessibility.getFullAXTree"),
    clickables(t),
    frames(t),
    read(t, "[location.href, document.title]").catch(() => ["", ""]),
  ]);
  const [url, title] = info;
  // a new document starts its refs over; the same one keeps them
  const doc = url.split("#")[0];
  if (doc !== t.doc) {
    t.doc = doc;
    t.keys = new Map();
    t.next = 1;
  }
  const refs = new Map();
  const seen = new Map(); // role|name → how many so far, for the nth
  const lines = [];
  let interactive = 0;

  const index = (list) => {
    const m = new Map();
    for (const n of list) m.set(n.nodeId, n);
    return m;
  };

  // where a ref's element is: the session (a frame from another site)
  // and the frame its document is
  let sid, frame;
  const refFor = (role, name, backend) => {
    const k = role + "|" + name;
    const nth = seen.get(k) || 0;
    seen.set(k, nth + 1);
    const key = k + "|" + nth;
    let ref = t.keys.get(key);
    if (!ref || refs.has(ref)) {
      ref = "e" + t.next++;
      t.keys.set(key, ref);
    }
    refs.set(ref, { backend, role, name, key, ...(sid ? { sid } : {}), ...(frame ? { frame } : {}) });
    return ref;
  };

  // text a node's subtree reads as, for an element that has no name
  const textOf = (byId, n, depth = 0) => {
    if (!n || depth > 12) return "";
    const r = val(n.role);
    if (r === "StaticText") return val(n.name);
    let s = "";
    for (const c of n.childIds || []) {
      const x = textOf(byId, byId.get(c), depth + 1);
      if (x) s += (s ? " " : "") + x;
      if (s.length > NAMED_MAX) break;
    }
    return s;
  };

  const walk = (byId, n, depth, named) => {
    if (!n || depth > 60) return;
    const role = val(n.role);
    const children = () => { for (const c of n.childIds || []) walk(byId, byId.get(c), depth, named); };
    if (SKIP.has(role)) return;
    if (n.ignored) {
      for (const c of n.childIds || []) walk(byId, byId.get(c), depth, named);
      return;
    }
    const name = clip(val(n.name).replace(/\s+/g, " ").trim(), NAMED_MAX);
    if (role === "StaticText") {
      const text = name;
      if (!text || (named && named.includes(text))) return;
      if (!opts.interactive) lines.push("  ".repeat(depth) + "- text: " + clip(text, TEXT_MAX));
      return;
    }
    if (role === "RootWebArea" || role === "WebArea") {
      for (const c of n.childIds || []) walk(byId, byId.get(c), depth, named);
      return;
    }
    const backend = n.backendDOMNodeId;
    // the pointer-cursor scan reads the main document only
    const isClick = backend && !sid && !frame && clickable.has(backend);
    const at = (sid || "") + ":" + backend;
    const props = {};
    for (const p of n.properties || []) props[p.name] = p.value && p.value.value;
    const acts = INTERACTIVE.has(role) || isClick || (props.focusable && props.editable);
    if (TRANSPARENT.has(role) && !name && !acts) {
      // an iframe's document is read under its element
      if (backend && inner.has(at)) return inFrame(at, depth);
      return children();
    }
    if (role === "Iframe" || role === "iframe") {
      lines.push("  ".repeat(depth) + "- iframe" + (name ? " " + q(name) : ""));
      if (backend && inner.has(at)) inFrame(at, depth + 1);
      return;
    }
    let shown = isClick && TRANSPARENT.has(role) ? "clickable" : role;
    let label = name;
    if (isClick && !label) label = clip(textOf(byId, n).replace(/\s+/g, " ").trim(), NAMED_MAX);
    let line = "- " + shown + (label ? " " + q(label) : "");
    const states = [];
    for (const s of STATES) {
      const v = props[s];
      if (v === true || v === "true" || (s === "checked" && v === "mixed")) states.push(s === "checked" && v === "mixed" ? "checked=mixed" : s);
    }
    if (props.level && role === "heading") states.unshift("level=" + props.level);
    if (states.length) line += " [" + states.join(", ") + "]";
    if (acts && backend && !props.disabled) {
      line += " [ref=" + refFor(shown, label, backend) + "]";
      interactive++;
    } else if ((role === "image" || role === "img") && backend) {
      line += " [ref=" + refFor(role, label, backend) + "]";
    }
    const v = val(n.value);
    if (v && v !== name) line += ": " + q(clip(v.replace(/\s+/g, " "), TEXT_MAX));
    if (!opts.interactive || line.includes("[ref=") || role === "heading") lines.push("  ".repeat(depth) + line);
    const under = label ? (named ? named + "\n" + label : label) : named;
    for (const c of n.childIds || []) walk(byId, byId.get(c), opts.interactive ? depth : depth + 1, under);
  };

  const inFrame = (at, depth) => {
    const f = inner.get(at);
    if (!f) {
      lines.push("  ".repeat(depth) + "- (a frame this browser doesn't let Magpie Browser read: use a screenshot, or open its address in a tab)");
      return;
    }
    const was = [sid, frame];
    [sid, frame] = [f.sid, f.frame];
    try {
      const byId = index(f.nodes);
      walk(byId, f.nodes.find((n) => !n.parentId) || f.nodes[0], depth, "");
    } finally {
      [sid, frame] = was;
    }
  };

  let list = nodes;
  let root = nodes.find((n) => !n.parentId) || nodes[0];
  if (opts.ref) {
    const r = t.refs.get(opts.ref);
    // the ref's own document: the main one, or the frame it is in
    const f = r && (r.sid || r.frame) ? [...inner.values()].find((x) => x && x.sid === r.sid && x.frame === r.frame) : null;
    if (f) { list = f.nodes; [sid, frame] = [f.sid, f.frame]; }
    const n = r && list.find((x) => x.backendDOMNodeId === r.backend);
    if (!n) throw fault("STALE_REF", `ref ${opts.ref} isn't on the page now: take a snapshot without ref`);
    root = n;
  }
  walk(index(list), root, 0, "");
  [sid, frame] = [undefined, undefined];
  // the old refs stay good for elements still on the page that this
  // snapshot didn't show (a scoped one)
  if (opts.ref) for (const [k, v] of refs) t.refs.set(k, v);
  else t.refs = refs;

  const max = opts.maxChars || 40000;
  let body = lines.join("\n");
  let cut = "";
  if (body.length > max) {
    const at = body.lastIndexOf("\n", max);
    body = body.slice(0, at > 0 ? at : max);
    cut = `\n… (cut at ${max} characters of ${lines.join("\n").length}: pass interactive: true for only what can be acted on, or ref to read one part)`;
  }
  return { url, title, text: body + cut, count: refs.size, interactive };
}

// find is the element a ref names: its node if still there, else the one
// element the page now has with the same role and name, else an error
// that says what the page has.
export async function find(t, ref) {
  const m = /^(?:ref=)?(e\d+)$/i.exec(String(ref || "").trim());
  if (!m) throw fault("BAD_ARGS", `"${ref}" isn't a ref: refs look like e12, from the latest snapshot`);
  ref = m[1].toLowerCase();
  const r = t.refs.get(ref);
  if (!r) throw fault("STALE_REF", `ref ${ref} isn't in the latest snapshot of this tab: take a snapshot and use a ref from it`);
  try {
    const o = await resolve(t, r.backend, r);
    const c = await send(t.tabId, "Runtime.callFunctionOn", { objectId: o.objectId, functionDeclaration: "function(){return this.isConnected}", returnByValue: true }, r.sid);
    if (c.result.value) return { ...r, ref, objectId: o.objectId };
  } catch {}
  // the page drew it again: the same role and name, if only one has it
  await take(t, { maxChars: 1 });
  const same = [...t.refs].filter(([, v]) => v.role === r.role && v.name === r.name);
  if (same.length === 1) {
    const [nref, v] = same[0];
    const o = await resolve(t, v.backend, v);
    return { ...v, ref: nref, objectId: o.objectId };
  }
  if (same.length > 1) throw fault("AMBIGUOUS", `${ref} (${r.role} "${r.name}") was redrawn and ${same.length} elements now match it: take a snapshot and pick one`);
  throw fault("STALE_REF", `${ref} (${r.role} "${r.name}") is no longer on the page: take a snapshot`);
}
