// Run with Node's test runner and Playwright on the module path; see README.md.
// omp's advisor (#6408, Awad_Yoo on X): in omp's own settings, in the
// window's opened row and in the panel's sheet, three rows as omp's
// config.yml has them (omp_advisor.go): whether it reviews and when
// (Off, Every turn, At the end), how long the agent waits for its review,
// and which note calls a finished agent back. The last two show only while
// it reviews, and a pick writes its field. With the advisor off, its role's
// line in the roles square says so. English, both Chinese, Japanese and
// German, wide and at 390px, Chromium and WebKit. The API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "magpie/deepseek/pro", label: "DeepSeek Pro", ref: "deepseek/pro" }];
const st = (...v) => v.map((value) => ({ value }));
const fresh = (review) => ({
  agents: [{
    id: "omp", name: "omp", path: "/test/config.yml", icon: "omp", wired: true,
    fields: [
      { key: "model", label: "model", value: "magpie/deepseek/pro", options },
      { key: "advisor", label: "advisor", value: "magpie/deepseek/pro", options },
      { key: "advisor_thinking", label: "advisor thinking", value: "", options: st("auto", "low", "high") },
      { key: "advisor_review", label: "advisor review", value: review, options: st("off", "turn", "agent-end") },
      { key: "advisor_wait", label: "advisor wait", value: "", options: st("off", "1", "3", "5", "strict") },
      { key: "advisor_wake", label: "advisor wakes on", value: "", options: st("nit", "concern", "blocker") },
    ],
  }],
  profiles: [],
});

