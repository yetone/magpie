// Run with Node's test runner and Playwright on the module path; see README.md.
// HTTPS for other computers (zhangly1 on X: Claude on another computer on
// the LAN needed magpie at https://): Settings › Network, under Require a
// client certificate, turns HTTPS on; the Certificate row says which is
// served and imports the user's own PEM files (or removes them); magpie's
// CA row shows its SHA-256 and saves it to Downloads, the path said; the
// rows under it give the download and each system's trust command with a
// Copy button. A refused import or a far page's switch is told in the
// page's language. Hidden while magpie answers only this computer.
// English and Chinese, Chromium and WebKit, 390px; the strings in all
// four languages; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const file = async (route, url) => {
  const f = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(f)];
  await route.fulfill({ body: await fs.readFile(f), contentType });
};
const boot = (route, lang) => route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });

const FP = Array.from({ length: 32 }, (_, i) => (i * 7 + 16).toString(16).toUpperCase().padStart(2, "0")).join(":");
const FAR = "this page is open from another computer, and HTTPS changes how it is reached: turn it on or off from the computer magpie runs on";
const NOKEY = "a certificate but no private key: give the key's file too";

function server(lang, posts, over = {}) {
  const s = {
    theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.400", dir: "/config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], allowIPs: [],
    lan: true, lanURLs: ["http://192.168.1.20:3425"], mtls: false, mtlsCerts: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, ...over.settings,
  };
  let h = { on: false, served: false };
  const served = (from) => ({
    on: true, served: true,
    cert: { from, names: from === "magpie" ? ["mac.local", "192.168.1.20"] : ["nas.home"], issuer: from === "magpie" ? "magpie CA" : "Test Root", until: "2027-11-10T00:00:00Z" },
    ...(from === "magpie" ? { ca: FP, caURLs: ["https://192.168.1.20:3425/magpie-ca.crt"] } : {}),
  });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return boot(route, lang);
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (url.pathname === "/api/settings") return json(s);
    if (url.pathname === "/api/settings/https" && route.request().method() === "GET") return json(h);
    if (url.pathname.startsWith("/api/settings/https")) {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/settings/https") {
        if (over.farPage) return json({ error: FAR }, 400);
        h = body.on ? served("magpie") : { on: false, served: false };
      } else if (url.pathname.endsWith("/import")) {
        if (body.files.length < 2) return json({ error: NOKEY }, 400);
        h = served("imported");
      } else if (url.pathname.endsWith("/remove")) {
        h = served("magpie");
      } else if (url.pathname.endsWith("/ca")) {
        return json({ ...h, saved: "~/Downloads/magpie-ca.crt" });
      }
      return json(h);
    }
    if (url.pathname === "/api/copy") { posts.push({ path: url.pathname, body: route.request().postDataJSON() }); return json({}); }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    await file(route, url);
  };
}

const en = {
  sub: "Other computers call magpie at https://; this computer can still use http://",
  mine: "Your certificate",
  signed: "Signed by magpie’s CA",
  ca: "magpie’s CA",
  node: "Claude Code and other Node apps",
  far: "This page is open from another computer: turn HTTPS on or off from the computer magpie runs on",
  nokey: "A certificate but no private key: choose the key’s file too",
};
const zh = { sub: "其他电脑通过 https:// 访问 magpie；本机仍可用 http://", ca: "magpie 的 CA" };
const others = [
  "Already served over https:// by Require a client certificate or --tls-cert",
  "Trust it once on each computer that calls magpie · SHA-256 {fp}",
  "A private key but no certificate: choose the certificate’s file too",
  "The private key belongs to none of the certificates",
  "The private key has a passphrase: choose one without",
  "No PEM certificate or key in the files chosen",
];

