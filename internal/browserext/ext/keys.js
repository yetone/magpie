// Keys as Input.dispatchKeyEvent wants them. A press is a name ("Enter",
// "a", "ArrowDown") or a chord ("Control+a", "Meta+Shift+K").

import { send, sleep } from "./cdp.js";

const KEYS = {
  Enter: { code: "Enter", keyCode: 13, text: "\r" },
  Tab: { code: "Tab", keyCode: 9 },
  Backspace: { code: "Backspace", keyCode: 8 },
  Delete: { code: "Delete", keyCode: 46 },
  Escape: { code: "Escape", keyCode: 27 },
  Space: { key: " ", code: "Space", keyCode: 32, text: " " },
  " ": { code: "Space", keyCode: 32, text: " " },
  ArrowUp: { code: "ArrowUp", keyCode: 38 },
  ArrowDown: { code: "ArrowDown", keyCode: 40 },
  ArrowLeft: { code: "ArrowLeft", keyCode: 37 },
  ArrowRight: { code: "ArrowRight", keyCode: 39 },
  Home: { code: "Home", keyCode: 36 },
  End: { code: "End", keyCode: 35 },
  PageUp: { code: "PageUp", keyCode: 33 },
  PageDown: { code: "PageDown", keyCode: 34 },
  Insert: { code: "Insert", keyCode: 45 },
  Shift: { code: "ShiftLeft", keyCode: 16 },
  Control: { code: "ControlLeft", keyCode: 17 },
  Alt: { code: "AltLeft", keyCode: 18 },
  Meta: { code: "MetaLeft", keyCode: 91 },
};
for (let i = 1; i <= 12; i++) KEYS["F" + i] = { code: "F" + i, keyCode: 111 + i };

const ALIASES = {
  return: "Enter", esc: "Escape", del: "Delete", up: "ArrowUp", down: "ArrowDown",
  left: "ArrowLeft", right: "ArrowRight", space: "Space", ctrl: "Control",
  control: "Control", cmd: "Meta", command: "Meta", meta: "Meta", option: "Alt",
  alt: "Alt", shift: "Shift", pageup: "PageUp", pagedown: "PageDown",
  backspace: "Backspace", tab: "Tab", enter: "Enter", escape: "Escape",
  delete: "Delete", home: "Home", end: "End",
};
const MODS = { Alt: 1, Control: 2, Meta: 4, Shift: 8 };
// what a chord with Control or Meta does on a Mac, where the page never
// sees the key turn into an edit
const MAC_COMMANDS = { a: "selectAll", c: "copy", x: "cut", v: "paste", z: "undo" };

function name(k) {
  if (KEYS[k]) return k;
  const a = ALIASES[k.toLowerCase()];
  if (a) return a;
  if (/^f\d{1,2}$/i.test(k)) return k.toUpperCase();
  return k;
}

function describe(k, shift) {
  const n = name(k);
  if (KEYS[n]) return { key: KEYS[n].key || n, ...KEYS[n] };
  if (n.length !== 1) throw new Error(`"${k}" isn't a key this knows: use a name like Enter, Tab, Escape, ArrowDown, PageDown, F5, or one character`);
  const ch = shift ? n.toUpperCase() : n;
  const up = n.toUpperCase();
  let code = "";
  if (/[a-z]/i.test(n)) code = "Key" + up;
  else if (/[0-9]/.test(n)) code = "Digit" + n;
  return { key: ch, code, keyCode: /[a-z0-9]/i.test(n) ? up.charCodeAt(0) : 0, text: ch };
}

const mac = /Mac/i.test(navigator.platform || navigator.userAgent);

// press presses one key or chord in the focused element.
export async function press(t, combo) {
  const parts = String(combo).split("+").map((s) => s.trim());
  if (parts.some((p) => p === "")) {
    // "Control++" or a bare "+"
    const i = parts.lastIndexOf("");
    parts.splice(i - (i > 0 ? 1 : 0), 2, "+");
  }
  const keyName = parts.pop();
  const mods = parts.map(name);
  for (const m of mods) if (!(m in MODS)) throw new Error(`"${m}" isn't a modifier: use Control, Alt, Shift or Meta`);
  let modifiers = mods.reduce((a, m) => a | MODS[m], 0);
  const k = describe(keyName, !!(modifiers & 8));
  // a modifier pressed alone is the left one, as on a keyboard
  if (/Left$/.test(k.code)) k.location = 1;
  const commands = [];
  if (mac && (modifiers & 4) && MAC_COMMANDS[k.key.toLowerCase()]) commands.push(MAC_COMMANDS[k.key.toLowerCase()]);
  // held as a hand holds them: the modifiers a moment first, the key
  // down for as long as a press lasts
  const hold = (a, b) => sleep(a + Math.random() * (b - a));
  let held = 0;
  for (const m of mods) {
    held |= MODS[m];
    await send(t.tabId, "Input.dispatchKeyEvent", { type: "rawKeyDown", modifiers: held, key: m, code: KEYS[m].code, location: 1, windowsVirtualKeyCode: KEYS[m].keyCode });
    await hold(30, 80);
  }
  // a chord with Control, Alt or Meta types no text
  const typed = k.text && !(modifiers & 7);
  await send(t.tabId, "Input.dispatchKeyEvent", {
    type: typed ? "keyDown" : "rawKeyDown", modifiers, key: k.key, code: k.code, location: k.location || 0,
    windowsVirtualKeyCode: k.keyCode,
    ...(typed ? { text: k.text, unmodifiedText: k.text } : {}),
    ...(commands.length ? { commands } : {}),
  });
  await hold(50, 120);
  await send(t.tabId, "Input.dispatchKeyEvent", { type: "keyUp", modifiers, key: k.key, code: k.code, location: k.location || 0, windowsVirtualKeyCode: k.keyCode });
  for (const m of mods.reverse()) {
    await hold(20, 60);
    held &= ~MODS[m];
    await send(t.tabId, "Input.dispatchKeyEvent", { type: "keyUp", modifiers: held, key: m, code: KEYS[m].code, location: 1, windowsVirtualKeyCode: KEYS[m].keyCode });
  }
}
