// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's model list that is still loading when the Agents page is drawn
// again (the state re-read after a changed list closes, or any refresh): the
// click isn't dropped. The list comes up under the redrawn button, which is
// marked open and gets the focus back on Escape, and a click on the redrawn
// button while it loads takes it back, as one on the first would. For each
// button that opens the list: a connected row's Pick, Claude Desktop's model
// menu button, and the line under a name that counts the models.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture() {
  const list = [
    { id: "openai/gpt-5.5", name: "GPT-5.5", group: "OpenAI", icon: "openai", context: 272e3 },
    { id: "openai/gpt-5.4", name: "GPT-5.4", group: "OpenAI", icon: "openai", context: 272e3 },
    { id: "zai/glm-5.2", name: "GLM-5.2", group: "Z.ai", icon: "zai", context: 1e6 },
  ];
  const fx = { load: 0, stateWait: 0, posts: 0 };
  const count = () => {
    const on = list.filter((m) => !m.hidden);
    return { shown: on.length, listed: list.length, names: on.map((m) => m.name) };
  };
  const state = () => ({
    agents: [
      {
        id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
        fields: [{ key: "model", label: "model", value: "magpie/openai/gpt-5.5", options: [{ value: "magpie/openai/gpt-5.5", label: "GPT-5.5", ref: "openai/gpt-5.5" }] }],
        models: count(),
      },
      {
        id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired: true,
        fields: [{ key: "provider", label: "provider", value: "magpie", options: [{ value: "magpie", label: "magpie", icon: "magpie" }] }],
        models: count(),
      },
      // nothing to connect: its models counted on the line under its name
      { id: "plain", name: "Plain", path: "/test/plain.json", icon: "generic", fields: [], models: { shown: 0, listed: 0 } },
    ],
    profiles: [], settings: { lang: "en", theme: "light" },
  });
  fx.serve = async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") {
      if (fx.stateWait) await new Promise((r) => setTimeout(r, fx.stateWait));
      return json(state());
    }
    if (url.pathname.startsWith("/api/agent-models/")) {
      if (req.method() === "POST") {
        fx.posts++;
        const { hidden } = req.postDataJSON();
        for (const m of list) m.hidden = hidden.includes(m.id);
        return json({ models: list, count: count() });
      }
      if (fx.load) await new Promise((r) => setTimeout(r, fx.load));
      return json({ models: list });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
  return fx;
}

// the buttons that open an agent's model list, found again after each redraw
const BUTTONS = {
  "a connected row's Pick": '.row.agent[data-id="codex"] .ag-exp .ag-chips .ag-quiet',
  "Claude Desktop's model menu": '.row.agent[data-id="claude-desktop"] > .ag-menu',
  "the line counting the models": '.row.agent[data-id="plain"] .ag-models',
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 560]) {
    test(`${engine} ${width}px: an agent's model list loading while the agents are drawn again`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width, height: 700 } });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const fx = fixture();
      await page.route("**/*", fx.serve);
      t.after(() => browser.close());
      await page.goto("http://magpie.test/");
      await page.locator('.row.agent[data-id="codex"] .ag-link').click();
      // Plain, with nothing set, is folded under the two connected ones
      await page.locator("#agents .agent-more").click();
      await page.locator('#agents .agent-more[aria-expanded="true"]').waitFor();
      const pop = page.locator(".am-pop:not(.leaving)");
      const redraw = () => page.evaluate(() => renderAgents());

      // every case is checked, so a run says which of them fail
      const failed = [];
      const check = (ok, said) => { if (!ok) failed.push(said); };
      const opened = () => pop.waitFor({ timeout: 2000 }).then(() => true, () => false);
      const shut = async () => {
        if (!await pop.count()) return;
        await page.keyboard.press("Escape");
        await pop.waitFor({ state: "detached" });
      };

      // in view and clear of the footer, scrolled to with the wheel: the
      // page puts back a scroll that isn't the reader's (scrollIntoView,
      // Playwright's own before a click)
      const pressOn = async (btn) => {
        const [y, top, bottom] = await btn.evaluate((e) => {
          const r = e.getBoundingClientRect(), v = e.closest(".view").getBoundingClientRect();
          return [r.top + r.height / 2, v.top, Math.min(v.bottom, document.querySelector("footer.foot")?.getBoundingClientRect().top ?? v.bottom)];
        });
        if (y < top + 20 || y > bottom - 20) {
          await page.mouse.move(width / 2, (top + bottom) / 2);
          await page.mouse.wheel(0, y - (top + bottom) / 2);
          await page.waitForTimeout(300);
        }
        await btn.click();
      };

      for (const [name, sel] of Object.entries(BUTTONS)) {
        const btn = page.locator(sel);
        await btn.waitFor();
        const press = () => pressOn(btn);
        fx.load = 400;

        // drawn again while it loads: the list still comes, at the new button
        await press();
        await redraw();
        if (await opened()) {
          assert.equal(await btn.count(), 1);
          check(/\bopen\b/.test(await btn.getAttribute("class")), `${name}: the redrawn button isn't marked open`);
          // where it was put, not where its opening animation has it now
          const gap = await pop.evaluate((e, sel) => {
            const r = document.querySelector(sel).getBoundingClientRect();
            return e.classList.contains("up") ? r.top - (innerHeight - parseFloat(e.style.bottom)) : parseFloat(e.style.top) - r.bottom;
          }, sel);
          check(Math.abs(gap - 8) <= 2, `${name}: the list is ${gap}px from its button`);
          await page.keyboard.press("Escape");
          await pop.waitFor({ state: "detached" });
          check(await btn.evaluate((e) => e === document.activeElement), `${name}: Escape didn't give the redrawn button the focus`);
        } else {
          check(false, `${name}: the redraw dropped the click`);
          await page.waitForTimeout(400);
        }

        // drawn again while it loads, then the new button clicked: taken back
        await press();
        await redraw();
        await press();
        await page.waitForTimeout(700);
        check(await pop.count() === 0, `${name}: a second click on the redrawn button didn't take it back`);
        await shut();
        check(!/\bopen\b/.test(await btn.getAttribute("class")), `${name}: the button stays marked open`);
      }

      // the state re-read a changed list's close queues lands while the
      // next list loads (agent-models.test.cjs: Escape, then Pick again)
      const pick = page.locator(BUTTONS["a connected row's Pick"]);
      fx.load = 0;
      await pressOn(pick);
      await pop.waitFor();
      await pop.locator(".am-mr", { hasText: "GLM-5.2" }).click();
      for (let i = 0; !fx.posts && i < 50; i++) await page.waitForTimeout(20);
      fx.load = 900;
      fx.stateWait = 350;
      await page.keyboard.press("Escape");
      await pop.waitFor({ state: "detached" });
      await pressOn(pick);
      check(await opened(), "the state re-read after a changed list dropped the click on Pick");
      assert.equal(fx.posts, 1);
      await shut();
      assert.deepEqual(failed, []);
      assert.deepEqual(errors, []);
    });
  }
}
