// Run with Node's test runner and Playwright on the module path; see README.md.
// real-jacket, #1581: on the Usage page a card's grip (drawn on hover by its
// logo) stood on the card's left border, while the Agents row's grip sits
// 4px inside the list's. Each grip had its own `left`, copied from the
// Agents row while the box around it had its own padding. Every kind of
// grip that is drawn in a row's left margin now stands at least 4px inside
// the edge the reader sees (the card's or the list's border, or the edge
// its shadow draws): the Agents, Providers, Usage and Routing pages, a
// group's members in its editor, an agent's model order, and the tray
// panel's agents and Arrange rows. The grips still show only on hover (the
// owner: 一直出现太丑了). Chromium and WebKit, every language, a wide window
// and a narrow one; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const prov = (id) => ({
  id, name: id.toUpperCase(), icon: "generic", host: id + ".example.com", chat: "https://" + id + ".example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m", name: "M", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…" + id }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});
const quotas = [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "x@y.z", windows: [{ name: "5-hour", used: 8 }, { name: "Weekly", used: 40 }] },
  { provider: "kimi", name: "Kimi", icon: "kimi-color", windows: [{ name: "Weekly", used: 30 }] },
  { provider: "deepseek", name: "DeepSeek", icon: "deepseek-color", windows: [], balance: "¥12.30" },
];
const models = [
  { id: "a/one", name: "one", providerName: "A", icon: "generic" },
  { id: "a/two", name: "two", providerName: "A", icon: "generic" },
  { id: "b/three", name: "three", providerName: "B", icon: "generic" },
];
const members = ["a/one", "a/two", "b/three"];
const groups = {
  models, pools: [], deciders: [], found: true,
  groups: [
    { id: "g", name: "G", members, ready: true, routing: "order", memberInfo: members.map((id) => ({ id, ready: true })) },
    { id: "h", name: "H", members: ["b/three"], ready: true, memberInfo: [{ id: "b/three", ready: true }] },
  ],
};
const agentModels = [
  { id: "relay/m1", name: "m1", group: "Relay", icon: "generic" },
  { id: "relay/m2", name: "m2", group: "Relay", icon: "generic" },
  { id: "relay/m3", name: "m3", group: "Relay", icon: "generic" },
];

