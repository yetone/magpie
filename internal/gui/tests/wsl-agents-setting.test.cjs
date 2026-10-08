// Run with Node's test runner and Playwright on the module path; see README.md.
// #1264 (Etsuya233): magpie on Windows lists the WSL distros and probes the
// running ones for agents, with no way to stop it. Settings → General has a
// "Detect agents in WSL" row where there is WSL (the settings say wsl),
// on by default: Off and On are saved as noWSLAgents true and false, the
// click moving nothing, and another setting's save keeps it. Where there
// is no WSL (a Mac, Linux) the row isn't shown. In every language, at
// 900px and 440px, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Detect agents in WSL", sub: "Looks in running WSL distros for agents to set up, their sessions and Claude Code; off, WSL is never asked anything and the agents found there are no longer shown", on: "On", off: "Off" },
  zh: { name: "检测 WSL 中的 Agent", sub: "在运行中的 WSL 发行版里查找可接入的 Agent、它们的会话和 Claude Code；关闭后不再向 WSL 询问任何内容，之前在那里找到的 Agent 也不再显示", on: "开启", off: "关闭" },
  "zh-TW": { name: "偵測 WSL 中的 Agent", sub: "在執行中的 WSL 發行版裡尋找可接入的 Agent、它們的工作階段和 Claude Code；關閉後不再向 WSL 詢問任何內容，之前在那裡找到的 Agent 也不再顯示", on: "開啟", off: "關閉" },
  ja: { name: "WSL のエージェントを検出", sub: "実行中の WSL ディストリビューションで、設定できるエージェント、そのセッション、Claude Code を探します。オフにすると WSL には一切問い合わせず、そこで見つかったエージェントも表示しません", on: "オン", off: "オフ" },
  de: { name: "Agenten in WSL erkennen", sub: "Sucht in laufenden WSL-Distributionen nach einrichtbaren Agenten, ihren Sitzungen und Claude Code; aus, wird WSL nie etwas gefragt, und die dort gefundenen Agenten werden nicht mehr angezeigt", on: "An", off: "Aus" },
};

function serve(lang, posted, st) {
  const settings = () => ({ lang, theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        posted.push(b);
        Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, posted, st, t, width = 900) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 700 } })).newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posted, st));
  await page.goto("http://magpie.test/?view=settings&tab=general");
  await page.locator("#setPage-general .row.pref").first().waitFor();
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) for (const width of [900, 440]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: WSL detection is turned off and on in Settings → General`, async (t) => {
      const posted = [], st = { wsl: true };
      const { page, errors } = await open(engine, lang, posted, st, t, width);
      const row = page.locator("#wslAgentsRow");
      await row.waitFor();
      await page.mouse.move(width / 2, 350);
      const end = () => page.evaluate(() => { const m = document.querySelector("#view-settings"); return m.scrollTop + m.clientHeight >= m.scrollHeight - 1; });
      for (let i = 0; i < 20 && !(await end()); i++) {
        await page.mouse.wheel(0, 400);
        await page.waitForTimeout(150);
      }
      assert.equal(await row.locator(".name").innerText(), w.name);
      assert.equal(await row.locator(".sub").innerText(), w.sub);
      const opt = (name) => row.locator("#wslAgentsSegs .opt").getByText(name, { exact: true });
      assert.equal(await row.locator("#wslAgentsSegs .opt.on").innerText(), w.on, "on by default");
      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)]);
      const before = await where();
      const click = async (loc) => {
        await page.waitForFunction(() => prefsBusy === 0);
        const b = await loc.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      await click(opt(w.off));
      for (let i = 0; i < 50 && posted.at(-1)?.noWSLAgents !== true; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1)?.noWSLAgents, true, `posted ${JSON.stringify(posted)}`);
      assert.equal(await row.locator("#wslAgentsSegs .opt.on").innerText(), w.off);
      // another setting saved keeps it off
      const n = posted.length;
      await click(page.locator("#keepAwakeSegs .opt").nth(1));
      for (let i = 0; i < 50 && posted.length === n; i++) await page.waitForTimeout(50);
      assert(posted.length > n, "the other setting wasn't saved");
      assert.equal(posted.at(-1).noWSLAgents, true, "another setting's save turned WSL detection on");
      const fit = await page.evaluate(() => {
        const row = document.querySelector("#wslAgentsRow").getBoundingClientRect();
        return [...document.querySelectorAll("#wslAgentsSegs .opt")].every((o) => { const b = o.getBoundingClientRect(); return b.width > 0 && b.right <= row.right + 1 && o.scrollWidth <= o.clientWidth + 1; });
      });
      assert(fit, "the segments don't fit the row");
      await click(opt(w.on));
      for (let i = 0; i < 50 && posted.at(-1)?.noWSLAgents !== false; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1).noWSLAgents, false);
      assert.deepEqual(await where(), before, "the clicks moved the page");
      const left = await row.evaluate((e) => getComputedStyle(e).borderLeftWidth);
      assert(parseFloat(left) <= 1, `left border ${left}`);
      assert.deepEqual(errors, []);
    });

    if (width === 900) test(`${engine} ${lang}: no WSL, no row; off is shown as saved`, async (t) => {
      const a = await open(engine, lang, [], {}, t);
      await a.page.locator("#keepAwakeRow").waitFor();
      assert.equal(await a.page.locator("#wslAgentsRow").isVisible(), false, "shown without WSL");
      const b = await open(engine, lang, [], { wsl: true, noWSLAgents: true }, t);
      await b.page.locator("#wslAgentsRow").waitFor();
      assert.equal(await b.page.locator("#wslAgentsSegs .opt.on").innerText(), w.off);
      assert.deepEqual([...a.errors, ...b.errors], []);
    });
  }
}
