// Run with Node's test runner and Playwright on the module path; see README.md.
// The request archive from the Usage page (jorben, #447): a request the
// archive kept names it in its row's details, long after it left the
// Gateway page's recent calls, with "Fetch from archive" and "Download". A
// body past 256 KB is said by its size only, for the file to be downloaded
// whole; an archive too large to read here says so. Download in the app asks
// archive/export and says where it saved the file; in magpie web it is the
// browser's download of archive/file. A request with no copy, with the
// archive on, says "Not archived". No click moves the page, no left-border
// accent. English and Chinese, Chromium and WebKit; the API is faked.
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
const BIG = `${DATE}/101400-fedcba9876543210`;
const row = (i, model, archive) => ({
  t: new Date(now - (i + 1) * 3600e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay",
  req: model, model, served: model, in: 300, out: 40, ms: 2000, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true, ...(archive ? { archive } : {}),
});
const ROWS = [row(0, "kept", KEPT), row(1, "big", BIG), row(2, "plain")];
const preview = {
  id: KEPT, bytes: 21 << 20, call: { model: "relay/kept", status: 200 },
  request: { method: "POST", path: "/v1/messages", headers: { Authorization: ["[REDACTED]"] }, body: JSON.stringify({ model: "kept", messages: [{ role: "user", content: "REQ-WHOLE" }] }), size: 64 },
  response: { status: 200, headers: { "Content-Type": ["application/json"] }, body: "", size: 20e6, omitted: true },
};

function server(lang, web, seen) {
  const gateway = { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", groups: [], calls: [], archive: { on: true, bucket: "bkt/team" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", requestArchive: true } });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: 3, calls: 3, errors: 0, input: 900, output: 120, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.03, unpriced: 0, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 3, errors: 0, input: 900, output: 120, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.03, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    const name = `${url.searchParams.get("date")}/${url.searchParams.get("id")}`;
    if (url.pathname === "/api/archive") {
      seen.push(["fetch", name]);
      await new Promise((r) => setTimeout(r, 120));
      if (name === KEPT) return json(preview);
      return json({ id: name, bytes: 120e6, large: true });
    }
    if (url.pathname === "/api/archive/export") {
      seen.push([req.method() + " export", name]);
      return json({ path: "~/Downloads/magpie-request-" + name.replace("/", "-") + ".json" });
    }
    if (url.pathname === "/api/archive/file") {
      seen.push([req.method() + " file", name]);
      return route.fulfill({ status: 200, headers: { "Content-Disposition": `attachment; filename="magpie-request-${name.replace("/", "-")}.json"` }, contentType: "application/json", body: "{}" });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const L = {
  en: {
    name: "Request archive", fetch: "Fetch from archive", download: "Download", secrets: "Secrets taken out", not: "Not archived",
    omitted: "Too long to show here. Download the archive to read it whole.",
    large: "This archive is 120 MB, too large to show here. Download it to read it.",
  },
  zh: {
    name: "请求存档", fetch: "从存档取回", download: "下载", secrets: "已去除密钥", not: "无存档",
    omitted: "内容过长，不在此显示。下载存档可查看完整内容。",
    large: "存档 120 MB，过大无法在此显示，请下载后查看。",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a request's archive from the Usage page", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      for (const web of [false, true]) {
        const w = L[lang];
        await t.test(`${lang}${web ? " web" : " app"}`, async () => {
          const errors = [], seen = [];
          const ctx = await browser.newContext({ viewport: { width: 1180, height: 640 }, reducedMotion: "reduce" });
          const p = await ctx.newPage();
          t.after(() => ctx.close());
          p.setDefaultTimeout(5000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", server(lang, web, seen));
          await p.addInitScript(() => {
            window.followed = [];
            HTMLAnchorElement.prototype.click = function () { window.followed.push([this.href, this.hasAttribute("download")]); };
          });
          await p.goto("http://magpie.test/");
          await p.locator('[data-view="usage"]').first().click();
          await p.locator("#usageTab .opt").nth(1).click();
          const rows = p.locator(".led tbody tr.led-row");
          await rows.first().waitFor();
          assert.equal(await rows.count(), 3);
          const top = () => p.locator("#view-usage").evaluate((v) => v.scrollTop);
          const press = async (loc) => {
            // brought into view by the wheel, as the reader would; the
            // click itself must not move it
            await reader.inView(p, loc);
            const at = await top();
            await loc.click();
            await p.waitForTimeout(200);
            assert.equal(await top(), at, "a click moved the page");
          };
          const wait = async (n) => { for (let i = 0; i < 60 && seen.length < n; i++) await p.waitForTimeout(50); };

          // a request the archive kept: its name, fetched back
          await press(rows.nth(0));
          const box = p.locator(".led-detail").nth(0).locator(".led-archive .call-archive");
          await box.waitFor();
          assert.equal(await box.locator(".call-body-label").first().innerText(), w.name);
          assert.equal(await box.locator(".call-archive-id").innerText(), KEPT);
          assert(await box.getByRole("button", { name: w.download }).isVisible(), "Download before it is fetched");
          await press(box.getByRole("button", { name: w.fetch }));
          await wait(1);
          assert.deepEqual(seen[0], ["fetch", KEPT]);
          await box.locator(".call-details").waitFor();
          assert.equal(await box.locator(".call-archive-note").innerText(), w.secrets);
          const text = await box.innerText();
          assert(text.includes("REQ-WHOLE"), "the request body is shown");
          assert(text.includes("Authorization: [REDACTED]"));
          // the long response: its size and a note, no body
          assert.equal(await box.locator(".call-archive-big").innerText(), w.omitted);
          assert(text.includes("20 MB"), `the response's size in:\n${text}`);

          // downloaded: to Downloads in the app, the browser's in magpie web
          const dl = box.getByRole("button", { name: w.download });
          if (web) {
            // the browser's own download of archive/file, its name the
            // server's Content-Disposition (Go's TestArchivePreviewAndDownload);
            // the link it follows is kept here, as WebKit's headless
            // download of a routed response isn't one Playwright sees
            await press(dl);
            const [date, id] = KEPT.split("/");
            assert.deepEqual(await p.evaluate(() => window.followed), [[`http://magpie.test/api/archive/file?date=${date}&id=${id}`, true]]);
            assert.equal(seen.filter(([k]) => k.endsWith("export")).length, 0, "magpie web doesn't ask the app to save it");
          } else {
            await press(dl);
            await wait(2);
            assert.deepEqual(seen[1], ["POST export", KEPT]);
            const said = `~/Downloads/magpie-request-${KEPT.replace("/", "-")}.json`;
            await p.waitForFunction((s) => document.querySelector("#status").textContent.includes(s), said);
          }

          // one too large to read here: said so, still downloadable
          await press(rows.nth(1));
          const big = p.locator(".led-detail").nth(1).locator(".call-archive");
          await press(big.getByRole("button", { name: w.fetch }));
          await big.locator(".call-archive-big").waitFor();
          assert.equal(await big.locator(".call-archive-big").innerText(), w.large);
          assert(await big.getByRole("button", { name: w.download }).isVisible());

          // one with no copy, the archive on: said so
          await press(rows.nth(2));
          const plain = p.locator(".led-detail").nth(2);
          await plain.locator("dd", { hasText: w.not }).waitFor();
          assert.equal(await plain.locator(".call-archive").count(), 0);

          const border = await p.evaluate(() => [...document.querySelectorAll(".led-archive, .led-archive *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
          assert.deepEqual(border, [], "no left-border accent");
          const missing = await p.evaluate(() => [
            "Too long to show here. Download the archive to read it whole.", "This archive is {size}, too large to show here. Download it to read it.",
            "very large", "first {n} of {size}", "Not archived", "Download", "Saved to {path}",
          ].filter((k) => !I18N.zh[k]));
          assert.deepEqual(missing, [], "every string has its Chinese");
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
