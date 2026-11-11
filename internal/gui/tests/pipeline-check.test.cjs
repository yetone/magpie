// Run with Node's test runner and Playwright on the module path; see README.md.
// EV_John_Liu on X: "现在不是有图片生成、asr、tts管线和网络搜索。能不能做个快速
// 检测。可用". Settings › Models › Quick check has a row per pipeline (image
// generation, speech to text, text to speech, web search), each with Test,
// and Test all. A check is one real call the user pays for, so drawing the
// page calls nothing; a row's Test checks that pipeline alone, and Test all
// each of them. A row then says ok with the model and how long it took, the
// vendor's error with its status, or that nothing is set up. Every
// language; wide and at a phone's width; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(lang) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], workbuddyCheckins: [], lanURLs: [],
    imageGenAuto: "openai/gpt-image-2",
    imageGenModels: [{ id: "openai/gpt-image-2", name: "gpt-image-2", provider: "openai", providerName: "OpenAI", icon: "openai" }],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

const answers = {
  image: { kind: "image", ok: true, model: "openai/gpt-image-2", status: 200, ms: 840 },
  transcription: { kind: "transcription", ok: false, model: "ears/whisper-1", status: 400, ms: 120, error: "Invalid file format." },
  speech: { kind: "speech", ok: false, unset: true, ms: 0, error: "No provider lists a text-to-speech model" },
  search: { kind: "search", ok: true, ms: 1500 },
};

