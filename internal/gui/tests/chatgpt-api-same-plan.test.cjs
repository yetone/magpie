// Run with Node's test runner and Playwright on the module path; see README.md.
// tmr11235 on X: an account signed in both as ChatGPT (Codex) and as
// ChatGPT API showed its five hours and week on the ChatGPT row only. The
// ChatGPT API row now shows the same meters, from the Codex account's
// reading (samePlanReadings, `samePlan` names it): no Codex resets on it,
// a tooltip that says where the reading is from, and its refresh reads the
// Codex account's card again. An account with no Codex twin still says
// where its usage is told. Both engines, every language, the window's
// narrowest and a phone's browser.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const twin = "dee@example.com", solo = "solo@example.com", codexUser = "Dee@Example.com · Plus";
const siwc = "a ChatGPT API account's usage isn't told through OpenAI's API, only at https://chatgpt.com/settings/usage; magpie shows it here while Codex is signed in to the same ChatGPT account";
const siwcKey = "OpenAI's API doesn't tell a ChatGPT API account's usage — see chatgpt.com/settings/usage. It shows here while Codex is signed in to the same ChatGPT account";
const tipKey = "The ChatGPT plan's windows, as Codex's sign-in to the same account reads them";

const gpt = () => ({
  id: "chatgpt-api", name: "ChatGPT API", icon: "openai", chat: "", responses: "https://api.openai.com/v1", anthropic: "", catalog: "", routing: "",
  models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: { set: false, masked: "" },
  account: {
    agent: "chatgpt-api", agentName: "ChatGPT API", user: twin, plan: "plus",
    logins: [{ user: twin, plan: "plus", active: true, on: true }, { user: solo, plan: "plus", on: true }],
  },
});

const usage = () => {
  const soon = new Date(Date.now() + 3 * 3600e3).toISOString(), week = new Date(Date.now() + 5 * 86400e3).toISOString();
  return {
    [twin]: { provider: "chatgpt-api", plan: "plus", samePlan: codexUser, readAt: new Date().toISOString(),
      windows: [{ name: "5 hours", used: 30, resetsAt: soon }, { name: "Weekly", used: 40, resetsAt: week }] },
    [solo]: { provider: "chatgpt-api", plan: "plus", windows: [], error: siwc, seeAt: "https://chatgpt.com/settings/usage" },
  };
};

const serve = (lang, web, refreshed) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
  if (url.pathname === "/api/providers") return json({ providers: [gpt()], presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/login/usage") return json(url.searchParams.get("agent") === "chatgpt-api" ? usage() : {});
  if (url.pathname === "/api/usage/quotas/refresh") {
    refreshed.push([url.searchParams.get("provider"), url.searchParams.get("user")]);
    return json([]);
  }
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};

for (const [engine, browserType] of [["chromium", chromium], ["webkit", webkit]]) {
  test(engine + ": a ChatGPT API account shows the same account's ChatGPT windows", async (t) => {
    const browser = await browserType.launch();
    t.after(() => browser.close());
    for (const [web, width] of [[false, 560], [true, 390]]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      const errors = [], refreshed = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, web, refreshed));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "ChatGPT API" }).first().click();
      const row = page.locator(`.editor .accts .acc[data-account-id="${twin}"]`);
      await row.locator(".aq .aq-w").first().waitFor();
      const meters = row.locator(".aq .aq-w");
      assert.equal(await meters.count(), 2, `${at}: the twin's row has its five hours and week`);
      assert.equal(await row.locator(".aq-none").count(), 0, `${at}: no "no usage" line on the twin's row`);
      assert.equal(await row.locator(".aq-resets").count(), 0, `${at}: Codex's resets stay on the Codex row`);
      const [tip, sameTip] = await page.evaluate((k) => [t(k), k], tipKey);
      if (lang !== "en") assert.notEqual(tip, sameTip, `${at}: ${lang} has its own words for the tooltip`);
      assert.ok((await row.locator(".aq").getAttribute("title")).includes(tip), `${at}: the line says where the reading is from`);
      // the meters fit the row
      const over = await row.evaluate((r) => { const b = r.getBoundingClientRect(), q = r.querySelector(".aq").getBoundingClientRect(); return Math.round(q.right - b.right); });
      assert.ok(over <= 0, `${at}: the twin's line runs ${over}px past its row`);

      // no Codex twin: still says where its usage is told
      const lone = page.locator(`.editor .accts .acc[data-account-id="${solo}"]`);
      const said = await lone.locator(".aq-none").textContent();
      assert.ok(said.endsWith(await page.evaluate((k) => t(k), siwcKey)), `${at}: the lone account says ${JSON.stringify(said)}`);
      assert.equal(await lone.locator(".aq .aq-w").count(), 0, `${at}: the lone account has no meters`);

      // the twin's refresh reads the Codex account's card again
      await row.locator(".quota-refresh").click();
      await page.waitForFunction(() => !document.querySelector(".quota-refresh.busy"));
      assert.deepEqual(refreshed, [["codex", codexUser]], `${at}: refreshed ${JSON.stringify(refreshed)}`);
      await lone.locator(".quota-refresh").click();
      await page.waitForFunction(() => !document.querySelector(".quota-refresh.busy"));
      assert.deepEqual(refreshed[1], ["chatgpt-api", solo], `${at}: the lone account's refresh is its own`);
      assert.deepEqual(errors, [], at);
      await context.close();
    }
  });
}
