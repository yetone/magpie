// Run with Node's test runner and Playwright on the module path; see README.md.
// maxleebeta in #feedback: Pi 1.5.0 connected to magpie, picking one of
// Pi's own OpenCode Go models (opencode-go/<model>, the OPENCODE GO section
// of its model list) turned 「接入」 off at once, with nothing said, while
// magpie's DeepSeek V4.1 Flash kept it on. The own model is asked of
// OpenCode Go by Pi itself, so the pick does take Pi off magpie; now the
// list says so (direct, not via magpie, listed after magpie's), and the
// pick is asked first, Cancel leaving Pi connected, as Claude Code's own
// models are (#726). The options are what /api/state sends since the fix:
// an own model carries direct. Every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const DS = "magpie/deepseek/deepseek-v4.1-flash";
const options = [
  { value: "opencode-go/glm-5.1", note: "GLM-5.1", group: "OpenCode Go", direct: "OpenCode Go" },
  { value: "opencode-go/kimi-k2.6", note: "Kimi K2.6", group: "OpenCode Go", direct: "OpenCode Go" },
  { value: DS, label: "DeepSeek V4.1 Flash", ref: "deepseek/deepseek-v4.1-flash", group: "DeepSeek", note: "DeepSeek · via magpie" },
  { value: "magpie/deepseek/deepseek-v4.1-pro", label: "DeepSeek V4.1 Pro", ref: "deepseek/deepseek-v4.1-pro", group: "DeepSeek", note: "DeepSeek · via magpie" },
];

function server(lang, posts) {
  let cur = { agents: [{ id: "pi", name: "Pi", icon: "generic", path: "/fixture/pi", wired: true, fields: [{ key: "model", label: "model", value: DS, options }] }], profiles: [] };
  const reply = (route) => route.fulfill({ json: { ...cur, settings: { lang, theme: "light" } } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return reply(route);
    if (url.pathname === "/api/set") {
      const body = JSON.parse(req.postData());
      posts.push(body);
      cur = JSON.parse(JSON.stringify(cur));
      const a = cur.agents[0];
      a.fields[0].value = body.value;
      a.wired = body.value.startsWith("magpie/");
      return reply(route);
    }
    if (url.pathname.startsWith("/api/agents/preview/")) return json({ changes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const row = '.row.agent[data-id="pi"]';
const KEYS = ["direct, not via magpie", "via magpie", "Take {agent} off magpie?", "Cancel", "Use {model}",
  "{model} is {agent}'s own model: {agent} asks {vendor} for it itself, with its own sign-in, not through magpie. Picking it takes {agent} off magpie, and it starts on {model}."];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [980, 420]) {
      test(`${engine} ${lang} ${width}: Pi's own OpenCode Go model says it goes direct and is asked before 接入 goes off`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [], dialogs = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { dialogs.push(d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(`${row} .ag-conn`).waitFor();
        const tr = (k, v) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        const missing = await page.evaluate(([keys, lang]) => lang === "en" ? [] : keys.filter((k) => !I18N[lang]?.[k]), [KEYS, lang]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
        const tops = () => page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
        const top = await tops();
        const start = page.locator(`${row} > .field.ag-start`);
        const item = (text) => page.locator("#pop #list li", { hasText: text }).first();
        const open = async () => { await start.click(); await page.locator("#pop:not([hidden]) #list li").first().waitFor(); };
        const on = () => page.locator(`${row} .ag-conn`).getAttribute("aria-checked");

        // each says which way it goes; magpie's are listed first
        await open();
        const tag = (li) => li.locator(".badge.path").textContent();
        assert.equal(await tag(item("GLM-5.1")), await tr("direct, not via magpie"));
        assert.equal(await tag(item("DeepSeek V4.1 Pro")), await tr("via magpie"));
        const order = await page.locator("#pop #list li").allTextContents();
        const at = (s) => order.findIndex((x) => x.includes(s));
        assert.ok(at("DeepSeek V4.1 Pro") >= 0 && at("DeepSeek V4.1 Pro") < at("GLM-5.1"), `magpie's first: ${JSON.stringify(order)}`);

        // the OpenCode Go model is asked first; Cancel leaves Pi connected
        await item("GLM-5.1").click();
        const ask = page.locator("#modal .leave-ask");
        await ask.waitFor();
        assert.equal((await ask.locator(".ehead b").textContent()).trim(), await tr("Take {agent} off magpie?", { agent: "Pi" }));
        assert.ok((await ask.textContent()).includes("OpenCode Go"), "says who Pi asks instead");
        assert.deepEqual(posts, [], "nothing written before it is confirmed");
        const box = await ask.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `the ask fits at ${width}px`);
        await ask.locator("button", { hasText: await tr("Cancel") }).click();
        await ask.waitFor({ state: "detached" });
        assert.deepEqual(posts, []);
        assert.equal(await on(), "true");

        // magpie's other model isn't asked, and Pi stays on
        await open();
        await item("DeepSeek V4.1 Pro").click();
        await page.waitForFunction((r) => document.querySelector(r + " > .field.ag-start")?.textContent.includes("Pro"), row);
        assert.equal(await page.locator("#modal:not([hidden]) .leave-ask").count(), 0);
        assert.deepEqual(posts, [{ agent: "pi", field: "model", value: "magpie/deepseek/deepseek-v4.1-pro" }]);
        assert.equal(await on(), "true");

        // confirmed, it is written, and 「接入」 goes off as it said
        await open();
        await item("GLM-5.1").click();
        await ask.waitFor();
        await ask.locator("button", { hasText: await tr("Use {model}", { model: "opencode-go/glm-5.1" }) }).click();
        await page.waitForFunction(() => document.querySelector("#modal").hidden);
        await page.waitForFunction((r) => document.querySelector(r + " .ag-conn")?.getAttribute("aria-checked") === "false", row);
        assert.deepEqual(posts.at(-1), { agent: "pi", field: "model", value: "opencode-go/glm-5.1" });

        assert.equal(await page.locator("select").count(), 0, "no native select");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0, `no sideways scroll at ${width}px`);
        assert.deepEqual(await tops(), top, "no click moved the page");
        assert.deepEqual(dialogs, [], "no native dialog");
        assert.deepEqual(errors, []);
      });
    }
  }
}
