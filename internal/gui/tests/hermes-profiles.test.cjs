// Run with Node's test runner and Playwright on the module path; see README.md.
// The connected Hermes row has no control for its other profiles: each is an
// agent row of its own (hermes#<name>, hermes-profile-row.test.cjs), so two
// controls never pick one profile's model (Norethcy on X). The API still has
// the "profile:<name>" fields (dongshanfengdongshizhcn on Discord); the row
// draws none, no "Other profiles" section and no "hermes -p" line. In every
// language, at a normal and a narrow width, in both engines. No backend: the
// API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ value: "magpie/v/glm", label: "GLM", ref: "v/glm" }, { value: "magpie/v/flash", label: "Flash", ref: "v/flash" }];
const hermes = (id, bots) => ({
  id, name: "Hermes", path: "/test/.hermes/config.yaml", icon: "hermes", wired: true,
  fields: [
    { key: "model", label: "model", value: "magpie/v/glm", options: models },
    ...bots.map((b) => ({ key: "profile:" + b, label: b, value: b === "coder" ? "kimi-k3.5" : "magpie/v/glm", quiet: true, options: [{ value: "kimi-k3.5" }, ...models] })),
  ],
});
const fresh = () => ({ agents: [hermes("hermes", ["a-profile-with-a-long-name", "coder"]), hermes("hermes-one", [])], profiles: [] });

function server(lang, sets) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the words as i18n.js has them
const W = {
  en: { head: "Other profiles", def: "Default", note: "put back what Hermes had before magpie" },
  zh: { head: "其他 profile", def: "默认", note: "还原 Hermes 接入 magpie 之前的设置" },
  "zh-TW": { head: "其他 profile", def: "預設", note: "還原 Hermes 接入 magpie 之前的設定" },
  ja: { head: "ほかのプロファイル", def: "デフォルト", note: "Hermes を magpie 接続前の設定に戻す" },
  de: { head: "Weitere Profile", def: "Standard", note: "Stand von Hermes vor magpie wiederherstellen" },
};

const row = '.row.agent[data-id="hermes"]';
const one = '.row.agent[data-id="hermes-one"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: the Hermes row has no control for its other profiles`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 800 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], sets = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, sets));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-hermes-profiles.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/");
        await page.locator(row).waitFor();
        // no profile picker in the row itself
        assert.equal(await page.locator(`${row} > * .field[data-key^="profile:"]`).count(), 0);

        // one profile only: no section
        await page.locator(`${one} .ag-link`).click();
        await page.locator(`${one} .ag-exp`).waitFor();
        assert.equal(await page.locator(`${one} .ag-exp`).innerText().then((s) => s.includes(w.head)), false);
        assert.equal(await page.locator(`${one} .field[data-key^="profile:"]`).count(), 0);

        // the connected row has no profile controls either: each other profile
        // is its own row (hermes#<name>), where its model is picked
        await page.locator(`${row} .ag-link`).click();
        await page.locator(`${row} .ag-exp`).waitFor();
        assert.equal((await page.locator(`${row} .ag-exp`).innerText()).includes(w.head), false);
        assert.equal(await page.locator(`${row} .field[data-key^="profile:"]`).count(), 0);
        assert.equal((await page.locator(`${row} .ag-exp`).innerText()).includes("hermes -p"), false);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the Agents view overflows");
        assert.deepEqual(errors, []);
      });
    }
  }
}
