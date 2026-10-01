// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage alerts (#368): the Settings page's "Usage alert" and "Low balance
// alert" rows start Off; On saves 80% (and 5 for a balance), the field
// beside it saves the number typed, a share outside 1-100 is put back
// unsaved, and a choice saved later elsewhere on the page keeps the alerts
// (the page sends all its settings each time, and the server keeps only
// what it is sent). When the system has magpie's notifications turned off
// the rows say so. No click moves the page, and the rows have no coloured
// left border. English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

// server answers as magpie does: a POST to /api/settings replaces what is
// kept with what the page sent, and the notifications' state (problem) is
// said while an alert is on.
function server(lang, posts, problem) {
  const fixed = settingsPayload({ lang });
  let cur = fixed;
  const answer = () => ((cur.usageAlert || cur.balanceAlert) && problem ? { ...cur, notifyProblem: problem } : cur);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: fixed.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...fixed, ...body };
      }
      return json(answer());
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

const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

// wheelTo scrolls the settings with the wheel, as a reader would (a script
// setting scrollTop is put back), until the alert rows are mid-page.
async function wheelTo(page) {
  await page.locator("#view-settings").hover();
  for (let i = 0; i < 40; i++) {
    const box = await page.locator("#balanceAlertRow").boundingBox();
    if (box && box.y + box.height < 300 && await view(page) > 0) return;
    await page.mouse.wheel(0, 120);
    await page.waitForTimeout(30);
  }
  throw new Error("couldn't scroll the alert rows into view");
}

