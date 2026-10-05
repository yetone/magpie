// Local discovery is an optional hint. Only the existing import picker writes
// providers, after the reader chooses them. All APIs and credentials are fixtures.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.ASSET_DIR || path.resolve(__dirname, "../assets");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];

async function fixture(t, engine, lang = "en", options = {}) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(7000);
  const errors = [], imported = new Set(), posts = [];
  const control = { scans: 0, reads: 0, posts, release: null, fail: false, empty: false, revisions: {}, omitted: [], extra: [] };
  const provider = (id, name) => ({ id, name, icon: "generic", host: "relay.example", chat: "https://relay.example/v1", models: [], agents: [], key: { set: true, masked: "sk-…1234" } });
  const state = () => ({ providers: [...(options.firstUse ? [] : [provider("existing", "Existing Relay")]), ...[...imported].map((id) => provider(id, id === "alpha" ? "Alpha Relay" : "Beta Relay"))],
    presets: [{ id: "openai", name: "OpenAI", kind: "vendor", icon: "openai", chat: "https://api.openai.example/v1" }], excluded: [], gateway: { running: true, window: true } });
  const sources = () => options.manualOnly ? [{
    id: options.manualOnly === "off" ? "alma" : "codex",
    name: options.manualOnly === "off" ? "Alma" : "Codex", path: "/fixture/config", found: true,
    items: [{ ref: "manual", fingerprint: "manual-1", status: options.manualOnly === "collision" ? "taken" : "new",
      off: options.manualOnly === "off" ? "turned off in Alma" : "", existing: options.manualOnly === "collision" ? "Existing Relay" : "",
      provider: { id: "manual", name: "Manual Relay", chat: "https://manual.example/v1", key: "sk-…1234", models: ["fixture-model"] } }],
  }] : [{ id: "codex", name: "Codex", path: "/fixture/.codex/config.toml", found: true, items: [
    ...["alpha", "beta"].map((ref) => ({ ref, fingerprint: `${ref}-${control.revisions[ref] || 1}`, from: "config.toml", status: imported.has(ref) ? "same" : "new",
      provider: { id: ref, name: ref === "alpha" ? "Alpha Relay" : "Beta Relay", responses: `https://${ref}.example/v1`, key: "sk-…1234", models: ["fixture-model"] } })),
    { ref: "existing", status: "same", provider: { name: "Already here" } },
    { ref: "magpie", skip: "it points at magpie itself", provider: { name: "magpie" } },
  ] }];
  page.on("pageerror", (e) => errors.push(e.stack || e.message));
  await page.route("**/*", async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(state());
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/importapps/discovery") {
      control.scans++;
      if (control.scans === 1) control.firstScan = req;
      const result = control.empty || options.manualOnly ? [] : ["alpha", "beta", ...control.extra]
        .filter((id) => !imported.has(id) && !control.omitted.includes(id))
        .map((id) => ({ fingerprint: `${id}-${control.revisions[id] || 1}`, source: "Codex" }));
      if (options.defer && control.scans === 1) await new Promise((resolve) => { control.release = resolve; });
      if (control.fail) return route.fulfill({ status: 500, body: "Fixture discovery unavailable" });
      return json(result);
    }
    if (url.pathname === "/api/importapps") {
      if (req.method() === "GET") { control.reads++; return json(sources()); }
      const body = req.postDataJSON();
      posts.push(body);
      for (const p of body.picks) imported.add(p.ref);
      return json({ added: body.picks.map((p) => p.ref), state: state() });
    }
    if (url.pathname === "/api/gateway/trace") return new Promise(() => {});
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  });
  t.after(async () => { control.release?.(); await browser.close(); assert.deepEqual(errors, []); });
  const go = async (view) => {
    const loaded = view === "providers" ? page.waitForResponse((r) => new URL(r.url()).pathname === "/api/providers") : null;
    await page.evaluate((v) => show(v), view);
    if (loaded) await loaded;
  };
  return { page, control, go };
}

