// Input as a person makes it. A site that watches for automation reads
// how input arrives: a pointer that jumps to the exact middle of a button
// and clicks in the same instant, text that appears with no key pressed,
// a page scrolled in one leap, one action straight after another. Here the
// mouse travels a curved, uneven path to a point somewhere on the element,
// waits a moment and holds the button as a finger does; text is typed key
// by key with a typist's uneven rhythm (an input method's composition for
// Chinese, Japanese and Korean); a scroll is the wheel's notches; and an
// action waits a person's moment after the last one.

import { send, sleep } from "./cdp.js";

const rand = (a, b) => a + Math.random() * (b - a);
// a normal draw (Box–Muller), for what clusters around a value
function gauss(mean, sd) {
  let u = 0, v = 0;
  while (!u) u = Math.random();
  while (!v) v = Math.random();
  return mean + sd * Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v);
}
const clamp = (x, a, b) => Math.min(b, Math.max(a, x));

// pace waits until a person's moment has passed since the last action in
// the tab: no one clicks the next thing in the same instant
export async function pace(t) {
  const since = Date.now() - (t.lastInput || 0);
  const want = clamp(gauss(750, 250), 350, 1600);
  if (since < want) await sleep(want - since);
}

export function done(t) {
  t.lastInput = Date.now();
}

// aim is where on a box [x0, y0, x1, y1] to put the mouse: near the middle,
// rarely the exact middle, never at the edges
export function aim(x0, y0, x1, y1) {
  const w = x1 - x0, h = y1 - y0;
  const fx = clamp(gauss(0.5, 0.12), 0.25, 0.75);
  const fy = clamp(gauss(0.5, 0.1), 0.3, 0.7);
  return { x: x0 + w * fx, y: y0 + h * fy };
}

const ease = (s) => (s < 0.5 ? 2 * s * s : 1 - Math.pow(-2 * s + 2, 2) / 2);

// move takes the mouse from where it was to (x, y) along a curve, quick in
// the middle and slow at both ends, a few pixels off a straight line;
// buttons is held down for a drag
export async function move(t, x, y, opts = {}) {
  const buttons = opts.buttons || 0;
  const modifiers = opts.modifiers || 0;
  let from = t.mouse;
  if (!from) {
    // the first move in a tab comes in from somewhere off to one side
    const m = await send(t.tabId, "Page.getLayoutMetrics").catch(() => null);
    const vw = (m && (m.cssVisualViewport || m.layoutViewport)) || { clientWidth: 1000, clientHeight: 700 };
    from = { x: clamp(x + rand(-260, 260), 2, vw.clientWidth - 2), y: clamp(y + rand(120, 260) * (Math.random() < 0.5 ? -1 : 1), 2, vw.clientHeight - 2) };
  }
  const dx = x - from.x, dy = y - from.y;
  const dist = Math.hypot(dx, dy);
  const steps = clamp(Math.round(dist / rand(18, 30)), dist < 3 ? 1 : 4, 40);
  // two control points off the line, on one side: an arm's arc
  const side = Math.random() < 0.5 ? -1 : 1;
  const bend = clamp(dist * rand(0.05, 0.2), 0, 120) * side;
  const nx = dist ? -dy / dist : 0, ny = dist ? dx / dist : 0;
  const c1 = { x: from.x + dx * 0.3 + nx * bend, y: from.y + dy * 0.3 + ny * bend };
  const c2 = { x: from.x + dx * 0.7 + nx * bend * 0.6, y: from.y + dy * 0.7 + ny * bend * 0.6 };
  for (let i = 1; i <= steps; i++) {
    const s = ease(i / steps), r = 1 - s;
    let px = r * r * r * from.x + 3 * r * r * s * c1.x + 3 * r * s * s * c2.x + s * s * s * x;
    let py = r * r * r * from.y + 3 * r * r * s * c1.y + 3 * r * s * s * c2.y + s * s * s * y;
    if (i < steps) { px += rand(-0.7, 0.7); py += rand(-0.7, 0.7); }
    await send(t.tabId, "Input.dispatchMouseEvent", {
      type: "mouseMoved", x: Math.round(px * 100) / 100, y: Math.round(py * 100) / 100, modifiers,
      button: buttons ? "left" : "none", buttons,
    });
    t.mouse = { x: px, y: py };
    await sleep(rand(7, 17));
  }
  t.mouse = { x, y };
}

