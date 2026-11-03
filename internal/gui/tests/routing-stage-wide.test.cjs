// Run with Node's test runner and Playwright on the module path; see README.md.
// refeccd on Discord: in a full-screen window the Routing stage kept the
// agent and magpie in columns of 124px each, the agent cut to "Claude
// Desk…", while the group's member rows ran on across the rest of the
// screen. The two columns are as wide as their words now, at least 124px
// and at most 15% of the stage: at 2560px and 1920px the agent's name, its
// model and magpie's address read whole, the wires still start and end at
// the cards, and at 1100px down to a phone's width nothing is wider than
// it was (124px each, or the narrow layout's two halves) and nothing
// scrolls sideways. Every language, both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const at = (ms) => new Date(now - ms).toISOString();
const models = [
  { id: "yoofang/claude-opus-5-5", name: "claude-opus-5-5", providerName: "YooFang", icon: "generic", context: 1000000, ready: true },
  { id: "cctoken/claude-opus-5-5", name: "claude-opus-5-5", providerName: "CcToken", icon: "generic", context: 1000000, ready: true },
];
const members = models.map((m) => m.id);
const group = { id: "opus", name: "Claude Opus 5.5", members };
const order = () => [
  { id: "yoofang", provider: "yoofang", name: "YooFang", kind: "key", model: "claude-opus-5-5", known: true, used: 10 },
  { id: "cctoken", provider: "cctoken", name: "CcToken", kind: "key", model: "claude-opus-5-5", known: true, used: 20 },
];
const routes = () => [{ id: 7, seq: 7, time: at(3000), agent: "claude-desktop", model: "auto/claude-opus-5-5", provider: "yoofang", tokens: 9000, group, order: order(),
  tries: [{ id: "yoofang", model: "claude-opus-5-5", start: at(2500) }], done: false }];

function serve(lang) {
  const state = { agents: [{ id: "claude-desktop", name: "Claude Desktop", path: "/test/claude-desktop", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let first = true;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      const rs = first ? routes() : [];
      first = false;
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: rs });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models, groups: [{ id: "opus", name: "Claude Opus 5.5", members, routing: "smart", ready: true,
      memberInfo: members.map((id) => ({ id, ready: true, context: 1000000 })) }], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3425" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the stage: its columns, the cards' words and whether each is cut, and
// where the agent's wire starts and ends against the cards
const measure = () => {
  const stage = document.querySelector(".rt-stage"), s = stage.getBoundingClientRect();
  const cut = (e) => e.scrollWidth > e.clientWidth + 1;
  const src = document.querySelector(".rt-src"), hub = document.querySelector(".rt-hub");
  const sr = src.getBoundingClientRect(), hr = hub.getBoundingClientRect();
  const wire = [...document.querySelectorAll(".rt-wires path")].find((p) => (p.getAttribute("d") || "").startsWith("M"));
  const xs = (wire?.getAttribute("d") || "").match(/-?\d+(\.\d+)?/g)?.map(Number) || [];
  const list = document.querySelector(".rt-accts").getBoundingClientRect();
  return {
    stageW: s.width, cols: getComputedStyle(stage).gridTemplateColumns.split(" ").map(parseFloat),
    narrow: document.querySelector(".rt").classList.contains("max560"),
    name: src.querySelector("b").textContent, nameCut: cut(src.querySelector("b")), modelCut: cut(src.querySelector("small")), hubCut: cut(hub.querySelector("small")),
    srcW: sr.width, hubW: hr.width, listW: list.width,
    wireFrom: xs[0], srcRight: sr.right - s.left, wireTo: xs[xs.length - 2], hubLeft: hr.left - s.left,
    sideways: document.documentElement.scrollWidth - document.documentElement.clientWidth,
  };
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [2560, 1920, 1100, 800, 560, 390]) {
      test(`${engine} ${lang} ${width}px: the agent and magpie take the room a wide stage has, and no more than before on a narrow one`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-stage-wide.png`) }).catch(() => {});
          }
          await browser.close();
        });
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        await page.waitForFunction(() => document.querySelector(".rt-src b")?.textContent === "Claude Desktop" && document.querySelectorAll(".rt-accts li:not(.idle)").length >= 2);
        // laid out on the frame after a resize (client.js's stage observer)
        await page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        const m = await page.evaluate(measure);
        const msg = JSON.stringify(m);
        assert.equal(m.sideways, 0, `no sideways scroll: ${msg}`);
        if (m.narrow) {
          // the narrow layout: the agent and magpie side by side, halves of the stage
          assert.equal(m.cols.length, 2, msg);
        } else {
          assert.equal(m.cols.length, 3, msg);
          const most = Math.max(124, m.stageW * .15);
          for (const c of m.cols.slice(0, 2)) {
            assert(c >= 124 - .5, `a card's column keeps its 124px: ${msg}`);
            assert(c <= most + .5, `a card's column is at most 15% of the stage: ${msg}`);
          }
          // where 15% is no more than 124px, as before: the member rows lose nothing
          if (m.stageW * .15 <= 124) for (const c of m.cols.slice(0, 2)) assert(Math.abs(c - 124) <= .5, `as wide as before: ${msg}`);
          // the agent's wire runs from its card to magpie's
          assert(Math.abs(m.wireFrom - m.srcRight) <= 1, `the agent's wire starts at its card: ${msg}`);
          assert(Math.abs(m.wireTo - m.hubLeft) <= 1, `the agent's wire ends at magpie's card: ${msg}`);
        }
        if (width >= 1920) {
          assert(!m.nameCut, `the agent's name reads whole: ${msg}`);
          assert(!m.modelCut, `the agent's model reads whole: ${msg}`);
          assert(!m.hubCut, `magpie's address reads whole: ${msg}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
