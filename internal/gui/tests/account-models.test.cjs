// Run with Node's test runner and Playwright on the module path; see README.md.
// Each account of a subscription, and each key, can be kept for some of the
// provider's models only (#474: 增加账号级别的模型选项). Its row has a badge:
// one kept for some says how many, always shown; one serving every model
// says All models, on hover. A click opens the provider's models under the
// row as chips; Save posts provider/accountmodels with the account (or the
// key's id) and the models picked, All models posts none. A click moves
// nothing and the page stays where it is. A subscription with one account
// has no badge. In English and Chinese.
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
        { user: "Spare@Example.com", plan: "PLUS", on: true },
      ],
    },
    accountModels: { "spare@example.com": ["gpt-6-mini"] },
  },
  claude: {
    id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "",
    models: [{ id: "claude-opus-5", name: "", on: true }], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
    account: { agent: "claude", agentName: "Claude Code", user: "solo@example.com", plan: "MAX", logins: [{ user: "solo@example.com", plan: "MAX", active: true, on: true }] },
  },
  relay: {
    id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
    models: [{ id: "big", name: "", on: true }, { id: "cheap", name: "Cheap one", on: true }], agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
    keyList: [{ id: "aaaaaaaaaa", name: "Team", masked: "sk-…one", on: true, active: true }, { id: "bbbbbbbbbb", name: "", masked: "sk-…two", on: true }],
  },
});

function serve(lang, posts) {
  const ps = fresh();
  const providers = { providers: [ps.codex, ps.claude, ps.relay], presets: [], excluded: [], gateway: { running: true, window: true } };
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

const words = {
  en: { all: "All models", one: "1 model", two: "2 models", save: "Save", only: "Serves only gpt-6-mini" },
  zh: { all: "全部模型", one: "1 个模型", two: "2 个模型", save: "保存", only: "只用于 gpt-6-mini" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name, wait) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-account-models.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
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
    // what the page and its scrollers have scrolled
    const scrolled = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
    // a click that leaves the row where it was and scrolls nothing
    const still = async (page, id, b) => {
      await b.scrollIntoViewIfNeeded();
      await page.waitForTimeout(150);
      const before = await row(page, id).evaluate((e) => Math.round(e.getBoundingClientRect().top)), sc = await scrolled(page);
      await b.click();
      await page.waitForTimeout(200);
      assert.equal(await row(page, id).evaluate((e) => Math.round(e.getBoundingClientRect().top)), before, `${id}'s row moved`);
      assert.equal(await scrolled(page), sc, "a click scrolled");
    };
    const posted = async (posts, n) => {
      for (let i = 0; i < 60 && posts.length === n; i++) await new Promise((r) => setTimeout(r, 50));
      return posts.at(-1);
    };

    test(`${engine} ${lang}: a Codex account is kept for some models, and given all again`, async (t) => {
      const { page, errors, posts } = await open(t, "Codex", ".editor .accts .acc .amodels");
      // the one kept for gpt-6-mini says so, always; the other says All models on hover only
      assert.equal(await pill(page, "Spare@Example.com").textContent(), w.one);
      assert.equal(await pill(page, "Spare@Example.com").getAttribute("title"), w.only);
      assert.equal(await pill(page, "Spare@Example.com").evaluate((e) => getComputedStyle(e).opacity), "1");
      assert.equal(await pill(page, "me@example.com").textContent(), w.all);
      await page.mouse.move(0, 0);
      await page.waitForTimeout(250);
      assert.equal(await pill(page, "me@example.com").evaluate((e) => getComputedStyle(e).opacity), "0");

      // me: the provider's models it serves agents, none picked; Save waits for one
      await still(page, "me@example.com", pill(page, "me@example.com"));
      assert.deepEqual(await chips(page, "me@example.com"), ["gpt-6", "gpt-6-mini"]);
      const save = panel(page, "me@example.com").getByRole("button", { name: w.save, exact: true });
      assert(await save.isDisabled());
      await still(page, "me@example.com", panel(page, "me@example.com").locator(".mchip", { hasText: /^gpt-6$/ }));
      assert.deepEqual(await chips(page, "me@example.com"), ["gpt-6*", "gpt-6-mini"]);
      assert(await save.isEnabled());
      let n = posts.length;
      await save.click();
      assert.deepEqual(await posted(posts, n), { id: "codex", account: "me@example.com", allow: ["gpt-6"] });
      await panel(page, "me@example.com").waitFor({ state: "detached" });
      assert.equal(await pill(page, "me@example.com").textContent(), w.one);

      // the spare opens on what it has; All models gives it every one again
      await still(page, "Spare@Example.com", pill(page, "Spare@Example.com"));
      assert.deepEqual(await chips(page, "Spare@Example.com"), ["gpt-6", "gpt-6-mini*"]);
      n = posts.length;
      await panel(page, "Spare@Example.com").locator("button.action").click();
      assert.deepEqual(await posted(posts, n), { id: "codex", account: "Spare@Example.com", allow: [] });
      await page.locator(".editor .acct-models").waitFor({ state: "detached" });
      assert.equal(await pill(page, "Spare@Example.com").textContent(), w.all);

      const border = await page.evaluate(() => [...document.querySelectorAll(".accts .amodels, .acct-models, .acct-models *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      const missing = await page.evaluate(() => [
        "Serves only {models}",
        "Every model of the provider goes to this account. Click to keep it for some only",
        "Every model of the provider goes to this key. Click to keep it for some only",
        "Pick the provider's models first.",
        "{who} serves only {models}",
        "{who} serves every model again",
        "Every model of the provider, as an account without a list of its own",
        "Only the models picked go to this account; the others go to the provider's other accounts.",
        "Only the models picked go to this key; the others go to the provider's other keys.",
        "set to serve other models, not {model}",
        "{who} is left out: it is set to serve other models, not {model}.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a key is kept for some models by its id`, async (t) => {
      const { page, errors, posts } = await open(t, "Relay", ".editor .accts .acc .amodels");
      assert.equal(await pill(page, "aaaaaaaaaa").textContent(), w.all);
      await still(page, "aaaaaaaaaa", pill(page, "aaaaaaaaaa"));
      assert.deepEqual(await chips(page, "aaaaaaaaaa"), ["big", "Cheap one"], "a model's own name, as the provider's chips");
      await panel(page, "aaaaaaaaaa").locator(".mchip", { hasText: "Cheap one" }).click();
      await panel(page, "aaaaaaaaaa").locator(".mchip", { hasText: "big" }).click();
      const n = posts.length;
      await panel(page, "aaaaaaaaaa").getByRole("button", { name: w.save, exact: true }).click();
      assert.deepEqual(await posted(posts, n), { id: "relay", account: "aaaaaaaaaa", allow: ["cheap", "big"] });
      await page.locator(".editor .acct-models").waitFor({ state: "detached" });
      assert.equal(await pill(page, "aaaaaaaaaa").textContent(), w.two);
      assert.equal(await pill(page, "bbbbbbbbbb").textContent(), w.all);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a subscription with one account has no models badge`, async (t) => {
      const { page, errors } = await open(t, "Claude", ".editor .accts .acc");
      assert.equal(await page.locator(".editor .accts .amodels").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
