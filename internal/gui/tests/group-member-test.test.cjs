// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's member is tested where it is, from its row (#feedback:
// when a group's first model wasn't the one called, finding out whether that
// model answers meant going to its provider's page). The row's Test sends
// the smallest request a model can be sent, through the provider magpie has
// saved and not through the group (#4540, IceCodeNew). While it waits a
// pulsing dot after the word says so, and then the dot says whether the
// model answered (green), didn't (a red cross) or was queued (amber), with
// the time or the error in its title and the status line; the word never
// changes, so the row's other controls stay where they are. A member pinned
// to an account is tested on that account. A member that is a group is
// tested on its own card, and a member whose provider tests its models as
// one endpoint says why when its Test is clicked, sent nowhere. English and
// Chinese for the flow, every language for the row's fit at the window's
// least width and a phone's, Chromium and WebKit, and a group not saved
// yet.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", provider: "oa", providerName: "OpenAI", icon: "generic", efforts: ["low", "medium", "high"], canFast: false },
  { id: "rl/glm-5", name: "glm-5", provider: "rl", providerName: "Relay", icon: "generic" },
  { id: "cu/composer-1", name: "composer-1", provider: "cu", providerName: "Cursor", icon: "generic", modelTest: "own-api" },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", provider: "an", providerName: "Anthropic", icon: "generic" },
];
const solMembers = ["oa/gpt-6.1-sol", "group/inner", "rl/glm-5", "cu/composer-1"];
const A = "a1b2c3d4e5f60718";
const accounts = { oa: [{ id: "oa/" + A, name: "me@x.com", provider: "oa", providerName: "OpenAI", account: true, plan: "Plus" }] };
const groups = (routing = "order") => ({
  models, pools: [], accounts,
  groups: [
    { id: "sol", name: "Sol", members: solMembers, fast: [], off: [], routing, ready: true,
      memberInfo: [
        { id: "oa/gpt-6.1-sol", ready: true, provider: "oa", name: "OpenAI", icon: "generic", model: "gpt-6.1-sol", on: 1, efforts: ["low", "medium", "high"] },
        { id: "group/inner", ready: true, group: true, name: "Inner", icon: "generic", on: 1 },
        { id: "rl/glm-5", ready: true, provider: "rl", name: "Relay", icon: "generic", model: "glm-5", on: 1 },
        { id: "cu/composer-1", ready: true, provider: "cu", name: "Cursor", icon: "generic", model: "composer-1", on: 1, modelTest: "own-api" },
      ] },
    { id: "inner", name: "Inner", members: ["an/claude-opus-5-5"], fast: [], off: [], routing: "order", ready: true,
      memberInfo: [{ id: "an/claude-opus-5-5", ready: true, provider: "an", name: "Anthropic", icon: "generic", model: "claude-opus-5-5", on: 1 }] },
    { id: "mine", name: "Mine", members: ["oa/gpt-6.1-sol#" + A, "rl/glm-5"], fast: [], off: [], routing: "order", ready: true,
      memberInfo: [
        { id: "oa/gpt-6.1-sol#" + A, ready: true, provider: "oa", name: "OpenAI", icon: "generic", model: "gpt-6.1-sol", on: 1 },
        { id: "rl/glm-5", ready: true, provider: "rl", name: "Relay", icon: "generic", model: "glm-5", on: 1 },
      ] },
  ],
});

const words = {
  en: { edit: "Edit", down: "Down", test: "Test", testing: "Testing…", group: "A group in the group: open Inner's card to test its models there",
    own: "Cursor is reached through its own API, which magpie translates each agent request for, so a test request can't be sent to it on its own. Ask the model from an agent to try it." },
  zh: { edit: "编辑", down: "下移", test: "测试", testing: "测试中…", group: "组里套组：到「Inner」的卡片上测试它的模型",
    own: "Cursor 使用自有接口，由 magpie 转换 Agent 的请求，无法单独发测试请求。请在 Agent 中使用该模型测试。" },
};