function server(lang, sets, review) {
  let cur = fresh(review);
  const settings = { theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", redactWords: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      sets.push({ agent: body.agent, field: body.field, value: body.value });
      cur = JSON.parse(JSON.stringify(cur));
      cur.agents[0].fields.find((f) => f.key === body.field).value = body.value;
      return route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
    }
    if (url.pathname === "/api/settings") return route.fulfill({ json: settings });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

// the words as i18n.js has them
const W = {
  en: { names: ["Advisor", "Waits for its review", "Calls a finished agent back on"], review: ["Off", "Every turn", "At the end"], wait: ["Don't wait", "Up to 30s", "Until done"], wake: ["Any note", "Concern", "Blocker"], off: "off", role: "advisor" },
  zh: { names: ["顾问", "等它审完", "叫回已结束的 agent"], review: ["关闭", "每轮", "结束时"], wait: ["不等", "最多 30 秒", "等到审完"], wake: ["任何建议", "问题", "阻断"], off: "关闭", role: "顾问" },
  "zh-TW": { names: ["顧問", "等它審完", "叫回已結束的 agent"], review: ["關閉", "每輪", "結束時"], wait: ["不等", "最多 30 秒", "等到審完"], wake: ["任何建議", "問題", "阻斷"], off: "關閉", role: "顧問" },
  ja: { names: ["アドバイザー", "レビューを待つ", "終了した agent を呼び戻す"], review: ["オフ", "毎ターン", "終了時"], wait: ["待たない", "最大 30 秒", "完了まで"], wake: ["すべての指摘", "懸念", "ブロッカー"], off: "オフ", role: "アドバイザー" },
  de: { names: ["Berater", "Wartet auf seine Prüfung", "Holt einen fertigen Agenten zurück bei"], review: ["Aus", "Jede Runde", "Am Ende"], wait: ["Nicht warten", "Bis zu 30 s", "Bis fertig"], wake: ["Jedem Hinweis", "Bedenken", "Blocker"], off: "aus", role: "Berater" },
};

const omp = '.row.agent[data-id="omp"]';
// each advisor row: its name, its options and the one on
const rowsIn = (page, box) => page.locator(`${box} .row.pref.ag-advisor`).evaluateAll((rs) => rs.map((r) => [
  r.dataset.key, r.querySelector(".name").textContent, [...r.querySelectorAll(".segs .opt")].map((o) => o.textContent),
  r.querySelector(".segs .opt.on")?.dataset.seg || "",
]));
const waitRows = (page, box, n) => page.waitForFunction(([box, n]) => document.querySelectorAll(box + " .row.pref.ag-advisor").length === n, [box, n]);
const opt = (page, box, key, seg) => page.locator(`${box} .row.pref.ag-advisor[data-key="${key}"] .segs .opt[data-seg="${seg}"]`);

async function open(engine, width, lang, t, review, url = "http://magpie.test/") {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width, height: 760 } });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], sets = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, sets, review));
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-omp-advisor.png`) });
    }
    await browser.close();
  });
  await page.goto(url);
  await page.locator(omp).waitFor();
  return { page, errors, sets };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 390]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang} ${width}px: omp's advisor, off, then reviewing at the end, its wait and its call-back`, async (t) => {
      const w = W[lang];
      const { page, errors, sets } = await open(engine, width, lang, t, "off");
      await page.locator(`${omp} .ag-link`).click();
      await waitRows(page, omp, 1);
      // off: one row, and the roles square's advisor line says so
      assert.deepEqual(await rowsIn(page, omp), [["advisor_review", w.names[0], w.review, "off"]]);
      assert.equal(await page.locator(`${omp} .field:not(.extra)[data-key^="advisor_"]`).count(), 0, "an advisor setting got a picker of its own");
      const square = page.locator(`${omp} .field.extra[data-key="tiers"]`);
      await square.click();
      await page.locator("#pop:not([hidden]) #list li").first().waitFor();
      const line = await page.locator("#pop:not([hidden]) #list li", { hasText: w.role }).first().innerText();
      assert.ok(line.includes("DeepSeek Pro · " + w.off), line);
      await page.keyboard.press("Escape");
      await page.waitForFunction(() => !document.querySelector("#pop:not([hidden])"));

      // At the end: written, and the wait and call-back rows come
      await opt(page, omp, "advisor_review", "agent-end").click();
      await waitRows(page, omp, 3);
      assert.deepEqual(sets, [{ agent: "omp", field: "advisor_review", value: "agent-end" }]);
      assert.deepEqual(await rowsIn(page, omp), [
        ["advisor_review", w.names[0], w.review, "agent-end"],
        ["advisor_wait", w.names[1], w.wait, "off"],
        ["advisor_wake", w.names[2], w.wake, "concern"],
      ]);
      await opt(page, omp, "advisor_wait", "1").click();
      await page.waitForFunction(() => document.querySelector('.row.pref.ag-advisor[data-key="advisor_wait"] .opt.on')?.dataset.seg === "1" && document.querySelectorAll('.row.pref.ag-advisor').length === 3);
      await opt(page, omp, "advisor_wake", "blocker").click();
      await page.waitForFunction(() => document.querySelector('.row.pref.ag-advisor[data-key="advisor_wake"] .opt.on')?.dataset.seg === "blocker");
      for (let i = 0; sets.length < 3 && i < 100; i++) await page.waitForTimeout(50);
      assert.deepEqual(sets.slice(1), [{ agent: "omp", field: "advisor_wait", value: "1" }, { agent: "omp", field: "advisor_wake", value: "blocker" }]);
      // its own value picked again writes nothing
      await opt(page, omp, "advisor_wake", "blocker").click();
      await page.waitForTimeout(150);
      assert.equal(sets.length, 3);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the row scrolls sideways");
      assert.deepEqual(errors, []);
    });
  }
  for (const lang of ["en", "zh", "de"]) {
    test(`${engine} ${lang}: in the panel the agent's sheet has the advisor's rows, and they answer in place`, async (t) => {
      const w = W[lang];
      const { page, errors, sets } = await open(engine, 360, lang, t, "", "http://magpie.test/?mode=panel&view=agents");
      await page.locator(`${omp} .ag-sum`).click();
      const line = page.locator(`${omp} .agent-prefs-open`);
      await line.waitFor();
      assert.ok((await line.textContent()).includes(w.names[0]), await line.textContent());
      await line.click();
      const sheet = "#modal .editor.agent-prefs";
      await page.locator(sheet).waitFor();
      await waitRows(page, sheet, 1);
      // unset reads as omp's default, off
      assert.deepEqual(await rowsIn(page, sheet), [["advisor_review", w.names[0], w.review, "off"]]);
      await opt(page, sheet, "advisor_review", "turn").click();
      await waitRows(page, sheet, 3);
      assert.deepEqual(sets, [{ agent: "omp", field: "advisor_review", value: "turn" }]);
      await opt(page, sheet, "advisor_wait", "strict").click();
      await page.waitForFunction((s) => document.querySelector(s + ' .row.pref.ag-advisor[data-key="advisor_wait"] .opt.on')?.dataset.seg === "strict", sheet);
      for (let i = 0; sets.length < 2 && i < 100; i++) await page.waitForTimeout(50);
      assert.deepEqual(sets[1], { agent: "omp", field: "advisor_wait", value: "strict" });
      // off again: the two go
      await opt(page, sheet, "advisor_review", "off").click();
      await waitRows(page, sheet, 1);
      assert.deepEqual(sets[2], { agent: "omp", field: "advisor_review", value: "off" });
      assert(await page.locator("#modal .dialog").evaluate((d) => d.scrollWidth <= d.clientWidth + 1), `${lang}: the sheet fits 360`);
      assert.deepEqual(errors, []);
    });
  }
}
