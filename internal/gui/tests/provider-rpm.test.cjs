// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's Requests per minute (coeo91 on Discord: OpenRouter's free
// models take 20 a minute, which Concurrency can't keep to): its editor
// has the field under Concurrency, empty for no limit; Save posts the
// number typed, 0 when it is empty, and refuses one that isn't a whole
// number from 0 to 10000 before anything is posted. A signed-in account
// (Codex) opens on what it has and saves it with its picks. At the
// narrowest window the label stays on one line and the field keeps its
// placeholder readable. In English, Chinese, Japanese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const openrouter = {
  id: "openrouter", name: "OpenRouter", icon: "generic", chat: "https://openrouter.ai/api/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "x/free", name: "Free", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", maxConcurrency: null,
};
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "", maxConcurrency: null, maxRPM: 20,
};

function serve(lang, posts) {
  const providers = { providers: [openrouter, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { label: "Requests per minute", save: "Save", none: "No limit", bad: "Requests per minute: a whole number from 0 to 10000" },
  zh: { label: "每分钟请求数", save: "保存", none: "不限制", bad: "每分钟请求数：0 到 10000 的整数" },
  ja: { label: "1 分間のリクエスト数", bad: "1 分間のリクエスト数：0〜10000 の整数" },
  de: { label: "Anfragen pro Minute", bad: "Anfragen pro Minute: eine ganze Zahl von 0 bis 10000" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = words[lang];
    const open = async (t, name, width = 900) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-${width}-rpm.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: name }).click();
      await page.locator(".editor input.rpm").waitFor();
      await page.waitForTimeout(300); // the editor drawn again as what it asked for comes in
      return { page, errors, posts };
    };
    const box = (page) => page.locator(".editor input.rpm");
    const saveName = async (page) => page.evaluate(() => t("Save"));
    const save = async (page, posts) => {
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: await saveName(page), exact: true }).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.at(-1);
    };

    test(`${engine} ${lang}: the field saves a whole number, 0 when empty, and refuses others`, async (t) => {
      const { page, errors, posts } = await open(t, "OpenRouter");
      const label = await box(page).evaluate((e) => e.closest(".editor").querySelector(`label[for="${e.id}"]`)?.textContent || e.parentElement.previousElementSibling?.textContent || "");
      assert.equal(label.trim(), w.label);
      assert.equal(await box(page).inputValue(), "");
      assert.equal(await box(page).getAttribute("placeholder"), await page.evaluate(() => t("No limit")));
      // under Concurrency
      const order = await page.evaluate(() => {
        const c = document.querySelector(".editor input.concurrency"), r = document.querySelector(".editor input.rpm");
        return !!(c.compareDocumentPosition(r) & Node.DOCUMENT_POSITION_FOLLOWING);
      });
      assert(order, "after Concurrency");

      for (const bad of ["2.5", "10001"]) {
        await box(page).fill(bad);
        await page.locator(".editor .bar").getByRole("button", { name: await saveName(page), exact: true }).click();
        await page.locator(".editor .editor-error").waitFor();
        assert.equal(await page.locator(".editor .editor-error").textContent(), w.bad);
        assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("rpm")), true, "the field is focused");
        assert.equal(posts.length, 0);
      }

      await box(page).fill("20");
      let saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "openrouter");
      assert.equal(saved.body.maxRPM, 20);
      assert.equal(saved.body.queueLimit, 0, "the queue goes with it as before");

      await page.locator(".row.provider", { hasText: "OpenRouter" }).click();
      await box(page).fill("");
      saved = await save(page, posts);
      assert(Object.hasOwn(saved.body, "maxRPM"), "an empty field is saved as no limit, not left out");
      assert.equal(saved.body.maxRPM, 0);

      const missing = await page.evaluate(() => [
        "Requests per minute", "Requests per minute: a whole number from 0 to 10000",
        "How many requests each key or account sends in any minute, retries included; one more waits for room, up to 2 minutes. 0 or empty is no limit",
      ].flatMap((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l][k]).map((l) => `${l}: ${k}`)));
      assert.deepEqual(missing, [], "every string has its translations");
      const stripes = await page.evaluate(() => [...document.querySelectorAll(".editor input.rpm, .editor input.rpm ~ *")].map((e) => getComputedStyle(e)).filter((s) => parseFloat(s.borderLeftWidth) > parseFloat(s.borderRightWidth) || (parseFloat(s.borderLeftWidth) > 0 && s.borderLeftColor !== s.borderRightColor)).length);
      assert.equal(stripes, 0, "no border stripes");
      assert.equal(await page.locator(".editor select").count(), 0, "no native select");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a signed-in account's limit opens as it is and is saved with its picks`, async (t) => {
      const { page, errors, posts } = await open(t, "Codex");
      assert.equal(await box(page).inputValue(), "20");
      await box(page).fill("12");
      const saved = await save(page, posts);
      assert.equal(saved.body.id, "codex");
      assert.equal(saved.body.maxRPM, 12);
      assert.deepEqual(saved.body.models, ["gpt-6"], "its picks go with it");
      assert.deepEqual(errors, []);
    });

    for (const width of [360, 440]) {
      test(`${engine} ${lang} ${width}px: the label is on one line, styled like Concurrency, nothing runs off the page`, async (t) => {
        const { page, errors } = await open(t, "OpenRouter", width);
        const look = (e) => { const s = getComputedStyle(e); return { bg: s.backgroundColor, border: s.borderTopColor, radius: s.borderTopLeftRadius, height: s.height }; };
        assert.deepEqual(await box(page).evaluate(look), await page.locator(".editor input.concurrency").evaluate(look), "styled as Concurrency");
        const rpmLabel = page.locator(".editor label", { hasText: w.label });
        const ccLabel = page.locator(".editor label", { hasText: await page.evaluate(() => t("Concurrency")) });
        const [h, one] = [await rpmLabel.evaluate((e) => e.getBoundingClientRect().height), await ccLabel.evaluate((e) => e.getBoundingClientRect().height)];
        assert(h <= one + 1, `the label takes ${h}px, Concurrency's ${one}px`);
        const fit = await box(page).evaluate((e) => {
          const r = e.getBoundingClientRect();
          return { right: r.right, width: r.width, page: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth };
        });
        assert(fit.right <= fit.page + 0.5, `the field runs to ${fit.right}, the page is ${fit.page}`);
        assert(fit.width >= 80, `the field is ${fit.width}px`);
        assert(fit.scroll <= fit.page + 0.5, `the page scrolls sideways: ${fit.scroll} > ${fit.page}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
