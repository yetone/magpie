// Run with Node's test runner and Playwright on the module path; see README.md.
// Which Codex resets are used by themselves, by the days they have left
// before they expire (shangyuan0818 on X: 0 to 7, only those running out
// within the week): a pair of fields under "How early before one expires"
// in Codex's provider editor, for every account. Both empty is every one,
// the default. Enter or leaving the pair posts settings/codex-reset-days
// with both; moving from one field to the other posts nothing yet, so
// 3–7 to 10–20 is never 10–7 on the way; Escape puts a field back; a pair
// out of order or range is said and not posted. An account's card then
// says "not auto-used" for the reset that runs out first when it is out
// of those days, with why in its title. Five languages, 1100 and 420
// wide, both engines; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const scrolls = (page) => page.evaluate(() => [scrollY, Math.round(document.querySelector("#codexResetDaysRow .name").getBoundingClientRect().top)].join(","));

const assets = path.resolve(__dirname, "../assets");

function server(lang, users, posts, days) {
  const fixed = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", dock: false, dockWindow: false, proxy: "",
    redact: false, redactPersonal: false, redactWords: [], codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "", version: "0.1.900", dir: "~/.config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    codexUsers: users, codexAutoReset: users.slice(0, 1).map((u) => u.toLowerCase()), codexAutoResetOnly: {},
    ...days,
  };
  let cur = fixed;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: fixed.fx });
    if (url.pathname === "/api/settings/codex-reset-days") {
      const body = req.postDataJSON();
      posts.push(body);
      cur = { ...cur, codexResetDaysLeast: body.least, codexResetDaysMost: body.most };
      return json(cur);
    }
    if (url.pathname === "/api/settings/codex-auto-reset") {
      const body = req.postDataJSON();
      posts.push(body);
      // as the settings keep it: on, and narrowed to one time or not
      const u = body.user.toLowerCase(), only = { ...cur.codexAutoResetOnly };
      const was = cur.codexAutoReset.includes(u) ? { week: only[u] !== "expiring", expiring: only[u] !== "week" } : { week: false, expiring: false };
      const w = body.when ? { ...was, [body.when]: body.on } : { week: body.on, expiring: body.on };
      const on = cur.codexAutoReset.filter((x) => x !== u);
      delete only[u];
      if (w.week || w.expiring) on.push(u);
      if (w.week !== w.expiring) only[u] = w.week ? "week" : "expiring";
      cur = { ...cur, codexAutoReset: on, codexAutoResetOnly: only };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") cur = { ...cur, ...req.postDataJSON() };
      return json(cur);
    }
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["codex"], { codex: "Codex" }));
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}


const langs = ["en", "zh", "zh-TW", "ja", "de"];
const users = ["alice@example.com", "a-much-longer-team-seat-address@some-company.example.org"];
const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });

async function open(t, engine, lang, width, days, posts) {
  const browser = await launch(engine);
  const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-codex-reset-days.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
  await page.route("**/*", server(lang, users, posts, days));
  await watchSettingsReads(page);
  await openProvPrefs(page, "codex");
  await settingsDrawn(page);
  await page.locator(".editor .prov-prefs #codexResetDaysRow").waitFor();
  return { page, errors };
}

