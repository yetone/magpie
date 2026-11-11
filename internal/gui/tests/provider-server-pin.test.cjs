// Run with Node's test runner and Playwright on the module path; see README.md.
// A Remote magpie (or a provider of the user's) at https:// with a
// self-signed certificate (Zhangzhangi on X): the editor's Advanced holds a
// Server certificate row. "Trust its certificate…" asks provider/peekcert,
// shows the fingerprint and why this computer refuses it, and "Trust this
// certificate" pins it; Save sends serverPin. A pinned one says in red that
// verification is off, and "Verify it again" clears it. A preset pointed at
// a server of the user's own takes one too (LM Studio at https://lms.box.lan,
// "建议对别的自签名域名也生效"), with no client certificate. None at http://
// or at a vendor's own address. Every language, 1100px and 420px, Chromium and WebKit; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const file = async (route, url) => {
  const f = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(f)];
  await route.fulfill({ body: await fs.readFile(f), contentType });
};
const boot = (route, lang) => route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });

const SHA = "3f".repeat(16) + "a0".repeat(16);
const PINNED = "ab".repeat(32);
const colon = (h) => h.toUpperCase().match(/../g).join(":");
const WHY = 'x509: "api.magpie.local" certificate is not standards compliant';

const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m", name: "m", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "mk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const remote = { ...base, id: "remote-magpie", name: "Remote magpie", preset: "remote-magpie", icon: "magpie", chat: "https://api.magpie.local:13003/v1", takesClientCert: true };
const own = { ...base, id: "box", name: "Own Box", chat: "https://box.example/v1", takesClientCert: true };
const pinned = { ...base, id: "pinned", name: "Pinned Box", chat: "https://pinned.example/v1", takesClientCert: true, serverPin: PINNED };
const plain = { ...base, id: "plain", name: "Plain Box", chat: "http://192.168.1.21:8000/v1", takesClientCert: true };
const lms = { ...base, id: "lmstudio", name: "LM Studio Box", preset: "lmstudio", icon: "lmstudio", chat: "https://lms.box.lan/v1", takesClientCert: false, takesServerPin: true };
const openai = { ...base, id: "openai", name: "OpenAI", preset: "openai", chat: "https://api.openai.example/v1", takesClientCert: false };

const en = {
  row: "Server certificate",
  hint: "For your own server with a self-signed certificate. Check the fingerprint against the server's before you trust it; a certificate renewed later has to be trusted again.",
  peek: "Trust its certificate…",
  trust: "Trust this certificate",
  off: "Certificate not verified: only this one is accepted",
  again: "Verify it again",
  seen: "{subject}, issued by {issuer}, until {date}",
  trusted: "This computer already trusts it: nothing to pin.",
};

function server(lang, posts, peek) {
  const providers = {
    providers: [remote, own, pinned, plain, lms, openai],
    presets: [
      { id: "openai", name: "OpenAI", kind: "vendor", icon: "openai", chat: "https://api.openai.example/v1", added: true },
      { id: "remote-magpie", name: "Remote magpie", kind: "magpie", icon: "magpie", chat: "", added: true },
      { id: "lmstudio", name: "LM Studio", kind: "local", icon: "lmstudio", chat: "http://localhost:1234/v1", added: true },
    ],
    excluded: [], gateway: { running: true, window: true },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return boot(route, lang);
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      const action = url.pathname.slice("/api/provider/".length);
      posts.push({ action, body: route.request().postDataJSON() });
      if (action === "peekcert") return peek.error ? json({ error: peek.error }, 400) : json(peek.seen);
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    await file(route, url);
  };
}

async function open(browser, lang, name, width, peek = {}) {
  const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const posts = [];
  await page.route("**/*", server(lang, posts, peek));
  await page.goto("http://magpie.test/?view=providers");
  await page.locator(".row.provider", { hasText: name }).first().click();
  await page.locator(".editor .bar").waitFor();
  const adv = page.locator(".editor details.advanced");
  return { page, errors, posts, adv };
}