const words = {
  en: {
    usage: "Usage alert", balance: "Low balance alert", off: "Off", on: "On",
    usageSub: "A notification when a 5-hour, weekly or monthly window reaches this share used, once each time it runs",
    balanceSub: "A notification when a balance falls to this amount, in its own currency or credits, once until it is topped up",
    denied: "Notifications are turned off for magpie in the system's settings",
    share: "Share used", amount: "Amount", cny: "¥ CNY",
  },
  zh: {
    usage: "用量提醒", balance: "余额提醒", off: "关闭", on: "开启",
    usageSub: "5 小时、每周或每月窗口用到这个比例时发一条系统通知，每个周期只提醒一次",
    balanceSub: "余额降到这个数（按它自己的货币或积分）时发一条系统通知，充值回升前只提醒一次",
    denied: "系统设置里关闭了 magpie 的通知",
    share: "已用比例", amount: "金额", cny: "¥ 人民币",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": usage and balance alerts are set on the Settings page", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-usage-alert-${i}.png`) });
      }
      await browser.close();
    });

    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang, async () => {
        const errors = [], posts = [];
        const context = await browser.newContext({ viewport: { width: 900, height: 480 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts, ""));
        const posted = async (n) => { for (let i = 0; i < 100 && posts.length < n; i++) await page.waitForTimeout(20); assert.equal(posts.length, n, "posts"); return posts[n - 1]; };
        const settled = () => page.waitForTimeout(300);

        await page.goto("http://magpie.test/?view=settings&tab=usage");
        const usageSegs = page.locator("#usageAlertSegs .opt"), balSegs = page.locator("#balanceAlertSegs .opt");
        await usageSegs.first().waitFor();

        // the rows, in the page's language, Off to begin with
        assert.equal((await page.locator("#usageAlertRow .name").textContent()).trim(), w.usage);
        assert.equal((await page.locator("#balanceAlertRow .name").textContent()).trim(), w.balance);
        assert.equal((await page.locator("#usageAlertSub").textContent()).trim(), w.usageSub);
        assert.equal((await page.locator("#balanceAlertSub").textContent()).trim(), w.balanceSub);
        assert.deepEqual(await usageSegs.allTextContents(), [w.off, w.on]);
        assert.equal(await page.locator("#usageAlertSegs .opt.on").textContent(), w.off);
        assert.equal(await page.locator("#balanceAlertSegs .opt.on").textContent(), w.off);
        assert.equal(await page.locator("#usageAlertSegs input, #balanceAlertSegs input").count(), 0, "no field while off");

        // scrolled down with the wheel (the rows to the middle, clear of
        // the footer), turning it on moves nothing
        await wheelTo(page);
        const before = await view(page);
        assert(before > 0, "the settings list must be long enough to scroll");
        await usageSegs.nth(1).click();
        assert.equal((await posted(1)).usageAlert, 80, "On saves 80%");
        const share = page.locator("#usageAlertSegs input");
        await share.waitFor();
        await settled();
        assert.equal(await view(page), before, "turning the alert on must not scroll the settings page");
        assert.equal(await share.inputValue(), "80");
        assert.equal(await share.getAttribute("aria-label"), w.share);
        assert.equal((await page.locator("#usageAlertSegs .unit").textContent()), "%");
        assert.equal(await page.locator("#usageAlertSegs .opt.on").textContent(), w.on);

        // a share typed in is saved; one past 100 is put back, unsaved
        await share.fill("90");
        await share.press("Enter");
        assert.equal((await posted(2)).usageAlert, 90);
        await settled();
        await share.fill("150");
        await share.press("Enter");
        await settled();
        assert.equal(posts.length, 2, "150% is not saved");
        assert.equal(await share.inputValue(), "90");

        // the balance: On saves 5, then the amount typed
        await balSegs.nth(1).click();
        const b = await posted(3);
        assert.equal(b.balanceAlert, 5);
        assert.equal(b.usageAlert, 90, "the usage alert is sent along");
        const amount = page.locator("#balanceAlertSegs input");
        await amount.waitFor();
        assert.equal(await amount.getAttribute("aria-label"), w.amount);
        await amount.fill("2.5");
        await amount.press("Enter");
        assert.equal((await posted(4)).balanceAlert, 2.5);
        await settled();

        // another setting saved later keeps both alerts
        await page.locator("#currencySegs .opt", { hasText: w.cny }).click();
        const c = await posted(5);
        assert.equal(c.currency, "cny");
        assert.equal(c.usageAlert, 90, "the usage alert is kept by another save");
        assert.equal(c.balanceAlert, 2.5, "the balance alert is kept by another save");
        await settled();
        assert.equal(await share.inputValue(), "90");
        assert.equal(await view(page), before, "no click moved the page");

        // Off saves 0, and the field goes
        await usageSegs.nth(0).click();
        assert.equal((await posted(6)).usageAlert, 0);
        await settled();
        assert.equal(await page.locator("#usageAlertSegs input").count(), 0);

        // no coloured stripe down a row's left
        for (const sel of ["#usageAlertRow", "#balanceAlertRow", "#usageAlertSub", "#balanceAlertSub"]) {
          assert.equal(await page.locator(sel).evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px", sel);
        }
        assert.deepEqual(errors, []);
        await context.close();
      });

      await t.test(lang + ": notifications turned off in the system", async () => {
        const errors = [], posts = [];
        const context = await browser.newContext({ viewport: { width: 900, height: 480 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts, "denied"));
        await page.goto("http://magpie.test/?view=settings&tab=usage");
        await page.locator("#usageAlertSegs .opt").first().waitFor();
        await wheelTo(page);
        // nothing said while every alert is off
        assert.equal((await page.locator("#usageAlertSub").textContent()).trim(), w.usageSub);
        await page.locator("#usageAlertSegs .opt").nth(1).click();
        await page.locator("#usageAlertSegs input").waitFor();
        await page.waitForTimeout(300);
        for (const id of ["#usageAlertSub", "#balanceAlertSub"]) {
          const sub = page.locator(id);
          assert((await sub.textContent()).includes(w.denied), `${id} says notifications are off: ${await sub.textContent()}`);
          assert.equal((await sub.locator(".warn").textContent()), w.denied, "the problem stands out");
          assert.equal(await sub.evaluate((e) => getComputedStyle(e).borderLeftWidth), "0px");
        }
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });
}
