// Run with Node's test runner and Playwright on the module path; see README.md.
// CoderMaurice on X: a Claude account upgraded to Max read "Claude Pro" on
// its row in the provider editor, with its 5-hour and weekly meters only,
// while the tray's card for the same account drew a third ring, the
// weekly Fable window. The row names the plan its reading says, as the
// tray does, and draws the windows the tray draws; a Codex account
// upgraded from Plus says Pro from its reading too, the sign-in's plan
// standing in only until one is read. In every language, Chromium and
// WebKit, wide and narrow, the meters stay inside the row.
// No backend; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const soon = (h) => new Date(Date.now() + h * 3600e3).toISOString();
const claude = () => ({
  id: "claude", name: "Claude", icon: "claudecode-color", models: [], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "claude", agentName: "Claude Code", user: "maurice@example.com", plan: "pro",
    logins: [
      { user: "maurice@example.com", plan: "pro", active: true, on: true },
      { user: "other@example.com", plan: "pro", on: true },
    ],
  },
});
const codex = () => ({
  id: "codex", name: "Codex", icon: "codex-color", models: [], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "plus@example.com", plan: "plus",
    logins: [
      { user: "plus@example.com", plan: "plus", active: true, on: true },
      { user: "unread@example.com", plan: "plus", on: true },
    ],
  },
});
const usage = {
  claude: {
    "maurice@example.com": { plan: "max", windows: [
      { name: "5 hours", used: 32, resetsAt: soon(3) },
      { name: "7 days", used: 40, resetsAt: soon(90) },
      { name: "7 days · Fable", model: "fable", used: 40, resetsAt: soon(90) },
    ] },
    "other@example.com": { plan: "pro", windows: [{ name: "5 hours", used: 5, resetsAt: soon(2) }, { name: "7 days", used: 9, resetsAt: soon(50) }] },
  },
  codex: {
    "plus@example.com": { plan: "pro", windows: [{ name: "5 hours", used: 10, resetsAt: soon(2) }, { name: "7 days", used: 20, resetsAt: soon(50) }] },
    // a read that failed names no plan: the sign-in's stands
    "unread@example.com": { plan: "", error: "Codex didn't answer", windows: [] },
  },
};
const serve = (lang, web) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"dark",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
  if (url.pathname === "/api/providers") return json({ providers: [claude(), codex()], presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/login/usage") return json(usage[url.searchParams.get("agent")] || {});
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};

const langs = ["en", "zh", "zh-TW", "ja", "de"];
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
for (const engine of engines) {
  test(engine + ": an upgraded account's row says its plan now and draws its Fable window", async (t) => {
    const browser = await (engine === "webkit" ? webkit : chromium).launch();
    t.after(() => browser.close());
    for (const [web, width] of [[false, 1000], [false, 560], [true, 390]]) for (const lang of langs) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, web));
      await page.goto("http://magpie.test/?view=providers");
      const row = (who) => page.locator(`.editor .accts .acc[data-account-id="${who}"]`);

      await page.locator(".row.provider", { hasText: "Claude" }).first().click();
      const me = row("maurice@example.com");
      await me.locator(".aq .aq-w").first().waitFor();
      assert.equal(await me.locator(".acc-line > .plan").textContent(), "Claude Max", `${at}: the plan its reading says`);
      assert.equal(await row("other@example.com").locator(".acc-line > .plan").textContent(), "Claude Pro", `${at}: a Pro account still says Pro`);
      const meters = me.locator(".aq .aq-w");
      assert.equal(await meters.count(), 3, `${at}: the 5-hour, weekly and Fable windows, as the tray draws them`);
      const fable = await page.evaluate(() => t("7 days · Fable"));
      assert.equal(await meters.nth(2).locator(".aq-n").textContent(), fable, `${at}: the third is Fable's`);
      const m = await me.evaluate((r) => {
        const b = r.getBoundingClientRect();
        return [...r.querySelectorAll(".aq .aq-w")].map((w) => { const x = w.getBoundingClientRect(); return x.width > 0 && x.left >= b.left - 1 && x.right <= b.right + 1 && x.bottom <= b.bottom + 1; });
      });
      assert.deepEqual(m, [true, true, true], `${at}: every meter inside its row`);
      const wide = await page.evaluate(() => { const b = document.querySelector(".modal .ebody"); return b.scrollWidth - b.clientWidth; });
      assert.ok(wide <= 0, `${at}: the sheet doesn't scroll sideways (${wide}px)`);

      await page.keyboard.press("Escape");
      await page.locator("#modal.out, #modal:not(.open)").first().waitFor({ state: "attached" }).catch(() => {});
      await page.waitForFunction(() => !document.querySelector("#modal:not(.out) .editor"));
      await page.locator(".row.provider", { hasText: "Codex" }).first().click();
      await row("plus@example.com").locator(".aq .aq-w").first().waitFor();
      assert.equal(await row("plus@example.com").locator(".acc-line > .plan").textContent(), "ChatGPT Pro", `${at}: a Codex account upgraded from Plus says Pro`);
      assert.equal(await row("unread@example.com").locator(".acc-line > .plan").textContent(), "ChatGPT Plus", `${at}: with no reading, the sign-in's plan`);
      assert.deepEqual(errors, [], `${at}: no page errors`);
      await context.close();
    }
  });
}
