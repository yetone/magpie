// Run with Node's test runner and Playwright on the module path; see README.md.
// young02036 on Discord: Fetch models ticks, for each key, the models its own
// list gained, and leaves the ones the user un-ticked alone. A key's model
// picker now has Select all available, which ticks every model the key's own
// fetched list has (provider/keylisted), the un-ticked ones too, since the
// user asks it; models the list lacks stay as they are, and nothing is saved
// until Save. A plugin's account ticks the models its plugin lists for it
// (young02036: a ZCode account with only GLM-5.3-Flash got the other
// account's models). A key no list marks, and an account with no list of
// its own, tick every model shown. In both engines, every language, at a
// narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fresh = () => ({
  codex: {
    id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
    models: [{ id: "gpt-6", name: "", on: true }, { id: "gpt-6-mini", name: "", on: true }, { id: "gpt-5", name: "", on: false }],
    agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
    account: {
      agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS",
      logins: [
        { user: "me@example.com", plan: "PLUS", active: true, on: true },
        { user: "spare@example.com", plan: "PLUS", on: true },
      ],
    },
    accountModels: { "spare@example.com": ["gpt-6-mini"] },
  },
  zcode: {
    id: "zcode", name: "ZCode", icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
    models: [{ id: "GLM-5.3-Flash", name: "", on: true }, { id: "GLM-5.2", name: "", on: true }, { id: "GLM-5-Turbo", name: "", on: true }, { id: "GLM-5.3-Flash-Trial", name: "", on: true }],
    agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
    account: {
      agent: "zcode", agentName: "ZCode", user: "paid@example.com", plan: "Coding Plan",
      logins: [
        { user: "paid@example.com", plan: "Coding Plan", active: true, on: true },
        { user: "free@example.com", plan: "Start Plan", on: true },
      ],
    },
    accountModels: { "free@example.com": ["GLM-5.2"] },
  },
  relay: {
    id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
    models: [{ id: "big", name: "", on: true }, { id: "cheap", name: "", on: true }, { id: "mid", name: "", on: true }, { id: "off", name: "", on: false }],
    agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
    keyList: [{ id: "aaaaaaaaaa", name: "Team", masked: "sk-…one", on: true, active: true }, { id: "bbbbbbbbbb", name: "", masked: "sk-…two", on: true }],
    // the user un-ticked cheap for the second key, which its group lists
    accountModels: { "bbbbbbbbbb": ["big"] },
  },
});
// each key's own fetched list: the second key's group has big, cheap and a
// model the provider doesn't serve agents; the first key's is unknown
const listed = { bbbbbbbbbb: { models: ["big", "cheap", "off"], known: true }, aaaaaaaaaa: { models: null, known: false },
  // what the ZCode plugin lists for its gift-only account
  "free@example.com": { models: ["GLM-5.3-Flash"], known: true } };

