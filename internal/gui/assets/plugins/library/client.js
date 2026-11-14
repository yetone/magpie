// Library: one set of instructions, MCP servers and skills, written into
// every agent that should have them (#29). magpie keeps the library in its
// own folder and writes each agent's files from it; what the user has in
// those files besides stays theirs, and every file is kept aside before
// magpie writes it. Everything shown here comes from /api/library.
//
// A page the Library plugin adds to the window (kernel.js): mount wires it
// up, open and relang read the library again, redraw draws it again as it
// is, and close, dirty and discard are what app.js asks of it before the
// reader leaves it.
magpie.plugin("library", (ctx) => {
  let page = null;
  let loading = null;    // the read open started, which server waits for

  let lib = null;        // the page, as /api/library gives it
  let taken = 0;         // how many times lib was set, for a read to tell a newer answer came first
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
  let autoChecked = false; // the page checked by itself, as the server said it was due (#1449)
  let updatingAll = false; // Update all, picked from the bar's menu
  let regrouping = false; // the skills' repositories fetched to group them again
  let modal = null;      // what the library has open in the dialog
  const hid = new Set(); // the write failures the reader hid, by agent and what (#3745)

  const GLYPH = {
    cmd: "M3 4.5h10v7H3z M5.5 7l1.5 1.2-1.5 1.3 M8.5 9.5h2",
    // two arrows closing on a line: billion-context folding a context
    fold: "M8 2v4 M6 4.2 8 6l2-1.8 M8 14v-4 M6 11.8 8 10l2 1.8 M3 8h10",
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
    hook: "M10.5 2.5v6.5a3 3 0 0 1-6 0V7.5 M3 9l1.5-1.5L6 9",
    // a person with a second, smaller one it hands work to
    subagent: "M6 3a2.2 2.2 0 1 0 0 4.4A2.2 2.2 0 0 0 6 3z M2 13c.5-2.2 2-3.4 4-3.4s3.5 1.2 4 3.4 M11.5 7.5a1.5 1.5 0 1 0 0 .01 M10.2 12.5c.3-1.3 1.1-2 2.3-2s1.6.5 1.9 1.4",
    // a clock with an arrow going back round it, as macOS draws backups
    backup: "M3.2 8a4.8 4.8 0 1 0 1.4-3.4 M2.8 2.6v2.4h2.4 M8 5.3V8l1.9 1.2",
    power: "M8 2.5v5 M4.7 4.6a4.8 4.8 0 1 0 6.6 0",
  };

  const tilde = (p) => (lib?.home && p?.startsWith(lib.home) ? "~" + p.slice(lib.home.length) : p || "");
  const agentOf = (id) => lib.agents.find((a) => a.id === id);
  const nameOf = (id) => agentOf(id)?.name || id;
  // An agent with no MCP of its own (Pi) reads its servers through an
  // extension, which each of its chips says.
  // An agent hidden on the Agents page is left out here too (#71): what it
  // already has stays with it, and a new server or skill isn't given to it.
  // One gone from this computer (gone: uninstalled, or its CLI) is listed
  // only on the rows of what it still has (withGone), for that to be taken
  // out: an agent that isn't installed was on every row (@jiahao_jayden).
  // An agent in a stopped WSL distro (asleep) is listed by what magpie last
  // knew of it, its ticks kept for when the distro next runs; one whose
  // places magpie never saw (unseen) only on the rows that name it, like
  // a gone one, its chip greyed out: opening its files would start WSL
  // (#3896, cybercaius on Discord).
  // In gateway mode (gatewaymode.go) none is shown: the page is the
  // MCP servers the router gives other computers' apps.
  const shownAgents = () => gatewayPage() ? [] : lib.agents.filter((a) => !a.gone && !a.unseen && !isHidden(a));
  // With no agent to write to, none on this computer or gateway mode, the
  // library is its MCP servers alone, served to any app through the router
  // (treeman_26897 on Discord: magpie on a server, an MCP entry point for
  // remote clients, had no way to turn the router on).
  const gatewayPage = () => typeof gatewayMode !== "undefined" && !!gatewayMode;
  const agentless = () => !!lib && (gatewayPage() || !lib.agents.length);
  const withGone = (all, on) => [...all, ...lib.agents.filter((a) => (a.gone || a.unseen && !isHidden(a)) && (on || []).includes(a.id) && !all.some((x) => x.id === a.id))];
  const mcpAgents = () => shownAgents().filter((a) => a.mcp)
    .map((a) => a.mcpVia ? { ...a, aside: t("{agent} reads MCP servers through the {ext} extension", { agent: a.name, ext: a.mcpVia }) } : a);
  const skillAgents = () => shownAgents().filter((a) => a.skills);
  // the MCP hub's agents: not a stopped distro's, whose files say what it
  // has of the hub, and can't be read without starting it
  const hubAgents = () => mcpAgents().filter((a) => !a.asleep);
  // one line for each stopped WSL distro the page lists agents of: they
  // show what magpie last saw, and a change waits for the distro to run.
  // The Instructions page passes its own rows' agents ({asleep, unseen,
  // name}).
  function asleepAsides(seen = lib.agents.filter((a) => a.asleep && !isHidden(a))) {
    return [...new Set(seen.map((a) => a.asleep))].map((d) => {
      const unseen = seen.filter((a) => a.asleep === d && a.unseen).map((a) => a.name);
      const p = el("p", "lib-aside lib-asleep", t("WSL {distro} isn't running: its agents show what magpie last saw there, and what you change is written there the next time it runs.", { distro: d }));
      if (unseen.length) p.append(" " + t("{agents} can be changed once it runs.", { agents: unseen.join(", ") }));
      p.dataset.distro = d;
      return p;
    });
  }

  // the hub's mark: magpie's bird, the one server each agent is given,
  // its tail three lines out to three servers (the owner's pick, 10-31)
  function hubMark() {
    const g = el("span", "lib-glyph lib-hubmark");
    g.innerHTML = '<svg viewBox="0 0 44 44" aria-hidden="true"><use href="#bird" clip-path="url(#bird-notail)"/>'
      + '<g fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><path d="M13.5 24C9 21 7 15 4.5 9.5"/><path d="M13 25C9 24 6 21.5 3 19.5"/><path d="M12.5 26.2C9 27 6.5 28 4 29.5"/></g>'
      + '<g fill="currentColor"><circle cx="4" cy="8.5" r="2.6"/><circle cx="2.8" cy="19.2" r="2.6"/><circle cx="3.6" cy="29.8" r="2.6"/></g></svg>';
    return g;
  }
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

  const bridgedWhy = () => t("Added from a Remote magpie with this computer's key for it, so it stays on this computer");

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
    // a row's profiles of one agent (hermes#coder, omp#work…) are one chip
    // that opens a menu of them, not one icon each (Yanzhongixuy on X: 20
    // Hermes glyphs in a row)
    const fams = onChange.rows ? profileFamilies(all) : new Map();
    const shown = [], made = new Set();
    for (const a of all) {
      const f = familyOf(a.id);
      if (!fams.has(f)) shown.push({ a });
      else if (!made.has(f)) { made.add(f); shown.push({ family: f, members: fams.get(f) }); }
    }
    box.style.setProperty("--lib-agent-count", shown.length);
    // the row it is put in says so (.with-agents), for its name's room:
    // Safari before 15.4 has no :has(). Every caller appends it at once.
    queueMicrotask(() => box.parentElement?.classList.contains("lib-row") && box.parentElement.classList.add("with-agents"));
    for (const [i, g] of shown.entries()) {
      if (g.family) { box.append(profileChip(g, i, shown.length, all, on, onChange, opts)); continue; }
      const a = g.a;
      const always = opts.always?.(a) || "";
      const has = on.includes(a.id) || !!always;
      const c = el("button", "lib-ag" + (has ? " on" : "") + (always ? " always" : ""));
      c.dataset.agent = a.id;
      // how many chips are to its right, how far it slides into the hand
      c.style.setProperty("--k", shown.length - 1 - i);
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
      if (a.gone) { c.classList.add("gone"); tip = t("{agent} isn't installed here — click to take it away", { agent: a.name }); }
      if (a.unseen) { c.classList.add("asleep", "unseen", "blocked"); c.setAttribute("aria-disabled", "true"); tip = t("WSL {distro} isn't running, and magpie hasn't seen {agent}'s files there yet: start the distro to change this", { distro: a.asleep, agent: a.name }); }
      else if (a.asleep) {
        c.classList.add("asleep");
        const v = { distro: a.asleep, agent: a.name };
        if (!problem && !blocked) tip = has ? t("WSL {distro} isn't running: {agent} has it — click to take it away, and magpie writes the change there the next time it runs", v) : t("WSL {distro} isn't running: a click gives it to {agent} the next time the distro runs", v);
      }
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
    if (opts.all) allChip(box, all, on, onChange, opts);
    return box;
  }

  // The agent a profile belongs to: hermes#coder and hermes are Hermes,
  // omp#work@wsl:Ubuntu and omp@wsl:Ubuntu are omp in that distro.
  const familyOf = (id) => id.replace(/#[^@]*/, "");
  // Three or more of an agent's own and its profiles are one chip; fewer
  // fit the row as they are.
  function profileFamilies(all) {
    const by = new Map();
    for (const a of all) {
      const f = familyOf(a.id);
      if (!by.has(f)) by.set(f, []);
      by.get(f).push(a);
    }
    for (const [f, ms] of [...by]) if (ms.length < 3 || !ms.some((a) => a.id !== f)) by.delete(f);
    return by;
  }
  // The agents a chip stands for: its own, or the profiles of a group.
  const idsOf = (x) => x.dataset.members ? x.dataset.members.split("\t") : x.dataset.agent ? [x.dataset.agent] : [];
  const isChip = (x) => !!(x.dataset.agent || x.dataset.members);
  const memberOn = (x) => x.dataset.members ? (x.dataset.lit ? x.dataset.lit.split("\t") : []) : x.getAttribute("aria-pressed") === "true" ? [x.dataset.agent] : [];
  // A group's chip: lit when any profile has the item, dashed while only
  // some do, with how many in the corner.
  function paintProfiles(c, lit) {
    const n = lit.length, total = idsOf(c).length - (c.dataset.fixed ? c.dataset.fixed.split("\t").length : 0);
    c.dataset.lit = lit.join("\t");
    const has = n > 0 || !!c.dataset.fixed;
    c.classList.toggle("on", has);
    c.classList.toggle("some", n > 0 && n < total);
    c.setAttribute("aria-pressed", has ? "true" : "false");
    const k = c.querySelector(":scope > .cnt");
    if (k) k.textContent = n + (c.dataset.fixed ? c.dataset.fixed.split("\t").length : 0) || "";
  }
  function profileChip(g, i, count, all, on, onChange, opts) {
    const ms = g.members, main = ms.find((a) => a.id === g.family) || ms[0];
    const always = new Map(ms.map((a) => [a.id, opts.always?.(a) || ""]).filter(([, w]) => w));
    const fixed = ms.filter((a) => always.has(a.id) || a.unseen).map((a) => a.id);
    const lit = ms.filter((a) => on.includes(a.id) && !fixed.includes(a.id)).map((a) => a.id);
    const name = main.id === g.family ? main.name : main.name.split(" · ")[0];
    const c = el("button", "lib-ag profiles");
    c.dataset.members = ms.map((a) => a.id).join("\t");
    c.dataset.fixed = fixed.join("\t");
    c.style.setProperty("--k", count - 1 - i);
    c.append(agentIcon(main.icon));
    if (opts.names) c.append(el("span", "n", name));
    c.append(el("span", "cnt"));
    paintProfiles(c, lit);
    const problems = ms.filter((a) => opts.problems?.[a.id]);
    if (problems.length) c.classList.add("warn");
    c.setAttribute("aria-haspopup", "menu");
    c.title = problems.length ? name + ": " + problems.map((a) => a.name + ": " + opts.problems[a.id]).join("\n") : name;
    c.onclick = (e) => {
      e.stopPropagation();
      const me = e.currentTarget;
      const pick = ms.filter((a) => !fixed.includes(a.id) && !opts.blocked?.(a));
      const stuck = ms.filter((a) => !fixed.includes(a.id) && opts.blocked?.(a) && memberOn(me).includes(a.id)).map((a) => a.id);
      const menu = [{ v: "\x00" + g.family, group: pick.map((a) => a.id), name, literalName: true, note: "" },
        ...pick.map((a) => ({ v: a.id, name: a.name, literalName: true, note: "", title: opts.problems?.[a.id] || "" }))];
      openProtoMenu(me, menu, memberOn(me).filter((id) => !stuck.includes(id)), (next) => {
        const now = [...stuck, ...next.filter((id) => pick.some((a) => a.id === id))];
        paintProfiles(me, now);
        const litAll = litOf(me.parentElement);
        onChange([...keptOf(me.parentElement, all, on), ...litAll], me);
      }, name, "", "left", true);
    };
    return c;
  }

  // cannot says a chip's agent can't be given the item, or has it whatever
  // is ticked: a click says why rather than switching it
  const cannot = (c) => c.getAttribute("aria-disabled") === "true";

  // The agents a row's chips have lit by a click: not one lit because it
  // has the item whatever is ticked.
  function litOf(box) {
    return [...box.children].filter((x) => isChip(x) && !x.classList.contains("always") && !x.classList.contains("unseen")).flatMap(memberOn);
  }
  // What a click leaves as it was: an agent not shown, one that has the
  // item whatever is ticked, whose chip can't be clicked, and one in a
  // stopped distro magpie can't show the ticks of (unseen).
  function keptOf(box, all, on) {
    const fixed = [...box.children].filter((x) => x.classList.contains("always") || x.classList.contains("unseen")).map((x) => x.dataset.agent)
      .concat([...box.children].flatMap((x) => x.dataset.fixed ? x.dataset.fixed.split("\t") : []));
    return on.filter((id) => !all.some((x) => x.id === id) || fixed.includes(id));
  }

  // All, ahead of a row's chips: one click gives the item to every agent
  // shown that can take it (not those a chip is greyed out for: no SSE, no
  // remote server), and when they all have it, takes it from every one.
  // An agent not shown keeps what it has, as with a chip. The pickers that
  // choose the agents before an item is added (installing skills, adding or
  // bringing in servers) have it too (#1531): most start with every agent
  // lit, and one agent alone was a click on each of the others.
  function allChip(box, all, on, onChange, opts = {}) {
    const can = [...box.children].filter((c) => !cannot(c) && !c.classList.contains("gone")).flatMap((c) => c.dataset.members ? idsOf(c).filter((id) => !c.dataset.fixed?.split("\t").includes(id) && !opts.blocked?.(all.find((a) => a.id === id))) : [c.dataset.agent]);
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
    const can = [...box.children].filter((x) => x.dataset.agent && !cannot(x) && !x.classList.contains("gone"));
    const groups = [...box.children].filter((x) => x.dataset.members);
    const n = can.length + groups.reduce((k, g) => k + idsOf(g).length - (g.dataset.fixed ? g.dataset.fixed.split("\t").length : 0), 0);
    const full = can.every((x) => x.getAttribute("aria-pressed") === "true") && groups.every((g) => g.dataset.lit.split("\t").filter(Boolean).length === idsOf(g).length - (g.dataset.fixed ? g.dataset.fixed.split("\t").length : 0));
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
    const go = async (next, c) => {
      // All lights or darkens every chip of the row, a chip only itself
      const every = !!c.dataset.all;
      for (const x of every ? [...c.parentElement.children].filter((y) => isChip(y) && !y.classList.contains("always")) : [c]) {
        if (x.dataset.members) {
          paintProfiles(x, idsOf(x).filter((id) => next.includes(id) && !x.dataset.fixed?.split("\t").includes(id)));
          continue;
        }
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
    go.rows = true; // its chips are a row's: an agent's profiles go into one
    return go;
  }
  // What All did, said of the item: on for how many of the agents it went
  // to, and which of them couldn't be given it, and why.
  function reportAll(v, me) {
    const what = ({ servers: "mcp:", hooks: "hook:", env: "env:", subagents: "subagent:" }[me.list] || "skill:") + me.name;
    const shown = [...me.box.children].filter((x) => isChip(x) && !cannot(x)).flatMap((x) => idsOf(x).filter((id) => !x.dataset.fixed?.split("\t").includes(id)));
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
    // What the reader hid stays hidden while it still fails and nothing
    // else does: every change writes every agent again, so a file magpie
    // can't stat fails at each one (#3745). A failure not hidden brings the
    // card back whole, and an item written meanwhile is new when it fails
    // again. v.problems is everything that stands, whatever answered.
    const failing = new Set((v.problems || []).filter(failure).map(problemKey));
    for (const k of hid) if (!failing.has(k)) hid.delete(k);
    if ([...failing].some((k) => !hid.has(k))) hid.clear();
    const was = lib && shelf();
    lib = v;
    taken++;
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
    const key = (c) => c.dataset.agent || c.dataset.members;
    if (was.length !== now.length || was.some((c, i) => key(c) !== key(now[i]))) return false;
    was.forEach((c, i) => {
      const n = now[i];
      if (n.dataset.members) { c.dataset.lit = n.dataset.lit; c.dataset.fixed = n.dataset.fixed; c.querySelector(":scope > .cnt")?.replaceWith(n.querySelector(":scope > .cnt").cloneNode(true)); }
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
  // The first read is a glance: the page but for the skills the agents
  // have of their own and which copies are out of date, which take reading
  // their skill folders through, and on a machine with many big skills
  // that took minutes before anything showed (0day404, #541). It is drawn
  // at once, and the whole page after it; only the Skills tab shows what
  // the glance left out, so the others aren't drawn again for it.
  async function load(quiet) {
    let glanced = false;
    readFollow();
    if (!lib) {
      renderLoading();
      const at = taken;
      try {
        const g = await api("library?glance=1");
        if (!lib && taken === at) {
          take(g);
          glanced = true;
          render();
        }
      } catch {}
    }
    try {
      const at = taken;
      const v = await api("library");
      // a change answered meanwhile, with the library as it is after it
      if (taken !== at) return;
      const was = lib && seen(lib);
      take(v);
      if (glanced && tab !== "skills") return;
      if ((quiet === true || glanced) && seen(lib) === was) return;
      // opening the page (its nav clicked, while on it too) draws it again
      // only for a change, or for RTK's tab, which is asked again: a page
      // drawn over again replaced the row under the reader's pointer (#3099)
      if (quiet === "open" && seen(lib) === was && tab !== "rtk" && tab !== "bili" && page.querySelector(":scope > .lib-body")) return;
      rtk = null; // asked again when its tab is drawn: an agent may have been installed since
      bili = null;
      piView = null;
      render();
    } catch (e) {
      status(e.message, "err");
    }
  }
  // the library as the page shows it: what a change did is said once, not shown
  const seen = (v) => JSON.stringify({ ...v, result: undefined });

  // Every change answers with the page as it is after it, and what it did;
  // one to the instructions with them alone, the rest kept as it is (#1624).
  // One change is on its way at a time: while it is, the page's buttons
  // can't be pressed and the one pressed says it is saving, so a second
  // click doesn't make or remove a set twice (Hipple, #1624: a minute a
  // click with nothing to show for it).
  let changing = false;
  async function change(path, body, done) {
    if (changing) return false;
    changing = true;
    const ev = window.event;
    const pressed = ev?.type === "click" && ev.target?.closest?.("button:not(.lib-switch):not(.lib-radio)");
    const held = [...document.querySelectorAll("#view-library button, #modal.lib button")].filter((b) => !b.disabled);
    for (const b of held) b.disabled = true;
    page.setAttribute("aria-busy", "true");
    if (pressed && pressed.textContent.trim()) saving(pressed, t("Saving…"));
    status(t("Saving…"), "", 120000);
    let ok = false;
    try {
      const v = await api("library/" + path, body);
      take(v.part === "instructions" ? { ...lib, instructions: v.instructions, problems: v.problems, result: v.result } : v);
      status(""); // what it did, said next, or nothing for one that says nothing
      report(v.result, done);
      ok = true;
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      return false;
    } finally {
      changing = false;
      page.removeAttribute("aria-busy");
      for (const b of held) b.disabled = false;
      if (pressed) doneSaving(pressed);
      if (ok) render();
    }
  }
  // given, for a row's agent chips, is the agents that have it now: an
  // agent written that isn't one of them had it taken out (#332).
  function report(res, done, given) {
    if (!res) return;
    // subagents imported from a repository (subagent_import.go)
    const sa = res.subagents;
    if (sa) {
      const n = sa.added?.length || 0, parts = [];
      if (n) parts.push(n === 1 ? t("1 subagent imported") : t("{count} subagents imported", { count: n }));
      if (sa.updated?.length) parts.push(t("updated: {names}", { names: sa.updated.join(", ") }));
      if (sa.had?.length) parts.push(t("already in the library: {names}", { names: sa.had.join(", ") }));
      if (sa.kept?.length) parts.push(t("changed here, left as they are: {names}", { names: sa.kept.join(", ") }));
      const warn = res.problems?.length;
      if (warn) { const p = res.problems[0]; parts.push(t("{agent}: {error}", { agent: tilde(nameOf(p.agent)), error: p.error })); }
      status(parts.join(" · "), warn ? "warn" : "ok", warn ? 10000 : 6000);
      return;
    }
    // an update that found its source without the skill: said so, not
    // as done or failed (スライム on Discord)
    if (res.gone?.length) {
      status(goneSaid(res.gone), "warn", 10000);
      return;
    }
    // skills installed together (lc on Discord): those the library had
    // already, and those left out because another by that name is in the
    // way, are named, beside how many were installed
    const had = res.had || [], skipped = res.skipped || [], linked = res.linked || [];
    if (had.length || skipped.length || linked.length) {
      const n = res.installed?.length || 0, parts = [];
      if (n) parts.push(n === 1 ? t("1 skill installed") : t("{n} skills installed", { n }));
      // a skill from a folder, linked to the one installed (skill_link.go)
      if (linked.length) parts.push(t("updates from GitHub now: {names}", { names: linked.join(", ") }));
      if (res.linkedKept?.length) parts.push(t("files of their own kept with the backups: {names}", { names: res.linkedKept.join(", ") }));
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
    if (done) status(typeof done === "function" ? done(res) : done, "ok");
    else if (out && out === n) status(n === 1 ? t("Removed from {agent}", { agent: nameOf(res.changed[0]) }) : t("Removed from {n} agents", { n }), "ok");
    else if (out) status(t("{n} agents updated", { n }), "ok");
    else if (n) status(n === 1 ? t("Written to {agent}", { agent: nameOf(res.changed[0]) }) : t("Written to {n} agents", { n }), "ok");
    else status(t("Saved — the agents already had it"), "ok");
  }

  // What the last change couldn't write, whole: a chip's amber dot holds
  // only its own item's, so an agent's instructions, its MCP file, a
  // project or a skill that wouldn't update were said once in a toast
  // ("see Library") and shown nowhere.
  // Each is listed on the tab it is about; another tab says only how many
  // wait where, with a button to go there: Claude Desktop's own skills
  // listed one by one on the MCP servers tab read as duplicated servers
  // (#1896). An agent's own skills in the library's way are one row for
  // the agent, settled together, not a write that failed: Try again, which
  // can't change them, is offered only beside a write that did fail.
  function problemsCard() {
    // the write failures the reader hid are left out while they are all
    // that fails; an agent's own skills, still to settle, stay
    const all = lib.problems || [];
    const quiet = all.some(failure) && all.filter(failure).every((p) => hid.has(problemKey(p)));
    const list = quiet ? all.filter((p) => !failure(p)) : all;
    if (!list.length) return null;
    const here = list.filter((p) => !problemTab(p.what) || problemTab(p.what) === tab);
    const away = {};
    for (const p of list) if (!here.includes(p)) away[problemTab(p.what)] = (away[problemTab(p.what)] || 0) + 1;
    const owns = new Map(); // agent → its own skills by the library's names
    const rest = [];
    for (const p of here) {
      if (!failure(p)) {
        if (!owns.has(p.agent)) owns.set(p.agent, []);
        owns.get(p.agent).push(p.what.slice("skill:".length));
      } else rest.push(p);
    }
    const card = el("div", "lib-problems");
    card.setAttribute("role", "status");
    const ttl = el("div", "lib-problems-head");
    const failed = rest.length || list.some((p) => !here.includes(p) && failure(p));
    ttl.append(el("span", "lib-problems-dot"), el("b", "", failed ? t("Some of the Library couldn't be written") : t("Some agents have skills of their own by the library's names")));
    if (failed) {
      const again = button(t("Try again"), "action", () => change("all/sync", {}));
      again.title = t("Writes the library into every agent again");
      // the reader who can't mend it here, or won't now, puts it away; a
      // change's toast still says what failed, and something new that
      // can't be written brings it back (#3745)
      const hide = button("", "lib-x", () => hideFailed(card));
      hide.append(svg("M4.5 4.5l7 7M11.5 4.5l-7 7", 11, 1.6));
      hide.title = t("Hide until something new can't be written");
      ttl.append(again, hide);
    }
    card.append(ttl);
    const ul = el("ul");
    for (const p of rest) {
      const li = el("li");
      const who = p.what.startsWith("project:") ? tilde(p.agent) : p.agent ? nameOf(p.agent) : "";
      if (who) li.append(el("b", "", who), el("span", "lib-problems-sep", " · "));
      li.append(el("span", "", problemWhat(p.what)), el("span", "lib-problems-err", p.error));
      ul.append(li);
    }
    // an agent's own skills in the library's way are settled right here:
    // the library's in their place (the agent's kept aside), or the agent's
    for (const [id, names] of owns) {
      const li = el("li", "lib-problems-own");
      const who = nameOf(id), name = names.join(", ");
      const one = names.length === 1;
      li.append(el("b", "", who), el("span", "lib-problems-sep", " · "), el("span", "", one ? t("Skill {name}", { name }) : t("Skills {names}", { names: name })));
      // Claude Desktop's are Anthropic's built in or made in Desktop, listed
      // in its manifest: kept, never put aside for the library's
      const desktop = id === "claude-desktop";
      li.append(el("span", "lib-problems-err", desktop
        ? (one ? t("Claude Desktop has its own {name}, so it doesn't get the library's", { name }) : t("Claude Desktop has its own of these, so it doesn't get the library's"))
        : (one ? t("{agent} has a skill of its own called {name}, not the same as the library's", { agent: who, name }) : t("{agent} has skills of its own by these names, not the same as the library's", { agent: who }))));
      const body = one ? { name: names[0], agent: id } : { names, agent: id };
      const fix = el("div", "lib-problems-fix");
      if (!desktop) {
        const use = button(t("Use the library's"), "action", () => change("skills/use-library", body, one ? t("{agent} has the library's {name} now; its own is kept with the backups", { agent: who, name }) : t("{agent} has the library's {names} now; its own are kept with the backups", { agent: who, names: name })));
        use.title = one ? t("Sets {agent}'s own {name} aside with the backups and gives it the library's in its place", { agent: who, name }) : t("Sets {agent}'s own {names} aside with the backups and gives it the library's in their place", { agent: who, names: name });
        fix.append(use);
      }
      const keep = button(t("Keep {agent}'s", { agent: who }), "action", () => change("skills/keep-own", body, one ? t("{agent} keeps its own {name}", { agent: who, name }) : t("{agent} keeps its own {names}", { agent: who, names: name })));
      keep.title = one ? t("{agent} keeps its own {name}, and the library no longer gives it this skill", { agent: who, name }) : t("{agent} keeps its own {names}, and the library no longer gives it these skills", { agent: who, names: name });
      fix.append(keep);
      li.append(fix);
      ul.append(li);
    }
    if (ul.childElementCount) card.append(ul);
    const tabNames = { instructions: t("Instructions"), mcp: t("MCP servers"), skills: t("Skills"), hooks: t("Hooks"), env: t("Environment variables"), subagents: t("Subagents"), plugins: t("Claude plugins") };
    const others = Object.keys(away).filter((k) => k in tabNames);
    if (others.length) {
      const more = el("div", "lib-problems-away");
      for (const k of others) {
        const go = button(t("{tab}: {n} more", { n: away[k], tab: tabNames[k] }), "action", () => openTab(k));
        go.dataset.tab = k;
        more.append(go);
      }
      card.append(more);
    }
    return card;
  }
  // a write that failed, as against an agent's own skill in the library's
  // way, which is the agent's to settle
  function failure(p) {
    return !(p.own && p.what.startsWith("skill:") && p.agent);
  }
  // a failure is the same from one sync to the next by its agent and its
  // item: the error can name a temp file new at each write (edit.WriteAtomic)
  function problemKey(p) {
    return p.agent + "\n" + p.what;
  }
  function hideFailed(card) {
    const focused = card.contains(document.activeElement);
    for (const p of lib.problems || []) if (failure(p)) hid.add(problemKey(p));
    // the view keeps its scroll, as render keeps it for Try again:
    // Chromium's scroll anchoring would move it as the card goes
    const top = page.scrollTop;
    syncProblems();
    page.scrollTop = top;
    // the keyboard's place: the card's next button, or the tab open
    if (focused) (page.querySelector(":scope > .lib-problems button") || page.querySelector(".lib-tabs .opt.on"))?.focus({ preventScroll: true });
  }
  // the tab a problem is about; "" is one shown on every tab
  function problemTab(what) {
    return { instructions: "instructions", mcp: "mcp", skill: "skills", skills: "skills", project: "skills", hook: "hooks", hooks: "hooks", env: "env", subagent: "subagents", plugin: "plugins" }[what.split(":")[0]] || "";
  }
  function problemWhat(what) {
    const [kind, ...rest] = what.split(":");
    const name = rest.join(":");
    if (kind === "instructions") return t("Instructions");
    if (kind === "mcp") return name ? t("MCP server {name}", { name }) : t("MCP servers");
    if (kind === "skill") return t("Skill {name}", { name });
    if (kind === "hook") return t("Hook {name}", { name });
    if (kind === "subagent") return t("Subagent {name}", { name });
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
    } else if (which === "rtk" || which === "bili") {
      box.append(rows(1, false), head("60px"), rows(which === "rtk" ? 5 : 3, true));
    } else if (which === "memory") {
      box.append(rows(1, true), rows(6, false));
    } else if (which === "pi") {
      box.append(head("60px"), rows(4, true));
    } else {
      box.append(rows(5, true));
    }
    return box;
  }

  function openTab(id) {
    tab = id;
    try { localStorage.setItem("magpie.libTab", id); } catch {}
    // RTK's count read afresh each time its tab is opened: an agent run
    // since adds to it (#741); the page keeps the last one until then
    if (id === "rtk" && rtk) loadRTK();
    // and which agents have billion-context's plugin: bili switched outside magpie
    if (id === "bili" && bili) loadBili();
    // Pi's extensions too: one installed or changed outside magpie since
    if (id === "pi" && piView) loadPi();
    // memory too: agents save to it while the page is open
    if (id === "memory" && mem) loadMemory();
    if (!lib) return page.querySelector(":scope > .lib-skel")?.replaceWith(skeleton(id));
    render(); syncLists();
  }

  // the tabs and the Library folder; without the library yet (lib is null
  // while it loads) the tabs have no counts and the folder waits
  function libHead(counts) {
    const head = el("div", "lib-head");
    const n = (k) => (counts?.[k] ? " · " + counts[k] : "");
    const tabs = segs(agentless() ? [["mcp", t("MCP servers") + n("mcp")]] : [
      ["instructions", t("Instructions")],
      ["memory", t("Memory")],
      ["mcp", t("MCP servers") + n("mcp")],
      ["skills", t("Skills") + n("skills")],
      ["hooks", t("Hooks") + n("hooks")],
      ["env", t("Environment variables") + n("env")],
      ["subagents", t("Subagents") + n("subagents")],
      ["plugins", t("Claude plugins") + n("plugins")],
      ["rtk", "RTK"],
      ["bili", "billion-context"],
      ...(hasPi() ? [["pi", t("Pi packages")]] : []),
    ], tab, openTab);
    tabs.classList.add("lib-tabs");
    head.append(tabs);
    // the buttons keep together at the end, on a line of their own when the
    // window is too narrow for the tabs and them
    const acts = el("span", "lib-headacts");
    // The market sits under everything the tab lists, many screens down with
    // dozens of skills (#1348): the strip that stays at the top goes to it.
    if (lib && (tab === "mcp" || tab === "skills")) {
      const go = button("", "lib-discover", (e) => {
        const mk = page.querySelector(`.mk[data-market="${tab}"]`);
        if (!mk || !window.scrollOnPurpose?.(e, 1500)) return;
        // its heading just under the strip, however many lines that takes
        const top = mk.getBoundingClientRect().top - page.getBoundingClientRect().top + page.scrollTop - head.getBoundingClientRect().height - 8;
        page.scrollTo({ top, behavior: calm() ? "auto" : "smooth" });
        mk.querySelector("input")?.focus({ preventScroll: true });
      });
      go.append(glyph(GLYPH.search, "lib-mini"), el("span", "", t("Discover")));
      go.title = tab === "mcp" ? t("Go to the MCP servers you can add, under the list") : t("Go to the skills you can add, under the list");
      // a narrow window's title bar shows the icon alone (app.css
      // .tools-tight): the name stays the button's, not only its tooltip's
      go.setAttribute("aria-label", t("Discover"));
      acts.append(go);
    }
    const more = button("", "lib-more", () => lib && reveal(lib.dir));
    more.append(glyph(GLYPH.folder, "lib-mini"), el("span", "", t("Library folder")));
    more.setAttribute("aria-label", t("Library folder"));
    if (lib) more.title = tilde(lib.dir);
    else more.disabled = true;
    // magpie's backups beside it, on every tab: at the foot of a long
    // page they were hard to find (Dade on Discord: 备份在最底部)
    const back = button("", "lib-backups", () => lib && reveal(lib.backups));
    back.append(glyph(GLYPH.backup, "lib-mini"), el("span", "", t("Backups")));
    back.setAttribute("aria-label", t("Backups"));
    if (lib) back.title = t("magpie keeps a copy of every file before it writes it.") + "\n" + tilde(lib.backups);
    else back.disabled = true;
    acts.append(more, back);
    head.append(acts);
    return head;
  }

  // ---------- the page ----------

  function render() {
    if (!lib) return;
    const top = page.scrollTop;
    const focus = document.activeElement?.dataset?.lib; // keep the caret where the reader is typing
    const caret = focus ? [document.activeElement.selectionStart, document.activeElement.selectionEnd] : null;
    // an editor open before is open still, not opened again: drawn anew
    // it would fade and slide in on every render, which a poll's made a
    // jitter every 2s (Nietzsche_Li on X)
    const had = new Set([...page.querySelectorAll(".lib-detail[data-detail]")].map((d) => d.dataset.detail));
    page.replaceChildren();
    fits = [];
    // the MCP servers alone with no agent to write to; the tab the reader
    // last picked is kept for when there is one
    if (agentless()) tab = "mcp";
    // Pi's tab only while a Pi is here; the pick is kept for when one is
    else if (tab === "pi" && !hasPi()) tab = "instructions";
    const head = libHead({ mcp: lib.servers.length, skills: lib.skills.length, hooks: (lib.hooks || []).length, env: (lib.env || []).length, subagents: (lib.subagents || []).length, plugins: (lib.plugins || []).length });
    page.append(head);
    head.classList.toggle("stuck", top > 0);
    // what changed in a tab is drawn in place: only another tab (or the
    // first one after the skeleton) fades in
    const problems = problemsCard();
    if (problems) page.append(problems);
    const body = el("div", "lib-body" + (shown !== tab ? " enter" : ""));
    shown = tab;
    const fnote = followNote();
    if (fnote) body.append(fnote);
    if (tab === "instructions") renderInstructions(body);
    else if (tab === "mcp") renderServers(body);
    else if (tab === "rtk") renderRTK(body);
    else if (tab === "bili") renderBili(body);
    else if (tab === "memory") renderMemory(body);
    else if (tab === "pi") renderPi(body);
    else if (tab === "hooks") renderHooks(body);
    else if (tab === "env") renderEnv(body);
    else if (tab === "subagents") renderSubagents(body);
    else if (tab === "plugins") renderPlugins(body);
    else renderSkills(body);
    page.append(body);
    for (const n of body.querySelectorAll(".row-head .note")) n.title = n.textContent;
    for (const d of page.querySelectorAll(".lib-detail[data-detail]")) if (had.has(d.dataset.detail)) d.classList.add("kept");
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

  // A computer that follows the server (Settings → Sync → This computer)
  // has what it changes in the Library replaced by the next sync: said
  // above the tabs the sync carries, with the way to that setting (a
  // report on X: a skill added here was taken for deleted by the update).
  let follow = null; // davsync's view, while this computer follows
  async function readFollow() {
    let v = null;
    try { v = await api("davsync"); } catch {}
    const was = follow;
    follow = v && v.on && v.followServer ? { library: !!v.library, memory: !!v.memory } : null;
    if (JSON.stringify(follow) !== JSON.stringify(was) && lib && page.isConnected) render();
  }
  function followNote() {
    const part = tab === "memory" ? "memory" : ["instructions", "mcp", "skills", "hooks", "env", "subagents", "plugins"].includes(tab) ? "library" : "";
    if (!part || !follow?.[part]) return null;
    const p = el("p", "lib-intro lib-follow", t("This computer follows the server: what you change here is replaced by the next sync.") + " ");
    const go = button(t("Sync settings"), "link", (e) => openSettingsAt(e, "sync", "#syncList .sync-follows"));
    p.append(go);
    return p;
  }

  // ---------- memory ----------

  // What the agents know about the user and their projects, shared by all
  // of them (internal/memory): agents search it and save to it through the
  // Magpie Memory server, and the ones ticked for instructions read its
  // digest. A memory an agent only guessed waits under Suggested until the
  // user keeps it.
  let mem = null;        // /api/memory
  let dist = null;       // /api/memory/distill: conversations read into memory
  let distPoll = 0;
  let sem = null;        // /api/memory/semantic: search by meaning (memembed)
  let semPoll = 0;
  let memRank = null;    // {q, status, ids}: the search's ranking by meaning, best first
  let rankT = 0;
  let memLoading = false;
  let memQ = "";         // the search typed
  let memStatus = "active";
  let memScope = "";     // "" every scope
  let memOpen = null;    // the memory open to edit, or "new"
  let memDraft = null;   // its fields as typed
  let memRemoving = null; // the memory whose Delete was clicked once
  // Select (RealZeroP27 on X): a box on each memory shown, and a bar that
  // keeps, archives, restores or deletes the ones picked at once
  let memPicking = false;
  const memPicked = new Set(); // ids
  let memAnchor = null;  // the one picked or unpicked last: a Shift-click's range starts there
  let memSure = false;   // the bar's Delete was clicked once
  let memShown = [];     // the memories listed, in their order
  const MEM_KINDS = ["profile", "preference", "rule", "reference", "fact", "procedure", "episode"];
  const kindName = (k) => ({ profile: t("About you"), preference: t("Preference"), rule: t("Rule"), reference: t("Reference"), fact: t("Fact"), procedure: t("How-to"), episode: t("Decision") })[k] || k;
  function scopeName(s) {
    if (!s || s === "global") return t("Everywhere");
    if (s.startsWith("project:")) {
      const k = s.slice(8), p = mem?.projects?.[k];
      return p?.name || k;
    }
    if (s.startsWith("agent:")) return t("Only {agent}", { agent: nameOf(s.slice(6)) });
    return s;
  }
  async function loadMemory() {
    if (memLoading) return;
    memLoading = true;
    try {
      [mem, dist, sem] = await Promise.all([api("memory"), api("memory/distill").catch(() => null), api("memory/semantic").catch(() => null)]);
    } catch (e) {
      status(e.message, "err");
    }
    memLoading = false;
    if (tab === "memory" && mem) render();
    distWatch();
    semWatch();
    rankSoon();
  }
  function memDirty() {
    if (!memOpen || !memDraft) return false;
    const m = mem?.memories.find((x) => x.id === memOpen);
    if (!m) return !!memDraft.text.trim();
    return memDraft.text !== m.text || memDraft.why !== (m.why || "") || memDraft.tags !== (m.tags || []).join(", ") || memDraft.kind !== m.kind || memDraft.scope !== m.scope;
  }
  let memBusy = false;
  async function memChange(path, body, done) {
    if (memBusy) return false;
    memBusy = true;
    page.setAttribute("aria-busy", "true");
    try {
      const v = await api(path, body);
      mem = v;
      if (v.problem) status(v.problem, "err", 10000);
      else if (done) status(done, "", 4000);
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      return false;
    } finally {
      memBusy = false;
      page.removeAttribute("aria-busy");
      render();
    }
  }
  // a picker: the app's menu, never a <select>
  function menuButton(text, cls, onclick) {
    const b = el("button", "sess-pick lib-menu " + cls);
    b.type = "button";
    b.setAttribute("aria-haspopup", "menu");
    b.append(el("span", "", text), svg(CHEV, 11, 1.6));
    b.onclick = (e) => { e.stopPropagation(); onclick(e); };
    return b;
  }
  const memTags = (s) => s.split(/[,，]/).map((x) => x.trim()).filter(Boolean);
  function openMemory(m) {
    memRemoving = null;
    if (!m) { memOpen = "new"; memDraft = { text: "", why: "", tags: "", kind: "preference", scope: memScope || "global" }; return; }
    memOpen = m.id;
    memDraft = { text: m.text, why: m.why || "", tags: (m.tags || []).join(", "), kind: m.kind, scope: m.scope };
  }
  async function saveMemory() {
    if (!memDraft || !memDraft.text.trim()) return;
    const isNew = memOpen === "new";
    const body = { text: memDraft.text.trim(), why: memDraft.why.trim(), tags: memTags(memDraft.tags), kind: memDraft.kind, scope: memDraft.scope };
    if (!isNew) body.id = memOpen;
    if (await memChange("memory", body, isNew ? t("Remembered") : t("Saved"))) { memOpen = null; memDraft = null; render(); }
  }
  // every scope a memory is kept in, and the places one can be put
  function memScopes(withAgents) {
    const out = ["global"];
    for (const k of Object.keys(mem.projects || {})) out.push("project:" + k);
    for (const m of mem.memories) if (!out.includes(m.scope) && (withAgents || !m.scope.startsWith("agent:"))) out.push(m.scope);
    if (withAgents) for (const a of shownAgents()) if (!out.includes("agent:" + a.id.split("@")[0])) out.push("agent:" + a.id.split("@")[0]);
    return out;
  }
  function renderMemory(body) {
    body.append(intro(t("What your agents know about you and your projects, shared by all of them. They search it and save to it with Magpie Memory, and the agents switched on under Instructions read a summary of it.")));
    if (!mem) {
      loadMemory();
      body.append(skeleton("memory").lastChild);
      return;
    }
    const card = el("div", "list lib-list lib-memcard");
    const row = el("div", "row lib-row lib-memdigest");
    const who = el("div", "who");
    who.append(el("div", "name", t("In the agents' instructions")));
    who.append(el("div", "sub", mem.agents.length ? mem.agents.map(nameOf).join(", ") : t("No agent is switched on under Instructions")));
    const sw = toggle(mem.digest, t("In the agents' instructions"), (on) => memChange("memory/digest", { on }, on ? t("The agents read the memories' summary") : t("The memories' summary is taken out of the agents' instructions")));
    row.append(glyph(GLYPH.doc), who, sw);
    card.append(row);
    if (dist) card.append(distillRow(), dreamRow());
    if (sem) card.append(semRow());
    if (!lib.servers.some((s) => s.name === "magpie-memory")) {
      const r = el("div", "row lib-row lib-memtools");
      const w = el("div", "who");
      w.append(el("div", "name", "Magpie Memory"), el("div", "sub", t("Add it for agents to search and save memories")));
      r.append(glyph(GLYPH.search), w, button(t("Add"), "action", () => change("market/server", { id: "magpie-memory", values: {} }, t("{name} is in the library now", { name: "Magpie Memory" }))));
      card.append(r);
    }
    body.append(card);

    const all = mem.memories;
    const count = (st) => all.filter((m) => st === "archived" ? m.status === "archived" || m.status === "superseded" : m.status === st).length;
    const bar = el("div", "lib-membar");
    const q = input(memQ, t("Search memories"));
    q.type = "search";
    q.className = "lib-memq";
    q.dataset.lib = "memq";
    q.oninput = () => { memQ = q.value; render(); rankSoon(); };
    q.onkeydown = (e) => e.stopPropagation();
    const sugg = count("candidate");
    const st = segs([
      ["active", t("In use") + (count("active") ? " · " + count("active") : "")],
      ...(sugg || memStatus === "candidate" ? [["candidate", t("To review") + (sugg ? " · " + sugg : "")]] : []),
      ["archived", t("Archived") + (count("archived") ? " · " + count("archived") : "")],
    ], memStatus, (v) => { memStatus = v; memOpen = null; render(); rankSoon(); });
    st.classList.add("lib-memstatus");
    const sc = menuButton(memScope ? scopeName(memScope) : t("All"), "lib-memscope", () => openProtoMenu(sc, [{ v: "", name: t("Everywhere and every project"), literalName: true, note: "" }, ...memScopes(true).filter((s) => all.some((m) => m.scope === s)).map((s) => ({ v: s, name: scopeName(s), literalName: true, note: "" }))], memScope, (v) => { memScope = v; render(); }, "Where it applies"));
    const words = memQ.trim().toLowerCase().split(/\s+/).filter(Boolean);
    // with search by meaning on, what it ranked comes first, best first,
    // then what holds the words typed and it didn't rank
    const ranked = memRank && memRank.q === memQ && memRank.status === memStatus ? memRank.ids : [];
    const at = new Map(ranked.map((id, i) => [id, i]));
    const shown = all.filter((m) => (memStatus === "archived" ? m.status === "archived" || m.status === "superseded" : m.status === memStatus)
      && (!memScope || m.scope === memScope)
      && (at.has(m.id) || words.every((w) => [m.text, m.why || "", ...(m.tags || []), kindName(m.kind), scopeName(m.scope)].join(" ").toLowerCase().includes(w))))
      .sort((a, b) => (at.has(b.id) - at.has(a.id)) || (at.get(a.id) - at.get(b.id)) || (!!b.pinned - !!a.pinned) || (b.updated > a.updated ? 1 : b.updated < a.updated ? -1 : 0));
    // what's picked is what's listed: a search, a filter or another
    // status leaves the rest unpicked, and an empty list ends Select
    memShown = shown;
    for (const id of memPicked) if (!shown.some((m) => m.id === id)) memPicked.delete(id);
    if (memAnchor && !memPicked.has(memAnchor) && !shown.some((m) => m.id === memAnchor)) memAnchor = null;
    if (memPicking && !shown.length) endMemPick();
    bar.append(q, sc, st, el("span", "grow"));
    if (memPicking || shown.length > 1) bar.append(memPickButton());
    bar.append(button("+ " + t("Add"), "action lib-memadd", () => { endMemPick(); openMemory(null); render(); }));
    body.append(bar);

    if (memStatus === "active" && !words.length && !memScope && !memPicking && (mem.suggestions || []).length) body.append(memSuggestions());
    const list = el("div", "list lib-list lib-mems" + (memPicking ? " picking" : ""));
    if (memOpen === "new") list.append(memDetail(null));
    for (const m of shown) list.append(...memRow(m));
    if (!shown.length && memOpen !== "new") {
      list.append(el("div", "lib-none", words.length || memScope ? t("No memory matches.")
        : memStatus === "candidate" ? t("Nothing to review.")
        : memStatus === "archived" ? t("Nothing archived.")
        : t("Nothing remembered yet. Tell an agent “remember that…”, or add one here.")));
    }
    body.append(list);
    if (memPicking) body.append(memPickBar());
  }

  // what memory suggests (memory.Suggest): a memory learned in several
  // projects to hold everywhere, a procedure searched for often to be a
  // skill; each taken or turned down for good
  function memSuggestions() {
    const list = el("div", "list lib-list lib-memsugg");
    for (const s of mem.suggestions) {
      const r = el("div", "row lib-row");
      r.dataset.sugg = s.key;
      const w = el("div", "who");
      w.append(el("div", "name", s.text));
      const acts = el("div", "lib-acts");
      if (s.to === "global") {
        w.append(el("div", "sub", t("Learned in {projects}", { projects: s.projects.map((k) => mem.projects?.[k]?.name || k).join(", ") })));
        acts.append(button(t("Use everywhere"), "action", () => memChange("memory/promote", { ids: s.ids }, t("It holds everywhere now"))));
      } else {
        w.append(el("div", "sub", t("Searched {n} times", { n: s.uses })));
        acts.append(button(t("Make a skill"), "action", async () => { if (await memChange("memory/skill", { id: s.ids[0] }, t("It is a skill in the library now"))) load(true); }));
      }
      acts.append(button(t("Not now"), "action", () => memChange("memory/decline", { key: s.key })));
      r.append(glyph(s.to === "global" ? GLYPH.web : GLYPH.skill), w, acts);
      list.append(r);
    }
    return list;
  }

  // ---------- some memories, picked, acted on at once ----------

  function endMemPick() {
    memPicking = false;
    memPicked.clear();
    memAnchor = null;
    memSure = false;
  }
  function memPickButton() {
    const b = button(memPicking ? t("Done") : t("Select"), "action lib-select lib-memselect", () => {
      const on = !memPicking;
      endMemPick();
      if (on) { memPicking = true; memOpen = null; memDraft = null; memRemoving = null; }
      render();
      if (on) requestAnimationFrame(() => page.querySelector(".lib-mems .lib-pickbox")?.focus({ preventScroll: true }));
    });
    b.dataset.alt = memPicking ? t("Select") : t("Done"); // as wide as the other, see library.css
    b.title = memPicking ? t("Stop picking memories") : t("Pick some memories and act on them together");
    return b;
  }
  // a click picks one or unpicks it; with Shift, every one from the last
  // clicked to this one takes the last one's state
  function memPick(id, range) {
    const ids = memShown.map((m) => m.id);
    const to = ids.indexOf(id), from = range && memAnchor ? ids.indexOf(memAnchor) : -1;
    if (from >= 0 && to >= 0) {
      const on = memPicked.has(memAnchor);
      for (const x of ids.slice(Math.min(from, to), Math.max(from, to) + 1)) on ? memPicked.add(x) : memPicked.delete(x);
    } else if (memPicked.has(id)) memPicked.delete(id);
    else memPicked.add(id);
    memAnchor = id;
    memSync();
  }
  function memPickAll(on) {
    for (const m of memShown) on ? memPicked.add(m.id) : memPicked.delete(m.id);
    memSync();
  }
  // every box and the bar as the picks are now, without drawing the page
  // again: the box with the focus keeps it
  function memSync() {
    memSure = false;
    for (const row of page.querySelectorAll(".lib-mems .lib-mem")) {
      const on = memPicked.has(row.dataset.id);
      row.classList.toggle("picked", on);
      const box = row.querySelector(".lib-pickbox");
      if (box) box.checked = on;
    }
    page.querySelector(".lib-mempickbar")?.replaceWith(memPickBar());
  }
  // the keys a picked list answers to: ↑ ↓ move between the boxes (with
  // Shift, picking as they go), ⌘A / Ctrl+A picks every one shown, Escape
  // ends Select
  function memPickKeys(e, id) {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); endMemPick(); render(); page.querySelector(".lib-memselect")?.focus({ preventScroll: true }); return; }
    if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "a") { e.preventDefault(); e.stopPropagation(); memPickAll(memPicked.size !== memShown.length); return; }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    e.preventDefault();
    e.stopPropagation();
    const boxes = [...page.querySelectorAll(".lib-mems .lib-pickbox")];
    const i = boxes.findIndex((b) => b.closest(".lib-mem")?.dataset.id === id);
    const next = boxes[i + (e.key === "ArrowDown" ? 1 : -1)];
    if (!next) return;
    next.focus({ preventScroll: true });
    next.closest(".lib-mem")?.scrollIntoView({ block: "nearest" });
    if (e.shiftKey) {
      const nid = next.closest(".lib-mem").dataset.id;
      if (!memAnchor) memAnchor = id;
      memPick(nid, true);
    }
  }
  function memPickBar() {
    const bar = el("div", "row lib-row lib-pickbar lib-mempickbar");
    const n = memShown.filter((m) => memPicked.has(m.id)).length;
    const every = el("label", "lib-pickall");
    const all = el("input", "lib-pickbox lib-mempickall");
    all.type = "checkbox";
    all.checked = n > 0 && n === memShown.length;
    all.indeterminate = n > 0 && n < memShown.length;
    all.onclick = (e) => { e.stopPropagation(); memPickAll(n !== memShown.length); };
    all.onkeydown = (e) => { if (e.key === "Escape") memPickKeys(e, ""); };
    every.append(all, el("span", "", t("All {n}", { n: memShown.length })));
    bar.append(every, el("span", "lib-pickn" + (n ? "" : " none"), n ? t("{n} selected", { n }) : t("Shift-click picks a range")), el("span", "grow"));
    if (n) {
      const ids = memShown.filter((m) => memPicked.has(m.id)).map((m) => m.id);
      const act = async (path, body, done) => {
        const ok = await memChange(path, body, done);
        if (ok) { memPicked.clear(); memAnchor = null; memSure = false; render(); }
      };
      // what the status shown moves them to: kept, archived or brought back
      const to = memStatus === "candidate" ? ["active", t("Keep"), t("{n} kept", { n })]
        : memStatus === "active" ? ["archived", t("Archive"), t("{n} archived", { n })]
        : ["active", t("Restore"), t("{n} restored", { n })];
      bar.append(button(to[1], "action lib-updall lib-memto", () => act("memory/status", { ids, status: to[0] }, to[2])));
      // deleted on a second click, as one memory's Delete is
      const del = button(memSure ? t("Delete {n} for good", { n }) : t("Delete"), (memSure ? "primary danger-fill" : "action danger") + " lib-updall lib-memdel", () => {
        if (!memSure) {
          memSure = true;
          const b = memPickBar();
          bar.replaceWith(b);
          b.querySelector(".lib-memdel")?.focus({ preventScroll: true });
          return;
        }
        act("memory/remove", { ids }, t("{n} deleted", { n }));
      });
      bar.append(del);
      const c = button(t("Clear"), "lib-updall lib-pickclear", () => { memPicked.clear(); memAnchor = null; memSync(); });
      c.title = t("Unpick them all");
      bar.append(c);
    }
    bar.append(button(t("Done"), "action lib-updall lib-pickdone", () => { endMemPick(); render(); }));
    bar.onkeydown = (e) => { if (e.key === "Escape") memPickKeys(e, ""); };
    return bar;
  }
  // the model conversations are read and memory tidied with: the
  // automatic one or one picked, shared by both rows
  function memModelPick(d) {
    const modelName = (id) => { const m = d.models.find((x) => x.id === id); return m ? (m.name || m.id) : id; };
    const auto = d.auto ? t("Automatic") + " · " + modelName(d.auto) : t("Automatic");
    const pick = menuButton(d.model ? modelName(d.model) : auto, "lib-distmodel", () => openProtoMenu(pick, [
      { v: "", name: t("Automatic"), note: d.auto ? modelName(d.auto) : t("No models"), literalName: true },
      ...d.models.map((m) => ({ v: m.id, name: m.name || m.id, note: m.providerName || "", literalName: true }))],
    d.model || "", (v) => distChange("memory/distill", { on: d.on, model: v }), "Model", "", "left", false, d.models.length > 8 ? "Search models" : ""));
    return pick;
  }
  // the reasoning level the model reads and tidies at, when its levels
  // are known: Default sends none (RealZeroP27 on X)
  function memEffortPick(d) {
    if (!d.efforts?.length) return null;
    const shown = d.effort ? d.effortNow || d.effort : "";
    const b = menuButton(shown ? t(shown) : t("Default"), "lib-disteffort", () => openProtoMenu(b,
      [{ v: "", name: "Default", note: "The model's own" }, ...d.efforts.map((l) => ({ v: l, name: l, note: "" }))],
      shown, (v) => v !== (d.effort || "") && distChange("memory/distill", { on: d.on, model: d.model || "", effort: v }), "Thinking"));
    b.prepend(effortIcon({ options: d.efforts.map((l) => ({ value: l })), value: shown }));
    b.title = t("How hard this model thinks");
    return b;
  }
  // how far back conversations are read: 7 days unless picked, All the
  // latest 400 (memdistill.Listed); a longer range reads at once
  const rangeName = (n) => n === -1 ? t("All") : n === 365 ? t("1 year") : t("{n} days", { n });
  function memRangePick(d) {
    const cur = d.days || 7;
    const b = menuButton(rangeName(cur), "lib-distrange", () => openProtoMenu(b,
      (d.ranges || [7, 30, 90, 365, -1]).map((n) => ({ v: String(n), name: rangeName(n), literalName: true, note: n === -1 ? "The latest 400 conversations" : "" })),
      String(cur), (v) => +v !== cur && distChange("memory/distill", { on: d.on, model: d.model || "", days: +v }), "How far back it reads"));
    b.title = t("How far back it reads");
    return b;
  }
  // Conversations read into memory (internal/memdistill): what the
  // agents' finished sessions say worth keeping waits under To review.
  // Off until switched on; the model is the automatic one or one picked.
  function distillRow() {
    const d = dist;
    const r = el("div", "row lib-row lib-memdistill");
    const w = el("div", "who");
    w.append(el("div", "name", t("Learn from conversations")));
    let line = t("Reads each finished conversation for what to remember. You review each one before agents see it.");
    let err = false;
    // a run's progress, and what one left, so a long backlog is seen
    // going and Read now reads the rest (RealZeroP27 on X)
    const left = d.left ? " · " + t("{n} not read yet", { n: d.left }) : "";
    if (d.on && d.running) line = d.of ? t("Reading conversations… {n} of {m}", { n: d.doing || 0, m: d.of }) : t("Reading conversations…");
    else if (d.on && d.error) { line = t("Stopped {when}: {error}", { when: ago(d.last), error: d.error }) + left; err = true; }
    else if (d.on && d.last) line = t("Read {when} · {k} new to review", { when: ago(d.last), k: d.added || 0 }) + left;
    else if (d.on) line = t("Reads conversations quiet for 30 minutes, every hour");
    w.append(el("div", "sub" + (err ? " err" : ""), line));
    const acts = el("div", "lib-acts");
    if (d.on) {
      acts.append(memModelPick(d), ...[memEffortPick(d)].filter(Boolean), memRangePick(d));
      if (!d.running) acts.append(button(t("Read now"), "action lib-distnow", () => distChange("memory/distill/run", {})));
      else if (d.of > 1) acts.append(button(t("Stop"), "action lib-diststop", () => distChange("memory/distill/stop", {})));
    }
    acts.append(toggle(d.on, t("Learn from conversations"), (on) => distChange("memory/distill", { on, model: d.model || "" })));
    r.append(glyph(GLYPH.search), w, acts);
    return r;
  }
  // The nightly tidy (memdistill's Dream): memories that repeat are
  // merged, ones out of date set aside, the last night's undone at once.
  // The model is the one picked above; with reading off, it is picked here.
  function dreamRow() {
    const d = dist;
    const r = el("div", "row lib-row lib-memdream");
    const w = el("div", "who");
    w.append(el("div", "name", t("Tidy up every night")));
    const ch = d.changes || [];
    const merged = ch.filter((c) => c.action === "merge").length, aside = ch.length - merged;
    let line = t("Merges memories that repeat and sets aside ones out of date. What you wrote or pinned stays.");
    let err = false;
    if (d.dreaming) line = t("Tidying…");
    else if (d.dreamError) { line = t("Couldn't tidy: {error}", { error: d.dreamError }); err = true; }
    else if (d.dreamt && d.undone) line = t("Tidied {when} · undone", { when: ago(d.dreamt) });
    else if (d.dreamt && ch.length) line = t("Tidied {when} · {m} merged, {a} set aside", { when: ago(d.dreamt), m: merged, a: aside });
    else if (d.dreamt) line = t("Nothing to tidy {when}", { when: ago(d.dreamt) });
    w.append(el("div", "sub" + (err ? " err" : ""), line));
    const acts = el("div", "lib-acts");
    if (ch.length && !d.undone && !d.dreaming) acts.append(button(t("Undo"), "action lib-dreamundo", () => distChange("memory/dream/undo", {}, true)));
    if (d.dream && !d.on) acts.append(memModelPick(d), ...[memEffortPick(d)].filter(Boolean));
    if (d.dream && !d.dreaming && !d.running) acts.append(button(t("Tidy now"), "action lib-dreamnow", () => distChange("memory/dream/run", {})));
    acts.append(toggle(d.dream, t("Tidy up every night"), (on) => distChange("memory/dream", { on })));
    r.append(glyph(GLYPH.backup), w, acts);
    return r;
  }
  // Search by meaning (memembed): a small model on this computer reads
  // each memory once, and the search finds what means the same in other
  // words or another language. Switched off, the model is deleted.
  function semRow() {
    const s = sem;
    const r = el("div", "row lib-row lib-memsem");
    const w = el("div", "who");
    w.append(el("div", "name", t("Search by meaning")));
    let line = t("Finds memories that mean what you search for, in any language. Downloads a {size} model that runs on this computer.", { size: Math.round(s.size / 1e6) + " MB" });
    let err = false;
    if (s.on && s.downloading) line = t("Downloading the model · {p}%", { p: Math.floor(100 * s.done / (s.size || 1)) });
    else if (s.on && s.error && !s.installed) { line = t("Couldn't download the model: {error}", { error: s.error }); err = true; }
    else if (s.on && s.failed) { line = t("Couldn't read the memories: {error}", { error: s.failed }); err = true; }
    else if (s.on && s.installed && s.read < s.of) line = t("Reading the memories · {n} of {m}", { n: s.read, m: s.of });
    else if (s.on && s.installed) line = t("Finds memories that mean what you search for, in any language, on this computer.");
    w.append(el("div", "sub" + (err ? " err" : ""), line));
    const acts = el("div", "lib-acts");
    if (s.on && s.error && !s.installed && !s.downloading) acts.append(button(t("Try again"), "action lib-semretry", () => semChange(true)));
    acts.append(toggle(s.on, t("Search by meaning"), (on) => semChange(on)));
    r.append(glyph(GLYPH.search), w, acts);
    return r;
  }
  async function semChange(on) {
    try {
      sem = await api("memory/semantic", { on });
    } catch (e) {
      status(e.message, "err", 6000);
    }
    memRank = null;
    render();
    semWatch();
    rankSoon();
  }
  // while the model downloads or the memories are read, the row is read
  // again every 2s
  function semWatch() {
    clearTimeout(semPoll);
    const s = sem;
    if (!s?.on || !(s.downloading || s.reading || (s.installed && s.read < s.of && !s.failed))) return;
    semPoll = setTimeout(async () => {
      if (tab !== "memory") return;
      const was = s.installed, before = JSON.stringify(sem);
      try { sem = await api("memory/semantic"); } catch {}
      if (JSON.stringify(sem) !== before) redrawRow(".lib-memsem", semRow);
      semWatch();
      if (!was && sem.installed) rankSoon();
    }, 2000);
  }
  // the search, ranked by meaning a moment after the typing stops
  function rankSoon() {
    clearTimeout(rankT);
    if (!sem?.on || !sem.installed || !memQ.trim()) { memRank = null; return; }
    rankT = setTimeout(async () => {
      const q = memQ, st = memStatus;
      let r;
      try { r = await api("memory/rank", { q, status: st }); } catch { return; }
      if (q !== memQ || st !== memStatus || tab !== "memory") return;
      memRank = r.ids ? { q, status: st, ids: r.ids } : null;
      render();
    }, 250);
  }
  async function distChange(path, body, reload) {
    try {
      dist = await api(path, body);
      if (reload) mem = await api("memory");
    } catch (e) {
      status(e.message, "err", 6000);
    }
    render();
    distWatch();
  }
  // while a run goes, the row is read again every 2s; when it ends the
  // memories are too, for what it proposed
  function distWatch() {
    clearTimeout(distPoll);
    if (!dist?.running && !dist?.dreaming) return;
    distPoll = setTimeout(async () => {
      if (tab !== "memory") return;
      const before = JSON.stringify(dist), memWas = JSON.stringify(mem);
      try {
        dist = await api("memory/distill");
        if (!dist.running && !dist.dreaming) mem = await api("memory");
      } catch {}
      if (JSON.stringify(mem) !== memWas) render();
      else if (JSON.stringify(dist) !== before) { redrawRow(".lib-memdistill", distillRow); redrawRow(".lib-memdream", dreamRow); }
      distWatch();
    }, 2000);
  }
  // a poll's news drawn in its own row of the memory card, so the
  // memories under it, and the one open to edit, stay as they are
  function redrawRow(sel, make) {
    const old = page.querySelector(".lib-memcard > " + sel);
    if (old) old.replaceWith(make());
    else render();
  }
  function memRow(m) {
    const isOpen = !memPicking && memOpen === m.id;
    const row = el("div", "row lib-row click lib-mem" + (isOpen ? " open" : ""));
    row.dataset.id = m.id;
    const who = el("div", "who");
    who.append(el("div", "name", m.text));
    const sub = [scopeName(m.scope), ...(m.tags || []).map((x) => "#" + x)];
    const from = m.origin === "agent" || m.origin === "inferred" ? (m.sources || []).find((s) => s.agent)?.agent : "";
    if (from) sub.push(t("from {agent}", { agent: nameOf(from) }));
    who.append(el("div", "sub", sub.join(" · ")));
    const tags = el("div", "lib-tags");
    if (m.pinned) tags.append(tag(t("Pinned"), "", t("Given to the agents first")));
    tags.append(tag(kindName(m.kind)));
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(who, tags, chev);
    if (memPicking) {
      // a row with a box, which its click ticks rather than opening it
      const box = el("input", "lib-pickbox");
      box.type = "checkbox";
      box.checked = memPicked.has(m.id);
      box.setAttribute("aria-label", m.text);
      box.onclick = (e) => { e.stopPropagation(); memPick(m.id, e.shiftKey); };
      box.onkeydown = (e) => memPickKeys(e, m.id);
      row.prepend(box);
      row.classList.toggle("picked", box.checked);
      // a Shift-click picks a range, not the text between
      row.onmousedown = (e) => { if (e.shiftKey) e.preventDefault(); };
      row.onclick = (e) => { memPick(m.id, e.shiftKey); box.focus({ preventScroll: true }); };
      return [row];
    }
    row.onclick = () => { if (isOpen) { memOpen = null; memDraft = null; } else openMemory(m); render(); };
    return isOpen ? [row, memDetail(m)] : [row];
  }
  function memDetail(m) {
    const d = memDraft;
    const det = el("div", "lib-detail lib-memdetail");
    det.dataset.detail = "mem:" + (m ? m.id : "new");
    const ta = el("textarea", "lib-text small");
    ta.dataset.lib = "mem:" + (m ? m.id : "new");
    ta.value = d.text;
    ta.placeholder = t("What to remember, in a sentence — e.g. Use pnpm, never npm, in this repo");
    ta.oninput = () => { d.text = ta.value; };
    ta.onkeydown = (e) => { e.stopPropagation(); if ((e.metaKey || e.ctrlKey) && (e.key === "s" || e.key === "Enter")) { e.preventDefault(); saveMemory(); } };
    autosize(ta, 52);
    // a new one's text takes the focus when it opens, not on every render
    // after (the reader may be in its tags)
    if (!m) queueMicrotask(() => { if (!det.classList.contains("kept")) ta.focus(); });
    const pick = (label, value, opts, head, set) => {
      const b = menuButton(label(value), "lib-mempick", () => openProtoMenu(b, opts.map((v) => ({ v, name: label(v), literalName: true, note: "" })), value, (v) => { set(v); render(); }, head));
      return b;
    };
    const line = el("div", "lib-memline");
    line.append(pick(kindName, d.kind, MEM_KINDS, "Kind", (v) => { d.kind = v; }), pick(scopeName, d.scope, memScopes(true), "Where it applies", (v) => { d.scope = v; }));
    const tg = input(d.tags, t("Tags, separated by commas"));
    tg.className = "lib-memtags grow";
    tg.dataset.lib = "memtags:" + (m ? m.id : "new");
    tg.oninput = () => { d.tags = tg.value; };
    tg.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") saveMemory(); };
    line.append(tg);
    const why = input(d.why, t("Why (optional)"));
    why.className = "lib-memwhy";
    why.dataset.lib = "memwhy:" + (m ? m.id : "new");
    why.oninput = () => { d.why = why.value; };
    why.onkeydown = tg.onkeydown;
    det.append(ta, line, why);
    const acts = el("div", "lib-acts");
    if (m) {
      acts.append(button(m.pinned ? t("Unpin") : t("Pin"), "action", () => memChange("memory", { id: m.id, pinned: !m.pinned })));
      if (m.status === "candidate") acts.append(button(t("Keep"), "action", () => memChange("memory", { id: m.id, status: "active" }, t("Kept"))));
      else if (m.status === "active") acts.append(button(t("Archive"), "action", () => { memOpen = null; memChange("memory", { id: m.id, status: "archived" }, t("Archived")); }));
      else acts.append(button(t("Restore"), "action", () => { memOpen = null; memChange("memory", { id: m.id, status: "active" }, t("Restored")); }));
      const sure = memRemoving === m.id;
      acts.append(button(sure ? t("Delete for good") : t("Delete"), sure ? "primary danger-fill" : "action danger", () => {
        if (!sure) { memRemoving = m.id; render(); return; }
        memRemoving = null; memOpen = null; memDraft = null;
        memChange("memory/remove", { ids: [m.id] }, t("Deleted"));
      }));
    }
    acts.append(el("span", "grow"), button(t("Cancel"), "", () => { memOpen = null; memDraft = null; memRemoving = null; render(); }), button(m ? t("Save") : t("Remember"), "primary", saveMemory));
    det.append(acts);
    det.onclick = (e) => e.stopPropagation();
    return det;
  }

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
    logo.append(maskSpan("icons/rtk.png"));
    ttl.append(logo, el("b", "", "RTK"), el("span", "grow"));
    if (rtk.path) {
      ttl.append(el("span", "note mono", rtk.version ? "v" + rtk.version : tilde(rtk.path)));
      // a newer release: upgraded the way this rtk was installed
      // …unless the package manager that installed it (winget, Homebrew)
      // doesn't have it yet: an upgrade through it would change nothing
      // (#1025), so none is offered until it does
      const waiting = !rtkUpgrading && rtk.waiting;
      const behind = !waiting && rtk.latest && rtk.version && vNewer(rtk.latest, rtk.version);
      // why there is no upgrade to run: a cargo RTK with no cargo to be
      // found says what to install or run (新v学员)
      const noUpgrade = rtk.noUpgrade === "cargo"
        ? t("RTK was installed with cargo, but magpie can't find cargo to upgrade it. Install Rust from rustup.rs, or run {cmd} in a terminal.", { cmd: rtk.manual })
        : t("magpie can't tell how this RTK was installed — update it the way you installed it");
      if (rtkUpgrading) ttl.append(tag(t("Upgrading…"), "lib-new"));
      else if (waiting) ttl.append(tag(t("v{v} is out · waiting for {via}", { v: rtk.latest, via: rtk.waiting }), "", t("RTK {v} is out, but {via} has {has} so far — usually within a few days. Upgrade comes back once {via} has it.", { v: rtk.latest, via: rtk.waiting, has: rtk.waitingHas || rtk.version })));
      else if (behind) ttl.append(tag(t("v{v} is out", { v: rtk.latest }), "lib-new", t("RTK {v} is the latest release; this one is {have}", { v: rtk.latest, have: rtk.version })));
      else if (rtk.latest && rtk.version) ttl.append(tag(t("Up to date"), "", t("RTK {v} is the latest release", { v: rtk.latest })));
      if (behind || rtkUpgrading) {
        const ub = button(rtkUpgrading ? t("Upgrading…") : t("Upgrade"), "action", upgradeRTK);
        ub.disabled = rtkUpgrading || !rtk.upgrade;
        ub.title = rtk.upgrade ? (rtk.admin ? t("Runs {cmd}, as administrator: Windows asks first", { cmd: rtk.upgrade }) : t("Runs {cmd}", { cmd: rtk.upgrade })) : noUpgrade;
        ttl.append(ub);
      }
      card.append(ttl);
      // an upgrade under way says what it runs; one that failed says why,
      // with the tool's exit code and what it said, until the next try (#741)
      if (rtkUpgrading && rtk.upgrade) card.append(el("p", "lib-rtk-gain", t("Running {cmd} — this can take a few minutes.", { cmd: rtk.upgrade })));
      else if (rtkUpgradeErr) card.append(el("p", "lib-rtk-note lib-rtk-err", t("The upgrade failed: {why}", { why: rtkUpgradeErr })));
      // no upgrade to run: said on the card, not only in the disabled
      // button's tooltip, which a click never shows (新v学员: a cargo RTK
      // on Windows whose Upgrade did nothing)
      else if (behind && !rtk.upgrade) card.append(el("p", "lib-rtk-note lib-rtk-noupgrade", noUpgrade));
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
        ? t("{saved} tokens saved over {n} commands — {pct}% on average", { saved: tokens(g.saved), n: g.commands.toLocaleString(intlLang()), pct: Math.round(g.pct) })
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
      // why the last install failed stays on the card, beside Get RTK, not
      // only in a status line gone after ten seconds (Scott__D on X: the
      // install never succeeded, and nothing on the page said why)
      if (rtkInstallErr && !rtkInstalling) card.append(el("p", "lib-rtk-note lib-rtk-err lib-rtk-install-err", t("The install failed: {why}", { why: rtkInstallErr })));
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
    // an agent in WSL runs its distro's own RTK (rtk_wsl.go): one card each
    for (const d of rtk.wsl || []) body.append(rtkDistroCard(d));
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
      // an agent in WSL: its distro's RTK, not this computer's
      const kind = a.id.split("@")[0];
      const d = a.wsl ? (rtk.wsl || []).find((x) => x.name === a.wsl) : null;
      const path = a.wsl ? d?.path : rtk.path, offPath = a.wsl ? d?.offPath : rtk.offPath;
      if (a.asleep) row.append(tag(t("Not running"), "", t("WSL {distro} isn't running: magpie doesn't start it to read or change {agent}. Start the distro, then Check again.", { distro: a.wsl, agent: a.name })));
      // its hook calls an rtk that isn't there: switching it off still works
      else if (a.on && !path) row.append(tag(t("RTK missing"), "warn", a.wsl
        ? t("{agent}'s hook calls rtk, which isn't installed in WSL {distro}, so its shell commands fail. Install RTK there, or switch this off.", { agent: a.name, distro: a.wsl })
        : t("{agent}'s hook calls rtk, which isn't installed, so its shell commands fail. Install RTK, or switch this off.", { agent: a.name })));
      // …or one that is, off the PATH the agent gets
      else if (a.on && offPath) row.append(tag(t("Not on PATH"), "warn", a.wsl
        ? t("{agent}'s hook runs rtk by name and can't find it in WSL {distro}, so RTK does nothing for it. Add RTK's folder to PATH there.", { agent: a.name, distro: a.wsl })
        : t("{agent}'s hook runs rtk by name and can't find it, so RTK does nothing for it. Put RTK on PATH above.", { agent: a.name })));
      // an agent RTK has no hook for (rtkNoHook in rtk.go): listed, with its own why
      if (a.noHook) row.append(tag(t("No RTK hook"), "warn", rtkNoHookWhy(a)));
      // RTK before 0.51 installs Antigravity's hook only per project
      else if (a.blocked && kind === "agy") row.append(tag(t("Update RTK"), "warn", t("{agent}'s hook needs RTK 0.51 or newer: older ones install it only per project. Update RTK (brew upgrade rtk, or its installer again), then switch it on.", { agent: a.name })));
      // RTK before 0.44 has no Droid agent
      else if (a.blocked && kind === "droid") row.append(tag(t("Update RTK"), "warn", t("{agent}'s hook needs RTK 0.44 or newer: older ones have no hook for it. Update RTK (brew upgrade rtk, or its installer again), then switch it on.", { agent: a.name })));
      else if (a.blocked && kind !== "opencode") row.append(tag(t("Update RTK"), "warn", t("{agent}'s hook needs RTK 0.50 or newer: older ones only add @RTK.md to AGENTS.md, which rewrites no command. Update RTK (brew upgrade rtk, or its installer again), then switch it on.", { agent: a.name })));
      // OpenCode 2 loads only the plugin RTK 0.52 on writes (rtk-ai/rtk#4187);
      // an older one's, already there, can be switched off
      else if (a.blocked) row.append(tag(t("Update RTK"), "warn", a.on
        ? t("OpenCode 2 refuses to load this plugin: RTK before 0.52 writes it for OpenCode 1. Update RTK (brew upgrade rtk, or its installer again) and switch it on again, or switch this off.")
        : t("OpenCode 2 needs RTK 0.52 or newer: older ones write a plugin for OpenCode 1, which OpenCode 2 refuses to load. Update RTK (brew upgrade rtk, or its installer again), then switch it on.")));
      const sw = toggle(a.on, t("{agent} runs its commands through RTK", { agent: a.name }), (on) => setRTK(a, on));
      if ((!path && !a.on) || a.asleep || (a.blocked && !a.on) || rtkBusy.has(a.id) || rtkInstalling || rtkInstallingIn || rtkUpgrading) sw.disabled = true;
      row.append(sw);
      list.append(row);
    }
    if (!rows.length) list.append(el("div", "lib-none", t("None of your agents is one RTK has a hook for.")));
    body.append(list);
  }
  // Why RTK has no hook for an agent: each agent's own reason and its own
  // rtk-ai/rtk issue, never another agent's (新v学员 on Discord: Grok Build's
  // said DeepSeek Harness's hooks can't rewrite and linked dsh's issue).
  // Keyed as rtkNoHook in rtk.go; TestRTKNoHookIssuesAreEachAgents holds the
  // two together. An agent with no entry gets no reason and no link.
  function rtkNoHookWhy(a) {
    const agent = a.name;
    switch (a.id.split("@")[0]) {
      case "dsh": return t("RTK can't be given to {agent} yet: RTK works by rewriting the shell command an agent is about to run, and {agent}'s hooks can only allow or deny a command, not change it. RTK's installer has no option for it either (github.com/rtk-ai/rtk/issues/3847).", { agent });
      case "grok": return t("RTK can't be given to {agent} yet: RTK's installer has no option for Grok Build (github.com/rtk-ai/rtk/issues/2136).", { agent });
      case "grok-app": return t("RTK can't be given to {agent} yet: {agent} runs Grok Build, and RTK's installer has no option for Grok Build (github.com/rtk-ai/rtk/issues/2136).", { agent });
      case "qoder": return t("RTK can't be given to {agent} yet: RTK's installer has no option for Qoder (github.com/rtk-ai/rtk/issues/1327).", { agent });
      case "qoder-cn": return t("RTK can't be given to {agent} yet: {agent} is Qoder's China build, and RTK's installer has no option for Qoder (github.com/rtk-ai/rtk/issues/1327).", { agent });
      case "cline": return t("RTK can't be given to {agent} yet: RTK's installer only writes a .clinerules into one project, asking the model to type rtk itself, and has nothing for every project (github.com/rtk-ai/rtk/issues/3841).", { agent });
      case "kimi": return t("RTK can't be given to {agent}: {agent}'s hooks can only allow or deny a command, not change it, so RTK's installer only adds instructions to one project's AGENTS.md and has nothing for every project.", { agent });
      default: return t("RTK can't be given to {agent} yet: RTK has no hook for it.", { agent });
    }
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
  // ---------- billion-context ----------

  // billion-context (its CLI bili) folds an agent's older turns into
  // summaries the model can open again, so a long session stays in the
  // window. magpie runs bili's own installer for each agent switched on and
  // its remover for one switched off, then puts back what the install
  // changed in the agent's files (bili.go).
  let bili = null;          // /api/library/bili
  const biliBusy = new Set(); // agents being switched
  let biliLoading = false, biliInstalling = false, biliInstallErr = "";
  // billion-context.json's settings the tab changes, with magpie's models
  // to pick the summary model from (/api/library/bili/settings)
  let biliSet = null, biliSetBusy = false;
  async function loadBili() {
    if (biliLoading) return;
    biliLoading = true;
    try {
      [bili, biliSet] = await Promise.all([api("library/bili"), api("library/bili/settings").catch(() => null)]);
    } catch (e) {
      status(e.message, "err");
    }
    biliLoading = false;
    if (tab === "bili" && bili) render();
  }
  function renderBili(body) {
    body.append(intro(t("billion-context folds an agent's older turns into summaries the model can open again, so a long session stays inside its window. Switch it on for an agent and magpie runs billion-context's own installer for it.")));
    if (!bili) {
      body.append(el("div", "row skeleton"));
      loadBili();
      return;
    }
    const card = el("div", "list lib-card lib-bili");
    const ttl = el("div", "lib-cardhead");
    ttl.append(glyph(GLYPH.fold), el("b", "", "billion-context"), el("span", "grow"));
    card.append(ttl);
    if (bili.path) {
      ttl.append(el("span", "note mono", bili.version ? "v" + bili.version : tilde(bili.path)));
      card.append(biliSaved(bili.saved));
      card.append(el("p", "lib-rtk-gain", t("An agent switched on here can't also run billion-context-pi or opencode-acp.")));
      if (bili.listErr) card.append(el("p", "lib-rtk-note lib-rtk-err", t("magpie couldn't read which agents have it: {why}", { why: bili.listErr })));
    } else {
      card.append(el("p", "lib-rtk-gain", biliInstalling
        ? t("Installing billion-context… this can take a minute.")
        : t("billion-context isn't installed. magpie can install it for you, or get it from its site.")));
      if (bili.install) card.append(el("p", "lib-rtk-cmd", bili.install));
      if (biliInstallErr && !biliInstalling) card.append(el("p", "lib-rtk-note lib-rtk-err lib-bili-install-err", t("The install failed: {why}", { why: biliInstallErr })));
      const acts = el("div", "lib-acts");
      if (bili.install) {
        const ib = button(biliInstalling ? t("Installing…") : t("Install billion-context"), "action", installBili);
        ib.disabled = biliInstalling;
        acts.append(ib);
      }
      acts.append(button(t("Get billion-context"), bili.install ? "" : "action", () => browse(bili.url)), button(t("Check again"), "", () => { bili = null; render(); }));
      card.append(acts);
    }
    body.append(card);
    const rh = el("div", "row-head");
    rh.append(el("span", "label", t("Agents")), el("span", "grow"), el("span", "note", t("restart an agent after switching it")));
    body.append(rh);
    const list = el("div", "list lib-list");
    const rows = bili.agents.filter((a) => !isHidden(a));
    for (const a of rows) {
      const row = el("div", "row lib-row");
      const who = el("div", "who");
      who.append(el("div", "name", a.name));
      row.append(icon(a.icon), who, el("span", "grow"));
      // bili lists it as neither on nor off: partly there, broken
      if (a.status) row.append(tag(t("Not whole"), "warn", t("bili plugin list says: {status}. Switch it off to take out what is there.", { status: a.status })));
      const on = a.on || !!a.status;
      const sw = toggle(on, t("{agent} folds its context with billion-context", { agent: a.name }), (v) => setBili(a, v));
      if ((!bili.path) || biliBusy.has(a.id) || biliInstalling) sw.disabled = true;
      row.append(sw);
      list.append(row);
    }
    if (!rows.length) list.append(el("div", "lib-none", t("None of your agents can use billion-context with magpie: Pi, omp, OpenCode, Claude Code, Hermes, DeepSeek Harness or Zed.")));
    body.append(list);
    if (bili.path && biliSet?.path) biliSettings(body);
  }
  // billion-context.json's settings, as billion-context reads them: the
  // ones changed often in front, its compression philosophy under
  // Advanced. A change is written into the file (keeping what else it
  // has) and the running billion-context reads it again (bili_config.go).
  function biliSettings(body) {
    const s = biliSet;
    const rh = el("div", "row-head");
    rh.append(el("span", "label", t("Settings")), el("span", "grow"), pathLink(s.path));
    body.append(rh);
    const list = el("div", "list lib-list lib-bili-set");
    if (s.bad) {
      list.append(el("div", "lib-none lib-rtk-err", t("billion-context.json can't be read, so magpie leaves it alone. Fix it or take it out to change these here.")));
      body.append(list);
      return;
    }
    const row = (name, ...end) => {
      const r = el("div", "row lib-row");
      const who = el("div", "who");
      who.append(el("div", "name", name));
      r.append(who, el("span", "grow"), ...end);
      list.append(r);
      return r;
    };
    const models = biliSet.models || [];
    const target = s.targets[0] || "";
    const ours = target.startsWith("magpie/") ? target.slice(7) : "";
    const named = ours ? (models.find((m) => m.id === ours)?.name || ours) : target;
    const pick = menuButton(named || t("Choose a model"), "lib-bili-model", () => openProtoMenu(pick, [
      ...models.map((m) => ({ v: "m\n" + m.id, name: m.name || m.id, note: m.providerName || "", literalName: true })),
      ...s.recipes.map((r) => ({ v: "r\n" + r, name: r, note: "billion-context.json", literalName: true }))],
    ours ? "m\n" + ours : target ? "r\n" + target : "", (v) => {
      const [k, id] = v.split("\n");
      biliChange(k === "m" ? { model: id, summary: true } : { target: id, summary: true });
    }, "Model", "", "left", false, models.length + s.recipes.length > 8 ? "Search models" : ""));
    pick.disabled = biliSetBusy;
    const sum = toggle(s.summary, t("Summarize with a fast model"), (v) => biliChange({ summary: v }));
    sum.disabled = biliSetBusy || !target;
    if (!target) sum.title = t("Choose a model first");
    row(t("Summarize with a fast model"), pick, sum);
    const nudge = toggle(s.nudge, t("Fold only when nudged"), (v) => biliChange({ nudge: v }));
    nudge.disabled = biliSetBusy;
    row(t("Fold only when nudged"), nudge);
    const num = el("input", "lib-bili-growth");
    num.type = "number";
    num.min = "1000";
    num.step = "1000";
    num.inputMode = "numeric";
    num.placeholder = "50000";
    num.value = s.growth ? String(s.growth) : "";
    num.disabled = biliSetBusy;
    num.setAttribute("aria-label", t("Nudge every"));
    const putGrowth = () => {
      const v = num.value.trim() === "" ? 0 : Math.round(+num.value);
      if (v !== (s.growth || 0)) biliChange({ growth: v });
    };
    num.onchange = putGrowth;
    num.onkeydown = (e) => { if (e.key === "Enter") num.blur(); };
    row(t("Nudge every"), num, el("span", "note", t("tokens")));
    body.append(list);

    const more = el("details", "lib-more lib-bili-more");
    more.append(el("summary", "", t("Advanced")));
    // compress.visibilityMarkers: the 📦/❌ lines after a fold, which
    // some models imitate (@JamesYu32 on X)
    const marks = el("div", "list lib-list lib-bili-marks");
    const mk = toggle(s.markers !== false, t("Show compression markers"), (v) => biliChange({ markers: v }));
    mk.disabled = biliSetBusy;
    const mr = el("div", "row lib-row");
    const mw = el("div", "who");
    mw.append(el("div", "name", t("Show compression markers")));
    mr.append(mw, el("span", "grow"), mk);
    marks.append(mr);
    more.append(marks);
    const ph = el("div", "lib-bili-phil");
    ph.append(el("div", "name", t("Compression philosophy")));
    const ta = el("textarea", "lib-text small");
    // what's typed outlives a redraw until it is saved
    ta.value = biliPhil ?? (s.philosophy || "");
    ta.oninput = () => { biliPhil = ta.value; };
    ta.placeholder = t("billion-context's own");
    ta.setAttribute("aria-label", t("Compression philosophy"));
    ta.disabled = biliSetBusy;
    ph.append(ta, el("p", "lib-rtk-note", t("Replaces billion-context's guidance on what a fold keeps. Setting it also tells billion-context you accept the risk of changing its prompts.")));
    const acts = el("div", "lib-acts");
    const save = button(t("Save"), "action", () => ta.value.trim() !== (s.philosophy || "") && biliChange({ philosophy: ta.value }));
    save.disabled = biliSetBusy;
    acts.append(save);
    ph.append(acts);
    more.append(ph);
    // open as it was across redraws
    more.open = biliMoreOpen;
    more.ontoggle = () => { biliMoreOpen = more.open; };
    body.append(more);
    autosize(ta, 72);
  }
  let biliMoreOpen = false, biliPhil = null;
  async function biliChange(c) {
    biliSetBusy = true;
    render();
    try {
      biliSet = await api("library/bili/settings", c);
      if ("philosophy" in c) biliPhil = null;
      status(biliSet.reloaded
        ? t("Saved — the running billion-context uses it now")
        : biliSet.running
          ? t("Saved — billion-context didn't reload it; restart the agent to use it")
          : t("Saved — billion-context uses it when it next starts"), "ok", 6000);
    } catch (e) {
      status(e.message, "err", 8000);
    }
    biliSetBusy = false;
    render();
  }
  // What billion-context saved, in its own count: what its running proxy
  // says at /__bili/overview (its dashboard's net savings), over every
  // session on this computer; the last answer kept when none runs now
  function biliSaved(s) {
    if (!s) return el("p", "lib-rtk-gain lib-bili-saved", t("What billion-context saves shows here once it has run for an agent."));
    const n = (v) => Number(v || 0).toLocaleString(intlLang());
    const count = (v, one, many) => v === 1 ? t(one) : t(many, { n: n(v) });
    const p = el("p", "lib-rtk-gain lib-bili-saved", s.hasFolds
      ? t("{saved} tokens saved over {folds} in {sessions}", { saved: (s.net < 0 ? "-" : "") + tokens(Math.abs(s.net)), folds: count(s.folds, "1 fold", "{n} folds"), sessions: count(s.sessions, "1 session", "{n} sessions") })
      : t("Nothing folded yet over {requests}: billion-context folds a session once it nears the model's window.", { requests: count(s.requests, "1 request", "{n} requests") }));
    if (s.hasFolds) p.append(" ", el("span", "lib-rtk-scope", t("billion-context's own count, after the rebuilds a fold causes and the summaries' cost ({gross} before them): every session it ran on this computer, from any agent.", { gross: tokens(s.gross) })));
    if (!s.live) {
      const at = new Date(s.at);
      p.append(" ", el("span", "lib-rtk-scope lib-bili-asof", t("As of {time}: billion-context isn't running now.", { time: isNaN(at) ? s.at : at.toLocaleString(intlLang(), { dateStyle: "medium", timeStyle: "short" }) })));
    }
    return p;
  }
  async function setBili(a, on) {
    biliBusy.add(a.id);
    render();
    try {
      bili = await api("library/bili", { agent: a.id, on });
      const names = (bili.restart || [a.id]).map((id) => bili.agents.find((x) => x.id === id)?.name || id).join(", ");
      status(on ? t("billion-context is on for {agent} — restart it to use it", { agent: names }) : t("billion-context is off for {agent} — restart it to see it", { agent: names }), "ok", 6000);
    } catch (e) {
      status(e.message, "err", 8000);
    }
    biliBusy.delete(a.id);
    render();
  }
  async function installBili() {
    biliInstalling = true;
    biliInstallErr = "";
    render();
    try {
      bili = await api("library/bili/install", {});
      status(t("billion-context is installed — switch it on for your agents below"), "ok", 6000);
    } catch (e) {
      biliInstallErr = e.message;
      status(e.message, "err", 10000);
    }
    biliInstalling = false;
    render();
  }

  // ---------- Pi extensions ----------

  // Each Pi here (this computer's, and each WSL distro's) with the
  // extensions and packages it loads (piext.go). One is switched off or on
  // in its settings.json the way `pi config` does it, and copied to
  // another Pi: a package as its entry in that Pi's settings.json, which
  // Pi installs itself when it starts; an extension as its file or folder.
  // leokon3 on Discord: moving an extension from the WSL Pi to Windows'.
  const hasPi = () => !!lib && !gatewayPage() && lib.agents.some((a) => a.id.split("@")[0] === "pi" && !a.gone && !a.unseen && !isHidden(a));
  let piView = null;       // /api/library/pi
  const piBusy = new Set(); // "<pi> <ext or package>" being switched or copied
  let piLoading = false;
  async function loadPi() {
    if (piLoading) return;
    piLoading = true;
    try {
      piView = await api("library/pi");
    } catch (e) {
      status(e.message, "err");
    }
    piLoading = false;
    if (tab === "pi" && piView) render();
  }
  function renderPi(body) {
    body.append(intro(t("The extensions and packages each Pi loads. Switch one off, or copy it to another Pi: a package is added to that Pi's settings, and Pi installs it when it next starts.")));
    if (!piView) {
      body.append(el("div", "row skeleton"));
      loadPi();
      return;
    }
    const pis = (piView.pis || []).filter((p) => !isHidden({ id: p.id }));
    const live = pis.filter((p) => !p.asleep && !p.err);
    for (const p of pis) {
      const rh = el("div", "row-head lib-pi-head");
      rh.dataset.pi = p.id;
      rh.append(el("span", "label", p.name), el("span", "grow"));
      if (p.dir && !p.asleep) rh.append(pathLink(p.dir));
      body.append(rh);
      const list = el("div", "list lib-list lib-pi");
      list.dataset.pi = p.id;
      const others = live.filter((x) => x.id !== p.id);
      if (p.asleep) {
        list.append(el("div", "lib-none", t("WSL {distro} isn't running: magpie doesn't start it to read or change {agent}. Start the distro, then Check again.", { distro: p.wsl, agent: p.name })));
      } else if (p.err) {
        list.append(el("div", "lib-none lib-pi-err", t("magpie couldn't read {file}, so it lists nothing and changes nothing there: {why}", { file: "settings.json", why: p.err })));
      } else {
        for (const x of p.extensions || []) list.append(piRow(p, others, { ext: x.id }, x.name, tilde(x.path), x.on, null));
        for (const k of p.packages || []) {
          const tags = [tag(k.kind)];
          if (!k.installed) tags.push(tag(t("Not installed"), "", t("Pi installs it when it next starts")));
          else if (k.version) tags.push(tag("v" + k.version));
          list.append(piRow(p, others, { pkg: k.source }, k.name, k.source, k.on, tags, k.filtered, () => confirmRemovePi(p, k)));
        }
        if (!(p.extensions || []).length && !(p.packages || []).length) list.append(el("div", "lib-none", t("No extensions or packages yet.")));
      }
      body.append(list);
    }
    if (live.length) body.append(discover("pi"));
  }
  function piRow(p, others, what, name, sub, on, tags, filtered, remove) {
    const key = p.id + " " + (what.ext || what.pkg);
    const row = el("div", "row lib-row lib-pi-row");
    row.dataset.id = what.ext || what.pkg;
    const who = el("div", "who");
    const n = el("div", "name mono", name);
    for (const g of tags || []) n.append(g);
    who.append(n, el("div", "sub mono", sub));
    row.append(who);
    if (others.length) {
      const b = button(t("Copy to…"), "lib-pi-copy", () => {
        openProtoMenu(b, others.map((x) => ({ v: x.id, name: x.name, literalName: true, note: "" })), "", (to) => copyPi(p, others.find((x) => x.id === to), what, name), "Copy to another Pi", "", "right");
      });
      b.disabled = piBusy.has(key);
      row.append(b);
    }
    const sw = toggle(on, t("{agent} loads {name}", { agent: p.name, name }), (v) => setPi(p, what, name, v));
    if (filtered) sw.title = t("This entry loads only some of the package; change it with pi config");
    sw.disabled = !!filtered || piBusy.has(key);
    row.append(sw);
    if (remove) {
      const rm = button("", "lib-icon danger lib-rm lib-pi-rm", remove);
      rm.append(svg(GLYPH.trash, 13, 1.4));
      rm.title = t("Remove");
      rm.disabled = piBusy.has(key);
      row.append(rm);
    }
    return row;
  }
  async function setPi(p, what, name, on) {
    const key = p.id + " " + (what.ext || what.pkg);
    piBusy.add(key);
    render();
    try {
      piView = await api("library/pi", { pi: p.id, ...what, on });
      status(on ? t("{name} is on for {agent}: Pi loads it when it next starts, or at /reload", { name, agent: p.name }) : t("{name} is off for {agent}: Pi drops it when it next starts, or at /reload", { name, agent: p.name }), "ok", 6000);
    } catch (e) {
      status(e.message, "err", 8000);
    }
    piBusy.delete(key);
    render();
  }
  async function copyPi(p, to, what, name) {
    if (!to) return;
    const key = p.id + " " + (what.ext || what.pkg);
    piBusy.add(key);
    render();
    try {
      const v = await api("library/pi/copy", { from: p.id, to: to.id, ...what });
      piView = v;
      status(what.pkg
        ? t("{name} is in {agent}'s packages now: Pi installs it when it next starts", { name, agent: to.name })
        : v.install
          ? t("{name} is copied to {agent}. It needs its packages: run npm install in {dir} there", { name, agent: to.name, dir: v.install })
          : t("{name} is copied to {agent}: Pi loads it when it next starts, or at /reload", { name, agent: to.name }), "ok", v.install ? 12000 : 6000);
    } catch (e) {
      status(e.message, "err", 8000);
    }
    piBusy.delete(key);
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
    // the bars' scale, the busiest day (week) in the range, said as that:
    // a bare number there read as RTK's total, one that stopped growing
    // once no day passed the old busiest (jasonchia58 on X); the Usage page's is #6517
    head.append(el("span", "label", t(step === 7 ? "Saved by week" : "Saved by day")), seg, el("span", "grow"));
    if (sum.input) head.append(el("span", "peak", t(step === 7 ? "peak week {n}" : "peak day {n}", { n: tokens(peak) })));
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
        ? t("{when} · {saved} tokens saved over {n} commands — {pct}%", { when, saved: tokens(b.saved), n: b.commands.toLocaleString(intlLang()), pct: b.input ? Math.round(100 * b.saved / b.input) : 0 })
        : t("{when} · nothing", { when });
      bars.append(bar);
      const end = i === k - 1 && (k - 1) % every >= every / 2;
      labels.append(el("span", "", i % every === 0 || end ? short(b.day) : ""));
    });
    const foot = el("div", "lib-rtk-foot");
    const key = (cls, text) => { const x = el("span", "lib-rtk-key"); x.append(el("i", cls), el("span", "", text)); return x; };
    foot.append(key("out", t("saved")), key("in", t("still sent")), el("span", "grow"),
      el("span", "", sum.commands
        ? t("{saved} tokens saved over {n} commands — {pct}%", { saved: tokens(sum.saved), n: sum.commands.toLocaleString(intlLang()), pct: sum.input ? Math.round(100 * sum.saved / sum.input) : 0 })
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
  // RTK in a WSL distro: its agents' hooks run there, so they run its own
  // rtk, installed by RTK's own Linux script run in the distro. A distro
  // that isn't running is never started for it.
  let rtkInstallingIn = "";
  const rtkInstallErrIn = {};
  function rtkDistroCard(d) {
    const card = el("div", "list lib-card lib-rtk-wsl");
    const ttl = el("div", "lib-cardhead");
    ttl.append(el("b", "", t("RTK in WSL {distro}", { distro: d.name })), el("span", "grow"));
    if (d.path) ttl.append(el("span", "note mono", d.version ? "v" + d.version : d.path));
    card.append(ttl);
    const acts = el("div", "lib-acts");
    const again = (cls) => button(t("Check again"), cls, () => { rtk = null; render(); });
    if (d.asleep) {
      card.append(el("p", "lib-rtk-gain", t("WSL {distro} isn't running: magpie doesn't start it to look for RTK or change its agents. Start the distro, then Check again.", { distro: d.name })));
      acts.append(again("action"));
    } else if (!d.path) {
      const installing = rtkInstallingIn === d.name;
      card.append(el("p", "lib-rtk-gain", installing
        ? t("Installing RTK in WSL {distro}… this can take a minute.", { distro: d.name })
        : d.install
          ? t("RTK isn't installed in WSL {distro}. Agents there run their hooks in the distro, so they need RTK there: magpie can run RTK's own installer in it.", { distro: d.name })
          : t("magpie couldn't look for RTK in WSL {distro}.", { distro: d.name })));
      if (d.install) card.append(el("p", "lib-rtk-cmd", d.install));
      if (rtkInstallErrIn[d.name] && !installing) card.append(el("p", "lib-rtk-note lib-rtk-err lib-rtk-install-err", t("The install failed: {why}", { why: rtkInstallErrIn[d.name] })));
      if (d.install) {
        const ib = button(installing ? t("Installing…") : t("Install RTK in WSL {distro}", { distro: d.name }), "action", () => installRTKIn(d.name));
        ib.disabled = !!rtkInstallingIn || rtkInstalling;
        acts.append(ib);
      }
      acts.append(again(d.install ? "" : "action"));
    } else if (d.offPath) {
      const dir = d.path.replace(/\/[^/]*$/, "");
      card.append(el("p", "lib-rtk-note lib-rtk-offpath", t("RTK is in {dir} in WSL {distro}, which isn't on its shells' PATH. Agents there run rtk by name, so they can't find it. Add {dir} to PATH in your shell profile there, then Check again.", { dir, distro: d.name })));
      acts.append(again("action"));
    } else {
      card.append(el("p", "lib-rtk-gain", t("Agents in WSL {distro} run its own RTK, at {path}. What it saves there is in its own count (rtk gain in the distro), not the one above.", { distro: d.name, path: d.path })));
    }
    if (acts.childNodes.length) card.append(acts);
    return card;
  }
  async function installRTKIn(distro) {
    rtkInstallingIn = distro;
    delete rtkInstallErrIn[distro];
    render();
    try {
      rtk = await api("library/rtk/install", { distro });
      status(t("RTK is installed in WSL {distro} — switch it on for its agents below", { distro }), "ok", 6000);
    } catch (e) {
      rtkInstallErrIn[distro] = e.message;
      status(e.message, "err", 10000);
    }
    rtkInstallingIn = "";
    render();
  }
  let rtkInstalling = false, rtkInstallErr = "";
  async function installRTK() {
    rtkInstalling = true;
    rtkInstallErr = "";
    render();
    try {
      rtk = await api("library/rtk/install", {});
      status(t("RTK is installed — switch it on for your agents below"), "ok", 6000);
    } catch (e) {
      rtkInstallErr = e.message;
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

  // Measured at height auto, the textarea is short for a moment. What holds
  // it is held at its height meanwhile, so the page never is: a page gone
  // short has its view's scroll clamped by the browser, which no height put
  // back undoes, and an input method's every keystroke is an input (#2636:
  // the page jumped while a word was composed).
  function autosize(ta, min) {
    const fit = () => {
      const box = ta.parentElement, was = box?.style.minHeight;
      if (box) box.style.minHeight = box.offsetHeight + "px";
      ta.style.height = "auto";
      ta.style.height = Math.max(min, ta.scrollHeight + 2) + "px";
      if (box) box.style.minHeight = was;
    };
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
    if (force === true) { memOpen = null; memDraft = null; }
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
    // a stopped WSL distro's agents are rows above when magpie saw their
    // file; one it didn't see isn't said to keep none
    const skip = shownAgents().filter((a) => !a.instructions && !a.asleep);
    if (skip.length) body.append(el("p", "lib-aside", t(skip.length > 1 ? "{agents} keep no user-wide instructions file." : "{agents} keeps no user-wide instructions file.", { agents: skip.map((a) => a.name).join(", ") })));
    body.append(...asleepAsides(rows.filter((a) => a.asleep)));
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
    if (x.file) tags.append(tag(t("Your file"), "", t("Kept in {path}, not in the library: edits there reach the agents, and Save writes there", { path: tilde(x.file) })));
    if (text !== x.text) tags.append(tag(t("Not saved yet"), "warn"));
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(pick, who, tags, chev);
    row.onclick = () => { openSet = isOpen ? null : x.id; removingSet = null; render(); };
    if (!isOpen) return [row];

    const det = el("div", "lib-detail");
    det.dataset.detail = "set:" + x.id;
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
    det.append(nm);
    if (x.file) {
      // a set kept in a file of the user's own (~/.agents/AGENTS.md): what
      // the page shows is that file, and Save writes to it
      const where = el("div", "note lib-setfile");
      where.append(t("Kept in") + " ", pathLink(x.file));
      det.append(where);
      // Droid and omp read ~/.agents/AGENTS.md themselves: given it by
      // magpie too, they'd read it twice
      const twice = /[\\/]\.agents[\\/]AGENTS\.md$/.test(x.file) ? lib.instructions.agents.filter((a) => a.on && ["droid", "omp"].includes(a.agent)) : [];
      if (twice.length) det.append(el("p", "lib-aside lib-twice", t(twice.length > 1 ? "{agents} read this file themselves too: switch them off below so they don't read it twice." : "{agents} reads this file itself too: switch it off below so it doesn't read it twice.", { agents: twice.map((a) => a.name).join(", ") })));
    }
    det.append(ta);
    const acts = el("div", "lib-acts");
    if (!x.active) acts.append(button(t("Switch the agents to this set"), "action lib-use", () => pick.onclick({ stopPropagation() {} })));
    if (x.id !== "default") {
      // its text goes with it, so a second click says so and does it
      const sure = removingSet === x.id;
      const rm = button(sure ? (x.file ? t("Remove {name} — the file stays", { name: setName(x) }) : t("Remove {name} and its text", { name: setName(x) })) : t("Remove"), sure ? "primary danger-fill" : "action danger", () => {
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
    const row = el("div", "row lib-row lib-set lib-setnew");
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
      const create = { id, name };
      if (file && file.value.trim()) create.file = file.value.trim();
      if (await change("instructions/save", { create }, said)) {
        addingSet = false;
        openSet = id;
        render();
      }
    };
    const cancel = () => { addingSet = false; render(); };
    nm.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") create(); else if (e.key === "Escape") cancel(); };
    queueMicrotask(() => nm.focus());
    // a set can be kept in a file of the user's own instead, one that other
    // tools keep or read too: ~/.agents/AGENTS.md (新v学员 on Discord)
    let file = null;
    const link = button(t("Keep in a file…"), "action lib-setlink", () => {
      file = input("~/.agents/AGENTS.md", t("Full path of a Markdown file"));
      file.value = "~/.agents/AGENTS.md";
      file.dataset.lib = "newsetfile";
      file.className = "lib-setname lib-setfile-in grow";
      file.onkeydown = nm.onkeydown;
      if (!nm.value.trim()) nm.value = "AGENTS.md";
      link.replaceWith(file);
      file.focus();
    });
    row.append(nm, link, button(t("Cancel"), "", cancel), button(t("Add"), "primary", create));
    return row;
  }

  // An agent in a stopped WSL distro (asleep) is its row as magpie last saw
  // it, greyer: its file isn't read or shown in the file manager, either of
  // which would start WSL (#3896), and a switch is written there the next
  // time it runs. One whose file magpie never saw (unseen) is listed only
  // while it is on, its switch kept as it is (#4182).
  function instructionsRow(a) {
    const isOpen = open.has(a.agent);
    const row = el("div", "row lib-row click" + (isOpen ? " open" : "") + (a.asleep ? " asleep" : ""));
    const who = el("div", "who");
    const name = el("div", "name", a.name);
    who.append(name);
    const sub = el("div", "sub");
    if (a.asleep) { if (a.path) sub.append(el("span", "lib-path", tilde(a.path))); }
    else sub.append(pathLink(a.path));
    who.append(sub);
    const tags = el("div", "lib-tags");
    const extra = extras[a.agent] ?? a.extra;
    if (isHidden({ id: a.agent })) tags.append(tag(t("Hidden"), "", t("Hidden on the Agents page: switch it off here to take the shared instructions out of its file")));
    if (a.on && extra) tags.append(tag(t("+ its own"), "", t("{agent} gets something of its own after the shared text", { agent: a.name })));
    if (a.edited) tags.append(tag(t("edited in the file"), "warn", t("magpie's part of this file was changed there; magpie leaves it until the library's text changes")));
    if (a.own) tags.append(tag(a.own === 1 ? t("1 line of its own") : t("{n} lines of its own", { n: a.own }), "", t("The file has instructions besides magpie's part — they stay, and the agent reads both")));
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    const sw = toggle(a.on, t("{agent} reads the shared instructions", { agent: a.name }), (on) => {
      if (a.unseen) return;
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
      if (a.asleep) done = asleepSaid;
      change("instructions/save", { agents }, done);
    });
    const asleepSaid = a.asleep && t("WSL {distro} isn't running: its agents show what magpie last saw there, and what you change is written there the next time it runs.", { distro: a.asleep });
    if (a.unseen) { sw.setAttribute("aria-disabled", "true"); sw.title = t("WSL {distro} isn't running, and magpie hasn't seen {agent}'s files there yet: start the distro to change this", { distro: a.asleep, agent: a.name }); }
    else if (a.asleep) sw.title = asleepSaid;
    row.append(icon(a.icon), who, tags, sw, chev);
    row.onclick = () => { if (isOpen) open.delete(a.agent); else open.add(a.agent); render(); };
    if (!isOpen) return [row];

    const det = el("div", "lib-detail");
    det.dataset.detail = "agent:" + a.agent;
    if (a.override) {
      const w = el("div", "lib-warn");
      w.append(el("span", "", t("{agent} reads {file} instead of this file while it has anything in it.", { agent: a.name, file: tilde(a.override) }) + " "), pathLink(a.override));
      det.append(w);
    }
    if (a.agent === "opencode" || a.agent === "mimocode" || a.agent === "deveco") det.append(el("p", "lib-aside", t("{agent} reads Claude Code's CLAUDE.md when it has no AGENTS.md of its own.", { agent: a.name })));
    if (a.agent === "agy") det.append(el("p", "lib-aside", t("Antigravity reads Gemini CLI's ~/.gemini/GEMINI.md as well as this file, so text given to both is read twice.")));
    if (a.profile) det.append(el("p", "lib-aside", t("This file is {agent}'s profile of you: magpie adds its part after yours and leaves your own lines in it. A USER.md in a workspace is read instead of this one.", { agent: a.name })));
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
    if (a.own && !a.profile) {
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
  // quote: an argument as this system's shell reads it, and words() reads
  // back. On Windows that is cmd's double quotes, and a \ is a path's, not an
  // escape: sh's 'C:\…\npx.cmd' read as quotes magpie had added to the
  // command (fremontdango on Discord), and a path typed as it is lost its \
  // and : on save (#2685).
  const WIN_LINE = /^Win/.test(navigator.platform);
  function quote(a) {
    if (WIN_LINE) return a && !/[\s"'&|<>^%]/.test(a) ? a : '"' + a.replace(/"/g, '""') + '"';
    return /^[\w@%+=:,./-]+$/.test(a) ? a : "'" + a.replace(/'/g, `'\\''`) + "'";
  }

  function sseBlocked(s) {
    return (a) => {
      if (remote(s) && a.noRemote) return t("{agent} runs only a command from its settings — add a remote server in its Connectors instead", { agent: a.name });
      if (s.transport === "sse" && a.noSSE) return t("{agent} can't reach a server over SSE — only a command or streamable HTTP", { agent: a.name });
      // #1250: written there as it is, the agent would send the text
      // itself; written with the value, the token would be in its file
      return envRefs(s) && a.noEnvRefs ? t("{agent} can't read {ref} from its settings — given this server, the token would be written there as plain text", { agent: a.name, ref: "${NAME}" }) : "";
    };
  }
  // envRefs says whether the server's headers (remote) or environment
  // (command) reference a variable: ${NAME}, as the library writes one
  const envRef = /\$\{[A-Za-z_][A-Za-z0-9_]*\}/;
  const envRefs = (s) => Object.values((remote(s) ? s.headers : s.env) || {}).some((v) => envRef.test(v));
  // reaches(s) says whether an agent can be given the server
  const remote = (s) => s.transport === "http" || s.transport === "sse";
  const reaches = (s) => (a) => !a || (!(remote(s) && a.noRemote) && !(s.transport === "sse" && a.noSSE) && !(envRefs(s) && a.noEnvRefs));

  function renderServers(body) {
    body.append(intro(agentless()
      ? t("Add the servers other apps should reach, and turn on the MCP router: they connect at its address, from another computer with a gateway key.")
      : t("Add a server once and switch it on for the agents that should have it — magpie writes it into each one's config in the shape that agent reads.")));
    const all = mcpAgents();
    pickingFor("servers", lib.servers);
    if (!lib.servers.length) {
      const add = button(t("＋ Add a server"), "action", () => editServer(null));
      body.append(empty(t("No servers in the library yet"), lib.foundServers.length && !agentless() ? t("Add one, or bring in those your agents already have, below.") : t("Paste a server's JSON from its README, or fill it in."), add));
      // with no agent, the router is what the page is for: there to turn
      // on before the first server
      if (agentless()) body.append(...hubPart());
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
        // the rest under ⋯, as the skills' (ryanpascal on Discord)
        if (many && all.length) rh.append(pickButton("servers"));
        rh.append(barMore([
          ...many && all.length ? everyButtons("servers", all).map((b) => [b, GLYPH.power]) : [],
          by && [byAgentButton("servers"), GLYPH.person],
          null,
          many && [removeEveryButton("servers"), GLYPH.trash],
        ]));
        body.append(rh);
      }
      fill();
      body.append(list);
      if (picking) body.append(pickBar(all));
      const after = el("div", "after-list");
      after.append(button(t("＋ Add server"), "", () => editServer(null)));
      body.append(after);
      // kept while picking too, as Add server is: the page stays as tall,
      // so Select doesn't move under the pointer (library-servers-bulk)
      body.append(...hubPart());
      // each server is checked once a session, and again only once it
      // changed; once the rows are on the page, for them to show it. One
      // that no agent and no project has is started for nobody, so it is
      // checked only when the reader clicks its status (#1597, xiaozhu1337),
      // and again once it changed after that: a sign-in since isn't left
      // beside "needs sign-in" (新v学员 on Discord)
      // With no agent, the router is what starts them, so on it checks them too
      // One turned off is started for nobody, and checked only on a click.
      const unchecked = lib.servers.filter((s) => (!s.hidden && (inUse(s) || (agentless() && lib.mcpHub && inHub(s))) || health.has(s.name)) && health.get(s.name)?.key !== healthKey(s));
      queueMicrotask(() => checkHealth(unchecked));
    }
    // an agent's own servers (Codex's node_repl, added each time it starts)
    // aren't listed: magpie leaves them as they are
    const found = agentless() ? [] : lib.foundServers.filter((f) => !f.own);
    if (found.length) {
      const rh = el("div", "row-head");
      rh.append(el("span", "label", t("In your agents")), el("span", "grow"), el("span", "note", t("not in the library — bring one in to manage it here")));
      body.append(rh);
      body.append(foldedFound("mcp", found.map(foundServerRow)));
    }
    if (!agentless() && (lib.servers.length || lib.projects.length)) renderProjects(body, "mcp");
    const skip = shownAgents().filter((a) => !a.mcp);
    if (skip.length) body.append(el("p", "lib-aside", t("{agents} has no MCP servers magpie can write.", { agents: skip.map((a) => a.name).join(", ") })));
    if (!gatewayPage()) body.append(...asleepAsides());
    // WorkBuddy connects a server only once it is trusted there, again
    // after its command or address changes (#1266)
    for (const wb of shownAgents().filter((a) => (a.id === "workbuddy" || a.id === "workbuddy-ai") && a.mcp)) {
      if (lib.servers.some((s) => s.agents?.includes(wb.id))) {
        body.append(el("p", "lib-aside lib-wb-trust", t("{agent} connects a server only once you trust it: switch it on in {agent}'s MCP settings, and again after its command or address changes.", { agent: wb.name })));
      }
    }
    body.append(discover("mcp"));
  }

  // The MCP hub (#2679): magpie as each agent's one MCP server, handing
  // each call on to the library's servers ticked for it, and one address
  // for any other app with every server in the library. A switch, off
  // till turned on; on, what it serves, the address with Connect, and the
  // agents that keep each server's own entry, with why.
  function hubPart() {
    const list = el("div", "list lib-list lib-hublist");
    const row = el("div", "row lib-row lib-hub");
    const who = el("div", "who");
    who.append(el("div", "name", t("MCP router")));
    who.append(el("div", "sub wraps", agentless()
      ? (lib.mcpHub ? t("Any app connects at the address below for every server in the library, as server__tool.") : t("Serve every server in the library at one address, for any app to connect to."))
      : lib.mcpHub
      ? t("Each agent is given one server, magpie, in place of these. magpie runs each server once for all of them and hands each call to it, as server__tool. Restart an agent for it to read the change.")
      : t("Serve every server through magpie: each agent is given one server, magpie, in place of these, and any other app connects at one address.")));
    if (lib.mcpHub) who.append(el("div", "sub lib-hubstats", hubStats()));
    row.append(hubMark(), who, toggle(!!lib.mcpHub, t("MCP router"), (on) =>
      change("servers/hub", { on }, agentless()
        ? (on ? t("The MCP router is on: apps can connect at its address") : t("The MCP router is off"))
        : on
        ? t("The agents are given magpie's one MCP server — restart them to use it")
        : t("The agents are given each server's own entry again — restart them to use it"))));
    list.append(row);
    const out = [list];
    if (!lib.mcpHub) return out;
    // the one address any other app connects at
    const at = el("div", "row lib-row lib-hubat");
    const aw = el("div", "who");
    // what it is, not who: the hub's address (the owner: "Other apps" over
    // an address read as nonsense); who connects there is in the tooltip
    aw.append(el("div", "name", t("Router address")));
    aw.title = t("Any other app, as Cursor or VS Code, connects here for every server in the library");
    const u = el("div", "sub mono lib-hubURL", hubURL());
    u.title = hubURL();
    aw.append(u);
    const connect = button(t("Connect…"), "", () => connectHub());
    connect.classList.add("lib-hubconnect");
    at.append(glyph(GLYPH.web), aw, copyBtn(hubURL(), t("Address")), connect);
    list.append(at);
    list.append(hubNoteRow(), hubToolsRow());
    const names = (why) => hubAgents().filter((a) => a.hub === why).map((a) => a.name).join(", ");
    const why = [
      ["no-remote", "{agents} reaches no MCP server over HTTP, so it keeps each server's own entry."],
      ["taken", "A server in the library is called magpie, so {agents} keeps each server's own entry: give that server another name for the router to be used."],
      ["own", "{agents} has an MCP server called magpie of its own, which magpie leaves as it is, so it keeps each server's own entry."],
    ];
    for (const [k, text] of why) {
      const n = names(k);
      if (n) {
        const p = el("p", "lib-aside lib-hub-why", t(text, { agents: n }));
        p.dataset.why = k;
        out.push(p);
      }
    }
    // skills in an agent's own folder that the library doesn't have are not
    // listed, so each stays in its prompt
    const mine = skillAgents().filter((a) => !a.gone && (a.skillRouter === "on" || a.skillRouter === "none")
      && lib.foundSkills.some((f) => !f.manual && f.agents.includes(a.id))).map((a) => a.name).join(", ");
    if (mine) {
      const p = el("p", "lib-aside lib-hub-why", t("{agents} has skills of its own that aren't in the library, so the router doesn't list them: bring them in below.", { agents: mine }));
      p.dataset.why = "found";
      out.push(p);
    }
    return out;
  }
  // What the router says of itself to the agents (packinggoodsllc_07668 on
  // Discord: one magpie in China and one in Los Angeles serve the same
  // tools, and an agent fetching Google should pick Los Angeles): before
  // each tool's description and in what it tells an agent connecting.
  // Empty, an agent on another computer is told this computer's name,
  // shown as the placeholder. Saved on Enter or leaving the field, this
  // computer's own (settings' routerNote).
  function hubNoteRow() {
    const row = el("div", "row lib-row lib-hubnoterow");
    const who = el("div", "who");
    who.append(el("div", "name", t("Router name")));
    who.append(el("div", "sub wraps", t("Agents connected to more than one router tell them apart by it. Empty, agents on other computers are told this computer's name.")));
    const f = el("input", "lib-filter lib-hubname");
    f.type = "text";
    f.value = lib.hubNote || "";
    f.placeholder = lib.hubName || "";
    f.maxLength = 80;
    f.spellcheck = false;
    f.autocomplete = "off";
    f.setAttribute("aria-label", t("Router name"));
    let saved = lib.hubNote || "";
    const save = () => {
      const note = f.value.trim().replace(/\s+/g, " ");
      if (note === saved) return;
      const was = saved;
      saved = note;
      // a change of settings, not of the agents' files: no result to report
      change("servers/hub-note", { note }).then((ok) => {
        if (!ok) { saved = was; return; }
        status(note
          ? t("Agents are told this router is {name} — restart them to read it", { name: note })
          : t("Agents on other computers are told this computer's name — restart them to read it"), "ok");
      });
    };
    f.onkeydown = (e) => {
      e.stopPropagation();
      // an Enter that ends an input method's composition is the IME's
      if (e.isComposing || e.keyCode === 229) return;
      if (e.key === "Enter") { e.preventDefault(); f.blur(); }
      if (e.key === "Escape") { e.preventDefault(); f.value = saved; f.blur(); }
    };
    f.onblur = save;
    row.append(glyph(GLYPH.doc), who, f);
    return row;
  }
  // Tools on demand (ArvinCharl on X: the router's tools filled the
  // context): on, the default, an agent that doesn't search its MCP tools
  // itself and is given more than a few is given two in their place, one
  // finding a tool and one running it (gateway.hubOnDemand). Off gives
  // every agent every tool whole (settings' routerAllTools).
  function hubToolsRow() {
    const row = el("div", "row lib-row lib-hubtoolsrow");
    const who = el("div", "who");
    who.append(el("div", "name", t("Tools on demand")));
    who.append(el("div", "sub wraps", t("An agent given more than 10 tools gets two in their place, one that finds a tool and one that runs it, so their definitions stay out of every request. Claude Code, Claude Desktop, Codex and Pi find tools themselves and are given every one.")));
    const on = !lib.hubAllTools;
    row.append(glyph(GLYPH.search), who, toggle(on, t("Tools on demand"), (v) =>
      change("servers/hub-tools", { on: !v }).then((ok) => {
        if (ok) status(v
          ? t("Agents are given the router's tools on demand — restart them to use it")
          : t("Agents are given every tool of the router — restart them to use it"), "ok");
      })));
    return row;
  }
  // every server is behind the hub, magpie's own image server too, but one
  // the user keeps direct (#4382): each agent's own entry (inHub in
  // mcp_hub.go)
  const inHub = (s) => s.hub !== "direct";
  // the servers each agent is given directly, by name, for the hub's lines
  const directNames = () => lib.servers.filter((s) => !inHub(s)).map((s) => s.name).join(", ");
  // what the hub serves: its servers, their tools as far as checked, and
  // the agents given it
  function hubStats() {
    const servers = lib.servers.filter(inHub);
    const parts = [servers.length === 1 ? t("1 server") : t("{n} servers", { n: servers.length })];
    const known = servers.filter((s) => health.get(s.name)?.h?.state === "ok");
    if (known.length) {
      const n = known.reduce((sum, s) => sum + Math.max(0, (health.get(s.name).h.tools || 0) - (s.disabledTools?.length || 0)), 0);
      parts.push(n === 1 ? t("1 tool") : t("{n} tools", { n }));
    }
    const given = hubAgents().filter((a) => a.hub === "on").map((a) => a.name);
    if (given.length) parts.push(t("in {agents}", { agents: given.length > 3 ? t("{first} and {n} more", { first: given.slice(0, 2).join(", "), n: given.length - 2 }) : given.join(", ") }));
    // the owner, counting two servers and seeing one: where the other went
    const direct = agentless() ? "" : directNames();
    if (direct) parts.push(t("{name} is given to each agent directly", { name: direct }));
    return parts.join(" · ");
  }
  const hubURL = () => mcpAddress(state.settings || {}, lib.hubPath.replace(/\/+$/, ""));

  // Connect to the hub (#2679): two tabs. Agents: every agent magpie looks
  // after, in three groups, each a switch of its own. Not connected
  // comes first, then connected, then the agents that keep each server's
  // own entry, with why. Over them is one line of how many go through
  // magpie, and in the bar a Connect all for the rest. With many agents,
  // the connected and the kept fold to one row each and a search narrows
  // them all. Other apps: the one address, and a row per app with what
  // adds it, by its install link (servers/hub-open, which the app
  // confirms) or by copying its entry. The agents are magpie's to
  // connect, never a command for the user to run; Cursor is among them
  // when it's here.
  let hubTab = "agents";
  const hubFold = { on: true, kept: true };
  function connectHub() {
    const url = hubURL();
    const json = (o) => JSON.stringify(o, null, 2);
    const served = () => lib.servers.filter(inHub);
    const ed = el("div", "editor lib-editor lib-hubsheet");
    const head = el("div", "ehead");
    head.append(hubMark(), el("b", "", t("Connect an app")));
    ed.append(head);
    const pane = el("div", "lib-hubpane");
    const bar = el("div", "bar");
    let busy = false, changed = false, q = "";
    const why = new Set();
    if (!mcpAgents().length) hubTab = "apps";
    if (mcpAgents().length) ed.append(segs([["agents", t("Agents")], ["apps", t("Other apps")]], hubTab, (k) => { hubTab = k; draw(); }));
    ed.append(pane, bar);

    // posts agents-all for some agents, on or off, and says what came of it
    const give = async (ids, on, b) => {
      if (busy) return;
      busy = true;
      for (const x of ed.querySelectorAll(".lib-hubpane button, .bar button")) x.disabled = true;
      b?.classList.add("busy");
      const named = mcpAgents().filter((a) => ids.includes(a.id));
      try {
        take(await api("library/servers/agents-all", { agents: ids, on }));
        changed = true;
        const now = mcpAgents().filter((a) => ids.includes(a.id));
        const failed = now.filter((a) => on ? a.hub !== "on" : a.hub === "on");
        if (failed.length) status(t("{agent} couldn't be connected", { agent: failed.map((a) => a.name).join(", ") }), "warn", 8000);
        else if (!on) status(t("{agent} no longer has the library's servers — restart it to read the change", { agent: named[0]?.name || "" }), "ok");
        else if (named.length === 1) status(t("{agent} is connected — restart it to use magpie's tools", { agent: named[0].name }), "ok");
        else status(t("{n} agents are connected — restart them to use magpie's tools", { n: named.length }), "ok");
        render();
      } catch (e) {
        status(e.message, "err", 6000);
      }
      busy = false;
      if (ed.isConnected) draw();
    };

    // an agent's row: a switch, or, kept, why in a word and in full
    const agentRow = (a) => {
      const st = a.hub || (a.noRemote ? "no-remote" : "");
      const row = el("div", "row lib-row lib-hubagent");
      row.dataset.agent = a.id;
      row.dataset.state = st || "none";
      const who = el("div", "who");
      who.append(el("div", "name", a.name));
      if (a.aside && st !== "no-remote") who.append(el("div", "sub", a.aside));
      row.append(agentIcon(a.icon), who);
      if (st === "on" || !st) {
        const sw = toggle(st === "on", t("{agent} through magpie", { agent: a.name }), (on) => give([a.id], on, sw));
        if (!st && !served().length) { sw.disabled = true; sw.title = t("The library has no MCP servers yet"); }
        row.append(sw);
        return [row];
      }
      row.classList.add("lib-hubkept");
      row.append(el("span", "lib-hubtag", { "no-remote": t("No HTTP"), own: t("Has its own magpie"), taken: t("Name taken") }[st]));
      const open = why.has(a.id);
      const i = el("button", "lib-hubinfo");
      i.type = "button";
      i.setAttribute("aria-expanded", open ? "true" : "false");
      i.setAttribute("aria-label", t("Why"));
      i.title = t("Why");
      i.append(svg("M8 2.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 0 0 0-11z M8 7.2v3.6 M8 5.2v.1", 14, 1.3));
      i.onclick = (e) => { e.stopPropagation(); open ? why.delete(a.id) : why.add(a.id); draw(); };
      row.append(i);
      if (!open) return [row];
      const p = el("p", "lib-hubwhy", {
        "no-remote": t("It reaches no MCP server over HTTP, so it keeps each server's own entry"),
        own: t("It has a server called magpie of its own, which magpie leaves as it is"),
        taken: t("A server in the library is called magpie: give it another name to connect"),
      }[st]);
      p.dataset.why = st;
      return [row, p];
    };
    // a group: its name and count, a fold when there are many, its rows
    const group = (key, title, list, many) => {
      if (!list.length) return [];
      const sec = el("div", "lib-hubgrp");
      sec.dataset.group = key;
      const h = el("div", "lib-hubsec");
      h.append(el("span", "", title), el("span", "lib-hubn", String(list.length)));
      const folds = many && key !== "off" && !q;
      const folded = folds && hubFold[key];
      if (folds) {
        const f = button(folded ? t("Show all") : t("Hide"), "lib-hubfold", () => { hubFold[key] = !hubFold[key]; draw(); });
        f.setAttribute("aria-expanded", folded ? "false" : "true");
        h.append(f);
      }
      const box = el("div", "list lib-list lib-hubagents");
      if (folded) {
        // one row for them all: a few icons, a couple of names, how many
        const row = el("button", "row lib-row lib-hubfolded");
        row.type = "button";
        const stack = el("span", "lib-hubstack");
        for (const a of list.slice(0, 4)) stack.append(agentIcon(a.icon));
        const names = list.length > 2
          ? t("{first} and {n} more", { first: list.slice(0, 2).map((a) => a.name).join(", "), n: list.length - 2 })
          : list.map((a) => a.name).join(", ");
        row.append(stack, el("span", "name", names), svg("M6 3.5l4.5 4.5L6 12.5", 12, 1.6));
        row.onclick = (e) => { e.stopPropagation(); hubFold[key] = false; draw(); };
        box.append(row);
      } else box.append(...list.flatMap(agentRow));
      sec.append(h, box);
      return [sec];
    };

    const drawAgents = () => {
      const all = hubAgents();
      const st = (a) => a.hub || (a.noRemote ? "no-remote" : "");
      const can = all.filter((a) => st(a) === "on" || !st(a));
      const on = can.filter((a) => st(a) === "on");
      const kept = all.filter((a) => !can.includes(a));
      const many = all.length > 8;
      // agents → magpie → servers, and the one line of where things stand
      const flow = el("div", "lib-hubflow");
      const route = el("div", "lib-hubroute");
      const ends = (items, more) => {
        const s = el("span", "lib-hubstack");
        s.append(...items);
        if (more > 0) s.append(el("span", "ic lib-hubmore", "+" + more));
        return s;
      };
      // magpie itself in the middle: the bird the sidebar shows, on a plain tile
      const node = el("span", "lib-hubnode");
      node.innerHTML = '<svg viewBox="0 0 44 44" width="22" height="22" aria-hidden="true"><use href="#bird"/></svg>';
      const srv = served();
      route.append(ends(on.slice(0, 3).map((a) => agentIcon(a.icon)), on.length - 3), el("span", "lib-hubwire"), node, el("span", "lib-hubwire"),
        ends(srv.slice(0, 3).map((s) => mark(s.icon, s.transport === "stdio" ? GLYPH.cmd : GLYPH.web)), srv.length - 3));
      flow.append(route);
      flow.append(el("p", "lib-hublead", !srv.length ? t("The library has no MCP servers yet")
        : can.length === 1 ? t(on.length ? "{agent} uses the library's servers through magpie" : "{agent} can use the library's servers through magpie", { agent: can[0].name })
        : t("{on} of {all} agents use the library's servers through magpie", { on: on.length, all: can.length })));
      const left = can.length - on.length;
      const sub = [left ? t("{n} more can be connected", { n: left }) : t("Each server runs once, for all of them")];
      if (kept.length) sub.push(kept.length === 1 ? t("1 keeps each server's own entry") : t("{n} keep each server's own entry", { n: kept.length }));
      const direct = directNames();
      if (direct) sub.push(t("{name} is given to each agent directly", { name: direct }));
      // each part whole on its line, the dot kept with the part before it
      const line = el("p", "lib-hubsub");
      sub.forEach((x, i) => line.append(...(i ? [" "] : []), el("span", "", x + (i < sub.length - 1 ? "\u00a0·" : ""))));
      flow.append(line);
      const out = [flow];
      if (many) {
        const box = el("label", "lib-hubsearch");
        box.append(svg("M7 2.5a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9z M10.3 10.3l3.2 3.2", 13, 1.5));
        const inp = el("input");
        inp.type = "search";
        inp.id = "lib-hubq";
        inp.placeholder = t("Find an agent");
        inp.setAttribute("aria-label", t("Find an agent"));
        inp.value = q;
        inp.oninput = () => { q = inp.value.trim(); draw(); };
        box.append(inp);
        out.push(box);
      }
      const m = (a) => !q || a.name.toLowerCase().includes(q.toLowerCase());
      const groups = [
        ...group("off", t("Not connected"), can.filter((a) => !st(a) && m(a)), many),
        ...group("on", t("Connected"), on.filter(m), many),
        ...group("kept", t("Keep each server's own entry"), kept.filter(m), many),
      ];
      out.push(...(groups.length ? groups : [el("p", "lib-hubnone", t("No agent called “{q}”", { q }))]));
      return out;
    };

    // any other app: the address, and a row per app with what adds it
    const drawApps = () => {
      const out = [el("p", "lib-hublead lib-hubapps", t("Any app that speaks MCP connects at this address"))];
      const addr = el("div", "lib-hubaddr");
      addr.append(el("code", "", url), copyBtn(url, t("Address")));
      out.push(addr, el("p", "lib-hubcap", t("From another computer, use the address magpie is reached at there, with a gateway key: Authorization: Bearer <key>.")));
      const apps = [
        ...(mcpAgents().some((a) => a.id === "cursor") ? [] : [{ id: "cursor", icon: "cursor", name: "Cursor", link: true, where: t("Or add it to ~/.cursor/mcp.json"), text: json({ mcpServers: { magpie: { url } } }) }]),
        { id: "vscode", icon: "vscode", name: "VS Code", link: true, where: t("Or add it to .vscode/mcp.json, or to your user mcp.json"), text: json({ servers: { magpie: { type: "http", url } } }) },
        { id: "windsurf", icon: "windsurf", name: "Windsurf", where: t("Paste it into ~/.codeium/windsurf/mcp_config.json"), text: json({ mcpServers: { magpie: { serverUrl: url } } }) },
        { id: "json", icon: "generic", name: t("Other"), where: t("Cline, LM Studio and any app that takes MCP servers as JSON"), text: json({ mcpServers: { magpie: { type: "http", url } } }) },
      ];
      const sec = el("div", "lib-hubgrp");
      sec.append(el("div", "lib-hubsec", t("Add to an app")));
      const box = el("div", "list lib-list lib-hubappl");
      for (const a of apps) {
        const row = el("div", "row lib-row lib-hubapp");
        row.dataset.app = a.id;
        const who = el("div", "who");
        const link = a.link && !web;
        who.append(el("div", "name", a.name), el("div", "sub", link ? t("{app} asks you to confirm, then it's ready", { app: a.name }) : a.where));
        row.append(icon(a.icon), who);
        if (link) {
          const go = button(t("Add"), "lib-hubpill lib-hubadd", async () => {
            go.disabled = true;
            try {
              await api("library/servers/hub-open", { app: a.id });
              status(t("{app} asks you to confirm adding magpie", { app: a.name }));
            } catch (e) { status(e.message, "err"); }
            go.disabled = false;
          });
          go.title = t("Add to {app}", { app: a.name });
          row.append(go);
        }
        const cp = button(t("Copy entry"), "lib-hubpill lib-hubcopy", () => copy(a.text, a.name, cp, t("{app}'s entry copied", { app: a.name })));
        cp.title = a.text;
        if (link) cp.classList.add("quiet");
        row.append(cp);
        box.append(row);
      }
      sec.append(box);
      out.push(sec);
      return out;
    };

    const done = button(t("Done"), "", () => closeLibModal(true));
    const draw = () => {
      const top = pane.scrollTop;
      const focus = document.activeElement?.id === "lib-hubq";
      pane.replaceChildren(...(hubTab === "agents" ? drawAgents() : drawApps()));
      pane.scrollTop = top;
      if (focus) { const i = pane.querySelector("#lib-hubq"); i.focus(); i.setSelectionRange(i.value.length, i.value.length); }
      // the one primary action: Connect all while there are agents to connect
      const rest = hubTab === "agents" && served().length ? hubAgents().filter((a) => !a.hub && !a.noRemote) : [];
      bar.replaceChildren(el("span", "grow lib-hubnote", changed ? t("Restart an agent for it to read the change") : ""), done);
      done.className = "text" + (rest.length ? "" : " primary");
      done.disabled = false;
      if (rest.length) {
        const all = button(t("Connect all"), "primary lib-huball", (e, b) => give(rest.map((a) => a.id), true, b));
        all.title = rest.map((a) => a.name).join(", ");
        bar.append(all);
      }
    };
    draw();
    modal = { save: () => (bar.querySelector(".lib-huball") || done).click() };
    openLib(ed);
  }

  // a row's line under its name: the user's note when there is one, in
  // the place of what it would say (a skill's description, a server's
  // command), which its tooltip then gives after the note. A row keeps its
  // height either way
  function noteSub(note, sub, said) {
    if (!note) return sub;
    const n = el("div", "sub lib-subnote", note);
    n.title = said ? note + "\n\n" + said : note;
    return n;
  }

  function serverRow(s, all) {
    const row = el("div", "row lib-row click lib-server" + (s.hidden ? " off" : ""));
    row.dataset.server = s.name;
    const who = el("div", "who");
    const nm = el("div", "name mono", s.name);
    if (s.signIn?.dead) nm.append(tag(t("Sign-in ran out"), "warn", t("Open it to sign in again")));
    else if (s.signIn?.signedIn) nm.append(tag(t("Signed in"), "lib-signed", t("The agents given it use magpie's sign-in")));
    // kept to this computer: sync carries it nowhere (huhuy72219405 on X)
    // a plugin's tools say whose they are, where they are turned off too
    if (s.plugin) nm.append(tag(t("Plugin"), "lib-local lib-plugin", t("{plugin}'s tools. Plugins can hide them from every agent, keeping the plugin; sync never carries them to your other computers", { plugin: s.plugin })));
    else if (s.local) nm.append(tag(t("Not synced"), "lib-local", s.bridge ? bridgedWhy() : t("Sync never carries it to your other computers")));
    // out of the hub, each agent's own entry (#4382): said only while the hub is on
    if (lib.mcpHub && s.hub === "direct") nm.append(tag(t("Not through the router"), "lib-direct", t("Each agent is given its own entry for it, as with the MCP router off")));
    // its status beside the name, not in it: the name is still the name
    who.classList.add("lib-srvwho");
    who.append(nm, healthEl(s.name));
    const sub = el("div", "sub mono", serverLine(s));
    sub.title = serverLine(s);
    who.append(noteSub(s.note, sub, serverLine(s)), toolsEl(s.name));
    row.append(mark(s.icon, s.transport === "stdio" ? GLYPH.cmd : GLYPH.web), who, ...removeAct(() => confirmRemoveServer(s)),
      // no agent to give it to: other apps reach it through the router
      ...(agentless() ? [] : [agentChips(withGone(all, s.agents), s.agents, chipsChange("servers/agents", s.name, "servers", (x) => serverRow(x, all)), { problems: s.problems, blocked: sseBlocked(s), all: true })]),
      ...serverSwitch(s));
    if (picking) return pickRow(row, s.name);
    // a line of its own under the row, not in the name's column (tao0194)
    if (s.name === "magpie-browser" && !s.agents?.length) row.append(browserAsk(row));
    row.onclick = () => editServer(s);
    row.title = t("Edit {name}", { name: s.name });
    return row;
  }

  // A server off for everyone, or on again as it was (swang20201 on X:
  // an installed server could only be removed). Off, no agent, project or
  // app at the router's address has it, and it keeps its settings, env
  // and sign-in; on, the agents it had get it back. While picking, the
  // bar is the one acting, as with Remove.
  function serverSwitch(s) {
    if (picking) return [];
    const on = !s.hidden;
    return [toggle(on, t("Turn {name} on or off", { name: s.name }), (v) => change("servers/on", { name: s.name, on: v },
      v ? t("{name} is on again", { name: s.name }) : t("{name} is off; its settings are kept", { name: s.name })))];
  }

  // Magpie Browser connected and given to no agent: it is added opt-in, so
  // until an agent is ticked the router offers it to none, and an agent
  // asked to use the browser reached for something else (AppleScript).
  // Once the extension connects, its row asks which agents may use it,
  // each a button that clicks that agent's chip in the row; the first one
  // taken, it says nothing more. Read again every few seconds while the
  // row is shown, so connecting the extension shows it without a reload.
  let browserSeen = null; // { at, link } — the last read of the extension's state, shared by redrawn rows
  function browserLink() {
    if (browserSeen && Date.now() - browserSeen.at < 2500) return browserSeen.link;
    const link = api("library/browser").then((b) => !!b?.connected, () => false);
    browserSeen = { at: Date.now(), link };
    return link;
  }
  function browserAsk(row) {
    const ask = el("div", "lib-browserask");
    ask.hidden = true;
    ask.onclick = (e) => e.stopPropagation();
    const draw = () => {
      const chips = [...row.querySelectorAll(":scope > .lib-agents > .lib-ag[data-agent]")].filter((c) => !cannot(c) && !c.classList.contains("gone"));
      const picks = el("div", "lib-browserask-picks");
      for (const c of chips) {
        const b = button("", "lib-browserask-pick", () => { c.click(); ask.remove(); });
        b.dataset.agent = c.dataset.agent;
        b.append(agentIcon(agentOf(c.dataset.agent)?.icon), el("span", "", nameOf(c.dataset.agent)));
        picks.append(b);
      }
      ask.replaceChildren(el("span", "lib-browserask-dot"), el("span", "lib-browserask-q", t("Connected to your browser. Which agents may use it? They act in your tabs, signed in as you are.")), picks);
    };
    const look = async () => {
      if (!ask.isConnected && ask.dataset.drawn) return;
      ask.dataset.drawn = "1";
      const on = await browserLink();
      if (on && ask.hidden) draw();
      ask.hidden = !on;
      row.classList.toggle("lib-asking", on);
      setTimeout(look, 3000);
    };
    requestAnimationFrame(look);
    return ask;
  }

  // A server's or a hook's own Remove on its row, as a skill's has (#2364,
  // szoxidy: a server turned off for every agent stayed in the list, its
  // only Remove inside the editor its row opens). It asks first in the
  // page, naming the one it takes and what goes with it. While picking,
  // the bar's Remove is the one, and the row keeps the room it had.
  function removeAct(run) {
    if (picking) return [];
    const acts = el("div", "lib-rowacts");
    const rm = button("", "lib-icon danger lib-rm", run);
    rm.append(svg(GLYPH.trash, 13, 1.4));
    rm.title = t("Remove");
    acts.append(rm);
    return [acts];
  }
  function confirmRemoveOne(kind, name, title, lines) {
    const ed = el("div", "editor lib-editor lib-rmone");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", title));
    ed.append(head);
    for (const l of lines) if (l) ed.append(el("p", "lib-confirm", l));
    const bar = el("div", "bar");
    const go = button(t("Remove"), "primary danger-fill", async () => {
      go.disabled = true;
      if (await change(kind + "/remove", { name }, t("{name} removed", { name }))) closeLibModal(true);
      else go.disabled = false;
    });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }
  const takenFromOne = (x) => x.agents?.length
    ? t("It is taken out of the library and out of {agents}.", { agents: x.agents.map(nameOf).join(", ") })
    : t("It is taken out of the library; no agent has it.");
  function confirmRemoveServer(s) {
    const projects = (lib.projects || []).filter((p) => p.servers?.[s.name]?.length).length;
    confirmRemoveOne("servers", s.name, t("Remove the MCP server {name}?", { name: s.name }), [
      takenFromOne(s),
      projects && t("It is taken out of the {n} projects magpie placed it in too.", { n: projects }),
      s.signIn?.signedIn && t("magpie's sign-in to it is forgotten too."),
      t("Servers your agents have that aren't in the library stay as they are."),
    ]);
  }
  function confirmRemoveEnv(v) {
    confirmRemoveOne("env", v.name, t("Remove the variable {name}?", { name: v.name }), [
      takenFromOne(v),
      t("Variables your agents have that aren't in the library stay as they are, and so does one you changed in an agent's file."),
    ]);
  }
  function confirmRemoveHook(h) {
    confirmRemoveOne("hooks", h.name, t("Remove the hook {name}?", { name: h.name }), [
      takenFromOne(h),
      t("Hooks your agents have that aren't in the library stay as they are, and so does one you changed in an agent's file."),
    ]);
  }

  // Whether a server works, as magpie found by connecting to it: started
  // (or reached) and asked for its tools. Its row shows it as a dot and a
  // few words, the whole reason in its tooltip; a click checks it again.
  // A server that listed its tools shows them instead, by name and what
  // each says it does (nf1qaz2w on Discord), with Check again under them.
  const health = new Map(); // name → { key: the server as checked, h: what was found, null while checking }
  const toolsOpen = new Set(); // the servers whose tools are shown
  // a command is started where its agents run, this computer or a WSL
  // distro (mcp_check_wsl.go): agents moved between them is a check anew,
  // and so is the router taking it or leaving it to the agents, which
  // start it in their project and the router in home (inProject)
  const healthKey = (s) => JSON.stringify([s.transport, s.command, s.args, s.env, s.url, s.headers, s.signIn,
    s.transport === "stdio" ? [...new Set((s.agents || []).map((a) => a.split("@wsl:")[1] || ""))].sort() : null,
    s.transport === "stdio" && !!lib.mcpHub && s.hub !== "direct"]);
  const toolList = (name) => { const h = health.get(name)?.h; return h?.state === "ok" ? h.toolList || [] : []; };
  // whether an agent the page shows gets the server, here or in a project
  // magpie keeps whose folder is there. The library keeps every agent it
  // gave a server to, those the page leaves out too: one hidden on the
  // Agents page, one no longer installed, one with no MCP of its own. A
  // server on only for those has every chip off, and isn't started for
  // them on its own (#1597, xiaozhu1337: ida-pro-mcp and windbg-mcp were)
  const inUse = (s) => {
    const shown = new Set(mcpAgents().map((a) => a.id)), inProjects = new Set(projectMCPAgents().map((a) => a.id));
    return (s.agents || []).some((id) => shown.has(id))
      || (lib.projects || []).some((p) => !p.missing && (p.servers?.[s.name] || []).some((id) => inProjects.has(id)));
  };
  // whether the library has it on for anyone at all, shown here or not
  const onAnywhere = (s) => !!s.agents?.length || (lib.projects || []).some((p) => p.servers?.[s.name]?.length);
  // a click checks a server again, or for the first time one in no use;
  // not while it is being checked
  function recheck(name) {
    const s = lib.servers.find((x) => x.name === name), e = health.get(name);
    if (s && (!e || e.h)) checkHealth([s], true);
  }
  function healthEl(name) {
    const b = el("button", "lib-health");
    b.type = "button";
    b.dataset.server = name;
    b.onclick = (e) => {
      e.stopPropagation(); // the row opens the editor
      // a server that asks for a sign-in is signed in to from its status
      // (the owner: a click on "needs sign-in" signs in)
      const s = lib.servers.find((x) => x.name === name);
      if (mcpSigning?.name === name && mcpSigning.id) return editServer(s); // under way: its dialog has Cancel
      if (canSignIn(s)) return signInFromRow(s);
      if (!toolList(name).length) return recheck(name);
      if (toolsOpen.has(name)) toolsOpen.delete(name); else toolsOpen.add(name);
      paintHealth(name);
    };
    paintHealth(b);
    return b;
  }
  // toolsEl is the list a server's status opens: filled by paintHealth
  function toolsEl(name) {
    const box = el("div", "lib-tools");
    box.dataset.server = name;
    box.hidden = true;
    box.onclick = (e) => e.stopPropagation(); // reading or selecting it opens no editor
    paintTools(box);
    return box;
  }
  function paintTools(box) {
    const name = box.dataset.server, list = toolList(name);
    // while it is checked again the list stays as it was, so nothing under
    // the click moves until the answer comes
    if (health.get(name) && !health.get(name).h && !box.hidden) return;
    box.hidden = !(toolsOpen.has(name) && list.length);
    if (box.hidden) return box.replaceChildren();
    const ul = el("ul", "lib-toollist");
    const off = toolsOffOf(name);
    for (const tl of list) {
      const li = el("li", "lib-tool" + (off.includes(tl.name) ? " off" : ""));
      const txt = el("div", "lib-tooltxt");
      txt.append(el("span", "lib-toolname mono", tl.name));
      if (tl.description) txt.append(el("span", "lib-tooldesc", tl.description));
      li.append(txt, toolSwitch(name, tl.name, !off.includes(tl.name)));
      ul.append(li);
    }
    const parts = [ul];
    // the agents given it that can't leave out a single tool get them all,
    // which is said here rather than shown as off for them too
    const cant = (lib.servers.find((s) => s.name === name)?.agents || []).map(agentOf).filter((a) => a && a.mcp && !a.toolsOff);
    if (cant.length) parts.push(el("p", "lib-toolcant", t("{agents} can't turn off single tools: they get every tool of the server.", { agents: cant.map((a) => a.name).join(", ") })));
    const foot = el("div", "lib-toolfoot");
    const h = health.get(name).h;
    if (h.tools > list.length) foot.append(el("span", "note", t("{n} more not shown", { n: h.tools - list.length })));
    foot.append(el("span", "grow"), button(t("Check again"), "", () => recheck(name)));
    parts.push(foot);
    box.replaceChildren(...parts);
  }
  // Each tool has a switch (nf1qaz2w on Discord): one turned off is left
  // out by the agents given the server that can leave out a single tool.
  // A click flips its own switch at once and nothing else on the page, so
  // the list doesn't move under it; quick clicks are sent as one.
  const toolsSending = new Map(); // server → { want: the tools off, as last clicked }
  const toolsOffOf = (name) => toolsSending.get(name)?.want || lib.servers.find((s) => s.name === name)?.disabledTools || [];
  function toolSwitch(name, tool, on) {
    const s = toggle(on, t("{tool} on", { tool }), () => {});
    s.dataset.tool = tool;
    s.onclick = (e) => {
      e.stopPropagation();
      const now = s.getAttribute("aria-checked") !== "true";
      s.classList.toggle("on", now);
      s.setAttribute("aria-checked", String(now));
      s.closest("li")?.classList.toggle("off", !now);
      const cur = toolsOffOf(name);
      setToolsOff(name, now ? cur.filter((x) => x !== tool) : [...new Set([...cur, tool])].sort());
    };
    return s;
  }
  async function setToolsOff(name, want) {
    const w = toolsSending.get(name);
    if (w) { w.want = want; return; }
    const me = { want };
    toolsSending.set(name, me);
    let sent = null, v = null;
    try {
      while (!sent || sent.join("\n") !== me.want.join("\n")) {
        sent = me.want;
        v = await api("library/servers/tools", { name, names: sent });
      }
      toolsSending.delete(name);
      take(v);
      report(v, t("Tools of {name} saved", { name }));
    } catch (e) {
      toolsSending.delete(name);
      status(e.message, "err");
      // the switches go back to what the library has
      for (const box of page.querySelectorAll(`.lib-tools[data-server="${CSS.escape(name)}"]`)) {
        const off = toolsOffOf(name);
        for (const s of box.querySelectorAll(".lib-switch[data-tool]")) {
          const on = !off.includes(s.dataset.tool);
          s.classList.toggle("on", on);
          s.setAttribute("aria-checked", String(on));
          s.closest("li")?.classList.toggle("off", !on);
        }
      }
    }
  }
  async function checkHealth(list, fresh) {
    if (!list.length) return;
    const keys = new Map(list.map((s) => [s.name, healthKey(s)]));
    for (const [name, key] of keys) { health.set(name, { key, h: null }); paintHealth(name); }
    let got = {}, failed = null;
    try { got = (await api("library/mcp/check", { names: [...keys.keys()], fresh: !!fresh })).servers || {}; } catch (e) { failed = e.message; }
    let signIns = false;
    for (const [name, key] of keys) {
      // a server changed while it was checked has been asked for again
      if (health.get(name)?.key !== key) continue;
      const h = got[name] || (failed ? { state: "error", why: "request", detail: failed } : null);
      // the check found magpie's sign-in run out (the server refused even
      // a renewed token): the row's tag says so, not "Signed in" beside
      // "needs sign-in" (新v学员 on Discord)
      const s = lib.servers.find((x) => x.name === name);
      if (h?.signIn && s && (!!h.signIn.signedIn !== !!s.signIn?.signedIn || !!h.signIn.dead !== !!s.signIn?.dead)) {
        s.signIn = h.signIn;
        health.set(name, { key: healthKey(s), h });
        signIns = true;
        continue;
      }
      if (h) health.set(name, { key, h }); else health.delete(name);
      paintHealth(name);
    }
    if (signIns) render();
  }
  function healthWords(h, s) {
    const more = (x) => (h.detail ? x + "\n" + h.detail : x);
    const count = (n) => (n === 1 ? t("1 tool") : t("{n} tools", { n }));
    // it exits in home and lists its tools in a project folder, where an
    // agent starts it (fff-mcp, math_milian on Discord): fine for agents
    // that start it themselves, not those magpie runs it for, in home
    if (h.state === "ok" && h.project) return [count(h.tools), more(t("It started in a project folder and listed its tools, as an agent starts it there. It exits when started in your home folder"))];
    if (h.state === "ok") return [count(h.tools), t("It started and listed its tools")];
    // an agent signed in to it on its own (Claude Code's /mcp, in WSL too)
    // uses it, though magpie's check, without that sign-in, was asked for
    // one: the row says who, not "needs sign-in" (CyberCaius on Discord)
    if (h.state === "auth" && h.agentSignIn?.length) {
      const names = h.agentSignIn.map(nameOf);
      const text = t("signed in in {agent}", { agent: names[0] }) + (names.length > 1 ? " +" + (names.length - 1) : "");
      return [text, t("{agents}: signed in to it on its own, and uses it. magpie's check doesn't have that sign-in, so it can't list the tools: open it to sign in once in magpie too", { agents: names.join(", ") })];
    }
    if (h.state === "auth") {
      const why = h.oauth
        ? (s?.transport === "http" ? t("The server asks for a sign-in — open it to sign in once in magpie") : t("The server asks for a sign-in"))
        : t("The server refused magpie (HTTP {code}) — a key in its headers may be missing or wrong", { code: h.code || 401 });
      return [t("needs sign-in"), h.detail ? why + "\n" + h.detail : why];
    }
    switch (h.why) {
      case "home": return [t("won't start in home"), more(t("It exits when started in your home folder, where magpie runs it for the agents given it, and works in a project folder. Turn off Through the MCP router in its editor, so each agent starts it in its own project"))];
      case "notfound": return [t("can't start: {cmd} not found", { cmd: h.detail }), t("Can't start it: there is no {cmd} on the PATH magpie has", { cmd: h.detail })];
      case "start": return [t("can't start"), more(t("Can't start it"))];
      case "exited": return [h.code ? t("exited ({code})", { code: h.code }) : t("exited"), more(h.code ? t("It exited with code {code} before listing its tools", { code: h.code }) : t("It exited before listing its tools"))];
      // which step went unanswered, and for how long (#1467): a slow server
      // lists its tools long after it initialized
      case "timeout": {
        const n = Math.round((h.waited || 0) / 1000);
        if (!h.step) return [t("no answer"), more(t("No answer in {n} seconds", { n }))];
        return [t("no answer to {step}", { step: h.step }), more(t("No answer to {step} in {n} seconds", { step: h.step, n }))];
      }
      case "http": return ["HTTP " + h.code, t("The server answered {status}", { status: h.detail })];
      case "refused": return [t("connection refused"), more(t("Nothing is listening at that address"))];
      case "unreachable": return [t("can't reach"), more(t("Can't reach the server"))];
      case "protocol": return [t("bad reply"), more(t("It answered, but not as an MCP server does"))];
      // a ${NAME} magpie's own environment hasn't; an agent started from a
      // shell that has it still gets it
      case "novar": return [t("{names} not set", { names: h.detail }), t("magpie can't check it: {names} isn't set where magpie runs. An agent started where it is set still gets it", { names: h.detail })];
      // the agents given it run in a WSL distro that isn't running, which
      // a check doesn't start: nothing is known of the server
      case "wsldown": return [t("WSL {distro} not running", { distro: h.detail }), t("magpie can't check it: the agents given it run in WSL {distro}, which isn't running, and a check doesn't start it", { distro: h.detail })];
      default: return [t("couldn't check"), more(t("magpie couldn't check it"))];
    }
  }
  // paintHealth fills a row's status in place, by the button or the
  // server's name, so a check ending moves nothing on the page
  function paintHealth(x) {
    // the hub's count of tools follows each check
    const stats = page.querySelector(".lib-hubstats");
    if (stats) stats.textContent = hubStats();
    const boxes = typeof x === "string" ? page.querySelectorAll(`.lib-health[data-server="${CSS.escape(x)}"]`) : [x];
    for (const b of boxes) {
      const e = health.get(b.dataset.server);
      b.removeAttribute("aria-expanded");
      if (!e) {
        // one in no use waits for a click (#1597); one in use is about
        // to be checked
        const s = lib.servers.find((x) => x.name === b.dataset.server);
        b.hidden = !s || inUse(s);
        b.className = "lib-health idle";
        b.replaceChildren(el("span", "", t("not checked")));
        b.title = t(s && onAnywhere(s) ? "No agent shown here has it on, so it isn't checked on its own" : "No agent has it, so it isn't checked on its own") + "\n" + t("Click to check it");
        continue;
      }
      b.hidden = false;
      const sg = mcpSigning?.name === b.dataset.server ? mcpSigning : null;
      if (sg && (sg.state === "starting" || sg.state === "waiting")) {
        b.className = "lib-health auth";
        b.replaceChildren(el("span", "", sg.state === "starting" ? t("Opening the sign-in…") : t("Finish signing in in your browser…")));
        b.title = t("Click to see the sign-in");
        continue;
      }
      if (!e.h) {
        b.className = "lib-health checking";
        b.replaceChildren(el("span", "", t("checking…")));
        b.title = t("Connecting to it to list its tools");
        continue;
      }
      let [text, tip] = healthWords(e.h, lib.servers.find((s) => s.name === b.dataset.server));
      // a command agents in WSL start is checked where they run: the tip
      // says where, and a failure which place it was (loong on Discord)
      const places = (e.h.checked || []).map((p) => (p ? "WSL " + p : "Windows"));
      if (places.length && e.h.state === "ok") tip += "\n" + t("Checked in: {places}", { places: places.join(", ") });
      else if (places.length) {
        text += " · " + places[places.length - 1];
        tip += "\n" + t("Failed in: {place}", { place: places[places.length - 1] });
      }
      // a check magpie couldn't make for want of a variable only the
      // agent's own launch sets says nothing of the server, so it isn't
      // painted as broken (#2308, ASD-max: Codex's host-only codex_app,
      // whose pipe Codex itself hands it)
      b.className = "lib-health " + (e.h.state === "ok" || e.h.agentSignIn?.length ? "ok" : e.h.state === "auth" ? "auth" : e.h.why === "novar" || e.h.why === "wsldown" ? "unknown" : "err");
      b.replaceChildren(el("span", "", text));
      if (toolList(b.dataset.server).length) {
        const open = toolsOpen.has(b.dataset.server);
        b.classList.add("has-list");
        b.setAttribute("aria-expanded", String(open));
        b.title = tip + "\n" + (open ? t("Click to hide them") : t("Click to see them"));
      } else b.title = tip + "\n" + (canSignIn(lib.servers.find((s) => s.name === b.dataset.server)) ? t("Click to sign in") : t("Click to check again"));
    }
    const name = typeof x === "string" ? x : x.dataset.server;
    for (const box of page.querySelectorAll(`.lib-tools[data-server="${CSS.escape(name)}"]`)) paintTools(box);
  }

  // magpie's sign-in to a remote server (#615): signed in once here, and
  // every agent given the server reaches it through magpie, with it
  let mcpSigning = null; // { name, id, state, error } while one is under way
  let signDraw = null; // the open dialog's sign-in box, drawn as a sign-in moves
  function signMoved(name) {
    if (signDraw && !signDraw.box.isConnected) signDraw = null;
    signDraw?.draw();
    if (name) paintHealth(name);
  }
  // one the row's status signs in to: a remote server that asked for a
  // sign-in, unless an agent's own sign-in already uses it
  function canSignIn(s) {
    const h = s && health.get(s.name)?.h;
    return !!h && s.transport === "http" && h.state === "auth" && !!h.oauth && !h.agentSignIn?.length;
  }
  // a sign-in from a row's status, as the dialog's Sign in starts one; the
  // dialog opens when the user has something to do there: the browser
  // didn't open (the link to open by hand) or the sign-in failed
  async function signInFromRow(s) {
    const name = s.name;
    mcpSigning = { name, state: "starting" };
    signMoved(name);
    try {
      const st = await api("library/mcp-signin", { name });
      if (mcpSigning?.name !== name || mcpSigning.id) return; // canceled
      if (web && st.url && !st.linkOnly) api("open", { url: st.url }).catch(() => {});
      mcpSigning = { ...st, name };
      signMoved(name);
      if (st.openError || st.linkOnly) editServer(s);
      followSignIn(st.id, name);
    } catch (e) {
      if (mcpSigning?.name !== name) return;
      mcpSigning = { name, state: "failed", error: t(e.message) };
      signMoved(name);
      editServer(s);
    }
  }
  // followed with the dialog closed too, for the row to say it's signed in
  async function followSignIn(id, name) {
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
      signMoved(name);
      return;
    }
  }
  // name is the server as saved ("" for one being added); ready saves what
  // the editor shows when it isn't that (a URL changed to the one that
  // signs in, as Exa's ?login) and gives the name it's saved as, for the
  // sign-in to be to the server the form shows. ready.saves says it would
  // save, and the button then says so; box.redraw draws it again as the
  // form changes
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
            signMoved(name);
          }));
      } else if (cur.accounts?.length > 1) {
        // several sign-ins: the one in use is what every agent is given
        box.append(acctList(cur.accounts.map((a) => ({ id: a.id, name: a.label || t("Account {n}", { n: cur.accounts.indexOf(a) + 1 }), on: a.active, bad: a.dead })), "signin", name, draw));
        line.append(addSign(), el("span", "grow"), button(t("Sign out"), "", signOut));
      } else if (cur.signedIn && !cur.dead) {
        line.append(tag(t("Signed in"), "lib-signed"), el("span", "note", t("The agents given it use magpie's sign-in")), el("span", "grow"), name ? addSign() : "", button(t("Sign out"), "", signOut));
      } else {
        if (cur.dead) line.append(tag(t("Sign-in ran out"), "warn"));
        line.append(button(ready?.saves?.() ? t("Save and sign in") : cur.dead ? t("Sign in again") : t("Sign in"), "action", () => start()));
      }
      box.append(line);
      // the browser didn't start (#2043): the page to open by hand, rather
      // than waiting on one that never opened until the sign-in times out;
      // or the user keeps sign-in pages to a browser of their own
      // (settings.SignInLinkOnly): the link to copy there, or open here
      if (sg?.state === "waiting" && (sg.openError || sg.linkOnly) && sg.url) {
        box.append(sg.linkOnly
          ? el("div", "note", t("magpie didn't open the sign-in page, as set in Settings. Copy the link below into the browser you sign in with."))
          : el("div", "lib-signin-err", t("magpie couldn't open your browser: {error}. Open the sign-in link below in a browser yourself.", { error: sg.openError })));
        const link = el("div", "lib-signin-link");
        const u = el("code", "", sg.url);
        u.title = sg.url;
        const cp = copyBtn(sg.url, t("Sign-in link"));
        cp.title = t("Copy link");
        link.append(u, cp);
        if (sg.linkOnly) link.append(button(t("Open in browser"), "", () => browse(sg.url)));
        box.append(link);
      }
      if (sg?.state === "failed") box.append(el("div", "lib-signin-err", sg.error));
    };
    // another sign-in to the server, called what the reader types
    let adding = false;
    function addSign() {
      if (!adding) return button(t("＋ Add account"), "lib-addacct", () => { adding = true; draw(); box.querySelector(".lib-acct-new input")?.focus(); });
      const f = el("span", "lib-acct-new");
      const i = field2("", t("Name"), () => {});
      i.maxLength = 60;
      const go = () => { adding = false; start(i.value.trim()); };
      i.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); e.stopPropagation(); go(); } });
      f.append(i, button(t("Sign in"), "action", go), button(t("Cancel"), "", () => { adding = false; draw(); }));
      return f;
    }
    async function start(label) {
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
        const st = await api("library/mcp-signin", typeof label === "string" ? { name, add: true, label } : { name });
        if (web && st.url && !st.linkOnly) api("open", { url: st.url }).catch(() => {});
        mcpSigning = { ...st, name };
        draw();
        followSignIn(st.id, name);
      } catch (e) {
        if (!mcpSigning) return; // canceled
        // magpie's words in the reader's language, where it has them
        mcpSigning = { name, state: "failed", error: t(e.message) };
        draw();
        ready?.failed?.(e.message);
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
    box.redraw = draw;
    signDraw = { box, draw };
    draw();
    return box;
  }

  // the accounts of a server, one in use (Elorynth on X): a click on a row
  // puts it in use, in place; every agent given the server follows. kind is
  // "signin" for magpie's OAuth sign-ins, "header" for sets of headers
  function acctList(rows, kind, name, redraw) {
    const list = el("div", "lib-accts");
    list.setAttribute("role", "radiogroup");
    const call = async (op, id) => {
      try {
        take(await api("library/mcp-account", { name, kind, op, id }));
        render();
        redraw();
      } catch (e) { status(e.message, "err", 6000); }
    };
    for (const r of rows) {
      const row = el("div", "lib-acct" + (r.on ? " on" : ""));
      const pick = el("button", "lib-acct-pick");
      pick.setAttribute("role", "radio");
      pick.setAttribute("aria-checked", r.on ? "true" : "false");
      pick.append(el("span", "lib-acct-dot"), el("span", "lib-acct-name", r.name));
      if (r.bad) pick.append(tag(t("Sign-in ran out"), "warn"));
      pick.onclick = (e) => { e.stopPropagation(); if (!r.on) call("use", r.id); };
      row.append(pick);
      if (r.id !== "") {
        const x = button("", "lib-x", () => call("remove", r.id));
        x.append(svg("M4.5 4.5l7 7M11.5 4.5l-7 7", 11, 1.6));
        x.title = t("Remove");
        row.append(x);
      }
      list.append(row);
    }
    return list;
  }
  // Authorization headers a server can be asked with, besides its own
  function headerAccounts(name) {
    const box = el("div", "lib-hdr-accts");
    let adding = false;
    const draw = () => {
      box.replaceChildren();
      const s = lib.servers.find((x) => x.name === name);
      if (!s) return;
      const l = s.accounts || [];
      if (l.length) {
        box.append(acctList([{ id: "", name: t("Default"), on: !s.account }, ...l.map((a) => ({ id: a.id, name: a.name, on: a.id === s.account }))], "header", name, draw));
      }
      if (!adding) {
        box.append(button(t("＋ Add account"), "lib-addacct", () => { adding = true; draw(); box.querySelector(".lib-acct-form input")?.focus(); }));
        return;
      }
      const form = el("div", "lib-acct-form");
      let label = "", headers = { Authorization: "" };
      const add = async () => {
        try {
          take(await api("library/mcp-account", { name, kind: "header", op: "add", label, headers }));
          adding = false;
          render();
          draw();
        } catch (e) { status(e.message, "err", 6000); }
      };
      const n = field2("", t("Name"), (v) => { label = v; });
      n.maxLength = 60;
      form.append(n, pairs(headers, "Authorization", "Bearer …", (v) => { headers = v; }),
        el("div", "lib-acct-act"), );
      form.lastChild.append(button(t("Add"), "action", add), button(t("Cancel"), "", () => { adding = false; draw(); }));
      box.append(form);
    };
    box.redraw = draw;
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
    // gone from the agents' own MCP settings without bringing it in first
    // (#1618), as a skill of their own is (#1303)
    const rm = button("", "lib-icon danger", () => confirmRemoveFoundServer(f));
    rm.append(svg(GLYPH.trash, 13, 1.4));
    rm.title = t("Remove");
    row.append(b, rm);
    return row;
  }

  function confirmRemoveFoundServer(f) {
    const s = f.server;
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", t("Remove {name}?", { name: s.name })));
    ed.append(head);
    const agents = s.agents.map(nameOf).filter(Boolean);
    ed.append(el("p", "lib-confirm", t("It is taken out of the MCP settings of {agents}. Each file is copied to magpie's backups before it changes.", { agents: agents.join(", ") })));
    if (f.others?.length) ed.append(el("p", "lib-confirm", t("{agents} has another server by this name; that one stays.", { agents: f.others.map(nameOf).join(", ") })));
    const bar = el("div", "bar");
    const go = button(t("Remove"), "primary danger-fill", async () => { if (await change("servers/remove-found", { name: s.name }, t("{name} removed", { name: s.name }))) closeLibModal(true); });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  // A command line split the way a shell would, quotes and all.
  function words(s) {
    const out = [];
    let cur = "", q = "", any = false;
    for (let i = 0; i < s.length; i++) {
      const c = s[i];
      if (q) {
        // on Windows "" in quotes is one ", as quote() writes it
        if (c === q && WIN_LINE && q === '"' && s[i + 1] === '"') cur += s[++i];
        else if (c === q) q = "";
        else if (c === "\\" && q === '"' && !WIN_LINE && i + 1 < s.length) cur += s[++i];
        else cur += c;
      } else if (c === "'" || c === '"') { q = c; any = true; }
      else if (c === "\\" && !WIN_LINE && i + 1 < s.length) { cur += s[++i]; any = true; }
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
    // an Enter or Escape that ends an input method's composition (a
    // Chinese description, #2011) is the IME's, not a save or a close
    i.onkeydown = (e) => { e.stopPropagation(); if (e.isComposing || e.keyCode === 229) return; if (e.key === "Escape") closeLibModal(); else if (e.key === "Enter" && modal?.save) { e.preventDefault(); modal.save(); } };
    return i;
  }

  // lineBox is a command line in a box that wraps, so a long one (npx
  // with a database's host, user, password and name, hema_17384 on
  // Discord) is read and edited whole. It is still one line: Enter saves,
  // and a line break is a space between words, a shell's \ before it too.
  function lineBox(value, placeholder, onInput) {
    const ta = el("textarea", "lib-text small mono lib-cmd");
    ta.rows = 1;
    ta.value = value || "";
    ta.placeholder = placeholder || "";
    ta.spellcheck = false;
    ta.setAttribute("autocapitalize", "off");
    ta.setAttribute("autocorrect", "off");
    ta.oninput = () => onInput(WIN_LINE ? ta.value : ta.value.replace(/\\\r?\n/g, " "));
    ta.onkeydown = (e) => { e.stopPropagation(); if (e.isComposing || e.keyCode === 229) return; if (e.key === "Escape") closeLibModal(); else if (e.key === "Enter" && modal?.save) { e.preventDefault(); modal.save(); } };
    autosize(ta, 27);
    return ta;
  }

  // noteBox is the user's note on a server or a skill (youyo.p on Discord:
  // with many, which is which and when to use it). One line of at most 200
  // characters, as a hook's description (#2011); the library alone keeps
  // it, and no agent is given it.
  const NOTE_MAX = 200;
  function noteBox(value, placeholder, onInput) {
    const ta = el("textarea", "lib-text small lib-notein");
    ta.rows = 1;
    ta.value = value || "";
    ta.placeholder = placeholder;
    ta.maxLength = NOTE_MAX;
    ta.oninput = () => onInput(ta.value);
    ta.onkeydown = (e) => { e.stopPropagation(); if (e.isComposing || e.keyCode === 229) return; if (e.key === "Escape") closeLibModal(); else if (e.key === "Enter" && modal?.save) { e.preventDefault(); modal.save(); } };
    autosize(ta, 27);
    return ta;
  }
  const noteHint = () => t("Optional: what it's for and when to use it, in any language. Only the library keeps it; no agent is given it");

  // refHint: how a value is read from a variable (#1250)
  const refHint = () => t("A value can name a variable, as {ref}: each agent is given it in its own way, so the token stays out of its settings", { ref: "${NAME}" });

  // the start of mcpauth.ErrNoRegistration
  const NO_REGISTRATION = "the server's sign-in doesn't let an app register itself";
  // a server's own OAuth app, to tell a changed one: "" for none
  const appOf = (x) => (x?.transport === "http" && x.oauth?.clientId ? JSON.stringify([x.oauth.clientId, x.oauth.clientSecret || "", x.oauth.callbackPort || 0]) : "");

  function editServer(s, prefill) {
    const all = mcpAgents();
    const d = prefill || (s ? structuredClone(s) : { name: "", transport: "stdio", command: "", args: [], env: {}, url: "", headers: {}, agents: all.map((a) => a.id) });
    if (!d.env) d.env = {};
    if (!d.headers) d.headers = {};
    d.local = !!d.local;
    d.note = d.note || "";
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead lib-serverhead");
    head.append(mark(s?.icon, d.transport === "stdio" ? GLYPH.cmd : GLYPH.web), el("b", "", s ? s.name : t("New MCP server")));
    if (!s) head.append(el("span", "note", t("or paste its JSON anywhere here")));
    // the form, or the server as JSON (hema_17384 on Discord), in Claude
    // Code's .mcp.json shape, as Copy config gives it: read whole, edited,
    // pasted over. The note, the agents and sync are the library's own and
    // stay in the form below it
    let view = "form", jsonBad = "";
    const viewBox = el("div", "lib-viewpick");
    const viewSegs = () => segs([["form", t("Form")], ["json", "JSON"]], view, (v) => {
      if (v === "form" && jsonBad) { err.textContent = jsonBad; viewBox.replaceChildren(viewSegs()); return; }
      view = v;
      err.textContent = "";
      draw();
      (view === "json" ? top.querySelector(".lib-json") : name).focus();
    });
    viewBox.append(viewSegs());
    head.append(viewBox);
    ed.append(head);
    const err = el("div", "editor-error");

    let signBox = null; // the sign-in's, for its button to say it saves
    // a sign-in saves the form first only to sign in to another server than
    // the saved one; see ready
    ready.saves = () => !s || d.name !== s.name || d.transport !== s.transport || d.url !== (s.url || "") || appOf(d) !== appOf(s);
    // a server whose sign-in doesn't let magpie register itself (GitHub's,
    // 新v学员 on Discord): the fields for the user's own app open
    ready.failed = (msg) => {
      if (!msg.startsWith(NO_REGISTRATION) || !appBox) return;
      appOpen = true;
      drawApp();
      appBox.querySelector("input")?.focus();
    };
    let appOpen = !!d.oauth?.clientId, appBox = null, appHint = null;
    // the user's own OAuth app, by Claude Code's names (--client-id,
    // --client-secret, --callback-port)
    function drawApp() {
      appBox.replaceChildren();
      const hint = appHint;
      if (!appOpen) {
        appBox.append(button(t("Use your own OAuth app"), "lib-addpair", () => { appOpen = true; drawApp(); appBox.querySelector("input")?.focus(); }));
        if (hint) hint.textContent = t("For a server whose sign-in doesn't let magpie register itself, as GitHub's");
        return;
      }
      const a = () => (d.oauth ||= { clientId: "", clientSecret: "", callbackPort: 0 });
      const id = field2(d.oauth?.clientId, t("Client ID"), (v) => { a().clientId = v.trim(); signBox?.redraw(); });
      const secret = field2(d.oauth?.clientSecret, t("Client secret"), (v) => { a().clientSecret = v.trim(); signBox?.redraw(); });
      secret.type = "password";
      const port = field2(d.oauth?.callbackPort || "", t("Callback port"), (v) => {
        port.value = v.replace(/\D/g, "").slice(0, 5);
        a().callbackPort = Number(port.value) || 0;
        signBox?.redraw();
        if (hint) hint.textContent = callback();
      });
      port.inputMode = "numeric";
      port.classList.add("port");
      for (const [i, l] of [[id, t("Client ID")], [secret, t("Client secret")], [port, t("Callback port")]]) i.setAttribute("aria-label", l);
      const x = button("", "lib-x", () => { d.oauth = s?.oauth ? null : undefined; appOpen = false; drawApp(); signBox?.redraw(); });
      x.append(svg("M4.5 4.5l7 7M11.5 4.5l-7 7", 11, 1.6));
      x.title = t("Remove");
      appBox.append(id, secret, port, x);
      if (hint) hint.textContent = callback();
    }
    const callback = () => t("Made with its vendor (GitHub's under Settings › Developer settings › OAuth Apps), with {url} as its callback URL", { url: "http://localhost" + (d.oauth?.callbackPort ? ":" + d.oauth.callbackPort : "") + "/callback" });
    const name = field2(d.name, t("e.g. github"), (v) => { d.name = v.trim(); signBox?.redraw(); });
    const kindOf = () => segs([["stdio", t("Command")], ["http", "HTTP"], ["sse", "SSE"]], d.transport, (v) => { d.transport = v; draw(); });
    const kindBox = el("div");
    kindBox.append(kindOf());
    const nameF = field(t("Name"), name, t("Letters, digits, - and _ — what the agents call it")), kindF = field(t("Runs as"), kindBox);
    const top = el("div", "lib-slot");
    ed.append(top);
    const note = noteBox(d.note, t("e.g. The shop's database, read only"), (v) => { d.note = v; });
    ed.append(...field(t("Note"), note, noteHint()));
    const slot = el("div", "lib-slot");
    ed.append(slot);
    const agentsBox = el("div");
    ed.append(...field(t("Agents"), agentsBox));
    // kept to this computer: a sync neither carries it to the others nor
    // takes it away here (huhuy72219405 on X)
    const hereBox = el("div", "lib-localbox");
    // said as sync, on while it syncs: "This computer only" read as a
    // sharing switch (bin.90 on Discord). local keeps its meaning: on is
    // kept here, so the switch shows and saves its opposite.
    const drawHere = () => hereBox.replaceChildren(toggle(!d.local, t("Sync to your other computers"), (on) => { d.local = !on; drawHere(); }));
    drawHere();
    // a Remote magpie's server holds this computer's key for it and goes
    // with it, so it is never synced: the switch says so, off and held,
    // where it showed on and went back off once saved (treeman_26897)
    const bridged = !!d.bridge;
    if (bridged) {
      const sw = hereBox.firstChild;
      sw.setAttribute("aria-disabled", "true");
      sw.onclick = (e) => e.stopPropagation();
    }
    ed.append(...field(t("Sync to your other computers"), hereBox, bridged ? bridgedWhy() : t("Off keeps it on this computer: sync never carries it to your other computers, and a sync from them neither removes it nor puts theirs in its place")));
    // with the hub on, a server can stay each agent's own entry (#4382,
    // cuipengcx90): one that reads its project from the folder it starts
    // in, or keeps a session's state. Shown while the hub is on, and for a
    // server the check found won't start in home, where magpie runs it for
    // Codex too (inHome)
    d.hub = d.hub === "direct" ? "direct" : "";
    if (lib.mcpHub || (s && health.get(s.name)?.h?.why === "home")) {
      const hubBox = el("div", "lib-hubbox");
      const drawHub = () => hubBox.replaceChildren(toggle(d.hub !== "direct", t("Through the MCP router"), (on) => { d.hub = on ? "" : "direct"; drawHub(); }));
      drawHub();
      ed.append(...field(t("Through the MCP router"), hubBox, t("Off gives each agent its own entry for it, as with the router off: for a server that reads its project from the folder it starts in, or keeps a session's state")));
    }
    ed.append(err);

    function drawAgents() {
      agentsBox.replaceChildren(agentChips(all, d.agents, (next) => { d.agents = next; drawAgents(); }, { names: true, blocked: sseBlocked(d), all: true }));
    }
    // the server as Copy config's Claude Code entry has it (fmtClaude)
    function jsonOf() {
      const e = d.transport === "stdio"
        ? { type: "stdio", command: d.command || "", args: d.args || [], env: d.env || {} }
        : { type: d.transport, url: d.url || "", ...(Object.keys(d.headers || {}).length ? { headers: d.headers } : {}) };
      return JSON.stringify({ mcpServers: { [d.name]: e } }, null, 2);
    }
    // what is typed is read as a paste is (serversIn): each valid edit is
    // the form's at once; one that isn't keeps the dialog from saving or
    // going back to the form until it is
    function jsonBox() {
      const ta = el("textarea", "lib-text small mono lib-json");
      ta.value = jsonOf();
      ta.spellcheck = false;
      ta.setAttribute("autocapitalize", "off");
      ta.setAttribute("autocorrect", "off");
      ta.oninput = () => {
        const found = serversIn(ta.value);
        jsonBad = !found?.length ? t("This isn't an MCP server's JSON") : found.length > 1 ? t("Only one server here; paste several outside this box to add them") : "";
        ta.setAttribute("aria-invalid", String(!!jsonBad));
        err.textContent = jsonBad;
        if (jsonBad) return;
        const f = found[0];
        Object.assign(d, { transport: f.transport, command: f.command || "", args: f.args || [], env: f.env || {}, url: f.url || "", headers: f.headers || {} });
        if (f.name) d.name = f.name;
        name.value = d.name;
        kindBox.replaceChildren(kindOf());
        drawAgents();
      };
      // a newline is the box's; ⌘/Ctrl-Enter saves, Escape closes
      ta.onkeydown = (e) => {
        e.stopPropagation();
        if (e.isComposing || e.keyCode === 229) return;
        if (e.key === "Escape") closeLibModal();
        else if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && modal?.save) { e.preventDefault(); modal.save(); }
      };
      autosize(ta, 160);
      return ta;
    }
    function draw() {
      slot.replaceChildren();
      if (view === "json") {
        signBox = appBox = null;
        top.replaceChildren(...field("JSON", jsonBox(), t("As Claude Code's .mcp.json has it. A value naming a variable, as {ref}, stays a name here", { ref: "${NAME}" })));
        head.firstChild.replaceWith(mark(s && d.transport === s.transport ? s.icon : "", d.transport === "stdio" ? GLYPH.cmd : GLYPH.web));
        drawAgents();
        return;
      }
      top.replaceChildren(...nameF);
      slot.append(...kindF);
      const g = el("div", "lib-grid");
      if (d.transport === "stdio") {
        const line = lineBox([d.command, ...(d.args || [])].filter((x, i) => i > 0 || x).map(quote).join(" "), "npx -y @modelcontextprotocol/server-github", (v) => { const w = words(v); d.command = w[0] || ""; d.args = w.slice(1); });
        // a synced server can run its own command here (#3836, #6426;
        // jinpwng on X: one server on a Mac and in WSL, each with its own
        // path). Its name, agents and note stay the one server; d.shared
        // is what the other computers run, the line this computer's
        const cmdBox = el("div", "lib-cmdbox");
        cmdBox.append(line);
        const synced = d.shared ? [d.shared.command, ...(d.shared.args || [])].map(quote).join(" ") : "";
        if (s && s.transport === "stdio" && !d.local && !s.plugin) {
          cmdBox.append(d.shared
            ? button(t("Use the synced command"), "lib-addpair", () => { Object.assign(d, { command: d.shared.command, args: [...(d.shared.args || [])], env: { ...(d.shared.env || {}) } }); d.shared = null; draw(); })
            : button(t("Use another command on this computer"), "lib-addpair", () => {
              d.shared = { command: s.shared?.command || s.command, args: [...((s.shared || s).args || [])], env: { ...((s.shared || s).env || {}) } };
              draw();
              slot.querySelector(".lib-cmd")?.focus();
            }));
        }
        g.append(...field(t("Command"), cmdBox, d.shared ? t("This computer's own, with its environment below. Your other computers run {cmd}", { cmd: synced }) : t("The command and its arguments, as you'd type them")));
        g.append(...field(t("Environment"), pairs(d.env, "GITHUB_TOKEN", t("value"), (v) => { d.env = v; drawAgents(); }), refHint()));
      } else {
        const url = field2(d.url, "https://example.com/mcp", (v) => { d.url = v.trim(); signBox?.redraw(); });
        url.classList.add("mono");
        g.append(...field("URL", url));
        g.append(...field(t("Headers"), pairs(d.headers, "Authorization", "Bearer …", (v) => { d.headers = v; drawAgents(); }), refHint()));
        if (s && s.transport === d.transport && s.url) g.append(...field(t("Accounts"), headerAccounts(s.name)));
        // Sign in saves the form first when it is of another server than
        // the saved one, so a URL just changed (or a server just added) is
        // the one signed in to
        signBox = d.transport === "http" ? signInBox(s ? s.name : "", ready) : null;
        if (signBox) {
          appBox = el("div", "lib-app");
          const f = field(t("OAuth app"), appBox, " ");
          appHint = f[1].querySelector(".hint");
          g.append(...f);
          g.append(...field(t("Sign-in"), signBox, t("For a server that asks you to sign in (OAuth): magpie signs in once, and every agent given it uses that sign-in")));
        } else appBox = null;
      }
      // how long the agents that take one (Codex's startup_timeout_sec)
      // wait for it to start, shown only when one of them is here (#3168)
      const slow = [...new Set(all.filter((a) => a.startup).map((a) => a.name))];
      if (slow.length) {
        const wait = field2(d.startupTimeout || "", "30", (v) => {
          wait.value = v.replace(/\D/g, "").slice(0, 5);
          d.startupTimeout = Number(wait.value) || 0;
        });
        wait.inputMode = "numeric";
        wait.classList.add("lib-startup");
        wait.setAttribute("aria-label", t("Startup timeout"));
        g.append(...field(t("Startup timeout"), wait, t("Seconds {agents} waits for it to start and list its tools. Empty leaves {agents}'s own 30; a value you wrote in its config.toml by hand is kept", { agents: slow.join(", ") })));
      }
      slot.append(g);
      if (appBox) drawApp();
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
    if (s && projectMCPAgents().length) bar.append(copyConfigButton([s.name]));
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal));
    // send saves the form; s is then the server as saved, for a save after
    // a sign-in's to be of it
    const send = async () => {
      const body = { old: s ? s.name : "", server: { name: d.name, transport: d.transport, agents: d.agents, startupTimeout: d.startupTimeout || 0, local: !!d.local, hub: d.hub || "", note: d.note || "" } };
      if (d.transport === "stdio") Object.assign(body.server, { command: d.command, args: d.args, env: d.env, shared: d.local ? null : d.shared || null });
      else Object.assign(body.server, { url: d.url, headers: d.headers, oauth: d.transport === "http" && d.oauth?.clientId ? d.oauth : null });
      take(await api("library/servers/save", body));
      const was = s;
      s = lib.servers.find((x) => x.name === d.name) || s;
      return was;
    };
    const save = async () => {
      if (jsonBad) { err.textContent = jsonBad; return; }
      err.textContent = "";
      try {
        const was = await send();
        report(lib.result, was ? t("{name} saved", { name: d.name }) : "");
        closeLibModal(true);
        render();
      } catch (e) { err.textContent = e.message; }
    };
    // before a sign-in: the form saved, the dialog left open, only when
    // the sign-in is to another server than the saved one (a new one, a
    // name or URL changed). It never saves the rest of the form on its
    // own: a header removed, then Sign in, then Cancel left the header
    // removed (新v学员 on Discord). A save says so on the button ("Save and
    // sign in"), and what it saved is what Cancel then keeps
    async function ready() {
      if (!ready.saves()) return s.name;
      err.textContent = "";
      const was = await send();
      report(lib.result, was ? t("{name} saved", { name: d.name }) : "");
      ok.textContent = t("Save");
      original = JSON.stringify(d);
      render();
      return s.name;
    }
    const ok = button(s ? t("Save") : t("Add"), "primary", save);
    bar.append(ok);
    ed.append(bar);

    // a README's JSON pasted anywhere in the dialog fills it in
    ed.addEventListener("paste", (e) => {
      if (e.target.closest?.(".lib-json")) return; // the JSON box's own
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
    let original = JSON.stringify(d);
    modal = { save, dirty: () => !!jsonBad || JSON.stringify(d) !== original };
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
    const drawAgents = () => agentsBox.replaceChildren(agentChips(all, who, (n) => { who = n; drawAgents(); }, { names: true, all: true }));
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

  // ---------- variables ----------

  // An environment variable the agent's own settings give every run of it
  // (a proxy, an API base URL): Claude Code's settings.json "env", Codex's
  // shell_environment_policy.set. It is kept once and written into each
  // agent picked (env.go); a value the user set in the file is left alone.
  const envAgentOf = (id) => (lib.envAgents || []).find((x) => x.id === id);
  const envAgents = () => shownAgents().filter((a) => envAgentOf(a.id));
  const envBlocked = () => (a) => {
    const ea = envAgentOf(a.id);
    if (ea && !ea.no) return "";
    return ea?.wsl ? t("magpie gives variables only to agents on this computer, not yet to {agent} in WSL", { agent: a.name }) : t("{agent} has no environment settings magpie can write", { agent: a.name });
  };
  function renderEnv(body) {
    body.append(intro(t("An environment variable is set for an agent every time it runs: a proxy, an API base URL. Add it once and magpie writes it into each agent you pick, in that agent's own settings.")));
    const all = envAgents();
    const vars = lib.env || [];
    if (!vars.length) {
      body.append(empty(t("No environment variables in the library yet"), t("Add one, and pick the agents that get it."), button(t("＋ Add an environment variable"), "action", () => editEnv(null))));
      return;
    }
    const list = el("div", "list lib-list");
    list.append(...[...vars].sort((a, b) => a.name.localeCompare(b.name)).map((v) => envRow(v, all)));
    body.append(list);
    const after = el("div", "after-list");
    after.append(button(t("＋ Add environment variable"), "", () => editEnv(null)));
    body.append(after);
  }
  function envRow(v, all) {
    const row = el("div", "row lib-row click lib-env");
    const who = el("div", "who");
    who.append(el("div", "name mono", v.name));
    const val = el("div", "sub mono lib-envval", v.value);
    val.title = v.value;
    who.append(val);
    row.append(glyph(GLYPH.hook), who, ...removeAct(() => confirmRemoveEnv(v)),
      agentChips(withGone(all, v.agents), v.agents, chipsChange("env/agents", v.name, "env", (x) => envRow(x, all)), { problems: v.problems, blocked: envBlocked(), all: true }));
    row.onclick = () => editEnv(v);
    row.title = t("Edit {name}", { name: v.name });
    return row;
  }
  function editEnv(v) {
    const all = envAgents();
    const d = v ? { name: v.name, value: v.value, agents: [...(v.agents || [])] } : { name: "", value: "", agents: all.filter((a) => !envBlocked()(a)).map((a) => a.id) };
    const ed = el("div", "editor lib-editor lib-envedit");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.hook), el("b", "", v ? v.name : t("New environment variable")));
    ed.append(head);
    const err = el("div", "editor-error");
    const name = field2(d.name, t("e.g. HTTPS_PROXY"), (x) => { d.name = x.trim(); });
    name.classList.add("mono");
    ed.append(...field(t("Name"), name, t("Letters, digits and _")));
    const val = field2(d.value, t("e.g. http://127.0.0.1:7890"), (x) => { d.value = x; });
    val.classList.add("mono");
    ed.append(...field(t("Value"), val));
    const agentsBox = el("div");
    ed.append(...field(t("Agents"), agentsBox), err);
    const drawAgents = () => agentsBox.replaceChildren(agentChips(all, d.agents, (next) => { d.agents = next; drawAgents(); }, { names: true, blocked: envBlocked(), all: true }));
    drawAgents();
    const bar = el("div", "bar");
    if (v) bar.append(button(t("Remove from the library"), "danger", async () => {
      if (!await confirmRemoval(v.name, "It will be removed from the library and the agents it was given to.")) return;
      if (await change("env/remove", { name: v.name }, t("{name} is out of the library and the agents it was given to", { name: v.name }))) closeLibModal(true);
    }));
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal));
    const save = async () => {
      err.textContent = "";
      try {
        const env = { name: d.name, value: d.value, agents: d.agents.filter((id) => !envBlocked()(agentOf(id) || { id })) };
        take(await api("library/env/save", { old: v ? v.name : "", env }));
        report(lib.result, v ? t("{name} saved", { name: d.name }) : "");
        closeLibModal(true);
        render();
      } catch (e) { err.textContent = e.message; }
    };
    bar.append(button(v ? t("Save") : t("Add"), "primary", save));
    ed.append(bar);
    const original = JSON.stringify(d);
    modal = { save, dirty: () => JSON.stringify(d) !== original };
    openLib(ed);
    if (!v) requestAnimationFrame(() => name.focus());
  }

  // ---------- hooks ----------

  // A hook (#1790, AllenReder) is a command an agent runs at a point of
  // its turn: a phone notification when it ends, a formatter after it
  // edits a file. It is kept once and written into each agent picked, in
  // that agent's own file and shape (hooks.go). An event is one every
  // agent has, under its own name, or one agent's own, for it alone.
  const HOOK_EVENTS = () => [
    ["turn-end", t("When a turn ends")],
    ["after-tool", t("After a tool runs")],
    ["before-tool", t("Before a tool runs")],
    ["prompt", t("When a prompt is sent")],
    ["session-start", t("When a session starts")],
  ];
  const HOOK_TOOLS = () => [["", t("Any tool")], ["shell", t("Shell commands")], ["edit", t("File edits")]];
  // the starts a session-start hook can be narrowed to (#5604, NEKO-CwC),
  // in Claude Code's words for them, which library.HookStarts keeps
  const HOOK_STARTS = () => [
    ["", t("Any start")],
    ["startup", t("A fresh session")],
    ["resume", t("A resumed session")],
    ["clear", t("After /clear")],
    ["compact", t("After the context is compacted")],
    ["fork", t("A forked session")],
    ["startup|resume|clear|compact", t("Any start but a fork")],
  ];
  const STARTS = ["startup", "resume", "clear", "compact", "fork"];
  // a matcher of starts in library.HookStarts' order, as each agent's
  // view lists the ones it takes
  const startsOf = (m) => STARTS.filter((s) => m.split("|").includes(s)).join("|");
  const toolEvent = (ev) => ev === "after-tool" || ev === "before-tool";
  // the choices an event can be narrowed by, or null
  const narrowsOf = (ev) => toolEvent(ev) ? HOOK_TOOLS() : ev === "session-start" ? HOOK_STARTS() : null;
  const hookAgentOf = (id) => (lib.hookAgents || []).find((x) => x.id === id);
  // the agents the tab shows a chip for: every one shown, those magpie
  // can't give a hook greyed, saying why
  const hookAgents = () => shownAgents().filter((a) => hookAgentOf(a.id));
  // why a can't be given h, or ""
  const hookBlocked = (h) => (a) => {
    const ha = hookAgentOf(a.id);
    if (!ha || ha.no) return ha?.wsl ? t("magpie gives hooks only to agents on this computer, not yet to {agent} in WSL", { agent: a.name }) : t("{agent} has no hooks magpie can write", { agent: a.name });
    const own = h.event?.includes(":") && h.event.split(":")[0];
    if (own && own !== a.id) return t("{event} is {agent}'s own event: only it can run this hook", { event: h.event.slice(own.length + 1), agent: nameOf(own) });
    // narrowed to starts this agent can't single out: never widened to
    // every start, nor written to match none (hooks.go, startMatcher)
    if (h.event === "session-start" && h.matcher && !(ha.starts || []).includes(startsOf(h.matcher))) {
      if (!ha.startSources?.length) return t("{agent} runs a session-start hook at every start: it can't be narrowed", { agent: a.name });
      if (!h.matcher.split("|").some((s) => ha.startSources.includes(s))) return t("{agent} never starts a session that way", { agent: a.name });
      return t("{agent} can't tell those starts apart from its others", { agent: a.name });
    }
    return "";
  };
  function hookEventLabel(h) {
    const i = h.event.indexOf(":");
    if (i > 0) return nameOf(h.event.slice(0, i)) + " · " + h.event.slice(i + 1);
    return HOOK_EVENTS().find(([v]) => v === h.event)?.[1] || h.event;
  }
  function hookLine(h) {
    let s = hookEventLabel(h);
    if (h.matcher) s += " · " + (h.event.includes(":") ? h.matcher : (narrowsOf(h.event) || []).find(([v]) => v === h.matcher)?.[1] || h.matcher);
    return s;
  }

  function renderHooks(body) {
    body.append(intro(t("A hook is a command an agent runs at a point of its turn — a notification when it ends, a formatter after it edits a file. Add it once and magpie writes it into each agent you pick, in the shape that agent reads.")));
    const all = hookAgents();
    const hooks = lib.hooks || [];
    pickingFor("hooks", hooks);
    if (!hooks.length) {
      body.append(empty(t("No hooks in the library yet"), t("Add one, and pick the agents that run it."), button(t("＋ Add a hook"), "action", () => editHook(null))));
    } else {
      if (hooks.length > 1) {
        const rh = el("div", "row-head lib-hookhead");
        rh.append(el("span", "label", t("In the library")), el("span", "grow"));
        if (all.length) rh.append(pickButton("hooks"));
        rh.append(removeEveryButton("hooks"));
        body.append(rh);
      }
      const list = el("div", "list lib-list");
      list.append(...[...hooks].sort((a, b) => a.name.localeCompare(b.name)).map((h) => hookRow(h, all)));
      body.append(list);
      if (picking) body.append(pickBar(all));
      const after = el("div", "after-list");
      after.append(button(t("＋ Add hook"), "", () => editHook(null)));
      body.append(after);
    }
    const given = (id) => hooks.some((h) => h.agents?.includes(id));
    for (const a of all) {
      const ha = hookAgentOf(a.id);
      if (ha.off && given(a.id)) body.append(el("p", "lib-aside lib-hooks-off", t("{agent} has hooks turned off in its settings: it runs none of them until they're turned on there.", { agent: a.name })));
    }
    // Codex runs a hook only once it is trusted there, again after it
    // changes (codex-rs/hooks: trusted_hash)
    if (given("codex")) body.append(el("p", "lib-aside lib-hooks-codex", t("Codex runs a new or changed hook only once you trust it: review it in Codex's /hooks.")));
    // Cursor runs Claude Code's hooks as well as its own
    if (hooks.some((h) => h.agents?.includes("cursor") && h.agents?.includes("claude"))) body.append(el("p", "lib-aside lib-hooks-twice", t("Cursor runs Claude Code's hooks too: a hook given to both runs twice in Cursor.")));
    // the agents greyed: Claude Code, Codex, Gemini CLI and Cursor are the
    // ones whose hooks files magpie knows
    const skip = all.filter((a) => hookAgentOf(a.id).no);
    if (skip.length) body.append(el("p", "lib-aside lib-hooks-no", t("magpie can't give hooks to {agents} yet: their chips are greyed, and say why.", { agents: skip.map((a) => a.name).join(", ") })));
  }

  function hookRow(h, all) {
    const row = el("div", "row lib-row click lib-hook");
    const who = el("div", "who");
    who.append(el("div", "name mono", h.name));
    // what it is for, in the user's own words (#2011)
    if (h.description) who.append(el("div", "lib-hookdesc", h.description));
    who.append(el("div", "sub lib-hookev", hookLine(h)));
    const cmd = el("div", "sub mono lib-hookcmd", h.command);
    cmd.title = h.command;
    who.append(cmd);
    row.append(glyph(GLYPH.hook), who, ...removeAct(() => confirmRemoveHook(h)),
      agentChips(withGone(all, h.agents), h.agents, chipsChange("hooks/agents", h.name, "hooks", (x) => hookRow(x, all)), { problems: h.problems, blocked: hookBlocked(h), all: true }));
    if (picking) return pickRow(row, h.name);
    row.onclick = () => editHook(h);
    row.title = t("Edit {name}", { name: h.name });
    return row;
  }

  function editHook(h) {
    const all = hookAgents();
    const d = h ? { name: h.name, description: h.description || "", event: h.event, matcher: h.matcher || "", command: h.command, timeout: h.timeout || 0, agents: [...(h.agents || [])] }
      : { name: "", description: "", event: "turn-end", matcher: "", command: "", timeout: 0, agents: [] };
    if (!h) d.agents = all.filter((a) => !hookBlocked(d)(a)).map((a) => a.id);
    const ed = el("div", "editor lib-editor lib-hookedit");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.hook), el("b", "", h ? h.name : t("New hook")));
    ed.append(head);
    const err = el("div", "editor-error");
    const name = field2(d.name, t("e.g. notify"), (v) => { d.name = v.trim(); });
    ed.append(...field(t("Name"), name, t("Letters, digits, - and _")));
    // a box that wraps, so a long description is read whole while it is
    // written (#2085). It is kept as one paragraph (library.SaveHook joins
    // its lines), so Enter still saves, as in every other field here
    const desc = el("textarea", "lib-text small lib-hookdescin");
    desc.value = d.description;
    desc.placeholder = t("e.g. Tell me when a turn ends");
    desc.spellcheck = false;
    desc.rows = 3;
    desc.maxLength = 200;
    desc.oninput = () => { d.description = desc.value; };
    desc.onkeydown = (e) => { e.stopPropagation(); if (e.isComposing || e.keyCode === 229) return; if (e.key === "Escape") closeLibModal(); else if (e.key === "Enter" && modal?.save) { e.preventDefault(); modal.save(); } };
    autosize(desc, 64);
    ed.append(...field(t("Description"), desc, t("Optional: what it's for, in any language. Only the library keeps it")));

    // the event and the tools it is narrowed to: menus, never a <select>
    const evBtn = el("button", "sess-pick lib-menu lib-hookevent");
    evBtn.type = "button";
    evBtn.setAttribute("aria-haspopup", "menu");
    evBtn.setAttribute("aria-expanded", "false");
    evBtn.onclick = (e) => {
      e.stopPropagation();
      const opts = HOOK_EVENTS().map(([v, n]) => ({ v, name: n, literalName: true, note: "every agent, in its own name for it", always: true }));
      // an agent's own events, for it alone
      for (const a of all) {
        const ha = hookAgentOf(a.id);
        if (ha.no) continue;
        for (const ev of ha.events || []) opts.push({ v: a.id + ":" + ev, name: a.name + " · " + ev, literalName: true, note: t("{agent}'s own event", { agent: a.name }) });
      }
      openProtoMenu(evBtn, opts, d.event, (v) => {
        d.event = v;
        if (v.includes(":")) d.agents = [v.split(":")[0]];
        // a tool kept from one tool event to the other; nothing else
        if (v.includes(":") || !(narrowsOf(v) || []).some(([m]) => m === d.matcher)) d.matcher = "";
        draw();
      }, "When it runs", "", "left", false, "Find an event");
    };
    ed.append(...field(t("When it runs"), evBtn));
    const slot = el("div", "lib-slot");
    ed.append(slot);
    const cmd = field2(d.command, "curl -s https://api.day.app/KEY/done", (v) => { d.command = v; });
    cmd.classList.add("mono");
    ed.append(...field(t("Command"), cmd, t("Run by the agent's shell, in the session's folder; the agent passes what happened as JSON on its input")));
    const to = field2(d.timeout ? String(d.timeout) : "", t("the agent's own"), (v) => { d.timeout = Math.max(0, parseInt(v, 10) || 0); });
    to.inputMode = "numeric";
    to.classList.add("lib-hooktimeout");
    ed.append(...field(t("Timeout (seconds)"), to));
    const agentsBox = el("div");
    ed.append(...field(t("Agents"), agentsBox));
    ed.append(err);

    function drawAgents() {
      agentsBox.replaceChildren(agentChips(all, d.agents, (next) => { d.agents = next; drawAgents(); }, { names: true, blocked: hookBlocked(d), all: true }));
    }
    function draw() {
      evBtn.replaceChildren(el("span", "", hookEventLabel(d)), svg(CHEV, 11, 1.6));
      slot.replaceChildren();
      if (d.event.includes(":")) {
        const m = field2(d.matcher, t("every tool"), (v) => { d.matcher = v.trim(); });
        m.classList.add("mono", "lib-hookmatcher");
        slot.append(...field(t("Matcher"), m, t("The agent's own matcher, as its hooks file has it; empty for all")));
      } else if (narrowsOf(d.event)) {
        const tools = toolEvent(d.event);
        const opts = narrowsOf(d.event);
        const mb = el("button", "sess-pick lib-menu " + (tools ? "lib-hooktool" : "lib-hookstart"));
        mb.type = "button";
        mb.setAttribute("aria-haspopup", "menu");
        mb.setAttribute("aria-expanded", "false");
        // one written by hand into library.json shows as it is
        mb.append(el("span", "", opts.find(([v]) => v === d.matcher)?.[1] || d.matcher), svg(CHEV, 11, 1.6));
        const title = tools ? "For which tools" : "At which starts";
        mb.onclick = (e) => {
          e.stopPropagation();
          // an agent that can't run it at just those greys, saying why,
          // and is left out when it is saved
          openProtoMenu(mb, opts.map(([v, n]) => ({ v, name: n, literalName: true, note: "" })), d.matcher, (v) => { d.matcher = v; draw(); }, title);
        };
        slot.append(...field(t(title), mb, t("Each agent's own names for them")));
      }
      drawAgents();
    }
    draw();

    const bar = el("div", "bar");
    if (h) bar.append(button(t("Remove from the library"), "danger", async () => {
      if (!await confirmRemoval(h.name, "It will be removed from the library and the agents it was given to.")) return;
      if (await change("hooks/remove", { name: h.name }, t("{name} is out of the library and the agents it was given to", { name: h.name }))) closeLibModal(true);
    }));
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal));
    const save = async () => {
      err.textContent = "";
      try {
        const hook = { name: d.name, description: d.description.trim(), event: d.event, matcher: d.matcher, command: d.command, timeout: d.timeout, agents: d.agents.filter((id) => !hookBlocked(d)(agentOf(id) || { id })) };
        take(await api("library/hooks/save", { old: h ? h.name : "", hook }));
        report(lib.result, h ? t("{name} saved", { name: d.name }) : "");
        closeLibModal(true);
        render();
      } catch (e) { err.textContent = e.message; }
    };
    bar.append(button(h ? t("Save") : t("Add"), "primary", save));
    ed.append(bar);
    const original = JSON.stringify(d);
    modal = { save, dirty: () => JSON.stringify(d) !== original };
    openLib(ed);
    if (!h) requestAnimationFrame(() => name.focus());
  }

  function confirmRemoveEveryHook(some) {
    const hooks = (some || lib.hooks || []).slice(), names = hooks.map((x) => x.name), n = names.length;
    confirmRemoveEvery("hooks", names, !some ? t("Remove all {n} hooks?", { n }) : n === 1 ? t("Remove {name}?", { name: names[0] }) : t("Remove {n} hooks?", { n }), [
      takenFrom(hooks),
      t("Hooks your agents have that aren't in the library stay as they are, and so does one you changed in an agent's file."),
    ], !!some);
  }

  // ---------- subagents ----------

  // A subagent (#2283, chuanchinatest) is a helper with a prompt of its
  // own that an agent hands a task to. It is kept once and written into
  // each agent picked, as that agent's own file in its own folder and
  // shape (subagents.go). Its skills and MCP servers go only into the
  // files that can name them: skills into Claude Code's, servers into
  // Claude Code's and Droid's. Every other agent gets its prompt alone.
  // Its model goes only into Codex's role file (luckymanjc on Discord: a
  // routing group named for a subagent, which ran on its lead's model).
  const subagentAgentOf = (id) => (lib.subagentAgents || []).find((x) => x.id === id);
  const subagentAgents = () => shownAgents().filter((a) => subagentAgentOf(a.id));
  // why a can't be given a subagent, or ""
  const subagentBlocked = (a) => {
    const sa = subagentAgentOf(a.id);
    if (!sa || sa.no) return sa?.wsl ? t("magpie gives subagents only to agents on this computer, not yet to {agent} in WSL", { agent: a.name }) : t("{agent} reads no subagents magpie can write", { agent: a.name });
    return "";
  };
  // the agents of all that take what s binds, by kind: who can't bind them
  const unbound = (all, ids, kind) => all.filter((a) => ids.includes(a.id) && !subagentBlocked(a) && !subagentAgentOf(a.id)[kind]);
  // the name of a model a subagent may run on, its id when it is no longer one
  const subagentModelName = (id) => (lib.subagentModels || []).find((m) => m.id === id)?.name || id;
  function subagentLine(s) {
    const parts = [];
    if (s.model) parts.push(t("Model: {name}", { name: subagentModelName(s.model) }));
    if (s.skills?.length) parts.push(t("Skills: {names}", { names: s.skills.join(", ") }));
    if (s.mcp?.length) parts.push(t("MCP servers: {names}", { names: s.mcp.join(", ") }));
    return parts.join(" · ");
  }

  function renderSubagents(body) {
    body.append(intro(t("A subagent is a helper with a prompt of its own that an agent hands a task to — a reviewer, a test writer. Add it once and magpie writes it into each agent you pick, as that agent's own subagent file.")));
    const all = subagentAgents();
    const subs = lib.subagents || [];
    pickingFor("subagents", []);
    body.append(subagentFind());
    if (!subs.length) {
      body.append(empty(t("No subagents in the library yet"), t("Add one, and pick the agents that can hand it a task."), button(t("＋ Add a subagent"), "action", () => editSubagent(null))));
    } else {
      const list = el("div", "list lib-list");
      list.append(...[...subs].sort((a, b) => a.name.localeCompare(b.name)).map((s) => subagentRow(s, all)));
      body.append(list);
      const after = el("div", "after-list");
      after.append(button(t("＋ Add subagent"), "", () => editSubagent(null)));
      body.append(after);
    }
    // what each agent does with what a subagent binds: said only when one
    // binds something that an agent it is given to can't
    const given = (kind) => [...new Set(subs.filter((s) => s[kind]?.length).flatMap((s) => unbound(all, s.agents || [], kind)))];
    const noSkills = given("skills"), noMCP = given("mcp");
    const noModel = [...new Set(subs.filter((s) => s.model).flatMap((s) => unbound(all, s.agents || [], "model")))];
    if (noSkills.length) body.append(el("p", "lib-aside lib-subagents-noskills", t("{agents} can't bind skills to a subagent: theirs gets its prompt, and every skill the agent has.", { agents: noSkills.map((a) => a.name).join(", ") })));
    if (noMCP.length) body.append(el("p", "lib-aside lib-subagents-nomcp", t("{agents} can't bind MCP servers to a subagent: theirs gets its prompt, and every server the agent has.", { agents: noMCP.map((a) => a.name).join(", ") })));
    if (noModel.length) body.append(el("p", "lib-aside lib-subagents-nomodel", t("{agents} can't be given a subagent's model: theirs runs on the model the agent picks.", { agents: noModel.map((a) => a.name).join(", ") })));
    // Cursor's app reads ~/.cursor/agents; its cursor-agent CLI reads
    // only a project's .cursor/agents
    if (subs.some((s) => s.agents?.includes("cursor"))) body.append(el("p", "lib-aside lib-subagents-cursor", t("The Cursor app has these subagents; its cursor-agent command reads only a project's own .cursor/agents.")));
    const skip = all.filter((a) => subagentAgentOf(a.id).no);
    if (skip.length) body.append(el("p", "lib-aside lib-subagents-no", t("magpie can't give subagents to {agents} yet: their chips are greyed, and say why.", { agents: skip.map((a) => a.name).join(", ") })));
    if (!agentless() && (subs.length || lib.projects.length)) renderProjects(body, "subagents");
  }

  // subagents from a repository (CHENGJIAHEGOXIS on X): a link to one,
  // a folder or a file in one, or a folder here; each Markdown file with
  // a name and a description is one (library/subagent_import.go)
  let saProbe = null;    // { source, found, pick:Set, agents, filter } or { source, error }
  let saProbing = false;
  const saPickable = (x) => !x.state || x.state === "update";

  function subagentFind() {
    const card = el("div", "list lib-card lib-install lib-safind");
    const line = el("div", "lib-find");
    const inp = el("input");
    inp.type = "text";
    inp.dataset.lib = "sasource";
    inp.placeholder = t("owner/repo, a GitHub or GitLab link, any git repository's address, or a folder on this computer");
    inp.spellcheck = false;
    inp.autocomplete = "off";
    inp.value = saProbe?.source || "";
    const go = button(saProbing ? t("Looking…") : t("Find subagents"), "action", () => find());
    go.disabled = saProbing;
    if (saProbing) go.classList.add("busy");
    inp.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") find(); else if (e.key === "Escape" && saProbe) { saProbe = null; render(); } };
    line.append(glyph(GLYPH.search, "lib-mini"), inp, go);
    card.append(line);
    async function find() {
      const src = inp.value.trim();
      if (!src || saProbing) return;
      saProbing = true;
      saProbe = { source: src };
      render();
      try {
        const f = await api("library/subagents/probe", { source: src });
        const fresh = f.candidates.filter(saPickable);
        saProbe = { source: src, found: f, pick: new Set((fresh.length <= 3 ? fresh : []).map((c) => c.path)), agents: subagentAgents().filter((a) => !subagentBlocked(a)).map((a) => a.id), filter: "" };
      } catch (e) {
        saProbe = { source: src, error: e.message };
      }
      saProbing = false;
      render();
    }
    if (saProbe?.error) card.append(el("div", "lib-err", saProbe.error));
    if (saProbe?.found) card.append(subagentPicks(saProbe));
    return card;
  }

  function subagentPicks(p) {
    const box = el("div", "lib-probe");
    const c = p.found.candidates;
    const left = (p.found.left || []).map((x) => el("div", "lib-none lib-left", t("{path} left out: {error}", { path: x.path, error: x.error })));
    const head = el("div", "lib-probehead");
    const src = p.found.kind === "github" ? "github.com/" + p.found.source.replace(/^https?:\/\/(www\.)?github\.com\//, "")
      : p.found.kind === "git" ? p.found.source.replace(/^https:\/\//, "") : tilde(p.found.source);
    head.append(el("span", "note", c.length === 1 ? t("1 subagent in {src}", { src }) : t("{count} subagents in {src}", { count: c.length, src })));
    head.append(el("span", "grow"));
    const fresh = c.filter(saPickable);
    if (fresh.length > 1) {
      const allOn = fresh.every((x) => p.pick.has(x.path));
      head.append(button(allOn ? t("Select none") : t("Select all"), "", () => { for (const x of fresh) allOn ? p.pick.delete(x.path) : p.pick.add(x.path); render(); }));
    }
    box.append(head);
    if (c.length > 8) {
      const f = el("input", "lib-filter");
      f.type = "text";
      f.dataset.lib = "safilter";
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
        if (q && !(x.name + " " + x.description + " " + x.path).toLowerCase().includes(q)) continue;
        const r = el("label", "lib-pick lib-sapick" + (saPickable(x) ? "" : " dim"));
        const cb = el("input");
        cb.type = "checkbox";
        cb.checked = p.pick.has(x.path);
        cb.disabled = !saPickable(x);
        cb.onchange = () => { if (cb.checked) p.pick.add(x.path); else p.pick.delete(x.path); syncGo(); };
        const w = el("span", "who");
        w.append(el("span", "name mono", x.as || x.name), el("span", "sub", x.description));
        r.append(cb, w);
        if (x.state === "have") r.append(tag(t("in the library"), ""));
        else if (x.state === "update") r.append(tag(t("Changed upstream"), "lib-as", t("Importing it again updates its description and prompt, and keeps its agents.")));
        else if (x.state === "edited") r.append(tag(t("Changed here"), "lib-as", t("You changed it here, so importing it again leaves it as it is.")));
        else if (x.as) r.append(tag(t("adds as {name}", { name: x.as }), "lib-as", t("The library has another subagent called {name}. This one goes in beside it as {as}.", { name: x.name, as: x.as })));
        list.append(r);
      }
    };
    drawList();
    box.append(list, ...left);
    if (!c.length) return box;
    const foot = el("div", "lib-probefoot");
    const agentsBox = el("div");
    const drawAgents = () => agentsBox.replaceChildren(agentChips(subagentAgents(), p.agents, (n) => { p.agents = n; drawAgents(); }, { names: false, all: true, blocked: subagentBlocked }));
    drawAgents();
    foot.append(el("span", "note", t("for")), agentsBox, el("span", "grow"));
    foot.append(button(t("Cancel"), "", () => { saProbe = null; render(); }));
    const go = button("", "primary", async () => {
      go.disabled = true;
      go.textContent = t("Importing…");
      if (await change("subagents/import", { source: p.source, paths: [...p.pick], agents: p.agents.filter((id) => !subagentBlocked(agentOf(id) || { id })) })) { saProbe = null; render(); }
      else { go.disabled = false; syncGo(); }
    });
    const syncGo = () => { const n = p.pick.size; go.textContent = n === 1 ? t("Import 1 subagent") : t("Import {count} subagents", { count: n }); go.disabled = !n; };
    syncGo();
    foot.append(go);
    box.append(foot);
    return box;
  }

  function subagentRow(s, all) {
    const row = el("div", "row lib-row click lib-subagent");
    const who = el("div", "who");
    who.append(el("div", "name mono", s.name));
    who.append(el("div", "lib-subagentdesc", s.description));
    const line = subagentLine(s);
    if (line) who.append(el("div", "sub lib-subagentuses", line));
    row.append(glyph(GLYPH.subagent), who,
      agentChips(all, s.agents, chipsChange("subagents/agents", s.name, "subagents", (x) => subagentRow(x, all)), { problems: s.problems, blocked: subagentBlocked, all: true }));
    row.onclick = () => editSubagent(s);
    row.title = t("Edit {name}", { name: s.name });
    return row;
  }

  // a menu of names to tick, for a subagent's skills or servers: the app
  // menu, never a <select>
  function namesPick(cls, names, d, key, head, find) {
    const b = el("button", "sess-pick lib-menu " + cls);
    b.type = "button";
    b.setAttribute("aria-haspopup", "menu");
    b.setAttribute("aria-expanded", "false");
    const draw = () => b.replaceChildren(el("span", "", d[key].length ? d[key].join(", ") : t("None")), svg(CHEV, 11, 1.6));
    b.onclick = (e) => {
      e.stopPropagation();
      const opts = [{ v: "", name: t("None"), literalName: true, note: "", always: true }, ...names.map((n) => ({ v: n, name: n, literalName: true, note: "" }))];
      openProtoMenu(b, opts, d[key], (v) => { d[key] = v; draw(); }, head, "", "left", true, names.length > 8 ? find : "");
    };
    draw();
    if (!names.length) b.disabled = true;
    return b;
  }

  // the model a subagent runs on, from the app menu: the agent's pick ("")
  // or one of the models and routing groups Codex lists, the one agent
  // whose subagent file takes a model
  function modelPick(d, changed) {
    const b = el("button", "sess-pick lib-menu lib-subagentmodel");
    b.type = "button";
    b.setAttribute("aria-haspopup", "menu");
    b.setAttribute("aria-expanded", "false");
    const draw = () => b.replaceChildren(el("span", "", d.model ? subagentModelName(d.model) : t("Picked by the agent")), svg(CHEV, 11, 1.6));
    b.onclick = (e) => {
      e.stopPropagation();
      const models = lib.subagentModels || [];
      const opts = [{ v: "", name: t("Picked by the agent"), literalName: true, note: "", always: true },
        ...models.map((m) => ({ v: m.id, name: m.name, literalName: true, note: m.group ? t("routing group") : m.note || "" }))];
      // a model set before that Codex no longer lists stays pickable
      if (d.model && !models.some((m) => m.id === d.model)) opts.push({ v: d.model, name: d.model, literalName: true, note: "" });
      openProtoMenu(b, opts, d.model, (v) => { d.model = v; draw(); changed(); }, "Model", "", "left", false, opts.length > 8 ? "Find a model…" : "");
    };
    draw();
    return b;
  }

  function editSubagent(s) {
    const all = subagentAgents();
    const d = s ? { name: s.name, description: s.description, prompt: s.prompt, model: s.model || "", skills: [...(s.skills || [])], mcp: [...(s.mcp || [])], agents: [...(s.agents || [])] }
      : { name: "", description: "", prompt: "", model: "", skills: [], mcp: [], agents: all.filter((a) => !subagentBlocked(a)).map((a) => a.id) };
    const ed = el("div", "editor lib-editor lib-subagentedit");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.subagent), el("b", "", s ? s.name : t("New subagent")));
    ed.append(head);
    const err = el("div", "editor-error");
    const name = field2(d.name, t("e.g. code-reviewer"), (v) => { d.name = v.trim(); });
    name.classList.add("mono");
    ed.append(...field(t("Name"), name, t("Lowercase letters, digits and -; it is the file's name in each agent")));
    const box = (cls, value, placeholder, rows, max, onEnter) => {
      const ta = el("textarea", "lib-text small " + cls);
      ta.value = value;
      ta.placeholder = placeholder;
      ta.spellcheck = false;
      ta.rows = rows;
      ta.maxLength = max;
      ta.onkeydown = (e) => { e.stopPropagation(); if (e.isComposing || e.keyCode === 229) return; if (e.key === "Escape") closeLibModal(); else if (onEnter && e.key === "Enter" && modal?.save) { e.preventDefault(); modal.save(); } };
      autosize(ta, rows * 20);
      return ta;
    };
    // one paragraph, as every agent reads it (library.SaveSubagent joins
    // its lines), so Enter saves; the prompt's Enter is a new line
    const desc = box("lib-subagentdescin", d.description, t("e.g. Reviews a diff for bugs. Use after code changes."), 2, 1000, true);
    desc.oninput = () => { d.description = desc.value; };
    ed.append(...field(t("Description"), desc, t("What it does and when to use it: the agent reads this to decide when to hand it a task")));
    const prompt = box("lib-subagentprompt", d.prompt, t("You are a careful code reviewer…"), 6, 100000, false);
    prompt.oninput = () => { d.prompt = prompt.value; };
    ed.append(...field(t("Prompt"), prompt, t("What the subagent is told before its task")));

    // what the skills and servers reach, said under each: the agents
    // picked that can't bind them
    const skillsHint = el("div", "hint lib-subagenthint-skills"), mcpHint = el("div", "hint lib-subagenthint-mcp"), modelHint = el("div", "hint lib-subagenthint-model");
    const skills = namesPick("lib-subagentskills", lib.skills.map((x) => x.name), d, "skills", "Skills", "Find a skill");
    const servers = namesPick("lib-subagentmcp", lib.servers.map((x) => x.name), d, "mcp", "MCP servers", "Find a server");
    const withHint = ([l, w], h) => { w.append(h); return [l, w]; };
    ed.append(...withHint(field(t("Model"), modelPick(d, () => drawHints())), modelHint));
    ed.append(...withHint(field(t("Skills"), skills), skillsHint));
    ed.append(...withHint(field(t("MCP servers"), servers), mcpHint));
    const agentsBox = el("div");
    ed.append(...field(t("Agents"), agentsBox));
    ed.append(err);
    function drawHints() {
      const sk = unbound(all, d.agents, "skills"), mc = unbound(all, d.agents, "mcp");
      skillsHint.textContent = !lib.skills.length ? t("The library has no skills yet") : sk.length ? t("Not bound for {agents}: theirs uses every skill the agent has", { agents: sk.map((a) => a.name).join(", ") }) : "";
      mcpHint.textContent = !lib.servers.length ? t("The library has no MCP servers yet") : mc.length ? t("Not bound for {agents}: theirs uses every server the agent has", { agents: mc.map((a) => a.name).join(", ") }) : "";
      // who runs it on the model set, and who doesn't: Codex's subagent
      // model on the Agents page takes the place of every subagent's
      const mo = unbound(all, d.agents, "model");
      const held = d.model && lib.codexSubagentModel && d.agents.includes("codex");
      modelHint.textContent = !d.model ? t("Each agent runs it on the model it picks for a subagent") : [
        mo.length ? t("Not set for {agents}: theirs runs on the model the agent picks", { agents: mo.map((a) => a.name).join(", ") }) : "",
        held ? t("Codex's subagent model on the Agents page is set, and takes the place of this one") : ""].filter(Boolean).join(" · ");
    }
    function drawAgents() {
      agentsBox.replaceChildren(agentChips(all, d.agents, (next) => { d.agents = next; drawAgents(); }, { names: true, blocked: subagentBlocked, all: true }));
      drawHints();
    }
    drawAgents();

    const bar = el("div", "bar");
    if (s) bar.append(button(t("Remove from the library"), "danger", async () => {
      if (!await confirmRemoval(s.name, "It will be removed from the library and the agents it was given to.")) return;
      if (await change("subagents/remove", { name: s.name }, t("{name} is out of the library and the agents it was given to", { name: s.name }))) closeLibModal(true);
    }));
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal));
    const save = async () => {
      err.textContent = "";
      try {
        const subagent = { name: d.name, description: d.description.trim(), prompt: d.prompt, model: d.model, skills: d.skills, mcp: d.mcp, agents: d.agents.filter((id) => !subagentBlocked(agentOf(id) || { id })) };
        take(await api("library/subagents/save", { old: s ? s.name : "", subagent }));
        report(lib.result, s ? t("{name} saved", { name: d.name }) : "");
        closeLibModal(true);
        render();
      } catch (e) { err.textContent = e.message; }
    };
    bar.append(button(s ? t("Save") : t("Add"), "primary", save));
    ed.append(bar);
    const original = JSON.stringify(d);
    modal = { save, dirty: () => JSON.stringify(d) !== original };
    openLib(ed);
    if (!s) requestAnimationFrame(() => name.focus());
  }

  // ---------- Claude plugins ----------

  // A Claude Code plugin kept as one item (FakeshadowzZ on X): its skills,
  // MCP servers, hooks and subagents are ordinary library items marked with
  // the plugin, so each agent gets them through the code that writes that
  // kind, and the other tabs go on listing them. The plugin's switch, its
  // agents and Remove act on every part at once (library/plugin.go).
  let plProbe = null;    // plugins found at an address: { source, found, pick:Set, agents }
  let plProbing = false;
  const pluginParts = (p) => [
    p.skills?.length ? t("Skills: {names}", { names: p.skills.join(", ") }) : "",
    p.mcp?.length ? t("MCP servers: {names}", { names: p.mcp.join(", ") }) : "",
    p.hooks?.length ? t("Hooks: {names}", { names: p.hooks.join(", ") }) : "",
    p.subagents?.length ? t("Subagents: {names}", { names: p.subagents.join(", ") }) : "",
  ].filter(Boolean).join(" · ");

  function renderPlugins(body) {
    body.append(intro(t("A Claude Code plugin is skills, MCP servers, hooks and subagents in one package. Install it once: magpie gives each part to the agents you pick, and you turn the plugin on, off or remove it as a whole. Its parts also show under their own tabs.")));
    body.append(pluginFind());
    const all = shownAgents();
    const plugins = lib.plugins || [];
    if (!plugins.length) {
      body.append(empty(t("No Claude plugins in the library yet"), t("Install one from a folder, a git address or a marketplace.json.")));
      return;
    }
    const list = el("div", "list lib-list");
    list.append(...[...plugins].sort((a, b) => a.name.localeCompare(b.name)).map((p) => pluginRow(p, all)));
    body.append(list);
  }

  function pluginFind() {
    const card = el("div", "list lib-card lib-install lib-plfind");
    const line = el("div", "lib-find");
    const inp = el("input");
    inp.type = "text";
    inp.dataset.lib = "plsource";
    inp.placeholder = t("owner/repo, a git address, or a folder with a plugin or a marketplace.json");
    inp.spellcheck = false;
    inp.autocomplete = "off";
    inp.value = plProbe?.source || "";
    const go = button(plProbing ? t("Looking…") : t("Find plugins"), "action", () => find());
    go.disabled = plProbing;
    if (plProbing) go.classList.add("busy");
    inp.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") find(); else if (e.key === "Escape" && plProbe) { plProbe = null; render(); } };
    line.append(glyph(GLYPH.search, "lib-mini"), inp, go);
    card.append(line);
    async function find() {
      const src = inp.value.trim();
      if (!src || plProbing) return;
      plProbing = true;
      plProbe = { source: src };
      render();
      try {
        const f = await api("library/plugins/probe", { source: src });
        const fresh = f.candidates.filter((c) => !c.have);
        plProbe = { source: src, found: f, pick: new Set((fresh.length === 1 ? fresh : []).map((c) => c.name)), agents: shownAgents().map((a) => a.id) };
        if (!f.candidates.length) status(t("No Claude plugin found there"), "warn");
      } catch (e) {
        plProbe = { source: src, error: e.message };
      }
      plProbing = false;
      render();
    }
    if (plProbe?.error) card.append(el("div", "lib-err", plProbe.error));
    if (plProbe?.found?.candidates.length) card.append(pluginPicks(plProbe));
    return card;
  }

  function pluginPicks(p) {
    const box = el("div", "lib-probe");
    const list = el("div", "lib-picks");
    for (const x of p.found.candidates) {
      const r = el("label", "lib-pick lib-plpick" + (x.have ? " dim" : ""));
      const cb = el("input");
      cb.type = "checkbox";
      cb.checked = p.pick.has(x.name);
      cb.onchange = () => { if (cb.checked) p.pick.add(x.name); else p.pick.delete(x.name); syncGo(); };
      const w = el("span", "who");
      w.append(el("span", "name", x.name), el("span", "sub", x.description || ""));
      const count = (n) => (n ? [String(n)] : []);
      const parts = x.remote ? t("Fetched from its own repository when installed")
        : pluginParts({ skills: count(x.skills), mcp: count(x.mcp), hooks: count(x.hooks), subagents: count(x.subagents) });
      if (parts) w.append(el("span", "sub lib-plparts", parts));
      r.append(cb, w);
      if (x.have) r.append(tag(t("in the library"), ""));
      list.append(r);
    }
    box.append(list);
    const foot = el("div", "lib-probefoot");
    const agentsBox = el("div");
    const drawAgents = () => agentsBox.replaceChildren(agentChips(shownAgents(), p.agents, (n) => { p.agents = n; drawAgents(); }, { names: false, all: true }));
    drawAgents();
    foot.append(el("span", "note", t("for")), agentsBox, el("span", "grow"));
    foot.append(button(t("Cancel"), "", () => { plProbe = null; render(); }));
    const go = button("", "primary", async () => {
      go.disabled = true;
      go.textContent = t("Installing…");
      const n = p.pick.size;
      if (await change("plugins/install", { source: p.source, names: [...p.pick], agents: p.agents }, n === 1 ? t("1 plugin installed") : t("{n} plugins installed", { n }))) { plProbe = null; render(); }
      else { go.disabled = false; syncGo(); }
    });
    const syncGo = () => { const n = p.pick.size; go.textContent = n === 1 ? t("Install 1 plugin") : t("Install {n} plugins", { n }); go.disabled = !n; };
    syncGo();
    foot.append(go);
    box.append(foot);
    return box;
  }

  function pluginRow(p, all) {
    const row = el("div", "row lib-row click lib-plugin" + (p.off ? " off" : ""));
    const who = el("div", "who");
    who.append(el("div", "name mono", p.name + (p.version ? " " + p.version : "")));
    if (p.description) who.append(el("div", "lib-subagentdesc", p.description));
    // counts, not every name: PStack has a hundred (jinpwng on X); the
    // names are in the dialog, folded
    const line = [[t("Skills"), p.skills], [t("MCP servers"), p.mcp], [t("Hooks"), p.hooks], [t("Subagents"), p.subagents]]
      .filter(([, v]) => v?.length).map(([k, v]) => k + " " + v.length).join(" · ");
    if (line) who.append(el("div", "sub lib-plparts", line));
    row.append(glyph(GLYPH.folder), who,
      agentChips(all, p.agents, chipsChange("plugins/agents", p.name, "plugins", (x) => pluginRow(x, all)), { all: true }),
      toggle(!p.off, t("Turn {name} on or off", { name: p.name }), (v) => change("plugins/on", { name: p.name, on: v },
        v ? t("{name} is on again", { name: p.name }) : t("{name} is off for every agent", { name: p.name }))));
    row.onclick = () => pluginDetail(p);
    row.title = t("Open the plugin {name}", { name: p.name });
    return row;
  }

  function pluginDetail(p) {
    const ed = el("div", "editor lib-editor lib-pldetail");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.folder), el("b", "", p.name));
    ed.append(head);
    if (p.description) ed.append(el("p", "lib-subagentdesc", p.description));
    const rows = [[t("Skills"), p.skills], [t("MCP servers"), p.mcp], [t("Hooks"), p.hooks], [t("Subagents"), p.subagents]].filter(([, v]) => v?.length);
    for (const [label, names] of rows) {
      const d = el("details", "lib-plgroup");
      d.append(el("summary", "", label + " " + names.length), el("div", "mono lib-plnames", names.join(", ")));
      ed.append(d);
    }
    if (p.missing?.length) ed.append(el("div", "hint lib-plmissing", t("Taken out by hand: {names}", { names: p.missing.join(", ") })));
    if (p.left?.length) {
      const left = el("div", "lib-plleft");
      for (const x of p.left) left.append(el("div", "hint", x.what + " — " + x.why));
      ed.append(...field(t("Not carried"), left));
    }
    if (p.source) ed.append(el("div", "hint lib-plsource mono", p.source + (p.pick ? " · " + p.pick : "")));
    const bar = el("div", "bar");
    bar.append(button(t("Remove from the library"), "danger", async () => {
      if (!await confirmRemoval(p.name, "It will be removed from the library, with its skills, MCP servers, hooks and subagents, and from the agents they were given to.")) return;
      if (await change("plugins/remove", { name: p.name }, t("{name} is out of the library and the agents it was given to", { name: p.name }))) closeLibModal(true);
    }));
    bar.append(el("span", "grow"));
    if (p.source) bar.append(button(t("Update"), "", async () => {
      if (await change("plugins/update", { name: p.name }, t("{name} read again from its source", { name: p.name }))) closeLibModal(true);
    }));
    bar.append(button(t("Cancel"), "", closeLibModal));
    ed.append(bar);
    modal = { dirty: () => false };
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
      const fresh = lib.skills.filter((s) => s.kind === "github" || s.kind === "git" || s.origin);
      rh.append(el("span", "grow"));
      if (lib.skills.length > 1) rh.append(sortBy("libSkills", SKILL_SORTS(), () => { if (box.isConnected) drawSkills(box, all); }));
      // what is used now and then waits under ⋯ (ryanpascal on Discord):
      // the bar keeps the order, Select, what is going on and what a check
      // found to do
      if (lib.skills.length > 1 && all.length) rh.append(pickButton("skills"));
      let check = false, again = false, updAll = false;
      if (lib.checkFolders || lib.skills.some((s) => s.kind === "github" || s.kind === "git")) {
        const c = button(checking ? t("Checking…") : t("Check for updates"), "lib-updall lib-check", () => checkSkills());
        c.title = lib.checkFolders
          ? t("Look in the repositories and folders your skills came from for changes and new skills")
          : lib.skills.some((s) => s.kind === "git")
          ? t("Ask the repositories your skills came from which skills changed since they were installed")
          : t("Ask GitHub which skills changed since they were installed");
        c.disabled = checking;
        if (checking) { c.classList.add("busy"); rh.append(c); }
        else check = [c, REAPPLY];
      }
      // not checked since magpie started, or not for a while: the page
      // checks by itself, once, after it is drawn (#1449)
      if (lib.checkDue && !checking && !autoChecked) {
        autoChecked = true;
        setTimeout(() => checkSkills(true), 0);
      }
      // the groups traced again: a pack added a while ago is mostly older
      // copies its repository has edited since (White Immortal on Discord)
      if (lib.skills.length > 1 && lib.skills.some((s) => skillRepo(s))) {
        const g = button(regrouping ? t("Grouping…") : t("Group again"), "lib-updall lib-regroup", () => regroupSkills());
        g.title = t("Fetch the repositories your skills came from, and group the skills they have with them");
        g.disabled = regrouping;
        if (regrouping) { g.classList.add("busy"); rh.append(g); }
        else again = [g, GLYPH.folder];
      }
      // skills their source no longer has, from one place only: no group
      // heading is drawn to hold its Clean up, so the bar does
      if (skillGroups().length === 1) {
        const gone = lib.skills.filter(isGone);
        if (gone.length) rh.append(cleanUpButton(gone));
      }
      // one changed here is updated from its own row, which asks first
      const stale = lib.skills.filter((s) => s.check?.status === "update" && !s.edited && !isGone(s));
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
        // picked from the menu: the bar says it is at work till it is done
        const u = button(updatingAll ? t("Updating…") : t("Update all"), "action lib-updall lib-updallnow", async () => {
          updatingAll = true;
          render();
          await updateAllSkills();
          updatingAll = false;
          render();
        });
        u.title = t("Fetch the {n} skills from GitHub again", { n: fresh.length });
        u.disabled = updatingAll;
        if (updatingAll) { u.classList.add("busy"); rh.append(u); }
        else updAll = [u, GLYPH.down];
      }
      // every one kept in ~/.agents/skills, moved into the library at once
      // (tnt1223); a row moves its own
      const kept = lib.skills.filter((s) => s.shared);
      const many = lib.skills.length > 1;
      rh.append(barMore([
        check, updAll, again,
        many && [foldAllButton(box, all), () => foldAllBtn?.textContent === t("Expand all") ? MOVE_DOWN : MOVE_UP],
        null,
        ...many && all.length ? everyButtons("skills", all).map((b) => [b, GLYPH.power]) : [],
        byAgentRows("skills").length && [byAgentButton("skills"), GLYPH.person],
        kept.length > 1 && [handOverButton(kept), GLYPH.skill],
        null,
        many && [removeEveryButton("skills"), GLYPH.trash],
      ]));
      body.append(rh);
      drawSkills(box, all);
      body.append(box);
      // under the list, so the skills stay where they were above it
      if (all.length) body.append(skillHowBar(), skillWhereBar(), ...skillAutoPart(), ...skillRouterPart());
      if (picking) body.append(pickBar(all));
    }
    if (lib.newSkills?.length) renderNewSkills(body);
    // a glance hasn't looked through the agents' own skills yet (#541)
    if (lib.partial) {
      const rh = el("div", "row-head lib-found-wait");
      rh.setAttribute("aria-busy", "true");
      rh.append(el("span", "label", t("In your agents")), el("span", "grow"), el("span", "note", t("Looking through the agents' skill folders…")));
      body.append(rh);
    } else if (lib.foundSkills.length) {
      const fnames = lib.foundSkills.map((f) => f.name);
      if (pickFound && fnames.length < 2) endFoundPick();
      for (const n of foundPicked) if (!fnames.includes(n)) foundPicked.delete(n);
      for (const n of foundErrs.keys()) if (!fnames.includes(n)) foundErrs.delete(n);
      const rh = el("div", "row-head lib-foundhead");
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
        rh.append(foundPickButton(), all);
      }
      body.append(rh);
      // picking, every row is there to be picked
      body.append(foldedFound("skills", lib.foundSkills.map(foundSkillRow), pickFound));
      if (pickFound) body.append(foundPickBar());
    }
    if (lib.skills.length || lib.projects.length) renderProjects(body, "skills");
    const skip = shownAgents().filter((a) => !a.skills);
    if (skip.length) body.append(el("p", "lib-aside", t("{agents} has no skills folder.", { agents: skip.map((a) => a.name).join(", ") })));
    body.append(...asleepAsides());
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
    rh.append(el("span", "label", t("Also in their repositories")), el("span", "grow"));
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
    if (n.kind === "folder") {
      // a folder the user added skills from: shown where it is
      src.append(pathLink(n.id));
    } else if (n.kind === "git" && !/^https?:\/\//.test(n.id)) {
      // an SSH address (git@host:path) or a file:// one: no page to open
      const a = el("span", "lib-srclink", n.id);
      a.title = n.id;
      src.append(a);
    } else {
      const a = el("a", "lib-srclink", n.kind === "git" ? n.id.replace(/^https?:\/\//, "") : n.repo + "/" + n.path);
      a.href = n.id;
      a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); browse(n.id); };
      src.append(a);
    }
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
  const pickErrs = new Map();  // name → why Remove left it, as the bar's Remove said (#1852)
  // the skills "In your agents" have a Select of their own, to take several
  // out of the agents at once (wongxy on Discord): never with the library's
  let pickFound = false;
  const foundPicked = new Set();
  const foundErrs = new Map();
  let skillQuery = "";         // what the filter over the skills holds
  let skillTimer = 0;          // the filter's redraw, waiting for typing to pause
  const unfiltered = new Set(); // groups folded while filtering, till the filter changes
  let folds = {};              // group → true when folded, false when opened by hand
  try { folds = JSON.parse(localStorage.getItem("magpie.libSkillFolds") || "{}") || {}; } catch {}
  function saveFolds() { try { localStorage.setItem("magpie.libSkillFolds", JSON.stringify(folds)); } catch {} }
  const repoOf = (u) => (u || "").replace(/^https:\/\/github\.com\//, "").split("/").slice(0, 2).join("/");
  // the repository a skill from another git host (kind "git") came from,
  // as the page names it: its address, without https:// or a folder in it
  const gitRepoOf = (s) => (s.kind === "git" ? (s.source || "").replace(/^https?:\/\//, "").replace(/\/-\/tree\/.*$/, "") : "");
  // the repository a skill came from, however it came in: magpie traces
  // one not installed from GitHub too (the skills CLI's lock, the git
  // checkout it's in), so a repository's skills are one group (White
  // Immortal on Discord). owner/repo on GitHub, host/path elsewhere.
  const skillRepo = (s) => s.repo || (s.kind === "github" ? repoOf(s.source) : s.origin ? repoOf(s.origin) : "");
  // a host's (with a path, or a site's kit of skills alone, #1862) or GitHub's
  const repoURL = (r) => (/^[^/]+\.[^/]+(\/|$)/.test(r) ? "https://" + r : "https://github.com/" + r);
  const MANY = 40;   // a group bigger than this starts folded, and so do its parts
  const SPLIT = 8;   // a group bigger than this is split by folder, or by name
  // Heights the rows are held to (library.css): a list's room is known
  // without drawing it.
  const ROW_H = 51, ROW_SRC_H = 67, SUB_H = 36;
  // and a skill off on this computer has its tag on a line of its own
  // under the name (Tao on Discord: its description and path were cut off)
  const HERE_H = 23;
  const skillH = (s) => (s.source ? ROW_SRC_H : ROW_H) + (s.offHere ? HERE_H : 0);

  // By source (the groups, as they always were) or one flat list of every
  // skill by name, A→Z or Z→A (#481)
  const SKILL_SORTS = () => [["source", t("By source")], ...NAME_SORTS];
  // the groups, and each skill's text to filter by, worked out once for
  // each answer from magpie and each pick
  let grouped = {}; // sort → { lib, groups }
  // bySource: the project picker's grouping, whatever sort the list has
  function skillGroups(bySource = false) {
    const pick = bySource ? "source" : sortOf("libSkills", SKILL_SORTS());
    if (grouped[pick]?.lib === lib) return grouped[pick].groups;
    if (pick !== "source") {
      const skills = [...lib.skills].sort(byName(pick));
      const g = { key: "flat", repo: "", skills, parts: null,
        text: new Map(skills.map((s) => [s, (s.name + " " + (s.note || "") + " " + (s.description || "") + " " + skillRepo(s)).toLowerCase()])) };
      grouped[pick] = { lib, groups: [g] };
      return grouped[pick].groups;
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
      g.text = new Map(g.skills.map((s) => [s, (s.name + " " + (s.note || "") + " " + (s.description || "") + " " + (g.mine || (g.owner ? skillRepo(s) : g.repo))).toLowerCase()]));
      g.parts = g.mine ? null : partsOf(g);
    }
    grouped[pick] = { lib, groups };
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
      if (s.kind === "git") {
        // GitLab's …/-/tree/<ref>/<path to the folder>/<skill>
        const m = /\/-\/tree\/[^/]+\/(.+)$/.exec(s.source || "");
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
      let m = g.skills.every((s) => s.kind === "github" || s.kind === "git") ? split(g.skills, folderIn) : null, mono = true;
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

  // What a group shows: its skills the filter matches, and whether it's
  // folded. While filtering a group is open unless folded since the filter
  // changed; else as the user last left it, or folded when there are many.
  const skillHits = (g, q) => (q ? g.skills.filter((s) => g.text.get(s).includes(q)) : g.skills);
  const groupFolded = (g, q) => (q ? unfiltered.has(g.key) : folds[g.key] ?? lib.skills.length > MANY);

  // Collapse all / Expand all (#1862, WardLu): every group the list shows
  // folded, or opened, at once. Only how the list looks changes, never what
  // an agent has. Not filtering, it is the groups' kept folds, as a click
  // on each heading would leave them; while filtering, it folds or opens
  // the groups the filter shows, till the filter changes, as a heading's
  // click does then. A part inside a group keeps its own fold.
  // The ⋯ at a list's heading (ryanpascal on Discord): the actions used
  // now and then, the app's own row menu. Each is [button, icon], a button
  // made as the bar's own would be, so its name, title and whether it can
  // be used are read when the menu opens; null is a line between them, and
  // a hidden button, or a false one (not there), is left out. The menu's item keeps the
  // button's lib-* names, which the tests click.
  function barMore(entries) {
    const b = el("button", "text action lib-updall lib-barmore");
    b.type = "button";
    b.title = t("More");
    b.setAttribute("aria-label", t("More"));
    b.setAttribute("aria-haspopup", "menu");
    b.append(svg(DOTS, 14, 2));
    b.hidden = !entries.some(Boolean);
    b.onclick = (e) => {
      e.stopPropagation();
      if (b.classList.contains("open")) return closeAgentMenu();
      const acts = [], cls = [];
      let sep = false;
      for (const x of entries) {
        if (x === null) { sep = acts.length > 0; continue; }
        if (!x) continue;
        const [btn, icon] = x;
        if (btn.hidden) continue;
        acts.push({ name: btn.textContent, literal: true, icon: typeof icon === "function" ? icon() : icon, off: btn.disabled, tip: btn.title, sep, run: (ev) => btn.onclick(ev) });
        cls.push([...btn.classList].filter((c) => c.startsWith("lib-") && c !== "lib-updall"));
        sep = false;
      }
      if (!acts.length) return;
      openRowMenu(b, acts);
      agentMenu?.box.querySelectorAll(".rm-item").forEach((it, i) => it.classList.add(...cls[i]));
    };
    return b;
  }

  let foldAllBtn = null;
  function foldAllButton(box, all) {
    const b = button("", "lib-updall lib-foldall", () => {
      const q = skillQuery.trim().toLowerCase();
      const shown = skillGroups().filter((g) => skillHits(g, q).length);
      if (shown.length < 2) return;
      const fold = shown.some((g) => !groupFolded(g, q));
      for (const g of shown) {
        if (q) { if (fold) unfiltered.add(g.key); else unfiltered.delete(g.key); }
        else folds[g.key] = fold;
      }
      if (!q) saveFolds();
      if (box.isConnected) drawSkills(box, all);
    });
    b.hidden = true; // till the list is drawn, which says
    foldAllBtn = b;
    return b;
  }
  // the button as the groups drawn are: hidden with one group or none
  function paintFoldAll(shown, q) {
    const b = foldAllBtn;
    if (!b) return;
    b.hidden = shown.length < 2;
    const fold = shown.some((g) => !groupFolded(g, q));
    b.textContent = fold ? t("Collapse all") : t("Expand all");
    b.title = q ? (fold ? t("Fold every group that matches the filter") : t("Open every group that matches the filter"))
      : fold ? t("Fold every group") : t("Open every group");
  }

  // The skills' lists as last drawn into a box: a group's list keeps its
  // rows through a filter or a fold, and draws only those it hasn't.
  let drawn = null; // { box, lists: Map group → window }
  function drawSkills(box, all) {
    const groups = skillGroups();
    const q = skillQuery.trim().toLowerCase();
    if (drawn?.box !== box) drawn = { box, lists: new Map() };
    const hitsOf = (g) => skillHits(g, q);
    const cards = [];
    const single = groups.length === 1;
    const redraw = () => { drawSkills(box, all); };
    const shown = [];
    for (const g of groups) {
      const hits = hitsOf(g);
      if (!hits.length) continue;
      if (!single) shown.push(g);
      const folded = !single && groupFolded(g, q);
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
    paintFoldAll(shown, q);
    if (box.isConnected) { syncLists(); if (picking) syncPicks(); } // the bar's All counts what the filter shows
  }

  // A group's list: its skills, or its parts, each a heading over its own.
  function groupItems(g, hits, all, filtering, redraw) {
    const row = (s) => ({ key: "s\n" + s.name, h: skillH(skillNamed(s.name) || s), make: () => skillRow(skillNamed(s.name) || s, all) });
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
    const stale = p.skills.filter((s) => s.check?.status === "update" && !isGone(s)).length;
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
    // a click draws it anew, and the first part's only neighbour above is
    // the list's empty pad: the page held the next part's heading, pushed
    // down by the rows opened, and scrolled the clicked one away
    // (jinpwng on X). Held is its list instead, whose top doesn't move.
    head.dataset.unrolls = "";
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
    const sub = el("div", "sub", filtering && hits.length !== n ? t("{n} of {total} skills", { n: hits.length, total: n })
      : n === 1 ? t("1 skill") : t("{n} skills", { n }));
    // those kept in ~/.agents/skills, which some agents read themselves:
    // said on the heading, before its chips are clicked (#1862)
    const shared = g.skills.filter((s) => all.some((a) => s.always?.includes(a.id)));
    if (shared.length) {
      const readers = all.filter((a) => shared.some((s) => s.always.includes(a.id))).map((a) => a.name).join(", ");
      const note = el("span", "lib-itself", " · " + t("{n} in ~/.agents/skills", { n: shared.length }));
      sub.title = t("{agents} read ~/.agents/skills themselves: they have these {n} whatever is ticked here, and turning the group off for them leaves those on", { agents: readers, n: shared.length });
      sub.append(note);
    }
    who.append(sub);
    const tags = el("div", "lib-tags");
    // one changed here is updated from its own row, which asks first (#1449)
    const stale = g.skills.filter((s) => s.check?.status === "update" && !s.edited && !isGone(s));
    const gone = g.skills.filter((s) => isGone(skillNamed(s.name) || s));
    if (gone.length) tags.append(cleanUpButton(gone.map((s) => skillNamed(s.name) || s)));
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
  // A skill kept in ~/.agents/skills is had by an agent that reads that
  // folder itself whatever is ticked (alwaysFor): a group's chips count, and
  // give or take, only the skills magpie decides for each agent (#1862,
  // WardLu: the group's chip went off and its rows stayed lit). An agent
  // that reads every one of them itself is lit and locked, as a row's chip
  // is; one that reads some says how many, before the click and after it.
  function groupChips(skills, all, repo, kind = "skills") {
    const servers = kind === "servers", hooks = kind === "hooks";
    const n = skills.length, names = skills.map((s) => s.name);
    const own = (s, a) => !s.always?.includes(a.id);
    const can = (s, a) => (servers ? !!reaches(s)(a) : hooks ? !hookBlocked(s)(a) : own(s, a));
    const reach = (a) => skills.filter((s) => can(s, a)).length;
    const count = (a) => skills.filter((s) => can(s, a) && s.agents?.includes(a.id)).length;
    const itself = (a) => (servers || hooks ? 0 : skills.filter((s) => !own(s, a)).length);
    const full = all.filter((a) => reach(a) && count(a) === reach(a)).map((a) => a.id);
    // each agent is sent the names it can be given: a skill it reads
    // itself is left as it is, so a group's agents may go in more than one
    const send = async (ids, on) => {
      const by = new Map();
      for (const id of ids) {
        const a = all.find((x) => x.id === id);
        const ns = servers || hooks || !a ? names : skills.filter((s) => own(s, a)).map((s) => s.name);
        const k = ns.join("\n");
        if (!by.has(k)) by.set(k, { names: ns, agents: [] });
        by.get(k).agents.push(id);
      }
      let v = null;
      for (const g of by.values()) v = await api("library/" + kind + "/agents-some", { names: g.names, agents: g.agents, on });
      return v;
    };
    const change = async (next) => {
      const on = next.filter((id) => !full.includes(id)), off = full.filter((id) => !next.includes(id));
      box.classList.add("busy");
      try {
        let v = null;
        if (on.length) v = await send(on, true);
        if (off.length) v = await send(off, false);
        if (!v) return;
        take(v);
        const who = (ids) => ids.map(nameOf).join(", ");
        // taken from an agent that still reads some of them itself: said so
        const still = off.map((id) => all.find((x) => x.id === id)).filter((a) => a && itself(a));
        const kept = still.length ? " — " + t("{agent} still reads {n} of them from ~/.agents/skills itself", { agent: still[0].name, n: itself(still[0]) })
          + (still.length > 1 ? " " + t("(and {n} more)", { n: still.length - 1 }) : "") : "";
        report(v.result, (repo != null
          ? (on.length ? t("{repo}'s skills are on for {agents}", { repo, agents: who(on) }) : t("{repo}'s skills are off for {agents}", { repo, agents: who(off) }))
          : n === 1 ? (on.length ? t("{name} is on for {agents}", { name: names[0], agents: who(on) }) : t("{name} is off for {agents}", { name: names[0], agents: who(off) }))
          : servers ? (on.length ? t("{n} servers are on for {agents}", { n, agents: who(on) }) : t("{n} servers are off for {agents}", { n, agents: who(off) }))
          : hooks ? (on.length ? t("{n} hooks are on for {agents}", { n, agents: who(on) }) : t("{n} hooks are off for {agents}", { n, agents: who(off) }))
          : on.length ? t("{n} skills are on for {agents}", { n, agents: who(on) }) : t("{n} skills are off for {agents}", { n, agents: who(off) })) + kept);
      } catch (e) {
        status(e.message, "err", 6000);
      }
      render();
    };
    // an agent's profiles are one chip here too (Yanzhongixuy on X: a
    // repository's heading drew an icon for each of twenty Hermes profiles)
    change.rows = true;
    const box = agentChips(all, full, change, { all: true, blocked: servers ? (a) => (reach(a) ? "" : n === 1 ? sseBlocked(skills[0])(a) : t("{agent} can reach none of these servers", { agent: a.name }))
      : hooks ? (a) => (reach(a) ? "" : n === 1 ? hookBlocked(skills[0])(a) : t("{agent} can run none of these hooks", { agent: a.name })) : null,
    always: servers || hooks || !n ? null : (a) => (reach(a) ? "" : n === 1 ? alwaysFor(skills[0])(a)
      : t("{agent} reads ~/.agents/skills itself, where all of these are kept — it has them whatever is ticked here", { agent: a.name })) });
    box.classList.add("lib-groupchips");
    // a profiles chip is half lit when a profile of it has some of the skills
    for (const c of box.querySelectorAll(".lib-ag[data-members]")) {
      const ms = idsOf(c).map((id) => all.find((x) => x.id === id)).filter((a) => a && reach(a));
      if (ms.some((a) => count(a) && count(a) < reach(a)) || (ms.some((a) => count(a)) && !c.classList.contains("on"))) c.classList.add("some");
    }
    for (const c of box.querySelectorAll(".lib-ag[data-agent]")) {
      const a = all.find((x) => x.id === c.dataset.agent), k = count(a), m = reach(a);
      if (!m) continue;
      if (k && k < m) c.classList.add("some");
      c.title = hooks
        ? (k === m ? t("{agent} has all of these hooks — click to take them away", { agent: a.name })
          : k ? t("{agent} has {n} of these hooks — click to give it the rest", { agent: a.name, n: k })
          : t("Give {agent} all of these hooks", { agent: a.name }))
        : servers
        ? (k === m ? t("{agent} has all of these servers — click to take them away", { agent: a.name })
          : k ? t("{agent} has {n} of these servers — click to give it the rest", { agent: a.name, n: k })
          : t("Give {agent} all of these servers", { agent: a.name }))
        : (k === m ? t("{agent} has all of them — click to take them away", { agent: a.name })
          : k ? t("{agent} has {n} of them — click to give it the rest", { agent: a.name, n: k })
          : t("Give {agent} all of them", { agent: a.name }))
          + (m < n ? "\n" + t("It reads {n} of them from ~/.agents/skills itself: a click here leaves those as they are", { n: n - m }) : "");
      if (m < n) c.classList.add("partly");
    }
    const every = box.querySelector(".lib-ag.all");
    if (every) every.title = every.classList.contains("on")
      ? (servers ? t("Every agent has all of these servers — click to take them from every one") : hooks ? t("Every agent has all of these hooks — click to take them from every one") : t("Every agent has all of them — click to take them from every one"))
      : servers ? t("Give all of these servers to every agent") : hooks ? t("Give all of these hooks to every agent") : t("Give all of them to every agent");
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
      pickErrs.clear();
      render();
    });
    b.dataset.alt = picking ? t("Select") : t("Done"); // as wide as the other, see library.css
    const skills = kind === "skills", hooks = kind === "hooks";
    b.title = picking ? (skills ? t("Stop picking skills") : hooks ? t("Stop picking hooks") : t("Stop picking servers"))
      : skills ? t("Pick some skills and turn them on or off together") : hooks ? t("Pick some hooks and turn them on or off together") : t("Pick some servers and turn them on or off together");
    // one Select at a time, the found skills' Select's other side: ending
    // it from here would take its bar away, scrolling the page
    if (skills && pickFound && !picking) {
      b.disabled = true;
      b.title = t("Done picking the skills in your agents first");
    }
    return b;
  }

  // what's picked is a page's own: another page, or a list too short to
  // pick from, ends it, and a name no longer in the list isn't picked
  function pickingFor(kind, items) {
    if (picking && (picking !== kind || items.length < 2)) { picking = ""; picked.clear(); pickErrs.clear(); }
    if (kind !== "skills") endFoundPick();
    for (const n of picked) if (!items.some((x) => x.name === n)) picked.delete(n);
    for (const n of pickErrs.keys()) if (!items.some((x) => x.name === n)) pickErrs.delete(n);
  }

  // a row with a box, which its click ticks rather than opening it
  function pickRow(row, name) {
    const box = pickBox([name]);
    row.prepend(box);
    row.classList.toggle("picked", picked.has(name));
    row.onclick = () => box.click();
    row.title = t("Select {name}", { name });
    // one the bar's Remove couldn't take out says why, under its name, in
    // place of its line (a row's height is held, so it takes no more room)
    const why = pickErrs.get(name), sub = why && row.querySelector(":scope > .who > .sub");
    if (sub) {
      row.classList.add("lib-pickfailed");
      sub.className = "sub lib-pickerr";
      sub.textContent = sub.title = t("Not removed: {error}", { error: why });
    }
    return row;
  }

  // the skills the filter shows, in every group, folded or not
  function shownSkills() {
    const q = skillQuery.trim().toLowerCase();
    return skillGroups().flatMap((g) => (q ? g.skills.filter((s) => g.text.get(s).includes(q)) : g.skills));
  }

  // A box for some skills: ticked when they're all picked, half when some
  // are; a click picks them all, or none when they all were.
  // The found skills' boxes are the same, on their own set (set, sync).
  function pickBox(names, set = picked, sync = syncPicks) {
    const box = el("input", "lib-pickbox");
    box.type = "checkbox";
    box._names = names;
    box._set = set;
    paintPick(box);
    box.onclick = (e) => {
      e.stopPropagation();
      const every = names.length && names.every((n) => set.has(n));
      for (const n of names) if (every) set.delete(n); else set.add(n);
      sync();
    };
    return box;
  }
  const pickAll = (skills) => pickBox(skills.map((s) => s.name));
  function paintPick(box) {
    const k = box._names.filter((n) => box._set.has(n)).length;
    box.checked = k > 0 && k === box._names.length;
    box.indeterminate = k > 0 && k < box._names.length;
    const row = box.parentElement;
    if (row?.classList.contains("lib-skill") || row?.classList.contains("lib-server") || row?.classList.contains("lib-hook") || row?.classList.contains("lib-found")) row.classList.toggle("picked", box.checked);
  }
  // every box drawn, and the bar, as the picks are now
  function syncPicks() {
    if (!picked.size) naming = false;
    for (const b of page.querySelectorAll(".lib-pickbox")) paintPick(b);
    const bar = page.querySelector(".lib-pickbar:not(.lib-foundbar)");
    if (bar) bar.replaceWith(pickBar(picking === "servers" ? mcpAgents() : picking === "hooks" ? hookAgents() : skillAgents()));
  }

  // the servers' bar is the same, with no filter or groups (#1027)
  function pickBar(all) {
    const bar = el("div", "row lib-row lib-pickbar");
    const servers = picking === "servers", hooks = picking === "hooks";
    const own = servers ? lib.servers : hooks ? lib.hooks || [] : lib.skills;
    const shown = servers || hooks ? own : shownSkills(), q = servers || hooks ? "" : skillQuery.trim();
    const every = el("label", "lib-pickall");
    every.append(pickAll(shown), el("span", "", q ? t("All {n} that match", { n: shown.length }) : t("All {n}", { n: shown.length })));
    every.onclick = (e) => e.stopPropagation();
    const skills = own.filter((s) => picked.has(s.name));
    const n = skills.length;
    bar.append(every, el("span", "lib-pickn" + (n ? "" : " none"), n ? t("{n} selected", { n })
      : servers ? t("Pick servers to turn them on or off together") : hooks ? t("Pick hooks to turn them on or off together") : t("Pick skills to turn them on or off together")), el("span", "grow"));
    if (n && naming) return nameBar(bar, skills);
    if (n) {
      bar.append(groupChips(skills, all, null, picking || "skills"));
      // a group of the user's made of them, or the ones in one let go (#791)
      if (!servers && !hooks && sortOf("libSkills", SKILL_SORTS()) === "source") {
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
      if (servers && projectMCPAgents().length) bar.append(copyConfigButton(skills.map((x) => x.name)));
      // the ones picked out of the library (#1852): asked first, as Remove
      // all asks, and each taken out as its own row's Remove would. Its
      // label holds still as the count changes (the count is beside it, and
      // in the sheet), so a pick never rewraps the bar under the pointer
      const rm = button(t("Remove…"), "action danger lib-updall lib-pickrm", () => servers ? confirmRemoveEveryServer(skills) : hooks ? confirmRemoveEveryHook(skills) : confirmRemoveEverySkill(skills));
      rm.title = servers ? t("Take the servers picked out of the library") : hooks ? t("Take the hooks picked out of the library") : t("Take the skills picked out of the library");
      bar.append(rm);
      const c = button(t("Clear"), "lib-updall lib-pickclear", () => { picked.clear(); syncPicks(); });
      c.title = t("Unpick them all");
      bar.append(c);
    }
    bar.append(button(t("Done"), "action lib-updall lib-pickdone", () => { picking = ""; picked.clear(); pickErrs.clear(); render(); }));
    return bar;
  }

  // Copy config… (#1478, xiaozhu1337): the servers named as an agent's
  // project file has them — what magpie would write into a project it
  // keeps — on the clipboard, for a project the user adds them to by hand.
  // A server the agent can't take that way is left out, and said.
  function copyConfigButton(names) {
    const b = button(t("Copy config…"), "lib-updall lib-copyconfig", () => {
      const opts = projectMCPAgents().map((a) => ({ v: a.id, name: a.name, literalName: true, note: a.projectMCP }));
      openProtoMenu(b, opts, "", async (id) => {
        const a = projectMCPAgents().find((x) => x.id === id);
        if (!a) return;
        try {
          const c = await api("library/mcp/config", { agent: id, names });
          const out = (c.skipped || []).join(", ");
          if (!c.text) return status(t("{agent} can't take {names} from a project's file", { agent: a.name, names: out }), "err", 6000);
          await copy(c.text, "", b, out
            ? t("Copied for {agent}, to paste into {file} in your project. Left out, as {agent} can't take them there: {names}", { agent: a.name, file: c.file, names: out })
            : t("Copied for {agent}, to paste into {file} in your project", { agent: a.name, file: c.file }));
        } catch (e) {
          status(e.message, "err", 6000);
        }
      }, "Copy as an agent's project file", "", "right");
    });
    b.title = names.length === 1 ? t("Copy {name} as an agent's project file has it, to paste into a project yourself", { name: names[0] })
      : t("Copy these servers as an agent's project file has them, to paste into a project yourself");
    return b;
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
  function wireLists() {
    page.addEventListener("scroll", () => {
      queueLists();
      if (!still) for (const w of lists) w.el.classList.add("scrolling");
      clearTimeout(still);
      still = setTimeout(settled, 150);
    }, { passive: true });
    window.addEventListener("resize", queueLists);
  }

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
  // An open project's card lists the skills it has, not every library
  // skill as though each were in it (POM_JUMPING on X): its menu picks
  // which go in. A row whose chips are switched off stays till the card is
  // closed, so it doesn't vanish under the pointer.
  const projectShown = new Map(); // project dir → the skills its open card lists
  const projectAgents = () => shownAgents().filter((a) => a.projectSkills);
  // a project's file may take less than the agent's own: Pi's .pi/mcp.json
  // no SSE, whatever extension its user-wide file is for
  const projectMCPAgents = () => shownAgents().filter((a) => a.projectMCP).map((a) => (a.projectNoSSE ? { ...a, noSSE: true } : a));
  const projectFiles = () => [...new Set(projectMCPAgents().map((a) => a.projectMCP))].join(", ");

  function renderProjects(body, kind) {
    const rh = el("div", "row-head");
    rh.append(el("span", "label", kind === "skills" ? t("Project skills") : kind === "subagents" ? t("Project subagents") : t("Project servers")), el("span", "grow"));
    if (!addingProject) rh.append(button(t("Add a project"), "action lib-updall", () => { addingProject = { dir: "" }; render(); }));
    body.append(rh);
    if (addingProject) body.append(addProjectCard(kind));
    if (!lib.projects.length && !addingProject) {
      body.append(el("p", "lib-aside", kind === "subagents"
        ? t("Give a project's agents some of these subagents as the project's own: magpie writes them into its .claude/agents and the like, and keeps them out of git.")
        : kind === "mcp"
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
        status(kind === "subagents" ? t("{name} added — pick the subagents it gets", { name: p?.name || dir }) : kind === "mcp" ? t("{name} added — pick the servers it gets", { name: p?.name || dir }) : t("{name} added — pick the skills it gets", { name: p?.name || dir }), "ok");
      } catch (e) {
        a.busy = false;
        a.error = e.message;
      }
      render();
    }
    return card;
  }

  function projectCard(p, kind) {
    const mcp = kind === "mcp", sub = kind === "subagents";
    const card = el("div", "list lib-card lib-project" + (p.missing ? " missing" : ""));
    const isOpen = openProjects.has(p.dir);
    const head = el("div", "row lib-row click lib-projhead" + (isOpen ? " open" : ""));
    const who = el("div", "who");
    const nm = el("div", "name", p.name);
    if (p.missing) nm.append(tag(t("Folder is gone"), "warn"));
    else if (p.copy && !mcp && !sub) nm.append(tag(t("Copies"), ""));
    who.append(nm);
    const src = el("div", "lib-src");
    src.append(pathLink(p.dir));
    who.append(src);
    const names = Object.keys((sub ? p.subagents : mcp ? p.servers : p.skills) || {}).sort();
    const pills = el("div", "lib-tags");
    if (!isOpen) {
      for (const n of names.slice(0, 4)) pills.append(tag(n, "lib-dot"));
      if (names.length > 4) pills.append(tag("+" + (names.length - 4), ""));
      if (!names.length) pills.append(tag(sub ? t("No subagents yet") : mcp ? t("No servers yet") : t("No skills yet"), "lib-unchecked"));
    }
    const acts = el("div", "lib-rowacts");
    const rm = button("", "lib-icon danger", () => confirmRemoveProject(p));
    rm.append(svg(GLYPH.trash, 13, 1.4));
    rm.title = t("Remove the project");
    acts.append(rm);
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    head.append(glyph(GLYPH.folder), who, pills, acts, chev);
    head.onclick = () => { isOpen ? openProjects.delete(p.dir) : openProjects.add(p.dir); projectShown.delete(p.dir); render(); };
    head.title = sub ? (isOpen ? t("Hide its subagents") : t("Pick the subagents it gets")) : mcp ? (isOpen ? t("Hide its servers") : t("Pick the servers it gets")) : isOpen ? t("Hide its skills") : t("Pick the skills it gets");
    card.append(head);
    if (p.problems?.[""]) card.append(el("div", "lib-err", p.problems[""]));
    if (!isOpen) return card;
    if (mcp) return projectServers(p, card);
    if (sub) return projectSubagents(p, card);

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
    const all = projectAgents();
    const shown = projectShown.get(p.dir) || new Set();
    projectShown.set(p.dir, shown);
    const has = (s) => !!(p.skills[s.name]?.length || p.problems?.[s.name] || p.skillsOff?.[s.name]?.length);
    for (const s of lib.skills) if (has(s)) shown.add(s.name);
    opts.append(projectSkillsPick(p, all, has));
    card.append(opts);

    const listed = lib.skills.filter((s) => shown.has(s.name));
    if (!listed.length) card.append(el("p", "lib-aside lib-projfoot lib-projnone", t("No skills yet")));
    for (const s of listed) {
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
      row.append(mark(s.icon, GLYPH.skill), w, scopedChips(p, s, all, "skill", { via, cantOff: routedOff }));
      card.append(row);
    }
    const dirs = [...new Set(all.map((a) => a.projectSkills))];
    card.append(el("p", "lib-aside lib-projfoot", t("Placed in {dirs} of the project and kept out of git, its .gitignore untouched. A folder there that isn't magpie's is left as it is.", { dirs: dirs.join(", ") })));
    const skip = shownAgents().filter((a) => a.skills && !a.projectSkills);
    if (skip.length) card.append(el("p", "lib-aside lib-projfoot", t("{agents} reads no project skills folder magpie knows of.", { agents: skip.map((a) => a.name).join(", ") })));
    return card;
  }

  // The open card's menu of the library's skills, those in the project
  // ticked. One ticked goes to the agents the project's skills are for
  // already, or, in a project with none yet, to every agent here that
  // reads a project's skills; its chips change that after. One unticked is
  // taken out of the project. The rest keep their agents.
  function projectSkillsPick(p, all, has) {
    const b = menuButton(t("Skills"), "lib-projpick", () => {
      // a skill from the Library is ticked while on in the project for
      // an agent that can have it turned off here; unticking turns it off
      const offIds = all.filter((a) => a.projectSkillOff).map((a) => a.id);
      const global = (s) => (s.agents || []).filter((id) => offIds.includes(id) && !routedOff(agentOf(id) || { name: id }, s));
      const inherited = (s) => !p.skills[s.name]?.length && global(s).some((id) => !(p.skillsOff?.[s.name] || []).includes(id));
      const now = lib.skills.filter((s) => p.skills[s.name]?.length || p.problems?.[s.name] || inherited(s)).map((s) => s.name);
      // by source, as the Library lists them, a heading ticking its group
      const gs = skillGroups(true);
      const opts = gs.length < 2 ? lib.skills.map((s) => ({ v: s.name, name: s.name, literalName: true, note: "" }))
        : gs.flatMap((g) => {
          const title = g.mine || g.repo || t("On this computer");
          return [{ v: "\x00" + g.key, group: g.skills.map((s) => s.name), name: title, literalName: true, note: "" },
            ...g.skills.map((s) => ({ v: s.name, name: s.name, literalName: true, note: "", find: title }))];
        });
      openProtoMenu(b, opts, now, async (next) => {
        const byName = (n) => lib.skills.find((s) => s.name === n);
        let adds = next.filter((n) => !now.includes(n)), outs = now.filter((n) => !next.includes(n));
        // from the Library: on and off in the project, not the project's own
        const back = adds.filter((n) => p.skillsOff?.[n]?.length);
        const offs = outs.filter((n) => inherited(byName(n)));
        adds = adds.filter((n) => !back.includes(n));
        outs = outs.filter((n) => !offs.includes(n));
        if (back.length) {
          await change("projects/skill-off", { dir: p.dir, names: back, agents: [] }, t("{item} is back on in {name}", { item: back.join(", "), name: p.name }));
        }
        if (offs.length) {
          const shown = projectShown.get(p.dir);
          for (const n of offs) shown?.add(n);
          const ids = [...new Set(offs.flatMap((n) => global(byName(n))))];
          await change("projects/skill-off", { dir: p.dir, names: offs, agents: ids }, t("{item} is off in {name} for {agents}", { item: offs.join(", "), name: p.name, agents: ids.map(nameOf).join(", ") }));
        }
        const ids = new Set(all.map((a) => a.id));
        const used = [...new Set(Object.values(p.skills || {}).flat())].filter((id) => ids.has(id));
        const agents = used.length ? used : all.filter((a) => !a.gone && !a.unseen && !a.asleep).map((a) => a.id);
        const names = (xs) => xs.join(", ");
        if (adds.length && agents.length) {
          await change("projects/skills", { dir: p.dir, names: adds, agents }, adds.length === 1
            ? t("{skill} is in {name} for {agents}", { skill: adds[0], name: p.name, agents: agents.map(nameOf).join(", ") })
            : t("{skills} are in {name} for {agents}", { skills: names(adds), name: p.name, agents: agents.map(nameOf).join(", ") }));
        }
        if (outs.length) {
          const shown = projectShown.get(p.dir);
          for (const n of outs) shown?.delete(n);
          await change("projects/skills", { dir: p.dir, names: outs, agents: [] }, outs.length === 1
            ? t("{skill} is out of {name}", { skill: outs[0], name: p.name })
            : t("{skills} are out of {name}", { skills: names(outs), name: p.name }));
        }
      }, "Pick the skills it gets", "", "right", false, lib.skills.length > 8 ? "Find a skill" : "");
    });
    b.setAttribute("aria-expanded", "false");
    if (!lib.skills.length || !all.length) b.disabled = true;
    return b;
  }

  // A project row's chip: lit at once, so a second click before magpie has
  // answered the first counts from it; the clicks are sent one at a time,
  // the last of them winning, and the page drawn again once they're in.
  const projectWriting = new Map();
  function projectChange(path, dir, name, done, offs = false) {
    return async (next, c) => {
      const on = offs ? !next.includes(c.dataset.agent) : next.includes(c.dataset.agent);
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

  // A project is the Library's global set with its own added and some of
  // the global ones turned off there. A row's chip is lit for an agent
  // that has the item in the project: given it in the Library (and not
  // turned off here), or by the project. A click on an agent that has it
  // from the Library turns it off, or back on, in this project alone; an
  // agent that can't have one turned off for a project keeps it lit, and
  // says so.
  function scopedChips(p, item, all, kind, opts = {}) {
    const skill = kind === "skill";
    const global = (id) => (item.agents || []).includes(id) && !item.hidden;
    const canOff = (a) => (skill ? a.projectSkillOff : a.projectOff) && !opts.cantOff?.(a, item);
    const offs = ((skill ? p.skillsOff : p.serversOff) || {})[item.name] || [];
    const own = ((skill ? p.skills : p.servers) || {})[item.name] || [];
    const on = all.filter((a) => (global(a.id) ? !offs.includes(a.id) : own.includes(a.id))).map((a) => a.id);
    const path = skill ? "projects/skill" : "projects/server";
    const say = (next) => next.length
      ? t(skill ? "{skill} is in {name} for {agents}" : "{server} is in {name} for {agents}", { skill: item.name, server: item.name, name: p.name, agents: next.map(nameOf).join(", ") })
      : t(skill ? "{skill} is out of {name}" : "{server} is out of {name}", { skill: item.name, server: item.name, name: p.name });
    const add = projectChange(path, p.dir, item.name, say);
    const off = projectChange(path + "-off", p.dir, item.name, (next) => next.length
      ? t("{item} is off in {name} for {agents}", { item: item.name, name: p.name, agents: next.map(nameOf).join(", ") })
      : t("{item} is back on in {name}", { item: item.name, name: p.name }), true);
    const change = (next, c) => {
      if (!global(c.dataset.agent)) return add(next.filter((id) => !global(id)), c);
      return off(all.filter((a) => global(a.id) && canOff(a) && !next.includes(a.id)).map((a) => a.id), c);
    };
    const always = (a) => global(a.id) && !canOff(a) ? opts.cantOff?.(a, item) || t("{agent} has it from the Library, and can't have it turned off in one project", { agent: a.name }) : "";
    return agentChips(all, on, change, { ...opts, always });
  }

  // A skill the skill router lists is reached through the router's own
  // skill, which a project can't turn one off in.
  const routedOff = (a, s) => lib.skillRouter && s.router !== "direct" && !s.manual
    ? t("{agent} reaches {skill} through the skill router, which can't leave it out in one project: set it to not go through the router first", { agent: a.name, skill: s.name })
    : "";

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
      row.append(mark(s.icon, s.transport === "stdio" ? GLYPH.cmd : GLYPH.web), w, scopedChips(p, s, all, "server", { blocked: sseBlocked(s) }));
      card.append(row);
    }
    if (!lib.servers.length) card.append(el("p", "lib-aside lib-projfoot", t("No servers in the library yet")));
    card.append(el("p", "lib-aside lib-projfoot", t("Written into {files} of the project, beside what's in them; a file magpie makes is kept out of git, its .gitignore untouched. A server there by the same name that isn't magpie's is left as it is.", { files: projectFiles() })));
    card.append(el("p", "lib-aside lib-projfoot", t("Claude Code asks before it starts a project's servers; Codex reads .codex/config.toml, and Pi .pi/mcp.json, only in a project you trust.")));
    const skip = shownAgents().filter((a) => a.mcp && !a.projectMCP);
    if (skip.length) card.append(el("p", "lib-aside lib-projfoot", t("{agents} reads no project MCP file magpie knows of.", { agents: skip.map((a) => a.name).join(", ") })));
    return card;
  }

  // The project's card, open on the Subagents tab: each library subagent
  // with the chips of the agents that read a project's own subagent folder.
  function projectSubagents(p, card) {
    const all = shownAgents().filter((a) => a.projectSubagents);
    for (const s of lib.subagents || []) {
      const row = el("div", "row lib-row lib-projskill lib-projsubagent");
      const w = el("div", "who");
      const n = el("div", "name mono", s.name);
      const problem = p.problems?.["subagent:" + s.name];
      if (problem) n.append(tag(t("Not written"), "warn", problem));
      w.append(n);
      const sub = el("div", "sub", problem || s.description || "");
      sub.title = problem || s.description || "";
      w.append(sub);
      const on = (p.subagents?.[s.name] || []).filter((id) => all.some((a) => a.id === id));
      const say = (next) => next.length
        ? t("{item} is in {name} for {agents}", { item: s.name, name: p.name, agents: next.map(nameOf).join(", ") })
        : t("{item} is out of {name}", { item: s.name, name: p.name });
      row.append(glyph(GLYPH.subagent), w, agentChips(all, on, projectChange("projects/subagents", p.dir, s.name, say)));
      card.append(row);
    }
    if (!(lib.subagents || []).length) card.append(el("p", "lib-aside lib-projfoot", t("No subagents in the library yet")));
    card.append(el("p", "lib-aside lib-projfoot", t("Written into the project's own agents folder, one file per subagent; a file magpie makes is kept out of git, its .gitignore untouched. A file there by the same name that isn't magpie's is left as it is.")));
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
        ? t("magpie forgets the project. The skills and MCP servers it put in the folder stay there as they are, kept out of git, and are yours from then on; a linked skill still follows the library's.")
        : t("The skills and MCP servers magpie put in it are taken away. Nothing else in the folder is touched.");
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
      const up = res.updated?.length || 0, no = res.unupdated || [], gone = res.gone || [];
      // one changed here is left as it is (#1449): said apart from those
      // that couldn't be fetched
      const isEdited = (p) => lib.skills.some((s) => s.edited && "skill:" + s.name === p.what);
      const kept = no.filter(isEdited).map((p) => p.what.replace(/^skill:/, "")), failed = no.filter((p) => !isEdited(p));
      if (no.length || gone.length) {
        const parts = [];
        if (gone.length) parts.push(goneSaid(gone));
        if (failed.length) {
          const p = failed[0];
          parts.push(t("{name} wasn't updated: {error}", { name: p.what.replace(/^skill:/, ""), error: p.error }) + (failed.length > 1 ? " " + t("(and {n} more)", { n: failed.length - 1 }) : ""));
        }
        if (kept.length) parts.push(t("Kept as you changed them: {names}. Update each from its row to replace your changes.", { names: kept.join(", ") }));
        if (up) parts.push(t("{n} up to date", { n: up }));
        status(parts.join(" · "), "warn", 10000);
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
      ? t("Each agent gets a folder of its own, made again when the library's skill changes. An edit made in a copy goes into the library and to the other agents.")
      : t("Links follow the library's skill: an update reaches the agents at once.")));
    return bar;
  }

  // Where the skills go (usrname1833): to the agents each is ticked for,
  // in their own folders, or also into ~/.agents/skills, which many agents
  // and tools read, so each of them has every skill. The files stay in the
  // library's folder either way; ~/.agents/skills gets links to them.
  function skillWhereBar() {
    const bar = el("div", "lib-skillhow lib-skillwhere");
    bar.id = "lib-skillwhere";
    const where = lib.sharedSkills ? "shared" : "agents";
    bar.append(el("span", "label", t("Put skills in")), segs([["agents", t("Each agent's own folder")], ["shared", "~/.agents/skills"]], where, (w) => {
      if (w !== where) change("skills/where", { how: w }, w === "shared" ? t("Every skill is in ~/.agents/skills now") : t("Skills go to the agents they're ticked for now"));
    }));
    bar.append(el("span", "note", where === "shared" && lib.skillRouter
      ? t("The skill router lists nothing while every skill is in ~/.agents/skills: choose each agent's own folder.")
      : where === "shared"
      ? t("Every skill on here is linked into ~/.agents/skills too: each agent that reads that folder has every skill, whatever is ticked. The files stay in the library.")
      : t("Each agent gets the skills ticked for it, in its own folder.")));
    return bar;
  }

  // Updates by themselves (0xYaCo on X): with the switch on, magpie checks
  // the skills from GitHub and git repositories as this page does and
  // fetches again each one changed there; one changed here keeps its
  // Update button (skill_autoupdate.go). Only with such a skill to update.
  function skillAutoPart() {
    if (!lib.autoUpdateSkills && !lib.skills.some((s) => s.kind === "github" || s.kind === "git")) return [];
    const list = el("div", "list lib-list lib-hublist lib-skillauto");
    const row = el("div", "row lib-row lib-hub");
    const who = el("div", "who");
    who.append(el("div", "name", t("Update by themselves")));
    who.append(el("div", "sub wraps", t("Fetch skills from GitHub and git again when they change there. One you changed here stays as it is.")));
    row.append(glyph(GLYPH.down), who, toggle(!!lib.autoUpdateSkills, t("Update by themselves"), (on) =>
      change("skills/auto-update", { on }, on ? t("Skills update by themselves now") : t("Skills wait for you to update them now"))));
    list.append(row);
    return [list];
  }

  // The skill router: each agent is given one skill, magpie-skills, whose
  // SKILL.md lists the skills ticked for it, in place of a link of each;
  // the agent reads one's SKILL.md when a task fits it (skill_router.go).
  // A skill kept direct, or one only for the user to type, keeps its link.
  function skillRouterPart() {
    const list = el("div", "list lib-list lib-hublist lib-skillrouter");
    const row = el("div", "row lib-row lib-hub");
    const who = el("div", "who");
    who.append(el("div", "name", t("Skill router")));
    who.append(el("div", "sub wraps", lib.skillRouter
      ? t("Each agent is given one skill, magpie-skills, that lists its skills. It reads a skill when a task fits it, so its prompt carries one description instead of every skill's. Restart an agent for it to read the change.")
      : t("Give each agent one skill, magpie-skills, that lists its skills, in place of each one: its prompt carries one description instead of every skill's.")));
    row.append(glyph(GLYPH.skill), who, toggle(!!lib.skillRouter, t("Skill router"), (on) =>
      change("skills/router", { on }, on
        ? t("The agents are given magpie-skills in place of their skills — restart them to use it")
        : t("The agents are given each skill again — restart them to use it"))));
    list.append(row);
    const out = [list];
    if (!lib.skillRouter) return out;
    const names = (why) => skillAgents().filter((a) => !a.gone && a.skillRouter === why).map((a) => a.name).join(", ");
    const why = [
      ["copies", "{agents} gets copies of its skills, so it keeps each skill of its own."],
      ["liveagent", "{agents} uses only the skills picked in its own list, so it keeps each skill of its own."],
      ["shared", "{agents} reads ~/.agents/skills, which other agents read too, so it keeps each skill of its own."],
      ["taken", "A skill in the library is called magpie-skills, so {agents} keeps each skill of its own: give that skill another name for the router to be used."],
      ["own", "{agents} has a skill called magpie-skills of its own, which magpie leaves as it is, so it keeps each skill of its own."],
      ["none", "No skill of the library's is listed for {agents}: tick one for it, or list one kept out of the router."],
    ];
    for (const [k, text] of why) {
      const n = names(k);
      if (n) {
        const p = el("p", "lib-aside lib-hub-why", t(text, { agents: n }));
        p.dataset.why = k;
        out.push(p);
      }
    }
    // skills in an agent's own folder that the library doesn't have are not
    // listed, so each stays in its prompt
    const mine = skillAgents().filter((a) => !a.gone && (a.skillRouter === "on" || a.skillRouter === "none")
      && lib.foundSkills.some((f) => !f.manual && f.agents.includes(a.id))).map((a) => a.name).join(", ");
    if (mine) {
      const p = el("p", "lib-aside lib-hub-why", t("{agents} has skills of its own that aren't in the library, so the router doesn't list them: bring them in below.", { agents: mine }));
      p.dataset.why = "found";
      out.push(p);
    }
    return out;
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
    const draw = () => list.replaceChildren(...lib.agents.filter((a) => a.skills && !a.gone && (!isHidden(a) || a.howOwn)).map((a) => {
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
      return { a, has, missing, hidden: isHidden(a), gone: !!a.gone };
    }).filter((r) => !r.hidden && !r.gone || r.has || kept?.has(r.a.id));
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
    const draw = () => list.replaceChildren(...byAgentRows(kind, listed).map(({ a, has, missing, hidden: shy, gone }) => {
      const row = el("div", "row lib-row lib-byagent-row");
      row.dataset.agent = a.id;
      const who = el("div", "who");
      const name = el("div", "name", a.name);
      // one gone from this computer is listed while it has some, for them
      // to be taken out, as one hidden is
      const hidden = shy || gone;
      if (gone) name.append(tag(t("Not installed here"), "", t("Not installed here: it isn't given anything new, and what it has can be taken out here")));
      else if (hidden) name.append(tag(t("Hidden"), "", t("Hidden on the Agents page: it isn't given anything new, and what it has can be taken out here")));
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
    const skills = kind === "skills", hooks = kind === "hooks";
    const b = button(t("Remove all"), "action danger lib-updall lib-everyrm", () => skills ? confirmRemoveEverySkill() : hooks ? confirmRemoveEveryHook() : confirmRemoveEveryServer());
    b.title = skills ? t("Take all {n} skills out of the library", { n: lib.skills.length })
      : hooks ? t("Take all {n} hooks out of the library", { n: (lib.hooks || []).length })
      : t("Take all {n} servers out of the library", { n: lib.servers.length });
    return b;
  }

  // the sheet asking first: its head and what it says, then Remove all n,
  // or Remove n when they are the ones picked (some)
  function confirmRemoveEvery(kind, names, title, lines, some) {
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", title));
    ed.append(head);
    for (const l of lines) if (l) ed.append(el("p", "lib-confirm", l));
    const bar = el("div", "bar");
    const label = !some ? t("Remove all {n}", { n: names.length }) : names.length === 1 ? t("Remove") : t("Remove {n}", { n: names.length });
    const go = button(label, "primary danger-fill", async () => { go.disabled = true; if (await removeEvery(kind, names, some)) closeLibModal(true); else go.disabled = false; });
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

  // every skill, or those picked (#1852)
  function confirmRemoveEverySkill(some) {
    const skills = (some || lib.skills).slice(), names = skills.map((s) => s.name), n = names.length;
    const linked = skills.filter((s) => s.kind === "folder").length, kept = n - linked;
    const projects = (lib.projects || []).filter((p) => names.some((x) => p.skills?.[x]?.length)).length;
    const where = [];
    if (kept) where.push(t("{n} folders are moved to magpie's backups (Backups, at the top of the Library), not deleted.", { n: kept }));
    if (linked) where.push(t("{n} linked from folders of your own are only unlinked: those folders stay where they are.", { n: linked }));
    confirmRemoveEvery("skills", names, !some ? t("Remove all {n} skills?", { n }) : n === 1 ? t("Remove {name}?", { name: names[0] }) : t("Remove {n} skills?", { n }), [
      takenFrom(skills),
      projects && t("The skills magpie placed in {n} projects are taken away too.", { n: projects }),
      where.join(" "),
    ], !!some);
  }

  function confirmRemoveEveryServer(some) {
    const servers = (some || lib.servers).slice(), names = servers.map((s) => s.name), n = names.length;
    const projects = (lib.projects || []).filter((p) => names.some((x) => p.servers?.[x]?.length)).length;
    const signed = servers.filter((s) => s.signIn?.signedIn).length;
    confirmRemoveEvery("servers", names, !some ? t("Remove all {n} servers?", { n }) : n === 1 ? t("Remove {name}?", { name: names[0] }) : t("Remove {n} servers?", { n }), [
      takenFrom(servers),
      projects && t("The servers magpie placed in {n} projects are taken away too.", { n: projects }),
      signed && t("magpie's sign-in to {n} of them is forgotten too.", { n: signed }),
      t("Servers your agents have that aren't in the library stay as they are."),
    ], !!some);
  }

  // Those picked (some, #1852): the ones taken out leave the list and the
  // picks; one that couldn't be stays picked and says why on its row, and
  // only those gone are counted as removed. Nothing removed at all (the
  // change refused) leaves every one picked, the sheet open with why.
  async function removeEvery(kind, names, some) {
    const what = (p) => p.what.replace(/^(skill|mcp|hook):/, "");
    try {
      const v = await api("library/" + kind + "/remove-all", { names });
      take(v);
      const res = v.result || {}, no = res.unremoved || [];
      if (some) {
        pickErrs.clear();
        for (const p of no) pickErrs.set(what(p), p.error);
        const gone = names.filter((x) => !pickErrs.has(x));
        for (const x of gone) picked.delete(x);
        const done = gone.length === 1 ? t("{name} removed", { name: gone[0] })
          : kind === "skills" ? t("{n} skills removed", { n: gone.length }) : kind === "hooks" ? t("{n} hooks removed", { n: gone.length }) : t("{n} servers removed", { n: gone.length });
        if (no.length) status((gone.length ? done + " · " : "") + t("{name} wasn't removed: {error}", { name: what(no[0]), error: no[0].error }) + (no.length > 1 ? " " + t("(and {n} more)", { n: no.length - 1 }) : ""), "warn", 10000);
        else report(res, done);
        render();
        return true;
      }
      if (no.length) {
        const p = no[0];
        status(t("{name} wasn't removed: {error}", { name: what(p), error: p.error }) + (no.length > 1 ? " " + t("(and {n} more)", { n: no.length - 1 }) : ""), "warn", 8000);
      } else report(res, kind === "skills" ? t("{n} skills removed", { n: names.length }) : kind === "hooks" ? t("{n} hooks removed", { n: names.length }) : t("{n} servers removed", { n: names.length }));
      render();
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      // none went: every one stays picked, and one alone says why on its
      // row too (the change names only the first reason of several)
      if (some) {
        pickErrs.clear();
        if (names.length === 1) pickErrs.set(names[0], e.message);
        render();
      }
      return false;
    }
  }

  // Which skills GitHub changed since they were installed: each row says,
  // and the heading offers to update just those.
  // quiet is the page's own check as it opens the skills: it says only
  // what there is to update or add, and nothing when there is nothing.
  async function checkSkills(quiet) {
    if (checking) return;
    checking = true;
    render();
    try {
      take(await api("library/skills/check", {}));
      const got = lib.skills.filter((s) => s.check);
      const n = got.filter((s) => s.check.status === "update" && !isGone(s)).length;
      const unknown = got.filter((s) => s.check.status === "unknown");
      const gone = lib.skills.filter(isGone).map((s) => s.name);
      let msg = n ? (n === 1 ? t("1 skill has an update") : t("{n} skills have updates", { n })) : gone.length ? "" : t("Every skill is up to date");
      if (gone.length) msg += (msg ? " · " : "") + goneSaid(gone);
      const more = lib.newSkills?.length || 0;
      if (more) msg += " · " + (more === 1 ? t("1 more skill in their repositories") : t("{n} more skills in their repositories", { n: more }));
      if (quiet) {
        const said = [];
        if (n) said.push(n === 1 ? t("1 skill has an update") : t("{n} skills have updates", { n }));
        if (gone.length) said.push(goneSaid(gone));
        if (more) said.push(more === 1 ? t("1 more skill in their repositories") : t("{n} more skills in their repositories", { n: more }));
        if (said.length) status(said.join(" · "), "ok");
      } else if (unknown.length) {
        msg += " · " + t("{n} couldn't be checked: {error}", { n: unknown.length, error: checkError(unknown[0].check) });
        status(msg, "warn", 8000);
      } else status(msg, gone.length ? "warn" : "ok", gone.length ? 10000 : undefined);
    } catch (e) {
      if (!quiet) status(e.message, "err", 6000);
    }
    checking = false;
    // drawn after the await, a page that can't be drawn says why, as one
    // loaded does, not left blank without a word (#1217)
    try { render(); } catch (e) { status(e.message, "err"); }
  }

  // Group again: each repository the library has skills from is fetched,
  // and the skills it has that weren't grouped with it join it
  async function regroupSkills() {
    if (regrouping) return;
    regrouping = true;
    render();
    try {
      const v = await api("library/skills/regroup", {});
      const n = v.regrouped || 0;
      take(v);
      status(n ? (n === 1 ? t("1 more skill grouped with its repository") : t("{n} more skills grouped with their repositories", { n }))
        : t("No more skills to group with their repositories"), "ok");
    } catch (e) {
      status(e.message, "err", 6000);
    }
    regrouping = false;
    try { render(); } catch (e) { status(e.message, "err"); }
  }

  // why a skill couldn't be checked: GitHub's rate limit used up said in
  // the page's words, with when it lifts and how a token raises it
  function checkError(c) {
    const l = c.limited;
    if (!l) return t(c.error);
    const d = l.until ? new Date(l.until) : null;
    const time = d && !isNaN(d) ? d.toLocaleTimeString(intlLang(), { hour: "2-digit", minute: "2-digit" }) : "";
    if (l.token) return time ? t("GitHub's rate limit for your GitHub token is used up until {time}", { time })
      : t("GitHub's rate limit for your GitHub token is used up for now");
    return time ? t("GitHub allows 60 requests an hour without a token, used up until {time}. Add a GitHub token in Settings → Network and sharing to raise it to 5,000.", { time })
      : t("GitHub allows 60 requests an hour without a token, used up for now. Add a GitHub token in Settings → Network and sharing to raise it to 5,000.");
  }

  // what a check found of a skill, for a tooltip: the last commit to it
  function checkLine(c) {
    const when = c.date ? new Date(c.date).toLocaleDateString(intlLang()) : "";
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

  // A skill of the user's own: a name and a description make its SKILL.md
  // in the library's folder (josephBenchap on X); it opens in the reader,
  // whose path opens it in their editor.
  function newSkill() {
    const all = skillAgents();
    const d = { name: "", description: "", agents: all.map((a) => a.id) };
    const ed = el("div", "editor lib-editor lib-newskill");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.skill), el("b", "", t("New skill")));
    ed.append(head);
    const err = el("div", "editor-error");
    const name = field2("", t("e.g. my-skill"), (v) => { d.name = v.trim(); });
    name.classList.add("mono");
    ed.append(...field(t("Name"), name, t("Letters, digits, - and _ — what the agents call it")));
    const desc = el("textarea", "lib-text small lib-newskilldesc");
    desc.placeholder = t("e.g. Writes release notes. Use when asked for a changelog.");
    desc.spellcheck = false;
    desc.rows = 2;
    desc.maxLength = 1024;
    desc.oninput = () => { d.description = desc.value; };
    desc.onkeydown = (e) => { e.stopPropagation(); if (e.isComposing || e.keyCode === 229) return; if (e.key === "Escape") closeLibModal(); else if (e.key === "Enter") { e.preventDefault(); modal?.save?.(); } };
    ed.append(...field(t("Description"), desc, t("What it does and when to use it: the agent reads this to decide when to load it")));
    const agentsBox = el("div");
    ed.append(...field(t("Agents"), agentsBox));
    ed.append(err);
    const drawAgents = () => agentsBox.replaceChildren(agentChips(all, d.agents, (next) => { d.agents = next; drawAgents(); }, { names: true, all: true }));
    drawAgents();
    const bar = el("div", "bar");
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal));
    const save = async () => {
      err.textContent = "";
      try {
        take(await api("library/skills/create", { name: d.name, description: d.description, agents: d.agents }));
        report(lib.result, t("{name} is in the library now", { name: d.name }));
        render();
        const made = lib.skills.find((x) => x.name === d.name);
        if (made) viewSkill(made); else closeLibModal(true);
      } catch (e) { err.textContent = e.message; }
    };
    bar.append(button(t("Create"), "primary", save));
    ed.append(bar);
    const original = JSON.stringify(d);
    modal = { save, dirty: () => JSON.stringify(d) !== original };
    openLib(ed);
    requestAnimationFrame(() => name.focus());
  }

  function installCard() {
    const card = el("div", "list lib-card lib-install");
    const line = el("div", "lib-find");
    const inp = el("input");
    inp.type = "text";
    inp.dataset.lib = "source";
    inp.placeholder = t("owner/repo, a GitHub or GitLab link, any git repository's address, or a folder on this computer");
    inp.spellcheck = false;
    inp.autocomplete = "off";
    inp.value = probe?.source || "";
    const go = button(probing ? t("Looking…") : t("Find skills"), "action", () => find());
    go.disabled = probing;
    if (probing) go.classList.add("busy");
    inp.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") find(); else if (e.key === "Escape" && probe) { probe = null; render(); } };
    const g = glyph(GLYPH.search, "lib-mini");
    const mk = button(t("New skill"), "", () => newSkill());
    mk.dataset.lib = "new-skill";
    line.append(g, inp, go, mk);
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
    // skills too big to install on their own, left out of the list (#2062)
    const left = (p.found.left || []).map((x) => el("div", "lib-none lib-left", t("{name} is too big to install: {size}, over the {limit} a skill may be", { name: x.path.split("/").pop() || p.found.source, size: Math.round(x.size / 1048576) + " MB", limit: "200 MB" })));
    if (!c.length) { box.append(...(left.length ? left : [el("div", "lib-none", t("No skills there — a skill is a folder with a SKILL.md in it."))])); return box; }
    const head = el("div", "lib-probehead");
    const src = p.found.kind === "github" ? "github.com/" + p.found.source.replace(/^https?:\/\/github\.com\//, "")
      : p.found.kind === "git" ? p.found.source.replace(/^https:\/\//, "") : tilde(p.found.source);
    head.append(el("span", "note", c.length === 1 ? t("1 skill in {src}", { src }) : t("{n} skills in {src}", { n: c.length, src })));
    head.append(el("span", "grow"));
    const fresh = c.filter((x) => !x.have);
    if (fresh.length > 1) {
      const allOn = fresh.every((x) => p.pick.has(x.path));
      head.append(button(allOn ? t("Select none") : t("Select all"), "", () => { for (const x of fresh) allOn ? p.pick.delete(x.path) : p.pick.add(x.path); render(); }));
    }
    box.append(head, ...left);
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
        // another skill from elsewhere has its name (mora1n on Discord):
        // this one is in the library, or goes in beside it, by its own
        if (x.have && x.as) r.append(tag(t("In the library as {name}", { name: x.as }), "lib-as", t("In the library as {name}", { name: x.as })));
        else if (x.have) r.append(tag(t("in the library"), ""));
        // the library's by that name came from a folder (biubiu8802 on
        // X): this one takes its place, and it follows GitHub from then on
        else if (x.links) r.append(tag(t("links to GitHub"), "lib-as", t("The library's {name} came from a folder. This one takes its place and it updates from GitHub from then on; files of its own that differ are kept with the backups.", { name: x.name })));
        else if (x.as) r.append(tag(t("adds as {name}", { name: x.as }), "lib-as", t("The library has another skill called {name}, from elsewhere. This one goes in beside it as {as}.", { name: x.name, as: x.as })));
        list.append(r);
      }
    };
    drawList();
    box.append(list);
    const foot = el("div", "lib-probefoot");
    const agentsBox = el("div");
    const drawAgents = () => agentsBox.replaceChildren(agentChips(skillAgents(), p.agents, (n) => { p.agents = n; drawAgents(); }, { names: false, all: true }));
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
    const row = el("div", "row lib-row click lib-skill" + (s.missing ? " missing" : "") + (s.source ? " src" : "") + (s.offHere ? " offhere" : ""));
    const who = el("div", "who");
    const nm = el("div", "name", s.name);
    const c = s.check;
    const gone = isGone(s);
    if (gone) {
      // its source no longer has it: kept till the user takes it out
      const repo = gitRepoOf(s) || skillRepo(s);
      nm.append(tag(t("Removed upstream"), "warn lib-gone", repo
        ? t("{repo} no longer has this skill: it was removed there. It stays here until you remove it.", { repo })
        : t("Its source no longer has this skill: it was removed there. It stays here until you remove it.")));
    } else if (c?.status === "update") {
      const b = tag(t("Update available"), "lib-new", t("Changed on GitHub since it was installed") + "\n" + checkLine(c));
      if (c.url) {
        b.classList.add("link");
        b.onclick = (e) => { e.stopPropagation(); browse(c.url); };
      }
      nm.append(b);
    } else if (c?.status === "unknown") {
      const b = tag(t("Not checked"), "lib-unchecked", checkError(c));
      // GitHub's limit without a token: the tag leads to where one is
      // added, Settings › Network and sharing's GitHub row
      if (c.limited && !c.limited.token) {
        b.classList.add("link");
        b.onclick = (e) => { e.stopPropagation(); openSettingsAt(e, "network", "#githubList .row"); };
      }
      nm.append(b);
    }
    // changed here since it was fetched from GitHub (#1449): an update asks
    // before it replaces that
    if (s.edited) nm.append(tag(t("Changed here"), "warn lib-edited", gitRepoOf(s)
      ? t("You changed it since it was fetched from {repo}. Updating asks before it replaces your changes.", { repo: gitRepoOf(s) })
      : t("You changed it since it was fetched from GitHub. Updating asks before it replaces your changes.")));
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
    if (s.manual) nm.append(manualTag(s.name));
    if (lib.skillRouter && s.router === "direct" && !s.manual) nm.append(tag(t("Not through the router"), "lib-direct", t("Each agent is given it as its own skill, as with the skill router off.")));
    // turned off on this computer alone: no agent here gets it, the ticks
    // stay what a sync carries to the others, and a sync doesn't turn it
    // on here again. A line of its own: a narrow row's name has no room
    let hereLine = null;
    if (s.offHere) {
      const b = tag(t("Off on this computer"), "link lib-offhere", t("No agent on this computer gets it. Your other computers keep their own choice, and a sync doesn't turn it on here. Click to turn it on here."));
      b.setAttribute("role", "button");
      b.tabIndex = 0;
      b.onclick = (e) => { e.stopPropagation(); change("skills/here", { name: s.name, on: true }, t("{name} is on on this computer", { name: s.name })); };
      b.onkeydown = (e) => {
        if (e.key !== "Enter" && e.key !== " ") return;
        e.preventDefault(); e.stopPropagation(); b.click();
      };
      hereLine = el("div", "lib-hereline");
      hereLine.append(b);
    }
    who.append(nm);
    if (hereLine) who.append(hereLine);
    const sub = el("div", "sub", s.missing ? t("Its folder is gone from the library") : s.description || "");
    sub.title = s.description || "";
    who.append(s.missing ? sub : noteSub(s.note, sub, s.description || ""));
    if (s.source) {
      const src = el("div", "lib-src");
      if (s.kind === "github") {
        const a = el("a", "lib-srclink", s.source.replace(/^https:\/\/github\.com\//, ""));
        a.href = s.source;
        a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); browse(s.source); };
        src.append(a);
      } else if (s.kind === "git" && /^https?:\/\//.test(s.source)) {
        const a = el("a", "lib-srclink", s.source.replace(/^https:\/\//, ""));
        a.href = s.source;
        a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); browse(s.source); };
        src.append(a);
      } else if (s.kind === "git") {
        // an SSH address (git@host:path) or a file:// one: no page to open
        const a = el("span", "lib-srclink", s.source);
        a.title = s.source;
        src.append(a);
      } else {
        src.append(el("span", "", t("linked from")), pathLink(s.source));
      }
      // kept in ~/.agents/skills, which some agents read themselves: moved
      // into the library, each of them can be turned off (tnt1223)
      if (s.shared) src.append(handOverButton([s]));
      who.append(src);
    }
    const acts = el("div", "lib-rowacts");
    // one its source no longer has has nothing to update from: Update all,
    // or a check, finds it there again if it comes back
    if (!gone && (s.kind === "github" || s.kind === "git" || s.origin)) {
      const u = button("", "lib-icon", async (e, b) => {
        if (s.edited) return confirmUpdateEdited(s);
        // spinning, with the row's actions kept in view while the fetch
        // runs; then what came of it: updated, nothing new, or the error
        b.classList.add("busy");
        b.parentElement?.classList.add("busy");
        await change("skills/update", { name: s.name }, (res) => res?.current?.includes(s.name)
          ? t("{name} is already up to date", { name: s.name }) : t("{name} is updated", { name: s.name }));
        b.classList.remove("busy");
        b.parentElement?.classList.remove("busy");
      });
      u.append(svg(GLYPH.up, 13, 1.5));
      const gitRepo = gitRepoOf(s);
      u.title = s.origin ? t("Update from GitHub ({repo}, as CC Switch installed it)", { repo: s.origin.replace(/^https:\/\/github\.com\//, "") })
        : gitRepo ? t("Update from {repo}", { repo: gitRepo }) : t("Update from GitHub");
      if (c?.status === "current") u.title += "\n" + (gitRepo ? t("Up to date with {repo}", { repo: gitRepo }) : t("Up to date with GitHub")) + "\n" + checkLine(c);
      else if (c?.status === "update") u.title += "\n" + checkLine(c);
      if (s.edited) u.title += "\n" + t("Changed here: asks before it replaces your changes");
      acts.append(u);
    }
    // with the skill router on: kept out of it, each agent's own skill
    // (to type it as /name, or keep it in the prompt); one only for the
    // user to type is kept out anyway
    if (lib.skillRouter && !s.manual) {
      const direct = s.router === "direct";
      const r = button("", "lib-icon lib-routedirect" + (direct ? " on" : ""), () => change("skills/router-direct", { name: s.name, on: !direct },
        direct ? t("{name} is listed in the skill router", { name: s.name }) : t("{name} is given to each agent as its own skill", { name: s.name })));
      r.append(svg(GLYPH.out, 13, 1.5));
      r.title = direct ? t("List it in the skill router") : t("Keep it out of the skill router: each agent is given it as its own skill, to type it as /{name}", { name: s.name });
      r.setAttribute("aria-pressed", direct ? "true" : "false");
      acts.append(r);
    }
    const here = button("", "lib-icon lib-here" + (s.offHere ? " on" : ""), () => change("skills/here", { name: s.name, on: !!s.offHere },
      s.offHere ? t("{name} is on on this computer", { name: s.name }) : t("{name} is off on this computer", { name: s.name })));
    here.append(svg(GLYPH.power, 13, 1.5));
    here.title = s.offHere ? t("Turn on on this computer") : t("Turn off on this computer only: your other computers keep it");
    here.setAttribute("aria-pressed", s.offHere ? "true" : "false");
    acts.append(here);
    const rm = button("", "lib-icon danger", () => confirmRemoveSkill(s));
    rm.append(svg(GLYPH.trash, 13, 1.4));
    rm.title = t("Remove");
    acts.append(rm);
    row.append(mark(s.icon, GLYPH.skill), who, acts, agentChips(withGone(all, s.agents), s.agents, chipsChange("skills/agents", s.name, "skills", (x) => skillRow(x, all)), { problems: s.problems, via: viaFor(s), all: true, always: alwaysFor(s) }));
    if (picking) return pickRow(row, s.name);
    row.onclick = () => viewSkill(s);
    row.title = t("Read {name}'s SKILL.md", { name: s.name });
    return row;
  }

  // A skill its source no longer has (スライム on Discord): an update or a
  // check read the repository and found it removed there. Nothing takes
  // it out by itself; Clean up takes out every such skill of a group at
  // once, asking first, and a row's Remove takes out one.
  const isGone = (s) => !!s.gone || s.check?.status === "gone";
  function goneSaid(names) {
    return names.length === 1 ? t("{name} was removed from its source", { name: names[0] })
      : t("{n} skills were removed from their sources: {names}", { n: names.length, names: names.join(", ") });
  }
  function cleanUpButton(skills) {
    const b = button(t("Clean up {n}", { n: skills.length }), "action lib-updall lib-cleanup", () => confirmRemoveGone(skills));
    b.title = skills.length === 1 ? t("Remove {name}: its source no longer has it", { name: skills[0].name })
      : t("Remove the {n} skills their source no longer has", { n: skills.length });
    return b;
  }
  function confirmRemoveGone(skills) {
    const names = skills.map((s) => s.name), n = names.length;
    const repos = [...new Set(skills.map((s) => gitRepoOf(s) || skillRepo(s)).filter(Boolean))];
    const ed = el("div", "editor lib-editor lib-remove-gone");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", n === 1 ? t("Remove {name}?", { name: names[0] }) : t("Remove {n} skills removed upstream?", { n })));
    ed.append(head);
    ed.append(el("p", "lib-confirm", repos.length === 1
      ? (n === 1 ? t("{repo} no longer has this skill: it was removed there.", { repo: repos[0] }) : t("{repo} no longer has these skills: they were removed there.", { repo: repos[0] }))
      : t("Their sources no longer have these skills: they were removed there.")));
    // each by name, and one changed here says so: removing it takes the change out too
    const list = el("ul", "lib-gonelist");
    for (const s of skills) {
      const li = el("li", "", s.name);
      if (s.edited) li.append(tag(t("Changed here"), "warn lib-edited", t("You changed it here since it was fetched.")));
      list.append(li);
    }
    ed.append(list);
    ed.append(el("p", "lib-confirm", takenFrom(skills) + " " + t("Their folders are moved to magpie's backups (Backups, at the top of the Library), not deleted.")));
    const bar = el("div", "bar");
    const go = button(n === 1 ? t("Remove") : t("Remove {n}", { n }), "primary danger-fill", async () => {
      go.disabled = true;
      if (await removeGone(names)) closeLibModal(true);
      else go.disabled = false;
    });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }
  // only those still marked removed from their source are taken out; one
  // found there again meanwhile stays, and is said
  async function removeGone(names) {
    try {
      const v = await api("library/skills/remove-gone", { names });
      take(v);
      const res = v.result || {}, no = res.unremoved || [];
      const out = names.filter((x) => !no.some((p) => p.what === "skill:" + x));
      const done = out.length === 1 ? t("{name} removed", { name: out[0] }) : t("{n} skills removed", { n: out.length });
      if (no.length) status((out.length ? done + " · " : "") + t("{name} wasn't removed: {error}", { name: no[0].what.replace(/^skill:/, ""), error: no[0].error }) + (no.length > 1 ? " " + t("(and {n} more)", { n: no.length - 1 }) : ""), "warn", 10000);
      else report(res, done);
      render();
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      return false;
    }
  }

  // An update of a skill changed here replaces the change (#1449): asked in
  // the page, and the version replaced is kept with the backups.
  function confirmUpdateEdited(s) {
    const ed = el("div", "editor lib-editor lib-update-edited");
    const head = el("div", "ehead");
    const repo = gitRepoOf(s);
    head.append(glyph(GLYPH.up), el("b", "", repo ? t("Replace your changes to {name} with the version in {repo}?", { name: s.name, repo }) : t("Replace your changes to {name}?", { name: s.name })));
    ed.append(head);
    ed.append(el("p", "lib-confirm", repo
      ? t("You changed {name} here since it was fetched from {repo}. Updating puts the version there in its place. Yours is kept in magpie's backups (Backups, at the top of the Library).", { name: s.name, repo })
      : t("You changed {name} here since it was fetched from GitHub. Updating puts GitHub's version in its place. Yours is kept in magpie's backups (Backups, at the top of the Library).", { name: s.name })));
    const bar = el("div", "bar");
    const go = button(t("Update"), "primary", async () => {
      go.disabled = true;
      if (await change("skills/update", { name: s.name, replace: true }, t("{name} is up to date; your version is in the backups", { name: s.name }))) closeLibModal(true);
      else go.disabled = false;
    });
    bar.append(el("span", "grow"), button(t("Keep mine"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  // Skills kept in ~/.agents/skills (tnt1223 on Discord): agents that read
  // that folder themselves have each whatever is ticked, so none can be
  // turned off for one of them. Manage in magpie moves them into the
  // library's own folder, asking first and saying where they go; the
  // agents that read them there are ticked, and keep them till unticked.
  function handOverButton(skills) {
    const one = skills.length === 1;
    const b = button(one ? t("Manage in magpie") : t("Manage {n} in magpie", { n: skills.length }), one ? "lib-handover" : "action lib-updall lib-handover", (e) => {
      e.stopPropagation();
      confirmHandOver(skills);
    });
    b.title = one ? t("Move it out of ~/.agents/skills into magpie's library, so you choose which agents get it")
      : t("Move these {n} from ~/.agents/skills into magpie's library, so you choose which agents get them", { n: skills.length });
    return b;
  }

  function confirmHandOver(skills) {
    const one = skills.length === 1, s = skills[0];
    const ed = el("div", "editor lib-editor lib-handover-ask");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.skill), el("b", "", one ? t("Manage {name} in magpie?", { name: s.name }) : t("Manage {n} skills in magpie?", { n: skills.length })));
    ed.append(head);
    const readers = [...new Set(skills.flatMap((x) => x.always || []))].map(nameOf).filter(Boolean).join(", ");
    // the entry only links to a folder elsewhere: only the link goes
    ed.append(el("p", "lib-confirm", one
      ? (s.sharedElsewhere ? t("Its entry in ~/.agents/skills, a link to {path}, is taken away. The folder stays where it is, and the library links to it.", { path: tilde(s.source) })
        : t("Its folder moves from {from} into magpie's library folder, {to}.", { from: tilde(s.shared), to: tilde(libSkillPath(s.name)) }))
      : t("Their folders move from ~/.agents/skills into magpie's library folder, {to}. One that only links there to a folder elsewhere keeps that folder; only the link goes.", { to: tilde(libSkillPath("")) })));
    if (readers) ed.append(el("p", "lib-confirm", one
      ? t("{agents} read ~/.agents/skills themselves. They keep it, ticked here, and you can turn it off for any of them, or on this computer only.", { agents: readers })
      : t("{agents} read ~/.agents/skills themselves. They keep them, ticked here, and you can turn each off for any of them, or on this computer only.", { agents: readers })));
    if (lib.sharedSkills) ed.append(el("p", "lib-confirm", t("Every skill is kept in ~/.agents/skills now, which can't go with choosing agents. That setting goes off: magpie's links there are taken away, your own entries stay.")));
    ed.append(el("p", "lib-confirm lib-muted", one
      ? t("To put it back: {cmd}", { cmd: "magpie library skill hand-back " + s.name })
      : t("To put one back: {cmd}", { cmd: "magpie library skill hand-back <name>" })));
    const bar = el("div", "bar");
    const go = button(t("Move into the library"), "primary", async () => {
      go.disabled = true;
      if (await handOverSkills(skills.map((x) => x.name))) closeLibModal(true);
      else go.disabled = false;
    });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  // where the library keeps a skill's folder: skills/<name> in its own
  function libSkillPath(name) {
    const dir = lib.dir || "", sep = dir.includes("\\") ? "\\" : "/";
    return dir + sep + "skills" + (name ? sep + name : "");
  }

  async function handOverSkills(names) {
    if (changing) return false;
    changing = true;
    page.setAttribute("aria-busy", "true");
    status(t("Saving…"), "", 120000);
    try {
      const v = await api("library/skills/hand-over", { names });
      take(v);
      status("");
      const res = v.result || {}, no = res.unmoved || [];
      const moved = names.filter((x) => !no.some((p) => p.what === "skill:" + x));
      const done = moved.length === 1 ? t("{name} is in magpie's library now", { name: moved[0] }) : t("{n} skills are in magpie's library now", { n: moved.length });
      if (no.length) status((moved.length ? done + " · " : "") + t("{name} wasn't moved: {error}", { name: no[0].what.replace(/^skill:/, ""), error: no[0].error }) + (no.length > 1 ? " " + t("(and {n} more)", { n: no.length - 1 }) : ""), "warn", 10000);
      else report(res, done);
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      return false;
    } finally {
      changing = false;
      page.removeAttribute("aria-busy");
      render();
    }
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

  // A skill's files, read and edited here (refeccd on Discord: skills
  // handed over from ~/.agents/skills had no other place to be edited).
  // Markdown is shown as it reads, its front matter as rows; Save writes
  // the library's folder, so every agent given the skill gets it.
  async function viewSkill(s, file = "") {
    const ed = el("div", "editor lib-editor lib-reader lib-skillfile");
    const head = el("div", "ehead");
    const pick = el("button", "lib-filepick");
    pick.type = "button";
    pick.hidden = true;
    head.append(mark(s.icon, GLYPH.skill), el("b", "", s.name), pick, el("span", "grow"));
    const body = el("div", "lib-skillbody");
    const bar = el("div", "bar");
    // the user's note on it (youyo.p_10001 on Discord), over its files:
    // the library's own, never in a file an agent reads. Save shows once
    // it's changed, and saves the file being edited with it
    let was = s.note || "", note = was;
    const noteDirty = () => note.trim() !== was;
    const keep = button(t("Save"), "primary", () => save());
    keep.hidden = true;
    const noteIn = noteBox(was, t("e.g. For the weekly report; the other PDF skill is for forms"), (v) => { note = v; keep.hidden = !!ta || !noteDirty(); });
    const noteRow = el("div", "lib-skillnote");
    noteRow.append(...field(t("Note"), noteIn, noteHint()));
    ed.append(head, noteRow, body, bar);
    let cur = null, ta = null;
    const lf = (x) => x.replace(/\r\n?/g, "\n");
    const fileDirty = () => !!(ta && cur && ta.value !== lf(cur.text));
    modal = { save: () => save(), dirty: () => fileDirty() || noteDirty() };
    const saveNote = async () => {
      if (!noteDirty()) return true;
      if (!(await change("skills/note", { name: s.name, note }, t("{name} saved", { name: s.name })))) return false;
      was = note = note.trim();
      keep.hidden = true;
      return true;
    };
    const read = async (f) => {
      body.replaceChildren(el("pre", "lib-md", "…"));
      try {
        cur = await api("library/skill?name=" + encodeURIComponent(s.name) + (f ? "&file=" + encodeURIComponent(f) : ""));
        show();
      } catch (e) {
        body.replaceChildren(el("pre", "lib-md", e.message));
        bar.replaceChildren(el("span", "grow"), button(t("Close"), "", closeLibModal));
      }
    };
    const swap = async (f) => {
      if (f === cur?.file || fileDirty() && !(await confirmDiscard())) return;
      ta = null;
      read(f);
    };
    pick.onclick = (e) => {
      e.stopPropagation();
      openProtoMenu(pick, cur.files.map((f) => ({ v: f, name: f, literalName: true, note: "" })), cur.file, swap, "Files");
    };
    const show = () => {
      ta = null;
      pick.hidden = !(cur.files?.length > 1);
      pick.replaceChildren(el("span", "", cur.file), svg(CHEV, 10, 1.7));
      body.replaceChildren(rendered(cur));
      const where = el("span", "note");
      where.append(pathLink(cur.path));
      keep.hidden = !noteDirty();
      bar.replaceChildren(where, el("span", "grow"), button(t("Edit"), "", edit), button(t("Close"), "", closeLibModal), keep);
    };
    const edit = () => {
      ta = el("textarea", "lib-text lib-skilltext");
      ta.spellcheck = false;
      ta.value = lf(cur.text);
      ta.onkeydown = (e) => {
        if (e.isComposing || e.keyCode === 229) return;
        if (e.key === "Escape") return;
        e.stopPropagation();
        if ((e.metaKey || e.ctrlKey) && e.key === "s") { e.preventDefault(); save(); }
      };
      body.replaceChildren(ta);
      const note = el("span", "note lib-reaches", t("Saved in the library: every agent given {name} gets it", { name: s.name }) + " · " + (/^Mac/.test(navigator.platform) ? "⌘S" : "Ctrl+S"));
      bar.replaceChildren(note, el("span", "grow"), button(t("Cancel"), "", async () => { if (!fileDirty() || await confirmDiscard()) show(); }), button(t("Save"), "primary", save));
      ta.focus({ preventScroll: true });
      ta.setSelectionRange(0, 0);
    };
    const save = async () => {
      if (!(await saveNote()) || !ta) return;
      if (!fileDirty()) return show();
      const f = cur.file;
      if (await change("skills/save", { name: s.name, file: f, text: ta.value, was: cur.text }, t("{name} saved", { name: f }))) {
        ta = null;
        read(f);
      }
    };
    openLib(ed);
    read(file);
  }
  // a skill's file as it reads: Markdown drawn, front matter as rows
  // (name, description), anything else as its text
  function rendered(f) {
    if (!/\.(md|markdown)$/i.test(f.file) || !window.mdRender) return el("pre", "lib-md", f.text);
    const text = f.text.replace(/\r\n?/g, "\n");
    const box = el("div", "lib-mdview");
    const m = text.match(/^---\n([\s\S]*?)\n---[ \t]*(?:\n|$)/);
    if (m) {
      const meta = [];
      for (const line of m[1].split("\n")) {
        const kv = line.match(/^([\w.-]+):\s*(.*)$/);
        if (kv) meta.push([kv[1], kv[2]]);
        else if (meta.length && line.trim()) meta[meta.length - 1][1] += " " + line.trim();
      }
      const dl = el("dl", "lib-fm");
      for (const [k, v] of meta) dl.append(el("dt", "", k), el("dd", "", v.replace(/^[>|][-+]?\s*/, "").trim().replace(/^(["'])(.*)\1$/, "$2")));
      if (meta.length) box.append(dl);
    }
    box.append(window.mdRender(m ? text.slice(m[0].length) : text));
    return box;
  }


  // its SKILL.md sets disable-model-invocation (sunset3279 on Discord):
  // the model doesn't use it on its own, only a /name the user types
  function manualTag(name) {
    return tag(t("Manual only"), "lib-manual", t("Its SKILL.md sets disable-model-invocation: Claude Code runs it only when you type /{name}, never on its own.", { name }));
  }

  // What the agents have and the library doesn't is a list to go through,
  // not the page: a long one shows its first few rows and how many more
  // there are, so it doesn't push everything under it a screen down
  const FOUND_SHOWN = 5;
  const foundOpen = new Set(); // "skills", "mcp": shown whole, for this session
  function foldedFound(kind, rows, whole) {
    const list = el("div", "list lib-list");
    // two or fewer hidden aren't worth a click
    const folds = !whole && rows.length > FOUND_SHOWN + 2;
    const open = !folds || foundOpen.has(kind);
    list.append(...(open ? rows : rows.slice(0, FOUND_SHOWN)));
    if (!folds) return list;
    const more = el("button", "lib-foundmore");
    more.type = "button";
    const chev = el("span", "chev");
    chev.append(svg(CHEV, 10, 1.8));
    more.append(el("span", "", open ? t("Show less") : t("Show {n} more", { n: rows.length - FOUND_SHOWN })), chev);
    more.classList.toggle("open", open);
    more.setAttribute("aria-expanded", String(open));
    more.onclick = () => {
      if (open) foundOpen.delete(kind); else foundOpen.add(kind);
      render();
    };
    list.append(more);
    return list;
  }

  function foundSkillRow(f) {
    const row = el("div", "row lib-row lib-found");
    const who = el("div", "who");
    const nm = el("div", "name", f.name);
    if (f.manual) nm.append(manualTag(f.name));
    who.append(nm);
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
    // gone from the agents without bringing it in first (#1303)
    const rm = button("", "lib-icon danger", () => confirmRemoveFoundSkill(f));
    rm.append(svg(GLYPH.trash, 13, 1.4));
    rm.title = t("Remove");
    // picking, a row is its box, as Finder's are in a selection: its own
    // buttons give way to the bar's
    if (pickFound) return foundPickRow(row, f.name);
    row.append(b, rm);
    return row;
  }

  // ---------- some found skills, picked, out of the agents at once ----------

  function endFoundPick() { pickFound = false; foundPicked.clear(); foundErrs.clear(); }

  function foundPickButton() {
    const b = button(pickFound ? t("Done") : t("Select"), "action lib-updall lib-select lib-foundselect", () => {
      const on = !pickFound;
      endFoundPick();
      pickFound = on;
      render();
    });
    b.dataset.alt = pickFound ? t("Select") : t("Done");
    b.title = pickFound ? t("Stop picking skills") : t("Pick some of these skills to take them out of your agents together");
    // one Select at a time: ending the library's from here would take its
    // bar away above this button, moving it from under the pointer
    if (picking === "skills" && !pickFound) {
      b.disabled = true;
      b.title = t("Done picking the library's skills first");
    }
    return b;
  }

  function foundPickRow(row, name) {
    const box = pickBox([name], foundPicked, syncFoundPicks);
    row.prepend(box);
    row.classList.toggle("picked", foundPicked.has(name));
    row.onclick = () => box.click();
    row.title = t("Select {name}", { name });
    const why = foundErrs.get(name), sub = why && row.querySelector(":scope > .who > .sub");
    if (sub) {
      row.classList.add("lib-pickfailed");
      sub.className = "sub lib-pickerr";
      sub.textContent = sub.title = t("Not removed: {error}", { error: why });
    }
    return row;
  }

  function syncFoundPicks() {
    for (const b of page.querySelectorAll(".lib-found .lib-pickbox, .lib-foundbar .lib-pickbox")) paintPick(b);
    const bar = page.querySelector(".lib-foundbar");
    if (bar) bar.replaceWith(foundPickBar());
  }

  // its bar: all of them, how many are picked, Remove…, Clear and Done
  function foundPickBar() {
    const bar = el("div", "row lib-row lib-pickbar lib-foundbar");
    const all = lib.foundSkills, some = all.filter((f) => foundPicked.has(f.name)), n = some.length;
    const every = el("label", "lib-pickall");
    every.append(pickBox(all.map((f) => f.name), foundPicked, syncFoundPicks), el("span", "", t("All {n}", { n: all.length })));
    every.onclick = (e) => e.stopPropagation();
    bar.append(every, el("span", "lib-pickn" + (n ? "" : " none"), n ? t("{n} selected", { n }) : t("Pick skills to take them out of your agents together")), el("span", "grow"));
    if (n) {
      const rm = button(t("Remove…"), "action danger lib-updall lib-pickrm", () => confirmRemoveFoundSome(some));
      rm.title = t("Take the skills picked out of your agents");
      const c = button(t("Clear"), "lib-updall lib-pickclear", () => { foundPicked.clear(); syncFoundPicks(); });
      c.title = t("Unpick them all");
      bar.append(rm, c);
    }
    bar.append(button(t("Done"), "action lib-updall lib-pickdone", () => { endFoundPick(); render(); }));
    return bar;
  }

  // asked first, as a row's Remove asks: one alone is said as its row's
  // sheet says it; several, how many and where they go
  function confirmRemoveFoundSome(some) {
    const names = some.map((f) => f.name), n = names.length;
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", n === 1 ? t("Remove {name}?", { name: names[0] }) : t("Remove {n} skills?", { n })));
    ed.append(head);
    const lines = [];
    if (n === 1) {
      const f = some[0], agents = [...f.agents, ...(f.copies || [])].map(nameOf).filter(Boolean);
      lines.push(!agents.length ? t("Its folder is moved to magpie's backups.")
        : f.link ? t("It is taken out of {agents}. The folder it was linked from stays where it is.", { agents: agents.join(", ") })
        : t("It is taken out of {agents}, and its folder is moved to magpie's backups.", { agents: agents.join(", ") }));
      if (f.shared) lines.push(t("Its entry in {path} goes to the backups too, so no agent reads it from there.", { path: f.shared }));
      if (f.others?.length) lines.push(t("{agents} has another skill by this name; that one stays.", { agents: f.others.map(nameOf).join(", ") }));
    } else {
      const agents = [...new Set(some.flatMap((f) => [...f.agents, ...(f.copies || [])]))].map(nameOf).filter(Boolean);
      if (agents.length) lines.push(t("They are taken out of {agents}.", { agents: agents.join(", ") }));
      const linked = some.filter((f) => f.link).length, kept = n - linked, shared = some.filter((f) => f.shared).length;
      const where = [];
      if (kept) where.push(t("{n} folders are moved to magpie's backups (Backups, at the top of the Library), not deleted.", { n: kept }));
      if (linked) where.push(t("{n} linked from folders of your own are only unlinked: those folders stay where they are.", { n: linked }));
      lines.push(where.join(" "));
      if (shared) lines.push(t("{n} have an entry in the shared skills folder, which goes to the backups too, so no agent reads them from there.", { n: shared }));
      if (some.some((f) => f.others?.length)) lines.push(t("Another agent's own skill by one of these names stays."));
    }
    for (const l of lines) if (l) ed.append(el("p", "lib-confirm", l));
    const bar = el("div", "bar");
    const go = button(n === 1 ? t("Remove") : t("Remove {n}", { n }), "primary danger-fill", async () => { go.disabled = true; if (await removeFoundSome(names)) closeLibModal(true); else go.disabled = false; });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  // as the library's Remove… (#1852): the ones taken out leave the list and
  // the picks; one that couldn't be stays picked and says why on its row;
  // none taken out (the change refused) leaves the sheet open with why
  async function removeFoundSome(names) {
    const what = (p) => p.what.replace(/^skill:/, "");
    try {
      const v = await api("library/skills/remove-found-some", { names });
      take(v);
      const res = v.result || {}, no = res.unremoved || [];
      foundErrs.clear();
      for (const p of no) foundErrs.set(what(p), p.error);
      const gone = names.filter((x) => !foundErrs.has(x));
      for (const x of gone) foundPicked.delete(x);
      const done = gone.length === 1 ? t("{name} removed", { name: gone[0] }) : t("{n} skills removed", { n: gone.length });
      if (no.length) status((gone.length ? done + " · " : "") + t("{name} wasn't removed: {error}", { name: what(no[0]), error: no[0].error }) + (no.length > 1 ? " " + t("(and {n} more)", { n: no.length - 1 }) : ""), "warn", 10000);
      else report(res, done);
      render();
      return true;
    } catch (e) {
      status(e.message, "err", 6000);
      foundErrs.clear();
      if (names.length === 1) foundErrs.set(names[0], e.message);
      render();
      return false;
    }
  }

  function confirmRemoveFoundSkill(f) {
    const ed = el("div", "editor lib-editor");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", t("Remove {name}?", { name: f.name })));
    ed.append(head);
    const agents = [...f.agents, ...(f.copies || [])].map(nameOf).filter(Boolean);
    ed.append(el("p", "lib-confirm", !agents.length ? t("Its folder is moved to magpie's backups.")
      : f.link ? t("It is taken out of {agents}. The folder it was linked from stays where it is.", { agents: agents.join(", ") })
      : t("It is taken out of {agents}, and its folder is moved to magpie's backups.", { agents: agents.join(", ") })));
    if (f.shared) ed.append(el("p", "lib-confirm", t("Its entry in {path} goes to the backups too, so no agent reads it from there.", { path: f.shared })));
    if (f.others?.length) ed.append(el("p", "lib-confirm", t("{agents} has another skill by this name; that one stays.", { agents: f.others.map(nameOf).join(", ") })));
    const bar = el("div", "bar");
    const go = button(t("Remove"), "primary danger-fill", async () => { if (await change("skills/remove-found", { name: f.name }, t("{name} removed", { name: f.name }))) closeLibModal(true); });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
  }

  // ---------- the market ----------

  // What the market offers: popular and featured ones until the reader
  // searches. Kept across renders; asked for the first time a tab shows it.
  const market = {
    mcp: { q: "", items: null, error: "", loading: false, seq: 0, timer: 0, custom: null, shown: 0 },
    skills: { q: "", items: null, error: "", loading: false, seq: 0, timer: 0, shown: 0 },
    pi: { q: "", items: null, error: "", loading: false, seq: 0, timer: 0, shown: 0 },
  };
  // how many cards a list shows at first, and each Show more adds
  const MARKET_PAGE = 60;
  const asked = new Set(); // skills whose description has been asked for

  async function fetchMarket(kind) {
    const m = market[kind];
    const seq = ++m.seq;
    m.loading = true;
    drawMarket(kind);
    try {
      const r = await api("library/market/" + ({ mcp: "servers", skills: "skills", pi: "pi" })[kind] + "?q=" + encodeURIComponent(m.q.trim()));
      if (seq !== m.seq) return;
      m.items = r.items || [];
      m.shown = MARKET_PAGE;
      m.error = r.error || "";
      // the source that didn't answer when the rest did (#1589)
      m.failed = r.failed ? { source: r.failed, cause: r.cause || "" } : null;
      // a search that is an address nothing listed is at: the server
      // there, to add by hand
      m.custom = r.custom || null;
    } catch (e) {
      if (seq !== m.seq) return;
      m.items = m.items || [];
      m.custom = null;
      m.failed = null;
      m.error = e.message;
    }
    m.loading = false;
    drawMarket(kind);
    if (kind === "skills") describeSkills();
    if (kind === "pi") return;
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
    inp.placeholder = kind === "mcp" ? t("Search MCP servers — GitHub, Postgres, Figma…") : kind === "pi" ? t("Search Pi packages on npm — mcp, web, subagents…") : t("Search skills on skills.sh — react, pdf, testing…");
    inp.spellcheck = false;
    inp.autocomplete = "off";
    inp.value = m.q;
    inp.oninput = () => {
      m.q = inp.value;
      clearTimeout(m.timer);
      const q = m.q.trim();
      if (kind !== "mcp" && q.length === 1) return;
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
      : kind === "mcp" ? t("Featured, and the official MCP Registry") : kind === "pi" ? t("Most downloaded on npm") : t("Popular on skills.sh");
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
    // one source not answering while the others did is a short list, not
    // a failed search: it says which one, over what the rest found (#1589)
    if (m.error && m.failed && m.items.length) {
      grid.append(el("div", "mk-msg mk-partial", kind === "mcp"
        ? t("The MCP Registry didn't answer ({error}), so these are only magpie's featured servers that match.", { error: m.failed.cause })
        : kind === "pi" ? t("npm's search didn't answer ({error}), so these are only the matches among the most downloaded packages.", { error: m.failed.cause })
        : t("skills.sh's search didn't answer ({error}), so these are only the matches among magpie's picks and the most installed skills.", { error: m.failed.cause })));
    } else if (m.error) grid.append(el("div", "mk-msg err", m.error));
    if (kind === "mcp" && q && m.custom) grid.append(customAt(q, m.custom, m.items.length));
    else if (!m.items.length && !m.error) {
      grid.append(el("div", "mk-msg", q ? t("Nothing matches “{q}”.", { q }) : t("Nothing to show.")));
      return;
    }
    for (const x of m.items.slice(0, m.shown)) grid.append(cardOf(kind, x));
    moreCards(kind, grid);
  }

  const cardOf = (kind, x) => kind === "mcp" ? serverCard(x) : kind === "pi" ? piCard(x) : skillCard(x);

  // The rest of a long list, a page at a time: the cards added under the
  // ones shown, which stay where they are.
  function moreCards(kind, grid) {
    const m = market[kind];
    const left = m.items.length - m.shown;
    if (left <= 0) return;
    const more = button(t("Show more ({n} left)", { n: left.toLocaleString(intlLang()) }), "mk-more", () => {
      const from = m.shown;
      m.shown += MARKET_PAGE;
      more.before(...m.items.slice(from, m.shown).map((x) => cardOf(kind, x)));
      more.remove();
      moreCards(kind, grid);
      if (kind === "skills") describeSkills();
    });
    grid.append(more);
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
    // a first fetch from GitHub can fail once (magpie-community's avatar
    // showed an M on the user's first look), so it's asked once more
    img.onerror = () => {
      if (img.dataset.again) return fallback();
      img.dataset.again = "1";
      setTimeout(() => { if (img.isConnected) img.src = "/api/library/icon?again=1&u=" + encodeURIComponent(url); }, 2000);
    };
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
    if (x.unready) { const u = el("span", "mk-badge mk-unready", t("Not set up yet")); u.title = t(x.unready); meta.append(u); }
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

  // the Library's web search servers: true for one Settings also offers as
  // a search API (provider.SearchVendors)
  const WEB_SEARCHES = { tavily: true, "brave-search": true, exa: true, firecrawl: true, anysearch: false };

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
    if (x.name === "magpie-browser") {
      ed.append(el("p", "mk-hint", t("Pick the agents that may use your browser: they act in your tabs, signed in as you are.")));
      ed.append(browserSetup());
    } else if (x.name === "magpie-web-search") {
      ed.append(el("p", "mk-hint", t("Pick the agents that may search with it: each search goes where Settings → Models → Web search sends it, and costs what that charges.")));
    } else if (x.name in WEB_SEARCHES) {
      // a search server of its own (netube99 on X): the agents call it
      // with its key, beside magpie's web search, which it neither uses nor
      // switches on; one Settings has as a search API is offered there too
      const v = WEB_SEARCHES[x.name];
      const hint = el("p", "mk-hint", t("Agents call it themselves, beside magpie's web search, which doesn't use it.")
        + (v ? " " + t("As a search API in Settings → Models → Web search, {name} searches for every model, with no server to add.", { name: x.title || x.name }) : ""));
      const go = el("button", "link", t("Set up in Settings"));
      go.type = "button";
      go.onclick = async (e) => { if (await closeLibModal()) openSettingsAt(e, "models", "#searchList .row"); };
      hint.append(" ", go);
      ed.append(hint);
    } else if (x.name === "magpie-vision") {
      ed.append(el("p", "mk-hint", t("Pick the agents that may read images with it: each image goes to the Image recognition model set in Settings → Models → Images, and costs what that model charges.")));
    } else if (x.optIn) ed.append(el("p", "mk-hint", t("Pick the agents that may generate images: each image costs what the model set in Settings → Models → Images charges.")));
    // one of magpie's own that can't answer yet says why, and is added all
    // the same: it answers once that is set up, its tools saying how
    if (x.unready) ed.append(el("p", "mk-hint mk-unready", t(x.unready)));
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
    const drawAgents = () => agentsBox.replaceChildren(agentChips(all, agents, (n) => { agents = n; drawAgents(); }, { names: true, blocked: sseBlocked(x), all: true }));
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

  // Magpie Browser's extension: whether it is connected, and setting it up
  // in the user's own browser, where their sign-ins are
  function browserSetup() {
    const box = el("div", "mk-browser");
    const state = el("div", "mk-browser-state");
    // a connected extension older than magpie's: its folder has the new
    // one, but the browser runs the old until it is reloaded
    const update = el("p", "mk-browser-update");
    const steps = el("ol", "mk-browser-steps");
    let away = false;
    const go = button(t("Set up the extension…"), "action", async () => {
      go.disabled = true;
      try {
        if (away) await download();
        else {
          draw(await api("library/browser/setup", {}));
          steps.hidden = false;
        }
      } catch (e) { status(e.message, "err"); }
      go.disabled = false;
    });
    // magpie's folders are out of this browser's reach (another computer,
    // or magpie in a container): the extension comes as a zip, pointed at
    // this page's address with a gateway key of its own
    const download = async () => {
      const r = await fetch("/api/library/browser/extension", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ at: location.origin }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      const url = URL.createObjectURL(await r.blob());
      const a = el("a");
      a.href = url;
      a.download = "Magpie Browser Extension.zip";
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 60000);
      steps.hidden = false;
    };
    const draw = (b) => {
      away = !!b.away;
      state.replaceChildren();
      const dot = el("span", "mk-browser-dot" + (b.connected ? " on" : ""));
      state.append(dot, el("span", "", b.connected
        ? t("Connected to your browser") + (b.extension ? " · " + b.extension : "")
        : away ? t("Not connected: download the extension and load it in your browser")
        : b.written ? t("Not connected: load the folder below in your browser, and keep that browser open") : t("Not set up: set up the extension in Chrome, Edge, Arc, Brave or another Chromium browser")));
      update.hidden = !(b.connected && b.outdated);
      update.textContent = update.hidden ? "" : away
        ? t("Magpie Browser {v} is ready: download the extension again, unzip it over the old folder, and reload Magpie Browser on chrome://extensions (edge://extensions in Edge).", { v: b.version })
        : t("Magpie Browser {v} is ready: reload Magpie Browser on chrome://extensions (edge://extensions in Edge).", { v: b.version });
      go.textContent = away ? t("Download the extension") : b.written ? t("Show the extension folder") : t("Set up the extension…");
      if (away) {
        steps.replaceChildren(
          el("li", "", t("Unzip the download.")),
          el("li", "", t("Open chrome://extensions (edge://extensions in Edge) in the browser you use every day.")),
          el("li", "", t("Turn on Developer mode.")),
          el("li", "", t("Click Load unpacked and choose the unzipped Magpie Browser Extension folder.")),
        );
        folder.hidden = true;
        if (b.connected) steps.hidden = true;
        return;
      }
      steps.replaceChildren(
        el("li", "", t("Open chrome://extensions (edge://extensions in Edge) in the browser you use every day.")),
        el("li", "", t("Turn on Developer mode.")),
        el("li", "", b.drag ? t("Drag the folder below onto that page.")
          : t("Drag the folder from the window that opened onto that page, or click Load unpacked and choose {dir}.", { dir: tilde(b.dir) })),
      );
      folder.title = t("Drag onto chrome://extensions");
      folder.lastChild.textContent = tilde(b.dir);
      folder.dataset.drag = b.drag ? "1" : "";
      if (b.connected) steps.hidden = true;
      else if (b.written) steps.hidden = false;
      folder.hidden = steps.hidden || !b.drag;
    };
    // the folder itself, dragged out of the window as Finder drags it: the
    // mouse that goes down on it and moves hands it to the system's drag
    const folder = el("div", "mk-browser-folder");
    folder.append(svg(GLYPH.folder, 22, 1.2), el("span", "mk-browser-folder-name", "Magpie Browser Extension"), el("span", "mk-browser-folder-path mono"));
    let from = null;
    folder.addEventListener("pointerdown", (e) => { if (e.button === 0) { from = [e.clientX, e.clientY]; e.preventDefault(); } });
    folder.addEventListener("pointermove", (e) => {
      if (!from || Math.hypot(e.clientX - from[0], e.clientY - from[1]) < 4) return;
      from = null;
      api("library/browser/drag", {}).catch((err) => status(err.message, "err"));
    });
    for (const ev of ["pointerup", "pointercancel"]) folder.addEventListener(ev, () => { from = null; });
    folder.addEventListener("dragstart", (e) => e.preventDefault());
    steps.hidden = true;
    folder.hidden = true;
    update.hidden = true;
    box.append(state, update, steps, folder, go);
    const poll = async () => {
      if (!box.isConnected) return;
      try { draw(await api("library/browser")); } catch {}
      setTimeout(poll, 3000);
    };
    requestAnimationFrame(poll);
    return box;
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
    // a trending skill's recent count isn't all its installs: none is told
    if (x.installs) {
      const n = el("span", "mk-installs");
      n.append(svg(GLYPH.down, 11, 1.5), el("span", "", compact(x.installs)));
      n.title = t("{n} installs", { n: x.installs.toLocaleString(intlLang()) });
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

  // A package from Pi's market: who has it, or Install into a Pi (picked
  // when there are several). Pi itself installs it.
  const piLive = () => (piView?.pis || []).filter((p) => !p.asleep && !p.err && !isHidden({ id: p.id }));
  const piHas = (x) => piLive().filter((p) => (p.packages || []).some((k) => k.kind === "npm" && k.name === x.name));
  function piCard(x) {
    const has = piHas(x);
    const c = el("div", "mk-card" + (has.length ? " have" : ""));
    c.dataset.id = x.source;
    const top = el("div", "mk-top");
    const who = el("div", "mk-who");
    const nm = el("div", "mk-name");
    nm.append(el("span", "mk-title", x.name));
    who.append(nm);
    const meta = el("div", "mk-meta");
    if (x.publisher) meta.append(el("span", "mk-pub", x.publisher));
    if (x.version) meta.append(el("span", "mk-badge", "v" + x.version));
    who.append(meta);
    top.append(logo(x.icon, x.name), who);
    const d = el("p", "mk-desc", x.description || "");
    d.title = x.description || "";
    const foot = el("div", "mk-foot");
    if (x.downloads) {
      const n = el("span", "mk-installs");
      n.append(svg(GLYPH.down, 11, 1.5), el("span", "", compact(x.downloads)));
      n.title = t("{n} downloads last month", { n: x.downloads.toLocaleString(intlLang()) });
      foot.append(n);
    }
    foot.append(extLink(x.repo || x.npm, x.repo ? t("Source") : "npm"), el("span", "grow"));
    const live = piLive();
    const want = live.filter((p) => !has.includes(p));
    if (has.length && !want.length) {
      const b = el("span", "mk-have");
      b.append(svg(CHECK, 11, 2), el("span", "", t("Installed")));
      b.title = t("In {agents}", { agents: has.map((p) => p.name).join(", ") });
      foot.append(b);
    } else if (want.length) {
      const busy = want.some((p) => piBusy.has(p.id + " " + x.source));
      const b = button(busy ? t("Installing…") : t("Install"), "action mk-add", () => {
        if (want.length === 1) return installPi(want[0], x);
        openProtoMenu(b, want.map((p) => ({ v: p.id, name: p.name, literalName: true, note: "" })), "", (id) => installPi(want.find((p) => p.id === id), x), "Install into", "", "right");
      });
      if (busy) { b.disabled = true; b.classList.add("busy"); }
      if (has.length) b.title = t("In {agents}", { agents: has.map((p) => p.name).join(", ") });
      foot.append(b);
    }
    c.append(top, d, foot);
    return c;
  }
  async function installPi(p, x) {
    if (!p) return;
    const key = p.id + " " + x.source;
    piBusy.add(key);
    drawMarket("pi");
    status(t("Installing {name} into {agent}…", { name: x.name, agent: p.name }), "", 300000);
    try {
      const v = await api("library/pi/install", { pi: p.id, source: x.source });
      piView = v;
      status(v.pending
        ? t("{name} is in {agent}'s packages now: Pi installs it when it next starts", { name: x.name, agent: p.name })
        : t("{name} is installed in {agent}: Pi loads it when it next starts, or at /reload", { name: x.name, agent: p.name }), "ok", 8000);
    } catch (e) {
      status(e.message, "err", 10000);
    }
    piBusy.delete(key);
    if (tab === "pi") render();
  }
  function confirmRemovePi(p, k) {
    const ed = el("div", "editor lib-editor lib-rmone");
    const head = el("div", "ehead");
    head.append(glyph(GLYPH.trash), el("b", "", t("Remove {name} from {agent}?", { name: k.name, agent: p.name })));
    ed.append(head);
    ed.append(el("p", "lib-confirm", p.wsl
      ? t("It is taken out of {agent}'s settings.json; Pi stops loading it when it next starts.", { agent: p.name })
      : t("pi remove takes it out of {agent}'s settings.json and uninstalls it.", { agent: p.name })));
    const bar = el("div", "bar");
    const go = button(t("Remove"), "primary danger-fill", async () => {
      go.disabled = true;
      const key = p.id + " " + k.source;
      piBusy.add(key);
      try {
        piView = await api("library/pi/remove", { pi: p.id, source: k.source });
        status(t("{name} removed from {agent}", { name: k.name, agent: p.name }), "ok", 6000);
        closeLibModal(true);
      } catch (e) {
        status(e.message, "err", 10000);
        go.disabled = false;
      }
      piBusy.delete(key);
      if (tab === "pi") render();
    });
    bar.append(el("span", "grow"), button(t("Cancel"), "", closeLibModal), go);
    ed.append(bar);
    modal = { save: () => go.click() };
    openLib(ed);
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
    if (x.installs) meta.append(el("span", "mk-badge", t("{n} installs", { n: compact(x.installs) })));
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
    const drawAgents = () => agentsBox.replaceChildren(agentChips(all, agents, (n) => { agents = n; drawAgents(); }, { names: true, all: true }));
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
  // Back to the window, the library is read again — it may have changed
  // in another window or through the CLI meanwhile. Not over an open dialog,
  // unsaved text, or a lookup under way.
  // What changed there changes what the market calls added, too: take asks it again.
  async function quietLoad() {
    if (page.hidden || modal || dirty() || memDirty() || probing) return;
    // what agents saved meanwhile
    if (tab === "memory" && mem) loadMemory();
    await load(true);
  }
  const shelf = () => JSON.stringify([lib?.servers?.map((x) => x.name), lib?.skills?.map((x) => x.name)]);

  ctx.slots.add("page", {
    key: "library",
    title: "Library",
    mount(main) {
      page = main;
      wireLists();
      // The dialog is the providers page's; while the library has it, its
      // backdrop and Escape close it here.
      $("#modal").addEventListener("click", (e) => {
        if (modal && modalBackdrop(e)) { e.stopImmediatePropagation(); closeLibModal(); }
      }, true);
      document.addEventListener("keydown", (e) => {
        if (!confirmationPending && modal && e.key === "Escape") { e.preventDefault(); e.stopImmediatePropagation(); closeLibModal(); }
      }, true);
      // leaving the page with the shared text not saved asks first
      window.addEventListener("beforeunload", (e) => { if (lib && (dirty() || memDirty())) e.preventDefault(); });
      page.addEventListener("scroll", () => page.querySelector(".lib-head")?.classList.toggle("stuck", page.scrollTop > 0), { passive: true });
      document.addEventListener("visibilitychange", () => { if (!document.hidden) quietLoad(); });
      window.addEventListener("focus", quietLoad);
    },
    open: () => (loading = load("open")),
    relang: () => load(),
    // one of the market's servers up close, from another page (Settings ›
    // Images' Magpie Image, #2091, Magpie Image Recognition, and Settings ›
    // Models' Magpie Web Search): its sheet over the MCP servers tab, to
    // pick the agents that get it; one already in the library is on that
    // tab's list, which opens with nothing over it
    server: async (name) => {
      if (tab !== "mcp") openTab("mcp");
      await loading;
      if (!market.mcp.items || market.mcp.q.trim()) {
        market.mcp.q = "";
        await fetchMarket("mcp");
      }
      const x = market.mcp.items?.find((m) => m.name === name || m.id === name);
      if (x && !x.have && !page.hidden) serverSheet(x);
    },
    // drawn again as it is: the agents hidden are known only from the state,
    // which can come after the page was drawn (it counted them, and Turn all
    // on gave them the servers)
    redraw: () => { if (lib) render(); },
    // the reader leaving the page: its dialog closed, and what isn't saved
    close: () => { if (modal) closeLibModal(true); else closeModal(); },
    dirty: () => !!lib && (dirty() || memDirty()),
    discard: () => discard(true),
  });
});
