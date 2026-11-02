// Real assets, isolated API fixtures; no Reasonix process or user configuration.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [
  { value: "magpie/b/executor", label: "B Executor", ref: "b/executor", group: "B", icon: "deepseek-color" },
  { value: "magpie/b/planner", label: "B Planner", ref: "b/planner", group: "B", icon: "deepseek-color" },
  { value: "magpie/a/pro", label: "A Pro", ref: "a/pro", group: "A", icon: "deepseek-color" },
  { value: "magpie/a/flash", label: "A Flash", ref: "a/flash", group: "A", icon: "deepseek-color" },
  { value: "native/old", label: "Native Old", group: "Reasonix Studio" },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const mode of ["window", "panel"]) {
      test(`${engine} ${lang} ${mode}: Reasonix roles keep labels and model controls`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await browser.newPage({ viewport: { width: mode === "panel" ? 440 : 1000, height: mode === "panel" ? 560 : 700 } });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const fields = [
          { key: "model", label: "executor", value: "", options },
          { key: "planner", label: "planner", value: "", options: [{ value: "off", label: "off" }, ...options] },
        ];
        // The role labels are field labels like any other: t(label) translates
        // them, and both the picker note and the confirm fill {field} from it.
        const roleText = { executor: "执行模型", planner: "规划模型" };
        const labelText = (l) => (lang === "zh" ? roleText[l] : l);
        const posts = [];
        const state = () => ({ agents: [{ id: "reasonix", name: "Reasonix Studio", icon: "reasonix-color", path: "/fixture/config.toml", wired: fields.some((f) => f.value.startsWith("magpie/")), fields }], profiles: [], settings: { lang, theme: "light", guideHidden: true } });
        await page.addInitScript(() => localStorage.setItem("magpie.modelFavorites", '["a/pro"]'));
        await page.route("**/*", async (route) => {
          const url = new URL(route.request().url());
          const json = (data) => route.fulfill({ json: data });
          if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
          if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
          if (url.pathname === "/api/state") return json(state());
          if (url.pathname === "/api/set") {
            const body = route.request().postDataJSON();
            posts.push(body);
            fields.find((f) => f.key === body.field).value = body.value || "native/old";
            return json(state());
          }
          if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
          if (url.pathname === "/api/groups") return json({ groups: [] });
          if (url.pathname === "/api/usage/quotas") return json([]);
          if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
          if (url.pathname.startsWith("/api/")) return json({});
          const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
          const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
          return route.fulfill({ body: await fs.readFile(file), contentType });
        });
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${mode}-reasonix-roles.png`) });
          }
          await browser.close();
        });
        const row = page.locator('.row.agent[data-id="reasonix"]');
        const open = async () => {
          await row.waitFor();
          if (mode === "panel") await row.locator(".ag-sum").click();
          else if (await row.locator(':scope > .ag-link[aria-expanded="false"]').count()) {
            await row.locator(":scope > .ag-link").click();
            await row.locator(".ag-exp").waitFor();
          }
        };
        await page.goto("http://magpie.test/" + (mode === "panel" ? "?mode=panel" : ""));
        await open();
        if (mode === "window") {
          // Not connected, the row says what the agent is on, its Default or
          // its own model, and lists its own models with magpie's as when
          // connected; it does not read "Pick a model" (EZN7L2C3, #834).
          // The Executor is the model field here and is named "executor".
          const def = lang === "zh" ? "默认" : "default";
          assert.equal((await row.locator(".ag-start").textContent()).trim(), `${labelText(fields[0].label)}${def}`);
        } else for (const f of fields) {
          assert.equal(await row.locator(`.field[data-key="${f.key}"] > .k`).count(), 1, "a default role must be labelled once");
        }
        fields[0].value = "magpie/b/executor";
        fields[1].value = "magpie/b/planner";
        // Either role keeps the connection while the other is on its default.
        for (const f of fields) {
          const selected = f.value;
          f.value = "";
          await page.reload();
          await open();
          assert.equal(await row.locator(`.field[data-key="${f.key}"] > .k`).count(), 1, "a connected default role must be labelled once");
          f.value = selected;
        }
        await page.reload();
        await open();
        for (const f of fields) {
          const field = row.locator(`.field[data-key="${f.key}"]`);
          assert.equal(await field.locator(":scope > .k").count(), 1, "a selected role must be labelled once");
          await field.click();
          const pop = page.locator("#pop:not([hidden])");
          assert(await pop.evaluate((e) => e.classList.contains("model-picker")), "role lost its model picker");
          assert(await page.locator("#pickerRail").isVisible());
          await page.locator('#pickerRail [data-group="favorites"]').click();
          await page.waitForFunction(() => document.querySelectorAll("#list li[data-i]").length === 1);
          assert.equal(await page.locator("#list li[data-i]").count(), 1);
          assert.match(await page.locator("#list").innerText(), /A Pro/);
          await page.locator('#pickerRail [data-group="A"]').click();
          await page.waitForFunction(() => {
            const text = document.querySelector("#list").innerText;
            return text.includes("A Pro") && text.includes("A Flash") && !text.includes("Native Old");
          });
          const listed = await page.locator("#list").innerText();
          assert.match(listed, /A Pro/);
          assert.match(listed, /A Flash/);
          assert(!listed.includes("Native Old"));
          await page.locator('#pickerRail [data-group="all"]').click();
          await page.locator("#q").fill("magpie/a/typed");
          assert.equal(await page.locator("#list li.custom").count(), 1, "typed model choice is missing");
          await page.keyboard.press("Escape");
        }
        // Restoring one role does not disconnect the other. The last role
        // asks before leaving, and describes restoration rather than a
        // factory reset. Exercise the actual picker and resulting API calls.
        const defaults = async (key) => {
          await row.locator(`.field[data-key="${key}"]`).click();
          const label = fields.find((f) => f.key === key).label;
          assert((await page.locator("#pop").innerText()).includes(lang === "zh" ? `还原原来的 ${labelText(label)} 选择` : `restore the previous ${label} selection`));
          await page.locator("#pop").getByText(lang === "zh" ? "默认" : "Default", { exact: true }).first().click();
        };
        const first = mode === "panel" ? fields[1] : fields[0];
        const last = fields.find((f) => f !== first);
        const remaining = last.value;
        await defaults(first.key);
        await page.waitForFunction((key) => document.querySelector(`.agent[data-id="reasonix"] .field[data-key="${key}"]`)?.textContent.includes("Native Old"), first.key);
        assert.deepEqual(posts, [{ agent: "reasonix", field: first.key, value: "" }]);
        assert.equal(await page.locator(".leave-ask").count(), 0, "the other role still uses magpie");
        assert.equal(last.value, remaining);
        await defaults(last.key);
        const ask = page.locator(".leave-ask");
        await ask.waitFor();
        assert((await ask.innerText()).includes(lang === "zh" ? `还原 Reasonix Studio 原来的 ${labelText(last.label)} 选择` : `Restores Reasonix Studio's previous ${last.label} selection`));
        assert.equal(posts.length, 1, "the last role waits for confirmation");
        await Promise.all([
          page.waitForResponse((r) => new URL(r.url()).pathname === "/api/set"),
          ask.locator(".bar button.primary").click(),
        ]);
        assert.deepEqual(posts[1], { agent: "reasonix", field: last.key, value: "" });
        assert.deepEqual(errors, []);
      });
    }
  }
}
