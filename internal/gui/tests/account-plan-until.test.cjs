// Run with Node's test runner and Playwright on the module path; see README.md.
// danzi002 on Discord: a Codex (ChatGPT) account's row on Providers says
// when its paid plan ends, "Until Nov 19" on the line under its name
// beside its allowance, the whole date and time in its tooltip, from the
// until /api/providers gives the account (its sign-in's
// chatgpt_subscription_active_until). An account with no until says none,
// never a guessed date. In every language, Chromium and WebKit, at the
// window's narrowest and in a phone's browser, the date is shown whole,
// inside the row, and the first line stays one line.
// No backend; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const until = "2099-11-19T16:12:01Z";
const soon = new Date(Date.now() + 3 * 3600e3).toISOString();
const codex = () => ({
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", agentIcon: "codex-color", user: "plus@example.com", plan: "plus",
    logins: [
      { user: "plus@example.com", plan: "plus", active: true, on: true, until },
      { user: "pro@example.com", plan: "pro", on: true },
    ],
  },
});
const serve = (lang, web) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"dark",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
  if (url.pathname === "/api/providers") return json({ providers: [codex()], presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/login/usage") return json({
    "plus@example.com": { windows: [{ name: "5 hours", used: 10, resetsAt: soon }, { name: "7 days", used: 40, resetsAt: soon }] },
    "pro@example.com": { windows: [{ name: "5 hours", used: 0, resetsAt: soon }, { name: "7 days", used: 5, resetsAt: soon }] },
  });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};

const langs = ["en", "zh", "zh-TW", "ja", "de"];
for (const [engine, browserType] of [["chromium", chromium], ["webkit", webkit]]) {
  test(engine + ": a ChatGPT account's row says when its plan ends", async (t) => {
    const browser = await browserType.launch();
    t.after(() => browser.close());
    for (const [web, width] of [[false, 1000], [false, 560], [true, 390]]) for (const lang of langs) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      await page.route("**/*", serve(lang, web));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Codex" }).first().click();
      const rows = page.locator(".editor .accts .acc[data-account-id]");
      await rows.nth(1).waitFor();
      const want = await page.evaluate((u) => ({
        short: t("Until {date}", { date: new Date(u).toLocaleDateString(intlLang(), { month: "short", day: "numeric" }) }),
        long: t("Until {date}", { date: new Date(u).toLocaleString(intlLang()) }),
      }), until);
      if (lang !== "en") assert.notEqual(want.short.replace(/[^A-Za-z ]/g, "").trim(), "Until", `${at}: "Until {date}" is translated`);
      const row = (who) => page.locator(`.editor .accts .acc[data-account-id="${who}"]`);
      const plus = row("plus@example.com").locator(".aq .aq-until");
      await row("plus@example.com").locator(".aq .aq-w").first().waitFor();
      assert.equal(await plus.textContent(), want.short, `${at}: when the plan ends`);
      assert.equal(await plus.getAttribute("title"), want.long, `${at}: the whole date in its tooltip`);
      assert.equal(await row("pro@example.com").locator(".aq-until").count(), 0, `${at}: no until, no date`);
      assert.equal(await row("plus@example.com").locator(".acc-line > .plan").textContent(), "ChatGPT Plus", `${at}: the plan as before`);
      const m = await plus.evaluate((e) => {
        const b = e.getBoundingClientRect(), r = e.closest(".acc").getBoundingClientRect(), line = e.closest(".acc").querySelector(".acc-line").getBoundingClientRect();
        return { cut: e.scrollWidth - e.clientWidth, w: b.width, inside: b.left >= r.left - 1 && b.right <= r.right + 1, line: line.height, vis: getComputedStyle(e).visibility };
      });
      assert.ok(m.w > 0 && m.cut <= 0 && m.vis === "visible", `${at}: the date is shown whole (${m.cut}px cut)`);
      assert.ok(m.inside, `${at}: the date stays inside its row`);
      assert.ok(m.line <= (web ? 48 : 24), `${at}: the first line is one line (${m.line}px)`);
      const wide = await page.evaluate(() => { const b = document.querySelector(".modal .ebody"); return b.scrollWidth - b.clientWidth; });
      assert.ok(wide <= 0, `${at}: the sheet doesn't scroll sideways (${wide}px)`);
      await context.close();
    }
  });
}
