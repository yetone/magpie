// Run with Node's test runner and Playwright on the module path; see README.md.
// abledo on Discord: "为什么claudecode的magpie模型选不了？其他的agent可以选".
// Claude Code not yet connected, with a relay of the user's own as its
// ANTHROPIC_BASE_URL: its picker opened on a long list of Claude models it
// asks the relay for itself, under "Claude Code · <host>", and every magpie
// model sat below it, out of sight. magpie's models now come first and the
// agent's own direct ones after them, connected or not, the current model
// still at the top. In five languages, chromium and webkit, at 1100 and 420
// wide. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const host = "api.somerelay.example";
const own = (value, note) => ({ value, note, icon: "claude-color", group: "Claude Code · " + host, direct: host });
// in the backend's order: Claude Code's own, then magpie's
const options = [
  own("claude-opus-5-5", "Claude Opus 5.5"),
  own("claude-haiku-5-5", "Claude Haiku 5.5"),
  own("claude-sonnet-5-5", "Claude Sonnet 5.5"),
  own("claude-fable-5-1", "Claude Fable 5.1"),
  own("claude-opus-5", "Claude Opus 5"),
  own("claude-sonnet-5", "Claude Sonnet 5"),
  own("claude-fable-5", "Claude Fable 5"),
  own("claude-opus-4-8", "Claude Opus 4.8"),
  own("claude-sonnet-4-8", "Claude Sonnet 4.8"),
  own("claude-opus-4-5", "Claude Opus 4.5"),
  own("claude-sonnet-4-5", "Claude Sonnet 4.5"),
  own("claude-haiku-4-5", "Claude Haiku 4.5"),
  { value: "zhipu/glm-4.6", label: "GLM-4.6", note: "Zhipu · via magpie", icon: "zhipu-color", group: "Zhipu", ref: "zhipu/glm-4.6" },
  { value: "deepseek/deepseek-v4", label: "DeepSeek V4", note: "DeepSeek · via magpie", icon: "deepseek-color", group: "DeepSeek", ref: "deepseek/deepseek-v4" },
];
const state = (value, wired) => ({
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", path: "/test/settings.json", wired,
    fields: [{ key: "model", label: "model", value, options }] }],
  profiles: [],
});

function server(lang, value, wired, sets) {
  let cur = state(value, wired);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light", guideHidden: true } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = state(body.value, true);
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light", guideHidden: true } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = '.row.agent[data-id="claude"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Claude Code's picker shows magpie's models first", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const width of [1100, 420]) {
      for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
        await t.test(`${lang} at ${width}`, async () => {
          for (const wired of [false, true]) {
            const sets = [];
            const context = await browser.newContext({ viewport: { width, height: 760 } });
            const page = await context.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, "claude-opus-5-5", wired, sets));
            await page.goto("http://magpie.test/");
            await page.locator(row).waitFor();
            await page.locator(`${row} .field[data-key="model"]`).first().click();
            await page.locator("#pop:not([hidden]) #list li").first().waitFor();
            await page.waitForTimeout(400); // the picker grows open
            const groups = await page.locator("#list li.group").allTextContents();
            const at = (g) => groups.findIndex((x) => x.toLowerCase() === g.toLowerCase());
            assert(at("Zhipu") >= 0 && at("DeepSeek") > at("Zhipu"), `${wired}: ${groups.join(" | ")}`);
            assert(at("Claude Code · " + host) > at("DeepSeek"), `${wired}: the agent's own come last: ${groups.join(" | ")}`);
            // the current one still heads the list, and magpie's first one
            // is in sight without a scroll
            const names = await page.locator("#list li:not(.group) .v").allTextContents();
            const cur = names.indexOf("claude-opus-5-5");
            assert(cur >= 0 && cur < names.indexOf("GLM-4.6") && names.lastIndexOf("claude-opus-5-5") === cur, `${wired}: ${names.join(" | ")}`);
            const glm = page.locator("#list li:not(.group)", { hasText: "GLM-4.6" });
            const box = await glm.boundingBox(), list = await page.locator("#list").boundingBox();
            assert(box && box.y + box.height <= list.y + list.height + 1, `${wired}: GLM-4.6 is below the fold at ${JSON.stringify(box)} in ${JSON.stringify(list)}`);
            await glm.click();
            await page.waitForFunction(() => document.querySelector("#pop").hidden);
            assert.deepEqual(sets, [{ agent: "claude", field: "model", value: "zhipu/glm-4.6" }]);
            await context.close();
          }
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
