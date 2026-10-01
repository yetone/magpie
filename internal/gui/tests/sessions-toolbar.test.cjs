// Sessions with many agents: tabs switch in one click when they fit, a compact
// menu takes over when they don't, and both keep the current agent visible.
// Search and Trash fit a narrow window. English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const agents = [
  ["qodercn", "Qoder CN", "qoder", 426], ["qoder", "Qoder", "qoder", 102],
  ["pi", "Pi", "pi", 89], ["claude", "Claude Code", "claudecode-color", 83],
  ["deepseek", "DeepSeek Harness", "deepseek-color", 70], ["codex", "Codex", "codex-color", 37],
  ["workbuddy", "WorkBuddy", "workbuddy-color", 24], ["opencode", "OpenCode", "opencode", 11],
  ["zcode", "ZCode", "zcode", 10], ["grokbuild", "Grok Build", "xai", 3],
].map(([agent, name, icon, count]) => ({ agent, name, icon, count, deletable: true }));

function serve(lang, theme) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      const agent = url.searchParams.get("agent") || agents[0].agent;
      const count = agents.find((a) => a.agent === agent).count;
      const sessions = Array.from({ length: count }, (_, i) => ({
        agent, id: `${agent}-${i}`, cwd: "/work/app", title: i ? `Session ${i + 1}` : "Fix the session toolbar",
        start: new Date(Date.now() - (i + 2) * 60e3).toISOString(), last: new Date(Date.now() - (i + 1) * 60e3).toISOString(),
        models: [], messages: 6, files: 1, size: 4096, deletable: true, resume: `${agent} --resume ${agent}-${i}`,
      }));
      return json({ agents, agent, sessions, trash: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    return body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Sessions switches between fitting tabs and a compact agent menu`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium", ignoreDefaultArgs: ["--hide-scrollbars"] }));
      t.after(() => browser.close());
      for (const theme of ["light", "dark"]) {
        const context = await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        // The fixture renders hundreds of sessions, also on Windows WebKit.
        page.setDefaultTimeout(10000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme));
        await page.goto("http://magpie.test/");
        await page.locator('#nav [data-view="sessions"]').click();
        const view = page.locator("#view-sessions");
        const picker = view.locator(".sm-agent-pick");
        const tabs = view.locator(".sm-agents");
        const settle = () => page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        await picker.waitFor();
        await page.waitForLoadState("networkidle");
        assert((await picker.textContent()).includes("Qoder CN"));
        assert.equal(await picker.locator("em").textContent(), "426");
        const filter = view.locator(".sm-filter");
        await filter.fill("Fix the session toolbar");
        assert.equal(await view.locator(".row.sm-sess").count(), 1, "search still filters the current agent's sessions");
        await filter.press("Escape");
        assert.equal(await view.locator(".row.sm-sess").count(), 426, "Escape clears the session search");
        await filter.evaluate((e) => e.blur());
        for (const width of [1800, 900, 660, 320]) {
          await page.setViewportSize({ width, height: 560 });
          await settle();
          if (width === 1800) {
            assert(await tabs.isVisible(), "a wide window keeps the one-click agent tabs");
            assert(await picker.isHidden(), "the menu button is hidden when the tabs fit");
            assert(await tabs.evaluate((e) => e.scrollWidth <= e.clientWidth), "visible tabs have no horizontal scrollbar");
            await tabs.locator(".on").focus();
            await page.evaluate(() => { document.documentElement.style.zoom = "1.5"; });
            await settle();
            assert(await tabs.isHidden(), "150% text zoom switches to the compact picker");
            assert(await picker.evaluate((e) => document.activeElement === e), "narrowing moves tab focus to the picker");
            await page.evaluate(() => { document.documentElement.style.zoom = ""; });
            await settle();
            assert(await tabs.isVisible(), "restoring text size returns the fitting tabs");
            assert(await tabs.locator(".on").evaluate((e) => document.activeElement === e), "widening returns focus to the selected tab");
            await tabs.locator(".on").evaluate((e) => e.blur());
          } else {
            assert(await tabs.isHidden(), "overflowing tabs give way to the menu");
            assert(await picker.isVisible());
          }
          const boxes = await view.locator(".sm-switch, .sm-filter, .sm-trash-btn").evaluateAll((els) => els.map((e) => {
            const b = e.getBoundingClientRect();
            return { name: e.className, left: b.left, right: b.right, top: b.top, bottom: b.bottom, width: b.width };
          }));
          assert(boxes.every((b) => b.width > 0 && b.left >= 0 && b.right <= width), `${theme} ${width}px: header controls fit: ${JSON.stringify(boxes)}`);
          if (width === 320) {
            assert(boxes[1].top >= boxes[0].bottom, "a narrow window puts search below the picker");
            assert(Math.abs(boxes[1].left - boxes[0].left) < 1, "the narrow search field aligns with the picker");
          }
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-${width}-sessions-toolbar.png`) });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-${width}-toolbar-only.png`), clip: { x: 0, y: 0, width, height: width === 320 ? 150 : 126 } });
          }
        }
        await picker.click();
        const menu = page.locator(".sm-agent-menu");
        await menu.waitFor();
        await menu.evaluate((e) => Promise.all(e.getAnimations().map((a) => a.finished.catch(() => {}))));
        assert.equal(await menu.getByRole("menuitemradio").count(), agents.length);
        assert.equal(await picker.getAttribute("aria-expanded"), "true");
        const mb = await menu.boundingBox();
        assert(mb.x >= 0 && mb.x + mb.width <= 320, "the open menu fits a narrow window");
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-menu-sessions-toolbar.png`) });
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        assert.equal(await picker.getAttribute("aria-expanded"), "false");
        assert(await picker.evaluate((e) => document.activeElement === e), "Escape returns focus to the picker");
        await page.setViewportSize({ width: 660, height: 560 });
        await settle();
        await picker.press("Enter");
        await menu.waitFor();
        await page.keyboard.press("ArrowUp");
        assert((await page.locator(":focus").textContent()).includes("Grok Build"), "the last agent is reachable with the keyboard");
        await page.keyboard.press("Enter");
        await menu.waitFor({ state: "detached" });
        await page.waitForFunction(() => document.querySelector("#view-sessions .sm-agent-pick")?.textContent.includes("Grok Build") && !document.querySelector("#view-sessions .skeleton"));
        assert.equal(await picker.locator("em").textContent(), "3");
        assert(await picker.isVisible(), "the loaded agent stays visible in the picker");
        assert(await picker.evaluate((e) => document.activeElement === e), "choosing an agent returns focus to the loaded picker");
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${theme}-selected-sessions-toolbar.png`) });
        await page.setViewportSize({ width: 1800, height: 560 });
        await settle();
        assert((await tabs.locator(".on").textContent()).includes("Grok Build"), "widening keeps the current agent selected");
        await tabs.locator(".opt").first().click();
        await page.waitForFunction(() => document.querySelector("#view-sessions .sm-agents .on")?.textContent.includes("Qoder CN") && !document.querySelector("#view-sessions .skeleton"));
        await page.setViewportSize({ width: 660, height: 560 });
        await settle();
        assert((await picker.textContent()).includes("Qoder CN"), "narrowing keeps the agent chosen in the tabs");
        await view.locator(".sm-trash-btn").click();
        assert.equal(await view.locator(".sm-trash-btn").getAttribute("aria-pressed"), "true");
        await picker.click();
        await menu.getByRole("menuitemradio", { name: /OpenCode/ }).click();
        await page.waitForFunction(() => document.querySelector("#view-sessions .sm-agent-pick")?.textContent.includes("OpenCode") && !document.querySelector("#view-sessions .skeleton"));
        assert.equal(await view.locator(".sm-trash-btn").getAttribute("aria-pressed"), "false", "choosing an agent leaves Trash");
        assert(await view.locator(".sm-filter").isVisible());
        await picker.click();
        assert.equal(await menu.getByRole("menuitemradio", { checked: true }).count(), 1, "the menu marks only the current agent");
        await view.locator(".sm-filter").click();
        await menu.waitFor({ state: "detached" });
        await view.locator(".sm-trash-btn").click();
        await view.locator(".sm-trash-btn").click();
        assert((await picker.textContent()).includes("OpenCode"), "leaving Trash preserves the current agent");
        assert.deepEqual(errors, []);
        await context.close();
      }
    });
  }
}
