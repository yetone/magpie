// Run with Node's test runner and Playwright on the module path; see README.md.
// A session's conversation exported as Markdown (sxwedo, #1276). A session
// whose conversation can be read has Export Markdown beside Show
// conversation in its details. In the app it posts sessions/export with the
// session's agent and id (the server finds the file and saves to Downloads)
// and the status says where it went; in magpie web the browser downloads
// /api/sessions/markdown. It doesn't open or close the row, nor move the
// page; an agent whose sessions can't be read has no such button. In
// English and Chinese, Chromium and WebKit, at a narrow width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (agent, id, title, min, extra) => ({
  agent, id, cwd: "/work/app", title, start: at(min + 5), last: at(min), models: [],
  resume: `claude --resume ${id}`, path: `~/.claude/projects/-work-app/${id}.jsonl`, size: 4096, messages: 6, files: 1, deletable: true, ...extra,
});

function serve(lang, web, calls) {
  const store = {
    claude: [sess("claude", "c-1", "Rename foo", 5, { transcript: true })],
    opencode: [{ ...sess("opencode", "o-1", "an opencode chat", 8), deletable: false, resume: "opencode -s o-1" }],
  };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      const agent = url.searchParams.get("agent") === "opencode" ? "opencode" : "claude";
      return json({
        agents: [
          { agent: "claude", count: 1, deletable: true, name: "Claude Code", icon: "claude" },
          { agent: "opencode", count: 1, deletable: false, name: "OpenCode", icon: "opencode" },
        ],
        agent, sessions: store[agent], terminal: false, trash: [], trashDir: "~/trash",
      });
    }
    if (url.pathname === "/api/sessions/export") {
      calls.push({ path: "export", method: route.request().method(), agent: url.searchParams.get("agent"), id: url.searchParams.get("id") });
      return json({ path: "~/Downloads/magpie-session-claude-c-1.md" });
    }
    if (url.pathname === "/api/sessions/transcript") {
      calls.push({ path: "transcript" });
      return json({ parts: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    if (!body) return route.fulfill({ status: 404, body: "" });
    await route.fulfill({ body, contentType });
  };
}

const words = {
  en: { nav: "Sessions", exp: "Export Markdown", saved: "Saved to ~/Downloads/magpie-session-claude-c-1.md" },
  zh: { nav: "会话", exp: "导出 Markdown", saved: "已保存到 ~/Downloads/magpie-session-claude-c-1.md" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const web of [false, true]) {
      const w = words[lang];
      test(`${engine} ${lang}${web ? " web" : ""}: a session's conversation is exported as Markdown`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width: 560, height: 600 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        const calls = [];
        await page.route("**/*", serve(lang, web, calls));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#nav").getByRole("button", { name: w.nav, exact: true }).click();
        const view = page.locator("#view-sessions");
        const row = view.locator('.row.sm-sess[data-id="c-1"]');
        await row.waitFor();
        await row.locator(".who").click();
        const detail = view.locator(".sess-detail");
        await detail.waitFor();
        const exp = detail.locator(".sess-export");
        assert.equal((await exp.textContent()).trim(), w.exp);
        assert(await exp.isVisible(), "the button is in sight at 560px");
        const box = await exp.boundingBox();
        assert(box.x + box.width <= 560, `the button fits the window: ${JSON.stringify(box)}`);
        const top = await row.evaluate((e) => e.getBoundingClientRect().top);
        if (web) {
          // the browser's own download of sessions/markdown, its name the
          // server's (Content-Disposition, checked in Go): a download of a
          // routed response isn't one Playwright sees in every engine, so
          // the link followed is what is checked
          await page.evaluate(() => { window.followed = []; HTMLAnchorElement.prototype.click = function () { window.followed.push([this.href, this.hasAttribute("download")]); }; });
          await exp.click();
          assert.deepEqual(await page.evaluate(() => window.followed), [["http://magpie.test/api/sessions/markdown?agent=claude&id=c-1", true]]);
          assert.equal(calls.filter((c) => c.path === "export").length, 0);
        } else {
          await exp.click();
          await page.locator("#status", { hasText: w.saved }).waitFor();
          assert.deepEqual(calls.filter((c) => c.path === "export"), [{ path: "export", method: "POST", agent: "claude", id: "c-1" }]);
        }
        await page.waitForTimeout(200);
        assert.equal(await row.evaluate((e) => e.getBoundingClientRect().top), top, "the click moved the page");
        assert(await detail.isVisible(), "the row stayed open");
        assert.equal(calls.filter((c) => c.path === "transcript").length, 0, "the conversation wasn't shown");

        // an agent whose sessions can't be read has nothing to export
        await view.locator(".sm-agents .opt", { hasText: "OpenCode" }).click();
        const o = view.locator('.row.sm-sess[data-id="o-1"]');
        await o.waitFor();
        await o.locator(".who").click();
        await view.locator(".sess-detail").waitFor();
        assert.equal(await view.locator(".sess-export").count(), 0);

        if (lang === "zh") {
          const missing = await page.evaluate(() => ["Export Markdown", "Save the whole conversation as a Markdown file"]
            .filter((k) => !I18N.zh[k] || !I18N["zh-TW"][k] || !I18N.ja[k] || !I18N.de[k]));
          assert.deepEqual(missing, [], "every string has its Chinese, Traditional Chinese, Japanese and German");
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
