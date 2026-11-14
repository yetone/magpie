// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › About › Download source (#893): updates come from usemagpie.ai,
// then GitHub, or a mirror typed in for GitHub (a full https:// address, the
// same setting as `magpie update mirror`), and no mirror is one click back. A download that
// failed through the mirror says it's the mirror's and offers GitHub, which
// takes the mirror away and downloads again. No click moves the page,
// nothing is a native select, no left border. English and Chinese; the
// line's way to the docs' Mirrors page and what a mirror's address is, in
// every language, wide and narrow. No backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { row: "Download source", use: "Use a mirror…", save: "Save", github: "Remove mirror", change: "Change", bad: "full https:// address",
    tip: "a file from the mirror that doesn't match it is not installed",
    viaGitHub: "from usemagpie.ai, and from GitHub when that fails", viaMirror: "and through this mirror when that fails", failed: "through the mirror", official: "Download from GitHub" },
  zh: { row: "下载源", use: "使用镜像…", save: "保存", github: "不用镜像", change: "修改", bad: "完整的 https:// 地址",
    tip: "从镜像下载的文件如果与校验和不符，magpie 不会安装它",
    viaGitHub: "从 usemagpie.ai 下载，失败时改从 GitHub 下载", viaMirror: "失败时经此镜像下载", failed: "经镜像", official: "改用官方下载" },
};

const M = "https://mirror.example/gh/";

function server(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status) => route.fulfill({ json: data, status: status || 200 });
    const settings = () => ({ theme: "light", lang, version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", lanURLs: [], fx: { rate: 7.2 }, ...(ctl.mirror ? { updateMirror: ctl.mirror } : {}) });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings/update-mirror") {
      const body = req.postDataJSON();
      ctl.posts.push(["mirror", body]);
      if (body.mirror && !body.mirror.startsWith("https://")) return json({ error: "a mirror is a full https:// address, like https://mirror.example/" }, 400);
      ctl.mirror = body.mirror;
      return json(settings());
    }
    if (url.pathname === "/api/update/install") {
      ctl.posts.push(["install", req.postDataJSON()]);
      ctl.update = { state: "downloading", current: "0.1.900", latest: "0.1.901" };
      return json(ctl.update);
    }
    if (url.pathname === "/api/update") return json(ctl.update || { state: "latest", current: "0.1.900" });
    if (url.pathname === "/api/open") { ctl.posts.push(["open", req.postDataJSON()]); return json({}); }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function showRow(page, row) {
  await page.locator("#view-settings").hover();
  for (let i = 0; i < 40; i++) {
    const box = await row.boundingBox();
    const v = await page.locator("#view-settings").boundingBox();
    if (box && v && box.y >= v.y + 4 && box.y + box.height <= v.y + v.height - 4) break;
    await page.mouse.wheel(0, 180);
    await page.waitForTimeout(16);
  }
  await page.waitForTimeout(250);
}
const scrolls = (page) => page.evaluate(() => [window.scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)].join(","));

