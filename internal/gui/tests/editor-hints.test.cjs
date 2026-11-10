// Run with Node's test runner and Playwright on the module path; see README.md.
// yujezhang on X (11-10): "界面只保留完成操作必需的信息", longer
// explanations shown on asking. A provider's editor and Routing groups'
// settings do as Settings has since 11-02 (settings-hints.test.cjs). With a
// mouse, in both engines, at 1100px and 420px, in every language:
// - at rest a field's explanation (Headers') is out of sight but still in
//   the page, read by a screen reader as the field's description;
// - the pointer resting on the field's name shows it whole in a card, and
//   no field moves; the pointer leaving takes the card away;
// - a key reaching a field's control shows its card;
// - Certificate keeps its risk in sight, in one short line;
// - Routing groups' settings rows are their names and controls, each
//   explanation shown in the card on resting over its row.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const vllm = {
  id: "vllm", name: "LAN vLLM", icon: "generic", chat: "https://192.168.1.20:8443/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3-32b", name: "qwen3-32b", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false,
  takesInsecureTLS: true, insecureTLS: false,
};
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
];

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json({ providers: [vllm], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({
      models, pools: [], deciders: [], found: true, defaults: { sink: false, firstToken: 0, patience: 0 },
      groups: [{ id: "sol", name: "Sol", members: models.map((m) => m.id), routing: "order", ready: true, memberInfo: models.map((m) => ({ id: m.id, ready: true })) }],
    });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? r.fulfill({ body, contentType }) : r.fulfill({ status: 404, body: "" }));
  };
}

