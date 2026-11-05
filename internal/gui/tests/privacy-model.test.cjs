// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Privacy's local masking model (#1977): a row under the masking
// switches, there while either is on. On downloads the model, its progress
// shown and a Cancel beside it; once it is on, the row says where it runs.
// Off, a downloaded model can be deleted, after asking. What it says while
// downloading or on is a state, shown in full; off, its line explains it
// and rests out of sight, for the hover card (settingshints.js), as the
// threshold's does. In every language, in a wide window and a narrow one,
// and a click moves nothing. Under it, the model's threshold is picked from the
// app's menu too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { wheelTo, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const size = 1637572192;
const words = {
  en: { name: "Find with a local model", badge: "New", on: "On", gpu: "On, on the GPU (MTL0)" },
  zh: { name: "用本地模型查找", badge: "新", on: "开启", gpu: "已开启，运行在 GPU（MTL0）" },
  "zh-TW": { name: "用本機模型查找", badge: "新", on: "開啟", gpu: "已開啟，執行於 GPU（MTL0）" },
  ja: { name: "ローカルモデルで検出", badge: "新着", on: "オン", gpu: "オン、GPU で実行中（MTL0）" },
  de: { name: "Mit lokalem Modell finden", badge: "Neu", on: "An", gpu: "An, auf der GPU (MTL0)" },
};

// model is the page's /api/privacy-model: a POST is recorded and moves it
// along, as magpie would (downloading, then on and running on the GPU)
function serve(lang, masking, model, posts) {
  const settings = () => ({ lang, theme: "light", redact: masking, redactPersonal: false });
  const state = { agents: [], profiles: [], settings: settings() };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/privacy-model") {
      if (r.request().method() === "POST") {
        const { do: doing, model: id, score } = JSON.parse(r.request().postData());
        posts.push([doing, id, score].filter((x) => x !== undefined && x !== "").join(" "));
        if (doing === "score") model.minScore = score || model.defaultScore;
        if (doing === "on") Object.assign(model, { downloading: true, done: 0, total: size, ticks: 0 });
        if (doing === "cancel") Object.assign(model, { downloading: false, done: 0 });
        if (doing === "remove") Object.assign(model, { installed: false, on: false });
      } else if (model.downloading && ++model.ticks >= 2) {
        Object.assign(model, { downloading: false, installed: true, on: true, running: true, gpu: true, device: "MTL0" });
      } else if (model.downloading) {
        model.done = Math.round(size * 0.4);
      }
      const { ticks, ...out } = model;
      out.model ||= "privacy-filter";
      // every model to pick: this one, and the other as the test has it
      out.models = [{ ...out, models: undefined }, ...(model.others || [])];
      return json(out);
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, masking, model, posts) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, masking, model, posts));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=privacy");
  await page.locator("#redactList .row").first().waitFor();
  await settingsDrawn(page);
  return { browser, page, errors };
}

const guard = { model: "astrlink-guard", supported: true, installed: false, size: 38958270 };
const pickWords = {
  en: { head: "The local model", note: "Chinese and English, few false alarms in code · 39 MB" },
  zh: { head: "本地模型", note: "中英文，代码里很少误判 · 39 MB" },
  "zh-TW": { head: "本機模型", note: "中英文，程式碼裡很少誤判 · 39 MB" },
  ja: { head: "ローカルモデル", note: "中国語と英語、コードでの誤検出が少ない · 39 MB" },
  de: { head: "Lokales Modell", note: "Chinesisch und Englisch, wenig Fehlalarme in Code · 39 MB" },
};

const scoreWords = {
  en: { name: "Confidence threshold", def: "Default (0.8)", most: "Masks the most" },
  zh: { name: "置信度阈值", def: "默认（0.8）", most: "遮得最多" },
  "zh-TW": { name: "信賴度門檻", def: "預設（0.8）", most: "遮得最多" },
  ja: { name: "信頼度のしきい値", def: "デフォルト（0.8）", most: "最も多くマスク" },
  de: { name: "Konfidenzschwelle", def: "Standard (0.8)", most: "Maskiert am meisten" },
};

