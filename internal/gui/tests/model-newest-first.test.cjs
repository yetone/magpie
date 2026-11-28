// Run with Node's test runner and Playwright on the module path; see README.md.
// elis_pina on X: 能不能加一个根据模型的推出时间进行排序功能啊，除了在供应商的界面还有
// agent界面。不然现在有一些后面加进来的老模型在Agent的界面排序是在新模型之前的.
// Every list a model is picked from puts the newest release first, by the
// day the server sends (`released`): an older model added later stands
// after the newer ones, one with no day after every dated one, in the
// order its provider listed it.
// - A provider's editor: within a maker, the day before the version its
//   name holds (claude-fable-5-2 came out after claude-haiku-5-5).
// - An agent's model list, by provider and by maker; an order the user
//   dragged (ordered) stays theirs.
// - A routing group's Add a model picker, each provider's together.
// Every language, Chromium and WebKit, at 390px and wide. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const fetched = new Date(Date.now() - 3600e3).toISOString();

// the days models.dev gives them (catalog/testdata), listed in the order
// they were added: the oldest Claude first, one with no day among them
const DAYS = {
  "claude-opus-4-1": "2025-08-05", "claude-haiku-5-5": "2026-10-07", "claude-fable-5-2": "2026-11-09",
  "gpt-5.4": "2026-03-05", "gpt-5.5": "2026-04-23", alpha: "2026-01-01",
};
const day = (id) => (DAYS[id] ? { released: DAYS[id] } : {});

const relay = {
  icon: "generic", responses: "", anthropic: "", catalog: "", chosen: [], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  id: "relay", name: "Relay", host: "relay.example.com", chat: "https://relay.example.com/v1",
  models: [
    ["claude-opus-4-1", "claude-color"], ["claude-next-9", "claude-color"], ["gpt-5.4", "openai"], ["claude-haiku-5-5", "claude-color"],
    ["zeta"], ["claude-fable-5-2", "claude-color"], ["gpt-5.5", "openai"], ["alpha"], ["beta"],
  ].map(([id, maker]) => ({ id, on: false, ...(maker ? { maker } : {}), ...day(id) })),
};

const agentModels = () => [
  ["relay", "Relay", "claude-opus-4-1", "Claude Opus 4.1", "claude-color"],
  ["relay", "Relay", "zeta", "Zeta"],
  ["relay", "Relay", "claude-fable-5-2", "Claude Fable 5.2", "claude-color"],
  ["relay", "Relay", "alpha", "Alpha"],
  ["relay", "Relay", "claude-haiku-5-5", "Claude Haiku 5.5", "claude-color"],
  ["codex", "Codex", "gpt-5.4", "GPT-5.4", "openai"],
  ["codex", "Codex", "gpt-5.5", "GPT-5.5", "openai"],
].map(([p, group, id, name, maker]) => ({ id: `${p}/${id}`, name, group, icon: p === "codex" ? "codex-color" : "generic", logo: maker || "generic", ...(maker ? { maker } : {}), context: 200e3, ...day(id) }))
  .concat([{ id: "group/fast", name: "fast", group: "Routing groups", icons: ["codex-color"], context: 200e3 }]);

const routingModels = [
  ["relay", "Relay", "claude-opus-4-1", "Claude Opus 4.1"],
  ["relay", "Relay", "zeta", "Zeta"],
  ["codex", "Codex", "gpt-5.4", "GPT-5.4"],
  ["relay", "Relay", "claude-fable-5-2", "Claude Fable 5.2"],
  ["codex", "Codex", "gpt-5.5", "GPT-5.5"],
  ["relay", "Relay", "alpha", "Alpha"],
].map(([provider, providerName, id, name]) => ({ id: `${provider}/${id}`, name, provider, providerName, icon: "generic", ...day(id) }));