const tr = (page, lang, key) => page.evaluate(([lang, key]) => I18N[lang]?.[key] || key, [lang, key]);
const values = (page) => page.evaluate(() => [...document.querySelectorAll("#codexResetDaysRow input")].map((i) => i.value));
const posted = async (page, posts, n) => { for (let i = 0; i < 100 && posts.length < n; i++) await page.waitForTimeout(20); };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of langs) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Codex's editor sets the days a reset used by itself may have left`, async (t) => {
        const posts = [];
        const { page, errors } = await open(t, engine, lang, width, {}, posts);
        const row = page.locator("#codexResetDaysRow");
        assert.equal(await row.isVisible(), true);
        if (lang !== "en") {
          assert.notEqual((await row.locator(".name").textContent()).trim(), "Days left for one used by itself", "name translated");
          assert.notEqual((await row.locator(".sub").textContent()).trim().slice(0, 20), "Only a reset with th", "words translated");
        }
        // after the lead, under the accounts' switches
        assert.equal(await page.evaluate(() => !!(document.querySelector("#codexResetLeadRow").compareDocumentPosition(document.querySelector("#codexResetDaysRow")) & Node.DOCUMENT_POSITION_FOLLOWING)), true);
        const [least, most] = [row.locator("input").nth(0), row.locator("input").nth(1)];
        assert.deepEqual(await values(page), ["", ""], "unset is any");
        assert.equal(await least.getAttribute("placeholder"), "0");
        assert.equal(await most.getAttribute("placeholder"), "∞");
        assert.equal(await most.getAttribute("inputmode"), "numeric");
        assert((await most.getAttribute("aria-label")).includes(await tr(page, lang, "At most")));

        await inEditor(page, most);
        const top = await scrolls(page);
        // at most 7, Enter
        await most.click();
        await most.fill("7");
        await most.press("Enter");
        await posted(page, posts, 1);
        assert.deepEqual(posts, [{ least: 0, most: 7 }]);
        await page.waitForFunction(() => document.querySelectorAll("#codexResetDaysRow input")[1]?.value === "7");
        assert.equal(await scrolls(page), top, "no click moved the page");
        // out of order: said, nothing posted, the pair kept
        await least.click();
        await least.fill("8");
        await least.press("Enter");
        await page.waitForFunction(() => document.querySelector("#status.err"));
        assert.equal(await page.locator("#status.err").textContent(), await tr(page, lang, "Days are whole numbers from 0 to 365, the first no more than the second"));
        await page.waitForFunction(() => document.querySelectorAll("#codexResetDaysRow input")[0]?.value === "");
        assert.equal(posts.length, 1, "8–7 isn't posted");
        // from one field to the other: nothing yet; leaving the pair posts both
        await least.click();
        await least.fill("10");
        await least.press("Tab");
        assert.equal(await most.evaluate((i) => i === document.activeElement), true, "Tab goes to the other field");
        await page.waitForTimeout(100);
        assert.equal(posts.length, 1, "10–7 on the way isn't posted");
        await most.fill("20");
        const name = row.locator(".name");
        await inEditor(page, name);
        const before = await scrolls(page);
        await name.click();
        await posted(page, posts, 2);
        assert.deepEqual(posts.at(-1), { least: 10, most: 20 });
        await page.waitForFunction(() => [...document.querySelectorAll("#codexResetDaysRow input")].map((i) => i.value).join() === "10,20");
        assert.equal(await scrolls(page), before, "no click moved the page");
        // Escape puts the field back and posts nothing
        await most.click();
        await most.fill("30");
        await most.press("Escape");
        await page.waitForTimeout(100);
        assert.equal(posts.length, 2, "Escape posts nothing");
        assert.deepEqual(await values(page), ["10", "20"]);
        // emptied: any again
        await least.click();
        await least.fill("");
        await least.press("Tab");
        await most.fill("");
        await most.press("Enter");
        await posted(page, posts, 3);
        assert.deepEqual(posts.at(-1), { least: 0, most: 0 });

        // fits, no sideways scroll, no native select, no left border
        const over = await page.evaluate(() => {
          const view = document.querySelector(".editor .prov-prefs[data-list=codexResetList]").getBoundingClientRect();
          return [...document.querySelectorAll("#codexResetDaysRow, #codexResetDaysRow *")]
            .filter((e) => { const r = e.getBoundingClientRect(); return r.width && (r.right > view.right + 1 || r.left < view.left - 1); })
            .map((e) => e.className + " " + e.textContent.slice(0, 30));
        });
        assert.deepEqual(over, []);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        assert.equal(await row.evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px");
        assert.equal(await page.locator(".editor select").count(), 0, "no native select");
        assert.deepEqual(errors, []);
      });
    }
  }

  // the card's words for the reset that runs out first follow the days, as
  // the gateway's spend does (settings' ResetInDays)
  test(`${engine}: an account's card says a reset out of the days isn't auto-used`, async (t) => {
    const { page, errors } = await open(t, engine, "en", 900, {}, []);
    const words = (days, inMs, only) => page.evaluate(([days, inMs, only]) => {
      state.settings = { ...prefs, codexAutoReset: ["alice@example.com"], codexAutoResetOnly: only ? { "alice@example.com": only } : {}, ...days };
      const until = new Date(Date.now() + inMs).toISOString();
      const q = { provider: "codex", user: "alice@example.com", resets: { count: 1, until, each: [{ until }] } };
      const w = resetsWords(q.resets, q);
      return [w.querySelector(".resets-kept")?.textContent.trim(), w.title];
    }, [days, inMs, only]);
    const day = 864e5;
    assert.equal((await words({}, 20 * 60e3))[0], "· auto-used before it expires", "none set");
    assert.equal((await words({ codexResetDaysMost: 7 }, 20 * 60e3))[0], "· auto-used before it expires", "0–7, half an hour before");
    let [say, title] = await words({ codexResetDaysLeast: 1 }, 20 * 60e3);
    assert.equal(say, "· not auto-used", "at least a day, half an hour before");
    assert(title.includes("Auto-use is on, but"), title);
    assert.equal((await words({ codexResetDaysLeast: 1, codexResetDaysMost: 7 }, 3 * day, "week"))[0], "· auto-used when the week runs out", "the week's, 3 days left of 1–7");
    assert.equal((await words({ codexResetDaysMost: 7 }, 20 * day, "week"))[0], "· not auto-used", "the week's, 20 days left of 0–7");
    assert.equal((await words({}, 20 * day, "week"))[0], "· auto-used when the week runs out", "the week's, none set");
    assert.deepEqual(errors, []);
  });

  test(`${engine}: with no Codex account the days aren't there`, async (t) => {
    const browser = await launch(engine);
    const page = await (await browser.newContext({ viewport: { width: 900, height: 600 }, reducedMotion: "reduce" })).newPage();
    t.after(() => browser.close());
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", server("en", [], [], {}));
    await watchSettingsReads(page);
    await openProvPrefs(page, "codex");
    await settingsDrawn(page);
    await page.locator(".editor .prov-prefs #warmSegs .opt").first().waitFor();
    assert.equal(await page.locator("#codexResetDaysRow").isVisible(), false);
  });
}
