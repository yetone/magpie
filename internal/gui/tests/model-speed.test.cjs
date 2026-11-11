// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's Fast and Ultrafast (kdajenks on X). Model settings gives a model
// that has a fast mode (canFast) a Standard speed / Fast / Ultrafast choice,
// Ultrafast only where an account offers it (canUltra) or the model is
// already set to it. A pick is staged as modelPrefs[id].tier, sent with the
// Save, put back by Escape-free reset (Restore default sends tier ""), and
// a model with no fast mode has none. In a group's editor the member's
// button goes Standard speed -> Fast -> Ultrafast -> Standard where the
// account offers Ultrafast, and saves its `ultra` list. Every language,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", catalog: "",
  models: [
    { id: "gpt-6.1-sol", name: "gpt-6.1-sol", on: true, context: 400000, output: 128000, canFast: true, canUltra: true },
    { id: "gpt-6.1-mini", name: "gpt-6.1-mini", on: true, context: 400000, output: 128000, canFast: true, tier: "priority" },
    { id: "glm-5", name: "GLM-5", on: true, context: 200000, output: 128000 },
  ],
  chosen: ["gpt-6.1-sol", "gpt-6.1-mini", "glm-5"], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…aaa" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
};
const gmodels = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", canFast: true, ultraOK: true },
  { id: "oa/gpt-6.1-mini", name: "gpt-6.1-mini", providerName: "OpenAI", icon: "generic", canFast: true },
];
const gmembers = ["oa/gpt-6.1-sol", "oa/gpt-6.1-mini"];
const groups = () => ({
  models: gmodels, pools: [],
  groups: [{ id: "sol", name: "Sol", members: gmembers, fast: gmembers, routing: "order", ready: true,
    memberInfo: gmembers.map((id) => ({ id, ready: true, fast: true, canFast: true, ultraOK: id.endsWith("sol") })) }],
});

function serve(lang, saves, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const list = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/provider/save") { saves.push(req.postDataJSON()); return json(list); }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) { posts.push({ path: url.pathname, body: JSON.parse(req.postData() || "{}") }); return json(groups()); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const say = (page, lang, s) => page.evaluate(([l, s]) => (l === "en" ? s : I18N[l][s]), [lang, s]);
const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1200, 420]) {
      test(`${engine} ${lang} ${width}px: a model's speed is set in Model settings`, async (t) => {
        const browser = await launch(engine);
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saves, []));
        await page.goto("http://magpie.test/?view=providers");
        const L = {};
        for (const k of ["Model settings", "Standard speed", "Fast", "Ultrafast", "Restore default"]) {
          L[k] = await say(page, lang, k);
          assert.ok(L[k], `${k} has its ${lang}`);
        }
        const tip = "Sent at this speed with every request for {id} from an agent with no fast pick of its own; where an account has no Ultrafast it goes fast";
        assert.ok(await say(page, lang, tip), `the tooltip has its ${lang}`);
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        const editor = page.locator("#modal:not([hidden]) .editor");
        await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        const names = editor.locator(".mnames:not([hidden])");
        const row = (id) => names.locator(".mname", { has: page.locator("code", { hasText: new RegExp(`^${id.replace(/\./g, "\\.")}$`) }) });
        const opt = (id, v) => row(id).locator(`.mspeed [data-seg="${v}"]`);
        const on = async (id) => row(id).locator(".mspeed .opt.on").getAttribute("data-seg");

        assert.equal(await row("glm-5").locator(".mspeed").count(), 0, "no fast mode, no choice");
        assert.equal(await opt("gpt-6.1-sol", "ultrafast").textContent(), L["Ultrafast"]);
        assert.equal(await opt("gpt-6.1-sol", "").textContent(), L["Standard speed"]);
        assert.equal(await opt("gpt-6.1-sol", "priority").textContent(), L["Fast"]);
        assert.equal(await opt("gpt-6.1-mini", "ultrafast").count(), 0, "no Ultrafast where no account offers it");
        assert.equal(await on("gpt-6.1-sol"), "");
        assert.equal(await on("gpt-6.1-mini"), "priority", "a saved one shows");
        assert.equal(await row("gpt-6.1-sol").locator(".mspeed").getAttribute("title"), (await say(page, lang, tip)).replace("{id}", "gpt-6.1-sol"));
        const [r, b] = [await row("gpt-6.1-sol").boundingBox(), await row("gpt-6.1-sol").locator(".mspeed").boundingBox()];
        assert.ok(b.x >= r.x - 0.5 && b.x + b.width <= r.x + r.width + 0.5, `the choice fits its row (${JSON.stringify([r, b])})`);

        await opt("gpt-6.1-sol", "ultrafast").click();
        assert.equal(await on("gpt-6.1-sol"), "ultrafast");
        assert.ok(await row("gpt-6.1-sol").locator(".munsaved").isVisible(), "the row says unsaved");
        assert.deepEqual(saves, [], "nothing is sent before the Save");
        // back to what it had: nothing to save
        await opt("gpt-6.1-mini", "").click();
        await opt("gpt-6.1-mini", "priority").click();
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-speed.png`) });
        }
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && !saves.length; i++) await page.waitForTimeout(20);
        assert.equal(saves.length, 1);
        assert.deepEqual(saves[0].modelPrefs, { "gpt-6.1-sol": { tier: "ultrafast" } });

        // Restore default sends the usual speed for the one that had its own
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        if (!(await editor.locator(".mnames:not([hidden])").count())) await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        await row("gpt-6.1-mini").getByRole("button", { name: L["Restore default"], exact: true }).click();
        assert.equal(await on("gpt-6.1-mini"), "");
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && saves.length < 2; i++) await page.waitForTimeout(20);
        assert.equal(saves.length, 2);
        assert.deepEqual(saves[1].modelPrefs, { "gpt-6.1-mini": { tier: "" } });
        assert.deepEqual(errors, []);
      });
    }
  }
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a group member goes Standard speed, Fast, Ultrafast`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, [], posts));
      await page.goto("http://magpie.test/?view=groups");
      const L = {};
      for (const k of ["Edit", "Save", "Fast", "Ultrafast", "Standard speed"]) { L[k] = await say(page, lang, k); assert.ok(L[k], `${k} has its ${lang}`); }
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();
      await card.locator("button", { hasText: L["Edit"] }).click();
      const ed = page.locator(".rt-gedit");
      const fast = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) }).locator("button.rt-fast");
      await fast("gpt-6.1-sol").waitFor();
      assert.equal(await fast("gpt-6.1-sol").textContent(), L["Fast"]);
      await fast("gpt-6.1-sol").click();
      assert.equal(await fast("gpt-6.1-sol").textContent(), L["Ultrafast"]);
      assert.equal(await fast("gpt-6.1-sol").getAttribute("aria-pressed"), "true");
      // a member whose account has no Ultrafast goes Fast -> Standard
      await fast("gpt-6.1-mini").click();
      assert.equal(await fast("gpt-6.1-mini").textContent(), L["Standard speed"]);
      await fast("gpt-6.1-mini").click();
      assert.equal(await fast("gpt-6.1-mini").textContent(), L["Fast"]);
      const b = ed.locator("button.primary", { hasText: L["Save"] });
      for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) { await page.mouse.wheel(0, 300); await page.waitForTimeout(150); }
      const bb = await b.boundingBox();
      await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.fast, gmembers);
      assert.deepEqual(sent.body.ultra, ["oa/gpt-6.1-sol"]);
      assert.deepEqual(errors, []);
    });
  }
}
