// Run with Node's test runner and Playwright on the module path; see README.md.
// ChatGPT accounts come in from CLIProxyAPI's auth files or Codex's own
// auth.json (Mrhe525 on #124: 要是能把那什么cpa的json文件可以导入直接使用就无敌了):
// the accounts list and the wait on a browser sign-in both offer it, the box
// says the file's sign-in is spent by the refresh, what is pasted is posted
// as it is, and each account's outcome is listed; in English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const cpa = '{"type":"codex","refresh_token":"r-cpa","email":"cpa@example.com"}';
const account = (logins) => ({
  id: "codex", name: "ChatGPT", icon: "openai", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "live@example.com", plan: "plus", logins },
});
const before = [{ user: "live@example.com", plan: "plus", active: true, on: true }];
const after = [...before, { user: "cpa@example.com", plan: "pro", on: true }];

function serve(lang, posted) {
  let providers = { providers: [account(before)], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/signin") return json({ id: "s1", agent: "codex", state: "waiting", url: "https://auth.openai.com/oauth/authorize?x=1" });
    if (url.pathname === "/api/signin/s1") return json({ id: "s1", agent: "codex", state: "waiting", url: "https://auth.openai.com/oauth/authorize?x=1" });
    if (url.pathname === "/api/signin/import") {
      posted.push(route.request().postDataJSON());
      await new Promise((r) => setTimeout(r, 300));
      providers = { ...providers, providers: [account(after)] };
      return json({ providers, results: [
        { user: "cpa@example.com", status: "added", plan: "pro" },
        { user: "live@example.com", status: "exists" },
        { user: "used@example.com", status: "failed", error: "ChatGPT didn't take the refresh token — it has been used since" },
      ] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { imp: "Import accounts from a file…", instead: "Import from a file instead…", title: "Import ChatGPT accounts", intro: /CLIProxyAPI's auth files \(JSON\) or Codex's auth\.json/, spent: /spends the file's sign-in/, checking: "Checking the accounts with ChatGPT…", done: ["Added", "Already in magpie", "Not added"] },
  zh: { imp: null, instead: null, title: null, intro: /CLIProxyAPI/, spent: /./, checking: null, done: null },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: ChatGPT accounts come in from CLIProxyAPI's files`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      await page.route("**/*", serve(lang, posted));
      // the browser sign-in opens ChatGPT's page in a new window, and the real
      // one is Cloudflare's challenge: in Chromium, while it runs, clicks on
      // this page hung past their 5s (#1307). The window gets an empty page
      await context.route((url) => url.hostname !== "magpie.test", (route) => route.fulfill({ contentType: "text/html", body: "" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-login-import.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "ChatGPT" }).click();
      const adds = page.locator(".editor .accts .acc.add");
      await adds.nth(1).waitFor();
      const T = async (s, v) => page.evaluate(([s, v]) => t(s, v), [s, v || {}]);
      if (lang === "zh") {
        // every new string has its Chinese
        for (const s of ["Import from a file instead…", "Checking the accounts with {vendor}…",
          "Bring in accounts from CLIProxyAPI's auth files or {own}",
          "Choose or paste CLIProxyAPI's auth files (JSON) or {own}. Each account's sign-in is refreshed with {vendor} before it is added.",
          "Refreshing it spends the file's sign-in: the tool it came from will need to sign in again to use that account.",
          "Each account's sign-in is refreshed and its account looked up, as signing in does."]) {
          assert.notEqual(await T(s), s, "no Chinese for: " + s);
        }
      }
      const w = want[lang];
      const say = {
        imp: w.imp || await T("Import accounts from a file…"),
        instead: w.instead || await T("Import from a file instead…"),
        title: w.title || await T("Import {name} accounts", { name: "ChatGPT" }),
        checking: w.checking || await T("Checking the accounts with {vendor}…", { vendor: "ChatGPT" }),
      };

      // from a browser sign-in under way, the file is one click away
      await adds.nth(0).click();
      const instead = page.locator(".signing .acts button", { hasText: say.instead });
      await instead.waitFor();
      assert.match(await instead.getAttribute("title"), /CLIProxyAPI/);
      await instead.click();
      const box = page.locator(".signing.import");
      await box.waitFor();
      assert.equal(await box.locator(".tt .n").first().textContent(), say.title);
      const lines = await box.locator(".tt > .s").allTextContents();
      assert.match(lines[0], w.intro);
      assert.match(lines[1], w.spent);
      await box.getByRole("button", { name: lang === "en" ? "Cancel" : await T("Cancel") }).click();

      // and from the accounts list
      await adds.nth(1).click();
      const area = page.locator(".signing.import textarea");
      await area.fill(cpa);
      await page.locator(".signing.import button.primary").click();
      await page.locator(".signing.import .spinner").waitFor();
      assert.equal(await page.locator(".signing.import .tt .n").textContent(), say.checking);
      await page.locator(".signing.import .results").waitFor();
      assert.deepEqual(posted, [{ agent: "codex", files: [cpa] }]);
      const done = await page.locator(".signing.import .res .st").allTextContents();
      assert.deepEqual(done, w.done || [await T("Added"), await T("Already in magpie"), await T("Not added")]);
      assert.match(await page.locator(".signing.import .res.failed .why").textContent(), /used since/);
      // the new account is listed
      await page.locator(".editor .accts .acc .n", { hasText: "cpa@example.com" }).waitFor();
      assert.deepEqual(errors, []);
    });
  }
}