function serve(lang, web) {
  const settings = { lang, theme: "light", tray: "panel", usageOrder: [], panelUsageHidden: [] };
  const agent = (id, name) => ({
    id, name, icon: "generic", path: "/fixture/" + id, wired: true,
    fields: [{ key: "model", label: "model", value: "m1", options: [{ value: "m1", label: "m1", ref: "relay/m1" }] }],
    models: { shown: 3, listed: 3, by: [{ name: "Relay", icon: "generic", n: 3 }] },
  });
  const state = { agents: [agent("codex", "Codex"), agent("beta", "Beta"), agent("gamma", "Gamma")], profiles: [], settings };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [prov("a"), prov("b")], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json({ calls: 0, cost: 0, days: [], agents: [], models: [], keys: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname.startsWith("/api/agent-models/")) return json({ models: agentModels, ordered: false });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// The grip under the pointer and the edge the reader sees left of it: the
// inner side of the first box out from the row that draws a border or a
// shadow. A grip drawn as ::before is read from its handle's box and its
// own left; one drawn as an element, from its box. `shown` is its opacity:
// the measure is taken with the pointer on the row, where it is drawn.
const measure = (page, row, grip) => page.evaluate(([row, grip]) => {
  const r = document.querySelector(row);
  if (!r) return null;
  const g = r.querySelector(grip);
  let left, width, opacity;
  if (g.classList.contains("grip")) {
    const b = g.getBoundingClientRect(), s = getComputedStyle(g);
    left = b.left; width = b.width; opacity = s.opacity;
  } else {
    const s = getComputedStyle(g, "::before");
    left = g.getBoundingClientRect().left + parseFloat(s.left); width = parseFloat(s.width); opacity = s.opacity;
  }
  let box = r;
  for (; box && box !== document.body; box = box.parentElement) {
    const s = getComputedStyle(box);
    if (parseFloat(s.borderLeftWidth) > 0 || s.boxShadow !== "none") break;
  }
  const edge = box.getBoundingClientRect().left + box.clientLeft;
  return { inset: +(left - edge).toFixed(2), left, width, opacity, box: box.tagName.toLowerCase() + "." + [...box.classList].join(".") };
}, [row, grip]);

// each kind of grip: where it is, how to bring it up, and its row
const kinds = [
  { name: "Agents", mode: "window", view: "agents", row: '#agents > .row.agent[data-id="beta"]', grip: ".ag-handle" },
  { name: "Providers", mode: "window", view: "providers", row: "#providers > .row.provider", grip: ".pv-handle" },
  { name: "Usage", mode: "window", view: "usage", row: ".subscription-card", grip: ".us-handle" },
  { name: "Routing groups", mode: "window", view: "groups", row: '.rt-groups > .rt-group[data-id="g"]', grip: ".rt-ghandle" },
  {
    name: "a group's members", mode: "window", view: "groups", row: ".rt-gedit .fbrow:has(.rt-mhandle)", grip: ".rt-mhandle",
    open: async (page) => {
      await page.locator('.rt-groups > .rt-group[data-id="g"]').waitFor();
      await page.evaluate(() => document.querySelector('.rt-groups > .rt-group[data-id="g"]').click());
      await page.locator(".rt-gedit .fbrow .rt-mhandle").first().waitFor();
    },
  },
  {
    name: "an agent's model order", mode: "window", view: "agents", row: ".am-pop:not(.leaving) .am-or", grip: ".grip",
    open: async (page) => {
      const row = page.locator('.row.agent[data-id="codex"]');
      await row.locator(".ag-link").click();
      await row.locator(".ag-exp .ag-chips .ag-quiet").click();
      const pop = page.locator(".am-pop:not(.leaving)");
      await pop.locator(".am-seg button").last().click();
      await pop.locator(".am-or").first().waitFor();
    },
  },
  { name: "the panel's agents", mode: "panel", view: "agents", row: '#agents > .row.agent[data-id="beta"]', grip: ".ag-handle" },
  {
    name: "the panel's Arrange", mode: "panel", row: "#panelQuota .pq-arow", grip: ".pq-ahandle", tab: "usage",
    open: async (page) => {
      await page.locator("#panelQuota .pq-arrange").click();
      await page.locator("#panelQuota .pq-arow").first().waitFor();
    },
  },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: every row's grip stands inside its card's edge`, { timeout: 600000 }, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [980, 420]) for (const k of kinds) {
      await t.test(`${lang} ${width}px: ${k.name}`, async () => {
        // the window is no narrower than 560px (windowMinW); narrower, it
        // is the browser page (magpie web, a phone)
        const web = k.mode === "window" && width < 560;
        const viewport = k.mode === "panel" ? { width: Math.min(width, 440), height: 640 } : { width, height: 760 };
        const ctx = await browser.newContext({ viewport, reducedMotion: "reduce" });
        if (k.tab) await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.panelTab", tab); } catch {} }, k.tab);
        const page = await ctx.newPage(), errors = [];
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        try {
          await page.route("**/*", serve(lang, web));
          await page.goto("http://magpie.test/?" + (web ? "" : `mode=${k.mode}&`) + (k.view ? `view=${k.view}` : ""));
          if (k.open) await k.open(page);
          const row = page.locator(k.row).first();
          await row.waitFor();
          await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => {}))));
          // drawn only on hover: away, it is not there
          await page.mouse.move(viewport.width - 2, viewport.height - 2);
          await page.waitForFunction(([r, g]) => {
            const e = document.querySelector(r)?.querySelector(g);
            return e && getComputedStyle(e, e.classList.contains("grip") ? null : "::before").opacity === "0";
          }, [k.row, k.grip]);
          // on the row's left padding, by the grip, which no other element
          // covers; the page brought to it by the test, as the reader would
          // the row brought into sight with the wheel, as the reader would (a
          // scroll by code the page puts back); its box taken once it holds
          // still
          const still = () => row.evaluate((r) => new Promise((done) => {
            let last = "", same = 0;
            const look = () => {
              const x = r.getBoundingClientRect(), now = [x.x, x.y, x.width, x.height].join();
              same = now === last ? same + 1 : 0;
              last = now;
              if (same >= 5) return done({ x: x.x, y: x.y, width: x.width, height: x.height });
              requestAnimationFrame(look);
            };
            look();
          }));
          let b = await still();
          for (let i = 0; i < 6 && (b.y < 60 || b.y + b.height > viewport.height - 20); i++) {
            await page.mouse.move(viewport.width / 2, viewport.height / 2);
            await page.mouse.wheel(0, b.y + b.height / 2 - viewport.height / 2);
            b = await still();
          }
          await page.mouse.move(b.x + 6, b.y + b.height / 2);
          await page.waitForFunction(([r, g]) => {
            const e = document.querySelector(r)?.querySelector(g);
            return e && getComputedStyle(e, e.classList.contains("grip") ? null : "::before").opacity === "1";
          }, [k.row, k.grip]);
          const m = await measure(page, k.row, k.grip);
          if (process.env.GRIP_LOG) console.log(engine, lang, width, k.name, JSON.stringify(m));
          assert(m.inset >= 4, `${k.name}'s grip is ${m.inset}px from its edge (${m.box}), not 4px inside it: ${JSON.stringify(m)}`);
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