function serve(lang, posts) {
  const ps = fresh();
  const providers = { providers: [ps.codex, ps.relay, ps.zcode], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/provider/keylisted" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push({ asked: body.account, id: body.id });
      return json(listed[body.account] || { models: null, known: false });
    }
    if (url.pathname === "/api/provider/accountmodels" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push(body);
      const p = providers.providers.find((x) => x.id === body.id);
      const am = { ...(p.accountModels || {}) };
      if (body.allow.length) am[body.account.toLowerCase()] = body.allow;
      else delete am[body.account.toLowerCase()];
      p.accountModels = am;
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const label = { en: "Select all available", zh: "全选可用模型", "zh-TW": "全選可用模型", ja: "利用可能なモデルをすべて選択", de: "Alle verfügbaren auswählen" };
const langs = process.env.GUI_LANG ? [process.env.GUI_LANG] : Object.keys(label);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of langs) {
    for (const width of [900, 420]) {
      const open = async (t, name, wait) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name}-available.png`) });
          }
          await browser.close();
        });
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: name }).first().click();
        await page.locator(wait).first().waitFor();
        return { page, errors, posts };
      };
      const row = (page, id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
      const pill = (page, id) => row(page, id).locator(".amodels");
      const panel = (page, id) => row(page, id).locator(".acct-models");
      const chips = (page, id) => panel(page, id).locator(".mchip").evaluateAll((cs) => cs.map((c) => `${c.textContent}${c.classList.contains("on") ? "*" : ""}`));
      const avail = (page, id) => panel(page, id).getByRole("button", { name: label[lang], exact: true });
      const reveal = async (page, id) => {
        // a key's badge shows on hover (#841), an account's under its ⋯
        if (await pill(page, id).isVisible()) return;
        await row(page, id).hover();
        if (await pill(page, id).isVisible()) return;
        await row(page, id).locator(".acc-more").click();
      };
      const until = async (f) => {
        for (let i = 0; i < 60; i++) { if (await f()) return true; await new Promise((r) => setTimeout(r, 50)); }
        return false;
      };
      // the button's words fit on its line, and nothing runs off the page
      const fits = async (page, id) => {
        const b = await avail(page, id).evaluate((e) => ({ over: e.scrollWidth > e.clientWidth + 1, right: e.getBoundingClientRect().right, vw: document.documentElement.clientWidth }));
        assert(!b.over, "the button's words are cut");
        assert(b.right <= b.vw + 1, `the button runs off the page (${b.right} > ${b.vw})`);
      };
      const saveOf = (page, id) => panel(page, id).locator(".acm-foot button.primary");

      test(`${engine} ${lang} ${width}px: a key's Select all available ticks its own list, un-ticked ones too`, async (t) => {
        const { page, errors, posts } = await open(t, "Relay", ".editor .accts .acc");
        await pill(page, "bbbbbbbbbb").click();
        assert.deepEqual(await chips(page, "bbbbbbbbbb"), ["big*", "cheap", "mid"]);
        await fits(page, "bbbbbbbbbb");
        const title = await avail(page, "bbbbbbbbbb").getAttribute("title");
        assert(title, "the button says what it does");
        await avail(page, "bbbbbbbbbb").click();
        assert(await until(async () => (await chips(page, "bbbbbbbbbb")).join() === "big*,cheap*,mid"), `chips after: ${await chips(page, "bbbbbbbbbb")}`);
        assert.deepEqual(posts.filter((x) => x.asked), [{ asked: "bbbbbbbbbb", id: "relay" }]);
        // nothing saved till Save, which keeps the picks
        assert.equal(posts.filter((x) => x.allow).length, 0, "Select all available saved by itself");
        await saveOf(page, "bbbbbbbbbb").click();
        assert(await until(() => posts.some((x) => x.allow)));
        assert.deepEqual(posts.find((x) => x.allow), { id: "relay", account: "bbbbbbbbbb", allow: ["big", "cheap"] });
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a key no list marks, and an account, tick every model shown`, async (t) => {
        const { page, errors } = await open(t, "Relay", ".editor .accts .acc");
        await reveal(page, "aaaaaaaaaa");
        await pill(page, "aaaaaaaaaa").click();
        assert.deepEqual(await chips(page, "aaaaaaaaaa"), ["big", "cheap", "mid"]);
        await avail(page, "aaaaaaaaaa").click();
        assert(await until(async () => (await chips(page, "aaaaaaaaaa")).join() === "big*,cheap*,mid*"), `chips after: ${await chips(page, "aaaaaaaaaa")}`);
        assert(await saveOf(page, "aaaaaaaaaa").isEnabled());
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a Codex account's Select all available ticks every model shown`, async (t) => {
        const { page, errors, posts } = await open(t, "Codex", ".editor .accts .acc .amodels.set");
        await pill(page, "spare@example.com").click();
        assert.deepEqual(await chips(page, "spare@example.com"), ["gpt-6", "gpt-6-mini*"]);
        await fits(page, "spare@example.com");
        await avail(page, "spare@example.com").click();
        assert(await until(async () => (await chips(page, "spare@example.com")).join() === "gpt-6*,gpt-6-mini*"));
        assert.deepEqual(posts.filter((x) => x.asked), [{ asked: "spare@example.com", id: "codex" }]);
        const missing = await page.evaluate(() => ["Select all available", "Ticks every model this key's own list has, un-ticked ones too", "Ticks every model this account's own list has, else every model shown"]
          .flatMap((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l]?.[k]).map((l) => `${l}: ${k}`)));
        assert.deepEqual(missing, [], "every string has its translations");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a plugin account's Select all available ticks the models its plugin lists for it`, async (t) => {
        const { page, errors, posts } = await open(t, "ZCode", ".editor .accts .acc .amodels.set");
        await pill(page, "free@example.com").click();
        assert.deepEqual(await chips(page, "free@example.com"), ["GLM-5.3-Flash", "GLM-5.2*", "GLM-5-Turbo", "GLM-5.3-Flash-Trial"]);
        await fits(page, "free@example.com");
        await avail(page, "free@example.com").click();
        assert(await until(async () => (await chips(page, "free@example.com")).join() === "GLM-5.3-Flash*,GLM-5.2*,GLM-5-Turbo,GLM-5.3-Flash-Trial"), `chips after: ${await chips(page, "free@example.com")}`);
        assert.deepEqual(posts.filter((x) => x.asked), [{ asked: "free@example.com", id: "zcode" }]);
        assert.equal(posts.filter((x) => x.allow).length, 0, "Select all available saved by itself");
        assert.deepEqual(errors, []);
      });
    }
  }
}
