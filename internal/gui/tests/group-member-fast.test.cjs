// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's member may be sent in its vendor's fast mode (vs on Discord:
// "gpt-6.1 sol · high · fast"). The group's card reads "· fast" after the
// member's reasoning; in the group editor a model that has a fast mode
// (canFast) shows a toggle beside its reasoning, "Standard speed" or
// "Fast", and one that has none shows no toggle. Clicking it moves nothing
// on the page; saved, the members sent fast go as fast, and a member whose
// reasoning is changed stays fast under its new id, while one taken out
// leaves it. A GPT model on a provider the user added by its address
// (ownFast, #1615) has the toggle too, off until clicked, and its title says
// fast is asked of the provider. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", efforts: ["low", "medium", "high", "xhigh"], canFast: true },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic", canFast: true },
  { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic" },
  // a GPT model on a provider the user added by its address (#1615)
  { id: "rl/o4-mini", name: "o4-mini", providerName: "Relay", icon: "generic", canFast: true, ownFast: true },
];
const members = ["oa/gpt-6.1-sol:high", "an/claude-opus-5-5", "rl/glm-5", "rl/o4-mini"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, fast: ["oa/gpt-6.1-sol:high"], routing: "order", ready: true,
    memberInfo: members.map((id) => ({ id, ready: true, fast: id === "oa/gpt-6.1-sol:high", canFast: id !== "rl/glm-5", ownFast: id === "rl/o4-mini" })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", fast: "Fast", std: "Standard speed", label: "gpt-6.1-sol · high reasoning · fast", remove: "Remove",
    ownOff: 'Sent as the agent asks; click to ask your provider for OpenAI\'s fast mode (service_tier "priority")',
    ownOn: 'Asked for OpenAI\'s fast mode (service_tier "priority") whatever the agent asks: quicker, at a higher price, where your provider serves it. One that turns it away is asked without it' },
  zh: { edit: "编辑", save: "保存", fast: "快速", std: "标准速度", label: "gpt-6.1-sol · high 推理强度 · 快速", remove: "移除",
    ownOff: '按代理的请求发送；点击向你的服务商请求 OpenAI 的快速模式（service_tier "priority"）',
    ownOn: '无论代理怎么请求，都向服务商请求 OpenAI 的快速模式（service_tier "priority"）：服务商支持时更快，价格更高。服务商拒绝这个字段时，会去掉它再请求一次' },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a group's member is sent fast`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-member-fast.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();
      assert((await card.locator(".mem").textContent()).includes(w.label), "the card says the member is sent fast");

      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) });
      const fast = (name) => row(name).locator("button.rt-fast");
      await fast("gpt-6.1-sol").waitFor();
      assert.equal(await fast("gpt-6.1-sol").textContent(), w.fast);
      assert.equal(await fast("gpt-6.1-sol").getAttribute("aria-pressed"), "true");
      assert.equal(await fast("claude-opus-5-5").textContent(), w.std);
      assert.equal(await fast("claude-opus-5-5").getAttribute("aria-pressed"), "false");
      assert.equal(await fast("glm-5").count(), 0, "no fast mode, no toggle");
      // on the user's own provider (#1615): off by default, and it says
      // fast is only asked of the provider
      assert.equal(await fast("o4-mini").getAttribute("aria-pressed"), "false");
      assert.equal(await fast("o4-mini").getAttribute("title"), w.ownOff);
      assert.notEqual(await fast("claude-opus-5-5").getAttribute("title"), w.ownOff);
      const border = await page.evaluate(() => [...document.querySelectorAll(".rt-fast")].map((e) => getComputedStyle(e))
        .filter((c) => c.borderLeftWidth !== c.borderRightWidth || c.borderLeftColor !== c.borderRightColor));
      assert.equal(border.length, 0, "no left-border accent");

      // toggles, the page kept still
      await row("claude-opus-5-5").evaluate((x) => x.scrollIntoView({ block: "center" })); // by the test, as the reader would
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
      const before = await at();
      await fast("claude-opus-5-5").click();
      assert.equal(await fast("claude-opus-5-5").textContent(), w.fast);
      await fast("gpt-6.1-sol").click();
      assert.equal(await fast("gpt-6.1-sol").textContent(), w.std);
      await fast("gpt-6.1-sol").click();
      assert.equal(await at(), before, "the clicks moved nothing");

      // the gpt member's reasoning changed: still fast, under its new id
      await row("gpt-6.1-sol").locator("button.rt-fixed:not(.rt-fast)").click();
      await page.locator(".pop .opt, .pop [role=option], .pop li", { hasText: /^xhigh/ }).first().click();
      assert.equal(await fast("gpt-6.1-sol").textContent(), w.fast, "fast follows the reasoning");
      // the Claude member taken out takes its fast with it
      await row("claude-opus-5-5").locator("button.text", { hasText: w.remove }).click();
      assert.equal(await row("claude-opus-5-5").count(), 0);

      await fast("o4-mini").click();
      assert.equal(await fast("o4-mini").textContent(), w.fast);
      assert.equal(await fast("o4-mini").getAttribute("title"), w.ownOn);

      const b = ed.locator("button.primary", { hasText: w.save });
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const bb = await b.boundingBox();
      await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.members, ["oa/gpt-6.1-sol:xhigh", "rl/glm-5", "rl/o4-mini"]);
      assert.deepEqual(sent.body.fast, ["oa/gpt-6.1-sol:xhigh", "rl/o4-mini"]);

      const missing = await page.evaluate(() => [
        "fast", "Fast", "Standard speed", "Sent in its vendor's fast mode, as the group says",
        "Sent in its vendor's fast mode whatever the agent asks: quicker, at a higher price",
        "Sent at its vendor's usual speed; click to send it in fast mode",
        'Asked for OpenAI\'s fast mode (service_tier "priority") whatever the agent asks: quicker, at a higher price, where your provider serves it. One that turns it away is asked without it',
        'Sent as the agent asks; click to ask your provider for OpenAI\'s fast mode (service_tier "priority")',
        "{model} is asked for OpenAI's fast mode for {agent}, where your provider serves it. Click to send it as {agent} asks",
        'Ask your provider for OpenAI\'s fast mode (service_tier "priority") on {model} for {agent}: quicker, at a higher price, where it serves it',
      ].filter((k) => !I18N.zh[k] || !I18N["zh-TW"][k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
