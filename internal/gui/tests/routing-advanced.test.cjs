// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's Routing, Rate limited and Stays are changed by few, so they
// sit under the editor's Advanced, at its top, folded by default (the
// owner, 11-12: "这些也要折叠在高级里"). Advanced opens on its own when one
// of them isn't its default: a routing other than Smart, Stays other than
// Auto, or Rate limited going to the back. For accounts and keys alike, in
// every language, both engines.
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
    logins: ["one", "two"].map((n, i) => ({ user: `${n}@example.test`, plan: "Pro", active: i === 0, on: true })),
  },
};
const relay = {
  ...base, id: "relay", name: "Relay", chat: "https://relay.example.invalid/v1", responses: "", anthropic: "", key: { set: true, masked: "sk-…1234" },
  keyList: [1, 2].map((i) => ({ id: `key-${i}`, name: `Key ${i}`, masked: `sk-…000${i}`, active: i === 1, on: true })),
};

function serve(lang, p) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const providers = { providers: [p], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Routing, Rate limited and Stays fold under Advanced, open when set`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const p of [sub, relay]) {
        for (const [set, with_] of [["nothing", {}], ["routing", { routing: "order" }], ["stays", { affinity: "session" }], ["rate limited", { sink: true }]]) {
          const at = `${p.name}, ${set} set`;
          const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, { ...p, ...with_ }));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: p.name }).first().click();
          await page.locator(".editor .accts").first().waitFor();
          const adv = page.locator(".editor details.advanced");
          const said = await page.evaluate(() => [t("Routing"), t("Rate limited"), t("Stays")]);
          // each label is Advanced's, the first of its fields
          const where = await page.evaluate((said) => {
            const adv = document.querySelector(".editor details.advanced");
            const labels = [...document.querySelectorAll(".editor label")];
            return { inside: said.map((s) => !!labels.find((l) => l.textContent === s && adv.contains(l))), first: adv.querySelector(".inner > label")?.textContent };
          }, said);
          assert.deepEqual(where.inside, [true, true, true], `${at}: ${JSON.stringify(where)}`);
          assert.equal(where.first, said[0], `${at}: Routing first under Advanced`);
          assert.equal(await adv.evaluate((d) => d.open), set !== "nothing", `${at}: Advanced open`);
          const routing = page.locator(".editor details.advanced .route-pick").first();
          assert.equal(await routing.isVisible(), set !== "nothing", `${at}: Routing shown`);
          assert.deepEqual(errors, [], at);
          await page.context().close();
        }
      }
    });
  }
}
