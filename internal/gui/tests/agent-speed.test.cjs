// Run with Node's test runner and Playwright on the module path; see README.md.
// Speed beside Model and Effort (Tengs999 on X): an agent whose model has a
// fast mode has a Speed line in its opened row, in the tray panel and under
// the window's New sessions. It says the speed the requests go at now and
// what decides it: the agent's own fast pick, else the model's own speed
// (its Model settings), else for Codex its service tier in Settings, or as
// Codex's /fast asks. Its menu is the app's, and sets the agent's own fast
// pick through /api/agent-fast, the switch the model's bolt in the picker
// is; a refusal puts it back. A model with no fast mode has no Speed. Window
// and 440px tray panel, en zh zh-TW ja de, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixtures(c) {
  const options = [
    { value: "magpie/codex/gpt-5.5", ref: "codex/gpt-5.5", label: "GPT-5.5", group: "ChatGPT", fastFor: c.agent, fast: !!c.pick, ultra: c.pick === "ultrafast", ultraOk: true },
    { value: "magpie/zai/glm-5", ref: "zai/glm-5", label: "GLM-5", group: "Z.ai" },
  ];
  const effort = { key: "effort", label: "effort", value: "high", options: ["low", "medium", "high"].map((v) => ({ value: v, label: v })) };
  const name = c.agent === "codex" ? "Codex" : "OpenCode";
  return [{ id: c.agent, name, path: "/test/" + c.agent, wired: true, fields: [{ key: "model", label: "model", value: c.model || "magpie/codex/gpt-5.5", options }, effort] }];
}

function server(lang, c, posts, refuse) {
  const agents = fixtures(c);
  const settings = { lang, theme: "light", modelTiers: c.modelTier ? { "codex/gpt-5.5": c.modelTier } : undefined, codexServiceTier: c.codexTier || "" };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/") && !url.pathname.startsWith("/api/window/")) {
      if (url.pathname === "/api/agent-fast") {
        posts.push(req.postDataJSON());
        if (refuse()) return route.fulfill({ status: 500, contentType: "text/plain", body: "no fast mode" });
        return json({ fast: req.postDataJSON().fast });
      }
      return json({});
    }
    if (url.pathname === "/api/state") return json({ agents, profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const said = {
  en: { usual: "Usual", fast: "Fast", ultra: "Ultrafast", ask: "As Codex asks", model: "model setting", codex: "Codex setting", dflt: "Default" },
  zh: { usual: "标准", fast: "快速", ultra: "极速", ask: "按 Codex 的请求", model: "模型设置", codex: "Codex 设置", dflt: "默认" },
  "zh-TW": { usual: "標準", fast: "快速", ultra: "極速", ask: "依 Codex 的請求", model: "模型設定", codex: "Codex 設定", dflt: "預設" },
  ja: { usual: "標準", fast: "高速", ultra: "超高速", ask: "Codex の指定どおり", model: "モデル設定", codex: "Codex 設定", dflt: "デフォルト" },
  de: { usual: "Normal", fast: "Schnell", ultra: "Ultrafast", ask: "Wie Codex es anfragt", model: "Modelleinstellung", codex: "Codex-Einstellung", dflt: "Standard" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Speed beside Model and Effort", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const panel of [false, true]) {
      await t.test(`${lang} ${panel ? "tray panel 440px" : "window"}`, async () => {
        const w = said[lang];
        const open = async (c, posts = [], refuse = () => false) => {
          const page = await (await browser.newContext({ viewport: panel ? { width: 440, height: 640 } : { width: 1100, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, c, posts, refuse));
          await page.goto("http://magpie.test/" + (panel ? "?mode=panel" : "?view=agents"));
          const row = `.row.agent[data-id="${c.agent}"]`;
          await page.locator(row).waitFor();
          if (panel) await page.locator(`${row} .ag-sum`).click();
          else await page.locator(`${row} .ag-link`).click();
          await page.locator(`${row} ${panel ? ".ag-open" : ".ag-exp"} .field[data-key="effort"], ${row} .ag-open .effort-control`).first().waitFor();
          await page.waitForTimeout(400);
          return { page, speed: page.locator(`${row} .field.ag-speed`) };
        };
        const value = (speed) => speed.locator(".v").evaluate((v) => v.textContent.trim());

        // the agent's own pick: off, then Fast from the app's menu
        {
          const posts = [];
          let refusing = false;
          const { page, speed } = await open({ agent: "codex", codexTier: "default" }, posts, () => refusing);
          assert.equal(await speed.count(), 1, "a model with a fast mode has Speed");
          assert(await speed.isVisible());
          assert.equal(await value(speed), w.usual);
          assert.equal(await speed.locator(".bolts, .v > svg").count(), 0);
          // it sits with Model and Effort, inside its row's width
          const fits = await speed.evaluate((b) => { const r = b.getBoundingClientRect(), p = b.closest(".row").getBoundingClientRect(); return r.width > 0 && r.left >= p.left - 0.5 && r.right <= p.right + 0.5; });
          assert(fits, "Speed fits in its row");
          await speed.click();
          const menu = page.locator(".proto-menu");
          await menu.waitFor();
          const names = await menu.locator(".pm-name").allTextContents();
          assert.deepEqual(names, [w.dflt, w.fast, w.ultra]);
          await menu.locator(".pm-item").nth(1).click();
          await page.waitForTimeout(200);
          assert.deepEqual(posts, [{ for: "codex", ref: "codex/gpt-5.5", fast: true }]);
          assert.equal(await value(speed), w.fast);
          assert.equal(await speed.locator(".v > svg").count(), 1, "a bolt for Fast");
          // a refusal puts it back
          refusing = true;
          await speed.click();
          await menu.locator(".pm-item").nth(2).click();
          await page.waitForTimeout(200);
          assert.deepEqual(posts[1], { for: "codex", ref: "codex/gpt-5.5", fast: true, ultra: true });
          assert.equal(await value(speed), w.fast, "refused: still Fast");
          await page.context().close();
        }
        // the model's own speed, with no pick of the agent's
        {
          const { page, speed } = await open({ agent: "opencode", modelTier: "priority" });
          assert.equal(await value(speed), `${w.fast} · ${w.model}`);
          assert.match(await speed.getAttribute("title"), /\n/, "the title says what decides it");
          await page.context().close();
        }
        // Codex's service tier in Settings over no pick, and as Codex asks
        {
          const { page, speed } = await open({ agent: "codex", codexTier: "priority" });
          assert.equal(await value(speed), `${w.fast} · ${w.codex}`);
          await page.context().close();
        }
        {
          const { page, speed } = await open({ agent: "codex", codexTier: "" });
          assert.equal(await value(speed), w.ask);
          await page.context().close();
        }
        // the agent's Ultrafast pick, and Codex's Standard doesn't slow it
        {
          const { page, speed } = await open({ agent: "codex", codexTier: "default", pick: "ultrafast" });
          assert.equal(await value(speed), w.ultra);
          assert.equal(await speed.locator(".v .bolts svg").count(), 2);
          await page.context().close();
        }
        // a model with no fast mode: no Speed
        {
          const { page, speed } = await open({ agent: "codex", model: "magpie/zai/glm-5" });
          assert.equal(await speed.count(), 0);
          await page.context().close();
        }
      });
    }
    assert.deepEqual(errors, []);
  });
}
