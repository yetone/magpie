// Run with Node's test runner and Playwright on the module path; see README.md.
// A model disabled in Settings → Models (#1867) says so in its provider's
// editor: its chip is dimmed with a "disabled" badge and its title names the
// pattern, where it read as an ordinary pick the gateway then refused. The
// API names the pattern (TestProviderInfoSaysADisabledModel). Both engines,
// every language; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const L = { en: "disabled", zh: "已停用", "zh-TW": "已停用", ja: "無効", de: "deaktiviert" };

function server(lang) {
  const settings = { lang, theme: "light", disabledModels: ["*FABLE*", "relay/exact-one", "tail"] };
  const relay = { id: "relay", name: "Relay", icon: "generic", host: "relay.test", chat: "https://relay.test/v1", responses: "", anthropic: "",
    models: [["claude-fable-5", "*FABLE*"], ["exact-one", "relay/exact-one"], ["vendor/tail", "tail"], ["kept-model", ""]].map(([id, disabled]) => ({ id, name: id, on: true, ...(disabled && { disabled }) })),
    agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a disabled model says so in its provider's editor", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) await t.test(lang, async () => {
      const page = await browser.newPage({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" });
      t.after(() => page.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#providers .row.provider[data-id="relay"]').click();
      await page.locator("#modal .mchips .mchip").first().waitFor();
      const chips = await page.locator("#modal .mchips .mchip.pick").evaluateAll((cs) => cs.map((c) => ({
        id: c.firstChild.textContent, badge: [...c.querySelectorAll(".badge")].map((b) => b.textContent), title: c.title, dim: c.classList.contains("noapi") })));
      const by = Object.fromEntries(chips.map((c) => [c.id, c]));
      for (const [id, pat] of [["claude-fable-5", "*FABLE*"], ["exact-one", "relay/exact-one"], ["vendor/tail", "tail"]]) {
        assert.deepEqual(by[id].badge, [L[lang]], `${id} badge`);
        assert.ok(by[id].dim, `${id} dimmed`);
        assert.ok(by[id].title.includes("(" + pat + ")") || by[id].title.includes("（" + pat + "）"), `${id} title names ${pat}: ${by[id].title}`);
      }
      assert.deepEqual(by["kept-model"].badge, [], "kept-model has no badge");
      assert.equal(by["kept-model"].dim, false);
      assert.deepEqual(errors, []);
    });
  });
}
