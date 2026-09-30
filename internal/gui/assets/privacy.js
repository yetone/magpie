// Screenshot privacy is a display preference. Original values stay in the
// app's data; text and tooltips are restored only while still owned here.
(() => {
  const EYE = "M2 12s3.5-8 10-8 10 8 10 8-3.5 8-10 8-10-8-10-8zM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z";
  const EYE_OFF = "M9.9 4.2A10.4 10.4 0 0 1 12 4c6.5 0 10 8 10 8a17.6 17.6 0 0 1-2.2 3.2M6.6 6.6C3.9 8.4 2 12 2 12s3.5 8 10 8a9.7 9.7 0 0 0 5.4-1.6M9.9 9.9a3 3 0 0 0 4.2 4.2M2 2l20 20";
  // Includes addresses already partly hidden by the vendor, plus apostrophes
  // and Unicode names, so no fragment of the local part is left showing.
  // A version pin (package@1.2.3 or owner/repo@v1.2.3) is not an email.
  const EMAIL = /[\p{L}\p{N}_.!#$%&'*+=?^`{|}~•-]+@[\p{L}\p{N}_*•-]+(?:\.[\p{L}\p{N}_*•-]+)*\.[\p{L}*•][\p{L}\p{N}*•-]*/gu;
  const hide = (s) => s.replace(EMAIL, (email) => email.replace(/[^@.]/gu, "•"));
  const standIn = (s) => { let h = 7; return s.replace(/[^@.]/gu, (c) => (h = (h * 31 + c.codePointAt(0)) >>> 0, "aeiounrstlcmdh"[h % 14])); };
  const text = new WeakMap(), attrs = new WeakMap(), optionValues = new WeakMap();
  const wrappers = new WeakSet();
  const ATTRS = ["title", "aria-label", "placeholder", "alt"];
  const OBS = { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: [...ATTRS, "value"] };
  const buttons = [...document.querySelectorAll("[data-privacy-toggle]")];
  let on = !!window.bootPrefs?.privacyMode, busy = false, dirty = false;
  let watching = null, epoch = 0, retry, animation, migrating = false;

  function mask(root) {
    // If the app edits a masked text node in place, keep the new text and
    // mask it afresh. Unchanged stand-ins must never be mistaken for emails.
    root = root.closest?.(".pii")?.parentElement || root;
    for (const s of root.querySelectorAll?.(".pii") || []) {
      if (!wrappers.has(s) || (s.childNodes.length === 1 && text.get(s.firstChild)?.hidden === s.textContent)) continue;
      wrappers.delete(s);
      s.replaceWith(...s.childNodes);
    }
    const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT), found = [];
    for (let n; (n = walk.nextNode());) if (!n.parentElement?.closest(".pii")) found.push(n);
    for (const n of found) {
      if (n.parentElement?.closest("script, style, textarea, [contenteditable]")) continue;
      const old = text.get(n);
      if (old && n.data === old.hidden) continue;
      const hidden = hide(n.data);
      if (hidden === n.data) { text.delete(n); continue; }
      dirty = true;
      const option = n.parentElement?.closest("option");
      if (option && !option.hasAttribute("value")) {
        optionValues.set(option, option.value);
        option.value = option.value; // changing the label must not change a form value
      }
      if (n.parentElement?.closest("option, svg, title")) {
        // Native options and SVG text cannot contain HTML masking spans.
        text.set(n, { raw: n.data, hidden });
        n.data = hidden;
        continue;
      }
      const bits = [];
      let last = 0;
      for (const m of n.data.matchAll(EMAIL)) {
        if (m.index > last) bits.push(document.createTextNode(n.data.slice(last, m.index)));
        const s = document.createElement("span"); s.className = "pii";
        const replacement = document.createTextNode(standIn(m[0]));
        text.set(replacement, { raw: m[0], hidden: replacement.data });
        s.append(replacement); wrappers.add(s); bits.push(s);
        last = m.index + m[0].length;
      }
      if (last < n.data.length) bits.push(document.createTextNode(n.data.slice(last)));
      if (bits.length === 1) n.replaceWith(bits[0]);
      else {
        // Keep a sentence as one flex item, as the original text node was.
        const run = document.createElement("span"); run.className = "pii-run";
        run.append(...bits); wrappers.add(run); n.replaceWith(run);
      }
    }
    const elements = root instanceof Element ? [root, ...root.querySelectorAll("*")] : [];
    for (const e of elements) {
      let saved = attrs.get(e);
      for (const name of ATTRS) {
        const value = e.getAttribute(name);
        if (value == null) { saved?.delete(name); continue; }
        if (saved?.get(name)?.hidden === value) continue;
        const hidden = hide(value);
        if (hidden === value) { saved?.delete(name); continue; }
        if (!saved) attrs.set(e, saved = new Map());
        dirty = true;
        saved.set(name, { raw: value, hidden });
        e.setAttribute(name, hidden);
      }
      if (e.matches("input, textarea, [contenteditable]")) {
        const value = e.value ?? e.textContent;
        e.classList.toggle("privacy-value", hide(value) !== value);
        if (e.classList.contains("privacy-value")) dirty = true;
      }
    }
  }

  function restore() {
    if (!dirty) return;
    dirty = false;
    const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n; (n = walk.nextNode());) {
      const old = text.get(n);
      if (old && n.data === old.hidden) n.data = old.raw;
      text.delete(n);
    }
    for (const s of document.querySelectorAll(".pii, .pii-run")) if (wrappers.has(s)) {
      wrappers.delete(s);
      s.replaceWith(...s.childNodes);
    }
    for (const e of document.body.querySelectorAll("*")) {
      const saved = attrs.get(e);
      for (const [name, old] of saved || []) if (e.getAttribute(name) === old.hidden) e.setAttribute(name, old.raw);
      attrs.delete(e);
      if (optionValues.has(e)) {
        if (e.value === optionValues.get(e)) e.removeAttribute("value");
        optionValues.delete(e);
      }
      e.classList.remove("privacy-value");
    }
  }

  const watch = new MutationObserver((records) => {
    if (!on) return;
    watch.disconnect();
    // New rows and changed tooltips are masked before the next paint. Only
    // visit changed subtrees, not every page on each streamed update.
    const roots = new Set(records.map((r) => r.target.nodeType === Node.TEXT_NODE ? r.target.parentElement : r.target));
    for (const root of roots) if (root?.isConnected && ![...roots].some((other) => other !== root && other.contains(root))) mask(root);
    watch.observe(document.body, OBS);
  });

  function paint() {
    watch.disconnect();
    document.body.classList.toggle("privacy-mode", on);
    for (const b of buttons) {
      b.setAttribute("aria-pressed", String(on));
      b.disabled = busy;
      const label = on ? "Privacy mode on" : "Privacy mode";
      b.setAttribute("aria-label", t("Privacy mode"));
      b.title = t("Hide emails across the app and quick panel for screenshots");
      b.querySelector("path").setAttribute("d", on ? EYE_OFF : EYE);
      const words = b.querySelector("[data-t]");
      if (words) { words.dataset.en = b.id === "privacySetting" ? (on ? "On" : "Off") : label; words.textContent = t(words.dataset.en); }
    }
    if (on) { mask(document.body); watch.observe(document.body, OBS); }
    else restore();
  }

  function accept(s) {
    if (typeof s?.on !== "boolean") return;
    if (s.on && !on) {
      document.body.classList.add("masking");
      clearTimeout(animation);
      animation = setTimeout(() => document.body.classList.remove("masking"), 450);
    }
    on = s.on;
    paint();
  }

  async function save(next) {
    if (busy) return;
    busy = true;
    epoch++;
    watching?.abort();
    paint();
    try {
      const res = await fetch("/api/settings/privacy", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ on: next }) });
      const s = await res.json();
      if (!res.ok) throw new Error(s.error || res.statusText);
      accept(s);
      migrating = false;
      try { localStorage.removeItem("magpie.maskEmails"); } catch {}
    } catch (e) {
      if (typeof status === "function") status(e.message, "err");
      else { const note = document.querySelector("#status"); note.textContent = e.message; note.className = "status err"; }
    }
    finally { busy = false; paint(); listen(); }
  }

  async function listen() {
    clearTimeout(retry);
    if (busy || migrating || typeof window.bootPrefs?.privacyMode !== "boolean") return;
    watching?.abort();
    const controller = watching = new AbortController(), asked = ++epoch;
    try {
      const res = await fetch(`/api/settings/privacy?wait=1&on=${on ? 1 : 0}`, { signal: controller.signal });
      if (!res.ok) throw new Error(res.statusText);
      const s = await res.json();
      // A toggle cancels older reads, including replies already in flight.
      if (asked !== epoch || busy) return;
      accept(s);
      retry = setTimeout(listen, 0);
    } catch (e) {
      if (e.name !== "AbortError" && asked === epoch) retry = setTimeout(listen, 3000);
    }
  }

  for (const b of buttons) b.onclick = () => save(!on);
  window.refreshPrivacy = paint;
  document.addEventListener("input", (e) => { if (on) mask(e.target); }, true);
  document.addEventListener("change", (e) => { if (on) mask(e.target); }, true);
  // An old page-local choice stays masked until the shared preference is saved.
  let legacy = false;
  try { legacy = localStorage.getItem("magpie.maskEmails") === "1"; } catch {}
  if (legacy && !on) {
    migrating = true; on = true; paint();
    document.addEventListener("DOMContentLoaded", () => save(true), { once: true });
  }
  else {
    if (legacy) { try { localStorage.removeItem("magpie.maskEmails"); } catch {} }
    paint(); listen();
  }
  window.addEventListener("focus", listen);
  // Keep a hidden panel in sync before it is shown, avoiding a frame of raw emails.
  document.addEventListener("visibilitychange", () => { if (!document.hidden) listen(); });
  window.addEventListener("pagehide", () => { epoch++; watching?.abort(); clearTimeout(retry); });
  window.addEventListener("pageshow", listen);
})();