const BUTTONS = { left: "left", right: "right", middle: "middle" };
const BUTTON_BIT = { left: 1, right: 2, middle: 4 };

// click moves to (x, y), waits as a hand settles on the spot, and presses
// and lets go of the button (twice for a double click)
export async function click(t, x, y, opts = {}) {
  const button = BUTTONS[opts.button] || "left";
  const modifiers = opts.modifiers || 0;
  const count = opts.double ? 2 : 1;
  await move(t, x, y, { modifiers });
  await sleep(clamp(gauss(110, 40), 45, 260));
  for (let i = 1; i <= count; i++) {
    await send(t.tabId, "Input.dispatchMouseEvent", { type: "mousePressed", x, y, button, buttons: BUTTON_BIT[button], clickCount: i, modifiers });
    await sleep(clamp(gauss(80, 22), 40, 150));
    await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button, buttons: 0, clickCount: i, modifiers });
    if (i < count) await sleep(rand(70, 130));
  }
}

// drag presses at one point, moves to the other with the button held and
// lets go there
export async function drag(t, p, q) {
  await move(t, p.x, p.y);
  await sleep(rand(80, 160));
  await send(t.tabId, "Input.dispatchMouseEvent", { type: "mousePressed", x: p.x, y: p.y, button: "left", buttons: 1, clickCount: 1 });
  try {
    await sleep(rand(90, 180));
    await move(t, q.x, q.y, { buttons: 1 });
    await sleep(rand(80, 160));
  } finally {
    await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseReleased", x: q.x, y: q.y, button: "left", buttons: 0, clickCount: 1 });
  }
}

