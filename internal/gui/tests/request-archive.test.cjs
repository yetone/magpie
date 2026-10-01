// Run with Node's test runner and Playwright on the module path; see README.md.
// The request archive (Jorben on Discord): a switch over the Gateway page's
// recent calls, off unless turned on; with sync not set up with an s3://
// bucket it can't be turned on and says why, with one it posts
// settings/archive and says the bucket. A call the archive kept has, under
// its bodies, its archive's date and id and "Fetch from archive", which
// reads archive?date=&id= back and shows the request's and response's
// headers and bodies, secrets taken out; one not there yet says so. No
// click moves the page. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const DATE = new Date(now).toISOString().slice(0, 10);
const KEPT = `${DATE}/101500-0123456789abcdef`;
const LOST = `${DATE}/101400-fedcba9876543210`;
const call = (i, model, archive) => ({
  time: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", model, from: "anthropic", to: "anthropic", status: 200, ms: 900,
  requestBody: JSON.stringify({ model, messages: [{ role: "user", content: "hi" }] }), responseBody: JSON.stringify({ id: "msg_1", content: [{ type: "text", text: "hello" }] }),
  ...(archive ? { archive } : {}),
});
const archived = {
  id: KEPT,
  call: { model: "fixture/kept", status: 200 },
  request: {
    method: "POST", path: "/v1/messages?beta=true",
    headers: { Authorization: ["[REDACTED]"], "Content-Type": ["application/json"], "User-Agent": ["claude-cli/2.1.0"] },
    body: JSON.stringify({ model: "fixture/kept", messages: [{ role: "user", content: "key [REDACTED:API_KEY]" }] }),
  },
  response: { status: 200, headers: { "Set-Cookie": ["[REDACTED]"], "X-Magpie-Archive-Id": ["101500-0123456789abcdef"] }, body: JSON.stringify({ id: "msg_1", content: [{ type: "text", text: "archived reply" }] }) },
};

