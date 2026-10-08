// Run with Node's test runner and Playwright on the module path; see README.md.
// A subscription account can be used up to a share of each usage window
// only — a new Claude account kept to 70% of its five hours and its week,
// asked on Discord to lower the risk of a ban. Its row has a pill: a capped
// one says its cap, always shown; one without says No cap, on hover. A
// click opens the app's own menu of shares (no native select): a share, No
// cap, or Other… for one typed in place, each posted to provider/accountcap
// with the account and the cap (0: none). Each meter the cap counts has a
// mark where it stands, and an account at it says so with when it is back.
// A click moves nothing and the page stays where it is. In English and
// Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const inHours = (h) => new Date(Date.now() + h * 3600e3 + 60e3).toISOString();
const fresh = () => ({
  codex: {
    id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
    models: [{ id: "gpt-6", name: "", on: true }],
    agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
    account: {
      agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS",
      logins: [
        { user: "me@example.com", plan: "PLUS", active: true, on: true },
        { user: "Spare@Example.com", plan: "PLUS", on: true },
      ],
    },
    accountCaps: { "me@example.com": 70 },
  },
});
// me is at 75% of its five hours, past its 70% cap; the spare at 20%
const usage = () => ({
  "me@example.com": { provider: "codex", windows: [
    { name: "5-hour", used: 75, resetsAt: inHours(3), capped: true },
    { name: "Weekly", used: 40, resetsAt: inHours(100), capped: true },
  ] },
  "Spare@Example.com": { provider: "codex", windows: [
    { name: "5-hour", used: 20, resetsAt: inHours(4), capped: true },
    { name: "Weekly", used: 10, resetsAt: inHours(120), capped: true },
  ] },
});

// me's five hours and week are under its cap, but its Opus week, which
// counts Opus alone, is past it: the account is held for Opus only (#760)
const opusUsage = () => ({
  "me@example.com": { provider: "codex", windows: [
    { name: "5-hour", used: 40, resetsAt: inHours(1), capped: true },
    { name: "Weekly", used: 50, resetsAt: inHours(100), capped: true },
    { name: "Opus week", used: 90, resetsAt: inHours(3), capped: true, capsSome: true },
  ] },
  "Spare@Example.com": usage()["Spare@Example.com"],
});

