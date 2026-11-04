// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Privacy says what is done with each kind of value found
// (#1977): Mask, Warn or Block, saved as the kind's choice, Mask saved as
// no choice at all; personal kinds behind Show all kinds; a Never mask
// list saved whole, a value a row; and Try it, which shows text as the vendor would get
// it, what was found and what would be done, says a block holds the
// request back, keeps the text nowhere in the browser and shows no value
// the preview didn't hand back. Every language, Chromium and WebKit,
// 1100 and 640px; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, wheelTo, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const SECRET = "sk-proj-Tr1eDq8Wm3Rt6YxN2vLp";
const PH = "{{API_KEY_ab12cd34}}";

function serve(lang, posted, shown = { from: SECRET, to: PH }) {
  let settings = { theme: "light", lang, tray: "panel", redact: true, redactPersonal: true };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/redact-actions") {
      const b = req.postDataJSON();
      posted.push(["actions", b]);
      settings = { ...settings, redactActions: Object.keys(b.actions).length ? b.actions : undefined };
      return json(settings);
    }
    if (url.pathname === "/api/settings/redact-allow") {
      const b = req.postDataJSON();
      posted.push(["allow", b]);
      settings = { ...settings, redactAllow: b.allow };
      return json(settings);
    }
    if (url.pathname === "/api/masking/preview") {
      const { text } = req.postDataJSON();
      posted.push(["preview", text]);
      const block = settings.redactActions?.API_KEY === "block";
      return json({ text: text.replaceAll(shown.from, shown.to), hits: text.includes(shown.from) ? [{ kind: "API_KEY", by: "rule", action: block ? "block" : "", placeholder: shown.to, head: shown.from.slice(0, 4), len: shown.from.length }] : [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// English a translated page must not show
const english = ["When a value is found", "Show all kinds", "Never mask", "Try it", "What the vendor would get", "Held back", "Goes to the vendor"];
const wide = (page) => page.evaluate(() => {
  const over = [...document.querySelectorAll("#maskActList *, #maskTryList *")].filter((n) => n.getClientRects().length && n.getBoundingClientRect().right > innerWidth + 1).map((n) => n.className?.baseVal ?? n.className);
  return { page: document.documentElement.scrollWidth > document.documentElement.clientWidth, over };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: each kind's action, the allow list and Try it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posted));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=privacy");
        await settingsDrawn(page);

        // the secret kinds first, each on Mask; personal ones behind a button
        const acts = page.locator("#maskActList");
        assert.deepEqual(await acts.locator(".mask-act").evaluateAll((rs) => rs.map((r) => r.dataset.kind)), ["API_KEY", "PRIVATE_KEY", "TOKEN", "PASSWORD", "SECRET"]);
        assert.equal(await acts.locator(".mask-act .segs .opt.on").count(), 5);
        const more = acts.locator(".mask-act-more button");
        await wheelTo(page, more, 300); // under the masking card, a long wheel down at 640px
        await more.click();
        await acts.locator('.mask-act[data-kind="EMAIL"]').waitFor();
        assert.equal(await acts.locator(".mask-act").count(), 22, "every personal kind shown");

        // Block on API_KEY is saved as its choice and said under it
        const key = acts.locator('.mask-act[data-kind="API_KEY"]');
        await reach(page, key.locator(".segs .opt").nth(2));
        await key.locator(".segs .opt").nth(2).click();
        await acts.locator('.mask-act[data-kind="API_KEY"] .sub.act-block').waitFor();
        assert.deepEqual(posted.at(-1), ["actions", { actions: { API_KEY: "block" } }]);
        // Warn on EMAIL is added beside it
        const mail = acts.locator('.mask-act[data-kind="EMAIL"]');
        await reach(page, mail.locator(".segs .opt").nth(1));
        await mail.locator(".segs .opt").nth(1).click();
        await acts.locator('.mask-act[data-kind="EMAIL"] .sub.act-warn').waitFor();
        assert.deepEqual(posted.at(-1), ["actions", { actions: { API_KEY: "block", EMAIL: "warn" } }]);
        // and Mask again is no choice at all
        await reach(page, acts.locator('.mask-act[data-kind="EMAIL"] .segs .opt').first());
        await acts.locator('.mask-act[data-kind="EMAIL"] .segs .opt').first().click();
        await page.waitForFunction(() => !document.querySelector('#maskActList .mask-act[data-kind="EMAIL"] .sub.act-warn'));
        assert.deepEqual(posted.at(-1), ["actions", { actions: { API_KEY: "block" } }]);

        // the allow list, its add box split on commas, saved whole and
        // drawn a value a row (wordList)
        const allow = acts.locator('.word-list[data-list="allow"] input.word-add');
        await reach(page, allow);
        await allow.fill(" team@kelvarn.io，build-01 ,, ");
        await allow.press("Enter");
        for (let i = 0; i < 100 && !posted.some(([k]) => k === "allow"); i++) await page.waitForTimeout(40);
        assert.deepEqual(posted.filter(([k]) => k === "allow").at(-1), ["allow", { allow: ["team@kelvarn.io", "build-01"] }]);
        await page.waitForFunction(() => [...document.querySelectorAll('#maskActList .word-list[data-list="allow"] input[data-word]')].map((i) => i.value).join("|") === "team@kelvarn.io|build-01");

        // Try it: the vendor's text, what was found, and that it is held back
        const box = page.locator("#maskTryList textarea.mask-try-in");
        await reach(page, box);
        await box.fill(`deploy with ${SECRET} now`);
        const out = page.locator("#maskTryList .mask-try-out");
        await out.locator("mark.mo-ph").waitFor();
        assert.equal(await out.locator(".mask-try-text").textContent(), `deploy with ${PH} now`);
        assert.equal(await out.locator(".mask-try-note.block").count(), 1, "a block says the request is held back");
        assert.equal(await out.locator(".mr-act.block").count(), 1);
        assert.equal((await out.textContent()).includes(SECRET), false, "the value is not shown back");
        assert.equal(posted.filter(([k]) => k === "preview").length, 1, "typing asks once, after a pause");
        // kept nowhere in the browser
        const kept = await page.evaluate(() => { const all = []; for (const s of [localStorage, sessionStorage]) for (let i = 0; i < s.length; i++) all.push(s.getItem(s.key(i))); return all.join("\n"); });
        assert.equal(kept.includes(SECRET), false, "the text is stored nowhere");
        // nothing found: said so
        await box.fill("nothing secret here");
        await out.locator(".mask-try-note").first().waitFor();
        await page.waitForFunction(() => !document.querySelector("#maskTryList mark.mo-ph"));
        assert.equal(await out.locator(".mask-try-hits").count(), 0);

        if (lang !== "en") {
          const said = await page.locator("#setPage-privacy").innerText();
          for (const w of english) assert(!said.includes(w), `${w} left in English`);
        }
        assert.deepEqual(await wide(page), { page: false, over: [] });
        assert.deepEqual(errors, []);
      });
    }
  }
}

// with "Keep a key's form" on the preview hands back a made-up key of the
// real one's form, not a {{…}} placeholder: Try it marks it all the same,
// and only it (#3599)
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Try it marks a made-up key`, async (t) => {
    const REAL = "sk_w3axCByTQgK1PxqPhfKTrJOLBqz6sbZv", MADE = "sk_hYQWRBFmdHNIo5XVT2VnIIGh4mmMiJNm";
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(8000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en", [], { from: REAL, to: MADE }));
    await watchSettingsReads(page);
    await page.goto("http://magpie.test/?view=settings&tab=privacy");
    await settingsDrawn(page);
    const box = page.locator("#maskTryList textarea.mask-try-in");
    await reach(page, box);
    await box.fill(`OPENAI_API_KEY=${REAL} sk_not_found_here, again ${REAL}`);
    const out = page.locator("#maskTryList .mask-try-out");
    await out.locator(".mask-try-hits").waitFor();
    assert.equal(await out.locator(".mask-try-text").textContent(), `OPENAI_API_KEY=${MADE} sk_not_found_here, again ${MADE}`);
    assert.deepEqual(await out.locator("mark.mo-ph").allTextContents(), [MADE, MADE]);
    assert.equal(await out.locator("mark.mo-ph").first().getAttribute("title"), "API keys");
    assert.deepEqual(errors, []);
  });
}
