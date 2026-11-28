// Run with Node's test runner and Playwright on the module path; see README.md.
// 辰林鸭~ on Discord: a provider's models merged into a routing group
// were still in every agent's model list beside the group. On the Gateway
// page's model list each provider's model has a switch that turns it off
// for agents (POST /api/agents-off), one an agent is set to held with the
// agent's name, a group never; the menu in the section head turns off the
// group's models, or the ones the search shows, at once. Each agent's own
// list holds a model turned off there, with the reason. Every language,
// wide and narrow, Chromium and WebKit; no click moves the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const providers = {
  providers: [
    { id: "relay", name: "Relay", icon: "generic", agents: [], models: ["m1", "m2", "m3", "m4", "m5"].map((id) => ({ id, name: id, on: true, efforts: [], images: false, context: 128000 })) },
  ],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", models: 6, calls: [],
    groups: [{ id: "group/pool", name: "Pool", icons: ["generic"], providers: ["Relay"], members: ["relay/m2", "relay/m3"] }] },
};

function fixture(lang) {
  const off = new Set();
  const posts = [];
  const state = {
    agents: [{
      id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
      fields: [{ key: "model", label: "model", value: "magpie/relay/m1", options: [{ value: "magpie/relay/m1", label: "m1" }] }],
      models: { shown: 4, listed: 5, by: [{ name: "Relay", icon: "generic", n: 4 }] },
    }],
    profiles: [], settings: { lang, theme: "light" },
  };
  const agentsOff = () => ({ off: [...off].sort(), keep: { "relay/m1": "Codex" } });
  async function serve(route) {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents-off") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        for (const id of body.ids) if (body.off && id !== "relay/m1") off.add(id); else if (!body.off) off.delete(id);
      }
      return json(agentsOff());
    }
    if (url.pathname === "/api/agent-models/codex") {
      return json({ models: providers.providers[0].models.map((m) => ({ id: "relay/" + m.id, name: m.id, group: "Relay", icon: "generic", context: 128000,
        hidden: off.has("relay/" + m.id), off: off.has("relay/" + m.id), inUse: m.id === "m1" })) });
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  }
  return { serve, posts, off };
}

const rowOf = (page, id) => page.locator("#gwModels .row.model").filter({ has: page.locator(".name", { hasText: new RegExp("^" + id.replace(/[./]/g, "\\$&") + "$") }) });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      test(`${engine} ${width} ${lang}: models turned off for agents on the Gateway page`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-gw-agents-off.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const fx = fixture(lang);
        await page.route("**/*", fx.serve);
        await page.goto("http://magpie.test/?view=gateway");
        await rowOf(page, "relay/m2").locator(".gw-agents").waitFor({ state: "attached" });

        // a group has no switch: it is what agents keep seeing
        assert.equal(await rowOf(page, "group/pool").locator(".gw-agents").count(), 0);
        // the model Codex is set to is held, with its name
        const held = rowOf(page, "relay/m1").locator(".gw-agents");
        assert.equal(await held.getAttribute("aria-disabled"), "true");
        assert.match(await held.getAttribute("title"), /Codex/);

        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        const sw = rowOf(page, "relay/m4").locator(".gw-agents");
        assert.equal(await sw.getAttribute("aria-checked"), "true");
        await rowOf(page, "relay/m4").hover();
        await sw.click();
        await rowOf(page, "relay/m4").and(page.locator(".agents-off")).waitFor();
        assert.deepEqual(fx.posts.at(-1), { ids: ["relay/m4"], off: true });
        assert.equal(await rowOf(page, "relay/m4").locator(".gw-agents").getAttribute("aria-checked"), "false");
        // still the model in the snippets it was: the switch is not the row
        assert.equal(await rowOf(page, "relay/m4").evaluate((r) => r.classList.contains("selected")), false);
        await held.click({ force: true });
        assert.equal(fx.posts.length, 1, "a held switch posts nothing");

        // the head's menu: the group's models at once
        const menu = page.locator("#agentsFor");
        assert.equal(await menu.isVisible(), true);
        await menu.click();
        const items = page.locator(".proto-menu .pm-item");
        await items.first().waitFor();
        assert.equal(await items.count(), 3);
        await items.first().click();
        await rowOf(page, "relay/m3").and(page.locator(".agents-off")).waitFor();
        assert.deepEqual(fx.posts.at(-1), { ids: ["relay/m2", "relay/m3"], off: true });
        assert.deepEqual([...fx.off].sort(), ["relay/m2", "relay/m3", "relay/m4"]);
        // and the ones shown back on
        await menu.click();
        await items.first().waitFor();
        await items.last().click();
        await page.waitForFunction(() => !document.querySelector("#gwModels .agents-off"));
        assert.deepEqual(fx.posts.at(-1), { ids: ["relay/m2", "relay/m3", "relay/m4"], off: false });
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "no click moved the page");

        // the head fits a narrow window
        const head = await page.evaluate(() => {
          const h = document.querySelector("#foldModels").closest(".row-head");
          const r = h.getBoundingClientRect();
          return { over: h.scrollWidth - h.clientWidth, right: Math.max(...[...h.children].filter((c) => !c.hidden).map((c) => c.getBoundingClientRect().right)) - r.right };
        });
        assert.ok(head.over <= 0 && head.right <= 1, "the head overflows: " + JSON.stringify(head));
        assert.deepEqual(errors, []);
      });
    }
  }
}

// Codex's own model list: one turned off on the Gateway page is held, as
// the one it is set to is, and says why; it can't be ticked there.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: an agent's model list holds one turned off for agents`, async () => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      try {
        const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const fx = fixture(lang);
        fx.off.add("relay/m2");
        await page.route("**/*", fx.serve);
        await page.goto("http://magpie.test/");
        const row = page.locator('.row.agent[data-id="codex"]');
        await row.locator(".ag-link").click();
        const entry = row.locator(".ag-exp .ag-chips .ag-quiet");
        await entry.waitFor();
        await page.waitForFunction(() => document.getAnimations().length === 0);
        await entry.click();
        const pop = page.locator(".am-pop:not(.leaving)");
        await pop.waitFor();
        const m2 = pop.locator(".am-mr").filter({ has: page.locator(".n", { hasText: /^m2$/ }) });
        assert.equal(await m2.getAttribute("aria-disabled"), "true");
        assert.equal(await m2.getAttribute("aria-checked"), "false");
        const why = await page.evaluate(() => t("Turned off for agents on the Gateway page"));
        if (lang !== "en") assert.notEqual(why, "Turned off for agents on the Gateway page");
        assert.equal(await m2.getAttribute("title"), why);
        assert.equal(await m2.locator(".am-tag").count(), 0, "not called Current");
        const m1 = pop.locator(".am-mr").filter({ has: page.locator(".n", { hasText: /^m1/ }) });
        assert.equal(await m1.locator(".am-tag").count(), 1, "the one it is set to still is");
        assert.deepEqual(errors, []);
      } finally {
        await browser.close();
      }
    });
  }
}
