// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Network and sharing › Passkeys, in a browser on magpie's page
// (weixiongbirnary on X: every new browser or computer needed the key
// copied again). At https, the browser adds a passkey under the name typed
// (or one made from the browser and system), through the browser's own
// WebAuthn sheet; it is listed, and Remove takes it out. The browser's
// sheet gets the challenge, the user id and the passkeys it has already
// decoded, and magpie gets back the bytes the authenticator made, base64url
// as passkey.go reads them. Closing the sheet says nothing. At plain http on
// a network address, or at an IP address, the section says it needs https
// at a name and how, with no Add.
// The app's window (not web) has no such section and never asks for it.
// Every language, Chromium and WebKit; no backend, the API is faked here
// and the authenticator is a software one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const b64u = (b) => Buffer.from(b).toString("base64url");
const CHALLENGE = Buffer.from("a challenge of thirty-two bytes!");
const USER = Buffer.from("user id under the key");
const HAD = Buffer.from("a passkey made before");
const CREATED = new Date(Date.now() - 86400000 * 3).toISOString();
const USED = new Date(Date.now() - 3600000).toISOString();

function server(lang, web, calls) {
  let keys = [{ id: b64u(HAD), name: "Phone", origin: "https://magpie.example.test", created: CREATED, used: USED }];
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},sideGateway:{url:"http://127.0.0.1:3425",running:true,boot:true}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, web });
    if (url.pathname.startsWith("/api/passkeys")) {
      const body = req.method() === "POST" ? JSON.parse(req.postData() || "{}") : null;
      calls.push({ path: url.pathname, body });
      if (url.pathname === "/api/passkeys") return json({ passkeys: keys });
      if (url.pathname === "/api/passkeys/options") return json({
        challenge: b64u(CHALLENGE),
        rp: { id: url.hostname, name: "magpie" },
        user: { id: b64u(USER), name: "magpie · " + url.hostname, displayName: "magpie · " + url.hostname },
        pubKeyCredParams: [{ type: "public-key", alg: -7 }, { type: "public-key", alg: -8 }, { type: "public-key", alg: -257 }],
        excludeCredentials: keys.map((k) => ({ type: "public-key", id: k.id })),
        authenticatorSelection: { residentKey: "required", userVerification: "preferred" },
        attestation: "none",
        timeout: 300000,
      });
      if (url.pathname === "/api/passkeys/add") {
        keys = [...keys, { id: body.id, name: body.name, origin: url.origin, created: new Date().toISOString() }];
        return json({ passkeys: keys });
      }
      if (url.pathname === "/api/passkeys/remove") {
        keys = keys.filter((k) => k.id !== body.id);
        return json({ passkeys: keys });
      }
    }
    if (url.pathname === "/api/settings") return json({ lang, theme: "light", gateway: "http://127.0.0.1:3425", lan: false, lanURLs: [], version: "0.1.900", dir: "/config/magpie", fx: { rate: 7.2, at: new Date().toISOString(), stale: false } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// a software authenticator in place of the browser's sheet: it keeps what
// it was asked, and answers with bytes the test knows, or as a closed
// sheet does when window.__closeSheet is set
const softAuthenticator = () => {
  const bytes = (s) => new TextEncoder().encode(s).buffer;
  window.__asked = [];
  window.PublicKeyCredential = window.PublicKeyCredential || function PublicKeyCredential() {};
  Object.defineProperty(navigator, "credentials", {
    configurable: true,
    value: {
      create: async ({ publicKey }) => {
        const seen = (b) => Array.from(new Uint8Array(b.buffer || b));
        window.__asked.push({
          challenge: seen(publicKey.challenge),
          user: { ...publicKey.user, id: seen(publicKey.user.id) },
          rp: publicKey.rp,
          exclude: publicKey.excludeCredentials.map((c) => seen(c.id)),
          residentKey: publicKey.authenticatorSelection.residentKey,
          algs: publicKey.pubKeyCredParams.map((p) => p.alg),
        });
        if (window.__closeSheet) throw new DOMException("The operation either timed out or was not allowed.", "NotAllowedError");
        return {
          id: "bmV3IHBhc3NrZXk",
          type: "public-key",
          response: { clientDataJSON: bytes('{"type":"webauthn.create"}'), attestationObject: new Uint8Array([0xa3, 0, 255, 254, 62, 63]).buffer },
        };
      },
    },
  });
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a browser on magpie's page adds, lists and removes its passkeys", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-passkeys-${i}.png`) });
      }
      await browser.close();
    });

    const open = async (lang, web, origin, width = 900) => {
      const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], calls = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(softAuthenticator);
      await page.route("**/*", server(lang, web, calls));
      await watchSettingsReads(page);
      await page.goto(origin + "/?view=settings&tab=network");
      await settingsDrawn(page);
      const tr = (s, vars) => page.evaluate(([s, vars]) => t(s, vars), [s, vars]);
      return { page, errors, calls, tr };
    };
    const until = async (page, ok) => {
      // the state is waited for, not a time
      for (let i = 0; i < 250 && !(await ok()); i++) await page.waitForTimeout(20);
      assert(await ok());
    };

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const { page, errors, calls, tr } = await open(lang, true, "https://magpie.example.test");
        const head = page.locator("#passkeyHead");
        const list = page.locator("#passkeyList");
        await list.locator(".passkey-row").first().waitFor();
        assert.equal((await head.textContent()).trim(), await tr("Passkeys"));
        if (lang !== "en") assert.notEqual(await tr("Add passkey"), "Add passkey", "translated");
        assert.equal(await page.locator("#webPageList").isVisible(), false, "Browser access stays the app's");

        const first = list.locator(".row.pref").first();
        assert.equal((await first.locator(".name").textContent()).trim(), await tr("Passkeys"));
        assert.equal((await first.locator(".sub").textContent()).trim(), await tr("Sign in without pasting the key"));
        const had = list.locator(".passkey-row");
        assert.equal(await had.count(), 1);
        assert.equal((await had.locator(".name").textContent()).trim(), "Phone");
        // when it was added and last used; made here, so no origin
        const when = await page.evaluate(([c, u]) => t("Added {when}", { when: stamp(c) }) + " · " + t("Used {when}", { when: ago(u) }), [CREATED, USED]);
        assert.equal((await had.locator(".sub").textContent()).trim(), when);
        const name = first.locator("input.passkey-name");
        const guessed = await name.getAttribute("placeholder");
        assert.match(guessed, engine === "webkit" ? /Safari|Chrome/ : /Chrome/, "named after the browser");

        // closing the browser's sheet adds nothing and says nothing
        await page.evaluate(() => { window.__closeSheet = true; });
        const add = first.getByRole("button", { name: await tr("Add passkey") });
        await reach(page, add);
        await add.click();
        await until(page, () => page.evaluate(() => window.__asked.length === 1));
        await until(page, async () => !(await add.isDisabled()));
        assert(!calls.some((c) => c.path === "/api/passkeys/add"), "a closed sheet adds nothing");
        assert.equal(await page.locator("#status.err").count(), 0, "and says nothing");

        // the sheet gets the challenge, user id and passkeys had, decoded
        await page.evaluate(() => { window.__closeSheet = false; });
        await name.fill("Work laptop");
        await add.click();
        await list.locator(".passkey-row").nth(1).waitFor();
        const asked = await page.evaluate(() => window.__asked[1]);
        assert.deepEqual(asked.challenge, [...CHALLENGE]);
        assert.deepEqual(asked.user.id, [...USER]);
        assert.equal(asked.rp.id, "magpie.example.test");
        assert.deepEqual(asked.exclude, [[...HAD]]);
        assert.equal(asked.residentKey, "required");
        assert.deepEqual(asked.algs, [-7, -8, -257]);
        const added = calls.find((c) => c.path === "/api/passkeys/add").body;
        assert.deepEqual(added, { name: "Work laptop", id: "bmV3IHBhc3NrZXk", clientDataJSON: b64u('{"type":"webauthn.create"}'), attestationObject: b64u([0xa3, 0, 255, 254, 62, 63]) });
        assert.equal((await list.locator(".passkey-row").nth(1).locator(".name").textContent()).trim(), "Work laptop");
        assert.equal(await name.inputValue().catch(() => ""), "", "the name field is empty again");

        // no name typed: the browser's own
        await list.locator("input.passkey-name").fill("");
        await list.getByRole("button", { name: await tr("Add passkey") }).click();
        await until(page, async () => calls.filter((c) => c.path === "/api/passkeys/add").length === 2);
        assert.equal(calls.filter((c) => c.path === "/api/passkeys/add")[1].body.name, guessed);

        // Remove takes out that one alone
        await list.locator(".passkey-row").nth(2).waitFor();
        const rm = list.locator(".passkey-row").filter({ hasText: "Phone" }).getByRole("button", { name: await tr("Remove") });
        await reach(page, rm);
        await rm.click();
        await until(page, async () => (await list.locator(".passkey-row").count()) === 2);
        assert.deepEqual(calls.find((c) => c.path === "/api/passkeys/remove").body, { id: b64u(HAD) });
        assert.equal(await list.locator(".passkey-row").filter({ hasText: "Phone" }).count(), 0);

        assert.equal(await page.locator("#passkeyList select").count(), 0, "no native select");
        assert.deepEqual(errors, []);
      });
    }

    await t.test("plain http on a network address says it needs https, in every language", async () => {
      for (const [lang, origin] of [["en", "http://192.168.1.5:3430"], ["en", "http://magpie.lan:3430"], ["en", "https://192.168.1.5:3430"], ["zh", "http://magpie.lan:3430"], ["zh-TW", "http://192.168.1.5:3430"], ["ja", "http://magpie.lan:3430"], ["de", "http://192.168.1.5:3430"]]) {
        const { page, errors, tr } = await open(lang, true, origin, 360);
        const list = page.locator("#passkeyList");
        const why = list.locator(".passkey-cant");
        await why.waitFor();
        const want = await tr("Passkeys need https at a name: open this page through tailscale serve, a proxy with TLS, or MAGPIE_TLS_CERT");
        if (lang !== "en") assert(!want.startsWith("Passkeys need"), "translated");
        assert.equal((await why.textContent()).trim(), want);
        assert.match(want, /tailscale serve/);
        assert.equal(await list.locator(".passkey-add").count(), 0, "no Add where it can't work");
        assert.equal(await list.locator(".passkey-row").count(), 1, "passkeys made elsewhere are listed, and can be removed");
        const over = await page.evaluate(() => { const b = document.querySelector("#passkeyList"); return b.scrollWidth - b.clientWidth; });
        assert(over <= 0, "nothing wider than a 360px window: " + over);
        assert.deepEqual(errors, []);
      }
    });

    await t.test("the app's window has no passkeys and never asks", async () => {
      const { page, errors, calls } = await open("en", false, "http://localhost:3430");
      await page.locator("#webPageList .row.pref").first().waitFor();
      assert.equal(await page.locator("#passkeyHead").isVisible(), false);
      assert.equal(await page.locator("#passkeyList").isVisible(), false);
      assert.equal(calls.length, 0, "never asks /api/passkeys");
      assert.deepEqual(errors, []);
    });
  });
}
