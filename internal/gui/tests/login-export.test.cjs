// Run with Node's test runner and Playwright on the module path; see README.md.
// ChatGPT and Claude accounts go out as a file Sub2API, CLIProxyAPI or
// magpie takes (heyiwei5 and Rk1ngggg on X): the accounts list offers it,
// the sheet warns that the file holds live sign-ins, lists each account to
// tick, posts the chosen ones and format, and says where the file went; in
// English and Chinese, in both engines, at 380px too. Its row comes last of
// the account list's rows and has no plus, since it adds nothing (the
// owner, 11-12), in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const logins = [
  { user: "live@example.com", plan: "plus", active: true, on: true },
  { user: "two@example.com", plan: "pro", on: true },
];
const account = {
  id: "codex", name: "ChatGPT", icon: "openai", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "live@example.com", plan: "plus", logins },
};

function serve(lang, posted, acct = account) {
  const providers = { providers: [acct], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/signin/export") {
      posted.push(route.request().postDataJSON());
      return json({ path: "~/Downloads/magpie-sub2api-codex.json", accounts: ["live@example.com"], skipped: [{ user: "two@example.com", error: "no refresh token" }] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of [["en", 900], ["zh", 900], ["en", 380]]) {
    test(`${engine} ${lang} ${width}: accounts are exported as a file`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      await page.route("**/*", serve(lang, posted));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-login-export.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "ChatGPT" }).click();
      const T = async (s, v) => page.evaluate(([s, v]) => t(s, v), [s, v || {}]);
      const row = page.locator(".editor .accts .acc.add", { hasText: await T("Export accounts to a file…") });
      await row.waitFor();
      if (lang === "zh") {
        for (const s of ["Export accounts to a file…", "Export {name} accounts", "Saved to {path}", "Export",
          "The file holds each account's live sign-in: whoever has it is signed in as that account. Keep it private and delete it once it is imported.",
          "A ChatGPT sign-in can be renewed in one place only: the first tool to renew it signs the other out."]) {
          assert.notEqual(await T(s), s, "no Chinese for: " + s);
        }
      }
      await row.click();
      const box = page.locator(".signing.export");
      await box.waitFor();
      assert.match(await box.textContent(), lang === "en" ? /whoever has it is signed in as that account/ : /拿到文件的人/);
      // each account to tick, the first format picked
      assert.equal(await box.locator("input[type=checkbox]").count(), 2);
      await box.locator("input[type=checkbox]").nth(1).uncheck();
      await box.locator(".segs .opt", { hasText: "CLIProxyAPI" }).click();
      await box.locator("button.primary").click();
      await box.locator(".res.added").waitFor();
      assert.deepEqual(posted, [{ agent: "codex", format: "cliproxyapi", users: ["live@example.com"] }]);
      assert.match(await box.textContent(), /magpie-sub2api-codex\.json/);
      assert.match(await box.textContent(), /two@example\.com/);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "page scrolls sideways");
      await box.locator("button.primary").click();
      await box.waitFor({ state: "detached" });
      assert.deepEqual(errors, []);
    });
  }
}

const claude = {
  ...account, id: "claude", name: "Claude", icon: "claude-color", models: [{ id: "claude-opus-5-5", name: "Claude Opus 5.5", on: true }],
  account: { agent: "claude", agentName: "Claude Code", user: "live@example.com", plan: "max", logins },
};
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Export is the last of the account rows, without a plus`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 900, height: 760 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, [], claude));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Claude" }).first().click();
      const T = async (s, v) => page.evaluate(([s, v]) => t(s, v), [s, v || {}]);
      const rows = page.locator(".editor .accts .acc.add");
      await rows.filter({ hasText: await T("Export accounts to a file…") }).waitFor();
      const said = (await rows.allInnerTexts()).map((s) => s.trim());
      assert.equal(said.length, 4, JSON.stringify(said));
      assert.equal(said[2], await T("Add an account by setup-token…"));
      assert.equal(said[3], await T("Export accounts to a file…"));
      const glyphs = await rows.evaluateAll((rs) => rs.map((r) => r.querySelector(".dot svg path")?.getAttribute("d")));
      assert.equal(new Set(glyphs.slice(0, 3)).size, 1, "the adding rows share the plus");
      assert.notEqual(glyphs[3], glyphs[0], "Export has the plus");
      assert.deepEqual(errors, []);
    });
  }
}
