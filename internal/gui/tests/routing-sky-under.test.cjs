// Run with Node's test runner and Playwright on the module path; see README.md.
// penershu on X (v0.1.2123): with two agents sending at once through one
// group, the Routing stage's magpies were drawn over the cards — one over
// the hub's "127.0.0.1:3425", its request's dot beside it, another over a
// member's row — and the row smart routing's intent picked ran its long
// intent off the row's right edge, its name squeezed out. The magpies fly
// over the cards, as before, but nothing at rest covers a card's words (the
// owner, 11-09: "鸟要在卡片上面", after 869c4d112 put them under), a dot held
// at its seat or waiting at magpie sits whole beside the card, and a long
// intent is cut short on its row, read whole in its tooltip, the name kept.
// Three agents at once (one still waiting at magpie), at a wide window (the
// reporter's, where a row says it on its name's line), at 1100px, at the
// narrow stage and at a phone's width, in every language, both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const INTENT = "Design architecture, define module boundaries, or compare substantial technical approaches. Exclude routine implementation";
const models = [
  { id: "a/fast", name: "fast", providerName: "A", icon: "generic", context: 256000, ready: true },
  { id: "b/opus", name: "opus", providerName: "B", icon: "generic", context: 1000000, ready: true },
];
const groups = () => ({
  models,
  groups: [{ id: "jd", name: "jd-auto-balanced", members: ["a/fast", "b/opus"], routing: "order", ready: true, classifier: "c/cls",
    memberInfo: [{ id: "a/fast", ready: true, context: 256000 }, { id: "b/opus", ready: true, context: 1000000 }],
    rules: [{ use: "b/opus", intent: INTENT }] }],
  pools: [],
});
const now = Date.now();
const order = () => [{ id: "b", provider: "b", name: "B", kind: "account", model: "opus", known: true, used: 10 },
  { id: "a", provider: "a", name: "A", kind: "account", model: "fast", known: true, used: 20 }];
const group = { id: "jd", name: "jd-auto-balanced", members: ["a/fast", "b/opus"] };
const at = (ms) => new Date(now - ms).toISOString();
// claude's request is with b (the intent's pick), codex's with a, and
// kimi's waits at magpie for routing to pick
const routes = () => [
  { id: 7, seq: 7, time: at(3000), agent: "claude", model: "group/jd", provider: "b", tokens: 9000, group, order: order(),
    tries: [{ id: "b", model: "opus", start: at(2500) }], done: false,
    rule: { n: 1, use: "b/opus", when: [`intent "${INTENT}"`], turn: 3, tokens: 9000, classified: { by: "c/cls", intents: [INTENT], intent: INTENT, ms: 300 } } },
  { id: 8, seq: 8, time: at(2000), agent: "codex", model: "group/jd", provider: "a", tokens: 4000, group, order: order().slice(1),
    tries: [{ id: "a", model: "fast", start: at(1500) }], done: false },
  { id: 9, seq: 9, time: at(1000), agent: "kimi", model: "group/jd", provider: "", tokens: 2000, group, order: [], tries: [], done: false },
];

function serve(lang, live) {
  const state = { agents: [
    { id: "claude", name: "Claude Code", path: "/test/claude", fields: [] },
    { id: "codex", name: "Codex", path: "/test/codex", fields: [] },
    { id: "kimi", name: "Kimi Code", path: "/test/kimi", fields: [] },
  ], profiles: [], settings: { lang, theme: "light" } };
  let first = true;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) { // what the test sends next, when it does
        if (live.queued.length) return json(live.queued.shift());
        live.pending.push(r);
        return;
      }
      const rs = first ? routes() : [];
      first = false;
      return json({ mine: true, now: new Date().toISOString(), seq: 9, totals: { requests: 3, rerouted: 0, errors: 0 }, routes: rs });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3425" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// covered: the words on the stage's cards that a magpie or a dot is drawn
