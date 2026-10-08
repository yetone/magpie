// Run with Node's test runner and Playwright on the module path; see README.md.
// A call's JSON body in the Gateway page's recent calls is a tree: its
// objects and arrays fold, its parts are coloured, a line's value copies from
// the button its hover shows and a key copies on a click, its text still the
// JSON as printed; what is folded stays folded as new calls come in, and no
// click moves the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

// the shape of a Codex call's bodies (#1033's screenshot)
const request = {
  client_metadata: {
    turn_id: "01a11181-1f12-7af1-b5d2-9a7099413ddd",
    guardian_credits_requested: "true",
    "x-codex-turn-metadata": "{\"installation_id\":\"519287e5\",\"request_kind\":\"turn\"}",
  },
  model: "gpt-6-luna",
  stream: true,
  max_output_tokens: 32000,
  store: false,
  include: [],
  tools: [{ type: "function", name: "shell", strict: false, parameters: { type: "object", properties: { cmd: { type: "string" } } } }],
  previous_response_id: null,
};
const response = {
  id: "resp_06bc9d430c97ada1016ac4fe3ef55487d0aa948a5118e0509f",
  model: "gpt-6-luna",
  status: "completed",
  output: [{ type: "message", text: "{\"exclude\":[]}" }],
  usage: { input_tokens: 4453, cached_tokens: 0, output_tokens: 0 },
};
const stream = [
  ["response.created", { type: "response.created", response: { id: "resp_1", model: "gpt-6-luna", status: "in_progress" } }],
  ["response.output_text.delta", { type: "response.output_text.delta", item_id: "m_1", delta: "Hi" }],
].map(([n, d]) => `event: ${n}\ndata: ${JSON.stringify(d)}\n\n`).join("");

const call = (i, model, requestBody, responseBody) => ({
  time: new Date(now - (i + 1) * 60e3).toISOString(), agent: "codex", model, from: "responses", to: "responses", status: 200, ms: 1800, ttft: 1600,
  requestBody, responseBody,
});
const calls = [
  call(0, "codex/gpt-6-luna", JSON.stringify(request), JSON.stringify(response)),
  call(1, "codex/stream", JSON.stringify({ model: "gpt-6-luna", stream: true }), stream),
  call(2, "codex/cut", "{\"model\":\"gpt-6-luna\",\"input\":[{\"role\":\"user\"", "not json"),
];
const providers = {
  providers: [{ id: "codex", name: "Codex", icon: "generic", models: [{ id: "gpt-6-luna", name: "gpt-6-luna", on: true }], agents: [] }],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls, groups: [] },
};

