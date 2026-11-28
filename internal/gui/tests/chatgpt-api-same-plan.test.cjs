// Run with Node's test runner and Playwright on the module path; see README.md.
// tmr11235 on X: an account signed in both as ChatGPT (Codex) and as
// ChatGPT API showed its five hours and week on the ChatGPT row only. The
// ChatGPT API row now shows the same meters, from the Codex account's
// reading (samePlanReadings, `samePlan` names it), a tooltip that says
// where the reading is from, and its refresh reads the Codex account's card
// again. Then (tmr11235 on X: "补一下套餐重置卡什么的") the rest of the
// plan as the Codex row shows it: the plan the reading names, its resets,
// used from here as on the Codex row, with that Codex account and nothing
// real spent, its credits and its paid-until. An account with no Codex twin
// still says where its usage is told, with none of them. Both engines,
// every language, the window's narrowest (560px, windowMinW) and a
// desk's, and a browser at 420px and a phone's.
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

const paid = new Date(Date.now() + 20 * 86400e3).toISOString();
const usage = (spent) => {
  const soon = new Date(Date.now() + 3 * 3600e3).toISOString(), week = new Date(Date.now() + 5 * 86400e3).toISOString();
  return {
    [twin]: { provider: "chatgpt-api", plan: "pro", samePlan: codexUser, readAt: new Date().toISOString(), until: paid, balance: "12 credits",
      resets: { count: spent ? 1 : 2, until: week },
      windows: [{ name: "5 hours", used: spent ? 0 : 30, resetsAt: soon }, { name: "Weekly", used: spent ? 0 : 40, resetsAt: week }] },
    [solo]: { provider: "chatgpt-api", plan: "plus", windows: [], error: siwc, seeAt: "https://chatgpt.com/settings/usage" },
  };
};

const serve = (lang, web, refreshed, posts) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
  if (url.pathname === "/api/providers") return json({ providers: [gpt()], presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/login/usage") return json(url.searchParams.get("agent") === "chatgpt-api" ? usage(posts.length) : {});
  if (url.pathname === "/api/usage/codex-reset") {
    posts.push(route.request().postDataJSON());
    return json({ code: "reset", windows: 2 });
  }
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
    for (const [web, width] of [[false, 560], [false, 1000], [true, 420], [true, 390]]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      const errors = [], refreshed = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, web, refreshed, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "ChatGPT API" }).first().click();
      const row = page.locator(`.editor .accts .acc[data-account-id="${twin}"]`);
      await row.locator(".aq .aq-w").first().waitFor();
      const meters = row.locator(".aq .aq-w:not(.aq-resets):not(.aq-balance)");
      assert.equal(await meters.count(), 2, `${at}: the twin's row has its five hours and week`);
      assert.equal(await row.locator(".aq-none").count(), 0, `${at}: no "no usage" line on the twin's row`);
      // the plan as the Codex row shows it: the plan its reading names, its
      // credits and paid-until
      const w = await page.evaluate(({ paid, lang }) => ({
        plan: accountPlan({ agent: "codex", plan: "pro" }), two: "↺ " + t("{n} resets", { n: 2 }), one: "↺ " + t("1 reset"),
        until: planSpan({ until: paid }).textContent, ask: t("Use a Codex reset?"), use: t("Use a reset"), balance: t("Balance"),
        own: lang === "en" || ["{n} resets", "1 reset", "Use a Codex reset?", "Use a reset", "Balance"].every((k) => I18N[lang]?.[k]),
      }), { paid, lang });
      assert.ok(w.own, `${at}: ${lang} has words for the resets`);
      assert.equal((await row.locator(".plan").first().textContent()).trim(), w.plan, `${at}: the plan chip is the reading's`);
      assert.equal((await row.locator(".aq-until").textContent()).trim(), w.until, `${at}: the plan's paid-until`);
      assert.ok((await row.locator(".aq-balance").textContent()).includes("12 credits"), `${at}: the plan's credits`);
      // its resets, used from here with the Codex account: Cancel spends nothing
      const chip = row.locator(".aq-resets");
      assert.equal((await chip.textContent()).trim(), w.two, `${at}: the plan's resets`);
      assert.equal(await chip.getAttribute("role"), "button", `${at}: the resets are words only, nothing uses one`);
      await chip.click();
      const ask = page.locator(".reset-ask");
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.ask, at);
      assert.ok((await ask.textContent()).includes(codexUser), `${at}: the confirmation names the Codex account that spends it`);
      await ask.locator(".bar button:not(.primary)").click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, [], `${at}: Cancel spent ${JSON.stringify(posts)}`);
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
      assert.equal(await lone.locator(".aq-resets, .aq-until").count(), 0, `${at}: the lone account has no resets or paid-until`);

      // the twin's refresh reads the Codex account's card again
      await row.locator(".quota-refresh").click();
      await page.waitForFunction(() => !document.querySelector(".quota-refresh.busy"));
      assert.deepEqual(refreshed, [["codex", codexUser]], `${at}: refreshed ${JSON.stringify(refreshed)}`);
      await lone.locator(".quota-refresh").click();
      await page.waitForFunction(() => !document.querySelector(".quota-refresh.busy"));
      assert.deepEqual(refreshed[1], ["chatgpt-api", solo], `${at}: the lone account's refresh is its own`);

      // confirmed: that Codex account's reset, once, and the row reads again
      await chip.click();
      await ask.waitFor();
      await ask.locator("button.primary", { hasText: w.use }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(posts, [{ user: codexUser }], `${at}: asked ${JSON.stringify(posts)}`);
      await page.waitForFunction((one) => document.querySelector(`.editor .accts .acc[data-account-id="dee@example.com"] .aq-resets`)?.textContent.trim() === one, w.one);
      assert.deepEqual(errors, [], at);
      await context.close();
    }
  });
}