// server answers a check only once the test lets it, so a row's busy state
// can be seen
function server(lang, checks) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 } });
    if (url.pathname === "/api/settings") return json(settingsPayload(lang));
    if (url.pathname === "/api/pipelines/check") {
      const { kind } = JSON.parse(req.postData() || "{}");
      let go;
      const held = new Promise((r) => (go = r));
      checks.push({ kind, go });
      await held;
      return json(answers[kind]);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const want = {
  en: { head: "Quick check", all: "Test all", test: "Test", testing: "Testing…", names: ["Image generation", "Speech to text", "Text to speech", "Web search"], ms: "840 ms", s: "1.5 s", unset: "No provider lists a text-to-speech model" },
  zh: { head: "快速检测", all: "全部测试", test: "测试", testing: "测试中…", names: ["生图模型", "语音转文字", "文字转语音", "联网搜索"], ms: "840 毫秒", s: "1.5 秒", unset: "没有供应商列出文字转语音模型" },
  "zh-TW": { head: "快速檢測", all: "全部測試", test: "測試", testing: "測試中…", names: ["生圖模型", "語音轉文字", "文字轉語音", "聯網搜尋"], ms: "840 毫秒", s: "1.5 秒", unset: "沒有供應商列出文字轉語音模型" },
  ja: { head: "クイックチェック", all: "すべてテスト", test: "テスト", testing: "テスト中…", names: ["画像生成", "音声からテキスト", "テキストから音声", "ウェブ検索"], ms: "840 ミリ秒", s: "1.5 秒", unset: "音声合成モデルを載せているプロバイダがありません" },
  de: { head: "Schnelltest", all: "Alle testen", test: "Testen", testing: "Wird getestet…", names: ["Bilderzeugung", "Sprache zu Text", "Text zu Sprache", "Websuche"], ms: "840 ms", s: "1.5 s", unset: "Kein Anbieter führt ein Sprachausgabemodell" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Settings checks each pipeline with one call, only when asked", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-pipeline-check-${name}.png`), buf);
      }
      await browser.close();
    });
    const fits = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    for (const lang of Object.keys(want)) {
      const w = want[lang];
      for (const width of lang === "en" || lang === "zh" ? [1100, 400] : [1100]) {
        await t.test(`${lang} at ${width}px: Test checks one pipeline, Test all each, and each row says what it found`, async () => {
          const checks = [];
          const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, checks));
          await page.goto("http://magpie.test/?view=settings&tab=models");
          const rows = page.locator("#pipeCheckList .pipe-check");
          await rows.nth(3).waitFor();
          const head = page.locator(".row-head", { has: page.locator("#pipeCheckAll") });
          assert.equal((await head.locator(".label").textContent()).trim(), w.head);
          const all = page.locator("#pipeCheckAll");
          assert.equal((await all.textContent()).trim(), w.all);
          assert.deepEqual((await rows.locator(".name").allTextContents()).map((x) => x.trim()), w.names);
          assert.deepEqual((await rows.locator(".pipe-test").allTextContents()).map((x) => x.trim()), Array(4).fill(w.test));
          assert.equal(await rows.locator(".pipe-said:visible").count(), 0, "nothing found yet");
          // drawing the page, and drawing it again, checks nothing
          await page.evaluate(() => window.pipeCheckFill());
          await page.waitForTimeout(50);
          assert.deepEqual(checks, [], "a check costs money: none without a press");
          assert.equal(await fits(page), 0, "no sideways scroll");

          // a row's Test checks that pipeline only
          // pressed where it is, as a pointer does (Playwright's click
          // scrolls its target into view first, which isn't the page's doing)
          const press = async (loc) => {
            const r = await loc.boundingBox();
            await page.mouse.click(r.x + r.width / 2, r.y + r.height / 2);
          };
          const speech = rows.nth(2);
          // brought into view with the wheel, as a user scrolls to it
          // (WebKit's wheel scroll runs on after the event, so wait till it rests)
          const settle = async () => {
            let last = null;
            for (let i = 0; i < 50; i++) {
              const now = await page.evaluate(() => document.getElementById("view-settings").scrollTop);
              if (now === last) return;
              last = now;
              await page.waitForTimeout(100);
            }
          };
          const bring = async (loc) => {
            for (let i = 0; i < 40; i++) {
              await settle();
              const b = await loc.boundingBox();
              if (b && b.y > 80 && b.y + b.height < 700) return;
              await page.mouse.move(width / 2, 380);
              await page.mouse.wheel(0, b && b.y < 80 ? -150 : 150);
              await page.waitForTimeout(30);
            }
            assert.fail("never came into view");
          };
          const scrolled = () => page.evaluate(() => [document.scrollingElement.scrollTop, document.getElementById("view-settings").scrollTop]);
          await bring(speech);
          const scrollBefore = await scrolled();
          const atBefore = (await speech.locator(".pipe-test").boundingBox()).y;
          await press(speech.locator(".pipe-test"));
          await page.waitForFunction(() => document.querySelector('[data-pipe="speech"] .pipe-test').disabled);
          assert.equal((await speech.locator(".pipe-test").textContent()).trim(), w.testing);
          assert.deepEqual(checks.map((c) => c.kind), ["speech"]);
          checks[0].go();
          await page.waitForFunction(() => !document.querySelector('[data-pipe="speech"] .pipe-test').disabled);
          const said = speech.locator(".pipe-said");
          assert.equal((await said.textContent()).trim(), w.unset);
          assert.equal(await said.evaluate((e) => e.classList.contains("err")), false, "nothing set up isn't a failure");
          // what was pressed stays where it was (app.js holds the clicked
          // element while what is around it draws), and the window never scrolls
          const at = (await speech.locator(".pipe-test").boundingBox()).y;
          assert.equal((await scrolled())[0], scrollBefore[0], "the window never scrolls");
          assert(Math.abs(at - atBefore) <= 1, `the pressed Test stays put: moved ${at - atBefore}px`);

          // Test all checks each, at once
          await bring(all);
          const allAt = (await all.boundingBox()).y;
          await press(all);
          await page.waitForFunction(() => document.querySelectorAll("#pipeCheckList .pipe-test:disabled").length === 4);
          assert.deepEqual(checks.slice(1).map((c) => c.kind).sort(), ["image", "search", "speech", "transcription"]);
          for (const c of checks.slice(1)) c.go();
          await page.waitForFunction(() => document.querySelectorAll("#pipeCheckList .pipe-test:disabled").length === 0);
          assert(Math.abs((await all.boundingBox()).y - allAt) <= 1, "Test all stays put");
          const line = (i) => rows.nth(i).locator(".pipe-said");
          assert.equal((await line(0).textContent()).trim(), `openai/gpt-image-2 · ${w.ms}`);
          assert.equal(await line(0).evaluate((e) => e.classList.contains("err")), false);
          assert.equal((await line(1).textContent()).trim(), "ears/whisper-1 · 400 · Invalid file format.");
          assert.equal(await line(1).evaluate((e) => e.classList.contains("err")), true, "the vendor's error reads as one");
          assert.equal(await line(1).isVisible(), true, "the error is in sight");
          assert.equal((await line(3).textContent()).trim(), w.s);
          assert.equal(checks.length, 5, "one call per press, no more");
          assert.equal(await fits(page), 0, "no sideways scroll");
          shots.push([`${lang}-${width}`, await page.screenshot({ fullPage: true })]);
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
