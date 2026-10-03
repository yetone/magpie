// Real compiled Magpie and real HTTP fixture vendors; no page.route mocks.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { demo, startVendors, startApp } = require("./fixtures.cjs");
const artifactDir = process.env.ARTIFACT_DIR || "/artifacts/screenshots";
for (const engine of ["chromium", "webkit"]) for (const lang of ["en", "zh"]) {
  test(`${engine}/${lang}: real save, reopen and vendor balance query`, { timeout: 120000 }, async (t) => {
    const config = await fs.mkdtemp(path.join(os.tmpdir(), "magpie-balance-e2e-"));
    const vendors = await startVendors();
    let app, browser, page;
    t.after(async () => {
      await fs.mkdir(artifactDir, { recursive: true });
      if (app) await fs.writeFile(path.join(artifactDir, `${engine}-${lang}-app.log`), app.log());
      await fs.writeFile(path.join(artifactDir, `${engine}-${lang}-vendor-requests.json`), JSON.stringify(vendors.requests, null, 2));
      try { if (page) await page.screenshot({ path: path.join(artifactDir, `${engine}-${lang}-e2e-final.png`), fullPage: true }); }
      finally { if (browser) await browser.close(); if (app) await app.close(); await vendors.close(); }
    });
    app = await startApp(config);
    browser = await (engine === "chromium" ? chromium : webkit).launch();
    page = await browser.newPage({ viewport: { width: 1000, height: 1000 }, reducedMotion: "reduce" });
    page.setDefaultTimeout(15000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto(app.url + "&locale=" + lang);
    await page.getByRole("button", { name: lang === "zh" ? "自定义供应商" : "Custom provider", exact: true }).click();
    const ed = page.locator(".editor");
    await ed.locator("input[type=text]").first().fill("New API Demo");
    await ed.locator("input[type=url]").first().fill(`http://127.0.0.1:${demo.newapi.port}/v1`);
    await ed.locator("input[type=password]").nth(0).fill(demo.key);
    await ed.locator("input[type=password]").nth(1).fill(demo.newapi.token);
    assert.equal(await ed.locator(".bal-url").inputValue(), "");
    await ed.locator(".bal-template").click(); await page.locator(".bal-template-menu .pm-item").nth(1).click();
    assert.equal(await ed.locator(".bal-url").inputValue(), `http://127.0.0.1:${demo.newapi.port}/api/user/self`);
    assert.equal(await ed.locator(".bal-path").inputValue(), "$data.quota / 500000");
    assert.equal(await ed.locator(".headers .pair input").nth(1).inputValue(), "");
    await ed.locator(".bar .primary").click();
    await ed.waitFor({ state: "detached" });
    let saved = JSON.parse(await fs.readFile(path.join(config, "magpie", "providers.json")));
    // Read the real on-disk provider store, not a fixture API response.
    const find = (data, name) => (Array.isArray(data) ? data : data.providers).find((p) => p.name === name);
    let newapi = find(saved, "New API Demo");
    assert.equal(newapi.balanceToken, demo.newapi.token);
    assert.equal(newapi.headers?.["New-Api-User"], undefined, "empty header was not persisted");
    await page.locator(".row.provider", { hasText: "New API Demo" }).click();
    assert.equal(await ed.locator(".bal-template").getAttribute("data-value"), "newapi");
    assert.equal(await ed.locator(".headers .pair input").nth(0).inputValue(), "New-Api-User");
    assert.equal(await ed.locator(".headers .pair input").nth(1).inputValue(), "");
    await ed.locator("details.more > summary").click();
    const check = ed.getByRole("button", { name: lang === "zh" ? "查询余额" : "Check balance", exact: true });
    await check.click();
    await ed.locator(".bal-res.bad").waitFor();
    assert.match(await ed.locator(".bal-res").textContent(), /New-Api-User/);
    let accountRequests = vendors.requests.filter((r) => r.kind === "newapi" && r.path === "/api/user/self");
    assert(accountRequests.length > 0);
    assert(accountRequests.every((r) => !r.userHeaderPresent), "an empty account ID was never sent");
    await ed.locator(".headers .pair input").nth(1).fill(demo.newapi.user);
    await check.click();
    await ed.locator(".bal-res.ok").waitFor();
    assert.match(await ed.locator(".bal-res").textContent(), /\$3\.00/);
    await fs.mkdir(artifactDir, { recursive: true });
    await page.screenshot({ path: path.join(artifactDir, `${engine}-${lang}-newapi-success.png`), fullPage: true });
    await ed.locator(".bar .primary").click();
    await ed.waitFor({ state: "detached" });
    saved = JSON.parse(await fs.readFile(path.join(config, "magpie", "providers.json")));
    newapi = find(saved, "New API Demo");
    assert.equal(newapi.headers["New-Api-User"], "42");
    await page.reload();
    await page.locator(".row.provider", { hasText: "New API Demo" }).click();
    assert.equal(await ed.locator(".headers .pair input").nth(1).inputValue(), "42");
    await ed.locator(".bar").getByRole("button", { name: lang === "zh" ? "取消" : "Cancel", exact: true }).click();
    // Add Sub2API through the real add sheet, with the token and template.
    await page.locator("#addProvider").click();
    await page.getByRole("button", { name: lang === "zh" ? "自定义供应商" : "Custom provider", exact: true }).click();
    await ed.locator("input[type=text]").first().fill("Sub2API Demo");
    await ed.locator("input[type=url]").first().fill(`http://127.0.0.1:${demo.sub2api.port}/v1`);
    await ed.locator("input[type=password]").nth(0).fill(demo.key);
    await ed.locator("input[type=password]").nth(1).fill(demo.sub2api.token);
    assert.equal(await ed.locator(".bal-template").getAttribute("data-value"), "", "the JWT did not select the platform");
    await ed.locator(".bal-template").click(); await page.locator(".bal-template-menu .pm-item").nth(2).click();
    await ed.locator(".bar .primary").click();
    await ed.waitFor({ state: "detached" });
    await page.locator(".row.provider", { hasText: "Sub2API Demo" }).click();
    assert.equal(await ed.locator(".bal-template").getAttribute("data-value"), "sub2api");
    await ed.locator("details.more > summary").click();
    await ed.getByRole("button", { name: lang === "zh" ? "查询余额" : "Check balance", exact: true }).click();
    await ed.locator(".bal-res.ok").waitFor();
    assert.match(await ed.locator(".bal-res").textContent(), /\$12\.50/);
    const sub = vendors.requests.filter((r) => r.kind === "sub2api" && r.path === "/api/v1/user/profile").at(-1);
    assert.equal(sub.authorization, "Bearer " + demo.sub2api.token);
    assert.equal(sub.userHeaderPresent, false);
    accountRequests = vendors.requests.filter((r) => r.kind === "newapi" && r.user === "42");
    assert(accountRequests.every((r) => r.authorization === demo.newapi.token));
    assert.deepEqual(errors, []);
  });
}
