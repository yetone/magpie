// Run with Node's test runner and Playwright on the module path; see README.md.
// #5395 (Musikrv77852 on X): DeepSeek Harness compacts on the session's
// latest model, so switching away from a model whose provider is out of its
// limit failed on the compaction. Its row has a square for the model its
// compactions go on, in the app's own picker; the Routing story says why a
// compaction went there. qtwaiter on X: with nothing set, a compaction on
// a provider out of its limit goes to the same model from another
// provider, and the story says that too. All APIs are faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { value: "fake/m1", label: "M1", ref: "fake/m1", group: "Fake" },
  { value: "fake/m2", label: "M2", ref: "fake/m2", group: "Fake" },
];
const fresh = (wired = true) => ({
  agents: [{
    id: "dsh", name: "DeepSeek Harness", path: "/test/.dsh/profiles/web.json", icon: "deepseek-color", wired,
    fields: [
      { key: "model", label: "model", value: wired ? "magpie/fake/m1" : "", options: wired ? models.map((m) => ({ ...m, value: "magpie/" + m.value })) : [] },
      { key: "compact_model", label: "compaction model", value: "", options: wired ? models.map((m) => ({ ...m, value: "magpie/" + m.value })) : [] },
    ],
  }],
  profiles: [],
});

const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const seat = { id: "fake", provider: "fake", name: "Fake", who: "Test key", kind: "key", agent: "dsh", model: "m2" };
const routes = [
  { id: 1, seq: 1, time: at(0), agent: "dsh", model: "fake/m1", provider: "fake", kind: "compact", moved: { to: "fake/m2", why: "dsh-compact" },
    order: [seat], tries: [{ id: "fake", model: "m2", start: at(0), done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 1200 },
  { id: 2, seq: 2, time: at(1), agent: "dsh", model: "workbuddy/deepseek-v4.1-flash", provider: "trae-cn", kind: "compact", moved: { to: "trae-cn/deepseek-v4.1-flash", why: "dsh-compact-rested" },
    order: [{ ...seat, id: "trae-cn", provider: "trae-cn", name: "Trae CN", model: "deepseek-v4.1-flash" }], tries: [{ id: "trae-cn", model: "deepseek-v4.1-flash", start: at(1), done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 1200 },
];

function server(lang, fixture) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...fixture.state, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      fixture.sets.push(body);
      fixture.state = JSON.parse(JSON.stringify(fixture.state));
      fixture.state.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return json({ ...fixture.state, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return new Promise(() => {});
      return json({ mine: true, now: now.toISOString(), seq: 2, totals: { requests: 2, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/gateway/session-titles") return json({ titles: {} });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// Expected reader-facing words, independent of the translation lookup.
const W = {
  en: { unset: "compaction model: the session's model", def: "Default", note: "each compaction on the session's model", set: "compaction model: M2",
    moved: "DeepSeek Harness asked for fake/m1 to compact its conversation; magpie served it on fake/m2, the model set for its compaction.",
    rested: "DeepSeek Harness asked for workbuddy/deepseek-v4.1-flash to compact its conversation, and its provider is out of its limit; magpie served it on trae-cn/deepseek-v4.1-flash, the same model from another provider." },
  zh: { unset: "压缩模型：会话所用的模型", def: "默认", note: "每次压缩都用会话所用的模型", set: "压缩模型：M2",
    moved: "DeepSeek Harness 请求用 fake/m1 压缩它的对话；magpie 让 fake/m2 来处理，即为它的压缩设定的模型。",
    rested: "DeepSeek Harness 请求用 workbuddy/deepseek-v4.1-flash 压缩它的对话，而它的服务商已超出限额；magpie 让 trae-cn/deepseek-v4.1-flash 来处理，即另一家服务商的同一模型。" },
  "zh-TW": { unset: "壓縮模型：工作階段所用的模型", def: "預設", note: "每次壓縮都用工作階段所用的模型", set: "壓縮模型：M2",
    moved: "DeepSeek Harness 請求用 fake/m1 壓縮它的對話；magpie 讓 fake/m2 來處理，即為它的壓縮設定的模型。",
    rested: "DeepSeek Harness 請求用 workbuddy/deepseek-v4.1-flash 壓縮它的對話，而它的服務商已超出限額；magpie 讓 trae-cn/deepseek-v4.1-flash 來處理，即另一家服務商的同一模型。" },
  ja: { unset: "圧縮モデル：セッションのモデル", def: "デフォルト", note: "圧縮はいつもセッションのモデルで行う", set: "圧縮モデル：M2",
    moved: "DeepSeek Harness は会話の圧縮に fake/m1 をリクエストしました。magpie は fake/m2（圧縮用に設定されたモデル）で応答しました。",
    rested: "DeepSeek Harness は会話の圧縮に workbuddy/deepseek-v4.1-flash をリクエストしましたが、そのプロバイダーは上限に達しています。magpie は trae-cn/deepseek-v4.1-flash（別のプロバイダーの同じモデル）で応答しました。" },
  de: { unset: "Komprimierungsmodell: das Modell der Sitzung", def: "Standard", note: "jede Komprimierung auf dem Modell der Sitzung", set: "Komprimierungsmodell: M2",
    moved: "DeepSeek Harness hat fake/m1 angefordert, um seine Unterhaltung zu komprimieren; magpie hat sie mit fake/m2 beantwortet, dem dafür eingestellten Modell.",
    rested: "DeepSeek Harness hat workbuddy/deepseek-v4.1-flash angefordert, um seine Unterhaltung zu komprimieren, doch sein Anbieter ist über seinem Limit; magpie hat sie mit trae-cn/deepseek-v4.1-flash beantwortet, demselben Modell eines anderen Anbieters." },
};

const row = '.row.agent[data-id="dsh"]';
const written = (page) => page.waitForResponse((r) => new URL(r.url()).pathname === "/api/set");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(W);

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: DeepSeek Harness's compaction square and routing story`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 760 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], fixture = { state: fresh(), sets: [] };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, fixture));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-dsh-compact-model.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/");
        await page.locator(row).waitFor();
        await page.locator(`${row} .ag-link`).click();
        await page.locator(`${row} .ag-exp`).waitFor();

        const square = page.locator(`${row} .ag-exp .field.extra[data-key="compact_model"]`);
        assert.equal(await square.count(), 1, "the opened row has one compaction square");
        assert.equal(await page.locator(`${row} .field:not(.extra)[data-key="compact_model"]`).count(), 0, "no plain picker");
        assert.equal(await square.getAttribute("title"), w.unset, "translated unset title");
        assert.equal(await square.getAttribute("aria-label"), w.unset);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        const box = await square.boundingBox();
        assert(box && box.x >= 0 && box.x + box.width <= width, JSON.stringify(box));

        const y = await page.evaluate(() => scrollY);
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.locator("#pop").evaluate((e) => e.classList.contains("model-picker")), true, "app's own model picker");
        assert.equal(await page.locator("select").count(), 0, "no native select");
        const first = await page.locator("#pop #list li").first().evaluate((e) => [e.querySelector(".v")?.textContent, e.querySelector(".n")?.textContent]);
        assert.deepEqual(first, [w.def, w.note], "translated Default first");
        const pop = await page.locator("#pop").boundingBox();
        assert(pop && pop.x >= 0 && pop.x + pop.width <= width, JSON.stringify(pop));

        let wrote = written(page);
        await page.locator("#pop #list li", { hasText: "M2" }).first().click();
        await wrote;
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="dsh"] .field.extra.set[data-key="compact_model"]'));
        assert.deepEqual(fixture.sets, [{ agent: "dsh", field: "compact_model", value: "magpie/fake/m2" }], "the pick posts the model as the model field spells it");
        assert.equal(await square.getAttribute("title"), w.set);
        assert.equal(await page.evaluate(() => scrollY), y, "picking does not scroll");

        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        wrote = written(page);
        await page.locator("#pop #list li").first().click();
        await wrote;
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="dsh"] .field.extra.set[data-key="compact_model"]'));
        assert.deepEqual(fixture.sets.at(-1), { agent: "dsh", field: "compact_model", value: "" }, "Default posts an empty value");
        assert.equal(await square.getAttribute("title"), w.unset);

        fixture.state = fresh(false);
        await page.goto("http://magpie.test/");
        await page.locator(row).waitFor();
        assert.equal(await page.locator(`${row} .field[data-key="compact_model"]`).count(), 0, "off magpie's route, no square");

        fixture.state = fresh();
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-req").nth(1).waitFor();
        for (const why of [w.moved, w.rested]) {
          const titles = await page.locator(".rt-req").evaluateAll((es) => es.map((e) => e.getAttribute("title") || ""));
          const i = titles.findIndex((x) => x.includes("\n" + why));
          assert(i >= 0, `no row's title says\n${why}\nin\n${titles.join("\n--\n")}`);
          await page.locator(".rt-req").nth(i).evaluate((e) => e.click());
          await page.waitForFunction((n) => document.querySelectorAll(".rt-req")[n]?.getAttribute("aria-pressed") === "true", i);
          const story = await page.locator(".rt-steps").textContent();
          assert.equal(story.split(why).length - 1, 1, `${why}\nonce in\n${story}`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
