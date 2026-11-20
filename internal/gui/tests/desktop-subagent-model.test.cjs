// Run with Node's test runner and Playwright on the module path; see README.md.
// Desktop's gateway-applied subagent model and effort share Codex's controls.
// Unset, its square and picker say the lead picks: a tier, on the model set
// for it in Tiers (@mathfournity on X: "Default" didn't say so).
// Desktop's routing trace has subagent picks without a kind. All APIs and
// assets are intercepted; no backend or external network is used.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { value: "fake/m1", label: "M1", ref: "fake/m1", group: "Fake" },
  { value: "fake/m2", label: "M2", ref: "fake/m2", group: "Fake" },
];
const allLevels = ["none", "minimal", "low", "medium", "high", "xhigh", "max"].map((value) => ({ value }));
const fresh = (wired = true) => ({
  agents: [{
    id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired,
    fields: [
      { key: "provider", label: "provider", value: wired ? "magpie" : "", options: [{ value: "magpie", label: "magpie" }] },
      ...["opus", "sonnet", "haiku", "fable"].map((tier) => ({ key: tier, label: tier, value: "", options: wired ? models : [] })),
      { key: "subagent_model", label: "subagent model", value: "", options: wired ? models : [] },
      { key: "subagent_effort", label: "subagent effort", value: "", options: wired ? allLevels : [] },
    ],
  }],
  profiles: [],
});

