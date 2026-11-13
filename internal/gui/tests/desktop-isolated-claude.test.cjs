// Run with Node's test runner and Playwright on the module path; see README.md.
// The Claude Magpie app's Code tab on a Claude Code folder of its own
// (#6459, xMopf7RF on X): with Claude Magpie on, Claude Desktop's opened
// row folds an "Advanced" part under its switch, shut while the folder is
// off, open while it is on. Its switch posts the new choice, follows the
// state the reply carries, names the folder, and the fold stays open
// through the redraw; no click scrolls the page. Claude Magpie off: no
// fold. Every language, wide and narrow. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const DIR = "/test/home/.claude-magpie-desktop";

function fixture(lang, iso) {
  const settings = { lang, theme: "light" };
  const posts = [];
  const state = () => ({
    agents: [{
      id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired: true,
      fields: [{ key: "provider", label: "provider", value: "magpie", options: [{ value: "magpie", label: "magpie", icon: "magpie" }] }],
      models: { shown: 2, listed: 2, by: [{ name: "OpenCode Go", icon: "opencode", n: 2 }], names: ["DeepSeek V4 Pro", "Exo Free"] },
      isolated: { ...iso },
    }],
    profiles: [], settings,
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/settings/desktop-isolated-claude") {
      const body = req.postDataJSON();
      posts.push(body);
      iso.claude = body.on;
      return json(state());
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  }
  return { serve, posts };
}

const W = {
  en: { adv: "Advanced", say: "Its own Claude Code folder", hint: `Its Code tab uses ${DIR}, without your CLAUDE.md, skills, plugins or history. Quit and reopen Claude Magpie after a change.`, ok: "Saved — quit and reopen Claude Magpie to use it", details: "Details" },
  zh: { adv: "高级", say: "这个分身自己的 Claude Code 文件夹", hint: `它的 Code 标签页使用 ${DIR}，不带你的 CLAUDE.md、技能、插件和历史记录。改动后退出并重新打开 Claude Magpie。`, ok: "已保存——退出并重新打开 Claude Magpie 后生效", details: "详情" },
  "zh-TW": { adv: "進階", say: "這個分身自己的 Claude Code 資料夾", hint: `它的 Code 分頁使用 ${DIR}，不帶你的 CLAUDE.md、技能、外掛和歷史記錄。變更後結束並重新開啟 Claude Magpie。`, ok: "已儲存——結束並重新開啟 Claude Magpie 後生效", details: "詳情" },
  ja: { adv: "詳細設定", say: "専用の Claude Code フォルダ", hint: `Code タブは ${DIR} を使います。あなたの CLAUDE.md、スキル、プラグイン、履歴は引き継がれません。変更後は Claude Magpie を終了して開き直してください。`, ok: "保存しました。Claude Magpie を終了して開き直すと反映されます", details: "詳細" },
  de: { adv: "Erweitert", say: "Eigener Claude-Code-Ordner", hint: `Der Code-Tab nutzt ${DIR}, ohne deine CLAUDE.md, Skills, Plugins und Verlauf. Beende Claude Magpie nach einer Änderung und öffne es neu.`, ok: "Gespeichert – beende Claude Magpie und öffne es neu, um es zu nutzen", details: "Details" },
};
const cd = '.row.agent[data-id="claude-desktop"]';
const more = `${cd} .ag-exp .ag-iso-more`;
const sw = `${more} .ag-iso-claude .lib-switch`;

async function open(engine, lang, width, iso, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 700 } });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const fx = fixture(lang, iso);
  await page.route("**/*", fx.serve);
  t.after(() => browser.close());
  await page.goto("http://magpie.test/");
  await page.locator(cd).waitFor();
  await page.locator(`${cd} .ag-link`, { hasText: W[lang].details }).click();
  await page.locator(`${cd} .ag-exp .ag-isolated`).waitFor();
  // the fold comes from claudemagpie.js, imported when it first shows
  if (iso.on) await page.locator(more).waitFor({ state: "attached" });
  return { page, fx, errors };
}

const isOpen = (page) => page.evaluate((sel) => document.querySelector(sel)?.open, more);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: Claude Magpie's own Claude Code folder, under Advanced`, async (t) => {
        const w = W[lang];
        const { page, fx, errors } = await open(engine, lang, width, { on: true, launcher: "/test/Applications/Claude Magpie.app", claude: false, claudeDir: DIR }, t);
        assert.equal(await isOpen(page), false, "open while off");
        assert.equal(await page.locator(`${more} > summary`).textContent(), w.adv);
        assert.equal(await page.locator(`${more} .ag-iso-claude`).isVisible(), false);

        const y = await page.evaluate(() => scrollY);
        await page.locator(`${more} > summary`).click();
        const line = page.locator(`${more} .ag-iso-claude`);
        await line.waitFor();
        assert.equal(await line.locator(".ag-iso-claude-say").textContent(), w.say);
        assert.equal(await line.locator(".ag-hint").textContent(), w.hint);
        assert.equal(await page.locator(sw).getAttribute("aria-checked"), "false");
        const box = await line.boundingBox();
        assert.ok(box.x + box.width <= width && box.height > 0, JSON.stringify(box));

        await page.locator(sw).click();
        await page.waitForFunction((sel) => document.querySelector(sel)?.getAttribute("aria-checked") === "true", sw);
        assert.deepEqual(fx.posts, [{ on: true }]);
        assert.equal(await page.locator("#status").textContent(), w.ok);
        assert.equal(await isOpen(page), true, "shut by the redraw");
        // and off again, by its words: still open
        await line.locator(".ag-iso-claude-say").click();
        await page.waitForFunction((sel) => document.querySelector(sel)?.getAttribute("aria-checked") === "false", sw);
        assert.deepEqual(fx.posts, [{ on: true }, { on: false }]);
        assert.equal(await isOpen(page), true, "shut by the redraw");
        // the Claude Magpie switch is still the first line
        assert.equal(await page.locator(`${cd} .ag-isolated .lib-switch`).getAttribute("aria-checked"), "true");

        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
        assert.equal(await page.locator("select").count(), 0, "a native select");
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: on, the fold opens by itself`, async (t) => {
    const { page, errors } = await open(engine, "en", 1100, { on: true, claude: true, claudeDir: DIR }, t);
    assert.equal(await isOpen(page), true);
    assert.equal(await page.locator(sw).getAttribute("aria-checked"), "true");
    assert.deepEqual(errors, []);
  });
  test(`${engine}: no fold while Claude Magpie is off`, async (t) => {
    const { page, errors } = await open(engine, "en", 1100, { on: false }, t);
    assert.equal(await page.locator(more).count(), 0);
    assert.equal(await page.locator(`${cd} .ag-iso-claude`).count(), 0);
    assert.deepEqual(errors, []);
  });
}