function serve(lang, ctl, { web = true, routing = "order" } = {}) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups(routing));
    if (url.pathname.startsWith("/api/groups/")) return json(groups(routing));
    // the member's own test: held until the test releases it, so the button
    // in between is checked without a wait on the clock
    if (url.pathname === "/api/provider/test") {
      ctl.calls.push(JSON.parse(r.request().postData() || "{}"));
      await new Promise((go) => { ctl.release = go; });
      return json({ results: [ctl.reply] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// until: waits for what asking for the test put in the page's state, never a
// duration (the answer is held by the test)
async function until(fn) {
  for (let i = 0; i < 1000; i++) {
    if (fn()) return;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error("the state waited on never came");
}

const editWord = { en: "Edit", zh: "编辑", "zh-TW": "編輯", ja: "編集", de: "Bearbeiten" };

// openGroup: the Routing page, wide or narrow, with the test's answers held
async function openGroup(engine, lang, width, t, opts) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1800 }, reducedMotion: "reduce" })).newPage();
  t.after(() => browser.close());
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], ctl = { calls: [], release: null, reply: null };
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, ctl, opts));
  await page.goto("http://magpie.test/?view=groups");
  return { page, ctl, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a group's members are tested from their rows`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1800 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-member-test.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], ctl = { calls: [], release: null, reply: null };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, ctl));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator('.rt-group[data-id="sol"]');
      await card.waitFor();
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = (name) => ed.locator(".fbrow", { has: page.locator(".n", { hasText: name }) });
      const btn = (name) => row(name).locator("button.rt-mtest");
      const status = page.locator("#status");

      // a model's own row has its Test on; a group's and a provider that
      // can't test its models one by one say why when clicked
      await btn("gpt-6.1-sol").waitFor();
      assert.equal(await btn("gpt-6.1-sol").getAttribute("aria-disabled"), null);
      assert.equal((await btn("gpt-6.1-sol").textContent()).trim(), w.test);
      assert.equal(await btn("Inner").getAttribute("title"), w.group);
      assert.equal(await btn("composer-1").getAttribute("title"), w.own);
      // clicked, each says why and sends nothing: no dead button, and no
      // reason hidden in a tooltip a disabled control never shows
      await btn("Inner").click();
      assert.equal((await status.textContent()).trim(), w.group);
      await btn("composer-1").click();
      assert.equal((await status.textContent()).trim(), w.own);
      assert.deepEqual(ctl.calls, [], "nothing was sent");

      // where the reader is, so a click that moved the page would show
      await btn("gpt-6.1-sol").scrollIntoViewIfNeeded();
      await page.waitForTimeout(100);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop && !e.closest("#pop")).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
      const before = await at();

      // the test, asking the saved provider only for this model
      ctl.reply = { ok: true, protocol: "chat", model: "gpt-6.1-sol", ms: 312 };
      await btn("gpt-6.1-sol").click();
      await until(() => ctl.release);
      // the word stays (the row doesn't shift), the dot pulses, and a second
      // click while it waits sends nothing more
      assert.equal((await btn("gpt-6.1-sol").textContent()).trim(), w.test);
      assert.equal(await btn("gpt-6.1-sol").getAttribute("aria-busy"), "true");
      assert.equal(await btn("gpt-6.1-sol").getAttribute("aria-disabled"), "true");
      assert.equal(await btn("gpt-6.1-sol").getAttribute("title"), w.testing);
      assert.equal(await btn("gpt-6.1-sol").locator(".tdot.wait").count(), 1);
      const waitW = (await btn("gpt-6.1-sol").boundingBox()).width;
      await btn("gpt-6.1-sol").evaluate((b) => b.click());
      assert.deepEqual(ctl.calls, [{ id: "oa", test: ["gpt-6.1-sol"] }], "the model alone, as the provider is saved, once");
      ctl.release();
      ctl.release = null;
      await page.waitForFunction(() => document.querySelector(".rt-gedit .fbrow .rt-mtest .tdot.ok"));
      assert.equal((await btn("gpt-6.1-sol").textContent()).trim(), w.test);
      assert.equal(await btn("gpt-6.1-sol").getAttribute("aria-busy"), null);
      assert.equal((await btn("gpt-6.1-sol").boundingBox()).width, waitW, "the answer doesn't move the row's controls");
      if (lang === "en") {
        assert.match(await btn("gpt-6.1-sol").getAttribute("title"), /Test this model\nAnswered via OpenAI in 312 ms/);
        assert.match(await status.textContent(), /gpt-6\.1-sol answered via OpenAI in 312 ms/);
      } else {
        assert.match(await status.textContent(), /gpt-6\.1-sol/);
      }
      assert.equal(await at(), before, "the test moved nothing on the page");

      // a red one: the answer and why, where the reader is
      ctl.calls.length = 0;
      ctl.reply = { ok: false, protocol: "chat", model: "gpt-6.1-sol", status: 401, error: "Invalid API key", ms: 12 };
      await btn("gpt-6.1-sol").click();
      await until(() => ctl.release);
      ctl.release();
      await page.waitForFunction(() => document.querySelector(".rt-gedit .fbrow .rt-mtest .tdot.bad"));
      assert.equal(await btn("gpt-6.1-sol").locator(".tdot.bad").count(), 1);
      if (lang === "en") {
        assert.match(await btn("gpt-6.1-sol").getAttribute("title"), /OpenAI · 401 · Invalid API key/);
        assert.match(await status.textContent(), /gpt-6\.1-sol didn't answer via OpenAI: 401 · Invalid API key/);
      }
      assert.equal(await at(), before, "the answer moved nothing on the page");
      assert.deepEqual(ctl.calls, [{ id: "oa", test: ["gpt-6.1-sol"] }]);

      // queued at its vendor: neither a pass nor a failure
      ctl.calls.length = 0;
      ctl.reply = { ok: false, protocol: "chat", model: "glm-5", queued: "3 ahead", ms: 40 };
      await btn("glm-5").click();
      await until(() => ctl.release);
      ctl.release();
      ctl.release = null;
      await page.waitForFunction(() => document.querySelector(".rt-gedit .fbrow .rt-mtest .tdot.queued"));
      assert.equal(await btn("glm-5").locator(".tdot.queued").count(), 1);
      assert.equal(await btn("gpt-6.1-sol").locator(".tdot.bad").count(), 1, "each row keeps its own answer");
      if (lang === "en") assert.match(await status.textContent(), /glm-5: Queued at its vendor, not answered: 3 ahead/);
      assert.deepEqual(ctl.calls, [{ id: "rl", test: ["glm-5"] }]);

      // the keyboard: Enter on a focused Test runs it and keeps the focus
      // there through the redraws
      ctl.calls.length = 0;
      ctl.reply = { ok: true, protocol: "chat", model: "glm-5", ms: 50 };
      await btn("glm-5").focus();
      await page.keyboard.press("Enter");
      await until(() => ctl.release);
      ctl.release();
      ctl.release = null;
      await page.waitForFunction(() => document.querySelector(".rt-gedit .fbrow .rt-mtest .tdot.ok"));
      assert.equal(await page.evaluate(() => document.activeElement?.dataset?.test), "rl/glm-5");
      assert.equal(await at(), before, "the keyboard's test moved nothing either");

      // its answer is kept while the group is edited: a reordering redraws
      // the rows and the dot stays on its model
      await row("gpt-6.1-sol").locator("button", { hasText: w.down }).click();
      assert.equal(await btn("gpt-6.1-sol").locator(".tdot.bad").count(), 1);

      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-member-test-wide.png`) });

      // narrow: the row keeps its Test in sight and the page doesn't scroll
      // sideways
      await page.setViewportSize({ width: 420, height: 1800 });
      assert.equal(await btn("gpt-6.1-sol").isVisible(), true);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, "no sideways scroll");

      const missing = await page.evaluate(() => [
        "A group in the group: open {name}'s card to test its models there",
        "Test {name}", "No answer came back",
      ].filter((k) => !I18N.zh[k] || !I18N["zh-TW"][k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has each language");
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine} en: a member of a group not saved yet is tested too`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 1800 }, reducedMotion: "reduce" })).newPage();
    t.after(() => browser.close());
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [], ctl = { calls: [], release: null, reply: { ok: true, protocol: "chat", model: "gpt-6.1-sol", ms: 120 } };
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en", ctl));
    // a group made from a model (Providers' Make a routing group of it)
    await page.goto("http://magpie.test/?view=groups&newgroup=" + encodeURIComponent("oa/gpt-6.1-sol"));
    const ed = page.locator("#view-groups:not([hidden]) .rt-gedit");
    await ed.waitFor();
    const b = ed.locator(".fbrow", { has: page.locator(".n", { hasText: "gpt-6.1-sol" }) }).locator("button.rt-mtest");
    assert.equal(await b.getAttribute("aria-disabled"), null);
    await b.click();
    await until(() => ctl.release);
    ctl.release();
    ctl.release = null;
    await page.waitForFunction(() => document.querySelector(".rt-gedit .fbrow .rt-mtest .tdot.ok"));
    assert.deepEqual(ctl.calls, [{ id: "oa", test: ["gpt-6.1-sol"] }]);
    assert.deepEqual(errors, []);
  });
  // a member pinned to an account is tested on that account (its stable id,
  // as the group keeps it), not on whichever of the provider's is next
  test(`${engine} en: a member on an account of its own is tested on it`, async (t) => {
    const { page, ctl, errors } = await openGroup(engine, "en", 1100, t);
    const card = page.locator('.rt-group[data-id="mine"]');
    await card.waitFor();
    await card.locator("button", { hasText: "Edit" }).click();
    const ed = page.locator(".rt-gedit");
    const b = ed.locator(`.rt-mtest[data-test="oa/gpt-6.1-sol#${A}"]`);
    await b.waitFor();
    ctl.reply = { ok: true, protocol: "chat", model: "gpt-6.1-sol", ms: 90 };
    await b.click();
    await until(() => ctl.release);
    ctl.release();
    ctl.release = null;
    await page.waitForFunction(() => document.querySelector(".rt-gedit .fbrow .rt-mtest .tdot.ok"));
    assert.deepEqual(ctl.calls, [{ id: "oa", test: ["gpt-6.1-sol"], account: A }]);
    assert.deepEqual(errors, []);
  });

  // the row's fit: with an answer's dot on every Test and the weight field
  // (the widest a row gets), no control sticks out of its row, no word is
  // cut, and the page doesn't scroll sideways, at the window's least width
  // and a phone's, in every language
  for (const lang of Object.keys(editWord)) {
    for (const [web, width] of [[false, 560], [true, 420], [true, 360]]) {
      test(`${engine} ${lang} ${web ? "web" : "app"} ${width}px: a member's Test fits its row`, async (t) => {
        const { page, ctl, errors } = await openGroup(engine, lang, width, t, { web, routing: "weight" });
        const card = page.locator('.rt-group[data-id="sol"]');
        await card.waitFor();
        await card.locator("button", { hasText: editWord[lang] }).first().click();
        const ed = page.locator(".rt-gedit");
        for (const [name, reply] of [["oa/gpt-6.1-sol", { ok: false, protocol: "chat", status: 401, error: "Invalid API key", ms: 9 }], ["rl/glm-5", { ok: true, protocol: "chat", ms: 9 }]]) {
          ctl.reply = reply;
          await ed.locator(`.rt-mtest[data-test="${name}"]`).click();
          await until(() => ctl.release);
          ctl.release();
          ctl.release = null;
          await page.waitForFunction((n) => document.querySelector(`.rt-gedit .rt-mtest[data-test="${n}"] .tdot:not(.wait)`), name);
        }
        const bad = await page.evaluate(() => {
          const out = [];
          for (const row of document.querySelectorAll(".rt-gedit .fbl > .fbrow")) {
            const r = row.getBoundingClientRect();
            if (row.scrollWidth > row.clientWidth + 1) out.push("row scrolls: " + row.textContent);
            for (const c of row.querySelectorAll(":scope > button, :scope > input")) {
              const b = c.getBoundingClientRect();
              if (b.width && (b.left < r.left - 1 || b.right > r.right + 1)) out.push("sticks out: " + (c.textContent || c.className));
            }
            for (const c of row.querySelectorAll(".rt-mtest")) if (c.scrollWidth > c.clientWidth + 1) out.push("cut: " + c.textContent);
          }
          if (document.documentElement.scrollWidth > document.documentElement.clientWidth) out.push("page scrolls sideways");
          return out;
        });
        assert.deepEqual(bad, []);
        if (process.env.ARTIFACT_DIR) await ed.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-group-member-test-fit.png`) });
        assert.deepEqual(errors, []);
      });
    }
  }
}
