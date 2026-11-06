// Run with Node's test runner and Playwright on the module path; see README.md.
// How long before a Codex reset about to run out unused it is used (the
// owner, 10-30: 把提前用卡的时间做成可配置的): one pick under Settings ›
// Usage's "Codex: auto-use resets", for every account, from the app's menu
// (no native select). Unset it reads half an hour, the default; a pick
// posts settings/codex-reset-lead with its minutes and the row, and the
// "before one expires" titles, say the new lead. A value set outside the
// choices (the CLI's 45m) is shown and kept; one out of range (written by
// hand) reads as the half hour, as the gateway reads it. Custom… after the
// choices opens a field of minutes in the row (shangyuan0818 on Discord):
// Enter or leaving it posts the number, Escape posts nothing, and one out
// of range is said and not posted. Five languages,
// 1100 and 420 wide, both engines; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, users, posts, lead) {
  const fixed = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", dock: false, dockWindow: false, proxy: "",
    redact: false, redactPersonal: false, redactWords: [], codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "", version: "0.1.900", dir: "~/.config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    codexUsers: users, codexAutoReset: users.slice(0, 1).map((u) => u.toLowerCase()), codexAutoResetOnly: {},
    ...(lead === undefined ? {} : { codexResetLead: lead }),
  };
  let cur = fixed;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: fixed.fx });
    if (url.pathname === "/api/settings/codex-reset-lead") {
      const body = req.postDataJSON();
      posts.push(body);
      cur = { ...cur, codexResetLead: body.minutes };
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

async function open(t, engine, lang, width, lead, posts) {
  const browser = await launch(engine);
  const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-codex-reset-lead.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, users, posts, lead));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=usage");
  await settingsDrawn(page);
  await page.locator("#codexResetLeadRow").waitFor();
  return { page, errors };
}

