// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's lead picks each subagent's model itself, and Codex's own config
// can't overrule it (willz on Discord), so magpie puts every Codex subagent
// on a model set in Codex's row: a square opening the app's model picker
// (no native select) with only a ChatGPT account's models, as a subagent's
// task is sealed for them, Default first. A model picked is posted and
// lights the square; Default posts "". The Routing page's story of a
// subagent's request says it was put on that model, or why it stayed on
// the one asked for. A click scrolls nothing, nothing has a coloured left
// border, and the row fits a narrow window, in every language. No backend:
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const own = [{ value: "gpt-5.5", label: "GPT-5.5", ref: "openai/gpt-5.5" }];
const chatgpt = [
  { value: "codex/gpt-6-astra", label: "GPT-6 Astra", note: "me@example.com · via magpie", group: "ChatGPT", ref: "codex/gpt-6-astra", icon: "openai" },
  { value: "codex/gpt-5.5", label: "GPT-5.5", note: "me@example.com · via magpie", group: "ChatGPT", ref: "codex/gpt-5.5", icon: "openai" },
];
const fresh = () => ({
  agents: [{
    id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
    fields: [
      { key: "model", label: "model", value: "gpt-5.5", options: own },
      { key: "subagent_model", label: "subagent model", value: "", options: chatgpt },
    ],
  },
  // no ChatGPT account in magpie: no square
  {
    id: "codex2", name: "Codex", path: "/test2/config.toml", icon: "codex-color", wired: false,
    fields: [{ key: "model", label: "model", value: "gpt-5.5", options: own }, { key: "subagent_model", label: "subagent model", value: "", options: [] }],
  }],
  profiles: [],
});

