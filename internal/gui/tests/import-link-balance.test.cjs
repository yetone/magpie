// A magpie://import link naming its balance query (balance=, balance_path=,
// balance_method=): the dialog shows where the balance will be asked, the
// dialog asks nothing of it before Add, and Add saves the query with the
// provider. All APIs and keys are fixtures.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.ASSET_DIR || path.resolve(__dirname, "../assets");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = ["en", "zh", "zh-TW", "ja", "de"];

const linked = {
  id: "bal-relay", name: "Bal Relay", chat: "https://bal-relay.example/v1", key: "sk-fixture-bal", models: ["gpt-6"],
  balanceURL: "https://bal-relay.example/api/usage/token/a-rather-long-path-for-a-narrow-window", balancePath: "data.total_available", balanceMethod: "POST",
};

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a link's balance query is shown, and saved with Add`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(7000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saves = [], foreign = [];
        page.on("pageerror", (e) => errors.push(e.stack || e.message));
        await page.route("**/*", async (route) => {
          const req = route.request(), url = new URL(req.url());
          const json = (data) => route.fulfill({ json: data });
          const state = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
          if (url.host !== "magpie.test") { foreign.push(req.url()); return route.abort(); }
          if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:true};` });
          if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
          if (url.pathname === "/api/providers") return json(state);
          if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
          if (url.pathname === "/api/import/bal1") return json({ provider: linked });
          if (url.pathname === "/api/provider/save") { saves.push(req.postDataJSON()); return json(state); }
          if (url.pathname === "/api/gateway/trace") return new Promise(() => {});
          if (url.pathname.startsWith("/api/")) return json({});
          const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
          const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
          try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
        });
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-import-link-balance.png`) });
          }
          await browser.close();
          assert.deepEqual(errors, []);
        });
        await page.goto("http://magpie.test/?view=providers&import=bal1");
        const ed = page.locator(".editor.import");
        await ed.waitFor();
        const bal = ed.locator("code.imp-bal");
        assert.equal(await bal.textContent(), `POST ${linked.balanceURL} data.total_available`);
        const label = await page.evaluate(() => t("Balance URL"));
        assert(await ed.getByText(label, { exact: true }).isVisible(), "labelled as the editor labels it");
        const fits = await ed.evaluate((e) => e.getBoundingClientRect().right <= innerWidth && e.scrollWidth <= e.clientWidth + 1);
        assert(fits, "the dialog fits the window");
        assert(await bal.evaluate((e) => e.getBoundingClientRect().right <= e.closest(".editor").getBoundingClientRect().right), "the URL wraps inside the dialog");
        await ed.locator(".bar button.primary").click();
        await page.waitForFunction(() => !importing);
        assert.equal(saves.length, 1);
        assert.deepEqual([saves[0].balanceURL, saves[0].balancePath, saves[0].balanceMethod], [linked.balanceURL, "data.total_available", "POST"]);
        assert.deepEqual(foreign, [], "nothing asked of another host");
      });
    }
  }
}
