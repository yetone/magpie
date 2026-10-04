// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's model list that takes a while to come (有时候这个关不掉): a
// second click on the line while it loads takes it back, a click elsewhere
// too, and however many clicks there were, one box at most is up and it
// closes. Its rows sit indented under their group (左侧有缩进才对).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const list = [
  { id: "openai/gpt-5.5", name: "GPT-5.5", group: "OpenAI", icon: "openai", context: 272e3 },
  { id: "openai/gpt-5.4", name: "GPT-5.4", group: "OpenAI", icon: "openai", context: 272e3 },
  { id: "zai/glm-5.2", name: "GLM-5.2", group: "Z.ai", icon: "zai", context: 1e6 },
];

async function serve(route) {
  const req = route.request();
  const url = new URL(req.url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({
    agents: [{
      id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
      fields: [{ key: "model", label: "model", value: "magpie/openai/gpt-5.5", options: [{ value: "magpie/openai/gpt-5.5", label: "GPT-5.5", ref: "openai/gpt-5.5" }] }],
      models: { shown: 3, listed: 3 },
    }],
    profiles: [], settings: { lang: "en", theme: "light" },
  });
  if (url.pathname === "/api/agent-models/codex") {
    await new Promise((r) => setTimeout(r, 400)); // a long list, slow to put together
    return json({ models: list });
  }
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: an agent's model list that loads slowly`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await browser.newPage({ viewport: { width: 1000, height: 700 } });
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve);
    t.after(() => browser.close());
    await page.goto("http://magpie.test/");
    // the list opens from Pick in the connected row, opened
    await page.locator('.row.agent[data-id="codex"] .ag-link').click();
    const entry = page.locator('.row.agent[data-id="codex"] .ag-exp .ag-chips .ag-quiet');
    await entry.waitFor();
    const boxes = () => page.locator(".am-pop:not(.leaving)").count();

    // two clicks while it loads: open, then taken back — no box
    await entry.click();
    await entry.click();
    await page.waitForTimeout(1000);
    assert.equal(await boxes(), 0, "the second click takes it back");

    // three: one box, and it closes
    await entry.click();
    await entry.click();
    await entry.click();
    await page.waitForTimeout(1000);
    assert.equal(await boxes(), 1, "one box at most");
    await page.mouse.click(900, 650);
    await page.waitForTimeout(500);
    assert.equal(await boxes(), 0, "a click elsewhere closes it");

    // a click elsewhere while it loads: it doesn't come up
    await entry.click();
    await page.mouse.click(900, 650);
    await page.waitForTimeout(1000);
    assert.equal(await boxes(), 0, "a click away while it loads takes it back");

    // the rows sit under their group: a model's icon in the group icon's column
    await entry.click();
    const pop = page.locator(".am-pop:not(.leaving)");
    await pop.waitFor();
    const gIcon = await pop.locator(".am-g").first().locator(".am-fold > .ic").boundingBox();
    const rIcon = await pop.locator(".am-g").first().locator(".am-mr .lg").first().boundingBox();
    const gName = await pop.locator(".am-g").first().locator(".am-fold .gn").boundingBox();
    assert(Math.abs(rIcon.x - gIcon.x) <= 2, `row icon at ${rIcon.x}, group icon at ${gIcon.x}`);
    assert(rIcon.x > gName.x - 30, "indented past the twisty");
    await page.keyboard.press("Escape");
    await pop.waitFor({ state: "detached" });
    assert.deepEqual(errors, []);
  });
}
