// Run with Node's test runner and Playwright on the module path; see README.md.
// Codex's lead picks each subagent's model itself, and Codex's own config
// can't overrule it (willz on Discord), so magpie puts every Codex subagent
// on a model set in Codex's settings, a named row. Codex's card has no
// square for it beside the row (the same setting twice, the owner, 10-25);
// a Codex in WSL, which those settings don't read, keeps it on its row: a
// square opening the app's model picker
// (no native select) with magpie's models, another provider's too (#1633),
// "Same as the lead" first. It is the one square for the subagents' model:
// Codex's own subagents square, which read the same (the owner), isn't
// drawn, and a model left in Codex's config by it is said on this one and
// lights it till a pick here takes it away. Each title says plainly what
// the choice does. A model picked is posted and lights the square; Same as
// the lead posts "". The row in Codex's sheet picks the same way. The Routing page's story of a subagent's request says it was
// put on that model, or why it stayed on the one asked for (a task sealed
// for ChatGPT accounts, the model set another provider's). A click scrolls nothing, nothing has a coloured left
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
  // #1633: another provider's model, a lead on it wanting its subagents there too
  { value: "token/gpt-6.1-sol", label: "GPT-6.1 Sol", note: "Token · via magpie", group: "Token", ref: "token/gpt-6.1-sol" },
];
const fresh = () => ({
  agents: [{
    id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
    fields: [
      { key: "model", label: "model", value: "gpt-5.5", options: own },
      { key: "subagent", label: "subagents", value: "gpt-5.4-mini", options: own },
      { key: "subagent_model", label: "subagent model", value: "", options: chatgpt },
    ],
  },
  {
    id: "codex@wsl:Ubuntu-24.04", name: "Codex", path: "/wsl/config.toml", icon: "codex-color", wired: true,
    fields: [
      { key: "model", label: "model", value: "gpt-5.5", options: own },
      { key: "subagent", label: "subagents", value: "gpt-5.4-mini", options: own },
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
  // #1633: set to another provider's model, a task its lead sealed stays put
  { id: 0, seq: 0, time: at(2), agent: "codex", kind: "collab_spawn", model: "gpt-5.5", provider: "openai", subagent: { asked: "gpt-5.5", to: "token/gpt-6.1-sol", kept: "its task was sealed by the ChatGPT backend that answered its lead, and only a ChatGPT account can open it" },
    order: [{ ...seat, provider: "openai", model: "gpt-5.5" }], tries: [{ id: "codex", model: "gpt-5.5", start: at(2), done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 1200 },
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
      const fields = cur.agents.find((a) => a.id === body.agent).fields;
      fields.find((f) => f.key === body.field).value = body.value;
      // as the backend does: a pick here takes Codex's own away
      if (body.field === "subagent_model") fields.find((f) => f.key === "subagent").value = "";
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
  en: { unset: "subagent model: Lead's pick", line: "The lead picks each subagent's model, one of Codex's own too; one it names none for runs on the lead's model (GPT-5.5). A resumed subagent stays on the model it started on. Same as the lead, or a model picked here, holds every subagent to it, even when the lead names another.",
    old: "Codex's config still starts subagents on gpt-5.4-mini when the lead names none; picking here replaces it.",
    def: "Lead's pick", note: "GPT-5.5 unless the lead names another", set: "subagent model: GPT-6 Astra", setLine: "Every subagent runs on this model, even when the lead names another. Lead's pick lets the lead choose again.", row: "Codex subagent model",
    moved: "Codex requested a subagent on gpt-5.5; magpie put it on codex/gpt-6-astra, the model set for its subagents.",
    kept: "Codex requested a subagent on gpt-5.5 and it stayed there: its subagents are set to codex/gpt-6-astra, but no ChatGPT account on in magpie serves it.",
    sealed: "Codex requested a subagent on gpt-5.5 and it stayed there: its subagents are set to token/gpt-6.1-sol, but its task was sealed by the ChatGPT backend that answered its lead, and only a ChatGPT account can open it.",
    lead: "subagent model: Same as the lead", leadLine: "Every subagent runs on the lead's model (GPT-5.5), even when the lead names another. Lead's pick lets the lead choose again.", same: "Same as the lead",
    leadMoved: "Codex requested a subagent on gpt-6-astra; magpie put it on token/gpt-6.1-sol, the model its lead is on." },
  zh: { unset: "子代理模型：由主代理自选", line: "主代理为每个子代理选模型，也可能选 Codex 自带的模型；主代理没指定时，子代理用主代理的模型（GPT-5.5）。恢复的子代理仍用它最初的模型。选“跟主代理一样”或在这里选定一个模型，所有子代理都固定用它，主代理指定了别的也一样。",
    old: "Codex 配置里还写着：主代理没指定时，子代理用 gpt-5.4-mini。在这里选一次就会替换掉它。",
    def: "由主代理自选", note: "GPT-5.5，主代理指定其他模型时除外", set: "子代理模型：GPT-6 Astra", setLine: "所有子代理都用这个模型，主代理指定了别的也一样。选“由主代理自选”就让主代理重新选择。", row: "Codex 子代理模型",
    moved: "Codex 请求了使用 gpt-5.5 的子代理；magpie 将其改用 codex/gpt-6-astra，即为它的子代理设定的模型。",
    kept: "Codex 请求了使用 gpt-5.5 的子代理，并保留在该模型上：它的子代理设为 codex/gpt-6-astra，但magpie 中启用的 ChatGPT 账号都不提供该模型。",
    sealed: "Codex 请求了使用 gpt-5.5 的子代理，并保留在该模型上：它的子代理设为 token/gpt-6.1-sol，但它的任务由回答主代理的 ChatGPT 后端加密，只有 ChatGPT 账号能打开。",
    lead: "子代理模型：跟主代理一样", leadLine: "所有子代理都用主代理的模型（GPT-5.5），主代理指定了别的也一样。选“由主代理自选”就让主代理重新选择。", same: "跟主代理一样",
    leadMoved: "Codex 请求了使用 gpt-6-astra 的子代理；magpie 将其改用 token/gpt-6.1-sol，即主代理正在用的模型。" },
  "zh-TW": { unset: "子代理模型：由主代理自選", line: "主代理為每個子代理選模型，也可能選 Codex 自帶的模型；主代理沒指定時，子代理用主代理的模型（GPT-5.5）。恢復的子代理仍用它最初的模型。選「跟主代理一樣」或在這裡選定一個模型，所有子代理都固定用它，主代理指定了別的也一樣。",
    old: "Codex 設定裡還寫著：主代理沒指定時，子代理用 gpt-5.4-mini。在這裡選一次就會取代它。",
    def: "由主代理自選", note: "GPT-5.5，主代理指定其他模型時除外", set: "子代理模型：GPT-6 Astra", setLine: "所有子代理都用這個模型，主代理指定了別的也一樣。選「由主代理自選」就讓主代理重新選擇。", row: "Codex 子代理模型",
    moved: "Codex 請求了使用 gpt-5.5 的子代理；magpie 將其改用 codex/gpt-6-astra，即為它的子代理設定的模型。",
    kept: "Codex 請求了使用 gpt-5.5 的子代理，並保留在該模型上：它的子代理設為 codex/gpt-6-astra，但magpie 中啟用的 ChatGPT 帳號都不提供該模型。",
    sealed: "Codex 請求了使用 gpt-5.5 的子代理，並保留在該模型上：它的子代理設為 token/gpt-6.1-sol，但它的任務由回應主代理的 ChatGPT 後端加密，只有 ChatGPT 帳號能打開。",
    lead: "子代理模型：跟主代理一樣", leadLine: "所有子代理都用主代理的模型（GPT-5.5），主代理指定了別的也一樣。選「由主代理自選」就讓主代理重新選擇。", same: "跟主代理一樣",
    leadMoved: "Codex 請求了使用 gpt-6-astra 的子代理；magpie 將其改用 token/gpt-6.1-sol，即主代理正在用的模型。" },
  ja: { unset: "サブエージェントのモデル：リードが選ぶ", line: "各サブエージェントのモデルはリードが選びます（Codex 自身のモデルも含みます）。指定がなければリードのモデル（GPT-5.5）で動きます。再開したサブエージェントは最初のモデルのままです。「リードと同じ」またはここで選んだモデルにすると、リードが別のモデルを指定しても、すべてのサブエージェントがそのモデルに固定されます。",
    old: "Codex の設定には、リードが指定しないときのサブエージェントのモデルとして gpt-5.4-mini が残っています。ここで選ぶとそれを置き換えます。",
    def: "リードが選ぶ", note: "GPT-5.5（リードが別のモデルを指定した場合を除く）", set: "サブエージェントのモデル：GPT-6 Astra", setLine: "リードが別のモデルを指定しても、すべてのサブエージェントがこのモデルで動きます。「リードが選ぶ」を選ぶと、選択をリードに戻します。", row: "Codex サブエージェントのモデル",
    moved: "Codex が gpt-5.5 のサブエージェントをリクエストしました。magpie はサブエージェント用に設定された codex/gpt-6-astra に切り替えました。",
    kept: "Codex が gpt-5.5 のサブエージェントをリクエストし、そのモデルのままになりました。サブエージェントは codex/gpt-6-astra に設定されていますが、magpie で有効な ChatGPT アカウントはどれもこのモデルを提供していません。",
    sealed: "Codex が gpt-5.5 のサブエージェントをリクエストし、そのモデルのままになりました。サブエージェントは token/gpt-6.1-sol に設定されていますが、そのタスクはリードに応答した ChatGPT バックエンドが暗号化しており、開けるのは ChatGPT アカウントだけです。",
    lead: "サブエージェントのモデル：リードと同じ", leadLine: "リードが別のモデルを指定しても、すべてのサブエージェントがリードのモデル（GPT-5.5）で動きます。「リードが選ぶ」を選ぶと、選択をリードに戻します。", same: "リードと同じ",
    leadMoved: "Codex が gpt-6-astra のサブエージェントをリクエストしました。magpie はリードが使っている token/gpt-6.1-sol に切り替えました。" },
  de: { unset: "Modell der Subagenten: Wahl des Haupt-Agenten", line: "Der Haupt-Agent wählt das Modell jedes Subagenten, auch eines von Codex selbst; nennt er keins, läuft der Subagent auf dem Modell des Haupt-Agenten (GPT-5.5). Ein fortgesetzter Subagent bleibt auf seinem ersten Modell. „Wie der Haupt-Agent“ oder ein hier gewähltes Modell legt alle Subagenten darauf fest, auch wenn der Haupt-Agent ein anderes nennt.",
    old: "Die Codex-Konfiguration startet Subagenten noch auf gpt-5.4-mini, wenn der Haupt-Agent keins nennt; eine Wahl hier ersetzt das.",
    def: "Wahl des Haupt-Agenten", note: "GPT-5.5, außer der Haupt-Agent nennt ein anderes", set: "Modell der Subagenten: GPT-6 Astra", setLine: "Alle Subagenten laufen auf diesem Modell, auch wenn der Haupt-Agent ein anderes nennt. „Wahl des Haupt-Agenten“ lässt ihn wieder wählen.", row: "Codex-Subagenten-Modell",
    moved: "Codex hat einen Subagent mit gpt-5.5 angefordert; magpie hat ihn auf codex/gpt-6-astra gesetzt, das für seine Subagenten festgelegte Modell.",
    kept: "Codex hat einen Subagent mit gpt-5.5 angefordert, und er blieb dort: Seine Subagenten sind auf codex/gpt-6-astra gesetzt, aber kein eingeschaltetes ChatGPT-Konto in magpie bietet es an.",
    sealed: "Codex hat einen Subagent mit gpt-5.5 angefordert, und er blieb dort: Seine Subagenten sind auf token/gpt-6.1-sol gesetzt, aber seine Aufgabe wurde vom ChatGPT-Backend versiegelt, das seinen Lead beantwortet hat, und nur ein ChatGPT-Konto kann sie öffnen.",
    lead: "Modell der Subagenten: Wie der Haupt-Agent", leadLine: "Alle Subagenten laufen auf dem Modell des Haupt-Agenten (GPT-5.5), auch wenn der Haupt-Agent ein anderes nennt. „Wahl des Haupt-Agenten“ lässt ihn wieder wählen.", same: "Wie der Haupt-Agent",
    leadMoved: "Codex hat einen Subagent mit gpt-6-astra angefordert; magpie hat ihn auf token/gpt-6.1-sol gesetzt, das Modell seines Haupt-Agenten." },
};

const codex = '.row.agent[data-id="codex@wsl:Ubuntu-24.04"]';
const wsl = "codex@wsl:Ubuntu-24.04";
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(W);

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: Codex's subagent model opens magpie's models, a ChatGPT account's first, a square only in WSL`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 760 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
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
        // Codex's own card: its settings have the row, so no square here
        await page.locator('.row.agent[data-id="codex"] .ag-link').click();
        await page.locator('.row.agent[data-id="codex"] .ag-exp').waitFor();
        assert.equal(await page.locator('.row.agent[data-id="codex"] .field[data-key="subagent_model"]').count(), 0, "a subagent model square on Codex's card beside its settings row");
        assert.equal(await page.locator('.row.agent[data-id="codex"] .field[data-key="subagent"]').count(), 0, "Codex's own subagents square");
        await page.locator('.row.agent[data-id="codex"] .ag-link').click();
        await page.locator('.row.agent[data-id="codex"]:not(.expanded)').waitFor();
        await page.locator(`${codex} .ag-link`).click();
        await page.locator(`${codex}.expanded > .ag-exp`).waitFor();
        await page.locator(`${codex} .ag-exp`).waitFor();

        assert.equal(await page.locator('.row.agent[data-id="codex2"] .field[data-key="subagent_model"]').count(), 0, "a square with nothing to pick");
        const square = page.locator(`${codex} .extras-cell .field.extra[data-key="subagent_model"]`);
        assert.equal(await square.count(), 1);
        assert.equal(await page.locator(`${codex} .field:not(.extra)[data-key="subagent_model"]`).count(), 0, "a picker of its own");
        assert.equal(await page.locator(`${codex} .field[data-key="subagent"]`).count(), 0, "Codex's own subagents square beside it");
        // the model Codex's config has for its subagents, said and lit till replaced
        assert.equal(await square.getAttribute("aria-label"), w.unset + "\n" + w.line + "\n" + w.old);
        assert.equal(await square.evaluate((e) => e.classList.contains("set")), true);
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
        assert(items[0].includes(w.def) && items[0].includes(w.note), items[0]);
        assert(items.some((s) => s.includes("GPT-6 Astra")), items.join(" | "));
        assert(!items.some((s) => s.includes("openai/")), "Codex's own models, which magpie can't put a request on");
        // #1633: another provider's model is offered too
        assert(items.some((s) => s.includes("GPT-6.1 Sol")), items.join(" | "));
        const pop = await page.locator("#pop").boundingBox();
        assert(pop.x >= 0 && pop.x + pop.width <= width, JSON.stringify(pop));
        const left = await page.evaluate(() => [...document.querySelectorAll("#pop *, .extras-cell *")].filter((e) => {
          const s = getComputedStyle(e);
          return parseFloat(s.borderLeftWidth) > 1 && s.borderLeftWidth !== s.borderRightWidth;
        }).length);
        assert.equal(left, 0, "a coloured left border");

        // Lead's pick, already the choice, still takes away the model
        // Codex's config starts subagents on (luckymanjc on Discord: it sent
        // nothing, and every subagent stayed on gpt-6.1-terra)
        await page.locator("#pop #list li").first().click();
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex@wsl:Ubuntu-24.04"] .field.extra.set[data-key="subagent_model"]'));
        assert.deepEqual(sets, [{ agent: wsl, field: "subagent_model", value: "" }]);
        assert.equal(await square.getAttribute("aria-label"), w.unset + "\n" + w.line);
        assert.equal(await page.evaluate(() => scrollY), y, "the pick scrolled the page");
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li", { hasText: "GPT-6 Astra" }).first().click();
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="codex@wsl:Ubuntu-24.04"] .field.extra.set[data-key="subagent_model"]'));
        assert.deepEqual(sets.at(-1), { agent: wsl, field: "subagent_model", value: "codex/gpt-6-astra" });
        // with nothing left in Codex's config, Lead's pick again sends nothing
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li").first().click();
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex@wsl:Ubuntu-24.04"] .field.extra.set[data-key="subagent_model"]'));
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li").first().click();
        await page.waitForTimeout(200);
        assert.equal(sets.length, 3, JSON.stringify(sets));
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li", { hasText: "GPT-6 Astra" }).first().click();
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="codex@wsl:Ubuntu-24.04"] .field.extra.set[data-key="subagent_model"]'));
        assert.equal(await square.getAttribute("aria-label"), w.set + "\n" + w.setLine);

        // Same as the lead: every subagent on the model asked for again, and
        // Codex's config no longer has one
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li").first().click();
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="codex@wsl:Ubuntu-24.04"] .field.extra.set[data-key="subagent_model"]'));
        assert.deepEqual(sets.at(-1), { agent: wsl, field: "subagent_model", value: "" });
        assert.equal(await square.getAttribute("aria-label"), w.unset + "\n" + w.line);
        assert.equal(await page.evaluate(() => scrollY), y);

        // Codex's sheet has it as a row with a name: where Codex's own is
        // picked
        const pick = page.locator("#codexSubagentModelRow button");
        await pick.waitFor();
        assert.equal((await page.locator("#codexSubagentModelRow .name").textContent()).trim(), w.row);
        await pick.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li", { hasText: "GPT-6 Astra" }).first().click();
        await page.locator("#codexSubagentModelRow button", { hasText: "GPT-6 Astra" }).waitFor();
        assert.deepEqual(sets.at(-1), { agent: "codex", field: "subagent_model", value: "codex/gpt-6-astra" });
        assert.equal(await page.locator("#codexSubagentModelRow .who").getAttribute("title"), w.setLine);
        // said under its name too, not only on hover (HoGee_xxl on X)
        assert.equal(await page.locator("#codexSubagentModelRow .who .sub").textContent(), w.setLine);
        await pick.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        await page.locator("#pop #list li").first().click();
        await page.locator("#codexSubagentModelRow button", { hasText: w.def }).waitFor();
        assert.equal(await page.locator("#codexSubagentModelRow .who .sub").count(), 1);
        assert((await page.locator("#codexSubagentModelRow .who .sub").textContent()).startsWith(w.line), "the row's note says what Same as the lead does");
        assert.deepEqual(sets.at(-1), { agent: "codex", field: "subagent_model", value: "" });

        // the Routing page's story: put on the model set, or why not
        // (each from the list afresh: a narrow window shows the story over it)
        for (const [i, said] of [[0, w.moved], [1, w.kept], [2, w.sealed]]) {
          await page.goto("http://magpie.test/?view=routing");
          await page.locator(".rt-days .rt-day").nth(1).click();
          await page.locator(".rt-req").nth(1).waitFor();
          await page.locator(".rt-req").nth(i).evaluate((e) => e.click());
          await page.waitForTimeout(300);
          const story = await page.locator(".rt-steps").textContent();
          assert(story.includes(said), `${said}\nnot in\n${story}`);
          // nothing says ChatGPT served the model the lead asked for
          if (i === 0 && lang === "en") assert(!story.includes("ChatGPT serves it") && story.includes("only account on for codex/gpt-6-astra"), story);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
