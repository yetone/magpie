// Run with Node's test runner and Playwright on the module path; see README.md.
// mTLS (hhdaxia on X, #5596): Settings › Network, beside Allowed
// addresses, turns on Require a client certificate, issues a device's
// certificate (saved to Downloads, the path said) and revokes one. A page
// that would shut itself out is refused in its own language. Hidden while
// magpie answers only this computer. The provider editor's Advanced holds
// a Client certificate row (certificate, key, server CA) for a provider of
// the user's at https://, saved with Save; none at http:// or for a
// preset's. English and Chinese, Chromium and WebKit, 390px; the strings
// in all four languages; no backend, the API is faked here.
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

const LOCKOUT = "this page is open from another computer without a client certificate this magpie issued, so turning this on would shut it out: issue one, install it in this browser, and open the page again over https://, or turn it on from the computer magpie runs on";

function settingsServer(lang, posts, over) {
  const s = {
    theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.400", dir: "/config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], allowIPs: [],
    lan: true, lanURLs: ["http://192.168.1.20:3425"], mtls: false, mtlsCerts: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, ...over,
  };
  let n = 0;
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return boot(route, lang);
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (url.pathname === "/api/settings") return json(s);
    if (url.pathname.startsWith("/api/settings/mtls")) {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      delete s.mtlsSaved;
      if (url.pathname === "/api/settings/mtls") {
        if (body.on && s.farPage) return json({ error: LOCKOUT }, 400);
        s.mtls = body.on;
      } else if (url.pathname.endsWith("/issue")) {
        n++;
        s.mtlsCerts = [...s.mtlsCerts, { name: body.name, serial: "ab" + n, expires: "2028-11-10T00:00:00Z" }];
        s.mtlsSaved = "~/Downloads/magpie-" + body.name;
      } else {
        s.mtlsCerts = s.mtlsCerts.filter((c) => c.serial !== body.serial);
      }
      return json(s);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    await file(route, url);
  };
}

const en = {
  name: "Require a client certificate",
  sub: "Other computers reach magpie over HTTPS only with a certificate issued here, even with a gateway key; this computer always can",
  certs: "Client certificates",
  certsSub: "Saved to Downloads for the device: the certificate, its key and magpie’s CA",
  field: "Device name", issue: "Issue", revoke: "Revoke",
  lockout: "This page is open from another computer without a certificate, so turning this on would shut it out: turn it on from the computer magpie runs on",
  own: "This page uses that certificate, so it would be shut out: do this from another device, or from the computer magpie runs on",
  cert: "Client certificate", ca: "Server CA", hint: "PEM files, for a server that asks for one (mTLS)",
};
const zh = { name: "要求客户端证书" };

async function newPage(browser, width = 390, height = 640) {
  const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  return { page, errors };
}

async function openSettings(browser, lang, posts, over) {
  const { page, errors } = await newPage(browser);
  await page.route("**/*", settingsServer(lang, posts, over));
  await watchSettingsReads(page);
  await page.goto("http://127.0.0.1:4999/?view=settings&tab=network");
  await settingsDrawn(page);
  return { page, errors };
}

async function fits(box) {
  const bad = await box.evaluate((b) => {
    const out = [];
    const right = b.getBoundingClientRect().right + 0.5;
    for (const e of b.querySelectorAll(".row, input, button, .name")) {
      const r = e.getBoundingClientRect();
      if (r.right > right || r.width === 0) out.push(e.className + " " + r.right + ">" + right);
      if (e.matches("button, .name") && e.scrollWidth > e.clientWidth + 1) out.push("cut: " + e.textContent);
    }
    if (document.documentElement.scrollWidth > innerWidth) out.push("page scrolls sideways");
    return out;
  });
  assert.deepEqual(bad, []);
}

const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m", name: "m", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "mk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const home = { ...base, id: "home", name: "Home Magpie", chat: "https://home.example:3425/v1", takesClientCert: true };
const kept = { ...base, id: "kept", name: "Kept Box", chat: "https://box.example/v1", takesClientCert: true, clientCert: "~/c/client.pem", clientKey: "~/c/client-key.pem", serverCA: "~/c/ca.pem" };
const plain = { ...base, id: "plain", name: "Plain Box", chat: "http://192.168.1.21:8000/v1", takesClientCert: true };
const openai = { ...base, id: "openai", name: "OpenAI", preset: "openai", chat: "https://api.openai.example/v1", takesClientCert: false };

