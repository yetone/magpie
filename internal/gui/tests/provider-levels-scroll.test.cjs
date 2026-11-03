// A reasoning level ticked leaves the provider editor's scrollable model
// list where it is: it is staged for the editor's Save, with nothing sent
// and nothing redrawn (provider-levels-save.test.cjs).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const levels = ["none", "low", "medium", "high"];
const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };

function serve(posts) {
  const models = Array.from({ length: 10 }, (_, i) => ({ id: `model-${i + 1}`, name: `Model ${i + 1}`, on: true, efforts: levels }));
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/efforts") {
      const body = route.request().postDataJSON();
      posts.push(body);
      const model = models.find((m) => m.id === body.model);
      model.kept = body.efforts.length ? body.efforts : undefined;
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: changing a model's reasoning level keeps the model list in place`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [], posts = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve(posts));
    await page.goto("http://magpie.test/?view=providers");
    await page.locator(".row.provider").click();
    await page.getByRole("button", { name: "Names, prices & levels" }).click();
    const list = page.locator(".mnames:not([hidden])");
    assert(await list.evaluate((e) => e.scrollHeight > e.clientHeight), "fixture must overflow");
    // the list scrolled by hand so a row's levels are in sight, mid-list: a
    // row has its price and more under its name now, so a fixed offset
    // left model-5 out of sight and Playwright's own scroll moved the list
    const into = (id) => list.evaluate((e, id) => {
      const box = [...e.querySelectorAll(".mname")].find((r) => r.querySelector("code")?.textContent === id).querySelector("input[type=checkbox]");
      e.scrollTop += box.getBoundingClientRect().top - e.getBoundingClientRect().top - e.clientHeight / 2;
      const r = box.getBoundingClientRect(), l = e.getBoundingClientRect();
      if (r.top < l.top || r.bottom > l.bottom) throw new Error(`${id}'s levels can't be scrolled into sight`);
      return e.scrollTop;
    }, id);
    let before = await into("model-5");
    assert(before > 0);
    const fifth = page.locator(".mname", { has: page.locator("code", { hasText: "model-5" }) });
    const old = await list.elementHandle();
    await fifth.getByRole("checkbox", { name: "low" }).uncheck();
    assert(await old.evaluate((e) => e.isConnected));
    assert.equal(await fifth.getByRole("checkbox", { name: "low" }).isChecked(), false);
    assert.equal(await list.evaluate((e) => e.scrollTop), before);
    const next = page.locator(".mname", { has: page.locator("code", { hasText: "model-6" }) });
    before = await into("model-6");
    await next.getByRole("checkbox", { name: "high" }).uncheck();
    assert.equal(await next.getByRole("checkbox", { name: "high" }).isChecked(), false);
    assert.deepEqual(posts, []);
    assert.equal(await list.evaluate((e) => e.scrollTop), before);
    assert.deepEqual(errors, []);
  });
}