// over. The sky is over the cards (the owner, 11-09: "鸟要在卡片上面"), so
// what sits at rest must sit beside the words, never on them.
const covered = () => {
  const words = [...document.querySelectorAll(".rt-stage .rt-node b, .rt-stage .rt-node small, .rt-stage .rt-node i, .rt-accts li:not(.rt-gap) > b, .rt-accts li:not(.rt-gap) > em, .rt-accts li.rt-sub > b")]
    .map((w) => [w, w.getBoundingClientRect()]).filter(([, r]) => r.width && r.height);
  const things = [...document.querySelectorAll(".rt-sky .rt-flier:not(.away) :is(.body, .tail, .wing, .belly), .rt-sky .pkt:not([visibility])")];
  const out = [];
  for (const th of things) {
    const a = th.getBoundingClientRect();
    if (!a.width || !a.height) continue;
    for (const [w, r] of words) {
      if (a.right > r.left + 1 && a.left < r.right - 1 && a.bottom > r.top + 1 && a.top < r.bottom - 1) out.push(`${th.getAttribute("class")} over ${w.tagName.toLowerCase()} "${w.textContent.slice(0, 30)}"`);
    }
  }
  return out;
};

// crossing: watches every frame a magpie flies through magpie's box, and
// keeps what of the sky was on the hub's words then: its dot and its body.
// The magpie trails its dot by its length, so as it leaves the box by the
// side its tail or a wing brushes the words for a frame or two; across
// them (the way before) dot and body went over the name and address.
const crossing = () => {
  window.crossed = { frames: 0, hits: [] };
  const words = () => [...document.querySelectorAll(".rt-stage .rt-hub b, .rt-stage .rt-hub small, .rt-stage .rt-hub i")].map((w) => [w, w.getBoundingClientRect()]).filter(([, r]) => r.width && r.height);
  const frame = () => {
    if (document.querySelector(".rt-sky path.via[d]")) {
      window.crossed.frames++;
      const ws = words();
      for (const th of document.querySelectorAll(".rt-sky .rt-flier:not(.away) .body, .rt-sky .pkt:not([visibility])")) {
        const a = th.getBoundingClientRect();
        for (const [w, r] of ws) if (a.right > r.left + 1 && a.left < r.right - 1 && a.bottom > r.top + 1 && a.top < r.bottom - 1) window.crossed.hits.push(`${th.getAttribute("class")} over "${w.textContent}"`);
      }
    }
    requestAnimationFrame(frame);
  };
  requestAnimationFrame(frame);
};

// above: whether the sky is drawn over the cards. A probe filling the sky
// is asked for at the middle of each card.
const above = () => {
  const sky = document.querySelector(".rt-sky");
  const probe = document.createElementNS("http://www.w3.org/2000/svg", "rect");
  probe.setAttribute("x", "-5000"); probe.setAttribute("y", "-5000");
  probe.setAttribute("width", "10000"); probe.setAttribute("height", "10000");
  probe.style.pointerEvents = "all";
  sky.append(probe);
  sky.style.pointerEvents = "auto";
  try {
    return [...document.querySelectorAll(".rt-stage .rt-node, .rt-accts li:not(.rt-gap)")].map((c) => {
      const r = c.getBoundingClientRect(), hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!hit && sky.contains(hit);
    });
  } finally { probe.remove(); sky.style.pointerEvents = ""; }
};

// dots: each request's dot at rest, and the cards it overlaps
const dots = () => {
  const cards = [...document.querySelectorAll(".rt-stage .rt-node, .rt-accts li:not(.rt-gap)")].map((e) => [e, e.getBoundingClientRect()]);
  return [...document.querySelectorAll(".rt-sky .pkt")].filter((d) => d.getAttribute("visibility") !== "hidden").map((d) => {
    const r = d.getBoundingClientRect(), cx = r.left + r.width / 2, cy = r.top + r.height / 2, rad = Number(d.getAttribute("r")) || 4.5;
    const over = cards.filter(([, c]) => cx + rad > c.left + .5 && cx - rad < c.right - .5 && cy + rad > c.top + .5 && cy - rad < c.bottom - .5)
      .map(([e]) => e.className || e.tagName);
    // the nearest card: how far the dot is from it
    const gap = Math.min(...cards.map(([, c]) => Math.hypot(Math.max(c.left - cx, 0, cx - c.right), Math.max(c.top - cy, 0, cy - c.bottom))));
    return { held: d.classList.contains("held"), over, gap };
  });
};

