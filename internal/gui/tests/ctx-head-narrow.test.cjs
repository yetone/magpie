// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing: at every window width the context window card's head keeps its
// parts apart (packinggoods on Discord: in a narrow window the provider's
// icon was drawn over the end of "Context window", the model id beside it).
// The title is never cut or run under its neighbour; the long model id is
// what gives way, ending in an ellipsis, and the icon before it stays clear
// of the title and of the state pill. Open and folded, with and without the
// agent, a live request and a counted one, every GUI language, Chromium and
// WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (ms) => new Date(ms).toISOString();
// the reporter's model: a remote magpie used as a provider
const model = "remote-magpie/claude/claude-opus-5-5";
const promptOf = (window) => ({
  window, tokens: 186000, counted: false, turns: 1,
  parts: [
    { kind: "system", tokens: 4000, items: [{ name: "prompt", tokens: 4000 }] },
    { kind: "chat", tokens: 182000, items: [{ name: "turn", tag: "turns", n: 1, tokens: 182000 }] },
  ],
});
function req(agent, live, window) {
  const prompt = promptOf(window);
  const seat = { id: "acc@me", provider: "remote-magpie", name: "Remote", who: "me@example.com", kind: "account", model, known: true };
  const r = {
    id: 101, seq: 1010, time: iso(Date.now() - 5000), agent, session: "s-1", model, provider: "remote-magpie",
    order: [seat], tries: [{ id: seat.id, model, start: iso(Date.now() - 5000), done: !live, status: live ? 0 : 200, ms: 3000 }],
    done: !live, status: live ? 0 : 200, ms: 3000, tokens: 900, usage: live ? [] : [{ in: 12000, out: 900 }], prompt,
  };
  if (!agent) delete r.agent;
  return r;
}

function serve(lang, r) {
  const state = { agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return; // no more requests come in
      return json({ mine: true, now: new Date().toISOString(), seq: r.seq, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [r] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

// the head's drawn parts, each as the box its ink takes: the title's text
// (a range, so text run out of its box counts where it is drawn), each icon,
// the model id as drawn (clipped to its box), the state pill
const parts = (page) => page.evaluate(() => {
  const assert = (ok, m) => { if (!ok) throw new Error(m); };
  const head = document.querySelector(".rt-ctx .ctx-head");
  const card = head.closest(".ctx-card");
  const box = (r) => ({ l: r.left, r: r.right, t: r.top, b: r.bottom, w: r.width });
  // what of a box is drawn: cut to every ancestor in the head that clips
  const drawn = (e) => {
    let r = e.getBoundingClientRect(), l = r.left, rt = r.right, t = r.top, b = r.bottom;
    for (let a = e.parentElement; a && a !== head.parentElement; a = a.parentElement) {
      if (getComputedStyle(a).overflowX === "visible") continue;
      const c = a.getBoundingClientRect();
      l = Math.max(l, c.left); rt = Math.min(rt, c.right); t = Math.max(t, c.top); b = Math.min(b, c.bottom);
    }
    return { l, r: rt, t, b, w: Math.max(0, rt - l), full: r.width };
  };
  const shown = (e) => !!e && e.getClientRects().length > 0 && e.getBoundingClientRect().width > 0;
  const out = [];
  const title = head.querySelector(".ctx-title");
  if (title) {
    const rg = document.createRange();
    rg.selectNodeContents(title);
    // the text against the box it sits in: the fold's button when folding
    // is on, the head otherwise
    const tr = rg.getBoundingClientRect(), holder = (title.closest(".ctx-fold") || head).getBoundingClientRect();
    out.push({ n: "title", ...box(tr), cut: tr.right > holder.right + 0.5 || tr.left < holder.left - 0.5 });
  }
  for (const [n, s] of [["agent icon", ".ctx-who .ic"], ["agent name", ".ctx-who-n"], ["lab icon", ".ctx-what .ic"], ["model", ".ctx-model"], ["short", ".ctx-short"], ["state", ".ctx-state"]]) {
    const e = head.querySelector(s);
    if (!shown(e)) continue;
    const d = drawn(e);
    // an icon is drawn whole or not at all, never cut by the route's edge
    if (n.endsWith("icon") && d.w > 0.5) assert(d.w >= d.full - 0.5, `the ${n} is cut: ${JSON.stringify(d)}`);
    if (d.w > 0.5) out.push({ n, ...d });
  }
  const hr = head.getBoundingClientRect();
  return { parts: out, head: box(hr), card: box(card.getBoundingClientRect()), shut: card.classList.contains("shut"), vw: innerWidth, model: head.querySelector(".ctx-model")?.textContent };
});

const overlaps = (a, b) => Math.min(a.r, b.r) - Math.max(a.l, b.l) > 0.5 && Math.min(a.b, b.b) - Math.max(a.t, b.t) > 0.5;

function check(seen, where) {
  const ps = seen.parts;
  const title = ps.find((p) => p.n === "title");
  assert.ok(title && !title.cut, `${where}: the title is whole ${JSON.stringify(title)}`);
  for (let i = 0; i < ps.length; i++) {
    for (let j = i + 1; j < ps.length; j++) {
      assert.ok(!overlaps(ps[i], ps[j]), `${where}: ${ps[i].n} and ${ps[j].n} overlap ${JSON.stringify([ps[i], ps[j]])}`);
    }
    assert.ok(ps[i].r <= seen.card.r + 0.5, `${where}: ${ps[i].n} runs past the card ${JSON.stringify(ps[i])} ${JSON.stringify(seen.card)}`);
  }
  assert.equal(seen.model, model);
  assert.ok(ps.some((p) => p.n === "state"), `${where}: the state pill is drawn`);
}

const langs = ["en", "zh", "zh-TW", "ja", "de"];
// the reporter's window (~640px of content beside the sidebar) and every
// width around it, down to a phone's
const widths = [1100, 980, 900, 860, 820, 780, 740, 700, 660, 620, 580, 540, 500, 460, 420, 380, 340];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the context window's head keeps its parts apart at every width`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of langs) {
      // the reporter's (no agent, live, the window not known) and its neighbours
      for (const [agent, live, window] of [[null, true, 0], ["claude", true, 0], ["claude", false, 0], ["claude", false, 272000], [null, true, 272000]]) {
        await t.test(`${lang} ${agent || "no agent"} ${live ? "live" : "counted"} ${window ? "window " + window : "window not known"}`, async () => {
          const context = await browser.newContext({ viewport: { width: widths[0], height: 900 } });
          t.after(() => context.close());
          const page = await context.newPage();
          await page.addInitScript(() => { try { localStorage.setItem("magpie.ctxShut", "0"); } catch {} });
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, req(agent, live, window)));
          await page.goto("http://magpie.test/?view=routing");
          await page.locator(".rt-ctx .ctx-head .ctx-route").waitFor();
          for (const shut of [false, true]) {
            if (shut) {
              await page.setViewportSize({ width: widths[0], height: 900 });
              await page.locator(".rt-ctx .ctx-fold").click();
              await page.locator(".rt-ctx .ctx-card.shut").waitFor();
            }
            for (const w of widths) {
              await page.setViewportSize({ width: w, height: 900 });
              // the card's width classes follow a ResizeObserver: a frame on
              await page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
              const seen = await parts(page);
              assert.equal(seen.shut, shut);
              check(seen, `${shut ? "folded" : "open"} at ${w}px`);
            }
          }
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
