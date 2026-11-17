// Run with Node's test runner and Playwright on the module path; see README.md.
// tmr11235 on X: a group's card read "Kimi Code · K3.1 Pro · OpenCode Go ·
// Kimi K3.1 Pro (关闭) · Devin · Kimi K3.1 Pro", the same " · " between a
// member's provider and model as between two members, so nobody could
// tell where one ended. Each member is now one piece, its provider quiet
// and its model after, and the members are parted by the list's comma (an
// arrow in order), never by " · ". Every language, Chromium and WebKit,
// wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "kc/k3.1-pro", name: "K3.1 Pro", provider: "kc", providerName: "Kimi Code", icon: "generic" },
  { id: "ocg/kimi-k3.1-pro", name: "Kimi K3.1 Pro", provider: "ocg", providerName: "OpenCode Go", icon: "generic" },
  { id: "dv/kimi-k3.1-pro", name: "Kimi K3.1 Pro", provider: "dv", providerName: "Devin", icon: "generic" },
];
const ids = models.map((m) => m.id);
const info = (id) => { const m = models.find((x) => x.id === id); return { id, ready: true, provider: m.provider, name: m.providerName, model: m.name }; };
const words = {
  en: { off: "off", join: ", " },
  zh: { off: "关闭", join: "、" },
  "zh-TW": { off: "關閉", join: "、" },
  ja: { off: "オフ", join: "、" },
  de: { off: "aus", join: ", " },
};

function serve(lang) {
  const groups = () => ({
    models, deciders: [], pools: [],
    groups: [
      { id: "auto-kimi-k3-1-pro", ref: "auto/kimi-k3.1-pro", name: "Kimi K3.1 Pro", auto: true, members: ids, off: ["ocg/kimi-k3.1-pro"], routing: "", ready: true, memberInfo: ids.map(info) },
      { id: "chain", name: "Chain", members: ["kc/k3.1-pro", "dv/kimi-k3.1-pro"], off: [], routing: "order", ready: true, memberInfo: ["kc/k3.1-pro", "dv/kimi-k3.1-pro"].map(info) },
    ],
  });
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// what a card's member line is made of: each member's text, its
// provider's and model's colours, and what parts the members
const lineOf = (card) => card.locator(".mem").first().evaluate((line) => ({
  members: [...line.querySelectorAll(".mbr")].map((b) => b.textContent),
  pv: [...line.querySelectorAll(".mbr .pv")].map((e) => getComputedStyle(e).color),
  mn: [...line.querySelectorAll(".mbr .mn")].map((e) => getComputedStyle(e).color),
  seps: [...line.querySelectorAll(".sep")].map((e) => e.textContent),
  loose: [...line.childNodes].filter((n) => n.nodeType === 3 && n.textContent.trim()).length,
  // a member broken over two lines reads as two (the narrow card put
  // "(off)" on a line of its own)
  split: [...line.querySelectorAll(".mbr")].filter((b) => new Set([...b.getClientRects()].map((r) => Math.round(r.bottom))).size > 1).map((b) => b.textContent),
}));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      test(`${engine} ${width} ${lang}: a group's members each read as one, parted by the list's own word`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 1200 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=groups");
        const card = page.locator('.rt-group[data-id="auto-kimi-k3-1-pro"]');
        await card.waitFor();

        const found = await lineOf(card);
        assert.deepEqual(found.members, ["Kimi Code K3.1 Pro", `OpenCode Go Kimi K3.1 Pro (${w.off})`, "Devin Kimi K3.1 Pro"], "each member is its provider then its model");
        assert.deepEqual(found.seps, [w.join, w.join], "the members are parted by the list's comma");
        assert.equal(found.loose, 0, "nothing sits between the members but the separator");
        assert.deepEqual(found.split, [], "no member is broken over two lines");
        for (const s of found.seps) assert(!s.includes("·"), `a member's " · " never parts two members: ${s}`);
        assert.equal(found.pv.length, 3);
        found.pv.forEach((c, i) => assert.notEqual(c, found.mn[i], "the provider is quieter than its model"));

        const chain = await lineOf(page.locator('.rt-group[data-id="chain"]'));
        assert.deepEqual(chain.members, ["Kimi Code K3.1 Pro", "Devin Kimi K3.1 Pro"]);
        assert.deepEqual(chain.seps, [" → "], "in order, an arrow parts them");

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page doesn't scroll sideways (${over}px over)`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
