// Run with Node's test runner and Playwright on the module path; see README.md.
// Player on Discord: checking the library's skills for updates ran into
// GitHub's rate limit. Settings → Network and sharing has a GitHub token
// row: a token typed is saved on its own and shown masked, with Remove; one
// GITHUB_TOKEN / GH_TOKEN gives is said; one refused is said in the row,
// what was typed kept; nothing scrolls. And a check GitHub limited says so
// in the page's words: until when, and that a token in Settings raises it.
// In English and Chinese, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const TOKEN = "ghp_abcdefghijklmnopqrstuvwxyz0123";
const words = {
  en: { head: "GitHub", name: "GitHub token", save: "Save", remove: "Remove", env: "Using GH_TOKEN from the environment",
    refused: "a GitHub token is one word, without spaces", limited: "GitHub allows 60 requests an hour without a token, used up until {time}. Add a GitHub token in Settings → Network and sharing to raise it to 5,000.",
    withToken: "GitHub's rate limit for your GitHub token is used up until {time}", check: "Check for updates" },
  zh: { head: "GitHub", name: "GitHub 令牌", save: "保存", remove: "移除", env: "正在使用环境变量 GH_TOKEN",
    refused: "GitHub 令牌是一串不含空格的字符", limited: "GitHub 对不带令牌的请求每小时只允许 60 次，已用完，{time} 恢复。在 设置 → 网络与共享 中添加 GitHub 令牌，可提高到每小时 5000 次。",
    withToken: "你的 GitHub 令牌的请求额度已用完，{time} 恢复", check: "检查更新" },
};
const UNTIL = "2030-01-02T03:04:00Z";

function serve(lang, posted, st) {
  const settings = () => ({ lang, theme: "light", searchVendors: [], searchAPIs: [], githubTokenMask: st.mask, githubTokenFrom: st.from });
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [{ id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: `${HOME}/.codex/mcp.json` }],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], servers: [],
    skills: [{ name: "pdf", description: "PDFs", kind: "github", agents: ["codex"], source: "https://github.com/owner/repo/tree/main/pdf" }],
  };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/github-token") {
      const b = JSON.parse(r.request().postData());
      posted.push(b);
      if (/\s/.test(b.token)) return json({ error: words.en.refused }, 400);
      if (b.token) Object.assign(st, { mask: b.token.slice(0, 4) + "…" + b.token.slice(-4), from: "settings" });
      else Object.assign(st, st.env ? { mask: "gho_…nt99", from: "GH_TOKEN" } : { mask: "", from: "" });
      return json(settings());
    }
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/library/skills/check") {
      const token = st.from !== "";
      lib.skills[0].check = { name: "pdf", status: "unknown", error: "GitHub's rate limit (English, from the server)", limited: { until: UNTIL, ...(token ? { token } : {}) } };
      return json(lib);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a GitHub token is set, shown masked and removed in Settings`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [], st = { mask: "", from: "", env: false };
      await page.route("**/*", serve(lang, posted, st));
      await page.goto("http://magpie.test/?view=settings&tab=network");
      const list = page.locator("#githubList");
      const row = list.locator(".row.github-token");
      await row.waitFor();
      assert.equal(await list.locator("xpath=preceding-sibling::div[1]").textContent(), w.head);
      assert.equal(await row.locator(".name").innerText(), w.name);
      const field = row.locator("input.github-token-input");
      assert.equal(await field.getAttribute("type"), "password", "the token is typed masked");
      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const click = async (loc) => {
        const b = await loc.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      const before = await where();

      // nothing typed: nothing sent
      await click(row.locator("button.text", { hasText: w.save }));
      assert.equal(posted.length, 0);
      // refused: said in the row, what was typed kept
      await field.fill("two words");
      await click(row.locator("button.text", { hasText: w.save }));
      await page.waitForFunction(() => document.querySelector("#githubList .sub.err"));
      assert.equal(await row.locator(".sub").innerText(), w.refused);
      assert.equal(await field.inputValue(), "two words");
      // saved: masked, with Remove, the token itself nowhere on the page
      await field.fill(TOKEN);
      await click(row.locator("button.text", { hasText: w.save }));
      await page.waitForFunction(() => document.querySelector("#githubList code"));
      assert.deepEqual(posted.at(-1), { token: TOKEN });
      assert.equal(await row.locator("code").innerText(), "ghp_…0123");
      assert(!(await page.content()).includes(TOKEN), "the token is shown");
      // removed, with GH_TOKEN set: the environment's is said
      st.env = true;
      await click(row.locator("button.text", { hasText: w.remove }));
      await page.waitForFunction(() => document.querySelector("#githubList input"));
      assert.deepEqual(posted.at(-1), { token: "" });
      assert((await row.locator(".sub").innerText()).includes(w.env));
      assert((await row.locator(".sub").innerText()).includes("gho_…nt99"));
      assert.deepEqual(await where(), before, "the clicks moved nothing");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a check GitHub limited says until when and that a token raises it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const ctx = await browser.newContext({ viewport: { width: 1000, height: 900 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      for (const st of [{ mask: "", from: "" }, { mask: "ghp_…0123", from: "settings" }]) {
        await page.unroute("**/*");
        await page.route("**/*", serve(lang, [], st));
        await page.goto("http://magpie.test/?view=library");
        const check = page.locator("#view-library button.lib-updall", { hasText: w.check });
        await check.waitFor();
        await check.click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-unchecked"));
        const time = await page.evaluate((u) => new Date(u).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }), UNTIL);
        const want = (st.from ? w.withToken : w.limited).replace("{time}", time);
        assert((await page.locator("#status").textContent()).includes(want), `status: ${await page.locator("#status").textContent()}`);
        assert.equal(await page.locator("#view-library .lib-unchecked").getAttribute("title"), want);
      }
      assert.deepEqual(errors, []);
    });
  }
}
