// Run with Node's test runner and Playwright on the module path; see README.md.
// A streamed reply's body in the Gateway page's recent calls reads as JSON:
// its reply put together, each event's data pretty-printed, or the body as it
// came; a long stream draws its events a page at a time, and no click moves
// the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const sse = (events) => events.map(([name, data]) => (name ? `event: ${name}\n` : "") + `data: ${typeof data === "string" ? data : JSON.stringify(data)}\n\n`).join("");

const anthropic = sse([
  ["message_start", { type: "message_start", message: { id: "msg_1", model: "claude-x", usage: { input_tokens: 12, output_tokens: 1 } } }],
  ["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "thinking", thinking: "" } }],
  ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "thinking_delta", thinking: "Let me " } }],
  ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "thinking_delta", thinking: "think." } }],
  ["content_block_start", { type: "content_block_start", index: 1, content_block: { type: "text", text: "" } }],
  ["content_block_delta", { type: "content_block_delta", index: 1, delta: { type: "text_delta", text: "Hello, " } }],
  ["content_block_delta", { type: "content_block_delta", index: 1, delta: { type: "text_delta", text: "world" } }],
  ["content_block_start", { type: "content_block_start", index: 2, content_block: { type: "tool_use", id: "tu_1", name: "Read", input: {} } }],
  ["content_block_delta", { type: "content_block_delta", index: 2, delta: { type: "input_json_delta", partial_json: "{\"path\":" } }],
  ["content_block_delta", { type: "content_block_delta", index: 2, delta: { type: "input_json_delta", partial_json: "\"a.go\"}" } }],
  ["message_delta", { type: "message_delta", delta: { stop_reason: "tool_use" }, usage: { output_tokens: 40 } }],
  ["message_stop", { type: "message_stop" }],
]);
const chat = sse([
  ["", { id: "c1", model: "gpt-x", choices: [{ index: 0, delta: { role: "assistant", content: "" } }] }],
  ["", { id: "c1", model: "gpt-x", choices: [{ index: 0, delta: { reasoning_content: "hmm" } }] }],
  ["", { id: "c1", model: "gpt-x", choices: [{ index: 0, delta: { content: "Hi" } }] }],
  ["", { id: "c1", model: "gpt-x", choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: "call_1", function: { name: "ls", arguments: "{\"d" } }] } }] }],
  ["", { id: "c1", model: "gpt-x", choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: "ir\":\".\"}" } }] }, finish_reason: "tool_calls" }] }],
  ["", "[DONE]"],
]);
const responses = sse([
  ["response.created", { type: "response.created", response: { id: "resp_1", model: "gpt-5", status: "in_progress" } }],
  ["response.output_item.added", { type: "response.output_item.added", output_index: 0, item: { id: "fc_1", type: "function_call", name: "shell", call_id: "call_9" } }],
  ["response.function_call_arguments.delta", { type: "response.function_call_arguments.delta", item_id: "fc_1", delta: "{\"cmd\":" }],
  ["response.function_call_arguments.delta", { type: "response.function_call_arguments.delta", item_id: "fc_1", delta: "\"ls\"}" }],
  ["response.output_item.added", { type: "response.output_item.added", output_index: 1, item: { id: "m_1", type: "message" } }],
  ["response.output_text.delta", { type: "response.output_text.delta", item_id: "m_1", delta: "Done " }],
  ["response.output_text.delta", { type: "response.output_text.delta", item_id: "m_1", delta: "now" }],
]);
const long = sse(Array.from({ length: 450 }, (_, i) => ["", { id: "c2", model: "gpt-x", choices: [{ index: 0, delta: { content: `w${i} ` } }] }]));