const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const seat = { id: "codex", provider: "codex", name: "ChatGPT", who: "me@example.com", kind: "account", agent: "codex", model: "gpt-6-astra" };
const routes = [
  { id: 2, seq: 2, time: at(0), agent: "codex", kind: "collab_spawn", model: "gpt-5.5", provider: "codex", subagent: { asked: "gpt-5.5", to: "codex/gpt-6-astra" },
    order: [seat], tries: [{ id: "codex", model: "gpt-6-astra", start: at(0), done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 1200 },
  { id: 1, seq: 1, time: at(1), agent: "codex", kind: "collab_spawn", model: "gpt-5.5", provider: "openai", subagent: { asked: "gpt-5.5", to: "codex/gpt-6-astra", kept: "no ChatGPT account on in magpie serves it" },
    order: [{ ...seat, provider: "openai", model: "gpt-5.5" }], tries: [{ id: "codex", model: "gpt-5.5", start: at(1), done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 1200 },
];

function server(lang, sets) {
  let cur = fresh();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...cur, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      return json({ ...cur, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the words as i18n.js has them
const W = {
  en: { unset: "subagent model: the model the lead asks for", def: "Default", set: "subagent model: GPT-6 Astra",
    moved: "Codex requested a subagent on gpt-5.5; magpie put it on codex/gpt-6-astra, the model set for its subagents.",
    kept: "Codex requested a subagent on gpt-5.5 and it stayed there: its subagents are set to codex/gpt-6-astra, but no ChatGPT account on in magpie serves it." },
  zh: { unset: "子代理模型：主代理请求的模型", def: "默认", set: "子代理模型：GPT-6 Astra",
    moved: "Codex 请求了使用 gpt-5.5 的子代理；magpie 将其改用 codex/gpt-6-astra，即为它的子代理设定的模型。",
    kept: "Codex 请求了使用 gpt-5.5 的子代理，并保留在该模型上：它的子代理设为 codex/gpt-6-astra，但magpie 中启用的 ChatGPT 账号都不提供该模型。" },
  "zh-TW": { unset: "子代理模型：主代理請求的模型", def: null, set: "子代理模型：GPT-6 Astra",
    moved: "Codex 請求了使用 gpt-5.5 的子代理；magpie 將其改用 codex/gpt-6-astra，即為它的子代理設定的模型。",
    kept: "Codex 請求了使用 gpt-5.5 的子代理，並保留在該模型上：它的子代理設為 codex/gpt-6-astra，但magpie 中啟用的 ChatGPT 帳號都不提供該模型。" },
  ja: { unset: "サブエージェントのモデル：リードがリクエストしたモデル", def: null, set: "サブエージェントのモデル：GPT-6 Astra",
    moved: "Codex が gpt-5.5 のサブエージェントをリクエストしました。magpie はサブエージェント用に設定された codex/gpt-6-astra に切り替えました。",
    kept: "Codex が gpt-5.5 のサブエージェントをリクエストし、そのモデルのままになりました。サブエージェントは codex/gpt-6-astra に設定されていますが、magpie で有効な ChatGPT アカウントはどれもこのモデルを提供していません。" },
  de: { unset: "Modell der Subagenten: das Modell, das der Haupt-Agent anfordert", def: null, set: "Modell der Subagenten: GPT-6 Astra",
    moved: "Codex hat einen Subagent mit gpt-5.5 angefordert; magpie hat ihn auf codex/gpt-6-astra gesetzt, das für seine Subagenten festgelegte Modell.",
    kept: "Codex hat einen Subagent mit gpt-5.5 angefordert, und er blieb dort: Seine Subagenten sind auf codex/gpt-6-astra gesetzt, aber kein eingeschaltetes ChatGPT-Konto in magpie bietet es an." },
};

const codex = '.row.agent[data-id="codex"]';
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(W);

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: Codex's subagent model is a square opening a ChatGPT account's models`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 760 } });
        page.setDefaultTimeout(5000);
        const errors = [], sets = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, sets));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-codex-subagent-model.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/");
        await page.locator(codex).waitFor();
        await page.locator(`${codex} .ag-link`).click();
        await page.locator(`${codex} .ag-exp`).waitFor();

        assert.equal(await page.locator('.row.agent[data-id="codex2"] .field[data-key="subagent_model"]').count(), 0, "a square with nothing to pick");
        const square = page.locator(`${codex} .extras-cell .field.extra[data-key="subagent_model"]`);
        assert.equal(await square.count(), 1);
        assert.equal(await page.locator(`${codex} .field:not(.extra)[data-key="subagent_model"]`).count(), 0, "a picker of its own");
        assert.equal(await square.getAttribute("aria-label"), w.unset);
        assert.equal(await square.evaluate((e) => e.classList.contains("set")), false);
        // the row fits: no page scrolls sideways, the square in the window
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
        const box = await square.boundingBox();
        assert(box && box.x >= 0 && box.x + box.width <= width, JSON.stringify(box));

        const y = await page.evaluate(() => scrollY);
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.evaluate(() => scrollY), y, "the click scrolled the page");
        assert.equal(await page.locator("#pop").evaluate((e) => e.classList.contains("model-picker")), true);
        assert.equal(await page.locator("select").count(), 0, "a native select");
        const items = await page.locator("#pop #list li").allInnerTexts();
        if (w.def) assert(items[0].includes(w.def), items[0]);
        assert(items.some((s) => s.includes("GPT-6 Astra")), items.join(" | "));
        assert(!items.some((s) => s.includes("openai/")), "only a ChatGPT account's models");
        const pop = await page.locator("#pop").boundingBox();
        assert(pop.x >= 0 && pop.x + pop.width <= width, JSON.stringify(pop));
        const left = await page.evaluate(() => [...document.querySelectorAll("#pop *, .extras-cell *")].filter((e) => {
          const s = getComputedStyle(e);
          return parseFloat(s.borderLeftWidth) > 1 && s.borderLeftWidth !== s.borderRightWidth;
        }).length);
        assert.equal(left, 0, "a coloured left border");

        await page.locator("#pop #list li", { hasText: "GPT-6 Astra" }).first().click();
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="codex"] .field.extra.set[data-key="subagent_model"]'));
        assert.deepEqual(sets, [{ agent: "codex", field: "subagent_model", value: "codex/gpt-6-astra" }]);
        assert.equal(await square.getAttribute("aria-label"), w.set + "\nme@example.com · via magpie");

        // Default: every subagent on the model asked for again
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li").first().click();
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex"] .field.extra.set[data-key="subagent_model"]'));
        assert.deepEqual(sets.at(-1), { agent: "codex", field: "subagent_model", value: "" });
        assert.equal(await page.evaluate(() => scrollY), y);

        // the Routing page's story: put on the model set, or why not
        // (each from the list afresh: a narrow window shows the story over it)
        for (const [i, said] of [[0, w.moved], [1, w.kept]]) {
          await page.goto("http://magpie.test/?view=routing");
          await page.locator(".rt-days .rt-day").nth(1).click();
          await page.locator(".rt-req").nth(1).waitFor();
          await page.locator(".rt-req").nth(i).evaluate((e) => e.click());
          await page.waitForTimeout(300);
          const story = await page.locator(".rt-steps").textContent();
          assert(story.includes(said), `${said}\nnot in\n${story}`);
          // nothing says ChatGPT served the model the lead asked for
          if (i === 0 && lang === "en") {
            assert(!story.includes("ChatGPT serves it"), story);
            const models = await page.locator(".rt-brief-path code").allTextContents();
            assert.deepEqual(models, ["gpt-5.5", "gpt-6-astra"], "the requested and actual subagent models are distinct");
            assert(story.includes("Only one account is enabled for this model."), story);
          }
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