for (const engine of engines) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: discover in the background, review picks, and refresh after import`, async (t) => {
      const { page, control, go } = await fixture(t, engine, lang, { defer: true });
      await page.goto("http://magpie.test/?view=agents");
      await page.waitForFunction(() => !!document.querySelector('#nav button[data-view="agents"].on'));
      assert.equal(control.scans, 0, "other pages do not scan local configs");
      await go("providers");
      await page.locator('#providers .row[data-id="existing"]').waitFor();
      // Providers render while discovery is waiting. A late hint does not
      // rebuild the Add sheet or lose a search being typed there.
      await page.locator("#addProvider").click();
      await page.locator("#addSheet .find").fill("OpenAI");
      assert.equal(control.scans, 1);
      assert.equal(await page.locator("#modal").isHidden(), true);
      control.release();
      await page.waitForFunction(() => !discoveryPending && providerDiscovery.length === 2);
      assert.equal(await page.locator("#addSheet .find").inputValue(), "OpenAI");
      await page.locator("#addSheet").getByRole("button", { name: lang === "zh" ? "关闭" : "Close", exact: true }).click();
      assert.equal(await page.locator("#providerDiscoveryCount").textContent(), lang === "zh" ? "发现 2 项本地供应商配置" : "Found 2 local provider configurations");
      assert.equal(await page.locator("#providerDiscoverySources").textContent(), "Codex");
      assert.equal(await page.locator(".after-list #providerDiscovery.compact").count(), 1);
      assert.equal(await page.locator("#providerDiscovery .who").isHidden(), true);
      assert.equal(await page.locator("#reviewProviderDiscovery").textContent(), lang === "zh" ? "导入本地配置（2）…" : "Import local configurations (2)…");
      assert.equal(control.reads, 0, "automatic discovery does not load provider details");
      assert.deepEqual(control.posts, [], "automatic discovery never imports");
      await go("agents");
      await go("providers");
      assert.equal(control.scans, 1, "returning promptly reuses the discovery result");
      // The hint and both actions remain inside the page in a narrow window.
      await page.setViewportSize({ width: 390, height: 800 });
      const bounds = await page.locator("#providerDiscovery").evaluate((el) => {
        const r = el.getBoundingClientRect();
        return [...el.querySelectorAll("button")].every((b) => { const q = b.getBoundingClientRect(); return q.left >= r.left && q.right <= r.right; }) && el.scrollWidth <= el.clientWidth;
      });
      assert(bounds, "discovery actions fit the narrow window");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-discovery.png`) });
      }
      await page.setViewportSize({ width: 900, height: 800 });
      await page.locator("#reviewProviderDiscovery").click();
      const picker = page.locator("#modal .importapps");
      await picker.locator(".approw").first().waitFor();
      assert.equal(control.reads, 1);
      assert.equal(await picker.locator('.approw input:disabled').count(), 2);
      await picker.locator(".approw", { hasText: "Beta Relay" }).locator("input").uncheck();
      await picker.locator(".bar .primary").click();
      await page.waitForFunction(() => document.querySelector("#providerDiscoveryCount").textContent.includes("1") && !document.querySelector("#providerDiscovery").hidden);
      assert.deepEqual(control.posts, [{ picks: [{ source: "codex", ref: "alpha", mode: "add" }] }]);
      assert.equal(control.scans, 2, "an import immediately rescans, bypassing the cache");
      assert.equal(await page.locator("#providerDiscoveryCount").textContent(), lang === "zh" ? "发现 1 项本地供应商配置" : "Found 1 local provider configuration");
      await page.locator("#reviewProviderDiscovery").click();
      await picker.locator(".approw input:not(:disabled)").waitFor();
      await picker.locator(".bar .primary").click();
      await page.locator('#providers .row[data-id="beta"]').waitFor();
      await page.locator("#providerDiscovery").waitFor({ state: "hidden" });
      assert.equal(control.posts.length, 2);
    });
  }

  test(`${engine}: ignored configurations stay quiet across reloads, removal and partial import`, async (t) => {
    const { page, control, go } = await fixture(t, engine);
    await page.goto("http://magpie.test/?view=providers");
    await page.locator("#providerDiscovery").waitFor();
    await page.locator("#dismissProviderDiscovery").click();
    await page.locator("#providerDiscovery").waitFor({ state: "hidden" });
    await go("agents");
    await go("providers");
    assert.equal(await page.locator("#providerDiscovery").isHidden(), true);
    assert.equal(control.scans, 1);
    const reload = async () => {
      await page.reload();
      await page.waitForFunction(() => discoveryAt > 0 && !discoveryPending);
    };
    await reload();
    assert.equal(await page.locator("#providerDiscovery").isHidden(), true, "dismissal survives a new page lifetime");
    control.omitted = ["alpha"];
    await reload();
    assert.equal(await page.locator("#providerDiscovery").isHidden(), true, "removal does not revive ignored candidates");
    control.omitted = [];
    await reload();
    assert.equal(await page.locator("#providerDiscovery").isHidden(), true, "returning unchanged candidates stay ignored");
    await page.locator("#addProvider").click();
    await page.locator("#addSheet").getByRole("button", { name: "Import…", exact: true }).click();
    const picker = page.locator("#modal .importapps");
    await picker.locator(".approw").first().waitFor();
    assert.equal(control.reads, 1);
    assert.deepEqual(control.posts, []);
    await picker.locator(".approw", { hasText: "Beta Relay" }).locator("input").uncheck();
    await picker.locator(".bar .primary").click();
    await page.waitForFunction(() => !discoveryPending && providerDiscovery.length === 1);
    assert.equal(await page.locator("#providerDiscovery").isHidden(), true, "partial import keeps the remaining dismissal");
    control.revisions.beta = 2;
    await reload();
    await page.locator("#providerDiscovery").waitFor();
    assert.equal(await page.locator("#providerDiscoveryCount").textContent(), "Found 1 local provider configuration");
    await page.locator("#dismissProviderDiscovery").click();
    control.extra = ["gamma"];
    await reload();
    await page.locator("#providerDiscovery").waitFor();
    assert.equal(await page.locator("#providerDiscoveryCount").textContent(), "Found 1 local provider configuration", "only the newly added candidate is offered");
  });

  test(`${engine}: first use offers discovered configs beside the automatic Add sheet`, async (t) => {
    const { page, control } = await fixture(t, engine, "en", { firstUse: true });
    await page.goto("http://magpie.test/?view=providers");
    await page.locator("#providerDiscovery").waitFor();
    assert.equal(await page.locator("#addSheet").isVisible(), true);
    assert.equal(await page.locator("#view-providers > #providerDiscovery:not(.compact)").count(), 1);
    assert.equal(await page.locator("#providerDiscovery .who").isVisible(), true);
    assert.equal(await page.locator("#providers .row").count(), 0);
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.setViewportSize({ width: 390, height: 800 });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-first-use-provider-discovery.png`) });
      await page.setViewportSize({ width: 900, height: 800 });
    }
    assert.deepEqual(control.posts, []);
    await page.locator("#reviewProviderDiscovery").click();
    await page.locator("#modal .approw").first().waitFor();
    await page.locator("#modal .importapps .bar .primary").click();
    await page.locator('#providers .row[data-id="alpha"]').waitFor();
    await page.locator("#addSheet").waitFor({ state: "hidden" });
    assert.equal(await page.locator("#providers .row").count(), 2);
  });

  test(`${engine}: reviewing a new offer does not select previously ignored configurations`, async (t) => {
    const { page, control } = await fixture(t, engine);
    await page.goto("http://magpie.test/?view=providers");
    await page.locator("#providerDiscovery").waitFor();
    await page.locator("#dismissProviderDiscovery").click();
    control.revisions.beta = 2;
    await page.reload();
    await page.locator("#providerDiscovery").waitFor();
    await page.locator("#reviewProviderDiscovery").click();
    const picker = page.locator("#modal .importapps");
    await picker.locator(".approw").first().waitFor();
    assert.equal(await picker.locator(".approw", { hasText: "Alpha Relay" }).locator("input").isChecked(), false);
    assert.equal(await picker.locator(".approw", { hasText: "Beta Relay" }).locator("input").isChecked(), true);
    await picker.locator(".bar .primary").click();
    await page.waitForFunction(() => !discoveryPending && providerDiscovery.length === 1);
    assert.deepEqual(control.posts, [{ picks: [{ source: "codex", ref: "beta", mode: "add" }] }]);
    assert.equal(await page.locator("#providerDiscovery").isHidden(), true);
  });

  for (const lang of ["ja", "de"]) {
    test(`${engine} ${lang}: the compact entry fits a narrow window`, async (t) => {
      const { page } = await fixture(t, engine, lang);
      await page.setViewportSize({ width: 390, height: 800 });
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#providerDiscovery").waitFor();
      assert(await page.locator("#providerDiscovery").evaluate((el) => {
        const r = el.getBoundingClientRect();
        return r.left >= 0 && r.right <= innerWidth && el.scrollWidth <= el.clientWidth;
      }));
    });
  }

  test(`${engine}: a later visit discovers configurations added since the previous scan`, async (t) => {
    const { page, control, go } = await fixture(t, engine);
    const now = Date.now();
    await page.clock.setFixedTime(now);
    control.empty = true;
    await page.goto("http://magpie.test/?view=providers");
    await page.waitForFunction(() => discoveryAt > 0);
    assert.equal(await page.locator("#providerDiscovery").isHidden(), true);
    control.empty = false;
    await go("agents");
    await page.clock.setFixedTime(now + 61000);
    await go("providers");
    await page.locator("#providerDiscovery").waitFor();
    assert.equal(control.scans, 2);
    assert.equal(await page.locator("#providerDiscoveryCount").textContent(), "Found 2 local provider configurations");
  });

  test(`${engine}: a late scan cannot restore the hint after importing`, async (t) => {
    const { page, control } = await fixture(t, engine, "en", { defer: true });
    await page.goto("http://magpie.test/?view=providers");
    await page.locator('#providers .row[data-id="existing"]').waitFor();
    await page.locator("#addProvider").click();
    await page.locator("#addSheet").getByRole("button", { name: "Import…", exact: true }).click();
    await page.locator("#modal .approw").first().waitFor();
    await page.locator("#modal .importapps .bar .primary").click();
    await page.locator('#providers .row[data-id="beta"]').waitFor();
    const late = page.waitForResponse((r) => r.request() === control.firstScan);
    control.release();
    await (await late).finished();
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await page.waitForFunction(() => !discoveryPending);
    assert.equal(await page.locator("#providerDiscovery").isHidden(), true);
    assert.equal(control.scans, 2);
  });

  for (const result of ["empty", "fail"]) {
    test(`${engine}: ${result} discovery leaves manual setup usable`, async (t) => {
      const { page, control } = await fixture(t, engine);
      control[result] = true;
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#providers .row[data-id="existing"]').waitFor();
      await page.waitForFunction(() => discoveryAt > 0);
      assert.equal(await page.locator("#providerDiscovery").isHidden(), true);
      assert.equal(await page.locator("#modal").isHidden(), true);
      await page.locator("#addProvider").click();
      await page.locator("#addSheet .tile").first().waitFor();
      assert.deepEqual(control.posts, []);
    });
  }

  for (const manualOnly of ["collision", "off"]) {
    test(`${engine}: only ${manualOnly} entries leave the hint hidden and remain in manual import`, async (t) => {
      const { page, control } = await fixture(t, engine, "en", { manualOnly, firstUse: manualOnly === "off" });
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForFunction(() => discoveryAt > 0 && !discoveryPending);
      assert.equal(await page.locator("#providerDiscovery").isHidden(), true);
      assert.equal(control.reads, 0);
      if (await page.locator("#addSheet").isHidden()) await page.locator("#addProvider").click();
      await page.locator("#addSheet").getByRole("button", { name: "Import…", exact: true }).click();
      const picker = page.locator("#modal .importapps"), box = picker.locator(".approw input");
      await box.waitFor();
      assert.equal(await box.isChecked(), false);
      assert.equal(await box.isDisabled(), false, "manual import still allows selecting the configuration");
      assert.equal(await picker.locator(".bar .primary").isDisabled(), true);
      await box.check();
      assert.equal(await picker.locator(".bar .primary").isEnabled(), true);
      assert.deepEqual(control.posts, [], "selection alone never saves a provider");
    });
  }
}