function serve(lang, copied) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/copy") { copied.push(JSON.parse(route.request().postData()).text); return json({}); }
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { items: "4 items", one: "1 item", copyValue: "Copy value", copyKey: "Click to copy the key", fold: "Fold", unfold: "Unfold", events: "Events", reply: "Reply" },
  zh: { items: "4 项", one: "1 项", copyValue: "复制值", copyKey: "点击复制键名", fold: "收起", unfold: "展开", events: "事件", reply: "回复" },
  ja: { items: "4 件", one: "1 件", copyValue: "値をコピー", copyKey: "クリックでキーをコピー", fold: "折りたたむ", unfold: "展開", events: "イベント", reply: "返信" },
  de: { items: "4 Einträge", one: "1 Eintrag", copyValue: "Wert kopieren", copyKey: "Klicken, um den Schlüssel zu kopieren", fold: "Einklappen", unfold: "Aufklappen", events: "Ereignisse", reply: "Antwort" },
};
const view = "#view-gateway";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    for (const width of [1000, 440]) {
      test(`${engine} ${lang} ${width}px: a JSON body folds, is coloured and copies a key or a value`, async (t) => {
        assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
        const w = words[lang];
        const copied = [];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, copied));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-json-tree.png`) });
          }
          await browser.close();
        });

        await page.goto("http://magpie.test/?view=gateway");
        await page.locator("#foldConnect").click(); // Connect away, the calls in view
        const rows = page.locator("#activity .call-item");
        await rows.first().waitFor();
        // what is clicked is brought into view by the wheel, as the reader
        // would, and clicked where it is (the app puts back any other scroll)
        const reach = async (loc) => {
          const [dy, x, y] = await loc.evaluate((e) => {
            const v = document.querySelector("#view-gateway").getBoundingClientRect();
            return [e.getBoundingClientRect().top - v.top - v.height / 2, v.left + 6, v.top + v.height / 2];
          });
          await page.mouse.move(x, y);
          await page.mouse.wheel(0, dy);
          let b = await loc.boundingBox();
          for (let i = 0; i < 20; i++) {
            await page.waitForTimeout(120);
            const n = await loc.boundingBox();
            if (n.y === b.y) break;
            b = n;
          }
          return b;
        };
        const press = async (loc) => {
          const b = await reach(loc);
          const at = await page.locator(view).evaluate((v) => v.scrollTop);
          await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
          await page.waitForTimeout(150);
          assert.equal(await page.locator(view).evaluate((v) => v.scrollTop), at, "a click leaves the page where it is");
        };
        const req = rows.nth(0).locator(".call-body").nth(0);
        const res = rows.nth(0).locator(".call-body").nth(1);
        // what was copied, once the copy's request is in
        const got = async (n) => {
          for (let i = 0; i < 40 && copied.length < n; i++) await page.waitForTimeout(50);
          return copied;
        };
        const text = async (body) => (await body.locator("pre").innerText()).replace(/\n$/, "");

        await press(rows.nth(0).locator(".call"));
        // the text is the JSON as printed, so a selection copies as JSON
        assert.equal(await text(req), JSON.stringify(request, null, 2));
        assert.equal(await text(res), JSON.stringify(response, null, 2));

        // coloured: keys, strings, numbers, and true, false and null
        const tree = req.locator("code.jt");
        assert.equal(await tree.locator(".jt-key").first().innerText(), "\"client_metadata\"");
        assert.equal(await tree.locator(".tk-s").first().innerText(), "\"01a11181-1f12-7af1-b5d2-9a7099413ddd\"");
        assert.equal(await tree.locator(".tk-n").first().innerText(), "32000");
        assert.deepEqual(await tree.locator(".tk-k").allInnerTexts(), ["true", "false", "false", "null"]);
        const colours = await tree.evaluate((c) => ["jt-key", "tk-s", "tk-n", "tk-k"].map((k) => getComputedStyle(c.querySelector("." + k)).color));
        assert.equal(new Set(colours).size, 4, "each part has its own colour");
        assert.notEqual(colours[0], await tree.evaluate((c) => getComputedStyle(c).color), "a key isn't the text's colour");

        // the fold arrow sits in the indent, taking no width: the key starts
        // where the JSON as printed has it
        const meta = tree.locator(".jt-head").nth(1);
        const fold = meta.locator(".jt-fold");
        assert.equal(await fold.getAttribute("title"), w.fold);
        assert.equal(await fold.evaluate((b) => b.getBoundingClientRect().width), 0);
        const arrow = await fold.locator("svg").boundingBox();
        const key = await meta.locator(".jt-key").boundingBox();
        assert(arrow.x + arrow.width <= key.x + 1, "the arrow is before the key, not on it");
        assert(arrow.x >= (await req.locator("pre").boundingBox()).x, "and inside the box");

        // a fold closes the object to {…} and says how many it holds
        await press(fold.locator("svg"));
        assert.equal(await fold.getAttribute("aria-expanded"), "false");
        assert.equal(await fold.getAttribute("title"), w.unfold);
        const folded = { ...request, client_metadata: "§" };
        assert.equal(await text(req), JSON.stringify(folded, null, 2).replace("\"§\"", "{…}"));
        const sum = tree.locator(".jt-sum:not([hidden])").first();
        assert.equal(await sum.evaluate((s) => getComputedStyle(s, "::after").content.replace(/"/g, "").trim()), w.items.replace("4", "3"));

        // and stays so as a new call comes in and the list is drawn again
        await page.evaluate(() => renderActivity());
        assert.equal(await text(req), JSON.stringify(folded, null, 2).replace("\"§\"", "{…}"));
        assert.equal(await text(res), JSON.stringify(response, null, 2), "the other body is untouched");
        // a click on the … opens it again
        await press(tree.locator(".jt-sum:not([hidden])").first());
        assert.equal(await text(req), JSON.stringify(request, null, 2));

        // a line's copy button shows on its hover, after the value, and
        // copies the value: a string's text, not its quotes; an object as JSON
        const line = tree.locator(".jt-head").filter({ hasText: "x-codex-turn-metadata" });
        const copyB = line.locator(".jt-copy");
        assert.equal(await copyB.evaluate((b) => getComputedStyle(b).opacity), "0");
        await reach(line);
        await line.locator(".jt-key").hover(); // the value wraps: its box's middle can be a gap
        // the app's every transition takes a frame, even with motion reduced
        await page.waitForFunction((b) => getComputedStyle(b).opacity === "1", await copyB.elementHandle());
        assert.equal(await copyB.getAttribute("title"), w.copyValue);
        const v = await line.locator(".tk-s").boundingBox();
        const cb = await copyB.locator("svg").boundingBox();
        assert(cb.x >= v.x + v.width - 1 || cb.y > v.y, "the button is after the value, not over it");
        // the pointer crosses from the value to the button as a hand would
        await page.mouse.move(v.x + v.width - 2, v.y + v.height - 4);
        await page.mouse.move(cb.x + cb.width / 2, cb.y + cb.height / 2, { steps: 6 });
        await page.mouse.down();
        await page.mouse.up();
        assert.deepEqual(await got(1), [request.client_metadata["x-codex-turn-metadata"]]);
        assert.equal(await text(req), JSON.stringify(request, null, 2), "the tick it shows adds no text");

        const tools = tree.locator(".jt-head").filter({ has: page.locator(".jt-key", { hasText: /^"tools"$/ }) });
        await reach(tools);
        await tools.locator(".jt-key").hover();
        const tb = await tools.locator(".jt-copy svg").boundingBox();
        await page.mouse.click(tb.x + tb.width / 2, tb.y + tb.height / 2);
        assert.deepEqual(JSON.parse((await got(2))[1]), request.tools);
        assert.equal(copied[1], JSON.stringify(request.tools, null, 2));

        // a click on a key copies the key
        const k = tree.locator(".jt-key", { hasText: /^"max_output_tokens"$/ });
        assert.equal(await k.getAttribute("title"), w.copyKey);
        await press(k);
        assert.equal((await got(3))[2], "max_output_tokens");

        // an empty array has nothing to fold; a cut body is shown as it came
        assert.equal(await tree.locator(".jt-head", { has: page.locator(".jt-key", { hasText: /^"include"$/ }) }).locator(".jt-fold").count(), 0);
        await press(rows.nth(2).locator(".call"));
        assert.equal(await rows.nth(2).locator("code.jt").count(), 0);
        assert.equal(await rows.nth(2).locator(".call-body").nth(1).locator("pre").innerText(), "not json");

        // a stream: each event's data, and the reply, fold the same way
        await press(rows.nth(1).locator(".call"));
        const sse = rows.nth(1).locator(".call-body").nth(1);
        await press(sse.locator(".segs .opt", { hasText: w.events }));
        const ev = sse.locator(".sse-ev").first();
        assert.match(await ev.innerText(), /^event: response\.created\n\{\n  "type": "response\.created",\n  "response": \{/);
        await press(ev.locator(".jt-fold svg").nth(1));
        assert.equal(await ev.innerText(), "event: response.created\n{\n  \"type\": \"response.created\",\n  \"response\": {…}\n}");
        assert.equal(await ev.locator(".jt-sum:not([hidden])").evaluate((s) => getComputedStyle(s, "::after").content.replace(/"/g, "").trim()), w.items.replace("4", "3"));
        await press(sse.locator(".segs .opt", { hasText: w.reply }));
        assert.deepEqual(JSON.parse(await sse.locator("pre").innerText()), { id: "resp_1", model: "gpt-6-luna", status: "in_progress", output: [{ type: "message", text: "Hi" }] });
        const out = sse.locator(".jt-head").filter({ has: page.locator(".jt-key", { hasText: /^"output"$/ }) });
        await press(out.locator(".jt-fold svg"));
        assert.equal(await sse.locator(".jt-sum:not([hidden])").first().evaluate((s) => getComputedStyle(s, "::after").content.replace(/"/g, "").trim()), w.one);
        await press(sse.locator(".segs .opt", { hasText: w.events }));
        assert.equal(await sse.locator(".sse-ev").first().innerText(), "event: response.created\n{\n  \"type\": \"response.created\",\n  \"response\": {…}\n}", "the event's fold is kept across a switch");

        // nothing scrolls sideways
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        assert.equal(await req.locator("pre").evaluate((p) => p.scrollWidth <= p.clientWidth), true);
        assert.deepEqual(errors, []);
      });
    }
  }
}
