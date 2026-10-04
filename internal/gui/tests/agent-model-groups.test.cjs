// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group in an agent's model list shows its providers' icons
// stacked, as it does in the pickers and on the Routing page — not a maker's
// logo, nor the fan — and the names beside groups of 1 to 4 providers still
// start in one column.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "group/wide", name: "wide", group: "Routing groups", icons: ["openai", "anthropic", "deepseek-color", "xai"] },
  { id: "group/pair", name: "GPT-6 Sol", group: "Routing groups", icons: ["openai", "anthropic"] },
  { id: "group/solo", name: "solo", group: "Routing groups", icons: ["anthropic"] },
  { id: "group/bare", name: "bare", group: "Routing groups" },
  { id: "openai/gpt-6", name: "GPT-6", group: "OpenAI", icon: "openai", logo: "openai", context: 1e6, inUse: true },
];

async function serve(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({
    agents: [{
      id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
      fields: [{ key: "model", label: "model", value: "magpie/openai/gpt-6", options: [{ value: "magpie/openai/gpt-6", label: "GPT-6", ref: "openai/gpt-6" }] }],
      models: { shown: models.length, listed: models.length },
    }],
    profiles: [], settings: { lang: "en", theme: "light" },
  });
  if (url.pathname === "/api/agent-models/codex") return json({ models });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: routing groups in an agent's model list show their providers' icons`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve);
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.locator(".am-pop").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-model-groups.png`) }).catch(() => {});
      }
      await browser.close();
    });
    await page.goto("http://magpie.test/");
    await page.locator('.row.agent[data-id="codex"] .ag-link').click();
    await page.locator('.row.agent[data-id="codex"] .ag-exp .ag-chips .ag-quiet').click();
    const pop = page.locator(".am-pop:not(.leaving)");
    await pop.waitFor();
    const row = (name) => pop.locator(".am-mr", { hasText: name });

    const icons = (name) => row(name).evaluate((r) => {
      const lg = r.querySelector(".lg");
      return {
        stack: lg.classList.contains("ic-stack"),
        discs: [...lg.querySelectorAll(".disc > .ic")].map((e) => e.dataset.icon),
        more: lg.querySelector(".more")?.textContent || "",
        own: lg.dataset.icon || "",
        fan: !!lg.querySelector(":scope > svg"),
      };
    });
    assert.deepEqual(await icons("wide"), { stack: true, discs: ["openai", "anthropic", "deepseek-color"], more: "+1", own: "", fan: false });
    // named after a model, but routed over two providers: theirs, not OpenAI's logo alone
    assert.deepEqual(await icons("GPT-6 Sol"), { stack: true, discs: ["openai", "anthropic"], more: "", own: "", fan: false });
    assert.deepEqual(await icons("solo"), { stack: true, discs: ["anthropic"], more: "", own: "", fan: false });
    assert.deepEqual(await icons("bare"), { stack: false, discs: [], more: "", own: "", fan: true });
    // a model keeps its maker's logo
    assert.equal(await row("GPT-6").last().locator(".lg").getAttribute("data-icon"), "openai");

    // the names start in one column, clear of the widest stack, and the
    // stack sits inside its row
    const boxes = await pop.locator(".am-g.routes .am-mr").evaluateAll((rs) => rs.map((r) => {
      const b = r.getBoundingClientRect(), lg = r.querySelector(".lg").getBoundingClientRect(), n = r.querySelector(".n").getBoundingClientRect();
      // what's drawn, not the slot: the first disc, the icon, the fan
      const mark = (r.querySelector(".lg .disc, .lg > svg, .lg > img") || r.querySelector(".lg")).getBoundingClientRect();
      return { top: b.top, bottom: b.bottom, lgTop: lg.top, lgBottom: lg.bottom, lgRight: lg.right, n: n.left, mark: mark.left };
    }));
    assert.equal(boxes.length, 4);
    assert.equal(new Set(boxes.map((b) => Math.round(b.n))).size, 1, JSON.stringify(boxes));
    // and every group's icons start at the row's left, a lone one too
    assert(Math.max(...boxes.map((b) => b.mark)) - Math.min(...boxes.map((b) => b.mark)) <= 2, JSON.stringify(boxes));
    for (const b of boxes) {
      assert(b.lgRight <= b.n, JSON.stringify(b));
      assert(b.lgTop >= b.top && b.lgBottom <= b.bottom, JSON.stringify(b));
    }
    assert.deepEqual(errors, []);
  });
}