// where AstrLink Guard runs (yyhhyyyyyy on Discord: it said "on the CPU"
// while CoreML ran it): CoreML on the device its plan picked, the CPU
// with CoreML's error after a failed read, the CPU where there's no CoreML,
// and a slow line with its fix under Rosetta
const runWords = {
  en: { ne: "On, with CoreML on the Neural Engine", gpu: "On, with CoreML on the GPU", ml: "On, with CoreML", failed: "On, on the CPU: CoreML failed (prediction failed)", cpu: "On, on the CPU", rosetta: "On, slowly under Rosetta: the next update installs magpie for Apple silicon" },
  zh: { ne: "已开启，通过 CoreML 运行在神经网络引擎", gpu: "已开启，通过 CoreML 运行在 GPU", ml: "已开启，通过 CoreML 运行", failed: "已开启，运行在 CPU：CoreML 出错（prediction failed）", cpu: "已开启，运行在 CPU", rosetta: "已开启，但在 Rosetta 下运行较慢：下次更新会装上 Apple 芯片版 magpie" },
  "zh-TW": { ne: "已開啟，透過 CoreML 執行於神經網路引擎", gpu: "已開啟，透過 CoreML 執行於 GPU", ml: "已開啟，透過 CoreML 執行", failed: "已開啟，執行於 CPU：CoreML 出錯（prediction failed）", cpu: "已開啟，執行於 CPU", rosetta: "已開啟，但在 Rosetta 下執行較慢：下次更新會裝上 Apple 晶片版 magpie" },
  ja: { ne: "オン、CoreML により Neural Engine で実行中", gpu: "オン、CoreML により GPU で実行中", ml: "オン、CoreML で実行中", failed: "オン、CPU で実行中：CoreML が失敗しました（prediction failed）", cpu: "オン、CPU で実行中", rosetta: "オン、Rosetta で動作中のため低速です。次のアップデートで Apple シリコン版の magpie が入ります" },
  de: { ne: "An, mit CoreML auf der Neural Engine", gpu: "An, mit CoreML auf der GPU", ml: "An, mit CoreML", failed: "An, auf der CPU: CoreML ist ausgefallen (prediction failed)", cpu: "An, auf der CPU", rosetta: "An, unter Rosetta langsam: das nächste Update installiert magpie für Apple Silicon" },
};
const runs = {
  ne: { via: "CoreML", device: "Neural Engine" }, gpu: { via: "CoreML", device: "GPU" }, ml: { via: "CoreML" },
  failed: { device: "CPU", gpuError: "prediction failed" }, cpu: { device: "CPU" },
  // the Intel build on Apple silicon (cdredfox on Discord: 150% of a core)
  rosetta: { device: "CPU", rosetta: true },
};

