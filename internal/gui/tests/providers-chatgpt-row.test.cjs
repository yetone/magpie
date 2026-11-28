// Run with Node's test runner and Playwright on the module path; see README.md.
// tmr11235 on X: the subscription added from the Add sheet's ChatGPT tile
// was listed under Providers as "Codex", the agent's name, so the row
// didn't read as the ChatGPT plan just added. magpie names the row
// ChatGPT now (provider.codexAccount; TestProvidersListTheChatGPTPlanAsChatGPT
// holds /api/providers to it). Here the page: the row, its pill and its
// editor say ChatGPT, the Add sheet's ChatGPT tile is the one marked added,
// and the Sign in with ChatGPT row stays ChatGPT API, its own name, logo
// and tile. Both engines, every language, the window's narrowest too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const sub = (id, name, icon, agentName, plan, logins) => ({
  id, name, icon, chat: "", responses: "https://chatgpt.example/v1", anthropic: "", catalog: "", routing: "smart",
  models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: { set: false, masked: "" },
  account: { agent: id, agentName, user: logins[0], plan, logins: logins.map((user, i) => ({ user, plan, active: i === 0, on: true })) },
});
const providers = [
  sub("codex", "ChatGPT", "codex-color", "Codex", "business", ["me@example.com", "work@example.com"]),
  sub("chatgpt-api", "ChatGPT API", "openai", "ChatGPT API", "plus", ["me@example.com"]),
];

const serve = (lang, web) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
  if (url.pathname === "/api/providers") return json({ providers, presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the ChatGPT plan's row is named ChatGPT, apart from ChatGPT API`, async (t) => {
    const browser = await (engine === "webkit" ? webkit : chromium).launch();
    t.after(() => browser.close());
    for (const [web, width] of [[false, 980], [false, 420], [true, 390]]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const at = `${lang} ${web ? "web " : ""}${width}px`;
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      const errors = [], dialogs = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
      await page.route("**/*", serve(lang, web));
      await page.goto("http://magpie.test/?view=providers");
      const plan = page.locator('#providers .row.provider[data-id="codex"]'), api = page.locator('#providers .row.provider[data-id="chatgpt-api"]');
      await plan.waitFor();
      const nameOf = (row) => row.locator(".who .name").evaluate((n) => n.firstChild.textContent);
      assert.equal(await nameOf(plan), "ChatGPT", `${at}: the plan's row`);
      assert.equal(await nameOf(api), "ChatGPT API", `${at}: Sign in with ChatGPT's row`);
      assert.doesNotMatch(await plan.innerText(), /Codex/, `${at}: the plan's row names no Codex`);
      // the row says ChatGPT once: its pill is the plan alone
      assert.equal((await plan.locator(".key").innerText()).trim(), "Business", `${at}: the plan's pill`);
      // the two rows don't look alike: each its own logo
      const logo = (row) => row.locator(".pi img, .pi svg, img, svg").first().evaluate((e) => e.outerHTML.slice(0, 200));
      assert.notEqual(await logo(plan), await logo(api), `${at}: the two rows have one logo`);
      const y = await page.evaluate(() => scrollY);

      // its editor is titled ChatGPT
      await plan.click();
      await page.waitForFunction(() => !document.querySelector("#modal").hidden);
      const title = await page.locator("#modal").evaluate((m) => m.innerText.split("\n").map((s) => s.trim()).find(Boolean));
      assert.equal(title, "ChatGPT", `${at}: the editor's title`);
      await page.keyboard.press("Escape");
      await page.locator("#modal").waitFor({ state: "hidden" });

      // the Add sheet's ChatGPT tile is the one added, with its two accounts
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");
      await sheet.locator(".tile").first().waitFor();
      const tile = (name) => sheet.locator(".tile").filter({ has: page.locator(".n", { hasText: new RegExp("^" + name + "$") }) });
      assert.equal(await tile("ChatGPT").count(), 1, `${at}: one ChatGPT tile`);
      assert.equal(await tile("ChatGPT").locator(".have").count(), 1, `${at}: the ChatGPT tile is the one added`);
      assert.equal(await tile("ChatGPT").locator(".cnt").textContent(), "2", `${at}: the ChatGPT tile counts the row's accounts`);
      assert.equal(await tile("Codex").count(), 0, `${at}: no Codex tile`);
      await page.keyboard.press("Escape");
      assert.equal(await page.evaluate(() => scrollY), y, `${at}: a click moved the page`);
      assert.deepEqual(dialogs, [], at);
      assert.deepEqual(errors, [], at);
      await context.close();
    }
  });
}
