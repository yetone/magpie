// Run with Node's test runner and Playwright on the module path; see README.md.
// A JS gateway plugin's strategies are routings "<plugin>/<name>"
// (internal/middleware/points.go). The ones the gateway serves now
// (jsRoutings in the settings state) are offered in a provider's editor
// beside the built-in ones, say which plugin routes, and are saved as
// picked. A routing saved whose plugin serves it no more shows as it is,
// saying its accounts go in their listed order. With the JS gateway
// plugins off (plugins/middleware), none is offered. In English and
// Chinese, Chromium and WebKit, at a phone's width too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = { icon: "generic", catalog: "", models: [{ id: "glm-5", name: "GLM-5", on: true }], agents: [], fallback: [], headers: {}, routing: "", affinity: "", ready: true };
const sub = {
  ...base, id: "workbuddy-ai", name: "WorkBuddy AI", chat: "", responses: "", anthropic: "", keyList: [],
  account: {
    agent: "workbuddy-ai", agentName: "WorkBuddy AI", user: "one@example.test", plan: "Pro",
    logins: ["one", "two", "three"].map((n, i) => ({ user: `${n}@example.test`, plan: "Pro", active: i === 0, on: true })),
  },
};
const relay = {
  ...base, routing: "gone/old", id: "relay", name: "Relay", chat: "https://relay.example.invalid/v1", responses: "", anthropic: "", key: { set: true, masked: "sk-…1234" },
  keyList: [1, 2].map((i) => ({ id: `key-${i}`, name: `Key ${i}`, masked: `sk-…000${i}`, active: i === 1, on: true })),
};

function serve(lang, posts, off = []) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light", jsRoutings: [{ id: "spread/fewest-tokens", name: "fewest-tokens", plugin: "spread" }] } };
  const providers = { providers: [sub, relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true,featuresOff:${JSON.stringify(off)}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    // what the real one answers: the state's settings (a provider's editor
    // reads them for its own warm-ups and check-ins)
    if (url.pathname === "/api/settings" && route.request().method() === "GET") return json(state.settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (route.request().method() === "POST" && url.pathname.startsWith("/api/provider")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const zh = lang === "zh";
    const L = { routing: zh ? "路由" : "Routing", save: zh ? "保存" : "Save" };
    test(`${engine} ${lang}: a JS plugin's routings are offered, said and saved; one no plugin serves shows as it is`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      for (const width of [900, 420]) {
        const page = await (await browser.newContext({ viewport: { width, height: 720 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const missing = await page.evaluate(() => [
          "Routed by {name}, a routing of the gateway plugin {plugin}: it orders the accounts or keys for each request. If it fails or takes too long, they are tried in their listed order.",
          "No plugin serves this routing now, so requests go to the accounts or keys in their listed order. Turn on the gateway plugin that serves it on the Plugins page.",
        ].filter((k) => ["zh", "zh-TW", "ja", "de"].some((l) => !I18N[l][k])));
        assert.deepEqual(missing, [], "every string has its translations");
        const field = () => page.locator(".editor label", { hasText: new RegExp(`^${L.routing}$`) }).locator("xpath=following-sibling::div[1]");
        const shown = () => field().locator(".segs").first().evaluate((box) => [...box.querySelectorAll(".opt")].filter((b) => getComputedStyle(b).display !== "none").map((b) => [b.textContent, b.classList.contains("on")]));
        const hint = () => field().locator(":scope > .hint").first().textContent();
        for (const p of [sub, relay]) {
          const where = `${width}px ${p.name}`;
          await page.locator(".row.provider", { hasText: p.name }).first().click();
          await page.locator(".editor .accts").first().waitFor();
          // Routing is under Advanced, open already when one is set (Relay's)
          const adv = page.locator(".editor details.advanced");
          assert.equal(await adv.evaluate((d) => d.open), p === relay, `${where}: Advanced open`);
          if (p !== relay) await adv.locator(":scope > summary").click();
          const opts = await shown();
          assert.ok(opts.some(([n]) => n === "spread/fewest-tokens"), `${where}: offered ${JSON.stringify(opts)}`);
          if (p === relay) {
            // saved, served by nothing: shown as it is, and picked
            assert.deepEqual(opts.at(-1), ["gone/old", true], `${where}: the saved routing`);
            assert.equal(await hint(), await page.evaluate(() => t("No plugin serves this routing now, so requests go to the accounts or keys in their listed order. Turn on the gateway plugin that serves it on the Plugins page.")), where);
          } else assert.ok(!opts.some(([n]) => n === "gone/old"), `${where}: not another provider's`);
          await field().locator(".segs .opt", { hasText: /^spread\/fewest-tokens$/ }).click();
          assert.equal(await hint(), await page.evaluate(() => t("Routed by {name}, a routing of the gateway plugin {plugin}: it orders the accounts or keys for each request. If it fails or takes too long, they are tried in their listed order.", { name: "fewest-tokens", plugin: "spread" })), where);
          assert.ok(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), `${where}: fits`);
          await page.getByRole("button", { name: L.save, exact: true }).click();
          await page.waitForFunction(() => !document.querySelector(".editor"));
          assert.equal(posts.at(-1).body.routing, "spread/fewest-tokens", `${where}: saved`);
        }
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
    test(`${engine} ${lang}: with the JS gateway plugins off, no JS routing is offered`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      await page.route("**/*", serve(lang, [], ["middleware"]));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: sub.name }).first().click();
      await page.locator(".editor .accts").first().waitFor();
      const field = page.locator(".editor label", { hasText: new RegExp(`^${L.routing}$`) }).locator("xpath=following-sibling::div[1]");
      const names = await field.locator(".segs").first().evaluate((box) => [...box.querySelectorAll(".opt")].filter((b) => getComputedStyle(b).display !== "none").map((b) => b.textContent));
      assert.ok(!names.includes("spread/fewest-tokens"), JSON.stringify(names));
      assert.ok(names.length >= 5, JSON.stringify(names));
    });
  }
}