// the row the intent picked: what it says, and whether its name kept room
const intentRow = (intent) => {
  const em = [...document.querySelectorAll(".rt-accts li > em")].find((e) => e.textContent.includes(intent));
  if (!em) return null;
  const li = em.parentElement, b = li.querySelector(":scope > b"), e = em.getBoundingClientRect(), l = li.getBoundingClientRect();
  return { emRight: e.right, liRight: l.right, cardRight: document.querySelector(".rt").getBoundingClientRect().right, name: b.getBoundingClientRect().width, title: em.title, text: em.textContent, cut: em.scrollWidth > em.clientWidth + 1, wraps: getComputedStyle(em).whiteSpace !== "nowrap" };
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1600, 1100, 560, 390]) {
      test(`${engine} ${lang} ${width}px: three agents at once, the magpies fly over the cards, beside their words, and a long intent stays in its row`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 1000 } })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-sky-under.png`) }).catch(() => {});
          }
          await browser.close();
        });
        const live = { pending: [], queued: [] };
        await page.route("**/*", serve(lang, live));
        await page.goto("http://magpie.test/?view=routing");
        // the two requests routing picked for are held at their seats, the
        // third's dot waits at magpie
        await page.waitForFunction(() => document.querySelectorAll(".rt-sky .pkt.held").length === 2);
        await page.waitForFunction(() => document.querySelectorAll(".rt-sky .pkt:not([visibility])").length === 3);
        await page.waitForTimeout(900); // the third's flight to magpie lands, the carriers fly off
        const flown = await page.evaluate(() => document.querySelectorAll(".rt-sky .rt-flier").length);
        assert(flown >= 1, "the magpie carrying the waiting request stays with it at magpie");

        const over = await page.evaluate(above);
        assert(over.length >= 3 && over.every(Boolean), `the magpies fly over the cards: ${JSON.stringify(over)}`);
        assert.deepEqual(await page.evaluate(covered), [], "a magpie or dot at rest is never on a card's words");
        const ds = await page.evaluate(dots);
        assert.equal(ds.length, 3, JSON.stringify(ds));
        for (const d of ds) {
          assert.deepEqual(d.over, [], `a ${d.held ? "held" : "waiting"} dot must sit whole beside its card, not under or over it: ${JSON.stringify(ds)}`);
          assert(d.gap <= 12, `a ${d.held ? "held" : "waiting"} dot must sit at its card (${d.gap.toFixed(1)}px off): ${JSON.stringify(ds)}`);
        }

        // routing picks a for kimi's: its magpie flies on through magpie's
        // box, along its edge, never over its name or address
        await page.evaluate(crossing);
        const next = { ...routes()[2], provider: "a", order: order().slice(1), tries: [{ id: "a", model: "fast", start: new Date().toISOString() }] };
        const reply = { mine: true, now: new Date().toISOString(), seq: 10, totals: { requests: 3, rerouted: 0, errors: 0 }, routes: [next] };
        if (live.pending.length) await live.pending.shift().fulfill({ json: reply }); else live.queued.push(reply);
        await page.waitForFunction(() => document.querySelectorAll(".rt-sky .pkt.held").length === 3, null, { timeout: 8000 });
        const crossed = await page.evaluate(() => window.crossed);
        assert(crossed.frames >= 5, `the magpie flew through magpie's box: ${JSON.stringify(crossed)}`);
        assert.deepEqual([...new Set(crossed.hits)], [], "a magpie crossing magpie's box keeps off its words");

        const row = await page.evaluate(intentRow, INTENT);
        assert(row, "the row the intent picked says the intent");
        assert(row.emRight <= row.liRight + .5, `the intent runs off its row: ${JSON.stringify(row)}`);
        assert(row.liRight <= row.cardRight + .5, `the row runs off the stage's card: ${JSON.stringify(row)}`);
        if (width >= 1600) assert(!row.wraps, `a wide list says it on the name's line: ${JSON.stringify(row)}`);
        if (!row.wraps) {
          assert(row.name >= 60, `the row's name keeps room beside the intent: ${JSON.stringify(row)}`);
          assert(row.cut, `a long intent is cut short on one line: ${JSON.stringify(row)}`);
        }
        assert.equal(row.title, row.text, "the whole intent is in the tooltip");
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(wide <= 0, `no sideways scroll: ${wide}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