async function open(browser, lang, ctl, errors, width) {
  const page = await (await browser.newContext({ viewport: { width: width || 900, height: 760 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
  await page.route("**/*", server(lang, ctl));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings");
  await settingsDrawn(page); // rows found are the ones that stay (#1659)
  await openPart(page, "about");
  await page.locator("#setPage-about").waitFor({ state: "visible" });
  const row = page.locator("#about .row.pref.update-mirror-row");
  await row.waitFor();
  return { page, row };
}

async function click(page, loc) {
  const before = await scrolls(page);
  await loc.click();
  await page.waitForTimeout(150);
  assert.equal(await scrolls(page), before, "the click must not scroll the page");
}

// the row's way to the docs' Mirrors page, and, typing one, what a
// mirror's address is (WeltAn_X on X), in every language
const how = {
  en: { use: "Use a mirror…", more: "How mirrors work", hint: "goes before the release's github.com link", docs: "https://usemagpie.ai/docs/mirrors#settings" },
  zh: { use: "使用镜像…", more: "镜像怎么用", hint: "加在发布文件的 github.com 链接前面", docs: "https://usemagpie.ai/docs/zh/mirrors#settings" },
  "zh-TW": { use: "使用鏡像…", more: "鏡像怎麼用", hint: "加在釋出檔案的 github.com 連結前面", docs: "https://usemagpie.ai/docs/zh/mirrors#settings" },
  ja: { use: "ミラーを使う…", more: "ミラーの使い方", hint: "github.com リンクの前に付けます", docs: "https://usemagpie.ai/docs/ja/mirrors#settings" },
  de: { use: "Spiegel verwenden…", more: "Wie Spiegel funktionieren", hint: "vor dem github.com-Link des Releases", docs: "https://usemagpie.ai/docs/mirrors#settings" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Download source says what a mirror is and links the docs, in every language", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(how)) for (const width of [900, 420]) {
      const w = how[lang];
      await t.test(lang + " at " + width + "px", async () => {
        const ctl = { posts: [] };
        const errors = [];
        const { page, row } = await open(browser, lang, ctl, errors, width);
        const more = row.locator(".sub button.sub-more");
        assert.equal((await more.innerText()).trim(), w.more);
        await showRow(page, row);
        const at = page.url();
        await click(page, more);
        assert.deepEqual(ctl.posts, [["open", { url: w.docs }]], "the docs open outside, once");
        assert.equal(page.url(), at, "the window stays where it was");
        // typing one, the line says where its address goes; Cancel puts the line back
        await click(page, row.locator("button", { hasText: w.use }));
        await row.locator("input.update-mirror-input").waitFor();
        await row.locator(".sub", { hasText: w.hint }).waitFor();
        assert.equal(await row.locator(".sub button.sub-more").count(), 1);
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        const box = await row.boundingBox(), sub = await row.locator(".sub").boundingBox();
        assert.ok(sub.x + sub.width <= box.x + box.width + 1, "the line stays inside its row");
        await row.locator("input.update-mirror-input").press("Escape");
        await row.locator("input.update-mirror-input").waitFor({ state: "detached" });
        assert.equal(await row.locator(".sub", { hasText: w.hint }).count(), 0);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });

  test(engine + ": Download source sets a mirror, and GitHub is one click back", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang + ": a mirror typed in, a bad one refused, GitHub back", async () => {
        const ctl = { posts: [] };
        const errors = [];
        const { page, row } = await open(browser, lang, ctl, errors);
        assert.equal((await row.locator(".name").innerText()).trim(), w.row);
        await row.locator(".sub", { hasText: w.viaGitHub }).waitFor();
        assert.equal((await row.locator("code").innerText()).trim(), "usemagpie.ai");
        // the tooltip's second half reads as a sentence (AlwaySleepyZzz on Discord)
        assert.ok((await row.locator(".sub").getAttribute("title")).includes(w.tip), "the tooltip says a mismatched mirror file is not installed");
        assert.equal(await page.locator("#about select").count(), 0, "no native select");
        const widths = await row.evaluate((e) => [e, ...e.querySelectorAll("*")].map((x) => parseFloat(getComputedStyle(x).borderLeftWidth) || 0));
        assert.ok(widths.every((x) => x === 0), "no left border");
        await showRow(page, row);

        // http is refused, and says why; the draft stays
        await click(page, row.locator("button", { hasText: w.use }));
        const input = row.locator("input.update-mirror-input");
        await input.waitFor();
        await input.fill("http://mirror.example/");
        await click(page, row.locator("button", { hasText: w.save }));
        await row.locator(".sub.err", { hasText: w.bad }).waitFor();
        assert.equal(await row.locator("input.update-mirror-input").inputValue(), "http://mirror.example/");

        // an https one is saved, and shown
        await row.locator("input.update-mirror-input").fill(M);
        await row.locator("input.update-mirror-input").press("Enter");
        await row.locator(".sub", { hasText: w.viaMirror }).waitFor();
        assert.equal((await row.locator("code").innerText()).trim(), M);
        assert.deepEqual(ctl.posts, [["mirror", { mirror: "http://mirror.example/" }], ["mirror", { mirror: M }]]);

        // GitHub again
        await click(page, row.locator("button", { hasText: w.github }));
        await row.locator(".sub", { hasText: w.viaGitHub }).waitFor();
        assert.deepEqual(ctl.posts.at(-1), ["mirror", { mirror: "" }]);
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(lang + ": a download failed through the mirror offers GitHub", async () => {
        const ctl = { posts: [], mirror: M,
          update: { state: "error", current: "0.1.900", latest: "0.1.901", retry: true, mirror: M, error: "through the mirror " + M + ": 502 Bad Gateway" } };
        const errors = [];
        const { page } = await open(browser, lang, ctl, errors);
        const ver = page.locator("#about .row.pref", { has: page.locator(".sub", { hasText: w.failed }) });
        await ver.waitFor();
        assert.match(await ver.locator(".sub").innerText(), new RegExp(M.replace(/[./]/g, "\\$&")));
        const official = ver.locator("button", { hasText: w.official });
        await showRow(page, ver);
        await click(page, official);
        await page.locator("#about .row.pref.update-mirror-row .sub", { hasText: w.viaGitHub }).waitFor();
        assert.deepEqual(ctl.posts.map((p) => p[0]), ["mirror", "install"]);
        assert.deepEqual(ctl.posts[0][1], { mirror: "" });
        assert.equal(await page.locator("#about button", { hasText: w.official }).count(), 0, "downloading, no longer offered");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
