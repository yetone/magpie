// Run with Node's test runner and Playwright on the module path; see README.md.
// The Gateway page's model list (ARNO on Discord): a search box finds models
// by id, name, provider and a group's models, and each row ends with what
// agents are told of the model — its reasoning levels, whether it takes
// images, video or audio, its context, its reply limit — the full detail in the chips' tooltip; a routing
// group's levels are what its models all have, its images any one's and its context the largest, its tooltip naming them.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const providers = {
  providers: [
    { id: "acme", name: "Acme", icon: "generic", agents: [], models: [
      { id: "gpt-5.5", name: "GPT-5.5", on: true, efforts: ["low", "medium", "high", "xhigh"], images: true, context: 400000, output: 128000 },
      { id: "gpt-5.5-mini", name: "GPT-5.5 mini", on: true, efforts: ["low", "medium", "high", "xhigh"], kept: ["low", "medium"], images: true, context: 272000 },
      { id: "text-only", name: "Text Only", on: true, images: false, context: 128000 },
      { id: "hidden", name: "Hidden", on: false, efforts: ["low"] },
    ] },
    { id: "zeta", name: "Zeta Cloud", icon: "generic", agents: [], models: [
      { id: "glm-5", name: "GLM-5", on: true, efforts: ["high"], images: false, inputs: ["audio"], context: 1000000 },
      { id: "kimi-k3", name: "Kimi K3", on: true, given: true, efforts: ["low", "high"], images: true, inputs: ["video", "audio"] },
    ] },
  ],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", models: 6, calls: [], groups: [
    { id: "group/smart", name: "Smart", icons: ["generic", "generic"], providers: ["Acme", "Zeta Cloud"],
      efforts: ["low", "medium", "high"], images: true, inputs: ["video"], context: 272000, output: 64000, members: ["acme/gpt-5.5-mini", "zeta/kimi-k3"] },
  ] },
};
const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };

async function serve(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json(state);
  if (url.pathname === "/api/providers") return json(providers);
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/gateway/trace") {
    if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
    return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
  }
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

const rowOf = (page, id) => page.locator("#gwModels .row.model").filter({ has: page.locator(".name", { hasText: new RegExp("^" + id.replace(/[./]/g, "\\$&") + "$") }) });
const ids = (page) => page.locator("#gwModels .row.model .name").allTextContents();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Gateway's models can be searched and say what they take", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve);
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.setViewportSize({ width: 900, height: 1100 });
        await page.locator("#findModel").fill("");
        await page.locator("#gwModels .row.model").first().hover();
        await page.waitForTimeout(300);
        await page.locator("#gwModels").screenshot({ path: path.join(process.env.ARTIFACT_DIR, engine + "-gw-models.png") });
      }
      await browser.close();
    });

    await page.goto("http://magpie.test/?view=gateway");
    await page.locator("#gwModels .row.model").first().waitFor();
    assert.deepEqual(await ids(page), ["group/smart", "acme/gpt-5.5", "acme/gpt-5.5-mini", "acme/text-only", "zeta/glm-5", "zeta/kimi-k3"]);

    // the info at each row's end, and its tooltip
    const info = async (id) => {
      const box = rowOf(page, id).locator(".minfo");
      return { chips: (await box.locator(".badge").allTextContents()).map((s) => s.trim()), img: await box.locator(".mi-img").count(), media: await box.locator(".mi-media").evaluateAll((cs) => cs.map((c) => c.classList[2])), tip: await box.getAttribute("title") };
    };
    let i = await info("acme/gpt-5.5");
    assert.deepEqual(i.chips, ["low–xhigh", "", "400K"]);
    assert.equal(i.img, 1);
    // its reply limit, as agents are told it (ARNO: a provider's model's
    // was said only once written to an agent's config)
    assert.equal(i.tip, "Reasoning: low, medium, high, xhigh\nAccepts images\nContext: 400,000 tokens\nOutput: up to 128,000 tokens");
    i = await info("acme/gpt-5.5-mini");
    assert.deepEqual(i.chips, ["low / medium", "", "272K"], "the levels kept, not all it has");
    i = await info("acme/text-only");
    assert.deepEqual(i.chips, ["128K"]);
    assert.equal(i.img, 0);
    assert.equal(i.tip, "Reasoning levels: none known\nText only\nContext: 128,000 tokens");
    assert.deepEqual(i.media, [], "no video or audio listed, none said");
    // video and audio, when its list says so (pingqipai3 on X)
    i = await info("zeta/glm-5");
    assert.deepEqual(i.chips, ["high", "", "1M"]);
    assert.deepEqual(i.media, ["mi-audio"]);
    assert.equal(i.tip, "Reasoning: high\nText only\nAccepts audio\nContext: 1,000,000 tokens");
    i = await info("zeta/kimi-k3");
    assert.deepEqual(i.chips, ["", "", ""], "levels it can be given but wasn't aren't said");
    assert.equal(i.img, 1);
    assert.deepEqual(i.media, ["mi-video", "mi-audio"]);
    assert.equal(i.tip, "Reasoning levels: none known\nAccepts images\nAccepts video\nAccepts audio");
    i = await info("group/smart");
    assert.deepEqual(i.chips, ["low–high", "", "", "272K"]);
    assert.deepEqual(i.media, ["mi-video"]);
    assert.equal(i.tip, "Reasoning: low, medium, high (the levels every model in it has)\nAccepts images (one of its models does)\nAccepts video (one of its models does)\nContext: 272,000 tokens (the largest of its models')\nOutput: up to 64,000 tokens\nModels: acme/gpt-5.5-mini, zeta/kimi-k3");
    // the chips sit at the row's end, before its copy button
    const [infoBox, copyBox, whoBox] = await Promise.all([".minfo", ".copy", ".who"].map((s) => rowOf(page, "acme/gpt-5.5").locator(s).boundingBox()));
    assert(infoBox.x > whoBox.x && infoBox.x + infoBox.width <= copyBox.x + 1, "info between the name and the copy button");

    // the search: by id, by name, by provider, by a group's models
    const find = page.locator("#findModel");
    assert(await find.isVisible(), "six models show the search");
    await find.fill("text");
    assert.deepEqual(await ids(page), ["acme/text-only"]);
    await find.fill("mini");
    assert.deepEqual(await ids(page), ["group/smart", "acme/gpt-5.5-mini"], "the group sending to it too");
    await find.fill("Kimi");
    assert.deepEqual(await ids(page), ["group/smart", "zeta/kimi-k3"], "a group is found by its models");
    await find.fill("zeta cloud");
    assert.deepEqual(await ids(page), ["group/smart", "zeta/glm-5", "zeta/kimi-k3"]);
    await find.fill("nothing-like-it");
    assert.equal(await page.locator("#gwModels .none").textContent(), "No models match “nothing-like-it”");
    await find.press("Escape");
    assert.equal(await find.inputValue(), "");
    assert.equal((await ids(page)).length, 6);

    // a click on the chips picks the model as a click on its row does, and
    // the row stays where it was
    // the line on where gateway keys come from (unshared) puts the row
    // past the window's foot: the reader wheels down to it
    await reach(page, rowOf(page, "acme/gpt-5.5").locator(".minfo"));
    const scroll = async () => (await rowOf(page, "acme/gpt-5.5").boundingBox()).y;
    const was = await scroll();
    await rowOf(page, "acme/gpt-5.5").locator(".minfo").click();
    assert(await rowOf(page, "acme/gpt-5.5").evaluate((r) => r.classList.contains("selected")));
    assert.equal(await scroll(), was);
    assert.deepEqual(errors, []);
  });
}
