// Run with Node's test runner and Playwright on the module path; see README.md.
// #1334 (laacmilan): with Codex's CLI and its desktop app both installed, the
// Agents page showed only the CLI's version. Now Codex's row has both, each
// named ("CLI 0.130.0", "App 26.930.31730"), the update pill still beside
// the CLI's; with the app alone, the app's; an agent with no app is as it
// was. At the window's narrow width the name keeps its room. No backend: the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const agent = (id, name) => ({ id, name, path: "~/." + id + "/config.toml", fields: [{ key: "model", label: "model", value: "model-a", options: models }] });
const AGENTS = [agent("codex", "Codex"), agent("gemini", "Gemini CLI")];

function server(lang, clis) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: AGENTS, profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/agents/cli" && req.method() === "GET") return route.fulfill({ json: { agents: clis, pending: false } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const row = (id) => `.row.agent[data-id="${id}"]`;
const BOTH = {
  codex: { version: "0.130.0", latest: "0.131.0", via: "npm", command: "npm install -g @openai/codex@latest", update: true, app: "26.930.31730" },
  gemini: { version: "0.60.0", latest: "0.60.0", via: "npm", command: "npm install -g @google/gemini-cli@latest" },
};
const APP_ONLY = { codex: { app: "26.930.31730" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Codex's row shows its app's version beside its CLI's", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, clis, width = 980) => {
      const page = await (await browser.newContext({ viewport: { width, height: 640 } })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, clis));
      await page.goto("http://magpie.test/");
      await page.locator(`${row("codex")} .ag-ver`).first().waitFor();
      return page;
    };
    const vers = (page, id) => page.locator(`${row(id)} .ag-ver`).allTextContents();

    for (const [lang, cli, app, tip] of [
      ["en", "CLI 0.130.0", "App 26.930.31730", /The Codex app 26\.930\.31730\. It reads the same settings as the CLI: connecting Codex here connects both/],
      ["zh", "CLI 0.130.0", "App 26.930.31730", /Codex App 26\.930\.31730。它和 CLI 读同一份设置：在这里接入 Codex，两者都会接入/],
      ["ja", "CLI 0.130.0", "アプリ 26.930.31730", /Codex アプリ 26\.930\.31730。CLI と同じ設定を読むので/],
      ["de", "CLI 0.130.0", "App 26.930.31730", /Codex-App 26\.930\.31730\. Sie liest dieselben Einstellungen wie die CLI/],
    ]) {
      await t.test(lang + ": both, each named, the pill beside the CLI's", async () => {
        const page = await open(lang, BOTH);
        assert.deepEqual(await vers(page, "codex"), [cli, app]);
        assert.match(await page.locator(`${row("codex")} .ag-app`).getAttribute("title"), tip);
        // the pill comes after the CLI's version, before the app's
        const order = await page.locator(`${row("codex")} .ag-cli > *`).evaluateAll((xs) => xs.map((x) => x.classList.contains("ag-app") ? "app" : x.classList.contains("ag-up") ? "pill" : "cli"));
        assert.deepEqual(order, ["cli", "pill", "app"]);
        // an agent with no app: its version alone, as before
        assert.deepEqual(await vers(page, "gemini"), ["0.60.0"]);
        // name, versions and pill on one line
        const mids = await page.evaluate((r) => [".name", ".ag-ver", ".ag-up", ".ag-app"].map((s) => {
          const b = document.querySelector(r + " " + s).getBoundingClientRect();
          return b.top + b.height / 2;
        }), row("codex"));
        assert(Math.max(...mids) - Math.min(...mids) <= 2, `not on one line: ${mids}`);
        await page.close();
      });
    }

    await t.test("the app alone: its version, no pill", async () => {
      const page = await open("en", APP_ONLY);
      assert.deepEqual(await vers(page, "codex"), ["App 26.930.31730"]);
      assert.equal(await page.locator(`${row("codex")} .ag-up`).count(), 0);
      assert.equal(await page.locator(`${row("gemini")} .ag-ver`).count(), 0);
      await page.close();
    });

    for (const width of [560, 440]) {
      await t.test(`at ${width}px the name keeps its room and nothing scrolls sideways`, async () => {
        const page = await open("de", BOTH, width);
        const name = await page.locator(`${row("codex")} .name`).evaluate((n) => n.scrollWidth <= n.clientWidth + 1);
        assert(name, "Codex's name is cut");
        // the app's version is never cut mid-word by the row: it ends in an
        // ellipsis within the line, or gives way altogether
        const app = await page.locator(`${row("codex")} .ag-app`).evaluate((a) => {
          const box = a.closest(".ag-cli").getBoundingClientRect(), r = a.getBoundingClientRect();
          return { shown: getComputedStyle(a).display !== "none", inside: r.right <= box.right + 1, ellipsis: getComputedStyle(a).textOverflow === "ellipsis" };
        });
        assert(!app.shown || (app.inside && app.ellipsis), `the app's version is cut: ${JSON.stringify(app)}`);
        const side = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert(side <= 0, `the page scrolls ${side}px sideways`);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-app-version-${width}.png`) });
        }
        await page.close();
      });
    }
  });
}
