// Run with Node's test runner and Playwright on the module path; see README.md.
// yckonnnn on X: Volcengine's pay as you go and its Agent Plan, a key of
// each in one provider. Each key row of a vendor whose plans take keys of
// their own (keyPlans) has a plan chip saying which plan it is for; a
// click opens the app's own menu of the plans, and a pick saves it
// (keys/plan), the provider's own plan as none. A key alone on the
// provider's plan, and a provider whose keys are on one plan, have no
// chip. A click scrolls nothing, nothing runs off a 420px window. In
// every language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ark = "https://ark.cn-beijing.volces.com";
const preset = {
  id: "volcengine", name: "Volcengine Ark", icon: "volcengine-color", kind: "vendor", keyPlans: true, regionLabel: "Plan",
  chat: ark + "/api/coding/v3", responses: ark + "/api/coding/v3", anthropic: ark + "/api/coding",
  regions: [
    { id: "coding", name: "Coding Plan", chat: ark + "/api/coding/v3", responses: ark + "/api/coding/v3", anthropic: ark + "/api/coding" },
    { id: "agent", name: "Agent Plan", chat: ark + "/api/plan/v3", responses: ark + "/api/plan/v3", anthropic: ark + "/api/plan" },
    { id: "api", name: "Pay as you go", chat: ark + "/api/v3", responses: ark + "/api/v3" },
  ],
};
const volc = (keys) => ({
  id: "volc", name: "Volc", preset: "volcengine", icon: "volcengine-color", chat: ark + "/api/coding/v3", responses: ark + "/api/coding/v3", anthropic: ark + "/api/coding", catalog: "",
  models: [{ id: "kimi-k3", name: "", on: true }], agents: [], fallback: [], headers: {}, routing: "",
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: keys,
});
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-5.5", name: "", on: true }], agents: [], fallback: [], headers: {}, routing: "",
  key: { set: true, masked: "sk-…r1" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [{ id: "rrrrrrrrr1", name: "r1", masked: "sk-…r1", on: true, active: true, plan: "api" }, { id: "rrrrrrrrr2", name: "r2", masked: "sk-…r2", on: true }],
};

function serve(lang, st) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    const list = () => ({ providers: [volc(st.keys), relay], presets: [preset], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(list());
    if (url.pathname === "/api/keys/plan") {
      const body = request.request().postDataJSON();
      st.posts.push(body);
      st.keys = st.keys.map((k) => (k.id === body.ref ? { ...k, plan: body.plan || undefined } : k));
      return json(list());
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

async function open(t, engine, lang, width, keys) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const st = { keys, posts: [] };
  await page.route("**/*", serve(lang, st));
  await page.goto("http://magpie.test/?view=providers");
  const say = (key) => page.evaluate(([lang, key]) => (lang !== "en" && I18N[lang]?.[key]) || key, [lang, key]);
  return { page, errors, say, st };
}

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
const three = () => [
  { id: "aaaaaaaaaa", name: "coding", masked: "sk-…one", on: true, active: true },
  { id: "bbbbbbbbbb", name: "payg", masked: "sk-…two", on: true, plan: "api" },
  { id: "cccccccccc", name: "agent", masked: "sk-…three", on: true, plan: "agent" },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1000]) {
      test(`${engine} ${lang} ${width}px: each key says its plan, and a pick saves it`, async (t) => {
        const { page, errors, say, st } = await open(t, engine, lang, width, three());
        await page.locator(".row.provider", { hasText: "Volc" }).first().click();
        const row = (id) => page.locator(`.editor .accts.keys .acc[data-account-id="${id}"]`);
        await row("aaaaaaaaaa").waitFor();
        const names = {};
        for (const r of preset.regions) names[r.id] = await say(r.name);
        for (const [id, plan] of [["aaaaaaaaaa", "coding"], ["bbbbbbbbbb", "api"], ["cccccccccc", "agent"]]) {
          // a key on the provider's own plan shows its chip, as Any protocol does
          await row(id).hover();
          const c = row(id).locator("button.proto").filter({ hasText: names[plan] });
          assert.equal(await c.count(), 1, `${id}: no chip saying ${names[plan]}`);
          assert.equal(await c.getAttribute("title"), await say("The plan this key is for: its requests go to that plan's endpoints, and it is asked only for that plan's models."));
          const box = await c.boundingBox();
          assert.ok(box && box.x >= 0 && box.x + box.width <= width, `the chip runs off the window: ${JSON.stringify(box)}`);
        }
        // the coding key moved to the Agent Plan
        const c = row("aaaaaaaaaa").locator("button.proto").filter({ hasText: names.coding });
        await c.scrollIntoViewIfNeeded();
        await row("aaaaaaaaaa").hover();
        const sc = await scrolled(page);
        await c.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), await say("Plan this key is for"));
        assert.equal(await menu.locator(".pm-item").count(), 3);
        assert.equal(await scrolled(page), sc, "a click scrolled");
        const mb = await menu.boundingBox();
        assert.ok(mb.x >= 0 && mb.x + mb.width <= width, `the menu runs off the window: ${JSON.stringify(mb)}`);
        await menu.locator(".pm-item", { hasText: names.agent }).click();
        await page.waitForFunction(() => !document.querySelector(".proto-menu"));
        await row("aaaaaaaaaa").locator("button.proto").filter({ hasText: names.agent }).waitFor();
        assert.deepEqual(st.posts, [{ id: "volc", ref: "aaaaaaaaaa", plan: "agent" }]);
        // the agent key back to the provider's own plan: saved as none
        await row("cccccccccc").locator("button.proto").filter({ hasText: names.agent }).click();
        await page.locator(".proto-menu .pm-item", { hasText: names.coding }).click();
        await row("cccccccccc").locator("button.proto:not(.set)").filter({ hasText: names.coding }).waitFor({ state: "attached" });
        assert.deepEqual(st.posts[1], { id: "volc", ref: "cccccccccc", plan: "" });
        assert.equal(await sideways(page), false, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: no chip for one key on the provider's plan, nor a preset of one plan`, async (t) => {
    const { page, errors } = await open(t, engine, "en", 420, [{ id: "aaaaaaaaaa", name: "coding", masked: "sk-…one", on: true, active: true }]);
    await page.locator(".row.provider", { hasText: "Volc" }).first().click();
    await page.locator(`.editor .accts.keys .acc[data-account-id="aaaaaaaaaa"]`).waitFor();
    assert.equal(await page.locator(".editor .accts.keys button.proto", { hasText: "Coding Plan" }).count(), 0);
    await page.goto("http://magpie.test/?view=providers");
    await page.locator(".row.provider", { hasText: "Relay" }).first().click();
    await page.locator(`.editor .accts.keys .acc[data-account-id="rrrrrrrrr1"]`).waitFor();
    assert.equal(await page.locator(".editor .accts.keys button.proto", { hasText: "Pay as you go" }).count(), 0);
    assert.deepEqual(errors, []);
  });
}