const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const seat = { id: "fake", provider: "fake", name: "Fake", who: "Test key", kind: "key", agent: "claude-desktop", model: "m2" };
// Deliberately no kind: Desktop must explain the pick without a Codex tag.
const routes = [
  { id: 3, seq: 3, time: at(0), agent: "claude-desktop", model: "fake/m1", provider: "fake", subagentEffort: "high", subagent: { asked: "fake/m1", to: "fake/m2" },
    order: [seat], tries: [{ id: "fake", model: "m2", start: at(0), done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 1200 },
  { id: 2, seq: 2, time: at(1), agent: "claude-desktop", model: "fake/m1", provider: "fake", subagentEffort: "high", subagent: { asked: "fake/m1", to: "fake/m2", kept: "it is disabled in magpie" },
    order: [{ ...seat, model: "m1" }], tries: [{ id: "fake", model: "m1", start: at(1), done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 1200 },
  { id: 1, seq: 1, time: at(2), agent: "claude-desktop", model: "fake/m1", provider: "fake", subagentEffort: "high",
    order: [{ ...seat, model: "m1" }], tries: [{ id: "fake", model: "m1", start: at(2), effort: "high", done: true, status: 200, ms: 900 }], done: true, status: 200, ms: 900, tokens: 1200 },
];

function server(lang, fixture) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...fixture.state, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      fixture.sets.push(body);
      fixture.state = JSON.parse(JSON.stringify(fixture.state));
      fixture.state.agents.find((a) => a.id === body.agent).fields.find((f) => f.key === body.field).value = body.value;
      if (body.field === "subagent_model") {
        fixture.state.agents[0].fields.find((f) => f.key === "subagent_effort").options =
          body.value ? [{ value: "low" }, { value: "high" }] : allLevels;
      }
      return json({ ...fixture.state, settings: { lang, theme: "light" } });
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// Expected reader-facing words, independent of the translation lookup.
const W = {
  en: { effortDefault: "subagent effort: default\nthe effort Claude Code asks for", effortHigh: "subagent effort: high",
    effortStory: "The subagent effort set in magpie is high; the model's supported levels and a routing group's fixed member effort still apply.",
    unset: "subagent model: Lead's pick\nThe lead names opus, sonnet, haiku or fable for each subagent, which runs on that tier's model in Tiers. A model picked here runs every subagent.", def: "Lead's pick", set: "subagent model: M2\nEvery subagent runs on this model, even when the lead names another. Lead's pick lets the lead choose again.",
    note: "the tier the lead names, on its model in Tiers",
    moved: "Claude Desktop requested a subagent on fake/m1; magpie put it on fake/m2, the model set for its subagents.",
    kept: "Claude Desktop requested a subagent on fake/m1 and it stayed there: its subagents are set to fake/m2, but it is disabled in magpie." },
  zh: { effortDefault: "子代理推理强度：默认\n跟随 Claude Code 请求的推理强度", effortHigh: "子代理推理强度：高",
    effortStory: "magpie 为子代理设定的推理强度是 高；仍按模型支持的等级适配，并保留路由组成员的固定强度。",
    unset: "子代理模型：由主代理自选\n主代理为每个子代理指定 opus、sonnet、haiku 或 fable，子代理就用“档位”里为该档设定的模型。在这里选一个模型，就让所有子代理都用它。", def: "由主代理自选", set: "子代理模型：M2\n所有子代理都用这个模型，主代理指定了别的也一样。选“由主代理自选”就让主代理重新选择。",
    note: "主代理指定的档位，用“档位”里为它设定的模型",
    moved: "Claude Desktop 请求了使用 fake/m1 的子代理；magpie 将其改用 fake/m2，即为它的子代理设定的模型。",
    kept: "Claude Desktop 请求了使用 fake/m1 的子代理，并保留在该模型上：它的子代理设为 fake/m2，但该模型在 magpie 中已停用。" },
  "zh-TW": { effortDefault: "子代理推理強度：預設\n跟隨 Claude Code 請求的推理強度", effortHigh: "子代理推理強度：高",
    effortStory: "magpie 為子代理設定的推理強度是 高；仍按模型支援的等級適配，並保留路由群組成員的固定強度。",
    unset: "子代理模型：由主代理自選\n主代理為每個子代理指定 opus、sonnet、haiku 或 fable，子代理就用「檔位」裡為該檔設定的模型。在這裡選一個模型，就讓所有子代理都用它。", def: "由主代理自選", set: "子代理模型：M2\n所有子代理都用這個模型，主代理指定了別的也一樣。選「由主代理自選」就讓主代理重新選擇。",
    note: "主代理指定的檔位，用「檔位」裡為它設定的模型",
    moved: "Claude Desktop 請求了使用 fake/m1 的子代理；magpie 將其改用 fake/m2，即為它的子代理設定的模型。",
    kept: "Claude Desktop 請求了使用 fake/m1 的子代理，並保留在該模型上：它的子代理設為 fake/m2，但該模型在 magpie 中已停用。" },
  ja: { effortDefault: "サブエージェントの推論レベル：デフォルト\nClaude Code が要求する推論レベル", effortHigh: "サブエージェントの推論レベル：高",
    effortStory: "magpie で設定したサブエージェントの推論レベルは 高 です。モデルが対応するレベルへの調整と、ルーティンググループのメンバーに固定されたレベルは引き続き適用されます。",
    unset: "サブエージェントのモデル：リードが選ぶ\nリードがサブエージェントごとに opus、sonnet、haiku、fable のいずれかを指定し、サブエージェントは「階層」でその階層に設定したモデルで動きます。ここでモデルを選ぶと、すべてのサブエージェントがそのモデルを使います。", def: "リードが選ぶ", set: "サブエージェントのモデル：M2\nリードが別のモデルを指定しても、すべてのサブエージェントがこのモデルで動きます。「リードが選ぶ」を選ぶと、選択をリードに戻します。",
    note: "リードが指定した階層の、「階層」で設定したモデル",
    moved: "Claude Desktop が fake/m1 のサブエージェントをリクエストしました。magpie はサブエージェント用に設定された fake/m2 に切り替えました。",
    kept: "Claude Desktop が fake/m1 のサブエージェントをリクエストし、そのモデルのままになりました。サブエージェントは fake/m2 に設定されていますが、このモデルは magpie で無効になっています。" },
  de: { effortDefault: "Reasoning-Effort der Subagenten: Standard\nder von Claude Code angeforderte Reasoning-Effort", effortHigh: "Reasoning-Effort der Subagenten: hoch",
    effortStory: "Der in magpie gesetzte Reasoning-Effort der Subagenten ist hoch; die unterstützten Stufen des Modells und die feste Stufe eines Mitglieds der Routing-Gruppe gelten weiterhin.",
    unset: "Modell der Subagenten: Wahl des Haupt-Agenten\nDer Haupt-Agent nennt für jeden Subagenten opus, sonnet, haiku oder fable, und der Subagent läuft auf dem Modell, das unter „Stufen“ für diese Stufe gesetzt ist. Wähle hier ein Modell, um es für alle Subagenten zu nutzen.", def: "Wahl des Haupt-Agenten", set: "Modell der Subagenten: M2\nAlle Subagenten laufen auf diesem Modell, auch wenn der Haupt-Agent ein anderes nennt. „Wahl des Haupt-Agenten“ lässt ihn wieder wählen.",
    note: "die Stufe, die der Haupt-Agent nennt, auf ihrem Modell unter „Stufen“",
    moved: "Claude Desktop hat einen Subagent mit fake/m1 angefordert; magpie hat ihn auf fake/m2 gesetzt, das für seine Subagenten festgelegte Modell.",
    kept: "Claude Desktop hat einen Subagent mit fake/m1 angefordert, und er blieb dort: Seine Subagenten sind auf fake/m2 gesetzt, aber es ist in magpie deaktiviert." },
};

const cd = '.row.agent[data-id="claude-desktop"]';
// A pick redraws its square at once and writes behind it, queued: what was
// posted is read once the write's answer is back, not when the square changes.
const written = (page) => page.waitForResponse((r) => new URL(r.url()).pathname === "/api/set");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const langs = process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(W);

for (const engine of engines) {
  for (const lang of langs) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: Desktop's subagent square, translated picker and routing story`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width, height: 760 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], fixture = { state: fresh(), sets: [] };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, fixture));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-desktop-subagent-model.png`) });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/");
        await page.locator(cd).waitFor();
        await page.locator(`${cd} .ag-link`).click();
        await page.locator(`${cd} .ag-exp`).waitFor();

        const square = page.locator(`${cd} .ag-exp .field.extra[data-key="subagent_model"]`);
        assert.equal(await square.count(), 1, "Desktop's opened row has one subagent square");
        assert.equal(await page.locator(`${cd} .field:not(.extra)[data-key="subagent_model"]`).count(), 0, "no plain subagent picker");
        assert.equal(await square.getAttribute("title"), w.unset, "translated unset title");
        assert.equal(await square.getAttribute("aria-label"), w.unset);
        assert.equal(await square.evaluate((e) => e.classList.contains("set")), false);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        const box = await square.boundingBox();
        assert(box && box.x >= 0 && box.x + box.width <= width, JSON.stringify(box));

        const y = await page.evaluate(() => scrollY);
        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.evaluate(() => scrollY), y, "opening the square does not scroll");
        assert.equal(await page.locator("#pop").evaluate((e) => e.classList.contains("model-picker")), true, "app's own model picker");
        assert.equal(await page.locator("select").count(), 0, "no native select");
        const first = await page.locator("#pop #list li").first().evaluate((e) => [e.querySelector(".v")?.textContent, e.querySelector(".n")?.textContent]);
        assert.deepEqual(first, [w.def, w.note], "translated Lead's pick first with Desktop's tier note");
        const items = await page.locator("#pop #list li").allInnerTexts();
        for (const model of models) assert(items.some((s) => s.includes(model.label)), `${model.label} offered by magpie`);
        const pop = await page.locator("#pop").boundingBox();
        assert(pop && pop.x >= 0 && pop.x + pop.width <= width, JSON.stringify(pop));
        const left = await page.locator(`${cd} .field.extra[data-key="subagent_model"], #pop #list li`).evaluateAll((es) => es.flatMap((e) => [e, ...e.querySelectorAll("*")]).filter((e) => {
          const s = getComputedStyle(e);
          return parseFloat(s.borderLeftWidth) > 1 && s.borderLeftWidth !== s.borderRightWidth;
        }).length);
        assert.equal(left, 0, "no coloured left border on square or picker rows");

        let wrote = written(page);
        await page.locator("#pop #list li", { hasText: "M2" }).first().click();
        await wrote;
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="claude-desktop"] .field.extra.set[data-key="subagent_model"]'));
        assert.deepEqual(fixture.sets, [{ agent: "claude-desktop", field: "subagent_model", value: "fake/m2" }], "Desktop pick posts the catalog id");
        assert.equal(await square.getAttribute("title"), w.set);
        assert.equal(await square.getAttribute("aria-label"), w.set);
        assert.equal(await page.evaluate(() => scrollY), y, "picking does not scroll");

        const effort = page.locator(`${cd} .ag-exp .field.extra[data-key="subagent_effort"]`);
        assert.equal(await effort.count(), 1, "one effort square beside the model");
        assert.equal(await effort.getAttribute("title"), w.effortDefault);
        await effort.click();
        await page.locator("#pop.effort-picker:not([hidden])").waitFor();
        assert.equal(await page.locator("#effortTicks i").count(), 3, "Default and the selected model's two levels");
        assert.equal(await page.evaluate(() => scrollY), y, "opening effort does not scroll");
        wrote = written(page);
        await page.locator("#effortRange").evaluate((r) => {
          r.value = "2";
          r.dispatchEvent(new Event("input", { bubbles: true }));
          r.dispatchEvent(new Event("change", { bubbles: true }));
        });
        await wrote;
        await page.waitForFunction(() => document.querySelector('.row.agent[data-id="claude-desktop"] .field.extra.set[data-key="subagent_effort"]'));
        assert.deepEqual(fixture.sets.at(-1), { agent: "claude-desktop", field: "subagent_effort", value: "high" });
        assert.equal(await effort.getAttribute("aria-label"), w.effortHigh);
        wrote = written(page);
        await page.locator("#effortRange").evaluate((r) => {
          r.value = "0";
          r.dispatchEvent(new Event("input", { bubbles: true }));
          r.dispatchEvent(new Event("change", { bubbles: true }));
        });
        await wrote;
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="claude-desktop"] .field.extra.set[data-key="subagent_effort"]'));
        assert.deepEqual(fixture.sets.at(-1), { agent: "claude-desktop", field: "subagent_effort", value: "" });
        assert.equal(await effort.getAttribute("aria-label"), w.effortDefault);
        await page.keyboard.press("Escape");
        assert.equal(await page.evaluate(() => scrollY), y, "effort picks do not scroll");
        assert.equal(await square.getAttribute("title"), w.set, "effort picks preserve the selected model");

        await square.click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        assert.equal(await page.evaluate(() => scrollY), y, "reopening does not scroll");
        wrote = written(page);
        await page.locator("#pop #list li").first().click();
        await wrote;
        await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="claude-desktop"] .field.extra.set[data-key="subagent_model"]'));
        assert.deepEqual(fixture.sets, [
          { agent: "claude-desktop", field: "subagent_model", value: "fake/m2" },
          { agent: "claude-desktop", field: "subagent_effort", value: "high" },
          { agent: "claude-desktop", field: "subagent_effort", value: "" },
          { agent: "claude-desktop", field: "subagent_model", value: "" },
        ], "Default posts an empty value");
        assert.equal(await square.getAttribute("title"), w.unset);
        assert.equal(await page.evaluate(() => scrollY), y, "Default does not scroll");

        fixture.state = fresh(false);
        await page.goto("http://magpie.test/");
        await page.locator(cd).waitFor();
        assert.equal(await page.locator(`${cd} .field[data-key="subagent_model"]`).count(), 0, "unwired Desktop with no options or value has no square");
        assert.equal(await page.locator(`${cd} .field[data-key="subagent_effort"]`).count(), 0, "unwired Desktop has no effort square");

        // A narrow window covers the list with the story, so reopen it afresh.
        for (const [i, said] of [[0, w.moved], [1, w.kept], [2, w.effortStory]]) {
          await page.goto("http://magpie.test/?view=routing");
          await page.locator(".rt-days .rt-day").nth(1).click();
          await page.locator(".rt-req").nth(1).waitFor();
          await page.locator(".rt-req").nth(i).evaluate((e) => e.click());
          await page.waitForFunction((i) => document.querySelectorAll(".rt-req")[i]?.getAttribute("aria-pressed") === "true", i);
          const story = await page.locator(".rt-steps").textContent();
          assert(story.includes(said), `Desktop ${i === 0 ? "moved" : "kept"} story: ${said}\nnot in\n${story}`);
          assert.equal(await page.locator(".rt-steps .aside", { hasText: said }).count(), 1, "one subagent aside without a kind");
          assert.equal(await page.locator(".rt-steps .kind").count(), 0, "Desktop has no Codex purpose tag");
          assert(story.includes(w.effortStory), "the effort pick is explained even without a model replacement");
          if (i === 1 && lang !== "en") assert(!story.includes("it is disabled in magpie"), "kept reason must be translated");
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