const inSight = (e) => { const b = e.getBoundingClientRect(); return b.height > 4 && b.width > 4; };
const fields = () => [...document.querySelectorAll(".editor.roomy > *, .editor.roomy .more .inner > *")].map((e) => { const b = e.getBoundingClientRect(); return [Math.round(b.top), Math.round(b.height)].join(","); });
const strings = [
  "Headers", "Certificate", "Turn it on only on a network you trust.",
  "For your own server with a self-signed certificate or one from your company's CA. magpie then can't tell it from another machine answering at its address: turn it on only on a network you trust.",
  "Find groups on their own", "Every member busy",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
    const open = async (t, at) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      let page;
      t.after(async () => {
        if (process.env.ARTIFACT_DIR && page) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-editor-hints.png`), fullPage: true });
        }
        await browser.close();
      });
      page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=" + at);
      const w = await page.evaluate(([lang, ks]) => Object.fromEntries(ks.map((k) => [k, (I18N[lang] || {})[k] || k])), [lang, strings]);
      if (lang !== "en") {
        const missing = await page.evaluate(([lang, ks]) => ks.filter((k) => !(I18N[lang] || {})[k]), [lang, strings.slice(0, 4)]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      return { page, errors, w };
    };

    test(`${engine} ${lang} ${width}: a provider editor's explanations show on asking`, async (t) => {
      const { page, errors, w } = await open(t, "providers");
      await page.locator(".row.provider", { hasText: "LAN vLLM" }).click();
      await page.locator(".editor.roomy .bar").waitFor();
      const ed = page.locator(".editor.roomy");
      // Headers and Certificate are under Advanced
      const adv = ed.locator("details.more.advanced");
      if (!(await adv.evaluate((e) => e.open))) await adv.locator(":scope > summary").click();
      const name = ed.locator("label", { hasText: new RegExp("^" + w.Headers.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$") });
      const fld = name.locator("xpath=following-sibling::div[1]");
      const hint = fld.locator(":scope > .hint.expl");
      await hint.waitFor({ state: "attached" });
      const text = (await hint.textContent()).trim();
      assert.ok(text.length > 40, "Headers has its explanation");

      // at rest: out of sight, still in the page and the field's description
      assert.equal(await hint.evaluate(inSight), false, "Headers' explanation shows at rest");
      // and placed in its field's box: against the page, deep in the
      // editor's scroll, it grew the page a scrollbar and the editor
      // 10px narrower (account-weight's de 390, in WebKit)
      assert.deepEqual(await ed.evaluate((ed) => [...ed.querySelectorAll(".fld > .hint.expl")].filter((h) => h.getClientRects().length && getComputedStyle(h).position === "absolute" && h.offsetParent !== h.parentElement).map((h) => h.id)), [], "explanations placed outside their field");
      const id = await hint.getAttribute("id");
      assert.ok(await fld.locator(`[aria-describedby~="${id}"]`).count() > 0 || await page.locator(`[aria-describedby~="${id}"]`).count() > 0, "nothing is described by the explanation");

      // Certificate: the risk in one short line in sight, the rest on asking
      const cert = ed.locator("label", { hasText: new RegExp("^" + w.Certificate + "$") }).locator("xpath=following-sibling::div[1]");
      const lead = cert.locator(":scope > .hint.lead");
      await cert.scrollIntoViewIfNeeded();
      assert.equal(await lead.evaluate(inSight), true, "Certificate's risk is out of sight");
      assert.equal((await lead.textContent()).trim(), w["Turn it on only on a network you trust."]);
      const lb = await lead.boundingBox();
      assert.ok(lb.height < 40, `the risk's line runs long: ${lb.height}`);
      assert.equal(await cert.locator(":scope > .hint.expl").evaluate(inSight), false);

      // the pointer resting on the field's name: the card, whole, and
      // nothing moves
      await name.scrollIntoViewIfNeeded();
      await page.mouse.move(1, 1);
      const before = await page.evaluate(fields);
      await name.hover();
      const card = page.locator(".set-hint.on");
      await card.waitFor();
      assert.equal((await card.textContent()).includes(text), true, "the card doesn't say the field's line");
      const cb = await card.boundingBox(), nb = await name.boundingBox();
      assert.ok(cb.x >= 15 && cb.x + cb.width <= width - 15, `the card runs out of the window: ${JSON.stringify(cb)}`);
      assert.ok(cb.y >= nb.y + nb.height - 1 || cb.y + cb.height <= nb.y + 1, "the card covers the field's name");
      assert.deepEqual(await page.evaluate(fields), before, "a field moved when the card showed");
      assert.equal(await card.evaluate((e) => getComputedStyle(e).pointerEvents), "none");
      // the pointer gone: the card goes
      await page.mouse.move(1, 1);
      await page.locator(".set-hint").waitFor({ state: "hidden" });

      // a key reaching Certificate's box shows its card
      const box = cert.locator("input").first();
      await box.focus();
      await page.keyboard.press("Shift+Tab");
      await page.keyboard.press("Tab");
      assert.equal(await cert.evaluate((r) => r.contains(document.activeElement)), true, "Tab didn't come back to Certificate's box");
      await card.waitFor();
      assert.equal((await card.textContent()).includes(w[strings[3]]), true, "focus showed another field's card");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang} ${width}: Routing groups' settings are their names, explained on asking`, async (t) => {
      const { page, errors, w } = await open(t, "groups");
      const found = page.locator(".rt-gset > .rt-gfound");
      await found.waitFor();
      const busy = page.locator(".rt-gset > .rt-gdef-patience");
      for (const [row, label] of [[found, w["Find groups on their own"]], [busy, w["Every member busy"]]]) {
        assert.equal((await row.locator(".txt > b").textContent()).trim(), label);
        const line = row.locator(".txt > small.expl");
        const text = (await line.textContent()).trim();
        assert.ok(text.length > 20);
        assert.equal(await line.evaluate(inSight), false, `${label}'s explanation shows at rest`);
        assert.equal(await line.evaluate((h) => h.offsetParent === h.parentElement), true, `${label}'s explanation is placed outside its row`);
        await row.scrollIntoViewIfNeeded();
        await page.mouse.move(1, 1);
        await page.locator(".set-hint").waitFor({ state: "hidden" });
        await row.locator(".txt > b").hover();
        const card = page.locator(".set-hint.on");
        await card.waitFor();
        assert.equal((await card.textContent()).includes(text), true, `${label}'s card doesn't say its line`);
        const cb = await card.boundingBox();
        assert.ok(cb.x >= 15 && cb.x + cb.width <= width - 15, `the card runs out of the window: ${JSON.stringify(cb)}`);
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      assert.deepEqual(errors, []);
    });
  }
}