const call = (i, model, responseBody, extra = {}) => ({
  time: new Date(now - (i + 1) * 60e3).toISOString(), agent: "fixture", model, from: "openai", to: "openai", status: 200, ms: 900,
  requestBody: JSON.stringify({ model, stream: true }), responseBody, ...extra,
});
const calls = [
  call(0, "fixture/claude", anthropic),
  call(1, "fixture/chat", chat),
  call(2, "fixture/responses", responses),
  call(3, "fixture/long", long),
  call(4, "fixture/plain", JSON.stringify({ id: "x", object: "chat.completion" })),
  ...Array.from({ length: 10 }, (_, i) => call(5 + i, "fixture/model-a", "")),
];
const providers = {
  providers: [{ id: "fixture", name: "Fixture", icon: "generic", models: [{ id: "model-a", name: "Model A", on: true }], agents: [] }],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls, groups: [] },
};

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    // the list leaves the bodies out, and an opened row asks for its own by id (#1521)
    if (url.pathname === "/api/providers") return json({ ...providers, gateway: { ...providers.gateway, calls: providers.gateway.calls.map(({ requestBody, responseBody, ...c }, i) => ({ id: i + 1, ...c })) } });
    if (url.pathname === "/api/gateway/call") return json(providers.gateway.calls[Number(url.searchParams.get("id")) - 1]);
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
  en: { reply: "Reply", events: "Events", raw: "Raw", count: "12 events", foot: (n) => `${n} of 450 events shown Show ${Math.min(200, 450 - n)} more events` },
  zh: { reply: "回复", events: "事件", raw: "原文", count: "12 个事件", foot: (n) => `已显示 ${n} / 450 个事件 再显示 ${Math.min(200, 450 - n)} 个事件` },
};
const view = "#view-gateway";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a streamed reply reads as JSON`, async (t) => {
      assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
      const w = words[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1000, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sse-body.png`) });
        }
        await browser.close();
      });

      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#foldConnect").click(); // Connect away, the calls in view
      const rows = page.locator("#activity .call-item");
      await rows.first().waitFor();
      const body = (i) => rows.nth(i).locator(".call-body").nth(1);
      // The app puts back any scroll of the view that isn't the reader's,
      // Playwright's own to what it clicks included, and the page's footer
      // lies over the view's foot: what is clicked is brought to the middle
      // of the view by the wheel, as the reader would, and clicked where it is.
      const reach = async (loc) => {
        const [dy, x, y] = await loc.evaluate((e) => {
          const v = document.querySelector("#view-gateway").getBoundingClientRect();
          return [e.getBoundingClientRect().top - v.top - v.height / 2, v.left + 6, v.top + v.height / 2];
        });
        await page.mouse.move(x, y);
        await page.mouse.wheel(0, dy);
        let b = await loc.boundingBox();
        for (let i = 0; i < 20; i++) { // till the view is still
          await page.waitForTimeout(120);
          const n = await loc.boundingBox();
          if (n.y === b.y) break;
          b = n;
        }
        return b;
      };
      const press = async (loc) => {
        const b = await reach(loc);
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      const snap = (name) => process.env.ARTIFACT_DIR && fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true })
        .then(() => page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sse-${name}.png`) }));
      const open = async (i) => { await press(rows.nth(i).locator(".call")); await body(i).waitFor(); };
      const pick = press;
      const shown = async (i) => (await body(i).locator("pre").innerText()).trim();

      // Anthropic: the events, each one's data as JSON under its name
      await open(0);
      const res = body(0);
      assert.deepEqual(await res.locator(".segs .opt").allInnerTexts(), [w.reply, w.events, w.raw]);
      assert.equal(await res.locator(".segs .opt.on").innerText(), w.events, "the events are shown first");
      assert.equal((await res.locator(".call-body-count").innerText()).trim(), w.count);
      assert.equal(await res.locator(".sse-ev").count(), 12);
      const first = await res.locator(".sse-ev").first().innerText();
      assert.match(first, /^event: message_start\n\{\n  "type": "message_start",\n  "message": \{\n    "id": "msg_1"/, "pretty-printed JSON, not the data line");
      assert(!(await shown(0)).includes("data: {"), "no raw data lines in the events view");

      // its reply put together: thinking, text and the tool's input as an object
      await reach(res.locator(".segs"));
      let at = await page.locator(view).evaluate((v) => v.scrollTop);
      const pickTop = await res.locator(".segs").evaluate((e) => e.getBoundingClientRect().top);
      await pick(res.locator(".segs .opt", { hasText: w.reply }));
      await page.waitForTimeout(300);
      assert.equal(await page.locator(view).evaluate((v) => v.scrollTop), at, "the switch leaves the page where it was");
      assert.equal(await res.locator(".segs").evaluate((e) => e.getBoundingClientRect().top), pickTop);
      await snap("reply");
      const reply = JSON.parse(await shown(0));
      assert.deepEqual(reply, {
        id: "msg_1", model: "claude-x",
        content: [
          { type: "thinking", thinking: "Let me think." },
          { type: "text", text: "Hello, world" },
          { type: "tool_use", id: "tu_1", name: "Read", input: { path: "a.go" } },
        ],
        stop_reason: "tool_use", usage: { input_tokens: 12, output_tokens: 40 },
      });

      // and as it came
      await pick(res.locator(".segs .opt", { hasText: w.raw }));
      assert.equal(await res.locator("pre code").textContent(), anthropic);

      // the pick holds for the next call opened; Chat Completions' reply,
      // and [DONE] as it is among its events
      await open(1);
      assert.equal(await body(1).locator(".segs .opt.on").innerText(), w.raw);
      await pick(body(1).locator(".segs .opt", { hasText: w.reply }));
      assert.deepEqual(JSON.parse(await shown(1)), {
        id: "c1", model: "gpt-x",
        choices: [
          { type: "message", content: "Hi", reasoning: "hmm" },
          { type: "tool_call", id: "call_1", name: "ls", arguments: { dir: "." } },
        ],
        finish_reason: "tool_calls",
      });
      await pick(body(1).locator(".segs .opt", { hasText: w.events }));
      assert.equal((await body(1).locator(".sse-ev").last().innerText()).trim(), "[DONE]");

      // OpenAI Responses, cut before response.completed: put together from its deltas
      await open(2);
      await pick(body(2).locator(".segs .opt", { hasText: w.reply }));
      assert.deepEqual(JSON.parse(await shown(2)), {
        id: "resp_1", model: "gpt-5", status: "in_progress",
        output: [
          { type: "function_call", call_id: "call_9", name: "shell", arguments: { cmd: "ls" } },
          { type: "message", text: "Done now" },
        ],
      });

      // a long stream draws 200 events, then 200 more on a click, the page still
      await open(3);
      await pick(body(3).locator(".segs .opt", { hasText: w.events }));
      assert.equal(await body(3).locator(".sse-ev").count(), 200);
      const foot = body(3).locator(".call-body-foot");
      assert.equal((await foot.innerText()).replace(/\s+/g, " ").trim(), w.foot(200));
      // what draws more stays where it is as they come, and the page with it
      const was = await reach(foot.locator("button"));
      at = await page.locator(view).evaluate((v) => v.scrollTop);
      await press(foot.locator("button"));
      await page.waitForTimeout(300);
      assert.equal(await body(3).locator(".sse-ev").count(), 400);
      assert.equal(await page.locator(view).evaluate((v) => v.scrollTop), at, "showing more leaves the page where it was");
      assert.equal((await foot.locator("button").boundingBox()).y, was.y, "and the button where it was");
      assert.equal((await foot.innerText()).replace(/\s+/g, " ").trim(), w.foot(400));
      await snap("more");
      await press(foot.locator("button"));
      assert.equal(await body(3).locator(".sse-ev").count(), 450);
      assert(await foot.evaluate((f) => f.hidden), "every event shown, nothing more to draw");

      // the events drawn stay drawn as a new call comes in and the list redraws
      await page.evaluate(() => renderActivity());
      assert.equal(await body(3).locator(".sse-ev").count(), 450);

      // a body that isn't a stream is as it was: JSON, no views
      await open(4);
      assert.equal(await body(4).locator(".segs").count(), 0);
      assert.match(await shown(4), /^\{\n  "id": "x",/);

      // the pick is remembered across a reload
      await page.reload();
      await open(0);
      assert.equal(await body(0).locator(".segs .opt.on").innerText(), w.events);
      assert.deepEqual(errors, []);
    });
  }
}
