// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Replies › Model name in replies (ZoeCats on Discord:
// since the asked-for name a reply's model was no longer the vendor's own,
// groups and all, with no way back but a header their client can't send).
// The row picks one of three by the app's menu, never a native select: the
// model asked for, the member that answered (#822; memberModel before it)
// and the vendor's own name. A user whose memberModel was on (the settings
// read says replyModel member) sees that one ticked; picking the vendor's
// saves replyModel with the rest of the settings kept and no memberModel,
// the page not moved by the click. In English, Simplified and Traditional
// Chinese, Japanese and German, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach, pickItem } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Model name in replies", head: "Replies", sub: "X-Magpie-Response-Model (asked, member or vendor)", asked: "The model asked for", member: "The member that answered", vendor: "The vendor's own name", note: "for every agent" },
  zh: { name: "回复里的模型名", head: "回复", sub: "只为该请求指定", asked: "所要的模型", member: "作答的成员", vendor: "厂商自己的名字", note: "对所有 agent" },
  "zh-TW": { name: "回覆裡的模型名稱", head: "回覆", sub: "只為該請求指定", asked: "所要的模型", member: "作答的成員", vendor: "廠商自己的名稱", note: "對所有 agent" },
  ja: { name: "応答のモデル名", head: "応答", sub: "そのリクエストだけを決めます", asked: "求めたモデル", member: "答えたメンバー", vendor: "ベンダー自身の名前", note: "どのエージェントにも" },
  de: { name: "Modellname in Antworten", head: "Antworten", sub: "wählt für sie allein", asked: "Das verlangte Modell", member: "Das Mitglied, das geantwortet hat", vendor: "Der eigene Name des Anbieters", note: "für jeden Agenten" },
};

function serve(lang, saved, start) {
  const settings = { lang, theme: "light", redact: true, ...start };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        saved.push(JSON.parse(r.request().postData()));
        Object.assign(settings, saved.at(-1));
      }
      return json(settings);
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(t, engine, lang, width, start) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const saved = [];
  await page.route("**/*", serve(lang, saved, start));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=models");
  await settingsDrawn(page); // rows found are the ones that stay (#1659)
  const row = page.locator("#replyList #replyModelRow");
  await row.waitFor();
  return { page, errors, saved, row };
}

// pick opens the row's menu as a reader does and presses the item named
// name, then waits for the one save
async function pick(page, row, name, saved) {
  const b = row.locator("button.sess-pick");
  // wheeled to as a reader would: the app puts back a scroll it didn't
  // get from the reader
  await reach(page, b);
  const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
  const box = await b.boundingBox();
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  const menu = page.locator(".proto-menu.sess-menu");
  await menu.waitFor();
  const items = menu.locator(".pm-item");
  const shown = { names: await items.locator(".pm-name").allTextContents(), ticked: await menu.locator('.pm-item[aria-checked="true"] .pm-name').allTextContents(), notes: await items.locator(".pm-note").allTextContents() };
  await pickItem(page, items.filter({ has: page.locator(".pm-name", { hasText: name }) }));
  for (let i = 0; i < 50 && !saved.length; i++) await page.waitForTimeout(50);
  const after = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
  return { shown, moved: JSON.stringify(after) !== JSON.stringify(before) };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}: a reply's model can be the vendor's own name again`, async (t) => {
      // a user whose memberModel was on: the settings read says member
      const { page, errors, saved, row } = await open(t, engine, lang, width, { memberModel: true, replyModel: "member" });
      assert.equal(await row.locator(".name").textContent(), w.name);
      assert((await row.locator(".sub").textContent()).includes(w.sub));
      assert.equal(await page.locator("#replyList").evaluate((l) => l.previousElementSibling.textContent.trim()), w.head);
      assert.equal(await row.locator("select").count(), 0, "no native select");
      assert.equal((await row.locator("button.sess-pick").textContent()).trim(), w.member, "memberModel on shows the member");
      // its explanation rests out of sight, shown whole in the row's hover card
      // (settings-hints.test.cjs)
      assert.equal(await row.locator(".sub").first().evaluate((e) => getComputedStyle(e).getPropertyValue("--hint").trim()), "1", "the explanation shows at rest");
      for (const part of [".name", "button.sess-pick"]) {
        const fit = await row.locator(part).first().evaluate((e) => {
          const r = e.getBoundingClientRect();
          return e.scrollWidth <= e.clientWidth + 1 && r.width > 0 && r.right <= innerWidth + 1;
        });
        assert(fit, `${part} fits at ${width}`);
      }

      const { shown, moved } = await pick(page, row, w.vendor, saved);
      assert.deepEqual(shown.names, [w.asked, w.member, w.vendor]);
      assert.deepEqual(shown.ticked, [w.member]);
      assert(shown.notes[2].includes(w.note), shown.notes[2]);
      // the member is named to every agent too, Claude Code and Codex
      // included (ZoeCats, after v0.1.2760)
      assert(shown.notes[1].includes(w.note), shown.notes[1]);
      assert(!/Claude Code/.test(shown.notes[1]), shown.notes[1]);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].replyModel, "vendor");
      assert(!("memberModel" in saved[0]), "memberModel isn't sent beside replyModel");
      assert.equal(saved[0].redact, true, "the rest kept");
      assert.equal(saved[0].lang, lang);
      assert(!moved, "the click moved nothing");
      await page.waitForFunction((v) => document.querySelector("#replyModelRow button.sess-pick")?.textContent.trim() === v, w.vendor);
      assert.deepEqual(errors, []);
    });
  }
  test(`${engine} en: the model asked for is the default, and the member can be picked`, async (t) => {
    const w = words.en;
    const { page, errors, saved, row } = await open(t, engine, "en", 1100, {});
    assert.equal((await row.locator("button.sess-pick").textContent()).trim(), w.asked);
    const { shown } = await pick(page, row, w.member, saved);
    assert.deepEqual(shown.ticked, [w.asked]);
    assert.equal(saved.length, 1, "saved once");
    assert.equal(saved[0].replyModel, "member");
    assert.deepEqual(errors, []);
  });
}
