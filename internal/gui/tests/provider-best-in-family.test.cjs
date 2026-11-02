// Run with Node's test runner and Playwright on the module path; see README.md.
// Best in family is the models control's fourth option (Steven on
// Discord): with nothing picked, only the newest model of each family the
// vendor lists is served. The editor draws those (m.best, as
// /api/providers says) dashed and the older ones as left out, says so in
// its hint, saves it as bestInFamily with no picks and opens again on it.
// Vendor's list and Usage only turn it off, and Picked only from it starts
// from the newest ones. A provider saved before it opens on Vendor's list.
// In every language, Chromium and WebKit, at 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const newest = ["claude-opus-5-5", "claude-sonnet-5-5"], older = ["claude-opus-4-8", "claude-sonnet-4-5"];
const models = (on) => ["claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-4-8", "claude-sonnet-4-5"].map((id) => ({ id, name: id, on: on(id), best: newest.includes(id) }));
const base = {
  icon: "generic", responses: "", anthropic: "", catalog: "", chosen: [], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
// one saved before Best in family, and one saved with it
const plain = { ...base, id: "plain", name: "Plain", host: "plain.example.com", chat: "https://plain.example.com/v1", models: models(() => true) };
const best = { ...base, id: "best", name: "Best", host: "best.example.com", chat: "https://best.example.com/v1", bestInFamily: true, exposed: 2, models: models((id) => newest.includes(id)) };

function serve(lang, saves) {
  const providers = { providers: [plain, best], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/provider/save") { saves.push(req.postDataJSON()); return json(providers); }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// hint: the editor's hint under Best in family
const words = {
  en: { save: "Save", list: "Vendor's list", picked: "Picked only", best: "Best in family", usage: "Usage only", hint: "Best in family: agents see the newest model of each family" },
  zh: { save: "保存", list: "供应商列表", picked: "仅所选", best: "每系最新", usage: "仅看用量", hint: "每系最新：Agent 看到它列表里每个系列最新的模型" },
  "zh-TW": { save: "儲存", list: "供應商列表", picked: "僅所選", best: "每系最新", usage: "僅看用量", hint: "每系最新：Agent 看到它列表裡每個系列最新的模型" },
  ja: { save: "保存", list: "ベンダーのリスト", picked: "選択したもののみ", best: "系列の最新のみ", usage: "使用量のみ", hint: "系列の最新のみ：エージェントには一覧の各系列の最新モデル" },
  de: { save: "Speichern", list: "Liste des Anbieters", picked: "Nur gewählte", best: "Neuestes je Familie", usage: "Nur Nutzung", hint: "Neuestes je Familie: Agents sehen das neueste Modell jeder Familie" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Best in family serves the newest of each family`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-provider-best-in-family.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const saves = [];
        await page.route("**/*", serve(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        const editor = page.locator("#modal:not([hidden]) .editor");
        const chip = (id) => editor.locator(".mchips .mchip", { hasText: id });
        const cls = (id) => chip(id).evaluate((c) => [c.classList.contains("on"), c.classList.contains("auto")]);
        const serves = async () => {
          const seg = editor.locator(".mserves");
          const box = await seg.boundingBox(), ed = await editor.boundingBox();
          assert.ok(box && box.x >= ed.x - 1 && box.x + box.width <= ed.x + ed.width + 1, "the control fits the editor");
          // every option's label shows whole
          for (const o of await seg.locator(".opt").all()) assert.ok(await o.evaluate((b) => b.scrollWidth <= b.clientWidth + 1), "an option's label fits");
          return seg.locator(".opt.on").innerText();
        };
        const save = async () => {
          const n = saves.length;
          await editor.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
          for (let i = 0; i < 60 && saves.length === n; i++) await page.waitForTimeout(50);
          assert.equal(saves.length, n + 1, "saved");
          await page.locator("#modal:not([hidden]) .editor").waitFor({ state: "detached" }).catch(() => {});
          return saves[n];
        };

        // one saved before it opens on the vendor's list, every model dashed
        await page.locator(".row.provider", { hasText: "Plain" }).click();
        await chip("claude-opus-4-8").waitFor();
        assert.equal(await serves(), w.list);
        assert.equal(await editor.locator(".mserves .opt").count(), 4);
        for (const id of [...newest, ...older]) assert.deepEqual(await cls(id), [false, true], id + " on the vendor's list");
        // Best in family: the newest dashed, the older ones left out
        await editor.locator(".mserves .opt[data-serves=best]").click();
        assert.equal(await serves(), w.best);
        for (const id of newest) assert.deepEqual(await cls(id), [false, true], id + " served");
        for (const id of older) assert.deepEqual(await cls(id), [false, false], id + " left out");
        await editor.getByText(w.hint, { exact: false }).waitFor();
        if (process.env.ARTIFACT_DIR) await editor.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-provider-best-in-family-editor.png`) });
        let sent = await save();
        assert.equal(sent.bestInFamily, true, "bestInFamily sent");
        assert.equal(sent.noModels, false);
        assert.deepEqual(sent.models || [], []);

        // one saved with it opens on it; Picked only starts from the newest
        await page.locator(".row.provider", { hasText: "Best" }).click();
        await chip("claude-opus-4-8").waitFor();
        assert.equal(await serves(), w.best);
        for (const id of older) assert.deepEqual(await cls(id), [false, false], id + " left out");
        await editor.getByText(w.hint, { exact: false }).waitFor();
        await editor.locator(".mserves .opt[data-serves=picked]").click();
        assert.equal(await serves(), w.picked);
        // nothing picked yet: Picked only ticks nothing, rather than the
        // newest of each family the list serves
        for (const id of newest) assert.deepEqual(await cls(id), [false, false], id + " not picked yet");
        // the older ones aren't picked (Picked only folds unpicked ones away)
        for (const id of older) assert.equal(await editor.locator(".mchips .mchip.on", { hasText: id }).count(), 0, id + " not picked");
        // Usage only, then Vendor's list: neither keeps it
        await editor.locator(".mserves .opt[data-serves=none]").click();
        assert.equal(await serves(), w.usage);
        await editor.locator(".mserves .opt[data-serves=list]").click();
        assert.equal(await serves(), w.list);
        for (const id of [...newest, ...older]) assert.deepEqual(await cls(id), [false, true], id + " vendor's list again");
        assert.equal(await editor.getByText(w.hint, { exact: false }).count(), 0);
        sent = await save();
        assert.equal(sent.bestInFamily, false, "Vendor's list sends it off");
        assert.equal(sent.noModels, false);
        assert.deepEqual(errors, []);
      });
    }
  }
}
