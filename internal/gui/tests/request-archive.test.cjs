// Run with Node's test runner and Playwright on the module path; see README.md.
// The request archive (Jorben on Discord): its switch sits at the top right
// of the Usage page's request list, beside the requests' sum, off unless
// turned on; with sync not set up with an s3:// bucket it can't be turned on
// and says why, with one it posts settings/archive and its tooltip says the
// bucket; a failed upload is said beside it. The Gateway page has no switch
// any more, and its recent calls show their own bodies with nothing of the
// archive under them, even one it kept: reading a call back from the archive
// is the Usage page's (request-archive-usage.test.cjs). No click moves the
// page. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const DATE = new Date(now).toISOString().slice(0, 10);
const KEPT = `${DATE}/101500-0123456789abcdef`;
const call = (i, model, archive) => ({
  time: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", model, from: "anthropic", to: "anthropic", status: 200, ms: 900,
  requestBody: JSON.stringify({ model, messages: [{ role: "user", content: "hi" }] }), responseBody: JSON.stringify({ id: "msg_1", content: [{ type: "text", text: "hello" }] }),
  ...(archive ? { archive } : {}),
});
const row = (i, model, archive) => ({
  t: new Date(now - (i + 1) * 3600e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay",
  req: model, model, served: model, in: 300, out: 40, ms: 2000, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true, ...(archive ? { archive } : {}),
});
const ROWS = [row(0, "kept", KEPT), row(1, "plain")];

function serve(lang, seen) {
  const where = "bkt/team on https://s3.example.com";
  const gateway = { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", groups: [], archive: { on: false },
    calls: [call(0, "fixture/kept", KEPT), call(1, "fixture/plain")] };
  const providers = { providers: [{ id: "fixture", name: "Fixture", icon: "generic", models: [{ id: "kept", name: "Kept", on: true }], agents: [] }], gateway };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/settings/archive") {
      const body = route.request().postDataJSON();
      seen.push(["switch", body]);
      await new Promise((r) => setTimeout(r, 80));
      if (body.on && !seen.bucket) return json({ error: "The request archive goes to the S3 bucket sync keeps its backup in: set up Sync and backup in Settings with an s3:// address first" }, 400);
      gateway.archive = { on: body.on, ...(seen.bucket ? { bucket: where } : {}), ...(body.on && seen.failing ? { error: "HTTP 403" } : {}) };
      return json(gateway.archive);
    }
    if (url.pathname === "/api/archive") {
      seen.push(["fetch", `${url.searchParams.get("date")}/${url.searchParams.get("id")}`]);
      return json({ error: "not asked for here" }, 400);
    }
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: 2, calls: 2, errors: 0, input: 600, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.02, unpriced: 0, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 2, errors: 0, input: 600, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.02, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    name: "Request archive", fetch: "Fetch from archive", failed: "Upload failed", failedWhy: "Last upload failed: HTTP 403",
    setup: "Set up Sync and backup in Settings with an s3:// address first",
    refused: "The request archive goes to the S3 bucket sync keeps its backup in: set up Sync and backup in Settings with an s3:// address first",
    bucket: "Keeps each call’s headers and bodies, secrets taken out, in bkt/team on https://s3.example.com",
  },
  zh: {
    name: "请求存档", fetch: "从存档取回", failed: "上传失败", failedWhy: "上次上传失败：HTTP 403",
    setup: "请先在设置的“同步与备份”中填写 s3:// 地址",
    refused: "请求存档会上传到同步备份所用的 S3 存储桶：请先在设置的“同步与备份”中填写 s3:// 地址",
    bucket: "每次调用的请求/响应头和正文去除密钥后存到 bkt/team on https://s3.example.com",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the request archive's switch over the Usage page's requests, none on the Gateway page`, async (t) => {
      const w = words[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const seen = [];
      await page.route("**/*", serve(lang, seen));
      // brought into view by the wheel, as the reader would; the click itself
      // must not move the page
      const press = async (loc, view) => {
        await reader.inView(page, loc);
        const at = await page.locator(view).evaluate((v) => v.scrollTop);
        await loc.click();
        await page.waitForTimeout(250);
        assert.equal(await page.locator(view).evaluate((v) => v.scrollTop), at, "a click moved the page");
      };
      const wait = async (n) => { for (let i = 0; i < 60 && seen.length < n; i++) await page.waitForTimeout(50); };

      // the Gateway page: no switch, and a call the archive kept shows its
      // bodies with nothing of the archive under them
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#foldConnect").click();
      const calls = page.locator("#activity .call-item");
      await calls.first().waitFor();
      assert.equal(await page.locator("#archiveList, #view-gateway [role=switch]").count(), 0, "no archive switch on the Gateway page");
      await press(calls.nth(0).locator(".call"), "#view-gateway");
      const details = calls.nth(0).locator(".call-details");
      await details.waitFor();
      assert.equal(await details.locator(".call-body").count(), 2, "the call's own request and response bodies");
      assert.equal(await page.locator("#view-gateway .call-archive").count(), 0, "no archive panel under a recent call");
      assert.equal(await page.locator("#view-gateway").getByRole("button", { name: w.fetch }).count(), 0, "no Fetch from archive on the Gateway page");
      assert(!(await page.locator("#view-gateway").innerText()).includes(w.name), "the Gateway page doesn't name the archive");

      // the Usage page's requests: the switch at the top right of their list
      await page.locator('[data-view="usage"]').first().click();
      await page.locator("#usageTab .opt").nth(1).click();
      await page.locator(".led tbody tr.led-row").first().waitFor();
      const sw = page.locator("#ledArchive .led-arch-sw");
      await sw.waitFor();
      assert.equal(await sw.getAttribute("role"), "switch");
      assert.equal(await sw.innerText(), w.name);
      assert.equal(await sw.getAttribute("aria-checked"), "false", "off unless turned on");
      assert((await sw.getAttribute("title")).includes(w.setup));
      const [swBox, sumBox, listBox] = await Promise.all([sw, page.locator("#ledSum"), page.locator("#ledWrap")].map((l) => l.boundingBox()));
      assert(Math.abs(swBox.x + swBox.width - (listBox.x + listBox.width)) <= 4, `the switch ends at the list's right edge: ${JSON.stringify([swBox, listBox])}`);
      assert(swBox.y + swBox.height <= listBox.y, "the switch is over the list");
      assert(Math.abs(swBox.y + swBox.height / 2 - (sumBox.y + sumBox.height / 2)) <= 3, "on the line of the requests' sum");

      // no bucket: turning it on is refused, and it stays off
      await press(sw, "#view-usage");
      await wait(1);
      assert.deepEqual(seen[0], ["switch", { on: true }]);
      await page.waitForFunction((s) => document.querySelector("#status").textContent === s, w.refused);
      await page.locator('#ledArchive .led-arch-sw[aria-checked="false"]:not([disabled])').waitFor();

      // with one: on, the bucket in its tooltip
      seen.bucket = true;
      await press(page.locator("#ledArchive .led-arch-sw"), "#view-usage");
      await wait(2);
      assert.deepEqual(seen[1], ["switch", { on: true }]);
      const on = page.locator('#ledArchive .led-arch-sw[aria-checked="true"]');
      await on.waitFor();
      assert.equal(await on.getAttribute("title"), w.bucket);
      assert.equal(await page.locator("#ledArchive .led-arch-err").count(), 0);
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        const head = await page.locator(".led-head").boundingBox();
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-archive-switch.png`), clip: { x: head.x - 16, y: head.y - 60, width: head.width + 32, height: head.height + 170 } });
      }

      // turned off again
      await press(on, "#view-usage");
      await wait(3);
      assert.deepEqual(seen[2], ["switch", { on: false }]);
      await page.locator('#ledArchive .led-arch-sw[aria-checked="false"]:not([disabled])').waitFor();

      // an upload that failed is said beside it, why in its tooltip
      seen.failing = true;
      await press(page.locator("#ledArchive .led-arch-sw"), "#view-usage");
      await wait(4);
      const err = page.locator("#ledArchive .led-arch-err");
      await err.waitFor();
      assert.equal(await err.innerText(), w.failed);
      assert.equal(await err.getAttribute("title"), w.failedWhy);
      assert.equal(seen.filter(([k]) => k === "fetch").length, 0, "nothing was read from the archive");

      const border = await page.evaluate(() => [...document.querySelectorAll(".led-head, .led-head *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      const missing = await page.evaluate(() => [
        "Request archive", "Upload failed", "Last upload failed: {e}",
        "Keeps each call’s headers and bodies, secrets taken out, in {where}",
        "Keeps each call’s headers and bodies, secrets taken out, in your S3 bucket. Set up Sync and backup in Settings with an s3:// address first",
        "The request archive goes to the S3 bucket sync keeps its backup in: set up Sync and backup in Settings with an s3:// address first",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    // Opened straight onto Usage — the address the page keeps, a reload, a
    // link from another machine — the requests' tab draws the switch from the
    // providers' state, and show()/load() read it for no other tab's view.
    // Before, providers was never read on this path and the switch stayed
    // hidden on every tab of the page.
    test(`${engine} ${lang}: the archive's switch is drawn when the page opens on Usage`, async (t) => {
      const w = words[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, []));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#usageTab .opt").nth(1).click();
      const sw = page.locator("#ledArchive .led-arch-sw");
      await sw.waitFor();
      assert.equal(await sw.getAttribute("role"), "switch");
      assert.equal(await sw.innerText(), w.name);
      assert.equal(await sw.getAttribute("aria-checked"), "false", "off unless turned on");
      assert((await sw.getAttribute("title")).includes(w.setup));
      // and it is the one the requests' list is under, not a stray box
      const [swBox, sumBox] = await Promise.all([sw.boundingBox(), page.locator("#ledSum").boundingBox()]);
      assert(Math.abs(swBox.y + swBox.height / 2 - (sumBox.y + sumBox.height / 2)) <= 3, "on the line of the requests' sum");
      assert.deepEqual(errors, []);
    });
  }
}