async function open(browser, lang, posts, over) {
  const page = await (await browser.newContext({ viewport: { width: 390, height: 640 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, posts, over));
  await watchSettingsReads(page);
  await page.goto("http://127.0.0.1:4999/?view=settings&tab=network");
  await settingsDrawn(page);
  return { page, errors };
}

async function fits(box) {
  const bad = await box.evaluate((b) => {
    const out = [];
    const right = b.getBoundingClientRect().right + 0.5;
    for (const e of b.querySelectorAll(".row, button, .name, code")) {
      const r = e.getBoundingClientRect();
      if (r.right > right || r.width === 0) out.push(e.className + " " + r.right + ">" + right);
      if (e.matches("button, .name") && e.scrollWidth > e.clientWidth + 1) out.push("cut: " + e.textContent);
    }
    if (document.documentElement.scrollWidth > innerWidth) out.push("page scrolls sideways");
    return out;
  });
  assert.deepEqual(bad, []);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": HTTPS in Settings (zhangly1 on X)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    await t.test("every language has the strings", async () => {
      const { page } = await open(browser, "en", []);
      for (const lang of ["zh", "zh-TW", "ja", "de"]) {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k] || I18N[lang][k] === k),
          [lang, [...Object.values(en), ...others]]);
        assert.deepEqual(missing, [], lang);
      }
      await page.context().close();
    });

    await t.test("hidden while magpie answers only this computer", async () => {
      const { page, errors } = await open(browser, "en", [], { settings: { lan: false, lanURLs: [] } });
      await page.locator("#lanList .row").first().waitFor();
      await page.waitForTimeout(200);
      assert.equal(await page.locator("#httpsList .row").count(), 0);
      assert.deepEqual(errors, []);
      await page.context().close();
    });

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": on, the CA and its steps, import, remove, off", async () => {
        const posts = [];
        const { page, errors } = await open(browser, lang, posts);
        const w = {};
        for (const [k, s] of Object.entries(en)) {
          w[k] = await page.evaluate((s) => t(s), s);
          if (lang !== "en") assert.notEqual(w[k], s, `"${s}" has no ${lang} translation`);
          if (lang === "zh" && zh[k]) assert.equal(w[k], zh[k]);
        }
        const box = page.locator("#httpsList");
        const toggle = box.locator(".https-row");
        await toggle.waitFor();
        assert.equal((await toggle.locator(".name").textContent()).trim(), "HTTPS");
        assert.equal((await toggle.locator(".sub").textContent()).trim(), w.sub);
        assert.equal(await box.locator(".https-cert").count(), 0, "no certificate row while off");
        await fits(box);

        const on = toggle.locator("button.opt").last();
        await reach(page, on);
        await on.click();
        await box.locator(".https-ca").waitFor();
        assert.deepEqual(posts.at(-1), { path: "/api/settings/https", body: { on: true } });
        const certSub = (await box.locator(".https-cert .sub").textContent()).trim();
        assert.ok(certSub.startsWith(w.signed + " · mac.local, 192.168.1.20 · "), certSub);
        assert.ok((await box.locator(".https-ca .sub").textContent()).includes("SHA-256 " + FP));
        const steps = await box.locator(".https-step").evaluateAll((rs) => rs.map((r) => [r.querySelector(".name").textContent, r.querySelector("code").textContent]));
        assert.deepEqual(steps.map((s) => s[0]).slice(1), ["macOS", "Windows", "Linux", w.node]);
        assert.equal(steps[0][1], "curl -kO https://192.168.1.20:3425/magpie-ca.crt");
        assert.match(steps[1][1], /add-trusted-cert .* magpie-ca\.crt$/);
        assert.equal(steps[2][1], "certutil -addstore -f Root magpie-ca.crt");
        await fits(box);

        // Copy sends the command itself
        const copy = box.locator(".https-step").nth(2).locator("button.copy");
        await reach(page, copy);
        await copy.click();
        await page.waitForFunction(() => document.querySelector("#status")?.textContent);
        assert.deepEqual(posts.at(-1), { path: "/api/copy", body: { text: "certutil -addstore -f Root magpie-ca.crt" } });

        const save = box.locator(".https-ca button");
        await reach(page, save);
        await save.click();
        const savedSays = await page.evaluate(() => t("Saved to {path}", { path: "~/Downloads/magpie-ca.crt" }));
        await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), savedSays);
        assert.deepEqual(posts.at(-1), { path: "/api/settings/https/ca", body: {} });

        // a certificate without its key is refused, in the page's language
        const input = box.locator(".https-cert input[type=file]");
        await input.setInputFiles([{ name: "cert.pem", mimeType: "application/x-pem-file", buffer: Buffer.from("-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n") }]);
        await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), w.nokey);
        assert.equal(posts.at(-1).path, "/api/settings/https/import");
        await input.setInputFiles([
          { name: "cert.pem", mimeType: "application/x-pem-file", buffer: Buffer.from("CERT") },
          { name: "key.pem", mimeType: "application/x-pem-file", buffer: Buffer.from("KEY") },
        ]);
        await box.locator(".https-cert .sub", { hasText: w.mine }).waitFor();
        assert.deepEqual(posts.at(-1), { path: "/api/settings/https/import", body: { files: ["CERT", "KEY"] } });
        assert.equal(await box.locator(".https-ca").count(), 0, "no CA row with the user's own certificate");
        const remove = box.locator(".https-cert button").last();
        await reach(page, remove);
        await remove.click();
        await box.locator(".https-ca").waitFor();
        assert.deepEqual(posts.at(-1), { path: "/api/settings/https/remove", body: {} });

        const off = box.locator(".https-row button.opt").first();
        await reach(page, off);
        await off.click();
        await page.waitForFunction(() => !document.querySelector("#httpsList .https-cert"));
        assert.deepEqual(posts.at(-1), { path: "/api/settings/https", body: { on: false } });
        const border = await box.locator(".row").first().evaluate((r) => getComputedStyle(r).borderLeftWidth);
        assert(border === "0px" || border === "", "no left border accent: " + border);
        assert.equal(await box.locator("select").count(), 0);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }

    await t.test("a page on another computer is told why it can't, in its language", async () => {
      const posts = [];
      const { page, errors } = await open(browser, "zh", posts, { farPage: true });
      const toggle = page.locator("#httpsList .https-row");
      await toggle.waitFor();
      const on = toggle.locator("button.opt").last();
      await reach(page, on);
      await on.click();
      const says = await page.evaluate((s) => t(s), en.far);
      assert.notEqual(says, en.far);
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), says);
      await page.waitForFunction(() => document.querySelector("#httpsList .https-row .opt")?.classList.contains("on"));
      assert.deepEqual(errors, []);
      await page.context().close();
    });
  });
}
