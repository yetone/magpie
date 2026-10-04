// Routing explains a sealed task's eligibility and parent account before
// quota ordering (#619), without inferring encryption from a subagent tag.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (minutes) => new Date(now.getTime() - minutes * 60e3).toISOString();
const account = {
  id: "codex", provider: "codex", name: "Codex", who: "lead@example.com",
  kind: "account", agent: "codex", model: "gpt-6-astra", routing: "pace",
  known: true, used: 45, pace: 1, due: new Date(now.getTime() + 55 * 36e5).toISOString(),
};
const spare = { ...account, id: "codex-spare", who: "spare@example.com", used: 2, pace: 0.6 };
const enterprise = {
  ...account, id: "claude-enterprise", provider: "claude", name: "Claude",
  who: "enterprise@example.com", agent: "claude", model: "claude-opus-5-5",
  used: 0, pace: 20, due: null,
};
const group = { id: "mixed", name: "Mixed", routing: "pace", members: ["claude/claude-opus-5-5", "codex/gpt-6-astra"] };
const route = (id, fields) => {
  const r = {
    id, seq: id, time: at(1), agent: "codex", model: "group/mixed", provider: "claude",
    kind: "collab_spawn", group, order: [account, spare],
    done: true, status: 200, ms: 900, tokens: 1200, ...fields,
  };
  r.tries = [{ id: r.order[0].id, model: r.order[0].model, start: at(1), done: true, status: 200, ms: 900 }];
  return r;
};
const routes = [
  route(105, { sealedTask: true, leadAccount: account.id, rule: { n: 0, turn: 1, tokens: 42000 } }),
  route(104, { sealedTask: true }),
  route(103, { order: [enterprise, account] }),
  route(102, { group: null, model: "codex/gpt-6-astra" }),
  // Old traces do not say whether the task was sealed.
  route(101, {}),
  route(100, { sealedTask: true, rule: { n: 12, use: "claude/claude-opus-5-5", unready: true, turn: 1 } }),
  route(99, { sealedTask: true, group: null, model: "codex/gpt-6-astra", order: [account] }),
];

function serve(lang) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({
      contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};`,
    });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({
      contentType: "text/javascript", body: "export const Window = {};",
    });
    if (url.pathname === "/api/state") return json({
      agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }],
      profiles: [], settings: { lang, theme: "light" },
    });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((resolve) => setTimeout(resolve, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 105, totals: { requests: routes.length }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: sealed eligibility and parent priority are explained`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 1200 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      const sealed = lang === "zh" ? /任务已加密.*只能使用 ChatGPT/ : /task is encrypted.*Only ChatGPT/;
      const excluded = lang === "zh" ? /其他供应商（如 Claude）不参与选择/ : /other providers \(such as Claude\) are excluded/;
      const parent = lang === "zh" ? /回答了主代理/ : /answered the parent agent/;
      const selected = lang === "zh" ? /具体模型由 magpie/ : /magpie selects its model/;
      const allowance = lang === "zh" ? /剩余额度除以距重置的小时数/ : /remaining allowance per hour/;

      const story = () => page.locator(".rt-steps").textContent();
      await page.locator(".rt-req").first().click();
      await page.waitForFunction((pattern) => new RegExp(pattern).test(document.querySelector(".rt-steps").textContent), sealed.source);
      assert.match(await story(), sealed);
      assert.match(await story(), excluded);
      assert.match(await story(), parent);
      assert.match(await story(), selected);
      assert.doesNotMatch(await page.locator(".rt-steps li.why").first().textContent(), allowance);
      assert.doesNotMatch(await story(), lang === "zh" ? /平常的方式路由/ : /routes it as usual/);

      await page.locator(".rt-req").nth(1).click();
      await page.waitForFunction((pattern) => !new RegExp(pattern).test(document.querySelector(".rt-steps").textContent), parent.source);
      assert.match(await story(), sealed);
      assert.doesNotMatch(await story(), parent);
      assert.match(await story(), allowance);

      await page.locator(".rt-req").nth(2).click();
      await page.waitForFunction(() => document.querySelector(".rt-steps li.why").textContent.includes("enterprise@example.com"));
      assert.doesNotMatch(await story(), sealed);
      assert.match(await story(), selected);
      assert.match(await story(), allowance);
      assert.doesNotMatch(await story(), lang === "zh" ? /每周剩余额度/ : /its week left/);

      await page.locator(".rt-req").nth(3).click();
      await page.waitForFunction(() => !document.querySelector(".rt-steps li.kind").textContent.includes("magpie"));
      assert.doesNotMatch(await story(), selected);
      assert.doesNotMatch(await story(), sealed);

      await page.locator(".rt-req").nth(4).click();
      await page.waitForFunction(() => document.querySelector(".rt-steps li.kind").textContent.includes("magpie"));
      assert.doesNotMatch(await story(), sealed);
      assert.doesNotMatch(await story(), parent);
      await page.locator(".rt-req").nth(5).click();
      await page.waitForFunction(() => document.querySelector(".rt-steps").textContent.includes("12"));
      assert.match(await story(), sealed);
      assert.match(await story(), lang === "zh" ? /没有能接这次请求的账号或 Key/ : /no account or key that can take this request/);
      assert.doesNotMatch(await story(), lang === "zh" ? /候选/ : /eligible candidate/);
      await page.locator(".rt-req").nth(6).click();
      await page.waitForFunction((pattern) => !new RegExp(pattern).test(document.querySelector(".rt-steps").textContent), selected.source);
      assert.match(await story(), sealed);
      assert.doesNotMatch(await story(), excluded);
      assert.doesNotMatch(await story(), /Claude/);
      assert.deepEqual(errors, []);
    });
  }
}