function serve(lang, seen) {
  const where = "bkt/team on https://s3.example.com";
  const gateway = { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", groups: [], archive: { on: false },
    calls: [call(0, "fixture/kept", KEPT), call(1, "fixture/lost", LOST), call(2, "fixture/plain")] };
  const providers = { providers: [{ id: "fixture", name: "Fixture", icon: "generic", models: [{ id: "kept", name: "Kept", on: true }], agents: [] }], gateway };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/settings/archive") {
      const body = route.request().postDataJSON();
      seen.push(["switch", body]);
      if (body.on && !seen.bucket) return json({ error: "The request archive goes to the S3 bucket sync keeps its backup in: set up Sync and backup in Settings with an s3:// address first" }, 400);
      gateway.archive = { on: body.on, ...(seen.bucket ? { bucket: where } : {}) };
      return json(gateway.archive);
    }
    if (url.pathname === "/api/archive") {
      const name = `${url.searchParams.get("date")}/${url.searchParams.get("id")}`;
      seen.push(["fetch", name]);
      await new Promise((r) => setTimeout(r, 150));
      if (name === KEPT) return json(archived);
      return json({ error: "Not in the archive yet: it is uploaded just after the call, or the upload failed (magpie's log says why)" }, 400);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    name: "Request archive", off: "Off", on: "On", fetch: "Fetch from archive", secrets: "Secrets taken out",
    setup: "Set up Sync and backup in Settings with an s3:// address first",
    refused: "The request archive goes to the S3 bucket sync keeps its backup in: set up Sync and backup in Settings with an s3:// address first",
    bucket: "Keeps each call’s headers and bodies, secrets taken out, in bkt/team on https://s3.example.com",
    reqH: "Request Headers", resH: "Response Headers", notYet: "Not in the archive yet",
  },
  zh: {
    name: "请求存档", off: "关闭", on: "开启", fetch: "从存档取回", secrets: "已去除密钥",
    setup: "请先在设置的“同步与备份”中填写 s3:// 地址",
    refused: "请求存档会上传到同步备份所用的 S3 存储桶：请先在设置的“同步与备份”中填写 s3:// 地址",
    bucket: "把每次调用的请求头、响应头和请求体、响应体去掉密钥后存到 bkt/team on https://s3.example.com",
    reqH: "请求头", resH: "响应头", notYet: "存档里还没有这次调用",
  },
};
const view = "#view-gateway";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the request archive's switch, and a call fetched back from it`, async (t) => {
      const w = words[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-request-archive.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const seen = [];
      await page.route("**/*", serve(lang, seen));
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#foldConnect").click();
      // what is clicked is brought into view by the wheel, as the reader
      // would, and clicked where it is; the click itself must not move it
      const press = async (loc) => {
        const [dy, x, y] = await loc.evaluate((e) => {
          const v = document.querySelector("#view-gateway").getBoundingClientRect();
          return [e.getBoundingClientRect().top - v.top - v.height / 2, v.left + 6, v.top + v.height / 2];
        });
        await page.mouse.move(x, y);
        await page.mouse.wheel(0, dy);
        let b = await loc.boundingBox();
        for (let i = 0; i < 20; i++) {
          await page.waitForTimeout(120);
          const n = await loc.boundingBox();
          if (n.y === b.y) break;
          b = n;
        }
        const at = await page.locator(view).evaluate((v) => v.scrollTop);
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        await page.waitForTimeout(250);
        assert.equal(await page.locator(view).evaluate((v) => v.scrollTop), at, "a click moved the page");
      };
      const wait = async (n) => { for (let i = 0; i < 60 && seen.length < n; i++) await page.waitForTimeout(50); };

      // the switch: off, and how to set it up
      const row = page.locator("#archiveList .row");
      await row.waitFor();
      assert.equal(await row.locator(".name").innerText(), w.name);
      assert.equal(await row.locator(".segs .opt.on").innerText(), w.off, "off unless turned on");
      assert((await row.locator(".sub").innerText()).includes(w.setup));

      // no bucket: turning it on is refused, and it stays off
      await press(row.locator(".segs .opt", { hasText: w.on }));
      await wait(1);
      assert.deepEqual(seen[0], ["switch", { on: true }]);
      await page.waitForFunction((s) => document.querySelector("#status").textContent === s, w.refused);
      assert.equal(await row.locator(".segs .opt.on").innerText(), w.off);

      // with one: on, and the bucket said
      seen.bucket = true;
      await press(row.locator(".segs .opt", { hasText: w.on }));
      await wait(2);
      assert.deepEqual(seen[1], ["switch", { on: true }]);
      await page.waitForFunction((s) => document.querySelector("#archiveList .segs .opt.on")?.textContent === s, w.on);
      assert.equal(await row.locator(".sub").innerText(), w.bucket);

      // a call the archive kept: its id, and fetched back
      const rows = page.locator("#activity .call-item");
      await press(rows.nth(0).locator(".call"));
      const box = rows.nth(0).locator(".call-archive");
      await box.waitFor();
      assert.equal(await box.locator(".call-archive-id").innerText(), KEPT);
      await press(box.getByRole("button", { name: w.fetch }));
      await wait(3);
      assert.deepEqual(seen[2], ["fetch", KEPT]);
      await box.locator(".call-details").waitFor();
      assert.equal(await box.locator(".call-archive-note").innerText(), w.secrets);
      const labels = await box.locator(".call-body-label").allInnerTexts();
      assert(labels.includes(w.reqH) && labels.includes(w.resH), `headers panels: ${labels}`);
      const text = await box.innerText();
      for (const s of ["POST /v1/messages?beta=true", "Authorization: [REDACTED]", "User-Agent: claude-cli/2.1.0", "HTTP 200", "Set-Cookie: [REDACTED]", "[REDACTED:API_KEY]", "archived reply"]) {
        assert(text.includes(s), `${s} not shown in:\n${text}`);
      }

      // one not uploaded: said so, and it can be asked again
      await press(rows.nth(1).locator(".call"));
      const lost = rows.nth(1).locator(".call-archive");
      await press(lost.getByRole("button", { name: w.fetch }));
      await wait(4);
      assert.deepEqual(seen[3], ["fetch", LOST]);
      await lost.locator(".call-archive-err").waitFor();
      assert((await lost.locator(".call-archive-err").innerText()).startsWith(w.notYet));
      assert(await lost.getByRole("button", { name: w.fetch }).isVisible());
      assert.equal(await box.locator(".call-details").count(), 1, "the first one's archive stays shown");

      // a call the archive didn't keep has nothing of it
      await press(rows.nth(2).locator(".call"));
      await rows.nth(2).locator(".call-details").waitFor();
      assert.equal(await rows.nth(2).locator(".call-archive").count(), 0);

      // turned off again
      await press(row.locator(".segs .opt", { hasText: w.off }));
      await wait(5);
      assert.deepEqual(seen[4], ["switch", { on: false }]);
      await page.waitForFunction((s) => document.querySelector("#archiveList .segs .opt.on")?.textContent === s, w.off);

      const border = await page.evaluate(() => [...document.querySelectorAll("#archiveList *, .call-archive, .call-archive *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");
      const missing = await page.evaluate(() => [
        "Request archive", "Fetch from archive", "Fetching…", "Request Headers", "Response Headers", "Secrets taken out", "Last upload failed: {e}",
        "Keeps each call’s headers and bodies, secrets taken out, in {where}",
        "Keeps each call’s headers and bodies, secrets taken out, in your S3 bucket. Set up Sync and backup in Settings with an s3:// address first",
        "The request archive goes to the S3 bucket sync keeps its backup in: set up Sync and backup in Settings with an s3:// address first",
        "The request archive goes to the S3 bucket sync keeps its backup in, and sync isn't set up with one",
        "Not in the archive yet: it is uploaded just after the call, or the upload failed (magpie's log says why)",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
