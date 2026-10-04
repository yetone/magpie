// Run with Node's test runner and Playwright on the module path; see README.md.
// Each MCP server's row says whether it works (Discord: lc asked for a
// status per server), as magpie found by connecting to it: a dot and a few
// words beside its name, the whole reason in its tooltip. The servers are
// checked once when the MCP tab opens, not again on coming back to it; a
// click on a row's status checks that one again (fresh), without opening
// the editor or moving the page. In English and Chinese. No backend: the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/lc";
const fixture = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
  servers: [
    { name: "files", transport: "stdio", command: "npx", args: ["files-mcp"], agents: ["claude"] },
    { name: "linear", transport: "http", url: "https://mcp.linear.app/mcp", agents: [], signIn: { signedIn: false } },
    { name: "fetch", transport: "stdio", command: "uvx", args: ["mcp-server-fetch"], agents: [] },
    { name: "github", transport: "stdio", command: "github-mcp", agents: [] },
    { name: "slow", transport: "http", url: "https://slow.example.com/mcp", agents: [], signIn: { signedIn: false } },
  ],
});
const found = {
  files: { state: "ok", tools: 12 },
  linear: { state: "auth", code: 401, oauth: true },
  fetch: { state: "error", why: "notfound", detail: "uvx" },
  github: { state: "error", why: "exited", code: 1, detail: "Error: GITHUB_PERSONAL_ACCESS_TOKEN is not set" },
  slow: { state: "error", why: "timeout" },
};

// checks holds each check asked for; a check's answer waits for release()
function server(lang, checks) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: fixture() });
    if (url.pathname === "/api/library/mcp/check") {
      const body = req.postDataJSON();
      let release;
      const held = new Promise((r) => (release = r));
      checks.push({ ...body, release });
      await held;
      const names = body.names?.length ? body.names : Object.keys(found);
      const servers = Object.fromEntries(names.map((n) => [n, body.fresh && n === "files" ? { state: "ok", tools: 13 } : found[n]]));
      return route.fulfill({ json: { servers } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    checking: "checking…",
    files: ["12 tools", "It started and listed its tools"],
    linear: ["needs sign-in", "The server asks for a sign-in — open it to sign in once in magpie"],
    fetch: ["can't start: uvx not found", "Can't start it: there is no uvx on the PATH magpie has"],
    github: ["exited (1)", "It exited with code 1 before listing its tools\nError: GITHUB_PERSONAL_ACCESS_TOKEN is not set"],
    slow: ["no answer", "No answer in 15 seconds"],
    again: "Click to check again", fresh: "13 tools", skills: "Skills",
  },
  zh: {
    checking: "检查中…",
    files: ["12 个工具", "已启动并列出了它的工具"],
    linear: ["需要登录", "服务器要求登录 — 打开它，在 magpie 里登录一次即可"],
    fetch: ["无法启动：找不到 uvx", "无法启动：magpie 的 PATH 里没有 uvx"],
    github: ["已退出（1）", "列出工具前就退出了，退出码 1\nError: GITHUB_PERSONAL_ACCESS_TOKEN is not set"],
    slow: ["无响应", "15 秒内没有响应"],
    again: "点击重新检查", fresh: "13 个工具", skills: "技能",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an MCP server's row says whether it works", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });

    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = words[lang], checks = [];
        const ctx = await browser.newContext({ viewport: { width: 980, height: 500 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("http://magpie.test/**", server(lang, checks));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        await page.locator("#view-library .lib-row").first().waitFor();
        const status = (name) => page.locator(`#view-library .lib-health[data-server="${name}"]`);

        // every server asked for at once, each showing it's being checked
        for (let i = 0; i < 50 && !checks.length; i++) await page.waitForTimeout(20);
        assert.equal(checks.length, 1);
        assert.deepEqual([...checks[0].names].sort(), ["fetch", "files", "github", "linear", "slow"]);
        assert.equal(checks[0].fresh, false);
        for (const n of Object.keys(found)) {
          assert.equal(await status(n).textContent(), w.checking);
          assert.equal(await status(n).evaluate((b) => b.classList.contains("checking")), true);
        }
        checks[0].release();

        // each status in a few words, the whole reason in its tooltip
        await page.waitForFunction(() => !document.querySelector("#view-library .lib-health.checking"));
        const cls = { files: "ok", linear: "auth", fetch: "err", github: "err", slow: "err" };
        for (const [n, [text, tip]] of Object.entries(w).filter(([k]) => k in found)) {
          assert.equal(await status(n).textContent(), text, n);
          assert.equal(await status(n).getAttribute("title"), tip + "\n" + w.again, n);
          assert.equal(await status(n).evaluate((b, c) => b.classList.contains(c), cls[n]), true, n);
          // a dot, not a coloured stripe
          assert.equal(await status(n).evaluate((b) => getComputedStyle(b).borderLeftWidth), "0px");
          assert.equal(await status(n).evaluate((b) => getComputedStyle(b, "::before").borderRadius), "50%");
        }
        // it stays on the row's name line, one line high
        const box = await status("fetch").boundingBox();
        assert.ok(box.height <= 18, "status wraps: " + box.height);

        // back from another tab: not checked again
        await page.locator(".lib-tabs button", { hasText: w.skills }).click();
        await page.locator(".lib-tabs button", { hasText: /MCP/ }).click();
        await status("files").waitFor();
        await page.waitForTimeout(150);
        assert.equal(checks.length, 1, "the servers were checked again on coming back");
        assert.equal(await status("files").textContent(), w.files[0]);

        // a click checks that server again, opens no editor, moves nothing
        // scrolled a little first, by the wheel as the reader would
        await page.mouse.move(490, 250);
        await page.mouse.wheel(0, 40);
        await page.waitForFunction(() => document.querySelector("#view-library").scrollTop > 0);
        await page.waitForTimeout(400);
        const top = await page.evaluate(() => document.querySelector("#view-library").scrollTop);
        await status("files").click();
        assert.equal(await page.evaluate(() => document.querySelector("#view-library").scrollTop), top);
        for (let i = 0; i < 50 && checks.length < 2; i++) await page.waitForTimeout(20);
        assert.deepEqual({ names: checks[1].names, fresh: checks[1].fresh }, { names: ["files"], fresh: true });
        assert.equal(await status("files").textContent(), w.checking);
        assert.equal(await status("linear").textContent(), w.linear[0], "another row was touched");
        checks[1].release();
        await page.waitForFunction((t) => document.querySelector('.lib-health[data-server="files"]').textContent === t, w.fresh);
        assert.equal(await page.evaluate(() => document.querySelector("#modal").classList.contains("lib")), false, "the click opened the editor");
        assert.equal(await page.evaluate(() => document.querySelector("#view-library").scrollTop), top);
        assert.equal(await page.locator("#view-library select").count(), 0);
        await ctx.close();
      });
    }
  });
}
