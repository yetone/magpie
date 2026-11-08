"use strict";
// Context windows: what a request's prompt holds, as the gateway read it
// (gateway.Route.Prompt), drawn as a card — how full the window is, how
// much the vendor read from its cache, a grid of the window with a cell
// for each 1/400 of it coloured by what fills it, and the contents part by
// part. Routing shows the card for the request it tells of; the Usage
// page's Context tab scores each agent's use of its window, tags it, and
// lists its sessions' fill request by request.
(() => {
  const PARTS = ["system", "tools", "skills", "memory", "files", "results", "chat"];
  const PART_NAMES = {
    system: "System prompt", tools: "Tools", skills: "Skills", memory: "Memory",
    files: "Files", results: "Tool results", chat: "Conversation",
  };
  const PART_NOTES = {
    system: "The agent's own instructions",
    tools: "The definitions of the tools it can call",
    skills: "The skills it can load, by name and what each is for",
    memory: "Instruction files, memory and reminders",
    files: "Files it read, and images",
    results: "What the other tools it called returned",
    chat: "What you and the model said, turn by turn",
  };
  const CELLS = 400;
  const partName = (k) => t(PART_NAMES[k] || k);
  // the Codexes here before 0.163 or of no known version (app.js too)
  const codexHeld = window.codexHeld = (held) => t("{who}: the share needs Codex 0.163 or later; until it's updated magpie writes only async_compaction = true, at 65%", { who: held.map((r) => t({ app: "Codex app", ide: "Codex editor extension", wsl: "Codex in WSL" }[r.kind] || "Codex CLI") + (r.version ? " " + r.version : "")).join(", ") });

  const fmtK = (n) => {
    n = Math.max(0, Math.round(n || 0));
    const short = (x, d) => x.toFixed(d).replace(/\.0+$/, "");
    if (n >= 1e6) return short(n / 1e6, n >= 1e7 ? 1 : 2) + "M";
    if (n >= 1e3) return short(n / 1e3, n >= 1e5 ? 0 : 1) + "K";
    return String(n);
  };
  const pct = (x) => Math.round(x * 100) + "%";
  // a count of tokens that a redraw counts on from what it said (morph)
  const num = (tag, cls, n) => { const e = el(tag, cls, fmtK(n)); e.dataset.n = Math.round(n); return e; };
  const median = (xs) => {
    if (!xs.length) return 0;
    const s = [...xs].sort((a, b) => a - b);
    return s[s.length >> 1];
  };
  const agentMeta = (id) => (state.clients || state.agents || []).find((a) => a.id === id);
  // "scripts": requests naming only a library (context.go's scriptsAgent)
  const agentLabel = (id) => (id === "scripts" ? t("Scripts") : agentMeta(id)?.name || id || t("your agent"));
  const agentIcon = (id) => icon(agentMeta(id)?.icon || "generic");
  // the lab a model id is from, by its name after the provider's; none when
  // it isn't known, so a wrong logo never stands for it
  const LABS = [[/claude/, "claude-color"], [/gpt|^o\d|codex/, "openai"], [/gemma/, "gemma-color"], [/gemini/, "gemini-color"],
    [/deepseek/, "deepseek-color"], [/qwen|qwq/, "qwen-color"], [/kimi|moonshot/, "kimi"], [/glm/, "zhipu-color"], [/grok/, "grok"],
    [/minimax|abab/, "minimax-color"], [/doubao|seed/, "doubao-color"], [/stral/, "mistral-color"], [/llama/, "meta-color"],
    [/mimo/, "xiaomimimo"], [/^step/, "stepfun-color"], [/hunyuan/, "hunyuan-color"]];
  const labIcon = (model) => {
    const m = model.toLowerCase().split("/").pop();
    const hit = LABS.find(([re]) => re.test(m));
    return hit ? [icon(hit[1])] : [];
  };

  // what an item of the prompt is called: a file's path or a tool's name
  // as it is; magpie's own names for the rest in the reader's language
  const ITEM_NAMES = {
    prompt: "Agent prompt", harness: "Harness messages", billing: "Billing header",
    environment: "Environment notes", context: "Context", skills: "Skills",
    reminders: "System reminders", compacted: "Compacted summary", image: "Image", document: "Document",
  };
  function itemName(it) {
    if (it.tag === "turn") return t("Turn {n}", { n: it.name });
    if (it.tag === "turns") return t("{n} earlier turns", { n: it.n });
    if (it.tag === "more") return t("{n} more", { n: it.n });
    return ITEM_NAMES[it.name] ? t(ITEM_NAMES[it.name]) : it.name;
  }
  // a path's file name first, where it is after it
  function itemLabel(it, kind) {
    const name = itemName(it);
    const e = el("span", "ctx-name");
    if ((kind === "files" || kind === "memory") && name.includes("/")) {
      const at = name.lastIndexOf("/");
      e.append(el("b", "", name.slice(at + 1)), el("small", "", name.slice(0, at + 1)));
    } else e.append(el("b", "", name));
    e.title = name;
    return e;
  }
  const TAG_NAMES = {
    mcp: "MCP", namespace: "namespace", read: "read", shell: "shell", result: "result",
    user: "user", project: "project", local: "local", memory: "memory", skills: "skills",
    reminder: "reminder", image: "image", turn: "", turns: "", more: "", compacted: "",
  };
  const tagOf = (it) => it.tag in TAG_NAMES ? TAG_NAMES[it.tag] : it.tag || "";

  const health = (fill) => fill < 0.75 ? ["ok", "Healthy"] : fill < 0.9 ? ["warn", "Filling up"] : ["bad", "Near full"];

  // the cache a route's vendor read: its last answer's usage
  function cacheOf(r) {
    const u = r.usage?.[r.usage.length - 1];
    // a vendor that counts no cache has none to show, not 0% (#3464)
    if (!u || u.cache_unknown) return null;
    const total = (u.in || 0) + (u.cache_read || 0) + (u.cache_write || 0);
    if (!total) return null;
    return { read: u.cache_read || 0, total };
  }

  // the cells of the grid, each the part and item it shows; a window
  // smaller than the prompt (not known, or overrun) is the prompt
  function cellsOf(p, window) {
    const scale = Math.max(window, p.tokens, 1);
    const used = Math.min(CELLS, Math.round((p.tokens / scale) * CELLS));
    const parts = PARTS.map((k) => p.parts.find((x) => x.kind === k)).filter((x) => x && x.tokens > 0);
    const sum = parts.reduce((s, x) => s + x.tokens, 0) || 1;
    // largest remainder, so the cells add up to used and a part of any
    // size has at least one
    const want = parts.map((x) => (x.tokens / sum) * used);
    const got = want.map((w) => Math.max(1, Math.floor(w)));
    let left = used - got.reduce((s, n) => s + n, 0);
    const order = want.map((w, i) => [w - Math.floor(w), i]).sort((a, b) => b[0] - a[0]);
    for (let j = 0; left > 0 && order.length; j = (j + 1) % order.length, left--) got[order[j][1]]++;
    for (let j = got.length - 1; left < 0 && j >= 0; j--) while (left < 0 && got[j] > 1) { got[j]--; left++; }
    const cells = [];
    parts.forEach((x, pi) => {
      const items = [...(x.items || [])].sort((a, b) => b.tokens - a.tokens);
      const isum = items.reduce((s, it) => s + it.tokens, 0) || 1;
      let at = 0, acc = 0;
      for (let c = 0; c < got[pi]; c++) {
        // the item whose share the cell's middle falls in
        const mid = ((c + 0.5) / got[pi]) * isum;
        while (at < items.length - 1 && acc + items[at].tokens < mid) acc += items[at++].tokens;
        cells.push({ kind: x.kind, item: items.length ? at : -1 });
      }
    });
    while (cells.length < CELLS) cells.push(null);
    return cells.slice(0, CELLS);
  }

  // the headroom: how many more requests fit, at the pace the session's
  // prompts grew
  function headroom(series, free) {
    const steps = [];
    for (let i = 1; i < series.length; i++) {
      const d = series[i].tokens - series[i - 1].tokens;
      if (d > 0 && series[i].tokens >= series[i - 1].tokens * 0.6) steps.push(d);
    }
    const g = median(steps);
    return g > 0 ? Math.floor(free / g) : null;
  }

  const count = (n, one, many) => n === 1 ? t(one) : t(many, { n });

  // a session's prompts against its window; a point opens its request
  function spark(series, at, window, onPoint) {
    const W = 240, H = 34, pad = 3;
    // scaled to its peak; the window's line shows once it is near
    const peak = Math.max(...series.map((p) => p.tokens), 1);
    const top = window && peak > window * 0.6 ? Math.max(window, peak) : peak * 1.2;
    const x = (i) => series.length < 2 ? W / 2 : pad + (i / (series.length - 1)) * (W - pad * 2);
    const y = (n) => H - pad - (n / top) * (H - pad * 2);
    const box = el("span", "ctx-spark");
    const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    s.setAttribute("viewBox", `0 0 ${W} ${H}`);
    s.setAttribute("preserveAspectRatio", "none");
    box.append(s);
    const ns = (tag, attrs) => {
      const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
      for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
      s.append(e);
      return e;
    };
    if (window && window <= top) ns("line", { x1: 0, x2: W, y1: y(window), y2: y(window), class: "win" });
    const pts = series.map((p, i) => `${x(i).toFixed(1)},${y(p.tokens).toFixed(1)}`);
    if (series.length > 1) {
      ns("path", { d: `M${pts[0]} L${pts.slice(1).join(" L")} L${x(series.length - 1).toFixed(1)},${H} L${x(0).toFixed(1)},${H} Z`, class: "area" });
      ns("polyline", { points: pts.join(" "), class: "line" });
    }
    series.forEach((p, i) => {
      const on = p.id === at;
      // the marker is a dot over the stretched svg, so it stays round
      if (on) {
        const dot = el("span", "at");
        dot.style.left = (x(i) / W * 100).toFixed(2) + "%";
        dot.style.top = (y(p.tokens) / H * 100).toFixed(2) + "%";
        box.append(dot);
      }
      if (!onPoint) return;
      const hit = ns("rect", { x: x(i) - Math.max(2, W / series.length / 2), y: 0, width: Math.max(4, W / series.length), height: H, class: "hit" + (on ? " on" : "") });
      const tip = document.createElementNS("http://www.w3.org/2000/svg", "title");
      const w = p.window || window;
      tip.textContent = w ? t("Request {n}: {tokens} of {window} ({pct})", { n: i + 1, tokens: fmtK(p.tokens), window: fmtK(w), pct: pct(p.tokens / w) })
        : t("Request {n}: {tokens} tokens", { n: i + 1, tokens: fmtK(p.tokens) });
      hit.append(tip);
      hit.onclick = (e) => { e.stopPropagation(); onPoint(p); };
    });
    return box;
  }

  // Narrow layouts go by the card's own width, as classes (max520), not
  // container queries: WebKit pulled a view scrolled to its end up by a
  // card's height on each redraw (#1249). A redrawn card starts at the last
  // width in its place, so it never lays out wide and pulls the page up
  const cardWidth = {};
  const sizeCard = (card, w) => {
    card.classList.toggle("max520", w <= 520);
    card.classList.toggle("max420", w <= 420);
  };
  const cardSizes = new ResizeObserver((all) => {
    for (const e of all) {
      if (!e.target.isConnected) { cardSizes.unobserve(e.target); continue; }
      const card = e.target, w = e.contentRect.width;
      if (!w) continue; // out of sight (#4652)
      cardWidth[card.dataset.place] = w;
      // on the next frame: a class changed in the observer's own call
      // changes the card's height, which WebKit reports as a
      // ResizeObserver loop (see quotaFit)
      requestAnimationFrame(() => sizeCard(card, w));
    }
  });

  // morph makes old what neu is, keeping old's nodes of the same kind: a
  // redrawn card is patched in place (Zhenzhen on Discord: each live
  // request flashed the whole card). Only handlers set as properties come
  // along, so nothing morphed uses addEventListener.
  const HANDLERS = ["onclick", "onpointerenter", "onpointerleave", "onkeydown"];
  function morph(old, neu) {
    if (old.nodeName !== neu.nodeName || (old.nodeType !== 1 && old.nodeType !== 3)) { old.replaceWith(neu); return neu; }
    if (old.nodeType === 3) { if (old.data !== neu.data) old.data = neu.data; return old; }
    // a count (data-n) is counted on from what it said to the new one
    const from = old._at ?? (old.dataset?.n ? +old.dataset.n : undefined);
    for (const a of [...old.attributes]) if (!neu.hasAttribute(a.name)) old.removeAttribute(a.name);
    for (const a of neu.attributes) if (old.getAttribute(a.name) !== a.value) old.setAttribute(a.name, a.value);
    for (const h of HANDLERS) if (old[h] !== neu[h]) old[h] = neu[h];
    morphKids(old, [...neu.childNodes]);
    if (neu.dataset?.n) roll(old, from, +neu.dataset.n, fmtK, calm());
    return old;
  }
  // roll counts node's text from one number to another as the map's tiles
  // glide, in their time (flohoeller's map counts up as its window fills);
  // fmt says a number. quiet, or with nothing to count from, it says the
  // new one at once
  function roll(node, from, to, fmt, quiet) {
    cancelAnimationFrame(node._raf);
    if (quiet || from === undefined || from === to || !node.isConnected) {
      delete node._at;
      if (node.textContent !== fmt(to)) node.textContent = fmt(to);
      return;
    }
    const t0 = performance.now();
    const step = (now) => {
      // a frame's time can be before t0 (it is when the frame began)
      const k = Math.min(1, Math.max(0, (now - t0) / 620));
      node._at = from + (to - from) * (1 - Math.pow(1 - k, 4));
      node.textContent = fmt(node._at);
      if (k < 1) node._raf = requestAnimationFrame(step);
      else delete node._at;
    };
    step(t0);
  }
  function morphKids(box, kids) {
    const old = [...box.childNodes];
    kids.forEach((k, i) => { if (i < old.length) morph(old[i], k); else box.append(k); });
    for (let i = kids.length; i < old.length; i++) old[i].remove();
  }

  // ---------- the map: the window as tiles (flohoeller's context map) ----------

  // The card's window is drawn one of two ways, the reader's pick: the
  // map, each item a tile sized by its tokens inside its part's, the free
  // space a hatched tile of its share; or the grid, a cell for each 1/400
  // of the window. The map is the default; the grid is kept for those who
  // had it.
  let ctxLook = "map";
  try { if (localStorage.getItem("magpie.ctxLook") === "grid") ctxLook = "grid"; } catch {}

  // squarify lays vals ([{v}]) in the rect x, y, w, h as rects whose sides
  // are as near alike as it can (Bruls, Huizing, van Wijk), in vals' order
  function squarify(vals, x, y, w, h) {
    const out = [];
    vals = vals.filter((a) => a.v > 0);
    const total = vals.reduce((s, a) => s + a.v, 0);
    if (!total || w <= 0 || h <= 0) return out;
    const k = (w * h) / total;
    const worst = (row, side) => {
      let sum = 0, hi = 0, lo = Infinity;
      for (const a of row) { const s = a.v * k; sum += s; hi = Math.max(hi, s); lo = Math.min(lo, s); }
      return Math.max((side * side * hi) / (sum * sum), (sum * sum) / (side * side * lo));
    };
    const lay = (row) => {
      const sum = row.reduce((s, a) => s + a.v * k, 0);
      if (w >= h) {
        const cw = sum / h;
        let cy = y;
        for (const a of row) { const ch = (a.v * k) / cw; out.push({ a, x, y: cy, w: cw, h: ch }); cy += ch; }
        x += cw; w -= cw;
      } else {
        const rh = sum / w;
        let cx = x;
        for (const a of row) { const cw = (a.v * k) / rh; out.push({ a, x: cx, y, w: cw, h: rh }); cx += cw; }
        y += rh; h -= rh;
      }
    };
    let row = [];
    for (const a of vals) {
      const side = Math.min(w, h);
      if (!row.length || worst([...row, a], side) <= worst(row, side)) { row.push(a); continue; }
      lay(row);
      row = [a];
    }
    if (row.length) lay(row);
    return out;
  }

  // tilesOf lays a prompt out on a map W×H px: the used share of the
  // window and the free space side by side along its long side (the
  // used share eased, see usedLen), the used
  // parts squarified in it, a part's items in the part. An item too small
  // to read goes in one tile with the part's other small ones; a part too
  // small for its items is one tile. Each tile: key (stays across draws),
  // kind, its item (it), the items it stands for (its), rect in px.
  const TILE_MIN = 1500, TILE_MAX = 12, TILE_FLOOR = 700, TILE_SIDE = 16;
  // the look switch's marks: tiles, and a grid of cells
  const LOOK_MAP = '<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><rect x="1.5" y="2" width="5.5" height="12" rx="1.6"/><rect x="8.5" y="2" width="6" height="6.5" rx="1.6"/><rect x="8.5" y="10" width="6" height="4" rx="1.4"/></svg>';
  const LOOK_GRID = '<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">' +
    [2, 6.5, 11].flatMap((y) => [2, 6.5, 11].map((x) => `<rect x="${x}" y="${y}" width="3" height="3" rx=".8"/>`)).join("") + "</svg>";
  function tilesOf(p, window, W, H) {
    const scale = Math.max(window, p.tokens, 1);
    const across = W >= H;
    const long = across ? W : H;
    // the used share drawn on a gentle curve (share^0.35): a window 8%
    // full still has room to read its tiles (41% of the map), a full one
    // stays full, and the free tile says its own share in words
    let usedLen = long;
    if (p.tokens < scale) usedLen = Math.min(long, Math.max(long * Math.pow(p.tokens / scale, 0.35), Math.min(long, 36)));
    const tiles = [];
    const parts = PARTS.map((k) => p.parts.find((x) => x.kind === k)).filter((x) => x && x.tokens > 0);
    const ur = across ? [0, 0, usedLen, H] : [0, 0, W, usedLen];
    // a tile is never laid smaller than TILE_FLOOR px² (a sliver can't be
    // pointed at): its value raised to that share of the rect it is in
    const floored = (vals, area) => {
      const sum = vals.reduce((s, a) => s + a.v, 0);
      const lo = (sum * TILE_FLOOR) / Math.max(1, area);
      return vals.map((a) => ({ ...a, v: Math.max(a.v, lo) }));
    };
    // and a part's tile is TILE_SIDE px across both ways: the floor is an
    // area, and a part laid in a long row beside large ones met it as a
    // strip (49 tokens of memory beside 70K: 190×4px, under the tiles' 3px
    // gap, so it was hidden; AIGCFREE on Discord). One too thin has its
    // value raised by as much and the parts are laid again.
    const partVals = floored(parts.map((x) => ({ v: x.tokens, x })), ur[2] * ur[3]);
    let partRects = squarify(partVals, ...ur);
    for (let n = 0; n < 8; n++) {
      const thin = partRects.filter((r) => Math.min(r.w, r.h) < TILE_SIDE);
      if (!thin.length) break;
      for (const r of thin) r.a.v *= (TILE_SIDE / Math.max(1, Math.min(r.w, r.h))) * 1.05;
      partRects = squarify(partVals, ...ur);
    }
    for (const r of partRects) {
      const x = r.a.x;
      const items = (x.items || []).filter((it) => it.tokens > 0).sort((a, b) => b.tokens - a.tokens);
      const isum = items.reduce((s, it) => s + it.tokens, 0);
      const area = r.w * r.h;
      if (!items.length || area < TILE_MIN * 2) {
        tiles.push({ key: x.kind, kind: x.kind, its: items, tokens: x.tokens, x: r.x, y: r.y, w: r.w, h: r.h });
        continue;
      }
      // an item's share of the part's tokens, its tile's share of the rect
      const kept = [], rest = [];
      for (const it of items) {
        if (kept.length < TILE_MAX && (it.tokens / isum) * area >= TILE_MIN) kept.push(it);
        else rest.push(it);
      }
      const vals = kept.map((it) => ({ v: it.tokens, it }));
      if (rest.length === 1 && !kept.length) vals.push({ v: rest[0].tokens, it: rest[0] });
      else if (rest.length) vals.push({ v: rest.reduce((s, it) => s + it.tokens, 0), rest });
      // a part whose items would leave one a strip too thin to read or
      // point at is one tile
      const laid = squarify(floored(vals, area), r.x, r.y, r.w, r.h);
      if (laid.some((q) => Math.min(q.w, q.h) < 14)) {
        tiles.push({ key: x.kind, kind: x.kind, its: items, tokens: x.tokens, x: r.x, y: r.y, w: r.w, h: r.h });
        continue;
      }
      for (const q of laid) {
        const t0 = q.a;
        const it = t0.it;
        tiles.push({
          key: x.kind + "\x00" + (it ? (it.tag || "") + "\x00" + it.name : "\x01rest"),
          kind: x.kind, it, its: it ? [it] : t0.rest, tokens: ((it ? it.tokens : t0.rest.reduce((s, y) => s + y.tokens, 0)) / isum) * x.tokens,
          x: q.x, y: q.y, w: q.w, h: q.h,
        });
      }
    }
    if (p.tokens < scale) {
      const fr = across ? [usedLen, 0, W - usedLen, H] : [0, usedLen, W, H - usedLen];
      tiles.push({ key: "\x00free", kind: "", free: true, tokens: scale - p.tokens, x: fr[0], y: fr[1], w: fr[2], h: fr[3] });
    }
    return tiles;
  }

  // ctxCard draws a route's prompt. opts: series (the session's prompts,
  // [{id, tokens, time}]), onPoint (a point of it clicked), cache ({read,
  // total}, when the route carries no usage), crumbs (the footer's path),
  // place (for its width: see cardWidth). The card's ctxUpdate(r, opts)
  // draws another route in it, keeping the nodes that are the same (grid
  // cells too), so nothing flashes and the grid keeps its height.
  // A foldable card (Routing's) keeps the reader's choice: folded, it is its
  // head alone with how full the window is, so the request list has room
  // (0xBCD18E on X: it took most of the height)
  let ctxShut = false;
  try { ctxShut = localStorage.getItem("magpie.ctxShut") === "1"; } catch {}
  // opts.open: this request was opened to see its context window (the
  // Usage page's Context tab), so it shows unfolded, it alone: the next
  // request drawn in the card takes the reader's choice again
  // a window as a thin bar, a part each
  function stackBar(p) {
    const w = p.window || 0, bar = el("span", "ctx-stack"), scale = Math.max(w, p.tokens, 1);
    bar.setAttribute("role", "img");
    bar.setAttribute("aria-label", t("{used} of {window} tokens used", { used: fmtK(p.tokens), window: fmtK(w || p.tokens) }));
    for (const k of PARTS) {
      const i = el("i", "k-" + k), n = (p.parts || []).find((x) => x.kind === k)?.tokens || 0;
      i.title = partName(k);
      i.style.width = +(Math.max(0, n) / scale * 100).toFixed(2) + "%";
      bar.append(i);
    }
    return bar;
  }
  window.ctxStack = stackBar;
  function ctxCard(r, opts = {}) {
    const card = el("div", "ctx-card" + (opts.foldable && ctxShut && !opts.open ? " shut" : ""));
    card.dataset.place = opts.place || "";
    if (cardWidth[card.dataset.place] !== undefined) sizeCard(card, cardWidth[card.dataset.place]);
    cardSizes.observe(card);

    // what stays across draws: the grid and its cells, its tip, the
    // parts around them, and the tab the reader is on
    const grid = el("div", "ctx-waffle");
    grid.setAttribute("role", "img");
    const map = el("div", "ctx-map");
    map.setAttribute("role", "img");
    // the map and the grid in one place, the reader's look shown
    const viz = el("div", "ctx-viz");
    viz.dataset.look = ctxLook;
    viz.append(map, grid);
    // the map's tiles by key, kept across draws: a live request's tiles
    // glide to their new sizes, and only a new item's comes in
    const tiles = new Map();
    const focusOn = (f) => { grid.dataset.f = map.dataset.f = f; };
    const unfocus = () => {
      delete grid.dataset.f;
      delete map.dataset.f;
      for (const e of viz.querySelectorAll(".on")) e.classList.remove("on");
    };
    const tip = el("div", "ctx-tip");
    tip.hidden = true;
    const contents = el("div", "ctx-contents"), ch = el("div", "ctx-contents-head"), list = el("div", "ctx-rows");
    contents.append(ch, list);
    let parts = null, cur = null, cacheShown = false, lastCache = null, cacheWhose = "", chKey = "", rid = 0;
    let tab = opts.tab || "all", more = false;

    const showTip = (target) => {
      const { p, window, cells, partOf } = cur;
      const idx = [...grid.children].indexOf(target);
      const c = cells[idx];
      tip.replaceChildren();
      const th = el("div", "ctx-tip-head");
      if (c) {
        const part = partOf.get(c.kind);
        th.append(el("i", "dot k-" + c.kind), el("b", "", partName(c.kind)), el("span", "grow"),
          el("span", "", fmtK(part.tokens) + " · " + pct(part.tokens / Math.max(1, p.tokens))));
        tip.append(th);
        const items = [...(part.items || [])].sort((a, b) => b.tokens - a.tokens);
        const shown = items.slice(0, 4);
        const here = items[c.item];
        if (here && !shown.includes(here)) shown[3] = here;
        for (const it of shown) {
          const row = el("div", "ctx-tip-row" + (it === here ? " on" : ""));
          row.append(el("span", "n", itemName(it)), el("span", "v", fmtK(it.tokens)));
          tip.append(row);
        }
        if (items.length > shown.length) tip.append(el("div", "ctx-tip-more", t("{n} more", { n: items.length - shown.length })));
        grid.dataset.f = c.kind;
      } else {
        th.append(el("i", "dot free"), el("b", "", t("Free space")), el("span", "grow"),
          el("span", "", fmtK(Math.max(0, window - p.tokens))));
        tip.append(th);
        grid.dataset.f = "free";
      }
      placeTip(target);
    };
    // the tip over what it tells of, or under it where there's no room
    const placeTip = (target) => {
      tip.hidden = false;
      const b = target.getBoundingClientRect(), cr = card.getBoundingClientRect();
      const w = tip.offsetWidth, h = tip.offsetHeight;
      let left = b.left - cr.left + b.width / 2 - w / 2;
      left = Math.max(8, Math.min(cr.width - w - 8, left));
      let topY = b.top - cr.top - h - 8;
      if (topY < 4) topY = b.bottom - cr.top + 8;
      tip.style.left = left + "px";
      tip.style.top = topY + "px";
    };
    // a tile's tip: the item, its part and share; a part's or the small
    // ones' tile, its largest items
    const showMapTip = (n) => {
      const { p, window } = cur;
      const tl = n._tile;
      tip.replaceChildren();
      const th = el("div", "ctx-tip-head");
      if (tl.free) {
        th.append(el("i", "dot free"), el("b", "", t("Free space")), el("span", "grow"),
          el("span", "", fmtK(Math.max(0, window - p.tokens))));
        tip.append(th);
      } else if (tl.it) {
        th.append(el("i", "dot k-" + tl.kind), el("b", "", itemName(tl.it)));
        tip.append(th, el("div", "ctx-tip-sub", partName(tl.kind) + " · " + fmtK(tl.it.tokens) + " " + t("tokens") + " · " + pct(tl.it.tokens / Math.max(1, p.tokens))));
      } else {
        th.append(el("i", "dot k-" + tl.kind), el("b", "", partName(tl.kind)), el("span", "grow"),
          el("span", "", fmtK(tl.tokens) + " · " + pct(tl.tokens / Math.max(1, p.tokens))));
        tip.append(th);
        const shown = tl.its.slice(0, 5);
        for (const it of shown) {
          const row = el("div", "ctx-tip-row");
          row.append(el("span", "n", itemName(it)), el("span", "v", fmtK(it.tokens)));
          tip.append(row);
        }
        if (tl.its.length > shown.length) tip.append(el("div", "ctx-tip-more", t("{n} more", { n: tl.its.length - shown.length })));
      }
      placeTip(n);
    };
    // the tip told again as its tile grows, and kept over it as it glides
    const followTip = (n) => {
      if (!n.isConnected || n.classList.contains("gone")) { tip.hidden = true; return; }
      showMapTip(n);
      const t0 = performance.now();
      const go = (now) => {
        if (tip.hidden || !n.classList.contains("on")) return;
        placeTip(n);
        if (now - t0 < 660) requestAnimationFrame(go);
      };
      requestAnimationFrame(go);
    };
    const tileAt = (e) => {
      const n = e.target.closest?.(".ctx-tile");
      return cur && n && n.parentElement === map && n._tile && !n.classList.contains("gone") ? n : null;
    };
    map.addEventListener("pointerover", (e) => {
      const n = tileAt(e);
      if (!n) return;
      map.dataset.f = "tile";
      for (const x of tiles.values()) x.classList.toggle("on", x === n);
      showMapTip(n);
    });
    map.addEventListener("pointerleave", () => { tip.hidden = true; unfocus(); });
    // a tile clicked opens the text of what it shows, as a cell does
    map.addEventListener("click", (e) => {
      const n = tileAt(e);
      if (!n || n._tile.free) return;
      e.stopPropagation();
      tip.hidden = true;
      openCtxText(rid, n._tile.kind, n._tile.it);
    });

    // drawMap lays the map out at its size: a tile kept glides to its new
    // place (CSS), a new one comes in, one gone fades. enter: every tile
    // comes in, one after another (the first draw, or the map picked)
    const drawMap = (enter) => {
      if (!cur || viz.dataset.look !== "map") return;
      const W = map.clientWidth, H = map.clientHeight;
      // folded or not in the page yet: drawn once it has a size
      if (!W || !H) return;
      map.dataset.size = W + "x" + H;
      const { p, window } = cur;
      const quiet = opts.still || calm();
      enter = enter || !tiles.size;
      const seen = new Set();
      tilesOf(p, window, W, H).forEach((tl, i) => {
        seen.add(tl.key);
        let n = tiles.get(tl.key);
        const fresh = !n;
        if (fresh) {
          n = el("div", "ctx-tile");
          n.append(el("b", "n"), el("span", "v"));
          tiles.set(tl.key, n);
          map.append(n);
        }
        n._tile = tl;
        const cls = "ctx-tile " + (tl.free ? "free" : "k-" + tl.kind) + (tl.w < 54 || tl.h < 30 ? " bare" : tl.h < 46 ? " low" : "") + (n.classList.contains("on") ? " on" : "");
        if (n.className !== cls) n.className = cls;
        // the value counts on to its tokens as the tile glides (roll)
        let name, v = (x) => fmtK(x) + " · " + partName(tl.kind);
        if (tl.free) { name = t("Free space"); v = (x) => fmtK(x) + " · " + pct(x / Math.max(window, p.tokens, 1)); }
        else if (tl.it) {
          name = itemName(tl.it);
          if (tl.kind === "files" || tl.kind === "memory") name = name.slice(name.lastIndexOf("/", name.length - 2) + 1) || name;
          // a tool's results gathered, how many: "web__run ×20"
          if (tl.it.n > 1 && tl.it.tag !== "turns" && tl.it.tag !== "more") name += " ×" + tl.it.n;
        } else if (tl.key === tl.kind) { name = partName(tl.kind); v = fmtK; }
        else {
          // the small files gathered: their folder, when they share one
          // ("src/api/ · 12 files"), as the reader would name them
          const dirs = new Set(tl.its.map((it) => it.name.slice(0, it.name.lastIndexOf("/") + 1)));
          const [dir] = dirs;
          if (tl.kind === "files" && dirs.size === 1 && dir) {
            name = dir.slice(dir.lastIndexOf("/", dir.length - 2) + 1);
            v = (x) => fmtK(x) + " · " + t("{n} files", { n: tl.its.length });
          } else name = t("{n} more", { n: tl.its.length });
        }
        if (n.firstChild.textContent !== name) n.firstChild.textContent = name;
        roll(n.lastChild, fresh ? undefined : n.lastChild._at ?? n.lastChild._n, tl.tokens, v, quiet || enter);
        n.lastChild._n = tl.tokens;
        // a gap of 3px between tiles: each 1.5px in from its rect
        const pc = (a, b) => +((a / b) * 100).toFixed(3);
        n.style.left = `calc(${pc(tl.x, W)}% + 1.5px)`;
        n.style.top = `calc(${pc(tl.y, H)}% + 1.5px)`;
        n.style.width = `max(0px, calc(${pc(tl.w, W)}% - 3px))`;
        n.style.height = `max(0px, calc(${pc(tl.h, H)}% - 3px))`;
        if (quiet || !n.animate || !(fresh || enter)) return;
        n.animate([{ opacity: 0, transform: "scale(.94)", filter: "blur(4px)" }, { opacity: 1, transform: "none", filter: "none" }],
          { duration: 380, easing: IOS_EASE, delay: enter ? 30 + Math.min(i, 16) * 20 : 0, fill: "backwards" });
      });
      for (const [k, n] of tiles) {
        if (seen.has(k)) continue;
        tiles.delete(k);
        n.classList.add("gone");
        if (quiet || !n.animate) { n.remove(); continue; }
        n.animate([{ opacity: 1 }, { opacity: 0, transform: "scale(.96)" }], { duration: 180, easing: IOS_EASE, fill: "forwards" })
          .finished.then(() => n.remove(), () => n.remove());
      }
    };
    // the map's size changed (the card unfolded, the window resized): laid
    // out again for it, its tiles' labels shown where they fit
    const mapSizes = new ResizeObserver(() => {
      if (!map.isConnected) return;
      if (map.dataset.size !== map.clientWidth + "x" + map.clientHeight) drawMap(false);
    });
    mapSizes.observe(map);

    // setLook shows the map or the grid, the reader's pick for every card;
    // the place eases to the other's height, the map's tiles come in
    const setLook = (k) => {
      if (viz.dataset.look === k) return;
      ctxLook = k;
      try { localStorage.setItem("magpie.ctxLook", k); } catch {}
      const quiet = opts.still || calm();
      const h0 = viz.offsetHeight;
      tip.hidden = true;
      unfocus();
      viz.dataset.look = k;
      for (const b of card.querySelectorAll(".ctx-look-b")) {
        const on = b.dataset.k === k;
        b.classList.toggle("on", on);
        b.setAttribute("aria-pressed", String(on));
      }
      if (k === "map") { for (const n of tiles.values()) n.remove(); tiles.clear(); drawMap(true); }
      if (quiet || !viz.animate) return;
      const h1 = viz.offsetHeight;
      if (h0 !== h1) {
        viz.style.overflow = "hidden";
        viz.animate([{ height: h0 + "px" }, { height: h1 + "px" }], { duration: 340, easing: IOS_EASE })
          .finished.finally(() => { viz.style.overflow = ""; }).catch(() => {});
      }
    };

    grid.addEventListener("pointerover", (e) => { if (cur && e.target.parentElement === grid) showTip(e.target); });
    grid.addEventListener("pointerleave", () => { tip.hidden = true; delete grid.dataset.f; });
    // a cell clicked opens the text of what it shows (masaka on Discord)
    grid.addEventListener("click", (e) => {
      if (!cur || e.target.parentElement !== grid) return;
      const c = cur.cells[[...grid.children].indexOf(e.target)];
      if (!c) return;
      e.stopPropagation();
      const items = [...(cur.partOf.get(c.kind)?.items || [])].sort((a, b) => b.tokens - a.tokens);
      tip.hidden = true;
      openCtxText(rid, c.kind, items[c.item]);
    });

    // the contents, part by part
    const drawRows = () => {
      const { cells, partOf, p } = cur;
      let rows;
      if (tab === "all") {
        rows = p.parts.flatMap((x) => (x.items || []).map((it, i) => ({ it, i, kind: x.kind })));
      } else {
        const x = partOf.get(tab);
        rows = (x?.items || []).map((it, i) => ({ it, i, kind: tab }));
      }
      rows.sort((a, b) => b.it.tokens - a.it.tokens);
      const most = Math.max(1, ...rows.map((x) => x.it.tokens));
      const n = more ? rows.length : Math.min(rows.length, 8);
      const out = rows.slice(0, n).map(({ it, i, kind }) => {
        const row = el("div", "ctx-row");
        const tg = tagOf(it);
        const bar = el("span", "ctx-mini");
        const b = el("i", "k-" + kind);
        b.style.width = Math.max(2, (it.tokens / most) * 100).toFixed(1) + "%";
        bar.append(b);
        row.append(el("i", "dot k-" + kind), itemLabel(it, kind));
        if (it.n > 1 && it.tag !== "turns" && it.tag !== "more") row.append(el("span", "ctx-x", "×" + it.n));
        if (tg) row.append(el("code", "ctx-tag", tg));
        row.append(bar, el("span", "ctx-v", fmtK(it.tokens)));
        // clicked, its text
        clickable(row, () => openCtxText(rid, kind, it));
        // its cells light up
        row.onpointerenter = () => {
          grid.dataset.f = "item";
          for (const [j, c] of cells.entries()) grid.children[j].classList.toggle("on", !!c && c.kind === kind && c.item === i);
          map.dataset.f = "tile";
          for (const n of tiles.values()) n.classList.toggle("on", n._tile.kind === kind && !!n._tile.its?.includes(it));
        };
        row.onpointerleave = unfocus;
        return row;
      });
      if (!rows.length) out.push(el("div", "ctx-empty", t("Nothing in it")));
      if (rows.length > 8) {
        const b = el("button", "text ctx-more", more ? t("Show fewer") : t("Show all {n}", { n: rows.length }));
        b.type = "button";
        b.onclick = () => { more = !more; drawRows(); };
        out.push(b);
      }
      morphKids(list, out);
    };

    const fill = (r, opts) => {
      const p = r.prompt;
      if (!p) {
        card.replaceChildren();
        parts = cur = null;
        return;
      }
      rid = r;
      const window = p.window || 0;
      const full = window ? p.tokens / window : 0;
      const live = !r.done;

      // the head: what it is and how it was counted
      if (opts.foldable) card.classList.toggle("shut", ctxShut && !opts.open);
      const head = el("div", "ctx-head");
      let title = el("span", "ctx-title", t("Context window"));
      if (opts.foldable) {
        const fold = el("button", "ctx-fold");
        fold.type = "button";
        fold.setAttribute("aria-expanded", String(!card.classList.contains("shut")));
        const tw = el("span", "tw");
        tw.append(svg(CHEV, 10, 1.7));
        fold.append(tw, title);
        // the head is morphed, so the button that is there is the target
        fold.onclick = (e) => {
          ctxShut = !card.classList.contains("shut");
          card.classList.toggle("shut", ctxShut);
          tip.hidden = true;
          e.currentTarget.setAttribute("aria-expanded", String(!ctxShut));
          try { localStorage.setItem("magpie.ctxShut", ctxShut ? "1" : "0"); } catch {}
          opts.onFold?.(ctxShut);
        };
        title = fold;
      }
      // the state's words are all there, one shown: the pill is as wide as
      // the widest in any language, so the model beside it doesn't move as
      // a request goes from Live to Counted (dumplings on Discord: each
      // request made the card jitter)
      const state = live ? "live" : p.counted ? "counted" : "est";
      const st = el("span", "ctx-state " + state);
      const words = el("span", "ctx-state-w");
      for (const [k, w] of [["live", "Live"], ["counted", "Counted"], ["est", "Estimated"]]) {
        const word = el("span", k === state ? "on" : "", t(w));
        if (k !== state) word.setAttribute("aria-hidden", "true");
        words.append(word);
      }
      st.append(el("i"), words);
      st.title = t(live ? "The request is on its way: the prompt is estimated from what the agent sent"
        : p.counted ? "As many tokens as the vendor counted; the parts are measured from the request and scaled to it"
        : "Estimated from what the agent sent: the vendor didn't say how many tokens it read");
      head.append(title);
      if (opts.foldable) {
        const short = el("span", "ctx-short");
        short.append(num("span", "", p.tokens), window ? " / " + fmtK(window) + " · " + pct(full) : "");
        head.append(short);
        // folded, the bar: morphed, so a live request moves widths only
        head.append(stackBar(p));
      }
      head.append(el("span", "grow"));
      // whose request it is and the model it went to; in a narrow card the
      // agent's name goes first, its icon stays
      const route = el("span", "ctx-route");
      if (r.agent) {
        const who = el("span", "ctx-who");
        who.append(agentIcon(r.agent), el("span", "ctx-who-n", agentLabel(r.agent)));
        route.append(who);
      }
      if (r.model) {
        const what = el("span", "ctx-what");
        what.append(...labIcon(r.model), el("code", "ctx-model", r.model));
        if (r.agent) route.append(el("span", "ctx-sep", "›"));
        route.append(what);
      }
      // the route and the state go together: where the title leaves them
      // too little room they take a line of their own, the model id cut
      // to fit there, and nothing is drawn over the title (packinggoods on
      // Discord: in a narrow window the provider's icon sat on it)
      const end = el("span", "ctx-end");
      end.append(route, st);
      head.append(end);

      // how full, and the cache
      const top = el("div", "ctx-top");
      const used = el("div", "ctx-stat");
      const k = el("div", "ctx-k");
      k.append(el("span", "", t("Context used")));
      if (window) {
        const [tone, word] = health(full);
        const h = el("span", "ctx-health " + tone, t(word));
        k.append(h);
      }
      const big = el("div", "ctx-big");
      big.append(num("b", "", p.tokens));
      big.append(el("span", "", window ? "/ " + fmtK(window) + " " + t("tokens") : t("tokens")));
      const sub = el("div", "ctx-sub");
      if (window) {
        const bits = [t("{pct} full", { pct: pct(full) })];
        const turns = opts.series ? headroom(opts.series, Math.max(0, window - p.tokens)) : null;
        if (turns !== null) bits.push(turns >= 999 ? t("plenty of headroom") : t("~{n} requests of headroom", { n: turns }));
        else bits.push(t("{tokens} free", { tokens: fmtK(Math.max(0, window - p.tokens)) }));
        sub.textContent = bits.join(" · ");
      } else sub.textContent = t("the model's window isn't known");
      used.append(k, big, sub);
      top.append(used);

      const cache = cacheOf(r) || opts.cache;
      // another agent or session drawn in the card has no last request's
      // cache to show: its place stays, with a dash till it is read
      const whose = r.agent + " " + (opts.series?.[0]?.id ?? "");
      if (whose !== cacheWhose) lastCache = null;
      cacheWhose = whose;
      const known = cache && p.counted !== false ? cache : null;
      if (known || cacheShown) {
        // once shown, the cache keeps its place, so nothing under it moves
        // up and back down. A request under way has read no cache yet:
        // the last request's stays, dimmed and named as the last one's,
        // until this one's answer says (dumplings on Discord: each new
        // request flashed it to "—" and an empty bar, the dash against
        // 命中). Nothing to show is said in words, with no bar
        const shown = known || (live ? lastCache : null);
        const c = el("div", "ctx-stat ctx-cache" + (!known && shown ? " was" : shown ? "" : " none"));
        c.append(el("div", "ctx-k", t("Prompt cache")));
        const cb = el("div", "ctx-big");
        cb.append(el("b", "", shown ? pct(shown.read / shown.total) : "—"), el("span", "", t("hit")));
        const bar = el("div", "ctx-bar");
        const fillBar = el("i");
        fillBar.style.width = (shown ? (shown.read / shown.total) * 100 : 0).toFixed(1) + "%";
        bar.append(fillBar);
        c.append(cb, bar, el("div", "ctx-sub", known ? t("{cached} cached · {fresh} new", { cached: fmtK(known.read), fresh: fmtK(known.total - known.read) })
          : shown ? t("The last request's · this one is on its way")
          : live ? t("Read once the vendor answers") : t("The vendor didn't say")));
        top.append(c);
        cacheShown = true;
        if (known) lastCache = known;
      }

      // the grid: its cells made once, each patched to what it shows now
      grid.setAttribute("aria-label", t("{used} of {window} tokens used", { used: fmtK(p.tokens), window: fmtK(window || p.tokens) }));
      const cells = cellsOf(p, window);
      const partOf = new Map(p.parts.map((x) => [x.kind, x]));
      const made = grid.children.length;
      cells.forEach((c, i) => {
        let e = grid.children[i];
        if (!e) {
          e = el("i");
          if (!made && !opts.still) e.style.setProperty("--d", Math.round(i * 1.4) + "ms");
          grid.append(e);
        }
        const cls = c ? "k-" + c.kind : "free";
        if (e.className !== cls) e.className = cls;
        if (!c) delete e.dataset.it;
        else if (e.dataset.it !== String(c.item)) e.dataset.it = c.item;
      });
      cur = { p, window, cells, partOf };
      // a tile pointed at keeps its tip through a live request's draws
      const over = !tip.hidden && map.dataset.f === "tile" ? [...tiles.values()].find((n) => n.classList.contains("on")) : null;
      if (!tip.hidden && !over) tip.hidden = true;
      map.setAttribute("aria-label", grid.getAttribute("aria-label"));
      drawMap(false);
      if (over) followTip(over);

      // the legend: a part hovered lights its cells
      const legend = el("div", "ctx-legend");
      for (const kind of PARTS) {
        const part = partOf.get(kind);
        if (!part || part.tokens <= 0) continue;
        const li = el("span", "ctx-leg");
        li.title = t(PART_NOTES[kind]);
        li.append(el("i", "dot k-" + kind), el("span", "n", partName(kind)), num("span", "v", part.tokens));
        clickable(li, () => openCtxText(rid, kind));
        li.onpointerenter = () => focusOn(kind);
        li.onpointerleave = unfocus;
        legend.append(li);
      }
      if (window && window > p.tokens) {
        const li = el("span", "ctx-leg");
        li.append(el("i", "dot free"), el("span", "n", t("Free space")), num("span", "v", window - p.tokens));
        li.onpointerenter = () => focusOn("free");
        li.onpointerleave = unfocus;
        legend.append(li);
      }
      // the look: the map or the grid
      const look = el("span", "ctx-look");
      look.setAttribute("role", "group");
      for (const [k, name, ic] of [["map", "Context map", LOOK_MAP], ["grid", "Block grid", LOOK_GRID]]) {
        const on = viz.dataset.look === k;
        const b = el("button", "ctx-look-b" + (on ? " on" : ""));
        b.type = "button";
        b.dataset.k = k;
        b.title = t(name);
        b.setAttribute("aria-label", t(name));
        b.setAttribute("aria-pressed", String(on));
        b.innerHTML = ic;
        b.onclick = (e) => { e.stopPropagation(); setLook(k); };
        look.append(b);
      }
      legend.append(look);

      // the contents' tabs, drawn again only when they change
      const tabs = [["all", t("All")], ...PARTS.filter((k) => partOf.get(k)?.tokens > 0).map((k) => [k, partName(k)])];
      if (!tabs.some(([id]) => id === tab)) { tab = "all"; more = false; }
      const key = JSON.stringify([tabs, tab]);
      if (key !== chKey) {
        chKey = key;
        ch.replaceChildren(el("span", "ctx-k", t("Contents")), el("span", "grow"),
          segs(tabs, tab, (id) => { tab = id; more = false; chKey = JSON.stringify([tabs, tab]); opts.onTab?.(id); drawRows(); }));
      }
      drawRows();

      // the foot: where it is, and the session's line
      const foot = el("div", "ctx-foot");
      const crumbs = el("code", "ctx-crumbs");
      crumbs.textContent = (opts.crumbs || [agentLabel(r.agent)]).filter(Boolean).join(" › ");
      const notes = [];
      if (p.held) notes.push(t("Earlier turns are held by the vendor"));
      if (p.turns) notes.push(count(p.turns, "1 turn", "{n} turns"));
      foot.append(crumbs, el("span", "grow"));
      if (notes.length) foot.append(el("span", "ctx-note", notes.join(" · ")));
      let sp = null;
      if (opts.series && opts.series.length > 1) {
        sp = el("div", "ctx-session");
        sp.append(el("span", "ctx-k", t("This session")), spark(opts.series, r.id, window, opts.onPoint),
          el("span", "ctx-sub", count(opts.series.length, "1 request", "{n} requests")));
      }

      if (!parts) {
        parts = { head, top, legend, foot, sp };
        card.append(head, top, viz, legend, contents, foot, ...(sp ? [sp] : []), tip);
        return;
      }
      for (const k of ["head", "top", "legend", "foot"]) parts[k] = morph(parts[k], { head, top, legend, foot }[k]);
      if (sp && parts.sp) parts.sp = morph(parts.sp, sp);
      else if (sp) card.insertBefore(parts.sp = sp, tip);
      else if (parts.sp) { parts.sp.remove(); parts.sp = null; }
    };
    card.ctxUpdate = fill;
    fill(r, opts);
    return card;
  }
  window.ctxCard = ctxCard;

  // clickable makes e a button for the keyboard too, its handlers set as
  // properties, so a morphed card keeps them
  function clickable(e, go) {
    e.setAttribute("role", "button");
    e.tabIndex = 0;
    e.classList.add("ctx-click");
    e.onclick = (ev) => { ev.stopPropagation(); go(); };
    e.onkeydown = (ev) => {
      if (ev.key !== "Enter" && ev.key !== " ") return;
      ev.preventDefault();
      go();
    };
  }

  // ---------- the text of a part (masaka on Discord) ----------

  // A part of the card clicked opens what the request sent there, read
  // from the request's body: an item alone (a tool, a file, a turn), or
  // the part's items, each opened to its text (ctxtext.js, loaded then)
  const openCtxText = (...a) => import("./ctxtext.js").then((m) => m.open({ fmtK, partName, itemName, itemLabel, tagOf }, ...a)).catch(statusErr);
  window.openCtxText = openCtxText;
  window.ctxFmt = fmtK;

  // ---------- the Usage page's Context tab ----------

  const CTX_RANGES = [["1", "Today"], ["7", "7 days"], ["30", "30 days"]];
  let ctxDays = "7";
  try { const d = localStorage.getItem("magpie.ctxDays"); if (CTX_RANGES.some(([id]) => id === d)) ctxDays = d; } catch {}
  let ctxData = null, ctxJSON = "", ctxRead = 0, ctxOpen = "", ctxAgent = "all";
  // the days the pane shows "Reading…" for: a second read before the history
  // answers (the page opened, then the first state's refresh) leaves it be,
  // so the advice drawn meanwhile keeps its entrance
  let ctxWaiting = "";

  const SCORE_NAMES = { cache: "Cache", lean: "Lean start", pace: "Pace", reliable: "Reliability" };
  const SCORE_NOTES = {
    cache: "How much of its prompts the vendor read from its cache: full marks at 90%",
    lean: "What every request starts with — system prompt, tools, memory — against the window: full marks at 5% or less",
    pace: "How much a request adds to the one before it: full marks at 1% of the window or less",
    reliable: "Its requests that didn't fail: none at 20% failing",
  };
  const TAGS = {
    "cache-friendly": ["Cache-friendly", (v) => t("{pct} of its prompts were read from the vendor's cache", { pct: pct(v) })],
    "cache-misses": ["Cache misses", (v) => t("Only {pct} of its prompts were read from the cache: each request pays for most of its prompt again", { pct: pct(v) })],
    lean: ["Lean start", (v) => t("Every request starts with {tokens} of system prompt, tools and memory", { tokens: fmtK(v) })],
    "heavy-start": ["Heavy start", (v) => t("Every request starts with {tokens} of system prompt, tools and memory, before a word of yours", { tokens: fmtK(v) })],
    "mcp-heavy": ["MCP-heavy", (v) => t("MCP servers' tool definitions take {tokens} of every request", { tokens: fmtK(v) })],
    "tool-heavy": ["Tool-heavy", (v) => t("Tool results are {pct} of its prompts", { pct: pct(v) })],
    "file-reader": ["Reads files", (v) => t("Files it read are {pct} of its prompts", { pct: pct(v) })],
    "memory-heavy": ["Memory-heavy", (v) => t("Instruction files and memory are {pct} of its prompts", { pct: pct(v) })],
    "fills-fast": ["Fills fast", (v) => t("A request adds {tokens} to the one before it, the median", { tokens: fmtK(v) })],
    "near-full": ["Runs near full", (v) => t("A session reached {pct} of its window", { pct: pct(v) })],
    compacts: ["Compacts", (v) => t("Its context was compacted {n} times", { n: v })],
    steady: ["Steady", () => t("Fewer than 1% of its requests failed")],
    flaky: ["Flaky", (v) => t("{pct} of its requests failed", { pct: pct(v) })],
  };
  const grade = (s) => s >= 85 ? ["great", "Excellent"] : s >= 70 ? ["good", "Good"] : s >= 50 ? ["fair", "Fair"] : ["poor", "Needs care"];

  function ring(score) {
    const r = 26, c = 2 * Math.PI * r;
    const [tone] = grade(score);
    const box = el("div", "ctx-ring " + tone);
    const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    s.setAttribute("viewBox", "0 0 64 64");
    s.innerHTML = `<circle cx="32" cy="32" r="${r}" class="track"/><circle cx="32" cy="32" r="${r}" class="arc" stroke-dasharray="${c.toFixed(2)}" stroke-dashoffset="${c.toFixed(2)}" style="--to:${(c * (1 - score / 100)).toFixed(2)}"/>`;
    box.append(s, el("b", "", String(score)));
    return box;
  }

  function shareBar(shares) {
    const bar = el("div", "ctx-share");
    for (const k of PARTS) {
      const v = shares?.[k] || 0;
      if (v <= 0.002) continue;
      const i = el("i", "k-" + k);
      i.style.flexGrow = v;
      i.title = partName(k) + " · " + pct(v);
      bar.append(i);
    }
    return bar;
  }

  function agentCard(a) {
    const card = el("div", "ctx-agent");
    const head = el("div", "ctx-agent-head");
    const who = el("div", "ctx-who");
    who.append(agentIcon(a.agent), el("b", "", agentLabel(a.agent)));
    const cl = a.clients || [];
    const meta = el("div", "ctx-who-sub", [...(cl.length ? [cl.slice(0, 3).join(", ") + (cl.length > 3 ? " +" + (cl.length - 3) : "")] : []), count(a.requests, "1 request", "{n} requests"), count(a.sessions, "1 session", "{n} sessions")].join(" · "));
    if (cl.length) who.title = t("Requests whose User-Agent names only an HTTP library or an SDK, not an agent: {clients}", { clients: cl.join(", ") });
    const name = el("div", "ctx-who-box");
    name.append(who, meta);
    const [tone, word] = grade(a.score);
    const g = el("div", "ctx-grade");
    g.append(ring(a.score), el("span", "ctx-grade-word " + tone, t(word)));
    head.append(name, el("span", "grow"), g);

    const scores = el("div", "ctx-scores");
    for (const s of a.scores || []) {
      const r = s.most ? s.points / s.most : 0;
      const row = el("div", "ctx-score " + (r >= 0.8 ? "ok" : r >= 0.5 ? "warn" : "bad"));
      row.title = t(SCORE_NOTES[s.key] || "");
      const bar = el("span", "ctx-bar");
      const f = el("i");
      f.style.width = ((s.points / s.most) * 100).toFixed(1) + "%";
      bar.append(f);
      row.append(el("span", "n", t(SCORE_NAMES[s.key] || s.key)), bar, el("span", "v", `${s.points}/${s.most}`));
      scores.append(row);
    }

    const tags = el("div", "ctx-tags");
    for (const tg of a.tags || []) {
      const def = TAGS[tg.key];
      const chip = el("span", "ctx-chip " + tg.tone);
      chip.append(el("i"), def ? t(def[0]) : tg.key);
      chip.dataset.tt = def ? def[1](tg.value || 0) : tg.key;
      tags.append(chip);
    }

    const facts = el("div", "ctx-facts");
    const fact = (label, value, note) => {
      const f = el("div", "ctx-fact");
      f.append(el("span", "ctx-k", t(label)), el("b", "", value));
      if (note) f.dataset.tt = t(note);
      facts.append(f);
    };
    fact("Typical prompt", fmtK(a.median), "The median request's prompt");
    fact("Largest", fmtK(a.p90), "90% of its requests' prompts are this size or smaller");
    fact("Starts with", fmtK(a.baseline), "System prompt, tools and memory: what every request carries before the conversation");
    fact("Grows by", a.growth ? "+" + fmtK(a.growth) : "—", "How much a request's prompt adds to the one before it, the median");
    fact("Cache hit", a.scores?.find((s) => s.key === "cache") && a.cache ? pct(a.cache) : "—", "Of its prompts, what the vendor read from its cache");
    fact("Window size", a.window ? fmtK(a.window) : "—", "The largest context window of the models it used");

    const mix = el("div", "ctx-mix");
    mix.append(el("span", "ctx-k", t("What its prompts hold")), shareBar(a.shares));
    const keys = el("div", "ctx-mix-keys");
    for (const k of PARTS) {
      const v = a.shares?.[k] || 0;
      if (v < 0.01) continue;
      const s = el("span", "");
      s.append(el("i", "dot k-" + k), partName(k) + " " + pct(v));
      keys.append(s);
    }
    mix.append(keys);

    const foot = el("div", "ctx-agent-foot");
    const models = el("span", "ctx-models");
    // by the model's own name: "live/claude/claude-sonnet-4-5" cut short
    // read the same for each of its models; the whole id in its tooltip
    for (const m of a.models || []) {
      const c = el("code", "", m.split("/").pop());
      c.title = m;
      models.append(c);
    }
    foot.append(models, el("span", "grow"));
    if (a.latestId) {
      const go = el("button", "text", t("Latest request"));
      go.type = "button";
      go.dataset.feature = "routing"; // it opens on the Routing page
      go.onclick = () => window.openRoute(a.latestId, a.latestTime, { context: true }).catch(statusErr);
      foot.append(go);
    }
    card.append(head, scores, tags, facts, mix, foot);
    return card;
  }

  function sessionRow(s) {
    const open = ctxOpen === s.agent + "\x00" + s.key;
    const box = el("div", "ctx-sess" + (open ? " open" : ""));
    const row = el("button", "ctx-sess-row");
    row.type = "button";
    row.setAttribute("aria-expanded", String(open));
    const name = el("span", "ctx-sess-name");
    // an untitled session goes by the head of its id, the way the footer names it
    name.append(s.title ? el("b", "", s.title) : el("b", "raw", s.key.slice(0, 8)));
    name.append(el("code", "", s.model));
    const fill = s.window ? s.peak / s.window : 0;
    const [tone] = health(fill);
    const peak = el("span", "ctx-sess-peak " + tone, s.window ? pct(fill) : fmtK(s.peak));
    peak.dataset.tt = t("Its largest prompt: {tokens} of {window}", { tokens: fmtK(s.peak), window: fmtK(s.window) });
    const meta = el("span", "ctx-sess-meta", count(s.requests, "1 request", "{n} requests") + (s.compacts ? " · " + count(s.compacts, "1 compaction", "{n} compactions") : ""));
    row.append(agentIcon(s.agent), name, spark(s.points, s.latestId, s.window), peak, meta, el("span", "ctx-sess-when", ago(s.last)));
    row.onclick = () => {
      ctxOpen = open ? "" : s.agent + "\x00" + s.key;
      renderContext();
    };
    box.append(row);
    if (open && s.latest) {
      const last = s.points[s.points.length - 1];
      const r = { id: s.latestId, time: s.last, done: true, agent: s.agent, model: s.model, prompt: s.latest };
      const cache = last && s.latest.counted && last.tokens && !last.cache_unknown ? { read: last.cache || 0, total: last.tokens } : null;
      box.append(ctxCard(r, {
        still: true, cache, place: "session",
        series: s.points,
        crumbs: [agentLabel(s.agent), s.title || s.key.slice(0, 12), t("latest request")],
        onPoint: (pt) => { if (!featuresOff.has("routing")) window.openRoute(pt.id, pt.time, { context: true }).catch(statusErr); },
      }));
    }
    return box;
  }

  window.ctxSpark = spark;
  window.ctxHealth = health;

  // ---------- tuned for you: the settings that would have spent the fewest tokens ----------

  let tuneData = null, tuneJSON = "", tuneRead = 0, tuneBusy = "";
  // whether the last draw showed the history and the advice: what was on
  // the pane already doesn't play its entrance again when the pane is
  // redrawn (the other answering, the state, a filter, a click)
  let ctxDrawn = false, tuneDrawn = false;

  const KNOBS = {
    compact: "Auto-compact",
    cache_ttl: "Prompt cache · main conversation",
    subagent_cache_ttl: "Prompt cache · subagents",
  };
  const ttlName = (v) => v === "1h" ? t("1 hour") : v === "5m" ? t("5 min") : v || "—";

  // chart draws what each setting would have cost over the range, as the
  // tokens it spends past the advice: the area over the advice's level is
  // what the others waste, and the one set now says how much
  let chartN = 0;
  // how far apart, as a share of the axis, two of its labels must sit: a
  // label is about 30px wide, the narrowest card's axis about 280px
  const TICK_GAP = 0.16;
  function chart(pts, current, best, show, timed) {
    if (pts.length < 2) return null;
    const near = (v) => pts.reduce((a, p) => Math.abs(p.value - v) < Math.abs(a.value - v) ? p : a, pts[0]);
    const now = typeof current === "number" ? near(current) : pts.find((p) => p.value === current);
    const top = typeof best === "number" ? near(best) : pts.find((p) => p.value === best);
    if (!top) return null;
    // measured from the advice, not the cheapest point: tune takes the
    // largest window within 1% of it, and those cost nothing more here.
    // Timed, the advice may pass a cheaper window that waits longer, so
    // each is said against the cheapest
    const low = timed ? Math.min(...pts.map((p) => p.cost)) : top.cost;
    const extra = (p) => Math.max(0, p.cost - low);
    const most = Math.max(...pts.map(extra));
    if (!(most > 0)) return null;
    const worst = pts.reduce((a, p) => p.cost > a.cost ? p : a, pts[0]);
    const W = 300, H = 100, top0 = 22, base = 84;
    const X = (i) => (i / (pts.length - 1)) * W;
    const Y = (p) => base - (extra(p) / most) * (base - top0);
    const xy = pts.map((p, i) => [X(i), Y(p)]);
    // a smooth line through every point that never swings past its neighbours
    let d = `M${xy[0][0].toFixed(1)} ${xy[0][1].toFixed(1)}`;
    for (let i = 1; i < xy.length; i++) {
      const [x0, y0] = xy[i - 1], [x1, y1] = xy[i], m = (x1 - x0) / 2;
      d += ` C${(x0 + m).toFixed(1)} ${y0.toFixed(1)} ${(x1 - m).toFixed(1)} ${y1.toFixed(1)} ${x1.toFixed(1)} ${y1.toFixed(1)}`;
    }
    const id = "ctx-waste-" + ++chartN;
    const box = el("div", "ctx-tune-chart");
    const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    s.setAttribute("viewBox", `0 0 ${W} ${H}`);
    s.setAttribute("preserveAspectRatio", "none");
    s.innerHTML = `<defs><linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1">` +
      `<stop offset="0" class="hot"/><stop offset="1" class="cool"/></linearGradient></defs>` +
      `<line class="level" x1="0" x2="${W}" y1="${base}" y2="${base}"/>` +
      `<path class="area" fill="url(#${id})" d="${d} L${W} ${base} L0 ${base} Z"/>` +
      `<path class="line" d="${d}"/>`;
    const plot = el("div", "ctx-tc-plot");
    plot.append(s);
    box.append(plot);
    const at = (p, cls, label) => {
      const i = pts.indexOf(p);
      const dot = el("span", "ctx-tc-dot " + cls);
      dot.style.left = (i / (pts.length - 1)) * 100 + "%";
      dot.style.top = (Y(p) / H) * 100 + "%";
      if (label) {
        const tag = el("b", "ctx-tc-tag", label);
        if (i === 0) tag.classList.add("start");
        if (i === pts.length - 1) tag.classList.add("end");
        dot.append(tag);
      }
      dot.dataset.tt = p === top
        ? timed ? t("{value}: the fewest tokens without waiting longer on compactions", { value: show(p.value) })
          : t("{value}: the fewest tokens for how you work", { value: show(p.value) })
        : t("{value}: {tokens} more tokens over the range (+{pct})", { value: show(p.value), tokens: fmtK(extra(p)), pct: pct(extra(p) / low) });
      plot.append(dot);
    };
    // the advice's value, the setting now, the range's ends: one too close
    // to one before is left off, so labels never overlap (#1549, Ti800)
    const along = (p) => pts.indexOf(p) / (pts.length - 1);
    // the tags the same way: Leanest, the setting now, the costliest; one
    // near a tag that matters more keeps its dot, not its tag (#1549)
    const tagged = [];
    const tag = (p, label) => {
      if (tagged.some((k) => Math.abs(along(k) - along(p)) < TICK_GAP && Math.abs(Y(k) - Y(p)) < 20)) return "";
      tagged.push(p);
      return label;
    };
    const bestTag = tag(top, timed ? t("Tokens + wait") : t("Leanest"));
    const nowTag = now && now !== top ? tag(now, "+" + pct(extra(now) / low)) : "";
    const worstTag = worst !== now && worst !== top ? tag(worst, "+" + pct(extra(worst) / low)) : "";
    if (worst !== now && worst !== top) at(worst, "worst", worstTag);
    at(top, "best", bestTag);
    if (now && now !== top) at(now, "now", nowTag);
    const axis = el("div", "ctx-tc-x");
    const kept = [];
    for (const p of new Set([top, now, pts[0], pts[pts.length - 1]].filter(Boolean))) {
      if (kept.every((k) => Math.abs(along(k) - along(p)) >= TICK_GAP)) kept.push(p);
    }
    for (const p of new Set([pts[0], top, now, pts[pts.length - 1]].filter(Boolean))) {
      const i = pts.indexOf(p);
      if (!kept.includes(p)) continue;
      const tick = el("span", (p === top ? "best" : p === now ? "now" : "") + (i === 0 ? " start" : i === pts.length - 1 ? " end" : ""), show(p.value));
      tick.style.left = (i / (pts.length - 1)) * 100 + "%";
      axis.append(tick);
    }
    box.append(axis);
    box.dataset.tt = t("Tokens each setting would have spent past the leanest, on your own calls");
    return box;
  }

  function adviceRow(agent, a) {
    // few: put in by tune, too few calls to advise on; kept for Undo (linkaww)
    const r = a.compact || a.ttl || { current: a.setting.value, best: a.setting.value, cost: 0, low: 0 };
    const ttl = a.knob != "compact";
    const show = (v) => ttl ? (v ? ttlName(v) : t("Default")) : fmtK(+v);
    const cur = ttl ? r.current : r.current, best = r.best;
    const same = String(cur) === String(best);
    const saves = r.cost > 0 ? (r.cost - r.low) / r.cost : 0;
    const set = a.setting || {};
    // a model's own Compact at is where Codex runs, over Long conversations
    // (hoobnn on Discord: "In use" for 170K beside "Now 430K")
    const own = a.own;
    const held = !!(set.locked || own);
    const applied = !own && set.ours && String(set.value) === String(best);
    const row = el("div", "ctx-tune-row" + (same || applied ? " done" : ""));

    const title = el("div", "ctx-tune-title");
    title.append(el("b", "", t(KNOBS[a.knob] || a.knob)));
    if (a.model) title.append(el("code", "", a.model));
    row.append(title);

    const vals = el("div", "ctx-tune-vals");
    const val = (label, v, cls) => {
      const f = el("div", "ctx-tune-val " + cls);
      f.append(el("span", "ctx-k", t(label)), el("b", "", show(v)));
      return f;
    };
    vals.append(val("Now", cur, "now"));
    // named for what it is measured by, tokens alone, not "best" (#1854);
    // with the agent's compactions timed, tokens and the wait on them
    const timed = !!(a.compact && (r.facts || {}).took > 0);
    if (!same) vals.append(el("span", "ctx-tune-arrow", "→"), val(timed ? "Tokens + wait" : "Fewest tokens", best, "best"));
    row.append(vals);
    const c = a.compact
      ? chart(a.compact.curve || [], r.current, r.best, show, timed)
      : chart([{ value: "5m", cost: r.short }, { value: "1h", cost: r.hour }], r.current, r.best, show);
    if (c) row.append(c);
    // the leanest band: within 3% of the cheapest (yapechan on Discord)
    const curve = (a.compact && a.compact.curve) || [];
    const fewest = curve.length ? Math.min(...curve.map((p) => p.cost)) : 0;
    const band = curve.filter((p) => p.cost <= fewest * 1.03);
    const lean = same && timed && band.length && r.cost > fewest * 1.03 ? band[band.length - 1] : null;
    const bandName = lean ? (band[0] === lean ? show(lean.value) : show(band[0].value) + "–" + show(lean.value)) : "";

    const why = el("p", "ctx-tune-why");
    const f = r.facts || {};
    if (a.few) why.textContent = t("Not enough calls in this range to tune on");
    else if (a.compact) {
      why.textContent = f.compacts
        ? t("Your conversations grow {growth} a call; a compaction leaves {after}, and reading back what it dropped costs {rework}. {n} compactions in the range.", { growth: fmtK(f.growth), after: fmtK(f.after), rework: fmtK(f.rework), n: f.compacts })
        : t("Your conversations grow {growth} a call, and none was compacted in the range.", { growth: fmtK(f.growth) });
      // how long the agent's own compactions took, from its files: what a
      // window that compacts more often would have kept the reader waiting
      if (timed) {
        const secs = f.took < 120 ? t("{n}s", { n: f.took }) : fmtDur(f.took);
        why.textContent += " " + (same
          ? t("Each compaction kept you waiting about {secs} ({n} timed). Over the range that's about {wait} at {value}.", { secs, n: f.timed, wait: fmtDur(r.wait || 0), value: show(cur) })
          : t("Each compaction kept you waiting about {secs} ({n} timed). Over the range that's about {wait} at {best}, against {now} at {value}.", { secs, n: f.timed, wait: fmtDur(r.wait_best || 0), best: show(best), now: fmtDur(r.wait || 0), value: show(cur) }));
        // the cheaper window the advice passed over, for waiting longer
        const cheap = (a.compact.curve || []).find((p) => p.value === r.cheapest);
        const adv = same ? r.cost : r.low;
        if (cheap && r.cheapest !== best && adv > 0 && (adv - cheap.cost) / adv >= 0.03 && (r.wait_cheapest || 0) > (same ? r.wait || 0 : r.wait_best || 0))
          why.textContent += " " + t("{value} would spend {pct} fewer tokens, but wait about {wait}.", { value: show(r.cheapest), pct: pct((adv - cheap.cost) / adv), wait: fmtDur(r.wait_cheapest) });
      }
    } else {
      why.textContent = t("{quick} of your calls came within 5 minutes of the one before, {pause} after a pause of 5 to 60 minutes.", { quick: pct(f.quick || 0), pause: pct(f.pause || 0) });
    }
    row.append(why);
    // one with its own Compact at, which Use doesn't move (hoobnn)
    for (const m of a.apart || []) row.append(el("p", "ctx-tune-caveat ctx-tune-apart", t("{model} compacts at its own {value}, which this doesn't move", { model: m.model, value: fmtK(m.value) })));
    // a window advised is weighed by tokens alone: what a compaction loses of
    // the task isn't measured, and a smaller one compacts more often (#1854)
    if (a.compact && (!same || lean)) row.append(el("p", "ctx-tune-caveat", timed
      ? t("Tokens and waiting only: it doesn't measure how well the agent keeps the task after a compaction. The lower the window, the more often it compacts.")
      : t("A token estimate only: it doesn't measure how well the agent keeps the task after a compaction. The lower the window, the more often it compacts.")));

    const foot = el("div", "ctx-tune-foot");
    // the advice magpie put in is "In use" in every range, Now at it too
    // (hoobnn: over 7 and 30 days it read "Already the leanest")
    const verdict = !lean && (same || applied)
      ? el("span", "ctx-chip good", "")
      : el("span", "ctx-chip info", "");
    verdict.append(el("i"), lean ? t("Not the leanest: {range} spends the fewest tokens", { range: bandName })
      : applied ? t("In use") : same ? t("Already the leanest for how you work") : t("{pct} fewer tokens · {tokens} over the range", { pct: pct(saves), tokens: fmtK(Math.round(r.cost - r.low)) }));
    verdict.hidden = !!a.few && !applied;
    foot.append(verdict, el("span", "grow"));
    const note = el("div", "ctx-tune-src");
    title.after(note);
    if (set.locked) note.textContent = t("{name} is set and comes first", { name: set.locked });
    else if (own) note.textContent = t("{model} compacts at its own {value}, which comes first", { model: own.model, value: fmtK(own.value) });
    else if (set.source == "long") note.textContent = t("Long conversations, shared by Codex, Claude Code and Grok Build");
    else if (set.ours) note.textContent = t("Set by magpie");
    else if (set.value && a.compact && +set.value > a.compact.window && a.compact.window) note.textContent = t("You set {value}; the model's {window} comes first", { value: show(set.value), window: fmtK(a.compact.window) });
    else if (set.value) note.textContent = t("You set {value}", { value: show(set.value) });
    else note.textContent = t("The agent's default");
    const key = [agent, a.knob, a.model || ""].join("\x00");
    const send = async (path, value, knob = a.knob) => {
      tuneBusy = key;
      renderContext();
      try {
        await api("tune/" + path, { agent, knob, model: a.model || "", value: String(value ?? "") });
        status(path === "apply" ? t("Saved: {agent} reads it from its next session", { agent: agentLabel(agent) }) : t("Put back"), "ok");
      } catch (e) {
        statusErr(e);
      }
      tuneBusy = "";
      await loadTune(true);
    };
    if (set.ours) {
      const undo = el("button", "text", t("Undo"));
      undo.type = "button";
      undo.disabled = tuneBusy === key;
      undo.onclick = () => send("undo");
      foot.append(undo);
    }
    const use = (v, tt) => {
      const go = el("button", "text primary", t("Use {value}", { value: show(v) }));
      go.type = "button";
      go.disabled = tuneBusy === key;
      if (tt) go.dataset.tt = tt;
      go.onclick = () => send("apply", v);
      foot.append(go);
    };
    const yours = set.value && !set.ours && t("Replaces your {value}; Undo puts it back", { value: show(set.value) });
    if (!same && !applied && !held) use(best, yours);
    // an hour no Undo puts back, too few calls to weigh (linkaww)
    if (a.few && ttl && cur === "1h" && !held && !set.ours) use("5m", yours);
    if (lean && !held) use(lean.value, yours || t("Fewer tokens, but more compactions to wait on"));
    // a window of the reader's own (#1860), against the setting now (hayden4608)
    const pts = (a.compact && a.compact.curve) || [];
    if (pts.length > 1 && !held) {
      const other = el("button", "text ctx-tune-pick", t("Other window…"));
      other.type = "button";
      other.disabled = tuneBusy === key;
      other.setAttribute("aria-haspopup", "menu");
      other.setAttribute("aria-expanded", "false");
      other.onclick = (e) => {
        e.stopPropagation();
        // the fewest tokens: timed, the advice may spend over the cheapest
        // to wait no longer, so the cheapest
        const cheapest = pts.reduce((x, p) => p.cost < x.cost ? p : x, pts[0]);
        const fewest = timed ? cheapest.value : best;
        const now = set.ours && set.value != null ? +set.value : cur;
        const near = pts.reduce((x, p) => Math.abs(p.value - now) < Math.abs(x.value - now) ? p : x, pts[0]);
        // the setting now's cost: its own point, else the replay's at it
        const base = near.value === now ? near.cost : r.cost > 0 ? r.cost : near.cost;
        const opts = pts.map((p) => {
          const diff = p.cost - base, share = base > 0 ? Math.abs(diff) / base : 0;
          const spend = p.value === now ? t("Your setting now")
            : share < 0.005 ? t("As few tokens")
            : diff < 0 ? t("−{tokens} tokens (−{pct})", { tokens: fmtK(-diff), pct: pct(share) })
            : t("+{tokens} tokens (+{pct})", { tokens: fmtK(diff), pct: pct(share) });
          const often = p.compacts == null ? "" : p.compacts ? count(p.compacts, "about 1 compaction", "about {n} compactions") : t("no compaction");
          const wait = timed && p.compacts ? t("about {d} waiting", { d: fmtDur(p.wait || 0) }) : "";
          return { v: String(p.value), name: show(p.value), literalName: true, note: [spend, p.value === fewest ? t("Fewest tokens") : "", often, wait].filter(Boolean).join(" · ") };
        });
        openProtoMenu(other, opts, String(near.value), (v) => {
          if (v !== String(now)) send("apply", v);
        }, "Compact at · against the setting now, from your calls of the range", "ctx-tune-menu");
      };
      foot.append(other);
    }
    // Codex's background compaction at the advice, the limit kept (herman_hong)
    const as = a.async, aset = as?.setting || {}, cav = (s) => row.append(el("p", "ctx-tune-caveat ctx-tune-async", s));
    const btn = (label, go, c = "") => {
      const b = el("button", "text" + c, label);
      b.type = "button";
      b.disabled = tuneBusy === key;
      b.onclick = go;
      return foot.appendChild(b);
    };
    if (!as) {} else if (aset.locked) cav(t("Background compaction: Codex's config.toml sets {key}, which comes first", { key: aset.locked }));
    else if (as.held?.length) cav(codexHeld(as.held));
    else if (aset.ours && +aset.value === as.percent) {
      verdict.after(el("span", "ctx-chip good ctx-tune-async", t("In the background at {value}", { value: fmtK(as.value) })));
      verdict.nextSibling.prepend(el("i"));
      btn(t("Undo background"), () => send("undo", "", "async_compact"));
    } else btn(t("Use {value} in the background", { value: fmtK(as.value) }), () => send("apply", as.percent, "async_compact"), " ctx-tune-async").dataset.tt = t("Codex compacts in the background past {pct} of the model's window, about {value} of {window}, while the turn goes on; {now} stays as the foreground limit, for when the background one isn't ready. A Codex feature still under development, 0.163 or later.", { pct: as.percent + "%", value: fmtK(as.value), window: fmtK(as.window), now: show(cur) });
    row.append(foot);
    return row;
  }

  function tuneCard(ag) {
    const card = el("div", "ctx-tune");
    const head = el("div", "ctx-agent-head");
    const who = el("div", "ctx-who");
    who.append(agentIcon(ag.agent), el("b", "", agentLabel(ag.agent)));
    const name = el("div", "ctx-who-box");
    name.append(who, el("div", "ctx-who-sub", count(ag.calls, "1 call", "{n} calls")));
    head.append(name, el("span", "grow"));
    const total = ag.advice.reduce((s, a) => { const r = a.compact || a.ttl || {}; return s + Math.max(0, r.cost - r.low || 0); }, 0);
    const all = ag.advice.reduce((s, a) => s + ((a.compact || a.ttl)?.cost || 0), 0);
    // a share that rounds to nothing is no promise
    if (all > 0 && total / all >= 0.01) head.append(el("span", "ctx-tune-total", t("Up to {pct} fewer", { pct: pct(total / all) })));
    card.append(head);
    for (const a of ag.advice) card.append(adviceRow(ag.agent, a));
    if (!ag.advice.length) card.append(el("p", "usage-note", t("Not enough calls in this range to tune on")));
    return card;
  }

  function renderTune(pane) {
    // gateway mode: the agents and their files are on other computers
    if (tuneData?.gateway || !tuneData && gatewayMode) return;
    const head = el("div", "row-head ctx-sess-head");
    head.append(el("span", "label", t("Tuned for you")), el("span", "grow"));
    pane.append(head);
    pane.append(el("p", "usage-note ctx-tune-note", t("Your own calls of the range, replayed setting by setting from your agents' session files: the settings that would have spent the fewest tokens, for how you work. Tokens are weighed as the vendor charges them: a cache read is a tenth of one.")));
    if (!tuneData) {
      pane.append(el("p", "usage-note", t("Working it out from your sessions…")));
      return;
    }
    const shown = tuneData.agents.filter((a) => a.advice.length);
    if (!shown.length) {
      pane.append(el("p", "usage-note", t("Not enough calls in this range to tune on")));
      return;
    }
    const grid = el("div", "ctx-agents ctx-tunes");
    for (const a of shown) grid.append(tuneCard(a));
    pane.append(grid);
  }

  // what this page can draw: another version's reply can hold other rows
  // (Linka on Discord: a page from before few rows threw on one, "r.cost")
  const obj = (r) => !!r && typeof r == "object", objs = (l) => Array.isArray(l) ? l.filter(obj) : [];

  async function loadTune(force) {
    const read = ++tuneRead, days = ctxDays;
    if (tuneData && tuneData.days !== +days) tuneData = null;
    const data = await api("tune?days=" + days);
    if (read !== tuneRead) return;
    const json = JSON.stringify(data);
    if (!force && tuneData && json === tuneJSON) return;
    tuneData = { days: +data.days || +days, gateway: !!data?.gateway, agents: objs(data?.agents).map((a) => ({ ...a, advice: objs(a.advice).filter((r) => obj(r.compact) || obj(r.ttl) || r.few).map((r) => ({ ...r, setting: obj(r.setting) ? r.setting : {} })) })) }, tuneJSON = json;
    renderContext();
  }

  function renderContext() {
    const pane = $("#contextPane");
    if (!pane) return;
    // the range sits in the Usage page's own head, where the other tabs keep
    // theirs, so no row of its own stands empty above the pane
    $("#ctxRange")?.replaceChildren(segs(CTX_RANGES.map(([id, n]) => [id, t(n)]), ctxDays, (id) => {
      ctxDays = id;
      try { localStorage.setItem("magpie.ctxDays", id); } catch {}
      loadContext();
    }));
    pane.replaceChildren();
    pane.classList.toggle("still", ctxDrawn && !!ctxData);
    pane.classList.toggle("tune-still", tuneDrawn && !!tuneData);
    ctxDrawn = !!ctxData, tuneDrawn = !!tuneData;
    renderTune(pane);
    if (!ctxData) {
      pane.append(el("p", "usage-note", t("Reading the routing history…")));
      return;
    }
    const seen = el("div", "row-head ctx-sess-head");
    seen.append(el("span", "label", t("Through magpie")), el("span", "grow"));
    pane.append(seen);
    pane.append(el("p", "usage-note", t("What each agent's prompts hold, as magpie's gateway read them: the score and tags are worked out from the requests of the range.")));
    if (!ctxData.agents.length) {
      const empty = el("div", "ctx-none");
      empty.append(el("b", "", t("No prompts read yet")), el("span", "", t("Requests your agents send through magpie show here.")));
      pane.append(empty);
      return;
    }
    const grid = el("div", "ctx-agents");
    for (const a of ctxData.agents) grid.append(agentCard(a));
    pane.append(grid);

    const agents = [...new Set(ctxData.sessions.map((s) => s.agent))];
    const head = el("div", "row-head ctx-sess-head");
    head.append(el("span", "label", t("Sessions")), el("span", "grow"));
    if (agents.length > 1) {
      if (ctxAgent !== "all" && !agents.includes(ctxAgent)) ctxAgent = "all";
      head.append(segs([["all", t("All")], ...agents.map((id) => [id, agentLabel(id)])], ctxAgent, (id) => { ctxAgent = id; renderContext(); }));
    }
    pane.append(head);
    const list = el("div", "ctx-sess-list");
    const shown = ctxData.sessions.filter((s) => ctxAgent === "all" || s.agent === ctxAgent);
    for (const s of shown) list.append(sessionRow(s));
    if (!shown.length) list.append(el("p", "usage-note", t("No sessions in this range")));
    pane.append(list);
  }

  async function loadContext() {
    const read = ++ctxRead, days = ctxDays;
    if (tuneData && tuneData.days !== +days) tuneData = null;
    if (ctxData && ctxData.days !== +days) ctxData = null;
    if (!ctxData && (ctxWaiting !== days || !$("#contextPane")?.childElementCount)) { ctxWaiting = days; renderContext(); }
    loadTune().catch(statusErr);
    const data = await api("context?days=" + days);
    if (read !== ctxRead) return;
    // the auto refresh redraws only what changed: the same answer keeps the
    // pane (and what the pointer is on), and a new one comes in without its
    // entrance animations. What it is drawn with counts too: the agents'
    // names and icons come with the state, which can answer after a first
    // history, drawn by the agents' ids until then
    const json = JSON.stringify([data, (state.clients || state.agents || []).map((a) => [a.id, a.name, a.icon])]);
    if (ctxData && json === ctxJSON) return;
    ctxData = data, ctxJSON = json;
    renderContext();
  }
  window.loadContext = loadContext;
  window.renderContext = renderContext;
})();
