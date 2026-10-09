// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider's balance token beside new-api's /api/usage/token, which
// takes only the key, is said in the editor with the one click that moves it
// to /api/user/self (and its field to the quota); the New-Api-User header
// that wants is asked for until it is set, Check balance asks as the form
// has it, and the Usage page says the fix for either refusal plainly.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [],
  balanceURL: "https://relay.example.com/api/usage/token", balancePath: "$data.total_available / 500000",
  balanceToken: { takes: true, set: true },
};
const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };

function server(posted, { lang = "en", provider = relay } = {}) {
  const testProviders = { ...providers, providers: [provider] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...state, settings: { ...state.settings, lang } });
    if (url.pathname === "/api/providers") return json(testProviders);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      const body = route.request().postDataJSON();
      posted.push({ action: url.pathname.slice("/api/provider/".length), body });
      if (url.pathname === "/api/provider/balance") {
        // new-api's /api/user/self: the quota once New-Api-User is there
        if (body.headers?.["New-Api-User"]) return json({ ok: true, amount: "$3.00" });
        return json({ ok: true, amount: "", error: "401 Unauthorized: add the header New-Api-User = your user ID (shown in the site's personal settings) to this provider's Headers; the relay said: 无权进行此操作，未提供 New-Api-User" });
      }
      return json(testProviders);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a balance token on /api/usage/token is moved to /api/user/self", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const posted = [];
    await page.route("**/*", server(posted));
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, engine + "-balance-fix.png") });
      }
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=providers");
    await page.locator(".row.provider", { hasText: "Relay" }).click();
    const fix = page.locator(".editor .bal-fix");
    await fix.waitFor();
    assert(await fix.evaluate((e) => e.classList.contains("warn")), "the saved token beside /api/usage/token is a warning");
    assert.match(await fix.textContent(), /\/api\/usage\/token takes the API key, not this token/);
    const use = fix.getByRole("button", { name: "Use https://relay.example.com/api/user/self" });
    await use.click();

    assert.equal(await page.locator(".editor .bal-url").inputValue(), "https://relay.example.com/api/user/self");
    assert.equal(await page.locator(".editor .bal-path").inputValue(), "$data.quota / 500000", "usage/token's field gives way to the quota");
    assert(await page.locator(".editor details.more").evaluate((d) => d.open), "what changed is in view");
    assert(!(await fix.evaluate((e) => e.classList.contains("warn"))));
    assert.match(await fix.textContent(), /New-Api-User = your user ID/, "the header /api/user/self wants is asked for");

    // asked as the form has it, without the header: the fix said plainly
    const check = page.getByRole("button", { name: "Check balance" });
    const res = page.locator(".editor .bal-res");
    await check.click();
    await page.locator(".editor .bal-res.bad").waitFor();
    assert.equal(await res.textContent(), "Add the header New-Api-User = your user ID (shown in the site's personal settings) to the provider's Headers");
    assert.match(await res.getAttribute("title"), /未提供 New-Api-User/, "the relay's own words on hover");
    let asked = posted.filter((p) => p.action === "balance").at(-1).body;
    assert.equal(asked.id, "relay");
    assert.equal(asked.balanceURL, "https://relay.example.com/api/user/self");
    assert.equal(asked.balancePath, "$data.quota / 500000");

    // the header typed in, the hint goes and the balance reads
    const row = page.locator(".editor .headers .pair").first();
    await row.locator("input").nth(0).fill("New-Api-User");
    await row.locator("input").nth(1).fill("42");
    assert.equal(await fix.textContent(), "", "no hint once New-Api-User is set");
    await check.click();
    await page.locator(".editor .bal-res.ok").waitFor();
    assert.equal(await res.textContent(), "Balance $3.00");
    asked = posted.filter((p) => p.action === "balance").at(-1).body;
    assert.deepEqual(asked.headers, { "New-Api-User": "42" });

    // Save keeps what the click set
    await page.locator(".editor .bar").getByRole("button", { name: "Save" }).click();
    await page.waitForTimeout(300);
    const saved = posted.filter((p) => p.action === "save").at(-1).body;
    assert.equal(saved.balanceURL, "https://relay.example.com/api/user/self");
    assert.equal(saved.balancePath, "$data.quota / 500000");
    assert.deepEqual(saved.headers, { "New-Api-User": "42" });

    // the Usage page's card says either fix plainly, in English and Chinese
    const said = await page.evaluate(() => [
      quotaError("the Balance URL https://relay.example.com/api/usage/token takes the API key, not the access token: set it to https://relay.example.com/api/user/self (Balance field $data.quota / 500000) for the account's balance, or remove the token for the key's own"),
      quotaError("401 Unauthorized: add the header New-Api-User = your user ID (shown in the site's personal settings) to this provider's Headers; the relay said: 无权进行此操作，未提供 New-Api-User"),
      quotaError("401 Unauthorized: something else"),
      // ZCode with nothing left to spend: the card gets either the
      // built-in's words (zcodeStartQuota, zcode_start.go) or the
      // plugin's NO_START, and the two are the same string — neither
      // names a way out, so the card says the action itself (#1001)
      quotaError("this account has no GLM Coding Plan, and ZCode's Start Plan has ended or was never started"),
      quotaError("this account has no GLM Coding Plan, and ZCode's Start Plan has ended or was never started"),
      // a sign-in error, which names the address and never reaches the
      // card — it must not be mistaken for the card's
      quotaError("this Z.ai account has no GLM Coding Plan, of its own or a team's, and ZCode's Start Plan has ended or was never started — subscribe at z.ai/subscribe, then add it again"),
      // one that couldn't be read is a reading that failed, and says so
      quotaError("this Z.ai account has no GLM Coding Plan, of its own or a team's, and ZCode's Start Plan could not be read: 502 Bad Gateway"),
    ]);
    assert.match(said[0], /set it to …\/api\/user\/self/);
    assert.match(said[1], /^Add the header New-Api-User = your user ID/);
    assert.equal(said[2], "Allowance unavailable");
    // the card's own words for a ZCode account with nothing to spend, and
    // the action on the card itself (there is none in the error to hover for)
    for (const s of [said[3], said[4]]) {
      assert.match(s, /^ZCode: no GLM Coding Plan, and no free Start Plan/);
      assert.match(s, /subscribe to a GLM Coding Plan to use this account/);
    }
    assert.equal(said[5], "Allowance unavailable", "a Start Plan that couldn't be read is not a plan that ended");
    assert.equal(said[6], "Allowance unavailable", "a sign-in error names the address itself and is not the card's")
    const missing = await page.evaluate(() => [
      "…/api/usage/token takes the API key, not this token: for the account's balance, new-api uses /api/user/self; sub2api uses /api/v1/user/profile.",
      "The token needs a Balance URL: a new-api relay uses /api/user/self; a sub2api panel uses /api/v1/user/profile to read the account's balance.",
      "Use {url}", "Check balance", "Ask the Balance URL now, as the form has it", "No Balance URL to ask",
      "A new-api relay also wants the header New-Api-User = your user ID (shown in the site's personal settings): add it under Headers.",
      "The Balance URL …/api/usage/token takes the API key, not the access token — set it to …/api/user/self in the provider's settings",
      "Add the header New-Api-User = your user ID (shown in the site's personal settings) to the provider's Headers",
      "ZCode: no GLM Coding Plan, and no free Start Plan — subscribe to a GLM Coding Plan to use this account",
    ].filter((k) => !I18N.zh[k]));
    assert.deepEqual(missing, [], "every new string has its Chinese");
    assert.deepEqual(errors, []);
  });

  test(engine + ": a token on a new custom provider with no Balance URL is pointed at /api/user/self", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", server([]));
    t.after(() => browser.close());

    await page.goto("http://magpie.test/?view=providers");
    await page.locator("#providers .row.provider").first().waitFor();
    await page.evaluate(() => { adding = true; editing = { custom: true }; draft = null; renderProviders(); });
    const tok = page.locator(".editor input[type=password]").nth(1);
    await page.locator(".editor input[type=url]").first().fill("https://relay.example.com/v1");
    const fix = page.locator(".editor .bal-fix");
    assert.equal(await fix.textContent(), "", "no token, no hint");
    await tok.fill("access-token");
    assert.match(await fix.textContent(), /The token needs a Balance URL/);
    await fix.getByRole("button", { name: "Use https://relay.example.com/api/user/self" }).click();
    assert.equal(await page.locator(".editor .bal-url").inputValue(), "https://relay.example.com/api/user/self");
    assert.equal(await page.locator(".editor .bal-path").inputValue(), "$data.quota / 500000");
    assert.equal(await page.locator(".editor .bal-res").count(), 0, "a provider not yet added has nothing to check with");
    assert.deepEqual(errors, []);
  });

  for (const lang of ["en", "zh", "ja", "de"]) {
    for (const width of [900, 440]) {
      test(`${engine}: Sub2API balance shortcut saves the profile endpoint (${lang}, ${width}px)`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        t.after(() => browser.close());

        for (const scenario of [
          { name: "saved token without a Balance URL", balanceURL: "", balancePath: "" },
          { name: "saved token with the key-only Balance URL", balanceURL: relay.balanceURL, balancePath: relay.balancePath },
          { name: "new provider with a token and no Balance URL", isNew: true },
        ]) {
          await t.test(scenario.name, async (st) => {
            const page = await context.newPage();
            page.setDefaultTimeout(5000);
            st.after(() => page.close());
            const errors = [], posted = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(posted, { lang, provider: scenario.isNew ? relay : { ...relay, balanceURL: scenario.balanceURL, balancePath: scenario.balancePath } }));
            await page.goto("http://magpie.test/?view=providers");
            await page.locator(".row.provider", { hasText: "Relay" }).waitFor();
            if (scenario.isNew) {
              await page.evaluate(() => { adding = true; editing = { custom: true }; draft = null; renderProviders(); });
              const namePlaceholder = await page.evaluate(() => t("e.g. My Relay"));
              await page.getByPlaceholder(namePlaceholder, { exact: true }).fill("Sub2API");
              await page.locator(".editor input[type=url]").first().fill("https://relay.example.com/v1");
              assert.equal(await page.locator(".editor .bal-fix").textContent(), "", "no token, no warning");
              await page.locator(".editor input[type=password]").nth(1).fill("eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln");
            } else {
              await page.locator(".row.provider", { hasText: "Relay" }).click();
            }

            const fix = page.locator(".editor .bal-fix");
            await fix.waitFor();
            assert.equal(await fix.getByRole("button").count(), 2, "both account balance shortcuts are offered");
            assert.match(await fix.textContent(), /\/api\/user\/self/);
            assert.match(await fix.textContent(), /\/api\/v1\/user\/profile/);
            const to = "https://relay.example.com/api/v1/user/profile";
            const label = await page.evaluate((url) => t("Use {url}", { url }), to);
            const use = fix.getByRole("button", { name: label, exact: true });
            await use.scrollIntoViewIfNeeded();
            const layout = await fix.evaluate((e) => {
              const bounds = e.getBoundingClientRect();
              return {
                fits: e.scrollWidth <= e.clientWidth + 1,
                buttonsFit: [...e.querySelectorAll("button")].every((b) => {
                  const r = b.getBoundingClientRect();
                  return r.left >= bounds.left - 1 && r.right <= bounds.right + 1;
                }),
                pageFits: document.documentElement.scrollWidth <= innerWidth,
              };
            });
            assert.deepEqual(layout, { fits: true, buttonsFit: true, pageFits: true }, "the shortcuts fit without sideways scroll");
            assert.equal(posted.length, 0, "showing the shortcuts writes nothing");
            await use.click();
            assert.equal(await page.locator(".editor .bal-url").inputValue(), to);
            assert.equal(await page.locator(".editor .bal-path").inputValue(), "$data.balance");
            assert(await page.locator(".editor details.more").evaluate((d) => d.open));
            assert.equal(await fix.textContent(), "", "the warning clears without asking for New-Api-User");
            assert.equal(posted.length, 0, "the shortcut changes only the draft");

            const save = await page.evaluate((isNew) => t(isNew ? "Add" : "Save"), !!scenario.isNew);
            const response = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/provider/save");
            await page.locator(".editor .bar").getByRole("button", { name: save, exact: true }).click();
            await response;
            const saved = posted.find((p) => p.action === "save").body;
            assert.equal(saved.balanceURL, to);
            assert.equal(saved.balancePath, "$data.balance");
            assert.equal(saved.new, !!scenario.isNew);
            if (scenario.isNew) assert.equal(saved.balanceToken, "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln");
            assert.deepEqual(errors, []);
          });
        }
      });
    }
  }

  test(`${engine}: account balance shortcuts preserve a user-written Balance field`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width: 440, height: 760 }, reducedMotion: "reduce" });
    t.after(() => browser.close());
    for (const endpoint of ["/api/user/self", "/api/v1/user/profile"]) {
      for (const [typed, replace] of [
        ["", true],
        ["   ", true],
        ["$data.total_available / 500000", true],
        ["$data.total_granted - data.total_used", true],
        ["unlimited_quota", true],
        ["$credits.remaining", false],
        ["$data.balance * 0.5", false],
      ]) {
        await t.test(`${endpoint}: ${JSON.stringify(typed)}`, async (st) => {
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          st.after(() => page.close());
          const posted = [], errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(posted));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: "Relay" }).click();
          await page.locator(".editor details.more summary").click();
          await page.locator(".editor .bal-path").fill(typed);
          const to = "https://relay.example.com" + endpoint;
          await page.locator(".editor .bal-fix").getByRole("button", { name: "Use " + to, exact: true }).click();
          const wanted = replace ? (endpoint === "/api/user/self" ? "$data.quota / 500000" : "$data.balance") : typed;
          assert.equal(await page.locator(".editor .bal-url").inputValue(), to);
          assert.equal(await page.locator(".editor .bal-path").inputValue(), wanted, "a user expression survives the URL change");
          const response = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/provider/save");
          await page.locator(".editor .bar").getByRole("button", { name: "Save", exact: true }).click();
          await response;
          const saved = posted.find((p) => p.action === "save").body;
          assert.equal(saved.balanceURL, to);
          assert.equal(saved.balancePath, wanted);
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