function serve(lang, posts, use = usage, alone = false) {
  const ps = fresh();
  // the spare off: Codex signed in to me has nowhere to be moved to
  if (alone) ps.codex.account.logins[1].on = false;
  const providers = { providers: [ps.codex], presets: [], excluded: [], gateway: { running: true, window: true } };
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
    if (url.pathname === "/api/login/usage") return json(url.searchParams.get("agent") === "codex" ? use() : {});
    if (url.pathname === "/api/provider/accountcap" && route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      posts.push(body);
      const p = providers.providers.find((x) => x.id === body.id);
      const caps = { ...(p.accountCaps || {}) };
      if (body.cap) caps[body.account.toLowerCase()] = body.cap;
      else delete caps[body.account.toLowerCase()];
      p.accountCaps = caps;
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { direct: "At its cap · Codex still uses it", directWhy: /refused with a usage-cap error until it renews.*\nCodex is signed in to this account and asks its vendor itself, not through magpie/s, opus: "Opus week at its cap · back in 3h", opusWhy: /^Opus week is at 90%, past this account's 70% cap, so magpie sends the requests it counts to the other accounts until it renews; other models still use this account/, none: "No cap", cap: (n) => `Cap ${n}%`, held: "At its cap · back in 3h", other: "Other…", menu: "Usage cap" },
  zh: { direct: "已达上限 · Codex 仍在使用", directWhy: /经 magpie 的请求会返回.*\nCodex 登录此账号并直接向厂商发请求/s, opus: "Opus week已达上限 · 3 小时后恢复", opusWhy: /^Opus week已用 90%，超过该账号的 70% 上限.*其他模型仍用此账号/, none: "不设上限", cap: (n) => `上限 ${n}%`, held: "已达上限 · 3 小时后恢复", other: "其他…", menu: "用量上限" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, name, use, alone) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-account-cap.png`) });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts, use, alone));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Codex" }).first().click();
      await page.locator(".editor .accts .acc .aq-w").first().waitFor();
      return { page, errors, posts };
    };
    const row = (page, id) => page.locator(`.editor .accts .acc[data-account-id="${id}"]`);
    const pill = (page, id) => row(page, id).locator(".acap");
    const menu = (page) => page.locator(".proto-menu");
    const item = (page, name) => menu(page).locator(".pm-item", { has: page.locator(".pm-name", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
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

    test(`${engine} ${lang}: an account's usage cap is shown, set from the app's menu, typed, and lifted`, async (t) => {
      const { page, errors, posts } = await open(t, "codex");
      // me is capped at 70% and at 75%: its pill, the note and a mark on each meter
      assert.equal(await pill(page, "me@example.com").textContent(), w.cap(70));
      assert.equal(await pill(page, "me@example.com").evaluate((e) => getComputedStyle(e).opacity), "1");
      assert.equal(await row(page, "me@example.com").locator(".acap-held").textContent(), w.held);
      assert.deepEqual(await row(page, "me@example.com").locator(".aq-cap").evaluateAll((ms) => ms.map((m) => m.style.left)), ["70%", "70%"]);
      // the spare has none: No cap on hover only, no note, no mark
      assert.equal(await pill(page, "Spare@Example.com").textContent(), w.none);
      await page.mouse.move(0, 0);
      await page.waitForTimeout(250);
      assert.equal(await pill(page, "Spare@Example.com").evaluate((e) => getComputedStyle(e).opacity), "0");
      assert.equal(await row(page, "Spare@Example.com").locator(".acap-held, .aq-cap").count(), 0);

      // the spare's pill opens the app's menu; 80% is posted
      await row(page, "Spare@Example.com").hover();
      await still(page, "Spare@Example.com", pill(page, "Spare@Example.com"));
      await menu(page).waitFor();
      assert.equal(await menu(page).locator(".pm-head").textContent(), w.menu);
      assert.deepEqual(await menu(page).locator(".pm-name").allTextContents(), [w.none, "50%", "60%", "70%", "80%", "90%", w.other]);
      assert.equal(await page.locator("select").count(), 0, "no native select");
      let n = posts.length;
      await item(page, "80%").click();
      assert.deepEqual(await posted(posts, n), { id: "codex", account: "Spare@Example.com", cap: 80 });
      await page.waitForFunction((want) => document.querySelector('.editor .acc[data-account-id="Spare@Example.com"] .acap')?.textContent === want, w.cap(80));

      // Other… types one in place: 65
      await still(page, "Spare@Example.com", pill(page, "Spare@Example.com"));
      await item(page, w.other).click();
      const typed = row(page, "Spare@Example.com").locator("input.acap-in");
      await typed.waitFor();
      await typed.fill("65");
      n = posts.length;
      await typed.press("Enter");
      assert.deepEqual(await posted(posts, n), { id: "codex", account: "Spare@Example.com", cap: 65 });
      await page.waitForFunction((want) => document.querySelector('.editor .acc[data-account-id="Spare@Example.com"] .acap')?.textContent === want, w.cap(65));

      // No cap lifts me's: the note and the marks go
      await still(page, "me@example.com", pill(page, "me@example.com"));
      n = posts.length;
      await item(page, w.none).click();
      assert.deepEqual(await posted(posts, n), { id: "codex", account: "me@example.com", cap: 0 });
      await page.waitForFunction((want) => document.querySelector('.editor .acc[data-account-id="me@example.com"] .acap')?.textContent === want, w.none);
      assert.equal(await row(page, "me@example.com").locator(".acap-held, .aq-cap").count(), 0);

      const border = await page.evaluate(() => [...document.querySelectorAll(".accts .acap, .accts .acap-held, .accts .aq-cap, .proto-menu, .proto-menu *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no border stripes");
      const missing = await page.evaluate(() => [
        "No cap", "Cap {n}%", "Usage cap", "Usage cap {n}%", "Usage cap, in percent", "Use each window to 100%", "Other…",
        "A share of your own, 1–99%", "A cap is a share from 1% to 99%", "At its cap", "At its cap · back {in}",
        "A usage window is at {n}%, past this account's {cap}% cap, so magpie counts it as used up and sends requests to the other accounts until that window renews",
        "With no other account on, requests are refused with a usage-cap error until then",
        "Stops at {n}% of each usage window: once magpie reads a window at {n}% or past it, it counts this account as used up and sends it nothing more until the window renews. A turn already under way can still take it past {n}%, so the cap doesn't promise the rest is left. Click to change",
        "Used to 100% of its usage windows. Click to cap it at a share of each, so magpie goes on to the other accounts past it",
        "{who} stops at {n}% of each window", "{who} has no usage cap",
        "held at its {cap}% usage cap",
        "{who} is left out: a usage window is at {n}, past the {cap}% cap set on the account, so it counts as used up until that window renews.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    // 𝕏 on Discord: a Claude account capped at 90% ran its five hours to
    // 100%, while its row said it was at its cap. Claude Code or Codex
    // signed in to it asks the vendor itself; with no other account on to
    // move it to, the cap stops only what goes through magpie, and the row
    // says so, as the pill does before the cap is reached
    test(`${engine} ${lang}: the only account on, at its cap, says the agent signed in to it still uses it`, async (t) => {
      const { page, errors } = await open(t, "alone", usage, true);
      const note = row(page, "me@example.com").locator(".acap-held");
      assert.equal(await note.textContent(), w.direct);
      assert.match(await note.getAttribute("title"), w.directWhy);
      assert.match(await pill(page, "me@example.com").getAttribute("title"), w.directWhy.source.includes("Codex is") ? /\n\nCodex is signed in to this account/ : /\n\nCodex 登录此账号/);
      assert.notEqual(await note.evaluate((e) => getComputedStyle(e).color), await row(page, "me@example.com").locator(".n").evaluate((e) => getComputedStyle(e).color), "a warning, not plain text");
      // the spare, off, has no pill note of its own
      assert.equal(await row(page, "Spare@Example.com").locator(".acap-held").count(), 0);
      const missing = await page.evaluate(() => [
        "At its cap · {agent} still uses it",
        "A usage window is at {n}%, past this account's {cap}% cap: requests through magpie are refused with a usage-cap error until it renews",
        "{agent} is signed in to this account and asks its vendor itself, not through magpie, so with no other account on to move it to, {agent} goes on using it past the cap. Add another account, or pick {agent}'s models via magpie, for the cap to hold it",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: with another account on, the pill and the note don't say the agent goes past the cap`, async (t) => {
      const { page } = await open(t, "codex");
      assert.equal(await row(page, "me@example.com").locator(".acap-direct").count(), 0);
      assert.doesNotMatch(await pill(page, "me@example.com").getAttribute("title"), /Codex is signed in|Codex 登录此账号/);
    });

    test(`${engine} ${lang}: a window of one model past the cap holds the account for that model, and says so`, async (t) => {
      const { page, errors } = await open(t, "opus", opusUsage);
      const note = row(page, "me@example.com").locator(".acap-held");
      assert.equal(await note.textContent(), w.opus);
      assert.match(await note.getAttribute("title"), w.opusWhy);
      const missing = await page.evaluate(() => [
        "{names} at its cap", "{names} at its cap · back {in}",
        "{names} is at {n}%, past this account's {cap}% cap, so magpie sends the requests it counts to the other accounts until it renews; other models still use this account",
        "With no other account on, those requests are refused with a usage-cap error until then",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
