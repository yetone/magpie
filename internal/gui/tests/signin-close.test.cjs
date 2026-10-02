// Run with Node's test runner and Playwright on the module path; see README.md.
// A sign-in for another account, left unfinished, can be put away (#526:
// 当我点击添加第二个账号，但最后没添加时，这个请在浏览器中完成登录的窗口状态不会释放，
// 无论我点击底部的移除、取消、保存都没办法 — 希望在"重新打开"按钮旁边再添加一个"关闭"按钮).
// In Qoder's editor, Add another account → Sign in anyway waits on the
// browser with Qoder's long device link. The box's Cancel stays inside the
// account list, the link never pushing it out of sight: it tells magpie to
// drop the sign-in (signin/<id>/cancel), stops asking after it, and the Add
// another row is back. It is the one button that does so: a Close beside
// Open again did the same (mintonight, on #526: 取消和关闭功能不是重复了吗，
// 只保留一个就行了). The editor's own
// Cancel and Save put a waiting sign-in away too, so the editor opened again
// is as it was.
// No click moves the page. English and Chinese, Chromium and WebKit; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LINK = "https://qoder.com/device/" + "a1b2c3d4e5".repeat(14) + "LprwDu";
const account = {
  id: "qoder", name: "Qoder", icon: "qoder", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3.8-max", name: "Qwen3.8-Max", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "qoder", agentName: "Qoder", user: "alice@example.com", logins: [{ user: "alice@example.com", active: true, on: true, own: true, plan: "personal_standard" }] },
};

