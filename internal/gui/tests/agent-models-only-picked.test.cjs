// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's model list, switched to "only models I pick" (nianlee-official,
// #1337): the switch in its foot is the agent's own; on, what is ticked is
// what is sent (not what is hidden), so a model that comes later — of a new
// provider or an old one — is off until it is ticked; off again, new models
// show. The switch is the list's foot, not the order's.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

// the server as magpie's is (internal/gui/agent_models.go): shown only its
// picks, a model it hasn't seen is off; the one in use always stays
function fixture(lang) {
  const list = [
    ...["gpt-5.5", "gpt-5.4", "o4-mini"].map((m, i) => ({ id: "openai/" + m, name: m.toUpperCase(), group: "OpenAI", icon: "openai", inUse: i === 0 })),
    ...["m1", "m2"].map((m) => ({ id: "relay/" + m, name: "Relay " + m, group: "Relay", icon: "generic" })),
  ];
  let only = false;
  const posts = [];
  const count = () => ({ shown: list.filter((m) => !m.hidden).length, listed: list.length });
  const state = () => ({
    agents: [{
      id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: true,
      fields: [{ key: "model", label: "model", value: "magpie/openai/gpt-5.5", options: [{ value: "magpie/openai/gpt-5.5", label: "GPT-5.5", ref: "openai/gpt-5.5" }] }],
      models: count(),
    }],
    profiles: [], settings: { lang, theme: "light" },
  });
  async function serve(route) {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/agent-models/codex") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        if (body.only !== undefined) only = body.only;
        else if (only) {
          if (!body.shown) return route.fulfill({ status: 500, json: { error: "picks sent as hidden" } });
          for (const m of list) m.hidden = !body.shown.includes(m.id) && !m.inUse;
        } else for (const m of list) m.hidden = (body.hidden || []).includes(m.id) && !m.inUse;
        return json({ models: list, count: count(), only });
      }
      return json({ models: list, ordered: false, only });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  }
  // a model that comes later: a new provider's, shown only its picks, is off
  const arrive = (m) => list.push({ ...m, hidden: only });
  return { serve, posts, arrive, only: () => only };
}

const W = {
  en: { only: "Only models I pick", order: "Order", all: "All", showAll: "Show all" },
  zh: { only: "只显示我勾选的模型", order: "排序", all: "全部", showAll: "全部显示" },
  // the longest words, beside the longest buttons, at the narrowest
  de: { only: "Nur meine Auswahl", order: "Reihenfolge", all: "Alle", showAll: "Alle anzeigen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "de"]) {
    for (const width of [420, 1100]) {
      test(`${engine} ${lang} ${width}: only the models picked for an agent`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 760 } });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const fx = fixture(lang);
        await page.route("**/*", fx.serve);
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-only-picked.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/");
        const row = page.locator('.row.agent[data-id="codex"]');
        const pop = page.locator(".am-pop:not(.leaving)");
        const open = async () => {
          const entry = row.locator(".ag-exp .ag-chips .ag-quiet");
          // the row stays open once opened
          if (!(await entry.isVisible())) await row.locator(".ag-link").click();
          await entry.waitFor();
          await page.waitForFunction(() => document.getAnimations().length === 0);
          await entry.click();
          await pop.waitFor();
        };
        const close = async () => {
          await page.keyboard.press("Escape");
          await pop.waitFor({ state: "detached" });
          // the state is read again once the writes are in
          await page.waitForTimeout(200);
        };
        const sw = pop.locator(".am-only .lib-switch");
        const ticked = async (name) => (await pop.locator(".am-mr", { hasText: name }).first().getAttribute("aria-checked")) === "true";
        const settle = () => page.waitForTimeout(150);

        await open();
        // the foot: the switch and its words, off, on the screen whole beside
        // Hide all · Show all
        const only = pop.locator(".am-only");
        assert.equal(await only.innerText(), w.only);
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        assert.equal(await sw.getAttribute("role"), "switch");
        const pb = await pop.boundingBox(), ob = await only.boundingBox(), rb = await pop.locator(".am-foot .am-hide").boundingBox();
        assert(ob.x >= pb.x && ob.x + ob.width <= rb.x, `switch ${JSON.stringify(ob)} clear of Hide all ${JSON.stringify(rb)}`);
        assert(rb.x + rb.width <= pb.x + pb.width, "Hide all inside the box");
        assert(pb.x >= 0 && pb.x + pb.width <= width, JSON.stringify(pb));

        // with new models shown, taking one out sends what is hidden
        await pop.locator(".am-mr", { hasText: "Relay m2" }).click();
        await settle();
        assert.deepEqual(fx.posts.at(-1), { hidden: ["relay/m2"] });

        // switched on, by its words too: the models shown stay as they are
        await only.locator("span").click();
        await settle();
        assert.deepEqual(fx.posts.at(-1), { only: true });
        assert.equal(await sw.getAttribute("aria-checked"), "true");
        assert(!(await ticked("Relay m2")) && (await ticked("Relay m1")));

        // a click now sends what is ticked: o4-mini out, the rest as they were
        await pop.locator(".am-mr", { hasText: "O4-MINI" }).click();
        await settle();
        assert.deepEqual(fx.posts.at(-1), { shown: ["openai/gpt-5.5", "openai/gpt-5.4", "relay/m1"] });
        await close();

        // a model of a new provider and a new one of an old provider come
        fx.arrive({ id: "fresh/x1", name: "Fresh X1", group: "Fresh", icon: "generic" });
        fx.arrive({ id: "openai/gpt-6", name: "GPT-6", group: "OpenAI", icon: "openai" });
        await open();
        assert.equal(await sw.getAttribute("aria-checked"), "true", "the switch is the agent's, kept");
        assert(!(await ticked("Fresh X1")) && !(await ticked("GPT-6")), "what comes later is off");
        // ticking one shows it, the other new one still off
        await pop.locator(".am-mr", { hasText: "GPT-6" }).click();
        await settle();
        assert.deepEqual(fx.posts.at(-1).shown.toSorted(), ["openai/gpt-5.4", "openai/gpt-5.5", "openai/gpt-6", "relay/m1"]);

        // Show all, twice, ticks every one, and sends them as picks
        const showAll = pop.locator(".am-reset:not(.am-hide)");
        await showAll.click();
        assert(await only.isHidden(), "the armed button has the room");
        await showAll.click();
        await settle();
        assert(await only.isVisible());
        assert.equal(fx.posts.at(-1).shown.length, 7);

        // the order's foot is the order's; back on the list, the switch is
        await pop.locator(".am-seg button", { hasText: w.order }).click();
        assert.equal(await only.count(), 0);
        await pop.locator(".am-seg button", { hasText: w.all }).click();
        assert.equal(await only.count(), 1);
        assert.equal(await sw.getAttribute("aria-checked"), "true");

        // off again: new models show
        await sw.click();
        await settle();
        assert.deepEqual(fx.posts.at(-1), { only: false });
        assert.equal(fx.only(), false);
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        await pop.locator(".am-mr", { hasText: "Fresh X1" }).click();
        await settle();
        assert.deepEqual(fx.posts.at(-1), { hidden: ["fresh/x1"] });

        // nothing turned away, nothing thrown
        assert.equal(await page.locator(".status.err, #status.err").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