// wheel scrolls by (dx, dy) at (x, y) a wheel notch at a time, the mouse
// moved there first
export async function wheel(t, x, y, dx, dy) {
  if (!t.mouse || Math.hypot(t.mouse.x - x, t.mouse.y - y) > 40) await move(t, x, y);
  const total = Math.abs(dx) + Math.abs(dy);
  const notch = 100;
  let n = Math.max(1, Math.round(total / notch));
  let left = { x: dx, y: dy };
  for (let i = 0; i < n; i++) {
    const last = i === n - 1;
    const sx = last ? left.x : Math.round(dx / n), sy = last ? left.y : Math.round(dy / n);
    left = { x: left.x - sx, y: left.y - sy };
    await send(t.tabId, "Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: sx, deltaY: sy });
    // a flick turns a few notches quickly, then the finger comes back
    await sleep(i % 4 === 3 ? rand(120, 260) : rand(28, 70));
  }
}

// ---- keys ----------------------------------------------------------------

// a US layout: the key a character is on, and whether Shift is held
const SHIFTED = { "!": "1", "@": "2", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7", "*": "8", "(": "9", ")": "0", "_": "-", "+": "=", "{": "[", "}": "]", "|": "\\", ":": ";", "\"": "'", "<": ",", ">": ".", "?": "/", "~": "`" };
const PUNCT = { "-": ["Minus", 189], "=": ["Equal", 187], "[": ["BracketLeft", 219], "]": ["BracketRight", 221], "\\": ["Backslash", 220], ";": ["Semicolon", 186], "'": ["Quote", 222], ",": ["Comma", 188], ".": ["Period", 190], "/": ["Slash", 191], "`": ["Backquote", 192], " ": ["Space", 32] };

// keyOf is the key a character is typed with on a US keyboard, or null
// when it isn't on one
export function keyOf(ch) {
  let base = ch, shift = false;
  if (/^[A-Z]$/.test(ch)) { base = ch.toLowerCase(); shift = true; }
  else if (SHIFTED[ch]) { base = SHIFTED[ch]; shift = true; }
  if (/^[a-z]$/.test(base)) return { key: ch, code: "Key" + base.toUpperCase(), keyCode: base.toUpperCase().charCodeAt(0), shift };
  if (/^[0-9]$/.test(base)) return { key: ch, code: "Digit" + base, keyCode: base.charCodeAt(0), shift };
  if (PUNCT[base]) return { key: ch, code: PUNCT[base][0], keyCode: PUNCT[base][1], shift };
  return null;
}

// text typed through an input method: Chinese, Japanese, Korean
const IME = /[぀-ヿ㐀-䶿一-鿿豈-﫿가-힯　-〿＀-￯]/;

async function shiftKey(t, down) {
  await send(t.tabId, "Input.dispatchKeyEvent", { type: down ? "rawKeyDown" : "keyUp", key: "Shift", code: "ShiftLeft", windowsVirtualKeyCode: 16, modifiers: down ? 8 : 0 });
}

// the pause after a key: a typist is quicker inside a word than between
// words, and stops to think now and then; scaled down for a long text
function gap(ch, scale) {
  let ms = clamp(gauss(105, 38), 38, 260);
  if (ch === " ") ms += rand(10, 70);
  else if (/[.,!?;:，。！？；：、]/.test(ch)) ms += rand(60, 220);
  if (Math.random() < 0.035) ms += rand(250, 700);
  return Math.max(10, ms * scale);
}

// how long typing may take in all: the gateway waits 100s for a call
const TYPE_BUDGET_MS = 35000;

// type types text into the focused element. newline is how a line break
// is typed there: "Shift+Enter" in a field where Enter sends (a chat box),
// "" where there are no lines (a one-line field drops it).
export async function type(t, text, opts = {}) {
  const chars = [...String(text)];
  const est = chars.length * 120;
  const scale = est > TYPE_BUDGET_MS ? TYPE_BUDGET_MS / est : 1;
  const newline = opts.newline ?? "Shift+Enter";
  let held = false;
  for (let i = 0; i < chars.length; i++) {
    const ch = chars[i];
    if (ch === "\r") continue;
    if (ch === "\n") {
      if (held) { await shiftKey(t, false); held = false; }
      if (newline) await opts.press(newline);
      await sleep(gap(ch, scale));
      continue;
    }
    if (IME.test(ch)) {
      if (held) { await shiftKey(t, false); held = false; }
      // a word or two at a time: what an input method commits
      let run = ch;
      const len = Math.round(rand(1, 4));
      while (run.length < len && i + 1 < chars.length && IME.test(chars[i + 1])) run += chars[++i];
      await compose(t, run, scale);
      await sleep(gap(run.slice(-1), scale) * 1.5);
      continue;
    }
    const k = keyOf(ch);
    if (!k) {
      // an emoji or a letter no US key has: chosen from a picker, as a
      // person enters one
      if (held) { await shiftKey(t, false); held = false; }
      await send(t.tabId, "Input.insertText", { text: ch });
      await sleep(gap(ch, scale) * 2);
      continue;
    }
    if (k.shift !== held) {
      await shiftKey(t, k.shift);
      held = k.shift;
      await sleep(rand(15, 45) * scale);
    }
    const mods = held ? 8 : 0;
    await send(t.tabId, "Input.dispatchKeyEvent", { type: "keyDown", key: k.key, code: k.code, windowsVirtualKeyCode: k.keyCode, nativeVirtualKeyCode: k.keyCode, text: ch, unmodifiedText: k.shift ? ch : ch, modifiers: mods });
    // the key comes up while the next is already on its way down, now
    // and then, as fingers roll
    await sleep(clamp(gauss(55, 18), 18, 120) * Math.max(scale, 0.3));
    await send(t.tabId, "Input.dispatchKeyEvent", { type: "keyUp", key: k.key, code: k.code, windowsVirtualKeyCode: k.keyCode, nativeVirtualKeyCode: k.keyCode, modifiers: mods });
    const next = chars[i + 1];
    if (held && !(next && keyOf(next) && keyOf(next).shift)) { await sleep(rand(10, 40) * scale); await shiftKey(t, false); held = false; }
    await sleep(gap(ch, scale));
  }
  if (held) await shiftKey(t, false);
}

// compose enters text as an input method does: the keys go to it (key
// "Process"), the text under composition grows, and it is committed
async function compose(t, run, scale) {
  const ime = (type) => send(t.tabId, "Input.dispatchKeyEvent", { type, key: "Process", code: "", windowsVirtualKeyCode: 229, nativeVirtualKeyCode: 229 });
  let ok = true;
  for (let j = 1; j <= run.length && ok; j++) {
    await ime("rawKeyDown");
    const part = run.slice(0, j);
    ok = await send(t.tabId, "Input.imeSetComposition", { text: part, selectionStart: part.length, selectionEnd: part.length }).then(() => true, () => false);
    await sleep(clamp(gauss(50, 15), 20, 100) * Math.max(scale, 0.3));
    await ime("keyUp");
    await sleep(clamp(gauss(140, 50), 50, 320) * scale);
  }
  await ime("rawKeyDown");
  await send(t.tabId, "Input.insertText", { text: run });
  await sleep(rand(20, 60) * Math.max(scale, 0.3));
  await ime("keyUp");
}
