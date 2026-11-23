// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's member may be sent in its vendor's fast mode (vs on Discord:
// "gpt-6.1 sol · high · fast"). The group's card reads "· fast" after the
// member's reasoning; in the group editor a model that has a fast mode
// (canFast) shows a button beside its reasoning, "Standard speed", "Fast"
// or "Ultrafast", that opens a menu of them (Ultrafast only where ultraOK;
// YsiCing on X: a button stepping through them read as only two), and one
// that has none shows no button. Picking moves nothing on the page; saved, the members sent fast go as fast, and a member whose
// reasoning is changed stays fast under its new id, while one taken out
// leaves it. A GPT model on a provider the user added by its address
// (ownFast, #1615) has the button too, off until picked, and its title says
// fast is asked of the provider; one OpenAI offers Ultrafast on (ultraOK)
// offers Ultrafast, and its title says that is asked for. A group's
// card says "· Ultrafast" of a member sent so, not "· fast" (OoopsHh on X). In English and Chinese, Chromium and WebKit.
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
  // one OpenAI offers Ultrafast on (KoiLx0 on X)
  { id: "rl/gpt-6.2-sol", name: "gpt-6.2-sol", providerName: "Relay", icon: "generic", canFast: true, ownFast: true, ultraOK: true },
  // #6881's member (paul-tian; ggbdpq's fixture): a Codex account's
  // GPT-6-Astra set Ultrafast, which the list read as Fast
  { id: "cx/gpt-6-astra", name: "gpt-6-astra", providerName: "Codex", icon: "generic", canFast: true, ultraOK: true },
];
const members = ["oa/gpt-6.1-sol:high", "an/claude-opus-5-5", "rl/glm-5", "rl/o4-mini", "rl/gpt-6.2-sol"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, fast: ["oa/gpt-6.1-sol:high"], routing: "order", ready: true,
    memberInfo: members.map((id) => ({ id, ready: true, fast: id === "oa/gpt-6.1-sol:high", canFast: id !== "rl/glm-5", ownFast: id.startsWith("rl/") && id !== "rl/glm-5", ultraOK: id === "rl/gpt-6.2-sol" })) },
  // one sent Ultrafast: its card says Ultrafast, not fast (OoopsHh on X)
  { id: "astra", name: "Astra", members: ["cx/gpt-6-astra", "rl/gpt-6.2-sol"], fast: ["cx/gpt-6-astra", "rl/gpt-6.2-sol"], ultra: ["cx/gpt-6-astra", "rl/gpt-6.2-sol"], routing: "order", ready: true,
    memberInfo: [{ id: "cx/gpt-6-astra", ready: true, fast: true, ultra: true, canFast: true, ultraOK: true },
      { id: "rl/gpt-6.2-sol", ready: true, fast: true, ultra: true, canFast: true, ownFast: true, ultraOK: true }] }],
});

