// Run with Node's test runner and Playwright on the module path; see README.md.
// A relay's model picked past the ones its list serves joins them (onkashiko
// on X: 点开一个供应商新选模型的时候，之前已经选中的模型就会失效，对于有大量
// 模型的中转供应商来说很不方便，新选的模型难道不应该是新增进模型列表吗). A relay
// listing 60 models serves the first 24 (dashed) with none picked; a click
// on the 30th picked it alone, so Save wrote that one and agents lost the
// 24 they had. Now the 24 stay and it joins them, as the TUI's toggle does,
// and so does an id added by hand, by Add model or left in the box at Save.
// A dashed one clicked is still picked alone (#681), and Best in family's
// newest ones are kept the same way. In every language, Chromium and
// WebKit, at 1000 and 420.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
// a relay's list, as such lists run: many vendors' models side by side
const families = ["gpt-5.5", "claude-opus-5-5", "gemini-3.5-pro", "deepseek-v4", "glm-5.3", "qwen4-max", "kimi-k3", "grok-5", "doubao-seed-2.5", "minimax-m3"];
const ids = Array.from({ length: 60 }, (_, i) => families[i % 10] + (i < 10 ? "" : "-" + String.fromCharCode(97 + Math.floor(i / 10) - 1)));
const first24 = ids.slice(0, 24);
// Best in family: one of each family
const best = ids.slice(0, 10);

function relay(chosen, bestOn) {
  const on = new Set(chosen.length ? chosen : bestOn ? best : first24);
  return {
    id: "relay", name: "Relay", icon: "generic", host: "relay.example.com", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
    models: ids.map((id) => ({ id, name: id, on: on.has(id), best: best.includes(id) })), chosen, bestInFamily: !!bestOn, fetched, agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
  };
}

function serve(lang, saves, at) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    const providers = () => ({ providers: [relay(at.chosen, at.best)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/save") {
      const body = req.postDataJSON();
      saves.push(body.models);
      at.chosen = body.models;
      at.best = body.bestInFamily;
      return json(providers());
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { save: "Save", add: "Add model", hint: "Click a dashed one to pick just it." },
  zh: { save: "保存", add: "添加模型", hint: "点击虚线框的模型可单选，点击其他模型会把它加进来。" },
  "zh-TW": { save: "儲存", add: "新增模型", hint: "點選虛線框的模型可單選，點選其他模型會把它加進來。" },
  ja: { save: "保存", add: "モデルを追加", hint: "破線のモデルをクリックするとそれだけを選択し、ほかのモデルをクリックするとそれらに追加します。" },
  de: { save: "Speichern", add: "Modell hinzufügen", hint: "Klicken Sie auf ein gestricheltes Modell, um nur dieses zu wählen, auf ein anderes, um es hinzuzufügen." },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}: a model past the served ones joins them`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saves = [], at = { chosen: [], best: false };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saves, at));
        await page.goto("http://magpie.test/?view=providers");
        const editor = page.locator("#modal:not([hidden]) .editor");
        const open = async () => {
          await page.locator(".row.provider", { hasText: "Relay" }).first().click();
          await editor.locator(".mchips .mchip").first().waitFor();
        };
        const chip = (id) => editor.locator(".mchips .mchip", { has: page.locator(`span:text-is("${id}")`) });
        const picked = () => editor.locator(".mchips .mchip.on").evaluateAll((cs) => cs.map((c) => c.firstChild.textContent));
        const save = async () => {
          await editor.getByRole("button", { name: w.save, exact: true }).click();
          await editor.waitFor({ state: "detached" });
        };
        const seg = (v) => editor.locator(`.mserves .opt[data-serves=${v}]`);
        // the saved provider set afresh, and the page loaded again to read it
        const again = async (chosen, best) => {
          Object.assign(at, { chosen, best });
          await page.reload();
          await open();
        };

        // the reporter's case: the 30th clicked joins the 24 served
        await open();
        assert.equal(await editor.locator(".mchips .mchip.auto").count(), 24);
        await editor.getByText(w.hint).waitFor();
        await chip(ids[29]).click();
        assert.equal(await seg("picked").getAttribute("aria-pressed"), "true");
        assert.deepEqual((await picked()).sort(), [...first24, ids[29]].sort());
        // every model stays in sight to go on picking
        assert.equal(await editor.locator(".mchips .mchip").count(), 60);
        await chip(ids[40]).click();
        await save();
        assert.deepEqual(saves.at(-1), [...first24, ids[29], ids[40]]);

        // saved picks: one more joins them
        await open();
        assert.equal(await seg("picked").getAttribute("aria-pressed"), "true");
        await editor.locator(".mchips .mchips-rest").click();
        await chip(ids[50]).click();
        await save();
        assert.deepEqual(saves.at(-1), [...first24, ids[29], ids[40], ids[50]]);

        // a dashed one, with none picked, is still picked alone (#681)
        await again([], false);
        await chip(ids[3]).click();
        assert.deepEqual(await picked(), [ids[3]]);
        await save();
        assert.deepEqual(saves.at(-1), [ids[3]]);

        // an id added by hand joins the 24, by Add model and by Save
        for (const how of ["add", "save"]) {
          await again([], false);
          const box = editor.locator(".mfoot input");
          await box.fill("my-own-model");
          if (how === "add") {
            await editor.getByRole("button", { name: w.add, exact: true }).click();
            assert.equal(await picked().then((l) => l.length), 25);
          }
          await save();
          assert.deepEqual(saves.at(-1), [...first24, "my-own-model"], how);
        }

        // Best in family: the newest of each kept, the one clicked beside them
        await again([], true);
        assert.equal(await seg("best").getAttribute("aria-pressed"), "true");
        assert.equal(await editor.locator(".mchips .mchip.auto").count(), 10);
        await chip(ids[33]).click();
        await save();
        assert.deepEqual(saves.at(-1), [...best, ids[33]]);

        // Usage only: nothing served, so a click picks just it
        await again([], false);
        await seg("none").click();
        await chip(ids[33]).click();
        assert.deepEqual(await picked(), [ids[33]]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