// what the row shows: its words and its New mark, whether they rest out of
// sight for the hover card (hint) or are cut, that it is inside the list,
// and that it is the page's first setting
const look = (page) => page.evaluate(() => {
  const r = document.querySelector("#privacyModelRow"), list = r.closest(".list").getBoundingClientRect();
  const sub = r.querySelector(".sub"), b = r.getBoundingClientRect(), v = r.querySelector(".val").getBoundingClientRect();
  return {
    name: r.querySelector(".name").firstChild.textContent, badge: r.querySelector(".name .badge.new")?.textContent, sub: sub.textContent,
    first: r === document.querySelector("#setPage-privacy .list.prefs > .row"),
    on: r.querySelector(".segs .opt.on")?.textContent, buttons: [...r.querySelectorAll("button.text")].map((x) => x.textContent),
    hint: getComputedStyle(sub).getPropertyValue("--hint").trim() === "1",
    cut: sub.scrollWidth > sub.clientWidth + 1 || sub.scrollHeight > sub.clientHeight + 1,
    inside: b.left >= list.left - 1 && b.right <= list.right + 1 && v.right <= b.right + 1,
    wide: document.documentElement.scrollWidth > document.documentElement.clientWidth,
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 640]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: turned on, the model downloads and says where it runs`, async (t) => {
        const posts = [], model = { supported: true, installed: false, size };
        const { browser, page, errors } = await open(engine, lang, width, true, model, posts);
        t.after(() => browser.close());
        const row = page.locator("#privacyModelRow");
        await row.locator(".segs").waitFor();
        let m = await look(page);
        assert.equal(m.name, w.name);
        assert.equal(m.badge, w.badge);
        assert(m.first, "the model is the first setting under the card");
        assert(m.sub.includes("1.6 GB"), `says what it downloads: ${m.sub}`);
        if (lang !== "en") assert(!/no rule can find|Turning it on/.test(m.sub), `translated: ${m.sub}`);
        assert(m.hint && m.inside && !m.wide, `off, the explanation rests out of sight, for the hover card: ${JSON.stringify(m)}`);

        await page.mouse.move(width / 2, 300);
        for (let i = 0; i < 20 && (await row.boundingBox()).y > 900; i++) {
          await page.mouse.wheel(0, 200);
          await page.waitForTimeout(80);
        }
        await page.waitForTimeout(300);
        const before = await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]);
        const b = await row.locator(".segs .opt", { hasText: w.on }).boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        // downloading: its progress, and a Cancel
        await page.waitForFunction(() => /655 MB/.test(document.querySelector("#privacyModelRow .sub").textContent));
        m = await look(page);
        assert.equal(m.buttons.length, 1, "a Cancel while it downloads");
        assert.equal(m.on, w.on);
        assert(!m.hint && !m.cut && m.inside && !m.wide, `the download's progress shown in full and inside: ${JSON.stringify(m)}`);
        // then on, on the GPU
        await page.waitForFunction((want) => document.querySelector("#privacyModelRow .sub").textContent === want, w.gpu);
        m = await look(page);
        assert.deepEqual(m.buttons, [], "nothing to cancel or delete while it is on");
        assert(!m.hint && !m.cut && m.inside && !m.wide, `shown in full and inside: ${JSON.stringify(m)}`);
        assert.deepEqual(posts, ["on"]);
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]), before, "the click moved nothing");
        assert.deepEqual(errors, []);
      });
    }
  }

  for (const lang of Object.keys(runWords)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: AstrLink Guard says what runs it`, async (t) => {
        for (const [k, run] of Object.entries(runs)) {
          const model = { ...guard, installed: true, on: true, running: true, minScore: 0.8, defaultScore: 0.8, ...run };
          const { browser, page, errors } = await open(engine, lang, width, true, model, []);
          await page.locator("#privacyModelRow .segs").waitFor();
          const m = await look(page);
          await browser.close();
          assert.equal(m.sub, runWords[lang][k], k);
          assert(!m.hint && !m.cut && m.inside && !m.wide, `${k} shown in full and inside: ${JSON.stringify(m)}`);
          assert.deepEqual(errors, []);
        }
      });
    }
  }

  test(`${engine}: a download is cancelled, and a downloaded model deleted after asking`, async (t) => {
    const posts = [], model = { supported: true, installed: false, size };
    const { browser, page, errors } = await open(engine, "en", 1100, true, model, posts);
    t.after(() => browser.close());
    const row = page.locator("#privacyModelRow");
    await wheelTo(page, row, 300); // under the masking card; WebKit's own scroll-into-view misses it
    await row.locator(".segs .opt", { hasText: "On" }).click();
    await row.locator("button.text", { hasText: "Cancel" }).click();
    await page.waitForFunction(() => document.querySelector("#privacyModelRow .segs .opt.on")?.textContent === "Off");
    assert.deepEqual(posts, ["on", "cancel"]);

    Object.assign(model, { installed: true, on: false });
    await page.reload();
    await wheelTo(page, row.locator("button.text", { hasText: "Delete model" }), 300);
    await row.locator("button.text", { hasText: "Delete model" }).click();
    await page.locator("dialog.action-confirm button", { hasText: /^Delete$/ }).click();
    await page.waitForFunction(() => !document.querySelector("#privacyModelRow button.text"));
    assert.deepEqual(posts, ["on", "cancel", "remove"]);
    assert.match((await look(page)).sub, /downloads 1\.6 GB once/);
    assert.deepEqual(errors, []);
  });

  test(`${engine}: while nothing is masked the model row waits under Rules, and none to turn on where it can't run`, async (t) => {
    const { browser, page, errors } = await open(engine, "en", 1100, false, { supported: true, size }, []);
    t.after(() => browser.close());
    await page.locator("#redactList .row", { hasText: "Mask personal data" }).waitFor();
    // How it's found, before What to mask: the model first, marked new,
    // then the rules, both waiting on something to mask, with nothing to
    // press
    const found = await page.$$eval("#findList > .row", (rs) => rs.map((r) => [r.id, r.querySelector(".name").textContent, r.querySelector(".sub").textContent, r.querySelectorAll("button").length]));
    assert.deepEqual(found, [
      ["privacyModelRow", "Find with a local model" + "New", "Turn on Mask secrets or Mask personal data first", 0],
      ["maskRulesRow", "Rules", "Turn on Mask secrets or Mask personal data first", 0],
    ]);
    assert(await page.evaluate(() => document.querySelector("#findList").compareDocumentPosition(document.querySelector("#redactList")) & Node.DOCUMENT_POSITION_FOLLOWING), "How it's found comes before What to mask");
    assert.equal(await page.locator("#privacyModelRow.pm-wait").count(), 1);
    await browser.close();

    const two = await open(engine, "en", 1100, true, { supported: false, size }, []);
    t.after(() => two.browser.close());
    await two.page.waitForFunction(() => document.querySelector("#privacyModelRow .sub")?.textContent === "Not available on this system yet");
    assert.equal(await two.page.locator("#privacyModelRow .segs").count(), 0);
    assert.deepEqual([...errors, ...two.errors], []);
  });

  // the model is picked in the app's own menu beside the switch (#3578):
  // each model by its name, what it is and its size, in every language;
  // picking one says so to magpie, and nothing is cut or moved
  for (const lang of Object.keys(pickWords)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: the local model is picked from the app's menu`, async (t) => {
        const posts = [], model = { supported: true, installed: true, on: true, running: true, device: "CPU", size, others: [guard] };
        const { browser, page, errors } = await open(engine, lang, width, true, model, posts);
        t.after(() => browser.close());
        const row = page.locator("#privacyModelRow");
        await wheelTo(page, row, 300);
        const pick = row.locator("button.pm-model");
        await pick.waitFor();
        assert.equal(await page.locator("#privacyModelRow select").count(), 0, "no native select");
        assert.equal((await pick.textContent()).trim(), "Privacy Filter");
        let m = await look(page);
        assert(!m.hint && !m.cut && m.inside && !m.wide, `where it runs shown in full and inside: ${JSON.stringify(m)}`);
        // the menu and the switch are two boxes: they stand apart, on one line
        const gap = await page.evaluate(() => {
          const p = document.querySelector("#privacyModelRow .pm-model").getBoundingClientRect(), s = document.querySelector("#privacyModelRow .segs").getBoundingClientRect();
          return { gap: s.left - p.right, mid: Math.abs(p.top + p.height / 2 - (s.top + s.height / 2)) };
        });
        assert(gap.gap >= 6 && gap.gap <= 12 && gap.mid <= 1, `the menu stands apart from the switch: ${JSON.stringify(gap)}`);
        const before = await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]);
        await pick.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        const items = await menu.evaluate((b) => ({
          head: b.querySelector(".pm-head").textContent,
          items: [...b.querySelectorAll(".pm-item")].map((i) => [i.querySelector(".pm-name").textContent, i.querySelector(".pm-note").textContent, i.classList.contains("on")]),
          inside: b.getBoundingClientRect().left >= 0 && b.getBoundingClientRect().right <= innerWidth,
        }));
        assert.equal(items.head, pickWords[lang].head);
        assert.deepEqual(items.items.map((i) => i[0]), ["Privacy Filter", "AstrLink Guard"]);
        assert.deepEqual(items.items.map((i) => i[2]), [true, false], "the one in use is ticked");
        assert.equal(items.items[1][1], pickWords[lang].note);
        assert(items.items[0][1].includes("1.6 GB"), items.items[0][1]);
        assert(items.inside, "the menu is inside the window");
        await menu.locator(".pm-item", { hasText: "AstrLink Guard" }).click();
        await page.waitForFunction(() => !document.querySelector(".proto-menu"));
        assert.deepEqual(posts, ["pick astrlink-guard"]);
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]), before, "the pick moved nothing");
        assert.deepEqual(errors, []);
      });
    }
  }

  // the threshold (@Yayoi_no_yume on X): under the model, its default
  // or one the user picks from the app's menu, in every language; the
  // pick is sent with the model it is for, and shown once magpie has it
  for (const lang of Object.keys(scoreWords)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: the model's threshold is picked from the app's menu`, async (t) => {
        const w = scoreWords[lang];
        const posts = [], model = { ...guard, installed: true, on: true, running: true, device: "CPU", minScore: 0.8, defaultScore: 0.8 };
        const { browser, page, errors } = await open(engine, lang, width, true, model, posts);
        t.after(() => browser.close());
        const row = page.locator("#pmScoreRow");
        await row.waitFor();
        await wheelTo(page, row, 300);
        assert(await page.evaluate(() => document.querySelector("#privacyModelRow").nextElementSibling === document.querySelector("#pmScoreRow")), "right under the model");
        assert.equal(await row.locator("select").count(), 0, "no native select");
        const shown = () => row.evaluate((r) => {
          const sub = r.querySelector(".sub"), b = r.getBoundingClientRect(), list = r.closest(".list").getBoundingClientRect();
          return {
            name: r.querySelector(".name").textContent, sub: sub.textContent, pick: r.querySelector("button.pm-score").textContent.trim(),
            hint: getComputedStyle(sub).getPropertyValue("--hint").trim() === "1",
            cut: sub.scrollWidth > sub.clientWidth + 1 || sub.scrollHeight > sub.clientHeight + 1,
            inside: b.left >= list.left - 1 && b.right <= list.right + 1, wide: document.documentElement.scrollWidth > document.documentElement.clientWidth,
          };
        });
        let m = await shown();
        assert.equal(m.name, w.name);
        if (lang !== "en") assert(!/less sure/.test(m.sub), `translated: ${m.sub}`);
        assert.equal(m.pick, "0.8");
        assert(m.hint && m.inside && !m.wide, `the threshold's explanation rests out of sight, for the hover card: ${JSON.stringify(m)}`);
        const before = await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]);
        const menu = page.locator(".proto-menu");
        const items = async () => {
          await row.locator("button.pm-score").click();
          await menu.waitFor();
          return menu.evaluate((b) => ({
            head: b.querySelector(".pm-head").textContent,
            items: [...b.querySelectorAll(".pm-item")].map((i) => [i.querySelector(".pm-name").textContent, i.querySelector(".pm-note").textContent, i.classList.contains("on")]),
            inside: b.getBoundingClientRect().left >= 0 && b.getBoundingClientRect().right <= innerWidth,
          }));
        };
        let got = await items();
        assert.equal(got.head, w.name);
        assert.deepEqual(got.items.map((i) => i[0]), [w.def, "0.5", "0.6", "0.7", "0.85", "0.9", "0.95"]);
        assert.deepEqual(got.items.map((i) => i[2]), [true, false, false, false, false, false, false], "the default is ticked");
        assert.equal(got.items[1][1], w.most);
        assert(got.inside, "the menu is inside the window");
        await menu.locator(".pm-item", { hasText: /^0\.9$/ }).click();
        await page.waitForFunction(() => document.querySelector("#pmScoreRow button.pm-score")?.textContent.trim() === "0.9");
        assert.deepEqual(posts, ["score astrlink-guard 0.9"]);
        got = await items();
        assert.deepEqual(got.items.map((i) => i[2]), [false, false, false, false, false, true, false], "0.9 is ticked");
        await menu.locator(".pm-item").first().click();
        await page.waitForFunction(() => document.querySelector("#pmScoreRow button.pm-score")?.textContent.trim() === "0.8");
        assert.deepEqual(posts, ["score astrlink-guard 0.9", "score astrlink-guard 0"]);
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]), before, "the picks moved nothing");
        assert.deepEqual(errors, []);
      });
    }
  }
}