const words = (page, keys) => page.evaluate((ks) => Object.fromEntries(ks.map((k) => [k, t(k)])), keys);

async function save(page, posts) {
  const n = posts.length;
  const label = await page.evaluate(() => t("Save"));
  await page.locator(".editor .bar").getByRole("button", { name: label, exact: true }).click();
  for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
  return posts.at(-1);
}

async function fits(page, box, width) {
  const bad = await box.evaluate((b, width) => {
    const out = [];
    for (const e of b.querySelectorAll("button, code, .hint, .pin-off")) {
      const r = e.getBoundingClientRect();
      if (r.width === 0) continue;
      if (r.right > width + 0.5) out.push(e.className + " " + r.right + ">" + width);
      if (e.matches("button") && e.scrollWidth > e.clientWidth + 1) out.push("cut: " + e.textContent);
    }
    if (document.documentElement.scrollWidth > innerWidth) out.push("page scrolls sideways");
    return out;
  }, width);
  assert.deepEqual(bad, []);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a self-signed server certificate is trusted by fingerprint (Zhangzhangi on X)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    await t.test("every language has the strings", async () => {
      const { page } = await open(browser, "en", "Plain Box", 1100);
      for (const lang of ["zh", "zh-TW", "ja", "de"]) {
        const missing = await page.evaluate(([lang, keys]) => keys.filter((k) => !I18N[lang]?.[k] || I18N[lang][k] === k), [lang, Object.values(en)]);
        assert.deepEqual(missing, [], lang);
      }
      await page.context().close();
    });

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        for (const name of ["Remote magpie", "Own Box"]) {
          await t.test(`${lang} ${width}px ${name}: peek, trust, save`, async () => {
            const seen = { host: "api.magpie.local:13003", sha256: SHA, subject: "api.magpie.local", issuer: "api.magpie.local", notAfter: "2027-11-10T00:00:00Z", checks: false, why: WHY };
            const { page, errors, posts, adv } = await open(browser, lang, name, width, { seen });
            const w = await words(page, Object.values(en));
            if (lang !== "en") for (const s of Object.values(en)) assert.notEqual(w[s], s, `"${s}" has no ${lang} translation`);
            assert.equal(await adv.evaluate((d) => d.open), false, "folded with nothing set");
            await adv.locator(":scope > summary").click();
            const box = page.locator(".editor .server-pin");
            await box.waitFor();
            assert.ok(await page.locator(".editor", { hasText: w[en.row] }).count() > 0);
            assert.equal(await box.locator(".pin-off").count(), 0, "verified until asked");

            await box.locator(".pin-peek").click();
            const card = box.locator(".pin-seen");
            await card.waitFor();
            const peeked = posts.find((p) => p.action === "peekcert");
            assert.equal(peeked.body.id, name === "Remote magpie" ? "remote-magpie" : "box");
            assert.equal((await card.locator(".pin-fp").textContent()).trim(), colon(SHA));
            assert.equal((await card.locator(".hint.err").textContent()).trim(), WHY);
            await fits(page, box, width);

            await card.locator(".pin-trust").click();
            await box.locator(".pin-off").waitFor();
            assert.equal((await box.locator(".pin-off").textContent()).trim(), w[en.off]);
            const red = await box.locator(".pin-off").evaluate((e) => getComputedStyle(e).color);
            assert.notEqual(red, "rgb(0, 0, 0)", "said in red, not plain text");
            const border = await box.locator(".pin-off").evaluate((e) => getComputedStyle(e).borderLeftWidth);
            assert(border === "0px" || border === "", "no left border accent: " + border);
            assert.equal((await box.locator(".pin-fp").textContent()).trim(), colon(SHA));
            await fits(page, box, width);

            const saved = await save(page, posts);
            assert.equal(saved.action, "save");
            assert.equal(saved.body.serverPin, SHA);
            assert.deepEqual(errors, []);
            await page.context().close();
          });
        }
      }
    }

    await t.test("a pinned one says so, and Verify it again clears it", async () => {
      const { page, errors, posts, adv } = await open(browser, "zh", "Pinned Box", 420);
      assert.equal(await adv.evaluate((d) => d.open), true, "opens with a pin set");
      const box = page.locator(".editor .server-pin");
      const w = await words(page, [en.off]);
      assert.equal((await box.locator(".pin-off").textContent()).trim(), w[en.off]);
      assert.equal((await box.locator(".pin-fp").textContent()).trim(), colon(PINNED));
      await box.locator(".pin-drop").click();
      await box.locator(".pin-peek").waitFor();
      const saved = await save(page, posts);
      assert.equal(saved.body.serverPin, "");
      assert.deepEqual(errors, []);
      await page.context().close();
    });

    await t.test("a certificate this computer trusts offers no pin", async () => {
      const seen = { host: "box.example", sha256: SHA, subject: "box.example", issuer: "R11", notAfter: "2027-01-01T00:00:00Z", checks: true, why: "" };
      const { page, errors, adv } = await open(browser, "en", "Own Box", 1100, { seen });
      await adv.locator(":scope > summary").click();
      const box = page.locator(".editor .server-pin");
      await box.locator(".pin-peek").click();
      await box.locator(".pin-seen").waitFor();
      assert.equal(await box.locator(".pin-trust").count(), 0);
      assert.ok((await box.locator(".pin-seen").textContent()).includes(en.trusted));
      await box.locator(".pin-cancel").click();
      await box.locator(".pin-peek").waitFor();
      assert.deepEqual(errors, []);
      await page.context().close();
    });

    await t.test("a server that can't be reached says why", async () => {
      const { page, errors, adv } = await open(browser, "en", "Own Box", 1100, { error: "dial tcp: connection refused" });
      await adv.locator(":scope > summary").click();
      const box = page.locator(".editor .server-pin");
      await box.locator(".pin-peek").click();
      await box.locator(".hint.err").waitFor();
      assert.ok((await box.locator(".hint.err").textContent()).includes("connection refused"));
      assert.equal(await box.locator(".pin-peek").isEnabled(), true, "can ask again");
      assert.deepEqual(errors, []);
      await page.context().close();
    });

    await t.test("a preset at the user's own https server takes a pin, and no client certificate", async () => {
      const seen = { host: "lms.box.lan", sha256: SHA, subject: "lms.box.lan", issuer: "lms.box.lan", notAfter: "2027-11-10T00:00:00Z", checks: false, why: WHY };
      const { page, errors, posts, adv } = await open(browser, "zh", "LM Studio Box", 420, { seen });
      await adv.locator(":scope > summary").click();
      const box = page.locator(".editor .server-pin");
      await box.waitFor();
      assert.equal(await page.locator(".editor .client-cert").count(), 0, "a client certificate stays for a provider of the user's own");
      await box.locator(".pin-peek").click();
      await box.locator(".pin-trust").click();
      await box.locator(".pin-off").waitFor();
      await fits(page, box, 420);
      const saved = await save(page, posts);
      assert.equal(saved.body.serverPin, SHA);
      assert.equal("clientCert" in saved.body, false);
      assert.deepEqual(errors, []);
      await page.context().close();
    });

    await t.test("none at http:// or at a vendor's own address", async () => {
      {
        const { page, errors, adv } = await open(browser, "en", "Plain Box", 1100);
        await adv.locator(":scope > summary").click();
        assert.equal(await page.locator(".editor .server-pin").isVisible(), false, "none at http://");
        assert.deepEqual(errors, []);
        await page.context().close();
      }
      {
        const { page, errors, posts, adv } = await open(browser, "en", "OpenAI", 1100);
        await adv.locator(":scope > summary").click();
        assert.equal(await page.locator(".editor .server-pin").isVisible(), false, "none at the vendor's own address");
        const saved = await save(page, posts);
        assert.equal("serverPin" in saved.body, false, "a preset's Save sends none");
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  });
}