function serve(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [account], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/signin" && req.method() === "POST") {
      ctl.started++;
      return json({ id: "q" + ctl.started, agent: "qoder", state: "waiting", url: LINK, instructions: "" });
    }
    const m = /^\/api\/signin\/([^/]+)(\/cancel)?$/.exec(url.pathname);
    if (m && m[2]) { ctl.canceled.push(m[1]); return route.fulfill({ status: 204 }); }
    if (m) {
      ctl.polled.push(m[1]);
      return json({ id: m[1], agent: "qoder", state: ctl.canceled.includes(m[1]) ? "canceled" : "waiting", url: LINK });
    }
    if (url.pathname === "/api/provider/save") { ctl.saved.push(req.postDataJSON().id); return json({ providers: [account], presets: [], excluded: [], gateway: { running: true, window: true } }); }
    if (url.pathname === "/api/open") { ctl.opened.push(req.postDataJSON().url); return json({}); }
    if (url.pathname.startsWith("/api/")) { ctl.other.push(req.method() + " " + url.pathname); return json({}); }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { add: "Add another Qoder (international) account", anyway: "Sign in anyway", wait: "Finish signing in to Qoder (international) in your browser", again: "Open again", close: "Close", cancel: "Cancel", stop: "Stop waiting for this sign-in" },
  zh: { add: "添加另一个 Qoder 国际版 (qoder.com) 账号", anyway: "仍然登录", wait: "请在浏览器中完成 Qoder 国际版 (qoder.com) 登录", again: "重新打开", close: "关闭", cancel: "取消", stop: "不再等待这次登录" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a sign-in left unfinished can be closed`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 970, height: 845 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-signin-close.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      const ctl = { started: 0, canceled: [], polled: [], opened: [], other: [], saved: [] };
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("dialog: " + d.message()); d.dismiss(); });
      await page.route("**/*", serve(lang, ctl));
      await page.goto("http://magpie.test/?view=providers");
      const open = async () => {
        if (!await page.locator(".editor .accts").count()) await page.locator(".row.provider", { hasText: "Qoder" }).first().click();
        const add = page.locator(".editor .accts .acc.add", { hasText: w.add });
        await add.waitFor();
        return add;
      };
      const begin = async () => {
        const add = await open();
        await add.click();
        await page.locator(".editor .accts .signing button", { hasText: w.anyway }).click();
        const box = page.locator(".editor .accts .signing");
        await box.locator(".n", { hasText: w.wait }).waitFor();
        return box;
      };
      const scrolls = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)]);

      const box = await begin();
      await page.waitForTimeout(100);
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-signin-waiting.png`) });
      }
      // the box and every button in it are inside the account list, in
      // sight: the long link doesn't push them out
      const fit = await box.evaluate((b) => {
        const r = b.closest(".accts").getBoundingClientRect();
        return [b, ...b.querySelectorAll("button")].map((x) => {
          const q = x.getBoundingClientRect();
          return { t: x.textContent.slice(0, 30), inside: q.width > 0 && q.left >= r.left - 0.5 && q.right <= r.right + 0.5 };
        });
      });
      assert(fit.every((f) => f.inside), JSON.stringify(fit));
      // one button puts it away: Open again stands alone, no Close
      const acts = box.locator(".acts");
      assert.deepEqual(await acts.locator("button").allTextContents(), [w.again]);
      assert.equal(await box.getByRole("button", { name: w.close, exact: true }).count(), 0, "no Close beside Cancel");
      const cancel = box.locator(":scope > button", { hasText: w.cancel });
      assert.equal(await cancel.count(), 1);
      assert.equal(await cancel.getAttribute("title"), w.stop);
      for (let i = 0; i < 40 && !ctl.polled.length; i++) await page.waitForTimeout(50);
      assert(ctl.polled.length, "the sign-in is followed");
      const before = await scrolls();
      await cancel.click();
      await page.locator(".editor .accts .acc.add", { hasText: w.add }).waitFor();
      assert.equal(await page.locator(".editor .accts .signing").count(), 0, "the box is gone");
      for (let i = 0; i < 40 && !ctl.canceled.length; i++) await page.waitForTimeout(25);
      assert.deepEqual(ctl.canceled, ["q1"], "magpie is told to drop it");
      assert.deepEqual(await scrolls(), before, "the click moved the page");
      // no more asking after it
      const polled = ctl.polled.length;
      await page.waitForTimeout(2000);
      assert(ctl.polled.length <= polled + 1, `still polled: ${ctl.polled.length - polled}`);

      // and again, for a second sign-in
      const box2 = await begin();
      await box2.locator(":scope > button", { hasText: w.cancel }).click();
      await page.locator(".editor .accts .acc.add", { hasText: w.add }).waitFor();
      assert.deepEqual(ctl.canceled, ["q1", "q2"]);

      // the editor's Cancel puts a waiting sign-in away: opened again, the
      // editor has Add another, not the box
      await begin();
      await page.locator(".editor .bar button", { hasText: new RegExp("^" + w.cancel + "$") }).last().click();
      await page.locator(".editor .accts").waitFor({ state: "detached" });
      for (let i = 0; i < 40 && ctl.canceled.length < 3; i++) await page.waitForTimeout(25);
      assert.deepEqual(ctl.canceled, ["q1", "q2", "q3"]);
      await open();
      assert.equal(await page.locator(".editor .accts .signing").count(), 0, "the box came back with the editor");

      // so does Save
      await begin();
      await page.locator(".editor .bar button.primary").last().click();
      await page.locator(".editor .accts").waitFor({ state: "detached" });
      for (let i = 0; i < 40 && ctl.canceled.length < 4; i++) await page.waitForTimeout(25);
      assert.deepEqual(ctl.canceled, ["q1", "q2", "q3", "q4"]);
      assert.deepEqual(ctl.saved, ["qoder"]);
      await open();
      await page.waitForTimeout(1000);
      assert.equal(await page.locator(".editor .accts .acc.add", { hasText: w.add }).count(), 1, "the editor stays open, with Add another");

      const missing = await page.evaluate(() => ["Stop waiting for this sign-in"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, []);
      assert.deepEqual(ctl.other.filter((o) => o.startsWith("POST") && !o.includes("/api/open")), [], "nothing else was changed");
      assert.deepEqual(errors, []);
    });
  }
}