const words = {
  en: { edit: "Edit", save: "Save", fast: "Fast", std: "Standard speed", label: "gpt-6.1-sol · high reasoning · fast", ultraLabel: "gpt-6.2-sol · Ultrafast", codexLabel: "gpt-6-astra · Ultrafast", remove: "Remove",
    ownOff: "Sent as the agent asks", noUltra: "No account here offers Ultrafast on this model",
    ownOn: 'Asked for OpenAI\'s fast mode (service_tier "priority") whatever the agent asks: quicker, at a higher price, where your provider serves it. One that turns it away is asked without it',
    ultra: "Ultrafast", ownUltra: 'Asked for service_tier "ultrafast" whatever the agent asks, where your provider serves it. One that turns it away is asked without it' },
  zh: { edit: "编辑", save: "保存", fast: "快速", std: "标准速度", label: "gpt-6.1-sol · high 推理强度 · 快速", ultraLabel: "gpt-6.2-sol · 极速", codexLabel: "gpt-6-astra · 极速", remove: "移除",
    ownOff: "按 Agent 的请求发送", noUltra: "这里没有账号在这个模型上提供极速",
    ownOn: '无论代理怎么请求，都向服务商请求 OpenAI 的快速模式（service_tier "priority"）：服务商支持时更快，价格更高。服务商拒绝这个字段时，会去掉它再请求一次',
    ultra: "极速", ownUltra: '无论 Agent 要求什么，都向你的服务商请求极速（service_tier "ultrafast"），前提是它支持；不接受的会改为不带该字段重新请求' },
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
      const card = page.locator('.rt-group[data-id="sol"]');
      await card.waitFor();
      assert((await card.locator(".mem").textContent()).includes(w.label), "the card says the member is sent fast");
      const ultraCard = (await page.locator('.rt-group[data-id="astra"]').locator(".mem").textContent());
      assert(ultraCard.includes(w.ultraLabel), `the card says the member is sent Ultrafast: ${ultraCard}`);
      assert(ultraCard.includes(w.codexLabel) && !ultraCard.includes(`gpt-6-astra · ${w.label.split(" · ").pop()}`), `the card says #6881's Codex member is sent Ultrafast: ${ultraCard}`);

      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) });
      const fast = (name) => row(name).locator("button.rt-fast");
      const option = (text) => page.locator("#pop:not([hidden]) #list li:not(.group)", { has: page.locator(".v", { hasText: new RegExp("^" + text + "$") }) });
      const opts = async () => page.locator("#pop:not([hidden]) #list li:not(.group) .v").allTextContents();
      // opens its menu, says what it offers, and picks one
      const speed = async (name, pick) => {
        await fast(name).click();
        await option(w.std).waitFor();
        const offered = await opts();
        await option(pick).click();
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        return offered;
      };
      await fast("gpt-6.1-sol").waitFor();
      assert.equal(await fast("gpt-6.1-sol").textContent(), w.fast);
      assert.equal(await fast("gpt-6.1-sol").getAttribute("class"), "rt-cond rt-fixed rt-fast on");
      assert.equal(await fast("claude-opus-5-5").textContent(), w.std);
      assert.equal(await fast("claude-opus-5-5").getAttribute("class"), "rt-cond rt-fixed rt-fast");
      assert.equal(await fast("glm-5").count(), 0, "no fast mode, no toggle");
      // on the user's own provider (#1615): off by default, and it says
      // fast is only asked of the provider
      assert.equal(await fast("o4-mini").getAttribute("class"), "rt-cond rt-fixed rt-fast");
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
      // no account offers Ultrafast on it: the menu has two, and says why
      await fast("claude-opus-5-5").click();
      await option(w.std).waitFor();
      assert.deepEqual(await opts(), [w.std, w.fast]);
      assert((await option(w.fast).textContent()).includes(w.noUltra), "Fast says why there is no Ultrafast");
      await option(w.fast).click();
      await page.waitForFunction(() => document.querySelector("#pop").hidden);
      assert.equal(await fast("claude-opus-5-5").textContent(), w.fast);
      await speed("gpt-6.1-sol", w.std);
      assert.equal(await fast("gpt-6.1-sol").textContent(), w.std);
      await speed("gpt-6.1-sol", w.fast);
      assert.equal(await at(), before, "the picks moved nothing");

      // the gpt member's reasoning changed: still fast, under its new id
      await row("gpt-6.1-sol").locator("button.rt-fixed:not(.rt-fast)").click();
      await page.locator(".pop .opt, .pop [role=option], .pop li", { hasText: /^xhigh/ }).first().click();
      assert.equal(await fast("gpt-6.1-sol").textContent(), w.fast, "fast follows the reasoning");
      // the Claude member taken out takes its fast with it
      await row("claude-opus-5-5").locator("button.text", { hasText: w.remove }).click();
      assert.equal(await row("claude-opus-5-5").count(), 0);

      assert.deepEqual(await speed("o4-mini", w.fast), [w.std, w.fast], "no Ultrafast where OpenAI offers none");
      assert.equal(await fast("o4-mini").textContent(), w.fast);
      assert.equal(await fast("o4-mini").getAttribute("title"), w.ownOn);
      // all three on the one OpenAI offers it on
      assert.deepEqual(await speed("gpt-6.2-sol", w.ultra), [w.std, w.fast, w.ultra]);
      assert.equal(await fast("gpt-6.2-sol").textContent(), w.ultra);
      assert.equal(await fast("gpt-6.2-sol").getAttribute("title"), w.ownUltra);

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
      assert.deepEqual(sent.body.members, ["oa/gpt-6.1-sol:xhigh", "rl/glm-5", "rl/o4-mini", "rl/gpt-6.2-sol"]);
      assert.deepEqual(sent.body.fast, ["oa/gpt-6.1-sol:xhigh", "rl/o4-mini", "rl/gpt-6.2-sol"]);
      assert.deepEqual(sent.body.ultra, ["rl/gpt-6.2-sol"]);

      const missing = await page.evaluate(() => [
        "fast", "Fast", "Ultrafast", "Standard speed", "Sent in its vendor's fast mode, as the group says",
        "Sent Ultrafast where the account offers it, as the group says",
        "Sent in its vendor's fast mode whatever the agent asks: quicker, at a higher price",
        "Sent at its vendor's usual speed", "as the agent asks", "No account here offers Ultrafast on this model",
        "Asked for Ultrafast, sent fast (service tier {tier}): the account it went on has no Ultrafast on this model in its plan",
        'Asked for OpenAI\'s fast mode (service_tier "priority") whatever the agent asks: quicker, at a higher price, where your provider serves it. One that turns it away is asked without it',
        "Sent as the agent asks", "Sent Ultrafast where the account offers it, and fast where it doesn't, whatever the agent asks",
        'Asked for service_tier "ultrafast" whatever the agent asks, where your provider serves it. One that turns it away is asked without it',
        "{model} is asked for OpenAI's fast mode for {agent}, where your provider serves it. Click to send it as {agent} asks",
        'Ask your provider for OpenAI\'s fast mode (service_tier "priority") on {model} for {agent}: quicker, at a higher price, where it serves it',
      ].filter((k) => !I18N.zh[k] || !I18N["zh-TW"][k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