function providersServer(lang, posts) {
  const providers = {
    providers: [home, kept, plain, openai],
    presets: [{ id: "openai", name: "OpenAI", kind: "vendor", icon: "openai", chat: "https://api.openai.example/v1", added: true }],
    excluded: [], gateway: { running: true, window: true },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return boot(route, lang);
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    await file(route, url);
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": mTLS in Settings and the provider editor (hhdaxia, #5596)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    await t.test("every language has the strings", async () => {
      const { page } = await openSettings(browser, "en", []);
      for (const lang of ["zh", "zh-TW", "ja", "de"]) {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k] || I18N[lang][k] === k),
          [lang, [...Object.values(en), "Until {date}", "Saved to {path}"]]);
        assert.deepEqual(missing, [], lang);
      }
      await page.context().close();
    });

    await t.test("hidden while magpie answers only this computer", async () => {
      const { page, errors } = await openSettings(browser, "en", [], { lan: false, lanURLs: [] });
      await page.locator("#lanList .row").first().waitFor();
      assert.equal(await page.locator("#mtlsList .row").count(), 0);
      assert.deepEqual(errors, []);
      await page.context().close();
    });

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": on, issue, revoke", async () => {
        const posts = [];
        const { page, errors } = await openSettings(browser, lang, posts);
        const box = page.locator("#mtlsList");
        const w = {};
        for (const [k, s] of Object.entries(en)) {
          w[k] = await page.evaluate((s) => t(s), s);
          if (lang !== "en") assert.notEqual(w[k], s, `"${s}" has no ${lang} translation`);
          if (lang === "zh" && zh[k]) assert.equal(w[k], zh[k]);
        }
        const toggle = box.locator(".mtls-row");
        await toggle.waitFor();
        assert.equal((await toggle.locator(".name").textContent()).trim(), w.name);
        assert.equal((await toggle.locator(".sub").textContent()).trim(), w.sub);
        await fits(box);

        const on = toggle.locator("button.opt").last();
        await reach(page, on);
        await on.click();
        await page.waitForFunction(() => document.querySelector("#mtlsList .mtls-row .opt.on")?.textContent && document.querySelectorAll("#mtlsList .mtls-row .opt")[1].classList.contains("on"));
        assert.deepEqual(posts.at(-1), { path: "/api/settings/mtls", body: { on: true } });

        const field = box.locator(".mtls-issue input");
        assert.equal(await field.getAttribute("aria-label"), w.field);
        await reach(page, field);
        await field.fill("laptop");
        const issue = box.locator(".mtls-issue button", { hasText: w.issue });
        await reach(page, issue);
        await issue.click();
        await box.locator(".mtls-cert .name", { hasText: "laptop" }).waitFor();
        assert.deepEqual(posts.at(-1), { path: "/api/settings/mtls/issue", body: { name: "laptop" } });
        assert.equal(await box.locator(".mtls-issue input").inputValue(), "", "the field is left empty for the next one");
        // and with Enter
        await box.locator(".mtls-issue input").fill("phone");
        await box.locator(".mtls-issue input").press("Enter");
        await box.locator(".mtls-cert .name", { hasText: "phone" }).waitFor();
        const until = await page.evaluate(() => t("Until {date}", { date: new Date("2028-11-10T00:00:00Z").toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" }) }));
        assert.equal((await box.locator(".mtls-cert .sub").first().textContent()).trim(), until);
        await fits(box);

        const revoke = box.locator(".mtls-cert").filter({ hasText: "laptop" }).locator("button", { hasText: w.revoke });
        await reach(page, revoke);
        await revoke.click();
        await page.waitForFunction(() => document.querySelectorAll("#mtlsList .mtls-cert").length === 1);
        assert.deepEqual(posts.at(-1), { path: "/api/settings/mtls/revoke", body: { serial: "ab1" } });
        const border = await box.locator(".row").first().evaluate((r) => getComputedStyle(r).borderLeftWidth);
        assert(border === "0px" || border === "", "no left border accent: " + border);
        assert.equal(await box.locator("select").count(), 0);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }

    await t.test("a page that would shut itself out is told so in its language", async () => {
      const posts = [];
      const { page, errors } = await openSettings(browser, "zh", posts, { farPage: true });
      const toggle = page.locator("#mtlsList .mtls-row");
      await toggle.waitFor();
      const on = toggle.locator("button.opt").last();
      await reach(page, on);
      await on.click();
      const says = await page.evaluate((s) => t(s), en.lockout);
      assert.notEqual(says, en.lockout);
      await page.waitForFunction((s) => document.querySelector("#mtlsList .mtls-row .sub.err")?.textContent.trim() === s, says);
      assert.equal(await toggle.locator(".opt").first().evaluate((b) => b.classList.contains("on")), true, "still off");
      await fits(page.locator("#mtlsList"));
      assert.deepEqual(errors, []);
      await page.context().close();
    });

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": the provider editor's client certificate", async () => {
        const open = async (name) => {
          const { page, errors } = await newPage(browser, 420, 760);
          const posts = [];
          await page.route("**/*", providersServer(lang, posts));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: name }).click();
          await page.locator(".editor .bar").waitFor();
          const adv = page.locator(".editor details.advanced");
          return { page, errors, posts, adv };
        };
        const w = await (async () => {
          const { page } = await newPage(browser);
          await page.route("**/*", providersServer(lang, []));
          await page.goto("http://magpie.test/?view=providers");
          await page.waitForFunction(() => typeof t === "function");
          const out = await page.evaluate((ks) => Object.fromEntries(ks.map((k) => [k, t(k)])), [en.cert, en.hint, "Save"]);
          await page.context().close();
          return out;
        })();
        const save = async (page, posts) => {
          const n = posts.length;
          await page.locator(".editor .bar").getByRole("button", { name: w.Save, exact: true }).click();
          for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
          return posts.at(-1);
        };

        {
          const { page, errors, posts, adv } = await open("Home Magpie");
          assert.equal(await adv.evaluate((d) => d.open), false, "folded with nothing set");
          await adv.locator(":scope > summary").click();
          const box = page.locator(".editor .client-cert");
          await box.waitFor();
          assert.ok(await page.locator(".editor", { hasText: w[en.hint] }).count() > 0);
          await box.locator("input.cert-clientCert").fill("~/magpie-laptop/client.pem");
          await box.locator("input.cert-clientKey").fill("~/magpie-laptop/client-key.pem");
          await box.locator("input.cert-serverCA").fill("~/magpie-laptop/ca.pem ");
          for (const i of await box.locator("input").all()) {
            const r = await i.boundingBox();
            assert.ok(r.width > 120 && r.x + r.width <= 420, "fits: " + JSON.stringify(r));
          }
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          const saved = await save(page, posts);
          assert.equal(saved.action, "save");
          assert.equal(saved.body.clientCert, "~/magpie-laptop/client.pem");
          assert.equal(saved.body.clientKey, "~/magpie-laptop/client-key.pem");
          assert.equal(saved.body.serverCA, "~/magpie-laptop/ca.pem");
          assert.deepEqual(errors, []);
          await page.context().close();
        }
        {
          const { page, errors, adv } = await open("Kept Box");
          assert.equal(await adv.evaluate((d) => d.open), true, "opens with them set");
          assert.equal(await page.locator(".editor .client-cert input.cert-serverCA").inputValue(), "~/c/ca.pem");
          assert.deepEqual(errors, []);
          await page.context().close();
        }
        {
          const { page, errors, adv } = await open("Plain Box");
          await adv.locator(":scope > summary").click();
          assert.equal(await page.locator(".editor .client-cert").isVisible(), false, "none at http://");
          assert.deepEqual(errors, []);
          await page.context().close();
        }
        {
          const { page, errors, posts } = await open("OpenAI");
          assert.equal(await page.locator(".editor .client-cert").count(), 0);
          const saved = await save(page, posts);
          assert.equal("clientCert" in saved.body, false, "a preset's Save sends none");
          assert.deepEqual(errors, []);
          await page.context().close();
        }
      });
    }
  });
}