function serve(lang, ordered) {
  const state = {
    agents: [{
      id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
      fields: [{ key: "model", label: "model", value: "magpie/codex/gpt-5.5", options: [{ value: "magpie/codex/gpt-5.5", label: "GPT-5.5", ref: "codex/gpt-5.5" }] }],
      models: { shown: 8, listed: 8, by: [] },
    }],
    profiles: [], settings: { lang, theme: "light" },
  };
  const groups = {
    models: routingModels, drawers: [], pools: [],
    groups: [{ id: "g", name: "G", members: ["relay/zeta"], off: [], routing: "order", ready: true, memberInfo: [{ id: "relay/zeta", ready: true }] }],
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/agent-models/codex") return json({ models: agentModels(), ...(ordered ? { ordered: true } : {}) });
    if (url.pathname === "/api/providers") return json({ providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    if (!body) return route.fulfill({ status: 404, body: "" });
    await route.fulfill({ body, contentType });
  };
}

// wait on what is drawn, never on a duration
const until = async (ok, what) => {
  let last;
  for (const end = Date.now() + 5000; Date.now() < end; await new Promise((r) => setTimeout(r, 25))) if ((last = await ok()) === true) return;
  assert.fail(`timed out waiting for ${what}: ${JSON.stringify(last)}`);
};
const same = (want) => async (got) => JSON.stringify(got) === JSON.stringify(want) || got;

async function page(browser, lang, width, ordered, t) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
  const p = await ctx.newPage();
  p.setDefaultTimeout(5000);
  p.setDefaultNavigationTimeout(60000);
  const errors = [];
  p.on("pageerror", (e) => errors.push(e.message));
  await p.route("**/*", serve(lang, ordered));
  t.after(() => ctx.close());
  return { p, errors };
}

const other = { en: "Other", zh: "其他", "zh-TW": "其他", ja: "その他", de: "Sonstige" };
const langs = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of langs) {
    for (const width of lang === "en" ? [390, 1000] : [390]) {
      test(`${engine} ${lang} ${width}px: the newest release first`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());

        // a provider's editor: each maker's newest first, one with no
        // day after the dated, in the vendor's order (zeta before beta)
        {
          const { p, errors } = await page(browser, lang, width, false, t);
          await p.goto("http://magpie.test/?view=providers");
          await p.locator(".row.provider", { hasText: "Relay" }).click();
          const editor = p.locator("#modal:not([hidden]) .editor");
          await p.waitForFunction(() => document.querySelector("#modal:not([hidden]) .editor .mchips .mmaker"));
          const drawn = () => editor.locator(".mchips").first().evaluate((box) =>
            [...box.querySelectorAll(":scope > .mmaker, :scope > .mchip")].map((e) => (e.classList.contains("mmaker") ? [e.textContent] : e.querySelector("span").textContent)));
          await until(async () => same([
            ["Anthropic"], "claude-fable-5-2", "claude-haiku-5-5", "claude-opus-4-1", "claude-next-9",
            ["OpenAI"], "gpt-5.5", "gpt-5.4",
            [other[lang]], "alpha", "zeta", "beta",
          ])(await drawn()), "the editor's order");
          const over = await editor.locator(".mchips").first().evaluate((b) => b.scrollWidth - b.clientWidth);
          assert.ok(over <= 0, `the chips run ${over}px past their box`);
          assert.deepEqual(errors, []);
        }

        // an agent's model list, by provider, then by maker; and with an
        // order the user dragged, as they left it
        for (const ordered of [false, true]) {
          const { p, errors } = await page(browser, lang, width, ordered, t);
          await p.goto("http://magpie.test/");
          const row = p.locator('.row.agent[data-id="codex"]');
          await row.locator(".ag-link").click();
          const entry = row.locator(".ag-exp .ag-chips .ag-quiet");
          await entry.waitFor();
          await p.waitForFunction(() => document.getAnimations().length === 0);
          await entry.click();
          const pop = p.locator(".am-pop:not(.leaving)");
          await pop.waitFor();
          const names = () => pop.locator(".am-mr .n").allInnerTexts();
          const byProvider = ordered
            ? ["fast", "Claude Opus 4.1", "Zeta", "Claude Fable 5.2", "Alpha", "Claude Haiku 5.5", "GPT-5.4", "GPT-5.5"]
            : ["fast", "Claude Fable 5.2", "Claude Haiku 5.5", "Alpha", "Claude Opus 4.1", "Zeta", "GPT-5.5", "GPT-5.4"];
          await until(async () => same(byProvider)(await names()), `by provider${ordered ? ", ordered" : ""}`);
          await pop.locator(".am-head .am-by button").nth(1).click();
          const byMaker = ordered
            ? ["fast", "Claude Opus 4.1", "Claude Fable 5.2", "Claude Haiku 5.5", "GPT-5.4", "GPT-5.5", "Zeta", "Alpha"]
            : ["fast", "Claude Fable 5.2", "Claude Haiku 5.5", "Claude Opus 4.1", "GPT-5.5", "GPT-5.4", "Alpha", "Zeta"];
          await until(async () => same(byMaker)(await names()), `by maker${ordered ? ", ordered" : ""}`);
          assert.deepEqual(errors, []);
        }

        // a routing group's Add a model picker: each provider's together,
        // the newest first
        {
          const { p, errors } = await page(browser, lang, width, false, t);
          await p.goto("http://magpie.test/?view=groups");
          const card = p.locator('.rt-group[data-id="g"]');
          await card.waitFor();
          const edit = await p.evaluate(() => t("Edit"));
          await card.locator("button", { hasText: edit }).click();
          await p.locator(".rt-gedit button.rt-gadd").first().click();
          const pop = p.locator("#pop");
          await pop.locator("li .v").first().waitFor();
          await until(async () => same(["Claude Fable 5.2", "Alpha", "Claude Opus 4.1", "Zeta", "GPT-5.5", "GPT-5.4"])(
            await pop.locator("li .v").allInnerTexts()), "the picker's order");
          assert.equal(await p.locator("select").count(), 0, "a native select");
          assert.deepEqual(errors, []);
        }
      });
    }
  }
}
