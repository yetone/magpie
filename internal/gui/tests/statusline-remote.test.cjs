const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");
const { openPart } = require("./reader.cjs");

// steven_4422 (Discord): magpie in Docker, Claude Code on the host. The
// status line square writes the host's settings.json with a binary inside
// the container, so magpie web shows the curl the host runs instead, against
// the gateway's own /v1/magpie/statusline. The app (not web) shows no such row.
const PUBLIC = "https://magpie.example.com";
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const titles = {
  en: "Status line on another computer", zh: "其他电脑上的状态栏", "zh-TW": "其他電腦上的狀態列",
  ja: "別のコンピューターのステータスライン", de: "Statuszeile auf einem anderen Rechner",
};

async function setup(t, engine, lang, width, options) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(6000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", fixture(lang, "light", [], { lan: true, ...options }));
  await page.goto("http://magpie.test/?view=gateway");
  await page.locator("#prefs").waitFor();
  if (!(await page.locator("#view-settings").isVisible())) await page.locator("#prefs").click();
  await openPart(page, "about");
  await page.locator("#about .row").first().waitFor();
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [1100, 400]) {
      test(`${engine} ${lang} ${width}: magpie web shows the curl for Claude Code on another computer`, async (t) => {
        const { page, errors } = await setup(t, engine, lang, width, { lanURLs: [PUBLIC], publicURL: PUBLIC, web: true });
        const row = page.locator("#statuslineRemoteRow");
        await row.waitFor();
        assert.match(await row.locator(".who").textContent(), new RegExp(titles[lang]));
        assert.equal(await row.locator("code").textContent(),
          "curl -fsS -m 1 -H 'Authorization: Bearer <gateway key>' --data-binary @- " + PUBLIC + "/v1/magpie/statusline || true");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "About wider than the window");
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: the app has no such row`, async (t) => {
    const { page, errors } = await setup(t, engine, "en", 1100, { lanURLs: ["http://192.168.1.10:3999"] });
    assert.equal(await page.locator("#statuslineRemoteRow").count(), 0);
    assert.deepEqual(errors, []);
  });
}
