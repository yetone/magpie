"use strict";
// Context windows: what a request's prompt holds, as the gateway read it
// (gateway.Route.Prompt), drawn as a card — how full the window is, how
// much the vendor read from its cache, a grid of the window with a cell
// for each 1/400 of it coloured by what fills it, and the contents part by
// part. Routing shows the card for the request it tells of; the Usage
// page's Context tab scores each agent's use of its window, tags it, and
// lists its sessions' fill request by request.
(() => {
  const PARTS = ["system", "tools", "memory", "files", "results", "chat"];
  const PART_NAMES = {
    system: "System prompt", tools: "Tools", memory: "Memory",
    files: "Files", results: "Tool results", chat: "Conversation",
  };
  const PART_NOTES = {
    system: "The agent's own instructions",
    tools: "The definitions of the tools it can call",
    memory: "Instruction files, memory and reminders",
    files: "Files it read, and images",
    results: "What the other tools it called returned",
    chat: "What you and the model said, turn by turn",
  };
  const CELLS = 400;
  const partName = (k) => t(PART_NAMES[k] || k);

  const fmtK = (n) => {
    n = Math.max(0, Math.round(n || 0));
    const short = (x, d) => x.toFixed(d).replace(/\.0+$/, "");
    if (n >= 1e6) return short(n / 1e6, n >= 1e7 ? 1 : 2) + "M";
    if (n >= 1e3) return short(n / 1e3, n >= 1e5 ? 0 : 1) + "K";
    return String(n);
  };
  const pct = (x) => Math.round(x * 100) + "%";
  const median = (xs) => {
    if (!xs.length) return 0;
    const s = [...xs].sort((a, b) => a - b);
    return s[s.length >> 1];
  };
  const agentMeta = (id) => (state.clients || state.agents || []).find((a) => a.id === id);
  const agentLabel = (id) => agentMeta(id)?.name || id || t("your agent");
  const agentIcon = (id) => icon(agentMeta(id)?.icon || "generic");

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
    if (!u) return null;
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

  // count words a number with the phrase for one, or for many
  const count = (n, one, many) => n === 1 ? t(one) : t(many, { n });

  // spark draws a session's prompts, request by request, against its
  // window; the one the card tells of is marked, and a point clicked opens
  // its request
  function spark(series, at, window, onPoint) {
    const W = 240, H = 34, pad = 3;
    // scaled to the session's peak, so a session that never came near its
    // window still shows how it grew; the window's line shows once it is near
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
      tip.textContent = t("Request {n}: {tokens} tokens", { n: i + 1, tokens: fmtK(p.tokens) });
      hit.append(tip);
      hit.onclick = (e) => { e.stopPropagation(); onPoint(p); };
    });
    return box;
  }

  // A card's narrow layouts go by its own width, as container queries
  // would, set here as classes (max520: 520px wide or less). Not by
  // container queries: WebKit pulled a view scrolled to its end back up by
  // a card's height each time the card was drawn again (#1249). A card
  // drawn again starts with the width the last one in its place had, so it
  // doesn't lay out wide for a moment and pull the page up as it narrows
  const cardWidth = {};
  const sizeCard = (card, w) => {
    card.classList.toggle("max520", w <= 520);
    card.classList.toggle("max420", w <= 420);
  };
  const cardSizes = new ResizeObserver((all) => {
    for (const e of all) {
      if (!e.target.isConnected) { cardSizes.unobserve(e.target); continue; }
      const card = e.target, w = e.contentRect.width;
      cardWidth[card.dataset.place] = w;
      // on the next frame: a class changed in the observer's own call
      // changes the card's height, which WebKit reports as a
      // ResizeObserver loop (see quotaFit)
      requestAnimationFrame(() => sizeCard(card, w));
    }
  });

  // morph makes old what neu is, keeping old's nodes wherever they are of
  // the same kind: a card drawn again for the next request is patched in
  // place, not put in afresh (Zhenzhen on Discord: each live request
  // flashed the whole card, its cells coming in again). Handlers set as
  // properties come along; one added with addEventListener wouldn't, so
  // nothing morphed has one.
  const HANDLERS = ["onclick", "onpointerenter", "onpointerleave"];
  function morph(old, neu) {
    if (old.nodeName !== neu.nodeName || (old.nodeType !== 1 && old.nodeType !== 3)) { old.replaceWith(neu); return neu; }
    if (old.nodeType === 3) { if (old.data !== neu.data) old.data = neu.data; return old; }
    for (const a of [...old.attributes]) if (!neu.hasAttribute(a.name)) old.removeAttribute(a.name);
    for (const a of neu.attributes) if (old.getAttribute(a.name) !== a.value) old.setAttribute(a.name, a.value);
    for (const h of HANDLERS) if (old[h] !== neu[h]) old[h] = neu[h];
    morphKids(old, [...neu.childNodes]);
    return old;
  }
  function morphKids(box, kids) {
    const old = [...box.childNodes];
    kids.forEach((k, i) => { if (i < old.length) morph(old[i], k); else box.append(k); });
    for (let i = kids.length; i < old.length; i++) old[i].remove();
  }

  // ctxCard draws a route's prompt. opts: series (the session's prompts,
  // [{id, tokens, time}]), onPoint (a point of it clicked), cache ({read,
  // total}, when the route carries no usage), crumbs (the footer's path),
  // place (where it is drawn, for its width: see cardWidth). The card's
  // ctxUpdate(r, opts) draws another route, or the same one further on, in
  // it: what is the same stays the same nodes, its grid's cells among them,
  // so nothing flashes or comes in again and the grid keeps its height
  // a card that folds (Routing's, opts.foldable) keeps the reader's
  // choice: folded, it is its head alone, with how full the window is
  // beside the title, so the request list under it has the room
  // (0xBCD18E on X: it took most of the height, three rows were left)
  let ctxShut = false;
  try { ctxShut = localStorage.getItem("magpie.ctxShut") === "1"; } catch {}
  // opts.open: this request was opened to see its context window (the
  // Usage page's Context tab), so it shows unfolded, it alone: the next
  // request drawn in the card takes the reader's choice again
  function ctxCard(r, opts = {}) {
    const card = el("div", "ctx-card" + (opts.foldable && ctxShut && !opts.open ? " shut" : ""));
    card.dataset.place = opts.place || "";
    if (cardWidth[card.dataset.place] !== undefined) sizeCard(card, cardWidth[card.dataset.place]);
    cardSizes.observe(card);

    // what stays across draws: the grid and its cells, its tip, the
    // parts around them, and the tab the reader is on
    const grid = el("div", "ctx-waffle");
    grid.setAttribute("role", "img");
    const tip = el("div", "ctx-tip");
    tip.hidden = true;
    const contents = el("div", "ctx-contents"), ch = el("div", "ctx-contents-head"), list = el("div", "ctx-rows");
    contents.append(ch, list);
    let parts = null, cur = null, cacheShown = false, chKey = "";
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
    grid.addEventListener("pointerover", (e) => { if (cur && e.target.parentElement === grid) showTip(e.target); });
    grid.addEventListener("pointerleave", () => { tip.hidden = true; delete grid.dataset.f; });

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
        // its cells light up
        row.onpointerenter = () => {
          grid.dataset.f = "item";
          for (const [j, c] of cells.entries()) grid.children[j].classList.toggle("on", !!c && c.kind === kind && c.item === i);
        };
        row.onpointerleave = () => {
          delete grid.dataset.f;
          for (const e of grid.querySelectorAll("i.on")) e.classList.remove("on");
        };
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
      const st = el("span", "ctx-state " + (live ? "live" : p.counted ? "counted" : "est"));
      st.append(el("i"), t(live ? "Live" : p.counted ? "Counted" : "Estimated"));
      st.title = t(live ? "The request is on its way: the prompt is estimated from what the agent sent"
        : p.counted ? "As many tokens as the vendor counted; the parts are measured from the request and scaled to it"
        : "Estimated from what the agent sent: the vendor didn't say how many tokens it read");
      head.append(title);
      if (opts.foldable) {
        head.append(el("span", "ctx-short", fmtK(p.tokens) + (window ? " / " + fmtK(window) + " · " + pct(full) : "")));
        // folded, a thin bar of what fills the window, a part each; it is
        // morphed with the head, so a live request moves its widths only
        const bar = el("span", "ctx-stack");
        bar.setAttribute("role", "img");
        bar.setAttribute("aria-label", t("{used} of {window} tokens used", { used: fmtK(p.tokens), window: fmtK(window || p.tokens) }));
        const scale = Math.max(window, p.tokens, 1);
        for (const k of PARTS) {
          const n = p.parts.find((x) => x.kind === k)?.tokens || 0;
          const i = el("i", "k-" + k);
          i.title = partName(k);
          i.style.width = +(Math.max(0, n) / scale * 100).toFixed(2) + "%";
          bar.append(i);
        }
        head.append(bar);
      }
      head.append(el("span", "grow"));
      if (r.model) head.append(el("code", "ctx-model", r.model));
      head.append(st);

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
      big.append(el("b", "", fmtK(p.tokens)));
      big.append(el("span", "", window ? " / " + fmtK(window) + " " + t("tokens") : " " + t("tokens")));
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
      if ((cache && p.counted !== false) || (live && cacheShown)) {
        // a request under way has read no cache yet: its place is kept,
        // empty, so the grid under it doesn't move up and back down
        const c = el("div", "ctx-stat ctx-cache");
        const hit = cache && p.counted !== false ? cache.read / cache.total : null;
        c.append(el("div", "ctx-k", t("Prompt cache")));
        const cb = el("div", "ctx-big");
        cb.append(el("b", "", hit === null ? "—" : pct(hit)), el("span", "", " " + t("hit")));
        const bar = el("div", "ctx-bar");
        const fillBar = el("i");
        fillBar.style.width = ((hit || 0) * 100).toFixed(1) + "%";
        bar.append(fillBar);
        c.append(cb, bar, el("div", "ctx-sub", hit === null ? " " : t("{cached} cached · {fresh} new", { cached: fmtK(cache.read), fresh: fmtK(cache.total - cache.read) })));
        top.append(c);
        cacheShown = true;
      } else cacheShown = false;

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
      if (!tip.hidden) tip.hidden = true;

      // the legend: a part hovered lights its cells
      const legend = el("div", "ctx-legend");
      for (const kind of PARTS) {
        const part = partOf.get(kind);
        if (!part || part.tokens <= 0) continue;
        const li = el("span", "ctx-leg");
        li.title = t(PART_NOTES[kind]);
        li.append(el("i", "dot k-" + kind), el("span", "n", partName(kind)), el("span", "v", fmtK(part.tokens)));
        li.onpointerenter = () => { grid.dataset.f = kind; };
        li.onpointerleave = () => { delete grid.dataset.f; };
        legend.append(li);
      }
      if (window && window > p.tokens) {
        const li = el("span", "ctx-leg");
        li.append(el("i", "dot free"), el("span", "n", t("Free space")), el("span", "v", fmtK(window - p.tokens)));
        li.onpointerenter = () => { grid.dataset.f = "free"; };
        li.onpointerleave = () => { delete grid.dataset.f; };
        legend.append(li);
      }

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
        card.append(head, top, grid, legend, contents, foot, ...(sp ? [sp] : []), tip);
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
  window.ctxFmt = fmtK;

  // ---------- the Usage page's Context tab ----------

  const CTX_RANGES = [["1", "Today"], ["7", "7 days"], ["30", "30 days"]];
  let ctxDays = "7";
  try { const d = localStorage.getItem("magpie.ctxDays"); if (CTX_RANGES.some(([id]) => id === d)) ctxDays = d; } catch {}
  let ctxData = null, ctxJSON = "", ctxRead = 0, ctxOpen = "", ctxAgent = "all";

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
    const meta = el("div", "ctx-who-sub", [count(a.requests, "1 request", "{n} requests"), count(a.sessions, "1 session", "{n} sessions")].join(" · "));
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
      go.onclick = () => window.openRoute(a.latestId, a.latestTime, { context: true }).catch((e) => status(e.message, "err"));
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
      const cache = last && s.latest.counted && last.tokens ? { read: last.cache || 0, total: last.tokens } : null;
      box.append(ctxCard(r, {
        still: true, cache, place: "session",
        series: s.points,
        crumbs: [agentLabel(s.agent), s.title || s.key.slice(0, 12), t("latest request")],
        onPoint: (pt) => window.openRoute(pt.id, pt.time, { context: true }).catch((e) => status(e.message, "err")),
      }));
    }
    return box;
  }

  function renderContext() {
    const pane = $("#contextPane");
    if (!pane) return;
    const tools = el("div", "sess-tools ctx-tools");
    tools.append(segs(CTX_RANGES.map(([id, n]) => [id, t(n)]), ctxDays, (id) => {
      ctxDays = id;
      try { localStorage.setItem("magpie.ctxDays", id); } catch {}
      loadContext();
    }));
    tools.append(el("span", "grow"));
    pane.replaceChildren(tools);
    if (!ctxData) {
      pane.append(el("p", "usage-note", t("Reading the routing history…")));
      return;
    }
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
    if (!ctxData || ctxData.days !== +days) { ctxData = null; renderContext(); }
    const data = await api("context?days=" + days);
    if (read !== ctxRead) return;
    // the auto refresh redraws only what changed: the same answer keeps the
    // pane (and what the pointer is on), and a new one comes in without its
    // entrance animations. What it is drawn with counts too: the agents'
    // names and icons come with the state, which can answer after a first
    // history, drawn by the agents' ids until then
    const json = JSON.stringify([data, (state.clients || state.agents || []).map((a) => [a.id, a.name, a.icon])]);
    if (ctxData && json === ctxJSON) return;
    const pane = $("#contextPane");
    if (pane) pane.classList.toggle("still", !!ctxData);
    ctxData = data, ctxJSON = json;
    renderContext();
  }
  window.loadContext = loadContext;
  window.renderContext = renderContext;
  // opened on the Context tab (?view=usage): app.js showed it, and may have
  // read the state, before this file was here to load it
  if (view === "usage" && usageTab === "context") loadContext().catch((e) => status(e.message, "err"));
})();
