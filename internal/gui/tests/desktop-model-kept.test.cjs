// Run with Node's test runner and Playwright on the module path; see README.md.
// whysugarsweet on X: Claude Desktop narrowed to some providers (magpie
// visible, from the terminal) never listed OpenCode Go, added after, while
// Pi listed it, and Desktop's model list had no way to it. The models its
// visibility keeps come in the list, off, under their provider; ticking one
// is posted as widen, so the agent is shown that provider, whose other
// models are then listed, off, and ticked as any other (no widen again).
// The row counts only what is listed. Window (with the rail) and the tray
// panel (no rail), Chromium and WebKit, every language. No backend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// a real Desktop row has its tiers' fields too
const tiers = ["opus", "sonnet", "haiku", "fable"];
const options = [{ value: "deepseek/deepseek-v4", label: "DeepSeek V4", ref: "deepseek/deepseek-v4" }];

function fixture(lang) {
  const list = [
    { id: "deepseek/deepseek-v4", name: "DeepSeek V4", group: "DeepSeek", icon: "deepseek" },
    { id: "grok/grok-4.6", name: "grok-4.6", group: "Grok", icon: "generic" },
    { id: "opencode-go/kimi-k2.6", name: "Kimi K2.6", group: "OpenCode Go", icon: "opencode", hidden: true, kept: true },
    { id: "opencode-go/glm-5.1", name: "GLM-5.1", group: "OpenCode Go", icon: "opencode", hidden: true, kept: true },
  ];
  const posts = [];
  const count = () => {
    const on = list.filter((m) => !m.hidden);
    return { shown: on.length, listed: list.filter((m) => !m.kept).length, by: [], names: on.slice(0, 3).map((m) => m.name) };
  };
  const state = () => ({
    agents: [{
      id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired: true,
      fields: [
        { key: "provider", label: "provider", value: "magpie", options: [{ value: "magpie", label: "magpie", icon: "magpie" }] },
        ...tiers.map((tier) => ({ key: tier, label: tier, value: "", options })),
      ],
      models: count(),
    }],
    profiles: [], settings: { lang, theme: "light" },
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/agent-models/claude-desktop") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        // as the server does: a widened one's provider is listed
        const groups = new Set(list.filter((m) => body.widen?.includes(m.id)).map((m) => m.group));
        for (const m of list) {
          if (groups.has(m.group)) m.kept = false;
          m.hidden = m.kept || body.hidden.includes(m.id);
        }
        return json({ models: list, count: count() });
      }
      return json({ models: list });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    if (!body) return route.fulfill({ status: 404, body: "" });
    await route.fulfill({ body, contentType });
  }
  return { serve, posts };
}

const cd = '.row.agent[data-id="claude-desktop"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const where of ["window", "panel"]) {
      test(`${engine} ${lang} ${where}: a provider Claude Desktop's visibility keeps is in its list, and ticked is shown it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: where === "panel" ? { width: 420, height: 640 } : { width: 1100, height: 700 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const fx = fixture(lang);
        await page.route("**/*", fx.serve);
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${where}-desktop-kept.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/" + (where === "panel" ? "?mode=panel" : ""));
        await page.locator(cd).waitFor();
        let menu;
        if (where === "window") menu = page.locator(`${cd} > .ag-menu`);
        else {
          await page.locator(`${cd} .ag-sum`).click();
          menu = page.locator(`${cd} .ag-open .ag-menu`);
          await page.waitForFunction((sel) => document.querySelector(sel)?.getBoundingClientRect().height > 0, `${cd} .ag-open .ag-menu`);
        }
        assert.equal(await menu.locator(".v").textContent(), await page.evaluate(() => t("All {n} models", { n: 2 })), "a kept one counted as listed");
        const y = await page.evaluate(() => scrollY);
        await menu.click();
        const box = page.locator(".am-pop:not(.leaving)");
        await box.waitFor();
        const rows = () => box.locator(".am-mr").evaluateAll((es) => es.map((e) => [e.querySelector(".n").textContent, e.getAttribute("aria-checked")]));
        assert.deepEqual(await rows(), [["DeepSeek V4", "true"], ["grok-4.6", "true"], ["Kimi K2.6", "false"], ["GLM-5.1", "false"]]);
        if (where === "window") {
          assert.deepEqual(await box.locator(".am-rail .am-ri .c").allInnerTexts(), ["2/4", "1/1", "1/1", "0/2"]);
        } else assert.equal(await box.locator(".am-rail .am-ri").count(), 0, "a rail at 420px");
        const box1 = await box.boundingBox();
        assert.ok(box1.x >= 0 && box1.x + box1.width <= (where === "panel" ? 420 : 1100), JSON.stringify(box1));

        // wait on the posts, never on a duration
        const posted = async (n) => {
          for (const end = Date.now() + 5000; Date.now() < end; await new Promise((r) => setTimeout(r, 25))) if (fx.posts.length === n) return;
          assert.fail(n + " posts");
        };
        // Kimi ticked: sent as widen, GLM as taken out
        await box.locator(".am-mr", { hasText: "Kimi K2.6" }).click();
        await posted(1);
        assert.deepEqual(fx.posts[0].widen, ["opencode-go/kimi-k2.6"]);
        assert.deepEqual(fx.posts[0].hidden, ["opencode-go/glm-5.1"]);
        const some = await page.evaluate(() => t("Showing {shown} / {listed} models", { shown: 3, listed: 4 }));
        await page.waitForFunction((s) => document.querySelector(".ag-menu .v")?.textContent === s, some);
        // GLM ticked after: listed now, so no widen
        await box.locator(".am-mr", { hasText: "GLM-5.1" }).click();
        await posted(2);
        assert.equal(fx.posts[1].widen, undefined, JSON.stringify(fx.posts[1]));
        assert.deepEqual(fx.posts[1].hidden, []);
        const all = await page.evaluate(() => t("All {n} models", { n: 4 }));
        await page.waitForFunction((s) => document.querySelector(".ag-menu .v")?.textContent === s, all);
        assert.deepEqual(await rows(), [["DeepSeek V4", "true"], ["grok-4.6", "true"], ["Kimi K2.6", "true"], ["GLM-5.1", "true"]]);

        assert.equal(await page.evaluate(() => scrollY), y, "a click scrolled the page");
        assert.deepEqual(errors, []);
      });
    }
  }
}
