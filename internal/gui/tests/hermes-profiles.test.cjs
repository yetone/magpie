// Run with Node's test runner and Playwright on the module path; see README.md.
// Hermes keeps more than one profile (hermes -p <name>), each with its own
// config.yaml, and magpie's Hermes row picked the model of one of them only
// (dongshanfengdongshizhcn on Discord). Each other profile comes as a field
// keyed "profile:<name>": connected, the opened row has an Other profiles
// section with one line per profile (its name, its model picker, the
// command that runs it), and no square of the row's own. Hermes is one row
// in the Agents list (geekbb on X), so this section is the only place a
// profile's model is picked; v0.1.4022 drew it nowhere and the profiles
// looked gone (Norethcy on X). A profile named in Chinese reads as its name
// and its command says its id. A click opens the app's model picker with
// Default first, noted as putting back what the profile had before magpie; a
// model picked is posted for that profile alone. With one profile only there
// is no section. No click scrolls the page. In every language, at a normal
// and a narrow width, in both engines. No backend: the API is faked here.
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
    ...bots.map(([b, label = b]) => ({ key: "profile:" + b, label, value: b === "coder" ? "kimi-k3.5" : "magpie/v/glm", quiet: true, options: [{ value: "kimi-k3.5" }, ...models] })),
  ],
});
const fresh = () => ({ agents: [hermes("hermes", [["a-profile-with-a-long-name"], ["coder"], ["u5c0f-u52a9-u624b", "小助手"]]), hermes("hermes-one", [])], profiles: [] });

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
      test(`${engine} ${lang} ${width}px: each Hermes profile picks its own model`, async (t) => {
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

        await page.locator(`${row} .ag-link`).click();
        await page.locator(`${row} .ag-exp`).waitFor();
        assert.ok((await page.locator(`${row} .ag-exp`).innerText()).includes(w.head));
        const keys = await page.locator(`${row} .ag-exp .field.ag-pick[data-key^="profile:"]`).evaluateAll((es) => es.map((e) => e.dataset.key));
        assert.deepEqual(keys, ["profile:a-profile-with-a-long-name", "profile:coder", "profile:u5c0f-u52a9-u624b"]);
        // a profile named in Chinese: its name, and the id hermes -p takes
        const zh = page.locator(`${row} .ag-exp .field.ag-pick[data-key="profile:u5c0f-u52a9-u624b"]`).locator("xpath=..");
        assert.equal(await zh.locator(".ag-tier").textContent(), "小助手");
        assert.equal(await zh.locator(".ag-hint").textContent(), "hermes -p u5c0f-u52a9-u624b");
        const pick = page.locator(`${row} .ag-exp .field.ag-pick[data-key="profile:coder"]`);
        const line = pick.locator("xpath=..");
        assert.equal(await line.locator(".ag-tier").textContent(), "coder");
        assert.equal(await line.locator(".ag-hint").textContent(), "hermes -p coder");
        assert.equal(await pick.locator(".v").textContent(), "kimi-k3.5");
        // a long name is cut short, not pushing its picker out
        const long = page.locator(`${row} .ag-exp .field.ag-pick[data-key="profile:a-profile-with-a-long-name"]`).locator("xpath=..").locator(".ag-tier");
        assert.ok(await long.evaluate((e) => e.scrollWidth > e.clientWidth && getComputedStyle(e).textOverflow === "ellipsis"));
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the Agents view overflows");

        // the app's model picker, Default first, noted as putting back the profile's own
        const y = await page.evaluate(() => scrollY);
        await pick.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.evaluate(() => scrollY), y, "the click scrolled the page");
        assert.equal(await page.locator("#pop").evaluate((e) => e.classList.contains("model-picker")), true);
        const first = await page.locator("#pop #list li").first().innerText();
        assert.ok(first.includes(w.def) && first.includes(w.note), first);
        await page.locator("#pop #list li", { hasText: "Flash" }).first().click();
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="hermes"] .ag-exp .field[data-key="profile:coder"] .v')?.textContent === "Flash");
        assert.deepEqual(sets, [{ agent: "hermes", field: "profile:coder", value: "magpie/v/flash" }]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