// what the page says a lead of m minutes is, in lang
const say = (page, lang, m) => page.evaluate(([lang, m]) => {
  const n = m < 60 || m % 60 ? I18N[lang]?.["{n} min"] || "{n} min" : I18N[lang]?.["{n} h"] || "{n} h";
  const lead = n.replace("{n}", m < 60 || m % 60 ? m : m / 60);
  return (I18N[lang]?.["{lead} before"] || "{lead} before").replace("{lead}", lead);
}, [lang, m]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of langs) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Settings › Usage picks how early a reset about to expire is used`, async (t) => {
        const posts = [];
        const { page, errors } = await open(t, engine, lang, width, undefined, posts);
        const row = page.locator("#codexResetLeadRow");
        assert.equal(await row.isVisible(), true);
        // translated, its name and its words
        if (lang !== "en") {
          const name = (await row.locator(".name").textContent()).trim();
          assert.notEqual(name, "Codex: how early before one expires", "name translated");
          assert.notEqual((await row.locator(".sub").textContent()).trim().slice(0, 20), "For every account wi", "words translated");
        }
        const pick = row.locator("button.reset-lead");
        assert.equal((await pick.locator(".rl-say > .on").textContent()).trim(), await say(page, lang, 30), "unset is half an hour");
        // under the accounts' switches, not between them
        assert.equal(await page.evaluate(() => {
          const own = [...document.querySelectorAll(".ar-own")].pop();
          return !!(own.compareDocumentPosition(document.querySelector("#codexResetLeadRow")) & Node.DOCUMENT_POSITION_FOLLOWING);
        }), true);
        // the "before one expires" title says the lead
        const exp = page.locator(".ar-own").first().locator('.ar-own-case[data-case="expiring"] .ar-own-say');
        const half = await page.evaluate((lang) => (I18N[lang]?.["{n} min"] || "{n} min").replace("{n}", 30), lang);
        assert((await exp.getAttribute("title")).includes(half), "the title says half an hour");

        await reach(page, pick);
        const top = await page.locator("#view-settings").evaluate((v) => v.scrollTop);
        await pick.click();
        const items = page.locator(".proto-menu .pm-item");
        assert.equal(await items.count(), 12, "3 min to 24 h, then Custom…");
        assert.equal(await page.locator(".proto-menu .pm-item.on .pm-name").textContent(), await say(page, lang, 30));
        await page.locator(".proto-menu .pm-item", { hasText: await say(page, lang, 180) }).click();
        for (let i = 0; i < 100 && !posts.length; i++) await page.waitForTimeout(20);
        assert.deepEqual(posts, [{ minutes: 180 }]);
        await page.waitForFunction((want) => document.querySelector("#codexResetLeadRow button.reset-lead .rl-say > .on")?.textContent.trim() === want, await say(page, lang, 180));
        const title = await page.locator(".ar-own").first().locator('.ar-own-case[data-case="expiring"] .ar-own-say').getAttribute("title");
        const hours = await page.evaluate((lang) => (I18N[lang]?.["{n} h"] || "{n} h").replace("{n}", 3), lang);
        assert(title.includes(hours), `the title says 3 h: ${title}`);
        assert.equal(await page.locator("#view-settings").evaluate((v) => v.scrollTop), top, "no click moved the page");
        // the shortest, three minutes (shangyuan0818 on Discord asked for less than 15)
        await pick.click();
        await page.locator(`.proto-menu .pm-item .pm-name:text-is(${JSON.stringify(await say(page, lang, 3))})`).click(); // not "3 h before"
        for (let i = 0; i < 100 && posts.length < 2; i++) await page.waitForTimeout(20);
        assert.deepEqual(posts, [{ minutes: 180 }, { minutes: 3 }]);
        await page.waitForFunction((want) => document.querySelector("#codexResetLeadRow button.reset-lead .rl-say > .on")?.textContent.trim() === want, await say(page, lang, 3));
        assert.equal(await page.locator("#view-settings").evaluate((v) => v.scrollTop), top, "no click moved the page");

        // Custom…: the last item, a field of minutes in the row, focused
        const custom = await page.evaluate((lang) => I18N[lang]?.["Custom…"] || "Custom…", lang);
        const own = page.locator("#codexResetLeadRow input.reset-lead-own");
        const ownField = async () => {
          await page.locator("#codexResetLeadRow button.reset-lead").click();
          const last = page.locator(".proto-menu .pm-item").last();
          assert.equal((await last.locator(".pm-name").textContent()).trim(), custom);
          await last.click();
          await own.waitFor();
          assert.equal(await own.evaluate((i) => i === document.activeElement), true, "the field has the keys");
        };
        await ownField();
        assert.equal(await own.inputValue(), "3", "it starts at the lead");
        assert.equal(await own.getAttribute("inputmode"), "numeric");
        const r = await own.boundingBox(), v = await page.locator("#view-settings").boundingBox();
        assert(r.x >= v.x && r.x + r.width <= v.x + v.width + 1, "the field fits");
        assert.equal(await page.locator("#view-settings").evaluate((v) => v.scrollTop), top, "opening the field moved nothing");
        await own.fill("7");
        await own.press("Enter");
        for (let i = 0; i < 100 && posts.length < 3; i++) await page.waitForTimeout(20);
        assert.deepEqual(posts.at(-1), { minutes: 7 });
        await page.waitForFunction((want) => document.querySelector("#codexResetLeadRow button.reset-lead .rl-say > .on")?.textContent.trim() === want, await say(page, lang, 7));
        // under the least: said, nothing posted, the lead kept
        await ownField();
        await own.fill("2");
        await own.press("Enter");
        await page.locator("#codexResetLeadRow button.reset-lead").waitFor();
        assert.equal(posts.length, 3, "2 isn't posted");
        assert.equal(await page.locator("#status.err").textContent(), await page.evaluate((lang) => I18N[lang]?.["Minutes is a whole number from 3 to 1440"] || "Minutes is a whole number from 3 to 1440", lang));
        assert.equal((await page.locator("#codexResetLeadRow .rl-say > .on").textContent()).trim(), await say(page, lang, 7));
        assert(await page.evaluate(() => RESET_LEADS[0] === 3 && RESET_LEADS.at(-1) === 1440), "the words say the range RESET_LEADS has");
        // Escape: nothing posted
        await ownField();
        await own.fill("9");
        await own.press("Escape");
        await page.locator("#codexResetLeadRow button.reset-lead").waitFor();
        assert.equal(posts.length, 3, "Escape posts nothing");
        // leaving the field saves it
        await ownField();
        await own.fill("12");
        await page.locator("#codexResetLeadRow .name").click();
        for (let i = 0; i < 100 && posts.length < 4; i++) await page.waitForTimeout(20);
        assert.deepEqual(posts.at(-1), { minutes: 12 });
        await page.waitForFunction((want) => document.querySelector("#codexResetLeadRow button.reset-lead .rl-say > .on")?.textContent.trim() === want, await say(page, lang, 12));
        assert.equal(await page.locator("#view-settings").evaluate((v) => v.scrollTop), top, "no click moved the page");

        // fits, no sideways scroll, no native select, no left border
        const over = await page.evaluate(() => {
          const view = document.querySelector("#view-settings").getBoundingClientRect();
          return [...document.querySelectorAll("#codexResetLeadRow, #codexResetLeadRow *")]
            .filter((e) => { const r = e.getBoundingClientRect(); return r.width && (r.right > view.right + 1 || r.left < view.left - 1); })
            .map((e) => e.className + " " + e.textContent.slice(0, 30));
        });
        assert.deepEqual(over, []);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        assert.equal(await row.evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px");
        assert.equal(await page.locator("#view-settings select").count(), 0, "no native select");
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: a lead set outside the choices is shown and kept; one out of range reads as half an hour`, async (t) => {
    const posts = [];
    const { page, errors } = await open(t, engine, "en", 900, 45, posts);
    const pick = page.locator("#codexResetLeadRow button.reset-lead");
    assert.equal((await pick.locator(".rl-say > .on").textContent()).trim(), "45 min before");
    await pick.click();
    assert.equal(await page.locator(".proto-menu .pm-item").count(), 13);
    assert.equal(await page.locator(".proto-menu .pm-item.on .pm-name").textContent(), "45 min before");
    await page.keyboard.press("Escape");
    assert.deepEqual(posts, [], "nothing written by looking");
    assert.deepEqual(errors, []);
    const bad = await open(t, engine, "en", 900, 2, []);
    assert.equal((await bad.page.locator("#codexResetLeadRow button.reset-lead .rl-say > .on").textContent()).trim(), "30 min before");
    assert.deepEqual(bad.errors, []);
  });

  test(`${engine}: with no Codex account the lead isn't there`, async (t) => {
    const browser = await launch(engine);
    const page = await (await browser.newContext({ viewport: { width: 900, height: 600 }, reducedMotion: "reduce" })).newPage();
    t.after(() => browser.close());
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", server("en", [], [], undefined));
    await watchSettingsReads(page);
    await page.goto("http://magpie.test/?view=settings&tab=usage");
    await settingsDrawn(page);
    await page.locator("#resetReminderSegs .opt").first().waitFor();
    assert.equal(await page.locator("#codexResetLeadRow").isVisible(), false);
  });
}
