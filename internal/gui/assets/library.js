// Library: one set of instructions, MCP servers and skills, written into
// every agent that should have them (#29). magpie keeps the library in its
// own folder and writes each agent's files from it; what the user has in
// those files besides stays theirs, and every file is kept aside before
// magpie writes it. Everything shown here comes from /api/library.
(() => {
  const page = $("#view-library");
  if (!page) return;

  let lib = null;        // the page, as /api/library gives it
  let tab = "instructions";
  let shown = "";        // the tab the page last drew, which fades in only when it changes
  let fits = [];         // textareas to fit before the page is painted
  try { tab = localStorage.getItem("magpie.libTab") || tab; } catch {}
  const texts = {};      // set → its shared instructions as typed, while not saved
  let openSet = null;    // the set of instructions opened to edit
  let addingSet = false; // a new set being named
  let removingSet = null; // a set whose Remove was clicked once
  const extras = {};     // agent → its own additions as typed, while not saved
  const open = new Set(); // agents whose instructions row is open
  let probe = null;      // skills found at a source: { source, candidates, pick:Set, agents:Set }
  let probing = false;
  let checking = false;  // asking GitHub which skills it has changed
  let modal = null;      // what the library has open in the dialog

  const GLYPH = {
    cmd: "M3 4.5h10v7H3z M5.5 7l1.5 1.2-1.5 1.3 M8.5 9.5h2",
    web: "M8 2.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 0 0 0-11z M2.5 8h11 M8 2.5c1.6 1.7 2.3 3.5 2.3 5.5S9.6 11.8 8 13.5 M8 2.5C6.4 4.2 5.7 6 5.7 8s.7 3.8 2.3 5.5",
    skill: "M4 2.5h6.5L12 4v9.5H4z M6 6h4 M6 8.5h4 M6 11h2.5",
    doc: "M4 2.5h5.5L12 5v8.5H4z M9.5 2.5V5H12",
    folder: "M2.5 4.5h4l1.2 1.3h5.8v6.7h-11z",
    up: "M8 12.5v-9 M4.5 7 8 3.5 11.5 7",
    down: "M8 3.5v9 M4.5 9 8 12.5 11.5 9",
    trash: "M3.5 4.5h9 M6.5 4.5V3h3v1.5 M4.5 4.5l.6 8.5h5.8l.6-8.5",
    search: "M7 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M10 10l3 3",
    out: "M9.5 3.5h3v3 M12.5 3.5 7.5 8.5 M11 9.5v3H3.5V5h3",
    key: "M10 2.8a3.2 3.2 0 1 0 0 6.4 3.2 3.2 0 0 0 0-6.4z M7.8 8.2 3 13 M4.5 11.5 6 13",
    person: "M8 3a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5z M3.5 13.5c.6-2.4 2.3-3.6 4.5-3.6s3.9 1.2 4.5 3.6",
    back: "m9.5 4.5-3 3.5 3 3.5",
  };

  const tilde = (p) => (lib?.home && p?.startsWith(lib.home) ? "~" + p.slice(lib.home.length) : p || "");
  const agentOf = (id) => lib.agents.find((a) => a.id === id);
  const nameOf = (id) => agentOf(id)?.name || id;
  // An agent with no MCP of its own (Pi) reads its servers through an
  // extension, which each of its chips says.
  // An agent hidden on the Agents page is left out here too (#71): what it
  // already has stays with it, and a new server or skill isn't given to it.
  const shownAgents = () => lib.agents.filter((a) => !isHidden(a));
  const mcpAgents = () => shownAgents().filter((a) => a.mcp)
    .map((a) => a.mcpVia ? { ...a, aside: t("{agent} reads MCP servers through the {ext} extension", { agent: a.name, ext: a.mcpVia }) } : a);
  const skillAgents = () => shownAgents().filter((a) => a.skills);

  function glyph(d, cls = "lib-glyph") {
    const g = el("span", cls);
    g.append(svg(d, 16, 1.3));
    return g;
  }
  // A row's mark: the thing's own icon where the market knows it, else a
  // glyph for what it is — never a made-up picture.
  function mark(url, d) {
    if (!url) return glyph(d);
    const box = logo(url, "");
    box.classList.add("lib-logo");
    box.querySelector("img").onerror = () => box.replaceWith(glyph(d));
    return box;
  }
  function button(text, cls, onclick) {
    const b = el("button", "text " + (cls || ""), text);
    b.onclick = (e) => { e.stopPropagation(); onclick(e, b); };
    return b;
  }
  // a path shown on the page: a click shows it in the file manager
  function pathLink(p) {
    const b = el("button", "lib-path", tilde(p));
    b.title = t("Show in {fm}", { fm: fileManager() });
    b.onclick = (e) => { e.stopPropagation(); reveal(p); };
    return b;
  }
  function fileManager() { return /^Mac/.test(navigator.platform) ? "Finder" : /^Win/.test(navigator.platform) ? "Explorer" : t("the file manager"); }
  // the app's webview opens no new windows: a link goes to the system browser
  function browse(u) { api("open", { url: u }).catch((e) => status(e.message, "err")); }
  function reveal(p) { api("library/reveal", { path: p }).catch((e) => status(e.message, "err")); }

  // A switch: on or off, nothing between.
  function toggle(on, label, onChange) {
    const s = el("button", "lib-switch" + (on ? " on" : ""));
    s.setAttribute("role", "switch");
    s.setAttribute("aria-checked", on ? "true" : "false");
    s.setAttribute("aria-label", label);
    s.append(el("i"));
    s.onclick = (e) => { e.stopPropagation(); onChange(!on); };
    return s;
  }

  // An agent's icon, made once and copied: a list of hundreds of skills has
  // a chip for each agent on every row, and making each icon afresh (its
  // image, and a probe of whether it loads) was most of drawing the list.
  // One whose picture didn't load is let go, so the next row makes it
  // afresh: kept, its copies would all be empty until the page was loaded
  // again (as Claude Code's were seen to, in the skills list).
  // It's made apart from the Providers page's kept icons, which are its
  // own rows' to take back.
  const icons = new Map();
  function agentIcon(name) {
    let i = icons.get(name);
    if (!i) {
      const kept = keptIcons;
      keptIcons = null;
      try { i = icon(name); } finally { keptIcons = kept; }
      icons.set(name, i);
      const im = i.querySelector(":scope > img");
      if (im) im.onerror = () => { if (icons.get(name) === i) icons.delete(name); };
      greyIcon(i);
    }
    return i.cloneNode(true);
  }
  // A copy already drawn whose picture failed asks for it again, a few
  // times and a little later each time; the grey copy is left to the filter.
  document.addEventListener("error", (e) => {
    const im = e.target;
    if (im.tagName !== "IMG" || im.classList.contains("grey") || !im.parentElement?.classList.contains("ic") || !im.closest("#view-library, #modal.lib")) return;
    const n = +(im.dataset.tries || 0);
    if (n >= 3) return;
    im.dataset.tries = n + 1;
    setTimeout(() => { if (im.isConnected) im.src = srcOf(im) + "?try=" + (n + 1); }, 400 * (n + 1));
  }, true);
  const srcOf = (im) => (im.getAttribute("src") || "").split("?")[0];

  // A chip's icon is grey while its agent hasn't the item. A filter made it
  // so on every paint of every chip — most of what a scroll through
  // hundreds of skills painted — so it's made grey once instead: a picture
  // gets a grey copy (by the filter's own sum), which stands in for
  // it once ready, and an icon drawn in the text's colour takes that colour
  // grey (library.css). Until then, or if it can't be, the filter does it.
  function greyIcon(i) {
    if (i.querySelector(":scope > .mask, :scope > svg")) { i.classList.add("flat"); return; }
    const src = i.querySelector(":scope > img") && srcOf(i.querySelector(":scope > img"));
    if (!src) return;
    greyCopy(src).then((url) => {
      if (!url) return;
      const add = (ic) => {
        if (ic.classList.contains("baked")) return;
        const g = el("img", "grey");
        g.src = url;
        g.alt = "";
        g.draggable = false;
        ic.append(g);
        ic.classList.add("baked");
      };
      add(i);
      for (const x of document.querySelectorAll(".lib-ag > .ic:not(.baked) > img")) if (srcOf(x) === src) add(x.parentElement);
    }, () => {});
  }
  // grayscale(1)'s own sum: each colour's red, green and blue become this
  const lum = (r, g, b) => Math.round(r * .2126 + g * .7152 + b * .0722);
  let tint;
  function greyColour(v) {
    tint ||= document.createElement("canvas").getContext("2d");
    // a colour in any spelling, read back as #rrggbb or rgba(…); anything
    // else (none, url(#…), currentColor) is left as it is
    if (/^\s*(none|currentcolor|inherit|transparent|url\()/i.test(v)) return v;
    tint.fillStyle = "#010203";
    tint.fillStyle = v;
    const c = tint.fillStyle;
    let m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(c);
    if (m) {
      if (c === "#010203" && !/010203/.test(v)) return v;
      const y = lum(parseInt(m[1], 16), parseInt(m[2], 16), parseInt(m[3], 16)).toString(16).padStart(2, "0");
      return "#" + y + y + y;
    }
    m = /^rgba?\(\s*([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\s*\)$/.exec(c);
    if (!m) return v;
    const y = lum(+m[1], +m[2], +m[3]);
    return `rgba(${y}, ${y}, ${y}, ${m[4] ?? 1})`;
  }
  async function greyCopy(src) {
    const r = await fetch(src);
    if (!r.ok) return null;
    let blob;
    if (/svg/.test(r.headers.get("content-type") || "") || /\.svg$/.test(src)) {
      // the drawing with each of its colours made grey — the same as the
      // filter, which is a sum over each colour (blending and gradients
      // mix colours in sRGB, as the filter does, so they come out the same)
      // and drawn as sharp as the drawing itself
      // (a name inside another, as data-fill, is left: checked by hand, as a
      // lookbehind in the pattern is a syntax error before Safari 16.4, #220)
      const inWord = (s, at) => /[\w-]/.test(s[at - 1] || "");
      const s = (await r.text()).replace(/(fill|stroke|stop-color|flood-color|lighting-color|color)(\s*=\s*)(["'])([^"']*)\3/g, (all, k, eq, q, v, at, s) => inWord(s, at) ? all : k + eq + q + greyColour(v) + q)
        .replace(/(fill|stroke|stop-color|flood-color|lighting-color|color)(\s*:\s*)([^;"'}]+)/g, (all, k, eq, v, at, s) => inWord(s, at) ? all : k + eq + greyColour(v));
      if (/<(image|feColorMatrix|feComponentTransfer|feTurbulence)\b/.test(s)) return null; // a colour the sum can't reach
      blob = new Blob([s], { type: "image/svg+xml" });
    } else {
      // a picture made grey pixel by pixel
      const im = new Image();
      im.src = URL.createObjectURL(await r.blob());
      await im.decode();
      const c = document.createElement("canvas");
      c.width = im.naturalWidth;
      c.height = im.naturalHeight;
      const x = c.getContext("2d");
      x.drawImage(im, 0, 0);
      URL.revokeObjectURL(im.src);
      const d = x.getImageData(0, 0, c.width, c.height), p = d.data;
      for (let k = 0; k < p.length; k += 4) p[k] = p[k + 1] = p[k + 2] = p[k] * .2126 + p[k + 1] * .7152 + p[k + 2] * .0722;
      x.putImageData(d, 0, 0);
      blob = await new Promise((done) => c.toBlob(done));
      if (!blob) return null;
    }
    const url = URL.createObjectURL(blob);
    const t = new Image();
    t.src = url;
    await t.decode(); // ready before it stands in, so nothing blinks
    return url;
  }

  // Agent chips for a server or a skill: each agent that could have it, lit
  // when it does. A chip whose agent couldn't be given it says why. One that
  // has it whatever is ticked (opts.always: a skill kept in ~/.agents/skills,
  // which the agent reads itself, #595) is lit, can't be clicked, and says
  // why; it is left out of what a click sends, and keeps what it had.
  function agentChips(all, on, onChange, opts = {}) {
    on ||= [];
    const box = el("div", "lib-agents");
    box.style.setProperty("--lib-agent-count", all.length);
    // the row it is put in says so (.with-agents), for its name's room:
    // Safari before 15.4 has no :has(). Every caller appends it at once.
    queueMicrotask(() => box.parentElement?.classList.contains("lib-row") && box.parentElement.classList.add("with-agents"));
    for (const a of all) {
      const always = opts.always?.(a) || "";
      const has = on.includes(a.id) || !!always;
      const c = el("button", "lib-ag" + (has ? " on" : "") + (always ? " always" : ""));
      c.dataset.agent = a.id;
      c.append(agentIcon(a.icon));
      if (opts.names) c.append(el("span", "n", a.name));
      const problem = !always && opts.problems?.[a.id];
      const blocked = always || opts.blocked?.(a);
      const via = !has && opts.via?.(a);
      let tip = has ? t("{agent} has it — click to take it away", { agent: a.name }) : t("Give it to {agent}", { agent: a.name });
      if (via) { c.classList.add("via"); tip = t("{agent} reads it through {other} — click to give it its own", { agent: a.name, other: via }); }
      if (problem) { c.classList.add("warn"); tip = a.name + ": " + problem; }
      // not disabled: a disabled button tells nothing on a click, and
      // WebKit shows it no tooltip either — 蓝猫 clicked Claude Desktop's
      // grey chip for a remote server and nothing said why
      if (blocked) { c.classList.add("blocked"); c.setAttribute("aria-disabled", "true"); tip = blocked; }
      c.title = a.aside && !problem && !blocked ? tip + "\n" + a.aside : tip;
      c.setAttribute("aria-pressed", has ? "true" : "false");
      // What's lit is read off the chips clicked, not the list they were
      // drawn with: a row's chips change in place, several clicks before
      // magpie has answered the first, and take on the handlers of chips
      // drawn for them (morphChips). An agent not shown keeps what it has.
      c.onclick = (e) => {
        e.stopPropagation();
        const me = e.currentTarget;
        if (cannot(me)) { status(me.title, "warn", 8000); return; }
        const lit = litOf(me.parentElement);
        const kept = keptOf(me.parentElement, all, on);
        onChange([...kept, ...(lit.includes(a.id) ? lit.filter((x) => x !== a.id) : [...lit, a.id])], me);
      };
      box.append(c);
    }
    if (opts.all) allChip(box, all, on, onChange);
    return box;
  }

  // cannot says a chip's agent can't be given the item, or has it whatever
  // is ticked: a click says why rather than switching it
  const cannot = (c) => c.getAttribute("aria-disabled") === "true";

  // The agents a row's chips have lit by a click: not one lit because it
  // has the item whatever is ticked.
  function litOf(box) {
    return [...box.children].filter((x) => x.dataset.agent && !x.classList.contains("always") && x.getAttribute("aria-pressed") === "true").map((x) => x.dataset.agent);
  }
  // What a click leaves as it was: an agent not shown, and one that has the
  // item whatever is ticked, whose chip can't be clicked.
  function keptOf(box, all, on) {
    const fixed = [...box.children].filter((x) => x.classList.contains("always")).map((x) => x.dataset.agent);
    return on.filter((id) => !all.some((x) => x.id === id) || fixed.includes(id));
  }

  // All, ahead of a row's chips: one click gives the item to every agent
  // shown that can take it (not those a chip is greyed out for: no SSE, no
  // remote server), and when they all have it, takes it from every one.
  // An agent not shown keeps what it has, as with a chip.
  function allChip(box, all, on, onChange) {
    const can = [...box.children].filter((c) => !cannot(c)).map((c) => c.dataset.agent);
    if (can.length < 2) return;
    const c = el("button", "lib-ag all", t("All"));
    c.dataset.all = "1";
    c.onclick = (e) => {
      e.stopPropagation();
      const me = e.currentTarget;
      const lit = litOf(me.parentElement);
      const kept = keptOf(me.parentElement, all, on);
      onChange(can.every((id) => lit.includes(id)) ? kept : [...new Set([...kept, ...lit, ...can])], me);
    };
    box.prepend(c);
    paintAll(box);
  }
  // the All chip shows whether every agent that can take the item has it
  function paintAll(box) {
    const c = box.querySelector(":scope > .lib-ag.all");
    if (!c) return;
    const can = [...box.children].filter((x) => x.dataset.agent && !cannot(x));
    const n = can.length, full = can.every((x) => x.getAttribute("aria-pressed") === "true");
    c.classList.toggle("on", full);
    c.setAttribute("aria-pressed", full ? "true" : "false");
    c.title = full ? t("Every agent that can take it has it — click to take it from all {n}", { n }) : t("Give it to all {n} agents that can take it", { n });
  }

  // A row's chips switched in place: the page isn't drawn again, which
  // lost the chips' hover (they folded back together and spread again under
  // the pointer) and blinked their icons (#69). The chip shows the click at
  // once and the next click needn't wait: a row writes one list at a time,
  // and clicks made meanwhile are sent together once it's answered, so the
  // last click is what the agents end up with. What magpie wrote is then
  // painted onto the same buttons — or, when it couldn't, what it has, which
  // takes back the clicks it refused. Nothing else on the page is drawn
  // again for it: the rest is drawn from the new answer when the page next is.
  const writing = new Map(); // path + name → { list, name, want, box } while a row's agents are written
  let refused = 0;           // when a row's write last failed, whose error another row's "Written" doesn't cover
  function chipsChange(path, name, list, rowOf) {
    return async (next, c) => {
      // All lights or darkens every chip of the row, a chip only itself
      const every = !!c.dataset.all;
      for (const x of every ? [...c.parentElement.children].filter((y) => y.dataset.agent && !y.classList.contains("always")) : [c]) {
        const on = next.includes(x.dataset.agent);
        x.classList.toggle("on", on);
        if (on || x === c) x.classList.remove("via");
        x.setAttribute("aria-pressed", on ? "true" : "false");
      }
      paintAll(c.parentElement);
      const key = path + "\n" + name;
      const w = writing.get(key);
      if (w) { w.want = next; w.box = c.parentElement; w.every = every; take(lib); return; }
      const me = { list, name, want: next, box: c.parentElement, every };
      writing.set(key, me);
      take(lib);
      let sent = null, failed = false;
      try {
        // clicks that came back to what was sent aren't sent again
        const same = () => [...sent].sort().join() === [...me.want].sort().join();
        while (!sent || !same()) {
          sent = me.want;
          const v = await api("library/" + path, { name, agents: sent });
          take(v);
          if (same() && me.every && Date.now() - refused > 6000) { reportAll(v, me); continue; }
          if (same() && Date.now() - refused > 6000) report(v.result, null, sent);
        }
      } catch (e) {
        failed = true;
        refused = Date.now();
        status(e.message, "err", 6000);
      }
      writing.delete(key);
      syncProblems();
      // the page held the clicks: what magpie really has is read again
      if (failed) await api("library").then(take, () => {});
      const x = lib[list].find((y) => y.name === name);
      const fresh = x && rowOf(x).querySelector(":scope > .lib-agents");
      if (!me.box.isConnected) { if (failed) render(); } // drawn again meanwhile, from the clicks
      else if (fresh && !morphChips(me.box, fresh)) me.box.replaceWith(fresh);
    };
  }
  // What All did, said of the item: on for how many of the agents it went
  // to, and which of them couldn't be given it, and why.
  function reportAll(v, me) {
    const what = (me.list === "servers" ? "mcp:" : "skill:") + me.name;
    const shown = [...me.box.children].filter((x) => x.dataset.agent && !cannot(x)).map((x) => x.dataset.agent);
    const n = shown.filter((id) => me.want.includes(id)).length;
    const bad = (v.result?.problems || []).filter((p) => p.what === what && shown.includes(p.agent));
    if (!bad.length) {
      status(n ? t("{name} is on for all {n} agents", { name: me.name, n }) : t("{name} is off for every agent", { name: me.name }), "ok");
      return;
    }
    const p = bad[0];
    status((n ? t("{name} is on for {ok} of {n} agents", { name: me.name, ok: n - bad.length, n }) : t("{name} couldn't be taken from every agent", { name: me.name })) + " — "
      + t("{agent}: {error}", { agent: nameOf(p.agent), error: p.error }) + (bad.length > 1 ? " " + t("(and {n} more)", { n: bad.length - 1 }) : ""), "warn", 8000);
  }

  // take is the page as magpie answered it, with the rows still being
  // written kept as they were last clicked. A server or skill added,
  // removed or renamed changes what the market calls added (#300): one gone
  // from the library loses its mark at once, and the market is asked again.
  function take(v) {
    const was = lib && shelf();
    lib = v;
    for (const w of writing.values()) {
      const x = lib[w.list].find((y) => y.name === w.name);
      if (x) x.agents = w.want;
    }
    if (!was || shelf() === was) return;
    for (const [kind, list] of [["mcp", lib.servers], ["skills", lib.skills]]) {
      const m = market[kind];
      if (!m.items) continue;
      const names = new Set((list || []).map((x) => x.name));
      for (const x of m.items) if (x.have && !names.has(x.have)) x.have = "";
      drawMarket(kind);
      fetchMarket(kind);
    }
  }
  function morphChips(box, fresh) {
    const was = [...box.children], now = [...fresh.children];
    if (was.length !== now.length || was.some((c, i) => c.dataset.agent !== now[i].dataset.agent)) return false;
    was.forEach((c, i) => {
      const n = now[i];
      c.className = n.className;
      c.title = n.title;
      if (cannot(n)) c.setAttribute("aria-disabled", "true"); else c.removeAttribute("aria-disabled");
      c.setAttribute("aria-pressed", n.getAttribute("aria-pressed"));
      c.onclick = n.onclick;
    });
    return true;
  }

  // ---------- loading and changing ----------

  // quiet is a read on coming back to the window: the page is drawn again
  // only when the library changed meanwhile, so a click that brings the
  // window forward doesn't redraw what it clicked on.
  async function load(quiet) {
    if (!lib) renderLoading();
    try {
      const v = await api("library");
      const was = lib && seen(lib);
      take(v);
      if (quiet === true && seen(lib) === was) return;
      rtk = null; // asked again when its tab is drawn: an agent may have been installed since
      render();
    } catch (e) {
      status(e.message, "err");
    }
  }
  window.loadLibrary = load;
  // the library as the page shows it: what a change did is said once, not shown
  const seen = (v) => JSON.stringify({ ...v, result: undefined });

  // Every change answers with the page as it is after it, and what it did.
  async function change(path, body, done) {
    try {
      const v = await api("library/" + path, body);
      take(v);
      report(v.result, done);
      render();
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      return false;
    }
  }
  // given, for a row's agent chips, is the agents that have it now: an
  // agent written that isn't one of them had it taken out (#332).
  function report(res, done, given) {
    if (!res) return;
    // skills installed together (lc on Discord): those the library had
    // already, and those left out because another by that name is in the
    // way, are named, beside how many were installed
    const had = res.had || [], skipped = res.skipped || [];
    if (had.length || skipped.length) {
      const n = res.installed?.length || 0, parts = [];
      if (n) parts.push(n === 1 ? t("1 skill installed") : t("{n} skills installed", { n }));
      if (had.length) parts.push(t("already in the library: {names}", { names: had.join(", ") }));
      for (const p of skipped) parts.push(t("{name} skipped: {error}", { name: p.what.replace(/^skill:/, ""), error: p.error }));
      status(parts.join(" · "), skipped.length ? "warn" : "ok", skipped.length ? 12000 : 6000);
      return;
    }
    if (res.problems?.length) {
      const p = res.problems[0];
      status(t("{agent}: {error}", { agent: tilde(nameOf(p.agent)), error: p.error }) + (res.problems.length > 1 ? " " + t("(and {n} more)", { n: res.problems.length - 1 }) : ""), "warn", 8000);
      return;
    }
    const n = res.changed?.length || 0;
    const out = given ? (res.changed || []).filter((id) => !given.includes(id)).length : 0;
    if (done) status(done, "ok");
    else if (out && out === n) status(n === 1 ? t("Removed from {agent}", { agent: nameOf(res.changed[0]) }) : t("Removed from {n} agents", { n }), "ok");
    else if (out) status(t("{n} agents updated", { n }), "ok");
    else if (n) status(n === 1 ? t("Written to {agent}", { agent: nameOf(res.changed[0]) }) : t("Written to {n} agents", { n }), "ok");
    else status(t("Saved — the agents already had it"), "ok");
  }

  // What the last change couldn't write, whole: a chip's amber dot holds
  // only its own item's, so an agent's instructions, its MCP file, a
  // project or a skill that wouldn't update were said once in a toast
  // ("see Library") and shown nowhere.
  function problemsCard() {
    const list = lib.problems || [];
    if (!list.length) return null;
    const card = el("div", "lib-problems");
    card.setAttribute("role", "status");
    const ttl = el("div", "lib-problems-head");
    const again = button(t("Try again"), "action", () => change("all/sync", {}));
    again.title = t("Writes the library into every agent again");
    ttl.append(el("span", "lib-problems-dot"), el("b", "", t("Some of the Library couldn't be written")), el("span", "grow"), again);
    const ul = el("ul");
    for (const p of list) {
      const li = el("li");
      const who = p.what.startsWith("project:") ? tilde(p.agent) : p.agent ? nameOf(p.agent) : "";
      if (who) li.append(el("b", "", who), el("span", "lib-problems-sep", " · "));
      // an agent's own skill in the library's way is settled right here:
      // the library's in its place (the agent's kept aside), or the agent's
      const own = p.own && p.what.startsWith("skill:") && p.agent;
      const name = own && p.what.slice("skill:".length);
      li.append(el("span", "", problemWhat(p.what)), el("span", "lib-problems-err", own ? t("{agent} has a skill of its own called {name}, not the same as the library's", { agent: who, name }) : p.error));
      if (own) {
        const fix = el("div", "lib-problems-fix");
        const use = button(t("Use the library's"), "action", () => change("skills/use-library", { name, agent: p.agent }, t("{agent} has the library's {name} now; its own is kept with the backups", { agent: who, name })));
        use.title = t("Sets {agent}'s own {name} aside with the backups and gives it the library's in its place", { agent: who, name });
        const keep = button(t("Keep {agent}'s", { agent: who }), "action", () => change("skills/keep-own", { name, agent: p.agent }, t("{agent} keeps its own {name}", { agent: who, name })));
        keep.title = t("{agent} keeps its own {name}, and the library no longer gives it this skill", { agent: who, name });
        fix.append(use, keep);
        li.append(fix);
      }
      ul.append(li);
    }
    card.append(ttl, ul);
    return card;
  }
  function problemWhat(what) {
    const [kind, ...rest] = what.split(":");
    const name = rest.join(":");
    if (kind === "instructions") return t("Instructions");
    if (kind === "mcp") return name ? t("MCP server {name}", { name }) : t("MCP servers");
    if (kind === "skill") return t("Skill {name}", { name });
    if (kind === "project") return name ? t("Skill {name}", { name }) : t("The project");
    return what;
  }
  // a chip's write changes the list without drawing the page again: the
  // card is put right in place (the chip clicked is held where it is on the
  // screen by app.js, as for any click)
  function syncProblems() {
    const was = page.querySelector(":scope > .lib-problems");
    const card = problemsCard();
    if (!was && !card) return;
    if (was && card && was.textContent === card.textContent) return;
    if (was && card) was.replaceWith(card);
    else if (was) was.remove();
    else page.querySelector(":scope > .lib-head")?.after(card);
  }

  // Until the library comes, the page is drawn as it will be: the real tabs
  // (one can be picked already) and, for the tab open, its rows in outline,
  // so nothing moves when they're filled in. The outline shows only if the
  // wait is long enough to see it.
  function renderLoading() {
    page.replaceChildren();
    shown = "";
    page.append(libHead(null), skeleton(tab));
  }

  function skeleton(which) {
    const box = el("div", "lib-body lib-skel");
    box.setAttribute("aria-busy", "true");
    const bar = (w, h, cls = "") => {
      const b = el("span", "skeleton " + cls);
      b.style.cssText = `width:${w};height:${h}px`;
      return b;
    };
    // name and path lengths vary, as they will
    const widths = [[34, 46], [22, 40], [30, 52], [26, 38], [38, 44], [20, 36], [28, 48]];
    const rows = (n, sw) => {
      const list = el("div", "list lib-list");
      for (let i = 0; i < n; i++) {
        const [a, b] = widths[i % widths.length];
        const row = el("div", "row lib-row");
        const who = el("div", "who");
        who.append(bar(a + "%", 10), bar(b + "%", 8));
        row.append(bar("26px", 26, "lib-sk-icon"), who, el("span", "grow"));
        if (sw) row.append(bar("34px", 20, "lib-sk-switch"));
        list.append(row);
      }
      return list;
    };
    const head = (w) => {
      const rh = el("div", "row-head");
      rh.append(bar(w, 8));
      return rh;
    };
    const intro = el("div", "lib-intro lib-sk-intro");
    intro.append(bar("min(520px, 80%)", 9));
    box.append(intro);
    if (which === "instructions") {
      const card = el("div", "list lib-list");
      const ch = el("div", "row lib-row");
      ch.append(bar("26px", 26, "lib-sk-icon"), bar("150px", 11));
      card.append(ch, rows(1, false).firstChild);
      box.append(card, head("60px"), rows(6, true));
    } else if (which === "rtk") {
      box.append(rows(1, false), head("60px"), rows(5, true));
    } else {
      box.append(rows(5, true));
    }
    return box;
  }

  // the tabs and the Library folder; without the library yet (lib is null
  // while it loads) the tabs have no counts and the folder waits
  function libHead(counts) {
    const head = el("div", "lib-head");
    const n = (k) => (counts?.[k] ? " · " + counts[k] : "");
    const tabs = segs([
      ["instructions", t("Instructions")],
      ["mcp", t("MCP servers") + n("mcp")],
      ["skills", t("Skills") + n("skills")],
      ["rtk", "RTK"],
    ], tab, (id) => {
      tab = id;
      try { localStorage.setItem("magpie.libTab", id); } catch {}
      // RTK's count read afresh each time its tab is opened: an agent run
      // since adds to it (#741); the page keeps the last one until then
      if (id === "rtk" && rtk) loadRTK();
      if (!lib) return page.querySelector(":scope > .lib-skel")?.replaceWith(skeleton(id));
      render(); syncLists();
    });
    tabs.classList.add("lib-tabs");
    head.append(tabs, el("span", "grow"));
    const more = button("", "lib-more", () => lib && reveal(lib.dir));
    more.append(glyph(GLYPH.folder, "lib-mini"), el("span", "", t("Library folder")));
    if (lib) more.title = tilde(lib.dir);
    else more.disabled = true;
    head.append(more);
    return head;
  }

  // ---------- the page ----------

  function render() {
    if (!lib) return;
    const top = page.scrollTop;
    const focus = document.activeElement?.dataset?.lib; // keep the caret where the reader is typing
    const caret = focus ? [document.activeElement.selectionStart, document.activeElement.selectionEnd] : null;
    page.replaceChildren();
    fits = [];
    const head = libHead({ mcp: lib.servers.length, skills: lib.skills.length });
    page.append(head);
    head.classList.toggle("stuck", top > 0);
    if (!lib.agents.length) {
      page.append(empty(t("No agents to write to"), t("Install Claude Code, Codex, Gemini CLI, OpenCode… and the library can give them the same instructions, MCP servers and skills.")));
      return;
    }
    // what changed in a tab is drawn in place: only another tab (or the
    // first one after the skeleton) fades in
    const problems = problemsCard();
    if (problems) page.append(problems);
    const body = el("div", "lib-body" + (shown !== tab ? " enter" : ""));
    shown = tab;
    if (tab === "instructions") renderInstructions(body);
    else if (tab === "mcp") renderServers(body);
    else if (tab === "rtk") renderRTK(body);
    else renderSkills(body);
    page.append(body);
    for (const n of body.querySelectorAll(".row-head .note")) n.title = n.textContent;
    const foot = el("p", "lib-foot");
    foot.append(el("span", "", t("magpie keeps a copy of every file before it writes it.") + " "));
    const b = el("button", "lib-link", t("Backups"));
    b.onclick = () => reveal(lib.backups);
    foot.append(b);
    page.append(foot);
    for (const f of fits.splice(0)) f();
    page.scrollTop = top;
    syncLists(); // a long list's rows where the view is, before it's painted
    if (focus) {
      const e = page.querySelector(`[data-lib="${CSS.escape(focus)}"]`);
      if (e) { e.focus({ preventScroll: true }); if (caret && e.setSelectionRange) e.setSelectionRange(caret[0], caret[1]); }
    }
  }

  function empty(title, text, action) {
    const box = el("div", "lib-empty");
    box.append(el("b", "", title), el("p", "", text));
    if (action) box.append(action);
    return box;
  }

  function intro(text) { return el("p", "lib-intro", text); }

  // ---------- RTK ----------

  // RTK (rtk-ai.app) cuts down what the shell commands an agent runs print,
  // so their output costs fewer tokens. magpie runs rtk's own installer for
  // each agent switched on, takes out what it wrote for one switched off
  // (with or without rtk), and reads the agents' files for which have it.
  let rtk = null;          // /api/library/rtk
  const rtkBusy = new Set(); // agents being switched
  let rtkLoading = false;
  async function loadRTK() {
    if (rtkLoading) return;
    rtkLoading = true;
    try {
      rtk = await api("library/rtk");
    } catch (e) {
      status(e.message, "err");
    }
    rtkLoading = false;
    if (tab === "rtk" && rtk) render();
  }
  function renderRTK(body) {
    body.append(intro(t("RTK rewrites the shell commands an agent runs — git status, cargo test, ls… — to print only what the model needs, so they cost fewer tokens. Switch it on for an agent and magpie runs RTK's own installer for it.")));
    if (!rtk) {
      body.append(el("div", "row skeleton"));
      loadRTK();
      return;
    }
    const card = el("div", "list lib-card");
    const ttl = el("div", "lib-cardhead");
    // RTK's own mark (rtk-ai.app), drawn in the text's colour
    const logo = el("span", "lib-glyph lib-rtk-logo");
    const m = el("span", "mask");
    m.style.setProperty("--i", "url(icons/rtk.png)");
    logo.append(m);
    ttl.append(logo, el("b", "", "RTK"), el("span", "grow"));
    if (rtk.path) {
      ttl.append(el("span", "note mono", rtk.version ? "v" + rtk.version : tilde(rtk.path)));
      // a newer release: upgraded the way this rtk was installed
      // …unless the package manager that installed it (winget, Homebrew)
      // doesn't have it yet: an upgrade through it would change nothing
      // (#1025), so none is offered until it does
      const waiting = !rtkUpgrading && rtk.waiting;
      const behind = !waiting && rtk.latest && rtk.version && vNewer(rtk.latest, rtk.version);
      if (rtkUpgrading) ttl.append(tag(t("Upgrading…"), "lib-new"));
      else if (waiting) ttl.append(tag(t("v{v} is out · waiting for {via}", { v: rtk.latest, via: rtk.waiting }), "", t("RTK {v} is out, but {via} has {has} so far — usually within a few days. Upgrade comes back once {via} has it.", { v: rtk.latest, via: rtk.waiting, has: rtk.waitingHas || rtk.version })));
      else if (behind) ttl.append(tag(t("v{v} is out", { v: rtk.latest }), "lib-new", t("RTK {v} is the latest release; this one is {have}", { v: rtk.latest, have: rtk.version })));
      else if (rtk.latest && rtk.version) ttl.append(tag(t("Up to date"), "", t("RTK {v} is the latest release", { v: rtk.latest })));
      if (behind || rtkUpgrading) {
        const ub = button(rtkUpgrading ? t("Upgrading…") : t("Upgrade"), "action", upgradeRTK);
        ub.disabled = rtkUpgrading || !rtk.upgrade;
        ub.title = rtk.upgrade ? t("Runs {cmd}", { cmd: rtk.upgrade }) : t("magpie can't tell how this RTK was installed — update it the way you installed it");
        ttl.append(ub);
      }
      card.append(ttl);
      // an upgrade under way says what it runs; one that failed says why,
      // with the tool's exit code and what it said, until the next try (#741)
      if (rtkUpgrading && rtk.upgrade) card.append(el("p", "lib-rtk-gain", t("Running {cmd} — this can take a few minutes.", { cmd: rtk.upgrade })));
      else if (rtkUpgradeErr) card.append(el("p", "lib-rtk-note lib-rtk-err", t("The upgrade failed: {why}", { why: rtkUpgradeErr })));
      if (rtk.note) card.append(el("p", "lib-rtk-note", rtk.note));
      // found where magpie looks, but not on the PATH the agents get: their
      // hooks run rtk by name, so it does nothing for them (#601)
      if (rtk.offPath) {
        const dir = tilde(rtk.path.replace(/[\\/][^\\/]*$/, ""));
        card.append(el("p", "lib-rtk-note lib-rtk-offpath", t("RTK is in {dir}, which isn't on your PATH. Agents run rtk by name, so they can't find it and RTK does nothing for them (Pi says \"rtk binary not found in PATH\").", { dir })));
        card.append(el("p", "lib-rtk-cmd", !rtk.pathDir
          ? t("Add {dir} to PATH in your shell profile, then check again.", { dir })
          : rtk.pathLink
            ? t("Put RTK on PATH links it into {dir}.", { dir: tilde(rtk.pathDir) })
            : t("Put RTK on PATH adds {dir} to your user PATH.", { dir: tilde(rtk.pathDir) })));
        const acts = el("div", "lib-acts");
        if (rtk.pathDir) {
          const pb = button(rtkPathing ? t("Putting RTK on PATH…") : t("Put RTK on PATH"), "action", pathRTK);
          pb.disabled = rtkPathing;
          acts.append(pb);
        }
        acts.append(button(t("Check again"), rtk.pathDir ? "" : "action", () => { rtk = null; render(); }));
        card.append(acts);
      }
      // RTK's own count (rtk gain): every agent and terminal, all time —
      // said on the page, so an empty one isn't read as magpie counting
      // only some (#741); one rtk couldn't give says why
      const g = rtk.gain;
      const gain = el("p", "lib-rtk-gain", g
        ? t("{saved} tokens saved over {n} commands — {pct}% on average", { saved: tokens(g.saved), n: g.commands.toLocaleString(), pct: Math.round(g.pct) })
        : rtk.gainErr
          ? t("magpie couldn't read what RTK saved: {why}", { why: rtk.gainErr })
          : t("Nothing saved yet: the agents' commands go through RTK once it's switched on and the agent is restarted."));
      gain.append(" ", el("span", "lib-rtk-scope", t("RTK's own count (rtk gain): every command run through RTK on this computer, from any agent or terminal.")));
      card.append(gain);
      // Codex's Windows sandbox: rtk runs there as another account, or one
      // that can't write RTK's history, so what it saves isn't in that count
      if (rtk.codexSandbox) card.append(el("p", "lib-rtk-note lib-rtk-sandbox", rtk.codexSandbox === "elevated"
        ? t("Codex runs its commands in its Windows sandbox, as its own sandbox account: RTK keeps what it saves there in that account's history, not yours, so it isn't counted here.")
        : t("Codex runs its commands in its Windows sandbox, which can't write RTK's history: what RTK saves there isn't recorded, so it isn't counted here.")));
      if (rtk.days?.length) card.append(rtkChart(rtk.days));
    } else {
      card.append(ttl);
      card.append(el("p", "lib-rtk-gain", rtkInstalling
        ? t("Installing RTK… this can take a minute.")
        : t("RTK isn't installed. magpie can install it for you, or get it from its site.")));
      if (rtk.install) card.append(el("p", "lib-rtk-cmd", rtk.install));
      const acts = el("div", "lib-acts");
      if (rtk.install) {
        const ib = button(rtkInstalling ? t("Installing…") : t("Install RTK"), "action", installRTK);
        ib.disabled = rtkInstalling;
        acts.append(ib);
      }
      acts.append(button(t("Get RTK"), rtk.install ? "" : "action", () => browse(rtk.url)), button(t("Check again"), "", () => { rtk = null; render(); }));
      card.append(acts);
    }
    body.append(card);
    const rh = el("div", "row-head");
    rh.append(el("span", "label", t("Agents")), el("span", "grow"), el("span", "note", t("restart an agent after switching it")));
    body.append(rh);
    const list = el("div", "list lib-list");
    const rows = rtk.agents.filter((a) => !isHidden(a));
    for (const a of rows) {
      const row = el("div", "row lib-row");
      const who = el("div", "who");
      who.append(el("div", "name", a.name));
      row.append(icon(a.icon), who, el("span", "grow"));
      // its hook calls an rtk that isn't there: switching it off still works
      if (a.on && !rtk.path) row.append(tag(t("RTK missing"), "warn", t("{agent}'s hook calls rtk, which isn't installed, so its shell commands fail. Install RTK, or switch this off.", { agent: a.name })));
      // …or one that is, off the PATH the agent gets
      else if (a.on && rtk.offPath) row.append(tag(t("Not on PATH"), "warn", t("{agent}'s hook runs rtk by name and can't find it, so RTK does nothing for it. Put RTK on PATH above.", { agent: a.name })));
      // OpenCode 2 won't load rtk's plugin (written for OpenCode 1): it can't
      // be switched on, and one already there can be switched off
      // an agent RTK has no hook for (DeepSeek Harness): listed, with why
      if (a.noHook) row.append(tag(t("No RTK hook"), "warn", t("RTK can't be given to {agent} yet: RTK works by rewriting the shell command an agent is about to run, and {agent}'s hooks can only allow or deny a command, not change it. RTK's installer has no option for it either (github.com/rtk-ai/rtk/issues/3847).", { agent: a.name })));
      else if (a.blocked && a.id !== "opencode") row.append(tag(t("Update RTK"), "warn", t("{agent}'s hook needs RTK 0.50 or newer: older ones only add @RTK.md to AGENTS.md, which rewrites no command. Update RTK (brew upgrade rtk, or its installer again), then switch it on.", { agent: a.name })));
      else if (a.blocked) row.append(tag(t("Not for OpenCode 2"), "warn", a.on
        ? t("RTK's plugin is written for OpenCode 1, and OpenCode 2 refuses to load it. Switch this off until RTK supports OpenCode 2.")
        : t("RTK's plugin is written for OpenCode 1, and OpenCode 2 refuses to load it. It can be switched on once RTK supports OpenCode 2.")));
      const sw = toggle(a.on, t("{agent} runs its commands through RTK", { agent: a.name }), (on) => setRTK(a, on));
      if ((!rtk.path && !a.on) || (a.blocked && !a.on) || rtkBusy.has(a.id) || rtkInstalling || rtkUpgrading) sw.disabled = true;
      row.append(sw);
      list.append(row);
    }
    if (!rows.length) list.append(el("div", "lib-none", t("None of your agents is one RTK has a hook for.")));
    body.append(list);
  }
  async function setRTK(a, on) {
    rtkBusy.add(a.id);
    render();
    try {
      rtk = await api("library/rtk", { agent: a.id, on });
      const names = (rtk.restart || [a.id]).map((id) => rtk.agents.find((x) => x.id === id)?.name || id).join(", ");
      status(on ? t("RTK is on for {agent} — restart it to use it", { agent: names }) : t("RTK is off for {agent} — restart it to see it", { agent: names }), "ok", 6000);
    } catch (e) {
      status(e.message, "err", 8000);
    }
    rtkBusy.delete(a.id);
    render();
  }
  // what rtk saved day by day (week by week past 92 days), over the last
  // 30 or 90 days or since it first ran: each bar the commands' whole
  // output, the part RTK kept from the model on top of what still went
  let rtkRange = 0; // days; 0 all
  try { rtkRange = +(localStorage.getItem("magpie.rtkRange") ?? 0) || 0; } catch {}
  const isoDay = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  const dayOf = (s) => { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d); };
  function rtkChart(days) {
    const chart = el("div", "chart lib-rtk-chart");
    const today = new Date(); today.setHours(0, 0, 0, 0);
    let first = dayOf(days[0].date);
    if (rtkRange) { first = new Date(today); first.setDate(first.getDate() - rtkRange + 1); }
    if (first > today) first = new Date(today);
    const n = Math.round((today - first) / 864e5) + 1;
    const step = n > 92 ? 7 : 1;
    const buckets = [], at = new Map();
    for (let d = new Date(first); d <= today; d.setDate(d.getDate() + step)) {
      const b = { day: new Date(d), commands: 0, input: 0, saved: 0 };
      for (let i = 0; i < step; i++) { const x = new Date(d); x.setDate(x.getDate() + i); at.set(isoDay(x), b); }
      buckets.push(b);
    }
    const sum = { commands: 0, input: 0, saved: 0 };
    for (const r of days) {
      const b = at.get(r.date);
      if (!b) continue;
      b.commands += r.commands; b.input += r.input; b.saved += r.saved;
      sum.commands += r.commands; sum.input += r.input; sum.saved += r.saved;
    }
    const peak = Math.max(1, ...buckets.map((b) => b.input));
    const head = el("div", "sess-chart-head");
    const seg = segs([[30, t("30 days")], [90, t("90 days")], [0, t("All")]], rtkRange, (r) => {
      rtkRange = r;
      try { localStorage.setItem("magpie.rtkRange", String(r)); } catch {}
      chart.replaceWith(rtkChart(days));
    });
    head.append(el("span", "label", t(step === 7 ? "Saved by week" : "Saved by day")), seg, el("span", "grow"), el("span", "peak", tokens(peak)));
    const bars = el("div", "bars"), labels = el("div", "labels");
    const k = buckets.length;
    const every = k <= 8 ? 1 : k <= 31 ? Math.ceil(k / 6) : Math.ceil(k / 5);
    const short = (d) => `${d.getMonth() + 1}/${d.getDate()}`;
    buckets.forEach((b, i) => {
      const bar = el("div", "bar");
      const saved = el("i", "out"), kept = el("i", "in");
      saved.style.height = (100 * b.saved / peak).toFixed(1) + "%";
      kept.style.height = (100 * Math.max(0, b.input - b.saved) / peak).toFixed(1) + "%";
      bar.append(saved, kept);
      const when = step === 7 ? t("week of {label}", { label: short(b.day) }) : short(b.day);
      bar.title = b.commands
        ? t("{when} · {saved} tokens saved over {n} commands — {pct}%", { when, saved: tokens(b.saved), n: b.commands.toLocaleString(), pct: b.input ? Math.round(100 * b.saved / b.input) : 0 })
        : t("{when} · nothing", { when });
      bars.append(bar);
      const end = i === k - 1 && (k - 1) % every >= every / 2;
      labels.append(el("span", "", i % every === 0 || end ? short(b.day) : ""));
    });
    const foot = el("div", "lib-rtk-foot");
    const key = (cls, text) => { const x = el("span", "lib-rtk-key"); x.append(el("i", cls), el("span", "", text)); return x; };
    foot.append(key("out", t("saved")), key("in", t("still sent")), el("span", "grow"),
      el("span", "", sum.commands
        ? t("{saved} tokens saved over {n} commands — {pct}%", { saved: tokens(sum.saved), n: sum.commands.toLocaleString(), pct: sum.input ? Math.round(100 * sum.saved / sum.input) : 0 })
        : t("Nothing run through RTK in this time")));
    chart.append(head, bars, labels, foot);
    return chart;
  }
  // vNewer: version a is after b
  function vNewer(a, b) {
    const x = a.split(/[.-]/).map((n) => parseInt(n, 10) || 0), y = b.split(/[.-]/).map((n) => parseInt(n, 10) || 0);
    for (let i = 0; i < 3; i++) if ((x[i] || 0) !== (y[i] || 0)) return (x[i] || 0) > (y[i] || 0);
    return false;
  }
  let rtkUpgrading = false;
  let rtkUpgradeErr = ""; // why the last upgrade failed, shown on the card
  async function upgradeRTK() {
    rtkUpgrading = true;
    rtkUpgradeErr = "";
    render();
    let failed = false;
    try {
      rtk = await api("library/rtk/upgrade", {});
      if (rtk.note) status(rtk.note, "", 10000);
      else status(t("RTK is now {v}", { v: rtk.version }), "ok", 6000);
    } catch (e) {
      failed = true;
      rtkUpgradeErr = e.message;
      status(t("RTK's upgrade failed — the RTK card says why"), "err", 10000);
    }
    rtkUpgrading = false;
    render();
    // the version it left, whatever the installer said
    if (failed) loadRTK();
  }
  let rtkPathing = false;
  async function pathRTK() {
    rtkPathing = true;
    render();
    try {
      rtk = await api("library/rtk/path", {});
      status(rtk.restart?.length ? t("RTK is on your PATH — restart your agents, and the terminals they run in, to use it") : t("RTK is on your PATH"), "ok", 6000);
    } catch (e) {
      status(e.message, "err", 10000);
    }
    rtkPathing = false;
    render();
  }
  let rtkInstalling = false;
  async function installRTK() {
    rtkInstalling = true;
    render();
    try {
      rtk = await api("library/rtk/install", {});
      status(t("RTK is installed — switch it on for your agents below"), "ok", 6000);
    } catch (e) {
      status(e.message, "err", 10000);
    }
    rtkInstalling = false;
    render();
  }
  function tokens(n) {
    if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
    return String(n);
  }

  // ---------- instructions ----------

  function autosize(ta, min) {
    const fit = () => { ta.style.height = "auto"; ta.style.height = Math.max(min, ta.scrollHeight + 2) + "px"; };
    ta.addEventListener("input", fit);
    // fitted once it's on the page, before it's painted, so what's under it
    // doesn't move a frame later
    fits.push(fit);
    requestAnimationFrame(fit); // and one drawn outside render()
  }

  function iv() { return lib.instructions; }
  const setOf = (id) => lib.instructions.sets.find((x) => x.id === id);
  const setName = (x) => x.name || t("Default");
  function unsavedTexts() {
    const out = {};
    for (const [id, v] of Object.entries(texts)) if (setOf(id) && v !== setOf(id).text) out[id] = v;
    return out;
  }
  function dirty() {
    if (Object.keys(unsavedTexts()).length) return true;
    return Object.entries(extras).some(([id, v]) => v !== (lib.instructions.agents.find((a) => a.agent === id)?.extra || ""));
  }

  async function saveInstructions() {
    const c = {};
    const tx = unsavedTexts();
    if (Object.keys(tx).length) c.texts = tx;
    const ex = {};
    for (const [id, v] of Object.entries(extras)) if (v !== (lib.instructions.agents.find((a) => a.agent === id)?.extra || "")) ex[id] = v;
    if (Object.keys(ex).length) c.extra = ex;
    if (!Object.keys(c).length) return;
    if (await change("instructions/save", c)) {
      for (const k of Object.keys(texts)) delete texts[k];
      for (const k of Object.keys(extras)) delete extras[k];
      render();
    }
  }
  async function discard(force = false) {
    if (force !== true && dirty() && !(await confirmDiscard())) return;
    for (const k of Object.keys(texts)) delete texts[k];
    for (const k of Object.keys(extras)) delete extras[k];
    render();
  }

  function renderInstructions(body) {
    const iv = lib.instructions;
    body.append(intro(t("Write it once: every agent switched on below reads it before each conversation, in its own instructions file.")));
    // the sets of shared instructions: one is on, the one the agents read;
    // a set opens to be edited (#106)
    const card = el("div", "list lib-list lib-sets");
    const ttl = el("div", "lib-cardhead");
    ttl.append(glyph(GLYPH.doc), el("b", "", t("Shared instructions")), el("span", "grow"), el("span", "note", "Markdown · " + (/^Mac/.test(navigator.platform) ? "⌘S" : "Ctrl+S")));
    card.append(ttl);
    for (const x of iv.sets) card.append(...setRow(x));
    if (addingSet) card.append(newSetRow());
    else {
      const add = el("div", "lib-acts lib-setadd");
      add.append(button("+ " + t("New set"), "action", () => { addingSet = true; render(); }));
      card.append(add);
    }
    const bar = el("div", "lib-savebar");
    bar.append(el("span", "note", t("Not saved yet")), el("span", "grow"), button(t("Discard"), "", discard), button(t("Save"), "primary", saveInstructions));
    bar.hidden = !dirty();
    body.append(card);

    const rh = el("div", "row-head");
    // which set the agents switched on read, said where they're switched
    // on (Fate: a set made and written was read by none, the Default being
    // the one in use)
    const inUse = (iv.sets || []).find((x) => x.active) || (iv.sets || [])[0];
    rh.append(el("span", "label", t("Agents")));
    if (inUse) rh.append(el("span", "note lib-reads", t("They read {name}", { name: setName(inUse) }) + ((inUse.text || "").trim() ? "" : " · " + t("Empty"))));
    rh.append(el("span", "grow"), el("span", "note", t("magpie writes its part between two marker lines — the rest of each file stays yours")));
    body.append(rh);
    const list = el("div", "list lib-list");
    // a hidden agent still reading them is listed, to switch it off (#475)
    const rows = iv.agents.filter((a) => !isHidden({ id: a.agent }) || a.on);
    for (const a of rows) list.append(...instructionsRow(a));
    if (!rows.length) list.append(el("div", "lib-none", t("None of your agents reads a user-wide instructions file magpie knows.")));
    body.append(list);
    const skip = shownAgents().filter((a) => !a.instructions);
    if (skip.length) body.append(el("p", "lib-aside", t(skip.length > 1 ? "{agents} keep no user-wide instructions file." : "{agents} keeps no user-wide instructions file.", { agents: skip.map((a) => a.name).join(", ") })));
    body.append(bar);
  }

  function setRow(x) {
    const isOpen = openSet === x.id;
    const text = texts[x.id] ?? x.text;
    const row = el("div", "row lib-row click lib-set" + (isOpen ? " open" : "") + (x.active ? " on" : ""));
    const pick = el("button", "lib-radio" + (x.active ? " on" : ""));
    pick.setAttribute("role", "radio");
    pick.setAttribute("aria-checked", x.active ? "true" : "false");
    pick.title = x.active ? t("The agents switched on below read this set") : t("Switch the agents to this set");
    pick.onclick = (e) => {
      e.stopPropagation();
      if (x.active) return;
      const c = { activate: x.id };
      const tx = unsavedTexts();
      if (Object.keys(tx).length) c.texts = tx;
      change("instructions/save", c, t("The agents read {name} now", { name: setName(x) })).then((ok) => { if (ok) for (const k of Object.keys(tx)) delete texts[k]; });
    };
    const who = el("div", "who");
    who.append(el("div", "name", setName(x)));
    const first = text.split("\n").find((l) => l.trim()) || "";
    who.append(el("div", "sub", first ? first.replace(/^#+\s*/, "") : t("Empty")));
    const tags = el("div", "lib-tags");
    if (x.active) tags.append(tag(t("In use"), "", t("The agents switched on below read this set")));
    if (text !== x.text) tags.append(tag(t("Not saved yet"), "warn"));
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(pick, who, tags, chev);
    row.onclick = () => { openSet = isOpen ? null : x.id; removingSet = null; render(); };
    if (!isOpen) return [row];

    const det = el("div", "lib-detail");
    const nm = input(setName(x), t("Name"));
    nm.dataset.lib = "setname:" + x.id;
    nm.className = "lib-setname";
    const rename = () => {
      const v = nm.value.trim();
      if (!v || v === setName(x)) { nm.value = setName(x); return; }
      change("instructions/save", { rename: { id: x.id, name: v } }, t("Renamed to {name}", { name: v }));
    };
    nm.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") nm.blur(); else if (e.key === "Escape") { nm.value = setName(x); nm.blur(); } };
    nm.onblur = rename;
    const ta = el("textarea", "lib-text small");
    ta.dataset.lib = "set:" + x.id;
    ta.spellcheck = false;
    ta.value = text;
    ta.placeholder = t("What every agent should know — how you like code written, the stack you work in, the commands that run your tests…");
    ta.oninput = () => { texts[x.id] = ta.value; renderSaveBars(); };
    ta.onkeydown = (e) => { e.stopPropagation(); if ((e.metaKey || e.ctrlKey) && e.key === "s") { e.preventDefault(); saveInstructions(); } };
    autosize(ta, 150);
    det.append(nm, ta);
    const acts = el("div", "lib-acts");
    if (!x.active) acts.append(button(t("Switch the agents to this set"), "action lib-use", () => pick.onclick({ stopPropagation() {} })));
    if (x.id !== "default") {
      // its text goes with it, so a second click says so and does it
      const sure = removingSet === x.id;
      const rm = button(sure ? t("Remove {name} and its text", { name: setName(x) }) : t("Remove"), sure ? "primary danger-fill" : "action danger", () => {
        if (!sure) { removingSet = x.id; render(); return; }
        removingSet = null;
        delete texts[x.id];
        openSet = null;
        change("instructions/save", { remove: x.id }, t("{name} removed", { name: setName(x) }));
      });
      if (x.active) { rm.disabled = true; rm.title = t("Switch the agents to another set before removing this one"); }
      acts.append(rm);
      if (sure) acts.append(button(t("Cancel"), "", () => { removingSet = null; render(); }));
    }
    if (acts.childElementCount) det.append(acts);
    det.onclick = (e) => e.stopPropagation();
    return [row, det];
  }

  function newSetRow() {
    const row = el("div", "row lib-row lib-set");
    const nm = input("", t("Name, e.g. Work"));
    nm.dataset.lib = "newset";
    nm.className = "lib-setname grow";
    const create = async () => {
      const name = nm.value.trim();
      if (!name) return;
      const id = "s" + Date.now().toString(36);
      // the set in use empty, the new one is read from then on (as the
      // library does it)
      const inUse = (iv().sets || []).find((x) => x.active);
      const said = !inUse || (inUse.text || "").trim() ? t("{name} added — switch the agents to it when it's ready", { name }) : t("{name} added — the agents read it", { name });
      if (await change("instructions/save", { create: { id, name } }, said)) {
        addingSet = false;
        openSet = id;
        render();
      }
    };
    const cancel = () => { addingSet = false; render(); };
    nm.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") create(); else if (e.key === "Escape") cancel(); };
    queueMicrotask(() => nm.focus());
    row.append(nm, button(t("Cancel"), "", cancel), button(t("Add"), "primary", create));
    return row;
  }

  function instructionsRow(a) {
    const isOpen = open.has(a.agent);
    const row = el("div", "row lib-row click" + (isOpen ? " open" : ""));
    const who = el("div", "who");
    const name = el("div", "name", a.name);
    who.append(name);
    const sub = el("div", "sub");
    sub.append(pathLink(a.path));
    who.append(sub);
    const tags = el("div", "lib-tags");
    const extra = extras[a.agent] ?? a.extra;
    if (isHidden({ id: a.agent })) tags.append(tag(t("Hidden"), "", t("Hidden on the Agents page: switch it off here to take the shared instructions out of its file")));
    if (a.on && extra) tags.append(tag(t("+ its own"), "", t("{agent} gets something of its own after the shared text", { agent: a.name })));
    if (a.edited) tags.append(tag(t("edited in the file"), "warn", t("magpie's part of this file was changed there; magpie leaves it until the library's text changes")));
    if (a.own) tags.append(tag(a.own === 1 ? t("1 line of its own") : t("{n} lines of its own", { n: a.own }), "", t("The file has instructions besides magpie's part — they stay, and the agent reads both")));
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(icon(a.icon), who, tags, toggle(a.on, t("{agent} reads the shared instructions", { agent: a.name }), (on) => {
      const agents = lib.instructions.agents.filter((x) => (x.agent === a.agent ? on : x.on)).map((x) => x.agent);
      let done = t("Taken out of {agent}'s file", { agent: a.name });
      if (on && dirty()) done = t("{agent} is on — save to write the new text into its file", { agent: a.name });
      else if (on && !iv().shared.trim() && !(a.extra || "").trim()) {
        const sets = iv().sets || [], inUse = sets.find((x) => x.active) || sets[0];
        done = inUse && sets.some((x) => (x.text || "").trim())
          ? t("{agent} is on, but it reads {name}, which is empty — switch the agents to the set they should read", { agent: a.name, name: setName(inUse) })
          : t("{agent} is on — it gets the shared instructions once there are some", { agent: a.name });
      }
      else if (on) done = t("{agent} reads the shared instructions now", { agent: a.name });
      change("instructions/save", { agents }, done);
    }), chev);
    row.onclick = () => { if (isOpen) open.delete(a.agent); else open.add(a.agent); render(); };
    if (!isOpen) return [row];

    const det = el("div", "lib-detail");
    if (a.override) {
      const w = el("div", "lib-warn");
      w.append(el("span", "", t("{agent} reads {file} instead of this file while it has anything in it.", { agent: a.name, file: tilde(a.override) }) + " "), pathLink(a.override));
      det.append(w);
    }
    if (a.agent === "opencode" || a.agent === "mimocode") det.append(el("p", "lib-aside", t("{agent} reads Claude Code's CLAUDE.md when it has no AGENTS.md of its own.", { agent: a.name })));
    if (a.agent === "agy") det.append(el("p", "lib-aside", t("Antigravity reads Gemini CLI's ~/.gemini/GEMINI.md as well as this file, so text given to both is read twice.")));
    const lab = el("label", "lib-lab", t("Only for {agent}, after the shared text", { agent: a.name }));
    const ta = el("textarea", "lib-text small");
    ta.dataset.lib = "extra:" + a.agent;
    ta.spellcheck = false;
    ta.value = extra;
    ta.placeholder = t("Anything only {agent} should be told", { agent: a.name });
    ta.oninput = () => { extras[a.agent] = ta.value; renderSaveBars(); };
    ta.onkeydown = (e) => { e.stopPropagation(); if ((e.metaKey || e.ctrlKey) && e.key === "s") { e.preventDefault(); saveInstructions(); } };
    autosize(ta, 64);
    det.append(lab, ta);
    if (!a.on && extra) det.append(el("p", "lib-aside", t("Switch {agent} on for it to read this.", { agent: a.name })));
    const acts = el("div", "lib-acts");
    if (a.own) {
      const b = button(t("Move its own into the shared text"), "action", () => change("instructions/import", { name: a.agent }, t("Moved into the shared instructions — {agent} reads the same as before", { agent: a.name })));
      b.title = t("Takes the {n} lines out of the file, adds them to the shared text, and switches {agent} on — so it reads what it read before, and the other agents can too", { n: a.own, agent: a.name });
      acts.append(b);
    }
    if (a.edited) {
      const b = button(t("Write the library's text again"), "action", () => change("instructions/save", { rewrite: [a.agent] }, t("{agent}'s file has the library's text again", { agent: a.name })));
      b.title = t("Replaces what was changed in magpie's part of the file; the file is kept aside first");
      acts.append(b);
    }
    if (acts.childElementCount) det.append(acts);
    det.onclick = (e) => e.stopPropagation();
    return [row, det];
  }

  function renderSaveBars() {
    const bar = page.querySelector(".lib-savebar");
    if (bar) bar.hidden = !dirty();
  }

  function tag(text, kind, title) {
    const s = el("span", "lib-tag " + (kind || ""), text);
    if (title) s.title = title;
    return s;
  }

  // ---------- MCP servers ----------

  function serverLine(s) {
    if (s.transport === "stdio") return [s.command, ...(s.args || [])].map(quote).join(" ");
    return s.url;
  }
  function quote(a) { return /^[\w@%+=:,./-]+$/.test(a) ? a : "'" + a.replace(/'/g, `'\\''`) + "'"; }

  function sseBlocked(s) {
    return (a) => {
      if (remote(s) && a.noRemote) return t("{agent} runs only a command from its settings — add a remote server in its Connectors instead", { agent: a.name });
      return s.transport === "sse" && a.noSSE ? t("{agent} can't reach a server over SSE — only a command or streamable HTTP", { agent: a.name }) : "";
    };
  }
  // reaches(s) says whether an agent can be given the server
  const remote = (s) => s.transport === "http" || s.transport === "sse";
  const reaches = (s) => (a) => !a || (!(remote(s) && a.noRemote) && !(s.transport === "sse" && a.noSSE));

  function renderServers(body) {
    body.append(intro(t("Add a server once and switch it on for the agents that should have it — magpie writes it into each one's config in the shape that agent reads.")));
    const all = mcpAgents();
    pickingFor("servers", lib.servers);
    if (!lib.servers.length) {
      const add = button(t("＋ Add a server"), "action", () => editServer(null));
      body.append(empty(t("No servers in the library yet"), lib.foundServers.length ? t("Add one, or bring in those your agents already have, below.") : t("Paste a server's JSON from its README, or fill it in."), add));
    } else {
      const list = el("div", "list lib-list");
      // in the order the reader picked (#481), A→Z till then
      const fill = () => list.replaceChildren(...[...lib.servers].sort(byName(sortOf("libServers"))).map((s) => serverRow(s, all)));
      // one heading over the list, with what the skills' has (#1027): the
      // order, Select, every server on or off for every agent shown, one
      // agent's (#475, or out of a hidden one) and every server out
      const many = lib.servers.length > 1, by = byAgentRows("servers").length;
      if (many || by) {
        const rh = el("div", "row-head lib-serverhead");
        rh.append(el("span", "label", t("In the library")), el("span", "grow"));
        if (many) rh.append(sortBy("libServers", NAME_SORTS, fill));
        if (many && all.length) rh.append(pickButton("servers"), ...everyButtons("servers", all));
        if (by) rh.append(byAgentButton("servers"));
        if (many) rh.append(removeEveryButton("servers"));
        body.append(rh);
      }
      fill();
      body.append(list);
      if (picking) body.append(pickBar(all));
      const after = el("div", "after-list");
      after.append(button(t("＋ Add server"), "", () => editServer(null)));
      body.append(after);
      // each server is checked once a session, and again only once it
      // changed; once the rows are on the page, for them to show it
      const unchecked = lib.servers.filter((s) => health.get(s.name)?.key !== healthKey(s));
      queueMicrotask(() => checkHealth(unchecked));
    }
    // an agent's own servers (Codex's node_repl, added each time it starts)
    // aren't listed: magpie leaves them as they are
    const found = lib.foundServers.filter((f) => !f.own);
    if (found.length) {
      const rh = el("div", "row-head");
      rh.append(el("span", "label", t("In your agents")), el("span", "grow"), el("span", "note", t("not in the library — bring one in to manage it here")));
      body.append(rh);
      const list = el("div", "list lib-list");
      for (const f of found) list.append(foundServerRow(f));
      body.append(list);
    }
    if (lib.servers.length || lib.projects.length) renderProjects(body, "mcp");
    const skip = shownAgents().filter((a) => !a.mcp);
    if (skip.length) body.append(el("p", "lib-aside", t("{agents} has no MCP servers magpie can write.", { agents: skip.map((a) => a.name).join(", ") })));
    body.append(discover("mcp"));
  }

  function serverRow(s, all) {
    const row = el("div", "row lib-row click lib-server");
    const who = el("div", "who");
    const nm = el("div", "name mono", s.name);
    if (s.signIn?.dead) nm.append(tag(t("Sign-in ran out"), "warn", t("Open it to sign in again")));
    else if (s.signIn?.signedIn) nm.append(tag(t("Signed in"), "lib-signed", t("The agents given it use magpie's sign-in")));
    // its status beside the name, not in it: the name is still the name
    who.classList.add("lib-srvwho");
    who.append(nm, healthEl(s.name));
    const sub = el("div", "sub mono", serverLine(s));
    sub.title = serverLine(s);
    who.append(sub);
    row.append(mark(s.icon, s.transport === "stdio" ? GLYPH.cmd : GLYPH.web), who,
      agentChips(all, s.agents, chipsChange("servers/agents", s.name, "servers", (x) => serverRow(x, all)), { problems: s.problems, blocked: sseBlocked(s), all: true }));
    if (picking) return pickRow(row, s.name);
    row.onclick = () => editServer(s);
    row.title = t("Edit {name}", { name: s.name });
    return row;
  }

  // Whether a server works, as magpie found by connecting to it: started
  // (or reached) and asked for its tools. Its row shows it as a dot and a
  // few words, the whole reason in its tooltip; a click checks it again.
  const health = new Map(); // name → { key: the server as checked, h: what was found, null while checking }
  const healthKey = (s) => JSON.stringify([s.transport, s.command, s.args, s.env, s.url, s.headers, s.signIn]);
  function healthEl(name) {
    const b = el("button", "lib-health");
    b.type = "button";
    b.dataset.server = name;
    b.onclick = (e) => {
      e.stopPropagation(); // the row opens the editor
      const s = lib.servers.find((x) => x.name === name);
      if (s && health.get(name)?.h) checkHealth([s], true);
    };
    paintHealth(b);
    return b;
  }
  async function checkHealth(list, fresh) {
    if (!list.length) return;
    const keys = new Map(list.map((s) => [s.name, healthKey(s)]));
    for (const [name, key] of keys) { health.set(name, { key, h: null }); paintHealth(name); }
    let got = {}, failed = null;
    try { got = (await api("library/mcp/check", { names: [...keys.keys()], fresh: !!fresh })).servers || {}; } catch (e) { failed = e.message; }
    for (const [name, key] of keys) {
      // a server changed while it was checked has been asked for again
      if (health.get(name)?.key !== key) continue;
      const h = got[name] || (failed ? { state: "error", why: "request", detail: failed } : null);
      if (h) health.set(name, { key, h }); else health.delete(name);
      paintHealth(name);
    }
  }
  function healthWords(h, s) {
    if (h.state === "ok") return [h.tools === 1 ? t("1 tool") : t("{n} tools", { n: h.tools }), t("It started and listed its tools")];
    if (h.state === "auth") {
      const why = h.oauth
        ? (s?.transport === "http" ? t("The server asks for a sign-in — open it to sign in once in magpie") : t("The server asks for a sign-in"))
        : t("The server refused magpie (HTTP {code}) — a key in its headers may be missing or wrong", { code: h.code || 401 });
      return [t("needs sign-in"), h.detail ? why + "\n" + h.detail : why];
    }
    const more = (x) => (h.detail ? x + "\n" + h.detail : x);
    switch (h.why) {
      case "notfound": return [t("can't start: {cmd} not found", { cmd: h.detail }), t("Can't start it: there is no {cmd} on the PATH magpie has", { cmd: h.detail })];
      case "start": return [t("can't start"), more(t("Can't start it"))];
      case "exited": return [h.code ? t("exited ({code})", { code: h.code }) : t("exited"), more(h.code ? t("It exited with code {code} before listing its tools", { code: h.code }) : t("It exited before listing its tools"))];
      case "timeout": return [t("no answer"), more(t("No answer in 15 seconds"))];
      case "http": return ["HTTP " + h.code, t("The server answered {status}", { status: h.detail })];
      case "refused": return [t("connection refused"), more(t("Nothing is listening at that address"))];
      case "unreachable": return [t("can't reach"), more(t("Can't reach the server"))];
      case "protocol": return [t("bad reply"), more(t("It answered, but not as an MCP server does"))];
      default: return [t("couldn't check"), more(t("magpie couldn't check it"))];
    }
  }
  // paintHealth fills a row's status in place, by the button or the
  // server's name, so a check ending moves nothing on the page
  function paintHealth(x) {
    const boxes = typeof x === "string" ? page.querySelectorAll(`.lib-health[data-server="${CSS.escape(x)}"]`) : [x];
    for (const b of boxes) {
      const e = health.get(b.dataset.server);
      b.hidden = !e;
      if (!e) continue;
      if (!e.h) {
        b.className = "lib-health checking";
        b.replaceChildren(el("span", "", t("checking…")));
        b.title = t("Connecting to it to list its tools");
        continue;
      }
      const [text, tip] = healthWords(e.h, lib.servers.find((s) => s.name === b.dataset.server));
      b.className = "lib-health " + (e.h.state === "ok" ? "ok" : e.h.state === "auth" ? "auth" : "err");
      b.replaceChildren(el("span", "", text));
      b.title = tip + "\n" + t("Click to check again");
    }
  }

  // magpie's sign-in to a remote server (#615): signed in once here, and
  // every agent given the server reaches it through magpie, with it
  let mcpSigning = null; // { name, id, state, error } while one is under way
  // name is the server as saved ("" for one being added); ready saves what
  // the editor shows when it isn't that (a URL changed to the one that
  // signs in, as Exa's ?login) and gives the name it's saved as, for the
  // sign-in to be to the server the form shows
  function signInBox(name, ready) {
    const box = el("div", "lib-signin");
    const draw = () => {
      box.replaceChildren();
      const cur = lib.servers.find((x) => x.name === name)?.signIn || {};
      const sg = mcpSigning?.name === name ? mcpSigning : null;
      const line = el("div", "lib-signin-line");
      if (sg && (sg.state === "starting" || sg.state === "waiting")) {
        line.append(el("span", "note", sg.state === "starting" ? t("Opening the sign-in…") : t("Finish signing in in your browser…")),
          button(t("Cancel"), "", async () => {
            if (sg.id) await api("library/mcp-signin/" + sg.id + "/cancel", {}).catch(() => {});
            mcpSigning = null;
            draw();
          }));
      } else if (cur.signedIn && !cur.dead) {
        line.append(tag(t("Signed in"), "lib-signed"), el("span", "note", t("The agents given it use magpie's sign-in")), el("span", "grow"), button(t("Sign out"), "", signOut));
      } else {
        if (cur.dead) line.append(tag(t("Sign-in ran out"), "warn"));
        line.append(button(cur.dead ? t("Sign in again") : t("Sign in"), "action", start));
      }
      box.append(line);
      if (sg?.state === "failed") box.append(el("div", "lib-signin-err", sg.error));
    };
    async function start() {
      const was = name;
      mcpSigning = { name, state: "starting" };
      draw();
      try {
        if (ready) {
          const saved = await ready();
          // canceled while it was saved
          if (mcpSigning?.name !== was || mcpSigning.id) return;
          name = saved;
          mcpSigning = { name, state: "starting" };
        }
        const st = await api("library/mcp-signin", { name });
        if (web && st.url) api("open", { url: st.url }).catch(() => {});
        mcpSigning = { ...st, name };
        draw();
        follow(st.id);
      } catch (e) {
        if (!mcpSigning) return; // canceled
        // magpie's words in the reader's language, where it has them
        mcpSigning = { name, state: "failed", error: t(e.message) };
        draw();
      }
    }
    // followed with the dialog closed too, for the row to say it's signed in
    async function follow(id) {
      while (mcpSigning?.id === id) {
        await new Promise((r) => setTimeout(r, 800));
        let st;
        try { st = await api("library/mcp-signin/" + id); } catch { continue; }
        if (mcpSigning?.id !== id) return;
        if (st.state === "waiting") continue;
        if (st.state === "done") {
          mcpSigning = null;
          await api("library").then(take, () => {});
          status(t("Signed in to {name} — the agents given it use magpie's sign-in", { name }), "ok");
          render();
        } else mcpSigning = st.state === "canceled" ? null : { ...st, name };
        draw();
        return;
      }
    }
    async function signOut() {
      try {
        take(await api("library/mcp-signout", { name }));
        status(t("Signed out of {name} — the agents are given the server's own address again", { name }), "ok");
        render();
        draw();
      } catch (e) { status(e.message, "err", 6000); }
    }
    draw();
    return box;
  }

  function foundServerRow(f) {
    const s = f.server;
    const row = el("div", "row lib-row");
    const who = el("div", "who");
    const nm = el("div", "name mono", s.name);
    who.append(nm);
    const sub = el("div", "sub mono", serverLine(s));
    sub.title = serverLine(s);
    who.append(sub);
    const have = el("div", "lib-have");
    for (const id of s.agents) { const a = agentOf(id); if (a) { const i = icon(a.icon); i.title = a.name; have.append(i); } }
    row.append(mark(f.icon, s.transport === "stdio" ? GLYPH.cmd : GLYPH.web), who, have);
    if (f.others?.length) row.append(tag(t("differs in {agents}", { agents: f.others.map(nameOf).join(", ") }), "warn", t("{agents} has another server by this name; bringing this one in leaves that one as it is", { agents: f.others.map(nameOf).join(", ") })));
    const b = button(t("Bring in"), "action", () => change("servers/import", { name: s.name }, t("{name} is in the library now", { name: s.name })));
    b.title = t("Keeps it in the library: {agents} go on having it, and you can give it to the others", { agents: s.agents.map(nameOf).join(", ") });
    row.append(b);
    return row;
  }

  // A command line split the way a shell would, quotes and all.
  function words(s) {
    const out = [];
    let cur = "", q = "", any = false;
    for (let i = 0; i < s.length; i++) {
      const c = s[i];
      if (q) {
        if (c === q) q = "";
        else if (c === "\\" && q === '"' && i + 1 < s.length) cur += s[++i];
        else cur += c;
      } else if (c === "'" || c === '"') { q = c; any = true; }
      else if (c === "\\" && i + 1 < s.length) { cur += s[++i]; any = true; }
      else if (/\s/.test(c)) { if (cur || any) out.push(cur); cur = ""; any = false; }
      else cur += c;
    }
    if (cur || any) out.push(cur);
    return out;
  }

  // The servers in JSON the user pasted: a README's {"mcpServers": {...}},
  // one server's entry, or VS Code's {"servers": {...}}.
  function serversIn(text) {
    let v;
    try { v = JSON.parse(text.trim().replace(/,\s*([}\]])/g, "$1")); } catch {
      try { v = JSON.parse("{" + text.trim().replace(/,\s*$/, "") + "}"); } catch { return null; }
    }
    if (!v || typeof v !== "object") return null;
    const map = v.mcpServers || v.servers || v.mcp || v.context_servers || null;
    const one = (name, e) => {
      if (!e || typeof e !== "object") return null;
      const url = e.url || e.serverUrl || e.httpUrl || e.uri;
      const s = { name: (name || "").replace(/[^A-Za-z0-9_-]+/g, "-").replace(/^-+|-+$/g, ""), env: {}, headers: {} };
      if (url) {
        s.transport = e.type === "sse" || e.transport === "sse" || /\/sse\/?$/.test(url) ? "sse" : "http";
        s.url = url;
        s.headers = { ...(e.headers || e.http_headers || {}) };
      } else if (e.command) {
        s.transport = "stdio";
        const cmd = Array.isArray(e.command) ? e.command : [e.command];
        s.command = String(cmd[0]);
        s.args = [...cmd.slice(1), ...(e.args || [])].map(String);
        s.env = { ...(e.env || e.environment || {}) };
      } else return null;
      return s;
    };
    if (map && typeof map === "object") {
      return Object.entries(map).map(([n, e]) => one(n, e)).filter(Boolean);
    }
    if (v.command || v.url || v.serverUrl || v.httpUrl) { const s = one(v.name || "", v); return s ? [s] : null; }
    const entries = Object.entries(v).filter(([, e]) => e && typeof e === "object" && (e.command || e.url || e.serverUrl || e.httpUrl));
    return entries.length ? entries.map(([n, e]) => one(n, e)).filter(Boolean) : null;
  }

  // Key–value rows for a server's environment or headers.
  function pairs(obj, keyHint, valHint, onChange) {
    const box = el("div", "lib-pairs");
    const rows = Object.entries(obj || {});
    const emit = () => onChange(Object.fromEntries(rows.filter(([k]) => k.trim()).map(([k, v]) => [k.trim(), v])));
    const draw = () => {
      box.replaceChildren();
      rows.forEach((r, i) => {
        const line = el("div", "lib-pair");
        const k = field2(r[0], keyHint, (v) => { r[0] = v; emit(); });
        const v = field2(r[1], valHint, (x) => { r[1] = x; emit(); });
        const x = button("", "lib-x", () => { rows.splice(i, 1); emit(); draw(); });
        x.append(svg("M4.5 4.5l7 7M11.5 4.5l-7 7", 11, 1.6));
        x.title = t("Remove");
        line.append(k, v, x);
        box.append(line);
      });
      box.append(button(t("＋ Add"), "lib-addpair", () => { rows.push(["", ""]); draw(); box.querySelectorAll(".lib-pair input")[rows.length * 2 - 2]?.focus(); }));
    };
    draw();
    return box;
  }
  function field2(value, placeholder, onInput) {
    const i = el("input");
    i.type = "text";
    i.value = value || "";
    i.placeholder = placeholder || "";
    i.spellcheck = false;
    i.autocomplete = "off";
    i.oninput = () => onInput(i.value);
    i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") closeLibModal(); else if (e.key === "Enter" && modal?.save) { e.preventDefault(); modal.save(); } };
    return i;
  }

  function editServer(s, prefill) {
    const all = mcpAgents();
    const d = prefill || (s ? structuredClone(s) : { name: "", transport: "stdio", command: "", args: [], env: {}, url: "", headers: {}, agents: all.map((a) => a.id) });
    if (!d.env) d.env = {};
    if (!d.headers) d.headers = {};
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(mark(s?.icon, d.transport === "stdio" ? GLYPH.cmd : GLYPH.web), el("b", "", s ? s.name : t("New MCP server")));
    if (!s) head.append(el("span", "note", t("or paste its JSON anywhere here")));
    ed.append(head);
    const err = el("div", "editor-error");

    const name = field2(d.name, t("e.g. github"), (v) => { d.name = v.trim(); });
    ed.append(...field(t("Name"), name, t("Letters, digits, - and _ — what the agents call it")));
    const kindOf = () => segs([["stdio", t("Command")], ["http", "HTTP"], ["sse", "SSE"]], d.transport, (v) => { d.transport = v; draw(); });
    const kindBox = el("div");
    kindBox.append(kindOf());
    ed.append(...field(t("Runs as"), kindBox));
    const slot = el("div", "lib-slot");
    ed.append(slot);
    const agentsBox = el("div");
    ed.append(...field(t("Agents"), agentsBox));
    ed.append(err);

    function drawAgents() {
      agentsBox.replaceChildren(agentChips(all, d.agents, (next) => { d.agents = next; drawAgents(); }, { names: true, blocked: sseBlocked(d) }));
    }
    function draw() {
      slot.replaceChildren();
      const g = el("div", "lib-grid");
      if (d.transport === "stdio") {
        const line = field2([d.command, ...(d.args || [])].filter((x, i) => i > 0 || x).map(quote).join(" "), "npx -y @modelcontextprotocol/server-github", (v) => { const w = words(v); d.command = w[0] || ""; d.args = w.slice(1); });
        line.classList.add("mono");
        g.append(...field(t("Command"), line, t("The command and its arguments, as you'd type them")));
        g.append(...field(t("Environment"), pairs(d.env, "GITHUB_TOKEN", t("value"), (v) => { d.env = v; })));
      } else {
        const url = field2(d.url, "https://example.com/mcp", (v) => { d.url = v.trim(); });
        url.classList.add("mono");
        g.append(...field("URL", url));
        g.append(...field(t("Headers"), pairs(d.headers, "Authorization", "Bearer …", (v) => { d.headers = v; })));
        // Sign in saves the form first when it isn't what's saved, so a URL
        // just changed (or a server just added) is the one signed in to
        if (d.transport === "http") g.append(...field(t("Sign-in"), signInBox(s ? s.name : "", ready), t("For a server that asks you to sign in (OAuth): magpie signs in once, and every agent given it uses that sign-in")));
      }
      slot.append(g);
      // its own icon while it runs as it did; another way of running is another server
      head.firstChild.replaceWith(mark(s && d.transport === s.transport ? s.icon : "", d.transport === "stdio" ? GLYPH.cmd : GLYPH.web));
      drawAgents();
    }
    draw();

    const bar = el("div", "bar");
    if (s) bar.append(button(t("Remove from the library"), "danger", async () => {
      if (!await confirmRemoval(s.name, "It will be removed from the library and the agents it was given to.")) return;
      if (await change("servers/remove", { name: s.name }, t("{name} is out of the library and the agents it was given to", { name: s.name }))) closeLibModal(true);
    }));
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal));
    // send saves the form; s is then the server as saved, for a save after
    // a sign-in's to be of it
    const send = async () => {
      const body = { old: s ? s.name : "", server: { name: d.name, transport: d.transport, agents: d.agents } };
      if (d.transport === "stdio") Object.assign(body.server, { command: d.command, args: d.args, env: d.env });
      else Object.assign(body.server, { url: d.url, headers: d.headers });
      take(await api("library/servers/save", body));
      const was = s;
      s = lib.servers.find((x) => x.name === d.name) || s;
      return was;
    };
    const save = async () => {
      err.textContent = "";
      try {
        const was = await send();
        report(lib.result, was ? t("{name} saved", { name: d.name }) : "");
        closeLibModal(true);
        render();
      } catch (e) { err.textContent = e.message; }
    };
    // before a sign-in: the form saved, the dialog left open, when it
    // isn't the server as saved
    const sorted = (h) => JSON.stringify(Object.entries(h || {}).sort());
    async function ready() {
      if (s && d.name === s.name && d.transport === s.transport && d.url === (s.url || "") && sorted(d.headers) === sorted(s.headers)) return s.name;
      err.textContent = "";
      const was = await send();
      report(lib.result, was ? t("{name} saved", { name: d.name }) : "");
      ok.textContent = t("Save");
      render();
      return s.name;
    }
    const ok = button(s ? t("Save") : t("Add"), "primary", save);
    bar.append(ok);
    ed.append(bar);

    // a README's JSON pasted anywhere in the dialog fills it in
    ed.addEventListener("paste", (e) => {
      const text = e.clipboardData?.getData("text") || "";
      if (!/[{]/.test(text)) return;
      const found = serversIn(text);
      if (!found?.length) return;
      e.preventDefault();
      if (found.length === 1) {
        const f = found[0];
        Object.assign(d, { transport: f.transport, command: f.command || "", args: f.args || [], env: f.env || {}, url: f.url || "", headers: f.headers || {} });
        if (f.name && (!d.name || !s)) d.name = f.name;
        name.value = d.name;
        kindBox.replaceChildren(kindOf());
        draw();
        status(t("Filled in from what you pasted"), "ok");
      } else pasteMany(found, d.agents);
    });
    const original = JSON.stringify(d);
    modal = { save, dirty: () => JSON.stringify(d) !== original };
    openLib(ed);
    if (!s) requestAnimationFrame(() => name.focus());
  }

  // Several servers pasted at once: pick those to add.
  function pasteMany(found, agents) {
    const all = mcpAgents();
    const have = new Set(lib.servers.map((s) => s.name));
    const pick = new Set(found.filter((f) => f.name && !have.has(f.name)).map((f) => f.name));
    let who = [...agents];
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.cmd), el("b", "", t("{n} servers in what you pasted", { n: found.length })));
    ed.append(head);
    const list = el("div", "lib-picks");
    for (const f of found) {
      const r = el("label", "lib-pick" + (have.has(f.name) ? " dim" : ""));
      const c = el("input");
      c.type = "checkbox";
      c.checked = pick.has(f.name);
      c.disabled = !f.name || have.has(f.name);
      c.onchange = () => { if (c.checked) pick.add(f.name); else pick.delete(f.name); go.textContent = label(); go.disabled = !pick.size; };
      const w = el("span", "who");
      w.append(el("span", "name mono", f.name || t("(no name)")), el("span", "sub mono", serverLine(f)));
      r.append(c, w);
      if (have.has(f.name)) r.append(tag(t("in the library"), ""));
      list.append(r);
    }
    ed.append(list);
    const agentsBox = el("div");
    const drawAgents = () => agentsBox.replaceChildren(agentChips(all, who, (n) => { who = n; drawAgents(); }, { names: true }));
    drawAgents();
    ed.append(...field(t("Agents"), agentsBox));
    const err = el("div", "editor-error");
    ed.append(err);
    const bar = el("div", "bar");
    const label = () => (pick.size === 1 ? t("Add 1 server") : t("Add {n} servers", { n: pick.size }));
    const go = button(label(), "primary", async () => {
      go.disabled = true;
      let last = null;
      for (const f of found.filter((x) => pick.has(x.name))) {
        try {
          last = await api("library/servers/save", { old: "", server: { ...f, agents: who.filter((id) => reaches(f)(agentOf(id))) } });
        } catch (e) { err.textContent = f.name + ": " + e.message; go.disabled = false; if (last) { take(last); render(); } return; }
      }
      take(last);
      report(lib.result, t("Added {n} servers", { n: pick.size }));
      closeLibModal(true);
      render();
    });
    go.disabled = !pick.size;
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  // ---------- skills ----------

  function renderSkills(body) {
    body.append(intro(t("A skill is a folder with a SKILL.md an agent loads when it's needed. The library keeps each one once and links it into the agents you pick.")));
    body.append(installCard());
    const all = skillAgents();
    pickingFor("skills", lib.skills);
    if (lib.skills.length) {
      const rh = el("div", "row-head lib-skillshead");
      rh.append(el("span", "label", t("In the library")));
      const box = el("div", "lib-groups");
      if (lib.skills.length > 8) rh.append(skillFilter(box, all));
      const fresh = lib.skills.filter((s) => s.kind === "github" || s.origin);
      rh.append(el("span", "grow"));
      if (lib.skills.length > 1) rh.append(sortBy("libSkills", SKILL_SORTS(), () => { if (box.isConnected) drawSkills(box, all); }));
      if (lib.skills.length > 1 && all.length) rh.append(pickButton("skills"), ...everyButtons("skills", all));
      if (byAgentRows("skills").length) rh.append(byAgentButton("skills"));
      if (lib.skills.length > 1) rh.append(removeEveryButton("skills"));
      if (lib.skills.some((s) => s.kind === "github")) {
        const c = button(checking ? t("Checking…") : t("Check for updates"), "lib-updall", () => checkSkills());
        c.title = t("Ask GitHub which skills changed since they were installed");
        c.disabled = checking;
        if (checking) c.classList.add("busy");
        rh.append(c);
      }
      const stale = lib.skills.filter((s) => s.check?.status === "update");
      if (stale.length) {
        const n = stale.length;
        const u = button(t("Update {n}", { n }), "action lib-updall", async (e, b) => {
          b.classList.add("busy");
          b.textContent = t("Updating…");
          await updateAllSkills(stale.map((s) => s.name));
          b.classList.remove("busy");
        });
        u.title = n === 1 ? t("Fetch {name} from GitHub again", { name: stale[0].name }) : t("Fetch the {n} skills GitHub changed again", { n });
        rh.append(u);
      } else if (fresh.length > 1) {
        const u = button(t("Update all"), "action lib-updall", async (e, b) => {
          b.classList.add("busy");
          b.textContent = t("Updating…");
          await updateAllSkills();
          b.classList.remove("busy");
          b.textContent = t("Update all");
        });
        u.title = t("Fetch the {n} skills from GitHub again", { n: fresh.length });
        rh.append(u);
      }
      body.append(rh);
      drawSkills(box, all);
      body.append(box);
      // under the list, so the skills stay where they were above it
      if (all.length) body.append(skillHowBar());
      if (picking) body.append(pickBar(all));
    }
    if (lib.newSkills?.length) renderNewSkills(body);
    if (lib.foundSkills.length) {
      const rh = el("div", "row-head");
      rh.append(el("span", "label", t("In your agents")), el("span", "grow"), el("span", "note", t("not in the library — bring one in to give it to the others")));
      // every one at once, rather than a click for each
      if (lib.foundSkills.length > 1) {
        const names = lib.foundSkills.map((f) => f.name);
        const all = button(t("Bring in all"), "action lib-updall lib-importall", async (e, b) => {
          b.classList.add("busy");
          b.textContent = t("Bringing in…");
          await importAllSkills(names);
          b.classList.remove("busy");
          b.textContent = t("Bring in all");
        });
        all.title = t("Brings the {n} skills into the library: the agents that have them go on having them, and you can give them to the others", { n: names.length });
        rh.append(all);
      }
      body.append(rh);
      const list = el("div", "list lib-list");
      for (const f of lib.foundSkills) list.append(foundSkillRow(f));
      body.append(list);
    }
    if (lib.skills.length || lib.projects.length) renderProjects(body, "skills");
    const skip = shownAgents().filter((a) => !a.skills);
    if (skip.length) body.append(el("p", "lib-aside", t("{agents} has no skills folder.", { agents: skip.map((a) => a.name).join(", ") })));
    // Desktop reads its skills list again only when its window is reloaded (#638)
    if (shownAgents().some((a) => a.id === "claude-desktop" && a.skills)) {
      body.append(el("p", "lib-aside", t("Claude Desktop shows skill changes once its window is reloaded ({keys}). It gets a copy of each skill, and one you change in Desktop is left as it is.", { keys: /^Mac/.test(navigator.platform) ? "⌘R" : "Ctrl+R" })));
    }
    body.append(discover("skills"));
  }

  // Skills a check for updates found in the GitHub repositories the
  // library's skills came from, beside them, that the library doesn't have:
  // ones a repository added since, or one the picker showed and wasn't
  // picked before magpie kept which it showed. Each is added for the agents
  // that have its repository's others, or set aside so a check doesn't
  // offer it again.
  function renderNewSkills(body) {
    const list = lib.newSkills;
    const rh = el("div", "row-head lib-newhead");
    rh.append(el("span", "label", t("Also in their repositories")), el("span", "grow"), el("span", "note", t("on GitHub beside skills you have, not in the library yet")));
    if (list.length > 1) {
      const ids = list.map((n) => n.id);
      const ign = button(t("Ignore all"), "lib-updall", () => change("skills/ignore-new", { names: ids }, t("{n} skills set aside", { n: ids.length })));
      ign.title = t("A check for updates won't offer these again");
      const add = button(t("Add all"), "action lib-updall", async (e, b) => {
        b.classList.add("busy");
        b.textContent = t("Adding…");
        await change("skills/add-new", { names: ids }, t("{n} skills added", { n: ids.length }));
        b.classList.remove("busy");
        b.textContent = t("Add all");
      });
      add.title = t("Adds the {n} skills, each for the agents that have its repository's other skills", { n: ids.length });
      rh.append(ign, add);
    }
    body.append(rh);
    const box = el("div", "list lib-list lib-newskills");
    for (const n of list) box.append(newSkillRow(n));
    body.append(box);
  }

  function newSkillRow(n) {
    const row = el("div", "row lib-row lib-newskill");
    const who = el("div", "who");
    who.append(el("div", "name", n.name));
    const sub = el("div", "sub", n.description || "");
    sub.title = n.description || "";
    who.append(sub);
    const src = el("div", "lib-src");
    const a = el("a", "lib-srclink", n.repo + "/" + n.path);
    a.href = n.id;
    a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); browse(n.id); };
    src.append(a);
    who.append(src);
    const have = el("div", "lib-have");
    // one find without its list of agents doesn't take the page down (#1217)
    const agents = n.agents || [];
    for (const id of agents) { const ag = agentOf(id); if (ag) { const i = agentIcon(ag.icon); i.title = ag.name; have.append(i); } }
    const ign = button(t("Ignore"), "", () => change("skills/ignore-new", { names: [n.id] }, t("{name} set aside", { name: n.name })));
    ign.title = t("A check for updates won't offer it again");
    const add = button(t("Add"), "action", async (e, b) => {
      b.classList.add("busy");
      await change("skills/add-new", { names: [n.id] }, t("{name} is in the library now", { name: n.name }));
      b.classList.remove("busy");
    });
    const names = agents.map(nameOf).join(", ");
    add.title = names ? t("Adds it to the library for {agents}, which have its repository's other skills", { agents: names }) : t("Adds it to the library");
    row.append(glyph(GLYPH.skill), who, have, ign, add);
    return row;
  }

  // ---------- the library's skills, by where they came from ----------

  // Hundreds of skills were one list of rows, every one of them drawn with
  // a chip for each agent — thousands of icons laid out and painted on each
  // redraw, scroll and hover. They're grouped by the GitHub repository they
  // came from (the ones on this computer together), a big group again by
  // the folder they sit in there (or the start of their names), and a
  // group's rows are a window on its list: only those near the view are
  // drawn, the rest is room kept for them, so a scroll through a thousand
  // skills draws a few rows now and then instead of laying out and painting
  // each as it comes into view.
  let picking = "";            // "skills" or "servers": that page's rows have a box each, to pick some (#791, #1027)
  let naming = false;          // the pick bar asks the name of a group to put them in
  const picked = new Set();    // the names picked
  let skillQuery = "";         // what the filter over the skills holds
  let skillTimer = 0;          // the filter's redraw, waiting for typing to pause
  const unfiltered = new Set(); // groups folded while filtering, till the filter changes
  let folds = {};              // group → true when folded, false when opened by hand
  try { folds = JSON.parse(localStorage.getItem("magpie.libSkillFolds") || "{}") || {}; } catch {}
  function saveFolds() { try { localStorage.setItem("magpie.libSkillFolds", JSON.stringify(folds)); } catch {} }
  const repoOf = (u) => (u || "").replace(/^https:\/\/github\.com\//, "").split("/").slice(0, 2).join("/");
  // the repository a skill came from, however it came in: magpie traces
  // one not installed from GitHub too (the skills CLI's lock, the git
  // checkout it's in), so a repository's skills are one group (White
  // Immortal on Discord). owner/repo on GitHub, host/path elsewhere.
  const skillRepo = (s) => s.repo || (s.kind === "github" ? repoOf(s.source) : s.origin ? repoOf(s.origin) : "");
  const repoURL = (r) => (/^[^/]+\.[^/]+\//.test(r) ? "https://" + r : "https://github.com/" + r);
  const MANY = 40;   // a group bigger than this starts folded, and so do its parts
  const SPLIT = 8;   // a group bigger than this is split by folder, or by name
  // Heights the rows are held to (library.css): a list's room is known
  // without drawing it.
  const ROW_H = 51, ROW_SRC_H = 67, SUB_H = 36;

  // By source (the groups, as they always were) or one flat list of every
  // skill by name, A→Z or Z→A (#481)
  const SKILL_SORTS = () => [["source", t("By source")], ...NAME_SORTS];
  // the groups, and each skill's text to filter by, worked out once for
  // each answer from magpie and each pick
  let grouped = null;
  function skillGroups() {
    const pick = sortOf("libSkills", SKILL_SORTS());
    if (grouped?.lib === lib && grouped.pick === pick) return grouped.groups;
    if (pick !== "source") {
      const skills = [...lib.skills].sort(byName(pick));
      const g = { key: "flat", repo: "", skills, parts: null,
        text: new Map(skills.map((s) => [s, (s.name + " " + (s.description || "") + " " + skillRepo(s)).toLowerCase()])) };
      grouped = { lib, pick, groups: [g] };
      return grouped.groups;
    }
    // the user's own groups first, in the order made, each skill in its
    // group rather than its source's (#791)
    const mine = (lib.skillGroups || []).map((u) => ({ key: "my:" + u.name, repo: "", mine: u.name, skills: [], names: u.skills }));
    const inMine = new Map();
    for (const g of mine) for (const n of g.names) inMine.set(n, g);
    const by = new Map();
    for (const s of lib.skills) {
      const own = inMine.get(s.name);
      if (own) { own.skills.push(s); continue; }
      const repo = skillRepo(s);
      const key = repo ? "gh:" + repo.toLowerCase() : "local";
      let g = by.get(key);
      if (!g) by.set(key, (g = { key, repo, skills: [] }));
      g.skills.push(s);
    }
    // Installed from GitHub (in the Library's search, Install, or by CC
    // Switch) above, this computer's below (#1031, mintonight): a
    // repository traced for skills only found here, and a group of the
    // user's with any of those in it, are with On this computer. One
    // author's repositories above are one group, each a part when many
    // (oil-oil/oil-ui, oil-oil/oil-ppt…): skills.sh lists many authors'
    // skills a repository each.
    const online = (s) => s.kind === "github" || !!s.origin;
    const repos = [...by.values()].filter((g) => g.key !== "local");
    const up = repos.filter((g) => g.skills.some(online)), down = repos.filter((g) => !g.skills.some(online));
    const owners = new Map();
    for (const g of up) {
      if (/^[^/]+\.[^/]+\//.test(g.repo)) continue; // another git host's
      const o = g.repo.split("/")[0].toLowerCase();
      if (!owners.has(o)) owners.set(o, []);
      owners.get(o).push(g);
    }
    for (const [o, gs] of owners) {
      if (gs.length < 2) continue;
      const all = { key: "owner:" + o, repo: gs[0].repo.split("/")[0], owner: true, skills: gs.flatMap((g) => g.skills) };
      up.splice(up.indexOf(gs[0]), 1, all);
      for (const g of gs.slice(1)) up.splice(up.indexOf(g), 1);
    }
    const byRepo = (a, b) => a.repo.localeCompare(b.repo);
    const mineHere = (g) => !g.skills.every(online);
    const groups = [...mine.filter((g) => g.skills.length && !mineHere(g)), ...up.sort(byRepo),
      ...mine.filter((g) => g.skills.length && mineHere(g)), ...down.sort(byRepo), ...(by.has("local") ? [by.get("local")] : [])];
    for (const g of groups) {
      g.skills.sort((a, b) => a.name.localeCompare(b.name));
      g.text = new Map(g.skills.map((s) => [s, (s.name + " " + (s.description || "") + " " + (g.mine || (g.owner ? skillRepo(s) : g.repo))).toLowerCase()]));
      g.parts = g.mine ? null : partsOf(g);
    }
    grouped = { lib, pick, groups };
    return groups;
  }

  // A big group's parts. A repository's: by the folder its skills sit in
  // there when they sit in more than one, else by the start their names
  // share ("gh-review", "gh-triage" → gh), a start three or more share
  // being a part and the rest one more. On this computer the start comes
  // first (#791, mintonight: lark-* among skills in two folders, or a few
  // lark-* among many, had no part of their own), since the folder there
  // is only where an installer put a skill, and the rest go by folder.
  // Nothing is fetched: it's all in where each skill came from.
  function partsOf(g) {
    if (g.skills.length <= SPLIT) return null;
    const split = (skills, keyOf) => {
      const m = new Map();
      for (const s of skills) {
        const k = keyOf(s);
        if (!m.has(k)) m.set(k, []);
        m.get(k).push(s);
      }
      return m.size > 1 ? m : null;
    };
    const folderIn = (s) => {
      if (s.kind === "github") {
        // …/tree/<ref>/<path to the folder>/<skill>
        const m = /^https:\/\/github\.com\/[^/]+\/[^/]+\/tree\/[^/]+\/(.+)$/.exec(s.source || "");
        return m ? m[1].split("/").slice(0, -1).join("/") : "";
      }
      if (s.kind === "folder") return tilde((s.source || "").replace(/[\\/][^\\/]+[\\/]?$/, ""));
      return "";
    };
    const byStart = () => {
      const segs = (s) => s.name.toLowerCase().split(/[-_:.\s]+/).filter(Boolean);
      // a start every name has says nothing: the part after it does
      const all = g.skills.map(segs);
      let skip = 0;
      while (all.every((x) => x.length > skip + 1 && x[skip] === all[0][skip])) skip++;
      const lead = new Map();
      for (const x of all) if (x.length > skip + 1) lead.set(x[skip], (lead.get(x[skip]) || 0) + 1);
      return split(g.skills, (s) => { const x = segs(s); const k = x.length > skip + 1 ? x[skip] : ""; return lead.get(k) >= 3 ? k : ""; });
    };
    const label = (key, mono) => key || (mono ? (g.repo ? t("At the top of the repository") : t("Kept in the library")) : t("Others"));
    const order = (a, b) => (!a.key) - (!b.key) || a.mono - b.mono || a.key.localeCompare(b.key);
    // an author's: by repository
    if (g.owner) {
      return [...split(g.skills, (s) => skillRepo(s).toLowerCase())]
        .map(([key, skills]) => ({ key: "repo:" + key, skills, mono: true, label: skillRepo(skills[0]) })).sort(order);
    }
    if (g.repo) {
      // the folder in the repository is known of those installed from it
      // only: one traced to it goes by its name with the rest
      let m = g.skills.every((s) => s.kind === "github") ? split(g.skills, folderIn) : null, mono = true;
      if (!m) { mono = false; m = byStart(); }
      if (!m) return null;
      return [...m].map(([key, skills]) => ({ key, skills, mono, label: label(key, mono) })).sort(order);
    }
    const starts = byStart();
    const named = starts ? [...starts].filter(([k]) => k) : [];
    const rest = starts ? starts.get("") || [] : g.skills;
    const folders = split(rest, folderIn);
    const parts = named.map(([key, skills]) => ({ key, skills, mono: false, label: key }));
    if (folders) for (const [key, skills] of folders) parts.push({ key: "dir:" + key, skills, mono: true, label: label(key, true) });
    else if (rest.length) parts.push({ key: "", skills: rest, mono: false, label: label("", false) });
    return parts.length > 1 ? parts.sort(order) : null;
  }

  function skillFilter(box, all) {
    const f = el("input", "lib-filter lib-skillq");
    f.type = "search";
    f.dataset.lib = "skillq";
    f.placeholder = t("Filter {n} skills…", { n: lib.skills.length });
    f.spellcheck = false;
    f.autocomplete = "off";
    f.value = skillQuery;
    // what's typed is kept at once (a redraw of the page shows it), the
    // rows drawn again once typing pauses
    const redraw = () => { clearTimeout(skillTimer); unfiltered.clear(); if (box.isConnected) drawSkills(box, all); };
    f.oninput = () => { skillQuery = f.value; clearTimeout(skillTimer); skillTimer = setTimeout(redraw, 120); };
    f.onkeydown = (e) => {
      e.stopPropagation();
      if (e.key === "Escape" && f.value) { e.preventDefault(); f.value = ""; skillQuery = ""; redraw(); }
    };
    return f;
  }

  // The skills' lists as last drawn into a box: a group's list keeps its
  // rows through a filter or a fold, and draws only those it hasn't.
  let drawn = null; // { box, lists: Map group → window }
  function drawSkills(box, all) {
    const groups = skillGroups();
    const q = skillQuery.trim().toLowerCase();
    if (drawn?.box !== box) drawn = { box, lists: new Map() };
    const hitsOf = (g) => (q ? g.skills.filter((s) => g.text.get(s).includes(q)) : g.skills);
    const cards = [];
    const single = groups.length === 1;
    const many = lib.skills.length > MANY;
    const redraw = () => { drawSkills(box, all); };
    for (const g of groups) {
      const hits = hitsOf(g);
      if (!hits.length) continue;
      const folded = !single && (q ? unfiltered.has(g.key) : folds[g.key] ?? many);
      let w = drawn.lists.get(g.key);
      if (!folded) {
        if (!w) drawn.lists.set(g.key, (w = windowed()));
        w.set(groupItems(g, hits, all, !!q, redraw));
      }
      // from one place only: its list with no heading over it
      if (single) {
        const card = el("div", "list lib-list lib-vcard");
        w.el.classList.add("bare");
        card.append(w.el);
        cards.push(card);
      } else cards.push(groupCard(g, hits, all, folded, !!q, w, redraw));
    }
    if (!cards.length) cards.push(el("div", "list lib-none", t("No skill matches “{q}”.", { q: skillQuery.trim() })));
    box.replaceChildren(...cards);
    if (box.isConnected) { syncLists(); if (picking) syncPicks(); } // the bar's All counts what the filter shows
  }

  // A group's list: its skills, or its parts, each a heading over its own.
  function groupItems(g, hits, all, filtering, redraw) {
    const row = (s) => ({ key: "s\n" + s.name, h: s.source ? ROW_SRC_H : ROW_H, make: () => skillRow(skillNamed(s.name) || s, all) });
    if (!g.parts) return hits.map(row);
    const hit = hits.length === g.skills.length ? null : new Set(hits);
    const items = [];
    for (const p of g.parts) {
      const ph = hit ? p.skills.filter((s) => hit.has(s)) : p.skills;
      if (!ph.length) continue;
      const fk = g.key + "\n" + p.key;
      const folded = filtering ? unfiltered.has(fk) : folds[fk] ?? g.skills.length > MANY;
      items.push({ key: ["p", p.key, folded, filtering, ph.length].join("\n"), h: SUB_H, make: () => partHead(g, p, ph, folded, filtering, fk, redraw, all) });
      if (!folded) for (const s of ph) items.push(row(s));
    }
    return items;
  }

  function partHead(g, p, hits, folded, filtering, fk, redraw, all) {
    const head = el("div", "row lib-row click lib-subhead" + (folded ? "" : " open"));
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 10, 1.7));
    const n = p.skills.length;
    if (picking) head.append(pickAll(hits));
    head.append(chev, el("span", "name" + (p.mono && p.key ? " mono" : ""), p.label),
      el("span", "sub", filtering && hits.length !== n ? t("{n} of {total}", { n: hits.length, total: n }) : String(n)));
    // a dot when GitHub changed some of its skills
    const stale = p.skills.filter((s) => s.check?.status === "update").length;
    if (stale) {
      const dot = el("span", "lib-dot");
      dot.title = stale === 1 ? t("1 skill has an update") : t("{n} skills have updates", { n: stale });
      head.append(dot);
    }
    // its skills on or off for an agent at once, as a group's are (#791)
    if (all.length) {
      const live = p.skills.map((s) => skillNamed(s.name) || s);
      head.append(el("span", "grow"), groupChips(live, all, null));
    }
    head.title = folded ? t("Show its skills") : t("Hide its skills");
    head.onclick = () => {
      if (filtering) { if (folded) unfiltered.delete(fk); else unfiltered.add(fk); }
      else { folds[fk] = !folded; saveFolds(); }
      redraw();
    };
    return head;
  }

  // the skill as magpie last said, for a row drawn after the list was:
  // a row scrolled away and back is drawn from what the page has now
  let named = null;
  function skillNamed(name) {
    if (named?.lib !== lib) named = { lib, by: new Map(lib.skills.map((s) => [s.name, s])) };
    return named.by.get(name);
  }

  function groupCard(g, hits, all, folded, filtering, w, redraw) {
    const card = el("div", "list lib-card lib-group");
    card.dataset.group = g.key;
    const head = el("div", "row lib-row click lib-grouphead" + (folded ? "" : " open"));
    const who = el("div", "who");
    const title = g.mine || g.repo || t("On this computer");
    const name = el("div", "name" + (g.repo ? " mono" : ""), title);
    who.append(name);
    const n = g.skills.length;
    who.append(el("div", "sub", filtering && hits.length !== n ? t("{n} of {total} skills", { n: hits.length, total: n })
      : n === 1 ? t("1 skill") : t("{n} skills", { n })));
    const tags = el("div", "lib-tags");
    const stale = g.skills.filter((s) => s.check?.status === "update");
    if (stale.length) {
      const u = button(t("Update {n}", { n: stale.length }), "action lib-updall", async (e, b) => {
        b.classList.add("busy");
        b.textContent = t("Updating…");
        await updateAllSkills(stale.map((s) => s.name));
      });
      u.title = stale.length === 1 ? t("Fetch {name} from GitHub again", { name: stale[0].name }) : t("Fetch the {n} skills GitHub changed again", { n: stale.length });
      tags.append(u);
    }
    const have = groupChips(g.skills, all, title);
    const acts = el("div", "lib-rowacts");
    // a group of the user's is renamed in place, or let go: its skills go
    // back to their sources' groups, and stay in the library as they are
    if (g.mine) {
      const rn = button(t("Rename"), "lib-updall lib-grouprename", () => renameGroup(g, name));
      rn.title = t("Rename this group");
      const un = button(t("Ungroup"), "lib-updall lib-ungroup", () => ungroup(g.mine, null));
      un.title = t("Let this group go: its skills go back to where they came from, and stay as they are");
      tags.append(rn, un);
    }
    if (g.repo) {
      const o = button("", "lib-icon", () => browse(repoURL(g.repo)));
      o.append(svg(GLYPH.out, 13, 1.4));
      o.title = repoURL(g.repo).startsWith("https://github.com/") ? t("Open {repo} on GitHub", { repo: g.repo }) : t("Open {repo}", { repo: g.repo });
      acts.append(o);
    }
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    const pic = g.repo ? mark(g.skills.find((s) => s.icon)?.icon, GLYPH.skill) : glyph(g.mine ? GLYPH.skill : GLYPH.folder);
    if (picking) head.append(pickAll(hits));
    head.append(pic, who, tags, have, acts, chev);
    head.title = folded ? t("Show its skills") : t("Hide its skills");
    head.onclick = () => {
      if (filtering) { if (folded) unfiltered.delete(g.key); else unfiltered.add(g.key); }
      else { folds[g.key] = !folded; saveFolds(); }
      redraw();
    };
    card.append(head);
    if (!folded) card.append(w.el);
    return card;
  }

  // A group's chips: which agents have its skills, all of them or some,
  // and a click gives an agent every one of them or takes them all (#787,
  // mintonight: a repository's skills were on or off one row at a time).
  // A chip is lit when its agent has them all; one with some is half lit,
  // and a click gives it the rest. All gives them to every agent shown.
  // A part's chips (#791, mintonight: the lark-* skills of Feishu's pack
  // on or off together) and a pick's are the same, said of so many skills
  // rather than of a repository.
  // Servers picked (#1027) are the same, counted of those an agent can
  // reach: one given every server it can reach of them has them all, and
  // one that can reach none of them can't be given them.
  function groupChips(skills, all, repo, kind = "skills") {
    const servers = kind === "servers";
    const n = skills.length, names = skills.map((s) => s.name);
    const can = (s, a) => !servers || reaches(s)(a);
    const reach = (a) => skills.filter((s) => can(s, a)).length;
    const count = (a) => skills.filter((s) => can(s, a) && s.agents?.includes(a.id)).length;
    const full = all.filter((a) => reach(a) && count(a) === reach(a)).map((a) => a.id);
    const box = agentChips(all, full, async (next) => {
      const on = next.filter((id) => !full.includes(id)), off = full.filter((id) => !next.includes(id));
      box.classList.add("busy");
      try {
        let v = null;
        if (on.length) v = await api("library/" + kind + "/agents-some", { names, agents: on, on: true });
        if (off.length) v = await api("library/" + kind + "/agents-some", { names, agents: off, on: false });
        if (!v) return;
        take(v);
        const who = (ids) => ids.map(nameOf).join(", ");
        report(v.result, repo != null
          ? (on.length ? t("{repo}'s skills are on for {agents}", { repo, agents: who(on) }) : t("{repo}'s skills are off for {agents}", { repo, agents: who(off) }))
          : n === 1 ? (on.length ? t("{name} is on for {agents}", { name: names[0], agents: who(on) }) : t("{name} is off for {agents}", { name: names[0], agents: who(off) }))
          : servers ? (on.length ? t("{n} servers are on for {agents}", { n, agents: who(on) }) : t("{n} servers are off for {agents}", { n, agents: who(off) }))
          : on.length ? t("{n} skills are on for {agents}", { n, agents: who(on) }) : t("{n} skills are off for {agents}", { n, agents: who(off) }));
      } catch (e) {
        status(e.message, "err", 6000);
      }
      render();
    }, { all: true, blocked: servers ? (a) => (reach(a) ? "" : n === 1 ? sseBlocked(skills[0])(a) : t("{agent} can reach none of these servers", { agent: a.name })) : null });
    box.classList.add("lib-groupchips");
    for (const c of box.querySelectorAll(".lib-ag[data-agent]")) {
      const a = all.find((x) => x.id === c.dataset.agent), k = count(a), m = reach(a);
      if (!m) continue;
      if (k && k < m) c.classList.add("some");
      c.title = servers
        ? (k === m ? t("{agent} has all of these servers — click to take them away", { agent: a.name })
          : k ? t("{agent} has {n} of these servers — click to give it the rest", { agent: a.name, n: k })
          : t("Give {agent} all of these servers", { agent: a.name }))
        : k === n ? t("{agent} has all of them — click to take them away", { agent: a.name })
          : k ? t("{agent} has {n} of them — click to give it the rest", { agent: a.name, n: k })
          : t("Give {agent} all of them", { agent: a.name });
    }
    const every = box.querySelector(".lib-ag.all");
    if (every) every.title = every.classList.contains("on")
      ? (servers ? t("Every agent has all of these servers — click to take them from every one") : t("Every agent has all of them — click to take them from every one"))
      : servers ? t("Give all of these servers to every agent") : t("Give all of them to every agent");
    return box;
  }

  // ---------- some skills, picked, on or off at once ----------

  // Select puts a box on each skill's row, and on each heading for the
  // skills under it, and a bar under the list turns the ones picked on or
  // off for an agent at once (#791, mintonight: 能不能支持多选来开启或关闭):
  // its chips work as a group's do. Done takes the boxes away.
  // The servers' page has the same (#1027, emo172), one box on each row.
  function pickButton(kind) {
    const b = button(picking ? t("Done") : t("Select"), "action lib-updall lib-select", () => {
      picking = picking ? "" : kind;
      picked.clear();
      render();
    });
    const skills = kind === "skills";
    b.title = picking ? (skills ? t("Stop picking skills") : t("Stop picking servers"))
      : skills ? t("Pick some skills and turn them on or off together") : t("Pick some servers and turn them on or off together");
    return b;
  }

  // what's picked is a page's own: another page, or a list too short to
  // pick from, ends it, and a name no longer in the list isn't picked
  function pickingFor(kind, items) {
    if (picking && (picking !== kind || items.length < 2)) { picking = ""; picked.clear(); }
    for (const n of picked) if (!items.some((x) => x.name === n)) picked.delete(n);
  }

  // a row with a box, which its click ticks rather than opening it
  function pickRow(row, name) {
    const box = pickBox([name]);
    row.prepend(box);
    row.classList.toggle("picked", picked.has(name));
    row.onclick = () => box.click();
    row.title = t("Select {name}", { name });
    return row;
  }

  // the skills the filter shows, in every group, folded or not
  function shownSkills() {
    const q = skillQuery.trim().toLowerCase();
    return skillGroups().flatMap((g) => (q ? g.skills.filter((s) => g.text.get(s).includes(q)) : g.skills));
  }

  // A box for some skills: ticked when they're all picked, half when some
  // are; a click picks them all, or none when they all were.
  function pickBox(names) {
    const box = el("input", "lib-pickbox");
    box.type = "checkbox";
    box._names = names;
    paintPick(box);
    box.onclick = (e) => {
      e.stopPropagation();
      const every = names.length && names.every((n) => picked.has(n));
      for (const n of names) if (every) picked.delete(n); else picked.add(n);
      syncPicks();
    };
    return box;
  }
  const pickAll = (skills) => pickBox(skills.map((s) => s.name));
  function paintPick(box) {
    const k = box._names.filter((n) => picked.has(n)).length;
    box.checked = k > 0 && k === box._names.length;
    box.indeterminate = k > 0 && k < box._names.length;
    const row = box.parentElement;
    if (row?.classList.contains("lib-skill") || row?.classList.contains("lib-server")) row.classList.toggle("picked", box.checked);
  }
  // every box drawn, and the bar, as the picks are now
  function syncPicks() {
    if (!picked.size) naming = false;
    for (const b of page.querySelectorAll(".lib-pickbox")) paintPick(b);
    const bar = page.querySelector(".lib-pickbar");
    if (bar) bar.replaceWith(pickBar(picking === "servers" ? mcpAgents() : skillAgents()));
  }

  // the servers' bar is the same, with no filter or groups (#1027)
  function pickBar(all) {
    const bar = el("div", "row lib-row lib-pickbar");
    const servers = picking === "servers";
    const shown = servers ? lib.servers : shownSkills(), q = servers ? "" : skillQuery.trim();
    const every = el("label", "lib-pickall");
    every.append(pickAll(shown), el("span", "", q ? t("All {n} that match", { n: shown.length }) : t("All {n}", { n: shown.length })));
    every.onclick = (e) => e.stopPropagation();
    const skills = (servers ? lib.servers : lib.skills).filter((s) => picked.has(s.name));
    const n = skills.length;
    bar.append(every, el("span", "lib-pickn" + (n ? "" : " none"), n ? t("{n} selected", { n })
      : servers ? t("Pick servers to turn them on or off together") : t("Pick skills to turn them on or off together")), el("span", "grow"));
    if (n && naming) return nameBar(bar, skills);
    if (n) {
      bar.append(groupChips(skills, all, null, servers ? "servers" : "skills"));
      // a group of the user's made of them, or the ones in one let go (#791)
      if (!servers && sortOf("libSkills", SKILL_SORTS()) === "source") {
        const gb = button(t("Group…"), "lib-updall lib-pickgroup", () => { naming = true; syncPicks(); });
        gb.title = t("Put the skills picked in a group of your own");
        bar.append(gb);
        const own = (lib.skillGroups || []).filter((u) => u.skills.some((x) => picked.has(x)));
        if (own.length) {
          const ub = button(t("Ungroup"), "lib-updall lib-pickungroup", async () => {
            for (const u of own) await ungroup(u.name, u.skills.filter((x) => picked.has(x)));
          });
          ub.title = t("Take the skills picked out of their groups");
          bar.append(ub);
        }
      }
      const c = button(t("Clear"), "lib-updall lib-pickclear", () => { picked.clear(); syncPicks(); });
      c.title = t("Unpick them all");
      bar.append(c);
    }
    bar.append(button(t("Done"), "action lib-updall lib-pickdone", () => { picking = ""; picked.clear(); render(); }));
    return bar;
  }

  // The bar asking a group's name for the skills picked: a new group's, or
  // one there is, which they join. Enter makes it, Escape goes back.
  function nameBar(bar, skills) {
    bar.classList.add("naming");
    const f = el("input", "lib-filter lib-groupname");
    f.placeholder = t("Group name");
    f.spellcheck = false;
    f.autocomplete = "off";
    const have = (lib.skillGroups || []).map((u) => u.name);
    const go = button(t("Group"), "action lib-updall lib-groupgo", () => makeGroup(f.value, skills));
    const sync = () => {
      const v = f.value.trim();
      go.disabled = !v;
      go.textContent = have.includes(v) ? t("Add to {name}", { name: v }) : t("Group");
    };
    f.oninput = sync;
    f.onkeydown = (e) => {
      e.stopPropagation();
      if (e.key === "Enter" && f.value.trim()) { e.preventDefault(); makeGroup(f.value, skills); }
      if (e.key === "Escape") { e.preventDefault(); naming = false; syncPicks(); }
    };
    sync();
    const back = button(t("Cancel"), "lib-updall lib-groupcancel", () => { naming = false; syncPicks(); });
    bar.append(el("span", "lib-pickn", skills.length === 1 ? t("Group 1 skill as") : t("Group {n} skills as", { n: skills.length })), f);
    // a group there is, to put them in with a click
    for (const h of have.slice(0, 4)) {
      const b = button(h, "lib-updall lib-groupto", () => makeGroup(h, skills));
      b.title = t("Add them to {name}", { name: h });
      bar.append(b);
    }
    bar.append(el("span", "grow"), back, go);
    requestAnimationFrame(() => f.focus({ preventScroll: true }));
    return bar;
  }

  async function makeGroup(name, skills) {
    name = name.trim();
    if (!name) return;
    try {
      const v = await api("library/skills/group", { name, names: skills.map((s) => s.name) });
      take(v);
      folds["my:" + name] = false; // a group just made is shown open
      saveFolds();
      picking = "";
      naming = false;
      picked.clear();
      report(v.result, skills.length === 1 ? t("{skill} is in {name}", { skill: skills[0].name, name }) : t("{n} skills are in {name}", { n: skills.length, name }));
    } catch (e) {
      status(e.message, "err", 6000);
    }
    render();
  }

  // a group of the user's let go, or only the skills named out of it
  async function ungroup(name, names) {
    try {
      const v = await api("library/skills/ungroup", { name, names: names || [] });
      take(v);
      if (names) for (const n of names) picked.delete(n);
      report(v.result, names ? t("{n} skills are out of {name}", { n: names.length, name }) : t("{name} is no longer a group", { name }));
    } catch (e) {
      status(e.message, "err", 6000);
    }
    render();
  }

  // a group's name, made a box to type its new one in
  function renameGroup(g, name) {
    const f = el("input", "lib-filter lib-grouprename-in");
    f.value = g.mine;
    f.spellcheck = false;
    f.autocomplete = "off";
    let done = false;
    const save = async () => {
      if (done) return;
      done = true;
      const to = f.value.trim();
      if (!to || to === g.mine) { render(); return; }
      try {
        const v = await api("library/skills/group", { old: g.mine, name: to, names: [] });
        take(v);
        if (g.key in folds) { folds["my:" + to] = folds[g.key]; delete folds[g.key]; saveFolds(); }
        report(v.result, t("{old} is now {name}", { old: g.mine, name: to }));
      } catch (e) {
        status(e.message, "err", 6000);
      }
      render();
    };
    f.onclick = (e) => e.stopPropagation();
    f.onkeydown = (e) => {
      e.stopPropagation();
      if (e.key === "Enter") { e.preventDefault(); save(); }
      if (e.key === "Escape") { e.preventDefault(); done = true; render(); }
    };
    f.onblur = save;
    name.replaceWith(f);
    f.focus({ preventScroll: true });
    f.select();
  }

  // ---------- a long list, drawn near the view first ----------

  // A list of items of known heights ({ key, h, make }) drawn in two goes:
  // the rows near the page's view at once, with room kept above and below
  // for the rest, and then the rest while the page is idle, a few rows at
  // a time, each kept once drawn. A scroll builds nothing and takes nothing
  // away, so it only paints what's there — unless it outruns the filling,
  // when what's left is drawn at once rather than a view a frame. A row
  // still wanted after a change (a filter, a fold) is kept as it is.
  const lists = new Set();
  let listFrame = 0;
  function syncLists() {
    cancelAnimationFrame(listFrame);
    listFrame = 0;
    if (page.hidden) return;
    let vp = null;
    for (const w of lists) {
      if (!w.el.isConnected) { if (w.shown) lists.delete(w); continue; } // its page is gone; one drawn again comes back
      if (!w.full()) w.sync(vp ||= page.getBoundingClientRect());
    }
  }
  const queueLists = () => { if (!listFrame) listFrame = requestAnimationFrame(syncLists); };
  // and while the page scrolls, its lists say so (library.css, .scrolling)
  let still = 0;
  const settled = () => { still = 0; for (const w of lists) w.el.classList.remove("scrolling"); };
  page.addEventListener("scroll", () => {
    queueLists();
    if (!still) for (const w of lists) w.el.classList.add("scrolling");
    clearTimeout(still);
    still = setTimeout(settled, 150);
  }, { passive: true });
  window.addEventListener("resize", queueLists);

  // The filling: one list's next few rows per idle moment, the lists in turn.
  const FILL = 20;
  const filling = new Set();
  let fillTask = 0;
  const idle = window.requestIdleCallback
    ? (f) => requestIdleCallback(f, { timeout: 200 })
    : (f) => setTimeout(f, 24);
  function queueFill(w) {
    filling.add(w);
    if (!fillTask) fillTask = idle(fillSome);
  }
  function fillSome() {
    fillTask = 0;
    for (const w of filling) {
      if (!w.el.isConnected || w.full()) { filling.delete(w); continue; }
      w.grow(FILL);
      if (w.full()) filling.delete(w);
      break;
    }
    if (filling.size) fillTask = idle(fillSome);
  }

  function windowed() {
    const box = el("div", "lib-vl");
    const above = el("div", "lib-vpad"), below = el("div", "lib-vpad");
    box.append(above, below);
    let items = [], tops = [0], from = 0, to = 0;
    let live = new Map(); // key → its row, for the items from…to
    const w = { el: box, shown: false };
    // the first item whose bottom is below y
    const at = (y) => {
      let lo = 0, hi = items.length;
      while (lo < hi) { const mid = (lo + hi) >> 1; if (tops[mid + 1] > y) hi = mid; else lo = mid + 1; }
      return lo;
    };
    const node = (i) => {
      const it = items[i];
      let n = live.get(it.key);
      if (!n) n = it.make();
      n.classList.toggle("first", i === 0);
      return n;
    };
    const pads = () => {
      above.style.height = tops[from] + "px";
      below.style.height = tops[items.length] - tops[to] + "px";
    };
    // draw items a…b around those drawn (a ≤ from, b ≥ to), or afresh,
    // keeping the rows of those already drawn
    function show(a, b, fresh) {
      if (!fresh && a === from && b === to) return;
      if (!fresh && a <= from && b >= to && from < to) {
        if (a < from) {
          const f = document.createDocumentFragment();
          const made = [];
          for (let i = a; i < from; i++) { const n = node(i); made.push([items[i].key, n]); f.append(n); }
          above.after(f);
          for (const [k, n] of made) live.set(k, n);
        }
        if (b > to) {
          const f = document.createDocumentFragment();
          for (let i = to; i < b; i++) { const n = node(i); live.set(items[i].key, n); f.append(n); }
          below.before(f);
        }
      } else {
        const next = new Map(), rows = [];
        for (let i = a; i < b; i++) { const n = node(i); next.set(items[i].key, n); rows.push(n); }
        for (const [k, n] of live) if (next.get(k) !== n) n.remove();
        live = next;
        // rows kept stay where they are; the others go in between them
        let prev = above;
        for (const n of rows) {
          if (prev.nextSibling !== n) prev.after(n);
          prev = n;
        }
      }
      from = a; to = b;
      pads();
    }
    w.full = () => from === 0 && to === items.length;
    // n more rows, below what's drawn first, then above it
    w.grow = (n) => {
      const b = Math.min(items.length, to + n);
      show(Math.max(0, from - (n - (b - to))), b);
    };
    w.set = (next) => {
      items = next;
      tops = [0];
      for (const it of items) tops.push(tops[tops.length - 1] + it.h);
      // placed, what's near the view; until then the first rows, enough for one
      if (box.isConnected && !page.hidden) w.sync(page.getBoundingClientRect(), true);
      else show(0, Math.min(items.length, 12), true);
      if (!w.full()) queueFill(w);
    };
    w.sync = (vp, fresh) => {
      w.shown = true;
      lists.add(w);
      const r = box.getBoundingClientRect();
      const total = tops[items.length];
      if (!total) { show(0, 0, fresh); return; }
      const k = r.height ? r.height / total : 1; // the page may be zoomed
      const view = vp.height / k;
      const top = (vp.top - r.top) / k, bottom = (vp.bottom - r.top) / k;
      // the rows in view and half a view about it
      const a = top - view / 2 > total ? items.length : at(Math.max(0, top - view / 2));
      const b = bottom + view / 2 < 0 ? 0 : Math.min(items.length, at(bottom + view / 2) + 1);
      if (fresh) { show(a, Math.max(a, b), true); return; }
      // a scroll past what's filled in: the rest, now
      if (from === to || (a < b && (a < from || b > to))) show(0, items.length, from === to);
    };
    lists.add(w);
    return w;
  }

  // ---------- projects ----------

  // A project is a folder of the user's whose agents get some of the
  // library's skills there, as the project's own: linked (or copied) into
  // the folder each agent reads a project's skills from, and kept out of
  // git in the project's .gitignore. Only what magpie placed is taken away.
  // Its MCP servers are written into the file each agent reads a project's
  // servers from (.mcp.json, .codex/config.toml…), beside what's there; the
  // MCP tab lists the same projects with the servers.
  let addingProject = null;       // the folder being added: { dir, error, busy }
  const openProjects = new Set(); // projects whose skills (or servers) are shown
  const projectAgents = () => shownAgents().filter((a) => a.projectSkills);
  // a project's file may take less than the agent's own: Pi's .pi/mcp.json
  // no SSE, whatever extension its user-wide file is for
  const projectMCPAgents = () => shownAgents().filter((a) => a.projectMCP).map((a) => (a.projectNoSSE ? { ...a, noSSE: true } : a));
  const projectFiles = () => [...new Set(projectMCPAgents().map((a) => a.projectMCP))].join(", ");

  function renderProjects(body, kind) {
    const rh = el("div", "row-head");
    rh.append(el("span", "label", t("In projects")), el("span", "grow"));
    if (!addingProject) rh.append(button(t("Add a project"), "action lib-updall", () => { addingProject = { dir: "" }; render(); }));
    body.append(rh);
    if (addingProject) body.append(addProjectCard(kind));
    if (!lib.projects.length && !addingProject) {
      body.append(el("p", "lib-aside", kind === "mcp"
        ? t("Give a project's agents some of these servers as the project's own: magpie writes them into its {files}, leaving the rest of each file as it is.", { files: projectFiles() })
        : t("Give a project's agents some of these skills as the project's own: magpie links them into its .claude/skills and .agents/skills and keeps them out of git.")));
      return;
    }
    for (const p of lib.projects) body.append(projectCard(p, kind));
  }

  function addProjectCard(kind) {
    const a = addingProject;
    const card = el("div", "list lib-card lib-install");
    const line = el("div", "lib-find");
    const inp = el("input");
    inp.type = "text";
    inp.dataset.lib = "project";
    inp.placeholder = t("The project's folder, like ~/code/app");
    inp.spellcheck = false;
    inp.autocomplete = "off";
    inp.value = a.dir;
    inp.oninput = () => { a.dir = inp.value; };
    inp.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") add(); else if (e.key === "Escape") { addingProject = null; render(); } };
    line.append(glyph(GLYPH.folder, "lib-mini"), inp);
    // the app has the system's folder picker; a browser tab only the path
    if (!web) {
      line.append(button(t("Choose…"), "", async () => {
        try {
          const r = await api("library/projects/choose", {});
          if (r.dir) { a.dir = r.dir; a.error = ""; render(); }
        } catch (e) { status(e.message, "err", 6000); }
      }));
    }
    const go = button(a.busy ? t("Adding…") : t("Add"), "action", () => add());
    go.disabled = !!a.busy;
    line.append(go, button(t("Cancel"), "", () => { addingProject = null; render(); }));
    card.append(line);
    if (a.error) card.append(el("div", "lib-err", a.error));
    async function add() {
      const dir = a.dir.trim();
      if (!dir || a.busy) return;
      a.busy = true;
      render();
      const was = new Set(lib.projects.map((x) => x.dir));
      try {
        take(await api("library/projects/add", { dir }));
        addingProject = null;
        const p = lib.projects.find((x) => !was.has(x.dir));
        if (p) openProjects.add(p.dir);
        status(kind === "mcp" ? t("{name} added — pick the servers it gets", { name: p?.name || dir }) : t("{name} added — pick the skills it gets", { name: p?.name || dir }), "ok");
      } catch (e) {
        a.busy = false;
        a.error = e.message;
      }
      render();
    }
    return card;
  }

  function projectCard(p, kind) {
    const mcp = kind === "mcp";
    const card = el("div", "list lib-card lib-project" + (p.missing ? " missing" : ""));
    const isOpen = openProjects.has(p.dir);
    const head = el("div", "row lib-row click lib-projhead" + (isOpen ? " open" : ""));
    const who = el("div", "who");
    const nm = el("div", "name", p.name);
    if (p.missing) nm.append(tag(t("Folder is gone"), "warn"));
    else if (p.copy && !mcp) nm.append(tag(t("Copies"), ""));
    who.append(nm);
    const src = el("div", "lib-src");
    src.append(pathLink(p.dir));
    who.append(src);
    const names = Object.keys((mcp ? p.servers : p.skills) || {}).sort();
    const pills = el("div", "lib-tags");
    if (!isOpen) {
      for (const n of names.slice(0, 4)) pills.append(tag(n, "lib-dot"));
      if (names.length > 4) pills.append(tag("+" + (names.length - 4), ""));
      if (!names.length) pills.append(tag(mcp ? t("No servers yet") : t("No skills yet"), "lib-unchecked"));
    }
    const acts = el("div", "lib-rowacts");
    const rm = button("", "lib-icon danger", () => confirmRemoveProject(p));
    rm.append(svg(GLYPH.trash, 13, 1.4));
    rm.title = t("Remove the project");
    acts.append(rm);
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    head.append(glyph(GLYPH.folder), who, pills, acts, chev);
    head.onclick = () => { isOpen ? openProjects.delete(p.dir) : openProjects.add(p.dir); render(); };
    head.title = mcp ? (isOpen ? t("Hide its servers") : t("Pick the servers it gets")) : isOpen ? t("Hide its skills") : t("Pick the skills it gets");
    card.append(head);
    if (p.problems?.[""]) card.append(el("div", "lib-err", p.problems[""]));
    if (!isOpen) return card;
    if (mcp) return projectServers(p, card);

    const opts = el("div", "lib-projopts");
    const copy = toggle(p.copy, t("Copy files instead of linking"), (on) =>
      change("projects/copy", { dir: p.dir, copy: on }, on ? t("{name} gets copies now", { name: p.name }) : t("{name} gets links now", { name: p.name })));
    const lab = el("label", "lib-projcopy");
    const words = el("span", "", t("Copy files instead of linking"));
    words.onclick = (e) => { e.preventDefault(); copy.click(); };
    lab.append(copy, words);
    opts.append(lab, el("span", "note", p.copy
      ? t("Copies are made again whenever the library's skill changes; edits made to them are replaced.")
      : t("Links follow the library's skill: an update reaches the project at once.")));
    card.append(opts);

    const all = projectAgents();
    for (const s of lib.skills) {
      const row = el("div", "row lib-row lib-projskill");
      const w = el("div", "who");
      const n = el("div", "name", s.name);
      const problem = p.problems?.[s.name];
      if (problem) n.append(tag(t("Not placed"), "warn", problem));
      w.append(n);
      const sub = el("div", "sub", problem || s.description || "");
      sub.title = problem || s.description || "";
      w.append(sub);
      const on = p.skills[s.name] || [];
      // an agent reading the same folder as one that has it has it too
      const via = (a) => { const o = on.find((id) => agentOf(id)?.projectSkills === a.projectSkills); return o ? nameOf(o) : ""; };
      row.append(mark(s.icon, GLYPH.skill), w, agentChips(all, on, projectChange("projects/skill", p.dir, s.name, (next) => next.length
        ? t("{skill} is in {name} for {agents}", { skill: s.name, name: p.name, agents: next.map(nameOf).join(", ") })
        : t("{skill} is out of {name}", { skill: s.name, name: p.name })), { via }));
      card.append(row);
    }
    const dirs = [...new Set(all.map((a) => a.projectSkills))];
    card.append(el("p", "lib-aside lib-projfoot", t("Placed in {dirs} of the project and listed in its .gitignore. A folder there that isn't magpie's is left as it is.", { dirs: dirs.join(", ") })));
    const skip = shownAgents().filter((a) => a.skills && !a.projectSkills);
    if (skip.length) card.append(el("p", "lib-aside lib-projfoot", t("{agents} reads no project skills folder magpie knows of.", { agents: skip.map((a) => a.name).join(", ") })));
    return card;
  }

  // A project row's chip: lit at once, so a second click before magpie has
  // answered the first counts from it; the clicks are sent one at a time,
  // the last of them winning, and the page drawn again once they're in.
  const projectWriting = new Map();
  function projectChange(path, dir, name, done) {
    return async (next, c) => {
      const on = next.includes(c.dataset.agent);
      c.classList.toggle("on", on);
      if (on) c.classList.remove("via");
      c.setAttribute("aria-pressed", on ? "true" : "false");
      const key = path + "\n" + dir + "\n" + name;
      const w = projectWriting.get(key);
      if (w) { w.want = next; return; }
      const me = { want: next };
      projectWriting.set(key, me);
      let sent = null;
      const same = () => [...sent].sort().join() === [...me.want].sort().join();
      try {
        while (!sent || !same()) {
          sent = me.want;
          const v = await api("library/" + path, { dir, name, agents: sent });
          take(v);
          if (same()) report(v.result, done(sent));
        }
      } catch (e) {
        status(e.message, "err", 6000);
        await api("library").then(take, () => {});
      }
      projectWriting.delete(key);
      render();
    };
  }

  // The project's card, open on the MCP tab: each library server with the
  // chips of the agents that read a project's own servers.
  function projectServers(p, card) {
    const all = projectMCPAgents();
    for (const s of lib.servers) {
      const row = el("div", "row lib-row lib-projskill lib-projserver");
      const w = el("div", "who");
      const n = el("div", "name mono", s.name);
      const problem = p.problems?.["mcp:" + s.name];
      if (problem) n.append(tag(t("Not written"), "warn", problem));
      w.append(n);
      const sub = el("div", "sub mono", problem || serverLine(s));
      sub.title = problem || serverLine(s);
      w.append(sub);
      row.append(mark(s.icon, s.transport === "stdio" ? GLYPH.cmd : GLYPH.web), w, agentChips(all, p.servers?.[s.name] || [], projectChange("projects/server", p.dir, s.name, (next) => next.length
        ? t("{server} is in {name} for {agents}", { server: s.name, name: p.name, agents: next.map(nameOf).join(", ") })
        : t("{server} is out of {name}", { server: s.name, name: p.name })), { blocked: sseBlocked(s) }));
      card.append(row);
    }
    if (!lib.servers.length) card.append(el("p", "lib-aside lib-projfoot", t("No servers in the library yet")));
    card.append(el("p", "lib-aside lib-projfoot", t("Written into {files} of the project, beside what's in them; a file magpie makes is listed in its .gitignore. A server there by the same name that isn't magpie's is left as it is.", { files: projectFiles() })));
    card.append(el("p", "lib-aside lib-projfoot", t("Claude Code asks before it starts a project's servers; Codex reads .codex/config.toml, and Pi .pi/mcp.json, only in a project you trust.")));
    const skip = shownAgents().filter((a) => a.mcp && !a.projectMCP);
    if (skip.length) card.append(el("p", "lib-aside lib-projfoot", t("{agents} reads no project MCP file magpie knows of.", { agents: skip.map((a) => a.name).join(", ") })));
    return card;
  }

  function confirmRemoveProject(p) {
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", t("Remove {name}?", { name: p.name })));
    ed.append(head);
    const say = el("p", "lib-confirm");
    ed.append(say);
    // #514: skills or servers given to a project once are often to stay
    // there; magpie can forget the project and leave them as they are
    const has = p.placed?.length || Object.values(p.wrote || {}).some((x) => x.length);
    const keep = el("input");
    keep.type = "checkbox";
    const tell = () => {
      say.textContent = keep.checked
        ? t("magpie forgets the project. The skills and MCP servers it put in the folder stay there as they are, with their lines in its .gitignore, and are yours from then on; a linked skill still follows the library's.")
        : t("The skills and MCP servers magpie put in it are taken away, with their lines in its .gitignore. Nothing else in the folder is touched.");
    };
    keep.onchange = tell;
    if (has) {
      const r = el("label", "lib-pick lib-keep");
      const w = el("span", "who");
      w.append(el("span", "name", t("Keep its skills and MCP servers")), el("span", "sub", t("Remove the project from magpie only, leaving what magpie wrote into it")));
      r.append(keep, w);
      ed.append(r);
    }
    tell();
    const bar = el("div", "bar");
    const go = button(t("Remove"), "primary danger-fill", async () => {
      const body = keep.checked ? { dir: p.dir, keep: true } : { dir: p.dir };
      if (await change("projects/remove", body, t(keep.checked ? "{name} removed, its skills and servers kept" : "{name} removed", { name: p.name }))) { openProjects.delete(p.dir); closeLibModal(true); }
    });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  // Every skill from GitHub, fetched again; the ones that couldn't be are
  // said, and the rest are updated all the same.
  // names, when given, are the skills to update: those a check found GitHub
  // to have changed.
  async function updateAllSkills(names) {
    try {
      const v = await api(names ? "library/skills/update-some" : "library/skills/update-all", names ? { names } : {});
      take(v);
      const res = v.result || {};
      const up = res.updated?.length || 0, no = res.unupdated || [];
      if (no.length) {
        const p = no[0];
        status(t("{name} wasn't updated: {error}", { name: p.what.replace(/^skill:/, ""), error: p.error }) + (no.length > 1 ? " " + t("(and {n} more)", { n: no.length - 1 }) : "") + (up ? " · " + t("{n} up to date", { n: up }) : ""), "warn", 8000);
      } else report(res, t("{n} skills up to date", { n: up }));
      render();
    } catch (e) {
      status(e.message, "err", 6000);
    }
  }

  // Every skill found in the agents brought in at once: one that couldn't be
  // is said, and the others are brought in all the same.
  async function importAllSkills(names) {
    try {
      const v = await api("library/skills/import-all", { names });
      take(v);
      const res = v.result || {}, no = res.unimported || [];
      if (no.length) {
        const p = no[0];
        status(t("{name} wasn't brought in: {error}", { name: p.what.replace(/^skill:/, ""), error: p.error }) + (no.length > 1 ? " " + t("(and {n} more)", { n: no.length - 1 }) : ""), "warn", 8000);
      } else report(res, t("{n} skills are in the library now", { n: names.length }));
      render();
    } catch (e) {
      status(e.message, "err", 6000);
    }
  }

  // Every skill on, or off, for every agent shown that can take skills, in
  // one write rather than a row's All for each (#443); an agent not shown
  // keeps what it has. Off asks first, in the page.
  // How the agents are given their skills (#896): links to the library's
  // folder, which an update reaches at once, or folders of their own, made
  // again when the library's skill changes. By agent gives one agent its
  // own way; Claude Desktop and an agent in WSL only ever take copies.
  function skillHowBar() {
    const bar = el("div", "lib-skillhow");
    bar.id = "lib-skillhow";
    const how = lib.copySkills ? "copy" : "link";
    bar.append(el("span", "label", t("Give skills as")), segs([["link", t("Links")], ["copy", t("Copies")]], how, (h) => {
      if (h !== how) change("skills/how", { agent: "", how: h }, h === "copy" ? t("The agents get copies of their skills now") : t("The agents get links to their skills now"));
    }));
    const own = skillAgents().filter((a) => a.howOwn).length;
    const by = button(own ? t("By agent ({n})", { n: own }) : t("By agent"), "action lib-updall lib-howagent", () => openSkillHow());
    by.title = t("Give an agent its skills its own way");
    bar.append(by, el("span", "note", how === "copy"
      ? t("Each agent gets a folder of its own, made again when the library's skill changes; edits made in a copy are replaced.")
      : t("Links follow the library's skill: an update reaches the agents at once.")));
    return bar;
  }

  function openSkillHow() {
    const ed = el("div", "editor lib-editor lib-byagent-sheet lib-howsheet");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.skill), el("b", "", t("How each agent gets its skills")));
    ed.append(head);
    ed.append(el("p", "lib-confirm", lib.copySkills
      ? t("An agent can have its own way; the others get copies, as the library gives them.")
      : t("An agent can have its own way; the others get links, as the library gives them.")));
    const list = el("div", "list lib-list lib-byagent-list");
    let busy = false;
    const draw = () => list.replaceChildren(...lib.agents.filter((a) => a.skills && (!isHidden(a) || a.howOwn)).map((a) => {
      const row = el("div", "row lib-row lib-byagent-row lib-how-row");
      row.dataset.agent = a.id;
      const who = el("div", "who");
      who.append(el("div", "name", a.name), el("div", "sub", a.mustCopy ? t("Only ever takes copies") : a.how === "copy" ? t("Gets copies") : t("Gets links")));
      row.append(agentIcon(a.icon), who);
      if (!a.mustCopy) {
        row.append(segs([["", t("Library's way")], ["link", t("Links")], ["copy", t("Copies")]], a.howOwn ? a.how : "", async (h) => {
          if (busy) return;
          busy = true;
          try {
            const v = await api("library/skills/how", { agent: a.id, how: h });
            take(v);
            const now = agentOf(a.id);
            report(v.result, now?.how === "copy" ? t("{agent} gets copies of its skills now", { agent: a.name }) : t("{agent} gets links to its skills now", { agent: a.name }));
            render();
          } catch (e) {
            status(e.message, "err", 6000);
          }
          busy = false;
          if (list.isConnected) { draw(); markModalSaved(); }
        }));
      }
      return row;
    }));
    draw();
    ed.append(list);
    const bar = el("div", "bar");
    const done = button(t("Done"), "primary", closeLibModal);
    bar.append(el("span", "grow"), done);
    ed.append(bar);
    modal = { save: () => done.click() };
    openLib(ed);
  }

  // Every skill, or every server (#1027), on or off for every agent shown
  // at once; an agent not shown keeps what it has, and a server goes only
  // to the agents that can reach it, as its All chip gives it.
  function everyButtons(kind, all) {
    const ids = all.map((a) => a.id), n = all.length, skills = kind === "skills";
    const items = skills ? lib.skills : lib.servers;
    const can = (x, a) => skills || reaches(x)(a);
    const on = button(t("Turn all on"), "action lib-updall lib-everyon", () => everyOf(kind, ids, true));
    on.title = skills ? t("Give every skill to all {n} agents that can take skills", { n }) : t("Give every server to all {n} agents, each the ones it can reach", { n });
    on.disabled = items.every((x) => all.every((a) => !can(x, a) || x.agents?.includes(a.id)));
    const off = button(t("Turn all off"), "action lib-updall lib-everyoff", () => confirmEveryOff(kind, ids));
    off.title = skills ? t("Take every skill from all {n} agents", { n }) : t("Take every server from all {n} agents", { n });
    off.disabled = !items.some((x) => ids.some((id) => x.agents?.includes(id)));
    return [on, off];
  }

  function confirmEveryOff(kind, ids) {
    const skills = kind === "skills", n = (skills ? lib.skills : lib.servers).length, agents = ids.map(nameOf).join(", ");
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(skills ? GLYPH.skill : GLYPH.cmd), el("b", "", skills ? t("Turn off all {n} skills?", { n }) : t("Turn off all {n} servers?", { n })));
    ed.append(head);
    ed.append(el("p", "lib-confirm", skills
      ? t("Every skill is taken out of {agents}. They stay in the library, to turn on again.", { agents })
      : t("Every server is taken out of {agents}. They stay in the library, to turn on again.", { agents })));
    const bar = el("div", "bar");
    const go = button(t("Turn all off"), "primary danger-fill", async () => { go.disabled = true; if (await everyOf(kind, ids, false)) closeLibModal(true); else go.disabled = false; });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  async function everyOf(kind, ids, on) {
    try {
      const v = await api("library/" + kind + "/agents-all", { agents: ids, on });
      take(v);
      const m = ids.length;
      if (kind === "skills") {
        const n = lib.skills.length;
        report(v.result, on ? t("{n} skills are on for all {m} agents", { n, m }) : t("{n} skills are off for all {m} agents", { n, m }));
      } else {
        const n = lib.servers.length;
        report(v.result, on ? t("{n} servers are on for all {m} agents", { n, m }) : t("{n} servers are off for all {m} agents", { n, m }));
      }
      render();
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      return false;
    }
  }

  // One agent's skills, or servers, on or off at once (#475), rather than a
  // chip on every row: the agents that can take them, each with how many it
  // has, a button that gives it every one it can take and one that takes
  // every one from it. Every other agent keeps what it has. An agent hidden
  // on the Agents page (#71) has no chips, so it's listed here while it has
  // any, to take them out of it; it isn't given more.
  function byAgentButton(kind) {
    const b = button(t("By agent"), "action lib-updall lib-byagent", () => openByAgent(kind));
    b.title = kind === "skills" ? t("Turn every skill on or off for one agent") : t("Turn every server on or off for one agent");
    return b;
  }

  // kept: agents listed when the sheet opened, which stay listed in it once
  // a hidden one has nothing left
  function byAgentRows(kind, kept) {
    const items = kind === "skills" ? lib.skills : lib.servers;
    const can = kind === "skills" ? () => true : (s, a) => reaches(s)(a);
    return lib.agents.filter((a) => kind === "skills" ? a.skills : a.mcp).map((a) => {
      const has = items.filter((x) => x.agents?.includes(a.id)).length;
      const missing = items.filter((x) => can(x, a) && !x.agents?.includes(a.id)).length;
      return { a, has, missing, hidden: isHidden(a) };
    }).filter((r) => !r.hidden || r.has || kept?.has(r.a.id));
  }

  function openByAgent(kind) {
    const skills = kind === "skills", items = () => skills ? lib.skills : lib.servers;
    const ed = el("div", "editor lib-editor lib-byagent-sheet");
    const head = el("div", "ehead");
    head.append(glyph(skills ? GLYPH.skill : GLYPH.cmd), el("b", "", skills ? t("Skills by agent") : t("MCP servers by agent")));
    ed.append(head);
    ed.append(el("p", "lib-confirm", skills
      ? t("Every skill on or off for one agent; the others keep theirs. Turned off, they stay in the library.")
      : t("Every server on or off for one agent; the others keep theirs. Turned off, they stay in the library.")));
    const list = el("div", "list lib-list lib-byagent-list");
    let busy = false;
    const listed = new Set(byAgentRows(kind).map((r) => r.a.id));
    const draw = () => list.replaceChildren(...byAgentRows(kind, listed).map(({ a, has, missing, hidden }) => {
      const row = el("div", "row lib-row lib-byagent-row");
      row.dataset.agent = a.id;
      const who = el("div", "who");
      const name = el("div", "name", a.name);
      if (hidden) name.append(tag(t("Hidden"), "", t("Hidden on the Agents page: it isn't given anything new, and what it has can be taken out here")));
      who.append(name, el("div", "sub", skills ? t("{n} of {m} skills", { n: has, m: items().length }) : t("{n} of {m} servers", { n: has, m: items().length })));
      const on = button(t("Turn all on"), "action lib-updall lib-agenton", (e, b) => everyFor(a, true, b));
      on.disabled = busy || hidden || !missing;
      const off = button(t("Turn all off"), "action lib-updall lib-agentoff", (e, b) => everyFor(a, false, b));
      off.disabled = busy || !has;
      on.title = skills ? t("Give {agent} every skill", { agent: a.name }) : t("Give {agent} every server it can reach", { agent: a.name });
      off.title = skills ? t("Take every skill from {agent}", { agent: a.name }) : t("Take every server from {agent}", { agent: a.name });
      row.append(agentIcon(a.icon), who, on, off);
      return row;
    }));
    // what each button wrote is what the list then shows; the page under
    // it is drawn again too, its chips with it
    async function everyFor(a, on, b) {
      busy = true;
      for (const x of list.querySelectorAll("button")) x.disabled = true;
      b.classList.add("busy");
      try {
        const v = await api("library/" + (skills ? "skills" : "servers") + "/agents-all", { agents: [a.id], on });
        take(v);
        const n = items().filter((x) => x.agents?.includes(a.id)).length;
        report(v.result, skills
          ? (on ? t(n === 1 ? "1 skill is on for {agent}" : "{n} skills are on for {agent}", { n, agent: a.name }) : t("Every skill is off for {agent}", { agent: a.name }))
          : (on ? t(n === 1 ? "1 server is on for {agent}" : "{n} servers are on for {agent}", { n, agent: a.name }) : t("Every server is off for {agent}", { agent: a.name })));
        render();
      } catch (e) {
        status(e.message, "err", 6000);
      }
      busy = false;
      if (list.isConnected) draw();
    }
    draw();
    ed.append(list);
    const bar = el("div", "bar");
    const done = button(t("Done"), "primary", closeLibModal);
    bar.append(el("span", "grow"), done);
    ed.append(bar);
    modal = { save: () => done.click() };
    openLib(ed);
  }

  // Every skill out of the library at once (#449), each as a row's Remove
  // takes it: out of every agent and project, its folder moved to magpie's
  // backups, or for one linked from a folder of the user's only the link
  // taken away. Every server likewise (#1027): out of every agent and
  // project magpie gave it to, its sign-in forgotten; the agents' servers
  // of their own stay. It asks first, in the page, saying who loses what.
  function removeEveryButton(kind) {
    const skills = kind === "skills";
    const b = button(t("Remove all"), "action danger lib-updall lib-everyrm", () => skills ? confirmRemoveEverySkill() : confirmRemoveEveryServer());
    b.title = skills ? t("Take all {n} skills out of the library", { n: lib.skills.length }) : t("Take all {n} servers out of the library", { n: lib.servers.length });
    return b;
  }

  // the sheet asking first: its head and what it says, then Remove all n
  function confirmRemoveEvery(kind, names, title, lines) {
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", title));
    ed.append(head);
    for (const l of lines) if (l) ed.append(el("p", "lib-confirm", l));
    const bar = el("div", "bar");
    const go = button(t("Remove all {n}", { n: names.length }), "primary danger-fill", async () => { go.disabled = true; if (await removeEvery(kind, names)) closeLibModal(true); else go.disabled = false; });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }
  const takenFrom = (items) => {
    const agents = [...new Set(items.flatMap((s) => s.agents || []))];
    return agents.length
      ? t("They are taken out of the library and out of {agents}.", { agents: agents.map(nameOf).join(", ") })
      : t("They are taken out of the library; no agent has any of them.");
  };

  function confirmRemoveEverySkill() {
    const skills = lib.skills.slice(), names = skills.map((s) => s.name), n = names.length;
    const linked = skills.filter((s) => s.kind === "folder").length, kept = n - linked;
    const projects = (lib.projects || []).filter((p) => names.some((x) => p.skills?.[x]?.length)).length;
    const where = [];
    if (kept) where.push(t("{n} folders are moved to magpie's backups (Backups, at the foot of the Library), not deleted.", { n: kept }));
    if (linked) where.push(t("{n} linked from folders of your own are only unlinked: those folders stay where they are.", { n: linked }));
    confirmRemoveEvery("skills", names, t("Remove all {n} skills?", { n }), [
      takenFrom(skills),
      projects && t("The skills magpie placed in {n} projects are taken away too.", { n: projects }),
      where.join(" "),
    ]);
  }

  function confirmRemoveEveryServer() {
    const servers = lib.servers.slice(), names = servers.map((s) => s.name), n = names.length;
    const projects = (lib.projects || []).filter((p) => names.some((x) => p.servers?.[x]?.length)).length;
    const signed = servers.filter((s) => s.signIn?.signedIn).length;
    confirmRemoveEvery("servers", names, t("Remove all {n} servers?", { n }), [
      takenFrom(servers),
      projects && t("The servers magpie placed in {n} projects are taken away too.", { n: projects }),
      signed && t("magpie's sign-in to {n} of them is forgotten too.", { n: signed }),
      t("Servers your agents have that aren't in the library stay as they are."),
    ]);
  }

  async function removeEvery(kind, names) {
    try {
      const v = await api("library/" + kind + "/remove-all", { names });
      take(v);
      const res = v.result || {}, no = res.unremoved || [];
      if (no.length) {
        const p = no[0];
        status(t("{name} wasn't removed: {error}", { name: p.what.replace(/^(skill|mcp):/, ""), error: p.error }) + (no.length > 1 ? " " + t("(and {n} more)", { n: no.length - 1 }) : ""), "warn", 8000);
      } else report(res, kind === "skills" ? t("{n} skills removed", { n: names.length }) : t("{n} servers removed", { n: names.length }));
      render();
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      return false;
    }
  }

  // Which skills GitHub changed since they were installed: each row says,
  // and the heading offers to update just those.
  async function checkSkills() {
    if (checking) return;
    checking = true;
    render();
    try {
      take(await api("library/skills/check", {}));
      const got = lib.skills.filter((s) => s.check);
      const n = got.filter((s) => s.check.status === "update").length;
      const unknown = got.filter((s) => s.check.status === "unknown");
      let msg = n ? (n === 1 ? t("1 skill has an update") : t("{n} skills have updates", { n })) : t("Every skill is up to date");
      const more = lib.newSkills?.length || 0;
      if (more) msg += " · " + (more === 1 ? t("1 more skill in their repositories") : t("{n} more skills in their repositories", { n: more }));
      if (unknown.length) {
        msg += " · " + t("{n} couldn't be checked: {error}", { n: unknown.length, error: checkError(unknown[0].check) });
        status(msg, "warn", 8000);
      } else status(msg, "ok");
    } catch (e) {
      status(e.message, "err", 6000);
    }
    checking = false;
    // drawn after the await, a page that can't be drawn says why, as one
    // loaded does, not left blank without a word (#1217)
    try { render(); } catch (e) { status(e.message, "err"); }
  }

  // why a skill couldn't be checked: GitHub's rate limit used up said in
  // the page's words, with when it lifts and how a token raises it
  function checkError(c) {
    const l = c.limited;
    if (!l) return t(c.error);
    const d = l.until ? new Date(l.until) : null;
    const time = d && !isNaN(d) ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "";
    if (l.token) return time ? t("GitHub's rate limit for your GitHub token is used up until {time}", { time })
      : t("GitHub's rate limit for your GitHub token is used up for now");
    return time ? t("GitHub allows 60 requests an hour without a token, used up until {time}. Add a GitHub token in Settings → Network and sharing to raise it to 5,000.", { time })
      : t("GitHub allows 60 requests an hour without a token, used up for now. Add a GitHub token in Settings → Network and sharing to raise it to 5,000.");
  }

  // what a check found of a skill, for a tooltip: the last commit to it
  function checkLine(c) {
    const when = c.date ? new Date(c.date).toLocaleDateString() : "";
    return [c.message, [when, c.commit?.slice(0, 7)].filter(Boolean).join(" · ")].filter(Boolean).join("\n");
  }

  // Claude Code's skills are OpenCode's and Crush's too: a chip for one of
  // those says so while it has none of its own.
  // the agents that have a skill kept in ~/.agents/skills whatever is
  // ticked: they read that folder themselves (#595)
  function alwaysFor(s) {
    return (a) => s.always?.includes(a.id) ? t("{agent} reads ~/.agents/skills itself, where this skill is kept — it has it whatever is ticked here", { agent: a.name }) : "";
  }

  function viaFor(s) {
    return (a) => {
      const also = lib.agents.find((x) => x.id === a.id)?.skillsAlso || [];
      const other = also.find((id) => s.agents.includes(id));
      return other ? nameOf(other) : "";
    };
  }

  function installCard() {
    const card = el("div", "list lib-card lib-install");
    const line = el("div", "lib-find");
    const inp = el("input");
    inp.type = "text";
    inp.dataset.lib = "source";
    inp.placeholder = t("owner/repo, a GitHub link, or a folder on this computer");
    inp.spellcheck = false;
    inp.autocomplete = "off";
    inp.value = probe?.source || "";
    const go = button(probing ? t("Looking…") : t("Find skills"), "action", () => find());
    go.disabled = probing;
    if (probing) go.classList.add("busy");
    inp.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") find(); else if (e.key === "Escape" && probe) { probe = null; render(); } };
    const g = glyph(GLYPH.search, "lib-mini");
    line.append(g, inp, go);
    card.append(line);
    async function find() {
      const src = inp.value.trim();
      if (!src || probing) return;
      probing = true;
      probe = { source: src };
      render();
      try {
        const p = await api("library/skills/probe", { source: src });
        const fresh = p.candidates.filter((c) => !c.have);
        probe = { source: src, found: p, pick: new Set((fresh.length <= 3 ? fresh : []).map((c) => c.path)), agents: skillAgents().map((a) => a.id), filter: "" };
        if (!p.candidates.length) status(t("No SKILL.md found there"), "warn");
      } catch (e) {
        probe = { source: src, error: e.message };
      }
      probing = false;
      render();
    }
    if (probe?.error) card.append(el("div", "lib-err", probe.error));
    if (probe?.found) card.append(probeResult(probe));
    return card;
  }

  function probeResult(p) {
    const box = el("div", "lib-probe");
    const c = p.found.candidates;
    if (!c.length) { box.append(el("div", "lib-none", t("No skills there — a skill is a folder with a SKILL.md in it."))); return box; }
    const head = el("div", "lib-probehead");
    const src = p.found.kind === "github" ? "github.com/" + p.found.source.replace(/^https?:\/\/github\.com\//, "") : tilde(p.found.source);
    head.append(el("span", "note", c.length === 1 ? t("1 skill in {src}", { src }) : t("{n} skills in {src}", { n: c.length, src })));
    head.append(el("span", "grow"));
    const fresh = c.filter((x) => !x.have);
    if (fresh.length > 1) {
      const allOn = fresh.every((x) => p.pick.has(x.path));
      head.append(button(allOn ? t("Select none") : t("Select all"), "", () => { for (const x of fresh) allOn ? p.pick.delete(x.path) : p.pick.add(x.path); render(); }));
    }
    box.append(head);
    if (c.length > 8) {
      const f = el("input", "lib-filter");
      f.type = "text";
      f.dataset.lib = "filter";
      f.placeholder = t("Filter…");
      f.value = p.filter;
      f.oninput = () => { p.filter = f.value; drawList(); };
      f.onkeydown = (e) => e.stopPropagation();
      box.append(f);
    }
    const list = el("div", "lib-picks");
    const drawList = () => {
      list.replaceChildren();
      const q = p.filter.toLowerCase();
      for (const x of c) {
        if (q && !(x.name + " " + x.description).toLowerCase().includes(q)) continue;
        const r = el("label", "lib-pick" + (x.have ? " dim" : ""));
        const cb = el("input");
        cb.type = "checkbox";
        cb.checked = p.pick.has(x.path);
        cb.disabled = x.have;
        cb.onchange = () => { if (cb.checked) p.pick.add(x.path); else p.pick.delete(x.path); syncGo(); };
        const w = el("span", "who");
        w.append(el("span", "name", x.name), el("span", "sub", x.description || x.path || ""));
        r.append(cb, w);
        if (x.have) r.append(tag(t("in the library"), ""));
        list.append(r);
      }
    };
    drawList();
    box.append(list);
    const foot = el("div", "lib-probefoot");
    const agentsBox = el("div");
    const drawAgents = () => agentsBox.replaceChildren(agentChips(skillAgents(), p.agents, (n) => { p.agents = n; drawAgents(); }, { names: false }));
    drawAgents();
    foot.append(el("span", "note", t("for")), agentsBox, el("span", "grow"));
    foot.append(button(t("Cancel"), "", () => { probe = null; render(); }));
    const go = button("", "primary", async () => {
      go.disabled = true;
      go.textContent = t("Installing…");
      const n = p.pick.size;
      if (await change("skills/install", { source: p.source, paths: [...p.pick], agents: p.agents }, n === 1 ? t("1 skill installed") : t("{n} skills installed", { n }))) { probe = null; render(); }
      else { go.disabled = false; syncGo(); }
    });
    const syncGo = () => { const n = p.pick.size; go.textContent = n === 1 ? t("Install 1 skill") : t("Install {n} skills", { n }); go.disabled = !n; };
    syncGo();
    foot.append(go);
    box.append(foot);
    return box;
  }

  function skillRow(s, all) {
    const row = el("div", "row lib-row click lib-skill" + (s.missing ? " missing" : "") + (s.source ? " src" : ""));
    const who = el("div", "who");
    const nm = el("div", "name", s.name);
    const c = s.check;
    if (c?.status === "update") {
      const b = tag(t("Update available"), "lib-new", t("Changed on GitHub since it was installed") + "\n" + checkLine(c));
      if (c.url) {
        b.classList.add("link");
        b.onclick = (e) => { e.stopPropagation(); browse(c.url); };
      }
      nm.append(b);
    } else if (c?.status === "unknown") {
      nm.append(tag(t("Not checked"), "lib-unchecked", checkError(c)));
    }
    // a copy in an agent that differs from the library's skill (#896): a
    // sync makes it again
    if (s.behind?.length) {
      const b = tag(t("Copy out of date"), "lib-new link lib-behind", t("The copy in {agents} differs from the library's skill. Click to copy it again.", { agents: s.behind.map(nameOf).join(", ") }));
      b.setAttribute("role", "button");
      b.tabIndex = 0;
      b.onclick = (e) => { e.stopPropagation(); change("all/sync", {}, t("Copies updated")); };
      b.onkeydown = (e) => {
        if (e.key !== "Enter" && e.key !== " ") return;
        e.preventDefault(); e.stopPropagation(); b.click();
      };
      nm.append(b);
    }
    who.append(nm);
    const sub = el("div", "sub", s.missing ? t("Its folder is gone from the library") : s.description || "");
    sub.title = s.description || "";
    who.append(sub);
    if (s.source) {
      const src = el("div", "lib-src");
      if (s.kind === "github") {
        const a = el("a", "lib-srclink", s.source.replace(/^https:\/\/github\.com\//, ""));
        a.href = s.source;
        a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); browse(s.source); };
        src.append(a);
      } else {
        src.append(el("span", "", t("linked from")), pathLink(s.source));
      }
      who.append(src);
    }
    const acts = el("div", "lib-rowacts");
    if (s.kind === "github" || s.origin) {
      const u = button("", "lib-icon", async (e, b) => {
        b.classList.add("busy");
        await change("skills/update", { name: s.name }, t("{name} is up to date", { name: s.name }));
        b.classList.remove("busy");
      });
      u.append(svg(GLYPH.up, 13, 1.5));
      u.title = s.origin ? t("Update from GitHub ({repo}, as CC Switch installed it)", { repo: s.origin.replace(/^https:\/\/github\.com\//, "") }) : t("Update from GitHub");
      if (c?.status === "current") u.title += "\n" + t("Up to date with GitHub") + "\n" + checkLine(c);
      else if (c?.status === "update") u.title += "\n" + checkLine(c);
      acts.append(u);
    }
    const rm = button("", "lib-icon danger", () => confirmRemoveSkill(s));
    rm.append(svg(GLYPH.trash, 13, 1.4));
    rm.title = t("Remove");
    acts.append(rm);
    row.append(mark(s.icon, GLYPH.skill), who, acts, agentChips(all, s.agents, chipsChange("skills/agents", s.name, "skills", (x) => skillRow(x, all)), { problems: s.problems, via: viaFor(s), all: true, always: alwaysFor(s) }));
    if (picking) return pickRow(row, s.name);
    row.onclick = () => viewSkill(s);
    row.title = t("Read {name}'s SKILL.md", { name: s.name });
    return row;
  }

  function confirmRemoveSkill(s) {
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", t("Remove {name}?", { name: s.name })));
    ed.append(head);
    ed.append(el("p", "lib-confirm", s.kind === "folder"
      ? t("It is taken out of every agent it was given to. The folder it was linked from stays where it is.")
      : t("It is taken out of every agent it was given to, and its folder is moved to magpie's backups.")));
    const bar = el("div", "bar");
    const go = button(t("Remove"), "primary danger-fill", async () => { if (await change("skills/remove", { name: s.name }, t("{name} removed", { name: s.name }))) closeLibModal(true); });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  async function viewSkill(s) {
    const ed = el("div", "editor lib-editor lib-reader");
    const head = el("div", "ehead");
    head.append(mark(s.icon, GLYPH.skill), el("b", "", s.name), el("span", "grow"));
    const pre = el("pre", "lib-md", "…");
    ed.append(head, pre);
    const bar = el("div", "bar");
    const where = el("span", "note");
    bar.append(where, el("span", "grow"), button(t("Close"), "", closeLibModal));
    ed.append(bar);
    modal = {};
    openLib(ed);
    try {
      const r = await api("library/skill?name=" + encodeURIComponent(s.name));
      pre.textContent = r.text;
      where.replaceChildren(pathLink(r.path));
    } catch (e) { pre.textContent = e.message; }
  }

  function foundSkillRow(f) {
    const row = el("div", "row lib-row");
    const who = el("div", "who");
    who.append(el("div", "name", f.name));
    const sub = el("div", "sub", f.description || "");
    sub.title = f.description || "";
    who.append(sub);
    // put in the library's own folder by hand, not listed by it (#595)
    if (f.library) { const src = el("div", "lib-src"); src.append(el("span", "", t("in the library's folder, not listed")), pathLink(f.library)); who.append(src); }
    if (f.shared) { const src = el("div", "lib-src"); src.append(el("span", "", t("shared in")), pathLink(f.shared)); who.append(src); }
    if (f.link) { const src = el("div", "lib-src"); src.append(el("span", "", t("linked from")), pathLink(f.link)); who.append(src); }
    const have = el("div", "lib-have");
    // an agent with a copy of the very same files has it as much as one
    // with the folder itself: it isn't said to differ
    for (const id of [...f.agents, ...(f.copies || [])]) { const a = agentOf(id); if (a) { const i = agentIcon(a.icon); i.title = f.agents.includes(id) ? a.name : a.name + " — " + t("{agents} has the very same files; bringing it in gives it the library's, its copy kept with the backups", { agents: a.name }); have.append(i); } }
    row.append(glyph(GLYPH.skill), who, have);
    if (f.others?.length) row.append(tag(t("differs in {agents}", { agents: f.others.map(nameOf).join(", ") }), "warn", t("{agents} has another skill by this name; bringing this one in leaves that one as it is", { agents: f.others.map(nameOf).join(", ") })));
    const b = button(t("Bring in"), "action", () => change("skills/import", { name: f.name }, t("{name} is in the library now", { name: f.name })));
    b.title = f.library ? t("Lists it in the library where it is, nothing moved: you can give it to any agent")
      : f.shared ? t("Keeps it where it is in the shared skills folder and links to it: you can give it to any agent")
      : f.link ? t("Keeps a link to where it is: {agents} go on having it, and you can give it to the others", { agents: [...f.agents, ...(f.copies || [])].map(nameOf).join(", ") })
      : t("Moves it into the library and links it back: {agents} go on having it, and you can give it to the others", { agents: [...f.agents, ...(f.copies || [])].map(nameOf).join(", ") });
    row.append(b);
    return row;
  }

  // ---------- the market ----------

  // What the market offers: popular and featured ones until the reader
  // searches. Kept across renders; asked for the first time a tab shows it.
  const market = {
    mcp: { q: "", items: null, error: "", loading: false, seq: 0, timer: 0, custom: null },
    skills: { q: "", items: null, error: "", loading: false, seq: 0, timer: 0 },
  };
  const asked = new Set(); // skills whose description has been asked for

  async function fetchMarket(kind) {
    const m = market[kind];
    const seq = ++m.seq;
    m.loading = true;
    drawMarket(kind);
    try {
      const r = await api("library/market/" + (kind === "mcp" ? "servers" : "skills") + "?q=" + encodeURIComponent(m.q.trim()));
      if (seq !== m.seq) return;
      m.items = r.items || [];
      m.error = r.error || "";
      // a search that is an address nothing listed is at: the server
      // there, to add by hand
      m.custom = r.custom || null;
    } catch (e) {
      if (seq !== m.seq) return;
      m.items = m.items || [];
      m.custom = null;
      m.error = e.message;
    }
    m.loading = false;
    drawMarket(kind);
    if (kind === "skills") describeSkills();
    // The market reads the library as it is now; one it calls added that
    // this page doesn't list was added elsewhere — another window, the CLI.
    const mine = new Set((kind === "mcp" ? lib?.servers : lib?.skills)?.map((x) => x.name) || []);
    if (lib && m.items.some((x) => x.have && !mine.has(x.have))) quietLoad();
  }

  // The descriptions skills.sh doesn't list, read from each SKILL.md.
  async function describeSkills() {
    const ids = (market.skills.items || []).filter((x) => !x.description && !asked.has(x.id)).map((x) => x.id).slice(0, 60);
    if (!ids.length) return;
    ids.forEach((id) => asked.add(id));
    try {
      const about = await api("library/market/about", { ids });
      for (const x of market.skills.items || []) if (about[x.id]) x.description = about[x.id];
      for (const [id, d] of Object.entries(about)) {
        const p = page.querySelector(`.mk-card[data-id="${CSS.escape(id)}"] .mk-desc`);
        if (p && d) { p.textContent = d; p.title = d; p.classList.remove("wait"); }
      }
    } catch {}
    for (const p of page.querySelectorAll(".mk-desc.wait")) { p.classList.remove("wait"); p.textContent = ""; }
  }

  function discover(kind) {
    const m = market[kind];
    const box = el("section", "mk");
    box.dataset.market = kind;
    const rh = el("div", "row-head");
    rh.append(el("span", "label", t("Discover")), el("span", "grow"));
    const note = el("span", "note mk-note");
    rh.append(note);
    box.append(rh);
    const find = el("div", "mk-find");
    const inp = el("input");
    inp.type = "search";
    inp.dataset.lib = "market-" + kind;
    inp.placeholder = kind === "mcp" ? t("Search MCP servers — GitHub, Postgres, Figma…") : t("Search skills on skills.sh — react, pdf, testing…");
    inp.spellcheck = false;
    inp.autocomplete = "off";
    inp.value = m.q;
    inp.oninput = () => {
      m.q = inp.value;
      clearTimeout(m.timer);
      const q = m.q.trim();
      if (kind === "skills" && q.length === 1) return;
      m.timer = setTimeout(() => fetchMarket(kind), 320);
    };
    inp.onkeydown = (e) => {
      e.stopPropagation();
      if (e.key === "Escape" && inp.value) { e.preventDefault(); inp.value = ""; inp.oninput(); }
      else if (e.key === "Enter") { clearTimeout(m.timer); fetchMarket(kind); }
    };
    find.append(glyph(GLYPH.search, "lib-mini"), inp, el("span", "mk-spin"));
    box.append(find);
    box.append(el("div", "mk-grid"));
    drawMarket(kind, box); // now, so the page keeps its scroll across a render
    if (!m.items && !m.loading) queueMicrotask(() => fetchMarket(kind));
    return box;
  }

  function drawMarket(kind, box = page.querySelector(`.mk[data-market="${kind}"]`)) {
    if (!box) return;
    const m = market[kind];
    const q = m.q.trim();
    box.classList.toggle("loading", m.loading);
    const note = box.querySelector(".mk-note");
    note.textContent = q && m.items ? (m.items.length === 1 ? t("1 result") : t("{n} results", { n: m.items.length }))
      : kind === "mcp" ? t("Featured, and the official MCP Registry") : t("Popular on skills.sh");
    const grid = box.querySelector(".mk-grid");
    grid.replaceChildren();
    if (!m.items) {
      for (let i = 0; i < 6; i++) {
        const c = el("div", "mk-card mk-skel");
        const top = el("div", "mk-top");
        const lines = el("div", "mk-who");
        const a = el("span", "skeleton"); a.style.cssText = "width:55%;height:10px";
        const b = el("span", "skeleton"); b.style.cssText = "width:35%;height:8px";
        lines.append(a, b);
        top.append(el("span", "mk-icon skeleton"), lines);
        const d = el("span", "skeleton"); d.style.cssText = "width:90%;height:8px;margin-top:4px";
        c.append(top, d);
        grid.append(c);
      }
      return;
    }
    if (m.error) grid.append(el("div", "mk-msg err", m.error));
    if (kind === "mcp" && q && m.custom) grid.append(customAt(q, m.custom, m.items.length));
    else if (!m.items.length && !m.error) {
      grid.append(el("div", "mk-msg", q ? t("Nothing matches “{q}”.", { q }) : t("Nothing to show.")));
      return;
    }
    for (const x of m.items) grid.append(kind === "mcp" ? serverCard(x) : skillCard(x));
  }

  // An address searched for that nothing listed is at: say so, over the
  // servers found by its name if any, and offer to add it by hand — the
  // form filled in with what the address says (an endpoint, a package).
  function customAt(q, c, found) {
    const box = el("div", "mk-msg mk-custom");
    box.append(el("span", "", found ? t("None of these is at {q} — they're found by its name.", { q }) : t("Nothing listed is at {q}.", { q })));
    box.append(button(t("＋ Add it yourself…"), "action", () => editServer(null, {
      name: c.name || "", transport: c.transport || "stdio", command: c.command || "", args: c.args || [], env: {},
      url: c.url || "", headers: {}, agents: mcpAgents().map((a) => a.id),
    })));
    return box;
  }

  // A real picture: the project's own logo or its owner's, through magpie so
  // it's cached; its initial on a tile when there's none.
  function logo(url, name) {
    const box = el("span", "mk-icon");
    const fallback = () => { box.replaceChildren(el("span", "mk-mono", (name || "?").replace(/^[^a-z0-9]+/i, "").charAt(0).toUpperCase() || "?")); box.classList.add("mono"); };
    if (!url) { fallback(); return box; }
    const img = el("img");
    img.alt = "";
    img.decoding = "async";
    img.loading = "lazy";
    img.referrerPolicy = "no-referrer";
    img.src = "/api/library/icon?u=" + encodeURIComponent(url);
    if (/\.svg(\?|$)/i.test(url)) box.classList.add("svg");
    img.onerror = fallback;
    box.append(img);
    return box;
  }

  function compact(n) {
    if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1).replace(/\.0$/, "") + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1).replace(/\.0$/, "") + "K";
    return String(n);
  }

  function runsLabel(x) {
    if (x.transport !== "stdio") return x.signIn ? t("Remote · sign-in") : t("Remote");
    return x.runs || t("Local");
  }
  const needsKey = (x) => x.inputs?.some((i) => i.required);
  // the featured servers' words are magpie's own, so they're translated
  const about = (x) => (x.featured ? t(x.description) : x.description) || "";

  function addButton(x, onAdd) {
    if (x.have) {
      const b = el("span", "mk-have");
      b.append(svg(CHECK, 11, 2), el("span", "", t("Added")));
      b.title = x.have === x.name ? t("In the library") : t("In the library as {name}", { name: x.have });
      return b;
    }
    const b = button(t("Add"), "action mk-add", async (e, btn) => {
      btn.disabled = true;
      btn.classList.add("busy");
      btn.textContent = t("Adding…");
      const ok = await onAdd();
      if (!ok && btn.isConnected) { btn.disabled = false; btn.classList.remove("busy"); btn.textContent = t("Add"); }
    });
    return b;
  }

  function serverCard(x) {
    const c = el("div", "mk-card click" + (x.have ? " have" : ""));
    c.dataset.id = x.id;
    const top = el("div", "mk-top");
    const who = el("div", "mk-who");
    const nm = el("div", "mk-name");
    nm.append(el("span", "mk-title", x.title || x.name));
    who.append(nm);
    const meta = el("div", "mk-meta");
    if (x.publisher) meta.append(el("span", "mk-pub", x.publisher));
    meta.append(el("span", "mk-badge", runsLabel(x)));
    if (needsKey(x)) { const k = el("span", "mk-badge mk-needs"); k.append(svg(GLYPH.key, 11, 1.5)); k.title = t("Needs {what} to add", { what: x.inputs.filter((i) => i.required).map((i) => t(i.label)).join(", ") }); meta.append(k); }
    who.append(meta);
    top.append(logo(x.icon, x.title || x.name), who);
    const d = el("p", "mk-desc", about(x));
    d.title = about(x);
    const foot = el("div", "mk-foot");
    foot.append(el("span", "mk-id mono", x.name), el("span", "grow"), (needsKey(x) || x.optIn) && !x.have ? button(t("Add…"), "action mk-add", () => serverSheet(x)) : addButton(x, () => addServer(x, {}, null)));
    c.append(top, d, foot);
    c.onclick = () => serverSheet(x);
    c.title = t("About {name}", { name: x.title || x.name });
    return c;
  }

  async function addServer(x, values, agents) {
    const body = { id: x.id, values };
    if (agents) body.agents = agents;
    const ok = await change("market/server", body, t("{name} is in the library now", { name: x.title || x.name }));
    if (ok) { x.have = x.name; drawMarket("mcp"); } // take asked the market again
    return ok;
  }

  // A market server up close: what it is, what it needs, and who gets it.
  function serverSheet(x) {
    const all = mcpAgents();
    // an opt-in one (magpie's image generation costs what its model does)
    // goes only to the agents picked for it
    let agents = x.optIn ? [] : all.filter(reaches(x)).map((a) => a.id);
    const values = {};
    const ed = el("div", "editor lib-editor mk-sheet");
    const head = el("div", "mk-sheethead");
    const who = el("div", "mk-who");
    who.append(el("div", "mk-name big", x.title || x.name));
    const meta = el("div", "mk-meta");
    if (x.publisher) meta.append(el("span", "mk-pub", x.publisher));
    meta.append(el("span", "mk-badge", runsLabel(x)));
    who.append(meta);
    head.append(logo(x.icon, x.title || x.name), who);
    if (x.homepage) head.append(extLink(x.homepage, t("Homepage")));
    ed.append(head);
    if (x.description) ed.append(el("p", "mk-about", about(x)));
    if (x.signIn) ed.append(el("p", "mk-hint", t("Each agent asks you to sign in, in the browser, the first time it uses it.")));
    if (x.optIn) ed.append(el("p", "mk-hint", t("Pick the agents that may generate images: each image costs what the model set in Settings → Images charges.")));
    const firsts = [];
    for (const i of x.inputs || []) {
      const f = field2("", i.placeholder || (i.where === "env" ? i.key : ""), (v) => { values[i.key] = v.trim(); });
      if (i.secret) f.type = "password";
      f.classList.add("mono");
      f.dataset.key = i.key;
      firsts.push(f);
      const hint = [i.description && t(i.description), i.where === "env" ? t("Set as {key}", { key: i.key }) : i.where === "header" ? t("Sent as the {key} header", { key: i.key }) : ""].filter(Boolean).join(" · ");
      ed.append(...field(t(i.label) + (i.required ? "" : " " + t("(optional)")), f, hint));
    }
    const agentsBox = el("div");
    const drawAgents = () => agentsBox.replaceChildren(agentChips(all, agents, (n) => { agents = n; drawAgents(); }, { names: true, blocked: sseBlocked(x) }));
    drawAgents();
    ed.append(...field(t("Agents"), agentsBox));
    const err = el("div", "editor-error");
    ed.append(err);
    const bar = el("div", "bar");
    bar.append(el("span", "mk-id mono", x.name), el("span", "grow"), button(t(x.have ? "Close" : "Cancel"), "", closeLibModal));
    let save = () => {};
    if (!x.have) {
      const go = button(t("Add to the library"), "primary", async () => {
        err.textContent = "";
        const miss = (x.inputs || []).find((i) => i.required && !values[i.key]);
        if (miss) { err.textContent = t("{what} is needed", { what: t(miss.label) }); ed.querySelector(`[data-key="${CSS.escape(miss.key)}"]`)?.focus(); return; }
        go.disabled = true;
        go.textContent = t("Adding…");
        if (await addServer(x, values, agents)) closeLibModal(true);
        else { go.disabled = false; go.textContent = t("Add to the library"); }
      });
      bar.append(go);
      save = () => go.click();
    } else bar.prepend(el("span", "mk-have", t("In the library as {name}", { name: x.have })));
    ed.append(bar);
    modal = { save };
    openLib(ed);
    if (firsts.length) requestAnimationFrame(() => firsts[0].focus());
  }

  function extLink(href, text) {
    const a = el("a", "mk-ext");
    a.href = href;
    a.append(el("span", "", text), svg(GLYPH.out, 11, 1.5));
    a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); browse(href); };
    return a;
  }

  function skillCard(x) {
    const c = el("div", "mk-card click" + (x.have ? " have" : ""));
    c.dataset.id = x.id;
    const top = el("div", "mk-top");
    const who = el("div", "mk-who");
    const nm = el("div", "mk-name");
    nm.append(el("span", "mk-title", x.name));
    if (x.official) { const o = el("span", "mk-official"); o.append(svg(CHECK, 9, 2.2)); o.title = t("Official — from the team that makes it"); nm.append(o); }
    who.append(nm);
    const meta = el("div", "mk-meta");
    meta.append(el("span", "mk-pub", x.source));
    who.append(meta);
    top.append(logo(x.icon, x.source), who);
    const d = el("p", "mk-desc" + (x.description ? "" : " wait"), x.description || "");
    d.title = x.description || "";
    const foot = el("div", "mk-foot");
    // magpie's own skill has no count until skills.sh lists it
    if (!x.featured || x.installs) {
      const n = el("span", "mk-installs");
      n.append(svg(GLYPH.down, 11, 1.5), el("span", "", compact(x.installs)));
      n.title = t("{n} installs", { n: x.installs.toLocaleString() });
      foot.append(n);
    }
    foot.append(el("span", "grow"), addButton(x, () => addSkill(x)));
    c.append(top, d, foot);
    c.onclick = () => skillSheet(x);
    c.title = t("About {name}", { name: x.name });
    return c;
  }

  async function addSkill(x, agents) {
    const body = { source: x.source, id: x.skillId };
    if (agents) body.agents = agents;
    const ok = await change("market/skill", body, t("{name} is in the library now", { name: x.name }));
    if (ok) { x.have = x.name; drawMarket("skills"); } // take asked the market again
    return ok;
  }

  function skillSheet(x) {
    const all = skillAgents();
    let agents = all.map((a) => a.id);
    const ed = el("div", "editor lib-editor mk-sheet");
    const head = el("div", "mk-sheethead");
    const who = el("div", "mk-who");
    who.append(el("div", "mk-name big", x.name));
    const meta = el("div", "mk-meta");
    meta.append(el("span", "mk-pub", x.source));
    if (!x.featured || x.installs) meta.append(el("span", "mk-badge", t("{n} installs", { n: compact(x.installs) })));
    if (x.official) meta.append(el("span", "mk-badge", t("Official")));
    who.append(meta);
    head.append(logo(x.icon, x.source), who);
    if (!x.featured) head.append(extLink("https://skills.sh/" + x.source + "/" + x.skillId, "skills.sh"));
    ed.append(head);
    const about = el("p", "mk-about", x.description || "…");
    ed.append(about);
    if (!x.description) api("library/market/about", { ids: [x.id] }).then((r) => { x.description = r[x.id] || ""; about.textContent = x.description || t("No description."); }).catch(() => { about.textContent = ""; });
    const src = el("div", "mk-hint");
    src.append(el("span", "", t("From")), extLink("https://github.com/" + x.source, "github.com/" + x.source));
    ed.append(src);
    const agentsBox = el("div");
    const drawAgents = () => agentsBox.replaceChildren(agentChips(all, agents, (n) => { agents = n; drawAgents(); }, { names: true }));
    drawAgents();
    if (!x.have) ed.append(...field(t("Agents"), agentsBox));
    const bar = el("div", "bar");
    bar.append(el("span", "grow"), button(t(x.have ? "Close" : "Cancel"), "", closeLibModal));
    let save = () => {};
    if (!x.have) {
      const go = button(t("Add to the library"), "primary", async () => {
        go.disabled = true;
        go.textContent = t("Adding…");
        if (await addSkill(x, agents)) closeLibModal(true);
        else { go.disabled = false; go.textContent = t("Add to the library"); }
      });
      bar.append(go);
      save = () => go.click();
    } else bar.prepend(el("span", "mk-have", t("In the library as {name}", { name: x.have })));
    ed.append(bar);
    modal = { save };
    openLib(ed);
  }

  // ---------- the dialog ----------

  function openLib(content) { openModal(content); $("#modal").classList.add("lib"); }
  async function closeLibModal(saved = false) {
    if (saved !== true && (modal?.dirty?.() ?? modalFormDirty()) && !(await confirmDiscard())) return false;
    modal = null;
    closeModal().then(() => { if (!modal) $("#modal").classList.remove("lib"); });
    return true;
  }
  window.closeLibraryModal = () => { if (modal) closeLibModal(true); else closeModal(); };
  window.libraryDirty = () => !!lib && dirty();
  window.discardLibrary = () => discard(true);
  // The dialog is the providers page's; while the library has it, its
  // backdrop and Escape close it here.
  $("#modal").addEventListener("click", (e) => {
    if (modal && e.target === e.currentTarget) { e.stopImmediatePropagation(); closeLibModal(); }
  }, true);
  document.addEventListener("keydown", (e) => {
    if (!confirmationPending && modal && e.key === "Escape") { e.preventDefault(); e.stopImmediatePropagation(); closeLibModal(); }
  }, true);
  // leaving the page with the shared text not saved asks first
  window.addEventListener("beforeunload", (e) => { if (lib && dirty()) e.preventDefault(); });
  // Back to the window, the library is read again — it may have changed
  // in another window or through the CLI meanwhile. Not over an open dialog,
  // unsaved text, or a lookup under way.
  page.addEventListener("scroll", () => page.querySelector(".lib-head")?.classList.toggle("stuck", page.scrollTop > 0), { passive: true });
  // What changed there changes what the market calls added, too: take asks it again.
  async function quietLoad() {
    if (page.hidden || modal || dirty() || probing) return;
    await load(true);
  }
  const shelf = () => JSON.stringify([lib?.servers?.map((x) => x.name), lib?.skills?.map((x) => x.name)]);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) quietLoad(); });
  window.addEventListener("focus", quietLoad);
  // opened on ?view=library: app.js showed the page before this was here
  if (!page.hidden) load();
})();
